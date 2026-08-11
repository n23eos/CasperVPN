//go:build unix

package provision

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// processAlive reports whether pid is a live (non-zombie) process. kill(pid, 0)
// alone is not enough: in containers whose PID 1 does not reap orphans, a killed
// child lingers as a zombie and kill(pid, 0) still succeeds on it.
func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// No /proc entry (e.g. non-Linux unix): fall back to the signal check,
		// which said the process exists.
		return true
	}
	// State is the field right after the parenthesized comm, which may itself
	// contain spaces — parse from the last ')'.
	rest := string(stat)
	if i := strings.LastIndexByte(rest, ')'); i >= 0 {
		rest = strings.TrimSpace(rest[i+1:])
	}
	return !strings.HasPrefix(rest, "Z")
}

// TestTimeoutKillsProcessTree proves the timeout kills the WHOLE process tree, not
// just that Run() returns quickly: the script forks a background child that would
// create a marker after a delay, then blocks. After the timeout the child must be
// gone AND the marker must never appear.
func TestTimeoutKillsProcessTree(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	pidfile := filepath.Join(dir, "child.pid")
	writeScript(t, dir, "node_rotate.sh",
		`( sleep 3; touch "`+marker+`" ) &`+"\n"+
			`echo $! > "`+pidfile+`"`+"\n"+
			`sleep 30`)

	// Timeout comfortably exceeds the child fork + pidfile write, but is well under
	// the child's 3s marker delay, so a correct kill still prevents the marker.
	s := &ScriptRunner{Dir: dir, Timeout: time.Second}
	start := time.Now()
	if err := s.NodeRotate(context.Background(), "node-1"); err == nil {
		t.Fatal("want timeout error")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Run did not return promptly (took %s) — Wait wedged on a surviving child", took)
	}

	// The forked child must be dead (its process group was signalled).
	pidRaw, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", pidRaw, err)
	}
	if processAlive(pid) {
		t.Fatalf("child %d still alive after timeout — process tree not killed", pid)
	}

	// Its deferred side effect must never happen.
	time.Sleep(4 * time.Second) // past the child's 3s marker delay
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker created after the kill — a descendant kept running")
	}
}
