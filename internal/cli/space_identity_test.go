package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// requireCaseInsensitiveFS probes dir for case-insensitivity and skips the
// test when the volume is case-sensitive. The probe creates and removes one
// directory and is always run before any before/after snapshot.
func requireCaseInsensitiveFS(t *testing.T, dir string) {
	t.Helper()
	upper := filepath.Join(dir, "TwsCaseProbe")
	if err := os.MkdirAll(upper, 0755); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(upper); err != nil {
			t.Fatal(err)
		}
	}()

	upperInfo, err := os.Stat(upper)
	if err != nil {
		t.Fatal(err)
	}
	lowerInfo, err := os.Stat(filepath.Join(dir, "twscaseprobe"))
	if err != nil || !os.SameFile(upperInfo, lowerInfo) {
		t.Skip("filesystem is case-sensitive; the case-collision path cannot be exercised here")
	}
}

// mustReadFile returns file bytes, failing the test when unreadable.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// absoluteInRootFixture writes a hand-edited registry whose stored path is
// absolute yet resolves inside the spaces root — a form tws itself never
// writes but must still honour.
func absoluteInRootFixture(t *testing.T, root, name, target string) {
	t.Helper()
	if !filepath.IsAbs(target) {
		t.Fatalf("fixture target %q must be absolute", target)
	}
	writeSpaces(t, root, "version: 1\nspaces:\n  - name: "+name+
		"\n    kind: docs\n    path: "+target+
		"\n    added_at: 2026-01-01T00:00:00Z\n")
}

// ---------- absolute stored paths that resolve inside the root ----------

func TestSpaceIdentity_AbsoluteInRootEntryIsOwned(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("real", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	learning := mustMkdir(t, filepath.Join(root, "learning"))
	notes := filepath.Join(learning, "notes.md")
	if err := os.WriteFile(notes, []byte("owned bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	absoluteInRootFixture(t, root, "notes", learning)

	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if contains(features, "learning") {
		t.Fatalf("an absolute in-root entry must still be excluded from listings: %v", features)
	}
	if names := internal.ListFeatures(); contains(names, "learning") {
		t.Fatalf("completion offered the registered space: %v", names)
	}

	if err := internal.GuardFeatureName(root, "learning"); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("guard err = %v", err)
	}

	// tws add and tws delete both refuse, and the bytes survive.
	if err := addExternal("learning", nil, "", "", false, false, false); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("tws add err = %v", err)
	}
	if err := deleteExternal("learning", false, false); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("tws delete err = %v", err)
	}
	if got := string(mustReadFile(t, notes)); got != "owned bytes\n" {
		t.Fatalf("registered target bytes changed: %q", got)
	}

	// `space list --feature` may not accept a registered space name either.
	if _, err := runSpace(t, "list", "--feature", "learning"); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("space list --feature err = %v", err)
	}

	// The entry itself still lists, with its absolute path resolved verbatim.
	out, err := runSpace(t, "list", "--json", "--all")
	if err != nil {
		t.Fatal(err)
	}
	views := decodeSpaceViews(t, out)
	if len(views) != 1 || views[0]["resolved_path"] != learning {
		t.Fatalf("unexpected views: %s", out)
	}
}

func TestSpaceIdentity_AbsoluteInRootEntryBlocksDeleteContainment(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	featurePath := internal.FeaturePath("acme")
	tickets := mustMkdir(t, filepath.Join(featurePath, "tickets"))
	stored := filepath.Join(tickets, "store.md")
	if err := os.WriteFile(stored, []byte("ticket bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	absoluteInRootFixture(t, root, "tickets", tickets)

	err := deleteExternal("acme", false, false)
	if err == nil || !strings.Contains(err.Error(), "1 registered space lives inside") {
		t.Fatalf("delete err = %v", err)
	}
	if !strings.Contains(err.Error(), "'tws space remove tickets --workspace'") {
		t.Fatalf("delete refusal must name the scoped removal command: %v", err)
	}
	if got := string(mustReadFile(t, stored)); got != "ticket bytes\n" {
		t.Fatalf("nested target bytes changed: %q", got)
	}
	if _, statErr := os.Stat(featurePath); statErr != nil {
		t.Fatal("the feature directory must survive a refused delete")
	}
}

func TestSpaceIdentity_AbsoluteInRootEntryBlocksMigrate(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")
	ws := requireWorkspaceForTest(t, repo)

	legacy := mustMkdir(t, filepath.Join(root, "scratch"))
	if err := os.WriteFile(filepath.Join(legacy, "keep.md"), []byte("legacy bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The destination of the migration is registered, absolutely, in-root.
	absoluteInRootFixture(t, root, "scratch", filepath.Join(root, "features", "scratch"))

	if err := internal.MigrateFeatureLayout(ws, "scratch"); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("migrate-layout err = %v", err)
	}
	result := internal.MigrateAllFeatures(ws)
	if len(result.Errors) == 0 {
		t.Fatalf("migrate --all must fail closed, got %+v", result)
	}
	if len(result.Migrated) != 0 {
		t.Fatalf("no candidate may move: %+v", result)
	}
	if got := string(mustReadFile(t, filepath.Join(legacy, "keep.md"))); got != "legacy bytes\n" {
		t.Fatalf("legacy bytes changed: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "features", "scratch")); err == nil {
		t.Fatal("the registered destination must not be created")
	}
}

// ---------- same directory, different letter case ----------

func TestSpaceIdentity_CaseInsensitiveNameRefusesAddAndDelete(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	mustMkdir(t, root)
	requireCaseInsensitiveFS(t, root)

	learning := mustMkdir(t, filepath.Join(root, "learning"))
	notes := filepath.Join(learning, "notes.md")
	if err := os.WriteFile(notes, []byte("case bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "learning", learning, "--kind", "learning"); err != nil {
		t.Fatal(err)
	}
	registryBefore := mustReadFile(t, spacesFileIn(root))

	for _, spelling := range []string{"LEARNING", "Learning"} {
		if err := internal.GuardFeatureName(root, spelling); err == nil ||
			!strings.Contains(err.Error(), "top-level directory of registered space") {
			t.Fatalf("guard %q err = %v", spelling, err)
		}
		if err := addExternal(spelling, nil, "", "", false, false, false); err == nil ||
			!strings.Contains(err.Error(), "top-level directory of registered space") {
			t.Fatalf("tws add %q err = %v", spelling, err)
		}
		if err := deleteExternal(spelling, false, false); err == nil ||
			!strings.Contains(err.Error(), "top-level directory of registered space") {
			t.Fatalf("tws delete %q err = %v", spelling, err)
		}
	}

	if got := string(mustReadFile(t, notes)); got != "case bytes\n" {
		t.Fatalf("registered target bytes changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(learning, "worktrees")); err == nil {
		t.Fatal("a refused add must not create worktrees/ inside the registered space")
	}
	if got := mustReadFile(t, spacesFileIn(root)); string(got) != string(registryBefore) {
		t.Fatal("refused lifecycle commands must leave spaces.yaml byte-identical")
	}
}

func TestSpaceIdentity_CaseInsensitiveNestedTargetBlocksDelete(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	mustMkdir(t, root)
	requireCaseInsensitiveFS(t, root)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	featurePath := internal.FeaturePath("acme")
	tickets := mustMkdir(t, filepath.Join(featurePath, "tickets"))
	stored := filepath.Join(tickets, "store.md")
	if err := os.WriteFile(stored, []byte("nested bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "tickets", tickets, "--kind", "tickets"); err != nil {
		t.Fatal(err)
	}
	registryBefore := mustReadFile(t, spacesFileIn(root))

	// The feature is spelled differently but names the same directory.
	err := deleteExternal("ACME", false, false)
	if err == nil || !strings.Contains(err.Error(), "1 registered space lives inside") {
		t.Fatalf("delete err = %v", err)
	}
	if got := string(mustReadFile(t, stored)); got != "nested bytes\n" {
		t.Fatalf("nested target bytes changed: %q", got)
	}
	if _, statErr := os.Stat(featurePath); statErr != nil {
		t.Fatal("the feature directory must survive a refused delete")
	}
	if got := mustReadFile(t, spacesFileIn(root)); string(got) != string(registryBefore) {
		t.Fatal("a refused delete must leave spaces.yaml byte-identical")
	}

	// Removing the entry with the scoped command unblocks the delete.
	if _, err := runSpace(t, "remove", "tickets", "--workspace"); err != nil {
		t.Fatal(err)
	}
	if err := deleteExternal("acme", false, false); err != nil {
		t.Fatalf("delete after removing the entry: %v", err)
	}
}

// requireNoFeatureSignals fails when dir carries any signal that would make it
// look like a tws feature, which is what a wrongly allowed `tws add` creates.
func requireNoFeatureSignals(t *testing.T, dir string) {
	t.Helper()
	for _, signal := range []string{"stack.yaml", "worktrees", "FEATURE.md"} {
		if _, err := os.Stat(filepath.Join(dir, signal)); err == nil {
			t.Fatalf("a refused add must not create %s in %s", signal, dir)
		}
	}
}

// nestedSpaceFixture registers "<root>/Learning/notes" — a target nested one
// level below the directory the claim actually covers — and returns the
// claimed parent directory and the file whose bytes must survive.
func nestedSpaceFixture(t *testing.T, root string) (parent, kept string) {
	t.Helper()
	parent = mustMkdir(t, filepath.Join(root, "Learning"))
	target := mustMkdir(t, filepath.Join(parent, "notes"))
	kept = filepath.Join(target, "keep.md")
	if err := os.WriteFile(kept, []byte("nested bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "notes", target, "--kind", "learning"); err != nil {
		t.Fatal(err)
	}
	return parent, kept
}

func TestSpaceIdentity_NestedTargetClaimsParentByExactSpelling(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	mustMkdir(t, root)

	parent, kept := nestedSpaceFixture(t, root)
	registryBefore := mustReadFile(t, spacesFileIn(root))

	// The exact spelling of the claimed parent is refused on every volume.
	if err := internal.GuardFeatureName(root, "Learning"); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("guard err = %v", err)
	}
	if err := addExternal("Learning", nil, "", "", false, false, false); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("tws add err = %v", err)
	}
	requireNoFeatureSignals(t, parent)
	if got := string(mustReadFile(t, kept)); got != "nested bytes\n" {
		t.Fatalf("registered target bytes changed: %q", got)
	}

	// Only the claimed parent is owned: the nested segment name is not, and
	// neither is an unrelated name.
	for _, feature := range []string{"notes", "unrelated"} {
		if err := internal.GuardFeatureName(root, feature); err != nil {
			t.Fatalf("guard %q must not claim an unowned directory: %v", feature, err)
		}
	}
	if got := mustReadFile(t, spacesFileIn(root)); string(got) != string(registryBefore) {
		t.Fatal("a refused add must leave spaces.yaml byte-identical")
	}
}

func TestSpaceIdentity_CaseInsensitiveNestedTargetRefusesAdd(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	mustMkdir(t, root)
	requireCaseInsensitiveFS(t, root)

	parent, kept := nestedSpaceFixture(t, root)
	registryBefore := mustReadFile(t, spacesFileIn(root))

	// "learning" misses the exact map, but names the claimed parent on a
	// case-insensitive volume, so identity must still refuse it.
	for _, spelling := range []string{"learning", "LEARNING"} {
		if err := internal.GuardFeatureName(root, spelling); err == nil ||
			!strings.Contains(err.Error(), "top-level directory of registered space") {
			t.Fatalf("guard %q err = %v", spelling, err)
		}
		if err := addExternal(spelling, nil, "", "", false, false, false); err == nil ||
			!strings.Contains(err.Error(), "top-level directory of registered space") {
			t.Fatalf("tws add %q err = %v", spelling, err)
		}
		requireNoFeatureSignals(t, filepath.Join(root, spelling))
	}

	requireNoFeatureSignals(t, parent)
	if got := string(mustReadFile(t, kept)); got != "nested bytes\n" {
		t.Fatalf("registered target bytes changed: %q", got)
	}
	if got := mustReadFile(t, spacesFileIn(root)); string(got) != string(registryBefore) {
		t.Fatal("a refused add must leave spaces.yaml byte-identical")
	}
}
