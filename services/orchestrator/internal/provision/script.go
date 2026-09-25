// Package provision implements ports.Provisioner on top of the repository's
// infra/scripts lifecycle scripts (node_up.sh / node_rotate.sh /
// node_down.sh). Zero hardcode: clouds, regions, tokens and control-plane
// coordinates flow in exclusively through parameters and the process
// environment; this package never embeds an IP, domain or provider name.
//
// The scripts themselves register/patch/retire the Node in the control-plane
// (they source control_plane.sh); the orchestrator verifies the outcome via
// the control-plane API afterwards.
package provision

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/orchestrator/internal/ports"
)

// Result is the structured outcome of one script invocation.
type Result struct {
	Script   string
	ExitCode int
	Duration time.Duration
	// Tail holds the last portion of combined output for diagnostics.
	Tail string
}

// Error is a failed script invocation carrying the structured Result.
type Error struct {
	Result Result
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("provision: %s failed (exit=%d, took %s): %v\n%s",
		e.Result.Script, e.Result.ExitCode, e.Result.Duration.Round(time.Second), e.Err, e.Result.Tail)
}

func (e *Error) Unwrap() error { return e.Err }

// ScriptRunner runs the lifecycle scripts with a bounded timeout.
type ScriptRunner struct {
	// Dir is the directory holding the scripts (SCRIPTS_DIR).
	Dir string
	// Timeout bounds every invocation.
	Timeout time.Duration
	// Env is appended to the inherited environment (provider tokens,
	// CONTROL_PLANE_URL/TOKEN etc. are already in the process env).
	Env map[string]string
	// Logf receives one line per invocation outcome; nil disables logging.
	Logf func(format string, args ...any)
}

const (
	runIDLabel       = "orchestrator.run_id"
	workspaceLabel   = "orchestrator.tf_workspace"
	manifestFileMode = 0o600
)

const tailBytes = 4 << 10 // keep the last 4 KiB of output for diagnostics

// scriptWaitDelay bounds how long cmd.Wait blocks after the timeout kill before Go
// force-closes the I/O pipes and returns — a fallback for the case a descendant still
// holds an inherited pipe. The process-group kill (setProcGroup) is the primary
// mechanism; this only guards against a wedged Wait.
const scriptWaitDelay = 2 * time.Second

// NodeUp provisions and guarded-activates a fresh entry+exit pair in a unique run.
func (s *ScriptRunner) NodeUp(ctx context.Context, region, cloud string) (ports.ProvisionedPair, error) {
	if region == "" || cloud == "" {
		return ports.ProvisionedPair{}, fmt.Errorf("provision: node up requires region and cloud (got %q/%q)", region, cloud)
	}
	runID, err := freshRunID()
	if err != nil {
		return ports.ProvisionedPair{}, err
	}
	vars := map[string]string{
		"REGION": region, "CLOUD": cloud,
		"RUN_ID": runID, "TF_WORKSPACE": runID,
		"RUN_DIR": s.runDir(),
	}
	if err := s.run(ctx, "node_up.sh", vars); err != nil {
		return ports.ProvisionedPair{}, err
	}
	pair, err := s.readPair(runID)
	if err != nil {
		return ports.ProvisionedPair{}, err
	}
	if err := s.run(ctx, "reconcile_live.sh", map[string]string{"RUN_ID": runID, "RUN_DIR": s.runDir()}); err != nil {
		return ports.ProvisionedPair{}, fmt.Errorf("provision: replacement run %s failed guarded activation: %w", runID, err)
	}
	return pair, nil
}

// NodeRotate replaces the node's ephemeral entry IP and re-keys REALITY.
func (s *ScriptRunner) NodeRotate(ctx context.Context, node contracts.Node) error {
	if node.ID == "" {
		return fmt.Errorf("provision: node rotate requires a node id")
	}
	runID, err := lifecycleIdentity(node)
	if err != nil {
		return err
	}
	vars := map[string]string{"RUN_ID": runID, "RUN_DIR": s.runDir(), "NODE": node.ID}
	if err := s.run(ctx, "node_rotate.sh", vars); err != nil {
		return err
	}
	if err := s.run(ctx, "reconcile_live.sh", map[string]string{"RUN_ID": runID, "RUN_DIR": s.runDir()}); err != nil {
		return fmt.Errorf("provision: rotate run %s failed guarded activation: %w", runID, err)
	}
	return nil
}

// NodeDown drains and tears a node down.
func (s *ScriptRunner) NodeDown(ctx context.Context, node contracts.Node) error {
	if node.ID == "" {
		return fmt.Errorf("provision: node down requires a node id")
	}
	runID, err := lifecycleIdentity(node)
	if err != nil {
		return err
	}
	return s.run(ctx, "node_down.sh", map[string]string{
		"RUN_ID": runID, "RUN_DIR": s.runDir(), "NODE": node.ID,
	})
}

// SyncAccess converges VLESS and Hysteria2 from one leased CP snapshot.
func (s *ScriptRunner) SyncAccess(ctx context.Context, node contracts.Node, snapshot contracts.NodeAccessUsers) error {
	if node.ID == "" || snapshot.Revision == "" || snapshot.ValidUntil.IsZero() {
		return fmt.Errorf("provision: access sync requires node id, revision and valid_until")
	}
	if !snapshot.ValidUntil.After(time.Now()) {
		return fmt.Errorf("provision: access snapshot for %s already expired at %s", node.ID, snapshot.ValidUntil.UTC().Format(time.RFC3339))
	}
	runID, err := lifecycleIdentity(node)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "caspervpn-access-*.json")
	if err != nil {
		return fmt.Errorf("provision: create access snapshot: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	if err := f.Chmod(manifestFileMode); err != nil {
		_ = f.Close()
		return fmt.Errorf("provision: chmod access snapshot: %w", err)
	}
	if err := json.NewEncoder(f).Encode(snapshot); err != nil {
		_ = f.Close()
		return fmt.Errorf("provision: encode access snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("provision: close access snapshot: %w", err)
	}
	return s.run(ctx, "access_sync.sh", map[string]string{
		"RUN_ID": runID, "RUN_DIR": s.runDir(), "NODE": node.ID,
		"ACCESS_SNAPSHOT_FILE": path,
	})
}

func lifecycleIdentity(node contracts.Node) (string, error) {
	runID := strings.TrimSpace(node.Labels[runIDLabel])
	workspace := strings.TrimSpace(node.Labels[workspaceLabel])
	if runID == "" || workspace == "" {
		return "", fmt.Errorf("provision: node %s lacks %s/%s labels", node.ID, runIDLabel, workspaceLabel)
	}
	if runID != workspace {
		return "", fmt.Errorf("provision: node %s identity mismatch: run=%s workspace=%s", node.ID, runID, workspace)
	}
	return runID, nil
}

func freshRunID() (string, error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("provision: generate run id: %w", err)
	}
	return "run-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(suffix[:]), nil
}

func (s *ScriptRunner) runDir() string {
	if v := strings.TrimSpace(s.Env["RUN_DIR"]); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("RUN_DIR")); v != "" {
		return v
	}
	dir, err := filepath.Abs(filepath.Join(s.Dir, "..", "..", ".runs"))
	if err != nil {
		return filepath.Join(s.Dir, "..", "..", ".runs")
	}
	return dir
}

func (s *ScriptRunner) readPair(runID string) (ports.ProvisionedPair, error) {
	b, err := os.ReadFile(filepath.Join(s.runDir(), runID+".json"))
	if err != nil {
		return ports.ProvisionedPair{}, fmt.Errorf("provision: read run manifest %s: %w", runID, err)
	}
	var m struct {
		RunID string `json:"run_id"`
		Entry struct {
			CPID string `json:"cp_id"`
		} `json:"entry"`
		Exit struct {
			CPID string `json:"cp_id"`
		} `json:"exit"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return ports.ProvisionedPair{}, fmt.Errorf("provision: decode run manifest %s: %w", runID, err)
	}
	if m.RunID != runID || m.Entry.CPID == "" || m.Exit.CPID == "" {
		return ports.ProvisionedPair{}, fmt.Errorf("provision: incomplete or mismatched run manifest %s", runID)
	}
	return ports.ProvisionedPair{RunID: runID, EntryID: m.Entry.CPID, ExitID: m.Exit.CPID}, nil
}

func (s *ScriptRunner) run(ctx context.Context, script string, vars map[string]string) error {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	path := filepath.Join(s.Dir, script)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("provision: script %s: %w", path, err)
	}

	cmd := exec.CommandContext(ctx, path)
	// Kill the WHOLE process tree on timeout, not just the direct child. A script
	// whose shell forks a grandchild (e.g. dash running `sleep`) would otherwise
	// leave that grandchild alive holding the output pipe, so cmd.Run() blocks until
	// it exits on its own. setProcGroup puts the command in its own group and cancels
	// by signalling the group; WaitDelay is the bounded fallback for a wedged Wait.
	setProcGroup(cmd)
	cmd.WaitDelay = scriptWaitDelay
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	start := time.Now()
	err := cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	res := Result{
		Script:   script,
		ExitCode: exitCode,
		Duration: time.Since(start),
		Tail:     tail(out.Bytes()),
	}
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s: %w", timeout, ctx.Err())
	}
	if err != nil {
		return &Error{Result: res, Err: err}
	}
	if s.Logf != nil {
		s.Logf("provision: %s ok (took %s)", script, res.Duration.Round(time.Second))
	}
	return nil
}

func tail(b []byte) string {
	if len(b) > tailBytes {
		b = b[len(b)-tailBytes:]
	}
	return string(b)
}
