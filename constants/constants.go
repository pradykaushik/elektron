/*
Constants that are used across scripts
1. The available hosts = stratos-00x (x varies from 1 to 8)
2. Tolerance = tolerance for a task that when exceeded would starve the task.
3. ConsiderationWindowSize = number of tasks to consider for computation of the dynamic cap.
TODO: Clean this up and use Mesos Attributes instead.
*/
package constants

var Hosts = make(map[string]struct{})

/*
 Classification of the nodes in the cluster based on their Thermal Design Power (TDP).
 The power classes are labelled in the decreasing order of the corresponding TDP, with class A nodes
 	having the highest TDP and class C nodes having the lowest TDP.
*/
var PowerClasses = make(map[string]map[string]struct{})

/*
  Margin with respect to the required power for a job.
  So, if power required = 10W, the node would be capped to Tolerance * 10W.
  This value can be changed upon convenience.
*/
var Tolerance = 0.70

// Window size for running average
var ConsiderationWindowSize = 20

// Threshold below which a host should be capped
var LowerCapLimit = 12.5