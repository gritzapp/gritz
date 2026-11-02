package e2e

import (
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

// Task is a task in the harness. Its commands go through the RPCs the task
// page uses.
type Task struct {
	ID int64
	// Resp is the CreateTask response the task was made from.
	Resp *gritzv1.CreateTaskResponse

	org *Org
}

// AddInstruction adds an instruction with text, without starting the task.
func (task *Task) AddInstruction(text string) {
	task.org.server.t.Helper()
	task.org.UpdateTask(&gritzv1.UpdateTaskRequest{
		Id:              task.ID,
		AddInstructions: []*gritzv1.Instruction{{Text: text}},
	})
}

// Start starts the task, or wakes it.
func (task *Task) Start() {
	task.org.server.t.Helper()
	task.org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
}

// Events returns the task's events, oldest first.
func (task *Task) Events() []*gritzv1.Event {
	t := task.org.server.t
	t.Helper()
	events, err := task.org.events(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// WaitStatus waits until the task has status and returns it.
func (task *Task) WaitStatus(status gritzv1.TaskStatus) *gritzv1.Task {
	task.org.server.t.Helper()
	return task.org.WaitTask(task.ID, func(got *gritzv1.Task) bool { return got.Status == status })
}
