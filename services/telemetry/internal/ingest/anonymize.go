// Package ingest accepts anonymous FieldSignals (and authoritative HealthEvents),
// enforces anonymity and anti-poisoning at the boundary, and hands clean points to
// the store. Nothing here reads or stores a client IP or any user identity.
package ingest

import (
	"regexp"
	"strings"
	"time"

	"github.com/caspervpn/contracts"
)

// Anonymization limits — coarse by design so telemetry can never re-identify who
// went where (acceptance criterion 1).
const (
	maxISPLen           = 64
	maxClientVersionLen = 32
	maxRegionLen        = 8
	maxRTTms            = 60000 // 60s ceiling; larger is noise/poison
	maxLossPct          = 100.0
	maxASN              = 4294967295 // 32-bit AS number space

	// Timestamp acceptance window. Signals timestamped far in the future or past are
	// rejected — a poison vector for skewing rolling windows. observed_at is then
	// truncated to the minute to blunt timing correlation.
	futureSkew  = 2 * time.Minute
	clockCoarse = time.Minute
)

// regionRe matches a coarse geo code: country, optionally a subdivision.
// e.g. "RU", "RU-MOW", "IR-07". No finer granularity is accepted.
var regionRe = regexp.MustCompile(`^[A-Z]{2}(-[A-Z0-9]{1,4})?$`)

// ctrlChars strips control/formatting runes that could smuggle data or break logs.
var ctrlChars = regexp.MustCompile(`[[:cntrl:]]`)

// Sanitize enforces the anonymity + structural invariants on one signal, returning
// a cleaned copy. It never mutates the input (immutability at the boundary). The
// bool is false when the signal must be rejected; the string says why.
//
// maxAge bounds how far in the past observed_at may be (retention); now is the
// server clock. Only the caller knows those, so they are passed in (testability).
func Sanitize(in contracts.FieldSignal, now time.Time, maxAge time.Duration) (contracts.FieldSignal, bool, string) {
	s := in // value copy

	// Coarsen/validate geo. Reject junk regions rather than store them — junk is a
	// cheap way to forge many distinct sources (poisoning).
	s.Region = strings.ToUpper(strings.TrimSpace(s.Region))
	if len(s.Region) > maxRegionLen || !regionRe.MatchString(s.Region) {
		return contracts.FieldSignal{}, false, "invalid region"
	}

	// Clamp coarse network fields.
	if s.ASN < 0 || s.ASN > maxASN {
		s.ASN = 0
	}
	s.ISP = clip(ctrlChars.ReplaceAllString(s.ISP, ""), maxISPLen)
	s.ClientVersion = clip(ctrlChars.ReplaceAllString(s.ClientVersion, ""), maxClientVersionLen)

	// Clamp metrics into sane ranges (out-of-range = drop/clamp, a poison guard).
	if s.RTTms < 0 || s.RTTms > maxRTTms {
		s.RTTms = 0
	}
	if s.LossPct < 0 {
		s.LossPct = 0
	}
	if s.LossPct > maxLossPct {
		s.LossPct = maxLossPct
	}

	// Platform: keep only known values; drop anything else.
	if s.Platform != "" && !s.Platform.Valid() {
		s.Platform = ""
	}

	// Timestamp: reject far-future / far-past; coarsen to the minute (UTC).
	if s.ObservedAt.IsZero() {
		s.ObservedAt = now
	}
	if s.ObservedAt.After(now.Add(futureSkew)) {
		return contracts.FieldSignal{}, false, "timestamp in the future"
	}
	if s.ObservedAt.Before(now.Add(-maxAge)) {
		return contracts.FieldSignal{}, false, "timestamp older than retention"
	}
	s.ObservedAt = s.ObservedAt.UTC().Truncate(clockCoarse)

	// Structural contract validation last, on the cleaned copy.
	if err := s.Validate(); err != nil {
		return contracts.FieldSignal{}, false, err.Error()
	}
	return s, true, ""
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}
