//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package internal

import (
	"errors"
	"os"
	"os/exec"
)

func configureStatusCommand(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return terminateStatusCommand(cmd) }
}

func terminateStatusCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
