package contracts

import (
	"errors"
	"fmt"
)

// Transport is one anti-censorship transport offered by a Node. It is a tagged
// union: Type selects exactly one non-nil variant params pointer. A Node holds
// many Transports at once so the client's fallback cascade can live-switch
// between families (diversity over monoculture — see architecture.md).
//
// Mimicry domains / SNI / dest live INSIDE the variant params as data. Nothing
// here is hardcoded; the control plane fills these from config/DB.
type Transport struct {
	Tag      string           `json:"tag"`      // stable label unique within a Node
	Type     TransportType    `json:"type"`     // selects the active variant below
	Version  TransportVersion `json:"version"`  // params schema revision for Type
	Port     int              `json:"port"`     // listen port on the node
	Enabled  bool             `json:"enabled"`  // false => advertised but not offered
	Priority int              `json:"priority"` // lower = tried earlier in the fallback cascade

	// Exactly one of the following is non-nil, matching Type.
	VlessReality    *VlessRealityParams    `json:"vless_reality,omitempty"`
	AmneziaWG       *AmneziaWGParams       `json:"amneziawg,omitempty"`
	Hysteria2       *Hysteria2Params       `json:"hysteria2,omitempty"`
	Shadowsocks2022 *Shadowsocks2022Params `json:"shadowsocks_2022,omitempty"`
}

// VlessRealityParams configures VLESS + XTLS-Vision + REALITY. REALITY borrows a
// real site's TLS handshake: no own cert, no TLS fingerprint, active probing
// sees the genuine site. ServerNames/Dest ARE the mimicry target and come from
// config — never hardcode them.
type VlessRealityParams struct {
	ServerNames []string `json:"server_names"`          // mimicry SNI(s) presented to the network
	Dest        string   `json:"dest"`                  // real upstream stolen for the handshake (host:port)
	PublicKey   string   `json:"public_key"`            // server REALITY x25519 public key
	ShortIDs    []string `json:"short_ids"`             // pool of allowed short-ids; each user gets one (per-user isolation)
	Flow        string   `json:"flow"`                  // e.g. "xtls-rprx-vision"
	Fingerprint string   `json:"fingerprint,omitempty"` // uTLS client fingerprint, e.g. "chrome"
	SpiderX     string   `json:"spider_x,omitempty"`    // REALITY spiderX path
}

// AmneziaWGParams configures obfuscated WireGuard (defeats entropy/signature WG
// detection) — the speed transport. The Jc/S/H knobs are the obfuscation.
type AmneziaWGParams struct {
	PublicKey string `json:"public_key"` // server WG public key
	MTU       int    `json:"mtu,omitempty"`
	Jc        int    `json:"jc"`   // junk packet count
	Jmin      int    `json:"jmin"` // min junk packet size
	Jmax      int    `json:"jmax"` // max junk packet size
	S1        int    `json:"s1"`   // init packet junk size
	S2        int    `json:"s2"`   // response packet junk size
	H1        int64  `json:"h1"`   // magic header 1
	H2        int64  `json:"h2"`   // magic header 2
	H3        int64  `json:"h3"`   // magic header 3
	H4        int64  `json:"h4"`   // magic header 4
}

// Hysteria2Params configures Hysteria2 over QUIC/HTTP-3 for mobile/lossy nets,
// with an obfs layer and client-side TCP fallback when UDP/443 is cut.
type Hysteria2Params struct {
	Obfs         string `json:"obfs,omitempty"`          // obfuscation type, e.g. "salamander"
	ObfsPassword string `json:"obfs_password,omitempty"` // obfs secret
	Password     string `json:"password"`                // auth secret
	SNI          string `json:"sni"`                     // mimicry SNI (config)
	UpMbps       int    `json:"up_mbps,omitempty"`       // congestion hint
	DownMbps     int    `json:"down_mbps,omitempty"`     // congestion hint
	Insecure     bool   `json:"insecure,omitempty"`      // skip cert verify (self-signed fronts)
}

// Shadowsocks2022Params configures Shadowsocks-2022 (AEAD-2022). Kept as a
// lightweight fallback family; the per-user key is layered on top of PSK.
type Shadowsocks2022Params struct {
	Method string `json:"method"` // e.g. "2022-blake3-aes-256-gcm"
	PSK    string `json:"psk"`    // server pre-shared key (base64)
}

// ErrTransportVariantMismatch is returned when the non-nil variant params do not
// match Type, or when zero/multiple variants are set.
var ErrTransportVariantMismatch = errors.New("contracts: transport variant does not match type")

// Validate checks that Type is known and exactly one matching variant is set.
func (t Transport) Validate() error {
	if !t.Type.Valid() {
		return fmt.Errorf("contracts: unknown transport type %q", t.Type)
	}
	if !t.Version.Valid() {
		return fmt.Errorf("contracts: unknown transport version %q", t.Version)
	}
	set := 0
	if t.VlessReality != nil {
		set++
	}
	if t.AmneziaWG != nil {
		set++
	}
	if t.Hysteria2 != nil {
		set++
	}
	if t.Shadowsocks2022 != nil {
		set++
	}
	if set != 1 {
		return ErrTransportVariantMismatch
	}
	ok := (t.Type == TransportVlessReality && t.VlessReality != nil) ||
		(t.Type == TransportAmneziaWG && t.AmneziaWG != nil) ||
		(t.Type == TransportHysteria2 && t.Hysteria2 != nil) ||
		(t.Type == TransportShadowsocks2022 && t.Shadowsocks2022 != nil)
	if !ok {
		return ErrTransportVariantMismatch
	}
	return nil
}
