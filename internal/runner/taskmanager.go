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
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/outbox"
	"github.com/gritzapp/gritz/internal/x/safesem"
	"github.com/gritzapp/gritz/internal/x/wakeup"
)

// TaskManager orchestrates the runner's tasks: it replicates their event log,
// runs a follower per unarchived task that acts on the task's commands, builds
// the specs their sandboxes are created and run with, admits runs under the
// concurrency limit, and keeps the registry of the tasks' TaskRunners.
type TaskManager struct {
	backend    backend.Backend
	client     gritzclient.Client
	serverURL  string
	workspaces *workspace.Config
	runnerID   string
	sem        *safesem.Semaphore
	log        *slog.Logger
	queue      *outbox.Outbox[model.RunnerEvent]
	// tasklog is the runner's replica of its tasks' event logs. Run runs it,
	// and the followers read their tasks' streams from it.
	tasklog *taskevents.Log
	// store is tasklog's store, where each follower keeps its position, and
	// holds the tasks' sandbox records.
	store *runnerstore.Store
	// released wakes the followers waiting to retry a command when a run
	// releases its concurrency slot.
	released wakeup.Broadcast
	// pollInterval is Run's interval between Prune passes, and caps a
	// follower's backoff between attempts at a command.
	pollInterval time.Duration
	fatal        context.CancelCauseFunc
	// events is the unbuffered channel the TaskRunners send their events on;
	// HandleEvents drains it.
	events chan TaskEvent
	// newTaskRunner builds the manager's TaskRunners; see
	// TaskManagerOptions.NewTaskRunner.
	newTaskRunner func(opts TaskRunnerOptions) *TaskRunner
	// runs counts the Run goroutines run started that have not returned; see
	// Wait.
	runs sync.WaitGroup

	// mu guards tasks, the registry of the tasks' TaskRunners: one for each
	// task the runner has seen, from its first command (or its record, at
	// Load) until retire drops it once the task is archived or deleted. A
	// TaskRunner stays registered with no sandbox, before it creates one and
	// after it destroys it or finds it gone. Only Load reads the sandbox
	// records, to rebuild the registry at boot.
	mu    sync.Mutex
	tasks map[int64]*TaskRunner

	// followers holds the follower of each unarchived task Run has been told
	// of. Only Run's goroutine, and Prune, which it calls, use it.
	followers map[int64]*follower
}

// follower is a task's follower goroutine; see TaskManager.follow.
type follower struct {
	cancel context.CancelFunc
	// done is closed once the goroutine has returned.
	done chan struct{}
}

const (
	// defaultPollInterval is Run's prune interval when TaskManagerOptions
	// leaves it unset.
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
	Queue *outbox.Outbox[model.RunnerEvent]
	// TaskLog is the runner's replica of its tasks' event logs, which Run
	// runs. Required.
	TaskLog *taskevents.Log
	// RunnerStore is TaskLog's store, and the runner-local source of truth
	// for the task→sandbox-handle mapping: the runner is the only writer, and
	// backends never touch it. Required.
	RunnerStore *runnerstore.Store
	// PollInterval is how often Run prunes, and the most a follower waits
	// before it retries a command. Defaults to 30s.
	PollInterval time.Duration
	// Fatal terminates the runner by cancelling its root context with the given
	// cause. It is invoked by die when a durable local-store write fails; the
	// command layer wraps ctx with context.WithCancelCause and passes the
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
		sem:           safesem.New(concurrency),
		log:           log,
		queue:         opts.Queue,
		tasklog:       opts.TaskLog,
		store:         opts.RunnerStore,
		pollInterval:  cmp.Or(opts.PollInterval, defaultPollInterval),
		fatal:         fatal,
		events:        make(chan TaskEvent),
		newTaskRunner: newTaskRunner,
		tasks:         map[int64]*TaskRunner{},
		followers:     map[int64]*follower{},
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
// failure (outbox Enqueue or runnerstore WriteSandbox): those failures are
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

// Wake makes the event log check the server for new events now. Wake-ups
// coalesce: a burst of them before the next check results in a single one.
func (m *TaskManager) Wake() { m.tasklog.Wake() }

// Run runs the event log and the tasks' followers until ctx is done, then
// returns context.Cause(ctx) once they have stopped. Each task the log
// reports gets a follower while it is unarchived; an archived task's follower
// is stopped and its TaskRunner retired. Every PollInterval Run also runs
// Prune. If the log fails, Run terminates the runner through die.
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
	prune := time.NewTicker(m.pollInterval)
	defer prune.Stop()
	for {
		select {
		case c := <-m.tasklog.Tasks():
			m.apply(ctx, c)
		case <-prune.C:
			if err := m.Prune(ctx); err != nil {
				m.log.Error("failed to prune sandboxes", "err", err)
			}
		case <-ctx.Done():
			for taskID := range m.followers {
				m.unfollow(taskID)
			}
			<-logDone
			return context.Cause(ctx)
		}
	}
}

// apply applies one of the event log's task reports: an unarchived task gets
// a follower if it has none, and an archived one is retired. Reports may
// repeat a task's state, and applying one twice changes nothing.
func (m *TaskManager) apply(ctx context.Context, c runnerstore.TaskChange) {
	if c.Archived {
		m.retire(ctx, c.ID)
		return
	}
	if _, ok := m.followers[c.ID]; ok {
		return
	}
	fctx, cancel := context.WithCancel(ctx)
	f := &follower{cancel: cancel, done: make(chan struct{})}
	m.followers[c.ID] = f
	go func() {
		defer close(f.done)
		m.follow(ctx, fctx, c.ID)
	}()
}

// unfollow stops the task's follower, if it has one, and waits for it to
// return: a command it is handling is carried out first.
func (m *TaskManager) unfollow(taskID int64) {
	f, ok := m.followers[taskID]
	if !ok {
		return
	}
	f.cancel()
	<-f.done
	delete(m.followers, taskID)
}

// follow is the task's follower. It reads the task's event stream from the
// stored position, and acts on the last command event in what it read:
// commands are a desired state, so the latest supersedes the ones before it,
// and a runner reading a stream from the start does not replay its history.
// Once the command is carried out, or what it read has no command, it stores
// the last event it read as handled and reads on.
//
// A command that isn't carried out, because its handler failed or there was
// no concurrency slot for it, leaves the position where it was. The follower
// tries again once a run releases its slot, the task has a new event, or a
// backoff ends, rereading the stream each time, so a newer command takes the
// place of the one that waited.
//
// Commands are carried out on ctx, the runner's root context, as the Run
// goroutines they start must outlive the follower; fctx ends the follower.
// A store error terminates the runner through die.
func (m *TaskManager) follow(ctx, fctx context.Context, taskID int64) {
	handled, err := m.store.Handled(ctx, taskID)
	if err != nil {
		if ctx.Err() == nil {
			m.die(err)
		}
		return
	}
	stream := m.tasklog.Stream(taskID)
	retry := doubling(min(time.Second, m.pollInterval), m.pollInterval)
	for {
		// Take the signal before handling, so a slot released while the
		// handler runs still wakes the retry.
		released := m.released.Wait()
		events, err := stream.Read(fctx, handled)
		if fctx.Err() != nil {
			return
		}
		if err != nil {
			m.die(err)
			return
		}
		last := events[len(events)-1].GetId()
		if c, ok := lastCommand(taskID, events); ok {
			if err := m.command(ctx, c); err != nil {
				if errors.Is(err, errNoSlot) || errors.Is(err, ErrRunning) {
					m.log.Debug("command waits", "task", taskID, "command", c.Command, "version", c.Version, "reason", err)
				} else {
					m.log.Error("failed to handle command", "task", taskID, "command", c.Command, "version", c.Version, "err", err)
				}
				if !m.await(fctx, stream, last, released, retry.NextBackOff()) {
					return
				}
				continue
			}
		}
		if err := m.store.SetHandled(ctx, taskID, last); err != nil {
			if ctx.Err() == nil {
				m.die(err)
			}
			return
		}
		handled = last
		retry.Reset()
	}
}

// await waits for a reason to try a command again: released firing, an event
// for the task after last, or the backoff d running out. It reports false if
// ctx ended first.
func (m *TaskManager) await(ctx context.Context, stream *taskevents.Stream, last int64, released <-chan struct{}, d time.Duration) bool {
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	next := make(chan struct{})
	go func() {
		defer close(next)
		// The error is readCtx's, or a store error the follower's next Read
		// returns as well.
		_, _ = stream.Read(readCtx, last)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-released:
	case <-next:
	case <-timer.C:
	case <-ctx.Done():
	}
	return ctx.Err() == nil
}

// lastCommand returns the command of the last command event in events, and
// false if there is none.
func lastCommand(taskID int64, events []*gritzv1.Event) (runnerstore.TaskChange, bool) {
	for _, e := range slices.Backward(events) {
		if cmd := e.GetCommand(); cmd != nil {
			return runnerstore.TaskChange{
				ID:        taskID,
				Command:   cmd.GetCommand(),
				Version:   cmd.GetVersion(),
				Workspace: cmd.GetWorkspace(),
			}, true
		}
	}
	return runnerstore.TaskChange{}, false
}

// command carries out the task's command c. A non-nil error means it wasn't
// carried out, and the follower tries again. No command needs nothing done.
func (m *TaskManager) command(ctx context.Context, c runnerstore.TaskChange) error {
	switch c.Command {
	case gritzv1.TaskCommand_STOP:
		return m.stopTask(ctx, c)
	case gritzv1.TaskCommand_RESTART:
		return m.restartTask(ctx, c)
	case gritzv1.TaskCommand_START:
		return m.startTask(ctx, c)
	default:
		return nil
	}
}

// Wait blocks until every Run goroutine the manager started has returned, or
// ctx is done, when it returns ctx's cause. It must not be called while the
// manager can still start runs (Load, Start, or a follower with a command to
// handle): each of them can start a Run goroutine, which must not join the
// wait once it has begun.
func (m *TaskManager) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		m.runs.Wait()
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

// errNoSlot is what restartTask and startTask return when every concurrency
// slot is taken: the run waits for one, and its command is tried again.
var errNoSlot = errors.New("concurrency limit reached")

// stopTask, restartTask and startTask each handle one command for a task. A
// non-nil error means the command wasn't carried out and should be retried;
// the caller decides when.
func (m *TaskManager) stopTask(ctx context.Context, c runnerstore.TaskChange) error {
	signalled, err := m.Runner(c.ID).Kill(ctx)
	if err != nil {
		// The stop is retried.
		return fmt.Errorf("failed to stop task: %w", err)
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
		TaskID:  c.ID,
		Event:   model.RunnerEventStopped,
		Version: c.Version,
	}); err != nil {
		m.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventStopped, c.ID, err))
		return nil
	}
	return nil
}

func (m *TaskManager) restartTask(ctx context.Context, c runnerstore.TaskChange) error {
	// Idempotency guard: the record is stamped with the version of
	// the run it launched, so a record already on the task's current
	// version means this restart's new run was already launched, by
	// this runner before it restarted with the command unhandled. The
	// driver takes ~12s to boot, so killing here would SIGKILL the run
	// we just started (issue #1520). Do nothing: "started" consumes
	// the command.
	if m.Runner(c.ID).Version() == c.Version {
		m.log.Debug("restart already processed, waiting for started", "task", c.ID, "version", c.Version)
		return nil
	}
	// Kill existing sandbox if running. The old run's "stopped"
	// is rejected by the status guard while the restart command is
	// pending, so the command survives until the new run's
	// "started" consumes it.
	if _, err := m.Runner(c.ID).Kill(ctx); err != nil {
		m.log.Error("failed to kill task for restart", "task", c.ID, "err", err)
	}
	// Atomically acquire a semaphore slot before starting
	if !m.sem.TryAcquire(1) {
		return errNoSlot
	}
	err := m.Start(ctx, c)
	if errors.Is(err, ErrRunning) {
		// The killed run is still followed. Nothing was started, so
		// the slot goes back, and the restart is retried once that
		// run releases its own.
		m.sem.Release(1)
		return err
	}
	if err != nil {
		m.sem.Release(1) // Release the slot on failure
		m.log.Error("failed to start task", "task", c.ID, "err", err)
		err := m.queue.Enqueue(model.RunnerEvent{
			TaskID:  c.ID,
			Event:   model.RunnerEventFailed,
			Version: c.Version,
			Reason:  err.Error(),
		})
		if err != nil {
			m.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, c.ID, err))
		}
		return nil
	}
	// The "started" event is submitted by the driver on startup
	return nil
}

func (m *TaskManager) startTask(ctx context.Context, c runnerstore.TaskChange) error {
	// Check if the sandbox is already running
	running, err := m.Runner(c.ID).Running(ctx)
	if err != nil {
		return fmt.Errorf("failed to check if task is running: %w", err)
	}
	if running {
		if m.Runner(c.ID).Version() == c.Version {
			// The sandbox runs this version: do nothing, and let the
			// run finish. If the start is still pending then, the run
			// boundary moves it to the next version, in a new command
			// event.
			m.log.Debug("start command: sandbox already running, waiting for it to finish", "task", c.ID)
			return nil
		}
		// The sandbox still runs an earlier version, such as the run
		// whose end moved this start to the next version, before the
		// runner has parked it. The start is retried once that run
		// releases its slot.
		return ErrRunning
	}

	// Sandbox not running - atomically acquire a semaphore slot before starting
	if !m.sem.TryAcquire(1) {
		return errNoSlot
	}
	err = m.Start(ctx, c)
	if errors.Is(err, ErrRunning) {
		// The sandbox is parked, but a Run still follows its last
		// run. Nothing was started, so the slot goes back, and the
		// start is retried once that Run releases its own.
		m.sem.Release(1)
		return err
	}
	if err != nil {
		m.sem.Release(1) // Release the slot on failure
		m.log.Error("failed to start task", "task", c.ID, "err", err)
		err := m.queue.Enqueue(model.RunnerEvent{
			TaskID:  c.ID,
			Event:   model.RunnerEventFailed,
			Version: c.Version,
			Reason:  err.Error(),
		})
		if err != nil {
			m.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, c.ID, err))
		}
		return nil
	}
	// The "started" event is submitted by the driver on startup
	return nil
}

// Load rehydrates this runner's sandboxes at boot, before Run admits new work.
// The sandbox records enumerate; Status answers liveness per record. An up
// sandbox gets the same Run goroutine as the live path, which reattaches
// to the recorded run; a parked husk or a gone sandbox is a lost-report
// backstop, and a gone sandbox's dangling record is dropped (nothing to reuse
// or destroy). The semaphore is seeded with the up count last, so it may exceed
// capacity in over-limit scenarios — that's fine.
//
// Load rebuilds the task registry from the records it keeps: it is the only
// reader of the sandbox records. Each record is adopted by its task's
// TaskRunner through TaskRunner.Restore, which claims the TaskRunner of an up sandbox for its Run.
// A record whose handle cannot be opened is dropped like a gone sandbox's,
// leaving its TaskRunner with no sandbox.
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
		tr := m.Runner(rec.TaskID)
		st, err := tr.Restore(ctx, rec)
		if err != nil {
			m.log.Error("load: restore", "task", rec.TaskID, "err", err)
			continue
		}
		switch st {
		case backend.StatusUp: // container running / VM RUNNING: re-attach
			m.run(ctx, tr, nil)
			running++
		case backend.StatusParked: // stopped container / SUSPENDED VM (husk preserved)
			m.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
		case backend.StatusGone: // removed / TERMINATED: the bound sandbox vanished
			m.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
		}
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

// Runner returns the task's TaskRunner, registering one with no sandbox if the
// task has none. The TaskRunner stays registered until retire drops it; nothing
// else does.
func (m *TaskManager) Runner(taskID int64) *TaskRunner {
	m.mu.Lock()
	defer m.mu.Unlock()
	tr := m.tasks[taskID]
	if tr == nil {
		tr = m.newTaskRunner(TaskRunnerOptions{
			TaskID:  taskID,
			Backend: m.backend,
			Store:   m.store,
			Events:  m.events,
			Log:     m.log,
		})
		m.tasks[taskID] = tr
	}
	return tr
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

// Start brings up the task's sandbox and starts the task's run on it, on a Run
// goroutine. The task's TaskRunner creates the sandbox if it has none, or
// reuses it, and stamps the record with the command's version; see
// TaskRunner.Boot for what a failure leaves behind.
//
// Start returns once the platform reports the sandbox running: waiting for the
// driver and starting the run happen in Run, so a slow sandbox does not hold
// up the task's follower. A failure there is reported as the run's "failed".
//
// If a Run still follows the task's previous run, Start returns ErrRunning
// without starting anything, and the caller gives back the concurrency slot it
// acquired. The task token minted for the spec then goes unused.
func (m *TaskManager) Start(ctx context.Context, c runnerstore.TaskChange) error {
	tr := m.Runner(c.ID)
	spec, err := m.spec(ctx, c)
	if err != nil {
		return err
	}
	if err := tr.Boot(ctx, *spec); err != nil {
		return err
	}
	// Follow the run on the runner's root context: the run is started there,
	// after the record is stamped with its version, so a runner that restarts
	// in between reattaches to it.
	m.run(ctx, tr, m.runSpec(spec))
	return nil
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
// slot. Once Run returns, the slot is released and the followers waiting for
// one woken, unless the runner is shutting down: then the slot stays held and
// the sandbox is left for the next boot.
func (m *TaskManager) run(ctx context.Context, tr *TaskRunner, spec *driverv1.RunSpec) {
	m.runs.Go(func() {
		if err := tr.Run(ctx, spec); err != nil {
			return
		}
		m.sem.Release(1)
		m.released.Wake()
	})
}

// Prune retires the archived and deleted tasks among those with a TaskRunner
// or a follower: it checks each with the server, as a task deleted from the
// server leaves no event to report it. Run calls it, on its own goroutine, so
// it is never called concurrently with Run.
func (m *TaskManager) Prune(ctx context.Context) error {
	m.mu.Lock()
	ids := slices.Collect(maps.Keys(m.tasks))
	m.mu.Unlock()
	for taskID := range m.followers {
		if !slices.Contains(ids, taskID) {
			ids = append(ids, taskID)
		}
	}
	for _, taskID := range ids {
		stale, err := m.stale(ctx, taskID)
		if err != nil {
			m.log.Error("failed to prune sandbox", "task", taskID, "err", err)
			continue
		}
		if stale {
			m.retire(ctx, taskID)
		}
	}
	return nil
}

// retire stops the task's follower, then retires the task's TaskRunner and
// drops it from the registry; see TaskRunner.Retire. With the follower
// stopped, only a Run can still hold the TaskRunner, and Retire refuses a
// claimed one, so nothing still uses a TaskRunner that is dropped. One that
// can't be retired yet stays registered, and Prune tries again.
func (m *TaskManager) retire(ctx context.Context, taskID int64) {
	m.unfollow(taskID)
	m.mu.Lock()
	tr := m.tasks[taskID]
	m.mu.Unlock()
	if tr == nil {
		return
	}
	ok, err := tr.Retire(ctx)
	if err != nil {
		m.log.Error("failed to prune sandbox", "task", taskID, "err", err)
		return
	}
	if !ok {
		return
	}
	m.mu.Lock()
	delete(m.tasks, taskID)
	m.mu.Unlock()
	m.log.Info("task pruned", "task", taskID)
}

// stale reports whether the task is archived or deleted on the server.
func (m *TaskManager) stale(ctx context.Context, taskID int64) (bool, error) {
	resp, err := m.client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
	if connect.CodeOf(err) == connect.CodeNotFound {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to get task: %w", err)
	}
	return resp.Task.Archived, nil
}
