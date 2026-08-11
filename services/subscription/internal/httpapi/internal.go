package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/caspervpn/platform/httpjson"
)

// handleInternal serves the bearer-guarded control-plane callbacks:
//
//	POST /internal/tokens      register a token -> (user, subscription) mapping
//	POST /internal/revoke      revoke a token (leaked link) + flush caches
//	POST /internal/invalidate  cache invalidation on a node-blocked signal
//
// Disabled entirely (404) when INTERNAL_TOKEN is unset.
func (s *Server) handleInternal(w http.ResponseWriter, r *http.Request) {
	if s.cfg.InternalToken == "" {
		httpjson.Error(w, http.StatusNotFound, "not found")
		return
	}
	if !s.authorizedInternal(r) {
		httpjson.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		httpjson.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	switch strings.TrimPrefix(r.URL.Path, "/internal/") {
	case "tokens":
		s.internalRegisterToken(w, r)
	case "revoke":
		s.internalRevokeToken(w, r)
	case "invalidate":
		s.internalInvalidate(w, r)
	default:
		httpjson.Error(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) authorizedInternal(r *http.Request) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	return strings.HasPrefix(auth, prefix) &&
		subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, prefix)), []byte(s.cfg.InternalToken)) == 1
}

type registerReq struct {
	Token          string `json:"token"`
	UserID         string `json:"user_id"`
	SubscriptionID string `json:"subscription_id"`
}

func (s *Server) internalRegisterToken(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := decodeJSON(r, &req); err != nil || req.Token == "" || req.UserID == "" || req.SubscriptionID == "" {
		httpjson.Error(w, http.StatusBadRequest, "token, user_id and subscription_id are required")
		return
	}
	if err := s.idx.Register(r.Context(), req.Token, req.UserID, req.SubscriptionID); err != nil {
		httpjson.Error(w, http.StatusInternalServerError, "register failed")
		return
	}
	// A re-registered token may change its target; drop any stale cache.
	s.cache.InvalidateToken(req.Token)
	w.WriteHeader(http.StatusNoContent)
}

// revokeReq selects what to revoke. Exactly one selector must be set:
//
//	token           — one leaked link
//	user_id         — every link of a user (ban/suspend)
//	subscription_id — the link(s) of one subscription (cancel/expire/rotate)
//
// user_id/subscription_id are additive Wave-2 extensions (TZ-token-revocation):
// the control-plane stores only token hashes, so at ban time it cannot present
// the plaintext token — it revokes by owner instead.
type revokeReq struct {
	Token          string `json:"token,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
}

func (s *Server) internalRevokeToken(w http.ResponseWriter, r *http.Request) {
	var req revokeReq
	if err := decodeJSON(r, &req); err != nil {
		httpjson.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	set := 0
	for _, v := range []string{req.Token, req.UserID, req.SubscriptionID} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		httpjson.Error(w, http.StatusBadRequest, "exactly one of token, user_id, subscription_id is required")
		return
	}

	var err error
	switch {
	case req.Token != "":
		err = s.idx.Revoke(r.Context(), req.Token)
	case req.UserID != "":
		_, err = s.idx.RevokeByUser(r.Context(), req.UserID)
	default:
		_, err = s.idx.RevokeBySubscription(r.Context(), req.SubscriptionID)
	}
	if err != nil {
		httpjson.Error(w, http.StatusInternalServerError, "revoke failed")
		return
	}
	// Bump all caches so a revoked token cannot be served from cache until TTL
	// (the acceptance criterion: ban is visible immediately). The cache is keyed
	// by plaintext token, so owner-scoped revokes can only be enforced broadly.
	s.cache.InvalidateAll()
	w.WriteHeader(http.StatusNoContent)
}

type invalidateReq struct {
	NodeID string `json:"node_id"`
	Token  string `json:"token"`
}

func (s *Server) internalInvalidate(w http.ResponseWriter, r *http.Request) {
	var req invalidateReq
	if err := decodeJSON(r, &req); err != nil {
		httpjson.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	switch {
	case req.Token != "":
		s.cache.InvalidateToken(req.Token)
	default:
		// A blocked node affects every subscription that could include it:
		// rebuild them all so it washes out immediately.
		s.cache.InvalidateAll()
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}
