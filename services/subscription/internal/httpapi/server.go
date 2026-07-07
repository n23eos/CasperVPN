// Package httpapi wires the subscription endpoints: GET /sub/{token} in three
// negotiated formats, GET /sub/{token}/nodes (raw bundle), the Happ deep link,
// and the internal cache/token endpoints. Routing is done by hand (stdlib mux)
// because the toolchain floor is Go 1.20.
package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

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
	mux.HandleFunc("/sub/", s.handleSub)
	mux.HandleFunc("/internal/", s.handleInternal)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "subscription"})
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
