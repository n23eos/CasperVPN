package httpapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

func TestEmptyFleetNeverReturnsDirectProfile(t *testing.T) {
	for _, path := range []string{"?format=singbox", "?format=clash", "?format=base64", "/nodes"} {
		t.Run(path, func(t *testing.T) {
			srv, mem := newTestServer(t)
			mem.SetNodes(nil)
			w := do(t, srv.Handler(), http.MethodGet, "/sub/tok-good"+path, nil, nil)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("empty fleet status=%d, want 503; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCurrentEntitlementAlwaysCheckedBeforeCache(t *testing.T) {
	for _, action := range []string{"revoke", "ban", "expire", "empty-fleet"} {
		t.Run(action, func(t *testing.T) {
			srv, mem := newTestServer(t)
			h := srv.Handler()
			if w := do(t, h, http.MethodGet, "/sub/tok-good", nil, nil); w.Code != http.StatusOK {
				t.Fatalf("warm cache: %d", w.Code)
			}
			want := http.StatusUnauthorized
			switch action {
			case "revoke":
				_ = mem.Revoke(context.Background(), "tok-good")
			case "ban":
				u, _ := mem.GetUser(context.Background(), "u1")
				u.Status = contracts.UserStatusBanned
				mem.PutUser(u)
			case "expire":
				s, _ := mem.GetSubscription(context.Background(), "s1")
				expired := fixedNow.Add(-time.Minute)
				s.ExpiresAt = &expired
				mem.PutSubscription(s)
				want = http.StatusGone
			case "empty-fleet":
				mem.SetNodes(nil)
				want = http.StatusServiceUnavailable
			}
			if w := do(t, h, http.MethodGet, "/sub/tok-good", nil, nil); w.Code != want {
				t.Fatalf("without invalidate callback status=%d want %d", w.Code, want)
			}
		})
	}
}

func TestCredentialRotationBypassesStaleRender(t *testing.T) {
	srv, mem := newTestServer(t)
	h := srv.Handler()
	_ = do(t, h, http.MethodGet, "/sub/tok-good", nil, nil)
	u, _ := mem.GetUser(context.Background(), "u1")
	u.UUID = "new-personal-uuid"
	u.Hysteria2Password = "new-personal-password"
	mem.PutUser(u)
	w := do(t, h, http.MethodGet, "/sub/tok-good", nil, nil)
	raw, err := base64.StdEncoding.DecodeString(w.Body.String())
	if err != nil || !strings.Contains(string(raw), u.UUID) || !strings.Contains(string(raw), u.Hysteria2Password) {
		t.Fatalf("rotated credentials missing from fresh profile: status=%d err=%v", w.Code, err)
	}
}

func TestGracePreservesAccessUntilDeadline(t *testing.T) {
	srv, mem := newTestServer(t)
	sub, _ := mem.GetSubscription(context.Background(), "s1")
	expires, grace := fixedNow.Add(-time.Minute), fixedNow.Add(time.Minute)
	sub.Status, sub.ExpiresAt, sub.GraceUntil = contracts.SubscriptionStatusPastDue, &expires, &grace
	mem.PutSubscription(sub)
	if w := do(t, srv.Handler(), http.MethodGet, "/sub/tok-good", nil, nil); w.Code != http.StatusOK {
		t.Fatalf("within grace status=%d, want 200", w.Code)
	}
	grace = fixedNow
	mem.PutSubscription(sub)
	if w := do(t, srv.Handler(), http.MethodGet, "/sub/tok-good", nil, nil); w.Code != http.StatusGone {
		t.Fatalf("at grace deadline status=%d, want 410", w.Code)
	}
}
