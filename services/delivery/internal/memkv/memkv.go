// Package memkv is a tiny in-memory, concurrency-safe blob store. It backs the
// messenger channel's blob store in single-process/dev deployments; production
// swaps in an external store implementing the same Put/Get shape.
package memkv

import (
	"context"
	"sync"

	"github.com/caspervpn/delivery/internal/channel"
)

// Store is a concurrency-safe key -> blob map.
type Store struct {
	mu sync.RWMutex
	m  map[string][]byte
}

// New builds an empty store.
func New() *Store { return &Store{m: map[string][]byte{}} }

// Put stores a copy of blob under key (immutable at the boundary).
func (s *Store) Put(_ context.Context, key string, blob []byte) error {
	cp := make([]byte, len(blob))
	copy(cp, blob)
	s.mu.Lock()
	s.m[key] = cp
	s.mu.Unlock()
	return nil
}

// Get returns a copy of the blob for key, or channel.ErrNotFound.
func (s *Store) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	blob, ok := s.m[key]
	s.mu.RUnlock()
	if !ok {
		return nil, channel.ErrNotFound
	}
	cp := make([]byte, len(blob))
	copy(cp, blob)
	return cp, nil
}
