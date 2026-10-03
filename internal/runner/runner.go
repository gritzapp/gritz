package runner

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/icholy/gritz/internal/gritzclient"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/runner/agent"
	"github.com/icholy/gritz/internal/runner/backend"
	"github.com/icholy/gritz/internal/runner/taskstate"
	"github.com/icholy/gritz/internal/runner/workspace"
	"github.com/icholy/gritz/internal/x/outbox"
	"github.com/icholy/gritz/internal/x/safesem"
	"github.com/icholy/gritz/internal/x/wakeup"
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
}

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
				// No running sandbox to signal: there is no driver to
				// complete the cancel, so emit "stopped" to land the task in
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
				if err := r.Start(ctx, task); err != nil {
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
				if err := r.Start(ctx, task); err != nil {
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
// The statefile enumerates; Probe answers liveness per record. A running sandbox
// gets the same per-handle supervise goroutine as the live path (Wait
// re-attaches); an exited husk or a gone sandbox is a lost-report backstop, and a
// gone sandbox's dangling record is dropped (nothing to reuse or destroy). The
// semaphore is seeded with the running count last, so it may exceed capacity in
// over-limit scenarios — that's fine.
func (r *Runner) Load(ctx context.Context) error {
	recs, err := r.store.List()
	if err != nil {
		return fmt.Errorf("failed to list records: %w", err)
	}
	var running int64
	for _, rec := range recs {
		h := backend.Handle{Type: rec.Type, ID: rec.ID, Data: rec.Data}
		st, err := r.backend.Probe(ctx, h)
		if err != nil {
			r.log.Error("load: probe", "task", rec.TaskID, "err", err)
			continue
		}
		switch st {
		case backend.StateRunning: // container up / VM RUNNING: re-attach
			go r.supervise(ctx, rec.TaskID, rec.Version, h)
			running++
		case backend.StateExited: // stopped container / SUSPENDED VM (husk preserved)
			r.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
		case backend.StateGone: // removed / TERMINATED: the bound sandbox vanished
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

// handle returns the tracked handle for a task. ok is false when no record
// exists (the runner never started the task, or its sandbox was removed).
func (r *Runner) handle(taskID int64) (backend.Handle, bool, error) {
	rec, ok, err := r.store.Read(taskID)
	if err != nil || !ok {
		return backend.Handle{}, false, err
	}
	return backend.Handle{Type: rec.Type, ID: rec.ID, Data: rec.Data}, true, nil
}

// Running reports whether the task's sandbox is currently running, probing the
// handle the store tracks for it.
func (r *Runner) Running(ctx context.Context, taskID int64) (bool, error) {
	h, ok, err := r.handle(taskID)
	if err != nil || !ok {
		return false, err
	}
	state, err := r.backend.Probe(ctx, h)
	if err != nil {
		return false, err
	}
	return state == backend.StateRunning, nil
}

// List composes the orchestrator's sandbox view from the store: one Sandbox
// per tracked record, its state resolved by probing the backend handle.
func (r *Runner) List(ctx context.Context) ([]backend.Sandbox, error) {
	records, err := r.store.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list records: %w", err)
	}
	sandboxes := make([]backend.Sandbox, 0, len(records))
	for _, rec := range records {
		state, err := r.backend.Probe(ctx, backend.Handle{Type: rec.Type, ID: rec.ID, Data: rec.Data})
		if err != nil {
			return nil, fmt.Errorf("failed to probe task %d: %w", rec.TaskID, err)
		}
		sandboxes = append(sandboxes, backend.Sandbox{TaskID: rec.TaskID, State: state})
	}
	return sandboxes, nil
}

// Remove destroys the task's sandbox and deletes its record. The record is
// removed only after the backend destroy succeeds, so a failed destroy leaves
// the task tracked and the removal is retried.
func (r *Runner) Remove(ctx context.Context, taskID int64) error {
	h, ok, err := r.handle(taskID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := r.backend.Destroy(ctx, h); err != nil {
		return err
	}
	return r.store.Remove(taskID)
}

// Kill stops the task's sandbox if it is running. It reports whether a
// running sandbox was signalled — in that case the driver owns the
// terminal event report (see the driver-owned-events proposal).
func (r *Runner) Kill(ctx context.Context, task *model.Task) (bool, error) {
	h, ok, err := r.handle(task.ID)
	if err != nil || !ok {
		return false, err
	}
	return r.backend.Signal(ctx, h)
}

// spec assembles the sandbox spec for a task: the task token, the driver
// invocation, and the agent config with the injected gritz MCP server.
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

	env := []string{
		fmt.Sprintf("GRITZ_TASK_ID=%d", task.ID),
		fmt.Sprintf("GRITZ_TOKEN=%s", token),
		"GRITZ_SERVER=" + r.serverURL,
	}
	// Declared workspace secrets enter the sandbox as ordinary environment
	// variables; GRITZ_SECRETS carries their names so the driver can look the
	// values up in its own environment and mask them in the log it ships. The
	// names travel by env rather than in the agent config because the driver
	// opens its log before it reads that config — the mask has to exist before
	// the first shipped byte.
	if names := ws.SecretNames(); len(names) > 0 {
		for _, name := range names {
			env = append(env, name+"="+ws.Secrets[name])
		}
		env = append(env, "GRITZ_SECRETS="+strings.Join(names, ","))
	}

	return &backend.Spec{
		TaskID:    task.ID,
		Workspace: ws,
		Cmd: []string{
			backend.BinaryPath, "driver",
			"--server", r.serverURL,
			"--task", fmt.Sprint(task.ID),
			"--token", token,
		},
		Env: env,
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

// Start ensures the task's sandbox is running, records its handle, and spawns a
// supervise goroutine to observe its exit. It is idempotent: if the tracked
// handle is already running it returns without relaunching. Otherwise the prior
// handle (if any) is passed to Launch so the backend adopts the existing sandbox;
// a vanished sandbox surfaces as backend.ErrGone, which fails the task and drops
// the dangling record. The returned handle is persisted BEFORE supervise starts —
// the store write is the runner's job, not the backend's.
func (r *Runner) Start(ctx context.Context, task *model.Task) error {
	// Resolve the tracked handle first: a still-running sandbox makes Start a
	// no-op, and we skip minting a token for it. An exited handle is passed to
	// Launch so the backend can adopt the existing sandbox. A pre-Launch Probe
	// short-circuits a gone sandbox without minting a token, but Launch's ErrGone
	// guard is the authoritative one (Probe→Launch is racy).
	var reuse *backend.Handle
	if h, ok, err := r.handle(task.ID); err != nil {
		return err
	} else if ok {
		state, err := r.backend.Probe(ctx, h)
		if err != nil {
			return err
		}
		if state == backend.StateRunning {
			return nil
		}
		if state == backend.StateGone {
			return r.gone(task.ID)
		}
		reuse = &h
	}

	spec, err := r.spec(ctx, task)
	if err != nil {
		return err
	}

	h, err := r.backend.Launch(ctx, spec, reuse)
	if errors.Is(err, backend.ErrGone) {
		return r.gone(task.ID)
	}
	if err != nil {
		return err
	}
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
	// Observe the exit on the runner's root context: Wait is level-triggered and
	// spawned after the record is persisted, so an exit in the launch→persist
	// window is not lost.
	go r.supervise(ctx, task.ID, task.Version, h)
	return nil
}

// gone handles a vanished bound sandbox: the task's recorded sandbox no longer
// exists, so drop the dangling record (nothing to reuse or destroy) and return
// backend.ErrGone. The caller's error path emits "failed" and releases the
// acquired sem slot. A subsequent explicit start/restart, having no handle, is
// then a legitimate first-start-fresh.
func (r *Runner) gone(taskID int64) error {
	if err := r.store.Remove(taskID); err != nil {
		r.log.Error("failed to remove dangling record", "task", taskID, "err", err)
	}
	return backend.ErrGone
}

// supervise blocks in the backend's per-handle Wait until the sandbox reaches a
// terminal outcome, then releases the concurrency slot and reports the exit. A
// context.Canceled error means the runner is shutting down — the sandbox stays
// alive for next-boot rehydration, so no slot is released and no event emitted.
// Otherwise a non-zero exit code means the driver's report was lost, so "failed"
// is the honest outcome (see the driver-owned-events proposal); exit 0 means the
// driver already reported and nothing is owed.
func (r *Runner) supervise(ctx context.Context, taskID, version int64, h backend.Handle) {
	code, err := r.backend.Wait(ctx, h)
	if errors.Is(err, context.Canceled) {
		return // shutdown: leave the sandbox alive for next-boot rehydration
	}
	r.sem.Release(1)
	r.wake.Wake()
	if code == 0 {
		r.log.Info("sandbox exited, driver already reported", "task", taskID)
		return
	}
	r.log.Error("sandbox exited without reporting", "task", taskID, "exitCode", int(code))
	// Scope the backstop to the run that exited: a legacy record's version 0
	// bypasses the guard, a stamped version only applies while the task is still
	// on that run.
	if err := r.queue.Enqueue(model.RunnerEvent{
		TaskID:  taskID,
		Version: version,
		Event:   model.RunnerEventFailed,
		Reason:  fmt.Sprintf("sandbox exited with status code %d", code),
	}); err != nil {
		r.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, taskID, err))
		return
	}
}

// Prune removes sandboxes for archived tasks.
func (r *Runner) Prune(ctx context.Context) error {
	sandboxes, err := r.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list sandboxes: %w", err)
	}

	// Check each non-running sandbox's task status and remove if archived. Both
	// an exited husk and a gone sandbox count as "no live sandbox"; Destroy is
	// idempotent, so removing a gone record's (absent) sandbox is a no-op.
	for _, sb := range sandboxes {
		if sb.State == backend.StateRunning {
			continue
		}
		// Fetch task
		resp, err := r.client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: sb.TaskID})
		if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			r.log.Error("failed to get task", "task", sb.TaskID, "err", err)
			continue
		}
		// Remove sandbox if task is archived or deleted
		if connect.CodeOf(err) == connect.CodeNotFound || resp.Task.Archived {
			if err := r.Remove(ctx, sb.TaskID); err != nil {
				r.log.Error("failed to remove sandbox", "task", sb.TaskID, "err", err)
			} else {
				r.log.Info("sandbox removed", "task", sb.TaskID)
			}
		}
	}

	return nil
}
