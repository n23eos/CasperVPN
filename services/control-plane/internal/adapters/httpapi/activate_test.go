package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/memory"
)

// seedActive writes an ACTIVE node straight to the repo. Registration refuses
// status:active (a fresh node must be promoted via the guarded activation flow),
// so tests that need a serving node as a fixture use this internal seam instead of
// the HTTP path — never a production bypass.
func seedActive(t *testing.T, nodes *memory.Nodes, body string) {
	t.Helper()
	var n contracts.Node
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatalf("seed active node: %v", err)
	}
	if err := nodes.Create(context.Background(), n); err != nil {
		t.Fatalf("seed active node %s: %v", n.ID, err)
	}
}

const (
	vlessTr = `{"tag":"v","type":"vless-reality","version":"v1","port":443,"enabled":true,"priority":0,"vless_reality":{"server_names":["x"],"dest":"x:443","public_key":"p","short_ids":["aa11"],"flow":"xtls-rprx-vision"}}`
	hy2Tr   = `{"tag":"h","type":"hysteria2","version":"v1","port":8443,"enabled":true,"priority":1,"hysteria2":{"password":"pw","sni":"x"}}`
)

func postNode(t *testing.T, r http.Handler, body string) {
	t.Helper()
	rec := do(t, r, http.MethodPost, "/v1/nodes", orchTok, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post node: %d: %s", rec.Code, rec.Body)
	}
}

// seedActivatable sets up an eligible user, an ACTIVE paired exit, and a
// PROVISIONING entry with two distinct enabled client transports. Returns the
// entry id and the current allow-list revision.
func seedActivatable(t *testing.T, r http.Handler, nodes *memory.Nodes, entryTransports string) (entryID, revision string) {
	t.Helper()
	// eligible user (active account + active subscription)
	rec := do(t, r, http.MethodPost, "/v1/users", adminTok, `{"telegram_id":7}`)
	var u contracts.User
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	do(t, r, http.MethodPost, "/v1/subscriptions", adminTok, `{"user_id":"`+u.ID+`","plan":"basic"}`)

	// The paired exit must already be active — seed it via the repo (Register refuses
	// active); the entry is provisioning, so it still goes through the HTTP path.
	seedActive(t, nodes, `{"id":"ex-1","role":"exit","status":"active","entry_node_id":"en-1","provider":"l","cloud":"l","region":"eu","ephemeral_entry_ip":false,"transports":[]}`)
	postNode(t, r, `{"id":"en-1","role":"entry","status":"provisioning","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.1.1","ephemeral_entry_ip":false,"transports":[`+entryTransports+`]}`)

	rec = do(t, r, http.MethodGet, "/v1/nodes/en-1/reality-users", orchTok, "")
	var al contracts.NodeRealityUsers
	if err := json.Unmarshal(rec.Body.Bytes(), &al); err != nil {
		t.Fatal(err)
	}
	return "en-1", al.Revision
}

func activate(t *testing.T, r http.Handler, id, token, revision string) *httptest.ResponseRecorder {
	return do(t, r, http.MethodPost, "/v1/nodes/"+id+"/activate", token, `{"expected_revision":"`+revision+`"}`)
}

func activateExit(t *testing.T, r http.Handler, id, token, evidence string) *httptest.ResponseRecorder {
	return do(t, r, http.MethodPost, "/v1/nodes/"+id+"/activate", token, `{"expected_revision":"","evidence":"`+evidence+`"}`)
}

func TestActivate_HappyPath(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	id, rev := seedActivatable(t, r, nodes, vlessTr+","+hy2Tr)

	rec := activate(t, r, id, orchTok, rev)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var n contracts.Node
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatal(err)
	}
	if n.Status != contracts.NodeStatusActive {
		t.Errorf("status = %q, want active", n.Status)
	}
}

func TestActivate_StaleRevision(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	id, _ := seedActivatable(t, r, nodes, vlessTr+","+hy2Tr)
	rec := activate(t, r, id, orchTok, "stale-revision")
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("409 must also be no-store")
	}
}

func TestActivate_NotProvisioning(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	id, rev := seedActivatable(t, r, nodes, vlessTr+","+hy2Tr)
	if rec := activate(t, r, id, orchTok, rev); rec.Code != http.StatusOK {
		t.Fatalf("first activate: %d", rec.Code)
	}
	if rec := activate(t, r, id, orchTok, rev); rec.Code != http.StatusConflict {
		t.Errorf("re-activate active node: got %d, want 409", rec.Code)
	}
}

func TestActivate_TooFewTransportTypes(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	id, rev := seedActivatable(t, r, nodes, vlessTr) // single type
	if rec := activate(t, r, id, orchTok, rev); rec.Code != http.StatusConflict {
		t.Errorf("one transport type: got %d, want 409", rec.Code)
	}
}

func TestActivate_SameTypeIsNotDiversity(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	vless2 := `{"tag":"v2","type":"vless-reality","version":"v1","port":444,"enabled":true,"priority":2,"vless_reality":{"server_names":["y"],"dest":"y:443","public_key":"q","short_ids":["bb22"],"flow":"xtls-rprx-vision"}}`
	id, rev := seedActivatable(t, r, nodes, vlessTr+","+vless2) // two records, one type
	if rec := activate(t, r, id, orchTok, rev); rec.Code != http.StatusConflict {
		t.Errorf("two same-type transports: got %d, want 409", rec.Code)
	}
}

func TestActivate_ExitNotActive(t *testing.T) {
	r := newTestRouter(t)
	postNode(t, r, `{"id":"ex-2","role":"exit","status":"provisioning","entry_node_id":"en-2","provider":"l","cloud":"l","region":"eu","ephemeral_entry_ip":false,"transports":[]}`)
	postNode(t, r, `{"id":"en-2","role":"entry","status":"provisioning","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.1.2","ephemeral_entry_ip":false,"transports":[`+vlessTr+`,`+hy2Tr+`]}`)
	rec := do(t, r, http.MethodGet, "/v1/nodes/en-2/reality-users", orchTok, "")
	var al contracts.NodeRealityUsers
	_ = json.Unmarshal(rec.Body.Bytes(), &al)
	if rec := activate(t, r, "en-2", orchTok, al.Revision); rec.Code != http.StatusConflict {
		t.Errorf("exit not active: got %d, want 409", rec.Code)
	}
}

func TestActivate_ExitMissing(t *testing.T) {
	r := newTestRouter(t)
	postNode(t, r, `{"id":"en-3","role":"entry","status":"provisioning","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.1.3","ephemeral_entry_ip":false,"transports":[`+vlessTr+`,`+hy2Tr+`]}`)
	rec := do(t, r, http.MethodGet, "/v1/nodes/en-3/reality-users", orchTok, "")
	var al contracts.NodeRealityUsers
	_ = json.Unmarshal(rec.Body.Bytes(), &al)
	if rec := activate(t, r, "en-3", orchTok, al.Revision); rec.Code != http.StatusConflict {
		t.Errorf("exit missing: got %d, want 409", rec.Code)
	}
}

func TestActivate_RBAC(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	id, rev := seedActivatable(t, r, nodes, vlessTr+","+hy2Tr)
	if rec := activate(t, r, id, subTok, rev); rec.Code != http.StatusForbidden {
		t.Errorf("subscription role: got %d, want 403", rec.Code)
	}
	if rec := activate(t, r, id, "", rev); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauth: got %d, want 401", rec.Code)
	}
}

func TestActivate_MissingNode(t *testing.T) {
	r := newTestRouter(t)
	if rec := activate(t, r, "ghost", orchTok, "x"); rec.Code != http.StatusNotFound {
		t.Errorf("missing node: got %d, want 404", rec.Code)
	}
}

func TestActivate_ExitThenEntrySequence(t *testing.T) {
	r := newTestRouter(t)
	// eligible user
	rec := do(t, r, http.MethodPost, "/v1/users", adminTok, `{"telegram_id":8}`)
	var u contracts.User
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	do(t, r, http.MethodPost, "/v1/subscriptions", adminTok, `{"user_id":"`+u.ID+`","plan":"basic"}`)

	postNode(t, r, `{"id":"en-seq","role":"entry","status":"provisioning","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.9.1","ephemeral_entry_ip":false,"transports":[`+vlessTr+`,`+hy2Tr+`]}`)
	postNode(t, r, `{"id":"ex-seq","role":"exit","status":"provisioning","entry_node_id":"en-seq","provider":"l","cloud":"l","region":"eu","ephemeral_entry_ip":false,"transports":[]}`)

	rec = do(t, r, http.MethodGet, "/v1/nodes/en-seq/reality-users", orchTok, "")
	var al contracts.NodeRealityUsers
	_ = json.Unmarshal(rec.Body.Bytes(), &al)

	// entry cannot activate while exit is still provisioning.
	if rec := activate(t, r, "en-seq", orchTok, al.Revision); rec.Code != http.StatusConflict {
		t.Fatalf("entry before exit active: got %d, want 409", rec.Code)
	}
	// exit without evidence must NOT activate.
	if rec := activate(t, r, "ex-seq", orchTok, ""); rec.Code != http.StatusConflict {
		t.Fatalf("exit without evidence: got %d, want 409", rec.Code)
	}
	// exit with wrong evidence must NOT activate.
	if rec := activateExit(t, r, "ex-seq", orchTok, "nonsense"); rec.Code != http.StatusConflict {
		t.Fatalf("exit with bad evidence: got %d, want 409", rec.Code)
	}
	// activate exit with valid evidence (revision irrelevant for an exit).
	if rec := activateExit(t, r, "ex-seq", orchTok, "exit_data_plane_verified"); rec.Code != http.StatusOK {
		t.Fatalf("activate exit: got %d: %s", rec.Code, rec.Body)
	}
	// now the entry activates.
	rec = activate(t, r, "en-seq", orchTok, al.Revision)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate entry after exit: got %d: %s", rec.Code, rec.Body)
	}
	var n contracts.Node
	_ = json.Unmarshal(rec.Body.Bytes(), &n)
	if n.Status != contracts.NodeStatusActive {
		t.Errorf("entry status = %q, want active", n.Status)
	}
}

// nodeBody builds a valid entry-node PATCH/POST body with a given id and status.
func nodeBody(id, status string) string {
	return `{"id":"` + id + `","role":"entry","status":"` + status + `","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.1.9","ephemeral_entry_ip":false,"transports":[` + vlessTr + `,` + hy2Tr + `]}`
}

// Reaching active is ONLY via the guarded /activate path. A plain PATCH into active
// from ANY non-active state must be refused — including a resurrection of a
// decommissioned draining/retired node.
func TestPatch_NonActiveToActiveForbidden(t *testing.T) {
	r := newTestRouter(t)
	for _, src := range []string{"provisioning", "degraded", "blocked", "draining", "retired"} {
		id := "na-" + src
		postNode(t, r, nodeBody(id, src))
		rec := do(t, r, http.MethodPatch, "/v1/nodes/"+id, orchTok, nodeBody(id, "active"))
		if rec.Code != http.StatusConflict {
			t.Errorf("PATCH %s->active: got %d, want 409 (use /activate): %s", src, rec.Code, rec.Body)
		}
	}
}

// Staying active (field update) and leaving active (->degraded) are unaffected.
func TestPatch_ActiveTransitionsUnaffected(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	seedActive(t, nodes, nodeBody("act-1", "active"))
	if rec := do(t, r, http.MethodPatch, "/v1/nodes/act-1", orchTok, nodeBody("act-1", "active")); rec.Code != http.StatusOK {
		t.Errorf("PATCH active->active: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := do(t, r, http.MethodPatch, "/v1/nodes/act-1", orchTok, nodeBody("act-1", "degraded")); rec.Code != http.StatusOK {
		t.Errorf("PATCH active->degraded: got %d, want 200: %s", rec.Code, rec.Body)
	}
}
