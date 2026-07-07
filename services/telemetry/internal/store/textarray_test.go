package store

import (
	"reflect"
	"testing"
)

func TestTextArray_RoundTrip(t *testing.T) {
	cases := [][]string{
		nil,
		{"RU-MOW"},
		{"RU-MOW", "IR-07", "DE-BE"},
		{`with,comma`},
		{`with"quote`},
		{`with\backslash`},
	}
	for _, in := range cases {
		lit := pqTextArray(in)
		got := parseTextArray(lit)
		if len(in) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, in) {
			t.Fatalf("round trip: in=%v lit=%q got=%v", in, lit, got)
		}
	}
}

func TestParseTextArray_Empty(t *testing.T) {
	if got := parseTextArray("{}"); got != nil {
		t.Fatalf("empty literal → %v, want nil", got)
	}
	if got := parseTextArray(""); got != nil {
		t.Fatalf("blank literal → %v, want nil", got)
	}
}

func TestParseTextArray_UnquotedFromPostgres(t *testing.T) {
	// Postgres emits simple elements unquoted; parser must still split them.
	got := parseTextArray("{RU-MOW,IR-07}")
	want := []string{"RU-MOW", "IR-07"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
