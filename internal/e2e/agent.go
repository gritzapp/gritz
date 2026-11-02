package e2e

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent/puppet"
)

// Agent is the test's side of the puppet agent: every Prompt call the driver
// makes is a Run, queued per task in arrival order and held open until it
// ends.
type Agent struct {
	t       *testing.T
	org     *Org
	server  *puppet.Server
	url     string
	timeout time.Duration
}

// newAgent serves a puppet.Server on its own endpoint, closed on cleanup.
func newAgent(t *testing.T, org *Org) *Agent {
	server := puppet.NewServer()
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return &Agent{t: t, org: org, server: server, url: ts.URL, timeout: org.server.timeout}
}

// WaitRun waits for the task's next run, the first one no earlier WaitRun
// returned, and returns it.
func (a *Agent) WaitRun(taskID int64) *Run {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(a.t.Context(), a.timeout)
	defer cancel()
	run, err := a.server.Next(ctx, taskID)
	if err != nil {
		a.t.Fatalf("task %d: no next run: %v; runs so far:%s; %s",
			taskID, err, formatRuns(a.server.Runs(taskID)), a.org.taskState(taskID))
	}
	return a.wrap(run)
}

// Runs returns every run the task has had so far, in order.
func (a *Agent) Runs(taskID int64) []*Run {
	var runs []*Run
	for _, run := range a.server.Runs(taskID) {
		runs = append(runs, a.wrap(run))
	}
	return runs
}

func (a *Agent) wrap(run *puppet.Run) *Run {
	return &Run{Prompt: run.Prompt, Resume: run.Resume, run: run, agent: a}
}

// Run is one Prompt call, held open until it ends.
type Run struct {
	// Prompt and Resume are the arguments the driver passed to Prompt.
	Prompt string
	Resume bool

	run   *puppet.Run
	agent *Agent
}

// Complete ends the run; Prompt returns nil.
func (r *Run) Complete() {
	r.agent.t.Helper()
	if err := r.run.Complete(); err != nil {
		r.agent.t.Fatalf("task %d: complete run: %v", r.run.TaskID, err)
	}
}

// Fail ends the run; Prompt returns an error with msg.
func (r *Run) Fail(msg string) {
	r.agent.t.Helper()
	if err := r.run.Fail(msg); err != nil {
		r.agent.t.Fatalf("task %d: fail run: %v", r.run.TaskID, err)
	}
}

// WaitCancelled waits until the driver cancels the run.
func (r *Run) WaitCancelled() {
	t := r.agent.t
	t.Helper()
	timer := time.NewTimer(r.agent.timeout)
	defer timer.Stop()
	select {
	case <-r.run.Cancelled():
	case <-timer.C:
		t.Fatalf("task %d: run with prompt %q not cancelled after %s; %s",
			r.run.TaskID, r.Prompt, r.agent.timeout, r.agent.org.taskState(r.run.TaskID))
	}
}

func formatRuns(runs []*puppet.Run) string {
	if len(runs) == 0 {
		return " none"
	}
	var b strings.Builder
	for i, run := range runs {
		state := "open or ended"
		select {
		case <-run.Cancelled():
			state = "cancelled"
		default:
		}
		fmt.Fprintf(&b, "\n\trun %d (%s): resume %t, prompt %q", i+1, state, run.Resume, run.Prompt)
	}
	return b.String()
}

// taskState describes task id's status for a failed wait's report.
func (o *Org) taskState(id int64) string {
	resp, err := o.Client.GetTask(o.server.t.Context(), &gritzv1.GetTaskRequest{Id: id})
	if err != nil {
		return fmt.Sprintf("GetTask %d: %v", id, err)
	}
	return formatTask(resp.Task)
}

func formatTask(task *gritzv1.Task) string {
	return fmt.Sprintf("task %d: status %s, version %d, command %s", task.Id, task.Status, task.Version, task.Command)
}
