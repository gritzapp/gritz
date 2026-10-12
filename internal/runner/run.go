package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
)

// taskRun is one run of a task, followed by its supervise goroutine. Its
// fields are set by supervise and not changed after: the goroutine runs
// without e.mu, so it uses its own sb, never the entry's.
type taskRun struct {
	r       *Runner
	taskID  int64
	version int64
	// spec starts the run, or is nil when reattaching to a run already
	// started.
	spec *driverv1.RunSpec
	// sb is the sandbox the run is on.
	sb     backend.Sandbox
	cancel context.CancelCauseFunc
}

// follow starts the run if spec is set, attaches to it until it finishes, then
// releases the concurrency slot and reports the outcome if the driver did not.
// Cancelled by the runner's shutdown, it returns with the slot held and no
// event: the sandbox stays alive for next-boot rehydration. Cancelled by
// Remove, the run is lost. A run that could not be started or was lost gets
// the runner's fallback "failed", scoped to its version (a legacy record's
// version 0 bypasses the guard).
func (t *taskRun) follow(ctx context.Context) {
	r, taskID, version := t.r, t.taskID, t.version
	lost, err := t.run(ctx)
	if err != nil {
		if !errors.Is(context.Cause(ctx), errRemoved) {
			return // shutdown: leave the sandbox alive for next-boot rehydration
		}
		lost = "sandbox removed before the run finished"
	}
	r.sem.Release(1)
	r.wake.Wake()
	if lost == "" {
		r.log.Info("run finished, driver already reported", "task", taskID, "version", version)
		return
	}
	r.log.Error("run lost", "task", taskID, "version", version, "reason", lost)
	if err := r.queue.Enqueue(model.RunnerEvent{
		TaskID:  taskID,
		Version: version,
		Event:   model.RunnerEventFailed,
		Reason:  lost,
	}); err != nil {
		r.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventFailed, taskID, err))
		return
	}
}

// run starts the run on its sandbox with its spec, unless spec is nil, then
// attaches to it. A run that could not be started is reported lost with why,
// after its sandbox is parked: nothing runs on it. The only error is ctx's.
func (t *taskRun) run(ctx context.Context) (lost string, err error) {
	if t.spec != nil {
		if err := t.startRun(ctx); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			t.r.log.Error("failed to start run", "task", t.taskID, "version", t.version, "err", err)
			t.park(ctx)
			return err.Error(), nil
		}
	}
	return t.attach(ctx)
}

// startRun waits for the sandbox's driver to answer Status, backing off
// exponentially, then starts the run with its spec on it, returning once the
// driver has accepted it. A Run that is rejected because another stream is
// open is retried. The driver has the runner's readyTimeout to do both.
func (t *taskRun) startRun(ctx context.Context) error {
	r, version := t.r, t.version
	ctx, cancel := context.WithTimeout(ctx, r.readyTimeout)
	defer cancel()
	client, err := t.sb.Dial(ctx, "")
	if err != nil {
		return err
	}
	delay := r.retryInterval
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
		err := accept(ctx, client, &driverv1.RunRequest{Version: version, Spec: t.spec})
		if err == nil {
			return nil
		}
		if connect.CodeOf(err) != connect.CodeUnavailable || sleep(ctx, r.retryInterval) != nil {
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

// attach follows the run on its sandbox until its Finished, then parks the
// sandbox. It returns why the driver's report of the run's outcome was lost, or
// "" if the driver reported it. The only error is ctx's.
//
// A dropped stream is reattached while the sandbox is up, backing off
// exponentially. A sandbox that is parked or gone before Finished arrived, or a
// driver with no such run, is a lost run. Reattaching gives up once lostTimeout
// has passed without a good message — an accepted stream — from the driver:
// attach parks the sandbox and reports the run lost. It returns only once the
// park succeeded or the sandbox is gone, so the sandbox is never up with no
// supervise following it; while the platform cannot park it, attach keeps
// retrying.
func (t *taskRun) attach(ctx context.Context) (lost string, err error) {
	r, taskID, version, sb := t.r, t.taskID, t.version, t.sb
	lastGood := time.Now()
	delay := r.retryInterval
	var lastErr string
	for {
		finished, accepted, err := attachOnce(ctx, sb, version)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if finished != nil {
			t.park(ctx)
			if !finished.GetReported() {
				return "driver did not report the run's outcome", nil
			}
			return "", nil
		}
		if isTerminal(err) {
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come.
			r.log.Error("driver has no such run", "task", taskID, "version", version, "err", err)
			if connect.CodeOf(err) == connect.CodeNotFound {
				// Nothing runs on the sandbox, which would otherwise stay up.
				// On FailedPrecondition or Aborted a newer run is active on
				// it, which parking would kill.
				t.park(ctx)
			}
			return "driver has no such run", nil
		}
		if st, stErr := sb.Status(ctx); stErr != nil {
			err = fmt.Errorf("%w (status: %w)", err, stErr)
		} else if st != backend.StatusUp {
			r.log.Error("sandbox stopped before its run finished", "task", taskID, "status", st, "version", version, "err", err)
			if st == backend.StatusParked {
				// Let a sandbox that is still stopping (a SUSPENDING VM)
				// settle, so it is not reported up once supervise is done.
				t.park(ctx)
			}
			return "sandbox stopped before the run finished", nil
		}
		if accepted {
			// The stream was live until it dropped: the driver's idle watchdog
			// would have ended it otherwise.
			lastGood = time.Now()
			delay = r.retryInterval
			lastErr = ""
		}
		if time.Since(lastGood) >= r.lostTimeout {
			// Give up on the driver; park the sandbox instead. While that
			// fails, keep retrying the attach too in case the driver is back.
			parkErr := sb.Park(ctx)
			if parkErr == nil || errors.Is(parkErr, backend.ErrGone) {
				r.log.Error("run lost: driver unreachable, sandbox parked", "task", taskID, "version", version, "err", err)
				return "driver unreachable", nil
			}
			err = fmt.Errorf("failed to park sandbox of lost run: %w (driver: %w)", parkErr, err)
		}
		if msg := err.Error(); msg != lastErr {
			r.log.Warn("run stream dropped, reattaching", "task", taskID, "version", version, "err", err)
			lastErr = msg
		}
		if err := sleep(ctx, delay); err != nil {
			return "", err
		}
		delay = min(delay*2, r.retryMaxInterval)
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
func (t *taskRun) park(ctx context.Context) {
	if err := t.sb.Park(ctx); err != nil && !errors.Is(err, backend.ErrGone) {
		t.r.log.Error("failed to park sandbox after its run", "task", t.taskID, "err", err)
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
