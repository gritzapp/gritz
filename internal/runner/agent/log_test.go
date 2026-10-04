package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/logship"
)

func TestOpenLogSink_CreatesFile(t *testing.T) {
	t.Parallel()
	// Arrange - a path under a temp dir whose parent does not exist yet, so the
	// helper's MkdirAll fallback is exercised.
	logPath := filepath.Join(t.TempDir(), "sub", "log")

	// Act
	sink, err := OpenLogSink(logPath)
	assert.NilError(t, err)
	defer sink.Close()
	_, err = io.WriteString(sink, "hello\n")
	assert.NilError(t, err)
	assert.NilError(t, sink.Close())

	// Assert
	data, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Equal(t, string(data), "hello\n")
}

func TestOpenLogSink_AppendsExisting(t *testing.T) {
	t.Parallel()
	// Arrange - an existing file with prior content
	logPath := filepath.Join(t.TempDir(), "log")
	assert.NilError(t, os.WriteFile(logPath, []byte("run1\n"), 0o666))

	// Act - open and write again; O_APPEND must preserve, not truncate
	sink, err := OpenLogSink(logPath)
	assert.NilError(t, err)
	_, err = io.WriteString(sink, "run2\n")
	assert.NilError(t, err)
	assert.NilError(t, sink.Close())

	// Assert
	data, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Equal(t, string(data), "run1\nrun2\n")
}

func TestOpenLogSink_FailureDegradesToNoop(t *testing.T) {
	t.Parallel()
	// Arrange - a path whose parent is an existing regular file, so both
	// MkdirAll and OpenFile fail.
	file := filepath.Join(t.TempDir(), "file")
	assert.NilError(t, os.WriteFile(file, nil, 0o666))
	logPath := filepath.Join(file, "log")

	// Act
	sink, err := OpenLogSink(logPath)

	// Assert - the open failed but the returned sink is a usable no-op, so a run
	// never fails because logging could not be set up.
	assert.Assert(t, err != nil)
	assert.Assert(t, sink != nil)
	_, werr := io.WriteString(sink, "discarded\n")
	assert.NilError(t, werr)
	assert.NilError(t, sink.Close())
}

func TestOpenDriverLog_FailureDegrades(t *testing.T) {
	t.Parallel()
	// Arrange - a path whose parent is an existing regular file, so the sink
	// cannot be opened.
	file := filepath.Join(t.TempDir(), "file")
	assert.NilError(t, os.WriteFile(file, nil, 0o666))
	logPath := filepath.Join(file, "log")

	// Act - OpenDriverLog never returns nil and never fails the run
	log := OpenDriverLog(logPath, nil)
	defer log.Close()

	// Assert - the sink is a usable no-op and the logger still works
	assert.Assert(t, log.Sink() != nil)
	_, werr := io.WriteString(log.Sink(), "discarded\n")
	assert.NilError(t, werr)
	log.Info("still logs to stderr")
	assert.NilError(t, log.Close())
}

func TestDriverLog_CloseFlushesShipper(t *testing.T) {
	t.Parallel()
	// Arrange - a driver log holding bytes that no run ever flushed, as after an
	// early-error exit (a failed GetTask returns before Run's end-of-run flush).
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(_ context.Context, _ *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	log := OpenDriverLog(filepath.Join(t.TempDir(), "log"), logship.New(client, logship.Options{TaskID: 7}))
	_, err := io.WriteString(log.Sink(), "early failure\n")
	assert.NilError(t, err)

	// Act - the Close the driver command already defers
	assert.NilError(t, log.Close())

	// Assert - the backstop shipped the buffered tail, stamped with the
	// pre-run version.
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("early failure\n")},
		},
		protocmp.Transform(),
	)
}

func TestDriverLog_NoShipperNeverShips(t *testing.T) {
	t.Parallel()
	// Arrange - a nil shipper, as tests and directly-invoked drivers pass. The
	// mock has no AppendLogChunkFunc, so any call at all would panic.
	log := OpenDriverLog(filepath.Join(t.TempDir(), "log"), nil)

	// Act
	_, err := io.WriteString(log.Sink(), "not shipped\n")
	assert.NilError(t, err)
	log.StartRun(3)
	assert.NilError(t, log.Flush(t.Context()))
	assert.NilError(t, log.Close())
}
