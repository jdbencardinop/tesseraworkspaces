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
// into the target worktree. Uses relative symlinks so they work
// regardless of absolute path. Skips existing files.
func InjectFiles(featurePath, worktreePath string) error {
	injectDir := InjectPath(featurePath)

	if _, err := os.Stat(injectDir); os.IsNotExist(err) {
		return nil // no inject dir, nothing to do
	}

	return filepath.Walk(injectDir, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from inject/ dir
		relPath, err := filepath.Rel(injectDir, srcPath)
		if err != nil {
			return err
		}

		// Skip the root inject/ dir itself
		if relPath == "." {
			return nil
		}

		destPath := filepath.Join(worktreePath, relPath)

		if info.IsDir() {
			// Create directory in worktree if it doesn't exist
			return os.MkdirAll(destPath, 0755)
		}

		// Skip if destination already exists (don't overwrite)
		if _, err := os.Lstat(destPath); err == nil {
			return nil
		}

		// Compute relative symlink target from dest to source
		relTarget, err := filepath.Rel(filepath.Dir(destPath), srcPath)
		if err != nil {
			return fmt.Errorf("could not compute relative path: %w", err)
		}

		return os.Symlink(relTarget, destPath)
	})
}

// InjectFilesForFeature re-syncs inject/ into all active worktrees for a feature.
func InjectFilesForFeature(featurePath string) (int, error) {
	wtDir := filepath.Join(featurePath, "worktrees")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		return 0, nil // no worktrees dir
	}

	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wtPath := filepath.Join(wtDir, e.Name())
		if err := InjectFiles(featurePath, wtPath); err != nil {
			fmt.Printf("  Warning: inject failed for %s: %v\n", e.Name(), err)
		} else {
			count++
		}
	}
	return count, nil
}
