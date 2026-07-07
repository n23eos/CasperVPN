package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/caspervpn/control-plane/internal/authz"
)

type ctxKey int

const roleKey ctxKey = iota

// authenticate resolves the bearer token to a role and stores it in context.
// Missing/invalid tokens get 401. Applied to every route except /healthz.
func (h *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		role, ok := h.tokens.Resolve(token)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}
		ctx := context.WithValue(r.Context(), roleKey, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireRole wraps a handler so only the listed roles may call it (else 403).
func requireRole(allowed ...authz.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, _ := r.Context().Value(roleKey).(authz.Role)
			if !authz.Allows(role, allowed...) {
				writeError(w, http.StatusForbidden, "forbidden", "role not permitted for this operation")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// actorFromContext names the caller for audit history (rotation records).
func actorFromContext(ctx context.Context) string {
	if role, ok := ctx.Value(roleKey).(authz.Role); ok {
		return string(role)
	}
	return "unknown"
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
