package contracts

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
)

// RealityUser is one user's REALITY admission credentials for a node's allow-list:
// the VLESS UUID the node authenticates and the personal short-id the node must
// carry in its short-id pool. It is NOT secret in the private-key sense — the same
// values are already handed to the client in its subscription config — but it is
// the exact material a node needs to admit that user.
type RealityUser struct {
	UUID    string `json:"uuid"`
	ShortID string `json:"short_id"`
}

// NodeRealityUsers is the REALITY allow-list a node must admit: every eligible
// user's (uuid, short_id) plus a Revision that changes if and only if the set
// changes. A reconciler snapshots Revision before it converges a node and gates
// activation on the same Revision, so a ban/rotation racing the converge cannot
// activate a node with stale credentials.
type NodeRealityUsers struct {
	Revision string        `json:"revision"`
	Users    []RealityUser `json:"users"`
}

// NodeActivationEvidence is a reconciler-supplied attestation carried on an
// activation. The control-plane does not itself probe a node's data plane; it
// requires the reconciler to assert, after its own authenticated check, that a
// gate is satisfied. Distinguishes a verified node from mere structural presence.
type NodeActivationEvidence string

const (
	// ExitDataPlaneVerified attests that the reconciler ran an authenticated
	// entry->exit probe and the exit's egress (SS2022 inbound) actually works.
	// REQUIRED to activate an exit — its readiness is not structurally derivable.
	ExitDataPlaneVerified NodeActivationEvidence = "exit_data_plane_verified"
)

// Valid reports whether e is a known evidence value.
func (e NodeActivationEvidence) Valid() bool { return e == ExitDataPlaneVerified }

// NodeActivation is the body of POST /v1/nodes/{id}/activate. ExpectedRevision is
// the allow-list revision the reconciler synced; the control-plane refuses to
// activate if the current eligibility revision differs (a ban/rotation raced the
// converge), so a node never goes active with a stale allow-list. Evidence is
// required for exit nodes (see ExitDataPlaneVerified) and ignored for entries.
type NodeActivation struct {
	ExpectedRevision string                 `json:"expected_revision"`
	Evidence         NodeActivationEvidence `json:"evidence,omitempty"`
}

// DistinctEnabledTransportTypes counts the DISTINCT types among enabled
// transports — the anti-block diversity measure. Two enabled transports of the
// same type count as one: diversity is about kinds of transport, not records.
func DistinctEnabledTransportTypes(ts []Transport) int {
	seen := map[TransportType]struct{}{}
	for _, t := range ts {
		if t.Enabled {
			seen[t.Type] = struct{}{}
		}
	}
	return len(seen)
}

// RealityUsersRevision computes a stable SHA-256 digest over the user set. It is a
// pure function of the set: equal sets (in any order) yield equal revisions, and
// any add / remove / short-id rotation yields a different one. Fields are
// length-prefixed so no field-boundary rearrangement can collide. Both the
// control-plane (which emits it) and the reconciler (which echoes it back to
// /activate) call this, so they always agree on what "unchanged" means.
func RealityUsersRevision(users []RealityUser) string {
	sorted := make([]RealityUser, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].UUID != sorted[j].UUID {
			return sorted[i].UUID < sorted[j].UUID
		}
		return sorted[i].ShortID < sorted[j].ShortID
	})
	h := sha256.New()
	var lenbuf [8]byte
	write := func(s string) {
		binary.BigEndian.PutUint64(lenbuf[:], uint64(len(s)))
		h.Write(lenbuf[:])
		h.Write([]byte(s))
	}
	for _, u := range sorted {
		write(u.UUID)
		write(u.ShortID)
	}
	return hex.EncodeToString(h.Sum(nil))
}
