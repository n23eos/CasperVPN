package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/subscription/internal/cache"
	"github.com/caspervpn/subscription/internal/config"
	"github.com/caspervpn/subscription/internal/controlplane"
	"github.com/caspervpn/subscription/internal/personalize"
	"github.com/caspervpn/subscription/internal/render"
	"github.com/caspervpn/subscription/internal/resolve"
)

var fixedNow = time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)

func multiNode(id string, status contracts.NodeStatus) contracts.Node {
	mk := func(tag string, typ contracts.TransportType) contracts.Transport {
		t := contracts.Transport{Tag: tag, Type: typ, Version: contracts.TransportVersionV1, Port: 443, Enabled: true}
		switch typ {
		case contracts.TransportVlessReality:
			t.VlessReality = &contracts.VlessRealityParams{ServerNames: []string{"x.test"}, PublicKey: "k", Flow: "xtls-rprx-vision"}
		case contracts.TransportHysteria2:
			t.Port = 8443
			t.Hysteria2 = &contracts.Hysteria2Params{Password: "p", SNI: "x.test"}
		case contracts.TransportAmneziaWG:
			t.Port = 51820
			t.AmneziaWG = &contracts.AmneziaWGParams{PublicKey: "k"}
		}
		return t
	}
	return contracts.Node{
		ID: id, Role: contracts.NodeRoleCombined, Status: status, EntryIP: "198.51.100.10", Region: "eu",
		Transports: []contracts.Transport{
			mk(id+"-r", contracts.TransportVlessReality),
			mk(id+"-h", contracts.TransportHysteria2),
			mk(id+"-a", contracts.TransportAmneziaWG),
		},
	}
}

func newTestServer(t testing.TB) (*Server, *controlplane.Memory) {
	t.Helper()
	policy, err := config.LoadRoutingPolicy(filepath.FromSlash("../../config/routing.ru.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ProfileUpdateHours: 12,
		CacheTTL:           5 * time.Minute,
		InternalToken:      "secret",
		ProfileTitle:       "CasperVPN",
		Routing:            policy,
	}

	m := controlplane.NewMemory()
	m.PutUser(contracts.User{ID: "u1", Status: contracts.UserStatusActive, RealityShortID: "ab12", UUID: "uuid-1", UsedBytes: 42})
	future := fixedNow.Add(24 * time.Hour)
	past := fixedNow.Add(-24 * time.Hour)
	m.PutSubscription(contracts.Subscription{ID: "s1", UserID: "u1", Plan: contracts.SubscriptionPlanUnlimited, Status: contracts.SubscriptionStatusActive, Token: "tok-good", ExpiresAt: &future, TrafficLimitBytes: 0})
	m.PutSubscription(contracts.Subscription{ID: "s2", UserID: "u1", Plan: contracts.SubscriptionPlanBasic, Status: contracts.SubscriptionStatusExpired, Token: "tok-exp", ExpiresAt: &past})
	m.PutSubscription(contracts.Subscription{ID: "s3", UserID: "u1", Plan: contracts.SubscriptionPlanUnlimited, Status: contracts.SubscriptionStatusActive, Token: "tok-new", ExpiresAt: &future})
	_ = m.Register(nil, "tok-good", "u1", "s1")
	_ = m.Register(nil, "tok-exp", "u1", "s2")
	_ = m.Register(nil, "tok-nosub", "u1", "missing-sub")
	m.SetNodes([]contracts.Node{multiNode("n1", contracts.NodeStatusActive), multiNode("n2", contracts.NodeStatusActive)})

	now := func() time.Time { return fixedNow }
	srv := New(cfg,
		resolve.New(m, m, now),
		render.New(policy),
		cache.New(cfg.CacheTTL, now),
		m, now)
	return srv, m
}

func do(t *testing.T, h http.Handler, method, target string, headers map[string]string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSubscriptionBase64AndHeaders(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	w := do(t, h, http.MethodGet, "/sub/tok-good", map[string]string{"User-Agent": "Happ/1.0"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Profile-Update-Interval"); got != "12" {
		t.Errorf("Profile-Update-Interval = %q", got)
	}
	ui := w.Header().Get("Subscription-Userinfo")
	if !strings.Contains(ui, "download=42") || !strings.Contains(ui, "total=0") || !strings.Contains(ui, "expire=") {
		t.Errorf("Subscription-Userinfo = %q", ui)
	}
	if w.Header().Get("Routing-Enable") != "1" {
		t.Errorf("Routing-Enable not set")
	}
	raw, err := base64.StdEncoding.DecodeString(w.Body.String())
	if err != nil {
		t.Fatalf("body not base64: %v", err)
	}
	// Two nodes, reality+hysteria2 each in URI list (amnezia omitted): 4 URIs.
	if n := strings.Count(string(raw), "vless://"); n != 2 {
		t.Errorf("want 2 vless URIs, got %d", n)
	}
	if strings.Contains(string(raw), "-a\n") || strings.Contains(string(raw), "awg") {
		t.Errorf("amneziawg leaked into base64 list")
	}
}

func TestFormatNegotiationSingBoxAndClash(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	sb := do(t, h, http.MethodGet, "/sub/tok-good?format=singbox", nil, nil)
	if ct := sb.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("singbox content-type = %q", ct)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(sb.Body.Bytes(), &doc); err != nil {
		t.Fatalf("singbox invalid json: %v", err)
	}
	if _, ok := doc["route"]; !ok {
		t.Error("singbox missing route (split-tunnel)")
	}

	cl := do(t, h, http.MethodGet, "/sub/tok-good", map[string]string{"User-Agent": "mihomo/1.18"}, nil)
	if ct := cl.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("clash content-type = %q", ct)
	}
	if !strings.Contains(cl.Body.String(), "MATCH,PROXY") {
		t.Error("clash missing routing rules")
	}
}

func TestSubscriptionErrorCodes(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	cases := []struct {
		token string
		want  int
	}{
		{"tok-good", http.StatusOK},
		{"nope", http.StatusUnauthorized},
		{"tok-exp", http.StatusGone},
		{"tok-nosub", http.StatusNotFound},
	}
	for _, c := range cases {
		w := do(t, h, http.MethodGet, "/sub/"+c.token, nil, nil)
		if w.Code != c.want {
			t.Errorf("token %q: status = %d, want %d", c.token, w.Code, c.want)
		}
	}
}

func TestNodesEndpointKeepsMultiTransport(t *testing.T) {
	srv, _ := newTestServer(t)
	w := do(t, srv.Handler(), http.MethodGet, "/sub/tok-good/nodes", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var bundle contracts.SubscriptionBundle
	if err := json.Unmarshal(w.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if types := personalize.DistinctTransportTypes(bundle.Nodes); len(types) < 2 {
		t.Fatalf("subscription must always carry >=2 transports, got %v", types)
	}
}

func TestHappDeepLink(t *testing.T) {
	srv, _ := newTestServer(t)
	w := do(t, srv.Handler(), http.MethodGet, "/sub/tok-good/happ", map[string]string{"X-Forwarded-Host": "cdn.example.test", "X-Forwarded-Proto": "https"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	link := w.Body.String()
	if !strings.HasPrefix(link, "happ://add/") {
		t.Fatalf("deep link = %q", link)
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "happ://add/"))
	if err != nil {
		t.Fatalf("deep link payload not base64: %v", err)
	}
	if got := string(dec); got != "https://cdn.example.test/sub/tok-good" {
		t.Errorf("deep link subscription URL = %q", got)
	}
}

func TestCacheInvalidationWashesBlockedNode(t *testing.T) {
	srv, m := newTestServer(t)
	h := srv.Handler()

	first := do(t, h, http.MethodGet, "/sub/tok-good", nil, nil)
	rawFirst, _ := base64.StdEncoding.DecodeString(first.Body.String())
	if strings.Count(string(rawFirst), "vless://") != 2 {
		t.Fatalf("setup: want 2 nodes")
	}

	// n2 goes blocked upstream. Without invalidation the cache still serves it.
	m.SetNodes([]contracts.Node{multiNode("n1", contracts.NodeStatusActive)})
	cached := do(t, h, http.MethodGet, "/sub/tok-good", nil, nil)
	rawCached, _ := base64.StdEncoding.DecodeString(cached.Body.String())
	if strings.Count(string(rawCached), "vless://") != 2 {
		t.Errorf("expected stale cached response with 2 nodes")
	}

	// Control-plane signals the block -> rebuild.
	inv := do(t, h, http.MethodPost, "/internal/invalidate",
		map[string]string{"Authorization": "Bearer secret", "Content-Type": "application/json"},
		[]byte(`{"node_id":"n2"}`))
	if inv.Code != http.StatusNoContent {
		t.Fatalf("invalidate status = %d", inv.Code)
	}
	fresh := do(t, h, http.MethodGet, "/sub/tok-good", nil, nil)
	rawFresh, _ := base64.StdEncoding.DecodeString(fresh.Body.String())
	if strings.Count(string(rawFresh), "vless://") != 1 {
		t.Errorf("blocked node not washed out after invalidation: %s", rawFresh)
	}
}

func TestInternalRequiresBearer(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	w := do(t, h, http.MethodPost, "/internal/invalidate", map[string]string{"Content-Type": "application/json"}, []byte(`{}`))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing bearer: status = %d, want 401", w.Code)
	}
	w = do(t, h, http.MethodPost, "/internal/tokens",
		map[string]string{"Authorization": "Bearer secret", "Content-Type": "application/json"},
		[]byte(`{"token":"tok-new","user_id":"u1","subscription_id":"s3"}`))
	if w.Code != http.StatusNoContent {
		t.Errorf("register status = %d", w.Code)
	}
	if ok := do(t, h, http.MethodGet, "/sub/tok-new", nil, nil); ok.Code != http.StatusOK {
		t.Errorf("newly-registered token status = %d", ok.Code)
	}
}

// FuzzSubToken ensures arbitrary tokens never panic and always yield a sane code.
func FuzzSubToken(f *testing.F) {
	srv, _ := newTestServer(f)
	h := srv.Handler()
	for _, seed := range []string{"tok-good", "", "../etc", "%zz", "a/b/c", "tok exp", strings.Repeat("x", 5000)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		// Build the request without target parsing so arbitrary bytes in the
		// token exercise the handler, not httptest's URL parser.
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path = "/sub/" + token
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		switch w.Code {
		case http.StatusOK, http.StatusUnauthorized, http.StatusNotFound, http.StatusGone,
			http.StatusMethodNotAllowed, http.StatusMovedPermanently, http.StatusPermanentRedirect,
			http.StatusBadRequest, http.StatusRequestURITooLong:
		default:
			t.Fatalf("token %q: unexpected status %d", token, w.Code)
		}
	})
}
