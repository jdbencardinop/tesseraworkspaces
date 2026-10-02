//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package internal

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureStatusCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return terminateStatusCommand(cmd) }
}

func terminateStatusCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
