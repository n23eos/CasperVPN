package gitraw

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caspervpn/delivery/internal/channel"
)

// stubDoer returns a canned response/status for any request, recording the URL.
type stubDoer struct {
	status  int
	body    string
	lastURL string
	err     error
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	s.lastURL = req.URL.String()
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     make(http.Header),
	}, nil
}

func TestHTTPTransportGetOK(t *testing.T) {
	d := &stubDoer{status: http.StatusOK, body: "filebytes"}
	tr := HTTPTransport{HTTP: d}
	got, err := tr.Get(context.Background(), "https://raw.example/repo/", "/a/b.json")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "filebytes" {
		t.Fatalf("body = %q", got)
	}
	if d.lastURL != "https://raw.example/repo/a/b.json" {
		t.Fatalf("joined URL = %q", d.lastURL)
	}
}

func TestHTTPTransport404IsNotFound(t *testing.T) {
	tr := HTTPTransport{HTTP: &stubDoer{status: http.StatusNotFound}}
	if _, err := tr.Get(context.Background(), "https://m", "p"); !errors.Is(err, channel.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestHTTPTransport500IsError(t *testing.T) {
	tr := HTTPTransport{HTTP: &stubDoer{status: http.StatusInternalServerError}}
	if _, err := tr.Get(context.Background(), "https://m", "p"); err == nil || errors.Is(err, channel.ErrNotFound) {
		t.Fatalf("expected non-notfound error, got %v", err)
	}
}

func TestHTTPTransportTransportError(t *testing.T) {
	tr := HTTPTransport{HTTP: &stubDoer{err: errors.New("dial fail")}}
	if _, err := tr.Get(context.Background(), "https://m", "p"); err == nil {
		t.Fatalf("expected transport error")
	}
}

func TestChannelHealthy(t *testing.T) {
	// Reachable mirror (200/404) => healthy; transport error => unhealthy.
	f := newFakeRaw()
	ch, _ := New([]string{"https://m1"}, f, f)
	if !ch.Healthy(context.Background()) {
		t.Fatalf("expected healthy when mirror answers not-found")
	}
	f.down["https://m1"] = true
	if ch.Healthy(context.Background()) {
		t.Fatalf("expected unhealthy when mirror errors")
	}
}
