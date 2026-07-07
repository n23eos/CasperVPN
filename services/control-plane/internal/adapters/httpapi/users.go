package httpapi

import (
	"net/http"

	"github.com/caspervpn/contracts"
	"github.com/go-chi/chi/v5"
)

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	u, err := h.users.Create(r.Context(), req.TelegramID, req.Email)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.users.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request) {
	var patch contracts.User
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	u, err := h.users.Update(r.Context(), chi.URLParam(r, "id"), patch, actorFromContext(r.Context()))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (h *Handler) rotateUserSecrets(w http.ResponseWriter, r *http.Request) {
	u, err := h.users.RotateSecrets(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("reason"), actorFromContext(r.Context()))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// getSubscriptionSet is the additive endpoint that hands Agent C the structured
// Node×Transport set for a user (per-user secrets, dynamic active nodes).
func (h *Handler) getSubscriptionSet(w http.ResponseWriter, r *http.Request) {
	set, err := h.bundles.GetForUser(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subscriptionSetResponse{
		Revision:    set.Revision,
		GeneratedAt: set.Bundle.GeneratedAt,
		User:        set.Bundle.User,
		Nodes:       set.Bundle.Nodes,
	})
}
