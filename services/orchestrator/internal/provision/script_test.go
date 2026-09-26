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

	"github.com/caspervpn/contracts"
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

func lifecycleNode(id string) contracts.Node {
	return contracts.Node{ID: id, Labels: map[string]string{
		runIDLabel: "run-1", workspaceLabel: "run-1",
	}}
}

func lifecycleEntryNode(id string) contracts.Node {
	n := lifecycleNode(id)
	n.Transports = []contracts.Transport{
		{
			Type: contracts.TransportVlessReality, Enabled: true,
			VlessReality: &contracts.VlessRealityParams{
				ServerNames: []string{"front-a.test", "front-b.test"}, Dest: "front-a.test:443",
			},
		},
		{
			Type: contracts.TransportHysteria2, Enabled: true,
			Hysteria2: &contracts.Hysteria2Params{SNI: "hy2.test"},
		},
	}
	return n
}

func TestNodeTransportContextRequiresBothLaunchFamilies(t *testing.T) {
	names, dest, sni, err := nodeTransportContext(lifecycleEntryNode("entry-1"))
	if err != nil {
		t.Fatalf("nodeTransportContext() error: %v", err)
	}
	if names != "front-a.test,front-b.test" || dest != "front-a.test:443" || sni != "hy2.test" {
		t.Fatalf("context = %q %q %q", names, dest, sni)
	}
	if _, _, _, err := nodeTransportContext(lifecycleNode("entry-1")); err == nil {
		t.Fatal("nodeTransportContext() accepted a node without two enabled launch families")
	}
}

func TestNodeRotatePassesEnvAndArgs(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec")
	writeScript(t, dir, "node_rotate.sh", `echo "NODE=$NODE EXTRA=$EXTRA" > `+rec)
	writeScript(t, dir, "reconcile_live.sh", `exit 0`)

	s := &ScriptRunner{Dir: dir, Timeout: 5 * time.Second, Env: map[string]string{"EXTRA": "from-config"}}
	if err := s.NodeRotate(context.Background(), lifecycleNode("node-1")); err != nil {
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
	if _, err := s.NodeUp(context.Background(), "", "cloud-a"); err == nil {
		t.Fatal("want error without region")
	}
	if _, err := s.NodeUp(context.Background(), "eu-central", ""); err == nil {
		t.Fatal("want error without cloud")
	}
}

func TestNonZeroExitReturnsStructuredError(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeScript(t, dir, "node_down.sh", `echo "terraform exploded" >&2; exit 3`)

	s := &ScriptRunner{Dir: dir, Timeout: 5 * time.Second}
	err := s.NodeDown(context.Background(), lifecycleNode("node-1"))
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
	err := s.NodeRotate(context.Background(), lifecycleNode("node-1"))
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
	if err := s.NodeRotate(context.Background(), lifecycleNode("node-1")); err == nil {
		t.Fatal("want error for missing script")
	}
}

func TestNodeUpCreatesFreshRunAndActivatesPair(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	runDir := t.TempDir()
	rec := filepath.Join(dir, "rec")
	writeScript(t, dir, "node_up.sh", `
test "$RUN_ID" = "$TF_WORKSPACE" || exit 9
printf '%s\n' "$RUN_ID" > "`+rec+`"
mkdir -p "$RUN_DIR"
printf '{"run_id":"%s","entry":{"cp_id":"entry-new"},"exit":{"cp_id":"exit-new"}}' "$RUN_ID" > "$RUN_DIR/$RUN_ID.json"
`)
	writeScript(t, dir, "reconcile_live.sh", `test -f "$RUN_DIR/$RUN_ID.json"`)
	s := &ScriptRunner{Dir: dir, Timeout: 5 * time.Second, Env: map[string]string{"RUN_DIR": runDir}}
	pair, err := s.NodeUp(context.Background(), "eu-central", "cloud-a")
	if err != nil {
		t.Fatalf("NodeUp() error: %v", err)
	}
	if pair.EntryID != "entry-new" || pair.ExitID != "exit-new" || pair.RunID == "" {
		t.Fatalf("pair = %+v", pair)
	}
	raw, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != pair.RunID || !strings.HasPrefix(got, "run-") {
		t.Fatalf("run identity = %q, pair=%+v", got, pair)
	}
}

func TestRealNodeDownScriptUsesRunManifestContract(t *testing.T) {
	skipOnWindows(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	manifest := `{"run_id":"run-1","tf_workspace":"run-1","entry":{"cp_id":"node-1","raw_id":"e1","ip":"","cloud":"hetzner","region":"hel1"},"exit":{"cp_id":"exit-1","raw_id":"x1","ip":"","cloud":"vultr","region":"waw"},"state_backup":""}`
	if err := os.WriteFile(filepath.Join(runDir, "run-1.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	terraform := filepath.Join(fakeBin, "terraform")
	if err := os.WriteFile(terraform, []byte("#!/bin/sh\ncase \"$*\" in *'workspace show'*) echo run-1;; esac\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ansible-playbook", "curl"} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := &ScriptRunner{
		Dir:     filepath.Join(repoRoot, "infra", "scripts"),
		Timeout: 5 * time.Second,
		Env: map[string]string{
			"RUN_DIR": runDir,
			"PATH":    fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		},
	}
	if err := s.NodeDown(context.Background(), lifecycleNode("node-1")); err != nil {
		t.Fatalf("real node_down.sh contract failed: %v", err)
	}
}
