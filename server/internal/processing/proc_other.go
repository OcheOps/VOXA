//go:build !unix

package processing

import "os/exec"

// configureProcessGroup falls back to killing the direct child only.
func configureProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(int) {}
