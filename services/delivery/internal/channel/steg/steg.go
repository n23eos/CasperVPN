// Package steg hides the signed+sealed pointer inside an innocuous-looking API
// payload — here, an analytics/CDP "event batch" JSON of the shape libraries like
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
//   - It resists casual inspection AND cheap statistical fingerprinting, but NOT
//     a determined adversary who has the exact cover corpus. To raise the cost of
//     a single blocking signature we (a) rotate the cover template per emission,
//     (b) shape the covert carrier as UUID-form message ids (what real analytics
//     batches actually carry, instead of a fixed-length base64 tell), and (c)
//     randomize event labels. None of this is a substitute for the primary
//     channels — it buys time, not guarantees.
package steg

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// chunkBytes is how many covert bytes ride in one message id. A UUID is 16 bytes,
// so each carrier id decodes to exactly one 16-byte chunk and looks like a normal
// opaque analytics id.
const chunkBytes = 16

// lenHeader is a 4-byte big-endian length prefix on the covert blob, so trailing
// random padding in the final chunk is discarded on decode.
const lenHeader = 4

// coverTemplate is one plausible analytics client identity. Real traffic carries
// many of these; rotating per emission means no single (schema, library, version)
// tuple is a stable signature to block on.
type coverTemplate struct {
	Schema  string
	Library string
	Version string
}

// coverTemplates is the rotation pool of plausible analytics client identities.
// Extend/replace via cover-corpus updates; decode does not depend on which one
// was used, so the pool can grow freely.
var coverTemplates = []coverTemplate{
	{Schema: "cdp.event.v1", Library: "analytics.js", Version: "4.1.0"},
	{Schema: "cdp.event.v1", Library: "analytics.js", Version: "5.2.0"},
	{Schema: "track.batch.v2", Library: "rudder-analytics.js", Version: "3.0.1"},
	{Schema: "events.v1", Library: "@segment/analytics-next", Version: "1.70.0"},
	{Schema: "collect.v1", Library: "analytics-node", Version: "6.2.0"},
}

// eventNames are plausible event labels; one is chosen at random per event so the
// batch carries no fixed round-robin ordering pattern. Labels are NOT covert (the
// payload rides in message ids), so randomizing them is free.
var eventNames = []string{"page_view", "screen", "identify", "click", "scroll", "heartbeat", "form_submit", "add_to_cart"}

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
	MessageID string `json:"messageId"` // covert carrier field (UUID-form)
}

// cover is the innocuous document. The covert payload is spread across the
// batch[].messageId fields, concatenated in array order on decode.
type cover struct {
	Schema      string       `json:"schema"`
	AnonymousID string       `json:"anonymousId"`
	Context     coverContext `json:"context"`
	Batch       []coverEvent `json:"batch"`
}

// Embed encodes blob into a cover JSON document. anonID is an opaque, non-covert
// marker the caller supplies (e.g. a random token); it is NOT needed to decode.
func Embed(blob []byte, anonID string) ([]byte, error) {
	// Frame: 4-byte length prefix + blob, then pad to a chunkBytes multiple with
	// cryptographically-random bytes (so the tail id is indistinguishable from a
	// real random id and carries no zero-fill tell).
	framed := make([]byte, lenHeader+len(blob))
	binary.BigEndian.PutUint32(framed[:lenHeader], uint32(len(blob)))
	copy(framed[lenHeader:], blob)
	if pad := (chunkBytes - len(framed)%chunkBytes) % chunkBytes; pad > 0 {
		tail := make([]byte, pad)
		if _, err := rand.Read(tail); err != nil {
			return nil, fmt.Errorf("steg: pad: %w", err)
		}
		framed = append(framed, tail...)
	}

	batch := make([]coverEvent, 0, len(framed)/chunkBytes)
	for i := 0; i < len(framed); i += chunkBytes {
		ev, err := randomEventName()
		if err != nil {
			return nil, err
		}
		batch = append(batch, coverEvent{
			Type:      "track",
			Event:     ev,
			MessageID: uuidForm(framed[i : i+chunkBytes]),
		})
	}

	tmpl, err := randomTemplate()
	if err != nil {
		return nil, err
	}
	c := cover{
		Schema:      tmpl.Schema,
		AnonymousID: anonID,
		Context:     coverContext{Library: coverLibrary{Name: tmpl.Library, Version: tmpl.Version}},
		Batch:       batch,
	}
	return json.Marshal(c)
}

// Extract reverses Embed: read batch[].messageId in order, decode each UUID-form
// id back to 16 bytes, concatenate, then strip the length prefix + padding.
func Extract(coverJSON []byte) ([]byte, error) {
	var c cover
	if err := json.Unmarshal(coverJSON, &c); err != nil {
		return nil, fmt.Errorf("steg: not a cover document: %w", err)
	}
	if len(c.Batch) == 0 {
		return nil, errors.New("steg: cover carries no batch events")
	}
	framed := make([]byte, 0, len(c.Batch)*chunkBytes)
	for _, e := range c.Batch {
		b, err := bytesFromUUIDForm(e.MessageID)
		if err != nil {
			return nil, fmt.Errorf("steg: bad covert payload: %w", err)
		}
		framed = append(framed, b...)
	}
	if len(framed) < lenHeader {
		return nil, errors.New("steg: covert frame too short")
	}
	n := binary.BigEndian.Uint32(framed[:lenHeader])
	if int(n) > len(framed)-lenHeader {
		return nil, errors.New("steg: covert length exceeds frame")
	}
	return framed[lenHeader : lenHeader+int(n)], nil
}

// uuidForm renders 16 bytes as canonical 8-4-4-4-12 hex (a UUID-shaped opaque id).
// Bytes are carried verbatim (no version/variant bits are forced), so the value is
// fully recoverable while still matching the shape real analytics ids occupy.
func uuidForm(b []byte) string {
	h := hex.EncodeToString(b) // 32 hex chars
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// bytesFromUUIDForm reverses uuidForm: strip hyphens, hex-decode to 16 bytes.
func bytesFromUUIDForm(s string) ([]byte, error) {
	clean := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			clean = append(clean, s[i])
		}
	}
	b, err := hex.DecodeString(string(clean))
	if err != nil {
		return nil, err
	}
	if len(b) != chunkBytes {
		return nil, fmt.Errorf("steg: carrier id is %d bytes, want %d", len(b), chunkBytes)
	}
	return b, nil
}

// randomTemplate picks a cover template uniformly via crypto/rand.
func randomTemplate() (coverTemplate, error) {
	i, err := randIndex(len(coverTemplates))
	if err != nil {
		return coverTemplate{}, err
	}
	return coverTemplates[i], nil
}

// randomEventName picks a plausible event label uniformly via crypto/rand.
func randomEventName() (string, error) {
	i, err := randIndex(len(eventNames))
	if err != nil {
		return "", err
	}
	return eventNames[i], nil
}

// randIndex returns a uniform-enough index in [0,n) using crypto/rand. n is a
// small fixed pool size, so modulo bias is negligible.
func randIndex(n int) (int, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("steg: rand index: %w", err)
	}
	return int(binary.BigEndian.Uint16(b[:])) % n, nil
}
