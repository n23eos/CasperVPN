package httpapi

import (
	"net/http"
)

// ingestSignalAggregates accepts FieldSignal aggregates from telemetry and marks
// nodes degraded/blocked, triggering async rebuilds. Additive endpoint.
func (h *Handler) ingestSignalAggregates(w http.ResponseWriter, r *http.Request) {
	var req signalAggregateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if len(req.Aggregates) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "no aggregates")
		return
	}
	aggs, err := toAggregates(req.Aggregates)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	changes, err := h.signals.Ingest(r.Context(), aggs, actorFromContext(r.Context()))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, signalAggregateResponse{Applied: len(aggs), Changes: changes})
}
