package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthorityOverridesStaleLocalToken(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/subscription-tokens/resolve" || r.Header.Get("Authorization") != "Bearer service-auth" {
			t.Errorf("wrong authority request")
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["token_hash"] != HashToken("private-token") {
			t.Errorf("request must carry hash, not plaintext")
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"user_id":"u","subscription_id":"s"}`))
		}
	}))
	defer srv.Close()
	local := NewMemory()
	_ = local.Register(context.Background(), "private-token", "stale-user", "stale-sub")
	idx := &AuthoritativeIndex{TokenIndex: local, CP: NewHTTPClient(srv.URL, "service-auth", time.Second)}
	u, s, err := idx.Lookup(context.Background(), "private-token")
	if err != nil || u != "u" || s != "s" {
		t.Fatalf("fresh binding %s %s %v", u, s, err)
	}
	status = http.StatusNotFound
	if _, _, err = idx.Lookup(context.Background(), "private-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale local token authorized: %v", err)
	}
	status = http.StatusServiceUnavailable
	if _, _, err = idx.Lookup(context.Background(), "private-token"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("upstream failure must fail closed and remain distinguishable: %v", err)
	}
}
