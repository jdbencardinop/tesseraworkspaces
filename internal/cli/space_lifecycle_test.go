package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func runRenameFeature(t *testing.T, oldName, newName string) error {
	t.Helper()
	cmd := renameCmd()
	cmd.SetArgs([]string{"feature", oldName, newName})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	var err error
	_ = captureStdout(t, func() { err = cmd.Execute() })
	return err
}

func TestSpaceLifecycle_DeleteRefusesNestedTargets(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	featurePath := internal.FeaturePath("acme")
	learning := mustMkdir(t, filepath.Join(featurePath, "learning"))
	patching := mustMkdir(t, filepath.Join(featurePath, "patching"))

	if _, err := runSpace(t, "add", "learning", learning, "--kind", "learning"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "patching", patching, "--kind", "patching", "--feature", "acme"); err != nil {
		t.Fatal(err)
	}

	err := deleteExternal("acme", false, false)
	if err == nil {
		t.Fatal("expected delete refusal")
	}
	for _, want := range []string{
		`cannot delete feature "acme"`,
		"2 registered spaces live inside",
		"learning (workspace)",
		"patching (feature acme)",
		"'tws space remove learning --workspace' and 'tws space remove patching --feature acme'",
		"move the directories out of the feature",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q missing %q", err, want)
		}
	}
	if _, statErr := os.Stat(featurePath); statErr != nil {
		t.Fatal("nothing may be deleted")
	}

	if _, err := runSpace(t, "remove", "learning", "--workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "remove", "patching", "--feature", "acme"); err != nil {
		t.Fatal(err)
	}
	if err := deleteExternal("acme", false, false); err != nil {
		t.Fatalf("delete after removing both entries: %v", err)
	}
	if _, statErr := os.Stat(featurePath); statErr == nil {
		t.Fatal("feature directory should be gone")
	}
}

func TestSpaceLifecycle_DeleteInclusiveContainmentAndGuardOrdering(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	featurePath := internal.FeaturePath("acme")

	// An absolute entry resolving to exactly the feature directory blocks.
	writeSpaces(t, root, "version: 1\nspaces:\n  - name: hub\n    kind: docs\n    path: "+featurePath+
		"\n    added_at: 2026-01-01T00:00:00Z\n")

	err := deleteExternal("acme", false, false)
	if err == nil || !strings.Contains(err.Error(), "1 registered space lives inside") {
		t.Fatalf("inclusive containment: %v", err)
	}

	// Guard ordering: a top-level relative space whose target IS the directory
	// being deleted reports the name conflict, not the containment message.
	// (The directory must carry no feature signal, otherwise the ownership
	// exception of §7.1 correctly refuses to claim it.)
	plain := mustMkdir(t, filepath.Join(root, "learning"))
	writeSpaces(t, root, "version: 1\nspaces:\n  - name: hub\n    kind: docs\n    path: learning\n    added_at: 2026-01-01T00:00:00Z\n")
	err = deleteExternal("learning", false, false)
	if err == nil || !strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("guard ordering: %v", err)
	}
	if strings.Contains(err.Error(), "live inside") {
		t.Fatalf("the containment message must not win: %v", err)
	}
	if _, statErr := os.Stat(plain); statErr != nil {
		t.Fatal("the registered space directory must survive")
	}
}

func TestSpaceLifecycle_DeleteAllowsOutsideFeatureScopedEntry(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	outside := mustMkdir(t, filepath.Join(t.TempDir(), "notes"))
	if _, err := runSpace(t, "add", "notes", outside, "--kind", "docs", "--feature", "acme"); err != nil {
		t.Fatal(err)
	}

	root := internal.TwsRoot()
	before, err := os.ReadFile(spacesFileIn(root))
	if err != nil {
		t.Fatal(err)
	}

	if err := deleteExternal("acme", false, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatal("the linked directory must survive")
	}
	after, err := os.ReadFile(spacesFileIn(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("delete must never rewrite spaces.yaml")
	}

	out, err := runSpace(t, "list", "--json", "--all")
	if err != nil {
		t.Fatal(err)
	}
	views := decodeSpaceViews(t, out)
	if len(views) != 1 || views[0]["scope_status"] != "feature-missing" || views[0]["status"] != "ok" {
		t.Fatalf("unexpected views: %v", views)
	}
}

func TestSpaceLifecycle_DeleteWithoutRegistryCreatesNothing(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := deleteExternal("acme", false, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, path := range []string{spacesFileIn(root), spacesLockIn(root)} {
		if _, err := os.Lstat(path); err == nil {
			t.Fatalf("%s must not be created", path)
		}
	}
}

func TestSpaceLifecycle_RenameRewritesRelativeEntries(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	featurePath := internal.FeaturePath("acme")
	patching := mustMkdir(t, filepath.Join(featurePath, "patching"))
	docs := mustMkdir(t, filepath.Join(featurePath, "docs"))
	if _, err := runSpace(t, "add", "patching", patching, "--kind", "patching", "--feature", "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "docs", docs, "--kind", "docs"); err != nil {
		t.Fatal(err)
	}
	outsideAbs := mustMkdir(t, filepath.Join(t.TempDir(), "elsewhere"))
	if _, err := runSpace(t, "add", "elsewhere", outsideAbs, "--kind", "research"); err != nil {
		t.Fatal(err)
	}

	if err := runRenameFeature(t, "acme", "acme2"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	out, err := runSpace(t, "list", "--json", "--all")
	if err != nil {
		t.Fatal(err)
	}
	views := decodeSpaceViews(t, out)
	byName := map[string]map[string]any{}
	for _, v := range views {
		byName[v["name"].(string)] = v
	}
	if got := byName["patching"]["path"]; got != filepath.Join("acme2", "patching") {
		t.Fatalf("feature-scoped path = %v", got)
	}
	if got := byName["patching"]["feature"]; got != "acme2" {
		t.Fatalf("feature scope = %v", got)
	}
	if _, ok := byName["patching"]["updated_at"]; !ok {
		t.Fatal("updated_at must be set on rewritten entries")
	}
	if got := byName["docs"]["path"]; got != filepath.Join("acme2", "docs") {
		t.Fatalf("workspace-wide relative path = %v", got)
	}
	if got := byName["elsewhere"]["path"]; got != outsideAbs {
		t.Fatalf("absolute entry outside the feature must be untouched: %v", got)
	}
	if _, ok := byName["elsewhere"]["updated_at"]; ok {
		t.Fatal("untouched entries must not gain updated_at")
	}
}

func TestSpaceLifecycle_RenameCheckoutNewLayoutRewrite(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	ws := requireWorkspaceForTest(t, repo)

	if err := addCheckout(ws, "acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	patching := mustMkdir(t, filepath.Join(ws.FeaturePath("acme"), "patching"))
	if _, err := runSpace(t, "add", "patching", patching, "--kind", "patching", "--feature", "acme"); err != nil {
		t.Fatal(err)
	}

	if err := runRenameFeature(t, "acme", "acme2"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	out, err := runSpace(t, "list", "--json", "--all")
	if err != nil {
		t.Fatal(err)
	}
	views := decodeSpaceViews(t, out)
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %v", views)
	}
	if views[0]["path"] != filepath.Join("features", "acme2", "patching") {
		t.Fatalf("checkout new-layout path = %v", views[0]["path"])
	}
	if views[0]["feature"] != "acme2" {
		t.Fatalf("feature = %v", views[0]["feature"])
	}
}

func TestSpaceLifecycle_RenameRefusesAbsoluteEntryInside(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	featurePath := internal.FeaturePath("acme")
	tickets := mustMkdir(t, filepath.Join(featurePath, "tickets"))
	writeSpaces(t, root, "version: 1\nspaces:\n  - name: tickets\n    kind: tickets\n    path: "+tickets+
		"\n    added_at: 2026-01-01T00:00:00Z\n")

	err := runRenameFeature(t, "acme", "acme2")
	if err == nil {
		t.Fatal("expected refusal")
	}
	for _, want := range []string{
		"1 registered space is pinned inside",
		"tws space remove tickets --workspace",
		"workspace-relative",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q missing %q", err, want)
		}
	}
	if _, statErr := os.Stat(featurePath); statErr != nil {
		t.Fatal("nothing may be renamed")
	}
}

func TestSpaceLifecycle_RenameNoMatchingEntryWritesNothing(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	notes := mustMkdir(t, filepath.Join(root, "notes"))
	if _, err := runSpace(t, "add", "notes", notes, "--kind", "docs"); err != nil {
		t.Fatal(err)
	}

	path := spacesFileIn(root)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := runRenameFeature(t, "acme", "acme2"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("spaces.yaml must be byte-identical")
	}
	if !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("spaces.yaml mtime must be unchanged")
	}
}

func TestSpaceLifecycle_RenameWithoutRegistryCreatesNothing(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := runRenameFeature(t, "acme", "acme2"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "acme2")); statErr != nil {
		t.Fatal("rename must still work")
	}
	for _, p := range []string{spacesFileIn(root), spacesLockIn(root)} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s must not be created", p)
		}
	}
}

func TestSpaceLifecycle_RenameRollbackOnSaveFailure(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	featurePath := internal.FeaturePath("acme")
	patching := mustMkdir(t, filepath.Join(featurePath, "patching"))
	if _, err := runSpace(t, "add", "patching", patching, "--kind", "patching", "--feature", "acme"); err != nil {
		t.Fatal(err)
	}

	path := spacesFileIn(root)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected save failure")
	internal.SpacesSaveHook = func(string) error { return injected }
	t.Cleanup(func() { internal.SpacesSaveHook = nil })

	err = runRenameFeature(t, "acme", "acme2")
	if err == nil || !strings.Contains(err.Error(), "injected save failure") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(featurePath); statErr != nil {
		t.Fatal("the feature directory must be rolled back to its old path")
	}
	if _, statErr := os.Stat(filepath.Join(root, "acme2")); statErr == nil {
		t.Fatal("the new path must not remain")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("spaces.yaml must be byte-identical after rollback")
	}
}

func TestSpaceLifecycle_DeleteHoldsLockAcrossRemoval(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	notes := mustMkdir(t, filepath.Join(root, "notes"))
	if _, err := runSpace(t, "add", "notes", notes, "--kind", "docs"); err != nil {
		t.Fatal(err)
	}

	featurePath := internal.FeaturePath("acme")
	nested := mustMkdir(t, filepath.Join(featurePath, "scratch"))

	tx, err := internal.BeginSpacesFeatureDelete(root, "acme", featurePath)
	if err != nil {
		t.Fatal(err)
	}

	// A concurrent add blocks on the lock until the delete releases it.
	done := make(chan error, 1)
	go func() {
		done <- runSpaceIsolated("add", "scratch", nested, "--kind", "docs")
	}()

	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("concurrent add must not complete while the delete lock is held: %v", err)
	default:
	}

	if err := os.RemoveAll(featurePath); err != nil {
		t.Fatal(err)
	}
	if err := tx.Release(); err != nil {
		t.Fatal(err)
	}

	if err := <-done; err == nil {
		t.Fatal("the concurrent add must fail its own existence check")
	}

	out, err := runSpace(t, "list", "--json", "--all")
	if err != nil {
		t.Fatal(err)
	}
	views := decodeSpaceViews(t, out)
	for _, v := range views {
		if v["name"] == "scratch" {
			t.Fatal("a registered entry must never point into a concurrently removed directory")
		}
	}
}

// runSpaceIsolated runs a space command without a *testing.T, for goroutines.
func runSpaceIsolated(args ...string) error {
	cmd := spaceCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return cmd.Execute()
}
