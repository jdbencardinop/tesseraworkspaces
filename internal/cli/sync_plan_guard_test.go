package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// runSyncExecute drives the REAL package-level cli.Execute() entrypoint
// (root.go) — the one place a returned RunE error reaches stderr exactly as
// shipped: cobra's own "Error: " line plus its full Usage/Flags dump (syncCmd
// never sets SilenceUsage), followed by root.go's own trailing duplicate
// message line (its hardcoded fmt.Fprintln(os.Stderr, err)). syncExecute
// (sync_golden_test.go) is NOT a substitute here: it builds syncCmd()
// directly (skipping the "sync" subcommand lookup a real "tws sync ..."
// invocation performs) and forces SilenceErrors/SilenceUsage=true, so its
// stderr shape never matches production's. Every test in this file that
// asserts the plan-guard marker's exact form or position uses this helper.
func runSyncExecute(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = append([]string{"tws", "sync"}, args...)
	stdout, stderr = syncCaptureStreams(t, func() {
		exit = Execute()
	})
	return stdout, stderr, exit
}

// planGuardMarkerRe is spec.md §6.4's marker grammar:
// "^plan-guard: <kind>: <sanitized-detail>$", anchored to one physical line.
var planGuardMarkerRe = regexp.MustCompile(`(?m)^plan-guard: [a-z][a-z-]*: .*$`)

// ---------------------------------------------------------------------------
// §3.4 incompatibility matrix
// ---------------------------------------------------------------------------

// TestSyncPlanGuard_IncompatibilityMatrix exercises every row of spec §3.4 in
// its documented evaluation order, with the exact byte-for-byte message each
// row requires: exit 1, no stdout, and (per the row) the "--json"/"--abort"/
// "--max-replay-*"/"--approve-plan" wording verbatim. Row 6 additionally
// proves the --json rule is evaluated strictly before the --abort rows,
// exactly as the row's own annotation states.
func TestSyncPlanGuard_IncompatibilityMatrix(t *testing.T) {
	fortoken := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"row1-json-without-plan", []string{"feature", "--json"}, "--json requires --plan"},
		{"row2-plan-with-abort", []string{"feature", "--plan", "--abort"}, "--plan cannot be combined with --abort"},
		{"row3-max-per-entry-with-abort", []string{"feature", "--max-replay-per-entry", "5", "--abort"}, "--max-replay-per-entry cannot be combined with --abort"},
		{"row4-max-total-with-abort", []string{"feature", "--max-replay-total", "5", "--abort"}, "--max-replay-total cannot be combined with --abort"},
		{"row5-approve-plan-with-abort", []string{"feature", "--approve-plan", fortoken, "--abort"}, "--approve-plan cannot be combined with --abort"},
		{"row6-json-with-abort-json-checked-first", []string{"feature", "--json", "--abort"}, "--json requires --plan"},
		{"row7-max-per-entry-negative", []string{"feature", "--max-replay-per-entry", "-1"}, "--max-replay-per-entry must be zero or greater"},
		{"row8-max-total-negative", []string{"feature", "--max-replay-total", "-1"}, "--max-replay-total must be zero or greater"},
		{"row10-approve-plan-empty", []string{"feature", "--approve-plan", ""}, "--approve-plan requires a 64-character lowercase hex fingerprint"},
		{"row11-approve-plan-too-short", []string{"feature", "--approve-plan", "zz"}, "--approve-plan requires a 64-character lowercase hex fingerprint"},
		{"row12a-approve-plan-without-limits-with-plan", []string{"feature", "--plan", "--approve-plan", fortoken}, "--approve-plan requires --max-replay-per-entry or --max-replay-total"},
		{"row12b-approve-plan-without-limits-bare", []string{"feature", "--approve-plan", fortoken}, "--approve-plan requires --max-replay-per-entry or --max-replay-total"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newScopedFixture(t)
			stdout, stderr, exit := runSync(t, tc.args...)
			if exit == 0 {
				t.Fatalf("%v must refuse; stdout=%q", tc.args, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v: want %q, got stderr=%q", tc.args, tc.want, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v must print no stdout, got %q", tc.args, stdout)
			}
		})
	}
}

// TestSyncPlanGuard_NonIntegerLimitIsPflagsOwnUnchangedError proves row 9 of
// the matrix: a non-integer limit never reaches resolvePlanGuardOptions at
// all — pflag itself refuses during flag parsing, before RunE runs, with its
// own unchanged message shape.
func TestSyncPlanGuard_NonIntegerLimitIsPflagsOwnUnchangedError(t *testing.T) {
	newScopedFixture(t)
	stdout, stderr, exit := runSync(t, "feature", "--max-replay-total", "abc")
	if exit == 0 {
		t.Fatal("a non-integer limit must refuse")
	}
	if !strings.Contains(stderr, `invalid argument "abc" for "--max-replay-total" flag`) {
		t.Fatalf("expected pflag's own parse error, got stderr=%q", stderr)
	}
	if stdout != "" {
		t.Fatalf("must print no stdout, got %q", stdout)
	}
}

// TestSyncPlanGuard_ApproveTokenShapeDomain exercises planApproveTokenShape's
// exact regex domain ^[0-9a-f]{64}$ beyond the matrix's own single example:
// wrong length in both directions, uppercase hex, and non-hex characters at
// the right length all refuse with the identical message; a syntactically
// valid 64-character lowercase-hex string that simply does not correspond to
// any real plan passes THIS shape check (proving the check is purely
// syntactic) and instead refuses later as approval-mismatch, never with the
// shape message.
func TestSyncPlanGuard_ApproveTokenShapeDomain(t *testing.T) {
	shapeErr := "--approve-plan requires a 64-character lowercase hex fingerprint"
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"too-short-63", strings.Repeat("a", 63)},
		{"too-long-65", strings.Repeat("a", 65)},
		{"uppercase-hex", strings.Repeat("A", 64)},
		{"non-hex-char", strings.Repeat("a", 63) + "g"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newScopedFixture(t)
			stdout, stderr, exit := runSync(t, "feature", "--max-replay-total", "5", "--approve-plan", tc.token)
			if exit == 0 {
				t.Fatalf("token %q must refuse", tc.token)
			}
			if !strings.Contains(stderr, shapeErr) {
				t.Fatalf("token %q: want shape error, got stderr=%q", tc.token, stderr)
			}
			if stdout != "" {
				t.Fatalf("must print no stdout, got %q", stdout)
			}
		})
	}

	t.Run("shape-valid-but-fake-fails-later-as-approval-mismatch", func(t *testing.T) {
		newScopedFixture(t)
		fake := strings.Repeat("0", 64)
		stdout, stderr, exit := runSync(t, "feature", "--max-replay-total", "5", "--approve-plan", fake)
		if exit == 0 {
			t.Fatal("a fake fingerprint must still refuse")
		}
		if strings.Contains(stderr, shapeErr) {
			t.Fatalf("a syntactically valid token must not trip the shape check: %q", stderr)
		}
		if !strings.Contains(stderr, "approval-mismatch") {
			t.Fatalf("want approval-mismatch, got stderr=%q", stderr)
		}
		if stdout != "" {
			t.Fatalf("must print no stdout, got %q", stdout)
		}
	})
}

// ---------------------------------------------------------------------------
// Route naming and the plan -> fingerprint -> approve -> execute round trip
// ---------------------------------------------------------------------------

// TestSyncPlanGuard_PlanAloneDescribesLegacyRoute absorbs
// zz_smoke_plan_test.go's TestZZSmokeLegacyPlan: --plan with no trigger flag
// describes internal.RouteLegacy — "the run a bare tws sync <f> performs"
// (spec §3.4's own legal-combinations table).
func TestSyncPlanGuard_PlanAloneDescribesLegacyRoute(t *testing.T) {
	newScopedFixture(t)
	stdout, stderr, exit := runSync(t, "feature", "--plan")
	if exit != 0 {
		t.Fatalf("legacy plan must exit 0: stderr=%q", stderr)
	}
	if !strings.HasPrefix(stdout, "Plan: route="+internal.RouteLegacy+" ") {
		t.Fatalf("expected the legacy route, got %q", stdout)
	}
}

// TestSyncPlanGuard_ApprovalRoundTrip performs spec §8.6's two REQUIRED round
// trips verbatim (the legacy external route, and the explicit new-mode
// no-fetch route): a --plan --json mints a usable 64-hex fingerprint, and
// re-supplying the identical trigger/limit/push identity plus
// --approve-plan <fp> (dropping only --plan/--json, per §8.6's "cross-
// invocation flag identity") executes cleanly. Both round trips use a
// generous limit (5, well above this fixture's ~2-3 real candidates) so nothing
// is actually exceeded — the over-limit case belongs to
// TestSyncPlanGuard_GuardEvaluationLimitsAndWaiverDomains instead, since an
// approval never lets an over-limit run complete past its own JIT check
// (documented there).
func TestSyncPlanGuard_ApprovalRoundTrip(t *testing.T) {
	t.Run("legacy-external-route", func(t *testing.T) {
		newScopedFixture(t)
		planOut, _, exit := runSync(t, "feature", "--plan", "--json", "--max-replay-per-entry", "10")
		if exit != 0 {
			t.Fatalf("plan exit=%d", exit)
		}
		fp := planFieldString(t, planOut, "approval", "fingerprint")
		if len(fp) != 64 {
			t.Fatalf("expected a minted fingerprint, got %q", fp)
		}
		_, stderr, exit2 := runSync(t, "feature", "--approve-plan", fp, "--max-replay-per-entry", "10")
		if exit2 != 0 {
			t.Fatalf("approved legacy run must succeed: stderr=%q", stderr)
		}
	})

	t.Run("explicit-new-mode-no-fetch-route", func(t *testing.T) {
		newScopedFixture(t)
		planOut, _, exit := runSync(t, "feature", "--plan", "--no-fetch", "--json", "--max-replay-per-entry", "10")
		if exit != 0 {
			t.Fatalf("plan exit=%d", exit)
		}
		fp := planFieldString(t, planOut, "approval", "fingerprint")
		if len(fp) != 64 {
			t.Fatalf("expected a minted fingerprint, got %q", fp)
		}
		_, stderr, exit2 := runSync(t, "feature", "--no-fetch", "--approve-plan", fp, "--max-replay-per-entry", "10")
		if exit2 != 0 {
			t.Fatalf("approved new-mode run must succeed: stderr=%q", stderr)
		}
	})
}

// ---------------------------------------------------------------------------
// Pre-mutation limit refusal: no branch moves, marker exactly once,
// StatePreserved: false
// ---------------------------------------------------------------------------

// TestSyncPlanGuard_LimitRefusalBeforeAnyBranchMoves proves that a limit
// refused at the very first entry's own JIT check happens strictly before
// any Git mutation: every stack branch's SHA is unchanged, the guard/
// sentinel/payload files left by ClaimSyncRunGuard's own admission are
// released again by teardown (stateFilesGone), the marker line matches
// planGuardMarkerRe EXACTLY ONCE (never twice, never a differently shaped
// line), and — since nothing was preserved — the composed message does NOT
// carry "state-preserved: " (the false half of the state-preserved:
// composition pair; the true half is
// TestSyncPlanGuard_JITRevalidationMismatchAfterApprovedBaseMoves below).
// The marker assertion drives runSyncExecute (real cli.Execute()), per this
// file's own rule: syncExecute is not valid evidence for a marker's exact
// count or shape.
func TestSyncPlanGuard_LimitRefusalBeforeAnyBranchMoves(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	shasBefore := map[string]string{
		"root":   f.sha(t, "root"),
		"parent": f.sha(t, "parent"),
		"child":  f.sha(t, "child"),
	}

	stdout, stderr, exit := runSyncExecute(t, f.feature, "--no-fetch", "--max-replay-total", "0")
	if exit == 0 {
		t.Fatal("a guarded run over its limit must refuse")
	}
	if stdout != "" {
		t.Fatalf("a guarded refusal must print no document, got stdout=%q", stdout)
	}

	for name, before := range shasBefore {
		if after := f.sha(t, name); after != before {
			t.Fatalf("branch %s moved: before=%s after=%s (a limit refusal must precede every mutation)", name, before, after)
		}
	}

	matches := planGuardMarkerRe.FindAllString(stderr, -1)
	if len(matches) != 1 {
		t.Fatalf("expected exactly one plan-guard marker line, got %d: %v\nfull stderr=%q", len(matches), matches, stderr)
	}
	if !strings.Contains(matches[0], "limit-total") {
		t.Fatalf("expected a limit-total marker, got %q", matches[0])
	}
	if strings.Contains(matches[0], "state-preserved") {
		t.Fatalf("a pre-mutation refusal must NOT say state-preserved: %q", matches[0])
	}
	f.stateFilesGone(t)
}

// ---------------------------------------------------------------------------
// JIT revalidation-mismatch and StatePreserved: true
// ---------------------------------------------------------------------------

// TestSyncPlanGuard_JITRevalidationMismatchAfterApprovedBaseMoves builds the
// true JIT drift case: an approved plan over a 3-entry linear stack
// (root -> parent -> child) is executed, and a syncStepHook installed at
// (SyncStageRebasing, 0) — which fires exactly once, immediately before
// root's OWN "git rebase" but AFTER root's own guard.revalidate("root")
// already passed — commits a new file directly onto "parent" as a side
// effect. Root's rebase then completes normally (StatePreserved flips true),
// and when the loop reaches "parent", its own guard.revalidate("parent")
// re-measures parent's destination/head SHA, finds it no longer matches the
// approved plan's RevalidationDigest, and refuses with revalidation-mismatch
// — the ONLY JIT drift case distinct from the whole-plan, admission-time
// approval-mismatch (which fires on a stale/non-matching TOKEN, not on
// mid-execution drift of an entry the token never touched). Because root
// already completed, this is the true half of the state-preserved:
// composition pair — the marker's detail must carry the exact seventeen-byte
// "state-preserved: " literal (spec.md §6.4).
func TestSyncPlanGuard_JITRevalidationMismatchAfterApprovedBaseMoves(t *testing.T) {
	f := newScopedFixture(t)
	planOut, _, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch", "--max-replay-total", "10")
	if exit != 0 {
		t.Fatalf("plan exit=%d", exit)
	}
	fp := planFieldString(t, planOut, "approval", "fingerprint")
	if len(fp) != 64 {
		t.Fatalf("no fingerprint minted: %q", fp)
	}

	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		if stage == internal.SyncStageRebasing && index == 0 {
			writeAndCommit(t, f.wt("parent"), "drift.txt", "drift\n", "drift commit")
		}
		return nil
	})

	stdout, stderr, exit2 := runSyncExecute(t, f.feature, "--no-fetch", "--max-replay-total", "10", "--approve-plan", fp)
	if exit2 == 0 {
		t.Fatal("the drifted entry must refuse")
	}
	if stdout == "" || !strings.Contains(stdout, "[+] root (active)") {
		t.Fatalf("root must have completed before parent's own JIT check ran: stdout=%q", stdout)
	}

	matches := planGuardMarkerRe.FindAllString(stderr, -1)
	if len(matches) != 1 {
		t.Fatalf("expected exactly one plan-guard marker line, got %d: %v", len(matches), matches)
	}
	if !strings.Contains(matches[0], "revalidation-mismatch") {
		t.Fatalf("expected a revalidation-mismatch marker, got %q", matches[0])
	}
	if !strings.Contains(matches[0], "state-preserved: ") {
		t.Fatalf("root already completed; the marker MUST carry the state-preserved: literal, got %q", matches[0])
	}
}

// ---------------------------------------------------------------------------
// guard.evaluation[] / limits / waiver domains
// ---------------------------------------------------------------------------

// TestSyncPlanGuard_GuardEvaluationLimitsAndWaiverDomains describes, through
// two --plan --json documents over the identical over-limit fixture, the
// exact §4.6/§8.5 waiver shape: an UNAPPROVED plan carries
// refusal.kind:"limit-total", a guard.evaluation[] row with
// verdict:"exceeded", would_refuse/would_refuse_without_approval both true,
// approval.covers.waived_kinds:[] — and, notably, runnable:true regardless
// (spec §7.1's own "Forces runnable:false" column is "no" for ranks 11-12,
// unlike every rank 1-10 fact). Re-running WITH that plan's own fingerprint
// waives the row: refusal.kind becomes null, blockers/execute_blocked_by
// become empty, would_refuse flips to false while
// would_refuse_without_approval stays true, and
// approval.covers.{waived_evaluation_ids,waived_kinds} name the waived row
// (verdict becomes "waived", never deleted — limit/value/entry preserved, as
// spec §8.5 requires). This is the admission-time (EvaluatePlanGuard) shape
// only: the JIT per-entry seam (RevalidatePlanGuardEntry ->
// newlyExceededLimit) re-enforces plan.Guard.Limits' own raw numeric value
// unconditionally on every entry regardless of any waiver — verified, not
// asserted as a bug — so an approval never lets an ALREADY-exceeded limit's
// entry itself actually complete; TestSyncPlanGuard_ApprovalRoundTrip's own
// generous (non-exceeded) limit is the shape that a round trip actually
// executes.
func TestSyncPlanGuard_GuardEvaluationLimitsAndWaiverDomains(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t) // root now carries 2 local candidates against a limit of 1

	unapproved, _, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch", "--max-replay-total", "1")
	if exit != 0 {
		t.Fatalf("plan exit=%d", exit)
	}
	docU := planDoc(t, unapproved)
	if got := planFieldString(t, unapproved, "refusal", "kind"); got != "limit-total" {
		t.Fatalf("refusal.kind = %q, want limit-total", got)
	}
	if runnable, _ := docU["runnable"].(bool); !runnable {
		t.Fatal("ranks 11/12 must NOT force runnable:false, per spec §7.1's own column")
	}
	guardU, _ := docU["guard"].(map[string]any)
	evalU, _ := guardU["evaluation"].([]any)
	if len(evalU) == 0 {
		t.Fatal("expected at least one guard.evaluation[] row")
	}
	row0 := evalU[0].(map[string]any)
	if row0["id"] != "max_replay_total" || row0["verdict"] != "exceeded" {
		t.Fatalf("unapproved evaluation row = %+v, want id=max_replay_total verdict=exceeded", row0)
	}
	if guardU["would_refuse"] != true || guardU["would_refuse_without_approval"] != true {
		t.Fatalf("unapproved guard = %+v, want both would_refuse fields true", guardU)
	}
	approvalU, _ := docU["approval"].(map[string]any)
	coversU, _ := approvalU["covers"].(map[string]any)
	if waived, _ := coversU["waived_kinds"].([]any); len(waived) != 0 {
		t.Fatalf("nothing should be waived without a supplied token, got %v", waived)
	}
	fp, _ := approvalU["fingerprint"].(string)
	if len(fp) != 64 {
		t.Fatalf("expected a minted fingerprint, got %q", fp)
	}

	approved, _, exit2 := runSync(t, f.feature, "--plan", "--json", "--no-fetch", "--max-replay-total", "1", "--approve-plan", fp)
	if exit2 != 0 {
		t.Fatalf("approved plan-only invocation must still exit 0, got %d", exit2)
	}
	docA := planDoc(t, approved)
	if got := planFieldString(t, approved, "refusal", "kind"); got != "" {
		t.Fatalf("refusal.kind must be null once waived, got %q", got)
	}
	guardA, _ := docA["guard"].(map[string]any)
	if guardA["would_refuse"] != false {
		t.Fatalf("would_refuse must flip to false once waived, got %v", guardA["would_refuse"])
	}
	if guardA["would_refuse_without_approval"] != true {
		t.Fatalf("would_refuse_without_approval must stay true (the unwaived baseline fact), got %v", guardA["would_refuse_without_approval"])
	}
	limitsA, _ := guardA["limits"].(map[string]any)
	totalA, _ := limitsA["max_replay_total"].(map[string]any)
	if v, ok := totalA["value"].(float64); !ok || v != 1 {
		t.Fatalf("guard.limits.max_replay_total.value must stay the raw supplied 1, got %v", totalA["value"])
	}
	evalA, _ := guardA["evaluation"].([]any)
	if len(evalA) == 0 || evalA[0].(map[string]any)["verdict"] != "waived" {
		t.Fatalf("evaluation row must flip to waived (never deleted), got %v", evalA)
	}
	row0A := evalA[0].(map[string]any)
	if row0A["limit"] != float64(1) || row0A["value"] != float64(2) {
		t.Fatalf("a waived row must preserve its own limit/value, got %+v", row0A)
	}
	approvalA, _ := docA["approval"].(map[string]any)
	coversA, _ := approvalA["covers"].(map[string]any)
	waivedIDs, _ := coversA["waived_evaluation_ids"].([]any)
	waivedKinds, _ := coversA["waived_kinds"].([]any)
	if len(waivedIDs) != 1 || waivedIDs[0] != "max_replay_total" {
		t.Fatalf("waived_evaluation_ids = %v, want [max_replay_total]", waivedIDs)
	}
	if len(waivedKinds) != 1 || waivedKinds[0] != "limit-total" {
		t.Fatalf("waived_kinds = %v, want [limit-total]", waivedKinds)
	}

	// Verified (documented above): the JIT seam enforces the raw limit
	// unconditionally, so the SAME approval cannot carry this ALREADY-
	// exceeded run to completion — it refuses again at the first entry with
	// the identical rank/kind, StatePreserved:false (nothing rebased yet).
	_, execStderr, execExit := runSync(t, f.feature, "--no-fetch", "--max-replay-total", "1", "--approve-plan", fp)
	if execExit == 0 {
		t.Fatal("the JIT seam must still refuse an already-exceeded total even under a matching approval")
	}
	if !strings.Contains(execStderr, "limit-total") {
		t.Fatalf("expected the same limit-total kind at JIT time, got stderr=%q", execStderr)
	}
}

// ---------------------------------------------------------------------------
// Control flags never join the frozen-axis mismatch set
// ---------------------------------------------------------------------------

// TestSyncPlanGuard_ControlFlagNeverBecomesFrozenAxisMismatch proves
// syncContinueMismatches' own closed axis set — {fetch/no-fetch,
// full/local-only, only/from, push} (sync.go's syncContinueMismatches) —
// structurally excludes every plan-guard control flag: none of "plan",
// "json", "max-replay-per-entry", "max-replay-total", "approve-plan" is ever
// read from its changed map. A plain (unguarded) scoped conflict continued
// with a freshly supplied --max-replay-total must never produce the
// "cannot change %s on --continue" message that axis mismatches use, and
// must complete normally.
func TestSyncPlanGuard_ControlFlagNeverBecomesFrozenAxisMismatch(t *testing.T) {
	f := newScopedFixture(t)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child"); exit == 0 {
		t.Fatal("expected a conflict")
	}
	f.detachGuard(t)
	resolveRebase(t, f.wt("child"))

	stdout, stderr, exit := runSync(t, f.feature, "--continue", "--max-replay-total", "999")
	if strings.Contains(stderr, "cannot change") {
		t.Fatalf("a plan-guard control flag must never trip the frozen-axis mismatch: stderr=%q", stderr)
	}
	if exit != 0 {
		t.Fatalf("expected the continuation to succeed, got exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "Sync complete.") {
		t.Fatalf("expected the run to finish, got stdout=%q", stdout)
	}
}

// ---------------------------------------------------------------------------
// Shared JSON helper (planDoc itself lives in sync_plan_test.go)
// ---------------------------------------------------------------------------

// planFieldString reads a string field from a --json plan document's raw
// stdout via a dotted key path, tolerantly returning "" if any segment is
// absent, non-string, or null. Unlike planField (sync_plan_test.go), which
// treats an already-decoded doc plus a missing key as a hard test failure,
// this is for optional/nullable fields such as refusal.kind, where "" and
// "absent/null" are the same legitimate outcome for this file's assertions.
func planFieldString(t *testing.T, stdout string, path ...string) string {
	t.Helper()
	var cur any = map[string]any(planDoc(t, stdout))
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[key]
	}
	s, _ := cur.(string)
	return s
}

// ---------------------------------------------------------------------------
// The JIT total-limit accumulation regression (§10.3 row 1, §10.4).
// ---------------------------------------------------------------------------

// approvedReplayTotals reads one `--plan --json` document and returns the
// facts a max_replay_total judgement can be made from BEFORE any rebase
// runs: the number of rows, the largest single known `replay.candidate_count`
// (the anchor), the sum of every known count (which is exactly what a buggy
// approved-count accumulator would carry across the whole run) and the number
// of rows whose count is a deferred null.
func approvedReplayTotals(t *testing.T, stdout string) (rows, anchor, approvedSum, deferred int) {
	t.Helper()
	doc := planDoc(t, stdout)
	entries, _ := doc["entries"].([]any)
	for _, e := range entries {
		row := e.(map[string]any)
		replay, _ := row["replay"].(map[string]any)
		if c, ok := replay["candidate_count"].(float64); ok {
			approvedSum += int(c)
			if int(c) > anchor {
				anchor = int(c)
			}
		} else {
			deferred++
		}
	}
	return len(entries), anchor, approvedSum, deferred
}

// assertRunReplaysTotal asserts a limit-total refusal names EXACTLY the run
// total and limit given — the byte-exact §12 sentence, not a substring of the
// kind alone, so a carrier that refuses with a different accumulated total
// fails here rather than passing on the shared kind token.
func assertRunReplaysTotal(t *testing.T, stderr string, total, limit int) {
	t.Helper()
	want := fmt.Sprintf("limit-total: state-preserved: the run replays %d candidates (limit %d)", total, limit)
	if !strings.Contains(stderr, want) {
		t.Fatalf("refusal sentence mismatch.\nwant substring: %q\ngot stderr:\n%s", want, stderr)
	}
}

// TestSyncPlanGuard_TotalLimitStrictlyBetweenAnchorAndRunTotal is the
// regression for the JIT total-limit bypass: the running `max_replay_total`
// judgement must accumulate the count the seam FRESHLY resolved, never the
// approved row's own recorded value.
//
// The linear fixture's downstream rows have DEFERRED destinations — an
// earlier row of the same run rewrites the ref they rebase onto — so their
// approved `replay.candidate_count` is null (§10.4's `upstream-deferred`
// unknown). A carrier that accumulated the APPROVED value would therefore
// add zero for exactly those rows and would walk past any total limit that
// only the later rows can exceed.
//
// The fixture is exact and asserted, never sampled: three rows, whose
// freshly-resolved counts are 2 (root, the only known one) + 1 (parent) +
// 2 (child) = 5, against an approved sum of just 2. The limit is the
// anchor + 2 = 4, which is
//
//   - at or above the whole approved sum (2), so the up-front guard seam
//     cannot refuse and a buggy approved-count accumulator — carrying 2 for
//     the whole run — completes silently;
//   - strictly below the run's real total (5), so a correct JIT seam refuses
//     partway through with `limit-total` naming the total 5.
//
// Both halves are asserted, so the test is mutation-sensitive in both
// directions rather than merely "a refusal happened".
func TestSyncPlanGuard_TotalLimitStrictlyBetweenAnchorAndRunTotal(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)

	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("--plan must exit 0: exit=%d stderr=%q", exit, stderr)
	}
	rows, anchor, approvedSum, deferred := approvedReplayTotals(t, stdout)
	if rows != 3 || anchor != 2 || approvedSum != 2 || deferred != 2 {
		t.Fatalf("fixture drifted: rows=%d anchor=%d approvedSum=%d deferred=%d, want 3/2/2/2 "+
			"(root's 2 known candidates plus parent and child as upstream-deferred nulls)",
			rows, anchor, approvedSum, deferred)
	}

	const wantRunTotal = 5 // 2 (root) + 1 (parent) + 2 (child), all freshly resolved
	limit := anchor + 2
	if limit != 4 {
		t.Fatalf("limit = %d, want 4", limit)
	}
	if approvedSum > limit {
		t.Fatalf("the fixture no longer distinguishes the two carriers: an approved-count accumulator "+
			"would already refuse at limit %d with its own sum %d", limit, approvedSum)
	}
	if wantRunTotal <= limit {
		t.Fatalf("the fixture no longer refuses: run total %d is not above limit %d", wantRunTotal, limit)
	}

	_, stderr, exit = runSyncExecute(t, f.feature, "--no-fetch", "--max-replay-total", strconv.Itoa(limit))
	if exit != 1 {
		t.Fatalf("a run whose freshly-resolved total (%d) exceeds max_replay_total=%d must refuse (exit 1), got exit=%d stderr=%q\n"+
			"this is the JIT accumulation bypass: the carrier is adding the APPROVED (null => 0) count of every deferred row instead of the count the seam re-measured",
			wantRunTotal, limit, exit, stderr)
	}
	assertRunReplaysTotal(t, stderr, wantRunTotal, limit)
	assertExactlyOnePlanGuardMarker(t, stderr)

	// The matching admission: one more than the real total completes, proving
	// the refusal above was the total itself and not an unconditional stop.
	f2 := newScopedFixture(t)
	f2.advanceRoot(t)
	_, stderr, exit = runSyncExecute(t, f2.feature, "--no-fetch", "--max-replay-total", strconv.Itoa(wantRunTotal))
	if exit != 0 {
		t.Fatalf("max_replay_total=%d equals the run's real total and must admit it, got exit=%d stderr=%q",
			wantRunTotal, exit, stderr)
	}
}

// setupCheckoutJITTotalFixture builds the checkout twin of the external
// accumulation fixture: ONE repository, no worktrees, four stack entries.
//
//	main      = I - m1
//	feat-root = I  - r0 - r1        base main,      cutoff I   (2 candidates)
//	feat-a    = r0 - a0 - a1        base feat-root, cutoff r0  (2 candidates)
//	feat-b    = a0 - b0             base feat-a,    cutoff a0  (1 candidate)
//	feat-x    = I  - x0             base main,      cutoff I   (1 candidate)
//
// feat-a and feat-b hang off refs an EARLIER row of the same run rewrites, so
// both are approved as `upstream-deferred` nulls; feat-root and feat-x are
// measured up front. Every recorded cutoff is strictly behind its base's
// current tip in the approved document AND at the row's own seam, so every
// row's strategy is `onto` on both sides of the seam and no row can drift
// into a `revalidation-mismatch` that would mask the limit judgement.
//
// The run's real total is therefore 2+2+1+1 = 6 while the approved sum is
// only 2+1 = 3. HEAD is deliberately left on feat-b — never on `main` —
// because internal.DefaultBranchIn falls back to the checked-out branch when
// no origin/HEAD exists, and a fallback equal to an entry's Base would make
// ResolveSyncBase rewrite it to the nonexistent "origin/main".
func setupCheckoutJITTotalFixture(t *testing.T) (dir, featurePath string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	clearStepHook(t)
	dir = setupCheckoutSyncRepo(t)
	featurePath = setupFeaturePath(t, dir)
	forkSHA := gitSHA(t, dir, "HEAD")

	commit := func(name string) string {
		writeFileCS(t, dir, name+".txt", name+"\n")
		gitRunCS(t, dir, "add", ".")
		gitRunCS(t, dir, "commit", "-m", name)
		return gitSHA(t, dir, "HEAD")
	}

	gitRunCS(t, dir, "checkout", "-b", "feat-root")
	r0 := commit("r0")
	gitRunCS(t, dir, "checkout", "-b", "feat-a")
	a0 := commit("a0")
	gitRunCS(t, dir, "checkout", "-b", "feat-b")
	commit("b0")

	// Advance every base PAST the cutoff its child recorded, so no row is a
	// `plain` row at approval that turns into an `onto` row at its seam.
	gitRunCS(t, dir, "checkout", "feat-a")
	commit("a1")
	gitRunCS(t, dir, "checkout", "feat-root")
	commit("r1")
	gitRunCS(t, dir, "checkout", "main")
	gitRunCS(t, dir, "checkout", "-b", "feat-x")
	commit("x0")
	gitRunCS(t, dir, "checkout", "main")
	commit("m1")
	gitRunCS(t, dir, "checkout", "feat-b")

	saveTestStack(t, featurePath, []internal.StackEntry{
		{Name: "feat-root", Base: "main", LastBaseSHA: forkSHA},
		{Name: "feat-a", Base: "feat-root", LastBaseSHA: r0},
		{Name: "feat-b", Base: "feat-a", LastBaseSHA: a0},
		{Name: "feat-x", Base: "main", LastBaseSHA: forkSHA},
	})
	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)
	return dir, featurePath
}

// TestSyncPlanGuard_CheckoutTotalLimitAccumulatesFreshCounts is the checkout
// twin of the external accumulation regression above, driven through
// production cli.Execute() so it exercises internal/checkout_sync.go's own
// checkoutPlanGuardRun.revalidate seam — a completely separate carrier from
// package cli's planGuardRun, and therefore a separately mutable one.
//
// Four rows, real total 6, approved sum 3. At `--max-replay-total 5`:
//   - a correct carrier refuses with `limit-total` naming the total 6;
//   - a carrier accumulating the APPROVED counts carries 3 and completes.
//
// The admission control at 6 proves the refusal is the total, not the flag.
func TestSyncPlanGuard_CheckoutTotalLimitAccumulatesFreshCounts(t *testing.T) {
	setupCheckoutJITTotalFixture(t)

	stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("--plan must exit 0: exit=%d stderr=%q", exit, stderr)
	}
	rows, anchor, approvedSum, deferred := approvedReplayTotals(t, stdout)
	if rows != 4 || anchor != 2 || approvedSum != 3 || deferred != 2 {
		t.Fatalf("fixture drifted: rows=%d anchor=%d approvedSum=%d deferred=%d, want 4/2/3/2",
			rows, anchor, approvedSum, deferred)
	}

	const wantRunTotal = 6 // 2 (feat-root) + 2 (feat-a) + 1 (feat-b) + 1 (feat-x)
	const limit = 5
	if approvedSum > limit {
		t.Fatalf("the fixture no longer distinguishes the two carriers: an approved-count accumulator "+
			"would already refuse at limit %d with its own sum %d", limit, approvedSum)
	}

	_, stderr, exit = runSyncExecute(t, "test-feature", "--no-fetch", "--max-replay-total", strconv.Itoa(limit))
	if exit != 1 {
		t.Fatalf("the checkout carrier must refuse once its FRESHLY resolved counts total %d against limit %d, got exit=%d stderr=%q",
			wantRunTotal, limit, exit, stderr)
	}
	assertRunReplaysTotal(t, stderr, wantRunTotal, limit)
	assertExactlyOnePlanGuardMarker(t, stderr)

	setupCheckoutJITTotalFixture(t)
	_, stderr, exit = runSyncExecute(t, "test-feature", "--no-fetch", "--max-replay-total", strconv.Itoa(wantRunTotal))
	if exit != 0 {
		t.Fatalf("max_replay_total=%d equals the run's real total and must admit it, got exit=%d stderr=%q",
			wantRunTotal, exit, stderr)
	}
}

// ===========================================================================
// §22.11 — one fingerprint for the same facts, across FORMS and across
// PROCESSES.
// ===========================================================================

// criterion11RepoEnv/criterion11FeatureEnv hand a parent-built fixture to a
// re-executed child, so "across repeated processes" is a real OS-process
// claim rather than two in-process calls.
const (
	criterion11RepoEnv    = "TWS_C22_11_REPO"
	criterion11FeatureEnv = "TWS_C22_11_FEATURE"
	criterion11RootEnv    = "TWS_C22_11_ROOT"
)

// TestSyncPlanGuardCriterion11Child is the child fixture §22.11 re-executes:
// a no-op in an ordinary run (the discriminator env var is unset), and
// otherwise a single `--plan --json` over the parent's fixture whose minted
// fingerprint it prints on stdout with a fixed prefix.
func TestSyncPlanGuardCriterion11Child(t *testing.T) {
	repo := os.Getenv(criterion11RepoEnv)
	if repo == "" {
		t.Skip("child-only fixture: not selected by " + criterion11RepoEnv)
	}
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())
	// The PARENT's workspace root, not a fresh one: the child must resolve
	// the very fixture the parent built, or it would fingerprint different
	// facts and the comparison would be meaningless.
	t.Setenv("TWS_ROOT", os.Getenv(criterion11RootEnv))
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runSyncExecute(t, os.Getenv(criterion11FeatureEnv),
		"--plan", "--json", "--no-fetch", "--max-replay-total", "50")
	if exit != 0 {
		fmt.Fprintf(os.Stderr, "child --plan failed: exit=%d stderr=%q\n", exit, stderr)
		os.Exit(2)
	}
	fmt.Printf("C22_11_FINGERPRINT=%s\n", planFieldString(t, stdout, "approval", "fingerprint"))
}

// TestSyncPlanGuard_Criterion22_11_FingerprintIsStableAcrossFormsAndProcesses
// is §22.11's executable owner: `--plan` and `--plan --json` publish the
// SAME `Approval fingerprint:` / `approval.fingerprint` value for the same
// facts, and that value is byte-stable across repeated OS processes.
//
// The cross-process half re-executes this test binary twice over a fixture
// the parent built once and never touches again, so any per-process
// nondeterminism — map iteration order, address-derived state, a clock read
// — would show up as two different 64-hex values.
func TestSyncPlanGuard_Criterion22_11_FingerprintIsStableAcrossFormsAndProcesses(t *testing.T) {
	if os.Getenv(criterion11RepoEnv) != "" {
		t.Skip("running as the §22.11 child")
	}
	// runSyncExecute REWRITES os.Args (to "tws sync ..."), so the test
	// binary's own path must be captured before the first invocation or the
	// re-exec below would launch whatever `tws` PATH resolves to.
	testBinary := os.Args[0]
	f := newScopedFixture(t)
	f.advanceRoot(t)

	humanOut, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--no-fetch", "--max-replay-total", "50")
	if exit != 0 {
		t.Fatalf("human --plan must exit 0: exit=%d stderr=%q", exit, stderr)
	}
	jsonOut, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch", "--max-replay-total", "50")
	if exit != 0 {
		t.Fatalf("json --plan must exit 0: exit=%d stderr=%q", exit, stderr)
	}

	jsonFP := planFieldString(t, jsonOut, "approval", "fingerprint")
	if len(jsonFP) != 64 {
		t.Fatalf("approval.fingerprint = %q, want a minted 64-hex token", jsonFP)
	}
	wantLine := "Approval fingerprint: " + jsonFP + "\n"
	if !strings.Contains(humanOut, wantLine) {
		t.Fatalf("human --plan must print %q; got stdout:\n%s", wantLine, humanOut)
	}
	// The human form publishes exactly one fingerprint line, and it is that
	// one — a second, differing line would satisfy Contains but not §22.11.
	if n := strings.Count(humanOut, "Approval fingerprint: "); n != 1 {
		t.Fatalf("human --plan printed %d fingerprint lines, want exactly 1", n)
	}

	// Across repeated processes.
	child := func() string {
		t.Helper()
		cmd := exec.Command(testBinary, "-test.run=^TestSyncPlanGuardCriterion11Child$")
		cmd.Env = append(os.Environ(),
			criterion11RepoEnv+"="+f.repo,
			criterion11FeatureEnv+"="+f.feature,
			criterion11RootEnv+"="+internal.TwsRoot(),
			"GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("§22.11 child failed: %v\n%s", err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "C22_11_FINGERPRINT="); ok {
				return strings.TrimSpace(v)
			}
		}
		t.Fatalf("§22.11 child printed no fingerprint:\n%s", out)
		return ""
	}
	first, second := child(), child()
	if first != jsonFP || second != jsonFP {
		t.Fatalf("fingerprint drifted across processes: parent=%q child1=%q child2=%q", jsonFP, first, second)
	}
}

// ===========================================================================
// §22.22 — a limitless --plan mints nothing; adding a limit mints a usable
// token over the same facts.
// ===========================================================================

// TestSyncPlanGuard_Criterion22_22_LimitlessPlanMintsNoToken is §22.22's
// executable owner: over one fixture, the limitless `--plan --json`
// publishes `approval.fingerprint: null`, `approval.covers.requires_limits:
// true`, `approval.covers.has_work: true` and `guard.evaluation: []`; the
// SAME facts with a limit added mint a usable token.
func TestSyncPlanGuard_Criterion22_22_LimitlessPlanMintsNoToken(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)

	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("limitless --plan must exit 0: exit=%d stderr=%q", exit, stderr)
	}
	doc := planDoc(t, stdout)
	approval, _ := doc["approval"].(map[string]any)
	if approval == nil {
		t.Fatalf("approval = %#v, want an object", doc["approval"])
	}
	if approval["fingerprint"] != nil {
		t.Fatalf("approval.fingerprint = %v, want null on a limitless plan", approval["fingerprint"])
	}
	if approval["usable"] != false {
		t.Fatalf("approval.usable = %v, want false", approval["usable"])
	}
	covers, _ := approval["covers"].(map[string]any)
	if covers == nil {
		t.Fatalf("approval.covers = %#v, want an object", approval["covers"])
	}
	if covers["requires_limits"] != true {
		t.Fatalf("approval.covers.requires_limits = %v, want true", covers["requires_limits"])
	}
	if covers["has_work"] != true {
		t.Fatalf("approval.covers.has_work = %v, want true (this fixture really has rebase rows)", covers["has_work"])
	}
	guard, _ := doc["guard"].(map[string]any)
	if guard == nil {
		t.Fatalf("guard = %#v, want an object", doc["guard"])
	}
	evaluation, ok := guard["evaluation"].([]any)
	if !ok || len(evaluation) != 0 {
		t.Fatalf("guard.evaluation = %#v, want [] with no effective limit", guard["evaluation"])
	}

	// The same facts, with a limit: a usable token over a non-empty evaluation.
	limited, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch", "--max-replay-total", "50")
	if exit != 0 {
		t.Fatalf("limited --plan must exit 0: exit=%d stderr=%q", exit, stderr)
	}
	ldoc := planDoc(t, limited)
	lapproval, _ := ldoc["approval"].(map[string]any)
	fp, _ := lapproval["fingerprint"].(string)
	if len(fp) != 64 {
		t.Fatalf("approval.fingerprint = %v, want a 64-hex token once a limit is in force", lapproval["fingerprint"])
	}
	if lapproval["usable"] != true {
		t.Fatalf("approval.usable = %v, want true", lapproval["usable"])
	}
	lguard, _ := ldoc["guard"].(map[string]any)
	if rows, _ := lguard["evaluation"].([]any); len(rows) == 0 {
		t.Fatalf("guard.evaluation = %#v, want at least the aggregate row once a limit is in force", lguard["evaluation"])
	}
	// The facts really are the same: the two documents' entries agree.
	if fmt.Sprint(doc["entries"]) != fmt.Sprint(ldoc["entries"]) {
		t.Fatal("the two documents do not describe the same facts; §22.22 compares one fact set under two limit policies")
	}
}

// ===========================================================================
// §22.13m — `owner-artefact-undecodable` is ONE fact with TWO answers,
// asserted as a matched pair in BOTH modes (§12.5, §4.6, §7.1, §7.3).
// ===========================================================================

// symlinkOwnerArtefact replaces path with a symlink to a freshly written
// target that DECODES, which is the reduced domain's real member: the native
// ladder reads straight through the link, and only this plan's own
// Lstat-based probe distinguishes it.
func symlinkOwnerArtefact(t *testing.T, path, body string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "target.yaml")
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("this fixture needs symlink support: %v", err)
	}
	return target
}

// TestSyncPlanGuard_Criterion22_13m_OwnerArtefactUndecodableMatchedPair is
// §22.13m's executable owner: two fixtures — a symlinked `.sync-run.lock`
// under a legacy EXTERNAL route and a symlinked `.checkout-sync.lock` under a
// CHECKOUT route — each driven through TWO invocations differing only in
// `--plan`, asserting clauses (i)-(vi).
func TestSyncPlanGuard_Criterion22_13m_OwnerArtefactUndecodableMatchedPair(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		guardPath := internal.SyncRunGuardPath(f.featurePath)
		target := symlinkOwnerArtefact(t, guardPath,
			fmt.Sprintf("pid: %d\ncreated: \"2020-01-01T00:00:00Z\"\ntoken: \"t\"\nstate_version: 2\n", spawnDeadPID(t)))
		targetBefore, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}

		// (i) the plan invocation exits 0 with a complete document.
		stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch", "--max-replay-total", "50")
		if exit != 0 {
			t.Fatalf("the plan invocation must exit 0: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		state := doc["state"].(map[string]any)
		files := state["files"].(map[string]any)
		runGuard := files["external_run_guard"].(map[string]any)
		if runGuard["presence"] != "symlink" {
			t.Fatalf("external_run_guard.presence = %v, want symlink", runGuard["presence"])
		}
		guard := doc["guard"].(map[string]any)
		tokens, _ := guard["execute_blocked_by"].([]any)
		if !slices.ContainsFunc(tokens, func(v any) bool { return v == "owner-artefact-undecodable" }) {
			t.Fatalf("guard.execute_blocked_by = %v, want owner-artefact-undecodable", tokens)
		}
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			detail, _ := b["detail"].(string)
			if strings.Contains(detail, "symlink") || strings.Contains(detail, "run guard") {
				t.Fatalf("the token publishes NO blockers[] row of any rank for that fact, got %v", b)
			}
		}
		if doc["refusal"].(map[string]any)["kind"] != nil {
			t.Fatalf("refusal.kind = %v, want null", doc["refusal"])
		}

		// (ii) runnable is TRUE, positively.
		if doc["runnable"] != true {
			t.Fatalf("runnable = %v, want true: the token never causes a verdict", doc["runnable"])
		}

		// (iii) the token is minted, printed and non-admitting.
		approval := doc["approval"].(map[string]any)
		fp, _ := approval["fingerprint"].(string)
		if len(fp) != 64 || approval["usable"] != true {
			t.Fatalf("approval = %v, want a minted, usable token", approval)
		}
		human, _, exit := runSyncExecute(t, f.feature, "--plan", "--no-fetch", "--max-replay-total", "50")
		if exit != 0 {
			t.Fatalf("the human plan must exit 0")
		}
		lines := strings.Split(strings.TrimRight(human, "\n"), "\n")
		if last := lines[len(lines)-1]; !strings.HasPrefix(last, "Approval fingerprint: ") {
			t.Fatalf("the human tail's last line = %q, want the Approval fingerprint line", last)
		}
		if !strings.Contains(human, "\n  - owner-artefact-undecodable\n") {
			t.Fatalf("the Guarded execution blockers: block must carry the bare token line:\n%s", human)
		}

		// (iv) the guarded executing twin refuses at the guard seam, and the
		// token cannot waive it.
		_, stderr, exit = runSyncExecute(t, f.feature, "--no-fetch", "--max-replay-total", "50", "--approve-plan", fp)
		if exit != 1 {
			t.Fatalf("the guarded twin must exit 1: exit=%d stderr=%q", exit, stderr)
		}
		markers := planGuardMarkerRe.FindAllString(stderr, -1)
		if len(markers) != 1 || !strings.HasPrefix(markers[0], "plan-guard: state-refused: ") {
			t.Fatalf("stderr markers = %v, want exactly one ^plan-guard: state-refused: line\n%s", markers, stderr)
		}
		if internal.HasSyncRunState(f.featurePath) {
			t.Fatal("the refusing guarded twin must create no payload")
		}
		afterTarget, err := os.ReadFile(target)
		if err != nil || string(afterTarget) != string(targetBefore) {
			t.Fatalf("the artefact must be byte-identical afterwards (err=%v)", err)
		}

		// (vi) applicability: the checkout artefact is not-applicable here.
		checkoutLock := files["checkout_lock"].(map[string]any)
		if checkoutLock["presence"] != "not-applicable" {
			t.Fatalf("checkout_lock.presence = %v, want not-applicable on an external document", checkoutLock["presence"])
		}
	})

	t.Run("checkout", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		lockPath := internal.CheckoutLockPath(fp)
		symlinkOwnerArtefact(t, lockPath,
			fmt.Sprintf("pid: %d\ncreated: \"2020-01-01T00:00:00Z\"\n", spawnDeadPID(t)))

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch", "--max-replay-total", "50")
		if exit != 0 {
			t.Fatalf("the plan invocation must exit 0: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		files := doc["state"].(map[string]any)["files"].(map[string]any)
		if files["checkout_lock"].(map[string]any)["presence"] != "symlink" {
			t.Fatalf("checkout_lock.presence = %v, want symlink", files["checkout_lock"])
		}
		guard := doc["guard"].(map[string]any)
		tokens, _ := guard["execute_blocked_by"].([]any)
		if !slices.ContainsFunc(tokens, func(v any) bool { return v == "owner-artefact-undecodable" }) {
			t.Fatalf("guard.execute_blocked_by = %v, want owner-artefact-undecodable", tokens)
		}
		if doc["runnable"] != true {
			t.Fatalf("runnable = %v, want true", doc["runnable"])
		}
		// (vi) the three EXTERNAL artefacts are not-applicable here.
		for _, key := range []string{"external_run_guard", "external_legacy_state", "external_run_payload"} {
			row, ok := files[key].(map[string]any)
			if !ok {
				t.Fatalf("state.files.%s missing", key)
			}
			if row["presence"] != "not-applicable" {
				t.Fatalf("state.files.%s.presence = %v, want not-applicable on a checkout document", key, row["presence"])
			}
		}

		// (iv) the guarded executing twin refuses at the guard seam.
		_, stderr, exit = runSyncExecute(t, "test-feature", "--no-fetch", "--max-replay-total", "50")
		if exit != 1 {
			t.Fatalf("the guarded twin must exit 1: exit=%d stderr=%q", exit, stderr)
		}
		markers := planGuardMarkerRe.FindAllString(stderr, -1)
		if len(markers) != 1 || !strings.HasPrefix(markers[0], "plan-guard: state-refused: ") {
			t.Fatalf("stderr markers = %v, want exactly one ^plan-guard: state-refused: line\n%s", markers, stderr)
		}
		if internal.HasCheckoutTransaction(fp) {
			t.Fatal("the refusing guarded twin must create no transaction")
		}
	})

	// (vi, second half) an UNREADABLE REGULAR artefact takes the OTHER row of
	// the §12.5 table in both modes: the shipped native rank 3 refusal, not
	// the controlled token.
	t.Run("unreadable_regular_artefact_takes_the_other_row", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		if err := os.MkdirAll(filepath.Dir(internal.CheckoutLockPath(fp)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(internal.CheckoutLockPath(fp), []byte("not yaml: ["), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		files := doc["state"].(map[string]any)["files"].(map[string]any)
		if files["checkout_lock"].(map[string]any)["presence"] != "unreadable" {
			t.Fatalf("checkout_lock.presence = %v, want unreadable", files["checkout_lock"])
		}
		found := false
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			if detail, _ := b["detail"].(string); strings.HasPrefix(detail, "invalid checkout-sync lock") {
				found = true
				if b["kind"] != "state-refused" {
					t.Fatalf("the native row is rank 3 state-refused, got %v", b["kind"])
				}
			}
		}
		if !found {
			t.Fatalf("an unreadable REGULAR lock takes the shipped native rank 3 row, got %v", doc["blockers"])
		}
		if doc["runnable"] != false {
			t.Fatalf("runnable = %v, want false on the native-refusal row", doc["runnable"])
		}
	})
}

// TestSyncPlanGuard_Criterion22_17a_StatePreservedPrefixIsSeventeenBytes is
// §22.17a's NUMERIC assertion: the marker prefix §6.4 fixes is exactly the
// seventeen bytes `state-preserved: `, with exactly one trailing space, and
// it is byte-for-byte ABSENT — length zero — when StatePreserved is false.
// A prose-only "contains the literal" assertion cannot catch a prefix that
// gained or lost a space, so every claim here is a measured length or an
// index, never a substring match alone.
func TestSyncPlanGuard_Criterion22_17a_StatePreservedPrefixIsSeventeenBytes(t *testing.T) {
	const prefix = "state-preserved: "
	if len(prefix) != 17 {
		t.Fatalf("len(%q) = %d, want exactly 17 bytes", prefix, len(prefix))
	}
	if got := strings.Count(prefix, " "); got != 1 {
		t.Fatalf("the prefix carries %d spaces, want exactly one (its single trailing separator)", got)
	}
	if prefix[len(prefix)-1] != ' ' {
		t.Fatalf("the prefix's last byte is %q, want a single trailing space", prefix[len(prefix)-1])
	}
	if strings.TrimRight(prefix, " ") != "state-preserved:" {
		t.Fatalf("the prefix's non-space head = %q", strings.TrimRight(prefix, " "))
	}

	kind, detail := "limit-total", "the run replays 11 candidates (limit 10)"

	preserved := (&internal.PlanGuardRefusalError{Kind: kind, Detail: detail, StatePreserved: true}).Error()
	plain := (&internal.PlanGuardRefusalError{Kind: kind, Detail: detail, StatePreserved: false}).Error()

	// The true half: the prefix sits at exactly one position, immediately
	// after "<kind>: ", and the two renderings differ by exactly its length.
	head := kind + ": "
	if !strings.HasPrefix(preserved, head+prefix) {
		t.Fatalf("preserved = %q, want it to open with %q", preserved, head+prefix)
	}
	if got := strings.Count(preserved, prefix); got != 1 {
		t.Fatalf("the prefix appears %d times, want exactly once", got)
	}
	if got := len(preserved) - len(plain); got != len(prefix) {
		t.Fatalf("the two renderings differ by %d bytes, want exactly len(%q) = %d", got, prefix, len(prefix))
	}
	if got := strings.Index(preserved, prefix); got != len(head) {
		t.Fatalf("the prefix starts at byte %d, want %d (immediately after %q)", got, len(head), head)
	}
	if suffix := preserved[len(head)+len(prefix):]; suffix != detail {
		t.Fatalf("the bytes after the prefix = %q, want the detail verbatim %q", suffix, detail)
	}

	// The false half: length zero, not merely "no substring".
	if plain != head+detail {
		t.Fatalf("plain = %q, want exactly %q", plain, head+detail)
	}
	if got := len(plain) - len(head+detail); got != 0 {
		t.Fatalf("an unpreserved refusal carries %d extra bytes, want 0", got)
	}
	if got := strings.Count(plain, "state-preserved"); got != 0 {
		t.Fatalf("an unpreserved refusal mentions the marker %d times, want 0", got)
	}
	// A detail that itself contains the words must not be miscounted as the
	// prefix: the marker is a POSITION, not a substring.
	odd := (&internal.PlanGuardRefusalError{Kind: kind, Detail: "state-preserved: not really", StatePreserved: false}).Error()
	if strings.Index(odd, prefix) != len(head) {
		t.Fatalf("odd = %q: the detail's own words start at %d", odd, strings.Index(odd, prefix))
	}
	if len(odd) != len(head)+len("state-preserved: not really") {
		t.Fatalf("odd = %q carries an extra prefix it was never given", odd)
	}
}

// TestSyncPlanGuard_Criterion22_17a_WorktreePathParity is §9.0's layout
// parity requirement: the planner's own internal.RebasePlanLayout and the
// external route's externalSyncLayout must answer the SAME path for the same
// (worktrees root, name) pair — otherwise a document's projected worktree
// path and the path the executor actually operates in could diverge, and
// every path-carrying sentence in the document would name a directory the
// run never touches.
//
// Both are driven over the same fixture-name table, including the empty
// name, nested names, dot/dot-dot and unicode names as ordinary STRING
// inputs (the layouts are pure path joiners; neither validates a name, and
// this test asserts exactly that they agree, not that either sanitizes).
// The one documented asymmetry — an EMPTY worktrees root, where the planner
// answers "" because a document must not invent a path it never measured —
// is asserted as its own cell.
func TestSyncPlanGuard_Criterion22_17a_WorktreePathParity(t *testing.T) {
	roots := []string{
		"/tmp/ws/feature/worktrees",
		"relative/worktrees",
		"/ws with spaces/worktrees",
	}
	names := []string{
		"",
		"root",
		"nested/child",
		".",
		"..",
		"../escape",
		"./here",
		"ünïcodé",
		"名前",
		"name.with.dots",
		"trailing/",
	}

	for _, root := range roots {
		for _, name := range names {
			planner := internal.RebasePlanLayout{WorktreesRoot: root}.WorktreePath(name)
			external := externalSyncLayout{WorktreesRoot: root}.WorktreePath(name)
			if planner != external {
				t.Fatalf("root=%q name=%q: planner=%q external=%q; §9.0 requires ONE path answer", root, name, planner, external)
			}
			if planner != filepath.Join(root, name) {
				t.Fatalf("root=%q name=%q: %q is not filepath.Join(root, name)", root, name, planner)
			}
		}
	}

	// The documented asymmetry, asserted rather than assumed.
	if got := (internal.RebasePlanLayout{}).WorktreePath("root"); got != "" {
		t.Fatalf("an unmeasured worktrees root must yield %q, got %q", "", got)
	}

	// And no planner SIGNATURE may name the CLI's own layout type: the parity
	// above is a shared contract between two independent types, never a
	// shared signature (which would make internal depend on cli and could not
	// even compile). Comments may — and do — name it, because documenting the
	// parity is exactly how the two stay aligned; only executable code is
	// asserted here.
	planners := []string{
		"rebase_plan.go", "rebase_plan_build.go", "rebase_plan_guard.go",
		"rebase_planner.go", "rebase_plan_state.go", "rebase_plan_probe.go",
		"rebase_plan_fingerprint.go", "rebase_plan_render.go",
	}
	for _, name := range planners {
		src, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if strings.Contains(code, "externalSyncLayout") {
				t.Fatalf("internal/%s:%d names externalSyncLayout in CODE: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
