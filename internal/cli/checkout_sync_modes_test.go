package cli

import (
	"os"
	"path/filepath"
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
