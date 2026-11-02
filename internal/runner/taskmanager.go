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
// hands each task's reports to its TaskRunner, whose RunLoop manages the
// task's sandbox, and owns what the TaskRunners share: the concurrency slots,
// the runner-event outbox and the specs their sandboxes are created and run
// with. It has no command logic: the drivers act on the commands.
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
	// store is tasklog's store, which also holds the tasks' sandbox records.
	store *runnerstore.Store
	fatal context.CancelCauseFunc
	// newTaskRunner builds the manager's TaskRunners; see
	// TaskManagerOptions.NewTaskRunner.
	newTaskRunner func(opts TaskRunnerOptions) *TaskRunner

	// tasks is the registry of the tasks' TaskRunners, one per task whose
	// RunLoop is running: from the task's first unarchived report (or its
	// record, at Load) until RunLoop returns. Load fills it before Run
	// starts, and from then on only Run's goroutine touches it.
	tasks map[int64]*TaskRunner
	// done receives each TaskRunner whose RunLoop returned nil; Run drops
	// it from tasks.
	done chan *TaskRunner
	// loops counts the RunLoop goroutines; Run waits for them on shutdown.
	loops sync.WaitGroup
}

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
	// Fatal terminates the runner by cancelling its root context with the given
	// cause. It is invoked by die when a durable local-store write fails; the
	// command layer wraps ctx with context.WithCancelCause and passes the
	// CancelCauseFunc here. When nil it defaults to a no-op, so die never crashes
	// (durable-write failures fall back to the pre-existing behaviour).
	Fatal context.CancelCauseFunc
	// NewTaskRunner builds the TaskRunner of each task the manager registers.
	// Its options carry the task's ID and the manager's Backend, RunnerStore,
	// Slots, Queue, the task's Stream, the spec builder and Log; it may fill
	// in the rest. Defaults to NewTaskRunner.
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

	newTaskRunner := opts.NewTaskRunner
	if newTaskRunner == nil {
		newTaskRunner = NewTaskRunner
	}

	m := &TaskManager{
		backend:       opts.Backend,
		client:        opts.Client,
		serverURL:     opts.ServerURL,
		workspaces:    opts.Workspaces,
		runnerID:      opts.RunnerID,
		slots:         NewSlots(opts.Concurrency),
		log:           cmp.Or(opts.Log, slog.Default()),
		queue:         opts.Queue,
		tasklog:       opts.TaskLog,
		store:         opts.RunnerStore,
		fatal:         fatal,
		newTaskRunner: newTaskRunner,
		tasks:         map[int64]*TaskRunner{},
		done:          make(chan *TaskRunner),
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
// failure (outbox Enqueue or runnerstore WriteSandbox), which a RunLoop
// returns, or a failure of the event log's store: those failures are
// non-transient (ENOSPC/EIO/EROFS) and silently dropping a lifecycle event
// defeats the whole point of the durable store, so the runner fails loud and
// lets its supervisor restart and alert. Riding ctx cancellation instead of os.Exit preserves the
// detach-don't-kill teardown — in-flight sandboxes stay alive and are re-adopted
// by Load on the next boot. First cause wins (WithCancelCause is idempotent), so
// the first failure is the one reported.
func (m *TaskManager) die(err error) { m.fatal(FatalError{err}) }

// Wake makes the event log check the server for new events now. Wake-ups
// coalesce: a burst of them before the next check results in a single one.
func (m *TaskManager) Wake() { m.tasklog.Wake() }

// Run runs the event log and hands each task's reports to its TaskRunner
// until ctx is done, then returns context.Cause(ctx) once the log and every
// RunLoop have stopped. An unarchived task with no TaskRunner gets one, whose
// RunLoop starts; a TaskRunner whose RunLoop returned nil, its task archived
// and its sandbox destroyed, is dropped. If the log or a RunLoop fails, Run
// terminates the runner through die.
//
// Load must have returned before Run is called, on the same ctx.
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
			if tr := m.tasks[c.ID]; tr != nil {
				tr.Change(c)
			} else if !c.Archived {
				m.start(ctx, m.newRunner(c.ID), c)
			}
		case tr := <-m.done:
			m.remove(ctx, tr)
		case <-ctx.Done():
			<-logDone
			m.loops.Wait()
			return context.Cause(ctx)
		}
	}
}

// start registers tr, hands it c, the task's first report, and runs its
// RunLoop on its own goroutine. When RunLoop returns nil, tr is sent to Run
// to be removed; an error terminates the runner, unless ctx is done.
func (m *TaskManager) start(ctx context.Context, tr *TaskRunner, c runnerstore.TaskChange) {
	m.tasks[c.ID] = tr
	tr.Change(c)
	m.loops.Go(func() {
		if err := tr.RunLoop(ctx); err != nil {
			if ctx.Err() == nil {
				m.die(fmt.Errorf("task %d: %w", c.ID, err))
			}
			return
		}
		select {
		case m.done <- tr:
		case <-ctx.Done():
		}
	})
}

// remove drops tr, whose RunLoop returned once its task was archived. A
// report that reached tr after RunLoop decided to return is not lost: if the
// task's latest report is no longer archived, a fresh TaskRunner starts from
// it.
func (m *TaskManager) remove(ctx context.Context, tr *TaskRunner) {
	taskID := tr.opts.TaskID
	delete(m.tasks, taskID)
	m.log.Info("task runner removed", "task", taskID)
	if c, _ := tr.latest(); !c.Archived {
		m.start(ctx, m.newRunner(taskID), c)
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
// Each sandbox record is adopted by a TaskRunner for its task through
// TaskRunner.Restore, which also answers the sandbox's liveness, and handed
// the task's stored state (archived included) before its RunLoop starts: an
// up sandbox is driven, reattaching to whatever its driver is doing, and the
// sandbox of a task archived before the restart is destroyed. A suspended
// husk or a gone sandbox is a lost-report backstop, and a gone sandbox's
// dangling record is dropped. The slots are seeded with the up count, so
// they may exceed capacity in over-limit scenarios — that's fine.
//
// A record whose handle cannot be opened is dropped like a gone sandbox's,
// leaving its TaskRunner with no sandbox.
func (m *TaskManager) Load(ctx context.Context) error {
	recs, err := m.store.Sandboxes(ctx)
	if err != nil {
		return fmt.Errorf("failed to list records: %w", err)
	}
	var running int64
	type restored struct {
		tr *TaskRunner
		c  runnerstore.TaskChange
	}
	var runners []restored
	for _, rec := range recs {
		c, err := m.store.Task(ctx, rec.TaskID)
		if err != nil {
			return fmt.Errorf("failed to read task %d: %w", rec.TaskID, err)
		}
		tr := m.newRunner(rec.TaskID)
		st, err := tr.Restore(ctx, rec)
		if err != nil {
			m.log.Error("load: restore", "task", rec.TaskID, "err", err)
		}
		switch st {
		case backend.StatusUp: // container running / VM RUNNING: re-attach
			running++
		case backend.StatusSuspended: // stopped container / SUSPENDED VM (husk preserved)
			m.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
		case backend.StatusGone: // removed / TERMINATED: the bound sandbox vanished
			m.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
		}
		runners = append(runners, restored{tr, c})
	}
	// Seed the slots before any RunLoop can take or give one back.
	m.slots.Set(running)
	m.log.Info("initialized running sandbox count", "count", running)
	for _, r := range runners {
		m.start(ctx, r.tr, r.c)
	}
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

// newRunner builds a TaskRunner for the task, with no sandbox.
func (m *TaskManager) newRunner(taskID int64) *TaskRunner {
	return m.newTaskRunner(TaskRunnerOptions{
		TaskID:  taskID,
		Backend: m.backend,
		Store:   m.store,
		Slots:   m.slots,
		Queue:   m.queue,
		Stream:  m.tasklog.Stream(taskID),
		Spec:    m.specs,
		Log:     m.log,
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
