package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/subscription/internal/cache"
	"github.com/caspervpn/subscription/internal/personalize"
	"github.com/caspervpn/subscription/internal/render"
	"github.com/caspervpn/subscription/internal/resolve"
)

// handleSub dispatches /sub/{token}, /sub/{token}/nodes and /sub/{token}/happ.
func (s *Server) handleSub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/sub/")
	if rest == "" {
		writeError(w, http.StatusNotFound, "missing token")
		return
	}

	var token string
	switch {
	case strings.HasSuffix(rest, "/nodes"):
		token = strings.TrimSuffix(rest, "/nodes")
		s.serveNodes(w, r, token)
		return
	case strings.HasSuffix(rest, "/happ"):
		token = strings.TrimSuffix(rest, "/happ")
		s.serveDeepLink(w, r, token)
		return
	default:
		token = rest
	}
	if strings.Contains(token, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if dec, err := url.PathUnescape(token); err == nil {
		token = dec
	}
	if r.URL.Query().Get("deeplink") == "1" {
		s.serveDeepLink(w, r, token)
		return
	}
	s.serveSubscription(w, r, token)
}

// serveSubscription renders /sub/{token} in the negotiated format, from cache
// when possible.
func (s *Server) serveSubscription(w http.ResponseWriter, r *http.Request, token string) {
	q := r.URL.Query()
	format := render.Negotiate(q.Get("format"), r.Header.Get("Accept"), r.Header.Get("User-Agent"))
	params := personalize.Params{
		Region:   q.Get("region"),
		Platform: contracts.Platform(q.Get("platform")),
	}
	cacheKey := cacheFormatKey(format, params)

	if e, ok := s.cache.Get(token, cacheKey); ok {
		writeCached(w, e)
		return
	}

	res, err := s.resolver.Resolve(r.Context(), token)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	filtered := personalize.Filter(res.Bundle, res.Subscription.Plan, params)

	body, ct, err := s.renderer.Render(format, filtered.Bundle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "render failed")
		return
	}

	headers := s.happHeaders(res.Subscription, res.Bundle.User)
	entry := cache.Entry{Body: body, ContentType: ct, Headers: headers}
	s.cache.Put(token, cacheKey, entry)
	writeCached(w, entry)
}

// serveNodes returns the raw canonical bundle (structured) behind a token.
func (s *Server) serveNodes(w http.ResponseWriter, r *http.Request, token string) {
	if dec, err := url.PathUnescape(token); err == nil {
		token = dec
	}
	res, err := s.resolver.Resolve(r.Context(), token)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	params := personalize.Params{
		Region:   r.URL.Query().Get("region"),
		Platform: contracts.Platform(r.URL.Query().Get("platform")),
	}
	filtered := personalize.Filter(res.Bundle, res.Subscription.Plan, params)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(filtered.Bundle)
}

// serveDeepLink returns the Happ `happ://add/<base64>` deep link for this
// subscription. The subscription URL is reconstructed from the request (the
// channel that served it), never from a hardcoded domain.
func (s *Server) serveDeepLink(w http.ResponseWriter, r *http.Request, token string) {
	if dec, err := url.PathUnescape(token); err == nil {
		token = dec
	}
	// Confirm the token is real before minting a link.
	if _, _, err := s.idx.Lookup(r.Context(), token); err != nil {
		writeError(w, http.StatusUnauthorized, "unknown or revoked token")
		return
	}
	subURL := requestSubscriptionURL(r, token)
	link := render.HappAddDeepLink(subURL)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(link))
}

// happHeaders builds the response headers (Happ update/userinfo hints).
func (s *Server) happHeaders(sub contracts.Subscription, user contracts.User) map[string]string {
	h := http.Header{}
	meta := render.HappMetaFor(sub, user, s.cfg.ProfileUpdateHours,
		s.cfg.ProfileTitle, s.cfg.Announce, s.cfg.SupportURL, s.cfg.ProfileWebPageURL)
	render.WriteHappHeaders(h, meta)
	out := make(map[string]string, len(h))
	for k := range h {
		out[k] = h.Get(k)
	}
	return out
}

// writeCached emits a cache entry (headers + body).
func writeCached(w http.ResponseWriter, e cache.Entry) {
	for k, v := range e.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", e.ContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(e.Body)
}

// writeResolveError maps typed resolution errors to HTTP codes (per OpenAPI).
func writeResolveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, resolve.ErrUnknownToken):
		writeError(w, http.StatusUnauthorized, "unknown or revoked token")
	case errors.Is(err, resolve.ErrNoSubscription):
		writeError(w, http.StatusNotFound, "no active subscription for token")
	case errors.Is(err, resolve.ErrExpired):
		writeError(w, http.StatusGone, "subscription expired")
	default:
		writeError(w, http.StatusBadGateway, "upstream error")
	}
}

// cacheFormatKey folds personalization into the cache key so variants do not
// collide.
func cacheFormatKey(format render.Format, p personalize.Params) string {
	key := string(format)
	if p.Region != "" || p.Platform != "" {
		key += ":" + p.Region + ":" + string(p.Platform)
	}
	return key
}

// requestSubscriptionURL reconstructs the absolute URL that served this request.
func requestSubscriptionURL(r *http.Request, token string) string {
	scheme := "https"
	if r.TLS == nil {
		if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
			scheme = xf
		} else {
			scheme = "http"
		}
	}
	host := r.Host
	if xh := r.Header.Get("X-Forwarded-Host"); xh != "" {
		host = xh
	}
	u := url.URL{Scheme: scheme, Host: host, Path: "/sub/" + url.PathEscape(token)}
	return u.String()
}
