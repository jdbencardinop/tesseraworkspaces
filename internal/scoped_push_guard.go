package internal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func externalScopedPushLocation(featurePath string) ScopedPushLocation {
	return ScopedPushLocation{
		Mode: ModeExternal, Feature: filepath.Base(featurePath), FeaturePath: featurePath,
		MetadataRoot: filepath.Dir(featurePath),
	}
}

func acquireScopedPushGuard(loc ScopedPushLocation, token string, recovery bool) (func() error, func() error, error) {
	if loc.Mode == ModeCheckout {
		stateDir := filepath.Join(loc.MetadataRoot, "state")
		acquire := AcquireCheckoutMutationLock
		if recovery {
			acquire = ReclaimCheckoutMutationLock
		}
		if _, err := acquire(stateDir, token, loc.Feature, "publication", ScopedPushStatePath(loc)); err != nil {
			return nil, nil, err
		}
		return scopedPushOwnedGuard(CheckoutMutationLockPath(stateDir))
	}
	path := SyncRunGuardPath(loc.FeaturePath)
	if recovery {
		snapshot, err := scopedPushReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		if snapshot.info != nil {
			var guard SyncRunGuard
			if err := yaml.Unmarshal(snapshot.data, &guard); err != nil {
				return nil, nil, err
			}
			if guard.Token != token || guard.PID <= 0 ||
				(guard.PID != os.Getpid() && syncProcessAlive(guard.PID)) {
				return nil, nil, errors.New("publication guard is foreign or held by a live process")
			}
			if err := snapshot.verify(path); err != nil {
				return nil, nil, err
			}
			if err := os.Remove(path); err != nil {
				return nil, nil, err
			}
		}
		if err := writeSyncGuardExclusive(path, token); err != nil {
			return nil, nil, err
		}
	} else if err := ClaimSyncRunGuard(loc.FeaturePath, token); err != nil {
		return nil, nil, err
	}
	return scopedPushOwnedGuard(path)
}

func scopedPushOwnedGuard(path string) (func() error, func() error, error) {
	snapshot, err := scopedPushReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	verify := func() error { return snapshot.verify(path) }
	release := func() error {
		if err := scopedPushCheckpoint("before-release-guard", 0); err != nil {
			return err
		}
		if err := verify(); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := scopedPushCheckpoint("after-release-guard", 0); err != nil {
			return err
		}
		return syncDir(filepath.Dir(path))
	}
	return verify, release, nil
}

// RefuseCheckoutPublication is a local-only admission check for legacy
// checkout metadata mutators that do not construct a sync transaction.
func RefuseCheckoutPublication(ws Workspace) error {
	if ws.Mode != ModeCheckout {
		return nil
	}
	entries, err := os.ReadDir(ws.CheckoutStateDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".json" || len(name) < len("-push.v1.json") ||
			name[len(name)-len("-push.v1.json"):] != "-push.v1.json" {
			continue
		}
		state, err := loadScopedPushStatePath(filepath.Join(ws.CheckoutStateDir(), name))
		if err != nil {
			return fmt.Errorf("untrusted checkout publication state: %w", err)
		}
		if state != nil && !state.Terminal() {
			return fmt.Errorf("checkout publication is pending for %s; use tws push %s --continue or --cancel", state.Plan.Feature, state.Plan.Feature)
		}
	}
	return nil
}
