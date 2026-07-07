package aggregate

import (
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/telemetry/internal/config"
)

// defaultParams mirrors the config defaults so verdict tests are explicit and
// independent of the environment.
func defaultParams() config.VerdictParams {
	return config.VerdictParams{
		MinSources:        5,
		MinBlockedSources: 4,
		DeadShareThresh:   0.70,
		MaxOKShareForDead: 0.25,
		DegradedThresh:    0.40,
		SpikeDelta:        0.30,
		SpikeMinSources:   3,
	}
}

// sig builds a signal from a distinct source (asn), used to synthesize field data.
func sig(node string, tt contracts.TransportType, region string, asn int, st contracts.SignalType, at time.Time) contracts.FieldSignal {
	return contracts.FieldSignal{
		SignalID:      randID(node, asn, st, at),
		NodeID:        node,
		TransportType: tt,
		Region:        region,
		ASN:           asn,
		SignalType:    st,
		Success:       st == contracts.SignalOK,
		ObservedAt:    at,
	}
}

func randID(node string, asn int, st contracts.SignalType, at time.Time) string {
	return node + "-" + string(st) + "-" + time.Duration(asn).String() + "-" + at.Format(time.RFC3339Nano)
}

func TestCompute_SharesAndRates(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-1 * time.Minute)
	var sigs []contracts.FieldSignal
	// 4 blocked sources (reset), 1 ok source, 1 throttle source → 6 distinct.
	for asn := 1; asn <= 4; asn++ {
		sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalReset, at))
	}
	sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", 5, contracts.SignalOK, at))
	sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", 6, contracts.SignalThrottle, at))

	aggs := Compute(sigs, now, 15*time.Minute, defaultParams())
	a := aggs[Key{Region: "RU-MOW", Transport: contracts.TransportVlessReality}]

	if a.DistinctSources != 6 {
		t.Fatalf("distinct sources = %d, want 6", a.DistinctSources)
	}
	if a.BlockedSources != 4 || a.DegradedSources != 1 || a.OKSources != 1 {
		t.Fatalf("tally blocked/degraded/ok = %d/%d/%d, want 4/1/1", a.BlockedSources, a.DegradedSources, a.OKSources)
	}
	if a.ResetRate != 4.0/6.0 {
		t.Fatalf("reset rate = %v, want %v", a.ResetRate, 4.0/6.0)
	}
}

func TestCompute_AvgRTTAndLoss(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Minute)
	s1 := sig("n1", contracts.TransportHysteria2, "RU-MOW", 1, contracts.SignalOK, at)
	s1.RTTms, s1.LossPct = 100, 2
	s2 := sig("n1", contracts.TransportHysteria2, "RU-MOW", 2, contracts.SignalOK, at)
	s2.RTTms, s2.LossPct = 300, 8

	aggs := Compute([]contracts.FieldSignal{s1, s2}, now, 15*time.Minute, defaultParams())
	a := aggs[Key{Region: "RU-MOW", Transport: contracts.TransportHysteria2}]
	if a.AvgRTTms != 200 {
		t.Fatalf("avg rtt = %v, want 200", a.AvgRTTms)
	}
	if a.AvgLossPct != 5 {
		t.Fatalf("avg loss = %v, want 5", a.AvgLossPct)
	}
}

func TestCompute_TrendAndSpike(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	older := now.Add(-8 * time.Minute)  // first half
	recent := now.Add(-1 * time.Minute) // second half

	var sigs []contracts.FieldSignal
	// First half: 4 sources healthy.
	for asn := 1; asn <= 4; asn++ {
		sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalOK, older))
	}
	// Second half: same-region blocked by 4 fresh sources.
	for asn := 10; asn <= 13; asn++ {
		sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalDPIBlock, recent))
	}

	aggs := Compute(sigs, now, window, defaultParams())
	a := aggs[Key{Region: "RU-MOW", Transport: contracts.TransportVlessReality}]
	if a.Trend <= 0 {
		t.Fatalf("trend = %v, want positive (worsening)", a.Trend)
	}
	if !a.Spike {
		t.Fatalf("expected spike flagged; trend=%v", a.Trend)
	}
}

func TestCompute_IgnoresOutOfWindow(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour) // outside 15m window
	s := sig("n1", contracts.TransportVlessReality, "RU-MOW", 1, contracts.SignalReset, old)
	aggs := Compute([]contracts.FieldSignal{s}, now, 15*time.Minute, defaultParams())
	if len(aggs) != 0 {
		t.Fatalf("expected no aggregates for out-of-window signal, got %d", len(aggs))
	}
}
