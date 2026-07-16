package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// getNodeRealityUsers serves GET /v1/nodes/{id}/reality-users — the REALITY
// allow-list a node must admit (eligible users + a revision digest). The body
// carries live admission credentials (uuid/short_id), so it is set no-store and
// restricted to the orchestrator/admin roles at the router. No private key or PII
// is included (RealityUser is uuid + short_id only).
func (h *Handler) getNodeRealityUsers(w http.ResponseWriter, r *http.Request) {
	res, err := h.allow.ForNode(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, res)
}
