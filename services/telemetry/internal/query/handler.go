// Package query serves the control-plane read side of the feedback loop: current
// per-(region,transport) aggregates and ready-to-act rotation recommendations.
// These endpoints are internal (authenticated by the router).
package query

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/caspervpn/telemetry/internal/aggregate"
	"github.com/caspervpn/telemetry/internal/config"
	"github.com/caspervpn/telemetry/internal/store"
)

// Handler computes aggregates and recommendations on demand from the store.
type Handler struct {
	store   store.Store
	window  time.Duration
	verdict config.VerdictParams
	now     func() time.Time
}

// NewHandler builds the query handler. now is injectable for tests.
func NewHandler(s store.Store, window time.Duration, v config.VerdictParams, now func() time.Time) *Handler {
	if now == nil {
		now = time.Now
	}
	return &Handler{store: s, window: window, verdict: v, now: now}
}

// Aggregates computes the current windowed aggregates. Shared with the metrics
// endpoint so /metrics and /v1/aggregates always agree.
func (h *Handler) Aggregates(ctx context.Context) (map[aggregate.Key]aggregate.Aggregate, error) {
	now := h.now()
	sigs, err := h.store.SignalsSince(ctx, now.Add(-h.window))
	if err != nil {
		return nil, err
	}
	return aggregate.Compute(sigs, now, h.window, h.verdict), nil
}

// aggregateItem is the JSON shape for one aggregate (map keys aren't JSON-friendly,
// so the Key is flattened into fields).
type aggregateItem struct {
	Region          string  `json:"region"`
	Transport       string  `json:"transport"`
	WindowSeconds   int     `json:"window_seconds"`
	Total           int     `json:"total"`
	DistinctSources int     `json:"distinct_sources"`
	BlockedSources  int     `json:"blocked_sources"`
	BlockedShare    float64 `json:"blocked_share"`
	DegradedShare   float64 `json:"degraded_share"`
	OKShare         float64 `json:"ok_share"`
	ResetRate       float64 `json:"reset_rate"`
	ProbeRate       float64 `json:"probe_rate"`
	AvgRTTms        float64 `json:"avg_rtt_ms"`
	AvgLossPct      float64 `json:"avg_loss_pct"`
	Trend           float64 `json:"trend"`
	Spike           bool    `json:"spike"`
	Dead            bool    `json:"dead"`
	Degraded        bool    `json:"degraded"`
	Reason          string  `json:"reason"`
}

// HandleAggregates implements GET /v1/aggregates.
func (h *Handler) HandleAggregates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	aggs, err := h.Aggregates(r.Context())
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	items := make([]aggregateItem, 0, len(aggs))
	for k, a := range aggs {
		items = append(items, aggregateItem{
			Region: k.Region, Transport: string(k.Transport),
			WindowSeconds: a.WindowSeconds, Total: a.Total,
			DistinctSources: a.DistinctSources, BlockedSources: a.BlockedSources,
			BlockedShare: a.BlockedShare, DegradedShare: a.DegradedShare, OKShare: a.OKShare,
			ResetRate: a.ResetRate, ProbeRate: a.ProbeRate,
			AvgRTTms: a.AvgRTTms, AvgLossPct: a.AvgLossPct,
			Trend: a.Trend, Spike: a.Spike, Dead: a.Dead, Degraded: a.Degraded, Reason: a.Reason,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"generated_at": h.now().UTC(),
		"aggregates":   items,
	})
}

// HandleRecommendations implements GET /v1/recommendations — the orchestrator's
// input for node rotation and transport prioritization.
func (h *Handler) HandleRecommendations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := h.now()
	from := now.Add(-h.window)
	sigs, err := h.store.SignalsSince(r.Context(), from)
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	health, err := h.store.HealthSince(r.Context(), from)
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	recs := aggregate.Recommend(sigs, health, now, h.window, h.verdict)
	writeJSON(w, http.StatusOK, recs)
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
