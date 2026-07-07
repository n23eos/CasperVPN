package ingest

import (
	"testing"
	"time"
)

func TestDeduper_RejectsReplay(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	clock := now
	d := NewDeduper(30*time.Minute, func() time.Time { return clock })

	if !d.Fresh("sig-1") {
		t.Fatal("first sight should be fresh")
	}
	if d.Fresh("sig-1") {
		t.Fatal("replay within TTL must be rejected")
	}
	// After TTL expires, the id is fresh again.
	clock = clock.Add(31 * time.Minute)
	if !d.Fresh("sig-1") {
		t.Fatal("id should be fresh again after TTL")
	}
}

func TestDeduper_EmptyNeverFresh(t *testing.T) {
	d := NewDeduper(time.Minute, nil)
	if d.Fresh("") {
		t.Fatal("empty id must never be fresh")
	}
}
