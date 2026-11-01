package runner

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"regexp"
	"sync"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// TaskManager orchestrates the runner's tasks: it replicates their event log,
// hands each task's reports to its TaskRunner, whose RunLoop acts on them,
// builds the specs their sandboxes are created and run with, and keeps the
// registry of the tasks' TaskRunners.
type TaskManager struct {
	backend    backend.Backend
	client     gritzclient.Client
	serverURL  string
	workspaces *workspace.Config
	runnerID   string
	// slots are the concurrency slots, shared by the TaskRunners.
	slots *Slots
	log   *slog.Logger
	queue *RunnerEventOutbox
	// tasklog is the runner's replica of its tasks' event logs. Run runs it,
	// and feeds its task reports to the TaskRunners.
	tasklog *taskevents.Log
	// store is tasklog's store, where each TaskRunner keeps its position, and
	// holds the tasks' sandbox records.
	store *runnerstore.Store
	// pollInterval caps a TaskRunner's backoff between attempts at a command.
	pollInterval time.Duration
	fatal        context.CancelCauseFunc
	// events is the unbuffered channel the TaskRunners send their events on;
	// HandleEvents drains it.
	events chan TaskEvent
	// buildTaskRunner builds the manager's TaskRunners; see
	// TaskManagerOptions.NewTaskRunner.
	buildTaskRunner func(opts TaskRunnerOptions) *TaskRunner

	// mu guards tasks, the registry of the tasks' TaskRunners: one for each
	// task whose RunLoop runs, from the task's first report (or its record, at
	// Load) until the RunLoop returns for the archived task. A TaskRunner stays
	// registered with no sandbox, before it creates one and after it destroys
	// it or finds it gone. Only Load reads the sandbox records, to rebuild the
	// registry at boot.
	mu    sync.Mutex
	tasks map[int64]*TaskRunner
	// loops counts the RunLoop goroutines loop started that have not
	// returned; see Wait.
	loops sync.WaitGroup
}

const (
	// defaultPollInterval is TaskManagerOptions.PollInterval's default.
	defaultPollInterval = 30 * time.Second
)

type TaskManagerOptions struct {
	Client gritzclient.Client
	// Backend is the sandbox runtime that hosts task drivers.
	Backend backend.Backend
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
	Queue *RunnerEventOutbox
	// TaskLog is the runner's replica of its tasks' event logs, which Run
	// runs. Required.
	TaskLog *taskevents.Log
	// RunnerStore is TaskLog's store, and the runner-local source of truth
	// for the task→sandbox-handle mapping: the runner is the only writer, and
	// backends never touch it. Required.
	RunnerStore *runnerstore.Store
	// PollInterval is the most a TaskRunner waits before it retries a
	// command. Defaults to 30s.
	PollInterval time.Duration
	// Fatal terminates the runner by cancelling its root context with the given
	// cause. It is invoked by die when a durable local-store operation fails;
	// the command layer wraps ctx with context.WithCancelCause and passes the
	// CancelCauseFunc here. When nil it defaults to a no-op, so die never crashes
	// (durable-write failures fall back to the pre-existing behaviour).
	Fatal context.CancelCauseFunc
	// NewTaskRunner builds the TaskRunner of each task the manager registers.
	// Its options carry the task's ID and the manager's Backend, RunnerStore,
	// events channel and Log; it may fill in the rest. Defaults to
	// NewTaskRunner.
	NewTaskRunner func(opts TaskRunnerOptions) *TaskRunner
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
	if opts.TaskLog == nil || opts.RunnerStore == nil {
		return nil, fmt.Errorf("task log and runner store are required")
	}

	// Default Fatal to a no-op so die can call m.fatal unconditionally. A nil
	// Fatal means the caller opted out of crash-on-write-failure (only tests that
	// never trigger die); production always wires the CancelCauseFunc.
	fatal := opts.Fatal
	if fatal == nil {
		fatal = func(error) {}
	}

	buildTaskRunner := opts.NewTaskRunner
	if buildTaskRunner == nil {
		buildTaskRunner = NewTaskRunner
	}

	m := &TaskManager{
		backend:         opts.Backend,
		client:          opts.Client,
		serverURL:       opts.ServerURL,
		workspaces:      opts.Workspaces,
		runnerID:        opts.RunnerID,
		slots:           NewSlots(opts.Concurrency),
		log:             cmp.Or(opts.Log, slog.Default()),
		queue:           opts.Queue,
		tasklog:         opts.TaskLog,
		store:           opts.RunnerStore,
		pollInterval:    cmp.Or(opts.PollInterval, defaultPollInterval),
		fatal:           fatal,
		events:          make(chan TaskEvent),
		buildTaskRunner: buildTaskRunner,
		tasks:           map[int64]*TaskRunner{},
	}
	return m, nil
}

// FatalError wraps a durable local-store failure that terminates the
// runner. It is the cause set on the runner's root context by die; the command
// layer matches it with errors.As to distinguish a broken-disk crash (non-zero
// exit → supervisor restarts + alerts) from a graceful signal shutdown (exit 0),
// without depending on whatever cause signal.NotifyContext sets.
type FatalError struct{ err error }

func (e FatalError) Error() string { return "durable store write failed: " + e.err.Error() }
func (e FatalError) Unwrap() error { return e.err }

// die terminates the runner by cancelling its root context with a
// FatalError cause. It is the response to a durable local-store failure
// (outbox Enqueue, or a TaskRunner's RunLoop returning one): those failures are
// non-transient (ENOSPC/EIO/EROFS) and silently dropping a lifecycle event
// defeats the whole point of the durable store, so the runner fails loud and
// lets its supervisor restart and alert. Riding ctx cancellation instead of os.Exit preserves the
// detach-don't-kill teardown — in-flight sandboxes stay alive and are re-adopted
// by Load on the next boot. First cause wins (WithCancelCause is idempotent), so
// the first failure is the one reported.
func (m *TaskManager) die(err error) { m.fatal(FatalError{err}) }

// HandleEvents handles the TaskRunners' events until ctx is done. It must be
// running before Load is called, and run on the same context as Load, Run and
// the Run goroutines they start, so that no TaskRunner blocks sending to it
// after it has stopped.
//
// A TaskLostEvent is reported to the server as the run's "failed". Handling an
// event never takes a TaskRunner's lock, so a TaskRunner may send while
// holding it.
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

// Wake makes the event log check the server for new events now. Wake-ups
// coalesce: a burst of them before the next check results in a single one.
func (m *TaskManager) Wake() { m.tasklog.Wake() }

// Run runs the event log until ctx is done, and hands each task it reports to
// the task's TaskRunner, starting one if the task has none; see apply. Once
// ctx is done, it waits for the log and the RunLoops to stop, then returns
// context.Cause(ctx). If the log fails, Run terminates the runner through die.
//
// HandleEvents must be running, and Load must have returned, before Run is
// called, all on the same ctx.
func (m *TaskManager) Run(ctx context.Context) error {
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		if err := m.tasklog.Run(ctx); err != nil && ctx.Err() == nil {
			m.die(fmt.Errorf("event log: %w", err))
		}
	}()
	for {
		select {
		case c := <-m.tasklog.Tasks():
			m.apply(ctx, c)
		case <-ctx.Done():
			<-logDone
			m.loops.Wait()
			return context.Cause(ctx)
		}
	}
}

// apply hands one of the event log's task reports to the task's TaskRunner,
// first starting one if the task has none. Reports may repeat a task's state,
// and applying one twice changes nothing.
func (m *TaskManager) apply(ctx context.Context, c runnerstore.TaskChange) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tr, ok := m.tasks[c.ID]
	if !ok {
		tr = m.newTaskRunner(c.ID)
		m.loop(ctx, tr)
	}
	tr.Change(c)
}

// loop registers tr and runs its RunLoop on its own goroutine, until it
// returns for the archived task, when remove drops tr, or ctx is done. A
// RunLoop that fails terminates the runner through die. The caller holds m.mu.
func (m *TaskManager) loop(ctx context.Context, tr *TaskRunner) {
	taskID := tr.opts.TaskID
	m.tasks[taskID] = tr
	m.loops.Go(func() {
		for {
			if err := tr.RunLoop(ctx); err != nil {
				if ctx.Err() == nil {
					m.die(fmt.Errorf("task %d: %w", taskID, err))
				}
				return
			}
			if m.remove(taskID, tr) {
				return
			}
		}
	})
}

// remove drops tr, whose RunLoop returned for the archived task, from the
// registry, and reports true. If the task was reported unarchived since, it
// keeps tr and reports false, and the caller runs tr's RunLoop again: apply
// handed that report to tr, under the same lock, so a later report finds tr
// gone and starts a new TaskRunner.
func (m *TaskManager) remove(taskID int64, tr *TaskRunner) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, _ := tr.latest(); !c.Archived {
		return false
	}
	delete(m.tasks, taskID)
	m.log.Info("task removed", "task", taskID)
	return true
}

// Wait blocks until every RunLoop goroutine has returned, or ctx is done,
// when it returns ctx's cause. A RunLoop returns once its task is archived or
// the context Load or Run started it on is done. Wait must not be called
// while Load or Run can still start one.
func (m *TaskManager) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		m.loops.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (m *TaskManager) Close() error {
	return m.backend.Close()
}

// RegisterWorkspaces sends the available workspace names to the server.
func (m *TaskManager) RegisterWorkspaces(ctx context.Context) error {
	var workspaces []*gritzv1.RegisteredWorkspace
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

// Load rehydrates this runner's sandboxes at boot, before Run admits new work.
// The sandbox records enumerate; Status answers liveness per record. An up
// sandbox gets the same Run goroutine as the live path, which reattaches
// to the recorded run; a suspended husk or a gone sandbox is a lost-report
// backstop, and a gone sandbox's dangling record is dropped (nothing to reuse
// or destroy). The slots are seeded with the up count last, so they may exceed
// capacity in over-limit scenarios — that's fine.
//
// Load rebuilds the task registry from the records it keeps: it is the only
// reader of the sandbox records. Each record is adopted by a new TaskRunner
// for its task through TaskRunner.Restore, which claims the TaskRunner of an
// up sandbox for its Run, and the TaskRunner's RunLoop is started as Run
// starts the ones it creates. A record whose handle cannot be opened is
// dropped like a gone sandbox's, leaving its TaskRunner with no sandbox.
//
// HandleEvents must already be running: the Run goroutines Load starts are
// the first to send it events.
func (m *TaskManager) Load(ctx context.Context) error {
	recs, err := m.store.Sandboxes(ctx)
	if err != nil {
		return fmt.Errorf("failed to list records: %w", err)
	}
	var running int64
	for _, rec := range recs {
		tr := m.newTaskRunner(rec.TaskID)
		if st, err := tr.Restore(ctx, rec); err != nil {
			m.log.Error("load: restore", "task", rec.TaskID, "err", err)
		} else {
			switch st {
			case backend.StatusUp: // container running / VM RUNNING: re-attach
				tr.start(ctx, nil)
				running++
			case backend.StatusSuspended: // stopped container / SUSPENDED VM (husk preserved)
				m.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
			case backend.StatusGone: // removed / TERMINATED: the bound sandbox vanished
				m.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
			}
		}
		m.mu.Lock()
		m.loop(ctx, tr)
		m.mu.Unlock()
	}
	m.slots.Set(running)
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
	// Scope the backstop to the run that exited: it only applies while the task
	// is still on that run.
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

// newTaskRunner builds a TaskRunner with no sandbox for the task, with the
// manager's options.
func (m *TaskManager) newTaskRunner(taskID int64) *TaskRunner {
	return m.buildTaskRunner(TaskRunnerOptions{
		TaskID:       taskID,
		Backend:      m.backend,
		Store:        m.store,
		Events:       m.events,
		Slots:        m.slots,
		Queue:        m.queue,
		Spec:         m.specs,
		PollInterval: m.pollInterval,
		Log:          m.log,
	})
}

// spec assembles the sandbox spec for a task: the task token, the run's
// environment and secrets, and the agent config with the injected gritz MCP
// server.
func (m *TaskManager) spec(ctx context.Context, c runnerstore.TaskChange) (*backend.Spec, error) {
	ws, err := m.workspaces.Get(c.Workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}
	if err := m.backend.ValidateWorkspace(ws); err != nil {
		return nil, fmt.Errorf("invalid workspace %q: %w", c.Workspace, err)
	}

	// Mint the task token via the server rather than signing it locally: the runner
	// supplies only the task id and capability flags and the server derives the
	// task's workspace/runner/org from the row and signs a narrow app JWT. The
	// driver and injected MCP server present it directly to the server.
	tokenResp, err := m.client.CreateTaskToken(ctx, &gritzv1.CreateTaskTokenRequest{
		TaskId:       c.ID,
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
		"--task", fmt.Sprint(c.ID),
		"--runner", m.runnerID,
		"--workspace", c.Workspace,
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
		TaskID:    c.ID,
		Version:   c.Version,
		Workspace: ws,
		ServerURL: m.serverURL,
		Token:     token,
		// The agent's tools (the git credential helper, the GitHub MCP server)
		// reach the server through these.
		Env: map[string]string{
			"GRITZ_TASK_ID": fmt.Sprint(c.ID),
			"GRITZ_TOKEN":   token,
			"GRITZ_SERVER":  m.serverURL,
		},
		Secrets: maps.Clone(ws.Secrets),
		Files: []backend.File{
			// Allow non-root agents to write to this directory.
			{Path: path.Dir(agent.DefaultConfigStore.Path(c.ID)), Mode: 0777, Dir: true},
			{Path: agent.DefaultConfigStore.Path(c.ID), Data: cfgData, Mode: 0666},
			// Pre-create the log dir 0777 so a non-root driver can write the
			// append-only /gritz/log file (the driver's MkdirAll is only a
			// fallback for a directly-invoked driver).
			{Path: path.Dir(agent.DefaultLogPath), Mode: 0777, Dir: true},
		},
	}, nil
}

// specs builds the sandbox spec of the task's run c, and the driver.v1
// RunSpec its run is started with; see TaskRunnerOptions.Spec.
func (m *TaskManager) specs(ctx context.Context, c runnerstore.TaskChange) (*backend.Spec, *driverv1.RunSpec, error) {
	spec, err := m.spec(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	return spec, m.runSpec(spec), nil
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
