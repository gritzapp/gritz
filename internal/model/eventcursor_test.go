package model

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestEventCursor_Encode(t *testing.T) {
	t.Parallel()
	// Arrange
	want := EventCursor{XID: 1 << 40, ID: 42}

	// Act
	got, err := DecodeEventCursor(want.Encode())

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, got, want)
}

func TestDecodeEventCursor_Empty(t *testing.T) {
	t.Parallel()
	// Act
	got, err := DecodeEventCursor(nil)

	// Assert - empty is the start of the log.
	assert.NilError(t, err)
	assert.Equal(t, got, EventCursor{})
}

func TestDecodeEventCursor_Invalid(t *testing.T) {
	t.Parallel()
	// Act
	_, err := DecodeEventCursor([]byte{0xff})

	// Assert
	assert.ErrorContains(t, err, "invalid cursor")
}
