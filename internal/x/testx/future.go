package testx

import (
	"time"

	"gotest.tools/v3/assert"
)

// Future is the result of a function that Go runs on its own goroutine. Use it
// to call something that blocks, check whether it has returned, and collect
// its result.
type Future[T any] struct {
	done chan struct{}
	v    T
}

// Go runs fn on a new goroutine and returns its future.
func Go[T any](fn func() T) *Future[T] {
	f := &Future[T]{done: make(chan struct{})}
	go func() {
		defer close(f.done)
		f.v = fn()
	}()
	return f
}

// Done reports whether fn has returned, without blocking. To check that fn
// is still blocked, call it after testing/synctest.Wait: outside a bubble a
// false result only means fn has not returned yet.
func (f *Future[T]) Done() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

// Wait returns fn's result, failing the test (via t.Fatal) if fn does not
// return within timeout. Inside a testing/synctest bubble the clock is fake,
// so the timeout only fires once every goroutine in the bubble is blocked.
func (f *Future[T]) Wait(t assert.TestingT, timeout time.Duration) T {
	if h, ok := t.(interface{ Helper() }); ok {
		h.Helper()
	}
	select {
	case <-f.done:
		return f.v
	case <-time.After(timeout):
		assert.Assert(t, false, "function did not return within %v", timeout)
		var zero T
		return zero
	}
}
