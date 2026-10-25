package runner

import (
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// TestDoubling checks the backoff matches the schedule the hand-rolled loops
// used: start at initial, double without jitter, cap at maxInterval, and go
// back to initial on Reset.
func TestDoubling(t *testing.T) {
	bo := doubling(250*time.Millisecond, 3*time.Second)
	var got []time.Duration
	for range 6 {
		got = append(got, bo.NextBackOff())
	}
	assert.DeepEqual(t, got, []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second,
	})
	bo.Reset()
	assert.Equal(t, bo.NextBackOff(), 250*time.Millisecond)
}
