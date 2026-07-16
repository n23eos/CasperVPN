package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caspervpn/contracts"
)

// demote a node and return the response recorder.
func demote(t *testing.T, r http.Handler, id, token string) *httptest.ResponseRecorder {
	return do(t, r, http.MethodPost, "/v1/nodes/"+id+"/demote", token, "")
}

func TestDemote_ActiveNodeGoesProvisioning(t *testing.T) {
	r := newTestRouter(t)
	postNode(t, r, `{"id":"dn-1","role":"entry","status":"active","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.1.1","ephemeral_entry_ip":false,"transports":[`+vlessTr+`]}`)
	rec := demote(t, r, "dn-1", orchTok)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	var n contracts.Node
	_ = json.Unmarshal(rec.Body.Bytes(), &n)
	if n.Status != contracts.NodeStatusProvisioning {
		t.Errorf("status = %q, want provisioning", n.Status)
	}
}

func TestDemote_RejectsTerminalStates(t *testing.T) {
	r := newTestRouter(t)
	// draining: PATCH active -> draining, then demote must 409.
	postNode(t, r, `{"id":"dn-2","role":"entry","status":"active","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.1.2","ephemeral_entry_ip":false,"transports":[`+vlessTr+`]}`)
	drain := `{"id":"dn-2","role":"entry","status":"draining","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.1.2","ephemeral_entry_ip":false,"transports":[` + vlessTr + `]}`
	if rec := do(t, r, http.MethodPatch, "/v1/nodes/dn-2", orchTok, drain); rec.Code != http.StatusOK {
		t.Fatalf("set draining: %d", rec.Code)
	}
	if rec := demote(t, r, "dn-2", orchTok); rec.Code != http.StatusConflict {
		t.Errorf("demote draining: got %d, want 409", rec.Code)
	}

	// retired: DELETE sets retired, then demote must 409.
	postNode(t, r, `{"id":"dn-3","role":"entry","status":"active","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.1.3","ephemeral_entry_ip":false,"transports":[`+vlessTr+`]}`)
	if rec := do(t, r, http.MethodDelete, "/v1/nodes/dn-3", orchTok, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("retire: %d", rec.Code)
	}
	if rec := demote(t, r, "dn-3", orchTok); rec.Code != http.StatusConflict {
		t.Errorf("demote retired: got %d, want 409", rec.Code)
	}
}

func TestDemote_RBAC(t *testing.T) {
	r := newTestRouter(t)
	postNode(t, r, `{"id":"dn-4","role":"entry","status":"active","provider":"l","cloud":"l","region":"eu","entry_ip":"1.1.1.4","ephemeral_entry_ip":false,"transports":[`+vlessTr+`]}`)
	if rec := demote(t, r, "dn-4", subTok); rec.Code != http.StatusForbidden {
		t.Errorf("subscription role: got %d, want 403", rec.Code)
	}
	if rec := demote(t, r, "dn-4", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauth: got %d, want 401", rec.Code)
	}
}
