// Package metrics holds process counters and renders a Prometheus/OpenMetrics
// exposition (hand-rolled, no client library — the monorepo is dependency-free).
// It exposes both cumulative ingest counters and live per-(region,transport)
// verdict gauges derived from the aggregator.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/caspervpn/telemetry/internal/aggregate"
)

// Collector accumulates cumulative counters. Safe for concurrent use.
type Collector struct {
	mu          sync.Mutex
	ingested    map[string]float64 // key: transport|region|signal_type
	rejected    map[string]float64 // key: reason
	rateLimited float64
	healthTotal float64
}

// NewCollector builds an empty collector.
func NewCollector() *Collector {
	return &Collector{ingested: map[string]float64{}, rejected: map[string]float64{}}
}

// IncIngested records one accepted signal.
func (c *Collector) IncIngested(transport, region, signalType string) {
	c.mu.Lock()
	c.ingested[transport+"|"+region+"|"+signalType]++
	c.mu.Unlock()
}

// IncRejected records one rejected signal with a coarse reason.
func (c *Collector) IncRejected(reason string) {
	c.mu.Lock()
	c.rejected[reason]++
	c.mu.Unlock()
}

// IncRateLimited records one rate-limited batch.
func (c *Collector) IncRateLimited() {
	c.mu.Lock()
	c.rateLimited++
	c.mu.Unlock()
}

// AddHealth records accepted health events.
func (c *Collector) AddHealth(n int) {
	c.mu.Lock()
	c.healthTotal += float64(n)
	c.mu.Unlock()
}

// Render produces the full OpenMetrics text: cumulative counters plus live gauges
// computed from the supplied aggregates.
func (c *Collector) Render(aggs map[aggregate.Key]aggregate.Aggregate) string {
	var b strings.Builder

	c.mu.Lock()
	writeCounter(&b, "telemetry_signals_ingested_total",
		"Accepted anonymous field signals.", c.ingested, func(k string) string {
			p := strings.SplitN(k, "|", 3)
			return labels("transport", p[0], "region", p[1], "signal_type", p[2])
		})
	writeCounter(&b, "telemetry_signals_rejected_total",
		"Rejected field signals by reason.", c.rejected, func(k string) string {
			return labels("reason", k)
		})
	fmt.Fprintf(&b, "# TYPE telemetry_batches_rate_limited_total counter\n")
	fmt.Fprintf(&b, "telemetry_batches_rate_limited_total %g\n", c.rateLimited)
	fmt.Fprintf(&b, "# TYPE telemetry_health_events_total counter\n")
	fmt.Fprintf(&b, "telemetry_health_events_total %g\n", c.healthTotal)
	c.mu.Unlock()

	writeGauges(&b, aggs)
	return b.String()
}

func writeGauges(b *strings.Builder, aggs map[aggregate.Key]aggregate.Aggregate) {
	keys := make([]aggregate.Key, 0, len(aggs))
	for k := range aggs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Region != keys[j].Region {
			return keys[i].Region < keys[j].Region
		}
		return keys[i].Transport < keys[j].Transport
	})

	type gauge struct {
		name, help string
		val        func(a aggregate.Aggregate) float64
	}
	gauges := []gauge{
		{"telemetry_blocked_share", "Blocked-source share per region/transport.", func(a aggregate.Aggregate) float64 { return a.BlockedShare }},
		{"telemetry_degraded_share", "Degraded-source share.", func(a aggregate.Aggregate) float64 { return a.DegradedShare }},
		{"telemetry_ok_share", "Healthy-source share.", func(a aggregate.Aggregate) float64 { return a.OKShare }},
		{"telemetry_distinct_sources", "Distinct coarse sources observed.", func(a aggregate.Aggregate) float64 { return float64(a.DistinctSources) }},
		{"telemetry_avg_rtt_ms", "Mean RTT in ms.", func(a aggregate.Aggregate) float64 { return a.AvgRTTms }},
		{"telemetry_avg_loss_pct", "Mean packet loss percent.", func(a aggregate.Aggregate) float64 { return a.AvgLossPct }},
		{"telemetry_transport_dead", "1 when transport declared dead in region.", func(a aggregate.Aggregate) float64 { return b2f(a.Dead) }},
		{"telemetry_transport_degraded", "1 when transport degraded in region.", func(a aggregate.Aggregate) float64 { return b2f(a.Degraded) }},
	}

	for _, g := range gauges {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", g.name, g.help, g.name)
		for _, k := range keys {
			a := aggs[k]
			fmt.Fprintf(b, "%s{%s} %g\n", g.name,
				labels("region", k.Region, "transport", string(k.Transport)), g.val(a))
		}
	}
}

func writeCounter(b *strings.Builder, name, help string, vals map[string]float64, lbl func(string) string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s{%s} %g\n", name, lbl(k), vals[k])
	}
}

// labels builds a Prometheus label set from name,value pairs, escaping values.
func labels(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, kv[i], escapeLabel(kv[i+1])))
	}
	return strings.Join(parts, ",")
}

func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

func b2f(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
