package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTestDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}

func makeTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateFeatureLayout_Single(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	// Create legacy feature.
	makeTestDir(t, filepath.Join(dir, "myfeat", "worktrees"))
	makeTestDir(t, filepath.Join(dir, "features"))

	if err := MigrateFeatureLayout(ws, "myfeat"); err != nil {
		t.Fatal(err)
	}

	// Verify moved.
	if _, err := os.Stat(filepath.Join(dir, "features", "myfeat", "worktrees")); err != nil {
		t.Error("feature not at new location")
	}
	if _, err := os.Stat(filepath.Join(dir, "myfeat")); err == nil {
		t.Error("legacy location should not exist")
	}
}

func TestMigrateFeatureLayout_CollisionRejected(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "feat"))
	makeTestDir(t, filepath.Join(dir, "features", "feat"))

	err := MigrateFeatureLayout(ws, "feat")
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("expected collision in error, got: %v", err)
	}
}

func TestMigrateFeatureLayout_InvalidName(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	for _, name := range []string{"..", "a/b", "state", ""} {
		err := MigrateFeatureLayout(ws, name)
		if err == nil {
			t.Errorf("expected error for %q", name)
		}
	}
}

func TestMigrateAllFeatures_PreflightCollision(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	// Create legacy features with one collision.
	makeTestDir(t, filepath.Join(dir, "alpha"))
	makeTestDir(t, filepath.Join(dir, "beta"))
	makeTestDir(t, filepath.Join(dir, "features", "beta")) // collision

	result := MigrateAllFeatures(ws)

	// alpha should NOT have been moved since beta would fail at preflight.
	// Actually our implementation skips collisions and moves the rest.
	// Let's verify: alpha migrated, beta skipped.
	if len(result.Skipped) != 1 || result.Skipped[0] != "beta" {
		t.Errorf("expected beta skipped, got: %v", result.Skipped)
	}
	if len(result.Migrated) != 1 || result.Migrated[0] != "alpha" {
		t.Errorf("expected alpha migrated, got: %v", result.Migrated)
	}
}

func TestMigrateAllFeatures_Idempotent(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "feat"))
	makeTestDir(t, filepath.Join(dir, "features"))

	result := MigrateAllFeatures(ws)
	if len(result.Migrated) != 1 {
		t.Fatalf("expected 1 migrated, got %v", result)
	}

	// Run again — nothing to do (no legacy candidates remain).
	result2 := MigrateAllFeatures(ws)
	if len(result2.Migrated) != 0 {
		t.Errorf("expected 0 migrated on re-run, got %v", result2.Migrated)
	}
	if len(result2.Errors) != 0 {
		t.Errorf("expected no errors on re-run, got %v", result2.Errors)
	}
}

func TestMigrateAllFeatures_ReservedDirsUntouched(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	// Create reserved dirs that should NOT be migrated.
	for _, reserved := range []string{"features", "state", "templates", "hooks"} {
		makeTestDir(t, filepath.Join(dir, reserved))
	}
	// One real feature.
	makeTestDir(t, filepath.Join(dir, "realfeat"))

	result := MigrateAllFeatures(ws)
	if len(result.Migrated) != 1 || result.Migrated[0] != "realfeat" {
		t.Errorf("expected only realfeat migrated, got %v", result)
	}

	// Reserved dirs still exist at original location.
	for _, reserved := range []string{"state", "templates", "hooks"} {
		if _, err := os.Stat(filepath.Join(dir, reserved)); err != nil {
			t.Errorf("reserved dir %s should not be moved", reserved)
		}
	}
}

func TestMigrateAllFeatures_SymlinkInCandidates(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	// Real feature.
	makeTestDir(t, filepath.Join(dir, "good"))
	// Symlink "feature" — os.ReadDir won't report it as IsDir, so it's skipped.
	target := t.TempDir()
	makeTestSymlink(t, target, filepath.Join(dir, "bad"))
	makeTestDir(t, filepath.Join(dir, "features"))

	result := MigrateAllFeatures(ws)
	// Only "good" is a candidate (symlink skipped by ReadDir).
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got: %v", result.Errors)
	}
	if len(result.Migrated) != 1 || result.Migrated[0] != "good" {
		t.Errorf("expected [good] migrated, got: %v", result.Migrated)
	}
}

func TestMigrateFeatureLayout_SymlinkRejectedViaLstat(t *testing.T) {
	// Direct single-feature migration rejects symlinks via Lstat.
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	target := t.TempDir()
	makeTestSymlink(t, target, filepath.Join(dir, "linked"))
	makeTestDir(t, filepath.Join(dir, "features"))

	err := MigrateFeatureLayout(ws, "linked")
	if err == nil {
		t.Fatal("expected symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected symlink in error, got: %v", err)
	}
}

func TestMigrateAllFeatures_NotCheckoutMode(t *testing.T) {
	ws := Workspace{MetadataRoot: t.TempDir(), Mode: ModeExternal}
	result := MigrateAllFeatures(ws)
	if len(result.Errors) == 0 {
		t.Error("expected error for non-checkout mode")
	}
}

func TestMigrateFeatureLayout_RegisteredSpaceRefused(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "learning"))
	makeTestDir(t, filepath.Join(dir, "other"))
	writeSpacesFixture(t, dir, `version: 1
spaces:
  - name: notes
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	err := MigrateFeatureLayout(ws, "learning")
	if err == nil || !strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("expected space conflict, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "learning")); statErr != nil {
		t.Error("registered space directory was moved")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "features")); statErr == nil {
		t.Error("features/ must not be created by a refused migration")
	}

	// An unrelated legacy feature still migrates normally.
	if err := MigrateFeatureLayout(ws, "other"); err != nil {
		t.Fatalf("unrelated feature must still migrate: %v", err)
	}
}

func TestMigrateFeatureLayout_FeaturesOwnerRefused(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "scratch"))
	writeSpacesFixture(t, dir, `version: 1
spaces:
  - name: scratch
    kind: docs
    path: features/scratch
    added_at: 2026-01-01T00:00:00Z
`)

	err := MigrateFeatureLayout(ws, "scratch")
	if err == nil || !strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("guard must consult owners.Features, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "features")); statErr == nil {
		t.Error("features/ must not be created")
	}
}

func TestMigrateFeatureLayout_MalformedSpacesRefused(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "alpha"))
	writeSpacesFixture(t, dir, "version: 99\nspaces: []\n")

	err := MigrateFeatureLayout(ws, "alpha")
	if err == nil || !strings.Contains(err.Error(), "cannot verify registered spaces in ") {
		t.Fatalf("expected strict failure, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "alpha")); statErr != nil {
		t.Error("nothing may move on untrusted metadata")
	}
}

func TestMigrateAllFeatures_SpaceConflictBlocksBatch(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "alpha"))
	makeTestDir(t, filepath.Join(dir, "beta"))
	makeTestDir(t, filepath.Join(dir, "learning"))
	writeSpacesFixture(t, dir, `version: 1
spaces:
  - name: notes
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	result := MigrateAllFeatures(ws)
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "top-level directory of registered space") {
		t.Fatalf("expected one space conflict error, got %v", result.Errors)
	}
	if len(result.Migrated) != 0 {
		t.Fatalf("--all must be all-or-nothing, got migrated=%v", result.Migrated)
	}
	for _, name := range result.Skipped {
		if name == "learning" {
			t.Fatal("a registered space must be an error, never a skip")
		}
	}
	for _, name := range []string{"alpha", "beta", "learning"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s must not move", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "features")); err == nil {
		t.Error("features/ must not be created")
	}

	// Once the registration is gone, the directory is an ordinary candidate.
	if err := os.Remove(filepath.Join(dir, spacesFileName)); err != nil {
		t.Fatal(err)
	}
	rerun := MigrateAllFeatures(ws)
	if len(rerun.Errors) != 0 {
		t.Fatalf("rerun errors: %v", rerun.Errors)
	}
	if len(rerun.Migrated) != 3 {
		t.Fatalf("expected all three migrated, got %v", rerun.Migrated)
	}
}

func TestMigrateAllFeatures_FeaturesOwnerBlocksBatch(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "scratch"))
	writeSpacesFixture(t, dir, `version: 1
spaces:
  - name: scratch
    kind: docs
    path: features/scratch
    added_at: 2026-01-01T00:00:00Z
`)

	result := MigrateAllFeatures(ws)
	if len(result.Errors) != 1 {
		t.Fatalf("expected one error, got %v", result.Errors)
	}
	if _, err := os.Stat(filepath.Join(dir, "features")); err == nil {
		t.Error("features/ must not be created")
	}
}

func TestMigrateAllFeatures_MalformedSpacesFailsClosed(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "alpha"))
	writeSpacesFixture(t, dir, "version: 1\nspaces: [\n")

	result := MigrateAllFeatures(ws)
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "cannot verify registered spaces in ") {
		t.Fatalf("expected canonical preflight failure, got %v", result.Errors)
	}
	if len(result.Migrated) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("no candidate may be inspected: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "features")); err == nil {
		t.Error("features/ must not be created")
	}
}

func TestMigrateAllFeatures_AbsentSpacesUnchanged(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "alpha"))
	result := MigrateAllFeatures(ws)
	if len(result.Errors) != 0 || len(result.Migrated) != 1 {
		t.Fatalf("absent registry must not change behaviour: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, spacesFileName)); err == nil {
		t.Error("migrate must never create spaces.yaml")
	}
	if _, err := os.Stat(filepath.Join(dir, spacesLockName)); err == nil {
		t.Error("migrate must never create .spaces.lock")
	}
}

// nestedSpaceFixture builds a legacy feature directory that carries a feature
// signal — so its name is never claimed as a top-level space — holding a
// registered target underneath it.
func nestedSpaceFixture(t *testing.T, dir, stored string) {
	t.Helper()
	makeTestDir(t, filepath.Join(dir, "acme", "worktrees"))
	makeTestDir(t, filepath.Join(dir, "acme", "patching"))
	writeSpacesFixture(t, dir, `version: 1
spaces:
  - name: patching
    kind: patching
    feature: acme
    path: `+stored+`
    added_at: 2026-01-01T00:00:00Z
`)
	if err := GuardFeatureName(dir, "acme"); err != nil {
		t.Fatalf("fixture precondition: the feature name must not be claimed: %v", err)
	}
}

func TestMigrateFeatureLayout_NestedTargetRefused(t *testing.T) {
	for _, tc := range []struct{ name, stored string }{
		{"relative", "acme/patching"},
		{"absolute", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}
			stored := tc.stored
			if stored == "" {
				stored = filepath.Join(dir, "acme", "patching")
			}
			nestedSpaceFixture(t, dir, stored)

			err := MigrateFeatureLayout(ws, "acme")
			if err == nil {
				t.Fatal("expected the migration to be refused")
			}
			for _, want := range []string{
				`cannot migrate legacy feature "acme" to the new checkout layout`,
				"'tws space remove patching --feature acme'",
				"can be re-added with 'tws space add' once the migration is done",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q is missing %q", err.Error(), want)
				}
			}
			if _, statErr := os.Stat(filepath.Join(dir, "acme", "patching")); statErr != nil {
				t.Error("the registered target must not move")
			}
			if _, statErr := os.Stat(filepath.Join(dir, "features")); statErr == nil {
				t.Error("features/ must not be created")
			}
		})
	}
}

func TestMigrateAllFeatures_NestedTargetBlocksBatch(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	nestedSpaceFixture(t, dir, "acme/patching")
	makeTestDir(t, filepath.Join(dir, "beta"))

	result := MigrateAllFeatures(ws)
	if len(result.Errors) != 1 ||
		!strings.Contains(result.Errors[0], "to the new checkout layout") {
		t.Fatalf("expected one containment error, got %v", result.Errors)
	}
	if len(result.Migrated) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("one blocked feature must abort every candidate: %+v", result)
	}
	for _, name := range []string{"acme", "beta"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s must not move", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "features")); err == nil {
		t.Error("features/ must not be created")
	}

	// Once the registration is gone, both features migrate.
	if err := os.Remove(filepath.Join(dir, spacesFileName)); err != nil {
		t.Fatal(err)
	}
	rerun := MigrateAllFeatures(ws)
	if len(rerun.Errors) != 0 || len(rerun.Migrated) != 2 {
		t.Fatalf("rerun: %+v", rerun)
	}
	if err := GuardFeatureName(dir, "acme"); err != nil {
		t.Fatalf("the migrated feature must stay usable: %v", err)
	}
}

func TestMigrateFeatureLayout_HoldsSpacesLockAcrossRename(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	makeTestDir(t, filepath.Join(dir, "alpha"))
	makeTestDir(t, filepath.Join(dir, "learning"))
	writeSpacesFixture(t, dir, `version: 1
spaces:
  - name: notes
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	if err := MigrateFeatureLayout(ws, "alpha"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "features", "alpha")); err != nil {
		t.Fatal("alpha should be migrated")
	}
	// A registry exists, so the lock file is legitimately created — and it
	// must be released, which a fresh acquisition proves.
	if _, err := os.Lstat(filepath.Join(dir, spacesLockName)); err != nil {
		t.Fatalf("the migration must take the spaces lock when a registry exists: %v", err)
	}
	lock, err := acquireSpacesLock(dir)
	if err != nil {
		t.Fatalf("the lock must have been released: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
