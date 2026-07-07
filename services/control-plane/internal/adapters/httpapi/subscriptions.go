package httpapi

import (
	"net/http"

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
