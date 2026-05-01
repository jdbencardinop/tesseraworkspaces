package internal

import (
	"os"
	"path/filepath"
)

// resolveTsRoot contains the resolution logic with injectable dependencies.
func resolveTsRoot(envRoot string, repoRoot string, repoErr error, cfg Config) string {
	// 1. TS_ROOT env var
	if envRoot != "" {
		return envRoot
	}

	// 2. Global config keyed by repo path
	if repoErr == nil {
		if ws, ok := cfg.Workspaces[repoRoot]; ok {
			return ws
		}

		// 3. Repo-relative sibling: ../<repo-name>.ts/
		repoName := filepath.Base(repoRoot)
		return filepath.Join(filepath.Dir(repoRoot), repoName+".ts")
	}

	// 4. Global fallback
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "ts")
}

func TsRoot() string {
	repoRoot, repoErr := RepoRoot()
	cfg := LoadConfig()
	return resolveTsRoot(os.Getenv("TS_ROOT"), repoRoot, repoErr, cfg)
}

func FeaturePath(feature string) string {
	return filepath.Join(TsRoot(), feature)
}

func WorktreePath(feature, branch string) string {
	return filepath.Join(FeaturePath(feature), "worktrees", branch)
}
