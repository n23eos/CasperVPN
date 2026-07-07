package money

import "testing"

func TestGTE(t *testing.T) {
	cases := []struct {
		paid, expected string
		want           bool
	}{
		{"0.0001", "0.0001", true},   // exact
		{"0.0002", "0.0001", true},   // overpaid
		{"0.00005", "0.0001", false}, // underpaid
		{"1.000000001", "1", true},   // fine-grained
	}
	for _, c := range cases {
		got, err := GTE(c.paid, c.expected)
		if err != nil {
			t.Fatalf("GTE(%s,%s): %v", c.paid, c.expected, err)
		}
		if got != c.want {
			t.Fatalf("GTE(%s,%s) = %v, want %v", c.paid, c.expected, got, c.want)
		}
	}
}

func TestGTE_InvalidInput(t *testing.T) {
	if _, err := GTE("abc", "1"); err == nil {
		t.Fatal("expected error on invalid amount")
	}
}

func TestValid(t *testing.T) {
	if !Valid("0.0001") {
		t.Fatal("0.0001 should be valid")
	}
	if Valid("-1") {
		t.Fatal("negative should be invalid")
	}
	if Valid("xyz") {
		t.Fatal("garbage should be invalid")
	}
}
