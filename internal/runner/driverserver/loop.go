package driverserver

import (
	"context"
	"log/slog"
	"sync"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runrecord"
)

// loop is the driver loop, between the event file and the Executor: it acts
// on the latest command event the file holds, against the run record. It
// lives as long as the driver process, across Run streams; a stream only
// feeds it events and waits for it to go idle.
//
// The latest command wins: earlier ones are superseded, not queued.
//
//   - start v, v newer than the record: start run v once no run is in
//     progress.
//   - restart v, v newer than the record: stop the run in progress, if any,
//     then start run v once it has finished.
//   - stop: stop the run in progress, if any.
//   - anything else, and a start or restart the record has reached: nothing.
//
// The run record is the "already started" marker: it is written before a run
// starts, so a resent stream, a stale start and a fresh file holding a task's
// whole log all do nothing more than the latest command asks for.
type loop struct {
	exec *Executor
	log  *slog.Logger

	mu sync.Mutex
	// cmd is the latest command event in the event file, or nil if it has
	// none.
	cmd *gritzv1.CommandPayload
	// spec is what runs are started with: the latest a Run stream brought,
	// or nil before the first.
	spec *driverv1.RunSpec
	// closed is set by shutdown; no run starts after it.
	closed bool
	// changed is closed and replaced whenever the loop may have become idle.
	changed chan struct{}
}

func newLoop(exec *Executor, log *slog.Logger, events []*gritzv1.Event) *loop {
	l := &loop{exec: exec, log: log, changed: make(chan struct{})}
	l.observeLocked(events)
	return l
}

// open sets the spec runs are started with, unless spec is nil, and acts on
// the latest command. A Run stream calls it when it opens.
func (l *loop) open(spec *driverv1.RunSpec) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if spec != nil {
		l.spec = spec
	}
	l.actLocked()
}

// store acts on events, which the event file has just stored.
func (l *loop) store(events []*gritzv1.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.observeLocked(events)
	l.actLocked()
}

// observeLocked takes the latest command among events, if any. l.mu is held.
func (l *loop) observeLocked(events []*gritzv1.Event) {
	for _, ev := range events {
		if cmd := ev.GetCommand(); cmd != nil {
			l.cmd = cmd
		}
	}
}

// actLocked does what the latest command asks for, given the run record.
// l.mu is held.
func (l *loop) actLocked() {
	if l.closed || l.cmd == nil {
		return
	}
	rec, hasRec := l.exec.State()
	running := hasRec && rec.State == runrecord.Running
	version := l.cmd.GetVersion()
	newer := !hasRec || version > rec.Version
	switch l.cmd.GetCommand() {
	case gritzv1.TaskCommand_STOP:
		if !running {
			return
		}
		// A stop carries the task's version, which only moves ahead of the
		// run in progress through a restart: a stop newer than the run cancels
		// the restart as well as the run.
		if version < rec.Version {
			panic("driverserver: a stop is older than the run in progress; the server only stops the current version")
		}
		l.exec.Stop()
	case gritzv1.TaskCommand_RESTART:
		switch {
		case !newer:
		case running:
			l.exec.Stop()
		default:
			l.startLocked(version)
		}
	case gritzv1.TaskCommand_START:
		if newer && !running {
			l.startLocked(version)
		}
	}
}

// startLocked starts run version with the latest spec, and acts again once it
// has finished. l.mu is held, and no run is in progress.
func (l *loop) startLocked(version int64) {
	if l.spec == nil {
		// A driver that restarted, on a stream that brought no spec: the
		// stream goes idle, and the runner opens one with a spec.
		l.log.Warn("no spec to start run with", "version", version)
		return
	}
	if err := l.exec.Start(version, l.spec); err != nil {
		// Nothing started: the command is still pending, and the next stream
		// or batch tries again.
		l.log.Error("failed to start run", "version", version, "err", err)
		return
	}
	l.log.Info("run started", "version", version)
	done := l.exec.Done()
	go func() {
		<-done
		l.mu.Lock()
		defer l.mu.Unlock()
		l.actLocked()
		l.changedLocked()
	}()
	l.changedLocked()
}

// changedLocked wakes the streams waiting for the loop to go idle. l.mu is
// held.
func (l *loop) changedLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

// idle returns the run record, whether the loop is idle, and a channel closed
// once that may have changed. The loop is idle when no run is in progress and
// no newer start or restart is pending, or none can start: the driver is
// shutting down, or has no spec.
func (l *loop) idle() (rec runrecord.Record, idle bool, changed <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, hasRec := l.exec.State()
	if hasRec && rec.State == runrecord.Running {
		return rec, false, l.changed
	}
	pending := false
	switch l.cmd.GetCommand() {
	case gritzv1.TaskCommand_START, gritzv1.TaskCommand_RESTART:
		pending = !hasRec || l.cmd.GetVersion() > rec.Version
	}
	return rec, !pending || l.closed || l.spec == nil, l.changed
}

// closing reports whether shutdown has been called.
func (l *loop) closing() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// shutdown stops the run in progress, if any, and waits for it to finish and
// report, or for ctx to expire. No run starts after shutdown is called.
func (l *loop) shutdown(ctx context.Context) error {
	l.mu.Lock()
	l.closed = true
	l.changedLocked()
	l.mu.Unlock()
	return l.exec.Shutdown(ctx)
}
