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
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/x/outbox"
)

// TaskRunner is the runner's state for a task that has a sandbox: the
// sandbox, the task's record in taskstate/, and the run being followed on it.
// It is created around a sandbox once that is created, and writes its record
// when Start first stamps it with a version; or Load restores it from a record
// a previous process wrote. It deletes the record when it is destroyed or finds
// its sandbox gone.
//
// Its operations lock it and return removed if it is set: the TaskRunner was
// destroyed, or found its sandbox gone, while the operation waited for its
// lock. They do not look the task up again, so a task created anew after the
// removal is left to the next operation.
//
// Its TaskManager sets the exported fields when it constructs it (Load sets
// Sandbox otherwise), and does not change them after.
type TaskRunner struct {
	Store *taskstate.Store
	// Backend opens the sandbox of the task's record in Load.
	Backend backend.Backend
	Queue   *outbox.Outbox[model.RunnerEvent]
	Log     *slog.Logger
	// OnFatal terminates the runner on a durable store write failure.
	OnFatal func(error)
	// OnRemove deletes the task from the manager's registry.
	OnRemove func(taskID int64)

	// RetryInterval is the first pause between attempts to reach the driver
	// or reattach to a run, RetryMaxInterval caps the reattach backoff, and
	// LostTimeout is how long Run keeps reattaching without a good message
	// from the driver before it gives up on the run. ReadyTimeout is how long
	// a started sandbox's driver has to answer and accept its run.
	RetryInterval    time.Duration
	RetryMaxInterval time.Duration
	LostTimeout      time.Duration
	ReadyTimeout     time.Duration

	TaskID int64
	// Sandbox is the task's sandbox.
	Sandbox backend.Sandbox

	// mu serializes Start, Kill, Destroy and Running for the task.
	mu sync.Mutex
	// rec is the task's record, as last written to taskstate/, or zero until
	// it is first written. Guarded by mu.
	rec taskstate.Record
	// running is set from Start (or claim) until Run returns, so a second run
	// is not started meanwhile. Guarded by mu.
	running bool
	// cancel cancels the context of the Run in progress, or is nil. Guarded by
	// mu.
	cancel context.CancelCauseFunc
	// removed is why the TaskRunner was removed from the registry, or nil
	// while it is registered: ErrRemoved from Destroy, backend.ErrGone from
	// gone. Guarded by mu.
	removed error
}

// ErrRemoved is the cause a Run is cancelled with when Destroy destroys its
// sandbox, and the error an operation on the destroyed TaskRunner returns.
var ErrRemoved = errors.New("sandbox removed")

// ErrRunning is returned by Start when a Run still follows the task's previous
// run, so nothing was started. Running can report false then: the sandbox may
// already be parking after its run (a Lambda VM suspending).
var ErrRunning = errors.New("sandbox is still running")

// errOpen marks a Load error from opening the record's handle, as opposed to
// reading the record.
var errOpen = errors.New("failed to open sandbox handle")

// Load restores tr from the task's record in taskstate/, which a previous
// process wrote: it reads the record and opens its handle as tr's Sandbox. A
// handle that cannot be opened gives an error wrapping errOpen. It is called
// before tr is registered.
func (tr *TaskRunner) Load() error {
	rec, ok, err := tr.Store.Read(tr.TaskID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no record for task %d", tr.TaskID)
	}
	sb, err := tr.Backend.Open(backend.Handle{Type: rec.Type, ID: rec.ID, Data: rec.Data})
	if err != nil {
		return fmt.Errorf("%w: %w", errOpen, err)
	}
	tr.rec = rec
	tr.Sandbox = sb
	return nil
}

// lock locks tr, unless it was removed, in which case it returns why.
func (tr *TaskRunner) lock() error {
	tr.mu.Lock()
	if tr.removed != nil {
		tr.mu.Unlock()
		return tr.removed
	}
	return nil
}

// record returns the task's record of the sandbox's handle at version.
func (tr *TaskRunner) record(version int64) taskstate.Record {
	h := tr.Sandbox.Handle()
	return taskstate.Record{
		TaskID:  tr.TaskID,
		Version: version,
		Type:    h.Type,
		ID:      h.ID,
		Data:    h.Data,
	}
}

// write records rec in taskstate/ and on tr. A store write failure terminates the runner: the taskstate store is
// the runner's other durable local store, and a write failure here is the same
// non-transient disk failure as an outbox Enqueue failure, so crash rather than
// lose the handle. The returned error still unwinds the caller (releasing the
// sem slot) as ctx tears down. The caller holds tr.mu.
func (tr *TaskRunner) write(rec taskstate.Record) error {
	if err := tr.Store.Write(rec); err != nil {
		tr.OnFatal(fmt.Errorf("record handle for task %d: %w", tr.TaskID, err))
		return fmt.Errorf("failed to record task handle: %w", err)
	}
	tr.rec = rec
	return nil
}

// remove deletes tr's record and then tr from the registry, marking tr removed
// with cause. If the record cannot be deleted, tr stays registered. The caller
// holds tr.mu.
func (tr *TaskRunner) remove(cause error) error {
	if err := tr.Store.Remove(tr.TaskID); err != nil {
		return err
	}
	tr.OnRemove(tr.TaskID)
	tr.removed = cause
	return nil
}

// gone handles a vanished sandbox: it no longer exists, so drop the dangling
// record and tr (nothing to reuse or destroy) and return backend.ErrGone. The
// caller's error path emits "failed" and releases the acquired sem slot. A
// subsequent explicit start/restart, having no TaskRunner, is then a
// legitimate first-start-fresh. The caller holds tr.mu.
func (tr *TaskRunner) gone() error {
	if err := tr.remove(backend.ErrGone); err != nil {
		tr.Log.Error("failed to remove dangling record", "task", tr.TaskID, "err", err)
	}
	return backend.ErrGone
}

// dropGone deletes the record of a sandbox Load found gone, marking tr removed.
// If the record cannot be deleted, tr stays registered.
func (tr *TaskRunner) dropGone() error {
	if err := tr.lock(); err != nil {
		return err
	}
	defer tr.mu.Unlock()
	return tr.remove(backend.ErrGone)
}

// Version returns the version the task's record is stamped with.
func (tr *TaskRunner) Version() (int64, error) {
	if err := tr.lock(); err != nil {
		return 0, err
	}
	defer tr.mu.Unlock()
	return tr.rec.Version, nil
}

// Running reports whether the task's sandbox is up.
func (tr *TaskRunner) Running(ctx context.Context) (bool, error) {
	if err := tr.lock(); err != nil {
		return false, err
	}
	defer tr.mu.Unlock()
	st, err := tr.Sandbox.Status(ctx)
	if err != nil {
		return false, err
	}
	return st == backend.StatusUp, nil
}

// idle returns ErrRunning if a Run still follows the task's previous run.
func (tr *TaskRunner) idle() error {
	if err := tr.lock(); err != nil {
		return err
	}
	defer tr.mu.Unlock()
	if tr.running {
		return ErrRunning
	}
	return nil
}

// Start stamps the task's record with version and starts the sandbox, claiming
// tr for the run Run then starts. If the sandbox is gone, Start drops the record
// and tr and returns backend.ErrGone; any other failure to start it keeps them,
// so Destroy can still destroy the sandbox. If a Run still follows the task's
// previous run, Start returns ErrRunning without starting anything.
func (tr *TaskRunner) Start(ctx context.Context, version int64) error {
	if err := tr.lock(); err != nil {
		return err
	}
	defer tr.mu.Unlock()
	if tr.running {
		return ErrRunning
	}
	// A TaskRunner created for this run has no record yet; one already
	// stamped with version is not rewritten.
	if rec := tr.record(version); !reflect.DeepEqual(rec, tr.rec) {
		if err := tr.write(rec); err != nil {
			return err
		}
	}
	if err := tr.Sandbox.Start(ctx); errors.Is(err, backend.ErrGone) {
		return tr.gone()
	} else if err != nil {
		return err
	}
	tr.running = true
	return nil
}

// claim claims tr for a Run reattaching to a run already started, as Start does
// for a new one.
func (tr *TaskRunner) claim() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.running = true
}

// Kill asks the driver of the task's sandbox to stop the recorded run. It
// reports whether a running run was stopped — in that case the driver owns the
// terminal event report (see the driver-owned-events proposal). A sandbox that
// is not up has no driver to ask, so nothing is stopped; neither is a run that
// already finished or is not the recorded version.
func (tr *TaskRunner) Kill(ctx context.Context) (bool, error) {
	if err := tr.lock(); err != nil {
		return false, err
	}
	defer tr.mu.Unlock()
	st, err := tr.Sandbox.Status(ctx)
	if err != nil {
		return false, err
	}
	if st != backend.StatusUp {
		return false, nil
	}
	client, err := tr.Sandbox.Dial(ctx, "")
	if err != nil {
		return false, err
	}
	tr.Log.Info("stopping run", "task", tr.TaskID, "version", tr.rec.Version)
	resp, err := client.Stop(ctx, &driverv1.StopRequest{Version: tr.rec.Version})
	if err != nil {
		return false, fmt.Errorf("failed to stop run: %w", err)
	}
	return resp.GetStopped(), nil
}

// Destroy destroys the task's sandbox and deletes its record and tr. They are
// deleted only after the destroy succeeds, so a failed destroy leaves the task
// tracked and the removal is retried. Destroying a gone sandbox is not an
// error, so a task whose sandbox vanished is simply dropped. A Run still
// following the destroyed sandbox is cancelled, and reports the run lost.
func (tr *TaskRunner) Destroy(ctx context.Context) error {
	if err := tr.lock(); err != nil {
		return err
	}
	defer tr.mu.Unlock()
	return tr.destroy(ctx)
}

// destroy is Destroy with tr.mu held.
func (tr *TaskRunner) destroy(ctx context.Context) error {
	if err := tr.Sandbox.Destroy(ctx); err != nil {
		return err
	}
	if tr.cancel != nil {
		tr.cancel(ErrRemoved)
	}
	return tr.remove(ErrRemoved)
}

// destroyIf destroys the sandbox, as Destroy does, if it is not up and stale
// reports the task gone for good, and reports whether it did. Both a parked
// husk and a gone sandbox count as "no live sandbox". A removed tr is left
// alone.
func (tr *TaskRunner) destroyIf(ctx context.Context, stale func(context.Context) (bool, error)) (bool, error) {
	if tr.lock() != nil {
		return false, nil
	}
	defer tr.mu.Unlock()
	st, err := tr.Sandbox.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get sandbox status: %w", err)
	}
	if st == backend.StatusUp {
		return false, nil
	}
	if ok, err := stale(ctx); err != nil || !ok {
		return false, err
	}
	if err := tr.destroy(ctx); err != nil {
		return false, fmt.Errorf("failed to remove sandbox: %w", err)
	}
	return true, nil
}

// Run follows the run version tr's record is stamped with: it starts the run
// with spec first, or, with spec nil, reattaches to a run already started. It
// returns once the run finished, after parking the sandbox, and reports the
// outcome if the driver did not. A run that could not be started or was lost
// gets the runner's fallback "failed", scoped to its version (a legacy
// record's version 0 bypasses the guard). Cancelled by Destroy, the run is
// lost.
//
// Cancelled by ctx, Run returns its error with no event: the runner is
// shutting down, and the sandbox stays alive for next-boot rehydration. That
// is the only error.
func (tr *TaskRunner) Run(ctx context.Context, spec *driverv1.RunSpec) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	tr.mu.Lock()
	tr.running = true
	tr.cancel = cancel
	if tr.removed != nil {
		// Destroyed between Start and Run.
		cancel(ErrRemoved)
	}
	version := tr.rec.Version
	tr.mu.Unlock()
	defer func() {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		tr.running = false
		tr.cancel = nil
	}()

	lost, err := tr.run(ctx, version, spec)
	if err != nil {
		if !errors.Is(context.Cause(ctx), ErrRemoved) {
			return err // shutdown: leave the sandbox alive for next-boot rehydration
		}
		lost = "sandbox removed before the run finished"
	}
	if lost == "" {
		tr.Log.Info("run finished, driver already reported", "task", tr.TaskID, "version", version)
		return nil
	}
	tr.Log.Error("run lost", "task", tr.TaskID, "version", version, "reason", lost)
	if err := tr.Queue.Enqueue(model.RunnerEvent{
		TaskID:  tr.TaskID,
		Version: version,
		Event:   model.RunnerEventFailed,
		Reason:  lost,
	}); err != nil {
		tr.OnFatal(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, tr.TaskID, err))
	}
	return nil
}

// run starts the run version on the sandbox with spec, unless spec is nil,
// then attaches to it. A run that could not be started is reported lost with
// why, after the sandbox is parked: nothing runs on it. The only error is
// ctx's.
func (tr *TaskRunner) run(ctx context.Context, version int64, spec *driverv1.RunSpec) (lost string, err error) {
	if spec != nil {
		if err := tr.startRun(ctx, version, spec); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			tr.Log.Error("failed to start run", "task", tr.TaskID, "version", version, "err", err)
			tr.park(ctx)
			return err.Error(), nil
		}
	}
	return tr.attach(ctx, version)
}

// startRun waits for the sandbox's driver to answer Status, backing off
// exponentially, then starts run version with spec on it, returning once the
// driver has accepted it. A Run that is rejected because another stream is
// open is retried. The driver has ReadyTimeout to do both.
func (tr *TaskRunner) startRun(ctx context.Context, version int64, spec *driverv1.RunSpec) error {
	ctx, cancel := context.WithTimeout(ctx, tr.ReadyTimeout)
	defer cancel()
	client, err := tr.Sandbox.Dial(ctx, "")
	if err != nil {
		return err
	}
	delay := tr.RetryInterval
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
		err := accept(ctx, client, &driverv1.RunRequest{Version: version, Spec: spec})
		if err == nil {
			return nil
		}
		if connect.CodeOf(err) != connect.CodeUnavailable || sleep(ctx, tr.RetryInterval) != nil {
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

// attach follows run version on the sandbox until its Finished, then parks the
// sandbox. It returns why the driver's report of the run's outcome was lost, or
// "" if the driver reported it. The only error is ctx's.
//
// A dropped stream is reattached while the sandbox is up, backing off
// exponentially. A sandbox that is parked or gone before Finished arrived, or a
// driver with no such run, is a lost run. Reattaching gives up once LostTimeout
// has passed without a good message — an accepted stream — from the driver:
// attach parks the sandbox and reports the run lost. It returns only once the
// park succeeded or the sandbox is gone, so the sandbox is never up with no
// Run following it; while the platform cannot park it, attach keeps
// retrying.
func (tr *TaskRunner) attach(ctx context.Context, version int64) (lost string, err error) {
	taskID, sb := tr.TaskID, tr.Sandbox
	lastGood := time.Now()
	delay := tr.RetryInterval
	var lastErr string
	for {
		finished, accepted, err := attachOnce(ctx, sb, version)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if finished != nil {
			tr.park(ctx)
			if !finished.GetReported() {
				return "driver did not report the run's outcome", nil
			}
			return "", nil
		}
		if isTerminal(err) {
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come.
			tr.Log.Error("driver has no such run", "task", taskID, "version", version, "err", err)
			if connect.CodeOf(err) == connect.CodeNotFound {
				// Nothing runs on the sandbox, which would otherwise stay up.
				// On FailedPrecondition or Aborted a newer run is active on
				// it, which parking would kill.
				tr.park(ctx)
			}
			return "driver has no such run", nil
		}
		if st, stErr := sb.Status(ctx); stErr != nil {
			err = fmt.Errorf("%w (status: %w)", err, stErr)
		} else if st != backend.StatusUp {
			tr.Log.Error("sandbox stopped before its run finished", "task", taskID, "status", st, "version", version, "err", err)
			if st == backend.StatusParked {
				// Let a sandbox that is still stopping (a SUSPENDING VM)
				// settle, so it is not reported up once Run is done.
				tr.park(ctx)
			}
			return "sandbox stopped before the run finished", nil
		}
		if accepted {
			// The stream was live until it dropped: the driver's idle watchdog
			// would have ended it otherwise.
			lastGood = time.Now()
			delay = tr.RetryInterval
			lastErr = ""
		}
		if time.Since(lastGood) >= tr.LostTimeout {
			// Give up on the driver; park the sandbox instead. While that
			// fails, keep retrying the attach too in case the driver is back.
			parkErr := sb.Park(ctx)
			if parkErr == nil || errors.Is(parkErr, backend.ErrGone) {
				tr.Log.Error("run lost: driver unreachable, sandbox parked", "task", taskID, "version", version, "err", err)
				return "driver unreachable", nil
			}
			err = fmt.Errorf("failed to park sandbox of lost run: %w (driver: %w)", parkErr, err)
		}
		if msg := err.Error(); msg != lastErr {
			tr.Log.Warn("run stream dropped, reattaching", "task", taskID, "version", version, "err", err)
			lastErr = msg
		}
		if err := sleep(ctx, delay); err != nil {
			return "", err
		}
		delay = min(delay*2, tr.RetryMaxInterval)
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
func (tr *TaskRunner) park(ctx context.Context) {
	if err := tr.Sandbox.Park(ctx); err != nil && !errors.Is(err, backend.ErrGone) {
		tr.Log.Error("failed to park sandbox after its run", "task", tr.TaskID, "err", err)
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
