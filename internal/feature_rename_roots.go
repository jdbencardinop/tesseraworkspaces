package internal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

type featureRenameRootLink struct {
	Path     string                `json:"path"`
	Target   string                `json:"target"`
	Resolved string                `json:"resolved"`
	Identity managedObjectIdentity `json:"identity"`
}

type featureRenameRootAlias struct {
	Spelling    string                   `json:"spelling"`
	Directory   featureRenameDirectory   `json:"directory"`
	Links       []featureRenameRootLink  `json:"links"`
	Directories []featureRenameDirectory `json:"directories"`
}

// Only an explicitly selected workspace/repository root (or a stationary
// common-directory parent) is resolved here. Operation descendants still go
// through featureRenameOpenRoot and the unchanged no-follow rooted helpers.
// Retaining the resolution evidence prevents a later alias retarget, including
// replacement by a new link to the same target, from selecting another root.
func resolveFeatureRenameRoot(spelling string) (featureRenameDirectory, *featureRenameRootAlias, error) {
	if spelling == "" {
		return featureRenameDirectory{}, nil, errors.New("empty feature rename root")
	}
	spelling = cleanAbsolute(spelling)
	alias := &featureRenameRootAlias{Spelling: spelling}
	directories := map[string]featureRenameDirectory{}
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(spelling, current), string(filepath.Separator))
	followed := 0
	for len(parts) != 0 {
		part := parts[0]
		parts = parts[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			current = filepath.Dir(current)
			continue
		}
		path := filepath.Join(current, part)
		info, err := os.Lstat(path)
		if err != nil {
			return featureRenameDirectory{}, nil, err
		}
		identity, err := managedIdentityFromInfo(info)
		if err != nil {
			return featureRenameDirectory{}, nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			followed++
			if followed > 40 {
				return featureRenameDirectory{}, nil, fmt.Errorf("too many root aliases in %s", spelling)
			}
			target, err := os.Readlink(path)
			if err != nil {
				return featureRenameDirectory{}, nil, err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return featureRenameDirectory{}, nil, err
			}
			if err := managedVerifyPathIdentity(path, identity); err != nil {
				return featureRenameDirectory{}, nil, err
			}
			alias.Links = append(alias.Links, featureRenameRootLink{
				Path: path, Target: target, Resolved: cleanAbsolute(resolved), Identity: identity,
			})
			if filepath.IsAbs(target) {
				current = string(filepath.Separator)
			}
			parts = append(strings.Split(target, string(filepath.Separator)), parts...)
			continue
		}
		if !info.IsDir() {
			return featureRenameDirectory{}, nil, fmt.Errorf("feature rename root component is not a directory: %s", path)
		}
		directories[path] = featureRenameDirectory{Path: path, Identity: identity}
		current = path
	}
	directory, err := featureRenameDirectoryAt(current)
	if err != nil {
		return featureRenameDirectory{}, nil, err
	}
	alias.Directory = directory
	for _, directory := range directories {
		if err := directory.verify(); err != nil {
			return featureRenameDirectory{}, nil, err
		}
		alias.Directories = append(alias.Directories, directory)
	}
	sort.Slice(alias.Directories, func(i, j int) bool { return alias.Directories[i].Path < alias.Directories[j].Path })
	for _, link := range alias.Links {
		if err := managedVerifyPathIdentity(link.Path, link.Identity); err != nil {
			return featureRenameDirectory{}, nil, err
		}
		target, err := os.Readlink(link.Path)
		if err != nil || target != link.Target {
			return featureRenameDirectory{}, nil, errors.Join(err, fmt.Errorf("root alias changed at %s", link.Path))
		}
	}
	if len(alias.Links) == 0 {
		return directory, nil, nil
	}
	return directory, alias, nil
}

func (alias *featureRenameRootAlias) verify() error {
	if alias == nil {
		return nil
	}
	directory, current, err := resolveFeatureRenameRoot(alias.Spelling)
	if err != nil {
		return err
	}
	if current == nil || directory != alias.Directory ||
		!slices.Equal(current.Links, alias.Links) || !slices.Equal(current.Directories, alias.Directories) {
		return fmt.Errorf("feature rename root alias identity/target changed at %s", alias.Spelling)
	}
	return nil
}

func (plan *featureRenamePlan) addRootAlias(alias *featureRenameRootAlias) error {
	if alias == nil {
		return nil
	}
	if err := alias.verify(); err != nil {
		return err
	}
	for _, link := range alias.Links {
		if featureRenameInside(plan.Feature.Path, link.Path) ||
			featureRenameInside(filepath.Join(plan.Root.Path, plan.NewName), link.Path) {
			return fmt.Errorf("root alias lies inside a moving feature: %s", link.Path)
		}
	}
	for _, directory := range alias.Directories {
		if featureRenameInside(plan.Feature.Path, directory.Path) ||
			featureRenameInside(filepath.Join(plan.Root.Path, plan.NewName), directory.Path) {
			return fmt.Errorf("root alias resolution depends on a moving feature directory: %s", directory.Path)
		}
	}
	for _, existing := range plan.Aliases {
		if existing.Spelling == alias.Spelling {
			if existing.Directory != alias.Directory ||
				!slices.Equal(existing.Links, alias.Links) || !slices.Equal(existing.Directories, alias.Directories) {
				return fmt.Errorf("root alias changed during planning at %s", alias.Spelling)
			}
			return nil
		}
	}
	plan.Aliases = append(plan.Aliases, *alias)
	return nil
}

func (plan *featureRenamePlan) validateRootAliases(stack Stack) error {
	allowed := map[string]bool{plan.Root.Path: true, plan.Repository.Path: true}
	for _, tree := range plan.Worktrees {
		allowed[tree.Repository.Path] = true
	}
	for _, common := range plan.Registries {
		allowed[filepath.Dir(common.Path)] = true
	}
	seen := map[string]bool{}
	validDirectory := func(directory featureRenameDirectory) bool {
		return filepath.IsAbs(directory.Path) && filepath.Clean(directory.Path) == directory.Path &&
			directory.Identity != (managedObjectIdentity{})
	}
	for _, alias := range plan.Aliases {
		rootAllowed := allowed[alias.Directory.Path]
		for _, entry := range stack.Branches {
			if entry.Repo == alias.Spelling && featureRenameMappedPath(entry.Repo, plan.Aliases) == alias.Directory.Path {
				rootAllowed = true
			}
		}
		if !rootAllowed || seen[alias.Spelling] || !filepath.IsAbs(alias.Spelling) ||
			filepath.Clean(alias.Spelling) != alias.Spelling || alias.Spelling == alias.Directory.Path ||
			!validDirectory(alias.Directory) || len(alias.Links) == 0 || len(alias.Directories) == 0 {
			return errors.New("invalid feature rename root alias evidence")
		}
		seen[alias.Spelling] = true
		for _, link := range alias.Links {
			if !filepath.IsAbs(link.Path) || filepath.Clean(link.Path) != link.Path ||
				!filepath.IsAbs(link.Resolved) || filepath.Clean(link.Resolved) != link.Resolved ||
				link.Target == "" || strings.ContainsRune(link.Target, 0) || link.Identity == (managedObjectIdentity{}) ||
				featureRenameInside(plan.Feature.Path, link.Path) ||
				featureRenameInside(filepath.Join(plan.Root.Path, plan.NewName), link.Path) {
				return errors.New("unsafe feature rename root alias")
			}
		}
		for _, directory := range alias.Directories {
			if !validDirectory(directory) {
				return errors.New("invalid root alias parent evidence")
			}
			if featureRenameInside(plan.Feature.Path, directory.Path) ||
				featureRenameInside(filepath.Join(plan.Root.Path, plan.NewName), directory.Path) {
				return errors.New("root alias resolution depends on a moving feature directory")
			}
		}
		if featureRenameMappedPath(alias.Spelling, plan.Aliases) != alias.Directory.Path {
			return errors.New("root alias does not map to its frozen directory")
		}
	}
	return nil
}

// This only substitutes already-frozen ambient/root links. It never evaluates
// symlinks in a selected feature, worktree, marker, or administrative descendant.
func featureRenameMappedPath(path string, aliases []featureRenameRootAlias) string {
	for range 40 {
		previous := path
		for _, alias := range aliases {
			for _, link := range alias.Links {
				if path == link.Path || strings.HasPrefix(path, link.Path+string(filepath.Separator)) {
					path = link.Resolved + strings.TrimPrefix(path, link.Path)
				}
			}
		}
		if path == previous {
			break
		}
	}
	return filepath.Clean(path)
}

// Inspect raw components before ".." can erase a traversed symlink. Only
// already-frozen root aliases may participate in a Git directory binding.
// Historical receipts keep structural validation without requiring old Git
// administrative paths to survive later legitimate archive/delete operations.
func featureRenameRawDirectoryBinding(base, value string, aliases []featureRenameRootAlias, inspect bool, movingRoots ...string) (string, error) {
	current := base
	if filepath.IsAbs(value) {
		current = string(filepath.Separator)
	}
	checkMoving := func(path string) error {
		for _, root := range movingRoots {
			if featureRenameInside(root, path) {
				return fmt.Errorf("raw path traverses a moving feature at %s", path)
			}
		}
		return nil
	}
	for _, part := range strings.Split(value, string(filepath.Separator)) {
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		current = filepath.Join(current, part)
		if err := checkMoving(current); err != nil {
			return "", err
		}
		resolved := ""
		for _, alias := range aliases {
			for _, link := range alias.Links {
				if link.Path == current {
					if inspect {
						if err := alias.verify(); err != nil {
							return "", err
						}
					}
					resolved = link.Resolved
				}
			}
		}
		if inspect {
			info, err := os.Lstat(current)
			if err != nil {
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 && resolved == "" {
				return "", fmt.Errorf("unrecorded traversed symlink at %s", current)
			}
			if info.Mode()&os.ModeSymlink == 0 && !info.IsDir() {
				return "", fmt.Errorf("raw Git directory path traverses a non-directory at %s", current)
			}
		}
		if resolved != "" {
			current = resolved
			if err := checkMoving(current); err != nil {
				return "", err
			}
		}
	}
	return current, nil
}

func (plan *featureRenamePlan) bindRootSpelling(raw, expected string, root featureRenameDirectory) error {
	if featureRenameMappedPath(raw, plan.Aliases) == expected {
		return nil
	}
	relative, err := filepath.Rel(root.Path, expected)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.Join(err, errors.New("Git record is outside its verified root"))
	}
	suffix := string(filepath.Separator) + relative
	spelling, ok := strings.CutSuffix(raw, suffix)
	if relative == "." {
		spelling, ok = raw, true
	}
	if !ok {
		return fmt.Errorf("Git record %s does not bind %s", raw, expected)
	}
	directory, alias, err := resolveFeatureRenameRoot(spelling)
	if err != nil || directory != root {
		return errors.Join(err, fmt.Errorf("Git record root %s does not bind %s", spelling, root.Path))
	}
	if err := plan.addRootAlias(alias); err != nil {
		return err
	}
	if featureRenameMappedPath(raw, plan.Aliases) != expected {
		return errors.New("Git record alias does not preserve its verified descendant")
	}
	return nil
}

func (plan *featureRenamePlan) common(repo string, discover bool) (featureRenameDirectory, error) {
	path, err := featureRenameGitPath(repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return featureRenameDirectory{}, err
	}
	path = featureRenameMappedPath(path, plan.Aliases)
	if discover {
		parent, alias, err := resolveFeatureRenameRoot(filepath.Dir(path))
		if err != nil {
			return featureRenameDirectory{}, err
		}
		if err := plan.addRootAlias(alias); err != nil {
			return featureRenameDirectory{}, err
		}
		path = filepath.Join(parent.Path, filepath.Base(path))
	}
	return featureRenameDirectoryAt(path)
}

func (plan *featureRenamePlan) inventoryPath(path string, discover bool) (string, error) {
	mapped := featureRenameMappedPath(path, plan.Aliases)
	if featureRenameInside(plan.Root.Path, mapped) {
		return mapped, nil
	}
	parts := strings.Split(path, string(filepath.Separator))
	for i, part := range parts {
		if part != plan.OldName && part != plan.NewName {
			continue
		}
		spelling := strings.Join(parts[:i], string(filepath.Separator))
		root, alias, err := resolveFeatureRenameRoot(spelling)
		if err != nil || root != plan.Root {
			continue
		}
		if !discover {
			return "", fmt.Errorf("unrecorded workspace root alias in Git registration %s", path)
		}
		if err := plan.addRootAlias(alias); err != nil {
			return "", err
		}
		return featureRenameMappedPath(path, plan.Aliases), nil
	}
	// A descendant alias is not an ambient root spelling. Detect it (including
	// missing leaves) without adopting it or repairing an unrelated sibling.
	parent := path
	for {
		if _, err := os.Lstat(parent); err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(parent)
			if resolveErr == nil {
				relative, err := filepath.Rel(parent, path)
				if err != nil {
					return "", err
				}
				actual := filepath.Join(resolved, relative)
				if featureRenameInside(plan.Feature.Path, actual) ||
					featureRenameInside(filepath.Join(plan.Root.Path, plan.NewName), actual) {
					return "", fmt.Errorf("unsupported descendant alias in Git registration %s", path)
				}
			}
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	return mapped, nil
}
