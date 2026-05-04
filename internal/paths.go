package internal

import (
	"os"
	"path/filepath"
	"strings"
)

const workspaceMarker = ".tws-workspace"

// DetectWorkspaceRoot checks if cwd is inside an existing workspace.
// Returns the workspace root or empty string.
func DetectWorkspaceRoot(cwd string, cfg Config) string {
	// Tier 1: Walk up looking for .tws-workspace marker
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, workspaceMarker)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Tier 2: Check if cwd is inside any configured workspace path
	for _, ws := range cfg.Workspaces {
		absWs, err := filepath.Abs(ws)
		if err != nil {
			continue
		}
		absCwd, err := filepath.Abs(cwd)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absCwd, absWs+string(filepath.Separator)) || absCwd == absWs {
			return absWs
		}
	}

	// Tier 3: Check if cwd is inside ~/tws
	home, _ := os.UserHomeDir()
	globalDefault := filepath.Join(home, "tws")
	absCwd, _ := filepath.Abs(cwd)
	if strings.HasPrefix(absCwd, globalDefault+string(filepath.Separator)) || absCwd == globalDefault {
		return globalDefault
	}

	return ""
}

// resolveTwsRoot contains the resolution logic with injectable dependencies.
func resolveTwsRoot(envRoot string, cwd string, repoRoot string, repoErr error, cfg Config) string {
	// 0. TWS_ROOT env var — always wins
	if envRoot != "" {
		return envRoot
	}

	// 1. Already inside a workspace? Return that root.
	if wsRoot := DetectWorkspaceRoot(cwd, cfg); wsRoot != "" {
		return wsRoot
	}

	// 2. Global config keyed by repo path
	if repoErr == nil {
		if ws, ok := cfg.Workspaces[repoRoot]; ok {
			return ws
		}

		// 3. Repo-relative sibling: ../<repo-name>.tws/
		repoName := filepath.Base(repoRoot)
		return filepath.Join(filepath.Dir(repoRoot), repoName+".tws")
	}

	// 4. Global fallback
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "tws")
}

func TwsRoot() string {
	repoRoot, repoErr := MainRepoRoot()
	cfg := LoadConfig()
	cwd, _ := os.Getwd()
	return resolveTwsRoot(os.Getenv("TWS_ROOT"), cwd, repoRoot, repoErr, cfg)
}

func FeaturePath(feature string) string {
	return filepath.Join(TwsRoot(), feature)
}

func WorktreePath(feature, branch string) string {
	return filepath.Join(FeaturePath(feature), "worktrees", branch)
}

// ListFeatures returns the names of all feature directories in the workspace.
func ListFeatures() []string {
	wsRoot := TwsRoot()
	entries, err := os.ReadDir(wsRoot)
	if err != nil {
		return nil
	}
	var features []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != ".tws-workspace" {
			features = append(features, e.Name())
		}
	}
	return features
}

// ListBranches returns the branch names for a feature from stack.yaml,
// or from the worktrees directory if no stack exists.
func ListBranches(feature string) []string {
	featurePath := FeaturePath(feature)
	stack, err := LoadStack(featurePath)
	if err == nil && len(stack.Branches) > 0 {
		var names []string
		for _, e := range stack.Branches {
			names = append(names, e.Name)
		}
		return names
	}

	// Fallback: list worktree directories
	wtDir := filepath.Join(featurePath, "worktrees")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}
