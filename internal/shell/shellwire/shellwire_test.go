package shellwire_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/shell/shellwire"
	"gotest.tools/v3/assert"
)

func TestDataRoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange: arbitrary non-UTF-8 bytes, since PTY output is not text-safe.
	payload := []byte{0x00, 0x01, 0xff, 0xfe, 0x80, 'h', 'i', 0x00}

	// Act
	frame, err := shellwire.Parse(shellwire.Data(payload))

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, frame.Type, shellwire.TypeData)
	assert.DeepEqual(t, frame.Payload, payload)
}

func TestDataEmptyRoundTrip(t *testing.T) {
	t.Parallel()
	// Act
	frame, err := shellwire.Parse(shellwire.Data(nil))

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, frame.Type, shellwire.TypeData)
	assert.Equal(t, len(frame.Payload), 0)
}

func TestResizeRoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange
	tests := []struct {
		name       string
		rows, cols uint16
	}{
		{"typical", 24, 80},
		{"zero", 0, 0},
		{"max", 65535, 65535},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			frame, err := shellwire.Parse(shellwire.Resize(tt.rows, tt.cols))
			assert.NilError(t, err)
			assert.Equal(t, frame.Type, shellwire.TypeResize)
			rows, cols, err := frame.ResizeDims()

			// Assert
			assert.NilError(t, err)
			assert.Equal(t, rows, tt.rows)
			assert.Equal(t, cols, tt.cols)
		})
	}
}

func TestExitRoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange
	for _, code := range []int{0, 1, 42, 130, 255} {
		// Act
		frame, err := shellwire.Parse(shellwire.Exit(code))
		assert.NilError(t, err)
		assert.Equal(t, frame.Type, shellwire.TypeExit)
		got, err := frame.ExitCode()

		// Assert
		assert.NilError(t, err)
		assert.Equal(t, got, code)
	}
}

func TestPingRoundTrip(t *testing.T) {
	t.Parallel()
	// Act
	frame, err := shellwire.Parse(shellwire.Ping())

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, frame.Type, shellwire.TypePing)
	assert.Equal(t, len(frame.Payload), 0)
}

func TestParseRejectsEmpty(t *testing.T) {
	t.Parallel()
	// Act
	_, err := shellwire.Parse(nil)

	// Assert
	assert.ErrorContains(t, err, "empty frame")
}

func TestResizeDimsRejectsWrongType(t *testing.T) {
	t.Parallel()
	// Arrange
	frame, err := shellwire.Parse(shellwire.Data([]byte("x")))
	assert.NilError(t, err)

	// Act
	_, _, err = frame.ResizeDims()

	// Assert
	assert.ErrorContains(t, err, "not a resize frame")
}

func TestExitCodeRejectsWrongType(t *testing.T) {
	t.Parallel()
	// Arrange
	frame, err := shellwire.Parse(shellwire.Data([]byte("x")))
	assert.NilError(t, err)

	// Act
	_, err = frame.ExitCode()

	// Assert
	assert.ErrorContains(t, err, "not an exit frame")
}
