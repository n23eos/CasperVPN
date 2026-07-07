// Package steg hides the signed+sealed pointer inside an innocuous-looking API
// payload — here, a analytics/CDP "event batch" JSON of the shape libraries like
// Segment/analytics.js emit. The covert bytes ride in fields that legitimately
// carry opaque tokens (message ids), so a passive observer sees a normal
// telemetry POST, not a config fetch.
//
// THREAT MODEL / LIMITS (full version in docs/delivery.md):
//   - Steganography here is COVERTNESS, not confidentiality: the payload is
//     already sealed+signed by the artifact layer. Steg only hides that a
//     transfer is happening.
//   - It is LOW BANDWIDTH and fragile: a carrier that re-serializes, strips
//     unknown fields, or normalizes the JSON destroys the payload. Treat this as
//     a last-resort channel, not a primary.
//   - It resists casual inspection, NOT statistical/format analysis by a
//     determined adversary who has the cover template. Rotate the template.
package steg

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// chunkLen bounds each covert message id so it stays in the size range real
// analytics message ids occupy (looks unremarkable).
const chunkLen = 40

// coverLibrary is the plausible client library block a real payload carries.
type coverLibrary struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type coverContext struct {
	Library coverLibrary `json:"library"`
}

type coverEvent struct {
	Type      string `json:"type"`
	Event     string `json:"event"`
	MessageID string `json:"messageId"` // covert carrier field
}

// cover is the innocuous document. The covert payload is spread across the
// batch[].messageId fields, concatenated in order on decode.
type cover struct {
	Schema      string       `json:"schema"`
	AnonymousID string       `json:"anonymousId"`
	Context     coverContext `json:"context"`
	Batch       []coverEvent `json:"batch"`
}

// eventNames cycles plausible event labels so the batch looks organic.
var eventNames = []string{"page_view", "screen", "identify", "click", "scroll", "heartbeat"}

// Embed encodes blob into a cover JSON document. anonID is an opaque, non-covert
// marker the caller supplies (e.g. a random token); it is NOT needed to decode.
func Embed(blob []byte, anonID string) ([]byte, error) {
	enc := base64.RawURLEncoding.EncodeToString(blob)
	var batch []coverEvent
	for i := 0; len(enc) > 0; i++ {
		n := chunkLen
		if n > len(enc) {
			n = len(enc)
		}
		batch = append(batch, coverEvent{
			Type:      "track",
			Event:     eventNames[i%len(eventNames)],
			MessageID: enc[:n],
		})
		enc = enc[n:]
	}
	c := cover{
		Schema:      "cdp.event.v1",
		AnonymousID: anonID,
		Context:     coverContext{Library: coverLibrary{Name: "analytics.js", Version: "4.1.0"}},
		Batch:       batch,
	}
	return json.Marshal(c)
}

// Extract reverses Embed: read batch[].messageId in order, concatenate, decode.
func Extract(coverJSON []byte) ([]byte, error) {
	var c cover
	if err := json.Unmarshal(coverJSON, &c); err != nil {
		return nil, fmt.Errorf("steg: not a cover document: %w", err)
	}
	if len(c.Batch) == 0 {
		return nil, errors.New("steg: cover carries no batch events")
	}
	enc := make([]byte, 0, len(c.Batch)*chunkLen)
	for _, e := range c.Batch {
		enc = append(enc, e.MessageID...)
	}
	blob, err := base64.RawURLEncoding.DecodeString(string(enc))
	if err != nil {
		return nil, fmt.Errorf("steg: bad covert payload: %w", err)
	}
	return blob, nil
}
