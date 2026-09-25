package httpapi

import (
	"context"
	"github.com/caspervpn/contracts"
	"github.com/go-chi/chi/v5"
	"net/http"
	"time"
)

func (h *Handler) ensureTelegram(w http.ResponseWriter, r *http.Request) {
	var req contracts.EnsureTelegram
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid request")
		return
	}
	u, err := h.users.EnsureTelegram(r.Context(), req.TelegramID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	u.PrivateKey = ""
	u.UUID = ""
	u.RealityShortID = ""
	u.Hysteria2Password = ""
	writeJSON(w, 200, u)
}
func (h *Handler) ensureSubscription(w http.ResponseWriter, r *http.Request) {
	var req contracts.EnsureSubscription
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid request")
		return
	}
	s, err := h.subs.Ensure(r.Context(), chi.URLParam(r, "id"), req.Plan)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, 200, s)
}
func (h *Handler) billingState(w http.ResponseWriter, r *http.Request) {
	var req contracts.BillingState
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid request")
		return
	}
	s, err := h.subs.ApplyBillingState(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, 200, s)
}
func (h *Handler) deliveryLink(w http.ResponseWriter, r *http.Request) {
	link, err := h.subs.DeliveryLink(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, link)
}
func (h *Handler) resolveToken(w http.ResponseWriter, r *http.Request) {
	var req contracts.ResolveSubscriptionToken
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid request")
		return
	}
	b, err := h.subs.ResolveToken(r.Context(), req.TokenHash)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, b)
}
func (h *Handler) accessUsers(w http.ResponseWriter, r *http.Request) {
	s, err := h.allow.AccessForNode(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s)
}
func (h *Handler) WithReadiness(check func(context.Context) error) *Handler {
	h.ready = check
	return h
}
func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	if h.ready == nil {
		writeError(w, 503, "not_ready", "readiness not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.ready(ctx); err != nil {
		writeError(w, 503, "not_ready", "database unavailable")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}
