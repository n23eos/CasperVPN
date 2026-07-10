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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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

const tailBytes = 4 << 10 // keep the last 4 KiB of output for diagnostics

// NodeUp provisions a fresh entry+exit pair in the given placement.
func (s *ScriptRunner) NodeUp(ctx context.Context, region, cloud string) error {
	if region == "" || cloud == "" {
		return fmt.Errorf("provision: node up requires region and cloud (got %q/%q)", region, cloud)
	}
	return s.run(ctx, "node_up.sh", map[string]string{"REGION": region, "CLOUD": cloud})
}

// NodeRotate replaces the node's ephemeral entry IP and re-keys REALITY.
func (s *ScriptRunner) NodeRotate(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return fmt.Errorf("provision: node rotate requires a node id")
	}
	return s.run(ctx, "node_rotate.sh", map[string]string{"NODE": nodeID})
}

// NodeDown drains and tears a node down.
func (s *ScriptRunner) NodeDown(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return fmt.Errorf("provision: node down requires a node id")
	}
	return s.run(ctx, "node_down.sh", map[string]string{"NODE": nodeID})
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
