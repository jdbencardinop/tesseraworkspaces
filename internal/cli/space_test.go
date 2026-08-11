package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

// ---------- shared space test helpers ----------

// runSpace executes `tws space ...` with a captured writer.
func runSpace(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := spaceCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// withCheckoutEnv isolates HOME/TWS_ROOT and chdirs into a checkout repo.
// TWS_ROOT deliberately points at an unrelated directory so the checkout
// anchor rule (env ignored) is exercised.
func withCheckoutEnv(t *testing.T, repo string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	unrelated := filepath.Join(t.TempDir(), "unrelated")
	if err := os.MkdirAll(unrelated, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_ROOT", unrelated)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	return unrelated
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func spacesFileIn(root string) string { return filepath.Join(root, "spaces.yaml") }

func spacesLockIn(root string) string { return filepath.Join(root, ".spaces.lock") }

func snapshotTree(t *testing.T, dir string) string {
	t.Helper()
	var paths []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(paths, "\n")
}

func decodeSpaceViews(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var views []map[string]any
	if err := json.Unmarshal([]byte(raw), &views); err != nil {
		t.Fatalf("decoding %q: %v", raw, err)
	}
	return views
}

// ---------- external mode ----------

func TestSpaceAddListShowRemove_External(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := os.Getenv("TWS_ROOT")
	target := mustMkdir(t, filepath.Join(root, "learning"))

	out, err := runSpace(t, "add", "learning", target, "--kind", "learning", "--description", "notes")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "registered: learning (learning) -> ") {
		t.Fatalf("unexpected add output: %q", out)
	}

	info, err := os.Stat(spacesFileIn(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("spaces.yaml mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(spacesFileIn(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "version: 1") {
		t.Fatalf("missing version: %s", data)
	}
	if !strings.Contains(string(data), "path: learning") {
		t.Fatalf("expected workspace-relative storage: %s", data)
	}

	// Idempotent repeat.
	out, err = runSpace(t, "add", "learning", target, "--kind", "learning", "--description", "notes")
	if err != nil {
		t.Fatalf("repeat add: %v", err)
	}
	if out != "already registered: learning\n" {
		t.Fatalf("repeat add output = %q", out)
	}

	// Conflicting repeat leaves the file byte-identical.
	before, err := os.ReadFile(spacesFileIn(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "learning", target, "--kind", "docs"); err == nil {
		t.Fatal("expected conflicting add to fail")
	}
	after, err := os.ReadFile(spacesFileIn(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("conflicting add mutated the file")
	}

	out, err = runSpace(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Workspace: ") || !strings.Contains(out, "learning") {
		t.Fatalf("list output = %q", out)
	}

	out, err = runSpace(t, "show", "learning")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{"Name:        learning", "Kind:        learning", "Scope:       workspace", "Status:      ok", "Description: notes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Updated:") {
		t.Fatalf("Updated must be omitted when unset:\n%s", out)
	}

	out, err = runSpace(t, "remove", "learning")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out != "removed space: learning\n" {
		t.Fatalf("remove output = %q", out)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("remove deleted the target directory")
	}
}

func TestSpaceListJSONContract(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := os.Getenv("TWS_ROOT")

	out, err := runSpace(t, "list", "--json")
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if out != "[]\n" {
		t.Fatalf("empty JSON = %q, want %q", out, "[]\n")
	}

	target := mustMkdir(t, filepath.Join(root, "learning"))
	if _, err := runSpace(t, "add", "learning", target, "--kind", "learning"); err != nil {
		t.Fatal(err)
	}

	out, err = runSpace(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatal("JSON output must end with a newline")
	}
	if !strings.Contains(out, "\n  {\n") {
		t.Fatalf("expected two-space indentation:\n%s", out)
	}
	views := decodeSpaceViews(t, out)
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	v := views[0]
	for _, key := range []string{"name", "kind", "path", "resolved_path", "scope", "scope_status", "status", "added_at"} {
		if _, ok := v[key]; !ok {
			t.Fatalf("missing key %q in %v", key, v)
		}
	}
	for _, key := range []string{"description", "feature", "updated_at"} {
		if _, ok := v[key]; ok {
			t.Fatalf("key %q must be omitted: %v", key, v)
		}
	}
	if v["status"] != "ok" || v["scope"] != "workspace" || v["scope_status"] != "ok" {
		t.Fatalf("unexpected computed fields: %v", v)
	}

	// Removing the target makes status missing without mutating the file.
	before, err := os.ReadFile(spacesFileIn(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	out, err = runSpace(t, "list", "--json")
	if err != nil {
		t.Fatalf("list after target removal: %v", err)
	}
	views = decodeSpaceViews(t, out)
	if views[0]["status"] != "missing" {
		t.Fatalf("status = %v, want missing", views[0]["status"])
	}
	after, err := os.ReadFile(spacesFileIn(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a missing target must never mutate the file")
	}

	// show --json emits one object, not an array.
	out, err = runSpace(t, "show", "learning", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var single map[string]any
	if err := json.Unmarshal([]byte(out), &single); err != nil {
		t.Fatalf("show --json: %v (%q)", err, out)
	}
	if single["name"] != "learning" {
		t.Fatalf("unexpected show payload: %v", single)
	}
}

func TestSpaceAbsentRegistryCreatesNothing(t *testing.T) {
	for _, mode := range []string{"external", "checkout"} {
		t.Run(mode, func(t *testing.T) {
			var root, parent string
			if mode == "external" {
				repo := setupGitRepo(t, "master")
				withWorkspaceEnv(t, repo)
				root = os.Getenv("TWS_ROOT")
				parent = filepath.Dir(root)
			} else {
				repo := setupGitRepoCheckout(t)
				withCheckoutEnv(t, repo)
				root = filepath.Join(repo, ".tws")
				parent = repo
			}

			// The spaces root itself may not exist yet in external mode.
			before := snapshotTree(t, parent)

			out, err := runSpace(t, "list", "--json")
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if out != "[]\n" {
				t.Fatalf("list --json = %q", out)
			}

			if _, err := runSpace(t, "show", "learning"); err == nil {
				t.Fatal("show on an empty registry must exit nonzero")
			}

			_, err = runSpace(t, "remove", "learning")
			if err == nil || err.Error() != `no space named "learning"` {
				t.Fatalf(`remove err = %v, want exactly 'no space named "learning"'`, err)
			}

			after := snapshotTree(t, parent)
			if before != after {
				t.Fatalf("read-only/absent paths created files:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			for _, path := range []string{spacesFileIn(root), spacesLockIn(root), filepath.Join(root, ".tws-workspace")} {
				if _, err := os.Lstat(path); err == nil {
					t.Fatalf("%s must not be created", path)
				}
			}
		})
	}
}

func TestSpaceAddNonGitAndAbsoluteTargets(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	plain := mustMkdir(t, filepath.Join(t.TempDir(), "plain-directory"))
	if _, err := runSpace(t, "add", "tickets", plain, "--kind", "tickets"); err != nil {
		t.Fatalf("non-Git directory must be accepted: %v", err)
	}

	out, err := runSpace(t, "show", "tickets", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if view["path"] != view["resolved_path"] {
		t.Fatalf("absolute entry must have path == resolved_path: %v", view)
	}
}

func TestSpaceAddRejectsBadInputs(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := os.Getenv("TWS_ROOT")
	target := mustMkdir(t, filepath.Join(root, "learning"))
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing-target", []string{"add", "bad", filepath.Join(root, "nope"), "--kind", "docs"}, "does not exist"},
		{"file-target", []string{"add", "bad", file, "--kind", "docs"}, "is not a directory"},
		{"workspace-root", []string{"add", "x", root, "--kind", "docs"}, "refusing to register the workspace root itself"},
		{"upper-kind", []string{"add", "x", target, "--kind", "Learning"}, "malformed"},
		{"empty-kind", []string{"add", "x", target, "--kind", ""}, "kind cannot be empty"},
		{"long-kind", []string{"add", "x", target, "--kind", strings.Repeat("k", 33)}, "too long"},
		{"newline-description", []string{"add", "x", target, "--kind", "docs", "--description", "a\nb"}, "control characters"},
		{"long-description", []string{"add", "x", target, "--kind", "docs", "--description", strings.Repeat("d", 201)}, "too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSpace(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if _, statErr := os.Stat(spacesFileIn(root)); statErr == nil {
				t.Fatal("rejected add must not create spaces.yaml")
			}
		})
	}

	// A conforming but unconventional kind succeeds.
	if _, err := runSpace(t, "add", "ledger", target, "--kind", "ledger"); err != nil {
		t.Fatalf("conforming kind must be accepted: %v", err)
	}
}

func TestSpaceAddRequiresKindFlag(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := os.Getenv("TWS_ROOT")
	target := mustMkdir(t, filepath.Join(root, "learning"))

	if _, err := runSpace(t, "add", "learning", target); err == nil {
		t.Fatal("--kind must be required")
	}
}

func TestSpaceHelpListsExactlyFourSubcommands(t *testing.T) {
	cmd := spaceCmd()
	var names []string
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	want := map[string]bool{"add": true, "list": true, "show": true, "remove": true}
	if len(names) != len(want) {
		t.Fatalf("space subcommands = %v", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("unexpected subcommand %q", n)
		}
	}
}

func TestSpaceCmdRegisteredOnRoot(t *testing.T) {
	root := &cobra.Command{Use: "tws"}
	root.AddCommand(spaceCmd())
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "space" {
			found = true
		}
	}
	if !found {
		t.Fatal("space command not registered")
	}
	// The production wiring must include it too.
	if !strings.Contains(readSourceFile(t, "root.go"), "spaceCmd()") {
		t.Fatal("spaceCmd() is not wired into rootCmd.AddCommand")
	}
}

func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ---------- checkout mode ----------

func TestSpaceCheckoutModeIgnoresTwsRoot(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	unrelated := withCheckoutEnv(t, repo)

	mustMkdir(t, filepath.Join(repo, "notes"))
	if _, err := runSpace(t, "add", "docs", "./notes", "--kind", "docs"); err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := os.Stat(filepath.Join(repo, ".tws", "spaces.yaml")); err != nil {
		t.Fatalf("checkout mode must write <repo>/.tws/spaces.yaml: %v", err)
	}
	if _, err := os.Stat(spacesFileIn(unrelated)); err == nil {
		t.Fatal("TWS_ROOT must be untouched in checkout mode")
	}
	// Checkout mode must not create an external workspace marker.
	if _, err := os.Stat(filepath.Join(repo, ".tws", ".tws-workspace")); err == nil {
		t.Fatal("checkout mode must not create the external marker")
	}
}

func TestSpaceCheckoutFeatureScope(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)

	ws := requireWorkspaceForTest(t, repo)
	if err := addCheckout(ws, "acme", nil, "", "", false, false, false); err != nil {
		t.Fatalf("addCheckout: %v", err)
	}
	patching := mustMkdir(t, filepath.Join(ws.FeaturePath("acme"), "patching"))

	if _, err := runSpace(t, "add", "patching", patching, "--kind", "patching", "--feature", "acme"); err != nil {
		t.Fatalf("feature-scoped add: %v", err)
	}
	out, err := runSpace(t, "list", "--json", "--feature", "acme")
	if err != nil {
		t.Fatal(err)
	}
	views := decodeSpaceViews(t, out)
	if len(views) != 1 || views[0]["feature"] != "acme" || views[0]["scope"] != "feature" {
		t.Fatalf("unexpected views: %v", views)
	}
	if views[0]["path"] != filepath.Join("features", "acme", "patching") {
		t.Fatalf("expected workspace-relative storage under features/, got %v", views[0]["path"])
	}

	// The feature must still be listed as a feature.
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 || features[0] != "acme" {
		t.Fatalf("expected [acme], got %v", features)
	}

	if _, err := runSpace(t, "add", "x", ws.FeaturePath("acme"), "--kind", "docs"); err == nil ||
		!strings.Contains(err.Error(), "it is the feature directory") {
		t.Fatalf("registering a feature directory must be refused, got %v", err)
	}

	if _, err := runSpace(t, "add", "n", patching, "--kind", "docs", "--feature", "nosuch"); err == nil ||
		!strings.Contains(err.Error(), "not found in this workspace") {
		t.Fatalf("unknown --feature must fail, got %v", err)
	}
}

// ---------- TWS_ROOT divergence ----------

func TestSpaceTwsRootDivergence(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	envRoot := os.Getenv("TWS_ROOT")

	// A sibling <repo>.tws root that also holds a feature.
	siblingRoot := repo + ".tws"
	mustMkdir(t, filepath.Join(siblingRoot, "alpha"))
	if err := os.WriteFile(filepath.Join(siblingRoot, "alpha", "FEATURE.md"), []byte("# alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}

	learning := mustMkdir(t, filepath.Join(envRoot, "learning", "notes"))
	if _, err := runSpace(t, "add", "learning", filepath.Dir(learning), "--kind", "learning"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := os.Stat(spacesFileIn(envRoot)); err != nil {
		t.Fatalf("expected $TWS_ROOT/spaces.yaml: %v", err)
	}
	if _, err := os.Stat(spacesFileIn(siblingRoot)); err == nil {
		t.Fatal("must not write to <repo>.tws/spaces.yaml")
	}

	fromRepo, err := runSpace(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{envRoot, filepath.Join(envRoot, "learning"), learning} {
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		out, err := runSpace(t, "list", "--json")
		if err != nil {
			t.Fatalf("list from %s: %v", dir, err)
		}
		if out != fromRepo {
			t.Fatalf("list from %s differs:\n%s\nvs\n%s", dir, out, fromRepo)
		}
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	// `tws list` scans <repo>.tws and is unaffected.
	siblingWs := internal.Workspace{MetadataRoot: siblingRoot, Mode: internal.ModeExternal}
	features, err := siblingWs.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 || features[0] != "alpha" {
		t.Fatalf("sibling root listing = %v", features)
	}

	// Every external mutation rooted at $TWS_ROOT is guarded.
	if err := addExternal("learning", nil, "", "", false, false, false); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("tws add: %v", err)
	}
	if err := createWorktree("learning", "wt", "master", repo, false); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("tws new: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "learning", "worktrees")); err == nil {
		t.Fatal("tws new created something under the registered space")
	}
	if err := deleteExternal("learning", false, false); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("tws delete: %v", err)
	}
	if _, err := os.Stat(learning); err != nil {
		t.Fatal("tws delete removed the registered space directory")
	}

	// remove deletes only the registry line.
	if _, err := runSpace(t, "remove", "learning"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "learning")); err != nil {
		t.Fatal("remove deleted the target")
	}
}

func TestSpacesAnchorHasNoLegacyField(t *testing.T) {
	src := readSourceFileIn(t, filepath.Join("..", "spaces.go"))
	// Criterion 6: `rg -n 'Legacy\b' internal/spaces.go` returns nothing.
	// LegacyPath (a field of the shared ErrAmbiguousFeature) is not a match.
	if regexp.MustCompile(`Legacy\b`).MatchString(src) {
		t.Fatal("internal/spaces.go must contain no standalone Legacy symbol")
	}
	if strings.Contains(src, "SpacesAnchor{Root, Canon string; Mode WorkspaceMode; Legacy") {
		t.Fatal("SpacesAnchor must not carry a Legacy field")
	}
}

func readSourceFileIn(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ---------- scope selectors ----------

func TestSpaceShowRemoveScopeSelectors(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withUnifiedWorkspaceEnv(t, repo)
	root := internal.TwsRoot()

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	workspaceTarget := mustMkdir(t, filepath.Join(root, "notes"))
	featureTarget := mustMkdir(t, filepath.Join(internal.FeaturePath("acme"), "notes"))

	if _, err := runSpace(t, "add", "notes", workspaceTarget, "--kind", "docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "notes", featureTarget, "--kind", "docs", "--feature", "acme"); err != nil {
		t.Fatal(err)
	}

	// A bare name is ambiguous and names both disambiguating flags.
	_, err := runSpace(t, "show", "notes")
	if err == nil || !strings.Contains(err.Error(), "is ambiguous") {
		t.Fatalf("show err = %v", err)
	}
	if !strings.Contains(err.Error(), "disambiguate with --feature <name> or --workspace") {
		t.Fatalf("ambiguity guidance = %v", err)
	}
	if _, err := runSpace(t, "remove", "notes"); err == nil ||
		!strings.Contains(err.Error(), "disambiguate with --feature <name> or --workspace") {
		t.Fatalf("remove err = %v", err)
	}

	// --workspace reaches the entry that --feature cannot.
	out, err := runSpace(t, "show", "notes", "--workspace", "--json")
	if err != nil {
		t.Fatalf("show --workspace: %v", err)
	}
	var view map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &view); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if view["scope"] != "workspace" || view["resolved_path"] != workspaceTarget {
		t.Fatalf("show --workspace payload = %v", view)
	}

	out, err = runSpace(t, "show", "notes", "--feature", "acme", "--json")
	if err != nil {
		t.Fatalf("show --feature: %v", err)
	}
	if jsonErr := json.Unmarshal([]byte(out), &view); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if view["scope"] != "feature" || view["resolved_path"] != featureTarget {
		t.Fatalf("show --feature payload = %v", view)
	}

	// The two selectors are mutually exclusive on both subcommands.
	for _, args := range [][]string{
		{"show", "notes", "--workspace", "--feature", "acme"},
		{"remove", "notes", "--workspace", "--feature", "acme"},
	} {
		if _, err := runSpace(t, args...); err == nil ||
			!strings.Contains(err.Error(), "if any flags in the group [feature workspace] are set none of the others can be") {
			t.Fatalf("%v err = %v", args, err)
		}
	}

	// Scoped not-found names the scope.
	if _, err := runSpace(t, "show", "nosuch", "--workspace"); err == nil ||
		err.Error() != `no space named "nosuch" in the workspace scope` {
		t.Fatalf("workspace not-found = %v", err)
	}

	// remove --workspace drops only the workspace-wide entry.
	if _, err := runSpace(t, "remove", "notes", "--workspace"); err != nil {
		t.Fatalf("remove --workspace: %v", err)
	}
	out, err = runSpace(t, "list", "--json", "--all")
	if err != nil {
		t.Fatal(err)
	}
	views := decodeSpaceViews(t, out)
	if len(views) != 1 || views[0]["feature"] != "acme" {
		t.Fatalf("remove --workspace removed the wrong entry: %s", out)
	}
	if _, statErr := os.Stat(workspaceTarget); statErr != nil {
		t.Fatal("remove must never delete the target directory")
	}

	// The remaining entry is now reachable by bare name again.
	if _, err := runSpace(t, "show", "notes"); err != nil {
		t.Fatalf("bare name must resolve a unique match: %v", err)
	}
}

// ---------- list header, scope annotation, and empty states ----------

func TestSpaceListHeaderScopeAndEmptyStates(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withUnifiedWorkspaceEnv(t, repo)
	root := internal.TwsRoot()

	// Truly empty registry: the header still prints.
	out, err := runSpace(t, "list")
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if !strings.HasPrefix(out, "Workspace: "+root+" (mode: external, scope: all)\n\n") {
		t.Fatalf("empty listing must still print the header: %q", out)
	}
	if !strings.Contains(out, "No spaces registered. Use 'tws space add <name> <path> --kind <kind>' to add one.") {
		t.Fatalf("empty-registry message = %q", out)
	}

	// JSON keeps the bare array with no header.
	out, err = runSpace(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if out != "[]\n" {
		t.Fatalf("empty JSON = %q", out)
	}

	if err := addExternal("acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	learning := mustMkdir(t, filepath.Join(root, "learning"))
	patching := mustMkdir(t, filepath.Join(internal.FeaturePath("acme"), "patching"))
	if _, err := runSpace(t, "add", "learning", learning, "--kind", "learning"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "patching", patching, "--kind", "patching", "--feature", "acme"); err != nil {
		t.Fatal(err)
	}

	// A filter that matches nothing is distinguished from an empty registry.
	out, err = runSpace(t, "list", "--kind", "research")
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if !strings.Contains(out, "No spaces match the active filters (2 registered). Use 'tws space list --all' to see every entry.") {
		t.Fatalf("filtered empty message = %q", out)
	}
	if strings.Contains(out, "No spaces registered") {
		t.Fatalf("a non-empty registry must not claim to be empty: %q", out)
	}
	if !strings.HasPrefix(out, "Workspace: ") {
		t.Fatalf("filtered empty state must still print the header: %q", out)
	}

	// JSON stays a bare array for the same filter.
	out, err = runSpace(t, "list", "--json", "--kind", "research")
	if err != nil {
		t.Fatal(err)
	}
	if out != "[]\n" {
		t.Fatalf("filtered empty JSON = %q", out)
	}

	// Explicit feature scope is annotated in the header.
	out, err = runSpace(t, "list", "--feature", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Workspace: "+root+" (mode: external, scope: feature acme)\n\n") {
		t.Fatalf("--feature header = %q", out)
	}

	// --all is always the complete view.
	out, err = runSpace(t, "list", "--all")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Workspace: "+root+" (mode: external, scope: all)\n\n") {
		t.Fatalf("--all header = %q", out)
	}
	for _, want := range []string{"learning", "patching"} {
		if !strings.Contains(out, want) {
			t.Fatalf("--all listing missing %q:\n%s", want, out)
		}
	}

	// Auto-detected scope is annotated too.
	if err := os.Chdir(internal.FeaturePath("acme")); err != nil {
		t.Fatal(err)
	}
	out, err = runSpace(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "scope: feature acme)") {
		t.Fatalf("auto-detected header = %q", out)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceListLongHelpDocumentsDefaultScope(t *testing.T) {
	long := spaceListCmd().Long
	for _, want := range []string{"--all", "workspace-wide", "auto-detected", "--kind", "--json"} {
		if !strings.Contains(long, want) {
			t.Fatalf("space list --help must mention %q:\n%s", want, long)
		}
	}
	for _, cmd := range []*cobra.Command{spaceShowCmd(), spaceRemoveCmd()} {
		if !strings.Contains(cmd.Long, "--workspace") {
			t.Fatalf("%s --help must document --workspace:\n%s", cmd.Name(), cmd.Long)
		}
		if cmd.Flags().Lookup("workspace") == nil {
			t.Fatalf("%s must expose --workspace", cmd.Name())
		}
	}
}
