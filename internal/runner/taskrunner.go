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

// TaskRunner is the runner's state for a task: its sandbox, the task's record
// in taskstate/, and the run being followed on it. It is the runner's object
// for the task, not for one sandbox: it may have no sandbox yet, and it
// outlives the sandbox it has. Start creates a sandbox and writes its record,
// and Restore adopts the record a previous process wrote; Destroy and Retire
// destroy the sandbox and delete its record, and Start and Restore delete the
// record of a sandbox they find gone. The TaskRunner stays usable throughout,
// and the next Start creates a fresh sandbox.
type TaskRunner struct {
	env    *taskEnv
	taskID int64
	// mu serializes the TaskRunner's methods. Run holds it only to capture the
	// sandbox it follows and to release its claim.
	mu sync.Mutex
	// sb is the task's sandbox, or nil while it has none. It is set by Start,
	// which creates it, or by Restore, and cleared by clear. Guarded by mu.
	sb backend.Sandbox
	// rec is sb's record, as last written to taskstate/; zero while sb is nil.
	// Guarded by mu.
	rec taskstate.Record
	// running is set from Start (or Restore) until Run returns, so a second run
	// is not started meanwhile. Guarded by mu.
	running bool
	// cancel cancels the context of the Run in progress, or is nil. Guarded by
	// mu.
	cancel context.CancelCauseFunc
}

// taskEnv is what a TaskRunner needs from its TaskManager. A manager's
// TaskRunners share one.
type taskEnv struct {
	// backend creates the sandbox Start needs, and opens the sandbox of a
	// record Restore adopts.
	backend backend.Backend
	store   *taskstate.Store
	log     *slog.Logger
	// events receives the TaskRunners' events; see TaskRunner.send.
	events chan<- TaskEvent

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
// sandbox.
var errRemoved = errors.New("sandbox removed")

// errRunning is returned by Start when a Run still follows the task's previous
// run, so nothing was started. Running can report false then: the sandbox may
// already be parking after its run (a Lambda VM suspending).
var errRunning = errors.New("sandbox is still running")

// recordHandle returns the handle a record tracks.
func recordHandle(rec taskstate.Record) backend.Handle {
	return backend.Handle{Type: rec.Type, ID: rec.ID, Data: rec.Data}
}

// record returns the task's record of sb's handle at version.
func (tr *TaskRunner) record(sb backend.Sandbox, version int64) taskstate.Record {
	h := sb.Handle()
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
// sem slot) as ctx tears down. The caller holds tr.mu.
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

// clear deletes the task's record and leaves tr with no sandbox, which it
// always does: a record that cannot be deleted is only logged, since it tracks
// a sandbox that is gone or destroyed. The next Start overwrites it, and if
// there is none, the next boot's Load finds the sandbox gone and drops it. The
// caller holds tr.mu.
func (tr *TaskRunner) clear() {
	if err := tr.env.store.Remove(tr.taskID); err != nil {
		tr.env.log.Error("failed to remove record", "task", tr.taskID, "err", err)
	}
	tr.sb, tr.rec = nil, taskstate.Record{}
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
	if tr.sb == nil {
		return false, nil
	}
	st, err := tr.sb.Status(ctx)
	if err != nil {
		return false, err
	}
	return st == backend.StatusUp, nil
}

// Start brings up the task's sandbox for run spec.Version, claiming tr for the
// run Run then starts. With no sandbox, Start creates one from spec and records
// its handle; with one, it stamps the record with spec.Version and reuses it,
// since a task is bound 1:1 to its sandbox. Then it starts the sandbox.
//
// If the sandbox is gone, Start deletes its record, leaving tr with no
// sandbox, and returns backend.ErrGone: the caller reports the run failed, and
// the next Start creates a fresh sandbox. Any other failure to start it keeps
// the sandbox and its record, so Destroy can still destroy it. If a Run still
// follows the task's previous run, Start returns errRunning without doing
// anything.
func (tr *TaskRunner) Start(ctx context.Context, spec backend.Spec) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.running {
		return errRunning
	}
	if tr.sb == nil {
		if err := tr.create(ctx, &spec); err != nil {
			return err
		}
	} else if rec := tr.record(tr.sb, spec.Version); !reflect.DeepEqual(rec, tr.rec) {
		if err := tr.write(ctx, rec); err != nil {
			return err
		}
	}
	if err := tr.sb.Start(ctx); errors.Is(err, backend.ErrGone) {
		tr.clear()
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
	sb, err := tr.env.backend.Create(ctx, spec)
	if err != nil {
		return err
	}
	if err := tr.write(ctx, tr.record(sb, spec.Version)); err != nil {
		if err := sb.Destroy(context.WithoutCancel(ctx)); err != nil {
			tr.env.log.Error("failed to destroy unrecorded sandbox", "task", tr.taskID, "err", err)
		}
		return err
	}
	tr.sb = sb
	return nil
}

// Restore adopts rec, a record a previous process wrote, on a TaskRunner with
// no sandbox yet, and reports the status of the sandbox it opens from rec's
// handle. An up sandbox claims tr for a Run reattaching to the recorded run, in
// the same lock acquisition that installs it, as Start does for a new run.
//
// A handle that cannot be opened, or a sandbox that is gone, leaves tr with no
// sandbox and its record deleted; see clear. A failed status check keeps the
// sandbox and its record, since the sandbox may still be there, and claims
// nothing.
func (tr *TaskRunner) Restore(ctx context.Context, rec taskstate.Record) (backend.Status, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	sb, err := tr.env.backend.Open(recordHandle(rec))
	if err != nil {
		tr.clear()
		return backend.StatusUnknown, fmt.Errorf("failed to open sandbox: %w", err)
	}
	tr.sb, tr.rec = sb, rec
	st, err := sb.Status(ctx)
	if err != nil {
		return backend.StatusUnknown, fmt.Errorf("failed to get sandbox status: %w", err)
	}
	switch st {
	case backend.StatusGone:
		tr.clear()
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
	if tr.sb == nil {
		return false, nil
	}
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

// Destroy destroys the task's sandbox and deletes its record, leaving tr with
// no sandbox. The record is deleted only after the destroy succeeds, so a
// failed destroy leaves the sandbox tracked and the removal is retried.
// Destroying a gone sandbox is not an error, so a sandbox that vanished is
// simply dropped. A Run still following the destroyed sandbox is cancelled,
// and reports the run lost. A task with no sandbox has nothing to destroy.
func (tr *TaskRunner) Destroy(ctx context.Context) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.sb == nil {
		return nil
	}
	if err := tr.sb.Destroy(ctx); err != nil {
		return err
	}
	if tr.cancel != nil {
		tr.cancel(errRemoved)
	}
	tr.clear()
	return nil
}

// Retire destroys the task's sandbox, as Destroy does, if tr is idle: no run
// is claimed, and the sandbox is not up. Both a parked husk and a gone sandbox
// count as "no live sandbox". It reports whether tr is now idle with no
// sandbox, which it also is if it had none.
func (tr *TaskRunner) Retire(ctx context.Context) (bool, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.running {
		return false, nil
	}
	if tr.sb == nil {
		return true, nil
	}
	st, err := tr.sb.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get sandbox status: %w", err)
	}
	if st == backend.StatusUp {
		return false, nil
	}
	if err := tr.sb.Destroy(ctx); err != nil {
		return false, fmt.Errorf("failed to remove sandbox: %w", err)
	}
	tr.clear()
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
	sb, version := tr.sb, tr.rec.Version
	if sb == nil {
		// Destroyed between Start and Run.
		cancel(errRemoved)
	}
	tr.mu.Unlock()
	defer func() {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		tr.running = false
		tr.cancel = nil
	}()

	lost, err := tr.run(runCtx, sb, version, spec)
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

// run starts the run version on sb with spec, unless spec is nil, then
// attaches to it. A run that could not be started is reported lost with why,
// after sb is parked: nothing runs on it. The only error is ctx's, which is
// all a nil sb, destroyed before Run began, gets.
func (tr *TaskRunner) run(ctx context.Context, sb backend.Sandbox, version int64, spec *driverv1.RunSpec) (lost string, err error) {
	if sb == nil {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if spec != nil {
		if err := tr.startRun(ctx, sb, version, spec); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			tr.env.log.Error("failed to start run", "task", tr.taskID, "version", version, "err", err)
			tr.park(ctx, sb)
			return err.Error(), nil
		}
	}
	return tr.attach(ctx, sb, version)
}

// startRun waits for sb's driver to answer Status, backing off
// exponentially, then starts run version with spec on it, returning once the
// driver has accepted it. A Run that is rejected because another stream is
// open is retried. The driver has the runner's readyTimeout to do both.
func (tr *TaskRunner) startRun(ctx context.Context, sb backend.Sandbox, version int64, spec *driverv1.RunSpec) error {
	env := tr.env
	ctx, cancel := context.WithTimeout(ctx, env.readyTimeout)
	defer cancel()
	client, err := sb.Dial(ctx, "")
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

// attach follows run version on sb until its Finished, then parks sb. It returns why the driver's report of the run's outcome was lost, or
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
func (tr *TaskRunner) attach(ctx context.Context, sb backend.Sandbox, version int64) (lost string, err error) {
	env, taskID := tr.env, tr.taskID
	lastGood := time.Now()
	bo := doubling(env.retryInterval, env.retryMaxInterval)
	var lastErr string
	for {
		finished, accepted, err := attachOnce(ctx, sb, version)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if finished != nil {
			tr.park(ctx, sb)
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
				tr.park(ctx, sb)
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
				tr.park(ctx, sb)
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

// park parks the run's sandbox sb once the run is over. Failures are logged:
// the run's outcome is already known.
func (tr *TaskRunner) park(ctx context.Context, sb backend.Sandbox) {
	if err := sb.Park(ctx); err != nil && !errors.Is(err, backend.ErrGone) {
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
