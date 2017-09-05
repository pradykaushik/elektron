package schedulers

import (
	"bitbucket.org/sunybingcloud/electron/constants"
	"bitbucket.org/sunybingcloud/electron/def"
	"bitbucket.org/sunybingcloud/electron/rapl"
	"bitbucket.org/sunybingcloud/electron/utilities/mesosUtils"
	"bitbucket.org/sunybingcloud/electron/utilities/offerUtils"
	"errors"
	"fmt"
	"github.com/golang/protobuf/proto"
	mesos "github.com/mesos/mesos-go/mesosproto"
	"github.com/mesos/mesos-go/mesosutil"
	sched "github.com/mesos/mesos-go/scheduler"
	"log"
	"math"
	"os"
	"sync"
	"time"
)

/*
 Piston Capper implements the Scheduler interface

 This is basically extending the BinPacking algorithm to also cap each node at a different values,
  corresponding to the load on that node.
*/
type BinPackedPistonCapper struct {
	base        // Type embedded to inherit common functions
	taskMonitor map[string][]def.Task
	totalPower  map[string]float64
	ticker      *time.Ticker
	isCapping   bool
}

// New electron scheduler.
func NewBinPackedPistonCapper(tasks []def.Task, wattsAsAResource bool, schedTracePrefix string,
	classMapWatts bool) *BinPackedPistonCapper {

	logFile, err := os.Create("./" + schedTracePrefix + "_schedTrace.log")
	if err != nil {
		log.Fatal(err)
	}

	s := &BinPackedPistonCapper{
		base: base{
			tasks:            tasks,
			wattsAsAResource: wattsAsAResource,
			classMapWatts:    classMapWatts,
			Shutdown:         make(chan struct{}),
			Done:             make(chan struct{}),
			PCPLog:           make(chan struct{}),
			running:          make(map[string]map[string]bool),
			RecordPCP:        false,
			schedTrace:       log.New(logFile, "", log.LstdFlags),
		},
		taskMonitor: make(map[string][]def.Task),
		totalPower:  make(map[string]float64),
		ticker:      time.NewTicker(5 * time.Second),
		isCapping:   false,
	}
	return s
}

// check whether task fits the offer or not.
func (s *BinPackedPistonCapper) takeOffer(offer *mesos.Offer, offerWatts float64, offerCPU float64, offerRAM float64,
	totalWatts float64, totalCPU float64, totalRAM float64, task def.Task) bool {
	wattsConsideration, err := def.WattsToConsider(task, s.classMapWatts, offer)
	if err != nil {
		// Error in determining wattsToConsider
		log.Fatal(err)
	}
	if (!s.wattsAsAResource || (offerWatts >= (totalWatts + wattsConsideration))) &&
		(offerCPU >= (totalCPU + task.CPU)) &&
		(offerRAM >= (totalRAM + task.RAM)) {
		return true
	} else {
		return false
	}
}

// mutex
var bpPistonMutex sync.Mutex

func (s *BinPackedPistonCapper) newTask(offer *mesos.Offer, task def.Task) *mesos.TaskInfo {
	taskName := fmt.Sprintf("%s-%d", task.Name, *task.Instances)
	s.tasksCreated++

	if !s.RecordPCP {
		// Turn on logging
		s.RecordPCP = true
		time.Sleep(1 * time.Second) // Make sure we're recording by the time the first task starts
	}

	// If this is our first time running into this Agent
	if _, ok := s.running[offer.GetSlaveId().GoString()]; !ok {
		s.running[offer.GetSlaveId().GoString()] = make(map[string]bool)
	}

	// Setting the task ID to the task. This is done so that we can consider each task to be different,
	// even though they have the same parameters.
	task.SetTaskID(*proto.String("electron-" + taskName))
	// Add task to list of tasks running on node
	s.running[offer.GetSlaveId().GoString()][taskName] = true
	// Adding the task to the taskMonitor
	if len(s.taskMonitor[*offer.Hostname]) == 0 {
		s.taskMonitor[*offer.Hostname] = []def.Task{task}
	} else {
		s.taskMonitor[*offer.Hostname] = append(s.taskMonitor[*offer.Hostname], task)
	}

	resources := []*mesos.Resource{
		mesosutil.NewScalarResource("cpus", task.CPU),
		mesosutil.NewScalarResource("mem", task.RAM),
	}

	if s.wattsAsAResource {
		if wattsToConsider, err := def.WattsToConsider(task, s.classMapWatts, offer); err == nil {
			log.Printf("Watts considered for host[%s] and task[%s] = %f", *offer.Hostname, task.Name, wattsToConsider)
			resources = append(resources, mesosutil.NewScalarResource("watts", wattsToConsider))
		} else {
			// Error in determining wattsConsideration
			log.Fatal(err)
		}
	}

	return &mesos.TaskInfo{
		Name: proto.String(taskName),
		TaskId: &mesos.TaskID{
			Value: proto.String("electron-" + taskName),
		},
		SlaveId:   offer.SlaveId,
		Resources: resources,
		Command: &mesos.CommandInfo{
			Value: proto.String(task.CMD),
		},
		Container: &mesos.ContainerInfo{
			Type: mesos.ContainerInfo_DOCKER.Enum(),
			Docker: &mesos.ContainerInfo_DockerInfo{
				Image:   proto.String(task.Image),
				Network: mesos.ContainerInfo_DockerInfo_BRIDGE.Enum(), // Run everything isolated
			},
		},
	}
}

func (s *BinPackedPistonCapper) Disconnected(sched.SchedulerDriver) {
	// Need to stop the capping process
	s.ticker.Stop()
	bpPistonMutex.Lock()
	s.isCapping = false
	bpPistonMutex.Unlock()
	log.Println("Framework disconnected with master")
}

// go routine to cap the each node in the cluster at regular intervals of time.
var bpPistonCapValues = make(map[string]float64)

// Storing the previous cap value for each host so as to not repeatedly cap the nodes to the same value. (reduces overhead)
var bpPistonPreviousRoundedCapValues = make(map[string]float64)

func (s *BinPackedPistonCapper) startCapping() {
	go func() {
		for {
			select {
			case <-s.ticker.C:
				// Need to cap each node
				bpPistonMutex.Lock()
				for host, capValue := range bpPistonCapValues {
					roundedCapValue := float64(int(math.Floor(capValue + 0.5)))
					// has the cap value changed
					if prevRoundedCap, ok := bpPistonPreviousRoundedCapValues[host]; ok {
						if prevRoundedCap != roundedCapValue {
							if err := rapl.Cap(host, "rapl", roundedCapValue); err != nil {
								log.Println(err)
							} else {
								log.Printf("Capped [%s] at %d", host,
									int(math.Floor(capValue+0.5)))
							}
							bpPistonPreviousRoundedCapValues[host] = roundedCapValue
						}
					} else {
						if err := rapl.Cap(host, "rapl", roundedCapValue); err != nil {
							log.Println(err)
						} else {
							log.Printf("Capped [%s] at %d", host, int(math.Floor(capValue+0.5)))
						}
						bpPistonPreviousRoundedCapValues[host] = roundedCapValue
					}
				}
				bpPistonMutex.Unlock()
			}
		}
	}()
}

// Stop the capping
func (s *BinPackedPistonCapper) stopCapping() {
	if s.isCapping {
		log.Println("Stopping the capping.")
		s.ticker.Stop()
		bpPistonMutex.Lock()
		s.isCapping = false
		bpPistonMutex.Unlock()
	}
}

func (s *BinPackedPistonCapper) ResourceOffers(driver sched.SchedulerDriver, offers []*mesos.Offer) {
	log.Printf("Received %d resource offers", len(offers))

	// retrieving the total power for each host in the offers
	for _, offer := range offers {
		offerUtils.UpdateEnvironment(offer)
		if _, ok := s.totalPower[*offer.Hostname]; !ok {
			_, _, offerWatts := offerUtils.OfferAgg(offer)
			s.totalPower[*offer.Hostname] = offerWatts
		}
	}

	// Displaying the totalPower
	for host, tpower := range s.totalPower {
		log.Printf("TotalPower[%s] = %f", host, tpower)
	}

	/*
		Piston capping strategy

		Perform bin-packing of tasks on nodes in the cluster, making sure that no task is given less hard-limit resources than requested.
		For each set of tasks that are scheduled, compute the new cap values for each host in the cluster.
		At regular intervals of time, cap each node in the cluster.
	*/
	for _, offer := range offers {
		select {
		case <-s.Shutdown:
			log.Println("Done scheduling tasks: declining offer on [", offer.GetHostname(), "]")
			driver.DeclineOffer(offer.Id, mesosUtils.LongFilter)

			log.Println("Number of tasks still running: ", s.tasksRunning)
			continue
		default:
		}

		fitTasks := []*mesos.TaskInfo{}
		offerCPU, offerRAM, offerWatts := offerUtils.OfferAgg(offer)
		offerTaken := false
		totalWatts := 0.0
		totalCPU := 0.0
		totalRAM := 0.0
		// Store the partialLoad for host corresponding to this offer.
		// Once we can't fit any more tasks, we update capValue for this host with partialLoad and then launch the fit tasks.
		partialLoad := 0.0
		for i := 0; i < len(s.tasks); i++ {
			task := s.tasks[i]
			wattsConsideration, err := def.WattsToConsider(task, s.classMapWatts, offer)
			if err != nil {
				// Error in determining wattsConsideration
				log.Fatal(err)
			}

			// Don't take offer if it doesn't match our task's host requirement
			if offerUtils.HostMismatch(*offer.Hostname, task.Host) {
				continue
			}

			for *task.Instances > 0 {
				// Does the task fit
				if s.takeOffer(offer, offerWatts, offerCPU, offerRAM,
					totalWatts, totalCPU, totalRAM, task) {

					// Start piston capping if haven't started yet
					if !s.isCapping {
						s.isCapping = true
						s.startCapping()
					}

					offerTaken = true
					totalWatts += wattsConsideration
					totalCPU += task.CPU
					totalRAM += task.RAM
					log.Println("Co-Located with: ")
					coLocated(s.running[offer.GetSlaveId().GoString()])
					taskToSchedule := s.newTask(offer, task)
					fitTasks = append(fitTasks, taskToSchedule)

					log.Println("Inst: ", *task.Instances)
					s.schedTrace.Print(offer.GetHostname() + ":" + taskToSchedule.GetTaskId().GetValue())
					*task.Instances--
					// updating the cap value for offer.Hostname
					partialLoad += ((wattsConsideration * constants.Tolerance) / s.totalPower[*offer.Hostname]) * 100

					if *task.Instances <= 0 {
						// All instances of task have been scheduled. Remove it
						s.tasks = append(s.tasks[:i], s.tasks[i+1:]...)
						if len(s.tasks) <= 0 {
							log.Println("Done scheduling all tasks")
							close(s.Shutdown)
						}
					}
				} else {
					break // Continue on to next task
				}
			}
		}

		if offerTaken {
			// Updating the cap value for offer.Hostname
			bpPistonMutex.Lock()
			bpPistonCapValues[*offer.Hostname] += partialLoad
			bpPistonMutex.Unlock()
			log.Printf("Starting on [%s]\n", offer.GetHostname())
			driver.LaunchTasks([]*mesos.OfferID{offer.Id}, fitTasks, mesosUtils.DefaultFilter)
		} else {
			// If there was no match for task
			log.Println("There is not enough resources to launch task: ")
			cpus, mem, watts := offerUtils.OfferAgg(offer)

			log.Printf("<CPU: %f, RAM: %f, Watts: %f>\n", cpus, mem, watts)
			driver.DeclineOffer(offer.Id, mesosUtils.DefaultFilter)
		}
	}
}

// Remove finished task from the taskMonitor
func (s *BinPackedPistonCapper) deleteFromTaskMonitor(finishedTaskID string) (def.Task, string, error) {
	hostOfFinishedTask := ""
	indexOfFinishedTask := -1
	found := false
	var finishedTask def.Task

	for host, tasks := range s.taskMonitor {
		for i, task := range tasks {
			if task.TaskID == finishedTaskID {
				hostOfFinishedTask = host
				indexOfFinishedTask = i
				found = true
			}
		}
		if found {
			break
		}
	}

	if hostOfFinishedTask != "" && indexOfFinishedTask != -1 {
		finishedTask = s.taskMonitor[hostOfFinishedTask][indexOfFinishedTask]
		log.Printf("Removing task with TaskID [%s] from the list of running tasks\n",
			s.taskMonitor[hostOfFinishedTask][indexOfFinishedTask].TaskID)
		s.taskMonitor[hostOfFinishedTask] = append(s.taskMonitor[hostOfFinishedTask][:indexOfFinishedTask],
			s.taskMonitor[hostOfFinishedTask][indexOfFinishedTask+1:]...)
	} else {
		return finishedTask, hostOfFinishedTask, errors.New("Finished Task not present in TaskMonitor")
	}
	return finishedTask, hostOfFinishedTask, nil
}

func (s *BinPackedPistonCapper) StatusUpdate(driver sched.SchedulerDriver, status *mesos.TaskStatus) {
	log.Printf("Received task status [%s] for task [%s]\n", NameFor(status.State), *status.TaskId.Value)

	if *status.State == mesos.TaskState_TASK_RUNNING {
		bpPistonMutex.Lock()
		s.tasksRunning++
		bpPistonMutex.Unlock()
	} else if IsTerminal(status.State) {
		delete(s.running[status.GetSlaveId().GoString()], *status.TaskId.Value)
		// Deleting the task from the taskMonitor
		finishedTask, hostOfFinishedTask, err := s.deleteFromTaskMonitor(*status.TaskId.Value)
		if err != nil {
			log.Println(err)
		}

		// Need to determine the watts consideration for the finishedTask
		var wattsConsideration float64
		if s.classMapWatts {
			wattsConsideration = finishedTask.ClassToWatts[hostToPowerClass(hostOfFinishedTask)]
		} else {
			wattsConsideration = finishedTask.Watts
		}
		// Need to update the cap values for host of the finishedTask
		bpPistonMutex.Lock()
		bpPistonCapValues[hostOfFinishedTask] -= ((wattsConsideration * constants.Tolerance) / s.totalPower[hostOfFinishedTask]) * 100
		// Checking to see if the cap value has become 0, in which case we uncap the host.
		if int(math.Floor(bpPistonCapValues[hostOfFinishedTask]+0.5)) == 0 {
			bpPistonCapValues[hostOfFinishedTask] = 100
		}
		s.tasksRunning--
		bpPistonMutex.Unlock()

		if s.tasksRunning == 0 {
			select {
			case <-s.Shutdown:
				s.stopCapping()
				close(s.Done)
			default:
			}
		}
	}
	log.Printf("DONE: Task status [%s] for task [%s]", NameFor(status.State), *status.TaskId.Value)
}