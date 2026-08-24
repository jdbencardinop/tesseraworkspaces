package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ============================================================================
// rebase_plan_test.go — TestRebasePlanSchema*
//
// Schema totality for the RebasePlan document (spec.md §4): the
// twenty-five top-level keys in exact order, every nested closed object
// this file's assigned scope covers (fetch.repos[], push.targets[],
// push.targets[].lease) asserted bidirectionally closed, the eighteen
// never-null arrays, the closed token domains (RefusalKind,
// ControlledPathBlocker and the two composed surface domains), the total
// `restore` shape, RebasePlanLayout.WorktreePath, and the source-level
// layering guarantee that package internal never learns a cli type.
// ============================================================================

// rebasePlanTopLevelKeys is the twenty-five top-level document keys, in
// spec.md §4.1's own "Emitted in exactly this order" list — which is
// normative and complete — and therefore also in internal/rebase_plan.go's
// declaration/wire order, since §4.1 order IS the JSON key order. The list is
// transcribed from the specification, never read back out of the Go source
// the assertions below compare against.
var rebasePlanTopLevelKeys = []string{
	"schema_version", "route", "requested_route", "route_triggers", "invocation",
	"workspace", "feature", "policy", "intent", "push", "restore", "fetch",
	"freshness", "repositories", "state", "runnable", "blockers", "warnings",
	"encoding_issues", "config_issues", "entries", "summary", "guard", "refusal",
	"approval",
}

// structJSONFieldOrder returns v's exported struct fields' `json` tag names,
// in Go declaration order — the order encoding/json itself marshals them in.
func structJSONFieldOrder(t *testing.T, v interface{}) []string {
	t.Helper()
	typ := reflect.TypeOf(v)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("structJSONFieldOrder: %T is not a struct", v)
	}
	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

// jsonTopLevelKeyOrder decodes data (a single JSON object) and returns its
// keys in wire (encounter) order, using json.Decoder.Token so map-based
// unmarshalling's unspecified iteration order can never mask a key-order
// regression in the actual bytes on the wire.
func jsonTopLevelKeyOrder(t *testing.T, data []byte) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(data)))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("reading opening token: %v", err)
	}
	if tok != json.Delim('{') {
		t.Fatalf("expected top-level JSON object, got token %v", tok)
	}
	var keys []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			t.Fatalf("reading key token: %v", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			t.Fatalf("expected string key token, got %#v", keyTok)
		}
		keys = append(keys, key)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.Fatalf("skipping value for key %q: %v", key, err)
		}
	}
	return keys
}

func TestRebasePlanSchema_TopLevelKeyOrder(t *testing.T) {
	if len(rebasePlanTopLevelKeys) != 25 {
		t.Fatalf("test fixture itself must list 25 keys, has %d", len(rebasePlanTopLevelKeys))
	}

	declared := structJSONFieldOrder(t, RebasePlan{})
	if !reflect.DeepEqual(declared, rebasePlanTopLevelKeys) {
		t.Errorf("RebasePlan struct field order =\n%v\nwant\n%v", declared, rebasePlanTopLevelKeys)
	}

	data, err := MarshalRebasePlan(RebasePlan{})
	if err != nil {
		t.Fatalf("MarshalRebasePlan: %v", err)
	}
	wire := jsonTopLevelKeyOrder(t, data)
	if !reflect.DeepEqual(wire, rebasePlanTopLevelKeys) {
		t.Errorf("MarshalRebasePlan wire key order =\n%v\nwant\n%v", wire, rebasePlanTopLevelKeys)
	}
}

// jsonObjectKeySet marshals v and returns the sorted, de-duplicated set of
// its top-level JSON object keys.
func jsonObjectKeySet(t *testing.T, v interface{}) []string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal %T into generic object: %v (raw=%s)", v, err, data)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// assertClosedObjectKeys proves label's declared key list and its actually
// emitted JSON keys are the same set, checking both subset directions
// independently and reporting each violation on its own line: declared⊆emitted
// (every documented key really appears on the wire) and emitted⊆declared (the
// wire carries no undocumented key).
func assertClosedObjectKeys(t *testing.T, label string, v interface{}, declared []string) {
	t.Helper()
	declaredSet := make(map[string]bool, len(declared))
	for _, k := range declared {
		if declaredSet[k] {
			t.Fatalf("%s: fixture's own declared list repeats key %q", label, k)
		}
		declaredSet[k] = true
	}

	emitted := jsonObjectKeySet(t, v)
	emittedSet := make(map[string]bool, len(emitted))
	for _, k := range emitted {
		emittedSet[k] = true
	}

	for _, k := range declared {
		if !emittedSet[k] {
			t.Errorf("%s: declared key %q was never emitted (declared⊆emitted violated)", label, k)
		}
	}
	for _, k := range emitted {
		if !declaredSet[k] {
			t.Errorf("%s: emitted key %q is not declared anywhere (emitted⊆declared violated)", label, k)
		}
	}
}

// PlanFreshnessValues is §11.3's closed twelve-value `freshness` enum, in the
// spec's own order. It is declared here — in the schema test that owns the
// document's closed domains — so a production token outside it, or a value
// dropped from it, fails on its own.
var planFreshnessValues = []string{
	"fetched", "possibly-stale", "unknown-fetch-effect", "not-refreshed-no-resolved-remote",
	"local-only", "not-refreshed-continuation", "not-refreshed-no-plan-subject",
	"not-refreshed-live-run", "not-refreshed-context-indeterminate",
	"not-refreshed-submodule-reach-indeterminate", "not-refreshed-local-branch-checked-out",
	"not-refreshed-no-fetch-targets",
}

// planSuppressionCauses is §11.2's six causes IN PRECEDENCE ORDER, which is
// also §4.4a's `fetch.suppression_cause` domain. It introduces no second
// vocabulary: it is exactly the six-member suppression subset of the twelve
// freshness values above, which this file asserts rather than assumes.
var planSuppressionCauses = []string{
	"not-refreshed-continuation",
	"not-refreshed-no-plan-subject",
	"not-refreshed-live-run",
	"not-refreshed-context-indeterminate",
	"not-refreshed-submodule-reach-indeterminate",
	"not-refreshed-local-branch-checked-out",
}

// TestRebasePlanSchema_FetchObjectClosed pins the top-level `fetch` object's
// SEVEN members (§4.4a), in order, for both a populated and a zero value, and
// pins both tri-state members over all three of their values.
func TestRebasePlanSchema_FetchObjectClosed(t *testing.T) {
	want := []string{
		"attempted", "outcome", "policy_source", "suppression_cause",
		"mutated_remote_tracking_refs", "mutated_local_branches", "repos",
	}
	cause := planSuppressionCauses[0]
	tru, fls := true, false
	populated := PlanFetch{
		Attempted:                 true,
		Outcome:                   "ok",
		PolicySource:              "route-default",
		SuppressionCause:          &cause,
		MutatedRemoteTrackingRefs: &tru,
		MutatedLocalBranches:      &fls,
		Repos:                     []PlanFetchRepo{{}},
	}
	assertClosedObjectKeys(t, "fetch", populated, want)
	assertClosedObjectKeys(t, "fetch (zero value)", PlanFetch{}, want)

	// Both tri-states really are tri-states on the wire: true, false and null
	// are three distinct emissions, never two.
	for _, tc := range []struct {
		name  string
		value *bool
		want  interface{}
	}{
		{"true", &tru, true},
		{"false", &fls, false},
		{"null", nil, nil},
	} {
		f := PlanFetch{MutatedRemoteTrackingRefs: tc.value, MutatedLocalBranches: tc.value}
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("marshal fetch (%s): %v", tc.name, err)
		}
		var generic map[string]interface{}
		if err := json.Unmarshal(data, &generic); err != nil {
			t.Fatalf("unmarshal fetch (%s): %v", tc.name, err)
		}
		for _, key := range []string{"mutated_remote_tracking_refs", "mutated_local_branches"} {
			got, present := generic[key]
			if !present {
				t.Fatalf("fetch.%s missing on the %s cell: a tri-state is always emitted", key, tc.name)
			}
			if got != tc.want {
				t.Fatalf("fetch.%s = %#v on the %s cell, want %#v", key, got, tc.name, tc.want)
			}
		}
	}
}

// TestRebasePlanSchema_SuppressionCauseIsTheSixMemberFreshnessSubset asserts
// the two closed domains agree by construction (§4.4a, §11.2, §11.3): every
// suppression cause is a freshness value, the causes are exactly six, the
// freshness values are exactly twelve, and both lists are duplicate-free.
func TestRebasePlanSchema_SuppressionCauseIsTheSixMemberFreshnessSubset(t *testing.T) {
	if len(planFreshnessValues) != 12 {
		t.Fatalf("freshness domain has %d values, want the closed twelve of §11.3", len(planFreshnessValues))
	}
	if len(planSuppressionCauses) != 6 {
		t.Fatalf("suppression cause domain has %d values, want the six of §11.2", len(planSuppressionCauses))
	}
	seen := map[string]bool{}
	for _, v := range planFreshnessValues {
		if seen[v] {
			t.Fatalf("freshness value %q is declared twice", v)
		}
		seen[v] = true
	}
	causeSeen := map[string]bool{}
	for _, c := range planSuppressionCauses {
		if causeSeen[c] {
			t.Fatalf("suppression cause %q is declared twice", c)
		}
		causeSeen[c] = true
		if !seen[c] {
			t.Fatalf("suppression cause %q is not a freshness value: the two domains must be one vocabulary", c)
		}
	}
}

func TestRebasePlanSchema_FetchRepoRowClosed(t *testing.T) {
	// spec.md §4.4a: 8 members, closed.
	want := []string{
		"repo_token", "context_root", "context_common_dir", "context_source",
		"fetch_effect", "context_candidates", "attempted", "ok",
	}
	root := "/repo/root"
	common := "/repo/root/.git"
	source := "worktree"
	row := PlanFetchRepo{
		RepoToken:        "svc",
		ContextRoot:      root,
		ContextCommonDir: common,
		ContextSource:    source,
		Effect:           &PlanFetchEffect{},
		ContextCandidates: []PlanFetchCandidate{
			{ContextRoot: root, ContextSource: source, FirstEntry: strPtr("pr1")},
		},
		Attempted: true,
		OK:        true,
	}
	assertClosedObjectKeys(t, "fetch.repos[]", row, want)

	// The custom MarshalJSON's null-gates: a zero-value row (Attempted:false,
	// every context field "") must still emit exactly the same 8 keys, with
	// context_root/context_common_dir/context_source/ok/fetch_effect null.
	assertClosedObjectKeys(t, "fetch.repos[] (zero value)", PlanFetchRepo{}, want)

	data, err := json.Marshal(PlanFetchRepo{})
	if err != nil {
		t.Fatalf("marshal zero PlanFetchRepo: %v", err)
	}
	var generic map[string]interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, nullKey := range []string{"context_root", "context_common_dir", "context_source", "fetch_effect", "ok"} {
		if v, ok := generic[nullKey]; !ok || v != nil {
			t.Errorf("zero-value fetch.repos[] row: key %q = %#v, want present and null", nullKey, v)
		}
	}
	if arr, ok := generic["context_candidates"].([]interface{}); !ok || arr == nil {
		t.Errorf("zero-value fetch.repos[] row: context_candidates = %#v, want non-null []", generic["context_candidates"])
	}
	if generic["attempted"] != false {
		t.Errorf("zero-value fetch.repos[] row: attempted = %#v, want false", generic["attempted"])
	}
}

func TestRebasePlanSchema_PushTargetRowClosed(t *testing.T) {
	// spec.md §14.1a: 10 members, closed.
	want := []string{
		"repo", "context_id", "repo_root", "materialization", "git_branch",
		"remote", "ref", "force", "lease", "scope",
	}
	target := PlanPushTarget{
		Repo:            "",
		ContextID:       strPtr(strings.Repeat("a", 64)),
		RepoRoot:        strPtr("/repo"),
		Materialization: "materialized",
		GitBranch:       "feat/pr1",
		Remote:          "origin",
		Ref:             "refs/heads/feat/pr1",
		Force:           "with-lease",
		Lease:           PlanLease{Mode: "implicit-remote-tracking", Expectation: "sha", ExpectedSHAFreshness: "fresh"},
		Scope:           "stack-all",
	}
	assertClosedObjectKeys(t, "push.targets[]", target, want)
	assertClosedObjectKeys(t, "push.targets[] (zero value)", PlanPushTarget{}, want)
}

func TestRebasePlanSchema_PushLeaseClosed(t *testing.T) {
	// spec.md §14.2: 5 members, closed.
	want := []string{"mode", "expectation", "expected_ref", "expected_sha", "expected_sha_freshness"}
	lease := PlanLease{
		Mode:                 "implicit-remote-tracking",
		Expectation:          "sha",
		ExpectedRef:          strPtr("refs/remotes/origin/feat/pr1"),
		ExpectedSHA:          strPtr(strings.Repeat("b", 40)),
		ExpectedSHAFreshness: "possibly-stale",
	}
	assertClosedObjectKeys(t, "push.targets[].lease", lease, want)
	assertClosedObjectKeys(t, "push.targets[].lease (zero value)", PlanLease{}, want)

	// expected_sha_freshness is documented "never null" (unlike its siblings):
	// prove the JSON field itself is never encoded as a *string via reflection
	// (a pointer field could round-trip through the same key set test above
	// without this distinction being caught).
	field, ok := reflect.TypeOf(PlanLease{}).FieldByName("ExpectedSHAFreshness")
	if !ok {
		t.Fatal("PlanLease.ExpectedSHAFreshness field not found")
	}
	if field.Type.Kind() == reflect.Pointer {
		t.Errorf("PlanLease.ExpectedSHAFreshness is a pointer type %v, want a non-pointer (never-null) string", field.Type)
	}
}

// strPtr / boolPtr are already declared in internal/agent_status.go and
// reused here (Go compiles every _test.go file of a package together).

// ---------------------------------------------------------------------------
// Never-null arrays (spec.md §4.7): eighteen array fields that must
// encode as `[]`, never `null`, even when the underlying Go slice is nil.
// ---------------------------------------------------------------------------

// assertNeverNullArray asserts that value (obtained by decoding a
// MarshalRebasePlan document into a generic map[string]interface{} and
// walking to path) is a non-nil JSON array. Decoding "null" into
// interface{} yields an untyped nil, which fails the []interface{} type
// assertion below and is reported as the null case explicitly.
func assertNeverNullArray(t *testing.T, path string, value interface{}) {
	t.Helper()
	if value == nil {
		t.Errorf("%s: encoded as JSON null, want a non-null array (possibly empty [])", path)
		return
	}
	if _, ok := value.([]interface{}); !ok {
		t.Errorf("%s: decoded as %T (%v), want a JSON array", path, value, value)
	}
}

func TestRebasePlanSchema_NeverNullArraysOnEmptyDocument(t *testing.T) {
	// A wholly zero-value RebasePlan (every slice field nil) still must
	// render every one of the eighteen never-null arrays as [], never null.
	data, err := MarshalRebasePlan(RebasePlan{})
	if err != nil {
		t.Fatalf("MarshalRebasePlan: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal document: %v", err)
	}

	assertNeverNullArray(t, "route_triggers", doc["route_triggers"])
	assertNeverNullArray(t, "repositories", doc["repositories"])
	assertNeverNullArray(t, "blockers", doc["blockers"])
	assertNeverNullArray(t, "warnings", doc["warnings"])
	assertNeverNullArray(t, "encoding_issues", doc["encoding_issues"])
	assertNeverNullArray(t, "config_issues", doc["config_issues"])
	assertNeverNullArray(t, "entries", doc["entries"])

	fetch, ok := doc["fetch"].(map[string]interface{})
	if !ok {
		t.Fatalf("fetch is not an object: %#v", doc["fetch"])
	}
	assertNeverNullArray(t, "fetch.repos", fetch["repos"])

	push, ok := doc["push"].(map[string]interface{})
	if !ok {
		t.Fatalf("push is not an object: %#v", doc["push"])
	}
	assertNeverNullArray(t, "push.targets", push["targets"])
	assertNeverNullArray(t, "push.blocked_by", push["blocked_by"])

	restore, ok := doc["restore"].(map[string]interface{})
	if !ok {
		t.Fatalf("restore is not an object: %#v", doc["restore"])
	}
	assertNeverNullArray(t, "restore.blocked_by", restore["blocked_by"])

	guard, ok := doc["guard"].(map[string]interface{})
	if !ok {
		t.Fatalf("guard is not an object: %#v", doc["guard"])
	}
	assertNeverNullArray(t, "guard.evaluation", guard["evaluation"])
	assertNeverNullArray(t, "guard.limit_conflicts", guard["limit_conflicts"])
	assertNeverNullArray(t, "guard.execute_blocked_by", guard["execute_blocked_by"])

	approval, ok := doc["approval"].(map[string]interface{})
	if !ok {
		t.Fatalf("approval is not an object: %#v", doc["approval"])
	}
	covers, ok := approval["covers"].(map[string]interface{})
	if !ok {
		t.Fatalf("approval.covers is not an object: %#v", approval["covers"])
	}
	assertNeverNullArray(t, "approval.covers.waived_evaluation_ids", covers["waived_evaluation_ids"])
	assertNeverNullArray(t, "approval.covers.waived_kinds", covers["waived_kinds"])
}

func TestRebasePlanSchema_NeverNullArraysDescendIntoRows(t *testing.T) {
	// entries[].notes[] and fetch.repos[].context_candidates[] are only
	// observable once at least one row exists; both must still be [] (not
	// null) when the row's own slice field is left nil.
	plan := RebasePlan{
		Entries: []PlanEntry{{Name: "pr1", GitBranch: "feat/pr1"}},
		Fetch:   PlanFetch{Repos: []PlanFetchRepo{{RepoToken: "svc"}}},
	}
	data, err := MarshalRebasePlan(plan)
	if err != nil {
		t.Fatalf("MarshalRebasePlan: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	entries, ok := doc["entries"].([]interface{})
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %#v, want a single-element array", doc["entries"])
	}
	entry, ok := entries[0].(map[string]interface{})
	if !ok {
		t.Fatalf("entries[0] is not an object: %#v", entries[0])
	}
	assertNeverNullArray(t, "entries[0].notes", entry["notes"])

	fetch, ok := doc["fetch"].(map[string]interface{})
	if !ok {
		t.Fatalf("fetch is not an object: %#v", doc["fetch"])
	}
	repos, ok := fetch["repos"].([]interface{})
	if !ok || len(repos) != 1 {
		t.Fatalf("fetch.repos = %#v, want a single-element array", fetch["repos"])
	}
	repo, ok := repos[0].(map[string]interface{})
	if !ok {
		t.Fatalf("fetch.repos[0] is not an object: %#v", repos[0])
	}
	assertNeverNullArray(t, "fetch.repos[0].context_candidates", repo["context_candidates"])
}

// ---------------------------------------------------------------------------
// Closed token domains (§3, §7.1, §7.5): RefusalKind, ControlledPathBlocker,
// and the composed PushBlockedKind / RestoreBlockedKind surface domains.
// ---------------------------------------------------------------------------

// wantRefusalKindsInRankOrder is spec.md §7.1's verbatim, closed,
// rank-ordered thirty-member domain, transcribed independently of
// internal/rebase_plan.go's own RefusalKinds var so this test actually
// cross-checks contract against code rather than checking the code against
// itself.
var wantRefusalKindsInRankOrder = []RefusalKind{
	"plan-unavailable", "stack-unsortable", "selection-refused", "state-refused",
	"preflight-refused", "restore-blocked", "fetch-context-indeterminate",
	"fetch-submodule-reach-indeterminate", "fetch-local-branch-checked-out",
	"invalid-git-config", "identity-not-utf8", "repo-unavailable",
	"base-execution-store-divergent", "head-ref-missing", "prunable-worktree",
	"branch-checked-out-elsewhere", "external-rebase-in-progress", "context-dirty",
	"base-unset", "base-ref-missing", "cutoff-unresolvable", "probe-failed",
	"selection-hazard", "guard-limit-mismatch", "approval-without-limits",
	"approval-mismatch", "revalidation-mismatch", "indeterminate-entry",
	"limit-per-entry", "limit-total",
}

func TestRebasePlanSchema_RefusalKindsDomain(t *testing.T) {
	if len(RefusalKinds) != 30 {
		t.Fatalf("len(RefusalKinds) = %d, want 30 (schema-asserted, §7.5)", len(RefusalKinds))
	}
	if len(wantRefusalKindsInRankOrder) != 30 {
		t.Fatalf("test fixture itself must list 30 kinds, has %d", len(wantRefusalKindsInRankOrder))
	}
	if !reflect.DeepEqual(RefusalKinds, wantRefusalKindsInRankOrder) {
		t.Errorf("RefusalKinds =\n%v\nwant (spec.md §7.1 rank order)\n%v", RefusalKinds, wantRefusalKindsInRankOrder)
	}

	seen := make(map[RefusalKind]bool, len(RefusalKinds))
	for _, k := range RefusalKinds {
		if seen[k] {
			t.Errorf("RefusalKinds contains duplicate member %q", k)
		}
		seen[k] = true
	}
}

func TestRebasePlanSchema_ControlledPathBlockerDistinctFromRefusalKind(t *testing.T) {
	// §4.6/§7.5: ControlledPathBlockers is exactly 5 members, in sorted
	// order, and is a DISTINCT Go type from RefusalKind — no implicit
	// conversion exists between the two, even though 3 tokens share spelling.
	want := []ControlledPathBlocker{
		"fetch-context-indeterminate",
		"fetch-local-branch-checked-out",
		"fetch-submodule-reach-indeterminate",
		"live-owner-concurrency",
		"owner-artefact-undecodable",
	}
	if len(ControlledPathBlockers) != 5 {
		t.Fatalf("len(ControlledPathBlockers) = %d, want 5", len(ControlledPathBlockers))
	}
	sorted := append([]ControlledPathBlocker(nil), ControlledPathBlockers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if !reflect.DeepEqual(ControlledPathBlockers, sorted) {
		t.Errorf("ControlledPathBlockers is not sorted: %v", ControlledPathBlockers)
	}
	if !reflect.DeepEqual(ControlledPathBlockers, want) {
		t.Errorf("ControlledPathBlockers = %v, want %v", ControlledPathBlockers, want)
	}

	// The three shared-spelling tokens really share their string
	// representation with a RefusalKind member (that's the whole point of
	// the warning in §4.6), but the Go types are different: this would not
	// even compile as a direct ==, which is itself the distinctness proof —
	// comparison is only possible after an explicit string conversion.
	sharedSpelling := []RefusalKind{
		RefusalFetchContextIndeterminate, RefusalFetchLocalBranchCheckedOut, RefusalFetchSubmoduleReach,
	}
	for _, rk := range sharedSpelling {
		found := false
		for _, cpb := range ControlledPathBlockers {
			if string(cpb) == string(rk) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("RefusalKind %q has no ControlledPathBlocker sharing its spelling, contradicting §4.6", rk)
		}
	}

	// §2.17.7 / §4.6: invalid-git-config and identity-not-utf8 are RefusalKind
	// ranks 5.07/5.08 but are explicitly NOT ControlledPathBlocker members
	// (ownership reasons, not consequence): 5.07 is a failure Git raises on
	// any route, 5.08 is a fingerprint-encoding fact, neither is a
	// controlled-path precondition.
	for _, excluded := range []RefusalKind{RefusalInvalidGitConfig, RefusalIdentityNotUTF8} {
		for _, cpb := range ControlledPathBlockers {
			if string(cpb) == string(excluded) {
				t.Errorf("ControlledPathBlockers must not contain %q (§2.17.7)", excluded)
			}
		}
	}

	// A ControlledPathBlocker value cannot be assigned directly to a
	// RefusalKind variable without a conversion — verified at compile time
	// by the very fact this line, if it existed, would not build:
	//   var _ RefusalKind = ControlledFetchContextIndeterminate
	// The runtime assertion above (spelling match via string(), never ==)
	// is this test's positive evidence that the two types are independent.
	_ = ControlledPathBlocker(string(RefusalFetchContextIndeterminate))
}

func TestRebasePlanSchema_ComposedSurfaceDomains(t *testing.T) {
	// §7.5 "Schema tests (§23)": len(pushDomain)==31, len(restoreDomain)==33.
	// Neither PushBlockedKind nor RestoreBlockedKind declares its own
	// exhaustive slice constant (only RefusalKinds and ControlledPathBlockers
	// do), so this test builds each composed domain from RefusalKinds plus
	// its declared surface-specific extras and checks the resulting sizes
	// and cross-exclusivity the contract requires.
	pushDomain := make(map[PushBlockedKind]bool, len(RefusalKinds)+1)
	for _, k := range RefusalKinds {
		pushDomain[PushBlockedKind(k)] = true
	}
	pushDomain[PushBlockedDroppedRestoring] = true
	if len(pushDomain) != 31 {
		t.Errorf("push.blocked_by[] composed domain has %d members, want 31 (30 RefusalKind + push-dropped-restoring)", len(pushDomain))
	}

	restoreDomain := make(map[RestoreBlockedKind]bool, len(RefusalKinds)+3)
	for _, k := range RefusalKinds {
		restoreDomain[RestoreBlockedKind(k)] = true
	}
	restoreDomain[RestoreBlockedTargetMissing] = true
	restoreDomain[RestoreBlockedTargetHeld] = true
	restoreDomain[RestoreBlockedHeadMoved] = true
	if len(restoreDomain) != 33 {
		t.Errorf("restore.blocked_by[] composed domain has %d members, want 33 (30 RefusalKind + 3 restore-specific)", len(restoreDomain))
	}

	// push-dropped-restoring must be absent from the restore domain (a
	// dropped push is a consequence of restoring, never a reason restore
	// itself cannot run).
	if restoreDomain[RestoreBlockedKind(PushBlockedDroppedRestoring)] {
		t.Errorf("restore.blocked_by[] domain must not contain %q", PushBlockedDroppedRestoring)
	}
	// Each restore-specific token must be absent from the push domain.
	for _, restoreOnly := range []RestoreBlockedKind{RestoreBlockedTargetMissing, RestoreBlockedTargetHeld, RestoreBlockedHeadMoved} {
		if pushDomain[PushBlockedKind(restoreOnly)] {
			t.Errorf("push.blocked_by[] domain must not contain restore-specific token %q", restoreOnly)
		}
	}
}

// ---------------------------------------------------------------------------
// The eight-member warnings[].kind domain (§2.14, §4.6). No exported Go slice
// enumerates this domain (only RefusalKinds and ControlledPathBlockers get
// one), so this test cross-checks the doc comment against the actual
// construction sites: exactly six of the eight tokens are ever literally
// constructed (rebase_plan_build.go's own doc comment explains the remaining
// two, fetch-context-divergent and base-execution-context-split, depend on a
// route shape this tree's request never produces and are therefore never
// fabricated — a documented, intentional gap, not a bug).
// ---------------------------------------------------------------------------

func TestRebasePlanSchema_WarningKindDomainDocumented(t *testing.T) {
	wantDomain := []string{
		"collateral-update-refs-config", "base-execution-context-split",
		"fetch-context-divergent", "autostash-across-switch", "checkout-dirty-present",
		"push-dropped-restoring", "restore-head-collateral-risk", "untracked-present",
	}
	if len(wantDomain) != 8 {
		t.Fatalf("test fixture itself must list 8 tokens, has %d", len(wantDomain))
	}

	body := readSourceFile(t, "rebase_plan.go")
	docComment := extractDocComment(t, body, "type PlanWarning struct")
	for _, token := range wantDomain {
		if !strings.Contains(docComment, token) {
			t.Errorf("PlanWarning's doc comment does not mention domain member %q", token)
		}
	}

	// Every member of the closed eight-member domain is reachable: each has
	// a real construction site fed by a real measured fact.
	reachable := []string{
		"collateral-update-refs-config", "autostash-across-switch", "checkout-dirty-present",
		"push-dropped-restoring", "restore-head-collateral-risk", "untracked-present",
		"fetch-context-divergent", "base-execution-context-split",
	}
	buildBody := readSourceFile(t, "rebase_plan_build.go")
	literal := regexp.MustCompile(`Kind:\s*"([a-z-]+)"`)
	constructed := map[string]bool{}
	for _, m := range literal.FindAllStringSubmatch(buildBody, -1) {
		constructed[m[1]] = true
	}
	// push-dropped-restoring is constructed via the typed constant, not a
	// literal, so it is added to constructed the same way buildWarnings does
	// (string(PushBlockedDroppedRestoring)) rather than expected to match the
	// literal-string regexp above.
	constructed[string(PushBlockedDroppedRestoring)] = true

	for _, token := range reachable {
		if !constructed[token] {
			t.Errorf("warning kind %q is documented reachable but no PlanWarning{Kind: %q, ...} construction site was found in rebase_plan_build.go", token, token)
		}
	}
	for _, token := range wantDomain {
		if !constructed[token] {
			t.Errorf("warning kind %q is a member of the closed domain but has no construction site in rebase_plan_build.go", token)
		}
	}
}

// extractDocComment returns the contiguous run of `//` comment lines
// immediately above needle within body (needle itself excluded), or fails
// the test if needle or a preceding comment block cannot be found.
func extractDocComment(t *testing.T, body, needle string) string {
	t.Helper()
	idx := strings.Index(body, needle)
	if idx < 0 {
		t.Fatalf("needle %q not found in source", needle)
	}
	lines := strings.Split(body[:idx], "\n")
	var comment []string
	for i := len(lines) - 2; i >= 0; i-- { // -2: skip the blank/partial line right before needle
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "//") {
			comment = append([]string{line}, comment...)
			continue
		}
		if line == "" && len(comment) == 0 {
			continue
		}
		break
	}
	return strings.Join(comment, "\n")
}

// ---------------------------------------------------------------------------
// The total `restore` shape (§14.4's own normative ten-member field table,
// transcribed here in its declared order).
// ---------------------------------------------------------------------------

func TestRebasePlanSchema_RestoreShapeTotal(t *testing.T) {
	want := []string{
		"applies", "will_switch_head", "target_branch", "target_sha", "target_source",
		"executable", "blocked_by", "deletes_transaction", "releases_lock", "push_dropped",
	}
	populated := PlanRestore{
		Applies:            true,
		WillSwitchHead:     boolPtr(true),
		TargetBranch:       strPtr("main"),
		TargetSHA:          strPtr(strings.Repeat("c", 40)),
		TargetSource:       strPtr("original-head"),
		Executable:         boolPtr(true),
		BlockedBy:          []RestoreBlockedKind{RestoreBlockedTargetHeld},
		DeletesTransaction: true,
		ReleasesLock:       true,
		PushDropped:        false,
	}
	assertClosedObjectKeys(t, "restore", populated, want)
	assertClosedObjectKeys(t, "restore (zero value)", PlanRestore{}, want)

	// blocked_by is documented never-null; a zero-value (nil) BlockedBy must
	// still encode [] once it passes through the document-level normalizer.
	data, err := MarshalRebasePlan(RebasePlan{})
	if err != nil {
		t.Fatalf("MarshalRebasePlan: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	restore, ok := doc["restore"].(map[string]interface{})
	if !ok {
		t.Fatalf("restore is not an object: %#v", doc["restore"])
	}
	for _, k := range want {
		if _, present := restore[k]; !present {
			t.Errorf("restore.%s missing from wire document entirely", k)
		}
	}
}

// ---------------------------------------------------------------------------
// RebasePlanLayout.WorktreePath (§9.0)
// ---------------------------------------------------------------------------

func TestRebasePlanSchema_LayoutWorktreePath(t *testing.T) {
	checkout := RebasePlanLayout{FeaturePath: "/ws/.features/f", WorktreesRoot: "", RepoRoot: "/repo"}
	if got := checkout.WorktreePath("pr1"); got != "" {
		t.Errorf("checkout-mode layout (WorktreesRoot empty) WorktreePath(%q) = %q, want \"\"", "pr1", got)
	}

	external := RebasePlanLayout{FeaturePath: "/ws/.features/f", WorktreesRoot: "/ws/.worktrees/f"}
	want := filepath.Join("/ws/.worktrees/f", "pr1")
	if got := external.WorktreePath("pr1"); got != want {
		t.Errorf("external-mode layout WorktreePath(%q) = %q, want %q", "pr1", got, want)
	}

	// The empty name is not special-cased away: it still joins.
	if got := external.WorktreePath(""); got != filepath.Clean("/ws/.worktrees/f") {
		t.Errorf("WorktreePath(\"\") = %q, want %q", got, filepath.Clean("/ws/.worktrees/f"))
	}
}

// ---------------------------------------------------------------------------
// Layering: package internal must never learn a cli type (§19.1, §19.2;
// verbatim from rebase_plan_guard.go's own doc comment). This is asserted at
// the source level, over package internal's OWN files only — a non-recursive
// glob of "*.go" in the package directory, deliberately not the recursive
// nonTestGoFiles(t) walk this package's other tests use elsewhere, because
// that walk also descends into the internal/cli subdirectory, whose files
// legitimately import cobra and would make a recursive check meaningless.
// ---------------------------------------------------------------------------

func packageInternalGoFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing package internal's own .go files: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one .go file directly in package internal's directory; is the test cwd wrong?")
	}
	return matches
}

// fileImportSpecs parses only the import declaration of path (go/parser's
// ImportsOnly mode) and returns each import's path plus its local rename, if
// any. Using the real Go parser — rather than a text/regexp scan — means this
// test's own string literals and comments (which must themselves mention
// "cobra"/"internal/cli" as data, to check for them) can never be mistaken
// for a genuine import declaration in any file, including this one.
func fileImportSpecs(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing imports of %s: %v", path, err)
	}
	imports := make(map[string]string, len(f.Imports))
	for _, imp := range f.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("unquoting import path %s in %s: %v", imp.Path.Value, path, err)
		}
		local := ""
		if imp.Name != nil {
			local = imp.Name.Name
		}
		imports[importPath] = local
	}
	return imports
}

func TestRebasePlanSchema_NoCLIOrCobraDependency(t *testing.T) {
	const cobraPath = "github.com/spf13/cobra"
	const cliPath = "github.com/jdbencardinop/tesseraworkspaces/internal/cli"

	for _, file := range packageInternalGoFiles(t) {
		imports := fileImportSpecs(t, file)
		if _, ok := imports[cobraPath]; ok {
			t.Errorf("%s: package internal must not depend on %q, but the file imports it", file, cobraPath)
		}
		if _, ok := imports[cliPath]; ok {
			t.Errorf("%s: package internal must not depend on %q, but the file imports it", file, cliPath)
		}
		// A renamed import that locally binds any OTHER package to the name
		// "cli" would let a `cli.Foo` selector reference that package's
		// exported types while evading the two literal-path checks above;
		// reject that dodge directly, whatever the real import path is.
		for importPath, local := range imports {
			if local == "cli" {
				t.Errorf("%s: import %q is locally renamed to %q; package internal must name no cli type in any shared signature", file, importPath, local)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// ResolveFetchSuppression — the §11.2 cause ladder, driven DIRECTLY
// ---------------------------------------------------------------------------

// fetchCtxWithEffect builds one enumerated fetch context whose measured
// effect is exactly what the caller supplies.
func fetchCtxWithEffect(commonDir string, effect PlanFetchEffect) PlanFetchContext {
	e := effect
	return PlanFetchContext{
		RepoToken: "", Root: commonDir + "/wt", CommonDir: commonDir, Source: "worktree",
		Effect: &e, Candidates: []PlanFetchCandidate{},
	}
}

// reachUnknownEffect is cause 5's own shape: unconditional submodule
// recursion whose reach could not be bounded.
func reachUnknownEffect() PlanFetchEffect {
	return PlanFetchEffect{
		Contacted:          true,
		SubmoduleRecursion: PlanSubmoduleRecursion{Mode: "yes", Reach: "unknown"},
	}
}

// heldLocalBranchEffect is cause 6's own shape: a positive refspec covering
// refs/heads/** whose covered inventory contains a HELD branch.
func heldLocalBranchEffect() PlanFetchEffect {
	return PlanFetchEffect{
		Contacted: true,
		LocalBranchDestinations: PlanLocalBranchDestinations{
			Patterns: []PlanFetchRefspecPattern{{Remote: "origin", Dst: "refs/heads/*"}},
			Branches: []string{"main"},
			Held:     []PlanBranchHold{{Branch: "main", Worktree: "/wt"}},
		},
	}
}

// TestRebasePlanSchema_ResolveFetchSuppressionPrecedence drives
// ResolveFetchSuppression directly over the three causes it owns — 4
// (context-indeterminate), 5 (submodule-reach-indeterminate) and 6
// (local-branch-checked-out) — and asserts the PRECEDENCE between them, not
// merely that each fires alone. Causes 1-3 are decided above this function
// (they are route facts, not measured effects), so this ladder's own
// contract is exactly "4 beats 5 beats 6, and each publishes its own
// controlled token and blocker".
func TestRebasePlanSchema_ResolveFetchSuppressionPrecedence(t *testing.T) {
	both := reachUnknownEffect()
	both.LocalBranchDestinations = heldLocalBranchEffect().LocalBranchDestinations

	cases := []struct {
		name       string
		contexts   []PlanFetchContext
		want       string
		controlled ControlledPathBlocker
		kind       RefusalKind
	}{
		{
			name:     "no contexts suppress nothing",
			contexts: nil,
		},
		{
			name:     "one clean context suppresses nothing",
			contexts: []PlanFetchContext{fetchCtxWithEffect("/a/.git", PlanFetchEffect{Contacted: true})},
		},
		{
			name: "cause 4 fires when two contexts disagree",
			contexts: []PlanFetchContext{
				fetchCtxWithEffect("/a/.git", PlanFetchEffect{Contacted: true}),
				fetchCtxWithEffect("/b/.git", PlanFetchEffect{Contacted: true}),
			},
			want:       "not-refreshed-context-indeterminate",
			controlled: ControlledFetchContextIndeterminate,
			kind:       RefusalFetchContextIndeterminate,
		},
		{
			name:       "cause 5 fires on one context with unbounded submodule reach",
			contexts:   []PlanFetchContext{fetchCtxWithEffect("/a/.git", reachUnknownEffect())},
			want:       "not-refreshed-submodule-reach-indeterminate",
			controlled: ControlledFetchSubmoduleReach,
			kind:       RefusalFetchSubmoduleReach,
		},
		{
			name:       "cause 6 fires on one context whose covered local branch is held",
			contexts:   []PlanFetchContext{fetchCtxWithEffect("/a/.git", heldLocalBranchEffect())},
			want:       "not-refreshed-local-branch-checked-out",
			controlled: ControlledFetchLocalBranchHeld,
			kind:       RefusalFetchLocalBranchCheckedOut,
		},
		{
			name:       "cause 5 BEATS cause 6 on the same context",
			contexts:   []PlanFetchContext{fetchCtxWithEffect("/a/.git", both)},
			want:       "not-refreshed-submodule-reach-indeterminate",
			controlled: ControlledFetchSubmoduleReach,
			kind:       RefusalFetchSubmoduleReach,
		},
		{
			name: "cause 4 BEATS cause 5 and 6",
			contexts: []PlanFetchContext{
				fetchCtxWithEffect("/a/.git", both),
				fetchCtxWithEffect("/b/.git", both),
			},
			want:       "not-refreshed-context-indeterminate",
			controlled: ControlledFetchContextIndeterminate,
			kind:       RefusalFetchContextIndeterminate,
		},
		{
			name: "two AGREEING contexts collapse and never reach cause 4",
			contexts: []PlanFetchContext{
				fetchCtxWithEffect("/a/.git", heldLocalBranchEffect()),
				fetchCtxWithEffect("/a/.git", heldLocalBranchEffect()),
			},
			want:       "not-refreshed-local-branch-checked-out",
			controlled: ControlledFetchLocalBranchHeld,
			kind:       RefusalFetchLocalBranchCheckedOut,
		},
	}

	causes := map[string]bool{}
	for _, c := range planSuppressionCauses {
		causes[c] = true
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveFetchSuppression(tc.contexts)
			if got.Suppressed != tc.want {
				t.Fatalf("Suppressed = %q, want %q", got.Suppressed, tc.want)
			}
			if tc.want == "" {
				if len(got.Controlled) != 0 || len(got.Blockers) != 0 {
					t.Fatalf("an unsuppressed ladder must publish no token and no blocker, got %+v", got)
				}
				return
			}
			if !causes[tc.want] {
				t.Fatalf("%q is not a member of the closed six-cause domain", tc.want)
			}
			if len(got.Controlled) != 1 || got.Controlled[0] != tc.controlled {
				t.Fatalf("Controlled = %v, want exactly [%v]", got.Controlled, tc.controlled)
			}
			if len(got.Blockers) != 1 || got.Blockers[0].Kind != tc.kind {
				t.Fatalf("Blockers = %+v, want exactly one %v", got.Blockers, tc.kind)
			}
			if got.Blockers[0].Detail == "" {
				t.Fatal("a suppression blocker must carry a detail sentence")
			}
		})
	}
}

// ===========================================================================
// §22.19 / §22.20a — the non-waivable rank 1-10 matrix and the aggregate
// evaluation row's unknown-first branch, owned here as pure functions over
// the document model.
// ===========================================================================

// criterion19NonWaivableKinds is §22.19's own list, verbatim and in the
// spec's order: every rank 1-10 fact a matching approval token must NOT be
// able to waive.
var criterion19NonWaivableKinds = []RefusalKind{
	RefusalStateRefused, RefusalPreflightRefused, RefusalRestoreBlocked,
	RefusalRepoUnavailable, RefusalHeadRefMissing, RefusalPrunableWorktree,
	RefusalBranchCheckedOutElsewhere, RefusalContextDirty, RefusalBaseRefMissing,
	RefusalCutoffUnresolvable, RefusalProbeFailed, RefusalInvalidGitConfig,
	RefusalIdentityNotUTF8, RefusalSelectionHazard, RefusalGuardLimitMismatch,
	RefusalApprovalWithoutLimits, RefusalIndeterminateEntry,
}

// TestRebasePlanSchema_Criterion22_19_TokenCannotWaiveRank1To10 is §22.19's
// executable owner at the model level: the ONLY waiver this feature has is
// applyApprovalWaiver, whose domain is exactly ranks 11/12. Every one of
// §22.19's seventeen kinds is asserted to survive an ACCEPTED approval —
// both as a blockers[] row that is never removed or rewritten, and as a
// waived_kinds domain non-member.
//
// The positive control is asserted in the same table: the two limit kinds
// really are waived by the same accepted approval, so the test cannot pass
// by the waiver being a no-op.
func TestRebasePlanSchema_Criterion22_19_TokenCannotWaiveRank1To10(t *testing.T) {
	if len(criterion19NonWaivableKinds) != 17 {
		t.Fatalf("§22.19 names 17 kinds, this table has %d", len(criterion19NonWaivableKinds))
	}

	// Every rank 1-10 kind is a member of the closed RefusalKind domain, so
	// the list cannot silently name something that does not exist.
	declared := map[RefusalKind]bool{}
	for _, k := range RefusalKinds {
		declared[k] = true
	}
	for _, k := range criterion19NonWaivableKinds {
		if !declared[k] {
			t.Fatalf("§22.19 names %q, which is not a declared RefusalKind", k)
		}
	}

	// The waiver, driven with accepted == true, over an evaluation table
	// that ALSO contains the two waivable rows.
	perEntry := 40
	total := 40
	rows := []PlanGuardEvaluation{
		{ID: "entry:root", Limit: 10, Value: &perEntry, Basis: "per-entry", Verdict: "exceeded"},
		{ID: "max_replay_total", Limit: 10, Value: &total, Basis: "total", Verdict: "exceeded"},
	}
	out, waivedIDs, waivedKinds := applyApprovalWaiver(rows, true)
	if len(waivedIDs) != 2 {
		t.Fatalf("waivedIDs = %v, want both limit rows waived (the positive control)", waivedIDs)
	}
	waivable := map[RefusalKind]bool{}
	for _, k := range waivedKinds {
		waivable[k] = true
	}
	if !waivable[RefusalLimitPerEntry] || !waivable[RefusalLimitTotal] {
		t.Fatalf("waivedKinds = %v, want exactly the two limit kinds", waivedKinds)
	}
	if len(waivedKinds) != 2 {
		t.Fatalf("waivedKinds = %v, want EXACTLY two members: the waiver domain is closed", waivedKinds)
	}
	for _, k := range criterion19NonWaivableKinds {
		if waivable[k] {
			t.Fatalf("%q was waived; ranks 1-10 are non-waivable", k)
		}
	}
	for i, row := range out {
		if row.Limit != rows[i].Limit || row.Basis != rows[i].Basis || row.ID != rows[i].ID {
			t.Fatalf("row %d was altered by the waiver beyond its verdict: %+v", i, row)
		}
	}

	// A rank-10 unknown row and a rank 1-9 blocker row are never touched.
	unknownKind := "not-resolvable"
	untouched := []PlanGuardEvaluation{
		{ID: "max_replay_total", Limit: 1, Basis: "lower-bound", Verdict: "unknown", UnknownKind: &unknownKind},
	}
	after, ids, kinds := applyApprovalWaiver(untouched, true)
	if len(ids) != 0 || len(kinds) != 0 {
		t.Fatalf("an unknown row must never be waived: ids=%v kinds=%v", ids, kinds)
	}
	if after[0].Verdict != "unknown" {
		t.Fatalf("verdict = %q, want unknown (untouched)", after[0].Verdict)
	}
}

// TestRebasePlanSchema_Criterion22_20a_AggregateBranchesOnUnknownFirst is
// §22.20a's executable owner over totalEvaluationRow — the aggregate row's
// own producer — asserting the branch really is on the UNKNOWN COUNT FIRST
// and never on the value comparison first.
//
//	U == 0                -> basis "total",       unknown_entries null,
//	                         value == summary.total_candidates,
//	                         exceeded exactly when value > L (INCLUDING the
//	                         exceeding cell, where basis "lower-bound" is a
//	                         failure);
//	U  > 0, B  > L        -> basis "lower-bound", unknown_entries == U,
//	                         verdict "exceeded";
//	U  > 0, B <= L        -> basis "lower-bound", unknown_entries == U,
//	                         verdict "unknown".
func TestRebasePlanSchema_Criterion22_20a_AggregateBranchesOnUnknownFirst(t *testing.T) {
	deferredEntry := func() PlanEntry {
		reason := "upstream-deferred"
		return PlanEntry{Replay: PlanEntryReplay{Determinacy: "unknown", Reason: &reason}}
	}
	resolvedEntry := func() PlanEntry {
		return PlanEntry{Replay: PlanEntryReplay{Determinacy: "exact"}}
	}
	summaryOf := func(total, lowerBound, unknown int) PlanSummary {
		tc, lb := total, lowerBound
		s := PlanSummary{TotalCandidatesLowerBound: &lb, EntriesWithUnknownCandidates: unknown}
		if unknown == 0 {
			s.TotalCandidates = &tc
		}
		return s
	}

	cases := []struct {
		name           string
		limit          int
		entries        []PlanEntry
		summary        PlanSummary
		wantBasis      string
		wantVerdict    string
		wantValue      int
		wantUnknownPtr bool
	}{
		{"no unknown rows within limit", 10, []PlanEntry{resolvedEntry()}, summaryOf(4, 4, 0), "total", "within-limit", 4, false},
		{"no unknown rows exactly at limit", 4, []PlanEntry{resolvedEntry()}, summaryOf(4, 4, 0), "total", "within-limit", 4, false},
		{"no unknown rows OVER limit stays basis total", 3, []PlanEntry{resolvedEntry()}, summaryOf(4, 4, 0), "total", "exceeded", 4, false},
		{"unknown rows with lower bound over limit", 2, []PlanEntry{resolvedEntry(), deferredEntry()}, summaryOf(0, 3, 1), "lower-bound", "exceeded", 3, true},
		{"unknown rows with lower bound within limit", 10, []PlanEntry{resolvedEntry(), deferredEntry()}, summaryOf(0, 3, 1), "lower-bound", "unknown", 3, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := totalEvaluationRow(tc.limit, tc.entries, tc.summary)
			if row.ID != "max_replay_total" {
				t.Fatalf("ID = %q, want max_replay_total", row.ID)
			}
			if row.Basis != tc.wantBasis {
				t.Fatalf("basis = %q, want %q", row.Basis, tc.wantBasis)
			}
			if row.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q", row.Verdict, tc.wantVerdict)
			}
			if row.Value == nil || *row.Value != tc.wantValue {
				t.Fatalf("value = %v, want %d", row.Value, tc.wantValue)
			}
			if tc.wantUnknownPtr {
				if row.UnknownEntries == nil || *row.UnknownEntries != tc.summary.EntriesWithUnknownCandidates {
					t.Fatalf("unknown_entries = %v, want %d (== summary.entries_with_unknown_candidates)",
						row.UnknownEntries, tc.summary.EntriesWithUnknownCandidates)
				}
			} else if row.UnknownEntries != nil {
				t.Fatalf("unknown_entries = %v, want null when U == 0", row.UnknownEntries)
			}
			if tc.wantBasis == "total" {
				if tc.summary.TotalCandidates == nil || *row.Value != *tc.summary.TotalCandidates {
					t.Fatalf("value = %v, want summary.total_candidates %v", row.Value, tc.summary.TotalCandidates)
				}
			}
		})
	}

	// The unknown_kind sub-branch: a deferred-only unknown set is
	// deferred-resolvable; any other unknown reason is not-resolvable.
	merge := "merge-recreation"
	notResolvable := totalEvaluationRow(10,
		[]PlanEntry{{Replay: PlanEntryReplay{Determinacy: "unknown", Reason: &merge}}},
		summaryOf(0, 0, 1))
	if notResolvable.UnknownKind == nil || *notResolvable.UnknownKind != "not-resolvable" {
		t.Fatalf("unknown_kind = %v, want not-resolvable", notResolvable.UnknownKind)
	}
	resolvable := totalEvaluationRow(10, []PlanEntry{deferredEntry()}, summaryOf(0, 0, 1))
	if resolvable.UnknownKind == nil || *resolvable.UnknownKind != "deferred-resolvable" {
		t.Fatalf("unknown_kind = %v, want deferred-resolvable", resolvable.UnknownKind)
	}
}

// TestRebasePlan_Criterion22_33i_HolderProducerInvariant is the named owner of
// §22.33i's producer invariant. Criterion 22.33i's restore-target-held cell
// and the holder inventory it reads are only sound if the codebase has ONE
// place that decides "held" and ONE place that builds a holder inventory:
// a second emission site or a second inventory builder is exactly how the
// self-exclusion rule (a branch checked out in THIS worktree is not "held")
// and the common-dir cache silently diverge. This is a source scan, because
// the invariant is about the shape of the production code, not about any one
// run's output.
func TestRebasePlan_Criterion22_33i_HolderProducerInvariant(t *testing.T) {
	files := planProductionGoFiles(t)

	// (a) exactly one non-const emission of the restore-target-held token.
	// The RestoreBlockedKind constant declaration is the definition, not an
	// emission, so it is excluded by name; every other mention must be the
	// single append inside the restore probe.
	emissions := 0
	literals := 0
	for _, f := range files {
		src := readSourceFile(t, f)
		for i, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, `"restore-target-held"`) {
				literals++
				if !regexp.MustCompile(`RestoreBlockedTargetHeld\s+RestoreBlockedKind\s+=`).MatchString(line) {
					t.Fatalf("%s:%d: the restore-target-held token is spelled as a bare string outside its constant declaration: %s",
						f, i+1, trimmed)
				}
				continue
			}
			if strings.Contains(line, "RestoreBlockedTargetHeld") {
				emissions++
				if !strings.Contains(line, "append(") {
					t.Fatalf("%s:%d: RestoreBlockedTargetHeld is used outside the single append emission site: %s",
						f, i+1, trimmed)
				}
			}
		}
	}
	if literals != 1 {
		t.Fatalf("found %d string literal spellings of restore-target-held in production source, want exactly 1 (its constant declaration)", literals)
	}
	if emissions != 1 {
		t.Fatalf("found %d emission sites of RestoreBlockedTargetHeld in production source, want exactly 1", emissions)
	}

	// (b) exactly one holder-inventory producer. BuildBranchHolderInventory —
	// the process-spending builder — may be called from exactly one place:
	// BuildPlanHolderIndex, the common-dir cache. Every other consumer must
	// go through the index, or the "one worktree list per common dir" census
	// of §22.33i is unenforceable.
	builderCalls := map[string]int{}
	for _, f := range files {
		src := readSourceFile(t, f)
		for i, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "func BuildBranchHolderInventory") {
				continue
			}
			if strings.Contains(line, "BuildBranchHolderInventory(") {
				builderCalls[fmt.Sprintf("%s:%d", f, i+1)]++
			}
		}
	}
	if len(builderCalls) != 1 {
		t.Fatalf("BuildBranchHolderInventory is called from %d sites %v, want exactly 1 (BuildPlanHolderIndex)", len(builderCalls), builderCalls)
	}
	wantSite := fmt.Sprintf("rebase_plan_probe.go:%d", buildPlanHolderIndexCallLine(t, "rebase_plan_probe.go"))
	for site := range builderCalls {
		if site != wantSite {
			t.Fatalf("the sole BuildBranchHolderInventory call is at %s, want %s (inside BuildPlanHolderIndex)", site, wantSite)
		}
	}

	// (c) the self-exclusion decision — "held means held by a worktree that
	// is not this one" — is made in exactly one place too.
	exclusions := 0
	for _, f := range files {
		src := readSourceFile(t, f)
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, "targetHeld = canonicalize(") {
				exclusions++
			}
		}
	}
	if exclusions != 1 {
		t.Fatalf("found %d restore-target self-exclusion sites, want exactly 1", exclusions)
	}
}

// planProductionGoFiles lists every non-test Go source file of the two
// packages that carry the plan implementation, relative to the internal
// package directory, so a structural invariant can be asserted over the
// whole production surface rather than over one file a reviewer remembered.
func planProductionGoFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, dir := range []string{".", "cli"} {
		matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("globbing %s: %v", dir, err)
		}
		for _, m := range matches {
			if strings.HasSuffix(m, "_test.go") {
				continue
			}
			files = append(files, m)
		}
	}
	if len(files) < 2 {
		t.Fatalf("found %d production Go files, want the internal and internal/cli sources", len(files))
	}
	return files
}

// buildPlanHolderIndexCallLine reports the line inside BuildPlanHolderIndex
// that spends the worktree-list process, so (b) can name it rather than
// trusting a file-level match alone.
func buildPlanHolderIndexCallLine(t *testing.T, path string) int {
	t.Helper()
	src := readSourceFile(t, path)
	inFunc := false
	for i, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, "func BuildPlanHolderIndex(") {
			inFunc = true
			continue
		}
		if inFunc && strings.HasPrefix(line, "}") {
			break
		}
		if inFunc && strings.Contains(line, "BuildBranchHolderInventory(") {
			return i + 1
		}
	}
	t.Fatalf("BuildPlanHolderIndex does not call BuildBranchHolderInventory in %s", path)
	return 0
}

// TestRebasePlanSchema_UnavailableAlwaysNamesItsCause is the schema-level
// owner of §13.7a rule 4's consistency requirement: a document may not
// publish `plannability: unavailable` with an empty blockers[], a null
// refusal and `runnable: false` — a shape that tells the operator the run is
// impossible while naming no cause at all.
//
// It is asserted at the two places the shape is decided:
//
//	(a) computeRunnable reads the BLOCKERS alone — it has no plannability
//	    short-circuit, so an unavailable document with no blocker would be
//	    published as runnable, which is exactly what makes a missing cause a
//	    test failure rather than a silent, self-consistent lie;
//	(b) every rows-less REQUEST this tree can assemble — the stack, sort and
//	    selection errors, and a continuation with no persisted subject —
//	    publishes its own rank 1-5 cause, a non-null refusal and
//	    runnable: false.
func TestRebasePlanSchema_UnavailableAlwaysNamesItsCause(t *testing.T) {
	t.Run("computeRunnable_has_no_plannability_shortcircuit", func(t *testing.T) {
		if !computeRunnable(nil, "unavailable") {
			t.Fatal("computeRunnable must read blockers alone: an unavailable document with NO blocker is a missing cause, not a refusal")
		}
		if computeRunnable([]PlanBlocker{{Kind: RefusalStateRefused}}, "unavailable") {
			t.Fatal("a rank 3 cause must make the document non-runnable")
		}
		if computeRunnable([]PlanBlocker{{Kind: RefusalLimitTotal}}, "unavailable") != true {
			t.Fatal("ranks 11/12 never force runnable:false, on any plannability")
		}
		src := readSourceFile(t, "rebase_plan_build.go")
		body := src[strings.Index(src, "func computeRunnable("):]
		body = body[:strings.Index(body, "\n}\n")]
		if strings.Contains(body, `plannability == "unavailable"`) {
			t.Fatal("computeRunnable must not short-circuit on plannability: the cause, not the token, decides runnable")
		}
	})

	causes := []struct {
		name string
		req  RebasePlanRequest
		kind RefusalKind
	}{
		{
			name: "stack-load-error",
			req:  RebasePlanRequest{Mode: ModeExternal, StackErr: errors.New("load stack: broken")},
			kind: RefusalPlanUnavailable,
		},
		{
			name: "sort-error",
			req:  RebasePlanRequest{Mode: ModeExternal, SortErr: errors.New("cycle detected in stack.yaml")},
			kind: RefusalStackUnsortable,
		},
		{
			name: "selection-error",
			req:  RebasePlanRequest{Mode: ModeExternal, SelectionErr: errors.New("unknown entry")},
			kind: RefusalSelectionRefused,
		},
		{
			name: "continuation-without-a-persisted-subject",
			req: RebasePlanRequest{
				Mode: ModeExternal, Continue: true,
				ContinuationGate: PlanContinuationGate{
					Applies: true, Failed: true, Axis: "state",
					Detail: "nothing to continue — no sync in progress",
				},
			},
			kind: RefusalStateRefused,
		},
	}
	for _, c := range causes {
		t.Run(c.name, func(t *testing.T) {
			plan, err := BuildRebasePlan(c.req)
			if err != nil {
				t.Fatalf("BuildRebasePlan: %v", err)
			}
			if plan.Summary.Plannability != "unavailable" {
				t.Fatalf("plannability = %q, want unavailable", plan.Summary.Plannability)
			}
			if len(plan.Blockers) == 0 {
				t.Fatal("an unavailable document must publish its own cause")
			}
			found := false
			for _, b := range plan.Blockers {
				if b.Kind == c.kind {
					found = true
				}
				if rank, ok := refusalRank[b.Kind]; !ok || rank > refusalRank[RefusalProbeFailed] {
					t.Fatalf("blocker %v is not an effective rank 1-5 cause", b)
				}
			}
			if !found {
				t.Fatalf("blockers %v, want one of kind %q", plan.Blockers, c.kind)
			}
			if plan.Refusal.Kind == nil {
				t.Fatal("refusal.kind must never be null on an unavailable document")
			}
			if plan.Runnable {
				t.Fatal("runnable must be false when a rank 1-5 cause is published")
			}
		})
	}
}
