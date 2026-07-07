package steg

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/caspervpn/delivery/internal/channel"
)

// uuidFormRe matches a canonical 8-4-4-4-12 hex id (the shape real analytics
// message ids occupy) — the covert carrier must look like this, not like the old
// fixed-length base64 tell.
var uuidFormRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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
		if _, ok := probe["schema"].(string); !ok || probe["schema"] == "" {
			t.Fatalf("cover missing schema marker")
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

// TestCarrierIDsAreUUIDForm asserts the covert carrier ids match the UUID grammar
// real analytics batches carry (B9: kills the fixed-length base64 statistical tell).
func TestCarrierIDsAreUUIDForm(t *testing.T) {
	cover, err := Embed([]byte("some sealed pointer payload spanning chunks"), "anon-1")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	var c struct {
		Batch []struct {
			MessageID string `json:"messageId"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(cover, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(c.Batch) == 0 {
		t.Fatal("no batch events emitted")
	}
	for i, e := range c.Batch {
		if !uuidFormRe.MatchString(e.MessageID) {
			t.Fatalf("batch[%d].messageId %q is not UUID-form", i, e.MessageID)
		}
	}
}

// TestCoverTemplateRotates asserts the (schema, library, version) tuple is not a
// single stable signature across emissions (B9: no one template to block on).
func TestCoverTemplateRotates(t *testing.T) {
	seen := map[string]struct{}{}
	blob := []byte("x")
	for i := 0; i < 200; i++ {
		cover, err := Embed(blob, "anon")
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		var c struct {
			Schema  string `json:"schema"`
			Context struct {
				Library struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"library"`
			} `json:"context"`
		}
		if err := json.Unmarshal(cover, &c); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		seen[c.Schema+"|"+c.Context.Library.Name+"|"+c.Context.Library.Version] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("cover template did not rotate across 200 emissions (saw %d distinct)", len(seen))
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
