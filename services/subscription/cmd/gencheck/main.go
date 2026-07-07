// Command gencheck prints a sing-box config to stdout for CI validation with
// `sing-box check`. It renders the subscription service's real routing wrapper
// around a fixture bundle of the mainline-supported transports (VLESS-REALITY,
// Hysteria2, Shadowsocks-2022).
//
// AmneziaWG is intentionally excluded here: mainline sing-box does not implement
// it (nodes/clients run an AmneziaWG-capable core). Its presence in the full
// subscription output is covered by the structural Go tests instead. See
// docs/subscription.md.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/subscription/internal/config"
	"github.com/caspervpn/subscription/internal/render"
)

func main() {
	policyPath := "config/routing.ru.json"
	if len(os.Args) > 1 {
		policyPath = os.Args[1]
	}
	policy, err := config.LoadRoutingPolicy(policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gencheck: %v\n", err)
		os.Exit(1)
	}

	bundle := contracts.SubscriptionBundle{
		User: contracts.User{
			ID: "u-check", Status: contracts.UserStatusActive,
			RealityShortID: "ab12", UUID: "00000000-0000-0000-0000-000000000001",
		},
		Nodes: []contracts.Node{{
			ID: "n-check", Role: contracts.NodeRoleCombined, Status: contracts.NodeStatusActive,
			EntryIP: "198.51.100.10", Region: "eu-central",
			Transports: []contracts.Transport{
				{Tag: "reality-1", Type: contracts.TransportVlessReality, Version: contracts.TransportVersionV1, Port: 443, Enabled: true,
					// PublicKey is a valid 32-byte x25519 raw-url base64; PSK is a valid
					// 32-byte std base64 — real values from control-plane, placeholders here.
					VlessReality: &contracts.VlessRealityParams{ServerNames: []string{"example-cdn.test"}, Dest: "example-cdn.test:443", PublicKey: "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE", Flow: "xtls-rprx-vision", Fingerprint: "chrome"}},
				{Tag: "hy2-1", Type: contracts.TransportHysteria2, Version: contracts.TransportVersionV1, Port: 8443, Enabled: true,
					Hysteria2: &contracts.Hysteria2Params{Password: "pw", SNI: "example-cdn.test", Obfs: "salamander", ObfsPassword: "obf"}},
				{Tag: "ss-1", Type: contracts.TransportShadowsocks2022, Version: contracts.TransportVersionV1, Port: 9000, Enabled: true,
					Shadowsocks2022: &contracts.Shadowsocks2022Params{Method: "2022-blake3-aes-256-gcm", PSK: "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="}},
			},
		}},
		GeneratedAt: time.Unix(0, 0).UTC(),
	}

	body, _, err := render.New(policy).Render(render.FormatSingBox, bundle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gencheck: render: %v\n", err)
		os.Exit(1)
	}
	os.Stdout.Write(body)
}
