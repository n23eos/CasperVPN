package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/caspervpn/contracts"
)

// WithOperatorSummary configures the bounded, secret-free admin overview.
func (h *Handler) WithOperatorSummary(read func(context.Context) (contracts.ControlPlaneOperatorSummary, error)) *Handler {
	h.operator = read
	return h
}

func (h *Handler) operatorSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.operator == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "operator overview unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	summary, err := h.operator(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "operator overview unavailable")
		return
	}
	writeJSON(w, http.StatusOK, summary)
}
