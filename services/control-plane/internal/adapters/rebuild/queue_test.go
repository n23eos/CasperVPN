package rebuild

import (
	"context"
	"testing"
	"time"
)

// A full channel used to spawn one goroutine per enqueue, unbounded, and each of
// them blocked forever after shutdown (workers gone, nobody draining). This
// guards the fix: overflow enqueues on a stopped queue must return promptly
// instead of parking goroutines on a channel nobody reads.
func TestEnqueue_OverflowDoesNotBlockAfterShutdown(t *testing.T) {
	q := New(nil, nil, 1, 1, nil)

	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)
	cancel()
	<-q.done // shutdown observed

	// Saturate the buffer (workers are gone, nothing drains).
	q.jobs <- job{kind: jobUser, id: "fill"}

	finished := make(chan struct{})
	go func() {
		// Overflow path: semaphore slots first, then the blocking fallback —
		// both must yield to done instead of hanging.
		for i := 0; i < cap(q.pending)+10; i++ {
			q.EnqueueUser("u")
		}
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked after shutdown — overflow goroutines would leak")
	}
}

// Offload goroutines are capped by the pending semaphore; the cap frees up as
// workers drain, so a fan-out larger than buffer+cap still completes.
func TestEnqueue_OverflowIsBoundedAndDrains(t *testing.T) {
	q := New(nil, nil, 2, 1, nil)

	drained := make(chan struct{})
	go func() { // stand-in worker: drain everything
		n := 0
		for range q.jobs {
			n++
			if n == 20 {
				close(drained)
				return
			}
		}
	}()

	for i := 0; i < 20; i++ {
		q.EnqueueUser("u")
	}
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("jobs lost: 20 enqueued, fewer drained")
	}
}
