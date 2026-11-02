package e2e

import (
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/proto"
)

// Task is a task in the harness. Its commands go through the RPCs the task
// page uses.
type Task struct {
	ID int64
	// Resp is the CreateTask response the task was made from.
	Resp *gritzv1.CreateTaskResponse

	org *Org
}

// Update updates the task, as the task page does. It fills req.Id with the
// task's id.
func (task *Task) Update(req *gritzv1.UpdateTaskRequest) {
	t := task.org.server.t
	t.Helper()
	req = proto.CloneOf(req)
	req.Id = task.ID
	if _, err := task.org.Client.UpdateTask(t.Context(), req); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
}

// Cancel cancels the task, as the task page does.
func (task *Task) Cancel() {
	task.org.server.t.Helper()
	task.org.CancelTask(&gritzv1.CancelTaskRequest{Id: task.ID})
}

// Restart restarts the task, as the task page does.
func (task *Task) Restart() {
	t := task.org.server.t
	t.Helper()
	if _, err := task.org.Client.RestartTask(t.Context(), &gritzv1.RestartTaskRequest{Id: task.ID}); err != nil {
		t.Fatalf("RestartTask: %v", err)
	}
}

// Get returns the task.
func (task *Task) Get() *gritzv1.Task {
	task.org.server.t.Helper()
	return task.org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
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
