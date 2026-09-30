package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func TestSyncCutoffSnapshotReadyInterruptionCanContinue(t *testing.T) {
	for name, args := range map[string][]string{
		"no-fetch":      {"--no-fetch"},
		"fetch":         {"--fetch"},
		"default":       {},
		"guarded-fetch": {"--fetch", "--max-replay-total", "100"},
	} {
		t.Run(name, func(t *testing.T) {
			checkSyncCutoffSnapshotReadyInterruption(t, args)
		})
	}
}

func TestSyncCutoffPreFreezeFetchInterruptionCanContinue(t *testing.T) {
	for name, args := range map[string][]string{
		"fetch":   {"--fetch"},
		"default": {},
	} {
		t.Run(name, func(t *testing.T) {
			f := newScopedFixture(t)
			f.advanceRoot(t)
			previousHook := internal.SyncStepHook
			reached := false
			internal.SyncStepHook = func(stage internal.SyncRunStage, index int) error {
				if stage == internal.SyncStageInitializing && index == 2 {
					reached = true
					return errors.New("interrupted before fetch and cutoff freeze")
				}
				return nil
			}
			t.Cleanup(func() { internal.SyncStepHook = previousHook })
			_, stderr, exit := runSync(t, append([]string{f.feature}, args...)...)
			if exit == 0 || !reached {
				t.Fatalf("did not reach pre-fetch window: exit=%d stderr=%s", exit, stderr)
			}
			internal.SyncStepHook = previousHook
			f.detachGuard(t)
			stdout, stderr, exit := runSync(t, f.feature, "--continue")
			if exit != 0 {
				t.Fatalf("safe pre-mutation interruption cannot continue: exit=%d\n%s\n%s", exit, stdout, stderr)
			}
			gitRun(t, f.repo, "merge-base", "--is-ancestor", "root", "parent")
			gitRun(t, f.repo, "merge-base", "--is-ancestor", "parent", "child")
			f.stateFilesGone(t)
		})
	}
}

func checkSyncCutoffSnapshotReadyInterruption(t *testing.T, args []string) {
	t.Helper()
	f := newScopedFixture(t)
	f.advanceRoot(t)
	reached := false
	internal.SyncTransactionStepHook = func(step string) error {
		if step == "snapshot-ready" {
			reached = true
			return errors.New("interrupted before cutoff freeze")
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
	_, stderr, exit := runSync(t, append([]string{f.feature}, args...)...)
	if exit == 0 || !reached {
		t.Fatalf("did not reach pre-freeze window: exit=%d stderr=%s", exit, stderr)
	}
	internal.SyncTransactionStepHook = nil
	f.detachGuard(t)
	stdout, stderr, exit := runSync(t, f.feature, "--continue")
	if exit != 0 {
		t.Fatalf("safe pre-mutation interruption cannot continue: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	gitRun(t, f.repo, "merge-base", "--is-ancestor", "root", "parent")
	gitRun(t, f.repo, "merge-base", "--is-ancestor", "parent", "child")
	f.stateFilesGone(t)
}

func TestSyncArchivedParentRunsBeforeMaterializedChild(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	if err := archiveExternal(f.feature, "parent"); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runSync(t, f.feature, "--full", "--no-fetch")
	if exit != 0 {
		t.Fatalf("dependency-ordered mixed-materialization sync failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	parentAt := strings.Index(stdout, "parent (archived)")
	childAt := strings.Index(stdout, "child (active)")
	if parentAt < 0 || childAt < 0 || parentAt > childAt {
		t.Fatalf("execution order is not parent-before-child:\n%s", stdout)
	}
	if internal.RunSilentDir(f.repo, "git", "merge-base", "--is-ancestor", "parent", "child") != nil {
		t.Fatal("materialized child does not contain rewritten archived parent")
	}
}

func TestSyncExternalMetadataUsesExecutedDestinationSHA(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	oldMaster := gitOutput(t, repo, "rev-parse", "master")
	if err := createWorktree("customer", "pr1", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, internal.WorktreePath("customer", "pr1"), "pr1.txt", "pr1\n", "pr1")
	writeAndCommit(t, repo, "master-v2.txt", "v2\n", "master v2")
	newMaster := gitOutput(t, repo, "rev-parse", "master")
	gitRun(t, repo, "push", "origin", "master")
	gitRun(t, repo, "fetch", "origin")

	internal.SyncTransactionStepHook = func(step string) error {
		if step == "action-observed:rebase:pr1" {
			gitRun(t, repo, "update-ref", "refs/remotes/origin/master", oldMaster)
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
	stdout, stderr, exit := runSync(t, "customer", "--full", "--no-fetch")
	if exit != 0 {
		t.Fatalf("sync failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	stack, err := internal.LoadStack(internal.FeaturePath("customer"))
	if err != nil {
		t.Fatal(err)
	}
	if got := internal.GetBranch(stack, "pr1").LastBaseSHA; got != newMaster {
		t.Fatalf("last_base_sha = %s, want immutable executed destination %s", got, newMaster)
	}
}

func TestSyncCollateralUsesItsOwnLiteralParentDestination(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		t.Run(map[bool]string{false: "unguarded", true: "guarded"}[guarded], func(t *testing.T) {
			repo := setupGitRepo(t, "master")
			withWorkspaceEnv(t, repo)
			root := gitOutput(t, repo, "rev-parse", "master")
			if err := createWorktree("customer", "a", "master", repo, false); err != nil {
				t.Fatal(err)
			}
			aPath := internal.WorktreePath("customer", "a")
			writeAndCommit(t, aPath, "literal.txt", "literal\n", "literal base")
			oldLiteral := gitOutput(t, repo, "rev-parse", "a")
			gitRun(t, repo, "branch", "base2", oldLiteral)
			writeAndCommit(t, aPath, "z.txt", "z\n", "z")
			oldZ := gitOutput(t, repo, "rev-parse", "a")
			gitRun(t, repo, "branch", "feat-z", oldZ)
			stack, err := internal.LoadStack(internal.FeaturePath("customer"))
			if err != nil {
				t.Fatal(err)
			}
			stack.Branches = append(stack.Branches, internal.StackEntry{
				Name: "z", Branch: "feat-z", Base: "refs/heads/base2",
				LastBaseSHA: oldLiteral, Archived: true,
			})
			if err := internal.SaveStack(internal.FeaturePath("customer"), stack); err != nil {
				t.Fatal(err)
			}
			writeAndCommit(t, aPath, "a.txt", "a\n", "a")
			writeAndCommit(t, repo, "upstream.txt", "upstream\n", "upstream")
			gitRun(t, repo, "push", "origin", "master")

			args := []string{"customer", "--full", "--no-fetch"}
			if guarded {
				args = append(args, "--max-replay-total", "100")
			}
			stdout, stderr, exit := runSync(t, args...)
			if exit != 0 {
				t.Fatalf("first sync failed: %d\n%s\n%s", exit, stdout, stderr)
			}
			newLiteral := gitOutput(t, repo, "rev-parse", "base2")
			if newLiteral == oldLiteral {
				t.Fatal("literal parent was not moved by --update-refs")
			}
			if got := gitOutput(t, repo, "rev-parse", "feat-z"); got == oldZ {
				t.Fatal("collateral branch was not moved by --update-refs")
			}
			stack, err = internal.LoadStack(internal.FeaturePath("customer"))
			if err != nil {
				t.Fatal(err)
			}
			if got := internal.GetBranch(stack, "z").LastBaseSHA; got != newLiteral {
				t.Fatalf("collateral cutoff = %s, want literal parent destination %s", got, newLiteral)
			}
			stdout, stderr, exit = runSync(t, args...)
			if exit != 0 {
				t.Fatalf("second sync failed: %d\n%s\n%s", exit, stdout, stderr)
			}
			if got := gitOutput(t, repo, "rev-parse", "master"); got == root {
				t.Fatal("upstream fixture did not advance")
			}
		})
	}
}

func TestSyncCollateralCompletionSurvivesPostProgressCrash(t *testing.T) {
	for _, recovery := range []string{"continue", "abort"} {
		t.Run(recovery, func(t *testing.T) {
			checkSyncCollateralCompletionSurvivesPostProgressCrash(t, recovery)
		})
	}
}

func checkSyncCollateralCompletionSurvivesPostProgressCrash(t *testing.T, recovery string) {
	t.Helper()
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := gitOutput(t, repo, "rev-parse", "master")
	if err := createWorktree("customer", "a", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	aPath := internal.WorktreePath("customer", "a")
	writeAndCommit(t, aPath, "literal.txt", "literal\n", "literal base")
	oldLiteral := gitOutput(t, repo, "rev-parse", "a")
	gitRun(t, repo, "branch", "base2", oldLiteral)
	writeAndCommit(t, aPath, "z.txt", "z\n", "z")
	gitRun(t, repo, "branch", "feat-z", gitOutput(t, repo, "rev-parse", "a"))
	stack, err := internal.LoadStack(internal.FeaturePath("customer"))
	if err != nil {
		t.Fatal(err)
	}
	stack.Branches = append(stack.Branches, internal.StackEntry{
		Name: "z", Branch: "feat-z", Base: "refs/heads/base2",
		LastBaseSHA: oldLiteral, Archived: true,
	})
	if err := internal.SaveStack(internal.FeaturePath("customer"), stack); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, aPath, "a.txt", "a\n", "a")
	writeAndCommit(t, repo, "upstream.txt", "upstream\n", "upstream")
	gitRun(t, repo, "push", "origin", "master")
	originalRefs := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	originalStack, err := os.ReadFile(internal.StackPath(internal.FeaturePath("customer")))
	if err != nil {
		t.Fatal(err)
	}

	reached := false
	syncProgressPersistedHook = func(primary string) error {
		if primary == "a" {
			reached = true
			return errors.New("interrupt after durable affected progress")
		}
		return nil
	}
	t.Cleanup(func() { syncProgressPersistedHook = nil })
	_, _, exit := runSync(t, "customer", "--full", "--no-fetch")
	if exit == 0 || !reached {
		t.Fatalf("did not reach post-progress crash seam: exit=%d reached=%v", exit, reached)
	}
	syncProgressPersistedHook = nil

	featurePath := internal.FeaturePath("customer")
	payload, err := internal.LoadSyncRunState(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Transaction.Actions) != 1 ||
		!slices.Equal(payload.Completed, []string{"a", "z"}) ||
		len(payload.Pending) != 0 || payload.FailedBranch != "" {
		t.Fatalf("post-progress state lost affected completion: %+v", payload)
	}
	newLiteral := gitOutput(t, repo, "rev-parse", "base2")
	zAfter := gitOutput(t, repo, "rev-parse", "feat-z")
	refsBeforeContinue := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	stack, err = internal.LoadStack(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := internal.GetBranch(stack, "z").LastBaseSHA; got != newLiteral {
		t.Fatalf("durable collateral cutoff = %s, want %s", got, newLiteral)
	}
	if recovery == "abort" {
		detachSyncCutoffGuard(t, featurePath)
		stdout, stderr, exit := runSync(t, "customer", "--abort")
		if exit != 0 {
			t.Fatalf("abort after post-progress crash failed: %d\n%s\n%s", exit, stdout, stderr)
		}
		if got := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != originalRefs {
			t.Fatalf("abort did not restore refs:\n%s\n---\n%s", originalRefs, got)
		}
		restoredStack, readErr := os.ReadFile(internal.StackPath(featurePath))
		if readErr != nil || !slices.Equal(restoredStack, originalStack) {
			t.Fatalf("abort did not restore exact stack metadata: err=%v", readErr)
		}
		assertNoExternalSyncState(t, featurePath)
		return
	}

	rebaseRestarted := false
	internal.SyncTransactionStepHook = func(step string) error {
		if strings.HasPrefix(step, "action-intent:rebase:") {
			rebaseRestarted = true
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
	detachSyncCutoffGuard(t, featurePath)
	stdout, stderr, exit := runSync(t, "customer", "--continue")
	if exit != 0 {
		t.Fatalf("continue after post-progress crash failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	if rebaseRestarted {
		t.Fatal("continue started a duplicate rebase for durably completed collateral")
	}
	if got := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsBeforeContinue {
		t.Fatalf("continue changed refs:\n%s\n---\n%s", refsBeforeContinue, got)
	}
	if got := gitOutput(t, repo, "rev-parse", "feat-z"); got != zAfter {
		t.Fatalf("continue rewrote collateral again: %s, want %s", got, zAfter)
	}
	if got := gitOutput(t, repo, "log", "--format=%s", newLiteral+"..feat-z"); got != "z" {
		t.Fatalf("collateral range after recovery = %q, want exactly z", got)
	}
	for _, path := range []string{
		internal.SyncRunStatePath(featurePath),
		internal.SyncStatePath(featurePath),
		internal.SyncRunGuardPath(featurePath),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("continue left recovery artifact %s: %v", path, statErr)
		}
	}
	if gitOutput(t, repo, "rev-parse", "master") == root {
		t.Fatal("fixture upstream did not advance")
	}
}

func TestSyncNoUpdateRefsOverridesConfiguredUpdateRefs(t *testing.T) {
	t.Run("external-scoped-repo-local", func(t *testing.T) {
		checkExternalUpdateRefsConfigIsolation(t, false, false, false)
	})
	t.Run("external-archived-inherited-guarded", func(t *testing.T) {
		checkExternalUpdateRefsConfigIsolation(t, true, true, true)
	})
	t.Run("external-local-only-inherited-guarded", func(t *testing.T) {
		repo := setupGitRepo(t, "master")
		withWorkspaceEnv(t, repo)
		if err := createWorktree("customer", "parent", "master", repo, false); err != nil {
			t.Fatal(err)
		}
		parentPath := internal.WorktreePath("customer", "parent")
		writeAndCommit(t, parentPath, "parent.txt", "parent\n", "parent")
		if err := createWorktree("customer", "child", "parent", repo, false); err != nil {
			t.Fatal(err)
		}
		childPath := internal.WorktreePath("customer", "child")
		writeAndCommit(t, childPath, "z.txt", "z\n", "z")
		unselected := gitOutput(t, repo, "rev-parse", "child")
		gitRun(t, repo, "branch", "unselected", unselected)
		writeAndCommit(t, childPath, "child.txt", "child\n", "child")
		writeAndCommit(t, parentPath, "parent-next.txt", "next\n", "parent next")
		t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
		gitRun(t, repo, "config", "--global", "rebase.updateRefs", "true")

		planOut, planErr, planExit := runSyncExecute(t,
			"customer", "--plan", "--json", "--local-only", "--no-fetch", "--max-replay-total", "100")
		if planExit != 0 {
			t.Fatalf("local-only plan failed: %d\n%s\n%s", planExit, planOut, planErr)
		}
		assertPlanDisablesUpdateRefs(t, planOut, "child")
		stdout, stderr, exit := runSync(t,
			"customer", "--local-only", "--no-fetch", "--max-replay-total", "100")
		if exit != 0 {
			t.Fatalf("local-only sync failed: %d\n%s\n%s", exit, stdout, stderr)
		}
		if got := gitOutput(t, repo, "rev-parse", "unselected"); got != unselected {
			t.Fatalf("local-only sync moved unselected ref: %s, want %s", got, unselected)
		}
		assertNoExternalSyncState(t, internal.FeaturePath("customer"))
	})
	for _, inherited := range []bool{false, true} {
		name := map[bool]string{false: "checkout-repo-local", true: "checkout-inherited-guarded"}[inherited]
		t.Run(name, func(t *testing.T) {
			checkCheckoutUpdateRefsConfigIsolation(t, inherited)
		})
	}
}

func TestSyncGit237StrictParserCompatibility(t *testing.T) {
	installStrictGit237Parser(t)
	t.Run("external-scoped", func(t *testing.T) {
		checkExternalUpdateRefsConfigIsolation(t, false, false, true)
	})
	t.Run("external-archived", func(t *testing.T) {
		checkExternalUpdateRefsConfigIsolation(t, true, true, true)
	})
	t.Run("checkout", func(t *testing.T) {
		checkCheckoutUpdateRefsConfigIsolation(t, true)
	})
	t.Run("positive-update-refs-refuses-before-mutation", func(t *testing.T) {
		repo := setupGitRepo(t, "master")
		withWorkspaceEnv(t, repo)
		if err := createWorktree("customer", "primary", "master", repo, false); err != nil {
			t.Fatal(err)
		}
		primaryPath := internal.WorktreePath("customer", "primary")
		writeAndCommit(t, primaryPath, "primary.txt", "primary\n", "primary")
		writeAndCommit(t, repo, "upstream.txt", "upstream\n", "upstream")
		gitRun(t, repo, "push", "origin", "master")
		refs := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		_, stderr, exit := runSync(t, "customer", "--full", "--no-fetch")
		if exit == 0 || !strings.Contains(stderr, "planned argv carries --update-refs") ||
			!strings.Contains(stderr, "Git 2.38 or newer") {
			t.Fatalf("Git 2.37 positive route did not refuse capability safely: exit=%d stderr=%s", exit, stderr)
		}
		if got := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refs {
			t.Fatalf("capability refusal moved refs:\n%s\n---\n%s", refs, got)
		}
		assertNoExternalSyncState(t, internal.FeaturePath("customer"))
	})
}

func TestSyncContinueCapabilityIncludesFailedEntry(t *testing.T) {
	for _, route := range []string{"legacy", "full"} {
		for _, guarded := range []bool{false, true} {
			t.Run(route+"/"+map[bool]string{false: "unguarded", true: "guarded"}[guarded], func(t *testing.T) {
				repo := setupGitRepo(t, "master")
				withWorkspaceEnv(t, repo)
				if err := createWorktree("customer", "primary", "master", repo, false); err != nil {
					t.Fatal(err)
				}
				writeAndCommit(t, internal.WorktreePath("customer", "primary"), "child.txt", "child\n", "child")
				writeAndCommit(t, repo, "upstream.txt", "upstream\n", "upstream")
				gitRun(t, repo, "push", "origin", "master")
				hook := filepath.Join(repo, ".git", "hooks", "pre-rebase")
				if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				args := []string{"customer"}
				if route == "full" {
					args = append(args, "--full", "--no-fetch")
				}
				if _, _, exit := runSync(t, args...); exit == 0 {
					t.Fatal("rejecting pre-rebase hook did not interrupt sync")
				}
				if err := os.Remove(hook); err != nil {
					t.Fatal(err)
				}
				featurePath := internal.FeaturePath("customer")
				before, err := internal.LoadSyncRunState(featurePath)
				if err != nil {
					t.Fatal(err)
				}
				if before.FailedBranch != "primary" || len(before.Pending) != 0 ||
					len(before.Completed) != 0 || len(before.Transaction.Actions) != 1 {
					t.Fatalf("fixture lacks a failed row excluded from pending: %+v", before)
				}
				refs := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
				metadata, err := os.ReadFile(internal.StackPath(featurePath))
				if err != nil {
					t.Fatal(err)
				}
				detachSyncCutoffGuard(t, featurePath)
				installStrictGit237Parser(t)
				previewArgs := []string{"customer", "--continue", "--plan", "--json"}
				retryArgs := []string{"customer", "--continue"}
				if guarded {
					previewArgs = append(previewArgs, "--max-replay-total", "100")
					retryArgs = append(retryArgs, "--max-replay-total", "100")
				}
				stdout, stderr, exit := runSyncExecute(t, previewArgs...)
				if exit != 0 || planDoc(t, stdout)["runnable"] != false {
					t.Fatalf("failed-entry preview must refuse old Git: %d\n%s\n%s", exit, stdout, stderr)
				}
				_, stderr, exit = runSync(t, retryArgs...)
				if exit == 0 || !strings.Contains(stderr, "Git 2.38 or newer") ||
					strings.Contains(stderr, "unknown option") {
					t.Fatalf("retry reached unsupported rebase instead of capability refusal: %d\n%s", exit, stderr)
				}
				after, err := internal.LoadSyncRunState(featurePath)
				if err != nil {
					t.Fatal(err)
				}
				if len(after.Transaction.Actions) != len(before.Transaction.Actions) ||
					after.FailedBranch != before.FailedBranch ||
					!slices.Equal(after.Completed, before.Completed) ||
					!slices.Equal(after.Pending, before.Pending) {
					t.Fatalf("capability refusal changed recovery work or appended an action: %+v", after)
				}
				if got := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refs {
					t.Fatalf("capability refusal changed refs:\n%s\n---\n%s", refs, got)
				}
				afterMetadata, err := os.ReadFile(internal.StackPath(featurePath))
				if err != nil || !slices.Equal(afterMetadata, metadata) {
					t.Fatalf("capability refusal changed stack metadata: %v", err)
				}
			})
		}
	}
}

func installStrictGit237Parser(t *testing.T) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--version" ] || [ "$1" = "version" ]; then
  echo "git version 2.37.0"
  exit 0
fi
for arg in "$@"; do
  if [ "$arg" = "--update-refs" ] || [ "$arg" = "--no-update-refs" ]; then
    echo "error: unknown option $arg" >&2
    exit 129
  fi
done
exec '` + strings.ReplaceAll(realGit, "'", "'\\''") + `' "$@"
`
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func checkExternalUpdateRefsConfigIsolation(t *testing.T, archived, inherited, guarded bool) {
	t.Helper()
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := gitOutput(t, repo, "rev-parse", "master")
	if err := createWorktree("customer", "primary", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	primaryPath := internal.WorktreePath("customer", "primary")
	writeAndCommit(t, primaryPath, "z.txt", "z\n", "z")
	unselected := gitOutput(t, repo, "rev-parse", "primary")
	gitRun(t, repo, "branch", "unselected", unselected)
	writeAndCommit(t, primaryPath, "primary.txt", "primary\n", "primary")
	primaryBefore := gitOutput(t, repo, "rev-parse", "primary")
	writeAndCommit(t, repo, "upstream.txt", "upstream\n", "upstream")
	gitRun(t, repo, "push", "origin", "master")
	if inherited {
		t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
		gitRun(t, repo, "config", "--global", "rebase.updateRefs", "true")
	} else {
		gitRun(t, repo, "config", "rebase.updateRefs", "true")
	}
	if archived {
		if err := archiveExternal("customer", "primary"); err != nil {
			t.Fatal(err)
		}
	}
	planArgs := []string{"customer", "--plan", "--json", "--no-fetch", "--max-replay-total", "100"}
	execArgs := []string{"customer", "--no-fetch"}
	if archived {
		planArgs = append(planArgs, "--full")
		execArgs = append(execArgs, "--full")
	} else {
		planArgs = append(planArgs, "--only", "primary")
		execArgs = append(execArgs, "--only", "primary")
	}
	if guarded {
		execArgs = append(execArgs, "--max-replay-total", "100")
	}
	planOut, planErr, planExit := runSyncExecute(t, planArgs...)
	if planExit != 0 {
		t.Fatalf("external plan failed: %d\n%s\n%s", planExit, planOut, planErr)
	}
	assertPlanDisablesUpdateRefs(t, planOut, "primary")
	stdout, stderr, exit := runSync(t, execArgs...)
	if exit != 0 {
		t.Fatalf("external sync failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	if got := gitOutput(t, repo, "rev-parse", "unselected"); got != unselected {
		t.Fatalf("external sync moved unselected ref: %s, want %s", got, unselected)
	}
	if got := gitOutput(t, repo, "rev-parse", "primary"); got == primaryBefore || got == root {
		t.Fatalf("primary did not perform intended replay: %s", got)
	}
	assertNoExternalSyncState(t, internal.FeaturePath("customer"))
}

func checkCheckoutUpdateRefsConfigIsolation(t *testing.T, inherited bool) {
	t.Helper()
	repo := setupCheckoutSyncRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".tws", "config.yaml"), []byte("workspace_mode: checkout\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := gitSHA(t, repo, "main")
	gitRunCS(t, repo, "checkout", "-b", "feat-primary")
	writeFileCS(t, repo, "z.txt", "z\n")
	gitRunCS(t, repo, "add", "z.txt")
	gitRunCS(t, repo, "commit", "-m", "z")
	unselected := gitSHA(t, repo, "feat-primary")
	gitRunCS(t, repo, "branch", "unselected", unselected)
	writeFileCS(t, repo, "primary.txt", "primary\n")
	gitRunCS(t, repo, "add", "primary.txt")
	gitRunCS(t, repo, "commit", "-m", "primary")
	gitRunCS(t, repo, "checkout", "main")
	writeFileCS(t, repo, "upstream.txt", "upstream\n")
	gitRunCS(t, repo, "add", "upstream.txt")
	gitRunCS(t, repo, "commit", "-m", "upstream")
	featurePath := setupFeaturePath(t, repo)
	saveTestStack(t, featurePath, []internal.StackEntry{{
		Name: "primary", Branch: "feat-primary", Base: "main", LastBaseSHA: root,
	}})
	withUnifiedWorkspaceEnv(t, repo)
	if inherited {
		t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
		gitRunCS(t, repo, "config", "--global", "rebase.updateRefs", "true")
	} else {
		gitRunCS(t, repo, "config", "rebase.updateRefs", "true")
	}

	planOut, planErr, planExit := runSyncExecute(t,
		"test-feature", "--plan", "--json", "--full", "--no-fetch", "--max-replay-total", "100")
	if planExit != 0 {
		t.Fatalf("checkout plan failed: %d\n%s\n%s", planExit, planOut, planErr)
	}
	assertPlanDisablesUpdateRefs(t, planOut, "primary")
	args := []string{"test-feature", "--full", "--no-fetch"}
	if inherited {
		args = append(args, "--max-replay-total", "100")
	}
	stdout, stderr, exit := runSync(t, args...)
	if exit != 0 {
		t.Fatalf("checkout sync failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	if got := gitSHA(t, repo, "unselected"); got != unselected {
		t.Fatalf("checkout sync moved unselected ref: %s, want %s", got, unselected)
	}
	if got := gitRunCS(t, repo, "branch", "--show-current"); got != "main" {
		t.Fatalf("checkout sync restored %q, want main", got)
	}
	if internal.HasCheckoutTransaction(featurePath) || internal.HasCheckoutLock(featurePath) {
		t.Fatal("checkout sync leaked recovery state")
	}
}

func assertPlanDisablesUpdateRefs(t *testing.T, stdout, entryName string) {
	t.Helper()
	doc := planDoc(t, stdout)
	found := false
	for _, raw := range doc["entries"].([]any) {
		row := raw.(map[string]any)
		if row["name"] != entryName {
			continue
		}
		found = true
		argv, _ := row["argv"].([]any)
		var tokens []string
		for _, token := range argv {
			tokens = append(tokens, token.(string))
		}
		hasOverride := false
		for i := 0; i+1 < len(tokens); i++ {
			if tokens[i] == "-c" && tokens[i+1] == "rebase.updateRefs=false" {
				hasOverride = true
			}
		}
		if !hasOverride || slices.Contains(tokens, "--no-update-refs") || slices.Contains(tokens, "--update-refs") {
			t.Fatalf("plan argv does not explicitly disable update-refs: %v", tokens)
		}
		if row["collateral_mechanism"] != "none" {
			t.Fatalf("collateral mechanism = %v, want none", row["collateral_mechanism"])
		}
		if refs, ok := row["collateral_refs"].([]any); !ok || len(refs) != 0 {
			t.Fatalf("collateral refs = %#v, want empty", row["collateral_refs"])
		}
	}
	if !found {
		t.Fatalf("plan omitted entry %s", entryName)
	}
	for _, raw := range doc["warnings"].([]any) {
		warning := raw.(map[string]any)
		if warning["kind"] == "collateral-update-refs-config" {
			t.Fatalf("plan retained obsolete config collateral warning: %#v", warning)
		}
	}
	fingerprint, _ := doc["approval"].(map[string]any)["fingerprint"].(string)
	if len(fingerprint) != 64 {
		t.Fatalf("plan fingerprint = %q", fingerprint)
	}
}

func assertNoExternalSyncState(t *testing.T, featurePath string) {
	t.Helper()
	for _, path := range []string{
		internal.SyncRunStatePath(featurePath),
		internal.SyncStatePath(featurePath),
		internal.SyncRunGuardPath(featurePath),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("sync leaked recovery artifact %s: %v", path, err)
		}
	}
}

func TestSyncCollateralTransitionThenHonorsConfiguredParentDestination(t *testing.T) {
	for _, parentKind := range []string{"remote", "oid", "tag"} {
		for _, guarded := range []bool{false, true} {
			t.Run(parentKind+"/"+map[bool]string{false: "unguarded", true: "guarded"}[guarded], func(t *testing.T) {
				repo := setupGitRepo(t, "master")
				withWorkspaceEnv(t, repo)
				root := gitOutput(t, repo, "rev-parse", "master")
				if err := createWorktree("customer", "primary", "master", repo, false); err != nil {
					t.Fatal(err)
				}
				primaryPath := internal.WorktreePath("customer", "primary")
				gitRun(t, primaryPath, "branch", "-m", "feat-a")
				stack, err := internal.LoadStack(internal.FeaturePath("customer"))
				if err != nil {
					t.Fatal(err)
				}
				for i := range stack.Branches {
					if stack.Branches[i].Name == "primary" {
						stack.Branches[i].Branch = "feat-a"
					}
				}
				writeAndCommit(t, primaryPath, "z.txt", "z\n", "z")
				oldZ := gitOutput(t, repo, "rev-parse", "feat-a")
				gitRun(t, repo, "branch", "feat-z", oldZ)

				parent := ""
				switch parentKind {
				case "remote":
					parent = "refs/remotes/origin/master"
				case "oid":
					parent = root
				case "tag":
					gitRun(t, repo, "tag", "base-tag", root)
					parent = "refs/tags/base-tag"
				}
				stack.Branches = append(stack.Branches, internal.StackEntry{
					Name: "collateral", Branch: "feat-z", Base: parent,
					LastBaseSHA: root, Archived: true,
				})
				if err := internal.SaveStack(internal.FeaturePath("customer"), stack); err != nil {
					t.Fatal(err)
				}
				writeAndCommit(t, primaryPath, "a.txt", "a\n", "a")
				writeAndCommit(t, repo, "upstream.txt", "upstream\n", "upstream")
				gitRun(t, repo, "push", "origin", "master")
				transition := gitOutput(t, repo, "rev-parse", "refs/remotes/origin/master")
				configuredDestination := root
				if parentKind == "remote" {
					configuredDestination = transition
				}

				args := []string{"customer", "--full", "--no-fetch"}
				if guarded {
					args = append(args, "--max-replay-total", "100")
				}
				stdout, stderr, exit := runSync(t, args...)
				if exit != 0 {
					t.Fatalf("first sync failed: %d\n%s\n%s", exit, stdout, stderr)
				}
				if got := gitOutput(t, repo, "rev-parse", "feat-z"); got == oldZ {
					t.Fatal("collateral branch was not moved by --update-refs")
				}
				stack, err = internal.LoadStack(internal.FeaturePath("customer"))
				if err != nil {
					t.Fatal(err)
				}
				if got := internal.GetBranch(stack, "collateral").LastBaseSHA; got != transition {
					t.Fatalf("collateral cutoff = %s, want proven %s transition %s", got, parentKind, transition)
				}
				if got := gitOutput(t, repo, "log", "--format=%s", transition+"..feat-z"); got != "z" {
					t.Fatalf("collateral unique range = %q, want only original child change z", got)
				}
				if guarded {
					zPath := internal.WorktreePath("customer", "collateral")
					gitRun(t, repo, "worktree", "add", zPath, "feat-z")
					stack, err = internal.LoadStack(internal.FeaturePath("customer"))
					if err != nil {
						t.Fatal(err)
					}
					for i := range stack.Branches {
						if stack.Branches[i].Name == "collateral" {
							stack.Branches[i].Archived = false
						}
					}
					if err := internal.SaveStack(internal.FeaturePath("customer"), stack); err != nil {
						t.Fatal(err)
					}
				}
				planArgs := []string{"customer", "--plan", "--json", "--no-fetch", "--max-replay-total", "100"}
				followArgs := []string{"customer", "--full", "--no-fetch"}
				if guarded {
					planArgs = append(planArgs, "--only", "collateral")
					followArgs = []string{"customer", "--only", "collateral", "--no-fetch", "--max-replay-total", "100"}
				} else {
					planArgs = append(planArgs, "--full")
				}
				planOut, planErr, planExit := runSyncExecute(t, planArgs...)
				if planExit != 0 {
					t.Fatalf("literal-destination plan failed: %d\n%s\n%s", planExit, planOut, planErr)
				}
				plan := planDoc(t, planOut)
				foundPlan := false
				for _, raw := range plan["entries"].([]any) {
					row := raw.(map[string]any)
					if row["name"] != "collateral" {
						continue
					}
					foundPlan = true
					if got := row["destination"].(map[string]any)["sha"]; got != configuredDestination {
						t.Fatalf("planned destination = %v, want configured %s parent %s", got, parentKind, configuredDestination)
					}
					cutoff := row["cutoff"].(map[string]any)
					if cutoff["effective_sha"] != transition {
						t.Fatalf("planned cutoff = %#v, want prior collateral transition %s", cutoff, transition)
					}
					if got := row["replay"].(map[string]any)["candidate_count"]; got != float64(1) {
						t.Fatalf("planned candidate count = %v, want one child change", got)
					}
					assertOntoArgv(t, row, configuredDestination, transition, configuredDestination)
				}
				if !foundPlan {
					t.Fatal("second-sync plan omitted collateral row")
				}
				fingerprint, _ := plan["approval"].(map[string]any)["fingerprint"].(string)
				if len(fingerprint) != 64 {
					t.Fatalf("second-sync plan fingerprint = %q", fingerprint)
				}
				stdout, stderr, exit = runSync(t, followArgs...)
				if exit != 0 {
					t.Fatalf("literal-destination sync failed: %d\n%s\n%s", exit, stdout, stderr)
				}
				stack, err = internal.LoadStack(internal.FeaturePath("customer"))
				if err != nil {
					t.Fatal(err)
				}
				if got := internal.GetBranch(stack, "collateral").LastBaseSHA; got != configuredDestination {
					t.Fatalf("post-sync cutoff = %s, want actual configured destination %s", got, configuredDestination)
				}
				if got := gitOutput(t, repo, "log", "--format=%s", configuredDestination+"..feat-z"); got != "z" {
					t.Fatalf("post-sync child range = %q, want exactly z", got)
				}
			})
		}
	}
}

func TestCheckoutSyncHonorsConfiguredImmutableDestinationAfterRecordedTransition(t *testing.T) {
	for _, parentKind := range []string{"oid", "tag"} {
		t.Run(parentKind, func(t *testing.T) {
			repo := setupCheckoutSyncRepo(t)
			if err := os.MkdirAll(filepath.Join(repo, ".tws"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, ".tws", "config.yaml"), []byte("workspace_mode: checkout\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			configuredDestination := gitSHA(t, repo, "main")
			parent := configuredDestination
			if parentKind == "tag" {
				gitRunCS(t, repo, "tag", "base-tag", configuredDestination)
				parent = "refs/tags/base-tag"
			}
			writeFileCS(t, repo, "upstream.txt", "upstream\n")
			gitRunCS(t, repo, "add", "upstream.txt")
			gitRunCS(t, repo, "commit", "-m", "upstream")
			recordedTransition := gitSHA(t, repo, "main")
			gitRunCS(t, repo, "checkout", "-b", "feat-z")
			writeFileCS(t, repo, "z.txt", "z\n")
			gitRunCS(t, repo, "add", "z.txt")
			gitRunCS(t, repo, "commit", "-m", "z")
			gitRunCS(t, repo, "checkout", "main")

			featurePath := setupFeaturePath(t, repo)
			saveTestStack(t, featurePath, []internal.StackEntry{{
				Name: "collateral", Branch: "feat-z", Base: parent, LastBaseSHA: recordedTransition,
			}})
			withUnifiedWorkspaceEnv(t, repo)

			planOut, planErr, planExit := runSyncExecute(t,
				"test-feature", "--plan", "--json", "--full", "--no-fetch", "--max-replay-total", "100")
			if planExit != 0 {
				t.Fatalf("checkout literal-destination plan failed: %d\n%s\n%s", planExit, planOut, planErr)
			}
			plan := planDoc(t, planOut)
			row := plan["entries"].([]any)[0].(map[string]any)
			if got := row["destination"].(map[string]any)["sha"]; got != configuredDestination {
				t.Fatalf("checkout destination = %v, want configured %s", got, configuredDestination)
			}
			if got := row["cutoff"].(map[string]any)["effective_sha"]; got != recordedTransition {
				t.Fatalf("checkout cutoff = %v, want recorded transition %s", got, recordedTransition)
			}
			if got := row["replay"].(map[string]any)["candidate_count"]; got != float64(1) {
				t.Fatalf("checkout candidate count = %v, want one child change", got)
			}
			assertOntoArgv(t, row, configuredDestination, recordedTransition, configuredDestination)
			fingerprint, _ := plan["approval"].(map[string]any)["fingerprint"].(string)
			if len(fingerprint) != 64 {
				t.Fatalf("checkout fingerprint = %q", fingerprint)
			}

			args := []string{"test-feature", "--full", "--no-fetch"}
			if parentKind == "tag" {
				args = append(args, "--max-replay-total", "100")
			}
			stdout, stderr, exit := runSync(t, args...)
			if exit != 0 {
				t.Fatalf("checkout literal-destination sync failed: %d\n%s\n%s", exit, stdout, stderr)
			}
			stack, err := internal.LoadStack(featurePath)
			if err != nil {
				t.Fatal(err)
			}
			if got := internal.GetBranch(stack, "collateral").LastBaseSHA; got != configuredDestination {
				t.Fatalf("checkout post-sync cutoff = %s, want %s", got, configuredDestination)
			}
			if got := gitRunCS(t, repo, "log", "--format=%s", configuredDestination+"..feat-z"); got != "z" {
				t.Fatalf("checkout child range = %q, want exactly z", got)
			}
			if got := gitRunCS(t, repo, "branch", "--show-current"); got != "main" {
				t.Fatalf("checkout sync restored %q, want main", got)
			}
		})
	}
}

func TestSyncCollateralParentEvidenceSurvivesRecoveryWindows(t *testing.T) {
	for _, window := range []string{"action-observed:rebase:primary", "metadata-intent", "metadata-written"} {
		t.Run(window, func(t *testing.T) {
			repo, expected := setupRemoteCollateralFixture(t)
			reached := false
			internal.SyncTransactionStepHook = func(step string) error {
				if step == window && !reached {
					reached = true
					return errors.New("interrupt collateral attribution window")
				}
				return nil
			}
			t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
			_, _, exit := runSync(t, "customer", "--full", "--no-fetch")
			if exit == 0 || !reached {
				t.Fatalf("did not retain %s recovery window: exit=%d reached=%v", window, exit, reached)
			}
			internal.SyncTransactionStepHook = nil
			featurePath := internal.FeaturePath("customer")
			payload, err := internal.LoadSyncRunState(featurePath)
			if err != nil {
				t.Fatal(err)
			}
			action := payload.Transaction.Actions[len(payload.Transaction.Actions)-1]
			if !action.ParentDestinationsReady || len(action.ParentDestinations) != 1 {
				t.Fatalf("durable parent destinations = %+v", action.ParentDestinations)
			}
			parent := action.ParentDestinations[0]
			if parent.Entry != "collateral" || parent.Kind != "remote-tracking" ||
				parent.CanonicalRef != "refs/remotes/origin/master" ||
				parent.ActionSHA != expected || parent.DestinationSHA != expected {
				t.Fatalf("durable collateral parent evidence = %+v, want remote %s", parent, expected)
			}
			detachSyncCutoffGuard(t, featurePath)
			stdout, stderr, exit := runSync(t, "customer", "--continue")
			if exit != 0 {
				t.Fatalf("continue from %s failed: %d\n%s\n%s", window, exit, stdout, stderr)
			}
			stack, err := internal.LoadStack(featurePath)
			if err != nil {
				t.Fatal(err)
			}
			if got := internal.GetBranch(stack, "collateral").LastBaseSHA; got != expected {
				t.Fatalf("continued collateral cutoff = %s, want %s", got, expected)
			}
			stdout, stderr, exit = runSync(t, "customer", "--full", "--no-fetch")
			if exit != 0 {
				t.Fatalf("second sync after %s failed: %d\n%s\n%s", window, exit, stdout, stderr)
			}
			if got := gitOutput(t, repo, "rev-parse", "refs/remotes/origin/master"); got != expected {
				t.Fatalf("remote parent changed during recovery: %s, want %s", got, expected)
			}
		})
	}
}

func TestSyncCollateralParentEvidenceRejectsCrossFieldCorruption(t *testing.T) {
	setupRemoteCollateralFixture(t)
	reached := false
	internal.SyncTransactionStepHook = func(step string) error {
		if step == "action-observed:rebase:primary" {
			reached = true
			return errors.New("retain observed action")
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
	stdout, stderr, exit := runSync(t, "customer", "--full", "--no-fetch")
	if exit == 0 || !reached {
		t.Fatalf("expected observed-action interruption: exit=%d reached=%v\n%s\n%s", exit, reached, stdout, stderr)
	}
	internal.SyncTransactionStepHook = nil
	featurePath := internal.FeaturePath("customer")

	tests := []struct {
		name   string
		mutate func(*internal.SyncActionParentDestination)
	}{
		{"canonical-ref", func(parent *internal.SyncActionParentDestination) {
			parent.CanonicalRef = "refs/remotes/origin/other"
		}},
		{"destination", func(parent *internal.SyncActionParentDestination) {
			parent.DestinationSHA = strings.Repeat("a", 40)
		}},
		{"entry", func(parent *internal.SyncActionParentDestination) {
			parent.Entry = "primary"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := internal.LoadSyncRunState(featurePath)
			if err != nil {
				t.Fatal(err)
			}
			action := &payload.Transaction.Actions[len(payload.Transaction.Actions)-1]
			if len(action.ParentDestinations) != 1 {
				t.Fatalf("observed action parent destinations = %+v", action.ParentDestinations)
			}
			tc.mutate(&action.ParentDestinations[0])
			if err := internal.SaveSyncRunState(featurePath, payload); err == nil ||
				!strings.Contains(err.Error(), "invalid sync transaction evidence") {
				t.Fatalf("corrupt parent evidence was accepted: %v", err)
			}
		})
	}
}

func setupRemoteCollateralFixture(t *testing.T) (repo, expected string) {
	t.Helper()
	repo = setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := gitOutput(t, repo, "rev-parse", "master")
	if err := createWorktree("customer", "primary", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	primaryPath := internal.WorktreePath("customer", "primary")
	gitRun(t, primaryPath, "branch", "-m", "feat-a")
	stack, err := internal.LoadStack(internal.FeaturePath("customer"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range stack.Branches {
		if stack.Branches[i].Name == "primary" {
			stack.Branches[i].Branch = "feat-a"
		}
	}
	writeAndCommit(t, primaryPath, "z.txt", "z\n", "z")
	gitRun(t, repo, "branch", "feat-z", gitOutput(t, repo, "rev-parse", "feat-a"))
	stack.Branches = append(stack.Branches, internal.StackEntry{
		Name: "collateral", Branch: "feat-z", Base: "refs/remotes/origin/master",
		LastBaseSHA: root, Archived: true,
	})
	if err := internal.SaveStack(internal.FeaturePath("customer"), stack); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, primaryPath, "a.txt", "a\n", "a")
	writeAndCommit(t, repo, "upstream.txt", "upstream\n", "upstream")
	gitRun(t, repo, "push", "origin", "master")
	expected = gitOutput(t, repo, "rev-parse", "refs/remotes/origin/master")
	return repo, expected
}

func detachSyncCutoffGuard(t *testing.T, featurePath string) {
	t.Helper()
	guard, err := internal.ReadSyncRunGuard(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(internal.SyncRunGuardPath(featurePath), []byte(
		"pid: 99999999\ncreated: \""+guard.Created+"\"\ntoken: "+guard.Token+"\nstate_version: 2\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSyncCollateralRefusesUnprovableImmutableParentBeforeMovement(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		t.Run(map[bool]string{false: "unguarded", true: "guarded"}[guarded], func(t *testing.T) {
			checkSyncCollateralRefusesUnprovableImmutableParentBeforeMovement(t, guarded)
		})
	}
}

func checkSyncCollateralRefusesUnprovableImmutableParentBeforeMovement(t *testing.T, guarded bool) {
	t.Helper()
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	root := gitOutput(t, repo, "rev-parse", "master")
	if err := createWorktree("customer", "primary", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	primaryPath := internal.WorktreePath("customer", "primary")
	writeAndCommit(t, primaryPath, "parent.txt", "parent\n", "immutable parent")
	parent := gitOutput(t, repo, "rev-parse", "primary")
	gitRun(t, repo, "tag", "inside-replay", parent)
	writeAndCommit(t, primaryPath, "z.txt", "z\n", "z")
	zBefore := gitOutput(t, repo, "rev-parse", "primary")
	gitRun(t, repo, "branch", "feat-z", zBefore)
	stack, err := internal.LoadStack(internal.FeaturePath("customer"))
	if err != nil {
		t.Fatal(err)
	}
	stack.Branches = append(stack.Branches, internal.StackEntry{
		Name: "collateral", Branch: "feat-z", Base: "refs/tags/inside-replay",
		LastBaseSHA: parent, Archived: true,
	})
	if err := internal.SaveStack(internal.FeaturePath("customer"), stack); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, primaryPath, "a.txt", "a\n", "a")
	primaryBefore := gitOutput(t, repo, "rev-parse", "primary")
	writeAndCommit(t, repo, "upstream.txt", "upstream\n", "upstream")
	gitRun(t, repo, "push", "origin", "master")

	args := []string{"customer", "--full", "--no-fetch"}
	if guarded {
		args = append(args, "--max-replay-total", "100")
	}
	_, stderr, exit := runSync(t, args...)
	if exit == 0 || !strings.Contains(stderr, "sync collateral cutoff refused before rebase") ||
		!strings.Contains(stderr, "actual collateral old-cutoff -> new-cutoff transition is unproven") ||
		!strings.Contains(stderr, "No Git action for this entry was started") ||
		!strings.Contains(stderr, "tws sync customer --abort") {
		t.Fatalf("unsafe collateral result was not refused precisely: exit=%d stderr=%s", exit, stderr)
	}
	if got := gitOutput(t, repo, "rev-parse", "primary"); got != primaryBefore {
		t.Fatalf("primary moved before refusal: %s, want %s", got, primaryBefore)
	}
	if got := gitOutput(t, repo, "rev-parse", "feat-z"); got != zBefore {
		t.Fatalf("collateral moved before refusal: %s, want %s", got, zBefore)
	}
	gitRun(t, repo, "merge-base", "--is-ancestor", root, "primary")
	featurePath := internal.FeaturePath("customer")
	detachSyncCutoffGuard(t, featurePath)
	stdout, stderr, exit := runSync(t, "customer", "--abort")
	if exit != 0 {
		t.Fatalf("phase-correct abort failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	for _, path := range []string{
		internal.SyncRunStatePath(featurePath),
		internal.SyncStatePath(featurePath),
		internal.SyncRunGuardPath(featurePath),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("abort left recovery artifact %s: %v", path, err)
		}
	}
}

func TestSyncCollateralAttributionIsRepositoryScopedWithCollidingRefs(t *testing.T) {
	repoOne := setupGitRepo(t, "master")
	repoTwo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repoOne)

	type repoCase struct {
		repo, primary, collateral, marker string
		expected                          string
	}
	cases := []repoCase{
		{repo: repoOne, primary: "one-primary", collateral: "one-collateral", marker: "one"},
		{repo: repoTwo, primary: "two-primary", collateral: "two-collateral", marker: "two"},
	}
	for i := range cases {
		tc := &cases[i]
		root := gitOutput(t, tc.repo, "rev-parse", "master")
		if err := createWorktree("customer", tc.primary, "master", tc.repo, false); err != nil {
			t.Fatal(err)
		}
		primaryPath := internal.WorktreePath("customer", tc.primary)
		primaryBranch := "feat-a-" + tc.marker
		collateralBranch := "feat-z-" + tc.marker
		gitRun(t, primaryPath, "branch", "-m", primaryBranch)
		stack, err := internal.LoadStack(internal.FeaturePath("customer"))
		if err != nil {
			t.Fatal(err)
		}
		primaryRepo := ""
		for j := range stack.Branches {
			if stack.Branches[j].Name == tc.primary {
				stack.Branches[j].Branch = primaryBranch
				primaryRepo = stack.Branches[j].Repo
			}
		}
		writeAndCommit(t, primaryPath, tc.marker+"-z.txt", tc.marker+"\n", tc.marker+" z")
		gitRun(t, tc.repo, "branch", collateralBranch, gitOutput(t, tc.repo, "rev-parse", primaryBranch))
		stack.Branches = append(stack.Branches, internal.StackEntry{
			Name: tc.collateral, Branch: collateralBranch, Base: "refs/remotes/origin/master",
			Repo: primaryRepo, LastBaseSHA: root, Archived: true,
		})
		if err := internal.SaveStack(internal.FeaturePath("customer"), stack); err != nil {
			t.Fatal(err)
		}
		writeAndCommit(t, primaryPath, tc.marker+"-a.txt", tc.marker+"\n", tc.marker+" a")
		writeAndCommit(t, tc.repo, tc.marker+"-upstream.txt", tc.marker+"\n", tc.marker+" upstream")
		gitRun(t, tc.repo, "push", "origin", "master")
		tc.expected = gitOutput(t, tc.repo, "rev-parse", "refs/remotes/origin/master")
	}
	if cases[0].expected == cases[1].expected {
		t.Fatal("multi-repository fixture destinations unexpectedly collide")
	}

	stdout, stderr, exit := runSync(t, "customer", "--full", "--no-fetch")
	if exit != 0 {
		t.Fatalf("multi-repository sync failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	stack, err := internal.LoadStack(internal.FeaturePath("customer"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if got := internal.GetBranch(stack, tc.collateral).LastBaseSHA; got != tc.expected {
			t.Fatalf("%s cutoff = %s, want repository-local %s", tc.collateral, got, tc.expected)
		}
	}
	stdout, stderr, exit = runSync(t, "customer", "--full", "--no-fetch")
	if exit != 0 {
		t.Fatalf("second multi-repository sync failed: %d\n%s\n%s", exit, stdout, stderr)
	}
}

func TestExternalRecoveryCompletedFailureDoesNotNeedCutoffs(t *testing.T) {
	payload := &internal.SyncRunState{
		FailedBranch: "child",
		Completed:    []string{"child"},
		Transaction: &internal.SyncTransaction{Actions: []internal.SyncTransactionAction{{
			Kind: "rebase", Entry: "child", Status: internal.SyncTxnActionObserved,
		}}},
	}
	if externalRecoveryNeedsCutoffs(payload) {
		t.Fatal("publication-only recovery must not require replay cutoff evidence")
	}
}

func TestSyncPlanLocalOnlyAnchorResolvesRawRecordedCutoff(t *testing.T) {
	f := newScopedFixture(t)
	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--local-only", "--no-fetch")
	if exit != 0 {
		t.Fatalf("plan failed: %d %s", exit, stderr)
	}
	for _, raw := range planDoc(t, stdout)["entries"].([]any) {
		row := raw.(map[string]any)
		if row["name"] != "root" {
			continue
		}
		cutoff := row["cutoff"].(map[string]any)
		if cutoff["state"] != "present" || cutoff["resolved_sha"] == nil ||
			cutoff["validity"] != "not-applicable" || cutoff["effective_sha"] != nil {
			t.Fatalf("skipped anchor cutoff is dishonest: %#v", cutoff)
		}
		return
	}
	t.Fatal("root anchor row not found")
}

func TestSyncArchivedRestoreInterruptionContinuesOriginalContext(t *testing.T) {
	f := newScopedFixture(t)
	if err := archiveExternal(f.feature, "child"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.wt("parent"), "parent-v2.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.wt("parent"), "add", "parent-v2.txt")
	gitRun(t, f.wt("parent"), "commit", "-m", "parent v2")
	reached := false
	internal.SyncTransactionStepHook = func(step string) error {
		if strings.HasPrefix(step, "forward-holder-restore-intent:") {
			reached = true
			return errors.New("interrupt before archived context restore")
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
	_, _, exit := runSync(t, f.feature, "--local-only", "--no-fetch")
	if exit == 0 || !reached {
		t.Fatalf("did not retain the archived restoration window: exit=%d reached=%v", exit, reached)
	}
	internal.SyncTransactionStepHook = nil
	f.detachGuard(t)
	stdout, stderr, exit := runSync(t, f.feature, "--continue")
	if exit != 0 {
		t.Fatalf("continue failed: %d\n%s\n%s", exit, stdout, stderr)
	}
	if got := gitOutput(t, f.repo, "symbolic-ref", "--short", "HEAD"); got != "master" {
		t.Fatalf("continue left computation checkout on %s, want master", got)
	}
}
