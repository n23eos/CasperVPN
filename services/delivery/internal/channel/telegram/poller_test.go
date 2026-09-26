package telegram

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryUpdateStore struct {
	mu     sync.Mutex
	cursor int64
	status map[int64]string
}

func newMemoryUpdateStore() *memoryUpdateStore {
	return &memoryUpdateStore{status: make(map[int64]string)}
}

func (s *memoryUpdateStore) Cursor(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, nil
}

func (s *memoryUpdateStore) Begin(_ context.Context, updateID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status[updateID] == "done" {
		return false, nil
	}
	s.status[updateID] = "pending"
	return true, nil
}

func (s *memoryUpdateStore) Complete(_ context.Context, updateID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[updateID] = "done"
	if updateID > s.cursor {
		s.cursor = updateID
	}
	return nil
}

type repeatSource struct {
	updates []Update
	offsets chan int64
}

func (s *repeatSource) GetUpdates(ctx context.Context, offset int64, _ time.Duration) ([]Update, error) {
	select {
	case s.offsets <- offset:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return append([]Update(nil), s.updates...), nil
	}
}

type handlerFunc func(context.Context, Update) error

func (f handlerFunc) HandleUpdate(ctx context.Context, update Update) error { return f(ctx, update) }

func TestPollerRetriesTemporaryFailureWithoutAdvancingCursor(t *testing.T) {
	store := newMemoryUpdateStore()
	source := &repeatSource{updates: []Update{{UpdateID: 7}}, offsets: make(chan int64, 4)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	attempts := 0
	handler := handlerFunc(func(context.Context, Update) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary")
		}
		cancel()
		return nil
	})
	poller := NewPoller(source, handler, store, time.Millisecond, time.Millisecond)
	if err := poller.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if attempts != 2 || store.cursor != 7 || store.status[7] != "done" {
		t.Fatalf("attempts=%d cursor=%d status=%q", attempts, store.cursor, store.status[7])
	}
	first := <-source.offsets
	second := <-source.offsets
	if first != 1 || second != 1 {
		t.Fatalf("offsets before completion = %d, %d", first, second)
	}
}

func TestPollerRestartUsesDurableCursor(t *testing.T) {
	store := newMemoryUpdateStore()
	store.cursor = 7
	store.status[7] = "done"
	source := &repeatSource{updates: []Update{{UpdateID: 7}, {UpdateID: 8}}, offsets: make(chan int64, 2)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var handled []int64
	handler := handlerFunc(func(_ context.Context, update Update) error {
		handled = append(handled, update.UpdateID)
		cancel()
		return nil
	})
	if err := NewPoller(source, handler, store, time.Millisecond, time.Millisecond).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(handled) != 1 || handled[0] != 8 {
		t.Fatalf("handled after restart = %v", handled)
	}
	if offset := <-source.offsets; offset != 8 {
		t.Fatalf("restart offset = %d", offset)
	}
}

func TestPollerReadinessRequiresSuccessfulPoll(t *testing.T) {
	poller := NewPoller(&repeatSource{offsets: make(chan int64, 1)}, handlerFunc(func(context.Context, Update) error { return nil }), newMemoryUpdateStore(), time.Second, time.Millisecond)
	if err := poller.Ready(context.Background()); err == nil {
		t.Fatal("poller must be unready before the first successful poll")
	}
}
