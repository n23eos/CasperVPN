// Package idgen produces opaque random identifiers for invoices and events.
package idgen

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a 128-bit random hex id. crypto/rand failure is treated as fatal
// for callers only insofar as it returns an empty string; callers generate ids
// in non-adversarial paths (invoice creation) where a read failure is vanishing.
func New() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
