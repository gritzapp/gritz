package agent

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"testing/iotest"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestReadLines(t *testing.T) {
	t.Parallel()
	// Arrange - a line far over bufio.Scanner's 64 KiB default, then more
	// lines, the last one CRLF-terminated and an unterminated one after it
	long := strings.Repeat("x", 200<<10)
	r := strings.NewReader("first\n" + long + "\nthird\r\nlast")

	// Act
	var lines []string
	readLines(r, DiscardDriverLog, func(line []byte) {
		lines = append(lines, string(line))
	})

	// Assert
	assert.DeepEqual(t, lines, []string{"first", long, "third", "last"})
}

func TestReadLines_ReadError(t *testing.T) {
	t.Parallel()
	// Arrange - TimeoutReader fails the second read, mid-way through the long
	// line, and lets every read after it succeed
	var logBuf bytes.Buffer
	log := &DriverLog{Logger: *slog.New(slog.NewTextHandler(&logBuf, nil))}
	src := strings.NewReader("first\n" + strings.Repeat("x", 200<<10) + "\nafter\n")

	// Act
	var lines []string
	readLines(iotest.TimeoutReader(src), log, func(line []byte) {
		lines = append(lines, string(line))
	})

	// Assert - the error is logged, the cut-short line is dropped, and the
	// rest of the input is still drained
	assert.DeepEqual(t, lines, []string{"first"})
	assert.Assert(t, cmp.Contains(logBuf.String(), "level=ERROR"))
	assert.Assert(t, cmp.Contains(logBuf.String(), iotest.ErrTimeout.Error()))
	assert.Equal(t, src.Len(), 0)
}
