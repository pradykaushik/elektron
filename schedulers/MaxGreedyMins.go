// Copyright (C) 2018 spdfg
// 
// This file is part of Elektron.
// 
// Elektron is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
// 
// Elektron is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
// 
// You should have received a copy of the GNU General Public License
// along with Elektron.  If not, see <http://www.gnu.org/licenses/>.
// 

package schedulers

import (
	"log"

	mesos "github.com/mesos/mesos-go/api/v0/mesosproto"
	sched "github.com/mesos/mesos-go/api/v0/scheduler"
	"github.com/spdfg/elektron/def"
	"github.com/spdfg/elektron/utilities/mesosUtils"
	"github.com/spdfg/elektron/utilities/offerUtils"
)

// Decides if to take an offer or not
func (s *MaxGreedyMins) takeOffer(spc SchedPolicyContext, offer *mesos.Offer, task def.Task,
	totalCPU, totalRAM, totalWatts float64) bool {
	baseSchedRef := spc.(*BaseScheduler)
	cpus, mem, watts := offerUtils.OfferAgg(offer)

	//TODO: Insert watts calculation here instead of taking them as a parameter

	wattsConsideration, err := def.WattsToConsider(task, baseSchedRef.classMapWatts, offer)
	if err != nil {
		// Error in determining wattsConsideration
		log.Fatal(err)
	}
	if (cpus >= (totalCPU + task.CPU)) && (mem >= (totalRAM + task.RAM)) &&
		(!baseSchedRef.wattsAsAResource || (watts >= (totalWatts + wattsConsideration))) {
		return true
	}
	return false
}

type MaxGreedyMins struct {
	baseSchedPolicyState
}

// Determine if the remaining space inside of the offer is enough for this
// the task we need to create. If it is, create a TaskInfo and return it.
func (s *MaxGreedyMins) CheckFit(
	spc SchedPolicyContext,
	i int,
	task def.Task,
	wattsConsideration float64,
	offer *mesos.Offer,
	totalCPU *float64,
	totalRAM *float64,
	totalWatts *float64) (bool, *mesos.TaskInfo) {

	baseSchedRef := spc.(*BaseScheduler)
	// Does the task fit
	if s.takeOffer(spc, offer, task, *totalCPU, *totalRAM, *totalWatts) {

		*totalWatts += wattsConsideration
		*totalCPU += task.CPU
		*totalRAM += task.RAM
		baseSchedRef.LogCoLocatedTasks(offer.GetSlaveId().GoString())

		taskToSchedule := baseSchedRef.newTask(offer, task)

		baseSchedRef.LogSchedTrace(taskToSchedule, offer)
		*task.Instances--
		s.numTasksScheduled++

		if *task.Instances <= 0 {
			// All instances of task have been scheduled, remove it
			baseSchedRef.tasks = append(baseSchedRef.tasks[:i], baseSchedRef.tasks[i+1:]...)

			if len(baseSchedRef.tasks) <= 0 {
				baseSchedRef.LogTerminateScheduler()
				close(baseSchedRef.Shutdown)
			}
		}

		return true, taskToSchedule
	}

	return false, nil
}

func (s *MaxGreedyMins) ConsumeOffers(spc SchedPolicyContext, driver sched.SchedulerDriver, offers []*mesos.Offer) {
	baseSchedRef := spc.(*BaseScheduler)
	if baseSchedRef.schedPolSwitchEnabled {
		SortNTasks(baseSchedRef.tasks, baseSchedRef.numTasksInSchedWindow, def.SortByWatts)
	} else {
		def.SortTasks(baseSchedRef.tasks, def.SortByWatts)
	}
	baseSchedRef.LogOffersReceived(offers)

	for _, offer := range offers {
		offerUtils.UpdateEnvironment(offer)
		select {
		case <-baseSchedRef.Shutdown:
			baseSchedRef.LogNoPendingTasksDeclineOffers(offer)
			driver.DeclineOffer(offer.Id, mesosUtils.LongFilter)
			baseSchedRef.LogNumberOfRunningTasks()
			continue
		default:
		}

		tasks := []*mesos.TaskInfo{}

		offerTaken := false
		totalWatts := 0.0
		totalCPU := 0.0
		totalRAM := 0.0

		// Assumes s.tasks is ordered in non-decreasing median max peak order

		// Attempt to schedule a single instance of the heaviest workload available first
		// Start from the back until one fits
		for i := len(baseSchedRef.tasks) - 1; i >= 0; i-- {
			// If scheduling policy switching enabled, then
			// stop scheduling if the #baseSchedRef.schedWindowSize tasks have been scheduled.
			if baseSchedRef.schedPolSwitchEnabled && (s.numTasksScheduled >= baseSchedRef.schedWindowSize) {
				break // Offers will automatically get declined.
			}
			task := baseSchedRef.tasks[i]
			wattsConsideration, err := def.WattsToConsider(task, baseSchedRef.classMapWatts, offer)
			if err != nil {
				// Error in determining wattsConsideration
				log.Fatal(err)
			}

			// Don't take offer if it doesn't match our task's host requirement
			if offerUtils.HostMismatch(*offer.Hostname, task.Host) {
				continue
			}

			// TODO: Fix this so index doesn't need to be passed
			taken, taskToSchedule := s.CheckFit(spc, i, task, wattsConsideration, offer,
				&totalCPU, &totalRAM, &totalWatts)

			if taken {
				offerTaken = true
				tasks = append(tasks, taskToSchedule)
				break
			}
		}

		// Pack the rest of the offer with the smallest tasks
		for i := 0; i < len(baseSchedRef.tasks); i++ {
			task := baseSchedRef.tasks[i]
			wattsConsideration, err := def.WattsToConsider(task, baseSchedRef.classMapWatts, offer)
			if err != nil {
				// Error in determining wattsConsideration
				log.Fatal(err)
			}

			// Don't take offer if it doesn't match our task's host requirement
			if offerUtils.HostMismatch(*offer.Hostname, task.Host) {
				continue
			}

			for *task.Instances > 0 {
				// If scheduling policy switching enabled, then
				// stop scheduling if the #baseSchedRef.schedWindowSize tasks have been scheduled.
				if baseSchedRef.schedPolSwitchEnabled && (s.numTasksScheduled >= baseSchedRef.schedWindowSize) {
					break // Offers will automatically get declined.
				}
				// TODO: Fix this so index doesn't need to be passed
				taken, taskToSchedule := s.CheckFit(spc, i, task, wattsConsideration, offer,
					&totalCPU, &totalRAM, &totalWatts)

				if taken {
					offerTaken = true
					tasks = append(tasks, taskToSchedule)
				} else {
					break // Continue on to next task
				}
			}
		}

		if offerTaken {
			baseSchedRef.LogTaskStarting(nil, offer)
			LaunchTasks([]*mesos.OfferID{offer.Id}, tasks, driver)
		} else {

			// If there was no match for the task
			cpus, mem, watts := offerUtils.OfferAgg(offer)
			baseSchedRef.LogInsufficientResourcesDeclineOffer(offer, cpus, mem, watts)
			driver.DeclineOffer(offer.Id, mesosUtils.DefaultFilter)
		}
	}
}
