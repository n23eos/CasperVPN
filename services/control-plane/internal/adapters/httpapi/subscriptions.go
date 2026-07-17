package httpapi

import (
	"net/http"

	"github.com/caspervpn/contracts"
	"github.com/go-chi/chi/v5"
)

func (h *Handler) createSubscription(w http.ResponseWriter, r *http.Request) {
	var req createSubscriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	res, err := h.subs.Create(r.Context(), req.UserID, req.Plan)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	// The plaintext token rides in Subscription.Token exactly once, here.
	writeJSON(w, http.StatusCreated, res.Subscription)
}

func (h *Handler) getSubscription(w http.ResponseWriter, r *http.Request) {
	sub, err := h.subs.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// patchSubscription implements PATCH /v1/subscriptions/{id} (additive Wave-2
// endpoint): a TRUE partial update of status/expires_at. Unblocks billing
// activation/renewal. The token is never returned on this path.
func (h *Handler) patchSubscription(w http.ResponseWriter, r *http.Request) {
	var patch contracts.SubscriptionPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	sub, err := h.subs.Patch(r.Context(), chi.URLParam(r, "id"), patch)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// rotateSubscriptionToken implements POST /v1/subscriptions/{id}/rotate-token:
// leaked-link revocation without touching the account. The new plaintext token
// rides in Subscription.Token exactly once, here.
func (h *Handler) rotateSubscriptionToken(w http.ResponseWriter, r *http.Request) {
	res, err := h.subs.RotateToken(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res.Subscription)
}

// cancelSubscription implements POST /v1/subscriptions/{id}/cancel.
func (h *Handler) cancelSubscription(w http.ResponseWriter, r *http.Request) {
	sub, err := h.subs.Cancel(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sub)
}
