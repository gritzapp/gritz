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

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/x/common"
)

// TaskRunner is the runner's state for a task that has a sandbox: the
// sandbox, the task's record in taskstate/, and the run being followed on it.
// It exists exactly as long as the record: it is created around a sandbox
// once that is created, writing its record (or by Load, which has it Restore a
// record a previous process wrote), and it deletes the record when it is
// destroyed or finds its sandbox gone.
//
// Its operations lock it and return removed if it is set: the TaskRunner was
// destroyed, or found its sandbox gone, while the operation waited for its
// lock. They do not look the task up again, so a task created anew after the
// removal is left to the next operation.
type TaskRunner struct {
	env    *taskEnv
	taskID int64
	// sb is the task's sandbox. It is set when the TaskRunner is created, or
	// by Restore before Load registers it, and not changed after.
	sb backend.Sandbox

	// mu serializes Start, Kill, Destroy and Running for the task.
	mu sync.Mutex
	// rec is the task's record, as last written to taskstate/. Guarded by mu.
	rec taskstate.Record
	// running is set from Start (or Restore) until Run returns, so a second run
	// is not started meanwhile. Guarded by mu.
	running bool
	// cancel cancels the context of the Run in progress, or is nil. Guarded by
	// mu.
	cancel context.CancelCauseFunc
	// removed is why the TaskRunner was removed from the registry, or nil
	// while it is registered: errRemoved from Destroy, backend.ErrGone from
	// gone. Guarded by mu.
	removed error
}

// taskEnv is what a TaskRunner needs from its TaskManager. A manager's
// TaskRunners share one.
type taskEnv struct {
	// backend opens the sandbox of a record Restore adopts.
	backend backend.Backend
	store   *taskstate.Store
	log     *slog.Logger
	// events receives the TaskRunners' events; see TaskRunner.send.
	events chan<- TaskEvent
	// forget deletes the task from the manager's registry.
	forget func(taskID int64)

	// retryInterval is the first pause between attempts to reach the driver
	// or reattach to a run, retryMaxInterval caps the reattach backoff, and
	// lostTimeout is how long Run keeps reattaching without a good message
	// from the driver before it gives up on the run. readyTimeout is how long
	// a started sandbox's driver has to answer and accept its run. Tests
	// shorten them.
	retryInterval    time.Duration
	retryMaxInterval time.Duration
	lostTimeout      time.Duration
	readyTimeout     time.Duration
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

// errRemoved is the cause a Run is cancelled with when Destroy destroys its
// sandbox, and the error an operation on the destroyed TaskRunner returns.
var errRemoved = errors.New("sandbox removed")

// errRunning is returned by Start when a Run still follows the task's previous
// run, so nothing was started. Running can report false then: the sandbox may
// already be parking after its run (a Lambda VM suspending).
var errRunning = errors.New("sandbox is still running")

// newTaskRunner returns the TaskRunner of a sandbox just created for the task,
// recording its handle at version in taskstate/.
func newTaskRunner(ctx context.Context, env *taskEnv, sb backend.Sandbox, taskID, version int64) (*TaskRunner, error) {
	tr := &TaskRunner{env: env, taskID: taskID, sb: sb}
	if err := tr.write(ctx, tr.record(version)); err != nil {
		return nil, err
	}
	return tr, nil
}

// recordHandle returns the handle a record tracks.
func recordHandle(rec taskstate.Record) backend.Handle {
	return backend.Handle{Type: rec.Type, ID: rec.ID, Data: rec.Data}
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
	h := tr.sb.Handle()
	return taskstate.Record{
		TaskID:  tr.taskID,
		Version: version,
		Type:    h.Type,
		ID:      h.ID,
		Data:    h.Data,
	}
}

// write records rec in taskstate/ and on tr. A store write failure terminates
// the runner, through a TaskFatalEvent: the taskstate store is the runner's
// other durable local store, and a write failure here is the same
// non-transient disk failure as an outbox Enqueue failure, so crash rather than
// lose the handle. The returned error still unwinds the caller (releasing the
// sem slot) as ctx tears down. The caller holds tr.mu, or is creating tr.
func (tr *TaskRunner) write(ctx context.Context, rec taskstate.Record) error {
	if err := tr.env.store.Write(rec); err != nil {
		tr.send(ctx, TaskFatalEvent{
			TaskID: tr.taskID,
			Err:    fmt.Errorf("record handle for task %d: %w", tr.taskID, err),
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
	case tr.env.events <- ev:
	case <-ctx.Done():
		tr.env.log.Warn("task event dropped at shutdown", "task", tr.taskID, "event", ev)
	}
}

// remove deletes tr's record and then tr from the registry, marking tr removed
// with cause. If the record cannot be deleted, tr stays registered. The caller
// holds tr.mu.
func (tr *TaskRunner) remove(cause error) error {
	if err := tr.env.store.Remove(tr.taskID); err != nil {
		return err
	}
	tr.env.forget(tr.taskID)
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
		tr.env.log.Error("failed to remove dangling record", "task", tr.taskID, "err", err)
	}
	return backend.ErrGone
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
	st, err := tr.sb.Status(ctx)
	if err != nil {
		return false, err
	}
	return st == backend.StatusUp, nil
}

// idle returns errRunning if a Run still follows the task's previous run.
func (tr *TaskRunner) idle() error {
	if err := tr.lock(); err != nil {
		return err
	}
	defer tr.mu.Unlock()
	if tr.running {
		return errRunning
	}
	return nil
}

// Start stamps the task's record with version and starts the sandbox, claiming
// tr for the run Run then starts. If the sandbox is gone, Start drops the record
// and tr and returns backend.ErrGone; any other failure to start it keeps them,
// so Destroy can still destroy the sandbox. If a Run still follows the task's
// previous run, Start returns errRunning without starting anything.
func (tr *TaskRunner) Start(ctx context.Context, version int64) error {
	if err := tr.lock(); err != nil {
		return err
	}
	defer tr.mu.Unlock()
	if tr.running {
		return errRunning
	}
	// A TaskRunner created for this run has its record written already.
	if rec := tr.record(version); !reflect.DeepEqual(rec, tr.rec) {
		if err := tr.write(ctx, rec); err != nil {
			return err
		}
	}
	if err := tr.sb.Start(ctx); errors.Is(err, backend.ErrGone) {
		return tr.gone()
	} else if err != nil {
		return err
	}
	tr.running = true
	return nil
}

// Restore adopts rec, a record a previous process wrote, on a TaskRunner with
// no sandbox yet, and reports the status of the sandbox it opens from rec's
// handle. An up sandbox claims tr for a Run reattaching to the recorded run, in
// the same lock acquisition that installs it, as Start does for a new run.
//
// A handle that cannot be opened, or a sandbox that is gone, leaves tr with no
// sandbox and its record deleted; a failure to delete it is only logged, and
// the next boot drops the record again. A failed status check keeps the
// sandbox and its record, since the sandbox may still be there, and claims
// nothing.
func (tr *TaskRunner) Restore(ctx context.Context, rec taskstate.Record) (backend.Status, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	sb, err := tr.env.backend.Open(recordHandle(rec))
	if err != nil {
		if err := tr.env.store.Remove(rec.TaskID); err != nil {
			tr.env.log.Error("failed to remove unopenable record", "task", rec.TaskID, "err", err)
		}
		return backend.StatusUnknown, fmt.Errorf("failed to open sandbox: %w", err)
	}
	tr.sb, tr.rec = sb, rec
	st, err := sb.Status(ctx)
	if err != nil {
		return backend.StatusUnknown, fmt.Errorf("failed to get sandbox status: %w", err)
	}
	switch st {
	case backend.StatusGone:
		if err := tr.env.store.Remove(rec.TaskID); err != nil {
			tr.env.log.Error("failed to remove dangling record", "task", rec.TaskID, "err", err)
		}
		tr.sb, tr.rec = nil, taskstate.Record{}
	case backend.StatusUp:
		tr.running = true
	}
	return st, nil
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
	st, err := tr.sb.Status(ctx)
	if err != nil {
		return false, err
	}
	if st != backend.StatusUp {
		return false, nil
	}
	client, err := tr.sb.Dial(ctx, "")
	if err != nil {
		return false, err
	}
	tr.env.log.Info("stopping run", "task", tr.taskID, "version", tr.rec.Version)
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
	if err := tr.sb.Destroy(ctx); err != nil {
		return err
	}
	if tr.cancel != nil {
		tr.cancel(errRemoved)
	}
	return tr.remove(errRemoved)
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
	st, err := tr.sb.Status(ctx)
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
// A lost run is sent to the manager as a TaskLostEvent, on ctx rather than on
// the run's context that Destroy cancels, before tr is released for another
// run.
//
// Cancelled by ctx, Run returns its error with no event: the runner is
// shutting down, and the sandbox stays alive for next-boot rehydration. That
// is the only error.
func (tr *TaskRunner) Run(ctx context.Context, spec *driverv1.RunSpec) error {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	tr.mu.Lock()
	tr.running = true
	tr.cancel = cancel
	if tr.removed != nil {
		// Destroyed between Start and Run.
		cancel(errRemoved)
	}
	version := tr.rec.Version
	tr.mu.Unlock()
	defer func() {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		tr.running = false
		tr.cancel = nil
	}()

	lost, err := tr.run(runCtx, version, spec)
	if err != nil {
		if !errors.Is(context.Cause(runCtx), errRemoved) {
			return err // shutdown: leave the sandbox alive for next-boot rehydration
		}
		lost = "sandbox removed before the run finished"
	}
	if lost == "" {
		tr.env.log.Info("run finished, driver already reported", "task", tr.taskID, "version", version)
		return nil
	}
	tr.env.log.Error("run lost", "task", tr.taskID, "version", version, "reason", lost)
	tr.send(ctx, TaskLostEvent{TaskID: tr.taskID, Version: version, Reason: lost})
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
			tr.env.log.Error("failed to start run", "task", tr.taskID, "version", version, "err", err)
			tr.park(ctx)
			return err.Error(), nil
		}
	}
	return tr.attach(ctx, version)
}

// startRun waits for the sandbox's driver to answer Status, backing off
// exponentially, then starts run version with spec on it, returning once the
// driver has accepted it. A Run that is rejected because another stream is
// open is retried. The driver has the runner's readyTimeout to do both.
func (tr *TaskRunner) startRun(ctx context.Context, version int64, spec *driverv1.RunSpec) error {
	env := tr.env
	ctx, cancel := context.WithTimeout(ctx, env.readyTimeout)
	defer cancel()
	client, err := tr.sb.Dial(ctx, "")
	if err != nil {
		return err
	}
	bo := doubling(env.retryInterval, readyMaxInterval)
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
		if connect.CodeOf(err) != connect.CodeUnavailable || !common.SleepContext(ctx, env.retryInterval) {
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
// driver with no such run, is a lost run. Reattaching gives up once lostTimeout
// has passed without a good message — an accepted stream — from the driver:
// attach parks the sandbox and reports the run lost. It returns only once the
// park succeeded or the sandbox is gone, so the sandbox is never up with no
// Run following it; while the platform cannot park it, attach keeps
// retrying.
func (tr *TaskRunner) attach(ctx context.Context, version int64) (lost string, err error) {
	env, taskID, sb := tr.env, tr.taskID, tr.sb
	lastGood := time.Now()
	bo := doubling(env.retryInterval, env.retryMaxInterval)
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
			env.log.Error("driver has no such run", "task", taskID, "version", version, "err", err)
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
			env.log.Error("sandbox stopped before its run finished", "task", taskID, "status", st, "version", version, "err", err)
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
			bo.Reset()
			lastErr = ""
		}
		if time.Since(lastGood) >= env.lostTimeout {
			// Give up on the driver; park the sandbox instead. While that
			// fails, keep retrying the attach too in case the driver is back.
			parkErr := sb.Park(ctx)
			if parkErr == nil || errors.Is(parkErr, backend.ErrGone) {
				env.log.Error("run lost: driver unreachable, sandbox parked", "task", taskID, "version", version, "err", err)
				return "driver unreachable", nil
			}
			err = fmt.Errorf("failed to park sandbox of lost run: %w (driver: %w)", parkErr, err)
		}
		if msg := err.Error(); msg != lastErr {
			env.log.Warn("run stream dropped, reattaching", "task", taskID, "version", version, "err", err)
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

// park parks the run's sandbox once the run is over. Failures are logged: the
// run's outcome is already known.
func (tr *TaskRunner) park(ctx context.Context) {
	if err := tr.sb.Park(ctx); err != nil && !errors.Is(err, backend.ErrGone) {
		tr.env.log.Error("failed to park sandbox after its run", "task", tr.taskID, "err", err)
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
