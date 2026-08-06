package internal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// WorkspaceMode describes how tws stores metadata for a repository.
type WorkspaceMode string

const (
	// ModeExternal stores metadata in a sibling directory (<repo>.tws).
	// This is the default and preserves the original behaviour.
	ModeExternal WorkspaceMode = "external"

	// ModeCheckout stores metadata inside the repository at <repo>/.tws.
	ModeCheckout WorkspaceMode = "checkout"
)

// Capabilities reports what features a given workspace mode supports.
// Checkout mode retains logical stacks and decisions but cannot create
// linked worktrees because it stores metadata inside the repo tree.
type Capabilities struct {
	Stack           bool // logical stack/decisions
	LinkedWorktrees bool // git worktree add support
}

// Workspace is the resolved identity and layout for a single repository.
type Workspace struct {
	// RepoRoot is the absolute, cleaned repository path.
	RepoRoot string

	// Mode is the resolved workspace mode (external or checkout).
	Mode WorkspaceMode

	// MetadataRoot is the absolute path that holds tws state.
	// External: <repo>.tws   Checkout: <repo>/.tws
	MetadataRoot string

	// StableID is a deterministic identifier derived from the canonical
	// repo path. Computed on each resolution, not persisted.
	StableID string

	// Caps describes mode-specific capabilities.
	Caps Capabilities
}

// capsFor returns capabilities for a given mode.
func capsFor(mode WorkspaceMode) Capabilities {
	switch mode {
	case ModeCheckout:
		return Capabilities{Stack: true, LinkedWorktrees: false}
	default: // external
		return Capabilities{Stack: true, LinkedWorktrees: true}
	}
}

// stableID computes a deterministic hex ID from a canonical path.
// It uses the absolute, cleaned path (with symlinks resolved when
// possible). Relocating the repository changes the ID; use the
// persisted workspace marker identity for move-stable identity.
func stableID(canonicalPath string) string {
	h := sha256.Sum256([]byte(canonicalPath))
	return fmt.Sprintf("%x", h[:8])
}

// workspaceMarkerIDFile is the tool-owned file holding a workspace's
// persistent opaque identity. It lives inside the tool-owned metadata
// directory: <repo>/.tws for checkout workspaces and
// <workspace>/.tws-workspace for external workspaces.
const workspaceMarkerIDFile = "workspace-id"

var markerIDRegexp = regexp.MustCompile(`^[0-9a-f]{32}$`)

// CheckoutMarkerDir returns the shared Git metadata directory that holds the
// persistent marker identity of a checkout workspace.
func CheckoutMarkerDir(repoRoot string) string {
	if dir, err := GitMarkerDir(repoRoot); err == nil {
		return dir
	}
	return filepath.Join(repoRoot, ".git", "tws")
}

// ExternalMarkerDir returns the tool-owned metadata directory that holds
// the persistent marker identity of an external workspace.
func ExternalMarkerDir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, workspaceMarker)
}

// GitMarkerDir returns the shared Git metadata directory for a repository.
// Linked worktrees resolve to the same common directory as the main checkout.
func GitMarkerDir(repoRoot string) (string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("resolving Git marker directory: %w", err)
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repoRoot, dir)
	}
	return filepath.Join(canonicalize(dir), "tws"), nil
}

// ReadWorkspaceMarkerID reads the persistent marker identity stored in a
// tool-owned metadata directory. It returns an empty string when no marker
// file exists, and an error when the marker file is unreadable or malformed.
// It never creates or mutates anything on disk.
func ReadWorkspaceMarkerID(markerDir string) (string, error) {
	path := filepath.Join(markerDir, workspaceMarkerIDFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading workspace marker %s: %w", path, err)
	}
	id := strings.TrimSpace(string(data))
	if !markerIDRegexp.MatchString(id) {
		return "", fmt.Errorf("workspace marker %s is malformed", path)
	}
	return id, nil
}

// EnsureWorkspaceMarkerID returns the persistent marker identity for a
// tool-owned metadata directory, creating it when absent. Creation is
// atomic: the identity is written to a temporary file and linked into
// place, so a concurrent creator never loses its identity and a partial
// write is never observed as a valid marker.
func EnsureWorkspaceMarkerID(markerDir string) (string, error) {
	if id, err := ReadWorkspaceMarkerID(markerDir); err != nil || id != "" {
		return id, err
	}
	if err := os.MkdirAll(markerDir, 0755); err != nil {
		return "", fmt.Errorf("creating workspace marker dir %s: %w", markerDir, err)
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating workspace marker: %w", err)
	}
	id := hex.EncodeToString(buf)

	tmp, err := os.CreateTemp(markerDir, ".workspace-id-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating workspace marker: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func(primary error) error {
		return errors.Join(primary, os.Remove(tmpName))
	}
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		return "", cleanup(errors.Join(fmt.Errorf("writing workspace marker: %w", err), tmp.Close()))
	}
	if err := tmp.Sync(); err != nil {
		return "", cleanup(errors.Join(fmt.Errorf("syncing workspace marker: %w", err), tmp.Close()))
	}
	if err := tmp.Close(); err != nil {
		return "", cleanup(fmt.Errorf("closing workspace marker: %w", err))
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return "", cleanup(fmt.Errorf("setting workspace marker permissions: %w", err))
	}

	target := filepath.Join(markerDir, workspaceMarkerIDFile)
	if err := os.Link(tmpName, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Another writer won the race; adopt its identity.
			existing, readErr := ReadWorkspaceMarkerID(markerDir)
			return existing, cleanup(readErr)
		}
		return "", cleanup(fmt.Errorf("installing workspace marker atomically: %w", err))
	}
	if err := os.Remove(tmpName); err != nil {
		return "", fmt.Errorf("cleaning up workspace marker temp file: %w", err)
	}
	return id, nil
}

// EnsureExternalWorkspaceMarker creates the standard `.tws-workspace`
// marker directory for an external workspace root. The persistent identity
// file is created separately on explicit registry enrollment.
func EnsureExternalWorkspaceMarker(workspaceRoot string) error {
	if workspaceRoot == "" {
		return fmt.Errorf("empty workspace root")
	}
	markerDir := ExternalMarkerDir(workspaceRoot)
	if err := os.MkdirAll(markerDir, 0755); err != nil {
		return fmt.Errorf("creating workspace marker: %w", err)
	}
	return nil
}

// canonicalize returns an absolute, cleaned path. It resolves symlinks
// when possible; on error it falls back to filepath.Abs + filepath.Clean.
func canonicalize(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return cleanAbsolute(p)
}

func cleanAbsolute(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

// parseMode interprets a raw string as a WorkspaceMode.
// Unknown values return empty string and false.
func parseMode(raw string) (WorkspaceMode, bool) {
	switch WorkspaceMode(raw) {
	case ModeExternal:
		return ModeExternal, true
	case ModeCheckout:
		return ModeCheckout, true
	case "":
		return "", false
	default:
		return "", false
	}
}

// ResolveCurrentWorkspace determines the workspace for a given repo root
// and config. It checks, in order:
//
//  1. Explicit workspace_mode in the per-repo config (.tws/config.yaml).
//  2. Presence of .tws/config.yaml without workspace_mode -> external (legacy).
//  3. Default: external.
//
// This function is a pure resolver; it never calls TwsRoot or any path
// function that might recurse back into resolution.
func ResolveCurrentWorkspace(repoRoot string, cfg Config) Workspace {
	original := cleanAbsolute(repoRoot)
	canon := canonicalize(repoRoot)

	// Read per-repo config to look for explicit workspace_mode.
	repoConfigFile := filepath.Join(canon, ".tws", "config.yaml")
	repoCfg := loadConfigFile(repoConfigFile)

	var mode WorkspaceMode
	if m, ok := parseMode(repoCfg.WorkspaceMode); ok {
		mode = m
	} else {
		// Legacy: .tws/config.yaml exists but no workspace_mode -> external.
		// Or no config at all -> external.
		mode = ModeExternal
	}

	var metadataRoot string
	switch mode {
	case ModeCheckout:
		metadataRoot = filepath.Join(canon, ".tws")
	default: // external
		metadataRoot = resolveExternalRoot(original, canon, cfg)
	}

	return Workspace{
		RepoRoot:     canon,
		Mode:         mode,
		MetadataRoot: metadataRoot,
		StableID:     stableID(canon),
		Caps:         capsFor(mode),
	}
}

// resolveExternalRoot computes the external metadata root for a repo.
// Priority: original-path config key -> canonical-path config key -> sibling directory.
// This preserves legacy config entries that intentionally use a symlinked repo path.
func resolveExternalRoot(originalRepoRoot, canonicalRepoRoot string, cfg Config) string {
	if root, ok := cfg.Workspaces[originalRepoRoot]; ok {
		return root
	}
	if root, ok := cfg.Workspaces[canonicalRepoRoot]; ok {
		return root
	}
	return canonicalRepoRoot + ".tws"
}

// FeaturePath returns the canonical write path for NEW features.
// For external mode: MetadataRoot/<feature>.
// For checkout mode: MetadataRoot/features/<feature> (new layout).
// Use this for creating new features. For reading existing features that may
// be in legacy layout, use ResolveFeaturePath.
func (w Workspace) FeaturePath(feature string) string {
	if w.Mode == ModeCheckout {
		return filepath.Join(w.MetadataRoot, "features", feature)
	}
	return filepath.Join(w.MetadataRoot, feature)
}

// LegacyFeaturePath returns the legacy feature path (MetadataRoot/<feature>).
// Used only for migration reads and backward-compatible lookup.
func (w Workspace) LegacyFeaturePath(feature string) string {
	return filepath.Join(w.MetadataRoot, feature)
}

// WorktreePath returns the worktree directory within a feature.
// In checkout mode, linked worktrees are not supported; returns empty string.
func (w Workspace) WorktreePath(feature, worktree string) string {
	if w.Mode == ModeCheckout {
		return ""
	}
	return filepath.Join(w.MetadataRoot, feature, "worktrees", worktree)
}

// resolveWorkspaceMetadataRoot is the backend boundary function.
// It resolves the metadata root for a given repo, accounting for
// workspace mode. Called by resolveTwsRoot to route through the
// workspace layer while preserving byte-for-byte external outputs.
//
// If repoRoot is empty or repoErr is non-nil, this returns empty
// string (caller should fall back to home dir or env override).
func resolveWorkspaceMetadataRoot(repoRoot string, repoErr error, cfg Config) string {
	if repoErr != nil || repoRoot == "" {
		return ""
	}
	ws := ResolveCurrentWorkspace(repoRoot, cfg)
	return ws.MetadataRoot
}

// metadataRootExists checks if the metadata root directory exists on disk.
func metadataRootExists(root string) bool {
	info, err := os.Stat(root)
	return err == nil && info.IsDir()
}

func inferExternalRepoRoot(metadataRoot string, cfg Config) (string, error) {
	metadataRoot = canonicalize(metadataRoot)
	candidates := make(map[string]bool)
	addCandidate := func(path string) {
		if path == "" {
			return
		}
		if root, err := MainRepoRootIn(path); err == nil {
			candidates[canonicalize(root)] = true
		}
	}

	for repo, configuredRoot := range cfg.Workspaces {
		if canonicalize(configuredRoot) == metadataRoot {
			addCandidate(repo)
		}
	}
	if siblingRepo, ok := strings.CutSuffix(metadataRoot, ".tws"); ok {
		addCandidate(siblingRepo)
	}

	entries, _ := os.ReadDir(metadataRoot)
	for _, featureEntry := range entries {
		if !featureEntry.IsDir() || featureEntry.Name() == workspaceMarker {
			continue
		}
		featurePath := filepath.Join(metadataRoot, featureEntry.Name())
		stack, err := LoadStack(featurePath)
		if err != nil {
			continue
		}
		for _, entry := range stack.Branches {
			if entry.Repo != "" || entry.Archived {
				continue
			}
			worktreePath := filepath.Join(featurePath, "worktrees", entry.Name)
			if _, err := os.Stat(worktreePath); err == nil {
				addCandidate(worktreePath)
			}
		}
	}

	if len(candidates) == 1 {
		for root := range candidates {
			return root, nil
		}
	}
	if len(candidates) > 1 {
		var roots []string
		for root := range candidates {
			roots = append(roots, root)
		}
		sort.Strings(roots)
		return "", fmt.Errorf("external workspace %s maps to multiple default repositories (%s); run from a worktree or repository", metadataRoot, strings.Join(roots, ", "))
	}
	return "", fmt.Errorf("cannot determine source repository for external workspace %s; run from a worktree or configure the workspace path", metadataRoot)
}

// ResolveCurrentWorkspaceE is an error-returning resolver for use in CLI
// commands. Unlike ResolveCurrentWorkspace (which silently defaults to
// external mode), this function returns an error when the per-repo config
// contains an invalid workspace_mode value.
func ResolveCurrentWorkspaceE(repoRoot string, cfg Config) (Workspace, error) {
	original := cleanAbsolute(repoRoot)
	canon := canonicalize(repoRoot)

	repoConfigFile := filepath.Join(canon, ".tws", "config.yaml")
	repoCfg := loadConfigFile(repoConfigFile)

	var mode WorkspaceMode
	if repoCfg.WorkspaceMode != "" {
		m, ok := parseMode(repoCfg.WorkspaceMode)
		if !ok {
			return Workspace{}, fmt.Errorf("invalid workspace_mode %q in %s", repoCfg.WorkspaceMode, repoConfigFile)
		}
		mode = m
	} else {
		mode = ModeExternal
	}

	var metadataRoot string
	switch mode {
	case ModeCheckout:
		metadataRoot = filepath.Join(canon, ".tws")
	default:
		metadataRoot = resolveExternalRoot(original, canon, cfg)
	}

	return Workspace{
		RepoRoot:     canon,
		Mode:         mode,
		MetadataRoot: metadataRoot,
		StableID:     stableID(canon),
		Caps:         capsFor(mode),
	}, nil
}

// RequireWorkspace resolves the workspace for the current repo root,
// returning a stable CLI error if the repo root cannot be determined
// or the workspace mode is invalid. Commands should use this instead
// of silently ignoring errors from MainRepoRoot or ResolveCurrentWorkspace.
func RequireWorkspace() (Workspace, error) {
	cfg := LoadConfig()
	if repoRoot, err := MainRepoRoot(); err == nil {
		return ResolveCurrentWorkspaceE(repoRoot, cfg)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return Workspace{}, err
	}
	metadataRoot := DetectWorkspaceRoot(cwd, cfg)
	if metadataRoot == "" || !metadataRootExists(metadataRoot) {
		return Workspace{}, fmt.Errorf("not inside a git repository or tws workspace")
	}
	repoRoot, err := inferExternalRepoRoot(metadataRoot, cfg)
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{
		RepoRoot:     repoRoot,
		Mode:         ModeExternal,
		MetadataRoot: canonicalize(metadataRoot),
		StableID:     stableID(repoRoot),
		Caps:         capsFor(ModeExternal),
	}, nil
}
