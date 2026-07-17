//go:build !unix

package provision

import "os/exec"

// setProcGroup is a no-op off unix: the default CommandContext kill plus WaitDelay
// apply. The lifecycle scripts only run on unix hosts in practice.
func setProcGroup(_ *exec.Cmd) {}
