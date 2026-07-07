// Package api wires the telemetry HTTP surface: public anonymous ingest, internal
// authenticated ingest/read, health, and metrics.
package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/caspervpn/telemetry/internal/ingest"
	"github.com/caspervpn/telemetry/internal/metrics"
	"github.com/caspervpn/telemetry/internal/query"
)

// Router builds the full handler tree.
//
// Public (no auth): POST /v1/signals (anonymous clients/nodes), GET /healthz.
// Internal (bearer): POST /v1/health, GET /v1/aggregates, GET /v1/recommendations,
// GET /metrics. When internalToken is empty (dev only) the auth gate passes through.
func Router(in *ingest.Ingestor, q *query.Handler, m *metrics.Collector, internalToken string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/v1/signals", in.HandleSignals)

	auth := authMiddleware(internalToken)
	mux.Handle("/v1/health", auth(http.HandlerFunc(in.HandleHealth)))
	mux.Handle("/v1/aggregates", auth(http.HandlerFunc(q.HandleAggregates)))
	mux.Handle("/v1/recommendations", auth(http.HandlerFunc(q.HandleRecommendations)))
	mux.Handle("/metrics", auth(http.HandlerFunc(metricsHandler(q, m))))

	return mux
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","service":"telemetry"}`))
}

// metricsHandler renders OpenMetrics from live aggregates + cumulative counters.
func metricsHandler(q *query.Handler, m *metrics.Collector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		aggs, err := q.Aggregates(r.Context())
		if err != nil {
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(m.Render(aggs)))
	}
}

// authMiddleware enforces a bearer token in constant time. Empty configured token
// disables the gate (dev only; main logs a warning in that case).
func authMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
