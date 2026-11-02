package e2e

import (
	"bytes"
	"fmt"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/proto"
	"gotest.tools/v3/poll"
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

// Archive archives the task, as the task page does.
func (task *Task) Archive() {
	task.org.server.t.Helper()
	task.org.ArchiveTask(&gritzv1.ArchiveTaskRequest{Id: task.ID})
}

// Unarchive unarchives the task, as the task page does.
func (task *Task) Unarchive() {
	t := task.org.server.t
	t.Helper()
	if _, err := task.org.Client.UnarchiveTask(t.Context(), &gritzv1.UnarchiveTaskRequest{Id: task.ID}); err != nil {
		t.Fatalf("UnarchiveTask: %v", err)
	}
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

// WaitLog waits until match accepts the task's shipped log, every chunk
// concatenated, and returns it. The driver ships the log on its own schedule,
// flushing a run's tail only after reporting it, so a log is waited for rather
// than read.
func (task *Task) WaitLog(match func(log string) bool) string {
	t := task.org.server.t
	t.Helper()
	var log string
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		var err error
		log, err = task.org.log(task.ID)
		if err != nil {
			return poll.Error(err)
		}
		if match(log) {
			return poll.Success()
		}
		return poll.Continue("task %d: no matching log in:\n%s", task.ID, log)
	}, task.org.pollOps()...)
	return log
}

// log returns task id's shipped log, every chunk concatenated.
func (o *Org) log(id int64) (string, error) {
	var chunks []*gritzv1.LogChunk
	token := ""
	for {
		resp, err := o.Client.ListLogChunksByTask(o.server.t.Context(), &gritzv1.ListLogChunksByTaskRequest{
			TaskId:    id,
			PageSize:  200,
			PageToken: token,
		})
		if err != nil {
			return "", fmt.Errorf("ListLogChunksByTask %d: %w", id, err)
		}
		chunks = append(resp.Chunks, chunks...)
		if resp.PrevPageToken == "" {
			break
		}
		token = resp.PrevPageToken
	}
	var b bytes.Buffer
	for _, c := range chunks {
		b.Write(c.Data)
	}
	return b.String(), nil
}
