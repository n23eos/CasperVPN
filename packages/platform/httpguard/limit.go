// Package httpguard bounds public HTTP work without trusting forwarded headers.
package httpguard

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	at     time.Time
}

// Limiter is a bounded per-peer token bucket. Saturation rejects new peers
// rather than allocating unbounded memory under an address-rotation attack.
type Limiter struct {
	mu          sync.Mutex
	peers       map[string]bucket
	rate, burst float64
	maxPeers    int
	now         func() time.Time
}

func New(rate, burst, maxPeers int) *Limiter {
	if rate < 1 {
		rate = 1
	}
	if burst < 1 {
		burst = 1
	}
	if maxPeers < 1 {
		maxPeers = 1
	}
	return &Limiter{peers: make(map[string]bucket), rate: float64(rate), burst: float64(burst), maxPeers: maxPeers, now: time.Now}
}

func (l *Limiter) allow(peer string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, exists := l.peers[peer]
	if !exists {
		if len(l.peers) >= l.maxPeers {
			for key, old := range l.peers {
				if now.Sub(old.at) > 2*time.Minute {
					delete(l.peers, key)
				}
			}
			if len(l.peers) >= l.maxPeers {
				return false
			}
		}
		b = bucket{tokens: l.burst, at: now}
	}
	b.tokens += now.Sub(b.at).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.at = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.peers[peer] = b
	return allowed
}

func (l *Limiter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			peer = r.RemoteAddr
		}
		if !l.allow(peer) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
