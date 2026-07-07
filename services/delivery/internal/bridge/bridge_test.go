package bridge

import (
	"context"
	"errors"
	"testing"
)

func TestStubBrokerReportsNotImplemented(t *testing.T) {
	var b StubBroker
	if err := b.Announce(context.Background(), Offer{BridgeID: "x"}); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Announce = %v", err)
	}
	if _, err := b.Match(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Match = %v", err)
	}
}

func TestBridgeChannelStub(t *testing.T) {
	c := NewChannel(StubBroker{})
	if c.Kind() != kindBridge {
		t.Fatalf("kind = %q", c.Kind())
	}
	if err := c.Publish(context.Background(), "k", []byte("b")); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Publish = %v", err)
	}
	if _, err := c.Fetch(context.Background(), "k"); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Fetch = %v", err)
	}
	if c.Healthy(context.Background()) {
		t.Fatalf("stub bridge must be unhealthy until a real broker is wired")
	}
}
