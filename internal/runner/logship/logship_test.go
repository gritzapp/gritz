package logship

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/icholy/gritz/internal/gritzclient"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/x/testx"
)

func TestShipper_Flush(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7})

	// Act - a write below the chunk size stays buffered until Flush cuts it
	n, err := shipper.Write([]byte("hello\n"))
	assert.NilError(t, err)
	assert.Equal(t, n, 6)
	assert.Assert(t, cmp.Len(client.AppendedLogChunks(), 0))
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("hello\n")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_CutsAtChunkSize(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7, ChunkSize: 4})

	// Act - one oversized write is cut into whole chunks; the 2-byte remainder
	// stays buffered until Flush.
	_, err := shipper.Write([]byte("aaaabbbbcc"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("bbbb")},
			{TaskId: 7, Data: []byte("cc")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_SetVersion(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7})

	// Act - the pre-run preamble ships as version 0, because the flush cuts it
	// before the run is stamped; everything after ships as version 3
	_, err := shipper.Write([]byte("preamble\n"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))
	shipper.SetVersion(3)
	_, err = shipper.Write([]byte("run\n"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Version: 0, Data: []byte("preamble\n")},
			{TaskId: 7, Version: 3, Data: []byte("run\n")},
		},
		protocmp.Transform(),
	)
}

// TestShipper_SetVersionDoesNotCut asserts the version change is only a stamp:
// bytes written either side of it share a chunk when nothing drained in
// between, and that chunk carries the version current when it was cut.
func TestShipper_SetVersionDoesNotCut(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7})

	// Act
	_, err := shipper.Write([]byte("preamble\n"))
	assert.NilError(t, err)
	shipper.SetVersion(3)
	_, err = shipper.Write([]byte("run\n"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Version: 3, Data: []byte("preamble\nrun\n")},
		},
		protocmp.Transform(),
	)
}

// TestShipper_TickDrainCutsOnePartialChunk asserts a tick-started drain ships
// the remainder that was buffered when it started and then stops: the bytes
// that arrive during each round trip wait for the next tick rather than each
// going out as a chunk of their own. Without that rule, steady output below the
// chunk size costs one request and one log_chunks row per round trip.
func TestShipper_TickDrainCutsOnePartialChunk(t *testing.T) {
	t.Parallel()
	// Arrange - a slow client, modelled by the next line of output landing
	// while the request is still in flight, as a steady trickle does.
	var shipper *Shipper
	trickle := []string{"line 2\n", "line 3\n"}
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			if len(trickle) > 0 {
				_, _ = shipper.Write([]byte(trickle[0]))
				trickle = trickle[1:]
			}
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper = New(client, Options{TaskID: 7})
	_, err := shipper.Write([]byte("line 1\n"))
	assert.NilError(t, err)

	// Act - the flushInterval tick
	assert.Assert(t, shipper.drain(t.Context(), true))

	// Assert - one request, carrying only what was buffered when it started
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("line 1\n")},
		},
		protocmp.Transform(),
	)

	// Act - a wake-started drain gets no partial at all, so the line that
	// arrived during the round trip keeps waiting.
	assert.Assert(t, shipper.drain(t.Context(), false))

	// Assert
	assert.Assert(t, cmp.Len(client.AppendedLogChunks(), 1))

	// Act - the next tick ships it
	assert.Assert(t, shipper.drain(t.Context(), true))

	// Assert
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("line 1\n")},
			{TaskId: 7, Data: []byte("line 2\n")},
		},
		protocmp.Transform(),
	)
}

// TestShipper_WakeDrainCutsOnlyFullChunks asserts a drain woken by a full chunk
// ships every full chunk buffered and leaves the remainder to coalesce with the
// bytes still to come.
func TestShipper_WakeDrainCutsOnlyFullChunks(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7, ChunkSize: 4})

	// Act
	_, err := shipper.Write([]byte("aaaabbbbcc"))
	assert.NilError(t, err)
	assert.Assert(t, shipper.drain(t.Context(), false))

	// Assert - the 2-byte remainder stays buffered
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("bbbb")},
		},
		protocmp.Transform(),
	)

	// Act - it takes a tick (or a Flush) to let the remainder out
	assert.Assert(t, shipper.drain(t.Context(), true))

	// Assert
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("bbbb")},
			{TaskId: 7, Data: []byte("cc")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_OrderingAcrossRetries(t *testing.T) {
	t.Parallel()
	// Arrange - the first two attempts fail transiently
	var attempts atomic.Int64
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			if attempts.Add(1) <= 2 {
				return nil, connect.NewError(connect.CodeUnavailable, errors.New("server unreachable"))
			}
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{
		TaskID:    7,
		ChunkSize: 4,
		BackOff:   backoff.NewConstantBackOff(time.Millisecond),
		Log:       slog.New(slog.DiscardHandler),
	})

	// Act
	for _, w := range []string{"aaaa", "bbbb", "cccc"} {
		_, err := shipper.Write([]byte(w))
		assert.NilError(t, err)
	}
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - the failed head is retried before anything newer is sent, so the
	// order the server sees is still write order.
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("bbbb")},
			{TaskId: 7, Data: []byte("cccc")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_PermanentErrorDropsChunk(t *testing.T) {
	t.Parallel()
	// Arrange - the server rejects one chunk in a way no retry can fix
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			if string(req.Data) == "bad\n" {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("data is required"))
			}
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{
		TaskID:    7,
		ChunkSize: 4,
		Log:       slog.New(slog.DiscardHandler),
	})

	// Act
	_, err := shipper.Write([]byte("bad\nok!\n"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - the rejected chunk is dropped rather than retried forever, and
	// the queue behind it still drains.
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("bad\n")},
			{TaskId: 7, Data: []byte("ok!\n")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_OverflowDropsSilently(t *testing.T) {
	t.Parallel()
	// Arrange - nothing drains, so the cap is reached after 16 bytes
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7, ChunkSize: 8, MaxPendingBytes: 16})

	// Act - the last two writes have nowhere to go; they must still report a
	// full, error-free write.
	for _, w := range []string{"aaaaaaaa", "bbbbbbbb", "cccc", "ddddd"} {
		n, err := shipper.Write([]byte(w))
		assert.NilError(t, err)
		assert.Equal(t, n, len(w))
	}
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - what fit ships, the rest is dropped without a trace: the
	// complete copy is the one in /gritz/log.
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("aaaaaaaa")},
			{TaskId: 7, Data: []byte("bbbbbbbb")},
		},
		protocmp.Transform(),
	)
}

// TestShipper_DrainingFreesRoomForNewWrites asserts the cap is on the unsent
// bytes, not on the run: once a recovered server drains the buffer, writes are
// accepted again instead of the shipper staying wedged at the cap.
func TestShipper_DrainingFreesRoomForNewWrites(t *testing.T) {
	t.Parallel()
	// Arrange - an unreachable server, so the buffer fills and stays full
	var delivered testx.SafeSlice[string]
	var down atomic.Bool
	down.Store(true)
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			if down.Load() {
				return nil, connect.NewError(connect.CodeUnavailable, errors.New("server unreachable"))
			}
			delivered.Append(string(req.Data))
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{
		TaskID:          7,
		ChunkSize:       8,
		MaxPendingBytes: 16,
		BackOff:         backoff.NewConstantBackOff(time.Millisecond),
		Log:             slog.New(slog.DiscardHandler),
	})
	go shipper.Run(t.Context())

	for _, w := range []string{"aaaaaaaa", "bbbbbbbb", "ccccc"} {
		_, err := shipper.Write([]byte(w))
		assert.NilError(t, err)
	}

	// Act - the server comes back and the buffer empties, so the next write has
	// room again.
	down.Store(false)
	testx.WaitForWithTimeout(t, t.Context(), 5*time.Second, func() bool {
		shipper.mu.Lock()
		defer shipper.mu.Unlock()
		return shipper.buf.Len() == 0
	})
	_, err := shipper.Write([]byte("dddddddd"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - the bytes written once there was room ship, and the gap left by
	// the ones that were dropped is silent.
	got := delivered.Slice()
	assert.DeepEqual(t, got[len(got)-1], "dddddddd")
	for _, data := range got {
		assert.Assert(t, !strings.Contains(data, "dropped"), "expected no dropped-bytes marker, got %q", data)
	}
}

func TestShipper_FlushDeadline(t *testing.T) {
	t.Parallel()
	// Arrange - a server that never recovers within the flush deadline
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("server unreachable"))
		},
	}
	shipper := New(client, Options{
		TaskID:  7,
		BackOff: backoff.NewConstantBackOff(time.Millisecond),
		Log:     slog.New(slog.DiscardHandler),
	})
	_, err := shipper.Write([]byte("hello\n"))
	assert.NilError(t, err)

	// Act
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err = shipper.Flush(ctx)

	// Assert - the deadline is reported, not swallowed, so the driver can warn
	assert.ErrorContains(t, err, "flushing log chunks")
	assert.Assert(t, errors.Is(err, context.DeadlineExceeded))
	// Writes keep succeeding after a failed flush: the tee must never learn
	// about network trouble.
	n, err := shipper.Write([]byte("more\n"))
	assert.NilError(t, err)
	assert.Equal(t, n, 5)
}

func TestShipper_Run(t *testing.T) {
	t.Parallel()
	// Arrange
	var delivered testx.SafeSlice[string]
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			delivered.Append(string(req.Data))
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7, FlushInterval: 10 * time.Millisecond})
	go shipper.Run(t.Context())

	// Act - a write far below the chunk size, so only the interval can cut it
	_, err := shipper.Write([]byte("trickle\n"))
	assert.NilError(t, err)

	// Assert
	testx.WaitForWithTimeout(t, t.Context(), 5*time.Second, func() bool {
		return len(delivered.Slice()) == 1
	})
	assert.DeepEqual(t, delivered.Slice(), []string{"trickle\n"})
}

func TestShipper_WriteDoesNotBlockOnASlowServer(t *testing.T) {
	t.Parallel()
	// Arrange - every send hangs until the test releases it, so the sender is
	// stuck in flight for the whole run of writes.
	release := make(chan struct{})
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	opts := Options{TaskID: 7, ChunkSize: 8, MaxPendingBytes: 16}
	shipper := New(client, opts)
	go shipper.Run(t.Context())

	// Act - write well past the cap while the sender is blocked
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			_, _ = shipper.Write([]byte("aaaaaaaa"))
		}
	}()

	// Assert - the writes finish without waiting for the server
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked on the in-flight sender")
	}
	close(release)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Memory stayed bounded while the sender was stuck: the 8000 bytes written
	// could only ever leave behind a full buffer plus the one chunk already in
	// flight, and the rest was dropped rather than queued.
	shipped := client.ShippedLog()
	assert.Assert(t, len(shipped) > 0)
	assert.Assert(t, len(shipped) <= opts.MaxPendingBytes+opts.ChunkSize,
		"shipped %d bytes, more than the cap plus one in-flight chunk", len(shipped))
	assert.Equal(t, shipped, strings.Repeat("a", len(shipped)))
	for _, req := range client.AppendedLogChunks() {
		assert.Assert(t, len(req.GetData()) <= opts.ChunkSize,
			"chunk of %d bytes exceeds the chunk size", len(req.GetData()))
	}
}

// ghTokenSecret is the secrets map the driver hands the shipper for one
// declared workspace secret.
var ghTokenSecret = map[string]string{"GH_TOKEN": "ghp_abc123"}

// TestShipper_PlainTextIsNotHeldBack asserts the mask holds nothing back from
// a stream that carries no secret, so the shipped log keeps up with the written
// one between flushes rather than trailing it by the length of a secret.
func TestShipper_PlainTextIsNotHeldBack(t *testing.T) {
	t.Parallel()
	// Arrange - a long secret value, so any length-based holdback would swallow
	// the whole write below
	var delivered testx.SafeSlice[string]
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			delivered.Append(string(req.Data))
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{
		TaskID:        7,
		Secrets:       map[string]string{"GH_TOKEN": strings.Repeat("s", 900)},
		FlushInterval: 10 * time.Millisecond,
	})
	go shipper.Run(t.Context())

	// Act - no Flush, so only what the mask lets through can be cut and shipped
	_, err := shipper.Write([]byte("cloning the repo\n"))
	assert.NilError(t, err)

	// Assert
	testx.WaitForWithTimeout(t, t.Context(), 5*time.Second, func() bool {
		return len(delivered.Slice()) == 1
	})
	assert.DeepEqual(t, delivered.Slice(), []string{"cloning the repo\n"})
}

// TestShipper_MasksAcrossWrites asserts a secret split across two writes is
// still masked: log bytes arrive in arbitrary runs, so a value can land on any
// write boundary.
func TestShipper_MasksAcrossWrites(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7, Secrets: ghTokenSecret})

	// Act
	for _, w := range []string{"cloning with ghp_", "abc123 now\n"} {
		_, err := shipper.Write([]byte(w))
		assert.NilError(t, err)
	}
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert
	assert.Equal(t, client.ShippedLog(), "cloning with [gritz:masked GH_TOKEN] now\n")
}

// TestShipper_MasksAcrossChunks asserts the mask state outlives a chunk cut:
// with one-byte writes and a chunk cut every four bytes, both boundaries fall
// inside the secret, and it must still never reach the server.
func TestShipper_MasksAcrossChunks(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7, Secrets: ghTokenSecret, ChunkSize: 4})

	// Act
	for _, b := range []byte("using ghp_abc123 now\n") {
		_, err := shipper.Write([]byte{b})
		assert.NilError(t, err)
	}
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - chunk boundaries fall wherever they fall, but the reassembled
	// transcript carries the marker and not the value.
	assert.Assert(t, len(client.AppendedLogChunks()) > 1, "expected more than one chunk")
	assert.Equal(t, client.ShippedLog(), "using [gritz:masked GH_TOKEN] now\n")
}

// TestShipper_FlushDrainsHeldBytes asserts Flush drains the tail the mask holds
// back mid-potential-match, so a run's last bytes ship with that run instead of
// arriving a run late.
func TestShipper_FlushDrainsHeldBytes(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, Options{TaskID: 7, Secrets: ghTokenSecret})

	// Act - the stream ends mid-match, so "ghp_abc" is held back
	_, err := shipper.Write([]byte("prefix ghp_abc"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - a partial match is not a secret; it ships whole, in this flush
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("prefix ghp_abc")},
		},
		protocmp.Transform(),
	)
}

func TestIsPermanentChunkError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unavailable", connect.NewError(connect.CodeUnavailable, errors.New("down")), false},
		{"internal", connect.NewError(connect.CodeInternal, errors.New("boom")), false},
		{"deadline", context.DeadlineExceeded, false},
		{"not found", connect.NewError(connect.CodeNotFound, errors.New("task 1 not found")), true},
		{"invalid argument", connect.NewError(connect.CodeInvalidArgument, errors.New("too big")), true},
		{"permission denied", connect.NewError(connect.CodePermissionDenied, errors.New("nope")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, isPermanentChunkError(tt.err), tt.want)
		})
	}
}
