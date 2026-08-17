package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// tws push after the layout refactor (§3.11, §7.6, AC 59).
// ---------------------------------------------------------------------------

func runPush(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	stdout, stderr = syncCaptureStreams(t, func() {
		exit = syncExecute(pushCmd, args...)
	})
	return stdout, stderr, exit
}

// TestPushLayout_ExternalPushesTheGitBranch pins the C2 fix: the pushed ref is
// entry.GitBranch(), never the logical Name.
func TestPushLayout_ExternalPushesTheGitBranch(t *testing.T) {
	f := newScopedFixture(t)
	gitRun(t, f.wt("child"), "branch", "-m", "child", "user/child")
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range stack.Branches {
		if stack.Branches[i].Name == "child" {
			stack.Branches[i].Branch = "user/child"
		}
	}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runPush(t, f.feature)
	if exit != 0 {
		t.Fatalf("exit = %d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "[+] child (pushed)") {
		t.Fatalf("the per-entry line is keyed by the logical Name:\n%s\n%s", stdout, stderr)
	}
	refs := gitOutput(t, f.remote, "for-each-ref", "--format=%(refname)")
	if !strings.Contains(refs, "refs/heads/user/child") {
		t.Fatalf("the pushed ref must be the Git branch:\n%s", refs)
	}
	// Coupled entries are unchanged.
	for _, name := range []string{"root", "parent"} {
		if !strings.Contains(refs, "refs/heads/"+name) {
			t.Fatalf("coupled entry %s must still be pushed:\n%s", name, refs)
		}
	}
}

// TestPushLayout_DryRunIsUnchanged keeps the read-only surface stable.
func TestPushLayout_DryRunIsUnchanged(t *testing.T) {
	f := newScopedFixture(t)
	stdout, stderr, exit := runPush(t, f.feature, "--dry-run")
	if exit != 0 {
		t.Fatalf("exit = %d\n%s\n%s", exit, stdout, stderr)
	}
	for _, name := range []string{"root", "parent", "child"} {
		if !strings.Contains(stdout, "  [~] "+name+" (would push --force-with-lease)") {
			t.Fatalf("missing dry-run line for %s:\n%s", name, stdout)
		}
	}
	refs := gitOutput(t, f.remote, "for-each-ref", "--format=%(refname)")
	if strings.Contains(refs, "refs/heads/root") {
		t.Fatalf("--dry-run must push nothing:\n%s", refs)
	}
}

// TestPushLayout_CheckoutModeStillUnsupported pins that checkout-mode push
// keeps today's ErrWorktreeUnsupported failure and a nonzero exit.
func TestPushLayout_CheckoutModeStillUnsupported(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := setupGitRepoCheckout(t)
	t.Setenv("HOME", dir)
	t.Setenv("TWS_ROOT", "")
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.Mode != internal.ModeCheckout {
		t.Fatalf("fixture mode = %s, want checkout", ws.Mode)
	}
	featurePath := filepath.Join(ws.MetadataRoot, "features", "auth")
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "api", Base: "main"}}}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runPush(t, "auth")
	if exit == 0 {
		t.Fatalf("checkout push must keep failing:\n%s\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "linked worktrees are not supported in checkout mode") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// TestPushLayout_SelectedPushIsOrderedAndFiltered pins the new-mode push: only
// the selected, rebased entries are pushed, in selection order, and every
// success is recorded in the payload.
func TestPushLayout_SelectedPushIsOrderedAndFiltered(t *testing.T) {
	f := newScopedFixture(t)
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	policy := internal.SyncRunPolicy{
		Fetch:       internal.SyncFetchDisabled,
		Propagation: internal.SyncPropagationFull,
		ScopeKind:   internal.SyncScopeSubtree,
		Selector:    "parent",
	}
	sel, err := internal.ResolveSyncSelection(stack, policy, internal.SyncSelectionOpts{
		Mode:    internal.ModeExternal,
		NewMode: true,
		Feature: f.feature,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := internal.NewSyncRunState(f.feature, "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock", "token", policy)
	payload.Selected = sel.SelectedNames()

	stdout, _ := syncCaptureStreams(t, func() {
		if err := pushScoped(f.feature, f.layout, stack, sel, []string{"parent", "child"}, payload); err != nil {
			t.Errorf("pushScoped: %v", err)
		}
	})
	if !strings.Contains(stdout, "[+] parent (pushed)") || !strings.Contains(stdout, "[+] child (pushed)") {
		t.Fatalf("missing per-entry lines:\n%s", stdout)
	}
	if strings.Contains(stdout, "root") {
		t.Fatalf("the new-mode push must push only the selected entries:\n%s", stdout)
	}
	if got := strings.Join(payload.Pushed, ","); got != "parent,child" {
		t.Fatalf("payload pushed = %q, want the selection order parent,child", got)
	}
	refs := gitOutput(t, f.remote, "for-each-ref", "--format=%(refname)")
	if strings.Contains(refs, "refs/heads/root") {
		t.Fatalf("an unselected entry reached the remote:\n%s", refs)
	}
}

// TestPushLayout_ArchivedEntriesAreSkipped keeps the frozen skip line.
func TestPushLayout_ArchivedEntriesAreSkipped(t *testing.T) {
	f := newScopedFixture(t)
	if err := os.RemoveAll(f.wt("child")); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runPush(t, f.feature)
	if exit != 0 {
		t.Fatalf("exit = %d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "  [-] child (archived, skipped)") {
		t.Fatalf("missing the archived skip line:\n%s", stdout)
	}
}
