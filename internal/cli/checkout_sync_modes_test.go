package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// Checkout-mode sync modes (§10). These drive internal.RunCheckoutSync
// directly against a real single-checkout repository.
// ---------------------------------------------------------------------------

func checkoutModeFixture(t *testing.T) (dir, featurePath string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	clearStepHook(t)
	dir = setupCheckoutSyncRepo(t)
	featurePath = setupFeaturePath(t, dir)
	createStackBranch(t, dir, "feat-root", "main", "root.txt", "root\n")
	createStackBranch(t, dir, "feat-a", "feat-root", "a.txt", "a\n")
	createStackBranch(t, dir, "feat-b", "feat-a", "b.txt", "b\n")
	gitRunCS(t, dir, "checkout", "main")
	saveTestStack(t, featurePath, []internal.StackEntry{
		{Name: "feat-root", Base: "main"},
		{Name: "feat-a", Base: "feat-root"},
		{Name: "feat-b", Base: "feat-a"},
	})
	return dir, featurePath
}

func newModeOpts(dir, featurePath string, policy internal.SyncRunPolicy, changed ...string) internal.CheckoutSyncOpts {
	m := make(map[string]bool, len(changed))
	for _, k := range changed {
		m[k] = true
	}
	return internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: featurePath,
		RepoDir:     dir,
		Policy:      policy,
		NewMode:     true,
		Changed:     m,
	}
}

func captureRun(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = fn() })
	return out, err
}

func TestCheckoutSyncModes_ScopedPlanExcludesUnselected(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	bBefore := gitSHA(t, dir, "feat-b")

	// Advance main so feat-root has real work under `full`.
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "main2.txt", "main2\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "main advance")

	policy := internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeOne, Selector: "feat-root"}
	out, err := captureRun(t, func() error {
		return internal.RunCheckoutSync(newModeOpts(dir, fp, policy, "only"))
	})
	if err != nil {
		t.Fatalf("scoped checkout sync: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Sync mode: fetch=no-fetch propagation=full scope=only:feat-root") {
		t.Fatalf("missing header:\n%s", out)
	}
	if got := gitSHA(t, dir, "feat-b"); got != bBefore {
		t.Fatalf("an unselected branch moved: %s -> %s", bBefore, got)
	}
	tx, err := internal.LoadCheckoutTransaction(fp)
	if err == nil && tx != nil {
		t.Fatal("a completed transaction must be deleted")
	}
}

func TestCheckoutSyncModes_TransactionRecordsFrozenDecision(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	clearStepHook(t)
	internal.StepHook = func(stage internal.CheckoutStage, i int) error {
		if stage == internal.StageRebased {
			return errStop
		}
		return nil
	}
	policy := internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeSubtree, Selector: "feat-a"}
	_, _ = captureRun(t, func() error {
		return internal.RunCheckoutSync(newModeOpts(dir, fp, policy, "from"))
	})
	tx, err := internal.LoadCheckoutTransaction(fp)
	if err != nil {
		t.Fatalf("the interrupted transaction must be on disk: %v", err)
	}
	if tx.StateVersion != internal.CheckoutTransactionVersion {
		t.Fatalf("state_version = %d, want %d", tx.StateVersion, internal.CheckoutTransactionVersion)
	}
	if tx.FetchPolicy != "no-fetch" || tx.PropagationPolicy != "full" || tx.ScopeKind != "subtree" || tx.ScopeSelector != "feat-a" {
		t.Fatalf("the frozen decision is not persisted: %+v", tx)
	}
	if len(tx.Selected) != 2 || tx.Selected[0] != "feat-a" {
		t.Fatalf("selected = %v", tx.Selected)
	}
	for _, pe := range tx.Plan {
		if pe.Name == "" {
			t.Fatal("every plan entry carries its logical Name (C5)")
		}
		if pe.Name == "feat-root" {
			t.Fatal("an unselected entry must not be planned")
		}
	}
}

func TestCheckoutSyncModes_NoFlagTransactionStaysLegacyShape(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	clearStepHook(t)
	internal.StepHook = func(stage internal.CheckoutStage, i int) error {
		if stage == internal.StageRebased {
			return errStop
		}
		return nil
	}
	_, _ = captureRun(t, func() error {
		return internal.RunCheckoutSync(internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir})
	})
	data, err := os.ReadFile(internal.CheckoutTransactionPath(fp))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Contains(body, "state_version:") {
		t.Fatalf("a no-flag transaction omits state_version:\n%s", body)
	}
	for _, key := range []string{"fetch_policy:", "propagation_policy:", "scope_kind:", "scope_selector:", "selected:", "validation_source:"} {
		if strings.Contains(body, key) {
			t.Fatalf("a no-flag transaction must not gain %q:\n%s", key, body)
		}
	}
	// C5: the additive per-plan-entry name key IS written on the frozen path.
	if !strings.Contains(body, "name: feat-root") {
		t.Fatalf("the plan must carry names even on the no-flag path:\n%s", body)
	}
}

func TestCheckoutSyncModes_LocalOnlyNoOpBlock(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	saveTestStack(t, fp, []internal.StackEntry{{Name: "feat-root", Base: "main"}})

	policy := internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationLocalOnly, ScopeKind: internal.SyncScopeOne, Selector: "feat-root"}
	out, err := captureRun(t, func() error {
		return internal.RunCheckoutSync(newModeOpts(dir, fp, policy, "only", "local-only"))
	})
	if err != nil {
		t.Fatalf("a no-op selection is a success: %v\n%s", err, out)
	}
	if !strings.Contains(out, "  [-] feat-root (no in-stack parent edge to propagate)") {
		t.Fatalf("missing the no-op line:\n%s", out)
	}
	if !strings.Contains(out, "Nothing to propagate.") {
		t.Fatalf("missing the trailing line:\n%s", out)
	}
	if internal.HasCheckoutTransaction(fp) || internal.HasCheckoutLock(fp) {
		t.Fatal("an empty plan releases the lock and creates no transaction")
	}
}

func TestCheckoutSyncModes_PreflightRefusesBeforeTheLock(t *testing.T) {
	dir, fp := checkoutModeFixture(t)

	cases := []struct {
		name   string
		policy internal.SyncRunPolicy
		stack  []internal.StackEntry
		want   string
	}{
		{
			"I10",
			internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeOne, Selector: "nope"},
			nil,
			`unknown stack entry "nope" in feature "test-feature"; run: tws stack status test-feature`,
		},
		{
			"I11",
			internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeOne, Selector: "feat-b"},
			[]internal.StackEntry{{Name: "feat-root", Base: "main"}, {Name: "feat-b", Base: "feat-root", Archived: true}},
			`stack entry "feat-b" is archived; restore it with: tws new test-feature feat-b`,
		},
		{
			"I12",
			internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll},
			[]internal.StackEntry{{Name: "feat-root", Base: "main"}, {Name: "other", Base: "feat-root", Repo: "/elsewhere"}},
			`stack entry "other" belongs to repository "/elsewhere"; checkout sync is single-repository (cross-repo-unsupported)`,
		},
		{
			"I13",
			internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll},
			[]internal.StackEntry{{Name: "feat-root", Base: "main"}, {Name: "dup", Base: "feat-root", Branch: "feat-a"}, {Name: "dup2", Base: "feat-root", Branch: "feat-a"}},
			`share Git branch "feat-a"; select one of them with --only`,
		},
		{
			"I14",
			internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeOne, Selector: "feat-a"},
			[]internal.StackEntry{{Name: "feat-root", Base: "main"}, {Name: "feat-a", Base: "ghost"}},
			`base "ghost" for stack entry "feat-a" does not resolve locally; drop --no-fetch or fetch manually first`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.stack != nil {
				saveTestStack(t, fp, tc.stack)
			}
			_, err := captureRun(t, func() error {
				return internal.RunCheckoutSync(newModeOpts(dir, fp, tc.policy, "only", "no-fetch"))
			})
			if err == nil {
				t.Fatal("the preflight must refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, want %q", err.Error(), tc.want)
			}
			if internal.HasCheckoutLock(fp) || internal.HasCheckoutTransaction(fp) {
				t.Fatal("a preflight refusal must leave no lock and no transaction")
			}
		})
	}
}

func TestCheckoutSyncModes_I9MissingStack(t *testing.T) {
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)
	clearStepHook(t)
	policy := internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll}
	_, err := captureRun(t, func() error {
		return internal.RunCheckoutSync(newModeOpts(dir, fp, policy, "no-fetch"))
	})
	want := `sync modes require a stack; feature "test-feature" has no readable stack.yaml`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	if internal.HasCheckoutLock(fp) {
		t.Fatal("I9 must precede the lock")
	}
}

func TestCheckoutSyncModes_NoFlagBrokenStackStillLocksFirst(t *testing.T) {
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)
	clearStepHook(t)
	if err := os.WriteFile(filepath.Join(fp, "stack.yaml"), []byte("branches: [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := captureRun(t, func() error {
		return internal.RunCheckoutSync(internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir})
	})
	if err == nil || !strings.Contains(err.Error(), "load stack:") {
		t.Fatalf("the frozen path keeps today's wrapper; got %v", err)
	}
}

func TestCheckoutSyncModes_ContinueMismatchAndVersionRules(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	clearStepHook(t)
	internal.StepHook = func(stage internal.CheckoutStage, i int) error {
		if stage == internal.StageRebased {
			return errStop
		}
		return nil
	}
	policy := internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeSubtree, Selector: "feat-a"}
	_, _ = captureRun(t, func() error {
		return internal.RunCheckoutSync(newModeOpts(dir, fp, policy, "from"))
	})
	clearStepHook(t)

	// Rule 3: a conflicting axis is refused before any Git call.
	conflicting := newModeOpts(dir, fp, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchEnabled, Propagation: internal.SyncPropagationFull,
		ScopeKind: internal.SyncScopeSubtree, Selector: "feat-a",
	}, "fetch")
	conflicting.Continue = true
	_, err := captureRun(t, func() error { return internal.ContinueCheckoutSync(conflicting) })
	want := "cannot change fetch on --continue: the run was started with fetch=no-fetch and this invocation requests fetch"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}

	// Rule 5: the symmetric push rule applies to v2 transactions.
	pushOff := newModeOpts(dir, fp, policy, "push")
	pushOff.Continue = true
	pushOff.Push = true
	_, err = captureRun(t, func() error { return internal.ContinueCheckoutSync(pushOff) })
	if err == nil || !strings.Contains(err.Error(), "cannot change push on --continue") {
		t.Fatalf("v2 push mismatch: %v", err)
	}
}

func TestCheckoutSyncModes_ContinueWithCorruptStackFailsCleanly(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	clearStepHook(t)
	internal.StepHook = func(stage internal.CheckoutStage, i int) error {
		if stage == internal.StageRebased {
			return errStop
		}
		return nil
	}
	policy := internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeSubtree, Selector: "feat-a"}
	_, _ = captureRun(t, func() error {
		return internal.RunCheckoutSync(newModeOpts(dir, fp, policy, "from"))
	})
	clearStepHook(t)

	tx, err := internal.LoadCheckoutTransaction(fp)
	if err != nil {
		t.Fatalf("the interrupted transaction must be on disk: %v", err)
	}
	if len(tx.Selected) == 0 {
		t.Fatal("the fixture must have persisted a selection")
	}
	if err := os.WriteFile(filepath.Join(fp, "stack.yaml"), []byte("branches: [oops\n\t- broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := newModeOpts(dir, fp, policy, "from")
	opts.Continue = true
	lockPath := internal.CheckoutLockPath(fp)
	lockBefore, lockErr := os.ReadFile(lockPath)
	_, err = captureRun(t, func() error { return internal.ContinueCheckoutSync(opts) })
	if err == nil {
		t.Fatal("a v2 resume cannot verify its selection without the stack and must refuse")
	}
	if !strings.Contains(err.Error(), "load stack:") {
		t.Fatalf("err = %v, want a clean load-stack error", err)
	}
	// The refusal happens before the lock is reclaimed and changes nothing.
	lockAfter, lockErrAfter := os.ReadFile(lockPath)
	if (lockErr == nil) != (lockErrAfter == nil) || string(lockBefore) != string(lockAfter) {
		t.Fatal("a pre-flight refusal must not reclaim the lock")
	}
	if !internal.HasCheckoutTransaction(fp) {
		t.Fatal("a refusal must leave the transaction in place")
	}
}

func TestCheckoutSyncModes_LegacyTransactionKeepsOneWayPushRule(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	clearStepHook(t)
	internal.StepHook = func(stage internal.CheckoutStage, i int) error {
		if stage == internal.StageRebased {
			return errStop
		}
		return nil
	}
	_, _ = captureRun(t, func() error {
		return internal.RunCheckoutSync(internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir})
	})
	clearStepHook(t)

	opts := internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir, Push: true, Continue: true}
	_, err := captureRun(t, func() error { return internal.ContinueCheckoutSync(opts) })
	if err == nil || !strings.Contains(err.Error(), "cannot add --push to an existing transaction that was started without it") {
		t.Fatalf("a legacy transaction keeps today's one-way push rule; got %v", err)
	}
}

func TestCheckoutSyncModes_DeferredI7OnAbort(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	clearStepHook(t)
	internal.StepHook = func(stage internal.CheckoutStage, i int) error {
		if stage == internal.StageRebased {
			return errStop
		}
		return nil
	}
	policy := internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll}
	_, _ = captureRun(t, func() error {
		return internal.RunCheckoutSync(newModeOpts(dir, fp, policy, "no-fetch"))
	})
	clearStepHook(t)

	opts := internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir, Continue: true, Abort: true}
	_, err := captureRun(t, func() error { return internal.AbortCheckoutSync(opts) })
	if err == nil || err.Error() != "--continue and --abort are mutually exclusive" {
		t.Fatalf("deferred I7 must fire on a v2 transaction; got %v", err)
	}
}

func TestCheckoutSyncModes_DeferredI7NotOnLegacyTransaction(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	clearStepHook(t)
	internal.StepHook = func(stage internal.CheckoutStage, i int) error {
		if stage == internal.StageRebased {
			return errStop
		}
		return nil
	}
	_, _ = captureRun(t, func() error {
		return internal.RunCheckoutSync(internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir})
	})
	clearStepHook(t)

	opts := internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir, Continue: true, Abort: true}
	_, err := captureRun(t, func() error { return internal.AbortCheckoutSync(opts) })
	if err != nil {
		t.Fatalf("with a legacy transaction --abort wins and --continue is ignored; got %v", err)
	}
	if internal.HasCheckoutTransaction(fp) {
		t.Fatal("the abort must have completed")
	}
}

// TestCheckoutSyncModes_DuplicateBranchAttribution pins C3: LastBaseSHA is
// attributed by logical Name, on the no-flag path too.
func TestCheckoutSyncModes_DuplicateBranchAttribution(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "feat-root", Base: "main"},
		{Name: "one", Base: "feat-root", Branch: "feat-a"},
		{Name: "two", Base: "feat-root", Branch: "feat-a"},
	})
	_, err := captureRun(t, func() error {
		return internal.RunCheckoutSync(internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir})
	})
	if err != nil {
		t.Fatalf("no-flag duplicate-branch run: %v", err)
	}
	stack, err := internal.LoadStack(fp)
	if err != nil {
		t.Fatal(err)
	}
	var one, two internal.StackEntry
	for _, e := range stack.Branches {
		switch e.Name {
		case "one":
			one = e
		case "two":
			two = e
		}
	}
	if one.LastBaseSHA == "" || two.LastBaseSHA == "" {
		t.Fatalf("both duplicate entries must be attributed: one=%q two=%q", one.LastBaseSHA, two.LastBaseSHA)
	}
}

func TestCheckoutSyncModes_OldTransactionWithoutNamesFallsBack(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	tx := &internal.CheckoutTransaction{
		Feature:        "test-feature",
		OriginalBranch: "main",
		OriginalHEAD:   gitSHA(t, dir, "main"),
		Plan: []internal.CheckoutPlanEntry{{
			Branch:     "feat-a",
			Base:       "feat-root",
			NewBaseSHA: gitSHA(t, dir, "feat-root"),
			PreSHA:     gitSHA(t, dir, "feat-a"),
		}},
		Stage: internal.StagePlanned,
	}
	if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatal(err)
	}
	loaded, err := internal.LoadCheckoutTransaction(fp)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.StateVersion != 0 {
		t.Fatalf("an absent state_version means legacy; got %d", loaded.StateVersion)
	}
	if loaded.Plan[0].Name != "" {
		t.Fatal("an old plan entry has no name and must use the GitBranch fallback")
	}
	internal.DeleteCheckoutTransaction(fp)
}

func TestCheckoutSyncModes_NewerTransactionVersionRefused(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	_ = dir
	tx := &internal.CheckoutTransaction{StateVersion: 99, Feature: "test-feature", Stage: internal.StagePlanned}
	if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatal(err)
	}
	opts := internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir, Continue: true}
	_, err := captureRun(t, func() error { return internal.ContinueCheckoutSync(opts) })
	if err == nil || !strings.Contains(err.Error(), "is newer than") {
		t.Fatalf("forward-only protection: %v", err)
	}
	internal.DeleteCheckoutTransaction(fp)
}

type stopErr struct{}

func (stopErr) Error() string { return "test stop" }

var errStop = stopErr{}

// ---------------------------------------------------------------------------
// Guarded checkout DISPATCH (internal/cli/checkout_sync.go's runCheckoutSync,
// reached only through production cli.Execute() — unlike every test above,
// which calls internal.RunCheckoutSync/ContinueCheckoutSync directly and so
// never touches the CLI dispatch layer at all) and unguarded controls
// proving InspectCheckoutPlan runs on the --plan route and on a guarded real
// execution (internal.RunCheckoutSync's guard seam calls InspectCheckoutPlan
// above AcquireCheckoutLock precisely so a sort/stack error refuses before
// any lock is taken), but never on an unguarded real execution.
// ---------------------------------------------------------------------------

// TestCheckoutSyncModes_GuardedDispatchPlanRouteNeverExecutes confirms the
// CLI dispatch's own --plan branch, armed with the same guard limits a real
// guarded execution would carry, never reaches internal.RunCheckoutSync: no
// lock, no transaction, document-only output.
func TestCheckoutSyncModes_GuardedDispatchPlanRouteNeverExecutes(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)

	stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch", "--max-replay-total", "0", "--max-replay-per-entry", "0")
	if exit != 0 {
		t.Fatalf("--plan always exits 0 regardless of would_refuse: exit=%d stderr=%q", exit, stderr)
	}
	if internal.HasCheckoutLock(fp) || internal.HasCheckoutTransaction(fp) {
		t.Fatal("a --plan dispatch, even with limits armed, must never acquire the lock or create a transaction")
	}
	doc := planDoc(t, stdout)
	if _, ok := doc["entries"]; !ok {
		t.Fatalf("expected a plan document with an entries field on stdout, got:\n%s", stdout)
	}
}

// TestCheckoutSyncModes_GuardedDispatchRealExecutionPersistsGuardedTransaction
// pins the fixed behavior of a guarded (armed limits, within bounds — the
// fixture's stack has never been synced, so every entry's own replay
// candidate count is 0, and a limit of 0 is not exceeded) FRESH checkout
// dispatch that is NOT --plan: it reaches internal.RunCheckoutSync exactly
// like an unguarded run (same argv, same StepHook interruption point), but
// — since internal/checkout_sync.go's fresh-run body now calls
// internal.EvaluatePlanGuard through the guard seam placed after the plan
// is built and before SaveCheckoutTransaction — it persists a
// state_version: 3 transaction carrying its route and both limit pointers,
// rather than the plain, un-upgraded v2 an unguarded dispatch still writes.
func TestCheckoutSyncModes_GuardedDispatchRealExecutionPersistsGuardedTransaction(t *testing.T) {
	dir, fp := checkoutModeFixture(t)
	clearStepHook(t)
	internal.StepHook = func(stage internal.CheckoutStage, branchIndex int) error {
		if stage == internal.StagePlanned {
			return errStop
		}
		return nil
	}
	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)

	_, stderr, exit := runSyncExecute(t, "test-feature", "--no-fetch", "--max-replay-total", "0", "--max-replay-per-entry", "0")
	clearStepHook(t)
	if exit == 0 {
		t.Fatal("the injected step-hook error must have stopped the run before completion")
	}
	if strings.Contains(stderr, "plan-guard: ") {
		t.Fatalf("a guard seam that did not refuse (0 candidates never exceeds a limit of 0) must never print a plan-guard marker, got stderr:\n%s", stderr)
	}

	tx, err := internal.LoadCheckoutTransaction(fp)
	if err != nil {
		t.Fatalf("the interrupted run must have persisted a transaction: %v", err)
	}
	if tx.StateVersion != internal.CheckoutTransactionGuardedVersion {
		t.Fatalf("state_version = %d, want the guarded v3 (%d): a guarded fresh dispatch now writes its birth version",
			tx.StateVersion, internal.CheckoutTransactionGuardedVersion)
	}
	if tx.Route != internal.RouteNewMode {
		t.Fatalf("route = %q, want %q: this dispatch supplied --no-fetch, a new-mode trigger flag", tx.Route, internal.RouteNewMode)
	}
	if tx.MaxReplayPerEntry == nil || *tx.MaxReplayPerEntry != 0 {
		t.Fatalf("MaxReplayPerEntry = %v, want a pointer to 0: the armed CLI flag must now be persisted", tx.MaxReplayPerEntry)
	}
	if tx.MaxReplayTotal == nil || *tx.MaxReplayTotal != 0 {
		t.Fatalf("MaxReplayTotal = %v, want a pointer to 0: the armed CLI flag must now be persisted", tx.MaxReplayTotal)
	}
}

// checkoutSawGitVersionProbe drives production cli.Execute() with the given
// trailing args around a real git-invocation recorder and reports whether
// any recorded argv contains "--version" — internal.ProbeGitCapabilities'
// own signature call (internal.ProbeGitVersion's exec.Command("git",
// "--version")), which nothing else in a checkout sync ever issues, and
// which InspectCheckoutPlan is checkout mode's only caller of. Its presence
// or absence is therefore a clean, decisive signal for whether
// InspectCheckoutPlan ran.
func checkoutSawGitVersionProbe(t *testing.T, dir string, args ...string) bool {
	t.Helper()
	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)
	w := newSyncGitWrapper(t, false)
	w.around(t, func() {
		runSyncExecute(t, append([]string{"test-feature"}, args...)...)
	})
	for _, r := range w.records(t) {
		for _, a := range r.Argv {
			if a == "--version" {
				return true
			}
		}
	}
	return false
}

// TestCheckoutSyncModes_UnguardedRouteNeverCallsInspector is the required
// unguarded control: the --plan route calls InspectCheckoutPlan (observed via
// its git-version probe), but a plain, unguarded real execution over the
// identical fixture never does.
func TestCheckoutSyncModes_UnguardedRouteNeverCallsInspector(t *testing.T) {
	t.Run("plan route calls the inspector", func(t *testing.T) {
		dir, _ := checkoutModeFixture(t)
		if !checkoutSawGitVersionProbe(t, dir, "--plan", "--no-fetch") {
			t.Fatal("expected InspectCheckoutPlan's git --version probe on the --plan route")
		}
	})

	t.Run("unguarded real execution never calls the inspector", func(t *testing.T) {
		dir, _ := checkoutModeFixture(t)
		if checkoutSawGitVersionProbe(t, dir, "--no-fetch") {
			t.Fatal("a plain, unguarded real execution must never invoke InspectCheckoutPlan/ProbeGitCapabilities")
		}
	})
}

// ---------------------------------------------------------------------------
// §22.33i (i)-(iv) — the three probed restore cells, driven through
// production cli.Execute() over a REAL foreign holder.
// ---------------------------------------------------------------------------

// restoreDoc reads one checkout --plan --json document's `restore` object.
func restoreDoc(t *testing.T, stdout string) map[string]any {
	t.Helper()
	doc := planDoc(t, stdout)
	restore, ok := doc["restore"].(map[string]any)
	if !ok {
		t.Fatalf("restore = %#v, want an object", doc["restore"])
	}
	return restore
}

// restoreBlockedTokens projects restore.blocked_by[] into a []string.
func restoreBlockedTokens(t *testing.T, restore map[string]any) []string {
	t.Helper()
	raw, ok := restore["blocked_by"].([]any)
	if !ok {
		t.Fatalf("restore.blocked_by = %#v, want a never-null array", restore["blocked_by"])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

// saveRestoreTransaction persists a real conflict-stage checkout transaction
// whose OriginalBranch is target, so the described continuation's restore
// target is that branch (§14.4's `transaction` target_source) rather than
// whatever branch the repository happens to have checked out.
func saveRestoreTransaction(t *testing.T, dir, fp, target string, stage internal.CheckoutStage) {
	t.Helper()
	tx := &internal.CheckoutTransaction{
		Feature:        "test-feature",
		OriginalBranch: target,
		OriginalHEAD:   gitSHA(t, dir, target),
		Stage:          stage,
		Plan: []internal.CheckoutPlanEntry{
			{Name: "feat-a", Branch: "feat-a", Base: "feat-root", NewBaseSHA: gitSHA(t, dir, "feat-root")},
		},
	}
	if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { internal.DeleteCheckoutTransaction(fp) })
}

// TestCheckoutSyncModes_RestoreTargetHeldProjection is §22.33i (i)-(iv):
// the `restore-target-held` cell, from the holder index through the rank 4.5
// blocker to the document's own refusal, driven through production
// cli.Execute() over a REAL foreign linked worktree.
//
// Four cells are asserted, and they are the four the spec separates:
//
//	(i)   a FOREIGN holder emits the token, sets restore.executable false,
//	      publishes a document-level (entry: null) rank 4.5 restore-blocked
//	      blocker, and that blocker is the document's own non-waivable
//	      refusal;
//	(ii)  SELF-exclusion: the very same shape, where the only holder is the
//	      checkout repository itself, emits NOTHING — blocked_by is [] and
//	      executable is true, because the run's own checkout is excluded by
//	      the canonical-path comparison, not by omission;
//	(iii) a `completed`-stage document asks no restore-target question at
//	      all: none of the three tokens may appear even with the same
//	      foreign holder in place;
//	(iv)  an UNAVAILABLE holder inventory is §14.4's probe-failure cell —
//	      executable null with blocked_by [] — and never an empty map read
//	      as "not held".
func TestCheckoutSyncModes_RestoreTargetHeldProjection(t *testing.T) {
	t.Run("foreign_holder_blocks_and_refuses", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		foreign := filepath.Join(t.TempDir(), "foreign")
		gitRunCS(t, dir, "worktree", "add", foreign, "feat-b")
		saveRestoreTransaction(t, dir, fp, "feat-b", internal.StageConflict)

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		restore := restoreDoc(t, stdout)
		if restore["applies"] != true {
			t.Fatalf("restore.applies = %v, want true", restore["applies"])
		}
		if restore["target_source"] != "transaction" {
			t.Fatalf("restore.target_source = %v, want transaction", restore["target_source"])
		}
		if restore["target_branch"] != "feat-b" {
			t.Fatalf("restore.target_branch = %v, want feat-b", restore["target_branch"])
		}
		if got := restoreBlockedTokens(t, restore); !slices.Contains(got, "restore-target-held") {
			t.Fatalf("restore.blocked_by = %v, want it to contain restore-target-held (a foreign worktree really holds feat-b)", got)
		}
		if restore["executable"] != false {
			t.Fatalf("restore.executable = %v, want false", restore["executable"])
		}

		doc := planDoc(t, stdout)
		blockers, _ := doc["blockers"].([]any)
		found := false
		for _, raw := range blockers {
			b := raw.(map[string]any)
			if b["kind"] != "restore-blocked" {
				continue
			}
			found = true
			if b["entry"] != nil {
				t.Fatalf("the restore-blocked blocker must be document-level (entry: null), got entry=%v", b["entry"])
			}
			if detail, _ := b["detail"].(string); !strings.Contains(detail, "checked out in another worktree") {
				t.Fatalf("blocker detail = %q, want the held-target sentence", detail)
			}
		}
		if !found {
			t.Fatalf("no document-level restore-blocked blocker in %v", blockers)
		}
		refusal, _ := doc["refusal"].(map[string]any)
		if refusal == nil || refusal["kind"] != "restore-blocked" {
			t.Fatalf("refusal = %#v, want the rank 4.5 restore-blocked refusal", doc["refusal"])
		}
		if doc["runnable"] != false {
			t.Fatalf("runnable = %v, want false under a rank 4.5 refusal", doc["runnable"])
		}

		// NON-WAIVABLE, proved by execution rather than by a schema field.
		// The only waiver this feature has is an approval token, and a
		// refusing document mints none at all: approval.usable is false, so
		// there is nothing to present. An ARMED guarded continuation — the
		// invocation that would carry such a token — therefore still refuses
		// with the same kind, and `restore-blocked` is never a member of
		// guard.waived_kinds (whose domain is exactly the two limit kinds).
		approval, _ := doc["approval"].(map[string]any)
		if approval == nil {
			t.Fatalf("approval = %#v, want an object", doc["approval"])
		}
		if approval["usable"] != false {
			t.Fatalf("approval.usable = %v, want false: a rank 4.5 refusal mints no waivable token", approval["usable"])
		}
		if approval["fingerprint"] != nil {
			t.Fatalf("approval.fingerprint = %v, want null on a refusing document", approval["fingerprint"])
		}
		_, armedStderr, armedExit := runSyncExecute(t, "test-feature", "--continue", "--max-replay-total", "50")
		if armedExit != 1 {
			t.Fatalf("an armed guarded continuation must still refuse rank 4.5 restore-blocked: exit=%d stderr=%q", armedExit, armedStderr)
		}
		if !strings.Contains(armedStderr, "restore-blocked") {
			t.Fatalf("stderr = %q, want the restore-blocked refusal to survive arming", armedStderr)
		}
		guardBlock, _ := doc["guard"].(map[string]any)
		if guardBlock == nil {
			t.Fatalf("guard = %#v, want an object", doc["guard"])
		}
		waived, _ := guardBlock["waived_kinds"].([]any)
		for _, w := range waived {
			if w == "restore-blocked" {
				t.Fatal("restore-blocked must never be a member of guard.waived_kinds")
			}
		}
	})

	t.Run("self_holder_is_excluded_not_omitted", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		// No foreign worktree at all: the checkout repository itself is the
		// only holder of its own checked-out branch.
		gitRunCS(t, dir, "checkout", "feat-b")
		saveRestoreTransaction(t, dir, fp, "feat-b", internal.StageConflict)

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		restore := restoreDoc(t, stdout)
		if got := restoreBlockedTokens(t, restore); slices.Contains(got, "restore-target-held") {
			t.Fatalf("restore.blocked_by = %v, want NO restore-target-held: the run's OWN checkout is excluded by the canonical-path comparison", got)
		}
		if restore["executable"] != true {
			t.Fatalf("restore.executable = %v, want true (all three cells were evaluated and none held)", restore["executable"])
		}
		if got := restoreBlockedTokens(t, restore); len(got) != 0 {
			t.Fatalf("restore.blocked_by = %v, want [] because all three were EVALUATED and none held", got)
		}
	})

	t.Run("completed_stage_asks_no_restore_target_question", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		foreign := filepath.Join(t.TempDir(), "foreign")
		gitRunCS(t, dir, "worktree", "add", foreign, "feat-b")
		saveRestoreTransaction(t, dir, fp, "feat-b", internal.StageCompleted)

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		restore := restoreDoc(t, stdout)
		if restore["applies"] != true {
			t.Fatalf("restore.applies = %v, want true (a completed stage still finalizes)", restore["applies"])
		}
		if restore["will_switch_head"] != false {
			t.Fatalf("restore.will_switch_head = %v, want false: a completed-stage resume runs no git checkout", restore["will_switch_head"])
		}
		for _, token := range restoreBlockedTokens(t, restore) {
			for _, targetToken := range []string{"restore-target-held", "restore-target-missing", "restore-head-moved"} {
				if token == targetToken {
					t.Fatalf("a completed-stage document must never carry %q: the restore-target question does not arise there", targetToken)
				}
			}
		}
	})

	t.Run("unavailable_holder_inventory_is_the_probe_failure_cell", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		saveRestoreTransaction(t, dir, fp, "feat-b", internal.StageConflict)

		// A `git worktree list --porcelain` that cannot answer makes the
		// inventory UNAVAILABLE. §14.4 requires that to be published as the
		// probe-failure cell, never read as an empty "not held" map.
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Skipf("need a real git on PATH: %v", err)
		}
		stubDir := t.TempDir()
		script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"worktree\" ]; then\n    echo 'fatal: stubbed worktree failure' >&2\n    exit 128\n  fi\ndone\nexec " + realGit + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(stubDir, "git"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		restore := restoreDoc(t, stdout)
		if restore["executable"] != nil {
			t.Fatalf("restore.executable = %v, want null: an unmeasured holder inventory is the probe-failure cell", restore["executable"])
		}
		if got := restoreBlockedTokens(t, restore); len(got) != 0 {
			t.Fatalf("restore.blocked_by = %v, want [] in the probe-failure cell (§4.7's declared exception)", got)
		}
	})
}

// ===========================================================================
// §22.23 — a guarded continuation inherits persisted limits; a conflicting
// supplied limit is rank 7, keeps the persisted value effective, and still
// mints a token.
// ===========================================================================

// saveGuardedCheckoutTransaction persists a real guarded (v3) checkout
// transaction parked at `conflict` on the second row, carrying the limits
// given, so a later `--continue` really inherits them.
func saveGuardedCheckoutTransaction(t *testing.T, dir, fp string, perEntry, total *int) {
	t.Helper()
	tx := &internal.CheckoutTransaction{
		StateVersion:      internal.CheckoutTransactionGuardedVersion,
		Route:             internal.RouteNewMode,
		Feature:           "test-feature",
		OriginalBranch:    "main",
		OriginalHEAD:      gitSHA(t, dir, "main"),
		Stage:             internal.StageConflict,
		MaxReplayPerEntry: perEntry,
		MaxReplayTotal:    total,
		Plan: []internal.CheckoutPlanEntry{
			{Name: "feat-a", Branch: "feat-a", Base: "feat-root", NewBaseSHA: gitSHA(t, dir, "feat-root")},
		},
	}
	if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { internal.DeleteCheckoutTransaction(fp) })
}

// TestCheckoutSyncModes_Criterion22_23_ContinuationInheritsPersistedLimits is
// §22.23's executable owner, driven through production cli.Execute():
//
//	(a) a FLAGLESS guarded `--continue` inherits the persisted limits, which
//	    are published with `origin: persisted-transaction` and no conflict;
//	(b) a CONFLICTING supplied limit publishes a rank 7
//	    `guard-limit-mismatch` blocker naming the key, a
//	    `guard.limit_conflicts[]` row whose effective value is the PERSISTED
//	    one and whose supplied value is the flag's, keeps the persisted value
//	    in `guard.limits`, and STILL mints a usable token over those values;
//	(c) an AGREEING supplied limit is not a conflict at all.
func TestCheckoutSyncModes_Criterion22_23_ContinuationInheritsPersistedLimits(t *testing.T) {
	persisted := 7
	limitsOf := func(stdout string) (perEntry, total map[string]any) {
		t.Helper()
		guard, _ := planDoc(t, stdout)["guard"].(map[string]any)
		if guard == nil {
			t.Fatalf("guard = %#v, want an object", planDoc(t, stdout)["guard"])
		}
		limits, _ := guard["limits"].(map[string]any)
		if limits == nil {
			t.Fatalf("guard.limits = %#v, want an object", guard["limits"])
		}
		perEntry, _ = limits["max_replay_per_entry"].(map[string]any)
		total, _ = limits["max_replay_total"].(map[string]any)
		return perEntry, total
	}

	t.Run("flagless_continuation_inherits_the_persisted_limit", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		saveGuardedCheckoutTransaction(t, dir, fp, nil, &persisted)

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		_, total := limitsOf(stdout)
		if total["value"] != float64(persisted) {
			t.Fatalf("guard.limits.max_replay_total.value = %v, want the persisted %d", total["value"], persisted)
		}
		if total["origin"] != "persisted-transaction" {
			t.Fatalf("origin = %v, want persisted-transaction", total["origin"])
		}
		guard, _ := planDoc(t, stdout)["guard"].(map[string]any)
		if rows, _ := guard["limit_conflicts"].([]any); len(rows) != 0 {
			t.Fatalf("guard.limit_conflicts = %#v, want [] when nothing was supplied", guard["limit_conflicts"])
		}
	})

	t.Run("conflicting_supplied_limit_is_rank_7_and_still_mints", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		saveGuardedCheckoutTransaction(t, dir, fp, nil, &persisted)

		const supplied = 99
		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json",
			"--max-replay-total", strconv.Itoa(supplied))
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)

		// The persisted value stays effective.
		_, total := limitsOf(stdout)
		if total["value"] != float64(persisted) {
			t.Fatalf("guard.limits.max_replay_total.value = %v, want the PERSISTED %d to stay effective", total["value"], persisted)
		}
		if total["origin"] != "persisted-transaction" {
			t.Fatalf("origin = %v, want persisted-transaction", total["origin"])
		}

		// The conflict row names the key and both values.
		guard, _ := doc["guard"].(map[string]any)
		rows, _ := guard["limit_conflicts"].([]any)
		if len(rows) != 1 {
			t.Fatalf("guard.limit_conflicts = %#v, want exactly one row", guard["limit_conflicts"])
		}
		row := rows[0].(map[string]any)
		if row["key"] != "max_replay_total" {
			t.Fatalf("limit_conflicts[0].key = %v, want max_replay_total", row["key"])
		}
		if row["effective_value"] != float64(persisted) {
			t.Fatalf("effective_value = %v, want %d", row["effective_value"], persisted)
		}
		if row["effective_origin"] != "persisted-transaction" {
			t.Fatalf("effective_origin = %v, want persisted-transaction", row["effective_origin"])
		}
		if row["supplied_value"] != float64(supplied) {
			t.Fatalf("supplied_value = %v, want %d", row["supplied_value"], supplied)
		}

		// Rank 7 blocker, document-level, naming the key.
		blockers, _ := doc["blockers"].([]any)
		found := false
		for _, raw := range blockers {
			b := raw.(map[string]any)
			if b["kind"] != "guard-limit-mismatch" {
				continue
			}
			found = true
			if b["entry"] != nil {
				t.Fatalf("guard-limit-mismatch must be document-level, got entry=%v", b["entry"])
			}
			detail, _ := b["detail"].(string)
			if !strings.Contains(detail, "max_replay_total") {
				t.Fatalf("detail = %q, want it to name the key", detail)
			}
			if !strings.Contains(detail, strconv.Itoa(persisted)) || !strings.Contains(detail, strconv.Itoa(supplied)) {
				t.Fatalf("detail = %q, want both the persisted and the supplied value", detail)
			}
		}
		if !found {
			t.Fatalf("no rank 7 guard-limit-mismatch blocker in %v", blockers)
		}

		// And it STILL mints a token over those values.
		approval, _ := doc["approval"].(map[string]any)
		fp2, _ := approval["fingerprint"].(string)
		if len(fp2) != 64 {
			t.Fatalf("approval.fingerprint = %v, want a minted token: rank 7 is not a hard refusal", approval["fingerprint"])
		}
		if approval["usable"] != true {
			t.Fatalf("approval.usable = %v, want true", approval["usable"])
		}
	})

	t.Run("agreeing_supplied_limit_is_not_a_conflict", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		saveGuardedCheckoutTransaction(t, dir, fp, nil, &persisted)

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json",
			"--max-replay-total", strconv.Itoa(persisted))
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		guard, _ := planDoc(t, stdout)["guard"].(map[string]any)
		if rows, _ := guard["limit_conflicts"].([]any); len(rows) != 0 {
			t.Fatalf("guard.limit_conflicts = %#v, want [] when the supplied value agrees", guard["limit_conflicts"])
		}
	})
}

// ===========================================================================
// §22.25b / §22.25c — the `switched` stage's pinned-destination arm and its
// HEAD-identity rule (§13.3a).
// ===========================================================================

// switchedFixture parks a real checkout transaction at `Stage: switched` on
// an `onto` row whose `entry.Base` ref has SINCE been moved to a different
// commit, so a plan that re-resolved the ref would publish a different
// destination than the executor will really use. headOn names the branch HEAD
// is left on; "" leaves HEAD detached.
func switchedFixture(t *testing.T, headOn string, plain bool) (dir, featurePath, pinned, last string) {
	t.Helper()
	dir, featurePath = checkoutModeFixture(t)
	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)

	pinned = gitSHA(t, dir, "feat-root")
	last = gitSHA(t, dir, "main")

	// The base ref moves after the transaction pinned it.
	gitRunCS(t, dir, "checkout", "feat-root")
	writeFileCS(t, dir, "moved.txt", "moved\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "base moved after the pin")
	if moved := gitSHA(t, dir, "feat-root"); moved == pinned {
		t.Fatal("fixture is vacuous: feat-root did not move")
	}

	if headOn == "" {
		gitRunCS(t, dir, "checkout", "--detach", "main")
	} else {
		gitRunCS(t, dir, "checkout", headOn)
	}

	row := internal.CheckoutPlanEntry{
		Name: "feat-a", Branch: "feat-a", Base: "feat-root", NewBaseSHA: pinned, LastBaseSHA: last,
	}
	if plain {
		row.LastBaseSHA = "" // the plain-arm twin
	}
	tx := &internal.CheckoutTransaction{
		Feature: "test-feature", OriginalBranch: "main", OriginalHEAD: gitSHA(t, dir, "main"),
		Stage: internal.StageSwitched, CurrentIndex: 0,
		Plan: []internal.CheckoutPlanEntry{row},
	}
	if err := internal.SaveCheckoutTransaction(featurePath, tx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { internal.DeleteCheckoutTransaction(featurePath) })
	return dir, featurePath, pinned, last
}

// switchedRow returns the `feat-a` row of a checkout --plan --json document.
func switchedRow(t *testing.T, stdout string) map[string]any {
	t.Helper()
	for _, e := range planDoc(t, stdout)["entries"].([]any) {
		row := e.(map[string]any)
		if row["name"] == "feat-a" {
			return row
		}
	}
	t.Fatalf("no feat-a row in the document:\n%s", stdout)
	return nil
}

// TestCheckoutSyncModes_Criterion22_25b_SwitchedIsAPinnedDestinationArm is
// §22.25b's executable owner.
func TestCheckoutSyncModes_Criterion22_25b_SwitchedIsAPinnedDestinationArm(t *testing.T) {
	t.Run("onto_arm_publishes_the_persisted_pair_and_probes_once", func(t *testing.T) {
		dir, _, pinned, last := switchedFixture(t, "feat-a", false)
		w := newSyncGitWrapper(t, false)
		var stdout, stderr string
		var exit int
		w.around(t, func() {
			stdout, stderr, exit = runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		})
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		row := switchedRow(t, stdout)
		destination := row["destination"].(map[string]any)
		if destination["sha"] != pinned {
			t.Fatalf("destination.sha = %v, want the PERSISTED NewBaseSHA %s (never the ref's current resolution %s)",
				destination["sha"], pinned, gitSHA(t, dir, "feat-root"))
		}
		replay := row["replay"].(map[string]any)
		if replay["upstream_sha"] != last {
			t.Fatalf("replay.upstream_sha = %v, want the PERSISTED LastBaseSHA %s — the recorded cutoff, never the destination",
				replay["upstream_sha"], last)
		}
		if replay["range"] != last+"..feat-a" {
			t.Fatalf("replay.range = %v, want %q (spelled over the persisted upstream)", replay["range"], last+"..feat-a")
		}
		mutation := row["mutation"].(map[string]any)
		if mutation["will_switch_head"] != false {
			t.Fatalf("mutation.will_switch_head = %v, want false: that arm switches nothing", mutation["will_switch_head"])
		}

		// Probe budget: no rev-parse of entry.Base at all, and exactly one
		// existence probe of the persisted NewBaseSHA.
		baseProbes, pinProbes := 0, 0
		for _, r := range w.records(t) {
			tail := r.Tail()
			// The destination resolution GetBranchSHA issues is the bare
			// two-token form; the stack-wide ancestry pass's own
			// refs/heads/<parent> reads are a different question and are not
			// what §13.3a rule 6 forbids here.
			if len(tail) == 2 && tail[0] == "rev-parse" && tail[1] == "feat-root" {
				baseProbes++
			}
			if len(tail) >= 2 && tail[0] == "rev-parse" && tail[len(tail)-1] == pinned+"^{commit}" {
				pinProbes++
			}
		}
		if baseProbes != 0 {
			t.Fatalf("the switched plan issued %d rev-parse of entry.Base, want 0 (the executor consumes the pinned SHA)", baseProbes)
		}
		if pinProbes != 1 {
			t.Fatalf("the switched plan issued %d `rev-parse --verify <persisted NewBaseSHA>^{commit}` probes, want exactly 1", pinProbes)
		}
	})

	t.Run("guarded_continue_executes_the_pinned_argv_and_matches_the_unguarded_control", func(t *testing.T) {
		// Each capture builds its OWN fixture (a run mutates it), so the two
		// are compared by SHAPE plus each one's own persisted pair, never by
		// raw SHA equality across two independent repositories.
		capture := func(extra ...string) (argv []string, stderr string) {
			t.Helper()
			_, _, pinned, last := switchedFixture(t, "feat-a", false)
			w := newSyncGitWrapper(t, false)
			args := append([]string{"test-feature", "--continue"}, extra...)
			var exit int
			w.around(t, func() { _, stderr, exit = runSyncExecute(t, args...) })
			if exit != 0 {
				t.Fatalf("the continuation must succeed: exit=%d stderr=%q", exit, stderr)
			}
			for _, r := range w.records(t) {
				if r.Verb == "rebase" {
					argv = r.Tail()
					break
				}
			}
			want := []string{"rebase", "--no-fork-point", "--onto", pinned, last}
			if !slices.Equal(argv, want) {
				t.Fatalf("rebase argv = %#v, want %#v (the PERSISTED pair)", argv, want)
			}
			return argv, stderr
		}
		unguarded, unguardedErr := capture()
		guarded, guardedErr := capture("--max-replay-total", "50")

		// Shape equality: same length, same flags, and only the two operands
		// differ (each being its own fixture's persisted pair).
		if len(guarded) != len(unguarded) {
			t.Fatalf("guarded argv %#v and unguarded %#v have different shapes", guarded, unguarded)
		}
		for i := 0; i < 3; i++ {
			if guarded[i] != unguarded[i] {
				t.Fatalf("guarded argv diverged at token %d: %#v vs %#v", i, guarded, unguarded)
			}
		}
		for _, s := range []string{unguardedErr, guardedErr} {
			if strings.Contains(s, "revalidation-mismatch") {
				t.Fatalf("no revalidation-mismatch may be raised on a pinned arm: %q", s)
			}
		}
	})

	t.Run("plain_arm_twin_publishes_the_snapshot_and_a_snapshot_determinacy", func(t *testing.T) {
		dir, _, _, _ := switchedFixture(t, "feat-a", true)
		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		row := switchedRow(t, stdout)
		if row["strategy"] != "plain" {
			t.Fatalf("strategy = %v, want plain when the persisted LastBaseSHA is empty", row["strategy"])
		}
		cutoff := row["cutoff"].(map[string]any)
		if cutoff["usage"] != "not_used" {
			t.Fatalf("cutoff.usage = %v, want not_used on the plain arm", cutoff["usage"])
		}
		replay := row["replay"].(map[string]any)
		if replay["upstream_provenance"] != "base-ref-snapshot" {
			t.Fatalf("replay.upstream_provenance = %v, want base-ref-snapshot: the plain arm's upstream is the base's own resolution", replay["upstream_provenance"])
		}
		_ = dir
	})

	t.Run("planned_stage_control_re_resolves_as_shipped", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		pinned := gitSHA(t, dir, "feat-root")
		gitRunCS(t, dir, "checkout", "feat-root")
		writeFileCS(t, dir, "moved.txt", "moved\n")
		gitRunCS(t, dir, "add", ".")
		gitRunCS(t, dir, "commit", "-m", "moved")
		moved := gitSHA(t, dir, "feat-root")
		gitRunCS(t, dir, "checkout", "feat-a")
		tx := &internal.CheckoutTransaction{
			Feature: "test-feature", OriginalBranch: "main", OriginalHEAD: gitSHA(t, dir, "main"),
			Stage: internal.StagePlanned, CurrentIndex: 0,
			Plan: []internal.CheckoutPlanEntry{
				{Name: "feat-a", Branch: "feat-a", Base: "feat-root", NewBaseSHA: pinned, LastBaseSHA: gitSHA(t, dir, "main")},
			},
		}
		if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { internal.DeleteCheckoutTransaction(fp) })

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		destination := switchedRow(t, stdout)["destination"].(map[string]any)
		if destination["sha"] == pinned {
			t.Fatalf("destination.sha = %v: only the SWITCHED stage pins; a planned-stage row must re-resolve", destination["sha"])
		}
		if destination["snapshot_sha"] != moved {
			t.Fatalf("destination.snapshot_sha = %v, want the FRESHLY re-resolved %s", destination["snapshot_sha"], moved)
		}
	})

	t.Run("destroyed_pinned_object_is_rank_5_7_base_ref_missing", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		last := gitSHA(t, dir, "main")
		// A commit that NOTHING references: pin the transaction to it, then
		// expire every reflog and prune, so the pinned object really is gone
		// by the time the plan runs.
		tree := gitRunCS(t, dir, "rev-parse", "HEAD^{tree}")
		pinned := gitRunCS(t, dir, "commit-tree", tree, "-m", "unreferenced destination")
		gitRunCS(t, dir, "checkout", "feat-a")
		tx := &internal.CheckoutTransaction{
			Feature: "test-feature", OriginalBranch: "main", OriginalHEAD: last,
			Stage: internal.StageSwitched, CurrentIndex: 0,
			Plan: []internal.CheckoutPlanEntry{
				{Name: "feat-a", Branch: "feat-a", Base: "feat-root", NewBaseSHA: pinned, LastBaseSHA: last},
			},
		}
		if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { internal.DeleteCheckoutTransaction(fp) })

		gitRunCS(t, dir, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
		gitRunCS(t, dir, "gc", "--prune=now", "--quiet")
		if out := gitRunCSAllowFail(t, dir, "rev-parse", "--verify", pinned+"^{commit}"); out != "" {
			t.Fatalf("the fixture failed to destroy the pinned object; it still resolves to %s", out)
		}

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		if doc["runnable"] != false {
			t.Fatalf("runnable = %v, want false", doc["runnable"])
		}
		found := false
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			if b["kind"] == "base-ref-missing" {
				found = true
				if b["entry"] != "feat-a" {
					t.Fatalf("base-ref-missing must be on the row, got entry=%v", b["entry"])
				}
			}
			// No NEW refusal kind may appear.
			if b["kind"] == "revalidation-mismatch" {
				t.Fatalf("a destroyed pinned object is rank 5.7, never a revalidation-mismatch: %v", b)
			}
		}
		if !found {
			t.Fatalf("no rank 5.7 base-ref-missing blocker in %v", doc["blockers"])
		}
		replay := switchedRow(t, stdout)["replay"].(map[string]any)
		if replay["upstream_sha"] != last {
			t.Fatalf("replay.upstream_sha = %v, want the persisted LastBaseSHA %s even in the destroyed-object cell", replay["upstream_sha"], last)
		}

		_, stderr, exit = runSyncExecute(t, "test-feature", "--continue", "--max-replay-total", "50")
		if exit != 1 {
			t.Fatalf("the guarded resume must exit 1 before any rebase: exit=%d stderr=%q", exit, stderr)
		}
		if n := len(planGuardMarkerRe.FindAllString(stderr, -1)); n != 1 {
			t.Fatalf("stderr carried %d plan-guard markers, want exactly one:\n%s", n, stderr)
		}
	})
}

// gitRunCSAllowFail runs a git command that is expected to be able to fail,
// returning its trimmed stdout ("" on failure).
func gitRunCSAllowFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestCheckoutSyncModes_Criterion22_25c_SwitchedHeadIdentityIsPlanAndGuardOnly
// is §22.25c's executable owner: §13.3a rule 5 is enforced on the plan and
// guarded routes ONLY, and the shipped unguarded path is unchanged.
func TestCheckoutSyncModes_Criterion22_25c_SwitchedHeadIdentityIsPlanAndGuardOnly(t *testing.T) {
	t.Run("head_on_the_persisted_branch_is_runnable_and_completes", func(t *testing.T) {
		switchedFixture(t, "feat-a", false)
		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		if planDoc(t, stdout)["runnable"] != true {
			t.Fatalf("runnable = %v, want true when HEAD is on the persisted branch", planDoc(t, stdout)["runnable"])
		}
		_, stderr, exit = runSyncExecute(t, "test-feature", "--continue", "--max-replay-total", "50")
		if exit != 0 {
			t.Fatalf("the guarded continuation must complete: exit=%d stderr=%q", exit, stderr)
		}
	})

	for _, tc := range []struct{ name, headOn, want string }{
		{"head_on_a_different_branch", "feat-b", "feat-b"},
		{"detached_head", "", "a detached HEAD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, _, _ := switchedFixture(t, tc.headOn, false)
			before := gitSHA(t, dir, "feat-a")

			stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
			if exit != 0 {
				t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
			}
			doc := planDoc(t, stdout)
			if doc["runnable"] != false {
				t.Fatalf("runnable = %v, want false", doc["runnable"])
			}
			found := false
			for _, raw := range doc["blockers"].([]any) {
				b := raw.(map[string]any)
				if b["kind"] != "preflight-refused" {
					continue
				}
				found = true
				if b["entry"] != "feat-a" {
					t.Fatalf("the rank 4 blocker must be on the row, got entry=%v", b["entry"])
				}
				detail, _ := b["detail"].(string)
				if !strings.Contains(detail, "feat-a") || !strings.Contains(detail, tc.want) {
					t.Fatalf("detail = %q, want it to name the persisted branch and the measured HEAD %q", detail, tc.want)
				}
			}
			if !found {
				t.Fatalf("no rank 4 preflight-refused blocker in %v", doc["blockers"])
			}

			// The GUARDED continuation refuses with exactly one marker and
			// moves no ref.
			_, stderr, exit = runSyncExecute(t, "test-feature", "--continue", "--max-replay-total", "50")
			if exit != 1 {
				t.Fatalf("the guarded continuation must exit 1: exit=%d stderr=%q", exit, stderr)
			}
			markers := planGuardMarkerRe.FindAllString(stderr, -1)
			if len(markers) != 1 || !strings.HasPrefix(markers[0], "plan-guard: preflight-refused: ") {
				t.Fatalf("stderr markers = %v, want exactly one ^plan-guard: preflight-refused: line\n%s", markers, stderr)
			}
			if after := gitSHA(t, dir, "feat-a"); after != before {
				t.Fatalf("feat-a moved from %s to %s; a refusing guarded continuation moves no ref", before, after)
			}
		})
	}

	t.Run("unguarded_continue_is_unchanged", func(t *testing.T) {
		_, fp, _, _ := switchedFixture(t, "feat-b", false)
		_, stderr, exit := runSyncExecute(t, "test-feature", "--continue")
		if strings.Contains(stderr, "plan-guard: ") {
			t.Fatalf("the unguarded control must be marker-free: %q", stderr)
		}
		if exit != 0 {
			t.Fatalf("the shipped unguarded continuation must still run over whatever HEAD is on: exit=%d stderr=%q", exit, stderr)
		}
		// It really RESUMED: the shipped path finalized its transaction and
		// released its lock, rather than refusing on the HEAD identity the
		// plan and guarded routes enforce.
		if internal.HasCheckoutTransaction(fp) {
			t.Fatal("the unguarded continuation must complete and delete its transaction; §22.25c leaves the shipped path unchanged")
		}
		if internal.HasCheckoutLock(fp) {
			t.Fatal("the unguarded continuation must release its lock")
		}
	})
}

// ===========================================================================
// §22.13j — the projected `.checkout-sync.lock` ladder is the WHOLE native
// ladder, in both arms (§13.7 gate j).
// ===========================================================================

// writeCheckoutLock installs a `.checkout-sync.lock` with the given bytes,
// creating the state directory the shipped writer would have created.
func writeCheckoutLock(t *testing.T, featurePath, body string) string {
	t.Helper()
	path := internal.CheckoutLockPath(featurePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// canonicalArtefactPath resolves an artefact path the way the production
// route's own workspace resolution does, so a byte-exact sentence comparison
// is not defeated by macOS's /var -> /private/var symlink.
func canonicalArtefactPath(t *testing.T, path string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return path
	}
	return filepath.Join(dir, filepath.Base(path))
}

// lockLadderDoc drives one `--plan` invocation over a checkout fixture and
// returns its document plus the sidecar records, so a cell can assert the
// blocker AND the zero-fetch/zero-header facts in one place.
func lockLadderDoc(t *testing.T, cont bool) func(dir, featurePath string) (map[string]any, string, string, []gitRecord) {
	return func(dir, featurePath string) (map[string]any, string, string, []gitRecord) {
		t.Helper()
		args := []string{"test-feature", "--plan", "--json"}
		if cont {
			args = append(args, "--continue")
		} else {
			args = append(args, "--fetch")
		}
		w := newSyncGitWrapper(t, false)
		var stdout, stderr string
		var exit int
		w.around(t, func() { stdout, stderr, exit = runSyncExecute(t, args...) })
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		return planDoc(t, stdout), stdout, stderr, w.records(t)
	}
}

// TestCheckoutSyncModes_Criterion22_13j_ProjectedLockLadder is §22.13j's
// executable owner: EVERY row of the gate-j table, on the fresh arm
// (`--plan --fetch`) and on the continuation arm (`--plan --continue`), with
// the shipped sentence reproduced byte for byte.
//
// Each cell asserts the whole contract §22.13j names: rank 3 `state-refused`
// with a byte-equal detail, `runnable: false`, exit `0`, ZERO `git fetch`
// and no `Sync mode:` header. Only the live-holder cell carries
// `guard.execute_blocked_by: ["live-owner-concurrency"]`; every other cell's
// list is empty. The three negative assertions close it: a dead foreign lock
// publishes no blocker at all, no cell projects a write-race sentence, and a
// symlinked lock synthesizes no native sentence.
func TestCheckoutSyncModes_Criterion22_13j_ProjectedLockLadder(t *testing.T) {
	self := os.Getpid()
	live, cleanupLive := spawnLivePID(t)
	defer cleanupLive()
	dead := spawnDeadPID(t)

	type cell struct {
		name         string
		install      func(t *testing.T, dir, fp string) string // returns the lock path
		freshDetail  func(path string) string
		contDetail   func(path string) string
		liveHolder   bool
		noBlockerAll bool
	}

	cells := []cell{
		{
			name:         "absent lock refuses nothing",
			install:      func(t *testing.T, dir, fp string) string { return internal.CheckoutLockPath(fp) },
			noBlockerAll: true,
		},
		{
			name: "directory lock takes the read arm",
			install: func(t *testing.T, dir, fp string) string {
				path := internal.CheckoutLockPath(fp)
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				return path
			},
			freshDetail: func(path string) string { return "read existing checkout-sync lock: " },
			contDetail:  func(path string) string { return "read checkout-sync lock: " },
		},
		{
			name: "corrupt lock is the invalid arm",
			install: func(t *testing.T, dir, fp string) string {
				return writeCheckoutLock(t, fp, "not yaml: [")
			},
			freshDetail: func(path string) string {
				return "invalid checkout-sync lock; inspect " + path + " and use --abort to recover: "
			},
			contDetail: func(path string) string { return "invalid checkout-sync lock " + path + ": " },
		},
		{
			name: "empty lock is the invalid arm",
			install: func(t *testing.T, dir, fp string) string {
				return writeCheckoutLock(t, fp, "")
			},
			freshDetail: func(path string) string {
				return "invalid checkout-sync lock; inspect " + path + " and use --abort to recover: "
			},
			contDetail: func(path string) string { return "invalid checkout-sync lock " + path + ": " },
		},
		{
			name: "pid 0 is being initialized or invalid",
			install: func(t *testing.T, dir, fp string) string {
				return writeCheckoutLock(t, fp, "pid: 0\ncreated: \"2020-01-01T00:00:00Z\"\n")
			},
			freshDetail: func(path string) string {
				return "checkout-sync lock is being initialized or is invalid; retry or inspect " + path
			},
			contDetail: func(path string) string {
				return "checkout-sync lock is being initialized or is invalid; retry or inspect " + path
			},
		},
		{
			name: "pid -1 is being initialized or invalid",
			install: func(t *testing.T, dir, fp string) string {
				return writeCheckoutLock(t, fp, "pid: -1\ncreated: \"2020-01-01T00:00:00Z\"\n")
			},
			freshDetail: func(path string) string {
				return "checkout-sync lock is being initialized or is invalid; retry or inspect " + path
			},
			contDetail: func(path string) string {
				return "checkout-sync lock is being initialized or is invalid; retry or inspect " + path
			},
		},
		{
			name: "live foreign lock",
			install: func(t *testing.T, dir, fp string) string {
				return writeCheckoutLock(t, fp, fmt.Sprintf("pid: %d\ncreated: \"2020-01-01T00:00:00Z\"\n", live))
			},
			freshDetail: func(path string) string {
				return fmt.Sprintf("checkout-sync lock held by live process %d (created %s); cannot steal live lock", live, "2020-01-01T00:00:00Z")
			},
			contDetail: func(path string) string {
				return fmt.Sprintf("lock held by live process %d; cannot reclaim", live)
			},
			liveHolder: true,
		},
		{
			name: "dead foreign lock with no transaction relation",
			install: func(t *testing.T, dir, fp string) string {
				return writeCheckoutLock(t, fp, fmt.Sprintf("pid: %d\ncreated: \"2020-01-01T00:00:00Z\"\n", dead))
			},
			noBlockerAll: true,
		},
	}

	assertCell := func(t *testing.T, cont bool, c cell) {
		t.Helper()
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		if cont {
			saveRestoreTransaction(t, dir, fp, "main", internal.StageConflict)
		}
		path := canonicalArtefactPath(t, c.install(t, dir, fp))

		doc, stdout, stderr, records := lockLadderDoc(t, cont)(dir, fp)

		want := ""
		if !c.noBlockerAll {
			if cont {
				want = c.contDetail(path)
			} else {
				want = c.freshDetail(path)
			}
		}

		var got []string
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			if b["kind"] != "state-refused" {
				continue
			}
			detail, _ := b["detail"].(string)
			if b["entry"] != nil {
				t.Fatalf("a lock-ladder blocker is document-level, got entry=%v", b["entry"])
			}
			got = append(got, detail)
		}

		if want == "" {
			for _, d := range got {
				if strings.Contains(d, "checkout-sync lock") || strings.Contains(d, "cannot reclaim") {
					t.Fatalf("%s: expected NO lock blocker, got %q", c.name, d)
				}
			}
		} else {
			matched := false
			for _, d := range got {
				if strings.HasPrefix(d, want) {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("%s: no state-refused blocker whose detail starts with the shipped sentence.\nwant prefix: %q\ngot: %v", c.name, want, got)
			}
			if doc["runnable"] != false {
				t.Fatalf("%s: runnable = %v, want false", c.name, doc["runnable"])
			}
			// Zero fetch, and no Sync mode: header.
			for _, r := range records {
				if r.Verb == "fetch" {
					t.Fatalf("%s: a refusing lock cell must issue zero git fetch, got %v", c.name, r.Tail())
				}
			}
			if strings.Contains(stderr, "Sync mode:") || strings.Contains(stdout, "Sync mode:") {
				t.Fatalf("%s: a refusing lock cell prints no Sync mode: header", c.name)
			}
		}

		// The controlled-token contract: only the live-holder cell.
		guard := doc["guard"].(map[string]any)
		tokens, _ := guard["execute_blocked_by"].([]any)
		hasLive := slices.ContainsFunc(tokens, func(v any) bool { return v == "live-owner-concurrency" })
		if c.liveHolder && !cont && !hasLive {
			t.Fatalf("%s: guard.execute_blocked_by = %v, want it to carry live-owner-concurrency", c.name, tokens)
		}
		if !c.liveHolder && hasLive {
			t.Fatalf("%s: guard.execute_blocked_by = %v must not carry live-owner-concurrency", c.name, tokens)
		}

		// No cell ever projects a write-race or future-write sentence.
		for _, d := range got {
			for _, forbidden := range []string{
				"reclaim stale checkout-sync lock:",
				"reclaim checkout-sync lock:",
				"acquire checkout-sync lock after stale recovery:",
				"create lock directory:",
			} {
				if strings.Contains(d, forbidden) {
					t.Fatalf("%s: projected the write-race sentence %q", c.name, forbidden)
				}
			}
		}
	}

	for _, c := range cells {
		c := c
		t.Run("fresh/"+c.name, func(t *testing.T) { assertCell(t, false, c) })
		t.Run("continue/"+c.name, func(t *testing.T) { assertCell(t, true, c) })
	}

	// Row 5a: a SELF-PID lock refuses on the fresh arm with the
	// initialized-or-invalid sentence and an EMPTY execute_blocked_by, and
	// publishes NO blocker at all on the continuation arm.
	t.Run("row5a_self_pid", func(t *testing.T) {
		t.Run("fresh_refuses_without_a_live_owner_token", func(t *testing.T) {
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			path := canonicalArtefactPath(t, writeCheckoutLock(t, fp, fmt.Sprintf("pid: %d\ncreated: \"2020-01-01T00:00:00Z\"\n", self)))
			doc, _, stderr, records := lockLadderDoc(t, false)(dir, fp)
			want := "checkout-sync lock is being initialized or is invalid; retry or inspect " + path
			found := false
			for _, raw := range doc["blockers"].([]any) {
				if b := raw.(map[string]any); b["kind"] == "state-refused" && b["detail"] == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("row 5a fresh: want the byte-exact %q, got %v", want, doc["blockers"])
			}
			guard := doc["guard"].(map[string]any)
			if tokens, _ := guard["execute_blocked_by"].([]any); len(tokens) != 0 {
				t.Fatalf("row 5a fresh: execute_blocked_by = %v, want [] (a self PID is not a live foreign owner)", tokens)
			}
			if doc["freshness"] == "not-refreshed-live-run" {
				t.Fatal("row 5a fresh: a self PID must not publish not-refreshed-live-run")
			}
			for _, r := range records {
				if r.Verb == "fetch" {
					t.Fatal("row 5a fresh: zero git fetch")
				}
			}
			_ = stderr
		})

		t.Run("continuation_publishes_no_blocker_and_really_reclaims", func(t *testing.T) {
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			saveRestoreTransaction(t, dir, fp, "main", internal.StageConflict)
			writeCheckoutLock(t, fp, fmt.Sprintf("pid: %d\ncreated: \"2020-01-01T00:00:00Z\"\n", self))
			doc, _, _, _ := lockLadderDoc(t, true)(dir, fp)
			for _, raw := range doc["blockers"].([]any) {
				b := raw.(map[string]any)
				detail, _ := b["detail"].(string)
				if strings.Contains(detail, "checkout-sync lock") || strings.Contains(detail, "cannot reclaim") {
					t.Fatalf("row 5a continuation: want NO lock blocker at all, got %q", detail)
				}
			}
		})
	})

	// Row 6: a dead foreign lock BESIDE an undecodable transaction takes
	// GATE B's sentence, not the lock ladder's stale-lock sentence, because
	// HasCheckoutTransaction is a bare os.Stat evaluated above the lock.
	t.Run("row6_gate_b_wins_over_the_stale_lock_sentence", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		if err := os.MkdirAll(filepath.Dir(internal.CheckoutTransactionPath(fp)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(internal.CheckoutTransactionPath(fp), []byte("not yaml: ["), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { internal.DeleteCheckoutTransaction(fp) })
		writeCheckoutLock(t, fp, fmt.Sprintf("pid: %d\ncreated: \"2020-01-01T00:00:00Z\"\n", dead))

		doc, _, _, _ := lockLadderDoc(t, false)(dir, fp)
		wantB := "checkout sync transaction already exists; use --continue or --abort"
		foundB := false
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			detail, _ := b["detail"].(string)
			if detail == wantB {
				foundB = true
			}
			if strings.Contains(detail, "stale lock from PID") {
				t.Fatalf("row 6 must never project the stale-lock sentence: %q", detail)
			}
		}
		if !foundB {
			t.Fatalf("row 6: want gate b's own sentence %q, got %v", wantB, doc["blockers"])
		}
	})

	// A SYMLINKED lock publishes presence: symlink with no synthesized
	// native sentence, while its GUARDED twin refuses with
	// owner-artefact-undecodable (§12.5).
	t.Run("symlinked_lock_synthesizes_no_native_sentence", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		target := filepath.Join(t.TempDir(), "real.lock")
		if err := os.WriteFile(target, []byte("pid: 424242\ncreated: \"2020-01-01T00:00:00Z\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := internal.CheckoutLockPath(fp)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("this fixture needs symlink support: %v", err)
		}

		doc, _, _, _ := lockLadderDoc(t, false)(dir, fp)
		state := doc["state"].(map[string]any)
		files := state["files"].(map[string]any)
		lock := files["checkout_lock"].(map[string]any)
		if lock["presence"] != "symlink" {
			t.Fatalf("checkout_lock.presence = %v, want symlink", lock["presence"])
		}
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			detail, _ := b["detail"].(string)
			for _, native := range []string{"read existing checkout-sync lock:", "invalid checkout-sync lock", "is being initialized or is invalid", "cannot steal live lock"} {
				if strings.Contains(detail, native) {
					t.Fatalf("a symlinked lock must synthesize no native sentence, got %q", detail)
				}
			}
		}
		guard := doc["guard"].(map[string]any)
		tokens, _ := guard["execute_blocked_by"].([]any)
		if !slices.ContainsFunc(tokens, func(v any) bool { return v == "owner-artefact-undecodable" }) {
			t.Fatalf("guard.execute_blocked_by = %v, want owner-artefact-undecodable", tokens)
		}
	})
}

// ===========================================================================
// §22.13e — checkout continuation dirt and autostash are PER STAGE
// (§13.3b, §18.4).
// ===========================================================================

// dirtyContinuationFixture parks a checkout transaction at the given stage
// with a tracked-dirty working tree and the given rebase.autoStash setting
// ("" leaves the key unset, "bogus" makes it unreadable as a bool).
func dirtyContinuationFixture(t *testing.T, stage internal.CheckoutStage, autoStash string, overlapping bool) (dir, featurePath string) {
	t.Helper()
	dir, featurePath = checkoutModeFixture(t)
	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)
	if autoStash != "" {
		gitRunCS(t, dir, "config", "rebase.autoStash", autoStash)
	}
	gitRunCS(t, dir, "checkout", "feat-a")

	// Tracked dirt. `overlapping` decides whether the dirty path is one the
	// checkout target also carries.
	file := "a.txt"
	if !overlapping {
		file = "README.md"
	}
	writeFileCS(t, dir, file, "dirty\n")

	tx := &internal.CheckoutTransaction{
		Feature: "test-feature", OriginalBranch: "main", OriginalHEAD: gitSHA(t, dir, "main"),
		Stage: stage, CurrentIndex: 0,
		Plan: []internal.CheckoutPlanEntry{
			{Name: "feat-a", Branch: "feat-a", Base: "feat-root", NewBaseSHA: gitSHA(t, dir, "feat-root"), LastBaseSHA: gitSHA(t, dir, "main")},
		},
	}
	if err := internal.SaveCheckoutTransaction(featurePath, tx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { internal.DeleteCheckoutTransaction(featurePath) })
	return dir, featurePath
}

// warningKinds projects a document's warnings[] into a set of kinds.
func warningKinds(t *testing.T, doc map[string]any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	rows, _ := doc["warnings"].([]any)
	for _, raw := range rows {
		out[raw.(map[string]any)["kind"].(string)] = true
	}
	return out
}

// TestCheckoutSyncModes_Criterion22_13e_ContinuationDirtAndAutostashPerStage
// is §22.13e's executable owner: over a tracked-dirty checkout continuation
// at EACH of the eight stages, with rebase.autoStash true, false and
// unreadable.
//
//	switched + autostash false -> rank 5.5 context-dirty, runnable: false
//	switched + autostash true  -> applies true, the autostash-across-switch
//	                              warning, reapply_may_conflict true, runnable
//	planned / rebasing         -> the same rebase-level verdict PLUS the
//	                              residual checkout-dirty-present warning in
//	                              BOTH autostash arms
//	conflict/rebased/validating/completed -> applies false, no context-dirty
//	                              blocker of their own
//	restoring                  -> the checkout-dirty-present warning
//	unreadable rebase.autoStash -> both autostash fields null, no verdict
//
// The FRESH control is asserted unchanged: a dirty fresh checkout run is the
// shipped rank 3 sentence, never an autostash row.
func TestCheckoutSyncModes_Criterion22_13e_ContinuationDirtAndAutostashPerStage(t *testing.T) {
	rowOf := func(t *testing.T, stdout string) map[string]any {
		t.Helper()
		for _, e := range planDoc(t, stdout)["entries"].([]any) {
			if row := e.(map[string]any); row["name"] == "feat-a" {
				return row
			}
		}
		t.Fatalf("no feat-a row:\n%s", stdout)
		return nil
	}
	plan := func(t *testing.T) (map[string]any, map[string]any) {
		t.Helper()
		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		return planDoc(t, stdout), rowOf(t, stdout)
	}

	t.Run("switched_autostash_false_is_rank_5_5_context_dirty", func(t *testing.T) {
		dirtyContinuationFixture(t, internal.StageSwitched, "false", true)
		doc, row := plan(t)
		ctx := row["context"].(map[string]any)
		if ctx["dirty"] != true {
			t.Fatalf("context.dirty = %v, want true", ctx["dirty"])
		}
		if ctx["autostash_applies_to_this_arm"] != false {
			t.Fatalf("autostash_applies_to_this_arm = %v, want false", ctx["autostash_applies_to_this_arm"])
		}
		found := false
		for _, raw := range doc["blockers"].([]any) {
			if b := raw.(map[string]any); b["kind"] == "context-dirty" && b["entry"] == "feat-a" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no rank 5.5 context-dirty blocker on the row: %v", doc["blockers"])
		}
		if doc["runnable"] != false {
			t.Fatalf("runnable = %v, want false", doc["runnable"])
		}
		// The real resumed rebase really is refused.
		_, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--max-replay-total", "50")
		if exit != 1 {
			t.Fatalf("the guarded resume must be refused: exit=%d stderr=%q", exit, stderr)
		}
	})

	t.Run("switched_autostash_true_applies_warns_and_is_runnable", func(t *testing.T) {
		dirtyContinuationFixture(t, internal.StageSwitched, "true", true)
		doc, row := plan(t)
		ctx := row["context"].(map[string]any)
		if ctx["autostash_applies_to_this_arm"] != true {
			t.Fatalf("autostash_applies_to_this_arm = %v, want true", ctx["autostash_applies_to_this_arm"])
		}
		if ctx["autostash_reapply_may_conflict"] != true {
			t.Fatalf("autostash_reapply_may_conflict = %v, want true", ctx["autostash_reapply_may_conflict"])
		}
		if !warningKinds(t, doc)["autostash-across-switch"] {
			t.Fatalf("warnings = %v, want the autostash-across-switch warning", doc["warnings"])
		}
		if doc["runnable"] != true {
			t.Fatalf("runnable = %v, want true", doc["runnable"])
		}
	})

	for _, autoStash := range []string{"true", "false"} {
		autoStash := autoStash
		for _, stage := range []internal.CheckoutStage{internal.StagePlanned, internal.StageRebasing} {
			stage := stage
			t.Run(fmt.Sprintf("%s_autostash_%s_adds_the_residual_warning", stage, autoStash), func(t *testing.T) {
				dirtyContinuationFixture(t, stage, autoStash, true)
				doc, row := plan(t)
				ctx := row["context"].(map[string]any)
				want := autoStash == "true"
				if ctx["autostash_applies_to_this_arm"] != want {
					t.Fatalf("autostash_applies_to_this_arm = %v, want %v (the same rebase-level verdict as `switched`)", ctx["autostash_applies_to_this_arm"], want)
				}
				if !warningKinds(t, doc)["checkout-dirty-present"] {
					t.Fatalf("warnings = %v, want the residual checkout-dirty-present warning on a %s stage", doc["warnings"], stage)
				}
			})
		}
	}

	t.Run("planned_non_overlapping_dirt_still_warns", func(t *testing.T) {
		dirtyContinuationFixture(t, internal.StagePlanned, "true", false)
		doc, _ := plan(t)
		if !warningKinds(t, doc)["checkout-dirty-present"] {
			t.Fatalf("warnings = %v, want checkout-dirty-present even when the dirty path does not overlap the checkout target", doc["warnings"])
		}
	})

	for _, stage := range []internal.CheckoutStage{internal.StageConflict, internal.StageRebased, internal.StageValidating, internal.StageCompleted} {
		stage := stage
		t.Run(string(stage)+"_applies_false_with_no_context_dirty_blocker", func(t *testing.T) {
			dirtyContinuationFixture(t, stage, "false", true)
			doc, row := plan(t)
			ctx, _ := row["context"].(map[string]any)
			if ctx != nil && ctx["autostash_applies_to_this_arm"] == true {
				t.Fatalf("%s: autostash_applies_to_this_arm = true, want false", stage)
			}
			for _, raw := range doc["blockers"].([]any) {
				if b := raw.(map[string]any); b["kind"] == "context-dirty" && b["entry"] == "feat-a" {
					t.Fatalf("%s must publish no context-dirty blocker of its own: %v", stage, b)
				}
			}
		})
	}

	t.Run("restoring_warns_for_its_restore_checkout", func(t *testing.T) {
		dirtyContinuationFixture(t, internal.StageRestoring, "false", true)
		doc, _ := plan(t)
		if !warningKinds(t, doc)["checkout-dirty-present"] {
			t.Fatalf("warnings = %v, want checkout-dirty-present for the restoring stage's own checkout", doc["warnings"])
		}
	})

	t.Run("unreadable_autostash_publishes_null_and_no_verdict", func(t *testing.T) {
		dirtyContinuationFixture(t, internal.StageSwitched, "bogus", true)
		doc, row := plan(t)
		ctx := row["context"].(map[string]any)
		if ctx["autostash"] != nil {
			t.Fatalf("context.autostash = %v, want null for an unreadable rebase.autoStash", ctx["autostash"])
		}
		for _, raw := range doc["blockers"].([]any) {
			if b := raw.(map[string]any); b["kind"] == "context-dirty" && b["entry"] == "feat-a" {
				t.Fatalf("an unreadable rebase.autoStash publishes no verdict, got %v", b)
			}
		}
	})

	t.Run("fresh_dirty_control_is_the_shipped_rank_3_sentence", func(t *testing.T) {
		dir, _ := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		gitRunCS(t, dir, "config", "rebase.autoStash", "true")
		writeFileCS(t, dir, "README.md", "dirty\n")

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		want := "working tree is dirty; commit or stash changes before checkout sync"
		found := false
		for _, raw := range doc["blockers"].([]any) {
			if b := raw.(map[string]any); b["detail"] == want && b["kind"] == "state-refused" {
				found = true
			}
		}
		if !found {
			t.Fatalf("a dirty FRESH checkout run must publish the shipped rank 3 sentence %q, got %v", want, doc["blockers"])
		}
	})
}

// TestCheckoutSyncModes_Criterion22_24c_SyncTriggersNeedV2Matrix is the
// external half of criterion 22.24c's route x version matrix: the cli-side
// syncTriggersNeedV2 predicate, driven over every cell it can be asked about
// with a nil payload and with payloads carrying each route/version pair. Cell
// 4 is deliberately absent — the guarded-sentinel interception owns that
// cell's flag-conflict check before I20 is consulted — and cells 1 and 7
// always need v2 regardless of what the payload says.
func TestCheckoutSyncModes_Criterion22_24c_SyncTriggersNeedV2Matrix(t *testing.T) {
	payloads := []struct {
		name    string
		payload *internal.SyncRunState
		newMode bool
	}{
		{"nil-payload", nil, false},
		{"absent-route-v1", &internal.SyncRunState{StateVersion: 1}, false},
		{"absent-route-v2", &internal.SyncRunState{StateVersion: 2}, true},
		{"absent-route-v3", &internal.SyncRunState{StateVersion: 3}, true},
		{"route-legacy-v3", &internal.SyncRunState{StateVersion: 3, Route: internal.RouteLegacy}, false},
		{"route-new-mode-v1", &internal.SyncRunState{StateVersion: 1, Route: internal.RouteNewMode}, true},
		{"unknown-route-v2", &internal.SyncRunState{StateVersion: 2, Route: "sideways"}, true},
	}

	for _, p := range payloads {
		for cell := 1; cell <= 7; cell++ {
			if cell == 4 {
				continue
			}
			t.Run(fmt.Sprintf("%s/cell%d", p.name, cell), func(t *testing.T) {
				got := syncTriggersNeedV2(internal.SyncExternalState{Cell: internal.SyncStateCell(cell), Payload: p.payload})
				var want bool
				switch cell {
				case 1, 7:
					want = true
				case 5:
					want = !p.newMode
				}
				if got != want {
					t.Fatalf("syncTriggersNeedV2(cell %d, %s) = %v, want %v", cell, p.name, got, want)
				}
				// The cell-5 arm must agree with the production payload
				// predicate itself, not with a private version compare.
				if cell == 5 && internal.PayloadNewMode(p.payload) != p.newMode {
					t.Fatalf("PayloadNewMode(%s) = %v, want %v", p.name, internal.PayloadNewMode(p.payload), p.newMode)
				}
			})
		}
	}
}
