package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func runMigrateLayout(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := migrateLayoutCmd()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	return out, err
}

func TestSpaceMigrate_SingleFeatureRefusesRegisteredSpace(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")

	mustMkdir(t, filepath.Join(root, "learning"))
	mustMkdir(t, filepath.Join(root, "other"))
	writeSpaces(t, root, registeredLearningFixture("learning"))

	_, err := runMigrateLayout(t, "learning")
	if err == nil || !strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "learning")); statErr != nil {
		t.Fatal("the registered space directory must not move")
	}
	if _, statErr := os.Stat(filepath.Join(root, "features")); statErr == nil {
		t.Fatal("features/ must not be created")
	}

	if _, err := runMigrateLayout(t, "other"); err != nil {
		t.Fatalf("an unrelated legacy feature must still migrate: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "features", "other")); statErr != nil {
		t.Fatal("other should be migrated")
	}
}

func TestSpaceMigrate_AllFailsClosedAndRecoversAfterRemove(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")

	for _, name := range []string{"alpha", "beta", "learning"} {
		mustMkdir(t, filepath.Join(root, name))
	}
	writeSpaces(t, root, registeredLearningFixture("learning"))

	out, err := runMigrateLayout(t, "--all")
	if err == nil || err.Error() != "migration failed with 1 error(s)" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "error: cannot use feature name \"learning\"") {
		t.Fatalf("output = %q", out)
	}
	if strings.Contains(out, "skipped") {
		t.Fatalf("a registered space must never be reported as skipped: %q", out)
	}
	for _, name := range []string{"alpha", "beta", "learning"} {
		if _, statErr := os.Stat(filepath.Join(root, name)); statErr != nil {
			t.Fatalf("%s must not move", name)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, "features")); statErr == nil {
		t.Fatal("features/ must not be created")
	}

	if _, err := runSpace(t, "remove", "notes"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := runMigrateLayout(t, "--all"); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	for _, name := range []string{"alpha", "beta", "learning"} {
		if _, statErr := os.Stat(filepath.Join(root, "features", name)); statErr != nil {
			t.Fatalf("%s should be migrated after the registration is gone", name)
		}
	}
}

func TestSpaceMigrate_FeaturesLayoutOwnerRefused(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")

	mustMkdir(t, filepath.Join(root, "scratch"))
	writeSpaces(t, root, registeredLearningFixture("features/scratch"))

	if _, statErr := os.Stat(filepath.Join(root, "features", "scratch")); statErr == nil {
		t.Fatal("fixture precondition: destination must not exist yet")
	}

	_, err := runMigrateLayout(t, "scratch")
	if err == nil || !strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("single err = %v", err)
	}

	_, err = runMigrateLayout(t, "--all")
	if err == nil || err.Error() != "migration failed with 1 error(s)" {
		t.Fatalf("--all err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "scratch")); statErr != nil {
		t.Fatal("nothing may move")
	}
}

func TestSpaceMigrate_AbsentRegistryUnchanged(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")

	mustMkdir(t, filepath.Join(root, "alpha"))

	if _, err := runMigrateLayout(t, "--all"); err != nil {
		t.Fatalf("--all: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "features", "alpha")); statErr != nil {
		t.Fatal("alpha should be migrated")
	}
	for _, p := range []string{spacesFileIn(root), spacesLockIn(root)} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s must not be created", p)
		}
	}
}

func TestSpaceMigrate_CompletionBestEffort(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")

	mustMkdir(t, filepath.Join(root, "alpha"))
	mustMkdir(t, filepath.Join(root, "learning"))
	writeSpaces(t, root, registeredLearningFixture("learning"))

	ws := requireWorkspaceForTest(t, repo)
	names := ws.LegacyFeatureNames()
	if contains(names, "learning") {
		t.Fatalf("completion offered the registered space: %v", names)
	}
	if !contains(names, "alpha") {
		t.Fatalf("completion lost a real legacy feature: %v", names)
	}

	writeSpaces(t, root, "version: 99\nspaces: []\n")
	if got := ws.LegacyFeatureNames(); got != nil {
		t.Fatalf("untrusted metadata must yield no candidates, got %v", got)
	}
}

// ---------- nested targets inside a legacy feature directory ----------

// legacyFeatureWithNestedSpace builds the checkout fixture this feature's
// migration gap is about: a legacy feature directory <root>/acme that carries
// a feature signal — so it is never claimed as a top-level space name — with a
// feature-scoped space registered at <root>/acme/patching.
func legacyFeatureWithNestedSpace(t *testing.T) (root, patching string) {
	t.Helper()
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root = filepath.Join(repo, ".tws")

	mustMkdir(t, filepath.Join(root, "acme", "worktrees"))
	patching = mustMkdir(t, filepath.Join(root, "acme", "patching"))
	if err := os.WriteFile(filepath.Join(patching, "notes.md"), []byte("patch bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := runSpace(t, "add", "patching", patching, "--kind", "patching", "--feature", "acme"); err != nil {
		t.Fatalf("space add: %v", err)
	}
	if internal.GuardFeatureName(root, "acme") != nil {
		t.Fatal("fixture precondition: a nested target must not claim the feature name")
	}
	return root, patching
}

// assertNestedSpaceIntact asserts the refusal changed nothing on disk and left
// the registry entry healthy.
func assertNestedSpaceIntact(t *testing.T, root, patching string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "acme", "worktrees")); err != nil {
		t.Fatal("the legacy source must stay in place")
	}
	if got := string(mustReadFile(t, filepath.Join(patching, "notes.md"))); got != "patch bytes\n" {
		t.Fatalf("registered target bytes changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "features")); err == nil {
		t.Fatal("features/ must not be created by a refused migration")
	}

	out, err := runSpace(t, "list", "--json", "--all")
	if err != nil {
		t.Fatalf("space list: %v", err)
	}
	views := decodeSpaceViews(t, out)
	if len(views) != 1 || views[0]["name"] != "patching" || views[0]["status"] != "ok" {
		t.Fatalf("the entry must remain registered and status ok: %s", out)
	}
}

func TestSpaceMigrate_SingleRefusesNestedRegisteredTarget(t *testing.T) {
	root, patching := legacyFeatureWithNestedSpace(t)

	_, err := runMigrateLayout(t, "acme")
	if err == nil {
		t.Fatal("migration must refuse while a registered target lives inside the feature")
	}
	for _, want := range []string{
		`cannot migrate legacy feature "acme" to the new checkout layout`,
		"1 registered space lives inside ",
		filepath.Join(".tws", "acme") + " (patching (feature acme))",
		"'tws space remove patching --feature acme'",
		"can be re-added with 'tws space add' once the migration is done",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q is missing %q", err.Error(), want)
		}
	}
	assertNestedSpaceIntact(t, root, patching)

	// The printed guidance is directly runnable, and the migration then works.
	if _, err := runSpace(t, "remove", "patching", "--feature", "acme"); err != nil {
		t.Fatalf("the suggested removal must be executable: %v", err)
	}
	if _, err := runMigrateLayout(t, "acme"); err != nil {
		t.Fatalf("migration must succeed once the registration is gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "features", "acme", "patching", "notes.md")); err != nil {
		t.Fatal("the whole feature, including the former target, must move intact")
	}
	if _, err := os.Stat(filepath.Join(root, "acme")); err == nil {
		t.Fatal("the legacy directory must be gone after a successful migration")
	}

	// The migrated feature is still an ordinary, usable feature.
	if err := internal.GuardFeatureName(root, "acme"); err != nil {
		t.Fatalf("GuardFeatureName after migration: %v", err)
	}
	if _, err := runSpace(t, "add", "patching",
		filepath.Join(root, "features", "acme", "patching"),
		"--kind", "patching", "--feature", "acme"); err != nil {
		t.Fatalf("the link must be re-addable under the new layout: %v", err)
	}
}

func TestSpaceMigrate_AllRefusesNestedRegisteredTargetAndBlocksBatch(t *testing.T) {
	root, patching := legacyFeatureWithNestedSpace(t)
	mustMkdir(t, filepath.Join(root, "beta"))

	out, err := runMigrateLayout(t, "--all")
	if err == nil || err.Error() != "migration failed with 1 error(s)" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, `error: cannot migrate legacy feature "acme" to the new checkout layout`) ||
		!strings.Contains(out, "'tws space remove patching --feature acme'") {
		t.Fatalf("output = %q", out)
	}
	if strings.Contains(out, "skipped") || strings.Contains(out, "migrated:") {
		t.Fatalf("a blocked batch must move nothing: %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(root, "beta")); statErr != nil {
		t.Fatal("one blocked feature must abort every candidate")
	}
	assertNestedSpaceIntact(t, root, patching)

	if _, err := runSpace(t, "remove", "patching", "--feature", "acme"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := runMigrateLayout(t, "--all"); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	for _, name := range []string{"acme", "beta"} {
		if _, statErr := os.Stat(filepath.Join(root, "features", name)); statErr != nil {
			t.Fatalf("%s should be migrated after the registration is gone", name)
		}
	}
	if err := internal.GuardFeatureName(root, "acme"); err != nil {
		t.Fatalf("GuardFeatureName after migration: %v", err)
	}
}

func TestSpaceMigrate_AbsoluteNestedTargetRefused(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")

	mustMkdir(t, filepath.Join(root, "acme", "worktrees"))
	tickets := mustMkdir(t, filepath.Join(root, "acme", "tickets"))
	absoluteInRootFixture(t, root, "tickets", tickets)

	for _, args := range [][]string{{"acme"}, {"--all"}} {
		_, err := runMigrateLayout(t, args...)
		if err == nil {
			t.Fatalf("%v must be refused", args)
		}
	}
	if _, err := os.Stat(tickets); err != nil {
		t.Fatal("the absolute in-root target must survive")
	}
	if _, err := os.Stat(filepath.Join(root, "features")); err == nil {
		t.Fatal("features/ must not be created")
	}

	// The message names the workspace-wide scope of the hand-edited entry.
	_, err := runMigrateLayout(t, "acme")
	if !strings.Contains(err.Error(), "'tws space remove tickets --workspace'") {
		t.Fatalf("error = %v", err)
	}
}
