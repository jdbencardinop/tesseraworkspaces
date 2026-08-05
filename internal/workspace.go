package internal

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
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
// possible). Relocating the repository changes the ID; a future
// global registry will provide persistent identifiers.
func stableID(canonicalPath string) string {
	h := sha256.Sum256([]byte(canonicalPath))
	return fmt.Sprintf("%x", h[:8])
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

// FeaturePath returns the feature directory within the workspace.
// For external mode, this is MetadataRoot/<feature>.
// For checkout mode, this returns the legacy-compatible path (MetadataRoot/<feature>).
// Prefer ResolveFeaturePath for mode-aware commands that need ambiguity detection.
func (w Workspace) FeaturePath(feature string) string {
	return filepath.Join(w.MetadataRoot, feature)
}

// WorktreePath returns the worktree directory within a feature.
func (w Workspace) WorktreePath(feature, worktree string) string {
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
	repoRoot, err := MainRepoRoot()
	if err != nil {
		return Workspace{}, fmt.Errorf("not inside a git repository")
	}
	cfg := LoadConfig()
	return ResolveCurrentWorkspaceE(repoRoot, cfg)
}
