package internal

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// ============================================================================
// TestRebasePlanner* covers internal/rebase_planner.go: the pure decision
// layer over EntryContexts, ResolveSyncBase, ExecutionOrder,
// DestinationDeferred, ReplayUpstream, RemainingRebaseEntries,
// RebaseStrategy, GateBlockers/GateControlledTokens, the six push producers
// and SelectPrimaryRefusal.
//
// Fixture helpers are shared with TestRebasePlanProbe* (rpp-prefixed, defined
// in rebase_plan_probe_test.go, same package): rppNeutralize, rppRepo,
// rppNotARepo, rppHeadSHA, rppCommitN, rppCommonDir, and gitInTest
// (checkout_health_test.go). No new fixture helpers are redeclared here.
// ============================================================================

// ============================================================================
// EntryContexts (§4.4, §9.1a Group A)
// ============================================================================

func TestRebasePlanner_EntryContexts(t *testing.T) {
	t.Run("CheckoutModeUsesLayoutRepoRoot", func(t *testing.T) {
		repo := rppRepo(t)
		layout := RebasePlanLayout{RepoRoot: repo}
		entry := StackEntry{Name: "x"}
		sel := SyncSelection{Entries: []SyncSelectedEntry{{Name: "x", Role: SyncRoleAnchor}}}

		result := EntryContexts(EntryContextInput{Entry: entry, Mode: ModeCheckout, Layout: layout, Selection: sel})
		if result.ExecutionDir != repo || result.ExecutionSource != "workspace-repo-root" {
			t.Fatalf("dir/source = %q/%q, want %q/workspace-repo-root", result.ExecutionDir, result.ExecutionSource, repo)
		}
		if result.Materialization != "materialized" {
			t.Fatalf("Materialization = %q, want materialized (checkout mode is always materialized)", result.Materialization)
		}
		if result.Role != "anchor" {
			t.Fatalf("Role = %q, want anchor", result.Role)
		}
		if result.ExecutionErr != nil {
			t.Fatalf("unexpected ExecutionErr: %v", result.ExecutionErr)
		}
		if result.ExecutionContext.ContextID == nil {
			t.Fatal("expected a non-nil ContextID on the success path")
		}
	})

	t.Run("ExternalMaterializedUsesWorktreePath", func(t *testing.T) {
		wtRepo := rppRepo(t)
		layout := RebasePlanLayout{WorktreesRoot: filepath.Dir(wtRepo)}
		entry := StackEntry{Name: filepath.Base(wtRepo)}
		sel := SyncSelection{Entries: []SyncSelectedEntry{{Name: entry.Name, Role: SyncRolePropagated}}}

		result := EntryContexts(EntryContextInput{Entry: entry, Mode: ModeExternal, Layout: layout, Selection: sel})
		if result.Materialization != "materialized" {
			t.Fatalf("Materialization = %q, want materialized", result.Materialization)
		}
		if result.ExecutionDir != wtRepo || result.ExecutionSource != "worktree" {
			t.Fatalf("dir/source = %q/%q, want %q/worktree", result.ExecutionDir, result.ExecutionSource, wtRepo)
		}
		if result.ExecutionErr != nil {
			t.Fatalf("unexpected ExecutionErr: %v", result.ExecutionErr)
		}
		if result.Role != "propagated" {
			t.Fatalf("Role = %q, want propagated", result.Role)
		}
	})

	t.Run("ExternalNonMaterializedUsesEntryRepo", func(t *testing.T) {
		entryRepo := rppRepo(t)
		layout := RebasePlanLayout{WorktreesRoot: t.TempDir()} // no subdir named after the entry
		entry := StackEntry{Name: "missing-worktree", Repo: entryRepo}
		sel := SyncSelection{Entries: []SyncSelectedEntry{{Name: entry.Name, Role: SyncRoleAnchor}}}

		result := EntryContexts(EntryContextInput{Entry: entry, Mode: ModeExternal, Layout: layout, Selection: sel})
		if result.Materialization == "materialized" {
			t.Fatal("expected a non-materialized classification: no worktree directory exists")
		}
		if result.ExecutionDir != entryRepo || result.ExecutionSource != "entry-repo" {
			t.Fatalf("dir/source = %q/%q, want %q/entry-repo", result.ExecutionDir, result.ExecutionSource, entryRepo)
		}
	})

	t.Run("ExternalNonMaterializedNoRepoUsesProcessCWD", func(t *testing.T) {
		oldCWD, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldCWD) })
		nonRepoCWD := rppNotARepo(t)
		if err := os.Chdir(nonRepoCWD); err != nil {
			t.Fatal(err)
		}

		layout := RebasePlanLayout{WorktreesRoot: t.TempDir()}
		entry := StackEntry{Name: "no-repo-no-worktree"}
		sel := SyncSelection{Entries: []SyncSelectedEntry{{Name: entry.Name, Role: SyncRoleAnchor}}}

		result := EntryContexts(EntryContextInput{Entry: entry, Mode: ModeExternal, Layout: layout, Selection: sel})
		if result.ExecutionDir != "" || result.ExecutionSource != "process-cwd" {
			t.Fatalf("dir/source = %q/%q, want \"\"/process-cwd", result.ExecutionDir, result.ExecutionSource)
		}
		if result.ExecutionErr == nil {
			t.Fatal("expected a non-nil ExecutionErr: the process cwd was deliberately made a non-repository")
		}
		if result.ExecutionContext.Source != "process-cwd" || result.ExecutionContext.ContextID != nil {
			t.Fatalf("ExecutionContext = %+v, want {Source: process-cwd, ContextID: nil} on the error path", result.ExecutionContext)
		}
	})

	t.Run("ArchivedEntryIsArchivedMetadataEvenWithAPresentWorktreeDirectory", func(t *testing.T) {
		entryRepo := rppRepo(t)
		worktreesRoot := t.TempDir()
		entry := StackEntry{Name: "archived-one", Repo: entryRepo, Archived: true}
		// Prove Archived wins outright: a directory really does exist at the
		// would-be worktree path, yet materialization must still be
		// archived-metadata, never materialized.
		if err := os.MkdirAll(filepath.Join(worktreesRoot, entry.Name), 0o755); err != nil {
			t.Fatal(err)
		}
		layout := RebasePlanLayout{WorktreesRoot: worktreesRoot}
		sel := SyncSelection{Entries: []SyncSelectedEntry{{Name: entry.Name, Role: SyncRoleAnchor}}}

		result := EntryContexts(EntryContextInput{Entry: entry, Mode: ModeExternal, Layout: layout, Selection: sel})
		if result.Materialization != "archived-metadata" {
			t.Fatalf("Materialization = %q, want archived-metadata", result.Materialization)
		}
		if result.ExecutionDir != entryRepo || result.ExecutionSource != "entry-repo" {
			t.Fatalf("dir/source = %q/%q, want %q/entry-repo (archived rows never resolve via worktree)", result.ExecutionDir, result.ExecutionSource, entryRepo)
		}
	})

	t.Run("BaseContextNotApplicableWhenBaseUnconfigured", func(t *testing.T) {
		repo := rppRepo(t)
		layout := RebasePlanLayout{RepoRoot: repo}
		entry := StackEntry{Name: "x"} // Base == ""
		sel := SyncSelection{Entries: []SyncSelectedEntry{{Name: "x", Role: SyncRoleAnchor}}}

		result := EntryContexts(EntryContextInput{Entry: entry, Mode: ModeCheckout, Layout: layout, Selection: sel})
		if result.BaseContext.Source != "not-applicable" {
			t.Fatalf("BaseContext = %+v, want {Source: not-applicable}", result.BaseContext)
		}
	})

	t.Run("BaseContextMirrorsExecutionContextWhenConfiguredOnSuccess", func(t *testing.T) {
		repo := rppRepo(t)
		layout := RebasePlanLayout{RepoRoot: repo}
		entry := StackEntry{Name: "x", Base: "main"}
		sel := SyncSelection{Entries: []SyncSelectedEntry{{Name: "x", Role: SyncRoleAnchor}}}

		result := EntryContexts(EntryContextInput{Entry: entry, Mode: ModeCheckout, Layout: layout, Selection: sel})
		if result.BaseContext.ContextID != result.ExecutionContext.ContextID {
			t.Fatal("expected BaseContext.ContextID to be the SAME pointer as ExecutionContext.ContextID (a literal struct copy, not a re-derivation)")
		}
		if result.BaseContext.Source != result.ExecutionContext.Source {
			t.Fatalf("BaseContext.Source = %q, ExecutionContext.Source = %q, want equal", result.BaseContext.Source, result.ExecutionContext.Source)
		}
	})

	t.Run("BaseContextOnErrorPathFollowsTheSameUnconfiguredVsConfiguredSplit", func(t *testing.T) {
		oldCWD, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldCWD) })
		nonRepoCWD := rppNotARepo(t)
		if err := os.Chdir(nonRepoCWD); err != nil {
			t.Fatal(err)
		}
		layout := RebasePlanLayout{WorktreesRoot: t.TempDir()}
		sel := SyncSelection{Entries: []SyncSelectedEntry{{Name: "a", Role: SyncRoleAnchor}, {Name: "b", Role: SyncRoleAnchor}}}

		withBase := EntryContexts(EntryContextInput{Entry: StackEntry{Name: "a", Base: "main"}, Mode: ModeExternal, Layout: layout, Selection: sel})
		if withBase.ExecutionErr == nil {
			t.Fatal("test setup error: expected a context failure")
		}
		if withBase.BaseContext.Source != withBase.ExecutionContext.Source || withBase.BaseContext.ContextID != nil {
			t.Fatalf("BaseContext = %+v, want it to mirror the error-shaped ExecutionContext", withBase.BaseContext)
		}

		withoutBase := EntryContexts(EntryContextInput{Entry: StackEntry{Name: "b"}, Mode: ModeExternal, Layout: layout, Selection: sel})
		if withoutBase.BaseContext.Source != "not-applicable" {
			t.Fatalf("BaseContext = %+v, want {Source: not-applicable} even on an error path when Base is unconfigured", withoutBase.BaseContext)
		}
	})
}

// ============================================================================
// ResolveSyncBase (§10)
// ============================================================================

func TestRebasePlanner_ResolveSyncBase(t *testing.T) {
	t.Run("EmptyBaseIsKindNone", func(t *testing.T) {
		result := ResolveSyncBase(Stack{}, StackEntry{Name: "x"}, "")
		if result.Kind != "none" || result.Base != "" || result.IsRemoteTracking || result.DependsOnName != "" {
			t.Fatalf("result = %+v, want the zero {Kind: none}", result)
		}
	})

	t.Run("SameRepoStackParentUsesItsGitBranchOutright", func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{
			{Name: "parent-logical", Branch: "parent-git-branch"},
			{Name: "child", Base: "parent-logical"},
		}}
		entry := StackEntry{Name: "child", Base: "parent-logical"}
		result := ResolveSyncBase(stack, entry, "")
		if result.Kind != "stack-entry" || result.DependsOnName != "parent-logical" {
			t.Fatalf("Kind/DependsOnName = %q/%q, want stack-entry/parent-logical", result.Kind, result.DependsOnName)
		}
		if result.Base != "parent-git-branch" {
			t.Fatalf("Base = %q, want parent-git-branch (GitBranch(), not Name)", result.Base)
		}
		if result.IsRemoteTracking {
			t.Fatal("a same-repo stack parent must never be rewritten to a remote-tracking ref")
		}
	})

	t.Run("SameNamedEntryInADifferentRepoFallsThroughToLiteralResolutionButStillNamesTheDependency", func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{
			{Name: "shared-name", Repo: "/other/repo"},
		}}
		entry := StackEntry{Name: "child", Base: "shared-name", Repo: ""}
		result := ResolveSyncBase(stack, entry, "")
		if result.Kind != "stack-entry" || result.DependsOnName != "shared-name" {
			t.Fatalf("Kind/DependsOnName = %q/%q, want stack-entry/shared-name (still a configured dependency)", result.Kind, result.DependsOnName)
		}
		if result.Base != "shared-name" {
			t.Fatalf("Base = %q, want the literal %q (cross-repo, not GitBranch()-resolved)", result.Base, "shared-name")
		}
	})

	t.Run("NoMatchingStackEntryIsLiteralRefWithNoDependency", func(t *testing.T) {
		result := ResolveSyncBase(Stack{}, StackEntry{Name: "x", Base: "some-literal-ref"}, "")
		if result.Kind != "literal-ref" || result.DependsOnName != "" {
			t.Fatalf("Kind/DependsOnName = %q/%q, want literal-ref/\"\"", result.Kind, result.DependsOnName)
		}
	})

	t.Run("DefaultBranchIsRewrittenToRemoteTrackingPerRepoCtx", func(t *testing.T) {
		repoMain := rppRepo(t) // default branch "main"
		repoTrunk := t.TempDir()
		gitInTest(t, repoTrunk, "init", "-q", "--initial-branch=trunk")
		gitInTest(t, repoTrunk, "commit", "-q", "--allow-empty", "-m", "init")

		onTrunk := ResolveSyncBase(Stack{}, StackEntry{Name: "x", Base: "trunk"}, repoTrunk)
		if !onTrunk.IsRemoteTracking || onTrunk.Base != "origin/trunk" {
			t.Fatalf("result = %+v, want IsRemoteTracking=true Base=origin/trunk (repoTrunk's own default branch is trunk)", onTrunk)
		}

		onMain := ResolveSyncBase(Stack{}, StackEntry{Name: "x", Base: "trunk"}, repoMain)
		if onMain.IsRemoteTracking || onMain.Base != "trunk" {
			t.Fatalf("result = %+v, want IsRemoteTracking=false Base=trunk (repoMain's own default branch is main, not trunk: repoCtx, not any global state, must decide this)", onMain)
		}
	})

	t.Run("EmptyRepoCtxFallsBackToTheGlobalDefaultBranch", func(t *testing.T) {
		oldCWD, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldCWD) })
		repo := rppRepo(t) // default branch "main"
		if err := os.Chdir(repo); err != nil {
			t.Fatal(err)
		}
		result := ResolveSyncBase(Stack{}, StackEntry{Name: "x", Base: "main"}, "")
		if !result.IsRemoteTracking || result.Base != "origin/main" {
			t.Fatalf("result = %+v, want IsRemoteTracking=true Base=origin/main (repoCtx==\"\" falls back to the process's own DefaultBranch())", result)
		}
	})

	t.Run("NonDefaultLiteralBaseIsUnchanged", func(t *testing.T) {
		repo := rppRepo(t)
		result := ResolveSyncBase(Stack{}, StackEntry{Name: "x", Base: "feature/other"}, repo)
		if result.IsRemoteTracking || result.Base != "feature/other" {
			t.Fatalf("result = %+v, want IsRemoteTracking=false Base=feature/other", result)
		}
	})
}

// ============================================================================
// ExecutionOrder (§10, §13.3)
// ============================================================================

func TestRebasePlanner_ExecutionOrder(t *testing.T) {
	t.Run("SelectionFiltersToNamedEntriesPreservingOrderSequence", func(t *testing.T) {
		order := []StackEntry{{Name: "a"}, {Name: "b"}, {Name: "c"}}
		sel := SyncSelection{Names: map[string]bool{"a": true, "c": true}}
		out := ExecutionOrder(ModeCheckout, Stack{}, order, RebasePlanLayout{}, sel)
		if len(out) != 2 || out[0].Entry.Name != "a" || out[1].Entry.Name != "c" {
			t.Fatalf("out = %+v, want [a c] in that order", out)
		}
	})

	t.Run("CheckoutModeAlwaysPass1EvenForAnArchivedEntry", func(t *testing.T) {
		order := []StackEntry{{Name: "a", Archived: true}}
		sel := SyncSelection{Names: map[string]bool{"a": true}}
		out := ExecutionOrder(ModeCheckout, Stack{}, order, RebasePlanLayout{}, sel)
		if len(out) != 1 || out[0].Pass != 1 {
			t.Fatalf("out = %+v, want Pass=1", out)
		}
	})

	t.Run("ExternalMaterializedIsPass1NonMaterializedIsPass2", func(t *testing.T) {
		worktreesRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(worktreesRoot, "present"), 0o755); err != nil {
			t.Fatal(err)
		}
		order := []StackEntry{{Name: "present"}, {Name: "archived", Archived: true}}
		sel := SyncSelection{Names: map[string]bool{"present": true, "archived": true}}
		out := ExecutionOrder(ModeExternal, Stack{}, order, RebasePlanLayout{WorktreesRoot: worktreesRoot}, sel)
		byName := map[string]OrderedExecution{}
		for _, oe := range out {
			byName[oe.Entry.Name] = oe
		}
		if byName["present"].Pass != 1 || byName["present"].Materialization != "materialized" {
			t.Fatalf("present = %+v, want Pass=1 materialized", byName["present"])
		}
		if byName["archived"].Pass != 2 || byName["archived"].Materialization != "archived-metadata" {
			t.Fatalf("archived = %+v, want Pass=2 archived-metadata", byName["archived"])
		}
	})

	t.Run("SkipAnchorTracksAnchorRoleAndLocalOnlyPropagationExactly", func(t *testing.T) {
		order := []StackEntry{{Name: "anchor"}, {Name: "propagated"}}
		sel := SyncSelection{
			Names: map[string]bool{"anchor": true, "propagated": true},
			Entries: []SyncSelectedEntry{
				{Name: "anchor", Role: SyncRoleAnchor},
				{Name: "propagated", Role: SyncRolePropagated},
			},
			Policy: SyncRunPolicy{Propagation: SyncPropagationLocalOnly},
		}
		out := ExecutionOrder(ModeCheckout, Stack{}, order, RebasePlanLayout{}, sel)
		byName := map[string]OrderedExecution{}
		for _, oe := range out {
			byName[oe.Entry.Name] = oe
		}
		if !byName["anchor"].SkipAnchor {
			t.Fatal("expected SkipAnchor=true for an anchor row under local-only propagation")
		}
		if byName["propagated"].SkipAnchor {
			t.Fatal("expected SkipAnchor=false for a propagated row regardless of policy")
		}

		fullPolicySel := sel
		fullPolicySel.Policy = SyncRunPolicy{Propagation: SyncPropagationFull}
		out2 := ExecutionOrder(ModeCheckout, Stack{}, order, RebasePlanLayout{}, fullPolicySel)
		for _, oe := range out2 {
			if oe.Entry.Name == "anchor" && oe.SkipAnchor {
				t.Fatal("expected SkipAnchor=false for an anchor row under full propagation")
			}
		}
	})

	t.Run("UpdatedByRefMarksTransitiveSameRepoAncestorsUnderExternalUnscoped", func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{
			{Name: "P", Base: "release", Archived: true},
			{Name: "C", Base: "P"},
		}}
		order := []StackEntry{stack.Branches[0], stack.Branches[1]}
		sel := SyncSelection{
			Names: map[string]bool{"P": true, "C": true},
			Entries: []SyncSelectedEntry{
				{Name: "P", Role: SyncRolePropagated},
				{Name: "C", Role: SyncRolePropagated},
			},
			Policy: SyncRunPolicy{ScopeKind: SyncScopeAll},
		}
		worktreesRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(worktreesRoot, "C"), 0o755); err != nil {
			t.Fatal(err)
		}
		out := ExecutionOrder(ModeExternal, stack, order, RebasePlanLayout{WorktreesRoot: worktreesRoot}, sel)
		byName := map[string]OrderedExecution{}
		for _, oe := range out {
			byName[oe.Entry.Name] = oe
		}
		if byName["C"].Pass != 1 {
			t.Fatalf("test setup error: expected C (materialized) at Pass 1, got %+v", byName["C"])
		}
		if byName["P"].Pass != 2 {
			t.Fatalf("test setup error: expected P (archived) at Pass 2, got %+v", byName["P"])
		}
		if !byName["P"].UpdatedByRef {
			t.Fatal("expected P.UpdatedByRef=true: C is an unscoped external pass-1 row whose --update-refs rebase carries its same-repo parent P along")
		}
	})

	t.Run("UpdatedByRefStopsAtADifferentRepoBoundary", func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{
			{Name: "P", Base: "release", Archived: true, Repo: ""},
			{Name: "D", Base: "P", Repo: "other-repo"},
		}}
		order := []StackEntry{stack.Branches[0], stack.Branches[1]}
		worktreesRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(worktreesRoot, "D"), 0o755); err != nil {
			t.Fatal(err)
		}
		sel := SyncSelection{
			Names: map[string]bool{"P": true, "D": true},
			Entries: []SyncSelectedEntry{
				{Name: "P", Role: SyncRolePropagated},
				{Name: "D", Role: SyncRolePropagated},
			},
			Policy: SyncRunPolicy{ScopeKind: SyncScopeAll},
		}
		out := ExecutionOrder(ModeExternal, stack, order, RebasePlanLayout{WorktreesRoot: worktreesRoot}, sel)
		for _, oe := range out {
			if oe.Entry.Name == "P" && oe.UpdatedByRef {
				t.Fatal("expected P.UpdatedByRef=false: D's own repo differs from P's, so the ancestor walk must stop at that boundary")
			}
		}
	})

	t.Run("SkipAnchorRowsNeverStartAWalk", func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{
			{Name: "P", Base: "release", Archived: true},
			{Name: "C", Base: "P"},
		}}
		order := []StackEntry{stack.Branches[0], stack.Branches[1]}
		worktreesRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(worktreesRoot, "C"), 0o755); err != nil {
			t.Fatal(err)
		}
		sel := SyncSelection{
			Names: map[string]bool{"P": true, "C": true},
			Entries: []SyncSelectedEntry{
				{Name: "P", Role: SyncRolePropagated},
				{Name: "C", Role: SyncRoleAnchor},
			},
			Policy: SyncRunPolicy{ScopeKind: SyncScopeAll, Propagation: SyncPropagationLocalOnly},
		}
		out := ExecutionOrder(ModeExternal, stack, order, RebasePlanLayout{WorktreesRoot: worktreesRoot}, sel)
		for _, oe := range out {
			if oe.Entry.Name == "C" && !oe.SkipAnchor {
				t.Fatal("test setup error: expected C.SkipAnchor=true")
			}
			if oe.Entry.Name == "P" && oe.UpdatedByRef {
				t.Fatal("expected P.UpdatedByRef=false: C is a SkipAnchor row and must never start an ancestor walk")
			}
		}
	})

	t.Run("ScopedRunNeverComputesUpdatedByRef", func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{
			{Name: "P", Base: "release", Archived: true},
			{Name: "C", Base: "P"},
		}}
		order := []StackEntry{stack.Branches[0], stack.Branches[1]}
		sel := SyncSelection{
			Names:   map[string]bool{"P": true, "C": true},
			Entries: []SyncSelectedEntry{{Name: "P", Role: SyncRolePropagated}, {Name: "C", Role: SyncRolePropagated}},
			Policy:  SyncRunPolicy{ScopeKind: SyncScopeOne, Selector: "C"},
		}
		out := ExecutionOrder(ModeExternal, stack, order, RebasePlanLayout{WorktreesRoot: t.TempDir()}, sel)
		for _, oe := range out {
			if oe.Entry.Name == "P" && oe.UpdatedByRef {
				t.Fatal("expected P.UpdatedByRef=false under a scoped run")
			}
		}
	})

	t.Run("CheckoutModeNeverComputesUpdatedByRef", func(t *testing.T) {
		stack := Stack{Branches: []StackEntry{
			{Name: "P", Base: "release", Archived: true},
			{Name: "C", Base: "P"},
		}}
		order := []StackEntry{stack.Branches[0], stack.Branches[1]}
		sel := SyncSelection{
			Names:   map[string]bool{"P": true, "C": true},
			Entries: []SyncSelectedEntry{{Name: "P", Role: SyncRolePropagated}, {Name: "C", Role: SyncRolePropagated}},
			Policy:  SyncRunPolicy{ScopeKind: SyncScopeAll},
		}
		out := ExecutionOrder(ModeCheckout, stack, order, RebasePlanLayout{}, sel)
		for _, oe := range out {
			if oe.UpdatedByRef {
				t.Fatal("expected UpdatedByRef=false everywhere under checkout mode")
			}
		}
	})
}

// ============================================================================
// DestinationDeferred (§10)
// ============================================================================

func TestRebasePlanner_DestinationDeferred(t *testing.T) {
	execution := []OrderedExecution{{Entry: StackEntry{Name: "parent"}}}
	cases := []struct {
		name       string
		role       string
		parentName string
		want       bool
	}{
		{"NonPropagatedRoleIsAlwaysFalse", "anchor", "parent", false},
		{"EmptyParentNameIsAlwaysFalse", string(SyncRolePropagated), "", false},
		{"PropagatedWithParentInExecutionIsTrue", string(SyncRolePropagated), "parent", true},
		{"PropagatedWithParentNotInExecutionIsFalse", string(SyncRolePropagated), "someone-else", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DestinationDeferred(tc.role, tc.parentName, execution)
			if got != tc.want {
				t.Fatalf("DestinationDeferred(%q, %q, ...) = %v, want %v", tc.role, tc.parentName, got, tc.want)
			}
		})
	}
}

// ============================================================================
// firstReplayHazard, upstreamProvenanceFor, ReplayUpstream (§5, §2.15 Group D)
// ============================================================================

func TestRebasePlanner_FirstReplayHazard(t *testing.T) {
	tok := func(s string) *string { return &s }
	// The full eleven-rank ladder, each row isolating exactly the ONE hazard
	// its rank names while every higher-ranked hazard is cleared, proving
	// precedence descends in this exact order and nothing skips a rank.
	base := ReplayUpstreamInput{
		ContextUsable: true, HeadUsable: true, BaseUnset: false, BaseRefMissing: false,
		CutoffUsage: "not_used", CutoffState: "", Deferred: false,
	}
	cases := []struct {
		name            string
		mutate          func(in *ReplayUpstreamInput)
		wantDeterminacy string
		wantReason      *string
	}{
		{"1Skipped", func(in *ReplayUpstreamInput) { in.Skipped = true }, "not-applicable", tok("not-executed")},
		{"2MergeRecreation", func(in *ReplayUpstreamInput) { in.RebaseMergesConfigValid, in.RebaseMergesRecreates = true, true }, "unknown", tok("merge-recreation")},
		{"3RepoUnavailable", func(in *ReplayUpstreamInput) { in.ContextUsable = false }, "unknown", tok("repo-unavailable")},
		{"4HeadRefMissing", func(in *ReplayUpstreamInput) { in.HeadUsable = false }, "unknown", tok("head-ref-missing")},
		{"5BaseUnset", func(in *ReplayUpstreamInput) { in.BaseUnset = true }, "unknown", tok("base-unset")},
		{"6BaseRefMissing", func(in *ReplayUpstreamInput) { in.BaseRefMissing = true }, "unknown", tok("base-ref-missing")},
		{"7CutoffUnresolvable", func(in *ReplayUpstreamInput) { in.CutoffUsage, in.CutoffState = "used", "unresolvable" }, "unknown", tok("cutoff-unresolvable")},
		{"8UpstreamDeferred", func(in *ReplayUpstreamInput) { in.Deferred = true }, "unknown", tok("upstream-deferred")},
		{"9NoRecordedCutoff", func(in *ReplayUpstreamInput) { in.CutoffUsage, in.CutoffState = "used", "absent" }, "snapshot", tok("no-recorded-cutoff")},
		{"10CutoffNotUsedOnArm", func(in *ReplayUpstreamInput) { in.CutoffUsage, in.CutoffState = "not_used", "present" }, "snapshot", tok("cutoff-not-used-on-arm")},
		{"11Exact", func(in *ReplayUpstreamInput) {}, "exact", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			determinacy, reason := firstReplayHazard(in)
			if determinacy != tc.wantDeterminacy {
				t.Fatalf("determinacy = %q, want %q", determinacy, tc.wantDeterminacy)
			}
			if (reason == nil) != (tc.wantReason == nil) || (reason != nil && *reason != *tc.wantReason) {
				t.Fatalf("reason = %v, want %v", reason, tc.wantReason)
			}
		})
	}

	t.Run("PrecedenceRank1BeatsEveryLowerRank", func(t *testing.T) {
		// Every hazard flag set simultaneously: rank 1 (Skipped) must still
		// win outright over all ten lower-ranked hazards.
		in := ReplayUpstreamInput{
			Skipped: true, RebaseMergesConfigValid: true, RebaseMergesRecreates: true,
			ContextUsable: false, HeadUsable: false, BaseUnset: true, BaseRefMissing: true,
			CutoffUsage: "used", CutoffState: "unresolvable", Deferred: true,
		}
		determinacy, reason := firstReplayHazard(in)
		if determinacy != "not-applicable" || reason == nil || *reason != "not-executed" {
			t.Fatalf("determinacy/reason = %q/%v, want not-applicable/not-executed", determinacy, reason)
		}
	})

	t.Run("PrecedenceRank8BeatsRanks9And10", func(t *testing.T) {
		in := base
		in.Deferred = true
		in.CutoffUsage, in.CutoffState = "used", "absent" // would itself be rank 9
		determinacy, reason := firstReplayHazard(in)
		if determinacy != "unknown" || reason == nil || *reason != "upstream-deferred" {
			t.Fatalf("determinacy/reason = %q/%v, want unknown/upstream-deferred", determinacy, reason)
		}
	})
}

func TestRebasePlanner_UpstreamProvenanceFor(t *testing.T) {
	tok := func(s string) *string { return &s }
	cases := []struct {
		name        string
		reason      *string
		cutoffUsage string
		want        string
	}{
		{"NilReasonWithCutoffUsedIsRecordedCutoff", nil, "used", "recorded-cutoff"},
		{"NilReasonWithCutoffNotUsedIsBaseRefSnapshot", nil, "not_used", "base-ref-snapshot"},
		{"UpstreamDeferredIsBaseRefDeferred", tok("upstream-deferred"), "used", "base-ref-deferred"},
		{"NoRecordedCutoffIsBaseRefSnapshot", tok("no-recorded-cutoff"), "used", "base-ref-snapshot"},
		{"CutoffNotUsedOnArmIsBaseRefSnapshot", tok("cutoff-not-used-on-arm"), "not_used", "base-ref-snapshot"},
		{"AnyOtherReasonIsUnknown", tok("repo-unavailable"), "not_used", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := upstreamProvenanceFor(tc.reason, tc.cutoffUsage)
			if got != tc.want {
				t.Fatalf("upstreamProvenanceFor(%v, %q) = %q, want %q", tc.reason, tc.cutoffUsage, got, tc.want)
			}
		})
	}
}

func TestRebasePlanner_ReplayUpstream(t *testing.T) {
	t.Run("MayDropBecomesEmptyIsAlwaysTrueRegardlessOfDeterminacy", func(t *testing.T) {
		blocked := ReplayUpstream(ReplayUpstreamInput{Skipped: true})
		if !blocked.MayDropBecomesEmpty {
			t.Fatal("expected MayDropBecomesEmpty=true even for a not-applicable row")
		}
	})

	t.Run("NonExactSnapshotRowPublishesNilCandidateDataAndNilCommits", func(t *testing.T) {
		result := ReplayUpstream(ReplayUpstreamInput{Skipped: true})
		if result.Range != nil || result.CandidateCount != nil || result.FirstCandidate != nil || result.Commits != nil ||
			result.CommitsListed != nil || result.CommitsTruncated != nil || result.CandidateDigest != nil || result.MayDropPatchEquivalent != nil {
			t.Fatalf("result = %+v, want every candidate-data field nil", result)
		}
	})

	t.Run("ExactDeterminacyRunsTheRealProbesOverATemporaryRepo", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "branch", "upstream-base")
		upstreamSHA := gitInTest(t, repo, "rev-parse", "upstream-base")
		rppCommitN(t, repo, 3, "feature")

		result := ReplayUpstream(ReplayUpstreamInput{
			ContextUsable: true, ExecDir: repo,
			HeadUsable: true, GitBranch: "main",
			UpstreamRef: "upstream-base", UpstreamSHA: upstreamSHA,
			CutoffUsage: "not_used",
		})
		if result.Determinacy != "exact" || result.Reason != nil {
			t.Fatalf("Determinacy/Reason = %q/%v, want exact/nil", result.Determinacy, result.Reason)
		}
		// §4.2/§5 rule 1: the range is the FULL upstream SHA, never the ref
		// name the row also publishes as replay.upstream_ref.
		if result.Range == nil || *result.Range != upstreamSHA+"..main" {
			t.Fatalf("Range = %v, want %q", result.Range, upstreamSHA+"..main")
		}
		if result.CandidateCount == nil || *result.CandidateCount != 3 {
			t.Fatalf("CandidateCount = %v, want 3", result.CandidateCount)
		}
		if result.FirstCandidate == nil {
			t.Fatal("expected a non-nil FirstCandidate")
		}
		if len(result.Commits) != 3 {
			t.Fatalf("Commits = %v, want 3 entries", result.Commits)
		}
		if result.CommitsListed == nil || *result.CommitsListed != 3 {
			t.Fatalf("CommitsListed = %v, want 3", result.CommitsListed)
		}
		if result.CommitsTruncated == nil || *result.CommitsTruncated {
			t.Fatalf("CommitsTruncated = %v, want false", result.CommitsTruncated)
		}
		if result.CandidateDigest == nil || *result.CandidateDigest == "" {
			t.Fatal("expected a non-empty CandidateDigest")
		}
		if result.MayDropPatchEquivalent == nil {
			t.Fatal("expected a non-nil MayDropPatchEquivalent")
		}
		if result.UpstreamRef == nil || *result.UpstreamRef != "upstream-base" {
			t.Fatalf("UpstreamRef = %v, want upstream-base", result.UpstreamRef)
		}
	})

	t.Run("SnapshotDeterminacyAlsoRunsTheRealProbes", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "branch", "upstream-base")
		upstreamSHA := gitInTest(t, repo, "rev-parse", "upstream-base")
		rppCommitN(t, repo, 1, "feature")

		result := ReplayUpstream(ReplayUpstreamInput{
			ContextUsable: true, ExecDir: repo,
			HeadUsable: true, GitBranch: "main",
			UpstreamRef: "upstream-base", UpstreamSHA: upstreamSHA,
			CutoffUsage: "used", CutoffState: "absent",
		})
		if result.Determinacy != "snapshot" || result.Reason == nil || *result.Reason != "no-recorded-cutoff" {
			t.Fatalf("Determinacy/Reason = %q/%v, want snapshot/no-recorded-cutoff", result.Determinacy, result.Reason)
		}
		if result.CandidateCount == nil || *result.CandidateCount != 1 {
			t.Fatalf("CandidateCount = %v, want 1 (a snapshot row still runs the real candidate probes)", result.CandidateCount)
		}
	})

	t.Run("EmptyCandidateRangeYieldsAnEmptyNonNilCommitsSlice", func(t *testing.T) {
		repo := rppRepo(t)
		result := ReplayUpstream(ReplayUpstreamInput{
			ContextUsable: true, ExecDir: repo,
			HeadUsable: true, GitBranch: "main",
			UpstreamRef: "main", UpstreamSHA: rppHeadSHA(t, repo),
			CutoffUsage: "not_used",
		})
		if result.CandidateCount == nil || *result.CandidateCount != 0 {
			t.Fatalf("CandidateCount = %v, want 0", result.CandidateCount)
		}
		if result.Commits == nil || len(result.Commits) != 0 {
			t.Fatalf("Commits = %v, want a non-nil empty slice", result.Commits)
		}
		if result.FirstCandidate != nil {
			t.Fatal("expected a nil FirstCandidate for an empty range")
		}
	})

	t.Run("AProbeFailurePartwayThroughDowngradesToProbeFailedAndDropsPartialData", func(t *testing.T) {
		repo := rppRepo(t)
		result := ReplayUpstream(ReplayUpstreamInput{
			ContextUsable: true, ExecDir: repo,
			HeadUsable: true, GitBranch: "this-branch-does-not-exist",
			UpstreamRef: "main", UpstreamSHA: rppHeadSHA(t, repo),
			CutoffUsage: "not_used",
		})
		if result.Determinacy != "unknown" || result.Reason == nil || *result.Reason != "probe-failed" {
			t.Fatalf("Determinacy/Reason = %q/%v, want unknown/probe-failed", result.Determinacy, result.Reason)
		}
		if result.UpstreamProvenance != "unknown" {
			t.Fatalf("UpstreamProvenance = %q, want unknown", result.UpstreamProvenance)
		}
		if result.Range != nil || result.CandidateCount != nil || result.Commits != nil {
			t.Fatalf("result = %+v, want every candidate-data field dropped back to nil on a probe failure", result)
		}
		if result.UpstreamRef == nil || *result.UpstreamRef != "main" {
			t.Fatalf("UpstreamRef = %v, want main (preserved through the downgrade)", result.UpstreamRef)
		}
		if !result.MayDropBecomesEmpty {
			t.Fatal("expected MayDropBecomesEmpty=true even after a downgrade")
		}
	})
}

// ============================================================================
// RemainingRebaseEntries (§13.3)
// ============================================================================

func TestRebasePlanner_RemainingRebaseEntries(t *testing.T) {
	sel := SyncSelection{Entries: []SyncSelectedEntry{{Name: "a"}, {Name: "b"}, {Name: "c"}}}

	t.Run("FreshRunReturnsEverySelectedNameInSelectionOrder", func(t *testing.T) {
		got := RemainingRebaseEntries(RouteNewMode, RebasePlanLayout{}, RemainingEntriesState{Mode: ModeExternal}, nil, sel)
		want := []string{"a", "b", "c"}
		if !stringSlicesEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("OrderAndLayoutParametersAreGenuinelyUnusedByThisImplementation", func(t *testing.T) {
		// order is deliberately mismatched/bogus and layout is a non-zero
		// value neither of which this function's own doc comment says it
		// consults: the result must be identical regardless.
		bogusOrder := []StackEntry{{Name: "z"}, {Name: "y"}}
		bogusLayout := RebasePlanLayout{RepoRoot: "/does/not/exist", FeaturePath: "/also/bogus"}
		got := RemainingRebaseEntries(RouteNewMode, bogusLayout, RemainingEntriesState{Mode: ModeExternal}, bogusOrder, sel)
		want := []string{"a", "b", "c"}
		if !stringSlicesEqual(got, want) {
			t.Fatalf("got %v, want %v (order/layout must have no effect)", got, want)
		}
	})

	t.Run("CheckoutModeRemovesNamesBeforeTheTransactionCurrentIndex", func(t *testing.T) {
		tx := &CheckoutTransaction{
			Plan:         []CheckoutPlanEntry{{Name: "a"}, {Name: "b"}, {Name: "c"}},
			CurrentIndex: 2,
		}
		state := RemainingEntriesState{
			Mode: ModeCheckout,
			Checkout: CheckoutPlanState{
				Applicable: true,
				Files: CheckoutPlanFiles{
					CheckoutTransaction: PlanCheckoutTransactionFile{
						PlanFilePresence: PlanFilePresence{Presence: PlanPresenceReadable},
						Transaction:      tx,
					},
				},
			},
		}
		got := RemainingRebaseEntries(RouteNewMode, RebasePlanLayout{}, state, nil, sel)
		want := []string{"c"}
		if !stringSlicesEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("CheckoutFallsBackToPlanBranchWhenNameIsEmpty", func(t *testing.T) {
		tx := &CheckoutTransaction{
			Plan:         []CheckoutPlanEntry{{Branch: "a"}},
			CurrentIndex: 1,
		}
		state := RemainingEntriesState{
			Mode: ModeCheckout,
			Checkout: CheckoutPlanState{
				Applicable: true,
				Files: CheckoutPlanFiles{
					CheckoutTransaction: PlanCheckoutTransactionFile{
						PlanFilePresence: PlanFilePresence{Presence: PlanPresenceReadable},
						Transaction:      tx,
					},
				},
			},
		}
		got := RemainingRebaseEntries(RouteNewMode, RebasePlanLayout{}, state, nil, sel)
		want := []string{"b", "c"}
		if !stringSlicesEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("CheckoutCurrentIndexIsClampedToThePlanLength", func(t *testing.T) {
		tx := &CheckoutTransaction{
			Plan:         []CheckoutPlanEntry{{Name: "a"}, {Name: "b"}},
			CurrentIndex: 99, // must clamp to len(Plan) == 2, not run out of bounds
		}
		state := RemainingEntriesState{
			Mode: ModeCheckout,
			Checkout: CheckoutPlanState{
				Applicable: true,
				Files: CheckoutPlanFiles{
					CheckoutTransaction: PlanCheckoutTransactionFile{
						PlanFilePresence: PlanFilePresence{Presence: PlanPresenceReadable},
						Transaction:      tx,
					},
				},
			},
		}
		got := RemainingRebaseEntries(RouteNewMode, RebasePlanLayout{}, state, nil, sel)
		want := []string{"c"}
		if !stringSlicesEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("CheckoutNotApplicableLeavesEverythingRemaining", func(t *testing.T) {
		state := RemainingEntriesState{Mode: ModeCheckout, Checkout: CheckoutPlanState{Applicable: false}}
		got := RemainingRebaseEntries(RouteNewMode, RebasePlanLayout{}, state, nil, sel)
		if !stringSlicesEqual(got, []string{"a", "b", "c"}) {
			t.Fatalf("got %v, want everything remaining", got)
		}
	})

	t.Run("ExternalNewModeReadsThePayloadCompletedList", func(t *testing.T) {
		state := RemainingEntriesState{
			Mode: ModeExternal,
			External: ExternalPlanState{
				Applicable: true,
				Files: ExternalPlanFiles{
					Payload: PlanSyncRunPayloadFile{
						PlanFilePresence: PlanFilePresence{Presence: PlanPresenceReadable},
						Payload:          &SyncRunState{Completed: []string{"a"}},
					},
				},
			},
		}
		got := RemainingRebaseEntries(RouteNewMode, RebasePlanLayout{}, state, nil, sel)
		if !stringSlicesEqual(got, []string{"b", "c"}) {
			t.Fatalf("got %v, want [b c]", got)
		}
	})

	t.Run("ExternalLegacyRouteReadsTheLegacyStateCompletedListInstead", func(t *testing.T) {
		state := RemainingEntriesState{
			Mode: ModeExternal,
			External: ExternalPlanState{
				Applicable: true,
				Files: ExternalPlanFiles{
					Payload: PlanSyncRunPayloadFile{
						PlanFilePresence: PlanFilePresence{Presence: PlanPresenceReadable},
						Payload:          &SyncRunState{Completed: []string{"a", "b"}}, // must be ignored on RouteLegacy
					},
					LegacyState: PlanLegacySyncStateFile{
						PlanFilePresence: PlanFilePresence{Presence: PlanPresenceReadable},
						State:            &SyncState{Completed: []string{"a"}},
					},
				},
			},
		}
		got := RemainingRebaseEntries(RouteLegacy, RebasePlanLayout{}, state, nil, sel)
		if !stringSlicesEqual(got, []string{"b", "c"}) {
			t.Fatalf("got %v, want [b c] (from the legacy file, not the new-mode payload)", got)
		}
	})

	t.Run("ExternalNotApplicableLeavesEverythingRemaining", func(t *testing.T) {
		state := RemainingEntriesState{Mode: ModeExternal, External: ExternalPlanState{Applicable: false}}
		got := RemainingRebaseEntries(RouteNewMode, RebasePlanLayout{}, state, nil, sel)
		if !stringSlicesEqual(got, []string{"a", "b", "c"}) {
			t.Fatalf("got %v, want everything remaining", got)
		}
	})
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ============================================================================
// rebaseArgv, effectiveBackendFor, RebaseStrategy (§2.15 Group C, nine tokens)
// ============================================================================

func TestRebasePlanner_RebaseArgv(t *testing.T) {
	cases := []struct {
		name                      string
		mode                      WorkspaceMode
		base, branch, lastBaseSHA string
		onto, scoped              bool
		want                      []string
	}{
		{"CheckoutOnto", ModeCheckout, "base", "", "lastSHA", true, false, []string{"rebase", "--no-fork-point", "--onto", "base", "lastSHA"}},
		{"CheckoutPlain", ModeCheckout, "base", "", "", false, false, []string{"rebase", "--no-fork-point", "base"}},
		{"ExternalPass2ExplicitBranchIgnoresOntoAndScoped", ModeExternal, "base", "branch", "", true, true, []string{"rebase", "base", "branch"}},
		{"ExternalPass1ScopedOnto", ModeExternal, "base", "", "lastSHA", true, true, []string{"rebase", "--onto", "base", "lastSHA"}},
		{"ExternalPass1ScopedPlain", ModeExternal, "base", "", "", false, true, []string{"rebase", "base"}},
		{"ExternalPass1UnscopedOnto", ModeExternal, "base", "", "lastSHA", true, false, []string{"rebase", "--update-refs", "--onto", "base", "lastSHA"}},
		{"ExternalPass1UnscopedPlain", ModeExternal, "base", "", "", false, false, []string{"rebase", "--update-refs", "base"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rebaseArgv(tc.mode, tc.base, tc.branch, tc.lastBaseSHA, tc.onto, tc.scoped)
			if !stringSlicesEqual(got, tc.want) {
				t.Fatalf("rebaseArgv(...) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRebasePlanner_EffectiveBackendFor drives §11.7's TOTAL seven-row
// effective_backend table row by row, top-down, plus the two binding
// consequences the table itself states: configuration NEVER forces merge,
// and `--keep-base` is NOT a merge-forcing option.
func TestRebasePlanner_EffectiveBackendFor(t *testing.T) {
	merge, apply := "merge", "apply"
	ptr := func(s string) *string { return &s }

	cases := []struct {
		name string
		in   RebaseStrategyInput
		argv []string
		want *string
	}{
		{
			"Row1_MergeForcingArgvBeatsEveryConfigurationAndCapability",
			RebaseStrategyInput{BackendConfigReadable: true, BackendConfigValid: true, BackendConfigValue: "apply", CapDefaultBackendMerge: false, CapDefaultBackendMergeKnown: true},
			[]string{"rebase", "--update-refs", "base"},
			ptr(merge),
		},
		{
			"Row2_UnreadableInventoryIsNull",
			RebaseStrategyInput{BackendConfigReadable: false, CapDefaultBackendMerge: true, CapDefaultBackendMergeKnown: true},
			[]string{"rebase", "base"},
			nil,
		},
		{
			"Row3_ExplicitApplyAboveTheGate",
			RebaseStrategyInput{BackendConfigReadable: true, BackendConfigValid: true, BackendConfigValue: "apply", CapDefaultBackendMerge: true, CapDefaultBackendMergeKnown: true},
			[]string{"rebase", "base"},
			ptr(apply),
		},
		{
			"Row4_ExplicitMergeAboveTheGate",
			RebaseStrategyInput{BackendConfigReadable: true, BackendConfigValid: true, BackendConfigValue: "merge", CapDefaultBackendMerge: true, CapDefaultBackendMergeKnown: true},
			[]string{"rebase", "base"},
			ptr(merge),
		},
		{
			"Row5_AbsentValueAboveTheGateDefaultsToMerge",
			RebaseStrategyInput{BackendConfigReadable: true, BackendConfigValid: false, CapDefaultBackendMerge: true, CapDefaultBackendMergeKnown: true},
			[]string{"rebase", "base"},
			ptr(merge),
		},
		{
			"Row5_InvalidValueAboveTheGateDefaultsToMerge",
			RebaseStrategyInput{BackendConfigReadable: true, BackendConfigValid: true, BackendConfigValue: "something-unrecognized", CapDefaultBackendMerge: true, CapDefaultBackendMergeKnown: true},
			[]string{"rebase", "base"},
			ptr(merge),
		},
		{
			"Row6_BelowTheGateIsAlwaysApplyEvenWithAConfiguredMerge",
			RebaseStrategyInput{BackendConfigReadable: true, BackendConfigValid: true, BackendConfigValue: "merge", CapDefaultBackendMerge: false, CapDefaultBackendMergeKnown: true},
			[]string{"rebase", "base"},
			ptr(apply),
		},
		{
			"Row7_UnknownCapabilityIsNull",
			RebaseStrategyInput{BackendConfigReadable: true, BackendConfigValid: true, BackendConfigValue: "merge", CapDefaultBackendMergeKnown: false},
			[]string{"rebase", "base"},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveBackendFor(tc.in, tc.argv)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("effectiveBackendFor = %q, want null", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("effectiveBackendFor = null, want %q", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("effectiveBackendFor = %q, want %q", *got, *tc.want)
			}
		})
	}

	t.Run("ConfigurationNeverForcesMerge", func(t *testing.T) {
		// rebase.updateRefs / rebase.rebaseMerges are not inputs to the
		// table in any cell: an apply-configured context stays apply even
		// where those keys are set, which is precisely what the two rules
		// this value gates depend on.
		in := RebaseStrategyInput{BackendConfigReadable: true, BackendConfigValid: true, BackendConfigValue: "apply", CapDefaultBackendMerge: true, CapDefaultBackendMergeKnown: true}
		got := effectiveBackendFor(in, []string{"rebase", "base"})
		if got == nil || *got != apply {
			t.Fatalf("effectiveBackendFor = %v, want apply: configuration must never force the merge backend", got)
		}
	})

	t.Run("KeepBaseIsNotMergeForcing", func(t *testing.T) {
		if argvForcesMergeBackend([]string{"rebase", "--keep-base", "base"}) {
			t.Fatal("--keep-base must not be treated as a merge-forcing option")
		}
	})

	t.Run("EveryMergeForcingTokenIsRecognized", func(t *testing.T) {
		forcing := [][]string{
			{"rebase", "--update-refs", "base"},
			{"rebase", "--rebase-merges", "base"},
			{"rebase", "-r", "base"},
			{"rebase", "--interactive", "base"},
			{"rebase", "-i", "base"},
			{"rebase", "--exec", "make", "base"},
			{"rebase", "--exec=make", "base"},
			{"rebase", "-x", "make", "base"},
			{"rebase", "--autosquash", "base"},
			{"rebase", "--merge", "base"},
			{"rebase", "-m", "base"},
			{"rebase", "--strategy", "ort", "base"},
			{"rebase", "--strategy-option=ours", "base"},
			{"rebase", "-X", "ours", "base"},
			{"rebase", "--empty=drop", "base"},
			{"rebase", "--no-keep-empty", "base"},
			{"rebase", "--reapply-cherry-picks", "base"},
			{"rebase", "--no-reapply-cherry-picks", "base"},
			{"rebase", "--root"},
			{"rebase", "--trailer", "Signed-off-by: x", "base"},
		}
		for _, argv := range forcing {
			if !argvForcesMergeBackend(argv) {
				t.Errorf("argv %v must force the merge backend", argv)
			}
		}
		nonForcing := [][]string{
			{"rebase", "base"},
			{"rebase", "base", "gitbranch"},
			{"rebase", "--onto", "base", "lastSHA"},
			{"rebase", "--no-fork-point", "base"},
			{"rebase", "--root", "--onto", "base"},
		}
		for _, argv := range nonForcing {
			if argvForcesMergeBackend(argv) {
				t.Errorf("argv %v must NOT force the merge backend", argv)
			}
		}
	})
}

func TestRebasePlanner_RebaseStrategy(t *testing.T) {
	t.Run("EverySkipTokenPassesThroughVerbatimWithAnEmptyArgv", func(t *testing.T) {
		for _, tok := range []string{"skipped-no-base", "skipped-anchor", "skipped-archived", "skipped-updated-ref"} {
			t.Run(tok, func(t *testing.T) {
				result := RebaseStrategy(RebaseStrategyInput{Skip: tok, ContextUsable: false /* must not matter */})
				if result.Strategy != tok {
					t.Fatalf("Strategy = %q, want %q", result.Strategy, tok)
				}
				if result.Argv == nil || len(result.Argv) != 0 {
					t.Fatalf("Argv = %v, want a non-nil empty slice", result.Argv)
				}
				if result.EffectiveBackend != nil {
					t.Fatal("expected a nil EffectiveBackend for a skipped row")
				}
			})
		}
	})

	t.Run("ContextUnusableIsUnknownWithNilArgv", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{ContextUsable: false})
		if result.Strategy != "unknown" {
			t.Fatalf("Strategy = %q, want unknown", result.Strategy)
		}
		if result.Argv != nil {
			t.Fatalf("Argv = %v, want nil (distinct from the skip tokens' empty-but-non-nil Argv)", result.Argv)
		}
		if result.EffectiveBackend != nil {
			t.Fatal("expected a nil EffectiveBackend")
		}
	})

	t.Run("CheckoutHeadUnusableIsUnknown", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{Mode: ModeCheckout, ContextUsable: true, HeadUsable: false})
		if result.Strategy != "unknown" {
			t.Fatalf("Strategy = %q, want unknown", result.Strategy)
		}
	})

	t.Run("CheckoutUnresolvedBaseIsSkippedNoBase", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{Mode: ModeCheckout, ContextUsable: true, HeadUsable: true, BaseResolved: false})
		if result.Strategy != "skipped-no-base" {
			t.Fatalf("Strategy = %q, want skipped-no-base", result.Strategy)
		}
		if len(result.Argv) != 0 {
			t.Fatalf("Argv = %v, want empty", result.Argv)
		}
	})

	t.Run("CheckoutUnresolvableCurrentBaseSHAIsUnknown", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{Mode: ModeCheckout, ContextUsable: true, HeadUsable: true, BaseResolved: true, CurrentBaseSHA: ""})
		if result.Strategy != "unknown" {
			t.Fatalf("Strategy = %q, want unknown", result.Strategy)
		}
	})

	t.Run("CheckoutOntoWhenCheckoutOntoTrue", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{
			Mode: ModeCheckout, ContextUsable: true, HeadUsable: true, BaseResolved: true,
			CurrentBaseSHA: "newbase-sha", Base: "release", LastBaseSHA: "oldbase-sha", CheckoutOnto: true,
			BackendConfigReadable: true, BackendConfigValid: true, BackendConfigValue: "apply",
			CapDefaultBackendMerge: true, CapDefaultBackendMergeKnown: true,
		})
		if result.Strategy != "onto" {
			t.Fatalf("Strategy = %q, want onto", result.Strategy)
		}
		want := []string{"rebase", "--no-fork-point", "--onto", "newbase-sha", "oldbase-sha"}
		if !stringSlicesEqual(result.Argv, want) {
			t.Fatalf("Argv = %v, want %v", result.Argv, want)
		}
		if result.EffectiveBackend == nil || *result.EffectiveBackend != "apply" {
			t.Fatalf("EffectiveBackend = %v, want apply (checkout mode never forces --update-refs)", result.EffectiveBackend)
		}
	})

	t.Run("CheckoutPlainWhenCheckoutOntoFalse", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{
			Mode: ModeCheckout, ContextUsable: true, HeadUsable: true, BaseResolved: true,
			CurrentBaseSHA: "newbase-sha", Base: "release", CheckoutOnto: false,
		})
		if result.Strategy != "plain" {
			t.Fatalf("Strategy = %q, want plain", result.Strategy)
		}
		want := []string{"rebase", "--no-fork-point", "release"}
		if !stringSlicesEqual(result.Argv, want) {
			t.Fatalf("Argv = %v, want %v", result.Argv, want)
		}
	})

	t.Run("ExternalPass2IsPlainExplicitBranchIgnoringOntoPredicate", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{
			Mode: ModeExternal, ContextUsable: true, Pass: 2, Base: "release", GitBranch: "feature-x",
			LastBaseSHA: "old-sha", CurrentBaseSHA: "new-sha", // onto's own predicate would be true; pass 2 must ignore it
		})
		if result.Strategy != "plain-explicit-branch" {
			t.Fatalf("Strategy = %q, want plain-explicit-branch", result.Strategy)
		}
		want := []string{"rebase", "release", "feature-x"}
		if !stringSlicesEqual(result.Argv, want) {
			t.Fatalf("Argv = %v, want %v", result.Argv, want)
		}
	})

	t.Run("ExternalPass1PlainWhenLastBaseSHAEqualsCurrentBaseSHA", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{
			Mode: ModeExternal, ContextUsable: true, Pass: 1, Base: "release",
			LastBaseSHA: "same-sha", CurrentBaseSHA: "same-sha", Scoped: false,
		})
		if result.Strategy != "plain" {
			t.Fatalf("Strategy = %q, want plain", result.Strategy)
		}
		want := []string{"rebase", "--update-refs", "release"}
		if !stringSlicesEqual(result.Argv, want) {
			t.Fatalf("Argv = %v, want %v", result.Argv, want)
		}
		if result.EffectiveBackend == nil || *result.EffectiveBackend != "merge" {
			t.Fatalf("EffectiveBackend = %v, want merge (forced by --update-refs)", result.EffectiveBackend)
		}
	})

	t.Run("ExternalPass1OntoWithoutBaseMayMoveIsUnconditionalOnto", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{
			Mode: ModeExternal, ContextUsable: true, Pass: 1, Base: "release",
			LastBaseSHA: "old-sha", CurrentBaseSHA: "new-sha", Scoped: true, // scoped: EffectiveBackend not forced
			BackendConfigReadable: true, BackendConfigValid: true, BackendConfigValue: "apply",
			CapDefaultBackendMerge: true, CapDefaultBackendMergeKnown: true,
		})
		if result.Strategy != "onto" {
			t.Fatalf("Strategy = %q, want onto", result.Strategy)
		}
		want := []string{"rebase", "--onto", "release", "old-sha"}
		if !stringSlicesEqual(result.Argv, want) {
			t.Fatalf("Argv = %v, want %v", result.Argv, want)
		}
		if result.EffectiveBackend == nil || *result.EffectiveBackend != "apply" {
			t.Fatalf("EffectiveBackend = %v, want apply (scoped: not forced to merge)", result.EffectiveBackend)
		}
		if result.Condition != nil || result.ArgvAlternatives != nil {
			t.Fatalf("Condition/ArgvAlternatives = %v/%v, want nil/nil", result.Condition, result.ArgvAlternatives)
		}
	})

	t.Run("ExternalPass1OntoWithBaseMayMoveIsConditional", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{
			Mode: ModeExternal, ContextUsable: true, Pass: 1, Base: "release",
			LastBaseSHA: "old-sha", CurrentBaseSHA: "new-sha", Scoped: false,
			BaseMayMoveBeforeExecution: true,
		})
		if result.Strategy != "conditional" {
			t.Fatalf("Strategy = %q, want conditional", result.Strategy)
		}
		if result.Condition == nil || *result.Condition != "base-may-move-before-execution" {
			t.Fatalf("Condition = %v, want base-may-move-before-execution", result.Condition)
		}
		if result.Argv != nil {
			t.Fatalf("Argv = %v, want nil (only ArgvAlternatives is populated for conditional)", result.Argv)
		}
		if result.ArgvAlternatives == nil {
			t.Fatal("expected a non-nil ArgvAlternatives")
		}
		wantOnto := []string{"rebase", "--update-refs", "--onto", "release", "old-sha"}
		wantPlain := []string{"rebase", "--update-refs", "release"}
		if !stringSlicesEqual(result.ArgvAlternatives[0], wantOnto) || !stringSlicesEqual(result.ArgvAlternatives[1], wantPlain) {
			t.Fatalf("ArgvAlternatives = %v, want [%v %v]", *result.ArgvAlternatives, wantOnto, wantPlain)
		}
		if result.EffectiveBackend == nil || *result.EffectiveBackend != "merge" {
			t.Fatalf("EffectiveBackend = %v, want merge (unscoped pass 1 still forces merge even for a conditional row)", result.EffectiveBackend)
		}
	})

	t.Run("ExternalPass1PlainWhenLastBaseSHAEmpty", func(t *testing.T) {
		result := RebaseStrategy(RebaseStrategyInput{
			Mode: ModeExternal, ContextUsable: true, Pass: 1, Base: "release",
			LastBaseSHA: "", CurrentBaseSHA: "new-sha",
		})
		if result.Strategy != "plain" {
			t.Fatalf("Strategy = %q, want plain (onto requires a non-empty LastBaseSHA)", result.Strategy)
		}
	})
}

// ============================================================================
// GateBlockers, GateControlledTokens, sortAndDedupBlockers, SelectPrimaryRefusal
// ============================================================================

func strp(s string) *string { return &s }

func TestRebasePlanner_GateBlockers(t *testing.T) {
	t.Run("OnlyFiredAndOnGuardedRouteVerdictsContributeARowInInputOrder", func(t *testing.T) {
		entryA := strp("alpha")
		verdicts := []PlanGateVerdict{
			{Fired: true, OnGuardedRoute: true, Kind: RefusalHeadRefMissing, Entry: entryA, Detail: "head missing"},
			{Fired: false, OnGuardedRoute: true, Kind: RefusalBaseUnset, Detail: "not fired, excluded"},
			{Fired: true, OnGuardedRoute: false, Kind: RefusalPrunableWorktree, Detail: "fired but off-route, excluded"},
			{Fired: true, OnGuardedRoute: true, Kind: RefusalContextDirty, Detail: "dirty"},
		}
		got := GateBlockers(verdicts)
		want := []PlanBlocker{
			{Kind: RefusalHeadRefMissing, Entry: entryA, Detail: "head missing"},
			{Kind: RefusalContextDirty, Detail: "dirty"},
		}
		if len(got) != len(want) {
			t.Fatalf("GateBlockers returned %d rows, want %d: %+v", len(got), len(want), got)
		}
		for i := range want {
			if got[i].Kind != want[i].Kind || got[i].Detail != want[i].Detail || !blockerEntryEqualForTest(got[i].Entry, want[i].Entry) {
				t.Fatalf("row %d = %+v, want %+v (order must be preserved verbatim, no sort/dedup)", i, got[i], want[i])
			}
		}
	})

	t.Run("ZeroMatchesYieldsANonNilEmptySlice", func(t *testing.T) {
		got := GateBlockers(nil)
		if got == nil {
			t.Fatal("expected a non-nil empty slice")
		}
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})

	t.Run("DuplicateFiredOnRouteVerdictsAreNotDeduplicatedHere", func(t *testing.T) {
		verdicts := []PlanGateVerdict{
			{Fired: true, OnGuardedRoute: true, Kind: RefusalBaseUnset, Detail: "same"},
			{Fired: true, OnGuardedRoute: true, Kind: RefusalBaseUnset, Detail: "same"},
		}
		got := GateBlockers(verdicts)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (GateBlockers must not dedup; that is SelectPrimaryRefusal's job)", len(got))
		}
	})
}

func blockerEntryEqualForTest(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestRebasePlanner_GateControlledTokens(t *testing.T) {
	t.Run("EveryFiredVerdictContributesRegardlessOfOnGuardedRoute", func(t *testing.T) {
		verdicts := []PlanGateVerdict{
			{Fired: true, OnGuardedRoute: false, Controlled: ControlledLiveOwnerConcurrency},
			{Fired: false, OnGuardedRoute: true, Controlled: ControlledFetchSubmoduleReach}, // not fired: excluded
		}
		got := GateControlledTokens(verdicts)
		want := []ControlledPathBlocker{ControlledLiveOwnerConcurrency}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("GateControlledTokens = %v, want %v (off-route firing still counts)", got, want)
		}
	})

	t.Run("ResultIsSortedIntoTheCanonicalControlledPathBlockersOrderAndDeduplicated", func(t *testing.T) {
		verdicts := []PlanGateVerdict{
			{Fired: true, Controlled: ControlledOwnerArtefactUndecodable},
			{Fired: true, Controlled: ControlledFetchContextIndeterminate},
			{Fired: true, Controlled: ControlledFetchContextIndeterminate}, // duplicate token
			{Fired: true, Controlled: ControlledLiveOwnerConcurrency},
		}
		got := GateControlledTokens(verdicts)
		want := []ControlledPathBlocker{ControlledFetchContextIndeterminate, ControlledLiveOwnerConcurrency, ControlledOwnerArtefactUndecodable}
		if len(got) != len(want) {
			t.Fatalf("GateControlledTokens = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("GateControlledTokens = %v, want %v (canonical ControlledPathBlockers order)", got, want)
			}
		}
	})

	t.Run("ZeroMatchesYieldsANonNilEmptySlice", func(t *testing.T) {
		got := GateControlledTokens(nil)
		if got == nil {
			t.Fatal("expected a non-nil empty slice")
		}
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})
}

func TestRebasePlanner_SortAndDedupBlockers(t *testing.T) {
	t.Run("EveryMemberOfTheRealRefusalKindsTableSortsBackIntoItsDeclaredRankOrder", func(t *testing.T) {
		// Build one blocker per member of the actual exported RefusalKinds
		// slice (never a hand-retyped copy), inserted in REVERSED order, so a
		// passing sort is only possible if the implementation truly consults
		// the full, current rank table — not a partial or stale copy of it.
		n := len(RefusalKinds)
		if n == 0 {
			t.Fatal("RefusalKinds must not be empty")
		}
		reversed := make([]PlanBlocker, n)
		for i, k := range RefusalKinds {
			reversed[n-1-i] = PlanBlocker{Kind: k, Detail: "d"}
		}
		got := sortAndDedupBlockers(reversed)
		if len(got) != n {
			t.Fatalf("len(got) = %d, want %d", len(got), n)
		}
		for i, k := range RefusalKinds {
			if got[i].Kind != k {
				t.Fatalf("position %d = %q, want %q (full rank table must be honoured)", i, got[i].Kind, k)
			}
		}
	})

	t.Run("DoesNotMutateTheCallersBackingArray", func(t *testing.T) {
		in := []PlanBlocker{
			{Kind: RefusalLimitTotal, Detail: "z"},
			{Kind: RefusalPlanUnavailable, Detail: "a"},
		}
		inCopy := append([]PlanBlocker(nil), in...)
		_ = sortAndDedupBlockers(in)
		for i := range in {
			if in[i] != inCopy[i] {
				t.Fatalf("input slice was mutated: got %+v, want unchanged %+v", in, inCopy)
			}
		}
	})

	t.Run("NilEntryOrdersBeforeAnyNonNilEntryAtTheSameRank", func(t *testing.T) {
		e := "zzz-last-alphabetically"
		in := []PlanBlocker{
			{Kind: RefusalBaseUnset, Entry: &e, Detail: "d"},
			{Kind: RefusalBaseUnset, Entry: nil, Detail: "d"},
		}
		got := sortAndDedupBlockers(in)
		if len(got) != 2 || got[0].Entry != nil || got[1].Entry == nil {
			t.Fatalf("got = %+v, want nil-entry row first", got)
		}
	})

	t.Run("SameRankNonNilEntriesSortAlphabeticallyByEntry", func(t *testing.T) {
		in := []PlanBlocker{
			{Kind: RefusalBaseUnset, Entry: strp("bravo"), Detail: "d"},
			{Kind: RefusalBaseUnset, Entry: strp("alpha"), Detail: "d"},
		}
		got := sortAndDedupBlockers(in)
		if *got[0].Entry != "alpha" || *got[1].Entry != "bravo" {
			t.Fatalf("got = [%s %s], want [alpha bravo]", *got[0].Entry, *got[1].Entry)
		}
	})

	t.Run("SameRankSameEntrySortsByDetailAscending", func(t *testing.T) {
		in := []PlanBlocker{
			{Kind: RefusalBaseUnset, Entry: strp("x"), Detail: "zeta"},
			{Kind: RefusalBaseUnset, Entry: strp("x"), Detail: "alpha"},
		}
		got := sortAndDedupBlockers(in)
		if got[0].Detail != "alpha" || got[1].Detail != "zeta" {
			t.Fatalf("got Detail order = [%s %s], want [alpha zeta]", got[0].Detail, got[1].Detail)
		}
	})

	t.Run("ExactTupleDuplicatesCollapseToOne", func(t *testing.T) {
		in := []PlanBlocker{
			{Kind: RefusalBaseUnset, Entry: strp("x"), Detail: "same"},
			{Kind: RefusalBaseUnset, Entry: strp("x"), Detail: "same"},
			{Kind: RefusalBaseUnset, Entry: strp("x"), Detail: "same"},
		}
		got := sortAndDedupBlockers(in)
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1 (exact duplicates must collapse)", len(got))
		}
	})

	t.Run("DifferingOnlyByEntryDoesNotCollapse", func(t *testing.T) {
		in := []PlanBlocker{
			{Kind: RefusalBaseUnset, Entry: strp("alpha"), Detail: "same"},
			{Kind: RefusalBaseUnset, Entry: strp("bravo"), Detail: "same"},
		}
		got := sortAndDedupBlockers(in)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (differing Entry must not collapse)", len(got))
		}
	})

	t.Run("DifferingOnlyByDetailDoesNotCollapse", func(t *testing.T) {
		in := []PlanBlocker{
			{Kind: RefusalBaseUnset, Entry: strp("alpha"), Detail: "one"},
			{Kind: RefusalBaseUnset, Entry: strp("alpha"), Detail: "two"},
		}
		got := sortAndDedupBlockers(in)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (differing Detail must not collapse)", len(got))
		}
	})

	t.Run("EmptyInputYieldsANonNilEmptySlice", func(t *testing.T) {
		got := sortAndDedupBlockers(nil)
		if got == nil {
			t.Fatal("expected a non-nil empty slice")
		}
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})
}

func TestRebasePlanner_SelectPrimaryRefusal(t *testing.T) {
	t.Run("EmptyInputYieldsTheZeroKindAndANonNilEmptyOrderedSlice", func(t *testing.T) {
		kind, ordered := SelectPrimaryRefusal(nil)
		if kind != "" {
			t.Fatalf("kind = %q, want empty zero value", kind)
		}
		if ordered == nil || len(ordered) != 0 {
			t.Fatalf("ordered = %v, want a non-nil empty slice", ordered)
		}
	})

	t.Run("SingleBlockerIsItsOwnPrimary", func(t *testing.T) {
		kind, ordered := SelectPrimaryRefusal([]PlanBlocker{{Kind: RefusalContextDirty, Detail: "d"}})
		if kind != RefusalContextDirty {
			t.Fatalf("kind = %q, want %q", kind, RefusalContextDirty)
		}
		if len(ordered) != 1 {
			t.Fatalf("len(ordered) = %d, want 1", len(ordered))
		}
	})

	t.Run("HighestRankAmongTheWholeTableWinsRegardlessOfInputOrder", func(t *testing.T) {
		// RefusalLimitTotal is the LAST-ranked kind in the table and
		// RefusalPlanUnavailable is the FIRST; presenting Limit-total before
		// Plan-unavailable in the input must not change which one wins.
		kind, ordered := SelectPrimaryRefusal([]PlanBlocker{
			{Kind: RefusalLimitTotal, Detail: "low rank"},
			{Kind: RefusalPlanUnavailable, Detail: "highest rank"},
			{Kind: RefusalContextDirty, Detail: "middle rank"},
		})
		if kind != RefusalPlanUnavailable {
			t.Fatalf("kind = %q, want %q (rank-0 kind must always win)", kind, RefusalPlanUnavailable)
		}
		if ordered[0].Kind != RefusalPlanUnavailable || ordered[len(ordered)-1].Kind != RefusalLimitTotal {
			t.Fatalf("ordered = %+v, want plan-unavailable first and limit-total last", ordered)
		}
	})

	t.Run("ReturnsTheSameDeduplicatedOrderedSliceSortAndDedupBlockersWould", func(t *testing.T) {
		in := []PlanBlocker{
			{Kind: RefusalBaseUnset, Detail: "dup"},
			{Kind: RefusalBaseUnset, Detail: "dup"},
		}
		_, ordered := SelectPrimaryRefusal(in)
		direct := sortAndDedupBlockers(in)
		if len(ordered) != len(direct) || len(ordered) != 1 {
			t.Fatalf("ordered = %+v, want the same single deduplicated row sortAndDedupBlockers produces (%+v)", ordered, direct)
		}
	})
}

// ============================================================================
// The six push producers (§14.1a)
// ============================================================================

func rpnCaps() GitCapabilities {
	caps, _, err := ProbeGitCapabilities()
	if err != nil {
		panic(err)
	}
	return caps
}

func TestRebasePlanner_ResolvePushContext(t *testing.T) {
	t.Run("ResolvesIdentityHeadBranchAndRemoteOverARealRepo", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "remote", "add", "origin", "https://example.invalid/repo.git")
		inv := ProbeGitConfigInventory(repo)

		ctx, identity, remoteName, err := ResolvePushContext(repo, "cli-flag", "materialized", inv, rpnCaps())
		if err != nil {
			t.Fatalf("ResolvePushContext: %v", err)
		}
		if identity.ContextID == "" || identity.RepoRoot == "" {
			t.Fatalf("identity = %+v, want populated ContextID/RepoRoot", identity)
		}
		if ctx.ContextID == nil || *ctx.ContextID != identity.ContextID {
			t.Fatalf("ctx.ContextID = %v, want %q", ctx.ContextID, identity.ContextID)
		}
		if ctx.RepoRoot != identity.RepoRoot {
			t.Fatalf("ctx.RepoRoot = %q, want %q", ctx.RepoRoot, identity.RepoRoot)
		}
		if ctx.Source != "cli-flag" {
			t.Fatalf("ctx.Source = %q, want the passed-through cli-flag", ctx.Source)
		}
		if ctx.Materialization != "materialized" {
			t.Fatalf("ctx.Materialization = %q, want the passed-through materialized", ctx.Materialization)
		}
		if remoteName != "origin" {
			t.Fatalf("remoteName = %q, want origin", remoteName)
		}
	})

	t.Run("PropagatesEstablishContextIdentityErrorWithZeroResults", func(t *testing.T) {
		notRepo := rppNotARepo(t)
		ctx, identity, remoteName, err := ResolvePushContext(notRepo, "cli-flag", "materialized", GitConfigInventory{}, rpnCaps())
		if err == nil {
			t.Fatal("expected an error outside any repository")
		}
		if ctx != (PlanPushContext{}) || identity != (PlanContextIdentity{}) || remoteName != "" {
			t.Fatalf("expected every result zeroed on error, got ctx=%+v identity=%+v remoteName=%q", ctx, identity, remoteName)
		}
	})
}

func TestRebasePlanner_MeasurePushRemoteFacts(t *testing.T) {
	t.Run("RemoteFoundPopulatesRefspecsAndLeavesTrackingFieldsAtZeroValue", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "remote", "add", "origin", "https://example.invalid/repo.git")
		contextID := "ctx-1"
		inv := ProbeGitConfigInventory(repo)

		facts := MeasurePushRemoteFacts(&contextID, repo, "origin", inv, rpnCaps())
		if facts.ContextID != &contextID {
			t.Fatal("expected the same *string pointer to be threaded through verbatim")
		}
		if facts.CommonDir != repo {
			t.Fatalf("CommonDir = %q, want %q", facts.CommonDir, repo)
		}
		if facts.RemoteName != "origin" {
			t.Fatalf("RemoteName = %q, want origin", facts.RemoteName)
		}
		if !facts.MappingReadOK {
			t.Fatal("expected MappingReadOK=true")
		}
		if !facts.RemoteExists {
			t.Fatal("expected RemoteExists=true")
		}
		if len(facts.FetchRefspecs) == 0 {
			t.Fatal("expected at least one default fetch refspec for a newly added remote")
		}
		if facts.TrackingPhase != "not-applicable" {
			t.Fatalf("TrackingPhase = %q, want not-applicable (RefreshPushTrackingRefs alone sets it further)", facts.TrackingPhase)
		}
		if facts.TrackingReadOK {
			t.Fatal("expected TrackingReadOK to stay false: only RefreshPushTrackingRefs ever sets it")
		}
		if facts.TrackingRefs != nil {
			t.Fatal("expected TrackingRefs to stay nil until RefreshPushTrackingRefs runs")
		}
		if facts.FetchedThisRun {
			t.Fatal("expected FetchedThisRun to stay false")
		}
	})

	t.Run("RemoteNotFoundStillReportsMappingReadOK", func(t *testing.T) {
		repo := rppRepo(t)
		contextID := "ctx-1"
		facts := MeasurePushRemoteFacts(&contextID, repo, "ghost", ProbeGitConfigInventory(repo), rpnCaps())
		if !facts.MappingReadOK {
			t.Fatal("expected MappingReadOK=true even when the remote itself does not exist")
		}
		if facts.RemoteExists {
			t.Fatal("expected RemoteExists=false")
		}
		if facts.FetchRefspecs == nil || len(facts.FetchRefspecs) != 0 {
			t.Fatalf("FetchRefspecs = %v, want a non-nil empty slice when the remote was not found", facts.FetchRefspecs)
		}
	})

	// §22.24-adjacent, §14.1a rule 5a: MappingReadOK is a claim about TWO
	// halves — an established context identity and a READABLE ordered config
	// inventory. Either one missing is the `unknown` lease cell, never the
	// null-OID `absent` one.
	t.Run("UnreadableInventoryIsNotAReadMapping", func(t *testing.T) {
		repo := rppRepo(t)
		contextID := "ctx-1"
		facts := MeasurePushRemoteFacts(&contextID, repo, "origin", GitConfigInventory{Available: false}, rpnCaps())
		if facts.MappingReadOK {
			t.Fatal("MappingReadOK = true over an UNREADABLE inventory: nothing was read")
		}
		if facts.RemoteExists {
			t.Fatal("RemoteExists = true over an unreadable inventory")
		}
		if facts.FetchRefspecs == nil || len(facts.FetchRefspecs) != 0 {
			t.Fatalf("FetchRefspecs = %v, want a non-nil empty slice", facts.FetchRefspecs)
		}
		// Round trip: those facts really do produce §14.2's `unknown` cell.
		lease := ResolvePushLease(facts, "main")
		want := PlanLease{Mode: "implicit-remote-tracking", Expectation: "unknown", ExpectedSHAFreshness: "possibly-stale"}
		if lease != want {
			t.Fatalf("lease = %+v, want the cell-5 unknown lease %+v", lease, want)
		}
	})

	t.Run("UnestablishedContextIdentityIsNotAReadMapping", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "remote", "add", "origin", "https://example.invalid/repo.git")
		facts := MeasurePushRemoteFacts(nil, repo, "origin", ProbeGitConfigInventory(repo), rpnCaps())
		if facts.MappingReadOK {
			t.Fatal("MappingReadOK = true with a nil ContextID: the context was never established")
		}
		if lease := ResolvePushLease(facts, "main"); lease.Expectation != "unknown" {
			t.Fatalf("lease.Expectation = %q, want unknown", lease.Expectation)
		}
	})

	// A REAL probe failure, not an injected struct: the inventory is read
	// from a directory that is not a repository at all.
	t.Run("RealInventoryProbeFailureRoundTripsToTheUnknownCell", func(t *testing.T) {
		// A directory that does not exist at all: `git -C <path> config
		// --list --show-scope -z` cannot run there, so the ordered read
		// really fails rather than being injected as failed.
		missing := filepath.Join(rppNotARepo(t), "no-such-directory")
		inv := ProbeGitConfigInventory(missing)
		if inv.Available {
			t.Fatalf("the ordered inventory must fail for a nonexistent directory, got %+v", inv)
		}
		notRepo := missing
		contextID := "ctx-1"
		facts := MeasurePushRemoteFacts(&contextID, notRepo, "origin", inv, rpnCaps())
		if facts.MappingReadOK {
			t.Fatalf("MappingReadOK = true over a REAL failed inventory read: %+v", facts)
		}
		lease := ResolvePushLease(facts, "main")
		if lease.Expectation != "unknown" || lease.ExpectedRef != nil || lease.ExpectedSHA != nil {
			t.Fatalf("lease = %+v, want the unknown cell with null ref and sha", lease)
		}
		if lease.ExpectedSHAFreshness != "possibly-stale" {
			t.Fatalf("freshness = %q, want possibly-stale", lease.ExpectedSHAFreshness)
		}
	})
}

func TestRebasePlanner_RefreshPushTrackingRefs(t *testing.T) {
	// fetchedOK builds a PlanFetchOutcome describing one attempted, successful
	// fetch that contacted `origin` in commonDir.
	fetchedOK := func(commonDir string) PlanFetchOutcome {
		return PlanFetchOutcome{
			Applies: true, Attempted: true,
			Repos: []PlanFetchRepoResult{{
				Attempted: true, OK: true, ContextCommonDir: commonDir,
				Effect: &PlanFetchEffect{Contacted: true, Remotes: []PlanFetchRemote{{Name: "origin"}}},
			}},
		}
	}

	t.Run("NonApplicableFactsAreNeverMeasuredOrRefreshed", func(t *testing.T) {
		in := PlanPushFacts{Applies: false, Phase: "not-applicable"}
		got := RefreshPushTrackingRefs(in, fetchedOK("anything"))
		if got.Applies || got.Phase != "not-applicable" || got.Remotes != nil {
			t.Fatalf("facts = %+v, want the not-applicable value passed through unchanged", got)
		}
	})

	t.Run("PostFetchReadOfAFetchedContextIsFreshAndCarriesTheRefs", func(t *testing.T) {
		repo := rppRepo(t)
		commonDir := rppCommonDir(t, repo)
		sha := rppHeadSHA(t, repo)
		gitInTest(t, repo, "update-ref", "refs/remotes/origin/main", sha)

		id := "ctx"
		in := PlanPushFacts{
			Applies: true, Phase: "plan-point",
			Identities: PlanContextIdentities{id: {ContextID: id, RepoRoot: repo, CommonDir: commonDir}},
			Remotes: map[string]PlanPushRemoteFacts{"": {
				ContextID: &id, CommonDir: commonDir, RemoteName: "origin",
				MappingReadOK: true, TrackingPhase: "not-applicable",
			}},
		}
		got := RefreshPushTrackingRefs(in, fetchedOK(commonDir))
		if got.Phase != "post-fetch" {
			t.Fatalf("PlanPushFacts.Phase = %q, want post-fetch", got.Phase)
		}
		rf := got.Remotes[""]
		if !rf.TrackingReadOK {
			t.Fatal("expected TrackingReadOK=true")
		}
		if rf.TrackingPhase != "post-fetch" {
			t.Fatalf("TrackingPhase = %q, want post-fetch", rf.TrackingPhase)
		}
		if !rf.FetchedThisRun {
			t.Fatal("expected FetchedThisRun=true: mapping read, inventory read, post-fetch, and origin was contacted in this common dir")
		}
		if rf.TrackingRefs["main"] != sha {
			t.Fatalf("TrackingRefs[main] = %q, want %q", rf.TrackingRefs["main"], sha)
		}
	})

	t.Run("TwoLinkedWorktreesOfOneRepositoryAreBothFreshAfterOneFetch", func(t *testing.T) {
		repo := rppRepo(t)
		commonDir := rppCommonDir(t, repo)
		sha := rppHeadSHA(t, repo)
		gitInTest(t, repo, "update-ref", "refs/remotes/origin/main", sha)
		linked := filepath.Join(t.TempDir(), "linked")
		gitInTest(t, repo, "worktree", "add", "-b", "linked", linked)

		// Two DIFFERENT context ids (they hash different top levels) sharing
		// ONE canonical common dir: §14.1a rule 8's conjunct iv is exactly
		// why both are refreshed by the single collapsed fetch.
		a, b := "ctx-main", "ctx-linked"
		in := PlanPushFacts{
			Applies: true, Phase: "plan-point",
			Identities: PlanContextIdentities{
				a: {ContextID: a, RepoRoot: repo, CommonDir: commonDir},
				b: {ContextID: b, RepoRoot: linked, CommonDir: commonDir},
			},
			Remotes: map[string]PlanPushRemoteFacts{
				"a": {ContextID: &a, CommonDir: commonDir, RemoteName: "origin", MappingReadOK: true},
				"b": {ContextID: &b, CommonDir: commonDir, RemoteName: "origin", MappingReadOK: true},
			},
		}
		got := RefreshPushTrackingRefs(in, fetchedOK(commonDir))
		for _, token := range []string{"a", "b"} {
			rf := got.Remotes[token]
			if !rf.FetchedThisRun || rf.TrackingPhase != "post-fetch" || rf.TrackingRefs["main"] != sha {
				t.Fatalf("token %q: %+v, want post-fetch/fresh with the shared origin/main baseline", token, rf)
			}
		}
	})

	t.Run("AContextWhoseRemoteIsUpstreamStaysPossiblyStaleEvenAfterItsOwnFetch", func(t *testing.T) {
		repo := rppRepo(t)
		commonDir := rppCommonDir(t, repo)
		id := "ctx"
		in := PlanPushFacts{
			Applies: true, Phase: "plan-point",
			Identities: PlanContextIdentities{id: {ContextID: id, RepoRoot: repo, CommonDir: commonDir}},
			Remotes: map[string]PlanPushRemoteFacts{"": {
				ContextID: &id, CommonDir: commonDir, RemoteName: "upstream", MappingReadOK: true,
			}},
		}
		upstreamOnly := PlanFetchOutcome{
			Applies: true, Attempted: true,
			Repos: []PlanFetchRepoResult{{
				Attempted: true, OK: true, ContextCommonDir: commonDir,
				Effect: &PlanFetchEffect{Contacted: true, Remotes: []PlanFetchRemote{{Name: "upstream"}}},
			}},
		}
		got := RefreshPushTrackingRefs(in, upstreamOnly)
		rf := got.Remotes[""]
		if rf.TrackingPhase != "post-fetch" {
			t.Fatalf("TrackingPhase = %q, want post-fetch (a child really ran)", rf.TrackingPhase)
		}
		if rf.FetchedThisRun {
			t.Fatal("expected FetchedThisRun=false: origin's baseline was never refreshed, only upstream's")
		}
	})

	t.Run("NoFetchChildStampsPlanPointAndNeverFresh", func(t *testing.T) {
		repo := rppRepo(t)
		commonDir := rppCommonDir(t, repo)
		id := "ctx"
		in := PlanPushFacts{
			Applies: true, Phase: "plan-point",
			Identities: PlanContextIdentities{id: {ContextID: id, RepoRoot: repo, CommonDir: commonDir}},
			Remotes: map[string]PlanPushRemoteFacts{"": {
				ContextID: &id, CommonDir: commonDir, RemoteName: "origin", MappingReadOK: true,
			}},
		}
		got := RefreshPushTrackingRefs(in, PlanFetchOutcome{Applies: true})
		if got.Phase != "plan-point" {
			t.Fatalf("PlanPushFacts.Phase = %q, want plan-point", got.Phase)
		}
		rf := got.Remotes[""]
		if rf.TrackingPhase != "plan-point" || rf.FetchedThisRun {
			t.Fatalf("remote facts = %+v, want a plan-point read that is never fresh", rf)
		}
		if !rf.TrackingReadOK {
			t.Fatal("a plan-point read is still a REAL read of the current refs")
		}
	})

	t.Run("UnreadableInventoryStampsUnreadAndNeverFresh", func(t *testing.T) {
		notRepo := rppNotARepo(t)
		id := "ctx"
		in := PlanPushFacts{
			Applies: true, Phase: "plan-point",
			Identities: PlanContextIdentities{id: {ContextID: id, RepoRoot: notRepo, CommonDir: "common"}},
			Remotes: map[string]PlanPushRemoteFacts{"": {
				ContextID: &id, CommonDir: "common", RemoteName: "origin", MappingReadOK: true,
			}},
		}
		got := RefreshPushTrackingRefs(in, fetchedOK("common"))
		rf := got.Remotes[""]
		if rf.TrackingReadOK {
			t.Fatal("expected TrackingReadOK=false outside any repository")
		}
		if rf.TrackingPhase != "unread" {
			t.Fatalf("TrackingPhase = %q, want unread (first rung of the ladder)", rf.TrackingPhase)
		}
		if rf.FetchedThisRun {
			t.Fatal("expected FetchedThisRun=false: an unread inventory can never be fresh")
		}
		if rf.TrackingRefs != nil {
			t.Fatalf("TrackingRefs = %v, want nil on a failed read", rf.TrackingRefs)
		}
	})

	t.Run("ANilContextIDReadsNothingAndKeepsTheUnknownCell", func(t *testing.T) {
		in := PlanPushFacts{
			Applies: true, Phase: "plan-point",
			Identities: PlanContextIdentities{},
			Remotes: map[string]PlanPushRemoteFacts{"": {
				CommonDir: "common", RemoteName: "origin", MappingReadOK: false,
			}},
		}
		got := RefreshPushTrackingRefs(in, fetchedOK("common"))
		rf := got.Remotes[""]
		if rf.TrackingReadOK || rf.TrackingRefs != nil || rf.FetchedThisRun {
			t.Fatalf("remote facts = %+v, want a context that read NOTHING", rf)
		}
		if rf.TrackingPhase != "unread" {
			t.Fatalf("TrackingPhase = %q, want unread", rf.TrackingPhase)
		}
	})
}

func TestRebasePlanner_PushContextRefreshed(t *testing.T) {
	baseEffect := func(contacted bool, remotes ...string) *PlanFetchEffect {
		var rs []PlanFetchRemote
		for _, r := range remotes {
			rs = append(rs, PlanFetchRemote{Name: r})
		}
		return &PlanFetchEffect{Contacted: contacted, Remotes: rs}
	}
	row := func(attempted, ok bool, commonDir string, effect *PlanFetchEffect) PlanFetchOutcome {
		return PlanFetchOutcome{Applies: true, Attempted: attempted, Repos: []PlanFetchRepoResult{
			{Attempted: attempted, OK: ok, ContextCommonDir: commonDir, Effect: effect},
		}}
	}

	t.Run("EmptyCommonDirIsFalse", func(t *testing.T) {
		if PushContextRefreshed(row(true, true, "common", baseEffect(true, "origin")), "") {
			t.Fatal("expected false: an unmeasured common dir can never join")
		}
	})

	t.Run("NotAttemptedIsFalse", func(t *testing.T) {
		if PushContextRefreshed(PlanFetchOutcome{Applies: true}, "common") {
			t.Fatal("expected false when fetch was never attempted")
		}
	})

	t.Run("MatchingContactedOriginRowIsTrue", func(t *testing.T) {
		if !PushContextRefreshed(row(true, true, "common", baseEffect(true, "origin")), "common") {
			t.Fatal("expected true for a matching, contacted, OK origin row")
		}
	})

	t.Run("DifferentCommonDirIsFalse", func(t *testing.T) {
		if PushContextRefreshed(row(true, true, "other", baseEffect(true, "origin")), "common") {
			t.Fatal("expected false: common dir does not match")
		}
	})

	t.Run("NotContactedIsFalse", func(t *testing.T) {
		if PushContextRefreshed(row(true, true, "common", baseEffect(false, "origin")), "common") {
			t.Fatal("expected false: effect was not Contacted")
		}
	})

	t.Run("OriginNotAmongEffectRemotesIsFalse", func(t *testing.T) {
		if PushContextRefreshed(row(true, true, "common", baseEffect(true, "upstream")), "common") {
			t.Fatal("expected false: origin is not among the effect's remotes")
		}
	})

	t.Run("RepoAttemptedButNotOKIsSkipped", func(t *testing.T) {
		if PushContextRefreshed(row(true, false, "common", baseEffect(true, "origin")), "common") {
			t.Fatal("expected false: repo row was not OK")
		}
	})

	t.Run("NilEffectIsSkipped", func(t *testing.T) {
		if PushContextRefreshed(row(true, true, "common", nil), "common") {
			t.Fatal("expected false: nil Effect")
		}
	})

	t.Run("JoinIsOnTheCANONICALCommonDirNotTheRawString", func(t *testing.T) {
		dir := t.TempDir()
		nested := filepath.Join(dir, "a", "..", "a")
		if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
			t.Fatal(err)
		}
		fetched := row(true, true, nested, baseEffect(true, "origin"))
		if !PushContextRefreshed(fetched, filepath.Join(dir, "a")) {
			t.Fatal("expected true: the two spellings canonicalize to the same directory")
		}
	})
}

func TestRebasePlanner_MapThroughRefspec(t *testing.T) {
	t.Run("NonMatchingSrcFails", func(t *testing.T) {
		_, ok := mapThroughRefspec(PlanRefspec{Src: "refs/heads/release/*", Dst: "refs/remotes/origin/*"}, "refs/heads/main")
		if ok {
			t.Fatal("expected no match")
		}
	})

	t.Run("ExactNonWildcardPatternMapsToTheLiteralDst", func(t *testing.T) {
		dst, ok := mapThroughRefspec(PlanRefspec{Src: "refs/heads/main", Dst: "refs/remotes/origin/main"}, "refs/heads/main")
		if !ok || dst != "refs/remotes/origin/main" {
			t.Fatalf("dst=%q ok=%v, want refs/remotes/origin/main/true", dst, ok)
		}
	})

	t.Run("WildcardPatternSubstitutesTheMatchedSuffix", func(t *testing.T) {
		dst, ok := mapThroughRefspec(PlanRefspec{Src: "refs/heads/*", Dst: "refs/remotes/origin/*"}, "refs/heads/feature-x")
		if !ok || dst != "refs/remotes/origin/feature-x" {
			t.Fatalf("dst=%q ok=%v, want refs/remotes/origin/feature-x/true", dst, ok)
		}
	})

	t.Run("WildcardSrcWithNonWildcardDstUsesTheLiteralDst", func(t *testing.T) {
		dst, ok := mapThroughRefspec(PlanRefspec{Src: "refs/heads/*", Dst: "refs/remotes/origin/pinned"}, "refs/heads/anything")
		if !ok || dst != "refs/remotes/origin/pinned" {
			t.Fatalf("dst=%q ok=%v, want refs/remotes/origin/pinned/true (dst has no trailing * to substitute into)", dst, ok)
		}
	})
}

// TestRebasePlanner_ResolvePushLease drives §14.2's five closed cells, one
// subtest per cell, asserting the WHOLE PlanLease value (all five members) so
// no cell can be satisfied by a partially-correct projection.
func TestRebasePlanner_ResolvePushLease(t *testing.T) {
	wildcardRefspec := PlanRefspec{Src: "refs/heads/*", Dst: "refs/remotes/origin/*"}
	ref := func(s string) *string { return &s }

	t.Run("Cell5MappingUnreadableIsTheOnlyUnknownCell", func(t *testing.T) {
		lease := ResolvePushLease(PlanPushRemoteFacts{MappingReadOK: false}, "main")
		want := PlanLease{Mode: "implicit-remote-tracking", Expectation: "unknown", ExpectedSHAFreshness: "possibly-stale"}
		if lease != want {
			t.Fatalf("lease = %+v, want %+v", lease, want)
		}
	})

	t.Run("Cell4MappingReadButNoPositiveMappingIsAbsentAndFresh", func(t *testing.T) {
		lease := ResolvePushLease(PlanPushRemoteFacts{MappingReadOK: true, FetchRefspecs: nil}, "main")
		want := PlanLease{Mode: "implicit-remote-tracking", Expectation: "absent", ExpectedSHAFreshness: "fresh"}
		if lease != want {
			t.Fatalf("lease = %+v, want %+v: a configuration that WAS read and really maps nothing is Git's null-OID expectation, never `unknown`", lease, want)
		}
	})

	t.Run("Cell3MappedButTrackingInventoryUnreadIsAbsentAndPossiblyStale", func(t *testing.T) {
		facts := PlanPushRemoteFacts{
			MappingReadOK: true, RemoteName: "origin", FetchRefspecs: []PlanRefspec{wildcardRefspec},
			TrackingReadOK: false, TrackingPhase: "unread",
		}
		lease := ResolvePushLease(facts, "main")
		if lease.ExpectedRef == nil || *lease.ExpectedRef != "refs/remotes/origin/main" {
			t.Fatalf("ExpectedRef = %v, want refs/remotes/origin/main (the mapping survives an unread inventory)", lease.ExpectedRef)
		}
		want := PlanLease{
			Mode: "implicit-remote-tracking", Expectation: "absent",
			ExpectedRef: lease.ExpectedRef, ExpectedSHAFreshness: "possibly-stale",
		}
		if lease != want {
			t.Fatalf("lease = %+v, want %+v: Git falls back to the null OID when it cannot read the mapped ref, and nothing read may be called fresh", lease, want)
		}
	})

	t.Run("Cell2MappedAndReadWithTheMappedRefAbsentIsAbsentAndFresh", func(t *testing.T) {
		facts := PlanPushRemoteFacts{
			MappingReadOK: true, RemoteName: "origin", FetchRefspecs: []PlanRefspec{wildcardRefspec},
			TrackingReadOK: true, TrackingPhase: "plan-point", TrackingRefs: map[string]string{},
		}
		lease := ResolvePushLease(facts, "main")
		want := PlanLease{
			Mode: "implicit-remote-tracking", Expectation: "absent",
			ExpectedRef: ref("refs/remotes/origin/main"), ExpectedSHAFreshness: "fresh",
		}
		if lease.ExpectedRef == nil || *lease.ExpectedRef != *want.ExpectedRef {
			t.Fatalf("ExpectedRef = %v, want %v (it is the ref that must not exist)", lease.ExpectedRef, want.ExpectedRef)
		}
		if lease.Expectation != want.Expectation || lease.ExpectedSHA != nil || lease.ExpectedSHAFreshness != want.ExpectedSHAFreshness {
			t.Fatalf("lease = %+v, want expectation=absent expected_sha=nil freshness=fresh: this invocation READ the absence", lease)
		}
	})

	t.Run("Cell1MappedAndReadWithAShaPresentIsShaCarryingTheCurrentValue", func(t *testing.T) {
		facts := PlanPushRemoteFacts{
			MappingReadOK: true, RemoteName: "origin", FetchRefspecs: []PlanRefspec{wildcardRefspec},
			TrackingReadOK: true, TrackingPhase: "plan-point", TrackingRefs: map[string]string{"main": "deadbeef"},
		}
		lease := ResolvePushLease(facts, "main")
		if lease.Expectation != "sha" {
			t.Fatalf("Expectation = %q, want sha", lease.Expectation)
		}
		if lease.ExpectedSHA == nil || *lease.ExpectedSHA != "deadbeef" {
			t.Fatalf("ExpectedSHA = %v, want deadbeef", lease.ExpectedSHA)
		}
		if lease.ExpectedSHAFreshness != "possibly-stale" {
			t.Fatalf("Freshness = %q, want possibly-stale: a plan-point read refreshed nothing", lease.ExpectedSHAFreshness)
		}
	})

	t.Run("FreshnessIsFreshOnlyWhenAllThreeConditionsHold", func(t *testing.T) {
		full := PlanPushRemoteFacts{
			MappingReadOK: true, RemoteName: "origin", FetchRefspecs: []PlanRefspec{wildcardRefspec},
			TrackingReadOK: true, TrackingPhase: "post-fetch", FetchedThisRun: true,
			TrackingRefs: map[string]string{"main": "sha"},
		}
		if lease := ResolvePushLease(full, "main"); lease.ExpectedSHAFreshness != "fresh" {
			t.Fatalf("Freshness = %q, want fresh when TrackingReadOK && phase=post-fetch && FetchedThisRun", lease.ExpectedSHAFreshness)
		}

		notFetched := full
		notFetched.FetchedThisRun = false
		if lease := ResolvePushLease(notFetched, "main"); lease.ExpectedSHAFreshness != "possibly-stale" {
			t.Fatalf("Freshness = %q, want possibly-stale when FetchedThisRun is false", lease.ExpectedSHAFreshness)
		}

		wrongPhase := full
		wrongPhase.TrackingPhase = "pre-fetch"
		if lease := ResolvePushLease(wrongPhase, "main"); lease.ExpectedSHAFreshness != "possibly-stale" {
			t.Fatalf("Freshness = %q, want possibly-stale when phase is not post-fetch", lease.ExpectedSHAFreshness)
		}

		// A tracking read that did not happen is cell 3, whose OWN freshness
		// is possibly-stale — and whose expectation is `absent`, never the
		// `sha` the (unread) map still nominally contains.
		notRead := full
		notRead.TrackingReadOK = false
		notRead.TrackingPhase = "unread"
		lease := ResolvePushLease(notRead, "main")
		if lease.ExpectedSHAFreshness != "possibly-stale" {
			t.Fatalf("Freshness = %q, want possibly-stale when TrackingReadOK is false", lease.ExpectedSHAFreshness)
		}
		if lease.Expectation != "absent" || lease.ExpectedSHA != nil {
			t.Fatalf("lease = %+v, want the cell-3 absent/null pair", lease)
		}
	})

	t.Run("NegativeRefspecMatchForcesPossiblyStaleEvenWhenEveryFreshnessConditionOtherwiseHolds", func(t *testing.T) {
		facts := PlanPushRemoteFacts{
			MappingReadOK: true,
			RemoteName:    "origin",
			FetchRefspecs: []PlanRefspec{
				{Src: "refs/heads/main", Negative: true},
				wildcardRefspec,
			},
			TrackingReadOK: true, TrackingPhase: "post-fetch", FetchedThisRun: true,
			TrackingRefs: map[string]string{"main": "sha"},
		}
		lease := ResolvePushLease(facts, "main")
		if lease.ExpectedSHAFreshness != "possibly-stale" {
			t.Fatalf("Freshness = %q, want possibly-stale: a negative refspec match must override every fresh condition", lease.ExpectedSHAFreshness)
		}
		// The negative refspec must not itself prevent the positive
		// wildcard rung below it from still mapping the branch, and the
		// mapped ref and its current value both survive.
		if lease.Expectation != "sha" {
			t.Fatalf("Expectation = %q, want sha (the negative match only affects freshness, not the mapping/expectation itself)", lease.Expectation)
		}
		if lease.ExpectedRef == nil || *lease.ExpectedRef != "refs/remotes/origin/main" {
			t.Fatalf("ExpectedRef = %v, want the mapped ref refs/remotes/origin/main", lease.ExpectedRef)
		}
		if lease.ExpectedSHA == nil || *lease.ExpectedSHA != "sha" {
			t.Fatalf("ExpectedSHA = %v, want the tracking ref's current value", lease.ExpectedSHA)
		}
	})

	t.Run("NegativeRefspecAlsoForcesPossiblyStaleOnTheUnmappedAndAbsentCells", func(t *testing.T) {
		// Cell 4 under a negative match: nothing maps, so the expectation is
		// still `absent`, but no fetch can ever refresh what it names.
		unmapped := ResolvePushLease(PlanPushRemoteFacts{
			MappingReadOK: true,
			FetchRefspecs: []PlanRefspec{{Src: "refs/heads/main", Negative: true}},
		}, "main")
		if unmapped.Expectation != "absent" || unmapped.ExpectedSHAFreshness != "possibly-stale" {
			t.Fatalf("lease = %+v, want absent/possibly-stale", unmapped)
		}

		// Cell 2 under a negative match.
		absent := ResolvePushLease(PlanPushRemoteFacts{
			MappingReadOK: true, RemoteName: "origin",
			FetchRefspecs:  []PlanRefspec{{Src: "refs/heads/main", Negative: true}, wildcardRefspec},
			TrackingReadOK: true, TrackingPhase: "post-fetch", FetchedThisRun: true,
			TrackingRefs: map[string]string{},
		}, "main")
		if absent.Expectation != "absent" || absent.ExpectedSHAFreshness != "possibly-stale" {
			t.Fatalf("lease = %+v, want absent/possibly-stale", absent)
		}
	})

	t.Run("FirstPositiveMatchWinsWhenMultiplePositiveRefspecsCouldMap", func(t *testing.T) {
		facts := PlanPushRemoteFacts{
			MappingReadOK: true,
			FetchRefspecs: []PlanRefspec{
				{Src: "refs/heads/main", Dst: "refs/remotes/origin/pinned-main"},
				wildcardRefspec,
			},
		}
		lease := ResolvePushLease(facts, "main")
		if lease.ExpectedRef == nil || *lease.ExpectedRef != "refs/remotes/origin/pinned-main" {
			t.Fatalf("ExpectedRef = %v, want the FIRST positive match's mapping, refs/remotes/origin/pinned-main", lease.ExpectedRef)
		}
	})
}

func TestRebasePlanner_PushTargets(t *testing.T) {
	t.Run("NoIntentYieldsANonNilEmptySlice", func(t *testing.T) {
		got := PushTargets(PlanPushRequest{Intent: false})
		if got == nil || len(got) != 0 {
			t.Fatalf("got = %v, want a non-nil empty slice", got)
		}
	})

	t.Run("FiltersToRemainingAndNotYetPushed", func(t *testing.T) {
		req := PlanPushRequest{
			Intent: true,
			Selection: SyncSelection{Entries: []SyncSelectedEntry{
				{Name: "a", GitBranch: "a"},
				{Name: "b", GitBranch: "b"},
				{Name: "c", GitBranch: "c"},
			}},
			Remaining:     []string{"a", "b"}, // c is not remaining
			AlreadyPushed: map[string]bool{"b": true},
		}
		got := PushTargets(req)
		if len(got) != 1 || got[0].GitBranch != "a" {
			t.Fatalf("got = %+v, want exactly one target for entry a", got)
		}
	})

	t.Run("FixedFieldsAndRefShapeAndCheckoutModeClearsRepo", func(t *testing.T) {
		req := PlanPushRequest{
			Mode:      ModeCheckout,
			Scope:     "one",
			Intent:    true,
			Selection: SyncSelection{Entries: []SyncSelectedEntry{{Name: "a", GitBranch: "feature-a", Repo: "sub"}}},
			Remaining: []string{"a"},
		}
		got := PushTargets(req)
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		tgt := got[0]
		if tgt.Repo != "" {
			t.Fatalf("Repo = %q, want empty: checkout mode always clears push.targets[].repo", tgt.Repo)
		}
		if tgt.Remote != "origin" || tgt.Ref != "refs/heads/feature-a" || tgt.Force != "with-lease" {
			t.Fatalf("tgt = %+v, want Remote=origin Ref=refs/heads/feature-a Force=with-lease", tgt)
		}
		if tgt.Scope != "one" {
			t.Fatalf("Scope = %q, want one (passed through from the request)", tgt.Scope)
		}
	})

	t.Run("ExternalModePreservesTheEntryRepoToken", func(t *testing.T) {
		req := PlanPushRequest{
			Mode:      ModeExternal,
			Intent:    true,
			Selection: SyncSelection{Entries: []SyncSelectedEntry{{Name: "a", GitBranch: "feature-a", Repo: "sub"}}},
			Remaining: []string{"a"},
		}
		got := PushTargets(req)
		if len(got) != 1 || got[0].Repo != "sub" {
			t.Fatalf("got = %+v, want Repo=sub preserved for external mode", got)
		}
	})

	t.Run("ContextIDAndRepoRootResolveOnlyWhenBothLookupsSucceed", func(t *testing.T) {
		cid := "context-1"
		req := PlanPushRequest{
			Intent:    true,
			Selection: SyncSelection{Entries: []SyncSelectedEntry{{Name: "a", GitBranch: "a", Repo: ""}, {Name: "b", GitBranch: "b", Repo: "missing-remote-entry"}, {Name: "c", GitBranch: "c", Repo: "dangling-context"}}},
			Remaining: []string{"a", "b", "c"},
			Facts: PlanPushFacts{
				Remotes: map[string]PlanPushRemoteFacts{
					"":                 {ContextID: &cid},
					"dangling-context": {ContextID: strp("no-such-identity")},
				},
				Identities:      PlanContextIdentities{cid: {ContextID: cid, RepoRoot: "/repo/root"}},
				Materialization: map[string]string{"": "materialized"},
			},
		}
		got := PushTargets(req)
		byName := map[string]PlanPushTarget{}
		for _, tgt := range got {
			byName[tgt.GitBranch] = tgt
		}
		if a := byName["a"]; a.ContextID == nil || *a.ContextID != cid || a.RepoRoot == nil || *a.RepoRoot != "/repo/root" {
			t.Fatalf("a = %+v, want a resolved ContextID/RepoRoot", a)
		}
		if a := byName["a"]; a.Materialization != "materialized" {
			t.Fatalf("a.Materialization = %q, want materialized", a.Materialization)
		}
		if b := byName["b"]; b.ContextID != nil || b.RepoRoot != nil {
			t.Fatalf("b = %+v, want nil ContextID/RepoRoot: repo token has no Remotes entry at all", b)
		}
		if c := byName["c"]; c.ContextID != nil || c.RepoRoot != nil {
			t.Fatalf("c = %+v, want nil ContextID/RepoRoot: the ContextID it names has no Identities entry", c)
		}
	})

	t.Run("SortsByContextIDNilFirstThenAscendingThenGitBranchAscendingWithinTies", func(t *testing.T) {
		cidZ, cidA := "zzz", "aaa"
		req := PlanPushRequest{
			Intent: true,
			Selection: SyncSelection{Entries: []SyncSelectedEntry{
				{Name: "1", GitBranch: "b-branch", Repo: "z"},
				{Name: "2", GitBranch: "a-branch", Repo: "z"}, // same ContextID as 1: tie-break by GitBranch
				{Name: "3", GitBranch: "c-branch", Repo: "a"},
				{Name: "4", GitBranch: "d-branch", Repo: "no-remote-entry"}, // nil ContextID: sorts first
			}},
			Remaining: []string{"1", "2", "3", "4"},
			Facts: PlanPushFacts{
				Remotes: map[string]PlanPushRemoteFacts{
					"z": {ContextID: &cidZ},
					"a": {ContextID: &cidA},
				},
				Identities: PlanContextIdentities{
					cidZ: {ContextID: cidZ},
					cidA: {ContextID: cidA},
				},
			},
		}
		got := PushTargets(req)
		var order []string
		for _, tgt := range got {
			order = append(order, tgt.GitBranch)
		}
		want := []string{"d-branch", "c-branch", "a-branch", "b-branch"}
		if !stringSlicesEqual(order, want) {
			t.Fatalf("order = %v, want %v (nil ContextID first, then ascending ContextID, then ascending GitBranch within a tie)", order, want)
		}
	})
}

// ---------------------------------------------------------------------------
// §23.3 — TopoSort's 20,000-iteration randomized ordering test.
// ---------------------------------------------------------------------------

// topoComponents is the fixture the randomized test sorts: THREE disjoint
// linear chains (components). Each chain forces its own internal order by
// dependency alone — no siblings, so no intra-component ambiguity exists —
// which is what makes "only INTER-component interleaving may vary" an exact,
// checkable claim rather than a hedge.
func topoComponents() [][]string {
	return [][]string{
		{"a0", "a1", "a2", "a3"},
		{"b0", "b1", "b2"},
		{"c0", "c1", "c2", "c3", "c4"},
	}
}

// topoFixtureEntries builds the flat StackEntry list for topoComponents,
// where each chain's first element is rooted on an UNTRACKED base (so it has
// in-degree 0) and every later element is based on its predecessor.
func topoFixtureEntries() []StackEntry {
	var out []StackEntry
	for _, chain := range topoComponents() {
		for i, name := range chain {
			base := "main"
			if i > 0 {
				base = chain[i-1]
			}
			out = append(out, StackEntry{Name: name, Base: base})
		}
	}
	return out
}

// TestRebasePlanner_TopoSortRandomizedOrderingIsComponentStable is §23.3's
// 20,000-iteration randomized ordering test. Every iteration shuffles the
// INPUT order and asserts four things about the result:
//
//  1. it is a real topological order — every entry whose base is tracked
//     appears strictly after that base;
//  2. it is a permutation of the input — same names, same count, no
//     duplicates and no invention;
//  3. the relative order WITHIN each component is exactly the chain order,
//     invariant across every iteration; and
//  4. the ONLY thing that varies is how the components interleave, which is
//     asserted to really vary (at least two distinct interleavings are
//     observed) so the test cannot pass by the sort being accidentally
//     input-order-stable.
func TestRebasePlanner_TopoSortRandomizedOrderingIsComponentStable(t *testing.T) {
	const iterations = 20000

	entries := topoFixtureEntries()
	componentOf := map[string]int{}
	indexInComponent := map[string]int{}
	for c, chain := range topoComponents() {
		for i, name := range chain {
			componentOf[name] = c
			indexInComponent[name] = i
		}
	}

	rng := rand.New(rand.NewSource(20000))
	interleavings := map[string]bool{}

	for iter := 0; iter < iterations; iter++ {
		shuffled := append([]StackEntry(nil), entries...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

		sorted, err := TopoSort(Stack{Branches: shuffled})
		if err != nil {
			t.Fatalf("iteration %d: TopoSort: %v", iter, err)
		}
		if len(sorted) != len(entries) {
			t.Fatalf("iteration %d: sorted %d entries, want %d", iter, len(sorted), len(entries))
		}

		position := make(map[string]int, len(sorted))
		lastSeen := map[int]int{}
		var signature []byte
		for pos, e := range sorted {
			if _, dup := position[e.Name]; dup {
				t.Fatalf("iteration %d: %q appears twice", iter, e.Name)
			}
			position[e.Name] = pos

			c, known := componentOf[e.Name]
			if !known {
				t.Fatalf("iteration %d: TopoSort invented the entry %q", iter, e.Name)
			}
			// (3) intra-component order: this entry must be exactly the next
			// one of its own chain.
			want := 0
			if seen, ok := lastSeen[c]; ok {
				want = seen + 1
			}
			if got := indexInComponent[e.Name]; got != want {
				t.Fatalf("iteration %d: %q is index %d of component %d but appeared where index %d was due — intra-component order must never vary",
					iter, e.Name, got, c, want)
			}
			lastSeen[c] = want
			signature = append(signature, byte('0'+c))
		}
		if len(position) != len(entries) {
			t.Fatalf("iteration %d: result is not a permutation of the input", iter)
		}
		// (1) the dependency order itself.
		for _, e := range entries {
			if basePos, tracked := position[e.Base]; tracked && basePos >= position[e.Name] {
				t.Fatalf("iteration %d: %q at %d does not follow its base %q at %d",
					iter, e.Name, position[e.Name], e.Base, basePos)
			}
		}
		interleavings[string(signature)] = true
	}

	// (4) inter-component interleaving really is the varying dimension.
	if len(interleavings) < 2 {
		t.Fatalf("observed %d distinct component interleavings over %d iterations, want at least 2: the randomization is not reaching TopoSort's own queue seeding",
			len(interleavings), iterations)
	}
}

// TestRebasePlanner_TopoSortNeverInventsAnOrderForACycle is the companion
// negative: a cyclic stack has no topological order at all, so every
// iteration of the same randomized loop must refuse rather than emit a
// partial one.
func TestRebasePlanner_TopoSortNeverInventsAnOrderForACycle(t *testing.T) {
	cyclic := []StackEntry{
		{Name: "x", Base: "z"},
		{Name: "y", Base: "x"},
		{Name: "z", Base: "y"},
	}
	rng := rand.New(rand.NewSource(3))
	for iter := 0; iter < 1000; iter++ {
		shuffled := append([]StackEntry(nil), cyclic...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if _, err := TopoSort(Stack{Branches: shuffled}); err == nil {
			t.Fatalf("iteration %d: a cyclic stack must refuse, never sort", iter)
		}
	}
}
