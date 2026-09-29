package internal

import (
	"fmt"
	"strings"
)

func pinSyncDetachedHeads(tx *SyncTransaction, save func() error) error {
	for i := range tx.Repositories {
		repo := &tx.Repositories[i]
		for j := range repo.Holders {
			holder := &repo.Holders[j]
			if !holder.OriginalDetached {
				continue
			}
			pin := syncTransactionPinRef(tx.RunID, repo.CommonDir, "holder:"+holder.Path)
			sha, found, err := syncTransactionResolveRef(repo.Root, pin)
			if err != nil {
				return err
			}
			if found && sha != holder.OriginalHEAD {
				return fmt.Errorf("original detached HEAD pin %s changed", pin)
			}
			if !found {
				_, stderr, _, err := syncTransactionGit(repo.Root, nil, "update-ref", "--no-deref",
					pin, holder.OriginalHEAD, strings.Repeat("0", repo.OIDWidth))
				if err != nil {
					return fmt.Errorf("pin original detached HEAD: %s", syncTransactionGitError(stderr, err))
				}
			}
			holder.OriginalHeadPinned = true
			if err := save(); err != nil {
				return err
			}
		}
	}
	return nil
}

func deleteSyncDetachedHeadPins(tx *SyncTransaction, repo *SyncTransactionRepo, save func() error) error {
	for i := range repo.Holders {
		holder := &repo.Holders[i]
		if !holder.OriginalDetached {
			continue
		}
		pin := syncTransactionPinRef(tx.RunID, repo.CommonDir, "holder:"+holder.Path)
		sha, found, err := syncTransactionResolveRef(repo.Root, pin)
		if err != nil {
			return err
		}
		if !found {
			if holder.OriginalHeadPinned && !holder.HeadPinDeletePending {
				return fmt.Errorf("original detached HEAD pin %s disappeared before cleanup intent", pin)
			}
		} else {
			if sha != holder.OriginalHEAD {
				return fmt.Errorf("original detached HEAD pin %s changed", pin)
			}
			holder.HeadPinDeletePending = true
			if err := save(); err != nil {
				return err
			}
			if err := syncTransactionStep("holder-head-pin-delete-intent:" + holder.Path); err != nil {
				return err
			}
			_, stderr, _, err := syncTransactionGit(repo.Root, nil, "update-ref", "--no-deref",
				"-d", pin, holder.OriginalHEAD)
			if err != nil {
				return fmt.Errorf("delete detached HEAD pin: %s", syncTransactionGitError(stderr, err))
			}
			if err := syncTransactionStep("holder-head-pin-deleted:" + holder.Path); err != nil {
				return err
			}
		}
		holder.OriginalHeadPinned = false
		holder.HeadPinDeletePending = false
		if err := save(); err != nil {
			return err
		}
	}
	return nil
}

func syncHolderAtExpected(holder *SyncTransactionHolder, position SyncTransactionHolderValue) bool {
	return position.Path == holder.Path && position.Branch == holder.ExpectedBranch &&
		position.HEAD == holder.ExpectedHEAD && position.Detached == holder.ExpectedDetached
}

func observeSyncForwardRestoration(repo *SyncTransactionRepo, holder *SyncTransactionHolder) (bool, error) {
	if holder.ForwardRestoreTarget == nil {
		return false, nil
	}
	positions, err := syncTransactionHolderValues(repo.Root)
	if err != nil {
		return false, err
	}
	live, ok := syncTransactionHolderMap(positions)[holder.Path]
	if !ok {
		return false, fmt.Errorf("holder %s disappeared during checkout restoration", holder.Path)
	}
	if live == *holder.ForwardRestoreTarget {
		holder.ExpectedBranch = live.Branch
		holder.ExpectedHEAD = live.HEAD
		holder.ExpectedDetached = live.Detached
		holder.ForwardRestoreTarget = nil
		return true, nil
	}
	if !syncHolderAtExpected(holder, live) {
		return false, fmt.Errorf("holder %s is at neither persisted checkout restoration position", holder.Path)
	}
	return false, nil
}

func reconcileSyncForwardRestorations(tx *SyncTransaction, save func() error) error {
	if tx == nil {
		return nil
	}
	for i := range tx.Repositories {
		repo := &tx.Repositories[i]
		for j := range repo.Holders {
			changed, err := observeSyncForwardRestoration(repo, &repo.Holders[j])
			if err != nil {
				return err
			}
			if changed {
				if err := save(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func RestoreSyncCheckoutContext(tx *SyncTransaction, path string, restore, save func() error) error {
	repo, err := syncTransactionRepo(tx, path)
	if err != nil {
		return err
	}
	for i := range repo.Holders {
		holder := &repo.Holders[i]
		if holder.Path != canonicalize(path) {
			continue
		}
		if holder.ForwardRestoreTarget != nil {
			completed, err := observeSyncForwardRestoration(repo, holder)
			if err != nil {
				return err
			}
			if completed {
				return save()
			}
		} else {
			target := SyncTransactionHolderValue{
				Path: holder.Path, Branch: holder.OriginalBranch,
				HEAD: holder.OriginalHEAD, Detached: holder.OriginalDetached,
			}
			if !target.Detached {
				expected := syncTransactionExpectedRefs(repo)
				var ok bool
				target.HEAD, ok = expected["refs/heads/"+target.Branch]
				if !ok {
					return fmt.Errorf("original checkout branch %s lacks recorded ref evidence", target.Branch)
				}
			}
			holder.ForwardRestoreTarget = &target
			if err := save(); err != nil {
				return err
			}
		}
		if err := syncTransactionStep("forward-holder-restore-intent:" + holder.Path); err != nil {
			return err
		}
		if err := restore(); err != nil {
			return err
		}
		if err := syncTransactionStep("forward-holder-restore-applied:" + holder.Path); err != nil {
			return err
		}
		completed, err := observeSyncForwardRestoration(repo, holder)
		if err != nil {
			return err
		}
		if !completed {
			return fmt.Errorf("holder %s did not reach its recorded checkout restoration target", holder.Path)
		}
		return save()
	}
	return fmt.Errorf("checkout %s has no recorded holder", path)
}
