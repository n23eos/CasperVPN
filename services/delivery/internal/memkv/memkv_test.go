package memkv

import (
	"context"
	"errors"
	"testing"

	"github.com/caspervpn/delivery/internal/channel"
)

func TestPutGetRoundTrip(t *testing.T) {
	s := New()
	if err := s.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("got %q", got)
	}
}

func TestGetMissingIsNotFound(t *testing.T) {
	s := New()
	if _, err := s.Get(context.Background(), "absent"); !errors.Is(err, channel.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoredBlobIsCopied(t *testing.T) {
	// Mutating the caller's slice after Put must not change the stored value
	// (immutability at the boundary).
	s := New()
	in := []byte("orig")
	_ = s.Put(context.Background(), "k", in)
	in[0] = 'X'
	got, _ := s.Get(context.Background(), "k")
	if string(got) != "orig" {
		t.Fatalf("stored blob was aliased, got %q", got)
	}
}
