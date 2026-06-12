package fanout

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/test/testutil"
)

// TestSource_PauseBuffersUntilResume verifies true pause: while paused, Stream
// forwards nothing and buffers incoming changes; Resume flushes the backlog.
func TestSource_PauseBuffersUntilResume(t *testing.T) {
	s := New(Table("public", "events"))

	var mu sync.Mutex
	var got []string
	handler := laredo.ChangeHandlerFunc(func(ev laredo.ChangeEvent) error {
		mu.Lock()
		got = append(got, ev.Position.(string))
		mu.Unlock()
		return nil
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(got) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Stream(ctx, "", handler) }()

	// A change while streaming is forwarded.
	s.enqueue(nil, laredo.Row{"id": 1}, "0/1")
	testutil.AssertEventually(t, time.Second, func() bool { return count() == 1 }, "first change forwarded")

	// While paused, changes buffer and are NOT forwarded.
	if err := s.Pause(ctx); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	s.enqueue(nil, laredo.Row{"id": 2}, "0/2")
	s.enqueue(nil, laredo.Row{"id": 3}, "0/3")
	// Give the loop ample opportunity to (wrongly) drain before asserting it did not.
	time.Sleep(50 * time.Millisecond)
	if c := count(); c != 1 {
		t.Fatalf("paused: forwarded %d changes, want 1 (buffered)", c)
	}

	// Resume flushes the backlog.
	if err := s.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	testutil.AssertEventually(t, time.Second, func() bool { return count() == 3 }, "backlog flushed on resume")

	cancel()
	<-done
}
