package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"

	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/x/common"
	"github.com/gritzapp/gritz/internal/x/wakeup"
)

// TaskRunner is the runner's state for a task: its sandbox, the task's
// sandbox record in the runner store, and the task's latest report, which
// RunLoop acts on. It is the runner's object for the task, not for one
// sandbox: it may have no sandbox yet, and it outlives the sandbox it has.
// RunLoop boots, drives and suspends the sandbox as the task's reports ask,
// and destroys it once the task is archived; Restore adopts the record a
// previous process wrote, before RunLoop starts.
type TaskRunner struct {
	// opts is the TaskRunner's configuration, with its defaults applied.
	opts TaskRunnerOptions
	// sbx is the task's sandbox, or nil while it has none, and rec its
	// record, as last written to the runner store; zero while sbx is nil.
	// Only RunLoop uses them, once Restore has set them.
	sbx backend.Sandbox
	rec runnerstore.SandboxRecord
	// up is set by Restore for a sandbox it finds up: RunLoop drives it
	// first, with the slot Load counted for it.
	up bool

	// changeMu guards change, the task's latest report, and hasChange,
	// whether there has been one.
	changeMu  sync.Mutex
	change    runnerstore.TaskChange
	hasChange bool
	// changed wakes RunLoop when Change stores a report.
	changed wakeup.Chan
}

// TaskRunnerOptions configures a TaskRunner.
type TaskRunnerOptions struct {
	// TaskID is the task the TaskRunner runs.
	TaskID int64
	// Backend creates the sandbox the task boots, and opens the sandbox of a
	// record Restore adopts. Required.
	Backend backend.Backend
	// Store holds the task's record. Required.
	Store *runnerstore.Store
	// Slots are the concurrency slots a busy period holds, shared by all the
	// manager's TaskRunners. Required to RunLoop.
	Slots *Slots
	// Queue is the outbox the runner's own runner events are reported to.
	// Required to RunLoop.
	Queue *RunnerEventOutbox
	// Stream is the task's event stream, which drive forwards to the driver.
	// Required to RunLoop.
	Stream *taskevents.Stream
	// Spec builds the sandbox spec of the task's run c, and the driver.v1
	// RunSpec its stream is opened with. Required to RunLoop.
	Spec func(ctx context.Context, c runnerstore.TaskChange) (*backend.Spec, *driverv1.RunSpec, error)
	// Log is the logger; defaults to slog.Default().
	Log *slog.Logger
	// RetryInterval is the first pause between attempts to reach the driver,
	// reattach to it, or destroy an archived task's sandbox; defaults to
	// 250ms.
	RetryInterval time.Duration
	// RetryMaxInterval caps those backoffs; defaults to 10s.
	RetryMaxInterval time.Duration
	// LostTimeout is how long drive keeps reattaching without a good message
	// from the driver before it gives up on it; defaults to 2m.
	LostTimeout time.Duration
	// ReadyTimeout is how long a booted sandbox's driver has to answer;
	// defaults to 1m.
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
// side effects: RunLoop creates the sandbox and Restore adopts a record. It
// panics if Backend or Store is missing.
func NewTaskRunner(opts TaskRunnerOptions) *TaskRunner {
	switch {
	case opts.Backend == nil:
		panic("runner: TaskRunnerOptions.Backend is required")
	case opts.Store == nil:
		panic("runner: TaskRunnerOptions.Store is required")
	}
	opts.Log = cmp.Or(opts.Log, slog.Default())
	opts.RetryInterval = cmp.Or(opts.RetryInterval, defaultRetryInterval)
	opts.RetryMaxInterval = cmp.Or(opts.RetryMaxInterval, defaultRetryMaxInterval)
	opts.LostTimeout = cmp.Or(opts.LostTimeout, defaultLostTimeout)
	opts.ReadyTimeout = cmp.Or(opts.ReadyTimeout, defaultReadyTimeout)
	return &TaskRunner{opts: opts, changed: wakeup.New()}
}

// fatalError is a durable local-store write failure: RunLoop returns it, and
// the manager terminates the runner with it.
type fatalError struct{ err error }

func (e fatalError) Error() string { return e.err.Error() }
func (e fatalError) Unwrap() error { return e.err }

// record returns the task's record of sb's handle at version.
func (tr *TaskRunner) record(sb backend.Sandbox, version int64) runnerstore.SandboxRecord {
	return runnerstore.SandboxRecord{TaskID: tr.opts.TaskID, Version: version, Handle: sb.Handle()}
}

// write records rec in the runner store and on tr. A store write failure is a
// fatalError: the runner store is the runner's durable local state, and a
// write failure here is the same non-transient disk failure as an outbox
// Enqueue failure, so crash rather than lose the handle. The record is
// written even if ctx is done, so a sandbox created as the runner shuts down
// is not left untracked.
func (tr *TaskRunner) write(ctx context.Context, rec runnerstore.SandboxRecord) error {
	if err := tr.opts.Store.WriteSandbox(context.WithoutCancel(ctx), rec); err != nil {
		return fatalError{fmt.Errorf("record handle for task %d: %w", tr.opts.TaskID, err)}
	}
	tr.rec = rec
	return nil
}

// enqueue reports ev to the server through the outbox. A failure to persist
// it is a fatalError.
func (tr *TaskRunner) enqueue(ev model.RunnerEvent) error {
	if err := tr.opts.Queue.Enqueue(ev); err != nil {
		return fatalError{fmt.Errorf("persist %s for task %d: %w", ev.Event, ev.TaskID, err)}
	}
	return nil
}

// fail reports run version of the task failed for why.
func (tr *TaskRunner) fail(version int64, why string) error {
	tr.opts.Log.Error("run failed", "task", tr.opts.TaskID, "version", version, "reason", why)
	return tr.enqueue(model.RunnerEvent{
		TaskID:  tr.opts.TaskID,
		Event:   model.RunnerEventFailed,
		Version: version,
		Reason:  why,
	})
}

// clear deletes the task's record and leaves tr with no sandbox, which it
// always does: a record that cannot be deleted is only logged, since it tracks
// a sandbox that is gone or destroyed. The next boot overwrites it, and if
// there is none, the next runner's Load finds the sandbox gone and drops it.
// The record is deleted even if ctx is done, since the sandbox it tracks
// already is.
func (tr *TaskRunner) clear(ctx context.Context) {
	if err := tr.opts.Store.RemoveSandbox(context.WithoutCancel(ctx), tr.opts.TaskID); err != nil {
		tr.opts.Log.Error("failed to remove record", "task", tr.opts.TaskID, "err", err)
	}
	tr.sbx, tr.rec = nil, runnerstore.SandboxRecord{}
}

// boot brings up the task's sandbox for run spec.Version. With no sandbox,
// boot creates one from spec and records its handle; with one, it stamps the
// record with spec.Version and reuses it, since a task is bound 1:1 to its
// sandbox. Then it boots the sandbox.
//
// If the sandbox is gone, boot deletes its record, leaving tr with no
// sandbox, and returns backend.ErrGone: the caller reports the run failed, and
// the next boot creates a fresh sandbox. Any other failure to boot it keeps
// the sandbox and its record, so it can still be destroyed.
func (tr *TaskRunner) boot(ctx context.Context, spec *backend.Spec) error {
	if tr.sbx == nil {
		if err := tr.create(ctx, spec); err != nil {
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
	return nil
}

// create creates the task's sandbox from spec and records its handle at
// spec.Version. If the record cannot be written, the new sandbox is destroyed,
// on a best-effort basis and even if ctx is done, so it is not left untracked.
// tr has no sandbox.
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

// Restore adopts rec, a record a previous process wrote, and reports the
// status of the sandbox it opens from rec's handle. RunLoop drives an up
// sandbox first, as it reattaches to whatever the driver is doing. Restore
// is called before RunLoop starts, on a TaskRunner with no sandbox.
//
// A handle that cannot be opened, or a sandbox that is gone, leaves tr with no
// sandbox and its record deleted; see clear. A failed status check keeps the
// sandbox and its record, since the sandbox may still be there.
func (tr *TaskRunner) Restore(ctx context.Context, rec runnerstore.SandboxRecord) (backend.Status, error) {
	if tr.sbx != nil {
		panic("runner: Restore on a TaskRunner with a sandbox")
	}
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
		tr.up = true
	}
	return st, nil
}

// destroy destroys the task's sandbox and deletes its record, leaving tr with
// no sandbox. The record is deleted only after the destroy succeeds, so a
// failed destroy leaves the sandbox tracked. Destroying a gone sandbox is not
// an error, so a sandbox that vanished is simply dropped. A task with no
// sandbox has nothing to destroy.
func (tr *TaskRunner) destroy(ctx context.Context) error {
	if tr.sbx == nil {
		return nil
	}
	if err := tr.sbx.Destroy(ctx); err != nil {
		return err
	}
	tr.clear(ctx)
	return nil
}

// Change stores c as the task's latest report, replacing the one before it,
// and wakes RunLoop. RunLoop only ever looks at the latest report, so a
// command superseded before RunLoop got to it is never acted on.
func (tr *TaskRunner) Change(c runnerstore.TaskChange) {
	tr.changeMu.Lock()
	tr.change, tr.hasChange = c, true
	tr.changeMu.Unlock()
	tr.changed.Wake()
}

// latest returns the task's latest report, and false if there has been none.
func (tr *TaskRunner) latest() (runnerstore.TaskChange, bool) {
	tr.changeMu.Lock()
	defer tr.changeMu.Unlock()
	return tr.change, tr.hasChange
}

// wantsRun reports whether c's command needs the driver to run version
// c.Version.
func wantsRun(c runnerstore.TaskChange) bool {
	return c.Command == gritzv1.TaskCommand_START || c.Command == gritzv1.TaskCommand_RESTART
}

// handled is what RunLoop has done, kept in memory only: a runner that
// restarts may boot a sandbox again for a start it already carried out,
// which the driver finds nothing to do for.
type handled struct {
	// driven is the newest version RunLoop has driven, booted or failed: a
	// start or restart needs the driver only if it is newer.
	driven int64
	// stopped is the newest version whose stop needs no report from the
	// runner: the driver ran it, or the runner has reported it stopped.
	stopped int64
}

// RunLoop is the TaskRunner's lifetime: it boots, drives and suspends the
// task's sandbox as the task's reports ask, and returns nil once the task is
// archived and its sandbox destroyed. The manager hands it the task's first
// report before it starts.
//
// Each pass waits for a start or restart newer than the last version it
// handled, takes a slot, boots the sandbox, and holds the driver's stream in
// drive until the driver is idle; then it releases the slot and suspends the
// sandbox. A sandbox Restore found up is driven first, with the slot Load
// counted for it. Only the wait for a slot is preempted by a newer report;
// an archive that arrives during a boot or a drive waits for it to end.
//
// On shutdown RunLoop returns ctx's cause, and leaves the sandbox as it is
// for the next runner's Load. Any other error is a durable-write failure, a
// fatalError, which terminates the runner.
func (tr *TaskRunner) RunLoop(ctx context.Context) error {
	if tr.opts.Slots == nil || tr.opts.Queue == nil || tr.opts.Stream == nil || tr.opts.Spec == nil {
		panic("runner: TaskRunnerOptions.Slots, Queue, Stream and Spec are required to RunLoop")
	}
	if _, ok := tr.latest(); !ok {
		panic("runner: RunLoop before the TaskRunner's first Change")
	}
	var h handled
	if tr.up {
		if err := tr.busy(ctx, &h, nil); err != nil {
			return err
		}
	}
	for {
		c, archived, err := tr.next(ctx, &h)
		if err != nil || archived {
			return err
		}
		if err := tr.acquire(ctx); errors.Is(err, errChanged) {
			continue
		} else if err != nil {
			return err
		}
		h.driven = c.Version
		spec, runSpec, err := tr.opts.Spec(ctx, c)
		if err == nil {
			err = tr.boot(ctx, spec)
		}
		if err != nil {
			if fe := (fatalError{}); errors.As(err, &fe) {
				return err
			}
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			tr.opts.Slots.Release()
			if err := tr.fail(c.Version, err.Error()); err != nil {
				return err
			}
			continue
		}
		if err := tr.busy(ctx, &h, runSpec); err != nil {
			return err
		}
	}
}

// next waits for the task's latest report to want a run newer than h.driven,
// and returns it. Meanwhile it handles the two reports that need no driver:
// a stop newer than h.stopped, which no Run is open to report, is reported
// "stopped" here, and an archive destroys the sandbox and its record, when
// next reports archived. A destroy that fails is retried with backoff.
func (tr *TaskRunner) next(ctx context.Context, h *handled) (c runnerstore.TaskChange, archived bool, err error) {
	retry := doubling(tr.opts.RetryInterval, tr.opts.RetryMaxInterval)
	for {
		var timer <-chan time.Time
		c, _ := tr.latest()
		switch {
		case c.Archived:
			err := tr.destroy(ctx)
			if err == nil {
				tr.opts.Log.Info("task archived, sandbox destroyed", "task", tr.opts.TaskID)
				return c, true, nil
			}
			if ctx.Err() != nil {
				return c, false, context.Cause(ctx)
			}
			tr.opts.Log.Error("failed to destroy archived task's sandbox", "task", tr.opts.TaskID, "err", err)
			timer = time.After(retry.NextBackOff())
		case wantsRun(c) && c.Version > h.driven:
			return c, false, nil
		case c.Command == gritzv1.TaskCommand_STOP && c.Version > h.stopped:
			h.stopped = c.Version
			if err := tr.enqueue(model.RunnerEvent{
				TaskID:  tr.opts.TaskID,
				Event:   model.RunnerEventStopped,
				Version: c.Version,
			}); err != nil {
				return c, false, err
			}
		}
		select {
		case <-tr.changed:
		case <-timer:
		case <-ctx.Done():
			return c, false, context.Cause(ctx)
		}
	}
}

// errChanged is what acquire returns when a report arrived while it waited.
var errChanged = errors.New("task changed while waiting for a slot")

// acquire takes a slot, as Slots.Acquire does, unless a report arrives
// first: then it returns errChanged, and RunLoop looks at the latest report
// again, so a cancel that lands while a start waits means the start never
// runs. A report that arrives as the slot is taken gives the slot back too.
func (tr *TaskRunner) acquire(ctx context.Context) error {
	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-tr.changed:
			cancel(errChanged)
		case <-actx.Done():
		}
	}()
	err := tr.opts.Slots.Acquire(actx)
	cancel(nil)
	// Wait for the watcher, so that only RunLoop reads tr.changed once
	// acquire returns.
	<-done
	if errors.Is(context.Cause(actx), errChanged) {
		if err == nil {
			tr.opts.Slots.Release()
		}
		return errChanged
	}
	return err
}

// busy ends a busy period: it drives the sandbox until the driver is idle,
// releases the slot and suspends the sandbox. On shutdown, or a fatalError,
// it returns the error, with the slot held and the sandbox left as it is.
func (tr *TaskRunner) busy(ctx context.Context, h *handled, spec *driverv1.RunSpec) error {
	version, err := tr.drive(ctx, spec)
	if err != nil {
		return err
	}
	tr.opts.Slots.Release()
	h.driven = max(h.driven, version)
	h.stopped = max(h.stopped, version)
	tr.suspend(ctx)
	return nil
}

// drive holds the driver's Run stream on the task's sandbox for one busy
// period, and returns the version of the driver's last run once it is idle.
// It opens the stream with spec, or with none to reattach, as for a sandbox
// Load found up: the driver keeps the spec it has. It forwards the task's
// events on it; the driver acts on the commands among them, and reports its
// runs. drive reports a run the driver did not as "failed".
//
// If the task's latest report wants a run newer than the driver's last once
// the driver goes idle, the driver went idle before that command reached it:
// drive reopens the stream, with the spec of that report, instead of
// returning.
//
// A dropped stream is reopened with backoff. If the sandbox is gone, or the
// driver cannot be reached for LostTimeout, drive reports the latest run
// "failed" and returns its version; a gone sandbox's record is dropped.
//
// On shutdown drive returns ctx's cause. Any other error is a fatalError.
func (tr *TaskRunner) drive(ctx context.Context, spec *driverv1.RunSpec) (int64, error) {
	taskID := tr.opts.TaskID
	sbx := tr.sbx
	reopen := doubling(tr.opts.RetryInterval, tr.opts.RetryMaxInterval)
	var reopened int64
	for {
		idle, lost, err := tr.run(ctx, sbx, spec)
		if err != nil {
			return 0, err
		}
		if lost != "" {
			return tr.lost(ctx, lost)
		}
		version := idle.GetVersion()
		if version != 0 && !idle.GetReported() {
			if err := tr.fail(version, "driver did not report the run's outcome"); err != nil {
				return 0, err
			}
		}
		c, _ := tr.latest()
		if !wantsRun(c) || c.Version <= version {
			if version > tr.rec.Version {
				// The driver ran a version the runner didn't boot for, on a
				// stream already open: keep the record on the run that went
				// last.
				if err := tr.write(ctx, tr.record(sbx, version)); err != nil {
					return 0, err
				}
			}
			tr.opts.Log.Info("driver idle", "task", taskID, "version", version)
			return version, nil
		}
		tr.opts.Log.Info("driver went idle before the latest command, reopening", "task", taskID, "version", c.Version)
		// A driver that keeps going idle without running c is not hammered.
		if c.Version == reopened && !common.SleepContext(ctx, reopen.NextBackOff()) {
			return 0, context.Cause(ctx)
		}
		reopened = c.Version
		if c.Version != tr.rec.Version {
			if err := tr.write(ctx, tr.record(sbx, c.Version)); err != nil {
				return 0, err
			}
		}
		if _, spec, err = tr.opts.Spec(ctx, c); err != nil {
			if ctx.Err() != nil {
				return 0, context.Cause(ctx)
			}
			return tr.lost(ctx, fmt.Sprintf("failed to build the run's spec: %v", err))
		}
	}
}

// lost reports the task's latest run failed for why, as drive gave up on its
// driver, and returns its version: the latest report's, or the record's if
// that is newer.
func (tr *TaskRunner) lost(ctx context.Context, why string) (int64, error) {
	c, _ := tr.latest()
	version := max(c.Version, tr.rec.Version)
	if tr.sbx != nil && tr.gone(ctx, tr.sbx) {
		tr.clear(ctx)
	}
	if err := tr.fail(version, why); err != nil {
		return 0, err
	}
	return version, nil
}

// gone reports whether sbx is gone, by the platform's account. A failed
// status check counts as not gone.
func (tr *TaskRunner) gone(ctx context.Context, sbx backend.Sandbox) bool {
	st, err := sbx.Status(ctx)
	return err == nil && st == backend.StatusGone
}

// run holds one busy period's stream on sbx until the driver's Idle, which it
// returns: it opens it with spec, waiting for the driver to be ready, unless
// spec is nil, and reopens a dropped stream. A driver that could not be
// reached is reported lost with why. The only error is ctx's cause.
func (tr *TaskRunner) run(ctx context.Context, sbx backend.Sandbox, spec *driverv1.RunSpec) (idle *driverv1.Idle, lost string, err error) {
	if spec != nil {
		idle, lost, err := tr.ready(ctx, sbx, spec)
		if err != nil || lost != "" || idle != nil {
			return idle, lost, err
		}
	}
	return tr.attach(ctx, sbx, spec)
}

// ready opens Run streams on sbx with spec, backing off exponentially, until
// the driver answers one with Hello: opening Run is the readiness check. The
// driver has the runner's readyTimeout for it. If the driver went idle on
// that stream, ready returns its Idle; if the stream dropped after the Hello,
// it returns neither Idle nor why the driver was lost, and the caller
// reattaches. A sandbox that is gone, or a driver that is not ready in time,
// is lost.
func (tr *TaskRunner) ready(ctx context.Context, sbx backend.Sandbox, spec *driverv1.RunSpec) (idle *driverv1.Idle, lost string, err error) {
	opts := tr.opts
	rctx, cancel := context.WithTimeout(ctx, opts.ReadyTimeout)
	defer cancel()
	bo := doubling(opts.RetryInterval, readyMaxInterval)
	for {
		idle, hello, err := tr.session(rctx, sbx, spec)
		if idle != nil || hello {
			return idle, "", nil
		}
		if ctx.Err() != nil {
			return nil, "", context.Cause(ctx)
		}
		if tr.gone(ctx, sbx) {
			return nil, "sandbox gone", nil
		}
		if !common.SleepContext(rctx, bo.NextBackOff()) {
			if ctx.Err() != nil {
				return nil, "", context.Cause(ctx)
			}
			opts.Log.Error("driver not ready", "task", opts.TaskID, "err", err)
			return nil, fmt.Sprintf("driver server did not become ready: %v", err), nil
		}
	}
}

// attach reopens Run streams on sbx until the driver's Idle, which it
// returns, or why the driver was lost. The only error is ctx's cause. spec,
// if not nil, goes on every stream it opens, as on the first.
//
// A dropped stream is reopened, backing off exponentially. A sandbox that is
// gone is lost at once. Reopening gives up once lostTimeout has passed
// without a good message — a Hello — from the driver: attach suspends the
// sandbox and reports the driver lost. That covers a sandbox that went down
// under the stream too, which only happens when something outside the runner
// fails: the runner never suspends a sandbox it holds a stream on. It returns
// only once the suspend succeeded or the sandbox is gone, so the sandbox is
// never up with no Run holding it; while the platform cannot suspend it,
// attach keeps retrying.
func (tr *TaskRunner) attach(ctx context.Context, sbx backend.Sandbox, spec *driverv1.RunSpec) (idle *driverv1.Idle, lost string, err error) {
	opts, taskID := tr.opts, tr.opts.TaskID
	lastGood := time.Now()
	bo := doubling(opts.RetryInterval, opts.RetryMaxInterval)
	var lastErr string
	for {
		idle, hello, err := tr.session(ctx, sbx, spec)
		if ctx.Err() != nil {
			return nil, "", context.Cause(ctx)
		}
		if idle != nil {
			return idle, "", nil
		}
		if hello {
			// The stream was live until it dropped: the driver's idle watchdog
			// would have ended it otherwise.
			lastGood = time.Now()
			bo.Reset()
			lastErr = ""
		} else if tr.gone(ctx, sbx) {
			opts.Log.Error("sandbox gone under its driver", "task", taskID, "err", err)
			return nil, "sandbox gone", nil
		}
		if time.Since(lastGood) >= opts.LostTimeout {
			// Give up on the driver; suspend the sandbox instead. While that
			// fails, keep retrying the attach too in case the driver is back.
			suspendErr := sbx.Suspend(ctx)
			if suspendErr == nil || errors.Is(suspendErr, backend.ErrGone) {
				opts.Log.Error("driver unreachable, sandbox suspended", "task", taskID, "err", err)
				return nil, "driver unreachable", nil
			}
			err = fmt.Errorf("failed to suspend sandbox of lost run: %w (driver: %w)", suspendErr, err)
		}
		if msg := err.Error(); msg != lastErr {
			opts.Log.Warn("run stream dropped, reattaching", "task", taskID, "err", err)
			lastErr = msg
		}
		if !common.SleepContext(ctx, bo.NextBackOff()) {
			return nil, "", context.Cause(ctx)
		}
	}
}

// runStream is the runner's end of a driver Run stream.
type runStream = connect.BidiStreamForClientSimple[driverv1.RunRequest, driverv1.RunResponse]

// session opens one Run stream on sb, with spec if it is not nil, and holds
// it until the driver's Idle, which it returns. Once the driver has answered
// Hello, it forwards the task's events after the driver's last stored one, as
// the task's stream has them, until the stream ends. Otherwise it returns why
// the stream ended; hello reports whether the driver answered Hello before it
// did.
func (tr *TaskRunner) session(ctx context.Context, sbx backend.Sandbox, spec *driverv1.RunSpec) (idle *driverv1.Idle, hello bool, err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	client, err := sbx.Dial(ctx, "")
	if err != nil {
		return nil, false, err
	}
	stream, err := client.Run(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = stream.CloseResponse() }()
	// The HTTP/2 transport doesn't watch the context of a request whose body
	// is still open: closing the request side is what makes a cancelled
	// stream's Receive return.
	stop := context.AfterFunc(ctx, func() { _ = stream.CloseRequest() })
	defer stop()
	first := &driverv1.RunRequest{}
	if spec != nil {
		first.Msg = &driverv1.RunRequest_Spec{Spec: spec}
	}
	// A Send to a stream the driver has ended fails with io.EOF; Receive has
	// its error.
	if err := stream.Send(first); err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	msg, err := stream.Receive()
	if err != nil {
		return nil, false, err
	}
	if msg.GetHello() == nil {
		return nil, false, fmt.Errorf("unexpected first run response: %v", msg)
	}
	var forwarding sync.WaitGroup
	defer forwarding.Wait()
	// Cancelled before the wait, so the forwarder stops with the stream.
	defer cancel(nil)
	forwarding.Go(func() {
		if err := tr.forward(ctx, stream, msg.GetHello().GetLastEventId()); err != nil {
			cancel(err)
		}
	})
	for {
		msg, err := stream.Receive()
		if err != nil {
			if cause := context.Cause(ctx); errors.Is(cause, errReadEvents) {
				err = cause
			} else if errors.Is(err, io.EOF) {
				err = errors.New("run stream ended before Idle")
			}
			return nil, true, err
		}
		if idle := msg.GetIdle(); idle != nil {
			return idle, true, nil
		}
	}
}

// errReadEvents is what forward's error wraps: the task's stream could not be
// read, so the Run stream is torn down and reopened.
var errReadEvents = errors.New("failed to read task events")

// forward sends the task's events after the event with id after on stream:
// first every one already stored, in one batch even if there are none, which
// the driver waits for before it can go idle, then the rest as they are
// stored, until ctx ends or the stream does. It returns an error, wrapping
// errReadEvents, only if the task's stream cannot be read.
func (tr *TaskRunner) forward(ctx context.Context, stream *runStream, after int64) error {
	read := tr.opts.Stream.Stored
	for {
		events, err := read(ctx, after)
		read = tr.opts.Stream.Read
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("%w: %w", errReadEvents, err)
		}
		if err := stream.Send(&driverv1.RunRequest{Msg: &driverv1.RunRequest_Events{
			Events: &driverv1.TaskEvents{Events: events},
		}}); err != nil {
			// The stream has ended; session's Receive has why.
			return nil
		}
		if len(events) > 0 {
			after = events[len(events)-1].GetId()
		}
	}
}

// suspend suspends the task's sandbox, if it has one, once its busy period
// is over. Failures are logged: the runs' outcomes are already known.
func (tr *TaskRunner) suspend(ctx context.Context) {
	if tr.sbx == nil {
		return
	}
	if err := tr.sbx.Suspend(ctx); err != nil && !errors.Is(err, backend.ErrGone) {
		tr.opts.Log.Error("failed to suspend sandbox after its busy period", "task", tr.opts.TaskID, "err", err)
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
