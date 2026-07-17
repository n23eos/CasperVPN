package channel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caspervpn/delivery/internal/artifact"
)

// futureSkew tolerates modest clock skew before treating an artifact dated in
// the future as suspect (anti-rollback also rejects implausibly future copies).
const futureSkew = 5 * time.Minute

// Resolver fetches a signed artifact by trying channels in priority order until
// one returns bytes that VERIFY. Verification is the gate: a channel that returns
// a corrupt, truncated or spoofed blob is treated exactly like a channel that
// returned nothing — the resolver moves on. This is the failover that makes any
// single channel's failure (or capture) survivable.
//
// A valid signature proves authenticity but NOT freshness: a captured, genuinely
// signed but stale artifact could be replayed to pin a client to an old (perhaps
// since-blocked) directory. When maxAge > 0 the resolver enforces anti-rollback,
// rejecting artifacts whose IssuedAt is older than maxAge (or implausibly future)
// and failing over to a fresher copy on another channel.
type Resolver struct {
	reg    *Registry
	v      artifact.Verifier
	maxAge time.Duration
	now    func() time.Time
}

// ResolverOption tunes a Resolver.
type ResolverOption func(*Resolver)

// WithMaxAge enables anti-rollback: artifacts older than d are rejected. Zero
// (the default) disables the freshness check.
func WithMaxAge(d time.Duration) ResolverOption { return func(r *Resolver) { r.maxAge = d } }

// WithClock overrides the time source (tests).
func WithClock(now func() time.Time) ResolverOption { return func(r *Resolver) { r.now = now } }

// NewResolver builds a resolver over a registry and a signature verifier.
func NewResolver(reg *Registry, v artifact.Verifier, opts ...ResolverOption) *Resolver {
	r := &Resolver{reg: reg, v: v, now: time.Now}
	for _, o := range opts {
		o(r)
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r
}

// Result reports which channel satisfied a Resolve and the recovered artifact.
type Result struct {
	Channel  Kind
	Artifact artifact.Artifact
}

// ErrAllChannelsFailed is returned when no channel produced a verifiable artifact.
var ErrAllChannelsFailed = errors.New("channel: all channels failed to deliver a verifiable artifact")

// Resolve tries each channel for key, verifying every blob before accepting it.
// It returns the first verified artifact and the channel that carried it. Errors
// from individual channels are collected so the last cause is reported if all
// fail, but a single failure never aborts the sweep.
func (r *Resolver) Resolve(ctx context.Context, key string) (Result, error) {
	var lastErr error
	for _, ch := range r.reg.Ordered() {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		blob, err := ch.Fetch(ctx, key)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", ch.Kind(), err)
			continue
		}
		art, err := artifact.Open(blob, r.v) // verify signature before trusting
		if err != nil {
			lastErr = fmt.Errorf("%s: verify: %w", ch.Kind(), err)
			continue
		}
		if err := r.checkFresh(art); err != nil {
			lastErr = fmt.Errorf("%s: %w", ch.Kind(), err) // anti-rollback: try a fresher channel
			continue
		}
		return Result{Channel: ch.Kind(), Artifact: art}, nil
	}
	if lastErr != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrAllChannelsFailed, lastErr)
	}
	return Result{}, ErrAllChannelsFailed
}

// checkFresh enforces anti-rollback when a max age is configured. A valid
// signature already proves the artifact is authentic; this rejects a genuinely
// signed but stale (or implausibly future) copy that a captor could replay.
func (r *Resolver) checkFresh(a artifact.Artifact) error {
	if r.maxAge <= 0 {
		return nil // freshness enforcement disabled
	}
	age := r.now().Sub(a.IssuedAt)
	if age > r.maxAge {
		return fmt.Errorf("stale artifact: issued %s ago, exceeds max age %s", age.Round(time.Second), r.maxAge)
	}
	if age < -futureSkew {
		return fmt.Errorf("artifact dated %s in the future (beyond %s skew)", (-age).Round(time.Second), futureSkew)
	}
	return nil
}

// Publisher writes one blob to EVERY channel (best-effort broadcast). Used to push
// a fresh directory redundantly. It returns per-channel errors but never fails the
// whole broadcast because one channel is down — redundancy is the point.
type Publisher struct{ reg *Registry }

// NewPublisher builds a broadcast publisher over a registry.
func NewPublisher(reg *Registry) *Publisher { return &Publisher{reg: reg} }

// Broadcast publishes blob under key to all channels, returning kind -> error
// (nil on success). At least one nil means the artifact is reachable.
func (p *Publisher) Broadcast(ctx context.Context, key string, blob []byte) map[Kind]error {
	chans := p.reg.Ordered()
	out := make(map[Kind]error, len(chans))
	for _, ch := range chans {
		out[ch.Kind()] = ch.Publish(ctx, key, blob)
	}
	return out
}
