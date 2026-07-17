package probe

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

func nodeWith(ip string, port int, enabled bool) contracts.Node {
	return contracts.Node{
		ID: "node-1", Role: contracts.NodeRoleEntry, Status: contracts.NodeStatusActive,
		EntryIP: ip,
		Transports: []contracts.Transport{{
			Tag: "vr0", Type: contracts.TransportVlessReality, Port: port, Enabled: enabled,
		}},
	}
}

func TestProbeHealthy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)

	p := &TCPProber{Source: "test-probe", Timeout: 2 * time.Second}
	ev, err := p.Probe(context.Background(), nodeWith(addr.IP.String(), addr.Port, true))
	if err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	if ev.Status != contracts.HealthHealthy {
		t.Fatalf("status = %s, want healthy (%s)", ev.Status, ev.Detail)
	}
	if ev.ProbeSource != "test-probe" || ev.NodeID != "node-1" {
		t.Errorf("event attribution wrong: %+v", ev)
	}
	if err := ev.Validate(); err != nil {
		t.Errorf("emitted event must satisfy the contract: %v", err)
	}
}

func TestProbeUnreachable(t *testing.T) {
	p := &TCPProber{
		Source: "test-probe", Timeout: time.Second,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("connection refused")
		},
	}
	ev, err := p.Probe(context.Background(), nodeWith("192.0.2.1", 443, true))
	if err != nil {
		t.Fatalf("failed dial must be a verdict, not an error: %v", err)
	}
	if ev.Status != contracts.HealthUnreachable {
		t.Fatalf("status = %s, want unreachable", ev.Status)
	}
}

func TestProbeNoEndpoint(t *testing.T) {
	cases := []contracts.Node{
		nodeWith("", 443, true),           // no IP
		nodeWith("192.0.2.1", 443, false), // no enabled transport
	}
	p := &TCPProber{Source: "test-probe", Timeout: time.Second}
	for _, n := range cases {
		ev, err := p.Probe(context.Background(), n)
		if err != nil {
			t.Fatalf("Probe() error: %v", err)
		}
		if ev.Status != contracts.HealthUnreachable {
			t.Fatalf("status = %s, want unreachable for unprobeable node", ev.Status)
		}
	}
}
