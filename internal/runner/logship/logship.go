// Package logship mirrors a driver's log bytes to the server as asynchronous
// chunks. Its Shipper is an io.Writer spliced into the driver's log tee, so
// buffering, chunk cutting and retry all happen behind a Write that never
// blocks and never fails. Secret masking is the caller's: it sits in front of
// the shipper, since the secrets belong to a run and the shipper outlives it.
// It depends only on the gritz client and the proto types, not on
// internal/agent.
package logship

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"golang.org/x/sync/semaphore"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/common"
	"github.com/gritzapp/gritz/internal/x/wakeup"
)

// Shipper defaults, applied by New to the zero-valued fields of Options.
const (
	// DefaultChunkSize is the buffered-byte threshold that cuts a chunk. It
	// matches shell.Serve's PTY pump: big enough that per-request overhead is
	// negligible, small enough to keep a chatty run's chunks flowing.
	DefaultChunkSize = 32 << 10 // 32 KiB
	// DefaultFlushInterval is how often the sender is allowed to ship a
	// sub-chunk remainder, so a quiet run still ships promptly instead of
	// sitting in the buffer until the next 32 KiB.
	DefaultFlushInterval = 2 * time.Second
	// DefaultMaxPendingBytes caps the unsent bytes held in the buffer. Past it,
	// writes are dropped rather than allowed to grow without bound while the
	// server is unreachable.
	DefaultMaxPendingBytes = 1 << 20 // 1 MiB
)

// Shipper is an io.Writer that mirrors log bytes to the server as asynchronous
// chunks, one per AppendLogChunk request. Write never blocks and never returns
// an error; a full buffer drops bytes rather than stall the run.
//
// It borrows the shape of the runner's outbox.Outbox — in-order delivery,
// head-of-line retry with backoff, permanent-vs-transient classification —
// without its on-disk durability: log bytes are diagnostics whose durable copy
// is already in /gritz/log, so persisting them a second time to ship them is
// not worth the write amplification. Losing an unshipped tail on a crash is
// accepted; losing a lifecycle event is not, which is why the runner's outbox
// stays durable and this does not.
//
// Shipping is best-effort end to end: a slow or unreachable server never blocks
// a write, never fails a run, and at worst loses log bytes.
type Shipper struct {
	// Tunables, taken from Options and defaulted by New. They are not mutated
	// once the sender is running.
	chunkSize     int
	flushInterval time.Duration
	maxPending    int
	backoff       backoff.BackOff
	log           *slog.Logger

	// notify wakes the sender once a chunk's worth of bytes is buffered.
	notify wakeup.Chan
	// sendSem admits one sender at a time. Run and Flush both drain, so the
	// semaphore — not the single Run goroutine — is what guarantees never more
	// than one request in flight, and therefore that the server's insertion
	// order is write order.
	sendSem *semaphore.Weighted
	// inflight is the chunk the sender is delivering, nil when there is none.
	// It is held across retries so a transient failure resends the same bytes
	// rather than being overtaken by newer ones. It lives here rather than in a
	// local so a Flush that follows a cancelled Run picks it up instead of
	// losing it. Guarded by sendSem, not mu.
	inflight *chunk
	// failing suppresses a warning per retry, logging one per failure streak
	// instead. Guarded by sendSem, not mu.
	failing bool

	mu sync.Mutex
	// client, taskID and version are the run the bytes belong to, set by New
	// and replaced by Reset. A nil client ships nothing.
	client  gritzclient.Client
	taskID  int64
	version int64
	buf     bytes.Buffer
}

// chunk is a cut request together with the client that delivers it, so a
// chunk cut before a Reset still goes to the run it was cut for.
type chunk struct {
	client gritzclient.Client
	req    *gritzv1.AppendLogChunkRequest
}

var _ io.Writer = (*Shipper)(nil)

// Options configures a Shipper. Every field takes its default when zero, so a
// caller states only what it knows: the driver server leaves TaskID to Reset,
// and tests shrink the tunables to keep a test from waiting on production-sized
// thresholds.
type Options struct {
	// TaskID is the task whose log the chunks are appended to, until Reset.
	TaskID int64
	// ChunkSize is the buffered-byte threshold that cuts a chunk.
	// Defaults to DefaultChunkSize when zero.
	ChunkSize int
	// FlushInterval is how often the sender is allowed to ship a sub-chunk
	// remainder. Defaults to DefaultFlushInterval when zero.
	FlushInterval time.Duration
	// MaxPendingBytes caps the unsent bytes held in the buffer; writes past it
	// are dropped. Defaults to DefaultMaxPendingBytes when zero.
	MaxPendingBytes int
	// BackOff schedules the wait between retries of a failed chunk. It is
	// stateful and becomes the shipper's own. Defaults to a fresh exponential
	// backoff when nil.
	BackOff backoff.BackOff
	// Log reports send failures. It must not write back into the shipper (see
	// the default in New). Defaults to a text handler on os.Stderr when nil.
	Log *slog.Logger
}

// New returns a shipper that mirrors everything written to it to the server as
// log chunks for opts.TaskID, stamped with version 0 until Reset says
// otherwise. A nil client ships nothing, and drops what is written, until Reset
// gives it one. Nothing is sent until Run (or Flush) drains it.
func New(client gritzclient.Client, opts Options) *Shipper {
	s := &Shipper{
		client:        client,
		taskID:        opts.TaskID,
		chunkSize:     cmp.Or(opts.ChunkSize, DefaultChunkSize),
		flushInterval: cmp.Or(opts.FlushInterval, DefaultFlushInterval),
		maxPending:    cmp.Or(opts.MaxPendingBytes, DefaultMaxPendingBytes),
		backoff:       opts.BackOff,
		log:           opts.Log,
		notify:        wakeup.New(),
		sendSem:       semaphore.NewWeighted(1),
	}
	if s.backoff == nil {
		s.backoff = backoff.NewExponentialBackOff()
	}
	if s.log == nil {
		// Deliberately not the driver's logger: the driver's slog handler writes
		// through the same sink this shipper is teed into, so logging a send
		// failure there would buffer a line that fails to ship, logging another
		// line, and so on. os.Stderr is outside the tee (it reaches docker logs)
		// and breaks the loop.
		s.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return s
}

// Reset points the shipper at a new run: chunks cut from here on go through
// client to taskID, stamped with version, so one shipper serves every run of a
// long-lived driver. Whatever is still buffered is dropped: it is the tail of
// an earlier run whose Flush gave up, and it is still in /gritz/log. A chunk
// already in flight is still delivered, through the client it was cut for.
func (s *Shipper) Reset(client gritzclient.Client, taskID, version int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client, s.taskID, s.version = client, taskID, version
	s.buf.Reset()
}

// Write buffers p for shipping. It always reports the full write and a nil
// error: the caller is a log tee that must not learn about network trouble, and
// its other writers (the /gritz/log file, os.Stderr) still hold the bytes.
// Bytes that would take the buffer past the pending cap are dropped silently;
// the durable copy in /gritz/log is the one that has to be complete. Bytes
// written while the shipper has no client belong to no run and are dropped too.
//
// The sender is woken only once a whole chunk is buffered. A quiet run
// therefore ships on the flushInterval tick instead of turning every log line
// into its own request and row.
func (s *Shipper) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.client == nil {
		s.mu.Unlock()
		return len(p), nil
	}
	if s.buf.Len()+len(p) <= s.maxPending {
		s.buf.Write(p)
	}
	full := s.buf.Len() >= s.chunkSize
	s.mu.Unlock()
	if full {
		s.notify.Wake()
	}
	return len(p), nil
}

// Run drains the buffer on every wake-up and every flushInterval tick, until
// ctx is cancelled. It is the shipper's background sender and is expected to
// run for the driver's lifetime.
//
// Only the tick lets a sub-chunkSize remainder out, and only once per tick: a
// wake means a whole chunk is already buffered, so a wake-started drain ships
// full chunks and leaves the rest. Without that, the bytes arriving during each
// round trip would go out as their own small chunk on the drain's next pass,
// turning steady output into one request and one log_chunks row per round trip
// instead of one per chunk.
func (s *Shipper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	// The first pass is neither a tick nor a wake: whatever was buffered before
	// Run started waits for the first tick, as it would have without it.
	partial := false
	for {
		s.drain(ctx, partial)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			partial = true
		case <-s.notify:
			partial = false
		}
	}
}

// Flush sends everything buffered, bounded by ctx. It returns an error only
// when ctx expires with bytes still unsent — the caller is expected to log it
// and carry on, since the full log remains in /gritz/log. It is safe to call
// while Run is sending; the two never have two requests in flight.
func (s *Shipper) Flush(ctx context.Context) error {
	if !s.drain(ctx, true) {
		return fmt.Errorf("flushing log chunks: %w", ctx.Err())
	}
	return nil
}

// cut takes up to chunkSize bytes off the front of the buffer and returns them
// as the chunk that ships them, stamped with the current run, or nil when
// there was nothing to take. A remainder smaller than chunkSize is only taken
// when partial is set; otherwise it stays buffered to coalesce with the bytes
// still to come. The data is a copy rather than a window on the buffer:
// bytes.Buffer slides its contents down to reclaim the space a cut freed, which
// a concurrent Write would then overwrite.
func (s *Shipper) cut(partial bool) *chunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf.Len() == 0 || (s.buf.Len() < s.chunkSize && !partial) {
		return nil
	}
	return &chunk{
		client: s.client,
		req: &gritzv1.AppendLogChunkRequest{
			TaskId:  s.taskID,
			Version: s.version,
			Data:    bytes.Clone(s.buf.Next(s.chunkSize)),
		},
	}
}

// drain cuts and sends chunks, one AppendLogChunk per chunk, until nothing is
// left that it may cut or ctx is done; it reports whether it got that far. A
// transient failure retries the same chunk after a backoff (head-of-line
// blocking, as the outbox does) so a retry can never be overtaken by newer
// bytes; a permanent one drops the chunk and advances rather than wedge the
// shipper forever.
//
// partial allows one sub-chunkSize chunk — the remainder buffered when the
// drain started. Once that has been cut, only full chunks are taken, so the
// bytes that arrive during each round trip wait for the next tick rather than
// each becoming a chunk of their own. Flush passes it too: absent a concurrent
// writer, one partial cut is what empties the buffer.
//
// An inflight chunk left behind by an earlier drain is sent first either way:
// it was already cut, so partial has no say over it.
func (s *Shipper) drain(ctx context.Context, partial bool) bool {
	if err := s.sendSem.Acquire(ctx, 1); err != nil {
		return false
	}
	defer s.sendSem.Release(1)
	for {
		if ctx.Err() != nil {
			return false
		}
		if s.inflight == nil {
			next := s.cut(partial)
			if next == nil {
				s.backoff.Reset()
				return true
			}
			// cut takes min(chunkSize, buffered), so a short chunk is the
			// remainder and spends the allowance.
			if len(next.req.GetData()) < s.chunkSize {
				partial = false
			}
			s.inflight = next
		}
		_, err := s.inflight.client.AppendLogChunk(ctx, s.inflight.req)
		switch {
		case err == nil:
			s.failing = false
			s.inflight = nil
		case isPermanentChunkError(err):
			s.log.Warn("dropping undeliverable log chunk", "task", s.inflight.req.GetTaskId(), "bytes", len(s.inflight.req.GetData()), "err", err)
			s.inflight = nil
		default:
			if !s.failing {
				s.failing = true
				s.log.Warn("log chunk send failed, will retry", "task", s.inflight.req.GetTaskId(), "err", err)
			}
			if !common.SleepContext(ctx, s.backoff.NextBackOff()) {
				return false
			}
		}
	}
}

// isPermanentChunkError reports whether err will never succeed on retry (the
// task is gone, the token cannot write it, the chunk is malformed). It mirrors
// the runner outbox's classification; the shipper drops such chunks instead of
// dead-lettering them, since a diagnostic byte run has nowhere to go.
func isPermanentChunkError(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodePermissionDenied:
		return true
	default:
		return false
	}
}
