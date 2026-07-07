package aggregate

import (
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

func TestRecommend_NodeBlockFromField(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Minute)
	p := defaultParams()

	var sigs []contracts.FieldSignal
	for asn := 1; asn <= 6; asn++ {
		sigs = append(sigs, sig("node-x", contracts.TransportVlessReality, "RU-MOW", asn, contracts.SignalDPIBlock, at))
	}
	recs := Recommend(sigs, nil, now, 15*time.Minute, p)

	if len(recs.NodeBlocks) != 1 {
		t.Fatalf("want 1 node block, got %d", len(recs.NodeBlocks))
	}
	nb := recs.NodeBlocks[0]
	if nb.NodeID != "node-x" || nb.Action != ActionMarkNodeBlocked {
		t.Fatalf("unexpected node block: %+v", nb)
	}
	if nb.Confidence != ConfidenceCorroborated {
		t.Fatalf("field-derived block should be corroborated, got %s", nb.Confidence)
	}
	if len(nb.Regions) != 1 || nb.Regions[0] != "RU-MOW" {
		t.Fatalf("want region RU-MOW, got %v", nb.Regions)
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

func TestRecommend_PrioritizeHealthyTransport(t *testing.T) {
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

	var ru *RegionPriority
	for i := range recs.RegionPriorities {
		if recs.RegionPriorities[i].Region == "RU-MOW" {
			ru = &recs.RegionPriorities[i]
		}
	}
	if ru == nil {
		t.Fatal("no RU-MOW region priority")
	}
	if ru.Recommended != contracts.TransportHysteria2 {
		t.Fatalf("should prioritize hysteria2, got %q (ranked %+v)", ru.Recommended, ru.Ranked)
	}
}
