package internal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func featureRenameSyncDirectory(binding featureRenameDirectory) error {
	root, err := featureRenamePinnedRoot(binding)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	if err := featureRenameCheckpoint("before-directory-sync", binding.Path); err != nil {
		return err
	}
	if err := binding.verify(); err != nil {
		return err
	}
	if err := managedSyncRootDir(root, "."); err != nil {
		return err
	}
	if err := binding.verify(); err != nil {
		return err
	}
	return featureRenameCheckpoint("after-directory-sync", binding.Path)
}

// Keep the syscall/fsync boundary observable without weakening the shared
// no-replace primitive. Recovery must repeat these directory barriers even
// when a prior syscall's effect is already visible.
func featureRenameRenameNoReplace(binding featureRenameDirectory, from, to string) error {
	root, err := featureRenamePinnedRoot(binding)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	source, sourceID, err := managedOpenPinnedDirectory(root, filepath.Dir(from), false)
	if err != nil {
		return err
	}
	defer source.Close() //nolint:errcheck
	destination, destinationID, err := managedOpenPinnedDirectory(root, filepath.Dir(to), false)
	if err != nil {
		return err
	}
	defer destination.Close() //nolint:errcheck
	if err := managedRenameNoReplaceFD(int(source.Fd()), filepath.Base(from), int(destination.Fd()), filepath.Base(to)); err != nil {
		return fmt.Errorf("exclusive rename %s to %s: %w", from, to, err)
	}
	if err := featureRenameCheckpoint("after-rename-before-sync", filepath.Join(binding.Path, to)); err != nil {
		return err
	}
	if err := binding.verify(); err != nil {
		return err
	}
	if err := errors.Join(
		managedVerifyPinnedDirectoryBinding(root, filepath.Dir(from), sourceID),
		managedVerifyPinnedDirectoryBinding(root, filepath.Dir(to), destinationID),
	); err != nil {
		return err
	}
	return errors.Join(
		featureRenameSyncDirectory(featureRenameDirectory{Path: filepath.Join(binding.Path, filepath.Dir(from)), Identity: sourceID}),
		featureRenameSyncDirectory(featureRenameDirectory{Path: filepath.Join(binding.Path, filepath.Dir(to)), Identity: destinationID}),
	)
}

func (tx *featureRenameOperation) syncPointerBindings(index int, expectedState string) error {
	if err := tx.verifyStable(); err != nil {
		return err
	}
	state, err := tx.pointerState(index)
	if err != nil || state != expectedState {
		return errors.Join(err, errors.New("pointer binding changed before durability barrier"))
	}
	active, _, _, _, binding, err := tx.pointerPaths(index)
	if err != nil {
		return err
	}
	parent := binding
	if pointer := tx.plan.Pointers[index]; pointer.Kind == "forward" {
		parent = tx.plan.Worktrees[pointer.Worktree].Directory
		parent.Path = filepath.Dir(active)
	}
	if err := errors.Join(featureRenameSyncDirectory(*tx.stage(index).Directory), featureRenameSyncDirectory(parent)); err != nil {
		return err
	}
	state, err = tx.pointerState(index)
	if err != nil || state != expectedState {
		return errors.Join(err, errors.New("pointer binding changed during durability barrier"))
	}
	return nil
}

func (tx *featureRenameOperation) syncJournalEvidence() error {
	if err := tx.verifyJournal(); err != nil {
		return err
	}
	root, err := featureRenamePinnedRoot(tx.plan.Operation)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	directory, identity, err := managedOpenPinnedDirectory(root, ".", false)
	if err != nil {
		return err
	}
	defer directory.Close() //nolint:errcheck
	if identity != tx.plan.Operation.Identity {
		return errors.New("journal directory changed before durability barrier")
	}
	files := append([]featureRenameFile{tx.journal}, tx.eventFiles...)
	for _, expected := range files {
		if err := expected.verifyAt(expected.Path); err != nil {
			return err
		}
		if err := featureRenameCheckpoint("before-journal-file-open", expected.Path); err != nil {
			return err
		}
		fd, err := unix.Openat(int(directory.Fd()), filepath.Base(expected.Path),
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), expected.Path)
		info, statErr := file.Stat()
		identity, identityErr := managedIdentityFromInfo(info)
		if statErr != nil || identityErr != nil || !info.Mode().IsRegular() ||
			identity.Device != expected.Observation.Device || identity.Inode != expected.Observation.Inode {
			return errors.Join(statErr, identityErr, file.Close(), errors.New("journal file changed before durability barrier"))
		}
		if err := featureRenameCheckpoint("before-journal-file-sync", expected.Path); err != nil {
			return errors.Join(err, file.Close())
		}
		if err := errors.Join(tx.plan.Operation.verify(), expected.verifyAt(expected.Path)); err != nil {
			return errors.Join(err, file.Close())
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			return err
		}
		if err := expected.verifyAt(expected.Path); err != nil {
			return err
		}
	}
	for path := tx.plan.Operation.Path; ; path = filepath.Dir(path) {
		var binding featureRenameDirectory
		for _, parent := range tx.plan.Parents {
			if parent.Path == path {
				binding = parent
				break
			}
		}
		if binding.Path == "" {
			return errors.New("journal durability barrier has no recorded parent")
		}
		if err := featureRenameSyncDirectory(binding); err != nil {
			return err
		}
		if path == tx.plan.Root.Path {
			break
		}
	}
	return nil
}

func (tx *featureRenameOperation) syncFinalDirectories() error {
	seen := map[featureRenameDirectory]bool{}
	directories := []featureRenameDirectory{tx.plan.Root}
	for i, pointer := range tx.plan.Pointers {
		active, _, _, _, binding, err := tx.pointerPaths(i)
		if err != nil {
			return err
		}
		if pointer.Kind == "forward" {
			binding = tx.plan.Worktrees[pointer.Worktree].Directory
			binding.Path = filepath.Dir(active)
		}
		directories = append(directories, binding)
	}
	for _, directory := range directories {
		if seen[directory] {
			continue
		}
		seen[directory] = true
		if err := featureRenameSyncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}
