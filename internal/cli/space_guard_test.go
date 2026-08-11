package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// captureStdout captures os.Stdout for commands that print with bare fmt.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// writeSpaces writes a spaces.yaml fixture into root.
func writeSpaces(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spaces.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// withUnifiedWorkspaceEnv isolates the environment so that TwsRoot() and
// ws.MetadataRoot name the same external root (criterion 20).
func withUnifiedWorkspaceEnv(t *testing.T, repo string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TWS_ROOT", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.MetadataRoot != internal.TwsRoot() {
		t.Fatalf("roots diverge: %s vs %s", ws.MetadataRoot, internal.TwsRoot())
	}
	return ws.MetadataRoot
}

// snapshotTreeIgnoringLock is snapshotTree without the advisory lock file,
// which a transaction legitimately creates once a registry exists.
func snapshotTreeIgnoringLock(t *testing.T, dir string) string {
	t.Helper()
	var kept []string
	for _, line := range strings.Split(snapshotTree(t, dir), "\n") {
		if filepath.Base(line) == ".spaces.lock" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func registeredLearningFixture(name string) string {
	return fmt.Sprintf(`version: 1
spaces:
  - name: notes
    kind: learning
    path: %s
    added_at: 2026-01-01T00:00:00Z
`, name)
}

// malformedSpacesFixtures installs one untrusted spaces.yaml variant.
// Permission-based fixtures are deliberately not used.
func malformedSpacesFixtures() []struct {
	name    string
	install func(t *testing.T, root string)
} {
	return []struct {
		name    string
		install func(t *testing.T, root string)
	}{
		{"bad-yaml", func(t *testing.T, root string) { writeSpaces(t, root, "version: 1\nspaces: [\n") }},
		{"version-zero", func(t *testing.T, root string) { writeSpaces(t, root, "version: 0\nspaces: []\n") }},
		{"future-version", func(t *testing.T, root string) { writeSpaces(t, root, "version: 99\nspaces: []\n") }},
		{"unknown-field", func(t *testing.T, root string) { writeSpaces(t, root, "version: 1\nspaces: []\nextra: 1\n") }},
		{"symlinked-file", func(t *testing.T, root string) {
			t.Helper()
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			real := filepath.Join(root, "real.yaml")
			if err := os.WriteFile(real, []byte("version: 1\nspaces: []\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(root, "spaces.yaml")); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, root string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Join(root, "spaces.yaml"), 0755); err != nil {
				t.Fatal(err)
			}
		}},
	}
}

// ---------- feature listing exclusion ----------

func TestSpaceGuard_ListAndDoctorHideRegisteredSpaces(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := createWorktree("real", "root", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(root, "learning"))
	writeSpaces(t, root, registeredLearningFixture("learning"))

	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range features {
		if f == "learning" {
			t.Fatal("registered space appeared as a phantom feature")
		}
	}

	out := captureStdout(t, func() {
		cmd := listCmd()
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Errorf("list: %v", err)
		}
	})
	if strings.Contains(out, "learning") {
		t.Fatalf("tws list showed the registered space:\n%s", out)
	}

	if names := internal.ListFeatures(); contains(names, "learning") {
		t.Fatalf("completion offered the registered space: %v", names)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// ---------- logical name guards ----------

func TestSpaceGuard_ExternalCommandMatrix(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)

	if err := addExternal("foo", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	learning := mustMkdir(t, filepath.Join(root, "learning"))
	writeSpaces(t, root, registeredLearningFixture("learning"))
	before := snapshotTreeIgnoringLock(t, root)

	cases := []struct {
		name string
		run  func() error
	}{
		{"add", func() error { return addExternal("learning", nil, "", "", false, false, false) }},
		{"new", func() error { return createWorktree("learning", "wt", "master", repo, false) }},
		{"archive", func() error { return archiveExternal("learning", "wt") }},
		{"delete", func() error { return deleteExternal("learning", false, false) }},
		{"rename-feature-target", func() error {
			cmd := renameCmd()
			cmd.SetArgs([]string{"feature", "foo", "learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"rename-feature-source", func() error {
			cmd := renameCmd()
			cmd.SetArgs([]string{"feature", "learning", "foo2"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"rename-branch", func() error { return renameBranchExternal("learning", "a", "b") }},
		{"sync", func() error {
			cmd := syncCmd()
			cmd.SetArgs([]string{"learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"export", func() error {
			cmd := exportCmd()
			cmd.SetArgs([]string{"learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"open-feature-dir", func() error {
			cmd := openCmd()
			cmd.SetArgs([]string{"learning", "--feature-dir"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"open-all", func() error {
			cmd := openCmd()
			cmd.SetArgs([]string{"learning", "--all"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"import", func() error {
			return recreateExternal(internal.WorkspaceExport{Feature: "learning"}, "")
		}},
		{"stack", func() error {
			cmd := stackCmd()
			cmd.SetArgs([]string{"learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"inject", func() error {
			cmd := injectCmd()
			cmd.SetArgs([]string{"learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"push", func() error {
			cmd := pushCmd()
			cmd.SetArgs([]string{"learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"doctor", func() error {
			cmd := doctorCmd()
			cmd.SetArgs([]string{"learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"decide", func() error {
			cmd := decideCmd()
			cmd.SetArgs([]string{"learning", "note"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = tc.run() })
			if err == nil {
				t.Fatalf("expected the space-name conflict error (stdout: %s)", out)
			}
			if !strings.Contains(err.Error(), "top-level directory of registered space") {
				t.Fatalf("err = %v", err)
			}
			if got := snapshotTreeIgnoringLock(t, root); got != before {
				t.Fatalf("filesystem changed:\n%s", got)
			}
			if _, statErr := os.Stat(learning); statErr != nil {
				t.Fatal("registered space directory was touched")
			}
		})
	}
}

func TestSpaceGuard_CheckoutCommandMatrix(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")

	ws := requireWorkspaceForTest(t, repo)
	if err := addCheckout(ws, "foo", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(root, "learning"))
	writeSpaces(t, root, registeredLearningFixture("learning"))
	before := snapshotTreeIgnoringLock(t, root)

	cases := []struct {
		name string
		run  func() error
	}{
		{"add", func() error { return addCheckout(ws, "learning", nil, "", "", false, false, false) }},
		{"new", func() error { return createCheckoutBranch(ws, "learning", "b", "main", false) }},
		{"archive", func() error { return archiveCheckout(ws, "learning", "b") }},
		{"delete", func() error { return deleteCheckout(ws, "learning", false, false) }},
		{"rename-branch", func() error { return renameBranchCheckout(ws, "learning", "a", "b") }},
		{"import", func() error {
			return recreateCheckout(internal.WorkspaceExport{Feature: "learning"}, "", ws)
		}},
		{"open", func() error { return runCheckoutOpen(ws, []string{"learning"}, false, true, true, noopFlags{}) }},
		{"migrate-layout", func() error { return internal.MigrateFeatureLayout(ws, "learning") }},
		{"export", func() error {
			cmd := exportCmd()
			cmd.SetArgs([]string{"learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"stack", func() error {
			cmd := stackCmd()
			cmd.SetArgs([]string{"learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = tc.run() })
			if err == nil {
				t.Fatalf("expected the space-name conflict error (stdout: %s)", out)
			}
			if !strings.Contains(err.Error(), "top-level directory of registered space") {
				t.Fatalf("err = %v", err)
			}
			if got := snapshotTreeIgnoringLock(t, root); got != before {
				t.Fatalf("filesystem changed:\n%s", got)
			}
		})
	}
}

type noopFlags struct{}

func (noopFlags) Changed(string) bool { return false }

func TestSpaceGuard_CheckoutFeaturesLayoutSpaceBlocksAdd(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")
	ws := requireWorkspaceForTest(t, repo)

	mustMkdir(t, filepath.Join(root, "features", "scratch"))
	writeSpaces(t, root, registeredLearningFixture("features/scratch"))

	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if contains(features, "scratch") {
		t.Fatalf("features/<space> must be excluded from the new-layout branch: %v", features)
	}
	if err := addCheckout(ws, "scratch", nil, "", "", false, false, false); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("err = %v", err)
	}
}

// ---------- strict failure on untrusted metadata ----------

func TestSpaceGuard_StrictFailureExternal(t *testing.T) {
	for _, fixture := range malformedSpacesFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			repo := setupGitRepo(t, "master")
			root := withUnifiedWorkspaceEnv(t, repo)
			mustMkdir(t, root)
			if err := addExternal("alpha", nil, "", "", false, false, false); err != nil {
				t.Fatal(err)
			}
			fixture.install(t, root)
			before := snapshotTreeIgnoringLock(t, root)

			// All four space subcommands fail.
			for _, args := range [][]string{
				{"add", "x", root, "--kind", "docs"},
				{"list"},
				{"show", "x"},
				{"remove", "x"},
			} {
				if _, err := runSpace(t, args...); err == nil {
					t.Fatalf("space %v must exit nonzero", args)
				}
			}

			// Guarded lifecycle surfaces fail with the canonical message.
			runs := map[string]func() error{
				"add":     func() error { return addExternal("f", nil, "", "", false, false, false) },
				"new":     func() error { return createWorktree("f", "wt", "master", repo, false) },
				"archive": func() error { return archiveExternal("f", "wt") },
				"delete":  func() error { return deleteExternal("f", false, false) },
				"rename": func() error {
					cmd := renameCmd()
					cmd.SetArgs([]string{"feature", "alpha", "b"})
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
				"sync": func() error {
					cmd := syncCmd()
					cmd.SetArgs([]string{"f"})
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
				"export": func() error {
					cmd := exportCmd()
					cmd.SetArgs([]string{"f"})
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
				"open": func() error {
					cmd := openCmd()
					cmd.SetArgs([]string{"f", "--feature-dir"})
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
				"import": func() error { return recreateExternal(internal.WorkspaceExport{Feature: "f"}, "") },
				"list": func() error {
					cmd := listCmd()
					cmd.SetArgs(nil)
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
				"doctor": func() error {
					cmd := doctorCmd()
					cmd.SetArgs(nil)
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
				"template": func() error {
					cmd := templateCmd()
					cmd.SetArgs([]string{"sync", "--all"})
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
				"hooks": func() error {
					cmd := hooksCmd()
					cmd.SetArgs([]string{"install", "--all"})
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
			}
			for name, run := range runs {
				t.Run(name, func(t *testing.T) {
					var err error
					out := captureStdout(t, func() { err = run() })
					if err == nil {
						t.Fatalf("%s must exit nonzero (stdout: %s)", name, out)
					}
					if !strings.Contains(err.Error(), "cannot verify registered spaces in ") {
						t.Fatalf("%s err = %v", name, err)
					}
					if strings.Contains(out, "warning:") {
						t.Fatalf("%s printed a warning line: %s", name, out)
					}
					if (name == "list" || name == "doctor") && strings.Contains(out, "alpha") {
						t.Fatalf("%s printed a partial listing: %s", name, out)
					}
				})
			}

			if got := snapshotTreeIgnoringLock(t, root); got != before {
				t.Fatalf("filesystem changed:\n%s", got)
			}

			// Completion stays best-effort.
			if names := internal.ListFeatures(); names != nil {
				t.Fatalf("completion must yield no candidates, got %v", names)
			}
		})
	}
}

func TestSpaceGuard_StrictFailureCheckout(t *testing.T) {
	for _, fixture := range malformedSpacesFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			repo := setupGitRepoCheckout(t)
			withCheckoutEnv(t, repo)
			root := filepath.Join(repo, ".tws")
			ws := requireWorkspaceForTest(t, repo)
			if err := addCheckout(ws, "alpha", nil, "", "", false, false, false); err != nil {
				t.Fatal(err)
			}
			fixture.install(t, root)
			before := snapshotTreeIgnoringLock(t, root)

			if _, err := ws.ListFeaturesResolved(); err == nil {
				t.Fatal("ListFeaturesResolved must fail")
			}

			var buildErr error
			if _, buildErr = internal.BuildCheckoutList(ws); buildErr == nil {
				t.Fatal("BuildCheckoutList must return the error, not an empty slice")
			}
			if !strings.Contains(buildErr.Error(), "cannot verify registered spaces in ") {
				t.Fatalf("BuildCheckoutList err = %v", buildErr)
			}

			report, reportErr := internal.BuildCheckoutHealthReport(ws, &internal.CheckoutHealthOpts{})
			if reportErr == nil {
				t.Fatalf("health report must fail, got %+v", report)
			}

			runs := map[string]func() error{
				"list": func() error {
					cmd := listCmd()
					cmd.SetArgs(nil)
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
				"doctor": func() error {
					cmd := doctorCmd()
					cmd.SetArgs(nil)
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					return cmd.Execute()
				},
				"migrate-single": func() error { return internal.MigrateFeatureLayout(ws, "alpha") },
				"delete":         func() error { return deleteCheckout(ws, "alpha", false, false) },
			}
			for name, run := range runs {
				t.Run(name, func(t *testing.T) {
					var err error
					out := captureStdout(t, func() { err = run() })
					if err == nil {
						t.Fatalf("%s must exit nonzero (stdout: %s)", name, out)
					}
					if !strings.Contains(err.Error(), "cannot verify registered spaces in ") {
						t.Fatalf("%s err = %v", name, err)
					}
				})
			}

			// migrate-layout --all reports the canonical message and returns
			// the aggregated error.
			var migrateErr error
			out := captureStdout(t, func() {
				cmd := migrateLayoutCmd()
				cmd.SetArgs([]string{"--all"})
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				migrateErr = cmd.Execute()
			})
			if migrateErr == nil || migrateErr.Error() != "migration failed with 1 error(s)" {
				t.Fatalf("migrate --all err = %v", migrateErr)
			}
			if !strings.Contains(out, "error: cannot verify registered spaces in ") {
				t.Fatalf("migrate --all output = %q", out)
			}
			if _, err := os.Stat(filepath.Join(root, "features", "alpha")); err != nil {
				t.Fatal("alpha must stay where it was")
			}

			if got := snapshotTreeIgnoringLock(t, root); got != before {
				t.Fatalf("filesystem changed:\n%s", got)
			}
		})
	}
}

func TestSpaceGuard_CompletionsStayBestEffort(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	root := filepath.Join(repo, ".tws")
	ws := requireWorkspaceForTest(t, repo)
	mustMkdir(t, filepath.Join(root, "legacy"))
	writeSpaces(t, root, "version: 99\nspaces: []\n")

	if names := ws.LegacyFeatureNames(); names != nil {
		t.Fatalf("migrate-layout completion must yield nothing, got %v", names)
	}
	if names := internal.ListFeatures(); names != nil {
		t.Fatalf("open completion must yield nothing, got %v", names)
	}
	if names := internal.ListBranches("legacy"); names != nil {
		t.Fatalf("branch completion must yield nothing, got %v", names)
	}
	names, _ := completeSpaceNames(spaceShowCmd(), nil, "")
	if names != nil {
		t.Fatalf("space name completion must yield nothing, got %v", names)
	}
}

// ---------- template sync / hooks install carve-out ----------

func TestSpaceGuard_TemplateSyncAndHooksInstallSingleFeature(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	if err := addExternal("alpha", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(root, "learning"))
	writeSpaces(t, root, registeredLearningFixture("learning"))

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"template-sync", func() error {
			cmd := templateCmd()
			cmd.SetArgs([]string{"sync", "learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"hooks-install", func() error {
			cmd := hooksCmd()
			cmd.SetArgs([]string{"install", "learning"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = tc.run() })
			if err == nil {
				t.Fatalf("must exit nonzero (stdout: %s)", out)
			}
			if !strings.Contains(err.Error(), "top-level directory of registered space") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(out, "top-level directory of registered space") {
				t.Fatal("the guard message must not go to stdout")
			}
			if _, statErr := os.Stat(filepath.Join(root, "learning", "inject")); statErr == nil {
				t.Fatal("inject/ must not be created")
			}
		})
	}

	// Malformed metadata yields the canonical message on the same surfaces.
	writeSpaces(t, root, "version: 99\nspaces: []\n")
	for _, args := range [][]string{{"sync", "learning"}, {"install", "learning"}} {
		var err error
		if args[0] == "sync" {
			cmd := templateCmd()
			cmd.SetArgs(args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err = cmd.Execute()
		} else {
			cmd := hooksCmd()
			cmd.SetArgs(args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err = cmd.Execute()
		}
		if err == nil || !strings.Contains(err.Error(), "cannot verify registered spaces in ") {
			t.Fatalf("%v err = %v", args, err)
		}
	}
}

func TestSpaceGuard_TemplateSyncAndHooksInstallCarveOutBoundary(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	if err := addExternal("alpha", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}

	t.Run("unrelated-failure-preserved", func(t *testing.T) {
		// No spaces.yaml: an unrelated RequireFeaturePath failure keeps the
		// legacy shape — stdout line, exit 0 — because the void helper owns it.
		var err error
		out := captureStdout(t, func() {
			cmd := templateCmd()
			cmd.SetArgs([]string{"sync", "state"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err = cmd.Execute()
		})
		if err != nil {
			t.Fatalf("expected exit 0, got %v", err)
		}
		if !strings.Contains(out, "state: ") {
			t.Fatalf("expected legacy stdout line, got %q", out)
		}

		out = captureStdout(t, func() {
			cmd := hooksCmd()
			cmd.SetArgs([]string{"install", "state"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err = cmd.Execute()
		})
		if err != nil {
			t.Fatalf("expected exit 0, got %v", err)
		}
		if !strings.Contains(out, "[x] state: ") {
			t.Fatalf("expected legacy stdout line, got %q", out)
		}
	})

	t.Run("all-branch-unchanged", func(t *testing.T) {
		mustMkdir(t, filepath.Join(root, "learning"))
		writeSpaces(t, root, registeredLearningFixture("learning"))

		var err error
		out := captureStdout(t, func() {
			cmd := templateCmd()
			cmd.SetArgs([]string{"sync", "--all"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err = cmd.Execute()
		})
		if err != nil {
			t.Fatalf("--all must still exit 0, got %v", err)
		}
		if strings.Contains(out, "top-level directory of registered space") {
			t.Fatalf("--all must carry no per-feature guard:\n%s", out)
		}
		if !strings.Contains(out, "alpha:") {
			t.Fatalf("expected per-feature progress lines:\n%s", out)
		}

		out = captureStdout(t, func() {
			cmd := hooksCmd()
			cmd.SetArgs([]string{"install", "--all"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err = cmd.Execute()
		})
		if err != nil {
			t.Fatalf("--all must still exit 0, got %v", err)
		}
		if strings.Contains(out, "top-level directory of registered space") {
			t.Fatalf("--all must carry no per-feature guard:\n%s", out)
		}
	})
}

func TestSpaceGuard_NormalizedErrorPaths(t *testing.T) {
	t.Run("template-sync-usage", func(t *testing.T) {
		repo := setupGitRepo(t, "master")
		withWorkspaceEnv(t, repo)

		var err error
		out := captureStdout(t, func() {
			cmd := templateCmd()
			cmd.SetArgs([]string{"sync"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err = cmd.Execute()
		})
		if err == nil || !strings.Contains(err.Error(), "usage: tws template sync") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(out, "Usage: tws template sync") {
			t.Fatal("usage must no longer go to stdout")
		}
	})

	t.Run("hooks-install-checkout-mode", func(t *testing.T) {
		repo := setupGitRepoCheckout(t)
		withCheckoutEnv(t, repo)

		var err error
		out := captureStdout(t, func() {
			cmd := hooksCmd()
			cmd.SetArgs([]string{"install", "alpha"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err = cmd.Execute()
		})
		if err == nil || err.Error() != "hooks install requires linked worktrees; not supported in checkout mode" {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(out, "Error:") {
			t.Fatal("the refusal must no longer go to stdout")
		}
	})

	t.Run("hooks-install-no-feature-detected", func(t *testing.T) {
		repo := setupGitRepo(t, "master")
		withWorkspaceEnv(t, repo)

		cmd := hooksCmd()
		cmd.SetArgs([]string{"install"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "could not detect feature") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSpaceGuard_AbsentRegistryPreservesLegacyBehaviour(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	if err := addExternal("alpha", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}

	commands := []struct {
		name string
		run  func() error
	}{
		{"list", func() error {
			cmd := listCmd()
			cmd.SetArgs(nil)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"doctor", func() error {
			cmd := doctorCmd()
			cmd.SetArgs(nil)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"template-sync-all", func() error {
			cmd := templateCmd()
			cmd.SetArgs([]string{"sync", "--all"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"hooks-install-all", func() error {
			cmd := hooksCmd()
			cmd.SetArgs([]string{"install", "--all"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"template-sync-single", func() error {
			cmd := templateCmd()
			cmd.SetArgs([]string{"sync", "alpha"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
		{"hooks-install-single", func() error {
			cmd := hooksCmd()
			cmd.SetArgs([]string{"install", "alpha"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			return cmd.Execute()
		}},
	}
	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			_ = captureStdout(t, func() { err = tc.run() })
			if err != nil {
				t.Fatalf("%s must succeed with no registry: %v", tc.name, err)
			}
		})
	}

	// No spaces artifact anywhere.
	if _, err := os.Lstat(spacesFileIn(root)); err == nil {
		t.Fatal("spaces.yaml was created")
	}
	if _, err := os.Lstat(spacesLockIn(root)); err == nil {
		t.Fatal(".spaces.lock was created")
	}
	for _, line := range strings.Split(snapshotTree(t, root), "\n") {
		base := filepath.Base(line)
		if base == "spaces.yaml" || base == ".spaces.lock" || strings.HasPrefix(base, ".spaces-") {
			t.Fatalf("spaces artifact appeared: %s", line)
		}
	}
}

func TestSpaceGuard_NoFeaturesEmptyStatePreserved(t *testing.T) {
	repo := setupGitRepo(t, "master")
	mustMkdir(t, withUnifiedWorkspaceEnv(t, repo))

	var err error
	out := captureStdout(t, func() {
		cmd := templateCmd()
		cmd.SetArgs([]string{"sync", "--all"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
	})
	if err != nil || out != "No features found.\n" {
		t.Fatalf("template sync --all: err=%v out=%q", err, out)
	}

	out = captureStdout(t, func() {
		cmd := hooksCmd()
		cmd.SetArgs([]string{"install", "--all"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
	})
	if err != nil || out != "No features found.\n" {
		t.Fatalf("hooks install --all: err=%v out=%q", err, out)
	}
}
