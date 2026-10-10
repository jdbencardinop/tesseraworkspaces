//go:build !windows

package internal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type managedObjectIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func managedIdentityFromInfo(info fs.FileInfo) (managedObjectIdentity, error) {
	if info == nil {
		return managedObjectIdentity{}, errors.New("filesystem identity has no file information")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return managedObjectIdentity{}, fmt.Errorf("filesystem identity is unavailable for %s", info.Name())
	}
	return managedObjectIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, nil
}

func managedPathIdentity(path string) (managedObjectIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return managedObjectIdentity{}, err
	}
	return managedIdentityFromInfo(info)
}

func managedDirectoryIdentity(path string) (managedObjectIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return managedObjectIdentity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return managedObjectIdentity{}, fmt.Errorf("%s is not a regular directory", path)
	}
	return managedIdentityFromInfo(info)
}

func managedRootIdentity(root *os.Root) (managedObjectIdentity, error) {
	file, err := root.Open(".")
	if err != nil {
		return managedObjectIdentity{}, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return managedObjectIdentity{}, err
	}
	return managedIdentityFromInfo(info)
}

func managedVerifyPathIdentity(path string, want managedObjectIdentity) error {
	got, err := managedPathIdentity(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("filesystem root identity changed at %s", path)
	}
	return nil
}

func managedRenameNoReplace(root *os.Root, oldName, newName string) error {
	oldName = filepath.Clean(oldName)
	newName = filepath.Clean(newName)
	oldDirName, oldBase := filepath.Dir(oldName), filepath.Base(oldName)
	newDirName, newBase := filepath.Dir(newName), filepath.Base(newName)
	oldDir, oldIdentity, err := managedOpenPinnedDirectory(root, oldDirName, false)
	if err != nil {
		return err
	}
	defer oldDir.Close() //nolint:errcheck
	newDir := oldDir
	newIdentity := oldIdentity
	if newDirName != oldDirName {
		newDir, newIdentity, err = managedOpenPinnedDirectory(root, newDirName, false)
		if err != nil {
			return err
		}
		defer newDir.Close() //nolint:errcheck
	}
	if err := managedRenameNoReplaceFD(
		int(oldDir.Fd()), oldBase, int(newDir.Fd()), newBase,
	); err != nil {
		return fmt.Errorf("exclusive rename %s to %s: %w", oldName, newName, err)
	}
	var errs []error
	if err := oldDir.Sync(); err != nil {
		errs = append(errs, err)
	}
	if newDir != oldDir {
		if err := newDir.Sync(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := managedVerifyPinnedDirectoryBinding(root, oldDirName, oldIdentity); err != nil {
		errs = append(errs, err)
	}
	if newDirName != oldDirName {
		if err := managedVerifyPinnedDirectoryBinding(root, newDirName, newIdentity); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func managedSyncRootDir(root *os.Root, relative string) error {
	dir, identity, err := managedOpenPinnedDirectory(root, relative, false)
	if err != nil {
		return err
	}
	defer dir.Close() //nolint:errcheck
	if err := dir.Sync(); err != nil {
		return err
	}
	return managedVerifyPinnedDirectoryBinding(root, relative, identity)
}

func managedOpenPinnedDirectory(
	root *os.Root,
	relative string,
	create bool,
) (*os.File, managedObjectIdentity, error) {
	clean := filepath.Clean(relative)
	if clean == ".." || filepath.IsAbs(clean) ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, managedObjectIdentity{}, fmt.Errorf("unsafe managed parent path %q", relative)
	}
	current, err := root.Open(".")
	if err != nil {
		return nil, managedObjectIdentity{}, err
	}
	if clean != "." {
		for _, part := range strings.Split(clean, string(filepath.Separator)) {
			if part == "" || part == "." || part == ".." {
				_ = current.Close()
				return nil, managedObjectIdentity{}, fmt.Errorf("unsafe managed parent component %q", part)
			}
			fd, openErr := unix.Openat(
				int(current.Fd()), part,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0,
			)
			if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
				if err := unix.Mkdirat(int(current.Fd()), part, 0o755); err != nil &&
					!errors.Is(err, unix.EEXIST) {
					_ = current.Close()
					return nil, managedObjectIdentity{}, err
				}
				if err := current.Sync(); err != nil {
					_ = current.Close()
					return nil, managedObjectIdentity{}, err
				}
				fd, openErr = unix.Openat(
					int(current.Fd()), part,
					unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0,
				)
			}
			if openErr != nil {
				_ = current.Close()
				return nil, managedObjectIdentity{}, openErr
			}
			next := os.NewFile(uintptr(fd), part)
			if err := current.Close(); err != nil {
				_ = next.Close()
				return nil, managedObjectIdentity{}, err
			}
			current = next
		}
	}
	info, err := current.Stat()
	if err != nil {
		_ = current.Close()
		return nil, managedObjectIdentity{}, err
	}
	identity, err := managedIdentityFromInfo(info)
	if err != nil {
		_ = current.Close()
		return nil, managedObjectIdentity{}, err
	}
	return current, identity, nil
}

func managedLinkPinned(
	sourceParent *os.File,
	sourceName string,
	destinationParent *os.File,
	destinationName string,
) error {
	return unix.Linkat(
		int(sourceParent.Fd()), sourceName,
		int(destinationParent.Fd()), destinationName, 0,
	)
}

func managedVerifyPinnedDirectoryBinding(
	root *os.Root,
	relative string,
	expected managedObjectIdentity,
) error {
	clean := filepath.Clean(relative)
	reopened, identity, err := managedOpenPinnedDirectory(root, clean, false)
	if err != nil {
		return fmt.Errorf("revalidating managed directory binding %s: %w", clean, err)
	}
	if err := reopened.Close(); err != nil {
		return err
	}
	if identity != expected {
		return fmt.Errorf("managed directory identity changed at %s", clean)
	}
	return nil
}

func flockTryExclusive(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("flock LOCK_EX|LOCK_NB: %w", err)
	}
	return nil
}
