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

// TaskManager orchestrates the runner's tasks: it polls the server for their
// commands, builds the specs their sandboxes are created and run with, admits
// runs under the concurrency limit, and keeps the registry of the tasks that
// have a sandbox, each run by a TaskRunner.
type TaskManager struct {
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
	// pollInterval is Run's fallback interval between Poll passes, and its
	// interval between Prune passes.
	pollInterval time.Duration
	fatal        context.CancelCauseFunc
	// events is the unbuffered channel the TaskRunners send their events on;
	// HandleEvents drains it.
	events chan TaskEvent
	// env is shared by the manager's TaskRunners.
	env *taskEnv

	// mu guards tasks, the registry of the tasks this TaskManager has a sandbox
	// for. It mirrors taskstate/: a task has a TaskRunner if and only if it
	// has a record, and every change writes through to both. Only Load reads
	// taskstate/, to rebuild the registry at boot.
	mu    sync.Mutex
	tasks map[int64]*TaskRunner
}

const (
	// reattachInterval is the first pause between attempts to reattach to a
	// run.
	reattachInterval = 250 * time.Millisecond
	// reattachMaxInterval caps the backoff between attempts to reattach.
	reattachMaxInterval = 10 * time.Second
	// driverLostTimeout is how long Run keeps reattaching without a good
	// message from the driver before it parks the sandbox and reports the run
	// lost.
	driverLostTimeout = 2 * time.Minute
	// driverReadyTimeout is how long Run waits for a started sandbox's driver
	// to answer Status and accept its run. It counts whatever the image's
	// entrypoint does before it execs the driver, and a Lambda VM's boot.
	driverReadyTimeout = time.Minute
	// readyMaxInterval caps the backoff between attempts to reach a starting
	// driver.
	readyMaxInterval = 2 * time.Second
	// defaultPollInterval is Run's poll interval when TaskManagerOptions
	// leaves it unset.
	defaultPollInterval = 30 * time.Second
)

type TaskManagerOptions struct {
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
	// PollInterval is how long Run waits for a wake-up before it polls again,
	// and how often it prunes. Defaults to 30s.
	PollInterval time.Duration
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

func NewTaskManager(opts TaskManagerOptions) (*TaskManager, error) {
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

	// Default Fatal to a no-op so die can call m.fatal unconditionally. A nil
	// Fatal means the caller opted out of crash-on-write-failure (only tests that
	// never trigger die); production always wires the CancelCauseFunc.
	fatal := opts.Fatal
	if fatal == nil {
		fatal = func(error) {}
	}

	m := &TaskManager{
		backend:      opts.Backend,
		store:        opts.Store,
		client:       opts.Client,
		serverURL:    opts.ServerURL,
		workspaces:   opts.Workspaces,
		runnerID:     opts.RunnerID,
		concurrency:  concurrency,
		sem:          safesem.New(concurrency),
		log:          log,
		queue:        opts.Queue,
		wake:         wakeup.New(),
		pollInterval: cmp.Or(opts.PollInterval, defaultPollInterval),
		fatal:        fatal,
		events:       make(chan TaskEvent),
		tasks:        map[int64]*TaskRunner{},
	}
	m.env = &taskEnv{
		store:            opts.Store,
		log:              log,
		events:           m.events,
		forget:           m.forget,
		retryInterval:    reattachInterval,
		retryMaxInterval: reattachMaxInterval,
		lostTimeout:      driverLostTimeout,
		readyTimeout:     driverReadyTimeout,
	}
	return m, nil
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
func (m *TaskManager) die(err error) { m.fatal(FatalError{err}) }

// HandleEvents handles the TaskRunners' events until ctx is done. It must be
// running before Load is called, and run on the same context as Load, Poll and
// the Run goroutines they start, so that no TaskRunner blocks sending to it
// after it has stopped.
//
// A TaskFatalEvent terminates the runner; a TaskLostEvent is reported to the
// server as the run's "failed". Handling an event never takes a TaskRunner's
// lock, so a TaskRunner may send while holding it.
func (m *TaskManager) HandleEvents(ctx context.Context) {
	for {
		select {
		case ev := <-m.events:
			m.handle(ev)
		case <-ctx.Done():
			return
		}
	}
}

// handle handles one TaskRunner event; see HandleEvents.
func (m *TaskManager) handle(ev TaskEvent) {
	switch ev := ev.(type) {
	case TaskFatalEvent:
		m.die(ev.Err)
	case TaskLostEvent:
		if err := m.queue.Enqueue(model.RunnerEvent{
			TaskID:  ev.TaskID,
			Version: ev.Version,
			Event:   model.RunnerEventFailed,
			Reason:  ev.Reason,
		}); err != nil {
			m.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, ev.TaskID, err))
		}
	default:
		panic(fmt.Sprintf("unknown task event %T", ev))
	}
}

// Wake signals Run to poll immediately. Wake-ups coalesce: a burst of them
// before the next pass results in a single pass.
func (m *TaskManager) Wake() { m.wake.Wake() }

// Run polls the server for the runner's tasks until ctx is done, then returns
// context.Cause(ctx). It polls again as soon as Wake is called or a run
// releases its concurrency slot, and at the latest after PollInterval. Every
// PollInterval it also runs Prune, between two Poll passes, so the two never
// overlap.
//
// HandleEvents must be running, and Load must have returned, before Run is
// called, all on the same ctx.
func (m *TaskManager) Run(ctx context.Context) error {
	prune := time.NewTicker(m.pollInterval)
	defer prune.Stop()
	for {
		if err := m.Poll(ctx); err != nil {
			m.log.Error("failed to poll tasks", "err", err)
		}
		select {
		case <-prune.C:
			if err := m.Prune(ctx); err != nil {
				m.log.Error("failed to prune sandboxes", "err", err)
			}
		default:
		}
		select {
		case <-m.wake:
		case <-time.After(m.pollInterval):
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

func (m *TaskManager) Close() error {
	return m.backend.Close()
}

// RegisterWorkspaces sends the available workspace names to the server.
func (m *TaskManager) RegisterWorkspaces(ctx context.Context) error {
	workspaces := make([]*gritzv1.RegisteredWorkspace, 0, len(m.workspaces.Workspaces))
	for name, ws := range m.workspaces.Workspaces {
		workspaces = append(workspaces, &gritzv1.RegisteredWorkspace{
			Name:        name,
			Description: ws.Description,
		})
	}
	_, err := m.client.RegisterWorkspaces(ctx, &gritzv1.RegisterWorkspacesRequest{
		RunnerId:   m.runnerID,
		Workspaces: workspaces,
	})
	if err != nil {
		return fmt.Errorf("failed to register workspaces: %w", err)
	}
	m.log.Info("registered workspaces", "runner_id", m.runnerID, "count", len(workspaces))
	return nil
}

func (m *TaskManager) Poll(ctx context.Context) error {
	resp, err := m.client.ListRunnerTasks(ctx, &gritzv1.ListRunnerTasksRequest{Runner: m.runnerID})
	if err != nil {
		return err
	}

	// Plain group (limit + join only): WithContext's derived ctx is canceled
	// when Wait returns, which would kill the Run goroutines Start spawns on
	// this ctx. Run must outlive the poll cycle.
	//
	// A pass handles each task at most once, and passes run one after another,
	// so Start can create a task's sandbox and TaskRunner without a per-task
	// lock: nothing else creates the TaskRunner of a task this pass handles.
	// Run calls Prune only between passes, so Prune does not act on the same
	// tasks either.
	var g errgroup.Group
	g.SetLimit(int(m.concurrency))

	for _, pbTask := range resp.Tasks {
		task := model.TaskFromProto(pbTask)
		switch task.Command {
		case model.TaskCommandStop:
			g.Go(func() error {
				signalled, err := m.Kill(ctx, task)
				if err != nil {
					// The stop command survives, so the kill is retried on
					// the next poll.
					m.log.Error("failed to stop task", "task", task.ID, "err", err)
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
				if err := m.queue.Enqueue(model.RunnerEvent{
					TaskID:  task.ID,
					Event:   model.RunnerEventStopped,
					Version: task.Version,
				}); err != nil {
					m.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventStopped, task.ID, err))
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
				version, err := m.version(task.ID)
				if err != nil {
					m.log.Error("failed to read task record for restart", "task", task.ID, "err", err)
					return nil
				}
				if version == task.Version {
					m.log.Debug("restart already processed, waiting for started", "task", task.ID, "version", task.Version)
					return nil
				}
				// Kill existing sandbox if running. The old run's "stopped"
				// is rejected by the status guard while the restart command is
				// pending, so the command survives until the new run's
				// "started" consumes it.
				if _, err := m.Kill(ctx, task); err != nil {
					m.log.Error("failed to kill task for restart", "task", task.ID, "err", err)
				}
				// Atomically acquire a semaphore slot before starting
				if !m.sem.TryAcquire(1) {
					m.log.Debug("concurrency limit reached, skipping task", "task", task.ID, "limit", m.concurrency)
					return nil
				}
				err = m.Start(ctx, task)
				if errors.Is(err, errRunning) {
					// Nothing was started, so the slot goes back; the
					// command is retried on a later poll.
					m.sem.Release(1)
					m.log.Debug("sandbox still running, not started", "task", task.ID)
					return nil
				}
				if err != nil {
					m.sem.Release(1) // Release the slot on failure
					m.log.Error("failed to start task", "task", task.ID, "err", err)
					err := m.queue.Enqueue(model.RunnerEvent{
						TaskID:  task.ID,
						Event:   model.RunnerEventFailed,
						Version: task.Version,
						Reason:  err.Error(),
					})
					if err != nil {
						m.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, task.ID, err))
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
					m.log.Debug("start command: task.status=running, waiting for it to finish", "task", task.ID)
					return nil
				}
				// Check if the sandbox is already running
				running, err := m.Running(ctx, task.ID)
				if err != nil {
					m.log.Error("failed to check if task is running", "task", task.ID, "err", err)
					return nil
				}
				if running {
					// Sandbox is running - do nothing, let it finish naturally
					// The command will be processed when it stops
					m.log.Debug("start command: sandbox already running, waiting for it to finish", "task", task.ID)
					return nil
				}

				// Sandbox not running - atomically acquire a semaphore slot before starting
				if !m.sem.TryAcquire(1) {
					m.log.Debug("concurrency limit reached, skipping task", "task", task.ID, "limit", m.concurrency)
					return nil
				}
				err = m.Start(ctx, task)
				if errors.Is(err, errRunning) {
					// Nothing was started, so the slot goes back; the
					// command is retried on a later poll.
					m.sem.Release(1)
					m.log.Debug("sandbox still running, not started", "task", task.ID)
					return nil
				}
				if err != nil {
					m.sem.Release(1) // Release the slot on failure
					m.log.Error("failed to start task", "task", task.ID, "err", err)
					err := m.queue.Enqueue(model.RunnerEvent{
						TaskID:  task.ID,
						Event:   model.RunnerEventFailed,
						Version: task.Version,
						Reason:  err.Error(),
					})
					if err != nil {
						m.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, task.ID, err))
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
// gets the same Run goroutine as the live path, which reattaches
// to the recorded run; a parked husk or a gone sandbox is a lost-report
// backstop, and a gone sandbox's dangling record is dropped (nothing to reuse
// or destroy). The semaphore is seeded with the up count last, so it may exceed
// capacity in over-limit scenarios — that's fine.
//
// Load rebuilds the task registry from the records it keeps: it is the only
// reader of taskstate/. A record whose handle cannot be opened is dropped, so
// every TaskRunner has a sandbox.
//
// HandleEvents must already be running: the Run goroutines Load starts are
// the first to send it events.
func (m *TaskManager) Load(ctx context.Context) error {
	recs, err := m.store.List()
	if err != nil {
		return fmt.Errorf("failed to list records: %w", err)
	}
	var running int64
	for _, rec := range recs {
		sb, err := m.backend.Open(recordHandle(rec))
		if err != nil {
			m.log.Error("load: open, dropping record", "task", rec.TaskID, "err", err)
			if err := m.store.Remove(rec.TaskID); err != nil {
				m.log.Error("load: remove unopenable record", "task", rec.TaskID, "err", err)
			}
			continue
		}
		tr := loadTaskRunner(m.env, rec, sb)
		st, err := sb.Status(ctx)
		if err != nil {
			// Keep the record and its TaskRunner: the sandbox may well be
			// there.
			m.log.Error("load: status", "task", rec.TaskID, "err", err)
		}
		switch st {
		case backend.StatusUp: // container running / VM RUNNING: re-attach
			tr.claim()
			m.run(ctx, tr, nil)
			running++
		case backend.StatusParked: // stopped container / SUSPENDED VM (husk preserved)
			m.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
		case backend.StatusGone: // removed / TERMINATED: the bound sandbox vanished
			m.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
			err := tr.dropGone()
			if err == nil {
				continue
			}
			// The record stays, so its TaskRunner does too.
			m.log.Error("load: remove dangling record", "task", rec.TaskID, "err", err)
		}
		m.register(tr)
	}
	m.sem.Set(running)
	m.log.Info("initialized running sandbox count", "count", running)
	return nil
}

// failIfTaskRunning emits a "failed" event for a task whose sandbox is no longer
// running but whose server status is still RUNNING — the driver's terminal report was
// lost, so "failed" is the honest outcome (see the driver-owned-events proposal).
func (m *TaskManager) failIfTaskRunning(ctx context.Context, taskID, version int64) {
	task, err := m.client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
	if err != nil {
		m.log.Error("failed to get task", "task", taskID, "err", err)
		return
	}
	if task.Task.Status != gritzv1.TaskStatus_RUNNING {
		return
	}
	m.log.Error("load: sandbox exited without reporting", "task", taskID)
	// Scope the backstop to the run that exited: a legacy record's version 0
	// bypasses the guard, a stamped version only applies while the task is still
	// on that run.
	if err := m.queue.Enqueue(model.RunnerEvent{
		TaskID:  taskID,
		Version: version,
		Event:   model.RunnerEventFailed,
		Reason:  "sandbox exited",
	}); err != nil {
		m.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, taskID, err))
		return
	}
}

// lookup returns the task's TaskRunner, or nil if the task has none.
func (m *TaskManager) lookup(taskID int64) *TaskRunner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[taskID]
}

// register adds tr to the registry. Its record is in taskstate/ already.
func (m *TaskManager) register(tr *TaskRunner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[tr.taskID] = tr
}

// forget deletes the task from the registry, once its TaskRunner has deleted
// its record.
func (m *TaskManager) forget(taskID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, taskID)
}

// version returns the version the task's record is stamped with, or 0 if the
// task has none.
func (m *TaskManager) version(taskID int64) (int64, error) {
	tr := m.lookup(taskID)
	if tr == nil {
		return 0, nil
	}
	return tr.Version()
}

// Running reports whether the task's sandbox is up.
func (m *TaskManager) Running(ctx context.Context, taskID int64) (bool, error) {
	tr := m.lookup(taskID)
	if tr == nil {
		return false, nil
	}
	return tr.Running(ctx)
}

// Remove destroys the task's sandbox and deletes its record and TaskRunner;
// see TaskRunner.Destroy.
func (m *TaskManager) Remove(ctx context.Context, taskID int64) error {
	tr := m.lookup(taskID)
	if tr == nil {
		return nil
	}
	return tr.Destroy(ctx)
}

// Kill asks the driver of the task's sandbox to stop the recorded run, and
// reports whether a running run was stopped; see TaskRunner.Kill.
func (m *TaskManager) Kill(ctx context.Context, task *model.Task) (bool, error) {
	tr := m.lookup(task.ID)
	if tr == nil {
		return false, nil
	}
	return tr.Kill(ctx)
}

// spec assembles the sandbox spec for a task: the task token, the run's
// environment and secrets, and the agent config with the injected gritz MCP
// server.
func (m *TaskManager) spec(ctx context.Context, task *model.Task) (*backend.Spec, error) {
	ws, err := m.workspaces.Get(task.Workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}
	if err := m.backend.ValidateWorkspace(ws); err != nil {
		return nil, fmt.Errorf("invalid workspace %q: %w", task.Workspace, err)
	}

	// Mint the task token via the server rather than signing it locally: the runner
	// supplies only the task id and capability flags and the server derives the
	// task's workspace/runner/org from the row and signs a narrow app JWT. The
	// driver and injected MCP server present it directly to the server.
	tokenResp, err := m.client.CreateTaskToken(ctx, &gritzv1.CreateTaskTokenRequest{
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
		"--server", m.serverURL,
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
		ServerURL: m.serverURL,
		Token:     token,
		// The agent's tools (the git credential helper, the GitHub MCP server)
		// reach the server through these.
		Env: map[string]string{
			"GRITZ_TASK_ID": fmt.Sprint(task.ID),
			"GRITZ_TOKEN":   token,
			"GRITZ_SERVER":  m.serverURL,
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

// Start brings up the task's sandbox and starts the task's run on it, on a Run
// goroutine. A task with no TaskRunner gets a sandbox created, and a
// TaskRunner around it, which records its handle straight away, before
// anything else can fail; it is registered once the record is written. A task
// with one reuses its sandbox, since a task is bound 1:1 to it. Either way the
// record is stamped with the task's version; see TaskRunner.Start for what a
// failure to start the sandbox leaves behind.
//
// Start returns once the platform reports the sandbox running: waiting for the
// driver and starting the run happen in Run, so a slow sandbox does not hold
// up the poll pass. A failure there is reported as the run's "failed".
//
// If a Run still follows the task's previous run, Start returns errRunning
// without starting anything, and the caller gives back the concurrency slot it
// acquired.
func (m *TaskManager) Start(ctx context.Context, task *model.Task) error {
	tr := m.lookup(task.ID)
	if tr != nil {
		if err := tr.idle(); err != nil {
			return err
		}
	}
	spec, err := m.spec(ctx, task)
	if err != nil {
		return err
	}
	if tr == nil {
		if tr, err = m.create(ctx, task, spec); err != nil {
			return err
		}
	}
	if err := tr.Start(ctx, task.Version); err != nil {
		return err
	}
	// Follow the run on the runner's root context: the run is started there,
	// after the record is stamped with its version, so a runner that restarts
	// in between reattaches to it.
	m.run(ctx, tr, m.runSpec(spec))
	return nil
}

// create creates a sandbox for a task with no TaskRunner, and registers the
// TaskRunner it constructs around it. It takes no per-task lock: Poll handles
// each task at most once per pass and passes are sequential, so nothing else
// registers a TaskRunner for the task meanwhile.
func (m *TaskManager) create(ctx context.Context, task *model.Task, spec *backend.Spec) (*TaskRunner, error) {
	sb, err := m.backend.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	tr, err := newTaskRunner(ctx, m.env, sb, task.ID, task.Version)
	if err != nil {
		return nil, err
	}
	m.register(tr)
	return tr, nil
}

// runSpec builds the driver.v1 RunSpec for spec. A backend whose sandboxes
// have no environment of their own adds the workspace's to the run's, under
// spec.Env.
func (m *TaskManager) runSpec(spec *backend.Spec) *driverv1.RunSpec {
	env := map[string]string{}
	if be, ok := m.backend.(backend.RunEnver); ok {
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

// run calls tr's Run on its own goroutine, which holds the run's concurrency
// slot. Once Run returns, the slot is released and Poll woken, unless the
// runner is shutting down: then the slot stays held and the sandbox is left
// for the next boot.
func (m *TaskManager) run(ctx context.Context, tr *TaskRunner, spec *driverv1.RunSpec) {
	go func() {
		if err := tr.Run(ctx, spec); err != nil {
			return
		}
		m.sem.Release(1)
		m.wake.Wake()
	}()
}

// Prune removes sandboxes for archived tasks. It goes through a snapshot of the
// registry. Run calls it between Poll passes, so it never acts on a task Poll
// is handling.
func (m *TaskManager) Prune(ctx context.Context) error {
	m.mu.Lock()
	tasks := maps.Clone(m.tasks)
	m.mu.Unlock()
	for taskID, tr := range tasks {
		m.prune(ctx, taskID, tr)
	}
	return nil
}

// prune removes the task's sandbox if it is not up and the task is archived.
// Both a parked husk and a gone sandbox count as "no live sandbox"; Destroy is
// idempotent, so removing a gone sandbox is a no-op.
func (m *TaskManager) prune(ctx context.Context, taskID int64, tr *TaskRunner) {
	removed, err := tr.destroyIf(ctx, func(ctx context.Context) (bool, error) {
		resp, err := m.client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
		if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			return false, fmt.Errorf("failed to get task: %w", err)
		}
		// Remove sandbox if task is archived or deleted
		return connect.CodeOf(err) == connect.CodeNotFound || resp.Task.Archived, nil
	})
	if err != nil {
		m.log.Error("failed to prune sandbox", "task", taskID, "err", err)
	} else if removed {
		m.log.Info("sandbox removed", "task", taskID)
	}
}
