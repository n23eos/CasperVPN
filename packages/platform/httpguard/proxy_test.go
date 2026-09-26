package httpguard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestProxyClientKey(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.30.0.0/24"), netip.MustParsePrefix("fd00::/64")}
	for _, tc := range []struct {
		name, peer string
		headers    []string
		want       string
		trust      bool
	}{
		{"default ignores XFF", "172.30.0.2:443", []string{"198.51.100.4"}, "172.30.0.2", false},
		{"untrusted peer spoof", "203.0.113.9:4321", []string{"198.51.100.4"}, "203.0.113.9", true},
		{"trusted proxy", "172.30.0.2:443", []string{"198.51.100.4"}, "198.51.100.4", true},
		{"spoof prefix ignored", "172.30.0.2:443", []string{"192.0.2.99, 198.51.100.4"}, "198.51.100.4", true},
		{"garbage prefix ignored", "172.30.0.2:443", []string{"not-an-IP, 198.51.100.4"}, "198.51.100.4", true},
		{"trusted chain", "172.30.0.2:443", []string{"192.0.2.99, 198.51.100.4, 172.30.0.3"}, "198.51.100.4", true},
		{"untrusted intermediate", "172.30.0.2:443", []string{"192.0.2.99, 203.0.113.7"}, "203.0.113.7", true},
		{"multiple header values", "172.30.0.2:443", []string{"192.0.2.99, 198.51.100.4", "172.30.0.3"}, "198.51.100.4", true},
		{"invalid rightmost", "172.30.0.2:443", []string{"198.51.100.4, invalid"}, "172.30.0.2", true},
		{"empty hop", "172.30.0.2:443", []string{"198.51.100.4,"}, "172.30.0.2", true},
		{"empty header", "172.30.0.2:443", nil, "172.30.0.2", true},
		{"IPv6 chain", "[fd00::2]:443", []string{"2001:db8::8, fd00::3"}, "2001:db8::8", true},
		{"mapped IPv4 peer", "[::ffff:172.30.0.2]:443", []string{"::ffff:198.51.100.4"}, "198.51.100.4", true},
		{"zone in forwarded address", "172.30.0.2:443", []string{"fe80::2%eth0"}, "172.30.0.2", true},
		{"too many hops", "172.30.0.2:443", []string{strings.Repeat("172.30.0.3,", maxForwardedHops) + "198.51.100.4"}, "172.30.0.2", true},
		{"oversize chain", "172.30.0.2:443", []string{strings.Repeat(" ", maxForwardedBytes) + "198.51.100.4"}, "172.30.0.2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := New(1, 1, 10)
			if tc.trust {
				l.WithTrustedProxies(trusted)
			}
			r := httptest.NewRequest("GET", "/sub/token", nil)
			r.RemoteAddr = tc.peer
			for _, v := range tc.headers {
				r.Header.Add("X-Forwarded-For", v)
			}
			r.Header.Set("X-Real-IP", "192.0.2.123")
			r.Header.Set("Forwarded", "for=192.0.2.123")
			if got := l.clientKey(r); got != tc.want {
				t.Fatalf("clientKey=%s want %s", got, tc.want)
			}
		})
	}
}

func TestTrustedProxyClientsHaveIndependentBucketsOverHTTP(t *testing.T) {
	l := New(1, 1, 8).WithTrustedProxies([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	now := time.Now()
	l.now = func() time.Time { return now }
	backend := httptest.NewServer(l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	defer backend.Close()
	// Both TCP connections have the same proxy peer. Forwarded addresses distinguish clients.
	clients := []*http.Client{{Transport: &http.Transport{}}, {Transport: &http.Transport{}}}
	defer clients[0].CloseIdleConnections()
	defer clients[1].CloseIdleConnections()
	for _, tc := range []struct {
		client    int
		forwarded string
		want      int
	}{
		{0, "198.51.100.1", 204}, {0, "192.0.2.88, 198.51.100.1", 429}, {1, "198.51.100.2", 204}, {1, "198.51.100.2", 429},
	} {
		req, err := http.NewRequest("GET", backend.URL+"/sub/token", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Forwarded-For", tc.forwarded)
		resp, err := clients[tc.client].Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("client %d status=%d want %d", tc.client, resp.StatusCode, tc.want)
		}
	}
}
