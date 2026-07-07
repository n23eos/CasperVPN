package ingest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/telemetry/internal/metrics"
	"github.com/caspervpn/telemetry/internal/store"
)

func newTestIngestor(maxBatch int, limiter *RateLimiter) (*Ingestor, *store.MemoryStore) {
	now := func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) }
	st := store.NewMemoryStore(0)
	if limiter == nil {
		limiter = NewRateLimiter(1000, 1000, 100000, 100000, now)
	}
	dedup := NewDeduper(30*time.Minute, now)
	in := NewIngestor(st, limiter, dedup, metrics.NewCollector(), maxBatch, 72*time.Hour, now)
	return in, st
}

func post(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/signals", strings.NewReader(body))
	r.RemoteAddr = "1.2.3.4:5555"
	return r
}

const validSignal = `{"signal_id":"s1","node_id":"n1","transport_type":"vless-reality",` +
	`"region":"RU-MOW","signal_type":"reset","observed_at":"2026-07-07T11:59:00Z"}`

func TestHandleSignals_Accepts(t *testing.T) {
	in, st := newTestIngestor(1000, nil)
	w := httptest.NewRecorder()
	in.HandleSignals(w, post(`{"signals":[`+validSignal+`]}`))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var resp ingestResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Accepted != 1 || resp.Rejected != 0 {
		t.Fatalf("accepted/rejected = %d/%d, want 1/0", resp.Accepted, resp.Rejected)
	}
	got, _ := st.SignalsSince(nil, time.Time{})
	if len(got) != 1 {
		t.Fatalf("store has %d signals, want 1", len(got))
	}
}

func TestHandleSignals_RejectsUnknownField(t *testing.T) {
	in, _ := newTestIngestor(1000, nil)
	w := httptest.NewRecorder()
	// user_id is an unknown field — a PII smuggling attempt. Strict decode must 400.
	body := `{"signals":[{"signal_id":"s1","node_id":"n1","transport_type":"vless-reality",` +
		`"region":"RU-MOW","signal_type":"reset","user_id":"alice@example.com"}]}`
	in.HandleSignals(w, post(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown field rejected)", w.Code)
	}
}

func TestHandleSignals_EmptyBatch(t *testing.T) {
	in, _ := newTestIngestor(1000, nil)
	w := httptest.NewRecorder()
	in.HandleSignals(w, post(`{"signals":[]}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleSignals_TooLarge(t *testing.T) {
	in, _ := newTestIngestor(1, nil) // maxBatch=1
	w := httptest.NewRecorder()
	in.HandleSignals(w, post(`{"signals":[`+validSignal+`,`+validSignal+`]}`))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
}

func TestHandleSignals_RateLimited(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) }
	blocked := NewRateLimiter(0, 0, 0, 0, now) // no tokens ever
	in, _ := newTestIngestor(1000, blocked)
	w := httptest.NewRecorder()
	in.HandleSignals(w, post(`{"signals":[`+validSignal+`]}`))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
}

func TestHandleSignals_DedupCountsRejected(t *testing.T) {
	in, _ := newTestIngestor(1000, nil)
	body := `{"signals":[` + validSignal + `,` + validSignal + `]}` // same signal_id twice
	w := httptest.NewRecorder()
	in.HandleSignals(w, post(body))
	var resp ingestResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Accepted != 1 || resp.Rejected != 1 {
		t.Fatalf("accepted/rejected = %d/%d, want 1/1 (replay deduped)", resp.Accepted, resp.Rejected)
	}
}

func TestHandleHealth_Accepts(t *testing.T) {
	in, st := newTestIngestor(1000, nil)
	body := `{"events":[{"node_id":"n1","status":"blocked","probe_source":"ru-probe-1",` +
		`"blocked_from_regions":["RU-MOW"],"observed_at":"2026-07-07T11:59:00Z"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/health", strings.NewReader(body))
	w := httptest.NewRecorder()
	in.HandleHealth(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	got, _ := st.HealthSince(nil, time.Time{})
	if len(got) != 1 {
		t.Fatalf("store has %d health events, want 1", len(got))
	}
}
