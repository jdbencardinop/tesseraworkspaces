package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const featureRenameVersion = 1

type featureRenameDirectory struct {
	Path     string                `json:"path"`
	Identity managedObjectIdentity `json:"identity"`
}

type featureRenameFile struct {
	Path        string                 `json:"path"`
	Observation managedFileObservation `json:"observation"`
	Data        []byte                 `json:"data,omitempty"`
}

type featureRenameWorktree struct {
	Name        string                 `json:"name"`
	Branch      string                 `json:"branch"`
	OID         string                 `json:"oid"`
	Directory   featureRenameDirectory `json:"directory"`
	Repository  featureRenameDirectory `json:"repository"`
	Common      featureRenameDirectory `json:"common"`
	Admin       featureRenameDirectory `json:"admin"`
	Marker      featureRenameFile      `json:"marker"`
	Backpointer featureRenameFile      `json:"backpointer"`
	CommonFile  featureRenameFile      `json:"common_file"`
	Head        featureRenameFile      `json:"head"`
	Index       featureRenameFile      `json:"index"`
	ConfigHash  string                 `json:"config_hash"`
}

type featureRenamePointer struct {
	Kind      string            `json:"kind"`
	Worktree  int               `json:"worktree"`
	Before    featureRenameFile `json:"before"`
	After     []byte            `json:"after"`
	StageRoot string            `json:"stage_root"`
}

type featureRenameOwnedEvidence struct {
	Relative string                 `json:"relative"`
	File     managedFileObservation `json:"file"`
	Link     managedLinkObservation `json:"link"`
}

type featureRenamePlan struct {
	Version    int                          `json:"version"`
	ID         string                       `json:"id"`
	OldName    string                       `json:"old_name"`
	NewName    string                       `json:"new_name"`
	Root       featureRenameDirectory       `json:"root"`
	Repository featureRenameDirectory       `json:"repository"`
	Common     featureRenameDirectory       `json:"common"`
	Feature    featureRenameDirectory       `json:"feature"`
	Operation  featureRenameDirectory       `json:"operation"`
	Parents    []featureRenameDirectory     `json:"parents"`
	Aliases    []featureRenameRootAlias     `json:"root_aliases,omitempty"`
	GuardToken string                       `json:"guard_token"`
	Worktrees  []featureRenameWorktree      `json:"worktrees"`
	Pointers   []featureRenamePointer       `json:"pointers"`
	Metadata   []featureRenameFile          `json:"metadata"`
	Owned      []featureRenameOwnedEvidence `json:"owned"`
	Registry   featureRenameFile            `json:"registry"`
	Registries []featureRenameDirectory     `json:"repositories"`
}

func featureRenameReadJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing feature rename journal data")
	}
	return nil
}

// Each root is opened from its parent without following a symbolic link. The
// pinned descriptor is used for effects; the public binding is checked again
// before and after them. This is not a portable inode compare-and-swap.
func featureRenameOpenRoot(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("unsafe feature rename root %q", path)
	}
	base, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	defer base.Close() //nolint:errcheck
	relative := strings.TrimPrefix(path, string(filepath.Separator))
	dir, identity, err := managedOpenPinnedDirectory(base, relative, false)
	if err != nil {
		return nil, fmt.Errorf("opening feature rename root %s without symlinks: %w", path, err)
	}
	defer dir.Close() //nolint:errcheck
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	got, err := managedRootIdentity(root)
	if err != nil || got != identity {
		_ = root.Close()
		return nil, errors.Join(err, fmt.Errorf("feature rename root changed at %s", path))
	}
	return root, nil
}

func featureRenameDirectoryAt(path string) (featureRenameDirectory, error) {
	root, err := featureRenameOpenRoot(path)
	if err != nil {
		return featureRenameDirectory{}, err
	}
	defer root.Close() //nolint:errcheck
	identity, err := managedRootIdentity(root)
	return featureRenameDirectory{Path: path, Identity: identity}, err
}

func featureRenamePinnedRoot(binding featureRenameDirectory) (*os.Root, error) {
	root, err := featureRenameOpenRoot(binding.Path)
	if err != nil {
		return nil, err
	}
	identity, err := managedRootIdentity(root)
	if err != nil || identity != binding.Identity {
		_ = root.Close()
		return nil, errors.Join(err, fmt.Errorf("pinned feature rename root changed at %s", binding.Path))
	}
	if err := binding.verify(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func (directory featureRenameDirectory) verify() error {
	got, err := featureRenameDirectoryAt(directory.Path)
	if err != nil {
		return err
	}
	if got != directory {
		return fmt.Errorf("feature rename directory identity changed at %s", directory.Path)
	}
	return nil
}

func featureRenameReadFile(path string, contents bool) (featureRenameFile, error) {
	parent, err := featureRenameOpenRoot(filepath.Dir(path))
	if err != nil {
		return featureRenameFile{}, err
	}
	defer parent.Close() //nolint:errcheck
	name := filepath.Base(path)
	observation, err := observeManagedFileRoot(parent, name)
	if err != nil {
		return featureRenameFile{}, err
	}
	if observation.Exists && observation.Kind != "file" {
		return featureRenameFile{}, fmt.Errorf("feature rename requires a regular file at %s", path)
	}
	file := featureRenameFile{Path: path, Observation: observation}
	if contents && observation.Exists {
		file.Data, err = fs.ReadFile(parent.FS(), name)
		if err != nil {
			return featureRenameFile{}, err
		}
		if digestBytes(file.Data) != observation.Hash {
			return featureRenameFile{}, fmt.Errorf("file changed while reading %s", path)
		}
	}
	return file, nil
}

func (file featureRenameFile) verifyAt(path string) error {
	got, err := featureRenameReadFile(path, false)
	if err != nil {
		return err
	}
	if got.Observation != file.Observation {
		return fmt.Errorf("feature rename file identity/content changed at %s", path)
	}
	return nil
}

func featureRenameGit(dir string, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"--no-optional-locks", "--literal-pathspecs", "-C", dir}, args...)...)
	command.Env = os.Environ()
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	} {
		if os.Getenv(key) != "" {
			return nil, fmt.Errorf("feature rename cannot prove Git bindings with %s set", key)
		}
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("read-only Git validation in %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func featureRenameGitPath(dir string, args ...string) (string, error) {
	out, err := featureRenameGit(dir, args...)
	if err != nil {
		return "", err
	}
	path, err := injectGitPath(out)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path), nil
}

type featureRenameRegistration struct {
	Path   string
	Branch string
	OID    string
	Locked bool
}

func featureRenameInventory(common string) ([]featureRenameRegistration, error) {
	out, err := featureRenameGit(common, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var registrations []featureRenameRegistration
	var current *featureRenameRegistration
	for _, field := range bytes.Split(out, []byte{0}) {
		if len(field) == 0 {
			current = nil
			continue
		}
		value := string(field)
		if path, ok := strings.CutPrefix(value, "worktree "); ok {
			if current != nil || path == "" || !filepath.IsAbs(path) {
				return nil, errors.New("malformed feature rename Git inventory")
			}
			registrations = append(registrations, featureRenameRegistration{Path: filepath.Clean(path)})
			current = &registrations[len(registrations)-1]
			continue
		}
		if current == nil {
			return nil, errors.New("malformed feature rename Git inventory: field without worktree")
		}
		switch {
		case strings.HasPrefix(value, "HEAD "):
			current.OID = strings.TrimPrefix(value, "HEAD ")
		case strings.HasPrefix(value, "branch "):
			current.Branch = strings.TrimPrefix(value, "branch ")
		case value == "locked" || strings.HasPrefix(value, "locked "):
			current.Locked = true
		}
	}
	return registrations, nil
}

func featureRenameInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func featureRenamePointerImage(before []byte, prefix, oldBase, newBase, target string, aliases []featureRenameRootAlias) ([]byte, error) {
	text := before
	ending := ""
	if bytes.HasSuffix(text, []byte("\r\n")) {
		text, ending = text[:len(text)-2], "\r\n"
	} else if bytes.HasSuffix(text, []byte("\n")) {
		text, ending = text[:len(text)-1], "\n"
	}
	value, ok := strings.CutPrefix(string(text), prefix)
	if !ok || value == "" || strings.ContainsRune(value, 0) {
		return nil, errors.New("unsupported Git pointer encoding")
	}
	bound := value
	if !filepath.IsAbs(bound) {
		bound = filepath.Join(oldBase, bound)
	}
	if featureRenameMappedPath(bound, aliases) != target && prefix == "gitdir: " {
		return nil, errors.New("Git forward pointer does not bind the recorded admin directory")
	}
	if prefix == "gitdir: " {
		newBinding := value
		if !filepath.IsAbs(newBinding) {
			newBinding = filepath.Join(newBase, newBinding)
		}
		oldParts, newParts := strings.Split(oldBase, string(filepath.Separator)), strings.Split(newBase, string(filepath.Separator))
		movingRoot := ""
		for i := 0; i < len(oldParts) && i < len(newParts); i++ {
			if oldParts[i] != newParts[i] {
				movingRoot = strings.Join(oldParts[:i+1], string(filepath.Separator))
				break
			}
		}
		if featureRenameMappedPath(newBinding, aliases) == target && !featureRenameRawPointerTouches(newBase, value, movingRoot, aliases) {
			return append([]byte(nil), before...), nil
		}
	}
	if filepath.IsAbs(value) {
		return []byte(prefix + target + ending), nil
	}
	// Retain the exact old spelling when it still resolves correctly.
	if prefix != "gitdir: " && filepath.Clean(filepath.Join(newBase, value)) == target {
		return append([]byte(nil), before...), nil
	}
	relative, err := filepath.Rel(newBase, target)
	if err != nil {
		return nil, err
	}
	return []byte(prefix + relative + ending), nil
}

func featureRenameRawPointerTouches(base, value, movingRoot string, aliases []featureRenameRootAlias) bool {
	if movingRoot == "" {
		return false
	}
	current := base
	if filepath.IsAbs(value) {
		current = string(filepath.Separator)
	}
	for _, part := range strings.Split(value, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if featureRenameInside(movingRoot, featureRenameMappedPath(current, aliases)) {
			return true
		}
	}
	return false
}

func featureRenameConfig(path string) ([]byte, error) {
	data, err := featureRenameGit(path, "config", "--null", "--list")
	if err != nil {
		return nil, err
	}
	for _, field := range bytes.Split(data, []byte{0}) {
		key, value, _ := strings.Cut(string(field), "\n")
		key = strings.ToLower(key)
		switch {
		case key == "core.worktree", key == "core.hookspath", key == "core.excludesfile",
			key == "core.attributesfile", key == "core.fsmonitor", key == "core.sshcommand",
			key == "core.alternaterefscommand", key == "diff.external",
			key == "include.path", strings.HasPrefix(key, "includeif."),
			strings.HasPrefix(key, "filter."), strings.HasSuffix(key, ".driver"),
			strings.HasSuffix(key, ".command"):
			if value == "" || key == "core.fsmonitor" && (value == "false" || value == "0") {
				continue
			}
			return nil, fmt.Errorf("unsupported path-bearing Git configuration %q; feature rename will not rewrite configuration", key)
		}
	}
	return data, nil
}

func featureRenameNoAlternates(common string) error {
	for _, name := range []string{"alternates", "http-alternates"} {
		path := filepath.Join(common, "objects", "info", name)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != 0 {
			return fmt.Errorf("unsupported path-bearing Git alternates at %s; feature rename does not relocate object stores", path)
		}
	}
	return nil
}

func featureRenameIdleGit(admin string) error {
	for _, name := range []string{
		"index.lock", "HEAD.lock", "locked", "rebase-merge", "rebase-apply",
		"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "sequencer",
	} {
		path := filepath.Join(admin, name)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("Git operation or worktree lock at %s blocks feature rename", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func featureRenameScanGit(featurePath string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(featurePath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == ".git" {
			if !entry.Type().IsRegular() {
				return fmt.Errorf("unsupported primary, nested, or symlinked Git marker at %s", path)
			}
			paths = append(paths, filepath.Dir(path))
		}
		if entry.IsDir() {
			// A bare repository has no .git marker.
			if info, err := os.Lstat(filepath.Join(path, "objects")); err == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				if head, err := os.Lstat(filepath.Join(path, "HEAD")); err == nil && (head.Mode().IsRegular() || head.Mode()&os.ModeSymlink != 0) {
					for _, refs := range []string{"refs", "reftable"} {
						if info, err := os.Lstat(filepath.Join(path, refs)); err == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
							return fmt.Errorf("repository/common directory inside moving feature at %s is unsupported", path)
						}
					}
				}
			}
		}
		return nil
	})
	sort.Strings(paths)
	for i := 1; i < len(paths); i++ {
		for j := 0; j < i; j++ {
			if featureRenameInside(paths[j], paths[i]) {
				return nil, fmt.Errorf("nested repository or populated submodule at %s is unsupported", paths[i])
			}
		}
	}
	return paths, err
}

func (plan *featureRenamePlan) inspectWorktree(featurePath, name, repo string) (featureRenameWorktree, error) {
	path, err := injectWorktreePath(featurePath, name)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	if err := verifyInjectWorktree(featurePath, path); err != nil {
		return featureRenameWorktree{}, err
	}
	directory, err := featureRenameDirectoryAt(path)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	marker, err := featureRenameReadFile(filepath.Join(path, ".git"), true)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	rawAdmin, err := resolveInjectGitDir(path, marker.Path)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	actualAdmin, err := featureRenameGitPath(path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return featureRenameWorktree{}, err
	}
	// Git supplies discovery, not mutation authority. The raw forward/common
	// records below must still bind through a verified stationary root, with
	// every administrative descendant inspected without following links.
	adminPath, err := filepath.EvalSymlinks(actualAdmin)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	admin, err := featureRenameDirectoryAt(adminPath)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	if err := featureRenameIdleGit(adminPath); err != nil {
		return featureRenameWorktree{}, err
	}
	common, err := plan.common(path, true)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	if err := featureRenameNoAlternates(common.Path); err != nil {
		return featureRenameWorktree{}, err
	}
	if filepath.Dir(adminPath) != filepath.Join(common.Path, "worktrees") ||
		featureRenameInside(featurePath, adminPath) || featureRenameInside(featurePath, common.Path) {
		return featureRenameWorktree{}, fmt.Errorf("unsupported Git administrative/common directory relationship for %s", path)
	}
	commonParent, err := featureRenameDirectoryAt(filepath.Dir(common.Path))
	if err != nil {
		return featureRenameWorktree{}, err
	}
	if err := plan.bindRootSpelling(rawAdmin, adminPath, commonParent); err != nil {
		return featureRenameWorktree{}, err
	}
	if featureRenameMappedPath(rawAdmin, plan.Aliases) != adminPath {
		return featureRenameWorktree{}, errors.New("actual Git administrative directory differs from the forward record")
	}
	if err := plan.validateStationaryRecord(marker, "gitdir: ", path, true); err != nil {
		return featureRenameWorktree{}, err
	}
	if repo == "" {
		// Legacy markers are authoritative only after both Git links and the
		// exact inventory registration have been verified.
		repo = common.Path
	}
	repository, err := featureRenameDirectoryAt(repo)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	expectedCommon, err := plan.common(repo, true)
	if err != nil || expectedCommon != common {
		return featureRenameWorktree{}, errors.Join(err, fmt.Errorf("worktree %s belongs to a different repository", path))
	}
	config, err := featureRenameConfig(path)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	tree := featureRenameWorktree{
		Name: name, Directory: directory, Repository: repository, Admin: admin,
		Common: common, Marker: marker, ConfigHash: digestBytes(config),
	}
	for _, item := range []struct {
		name string
		file *featureRenameFile
		data bool
	}{
		{"gitdir", &tree.Backpointer, true}, {"commondir", &tree.CommonFile, true},
		{"HEAD", &tree.Head, true}, {"index", &tree.Index, false},
	} {
		*item.file, err = featureRenameReadFile(filepath.Join(adminPath, item.name), item.data)
		if err != nil {
			return featureRenameWorktree{}, err
		}
	}
	back, err := featureRenameRecordBinding(tree.Backpointer.Data, "", adminPath)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	if err := plan.bindRootSpelling(back, tree.Marker.Path, plan.Root); err != nil {
		return featureRenameWorktree{}, err
	}
	commonRecord, err := featureRenameRecordBinding(tree.CommonFile.Data, "", adminPath)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	if err := plan.bindRootSpelling(commonRecord, common.Path, commonParent); err != nil {
		return featureRenameWorktree{}, err
	}
	if err := plan.validateStationaryRecord(tree.CommonFile, "", adminPath, true); err != nil {
		return featureRenameWorktree{}, err
	}
	inventory, err := featureRenameInventory(common.Path)
	if err != nil {
		return featureRenameWorktree{}, err
	}
	matches := 0
	for _, entry := range inventory {
		registered, err := plan.inventoryPath(entry.Path, true)
		if err != nil {
			return featureRenameWorktree{}, err
		}
		if registered == path {
			matches++
			if entry.Locked {
				return featureRenameWorktree{}, fmt.Errorf("locked Git worktree %s", path)
			}
			tree.Branch, tree.OID = entry.Branch, entry.OID
		}
	}
	if matches != 1 || tree.OID == "" {
		return featureRenameWorktree{}, fmt.Errorf("worktree %s must have exactly one complete Git registration", path)
	}
	return tree, nil
}

func buildFeatureRenamePlan(ws Workspace, oldName, newName, token string, tx *SpacesRenameTx, aliases ...*featureRenameRootAlias) (*featureRenamePlan, error) {
	plan := &featureRenamePlan{
		Version: featureRenameVersion, OldName: oldName, NewName: newName, GuardToken: token,
	}
	var err error
	plan.ID, err = managedOperationID()
	if err != nil {
		return nil, err
	}
	plan.Operation.Path = filepath.Join(featureRenameJournalRoot(ws.MetadataRoot), plan.ID)
	for _, item := range []struct {
		path string
		dest *featureRenameDirectory
	}{
		{ws.MetadataRoot, &plan.Root}, {ws.RepoRoot, &plan.Repository},
		{filepath.Join(ws.MetadataRoot, oldName), &plan.Feature},
	} {
		*item.dest, err = featureRenameDirectoryAt(item.path)
		if err != nil {
			return nil, err
		}
	}
	for _, alias := range aliases {
		if err := plan.addRootAlias(alias); err != nil {
			return nil, err
		}
	}
	plan.Common, err = plan.common(ws.RepoRoot, true)
	if err != nil {
		return nil, err
	}
	if plan.Feature.Identity.Device != plan.Root.Identity.Device {
		return nil, errors.New("feature root is a mount or belongs to a different filesystem")
	}
	if featureRenameInside(plan.Feature.Path, plan.Repository.Path) ||
		featureRenameInside(plan.Feature.Path, plan.Common.Path) {
		return nil, errors.New("controlling repository cannot be inside the moving feature")
	}
	plan.Registries = append(plan.Registries, plan.Common)
	entries, hasStack, err := injectStackEntries(plan.Feature.Path)
	if err != nil {
		return nil, err
	}
	var stack Stack
	if hasStack {
		stack, err = LoadStack(plan.Feature.Path)
		if err != nil {
			return nil, err
		}
	}
	physical, err := featureRenameScanGit(plan.Feature.Path)
	if err != nil {
		return nil, err
	}
	entryByPath := make(map[string]injectStackEntry)
	repoByPath := make(map[string]string)
	for _, entry := range entries {
		entryByPath[entry.Path] = entry
		repo := ws.RepoRoot
		if configured := GetBranch(stack, entry.Name).Repo; configured != "" {
			repo = configured
			if !filepath.IsAbs(repo) {
				return nil, fmt.Errorf("repository for %s is not an absolute binding", entry.Name)
			}
		}
		repository, alias, err := resolveFeatureRenameRoot(repo)
		if err != nil {
			return nil, err
		}
		if err := plan.addRootAlias(alias); err != nil {
			return nil, err
		}
		repo = repository.Path
		common, err := plan.common(repo, true)
		if err != nil {
			return nil, err
		}
		plan.Registries = append(plan.Registries, common)
		repoByPath[entry.Path] = repo
		if _, err := os.Lstat(entry.Path); err == nil {
			if err := rejectInjectPathSymlinks(filepath.Join(plan.Feature.Path, "worktrees"), entry.Path); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	seen := map[managedObjectIdentity]bool{}
	for _, path := range physical {
		entry, listed := entryByPath[path]
		if hasStack && (!listed || entry.Archived) {
			return nil, fmt.Errorf("unlisted or archived-but-materialized Git worktree at %s", path)
		}
		name, err := filepath.Rel(filepath.Join(plan.Feature.Path, "worktrees"), path)
		if err != nil {
			return nil, err
		}
		if !hasStack {
			if _, err := injectWorktreePath(plan.Feature.Path, name); err != nil {
				return nil, fmt.Errorf("Git object outside supported worktrees layout at %s: %w", path, err)
			}
		}
		tree, err := plan.inspectWorktree(plan.Feature.Path, name, repoByPath[path])
		if err != nil {
			return nil, err
		}
		if tree.Directory.Identity.Device != plan.Feature.Identity.Device {
			return nil, fmt.Errorf("nested worktree mount at %s is unsupported", path)
		}
		if seen[tree.Directory.Identity] || seen[tree.Admin.Identity] {
			return nil, fmt.Errorf("duplicate/alias Git worktree identity at %s", path)
		}
		seen[tree.Directory.Identity], seen[tree.Admin.Identity] = true, true
		if hasStack && tree.Branch != "refs/heads/"+GetBranch(stack, name).GitBranch() {
			return nil, fmt.Errorf("Git branch for worktree %q does not match stack metadata", name)
		}
		if residue, err := ManagedTemplateRecoveryResidue(path); err != nil {
			return nil, err
		} else if residue != "" {
			return nil, fmt.Errorf("managed template recovery is pending at %s", residue)
		}
		plan.Registries = append(plan.Registries, tree.Common)
		plan.Worktrees = append(plan.Worktrees, tree)
	}
	unique := map[string]bool{}
	repositories := plan.Registries[:0]
	for _, common := range plan.Registries {
		if !unique[common.Path] {
			repositories = append(repositories, common)
			unique[common.Path] = true
		}
	}
	plan.Registries = repositories
	if err := plan.verifyInventoryAt(false, true); err != nil {
		return nil, err
	}
	for i, tree := range plan.Worktrees {
		newPath := filepath.Join(plan.Root.Path, newName, "worktrees", tree.Name)
		forward, err := featureRenamePointerImage(tree.Marker.Data, "gitdir: ", tree.Directory.Path, newPath, tree.Admin.Path, plan.Aliases)
		if err != nil {
			return nil, err
		}
		back, err := featureRenamePointerImage(tree.Backpointer.Data, "", tree.Admin.Path, tree.Admin.Path, filepath.Join(newPath, ".git"), plan.Aliases)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(forward, tree.Marker.Data) {
			plan.Pointers = append(plan.Pointers, featureRenamePointer{
				Kind: "forward", Worktree: i, Before: tree.Marker, After: forward,
			})
		}
		plan.Pointers = append(plan.Pointers, featureRenamePointer{
			Kind: "backpointer", Worktree: i, Before: tree.Backpointer, After: back,
		})
	}
	for _, name := range []string{"stack.yaml", managedTemplateStateName} {
		file, err := featureRenameReadFile(filepath.Join(plan.Feature.Path, name), true)
		if err != nil {
			return nil, err
		}
		plan.Metadata = append(plan.Metadata, file)
	}
	ownedPaths, err := featureRenameOwnedPaths(plan.Metadata[1].Data)
	if err != nil {
		return nil, err
	}
	featureRoot, err := featureRenameOpenRoot(plan.Feature.Path)
	if err != nil {
		return nil, err
	}
	defer featureRoot.Close() //nolint:errcheck
	for _, path := range ownedPaths {
		file, err := observeManagedFileRoot(featureRoot, path)
		if err != nil {
			return nil, err
		}
		link, err := observeManagedLinkRoot(featureRoot, path)
		if err != nil {
			return nil, err
		}
		plan.Owned = append(plan.Owned, featureRenameOwnedEvidence{Relative: path, File: file, Link: link})
	}
	plan.Registry, err = featureRenameReadFile(SpacesFilePath(ws.MetadataRoot), true)
	if err != nil {
		return nil, err
	}
	if tx != nil && tx.changed {
		after, err := tx.featureRenamePostimage()
		if err != nil {
			return nil, err
		}
		plan.Pointers = append(plan.Pointers, featureRenamePointer{
			Kind: "registry", Worktree: -1, Before: plan.Registry, After: after,
		})
	}
	for i := range plan.Pointers {
		pointer := &plan.Pointers[i]
		stageParent := plan.Root.Path
		if pointer.Kind == "backpointer" {
			stageParent = plan.Worktrees[pointer.Worktree].Admin.Path
		}
		pointer.StageRoot = filepath.Join(stageParent, ".tws-feature-rename-"+plan.ID+fmt.Sprintf("-%d", i))
	}
	return plan, nil
}

func (plan *featureRenamePlan) parentPaths() []string {
	roots := []string{plan.Root.Path, plan.Repository.Path, plan.Common.Path, plan.Feature.Path, plan.Operation.Path}
	for _, tree := range plan.Worktrees {
		roots = append(roots, tree.Directory.Path, tree.Repository.Path, tree.Admin.Path, tree.Common.Path)
	}
	for _, common := range plan.Registries {
		roots = append(roots, common.Path)
	}
	seen := map[string]bool{}
	for _, root := range roots {
		for path := root; ; path = filepath.Dir(path) {
			seen[path] = true
			if filepath.Dir(path) == path {
				break
			}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (plan *featureRenamePlan) verifyParents(moved bool) error {
	for _, directory := range plan.Parents {
		if moved && featureRenameInside(plan.Feature.Path, directory.Path) {
			rel, err := filepath.Rel(plan.Feature.Path, directory.Path)
			if err != nil {
				return err
			}
			directory.Path = filepath.Join(plan.Root.Path, plan.NewName, rel)
		}
		if err := directory.verify(); err != nil {
			return err
		}
	}
	return nil
}

func featureRenameOwnedPaths(ledger []byte) ([]string, error) {
	if len(ledger) == 0 {
		return nil, nil
	}
	var state ManagedTemplateState
	if err := featureRenameReadJSON(ledger, &state); err != nil {
		return nil, err
	}
	if err := validateManagedTemplateState(state, false); err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for role, state := range state.Roles {
		for _, file := range state.Files {
			paths[managedFeatureRelative(ManagedTemplateRole(role), file.Path)] = true
		}
		for _, link := range state.Links {
			paths[filepath.Join("worktrees", link.Worktree, filepath.FromSlash(link.Path))] = true
		}
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		if filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." ||
			strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("unsafe managed evidence path %q", path)
		}
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func (plan *featureRenamePlan) verifyInventory(moved bool) error {
	return plan.verifyInventoryAt(moved, false)
}

func (plan *featureRenamePlan) verifyInventoryAt(moved, discover bool) error {
	featurePath := plan.Feature.Path
	if moved {
		featurePath = filepath.Join(plan.Root.Path, plan.NewName)
	}
	expected := map[string]featureRenameWorktree{}
	for _, tree := range plan.Worktrees {
		expected[filepath.Join(featurePath, "worktrees", tree.Name)] = tree
	}
	for _, common := range plan.Registries {
		if err := common.verify(); err != nil {
			return err
		}
		inventory, err := featureRenameInventory(common.Path)
		if err != nil {
			return err
		}
		found := map[string]int{}
		for _, entry := range inventory {
			entry.Path, err = plan.inventoryPath(entry.Path, discover)
			if err != nil {
				return err
			}
			tree, selected := expected[entry.Path]
			if !selected {
				if featureRenameInside(plan.Feature.Path, entry.Path) ||
					featureRenameInside(filepath.Join(plan.Root.Path, plan.NewName), entry.Path) {
					return fmt.Errorf("stale, missing, or unlisted Git worktree registration at %s", entry.Path)
				}
				continue
			}
			if tree.Common != common || entry.Branch != tree.Branch || entry.OID != tree.OID || entry.Locked {
				return fmt.Errorf("Git registration identity changed at %s", entry.Path)
			}
			found[entry.Path]++
		}
		for path, tree := range expected {
			if tree.Common == common && found[path] != 1 {
				return fmt.Errorf("expected exactly one Git registration at %s, found %d", path, found[path])
			}
		}
	}
	return nil
}
