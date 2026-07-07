package channel

import (
	"context"
	"errors"
	"fmt"

	"github.com/caspervpn/delivery/internal/artifact"
)

// Resolver fetches a signed artifact by trying channels in priority order until
// one returns bytes that VERIFY. Verification is the gate: a channel that returns
// a corrupt, truncated or spoofed blob is treated exactly like a channel that
// returned nothing — the resolver moves on. This is the failover that makes any
// single channel's failure (or capture) survivable.
type Resolver struct {
	reg *Registry
	v   artifact.Verifier
}

// NewResolver builds a resolver over a registry and a signature verifier.
func NewResolver(reg *Registry, v artifact.Verifier) *Resolver {
	return &Resolver{reg: reg, v: v}
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
		return Result{Channel: ch.Kind(), Artifact: art}, nil
	}
	if lastErr != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrAllChannelsFailed, lastErr)
	}
	return Result{}, ErrAllChannelsFailed
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
