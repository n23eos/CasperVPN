package envcfg

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultsWhenUnset(t *testing.T) {
	var e Env
	if got := e.Str("ENVCFG_TEST_UNSET", "d"); got != "d" {
		t.Fatalf("Str = %q", got)
	}
	if got := e.Int("ENVCFG_TEST_UNSET", 7); got != 7 {
		t.Fatalf("Int = %d", got)
	}
	if got := e.Bool("ENVCFG_TEST_UNSET", true); !got {
		t.Fatal("Bool = false")
	}
	if got := e.Duration("ENVCFG_TEST_UNSET", time.Minute); got != time.Minute {
		t.Fatalf("Duration = %s", got)
	}
	if got := e.CSV("ENVCFG_TEST_UNSET"); got != nil {
		t.Fatalf("CSV = %v", got)
	}
	if err := e.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The whole point of the package: a malformed value must surface as an error,
// not silently fall back to the default.
func TestMalformedValuesFailFast(t *testing.T) {
	t.Setenv("ENVCFG_TEST_INT", "5O")     // letter O, the classic typo
	t.Setenv("ENVCFG_TEST_BOOL", "yes!")  // not ParseBool syntax
	t.Setenv("ENVCFG_TEST_DUR", "-5s")    // negative
	t.Setenv("ENVCFG_TEST_DUR2", "10sec") // bad unit

	var e Env
	e.Int("ENVCFG_TEST_INT", 1)
	e.Bool("ENVCFG_TEST_BOOL", false)
	e.Duration("ENVCFG_TEST_DUR", 0)
	e.Duration("ENVCFG_TEST_DUR2", 0)

	err := e.Err()
	if err == nil {
		t.Fatal("want joined errors, got nil")
	}
	for _, key := range []string{"ENVCFG_TEST_INT", "ENVCFG_TEST_BOOL", "ENVCFG_TEST_DUR", "ENVCFG_TEST_DUR2"} {
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("error does not mention %s: %v", key, err)
		}
	}
}

func TestValidValuesParse(t *testing.T) {
	t.Setenv("ENVCFG_TEST_OK_INT", "42")
	t.Setenv("ENVCFG_TEST_OK_DUR", "1500ms")
	t.Setenv("ENVCFG_TEST_OK_CSV", " a, ,b ,")

	var e Env
	if got := e.Int("ENVCFG_TEST_OK_INT", 0); got != 42 {
		t.Fatalf("Int = %d", got)
	}
	if got := e.Duration("ENVCFG_TEST_OK_DUR", 0); got != 1500*time.Millisecond {
		t.Fatalf("Duration = %s", got)
	}
	if got := e.CSV("ENVCFG_TEST_OK_CSV"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("CSV = %v", got)
	}
	if err := e.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
