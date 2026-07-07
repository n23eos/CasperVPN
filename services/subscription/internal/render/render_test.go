package render

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/subscription/internal/config"
)

// sampleBundle mirrors the frozen-contract test fixture: all four transport
// families with per-user secrets. No real domains/IPs — fixtures only.
func sampleBundle() contracts.SubscriptionBundle {
	return contracts.SubscriptionBundle{
		User: contracts.User{
			ID: "u-1", Status: contracts.UserStatusActive,
			RealityShortID: "ab12", UUID: "00000000-0000-0000-0000-000000000001", DeviceLimit: 3,
		},
		Nodes: []contracts.Node{{
			ID: "n-1", Role: contracts.NodeRoleCombined, Status: contracts.NodeStatusActive, EntryIP: "198.51.100.10",
			Region: "eu-central",
			Transports: []contracts.Transport{
				{Tag: "reality-1", Type: contracts.TransportVlessReality, Version: contracts.TransportVersionV1, Port: 443, Enabled: true,
					VlessReality: &contracts.VlessRealityParams{ServerNames: []string{"example-cdn.test"}, Dest: "example-cdn.test:443", PublicKey: "PBK", ShortIDs: []string{"ab12"}, Flow: "xtls-rprx-vision", Fingerprint: "chrome"}},
				{Tag: "hy2-1", Type: contracts.TransportHysteria2, Version: contracts.TransportVersionV1, Port: 8443, Enabled: true,
					Hysteria2: &contracts.Hysteria2Params{Password: "pw", SNI: "example-cdn.test", Obfs: "salamander", ObfsPassword: "obf"}},
				{Tag: "ss-1", Type: contracts.TransportShadowsocks2022, Version: contracts.TransportVersionV1, Port: 9000, Enabled: true,
					Shadowsocks2022: &contracts.Shadowsocks2022Params{Method: "2022-blake3-aes-256-gcm", PSK: "cHNr"}},
				{Tag: "awg-1", Type: contracts.TransportAmneziaWG, Version: contracts.TransportVersionV1, Port: 51820, Enabled: true,
					AmneziaWG: &contracts.AmneziaWGParams{PublicKey: "WGK", MTU: 1420, Jc: 4, Jmin: 40, Jmax: 70, S1: 50, S2: 60, H1: 1, H2: 2, H3: 3, H4: 4}},
			},
		}},
		GeneratedAt: time.Unix(0, 0).UTC(),
	}
}

func testPolicy(t *testing.T) config.RoutingPolicy {
	t.Helper()
	p, err := config.LoadRoutingPolicy(filepath.FromSlash("../../config/routing.ru.json"))
	if err != nil {
		t.Fatalf("load routing policy: %v", err)
	}
	return p
}

// checkGolden compares got to testdata/<name>, regenerating when UPDATE_GOLDEN=1.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 to create): %v", name, err)
	}
	if string(want) != string(got) {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

func TestRenderBase64Golden(t *testing.T) {
	r := New(testPolicy(t))
	body, ct, err := r.Render(FormatBase64, sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	if ct != CTBase64 {
		t.Errorf("content-type = %q", ct)
	}
	raw, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	list := string(raw)
	if strings.Contains(list, "awg-1") {
		t.Errorf("amneziawg leaked into base64 list:\n%s", list)
	}
	for _, want := range []string{"vless://", "hysteria2://", "ss://", "sid=ab12"} {
		if !strings.Contains(list, want) {
			t.Errorf("base64 list missing %q:\n%s", want, list)
		}
	}
	checkGolden(t, "subscription.base64.txt", body)
}

func TestRenderSingBoxGolden(t *testing.T) {
	r := New(testPolicy(t))
	body, ct, err := r.Render(FormatSingBox, sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	if ct != CTSingBox {
		t.Errorf("content-type = %q", ct)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("sing-box output is not valid JSON: %v", err)
	}
	outs, _ := doc["outbounds"].([]interface{})
	// 4 node transports + auto + proxy + direct
	if len(outs) != 7 {
		t.Fatalf("want 7 outbounds, got %d", len(outs))
	}
	route, _ := doc["route"].(map[string]interface{})
	if route["final"] != config.TagProxy {
		t.Errorf("route.final = %v, want %q", route["final"], config.TagProxy)
	}
	if _, ok := doc["dns"]; !ok {
		t.Error("sing-box output missing dns block (split-tunnel)")
	}
	checkGolden(t, "subscription.singbox.json", body)
}

func TestRenderClashGolden(t *testing.T) {
	r := New(testPolicy(t))
	body, ct, err := r.Render(FormatClash, sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	if ct != CTClash {
		t.Errorf("content-type = %q", ct)
	}
	y := string(body)
	for _, want := range []string{"proxies:\n", "proxy-groups:", "rule-providers:", "short-id: \"ab12\"", "MATCH,PROXY"} {
		if !strings.Contains(y, want) {
			t.Errorf("clash yaml missing %q:\n%s", want, y)
		}
	}
	checkGolden(t, "subscription.clash.yaml", body)
}

func TestNegotiate(t *testing.T) {
	cases := []struct {
		name, format, accept, ua string
		want                     Format
	}{
		{"explicit-clash", "clash", "application/json", "sing-box", FormatClash},
		{"accept-json", "", "application/json", "", FormatSingBox},
		{"accept-yaml", "", "application/yaml", "", FormatClash},
		{"ua-happ", "", "", "Happ/1.0", FormatBase64},
		{"ua-mihomo", "", "", "mihomo/1.18", FormatClash},
		{"default", "", "", "curl/8", FormatBase64},
	}
	for _, c := range cases {
		if got := Negotiate(c.format, c.accept, c.ua); got != c.want {
			t.Errorf("%s: Negotiate = %q, want %q", c.name, got, c.want)
		}
	}
}
