package secret

import "testing"

func TestRealityShortID_Width8Bytes(t *testing.T) {
	id, err := RealityShortID()
	if err != nil {
		t.Fatalf("RealityShortID: %v", err)
	}
	// 8 bytes => 16 lowercase hex chars (max width REALITY allows).
	if len(id) != 16 {
		t.Fatalf("short-id width = %d hex chars, want 16", len(id))
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("short-id has non-hex char %q in %q", c, id)
		}
	}
}

func TestRealityShortID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id, err := RealityShortID()
		if err != nil {
			t.Fatalf("RealityShortID: %v", err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("short-id collision at iteration %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}
