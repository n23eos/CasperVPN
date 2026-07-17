package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

func newRegisterSvc() (*NodeService, *fakeNodeRepo) {
	nodes := newFakeNodeRepo()
	return NewNodeService(nodes, newFakeRotationRepo(), newFakeQueue()), nodes
}

// Registration must never create a serving node: status:active is rejected (not
// silently downgraded), and no active node results — reaching active is only via
// the guarded /activate.
func TestRegister_RejectsActiveAndDoesNotServe(t *testing.T) {
	svc, nodes := newRegisterSvc()
	_, err := svc.Register(context.Background(), activeNode("n1", "1.2.3.4"), "orch")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("register active: err = %v, want ErrConflict", err)
	}
	active, _ := nodes.ListActive(context.Background())
	if len(active) != 0 {
		t.Fatalf("rejected active register produced %d active node(s)", len(active))
	}
}

func TestRegister_DefaultsToProvisioning(t *testing.T) {
	svc, _ := newRegisterSvc()
	n := activeNode("", "1.2.3.4")
	n.Status = "" // no status supplied
	got, err := svc.Register(context.Background(), n, "orch")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if got.Status != contracts.NodeStatusProvisioning {
		t.Fatalf("status = %q, want provisioning", got.Status)
	}
}

func TestRegister_AllowsExplicitProvisioning(t *testing.T) {
	svc, _ := newRegisterSvc()
	n := activeNode("n2", "1.2.3.4")
	n.Status = contracts.NodeStatusProvisioning
	got, err := svc.Register(context.Background(), n, "orch")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if got.Status != contracts.NodeStatusProvisioning {
		t.Fatalf("status = %q, want provisioning", got.Status)
	}
}
