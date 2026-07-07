package aggregate

import (
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/telemetry/internal/config"
)

// Key identifies an aggregation bucket: one transport family in one coarse region.
type Key struct {
	Region    string
	Transport contracts.TransportType
}

// Aggregate is the rolling-window rollup for one Key. Shares are computed over
// DISTINCT sources (poisoning-resistant), rates over raw signals (for dashboards).
type Aggregate struct {
	Key
	WindowSeconds int

	Total    int
	BySignal map[contracts.SignalType]int

	DistinctSources int
	BlockedSources  int
	DegradedSources int
	OKSources       int

	BlockedShare  float64 // blocked sources / distinct sources
	DegradedShare float64
	OKShare       float64

	ResetRate float64 // reset signals / total (raw)
	ProbeRate float64 // active-probe signals / total (raw)

	AvgRTTms   float64
	AvgLossPct float64

	Trend float64 // blockedShare(recent half) − blockedShare(older half); >0 = worsening
	Spike bool    // sudden jump in blocked sources this window

	Dead     bool // transport declared dead in region (see detector.go)
	Degraded bool
	Reason   string
}

// srcState tracks the worst outcome a single source reported in a window. Worst
// wins: a source that ever saw a block is a "blocked voice", so intermittent blocks
// are not washed out by that same source's successes.
type srcState struct{ worst outcome }

func (s *srcState) observe(o outcome) {
	if o > s.worst {
		s.worst = o
	}
}

// Compute rolls signals into per-Key aggregates over the trailing window ending at
// now, then applies the dead/degraded verdicts. Pure function — deterministic given
// its inputs, which is what makes the detector and poisoning tests reproducible.
func Compute(signals []contracts.FieldSignal, now time.Time, window time.Duration, p config.VerdictParams) map[Key]Aggregate {
	from := now.Add(-window)
	mid := now.Add(-window / 2)

	type acc struct {
		total      int
		bySignal   map[contracts.SignalType]int
		sources    map[SourceKey]*srcState
		firstHalf  map[SourceKey]*srcState // sources in [from, mid)
		secondHalf map[SourceKey]*srcState // sources in [mid, now]
		rttSum     float64
		rttCount   int
		lossSum    float64
		lossCount  int
		resetCount int
		probeCount int
	}
	groups := map[Key]*acc{}

	for i := range signals {
		s := signals[i]
		if s.ObservedAt.Before(from) || s.ObservedAt.After(now) {
			continue
		}
		k := Key{Region: s.Region, Transport: s.TransportType}
		g := groups[k]
		if g == nil {
			g = &acc{
				bySignal:   map[contracts.SignalType]int{},
				sources:    map[SourceKey]*srcState{},
				firstHalf:  map[SourceKey]*srcState{},
				secondHalf: map[SourceKey]*srcState{},
			}
			groups[k] = g
		}

		g.total++
		g.bySignal[s.SignalType]++
		if s.SignalType == contracts.SignalReset {
			g.resetCount++
		}
		if s.SignalType == contracts.SignalActiveProbe {
			g.probeCount++
		}
		if s.RTTms > 0 {
			g.rttSum += float64(s.RTTms)
			g.rttCount++
		}
		if s.LossPct > 0 {
			g.lossSum += s.LossPct
			g.lossCount++
		}

		o := classify(s)
		sk := sourceKey(s)
		observeInto(g.sources, sk, o)
		if s.ObservedAt.Before(mid) {
			observeInto(g.firstHalf, sk, o)
		} else {
			observeInto(g.secondHalf, sk, o)
		}
	}

	out := make(map[Key]Aggregate, len(groups))
	for k, g := range groups {
		a := Aggregate{
			Key:           k,
			WindowSeconds: int(window / time.Second),
			Total:         g.total,
			BySignal:      g.bySignal,
		}
		a.DistinctSources, a.BlockedSources, a.DegradedSources, a.OKSources = tally(g.sources)
		if a.DistinctSources > 0 {
			d := float64(a.DistinctSources)
			a.BlockedShare = float64(a.BlockedSources) / d
			a.DegradedShare = float64(a.DegradedSources) / d
			a.OKShare = float64(a.OKSources) / d
		}
		if g.total > 0 {
			a.ResetRate = float64(g.resetCount) / float64(g.total)
			a.ProbeRate = float64(g.probeCount) / float64(g.total)
		}
		if g.rttCount > 0 {
			a.AvgRTTms = g.rttSum / float64(g.rttCount)
		}
		if g.lossCount > 0 {
			a.AvgLossPct = g.lossSum / float64(g.lossCount)
		}

		firstShare := blockedShare(g.firstHalf)
		secondShare := blockedShare(g.secondHalf)
		a.Trend = secondShare - firstShare
		_, secondBlocked, _, _ := tally(g.secondHalf)
		a.Spike = a.Trend >= p.SpikeDelta && secondBlocked >= p.SpikeMinSources

		applyVerdict(&a, p)
		out[k] = a
	}
	return out
}

func observeInto(m map[SourceKey]*srcState, sk SourceKey, o outcome) {
	st := m[sk]
	if st == nil {
		st = &srcState{}
		m[sk] = st
	}
	st.observe(o)
}

// tally counts distinct sources by their worst outcome.
func tally(m map[SourceKey]*srcState) (distinct, blocked, degraded, ok int) {
	for _, st := range m {
		distinct++
		switch st.worst {
		case outcomeBlocked:
			blocked++
		case outcomeDegraded:
			degraded++
		case outcomeOK:
			ok++
		}
	}
	return
}

func blockedShare(m map[SourceKey]*srcState) float64 {
	distinct, blocked, _, _ := tally(m)
	if distinct == 0 {
		return 0
	}
	return float64(blocked) / float64(distinct)
}
