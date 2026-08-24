package internal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// TestCheckoutSyncPlan — the checkout-only plan-only route: InspectCheckoutPlan's
// ordered read-only measurement pass, its three fresh-run precondition gates,
// the shipped BuildCheckoutPlan entry point (signature-frozen), the D1-a/D1-b
// Name-vs-GitBranch() identity rule buildCheckoutPlanFrom enforces, and the
// two document builders (BuildCheckoutRebasePlan / BuildCheckoutContinuationPlan)
// together with the plan-only route's PlanWriters prose-only writer contract
// (§13.7, rebase_plan_guard.go; checkout_sync.go).
// ============================================================================

// ---------------------------------------------------------------------------
// Fixture helpers.
// ---------------------------------------------------------------------------

// cspFixture builds a real, minimal checkout-mode workspace exactly as
// rpsCheckoutFixture (rebase_plan_state_test.go) does: one real git repo, one
// commit on main, .tws/config.yaml, fully neutralized via t.Setenv, as the
// shipped fixtures do.
func cspFixture(t *testing.T) (repoDir string, ws Workspace) {
	t.Helper()
	return rpsCheckoutFixture(t)
}

// cspOpts builds a minimal, valid CheckoutSyncOpts for one feature, with the
// given policy. Every test starts from this and overrides only what it needs.
func cspOpts(feature, featurePath, repoDir string, policy SyncRunPolicy) CheckoutSyncOpts {
	return CheckoutSyncOpts{
		Feature:     feature,
		FeaturePath: featurePath,
		RepoDir:     repoDir,
		Policy:      policy,
	}
}

func cspAllPolicy(fetch SyncFetchPolicy) SyncRunPolicy {
	return SyncRunPolicy{Fetch: fetch, Propagation: SyncPropagationFull, ScopeKind: SyncScopeAll}
}

// cspGate finds one named gate row, failing the test if it is absent.
func cspGate(t *testing.T, gates []PlanGateResult, id string) PlanGateResult {
	t.Helper()
	for _, g := range gates {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("no gate with ID %q among %+v", id, gates)
	return PlanGateResult{}
}

// cspMarkOperationInProgress creates a real MERGE_HEAD artefact at the exact
// path `git rev-parse --git-path MERGE_HEAD` resolves to, so
// gitOperationInProgress's own git-path lookup (not a hardcoded ".git/...")
// finds it, mirroring a real interrupted `git merge --no-commit`.
func cspMarkOperationInProgress(t *testing.T, repoDir string) {
	t.Helper()
	gitPath := gitInTest(t, repoDir, "rev-parse", "--git-path", "MERGE_HEAD")
	if !filepath.IsAbs(gitPath) {
		gitPath = filepath.Join(repoDir, gitPath)
	}
	if err := os.WriteFile(gitPath, []byte(strings.Repeat("a", 40)+"\n"), 0o644); err != nil {
		t.Fatalf("write MERGE_HEAD: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Group A: InspectCheckoutPlan's ordered read-only pass and its gates.
// ---------------------------------------------------------------------------

func TestCheckoutSyncPlan_InspectCheckoutPlan_FreshRunHappyPathOrder(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat1"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	gitInTest(t, dir, "branch", "child")

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
	insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})

	if insp.Continue {
		t.Fatal("Continue must be false: opts.Continue was never set")
	}
	if !insp.Version.Probed {
		t.Fatal("expected a real, probed GitVersion")
	}
	if insp.StackErr != nil || insp.SortErr != nil || insp.SelectionErr != nil {
		t.Fatalf("expected every ladder error nil, got Stack=%v Sort=%v Selection=%v", insp.StackErr, insp.SortErr, insp.SelectionErr)
	}
	if len(insp.Order) != 1 || insp.Order[0].Name != "child" {
		t.Fatalf("Order = %+v, want exactly the one entry child", insp.Order)
	}
	if !insp.SelectionResolved || len(insp.Selection.Entries) != 1 {
		t.Fatalf("Selection = %+v resolved=%v, want one resolved entry", insp.Selection, insp.SelectionResolved)
	}

	if len(insp.Gates) != 3 {
		t.Fatalf("len(Gates) = %d, want 3 on a fresh run", len(insp.Gates))
	}
	for _, g := range insp.Gates {
		if !g.Applies {
			t.Fatalf("gate %s: Applies = false, want true on a fresh run", g.ID)
		}
		if g.Failed {
			t.Fatalf("gate %s: Failed = true, want false against a clean fixture", g.ID)
		}
	}

	if insp.Limits.PerEntry.Origin != "none" || insp.Limits.Total.Origin != "none" {
		t.Fatalf("Limits = %+v, want both origins none (no flags, no persisted transaction)", insp.Limits)
	}
	if len(insp.LimitConflicts) != 0 {
		t.Fatalf("LimitConflicts = %+v, want none (no persisted transaction to conflict with)", insp.LimitConflicts)
	}

	if len(insp.PlanEntries) != 1 || insp.PlanEntries[0].Name != "child" || insp.PlanEntries[0].Base != "main" {
		t.Fatalf("PlanEntries = %+v, want one child row based on main", insp.PlanEntries)
	}
	if insp.PlanBuildErr != nil {
		t.Fatalf("PlanBuildErr = %v, want nil", insp.PlanBuildErr)
	}
	if len(insp.Remaining) != 1 || insp.Remaining[0] != "child" {
		t.Fatalf("Remaining = %v, want [child] on a fresh run with nothing yet completed", insp.Remaining)
	}
	if insp.StageFacts != nil {
		t.Fatalf("StageFacts = %+v, want nil: the fresh route never populates it", insp.StageFacts)
	}
	if !insp.BasePreflight.Applies || insp.BasePreflight.Failed {
		t.Fatalf("BasePreflight = %+v, want Applies=true Failed=false", insp.BasePreflight)
	}
	if insp.FetchPlan.Applies {
		t.Fatal("FetchPlan.Applies = true, want false: policy fetch is disabled")
	}
}

func TestCheckoutSyncPlan_InspectCheckoutPlan_StackLoadErrorStopsTheLadderEarly(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-nostack"
	fp := ws.FeaturePath(feature)
	// Deliberately never call addStackEntries/SaveStack: fp has no stack.yaml.

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
	insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})

	if insp.StackErr == nil {
		t.Fatal("expected a StackErr: no stack.yaml exists at all")
	}
	if insp.SortErr != nil {
		t.Fatalf("SortErr = %v, want nil: the ladder must stop at StackErr before ever reaching TopoSort", insp.SortErr)
	}
	if insp.SelectionResolved {
		t.Fatal("SelectionResolved = true, want false: the ladder stopped before selection")
	}
	if insp.PlanEntries != nil {
		t.Fatalf("PlanEntries = %+v, want nil: never reached", insp.PlanEntries)
	}
	// Gates/Limits are measured BEFORE the stack load and must still be
	// populated even though the stack itself never loaded.
	if len(insp.Gates) != 3 {
		t.Fatalf("len(Gates) = %d, want 3: gates precede the stack load in the ladder", len(insp.Gates))
	}
}

func TestCheckoutSyncPlan_InspectCheckoutPlan_SortErrorStopsTheLadderEarly(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-cycle"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{
		{Name: "a", Base: "b"},
		{Name: "b", Base: "a"},
	})

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
	insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})

	if insp.StackErr != nil {
		t.Fatalf("StackErr = %v, want nil: the stack itself loaded fine", insp.StackErr)
	}
	if insp.SortErr == nil {
		t.Fatal("expected a SortErr: the stack is a two-entry cycle")
	}
	if insp.SelectionResolved {
		t.Fatal("SelectionResolved = true, want false: the ladder stopped at the sort error")
	}
	if insp.PlanEntries != nil {
		t.Fatalf("PlanEntries = %+v, want nil: never reached", insp.PlanEntries)
	}
}

func TestCheckoutSyncPlan_InspectCheckoutPlan_ContinueRouteSkipsPlanBuildAndPopulatesStageFacts(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-continue"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	gitInTest(t, dir, "branch", "child")

	tx := &CheckoutTransaction{
		StateVersion:   1,
		Feature:        feature,
		StartedAt:      "2026-01-01T00:00:00Z",
		OriginalBranch: "main",
		OriginalHEAD:   gitInTest(t, dir, "rev-parse", "HEAD"),
		Plan:           []CheckoutPlanEntry{{Name: "child", Branch: "child", Base: "main"}},
		CurrentIndex:   0,
		Stage:          StagePlanned,
	}
	if err := SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatalf("SaveCheckoutTransaction: %v", err)
	}

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchEnabled))
	opts.Continue = true
	insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})

	if !insp.Continue {
		t.Fatal("Continue must be true")
	}
	if insp.Gates != nil {
		t.Fatalf("Gates = %+v, want nil on --continue: the three pre-lock gates never apply to a continuation", insp.Gates)
	}
	if insp.PlanEntries != nil {
		t.Fatalf("PlanEntries = %+v, want nil: a continuation never re-derives a plan", insp.PlanEntries)
	}
	if insp.PlanBuildErr != nil {
		t.Fatalf("PlanBuildErr = %v, want nil: buildCheckoutPlanFrom is never even called", insp.PlanBuildErr)
	}
	if insp.BasePreflight != (PlanBasePreflight{}) {
		t.Fatalf("BasePreflight = %+v, want the zero value: I14 never runs on --continue", insp.BasePreflight)
	}
	if insp.FetchPlan.Applies {
		t.Fatal("FetchPlan.Applies = true, want false: the pre-fetch enumeration never runs on --continue even under an enabled fetch policy")
	}
	if insp.StageFacts == nil {
		t.Fatal("expected a non-nil StageFacts on --continue with a remaining entry")
	}
	if len(insp.StageFacts) != 1 || insp.StageFacts[0].Entry != "child" {
		t.Fatalf("StageFacts = %+v, want one row for child", insp.StageFacts)
	}
}

func TestCheckoutSyncPlan_InspectCheckoutPlan_TransactionExistsGateFires(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-existing-tx"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	gitInTest(t, dir, "branch", "child")

	tx := &CheckoutTransaction{Feature: feature, Plan: []CheckoutPlanEntry{{Name: "child", Branch: "child", Base: "main"}}}
	if err := SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatalf("SaveCheckoutTransaction: %v", err)
	}

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled)) // Continue left false: a fresh invocation
	insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})

	gate := cspGate(t, insp.Gates, "checkout-transaction-exists")
	if !gate.Applies || !gate.Failed {
		t.Fatalf("checkout-transaction-exists gate = %+v, want Applies=true Failed=true", gate)
	}
	for _, id := range []string{"checkout-operation-in-progress", "checkout-working-tree-dirty"} {
		if g := cspGate(t, insp.Gates, id); g.Failed {
			t.Fatalf("gate %s unexpectedly failed: %+v", id, g)
		}
	}
}

func TestCheckoutSyncPlan_InspectCheckoutPlan_OperationInProgressGateFires(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-mid-op"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	gitInTest(t, dir, "branch", "child")
	cspMarkOperationInProgress(t, dir)

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
	insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})

	gate := cspGate(t, insp.Gates, "checkout-operation-in-progress")
	if !gate.Applies || !gate.Failed {
		t.Fatalf("checkout-operation-in-progress gate = %+v, want Applies=true Failed=true", gate)
	}
	if g := cspGate(t, insp.Gates, "checkout-transaction-exists"); g.Failed {
		t.Fatalf("checkout-transaction-exists unexpectedly failed: %+v", g)
	}
}

func TestCheckoutSyncPlan_InspectCheckoutPlan_WorkingTreeDirtyGateFires(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-dirty"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	gitInTest(t, dir, "branch", "child")
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
	insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})

	gate := cspGate(t, insp.Gates, "checkout-working-tree-dirty")
	if !gate.Applies || !gate.Failed {
		t.Fatalf("checkout-working-tree-dirty gate = %+v, want Applies=true Failed=true", gate)
	}
}

func TestCheckoutSyncPlan_InspectCheckoutPlan_FetchPlanAppliesOnlyWhenPolicyFetchEnabled(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-fetchplan"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	gitInTest(t, dir, "branch", "child")

	t.Run("Disabled", func(t *testing.T) {
		opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
		insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})
		if insp.FetchPlan.Applies || insp.FetchPlan.Contexts != nil || insp.FetchPlan.Blockers != nil || insp.FetchPlan.Controlled != nil || insp.FetchPlan.Suppressed != "" {
			t.Fatalf("FetchPlan = %+v, want the zero value when the policy does not fetch", insp.FetchPlan)
		}
	})

	t.Run("Enabled", func(t *testing.T) {
		opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchEnabled))
		insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})
		if !insp.FetchPlan.Applies {
			t.Fatal("FetchPlan.Applies = false, want true when the policy fetches")
		}
		if len(insp.FetchPlan.Contexts) != 1 || insp.FetchPlan.Contexts[0].Source != "workspace-repo-root" {
			t.Fatalf("Contexts = %+v, want exactly one workspace-repo-root context", insp.FetchPlan.Contexts)
		}
		if insp.FetchPlan.Contexts[0].Effect == nil {
			t.Fatal("expected a measured Effect before any fetch runs")
		}
	})
}

// ---------------------------------------------------------------------------
// Group B: the shipped BuildCheckoutPlan entry point, and buildCheckoutPlanFrom's
// D1-a/D1-b identity rule.
// ---------------------------------------------------------------------------

// TestCheckoutSyncPlan_BuildCheckoutPlan_SignatureUnchanged pins the shipped,
// exported entry point's exact signature — func(repoDir string, stack Stack,
// sel SyncSelection) ([]CheckoutPlanEntry, error) — by calling it exactly as
// written; a signature change would fail this file to compile at all, which
// is the strongest test a signature-freeze claim can have in Go.
func TestCheckoutSyncPlan_BuildCheckoutPlan_SignatureUnchanged(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-shipped-signature"
	stack := Stack{Branches: []StackEntry{{Name: "child", Base: "main"}}}
	addStackEntries(t, ws, feature, stack.Branches)
	gitInTest(t, dir, "branch", "child")

	sel, err := ResolveSyncSelectionFromOrder(stack, stack.Branches, cspAllPolicy(SyncFetchDisabled), SyncSelectionOpts{Mode: ModeCheckout})
	if err != nil {
		t.Fatalf("ResolveSyncSelectionFromOrder: %v", err)
	}

	var entries []CheckoutPlanEntry
	entries, err = BuildCheckoutPlan(dir, stack, sel)
	if err != nil {
		t.Fatalf("BuildCheckoutPlan: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "child" || entries[0].Base != "main" {
		t.Fatalf("entries = %+v, want one child row based on main", entries)
	}
}

// TestCheckoutSyncPlan_BuildCheckoutPlanFrom_NameLookupVsGitBranchIdentityRule
// is the crux D1-a/D1-b test: a parent and child stack entry each carry a
// Branch DIFFERENT from their Name. Only the child's Base references the
// parent, and it references the parent's logical Name ("parent-logical"), a
// string that is deliberately never a real Git ref in this fixture. The two
// REAL git branches are "physical/parent" and "physical/child" — the
// entries' own GitBranch() values.
//
// If the implementation ever looked up the stack parent by GitBranch()
// instead of Name (D1-a), GetBranch would fail to find "parent-logical" (no
// stack entry is named that under GitBranch()) and the base would
// incorrectly fall through to the literal string "parent-logical", which
// gitResolveRef would then fail to resolve at all (no such ref exists) —
// this test would then fail with a non-nil error instead of succeeding. If
// the implementation ever fed a Name to Git instead of GitBranch() (D1-b),
// gitResolveRef("parent-logical")/("child-logical") would likewise fail:
// neither name is a real ref. The only way this test can pass is if the
// shipped body does exactly what §9.2 describes: Name for the stack lookup,
// GitBranch() for every Git-facing ref.
func TestCheckoutSyncPlan_BuildCheckoutPlanFrom_NameLookupVsGitBranchIdentityRule(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-identity-rule"

	stack := Stack{Branches: []StackEntry{
		{Name: "parent-logical", Branch: "physical/parent", Base: "main"},
		{Name: "child-logical", Branch: "physical/child", Base: "parent-logical"},
	}}
	addStackEntries(t, ws, feature, stack.Branches)

	// Only the PHYSICAL branch names exist in Git. Neither logical Name is
	// ever created as a ref, so any accidental Name-as-git-ref use fails loudly.
	gitInTest(t, dir, "branch", "physical/parent", "main")
	gitInTest(t, dir, "branch", "physical/child", "physical/parent")
	wantParentSHA := gitInTest(t, dir, "rev-parse", "physical/parent")
	wantChildSHA := gitInTest(t, dir, "rev-parse", "physical/child")

	order, err := TopoSort(stack)
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if len(order) != 2 || order[0].Name != "parent-logical" || order[1].Name != "child-logical" {
		t.Fatalf("order = %+v, want [parent-logical child-logical]", order)
	}

	plan, err := buildCheckoutPlanFrom(dir, stack, order, SyncSelection{})
	if err != nil {
		t.Fatalf("buildCheckoutPlanFrom: %v (a Name-vs-GitBranch mixup would surface as an unresolved-ref error here)", err)
	}
	if len(plan) != 2 {
		t.Fatalf("len(plan) = %d, want 2", len(plan))
	}

	parentRow, childRow := plan[0], plan[1]

	// D1-b: every Git-facing ref is GitBranch(), never Name.
	if parentRow.Branch != "physical/parent" {
		t.Fatalf("parentRow.Branch = %q, want physical/parent (GitBranch(), not Name)", parentRow.Branch)
	}
	if childRow.Branch != "physical/child" {
		t.Fatalf("childRow.Branch = %q, want physical/child (GitBranch(), not Name)", childRow.Branch)
	}
	// D1-a: the stack lookup for the base matches on Name ("parent-logical"),
	// but the RESOLVED base value that reaches Git is the parent's GitBranch().
	if childRow.Base != "physical/parent" {
		t.Fatalf("childRow.Base = %q, want physical/parent: the parent lookup must match child-logical's Base against StackEntry.Name, then resolve through the parent's own GitBranch()", childRow.Base)
	}
	// The literal (non-stack-entry) base "main" is untouched: no stack entry
	// is named "main", so it stays exactly as written.
	if parentRow.Base != "main" {
		t.Fatalf("parentRow.Base = %q, want main (a literal base with no in-stack parent entry stays literal)", parentRow.Base)
	}

	// The logical Name survives separately in the plan row (never conflated
	// with the physical Branch it resolves through).
	if parentRow.Name != "parent-logical" || childRow.Name != "child-logical" {
		t.Fatalf("Names = %q/%q, want parent-logical/child-logical preserved verbatim", parentRow.Name, childRow.Name)
	}

	// The resolved SHAs are the REAL physical branches' tips, proving actual
	// git resolution happened through the correct ref, not a string echo.
	if parentRow.NewBaseSHA == "" || childRow.PreSHA != wantChildSHA {
		t.Fatalf("childRow.PreSHA = %q, want %q (real git rev-parse physical/child)", childRow.PreSHA, wantChildSHA)
	}
	if childRow.NewBaseSHA != wantParentSHA {
		t.Fatalf("childRow.NewBaseSHA = %q, want %q (real git rev-parse physical/parent, reached via the parent's GitBranch())", childRow.NewBaseSHA, wantParentSHA)
	}
}

// TestCheckoutSyncPlan_D1bCollisionResolvesThroughGitBranch is the actual
// D1-b COLLISION fixture the CHANGELOG's "behaviour change" bullet claims.
// The previous test proves the identity rule where the logical name is not a
// ref at all — which cannot distinguish "resolved through GitBranch()" from
// "the logical name happened to be unusable". This one closes that gap:
//
//   - `physical/parent` is the parent entry's recorded Git branch;
//   - `parent-logical` ALSO exists as a real Git branch, at a DIVERGENT
//     commit (a different tip, reachable from neither of the others);
//   - the child's Base names the parent's LOGICAL name.
//
// The shipped pre-fix behaviour would rebase onto `parent-logical` by
// coincidence. The fixed behaviour resolves through `StackEntry.GitBranch()`,
// so the row's Base, its NewBaseSHA and therefore the landed destination are
// the PHYSICAL branch's — which is the exact silent argv/destination change
// the CHANGELOG documents. Both SHAs are asserted, and they are asserted to
// DIFFER, so a fixture where the two happened to agree could not pass this
// test vacuously.
func TestCheckoutSyncPlan_D1bCollisionResolvesThroughGitBranch(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-d1b-collision"

	stack := Stack{Branches: []StackEntry{
		{Name: "parent-logical", Branch: "physical/parent", Base: "main"},
		{Name: "child-logical", Branch: "physical/child", Base: "parent-logical"},
	}}
	addStackEntries(t, ws, feature, stack.Branches)

	// The parent's physical branch gets its own commit...
	gitInTest(t, dir, "branch", "physical/parent", "main")
	gitInTest(t, dir, "checkout", "physical/parent")
	writeFileInTest(t, dir, "physical-parent.txt", "physical parent tip\n")
	gitInTest(t, dir, "add", ".")
	gitInTest(t, dir, "commit", "-m", "physical parent commit")
	physicalParentSHA := gitInTest(t, dir, "rev-parse", "physical/parent")

	// ...and the COLLIDING branch that shares the parent's LOGICAL name gets
	// a different one, so the two tips genuinely diverge.
	gitInTest(t, dir, "checkout", "main")
	gitInTest(t, dir, "branch", "parent-logical", "main")
	gitInTest(t, dir, "checkout", "parent-logical")
	writeFileInTest(t, dir, "logical-collision.txt", "colliding logical-name tip\n")
	gitInTest(t, dir, "add", ".")
	gitInTest(t, dir, "commit", "-m", "colliding logical-name commit")
	collidingSHA := gitInTest(t, dir, "rev-parse", "parent-logical")

	if physicalParentSHA == collidingSHA {
		t.Fatalf("fixture is vacuous: physical/parent and parent-logical are both at %s", physicalParentSHA)
	}

	gitInTest(t, dir, "checkout", "main")
	gitInTest(t, dir, "branch", "physical/child", "physical/parent")

	order, err := TopoSort(stack)
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	plan, err := buildCheckoutPlanFrom(dir, stack, order, SyncSelection{})
	if err != nil {
		t.Fatalf("buildCheckoutPlanFrom: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("len(plan) = %d, want 2", len(plan))
	}
	childRow := plan[1]

	if childRow.Base != "physical/parent" {
		t.Fatalf("childRow.Base = %q, want physical/parent: the colliding real branch %q must NOT win", childRow.Base, "parent-logical")
	}
	if childRow.NewBaseSHA != physicalParentSHA {
		t.Fatalf("childRow.NewBaseSHA = %q, want %q (physical/parent); got the colliding logical-name tip %q instead?",
			childRow.NewBaseSHA, physicalParentSHA, collidingSHA)
	}
	if childRow.NewBaseSHA == collidingSHA {
		t.Fatalf("childRow.NewBaseSHA resolved the COLLIDING logical-name branch %q — this is exactly the D1-b regression", collidingSHA)
	}
}

// writeFileInTest writes a file inside a fixture repository, failing the
// test on any error.
func writeFileInTest(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestCheckoutSyncPlan_BuildCheckoutPlanFrom_NeverSorts confirms
// buildCheckoutPlanFrom's own doc contract: it walks order EXACTLY as given,
// never re-sorting it, so a caller-supplied (deliberately non-topological)
// order is echoed back in the SAME sequence rather than corrected.
func TestCheckoutSyncPlan_BuildCheckoutPlanFrom_NeverSorts(t *testing.T) {
	dir, _ := cspFixture(t)
	stack := Stack{Branches: []StackEntry{
		{Name: "a", Base: "main"},
		{Name: "b", Base: "main"},
	}}
	gitInTest(t, dir, "branch", "a")
	gitInTest(t, dir, "branch", "b")

	// Deliberately reversed relative to declaration order.
	reversed := []StackEntry{stack.Branches[1], stack.Branches[0]}
	plan, err := buildCheckoutPlanFrom(dir, stack, reversed, SyncSelection{})
	if err != nil {
		t.Fatalf("buildCheckoutPlanFrom: %v", err)
	}
	if len(plan) != 2 || plan[0].Name != "b" || plan[1].Name != "a" {
		t.Fatalf("plan = %+v, want [b a] echoing the caller's own order verbatim", plan)
	}
}

// TestCheckoutSyncPlan_BuildCheckoutPlanFrom_SkipRules exercises the three
// documented skip rules together: an archived entry is skipped outright, a
// root entry (empty Base) is skipped, and a scoped selection excludes any
// entry not in sel.Names.
func TestCheckoutSyncPlan_BuildCheckoutPlanFrom_SkipRules(t *testing.T) {
	dir, _ := cspFixture(t)
	stack := Stack{Branches: []StackEntry{
		{Name: "root", Base: ""},
		{Name: "archived", Base: "main", Archived: true},
		{Name: "in-scope", Base: "main"},
		{Name: "out-of-scope", Base: "main"},
	}}
	for _, name := range []string{"archived", "in-scope", "out-of-scope"} {
		gitInTest(t, dir, "branch", name)
	}

	sel := SyncSelection{Names: map[string]bool{"in-scope": true}}
	plan, err := buildCheckoutPlanFrom(dir, stack, stack.Branches, sel)
	if err != nil {
		t.Fatalf("buildCheckoutPlanFrom: %v", err)
	}
	if len(plan) != 1 || plan[0].Name != "in-scope" {
		t.Fatalf("plan = %+v, want exactly one row: in-scope (root/archived/out-of-scope all skipped)", plan)
	}
}

// ---------------------------------------------------------------------------
// Group C: the two document builders' fetch dispatch, PlanCheckoutRebase's
// own Continue-flag dispatch, and PlanWriters' prose-only writer contract.
// ---------------------------------------------------------------------------

// cspAddOrigin creates a real, local, network-free bare remote (a second
// real Git repository, not a mock) and wires it up as repoDir's "origin",
// so a real `git fetch` has something reachable to fetch from.
func cspAddOrigin(t *testing.T, repoDir string) {
	t.Helper()
	bareDir := t.TempDir()
	gitInTest(t, bareDir, "init", "-q", "--bare")
	gitInTest(t, repoDir, "remote", "add", "origin", bareDir)
}

// TestCheckoutSyncPlan_BuildCheckoutRebasePlan_FetchesOnFreshRunWhenPolicyEnabled
// proves the fresh route's own at-most-once fetch really runs a real `git
// fetch` against a real, reachable local remote when nothing blocks it: an
// enabled fetch policy, an applying/blocker-free FetchPlan, available rows,
// passing gates, a passing preflight and no live foreign lock.
func TestCheckoutSyncPlan_BuildCheckoutRebasePlan_FetchesOnFreshRunWhenPolicyEnabled(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-fetch-fresh"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	gitInTest(t, dir, "branch", "child")
	cspAddOrigin(t, dir)

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchEnabled))
	var prose bytes.Buffer
	plan, err := BuildCheckoutRebasePlan(opts, PlanWriters{Prose: &prose})
	if err != nil {
		t.Fatalf("BuildCheckoutRebasePlan: %v", err)
	}
	if !plan.Fetch.Attempted {
		t.Fatalf("Fetch.Attempted = false, want true: policy enabled, applying blocker-free FetchPlan, nothing else in the way (plan=%+v)", plan.Fetch)
	}
	if len(plan.Fetch.Repos) != 1 {
		t.Fatalf("len(Fetch.Repos) = %d, want exactly 1", len(plan.Fetch.Repos))
	}
	if !plan.Fetch.Repos[0].Attempted {
		t.Fatal("Fetch.Repos[0].Attempted = false, want true")
	}
	if !plan.Fetch.Repos[0].OK {
		t.Fatal("Fetch.Repos[0].OK = false, want true: a real `git fetch` against a real, reachable local bare remote must succeed")
	}
	if !strings.Contains(prose.String(), "Fetching") {
		t.Fatalf("prose = %q, want it to contain the human Fetching line", prose.String())
	}
}

// TestCheckoutSyncPlan_BuildCheckoutContinuationPlan_NeverFetchesEvenWithPolicyEnabled
// is the structural counterpart: same fetch-enabled policy, same real
// reachable remote, but a persisted transaction and Continue: true. Because
// BuildCheckoutContinuationPlan always threads an empty, zero-value
// PlanFetchOutcome into checkoutPlanRequest — there is no code path in it
// that could ever call fetchCheckoutRepoTo — the resulting document must
// show no fetch attempt at all, despite everything else being "ready" to
// fetch.
func TestCheckoutSyncPlan_BuildCheckoutContinuationPlan_NeverFetchesEvenWithPolicyEnabled(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-fetch-continuation"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	gitInTest(t, dir, "branch", "child")
	cspAddOrigin(t, dir)

	tx := &CheckoutTransaction{
		StateVersion:   1,
		Feature:        feature,
		StartedAt:      "2026-01-01T00:00:00Z",
		OriginalBranch: "main",
		OriginalHEAD:   gitInTest(t, dir, "rev-parse", "HEAD"),
		Plan:           []CheckoutPlanEntry{{Name: "child", Branch: "child", Base: "main"}},
		CurrentIndex:   0,
		Stage:          StagePlanned,
	}
	if err := SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatalf("SaveCheckoutTransaction: %v", err)
	}

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchEnabled))
	opts.Continue = true
	plan, err := BuildCheckoutContinuationPlan(opts)
	if err != nil {
		t.Fatalf("BuildCheckoutContinuationPlan: %v", err)
	}
	if plan.Fetch.Attempted {
		t.Fatal("Fetch.Attempted = true, want false: a continuation must never fetch, even under an enabled policy with a reachable remote ready to go")
	}
	if len(plan.Fetch.Repos) != 0 {
		t.Fatalf("len(Fetch.Repos) = %d, want 0 (never nil, but empty: no attempt was ever made)", len(plan.Fetch.Repos))
	}
}

// TestCheckoutSyncPlan_PlanCheckoutRebase_DispatchesOnContinueFlag proves
// PlanCheckoutRebase's own one-line dispatch by observing each route's
// distinct, independently-established behaviour through the SAME entry
// point and the SAME shared Prose writer: Continue: false reaches
// BuildCheckoutRebasePlan (which, under these fixtures, both fetches and
// writes a Fetching line to w.Prose); Continue: true reaches
// BuildCheckoutContinuationPlan, which never receives w at all and so can
// never write to it.
func TestCheckoutSyncPlan_PlanCheckoutRebase_DispatchesOnContinueFlag(t *testing.T) {
	t.Run("Continue=false", func(t *testing.T) {
		dir, ws := cspFixture(t)
		feature := "feat-dispatch-fresh"
		fp := ws.FeaturePath(feature)
		addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
		gitInTest(t, dir, "branch", "child")
		cspAddOrigin(t, dir)

		opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchEnabled))
		var prose bytes.Buffer
		plan, err := PlanCheckoutRebase(opts, PlanWriters{Prose: &prose})
		if err != nil {
			t.Fatalf("PlanCheckoutRebase: %v", err)
		}
		if !plan.Fetch.Attempted {
			t.Fatal("Fetch.Attempted = false, want true: Continue=false must dispatch to BuildCheckoutRebasePlan")
		}
		if prose.Len() == 0 {
			t.Fatal("prose is empty, want a Fetching line written through the SAME PlanWriters BuildCheckoutRebasePlan was given")
		}
	})

	t.Run("Continue=true", func(t *testing.T) {
		dir, ws := cspFixture(t)
		feature := "feat-dispatch-continue"
		fp := ws.FeaturePath(feature)
		addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
		gitInTest(t, dir, "branch", "child")
		cspAddOrigin(t, dir)

		tx := &CheckoutTransaction{
			Feature:        feature,
			OriginalBranch: "main",
			OriginalHEAD:   gitInTest(t, dir, "rev-parse", "HEAD"),
			Plan:           []CheckoutPlanEntry{{Name: "child", Branch: "child", Base: "main"}},
			Stage:          StagePlanned,
		}
		if err := SaveCheckoutTransaction(fp, tx); err != nil {
			t.Fatalf("SaveCheckoutTransaction: %v", err)
		}

		opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchEnabled))
		opts.Continue = true
		var prose bytes.Buffer
		plan, err := PlanCheckoutRebase(opts, PlanWriters{Prose: &prose})
		if err != nil {
			t.Fatalf("PlanCheckoutRebase: %v", err)
		}
		if plan.Fetch.Attempted {
			t.Fatal("Fetch.Attempted = true, want false: Continue=true must dispatch to BuildCheckoutContinuationPlan")
		}
		if prose.Len() != 0 {
			t.Fatalf("prose = %q, want empty: PlanCheckoutRebase's Continue=true branch calls BuildCheckoutContinuationPlan(opts), which never even receives w", prose.String())
		}
	})
}

// TestCheckoutSyncPlan_PlanWriters_ProseOnlyNoDocumentByteAndNoProcessGlobalStream
// captures the REAL process-global os.Stdout/os.Stderr around a fetching
// BuildCheckoutRebasePlan call and asserts both are untouched (the plan-only
// route's own doc-commented rule 1), while the SEPARATE Prose buffer it was
// given does receive the human "Fetching ... done" line — and never any
// document byte (no JSON object delimiter ever appears in prose, since a
// plan-only invocation never serializes/writes its own document there).
func TestCheckoutSyncPlan_PlanWriters_ProseOnlyNoDocumentByteAndNoProcessGlobalStream(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-prose-only"
	fp := ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	gitInTest(t, dir, "branch", "child")
	cspAddOrigin(t, dir)

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchEnabled))

	var prose bytes.Buffer
	var plan RebasePlan
	var err error
	stdout, stderr := captureStdoutAndStderr(t, func() {
		plan, err = BuildCheckoutRebasePlan(opts, PlanWriters{Prose: &prose})
	})
	if err != nil {
		t.Fatalf("BuildCheckoutRebasePlan: %v", err)
	}
	if stdout != "" {
		t.Fatalf("captured os.Stdout = %q, want empty: a plan-only route must never touch os.Stdout directly", stdout)
	}
	if stderr != "" {
		t.Fatalf("captured os.Stderr = %q, want empty: a plan-only route must never touch os.Stderr directly", stderr)
	}
	if !plan.Fetch.Attempted {
		t.Fatal("Fetch.Attempted = false, want true: this fixture is deliberately identical to the one already proven to fetch")
	}
	proseText := prose.String()
	if !strings.Contains(proseText, "Fetching") {
		t.Fatalf("prose = %q, want it to contain the human Fetching line", proseText)
	}
	if strings.ContainsAny(proseText, "{}") {
		t.Fatalf("prose = %q, want no JSON document delimiter ('{' or '}') ever written to the prose-only writer", proseText)
	}
}

// ============================================================================
// Group E: the guarded EXECUTION route (RunCheckoutSync/ContinueCheckoutSync)
// itself — proving the guard seam this suite's earlier groups only measured
// indirectly (through the plan-only producers) is actually wired into the
// real, mutating executor: a fresh run over its limit refuses before any
// branch moves and writes no transaction; an approved fresh run executes and
// persists state_version: 3 with its limits; an armed --continue upgrades a
// plain v2 transaction; and a fully unguarded run is byte-for-byte
// unchanged from before the guard seam existed (§13.2a, §13.5, §13.6).
// ============================================================================

// errCspGuardStop is the sentinel StepHook error these tests use to stop a
// real execution immediately after its first entry's own guard revalidation
// has run (processBranch's JIT seam fires before StepHook(StagePlanned, ...),
// so by the time this error is observed the guard has already had its say),
// so the persisted transaction can be inspected before any Git mutation.
var errCspGuardStop = errors.New("csp guard test stop")

// cspStopAtPlanned arms StepHook to stop the run the first time StagePlanned
// fires, and registers its own cleanup so StepHook never leaks into a later
// test.
func cspStopAtPlanned(t *testing.T) {
	t.Helper()
	StepHook = func(stage CheckoutStage, branchIndex int) error {
		if stage == StagePlanned {
			return errCspGuardStop
		}
		return nil
	}
	t.Cleanup(func() { StepHook = nil })
}

// cspChildWithReplayCandidate creates a "child" branch off main with exactly
// one commit of its own — a real replay candidate under a rebase onto main
// — then parks HEAD on a third, unrelated "parking" branch, deliberately
// neither "main" nor "child": ResolveSyncBase's default-branch rewrite
// treats a checked-out branch that equals an entry's literal Base as the
// remote default branch, rewriting "main" to "origin/main" even with no
// remote configured (leaving the range undeterminable), and separately, the
// guard's own collateral check refuses branch-checked-out-elsewhere when
// the entry under evaluation is itself the checked-out branch. Parking
// avoids both, so "child"'s one real commit is measured as exactly one
// candidate against the literal, unrewritten "main".
func cspChildWithReplayCandidate(t *testing.T, dir string) {
	t.Helper()
	gitInTest(t, dir, "checkout", "-b", "child")
	writeArtefact(t, filepath.Join(dir, "child.txt"), "child\n")
	gitInTest(t, dir, "add", ".")
	gitInTest(t, dir, "commit", "-m", "child commit")
	gitInTest(t, dir, "checkout", "main")
	gitInTest(t, dir, "checkout", "-b", "parking")
}

// TestCheckoutSyncPlan_RunCheckoutSync_GuardedFreshOverLimitRefusesBeforeAnyBranchMoves
// proves RunCheckoutSync's guarded fresh-route seam (§13.2a, §13.6 rule 1):
// internal.EvaluatePlanGuard, placed after the plan is built and before
// SaveCheckoutTransaction, refuses a real replay candidate that exceeds an
// armed --max-replay-total, and the refusal travels as a
// *PlanGuardRefusalError with StatePreserved: false, before AcquireCheckoutLock's
// own lock is left behind, before "child" has moved, and before any
// transaction byte is ever written.
func TestCheckoutSyncPlan_RunCheckoutSync_GuardedFreshOverLimitRefusesBeforeAnyBranchMoves(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-over-limit"
	fp := ws.FeaturePath(feature)
	cspChildWithReplayCandidate(t, dir)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	childBefore := gitInTest(t, dir, "rev-parse", "child")

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
	opts.PlanGuard = CheckoutPlanGuard{MaxTotal: intPtr(0)}

	err := RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("expected a refusal: one real replay candidate exceeds an armed limit of 0")
	}
	var refusal *PlanGuardRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v (%T), want a *PlanGuardRefusalError", err, err)
	}
	if refusal.StatePreserved {
		t.Fatal("StatePreserved = true, want false: this is a pre-mutation refusal, nothing was ever done that needs preserving")
	}
	if HasCheckoutTransaction(fp) {
		t.Fatal("a pre-mutation refusal must write no transaction")
	}
	if HasCheckoutLock(fp) {
		t.Fatal("a pre-mutation refusal must leave no lock behind")
	}
	if got := gitInTest(t, dir, "rev-parse", "child"); got != childBefore {
		t.Fatalf("child moved from %s to %s: a pre-mutation refusal must move no branch", childBefore, got)
	}
}

// TestCheckoutSyncPlan_RunCheckoutSync_GuardedApprovedRunPersistsGuardedVersionWithLimits
// is the approved counterpart: the identical real replay candidate, now
// under limits wide enough to admit it, executes exactly like an unguarded
// run up to the point its own StepHook fires, but — because the guard seam
// evaluated and approved the plan before SaveCheckoutTransaction — the
// transaction it persists carries state_version: 3, its route, and both
// armed limits, rather than the plain v2 an unguarded dispatch would write.
func TestCheckoutSyncPlan_RunCheckoutSync_GuardedApprovedRunPersistsGuardedVersionWithLimits(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-approved"
	fp := ws.FeaturePath(feature)
	cspChildWithReplayCandidate(t, dir)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	cspStopAtPlanned(t)

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
	opts.PlanGuard = CheckoutPlanGuard{MaxTotal: intPtr(5), MaxPerEntry: intPtr(5)}

	err := RunCheckoutSync(opts)
	if !errors.Is(err, errCspGuardStop) {
		t.Fatalf("err = %v, want the injected step-hook stop error: an approved plan must reach the same interruption point an unguarded run would", err)
	}

	tx, lerr := LoadCheckoutTransaction(fp)
	if lerr != nil {
		t.Fatalf("expected a persisted transaction: %v", lerr)
	}
	if tx.StateVersion != CheckoutTransactionGuardedVersion {
		t.Fatalf("StateVersion = %d, want the guarded %d: an approved guarded run must write its birth version", tx.StateVersion, CheckoutTransactionGuardedVersion)
	}
	if tx.MaxReplayPerEntry == nil || *tx.MaxReplayPerEntry != 5 {
		t.Fatalf("MaxReplayPerEntry = %v, want a pointer to 5: the armed limit must be persisted", tx.MaxReplayPerEntry)
	}
	if tx.MaxReplayTotal == nil || *tx.MaxReplayTotal != 5 {
		t.Fatalf("MaxReplayTotal = %v, want a pointer to 5: the armed limit must be persisted", tx.MaxReplayTotal)
	}
	if tx.Route != RouteLegacy {
		t.Fatalf("Route = %q, want %q: opts.NewMode was never set for this run", tx.Route, RouteLegacy)
	}
}

// TestCheckoutSyncPlan_ContinueCheckoutSync_ArmedResumeUpgradesV2Transaction
// proves ContinueCheckoutSync's own guard seam (§13.2a, §13.6 rule 4d): an
// armed --continue (opts.PlanGuard.Armed()) resuming a plain, pre-existing
// v2 transaction (one this test builds and saves directly, exactly as an
// older, unguarded run would have left behind) calls
// upgradeGuardedCheckoutTransaction below the seam and above resumeTransaction,
// so the persisted transaction is upgraded to state_version: 3 carrying the
// resume's own armed limit — a flagless resume of an already-guarded
// transaction stays guarded through PersistedGuarded alone, but this test
// exercises the upgrade path itself, on a transaction that was never guarded
// to begin with.
func TestCheckoutSyncPlan_ContinueCheckoutSync_ArmedResumeUpgradesV2Transaction(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-resume-upgrade"
	fp := ws.FeaturePath(feature)
	gitInTest(t, dir, "branch", "child")
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	headSHA := gitInTest(t, dir, "rev-parse", "HEAD")

	tx := &CheckoutTransaction{
		StateVersion:   CheckoutTransactionVersion,
		Feature:        feature,
		StartedAt:      "2026-01-01T00:00:00Z",
		OriginalBranch: "main",
		OriginalHEAD:   headSHA,
		Plan:           []CheckoutPlanEntry{{Name: "child", Branch: "child", Base: "main"}},
		CurrentIndex:   0,
		Stage:          StagePlanned,
	}
	if err := SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatalf("SaveCheckoutTransaction: %v", err)
	}
	if checkoutRecoveryIsGuarded(tx) {
		t.Fatal("the transaction this test built must start out NOT guarded, or the upgrade this test proves would be a no-op")
	}
	cspStopAtPlanned(t)

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
	opts.Continue = true
	opts.PlanGuard = CheckoutPlanGuard{MaxTotal: intPtr(5)}

	err := ContinueCheckoutSync(opts)
	if !errors.Is(err, errCspGuardStop) {
		t.Fatalf("err = %v, want the injected step-hook stop error", err)
	}

	got, lerr := LoadCheckoutTransaction(fp)
	if lerr != nil {
		t.Fatalf("expected the transaction to still be readable: %v", lerr)
	}
	if got.StateVersion != CheckoutTransactionGuardedVersion {
		t.Fatalf("StateVersion = %d, want upgraded to %d", got.StateVersion, CheckoutTransactionGuardedVersion)
	}
	if got.MaxReplayTotal == nil || *got.MaxReplayTotal != 5 {
		t.Fatalf("MaxReplayTotal = %v, want a pointer to 5: the armed resume must persist its own limit onto the upgraded transaction", got.MaxReplayTotal)
	}
	if !checkoutRecoveryIsGuarded(got) {
		t.Fatal("the upgraded transaction must now report guarded, so a later flagless resume stays guarded via PersistedGuarded")
	}
}

// TestCheckoutSyncPlan_RunCheckoutSync_UnguardedRunUnchanged is Group E's
// control: the identical fixture and policy as the approved-run test above,
// but with opts.PlanGuard left at its zero value. Guarded()'s own
// definition (Armed() || PersistedGuarded, and this run supplies neither)
// keeps this dispatch off the guard seam entirely, so it must still persist
// the plain, un-upgraded v2 transaction with no route and no limits — byte-
// identical to every pre-existing (pre-guard-seam) unguarded assertion this
// suite already relied on elsewhere.
func TestCheckoutSyncPlan_RunCheckoutSync_UnguardedRunUnchanged(t *testing.T) {
	dir, ws := cspFixture(t)
	feature := "feat-unguarded-control"
	fp := ws.FeaturePath(feature)
	cspChildWithReplayCandidate(t, dir)
	addStackEntries(t, ws, feature, []StackEntry{{Name: "child", Base: "main"}})
	cspStopAtPlanned(t)

	opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
	opts.NewMode = true // isolates this control to the v2-vs-v3 axis alone

	err := RunCheckoutSync(opts)
	if !errors.Is(err, errCspGuardStop) {
		t.Fatalf("err = %v, want the injected step-hook stop error", err)
	}

	tx, lerr := LoadCheckoutTransaction(fp)
	if lerr != nil {
		t.Fatalf("expected a persisted transaction: %v", lerr)
	}
	if tx.StateVersion != CheckoutTransactionVersion {
		t.Fatalf("StateVersion = %d, want the ordinary unguarded %d: an unguarded run must never be upgraded", tx.StateVersion, CheckoutTransactionVersion)
	}
	if tx.Route != "" {
		t.Fatalf("Route = %q, want empty: nothing populates it on an unguarded run", tx.Route)
	}
	if tx.MaxReplayPerEntry != nil || tx.MaxReplayTotal != nil {
		t.Fatalf("MaxReplayPerEntry=%v MaxReplayTotal=%v, want both nil: an unguarded run never persists limits", tx.MaxReplayPerEntry, tx.MaxReplayTotal)
	}
	if checkoutRecoveryIsGuarded(tx) {
		t.Fatal("an unguarded run's own persisted transaction must never itself report guarded")
	}
}

// cspCountAncestorProbes runs fn with a PATH-front `git` shim that appends
// every argv to a log and then execs the real git, and returns how many
// `merge-base --is-ancestor` processes fn caused. The real binary is
// resolved BEFORE the shim is installed, so the shim can never re-resolve
// itself.
func cspCountAncestorProbes(t *testing.T, fn func()) int {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("a real git must be resolvable before the shim is installed: %v", err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	fn()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "merge-base --is-ancestor") {
			n++
		}
	}
	return n
}

// ============================================================================
// Group F: finalizeTransaction's §22.13n whole-plan ancestry postcondition —
// proving the re-verification loop that runs once every entry has already
// been individually rebased and validated is a REAL, independent safety net
// rather than a restatement of processBranch's own per-entry check, and that
// a refusal there preserves recoverable state instead of silently completing
// or destroying it (checkout_sync.go's finalizeTransaction).
// ============================================================================

// cspEmptyTreeSHA is git's well-known, universally valid empty-tree object
// ID (the SHA-1 of a tree with zero entries). `git commit-tree
// cspEmptyTreeSHA` builds a brand-new, PARENTLESS commit that shares no
// common ancestor with anything else in the fixture repository, so forcing
// a branch onto it is a genuine ancestry break, never a same-lineage reset
// a real "is-ancestor" check could satisfy by accident.
const cspEmptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// cspFinalizePostconditionStack is the two-entry stack every test in this
// group shares: "second" depends on "first" by logical Name (D1-a), so
// TopoSort/buildCheckoutPlanFrom always order the plan [first, second] —
// making index 1 (len-1) the LAST plan index, and "first" the EARLIER entry
// a mid-flight hook can move out from under the whole-plan verification loop.
func cspFinalizePostconditionStack() []StackEntry {
	return []StackEntry{
		{Name: "first", Base: "main"},
		{Name: "second", Base: "first"},
	}
}

// cspFinalizePostconditionFixture builds a real two-entry checkout stack —
// "first" based on "main" with one commit of its own, "second" based on
// "first" with one commit of its own — leaves HEAD parked on "main" (which
// is in neither entry's plan row, so RunCheckoutSync's own precondition and
// restoration checks are undisturbed), and returns a real, history-
// disconnected "rogue" commit SHA a later StepHook can force "first" onto.
func cspFinalizePostconditionFixture(t *testing.T) (dir, feature, fp, rogueSHA string) {
	t.Helper()
	var ws Workspace
	dir, ws = cspFixture(t)
	feature = "feat-finalize-postcondition"
	fp = ws.FeaturePath(feature)
	addStackEntries(t, ws, feature, cspFinalizePostconditionStack())

	gitInTest(t, dir, "branch", "first", "main")
	gitInTest(t, dir, "checkout", "first")
	writeFileInTest(t, dir, "first.txt", "first\n")
	gitInTest(t, dir, "add", ".")
	gitInTest(t, dir, "commit", "-m", "first commit")

	gitInTest(t, dir, "branch", "second", "first")
	gitInTest(t, dir, "checkout", "second")
	writeFileInTest(t, dir, "second.txt", "second\n")
	gitInTest(t, dir, "add", ".")
	gitInTest(t, dir, "commit", "-m", "second commit")

	gitInTest(t, dir, "checkout", "main")

	rogueSHA = gitInTest(t, dir, "commit-tree", cspEmptyTreeSHA, "-m", "rogue: unrelated history")
	return dir, feature, fp, rogueSHA
}

// TestCheckoutSyncPlan_FinalizeTransaction_WholePlanAncestryPostcondition is
// the §22.13n finalization postcondition test: a real two-entry checkout
// sync run, with the SHIPPED, NON-RETURNING StepHook installed at its ONE
// shipped call site (doRebase's StageRebased transition), keyed on the LAST
// plan index (1 of 2) — the only index at which tx.CurrentIndex equals
// len(Plan)-1 throughout an entry's processing, since executeTransaction
// increments CurrentIndex only AFTER processBranch returns for that entry.
//
// The hook force-moves the EARLIER entry's branch ("first", plan index 0)
// onto a rogue, history-disconnected commit while the LAST entry ("second")
// is still mid-flight, and never itself returns an error — it only mutates
// Git. Because "second"'s own per-entry ancestry check (processBranch,
// immediately after doRebase returns) compares its tip against "first"'s
// ORIGINAL, plan-build-time SHA — a value resolved once into
// CheckoutPlanEntry.NewBaseSHA and never re-resolved — that check is
// unaffected by the move, so the run proceeds past processBranch and into
// finalizeTransaction. There, the WHOLE-PLAN re-verification loop
// re-resolves "first" as a LIVE ref, observes the moved tip, and is the
// thing that actually refuses.
func TestCheckoutSyncPlan_FinalizeTransaction_WholePlanAncestryPostcondition(t *testing.T) {
	t.Run("EarlierEntryMovedAtLastIndexRefusesAndPreservesState", func(t *testing.T) {
		dir, feature, fp, rogueSHA := cspFinalizePostconditionFixture(t)
		lastIndex := len(cspFinalizePostconditionStack()) - 1 // 1: "second", the last plan row

		var lastIndexHits int
		StepHook = func(stage CheckoutStage, branchIndex int) error {
			if stage == StageRebased && branchIndex == lastIndex {
				lastIndexHits++
				gitInTest(t, dir, "branch", "-f", "first", rogueSHA)
			}
			return nil
		}
		t.Cleanup(func() { StepHook = nil })

		opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))

		var err error
		stdout, stderr := captureStdoutAndStderr(t, func() {
			err = RunCheckoutSync(opts)
		})

		// (1) The shipped finalization sentence, matched EXACTLY: every byte
		// of it is composed by finalizeTransaction itself —
		// fmt.Errorf("final ancestry check failed: %s not descendant of %s",
		// pe.Branch, pe.Base) — never Git's own wording, so an exact match is
		// the right bar here (unlike a message that embeds raw Git output).
		const wantErr = "final ancestry check failed: first not descendant of main"
		if err == nil {
			t.Fatal("expected the whole-plan ancestry postcondition to refuse: 'first' was force-moved off main during 'second's own processing")
		}
		if err.Error() != wantErr {
			t.Fatalf("err = %q, want the shipped sentence %q", err.Error(), wantErr)
		}

		// Companion: the mutating hook fired EXACTLY once, at the last plan
		// index — proving this failure comes from ONE genuine, shipped-seam-
		// driven mutation (the shipped StageRebased call site, no new hook
		// site added), not a fixture that was already broken or a hook that
		// over-fired.
		if lastIndexHits != 1 {
			t.Fatalf("lastIndexHits = %d, want exactly 1: the shipped StageRebased call site must fire once for the last plan index", lastIndexHits)
		}

		// (4) "first" really is at the tip the hook moved it to: the failure
		// is a genuine, observable Git mutation, not incidental.
		if got := gitInTest(t, dir, "rev-parse", "first"); got != rogueSHA {
			t.Fatalf("git rev-parse first = %s, want %s (the hook's own target)", got, rogueSHA)
		}

		// (3) Marker-free: this test calls internal.RunCheckoutSync directly
		// and never goes through internal/cli, whose sync_plan_guard.go is
		// the ONLY place "plan-guard: " is ever written (PlanGuardRefusalError
		// itself never carries that prefix). This run is also unguarded
		// (opts.PlanGuard is the zero value), so no guard document, refusal,
		// or marker line is ever produced or has anywhere shipped to print
		// from in the first place.
		if combined := stdout + stderr; strings.Contains(combined, "plan-guard: ") {
			t.Fatalf("captured output = %q, want no %q marker: this run is unguarded", combined, "plan-guard: ")
		}

		// (2) The transaction is preserved, not deleted, so the operator can
		// recover: HasCheckoutTransaction must still report true, and the
		// reloaded transaction's stage/plan must reflect exactly the state
		// finalizeTransaction's own ancestry loop left behind — both entries
		// already individually completed, the loop returning before it ever
		// reaches StageRestoring.
		if !HasCheckoutTransaction(fp) {
			t.Fatal("HasCheckoutTransaction = false, want true: a finalization refusal must preserve the transaction for recovery")
		}
		tx, lerr := LoadCheckoutTransaction(fp)
		if lerr != nil {
			t.Fatalf("LoadCheckoutTransaction: %v, want the preserved transaction to still be readable", lerr)
		}
		if len(tx.Plan) != 2 || tx.Plan[0].Name != "first" || tx.Plan[1].Name != "second" {
			t.Fatalf("tx.Plan = %+v, want the two-entry [first second] plan preserved verbatim", tx.Plan)
		}
		if tx.CurrentIndex != len(tx.Plan) {
			t.Fatalf("tx.CurrentIndex = %d, want %d: both entries had already completed their own processing before finalization ran", tx.CurrentIndex, len(tx.Plan))
		}
		if len(tx.CompletedIndices) != 2 || tx.CompletedIndices[0] != 0 || tx.CompletedIndices[1] != 1 {
			t.Fatalf("tx.CompletedIndices = %v, want [0 1]: both entries finished before the whole-plan postcondition ran", tx.CompletedIndices)
		}
		if tx.Stage != StagePlanned {
			t.Fatalf("tx.Stage = %q, want %q: finalizeTransaction's ancestry loop returns before ever reaching StageRestoring", tx.Stage, StagePlanned)
		}
		if !HasCheckoutLock(fp) {
			t.Fatal("HasCheckoutLock = false, want true: a preserved, recoverable transaction must still hold its lock against a concurrent run")
		}

		// (6) The LastBaseSHA update written by the SaveStack IMMEDIATELY
		// ABOVE the ancestry loop STAYS WRITTEN: the refusing finalizer
		// neither rolls it back nor rewrites it, so the operator's recovery
		// resumes from the same stack.yaml the successful path would have
		// left. This is asserted against the transaction's own plan rows, so
		// a finalizer that silently skipped the SaveStack fails here.
		reloaded, lerr2 := LoadStack(fp)
		if lerr2 != nil {
			t.Fatalf("LoadStack after the refusal: %v", lerr2)
		}
		byName := map[string]string{}
		for _, e := range reloaded.Branches {
			byName[e.Name] = e.LastBaseSHA
		}
		for _, pe := range tx.Plan {
			if got := byName[pe.Name]; got != pe.NewBaseSHA {
				t.Fatalf("stack.yaml LastBaseSHA[%s] = %q, want the plan's own NewBaseSHA %q: the update above the ancestry loop must stay written",
					pe.Name, got, pe.NewBaseSHA)
			}
		}

		// (7) The failure is NATIVE, not a guard refusal: it is not a
		// *PlanGuardRefusalError under errors.As, and it carries neither the
		// plan-guard marker nor the state-preserved prefix, because the
		// refusing party is the shipped finalizer (§13.5 rule F, §6.4).
		var refusal *PlanGuardRefusalError
		if errors.As(err, &refusal) {
			t.Fatalf("err is a *PlanGuardRefusalError (%+v); the shipped finalizer's own postcondition must never be typed as a guard refusal", refusal)
		}
		if strings.Contains(err.Error(), "state-preserved: ") {
			t.Fatalf("err = %q, want no state-preserved prefix", err.Error())
		}
	})

	// (8) The GUARDED twin of the same postcondition. §22.13n binds
	// "executing routes only, guarded and unguarded alike": an armed run
	// must refuse with the IDENTICAL native sentence, leave the identical
	// artefacts, and still not produce a guard-typed error or a marker.
	t.Run("GuardedRunRefusesWithTheIdenticalNativeSentence", func(t *testing.T) {
		dir, feature, fp, rogueSHA := cspFinalizePostconditionFixture(t)
		lastIndex := len(cspFinalizePostconditionStack()) - 1

		StepHook = func(stage CheckoutStage, branchIndex int) error {
			if stage == StageRebased && branchIndex == lastIndex {
				gitInTest(t, dir, "branch", "-f", "first", rogueSHA)
			}
			return nil
		}
		t.Cleanup(func() { StepHook = nil })

		limit := 50
		opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
		opts.PlanGuard = CheckoutPlanGuard{MaxTotal: &limit}

		var err error
		stdout, stderr := captureStdoutAndStderr(t, func() {
			err = RunCheckoutSync(opts)
		})

		const wantErr = "final ancestry check failed: first not descendant of main"
		if err == nil || err.Error() != wantErr {
			t.Fatalf("guarded err = %v, want the identical shipped sentence %q", err, wantErr)
		}
		var refusal *PlanGuardRefusalError
		if errors.As(err, &refusal) {
			t.Fatalf("guarded err is a *PlanGuardRefusalError (%+v); the finalizer's postcondition is native on BOTH routes", refusal)
		}
		if combined := stdout + stderr; strings.Contains(combined, "plan-guard: ") {
			t.Fatalf("captured output = %q, want no plan-guard marker even on the guarded route", combined)
		}
		if !HasCheckoutTransaction(fp) || !HasCheckoutLock(fp) {
			t.Fatal("the guarded route must preserve the same two artefacts the unguarded one does")
		}
	})

	// (9) The PLAN route contributes ZERO EXTRA `merge-base --is-ancestor`
	// processes: it never runs finalizeTransaction, so the only ancestry
	// probes it issues are the shipped stack-wide pass's own — exactly one
	// per stack entry. The EXECUTING control twin, over the identical
	// fixture, issues strictly more, and the difference is precisely the
	// finalization loop's own len(Plan) probes plus the per-entry ones. Both
	// numbers are measured from a real argv log, never estimated.
	t.Run("PlanRouteAddsZeroFinalizationMergeBaseProbes", func(t *testing.T) {
		dir, feature, fp, _ := cspFinalizePostconditionFixture(t)
		opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
		entries := len(cspFinalizePostconditionStack())

		planProbes := cspCountAncestorProbes(t, func() {
			plan, err := PlanCheckoutRebase(opts, PlanWriters{Prose: io.Discard})
			if err != nil {
				t.Fatalf("PlanCheckoutRebase: %v", err)
			}
			if !plan.Restore.Applies {
				t.Fatalf("restore.applies = %v, want true on a fresh checkout plan with rows", plan.Restore.Applies)
			}
		})
		if HasCheckoutTransaction(fp) || HasCheckoutLock(fp) {
			t.Fatal("a --plan route must create neither a transaction nor a lock")
		}
		if planProbes != entries {
			t.Fatalf("plan route issued %d `merge-base --is-ancestor` processes, want exactly %d — one per stack entry, and ZERO from finalizeTransaction",
				planProbes, entries)
		}

		execProbes := cspCountAncestorProbes(t, func() {
			if err := RunCheckoutSync(opts); err != nil {
				t.Fatalf("the executing control must succeed: %v", err)
			}
		})
		if execProbes < planProbes+entries {
			t.Fatalf("executing route issued %d ancestry probes, want at least %d (the plan's %d plus finalizeTransaction's own one per plan row)",
				execProbes, planProbes+entries, planProbes)
		}
	})

	// (5) Control twin: the identical fixture, but with NO hook installed at
	// all (StepHook stays nil, its production zero value), so nothing ever
	// moves "first". This proves the refusal above is not vacuous: given the
	// SAME plan and the SAME finalizeTransaction whole-plan loop, an
	// undisturbed run completes normally, deletes its transaction, and
	// releases its lock.
	t.Run("ControlWithoutHookCompletesAndCleansUp", func(t *testing.T) {
		dir, feature, fp, _ := cspFinalizePostconditionFixture(t)

		opts := cspOpts(feature, fp, dir, cspAllPolicy(SyncFetchDisabled))
		if err := RunCheckoutSync(opts); err != nil {
			t.Fatalf("RunCheckoutSync = %v, want success: nothing disturbs 'first' in this control run", err)
		}

		if HasCheckoutTransaction(fp) {
			t.Fatal("HasCheckoutTransaction = true, want false: a successful run must delete its transaction")
		}
		if HasCheckoutLock(fp) {
			t.Fatal("HasCheckoutLock = true, want false: a successful run must release its lock")
		}
	})
}

// ===========================================================================
// §22.24i (x), checkout half — the checkout UPGRADE writer is a no-op on an
// already-`3` subject and touches exactly state_version, route and the
// effective limit keys.
//
// It lives here, in package internal, because upgradeGuardedCheckoutTransaction
// is unexported; the source-level five-site enumeration and the external
// upgrade writer's own no-op assertion are owned by
// TestSyncGuardedState_Criterion22_24i_x_VersionWritingSitesHaveOneOwnerEach.
// ===========================================================================

func TestCheckoutSyncPlan_Criterion22_24i_x_CheckoutUpgradeIsANoOpOnAGuardedTransaction(t *testing.T) {
	// Each cell needs its OWN workspace root: the transaction path is derived
	// from the feature path's grandparent `state/` directory, so two feature
	// paths under one root would collide on the same file.
	newFeaturePath := func(t *testing.T) string {
		t.Helper()
		fp := filepath.Join(t.TempDir(), ".tws", "features", "f")
		if err := os.MkdirAll(fp, 0o755); err != nil {
			t.Fatal(err)
		}
		return fp
	}

	t.Run("already_guarded_is_untouched", func(t *testing.T) {
		featurePath := newFeaturePath(t)
		limit := 5
		tx := &CheckoutTransaction{
			StateVersion: CheckoutTransactionGuardedVersion,
			Route:        RouteLegacy,
			Feature:      "f", Stage: StagePlanned,
			MaxReplayTotal: &limit,
		}
		if err := SaveCheckoutTransaction(featurePath, tx); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(CheckoutTransactionPath(featurePath))
		if err != nil {
			t.Fatal(err)
		}
		other := 99
		if err := upgradeGuardedCheckoutTransaction(featurePath, tx, nil, &other); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		after, err := os.ReadFile(CheckoutTransactionPath(featurePath))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("an already-guarded transaction was rewritten:\n before=%q\n after=%q", before, after)
		}
		if tx.MaxReplayTotal == nil || *tx.MaxReplayTotal != limit {
			t.Fatalf("MaxReplayTotal = %v, want the persisted %d untouched", tx.MaxReplayTotal, limit)
		}
		if tx.Route != RouteLegacy {
			t.Fatalf("Route = %q, want the persisted legacy route untouched", tx.Route)
		}
	})

	t.Run("unguarded_upgrade_touches_only_version_route_and_limits", func(t *testing.T) {
		featurePath := newFeaturePath(t)
		tx := &CheckoutTransaction{
			Feature: "f", OriginalBranch: "main", OriginalHEAD: "abc",
			Stage: StageConflict, CurrentIndex: 1,
			CompletedIndices: []int{0},
			Plan:             []CheckoutPlanEntry{{Name: "a", Branch: "a", Base: "main"}},
			TestCommand:      "make check",
		}
		snapshot := *tx
		limit := 3
		if err := upgradeGuardedCheckoutTransaction(featurePath, tx, nil, &limit); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		if tx.StateVersion != CheckoutTransactionGuardedVersion {
			t.Fatalf("StateVersion = %d, want the guarded version", tx.StateVersion)
		}
		if tx.Route == "" {
			t.Fatal("Route must be inherited, never left empty")
		}
		if tx.MaxReplayTotal == nil || *tx.MaxReplayTotal != limit {
			t.Fatalf("MaxReplayTotal = %v, want %d", tx.MaxReplayTotal, limit)
		}
		if tx.Feature != snapshot.Feature || tx.OriginalBranch != snapshot.OriginalBranch ||
			tx.OriginalHEAD != snapshot.OriginalHEAD || tx.Stage != snapshot.Stage ||
			tx.CurrentIndex != snapshot.CurrentIndex || tx.TestCommand != snapshot.TestCommand ||
			len(tx.Plan) != len(snapshot.Plan) || len(tx.CompletedIndices) != len(snapshot.CompletedIndices) {
			t.Fatalf("the upgrade touched a field outside {state_version, route, limits}:\n before=%+v\n after=%+v", snapshot, *tx)
		}
	})
}

// TestCheckoutSyncPlan_Criterion22_24c_RouteVersionMatrix is the named owner
// of criterion 22.24c's nil-safety and route-vs-version derivation. It drives
// the four production predicates of §13.6 rule 4 — txNewMode, its exported
// wrapper TransactionNewMode, PayloadNewMode and CheckoutTriggersNeedV2 —
// over the full cross product of {nil, absent route, route: new-mode,
// route: legacy, unknown route} x {no version, v1, v2, v3}, so a nil
// dereference or a version-only comparison in any of them fails here.
func TestCheckoutSyncPlan_Criterion22_24c_RouteVersionMatrix(t *testing.T) {
	routes := []struct {
		name  string
		route string
	}{
		{"absent-route", ""},
		{"route-new-mode", RouteNewMode},
		{"route-legacy", RouteLegacy},
		{"unknown-route", "sideways"},
	}
	versions := []int{0, 1, 2, 3}

	// want answers the question the production switch answers: an explicit
	// route decides outright, anything else falls back to the version.
	want := func(route string, version, floor int) bool {
		switch route {
		case RouteNewMode:
			return true
		case RouteLegacy:
			return false
		default:
			return version >= floor
		}
	}

	t.Run("nil-is-never-new-mode", func(t *testing.T) {
		if txNewMode(nil) || TransactionNewMode(nil) || checkoutRecoveryIsNewMode(nil) {
			t.Fatalf("TransactionNewMode(nil) = true, want false")
		}
		if PayloadNewMode(nil) {
			t.Fatalf("PayloadNewMode(nil) = true, want false")
		}
		if !CheckoutTriggersNeedV2(nil, nil) {
			t.Fatalf("CheckoutTriggersNeedV2(nil, nil) = false: an absent transaction must still refuse trigger flags")
		}
		if !CheckoutTriggersNeedV2(nil, errors.New("unreadable")) {
			t.Fatalf("CheckoutTriggersNeedV2(nil, err) = false: an unreadable transaction must still refuse trigger flags")
		}
	})

	for _, r := range routes {
		for _, v := range versions {
			name := fmt.Sprintf("%s/v%d", r.name, v)
			t.Run(name, func(t *testing.T) {
				tx := &CheckoutTransaction{Feature: "f", StateVersion: v, Route: r.route}
				payload := &SyncRunState{Feature: "f", StateVersion: v, Route: r.route}

				wantTx := want(r.route, v, CheckoutTransactionVersion)
				if got := TransactionNewMode(tx); got != wantTx {
					t.Fatalf("TransactionNewMode = %v, want %v", got, wantTx)
				}
				if got := txNewMode(tx); got != wantTx {
					t.Fatalf("txNewMode = %v, want %v: the exported wrapper and its unexported subject must not diverge", got, wantTx)
				}
				if got := checkoutRecoveryIsNewMode(tx); got != wantTx {
					t.Fatalf("checkoutRecoveryIsNewMode = %v, want %v", got, wantTx)
				}
				wantPayload := want(r.route, v, SyncRunStateVersion)
				if got := PayloadNewMode(payload); got != wantPayload {
					t.Fatalf("PayloadNewMode = %v, want %v", got, wantPayload)
				}

				// CheckoutTriggersNeedV2 is exactly !TransactionNewMode once
				// the transaction loaded, and unconditionally true when it
				// did not.
				if got := CheckoutTriggersNeedV2(tx, nil); got != !wantTx {
					t.Fatalf("CheckoutTriggersNeedV2(tx, nil) = %v, want %v", got, !wantTx)
				}
				if got := CheckoutTriggersNeedV2(tx, errors.New("boom")); !got {
					t.Fatalf("CheckoutTriggersNeedV2(tx, err) = %v, want true: a load error always needs v2", got)
				}

				// A route: legacy transaction at the guarded version is still
				// guarded — guardedness is a version fact, newness a route
				// fact, and the two predicates must not be collapsed.
				wantGuarded := v >= CheckoutTransactionGuardedVersion
				if got := TransactionGuarded(tx); got != wantGuarded {
					t.Fatalf("TransactionGuarded = %v, want %v", got, wantGuarded)
				}
			})
		}
	}
}
