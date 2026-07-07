package contracts

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// sampleBundle builds a bundle exercising all four transport families with
// per-user secrets. No real domains/IPs — fixtures only.
func sampleBundle() SubscriptionBundle {
	return SubscriptionBundle{
		User: User{
			ID:             "u-1",
			Status:         UserStatusActive,
			RealityShortID: "ab12",
			UUID:           "00000000-0000-0000-0000-000000000001",
			DeviceLimit:    3,
		},
		Nodes: []Node{{
			ID:      "n-1",
			Role:    NodeRoleCombined,
			Status:  NodeStatusActive,
			EntryIP: "198.51.100.10",
			Transports: []Transport{
				{Tag: "reality-1", Type: TransportVlessReality, Version: TransportVersionV1, Port: 443, Enabled: true,
					VlessReality: &VlessRealityParams{ServerNames: []string{"example-cdn.test"}, Dest: "example-cdn.test:443", PublicKey: "PBK", ShortIDs: []string{"ab12"}, Flow: "xtls-rprx-vision", Fingerprint: "chrome"}},
				{Tag: "hy2-1", Type: TransportHysteria2, Version: TransportVersionV1, Port: 8443, Enabled: true,
					Hysteria2: &Hysteria2Params{Password: "pw", SNI: "example-cdn.test", Obfs: "salamander", ObfsPassword: "obf"}},
				{Tag: "ss-1", Type: TransportShadowsocks2022, Version: TransportVersionV1, Port: 9000, Enabled: true,
					Shadowsocks2022: &Shadowsocks2022Params{Method: "2022-blake3-aes-256-gcm", PSK: "cHNr"}},
				{Tag: "awg-1", Type: TransportAmneziaWG, Version: TransportVersionV1, Port: 51820, Enabled: true,
					AmneziaWG: &AmneziaWGParams{PublicKey: "WGK", MTU: 1420, Jc: 4, Jmin: 40, Jmax: 70, S1: 50, S2: 60, H1: 1, H2: 2, H3: 3, H4: 4}},
			},
		}},
		GeneratedAt: time.Unix(0, 0).UTC(),
	}
}

func TestBase64ListDecodesAndCarriesPerUserSecrets(t *testing.T) {
	b := sampleBundle()
	raw, err := base64.StdEncoding.DecodeString(b.ToBase64List())
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	list := string(raw)
	// AmneziaWG has no URI scheme; it must be absent from the base64 list.
	if strings.Contains(list, "awg-1") {
		t.Errorf("amneziawg leaked into base64 URI list:\n%s", list)
	}
	for _, want := range []string{"vless://", "hysteria2://", "ss://", "sid=ab12"} {
		if !strings.Contains(list, want) {
			t.Errorf("base64 list missing %q:\n%s", want, list)
		}
	}
}

func TestSingBoxJSONIsValidAndCoversAllTransports(t *testing.T) {
	b := sampleBundle()
	data, err := b.ToSingBoxJSON()
	if err != nil {
		t.Fatalf("sing-box json: %v", err)
	}
	var cfg SingBoxConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("sing-box json not valid: %v", err)
	}
	if len(cfg.Outbounds) != 4 {
		t.Fatalf("want 4 outbounds (all transports), got %d", len(cfg.Outbounds))
	}
}

func TestClashYAMLNonEmptyAndPerUser(t *testing.T) {
	b := sampleBundle()
	y := string(b.ToClashMetaYAML())
	if !strings.HasPrefix(y, "proxies:\n") {
		t.Fatalf("clash yaml missing proxies root:\n%s", y)
	}
	if !strings.Contains(y, "short-id: \"ab12\"") {
		t.Errorf("clash yaml missing per-user short-id:\n%s", y)
	}
}

func TestTransportValidateRejectsMismatch(t *testing.T) {
	bad := Transport{Tag: "x", Type: TransportVlessReality, Version: TransportVersionV1,
		Hysteria2: &Hysteria2Params{}}
	if err := bad.Validate(); err == nil {
		t.Error("expected mismatch error when variant does not match type")
	}
}
