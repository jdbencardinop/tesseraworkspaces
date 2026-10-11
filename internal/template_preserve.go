package internal

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const managedTemplateRecoveryName = ".tws-template-recovery"

// ManagedTemplateIntentDurableHook is a test-only seam after an immutable
// intent record and its containing directory are synced, but before the
// described filesystem effect begins.
var ManagedTemplateIntentDurableHook func(effect, originalPath, privatePath, intentPath string) error

type managedPendingRecord struct {
	Version      int                   `json:"version"`
	Feature      string                `json:"feature"`
	Root         string                `json:"root"`
	RootIdentity managedObjectIdentity `json:"root_identity"`
	Paths        []string              `json:"paths"`
}

type managedPendingIntent struct {
	Version      int                     `json:"version"`
	Sequence     int                     `json:"sequence"`
	Effect       string                  `json:"effect"`
	Feature      string                  `json:"feature"`
	Root         string                  `json:"root"`
	RootIdentity managedObjectIdentity   `json:"root_identity"`
	OriginalPath string                  `json:"original_path,omitempty"`
	PrivatePath  string                  `json:"private_path,omitempty"`
	Kind         string                  `json:"kind"`
	ExpectedFile *managedFileObservation `json:"expected_file,omitempty"`
	ExpectedLink *managedLinkObservation `json:"expected_link,omitempty"`
}

type ManagedTemplateBranchRenameRecoveryEvidence struct {
	Version              int                  `json:"version"`
	Stage                string               `json:"stage"`
	CompletedStages      []string             `json:"completed_stages"`
	PendingStages        []string             `json:"pending_stages"`
	TargetRepo           string               `json:"target_repo"`
	OldName              string               `json:"old_name"`
	NewName              string               `json:"new_name"`
	OldGitBranch         string               `json:"old_git_branch"`
	NewGitBranch         string               `json:"new_git_branch"`
	OldPath              string               `json:"old_path"`
	NewPath              string               `json:"new_path"`
	ExpectedOID          string               `json:"expected_oid"`
	StackPreimagePath    string               `json:"stack_preimage_path"`
	StackPreimageSHA256  string               `json:"stack_preimage_sha256"`
	OriginalLedgerSHA256 string               `json:"original_ledger_sha256"`
	Owner                ManagedTemplateOwner `json:"owner"`
	StackPreimage        []byte               `json:"-"`
	OriginalLedger       []byte               `json:"-"`
}

type managedPreservedObject struct {
	active   string
	recovery string
	kind     string
	file     managedFileObservation
	link     managedLinkObservation
}

type managedStagedObject struct {
	original string
	path     string
	kind     string
	file     managedFileObservation
	link     managedLinkObservation
}

type managedPrivateMetadata struct {
	path string
	file managedFileObservation
}

type managedRecoveryRoot struct {
	displayPath       string
	root              *os.Root
	rootIdentity      managedObjectIdentity
	feature           string
	opDir             string
	journal           string
	journalFile       managedPrivateMetadata
	preserved         []managedPreservedObject
	staged            []managedStagedObject
	intentFiles       []managedPrivateMetadata
	nextIntent        int
	cleaned           bool
	validateUnwritten func() error
}

func managedRecoveryResidue(rootPath string) (string, error) {
	info, err := os.Lstat(rootPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("managed template root %s is not a regular directory", rootPath)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer root.Close() //nolint:errcheck
	recoveryInfo, err := root.Lstat(managedTemplateRecoveryName)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if recoveryInfo.Mode()&os.ModeSymlink != 0 || !recoveryInfo.IsDir() {
		return "", fmt.Errorf("managed template recovery path %s is not a regular directory",
			filepath.Join(rootPath, managedTemplateRecoveryName))
	}
	return filepath.Join(rootPath, managedTemplateRecoveryName), nil
}

func ManagedTemplateRecoveryResidue(rootPath string) (string, error) {
	return managedRecoveryResidue(rootPath)
}

func RecordManagedTemplateBranchRenameRecoveryEvidence(
	featurePath, feature string,
	evidence ManagedTemplateBranchRenameRecoveryEvidence,
) (string, error) {
	root, err := os.OpenRoot(featurePath)
	if err != nil {
		return "", err
	}
	defer root.Close() //nolint:errcheck

	var opDir string
	recoveryInfo, recoveryErr := root.Lstat(managedTemplateRecoveryName)
	switch {
	case errors.Is(recoveryErr, fs.ErrNotExist):
	case recoveryErr != nil:
		return "", recoveryErr
	case recoveryInfo.Mode()&os.ModeSymlink != 0 || !recoveryInfo.IsDir():
		return "", fmt.Errorf("managed template recovery path %s is not a regular directory",
			filepath.Join(featurePath, managedTemplateRecoveryName))
	default:
		entries, readErr := fs.ReadDir(root.FS(), managedTemplateRecoveryName)
		if readErr != nil {
			return "", readErr
		}
		if len(entries) > 1 || (len(entries) == 1 && !entries[0].IsDir()) {
			return "", fmt.Errorf("managed template recovery evidence at %s is ambiguous",
				filepath.Join(featurePath, managedTemplateRecoveryName))
		}
		if len(entries) == 1 {
			opDir = filepath.Join(managedTemplateRecoveryName, entries[0].Name())
		}
	}
	if opDir == "" {
		opID, idErr := managedOperationID()
		if idErr != nil {
			return "", idErr
		}
		tx, txErr := newManagedRecoveryRoot(
			root, featurePath, opID, feature,
			append(
				[]string{"branch-rename/" + evidence.Stage},
				evidence.OldPath, evidence.NewPath,
			),
		)
		if txErr != nil {
			return "", txErr
		}
		opDir = tx.opDir
	}

	evidence.Version = 1
	evidence.StackPreimagePath = filepath.Join(opDir, "stack-preimage.yaml")
	evidence.StackPreimageSHA256 = digestBytes(evidence.StackPreimage)
	evidence.OriginalLedgerSHA256 = digestBytes(evidence.OriginalLedger)
	writeExclusive := func(relative string, data []byte, mode fs.FileMode) error {
		file, openErr := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if openErr != nil {
			return openErr
		}
		if _, writeErr := file.Write(data); writeErr != nil {
			_ = file.Close()
			return writeErr
		}
		if syncErr := file.Sync(); syncErr != nil {
			_ = file.Close()
			return syncErr
		}
		return file.Close()
	}
	writeOrVerify := func(relative string, data []byte, mode fs.FileMode) error {
		if err := writeExclusive(relative, data, mode); err == nil {
			return nil
		} else if !errors.Is(err, fs.ErrExist) {
			return err
		}
		existing, err := fs.ReadFile(root.FS(), relative)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("existing managed rename recovery evidence changed at %s",
				filepath.Join(featurePath, relative))
		}
		return nil
	}
	if err := writeOrVerify(evidence.StackPreimagePath, evidence.StackPreimage, 0o600); err != nil {
		return "", err
	}
	recordPath := filepath.Join(opDir, "rename.json")
	record, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return "", err
	}
	record = append(record, '\n')
	if err := writeOrVerify(recordPath, record, 0o600); err != nil {
		return "", err
	}
	if err := managedSyncRootDir(root, opDir); err != nil {
		return "", err
	}
	return filepath.Join(featurePath, recordPath), nil
}

func openManagedFeatureRoot(plan *ManagedTemplatePlan) (*os.Root, error) {
	metadataPath := filepath.Clean(plan.Request.Workspace.MetadataRoot)
	if err := os.MkdirAll(metadataPath, 0o755); err != nil {
		return nil, err
	}
	metadataRoot, err := os.OpenRoot(metadataPath)
	if err != nil {
		return nil, err
	}
	defer metadataRoot.Close() //nolint:errcheck
	if plan.metadataIdentity != (managedObjectIdentity{}) {
		if err := managedVerifyPathIdentity(metadataPath, plan.metadataIdentity); err != nil {
			return nil, err
		}
	}
	rel, err := filepath.Rel(metadataPath, filepath.Clean(plan.Request.FeaturePath))
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("feature path %s escapes workspace metadata root %s",
			plan.Request.FeaturePath, metadataPath)
	}
	if err := metadataRoot.MkdirAll(rel, 0o755); err != nil {
		return nil, err
	}
	info, err := metadataRoot.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("managed template feature root %s is not a regular directory", plan.Request.FeaturePath)
	}
	featureRoot, err := metadataRoot.OpenRoot(rel)
	if err != nil {
		return nil, err
	}
	if plan.featureIdentity != (managedObjectIdentity{}) {
		got, identityErr := managedRootIdentity(featureRoot)
		if identityErr != nil {
			_ = featureRoot.Close()
			return nil, identityErr
		}
		if got != plan.featureIdentity {
			_ = featureRoot.Close()
			return nil, fmt.Errorf("managed template feature root identity changed at %s", plan.Request.FeaturePath)
		}
	}
	lockFile, err := featureRoot.OpenFile(managedTemplateLockName, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = featureRoot.Close()
		return nil, err
	}
	if err := lockFile.Close(); err != nil {
		_ = featureRoot.Close()
		return nil, err
	}
	return featureRoot, nil
}

func openManagedWorktreeRoot(path string, expected managedWorktreeIdentity) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	got, err := managedRootIdentity(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if got.Device != expected.WorktreeDevice || got.Inode != expected.WorktreeInode {
		_ = root.Close()
		return nil, fmt.Errorf("managed worktree root identity changed at %s", path)
	}
	return root, nil
}

func newManagedRecoveryRoot(
	root *os.Root,
	displayPath, opID, feature string,
	paths []string,
) (*managedRecoveryRoot, error) {
	rootIdentity, err := managedRootIdentity(root)
	if err != nil {
		return nil, err
	}
	_, recoveryErr := root.Lstat(managedTemplateRecoveryName)
	recoveryAbsent := errors.Is(recoveryErr, fs.ErrNotExist)
	if recoveryErr != nil && !recoveryAbsent {
		return nil, recoveryErr
	}
	if err := root.MkdirAll(managedTemplateRecoveryName, 0o700); err != nil {
		return nil, err
	}
	if recoveryAbsent {
		if err := managedSyncRootDir(root, "."); err != nil {
			return nil, err
		}
	}
	recoveryInfo, err := root.Lstat(managedTemplateRecoveryName)
	if err != nil {
		return nil, err
	}
	if recoveryInfo.Mode()&os.ModeSymlink != 0 || !recoveryInfo.IsDir() {
		return nil, fmt.Errorf("managed template recovery path %s is not a regular directory",
			filepath.Join(displayPath, managedTemplateRecoveryName))
	}
	entries, err := fs.ReadDir(root.FS(), managedTemplateRecoveryName)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("managed template recovery evidence already exists at %s",
			filepath.Join(displayPath, managedTemplateRecoveryName))
	}
	opDir := filepath.Join(managedTemplateRecoveryName, opID)
	if err := root.Mkdir(opDir, 0o700); err != nil {
		return nil, err
	}
	if err := managedSyncRootDir(root, managedTemplateRecoveryName); err != nil {
		return nil, err
	}
	sort.Strings(paths)
	record := managedPendingRecord{
		Version: 1, Feature: feature, Root: displayPath,
		RootIdentity: rootIdentity, Paths: paths,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	journal := filepath.Join(opDir, "pending.json")
	file, err := root.OpenFile(journal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := managedSyncRootDir(root, opDir); err != nil {
		return nil, err
	}
	journalObservation, err := observeManagedFileRoot(root, journal)
	if err != nil {
		return nil, err
	}
	return &managedRecoveryRoot{
		displayPath:  displayPath,
		root:         root,
		rootIdentity: rootIdentity,
		feature:      feature,
		opDir:        opDir,
		journal:      journal,
		journalFile: managedPrivateMetadata{
			path: journal,
			file: journalObservation,
		},
	}, nil
}

func managedOperationID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

func (tx *managedRecoveryRoot) uniquePath(prefix string) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		path := filepath.Join(tx.opDir, prefix+"-"+hex.EncodeToString(random))
		if _, err := tx.root.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate managed template recovery path")
}

func (tx *managedRecoveryRoot) recordIntent(intent managedPendingIntent) error {
	intent.Version = 1
	intent.Sequence = tx.nextIntent
	intent.Feature = tx.feature
	intent.Root = tx.displayPath
	intent.RootIdentity = tx.rootIdentity
	path := filepath.Join(tx.opDir, fmt.Sprintf("intent-%06d.json", intent.Sequence))
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := tx.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := managedSyncRootDir(tx.root, tx.opDir); err != nil {
		return err
	}
	observation, err := observeManagedFileRoot(tx.root, path)
	if err != nil {
		return err
	}
	tx.intentFiles = append(tx.intentFiles, managedPrivateMetadata{
		path: path,
		file: observation,
	})
	tx.nextIntent++
	if ManagedTemplateIntentDurableHook != nil {
		if err := ManagedTemplateIntentDurableHook(
			intent.Effect, intent.OriginalPath, intent.PrivatePath,
			filepath.Join(tx.displayPath, path),
		); err != nil {
			return err
		}
	}
	return nil
}

func (tx *managedRecoveryRoot) stageRegular(
	original string,
	data []byte,
	mode fs.FileMode,
) (managedStagedObject, error) {
	path, err := tx.uniquePath("stage-file")
	if err != nil {
		return managedStagedObject{}, err
	}
	expected := managedFileObservation{
		Exists: true, Kind: "file", Hash: digestBytes(data), Mode: uint32(mode.Perm()),
	}
	if err := tx.recordIntent(managedPendingIntent{
		Effect: "stage-create", OriginalPath: original, PrivatePath: path,
		Kind: "file", ExpectedFile: &expected,
	}); err != nil {
		return managedStagedObject{}, err
	}
	file, err := tx.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return managedStagedObject{}, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return managedStagedObject{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return managedStagedObject{}, err
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		_ = file.Close()
		return managedStagedObject{}, err
	}
	if err := file.Close(); err != nil {
		return managedStagedObject{}, err
	}
	if err := managedSyncRootDir(tx.root, tx.opDir); err != nil {
		return managedStagedObject{}, err
	}
	observation, err := observeManagedFileRoot(tx.root, path)
	if err != nil {
		return managedStagedObject{}, err
	}
	staged := managedStagedObject{
		original: original, path: path, kind: "file", file: observation,
	}
	tx.staged = append(tx.staged, staged)
	return staged, nil
}

func (tx *managedRecoveryRoot) stageLink(
	original, target string,
) (managedStagedObject, error) {
	path, err := tx.uniquePath("stage-link")
	if err != nil {
		return managedStagedObject{}, err
	}
	expected := managedLinkObservation{
		Exists: true, Kind: "symlink", Target: target,
	}
	if err := tx.recordIntent(managedPendingIntent{
		Effect: "stage-create", OriginalPath: original, PrivatePath: path,
		Kind: "link", ExpectedLink: &expected,
	}); err != nil {
		return managedStagedObject{}, err
	}
	if err := tx.root.Symlink(target, path); err != nil {
		return managedStagedObject{}, err
	}
	if err := managedSyncRootDir(tx.root, tx.opDir); err != nil {
		return managedStagedObject{}, err
	}
	observation, err := observeManagedLinkRoot(tx.root, path)
	if err != nil {
		return managedStagedObject{}, err
	}
	staged := managedStagedObject{
		original: original, path: path, kind: "link", link: observation,
	}
	tx.staged = append(tx.staged, staged)
	return staged, nil
}

func (tx *managedRecoveryRoot) publish(staged managedStagedObject, destination string) error {
	parent := filepath.Dir(destination)
	intent := managedPendingIntent{
		Effect: "publish-active", OriginalPath: destination,
		PrivatePath: staged.path, Kind: staged.kind,
	}
	if staged.kind == "file" {
		expected := staged.file
		intent.ExpectedFile = &expected
	} else {
		expected := staged.link
		intent.ExpectedLink = &expected
	}
	if err := tx.recordIntent(intent); err != nil {
		return err
	}
	if destination == managedTemplateStateName && tx.validateUnwritten != nil {
		if err := tx.validateUnwritten(); err != nil {
			return err
		}
	}
	sourceParentPath := filepath.Dir(staged.path)
	sourceParent, _, err := managedOpenPinnedDirectory(tx.root, sourceParentPath, false)
	if err != nil {
		return fmt.Errorf("opening pinned staging parent %s: %w", sourceParentPath, err)
	}
	defer sourceParent.Close() //nolint:errcheck
	destinationParent, destinationIdentity, err := managedOpenPinnedDirectory(tx.root, parent, true)
	if err != nil {
		return fmt.Errorf("opening pinned destination parent %s: %w", parent, err)
	}
	defer destinationParent.Close() //nolint:errcheck
	if err := managedLinkPinned(
		sourceParent, filepath.Base(staged.path),
		destinationParent, filepath.Base(destination),
	); err != nil {
		return fmt.Errorf("publishing managed path without replacement %s: %w",
			filepath.Join(tx.displayPath, destination), err)
	}
	if err := destinationParent.Sync(); err != nil {
		return err
	}
	if err := managedVerifyPinnedDirectoryBinding(tx.root, parent, destinationIdentity); err != nil {
		return fmt.Errorf("managed destination parent binding changed at %s: %w",
			filepath.Join(tx.displayPath, parent), err)
	}
	return nil
}

func (tx *managedRecoveryRoot) preserveFile(destination string, expected managedFileObservation) error {
	recovery, err := tx.uniquePath("old-file")
	if err != nil {
		return err
	}
	if err := tx.recordIntent(managedPendingIntent{
		Effect: "preserve-active", OriginalPath: destination,
		PrivatePath: recovery, Kind: "file", ExpectedFile: &expected,
	}); err != nil {
		return err
	}
	if destination == managedTemplateStateName && tx.validateUnwritten != nil {
		if err := tx.validateUnwritten(); err != nil {
			return err
		}
	}
	if err := managedRenameNoReplace(tx.root, destination, recovery); err != nil {
		return err
	}
	if err := tx.syncRenameParents(destination, recovery); err != nil {
		return err
	}
	got, err := observeManagedFileRoot(tx.root, recovery)
	if err != nil {
		return fmt.Errorf("inspecting captured file failed; preserved at %s with pending evidence %s: %w",
			filepath.Join(tx.displayPath, recovery), filepath.Join(tx.displayPath, tx.journal), err)
	}
	if got != expected {
		return tx.restoreAfterPreservationFailure(
			destination, recovery, "file", &got, nil,
			errors.New("captured file does not match planned identity/content"),
		)
	}
	tx.preserved = append(tx.preserved, managedPreservedObject{
		active: destination, recovery: recovery, kind: "file", file: expected,
	})
	return nil
}

func (tx *managedRecoveryRoot) preserveLink(destination string, expected managedLinkObservation) error {
	recovery, err := tx.uniquePath("old-link")
	if err != nil {
		return err
	}
	if err := tx.recordIntent(managedPendingIntent{
		Effect: "preserve-active", OriginalPath: destination,
		PrivatePath: recovery, Kind: "link", ExpectedLink: &expected,
	}); err != nil {
		return err
	}
	if err := managedRenameNoReplace(tx.root, destination, recovery); err != nil {
		return err
	}
	if err := tx.syncRenameParents(destination, recovery); err != nil {
		return err
	}
	got, err := observeManagedLinkRoot(tx.root, recovery)
	if err != nil {
		return fmt.Errorf("inspecting captured symbolic link failed; preserved at %s with pending evidence %s: %w",
			filepath.Join(tx.displayPath, recovery), filepath.Join(tx.displayPath, tx.journal), err)
	}
	if got != expected {
		return tx.restoreAfterPreservationFailure(
			destination, recovery, "link", nil, &got,
			errors.New("captured symbolic link does not match planned identity/target"),
		)
	}
	tx.preserved = append(tx.preserved, managedPreservedObject{
		active: destination, recovery: recovery, kind: "link", link: expected,
	})
	return nil
}

func (tx *managedRecoveryRoot) syncRenameParents(original, private string) error {
	var errs []error
	for _, parent := range []string{filepath.Dir(original), filepath.Dir(private)} {
		if err := managedSyncRootDir(tx.root, parent); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (tx *managedRecoveryRoot) restoreAfterPreservationFailure(
	destination, recovery, kind string,
	file *managedFileObservation,
	link *managedLinkObservation,
	cause error,
) error {
	if err := tx.recordIntent(managedPendingIntent{
		Effect: "restore-active", OriginalPath: destination,
		PrivatePath: recovery, Kind: kind,
		ExpectedFile: file, ExpectedLink: link,
	}); err != nil {
		return fmt.Errorf("%w; restore intent could not be persisted, object remains at %s: %v",
			cause, filepath.Join(tx.displayPath, recovery), err)
	}
	restoreErr := managedRenameNoReplace(tx.root, recovery, destination)
	if restoreErr == nil {
		restoreErr = tx.syncRenameParents(recovery, destination)
	}
	if restoreErr != nil {
		return fmt.Errorf("%w; preserved conflicting objects at %s and %s: %v",
			cause, filepath.Join(tx.displayPath, destination),
			filepath.Join(tx.displayPath, recovery), restoreErr)
	}
	return fmt.Errorf("%w; restored captured object at %s; pending evidence retained at %s",
		cause, filepath.Join(tx.displayPath, destination),
		filepath.Join(tx.displayPath, tx.journal))
}

func (tx *managedRecoveryRoot) preflightCleanup() error {
	for _, preserved := range tx.preserved {
		if ManagedTemplateApplyStepHook != nil {
			if err := ManagedTemplateApplyStepHook(
				"before-cleanup", filepath.Join(tx.displayPath, preserved.recovery),
			); err != nil {
				return fmt.Errorf("cleanup stopped with recovery object preserved at %s: %w",
					filepath.Join(tx.displayPath, preserved.recovery), err)
			}
		}
		if tx.validateUnwritten != nil {
			if err := tx.validateUnwritten(); err != nil {
				return err
			}
		}
	}
	for _, preserved := range tx.preserved {
		if preserved.kind == "file" {
			got, err := observeManagedFileRoot(tx.root, preserved.recovery)
			if err != nil {
				return fmt.Errorf("verifying recovery copy %s: %w",
					filepath.Join(tx.displayPath, preserved.recovery), err)
			}
			if got != preserved.file {
				return fmt.Errorf("recovery copy changed; preserved at %s with pending evidence %s",
					filepath.Join(tx.displayPath, preserved.recovery),
					filepath.Join(tx.displayPath, tx.journal))
			}
		} else {
			got, err := observeManagedLinkRoot(tx.root, preserved.recovery)
			if err != nil {
				return fmt.Errorf("verifying recovery link %s: %w",
					filepath.Join(tx.displayPath, preserved.recovery), err)
			}
			if got != preserved.link {
				return fmt.Errorf("recovery link changed; preserved at %s with pending evidence %s",
					filepath.Join(tx.displayPath, preserved.recovery),
					filepath.Join(tx.displayPath, tx.journal))
			}
		}
	}
	stagedByOriginal := make(map[string]managedStagedObject, len(tx.staged))
	for _, staged := range tx.staged {
		stagedByOriginal[staged.original] = staged
		var (
			stagedMatches bool
			activeMatches bool
			err           error
		)
		if staged.kind == "file" {
			var got managedFileObservation
			got, err = observeManagedFileRoot(tx.root, staged.path)
			stagedMatches = got == staged.file
			if err == nil {
				got, err = observeManagedFileRoot(tx.root, staged.original)
				activeMatches = got == staged.file
			}
		} else {
			var got managedLinkObservation
			got, err = observeManagedLinkRoot(tx.root, staged.path)
			stagedMatches = got == staged.link
			if err == nil {
				got, err = observeManagedLinkRoot(tx.root, staged.original)
				activeMatches = got == staged.link
			}
		}
		if err != nil {
			return fmt.Errorf("verifying staged and active managed object %s: %w",
				filepath.Join(tx.displayPath, staged.original), err)
		}
		if !stagedMatches {
			return fmt.Errorf("staging %s changed; preserved at %s with pending evidence %s",
				staged.kind, filepath.Join(tx.displayPath, staged.path),
				filepath.Join(tx.displayPath, tx.journal))
		}
		if !activeMatches {
			return fmt.Errorf("active managed %s no longer matches staged publication at %s; pending evidence retained at %s",
				staged.kind, filepath.Join(tx.displayPath, staged.original),
				filepath.Join(tx.displayPath, tx.journal))
		}
	}
	for _, preserved := range tx.preserved {
		if _, replaced := stagedByOriginal[preserved.active]; replaced {
			continue
		}
		if _, err := tx.root.Lstat(preserved.active); !errors.Is(err, fs.ErrNotExist) {
			if err != nil {
				return fmt.Errorf("verifying removed managed path %s: %w",
					filepath.Join(tx.displayPath, preserved.active), err)
			}
			return fmt.Errorf("removed managed path was recreated at %s; pending evidence retained at %s",
				filepath.Join(tx.displayPath, preserved.active),
				filepath.Join(tx.displayPath, tx.journal))
		}
	}
	if err := tx.verifyCleanupContents(true); err != nil {
		return err
	}
	return nil
}

func (tx *managedRecoveryRoot) cleanupVerified() error {
	if tx.validateUnwritten != nil {
		if err := tx.validateUnwritten(); err != nil {
			return err
		}
	}
	for _, preserved := range tx.preserved {
		intent := managedPendingIntent{
			Effect: "cleanup-private", OriginalPath: preserved.active,
			PrivatePath: preserved.recovery, Kind: preserved.kind,
		}
		if preserved.kind == "file" {
			expected := preserved.file
			intent.ExpectedFile = &expected
		} else {
			expected := preserved.link
			intent.ExpectedLink = &expected
		}
		if err := tx.recordIntent(intent); err != nil {
			return err
		}
		if err := tx.removeVerifiedPrivate(
			preserved.recovery, preserved.kind, preserved.file, preserved.link,
		); err != nil {
			return fmt.Errorf("cleaning verified recovery object %s: %w",
				filepath.Join(tx.displayPath, preserved.recovery), err)
		}
	}
	for _, staged := range tx.staged {
		intent := managedPendingIntent{
			Effect: "cleanup-private", OriginalPath: staged.original,
			PrivatePath: staged.path, Kind: staged.kind,
		}
		if staged.kind == "file" {
			expected := staged.file
			intent.ExpectedFile = &expected
		} else {
			expected := staged.link
			intent.ExpectedLink = &expected
		}
		if err := tx.recordIntent(intent); err != nil {
			return err
		}
		if err := tx.removeVerifiedPrivate(
			staged.path, staged.kind, staged.file, staged.link,
		); err != nil {
			return err
		}
	}
	if err := tx.verifyCleanupContents(false); err != nil {
		return err
	}
	for _, metadata := range tx.intentFiles {
		if err := tx.removeVerifiedMetadata(metadata); err != nil {
			return err
		}
	}
	entries, err := fs.ReadDir(tx.root.FS(), tx.opDir)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(tx.journal) {
		unexpected := tx.opDir
		if len(entries) > 0 {
			unexpected = filepath.Join(tx.opDir, entries[0].Name())
		}
		return fmt.Errorf("unexpected recovery content %s prevents cleanup",
			filepath.Join(tx.displayPath, unexpected))
	}
	if err := tx.removeVerifiedMetadata(tx.journalFile); err != nil {
		return err
	}
	entries, err = fs.ReadDir(tx.root.FS(), tx.opDir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("unexpected recovery content %s prevents cleanup",
			filepath.Join(tx.displayPath, tx.opDir, entries[0].Name()))
	}
	if err := managedSyncRootDir(tx.root, tx.opDir); err != nil {
		return err
	}
	if err := tx.root.Remove(tx.opDir); err != nil {
		return err
	}
	if err := managedSyncRootDir(tx.root, managedTemplateRecoveryName); err != nil {
		return err
	}
	if err := tx.root.Remove(managedTemplateRecoveryName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := managedSyncRootDir(tx.root, "."); err != nil {
		return err
	}
	tx.cleaned = true
	return nil
}

func (tx *managedRecoveryRoot) removeVerifiedPrivate(
	path, kind string,
	expectedFile managedFileObservation,
	expectedLink managedLinkObservation,
) error {
	if tx.validateUnwritten != nil {
		if err := tx.validateUnwritten(); err != nil {
			return err
		}
	}
	if kind == "file" {
		got, err := observeManagedFileRoot(tx.root, path)
		if err != nil {
			return err
		}
		if got != expectedFile {
			return fmt.Errorf("private file identity/content changed at %s",
				filepath.Join(tx.displayPath, path))
		}
	} else {
		got, err := observeManagedLinkRoot(tx.root, path)
		if err != nil {
			return err
		}
		if got != expectedLink {
			return fmt.Errorf("private link identity/target changed at %s",
				filepath.Join(tx.displayPath, path))
		}
	}
	return tx.root.Remove(path)
}

func (tx *managedRecoveryRoot) removeVerifiedMetadata(metadata managedPrivateMetadata) error {
	if tx.validateUnwritten != nil {
		if err := tx.validateUnwritten(); err != nil {
			return err
		}
	}
	got, err := observeManagedFileRoot(tx.root, metadata.path)
	if err != nil {
		return err
	}
	if got != metadata.file {
		return fmt.Errorf("private recovery metadata changed at %s",
			filepath.Join(tx.displayPath, metadata.path))
	}
	return tx.root.Remove(metadata.path)
}

func (tx *managedRecoveryRoot) verifyCleanupContents(includeObjects bool) error {
	entries, err := fs.ReadDir(tx.root.FS(), tx.opDir)
	if err != nil {
		return err
	}
	allowed := map[string]bool{filepath.Base(tx.journal): true}
	for _, metadata := range tx.intentFiles {
		allowed[filepath.Base(metadata.path)] = true
	}
	if includeObjects {
		for _, preserved := range tx.preserved {
			allowed[filepath.Base(preserved.recovery)] = true
		}
		for _, staged := range tx.staged {
			allowed[filepath.Base(staged.path)] = true
		}
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("unexpected recovery content %s prevents cleanup",
				filepath.Join(tx.displayPath, tx.opDir, entry.Name()))
		}
	}
	return nil
}

func observeManagedFileRoot(root *os.Root, relative string) (managedFileObservation, error) {
	info, err := root.Lstat(relative)
	if errors.Is(err, fs.ErrNotExist) {
		return managedFileObservation{}, nil
	}
	if err != nil {
		return managedFileObservation{}, err
	}
	identity, err := managedIdentityFromInfo(info)
	if err != nil {
		return managedFileObservation{}, err
	}
	kind := managedFileKind(info.Mode())
	if kind != "file" {
		return managedFileObservation{
			Exists: true, Kind: kind, Device: identity.Device, Inode: identity.Inode,
		}, nil
	}
	file, err := root.Open(relative)
	if err != nil {
		return managedFileObservation{}, err
	}
	defer file.Close() //nolint:errcheck
	before, err := file.Stat()
	if err != nil {
		return managedFileObservation{}, err
	}
	beforeIdentity, err := managedIdentityFromInfo(before)
	if err != nil {
		return managedFileObservation{}, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return managedFileObservation{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return managedFileObservation{}, err
	}
	afterIdentity, err := managedIdentityFromInfo(after)
	if err != nil {
		return managedFileObservation{}, err
	}
	if beforeIdentity != identity || afterIdentity != identity ||
		before.Mode() != after.Mode() ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return managedFileObservation{}, errors.New("regular file changed while hashing")
	}
	return managedFileObservation{
		Exists: true, Kind: "file", Hash: digestBytes(data),
		Mode: managedObservedPermissionMode(before.Mode()), Device: identity.Device, Inode: identity.Inode,
	}, nil
}

func observeManagedLinkRoot(root *os.Root, relative string) (managedLinkObservation, error) {
	info, err := root.Lstat(relative)
	if errors.Is(err, fs.ErrNotExist) {
		return managedLinkObservation{}, nil
	}
	if err != nil {
		return managedLinkObservation{}, err
	}
	identity, err := managedIdentityFromInfo(info)
	if err != nil {
		return managedLinkObservation{}, err
	}
	observation := managedLinkObservation{
		Exists: true, Kind: managedFileKind(info.Mode()),
		Device: identity.Device, Inode: identity.Inode,
	}
	if observation.Kind == "symlink" {
		target, err := root.Readlink(relative)
		if err != nil {
			return managedLinkObservation{}, err
		}
		observation.Target = target
	}
	return observation, nil
}

func applyManagedTemplatePlanPreserved(plan *ManagedTemplatePlan) (retErr error) {
	featureRoot, err := openManagedFeatureRoot(plan)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, featureRoot.Close()) }()

	opID, err := managedOperationID()
	if err != nil {
		return err
	}
	featurePaths := make([]string, 0, len(plan.Removals)+len(plan.Writes)+1)
	worktreePaths := make(map[string][]string)
	for _, removal := range plan.Removals {
		if removal.Kind == "link" {
			worktreePaths[removal.root] = append(worktreePaths[removal.root], removal.relative)
		} else {
			featurePaths = append(featurePaths, managedFeatureRelative(removal.Role, removal.relative))
		}
	}
	for _, write := range plan.Writes {
		featurePaths = append(featurePaths, managedFeatureRelative(write.Role, write.Relative))
	}
	for _, link := range plan.LinkWrites {
		worktreePaths[link.WorktreeRoot] = append(worktreePaths[link.WorktreeRoot], link.Relative)
	}
	if plan.StateChanged {
		featurePaths = append(featurePaths, managedTemplateStateName)
	}

	var featureTx *managedRecoveryRoot
	if len(featurePaths) > 0 {
		featureTx, err = newManagedRecoveryRoot(
			featureRoot, plan.Request.FeaturePath, opID, plan.Request.Feature, featurePaths,
		)
		if err != nil {
			return err
		}
		featureTx.validateUnwritten = func() error {
			return revalidateManagedTemplateFileChecks(plan, featureRoot, true)
		}
	}
	worktreeTx := make(map[string]*managedRecoveryRoot)
	for path, paths := range worktreePaths {
		var expected managedWorktreeIdentity
		for _, target := range plan.worktreeChecks {
			if filepath.Clean(target.Path) == filepath.Clean(path) {
				expected = plan.worktreeIdentities[target.Name]
				break
			}
		}
		root, openErr := openManagedWorktreeRoot(path, expected)
		if openErr != nil {
			return managedPendingApplyError(openErr, featureTx, worktreeTx)
		}
		tx, txErr := newManagedRecoveryRoot(root, path, opID, plan.Request.Feature, paths)
		if txErr != nil {
			_ = root.Close()
			return managedPendingApplyError(txErr, featureTx, worktreeTx)
		}
		worktreeTx[path] = tx
		tx.validateUnwritten = func() error {
			return revalidateManagedTemplateFileChecks(plan, featureRoot, true)
		}
	}
	defer func() {
		for _, tx := range worktreeTx {
			retErr = errors.Join(retErr, tx.root.Close())
		}
	}()

	stagedWrites := make([]managedStagedObject, len(plan.Writes))
	for i, write := range plan.Writes {
		relative := managedFeatureRelative(write.Role, write.Relative)
		staged, stageErr := featureTx.stageRegular(relative, write.Data, write.Mode)
		if stageErr != nil {
			return managedPendingApplyError(stageErr, featureTx, worktreeTx)
		}
		stagedWrites[i] = staged
	}
	stagedLinks := make([]managedStagedObject, len(plan.LinkWrites))
	for i, link := range plan.LinkWrites {
		tx := worktreeTx[link.WorktreeRoot]
		staged, stageErr := tx.stageLink(link.Relative, link.Target)
		if stageErr != nil {
			return managedPendingApplyError(stageErr, featureTx, worktreeTx)
		}
		stagedLinks[i] = staged
		if err := setManagedPlannedLinkIdentity(
			&plan.NewState, link.Worktree, link.Relative, staged.link,
			plan.worktreeIdentities[link.Worktree],
		); err != nil {
			return managedPendingApplyError(err, featureTx, worktreeTx)
		}
	}
	if err := validateManagedTemplateState(plan.NewState, false); err != nil {
		return managedPendingApplyError(
			fmt.Errorf("generated managed template state is invalid before publication: %w", err),
			featureTx, worktreeTx,
		)
	}
	var stagedState managedStagedObject
	if plan.StateChanged {
		stateData, marshalErr := marshalManagedTemplateState(plan.NewState)
		if marshalErr != nil {
			return managedPendingApplyError(marshalErr, featureTx, worktreeTx)
		}
		stagedState, err = featureTx.stageRegular(managedTemplateStateName, stateData, 0o600)
		if err != nil {
			return managedPendingApplyError(err, featureTx, worktreeTx)
		}
	}

	totalEffects := len(plan.Removals) + len(plan.Writes) + len(plan.LinkWrites)
	if plan.StateChanged {
		totalEffects++
	}
	completed := 0
	for _, removal := range plan.Removals {
		if ManagedTemplateApplyStepHook != nil {
			if err := ManagedTemplateApplyStepHook("before-remove", removal.Path); err != nil {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("stopped before removing %s: %w", removal.Path, err)), featureTx, worktreeTx)
			}
		}
		if removal.Kind == "link" {
			tx := worktreeTx[removal.root]
			tracked, err := managedGitTracked(removal.root, removal.relative)
			if err != nil {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("rechecking Git tracking before removing %s: %w", removal.Path, err)),
					featureTx, worktreeTx)
			}
			if tracked {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("worktree destination %s became tracked by Git", removal.Path)),
					featureTx, worktreeTx)
			}
			if err := tx.preserveLink(removal.relative, removal.linkBefore); err != nil {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("preserving removal %s failed: %w", removal.Path, err)), featureTx, worktreeTx)
			}
		} else {
			relative := managedFeatureRelative(removal.Role, removal.relative)
			if err := featureTx.preserveFile(relative, removal.fileBefore); err != nil {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("preserving removal %s failed: %w", removal.Path, err)), featureTx, worktreeTx)
			}
		}
		completed++
	}
	for i, write := range plan.Writes {
		path := filepath.Join(write.Root, filepath.FromSlash(write.Relative))
		if ManagedTemplateApplyStepHook != nil {
			if err := ManagedTemplateApplyStepHook("before-write", path); err != nil {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("stopped before writing %s: %w", path, err)), featureTx, worktreeTx)
			}
		}
		relative := managedFeatureRelative(write.Role, write.Relative)
		if write.Before.Exists {
			if err := featureTx.preserveFile(relative, write.Before); err != nil {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("preserving replacement %s failed: %w", path, err)), featureTx, worktreeTx)
			}
		}
		if err := featureTx.publish(stagedWrites[i], relative); err != nil {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("write %s failed: %w", path, err)), featureTx, worktreeTx)
		}
		completed++
	}
	for i, link := range plan.LinkWrites {
		path := filepath.Join(link.WorktreeRoot, filepath.FromSlash(link.Relative))
		if ManagedTemplateApplyStepHook != nil {
			if err := ManagedTemplateApplyStepHook("before-link", path); err != nil {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("stopped before linking %s: %w", path, err)), featureTx, worktreeTx)
			}
		}
		if err := validateManagedOwnedSourceFiles(
			plan.Request.FeaturePath, plan.ownedSourceChecks,
		); err != nil {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("managed source changed before linking %s: %w", path, err)),
				featureTx, worktreeTx)
		}
		tracked, err := managedGitTracked(link.WorktreeRoot, link.Relative)
		if err != nil {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("rechecking Git tracking before linking %s: %w", path, err)),
				featureTx, worktreeTx)
		}
		if tracked {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("worktree destination %s became tracked by Git", path)),
				featureTx, worktreeTx)
		}
		if err := worktreeTx[link.WorktreeRoot].publish(stagedLinks[i], link.Relative); err != nil {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("link %s failed: %w", path, err)), featureTx, worktreeTx)
		}
		completed++
	}
	if plan.StateChanged {
		if ManagedTemplateApplyStepHook != nil {
			if err := ManagedTemplateApplyStepHook("before-state", ManagedTemplateStatePath(plan.Request.FeaturePath)); err != nil {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("filesystem effects completed but ownership state publication stopped: %w", err)),
					featureTx, worktreeTx)
			}
		}
		if err := revalidateManagedTemplateFileChecks(plan, featureRoot, true); err != nil {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("unwritten managed content changed before ledger publication: %w", err)),
				featureTx, worktreeTx)
		}
		if err := revalidateManagedOwnedLinks(plan); err != nil {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("managed link ownership changed before ledger publication: %w", err)),
				featureTx, worktreeTx)
		}
		if err := validateManagedOwnedSourceFiles(
			plan.Request.FeaturePath, plan.ownedSourceChecks,
		); err != nil {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("managed source changed before ledger publication: %w", err)),
				featureTx, worktreeTx)
		}
		if err := revalidateManagedRetiredLinks(plan); err != nil {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("retired managed link ownership changed before ledger publication: %w", err)),
				featureTx, worktreeTx)
		}
		if plan.StateBefore.Exists {
			if err := featureTx.preserveFile(managedTemplateStateName, plan.StateBefore); err != nil {
				return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
					fmt.Errorf("preserving ownership state failed: %w", err)), featureTx, worktreeTx)
			}

			if ManagedTemplateApplyStepHook != nil {
				if err := ManagedTemplateApplyStepHook(
					"after-state-preserve", ManagedTemplateStatePath(plan.Request.FeaturePath),
				); err != nil {
					return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
						fmt.Errorf("ownership state was preserved but replacement publication stopped: %w", err)),
						featureTx, worktreeTx)
				}
			}
		}
		if err := featureTx.publish(stagedState, managedTemplateStateName); err != nil {
			return managedPendingApplyError(managedTemplateProgressError(completed, totalEffects,
				fmt.Errorf("publishing managed template ownership state: %w", err)), featureTx, worktreeTx)
		}
	}

	worktreeRoots := make([]string, 0, len(worktreeTx))
	for root := range worktreeTx {
		worktreeRoots = append(worktreeRoots, root)
	}
	sort.Strings(worktreeRoots)
	cleanupTxs := make([]*managedRecoveryRoot, 0, len(worktreeRoots)+1)
	for _, root := range worktreeRoots {
		cleanupTxs = append(cleanupTxs, worktreeTx[root])
	}
	if featureTx != nil {
		cleanupTxs = append(cleanupTxs, featureTx)
	}
	for _, tx := range cleanupTxs {
		if err := tx.preflightCleanup(); err != nil {
			return managedPendingApplyError(fmt.Errorf("managed template update succeeded but cleanup failed: %w", err),
				featureTx, worktreeTx)
		}
	}
	for _, tx := range cleanupTxs {
		if err := tx.cleanupVerified(); err != nil {
			return managedPendingApplyError(fmt.Errorf("managed template update succeeded but cleanup failed: %w", err),
				featureTx, worktreeTx)
		}
	}
	return nil
}

func revalidateManagedOwnedLinks(plan *ManagedTemplatePlan) error {
	worktreeSelected := false
	for _, role := range plan.Request.Roles {
		worktreeSelected = worktreeSelected || role == ManagedTemplateRoleWorktree
	}
	if !worktreeSelected {
		// An unselected role is copied verbatim, not re-adopted. Its worktrees
		// intentionally have no observations in an orchestrator-only plan.
		return nil
	}
	role, ok := plan.NewState.Roles[string(ManagedTemplateRoleWorktree)]
	if !ok || len(role.Links) == 0 {
		return nil
	}
	targets := make(map[string]injectWorktreeTarget, len(plan.worktreeChecks))
	for _, target := range plan.worktreeChecks {
		targets[target.Name] = target
	}
	for _, link := range role.Links {
		target, ok := targets[link.Worktree]
		if !ok {
			return fmt.Errorf("recorded worktree %q is no longer in the pinned plan", link.Worktree)
		}
		tracked, err := managedGitTracked(target.Path, link.Path)
		if err != nil {
			return err
		}
		if tracked {
			return fmt.Errorf("worktree %q destination %s is tracked by Git",
				link.Worktree, filepath.Join(target.Path, filepath.FromSlash(link.Path)))
		}
		observation, err := observeManagedLink(target.Path, link.Path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target.Path, filepath.FromSlash(link.Path))
		expectedTarget, err := filepath.Rel(
			filepath.Dir(destination),
			filepath.Join(InjectPath(plan.Request.FeaturePath), filepath.FromSlash(link.Source)),
		)
		if err != nil {
			return err
		}
		if err := matchOwnedLink(
			observation, link, plan.worktreeIdentities[link.Worktree],
			link.Source, expectedTarget,
		); err != nil {
			return fmt.Errorf("worktree %q destination %s: %w", link.Worktree, destination, err)
		}
	}
	return nil
}

func revalidateManagedRetiredLinks(plan *ManagedTemplatePlan) error {
	for _, retired := range plan.retiredLinks {
		tracked, err := managedGitTracked(retired.worktreeRoot, retired.owned.Path)
		if err != nil {
			return err
		}
		if tracked {
			return fmt.Errorf("worktree %q destination %s is tracked by Git",
				retired.owned.Worktree,
				filepath.Join(retired.worktreeRoot, filepath.FromSlash(retired.owned.Path)))
		}
		observation, err := observeManagedLink(retired.worktreeRoot, retired.owned.Path)
		if err != nil {
			return err
		}
		if observation != retired.before {
			return fmt.Errorf("worktree %q destination %s changed after retirement planning",
				retired.owned.Worktree,
				filepath.Join(retired.worktreeRoot, filepath.FromSlash(retired.owned.Path)))
		}
		if err := matchOwnedLink(
			observation,
			retired.owned,
			plan.worktreeIdentities[retired.owned.Worktree],
			retired.owned.Source,
			retired.owned.Target,
		); err != nil {
			return fmt.Errorf("worktree %q destination %s: %w",
				retired.owned.Worktree,
				filepath.Join(retired.worktreeRoot, filepath.FromSlash(retired.owned.Path)),
				err)
		}
	}
	return nil
}

func managedFeatureRelative(role ManagedTemplateRole, relative string) string {
	if role == ManagedTemplateRoleWorktree {
		return filepath.Join(InjectDir, filepath.FromSlash(relative))
	}
	return filepath.FromSlash(relative)
}

func setManagedPlannedLinkIdentity(
	state *ManagedTemplateState,
	worktree, path string,
	observation managedLinkObservation,
	worktreeIdentity managedWorktreeIdentity,
) error {
	role := state.Roles[string(ManagedTemplateRoleWorktree)]
	for i := range role.Links {
		if role.Links[i].Worktree == worktree && role.Links[i].Path == path {
			role.Links[i].Device = observation.Device
			role.Links[i].Inode = observation.Inode
			role.Links[i].WorktreeDevice = worktreeIdentity.WorktreeDevice
			role.Links[i].WorktreeInode = worktreeIdentity.WorktreeInode
			role.Links[i].GitDirDevice = worktreeIdentity.GitDirDevice
			role.Links[i].GitDirInode = worktreeIdentity.GitDirInode
			state.Roles[string(ManagedTemplateRoleWorktree)] = role
			return nil
		}
	}
	return fmt.Errorf("planned managed link %s/%s is missing from generated ownership state", worktree, path)
}

func managedPendingApplyError(
	cause error,
	feature *managedRecoveryRoot,
	worktrees map[string]*managedRecoveryRoot,
) error {
	var paths []string
	if feature != nil && !feature.cleaned {
		paths = append(paths, feature.pendingEvidencePath())
	}
	for _, tx := range worktrees {
		if !tx.cleaned {
			paths = append(paths, tx.pendingEvidencePath())
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return cause
	}
	return fmt.Errorf("%w; preserved pending evidence: %s", cause, joinManagedPaths(paths))
}

func (tx *managedRecoveryRoot) pendingEvidencePath() string {
	journal := filepath.Join(tx.displayPath, tx.journal)
	if _, err := os.Lstat(journal); err == nil {
		return journal
	}
	return filepath.Join(tx.displayPath, managedTemplateRecoveryName)
}

func joinManagedPaths(paths []string) string {
	result := ""
	for i, path := range paths {
		if i > 0 {
			result += ", "
		}
		result += path
	}
	return result
}
