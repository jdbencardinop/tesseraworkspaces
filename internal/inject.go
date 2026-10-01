package internal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const InjectDir = "inject"

const injectGitPathReadCap = 4096

// ErrInjectWorktreeNotFound identifies an absent materialized worktree without
// conflating it with an existing path that is not a valid linked worktree.
var ErrInjectWorktreeNotFound = errors.New("inject worktree not found")

// InjectPath returns the inject directory path for a feature.
func InjectPath(featurePath string) string {
	return filepath.Join(featurePath, InjectDir)
}

// InjectFiles symlinks all files from the feature's inject/ directory
// into the target worktree. injectInto is a relative subdirectory within
// the worktree (empty string or "." means worktree root).
// Uses relative symlinks. Skips existing files.
func InjectFiles(featurePath, worktreePath, injectInto string) error {
	injectDir := InjectPath(featurePath)

	if _, err := os.Stat(injectDir); os.IsNotExist(err) {
		return nil // no inject dir, nothing to do
	}

	targetBase := worktreePath
	if injectInto != "" && injectInto != "." {
		targetBase = filepath.Join(worktreePath, injectInto)
		if err := os.MkdirAll(targetBase, 0755); err != nil {
			return fmt.Errorf("could not create inject target %s: %w", targetBase, err)
		}
	}

	return filepath.Walk(injectDir, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(injectDir, srcPath)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		destPath := filepath.Join(targetBase, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		if _, err := os.Lstat(destPath); err == nil {
			return nil
		}

		relTarget, err := filepath.Rel(filepath.Dir(destPath), srcPath)
		if err != nil {
			return fmt.Errorf("could not compute relative path: %w", err)
		}

		return os.Symlink(relTarget, destPath)
	})
}

type injectWorktreeTarget struct {
	Name string
	Path string
}

type injectStackEntry struct {
	Name     string
	Path     string
	Archived bool
}

// ResolveInjectWorktree resolves and verifies one logical external worktree.
// stack.yaml is authoritative when present; otherwise the explicit logical
// path is accepted only when it is a verified linked Git worktree root.
func ResolveInjectWorktree(featurePath, name string) (string, error) {
	entries, hasStack, err := injectStackEntries(featurePath)
	if err != nil {
		return "", err
	}
	if hasStack {
		for _, entry := range entries {
			if entry.Name != name {
				continue
			}
			if entry.Archived {
				return "", fmt.Errorf("worktree %q is archived", name)
			}
			if err := verifyInjectWorktree(featurePath, entry.Path); err != nil {
				return "", err
			}
			return entry.Path, nil
		}
		return "", ErrInjectWorktreeNotFound
	}

	path, err := injectWorktreePath(featurePath, name)
	if err != nil {
		return "", err
	}
	if err := verifyInjectWorktree(featurePath, path); err != nil {
		return "", err
	}
	return path, nil
}

// InjectFilesForFeature re-syncs inject/ into every materialized active
// worktree for a feature. Targets are validated before the first write and
// processed in deterministic logical-name order.
func InjectFilesForFeature(featurePath, injectInto string) (int, error) {
	targets, err := discoverInjectWorktrees(featurePath)
	if err != nil {
		return 0, err
	}

	count := 0
	var injectErrs []error
	for _, target := range targets {
		if err := InjectFiles(featurePath, target.Path, injectInto); err != nil {
			injectErrs = append(injectErrs, fmt.Errorf("inject failed for %s: %w", target.Name, err))
			continue
		}
		count++
	}
	return count, errors.Join(injectErrs...)
}

func discoverInjectWorktrees(featurePath string) ([]injectWorktreeTarget, error) {
	entries, hasStack, err := injectStackEntries(featurePath)
	if err != nil {
		return nil, err
	}
	if !hasStack {
		return discoverLegacyInjectWorktrees(featurePath)
	}

	targets := make([]injectWorktreeTarget, 0, len(entries))
	seenPaths := make(map[string]string)
	for _, entry := range entries {
		if entry.Archived {
			continue
		}
		err := verifyInjectWorktree(featurePath, entry.Path)
		if errors.Is(err, ErrInjectWorktreeNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("worktree %q: %w", entry.Name, err)
		}
		canonicalPath := canonicalize(entry.Path)
		if previous, ok := seenPaths[canonicalPath]; ok {
			return nil, fmt.Errorf("worktrees %q and %q resolve to the same path %s", previous, entry.Name, canonicalPath)
		}
		seenPaths[canonicalPath] = entry.Name
		targets = append(targets, injectWorktreeTarget{Name: entry.Name, Path: entry.Path})
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Name < targets[j].Name
	})
	return targets, nil
}

func injectStackEntries(featurePath string) ([]injectStackEntry, bool, error) {
	stackPath := StackPath(featurePath)
	info, err := os.Lstat(stackPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", stackPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, true, fmt.Errorf("reading %s: stack metadata must be a regular file", stackPath)
	}
	stack, err := LoadStack(featurePath)
	if err != nil {
		return nil, true, fmt.Errorf("reading %s: %w", stackPath, err)
	}

	entries := make([]injectStackEntry, 0, len(stack.Branches))
	seenNames := make(map[string]bool)
	for _, branch := range stack.Branches {
		if seenNames[branch.Name] {
			return nil, true, fmt.Errorf("stack contains duplicate worktree name %q", branch.Name)
		}
		seenNames[branch.Name] = true
		path, err := injectWorktreePath(featurePath, branch.Name)
		if err != nil {
			return nil, true, fmt.Errorf("worktree %q: %w", branch.Name, err)
		}
		entries = append(entries, injectStackEntry{
			Name:     branch.Name,
			Path:     path,
			Archived: branch.Archived,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries, true, nil
}

func injectWorktreePath(featurePath, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("logical worktree name cannot be empty")
	}
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" || strings.Contains(name, "\\") {
		return "", fmt.Errorf("logical worktree name %q is not a safe relative path", name)
	}
	clean := filepath.Clean(name)
	if clean != name || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("logical worktree name %q is not a safe relative path", name)
	}
	return filepath.Join(featurePath, "worktrees", clean), nil
}

func discoverLegacyInjectWorktrees(featurePath string) ([]injectWorktreeTarget, error) {
	worktreesRoot := filepath.Join(featurePath, "worktrees")
	info, err := os.Lstat(worktreesRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading worktrees directory %s: %w", worktreesRoot, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("worktrees path %s is not a directory", worktreesRoot)
	}

	var targets []injectWorktreeTarget
	err = filepath.WalkDir(worktreesRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == worktreesRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}

		markerInfo, markerErr := os.Lstat(filepath.Join(path, ".git"))
		if errors.Is(markerErr, fs.ErrNotExist) {
			return nil
		}
		if markerErr != nil {
			return markerErr
		}
		if !markerInfo.Mode().IsRegular() {
			return filepath.SkipDir
		}
		if err := verifyInjectWorktree(featurePath, path); err != nil {
			return fmt.Errorf("legacy worktree %s: %w", path, err)
		}
		name, err := filepath.Rel(worktreesRoot, path)
		if err != nil {
			return err
		}
		targets = append(targets, injectWorktreeTarget{Name: name, Path: path})
		return filepath.SkipDir
	})
	if err != nil {
		return nil, fmt.Errorf("discovering legacy worktrees under %s: %w", worktreesRoot, err)
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Name < targets[j].Name
	})
	return targets, nil
}

func verifyInjectWorktree(featurePath, path string) error {
	worktreesRoot := filepath.Join(featurePath, "worktrees")
	if err := rejectInjectPathSymlinks(worktreesRoot, path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrInjectWorktreeNotFound, path)
		}
		return err
	}

	rootPath, err := filepath.EvalSymlinks(worktreesRoot)
	if err != nil {
		return err
	}
	targetPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("worktree path %s escapes %s", path, worktreesRoot)
	}

	marker := filepath.Join(path, ".git")
	markerInfo, err := os.Lstat(marker)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s is not a linked Git worktree", path)
		}
		return fmt.Errorf("reading Git marker %s: %w", marker, err)
	}
	if !markerInfo.Mode().IsRegular() {
		return fmt.Errorf("%s is not a linked Git worktree", path)
	}

	gitDir, err := resolveInjectGitDir(path, marker)
	if err != nil {
		return err
	}
	if err := verifyInjectGitBackpointer(path, marker, gitDir); err != nil {
		return err
	}
	if err := verifyInjectCommonDir(path, gitDir); err != nil {
		return err
	}

	topLevel, err := gitPathOutput(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("%s is not a valid Git worktree: %w", path, err)
	}
	if canonicalize(topLevel) != canonicalize(path) {
		return fmt.Errorf("Git worktree root for %s is %s", path, topLevel)
	}
	if err := verifyInjectWorktreeRegistration(path); err != nil {
		return err
	}
	return nil
}

func resolveInjectGitDir(worktreePath, markerPath string) (string, error) {
	pointer, err := readInjectGitPathFile(markerPath)
	if err != nil {
		return "", fmt.Errorf("reading Git marker %s: %w", markerPath, err)
	}
	gitDir, ok := strings.CutPrefix(pointer, "gitdir: ")
	if !ok || gitDir == "" {
		return "", fmt.Errorf("malformed gitdir pointer in %s", markerPath)
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	info, err := os.Lstat(gitDir)
	if err != nil {
		return "", fmt.Errorf("resolving Git directory for %s: %w", worktreePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("Git directory %s for %s is not a directory", gitDir, worktreePath)
	}
	return gitDir, nil
}

func verifyInjectGitBackpointer(worktreePath, markerPath, gitDir string) error {
	backpointerPath := filepath.Join(gitDir, "gitdir")
	backpointer, err := readInjectGitPathFile(backpointerPath)
	if err != nil {
		return fmt.Errorf("reading Git worktree backpointer for %s: %w", worktreePath, err)
	}
	if !filepath.IsAbs(backpointer) {
		backpointer = filepath.Join(gitDir, backpointer)
	}
	backpointer = filepath.Clean(backpointer)
	info, err := os.Lstat(backpointer)
	if err != nil {
		return fmt.Errorf("resolving Git worktree backpointer for %s: %w", worktreePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("Git worktree backpointer for %s does not name a regular .git file", worktreePath)
	}
	if canonicalize(backpointer) != canonicalize(markerPath) {
		return fmt.Errorf("Git worktree backpointer for %s names %s", worktreePath, backpointer)
	}
	return nil
}

func verifyInjectCommonDir(worktreePath, gitDir string) error {
	commonDirPath := filepath.Join(gitDir, "commondir")
	commonDir, err := readInjectGitPathFile(commonDirPath)
	if err != nil {
		return fmt.Errorf("reading Git common directory for %s: %w", worktreePath, err)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	info, err := os.Lstat(commonDir)
	if err != nil {
		return fmt.Errorf("resolving Git common directory for %s: %w", worktreePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("Git common directory %s for %s is not a directory", commonDir, worktreePath)
	}
	return nil
}

func verifyInjectWorktreeRegistration(worktreePath string) error {
	cmd := exec.Command("git", "-C", worktreePath, "worktree", "list", "--porcelain", "-z")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return fmt.Errorf("listing registered Git worktrees for %s: %w", worktreePath, err)
		}
		return fmt.Errorf("listing registered Git worktrees for %s: %w: %s", worktreePath, err, message)
	}

	want := canonicalize(worktreePath)
	inBlock := false
	found := false
	for _, field := range bytes.Split(out, []byte{0}) {
		if len(field) == 0 {
			inBlock = false
			continue
		}
		if bytes.HasPrefix(field, []byte("worktree ")) {
			if inBlock {
				return fmt.Errorf("malformed Git worktree inventory for %s: duplicate worktree field", worktreePath)
			}
			rawPath := string(bytes.TrimPrefix(field, []byte("worktree ")))
			if rawPath == "" {
				return fmt.Errorf("malformed Git worktree inventory for %s: empty worktree path", worktreePath)
			}
			inBlock = true
			if canonicalize(rawPath) == want {
				found = true
			}
			continue
		}
		if !inBlock {
			return fmt.Errorf("malformed Git worktree inventory for %s: field before worktree path", worktreePath)
		}
	}
	if !found {
		return fmt.Errorf("%s is not registered in the Git worktree inventory", worktreePath)
	}
	return nil
}

func readInjectGitPathFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > injectGitPathReadCap {
		return "", fmt.Errorf("%s exceeds %d bytes", path, injectGitPathReadCap)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// Git control files accept CRLF; command stdout may instead contain a
	// pathname ending in CR followed by its single LF terminator.
	if bytes.HasSuffix(data, []byte("\r\n")) {
		data = data[:len(data)-2]
	}
	return injectGitPath(data)
}

func injectGitPath(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("empty Git path")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("Git path contains NUL")
	}
	if data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return "", errors.New("empty Git path")
	}
	return string(data), nil
}

func rejectInjectPathSymlinks(root, target string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("worktrees path %s is not a directory", root)
	}

	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("worktree path %s escapes %s", target, root)
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("worktree path %s contains symlink component %s", target, current)
		}
	}
	return nil
}

func gitPathOutput(path string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, message)
	}
	return injectGitPath(out)
}

// ResolveInjectInto returns the inject target from the flag or config.
func ResolveInjectInto(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	cfg := LoadConfig()
	return cfg.InjectInto
}
