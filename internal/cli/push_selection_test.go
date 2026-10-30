package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

type publicationFixture struct {
	repo, remote, featurePath, root string
	mode                            internal.WorkspaceMode
	stack                           internal.Stack
	oids                            map[string]string
}

func newPublicationFixture(t *testing.T, mode internal.WorkspaceMode) *publicationFixture {
	t.Helper()
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(global, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	repo := setupGitRepo(t, "main")
	t.Chdir(repo)
	f := &publicationFixture{
		repo: repo, remote: filepath.Join(filepath.Dir(repo), "remote.git"), mode: mode,
		root: repo + ".tws", oids: map[string]string{"main": gitOutput(t, repo, "rev-parse", "HEAD")},
	}
	if mode == internal.ModeCheckout {
		f.root = filepath.Join(repo, ".tws")
		if err := os.MkdirAll(f.root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, "config.yaml"), []byte("workspace_mode: checkout\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TWS_ROOT", "")
		f.featurePath = filepath.Join(f.root, "features", "feature")
	} else {
		t.Setenv("TWS_ROOT", f.root)
		f.featurePath = filepath.Join(f.root, "feature")
	}
	if err := os.MkdirAll(f.featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	tree := gitOutput(t, repo, "rev-parse", "HEAD^{tree}")
	for _, row := range [][2]string{{"A", "main"}, {"B", "A"}, {"C", "A"}, {"D", "B"}, {"X", "main"}, {"Y", "X"}} {
		name, parent := row[0], row[1]
		oid := gitOutput(t, repo, "commit-tree", tree, "-p", f.oids[parent], "-m", "entry "+name)
		f.oids[name] = oid
		gitRun(t, repo, "update-ref", "refs/heads/"+name, oid)
		f.stack.Branches = append(f.stack.Branches, internal.StackEntry{Name: name, Base: parent, LastBaseSHA: f.oids[parent]})
		if mode == internal.ModeExternal {
			gitRun(t, repo, "worktree", "add", f.worktree(name), name)
		}
	}
	f.saveStack(t)
	return f
}

func (f *publicationFixture) worktree(name string) string {
	return filepath.Join(f.featurePath, "worktrees", name)
}

func (f *publicationFixture) location() internal.ScopedPushLocation {
	return internal.ScopedPushLocation{Mode: f.mode, Feature: "feature", FeaturePath: f.featurePath, MetadataRoot: f.root}
}

func (f *publicationFixture) saveStack(t *testing.T) {
	t.Helper()
	if err := internal.SaveStack(f.featurePath, f.stack); err != nil {
		t.Fatal(err)
	}
}

func (f *publicationFixture) state(t *testing.T) *internal.ScopedPushState {
	t.Helper()
	state, err := internal.LoadScopedPushState(f.location())
	if err != nil || state == nil {
		t.Fatalf("publication state: %+v %v", state, err)
	}
	return state
}

func runPublication(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := pushCmd()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"feature"}, args...))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return out.String(), stderr.String(), err
}

func publicationFiles(t *testing.T, paths ...string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, path := range paths {
		err := filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if entry.IsDir() {
				files[path] = "directory"
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				files[path] = "symlink:" + target
				return err
			}
			body, err := os.ReadFile(path)
			files[path] = string(body)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestScopedPushForkedSelection(t *testing.T) {
	for _, mode := range []internal.WorkspaceMode{internal.ModeExternal, internal.ModeCheckout} {
		for _, test := range []struct {
			flag, selector string
			names          []string
		}{
			{"--from", "A", []string{"A", "B", "C", "D"}},
			{"--from", "B", []string{"B", "D"}},
			{"--only", "B", []string{"B"}},
		} {
			t.Run(string(mode)+"/"+test.flag+"/"+test.selector, func(t *testing.T) {
				f := newPublicationFixture(t, mode)
				dirtyPath := filepath.Join(f.repo, "local-dirty")
				if mode == internal.ModeExternal {
					dirtyPath = filepath.Join(f.worktree("X"), "local-dirty")
					// An unrelated unavailable worktree remains in Git's
					// registration and in stack.yaml; it is not an archive.
					if err := os.Rename(f.worktree("Y"), f.worktree("Y")+".offline"); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(dirtyPath, []byte("preserve dirty bytes\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				before := publicationFiles(t, f.repo, f.root, f.remote)
				out, stderr, err := runPublication(t, test.flag, test.selector, "--dry-run")
				if err != nil || stderr != "" || !strings.Contains(out, "Remote state: unverified") {
					t.Fatalf("preview: %v\n%s\n%s", err, out, stderr)
				}
				after := publicationFiles(t, f.repo, f.root, f.remote)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("local-only preview changed repository, workspace, or remote bytes")
				}
				for _, name := range test.names {
					if !strings.Contains(out, f.oids[name]+" -> origin:refs/heads/"+name) {
						t.Fatalf("preview omits exact source/destination %s:\n%s", name, out)
					}
				}
				out, stderr, err = runPublication(t, test.flag, test.selector)
				if err != nil {
					t.Fatalf("publish: %v\n%s\n%s", err, out, stderr)
				}
				published := map[string]bool{}
				for _, name := range test.names {
					published[name] = true
					if got := gitOutput(t, f.remote, "rev-parse", "refs/heads/"+name); got != f.oids[name] {
						t.Fatalf("remote %s=%s, want frozen %s", name, got, f.oids[name])
					}
				}
				for _, name := range []string{"A", "B", "C", "D", "X", "Y"} {
					if got := gitOutput(t, f.repo, "rev-parse", "refs/heads/"+name); got != f.oids[name] {
						t.Fatalf("local %s changed", name)
					}
					if !published[name] && strings.Contains(remoteRefs(t, f.remote), "refs/heads/"+name+"\n") {
						t.Fatalf("unselected ref %s was published", name)
					}
				}
				if got := gitOutput(t, f.repo, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
					t.Fatalf("physical checkout switched: %s", got)
				}
				if body, err := os.ReadFile(dirtyPath); err != nil || string(body) != "preserve dirty bytes\n" {
					t.Fatal("dirty bytes changed")
				}
				if refs := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname)", "refs/tws/push"); refs != "" {
					t.Fatalf("completed publication left private refs: %s", refs)
				}
				if state := f.state(t); state.Phase != "completed" {
					t.Fatalf("not durably completed: %+v", state)
				}
			})
		}
	}
}

func TestScopedPushRefOnlyAndDecoupledSlashNames(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	for _, entry := range f.stack.Branches {
		gitRun(t, f.repo, "worktree", "remove", f.worktree(entry.Name))
	}
	f.stack.Branches[1].Name = "review/api"
	f.stack.Branches[1].Branch = "B"
	f.stack.Branches[3].Base = "review/api"
	f.saveStack(t)
	t.Chdir(f.featurePath)
	out, stderr, err := runPublication(t, "--only", "review/api")
	if err != nil {
		t.Fatalf("ref-only push from feature root: %v\n%s\n%s", err, out, stderr)
	}
	if got := gitOutput(t, f.remote, "rev-parse", "refs/heads/B"); got != f.oids["B"] {
		t.Fatal("logical name was used as the Git destination")
	}
	if _, err := os.Stat(f.worktree("review/api")); !os.IsNotExist(err) {
		t.Fatal("publication materialized a worktree")
	}
}

func TestScopedPushArchivedAndSelectionErrors(t *testing.T) {
	for _, mode := range []internal.WorkspaceMode{internal.ModeExternal, internal.ModeCheckout} {
		t.Run(string(mode), func(t *testing.T) {
			f := newPublicationFixture(t, mode)
			f.stack.Branches[2].Archived = true
			f.saveStack(t)
			before := remoteRefs(t, f.remote)
			for _, selector := range []string{"missing", "C"} {
				if _, _, err := runPublication(t, "--only", selector); err == nil {
					t.Fatalf("selected unknown/archive %s accepted", selector)
				}
			}
			out, stderr, err := runPublication(t, "--from", "A")
			if err != nil || !strings.Contains(out, "C (archived, skipped)") {
				t.Fatalf("archived closure: %v\n%s\n%s", err, out, stderr)
			}
			if strings.Contains(remoteRefs(t, f.remote), "refs/heads/C") || before == remoteRefs(t, f.remote) {
				t.Fatal("archived closure publication was wrong")
			}
			f.stack.Branches[3].Branch = "B"
			f.saveStack(t)
			if _, _, err := runPublication(t, "--from", "B"); err == nil || !strings.Contains(err.Error(), "share Git branch") {
				t.Fatalf("duplicate selected Git identity: %v", err)
			}
			gitRun(t, f.repo, "update-ref", "-d", "refs/heads/X")
			if _, _, err := runPublication(t, "--only", "X"); err == nil || !strings.Contains(err.Error(), "no local ref") {
				t.Fatalf("missing active ref: %v", err)
			}
		})
	}
}

func TestScopedPushFrozenRetrySkipsConfirmedAndNewDescendants(t *testing.T) {
	for _, mode := range []internal.WorkspaceMode{internal.ModeExternal, internal.ModeCheckout} {
		t.Run(string(mode), func(t *testing.T) {
			f := newPublicationFixture(t, mode)
			restore := rejectPushOf(t, f.remote, "B")
			out, stderr, err := runPublication(t, "--from", "A")
			if err == nil {
				t.Fatalf("rejected publication succeeded:\n%s\n%s", out, stderr)
			}
			state := f.state(t)
			if state.Rows[0] != "succeeded" || state.Rows[1] != "intent" || state.Rows[2] != "pending" {
				t.Fatalf("partial results were not frozen: %v", state.Rows)
			}
			if _, _, err := runPublication(t, "--only", "A"); err == nil || !strings.Contains(err.Error(), "pending") {
				t.Fatalf("fresh run adopted pending publication: %v", err)
			}
			restore()
			gitRun(t, f.remote, "update-ref", "refs/heads/A", f.oids["main"])
			gitRun(t, f.repo, "update-ref", "refs/heads/X", f.oids["B"])
			gitRun(t, f.repo, "branch", "new-child", "D")
			f.stack.Branches = append(f.stack.Branches, internal.StackEntry{Name: "new-child", Base: "D"})
			f.saveStack(t)
			before := publicationFiles(t, f.repo, f.root, f.remote)
			out, stderr, err = runPublication(t, "--continue", "--dry-run")
			if err != nil || strings.Contains(out, "new-child") || !strings.Contains(out, "succeeded") {
				t.Fatalf("frozen recovery preview: %v\n%s\n%s", err, out, stderr)
			}
			if !reflect.DeepEqual(before, publicationFiles(t, f.repo, f.root, f.remote)) {
				t.Fatal("recovery preview mutated state")
			}
			out, stderr, err = runPublication(t, "--continue")
			if err != nil {
				t.Fatalf("continue: %v\n%s\n%s", err, out, stderr)
			}
			if got := gitOutput(t, f.remote, "rev-parse", "A"); got != f.oids["main"] {
				t.Fatal("confirmed success was republished over later remote work")
			}
			if strings.Contains(remoteRefs(t, f.remote), "new-child") || strings.Contains(remoteRefs(t, f.remote), "refs/heads/X") {
				t.Fatal("continuation broadened its scope")
			}
		})
	}
}

func TestScopedPushFrozenAbsenceLeaseAndCancellation(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	restore := rejectPushOf(t, f.remote, "B")
	if _, _, err := runPublication(t, "--from", "A"); err == nil {
		t.Fatal("expected rejection")
	}
	restore()
	gitRun(t, f.remote, "update-ref", "refs/heads/B", f.oids["main"])
	gitRun(t, f.repo, "fetch", "origin")
	if _, _, err := runPublication(t, "--continue"); err == nil || !strings.Contains(err.Error(), "original lease") {
		t.Fatalf("advanced tracking refreshed authority: %v", err)
	}
	before := gitOutput(t, f.remote, "for-each-ref", "--format=%(refname) %(objectname)")
	out, _, err := runPublication(t, "--cancel")
	if err != nil || !strings.Contains(out, "not rolled back") {
		t.Fatalf("cancel: %v\n%s", err, out)
	}
	if after := gitOutput(t, f.remote, "for-each-ref", "--format=%(refname) %(objectname)"); after != before {
		t.Fatal("cancellation mutated remote refs")
	}
	if state := f.state(t); state.Phase != "canceled" {
		t.Fatal("cancellation was not durable")
	}
	if _, _, err := runPublication(t, "--continue"); err == nil {
		t.Fatal("continue resurrected a canceled plan")
	}
}

func TestScopedPushAcceptedBeforeRecording(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	internal.ScopedPushCheckpoint = func(step string, index int) error {
		if step == "after-push" && index == 0 {
			return errors.New("lost process after acceptance")
		}
		return nil
	}
	t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
	if _, _, err := runPublication(t, "--from", "A"); err == nil {
		t.Fatal("injected accepted-before-recording failure did not fire")
	}
	if f.state(t).Rows[0] != "intent" || gitOutput(t, f.remote, "rev-parse", "A") != f.oids["A"] {
		t.Fatal("fixture did not actually accept an unrecorded push")
	}
	internal.ScopedPushCheckpoint = nil
	restore := rejectPushOf(t, f.remote, "A")
	defer restore()
	out, stderr, err := runPublication(t, "--continue")
	if err != nil || !strings.Contains(out, "confirmed recorded source") {
		t.Fatalf("accepted source was not reconciled: %v\n%s\n%s", err, out, stderr)
	}
}

func TestScopedPushChildProcess(t *testing.T) {
	if os.Getenv("TWS_TEST_PUBLICATION_CHILD") != "1" {
		return
	}
	internal.ScopedPushCheckpoint = func(step string, index int) error {
		if step == "after-push" && index == 0 {
			return syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
		return nil
	}
	_, _, err := runPublication(t, "--from", "A")
	t.Fatalf("child did not die after real acceptance: %v", err)
}

func TestScopedPushProcessDeathReservesTypedState(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestScopedPushChildProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "TWS_TEST_PUBLICATION_CHILD=1")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("child survived:\n%s", output)
	}
	state := f.state(t)
	if state.Rows[0] != "intent" || gitOutput(t, f.remote, "rev-parse", "A") != f.oids["A"] {
		t.Fatal("child did not die in the accepted-before-recorded window")
	}
	if err := internal.ClaimSyncRunGuard(f.featurePath, strings.Repeat("a", 32)); err == nil {
		t.Fatal("stale guard without a sync payload was reclaimed over typed publication state")
	}
	for _, factory := range []func() *cobra.Command{syncCmd, archiveCmd, newCmd, deleteCmd} {
		command := factory()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SilenceUsage, command.SilenceErrors = true, true
		switch command.Name() {
		case "sync":
			command.SetArgs([]string{"feature", "--continue"})
		case "delete":
			command.SetArgs([]string{"feature"})
		default:
			command.SetArgs([]string{"feature", "B"})
		}
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "push") {
			t.Fatalf("%s adopted/erased pending publication: %v", command.Name(), err)
		}
	}
	restore := rejectPushOf(t, f.remote, "A")
	defer restore()
	if out, stderr, err := runPublication(t, "--continue"); err != nil {
		t.Fatalf("real process-death recovery: %v\n%s\n%s", err, out, stderr)
	}
}

func TestScopedPushCheckoutProcessDeathSurvivesMissingGlobalLock(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeCheckout)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestScopedPushChildProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "TWS_TEST_PUBLICATION_CHILD=1")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("child survived:\n%s", output)
	}
	if f.state(t).Rows[0] != "intent" || gitOutput(t, f.remote, "rev-parse", "A") != f.oids["A"] {
		t.Fatal("checkout child did not die after actual remote acceptance")
	}
	stateDir := filepath.Join(f.root, "state")
	if err := os.Remove(internal.CheckoutMutationLockPath(stateDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := internal.AcquireCheckoutMutationLock(stateDir, strings.Repeat("a", 32), "unrelated", "sync",
		filepath.Join(stateDir, "unrelated-checkout-sync.yaml")); err == nil {
		t.Fatal("checkout mutation scanner ignored typed publication without its global lock")
	}
	restore := rejectPushOf(t, f.remote, "A")
	defer restore()
	if out, stderr, err := runPublication(t, "--continue"); err != nil {
		t.Fatalf("checkout process-death recovery: %v\n%s\n%s", err, out, stderr)
	}
	if gitOutput(t, f.repo, "symbolic-ref", "HEAD") != "refs/heads/main" {
		t.Fatal("standalone checkout recovery switched HEAD")
	}
}

func TestScopedPushReftableObjectProtectionAndCleanup(t *testing.T) {
	t.Setenv("GIT_DEFAULT_REF_FORMAT", "reftable")
	f := newPublicationFixture(t, internal.ModeExternal)
	if format := gitOutput(t, f.repo, "rev-parse", "--show-ref-format"); format != "reftable" {
		t.Fatalf("fixture requires real reftable, got %q", format)
	}
	out, stderr, err := runPublication(t, "--only", "B")
	if err != nil {
		t.Fatalf("reftable publication: %v\n%s\n%s", err, out, stderr)
	}
	if got := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname)", "refs/tws/push"); got != "" {
		t.Fatalf("reftable cleanup left owned protection: %s", got)
	}
	if f.state(t).Phase != "completed" || gitOutput(t, f.remote, "rev-parse", "B") != f.oids["B"] {
		t.Fatal("reftable publication was not durably completed")
	}
}

func TestScopedPushPureFlagErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TWS_ROOT", filepath.Join(t.TempDir(), "missing"))
	for _, args := range [][]string{
		{"--only", ""}, {"--from", ""}, {"--only", "A", "--from", "B"},
		{"--continue", "--only", "A"}, {"--cancel", "--from", "A"},
		{"--continue", "--cancel"}, {"--cancel", "--dry-run"},
		{"--continue=false"}, {"--cancel=false"},
	} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			t.Setenv("PATH", "")
			if _, _, err := runPublication(t, args...); err == nil || !strings.Contains(err.Error(), "--") {
				t.Fatalf("flags were not validated before Git/workspace resolution: %v", err)
			}
		})
	}
}
