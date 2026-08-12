package internal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrWorktreeUnsupported is returned when a caller attempts to use worktree
// paths in checkout mode, which does not support linked worktrees.
var ErrWorktreeUnsupported = errors.New("linked worktrees are not supported in checkout mode")

// ErrAmbiguousFeature is returned when a feature exists in both legacy and
// new layout paths.
type ErrAmbiguousFeature struct {
	Feature    string
	LegacyPath string
	NewPath    string
}

func (e *ErrAmbiguousFeature) Error() string {
	return fmt.Sprintf("ambiguous feature %q: exists at both %s and %s; run 'tws migrate-layout %s' to resolve",
		e.Feature, e.LegacyPath, e.NewPath, e.Feature)
}

// ResolveFeaturePath returns the path to a feature, erroring if ambiguous.
// For checkout mode:
//   - New layout: <repoRoot>/.tws/features/<feature>
//   - Legacy layout: <metadataRoot>/<feature> (the old sibling-dir style)
//
// If both exist, returns ErrAmbiguousFeature.
// For external mode, returns the standard MetadataRoot/<feature> path.
//
// This resolver is deliberately guard-free: it never consults spaces.yaml.
// Burying the registered-space guard here would make a pure path join depend
// on registry state for every caller (including tight listing loops), and it
// would let the continue-on-resolve-error branches in checkout_health.go
// silently swallow an unreadable-spaces.yaml failure. The guard lives at named
// call sites instead (see GuardFeatureName).
func (w Workspace) ResolveFeaturePath(feature string) (string, error) {
	if err := validateFeatureName(feature); err != nil {
		return "", err
	}

	if w.Mode != ModeCheckout {
		return filepath.Join(w.MetadataRoot, feature), nil
	}

	newPath := filepath.Join(w.MetadataRoot, "features", feature)
	legacyPath := filepath.Join(w.MetadataRoot, feature)

	newExists := dirExists(newPath)
	legacyExists := dirExists(legacyPath) && !isReservedDir(feature)

	if newExists && legacyExists {
		return "", &ErrAmbiguousFeature{Feature: feature, LegacyPath: legacyPath, NewPath: newPath}
	}
	if newExists {
		return newPath, nil
	}
	if legacyExists {
		return legacyPath, nil
	}

	// Neither exists; default to new layout.
	return newPath, nil
}

// ResolveFeaturePathOrLegacy is like ResolveFeaturePath but for read-only
// operations that need to find existing features. Returns ("", nil) if not found.
//
// Deliberately guard-free for the same reason as ResolveFeaturePath.
func (w Workspace) ResolveFeaturePathOrLegacy(feature string) (string, error) {
	if err := validateFeatureName(feature); err != nil {
		return "", err
	}

	if w.Mode != ModeCheckout {
		p := filepath.Join(w.MetadataRoot, feature)
		if dirExists(p) {
			return p, nil
		}
		return "", nil
	}

	newPath := filepath.Join(w.MetadataRoot, "features", feature)
	legacyPath := filepath.Join(w.MetadataRoot, feature)

	newExists := dirExists(newPath)
	legacyExists := dirExists(legacyPath) && !isReservedDir(feature)

	if newExists && legacyExists {
		return "", &ErrAmbiguousFeature{Feature: feature, LegacyPath: legacyPath, NewPath: newPath}
	}
	if newExists {
		return newPath, nil
	}
	if legacyExists {
		return legacyPath, nil
	}
	return "", nil
}

// LegacyFeatureNames returns sorted legacy checkout feature names.
//
// It has no error channel and exactly one caller, a ValidArgsFunction, so it
// stays best-effort: untrusted spaces metadata yields no completion candidates.
func (w Workspace) LegacyFeatureNames() []string {
	if w.Mode != ModeCheckout {
		return nil
	}
	owners, err := SpaceDirOwners(w.MetadataRoot)
	if err != nil {
		return nil
	}
	var names []string
	if entries, err := os.ReadDir(w.MetadataRoot); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || isReservedDir(entry.Name()) {
				continue
			}
			if _, owned := owners.TopLevelOwner(entry.Name()); owned {
				continue
			}
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// ListFeaturesResolved returns sorted feature names for the workspace,
// excluding reserved internal directories and directories owned by a
// registered sibling space. Untrusted spaces metadata is a hard failure:
// no partial listing is ever returned.
func (w Workspace) ListFeaturesResolved() ([]string, error) {
	// One read per listing call; never once per feature.
	owners, err := SpaceDirOwners(w.MetadataRoot)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)

	if w.Mode == ModeCheckout {
		// New layout: .tws/features/*
		featuresDir := filepath.Join(w.MetadataRoot, "features")
		if entries, err := os.ReadDir(featuresDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() || isReservedDir(e.Name()) {
					continue
				}
				if _, owned := owners.FeatureOwner(e.Name()); owned {
					continue
				}
				seen[e.Name()] = true
			}
		}
		// Legacy layout: .tws/<feature> (non-reserved dirs in MetadataRoot)
		if entries, err := os.ReadDir(w.MetadataRoot); err == nil {
			for _, e := range entries {
				if !e.IsDir() || isReservedDir(e.Name()) || seen[e.Name()] {
					continue
				}
				if _, owned := owners.TopLevelOwner(e.Name()); owned {
					continue
				}
				seen[e.Name()] = true
			}
		}
	} else {
		if entries, err := os.ReadDir(w.MetadataRoot); err == nil {
			for _, e := range entries {
				if !e.IsDir() || isReservedDir(e.Name()) {
					continue
				}
				if _, owned := owners.TopLevelOwner(e.Name()); owned {
					continue
				}
				seen[e.Name()] = true
			}
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// reservedDirs are directories in .tws/ that are NOT features.
var reservedDirs = map[string]bool{
	"config.yaml": true,
	"features":    true,
	"state":       true,
	"templates":   true,
	"hooks":       true,
	"skills":      true,
	"spaces.yaml": true,
}

func isReservedDir(name string) bool {
	return reservedDirs[name] || strings.HasPrefix(name, ".")
}

// validateFeatureName rejects unsafe feature names (path traversal, symlinks, etc).
func validateFeatureName(name string) error {
	if name == "" {
		return fmt.Errorf("feature name cannot be empty")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("feature name %q contains path separator", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("feature name %q contains path traversal", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("feature name %q is reserved", name)
	}
	if isReservedDir(name) {
		return fmt.Errorf("feature name %q conflicts with reserved directory", name)
	}
	// Reject names that would be symlinks at the target.
	return nil
}

func dirExists(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	// Reject symlinks.
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	return info.IsDir()
}

// DetectFeatureFromCwdE is a mode-aware version of DetectFeatureFromCwd.
// It handles new layout (.tws/features/<f>), legacy layout (.tws/<f>),
// and repo root detection.
//
// Deliberately exclusion-free: it has no non-test callers, so adding a
// spaces.yaml read here would be dead weight. `tws space list` uses its own
// anchor-rooted scope detection instead.
func (w Workspace) DetectFeatureFromCwdE(cwd string) (feature, branch string) {
	if w.Mode == ModeCheckout {
		// New layout: cwd under .tws/features/<feature>/...
		featuresDir := filepath.Join(w.MetadataRoot, "features")
		if rel, err := filepath.Rel(featuresDir, cwd); err == nil && !strings.HasPrefix(rel, "..") {
			parts := strings.SplitN(rel, string(filepath.Separator), 2)
			if len(parts) >= 1 && parts[0] != "." {
				return parts[0], ""
			}
		}
	}

	// Legacy/external: cwd under MetadataRoot/<feature>/...
	if rel, err := filepath.Rel(w.MetadataRoot, cwd); err == nil && !strings.HasPrefix(rel, "..") {
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) >= 1 && parts[0] != "." && !isReservedDir(parts[0]) {
			feat := parts[0]
			if len(parts) >= 2 {
				// Might be in worktrees/<branch>
				sub := strings.SplitN(parts[1], string(filepath.Separator), 2)
				if sub[0] == "worktrees" && len(sub) >= 2 {
					branchParts := strings.SplitN(sub[1], string(filepath.Separator), 2)
					return feat, branchParts[0]
				}
			}
			return feat, ""
		}
	}

	return "", ""
}

// CheckoutStateDir returns the state directory for the workspace.
// Always .tws/state/ regardless of whether features are in legacy or new layout.
func (w Workspace) CheckoutStateDir() string {
	return filepath.Join(w.MetadataRoot, "state")
}

// RequireFeaturePath is the package-level error-returning feature path resolver.
// It calls RequireWorkspace, guards the logical feature name against registered
// sibling spaces, and then resolves the path. Callers must propagate errors
// (ambiguity, invalid workspace_mode, space-name conflict) instead of silently
// falling back.
func RequireFeaturePath(feature string) (string, error) {
	ws, err := RequireWorkspace()
	if err != nil {
		return "", err
	}
	if err := GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return "", err
	}
	return ws.ResolveFeaturePath(feature)
}

// ResolveFeaturePathFor resolves a feature directory from a workspace the
// caller has already resolved, so a read-only command can keep working when
// the workspace itself is resolvable but its source repository is not.
//
// It applies exactly the same strict feature-name and sibling-space validation
// as RequireFeaturePath. When the caller holds no usable workspace, it falls
// back to the guarded TwsRoot, which still honours TWS_ROOT and workspace
// detection from the current directory; it never re-derives a repository.
func ResolveFeaturePathFor(ws Workspace, feature string) (string, error) {
	if ws.MetadataRoot == "" {
		ws = Workspace{Mode: ModeExternal, MetadataRoot: TwsRoot()}
	}
	if err := GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return "", err
	}
	return ws.ResolveFeaturePath(feature)
}

// RequireWorktreePath resolves a worktree path through the workspace layer.
// Returns ErrWorktreeUnsupported in checkout mode.
func RequireWorktreePath(feature, worktree string) (string, error) {
	ws, err := RequireWorkspace()
	if err != nil {
		return "", err
	}
	p := ws.WorktreePath(feature, worktree)
	if p == "" {
		return "", ErrWorktreeUnsupported
	}
	return p, nil
}
