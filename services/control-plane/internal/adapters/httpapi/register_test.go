package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/caspervpn/contracts"
)

func regNode(id, status string) contracts.Node {
	return contracts.Node{
		ID: id, Role: contracts.NodeRoleCombined, Status: contracts.NodeStatus(status), Region: "eu",
		Transports: []contracts.Transport{{
			Tag: "r0", Type: contracts.TransportVlessReality, Version: contracts.TransportVersionV1,
			Port: 443, Enabled: true,
			VlessReality: &contracts.VlessRealityParams{
				ServerNames: []string{"e.com"}, Dest: "e.com:443", PublicKey: "pk", ShortIDs: []string{"aa"}, Flow: "v",
			},
		}},
	}
}

// A POST /v1/nodes with status:active must be refused (409) for EVERY writer role —
// admin does not bypass the gate any more than the orchestrator does. Reaching
// active is only via the guarded /activate.
func TestRegister_ActiveForbiddenForAllWriters(t *testing.T) {
	for _, c := range []struct{ name, tok string }{{"orchestrator", orchTok}, {"admin", adminTok}} {
		router := newTestRouter(t)
		doJSON(t, router, http.MethodPost, "/v1/nodes", c.tok, regNode("reg-"+c.name, "active"), http.StatusConflict, nil)
	}
}

// Registering a provisioning node (the normal node-up path) is unaffected.
func TestRegister_ProvisioningAccepted(t *testing.T) {
	router := newTestRouter(t)
	var created contracts.Node
	doJSON(t, router, http.MethodPost, "/v1/nodes", orchTok, regNode("reg-prov", "provisioning"), http.StatusCreated, &created)
	if created.Status != contracts.NodeStatusProvisioning {
		t.Fatalf("status = %q, want provisioning", created.Status)
	}
}
