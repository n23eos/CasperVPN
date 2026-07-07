package aggregate

import (
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

// TestSimulation_RegionBlockedReality models a region (RU-MOW) where the DPI starts
// dropping VLESS-REALITY: many INDEPENDENT sources report dpi_block, while a second
// transport (Hysteria2) stays healthy and a second region (DE-BE) is unaffected.
// The detector must declare exactly the dead cell and nothing else.
func TestSimulation_RegionBlockedReality(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-2 * time.Minute)
	p := defaultParams()

	var sigs []contracts.FieldSignal
	// RU-MOW / vless-reality: 8 distinct ASNs all blocked → should be DEAD.
	for asn := 100; asn < 108; asn++ {
		sigs = append(sigs, sig("n-ru", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalDPIBlock, at))
	}
	// RU-MOW / hysteria2: 6 distinct ASNs healthy → should stay alive.
	for asn := 200; asn < 206; asn++ {
		sigs = append(sigs, sig("n-ru", contracts.TransportHysteria2, "RU-MOW", asn, contracts.SignalOK, at))
	}
	// DE-BE / vless-reality: 6 distinct ASNs healthy → unaffected.
	for asn := 300; asn < 306; asn++ {
		sigs = append(sigs, sig("n-de", contracts.TransportVlessReality, "DE-BE", asn, contracts.SignalOK, at))
	}

	aggs := Compute(sigs, now, 15*time.Minute, p)

	dead := aggs[Key{Region: "RU-MOW", Transport: contracts.TransportVlessReality}]
	if !dead.Dead {
		t.Fatalf("RU-MOW/vless-reality should be DEAD; reason=%q", dead.Reason)
	}
	if alive := aggs[Key{Region: "RU-MOW", Transport: contracts.TransportHysteria2}]; alive.Dead {
		t.Fatalf("RU-MOW/hysteria2 must not be dead; reason=%q", alive.Reason)
	}
	if de := aggs[Key{Region: "DE-BE", Transport: contracts.TransportVlessReality}]; de.Dead {
		t.Fatalf("DE-BE/vless-reality must not be dead; reason=%q", de.Reason)
	}
}

// TestPoisoning_SingleSourceFlood: one attacker ASN posts a flood of blocked signals
// (distinct SignalIDs) while real users on many other ASNs report OK. Because the
// verdict counts DISTINCT sources, the flood is one voice and cannot kill the
// transport.
func TestPoisoning_SingleSourceFlood(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Minute)
	p := defaultParams()

	var sigs []contracts.FieldSignal
	// Attacker: one ASN, 1000 blocked signals with unique IDs/timestamps.
	for i := 0; i < 1000; i++ {
		s := sig("n1", contracts.TransportVlessReality, "RU-MOW", 66666, contracts.SignalDPIBlock, at.Add(time.Duration(i)*time.Millisecond))
		sigs = append(sigs, s)
	}
	// Honest users: 8 distinct ASNs, all OK.
	for asn := 1; asn <= 8; asn++ {
		sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalOK, at))
	}

	aggs := Compute(sigs, now, 15*time.Minute, p)
	a := aggs[Key{Region: "RU-MOW", Transport: contracts.TransportVlessReality}]
	if a.Dead {
		t.Fatalf("single-source flood must NOT kill transport; blocked=%d distinct=%d reason=%q",
			a.BlockedSources, a.DistinctSources, a.Reason)
	}
	if a.BlockedSources != 1 {
		t.Fatalf("flood should count as 1 blocked source, got %d", a.BlockedSources)
	}
}

// TestPoisoning_TooFewSources: an attacker controlling only a handful of distinct
// ASNs cannot reach the MinSources bar, so no verdict is issued.
func TestPoisoning_TooFewSources(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Minute)
	p := defaultParams()

	var sigs []contracts.FieldSignal
	for asn := 1; asn <= 4; asn++ { // 4 < MinSources(5)
		sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalDPIBlock, at))
	}
	aggs := Compute(sigs, now, 15*time.Minute, p)
	a := aggs[Key{Region: "RU-MOW", Transport: contracts.TransportVlessReality}]
	if a.Dead {
		t.Fatalf("4 sources must not trigger a verdict; reason=%q", a.Reason)
	}
}

// TestPoisoning_LiveTrafficCounterWeights: even many attacker sources cannot kill a
// transport while honest OK traffic dominates (OKShare gate).
func TestPoisoning_LiveTrafficCounterWeights(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Minute)
	p := defaultParams()

	var sigs []contracts.FieldSignal
	for asn := 1; asn <= 5; asn++ { // 5 attacker ASNs blocked (meets MinSources/MinBlocked)
		sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalDPIBlock, at))
	}
	for asn := 100; asn <= 119; asn++ { // 20 honest ASNs OK
		sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalOK, at))
	}
	aggs := Compute(sigs, now, 15*time.Minute, p)
	a := aggs[Key{Region: "RU-MOW", Transport: contracts.TransportVlessReality}]
	if a.Dead {
		t.Fatalf("live OK traffic must protect transport; blockedShare=%.2f okShare=%.2f reason=%q",
			a.BlockedShare, a.OKShare, a.Reason)
	}
}
