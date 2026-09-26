package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestSubscriptionLimiterUsesConfiguredClientAddress(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.TrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")}
	handler := srv.Handler()
	request := func(forwarded string) int {
		r := httptest.NewRequest("GET", "/sub/unknown", nil)
		r.RemoteAddr = "172.30.0.2:12345"
		r.Header.Set("X-Forwarded-For", forwarded)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	limited := false
	for i := 0; i < 300; i++ {
		if request("198.51.100.1") == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("first client did not exhaust its bucket")
	}
	if status := request("198.51.100.2"); status != http.StatusUnauthorized {
		t.Fatalf("second client behind same proxy status=%d want401", status)
	}
}
