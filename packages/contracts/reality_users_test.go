package contracts

import "testing"

func TestRealityUsersRevision_OrderIndependent(t *testing.T) {
	// Arrange
	a := []RealityUser{
		{UUID: "00000000-0000-0000-0000-000000000002", ShortID: "bb22"},
		{UUID: "00000000-0000-0000-0000-000000000001", ShortID: "aa11"},
	}
	b := []RealityUser{
		{UUID: "00000000-0000-0000-0000-000000000001", ShortID: "aa11"},
		{UUID: "00000000-0000-0000-0000-000000000002", ShortID: "bb22"},
	}

	// Act
	ra := RealityUsersRevision(a)
	rb := RealityUsersRevision(b)

	// Assert
	if ra != rb {
		t.Fatalf("revision must be order-independent: %s != %s", ra, rb)
	}
	if ra == "" {
		t.Fatal("revision must be non-empty")
	}
}

func TestRealityUsersRevision_EmptyStable(t *testing.T) {
	if RealityUsersRevision(nil) != RealityUsersRevision([]RealityUser{}) {
		t.Fatal("nil and empty slice must yield the same revision")
	}
	if RealityUsersRevision(nil) == "" {
		t.Fatal("empty revision must still be a non-empty digest")
	}
}

func TestRealityUsersRevision_ChangesOnAddRemoveRotate(t *testing.T) {
	base := []RealityUser{{UUID: "u1", ShortID: "aa11"}}
	added := []RealityUser{{UUID: "u1", ShortID: "aa11"}, {UUID: "u2", ShortID: "bb22"}}
	rotated := []RealityUser{{UUID: "u1", ShortID: "cc33"}} // same user, new short-id
	empty := []RealityUser{}

	r := RealityUsersRevision(base)
	if r == RealityUsersRevision(added) {
		t.Error("adding a user must change the revision")
	}
	if r == RealityUsersRevision(rotated) {
		t.Error("rotating a user's short-id must change the revision")
	}
	if r == RealityUsersRevision(empty) {
		t.Error("removing the last user must change the revision")
	}
}

func TestRealityUsersRevision_NoDelimiterCollision(t *testing.T) {
	// Two distinct sets that a naive concatenation could confuse.
	x := []RealityUser{{UUID: "ab", ShortID: "cd"}}
	y := []RealityUser{{UUID: "abc", ShortID: "d"}}
	if RealityUsersRevision(x) == RealityUsersRevision(y) {
		t.Fatal("length-prefixing must prevent field-boundary collisions")
	}
}
