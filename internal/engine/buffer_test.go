package engine

import (
	"sync"
	"testing"
	"time"
)

func TestChangeBuffer_SendReceive(t *testing.T) {
	buf := NewChangeBuffer[int](3)

	buf.Send(1)
	buf.Send(2)
	buf.Send(3)

	if buf.Len() != 3 {
		t.Errorf("expected len 3, got %d", buf.Len())
	}

	got := <-buf.Receive()
	if got != 1 {
		t.Errorf("expected 1, got %d", got)
	}

	got = <-buf.Receive()
	if got != 2 {
		t.Errorf("expected 2, got %d", got)
	}

	got = <-buf.Receive()
	if got != 3 {
		t.Errorf("expected 3, got %d", got)
	}
}

func TestChangeBuffer_BlockPolicy(t *testing.T) {
	buf := NewChangeBuffer[int](1)

	buf.Send(1) // fills the buffer

	// Second send should block until consumer reads.
	done := make(chan struct{})
	go func() {
		buf.Send(2) // blocks
		close(done)
	}()

	// Verify it's blocked.
	select {
	case <-done:
		t.Fatal("expected send to block")
	case <-time.After(50 * time.Millisecond):
		// good, it's blocked
	}

	// Read to unblock.
	<-buf.Receive()

	select {
	case <-done:
		// unblocked
	case <-time.After(1 * time.Second):
		t.Fatal("expected send to unblock after read")
	}
}

func TestChangeBuffer_Close(t *testing.T) {
	buf := NewChangeBuffer[int](5)
	buf.Send(1)
	buf.Send(2)
	buf.Close()

	// Should be able to drain remaining items.
	count := 0
	for range buf.Receive() {
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 items after close, got %d", count)
	}
}

func TestChangeBuffer_SendAfterClose(t *testing.T) {
	buf := NewChangeBuffer[int](5)
	buf.Close()

	// Send after close should return false (not panic).
	ok := buf.Send(1)
	if ok {
		t.Error("expected false for send after close")
	}
}

func TestChangeBuffer_Cap(t *testing.T) {
	buf := NewChangeBuffer[int](10)
	if buf.Cap() != 10 {
		t.Errorf("expected cap 10, got %d", buf.Cap())
	}
}

func TestChangeBuffer_MinSize(t *testing.T) {
	buf := NewChangeBuffer[int](0)
	if buf.Cap() != 1 {
		t.Errorf("expected min cap 1, got %d", buf.Cap())
	}
}

func TestChangeBuffer_ConcurrentSendReceive(t *testing.T) {
	buf := NewChangeBuffer[int](100)
	const n = 1000

	var wg sync.WaitGroup

	// Producer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range n {
			buf.Send(i)
		}
		buf.Close()
	}()

	// Consumer.
	var received int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range buf.Receive() {
			received++
		}
	}()

	wg.Wait()
	if received != n {
		t.Errorf("expected %d received, got %d", n, received)
	}
}

func TestChangeBuffer_TrySend_Success(t *testing.T) {
	buf := NewChangeBuffer[int](2)

	if !buf.TrySend(1) {
		t.Error("expected TrySend to succeed on empty buffer")
	}
	if !buf.TrySend(2) {
		t.Error("expected TrySend to succeed on non-full buffer")
	}
}

func TestChangeBuffer_TrySend_Full(t *testing.T) {
	buf := NewChangeBuffer[int](1)
	buf.Send(1) // fill it

	if buf.TrySend(2) {
		t.Error("expected TrySend to return false when buffer is full")
	}

	// Original item should still be there.
	got := <-buf.Receive()
	if got != 1 {
		t.Errorf("expected 1, got %d", got)
	}
}

func TestChangeBuffer_SendDropOldest_NoDropNeeded(t *testing.T) {
	buf := NewChangeBuffer[int](2)

	_, didDrop := buf.SendDropOldest(1)
	if didDrop {
		t.Error("expected no drop on empty buffer")
	}
	if buf.Len() != 1 {
		t.Errorf("expected len 1, got %d", buf.Len())
	}
}

func TestChangeBuffer_SendDropOldest_DropsOldest(t *testing.T) {
	buf := NewChangeBuffer[int](2)
	buf.Send(10)
	buf.Send(20) // full

	dropped, didDrop := buf.SendDropOldest(30)
	if !didDrop {
		t.Error("expected drop when buffer is full")
	}
	if dropped != 10 {
		t.Errorf("expected oldest (10) to be dropped, got %d", dropped)
	}

	// Buffer should contain [20, 30].
	got1 := <-buf.Receive()
	got2 := <-buf.Receive()
	if got1 != 20 || got2 != 30 {
		t.Errorf("expected [20, 30], got [%d, %d]", got1, got2)
	}
}
