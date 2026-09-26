// Package httpapi wires the subscription endpoints: GET /sub/{token} in three
// negotiated formats, GET /sub/{token}/nodes (raw bundle), the Happ deep link,
// and the internal cache/token endpoints. Routing is done by hand (stdlib mux)
// because the toolchain floor is Go 1.20.
package httpapi

import (
	"context"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/caspervpn/platform/httpguard"
	"github.com/caspervpn/platform/httpjson"
	"github.com/caspervpn/subscription/internal/cache"
	"github.com/caspervpn/subscription/internal/config"
	"github.com/caspervpn/subscription/internal/controlplane"
	"github.com/caspervpn/subscription/internal/render"
	"github.com/caspervpn/subscription/internal/resolve"
)

// Server holds the request-scoped dependencies.
type Server struct {
	cfg      config.Config
	resolver *resolve.Resolver
	renderer *render.Renderer
	cache    *cache.Cache
	idx      controlplane.TokenIndex
	now      func() time.Time
	Ready    func(context.Context) error
}

// New builds a Server.
func New(cfg config.Config, r *resolve.Resolver, rd *render.Renderer, c *cache.Cache, idx controlplane.TokenIndex, now func() time.Time) *Server {
	if now == nil {
		now = time.Now
	}
	return &Server{cfg: cfg, resolver: r, renderer: rd, cache: c, idx: idx, now: now}
}

// Handler returns the service's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if s.Ready != nil {
			if err := s.Ready(ctx); err != nil {
				httpjson.Error(w, http.StatusServiceUnavailable, "not ready")
				return
			}
		}
		s.handleHealth(w, r)
	})
	mux.Handle("/sub/", httpguard.New(20, 100, 4096).WithTrustedProxies(s.cfg.TrustedProxyCIDRs).Wrap(http.HandlerFunc(s.handleSub)))
	mux.HandleFunc("/internal/", s.handleInternal)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reject malformed bearer paths instead of redirecting them to a cleaned
		// URL. A redirect can expose credential-bearing paths in another handler.
		if strings.HasPrefix(r.URL.Path, "/sub/") && (path.Clean(r.URL.Path) != r.URL.Path || len(r.URL.Path) > 600) {
			httpjson.Error(w, http.StatusBadRequest, "invalid subscription path")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	httpjson.Write(w, http.StatusOK, map[string]string{"status": "ok", "service": "subscription"})
}
