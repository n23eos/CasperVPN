package onchain

import (
	"context"
	"testing"

	"github.com/caspervpn/billing/internal/model"
)

// fakeChain reports a fixed balance/confirmations per address.
type fakeChain struct {
	received map[string]string
	confs    map[string]int
}

func (f *fakeChain) AddressStatus(_ context.Context, _, addr string) (string, int, error) {
	return f.received[addr], f.confs[addr], nil
}

type fakePool struct{ addr string }

func (p *fakePool) Next(string) (string, error) { return p.addr, nil }

func TestCreateInvoice_AllocatesAddress(t *testing.T) {
	g := New([]string{"BTC"}, 2, &fakeChain{}, &fakePool{addr: "bc1qaddr"})
	inv, err := g.CreateInvoice(context.Background(), model.CreateInvoiceRequest{
		AnonUserID: "acct-1", Plan: "basic", Currency: "BTC", Amount: "0.0001",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inv.PayAddress != "bc1qaddr" {
		t.Fatalf("pay address = %q, want bc1qaddr", inv.PayAddress)
	}
	if inv.Status != model.StatusPending {
		t.Fatalf("status = %q, want pending", inv.Status)
	}
}

func TestPoll_SettlesWhenConfirmedAndPaid(t *testing.T) {
	chain := &fakeChain{
		received: map[string]string{"bc1qaddr": "0.0001"},
		confs:    map[string]int{"bc1qaddr": 3},
	}
	g := New([]string{"BTC"}, 2, chain, &fakePool{addr: "bc1qaddr"})
	open := []model.Invoice{{
		ID: "inv-1", Provider: "onchain", Currency: "BTC", Amount: "0.0001", PayAddress: "bc1qaddr",
	}}

	events, err := g.Poll(context.Background(), open)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 1 || events[0].Status != model.StatusSettled {
		t.Fatalf("expected one settled event, got %+v", events)
	}
	if events[0].ExternalID != "onchain:inv-1" {
		t.Fatalf("external id = %q, want stable onchain:inv-1", events[0].ExternalID)
	}
}

func TestPoll_SkipsWhenUnderConfirmed(t *testing.T) {
	chain := &fakeChain{
		received: map[string]string{"bc1qaddr": "0.0001"},
		confs:    map[string]int{"bc1qaddr": 1}, // below minConf=2
	}
	g := New([]string{"BTC"}, 2, chain, &fakePool{addr: "bc1qaddr"})
	open := []model.Invoice{{ID: "inv-1", Provider: "onchain", Currency: "BTC", Amount: "0.0001", PayAddress: "bc1qaddr"}}

	events, _ := g.Poll(context.Background(), open)
	if len(events) != 0 {
		t.Fatalf("expected no events under min confirmations, got %d", len(events))
	}
}

func TestPoll_SkipsUnderpaid(t *testing.T) {
	chain := &fakeChain{
		received: map[string]string{"bc1qaddr": "0.00005"}, // half
		confs:    map[string]int{"bc1qaddr": 5},
	}
	g := New([]string{"BTC"}, 2, chain, &fakePool{addr: "bc1qaddr"})
	open := []model.Invoice{{ID: "inv-1", Provider: "onchain", Currency: "BTC", Amount: "0.0001", PayAddress: "bc1qaddr"}}

	events, _ := g.Poll(context.Background(), open)
	if len(events) != 0 {
		t.Fatalf("expected no events for underpayment, got %d", len(events))
	}
}
