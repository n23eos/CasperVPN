// Package probe holds the MVP prober behind ports.Prober. Real in-country
// probes (RU vantage points) are operator infrastructure; this package ships
// a minimal TCP reachability prober so the loop is complete end-to-end and
// the seam is proven. Swap in real network checks without touching the loop.
package probe

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/caspervpn/contracts"
)

// TCPProber dials the node's advertised entry endpoint. It can only prove
// reachability from where the orchestrator runs — it is NOT a censorship
// vantage point. Its verdicts are still authoritative for "node is down".
type TCPProber struct {
	// Source labels emitted HealthEvents (PROBE_SOURCE).
	Source string
	// Timeout bounds one dial.
	Timeout time.Duration
	// Now abstracts time for tests; nil means time.Now.
	Now func() time.Time
	// Dial abstracts the network for tests; nil means net.Dialer.DialContext.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Probe checks node and renders a HealthEvent (never an error for a mere
// failed dial — unreachability IS the verdict).
func (p *TCPProber) Probe(ctx context.Context, node contracts.Node) (contracts.HealthEvent, error) {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	ev := contracts.HealthEvent{
		NodeID:      node.ID,
		ProbeSource: p.Source,
		ObservedAt:  now().UTC(),
	}
	port := probePort(node)
	if node.EntryIP == "" || port == 0 {
		ev.Status = contracts.HealthUnreachable
		ev.Detail = "no advertised entry_ip/port to probe"
		return ev, nil
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dial := p.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	addr := net.JoinHostPort(node.EntryIP, strconv.Itoa(port))
	start := now()
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		ev.Status = contracts.HealthUnreachable
		ev.Detail = fmt.Sprintf("tcp dial failed: %v", err)
		return ev, nil
	}
	_ = conn.Close()
	ev.Status = contracts.HealthHealthy
	ev.LatencyMs = int(now().Sub(start) / time.Millisecond)
	return ev, nil
}

// probePort picks the first enabled transport port (lowest priority first
// would require sorting; any enabled listener proves reachability).
func probePort(n contracts.Node) int {
	for _, tr := range n.Transports {
		if tr.Enabled && tr.Port > 0 {
			return tr.Port
		}
	}
	return 0
}
