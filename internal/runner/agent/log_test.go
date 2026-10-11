package agent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
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

func TestOpenDriverLog_Ships(t *testing.T) {
	t.Parallel()
	// Arrange - a log with a shipped branch
	logPath := filepath.Join(t.TempDir(), "log")
	var shipped bytes.Buffer
	log := OpenDriverLog(logPath, &shipped)

	// Act - write through every tee
	_, err := io.WriteString(log.Sink(), "sink\n")
	assert.NilError(t, err)
	log.StartRun(3)
	log.Info("logged")
	assert.NilError(t, log.Close())

	// Assert - the shipped branch got the log file byte for byte
	file, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(file), "==== run version=3"))
	assert.Equal(t, shipped.String(), string(file))
}
