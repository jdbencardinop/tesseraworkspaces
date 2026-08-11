package internal

import (
	"errors"
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
//   - the name or the directory is claimed by a registered sibling space
//   - source is a symlink
//   - destination already exists (collision)
//   - move fails
func MigrateFeatureLayout(ws Workspace, feature string) (err error) {
	if err := validateFeatureName(feature); err != nil {
		return err
	}

	src := filepath.Join(ws.MetadataRoot, feature)
	dst := filepath.Join(ws.MetadataRoot, "features", feature)

	// Refuse to move a directory whose name is owned by a registered sibling
	// space, or that still contains a registered target, before any
	// filesystem work. The transaction also refuses a destination name owned
	// by a features/<name> space, which the collision check below cannot see,
	// and it holds the spaces lock across os.Rename so no concurrent
	// `tws space add` can register into the directory being moved.
	tx, err := BeginSpacesLayoutMigration(ws.MetadataRoot,
		[]LayoutMigrationTarget{{Feature: feature, Path: src}})
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, tx.Release())
	}()

	// Reject symlinks at source.
	info, statErr := os.Lstat(src)
	if statErr != nil {
		return fmt.Errorf("source %s does not exist: %w", src, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source %s is a symlink; refusing to migrate", src)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %s is not a directory", src)
	}

	// Check destination collision.
	if _, dstErr := os.Lstat(dst); dstErr == nil {
		return fmt.Errorf("destination %s already exists (collision)", dst)
	}

	// Ensure features/ parent exists.
	if mkErr := os.MkdirAll(filepath.Dir(dst), 0755); mkErr != nil {
		return fmt.Errorf("creating features dir: %w", mkErr)
	}

	return os.Rename(src, dst)
}

// MigrateAllFeatures migrates all legacy features to the new layout.
// Preflight: one spaces transaction for the whole batch, then all sources and
// destinations, before moving any.
// On failure mid-run: best-effort rollback of previously moved features.
func MigrateAllFeatures(ws Workspace) (result MigrateResult) {
	if ws.Mode != ModeCheckout {
		result.Errors = append(result.Errors, "migrate-layout requires checkout mode")
		return result
	}

	// Discover legacy candidates (dirs in MetadataRoot that are not reserved).
	// Discovery is read-only, so it may precede the spaces preflight; nothing
	// is created or moved until the whole batch has been cleared.
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

	// Fail-closed spaces preflight, executed exactly once for the whole batch
	// and always before `features/` is created or anything is moved. A
	// registered name, or a registered target inside any candidate, is an
	// error and never a skip: one registration blocks every candidate rather
	// than producing a partial run.
	targets := make([]LayoutMigrationTarget, 0, len(candidates))
	for _, name := range candidates {
		targets = append(targets, LayoutMigrationTarget{
			Feature: name,
			Path:    filepath.Join(ws.MetadataRoot, name),
		})
	}
	tx, txErr := BeginSpacesLayoutMigration(ws.MetadataRoot, targets)
	if txErr != nil {
		result.Errors = append(result.Errors, txErr.Error())
		return result
	}
	// The lock is held across every move and any rollback below.
	defer func() {
		if relErr := tx.Release(); relErr != nil {
			result.Errors = append(result.Errors, relErr.Error())
		}
	}()

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
