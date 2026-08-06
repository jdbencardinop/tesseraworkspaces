//go:build !windows

package internal

import (
	"fmt"
	"os"
	"syscall"
)

// flockExclusive acquires an exclusive POSIX advisory lock on f.
// This is supported on macOS and Linux only.
func flockExclusive(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock LOCK_EX: %w", err)
	}
	return nil
}

// flockUnlock releases a POSIX advisory lock on f.
func flockUnlock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("flock LOCK_UN: %w", err)
	}
	return nil
}
