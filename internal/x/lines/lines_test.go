package lines

import (
	"strings"
	"testing"
	"testing/iotest"

	"gotest.tools/v3/assert"
)

func TestRead(t *testing.T) {
	t.Parallel()
	// Arrange - a line far over bufio.Scanner's 64 KiB default, then more
	// lines, one CRLF-terminated and an unterminated one after it
	long := strings.Repeat("x", 200<<10)
	r := strings.NewReader("first\n" + long + "\nthird\r\nlast")

	// Act
	var got []string
	for line, err := range Read(r) {
		assert.NilError(t, err)
		got = append(got, string(line))
	}

	// Assert
	assert.DeepEqual(t, got, []string{"first", long, "third", "last"})
}

func TestRead_Error(t *testing.T) {
	t.Parallel()
	// Arrange - TimeoutReader fails the second read, mid-way through the long
	// line
	r := iotest.TimeoutReader(strings.NewReader("first\n" + strings.Repeat("x", 200<<10) + "\nafter\n"))

	// Act
	var got []string
	var gotErr error
	for line, err := range Read(r) {
		if err != nil {
			gotErr = err
			continue
		}
		got = append(got, string(line))
	}

	// Assert - the cut-short line is dropped and the error ends the iteration
	assert.DeepEqual(t, got, []string{"first"})
	assert.ErrorIs(t, gotErr, iotest.ErrTimeout)
}

func TestRead_Break(t *testing.T) {
	t.Parallel()
	// Arrange
	r := strings.NewReader("a\nb\nc\n")

	// Act
	var got []string
	for line := range Read(r) {
		got = append(got, string(line))
		break
	}

	// Assert
	assert.DeepEqual(t, got, []string{"a"})
}
