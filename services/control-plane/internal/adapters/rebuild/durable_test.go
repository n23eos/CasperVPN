package rebuild

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/control-plane/internal/domain"
)

// fakeStore is an in-memory JobStore for offline tests. Retried jobs stay
// immediately claimable (backoff is ignored) so a test can iterate the
// attempt->drop path deterministically without a clock.
type fakeStore struct {
	mu        sync.Mutex
	next      int64
	jobs      map[int64]*Job
	completed []int64
	retried   []int64
}

func newFakeStore() *fakeStore { return &fakeStore{jobs: map[int64]*Job{}} }

func (s *fakeStore) Enqueue(_ context.Context, kind JobKind, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	s.jobs[s.next] = &Job{ID: s.next, Kind: kind, TargetID: id}
	return nil
}

func (s *fakeStore) Claim(_ context.Context, max int, _ time.Duration) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Job
	for id := int64(1); id <= s.next; id++ { // deterministic order
		j, ok := s.jobs[id]
		if !ok {
			continue
		}
		j.Attempts++
		out = append(out, *j)
		if len(out) >= max {
			break
		}
	}
	return out, nil
}

func (s *fakeStore) Complete(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	s.completed = append(s.completed, id)
	return nil
}

func (s *fakeStore) Retry(_ context.Context, id int64, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried = append(s.retried, id)
	return nil // keep the job claimable for the next iteration
}

func (s *fakeStore) remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

// fakeRebuilder records the users it (re)built and can be told to fail.
type fakeRebuilder struct {
	mu    sync.Mutex
	built []string
	err   error
}

func (r *fakeRebuilder) Build(_ context.Context, userID string) (domain.SubscriptionSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return domain.SubscriptionSet{}, r.err
	}
	r.built = append(r.built, userID)
	return domain.SubscriptionSet{}, nil
}

// fakeSets records node fan-out calls.
type fakeSets struct {
	users   []string
	dirtied []string
}

func (s *fakeSets) UserIDsByNode(_ context.Context, nodeID string) ([]string, error) {
	return s.users, nil
}
func (s *fakeSets) MarkDirtyByNode(_ context.Context, nodeID string) error {
	s.dirtied = append(s.dirtied, nodeID)
	return nil
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestDurableUserJobBuildsAndCompletes(t *testing.T) {
	store := newFakeStore()
	rb := &fakeRebuilder{}
	q := NewDurable(store, &fakeSets{}, rb, DurableConfig{}, quietLogger())

	q.EnqueueUser("u1")
	n, err := q.claimAndProcess(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("claimed = %d, want 1", n)
	}
	if len(rb.built) != 1 || rb.built[0] != "u1" {
		t.Fatalf("built = %v, want [u1]", rb.built)
	}
	if store.remaining() != 0 {
		t.Fatalf("job should be completed, %d remain", store.remaining())
	}
}

func TestDurableNodeJobFansOut(t *testing.T) {
	store := newFakeStore()
	rb := &fakeRebuilder{}
	sets := &fakeSets{users: []string{"u1", "u2"}}
	q := NewDurable(store, sets, rb, DurableConfig{}, quietLogger())

	q.EnqueueNode("n1")
	if _, err := q.claimAndProcess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sets.dirtied) != 1 || sets.dirtied[0] != "n1" {
		t.Fatalf("node not invalidated: %v", sets.dirtied)
	}
	if len(rb.built) != 2 {
		t.Fatalf("expected both affected users rebuilt, got %v", rb.built)
	}
	if store.remaining() != 0 {
		t.Fatalf("node job should be completed, %d remain", store.remaining())
	}
}

func TestDurableRetriesThenDrops(t *testing.T) {
	store := newFakeStore()
	rb := &fakeRebuilder{err: errors.New("boom")} // always fails
	q := NewDurable(store, &fakeSets{}, rb, DurableConfig{MaxAttempts: 2}, quietLogger())

	q.EnqueueUser("u1")

	// Attempt 1 (attempts=1 < 2): retried, job kept.
	if _, err := q.claimAndProcess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.retried) != 1 || store.remaining() != 1 {
		t.Fatalf("first failure should retry and keep the job: retried=%v remain=%d", store.retried, store.remaining())
	}

	// Attempt 2 (attempts=2 >= 2): dropped via Complete.
	if _, err := q.claimAndProcess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.remaining() != 0 {
		t.Fatalf("poison job should be dropped after max attempts, %d remain", store.remaining())
	}
	if len(store.completed) != 1 {
		t.Fatalf("drop should Complete the job once, got %v", store.completed)
	}
}
