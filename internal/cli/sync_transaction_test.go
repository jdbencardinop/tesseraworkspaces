package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func TestSyncTransactionExternalResumeActionWindows(t *testing.T) {
	for _, window := range []string{"action-intent:rebase:child", "action-observed:rebase:child"} {
		t.Run(window, func(t *testing.T) {
			f := newScopedFixture(t)
			f.advanceRoot(t)
			stop := errors.New("stop at native action window")
			reached := false
			internal.SyncTransactionStepHook = func(step string) error {
				if step == window {
					reached = true
					return stop
				}
				return nil
			}
			t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
			_, stderr, exit := runSync(t, f.feature, "--no-fetch")
			if exit == 0 || !reached {
				t.Fatalf("did not stop at %s: exit=%d stderr=%s", window, exit, stderr)
			}
			internal.SyncTransactionStepHook = nil
			f.detachGuard(t)
			stdout, stderr, exit := runSync(t, f.feature, "--continue")
			if exit != 0 {
				t.Fatalf("continue: %d\n%s\n%s", exit, stdout, stderr)
			}
			gitRun(t, f.repo, "merge-base", "--is-ancestor", "root", "parent")
			gitRun(t, f.repo, "merge-base", "--is-ancestor", "parent", "child")
			f.stateFilesGone(t)
		})
	}
}

func TestSyncTransactionExternalPublicationBoundary(t *testing.T) {
	for _, failBeforePush := range []bool{true, false} {
		name := "failed-push"
		if failBeforePush {
			name = "intent-before-push"
		}
		t.Run(name, func(t *testing.T) {
			f := newScopedFixture(t)
			f.advanceRoot(t)
			if failBeforePush {
				internal.SyncTransactionStepHook = func(step string) error {
					if strings.HasPrefix(step, "publication-intent:") {
						return errors.New("stop before actual push")
					}
					return nil
				}
			} else {
				gitRun(t, f.repo, "remote", "set-url", "--push", "origin", f.repo+"/missing-remote")
			}
			t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
			stdout, stderr, exit := runSync(t, f.feature, "--no-fetch", "--push")
			if exit == 0 {
				t.Fatalf("expected publication failure:\n%s\n%s", stdout, stderr)
			}
			internal.SyncTransactionStepHook = nil
			payload, err := internal.LoadSyncRunState(f.featurePath)
			if err != nil || payload.Transaction == nil || !payload.Transaction.Publication.IntentDurable {
				t.Fatalf("publication intent was lost: %v %+v", err, payload)
			}
			before := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			f.detachGuard(t)
			stdout, stderr, exit = runSync(t, f.feature, "--abort")
			if exit == 0 || !strings.Contains(stdout+stderr, "publication boundary") {
				t.Fatalf("abort must refuse publication rollback:\n%s\n%s", stdout, stderr)
			}
			if after := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); after != before {
				t.Fatal("abort changed local refs after publication intent")
			}
			if _, err := os.Stat(internal.SyncRunStatePath(f.featurePath)); err != nil {
				t.Fatalf("abort removed forward recovery evidence: %v", err)
			}
		})
	}
}

func TestSyncTransactionCheckoutAbortBeforeCleanup(t *testing.T) {
	for _, window := range []string{"forward-holder-restore-intent:", "forward-holder-restore-applied:", "cleanup-started"} {
		t.Run(window, func(t *testing.T) {
			dir, feature := checkoutModeFixture(t)
			writeAndCommit(t, dir, "upstream-new.txt", "upstream\n", "advance upstream")
			beforeRefs := gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			beforeMetadata, err := os.ReadFile(internal.StackPath(feature))
			if err != nil {
				t.Fatal(err)
			}
			stop := errors.New("stop before forward cleanup")
			internal.SyncTransactionStepHook = func(step string) error {
				if strings.HasPrefix(step, window) {
					return stop
				}
				return nil
			}
			t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
			opts := newModeOpts(dir, feature, internal.SyncRunPolicy{
				Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull,
				ScopeKind: internal.SyncScopeAll,
			})
			if err := internal.RunCheckoutSync(opts); !errors.Is(err, stop) {
				t.Fatalf("did not reach final cleanup: %v", err)
			}
			internal.SyncTransactionStepHook = nil
			if window == "cleanup-started" {
				beforeRefs = gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
				beforeMetadata, err = os.ReadFile(internal.StackPath(feature))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := internal.AbortCheckoutSync(opts); err != nil {
				t.Fatalf("abort before cleanup completed: %v", err)
			}
			if after := gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); after != beforeRefs {
				t.Fatal("abort did not follow the rollback-versus-completed-cleanup decision")
			}
			afterMetadata, err := os.ReadFile(internal.StackPath(feature))
			if err != nil || string(afterMetadata) != string(beforeMetadata) {
				t.Fatalf("abort did not restore exact metadata: %v", err)
			}
		})
	}
}

func TestSyncTransactionCheckoutPushRetriesOnlyUnpublished(t *testing.T) {
	dir, feature := checkoutModeFixture(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitRun(t, dir, "init", "--bare", remote)
	gitRun(t, dir, "remote", "add", "origin", remote)
	gitRun(t, dir, "push", "-u", "origin", "main")
	gitRun(t, dir, "remote", "set-head", "origin", "main")
	restoreRemote := rejectPushOf(t, remote, "feat-a")
	opts := newModeOpts(dir, feature, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull,
		ScopeKind: internal.SyncScopeAll,
	})
	opts.Push = true
	if err := internal.RunCheckoutSync(opts); err == nil {
		t.Fatal("remote must reject feat-a after feat-root succeeds")
	}
	tx, err := internal.LoadCheckoutTransaction(feature)
	if err != nil || !internal.SyncTransactionEntryPublished(tx.Transaction, "feat-root") {
		t.Fatalf("first push success was not recorded: %v", err)
	}
	if err := internal.AbortCheckoutSync(opts); err == nil {
		t.Fatal("abort must refuse once any push was attempted")
	}
	restoreRemote()
	_, _, log := reparentPushCapture(t, func() {
		if err := internal.ContinueCheckoutSync(opts); err != nil {
			t.Fatalf("retry publication: %v", err)
		}
	})
	for _, argv := range log.argv {
		if argvHas(argv, "push") && argvHas(argv, "feat-root") {
			t.Fatalf("already published branch was pushed again: %v", argv)
		}
	}
	for _, branch := range []string{"feat-root", "feat-a", "feat-b"} {
		if remoteTip, localTip := gitOutput(t, remote, "rev-parse", branch), gitSHA(t, dir, branch); remoteTip != localTip {
			t.Fatalf("branch %s was not published", branch)
		}
	}
	if internal.HasCheckoutTransaction(feature) {
		t.Fatal("successful publication retry left transaction residue")
	}
}

func TestSyncTransactionCheckoutPublicationResultCrashResumesRemaining(t *testing.T) {
	dir, feature := checkoutModeFixture(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitRun(t, dir, "init", "--bare", remote)
	gitRun(t, dir, "remote", "add", "origin", remote)
	gitRun(t, dir, "push", "-u", "origin", "main")
	gitRun(t, dir, "remote", "set-head", "origin", "main")
	opts := newModeOpts(dir, feature, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull,
		ScopeKind: internal.SyncScopeAll,
	})
	opts.Push = true
	stop := errors.New("stop after saved push result")
	internal.SyncTransactionStepHook = func(step string) error {
		if step == "publication-result:feat-root" {
			return stop
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
	if err := internal.RunCheckoutSync(opts); !errors.Is(err, stop) {
		t.Fatalf("push-result crash window: %v", err)
	}
	internal.SyncTransactionStepHook = nil
	tx, err := internal.LoadCheckoutTransaction(feature)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Stage != internal.StagePublishing || !internal.SyncTransactionEntryPublished(tx.Transaction, "feat-root") {
		t.Fatalf("durable publication progress was not resumable: stage=%s results=%+v", tx.Stage, tx.Transaction.Publication.Results)
	}
	_, _, log := reparentPushCapture(t, func() {
		if err := internal.ContinueCheckoutSync(opts); err != nil {
			t.Fatalf("continue remaining publication: %v", err)
		}
	})
	for _, argv := range log.argv {
		if !argvHas(argv, "push") {
			continue
		}
		for _, arg := range argv {
			if strings.HasSuffix(arg, ":refs/heads/feat-root") {
				t.Fatalf("already published checkout branch was repeated: %v", argv)
			}
		}
	}
	for _, branch := range []string{"feat-root", "feat-a", "feat-b"} {
		if remoteTip, localTip := gitOutput(t, remote, "rev-parse", branch), gitSHA(t, dir, branch); remoteTip != localTip {
			t.Fatalf("branch %s was not published", branch)
		}
	}
}

func TestSyncTransactionArchivedEntryRollsBack(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	gitRun(t, f.repo, "worktree", "remove", f.wt("child"))
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	testSyncTransactionArchivedEntryRollsBackAfterLoad(t, f, stack)
}

func TestSyncArchivedEntryRefreshesCutoffAfterParentAmend(t *testing.T) {
	f := newScopedFixture(t)
	oldParent := gitOutput(t, f.repo, "rev-parse", "parent")
	if err := os.WriteFile(filepath.Join(f.wt("parent"), "parent-amended.txt"), []byte("amended\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.wt("parent"), "add", "parent-amended.txt")
	gitRun(t, f.wt("parent"), "commit", "--amend", "--no-edit")
	newParent := gitOutput(t, f.repo, "rev-parse", "parent")
	if oldParent == newParent {
		t.Fatal("parent amend did not rewrite the commit")
	}
	if err := archiveExternal(f.feature, "child"); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runSync(t, f.feature, "--local-only", "--no-fetch")
	if exit != 0 {
		t.Fatalf("archived sync failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	child := internal.GetBranch(stack, "child")
	if child.LastBaseSHA != newParent {
		t.Fatalf("child last_base_sha = %s, want amended parent %s", child.LastBaseSHA, newParent)
	}
	if internal.RunSilentDir(f.repo, "git", "merge-base", "--is-ancestor", newParent, child.GitBranch()) != nil {
		t.Fatal("rewritten archived child does not contain amended parent")
	}
	if got := gitOutput(t, f.repo, "symbolic-ref", "--short", "HEAD"); got != "master" {
		t.Fatalf("archived sync left computation checkout on %s", got)
	}

	if err := createWorktree(f.feature, "child", "parent", f.repo, false); err != nil {
		t.Fatalf("restore archived child: %v", err)
	}
	stack, err = internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	child = internal.GetBranch(stack, "child")
	if child.Archived || child.LastBaseSHA != newParent {
		t.Fatalf("restored child metadata = %+v, want active with cutoff %s", child, newParent)
	}
	stdout, stderr, exit = runSync(t, f.feature, "--local-only", "--no-fetch")
	if exit != 0 {
		t.Fatalf("materialized resync failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	stack, err = internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := internal.GetBranch(stack, "child").LastBaseSHA; got != newParent {
		t.Fatalf("materialized resync cutoff = %s, want %s", got, newParent)
	}
}

func TestSyncStaleCutoffRefusesBeforeAnyBranchMoves(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		f := newScopedFixture(t)
		stack, err := internal.LoadStack(f.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		stale := gitOutput(t, f.repo, "rev-parse", "master")
		for i := range stack.Branches {
			if stack.Branches[i].Name == "child" {
				stack.Branches[i].LastBaseSHA = stale
			}
		}
		if err := internal.SaveStack(f.featurePath, stack); err != nil {
			t.Fatal(err)
		}
		beforeRefs := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		beforeStack, err := os.ReadFile(internal.StackPath(f.featurePath))
		if err != nil {
			t.Fatal(err)
		}

		stdout, stderr, exit := runSync(t, f.feature, "--local-only", "--no-fetch")
		if exit == 0 || !strings.Contains(stdout+stderr, "strictly predates shared parent/child history") {
			t.Fatalf("stale cutoff was not refused: %d\n%s\n%s", exit, stdout, stderr)
		}
		if after := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); after != beforeRefs {
			t.Fatalf("refs moved before cutoff refusal:\n%s\n---\n%s", beforeRefs, after)
		}
		afterStack, err := os.ReadFile(internal.StackPath(f.featurePath))
		if err != nil || string(afterStack) != string(beforeStack) {
			t.Fatalf("metadata changed before cutoff refusal: %v", err)
		}
		_, guardErr := os.Lstat(internal.SyncRunGuardPath(f.featurePath))
		if internal.HasSyncRunState(f.featurePath) || guardErr == nil {
			t.Fatal("fresh cutoff refusal left sync recovery state")
		}
	})

	t.Run("checkout", func(t *testing.T) {
		dir := setupCheckoutSyncRepo(t)
		featurePath := setupFeaturePath(t, dir)
		rootBase := gitSHA(t, dir, "main")
		createStackBranch(t, dir, "parent", "main", "parent.txt", "parent\n")
		parentTip := gitSHA(t, dir, "parent")
		createStackBranch(t, dir, "child", "parent", "child.txt", "child\n")
		saveTestStack(t, featurePath, []internal.StackEntry{
			{Name: "parent", Base: "main", LastBaseSHA: rootBase},
			{Name: "child", Base: "parent", LastBaseSHA: rootBase},
		})
		beforeRefs := gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		opts := newModeOpts(dir, featurePath, internal.SyncRunPolicy{
			Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull,
			ScopeKind: internal.SyncScopeAll,
		})
		err := internal.RunCheckoutSync(opts)
		if err == nil || !strings.Contains(err.Error(), "strictly predates shared parent/child history") {
			t.Fatalf("stale checkout cutoff was not refused: %v (parent=%s)", err, parentTip)
		}
		if after := gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); after != beforeRefs {
			t.Fatalf("checkout refs moved before refusal:\n%s\n---\n%s", beforeRefs, after)
		}
		if internal.HasCheckoutTransaction(featurePath) || internal.HasCheckoutLock(featurePath) {
			t.Fatal("checkout cutoff refusal left transaction or lock state")
		}
	})
}

func TestSyncMissingCutoffFreezesAncestorParentBeforeEarlierRewrite(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		f := newScopedFixture(t)
		stack, err := internal.LoadStack(f.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		oldParent := gitOutput(t, f.repo, "rev-parse", "parent")
		for i := range stack.Branches {
			if stack.Branches[i].Name == "child" {
				stack.Branches[i].LastBaseSHA = ""
			}
		}
		if err := internal.SaveStack(f.featurePath, stack); err != nil {
			t.Fatal(err)
		}
		f.advanceRoot(t)

		stdout, stderr, exit := runSync(t, f.feature, "--full", "--no-fetch")
		if exit != 0 {
			t.Fatalf("missing-cutoff external sync failed: %d\n%s\n%s", exit, stdout, stderr)
		}
		newParent := gitOutput(t, f.repo, "rev-parse", "parent")
		if newParent == oldParent {
			t.Fatal("fixture did not rewrite the parent before the child row")
		}
		if internal.RunSilentDir(f.repo, "git", "merge-base", "--is-ancestor", newParent, "child") != nil {
			t.Fatal("child does not contain rewritten parent")
		}
		stack, err = internal.LoadStack(f.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		if got := internal.GetBranch(stack, "child").LastBaseSHA; got != newParent {
			t.Fatalf("child last_base_sha = %s, want %s", got, newParent)
		}
	})

	t.Run("checkout", func(t *testing.T) {
		dir := setupCheckoutSyncRepo(t)
		featurePath := setupFeaturePath(t, dir)
		rootBase := gitSHA(t, dir, "main")
		createStackBranch(t, dir, "root", "main", "root.txt", "root\n")
		rootTip := gitSHA(t, dir, "root")
		createStackBranch(t, dir, "parent", "root", "parent.txt", "parent\n")
		oldParent := gitSHA(t, dir, "parent")
		createStackBranch(t, dir, "child", "parent", "child.txt", "child\n")
		if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{
			{Name: "root", Base: "main", LastBaseSHA: rootBase},
			{Name: "parent", Base: "root", LastBaseSHA: rootTip},
			{Name: "child", Base: "parent"},
		}}); err != nil {
			t.Fatal(err)
		}
		gitRunCS(t, dir, "checkout", "root")
		writeFileCS(t, dir, "root-v2.txt", "root-v2\n")
		gitRunCS(t, dir, "add", "root-v2.txt")
		gitRunCS(t, dir, "commit", "-m", "root v2")
		gitRunCS(t, dir, "checkout", "main")

		opts := newModeOpts(dir, featurePath, internal.SyncRunPolicy{
			Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull,
			ScopeKind: internal.SyncScopeAll,
		})
		if err := internal.RunCheckoutSync(opts); err != nil {
			t.Fatalf("missing-cutoff checkout sync failed: %v", err)
		}
		newParent := gitSHA(t, dir, "parent")
		if newParent == oldParent {
			t.Fatal("fixture did not rewrite checkout parent")
		}
		if internal.RunSilentDir(dir, "git", "merge-base", "--is-ancestor", newParent, "child") != nil {
			t.Fatal("checkout child does not contain rewritten parent")
		}
		stack, err := internal.LoadStack(featurePath)
		if err != nil {
			t.Fatal(err)
		}
		if got := internal.GetBranch(stack, "child").LastBaseSHA; got != newParent {
			t.Fatalf("checkout child last_base_sha = %s, want %s", got, newParent)
		}
	})
}

func TestSyncTransactionSixBranchConflictAfterAnchorRestoresEverything(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		repo := setupGitRepo(t, "master")
		withWorkspaceEnv(t, repo)
		writeAndCommit(t, repo, "conflict.txt", "base\n", "conflict base")
		feature := "six-branch"
		if err := createWorktree(feature, "pr1", "master", repo, false); err != nil {
			t.Fatal(err)
		}
		writeAndCommit(t, internal.WorktreePath(feature, "pr1"), "pr1.txt", "pr1\n", "pr1")
		pr1Base := gitOutput(t, repo, "rev-parse", "master")
		pr1Tip := gitOutput(t, repo, "rev-parse", "pr1")

		if err := createWorktree(feature, "pr2", "pr1", repo, false); err != nil {
			t.Fatal(err)
		}
		writeAndCommit(t, internal.WorktreePath(feature, "pr2"), "conflict.txt", "pr2\n", "pr2 conflict")
		if err := createWorktree(feature, "pr3", "pr1", repo, false); err != nil {
			t.Fatal(err)
		}
		writeAndCommit(t, internal.WorktreePath(feature, "pr3"), "pr3.txt", "pr3\n", "pr3")
		pr3Tip := gitOutput(t, repo, "rev-parse", "pr3")
		if err := createWorktree(feature, "pr4", "pr1", repo, false); err != nil {
			t.Fatal(err)
		}
		writeAndCommit(t, internal.WorktreePath(feature, "pr4"), "pr4.txt", "pr4\n", "pr4")
		if err := createWorktree(feature, "pr5", "pr3", repo, false); err != nil {
			t.Fatal(err)
		}
		writeAndCommit(t, internal.WorktreePath(feature, "pr5"), "pr5.txt", "pr5\n", "pr5")
		pr5Tip := gitOutput(t, repo, "rev-parse", "pr5")
		if err := createWorktree(feature, "pr6", "pr5", repo, false); err != nil {
			t.Fatal(err)
		}
		writeAndCommit(t, internal.WorktreePath(feature, "pr6"), "pr6.txt", "pr6\n", "pr6")

		writeAndCommit(t, repo, "conflict.txt", "master\n", "master conflict")
		featurePath := internal.FeaturePath(feature)
		stack, err := internal.LoadStack(featurePath)
		if err != nil {
			t.Fatal(err)
		}
		for i := range stack.Branches {
			switch stack.Branches[i].Name {
			case "pr1":
				stack.Branches[i].LastBaseSHA = pr1Base
			case "pr2", "pr3", "pr4":
				stack.Branches[i].LastBaseSHA = pr1Tip
			case "pr5":
				stack.Branches[i].LastBaseSHA = pr3Tip
			case "pr6":
				stack.Branches[i].LastBaseSHA = pr5Tip
			}
		}
		if err := internal.SaveStack(featurePath, stack); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(internal.StackPath(featurePath))
		if err != nil {
			t.Fatal(err)
		}
		beforeStack := append([]byte("# exact six-branch external preimage\n"), raw...)
		if err := os.WriteFile(internal.StackPath(featurePath), beforeStack, 0o600); err != nil {
			t.Fatal(err)
		}
		beforeRefs := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		beforePr1 := gitOutput(t, repo, "rev-parse", "pr1")

		stdout, stderr, exit := runSync(t, feature, "--no-fetch")
		if exit == 0 || !strings.Contains(stdout+stderr, "pr2") {
			t.Fatalf("six-branch conflict did not stop after the anchor: %d\n%s\n%s", exit, stdout, stderr)
		}
		if got := gitOutput(t, repo, "rev-parse", "pr1"); got == beforePr1 {
			t.Fatal("fixture did not move the pr1 anchor before the pr2 conflict")
		}
		f := &scopedFixture{repo: repo, feature: feature, featurePath: featurePath}
		f.detachGuard(t)
		stdout, stderr, exit = runSync(t, feature, "--abort")
		if exit != 0 {
			t.Fatalf("six-branch external abort: %d\n%s\n%s", exit, stdout, stderr)
		}
		if after := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); after != beforeRefs {
			t.Fatalf("six-branch external refs differ after abort:\n--- before ---\n%s\n--- after ---\n%s", beforeRefs, after)
		}
		afterStack, err := os.ReadFile(internal.StackPath(featurePath))
		if err != nil || string(afterStack) != string(beforeStack) {
			t.Fatalf("six-branch external metadata was not byte-exact: %v", err)
		}
	})

	t.Run("checkout", func(t *testing.T) {
		dir := setupCheckoutSyncRepo(t)
		featurePath := setupFeaturePath(t, dir)
		writeAndCommit(t, dir, "conflict.txt", "base\n", "conflict base")
		pr1Base := gitSHA(t, dir, "main")
		createStackBranch(t, dir, "pr1", "main", "pr1.txt", "pr1\n")
		pr1Tip := gitSHA(t, dir, "pr1")
		createStackBranch(t, dir, "pr2", "pr1", "pr2.txt", "pr2\n")
		gitRunCS(t, dir, "checkout", "pr2")
		writeAndCommit(t, dir, "conflict.txt", "pr2\n", "pr2 conflict")
		createStackBranch(t, dir, "pr3", "pr1", "pr3.txt", "pr3\n")
		pr3Tip := gitSHA(t, dir, "pr3")
		createStackBranch(t, dir, "pr4", "pr1", "pr4.txt", "pr4\n")
		createStackBranch(t, dir, "pr5", "pr3", "pr5.txt", "pr5\n")
		pr5Tip := gitSHA(t, dir, "pr5")
		createStackBranch(t, dir, "pr6", "pr5", "pr6.txt", "pr6\n")
		gitRunCS(t, dir, "checkout", "main")
		writeAndCommit(t, dir, "conflict.txt", "master\n", "master conflict")
		saveTestStack(t, featurePath, []internal.StackEntry{
			{Name: "pr1", Base: "main", LastBaseSHA: pr1Base},
			{Name: "pr2", Base: "pr1", LastBaseSHA: pr1Tip},
			{Name: "pr3", Base: "pr1", LastBaseSHA: pr1Tip},
			{Name: "pr4", Base: "pr1", LastBaseSHA: pr1Tip},
			{Name: "pr5", Base: "pr3", LastBaseSHA: pr3Tip},
			{Name: "pr6", Base: "pr5", LastBaseSHA: pr5Tip},
		})
		raw, err := os.ReadFile(internal.StackPath(featurePath))
		if err != nil {
			t.Fatal(err)
		}
		beforeStack := append([]byte("# exact six-branch checkout preimage\n"), raw...)
		if err := os.WriteFile(internal.StackPath(featurePath), beforeStack, 0o600); err != nil {
			t.Fatal(err)
		}
		beforeRefs := gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		beforePr1 := gitSHA(t, dir, "pr1")
		opts := newModeOpts(dir, featurePath, internal.SyncRunPolicy{
			Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull,
			ScopeKind: internal.SyncScopeAll,
		})
		err = internal.RunCheckoutSync(opts)
		if err == nil {
			t.Fatalf("six-branch checkout conflict did not stop after the anchor: %v", err)
		}
		tx, loadErr := internal.LoadCheckoutTransaction(featurePath)
		if loadErr != nil || tx.CurrentIndex >= len(tx.Plan) || tx.Plan[tx.CurrentIndex].Name != "pr2" {
			t.Fatalf("checkout conflict stopped at the wrong row: tx=%+v err=%v", tx, loadErr)
		}
		if got := gitSHA(t, dir, "pr1"); got == beforePr1 {
			t.Fatal("checkout fixture did not move the pr1 anchor before the pr2 conflict")
		}
		if err := internal.AbortCheckoutSync(opts); err != nil {
			t.Fatalf("six-branch checkout abort: %v", err)
		}
		if after := gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); after != beforeRefs {
			t.Fatalf("six-branch checkout refs differ after abort:\n--- before ---\n%s\n--- after ---\n%s", beforeRefs, after)
		}
		afterStack, err := os.ReadFile(internal.StackPath(featurePath))
		if err != nil || string(afterStack) != string(beforeStack) {
			t.Fatalf("six-branch checkout metadata was not byte-exact: %v", err)
		}
	})
}

func testSyncTransactionArchivedEntryRollsBackAfterLoad(t *testing.T, f *scopedFixture, stack internal.Stack) {
	for i := range stack.Branches {
		if stack.Branches[i].Name == "child" {
			stack.Branches[i].Archived = true
		}
	}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}
	beforeRefs := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	beforeStack, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	internal.SyncTransactionStepHook = func(step string) error {
		if step == "action-observed:rebase:child" {
			reached = true
			return errors.New("stop after archived row")
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
	stdout, stderr, exit := runSync(t, f.feature, "--no-fetch")
	if exit == 0 || !reached {
		t.Fatalf("archived row did not execute: %d\n%s\n%s", exit, stdout, stderr)
	}
	internal.SyncTransactionStepHook = nil
	f.detachGuard(t)
	stdout, stderr, exit = runSync(t, f.feature, "--abort")
	if exit != 0 {
		t.Fatalf("abort archived row: %d\n%s\n%s", exit, stdout, stderr)
	}
	if after := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); after != beforeRefs {
		t.Fatal("archived and materialized refs were not restored")
	}
	afterStack, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil || string(afterStack) != string(beforeStack) {
		t.Fatalf("archived metadata was not restored exactly: %v", err)
	}
	if got := gitOutput(t, f.repo, "symbolic-ref", "--short", "HEAD"); got != "master" {
		t.Fatalf("archived computation context remained on %s", got)
	}
}
