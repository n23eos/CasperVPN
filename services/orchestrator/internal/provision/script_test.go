package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeScript drops an executable fake lifecycle script into dir.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
}

func TestNodeRotatePassesEnvAndArgs(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec")
	writeScript(t, dir, "node_rotate.sh", `echo "NODE=$NODE EXTRA=$EXTRA" > `+rec)

	s := &ScriptRunner{Dir: dir, Timeout: 5 * time.Second, Env: map[string]string{"EXTRA": "from-config"}}
	if err := s.NodeRotate(context.Background(), "node-1"); err != nil {
		t.Fatalf("NodeRotate() error: %v", err)
	}
	got, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if want := "NODE=node-1 EXTRA=from-config"; !strings.Contains(string(got), want) {
		t.Fatalf("script saw %q, want %q", got, want)
	}
}

func TestNodeUpRequiresPlacement(t *testing.T) {
	s := &ScriptRunner{Dir: t.TempDir()}
	if err := s.NodeUp(context.Background(), "", "cloud-a"); err == nil {
		t.Fatal("want error without region")
	}
	if err := s.NodeUp(context.Background(), "eu-central", ""); err == nil {
		t.Fatal("want error without cloud")
	}
}

func TestNonZeroExitReturnsStructuredError(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeScript(t, dir, "node_down.sh", `echo "terraform exploded" >&2; exit 3`)

	s := &ScriptRunner{Dir: dir, Timeout: 5 * time.Second}
	err := s.NodeDown(context.Background(), "node-1")
	if err == nil {
		t.Fatal("want error on exit 3")
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("want *provision.Error, got %T: %v", err, err)
	}
	if pe.Result.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", pe.Result.ExitCode)
	}
	if !strings.Contains(pe.Result.Tail, "terraform exploded") {
		t.Errorf("Tail should carry script output, got %q", pe.Result.Tail)
	}
}

func TestTimeoutKillsScript(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeScript(t, dir, "node_rotate.sh", `sleep 30`)

	s := &ScriptRunner{Dir: dir, Timeout: 300 * time.Millisecond}
	start := time.Now()
	err := s.NodeRotate(context.Background(), "node-1")
	if err == nil {
		t.Fatal("want timeout error")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("script not killed on timeout (took %s)", took)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention timeout, got: %v", err)
	}
}

func TestMissingScript(t *testing.T) {
	s := &ScriptRunner{Dir: t.TempDir(), Timeout: time.Second}
	if err := s.NodeRotate(context.Background(), "node-1"); err == nil {
		t.Fatal("want error for missing script")
	}
}
