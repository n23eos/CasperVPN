// Package aggregate turns a stream of anonymous FieldSignals into per-(region,
// transport) verdicts and control-plane recommendations. Its central defence
// against data poisoning is SOURCE-DIVERSITY counting: verdicts are driven by the
// number of DISTINCT coarse sources that agree, not by the raw signal count. One
// attacker — however many signals it sends — is one voice.
package aggregate

import (
	"strconv"

	"github.com/caspervpn/contracts"
)

// SourceKey is a coarse, non-identifying origin bucket for a signal, used only to
// count independent voices. It is derived from region + ASN (falling back to ISP,
// then a coarse client fingerprint) — NEVER an IP, NEVER a user. It is a poisoning
// weight, not a stored identity.
type SourceKey string

// sourceKey derives the diversity bucket for a signal. Attackers can forge these
// fields, but forging N distinct ASNs is far costlier than sending N signals — which
// is exactly the asymmetry the verdict thresholds exploit.
func sourceKey(s contracts.FieldSignal) SourceKey {
	if s.ASN > 0 {
		return SourceKey("asn:" + strconv.Itoa(s.ASN))
	}
	if s.ISP != "" {
		return SourceKey("isp:" + s.Region + "|" + s.ISP)
	}
	// Weakest bucket: region + platform. Coarse by design; keeps a single origin
	// from masquerading as many when ASN/ISP are absent.
	return SourceKey("geo:" + s.Region + "|" + string(s.Platform))
}

// outcome classifies a signal into the three verdict-relevant buckets.
type outcome int

const (
	outcomeOK       outcome = iota // healthy: ok/success
	outcomeDegraded                // reachable but impaired: throttle
	outcomeBlocked                 // blocked-ish: reset/timeout/dpi/probe/handshake/dns
)

func classify(s contracts.FieldSignal) outcome {
	switch s.SignalType {
	case contracts.SignalThrottle:
		return outcomeDegraded
	case contracts.SignalReset, contracts.SignalTimeout, contracts.SignalDPIBlock,
		contracts.SignalActiveProbe, contracts.SignalHandshakeFail, contracts.SignalDNSPoison:
		return outcomeBlocked
	case contracts.SignalOK:
		return outcomeOK
	}
	// Unknown types never reach here (Validate rejects them). A successful non-OK
	// signal still counts as healthy evidence.
	if s.Success {
		return outcomeOK
	}
	return outcomeBlocked
}
