package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func TestScopedPushSelectedMultiRepositoryOwnership(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	other := setupGitRepo(t, "main")
	otherRemote := filepath.Join(filepath.Dir(other), "remote.git")
	gitRun(t, other, "branch", "C", "main")
	otherC := gitOutput(t, other, "rev-parse", "C")
	f.stack.Branches[2].Repo = other
	f.stack.Branches[5].Repo = filepath.Join(f.repo, "unavailable-neighbor")
	f.saveStack(t)
	out, stderr, err := runPublication(t, "--from", "A")
	if err != nil {
		t.Fatalf("selected multi-repository publication: %v\n%s\n%s", err, out, stderr)
	}
	if strings.Contains(remoteRefs(t, f.remote), "refs/heads/C") ||
		gitOutput(t, otherRemote, "rev-parse", "C") != otherC {
		t.Fatal("selected explicit repository was published through the default owner")
	}
	if got := remoteRefs(t, otherRemote); got != "refs/heads/C\nrefs/heads/main" {
		t.Fatalf("unselected branches reached explicit repository: %s", got)
	}
}

func TestScopedPushNeverBorrowsUnrelatedCallerRepository(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	other := setupGitRepo(t, "main")
	gitRun(t, other, "branch", "B", "main")
	t.Chdir(other)
	out, stderr, err := runPublication(t, "--only", "B")
	if err != nil {
		t.Fatalf("selected workspace from unrelated caller: %v\n%s\n%s", err, out, stderr)
	}
	if got := gitOutput(t, f.remote, "rev-parse", "B"); got != f.oids["B"] {
		t.Fatal("caller supplied the source instead of the selected workspace")
	}
	if refs := remoteRefs(t, filepath.Join(filepath.Dir(other), "remote.git")); strings.Contains(refs, "refs/heads/B") {
		t.Fatal("unrelated caller remote was contacted")
	}
}

func TestScopedPushRelativeOriginRejectsNestedRepositoryBeforeEffects(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	nested := filepath.Join(f.repo, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(f.repo, "remote.git")
	gitRun(t, "", "init", "--bare", decoy)
	gitRun(t, f.repo, "remote", "set-url", "origin", "../remote.git")
	gitRun(t, nested, "push", "origin", "A")
	if got := gitOutput(t, f.remote, "rev-parse", "A"); got != f.oids["A"] || remoteRefs(t, decoy) != "" {
		t.Fatal("plain Git did not publish relative to the actual repository root")
	}
	f.stack.Branches[1].Repo = nested
	f.saveStack(t)
	realBefore, decoyBefore := remoteRefs(t, f.remote), remoteRefs(t, decoy)
	for _, args := range [][]string{{"--only", "B", "--dry-run"}, {"--only", "B"}} {
		if _, _, err := runPublication(t, args...); err == nil || !strings.Contains(err.Error(), "subdirectory") {
			t.Fatalf("nested authored repository context was accepted: %v", err)
		}
		if remoteRefs(t, f.remote) != realBefore || remoteRefs(t, decoy) != decoyBefore {
			t.Fatal("nested-context refusal changed an actual or decoy remote")
		}
		if _, err := os.Lstat(internal.ScopedPushStatePath(f.location())); !os.IsNotExist(err) {
			t.Fatal("unsupported nested context published state")
		}
	}
	f.stack.Branches[1].Repo = f.repo
	f.saveStack(t)
	if out, stderr, err := runPublication(t, "--only", "B"); err != nil {
		t.Fatalf("canonical root did not publish: %v\n%s\n%s", err, out, stderr)
	}
	if got := gitOutput(t, f.remote, "rev-parse", "B"); got != f.oids["B"] || remoteRefs(t, decoy) != "" {
		t.Fatal("canonical publication reinterpreted the relative origin")
	}
}

func TestScopedPushRepositoryExecutionRootProof(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	separate := filepath.Join(t.TempDir(), "checkout")
	admin := filepath.Join(t.TempDir(), "administration")
	gitRun(t, "", "clone", "--separate-git-dir", admin, f.repo, separate)
	for _, test := range []struct{ name, root string }{
		{"main", f.repo},
		{"linked", f.worktree("B")},
		{"bare", f.remote},
		{"separate-git-dir", separate},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, err := internal.ScopedPushRepositoryAt(test.root)
			if err != nil {
				t.Fatalf("proven execution root refused: %v", err)
			}
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(test.root, alias); err != nil {
				t.Fatal(err)
			}
			aliased, err := internal.ScopedPushRepositoryAt(alias)
			if err != nil || aliased.Directory.Spelling != alias || aliased.Directory.Identity != repo.Directory.Identity {
				t.Fatalf("original root alias binding was lost: %+v %v", aliased, err)
			}
			nested := filepath.Join(test.root, "nested")
			if err := os.Mkdir(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := internal.ScopedPushRepositoryAt(nested); err == nil {
				t.Fatal("non-root context was accepted as an execution root")
			}
		})
	}
	for _, directory := range []string{filepath.Join(f.repo, ".git"), admin} {
		if _, err := internal.ScopedPushRepositoryAt(directory); err == nil {
			t.Fatalf("non-bare administrative context was accepted: %s", directory)
		}
	}
}

func TestScopedPushRefOnlyOwnerNeedsNarrowProof(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	for _, entry := range f.stack.Branches {
		gitRun(t, f.repo, "worktree", "remove", f.worktree(entry.Name))
	}
	customRoot := filepath.Join(filepath.Dir(f.repo), "custom-workspace")
	if err := os.Rename(f.root, customRoot); err != nil {
		t.Fatal(err)
	}
	f.root, f.featurePath = customRoot, filepath.Join(customRoot, "feature")
	t.Setenv("TWS_ROOT", customRoot)
	other := setupGitRepo(t, "main")
	t.Chdir(other)
	if _, _, err := runPublication(t, "--only", "B"); err == nil || !strings.Contains(err.Error(), "cannot prove one default owner") {
		t.Fatalf("unproved default owner borrowed caller: %v", err)
	}
	writeConfig := func(text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(internal.ConfigPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(internal.ConfigPath(), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(fmt.Sprintf("workspaces:\n  %q: %q\n", f.repo, customRoot))
	t.Chdir(f.featurePath)
	if out, stderr, err := runPublication(t, "--only", "B"); err != nil {
		t.Fatalf("explicit mapping not used for ref-only feature root: %v\n%s\n%s", err, out, stderr)
	}
	writeConfig(fmt.Sprintf("workspaces:\n  %q: %q\n  %q: %q\n", f.repo, customRoot, other, customRoot))
	if _, _, err := runPublication(t, "--only", "A"); err == nil || !strings.Contains(err.Error(), "cannot prove one default owner") {
		t.Fatalf("ambiguous default mapping was accepted: %v", err)
	}
}

func TestScopedPushFallbackRetainsBothRenameAdmissions(t *testing.T) {
	f := featureRenameReviewCLI(t)
	selected := filepath.Join(f.root, "selected-root")
	if err := os.MkdirAll(filepath.Join(selected, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_ROOT", selected)
	beforeA, beforeB := featureRenameReviewSnapshot(t, f.workspace), featureRenameReviewSnapshot(t, selected)
	if out, stderr, err := runManagedCobra(t, pushCmd(), []string{"old", "--only", "entry", "--dry-run"}, nil); err != nil {
		t.Fatalf("healthy scoped fallback preview: %v\n%s\n%s", err, out, stderr)
	}
	if beforeA != featureRenameReviewSnapshot(t, f.workspace) || beforeB != featureRenameReviewSnapshot(t, selected) {
		t.Fatal("scoped fallback preview created admission state")
	}
	stop := errors.New("stop under both namespace admissions")
	called := false
	internal.ScopedPushCheckpoint = func(step string, _ int) error {
		if step != "before-push" {
			return nil
		}
		called = true
		for _, root := range []string{selected, f.workspace} {
			_, err := internal.RenameExternalFeature(internal.Workspace{
				Mode: internal.ModeExternal, RepoRoot: f.repo, MetadataRoot: root,
			}, "old", "must-not-move", "", nil)
			if err == nil || !strings.Contains(err.Error(), "feature rename admission is busy") {
				t.Fatalf("scoped publication released admission at %s: %v", root, err)
			}
		}
		return stop
	}
	t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
	if _, _, err := runManagedCobra(t, pushCmd(), []string{"old", "--only", "entry"}, nil); !errors.Is(err, stop) || !called {
		t.Fatalf("fallback execution: called=%v err=%v", called, err)
	}
	internal.ScopedPushCheckpoint = nil
	if _, _, err := runManagedCobra(t, pushCmd(), []string{"old", "--cancel"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestScopedPushSelectedPendingNamespacePrecedesFallback(t *testing.T) {
	configured := newPublicationFixture(t, internal.ModeExternal)
	selected := newPublicationFixture(t, internal.ModeExternal)
	t.Chdir(configured.repo)
	t.Setenv("TWS_ROOT", selected.root)
	restore := rejectPushOf(t, selected.remote, "B")
	if _, _, err := runPublication(t, "--from", "A"); err == nil {
		t.Fatal("expected a genuine partially published selected-root journal")
	}
	if state := selected.state(t); state.Rows[0] != "succeeded" || state.Rows[1] != "intent" {
		t.Fatalf("publication was not partially accepted: %v", state.Rows)
	}
	stackPath := internal.StackPath(selected.featurePath)
	if err := os.Rename(stackPath, stackPath+".saved"); err != nil {
		t.Fatal(err)
	}
	before := publicationFiles(t, configured.repo, configured.root, configured.remote, selected.repo, selected.root, selected.remote)
	for _, test := range []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"scoped-push", pushCmd(), []string{"feature", "--only", "B"}},
		{"legacy-push", pushCmd(), []string{"feature"}},
		{"scoped-sync-push", syncCmd(), []string{"feature", "--only", "B", "--no-fetch", "--push"}},
		{"scoped-preview", pushCmd(), []string{"feature", "--only", "B", "--dry-run"}},
		{"legacy-preview", pushCmd(), []string{"feature", "--dry-run"}},
		{"sync-preview", syncCmd(), []string{"feature", "--only", "B", "--no-fetch", "--plan"}},
		{"reparent-preview", stackReparentCmd(), []string{"feature", "B", "--onto", "main", "--plan", "--no-fetch"}},
		{"reparent-execution", stackReparentCmd(), []string{"feature", "B", "--onto", "main", "--no-fetch", "--max-replay-total", "10", "--approve-plan", strings.Repeat("a", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if out, stderr, err := runManagedCobra(t, test.cmd, test.args, nil); err == nil || !strings.Contains(err.Error(), "scoped push") {
				t.Fatalf("selected pending namespace escaped to configured root: %v\n%s\n%s", err, out, stderr)
			}
			if !reflect.DeepEqual(before, publicationFiles(t, configured.repo, configured.root, configured.remote, selected.repo, selected.root, selected.remote)) {
				t.Fatal("refusal changed a root, repository, or remote")
			}
		})
	}
	if out, _, err := runPublication(t, "--continue", "--dry-run"); err != nil || !strings.Contains(out, selected.oids["B"]) {
		t.Fatalf("own-journal preview was blocked or redirected: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(before, publicationFiles(t, configured.repo, configured.root, configured.remote, selected.repo, selected.root, selected.remote)) {
		t.Fatal("own-journal preview was not read-only")
	}
	if err := os.Rename(stackPath+".saved", stackPath); err != nil {
		t.Fatal(err)
	}
	restore()
	if out, stderr, err := runPublication(t, "--continue"); err != nil {
		t.Fatalf("own selected-root continuation failed: %v\n%s\n%s", err, out, stderr)
	}
	if got := remoteRefs(t, configured.remote); got != "refs/heads/main" {
		t.Fatalf("continuation published to the configured root: %s", got)
	}
	if got := gitOutput(t, selected.remote, "rev-parse", "B"); got != selected.oids["B"] {
		t.Fatal("continuation did not finish the original selected publication")
	}
}

func TestScopedPushTerminalReceiptCannotShadowPendingFallback(t *testing.T) {
	for _, recovery := range []string{"--continue", "--cancel"} {
		t.Run(recovery, func(t *testing.T) {
			configured := newPublicationFixture(t, internal.ModeExternal)
			selected := newPublicationFixture(t, internal.ModeExternal)
			t.Chdir(configured.repo)
			t.Setenv("TWS_ROOT", selected.root)
			if _, _, err := runPublication(t, "--only", "X"); err != nil {
				t.Fatal(err)
			}
			selectedReceipt, err := os.ReadFile(internal.ScopedPushStatePath(selected.location()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(internal.StackPath(selected.featurePath), internal.StackPath(selected.featurePath)+".saved"); err != nil {
				t.Fatal(err)
			}
			restore := rejectPushOf(t, configured.remote, "B")
			if _, _, err := runPublication(t, "--from", "A"); err == nil {
				t.Fatal("expected a genuine pending fallback run")
			}
			if state := configured.state(t); state.Rows[0] != "succeeded" || state.Rows[1] != "intent" {
				t.Fatal("fallback did not partially publish")
			}
			before := publicationFiles(t, configured.repo, configured.root, configured.remote, selected.root, selected.remote)
			out, _, err := runPublication(t, "--continue", "--dry-run")
			if err != nil || !strings.Contains(out, configured.oids["B"]) || !strings.Contains(out, "intent") {
				t.Fatalf("terminal receipt shadowed pending preview: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(before, publicationFiles(t, configured.repo, configured.root, configured.remote, selected.root, selected.remote)) {
				t.Fatal("pending preview changed evidence")
			}
			restore()
			if out, stderr, err := runPublication(t, recovery); err != nil {
				t.Fatalf("terminal receipt shadowed recovery: %v\n%s\n%s", err, out, stderr)
			}
			want := "completed"
			if recovery == "--cancel" {
				want = "canceled"
			}
			if configured.state(t).Phase != want {
				t.Fatal("recovery did not finish the pending fallback")
			}
			after, err := os.ReadFile(internal.ScopedPushStatePath(selected.location()))
			if err != nil || string(after) != string(selectedReceipt) {
				t.Fatal("unrelated selected terminal receipt changed")
			}
		})
	}
}

func TestScopedPushUntrustedSelectedNamespacePrecedesFallback(t *testing.T) {
	for _, kind := range []string{"corrupt", "unsupported", "symlink", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeExternal)
			selected := filepath.Join(t.TempDir(), "selected")
			feature := filepath.Join(selected, "feature")
			if err := os.MkdirAll(feature, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TWS_ROOT", selected)
			path := internal.ScopedPushStatePath(internal.ScopedPushLocation{Mode: internal.ModeExternal, FeaturePath: feature})
			var err error
			switch kind {
			case "corrupt":
				err = os.WriteFile(path, []byte("{broken"), 0o600)
			case "unsupported":
				err = os.WriteFile(path, []byte(`{"kind":"tws-scoped-publication","version":999}`), 0o600)
			case "symlink":
				err = os.Symlink(filepath.Join(f.repo, ".git", "config"), path)
			case "fifo":
				err = unix.Mkfifo(path, 0o600)
			case "directory":
				err = os.Mkdir(path, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := publicationFiles(t, f.repo, f.root, f.remote)
			for _, test := range []struct {
				cmd  *cobra.Command
				args []string
			}{
				{pushCmd(), []string{"feature", "--only", "B"}},
				{pushCmd(), []string{"feature", "--dry-run"}},
				{syncCmd(), []string{"feature", "--only", "B", "--no-fetch", "--push"}},
				{stackReparentCmd(), []string{"feature", "B", "--onto", "main", "--plan", "--no-fetch"}},
			} {
				if _, _, err := runManagedCobra(t, test.cmd, test.args, nil); err == nil || !strings.Contains(err.Error(), "scoped push") {
					t.Fatalf("untrusted selected journal was bypassed: %v", err)
				}
			}
			if !reflect.DeepEqual(before, publicationFiles(t, f.repo, f.root, f.remote)) {
				t.Fatal("untrusted selected state changed the configured workspace")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("untrusted publication evidence was removed")
			}
		})
	}
}

func TestScopedPushFallbackRechecksPublicationAfterNameAdmission(t *testing.T) {
	configured := newPublicationFixture(t, internal.ModeExternal)
	selected := newPublicationFixture(t, internal.ModeExternal)
	t.Chdir(configured.repo)
	t.Setenv("TWS_ROOT", selected.root)
	restore := rejectPushOf(t, selected.remote, "B")
	defer restore()
	if _, _, err := runPublication(t, "--only", "B"); err == nil {
		t.Fatal("expected a genuine selected-root journal")
	}
	if err := os.Rename(internal.StackPath(selected.featurePath), internal.StackPath(selected.featurePath)+".saved"); err != nil {
		t.Fatal(err)
	}
	path := internal.ScopedPushStatePath(selected.location())
	expectedSelectedRoot, err := filepath.EvalSymlinks(selected.root)
	if err != nil {
		t.Fatal(err)
	}
	before := publicationFiles(t, configured.repo, configured.root, configured.remote)
	for _, test := range []struct {
		cmd  *cobra.Command
		args []string
	}{
		{pushCmd(), []string{"feature", "--only", "B"}},
		{pushCmd(), []string{"feature"}},
		{syncCmd(), []string{"feature", "--only", "B", "--no-fetch", "--push"}},
		{stackReparentCmd(), []string{"feature", "B", "--onto", "main", "--no-fetch", "--max-replay-total", "10", "--approve-plan", strings.Repeat("a", 64)}},
	} {
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		restored := false
		internal.FeatureRenameCheckpoint = func(step, opened string) error {
			if !restored && step == "before-lock-open" && filepath.Base(opened) == "feature" && strings.HasPrefix(opened, expectedSelectedRoot+string(filepath.Separator)) {
				restored = true
				return os.Rename(path+".saved", path)
			}
			return nil
		}
		t.Cleanup(func() { internal.FeatureRenameCheckpoint = nil })
		_, _, err := runManagedCobra(t, test.cmd, test.args, nil)
		internal.FeatureRenameCheckpoint = nil
		if err == nil || !restored || !strings.Contains(err.Error(), "scoped push") {
			t.Fatalf("publication appeared after resolution but crossed admission: restored=%v err=%v", restored, err)
		}
		if !reflect.DeepEqual(before, publicationFiles(t, configured.repo, configured.root, configured.remote)) {
			t.Fatal("late selected publication allowed configured-root effects")
		}
	}
}

func TestScopedPushCompletionUsesSelectedLayoutAndIsPure(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	other := setupGitRepo(t, "main")
	t.Chdir(other)
	f.stack.Branches[1].Name, f.stack.Branches[1].Branch = "logical/api", "B"
	f.stack.Branches[3].Base = "logical/api"
	f.stack.Branches[2].Archived = true
	f.saveStack(t)
	before := publicationFiles(t, f.repo, f.root, f.remote)
	names, directive := pushEntryCompletion(pushCmd(), []string{"feature"}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp || !reflect.DeepEqual(names, []string{"A", "logical/api", "D", "X", "Y"}) {
		t.Fatalf("execution-selected logical completion: %v %v", names, directive)
	}
	if !reflect.DeepEqual(before, publicationFiles(t, f.repo, f.root, f.remote)) {
		t.Fatal("completion mutated selected state")
	}
	if names, _ := pushEntryCompletion(pushCmd(), []string{"missing"}, ""); len(names) != 0 {
		t.Fatalf("unavailable completion should be silent: %v", names)
	}
}

func TestScopedPushRootAliasesRemainBoundAcrossRecovery(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	alias := filepath.Join(filepath.Dir(f.repo), "workspace-alias")
	if err := os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_ROOT", alias)
	restore := rejectPushOf(t, f.remote, "B")
	if _, _, err := runPublication(t, "--only", "B"); err == nil {
		t.Fatal("expected blocked publication through root alias")
	}
	restore()
	if err := os.Rename(alias, alias+".held-old-inode"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runPublication(t, "--continue"); err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("replacement root alias was adopted: %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(alias+".held-old-inode", alias); err != nil {
		t.Fatal(err)
	}
	if out, stderr, err := runPublication(t, "--continue"); err != nil {
		t.Fatalf("original proven alias could not recover: %v\n%s\n%s", err, out, stderr)
	}
}

func TestScopedPushTerminalReceiptDoesNotBlockFeatureRenameAndNewPush(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	if _, _, err := runPublication(t, "--only", "B"); err != nil {
		t.Fatal(err)
	}
	oldProbe := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldProbe })
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil); err != nil {
		t.Fatalf("terminal receipt blocked feature rename: %v", err)
	}
	if out, stderr, err := runManagedCobra(t, pushCmd(), []string{"renamed", "--only", "C"}, nil); err != nil {
		t.Fatalf("historical terminal receipt prevented a fresh push: %v\n%s\n%s", err, out, stderr)
	}
	if got := gitOutput(t, f.remote, "rev-parse", "C"); got != f.oids["C"] {
		t.Fatal("fresh publication after rename used the wrong source")
	}
}

func TestScopedPushOldGitRefusesLocallyWithoutChangingLegacy(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nfor arg do\n  if [ \"$arg\" = --no-lazy-fetch ]; then exit 129; fi\ndone\nexec %q \"$@\"\n", realGit)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	before := publicationFiles(t, f.repo, f.root, f.remote)
	if _, _, err := runPublication(t, "--only", "B", "--dry-run"); err == nil || !strings.Contains(err.Error(), "Git 2.45") {
		t.Fatalf("unsupported Git did not fail closed before local object probes: %v", err)
	}
	if !reflect.DeepEqual(before, publicationFiles(t, f.repo, f.root, f.remote)) {
		t.Fatal("old-Git refusal mutated state")
	}
	if _, _, err := runPublication(t, "--dry-run"); err != nil {
		t.Fatalf("new standalone Git requirement changed legacy push: %v", err)
	}
}

func TestScopedPushPreviewCannotLazilyFetchMissingCommit(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	gitRun(t, f.repo, "push", "origin", "A")
	gitRun(t, f.repo, "config", "remote.origin.promisor", "true")
	gitRun(t, f.repo, "config", "remote.origin.partialclonefilter", "blob:none")
	oid := f.oids["A"]
	if err := os.Remove(filepath.Join(f.repo, ".git", "objects", oid[:2], oid[2:])); err != nil {
		t.Fatal(err)
	}
	before := publicationFiles(t, f.repo, f.root, f.remote)
	if _, _, err := runPublication(t, "--only", "A", "--dry-run"); err == nil {
		t.Fatal("missing local source was fetched or accepted")
	}
	if !reflect.DeepEqual(before, publicationFiles(t, f.repo, f.root, f.remote)) {
		t.Fatal("preview lazily fetched an object or changed metadata")
	}
}

func TestScopedPushSpecialSharedLocksRefusePromptly(t *testing.T) {
	for _, mode := range []internal.WorkspaceMode{internal.ModeExternal, internal.ModeCheckout} {
		for _, kind := range []string{"fifo", "symlink", "directory"} {
			t.Run(string(mode)+"/"+kind, func(t *testing.T) {
				f := newPublicationFixture(t, mode)
				path := internal.SyncRunGuardPath(f.featurePath)
				if mode == internal.ModeCheckout {
					path = internal.CheckoutMutationLockPath(filepath.Join(f.root, "state"))
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				switch kind {
				case "fifo":
					err = unix.Mkfifo(path, 0o600)
				case "symlink":
					err = os.Symlink(filepath.Join(f.repo, ".git", "config"), path)
				case "directory":
					err = os.Mkdir(path, 0o700)
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := runPublication(t, "--only", "B"); err == nil {
					t.Fatal("special shared lock was adopted")
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("special shared lock was removed")
				}
				if strings.Contains(remoteRefs(t, f.remote), "refs/heads/B") {
					t.Fatal("published despite invalid shared lock")
				}
			})
		}
	}
}

func TestScopedPushSpecialStackRefusesAndFrozenPreviewStaysReadable(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	restore := rejectPushOf(t, f.remote, "B")
	if _, _, err := runPublication(t, "--only", "B"); err == nil {
		t.Fatal("expected a frozen failed publication")
	}
	restore()
	path := internal.StackPath(f.featurePath)
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, err := runPublication(t, "--continue", "--dry-run"); err != nil || !strings.Contains(out, f.oids["B"]) {
		t.Fatalf("frozen preview incorrectly read live stack FIFO: %v\n%s", err, out)
	}
	if _, _, err := runPublication(t, "--continue"); err == nil {
		t.Fatal("execution accepted non-regular selected metadata")
	}
	if _, _, err := runPublication(t, "--cancel"); err != nil {
		t.Fatalf("cancellation needed to read unrelated live stack bytes: %v", err)
	}
	if _, _, err := runPublication(t, "--only", "B", "--dry-run"); err == nil {
		t.Fatal("fresh preview accepted a stack FIFO")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("publication changed the operator's stack object")
	}
}

func TestScopedPushCheckoutStateDirectorySymlinkIsNotFollowed(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeCheckout)
	foreign := t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(f.root, "state")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--only", "B", "--dry-run"}, {"--only", "B"}, {"--cancel"}} {
		if _, _, err := runPublication(t, args...); err == nil {
			t.Fatal("publication followed a foreign state-directory symlink")
		}
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 {
		t.Fatalf("publication wrote through a foreign state directory: %v %v", entries, err)
	}
}

func TestScopedPushAndExistingSyncClearOnlySelectedFollowup(t *testing.T) {
	for _, mode := range []internal.WorkspaceMode{internal.ModeExternal, internal.ModeCheckout} {
		t.Run(string(mode), func(t *testing.T) {
			f := newPublicationFixture(t, mode)
			gitRun(t, f.repo, "push", "origin", "B")
			record := publicationFollowup(t, f, "B", f.oids["B"], f.oids["A"], true)
			if mode == internal.ModeCheckout {
				// Checkout sync's whole-stack single-repository validation is
				// unchanged. This unselected row would clear if probed.
				f.stack.Branches[5].Repo = ""
				f.saveStack(t)
				gitRun(t, f.repo, "update-ref", "refs/remotes/origin/Y", f.oids["Y"])
				if err := os.WriteFile(filepath.Join(f.repo, ".git", "info", "exclude"), []byte(".tws/\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			out, stderr, exit := runSync(t, "feature", "--only", "B", "--local-only", "--no-fetch", "--push")
			if exit != 0 {
				t.Fatalf("scoped sync touched an unavailable follow-up neighbor: %d\n%s\n%s", exit, out, stderr)
			}
			after, err := internal.LoadReparentRemoteRecord(f.location().ReparentLocation())
			if err != nil || after == nil || after.Entries[1] != record.Entries[1] || after.CreatedAt != record.CreatedAt {
				t.Fatalf("scoped sync altered unselected follow-up: %+v %v", after, err)
			}
			if after.Entries[0].State != internal.ReparentRemoteStateCleared {
				t.Fatal("scoped sync did not clear selected proven follow-up")
			}
		})
	}
}

func TestScopedPushRenamedTerminalHistoryDoesNotHideFallback(t *testing.T) {
	for _, recovery := range []string{"--continue", "--cancel"} {
		t.Run(recovery, func(t *testing.T) {
			configured := newPublicationFixture(t, internal.ModeExternal)
			oldProbe := managedTemplateTmuxProbe
			managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
			t.Cleanup(func() { managedTemplateTmuxProbe = oldProbe })
			if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil); err != nil {
				t.Fatal(err)
			}
			configured.featurePath = filepath.Join(configured.root, "renamed")
			selected := newPublicationFixture(t, internal.ModeExternal)
			if _, _, err := runPublication(t, "--only", "X"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil); err != nil {
				t.Fatal(err)
			}
			selected.featurePath = filepath.Join(selected.root, "renamed")
			receiptPath := internal.ScopedPushStatePath(selected.location())
			receipt, err := os.ReadFile(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(internal.StackPath(selected.featurePath), internal.StackPath(selected.featurePath)+".saved"); err != nil {
				t.Fatal(err)
			}
			t.Chdir(configured.repo)
			t.Setenv("TWS_ROOT", selected.root)
			restore := rejectPushOf(t, configured.remote, "B")
			if _, _, err := runManagedCobra(t, pushCmd(), []string{"renamed", "--from", "A"}, nil); err == nil {
				t.Fatal("expected pending configured-root fallback")
			}
			before := publicationFiles(t, configured.repo, configured.root, configured.remote, selected.root, selected.remote)
			out, _, err := runManagedCobra(t, pushCmd(), []string{"renamed", "--continue", "--dry-run"}, nil)
			if err != nil || !strings.Contains(out, configured.oids["B"]) || !strings.Contains(out, "intent") {
				t.Fatalf("renamed history hid pending preview: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(before, publicationFiles(t, configured.repo, configured.root, configured.remote, selected.root, selected.remote)) {
				t.Fatal("pending preview changed renamed history or publication")
			}
			restore()
			if _, _, err := runManagedCobra(t, pushCmd(), []string{"renamed", recovery}, nil); err != nil {
				t.Fatal(err)
			}
			want := "completed"
			if recovery == "--cancel" {
				want = "canceled"
			}
			if configured.state(t).Phase != want {
				t.Fatal("renamed terminal receipt hid the pending fallback")
			}
			after, err := os.ReadFile(receiptPath)
			if err != nil || string(after) != string(receipt) {
				t.Fatal("historical terminal bytes were rewritten")
			}
		})
	}
}

func TestScopedPushRenamedTerminalHistoryKeepsPhysicalOwnership(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	if _, _, err := runPublication(t, "--only", "B"); err != nil {
		t.Fatal(err)
	}
	oldProbe := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldProbe })
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil); err != nil {
		t.Fatal(err)
	}
	f.featurePath = filepath.Join(f.root, "renamed")
	if _, _, err := runManagedCobra(t, pushCmd(), []string{"renamed", "--continue"}, nil); err != nil {
		t.Fatalf("verified terminal history cleanup failed after rename: %v", err)
	}
	body, err := os.ReadFile(internal.ScopedPushStatePath(f.location()))
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(f.root, "foreign")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, ".push-state.v1.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, pushCmd(), []string{"foreign", "--cancel"}, nil); err == nil {
		t.Fatal("copied terminal bytes granted ownership of another feature inode")
	}
}
