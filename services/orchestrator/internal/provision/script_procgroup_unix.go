//go:build unix

package provision

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcGroup puts cmd in its own process group and, when the context is cancelled
// (timeout), SIGKILLs the ENTIRE group (negative pid) rather than only the direct
// child. A shell that forks a grandchild (dash running `sleep`) would otherwise leave
// that grandchild alive holding the command's output pipe, wedging cmd.Wait.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// Negative pid targets the whole process group led by the child.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return os.ErrProcessDone
	}
}
