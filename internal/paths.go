package internal

import (
	"os"
	"path/filepath"
)

func TsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "ts")
}

func FeaturePath(feature string) string {
	return filepath.Join(TsRoot(), feature)
}

func WorktreePath(feature, branch string) string {
	return filepath.Join(FeaturePath(feature), "worktrees", branch)
}
