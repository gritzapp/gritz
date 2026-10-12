package runner

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"path"
	"regexp"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/outbox"
	"github.com/gritzapp/gritz/internal/x/safesem"
	"github.com/gritzapp/gritz/internal/x/wakeup"
	"golang.org/x/sync/errgroup"
)

type Runner struct {
	backend     backend.Backend
	store       *taskstate.Store
	client      gritzclient.Client
	serverURL   string
	workspaces  *workspace.Config
	runnerID    string
	concurrency int64
	sem         *safesem.Semaphore
	log         *slog.Logger
	queue       *outbox.Outbox[model.RunnerEvent]
	wake        wakeup.Chan
	fatal       context.CancelCauseFunc

	// mu guards tasks.
	mu    sync.Mutex
	tasks map[int64]*taskEntry

	// retryInterval is the first pause between attempts to reach the driver
	// or reattach to a run, retryMaxInterval caps the reattach backoff, and
	// lostTimeout is how long supervise keeps reattaching without a good
	// message from the driver before it gives up on the run. readyTimeout is
	// how long a started sandbox's driver has to answer and accept its run.
	// Tests shorten them.
	retryInterval    time.Duration
	retryMaxInterval time.Duration
	lostTimeout      time.Duration
	readyTimeout     time.Duration
}

const (
	// reattachInterval is the first pause between attempts to reattach to a
	// run.
	reattachInterval = 250 * time.Millisecond
	// reattachMaxInterval caps the backoff between attempts to reattach.
	reattachMaxInterval = 10 * time.Second
	// driverLostTimeout is how long supervise keeps reattaching without a good
	// message from the driver before it parks the sandbox and reports the run
	// lost.
	driverLostTimeout = 2 * time.Minute
	// driverReadyTimeout is how long supervise waits for a started sandbox's
	// driver to answer Status and accept its run. It counts whatever the
	// image's entrypoint does before it execs the driver, and a Lambda VM's
	// boot.
	driverReadyTimeout = time.Minute
	// readyMaxInterval caps the backoff between attempts to reach a starting
	// driver.
	readyMaxInterval = 2 * time.Second
)

// taskEntry is the runner's in-memory state for one task. taskstate/ stays the
// durable record; an entry only caches what this process holds for it.
type taskEntry struct {
	// mu serializes Start, Kill and Remove for the task.
	mu sync.Mutex
	// sb is the task's sandbox, opened or created by this process, or nil.
	// Guarded by mu.
	sb backend.Sandbox
	// run is the run a supervise goroutine is following, or nil. Guarded by
	// mu.
	run *taskRun
}

// taskRun is one run of a task, followed by its supervise goroutine. Its
// fields are set by supervise and not changed after: the goroutine runs
// without e.mu, so it uses its own sb, never the entry's.
type taskRun struct {
	r       *Runner
	taskID  int64
	version int64
	// spec starts the run, or is nil when reattaching to a run already
	// started.
	spec *driverv1.RunSpec
	// sb is the sandbox the run is on.
	sb     backend.Sandbox
	cancel context.CancelCauseFunc
}

// errRemoved is the cause a supervise goroutine is cancelled with when Remove
// destroys its sandbox.
var errRemoved = errors.New("sandbox removed")

type Options struct {
	Client gritzclient.Client
	// Backend is the sandbox runtime that hosts task drivers.
	Backend backend.Backend
	// Store is the runner-local source of truth for the task→sandbox-handle
	// mapping. The runner is the only writer; backends never touch it.
	Store *taskstate.Store
	// ServerURL is the server URL injected into sandboxes so the driver and the
	// injected gritz MCP server connect directly to the server. It is the runner's
	// own configured --server value; the sandbox reaches the same server the runner
	// does. The runner authenticates the token-minting RPC with its own xat_ key.
	ServerURL   string
	Workspaces  *workspace.Config
	Concurrency int
	RunnerID    string
	Log         *slog.Logger
	// Queue is the durable outbox that delivers runner lifecycle events to the
	// server, buffering them across transient outages and restarts.
	Queue *outbox.Outbox[model.RunnerEvent]
	// Fatal terminates the runner by cancelling its root context with the given
	// cause. It is invoked by die when a durable local-store write fails; the
	// command layer wraps ctx with context.WithCancelCause and passes the
	// CancelCauseFunc here. When nil it defaults to a no-op, so die never crashes
	// (durable-write failures fall back to the pre-existing behaviour).
	Fatal context.CancelCauseFunc
}

var reRunnerID = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// ValidateRunnerID checks that id is a valid runner identifier.
func ValidateRunnerID(id string) error {
	if !reRunnerID.MatchString(id) {
		return fmt.Errorf("invalid runner id: %q", id)
	}
	return nil
}

func New(opts Options) (*Runner, error) {
	if err := ValidateRunnerID(opts.RunnerID); err != nil {
		return nil, err
	}
	if opts.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if opts.Store == nil {
		return nil, fmt.Errorf("store is required")
	}

	// Use math.MaxInt64 if no limit is set (concurrency <= 0)
	concurrency := int64(opts.Concurrency)
	if concurrency <= 0 {
		concurrency = math.MaxInt64
	}

	log := cmp.Or(opts.Log, slog.Default())

	// Default Fatal to a no-op so die can call r.fatal unconditionally. A nil
	// Fatal means the caller opted out of crash-on-write-failure (only tests that
	// never trigger die); production always wires the CancelCauseFunc.
	fatal := opts.Fatal
	if fatal == nil {
		fatal = func(error) {}
	}

	return &Runner{
		backend:     opts.Backend,
		store:       opts.Store,
		client:      opts.Client,
		serverURL:   opts.ServerURL,
		workspaces:  opts.Workspaces,
		runnerID:    opts.RunnerID,
		concurrency: concurrency,
		sem:         safesem.New(concurrency),
		log:         log,
		queue:       opts.Queue,
		wake:        wakeup.New(),
		fatal:       fatal,

		tasks:            map[int64]*taskEntry{},
		retryInterval:    reattachInterval,
		retryMaxInterval: reattachMaxInterval,
		lostTimeout:      driverLostTimeout,
		readyTimeout:     driverReadyTimeout,
	}, nil
}

// FatalError wraps a durable local-store write failure that terminates the
// runner. It is the cause set on the runner's root context by die; the command
// layer matches it with errors.As to distinguish a broken-disk crash (non-zero
// exit → supervisor restarts + alerts) from a graceful signal shutdown (exit 0),
// without depending on whatever cause signal.NotifyContext sets.
type FatalError struct{ err error }

func (e FatalError) Error() string { return "durable store write failed: " + e.err.Error() }
func (e FatalError) Unwrap() error { return e.err }

// die terminates the runner by cancelling its root context with a
// FatalError cause. It is the response to a durable local-store write
// failure (outbox Enqueue or taskstate Write): those failures are non-transient
// (ENOSPC/EIO/EROFS) and silently dropping a lifecycle event defeats the whole
// point of the durable store, so the runner fails loud and lets its supervisor
// restart and alert. Riding ctx cancellation instead of os.Exit preserves the
// detach-don't-kill teardown — in-flight sandboxes stay alive and are re-adopted
// by Load on the next boot. First cause wins (WithCancelCause is idempotent), so
// the first failure is the one reported.
func (r *Runner) die(err error) { r.fatal(FatalError{err}) }

// WakeC returns a channel that receives one value per coalesced burst of
// internally-generated wake-ups. Currently signalled when a concurrency
// slot is released so the main loop can immediately retry tasks that were
// previously skipped for being over the limit.
func (r *Runner) WakeC() <-chan struct{} { return r.wake }

// Wake signals the runner's main loop to poll immediately.
func (r *Runner) Wake() { r.wake.Wake() }

func (r *Runner) Close() error {
	return r.backend.Close()
}

// RegisterWorkspaces sends the available workspace names to the server.
func (r *Runner) RegisterWorkspaces(ctx context.Context) error {
	workspaces := make([]*gritzv1.RegisteredWorkspace, 0, len(r.workspaces.Workspaces))
	for name, ws := range r.workspaces.Workspaces {
		workspaces = append(workspaces, &gritzv1.RegisteredWorkspace{
			Name:        name,
			Description: ws.Description,
		})
	}
	_, err := r.client.RegisterWorkspaces(ctx, &gritzv1.RegisterWorkspacesRequest{
		RunnerId:   r.runnerID,
		Workspaces: workspaces,
	})
	if err != nil {
		return fmt.Errorf("failed to register workspaces: %w", err)
	}
	r.log.Info("registered workspaces", "runner_id", r.runnerID, "count", len(workspaces))
	return nil
}

func (r *Runner) Poll(ctx context.Context) error {
	resp, err := r.client.ListRunnerTasks(ctx, &gritzv1.ListRunnerTasksRequest{Runner: r.runnerID})
	if err != nil {
		return err
	}

	// Plain group (limit + join only): WithContext's derived ctx is canceled
	// when Wait returns, which would kill the supervise goroutines Start spawns
	// on this ctx. supervise must outlive the poll cycle.
	var g errgroup.Group
	g.SetLimit(int(r.concurrency))

	for _, pbTask := range resp.Tasks {
		task := model.TaskFromProto(pbTask)
		switch task.Command {
		case model.TaskCommandStop:
			g.Go(func() error {
				signalled, err := r.Kill(ctx, task)
				if err != nil {
					// The stop command survives, so the kill is retried on
					// the next poll.
					r.log.Error("failed to stop task", "task", task.ID, "err", err)
					return nil
				}
				if signalled {
					// The driver owns the terminal report. If it hangs until
					// SIGKILL, the non-zero exit triggers the monitor's
					// "failed" instead.
					return nil
				}
				// No running run to stop: there is no driver to complete
				// the cancel, so emit "stopped" to land the task in
				// cancelled instead of sticking in cancelling.
				if err := r.queue.Enqueue(model.RunnerEvent{
					TaskID:  task.ID,
					Event:   model.RunnerEventStopped,
					Version: task.Version,
				}); err != nil {
					r.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventStopped, task.ID, err))
					return nil
				}
				return nil
			})
		case model.TaskCommandRestart:
			g.Go(func() error {
				// Idempotency guard: the record is stamped with the version of
				// the run it launched, so a record already on the task's current
				// version means this restart's new run was launched by an
				// earlier poll pass. The driver takes ~12s to boot, so any poll
				// landing in the launch→"started" window still sees the pending
				// command; killing here would SIGKILL the run we just started
				// (issue #1520). Do nothing and wait for "started" to consume
				// the command, exactly like the start handler's already-running
				// guard. A legacy version-0 record never matches a live task's
				// version (>= 1), so the guard is a no-op for those.
				rec, ok, err := r.store.Read(task.ID)
				if err != nil {
					r.log.Error("failed to read task record for restart", "task", task.ID, "err", err)
					return nil
				}
				if ok && rec.Version == task.Version {
					r.log.Debug("restart already processed, waiting for started", "task", task.ID, "version", task.Version)
					return nil
				}
				// Kill existing sandbox if running. The old run's "stopped"
				// is rejected by the status guard while the restart command is
				// pending, so the command survives until the new run's
				// "started" consumes it.
				if _, err := r.Kill(ctx, task); err != nil {
					r.log.Error("failed to kill task for restart", "task", task.ID, "err", err)
				}
				// Atomically acquire a semaphore slot before starting
				if !r.sem.TryAcquire(1) {
					r.log.Debug("concurrency limit reached, skipping task", "task", task.ID, "limit", r.concurrency)
					return nil
				}
				err = r.Start(ctx, task)
				if errors.Is(err, errRunning) {
					// Nothing was started, so the slot goes back; the
					// command is retried on a later poll.
					r.sem.Release(1)
					r.log.Debug("sandbox still running, not started", "task", task.ID)
					return nil
				}
				if err != nil {
					r.sem.Release(1) // Release the slot on failure
					r.log.Error("failed to start task", "task", task.ID, "err", err)
					err := r.queue.Enqueue(model.RunnerEvent{
						TaskID:  task.ID,
						Event:   model.RunnerEventFailed,
						Version: task.Version,
						Reason:  err.Error(),
					})
					if err != nil {
						r.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, task.ID, err))
					}
					return nil
				}
				// The "started" event is submitted by the driver on startup
				return nil
			})
		case model.TaskCommandStart:
			g.Go(func() error {
				// Don't bother checking the backend if the status is still running
				if task.Status == model.TaskStatusRunning {
					r.log.Debug("start command: task.status=running, waiting for it to finish", "task", task.ID)
					return nil
				}
				// Check if the sandbox is already running
				running, err := r.Running(ctx, task.ID)
				if err != nil {
					r.log.Error("failed to check if task is running", "task", task.ID, "err", err)
					return nil
				}
				if running {
					// Sandbox is running - do nothing, let it finish naturally
					// The command will be processed when it stops
					r.log.Debug("start command: sandbox already running, waiting for it to finish", "task", task.ID)
					return nil
				}

				// Sandbox not running - atomically acquire a semaphore slot before starting
				if !r.sem.TryAcquire(1) {
					r.log.Debug("concurrency limit reached, skipping task", "task", task.ID, "limit", r.concurrency)
					return nil
				}
				err = r.Start(ctx, task)
				if errors.Is(err, errRunning) {
					// Nothing was started, so the slot goes back; the
					// command is retried on a later poll.
					r.sem.Release(1)
					r.log.Debug("sandbox still running, not started", "task", task.ID)
					return nil
				}
				if err != nil {
					r.sem.Release(1) // Release the slot on failure
					r.log.Error("failed to start task", "task", task.ID, "err", err)
					err := r.queue.Enqueue(model.RunnerEvent{
						TaskID:  task.ID,
						Event:   model.RunnerEventFailed,
						Version: task.Version,
						Reason:  err.Error(),
					})
					if err != nil {
						r.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, task.ID, err))
					}
					return nil
				}
				// The "started" event is submitted by the driver on startup
				return nil
			})
		}
	}

	return g.Wait()
}

// Load rehydrates this runner's sandboxes at boot, before Poll admits new work.
// The statefile enumerates; Status answers liveness per record. An up sandbox
// gets the same per-task supervise goroutine as the live path, which reattaches
// to the recorded run; a parked husk or a gone sandbox is a lost-report
// backstop, and a gone sandbox's dangling record is dropped (nothing to reuse
// or destroy). The semaphore is seeded with the up count last, so it may exceed
// capacity in over-limit scenarios — that's fine.
func (r *Runner) Load(ctx context.Context) error {
	recs, err := r.store.List()
	if err != nil {
		return fmt.Errorf("failed to list records: %w", err)
	}
	var running int64
	for _, rec := range recs {
		sb, err := r.backend.Open(recordHandle(rec))
		if err != nil {
			r.log.Error("load: open", "task", rec.TaskID, "err", err)
			continue
		}
		st, err := sb.Status(ctx)
		if err != nil {
			r.log.Error("load: status", "task", rec.TaskID, "err", err)
			continue
		}
		switch st {
		case backend.StatusUp: // container running / VM RUNNING: re-attach
			e := r.entry(rec.TaskID)
			e.mu.Lock()
			e.sb = sb
			r.supervise(ctx, e, rec.TaskID, rec.Version, sb, nil)
			e.mu.Unlock()
			running++
		case backend.StatusParked: // stopped container / SUSPENDED VM (husk preserved)
			r.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
		case backend.StatusGone: // removed / TERMINATED: the bound sandbox vanished
			r.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
			if err := r.store.Remove(rec.TaskID); err != nil {
				r.log.Error("load: remove dangling record", "task", rec.TaskID, "err", err)
			}
		}
	}
	r.sem.Set(running)
	r.log.Info("initialized running sandbox count", "count", running)
	return nil
}

// failIfTaskRunning emits a "failed" event for a task whose sandbox is no longer
// running but whose server status is still RUNNING — the driver's terminal report was
// lost, so "failed" is the honest outcome (see the driver-owned-events proposal).
func (r *Runner) failIfTaskRunning(ctx context.Context, taskID, version int64) {
	task, err := r.client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
	if err != nil {
		r.log.Error("failed to get task", "task", taskID, "err", err)
		return
	}
	if task.Task.Status != gritzv1.TaskStatus_RUNNING {
		return
	}
	r.log.Error("load: sandbox exited without reporting", "task", taskID)
	// Scope the backstop to the run that exited: a legacy record's version 0
	// bypasses the guard, a stamped version only applies while the task is still
	// on that run.
	if err := r.queue.Enqueue(model.RunnerEvent{
		TaskID:  taskID,
		Version: version,
		Event:   model.RunnerEventFailed,
		Reason:  "sandbox exited",
	}); err != nil {
		r.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, taskID, err))
		return
	}
}

// recordHandle returns the handle a record tracks.
func recordHandle(rec taskstate.Record) backend.Handle {
	return backend.Handle{Type: rec.Type, ID: rec.ID, Data: rec.Data}
}

// entry returns the task's registry entry, creating it if this process holds
// none yet. Entries are never deleted: one is a few words, and a task keeps
// its entry across runs.
func (r *Runner) entry(taskID int64) *taskEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.tasks[taskID]
	if !ok {
		e = &taskEntry{}
		r.tasks[taskID] = e
	}
	return e
}

// sandbox returns the sandbox rec tracks: the entry's, if it is that sandbox,
// otherwise rec's handle opened, which the entry then holds. Open ignores Data,
// so a handle an older runner recorded (which carries the run version there)
// opens like any other. The caller holds e.mu.
func (r *Runner) sandbox(e *taskEntry, rec taskstate.Record) (backend.Sandbox, error) {
	if e.sb != nil && e.sb.Handle().ID == rec.ID {
		return e.sb, nil
	}
	sb, err := r.backend.Open(recordHandle(rec))
	if err != nil {
		return nil, err
	}
	e.sb = sb
	return sb, nil
}

// handle returns the tracked handle for a task. ok is false when no record
// exists (the runner never started the task, or its sandbox was removed).
func (r *Runner) handle(taskID int64) (backend.Handle, bool, error) {
	rec, ok, err := r.store.Read(taskID)
	if err != nil || !ok {
		return backend.Handle{}, false, err
	}
	return recordHandle(rec), true, nil
}

// status opens a recorded handle and reports its sandbox's status. Open ignores
// Data, so a handle an older runner recorded (which carries the run version
// there) opens like any other.
func (r *Runner) status(ctx context.Context, h backend.Handle) (backend.Status, error) {
	sb, err := r.backend.Open(h)
	if err != nil {
		return backend.StatusUnknown, err
	}
	return sb.Status(ctx)
}

// Running reports whether the task's sandbox is up, asking the status of the
// handle the store tracks for it.
func (r *Runner) Running(ctx context.Context, taskID int64) (bool, error) {
	h, ok, err := r.handle(taskID)
	if err != nil || !ok {
		return false, err
	}
	st, err := r.status(ctx, h)
	if err != nil {
		return false, err
	}
	return st == backend.StatusUp, nil
}

// Remove destroys the task's sandbox and deletes its record. The record is
// removed only after the destroy succeeds, so a failed destroy leaves the task
// tracked and the removal is retried. Destroying a gone sandbox is not an
// error, so a record whose sandbox vanished is simply dropped. A supervise
// goroutine still following the destroyed sandbox is cancelled, and reports
// the run lost.
func (r *Runner) Remove(ctx context.Context, taskID int64) error {
	e := r.entry(taskID)
	e.mu.Lock()
	defer e.mu.Unlock()
	rec, ok, err := r.store.Read(taskID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	sb, err := r.sandbox(e, rec)
	if err != nil {
		return err
	}
	if err := sb.Destroy(ctx); err != nil {
		return err
	}
	if e.run != nil {
		e.run.cancel(errRemoved)
	}
	e.sb = nil
	return r.store.Remove(taskID)
}

// Kill asks the driver of the task's sandbox to stop the recorded run. It
// reports whether a running run was stopped — in that case the driver owns the
// terminal event report (see the driver-owned-events proposal). A sandbox that
// is not up has no driver to ask, so nothing is stopped; neither is a run that
// already finished or is not the recorded version.
func (r *Runner) Kill(ctx context.Context, task *model.Task) (bool, error) {
	e := r.entry(task.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	rec, ok, err := r.store.Read(task.ID)
	if err != nil || !ok {
		return false, err
	}
	sb, err := r.sandbox(e, rec)
	if err != nil {
		return false, err
	}
	st, err := sb.Status(ctx)
	if err != nil {
		return false, err
	}
	if st != backend.StatusUp {
		return false, nil
	}
	client, err := sb.Dial(ctx, "")
	if err != nil {
		return false, err
	}
	r.log.Info("stopping run", "task", task.ID, "version", rec.Version)
	resp, err := client.Stop(ctx, &driverv1.StopRequest{Version: rec.Version})
	if err != nil {
		return false, fmt.Errorf("failed to stop run: %w", err)
	}
	return resp.GetStopped(), nil
}

// spec assembles the sandbox spec for a task: the task token, the run's
// environment and secrets, and the agent config with the injected gritz MCP
// server.
func (r *Runner) spec(ctx context.Context, task *model.Task) (*backend.Spec, error) {
	ws, err := r.workspaces.Get(task.Workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}
	if err := r.backend.ValidateWorkspace(ws); err != nil {
		return nil, fmt.Errorf("invalid workspace %q: %w", task.Workspace, err)
	}

	// Mint the task token via the server rather than signing it locally: the runner
	// supplies only the task id and capability flags and the server derives the
	// task's workspace/runner/org from the row and signs a narrow app JWT. The
	// driver and injected MCP server present it directly to the server.
	tokenResp, err := r.client.CreateTaskToken(ctx, &gritzv1.CreateTaskTokenRequest{
		TaskId:       task.ID,
		Capabilities: ws.Capabilities,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create task token: %w", err)
	}
	token := tokenResp.Token

	// Build agent config
	cfg := ws.AgentConfig()
	mcpArgs := []string{
		"tool", "agent-mcp",
		"--server", r.serverURL,
		"--task", fmt.Sprint(task.ID),
		"--runner", task.Runner,
		"--workspace", task.Workspace,
		"--token", token,
	}
	for _, capability := range ws.Capabilities {
		mcpArgs = append(mcpArgs, "--capability", capability)
	}
	cfg.McpServers["gritz"] = agent.McpServer{
		Type:    "stdio",
		Command: backend.BinaryPath,
		Args:    mcpArgs,
	}
	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	return &backend.Spec{
		TaskID:    task.ID,
		Version:   task.Version,
		Workspace: ws,
		ServerURL: r.serverURL,
		Token:     token,
		// The agent's tools (the git credential helper, the GitHub MCP server)
		// reach the server through these.
		Env: map[string]string{
			"GRITZ_TASK_ID": fmt.Sprint(task.ID),
			"GRITZ_TOKEN":   token,
			"GRITZ_SERVER":  r.serverURL,
		},
		Secrets: maps.Clone(ws.Secrets),
		Files: []backend.File{
			// Allow non-root agents to write to this directory.
			{Path: path.Dir(agent.DefaultConfigStore.Path(task.ID)), Mode: 0777, Dir: true},
			{Path: agent.DefaultConfigStore.Path(task.ID), Data: cfgData, Mode: 0666},
			// Pre-create the log dir 0777 so a non-root driver can write the
			// append-only /gritz/log file (the driver's MkdirAll is only a
			// fallback for a directly-invoked driver).
			{Path: path.Dir(agent.DefaultLogPath), Mode: 0777, Dir: true},
		},
	}, nil
}

// errRunning is returned by Start when a supervise goroutine still follows the
// task's previous run, so nothing was started. Running can report false then:
// the sandbox may already be parking after its run (a Lambda VM suspending).
var errRunning = errors.New("sandbox is still running")

// Start brings up the task's sandbox and spawns a supervise goroutine that
// starts the task's run on it. With no record a sandbox is created, and its
// handle recorded straight away, before anything else can fail; with one, the
// recorded sandbox is reused, since a task is bound 1:1 to it. Either way the
// record is stamped with the task's version. If the recorded sandbox is gone,
// Start drops the record and returns backend.ErrGone; any other failure to
// start it keeps the record, so Remove can still destroy the sandbox.
//
// Start returns once the platform reports the sandbox running: waiting for the
// driver and starting the run happen in the supervise goroutine, so a slow
// sandbox does not hold up the poll pass. A failure there is reported as the
// run's "failed".
//
// If a supervise goroutine still follows the task's previous run, Start
// returns errRunning without starting anything, and the caller gives back the
// concurrency slot it acquired.
func (r *Runner) Start(ctx context.Context, task *model.Task) error {
	e := r.entry(task.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.run != nil {
		return errRunning
	}
	rec, ok, err := r.store.Read(task.ID)
	if err != nil {
		return err
	}
	spec, err := r.spec(ctx, task)
	if err != nil {
		return err
	}
	var sb backend.Sandbox
	if ok {
		sb, err = r.sandbox(e, rec)
		if err != nil {
			return err
		}
	} else {
		sb, err = r.backend.Create(ctx, spec)
		if err != nil {
			return err
		}
		e.sb = sb
	}
	h := sb.Handle()
	if err := r.store.Write(taskstate.Record{
		TaskID:  task.ID,
		Version: task.Version,
		Type:    h.Type,
		ID:      h.ID,
		Data:    h.Data,
	}); err != nil {
		// The taskstate store is the runner's other durable local store; a write
		// failure here is the same non-transient disk failure as an outbox
		// Enqueue failure, so crash rather than lose the handle. The returned
		// error still unwinds the caller (releasing the sem slot) as ctx tears down.
		r.die(fmt.Errorf("record handle for task %d: %w", task.ID, err))
		return fmt.Errorf("failed to record task handle: %w", err)
	}
	if err := sb.Start(ctx); errors.Is(err, backend.ErrGone) {
		return r.gone(e, task.ID)
	} else if err != nil {
		return err
	}
	// Follow the run on the runner's root context: the run is started there,
	// after the record is stamped with its version, so a runner that restarts
	// in between reattaches to it.
	r.supervise(ctx, e, task.ID, task.Version, sb, r.runSpec(spec))
	return nil
}

// runSpec builds the driver.v1 RunSpec for spec. A backend whose sandboxes
// have no environment of their own adds the workspace's to the run's, under
// spec.Env.
func (r *Runner) runSpec(spec *backend.Spec) *driverv1.RunSpec {
	env := map[string]string{}
	if be, ok := r.backend.(backend.RunEnver); ok {
		maps.Copy(env, be.RunEnv(spec.Workspace))
	}
	maps.Copy(env, spec.Env)
	rs := &driverv1.RunSpec{
		TaskId:    spec.TaskID,
		ServerUrl: spec.ServerURL,
		Token:     spec.Token,
		Env:       env,
		Secrets:   maps.Clone(spec.Secrets),
	}
	for _, f := range spec.Files {
		rs.Files = append(rs.Files, &driverv1.File{
			Path: f.Path,
			Data: f.Data,
			Mode: f.Mode,
			Dir:  f.Dir,
		})
	}
	return rs
}

// gone handles a vanished bound sandbox: the task's recorded sandbox no longer
// exists, so drop the dangling record (nothing to reuse or destroy) and return
// backend.ErrGone. The caller's error path emits "failed" and releases the
// acquired sem slot. A subsequent explicit start/restart, having no handle, is
// then a legitimate first-start-fresh. The caller holds e.mu.
func (r *Runner) gone(e *taskEntry, taskID int64) error {
	e.sb = nil
	if err := r.store.Remove(taskID); err != nil {
		r.log.Error("failed to remove dangling record", "task", taskID, "err", err)
	}
	return backend.ErrGone
}

// supervise spawns the goroutine that follows the task's run version on sb and
// registers its taskRun on e, so Remove can cancel it. With spec set the
// goroutine starts the run first; with spec nil it reattaches to a run already
// started. The caller holds e.mu.
func (r *Runner) supervise(ctx context.Context, e *taskEntry, taskID, version int64, sb backend.Sandbox, spec *driverv1.RunSpec) {
	ctx, cancel := context.WithCancelCause(ctx)
	t := &taskRun{
		r:       r,
		taskID:  taskID,
		version: version,
		spec:    spec,
		sb:      sb,
		cancel:  cancel,
	}
	e.run = t
	go func() {
		defer cancel(nil)
		t.follow(ctx)
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.run == t {
			e.run = nil
		}
	}()
}

// follow starts the run if spec is set, attaches to it until it finishes, then
// releases the concurrency slot and reports the outcome if the driver did not.
// Cancelled by the runner's shutdown, it returns with the slot held and no
// event: the sandbox stays alive for next-boot rehydration. Cancelled by
// Remove, the run is lost. A run that could not be started or was lost gets
// the runner's fallback "failed", scoped to its version (a legacy record's
// version 0 bypasses the guard).
func (t *taskRun) follow(ctx context.Context) {
	r, taskID, version := t.r, t.taskID, t.version
	lost, err := t.run(ctx)
	if err != nil {
		if !errors.Is(context.Cause(ctx), errRemoved) {
			return // shutdown: leave the sandbox alive for next-boot rehydration
		}
		lost = "sandbox removed before the run finished"
	}
	r.sem.Release(1)
	r.wake.Wake()
	if lost == "" {
		r.log.Info("run finished, driver already reported", "task", taskID, "version", version)
		return
	}
	r.log.Error("run lost", "task", taskID, "version", version, "reason", lost)
	if err := r.queue.Enqueue(model.RunnerEvent{
		TaskID:  taskID,
		Version: version,
		Event:   model.RunnerEventFailed,
		Reason:  lost,
	}); err != nil {
		r.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, taskID, err))
		return
	}
}

// run starts the run on its sandbox with its spec, unless spec is nil, then
// attaches to it. A run that could not be started is reported lost with why,
// after its sandbox is parked: nothing runs on it. The only error is ctx's.
func (t *taskRun) run(ctx context.Context) (lost string, err error) {
	if t.spec != nil {
		if err := t.startRun(ctx); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			t.r.log.Error("failed to start run", "task", t.taskID, "version", t.version, "err", err)
			t.park(ctx)
			return err.Error(), nil
		}
	}
	return t.attach(ctx)
}

// startRun waits for the sandbox's driver to answer Status, backing off
// exponentially, then starts the run with its spec on it, returning once the
// driver has accepted it. A Run that is rejected because another stream is
// open is retried. The driver has the runner's readyTimeout to do both.
func (t *taskRun) startRun(ctx context.Context) error {
	r, version := t.r, t.version
	ctx, cancel := context.WithTimeout(ctx, r.readyTimeout)
	defer cancel()
	client, err := t.sb.Dial(ctx, "")
	if err != nil {
		return err
	}
	delay := r.retryInterval
	for {
		_, err := client.Status(ctx, &driverv1.StatusRequest{})
		if err == nil {
			break
		}
		if sleep(ctx, delay) != nil {
			return fmt.Errorf("driver server did not become ready: %w", err)
		}
		delay = min(delay*2, readyMaxInterval)
	}
	for {
		err := accept(ctx, client, &driverv1.RunRequest{Version: version, Spec: t.spec})
		if err == nil {
			return nil
		}
		if connect.CodeOf(err) != connect.CodeUnavailable || sleep(ctx, r.retryInterval) != nil {
			return fmt.Errorf("failed to start run %d: %w", version, err)
		}
	}
}

// accept opens a Run stream and closes it after its Accepted: the run keeps
// going without it.
func accept(ctx context.Context, client driverv1connect.DriverServiceClient, req *driverv1.RunRequest) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.Run(ctx, req)
	if err != nil {
		return err
	}
	defer stream.Close()
	if !stream.Receive() {
		return cmp.Or(stream.Err(), errors.New("run stream ended before it was accepted"))
	}
	if stream.Msg().GetAccepted() == nil {
		return fmt.Errorf("unexpected first run response: %v", stream.Msg())
	}
	return nil
}

// attach follows the run on its sandbox until its Finished, then parks the
// sandbox. It returns why the driver's report of the run's outcome was lost, or
// "" if the driver reported it. The only error is ctx's.
//
// A dropped stream is reattached while the sandbox is up, backing off
// exponentially. A sandbox that is parked or gone before Finished arrived, or a
// driver with no such run, is a lost run. Reattaching gives up once lostTimeout
// has passed without a good message — an accepted stream — from the driver:
// attach parks the sandbox and reports the run lost. It returns only once the
// park succeeded or the sandbox is gone, so the sandbox is never up with no
// supervise following it; while the platform cannot park it, attach keeps
// retrying.
func (t *taskRun) attach(ctx context.Context) (lost string, err error) {
	r, taskID, version, sb := t.r, t.taskID, t.version, t.sb
	lastGood := time.Now()
	delay := r.retryInterval
	var lastErr string
	for {
		finished, accepted, err := attachOnce(ctx, sb, version)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if finished != nil {
			t.park(ctx)
			if !finished.GetReported() {
				return "driver did not report the run's outcome", nil
			}
			return "", nil
		}
		if isTerminal(err) {
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come.
			r.log.Error("driver has no such run", "task", taskID, "version", version, "err", err)
			if connect.CodeOf(err) == connect.CodeNotFound {
				// Nothing runs on the sandbox, which would otherwise stay up.
				// On FailedPrecondition or Aborted a newer run is active on
				// it, which parking would kill.
				t.park(ctx)
			}
			return "driver has no such run", nil
		}
		if st, stErr := sb.Status(ctx); stErr != nil {
			err = fmt.Errorf("%w (status: %w)", err, stErr)
		} else if st != backend.StatusUp {
			r.log.Error("sandbox stopped before its run finished", "task", taskID, "status", st, "version", version, "err", err)
			if st == backend.StatusParked {
				// Let a sandbox that is still stopping (a SUSPENDING VM)
				// settle, so it is not reported up once supervise is done.
				t.park(ctx)
			}
			return "sandbox stopped before the run finished", nil
		}
		if accepted {
			// The stream was live until it dropped: the driver's idle watchdog
			// would have ended it otherwise.
			lastGood = time.Now()
			delay = r.retryInterval
			lastErr = ""
		}
		if time.Since(lastGood) >= r.lostTimeout {
			// Give up on the driver; park the sandbox instead. While that
			// fails, keep retrying the attach too in case the driver is back.
			parkErr := sb.Park(ctx)
			if parkErr == nil || errors.Is(parkErr, backend.ErrGone) {
				r.log.Error("run lost: driver unreachable, sandbox parked", "task", taskID, "version", version, "err", err)
				return "driver unreachable", nil
			}
			err = fmt.Errorf("failed to park sandbox of lost run: %w (driver: %w)", parkErr, err)
		}
		if msg := err.Error(); msg != lastErr {
			r.log.Warn("run stream dropped, reattaching", "task", taskID, "version", version, "err", err)
			lastErr = msg
		}
		if err := sleep(ctx, delay); err != nil {
			return "", err
		}
		delay = min(delay*2, r.retryMaxInterval)
	}
}

// attachOnce opens a Run stream for version without a spec and reads it until
// the run's Finished, which it returns. Otherwise it returns why the stream
// ended. accepted reports whether the driver accepted the stream before it
// ended.
func attachOnce(ctx context.Context, sb backend.Sandbox, version int64) (finished *driverv1.Finished, accepted bool, err error) {
	client, err := sb.Dial(ctx, "")
	if err != nil {
		return nil, false, err
	}
	stream, err := client.Run(ctx, &driverv1.RunRequest{Version: version})
	if err != nil {
		return nil, false, err
	}
	defer stream.Close()
	for stream.Receive() {
		msg := stream.Msg()
		if msg.GetAccepted() != nil {
			accepted = true
		}
		if finished := msg.GetFinished(); finished != nil {
			return finished, accepted, nil
		}
	}
	return nil, accepted, cmp.Or(stream.Err(), errors.New("run stream ended before Finished"))
}

// isTerminal reports whether a Run error means the run can never be attached
// to: NotFound (no such run), FailedPrecondition (stale version) or Aborted
// (another version is running).
func isTerminal(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound, connect.CodeFailedPrecondition, connect.CodeAborted:
		return true
	}
	return false
}

// park parks the run's sandbox once the run is over. Failures are logged: the
// run's outcome is already known.
func (t *taskRun) park(ctx context.Context) {
	if err := t.sb.Park(ctx); err != nil && !errors.Is(err, backend.ErrGone) {
		t.r.log.Error("failed to park sandbox after its run", "task", t.taskID, "err", err)
	}
}

// sleep waits for d or until ctx is done, returning ctx's error in that case.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Prune removes sandboxes for archived tasks.
func (r *Runner) Prune(ctx context.Context) error {
	recs, err := r.store.List()
	if err != nil {
		return fmt.Errorf("failed to list records: %w", err)
	}

	// Check each sandbox that is not up and remove it if its task is archived.
	// Both a parked husk and a gone sandbox count as "no live sandbox"; Destroy
	// is idempotent, so removing a gone record's (absent) sandbox is a no-op.
	for _, rec := range recs {
		st, err := r.status(ctx, backend.Handle{Type: rec.Type, ID: rec.ID, Data: rec.Data})
		if err != nil {
			r.log.Error("failed to get sandbox status", "task", rec.TaskID, "err", err)
			continue
		}
		if st == backend.StatusUp {
			continue
		}
		// Fetch task
		resp, err := r.client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: rec.TaskID})
		if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			r.log.Error("failed to get task", "task", rec.TaskID, "err", err)
			continue
		}
		// Remove sandbox if task is archived or deleted
		if connect.CodeOf(err) == connect.CodeNotFound || resp.Task.Archived {
			if err := r.Remove(ctx, rec.TaskID); err != nil {
				r.log.Error("failed to remove sandbox", "task", rec.TaskID, "err", err)
			} else {
				r.log.Info("sandbox removed", "task", rec.TaskID)
			}
		}
	}

	return nil
}
