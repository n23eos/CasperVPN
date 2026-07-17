package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/httpapi"
	"github.com/caspervpn/control-plane/internal/adapters/memory"
	"github.com/caspervpn/control-plane/internal/authz"
	"github.com/caspervpn/control-plane/internal/usecase"
	"gopkg.in/yaml.v3"
)

const specPath = "../../../../../packages/contracts/openapi/control-plane.yaml"

// tokens used across contract tests, one per role.
const (
	adminTok = "t-admin"
	orchTok  = "t-orch"
	teleTok  = "t-tele"
	subTok   = "t-sub"
	billTok  = "t-bill"
)

func newTestRouter(t *testing.T) http.Handler {
	r, _ := newTestRouterWithNodes(t)
	return r
}

// newTestRouterWithNodes also returns the node repo, so a test can seed an ACTIVE
// node via explicit internal setup (nodes.Create) — registration itself refuses
// status:active, so the HTTP path can no longer be used to seed serving nodes.
func newTestRouterWithNodes(t *testing.T) (http.Handler, *memory.Nodes) {
	t.Helper()
	nodes := memory.NewNodes()
	users := memory.NewUsers()
	subs := memory.NewSubscriptions()
	rot := memory.NewRotations()
	sets := memory.NewSets()
	sigs := memory.NewSignals()
	q := memory.NewNoopQueue()

	bundle := usecase.NewBundleService(users, nodes, sets)
	nodeSvc := usecase.NewNodeService(nodes, rot, q)
	userSvc := usecase.NewUserService(users, rot, q)
	subSvc := usecase.NewSubscriptionService(subs, users)
	sigSvc := usecase.NewSignalService(nodes, sigs, nodeSvc)

	tokens := authz.NewTokenStore(map[string]authz.Role{
		adminTok: authz.RoleAdmin,
		orchTok:  authz.RoleOrchestrator,
		teleTok:  authz.RoleTelemetry,
		subTok:   authz.RoleSubscription,
		billTok:  authz.RoleBilling,
	})
	return httpapi.New(nodeSvc, userSvc, subSvc, bundle, sigSvc, tokens).Router(), nodes
}

type openAPISpec struct {
	Paths map[string]map[string]any `yaml:"paths"`
}

var httpMethods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}

// TestContract_EveryFrozenOperationIsWiredAndSecured loads the frozen OpenAPI and
// asserts every declared operation exists in the router (not 404) and rejects an
// unauthenticated caller (401) — except the public /healthz.
func TestContract_EveryFrozenOperationIsWiredAndSecured(t *testing.T) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	if len(spec.Paths) == 0 {
		t.Fatal("spec has no paths")
	}
	router := newTestRouter(t)

	for path, item := range spec.Paths {
		reqPath := strings.NewReplacer("{id}", "test-id").Replace(path)
		for method := range item {
			if !httpMethods[method] {
				continue // "parameters" and other non-operation keys
			}
			req := httptest.NewRequest(strings.ToUpper(method), reqPath, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if path == "/healthz" {
				if rec.Code != http.StatusOK {
					t.Errorf("GET /healthz = %d, want 200", rec.Code)
				}
				continue
			}
			if rec.Code == http.StatusNotFound {
				t.Errorf("%s %s not routed (404) — missing implementation of frozen op", method, path)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without auth = %d, want 401", method, path, rec.Code)
			}
		}
	}
}

// TestContract_RBAC verifies a wrong-role token is forbidden.
func TestContract_RBAC(t *testing.T) {
	router := newTestRouter(t)
	// Telemetry may not register nodes.
	req := httptest.NewRequest(http.MethodPost, "/v1/nodes", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+teleTok)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("telemetry POST /v1/nodes = %d, want 403", rec.Code)
	}
}

// TestContract_HappyPath exercises the core create/read flows end to end.
func TestContract_HappyPath(t *testing.T) {
	router, nodes := newTestRouterWithNodes(t)

	// Create a user (admin).
	var user contracts.User
	doJSON(t, router, http.MethodPost, "/v1/users", adminTok,
		map[string]any{"telegram_id": 42}, http.StatusCreated, &user)
	if user.RealityShortID == "" || user.UUID == "" {
		t.Fatal("created user missing personal secrets")
	}

	// Create a subscription (billing) — token returned once.
	var sub contracts.Subscription
	doJSON(t, router, http.MethodPost, "/v1/subscriptions", billTok,
		map[string]any{"user_id": user.ID, "plan": "unlimited"}, http.StatusCreated, &sub)
	if sub.Token == "" {
		t.Fatal("subscription create must return the plaintext token once")
	}

	// Reading it back must NOT expose the token.
	var got contracts.Subscription
	doJSON(t, router, http.MethodGet, "/v1/subscriptions/"+sub.ID, subTok, nil, http.StatusOK, &got)
	if got.Token != "" {
		t.Fatal("GET subscription leaked the token")
	}

	// Seed an ACTIVE node via internal setup — registration refuses status:active,
	// so a serving node is created directly through the repo, not the HTTP path.
	node := contracts.Node{
		ID: "n-happy", Role: contracts.NodeRoleCombined, Status: contracts.NodeStatusActive, Region: "eu",
		Transports: []contracts.Transport{{
			Tag: "r0", Type: contracts.TransportVlessReality, Version: contracts.TransportVersionV1,
			Port: 443, Enabled: true,
			VlessReality: &contracts.VlessRealityParams{ServerNames: []string{"e.com"}, Dest: "e.com:443", PublicKey: "pk", ShortIDs: []string{"aa"}, Flow: "v"},
		}},
	}
	if err := nodes.Create(context.Background(), node); err != nil {
		t.Fatalf("seed active node: %v", err)
	}

	// Agent C fetches the structured set (subscription role).
	var set map[string]any
	doJSON(t, router, http.MethodGet, "/v1/users/"+user.ID+"/subscription-set", subTok, nil, http.StatusOK, &set)
	nodesOut, _ := set["nodes"].([]any)
	if len(nodesOut) != 1 {
		t.Fatalf("expected 1 active node in set, got %d", len(nodesOut))
	}
}

// TestContract_SignalIngestWired checks the additive telemetry endpoint.
func TestContract_SignalIngestWired(t *testing.T) {
	router, nodes := newTestRouterWithNodes(t)
	// Seed an active node directly (registration refuses status:active).
	node := contracts.Node{
		ID: "n-sig", Role: contracts.NodeRoleCombined, Status: contracts.NodeStatusActive,
		Transports: []contracts.Transport{{
			Tag: "r0", Type: contracts.TransportVlessReality, Version: contracts.TransportVersionV1,
			Port: 443, Enabled: true,
			VlessReality: &contracts.VlessRealityParams{ServerNames: []string{"e.com"}, Dest: "e.com:443", PublicKey: "pk", ShortIDs: []string{"aa"}, Flow: "v"},
		}},
	}
	if err := nodes.Create(context.Background(), node); err != nil {
		t.Fatalf("seed active node: %v", err)
	}

	body := map[string]any{"aggregates": []map[string]any{{
		"node_id": "n-sig", "region": "RU-MOW", "total": 100,
		"by_signal": map[string]int{"dpi_block": 90, "ok": 10},
	}}}
	var resp map[string]any
	doJSON(t, router, http.MethodPost, "/v1/signals/aggregate", teleTok, body, http.StatusAccepted, &resp)
	changes, _ := resp["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("expected 1 node status change from block signals, got %v", resp["changes"])
	}
}

func doJSON(t *testing.T, router http.Handler, method, path, token string, body any, wantCode int, out any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != wantCode {
		t.Fatalf("%s %s = %d, want %d (body: %s)", method, path, rec.Code, wantCode, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode response: %v (body: %s)", err, rec.Body.String())
		}
	}
}
