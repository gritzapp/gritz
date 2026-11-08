package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"golang.org/x/oauth2"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// ServeOptions configures Serve.
type ServeOptions struct {
	Client gritzclient.Client
	// ServerURL is the server the runner and its sandboxes talk to.
	ServerURL string
	// TokenSource supplies the runner's key, for the notification stream.
	TokenSource oauth2.TokenSource
	Backend     backend.Backend
	// StateDB is the path of the runner's SQLite database (runner.db).
	StateDB      string
	Workspaces   *workspace.Config
	RunnerID     string
	Concurrency  int
	PollInterval time.Duration
	// LostTimeout and ReadyTimeout set the TaskRunnerOptions fields of the
	// same names; 0 takes the TaskRunner's defaults.
	LostTimeout  time.Duration
	ReadyTimeout time.Duration
	Log          *slog.Logger
}

// Serve runs the runner until ctx ends: it opens the state DB, registers the
// workspaces, restores sandboxes, delivers runner events, follows server
// notifications and drives the tasks. It leaves sandboxes up when ctx ends.
// It returns a FatalError when a durable write fails.
func Serve(ctx context.Context, opts ServeOptions) error {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	// Wrap the incoming ctx so a durable local-store write failure can
	// terminate the runner by cancelling it with a FatalError cause. This
	// must happen before anything that takes ctx is spawned (Load's
	// RunLoop goroutines, queue.Run, the notify goroutine, and m.Run) so
	// they all observe the cancellation.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// Open the runner's SQLite database, which holds the replica of its
	// tasks' event logs that the TaskManager acts on, the task→sandbox-
	// handle records, the single source of truth for which sandbox belongs
	// to which task, and the runner event outbox.
	if err := os.MkdirAll(filepath.Dir(opts.StateDB), 0o755); err != nil {
		return fmt.Errorf("failed to create state db directory: %w", err)
	}
	rstore, err := runnerstore.Open(opts.StateDB)
	if err != nil {
		return fmt.Errorf("failed to open state db: %w", err)
	}
	defer rstore.Close()
	tasklog := taskevents.New(taskevents.Options{
		Store:        rstore,
		Client:       opts.Client,
		RunnerID:     opts.RunnerID,
		PollInterval: opts.PollInterval,
		Log:          log,
	})

	// The outbox is durable: runner lifecycle events survive a restart and
	// are redelivered on the next Run pass rather than being lost with an
	// in-memory buffer.
	queue := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		Store:  rstore,
		Client: opts.Client,
		// Reproduce the old EventQueue's fixed retry interval (the poll
		// interval) with a constant backoff, for a drop-in match.
		Backoff: backoff.NewConstantBackOff(opts.PollInterval),
		Log:     log,
	})

	m, err := NewTaskManager(TaskManagerOptions{
		Client:      opts.Client,
		Backend:     opts.Backend,
		ServerURL:   opts.ServerURL,
		Workspaces:  opts.Workspaces,
		Concurrency: opts.Concurrency,
		RunnerID:    opts.RunnerID,
		Log:         log,
		Queue:       queue,
		TaskLog:     tasklog,
		RunnerStore: rstore,
		Fatal:       cancel,
		NewTaskRunner: func(o TaskRunnerOptions) *TaskRunner {
			o.LostTimeout = opts.LostTimeout
			o.ReadyTimeout = opts.ReadyTimeout
			return NewTaskRunner(o)
		},
	})
	if err != nil {
		return err
	}
	defer m.Close()

	// Register workspaces with the server (non-fatal if it fails)
	if err := m.RegisterWorkspaces(ctx); err != nil {
		log.Warn("failed to register workspaces", "err", err)
	}

	// Rehydrate the sandboxes the runner has records of: this starts a
	// RunLoop per record, which reattaches to a running sandbox, and seeds
	// the concurrency semaphore. It runs once, before Run admits new work.
	if err := m.Load(ctx); err != nil {
		return fmt.Errorf("failed to load sandboxes: %w", err)
	}

	// The outbox and the notification client use rstore and m, so Serve
	// waits for them before its deferred closes run.
	var wg sync.WaitGroup

	// Start the outbox delivery goroutine. Its first pass redelivers any
	// events that were persisted but not yet acknowledged before a restart.
	wg.Go(func() { queue.Run(ctx) })

	// Subscribe to server-sent task change notifications so the runner
	// reacts to new commands immediately instead of waiting for the
	// fallback poll.
	wg.Go(func() {
		nc := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
			BaseURL:     opts.ServerURL,
			Runner:      opts.RunnerID,
			TokenSource: opts.TokenSource,
			Log:         log,
			Handler:     func(model.Notification) { m.Wake() },
		})
		if err := nc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("notification client stopped", "err", err)
		}
	})

	// Run the tasks until ctx is done, which stops the goroutines above too.
	cause := m.Run(ctx)
	wg.Wait()
	// Distinguish a durable-store-write crash (die set a FatalError cause)
	// from a graceful signal shutdown.
	// The sentinel is what lets us return non-zero on a broken disk
	// without depending on whatever cause signal.NotifyContext sets.
	var fatal FatalError
	if errors.As(cause, &fatal) {
		log.Error("runner terminating: durable store write failed", "err", cause)
		return cause // non-zero exit → supervisor restarts + alerts
	}
	return nil // graceful signal shutdown → exit 0
}
