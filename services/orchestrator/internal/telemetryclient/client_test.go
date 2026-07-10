package telemetryclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/orchestrator/internal/httpx"
)

func TestRecommendationsHappyPath(t *testing.T) {
	want := contracts.Recommendations{
		GeneratedAt:   time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
		WindowSeconds: 300,
		NodeBlocks: []contracts.NodeBlock{{
			Action: contracts.RecommendationMarkNodeBlocked, NodeID: "node-1",
			Regions: []string{"RU-MOW"}, Confidence: contracts.RecommendationAuthoritative, Reason: "probe",
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/recommendations" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	got, err := New(srv.URL, "tok", time.Second).Recommendations(context.Background())
	if err != nil {
		t.Fatalf("Recommendations() error: %v", err)
	}
	if len(got.NodeBlocks) != 1 || got.NodeBlocks[0].NodeID != "node-1" {
		t.Fatalf("payload mismatch: %+v", got)
	}
}

func TestRecommendationsAuthFailureNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "bad", time.Second).Recommendations(context.Background())
	if err == nil {
		t.Fatal("want error on 401")
	}
	var se *httpx.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Fatalf("want StatusError 401, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("401 retried %d times — 4xx must never be retried", calls.Load())
	}
}

func TestRecommendationsServerErrorRetriedFinitely(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "tok", time.Second).Recommendations(context.Background()); err == nil {
		t.Fatal("want error on persistent 500")
	}
	if n := calls.Load(); n < 2 || n > 5 {
		t.Fatalf("500 attempted %d times, want bounded retries (2..5)", n)
	}
}

func TestRecommendationsMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"generated_at": nonsense`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "tok", time.Second).Recommendations(context.Background()); err == nil {
		t.Fatal("want error on malformed JSON")
	}
}

func TestRecommendationsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := New(srv.URL, "tok", 50*time.Millisecond).Recommendations(ctx); err == nil {
		t.Fatal("want timeout error")
	}
}

func TestReportHealthPostsEventOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/health" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var ev contracts.HealthEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil || ev.NodeID != "node-1" {
			t.Errorf("bad body: %v %+v", err, ev)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	ev := contracts.HealthEvent{
		NodeID: "node-1", Status: contracts.HealthBlocked,
		ProbeSource: "orchestrator-local", ObservedAt: time.Now(),
	}
	if err := New(srv.URL, "tok", time.Second).ReportHealth(context.Background(), ev); err != nil {
		t.Fatalf("ReportHealth() error: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("POST sent %d times, want exactly 1 (non-idempotent)", calls.Load())
	}
}

func TestReportHealthRejectsInvalidEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("invalid event must not reach the wire")
	}))
	defer srv.Close()

	err := New(srv.URL, "tok", time.Second).ReportHealth(context.Background(), contracts.HealthEvent{})
	if err == nil {
		t.Fatal("want validation error")
	}
}
