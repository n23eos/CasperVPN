// Package contracts is the single source of truth for cross-service types in
// CasperVPN. It defines the wire/data shapes shared by every service:
// Node, Transport (and its variants), User, Subscription, FieldSignal and
// HealthEvent, plus the status/version enums that bind them together.
//
// FROZEN CONTRACT. After the scaffolding wave, files in this package are frozen:
// the six subsystem teams build against these types in parallel. Additive,
// backward-compatible changes go through a versioned review; breaking changes do
// not happen without a coordinated migration. See docs/contracts.md.
//
// Anti-block invariants baked into these types (see architecture.md):
//   - A Node carries MANY Transports at once — diversity, not monoculture.
//   - A Node supports fast rotation (RotateAfter, EphemeralEntryIP) and
//     entry/exit separation (Role + EntryNodeID).
//   - A User owns a PERSONAL RealityShortID and UUID/key, so blocking one user
//     never burns the others sharing a node.
//   - A FieldSignal carries everything the "where did it get blocked" feedback
//     loop needs, anonymously.
//
// No mimicry domain and no IP is ever hardcoded here — those live in config/DB
// and flow through these fields at runtime.
package contracts
