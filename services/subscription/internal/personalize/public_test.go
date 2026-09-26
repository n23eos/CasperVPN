package personalize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caspervpn/contracts"
)

func TestPublicBundleIsolatesPersonalCredentials(t *testing.T) {
	b := bundleOf(node("entry", "eu", contracts.NodeStatusActive, nil,
		tr("vless", contracts.TransportVlessReality), tr("hy2", contracts.TransportHysteria2),
		tr("pair", contracts.TransportShadowsocks2022), tr("legacy", contracts.TransportAmneziaWG)))
	b.User.Hysteria2Password = "personal-password"
	b.User.PrivateKey = "private-key-must-not-leak"
	b.Nodes[0].Transports[0].VlessReality.ShortIDs = []string{"someone-else"}
	b.Nodes[0].Transports[1].Hysteria2.Password = "shared-password-must-not-leak"
	public, err := PublicBundle(b)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-key-must-not-leak", "shared-password-must-not-leak", "someone-else", "cHNr"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("public bundle leaked %s", secret)
		}
	}
	if len(public.Nodes[0].Transports) != 2 || !strings.Contains(string(data), "personal-password") {
		t.Fatal("missing personal transport diversity")
	}
	if b.Nodes[0].Transports[1].Hysteria2.Password != "shared-password-must-not-leak" || b.Nodes[0].Transports[0].VlessReality.ShortIDs[0] != "someone-else" {
		t.Fatal("personalization mutated the provider cache")
	}
}

func TestPublicBundleRequiresPersonalAdmission(t *testing.T) {
	b := bundleOf(multiTransportNode("entry", "eu"))
	if _, err := PublicBundle(b); err == nil {
		t.Fatal("missing personal Hysteria2 secret must fail closed")
	}
	b.User.Hysteria2Password = "p"
	b.Nodes[0].Role = contracts.NodeRoleExit
	if _, err := PublicBundle(b); err == nil {
		t.Fatal("exit node must not become a public entry")
	}
}
