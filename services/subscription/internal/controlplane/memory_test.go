package controlplane

import (
	"context"
	"testing"
)

// The token index must never hold plaintext tokens at rest
// (TZ-token-revocation §3): a leaked index dump must not yield working links.
func TestTokenIndex_StoresOnlyHashes(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	const tok = "plaintext-token-abc"

	if err := m.Register(ctx, tok, "u1", "s1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := m.tokens[tok]; ok {
		t.Fatal("plaintext token stored as index key")
	}
	if _, ok := m.tokens[HashToken(tok)]; !ok {
		t.Fatal("hashed token key missing")
	}

	uid, sid, err := m.Lookup(ctx, tok)
	if err != nil || uid != "u1" || sid != "s1" {
		t.Fatalf("lookup via plaintext failed: %s/%s %v", uid, sid, err)
	}

	if err := m.Revoke(ctx, tok); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, err := m.Lookup(ctx, tok); err == nil {
		t.Fatal("token resolvable after revoke")
	}
}

func TestTokenIndex_OwnerScopedRevocation(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	_ = m.Register(ctx, "t1", "u1", "s1")
	_ = m.Register(ctx, "t2", "u1", "s2")
	_ = m.Register(ctx, "t3", "u2", "s3")

	n, err := m.RevokeBySubscription(ctx, "s1")
	if err != nil || n != 1 {
		t.Fatalf("revoke by sub: n=%d err=%v", n, err)
	}
	if _, _, err := m.Lookup(ctx, "t1"); err == nil {
		t.Fatal("s1 token survived subscription revoke")
	}
	if _, _, err := m.Lookup(ctx, "t2"); err != nil {
		t.Fatal("sibling token must survive subscription revoke")
	}

	n, err = m.RevokeByUser(ctx, "u1")
	if err != nil || n != 1 {
		t.Fatalf("revoke by user: n=%d err=%v", n, err)
	}
	if _, _, err := m.Lookup(ctx, "t2"); err == nil {
		t.Fatal("u1 token survived user revoke")
	}
	if _, _, err := m.Lookup(ctx, "t3"); err != nil {
		t.Fatal("other user's token must survive")
	}
}
