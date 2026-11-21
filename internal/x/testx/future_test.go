package testx

import (
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"
)

// TestFuture checks that a future is not done while its function blocks, and
// that Wait returns the function's result once it returns.
func TestFuture(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Arrange
		release := make(chan struct{})
		f := Go(func() int {
			<-release
			return 7
		})
		synctest.Wait()
		assert.Assert(t, !f.Done())

		// Act
		close(release)
		got := f.Wait(t, time.Second)

		// Assert
		assert.Equal(t, got, 7)
		assert.Assert(t, f.Done())
	})
}

// TestFutureWait_Timeout checks that Wait fails the test when the function
// does not return within the timeout.
func TestFutureWait_Timeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Arrange
		release := make(chan struct{})
		f := Go(func() int {
			<-release
			return 7
		})

		// Act
		failed := runAssert(func(t assert.TestingT) {
			f.Wait(t, time.Second)
		})

		// Assert
		assert.Assert(t, failed)
		close(release)
		synctest.Wait()
		assert.Assert(t, f.Done())
	})
}
