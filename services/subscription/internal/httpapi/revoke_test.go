package httpapi

// TZ-token-revocation acceptance tests: a revoke pushed by the control-plane
// must take effect IMMEDIATELY — including for responses already sitting in the
// render cache — and owner-scoped revokes must not kill unrelated tokens.

import (
	"net/http"
	"testing"
)

var internalHeaders = map[string]string{
	"Authorization": "Bearer secret",
	"Content-Type":  "application/json",
}

func TestRevokeByToken_KillsCachedResponseImmediately(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	// Warm the cache: without invalidation this response would be served until TTL.
	if w := do(t, h, http.MethodGet, "/sub/tok-good", nil, nil); w.Code != http.StatusOK {
		t.Fatalf("setup: status = %d", w.Code)
	}

	w := do(t, h, http.MethodPost, "/internal/revoke", internalHeaders, []byte(`{"token":"tok-good"}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d", w.Code)
	}

	// The very next request must fail — before any TTL expiry.
	if w := do(t, h, http.MethodGet, "/sub/tok-good", nil, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want 401", w.Code)
	}
}

func TestRevokeByUser_KillsAllUserTokens(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	w := do(t, h, http.MethodPost, "/internal/revoke", internalHeaders, []byte(`{"user_id":"u1"}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d", w.Code)
	}
	for _, tok := range []string{"tok-good", "tok-exp", "tok-nosub"} {
		if w := do(t, h, http.MethodGet, "/sub/"+tok, nil, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("token %s after user revoke: status = %d, want 401", tok, w.Code)
		}
	}
}

func TestRevokeBySubscription_LeavesOtherTokensAlive(t *testing.T) {
	srv, m := newTestServer(t)
	h := srv.Handler()
	// Second live token of the same user, different subscription.
	_ = m.Register(nil, "tok-second", "u1", "s3")

	w := do(t, h, http.MethodPost, "/internal/revoke", internalHeaders, []byte(`{"subscription_id":"s1"}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/sub/tok-good", nil, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked subscription token status = %d, want 401", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/sub/tok-second", nil, nil); w.Code != http.StatusOK {
		t.Errorf("unrelated token status = %d, want 200 (must survive)", w.Code)
	}
}

func TestRevoke_RequiresExactlyOneSelector(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	for _, body := range []string{
		`{}`,
		`{"token":"t","user_id":"u1"}`,
		`{"token":"t","subscription_id":"s1"}`,
		`{"user_id":"u1","subscription_id":"s1"}`,
	} {
		if w := do(t, h, http.MethodPost, "/internal/revoke", internalHeaders, []byte(body)); w.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, w.Code)
		}
	}
}
