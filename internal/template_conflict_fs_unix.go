//go:build !windows

package internal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func readManagedConflictFile(root *os.Root, relative string, expected managedFileObservation) ([]byte, error) {
	parentPath := filepath.Dir(relative)
	parent, parentIdentity, err := managedOpenPinnedDirectory(root, parentPath, false)
	if err != nil {
		return nil, err
	}
	defer parent.Close() //nolint:errcheck
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(relative),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), relative)
	defer file.Close() //nolint:errcheck
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("managed observed file %s is no longer regular", relative)
	}
	identity, err := managedIdentityFromInfo(before)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	var current unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), filepath.Base(relative), &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	got := managedFileObservation{
		Exists: true, Kind: "file", Hash: digestBytes(data), Mode: managedObservedPermissionMode(after.Mode()),
		Device: identity.Device, Inode: identity.Inode,
	}
	if got != expected || !os.SameFile(before, after) ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		before.Mode() != after.Mode() || current.Mode&unix.S_IFMT != unix.S_IFREG ||
		uint64(current.Dev) != identity.Device || uint64(current.Ino) != identity.Inode {
		return nil, fmt.Errorf("managed observed file changed after consent at %s", relative)
	}
	if err := managedVerifyPinnedDirectoryBinding(root, parentPath, parentIdentity); err != nil {
		return nil, errors.Join(errors.New("managed observed file parent changed"), err)
	}
	return data, nil
}
