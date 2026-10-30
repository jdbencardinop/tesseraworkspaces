package internal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FeatureRenameCheckpoint is a test-only seam around real, durably intended
// effects. It is never configured through the environment or CLI.
var FeatureRenameCheckpoint func(step, path string) error

// ErrFeatureRenameInterrupted lets tests model process loss without running
// the ordinary-error rollback path. Real process loss leaves the same journal.
var ErrFeatureRenameInterrupted = errors.New("feature rename interrupted")

type FeatureRenameResult struct {
	Path        string
	Journal     string
	Aborted     bool
	CleanupOnly bool
}

// RenameExternalFeature changes no refs, index, working files, or templates.
// The name admission must precede lifecycle/feature mutation/registry locks.
// Recovery reads the bound journal before looking for the old feature path.
func RenameExternalFeature(
	ws Workspace, oldName, newName, recovery string,
	checkSessions func(feature, path string, names []string) error,
	selectedRootSpellings ...string,
) (result FeatureRenameResult, retErr error) {
	if ws.Mode != ModeExternal {
		return result, errors.New("feature rename recovery is external-only")
	}
	if ws.MetadataRoot == "" || ws.RepoRoot == "" {
		return result, errors.New("feature rename requires an explicit workspace and controlling repository")
	}
	rootSpelling := ws.MetadataRoot
	rootBinding, rootAlias, err := resolveFeatureRenameRoot(rootSpelling)
	if err != nil {
		return result, err
	}
	if len(selectedRootSpellings) > 1 {
		return result, errors.New("feature rename requires one execution-selected root")
	}
	if len(selectedRootSpellings) == 1 {
		selected, alias, err := resolveFeatureRenameRoot(selectedRootSpellings[0])
		if err != nil || selected != rootBinding {
			return result, errors.Join(err, errors.New("execution-selected root spelling no longer binds the resolved workspace"))
		}
		rootAlias = alias
	}
	repoBinding, repoAlias, err := resolveFeatureRenameRoot(ws.RepoRoot)
	if err != nil {
		return result, err
	}
	ws.MetadataRoot, ws.RepoRoot = rootBinding.Path, repoBinding.Path
	for _, name := range []string{oldName, newName} {
		if strings.ContainsRune(name, 0) {
			return result, errors.New("feature rename names cannot contain NUL")
		}
		if _, err := ws.ResolveFeaturePath(name); err != nil {
			return result, err
		}
	}
	if oldName == newName {
		return result, errors.New("feature rename requires distinct, non-aliasing names")
	}
	if recovery != "" && recovery != "continue" && recovery != "abort" {
		return result, errors.New("unknown feature rename recovery action")
	}
	if recovery != "" {
		plans, err := featureRenamePendingPlans(ws.MetadataRoot)
		if err != nil {
			return result, err
		}
		matching := false
		for _, plan := range plans {
			if plan.OldName == oldName && plan.NewName == newName {
				matching = true
			}
		}
		if !matching {
			return result, fmt.Errorf("no pending feature rename journal matches %q -> %q in %s (already cleaned or never started)", oldName, newName, ws.MetadataRoot)
		}
	} else {
		for _, name := range []string{oldName, newName} {
			if err := GuardFeatureName(ws.MetadataRoot, name); err != nil {
				return result, err
			}
		}
	}
	admission, err := acquireFeatureRenameAdmissionAt(rootBinding, rootAlias, []string{oldName, newName}, true, true, recovery != "")
	if err != nil {
		return result, err
	}
	defer func() { retErr = errors.Join(retErr, admission.Release()) }()
	if err := featureRenameCheckpoint("after-admission", ws.MetadataRoot); err != nil {
		return result, err
	}
	if err := admission.verify(); err != nil {
		return result, err
	}
	if admission.root != rootBinding {
		return result, errors.New("execution-selected workspace root changed during admission")
	}
	if err := repoAlias.verify(); err != nil {
		return result, err
	}
	pending, err := featureRenamePendingPlans(ws.MetadataRoot)
	if err != nil {
		return result, err
	}
	var tx *featureRenameOperation
	for _, plan := range pending {
		if recovery != "" && plan.OldName == oldName && plan.NewName == newName {
			if tx != nil {
				return result, errors.New("multiple matching feature rename journals; refusing ambiguous recovery")
			}
			tx, err = loadFeatureRename(ws.MetadataRoot, plan.ID)
			if err != nil {
				return result, err
			}
			continue
		}
		if featureRenameNamesAlias(ws.MetadataRoot, plan.OldName, oldName) || featureRenameNamesAlias(ws.MetadataRoot, plan.NewName, oldName) ||
			featureRenameNamesAlias(ws.MetadataRoot, plan.OldName, newName) || featureRenameNamesAlias(ws.MetadataRoot, plan.NewName, newName) ||
			plan.registryChanged() {
			return result, featureRenamePendingError(plan)
		}
	}
	if recovery != "" && tx == nil {
		return result, fmt.Errorf("no pending feature rename journal matches %q -> %q in %s (already cleaned or never started)", oldName, newName, ws.MetadataRoot)
	}
	currentPath := filepath.Join(ws.MetadataRoot, oldName)
	if tx != nil {
		result.Journal = tx.plan.Operation.Path
		if tx.plan.Repository.Path != ws.RepoRoot {
			return result, errors.New("feature rename journal belongs to a different controlling repository")
		}
		if err := tx.plan.Repository.verify(); err != nil {
			return result, err
		}
		currentPath, _, err = tx.location()
		if err != nil {
			return result, err
		}
	}
	lifecycle, err := AcquireManagedFeatureLifecycleLock(currentPath)
	if err != nil {
		return result, err
	}
	defer func() { retErr = errors.Join(retErr, lifecycle.Release()) }()
	featureIdentity, err := featureRenameDirectoryAt(currentPath)
	if err != nil || featureIdentity.Identity != lifecycle.Identity() {
		return result, errors.Join(err, errors.New("feature lifecycle binding changed"))
	}
	lockedInfo, err := lifecycle.dir.Stat()
	if err != nil {
		return result, err
	}
	lockedIdentity, err := managedIdentityFromInfo(lockedInfo)
	if err != nil || lockedIdentity != featureIdentity.Identity {
		return result, errors.Join(err, errors.New("held lifecycle lock does not bind the selected feature inode"))
	}
	if err := featureRenameMutationPreflight(currentPath); err != nil {
		return result, err
	}
	token, err := managedOperationID()
	if err != nil {
		return result, err
	}
	if tx != nil {
		if err := featureRenameRecoveryGuard(currentPath, tx.plan.GuardToken); err != nil {
			return result, err
		}
		token = tx.plan.GuardToken
	}
	if err := ClaimSyncRunGuard(currentPath, token); err != nil {
		return result, fmt.Errorf("claim feature rename mutation reservation: %w", err)
	}
	defer func() {
		// A move or fsync error may have occurred after the directory changed
		// location. Release only at the captured inode's actual public binding.
		path := currentPath
		if tx != nil {
			if actual, locationErr := tx.reservationLocation(); locationErr == nil {
				path = actual
			} else {
				retErr = errors.Join(retErr, fmt.Errorf("mutation reservation retained; feature location is uncertain: %w", locationErr))
				return
			}
		}
		if err := managedVerifyPathIdentity(path, featureIdentity.Identity); err != nil {
			retErr = errors.Join(retErr, err)
			return
		}
		retErr = errors.Join(retErr, ReleaseOwnedSyncRunGuard(path, token))
	}()
	if err := featureRenameMutationPreflight(currentPath); err != nil {
		return result, err
	}
	var registry *SpacesRenameTx
	if tx == nil {
		// Capture before and after the semantic transaction, including identity,
		// so an uncooperative edit during its read cannot change our authority.
		before, err := featureRenameReadFile(SpacesFilePath(ws.MetadataRoot), true)
		if err != nil {
			return result, err
		}
		registry, err = BeginSpacesFeatureRename(ws.MetadataRoot, oldName, newName, currentPath, filepath.Join(ws.MetadataRoot, newName))
		if err != nil {
			return result, err
		}
		defer func() { retErr = errors.Join(retErr, registry.Release()) }()
		if registry.changed && len(pending) > 0 {
			return result, fmt.Errorf("shared registry publication is reserved: %w", featureRenamePendingError(pending[0]))
		}
		if err := before.verifyAt(before.Path); err != nil {
			return result, err
		}
		if _, err := os.Lstat(filepath.Join(ws.MetadataRoot, newName)); !errors.Is(err, fs.ErrNotExist) {
			return result, errors.Join(err, fmt.Errorf("feature destination already exists: %s", newName))
		}
		if present, err := ManagedTemplateStatePresent(currentPath); err != nil {
			return result, err
		} else if present {
			state, err := LoadManagedTemplateState(currentPath)
			if err != nil {
				return result, err
			}
			if err := validateManagedTemplateOwnerForWorkspace(state, ws); err != nil {
				return result, err
			}
		}
		plan, err := buildFeatureRenamePlan(ws, oldName, newName, token, registry, rootAlias, repoAlias)
		if err != nil {
			return result, err
		}
		if err := before.verifyAt(before.Path); err != nil {
			return result, err
		}
		if err := admission.verify(); err != nil {
			return result, err
		}
		if checkSessions != nil {
			if err := checkSessions(oldName, currentPath, plan.names()); err != nil {
				return result, err
			}
		}
		tx, err = newFeatureRenameOperation(plan)
		if err != nil {
			return result, err
		}
		result.Journal = tx.plan.Operation.Path
	} else if tx.plan.Registry.Observation.Exists {
		lock, err := acquireSpacesLock(ws.MetadataRoot)
		if err != nil {
			return result, err
		}
		defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	}
	tx.admission = admission
	tx.checkSession = checkSessions
	if err := tx.syncJournalEvidence(); err != nil {
		return result, tx.failure(err)
	}
	if err := tx.verifyStable(); err != nil {
		return result, tx.failure(err)
	}
	if err := tx.verifyPhysicalInventory(); err != nil {
		return result, tx.failure(err)
	}
	if tx.has("complete", "done") {
		if recovery == "abort" {
			return result, fmt.Errorf("feature rename is durably complete at %s; --abort cannot undo it; use --continue for verified cleanup", result.Journal)
		}
		result.CleanupOnly = true
		err = tx.cleanup(false)
	} else if recovery == "abort" || tx.has("abort", "intent") || tx.has("aborted", "done") {
		err = tx.rollback()
		result.Aborted = true
	} else {
		err = tx.forward()
	}
	if err != nil {
		if recovery == "" && !errors.Is(err, ErrFeatureRenameInterrupted) &&
			!tx.has("complete", "done") && !tx.has("abort", "intent") {
			rollbackErr := tx.rollback()
			if rollbackErr == nil {
				return result, fmt.Errorf("feature rename failed and was rolled back to %s; operation receipt %s: %w",
					tx.plan.Feature.Path, result.Journal, err)
			}
			err = errors.Join(err, fmt.Errorf("automatic rollback could not be proven safe: %w", rollbackErr))
		}
		return result, tx.failure(err)
	}
	result.Path, _, err = tx.location()
	return result, err
}

func featureRenameMutationPreflight(path string) error {
	if info, err := os.Lstat(SyncRunGuardPath(path)); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("feature mutation reservation is not a regular file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, state := range []string{SyncStatePath(path), SyncRunStatePath(path)} {
		if _, err := os.Lstat(state); err == nil {
			return fmt.Errorf("sync recovery evidence at %s blocks feature rename", state)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if HasReparentState(ReparentLocation{Mode: ModeExternal, Feature: filepath.Base(path), FeaturePath: path}) {
		return fmt.Errorf("stack reparent recovery is active at %s", path)
	}
	if residue, err := ManagedTemplateRecoveryResidue(path); err != nil {
		return err
	} else if residue != "" {
		return fmt.Errorf("managed template recovery is pending at %s", residue)
	}
	return nil
}

func featureRenameRecoveryGuard(path, token string) error {
	info, err := os.Lstat(SyncRunGuardPath(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("feature rename mutation reservation is not a regular file")
	}
	guard, err := ReadSyncRunGuard(path)
	if err != nil {
		return err
	}
	if guard.Token != token {
		return errors.New("feature rename refuses a mutation reservation owned by another operation")
	}
	if syncProcessAlive(guard.PID) {
		return fmt.Errorf("feature rename mutation reservation is still live (pid %d)", guard.PID)
	}
	return nil
}

func (plan *featureRenamePlan) names() []string {
	names := make([]string, 0, len(plan.Worktrees))
	for _, tree := range plan.Worktrees {
		names = append(names, tree.Name)
	}
	if len(names) == 0 {
		return []string{"<feature>"}
	}
	return names
}

func (plan *featureRenamePlan) registryChanged() bool {
	return len(plan.Pointers) > 0 && plan.Pointers[len(plan.Pointers)-1].Kind == "registry"
}

func (tx *featureRenameOperation) location() (string, bool, error) {
	if err := tx.plan.Root.verify(); err != nil {
		return "", false, err
	}
	oldPath := tx.plan.Feature.Path
	newPath := filepath.Join(tx.plan.Root.Path, tx.plan.NewName)
	var current string
	moved := false
	for i, path := range []string{oldPath, newPath} {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		id, err := managedIdentityFromInfo(info)
		if err != nil {
			return "", false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			id != tx.plan.Feature.Identity || current != "" {
			return "", false, fmt.Errorf("feature rename path was replaced, reoccupied, or aliased at %s; neither occupant will be overwritten", path)
		}
		current, moved = path, i == 1
	}
	if current == "" {
		return "", false, errors.New("neither rename path contains the original feature directory identity")
	}
	return current, moved, nil
}

func (tx *featureRenameOperation) reservationLocation() (string, error) {
	if err := tx.plan.Root.verify(); err != nil {
		return "", err
	}
	current := ""
	for _, path := range []string{tx.plan.Feature.Path, filepath.Join(tx.plan.Root.Path, tx.plan.NewName)} {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		id, err := managedIdentityFromInfo(info)
		if err != nil {
			return "", err
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 && id == tx.plan.Feature.Identity {
			if current != "" {
				return "", errors.New("feature mutation reservation has ambiguous directory bindings")
			}
			current = path
		}
	}
	if current == "" {
		return "", errors.New("feature mutation reservation no longer has a verified public binding")
	}
	return current, nil
}

func (tx *featureRenameOperation) verifyStable() error {
	if err := tx.verifyJournal(); err != nil {
		return err
	}
	for i := range tx.plan.Aliases {
		if err := tx.plan.Aliases[i].verify(); err != nil {
			return err
		}
	}
	path, moved, err := tx.location()
	if err != nil {
		return err
	}
	if err := tx.plan.verifyParents(moved); err != nil {
		return err
	}
	for _, directory := range []featureRenameDirectory{tx.plan.Repository, tx.plan.Common} {
		if err := directory.verify(); err != nil {
			return err
		}
	}
	common, err := tx.plan.common(tx.plan.Repository.Path, false)
	if err != nil || common != tx.plan.Common {
		return errors.Join(err, errors.New("controlling repository common directory changed"))
	}
	if err := featureRenameMutationPreflight(path); err != nil {
		return err
	}
	guard, err := ReadSyncRunGuard(path)
	if err != nil || guard.Token != tx.plan.GuardToken || guard.PID != os.Getpid() {
		return errors.Join(err, errors.New("feature rename mutation reservation changed"))
	}
	for _, file := range tx.plan.Metadata {
		if err := file.verifyAt(filepath.Join(path, filepath.Base(file.Path))); err != nil {
			return err
		}
	}
	if err := tx.verifyOwned(path); err != nil {
		return err
	}
	if !tx.plan.registryChanged() {
		if err := tx.plan.Registry.verifyAt(tx.plan.Registry.Path); err != nil {
			return err
		}
	}
	for i, pointer := range tx.plan.Pointers {
		active, _, _, _, _, err := tx.pointerPaths(i)
		if err != nil {
			return err
		}
		stage := tx.stage(i)
		switch {
		case stage == nil:
			if err := pointer.Before.verifyAt(active); err != nil {
				return err
			}
		case tx.has(fmt.Sprintf("cleanup.%d", i), "intent"):
			expected := pointer.Before.Observation
			if tx.has("complete", "done") {
				expected = *stage.File
			}
			if err := (featureRenameFile{Observation: expected}).verifyAt(active); err != nil {
				return err
			}
		default:
			if _, err := tx.pointerState(i); err != nil {
				return err
			}
		}
	}
	for _, tree := range tx.plan.Worktrees {
		treePath := filepath.Join(path, "worktrees", tree.Name)
		directory := tree.Directory
		directory.Path = treePath
		for _, dir := range []featureRenameDirectory{directory, tree.Repository, tree.Admin, tree.Common} {
			if err := dir.verify(); err != nil {
				return err
			}
		}
		entryCommon, err := tx.plan.common(tree.Repository.Path, false)
		if err != nil || entryCommon != tree.Common {
			return errors.Join(err, fmt.Errorf("selected repository binding changed for %s", tree.Name))
		}
		for _, file := range []featureRenameFile{tree.CommonFile, tree.Head, tree.Index} {
			if err := file.verifyAt(file.Path); err != nil {
				return err
			}
		}
		rewritten := false
		for _, pointer := range tx.plan.Pointers {
			if pointer.Kind == "forward" && pointer.Before.Path == tree.Marker.Path {
				rewritten = true
			}
		}
		if !rewritten {
			if err := tree.Marker.verifyAt(filepath.Join(treePath, ".git")); err != nil {
				return err
			}
		}
		if err := featureRenameIdleGit(tree.Admin.Path); err != nil {
			return err
		}
		if err := featureRenameNoAlternates(tree.Common.Path); err != nil {
			return err
		}
		config, err := featureRenameConfig(tree.Admin.Path)
		if err != nil || digestBytes(config) != tree.ConfigHash {
			return errors.Join(err, fmt.Errorf("Git configuration changed for %s", tree.Name))
		}
		oid, err := featureRenameGit(tree.Admin.Path, "rev-parse", "--verify", "HEAD")
		if err != nil || strings.TrimSuffix(string(oid), "\n") != tree.OID {
			return errors.Join(err, fmt.Errorf("HEAD/ref identity changed for %s", tree.Name))
		}
	}
	if tx.checkSession != nil {
		for _, name := range []string{tx.plan.OldName, tx.plan.NewName} {
			if err := tx.checkSession(name, path, tx.plan.names()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (tx *featureRenameOperation) verifyOwned(path string) error {
	root, err := featureRenameOpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	for _, expected := range tx.plan.Owned {
		file, err := observeManagedFileRoot(root, expected.Relative)
		if err != nil {
			return err
		}
		link, err := observeManagedLinkRoot(root, expected.Relative)
		if err != nil {
			return err
		}
		if file != expected.File || link != expected.Link {
			return fmt.Errorf("managed ownership/effective content changed at %s", filepath.Join(path, expected.Relative))
		}
	}
	return nil
}

func (tx *featureRenameOperation) failure(cause error) error {
	var completed, pending []string
	for _, event := range tx.events {
		if event.Phase == "done" {
			completed = append(completed, event.Effect)
		} else if !tx.has(event.Effect, "done") {
			pending = append(pending, event.Effect)
		}
	}
	outcome := "feature rename incomplete"
	recovery := "matching --continue or --abort"
	if tx.has("complete", "done") {
		outcome, recovery = "feature rename completed; verified cleanup pending", "matching --continue (abort is no longer allowed)"
	} else if tx.has("aborted", "done") {
		outcome = "feature rename rolled back; verified cleanup pending"
	}
	var private []string
	for _, pointer := range tx.plan.Pointers {
		if _, err := os.Lstat(pointer.StageRoot); err == nil {
			private = append(private, pointer.StageRoot)
		}
	}
	return fmt.Errorf("%s: %w; old=%s new=%s; completed=%q pending=%q; private paths=%q; exact evidence=%s; use %s (never an older tws)",
		outcome,
		cause, tx.plan.Feature.Path, filepath.Join(tx.plan.Root.Path, tx.plan.NewName),
		completed, pending, private, tx.plan.Operation.Path, recovery)
}

func featureRenameCheckpoint(step, path string) error {
	if FeatureRenameCheckpoint != nil {
		return FeatureRenameCheckpoint(step, path)
	}
	return nil
}

func (tx *featureRenameOperation) effect(effect, path string, action func() error) error {
	if tx.has(effect, "done") {
		return action()
	}
	if err := tx.record(effect, "intent", nil); err != nil {
		return err
	}
	if err := featureRenameCheckpoint("before-"+effect, path); err != nil {
		return err
	}
	if err := tx.verifyStable(); err != nil {
		return err
	}
	if err := action(); err != nil {
		return err
	}
	if effect == "complete" || effect == "aborted" {
		// Here the durable record IS the effect and commit point, rather
		// than a progress acknowledgement of an earlier filesystem effect.
		if err := tx.record(effect, "done", nil); err != nil {
			return err
		}
		return featureRenameCheckpoint("after-"+effect, path)
	}
	if err := featureRenameCheckpoint("after-"+effect, path); err != nil {
		return err
	}
	return tx.record(effect, "done", nil)
}

func (tx *featureRenameOperation) forward() error {
	for i := range tx.plan.Pointers {
		if err := tx.preparePointer(i); err != nil {
			return err
		}
	}
	if err := tx.effect("move", tx.plan.Feature.Path, func() error { return tx.move(false) }); err != nil {
		return err
	}
	for i, pointer := range tx.plan.Pointers {
		if pointer.Kind == "registry" {
			if err := tx.verifyFinalGit(true); err != nil {
				return err
			}
			if !tx.has(fmt.Sprintf("preserve.%d", i), "intent") && SpacesSaveHook != nil {
				if err := SpacesSaveHook(tx.plan.Root.Path); err != nil {
					return err
				}
			}
		}
		if err := tx.effect(fmt.Sprintf("preserve.%d", i), pointer.Before.Path, func() error {
			return tx.preservePointer(i)
		}); err != nil {
			return err
		}
		if err := tx.effect(fmt.Sprintf("publish.%d", i), pointer.Before.Path, func() error {
			return tx.publishPointer(i)
		}); err != nil {
			return err
		}
	}
	if err := tx.verifyFinal(true); err != nil {
		return err
	}
	if err := tx.effect("complete", tx.plan.Operation.Path, func() error { return tx.verifyFinal(true) }); err != nil {
		return err
	}
	return tx.cleanup(false)
}

func (tx *featureRenameOperation) move(back bool) error {
	if err := tx.verifyStable(); err != nil {
		return err
	}
	if err := tx.verifyPhysicalInventory(); err != nil {
		return err
	}
	_, moved, err := tx.location()
	if err != nil {
		return err
	}
	if moved != back {
		return featureRenameSyncDirectory(tx.plan.Root)
	}
	if back && tx.plan.registryChanged() {
		if err := tx.plan.Registry.verifyAt(tx.plan.Registry.Path); err != nil {
			return fmt.Errorf("registry must be restored before moving the feature back: %w", err)
		}
	}
	from, to := tx.plan.OldName, tx.plan.NewName
	if back {
		from, to = to, from
	}
	if err := featureRenameCheckpoint("before-move-publication", filepath.Join(tx.plan.Root.Path, to)); err != nil {
		return err
	}
	if err := tx.plan.verifyParents(moved); err != nil {
		return err
	}
	return featureRenameRenameNoReplace(tx.plan.Root, from, to)
}

func (tx *featureRenameOperation) pointerPaths(index int) (active, original, next, published string, root featureRenameDirectory, err error) {
	pointer := tx.plan.Pointers[index]
	root = tx.plan.Root
	active = pointer.Before.Path
	switch pointer.Kind {
	case "backpointer":
		root = tx.plan.Worktrees[pointer.Worktree].Admin
	case "forward":
		path, _, locationErr := tx.location()
		if locationErr != nil {
			err = locationErr
			return
		}
		active = filepath.Join(path, "worktrees", tx.plan.Worktrees[pointer.Worktree].Name, ".git")
	}
	original = filepath.Join(pointer.StageRoot, "original")
	next = filepath.Join(pointer.StageRoot, "next")
	published = filepath.Join(pointer.StageRoot, "published")
	return
}

func (tx *featureRenameOperation) preparePointer(index int) error {
	pointer := tx.plan.Pointers[index]
	if stage := tx.stage(index); stage != nil {
		if err := stage.Directory.verify(); err != nil {
			return err
		}
		if err := (featureRenameFile{Observation: *stage.File}).verifyAt(filepath.Join(pointer.StageRoot, "next")); err != nil {
			return err
		}
		return featureRenameSyncDirectory(*stage.Directory)
	}
	effect := fmt.Sprintf("stage.%d", index)
	if err := tx.record(effect, "intent", nil); err != nil {
		return err
	}
	if err := tx.verifyStable(); err != nil {
		return err
	}
	_, _, _, _, binding, err := tx.pointerPaths(index)
	if err != nil {
		return err
	}
	parent, err := featureRenamePinnedRoot(binding)
	if err != nil {
		return err
	}
	defer parent.Close() //nolint:errcheck
	if err := parent.Mkdir(filepath.Base(pointer.StageRoot), 0o700); err != nil {
		return fmt.Errorf("unknown or incomplete staging at %s; it will not be overwritten: %w", pointer.StageRoot, err)
	}
	if err := managedSyncRootDir(parent, "."); err != nil {
		return err
	}
	directory, err := featureRenameDirectoryAt(pointer.StageRoot)
	if err != nil {
		return err
	}
	if err := featureRenameWriteExclusive(directory, "next", pointer.After, fs.FileMode(pointer.Before.Observation.Mode)); err != nil {
		return err
	}
	file, err := featureRenameReadFile(filepath.Join(pointer.StageRoot, "next"), false)
	if err != nil {
		return err
	}
	return tx.record(effect, "done", &featureRenameEvent{Directory: &directory, File: &file.Observation})
}

func (tx *featureRenameOperation) renamePointerObject(index int, from, to string, expected managedFileObservation) error {
	_, _, _, _, binding, err := tx.pointerPaths(index)
	if err != nil {
		return err
	}
	if err := binding.verify(); err != nil {
		return err
	}
	if err := (featureRenameFile{Observation: expected}).verifyAt(from); err != nil {
		return err
	}
	fromName, err := filepath.Rel(binding.Path, from)
	if err != nil {
		return err
	}
	toName, err := filepath.Rel(binding.Path, to)
	if err != nil {
		return err
	}
	if err := featureRenameCheckpoint(fmt.Sprintf("before-pointer-capture.%d", index), from); err != nil {
		return err
	}
	if err := tx.verifyStable(); err != nil {
		return err
	}
	renameErr := featureRenameRenameNoReplace(binding, fromName, toName)
	// Verify the captured object even when the rename returned a sync error.
	// An unexpected object is preserved, never discarded or overwritten.
	checkErr := (featureRenameFile{Observation: expected}).verifyAt(to)
	return errors.Join(renameErr, checkErr)
}

func (tx *featureRenameOperation) pointerState(index int) (string, error) {
	active, original, next, published, _, err := tx.pointerPaths(index)
	if err != nil {
		return "", err
	}
	pointer := tx.plan.Pointers[index]
	stage := tx.stage(index)
	if stage == nil {
		return "", fmt.Errorf("no verified staging evidence for %s", active)
	}
	if err := stage.Directory.verify(); err != nil {
		return "", err
	}
	if err := (featureRenameFile{Observation: *stage.File}).verifyAt(next); err != nil {
		return "", err
	}
	live, err := featureRenameReadFile(active, false)
	if err != nil {
		return "", err
	}
	saved, err := featureRenameReadFile(original, false)
	if err != nil {
		return "", err
	}
	retired, err := featureRenameReadFile(published, false)
	if err != nil {
		return "", err
	}
	pre, post := pointer.Before.Observation, *stage.File
	switch {
	case live.Observation == pre && !saved.Observation.Exists &&
		(!retired.Observation.Exists || retired.Observation == post):
		return "original", nil
	case !live.Observation.Exists && saved.Observation == pre &&
		(!retired.Observation.Exists || retired.Observation == post):
		return "preserved", nil
	case live.Observation == post && saved.Observation == pre && !retired.Observation.Exists:
		return "published", nil
	default:
		return "", fmt.Errorf("unknown pointer/registry binding at %s; preserve evidence at %s", active, pointer.StageRoot)
	}
}

func (tx *featureRenameOperation) preservePointer(index int) error {
	state, err := tx.pointerState(index)
	if err != nil {
		return err
	}
	if state != "original" {
		return tx.syncPointerBindings(index, state)
	}
	if tx.has(fmt.Sprintf("preserve.%d", index), "done") {
		return errors.New("preserved pointer unexpectedly returned to its original binding")
	}
	active, original, _, _, _, err := tx.pointerPaths(index)
	if err != nil {
		return err
	}
	return tx.renamePointerObject(index, active, original, tx.plan.Pointers[index].Before.Observation)
}

func (tx *featureRenameOperation) publishPointer(index int) error {
	state, err := tx.pointerState(index)
	if err != nil {
		return err
	}
	if state == "published" {
		return tx.syncPointerBindings(index, state)
	}
	if state != "preserved" || tx.has(fmt.Sprintf("publish.%d", index), "done") {
		return errors.New("pointer publication lost its preserved original or owned postimage")
	}
	active, _, next, _, binding, err := tx.pointerPaths(index)
	if err != nil {
		return err
	}
	root, err := featureRenamePinnedRoot(binding)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	activeRel, err := filepath.Rel(binding.Path, active)
	if err != nil {
		return err
	}
	nextRel, err := filepath.Rel(binding.Path, next)
	if err != nil {
		return err
	}
	source, sourceID, err := managedOpenPinnedDirectory(root, filepath.Dir(nextRel), false)
	if err != nil {
		return err
	}
	defer source.Close() //nolint:errcheck
	if sourceID != tx.stage(index).Directory.Identity {
		return errors.New("pointer staging parent identity changed")
	}
	destination, destinationID, err := managedOpenPinnedDirectory(root, filepath.Dir(activeRel), false)
	if err != nil {
		return err
	}
	defer destination.Close() //nolint:errcheck
	expectedDestination := binding.Identity
	if tx.plan.Pointers[index].Kind == "forward" {
		expectedDestination = tx.plan.Worktrees[tx.plan.Pointers[index].Worktree].Directory.Identity
	}
	if destinationID != expectedDestination {
		return errors.New("pointer destination parent identity changed")
	}
	if err := featureRenameCheckpoint(fmt.Sprintf("before-pointer-publication.%d", index), active); err != nil {
		return err
	}
	if err := binding.verify(); err != nil {
		return err
	}
	if err := managedVerifyPinnedDirectoryBinding(root, filepath.Dir(activeRel), destinationID); err != nil {
		return err
	}
	if err := managedLinkPinned(source, filepath.Base(nextRel), destination, filepath.Base(activeRel)); err != nil {
		return err
	}
	if err := featureRenameCheckpoint(fmt.Sprintf("after-pointer-link-before-sync.%d", index), active); err != nil {
		return err
	}
	return errors.Join(
		featureRenameSyncDirectory(featureRenameDirectory{Path: filepath.Dir(active), Identity: destinationID}),
		managedVerifyPinnedDirectoryBinding(root, filepath.Dir(nextRel), sourceID),
		managedVerifyPinnedDirectoryBinding(root, filepath.Dir(activeRel), destinationID),
		(featureRenameFile{Observation: *tx.stage(index).File}).verifyAt(active),
	)
}

func (tx *featureRenameOperation) verifyFinalGit(moved bool) error {
	if err := tx.verifyStable(); err != nil {
		return err
	}
	if err := tx.verifyPhysicalInventory(); err != nil {
		return err
	}
	path, actualMoved, err := tx.location()
	if err != nil || actualMoved != moved {
		return errors.Join(err, errors.New("feature rename is not at the required final binding"))
	}
	for _, tree := range tx.plan.Worktrees {
		treePath := filepath.Join(path, "worktrees", tree.Name)
		if err := verifyInjectWorktree(path, treePath); err != nil {
			return err
		}
		admin, err := resolveInjectGitDir(treePath, filepath.Join(treePath, ".git"))
		if err != nil || featureRenameMappedPath(admin, tx.plan.Aliases) != tree.Admin.Path {
			return errors.Join(err, errors.New("final worktree admin binding changed"))
		}
		actualAdmin, err := featureRenameGitPath(treePath, "rev-parse", "--absolute-git-dir")
		if err != nil || featureRenameMappedPath(actualAdmin, tx.plan.Aliases) != tree.Admin.Path {
			return errors.Join(err, errors.New("Git reports a different final administrative directory"))
		}
		common, err := tx.plan.common(treePath, false)
		if err != nil || common != tree.Common {
			return errors.Join(err, errors.New("final worktree common directory changed"))
		}
	}
	return tx.plan.verifyInventory(moved)
}

func (tx *featureRenameOperation) verifyPhysicalInventory() error {
	path, _, err := tx.location()
	if err != nil {
		return err
	}
	physical, err := featureRenameScanGit(path)
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(physical))
	for _, root := range physical {
		found[root] = true
	}
	for i, tree := range tx.plan.Worktrees {
		expected := filepath.Join(path, "worktrees", tree.Name)
		if !found[expected] {
			// A recorded forward-pointer preservation can legitimately leave
			// this one marker absent until publication or restoration.
			for index, pointer := range tx.plan.Pointers {
				if pointer.Kind == "forward" && pointer.Worktree == i &&
					tx.has(fmt.Sprintf("preserve.%d", index), "intent") {
					state, stateErr := tx.pointerState(index)
					if stateErr != nil {
						return stateErr
					}
					if state == "preserved" {
						found[expected] = true
					}
				}
			}
		}
		if !found[expected] {
			return errors.New("physical Git inventory no longer matches the frozen rename plan")
		}
	}
	if len(found) != len(tx.plan.Worktrees) {
		return errors.New("physical Git inventory changed since feature rename planning")
	}
	return nil
}

func (tx *featureRenameOperation) verifyFinal(moved bool) error {
	if err := tx.verifyFinalGit(moved); err != nil {
		return err
	}
	if !moved || !tx.plan.registryChanged() {
		if err := tx.plan.Registry.verifyAt(tx.plan.Registry.Path); err != nil {
			return err
		}
	}
	for i, pointer := range tx.plan.Pointers {
		active, _, _, _, _, err := tx.pointerPaths(i)
		if err != nil {
			return err
		}
		expected := pointer.Before.Observation
		if moved {
			stage := tx.stage(i)
			if stage == nil {
				return errors.New("final pointer has no staging evidence")
			}
			expected = *stage.File
		}
		if err := (featureRenameFile{Observation: expected}).verifyAt(active); err != nil {
			return err
		}
	}
	return nil
}

func (tx *featureRenameOperation) rollback() error {
	if tx.has("complete", "done") {
		return errors.New("completed feature rename cannot be aborted")
	}
	if tx.has("aborted", "done") {
		return tx.cleanup(true)
	}
	if err := tx.verifyStable(); err != nil {
		return err
	}
	if err := tx.verifyPhysicalInventory(); err != nil {
		return err
	}
	// Judge the entire inverse before changing anything. In particular a
	// foreign registry edit must not cause a directory-only rollback.
	if !tx.plan.registryChanged() {
		if err := tx.plan.Registry.verifyAt(tx.plan.Registry.Path); err != nil {
			return err
		}
	}
	for i, pointer := range tx.plan.Pointers {
		if tx.stage(i) == nil {
			if _, err := os.Lstat(pointer.StageRoot); !errors.Is(err, fs.ErrNotExist) {
				return errors.Join(err, fmt.Errorf("unknown staging residue at %s", pointer.StageRoot))
			}
			active, _, _, _, _, err := tx.pointerPaths(i)
			if err != nil {
				return err
			}
			if err := pointer.Before.verifyAt(active); err != nil {
				return err
			}
		} else if _, err := tx.pointerState(i); err != nil {
			return err
		}
	}
	if err := tx.record("abort", "intent", nil); err != nil {
		return err
	}
	if err := tx.record("abort", "done", nil); err != nil {
		return err
	}
	for i := len(tx.plan.Pointers) - 1; i >= 0; i-- {
		if tx.stage(i) == nil {
			continue
		}
		pointer := tx.plan.Pointers[i]
		if err := tx.effect(fmt.Sprintf("unpublish.%d", i), pointer.Before.Path, func() error {
			state, err := tx.pointerState(i)
			if err != nil {
				return err
			}
			if state != "published" {
				return tx.syncPointerBindings(i, state)
			}
			active, _, _, retired, _, err := tx.pointerPaths(i)
			if err != nil {
				return err
			}
			return tx.renamePointerObject(i, active, retired, *tx.stage(i).File)
		}); err != nil {
			return err
		}
		if err := tx.effect(fmt.Sprintf("restore.%d", i), pointer.Before.Path, func() error {
			state, err := tx.pointerState(i)
			if err != nil {
				return err
			}
			if state == "original" {
				return tx.syncPointerBindings(i, state)
			}
			if state != "preserved" {
				return errors.New("owned publication must be preserved before restoring the original")
			}
			active, original, _, _, _, err := tx.pointerPaths(i)
			if err != nil {
				return err
			}
			return tx.renamePointerObject(i, original, active, pointer.Before.Observation)
		}); err != nil {
			return err
		}
	}
	if err := tx.effect("move-back", tx.plan.Feature.Path, func() error { return tx.move(true) }); err != nil {
		return err
	}
	if err := tx.effect("aborted", tx.plan.Operation.Path, func() error { return tx.verifyFinal(false) }); err != nil {
		return err
	}
	return tx.cleanup(true)
}

func (tx *featureRenameOperation) cleanup(aborted bool) error {
	if err := tx.verifyFinal(!aborted); err != nil {
		return err
	}
	if err := tx.syncFinalDirectories(); err != nil {
		return err
	}
	if err := tx.verifyFinal(!aborted); err != nil {
		return err
	}
	for i, pointer := range tx.plan.Pointers {
		if tx.stage(i) == nil {
			continue
		}
		if err := tx.effect(fmt.Sprintf("cleanup.%d", i), pointer.StageRoot, func() error {
			return tx.cleanupPointer(i, aborted)
		}); err != nil {
			return err
		}
	}
	return tx.effect("cleaned", tx.plan.Operation.Path, func() error { return tx.verifyFinal(!aborted) })
}

func (tx *featureRenameOperation) cleanupPointer(index int, aborted bool) error {
	stage := tx.stage(index)
	info, err := os.Lstat(stage.Directory.Path)
	if errors.Is(err, fs.ErrNotExist) && tx.has(fmt.Sprintf("cleanup.%d", index), "intent") {
		_, _, _, _, binding, err := tx.pointerPaths(index)
		if err != nil {
			return err
		}
		if err := tx.verifyStable(); err != nil {
			return err
		}
		if err := featureRenameSyncDirectory(binding); err != nil {
			return err
		}
		if _, err := os.Lstat(stage.Directory.Path); !errors.Is(err, fs.ErrNotExist) {
			return errors.Join(err, errors.New("cleanup directory reappeared during durability barrier"))
		}
		return nil
	}
	if err != nil {
		return err
	}
	id, err := managedIdentityFromInfo(info)
	if err != nil || id != stage.Directory.Identity {
		return errors.Join(err, errors.New("cleanup staging directory was replaced"))
	}
	root, err := featureRenamePinnedRoot(*stage.Directory)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	expected := map[string]managedFileObservation{
		"next": *stage.File, "original": tx.plan.Pointers[index].Before.Observation,
		"published": *stage.File,
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := strings.TrimPrefix(entry.Name(), "cleanup-")
		want, known := expected[name]
		if !known || (aborted && name == "original") ||
			(name == "published" && !tx.has(fmt.Sprintf("unpublish.%d", index), "intent")) {
			return fmt.Errorf("unexpected recovery residue at %s; it will not be removed", filepath.Join(stage.Directory.Path, entry.Name()))
		}
		got, err := observeManagedFileRoot(root, entry.Name())
		if err != nil || got != want {
			return errors.Join(err, fmt.Errorf("recovery object changed at %s", filepath.Join(stage.Directory.Path, entry.Name())))
		}
	}
	for _, name := range []string{"original", "published", "next"} {
		private := "cleanup-" + name
		original, err := observeManagedFileRoot(root, name)
		if err != nil {
			return err
		}
		if original.Exists {
			if err := featureRenameCheckpoint(fmt.Sprintf("before-cleanup-capture.%d.%s", index, name), filepath.Join(stage.Directory.Path, name)); err != nil {
				return err
			}
			if err := stage.Directory.verify(); err != nil {
				return err
			}
			if err := tx.verifyStable(); err != nil {
				return err
			}
			if err := featureRenameRenameNoReplace(*stage.Directory, name, private); err != nil {
				return err
			}
			if err := featureRenameCheckpoint(fmt.Sprintf("after-cleanup-capture.%d.%s", index, name), filepath.Join(stage.Directory.Path, private)); err != nil {
				return err
			}
		}
		captured, err := observeManagedFileRoot(root, private)
		if err != nil {
			return err
		}
		if !captured.Exists {
			if err := featureRenameSyncDirectory(*stage.Directory); err != nil {
				return err
			}
			continue
		}
		if captured != expected[name] {
			return fmt.Errorf("unexpected object captured at %s; preserving it", filepath.Join(stage.Directory.Path, private))
		}
		if err := featureRenameCheckpoint(fmt.Sprintf("before-cleanup-remove.%d.%s", index, name), filepath.Join(stage.Directory.Path, private)); err != nil {
			return err
		}
		if err := stage.Directory.verify(); err != nil {
			return err
		}
		if err := tx.verifyStable(); err != nil {
			return err
		}
		if got, err := observeManagedFileRoot(root, private); err != nil || got != expected[name] {
			return errors.Join(err, errors.New("cleanup object changed immediately before removal"))
		}
		if err := root.Remove(private); err != nil {
			return err
		}
		if err := featureRenameCheckpoint("after-cleanup-remove-before-sync", filepath.Join(stage.Directory.Path, private)); err != nil {
			return err
		}
		if err := featureRenameSyncDirectory(*stage.Directory); err != nil {
			return err
		}
		if err := featureRenameCheckpoint(fmt.Sprintf("after-cleanup-remove.%d.%s", index, name), filepath.Join(stage.Directory.Path, private)); err != nil {
			return err
		}
	}
	_, _, _, _, binding, err := tx.pointerPaths(index)
	if err != nil {
		return err
	}
	parent, err := featureRenamePinnedRoot(binding)
	if err != nil {
		return err
	}
	defer parent.Close() //nolint:errcheck
	if err := stage.Directory.verify(); err != nil {
		return err
	}
	if err := parent.Remove(filepath.Base(stage.Directory.Path)); err != nil {
		return err
	}
	if err := featureRenameCheckpoint("after-cleanup-directory-remove-before-sync", stage.Directory.Path); err != nil {
		return err
	}
	return featureRenameSyncDirectory(binding)
}
