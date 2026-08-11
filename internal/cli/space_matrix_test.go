package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// TestSpaceMatrix_InvocationLocations covers the nine-row auto-detection
// matrix: `tws space <sub>` must work with no positional workspace argument
// from every supported location, in both modes.
func TestSpaceMatrix_InvocationLocations(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := os.Getenv("TWS_ROOT")

	if err := createWorktree("feature", "root", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	if err := internal.EnsureExternalWorkspaceMarker(root); err != nil {
		t.Fatal(err)
	}

	featurePath := internal.FeaturePath("feature")
	worktree := internal.WorktreePath("feature", "root")
	nestedWorktree := mustMkdir(t, filepath.Join(worktree, "a", "b"))
	nestedFeature := mustMkdir(t, filepath.Join(featurePath, "docs", "nested"))
	learningNested := mustMkdir(t, filepath.Join(root, "learning", "notes"))
	featureNotes := mustMkdir(t, filepath.Join(featurePath, "notes"))

	if _, err := runSpace(t, "add", "learning", filepath.Join(root, "learning"), "--kind", "learning"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "notes", featureNotes, "--kind", "docs", "--feature", "feature"); err != nil {
		t.Fatal(err)
	}
	// A second feature with its own scoped entry, so scoped rows differ from
	// unscoped ones.
	if err := addExternal("other", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	otherNotes := mustMkdir(t, filepath.Join(internal.FeaturePath("other"), "notes"))
	if _, err := runSpace(t, "add", "notes", otherNotes, "--kind", "docs", "--feature", "other"); err != nil {
		t.Fatal(err)
	}

	rows := []struct {
		name        string
		cwd         string
		wantFeature string // "" means no feature detected: show everything
		wantEntries int
	}{
		{"1-external-repo-root", repo, "", 3},
		{"2-worktree-root", worktree, "feature", 2},
		{"3-nested-in-worktree", nestedWorktree, "feature", 2},
		{"4-external-workspace-root", root, "", 3},
		{"5-external-feature-dir", featurePath, "feature", 2},
		{"6-nested-feature-dir", nestedFeature, "feature", 2},
		{"9-inside-registered-space", learningNested, "", 3},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if err := os.Chdir(row.cwd); err != nil {
				t.Fatal(err)
			}
			out, err := runSpace(t, "list", "--json")
			if err != nil {
				t.Fatalf("list from %s: %v", row.cwd, err)
			}
			views := decodeSpaceViews(t, out)
			if len(views) != row.wantEntries {
				t.Fatalf("from %s got %d entries: %s", row.cwd, len(views), out)
			}
			for _, v := range views {
				feature, _ := v["feature"].(string)
				if row.wantFeature != "" && feature != "" && feature != row.wantFeature {
					t.Fatalf("from %s leaked feature %q: %s", row.cwd, feature, out)
				}
			}

			// The human header annotates the scope that produced those rows.
			human, err := runSpace(t, "list")
			if err != nil {
				t.Fatalf("human list from %s: %v", row.cwd, err)
			}
			wantScope := "all"
			if row.wantFeature != "" {
				wantScope = "feature " + row.wantFeature
			}
			if !strings.Contains(human, "(mode: external, scope: "+wantScope+")") {
				t.Fatalf("from %s header = %q, want scope %q", row.cwd, human, wantScope)
			}

			// --all is always the complete view from every location.
			complete, err := runSpace(t, "list", "--json", "--all")
			if err != nil {
				t.Fatalf("list --all from %s: %v", row.cwd, err)
			}
			if got := len(decodeSpaceViews(t, complete)); got != 3 {
				t.Fatalf("--all from %s got %d entries: %s", row.cwd, got, complete)
			}
			if !strings.Contains(mustRunSpace(t, "list", "--all"), "(mode: external, scope: all)") {
				t.Fatalf("--all header must always report scope all (from %s)", row.cwd)
			}

			if _, err := runSpace(t, "show", "learning"); err != nil {
				t.Fatalf("show from %s: %v", row.cwd, err)
			}
		})
	}

	// Row 9: a registered space must never be auto-detected as a feature and
	// must never be offered by feature completion.
	if err := os.Chdir(learningNested); err != nil {
		t.Fatal(err)
	}
	if names := internal.ListFeatures(); contains(names, "learning") {
		t.Fatalf("completion offered the registered space: %v", names)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
}

// TestSpaceMatrix_SiblingRepoAnchorsOnParentWorkspace pins row 9 variant b:
// inside a sibling space that is its own Git repository, the enclosing
// .tws-workspace marker wins and the anchor stays the parent workspace.
func TestSpaceMatrix_SiblingRepoAnchorsOnParentWorkspace(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := os.Getenv("TWS_ROOT")

	if err := createWorktree("feature", "root", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	if err := internal.EnsureExternalWorkspaceMarker(root); err != nil {
		t.Fatal(err)
	}

	tickets := mustMkdir(t, filepath.Join(root, "tickets"))
	initSiblingRepo(t, tickets)
	nested := mustMkdir(t, filepath.Join(tickets, "store"))

	if _, err := runSpace(t, "add", "tickets", tickets, "--kind", "tickets"); err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	fromRoot, err := runSpace(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{tickets, nested} {
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		out, err := runSpace(t, "list", "--json")
		if err != nil {
			t.Fatalf("list from %s: %v", dir, err)
		}
		if out != fromRoot {
			t.Fatalf("list from %s differs:\n%s\nvs\n%s", dir, out, fromRoot)
		}
	}

	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(tickets, "spaces.yaml"),
		filepath.Join(tickets, ".tws"),
		tickets + ".tws",
	} {
		if _, err := os.Lstat(path); err == nil {
			t.Fatalf("%s must not be created for the sibling repo", path)
		}
	}
}

// TestSpaceMatrix_CheckoutRows covers rows 7 and 8.
func TestSpaceMatrix_CheckoutRows(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	ws := requireWorkspaceForTest(t, repo)

	if err := addCheckout(ws, "acme", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	featurePath := ws.FeaturePath("acme")
	patching := mustMkdir(t, filepath.Join(featurePath, "patching"))
	notes := mustMkdir(t, filepath.Join(repo, "notes"))

	if _, err := runSpace(t, "add", "notes", notes, "--kind", "docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSpace(t, "add", "patching", patching, "--kind", "patching", "--feature", "acme"); err != nil {
		t.Fatal(err)
	}

	rows := []struct {
		dir       string
		wantScope string
	}{
		{repo, "all"},
		{featurePath, "feature acme"},
	}
	for _, row := range rows {
		if err := os.Chdir(row.dir); err != nil {
			t.Fatal(err)
		}
		out, err := runSpace(t, "list", "--json")
		if err != nil {
			t.Fatalf("list from %s: %v", row.dir, err)
		}
		views := decodeSpaceViews(t, out)
		if len(views) != 2 {
			t.Fatalf("from %s got %d entries: %s", row.dir, len(views), out)
		}

		human := mustRunSpace(t, "list")
		if !strings.Contains(human, "(mode: checkout, scope: "+row.wantScope+")") {
			t.Fatalf("from %s header = %q, want scope %q", row.dir, human, row.wantScope)
		}
		if !strings.Contains(mustRunSpace(t, "list", "--all"), "(mode: checkout, scope: all)") {
			t.Fatalf("--all header must always report scope all (from %s)", row.dir)
		}
		if got := len(decodeSpaceViews(t, mustRunSpace(t, "list", "--json", "--all"))); got != 2 {
			t.Fatalf("--all from %s got %d entries", row.dir, got)
		}
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
}

func initSiblingRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"HOME="+dir,
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("commit", "--allow-empty", "-m", "init")
	if strings.TrimSpace(dir) == "" {
		t.Fatal("empty sibling repo dir")
	}
}

// mustRunSpace runs a space command and fails the test on error.
func mustRunSpace(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runSpace(t, args...)
	if err != nil {
		t.Fatalf("space %v: %v", args, err)
	}
	return out
}

// TestSpaceMatrix_SiblingRepoWithoutTwsRootAnchorsOnParentWorkspace is the
// row-9 variant-b regression with TWS_ROOT unset: the sibling space is its own
// Git repository beneath an external .tws-workspace marker, so the anchor must
// come from the marker walk-up rather than the sibling repo's own <repo>.tws.
func TestSpaceMatrix_SiblingRepoWithoutTwsRootAnchorsOnParentWorkspace(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	if os.Getenv("TWS_ROOT") != "" {
		t.Fatal("this regression requires TWS_ROOT to be unset")
	}

	if err := createWorktree("feature", "root", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	if err := internal.EnsureExternalWorkspaceMarker(root); err != nil {
		t.Fatal(err)
	}

	tickets := mustMkdir(t, filepath.Join(root, "tickets"))
	initSiblingRepo(t, tickets)
	nested := mustMkdir(t, filepath.Join(tickets, "store"))

	if _, err := runSpace(t, "add", "tickets", tickets, "--kind", "tickets"); err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	fromRoot, err := runSpace(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{tickets, nested} {
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		out, err := runSpace(t, "list", "--json")
		if err != nil {
			t.Fatalf("list from %s: %v", dir, err)
		}
		if out != fromRoot {
			t.Fatalf("list from %s differs:\n%s\nvs\n%s", dir, out, fromRoot)
		}
		anchor, anchorErr := internal.ResolveSpacesAnchor()
		if anchorErr != nil {
			t.Fatalf("anchor from %s: %v", dir, anchorErr)
		}
		if anchor.Root != root {
			t.Fatalf("anchor from %s = %s, want the parent workspace %s", dir, anchor.Root, root)
		}
		if anchor.Mode != internal.ModeExternal {
			t.Fatalf("anchor mode from %s = %s", dir, anchor.Mode)
		}
	}

	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(tickets, "spaces.yaml"),
		filepath.Join(tickets, ".spaces.lock"),
		filepath.Join(tickets, ".tws"),
		filepath.Join(tickets, ".tws-workspace"),
		tickets + ".tws",
	} {
		if _, err := os.Lstat(path); err == nil {
			t.Fatalf("%s must not be created for the sibling repo", path)
		}
	}
}
