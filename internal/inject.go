package internal

import (
	"fmt"
	"os"
	"path/filepath"
)

const InjectDir = "inject"

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

// InjectFilesForFeature re-syncs inject/ into all active worktrees for a feature.
func InjectFilesForFeature(featurePath, injectInto string) (int, error) {
	wtDir := filepath.Join(featurePath, "worktrees")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		return 0, nil
	}

	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wtPath := filepath.Join(wtDir, e.Name())
		if err := InjectFiles(featurePath, wtPath, injectInto); err != nil {
			fmt.Printf("  Warning: inject failed for %s: %v\n", e.Name(), err)
		} else {
			count++
		}
	}
	return count, nil
}

// ResolveInjectInto returns the inject target from the flag or config.
func ResolveInjectInto(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	cfg := LoadConfig()
	return cfg.InjectInto
}
