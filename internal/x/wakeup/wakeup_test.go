package wakeup

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestSignal_NoSignalPending(t *testing.T) {
	c := NewSignal()
	select {
	case <-c:
		t.Fatal("expected no signal pending on fresh Signal")
	default:
	}
}

func TestSignal_WakeDeliversOne(t *testing.T) {
	c := NewSignal()
	c.Wake()

	select {
	case <-c:
	default:
		t.Fatal("expected one signal after Wake")
	}

	select {
	case <-c:
		t.Fatal("expected no further signal after first receive")
	default:
	}
}

func TestSignal_WakeCoalesces(t *testing.T) {
	c := NewSignal()
	for range 5 {
		c.Wake()
	}

	got := 0
	for {
		select {
		case <-c:
			got++
			continue
		default:
		}
		break
	}
	assert.Equal(t, got, 1, "expected coalesced bursts to deliver exactly one signal")
}

func TestBroadcast_WakeWakesAllWaiters(t *testing.T) {
	var b Broadcast
	waiters := []<-chan struct{}{b.Wait(), b.Wait(), b.Wait()}
	b.Wake()

	for i, w := range waiters {
		select {
		case <-w:
		default:
			t.Fatalf("expected waiter %d to be woken", i)
		}
	}
}

func TestBroadcast_WaitAfterWakeBlocksUntilNext(t *testing.T) {
	var b Broadcast
	b.Wait()
	b.Wake()
	w := b.Wait()

	select {
	case <-w:
		t.Fatal("expected Wait taken after Wake to block")
	default:
	}

	b.Wake()
	select {
	case <-w:
	default:
		t.Fatal("expected the next Wake to wake it")
	}
}

func TestBroadcast_ZeroValue(t *testing.T) {
	var b Broadcast
	w := b.Wait()

	select {
	case <-w:
		t.Fatal("expected no signal pending on zero Broadcast")
	default:
	}

	b.Wake()
	select {
	case <-w:
	default:
		t.Fatal("expected Wake to wake a zero-value Broadcast's waiter")
	}
}
