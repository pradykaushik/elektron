package main

import (
	"flag"
	"fmt"
	"github.com/golang/protobuf/proto"
	mesos "github.com/mesos/mesos-go/mesosproto"
	"github.com/mesos/mesos-go/mesosutil"
	sched "github.com/mesos/mesos-go/scheduler"
	"log"
	"os"
	"time"
)

const (
	shutdownTimeout = time.Duration(30) * time.Second
)

var (
	defaultFilter = &mesos.Filters{RefuseSeconds: proto.Float64(1)}
	longFilter = &mesos.Filters{RefuseSeconds: proto.Float64(1000)}
)

func CoLocated(tasks map[string]bool) {

	for task := range tasks {
		log.Println(task)
	}

	fmt.Println("---------------------")
}

func OfferAgg(offer *mesos.Offer) (float64, float64, float64) {
	var cpus, mem, watts float64

	for _, resource := range offer.Resources {
		switch resource.GetName() {
		case "cpus":
			cpus += *resource.GetScalar().Value
		case "mem":
			mem += *resource.GetScalar().Value
		case "watts":
			watts += *resource.GetScalar().Value
		}
	}

	return cpus, mem, watts
}

// Decides if to take an offer or not
func TakeOffer(offer *mesos.Offer, task Task) bool {

	cpus, mem, watts := OfferAgg(offer)

	//TODO: Insert watts calculation here instead of taking them as a parameter

	if cpus >= task.CPU  && mem >= task.RAM && watts >= task.Watts {
		return true
	}

	return false
}

// rendlerScheduler implements the Scheduler interface and stores
// the state needed for Rendler to function.
type electronScheduler struct {
	tasksCreated int
	tasksRunning int
	tasks []Task
	metrics map[string]Metric
	running map[string]map[string]bool


	// This channel is closed when the program receives an interrupt,
	// signalling that the program should shut down.
	shutdown chan struct{}
	// This channel is closed after shutdown is closed, and only when all
	// outstanding tasks have been cleaned up
	done chan struct{}
}

// New electron scheduler
func newElectronScheduler(tasks []Task) *electronScheduler {

	s := &electronScheduler{
		tasks: tasks,
		shutdown: make(chan struct{}),
		done:     make(chan struct{}),
		running: make(map[string]map[string]bool),
	}
	return s
}

func (s *electronScheduler) newTask(offer *mesos.Offer, task Task) *mesos.TaskInfo {
	taskID := fmt.Sprintf("Electron-%s-%d", task.Name, *task.Instances)
	s.tasksCreated++

	// If this is our first time running into this Agent
	if _, ok := s.running[offer.GetSlaveId().GoString()]; !ok {
		s.running[offer.GetSlaveId().GoString()] = make(map[string]bool)
	}

	// Add task to list of tasks running on node
	s.running[offer.GetSlaveId().GoString()][taskID] = true

	return &mesos.TaskInfo{
		Name: proto.String(taskID),
		TaskId: &mesos.TaskID{
			Value: proto.String(taskID),
		},
		SlaveId: offer.SlaveId,
		Resources: []*mesos.Resource{
			mesosutil.NewScalarResource("cpus", task.CPU),
			mesosutil.NewScalarResource("mem", task.RAM),
			mesosutil.NewScalarResource("watts", task.Watts),
		},
		Command: &mesos.CommandInfo{
			Value: proto.String(task.CMD),
		},
		Container: &mesos.ContainerInfo{
			Type: mesos.ContainerInfo_DOCKER.Enum(),
			Docker: &mesos.ContainerInfo_DockerInfo{
			Image: proto.String(task.Image),
			},

		},
	}
}

func (s *electronScheduler) Registered(
	_ sched.SchedulerDriver,
	frameworkID *mesos.FrameworkID,
	masterInfo *mesos.MasterInfo) {
	log.Printf("Framework %s registered with master %s", frameworkID, masterInfo)
}

func (s *electronScheduler) Reregistered(_ sched.SchedulerDriver, masterInfo *mesos.MasterInfo) {
	log.Printf("Framework re-registered with master %s", masterInfo)
}

func (s *electronScheduler) Disconnected(sched.SchedulerDriver) {
	log.Println("Framework disconnected with master")
}

func (s *electronScheduler) ResourceOffers(driver sched.SchedulerDriver, offers []*mesos.Offer) {
	log.Printf("Received %d resource offers", len(offers))

	for _, offer := range offers {
		select {
		case <-s.shutdown:
			log.Println("Shutting down: declining offer on [", offer.GetHostname(), "]")
			driver.DeclineOffer(offer.Id, longFilter)

			log.Println("Number of tasks still running: ", s.tasksRunning)
			continue
		default:
		}


		tasks := []*mesos.TaskInfo{}

		// First fit strategy

		taken := false
		for i, task := range s.tasks {
			// Decision to take the offer or not
			if TakeOffer(offer, task) {

				log.Println("Co-Located with: ")
				CoLocated(s.running[offer.GetSlaveId().GoString()])

				tasks = append(tasks, s.newTask(offer, task))

				log.Printf("Starting %s on [%s]\n", task.Name, offer.GetHostname())
				driver.LaunchTasks([]*mesos.OfferID{offer.Id}, tasks, defaultFilter)

				taken = true

				fmt.Println("Inst: ", *task.Instances)
				*task.Instances--

				if *task.Instances <= 0 {
					// All instances of task have been scheduled, remove it
					s.tasks[i] = s.tasks[len(s.tasks)-1]
					s.tasks = s.tasks[:len(s.tasks)-1]


					if(len(s.tasks) <= 0) {
						log.Println("Done scheduling all tasks")
						close(s.shutdown)
					}
				}

				break // Offer taken, move on

			}
		}

		// If there was no match for the task
		if !taken {
			fmt.Println("There is not enough resources to launch a task:")
			cpus, mem, watts := OfferAgg(offer)

			log.Printf("<CPU: %f, RAM: %f, Watts: %f>\n", cpus, mem, watts)
			driver.DeclineOffer(offer.Id, defaultFilter)
		}

	}
}

func (s *electronScheduler) StatusUpdate(driver sched.SchedulerDriver, status *mesos.TaskStatus) {
	log.Printf("Received task status [%s] for task [%s]", NameFor(status.State), *status.TaskId.Value)

	if *status.State == mesos.TaskState_TASK_RUNNING {
		s.tasksRunning++
	} else if IsTerminal(status.State) {
		delete(s.running[status.GetSlaveId().GoString()],*status.TaskId.Value)
		s.tasksRunning--
		if s.tasksRunning == 0 {
			select {
			case <-s.shutdown:
				close(s.done)
			default:
			}
		}
	}
	log.Printf("DONE: Task status [%s] for task [%s]", NameFor(status.State), *status.TaskId.Value)
}

func (s *electronScheduler) FrameworkMessage(
	driver sched.SchedulerDriver,
	executorID *mesos.ExecutorID,
	slaveID *mesos.SlaveID,
	message string) {

	log.Println("Getting a framework message: ", message)
	log.Printf("Received a framework message from some unknown source: %s", *executorID.Value)
}

func (s *electronScheduler) OfferRescinded(_ sched.SchedulerDriver, offerID *mesos.OfferID) {
	log.Printf("Offer %s rescinded", offerID)
}
func (s *electronScheduler) SlaveLost(_ sched.SchedulerDriver, slaveID *mesos.SlaveID) {
	log.Printf("Slave %s lost", slaveID)
}
func (s *electronScheduler) ExecutorLost(_ sched.SchedulerDriver, executorID *mesos.ExecutorID, slaveID *mesos.SlaveID, status int) {
	log.Printf("Executor %s on slave %s was lost", executorID, slaveID)
}

func (s *electronScheduler) Error(_ sched.SchedulerDriver, err string) {
	log.Printf("Receiving an error: %s", err)
}

func main() {
	master := flag.String("master", "xavier:5050", "Location of leading Mesos master")
	tasksFile := flag.String("tasks", "", "JSON file containing task definitions")
	flag.Parse()


	if *tasksFile == "" {
		fmt.Println("No file containing tasks specifiction provided.")
		os.Exit(1)
	}

	tasks, err := TasksFromJSON(*tasksFile)
	if(err != nil || len(tasks) == 0) {
		fmt.Println("Invalid tasks specification file provided")
		os.Exit(1)
	}

	log.Println("Scheduling the following tasks:")
	for _, task := range tasks {
		fmt.Println(task)
	}

	scheduler := newElectronScheduler(tasks)
	driver, err := sched.NewMesosSchedulerDriver(sched.DriverConfig{
		Master: *master,
		Framework: &mesos.FrameworkInfo{
			Name: proto.String("Electron"),
			User: proto.String(""),
		},
		Scheduler: scheduler,
	})
	if err != nil {
		log.Printf("Unable to create scheduler driver: %s", err)
		return
	}

	// Catch interrupt
	go func() {

		select {
		case <-scheduler.shutdown:
		//			case <-time.After(shutdownTimeout):
		}

		select {
		case <-scheduler.done:
//			case <-time.After(shutdownTimeout):
		}

		// Done shutting down
		driver.Stop(false)

	}()

	log.Printf("Starting...")
	if status, err := driver.Run(); err != nil {
		log.Printf("Framework stopped with status %s and error: %s\n", status.String(), err.Error())
	}
	log.Println("Exiting...")
}
