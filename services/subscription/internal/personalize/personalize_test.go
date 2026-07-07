package personalize

import (
	"testing"

	"github.com/caspervpn/contracts"
)

func tr(tag string, typ contracts.TransportType) contracts.Transport {
	t := contracts.Transport{Tag: tag, Type: typ, Version: contracts.TransportVersionV1, Port: 443, Enabled: true}
	switch typ {
	case contracts.TransportVlessReality:
		t.VlessReality = &contracts.VlessRealityParams{ServerNames: []string{"x.test"}, PublicKey: "k", Flow: "xtls-rprx-vision"}
	case contracts.TransportHysteria2:
		t.Hysteria2 = &contracts.Hysteria2Params{Password: "p", SNI: "x.test"}
	case contracts.TransportAmneziaWG:
		t.AmneziaWG = &contracts.AmneziaWGParams{PublicKey: "k"}
	case contracts.TransportShadowsocks2022:
		t.Shadowsocks2022 = &contracts.Shadowsocks2022Params{Method: "2022-blake3-aes-256-gcm", PSK: "cHNr"}
	}
	return t
}

func node(id, region string, status contracts.NodeStatus, labels map[string]string, transports ...contracts.Transport) contracts.Node {
	return contracts.Node{ID: id, Role: contracts.NodeRoleCombined, Status: status, Region: region, Labels: labels, Transports: transports}
}

func multiTransportNode(id, region string) contracts.Node {
	return node(id, region, contracts.NodeStatusActive, nil,
		tr(id+"-r", contracts.TransportVlessReality),
		tr(id+"-h", contracts.TransportHysteria2),
		tr(id+"-a", contracts.TransportAmneziaWG))
}

func bundleOf(nodes ...contracts.Node) contracts.SubscriptionBundle {
	return contracts.SubscriptionBundle{
		User:  contracts.User{ID: "u", Status: contracts.UserStatusActive, RealityShortID: "ab12", UUID: "uuid"},
		Nodes: nodes,
	}
}

func TestFilterKeepsMultiTransport(t *testing.T) {
	b := bundleOf(multiTransportNode("n1", "eu"), multiTransportNode("n2", "eu"))
	res := Filter(b, contracts.SubscriptionPlanUnlimited, Params{})
	if len(res.Types) < 3 {
		t.Fatalf("want >=3 transport families, got %v", res.Types)
	}
}

func TestFilterDropsBlockedNodes(t *testing.T) {
	blockedByStatus := multiTransportNode("bad1", "eu")
	blockedByStatus.Status = contracts.NodeStatusBlocked
	blockedByLabel := multiTransportNode("bad2", "eu")
	blockedByLabel.Labels = map[string]string{"blocked": "true"}
	good := multiTransportNode("good", "eu")

	res := Filter(bundleOf(blockedByStatus, blockedByLabel, good), contracts.SubscriptionPlanUnlimited, Params{})
	if len(res.Bundle.Nodes) != 1 || res.Bundle.Nodes[0].ID != "good" {
		t.Fatalf("blocked nodes not dropped: %+v", nodeIDs(res.Bundle.Nodes))
	}
}

func TestFilterRegionPrefersButNeverStrands(t *testing.T) {
	b := bundleOf(multiTransportNode("n1", "eu"), multiTransportNode("n2", "asia"))
	// region with matches -> restricted
	res := Filter(b, contracts.SubscriptionPlanUnlimited, Params{Region: "asia"})
	if len(res.Bundle.Nodes) != 1 || res.Bundle.Nodes[0].ID != "n2" {
		t.Fatalf("region filter wrong: %v", nodeIDs(res.Bundle.Nodes))
	}
	// region with no match -> ignored (not stranded)
	res = Filter(b, contracts.SubscriptionPlanUnlimited, Params{Region: "mars"})
	if len(res.Bundle.Nodes) != 2 {
		t.Fatalf("unknown region should not strand user, got %v", nodeIDs(res.Bundle.Nodes))
	}
}

func TestFilterBasicPlanCaps(t *testing.T) {
	var nodes []contracts.Node
	for i := 0; i < maxBasicNodes+3; i++ {
		nodes = append(nodes, multiTransportNode("n"+string(rune('a'+i)), "eu"))
	}
	res := Filter(bundleOf(nodes...), contracts.SubscriptionPlanBasic, Params{})
	if len(res.Bundle.Nodes) != maxBasicNodes {
		t.Fatalf("basic plan should cap to %d nodes, got %d", maxBasicNodes, len(res.Bundle.Nodes))
	}
}

func TestFilterRelaxesToPreserveDiversity(t *testing.T) {
	// A premium single-transport node plus a full multi-transport node. Basic
	// plan would drop the premium node; that is fine here because diversity is
	// preserved by the multi node. Now make the ONLY multi-transport source
	// premium so basic filtering would collapse to a monoculture, forcing relax.
	monoBasic := node("mono", "eu", contracts.NodeStatusActive, nil, tr("mono-r", contracts.TransportVlessReality))
	richPremium := multiTransportNode("rich", "eu")
	richPremium.Labels = map[string]string{"tier": "premium"}

	res := Filter(bundleOf(monoBasic, richPremium), contracts.SubscriptionPlanBasic, Params{})
	if !res.Relaxed {
		t.Fatalf("expected relax to preserve diversity")
	}
	if len(res.Types) < minTransportDiversity {
		t.Fatalf("relax must restore diversity, got %v", res.Types)
	}
}

func nodeIDs(nodes []contracts.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}
