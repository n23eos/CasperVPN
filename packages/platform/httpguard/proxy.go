package httpguard

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const maxForwardedHops = 32
const maxForwardedBytes = 4096

// WithTrustedProxies configures explicit trust before the limiter serves traffic.
// The copied prefixes cannot be changed by the caller after construction.
func (l *Limiter) WithTrustedProxies(prefixes []netip.Prefix) *Limiter {
	l.trustedProxies = append([]netip.Prefix(nil), prefixes...)
	return l
}

func (l *Limiter) trusted(addr netip.Addr) bool {
	for _, prefix := range l.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (l *Limiter) clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.WithZone("").Unmap()
	// The immediate TCP peer is authoritative unless explicitly configured as a proxy.
	if !l.trusted(peer) {
		return peer.String()
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return peer.String()
	}
	size := 0
	for _, value := range values {
		size += len(value) + 1
		if size > maxForwardedBytes {
			return peer.String()
		}
	}
	hops := strings.Split(strings.Join(values, ","), ",")
	if len(hops) > maxForwardedHops {
		return peer.String()
	}
	current := peer
	for i := len(hops) - 1; i >= 0; i-- {
		// Everything to the left of the first untrusted hop is controlled by that hop.
		if !l.trusted(current) {
			return current.String()
		}
		next, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil || next.Zone() != "" {
			return peer.String()
		}
		current = next.Unmap()
	}
	return current.String()
}
