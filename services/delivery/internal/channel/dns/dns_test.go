package dns

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caspervpn/delivery/internal/channel"
)

func TestEncodeDecodeTXTRoundTrip(t *testing.T) {
	for _, blob := range [][]byte{
		[]byte("short"),
		bytes.Repeat([]byte{0xAB}, 400), // forces multi-string split (>255)
	} {
		strs := EncodeTXT(blob)
		got, err := DecodeTXT(strs)
		if err != nil {
			t.Fatalf("DecodeTXT(%d bytes): %v", len(blob), err)
		}
		if !bytes.Equal(got, blob) {
			t.Fatalf("round-trip mismatch for %d bytes", len(blob))
		}
		for _, s := range strs {
			if len(s) > txtStringMax {
				t.Fatalf("TXT string exceeds %d octets", txtStringMax)
			}
		}
	}
}

func TestQNameRespectsLabelLimit(t *testing.T) {
	name, err := QName("cfg.example.", strings.Repeat("k", 100))
	if err != nil {
		t.Fatalf("QName: %v", err)
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) > labelMax {
			t.Fatalf("label %q exceeds %d", label, labelMax)
		}
	}
	if !strings.HasSuffix(name, ".cfg.example") {
		t.Fatalf("name %q missing zone suffix", name)
	}
}

// fakeDNS is an in-memory TXT store implementing both Lookup and Publisher.
type fakeDNS struct{ recs map[string][]string }

func newFakeDNS() *fakeDNS { return &fakeDNS{recs: map[string][]string{}} }

func (f *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	s, ok := f.recs[name]
	if !ok {
		return nil, channel.ErrNotFound
	}
	return s, nil
}
func (f *fakeDNS) SetTXT(_ context.Context, name string, strs []string) error {
	f.recs[name] = strs
	return nil
}

func TestDNSChannelPublishFetchRoundTrip(t *testing.T) {
	f := newFakeDNS()
	for _, mk := range []struct {
		name string
		ctor func() (*Channel, error)
	}{
		{"txt", func() (*Channel, error) { return NewTXT("cfg.example.", f, f) }},
		{"doh", func() (*Channel, error) { return NewDoH("cfg.example.", f, f) }},
	} {
		ch, err := mk.ctor()
		if err != nil {
			t.Fatalf("%s ctor: %v", mk.name, err)
		}
		blob := []byte("signed-directory-blob")
		if err := ch.Publish(context.Background(), "directory", blob); err != nil {
			t.Fatalf("%s Publish: %v", mk.name, err)
		}
		got, err := ch.Fetch(context.Background(), "directory")
		if err != nil {
			t.Fatalf("%s Fetch: %v", mk.name, err)
		}
		if !bytes.Equal(got, blob) {
			t.Fatalf("%s round-trip mismatch", mk.name)
		}
	}
}

func TestDNSFetchMissingIsNotFound(t *testing.T) {
	ch, _ := NewTXT("cfg.example.", newFakeDNS(), nil)
	if _, err := ch.Fetch(context.Background(), "absent"); err == nil {
		t.Fatalf("expected not-found error")
	}
}

// fakeHTTP returns a canned DoH JSON response for any request.
type fakeHTTP struct{ body string }

func (f fakeHTTP) Do(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

func TestDNSChannelErrorPaths(t *testing.T) {
	// Fetch-only channel: Publish must error.
	ro, _ := NewTXT("cfg.example.", newFakeDNS(), nil)
	if err := ro.Publish(context.Background(), "k", []byte("x")); err == nil {
		t.Fatalf("fetch-only Publish should error")
	}
	// Constructor validation.
	if _, err := NewTXT("", newFakeDNS(), nil); err == nil {
		t.Fatalf("empty zone should error")
	}
	if _, err := NewDoH("z", nil, nil); err == nil {
		t.Fatalf("nil lookup should error")
	}
	// QName empty zone.
	if _, err := QName("", "k"); err == nil {
		t.Fatalf("QName empty zone should error")
	}
	// DecodeTXT bad base32.
	if _, err := DecodeTXT([]string{"!!!!"}); err == nil {
		t.Fatalf("bad base32 should error")
	}
}

func TestDNSChannelHealthy(t *testing.T) {
	f := newFakeDNS()
	ch, _ := NewTXT("cfg.example.", f, f)
	// No record for the health name => LookupTXT returns ErrNotFound => unhealthy.
	if ch.Healthy(context.Background()) {
		t.Fatalf("expected unhealthy when health lookup fails")
	}
	// Healthy probes "health."+zone (raw zone, incl. trailing dot).
	f.recs["health.cfg.example."] = []string{"ok"}
	if !ch.Healthy(context.Background()) {
		t.Fatalf("expected healthy when health record resolves")
	}
}

func TestDoHClientParsesTXTAnswers(t *testing.T) {
	// The DoH client must extract TXT strings and skip non-TXT answers.
	body := `{"Answer":[{"type":16,"data":"\"abc\" \"def\""},{"type":5,"data":"cname.ignored"}]}`
	c := DoHClient{Endpoint: "https://resolver.example/dns-query", HTTP: fakeHTTP{body: body}}
	strs, err := c.LookupTXT(context.Background(), "x.cfg.example")
	if err != nil {
		t.Fatalf("LookupTXT: %v", err)
	}
	if len(strs) != 2 || strs[0] != "abc" || strs[1] != "def" {
		t.Fatalf("unexpected TXT strings: %#v", strs)
	}
}
