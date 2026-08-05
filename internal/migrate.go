package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// MigrateResult describes the outcome of a migration run.
type MigrateResult struct {
	Migrated []string // features successfully moved
	Skipped  []string // features already in new layout
	Errors   []string // rollback/failure messages
}

// MigrateFeatureLayout migrates a single feature from legacy (.tws/<name>)
// to new layout (.tws/features/<name>). Returns an error if:
//   - feature name is invalid/unsafe
//   - source is a symlink
//   - destination already exists (collision)
//   - move fails
func MigrateFeatureLayout(ws Workspace, feature string) error {
	if err := validateFeatureName(feature); err != nil {
		return err
	}

	src := filepath.Join(ws.MetadataRoot, feature)
	dst := filepath.Join(ws.MetadataRoot, "features", feature)

	// Reject symlinks at source.
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("source %s does not exist: %w", src, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source %s is a symlink; refusing to migrate", src)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %s is not a directory", src)
	}

	// Check destination collision.
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("destination %s already exists (collision)", dst)
	}

	// Ensure features/ parent exists.
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating features dir: %w", err)
	}

	return os.Rename(src, dst)
}

// MigrateAllFeatures migrates all legacy features to the new layout.
// Preflight: checks ALL sources and destinations before moving any.
// On failure mid-run: best-effort rollback of previously moved features.
func MigrateAllFeatures(ws Workspace) MigrateResult {
	result := MigrateResult{}

	if ws.Mode != ModeCheckout {
		result.Errors = append(result.Errors, "migrate-layout requires checkout mode")
		return result
	}

	// Discover legacy candidates (dirs in MetadataRoot that are not reserved).
	entries, err := os.ReadDir(ws.MetadataRoot)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("reading %s: %v", ws.MetadataRoot, err))
		return result
	}

	var candidates []string
	for _, e := range entries {
		if !e.IsDir() || isReservedDir(e.Name()) {
			continue
		}
		candidates = append(candidates, e.Name())
	}
	sort.Strings(candidates)

	if len(candidates) == 0 {
		return result
	}

	// --- Preflight: check all sources and destinations ---
	type migration struct {
		name string
		src  string
		dst  string
	}
	var migrations []migration

	for _, name := range candidates {
		src := filepath.Join(ws.MetadataRoot, name)
		dst := filepath.Join(ws.MetadataRoot, "features", name)

		// Reject symlinks.
		info, err := os.Lstat(src)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("stat %s: %v", src, err))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			result.Errors = append(result.Errors, fmt.Sprintf("%s is a symlink; skipped", src))
			continue
		}

		// Check if already migrated (exists under features/).
		if _, err := os.Lstat(dst); err == nil {
			result.Skipped = append(result.Skipped, name)
			continue
		}

		migrations = append(migrations, migration{name: name, src: src, dst: dst})
	}

	// If any preflight errors detected, abort without moving anything.
	if len(result.Errors) > 0 {
		return result
	}

	// Ensure features/ exists.
	featuresDir := filepath.Join(ws.MetadataRoot, "features")
	if err := os.MkdirAll(featuresDir, 0755); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("creating features dir: %v", err))
		return result
	}

	// --- Execute moves with rollback on failure ---
	var moved []migration
	for _, m := range migrations {
		if err := os.Rename(m.src, m.dst); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("moving %s: %v", m.name, err))
			// Best-effort rollback.
			for _, done := range moved {
				if rbErr := os.Rename(done.dst, done.src); rbErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("rollback %s failed: %v", done.name, rbErr))
				}
			}
			return result
		}
		moved = append(moved, m)
		result.Migrated = append(result.Migrated, m.name)
	}

	return result
}
