package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caspervpn/delivery/internal/channel"
)

// memChannel is a minimal in-memory channel for handler tests.
type memChannel struct {
	kind channel.Kind
	m    map[string][]byte
}

func (c *memChannel) Kind() channel.Kind { return c.kind }
func (c *memChannel) Publish(_ context.Context, k string, b []byte) error {
	c.m[k] = b
	return nil
}
func (c *memChannel) Fetch(_ context.Context, k string) ([]byte, error) {
	b, ok := c.m[k]
	if !ok {
		return nil, channel.ErrNotFound
	}
	return b, nil
}
func (c *memChannel) Healthy(context.Context) bool { return true }

func newTestAPI() (*API, *memChannel) {
	reg := channel.NewRegistry()
	ch := &memChannel{kind: channel.KindTelegram, m: map[string][]byte{}}
	reg.Add(ch, 0)
	return New(reg), ch
}

func TestHealthz(t *testing.T) {
	api, _ := newTestAPI()
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
}

func TestListChannels(t *testing.T) {
	api, _ := newTestAPI()
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/channels", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var body struct {
		Items []channelDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Kind != "telegram" || !body.Items[0].Healthy {
		t.Fatalf("unexpected channels: %+v", body.Items)
	}
}

func TestFetchViaChannel(t *testing.T) {
	api, ch := newTestAPI()
	ch.m["tok1"] = []byte("signed-blob")

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/telegram/tok1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch = %d", rec.Code)
	}
	got, err := base64.StdEncoding.DecodeString(rec.Body.String())
	if err != nil {
		t.Fatalf("body not base64: %v", err)
	}
	if string(got) != "signed-blob" {
		t.Fatalf("wrong blob: %q", got)
	}
}

func TestFetchUnknownChannelIs404(t *testing.T) {
	api, _ := newTestAPI()
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/doh/tok1", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unregistered channel, got %d", rec.Code)
	}
}

func TestFetchMissingTokenIs404(t *testing.T) {
	api, _ := newTestAPI()
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/telegram/absent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for absent token, got %d", rec.Code)
	}
}

func TestCreateChannelValidates(t *testing.T) {
	api, _ := newTestAPI()

	// Unknown kind -> 400.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/channels", strings.NewReader(`{"kind":"pigeon","enabled":true}`))
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind should be 400, got %d", rec.Code)
	}

	// Existing kind -> 409.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/channels", strings.NewReader(`{"kind":"telegram","enabled":true}`))
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("existing kind should be 409, got %d", rec.Code)
	}

	// New known kind -> 201.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/channels", strings.NewReader(`{"kind":"doh","enabled":true,"endpoint":"https://r.example/dns-query"}`))
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("new known kind should be 201, got %d", rec.Code)
	}
}
