package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type checkoutMetadataMove struct {
	Source            string                `json:"source"`
	Destination       string                `json:"destination"`
	Identity          managedObjectIdentity `json:"identity"`
	SourceParent      ScopedPushDirectory   `json:"source_parent"`
	DestinationParent ScopedPushDirectory   `json:"destination_parent"`
}

// This is directory-movement evidence, not a sync/reparent transaction. It
// keeps exact inode/parent bindings and the registry preimage if compensation
// cannot restore a coherent namespace while the metadata reservation is held.
type checkoutMetadataMoves struct {
	Kind            string                 `json:"kind"`
	Version         int                    `json:"version"`
	Token           string                 `json:"token"`
	Root            ScopedPushDirectory    `json:"root"`
	Moves           []checkoutMetadataMove `json:"moves"`
	RegistryPresent bool                   `json:"registry_present"`
	RegistryBefore  []byte                 `json:"registry_before,omitempty"`
	Completed       bool                   `json:"completed"`
}

func checkoutMetadataMovePath(path string) bool {
	return strings.HasPrefix(filepath.Base(path), ".checkout-metadata-")
}

func loadCheckoutMetadataMoves(path string) (*checkoutMetadataMoves, error) {
	snapshot, err := scopedPushReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var moves checkoutMetadataMoves
	if err := scopedPushJSONKeys(json.NewDecoder(bytes.NewReader(snapshot.data))); err != nil {
		return nil, err
	}
	if err := featureRenameReadJSON(snapshot.data, &moves); err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if err := validateCheckoutMetadataMoves(&moves, filepath.Join(parent, filepath.Base(path))); err != nil {
		return nil, err
	}
	return &moves, nil
}

func validateCheckoutMetadataMoves(moves *checkoutMetadataMoves, path string) error {
	if moves.Kind != "tws-checkout-metadata-moves" || moves.Version != 1 ||
		!markerIDRegexp.MatchString(moves.Token) || !moves.Root.valid() ||
		filepath.Join(moves.Root.Path, "state", ".checkout-metadata-"+moves.Token) != path ||
		len(moves.Moves) == 0 || (!moves.RegistryPresent && len(moves.RegistryBefore) != 0) {
		return errors.New("invalid checkout metadata movement evidence")
	}
	for _, move := range moves.Moves {
		if !checkoutMetadataFeatureRelative(move.Source) || !checkoutMetadataFeatureRelative(move.Destination) ||
			move.Source == move.Destination || move.Identity.Inode == 0 ||
			!move.SourceParent.valid() || !move.DestinationParent.valid() ||
			move.SourceParent.Path != filepath.Join(moves.Root.Path, filepath.Dir(move.Source)) ||
			move.DestinationParent.Path != filepath.Join(moves.Root.Path, filepath.Dir(move.Destination)) {
			return errors.New("invalid checkout metadata movement binding")
		}
	}
	return nil
}

func checkoutMetadataFeatureRelative(path string) bool {
	parts := strings.Split(path, string(filepath.Separator))
	return (len(parts) == 1 && validateFeatureName(parts[0]) == nil) ||
		(len(parts) == 2 && parts[0] == "features" && validateFeatureName(parts[1]) == nil)
}

func (r *CheckoutMetadataReservation) beginMove(source, target string, identity managedObjectIdentity) error {
	fromParent, err := scopedPushDirectoryAt(filepath.Join(r.root.Path, filepath.Dir(source)))
	if err != nil {
		return err
	}
	toParent, err := scopedPushDirectoryAt(filepath.Join(r.root.Path, filepath.Dir(target)))
	if err != nil {
		return err
	}
	if source == target || !checkoutMetadataFeatureRelative(source) || !checkoutMetadataFeatureRelative(target) ||
		fromParent.Path != filepath.Join(r.root.Path, filepath.Dir(source)) ||
		toParent.Path != filepath.Join(r.root.Path, filepath.Dir(target)) {
		return errors.New("unsupported checkout metadata movement path")
	}
	if r.moves == nil {
		r.moves = &checkoutMetadataMoves{
			Kind: "tws-checkout-metadata-moves", Version: 1, Root: r.root,
			Token: strings.TrimPrefix(filepath.Base(r.statePath), ".checkout-metadata-"),
		}
		registry, err := scopedPushReadFile(SpacesFilePath(r.root.Path))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		r.moves.RegistryPresent, r.moves.RegistryBefore = registry.info != nil, registry.data
	}
	r.moves.Moves = append(r.moves.Moves, checkoutMetadataMove{
		Source: source, Destination: target, Identity: identity,
		SourceParent: fromParent, DestinationParent: toParent,
	})
	return r.saveMoves()
}

func (r *CheckoutMetadataReservation) saveMoves() error {
	if err := validateCheckoutMetadataMoves(r.moves, r.statePath); err != nil {
		return err
	}
	if err := errors.Join(r.Verify(), r.moveSnapshot.verify(r.statePath)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r.moves, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > 16<<20 {
		return errors.New("checkout metadata movement evidence is too large")
	}
	file, err := os.CreateTemp(r.directory.Path, ".metadata-move-")
	if err != nil {
		return err
	}
	name := file.Name()
	info, err := file.Stat()
	if err != nil {
		return errors.Join(err, file.Close())
	}
	defer func() {
		if current, err := os.Lstat(name); err == nil && os.SameFile(info, current) {
			_ = os.Remove(name)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := errors.Join(r.Verify(), r.moveSnapshot.verify(r.statePath)); err != nil {
		return err
	}
	if r.moveSnapshot.info == nil {
		err = os.Link(name, r.statePath)
	} else {
		err = os.Rename(name, r.statePath)
	}
	if err != nil {
		return err
	}
	if err := scopedPushCheckpoint("after-checkout-metadata-evidence-write", len(r.moves.Moves)-1); err != nil {
		return err
	}
	snapshot, err := scopedPushReadFile(r.statePath)
	if err != nil || !bytes.Equal(snapshot.data, data) {
		return errors.Join(err, errors.New("checkout metadata movement evidence changed"))
	}
	r.moveSnapshot = snapshot
	return scopedPushSyncSnapshot(r.statePath, r.moveSnapshot, r.directory, r.root, 0)
}

func (r *CheckoutMetadataReservation) moveLocation(move checkoutMetadataMove) (bool, bool, error) {
	if err := errors.Join(r.Verify(), move.SourceParent.verify(), move.DestinationParent.verify()); err != nil {
		return false, false, err
	}
	at := func(path string) (bool, error) {
		info, err := os.Lstat(filepath.Join(r.root.Path, path))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		got, err := managedIdentityFromInfo(info)
		return info.IsDir() && info.Mode()&os.ModeSymlink == 0 && got == move.Identity, err
	}
	source, sourceErr := at(move.Source)
	target, targetErr := at(move.Destination)
	if sourceErr != nil || targetErr != nil || source == target {
		return source, target, errors.Join(sourceErr, targetErr, errors.New("checkout feature inode location is ambiguous; movement evidence retained"))
	}
	return source, target, nil
}

func (r *CheckoutMetadataReservation) syncMoveParents(move checkoutMetadataMove) error {
	if err := errors.Join(r.Verify(), move.SourceParent.verify(), move.DestinationParent.verify()); err != nil {
		return err
	}
	for _, parent := range []ScopedPushDirectory{move.SourceParent, move.DestinationParent} {
		if err := syncDir(parent.Path); err != nil {
			return err
		}
		if err := parent.verify(); err != nil {
			return err
		}
	}
	return nil
}

// CommitMoves is called only after the corresponding registry/layout effect
// has completed. Release otherwise requires exact restoration to every first
// source; unresolved effects retain both real evidence and the reservation.
func (r *CheckoutMetadataReservation) CommitMoves() error {
	r.movesCommitted = true
	return r.finishMoves()
}

func (r *CheckoutMetadataReservation) finishMoves() error {
	if r.moves == nil || len(r.moves.Moves) == 0 {
		return nil
	}
	if r.moveSnapshot.info == nil {
		if r.moves.Completed {
			return syncDir(r.directory.Path)
		}
		// Without an observed intent, MoveFeature never reached its syscall;
		// an unobserved publication still requires preserving the guard.
		return r.moveSnapshot.verify(r.statePath)
	}
	expected := map[managedObjectIdentity]string{}
	for _, move := range r.moves.Moves {
		if r.movesCommitted {
			expected[move.Identity] = move.Destination
		} else if _, exists := expected[move.Identity]; !exists {
			expected[move.Identity] = move.Source
		}
		if err := r.syncMoveParents(move); err != nil {
			return fmt.Errorf("checkout metadata movement durability is unresolved at %s: %w", r.statePath, err)
		}
	}
	for identity, path := range expected {
		if err := managedVerifyPathIdentity(filepath.Join(r.root.Path, path), identity); err != nil {
			return fmt.Errorf("checkout metadata movement is unresolved; preserve and reconcile %s before further mutation: %w", r.statePath, err)
		}
	}
	// spaces.yaml may have been replaced in the workspace root rather than
	// either feature parent. Its directory barrier must precede completion.
	if err := errors.Join(syncDir(r.root.Path), r.root.verify()); err != nil {
		return fmt.Errorf("checkout registry durability is unresolved at %s: %w", r.statePath, err)
	}
	r.moves.Completed = true
	if err := r.saveMoves(); err != nil {
		return err
	}
	// A completed receipt is harmless if interrupted cleanup leaves it behind.
	if err := r.moveSnapshot.verify(r.statePath); err != nil {
		return err
	}
	if err := os.Remove(r.statePath); err != nil {
		return err
	}
	r.moveSnapshot = scopedPushFile{}
	return syncDir(r.directory.Path)
}
