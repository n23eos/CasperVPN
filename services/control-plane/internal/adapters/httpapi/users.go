package httpapi

import (
	"net/http"

	"github.com/caspervpn/contracts"
	"github.com/go-chi/chi/v5"
)

// writeUser emits a user response with the server-side PrivateKey stripped. No
// API consumer (admin, billing, subscription) needs the user's transport key
// material — its canonical home is the users table only. Centralizing the strip
// here closes every user-returning path, not just the subscription-set bundle.
func writeUser(w http.ResponseWriter, status int, u contracts.User) {
	u.PrivateKey = ""
	writeJSON(w, status, u)
}

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
	writeUser(w, http.StatusCreated, u)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.users.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeUser(w, http.StatusOK, u)
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
	writeUser(w, http.StatusOK, u)
}

func (h *Handler) rotateUserSecrets(w http.ResponseWriter, r *http.Request) {
	u, err := h.users.RotateSecrets(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("reason"), actorFromContext(r.Context()))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeUser(w, http.StatusOK, u)
}

// getSubscriptionSet is the additive endpoint that hands Agent C the structured
// Node×Transport set for a user (per-user secrets, dynamic active nodes).
func (h *Handler) getSubscriptionSet(w http.ResponseWriter, r *http.Request) {
	set, err := h.bundles.GetForUser(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	// Never expose the user's server-side PrivateKey over the wire, even if an
	// older cached set was persisted before the bundle builder started stripping it.
	respUser := set.Bundle.User
	respUser.PrivateKey = ""
	writeJSON(w, http.StatusOK, subscriptionSetResponse{
		Revision:    set.Revision,
		GeneratedAt: set.Bundle.GeneratedAt,
		User:        respUser,
		Nodes:       set.Bundle.Nodes,
	})
}
