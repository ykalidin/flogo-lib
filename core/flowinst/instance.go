package flowinst

import (
	"strconv"
	"sync"

	"github.com/TIBCOSoftware/flogo-lib/core/data"
	"github.com/TIBCOSoftware/flogo-lib/core/ext/activity"
	"github.com/TIBCOSoftware/flogo-lib/core/ext/model"
	"github.com/TIBCOSoftware/flogo-lib/core/flow"
	"github.com/TIBCOSoftware/flogo-lib/util"
	"github.com/op/go-logging"
)

var log = logging.MustGetLogger("instance")

// Instance is a structure for representing an instance of a Flow
type Instance struct {
	id          string
	stepID      int
	lock        sync.Mutex
	status      Status
	state       int
	FlowURI     string
	Flow        *flow.Definition
	RootTaskEnv *TaskEnv
	FlowModel   *model.FlowModel
	Attrs       map[string]*data.Attribute
	Patch       *flow.Patch
	Interceptor *flow.Interceptor

	WorkItemQueue *util.SyncQueue //todo: change to faster non-threadsafe queue

	wiCounter     int
	ChangeTracker *InstanceChangeTracker `json:"-"`

	flowProvider flow.Provider
}

// NewFlowInstance creates a new Flow Instance from the specified Flow
func NewFlowInstance(instanceID string, flowURI string, flow *flow.Definition) *Instance {

	var instance Instance
	instance.id = instanceID
	instance.stepID = 0
	instance.FlowURI = flowURI
	instance.Flow = flow
	instance.FlowModel = model.Get(flow.ModelID())
	instance.status = StatusNotStarted
	instance.WorkItemQueue = util.NewSyncQueue()
	instance.ChangeTracker = NewInstanceChangeTracker()

	var taskEnv TaskEnv
	taskEnv.ID = 1
	taskEnv.Task = flow.RootTask()
	taskEnv.taskID = flow.RootTask().ID()
	taskEnv.Instance = &instance
	taskEnv.TaskDatas = make(map[int]*TaskData)
	taskEnv.LinkDatas = make(map[int]*LinkData)

	instance.RootTaskEnv = &taskEnv

	return &instance
}

// SetFlowProvider sets the proces.Provider that the instance should use
func (pi *Instance) SetFlowProvider(provider flow.Provider) {
	pi.flowProvider = provider
}

// Restart indicates that this FlowInstance was restarted
func (pi *Instance) Restart(id string, provider flow.Provider) {
	pi.id = id
	pi.flowProvider = provider
	pi.Flow = pi.flowProvider.GetFlow(pi.FlowURI)
	pi.FlowModel = model.Get(pi.Flow.ModelID())
	pi.RootTaskEnv.init(pi)
}

// ID returns the ID of the Flow Instance
func (pi *Instance) ID() string {
	return pi.id
}

// FlowDefinition returns the Flow that the instance is of
func (pi *Instance) FlowDefinition() *flow.Definition {
	return pi.Flow
}

// StepID returns the current step ID of the Flow Instance
func (pi *Instance) StepID() int {
	return pi.stepID
}

// Status returns the current status of the Flow Instance
func (pi *Instance) Status() Status {
	return pi.status
}

func (pi *Instance) setStatus(status Status) {

	pi.status = status
	pi.ChangeTracker.SetStatus(status)
}

// State returns the state indicator of the Flow Instance
func (pi *Instance) State() int {
	return pi.state
}

// SetState sets the state indicator of the Flow Instance
func (pi *Instance) SetState(state int) {
	pi.state = state
	pi.ChangeTracker.SetState(state)
}

// UpdateAttrs updates the attributes of the Flow Instance
func (pi *Instance) UpdateAttrs(update map[string]interface{}) {

	if update != nil {

		log.Debugf("Updating flow attrs: %v", update)

		if pi.Attrs == nil {
			pi.Attrs = make(map[string]*data.Attribute, len(update))
		}

		for k, v := range update {
			pi.Attrs[k] = &data.Attribute{Name: k, Type: "string", Value: v}
		}
	}
}

// Start will start the Flow Instance, returns a boolean indicating
// if it was able to start
func (pi *Instance) Start(flowData map[string]interface{}) bool {

	pi.setStatus(StatusActive)
	pi.UpdateAttrs(flowData)

	log.Infof("FlowInstance Flow: %v", pi.FlowModel)
	model := pi.FlowModel.GetFlowBehavior(pi.Flow.TypeID())

	//todo: error if model not found

	ok, evalCode := model.Start(pi)

	if ok {
		rootTaskData := pi.RootTaskEnv.NewTaskData(pi.Flow.RootTask())

		pi.scheduleEval(rootTaskData, evalCode)
	}

	return ok
}

//Resume resumes a Flow Instance
func (pi *Instance) Resume(flowData map[string]interface{}) bool {

	model := pi.FlowModel.GetFlowBehavior(pi.Flow.TypeID())

	pi.setStatus(StatusActive)
	pi.UpdateAttrs(flowData)

	return model.Resume(pi)
}

// DoStep performs a single execution 'step' of the Flow Instance
func (pi *Instance) DoStep() bool {

	hasNext := false

	pi.ResetChanges()

	pi.stepID++

	if pi.status == StatusActive {

		item, ok := pi.WorkItemQueue.Pop()

		if ok {
			log.Debug("popped item off queue")

			workItem := item.(*WorkItem)

			pi.ChangeTracker.trackWorkItem(&WorkItemQueueChange{ChgType: CtDel, ID: workItem.ID, WorkItem: workItem})

			pi.execTask(workItem)
			hasNext = true
		} else {
			log.Debug("queue emtpy")
		}
	}

	return hasNext
}

// GetChanges returns the Change Tracker object
func (pi *Instance) GetChanges() *InstanceChangeTracker {
	return pi.ChangeTracker
}

// ResetChanges resets an changes that were being tracked
func (pi *Instance) ResetChanges() {

	if pi.ChangeTracker != nil {
		pi.ChangeTracker.ResetChanges()
	}

	//todo: can we reuse this to avoid gc
	pi.ChangeTracker = NewInstanceChangeTracker()
}

func (pi *Instance) scheduleEval(taskData *TaskData, evalCode int) {

	pi.wiCounter++

	workItem := NewWorkItem(pi.wiCounter, taskData, EtEval, evalCode)
	log.Debugf("Scheduling EVAL on task: %s\n", taskData.task.Name())

	pi.WorkItemQueue.Push(workItem)
	pi.ChangeTracker.trackWorkItem(&WorkItemQueueChange{ChgType: CtAdd, ID: workItem.ID, WorkItem: workItem})
}

// execTask executes the specified Work Item of the Flow Instance
func (pi *Instance) execTask(workItem *WorkItem) {

	taskData := workItem.TaskData

	taskBehavior := pi.FlowModel.GetTaskBehavior(taskData.task.TypeID())

	var done bool
	var doneCode int

	//todo: should validate process activities

	if workItem.ExecType == EtEval {

		eval := true

		if taskData.HasAttrs() {

			applyInputMapper(pi, taskData)
			eval = applyInputInterceptor(pi, taskData)
		}

		if eval {
			done, doneCode = taskBehavior.Eval(taskData, workItem.EvalCode)
		} else {
			done = true
		}
	} else {
		done, doneCode = taskBehavior.PostEval(taskData, workItem.EvalCode, nil)
	}

	if done {

		if taskData.HasAttrs() {
			applyOutputInterceptor(pi, taskData)

			appliedMapper := applyOutputMapper(pi, taskData)

			if !appliedMapper && !taskData.task.IsScope() {

				log.Debug("Applying Default Output Mapping")
				activity, _ := taskData.Activity()

				attrNS := "[T" + strconv.Itoa(taskData.task.ID()) + "."

				for _, attr := range activity.Metadata().Outputs {

					attrValue, _ := taskData.OutputScope().GetAttrValue(attr.Name)
					pi.AddAttr(attrNS+attr.Name+"]", attr.Type, attrValue)
				}
			}
		}

		pi.handleTaskDone(taskBehavior, taskData, doneCode)
	}
}

// handleTaskDone handles the compeletion of a task in the Flow Instance
func (pi *Instance) handleTaskDone(taskBehavior model.TaskBehavior, taskData *TaskData, doneCode int) {

	notifyParent, childDoneCode, taskEntries := taskBehavior.Done(taskData, doneCode)

	task := taskData.Task()

	if notifyParent {

		parentTask := task.Parent()

		if parentTask != nil {
			parentTaskData := taskData.taskEnv.TaskDatas[parentTask.ID()]
			parentBehavior := pi.FlowModel.GetTaskBehavior(parentTask.TypeID())
			parentDone, parentDoneCode := parentBehavior.ChildDone(parentTaskData, task, childDoneCode)

			if parentDone {
				pi.handleTaskDone(parentBehavior, parentTaskData, parentDoneCode)
			}

		} else {
			//Root Task is Done, so notify Flow
			flowBehavior := pi.FlowModel.GetFlowBehavior(pi.Flow.TypeID())
			flowBehavior.TasksDone(pi, childDoneCode)
			flowBehavior.Done(pi)

			pi.setStatus(StatusCompleted)
		}
	}

	if len(taskEntries) > 0 {

		for _, taskEntry := range taskEntries {

			log.Debugf("execTask - TaskEntry: %v\n", taskEntry)
			taskToEnterBehavior := pi.FlowModel.GetTaskBehavior(taskEntry.Task.TypeID())

			enterTaskData, _ := taskData.taskEnv.FindOrCreateTaskData(taskEntry.Task)

			eval, evalCode := taskToEnterBehavior.Enter(enterTaskData, taskEntry.EnterCode)

			if eval {
				pi.scheduleEval(enterTaskData, evalCode)
			}
		}
	}

	taskData.taskEnv.releaseTask(task)
}

// GetAttrType implements api.Scope.GetAttrType
func (pi *Instance) GetAttrType(attrName string) (attrType string, exists bool) {

	if pi.Attrs != nil {
		attr, found := pi.Attrs[attrName]
		if found {
			return attr.Type, true
		}
	}

	attr, found := pi.Flow.GetAttr(attrName)
	if found {
		return attr.Type, true
	}

	return "", false
}

// GetAttrValue implements api.Scope.GetAttrValue
func (pi *Instance) GetAttrValue(attrName string) (value interface{}, exists bool) {

	log.Debugf("fi.GetAttrValue: %v", pi.Attrs)
	log.Debugf("fi.AttrName: %s", attrName)

	if pi.Attrs != nil {
		attr, found := pi.Attrs[attrName]

		log.Debugf("fi.Attr: %v", attr)

		if found {
			return attr.Value, true
		}
	}

	attr, found := pi.Flow.GetAttr(attrName)
	if found {
		return attr.Value, true
	}

	return "", false
}

// SetAttrValue implements api.Scope.SetAttrValue
func (pi *Instance) SetAttrValue(attrName string, value interface{}) {
	if pi.Attrs == nil {
		pi.Attrs = make(map[string]*data.Attribute)
	}

	attrType, exists := pi.GetAttrType(attrName)

	if exists {
		pi.Attrs[attrName] = &data.Attribute{Name: attrName, Type: attrType, Value: value}
	}
	// else what do we do if its a completely new attr
}

// AddAttr add a new attribute to the instance
func (pi *Instance) AddAttr(attrName string, attrType string, value interface{}) {
	if pi.Attrs == nil {
		pi.Attrs = make(map[string]*data.Attribute)
	}

	log.Debugf("AddAttr name: %s, type: %s, value:%v\n", attrName, attrType, value)

	_, exists := pi.GetAttrType(attrName)

	if exists {
		// what should we do?
	} else {
		pi.Attrs[attrName] = &data.Attribute{Name: attrName, Type: attrType, Value: value}
	}

	log.Debugf("flow attrs: %v\n", pi.Attrs)
}

////////////////////////////////////////////////////////////////////////////////////////////////////////
// Task Environment

// TaskEnv is a structure that describes the execution enviroment for a set of tasks
type TaskEnv struct {
	ID        int
	Task      *flow.Task
	Instance  *Instance
	ParentEnv *TaskEnv

	TaskDatas map[int]*TaskData
	LinkDatas map[int]*LinkData

	taskID int // for deserialization
}

// init initializes the Task Environment, typically called on deserialization
func (te *TaskEnv) init(flowInst *Instance) {

	if te.Instance == nil {

		te.Instance = flowInst
		te.Task = flowInst.Flow.GetTask(te.taskID)

		for _, v := range te.TaskDatas {
			v.taskEnv = te
			v.task = flowInst.Flow.GetTask(v.taskID)
		}

		for _, v := range te.LinkDatas {
			v.taskEnv = te
			v.link = flowInst.Flow.GetLink(v.linkID)
		}
	}
}

// FindOrCreateTaskData finds an existing TaskData or creates ones if not found for the
// specified task the task enviroment
func (te *TaskEnv) FindOrCreateTaskData(task *flow.Task) (taskData *TaskData, created bool) {

	taskData, ok := te.TaskDatas[task.ID()]

	created = false

	if !ok {
		taskData = NewTaskData(te, task)
		te.TaskDatas[task.ID()] = taskData
		te.Instance.ChangeTracker.trackTaskData(&TaskDataChange{ChgType: CtAdd, ID: task.ID(), TaskData: taskData})

		created = true
	}

	return taskData, created
}

// NewTaskData creates a new TaskData object
func (te *TaskEnv) NewTaskData(task *flow.Task) *TaskData {

	taskData := NewTaskData(te, task)
	te.TaskDatas[task.ID()] = taskData
	te.Instance.ChangeTracker.trackTaskData(&TaskDataChange{ChgType: CtAdd, ID: task.ID(), TaskData: taskData})

	return taskData
}

// FindOrCreateLinkData finds an existing LinkData or creates ones if not found for the
// specified link the task enviroment
func (te *TaskEnv) FindOrCreateLinkData(link *flow.Link) (linkData *LinkData, created bool) {

	linkData, ok := te.LinkDatas[link.ID()]
	created = false

	if !ok {
		linkData = NewLinkData(te, link)
		te.LinkDatas[link.ID()] = linkData
		te.Instance.ChangeTracker.trackLinkData(&LinkDataChange{ChgType: CtAdd, ID: link.ID(), LinkData: linkData})
		created = true
	}

	return linkData, created
}

// releaseTask cleans up TaskData in the task environment any of its dependencies.
// This is called when a task is completed and can be discarded
func (te *TaskEnv) releaseTask(task *flow.Task) {
	delete(te.TaskDatas, task.ID())
	te.Instance.ChangeTracker.trackTaskData(&TaskDataChange{ChgType: CtDel, ID: task.ID()})

	childTasks := task.ChildTasks()

	if len(childTasks) > 0 {

		for _, childTask := range childTasks {
			delete(te.TaskDatas, childTask.ID())
			te.Instance.ChangeTracker.trackTaskData(&TaskDataChange{ChgType: CtDel, ID: childTask.ID()})
		}
	}

	links := task.FromLinks()

	for _, link := range links {
		delete(te.LinkDatas, link.ID())
		te.Instance.ChangeTracker.trackLinkData(&LinkDataChange{ChgType: CtDel, ID: link.ID()})
	}
}

// TaskData represents data associated with an instance of a Task
type TaskData struct {
	taskEnv *TaskEnv
	task    *flow.Task
	state   int
	done    bool
	attrs   map[string]*data.Attribute

	inScope  data.Scope
	outScope data.Scope

	changes int

	taskID int //needed for serialization
}

// NewTaskData creates a TaskData for the specified task in the specified task
// environment
func NewTaskData(taskEnv *TaskEnv, task *flow.Task) *TaskData {
	var taskData TaskData

	taskData.taskEnv = taskEnv
	taskData.task = task

	//taskData.TaskID = task.ID

	return &taskData
}

// HasAttrs indicates if the task has attributes
func (td *TaskData) HasAttrs() bool {
	return len(td.task.ActivityType()) > 0 || td.task.IsScope()
}

/////////////////////////////////////////
// TaskData - TaskContext Implementation

// State implements flow.TaskContext.GetState
func (td *TaskData) State() int {
	return td.state
}

// SetState implements flow.TaskContext.SetState
func (td *TaskData) SetState(state int) {
	td.state = state
	td.taskEnv.Instance.ChangeTracker.trackTaskData(&TaskDataChange{ChgType: CtUpd, ID: td.task.ID(), TaskData: td})
}

// Task implements model.TaskContext.Task, by returning the Task associated with this
// TaskData object
func (td *TaskData) Task() *flow.Task {
	return td.task
}

// FromLinks implements model.TaskContext.GetFromLinks, by returning the set of predecessor
// Links of the current task.
func (td *TaskData) FromLinks() []model.LinkContext {

	log.Debugf("GetFromLinks: task=%v\n", td.Task)

	links := td.task.FromLinks()

	numLinks := len(links)

	if numLinks > 0 {
		linkCtxs := make([]model.LinkContext, numLinks)

		for i, link := range links {
			linkCtxs[i], _ = td.taskEnv.FindOrCreateLinkData(link)
		}
		return linkCtxs
	}

	return nil
}

// EnterChildren implements activity.ActivityContext.EnterChildren method
func (td *TaskData) EnterChildren(taskEntries []*model.TaskEntry) {

	if (taskEntries == nil) || (len(taskEntries) == 1 && taskEntries[0].Task == nil) {

		var enterCode int

		if taskEntries == nil {
			enterCode = 0
		} else {
			enterCode = taskEntries[0].EnterCode
		}

		log.Debugf("Entering '%s' Task's %d children\n", td.task.Name(), len(td.task.ChildTasks()))

		for _, task := range td.task.ChildTasks() {

			taskData, _ := td.taskEnv.FindOrCreateTaskData(task)
			taskBehavior := td.taskEnv.Instance.FlowModel.GetTaskBehavior(task.TypeID())

			eval, evalCode := taskBehavior.Enter(taskData, enterCode)

			if eval {
				td.taskEnv.Instance.scheduleEval(taskData, evalCode)
			}
		}
	} else {

		for _, taskEntry := range taskEntries {

			//todo validate if specified task is child? or trust model

			taskData, _ := td.taskEnv.FindOrCreateTaskData(taskEntry.Task)
			taskBehavior := td.taskEnv.Instance.FlowModel.GetTaskBehavior(taskEntry.Task.TypeID())

			eval, evalCode := taskBehavior.Enter(taskData, taskEntry.EnterCode)

			if eval {
				td.taskEnv.Instance.scheduleEval(taskData, evalCode)
			}
		}
	}
}

// EvalLink implements activity.ActivityContext.EvalLink method
func (td *TaskData) EvalLink(link *flow.Link, evalCode int) model.LinkContext {

	linkData, _ := td.taskEnv.FindOrCreateLinkData(link)

	//linkBehavior := td.taskEnv.Instance.FlowModel.GetLinkBehavior(link.)
	//linkBehavior.Eval(linkData, evalCode)
	linkData.SetState(2)

	log.Debugf("TaskContext.EvalLink: State = %d\n", linkData.State())

	return linkData
}

// Activity implements activity.Context.Activity method
func (td *TaskData) Activity() (act activity.Activity, context activity.Context) {

	act = activity.Get(td.task.ActivityType())

	return act, td
}

// FlowInstanceID implements activity.Context.FlowInstanceID method
func (td *TaskData) FlowInstanceID() string {

	return td.taskEnv.Instance.id
}

// FlowName implements activity.Context.FlowName method
func (td *TaskData) FlowName() string {
	return td.taskEnv.Instance.Flow.Name()
}

// TaskName implements activity.Context.TaskName method
func (td *TaskData) TaskName() string {
	return td.task.Name()
}

// InputScope get the InputScope of the task instance
func (td *TaskData) InputScope() data.Scope {

	if td.inScope != nil {
		return td.inScope
	}

	if len(td.task.ActivityType()) > 0 {

		act := activity.Get(td.task.ActivityType())
		td.inScope = NewFixedTaskScope(act.Metadata().Inputs, td.task)

	} else if td.task.IsScope() {

		//add flow scope
	}

	return td.inScope
}

// OutputScope get the InputScope of the task instance
func (td *TaskData) OutputScope() data.Scope {

	if td.outScope != nil {
		return td.outScope
	}

	if len(td.task.ActivityType()) > 0 {

		act := activity.Get(td.task.ActivityType())
		td.outScope = NewFixedTaskScope(act.Metadata().Outputs, nil)

		log.Debugf("OutputScope: %v\n", td.outScope)
	} else if td.task.IsScope() {

		//add flow scope
	}

	return td.outScope
}

// GetInput implements activity.Context.GetInput
func (td *TaskData) GetInput(name string) interface{} {

	val, found := td.InputScope().GetAttrValue(name)
	if found {
		return val
	}

	return nil
}

// SetOutput implements activity.Context.SetOutput
func (td *TaskData) SetOutput(name string, value interface{}) {

	log.Debugf("SET OUTPUT: %s = %v\n", name, value)
	td.OutputScope().SetAttrValue(name, value)
}

// LinkData represents data associated with an instance of a Link
type LinkData struct {
	taskEnv *TaskEnv
	link    *flow.Link
	state   int
	attrs   map[string]string

	changes int

	linkID int //needed for serialization
}

// NewLinkData creates a LinkData for the specified link in the specified task
// environment
func NewLinkData(taskEnv *TaskEnv, link *flow.Link) *LinkData {
	var linkData LinkData

	linkData.taskEnv = taskEnv
	linkData.link = link
	//linkData.LinkID = link.ID

	return &linkData
}

// State returns the current state indicator for the LinkData
func (ld *LinkData) State() int {
	return ld.state
}

// SetState sets the current state indicator for the LinkData
func (ld *LinkData) SetState(state int) {
	ld.state = state
	ld.taskEnv.Instance.ChangeTracker.trackLinkData(&LinkDataChange{ChgType: CtUpd, ID: ld.link.ID(), LinkData: ld})
}

// Link returns the Link associated with ld context
func (ld *LinkData) Link() *flow.Link {

	return ld.link
}

// ExecType is the type of execution to perform
type ExecType int

const (
	// EtEval denoted the Eval execution type
	EtEval ExecType = 10

	// EtPostEval denoted the PostEval execution type
	EtPostEval ExecType = 20
)

// WorkItem describes an item of work (event for a Task) that should be executed on Step
type WorkItem struct {
	ID       int       `json:"id"`
	TaskData *TaskData `json:"-"`
	ExecType ExecType  `json:"execType"`
	EvalCode int       `json:"code"`

	TaskID int `json:"taskID"` //for now need for ser
	//taskCtxID int `json:"taskCtxID"` //not needed for now
}

// NewWorkItem constructs a new WorkItem for the specified TaskData
func NewWorkItem(id int, taskData *TaskData, execType ExecType, evalCode int) *WorkItem {

	var workItem WorkItem

	workItem.ID = id
	workItem.TaskData = taskData
	workItem.ExecType = execType
	workItem.EvalCode = evalCode

	workItem.TaskID = taskData.task.ID()

	return &workItem
}