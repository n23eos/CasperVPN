package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
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
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !s.authorizedInternal(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
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
		writeError(w, http.StatusNotFound, "not found")
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
		writeError(w, http.StatusBadRequest, "token, user_id and subscription_id are required")
		return
	}
	if err := s.idx.Register(r.Context(), req.Token, req.UserID, req.SubscriptionID); err != nil {
		writeError(w, http.StatusInternalServerError, "register failed")
		return
	}
	// A re-registered token may change its target; drop any stale cache.
	s.cache.InvalidateToken(req.Token)
	w.WriteHeader(http.StatusNoContent)
}

type revokeReq struct {
	Token string `json:"token"`
}

func (s *Server) internalRevokeToken(w http.ResponseWriter, r *http.Request) {
	var req revokeReq
	if err := decodeJSON(r, &req); err != nil || req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	if err := s.idx.Revoke(r.Context(), req.Token); err != nil {
		writeError(w, http.StatusInternalServerError, "revoke failed")
		return
	}
	// Bump all caches so a revoked token cannot be served from cache.
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
		writeError(w, http.StatusBadRequest, "invalid body")
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
