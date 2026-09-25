package internal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReparentPlanFetchUsesSharedSemantics(t *testing.T) {
	ctx := PlanFetchContext{
		RepoToken: "secondary", Root: "/repo", CommonDir: "/repo/.git",
		Source: "entry-repo",
		Candidates: []PlanFetchCandidate{{
			ContextRoot: "/repo", ContextSource: "entry-repo",
		}},
		Effect: &PlanFetchEffect{Contacted: true, MayUpdateLocalBranches: true},
	}

	noFetch := reparentFetchProjection(
		reparentFetchPlan(ReparentRouteFresh, SyncFetchDisabled, false, ctx),
		PlanFetchOutcome{},
	)
	if noFetch.Attempted || noFetch.Outcome != "skipped" ||
		noFetch.PolicySource != "flag" || noFetch.SuppressionCause != nil ||
		len(noFetch.Repos) != 0 || buildFreshness(noFetch) != "local-only" {
		t.Fatalf("explicit no-fetch projection = %+v", noFetch)
	}
	if noFetch.MutatedRemoteTrackingRefs == nil || *noFetch.MutatedRemoteTrackingRefs ||
		noFetch.MutatedLocalBranches == nil || *noFetch.MutatedLocalBranches {
		t.Fatalf("no-fetch mutation facts = remote:%v local:%v",
			noFetch.MutatedRemoteTrackingRefs, noFetch.MutatedLocalBranches)
	}
	if got := reparentFetchPlan(ReparentRouteFresh, SyncFetchDisabled, true, ctx).PolicySource; got != "route-default" {
		t.Fatalf("default policy source = %q", got)
	}
	continued := reparentFetchPlan(ReparentRouteContinue, SyncFetchEnabled, false, ctx)
	if continued.PolicySource != "persisted-transaction" ||
		continued.SuppressionCause == nil || *continued.SuppressionCause != "not-refreshed-continuation" {
		t.Fatalf("continue fetch projection = %+v", continued)
	}

	result := PlanFetchRepoResult{
		RepoToken: ctx.RepoToken, ContextRoot: ctx.Root,
		ContextCommonDir: ctx.CommonDir, ContextSource: ctx.Source,
		ContextCandidates: ctx.Candidates, Effect: ctx.Effect,
		Attempted: true, OK: false,
	}
	failed := reparentFetchProjection(
		reparentFetchPlan(ReparentRouteFresh, SyncFetchEnabled, true, ctx),
		PlanFetchOutcome{Applies: true, Attempted: true, Repos: []PlanFetchRepoResult{result}},
	)
	if failed.Outcome != "failed" || failed.PolicySource != "route-default" ||
		buildFreshness(failed) != "possibly-stale" || len(failed.Repos) != 1 {
		t.Fatalf("failed fetch projection = %+v", failed)
	}
	row := failed.Repos[0]
	if row.RepoToken != result.RepoToken || row.ContextRoot != result.ContextRoot ||
		row.ContextCommonDir != result.ContextCommonDir || row.ContextSource != result.ContextSource ||
		len(row.ContextCandidates) != 1 || row.Effect != result.Effect || !row.Attempted || row.OK {
		t.Fatalf("fetch repo outcome was not copied completely: %+v", row)
	}
	if failed.MutatedRemoteTrackingRefs != nil || failed.MutatedLocalBranches != nil {
		t.Fatalf("a failed contacting fetch has unknown mutation facts: %+v", failed)
	}

	result.OK = true
	succeeded := reparentFetchProjection(
		reparentFetchPlan(ReparentRouteFresh, SyncFetchEnabled, false, ctx),
		PlanFetchOutcome{Applies: true, Attempted: true, Repos: []PlanFetchRepoResult{result}},
	)
	if succeeded.Outcome != "ok" || succeeded.PolicySource != "flag" ||
		buildFreshness(succeeded) != "fetched" ||
		succeeded.MutatedRemoteTrackingRefs == nil || !*succeeded.MutatedRemoteTrackingRefs ||
		succeeded.MutatedLocalBranches == nil || !*succeeded.MutatedLocalBranches {
		t.Fatalf("successful fetch projection = %+v", succeeded)
	}

	plan := reparentSamplePlan()
	plan.Fetch = failed
	plan.Freshness = buildFreshness(failed)
	raw, err := MarshalReparentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	fetch := doc["fetch"].(map[string]any)
	if fetch["outcome"] != "failed" || fetch["policy_source"] != "route-default" ||
		fetch["mutated_remote_tracking_refs"] != nil || fetch["mutated_local_branches"] != nil ||
		doc["freshness"] != "possibly-stale" {
		t.Fatalf("failed fetch JSON = %s", raw)
	}
}

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2): the pure builder's gates and admission algebra.
//
//	T-022 genuine cross-repo descendant refuses ................... AC-034
//	T-023 archived target and archived descendant ................. AC-035
//	T-024 duplicate name / duplicate git branch, no Git spawned .... AC-036
//	(the same-parent no-work/stale CELL is owned by package cli; the pure
//	 verdict function is asserted here as supporting evidence, AC-039, AC-040)
//	T-036 both admission predicate tables, fresh and continue ...... AC-049, AC-050
//	T-049 merge commit in replay range ............................ AC-062
//	T-054 capability floor 2.38, unprobed and unknown backend ...... AC-066
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// The builder is pure, so every case below is a table over measured inputs —
// no repository, no process, no clock.
// ---------------------------------------------------------------------------

func reparentBuilderStack() Stack {
	return Stack{Branches: []StackEntry{
		{Name: "pr1", Base: "refs/heads/main", LastBaseSHA: "1111"},
		{Name: "pr2", Base: "pr1", LastBaseSHA: "2222"},
		{Name: "pr3", Base: "pr2", LastBaseSHA: "3333"},
		{Name: "other", Base: "refs/heads/main", LastBaseSHA: "4444"},
	}}
}

func reparentBuilderRequest() ReparentRequest {
	stack := reparentBuilderStack()
	dest := ReparentDestination{
		RequestedToken: "main",
		StoredToken:    "refs/heads/main",
		Kind:           ReparentParentKindLiteralRef,
		Ref:            reparentPlanFixtureString("refs/heads/main"),
		SHA:            "aaaa",
		Resolution:     ReparentResolutionRefHead,
	}
	rows := []ReparentRowProbe{
		{
			Name: "pr2", GitBranch: "pr2", Order: 0, Role: ReparentRoleTarget,
			HeadSHA: "2a2a", HeadFound: true, Materialization: "materialized", CommonDir: "/repo/.git",
			Cutoff: ReparentPlanCutoff{
				RecordedSHA: reparentPlanFixtureString("2222"), RecordedState: ReparentCutoffRecordPresent,
				ResolvedSHA: reparentPlanFixtureString("2222"), Provenance: ReparentCutoffRecordedBySync,
			},
			CutoffAncestorProbed: true, CutoffIsAncestor: true,
			MergeCommitsInRange: reparentPlanFixtureInt(0),
			Replay:              PlanEntryReplay{Determinacy: "exact", CandidateCount: reparentPlanFixtureInt(2)},
			Divergence:          "none",
		},
		{
			Name: "pr3", GitBranch: "pr3", Order: 1, Role: ReparentRoleDescendant,
			HeadSHA: "3a3a", HeadFound: true, Materialization: "materialized", CommonDir: "/repo/.git",
			Cutoff: ReparentPlanCutoff{
				RecordedSHA: reparentPlanFixtureString("3333"), RecordedState: ReparentCutoffRecordPresent,
				ResolvedSHA: reparentPlanFixtureString("3333"), Provenance: ReparentCutoffRecordedBySync,
			},
			CutoffAncestorProbed: true, CutoffIsAncestor: true,
			MergeCommitsInRange: reparentPlanFixtureInt(0),
			Replay:              PlanEntryReplay{Determinacy: "exact", CandidateCount: reparentPlanFixtureInt(1)},
			Divergence:          "none",
		},
	}
	return ReparentRequest{
		Route:               ReparentRouteFresh,
		Invocation:          ReparentInvocationPlanOnly,
		Mode:                ModeExternal,
		Feature:             "customer",
		Stack:               stack,
		StackSHA256:         strings.Repeat("f", 64),
		TargetName:          "pr2",
		OntoKindRequested:   "ref",
		RequestedToken:      "main",
		Destination:         dest,
		DestinationResolved: true,
		OldParent:           reparentEntryParent(stack, "pr2"),
		Rows:                rows,
		FetchPolicy:         SyncFetchDisabled,
		CapsProbed:          true,
		Caps:                GitCapabilities{CapRebaseUpdateRefs: true},
		RefBackend:          ReparentRefBackendFiles,
		OIDWidth:            40,
	}
}

func mustBuildReparentPlan(t *testing.T, req ReparentRequest) ReparentPlan {
	t.Helper()
	plan, err := BuildReparentPlan(req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return plan
}

func TestBuildReparentPlanClosureAndMetadataRecipe(t *testing.T) {
	_ = "asserts §7.11"
	assertReparentMatrixBehavior(t, "T-021", "ancestor-warning")
	plan := mustBuildReparentPlan(t, reparentBuilderRequest())

	if !plan.Runnable || len(plan.Blockers) != 0 {
		t.Fatalf("blockers = %+v", plan.Blockers)
	}
	if plan.Summary.Rows != 2 || plan.Summary.Descendants != 1 {
		t.Fatalf("summary = %+v", plan.Summary)
	}

	// §10.1's exact post-state, and only the closure.
	entries := plan.MetadataDelta.Entries
	if len(entries) != 2 {
		t.Fatalf("metadata entries = %+v", entries)
	}
	if entries[0].Name != "pr2" || entries[0].BaseBefore != "pr1" || entries[0].BaseAfter != "refs/heads/main" {
		t.Fatalf("target cell = %+v", entries[0])
	}
	if entries[0].LastBaseSHAAfter == nil || *entries[0].LastBaseSHAAfter != "aaaa" {
		t.Fatalf("the target takes the pinned destination: %+v", entries[0])
	}
	if entries[0].LastBaseSHAAfterSource != ReparentLastBaseSourcePinned {
		t.Fatalf("target source = %q", entries[0].LastBaseSHAAfterSource)
	}
	if entries[1].BaseBefore != entries[1].BaseAfter {
		t.Fatal("a descendant's configured parent never changes")
	}
	if entries[1].LastBaseSHAAfter != nil {
		t.Fatal("a descendant's post-replay tip cannot exist yet")
	}
	if entries[1].LastBaseSHAAfterSource != ReparentLastBaseSourceReplay {
		t.Fatalf("descendant source = %q", entries[1].LastBaseSHAAfterSource)
	}
	if !entries[1].Changed {
		t.Fatal("a descendant's deferred last_base_sha is a real planned change even while its after value is null")
	}
	if plan.MetadataDelta.EntriesOutsideClosureChanged {
		t.Fatal("no entry outside the closure is ever written")
	}
	if plan.MetadataDelta.Writer != ReparentMetadataWriter || plan.MetadataDelta.WritePoint != ReparentMetadataWritePoint {
		t.Fatalf("metadata constants = %+v", plan.MetadataDelta)
	}

	// The atomicity statement never claims more than §9.13 allows.
	if plan.Strategy.Atomicity.CombinedAtomic {
		t.Fatal("refs, metadata and worktrees are three separate effects")
	}
	if plan.Strategy.Atomicity.RefCommitCrashAtomic == nil || *plan.Strategy.Atomicity.RefCommitCrashAtomic {
		t.Fatal("the files backend is not crash-atomic")
	}
	if !plan.Strategy.Atomicity.RefCommitRaceAtomic {
		t.Fatal("the CAS is race-atomic on every backend")
	}
	if plan.Strategy.UsesGitReplay {
		t.Fatal("v1 never depends on git replay")
	}
	if !hasReparentWarning(plan, ReparentWarnFilesBackendNotCrashAtomic) {
		t.Fatal("a files/unknown backend must warn files-backend-not-crash-atomic")
	}

	targetOnly := reparentBuilderRequest()
	targetOnly.Rows = targetOnly.Rows[:1]
	targetPlan := mustBuildReparentPlan(t, targetOnly)
	if !targetPlan.MetadataDelta.PostImageKnown || targetPlan.MetadataDelta.StackSHA256AfterExpected == nil {
		t.Fatalf("a runnable target-only plan must publish its exact post-image hash: %+v", targetPlan.MetadataDelta)
	}
}

func TestBuildReparentPlanScopeGates(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-023", "archived-scope-gates")
	assertReparentMatrixBehavior(t, "T-049", "merge-range-scope-gate")
	_ = "asserts AC-035 AC-062"
	for _, tc := range []struct {
		name   string
		mutate func(*ReparentRequest)
		want   ReparentRefusalKind
	}{
		{"archived target", func(r *ReparentRequest) { r.Rows[0].Archived = true }, ReparentRefusalTargetArchived},
		{"archived descendant", func(r *ReparentRequest) { r.Rows[1].Archived = true }, ReparentRefusalAffectedArchived},
		{"missing branch ref", func(r *ReparentRequest) { r.Rows[1].HeadFound = false }, ReparentRefusalBranchRefMissing},
		{"cross-repo closure", func(r *ReparentRequest) { r.Rows[1].CommonDir = "/elsewhere/.git" }, ReparentRefusalCrossRepoClosure},
		{"git operation in progress", func(r *ReparentRequest) { r.Rows[0].GitOperationInProgress = "rebase" }, ReparentRefusalGitOperationInProgress},
		{"dirty context", func(r *ReparentRequest) { r.Rows[0].DirtyPaths = []string{"a.txt"} }, ReparentRefusalContextDirty},
		{"cutoff not an ancestor", func(r *ReparentRequest) { r.Rows[0].CutoffIsAncestor = false }, ReparentRefusalCutoffNotAncestor},
		{"merge commit in range", func(r *ReparentRequest) { r.Rows[1].MergeCommitsInRange = reparentPlanFixtureInt(1) }, ReparentRefusalMergeCommitInReplaySet},
		{"prunable holder", func(r *ReparentRequest) {
			r.Holders = []ReparentHolderProbe{{GitBranch: "pr2", HolderPath: "/wt", Prunable: true}}
		}, ReparentRefusalHolderUnsafe},
		{"duplicate holder", func(r *ReparentRequest) {
			r.Holders = []ReparentHolderProbe{{GitBranch: "pr2", HolderPath: "/wt", Duplicate: true}}
		}, ReparentRefusalHolderUnsafe},
		{"live session", func(r *ReparentRequest) { r.LiveSessions = []string{"pr3"} }, ReparentRefusalSessionLive},
		{"capability floor", func(r *ReparentRequest) { r.Caps = GitCapabilities{} }, ReparentRefusalCapabilityUnsupported},
		// AC-066's other half: an UNPROBED capability publishes no verdict at
		// all, so a document that refused before the probe never claims to
		// have observed a git version it never asked for.
		{"duplicate entry name", func(r *ReparentRequest) {
			r.Stack.Branches = append(r.Stack.Branches, StackEntry{Name: "pr2", Base: "pr1"})
		}, ReparentRefusalDuplicateEntryName},
		{"duplicate git branch", func(r *ReparentRequest) {
			r.Stack.Branches = append(r.Stack.Branches, StackEntry{Name: "clone", Branch: "pr2", Base: "pr1"})
		}, ReparentRefusalDuplicateGitBranch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := reparentBuilderRequest()
			tc.mutate(&req)
			plan := mustBuildReparentPlan(t, req)
			if !hasReparentBlocker(plan, tc.want) {
				t.Fatalf("blockers = %+v, want %s", plan.Blockers, tc.want)
			}
			if plan.Runnable {
				t.Fatal("a plan with blockers is never runnable")
			}
			if plan.Approval.Usable || plan.Approval.Fingerprint != nil {
				t.Fatal("a blocked plan mints no usable token")
			}
		})
	}
}

func TestReparentValidationOrderPrecedence(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-084", "validation-precedence")
	_ = "asserts AC-098"
	req := reparentBuilderRequest()
	req.Stack.Branches = append(req.Stack.Branches,
		StackEntry{Name: "pr2", Branch: "duplicate", Base: "pr1", Archived: true})
	req.Rows[0].DirtyPaths = []string{"dirty.txt"}
	req.LiveSessions = []string{"pr2"}
	plan := mustBuildReparentPlan(t, req)
	if plan.Refusal.Kind == nil || *plan.Refusal.Kind != ReparentRefusalDuplicateEntryName {
		t.Fatalf("primary refusal = %+v, want the highest-ranked duplicate-entry-name", plan.Refusal)
	}
	if len(plan.Blockers) < 3 {
		t.Fatalf("the fixture must violate multiple later rules too: %+v", plan.Blockers)
	}
}

func TestBuildReparentPlanUnprobedCapabilityPublishesNoVerdict(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-054", "capability-floor-and-unknown-backend")
	_ = "asserts AC-066"
	req := reparentBuilderRequest()
	req.CapsProbed = false
	req.Caps = GitCapabilities{}
	plan := mustBuildReparentPlan(t, req)
	if hasReparentBlocker(plan, ReparentRefusalCapabilityUnsupported) {
		t.Fatalf("an unprobed capability must not refuse: %+v", plan.Blockers)
	}
}

func TestBuildReparentPlanGraphSafety(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-019", "graph-cycle-safety")
	_ = "asserts AC-019"
	stack := reparentBuilderStack()
	for _, tc := range []struct {
		name string
		dest ReparentDestination
		want ReparentRefusalKind
	}{
		{
			"destination is the target itself",
			ReparentDestination{StoredToken: "pr2", Kind: ReparentParentKindStackEntry, EntryName: "pr2", SHA: "aaaa"},
			ReparentRefusalDestinationSelf,
		},
		{
			"destination is a descendant",
			ReparentDestination{StoredToken: "pr3", Kind: ReparentParentKindStackEntry, EntryName: "pr3", SHA: "aaaa"},
			ReparentRefusalDestinationDescendant,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := reparentBuilderRequest()
			req.Stack = stack
			req.Destination = tc.dest
			plan := mustBuildReparentPlan(t, req)
			if !hasReparentBlocker(plan, tc.want) {
				t.Fatalf("blockers = %+v, want %s", plan.Blockers, tc.want)
			}
		})
	}
}

func TestReparentSameParentCases(t *testing.T) {
	current := AncestryStatusCurrent
	stale := AncestryStatusStale

	// A same-parent, CURRENT edge is no work at all.
	got := evaluateReparentSameParent("pr1", "pr1", "aaaa", "aaaa", &current)
	if !got.Same || !got.NoWork || got.Blocker != nil {
		t.Fatalf("current same-parent verdict = %+v", got)
	}
	// A same-parent, NOT-current edge is a stale replay, which is sync's job.
	got = evaluateReparentSameParent("pr1", "pr1", "aaaa", "aaaa", &stale)
	if !got.Same || got.NoWork || got.Blocker == nil {
		t.Fatalf("stale same-parent verdict = %+v", got)
	}
	if got.Blocker.Kind != ReparentRefusalDestinationSameParentStale {
		t.Fatalf("blocker kind = %s", got.Blocker.Kind)
	}
	if got.Blocker.Detail != reparentSameParentGuidance {
		t.Fatalf("guidance = %q", got.Blocker.Detail)
	}
	// A different token, or a moved SHA, is not the same parent at all.
	if v := evaluateReparentSameParent("pr1", "refs/heads/main", "aaaa", "aaaa", &current); v.Same {
		t.Fatal("a different stored token is not the same parent")
	}
	if v := evaluateReparentSameParent("pr1", "pr1", "aaaa", "bbbb", &current); v.Same {
		t.Fatal("a different resolved SHA is not the same parent")
	}
}

func TestBuildReparentPlanNoWorkRoutes(t *testing.T) {
	current := AncestryStatusCurrent
	req := reparentBuilderRequest()
	req.Destination = ReparentDestination{
		StoredToken: "pr1", Kind: ReparentParentKindStackEntry, EntryName: "pr1", SHA: "1a1a",
	}
	req.OldParent.SHA = reparentPlanFixtureString("1a1a")
	req.Rows[0].Ancestry = PlanAncestry{Status: &current}

	// A plan route publishes no-work with a NULL refusal and no token.
	plan := mustBuildReparentPlan(t, req)
	if plan.Summary.Plannability != ReparentPlannabilityNoWork || plan.Summary.HasWork {
		t.Fatalf("summary = %+v", plan.Summary)
	}
	if plan.Runnable {
		t.Fatal("no work means not runnable")
	}
	if plan.Refusal.Kind != nil {
		t.Fatalf("a plan route publishes no refusal for no-work: %+v", plan.Refusal)
	}
	if plan.Approval.Fingerprint != nil {
		t.Fatal("a no-work plan mints no token")
	}
	if plan.MetadataDelta.StackSHA256AfterExpected != nil || plan.MetadataDelta.PostImageKnown {
		t.Fatalf("no-work metadata published a post-image: %+v", plan.MetadataDelta)
	}
	for _, entry := range plan.MetadataDelta.Entries {
		if entry.BaseBefore != entry.BaseAfter ||
			derefString(entry.LastBaseSHABefore) != derefString(entry.LastBaseSHAAfter) ||
			entry.Changed || entry.LastBaseSHAAfterSource != ReparentLastBaseSourceUnchanged {
			t.Fatalf("no-work metadata entry is not unchanged: %+v", entry)
		}
	}
	jsonBytes, err := MarshalReparentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(jsonBytes, &doc); err != nil {
		t.Fatal(err)
	}
	delta := doc["metadata_delta"].(map[string]any)
	if delta["stack_sha256_after_expected"] != nil || delta["post_image_known"] != false {
		t.Fatalf("no-work JSON metadata = %+v", delta)
	}
	for _, raw := range delta["entries"].([]any) {
		entry := raw.(map[string]any)
		if entry["base_before"] != entry["base_after"] ||
			entry["last_base_sha_before"] != entry["last_base_sha_after"] ||
			entry["changed"] != false ||
			entry["last_base_sha_after_source"] != ReparentLastBaseSourceUnchanged {
			t.Fatalf("no-work JSON entry = %+v", entry)
		}
	}
	human, err := FormatReparentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(human), "stack.yaml after:  unchanged") ||
		strings.Contains(string(human), "last_base_sha ") {
		t.Fatalf("no-work human metadata is not strictly unchanged:\n%s", human)
	}

	// Once strict current same-parent is known, replay-only evidence is
	// irrelevant and cannot outrank no-work.
	mergeCount := 2
	req.Rows[0].MergeCommitsInRange = &mergeCount
	req.Rows[0].CutoffAncestorProbed = true
	req.Rows[0].CutoffIsAncestor = false
	req.CapsProbed = true
	req.Caps = GitCapabilities{}
	noisyPlan := mustBuildReparentPlan(t, req)
	if noisyPlan.Summary.Plannability != ReparentPlannabilityNoWork ||
		noisyPlan.Runnable || noisyPlan.Refusal.Kind != nil ||
		noisyPlan.Approval.Fingerprint != nil || len(noisyPlan.Blockers) != 0 ||
		noisyPlan.Guard.WouldRefuse {
		t.Fatalf("same-parent no-work leaked replay/cutoff/merge/capability gates: %+v", noisyPlan)
	}

	// An execution route refuses no-work.
	req.Invocation = ReparentInvocationExecute
	exec := mustBuildReparentPlan(t, req)
	if len(exec.Blockers) != 1 || exec.Blockers[0].Kind != ReparentRefusalNoWork {
		t.Fatalf("execution blockers = %+v", exec.Blockers)
	}

	stale := AncestryStatusStale
	req.Invocation = ReparentInvocationPlanOnly
	req.Rows[0].Ancestry = PlanAncestry{Status: &stale}
	stalePlan := mustBuildReparentPlan(t, req)
	if stalePlan.Refusal.Kind == nil || *stalePlan.Refusal.Kind != ReparentRefusalDestinationSameParentStale {
		t.Fatalf("stale same-parent refusal = %+v", stalePlan.Refusal)
	}
	if stalePlan.Refusal.Detail == nil || *stalePlan.Refusal.Detail != reparentSameParentGuidance {
		t.Fatalf("stale same-parent guidance = %+v", stalePlan.Refusal.Detail)
	}
}

// ---------------------------------------------------------------------------
// §8 — limits, approval and the two admission predicates.
// ---------------------------------------------------------------------------

func TestReparentLimitAlgebraMatchesTheSyncLadder(t *testing.T) {
	// Limit-algebra parity (AC-049, AC-050, supporting evidence): the
	// reparent adapters must produce the same
	// PlanGuardEvaluation values as their sync twins for equivalent inputs,
	// so the two ladders cannot drift apart.
	cases := []struct {
		name  string
		count *int
		det   string
		limit int
	}{
		{"within limit", reparentPlanFixtureInt(2), "exact", 5},
		{"exceeded", reparentPlanFixtureInt(9), "exact", 5},
		{"unknown, not resolvable", nil, "unknown", 5},
		{"skip row counts zero", reparentPlanFixtureInt(0), "exact", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replay := PlanEntryReplay{Determinacy: tc.det, CandidateCount: tc.count}
			syncRow := perEntryEvaluationRow(tc.limit, PlanEntry{Name: "pr2", Replay: replay})
			reparentRow := reparentPerEntryEvaluationRow(tc.limit, ReparentPlanRow{Name: "pr2", Replay: replay})
			if syncRow.ID != reparentRow.ID || syncRow.Verdict != reparentRow.Verdict || syncRow.Basis != reparentRow.Basis {
				t.Fatalf("per-entry rows diverged:\nsync     %+v\nreparent %+v", syncRow, reparentRow)
			}
			if intOrNone(syncRow.Value) != intOrNone(reparentRow.Value) {
				t.Fatalf("per-entry values diverged: %v vs %v", syncRow.Value, reparentRow.Value)
			}
			if derefString(syncRow.UnknownKind) != derefString(reparentRow.UnknownKind) {
				t.Fatalf("unknown kinds diverged: %v vs %v", syncRow.UnknownKind, reparentRow.UnknownKind)
			}
		})
	}

	syncTotal := totalEvaluationRow(3, nil, PlanSummary{TotalCandidatesLowerBound: reparentPlanFixtureInt(4)})
	reparentTotal := reparentTotalEvaluationRow(3, nil, ReparentPlanSummary{TotalCandidatesLowerBound: reparentPlanFixtureInt(4)})
	if syncTotal.Verdict != reparentTotal.Verdict || syncTotal.Basis != reparentTotal.Basis {
		t.Fatalf("total rows diverged:\nsync     %+v\nreparent %+v", syncTotal, reparentTotal)
	}
}

func TestBuildReparentPlanAuthoritativeRecoveryStateTakesPrecedence(t *testing.T) {
	req := reparentBuilderRequest()
	req.AuthoritativeStatePresent = true
	req.Blockers = append(req.Blockers,
		ReparentPlanBlocker{
			Kind:   ReparentRefusalSyncStatePresent,
			Detail: "lower-fidelity compatibility envelope is present",
		},
		ReparentPlanBlocker{
			Kind:   ReparentRefusalTargetUnknown,
			Detail: "a lower-fidelity fresh-plan probe could not resolve the target",
		},
		ReparentPlanBlocker{
			Kind:   ReparentRefusalStatePresent,
			Detail: "authoritative recovery state exists",
		},
	)
	plan, err := BuildReparentPlan(req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Runnable || plan.Refusal.Kind == nil ||
		*plan.Refusal.Kind != ReparentRefusalStatePresent {
		t.Fatalf("fresh plan did not prioritize authoritative recovery state: %+v", plan)
	}
	if len(plan.Blockers) == 0 || plan.Blockers[0].Kind != ReparentRefusalStatePresent {
		t.Fatalf("authoritative blocker is not first: %+v", plan.Blockers)
	}
}

func TestReparentLimitsRefuseThroughTheGuardDomain(t *testing.T) {
	req := reparentBuilderRequest()
	limit := 1
	req.Limits = PlanGuardLimits{PerEntry: PlanGuardLimit{Value: &limit, Origin: "flag"}}
	req.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
	plan := mustBuildReparentPlan(t, req)

	if !plan.Guard.WouldRefuse || !plan.Guard.WouldRefuseWithoutApproval {
		t.Fatalf("guard = %+v", plan.Guard)
	}
	// limit-per-entry and limit-total belong to the reused SYNC guard domain
	// and are deliberately absent from the closed 49-member reparent domain,
	// so they must never appear as reparent blockers.
	for _, b := range plan.Blockers {
		if strings.HasPrefix(string(b.Kind), "limit-") {
			t.Fatalf("a limit kind leaked into the reparent domain: %+v", b)
		}
	}
	err := ReparentLimitRefusal(plan, req.Guard)
	guardErr, ok := err.(*PlanGuardRefusalError)
	if !ok {
		t.Fatalf("limit refusal = %v (%T)", err, err)
	}
	if guardErr.Kind != string(RefusalLimitPerEntry) {
		t.Fatalf("limit refusal kind = %q", guardErr.Kind)
	}
	if ReparentPlanAdmissible(plan) {
		t.Fatal("a would-refuse plan is inadmissible")
	}

	// A supplied token waives the limit evaluation; the fingerprint
	// comparison at admission is what then gates the run.
	req.Guard.Approve = strings.Repeat("a", 64)
	waived := mustBuildReparentPlan(t, req)
	if waived.Guard.WouldRefuse {
		t.Fatal("an approved plan does not refuse on limits")
	}
	if len(waived.Approval.Covers.WaivedKinds) == 0 {
		t.Fatal("the waived kinds must disclose which limit was waived")
	}
	if err := ReparentLimitRefusal(waived, req.Guard); err != nil {
		t.Fatalf("an approved invocation must not refuse on limits: %v", err)
	}
}

func TestReparentAdmissionPredicates(t *testing.T) {
	_ = "asserts AC-049 AC-050"
	assertReparentMatrixBehavior(t, "T-036", "fresh-and-resume-admission-predicates")
	limit := 50
	req := reparentBuilderRequest()
	req.Limits = PlanGuardLimits{PerEntry: PlanGuardLimit{Value: &limit, Origin: "flag"}}
	req.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
	fresh := mustBuildReparentPlan(t, req)

	// §7.12 — the fresh predicate.
	if !ReparentPlanAdmissible(fresh) {
		t.Fatalf("a clean fresh plan with limits must be admissible: %+v", fresh.Approval)
	}
	if fresh.Approval.Scope != ReparentApprovalScopeFresh || !fresh.Approval.Covers.RequiresLimits {
		t.Fatalf("fresh approval = %+v", fresh.Approval)
	}

	// §7.12a — a continue document carries no token at all and is admitted
	// on scope alone. The SAME document is inadmissible under the fresh
	// predicate, which is exactly what the two predicates must distinguish.
	req.Route = ReparentRouteContinue
	req.Resumable = true
	req.ApprovedFingerprint = strings.Repeat("b", 64)
	cont := mustBuildReparentPlan(t, req)
	if cont.Approval.Scope != ReparentApprovalScopeResume {
		t.Fatalf("continue scope = %q", cont.Approval.Scope)
	}
	if cont.Approval.Fingerprint != nil {
		t.Fatal("a continue document mints no fingerprint")
	}
	if cont.Approval.Supplied || cont.Approval.Accepted != nil {
		t.Fatal("nothing is supplied or accepted on a resume")
	}
	if cont.Approval.Covers.RequiresLimits {
		t.Fatal("a resume inherits its gate from the approved fresh run")
	}
	if cont.Policy.LimitsOrigin != ReparentLimitsOriginPersisted {
		t.Fatalf("continue limits origin = %q", cont.Policy.LimitsOrigin)
	}
	if cont.State.ApprovedFingerprint == nil {
		t.Fatal("the persisted fingerprint is published as audit evidence in state")
	}
	if !ReparentPlanAdmissible(cont) {
		t.Fatal("a healthy paused run is admissible under the continue predicate")
	}
	if cont.Approval.Usable && cont.Approval.Fingerprint != nil {
		t.Fatal("the fresh predicate's token term can never be satisfied by a resume")
	}
}

func TestRevalidateReparentRowDetectsDrift(t *testing.T) {
	plan := mustBuildReparentPlan(t, reparentBuilderRequest())
	row := plan.Target
	digest, err := ReparentRevalidationDigest(row)
	if err != nil {
		t.Fatal(err)
	}

	live := ReparentRowProbe{
		Name: row.Name, HeadSHA: derefString(row.Head.SHA), HeadFound: true,
		Cutoff: row.Cutoff, Replay: row.Replay,
	}
	if _, err := RevalidateReparentRow(row, digest, live); err != nil {
		t.Fatalf("an unchanged row must revalidate: %v", err)
	}

	drifted := live
	drifted.Replay = PlanEntryReplay{Determinacy: "exact", CandidateCount: reparentPlanFixtureInt(9)}
	_, err = RevalidateReparentRow(row, digest, drifted)
	guardErr, ok := err.(*PlanGuardRefusalError)
	if !ok {
		t.Fatalf("revalidation drift = %v (%T)", err, err)
	}
	if guardErr.Kind != string(RefusalRevalidationMismatch) || !guardErr.StatePreserved {
		t.Fatalf("revalidation refusal = %+v", guardErr)
	}
}

// ---------------------------------------------------------------------------
// §7.5b — argv materialization.
// ---------------------------------------------------------------------------

func TestMaterializeReparentArgvReplacesExactlyOneElement(t *testing.T) {
	template := reparentRowArgv(reparentDescendantOperand("pr2"), "cutoff")
	got, err := materializeReparentArgv(template, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(template) {
		t.Fatalf("materialization changed the argv length: %v", got)
	}
	diff := 0
	for i := range got {
		if got[i] != template[i] {
			diff++
		}
	}
	if diff != 1 {
		t.Fatalf("materialization changed %d elements, want exactly 1", diff)
	}
	if !containsString(got, "abcdef") {
		t.Fatalf("the placeholder was not replaced: %v", got)
	}
	for _, a := range got {
		if a == "-C" {
			t.Fatal("no element may ever become -C")
		}
	}

	// A target row's template carries the pinned OID, so materialization is
	// byte-identical to argv.
	targetTemplate := reparentRowArgv("aaaa", "cutoff")
	same, err := materializeReparentArgv(targetTemplate, "aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(same, " ") != strings.Join(targetTemplate, " ") {
		t.Fatalf("target materialization = %v", same)
	}
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func hasReparentBlocker(plan ReparentPlan, kind ReparentRefusalKind) bool {
	for _, b := range plan.Blockers {
		if b.Kind == kind {
			return true
		}
	}
	return false
}

func hasReparentWarning(plan ReparentPlan, kind string) bool {
	for _, w := range plan.Warnings {
		if w.Kind == kind {
			return true
		}
	}
	return false
}
