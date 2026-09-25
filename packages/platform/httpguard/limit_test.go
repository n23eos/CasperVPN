package httpguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestForwardedSpoofCannotBypassRateLimit(t *testing.T) {
	l := New(1, 1, 2)
	now := time.Now()
	l.now = func() time.Time { return now }
	h := l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for i, want := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		r := httptest.NewRequest(http.MethodGet, "/sub/token", nil)
		r.RemoteAddr = "127.0.0.1:9000"
		r.Header.Set("X-Forwarded-For", []string{"first", "second"}[i])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("request %d status %d want %d", i, w.Code, want)
		}
	}
	now = now.Add(time.Second)
	if !l.allow("127.0.0.1") {
		t.Fatal("bucket did not refill")
	}
}

func TestPeerMemoryBoundAndIdleRecovery(t *testing.T) {
	l := New(1, 1, 1)
	now := time.Now()
	l.now = func() time.Time { return now }
	if !l.allow("a") || l.allow("b") {
		t.Fatal("peer capacity not enforced")
	}
	now = now.Add(3 * time.Minute)
	if !l.allow("b") {
		t.Fatal("idle peer not reclaimed")
	}
}
