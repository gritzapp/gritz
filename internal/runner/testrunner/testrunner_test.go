package testrunner_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
)

// scriptedDriver is a driver whose Run sends msgs and ends the stream.
type scriptedDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler

	msgs []*driverv1.RunResponse
}

func (d *scriptedDriver) Run(_ context.Context, _ *driverv1.RunRequest, stream *connect.ServerStream[driverv1.RunResponse]) error {
	for _, msg := range d.msgs {
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

func TestNewStubDriver(t *testing.T) {
	t.Parallel()
	// Arrange - a stub that accepts, keeps alive and finishes, which is the
	// driver of an up sandbox.
	msgs := []*driverv1.RunResponse{
		{Event: &driverv1.RunResponse_Accepted{Accepted: &driverv1.Accepted{}}},
		{Event: &driverv1.RunResponse_KeepAlive{KeepAlive: &driverv1.KeepAlive{}}},
		{Event: &driverv1.RunResponse_Finished{Finished: &driverv1.Finished{Version: 1}}},
	}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewStubDriver(t, &scriptedDriver{msgs: msgs}),
	})
	sb, err := be.Open(be.Add("c1", fake.Up))
	assert.NilError(t, err)
	client, err := sb.Dial(t.Context(), "jwt")
	assert.NilError(t, err)

	// Act
	stream, err := client.Run(t.Context(), &driverv1.RunRequest{Version: 1})
	assert.NilError(t, err)
	defer func() { assert.NilError(t, stream.Close()) }()
	var got []*driverv1.RunResponse
	for stream.Receive() {
		got = append(got, proto.CloneOf(stream.Msg()))
	}

	// Assert - the stream carries the script, then ends cleanly.
	assert.NilError(t, stream.Err())
	assert.DeepEqual(t, got, msgs, protocmp.Transform())
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.Runs, 1)
}

func TestNewStubDriver_Unimplemented(t *testing.T) {
	t.Parallel()
	// Arrange - a stub that scripts nothing.
	client := testrunner.NewStubDriver(t, &scriptedDriver{})

	// Act
	_, err := client.Status(t.Context(), &driverv1.StatusRequest{})

	// Assert - unscripted calls fall through to the embedded handler.
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnimplemented)
}
