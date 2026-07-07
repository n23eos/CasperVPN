package ingest

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/telemetry/internal/metrics"
	"github.com/caspervpn/telemetry/internal/store"
)

// approxSignalBytes bounds body size per signal so a batch of maxBatch cannot be
// used to exhaust memory (defence-in-depth alongside maxBatch).
const approxSignalBytes = 512

// Ingestor handles anonymous signal + authoritative health ingest. It owns the
// anonymity/anti-poisoning pipeline: rate-limit → decode(strict) → sanitize → dedup.
type Ingestor struct {
	store   store.Store
	limiter *RateLimiter
	dedup   *Deduper
	metrics *metrics.Collector

	maxBatch     int
	retention    time.Duration
	maxBodyBytes int64
	now          func() time.Time
}

// NewIngestor wires the ingest pipeline. now is injectable for tests.
func NewIngestor(s store.Store, l *RateLimiter, d *Deduper, m *metrics.Collector, maxBatch int, retention time.Duration, now func() time.Time) *Ingestor {
	if now == nil {
		now = time.Now
	}
	return &Ingestor{
		store: s, limiter: l, dedup: d, metrics: m,
		maxBatch:     maxBatch,
		retention:    retention,
		maxBodyBytes: int64(maxBatch*approxSignalBytes) + 1024,
		now:          now,
	}
}

type signalsRequest struct {
	Signals []contracts.FieldSignal `json:"signals"`
}

type ingestResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
}

// HandleSignals implements POST /v1/signals — anonymous field-signal batch ingest.
func (in *Ingestor) HandleSignals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req signalsRequest
	if code, err := decodeStrict(w, r, in.maxBodyBytes, &req); err != nil {
		writeErr(w, code, err.Error())
		return
	}
	if len(req.Signals) == 0 {
		writeErr(w, http.StatusBadRequest, "empty batch")
		return
	}
	if len(req.Signals) > in.maxBatch {
		writeErr(w, http.StatusRequestEntityTooLarge, "batch too large")
		return
	}

	// Rate-limit by an ephemeral, salted token of the coarse origin. The raw origin
	// is used only to derive the token and is never stored.
	token := in.limiter.SourceToken(clientOrigin(r))
	if !in.limiter.Allow(token, len(req.Signals)) {
		in.metrics.IncRateLimited()
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}

	now := in.now()
	accepted := make([]contracts.FieldSignal, 0, len(req.Signals))
	rejected := 0
	for _, raw := range req.Signals {
		clean, ok, reason := Sanitize(raw, now, in.retention)
		if !ok {
			rejected++
			in.metrics.IncRejected(bucketReason(reason))
			continue
		}
		if !in.dedup.Fresh(clean.SignalID) {
			rejected++
			in.metrics.IncRejected("duplicate")
			continue
		}
		accepted = append(accepted, clean)
		in.metrics.IncIngested(string(clean.TransportType), clean.Region, string(clean.SignalType))
	}

	if len(accepted) > 0 {
		if err := in.store.WriteSignals(r.Context(), accepted); err != nil {
			writeErr(w, http.StatusInternalServerError, "storage error")
			return
		}
	}
	writeJSON(w, http.StatusAccepted, ingestResponse{Accepted: len(accepted), Rejected: rejected})
}

type healthRequest struct {
	Events []contracts.HealthEvent `json:"events"`
}

// HandleHealth implements POST /v1/health — authoritative orchestrator probe events.
// Authentication is enforced by the router (internal bearer token).
func (in *Ingestor) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req healthRequest
	if code, err := decodeStrict(w, r, in.maxBodyBytes, &req); err != nil {
		writeErr(w, code, err.Error())
		return
	}
	if len(req.Events) == 0 {
		writeErr(w, http.StatusBadRequest, "empty batch")
		return
	}
	if len(req.Events) > in.maxBatch {
		writeErr(w, http.StatusRequestEntityTooLarge, "batch too large")
		return
	}

	now := in.now()
	accepted := make([]contracts.HealthEvent, 0, len(req.Events))
	rejected := 0
	for _, e := range req.Events {
		if e.ObservedAt.IsZero() {
			e.ObservedAt = now
		}
		if err := e.Validate(); err != nil {
			rejected++
			continue
		}
		accepted = append(accepted, e)
	}

	if len(accepted) > 0 {
		if err := in.store.WriteHealth(r.Context(), accepted); err != nil {
			writeErr(w, http.StatusInternalServerError, "storage error")
			return
		}
		in.metrics.AddHealth(len(accepted))
	}
	writeJSON(w, http.StatusAccepted, ingestResponse{Accepted: len(accepted), Rejected: rejected})
}

// decodeStrict reads a size-capped body and decodes JSON with unknown fields
// forbidden — so no extra/PII field can be smuggled past the schema. Returns an
// HTTP status code on failure.
func decodeStrict(w http.ResponseWriter, r *http.Request, maxBytes int64, dst interface{}) (int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return http.StatusRequestEntityTooLarge, errors.New("body too large")
		}
		return http.StatusBadRequest, errors.New("malformed batch: " + err.Error())
	}
	if dec.More() {
		return http.StatusBadRequest, errors.New("trailing data after JSON body")
	}
	return http.StatusOK, nil
}

// clientOrigin returns the coarse origin used ONLY to derive an ephemeral
// rate-limit token. Never logged, never stored.
func clientOrigin(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}

// bucketReason collapses a rejection reason into a low-cardinality metric label.
func bucketReason(reason string) string {
	switch {
	case strings.Contains(reason, "region"):
		return "invalid_region"
	case strings.Contains(reason, "timestamp"):
		return "stale_or_future"
	case strings.Contains(reason, "signal_id"):
		return "missing_signal_id"
	default:
		return "invalid"
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
