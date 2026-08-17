package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// New-mode push (§8.5 steps 5-7) against a real bare remote: the push is
// strict, records every success in the payload before the next attempt, and
// leaves the run resumable when the remote refuses one branch.
// ---------------------------------------------------------------------------

// rejectPushOf installs a real pre-receive hook in the bare remote that refuses
// exactly one Git ref. It returns the removal function that "restores" the
// remote.
func rejectPushOf(t *testing.T, remote, gitBranch string) func() {
	t.Helper()
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nwhile read old new ref; do\n\tif [ \"$ref\" = \"refs/heads/" + gitBranch + "\" ]; then\n\t\techo \"refusing $ref\" >&2\n\t\texit 1\n\tfi\ndone\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Remove(hook); err != nil {
			t.Fatal(err)
		}
	}
}

func remoteRefs(t *testing.T, remote string) string {
	t.Helper()
	return gitOutput(t, remote, "for-each-ref", "--format=%(refname)")
}

func loadPayload(t *testing.T, featurePath string) *internal.SyncRunState {
	t.Helper()
	payload, err := internal.LoadSyncRunState(featurePath)
	if err != nil {
		t.Fatalf("the payload must survive a push failure: %v", err)
	}
	return payload
}

// TestSyncPush_FailureAfterOneSuccessIsResumable pins the resumable strict
// push: the first selected entry is pushed and recorded, the second fails, the
// run keeps all its state, and a plain --continue pushes only the second.
func TestSyncPush_FailureAfterOneSuccessIsResumable(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	restoreRemote := rejectPushOf(t, f.remote, "child")

	stdout, stderr, exit := runSync(t, f.feature, "--from", "parent", "--push")
	if exit == 0 {
		t.Fatalf("a refused push must fail the run:\n%s\n%s", stdout, stderr)
	}
	if !strings.Contains(stdout, "  [+] parent (pushed)") {
		t.Fatalf("the first selected entry must be pushed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "  [x] child (push failed)") {
		t.Fatalf("missing the frozen push-failure line:\n%s", stdout)
	}
	if !strings.Contains(stderr, "push failed for child") {
		t.Fatalf("stderr must name the failing entry: %q", stderr)
	}

	payload := loadPayload(t, f.featurePath)
	if payload.Stage != internal.SyncStageFailed {
		t.Fatalf("stage = %q, want failed", payload.Stage)
	}
	if payload.FailedBranch != "child" {
		t.Fatalf("failed_branch = %q, want the REAL name child", payload.FailedBranch)
	}
	if got := strings.Join(payload.Pushed, ","); got != "parent" {
		t.Fatalf("pushed = %q, want only the entry Git actually accepted", got)
	}
	for _, path := range []string{
		internal.SyncStatePath(f.featurePath),
		internal.SyncRunGuardPath(f.featurePath),
	} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("a push failure must leave %s in place for --continue", path)
		}
	}
	refs := remoteRefs(t, f.remote)
	if !strings.Contains(refs, "refs/heads/parent") {
		t.Fatalf("the successful push must be on the remote:\n%s", refs)
	}
	if strings.Contains(refs, "refs/heads/child") {
		t.Fatalf("the refused branch must not be on the remote:\n%s", refs)
	}

	// Resume: the remote works again and nothing else changed.
	restoreRemote()
	f.detachGuard(t)
	parentSHA := f.sha(t, "parent")
	childSHA := f.sha(t, "child")
	parentRemote := gitOutput(t, f.remote, "rev-parse", "refs/heads/parent")

	stdout, stderr, exit = runSync(t, f.feature, "--continue")
	if exit != 0 {
		t.Fatalf("--continue must retry the unpushed entry: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "  [+] child (pushed)") {
		t.Fatalf("the unpushed entry must be retried:\n%s", stdout)
	}
	if strings.Contains(stdout, "  [+] parent (pushed)") {
		t.Fatalf("an already pushed entry must never be pushed twice:\n%s", stdout)
	}
	if strings.Contains(stdout, "  [+] parent (active)") || strings.Contains(stdout, "  [+] child (active)") {
		t.Fatalf("a completed rebase must never be redone on --continue:\n%s", stdout)
	}
	if strings.Contains(stdout, formatSyncStatus("child", "active", "resolved")) {
		t.Fatalf("a push failure is not a conflict: the entry is already completed and must not be reported resolved:\n%s", stdout)
	}
	if got := f.sha(t, "parent"); got != parentSHA {
		t.Fatalf("parent moved on --continue: %s -> %s", parentSHA, got)
	}
	if got := f.sha(t, "child"); got != childSHA {
		t.Fatalf("child moved on --continue: %s -> %s", childSHA, got)
	}
	if got := gitOutput(t, f.remote, "rev-parse", "refs/heads/parent"); got != parentRemote {
		t.Fatalf("the already pushed remote ref moved: %s -> %s", parentRemote, got)
	}
	if got := gitOutput(t, f.remote, "rev-parse", "refs/heads/child"); got != childSHA {
		t.Fatalf("remote child = %s, want the local child %s", got, childSHA)
	}
	f.stateFilesGone(t)
}

// TestSyncPush_ScopeAllUsesTheLegacyFeaturePush pins §7.6: a new-mode run whose
// scope is `all` pushes the whole feature through the legacy loop, so every
// materialized entry is considered — including an anchor a `--local-only` run
// deliberately did not rebase — and a rejected push stays lenient.
func TestSyncPush_ScopeAllUsesTheLegacyFeaturePush(t *testing.T) {
	f := newScopedFixture(t)
	// Give the propagation edges real work; the anchor is never rebased under
	// --local-only, which is exactly why its push must still happen.
	writeAndCommit(t, f.wt("root"), "root-v2.txt", "root-v2\n", "root v2")
	rootSHA := f.sha(t, "root")

	stdout, stderr, exit := runSync(t, f.feature, "--local-only", "--no-fetch", "--push")
	if exit != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "  [-] root (no in-stack parent edge to propagate)") {
		t.Fatalf("the anchor must be a local-only no-op:\n%s", stdout)
	}
	if !strings.Contains(stdout, "  [+] root (pushed)") {
		t.Fatalf("a scope=all push must still push the untouched anchor:\n%s", stdout)
	}
	for _, name := range []string{"parent", "child"} {
		if !strings.Contains(stdout, "  [+] "+name+" (pushed)") {
			t.Fatalf("%s must be pushed by a scope=all run:\n%s", name, stdout)
		}
	}
	refs := remoteRefs(t, f.remote)
	for _, ref := range []string{"refs/heads/root", "refs/heads/parent", "refs/heads/child"} {
		if !strings.Contains(refs, ref) {
			t.Fatalf("missing %s on the remote:\n%s", ref, refs)
		}
	}
	if got := gitOutput(t, f.remote, "rev-parse", "refs/heads/root"); got != rootSHA {
		t.Fatalf("the anchor ref = %s, want the untouched local tip %s", got, rootSHA)
	}
	f.stateFilesGone(t)
}

// TestSyncPush_ScopeAllKeepsTheLenientFailure pins the other half of §7.6: the
// legacy loop reports a rejected push per entry and still exits 0, so a
// scope=all run behaves exactly like `tws push`, not like the strict scoped
// push.
func TestSyncPush_ScopeAllKeepsTheLenientFailure(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	defer rejectPushOf(t, f.remote, "child")()

	stdout, stderr, exit := runSync(t, f.feature, "--full", "--no-fetch", "--push")
	if exit != 0 {
		t.Fatalf("a scope=all push failure stays success-shaped: exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "  [x] child (push failed)") {
		t.Fatalf("missing the lenient per-entry failure line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "  [+] parent (pushed)") {
		t.Fatalf("the legacy loop continues past a failure:\n%s", stdout)
	}
	refs := remoteRefs(t, f.remote)
	if strings.Contains(refs, "refs/heads/child") {
		t.Fatalf("the refused branch must not be on the remote:\n%s", refs)
	}
	// A lenient push is not a failed run: the run finished and tore its state
	// down, so nothing is left to --continue.
	f.stateFilesGone(t)
}

// TestSyncPush_FailureWithNoPriorSuccessKeepsPushedEmpty pins the zero-success
// case: an unreachable remote fails the very first push, `pushed` stays empty,
// and --continue pushes the whole selection in selection order.
func TestSyncPush_FailureWithNoPriorSuccessKeepsPushedEmpty(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)

	// Make the remote unreachable for the duration of the first run.
	offline := f.remote + ".offline"
	if err := os.Rename(f.remote, offline); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runSync(t, f.feature, "--from", "parent", "--push", "--no-fetch")
	if exit == 0 {
		t.Fatalf("an unreachable remote must fail the run:\n%s\n%s", stdout, stderr)
	}
	if !strings.Contains(stdout, "  [x] parent (push failed)") {
		t.Fatalf("missing the push-failure line for the first entry:\n%s", stdout)
	}
	if strings.Contains(stdout, "child (push") {
		t.Fatalf("the push must stop at the first failure:\n%s", stdout)
	}

	payload := loadPayload(t, f.featurePath)
	if payload.Stage != internal.SyncStageFailed || payload.FailedBranch != "parent" {
		t.Fatalf("payload = stage %q failed_branch %q, want failed/parent", payload.Stage, payload.FailedBranch)
	}
	if len(payload.Pushed) != 0 {
		t.Fatalf("pushed = %v, want empty: Git never accepted anything", payload.Pushed)
	}
	if got := strings.Join(payload.Completed, ","); !strings.Contains(got, "parent") || !strings.Contains(got, "child") {
		t.Fatalf("completed = %q, want both rebased entries", got)
	}

	if err := os.Rename(offline, f.remote); err != nil {
		t.Fatal(err)
	}
	f.detachGuard(t)

	stdout, stderr, exit = runSync(t, f.feature, "--continue")
	if exit != 0 {
		t.Fatalf("--continue must retry every unpushed entry: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	parentAt := strings.Index(stdout, "  [+] parent (pushed)")
	childAt := strings.Index(stdout, "  [+] child (pushed)")
	if parentAt < 0 || childAt < 0 {
		t.Fatalf("both entries must be pushed on resume:\n%s", stdout)
	}
	if parentAt > childAt {
		t.Fatalf("the push must follow selection order (parent before child):\n%s", stdout)
	}
	refs := remoteRefs(t, f.remote)
	for _, ref := range []string{"refs/heads/parent", "refs/heads/child"} {
		if !strings.Contains(refs, ref) {
			t.Fatalf("missing %s on the remote:\n%s", ref, refs)
		}
	}
	if strings.Contains(refs, "refs/heads/root") {
		t.Fatalf("an unselected entry reached the remote:\n%s", refs)
	}
	f.stateFilesGone(t)
}
