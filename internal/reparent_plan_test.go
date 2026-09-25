package internal

import (
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2): the document's shape and its closed domains.
//
//	T-031 schema key counts, order, nullability, stream routing ... AC-041, AC-043, AC-044, AC-045
//	T-033 sync schema/fingerprint/refusal/warning domains frozen .. AC-042
//	T-081 refusal domain count/order and the anchored prefixes .... AC-094, AC-097
//	T-084 validation-order precedence fixture .................... AC-098
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Schema shape — key order and key counts are the contract (§7.3, §7.5, §7.11)
// ---------------------------------------------------------------------------

// reparentJSONKeys returns a struct's JSON key names in declaration order,
// which is also its wire key order.
func reparentJSONKeys(t *testing.T, v any) []string {
	t.Helper()
	typ := reflect.TypeOf(v)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%T is not a struct", v)
	}
	keys := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" {
			t.Fatalf("%s.%s has no json tag", typ.Name(), typ.Field(i).Name)
		}
		keys = append(keys, strings.Split(tag, ",")[0])
	}
	return keys
}

func reparentAssertKeys(t *testing.T, value any, want []string) {
	t.Helper()
	got := reparentJSONKeys(t, value)
	if len(got) != len(want) {
		t.Fatalf("%T has %d keys, want exactly %d\n got: %v\nwant: %v", value, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%T key %d = %q, want %q\n got: %v\nwant: %v", value, i, got[i], want[i], got, want)
		}
	}
}

func TestReparentPlan_TopLevelKeyOrderIsFrozenAt25(t *testing.T) {
	_ = "asserts AC-041 AC-043 AC-044 AC-045"
	reparentAssertKeys(t, ReparentPlan{}, []string{
		"schema_version", "route", "invocation", "workspace", "feature",
		"policy", "target", "descendants", "metadata_delta", "strategy",
		"holders", "remote", "fetch", "freshness", "repositories", "state",
		"runnable", "blockers", "warnings", "encoding_issues", "config_issues",
		"summary", "guard", "refusal", "approval",
	})
	if ReparentPlanSchemaVersion != 1 {
		t.Fatalf("ReparentPlanSchemaVersion = %d, want 1", ReparentPlanSchemaVersion)
	}
	if RebasePlanSchemaVersion != 1 {
		t.Fatalf("the sync document's schema version must not move with this feature")
	}
}

func TestReparentPlanRow_KeyOrderIsFrozenAt23(t *testing.T) {
	reparentAssertKeys(t, ReparentPlanRow{}, []string{
		"role", "order", "name", "git_branch", "repo", "materialization",
		"execution_context", "head", "old_parent", "new_parent",
		"destination_binding", "destination_parent", "destination_sha",
		"cutoff", "strategy", "argv", "effective_backend", "replay",
		"merge_commits_in_range", "ancestry", "pins", "collateral_refs", "notes",
	})
}

func TestReparentPlanSummaryAndApproval_KeyCountsAreFrozen(t *testing.T) {
	reparentAssertKeys(t, ReparentPlanSummary{}, []string{
		"plannability", "has_work", "rows", "descendants", "deferred_rows",
		"max_entry_candidates", "total_candidates", "total_candidates_lower_bound",
		"rows_with_unknown_candidates", "collateral_refs", "collateral_bound",
	})
	reparentAssertKeys(t, ReparentPlanApprovalCovers{}, []string{
		"scope", "waived_evaluation_ids", "waived_kinds", "hard_blockers_waived",
		"has_work", "requires_limits", "encoding_safe", "note",
	})
	reparentAssertKeys(t, ReparentPlanApproval{}, []string{
		"fingerprint", "usable", "scope", "supplied", "accepted", "covers",
	})
}

func TestReparentPlanNestedBlocks_KeyOrder(t *testing.T) {
	reparentAssertKeys(t, ReparentPlanPolicy{}, []string{
		"fetch", "fetch_default_applied", "onto_kind_requested", "oid_width",
		"cutoff_supplied", "limits_supplied", "limits_origin", "validation",
	})
	reparentAssertKeys(t, ReparentPlanValidation{}, []string{
		"applies", "source", "command_digest", "runs_on", "requires_clean_context",
	})
	reparentAssertKeys(t, ReparentParent{}, []string{
		"requested_token", "stored_token", "kind", "ref", "sha", "repo",
		"resolution", "candidates", "resolver_agreement",
	})
	reparentAssertKeys(t, ReparentPlanCutoff{}, []string{
		"recorded_sha", "recorded_state", "supplied_token", "supplied_sha",
		"resolved_sha", "provenance", "ancestor_of_branch", "conflict",
	})
	reparentAssertKeys(t, ReparentPlanMetadataDelta{}, []string{
		"entries", "stack_sha256_before", "stack_sha256_after_expected",
		"writer", "write_point", "post_image_known", "entries_outside_closure_changed",
	})
	reparentAssertKeys(t, ReparentPlanMetadataEntry{}, []string{
		"name", "base_before", "base_after", "last_base_sha_before",
		"last_base_sha_after", "last_base_sha_after_source", "changed",
	})
	reparentAssertKeys(t, ReparentPlanStrategy{}, []string{
		"kind", "run_id", "computation_context", "computation_path", "backend",
		"uses_git_replay", "forbidden_operations", "pin_namespace", "atomicity",
	})
	reparentAssertKeys(t, ReparentPlanAtomicity{}, []string{
		"ref_backend", "ref_commit", "ref_commit_race_atomic", "ref_commit_crash_atomic",
		"metadata_write_atomic", "metadata_write_durable", "combined_atomic",
		"partial_commit_recovery", "commit_point", "reference_transaction_hook_may_veto",
	})
	reparentAssertKeys(t, ReparentPlanState{}, []string{
		"snapshot", "files", "worktree", "git_op", "head", "exclusion", "approved_fingerprint",
	})
	reparentAssertKeys(t, ReparentPlanStateFiles{}, []string{
		"checkout_transaction", "checkout_lock", "external_legacy_state",
		"external_run_payload", "external_run_guard", "reparent_state",
		"reparent_remote_record",
	})
}

// TestReparentPlan_ReusesTheShippedTypesVerbatim proves §7.2's reuse is real
// reuse — the same Go types, not lookalikes — and that the sync-only types it
// forbids are absent from the document.
func TestReparentPlan_ReusesTheShippedTypesVerbatim(t *testing.T) {
	_ = "asserts AC-095"
	plan := ReparentPlan{}
	reused := map[string]reflect.Type{
		"workspace":       reflect.TypeOf(PlanWorkspace{}),
		"fetch":           reflect.TypeOf(PlanFetch{}),
		"guard":           reflect.TypeOf(PlanGuardBlock{}),
		"repositories":    reflect.TypeOf([]PlanRepository{}),
		"encoding_issues": reflect.TypeOf([]PlanEncodingIssue{}),
		"config_issues":   reflect.TypeOf([]PlanConfigIssue{}),
	}
	typ := reflect.TypeOf(plan)
	for i := 0; i < typ.NumField(); i++ {
		key := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if want, ok := reused[key]; ok && typ.Field(i).Type != want {
			t.Fatalf("field %q is %s, want the shipped %s", key, typ.Field(i).Type, want)
		}
	}

	row := reflect.TypeOf(ReparentPlanRow{})
	for name, want := range map[string]reflect.Type{
		"ExecutionContext": reflect.TypeOf(PlanContext{}),
		"Head":             reflect.TypeOf(PlanEntryHead{}),
		"Replay":           reflect.TypeOf(PlanEntryReplay{}),
		"Ancestry":         reflect.TypeOf(PlanAncestry{}),
		"CollateralRefs":   reflect.TypeOf([]PlanCollateralRef{}),
	} {
		field, ok := row.FieldByName(name)
		if !ok {
			t.Fatalf("ReparentPlanRow has no %s", name)
		}
		if field.Type != want {
			t.Fatalf("ReparentPlanRow.%s is %s, want the shipped %s", name, field.Type, want)
		}
	}

	// approval.covers.waived_kinds is deliberately the SYNC guard domain.
	covers, _ := reflect.TypeOf(ReparentPlanApprovalCovers{}).FieldByName("WaivedKinds")
	if covers.Type != reflect.TypeOf([]RefusalKind{}) {
		t.Fatalf("waived_kinds is %s, want []RefusalKind", covers.Type)
	}
}

// ---------------------------------------------------------------------------
// Refusal and warning domains (§13.1, §7.11)
// ---------------------------------------------------------------------------

func TestReparentRefusalKinds_ExactlyFortyNineInRankOrder(t *testing.T) {
	_ = "asserts AC-094 AC-097"
	assertReparentMatrixBehavior(t, "T-081", "refusal-domain-and-prefixes")
	want := []ReparentRefusalKind{
		"plan-unavailable", "stack-unsortable", "duplicate-entry-name",
		"duplicate-git-branch", "target-unknown", "target-archived",
		"affected-archived", "cross-repo-closure", "repo-unavailable",
		"destination-unset", "destination-unresolvable", "destination-ambiguous",
		"destination-kind-mismatch", "destination-self", "destination-descendant",
		"destination-cycle", "destination-resolver-divergent",
		"destination-same-parent-stale", "branch-ref-missing", "cutoff-absent",
		"cutoff-unresolvable", "cutoff-conflict", "cutoff-not-ancestor",
		"descendant-cutoff-absent", "descendant-cutoff-override-unsupported",
		"merge-commit-in-replay-set", "git-operation-in-progress", "context-dirty",
		"untracked-overwrite", "holder-unsafe", "session-live",
		"scratch-worktree-unavailable", "sync-state-present", "reparent-state-present",
		"reparent-state-unsupported", "reparent-state-corrupt", "reparent-state-foreign",
		"compat-artifact-missing", "probe-failed", "capability-unsupported",
		"validation-failed", "conflict-unresolved", "ref-transaction-mismatch",
		"ref-foreign-value", "metadata-pending", "metadata-drift",
		"abort-foreign-value", "remote-followup-unsafe-lease", "no-work",
	}
	if len(ReparentRefusalKinds) != 49 {
		t.Fatalf("ReparentRefusalKinds has %d members, want exactly 49", len(ReparentRefusalKinds))
	}
	for i := range want {
		if ReparentRefusalKinds[i] != want[i] {
			t.Fatalf("rank %d = %q, want %q", i+1, ReparentRefusalKinds[i], want[i])
		}
	}

	seen := make(map[ReparentRefusalKind]bool, len(ReparentRefusalKinds))
	for _, kind := range ReparentRefusalKinds {
		if seen[kind] {
			t.Fatalf("duplicate kind %q", kind)
		}
		seen[kind] = true
	}

	// The deliberate absences: guard-domain kinds and the progress token.
	for _, absent := range []ReparentRefusalKind{
		"limit-per-entry", "limit-total", "guard-limit-mismatch",
		"approval-without-limits", "approval-mismatch", "revalidation-mismatch",
		"ref-transaction-partial", "sentinel-missing",
	} {
		if seen[absent] {
			t.Fatalf("%q must not be a reparent refusal kind", absent)
		}
	}
	if ReparentRecoveryPartialToken != "ref-transaction-partial" {
		t.Fatalf("recovery token = %q", ReparentRecoveryPartialToken)
	}

	// The sync domain is untouched.
	if len(RefusalKinds) != 30 {
		t.Fatalf("the sync RefusalKinds domain has %d members, want the frozen 30", len(RefusalKinds))
	}
}

func TestReparentWarningKinds_ExactlyEleven(t *testing.T) {
	want := []string{
		"destination-remote-tracking", "destination-not-remote-rewritten",
		"destination-ancestor-of-old-parent", "collateral-ref-in-replay-range",
		"collateral-update-refs-config", "holder-detach-required",
		"holder-restore-deferred", "untracked-present",
		"remote-divergence-expected", "remote-followup-pending",
		"files-backend-not-crash-atomic",
	}
	if len(ReparentWarningKinds) != 11 {
		t.Fatalf("ReparentWarningKinds has %d members, want 11", len(ReparentWarningKinds))
	}
	for i := range want {
		if ReparentWarningKinds[i] != want[i] {
			t.Fatalf("warning %d = %q, want %q", i, ReparentWarningKinds[i], want[i])
		}
	}
}

func TestSelectPrimaryReparentRefusal_RanksDedupesAndOrders(t *testing.T) {
	entryB := "b-entry"
	entryA := "a-entry"
	blockers := []ReparentPlanBlocker{
		{Kind: ReparentRefusalNoWork, Detail: "lowest rank"},
		{Kind: ReparentRefusalCutoffAbsent, Entry: &entryB, Detail: "second entry"},
		{Kind: ReparentRefusalCutoffAbsent, Entry: &entryA, Detail: "first entry"},
		{Kind: ReparentRefusalCutoffAbsent, Detail: "document level"},
		{Kind: ReparentRefusalTargetUnknown, Detail: "rank five"},
		{Kind: ReparentRefusalTargetUnknown, Detail: "rank five"},
	}

	kind, ordered := SelectPrimaryReparentRefusal(blockers)
	if kind != ReparentRefusalTargetUnknown {
		t.Fatalf("primary kind = %q, want the highest-ranked target-unknown", kind)
	}
	if len(ordered) != 5 {
		t.Fatalf("ordered has %d rows, want 5 after exact-duplicate collapse", len(ordered))
	}
	if ordered[0].Kind != ReparentRefusalTargetUnknown {
		t.Fatalf("ordered[0] = %+v", ordered[0])
	}
	if ordered[1].Entry != nil {
		t.Fatalf("a document-level blocker must sort before entry-scoped ones: %+v", ordered[1])
	}
	if ordered[2].Entry == nil || *ordered[2].Entry != entryA {
		t.Fatalf("entry ordering is wrong: %+v", ordered[2])
	}
	if ordered[len(ordered)-1].Kind != ReparentRefusalNoWork {
		t.Fatalf("lowest rank must sort last: %+v", ordered[len(ordered)-1])
	}

	if kind, ordered := SelectPrimaryReparentRefusal(nil); kind != "" || len(ordered) != 0 {
		t.Fatalf("empty input = (%q, %v), want the zero kind and an empty slice", kind, ordered)
	}
}

func TestReparentRefusalError_ComposesTheAnchoredShape(t *testing.T) {
	plain := reparentRefusal(ReparentRefusalNoWork, "nothing to do")
	if plain.Error() != "no-work: nothing to do" {
		t.Fatalf("Error() = %q", plain.Error())
	}
	preserved := &ReparentRefusalError{Kind: ReparentRefusalMetadataPending, Detail: "refs moved", StatePreserved: true}
	if preserved.Error() != "metadata-pending: state-preserved: refs moved" {
		t.Fatalf("Error() = %q", preserved.Error())
	}
	if strings.Contains(plain.Error(), "reparent: ") {
		t.Fatal("the marker is the caller's framing and must never be inside the error")
	}

	entry := reparentEntryRefusal(ReparentRefusalCutoffAbsent, "pr2", "no boundary")
	blocker := entry.Blocker()
	if blocker.Kind != ReparentRefusalCutoffAbsent || blocker.Entry == nil || *blocker.Entry != "pr2" || blocker.Detail != "no boundary" {
		t.Fatalf("Blocker() = %+v", blocker)
	}

	multiline := &ReparentRefusalError{
		Kind:   ReparentRefusalValidationFailed,
		Detail: "hook failed\r\nfirst line\nsecond line\rtail",
	}
	if got := multiline.Error(); got != "validation-failed: hook failed first line second line tail" {
		t.Fatalf("multiline refusal = %q", got)
	}
	if blocker := multiline.Blocker(); strings.ContainsAny(blocker.Detail, "\r\n") {
		t.Fatalf("multiline blocker detail was not sanitized: %q", blocker.Detail)
	}
	if got := AnchorReparentRefusal(multiline).Error(); strings.Count(got, "reparent:") != 1 ||
		strings.ContainsAny(got, "\r\n") {
		t.Fatalf("anchored multiline refusal = %q", got)
	}
}

// ---------------------------------------------------------------------------
// EvaluateReparentGuard (§8.1)
// ---------------------------------------------------------------------------

func TestEvaluateReparentGuard_ThreeRungLadder(t *testing.T) {
	limit := 1
	guarded := CheckoutPlanGuard{MaxPerEntry: &limit}
	if !guarded.Guarded() {
		t.Fatal("fixture guard must be guarded")
	}

	runnable := ReparentPlan{Runnable: true}
	if err := EvaluateReparentGuard(runnable, guarded); err != nil {
		t.Fatalf("a clean runnable document must be admitted: %v", err)
	}

	// An unguarded invocation is a safe no-op even for a refusing document.
	kind := ReparentRefusalNoWork
	detail := "nothing to do"
	refusing := ReparentPlan{Refusal: ReparentPlanRefusal{Kind: &kind, Detail: &detail}}
	if err := EvaluateReparentGuard(refusing, CheckoutPlanGuard{}); err != nil {
		t.Fatalf("an unguarded invocation must not refuse: %v", err)
	}

	// Rung 1: refusal.kind.
	err := EvaluateReparentGuard(refusing, guarded)
	var guardErr *PlanGuardRefusalError
	if err == nil || !asPlanGuardRefusal(err, &guardErr) {
		t.Fatalf("error = %v, want a *PlanGuardRefusalError", err)
	}
	if guardErr.Kind != string(ReparentRefusalNoWork) || guardErr.Detail != detail {
		t.Fatalf("guard error = %+v", guardErr)
	}
	if strings.Contains(guardErr.Error(), "plan-guard: ") {
		t.Fatal("the plan-guard marker is the caller's framing")
	}

	// Rung 2: not runnable.
	err = EvaluateReparentGuard(ReparentPlan{}, guarded)
	if err == nil || !asPlanGuardRefusal(err, &guardErr) || guardErr.Kind != string(RefusalStateRefused) {
		t.Fatalf("not-runnable error = %v", err)
	}

	// Rung 3: a controlled-path token, mapped through the shipped table.
	blocked := ReparentPlan{
		Runnable: true,
		Guard:    PlanGuardBlock{ExecuteBlockedBy: []ControlledPathBlocker{ControlledLiveOwnerConcurrency}},
	}
	err = EvaluateReparentGuard(blocked, guarded)
	if err == nil || !asPlanGuardRefusal(err, &guardErr) {
		t.Fatalf("blocked error = %v", err)
	}
	if guardErr.Kind != string(controlledPathBlockerKind(ControlledLiveOwnerConcurrency)) {
		t.Fatalf("kind = %q, want the shipped controlled-path mapping", guardErr.Kind)
	}
}

func asPlanGuardRefusal(err error, target **PlanGuardRefusalError) bool {
	refusal, ok := err.(*PlanGuardRefusalError)
	if ok {
		*target = refusal
	}
	return ok
}

// ---------------------------------------------------------------------------
// Writers and frozen constants
// ---------------------------------------------------------------------------

func TestReparentWriters_CarriesBothStreams(t *testing.T) {
	typ := reflect.TypeOf(ReparentWriters{})
	if typ.NumField() != 2 {
		t.Fatalf("ReparentWriters has %d fields, want Doc and Prose", typ.NumField())
	}
	for _, name := range []string{"Doc", "Prose"} {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("ReparentWriters has no %s", name)
		}
		if field.Type.String() != "io.Writer" {
			t.Fatalf("ReparentWriters.%s is %s, want io.Writer", name, field.Type)
		}
	}
}

func TestReparentFrozenConstants(t *testing.T) {
	_ = "asserts AC-042"
	assertReparentMatrixBehavior(t, "T-033", "sync-domains-frozen")
	if ReparentDeferredRendering != "(computed at replay)" {
		t.Fatalf("deferred literal = %q", ReparentDeferredRendering)
	}
	if ReparentRemoteName != "origin" {
		t.Fatalf("remote = %q; this feature names exactly one remote", ReparentRemoteName)
	}
	if ReparentRowBackend != "merge" || ReparentRowStrategy != "detached-rebase-onto" {
		t.Fatalf("row strategy/backend = %q/%q", ReparentRowStrategy, ReparentRowBackend)
	}
	if ReparentMetadataWriter != "durable-atomic-stack-writer" || ReparentMetadataWritePoint != "after-ref-commit" {
		t.Fatalf("metadata writer/write point = %q/%q", ReparentMetadataWriter, ReparentMetadataWritePoint)
	}
	if ReparentCommitPoint != "durable-post-image-metadata" {
		t.Fatalf("commit point = %q", ReparentCommitPoint)
	}
	if ReparentPinNamespaceShape != "refs/tws/reparent/<run-id>" {
		t.Fatalf("pin namespace shape = %q", ReparentPinNamespaceShape)
	}
	want := []string{
		"git reset --hard", "git replay", "git rebase --update-refs",
		"git rebase --autostash", "git rebase --apply", "git push",
	}
	if len(ReparentForbiddenOperations) != len(want) {
		t.Fatalf("forbidden operations = %v", ReparentForbiddenOperations)
	}
	for i := range want {
		if ReparentForbiddenOperations[i] != want[i] {
			t.Fatalf("forbidden operation %d = %q, want %q", i, ReparentForbiddenOperations[i], want[i])
		}
	}
}
