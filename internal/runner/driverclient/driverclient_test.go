package driverclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"gotest.tools/v3/assert"
)

const testIdle = 100 * time.Millisecond

// runStream is the driver's end of a Run stream.
type runStream = connect.BidiStream[driverv1.RunRequest, driverv1.RunResponse]

// testDriver is a driver server whose Run is supplied by the test.
type testDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler
	run func(ctx context.Context, stream *runStream) error
}

// Run reads the first request before handing the stream to run, as the real
// driver server does. A handler that sent and returned without reading it
// could reset the stream while the client's first Send was still writing.
func (d *testDriver) Run(ctx context.Context, stream *runStream) error {
	if _, err := stream.Receive(); err != nil {
		return err
	}
	return d.run(ctx, stream)
}

// setup serves d over HTTP/2, which bidi streams need, and returns a client
// for it built by New.
func setup(t *testing.T, d *testDriver) driverv1connect.DriverServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(driverv1connect.NewDriverServiceHandler(d))
	hs := httptest.NewUnstartedServer(mux)
	hs.EnableHTTP2 = true
	hs.StartTLS()
	t.Cleanup(hs.Close)
	return New(hs.Client(), hs.URL, Options{IdleTimeout: testIdle})
}

var (
	hello = &driverv1.RunResponse{
		Msg: &driverv1.RunResponse_Hello{Hello: &driverv1.Hello{}},
	}
	keepAlive = &driverv1.RunResponse{
		Msg: &driverv1.RunResponse_KeepAlive{KeepAlive: &driverv1.KeepAlive{}},
	}
	idle = &driverv1.RunResponse{
		Msg: &driverv1.RunResponse_Idle{
			Idle: &driverv1.Idle{Version: 1, Reported: true},
		},
	}
)

// open opens a Run stream on client and sends its first message, a spec as
// the runner sends to start a run. It must not be empty: connect writes an
// empty message's 5-byte prefix and then a zero-length body, and that second
// write blocks until the transport reads the pipe again. The driver has the
// whole message after the prefix, so it can end the stream first and fail the
// Send with io.EOF.
func open(t *testing.T, client driverv1connect.DriverServiceClient) *connect.BidiStreamForClientSimple[driverv1.RunRequest, driverv1.RunResponse] {
	t.Helper()
	stream, err := client.Run(t.Context())
	assert.NilError(t, err)
	t.Cleanup(func() { _ = stream.CloseResponse() })
	assert.NilError(t, stream.Send(&driverv1.RunRequest{
		Msg: &driverv1.RunRequest_Spec{Spec: &driverv1.RunSpec{TaskId: 1}},
	}))
	return stream
}

// receiveAll reads stream to its end, returning every message and the error
// it ended with, nil for a clean end.
func receiveAll(stream *connect.BidiStreamForClientSimple[driverv1.RunRequest, driverv1.RunResponse]) ([]*driverv1.RunResponse, error) {
	var msgs []*driverv1.RunResponse
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return msgs, nil
		}
		if err != nil {
			return msgs, err
		}
		msgs = append(msgs, msg)
	}
}

func TestRun_HidesKeepAlives(t *testing.T) {
	t.Parallel()
	client := setup(t, &testDriver{run: func(_ context.Context, stream *runStream) error {
		for _, msg := range []*driverv1.RunResponse{
			hello,
			keepAlive,
			keepAlive,
			keepAlive,
			idle,
		} {
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
		return nil
	}})

	stream := open(t, client)
	msgs, err := receiveAll(stream)

	assert.NilError(t, err)
	assert.Equal(t, len(msgs), 2)
	assert.Assert(t, msgs[0].GetHello() != nil, "got %v, want hello", msgs[0])
	assert.Assert(t, msgs[1].GetIdle() != nil, "got %v, want idle", msgs[1])
}

func TestRun_IdleStreamCancelled(t *testing.T) {
	t.Parallel()
	client := setup(t, &testDriver{run: func(ctx context.Context, stream *runStream) error {
		// A half-open stream: hello, then silence.
		if err := stream.Send(hello); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}})

	stream := open(t, client)
	start := time.Now()
	msgs, err := receiveAll(stream)

	assert.ErrorIs(t, err, ErrStreamIdle)
	assert.Equal(t, len(msgs), 1)
	assert.Assert(t, msgs[0].GetHello() != nil, "got %v, want hello", msgs[0])
	assert.Assert(t, time.Since(start) < 10*testIdle, "cancelled after %v", time.Since(start))
}

func TestRun_LiveStreamKept(t *testing.T) {
	t.Parallel()
	client := setup(t, &testDriver{run: func(_ context.Context, stream *runStream) error {
		if err := stream.Send(hello); err != nil {
			return err
		}
		// Keep-alives well inside the idle timeout, for several times its length.
		for range 20 {
			time.Sleep(testIdle / 5)
			if err := stream.Send(keepAlive); err != nil {
				return err
			}
		}
		return stream.Send(idle)
	}})

	stream := open(t, client)
	msgs, err := receiveAll(stream)

	assert.NilError(t, err)
	assert.Equal(t, len(msgs), 2)
	assert.Assert(t, msgs[1].GetIdle() != nil, "got %v, want idle", msgs[1])
}
