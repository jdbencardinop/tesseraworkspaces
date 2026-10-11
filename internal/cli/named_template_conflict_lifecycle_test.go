package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

type namedConflictSessionRunner struct {
	run func(string, []string) error
}

func (runner namedConflictSessionRunner) Run(dir string, command []string) error {
	return runner.run(dir, command)
}

func TestNamedTemplateConflictCheckoutSessionExportAndFeatureRename(t *testing.T) {
	f, path, _ := newNamedConflictFixture(t, nil)
	namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "kept session bytes\n")
	namedConflictWrite(t, filepath.Join(path, "notes.md"), "kept orchestrator bytes\n")
	fakeNamedConflictTTY(t)
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader("keep\nkeep\n")); err != nil {
		t.Fatal(err)
	}
	originalLedger := namedConflictRead(t, internal.ManagedTemplateStatePath(path))
	managedTemplateInputIsTTY = managedTemplateTerminalInput
	links, err := internal.PlanCheckoutSessionLinks(f.ws, "feature", ".")
	if err != nil {
		t.Fatal(err)
	}
	excludes, err := internal.ApplyCheckoutSessionLinks(f.repo, links)
	if err != nil {
		t.Fatal(err)
	}
	if namedConflictRead(t, filepath.Join(f.repo, "shared.md")) != "kept session bytes\n" {
		t.Fatal("checkout temporary context used incoming instead of effective bytes")
	}
	if err := internal.CleanupCheckoutSessionLinks(f.repo, links, excludes); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.repo, "shared.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout session leaked permanent context: %v", err)
	}
	namedConflictWrite(t, filepath.Join(f.repo, ".git", "info", "exclude"), ".tws/\n")
	if _, _, err := runManagedCobra(t, newCmd(), []string{"feature", "session-entry", "--base", "main"}, nil); err != nil {
		t.Fatal(err)
	}
	stack, err := internal.LoadStack(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := internal.GetBranch(stack, "session-entry")
	ran := false
	runner := namedConflictSessionRunner{run: func(dir string, command []string) error {
		ran = true
		if !sameResolvedRepoRoot(dir, f.repo) || !reflect.DeepEqual(command, []string{"fake-session-agent"}) {
			t.Fatalf("checkout launch root/command changed: %s %v", dir, command)
		}
		if namedConflictRead(t, filepath.Join(dir, "shared.md")) != "kept session bytes\n" {
			t.Fatal("launched session did not use customization")
		}
		if present, err := internal.CheckoutAgentSessionPresence(f.ws); err != nil || !present {
			t.Fatalf("session did not own temporary context: %v %v", present, err)
		}
		return nil
	}}
	if err := internal.OpenCheckoutDirect(f.ws, "feature", entry, []string{"fake-session-agent"}, runner, nil, "."); err != nil {
		t.Fatal(err)
	}
	if !ran || namedConflictRead(t, internal.ManagedTemplateStatePath(path)) != originalLedger {
		t.Fatal("session launch changed v2 ownership")
	}
	if _, _, err := runManagedCobra(t, openCmd(), []string{"feature", "session-entry", "--no-agent"}, nil); err != nil {
		t.Fatalf("open could not validate effective owned context: %v", err)
	}
	if _, _, err := runManagedCobra(t, injectCmd(), []string{"feature"}, nil); err == nil || !strings.Contains(err.Error(), "checkout") {
		t.Fatalf("checkout permanent injection refusal changed: %v", err)
	}

	tarPath := filepath.Join(f.root, "effective-export.tar.gz")
	_, warning, err := runManagedCobra(t, exportCmd(), []string{"feature", "--full", "-o", tarPath}, nil)
	if err != nil || !strings.Contains(warning, "omits managed template selections") {
		t.Fatalf("export's existing portability warning changed: %v %s", err, warning)
	}
	archive, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close() //nolint:errcheck
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close() //nolint:errcheck
	reader := tar.NewReader(compressed)
	found := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(header.Name, ".tws-templates") {
			t.Fatal("resolver silently changed the shipped export/import ownership format")
		}
		if header.Name == "inject/shared.md" {
			data, err := io.ReadAll(reader)
			if err != nil || string(data) != "kept session bytes\n" {
				t.Fatalf("export did not preserve effective bytes: %q %v", data, err)
			}
			found = true
		}
	}
	if !found || namedConflictRead(t, internal.ManagedTemplateStatePath(path)) != originalLedger {
		t.Fatal("export lost content or changed live acknowledgement metadata")
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil); err != nil {
		t.Fatal(err)
	}
	renamed := f.ws.FeaturePath("renamed")
	if namedConflictRead(t, internal.ManagedTemplateStatePath(renamed)) != originalLedger {
		t.Fatal("feature rename changed relative acknowledgement metadata")
	}
	if err := internal.ValidateManagedCheckoutContext(f.ws, "renamed", renamed, "."); err != nil {
		t.Fatal(err)
	}
	namedConflictWrite(t, filepath.Join(renamed, "inject", "shared.md"), "unacknowledged session bytes\n")
	if _, err := internal.PlanCheckoutSessionLinks(f.ws, "renamed", "."); err == nil {
		t.Fatal("session path silently acknowledged a later local edit")
	}
}

func TestNamedTemplateConflictExternalMultiRepoLifecycle(t *testing.T) {
	falseValue := false
	fx := newManagedSecondaryFixture(t, "override-lifecycle",
		internal.Config{AgentCommand: "claude", UseTmux: &falseValue},
		internal.Config{AgentCommand: "copilot", UseTmux: &falseValue, InjectInto: ".custom"},
	)
	t.Chdir(fx.observer)
	f := fx.inject
	log := installManagedLaunchFakes(t)
	worktreeSource := filepath.Join(f.featurePath, "inject", "CLAUDE.local.md")
	namedConflictWrite(t, worktreeSource, "kept external worktree\n")
	namedConflictWrite(t, filepath.Join(f.featurePath, "CLAUDE.md"), "kept external orchestrator\n")
	fakeNamedConflictTTY(t)
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--resolve-conflicts"}, strings.NewReader("keep\nkeep\n")); err != nil {
		t.Fatal(err)
	}
	state, err := internal.LoadManagedTemplateState(f.featurePath)
	if err != nil || state.Version != 2 {
		t.Fatalf("external overrides not saved: %+v %v", state, err)
	}
	originalFiles := make(map[string][]byte)
	for role, value := range state.Roles {
		originalFiles[role], err = json.Marshal(value.Files)
		if err != nil {
			t.Fatal(err)
		}
	}
	managedTemplateInputIsTTY = managedTemplateTerminalInput
	for _, test := range []struct {
		command *cobra.Command
		args    []string
	}{
		{openCmd(), []string{f.feature, "entry"}},
		{openCmd(), []string{f.feature, "--feature-dir"}},
		{injectCmd(), []string{f.feature, "--into", "."}},
		{newCmd(), []string{f.feature, "nested/secondary", "--base", "main", "--repo", fx.secondary}},
		{openCmd(), []string{f.feature, "nested/secondary", "--no-agent"}},
		{renameCmd(), []string{"branch", f.feature, "nested/secondary", "deeper/renamed/entry"}},
		{archiveCmd(), []string{f.feature, "deeper/renamed/entry"}},
		{newCmd(), []string{f.feature, "deeper/renamed/entry"}},
	} {
		if _, _, err := runManagedCobra(t, test.command, test.args, nil); err != nil {
			t.Fatalf("v2 effective context failed on %s %v: %v", test.command.Name(), test.args, err)
		}
		state, err = internal.LoadManagedTemplateState(f.featurePath)
		if err != nil || state.Version != 2 {
			t.Fatalf("lifecycle dropped v2: %+v %v", state, err)
		}
		for role, value := range state.Roles {
			got, err := json.Marshal(value.Files)
			if err != nil || !bytes.Equal(originalFiles[role], got) {
				t.Fatalf("%s %v changed %s decisions: %s %v", test.command.Name(), test.args, role, got, err)
			}
		}
	}
	secondaryPath := filepath.Join(f.featurePath, "worktrees", "deeper", "renamed", "entry")
	secondaryRepo, err := internal.MainRepoRootIn(secondaryPath)
	if err != nil || !sameResolvedRepoRoot(secondaryRepo, fx.secondary) {
		t.Fatalf("reactivated slash worktree lost owning entry repository: %s %v", secondaryRepo, err)
	}
	if namedConflictRead(t, filepath.Join(secondaryPath, "CLAUDE.local.md")) != "kept external worktree\n" {
		t.Fatal("rename/archive/reactivation lost effective content")
	}
	if strings.Contains(namedConflictRead(t, log), "copilot") {
		t.Fatal("external launch used secondary repository renderer/configuration")
	}
	namedConflictWrite(t, worktreeSource, "later unacknowledged worktree edit\n")
	for _, test := range []struct {
		command *cobra.Command
		args    []string
	}{
		{openCmd(), []string{f.feature, "entry", "--no-agent"}},
		{injectCmd(), []string{f.feature, "entry"}},
		{newCmd(), []string{f.feature, "must-not-exist", "--base", "main"}},
		{renameCmd(), []string{"branch", f.feature, "entry", "must-not-rename"}},
		{archiveCmd(), []string{f.feature, "entry"}},
	} {
		if _, stderr, err := runManagedCobra(t, test.command, test.args, strings.NewReader("keep\n")); err == nil || strings.Contains(stderr, "[k/r") {
			t.Fatalf("nonresolver lifecycle accepted/prompted for unacknowledged edit: %v %v %q", test.args, err, stderr)
		}
	}
}

func TestNamedTemplateConflictExternalHardPreflightAndPromptIdentityDrift(t *testing.T) {
	for _, change := range []string{"tracked", "link", "worktree", "prompt-link", "prompt-worktree"} {
		t.Run(change, func(t *testing.T) {
			f := newInjectFixture(t, "external-conflict")
			t.Setenv("TWS_ROOT", f.workspace)
			t.Chdir(f.repo)
			worktree := f.addWorktree(t, f.repo, "nested/entry", "external-conflict-entry")
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "nested/entry", Branch: "external-conflict-entry"}}}); err != nil {
				t.Fatal(err)
			}
			old := managedTemplateTmuxProbe
			managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
			t.Cleanup(func() { managedTemplateTmuxProbe = old })
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--scope", "worktree"}, nil); err != nil {
				t.Fatal(err)
			}
			namedConflictWrite(t, filepath.Join(f.featurePath, "inject", "CLAUDE.local.md"), "local via owned source\n")
			mutate := func() {
				dest := filepath.Join(worktree, "CLAUDE.local.md")
				switch change {
				case "tracked":
					if err := os.Remove(dest); err != nil {
						t.Fatal(err)
					}
					namedConflictWrite(t, dest, "tracked collision\n")
					injectGit(t, worktree, "add", "-f", "--", "CLAUDE.local.md")
				case "link", "prompt-link":
					target, err := os.Readlink(dest)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(dest, dest+"-old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, dest); err != nil {
						t.Fatal(err)
					}
				case "worktree", "prompt-worktree":
					if err := os.Rename(worktree, worktree+"-old"); err != nil {
						t.Fatal(err)
					}
					copyManagedTreeForTest(t, worktree+"-old", worktree)
				}
			}
			var before string
			prompt := strings.HasPrefix(change, "prompt-")
			fakeNamedConflictTTY(t)
			reader := &namedConflictReader{reader: strings.NewReader("keep\n"), before: func() {
				if !prompt {
					t.Fatal("hard external blocker was shown as a conflict menu")
				}
				mutate()
				before = snapshotTree(t, f.featurePath)
			}}
			if !prompt {
				mutate()
				before = snapshotTree(t, f.featurePath)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--scope", "worktree", "--resolve-conflicts", "--allow-managed-removal"}, reader); err == nil {
				t.Fatal("tracked/external identity conflict was bypassed")
			}
			if snapshotTree(t, f.featurePath) != before {
				t.Fatal("external hard/drift refusal changed feature or links")
			}
		})
	}
}
