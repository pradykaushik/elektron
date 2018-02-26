package schedulers

import (
	mesos "github.com/mesos/mesos-go/api/v0/mesosproto"
	sched "github.com/mesos/mesos-go/api/v0/scheduler"
	"math/rand"
)

type SchedPolicyContext interface {
	// Change the state of scheduling.
	SwitchSchedPol(s SchedPolicyState)
}

type SchedPolicyState interface {
	// Define the particular scheduling policy's methodology of resource offer consumption.
	ConsumeOffers(SchedPolicyContext, sched.SchedulerDriver, []*mesos.Offer)
}

type baseSchedPolicyState struct {
	SchedPolicyState
	// Keep track of the number of tasks that have been scheduled.
	numTasksScheduled int
	// Distribution of tasks that the scheduling policy is most appropriate for.
	// This distribution corresponds to the ratio of low power consuming tasks to
	// high power consuming tasks.
	TaskDistribution float64 `json:"taskDist"`
	// The average variance in cpu-share per task that this scheduling policy can cause.
	// Note: This number corresponds to a given workload.
	VarianceCpuSharePerTask float64 `json:"varCpuShare"`
}

func (bsps *baseSchedPolicyState) switchIfNecessary(spc SchedPolicyContext) {
	baseSchedRef := spc.(*BaseScheduler)
	// Switch scheduling policy only if feature enabled from CLI
	if baseSchedRef.schedPolSwitchEnabled {
		// Need to recompute size of the scheduling window for the next offer cycle.
		// The next scheduling policy will schedule at max schedWindowSize number of tasks.
		baseSchedRef.schedWindowSize, baseSchedRef.numTasksInSchedWindow =
			baseSchedRef.schedWindowResStrategy.Apply(func() interface{} { return baseSchedRef.tasks })
		// Switching to a random scheduling policy.
		// TODO: Switch based on some criteria.
		index := rand.Intn(len(SchedPolicies))
		for k, v := range SchedPolicies {
			if index == 0 {
				baseSchedRef.LogSchedPolicySwitch(k, v)
				spc.SwitchSchedPol(v)
				// Resetting the number of tasks scheduled.
				bsps.numTasksScheduled = 0
				break
			}
			index--
		}
	}
}
