//go:build unix

package processing

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the child in a new process group and makes
// cancellation signal the whole group (SIGTERM, then SIGKILL after WaitDelay),
// so no grandchild survives a cancelled job.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

func killProcessGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }
