package personalize

import (
	"errors"

	"github.com/caspervpn/contracts"
)

// PublicBundle exposes only the two client transports with personal admission.
// Pair-level Shadowsocks credentials and legacy shared AmneziaWG credentials
// stay on the control plane. Copy nested parameters before personalizing them.
func PublicBundle(b contracts.SubscriptionBundle) (contracts.SubscriptionBundle, error) {
	out := b
	out.User.PrivateKey = ""
	out.User.TelegramID = nil
	out.User.Email = nil
	out.Nodes = nil
	for _, n := range b.Nodes {
		if n.Role == contracts.NodeRoleExit {
			continue
		}
		node := n
		node.Transports = nil
		for _, transport := range n.Transports {
			if !transport.Enabled {
				continue
			}
			switch transport.Type {
			case contracts.TransportVlessReality:
				if transport.VlessReality == nil || b.User.UUID == "" || b.User.RealityShortID == "" {
					return contracts.SubscriptionBundle{}, errors.New("personal VLESS credentials unavailable")
				}
				params := *transport.VlessReality
				params.ShortIDs = []string{b.User.RealityShortID}
				transport.VlessReality = &params
			case contracts.TransportHysteria2:
				if transport.Hysteria2 == nil || b.User.Hysteria2Password == "" {
					return contracts.SubscriptionBundle{}, errors.New("personal Hysteria2 credentials unavailable")
				}
				params := *transport.Hysteria2
				params.Password = b.User.Hysteria2Password
				transport.Hysteria2 = &params
			default:
				continue
			}
			node.Transports = append(node.Transports, transport)
		}
		if len(node.Transports) > 0 {
			out.Nodes = append(out.Nodes, node)
		}
	}
	if len(out.Nodes) == 0 {
		return contracts.SubscriptionBundle{}, errors.New("no eligible VPN nodes")
	}
	return out, nil
}
