package contracts

import (
	"encoding/base64"
	"strings"
	"testing"
)

// hy2Bundle builds a one-node bundle with a single hysteria2 transport carrying
// the given password + insecure flag.
func hy2Bundle(password string, insecure bool) SubscriptionBundle {
	return SubscriptionBundle{
		User: User{ID: "u", Status: UserStatusActive, RealityShortID: "ab12", UUID: "uuid-1", DeviceLimit: 1},
		Nodes: []Node{{
			ID: "n", Role: NodeRoleCombined, Status: NodeStatusActive, EntryIP: "1.2.3.4",
			Transports: []Transport{{
				Tag: "hy2", Type: TransportHysteria2, Version: TransportVersionV1, Port: 8443, Enabled: true,
				Hysteria2: &Hysteria2Params{Password: password, SNI: "cdn.example", Insecure: insecure},
			}},
		}},
	}
}

func TestHysteria2_SingBoxCarriesPasswordAndInsecure(t *testing.T) {
	b := hy2Bundle("secret-pw-123", true)
	out := b.ToSingBox()
	if len(out.Outbounds) != 1 {
		t.Fatalf("want 1 outbound, got %d", len(out.Outbounds))
	}
	o := out.Outbounds[0]
	if o["password"] != "secret-pw-123" {
		t.Errorf("password = %v, want secret-pw-123", o["password"])
	}
	tls, _ := o["tls"].(map[string]interface{})
	if tls["insecure"] != true {
		t.Errorf("tls.insecure = %v, want true", tls["insecure"])
	}
}

func TestHysteria2_ProdIsSecureByDefault(t *testing.T) {
	b := hy2Bundle("pw", false) // production: trusted cert, insecure omitted/false
	tls, _ := b.ToSingBox().Outbounds[0]["tls"].(map[string]interface{})
	if tls["insecure"] == true {
		t.Error("production hysteria2 must not be insecure")
	}
}

func TestHysteria2_Base64URICarriesPassword(t *testing.T) {
	b := hy2Bundle("pw-xyz", true)
	raw, err := base64.StdEncoding.DecodeString(b.ToBase64List())
	if err != nil {
		t.Fatal(err)
	}
	joined := string(raw)
	if !strings.Contains(joined, "hysteria2://") || !strings.Contains(joined, "pw-xyz") {
		t.Errorf("base64 URI must carry the hysteria2 password: %q", joined)
	}
	if !strings.Contains(joined, "insecure=1") {
		t.Errorf("e2e insecure flag must appear in the URI: %q", joined)
	}
}
