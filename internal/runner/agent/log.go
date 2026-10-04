package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"time"

	"github.com/gritzapp/gritz/internal/runner/logship"
)

// DefaultLogPath is the in-sandbox location of the driver's append-only log
// file. Like DefaultConfigStore, it is a fixed convention shared across the
// runner/driver boundary: the runner pre-creates its parent directory (0777)
// and the driver tees all of its output into it so a completed run can be
// inspected post-mortem via the reverse-shell. It lives under /gritz (the
// container's writable layer, preserved across adopted runs) rather than
// /tmp, which may be a tmpfs or cleared by a setup step.
const DefaultLogPath = "/gritz/log"

// logFlushTimeout bounds the shipper drain Close performs as a backstop for
// early-error exits. Driver.Run applies the same deadline to its own end-of-run
// flush. Flushing is best-effort either way: past the deadline the unshipped
// tail is given up on and the complete log remains in the DefaultLogPath file.
const logFlushTimeout = 5 * time.Second

// nopWriteCloser adds a no-op Close to an io.Writer so the caller can always
// defer Close regardless of whether the real file opened.
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

// OpenLogSink opens the append-only log file at logPath, creating its parent
// directory as a fallback (the runner normally pre-creates it so a non-root
// driver can write there). The file is opened O_CREATE|O_WRONLY|O_APPEND, so
// an existing file is appended to, never truncated.
//
// Opening is best-effort: on any filesystem failure it returns an
// io.Discard-backed no-op WriteCloser alongside the error, so the caller can
// log the failure but the sink is always usable and a run never fails because
// logging could not be set up. The returned WriteCloser must be closed.
func OpenLogSink(logPath string) (io.WriteCloser, error) {
	// The runner pre-creates the dir 0777; this MkdirAll is only a fallback for
	// a directly-invoked driver outside the runner. Its error is not fatal —
	// OpenFile below is the real check and degrades gracefully.
	_ = os.MkdirAll(path.Dir(logPath), 0o777)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return nopWriteCloser{io.Discard}, err
	}
	return f, nil
}

// DriverLog bundles the driver's structured logger with the raw byte sink they
// both feed, so the two travel together as a single value instead of as loose
// fields. The embedded slog.Logger writes to os.Stderr and the sink; Sink is
// the append-only /gritz/log file the driver tees setup command and Claude CLI
// stdio into, mirrored to the server when a shipper is wired in. Close flushes
// the shipper and releases the underlying log file.
//
// os.Stderr stays in the tee, so docker logs output is unchanged.
type DriverLog struct {
	slog.Logger
	sink   io.Writer
	closer io.Closer
	// shipper mirrors the sink to the server, nil when logs are not shipped
	// (DiscardDriverLog, directly-invoked drivers). It is held here rather than
	// on the Driver because the sink is where the bytes are, and because
	// StartRun already carries the run version the chunks are stamped with.
	shipper *logship.Shipper
}

// DiscardDriverLog is a DriverLog that discards everything. Tests and
// directly-invoked drivers use it as the required no-op Log.
var DiscardDriverLog = &DriverLog{
	Logger: *slog.New(slog.DiscardHandler),
	sink:   io.Discard,
}

// OpenDriverLog opens the append-only sandbox log at logPath and returns a
// DriverLog whose logger tees to os.Stderr and the log file, and whose Sink is
// the raw log file used for the driver's stdio tees.
//
// A non-nil shipper is spliced into the sink, so every existing tee — the slog
// handler, Sink(), Stdout(), Stderr(), StartRun — mirrors to the server without
// any of them knowing about it. A nil shipper (tests, directly-invoked drivers)
// leaves the log file as the only consumer.
//
// Declared secrets are masked by the shipper itself, so only the shipped copy
// is redacted: the log file and os.Stderr stay raw, since they are in-sandbox
// surfaces whose audience already holds those values, and full fidelity is what
// makes them useful post-mortem.
//
// Opening is best-effort: on failure the sink degrades to a no-op, the logger
// still writes to os.Stderr, and the failure is logged through that logger — a
// run never fails because logging could not be set up. The returned DriverLog
// must be closed.
func OpenDriverLog(logPath string, shipper *logship.Shipper) *DriverLog {
	file, err := OpenLogSink(logPath)
	var sink io.Writer = file
	if shipper != nil {
		sink = io.MultiWriter(file, shipper)
	}
	logger := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, sink), nil))
	if err != nil {
		logger.Warn("failed to open driver log sink, continuing without it",
			"path", logPath, "err", err)
	}
	return &DriverLog{Logger: *logger, sink: sink, closer: file, shipper: shipper}
}

// Sink returns the raw byte sink to tee stdio into, defaulting to io.Discard so
// the tees degrade to plain os.Stdout/os.Stderr behavior when unset.
func (l *DriverLog) Sink() io.Writer {
	if l.sink == nil {
		return io.Discard
	}
	return l.sink
}

// Stdout returns a writer that tees a subprocess's stdout to os.Stdout and the
// sink. os.Stdout stays wired, so docker logs output is unchanged.
func (l *DriverLog) Stdout() io.Writer {
	return io.MultiWriter(os.Stdout, l.Sink())
}

// Stderr returns a writer that tees a subprocess's stderr to os.Stderr and the
// sink. os.Stderr stays wired, so docker logs output is unchanged.
func (l *DriverLog) Stderr() io.Writer {
	return io.MultiWriter(os.Stderr, l.Sink())
}

// StartRun writes the per-run delimiter to os.Stderr and the sink before the
// run's first event, so an operator can find run boundaries in the single
// append-only log (runs are not split into separate files). It also stamps the
// version on shipped chunks, cutting one at the boundary so the pre-run
// preamble (buffered at version 0) and the run's own bytes never share a chunk.
// The stamp is applied first, so the delimiter itself belongs to the run it
// opens.
func (l *DriverLog) StartRun(version int64) {
	if l.shipper != nil {
		l.shipper.SetVersion(version)
	}
	fmt.Fprintf(l.Stderr(), "==== run version=%d pid=%d ====\n", version, os.Getpid())
}

// Flush ships whatever the shipper still has buffered, bounded by ctx. It
// returns an error only when ctx expires with bytes unsent; the caller is
// expected to log it and carry on, since the complete log is still in the log
// file. Without a shipper it is a no-op.
func (l *DriverLog) Flush(ctx context.Context) error {
	if l.shipper == nil {
		return nil
	}
	return l.shipper.Flush(ctx)
}

// Close flushes the shipper and releases the underlying log file, if any.
//
// The flush is a backstop for exits that return before Driver.Run's own
// end-of-run flush — a failed GetTask, say. It uses its own deadline rather
// than the run's context, which by Close time is typically already cancelled.
// A flush failure does not fail Close: the bytes are still in the log file.
func (l *DriverLog) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), logFlushTimeout)
	defer cancel()
	if err := l.Flush(ctx); err != nil {
		// Not through l.Logger: its handler writes into the sink this shipper
		// is teed into, so reporting a failed flush there would buffer a line
		// that can no longer be shipped. os.Stderr is outside the tee.
		fmt.Fprintf(os.Stderr, "gritz: %v\n", err)
	}
	if l.closer == nil {
		return nil
	}
	return l.closer.Close()
}
