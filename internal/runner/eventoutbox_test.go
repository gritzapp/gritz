package runner_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"github.com/gritzapp/gritz/internal/x/outbox"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

// TestRunnerEventOutbox_Permanent covers how the outbox classifies a failed
// delivery: a permanent error dead-letters the event after one attempt, and
// any other error is retried until the event is delivered.
func TestRunnerEventOutbox_Permanent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		// wantCalls is how many SubmitRunnerEvents calls it took to drain the
		// event.
		wantCalls int
		wantDead  bool
	}{
		{"nil", nil, 1, false},
		{"not found", connect.NewError(connect.CodeNotFound, errors.New("gone")), 1, true},
		{"invalid argument", connect.NewError(connect.CodeInvalidArgument, errors.New("bad")), 1, true},
		{"permission denied", connect.NewError(connect.CodePermissionDenied, errors.New("nope")), 1, true},
		{"unavailable", connect.NewError(connect.CodeUnavailable, errors.New("down")), 2, false},
		{"internal", connect.NewError(connect.CodeInternal, errors.New("boom")), 2, false},
		{"deadline exceeded", connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow")), 2, false},
		{"plain error", errors.New("connection refused"), 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - a server whose first SubmitRunnerEvents fails with
			// tt.err, and an outbox whose store records how the event left it.
			var calls atomic.Int32
			mock := &gritzclient.ClientMock{
				SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
					if calls.Add(1) == 1 && tt.err != nil {
						return nil, tt.err
					}
					return &gritzv1.SubmitRunnerEventsResponse{}, nil
				},
			}
			fs, err := outbox.Open(t.TempDir())
			assert.NilError(t, err)
			store := &outbox.StoreMock{AppendFunc: fs.Append, PeekFunc: fs.Peek, DropFunc: fs.Drop, LenFunc: fs.Len}
			queue, err := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
				Store:   store,
				Client:  mock,
				Backoff: backoff.NewConstantBackOff(0),
				Log:     slog.Default(),
			})
			assert.NilError(t, err)
			assert.NilError(t, queue.Enqueue(model.RunnerEvent{TaskID: 1, Event: model.RunnerEventStarted}))

			// Act
			testrunner.Drain(t, queue)

			// Assert - the event left the queue once, dead-lettered only on a
			// permanent error.
			assert.Equal(t, int(calls.Load()), tt.wantCalls)
			drops := store.DropCalls()
			assert.Equal(t, len(drops), 1)
			assert.Equal(t, drops[0].Dead, tt.wantDead)
		})
	}
}

// TestTaskManagerEventsSurviveRestart is the durability payoff of this layer: events
// persisted by one runner process are redelivered by the next, with no separate
// recovery path — the reopened outbox's first Run pass drains them in FIFO order.
func TestTaskManagerEventsSurviveRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A runner persists two lifecycle events, then "crashes" before delivering
	// them: this outbox is never Run, so nothing reaches the server.
	crashed, err := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
		StoreDir: dir,
		Client:   &gritzclient.ClientMock{},
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	assert.NilError(t, crashed.Enqueue(model.RunnerEvent{TaskID: 1, Event: model.RunnerEventStarted}))
	assert.NilError(t, crashed.Enqueue(model.RunnerEvent{TaskID: 2, Event: model.RunnerEventFailed, Version: 5, Reason: "boom"}))

	// The new process re-opens the same durable dir and delivers on startup.
	mock := &gritzclient.ClientMock{
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	restarted, err := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
		StoreDir: dir,
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)

	// The events survived the restart and are redelivered in FIFO order, with
	// their TaskId/Version/Reason intact.
	testrunner.Drain(t, restarted)
	assert.DeepEqual(t, mock.SubmittedRunnerEvents(), []*gritzv1.RunnerEvent{
		{Event: "started", TaskId: 1},
		{Event: "failed", TaskId: 2, Version: 5, Reason: "boom"},
	}, protocmp.Transform())
}
