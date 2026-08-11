package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/telemetry/internal/aggregate"
	"github.com/caspervpn/telemetry/internal/config"
	"github.com/caspervpn/telemetry/internal/ingest"
	"github.com/caspervpn/telemetry/internal/metrics"
	"github.com/caspervpn/telemetry/internal/query"
	"github.com/caspervpn/telemetry/internal/store"
)

func testParams() config.VerdictParams {
	return config.VerdictParams{
		MinSources: 5, MinBlockedSources: 4, DeadShareThresh: 0.70,
		MaxOKShareForDead: 0.25, DegradedThresh: 0.40, SpikeDelta: 0.30, SpikeMinSources: 3,
	}
}

func newServer(token string) http.Handler {
	now := func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) }
	st := store.NewMemoryStore(0)
	coll := metrics.NewCollector()
	limiter := ingest.NewRateLimiter(100000, 100000, 1000000, 1000000, now)
	dedup := ingest.NewDeduper(30*time.Minute, now)
	in := ingest.NewIngestor(st, limiter, dedup, coll, 1000, 72*time.Hour, now)
	q := query.NewHandler(st, 15*time.Minute, testParams(), now)
	return Router(in, q, coll, token)
}

// TestLoop_IngestToRecommendation drives the whole feedback loop: 6 independent
// sources report a DPI block for one node/transport, and the recommendations API
// then returns an actionable mark_node_blocked — the core acceptance criterion.
func TestLoop_IngestToRecommendation(t *testing.T) {
	srv := newServer("loop-token")

	var items []string
	for asn := 1; asn <= 6; asn++ {
		items = append(items, fmt.Sprintf(
			`{"signal_id":"s%d","node_id":"node-x","transport_type":"vless-reality",`+
				`"region":"RU-MOW","asn":%d,"signal_type":"dpi_block","observed_at":"2026-07-07T11:59:00Z"}`,
			asn, asn))
	}
	body := `{"signals":[` + strings.Join(items, ",") + `]}`

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/signals", strings.NewReader(body)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("ingest status = %d, want 202; body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/recommendations", nil)
	r.Header.Set("Authorization", "Bearer loop-token")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("recommendations status = %d, want 200", w.Code)
	}
	var recs aggregate.Recommendations
	if err := json.Unmarshal(w.Body.Bytes(), &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs.NodeBlocks) != 1 || recs.NodeBlocks[0].NodeID != "node-x" {
		t.Fatalf("want mark_node_blocked for node-x, got %+v", recs.NodeBlocks)
	}
	if recs.NodeBlocks[0].Action != aggregate.ActionMarkNodeBlocked {
		t.Fatalf("unexpected action %q", recs.NodeBlocks[0].Action)
	}
}

func TestAuth_InternalEndpointsRequireToken(t *testing.T) {
	srv := newServer("secret-token")

	// Public ingest needs no token.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", w.Code)
	}

	// Recommendations without token → 401.
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/recommendations", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no-token recommendations = %d, want 401", w.Code)
	}

	// With correct token → 200.
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/recommendations", nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("valid-token recommendations = %d, want 200", w.Code)
	}
}

// TestAuth_EmptyTokenFailsClosed guards the fail-closed policy: an unset
// internal token must disable the internal surface, not open it — HealthEvents
// feed node rotation, so pass-through would let anyone poison the loop.
func TestAuth_EmptyTokenFailsClosed(t *testing.T) {
	srv := newServer("")

	for _, path := range []string{"/v1/health", "/v1/aggregates", "/v1/recommendations", "/metrics"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s with empty configured token = %d, want 403", path, w.Code)
		}
	}

	// Public surface stays open.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", w.Code)
	}
}

func TestMetrics_Exposed(t *testing.T) {
	srv := newServer("metrics-token")
	// Ingest one signal so a counter exists.
	body := `{"signals":[{"signal_id":"m1","node_id":"n1","transport_type":"hysteria2",` +
		`"region":"RU-MOW","signal_type":"ok","observed_at":"2026-07-07T11:59:00Z"}]}`
	srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/signals", strings.NewReader(body)))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("Authorization", "Bearer metrics-token")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("metrics = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "telemetry_signals_ingested_total") {
		t.Fatalf("metrics missing ingest counter:\n%s", w.Body.String())
	}
}

// TestPrivacy_NoRawIPInStore is a guard: ingest must not persist the client IP.
// The store only holds contracts.FieldSignal, which has no IP field — this test
// documents/enforces that the wire type stays IP-free.
func TestPrivacy_ContractHasNoIPField(t *testing.T) {
	var s contracts.FieldSignal
	b, _ := json.Marshal(s)
	if strings.Contains(strings.ToLower(string(b)), "ip") {
		t.Fatalf("FieldSignal JSON unexpectedly mentions an IP field: %s", b)
	}
}
