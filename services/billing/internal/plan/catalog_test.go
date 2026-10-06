package plan

import (
	"strings"
	"testing"

	"github.com/caspervpn/contracts"
)

const goodCatalog = `{
  "plans": [
    {"id":"basic","duration":"720h","grace":"72h","traffic_limit_bytes":107374182400,"speed_limit_mbps":50,"device_limit":2,"prices":{"BTC":"0.0001","XMR":"0.05"}},
    {"id":"unlimited","duration":"720h","grace":"72h","traffic_limit_bytes":0,"speed_limit_mbps":0,"device_limit":5,"prices":{"BTC":"0.0003"}}
  ]
}`

func TestLoad_Valid(t *testing.T) {
	c, err := Load(strings.NewReader(goodCatalog))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p, ok := c.Get(contracts.SubscriptionPlanBasic)
	if !ok {
		t.Fatal("basic plan missing")
	}
	if p.Duration.Hours() != 720 {
		t.Fatalf("duration = %v, want 720h", p.Duration)
	}
	price, ok := c.Price(contracts.SubscriptionPlanBasic, "XMR")
	if !ok || price != "0.05" {
		t.Fatalf("XMR price = %q ok=%v, want 0.05", price, ok)
	}
}

func TestLoad_RejectsUnknownPlan(t *testing.T) {
	_, err := Load(strings.NewReader(`{"plans":[{"id":"gold","duration":"1h","grace":"1h","prices":{"BTC":"1"}}]}`))
	if err == nil {
		t.Fatal("expected error for unknown plan id")
	}
}

func TestLoad_RejectsBadDuration(t *testing.T) {
	_, err := Load(strings.NewReader(`{"plans":[{"id":"basic","duration":"nope","grace":"1h","prices":{"BTC":"1"}}]}`))
	if err == nil {
		t.Fatal("expected error for bad duration")
	}
}

func TestLoad_RejectsEmptyPrices(t *testing.T) {
	_, err := Load(strings.NewReader(`{"plans":[{"id":"basic","duration":"1h","grace":"1h","prices":{}}]}`))
	if err == nil {
		t.Fatal("expected error for empty prices")
	}
}

func TestLoad_RejectsEmptyCatalog(t *testing.T) {
	_, err := Load(strings.NewReader(`{"plans":[]}`))
	if err == nil {
		t.Fatal("expected error for empty catalog")
	}
}

func TestCatalog_ListSortedAndPricesAreImmutableCopies(t *testing.T) {
	prices := map[string]string{"BTC": "0.0001"}
	c := NewCatalog(
		Plan{ID: contracts.SubscriptionPlanUnlimited, Prices: map[string]string{"BTC": "0.0003"}},
		Plan{ID: contracts.SubscriptionPlanBasic, Prices: prices},
	)
	prices["BTC"] = "changed"

	plans := c.List()
	if len(plans) != 2 || plans[0].ID != contracts.SubscriptionPlanBasic || plans[1].ID != contracts.SubscriptionPlanUnlimited {
		t.Fatalf("list order = %+v, want basic then unlimited", plans)
	}
	if plans[0].Prices["BTC"] != "0.0001" {
		t.Fatalf("stored price = %q, want immutable input copy", plans[0].Prices["BTC"])
	}
	plans[0].Prices["BTC"] = "mutated"
	got, _ := c.Price(contracts.SubscriptionPlanBasic, "BTC")
	if got != "0.0001" {
		t.Fatalf("catalog price changed through List: %q", got)
	}
	p, _ := c.Get(contracts.SubscriptionPlanBasic)
	p.Prices["BTC"] = "mutated-again"
	got, _ = c.Price(contracts.SubscriptionPlanBasic, "BTC")
	if got != "0.0001" {
		t.Fatalf("catalog price changed through Get: %q", got)
	}
}
