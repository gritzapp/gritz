package driverclient

import (
	"context"
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

// testDriver is a driver server whose Run and Status are supplied by the test.
type testDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler
	run    func(ctx context.Context, stream *connect.ServerStream[driverv1.RunResponse]) error
	status func(ctx context.Context) error
}

func (d *testDriver) Run(ctx context.Context, _ *driverv1.RunRequest, stream *connect.ServerStream[driverv1.RunResponse]) error {
	return d.run(ctx, stream)
}

func (d *testDriver) Status(ctx context.Context, _ *driverv1.StatusRequest) (*driverv1.StatusResponse, error) {
	if err := d.status(ctx); err != nil {
		return nil, err
	}
	return &driverv1.StatusResponse{}, nil
}

// setup serves d and returns a client for it built by New.
func setup(t *testing.T, d *testDriver) driverv1connect.DriverServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(driverv1connect.NewDriverServiceHandler(d))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return New(hs.Client(), hs.URL, Options{IdleTimeout: testIdle})
}

var (
	accepted  = &driverv1.RunResponse{Event: &driverv1.RunResponse_Accepted{Accepted: &driverv1.Accepted{}}}
	keepAlive = &driverv1.RunResponse{Event: &driverv1.RunResponse_KeepAlive{KeepAlive: &driverv1.KeepAlive{}}}
	finished  = &driverv1.RunResponse{Event: &driverv1.RunResponse_Finished{Finished: &driverv1.Finished{Version: 1, Reported: true}}}
)

// receiveAll reads stream to its end, returning every message and its error.
func receiveAll(stream *connect.ServerStreamForClient[driverv1.RunResponse]) ([]*driverv1.RunResponse, error) {
	var msgs []*driverv1.RunResponse
	for stream.Receive() {
		msgs = append(msgs, stream.Msg())
	}
	return msgs, stream.Err()
}

func TestRun_HidesKeepAlives(t *testing.T) {
	t.Parallel()
	client := setup(t, &testDriver{run: func(_ context.Context, stream *connect.ServerStream[driverv1.RunResponse]) error {
		for _, msg := range []*driverv1.RunResponse{accepted, keepAlive, keepAlive, keepAlive, finished} {
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
		return nil
	}})

	stream, err := client.Run(t.Context(), &driverv1.RunRequest{Version: 1})
	assert.NilError(t, err)
	defer stream.Close()
	msgs, err := receiveAll(stream)

	assert.NilError(t, err)
	assert.Equal(t, len(msgs), 2)
	assert.Assert(t, msgs[0].GetAccepted() != nil, "got %v, want accepted", msgs[0])
	assert.Assert(t, msgs[1].GetFinished() != nil, "got %v, want finished", msgs[1])
}

func TestRun_IdleStreamCancelled(t *testing.T) {
	t.Parallel()
	client := setup(t, &testDriver{run: func(ctx context.Context, stream *connect.ServerStream[driverv1.RunResponse]) error {
		// A half-open stream: accepted, then silence.
		if err := stream.Send(accepted); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}})

	stream, err := client.Run(t.Context(), &driverv1.RunRequest{Version: 1})
	assert.NilError(t, err)
	defer stream.Close()
	start := time.Now()
	msgs, err := receiveAll(stream)

	assert.ErrorIs(t, err, ErrStreamIdle)
	assert.Equal(t, len(msgs), 1)
	assert.Assert(t, msgs[0].GetAccepted() != nil, "got %v, want accepted", msgs[0])
	assert.Assert(t, time.Since(start) < 10*testIdle, "cancelled after %v", time.Since(start))
}

func TestRun_LiveStreamKept(t *testing.T) {
	t.Parallel()
	client := setup(t, &testDriver{run: func(_ context.Context, stream *connect.ServerStream[driverv1.RunResponse]) error {
		if err := stream.Send(accepted); err != nil {
			return err
		}
		// Keep-alives well inside the idle timeout, for several times its length.
		for range 20 {
			time.Sleep(testIdle / 5)
			if err := stream.Send(keepAlive); err != nil {
				return err
			}
		}
		return stream.Send(finished)
	}})

	stream, err := client.Run(t.Context(), &driverv1.RunRequest{Version: 1})
	assert.NilError(t, err)
	defer stream.Close()
	msgs, err := receiveAll(stream)

	assert.NilError(t, err)
	assert.Equal(t, len(msgs), 2)
	assert.Assert(t, msgs[1].GetFinished() != nil, "got %v, want finished", msgs[1])
}

func TestStatus_NotWatched(t *testing.T) {
	t.Parallel()
	client := setup(t, &testDriver{status: func(context.Context) error {
		time.Sleep(2 * testIdle)
		return nil
	}})

	_, err := client.Status(t.Context(), &driverv1.StatusRequest{})

	assert.NilError(t, err)
}
