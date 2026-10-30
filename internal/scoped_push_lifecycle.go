package internal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CheckoutMetadataReservation uses the existing workspace-wide mutation lock,
// but describes its real metadata purpose, not a sync transaction.
type CheckoutMetadataReservation struct {
	root           ScopedPushDirectory
	directory      ScopedPushDirectory
	statePath      string
	guard          scopedPushFile
	moves          *checkoutMetadataMoves
	moveSnapshot   scopedPushFile
	movesCommitted bool
}

func checkoutMetadataOwnerPath(stateDir, token string) string {
	if physical, err := filepath.EvalSymlinks(stateDir); err == nil {
		stateDir = physical
	}
	return filepath.Join(stateDir, ".checkout-metadata-"+token)
}

func AcquireCheckoutMetadataReservation(ws Workspace, feature string) (*CheckoutMetadataReservation, error) {
	if ws.Mode != ModeCheckout {
		return nil, errors.New("checkout metadata reservation requires checkout mode")
	}
	if err := validateFeatureName(feature); err != nil {
		return nil, err
	}
	root, err := scopedPushDirectoryAt(ws.MetadataRoot)
	if err != nil {
		return nil, err
	}
	stateDir := filepath.Join(root.Path, "state")
	if info, err := os.Lstat(stateDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("checkout metadata requires a physical state directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	token, err := managedOperationID()
	if err != nil {
		return nil, err
	}
	owner := checkoutMetadataOwnerPath(stateDir, token)
	if _, err := AcquireCheckoutMutationLock(stateDir, token, feature, "metadata", owner); err != nil {
		return nil, err
	}
	directory, err := scopedPushDirectoryAt(stateDir)
	if err != nil || directory.Path != stateDir {
		return nil, errors.Join(err, errors.New("checkout state directory changed during reservation; guard retained"))
	}
	guard, err := scopedPushReadFile(CheckoutMutationLockPath(stateDir))
	if err != nil {
		return nil, err
	}
	reservation := &CheckoutMetadataReservation{root: root, directory: directory, statePath: owner, guard: guard}
	if err := reservation.Verify(); err != nil {
		return nil, errors.Join(err, reservation.Release())
	}
	return reservation, nil
}

func (r *CheckoutMetadataReservation) Verify() error {
	if r == nil || r.guard.info == nil {
		return errors.New("checkout metadata mutation requires a held reservation")
	}
	if err := errors.Join(r.root.verify(), r.directory.verify(),
		r.guard.verify(CheckoutMutationLockPath(r.directory.Path))); err != nil {
		return err
	}
	return verifyCheckoutMutationReservation(r.directory.Path, r.statePath, nil, []string{r.statePath})
}

func (r *CheckoutMetadataReservation) Release() error {
	if r == nil || r.guard.info == nil {
		return nil
	}
	if err := r.finishMoves(); err != nil {
		return err
	}
	path := CheckoutMutationLockPath(r.directory.Path)
	if err := errors.Join(r.root.verify(), r.directory.verify(), r.guard.verify(path)); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	r.guard = scopedPushFile{}
	return syncDir(r.directory.Path)
}

func (r *CheckoutMetadataReservation) RefusePublicationDestination(feature string) error {
	if err := r.Verify(); err != nil {
		return err
	}
	if err := validateFeatureName(feature); err != nil {
		return err
	}
	path := filepath.Join(r.directory.Path, feature+"-push.v1.json")
	if _, err := os.Lstat(path); err == nil {
		return errors.New("destination name has publication evidence; refusing to overwrite or adopt it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Retirement preserves exact terminal bytes/inode in immutable owned history.
// History is durable BEFORE unlinking the reusable live name. A crash may leave
// two links to the same owned inode, never a need to trust a copied receipt.
func (r *CheckoutMetadataReservation) RetirePublicationReceipt(feature, featurePath string) error {
	snapshot, state, err := r.terminalPublicationSnapshot(feature, featurePath)
	if err != nil {
		return err
	}
	if state == nil {
		return syncDir(r.directory.Path)
	}
	path := filepath.Join(r.directory.Path, feature+"-push.v1.json")
	return r.retirePublicationSnapshot(path, snapshot, state)
}

func (r *CheckoutMetadataReservation) ValidatePublicationReceipt(feature, featurePath string) error {
	_, _, err := r.terminalPublicationSnapshot(feature, featurePath)
	return err
}

func (r *CheckoutMetadataReservation) terminalPublicationSnapshot(feature, featurePath string) (scopedPushFile, *ScopedPushState, error) {
	if err := r.Verify(); err != nil {
		return scopedPushFile{}, nil, err
	}
	if err := validateFeatureName(feature); err != nil {
		return scopedPushFile{}, nil, err
	}
	loc := ScopedPushLocation{Mode: ModeCheckout, Feature: feature, FeaturePath: featurePath, MetadataRoot: r.root.Path}
	path := ScopedPushStatePath(loc)
	snapshot, err := scopedPushReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return scopedPushFile{}, nil, nil
	}
	if err != nil {
		return scopedPushFile{}, nil, err
	}
	state, err := scopedPushDecode(snapshot.data)
	if err != nil {
		return scopedPushFile{}, nil, err
	}
	if !state.Terminal() {
		return scopedPushFile{}, nil, errors.New("pending publication cannot be retired by a metadata operation")
	}
	featureDir, err := scopedPushDirectoryAt(featurePath)
	if err != nil {
		return scopedPushFile{}, nil, err
	}
	loc.FeaturePath = featureDir.Path
	journal := scopedPushJournal{path: path, snapshot: snapshot, state: state}
	if err := journal.verifyLocation(loc); err != nil {
		return scopedPushFile{}, nil, fmt.Errorf("refusing to retire foreign publication history: %w", err)
	}
	if snapshot.info.Mode().Perm() != 0o600 {
		return scopedPushFile{}, nil, errors.New("terminal publication receipt permissions changed")
	}
	return snapshot, state, nil
}

func (r *CheckoutMetadataReservation) retirePublicationSnapshot(path string, snapshot scopedPushFile, state *ScopedPushState) error {
	historyPath := filepath.Join(r.directory.Path, "publication-history")
	if err := os.Mkdir(historyPath, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(historyPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(err, errors.New("publication history must be a physical directory"))
	}
	historyDir, err := scopedPushDirectoryAt(historyPath)
	if err != nil {
		return err
	}
	target := filepath.Join(historyPath, state.Plan.Token+".json")
	if err := scopedPushCheckpoint("before-terminal-receipt-link", 0); err != nil {
		return err
	}
	if err := errors.Join(r.Verify(), snapshot.verify(path), historyDir.verify()); err != nil {
		return err
	}
	if err := os.Link(path, target); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	history, err := scopedPushReadFile(target)
	if err != nil {
		return err
	}
	if !os.SameFile(snapshot.info, history.info) || !bytes.Equal(snapshot.data, history.data) {
		return errors.New("publication history destination is foreign; evidence retained")
	}
	if err := scopedPushCheckpoint("after-terminal-receipt-link", 0); err != nil {
		return err
	}
	if err := scopedPushSyncSnapshot(target, history, historyDir, r.root, state.Revision); err != nil {
		return err
	}
	if err := scopedPushCheckpoint("before-terminal-receipt-remove", 0); err != nil {
		return err
	}
	if err := errors.Join(r.Verify(), snapshot.verify(path), history.verify(target), historyDir.verify()); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := scopedPushCheckpoint("after-terminal-receipt-remove", 0); err != nil {
		return err
	}
	return errors.Join(syncDir(r.directory.Path), r.Verify(), history.verify(target))
}

func (r *CheckoutMetadataReservation) EnsureFeatureParent() error {
	if err := r.Verify(); err != nil {
		return err
	}
	path := filepath.Join(r.root.Path, "features")
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(err, errors.New("checkout features must use a physical directory"))
	}
	return syncDir(r.root.Path)
}

func (r *CheckoutMetadataReservation) MoveFeature(from, to string, identity managedObjectIdentity) (bool, error) {
	if err := scopedPushCheckpoint("before-checkout-feature-move", 0); err != nil {
		return false, err
	}
	if err := r.Verify(); err != nil {
		return false, err
	}
	source, err := r.relativeFeaturePath(from)
	if err != nil {
		return false, errors.New("checkout feature source is outside the reserved workspace")
	}
	target, err := r.relativeFeaturePath(to)
	if err != nil {
		return false, errors.New("checkout feature destination is outside the reserved workspace")
	}
	if err := managedVerifyPathIdentity(from, identity); err != nil {
		return false, err
	}
	if err := r.beginMove(source, target, identity); err != nil {
		return false, err
	}
	root, err := os.OpenRoot(r.root.Path)
	if err != nil {
		return false, err
	}
	defer root.Close() //nolint:errcheck
	moveErr := managedRenameNoReplaceWith(root, source, target, func() error {
		return scopedPushCheckpoint("after-checkout-feature-rename-before-sync", len(r.moves.Moves)-1)
	})
	move := r.moves.Moves[len(r.moves.Moves)-1]
	_, moved, locationErr := r.moveLocation(move)
	if moveErr != nil {
		// Visibility is not durability. Repeat the barriers under the same
		// reservation, but preserve the original failure for the caller.
		return moved, errors.Join(moveErr, locationErr, r.syncMoveParents(move))
	}
	if !moved {
		return false, errors.Join(locationErr, errors.New("checkout feature did not remain at its intended destination"))
	}
	return moved, locationErr
}

func (r *CheckoutMetadataReservation) RemoveFeature(path string, identity managedObjectIdentity) error {
	if err := scopedPushCheckpoint("before-checkout-feature-remove", 0); err != nil {
		return err
	}
	if err := errors.Join(r.Verify(), managedVerifyPathIdentity(path, identity)); err != nil {
		return err
	}
	relative, err := r.relativeFeaturePath(path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(r.root.Path)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	if err := root.RemoveAll(relative); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (r *CheckoutMetadataReservation) relativeFeaturePath(path string) (string, error) {
	for _, root := range []string{r.root.Path, r.root.Spelling} {
		relative, err := filepath.Rel(root, path)
		if err == nil && filepath.IsLocal(relative) && relative != "." {
			return relative, nil
		}
	}
	return "", errors.New("path is not within the reserved checkout workspace")
}
