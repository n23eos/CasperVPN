// Package authz defines service-to-service roles and bearer-token resolution.
// Tokens are supplied via configuration (env / secret manager) — never hardcoded.
package authz

import "crypto/subtle"

// Role is a service-to-service identity. Admin is the human-operator role.
type Role string

const (
	RoleAdmin        Role = "admin"
	RoleOrchestrator Role = "orchestrator"
	RoleTelemetry    Role = "telemetry"
	RoleSubscription Role = "subscription"
	RoleBilling      Role = "billing"
)

// ParseRole validates a role string.
func ParseRole(s string) (Role, bool) {
	switch Role(s) {
	case RoleAdmin, RoleOrchestrator, RoleTelemetry, RoleSubscription, RoleBilling:
		return Role(s), true
	}
	return "", false
}

// TokenStore maps opaque bearer tokens to roles.
type TokenStore struct {
	tokens map[string]Role
}

// NewTokenStore builds a store from a token→role map.
func NewTokenStore(tokens map[string]Role) *TokenStore {
	cp := make(map[string]Role, len(tokens))
	for k, v := range tokens {
		cp[k] = v
	}
	return &TokenStore{tokens: cp}
}

// Empty reports whether no tokens are configured.
func (s *TokenStore) Empty() bool { return len(s.tokens) == 0 }

// Resolve returns the role for a presented token using a constant-time compare
// against every configured token (avoids leaking which prefix matched).
func (s *TokenStore) Resolve(presented string) (Role, bool) {
	var (
		role  Role
		found bool
	)
	pb := []byte(presented)
	for tok, r := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(tok), pb) == 1 {
			role, found = r, true
		}
	}
	return role, found
}

// Allows reports whether role is in the allowed set.
func Allows(role Role, allowed ...Role) bool {
	for _, a := range allowed {
		if role == a {
			return true
		}
	}
	return false
}
