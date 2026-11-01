package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"

	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/x/common"
)

// TaskRunner is the runner's state for a task: its sandbox, the task's
// sandbox record in the runner store, the run being followed on it, and the
// follower that acts on the task's commands; see Follow. It is
// the runner's object for the task, not for one sandbox: it may have no
// sandbox yet, and it outlives the sandbox it has. Boot creates a sandbox and writes its record,
// and Restore adopts the record a previous process wrote; Destroy and Retire
// destroy the sandbox and delete its record, and Boot and Restore delete the
// record of a sandbox they find gone. The TaskRunner stays usable throughout,
// and the next Boot creates a fresh sandbox.
type TaskRunner struct {
	// opts is the TaskRunner's configuration, with its defaults applied.
	opts TaskRunnerOptions
	// mu serializes the TaskRunner's methods. Run holds it only to capture the
	// sandbox it follows and to release its claim.
	mu sync.Mutex
	// sbx is the task's sbx, or nil while it has none. It is set by Boot,
	// which creates it, or by Restore, and cleared by clear. Guarded by mu.
	sbx backend.Sandbox
	// rec is sb's record, as last written to the runner store; zero while sb
	// is nil. Guarded by mu.
	rec runnerstore.SandboxRecord
	// running is set from Boot (or Restore) until Run returns, so a second run
	// is not started meanwhile, and the sandbox is not destroyed under it.
	// Guarded by mu.
	running bool
	// runs counts the Run goroutines start started that have not returned;
	// see wait.
	runs sync.WaitGroup

	// stopFollow stops the Follow goroutine follow started, and followDone is
	// closed once it has returned; both are nil while there is none. Only the
	// manager's Run goroutine uses them.
	stopFollow context.CancelFunc
	followDone chan struct{}
}

// TaskRunnerOptions configures a TaskRunner.
type TaskRunnerOptions struct {
	// TaskID is the task the TaskRunner runs.
	TaskID int64
	// Backend creates the sandbox Boot needs, and opens the sandbox of a
	// record Restore adopts. Required.
	Backend backend.Backend
	// Store holds the task's record. Required.
	Store *runnerstore.Store
	// Events receives the TaskRunner's events; see TaskRunner.send. Required.
	Events chan<- TaskEvent
	// Slots are the concurrency slots a run holds, shared by all the
	// manager's TaskRunners. Required to Follow or Start.
	Slots *Slots
	// Queue is the outbox the commands' runner events are reported to.
	// Required to Follow.
	Queue *RunnerEventOutbox
	// Watch is the task's latest state in the event log, whose commands
	// Follow acts on. Required to Follow.
	Watch *taskevents.TaskWatch
	// Spec builds the sandbox spec of the task's run c, and the driver.v1
	// RunSpec its run is started with. Required to Follow or Start.
	Spec func(ctx context.Context, c runnerstore.TaskChange) (*backend.Spec, *driverv1.RunSpec, error)
	// PollInterval caps Follow's backoff between attempts at a command;
	// defaults to 30s.
	PollInterval time.Duration
	// Log is the logger; defaults to slog.Default().
	Log *slog.Logger
	// RetryInterval is the first pause between attempts to reach the driver
	// or reattach to a run; defaults to 250ms.
	RetryInterval time.Duration
	// RetryMaxInterval caps the reattach backoff; defaults to 10s.
	RetryMaxInterval time.Duration
	// LostTimeout is how long Run keeps reattaching without a good message
	// from the driver before it gives up on the run; defaults to 2m.
	LostTimeout time.Duration
	// ReadyTimeout is how long a started sandbox's driver has to answer and
	// accept its run; defaults to 1m.
	ReadyTimeout time.Duration
}

const (
	// defaultRetryInterval is TaskRunnerOptions.RetryInterval's default.
	defaultRetryInterval = 250 * time.Millisecond
	// defaultRetryMaxInterval is TaskRunnerOptions.RetryMaxInterval's default.
	defaultRetryMaxInterval = 10 * time.Second
	// defaultLostTimeout is TaskRunnerOptions.LostTimeout's default.
	defaultLostTimeout = 2 * time.Minute
	// defaultReadyTimeout is TaskRunnerOptions.ReadyTimeout's default. It
	// counts whatever the image's entrypoint does before it execs the driver,
	// and a Lambda VM's boot.
	defaultReadyTimeout = time.Minute
	// readyMaxInterval caps the backoff between attempts to reach a starting
	// driver.
	readyMaxInterval = 2 * time.Second
)

// NewTaskRunner returns a TaskRunner for opts.TaskID with no sandbox. It has no
// side effects: Boot creates the sandbox and Restore adopts a record. It
// panics if Backend, Store or Events is missing.
func NewTaskRunner(opts TaskRunnerOptions) *TaskRunner {
	switch {
	case opts.Backend == nil:
		panic("runner: TaskRunnerOptions.Backend is required")
	case opts.Store == nil:
		panic("runner: TaskRunnerOptions.Store is required")
	case opts.Events == nil:
		panic("runner: TaskRunnerOptions.Events is required")
	}
	opts.Log = cmp.Or(opts.Log, slog.Default())
	opts.RetryInterval = cmp.Or(opts.RetryInterval, defaultRetryInterval)
	opts.RetryMaxInterval = cmp.Or(opts.RetryMaxInterval, defaultRetryMaxInterval)
	opts.LostTimeout = cmp.Or(opts.LostTimeout, defaultLostTimeout)
	opts.ReadyTimeout = cmp.Or(opts.ReadyTimeout, defaultReadyTimeout)
	opts.PollInterval = cmp.Or(opts.PollInterval, defaultPollInterval)
	return &TaskRunner{opts: opts}
}

// TaskEvent is something a TaskRunner reports to its manager, on the
// manager's events channel; see TaskManager.HandleEvents. Only this package
// implements it, so the manager's type switch is exhaustive.
type TaskEvent interface{ taskEvent() }

// TaskFatalEvent reports a durable write failure that must terminate the
// runner.
type TaskFatalEvent struct {
	TaskID int64
	Err    error
}

// TaskLostEvent reports a run whose outcome the driver did not report: the
// manager reports it "failed", scoped to Version.
type TaskLostEvent struct {
	TaskID  int64
	Version int64
	Reason  string
}

func (TaskFatalEvent) taskEvent() {}
func (TaskLostEvent) taskEvent()  {}

// ErrRunning is returned by Boot and Destroy when a Run still follows the
// task's previous run, so nothing was done. Running can report false then: the
// sandbox may already be suspending after its run (a Lambda VM).
var ErrRunning = errors.New("sandbox is still running")

// record returns the task's record of sb's handle at version.
func (tr *TaskRunner) record(sb backend.Sandbox, version int64) runnerstore.SandboxRecord {
	return runnerstore.SandboxRecord{TaskID: tr.opts.TaskID, Version: version, Handle: sb.Handle()}
}

// write records rec in the runner store and on tr. A store write failure
// terminates the runner, through a TaskFatalEvent: the runner store is the
// runner's durable local state, and a write failure here is the same
// non-transient disk failure as an outbox Enqueue failure, so crash rather than
// lose the handle. The record is written even if ctx is done, so a sandbox
// created as the runner shuts down is not left untracked. The returned error
// still unwinds the caller (releasing the concurrency slot) as ctx tears down. The
// caller holds tr.mu.
func (tr *TaskRunner) write(ctx context.Context, rec runnerstore.SandboxRecord) error {
	if err := tr.opts.Store.WriteSandbox(context.WithoutCancel(ctx), rec); err != nil {
		tr.send(ctx, TaskFatalEvent{
			TaskID: tr.opts.TaskID,
			Err:    fmt.Errorf("record handle for task %d: %w", tr.opts.TaskID, err),
		})
		return fmt.Errorf("failed to record task handle: %w", err)
	}
	tr.rec = rec
	return nil
}

// send sends ev to the manager, or drops it if ctx is done first: the runner
// is shutting down, and the manager's HandleEvents, which runs on the same
// context, may have stopped. The manager never takes a TaskRunner's lock to
// handle an event, so the caller may hold tr.mu.
func (tr *TaskRunner) send(ctx context.Context, ev TaskEvent) {
	select {
	case tr.opts.Events <- ev:
	case <-ctx.Done():
		tr.opts.Log.Warn("task event dropped at shutdown", "task", tr.opts.TaskID, "event", ev)
	}
}

// clear deletes the task's record and leaves tr with no sandbox, which it
// always does: a record that cannot be deleted is only logged, since it tracks
// a sandbox that is gone or destroyed. The next Boot overwrites it, and if
// there is none, the next boot's Load finds the sandbox gone and drops it. The
// caller holds tr.mu. The record is deleted even if ctx is done, since the
// sandbox it tracks already is.
func (tr *TaskRunner) clear(ctx context.Context) {
	if err := tr.opts.Store.RemoveSandbox(context.WithoutCancel(ctx), tr.opts.TaskID); err != nil {
		tr.opts.Log.Error("failed to remove record", "task", tr.opts.TaskID, "err", err)
	}
	tr.sbx, tr.rec = nil, runnerstore.SandboxRecord{}
}

// Version returns the version the task's record is stamped with, or 0 if it
// has no sandbox.
func (tr *TaskRunner) Version() int64 {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.rec.Version
}

// Running reports whether the task's sandbox is up: false if it has none.
func (tr *TaskRunner) Running(ctx context.Context) (bool, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.sbx == nil {
		return false, nil
	}
	st, err := tr.sbx.Status(ctx)
	if err != nil {
		return false, err
	}
	return st == backend.StatusUp, nil
}

// Boot brings up the task's sandbox for run spec.Version, claiming tr for the
// run Run then starts. With no sandbox, Boot creates one from spec and records
// its handle; with one, it stamps the record with spec.Version and reuses it,
// since a task is bound 1:1 to its sandbox. Then it boots the sandbox.
//
// If the sandbox is gone, Boot deletes its record, leaving tr with no
// sandbox, and returns backend.ErrGone: the caller reports the run failed, and
// the next Boot creates a fresh sandbox. Any other failure to boot it keeps
// the sandbox and its record, so Destroy can still destroy it. If a Run still
// follows the task's previous run, Boot returns ErrRunning without doing
// anything.
func (tr *TaskRunner) Boot(ctx context.Context, spec backend.Spec) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.running {
		return ErrRunning
	}
	if tr.sbx == nil {
		if err := tr.create(ctx, &spec); err != nil {
			return err
		}
	} else if rec := tr.record(tr.sbx, spec.Version); !reflect.DeepEqual(rec, tr.rec) {
		if err := tr.write(ctx, rec); err != nil {
			return err
		}
	}
	if err := tr.sbx.Boot(ctx); errors.Is(err, backend.ErrGone) {
		tr.clear(ctx)
		return backend.ErrGone
	} else if err != nil {
		return err
	}
	tr.running = true
	return nil
}

// create creates the task's sandbox from spec and records its handle at
// spec.Version. If the record cannot be written, the new sandbox is destroyed,
// on a best-effort basis and even if ctx is done, so it is not left untracked.
// The caller holds tr.mu, and tr has no sandbox.
func (tr *TaskRunner) create(ctx context.Context, spec *backend.Spec) error {
	sb, err := tr.opts.Backend.Create(ctx, spec)
	if err != nil {
		return err
	}
	if err := tr.write(ctx, tr.record(sb, spec.Version)); err != nil {
		if err := sb.Destroy(context.WithoutCancel(ctx)); err != nil {
			tr.opts.Log.Error("failed to destroy unrecorded sandbox", "task", tr.opts.TaskID, "err", err)
		}
		return err
	}
	tr.sbx = sb
	return nil
}

// Restore adopts rec, a record a previous process wrote, on a TaskRunner with
// no sandbox yet, and reports the status of the sandbox it opens from rec's
// handle. An up sandbox claims tr for a Run reattaching to the recorded run, in
// the same lock acquisition that installs it, as Boot does for a new run.
//
// A handle that cannot be opened, or a sandbox that is gone, leaves tr with no
// sandbox and its record deleted; see clear. A failed status check keeps the
// sandbox and its record, since the sandbox may still be there, and claims
// nothing.
func (tr *TaskRunner) Restore(ctx context.Context, rec runnerstore.SandboxRecord) (backend.Status, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	sb, err := tr.opts.Backend.Open(rec.Handle)
	if err != nil {
		tr.clear(ctx)
		return backend.StatusUnknown, fmt.Errorf("failed to open sandbox: %w", err)
	}
	tr.sbx, tr.rec = sb, rec
	st, err := sb.Status(ctx)
	if err != nil {
		return backend.StatusUnknown, fmt.Errorf("failed to get sandbox status: %w", err)
	}
	switch st {
	case backend.StatusGone:
		tr.clear(ctx)
	case backend.StatusUp:
		tr.running = true
	}
	return st, nil
}

// Kill asks the driver of the task's sandbox to stop the recorded run. It
// reports whether a running run was stopped — in that case the driver owns the
// terminal event report (see the driver-owned-events proposal). A sandbox that
// is not up has no driver to ask, so nothing is stopped; neither is a run that
// already finished or is not the recorded version, or a task with no sandbox.
func (tr *TaskRunner) Kill(ctx context.Context) (bool, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.sbx == nil {
		return false, nil
	}
	status, err := tr.sbx.Status(ctx)
	if err != nil {
		return false, err
	}
	if status != backend.StatusUp {
		return false, nil
	}
	client, err := tr.sbx.Dial(ctx, "")
	if err != nil {
		return false, err
	}
	tr.opts.Log.Info("stopping run", "task", tr.opts.TaskID, "version", tr.rec.Version)
	resp, err := client.Stop(ctx, &driverv1.StopRequest{Version: tr.rec.Version})
	if err != nil {
		return false, fmt.Errorf("failed to stop run: %w", err)
	}
	return resp.GetStopped(), nil
}

// Destroy destroys the task's sandbox and deletes its record, leaving tr with
// no sandbox. The record is deleted only after the destroy succeeds, so a
// failed destroy leaves the sandbox tracked and the removal is retried.
// Destroying a gone sandbox is not an error, so a sandbox that vanished is
// simply dropped. A task with no sandbox has nothing to destroy. If a run is
// claimed, from Boot (or Restore) until its Run returns, Destroy returns
// ErrRunning without doing anything.
func (tr *TaskRunner) Destroy(ctx context.Context) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.running {
		return ErrRunning
	}
	if tr.sbx == nil {
		return nil
	}
	if err := tr.sbx.Destroy(ctx); err != nil {
		return err
	}
	tr.clear(ctx)
	return nil
}

// Retire destroys the task's sandbox, as Destroy does, if tr is idle: no run
// is claimed, and the sandbox is not up. Both a suspended husk and a gone sandbox
// count as "no live sandbox". It reports whether tr is now idle with no
// sandbox, which it also is if it had none.
func (tr *TaskRunner) Retire(ctx context.Context) (bool, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.running {
		return false, nil
	}
	if tr.sbx == nil {
		return true, nil
	}
	status, err := tr.sbx.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get sandbox status: %w", err)
	}
	if status == backend.StatusUp {
		return false, nil
	}
	if err := tr.sbx.Destroy(ctx); err != nil {
		return false, fmt.Errorf("failed to remove sandbox: %w", err)
	}
	tr.clear(ctx)
	return true, nil
}

// follow runs Follow on its own goroutine, on the runner's root context ctx,
// unless it already runs. The manager calls it for each report of the
// unarchived task.
func (tr *TaskRunner) follow(ctx context.Context) {
	if tr.followDone != nil {
		return
	}
	fctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	tr.stopFollow, tr.followDone = cancel, done
	go func() {
		defer close(done)
		tr.Follow(ctx, fctx)
	}()
}

// unfollow stops the Follow goroutine, if there is one, and waits for it to
// return: a command it is handling is carried out first.
func (tr *TaskRunner) unfollow() {
	if tr.followDone == nil {
		return
	}
	tr.stopFollow()
	<-tr.followDone
	tr.stopFollow, tr.followDone = nil, nil
}

// Follow is the task's follower. It acts on the command of the task's latest
// change, which it watches in the event log: commands are a desired state, so
// the latest supersedes the ones before it, and a change that repeats a
// command already handled does nothing. Once the command is carried out, it stores the
// command's event id as handled and waits for the next change.
//
// A command that isn't carried out, because its handler failed or there was
// no concurrency slot for it, leaves handled where it was. The follower tries
// again once a run releases its slot, the task has a new change, or a backoff
// ends, acting on the latest change each time, so a newer command takes the
// place of the one that waited.
//
// Commands are carried out on ctx, the runner's root context, as the Run
// goroutines they start must outlive the follower; fctx ends the follower.
// A store error terminates the runner, through a TaskFatalEvent.
func (tr *TaskRunner) Follow(ctx, fctx context.Context) {
	opts, taskID := tr.opts, tr.opts.TaskID
	handled, err := opts.Store.Handled(ctx, taskID)
	if err != nil {
		if ctx.Err() == nil {
			tr.send(ctx, TaskFatalEvent{TaskID: taskID, Err: err})
		}
		return
	}
	retry := doubling(min(time.Second, opts.PollInterval), opts.PollInterval)
	for {
		// Take the signals before reading and handling, so a change stored
		// meanwhile, or a slot released while the handler runs, still wakes
		// the follower.
		released := opts.Slots.Released()
		changed := opts.Watch.Changed()
		c, ok := opts.Watch.Latest()
		if !ok || c.CommandEventID <= handled {
			select {
			case <-changed:
				continue
			case <-fctx.Done():
				return
			}
		}
		if err := tr.command(ctx, c); err != nil {
			if errors.Is(err, errNoSlot) || errors.Is(err, ErrRunning) {
				opts.Log.Debug("command waits", "task", taskID, "command", c.Command, "version", c.Version, "reason", err)
			} else {
				opts.Log.Error("failed to handle command", "task", taskID, "command", c.Command, "version", c.Version, "err", err)
			}
			if !await(fctx, released, changed, retry.NextBackOff()) {
				return
			}
			continue
		}
		if err := opts.Store.SetHandled(ctx, taskID, c.CommandEventID); err != nil {
			if ctx.Err() == nil {
				tr.send(ctx, TaskFatalEvent{TaskID: taskID, Err: err})
			}
			return
		}
		handled = c.CommandEventID
		retry.Reset()
	}
}

// await waits for a reason to try a command again: released firing, changed
// firing on a new change to the task, or the backoff d running out. It
// reports false if ctx ended first.
func await(ctx context.Context, released, changed <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-released:
	case <-changed:
	case <-timer.C:
	case <-ctx.Done():
	}
	return ctx.Err() == nil
}

// command carries out the task's command c. A non-nil error means it wasn't
// carried out, and the follower tries again. No command needs nothing done.
func (tr *TaskRunner) command(ctx context.Context, c runnerstore.TaskChange) error {
	switch c.Command {
	case gritzv1.TaskCommand_STOP:
		return tr.stopTask(ctx, c)
	case gritzv1.TaskCommand_RESTART:
		return tr.restartTask(ctx, c)
	case gritzv1.TaskCommand_START:
		return tr.startTask(ctx, c)
	default:
		return nil
	}
}

// errNoSlot is what restartTask and startTask return when every concurrency
// slot is taken: the run waits for one, and its command is tried again.
var errNoSlot = errors.New("concurrency limit reached")

// enqueue reports ev to the server through the outbox. A failure to persist
// it terminates the runner, through a TaskFatalEvent.
func (tr *TaskRunner) enqueue(ctx context.Context, ev model.RunnerEvent) {
	if err := tr.opts.Queue.Enqueue(ev); err != nil {
		tr.send(ctx, TaskFatalEvent{
			TaskID: ev.TaskID,
			Err:    fmt.Errorf("persist %s for task %d: %w", ev.Event, ev.TaskID, err),
		})
	}
}

// stopTask, restartTask and startTask each handle one command for the task. A
// non-nil error means the command wasn't carried out and should be retried;
// the caller decides when.
func (tr *TaskRunner) stopTask(ctx context.Context, c runnerstore.TaskChange) error {
	signalled, err := tr.Kill(ctx)
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
	tr.enqueue(ctx, model.RunnerEvent{
		TaskID:  c.ID,
		Event:   model.RunnerEventStopped,
		Version: c.Version,
	})
	return nil
}

func (tr *TaskRunner) restartTask(ctx context.Context, c runnerstore.TaskChange) error {
	// Idempotency guard: the record is stamped with the version of
	// the run it launched, so a record already on the task's current
	// version means this restart's new run was already launched, by
	// this runner before it restarted with the command unhandled. The
	// driver takes ~12s to boot, so killing here would SIGKILL the run
	// we just started (issue #1520). Do nothing: "started" consumes
	// the command.
	if tr.Version() == c.Version {
		tr.opts.Log.Debug("restart already processed, waiting for started", "task", c.ID, "version", c.Version)
		return nil
	}
	// Kill existing sandbox if running. The old run's "stopped"
	// is rejected by the status guard while the restart command is
	// pending, so the command survives until the new run's
	// "started" consumes it.
	if _, err := tr.Kill(ctx); err != nil {
		tr.opts.Log.Error("failed to kill task for restart", "task", c.ID, "err", err)
	}
	// Atomically acquire a slot before starting
	if !tr.opts.Slots.TryAcquire() {
		return errNoSlot
	}
	err := tr.Start(ctx, c)
	if errors.Is(err, ErrRunning) {
		// The killed run is still followed. Nothing was started, so
		// the slot goes back, and the restart is retried once that
		// run releases its own.
		tr.opts.Slots.Release()
		return err
	}
	if err != nil {
		tr.opts.Slots.Release() // Release the slot on failure
		tr.opts.Log.Error("failed to start task", "task", c.ID, "err", err)
		tr.enqueue(ctx, model.RunnerEvent{
			TaskID:  c.ID,
			Event:   model.RunnerEventFailed,
			Version: c.Version,
			Reason:  err.Error(),
		})
		return nil
	}
	// The "started" event is submitted by the driver on startup
	return nil
}

func (tr *TaskRunner) startTask(ctx context.Context, c runnerstore.TaskChange) error {
	// Check if the sandbox is already running
	running, err := tr.Running(ctx)
	if err != nil {
		return fmt.Errorf("failed to check if task is running: %w", err)
	}
	if running {
		if tr.Version() == c.Version {
			// The sandbox runs this version: do nothing, and let the
			// run finish. If the start is still pending then, the run
			// boundary moves it to the next version, in a new command
			// event.
			tr.opts.Log.Debug("start command: sandbox already running, waiting for it to finish", "task", c.ID)
			return nil
		}
		// The sandbox still runs an earlier version, such as the run
		// whose end moved this start to the next version, before the
		// runner has suspended it. The start is retried once that run
		// releases its slot.
		return ErrRunning
	}

	// Sandbox not running - atomically acquire a slot before starting
	if !tr.opts.Slots.TryAcquire() {
		return errNoSlot
	}
	err = tr.Start(ctx, c)
	if errors.Is(err, ErrRunning) {
		// The sandbox is suspended, but a Run still follows its last
		// run. Nothing was started, so the slot goes back, and the
		// start is retried once that Run releases its own.
		tr.opts.Slots.Release()
		return err
	}
	if err != nil {
		tr.opts.Slots.Release() // Release the slot on failure
		tr.opts.Log.Error("failed to start task", "task", c.ID, "err", err)
		tr.enqueue(ctx, model.RunnerEvent{
			TaskID:  c.ID,
			Event:   model.RunnerEventFailed,
			Version: c.Version,
			Reason:  err.Error(),
		})
		return nil
	}
	// The "started" event is submitted by the driver on startup
	return nil
}

// Start brings up the task's sandbox and starts the task's run c on it, on a
// Run goroutine. It creates the sandbox if tr has none, or reuses it, and
// stamps the record with c's version; see Boot for what a failure leaves
// behind.
//
// Start returns once the platform reports the sandbox running: waiting for the
// driver and starting the run happen in Run, so a slow sandbox does not hold
// up the task's follower. A failure there is reported as the run's "failed".
//
// If a Run still follows the task's previous run, Start returns ErrRunning
// without starting anything, and the caller gives back the concurrency slot it
// acquired. The task token minted for the spec then goes unused.
func (tr *TaskRunner) Start(ctx context.Context, c runnerstore.TaskChange) error {
	spec, runSpec, err := tr.opts.Spec(ctx, c)
	if err != nil {
		return err
	}
	if err := tr.Boot(ctx, *spec); err != nil {
		return err
	}
	// Follow the run on the runner's root context: the run is started there,
	// after the record is stamped with its version, so a runner that restarts
	// in between reattaches to it.
	tr.start(ctx, runSpec)
	return nil
}

// start calls Run on its own goroutine, which holds the run's concurrency
// slot. Once Run returns, the slot is released and the followers waiting for
// one woken, unless the runner is shutting down: then the slot stays held and
// the sandbox is left for the next boot.
func (tr *TaskRunner) start(ctx context.Context, spec *driverv1.RunSpec) {
	tr.runs.Go(func() {
		if err := tr.Run(ctx, spec); err != nil {
			return
		}
		tr.opts.Slots.Release()
	})
}

// wait blocks until every Run goroutine start started has returned.
func (tr *TaskRunner) wait() { tr.runs.Wait() }

// Run follows the run version tr's record is stamped with: it starts the run
// with spec first, or, with spec nil, reattaches to a run already started. It
// returns once the run finished, after suspending the sandbox, and reports the
// outcome if the driver did not. A run that could not be started or was lost
// gets the runner's fallback "failed", scoped to its version.
//
// A lost run is sent to the manager as a TaskLostEvent before tr is released
// for another run.
//
// Cancelled by ctx, Run returns its error with no event: the runner is
// shutting down, and the sandbox stays alive for next-boot rehydration. That
// is the only error.
func (tr *TaskRunner) Run(ctx context.Context, spec *driverv1.RunSpec) error {
	tr.mu.Lock()
	tr.running = true
	sbx, version := tr.sbx, tr.rec.Version
	tr.mu.Unlock()
	defer func() {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		tr.running = false
	}()

	lost, err := tr.run(ctx, sbx, version, spec)
	if err != nil {
		return err // shutdown: leave the sandbox alive for next-boot rehydration
	}
	if lost == "" {
		tr.opts.Log.Info("run finished, driver already reported", "task", tr.opts.TaskID, "version", version)
		return nil
	}
	tr.opts.Log.Error("run lost", "task", tr.opts.TaskID, "version", version, "reason", lost)
	tr.send(ctx, TaskLostEvent{TaskID: tr.opts.TaskID, Version: version, Reason: lost})
	return nil
}

// run starts the run version on sb with spec, unless spec is nil, then
// attaches to it. A run that could not be started is reported lost with why,
// after sb is suspended: nothing runs on it. The only error is ctx's.
func (tr *TaskRunner) run(ctx context.Context, sbx backend.Sandbox, version int64, spec *driverv1.RunSpec) (lost string, err error) {
	if spec != nil {
		if err := tr.startRun(ctx, sbx, version, spec); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			tr.opts.Log.Error("failed to start run", "task", tr.opts.TaskID, "version", version, "err", err)
			tr.suspend(ctx, sbx)
			return err.Error(), nil
		}
	}
	return tr.attach(ctx, sbx, version)
}

// startRun waits for sb's driver to answer Status, backing off
// exponentially, then starts run version with spec on it, returning once the
// driver has accepted it. A Run that is rejected because another stream is
// open is retried. The driver has the runner's readyTimeout to do both.
func (tr *TaskRunner) startRun(ctx context.Context, sbx backend.Sandbox, version int64, spec *driverv1.RunSpec) error {
	opts := tr.opts
	ctx, cancel := context.WithTimeout(ctx, opts.ReadyTimeout)
	defer cancel()
	client, err := sbx.Dial(ctx, "")
	if err != nil {
		return err
	}
	bo := doubling(opts.RetryInterval, readyMaxInterval)
	for {
		_, err := client.Status(ctx, &driverv1.StatusRequest{})
		if err == nil {
			break
		}
		if !common.SleepContext(ctx, bo.NextBackOff()) {
			return fmt.Errorf("driver server did not become ready: %w", err)
		}
	}
	for {
		err := accept(ctx, client, &driverv1.RunRequest{Version: version, Spec: spec})
		if err == nil {
			return nil
		}
		if connect.CodeOf(err) != connect.CodeUnavailable || !common.SleepContext(ctx, opts.RetryInterval) {
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

// attach follows run version on sb until its Finished, then suspends sb. It returns why the driver's report of the run's outcome was lost, or
// "" if the driver reported it. The only error is ctx's.
//
// A dropped stream is reattached, backing off exponentially. A driver with no
// such run is a lost run. Reattaching gives up once lostTimeout has passed
// without a good message — an accepted stream — from the driver: attach suspends
// the sandbox and reports the run lost. That covers a sandbox that went down
// under the run too, which only happens when something outside the runner
// fails: the runner never suspends a sandbox it is following a run on. It returns only once the
// suspend succeeded or the sandbox is gone, so the sandbox is never up with no
// Run following it; while the platform cannot suspend it, attach keeps
// retrying.
func (tr *TaskRunner) attach(ctx context.Context, sbx backend.Sandbox, version int64) (lost string, err error) {
	opts, taskID := tr.opts, tr.opts.TaskID
	lastGood := time.Now()
	bo := doubling(opts.RetryInterval, opts.RetryMaxInterval)
	var lastErr string
	for {
		finished, accepted, err := attachOnce(ctx, sbx, version)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if finished != nil {
			tr.suspend(ctx, sbx)
			if !finished.GetReported() {
				return "driver did not report the run's outcome", nil
			}
			return "", nil
		}
		if isTerminal(err) {
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come.
			opts.Log.Error("driver has no such run", "task", taskID, "version", version, "err", err)
			if connect.CodeOf(err) == connect.CodeNotFound {
				// Nothing runs on the sandbox, which would otherwise stay up.
				// On FailedPrecondition or Aborted a newer run is active on
				// it, which suspending would kill.
				tr.suspend(ctx, sbx)
			}
			return "driver has no such run", nil
		}
		if accepted {
			// The stream was live until it dropped: the driver's idle watchdog
			// would have ended it otherwise.
			lastGood = time.Now()
			bo.Reset()
			lastErr = ""
		}
		if time.Since(lastGood) >= opts.LostTimeout {
			// Give up on the driver; suspend the sandbox instead. While that
			// fails, keep retrying the attach too in case the driver is back.
			suspendErr := sbx.Suspend(ctx)
			if suspendErr == nil || errors.Is(suspendErr, backend.ErrGone) {
				opts.Log.Error("run lost: driver unreachable, sandbox suspended", "task", taskID, "version", version, "err", err)
				return "driver unreachable", nil
			}
			err = fmt.Errorf("failed to suspend sandbox of lost run: %w (driver: %w)", suspendErr, err)
		}
		if msg := err.Error(); msg != lastErr {
			opts.Log.Warn("run stream dropped, reattaching", "task", taskID, "version", version, "err", err)
			lastErr = msg
		}
		if !common.SleepContext(ctx, bo.NextBackOff()) {
			return "", ctx.Err()
		}
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

// suspend suspends the run's sandbox sb once the run is over. Failures are logged:
// the run's outcome is already known.
func (tr *TaskRunner) suspend(ctx context.Context, sb backend.Sandbox) {
	if err := sb.Suspend(ctx); err != nil && !errors.Is(err, backend.ErrGone) {
		tr.opts.Log.Error("failed to suspend sandbox after its run", "task", tr.opts.TaskID, "err", err)
	}
}

// doubling returns a backoff that starts at initial and doubles up to
// maxInterval, without jitter. It never stops: callers bound it themselves.
func doubling(initial, maxInterval time.Duration) *backoff.ExponentialBackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = initial
	bo.MaxInterval = maxInterval
	bo.Multiplier = 2
	bo.RandomizationFactor = 0
	bo.Reset()
	return bo
}
