package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/subscription/internal/controlplane"
)

// devFixtures is the shape of the optional SUBSCRIPTION_FIXTURES file used to
// seed the in-memory store for local runs (never used in production, where the
// control-plane is authoritative).
type devFixtures struct {
	Users         []contracts.User         `json:"users"`
	Subscriptions []contracts.Subscription `json:"subscriptions"`
	Nodes         []contracts.Node         `json:"nodes"`
	Tokens        []struct {
		Token          string `json:"token"`
		UserID         string `json:"user_id"`
		SubscriptionID string `json:"subscription_id"`
	} `json:"tokens"`
}

// loadFixtures seeds a Memory store from a JSON fixtures file.
func loadFixtures(m *controlplane.Memory, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var fx devFixtures
	if err := json.Unmarshal(raw, &fx); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for _, u := range fx.Users {
		m.PutUser(u)
	}
	for _, s := range fx.Subscriptions {
		m.PutSubscription(s)
	}
	m.SetNodes(fx.Nodes)
	for _, t := range fx.Tokens {
		if err := m.Register(context.Background(), t.Token, t.UserID, t.SubscriptionID); err != nil {
			return err
		}
	}
	return nil
}
