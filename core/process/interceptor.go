package process

import "github.com/TIBCOSoftware/flogo-lib/core/data"

// Interceptor contains a set of task interceptor, this can be used to override
// runtime data of an instance of the corresponding Process.  This can be used to
// modify runtime execution of a process or in test/debug for implemeting mocks
// for tasks
type Interceptor struct {
	TaskInterceptors []*TaskInterceptor `json:"tasks"`

	taskInterceptorMap map[int]*TaskInterceptor
}

// Init initializes the ProcessInterceptor, usually called afer deserialization
func (pi *Interceptor) Init() {

	numAttrs := len(pi.TaskInterceptors)
	if numAttrs > 0 {

		pi.taskInterceptorMap = make(map[int]*TaskInterceptor, numAttrs)

		for _, interceptor := range pi.TaskInterceptors {
			pi.taskInterceptorMap[interceptor.ID] = interceptor
		}
	}
}

// GetTaskInterceptor get the TaskInterceptor for the specified task (reffered to by ID)
func (pi *Interceptor) GetTaskInterceptor(taskID int) *TaskInterceptor {
	return pi.taskInterceptorMap[taskID]
}

// TaskInterceptor contains instance override information for a Task, such has attributes.
// Also, a 'Skip' flag can be enabled to inform the runtime that the task should not
// execute.
type TaskInterceptor struct {
	ID      int               `json:"id"`
	Skip    bool              `json:"skip,omitempty"`
	Inputs  []*data.Attribute `json:"inputs,omitempty"`
	Outputs []*data.Attribute `json:"outputs,omitempty"`
}