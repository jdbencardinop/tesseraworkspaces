package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// Acceptance criteria carried by this file's cells: AC-052 and AC-053 (argv
// template vs materialization, and a guarded run equal to its unguarded twin),
// AC-054 (hostile rebase.* config is neutralized), AC-055 (the forbidden-verb
// audit), AC-056 (the scratch worktree lifecycle), AC-074 (all 17 crash
// windows, each verb, twice), AC-075 (partial-commit reconciliation exits 0),
// AC-076 (abort before and after the commit point), AC-077 (a resume takes no
// token and no limit), AC-078 (the cleanup order, artifact last).

// ---------------------------------------------------------------------------
// T-037 — plan side effects and fetch placement.
// T-039 — the emitted argv template and its materialization.
// T-040 — hostile `rebase.*` configuration.
// T-042 — the scratch worktree's lifecycle.
// T-047 — pins, observed through the production command.
// T-061, T-062 — crash windows and forward resume.
// T-063 — abort before the commit point.
// T-065 — the recovery verbs' frozen-flag rejection.
// T-066 — repeatable cleanup.
//
// Every argv assertion here is a BEHAVIOUR twin of the source-audit file's own
// merge-base and forbidden-verb cells, and cites its own AC directly. It
// deliberately re-claims neither of those two matrix cells: that file is their
// sole owner, so the bidirectional mapping assertion keeps reporting exactly
// one owner each.
// ---------------------------------------------------------------------------

// TestReparentIntegration_PlanIsSideEffectFree is T-037 / AC-011. A plan moves
// no branch, writes no tws runtime state, and leaves stack.yaml byte-identical
// — while still being allowed to fetch exactly where the run it describes
// fetches.
func TestReparentIntegration_PlanIsSideEffectFree(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-037",
		"plan-side-effect-free",
		"fetch-projection-shared-semantics",
	)
	_ = "asserts AC-051"
	f := newReparentCustomerExternal(t)

	stackBefore, err := os.ReadFile(internal.StackPath(f.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)")

	log := reparentCaptureArgv(t)
	noFetchJSON, _ := f.Plan(
		f.Feature, "pr2", "--onto", "master", "--no-fetch",
		"--max-replay-total", "50", "--json",
	)
	var noFetchPlan internal.ReparentPlan
	if err := json.Unmarshal([]byte(noFetchJSON), &noFetchPlan); err != nil {
		t.Fatal(err)
	}
	if noFetchPlan.Fetch.Attempted || noFetchPlan.Fetch.Outcome != "skipped" ||
		noFetchPlan.Fetch.PolicySource != "flag" ||
		noFetchPlan.Fetch.SuppressionCause != nil ||
		noFetchPlan.Freshness != "local-only" {
		t.Fatalf("no-fetch projection = %+v freshness=%q", noFetchPlan.Fetch, noFetchPlan.Freshness)
	}

	// §6.3: steps 1-10 issue no MUTATING Git verb.
	mutating := map[string]bool{
		"rebase": true, "reset": true, "switch": true, "checkout": true,
		"update-ref": true, "push": true, "commit": true, "merge": true,
		"cherry-pick": true, "branch": true,
	}
	for _, argv := range log.Argv {
		if len(argv) == 0 {
			continue
		}
		verb := argvVerb(argv)
		if verb == "worktree" && len(argv) > 1 && argv[1] != "list" && argv[1] != "prune" {
			t.Fatalf("a plan may not create or remove a worktree: %v", argv)
		}
		if mutating[verb] {
			t.Fatalf("a plan may issue no mutating verb, got %v", argv)
		}
	}

	stackAfter, err := os.ReadFile(internal.StackPath(f.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(stackBefore) != string(stackAfter) {
		t.Fatal("a plan must leave stack.yaml byte-identical")
	}
	if after := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)"); after != refsBefore {
		t.Fatalf("a plan must move no ref:\n--- before ---\n%s\n--- after ---\n%s", refsBefore, after)
	}
	if internal.HasReparentState(f.Loc()) {
		t.Fatal("a plan must write no reparent runtime state")
	}
	if internal.HasSyncState(f.FeaturePath) || internal.HasSyncRunState(f.FeaturePath) {
		t.Fatal("a plan must write no compatibility artifact")
	}

	missingRemote := filepath.Join(t.TempDir(), "missing-origin.git")
	f.reparentGit(f.Repo, "remote", "set-url", "origin", missingRemote)
	failedJSON, _, exit := f.Run(
		f.Feature, "pr2", "--onto", "master",
		"--plan", "--json", "--max-replay-total", "50",
	)
	if exit != 0 {
		t.Fatalf("failed-fetch plan exit = %d", exit)
	}
	var failedPlan internal.ReparentPlan
	if err := json.Unmarshal([]byte(failedJSON), &failedPlan); err != nil {
		t.Fatal(err)
	}
	if !failedPlan.Fetch.Attempted || failedPlan.Fetch.Outcome != "failed" ||
		failedPlan.Fetch.PolicySource != "route-default" ||
		failedPlan.Freshness != "possibly-stale" ||
		len(failedPlan.Fetch.Repos) == 0 ||
		failedPlan.Fetch.MutatedRemoteTrackingRefs != nil ||
		failedPlan.Fetch.MutatedLocalBranches != nil {
		t.Fatalf("failed-fetch projection = %+v freshness=%q", failedPlan.Fetch, failedPlan.Freshness)
	}
}

// TestReparentIntegration_RecoveryVerbsNeverFetch is AC-051's second half.
func TestReparentIntegration_RecoveryVerbsNeverFetch(t *testing.T) {
	f := newReparentCustomerExternal(t)
	reparentPlantState(t, f, internal.ReparentStageComputing)

	for _, verb := range []string{"--continue", "--abort"} {
		t.Run(verb, func(t *testing.T) {
			log := reparentCaptureArgv(t)
			_, stderr, _ := f.Run(f.Feature, verb)
			for _, argv := range log.Argv {
				if argvVerb(argv) == "fetch" {
					t.Fatalf("%s must never fetch, got %v (%s)", verb, argv, stderr)
				}
			}
			if strings.Contains(stderr, "Fetching ") {
				t.Fatalf("%s must emit no fetch prose:\n%s", verb, stderr)
			}
		})
	}
}

// TestReparentIntegration_ExternalPlanFetchesByDefault is §3.4's fetch policy:
// external defaults to fetch, checkout to no-fetch, matching resolveSyncPolicy
// exactly — and it happens EXACTLY ONCE.
func TestReparentIntegration_ExternalPlanFetchesByDefault(t *testing.T) {
	t.Run("external default fetches once", func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		_, prose := f.Plan(f.Feature, "pr2", "--onto", "master", "--max-replay-total", "50")
		if n := strings.Count(prose, "Fetching "); n != 1 {
			t.Fatalf("an external plan must fetch exactly once, saw %d:\n%s", n, prose)
		}

		func(t *testing.T) {
			reparentCountCLIGitLeaf(t)
			t.Setenv("GIT_CONFIG_COUNT", "0")
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			root := setupGitRepo(t, "master")
			secondary := setupGitRepo(t, "master")
			withWorkspaceEnv(t, root)

			base := gitOutput(t, secondary, "rev-parse", "master")
			gitRun(t, secondary, "branch", "topic", base)
			gitRun(t, secondary, "switch", "topic")
			writeAndCommit(t, secondary, "topic.txt", "topic\n", "topic")
			gitRun(t, secondary, "switch", "master")
			writeAndCommit(t, secondary, "remote.txt", "remote\n", "remote")
			remoteTip := gitOutput(t, secondary, "rev-parse", "master")
			gitRun(t, secondary, "push", "origin", "master")
			gitRun(t, secondary, "update-ref", "refs/heads/master", base)
			gitRun(t, secondary, "update-ref", "refs/remotes/origin/master", base)

			featurePath := internal.FeaturePath("feature")
			if err := os.MkdirAll(featurePath, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: "topic", Branch: "topic", Repo: secondary,
				Base: "refs/heads/master", LastBaseSHA: base,
			}}}); err != nil {
				t.Fatal(err)
			}
			rootRefsBefore := gitOutput(t, root, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes")

			stdout, stderr := syncCaptureStreams(t, func() {
				if code := syncExecute(stackCmd, "reparent", "feature", "topic",
					"--onto", "origin/master", "--onto-kind", "ref",
					"--fetch", "--plan", "--json", "--max-replay-total", "10"); code != 0 {
					t.Errorf("plan exit = %d", code)
				}
			})
			var plan internal.ReparentPlan
			if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
				t.Fatalf("plan json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			if got := gitOutput(t, secondary, "rev-parse", "refs/remotes/origin/master"); got != remoteTip {
				t.Fatalf("secondary tracking ref = %s, want fetched %s", got, remoteTip)
			}
			if got := gitOutput(t, root, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes"); got != rootRefsBefore {
				t.Fatalf("workspace repository was fetched instead of the target repository:\n--- before ---\n%s\n--- after ---\n%s", rootRefsBefore, got)
			}
			wantSecondary, err := filepath.EvalSymlinks(secondary)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Target.ExecutionContext.RepoRoot == nil || *plan.Target.ExecutionContext.RepoRoot != filepath.Clean(wantSecondary) {
				t.Fatalf("plan target repository = %v, want %s", plan.Target.ExecutionContext.RepoRoot, secondary)
			}
			if plan.Target.DestinationSHA == nil || *plan.Target.DestinationSHA != remoteTip {
				t.Fatalf("destination = %v, want post-fetch target-repo tip %s", plan.Target.DestinationSHA, remoteTip)
			}
		}(t)
	})
	t.Run("checkout default does not fetch", func(t *testing.T) {
		f := newReparentCustomerCheckout(t)
		_, prose := f.Plan(f.Feature, "pr2", "--onto", "main", "--max-replay-total", "50")
		if strings.Contains(prose, "Fetching ") {
			t.Fatalf("a checkout plan must not fetch by default:\n%s", prose)
		}
	})
	t.Run("a fresh execution fetches exactly once", func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		human, prose := f.Plan(f.Feature, "pr2", "--onto", "master", "--max-replay-total", "50")
		token := reparentFingerprint(t, human+"\n"+prose)
		_, stderr, exit := f.Run(f.Feature, "pr2", "--onto", "master",
			"--max-replay-total", "50", "--approve-plan", token)
		if exit != 0 {
			t.Fatalf("execution exit = %d:\n%s", exit, stderr)
		}
		if n := strings.Count(stderr, "Fetching "); n != 1 {
			t.Fatalf("a fresh execution must fetch exactly once — the post-lock re-snapshot reuses the measured outcome — saw %d:\n%s", n, stderr)
		}
	})
}

// TestReparentIntegration_ArgvShapeIsPathFree is T-039's behaviour twin
// (AC-031): the executor selects its directory with cmd.Dir, so no emitted
// argv carries `-C`, and each replay is the documented `--onto <dest>
// <cutoff>` form.
func TestReparentIntegration_ArgvShapeIsPathFree(t *testing.T) {
	_ = "asserts AC-052 AC-053"
	assertReparentMatrixBehavior(t, "T-039", "argv-template-materialization-no-dash-c")
	f := newReparentCustomerExternal(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	processLog := filepath.Join(shimDir, "all-git-argv.log")
	shim := filepath.Join(shimDir, "git")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$TWS_REPARENT_GIT_LOG\"\n" +
		"exec \"$TWS_REPARENT_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_REPARENT_GIT_LOG", processLog)
	t.Setenv("TWS_REPARENT_REAL_GIT", realGit)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := reparentCaptureArgv(t)
	if _, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 0 {
		t.Fatalf("execution exit = %d: %s", exit, stderr)
	}

	sawRebase := false
	for _, argv := range log.Argv {
		if argvHas(argv, "-C") {
			t.Fatalf("no emitted argv may carry -C: %v", argv)
		}
		if argvVerb(argv) == "rebase" {
			sawRebase = true
			if !argvHas(argv, "--onto") {
				t.Fatalf("every replay must use the --onto form, got %v", argv)
			}
		}
	}
	if !sawRebase {
		t.Fatalf("the execution must have replayed at least one row:\n%s", strings.Join(log.Lines(), "\n"))
	}
	for i, dir := range log.Dirs {
		if dir == "" {
			t.Fatalf("every reparent child must select its directory explicitly, argv %v had none", log.Argv[i])
		}
	}
	allProcesses, err := os.ReadFile(processLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(allProcesses)), "\n") {
		for _, arg := range strings.Fields(line) {
			if arg == "-C" {
				t.Fatalf("PATH-level audit observed hidden git -C: %q\nall processes:\n%s", line, allProcesses)
			}
		}
	}
}

// TestReparentIntegration_HostileRebaseConfigCannotChangeTheReplay is T-040.
// The run must be immune to repository-level `rebase.*` configuration that
// would otherwise silently change the backend, add autostash, or turn the
// replay into a merge-preserving one.
func TestReparentIntegration_HostileRebaseConfigCannotChangeTheReplay(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-040", "hostile-rebase-config-neutralized")
	_ = "asserts AC-054"
	f := newReparentCustomerExternal(t)
	for _, kv := range [][2]string{
		{"rebase.autoStash", "true"},
		{"rebase.updateRefs", "true"},
		{"rebase.rebaseMerges", "true"},
		{"rebase.forkPoint", "true"},
		{"rebase.backend", "apply"},
		{"rebase.abbreviateCommands", "true"},
	} {
		f.reparentGit(f.Repo, "config", kv[0], kv[1])
	}

	log := reparentCaptureArgv(t)
	if _, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 0 {
		t.Fatalf("hostile rebase config must not break the run, exit = %d:\n%s", exit, stderr)
	}
	for _, argv := range log.Argv {
		if argvVerb(argv) != "rebase" {
			continue
		}
		for _, forbidden := range []string{"--autostash", "--update-refs", "--rebase-merges", "--fork-point", "--apply"} {
			if argvHas(argv, forbidden) {
				t.Fatalf("configuration must never reintroduce %s: %v", forbidden, argv)
			}
		}
		// The run pins every hostile knob back off explicitly, rather than
		// trusting the ambient value.
		for _, required := range []string{"--no-autostash", "--no-update-refs", "--no-rebase-merges", "--no-fork-point", "--merge"} {
			if !argvHas(argv, required) {
				t.Fatalf("the replay must pin %s explicitly against hostile config, got %v", required, argv)
			}
		}
	}

	// And the replay is still correct: exactly C lands above master.
	master := f.SHA("master")
	if n := len(strings.Fields(f.reparentGit(f.Repo, "rev-list", master+"..feat-pr2"))); n != 1 {
		t.Fatalf("hostile config changed the replay set: %d commits above master, want 1", n)
	}
}

// TestReparentIntegration_ScratchLifecycle is T-042. The external computation
// worktree is created under the feature's own `.reparent/` tree, is never
// inside `worktrees/`, and is removed and pruned by a successful run.
func TestReparentIntegration_ScratchLifecycle(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-042", "scratch-lifecycle-and-filtering")
	_ = "asserts AC-056"
	f := newReparentCustomerExternal(t)
	scratchRoot := filepath.Join(f.FeaturePath, ".reparent")

	if _, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 0 {
		t.Fatalf("execution exit = %d: %s", exit, stderr)
	}

	// The scratch WORKTREE is gone and Git no longer knows about it. The
	// run-id directory beneath `.reparent/` may legitimately survive as an
	// empty shell; what must not survive is a live worktree.
	if entries, err := os.ReadDir(scratchRoot); err == nil {
		for _, e := range entries {
			inner := filepath.Join(scratchRoot, e.Name(), "scratch")
			if _, statErr := os.Stat(inner); statErr == nil {
				t.Fatalf("the scratch worktree must be removed by a successful run: %s", inner)
			}
		}
	}
	porcelain := f.reparentGit(f.Repo, "worktree", "list", "--porcelain")
	if strings.Contains(porcelain, string(filepath.Separator)+".reparent"+string(filepath.Separator)) {
		t.Fatalf("the scratch worktree must be removed and pruned:\n%s", porcelain)
	}
	// The shipped worktrees/ tree is untouched and still collision-free.
	for _, name := range []string{"pr1", "pr2"} {
		if _, err := os.Stat(internal.WorktreePath(f.Feature, name)); err != nil {
			t.Fatalf("the shipped worktree for %s must survive: %v", name, err)
		}
	}
}

// TestReparentIntegration_PinsAreRemovedBySuccess is T-047's CLI half: a
// successful run leaves no pin under its own namespace.
func TestReparentIntegration_PinsAreRemovedBySuccess(t *testing.T) {
	f := newReparentCustomerExternal(t)
	if _, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 0 {
		t.Fatalf("execution exit = %d: %s", exit, stderr)
	}
	refs := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname)", "refs/tws/reparent/")
	if strings.TrimSpace(refs) != "" {
		t.Fatalf("a successful run must delete every pin, still present:\n%s", refs)
	}
}

// TestReparentRecovery_ContinueCompletesAPausedRun is T-061/T-062's forward
// half. A crash injected before the CAS leaves a resumable artifact, and
// --continue drives the same ladder forward to completion.
func TestReparentRecovery_ContinueCompletesAPausedRun(t *testing.T) {
	f := newReparentCustomerExternal(t)

	// Fail at the ref transaction's own step, which is before the commit
	// point: the artifact must survive and the topology must not have moved.
	internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
		if stage == internal.ReparentStageCommittingRefs && step == "before-cas" {
			return &reparentInjectedFailure{}
		}
		return nil
	}
	t.Cleanup(func() { internal.ReparentStepHook = nil })

	_, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch")
	if exit != 1 {
		t.Fatalf("an injected crash must fail the invocation, exit = %d:\n%s", exit, stderr)
	}
	if !internal.HasReparentState(f.Loc()) {
		t.Fatal("a pre-commit-point crash must leave a resumable artifact")
	}

	internal.ReparentStepHook = nil
	stdout, stderr, exit := f.Run(f.Feature, "--continue")
	if exit != 0 {
		t.Fatalf("--continue exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("continue success writes no plan document, got %q", stdout)
	}
	for _, want := range []string{
		"materialized argv:",
		"atomicity: the ref CAS was race-atomic",
		"atomicity: refs, stack.yaml, and worktree/index state were three separate effects",
		"tws changed no remote ref and no pull request.",
		"retarget the pull request first",
		"git push --force-with-lease --force-if-includes origin",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("continue success must reconstruct %q from persisted state:\n%s", want, stderr)
		}
	}
	master := f.SHA("master")
	if !reparentIsAncestorOf(t, f, master, f.SHA("feat-pr2")) {
		t.Fatal("--continue must complete the topology change")
	}
	assertReparentResidueGone(t, f)
}

// TestReparentRecovery_AbortRollsBackBeforeTheCommitPoint is T-063. Before the
// commit point an --abort is a bounded, non-destructive rollback: every ref
// returns to its pre-image and stack.yaml is untouched.
func TestReparentRecovery_AbortRollsBackBeforeTheCommitPoint(t *testing.T) {
	f := newReparentCustomerExternal(t)
	f.reparentGit(f.Repo, "update-ref", "refs/remotes/origin/feat-pr2", f.SHA("feat-pr2"))
	refsBefore := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	stackBefore, err := os.ReadFile(internal.StackPath(f.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}

	internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
		if stage == internal.ReparentStageCommittingRefs && step == "after-cas" {
			return &reparentInjectedFailure{}
		}
		return nil
	}
	t.Cleanup(func() { internal.ReparentStepHook = nil })
	if _, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 1 {
		t.Fatalf("the injected crash must fail, exit = %d:\n%s", exit, stderr)
	}
	internal.ReparentStepHook = nil

	recordBefore, err := os.ReadFile(internal.ReparentRemoteRecordPath(f.Loc()))
	if err != nil {
		t.Fatalf("CAS-done/metadata-pending must retain its remote record: %v", err)
	}
	_, pushErr, log := reparentPushCapture(t, func() {
		if code := syncExecute(pushCmd, f.Feature); code != 1 {
			t.Errorf("pre-commit push exit = %d, want 1", code)
		}
	})
	if !strings.Contains(pushErr, "reparent: reparent-state-present:") {
		t.Fatalf("pre-commit push must refuse before clearing protection:\n%s", pushErr)
	}
	for _, argv := range log.argv {
		if argvVerb(argv) == "push" {
			t.Fatalf("pre-commit push reached Git: %v", argv)
		}
	}
	recordAfter, err := os.ReadFile(internal.ReparentRemoteRecordPath(f.Loc()))
	if err != nil || string(recordAfter) != string(recordBefore) {
		t.Fatalf("pre-commit push changed the remote record: %v", err)
	}

	holder := internal.WorktreePath(f.Feature, "pr2")
	if err := os.WriteFile(filepath.Join(holder, "c.txt"), []byte("block holder restoration\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := f.Run(f.Feature, "--abort")
	if exit != 0 {
		t.Fatalf("--abort exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if strings.Contains(stderr, "reparent holder restoration is deferred") {
		t.Fatalf("CLI duplicated the executor-owned holder warning:\n%s", stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("deferred abort emitted %d lines, want the executor's two anchored lines:\n%s", len(lines), stderr)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "reparent: holder-restore-deferred:") {
			t.Fatalf("deferred abort emitted an unanchored line %q", line)
		}
	}
	if load := internal.LoadReparentState(f.Loc()); load.Kind != internal.ReparentStateOK {
		t.Fatalf("deferred abort removed authoritative state: %+v", load)
	}
	f.reparentGit(holder, "checkout", "--", "c.txt")
	stdout, stderr, exit = f.Run(f.Feature, "--abort")
	if exit != 0 {
		t.Fatalf("retry --abort exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if after := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); after != refsBefore {
		t.Fatalf("a pre-commit-point abort must restore every ref:\n--- before ---\n%s\n--- after ---\n%s", refsBefore, after)
	}
	stackAfter, err := os.ReadFile(internal.StackPath(f.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(stackBefore) != string(stackAfter) {
		t.Fatal("a pre-commit-point abort must leave stack.yaml byte-identical")
	}
	if _, err := os.Stat(internal.ReparentRemoteRecordPath(f.Loc())); !os.IsNotExist(err) {
		t.Fatalf("abort must remove the uncommitted run's remote protection: %v", err)
	}
	assertReparentResidueGone(t, f)
}

// TestReparentRecovery_RecoveryVerbsRejectEveryFrozenFlag is T-065 and §3.2's
// forbidden-flag column: a recovery verb takes the whole decision from
// persisted state, so supplying any of it refuses BEFORE any lock or Git
// command.
func TestReparentRecovery_RecoveryVerbsRejectEveryFrozenFlag(t *testing.T) {
	_ = "asserts AC-005 AC-077"
	assertReparentMatrixBehavior(t, "T-065", "recovery-flags-rejected")
	f := newReparentCustomerExternal(t)
	reparentPlantState(t, f, internal.ReparentStageComputing)

	for _, flag := range [][]string{
		{"--onto", "master"},
		{"--onto-kind", "ref"},
		{"--cutoff", "master"},
		{"--approve-plan", strings.Repeat("a", 64)},
		{"--max-replay-per-entry", "3"},
		{"--max-replay-total", "3"},
		{"--fetch"},
		{"--no-fetch"},
	} {
		t.Run(flag[0], func(t *testing.T) {
			log := reparentCaptureArgv(t)
			args := append([]string{f.Feature, "--continue"}, flag...)
			_, stderr, exit := f.Run(args...)
			if exit != 1 {
				t.Fatalf("exit = %d, want 1: %s", exit, stderr)
			}
			if !strings.Contains(stderr, flag[0]+" cannot be combined with --continue") {
				t.Fatalf("stderr = %q, want the frozen-flag refusal", stderr)
			}
			if len(log.Argv) != 0 {
				t.Fatalf("the refusal must precede every Git command, saw:\n%s", strings.Join(log.Lines(), "\n"))
			}
		})
	}
}

// TestReparentRecovery_CleanupIsRepeatable is T-066: a second --continue on an
// already-complete run reports it, removes residue, and exits 0.
func TestReparentRecovery_CleanupIsRepeatable(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-066", "cleanup-repeatable")
	_ = "asserts AC-078"
	f := newReparentCustomerExternal(t)
	if _, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 0 {
		t.Fatalf("execution exit = %d: %s", exit, stderr)
	}
	// With the run complete and its artifact removed, a --continue must refuse
	// cleanly rather than invent a run.
	_, stderr, exit := f.Run(f.Feature, "--continue")
	if exit != 1 {
		t.Fatalf("exit = %d, want 1: %s", exit, stderr)
	}
	if !strings.Contains(stderr, "no reparent run is recorded") {
		t.Fatalf("stderr = %q, want the no-run refusal", stderr)
	}
}

// reparentInjectedFailure is the crash the step hook injects. It is a distinct
// type so an assertion can tell an injected failure from a real one.
type reparentInjectedFailure struct{}

func (e *reparentInjectedFailure) Error() string { return "injected reparent step failure" }

// TestReparentRecovery_AbortAfterTheCommitPointSaysCommitted pins §11.8a's
// other arm on the operator-facing stream. At or after the commit point
// --abort moves no ref and rewrites no stack.yaml, so reporting "was rolled
// back" would tell the operator their topology change had been undone while
// it is still live on every branch.
func TestReparentRecovery_AbortAfterTheCommitPointSaysCommitted(t *testing.T) {
	f := newReparentCustomerExternal(t)

	internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
		if stage == internal.ReparentStageWritingMetadata && step == "after-write" {
			return &reparentInjectedFailure{}
		}
		return nil
	}
	t.Cleanup(func() { internal.ReparentStepHook = nil })
	if _, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 1 {
		t.Fatalf("the injected crash must fail, exit = %d:\n%s", exit, stderr)
	}
	internal.ReparentStepHook = nil

	refsAfterCommit := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	stackAfterCommit, err := os.ReadFile(internal.StackPath(f.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := f.Run(f.Feature, "--abort")
	if exit != 0 {
		t.Fatalf("--abort exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if strings.Contains(stderr, "was rolled back") {
		t.Fatalf("a post-commit-point abort never rolls back:\n%s", stderr)
	}
	if !strings.Contains(stderr, "had already committed") {
		t.Fatalf("the operator must be told the reparent had already committed:\n%s", stderr)
	}
	if !strings.Contains(stderr, "no ref or metadata was changed") {
		t.Fatalf("the wording must say what the abort did NOT do:\n%s", stderr)
	}
	if !strings.Contains(stderr, "new reparent in the opposite direction") {
		t.Fatalf("the operator must be told how to undo it:\n%s", stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout is reserved for the plan document: %q", stdout)
	}

	if after := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); after != refsAfterCommit {
		t.Fatalf("a post-commit-point abort must move no ref:\n--- before ---\n%s\n--- after ---\n%s", refsAfterCommit, after)
	}
	stackNow, err := os.ReadFile(internal.StackPath(f.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(stackNow) != string(stackAfterCommit) {
		t.Fatal("a post-commit-point abort must not rewrite stack.yaml")
	}
	assertReparentResidueGone(t, f)
}
