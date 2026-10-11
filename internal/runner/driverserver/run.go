package driverserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/logship"
	"github.com/gritzapp/gritz/internal/shell"
	"github.com/gritzapp/gritz/internal/x/redact"
)

// The two causes a run's context is cancelled with. Either way the agent and
// setup commands get SIGTERM, then SIGKILL after their wait delay; the cause
// decides what the run reports.
var (
	// ErrStop is the cause Stop ends a run with. The run was asked to end, so
	// it reports stopped.
	ErrStop = errors.New("stop")
	// ErrShutdown is the cause Shutdown ends a run with: the driver is going
	// away (SIGTERM: docker stop, host shutdown, the VM suspended or
	// terminated). Nobody asked for the run to end, so it reports failed, with
	// this as the reason.
	ErrShutdown = errors.New("driver shut down mid-run")
)

// logFlushTimeout bounds a run's end-of-run flush of the shipped log. Flushing
// is best-effort: past the deadline the unshipped tail is given up on and the
// complete log remains in /gritz/log.
const logFlushTimeout = 5 * time.Second

// Run is one execution of a task version: everything it is built from, and
// everything it owns until it finishes. It runs the task's setup and prompt,
// flushes its log, and reports its terminal event itself; the server only
// records the outcome.
type Run struct {
	version int64
	spec    *driverv1.RunSpec

	// ctx is the run's own context. Only Stop and Shutdown cancel it, with
	// ErrStop and ErrShutdown as the cause; no request's context reaches it.
	ctx    context.Context
	cancel context.CancelCauseFunc
	// done is closed once the run has reported and its record is written.
	done chan struct{}
	// reported says whether the terminal event was acknowledged. It is set
	// before done is closed and read only after.
	reported bool

	client  gritzclient.Client
	config  agent.ConfigStore
	env     []string
	log     *agent.DriverLog
	mask    *maskWriter
	shipper *logship.Shipper
}

// newRun builds the run of version from spec, opening its /gritz/log tee and
// pointing the server's shipper at it. The mask is in place before the run
// writes its first byte.
func (s *Server) newRun(version int64, spec *driverv1.RunSpec) *Run {
	ctx, cancel := context.WithCancelCause(context.Background())
	client := s.newClient(spec.GetServerUrl(), spec.GetToken())
	s.shipper.Reset(client, spec.GetTaskId(), version)
	mask := &maskWriter{w: redact.NewWriter(s.shipper, Secrets(spec))}
	return &Run{
		version: version,
		spec:    spec,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		client:  client,
		config:  s.config,
		env:     Env(s.environ, spec),
		log:     agent.OpenDriverLog(s.logPath, mask),
		mask:    mask,
		shipper: s.shipper,
	}
}

// finishedRun returns a run that has already finished.
func finishedRun(version int64, reported bool) *Run {
	r := &Run{version: version, done: make(chan struct{}), reported: reported}
	close(r.done)
	return r
}

// Stop ends the run with ErrStop, so it reports stopped. It reports whether
// the run was still running. It does not wait.
func (r *Run) Stop() bool {
	return r.end(ErrStop)
}

// Shutdown ends the run with ErrShutdown, so it reports failed. It reports
// whether the run was still running. It does not wait.
func (r *Run) Shutdown() bool {
	return r.end(ErrShutdown)
}

// end cancels the run with cause if it is still running. The first cause
// sticks: a Stop after Shutdown changes nothing.
func (r *Run) end(cause error) bool {
	select {
	case <-r.done:
		return false
	default:
	}
	r.cancel(cause)
	return true
}

// finish records the outcome and closes done.
func (r *Run) finish(reported bool) {
	r.reported = reported
	close(r.done)
}

// execute runs the task and reports its outcome to the gritz server. It
// returns an error only when it could not report the outcome, so Finished's
// one bit means "did the driver report?". The shipped log is flushed before
// the terminal event, so the server has the run's log by the time it learns
// the run ended.
func (r *Run) execute() error {
	defer r.cancel(nil)
	defer r.log.Close()
	// Events are submitted on a context detached from the run's cancellation,
	// so the terminal event still goes out after Stop or Shutdown, bounded by
	// the client's HTTP timeout.
	eventCtx := context.WithoutCancel(r.ctx)
	event, err := r.run(eventCtx)
	r.flush()
	if err != nil {
		return err
	}
	return r.submit(eventCtx, event)
}

// run reports started, runs the task and returns the terminal event to
// report. It returns an error when the run cannot be reported at all.
func (r *Run) run(eventCtx context.Context) (model.RunnerEvent, error) {
	// The task is fetched once, before any event: task.Version is stamped on
	// every runner event, so the server's stale guard drops events from a
	// superseded run instead of clobbering the live one. A failed GetTask
	// means there is no working server connection to report through, so the
	// run finishes unreported and the runner's backstop reports it.
	resp, err := r.client.GetTask(r.ctx, &gritzv1.GetTaskRequest{Id: r.spec.GetTaskId()})
	if err != nil {
		return model.RunnerEvent{}, fmt.Errorf("failed to get task: %w", err)
	}
	task := resp.GetTask()
	event := model.RunnerEvent{TaskID: r.spec.GetTaskId(), Version: task.GetVersion()}

	r.log.StartRun(event.Version)

	// An acked started proves the connection, token, server, and DB are all
	// healthy.
	event.Event = model.RunnerEventStarted
	if err := r.submit(eventCtx, event); err != nil {
		return model.RunnerEvent{}, err
	}

	err = r.task(task)
	event.Event = model.RunnerEventStopped
	if err != nil {
		switch context.Cause(r.ctx) {
		case ErrStop:
			r.log.Info("agent stopped gracefully")
			return event, nil
		case ErrShutdown:
			r.log.Error("task interrupted", "err", err)
			err = ErrShutdown
		default:
			r.log.Error("task failed", "err", err)
		}
		// The error distinguishes setup failures from agent failures, so it
		// carries that context into the timeline as the failure reason.
		event.Event = model.RunnerEventFailed
		event.Reason = err.Error()
	}
	return event, nil
}

// task runs one of two mutually exclusive modes: a debug shell when the task
// carries a shell_session, or the agent otherwise (see
// proposals/draft/driver-reverse-shell.md). Both report the same lifecycle
// events.
func (r *Run) task(task *gritzv1.Task) error {
	if session := task.GetShellSession(); session != "" {
		return shell.Serve(r.ctx, shell.ServeOptions{
			ServerURL: r.spec.GetServerUrl(),
			Token:     r.spec.GetToken(),
			Session:   session,
			Log:       &r.log.Logger,
		})
	}
	return agent.Execute(r.ctx, task, agent.ExecuteOptions{
		Client: r.client,
		Log:    r.log,
		Config: r.config,
		Env:    r.env,
	})
}

// flush detaches the run from the shipper and ships what it still holds,
// under logFlushTimeout. It is best-effort and never affects the run's
// outcome: on deadline the unshipped tail is given up on, and the complete log
// is still in /gritz/log.
func (r *Run) flush() {
	ctx, cancel := context.WithTimeout(context.Background(), logFlushTimeout)
	defer cancel()
	r.mask.Close()
	if err := r.shipper.Flush(ctx); err != nil {
		// Not through r.log: it is teed into the shipper being flushed. os.Stderr
		// is outside the tee.
		fmt.Fprintf(os.Stderr, "gritz: %v\n", err)
	}
}

// submit reports a runner event and waits for the ack. The SubmitRunnerEvents
// handler commits before returning, so a nil error means the state transition
// is durable.
func (r *Run) submit(ctx context.Context, event model.RunnerEvent) error {
	_, err := r.client.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{event.Proto()},
	})
	if err != nil {
		return fmt.Errorf("failed to submit %s event: %w", event.Event, err)
	}
	return nil
}

// maskWriter is a run's secret mask in front of the server's shipper. The
// log's tees write from several goroutines, so unlike redact.Writer it is safe
// for concurrent use. Like the shipper, it reports every write as complete.
type maskWriter struct {
	mu sync.Mutex
	// w is nil once closed.
	w *redact.Writer
}

// Write masks p into the shipper, or drops it once the mask is closed.
func (m *maskWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w != nil {
		// The shipper never fails a write, so neither does the mask.
		_, _ = m.w.Write(p)
	}
	return len(p), nil
}

// Close passes on the bytes the mask is holding back as a possible secret
// prefix, and detaches it from the shipper, so nothing the run writes after
// its flush lands in the next run's log. A value split across the close goes
// out unmasked (see redact.Writer.Flush); a run closes its mask only once its
// output has ended, so that tail is the log's, not a secret's.
func (m *maskWriter) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w == nil {
		return nil
	}
	err := m.w.Flush()
	m.w = nil
	return err
}
