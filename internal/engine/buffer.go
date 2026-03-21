package engine

// ChangeBuffer is a bounded buffer between the source dispatcher and a pipeline's
// target. It decouples the source read speed from the target write speed.
//
// The Block policy (default) uses Go's built-in channel blocking: when the buffer
// is full, sends block until the consumer reads.
type ChangeBuffer[T any] struct {
	ch   chan T
	size int
}

// NewChangeBuffer creates a bounded buffer with the given capacity.
func NewChangeBuffer[T any](size int) *ChangeBuffer[T] {
	if size <= 0 {
		size = 1
	}
	return &ChangeBuffer[T]{
		ch:   make(chan T, size),
		size: size,
	}
}

// Send adds an item to the buffer. Blocks if the buffer is full (Block policy).
// Returns false if the buffer has been closed.
func (b *ChangeBuffer[T]) Send(item T) bool {
	defer func() { recover() }() //nolint:errcheck // catch send on closed channel
	b.ch <- item
	return true
}

// TrySend attempts a non-blocking send. Returns false if the buffer is full
// or closed (Error policy: caller marks pipeline as ERROR).
func (b *ChangeBuffer[T]) TrySend(item T) bool {
	select {
	case b.ch <- item:
		return true
	default:
		return false
	}
}

// SendDropOldest adds an item to the buffer. If the buffer is full, it drops
// the oldest item to make room (DropOldest policy — ring buffer semantics).
// Returns the dropped item and true if an item was dropped, or zero value and
// false if no drop was needed.
func (b *ChangeBuffer[T]) SendDropOldest(item T) (dropped T, didDrop bool) {
	select {
	case b.ch <- item:
		return dropped, false
	default:
		// Buffer full — drain oldest, then send.
		dropped = <-b.ch
		b.ch <- item
		return dropped, true
	}
}

// Receive returns the channel for reading items.
func (b *ChangeBuffer[T]) Receive() <-chan T {
	return b.ch
}

// Close closes the buffer channel. The consumer should drain remaining items.
func (b *ChangeBuffer[T]) Close() {
	close(b.ch)
}

// Len returns the current number of items in the buffer.
func (b *ChangeBuffer[T]) Len() int {
	return len(b.ch)
}

// Cap returns the buffer capacity.
func (b *ChangeBuffer[T]) Cap() int {
	return b.size
}
