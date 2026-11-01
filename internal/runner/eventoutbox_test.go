package runner_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

// TestRunnerEventOutbox_Permanent covers how the outbox classifies a failed
// delivery: a permanent error dead-letters the event after one attempt, and
// any other error is retried until the event is delivered. Either way the
// queue drains.
func TestRunnerEventOutbox_Permanent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		// wantCalls is how many SubmitRunnerEvents calls it took to drain the
		// event.
		wantCalls int
	}{
		{"nil", nil, 1},
		{"not found", connect.NewError(connect.CodeNotFound, errors.New("gone")), 1},
		{"invalid argument", connect.NewError(connect.CodeInvalidArgument, errors.New("bad")), 1},
		{"permission denied", connect.NewError(connect.CodePermissionDenied, errors.New("nope")), 1},
		{"unavailable", connect.NewError(connect.CodeUnavailable, errors.New("down")), 2},
		{"internal", connect.NewError(connect.CodeInternal, errors.New("boom")), 2},
		{"deadline exceeded", connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow")), 2},
		{"plain error", errors.New("connection refused"), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - a server whose first SubmitRunnerEvents fails with
			// tt.err.
			var calls atomic.Int32
			mock := &gritzclient.ClientMock{
				SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
					if calls.Add(1) == 1 && tt.err != nil {
						return nil, tt.err
					}
					return &gritzv1.SubmitRunnerEventsResponse{}, nil
				},
			}
			queue := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
				Store:   testrunner.NewRunnerStore(t),
				Client:  mock,
				Backoff: backoff.NewConstantBackOff(0),
				Log:     slog.Default(),
			})
			assert.NilError(t, queue.Enqueue(model.RunnerEvent{
				TaskID: 1,
				Event:  model.RunnerEventStarted,
			}))

			// Act
			testrunner.Drain(t, queue)

			// Assert - the queue drained, and the event was resubmitted only
			// after a transient error.
			assert.Equal(t, int(calls.Load()), tt.wantCalls)
		})
	}
}

// TestTaskManagerEventsSurviveRestart is the durability payoff of this layer: events
// persisted by one runner process are redelivered by the next, with no separate
// recovery path — the reopened outbox's first Run pass drains them in FIFO order.
func TestTaskManagerEventsSurviveRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runner.db")

	// A runner persists two lifecycle events, then "crashes" before delivering
	// them: this outbox is never Run, so nothing reaches the server.
	store, err := runnerstore.Open(path)
	assert.NilError(t, err)
	crashed := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
		Store:   store,
		Client:  &gritzclient.ClientMock{},
		Backoff: backoff.NewConstantBackOff(0),
		Log:     slog.Default(),
	})
	assert.NilError(t, crashed.Enqueue(model.RunnerEvent{
		TaskID: 1,
		Event:  model.RunnerEventStarted,
	}))
	assert.NilError(t, crashed.Enqueue(model.RunnerEvent{
		TaskID:  2,
		Event:   model.RunnerEventFailed,
		Version: 5,
		Reason:  "boom",
	}))
	assert.NilError(t, store.Close())

	// The new process re-opens the same database and delivers on startup.
	mock := &gritzclient.ClientMock{
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	store, err = runnerstore.Open(path)
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	restarted := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
		Store:   store,
		Client:  mock,
		Backoff: backoff.NewConstantBackOff(0),
		Log:     slog.Default(),
	})

	// The events survived the restart and are redelivered in FIFO order, with
	// their TaskId/Version/Reason intact.
	testrunner.Drain(t, restarted)
	assert.DeepEqual(t, mock.SubmittedRunnerEvents(), []*gritzv1.RunnerEvent{
		{Event: "started", TaskId: 1},
		{Event: "failed", TaskId: 2, Version: 5, Reason: "boom"},
	}, protocmp.Transform())
}
