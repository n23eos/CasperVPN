// Package plan is the billing-owned pricing catalog: plan -> duration, grace,
// per-currency prices and entitlement limits. Prices and durations come from
// config (env/JSON), never hardcoded, per the anti-block "zero hardcode" rule.
package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/caspervpn/contracts"
)

// Plan is a single pricing tier.
type Plan struct {
	ID                contracts.SubscriptionPlan
	Duration          time.Duration
	Grace             time.Duration
	Prices            map[string]string // currency -> decimal amount
	TrafficLimitBytes uint64
	SpeedLimitMbps    int
	DeviceLimit       int
}

// Catalog is an immutable set of plans keyed by id.
type Catalog struct {
	plans map[contracts.SubscriptionPlan]Plan
}

// Get returns the plan for id.
func (c *Catalog) Get(id contracts.SubscriptionPlan) (Plan, bool) {
	p, ok := c.plans[id]
	return p, ok
}

// Price returns the decimal amount for (plan, currency).
func (c *Catalog) Price(id contracts.SubscriptionPlan, currency string) (string, bool) {
	p, ok := c.plans[id]
	if !ok {
		return "", false
	}
	amount, ok := p.Prices[currency]
	return amount, ok
}

// wire is the on-disk JSON shape. Durations are Go duration strings ("720h").
type wire struct {
	Plans []struct {
		ID                string            `json:"id"`
		Duration          string            `json:"duration"`
		Grace             string            `json:"grace"`
		Prices            map[string]string `json:"prices"`
		TrafficLimitBytes uint64            `json:"traffic_limit_bytes"`
		SpeedLimitMbps    int               `json:"speed_limit_mbps"`
		DeviceLimit       int               `json:"device_limit"`
	} `json:"plans"`
}

// Load reads and validates a catalog from JSON. It fails fast on unknown plans,
// bad durations or empty price maps so misconfiguration cannot reach production.
func Load(r io.Reader) (*Catalog, error) {
	var w wire
	if err := json.NewDecoder(r).Decode(&w); err != nil {
		return nil, fmt.Errorf("plan: decode catalog: %w", err)
	}
	if len(w.Plans) == 0 {
		return nil, fmt.Errorf("plan: catalog has no plans")
	}
	plans := make(map[contracts.SubscriptionPlan]Plan, len(w.Plans))
	for _, p := range w.Plans {
		id := contracts.SubscriptionPlan(p.ID)
		if !id.Valid() {
			return nil, fmt.Errorf("plan: unknown plan id %q", p.ID)
		}
		dur, err := time.ParseDuration(p.Duration)
		if err != nil || dur <= 0 {
			return nil, fmt.Errorf("plan %q: invalid duration %q", p.ID, p.Duration)
		}
		grace, err := time.ParseDuration(p.Grace)
		if err != nil || grace < 0 {
			return nil, fmt.Errorf("plan %q: invalid grace %q", p.ID, p.Grace)
		}
		if len(p.Prices) == 0 {
			return nil, fmt.Errorf("plan %q: no prices configured", p.ID)
		}
		plans[id] = Plan{
			ID:                id,
			Duration:          dur,
			Grace:             grace,
			Prices:            p.Prices,
			TrafficLimitBytes: p.TrafficLimitBytes,
			SpeedLimitMbps:    p.SpeedLimitMbps,
			DeviceLimit:       p.DeviceLimit,
		}
	}
	return &Catalog{plans: plans}, nil
}

// NewCatalog builds a catalog directly (used by tests).
func NewCatalog(plans ...Plan) *Catalog {
	m := make(map[contracts.SubscriptionPlan]Plan, len(plans))
	for _, p := range plans {
		m[p.ID] = p
	}
	return &Catalog{plans: m}
}
