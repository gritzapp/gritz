package gritzclient

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/poll"
)

// runnerEventWaitTimeout bounds each RunnerEventWaiter.Wait.
const runnerEventWaitTimeout = 30 * time.Second

// RunnerEventWaiter waits for runner events submitted through a ClientMock's
// SubmitRunnerEvents, in submission order.
type RunnerEventWaiter struct {
	t    testing.TB
	mock *ClientMock
	// next is the index, among the submitted events, after the one the
	// previous Wait returned.
	next int
}

// RunnerEventWaiter returns a waiter over the runner events m has recorded.
func (mock *ClientMock) RunnerEventWaiter(t testing.TB) *RunnerEventWaiter {
	return &RunnerEventWaiter{t: t, mock: mock}
}

// Wait waits until an event for taskID named event (model.RunnerEvent*) is
// submitted after the one the previous Wait returned, and returns it.
func (w *RunnerEventWaiter) Wait(taskID int64, event model.RunnerEventType) *gritzv1.RunnerEvent {
	w.t.Helper()
	var found *gritzv1.RunnerEvent
	poll.WaitOn(w.t, func(poll.LogT) poll.Result {
		events := w.mock.SubmittedRunnerEvents()
		for i := w.next; i < len(events); i++ {
			if ev := events[i]; ev.GetTaskId() == taskID && ev.GetEvent() == string(event) {
				found, w.next = ev, i+1
				return poll.Success()
			}
		}
		return poll.Continue("no %s event for task %d after event %d; submitted:%s",
			event, taskID, w.next, formatRunnerEvents(events))
	}, poll.WithTimeout(runnerEventWaitTimeout), poll.WithDelay(20*time.Millisecond))
	return found
}

func formatRunnerEvents(events []*gritzv1.RunnerEvent) string {
	if len(events) == 0 {
		return " none"
	}
	var b strings.Builder
	for i, ev := range events {
		fmt.Fprintf(&b, "\n\tevent %d: task %d, %s, version %d", i+1, ev.GetTaskId(), ev.GetEvent(), ev.GetVersion())
	}
	return b.String()
}
