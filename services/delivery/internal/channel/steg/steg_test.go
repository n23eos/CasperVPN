package steg

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/caspervpn/delivery/internal/channel"
)

func TestEmbedExtractRoundTrip(t *testing.T) {
	for _, blob := range [][]byte{
		[]byte("p"),
		[]byte("a longer sealed+signed pointer payload that spans several chunks!!"),
		bytes.Repeat([]byte{0x01, 0x02, 0x03}, 50),
	} {
		cover, err := Embed(blob, "anon-123")
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		// Cover must be valid JSON that looks like an analytics batch.
		var probe map[string]interface{}
		if err := json.Unmarshal(cover, &probe); err != nil {
			t.Fatalf("cover is not valid JSON: %v", err)
		}
		if probe["schema"] != "cdp.event.v1" {
			t.Fatalf("cover missing plausible schema marker")
		}
		got, err := Extract(cover)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if !bytes.Equal(got, blob) {
			t.Fatalf("round-trip mismatch for %d bytes", len(blob))
		}
	}
}

func TestExtractRejectsNonCover(t *testing.T) {
	if _, err := Extract([]byte(`{"unrelated":true}`)); err == nil {
		t.Fatalf("expected error extracting from a cover with no batch")
	}
	if _, err := Extract([]byte("not json")); err == nil {
		t.Fatalf("expected error for non-JSON cover")
	}
}

// fakeCarrier is an in-memory cover store.
type fakeCarrier struct{ covers map[string][]byte }

func newFakeCarrier() *fakeCarrier { return &fakeCarrier{covers: map[string][]byte{}} }

func (f *fakeCarrier) PutCover(_ context.Context, key string, cover []byte) error {
	f.covers[key] = cover
	return nil
}
func (f *fakeCarrier) GetCover(_ context.Context, key string) ([]byte, error) {
	c, ok := f.covers[key]
	if !ok {
		return nil, channel.ErrNotFound
	}
	return c, nil
}

func TestStegChannelPublishFetchRoundTrip(t *testing.T) {
	ch, err := New(newFakeCarrier())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	blob := []byte("signed-directory-pointer")
	if err := ch.Publish(context.Background(), "directory", blob); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, err := ch.Fetch(context.Background(), "directory")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("round-trip mismatch")
	}
	if ch.Kind() != channel.KindSteg {
		t.Fatalf("unexpected kind %q", ch.Kind())
	}
}
