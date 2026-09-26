package aggregate

import (
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

func TestRecommend_ForgedFieldDiversityCannotBlockNode(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Minute)
	p := defaultParams()

	var sigs []contracts.FieldSignal
	for asn := 1; asn <= 6; asn++ {
		sigs = append(sigs, sig("node-x", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalDPIBlock, at))
	}
	recs := Recommend(sigs, nil, now, 15*time.Minute, p)

	if len(recs.NodeBlocks) != 0 || len(recs.RegionPriorities) != 0 {
		t.Fatalf("client-chosen ASNs produced actions: %+v", recs)
	}
}

func TestRecommend_NodeBlockFromHealthIsAuthoritative(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	health := []contracts.HealthEvent{{
		NodeID:             "node-y",
		Status:             contracts.HealthBlocked,
		ProbeSource:        "ru-probe-1",
		BlockedFromRegions: []string{"IR-07"},
		ObservedAt:         now.Add(-30 * time.Second),
	}}
	recs := Recommend(nil, health, now, 15*time.Minute, defaultParams())
	if len(recs.NodeBlocks) != 1 {
		t.Fatalf("want 1 node block from health, got %d", len(recs.NodeBlocks))
	}
	if recs.NodeBlocks[0].Confidence != ConfidenceAuthoritative {
		t.Fatalf("health-derived block must be authoritative, got %s", recs.NodeBlocks[0].Confidence)
	}
}

func TestRecommend_ForgedFieldDiversityCannotChangeTransport(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Minute)
	p := defaultParams()

	var sigs []contracts.FieldSignal
	// vless-reality dead in RU-MOW.
	for asn := 1; asn <= 6; asn++ {
		sigs = append(sigs, sig("n1", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalDPIBlock, at))
	}
	// hysteria2 healthy in RU-MOW.
	for asn := 10; asn <= 16; asn++ {
		sigs = append(sigs, sig("n1", contracts.TransportHysteria2, "RU-MOW", asn, contracts.SignalOK, at))
	}
	recs := Recommend(sigs, nil, now, 15*time.Minute, p)

	if len(recs.RegionPriorities) != 0 {
		t.Fatalf("untrusted field reports changed routing: %+v", recs.RegionPriorities)
	}
}
