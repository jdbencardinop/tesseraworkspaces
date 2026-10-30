package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type featureRenameEvent struct {
	Version   int                     `json:"version"`
	Sequence  int                     `json:"sequence"`
	PlanHash  string                  `json:"plan_hash"`
	Effect    string                  `json:"effect"`
	Phase     string                  `json:"phase"`
	Directory *featureRenameDirectory `json:"directory,omitempty"`
	File      *managedFileObservation `json:"file,omitempty"`
}

type featureRenameOperation struct {
	plan         *featureRenamePlan
	journal      featureRenameFile
	events       []featureRenameEvent
	eventFiles   []featureRenameFile
	admission    *ExternalFeatureAdmission
	checkSession func(feature, path string, names []string) error
}

func featureRenameWriteExclusive(binding featureRenameDirectory, name string, data []byte, mode fs.FileMode) error {
	if name != filepath.Base(name) || name == "." || name == ".." {
		return errors.New("unsafe feature rename private filename")
	}
	parent, err := featureRenamePinnedRoot(binding)
	if err != nil {
		return err
	}
	defer parent.Close() //nolint:errcheck
	file, err := parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	chmodErr := file.Chmod(mode)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, chmodErr, syncErr, closeErr, managedSyncRootDir(parent, "."), binding.verify())
}

func newFeatureRenameOperation(plan *featureRenamePlan) (_ *featureRenameOperation, retErr error) {
	if err := plan.validateBound(plan.Root.Path, plan.ID, "", false); err != nil {
		return nil, fmt.Errorf("invalid feature rename plan; no journal published: %w", err)
	}
	if err := plan.validateRawCommonRecords(); err != nil {
		return nil, fmt.Errorf("invalid feature rename Git binding; no journal published: %w", err)
	}
	createdPath := ""
	defer func() {
		if retErr != nil && createdPath != "" {
			retErr = fmt.Errorf("feature rename preparation interrupted before movement; preserve evidence at %s: %w", createdPath, retErr)
		}
	}()
	guard, err := lockFeatureRenameJournals(plan.Root.Path, true, true)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, guard.Release()) }()
	root, err := featureRenamePinnedRoot(plan.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close() //nolint:errcheck
	parentName := filepath.Join(workspaceMarker, featureRenameJournalDir)
	dir, _, err := managedOpenPinnedDirectory(root, parentName, true)
	if err != nil {
		return nil, err
	}
	if err := dir.Close(); err != nil {
		return nil, err
	}
	operationName := filepath.Join(parentName, plan.ID)
	preparationName := filepath.Join(workspaceMarker, ".feature-rename-preparation-"+plan.ID)
	if err := root.Mkdir(preparationName, 0o700); err != nil {
		return nil, err
	}
	createdPath = filepath.Join(plan.Root.Path, preparationName)
	if err := managedSyncRootDir(root, workspaceMarker); err != nil {
		return nil, err
	}
	prepared, err := featureRenameDirectoryAt(createdPath)
	if err != nil {
		return nil, err
	}
	plan.Operation.Identity = prepared.Identity
	for _, path := range plan.parentPaths() {
		if path == plan.Operation.Path {
			plan.Parents = append(plan.Parents, plan.Operation)
			continue
		}
		directory, err := featureRenameDirectoryAt(path)
		if err != nil {
			return nil, err
		}
		plan.Parents = append(plan.Parents, directory)
	}
	if err := plan.validateBound(plan.Root.Path, plan.ID, prepared.Path, true); err != nil {
		return nil, fmt.Errorf("validating unpublished feature rename evidence: %w", err)
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := featureRenameWriteExclusive(prepared, "journal.json", append(data, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("feature rename preparation interrupted; evidence at %s: %w", prepared.Path, err)
	}
	if err := managedRenameNoReplace(root, preparationName, operationName); err != nil {
		return nil, fmt.Errorf("publishing feature rename journal (prepared=%s final=%s): %w", prepared.Path, plan.Operation.Path, err)
	}
	createdPath = plan.Operation.Path
	return loadFeatureRename(plan.Root.Path, plan.ID)
}

func loadFeatureRename(root, id string) (*featureRenameOperation, error) {
	if !markerIDRegexp.MatchString(id) {
		return nil, errors.New("invalid feature rename operation identifier")
	}
	path := filepath.Join(featureRenameJournalRoot(root), id)
	journal, err := featureRenameReadFile(filepath.Join(path, "journal.json"), true)
	if err != nil {
		return nil, fmt.Errorf("unreadable feature rename journal at %s: %w", path, err)
	}
	plan := new(featureRenamePlan)
	if err := featureRenameReadJSON(journal.Data, plan); err != nil {
		return nil, fmt.Errorf("malformed feature rename journal at %s: %w", path, err)
	}
	if err := plan.validateBound(root, id, plan.Operation.Path, true); err != nil {
		return nil, fmt.Errorf("untrusted feature rename journal at %s: %w", path, err)
	}
	tx := &featureRenameOperation{plan: plan, journal: journal}
	pinned, err := featureRenameOpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer pinned.Close() //nolint:errcheck
	entries, err := fs.ReadDir(pinned.FS(), ".")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == "journal.json" {
			continue
		}
		want := fmt.Sprintf("%06d.json", len(tx.events))
		if entry.Name() != want {
			return nil, fmt.Errorf("unexpected feature rename evidence at %s (expected %s); evidence retained",
				filepath.Join(path, entry.Name()), want)
		}
		file, err := featureRenameReadFile(filepath.Join(path, entry.Name()), true)
		if err != nil {
			return nil, err
		}
		var event featureRenameEvent
		if err := featureRenameReadJSON(file.Data, &event); err != nil {
			return nil, err
		}
		if err := tx.validateEvent(event); err != nil {
			return nil, fmt.Errorf("invalid rename progress at %s: %w", file.Path, err)
		}
		tx.events = append(tx.events, event)
		tx.eventFiles = append(tx.eventFiles, file)
	}
	// Fully validated cleaned receipts grant no recovery authority. Their
	// former Git paths may legitimately disappear during later lifecycle work;
	// only an active operation requires the additional live raw-path proof.
	if !tx.has("cleaned", "done") {
		if err := plan.validateRawCommonRecords(); err != nil {
			return nil, fmt.Errorf("untrusted feature rename Git binding at %s: %w", path, err)
		}
	}
	return tx, nil
}

func (plan *featureRenamePlan) validate(root, id string) error {
	if err := plan.validateBound(root, id, plan.Operation.Path, true); err != nil {
		return err
	}
	return plan.validateRawCommonRecords()
}

func (plan *featureRenamePlan) validateBound(root, id, operationPath string, metadataReady bool) error {
	if plan.Version != featureRenameVersion {
		return fmt.Errorf("unsupported feature rename journal version %d", plan.Version)
	}
	for _, name := range []string{plan.OldName, plan.NewName} {
		if strings.ContainsRune(name, 0) {
			return errors.New("feature rename names contain NUL")
		}
		if err := validateFeatureName(name); err != nil {
			return err
		}
	}
	if plan.OldName == plan.NewName || plan.ID != id || !markerIDRegexp.MatchString(id) ||
		plan.Root.Path != root || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		plan.Feature.Path != filepath.Join(root, plan.OldName) ||
		plan.Operation.Path != filepath.Join(featureRenameJournalRoot(root), id) ||
		plan.GuardToken == "" {
		return errors.New("feature rename journal names, workspace, or operation binding do not match")
	}
	directories := []featureRenameDirectory{plan.Root, plan.Repository, plan.Common, plan.Feature}
	if metadataReady {
		directories = append(directories, plan.Operation)
	} else if plan.Operation.Identity != (managedObjectIdentity{}) || len(plan.Parents) != 0 {
		return errors.New("unexpected runtime evidence in unpublished rename plan")
	}
	for _, dir := range directories {
		if !filepath.IsAbs(dir.Path) || filepath.Clean(dir.Path) != dir.Path ||
			dir.Identity == (managedObjectIdentity{}) {
			return errors.New("invalid feature rename directory evidence")
		}
	}
	if err := plan.Root.verify(); err != nil {
		return err
	}
	if metadataReady {
		operation := plan.Operation
		operation.Path = operationPath
		if err := operation.verify(); err != nil {
			return err
		}
		parentPaths := plan.parentPaths()
		if len(plan.Parents) != len(parentPaths) {
			return errors.New("incomplete feature rename parent-directory evidence")
		}
		for i, path := range parentPaths {
			if plan.Parents[i].Path != path || plan.Parents[i].Identity == (managedObjectIdentity{}) {
				return errors.New("invalid feature rename parent-directory evidence")
			}
		}
	}
	parentIdentities := make(map[string]managedObjectIdentity, len(plan.Parents))
	for _, directory := range plan.Parents {
		parentIdentities[directory.Path] = directory.Identity
	}
	bindings := append([]featureRenameDirectory(nil), directories...)
	commonDirectories := make(map[string]featureRenameDirectory, len(plan.Registries))
	for _, common := range plan.Registries {
		if _, duplicate := commonDirectories[common.Path]; duplicate {
			return errors.New("duplicate feature rename repository binding")
		}
		commonDirectories[common.Path] = common
		bindings = append(bindings, common)
	}
	if commonDirectories[plan.Common.Path] != plan.Common {
		return errors.New("feature rename inventory omits the controlling repository")
	}
	for _, tree := range plan.Worktrees {
		bindings = append(bindings, tree.Directory, tree.Repository, tree.Common, tree.Admin)
		if commonDirectories[tree.Common.Path] != tree.Common {
			return errors.New("feature rename inventory omits an affected repository")
		}
		if len(tree.Index.Data) != 0 {
			return errors.New("unexpected data in feature rename index observation")
		}
	}
	for _, binding := range bindings {
		if !filepath.IsAbs(binding.Path) || filepath.Clean(binding.Path) != binding.Path ||
			binding.Identity == (managedObjectIdentity{}) || metadataReady && parentIdentities[binding.Path] != binding.Identity {
			return errors.New("inconsistent feature rename directory identities")
		}
	}
	if plan.Registry.Path != SpacesFilePath(root) ||
		featureRenameInside(plan.Feature.Path, plan.Repository.Path) ||
		featureRenameInside(plan.Feature.Path, plan.Common.Path) {
		return errors.New("unsafe feature rename repository or registry binding")
	}
	if err := featureRenameValidateImage(plan.Registry); err != nil {
		return err
	}
	if len(plan.Metadata) != 2 || plan.Metadata[0].Path != filepath.Join(plan.Feature.Path, "stack.yaml") ||
		plan.Metadata[1].Path != filepath.Join(plan.Feature.Path, managedTemplateStateName) {
		return errors.New("unexpected feature rename metadata inventory")
	}
	for _, file := range plan.Metadata {
		if err := featureRenameValidateImage(file); err != nil {
			return err
		}
	}
	ownedPaths, err := featureRenameOwnedPaths(plan.Metadata[1].Data)
	if err != nil || len(ownedPaths) != len(plan.Owned) {
		return errors.Join(err, errors.New("managed ownership evidence set changed"))
	}
	for i, path := range ownedPaths {
		if plan.Owned[i].Relative != path {
			return errors.New("unexpected managed ownership evidence path")
		}
	}
	var stack Stack
	if plan.Metadata[0].Observation.Exists {
		if err := yaml.Unmarshal(plan.Metadata[0].Data, &stack); err != nil {
			return err
		}
		names := map[string]bool{}
		for _, entry := range stack.Branches {
			if _, err := injectWorktreePath(plan.Feature.Path, entry.Name); err != nil {
				return err
			}
			if names[entry.Name] {
				return errors.New("duplicate names in frozen feature rename stack")
			}
			names[entry.Name] = true
		}
	}
	if err := plan.validateRootAliases(stack); err != nil {
		return err
	}
	seenPath := map[string]bool{}
	seenAdmin := map[managedObjectIdentity]bool{}
	expectedPointers := make([]featureRenamePointer, 0, len(plan.Worktrees)*2+1)
	for i, tree := range plan.Worktrees {
		path, err := injectWorktreePath(plan.Feature.Path, tree.Name)
		if err != nil {
			return err
		}
		if plan.Metadata[0].Observation.Exists {
			entry := GetBranch(stack, tree.Name)
			repo := entry.Repo
			if repo == "" {
				repo = plan.Repository.Path
			}
			if entry.Name == "" || entry.Archived ||
				tree.Branch != "refs/heads/"+entry.GitBranch() || featureRenameMappedPath(repo, plan.Aliases) != tree.Repository.Path {
				return errors.New("feature rename worktree set does not match the frozen stack")
			}
		}
		if tree.Directory.Path != path || seenPath[path] || seenAdmin[tree.Admin.Identity] ||
			filepath.Dir(tree.Admin.Path) != filepath.Join(tree.Common.Path, "worktrees") ||
			featureRenameInside(plan.Feature.Path, tree.Admin.Path) ||
			featureRenameInside(plan.Feature.Path, tree.Repository.Path) ||
			tree.Marker.Path != filepath.Join(path, ".git") ||
			tree.Backpointer.Path != filepath.Join(tree.Admin.Path, "gitdir") ||
			tree.CommonFile.Path != filepath.Join(tree.Admin.Path, "commondir") ||
			tree.Head.Path != filepath.Join(tree.Admin.Path, "HEAD") ||
			tree.Index.Path != filepath.Join(tree.Admin.Path, "index") || tree.OID == "" || tree.ConfigHash == "" {
			return errors.New("invalid feature rename Git relationship")
		}
		for previous := range seenPath {
			if featureRenameInside(previous, path) || featureRenameInside(path, previous) {
				return errors.New("overlapping feature rename worktrees")
			}
		}
		seenPath[path], seenAdmin[tree.Admin.Identity] = true, true
		for _, file := range []featureRenameFile{tree.Marker, tree.Backpointer, tree.CommonFile, tree.Head} {
			if err := featureRenameValidateImage(file); err != nil || !file.Observation.Exists {
				return errors.Join(err, errors.New("missing Git pointer preimage"))
			}
		}
		if err := plan.validateStationaryRecord(tree.Marker, "gitdir: ", path, false); err != nil {
			return err
		}
		originalBack, err := featureRenameRecordBinding(tree.Backpointer.Data, "", tree.Admin.Path)
		if err != nil || featureRenameMappedPath(originalBack, plan.Aliases) != tree.Marker.Path {
			return errors.Join(err, errors.New("backpointer preimage does not name selected marker"))
		}
		originalCommon, err := featureRenameRecordBinding(tree.CommonFile.Data, "", tree.Admin.Path)
		if err != nil || featureRenameMappedPath(originalCommon, plan.Aliases) != tree.Common.Path {
			return errors.Join(err, errors.New("commondir preimage does not name selected repository"))
		}
		if err := plan.validateStationaryRecord(tree.CommonFile, "", tree.Admin.Path, false); err != nil {
			return err
		}
		newPath := filepath.Join(root, plan.NewName, "worktrees", tree.Name)
		forward, err := featureRenamePointerImage(tree.Marker.Data, "gitdir: ", path, newPath, tree.Admin.Path, plan.Aliases)
		if err != nil {
			return err
		}
		back, err := featureRenamePointerImage(tree.Backpointer.Data, "", tree.Admin.Path, tree.Admin.Path, filepath.Join(newPath, ".git"), plan.Aliases)
		if err != nil {
			return err
		}
		if !bytes.Equal(forward, tree.Marker.Data) {
			expectedPointers = append(expectedPointers, featureRenamePointer{Kind: "forward", Worktree: i, Before: tree.Marker, After: forward})
		}
		expectedPointers = append(expectedPointers, featureRenamePointer{Kind: "backpointer", Worktree: i, Before: tree.Backpointer, After: back})
	}
	if len(plan.Pointers) == len(expectedPointers)+1 {
		pointer := plan.Pointers[len(plan.Pointers)-1]
		if pointer.Kind != "registry" || pointer.Worktree != -1 || !plan.Registry.Observation.Exists ||
			pointer.Before.Path != plan.Registry.Path || !bytes.Equal(pointer.Before.Data, plan.Registry.Data) ||
			pointer.Before.Observation != plan.Registry.Observation {
			return errors.New("invalid registry rewrite evidence")
		}
		before, err := decodeSpaces(plan.Registry.Data, plan.Registry.Path)
		if err != nil {
			return err
		}
		after, err := decodeSpaces(pointer.After, plan.Registry.Path)
		if err != nil {
			return err
		}
		if err := featureRenameValidateRegistry(before, after, plan.OldName, plan.NewName); err != nil {
			return err
		}
		expectedPointers = append(expectedPointers, pointer)
	}
	if len(expectedPointers) != len(plan.Pointers) {
		return errors.New("feature rename pointer set is incomplete or expanded")
	}
	for i, expected := range expectedPointers {
		got := plan.Pointers[i]
		parent := root
		if expected.Kind == "backpointer" {
			parent = plan.Worktrees[expected.Worktree].Admin.Path
		}
		wantStage := filepath.Join(parent, ".tws-feature-rename-"+id+fmt.Sprintf("-%d", i))
		if got.Kind != expected.Kind || got.Worktree != expected.Worktree ||
			got.StageRoot != wantStage || got.Before.Path != expected.Before.Path ||
			got.Before.Observation != expected.Before.Observation ||
			!bytes.Equal(got.Before.Data, expected.Before.Data) || !bytes.Equal(got.After, expected.After) {
			return errors.New("feature rename pointer intent does not match verified roots")
		}
	}
	return nil
}

func featureRenameValidateImage(file featureRenameFile) error {
	if !file.Observation.Exists {
		if file.Observation != (managedFileObservation{}) || len(file.Data) != 0 {
			return errors.New("invalid absent feature rename preimage")
		}
		return nil
	}
	if file.Observation.Kind != "file" || file.Observation.Inode == 0 ||
		file.Observation.Hash != digestBytes(file.Data) || file.Observation.Mode&^uint32(0o777) != 0 {
		return errors.New("invalid regular feature rename preimage")
	}
	return nil
}

func featureRenameRecordBinding(data []byte, prefix, base string) (string, error) {
	value := bytes.TrimSuffix(data, []byte("\n"))
	value = bytes.TrimSuffix(value, []byte("\r"))
	path, ok := strings.CutPrefix(string(value), prefix)
	if !ok || path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("invalid Git pointer record")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path), nil
}

func (plan *featureRenamePlan) validateRawCommonRecords() error {
	for _, tree := range plan.Worktrees {
		if err := plan.validateStationaryRecord(tree.CommonFile, "", tree.Admin.Path, true); err != nil {
			return err
		}
	}
	return nil
}

func (plan *featureRenamePlan) validateStationaryRecord(file featureRenameFile, prefix, base string, inspect bool) error {
	lexical, err := featureRenameRecordBinding(file.Data, prefix, base)
	if err != nil {
		return err
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(file.Data), "\n"), "\r")
	value = strings.TrimPrefix(value, prefix)
	var movingRoots []string
	if prefix == "" {
		// commondir is stationary. A forward preimage can traverse the old
		// name because its exact safe replacement is a journaled effect.
		movingRoots = []string{plan.Feature.Path, filepath.Join(plan.Root.Path, plan.NewName)}
	}
	raw, err := featureRenameRawDirectoryBinding(base, value, plan.Aliases, inspect, movingRoots...)
	if err != nil {
		return fmt.Errorf("unsupported relocation-dependent Git record at %s: %w", file.Path, err)
	}
	if raw != featureRenameMappedPath(lexical, plan.Aliases) {
		return fmt.Errorf("raw Git record at %s does not bind its verified directory", file.Path)
	}
	return nil
}

func featureRenameValidateRegistry(before, after *SpacesFile, oldName, newName string) error {
	if len(before.Spaces) != len(after.Spaces) {
		return errors.New("feature rename registry entry set changed")
	}
	expected := &SpacesFile{Version: before.Version, Spaces: append([]SpaceEntry(nil), before.Spaces...)}
	for i := range expected.Spaces {
		entry := &expected.Spaces[i]
		changed := false
		if entry.Feature == oldName {
			entry.Feature, changed = newName, true
		}
		if !filepath.IsAbs(entry.Path) {
			if path, ok := rewriteSpaceRelPath(entry.Path, oldName, newName); ok {
				entry.Path, changed = path, true
			}
		}
		if changed {
			// Only the changed entries may receive the transaction's timestamp.
			for _, candidate := range after.Spaces {
				if candidate.Name == entry.Name && candidate.Feature == entry.Feature {
					entry.UpdatedAt = candidate.UpdatedAt
				}
			}
		}
	}
	sortSpaces(expected)
	a, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	b, err := json.Marshal(after)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return errors.New("feature rename registry postimage is not the selected semantic rewrite")
	}
	return nil
}

func (tx *featureRenameOperation) has(effect, phase string) bool {
	for _, event := range tx.events {
		if event.Effect == effect && event.Phase == phase {
			return true
		}
	}
	return false
}

func (tx *featureRenameOperation) stage(index int) *featureRenameEvent {
	effect := fmt.Sprintf("stage.%d", index)
	for i := range tx.events {
		if tx.events[i].Effect == effect && tx.events[i].Phase == "done" {
			return &tx.events[i]
		}
	}
	return nil
}

func (tx *featureRenameOperation) validateEvent(event featureRenameEvent) error {
	if event.Version != featureRenameVersion || event.Sequence != len(tx.events) ||
		event.PlanHash != tx.journal.Observation.Hash ||
		(event.Phase != "intent" && event.Phase != "done") ||
		tx.has(event.Effect, event.Phase) {
		return errors.New("invalid or duplicate feature rename event")
	}
	if event.Phase == "done" && !tx.has(event.Effect, "intent") {
		return errors.New("feature rename effect has no durable intent")
	}
	effect := event.Effect
	index := -1
	if kind, number, found := strings.Cut(effect, "."); found {
		var err error
		index, err = strconv.Atoi(number)
		if err != nil || index < 0 || index >= len(tx.plan.Pointers) || strconv.Itoa(index) != number {
			return errors.New("invalid feature rename pointer effect")
		}
		switch kind {
		case "stage", "preserve", "publish", "unpublish", "restore", "cleanup":
			effect = kind
		default:
			return errors.New("unknown feature rename pointer effect")
		}
	} else {
		switch effect {
		case "move", "abort", "move-back", "complete", "aborted", "cleaned":
		default:
			return errors.New("unknown feature rename effect")
		}
	}
	if tx.has("cleaned", "done") {
		return errors.New("feature rename evidence follows cleanup completion")
	}
	if effect == "stage" && event.Phase == "done" {
		if event.Directory == nil || event.File == nil ||
			event.Directory.Path != tx.plan.Pointers[index].StageRoot ||
			event.Directory.Identity == (managedObjectIdentity{}) || !event.File.Exists ||
			event.File.Kind != "file" || event.File.Inode == 0 ||
			event.File.Hash != digestBytes(tx.plan.Pointers[index].After) ||
			event.File.Mode != tx.plan.Pointers[index].Before.Observation.Mode {
			return errors.New("invalid staged pointer evidence")
		}
	} else if event.Directory != nil || event.File != nil {
		return errors.New("unexpected filesystem authority in feature rename event")
	}
	terminal := tx.has("complete", "done") || tx.has("aborted", "done")
	switch effect {
	case "cleanup", "cleaned":
		if !terminal {
			return errors.New("feature rename cleanup precedes durable completion")
		}
	case "abort", "unpublish", "restore", "move-back", "aborted":
		if tx.has("complete", "done") || terminal {
			return errors.New("feature rename abort follows completion")
		}
		if effect != "abort" && !tx.has("abort", "intent") {
			return errors.New("feature rename rollback has no intent")
		}
	default:
		if terminal || tx.has("abort", "intent") {
			return errors.New("forward effect follows rollback/completion")
		}
	}
	if effect == "complete" {
		if !tx.has("move", "done") {
			return errors.New("completion precedes feature movement")
		}
		for i := range tx.plan.Pointers {
			if !tx.has(fmt.Sprintf("publish.%d", i), "done") {
				return errors.New("completion precedes pointer/registry publication")
			}
		}
	}
	switch effect {
	case "stage":
		if tx.has("move", "intent") || (index > 0 && !tx.has(fmt.Sprintf("stage.%d", index-1), "done")) {
			return errors.New("staging is out of the frozen effect order")
		}
	case "move":
		for i := range tx.plan.Pointers {
			if !tx.has(fmt.Sprintf("stage.%d", i), "done") {
				return errors.New("feature movement precedes pointer staging")
			}
		}
	case "preserve", "publish":
		if !tx.has("move", "done") || !tx.has(fmt.Sprintf("stage.%d", index), "done") ||
			(index > 0 && !tx.has(fmt.Sprintf("publish.%d", index-1), "done")) ||
			(effect == "publish" && !tx.has(fmt.Sprintf("preserve.%d", index), "done")) {
			return errors.New("pointer effect is out of the frozen effect order")
		}
	case "restore":
		if !tx.has(fmt.Sprintf("unpublish.%d", index), "done") {
			return errors.New("restore precedes owned postimage preservation")
		}
	case "move-back":
		for i := range tx.plan.Pointers {
			if tx.stage(i) != nil && !tx.has(fmt.Sprintf("restore.%d", i), "done") {
				return errors.New("directory rollback precedes pointer/registry restoration")
			}
		}
	case "aborted":
		if !tx.has("move-back", "done") {
			return errors.New("abort completion precedes directory restoration")
		}
	case "cleaned":
		for i := range tx.plan.Pointers {
			if tx.stage(i) != nil && !tx.has(fmt.Sprintf("cleanup.%d", i), "done") {
				return errors.New("cleanup completion precedes preserved-object cleanup")
			}
		}
	}
	return nil
}

func (tx *featureRenameOperation) verifyJournal() error {
	if err := tx.admission.verify(); err != nil {
		return err
	}
	if err := tx.plan.Root.verify(); err != nil {
		return err
	}
	if err := tx.plan.Operation.verify(); err != nil {
		return err
	}
	if err := tx.journal.verifyAt(tx.journal.Path); err != nil {
		return err
	}
	for _, file := range tx.eventFiles {
		if err := file.verifyAt(file.Path); err != nil {
			return err
		}
	}
	root, err := featureRenameOpenRoot(tx.plan.Operation.Path)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) != len(tx.events)+1 {
		return fmt.Errorf("unexpected feature rename residue at %s; evidence retained", tx.plan.Operation.Path)
	}
	return nil
}

func (tx *featureRenameOperation) record(effect, phase string, stage *featureRenameEvent) (retErr error) {
	if tx.has(effect, phase) {
		return tx.verifyJournal()
	}
	if err := tx.verifyJournal(); err != nil {
		return err
	}
	guard, err := lockFeatureRenameJournals(tx.plan.Root.Path, true, false)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, guard.Release()) }()
	event := featureRenameEvent{
		Version: featureRenameVersion, Sequence: len(tx.events),
		PlanHash: tx.journal.Observation.Hash, Effect: effect, Phase: phase,
	}
	if stage != nil {
		event.Directory, event.File = stage.Directory, stage.File
	}
	if err := tx.validateEvent(event); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	path := filepath.Join(tx.plan.Operation.Path, fmt.Sprintf("%06d.json", event.Sequence))
	if err := featureRenameWriteExclusive(tx.plan.Operation, filepath.Base(path), append(data, '\n'), 0o600); err != nil {
		// A returned sync/close error does not mean the record is absent.
		// Do not attempt rollback from an in-memory guess about completion.
		return errors.Join(ErrFeatureRenameInterrupted, fmt.Errorf("persisting %s at %s: %w", effect, path, err))
	}
	file, err := featureRenameReadFile(path, true)
	if err != nil {
		return errors.Join(ErrFeatureRenameInterrupted, err)
	}
	tx.events = append(tx.events, event)
	tx.eventFiles = append(tx.eventFiles, file)
	return nil
}
