package gitraw

import (
	"context"
	"errors"
	"testing"

	"github.com/caspervpn/delivery/internal/channel"
)

// fakeRaw is an in-memory mirror set: path -> blob, with per-mirror outages.
type fakeRaw struct {
	files map[string][]byte
	down  map[string]bool // base -> unreachable
	gets  int
}

func newFakeRaw() *fakeRaw {
	return &fakeRaw{files: map[string][]byte{}, down: map[string]bool{}}
}

func (f *fakeRaw) Get(_ context.Context, base, path string) ([]byte, error) {
	f.gets++
	if f.down[base] {
		return nil, errors.New("mirror down")
	}
	b, ok := f.files[path]
	if !ok {
		return nil, channel.ErrNotFound
	}
	return b, nil
}

// Put writes to the shared file map (all mirrors serve the same origin).
func (f *fakeRaw) Put(_ context.Context, path string, blob []byte) error {
	f.files[path] = blob
	return nil
}

func TestGitRawPublishFetchRoundTrip(t *testing.T) {
	f := newFakeRaw()
	ch, err := New([]string{"https://m1", "https://m2"}, f, f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	blob := []byte("signed-blob")
	if err := ch.Publish(context.Background(), "tok1", blob); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, err := ch.Fetch(context.Background(), "tok1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != string(blob) {
		t.Fatalf("round-trip mismatch: got %q", got)
	}
}

func TestGitRawFailsOverToNextMirror(t *testing.T) {
	f := newFakeRaw()
	ch, _ := New([]string{"https://m1", "https://m2"}, f, f)
	_ = ch.Publish(context.Background(), "tok1", []byte("x"))
	f.down["https://m1"] = true // first mirror blocked

	// Every Fetch must still succeed via m2 regardless of rotation start.
	for i := 0; i < 4; i++ {
		if _, err := ch.Fetch(context.Background(), "tok1"); err != nil {
			t.Fatalf("Fetch %d should fail over to m2: %v", i, err)
		}
	}
}

func TestGitRawAllMirrorsDownIsNotFound(t *testing.T) {
	f := newFakeRaw()
	ch, _ := New([]string{"https://m1", "https://m2"}, f, f)
	_ = ch.Publish(context.Background(), "tok1", []byte("x"))
	f.down["https://m1"] = true
	f.down["https://m2"] = true
	if _, err := ch.Fetch(context.Background(), "tok1"); !errors.Is(err, channel.ErrNotFound) {
		t.Fatalf("expected ErrNotFound when all mirrors down, got %v", err)
	}
}

func TestGitRawFetchOnlyPublishErrors(t *testing.T) {
	f := newFakeRaw()
	ch, _ := New([]string{"https://m1"}, f, nil) // no writer
	if err := ch.Publish(context.Background(), "k", []byte("x")); err == nil {
		t.Fatalf("expected error publishing on fetch-only channel")
	}
}

func TestGitRawNewValidation(t *testing.T) {
	if _, err := New(nil, newFakeRaw(), nil); err == nil {
		t.Fatalf("expected error for no mirrors")
	}
	if _, err := New([]string{"m"}, nil, nil); err == nil {
		t.Fatalf("expected error for nil transport")
	}
}
