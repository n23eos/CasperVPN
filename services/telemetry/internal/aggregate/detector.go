package aggregate

import (
	"fmt"

	"github.com/caspervpn/telemetry/internal/config"
)

// applyVerdict decides whether a transport is dead or degraded in a region. This is
// the poisoning firewall: no single source can flip it. All three gates must hold.
//
//  1. Enough INDEPENDENT sources (DistinctSources ≥ MinSources). A flooder is one
//     source no matter how many signals or SignalIDs it sends, so it cannot reach
//     the bar alone.
//  2. A super-majority of those sources report blocked (BlockedShare ≥ threshold)
//     AND at least MinBlockedSources of them do — absolute floor, not just a ratio.
//  3. Live traffic is scarce (OKShare ≤ MaxOKShareForDead). While real users still
//     succeed, fake "blocked" reports cannot kill a working transport — the honest
//     successes counter-weight them.
func applyVerdict(a *Aggregate, p config.VerdictParams) {
	if a.DistinctSources < p.MinSources {
		a.Reason = fmt.Sprintf("insufficient sources: %d < %d (verdict withheld)",
			a.DistinctSources, p.MinSources)
		return
	}

	dead := a.BlockedShare >= p.DeadShareThresh &&
		a.BlockedSources >= p.MinBlockedSources &&
		a.OKShare <= p.MaxOKShareForDead
	if dead {
		a.Dead = true
		a.Reason = fmt.Sprintf(
			"DEAD: %d/%d sources blocked (share %.2f≥%.2f), ok-share %.2f≤%.2f%s",
			a.BlockedSources, a.DistinctSources, a.BlockedShare, p.DeadShareThresh,
			a.OKShare, p.MaxOKShareForDead, spikeNote(a.Spike))
		return
	}

	if a.BlockedShare+a.DegradedShare >= p.DegradedThresh {
		a.Degraded = true
		a.Reason = fmt.Sprintf(
			"DEGRADED: impaired share %.2f≥%.2f across %d sources%s",
			a.BlockedShare+a.DegradedShare, p.DegradedThresh, a.DistinctSources, spikeNote(a.Spike))
		return
	}

	a.Reason = fmt.Sprintf("healthy: blocked-share %.2f across %d sources",
		a.BlockedShare, a.DistinctSources)
}

func spikeNote(spike bool) string {
	if spike {
		return " [SPIKE]"
	}
	return ""
}
