package ingest

import (
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

func baseSignal(now time.Time) contracts.FieldSignal {
	return contracts.FieldSignal{
		SignalID:      "sig-1",
		NodeID:        "node-1",
		TransportType: contracts.TransportVlessReality,
		Region:        "RU-MOW",
		SignalType:    contracts.SignalOK,
		Success:       true,
		ObservedAt:    now.Add(-time.Minute),
	}
}

func TestSanitize_Valid(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 30, 0, time.UTC)
	out, ok, reason := Sanitize(baseSignal(now), now, 72*time.Hour)
	if !ok {
		t.Fatalf("valid signal rejected: %s", reason)
	}
	// Timestamp coarsened to the minute.
	if out.ObservedAt.Second() != 0 || out.ObservedAt.Nanosecond() != 0 {
		t.Fatalf("timestamp not truncated to minute: %v", out.ObservedAt)
	}
}

func TestSanitize_RejectsJunkRegion(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := baseSignal(now)
	s.Region = "Moscow, Tverskaya 7, apt 4" // fine-grained / junk → reject
	if _, ok, reason := Sanitize(s, now, 72*time.Hour); ok {
		t.Fatalf("junk region should be rejected")
	} else if !strings.Contains(reason, "region") {
		t.Fatalf("reason = %q, want region", reason)
	}
}

func TestSanitize_RejectsFutureTimestamp(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := baseSignal(now)
	s.ObservedAt = now.Add(time.Hour)
	if _, ok, _ := Sanitize(s, now, 72*time.Hour); ok {
		t.Fatal("future timestamp should be rejected")
	}
}

func TestSanitize_RejectsStaleTimestamp(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := baseSignal(now)
	s.ObservedAt = now.Add(-100 * time.Hour) // older than retention
	if _, ok, _ := Sanitize(s, now, 72*time.Hour); ok {
		t.Fatal("stale timestamp should be rejected")
	}
}

func TestSanitize_ClampsAndCoarsens(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := baseSignal(now)
	s.RTTms = 999999                                 // out of range → dropped to 0
	s.LossPct = 250                                  // clamp to 100
	s.ASN = -5                                       // invalid → 0
	s.Platform = contracts.Platform("supercomputer") // unknown → dropped
	s.ISP = strings.Repeat("x", 500)                 // truncated
	s.Region = "ru-mow"                              // lowercased input → uppercased

	out, ok, reason := Sanitize(s, now, 72*time.Hour)
	if !ok {
		t.Fatalf("should pass after clamping: %s", reason)
	}
	if out.RTTms != 0 {
		t.Fatalf("rtt not dropped: %d", out.RTTms)
	}
	if out.LossPct != 100 {
		t.Fatalf("loss not clamped: %v", out.LossPct)
	}
	if out.ASN != 0 {
		t.Fatalf("asn not zeroed: %d", out.ASN)
	}
	if out.Platform != "" {
		t.Fatalf("unknown platform not dropped: %q", out.Platform)
	}
	if len(out.ISP) != maxISPLen {
		t.Fatalf("isp not truncated: len=%d", len(out.ISP))
	}
	if out.Region != "RU-MOW" {
		t.Fatalf("region not normalized: %q", out.Region)
	}
}

func TestSanitize_CoarsensClientVersion(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"1.4.2-beta+build.abc": "1.4",
		"1.4.2":                "1.4",
		"1.4":                  "1.4",
		"v2.10.7":              "2.10",
		"3":                    "3",
		"garbage":              "",
		"":                     "",
	}
	for in, want := range cases {
		s := baseSignal(now)
		s.ClientVersion = in
		out, ok, reason := Sanitize(s, now, 72*time.Hour)
		if !ok {
			t.Fatalf("input %q rejected: %s", in, reason)
		}
		if out.ClientVersion != want {
			t.Fatalf("version %q: got %q, want %q", in, out.ClientVersion, want)
		}
	}
}

func TestSanitize_DropsISPWhenASNPresent(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := baseSignal(now)
	s.ASN = 12345
	s.ISP = "Rostelecom"

	out, ok, reason := Sanitize(s, now, 72*time.Hour)
	if !ok {
		t.Fatalf("should pass: %s", reason)
	}
	if out.ISP != "" {
		t.Fatalf("ISP should be dropped when ASN present: %q", out.ISP)
	}
	if out.ASN != 12345 {
		t.Fatalf("ASN should survive: %d", out.ASN)
	}
}

func TestSanitize_KeepsISPWhenNoASN(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := baseSignal(now)
	s.ASN = 0
	s.ISP = "Rostelecom"

	out, ok, reason := Sanitize(s, now, 72*time.Hour)
	if !ok {
		t.Fatalf("should pass: %s", reason)
	}
	if out.ISP != "Rostelecom" {
		t.Fatalf("ISP should survive when ASN absent: %q", out.ISP)
	}
}

func TestSanitize_RejectsMissingIDs(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := baseSignal(now)
	s.SignalID = ""
	if _, ok, _ := Sanitize(s, now, 72*time.Hour); ok {
		t.Fatal("missing signal_id should be rejected")
	}
}
