package internal

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2): the execution and recovery cells this package owns.
//
//	T-013 SHA-256 repository, oid_width 64 end to end ............. AC-016
//	T-028 holder held elsewhere, prunable holder, holder drift ..... AC-038, §9.9
//	T-043 descendant destination read from pin and record .......... AC-057
//	T-044 single CAS transaction: shape, order, expected old values,
//	      captured output, no-op verify rows, all-no-op transaction .. AC-058
//	T-046 CAS race under the reftable backend (skipped if absent) ... AC-059
//	T-048 pin-before-validate ordering and GC survival ............. AC-061
//	T-050 validation pass, failure, and dirty-residue refusal ...... AC-063
//	T-051 untracked: preflight warning, JIT refusal, no force ...... AC-064
//	T-052 conflict pause and the non-conflict native Git failure ... AC-065
//	T-053 conflict pause then --abort ............................. AC-076
//	T-055 post-lock re-snapshot races ............................. AC-067
//	T-057 metadata exactness, entries outside the closure .......... AC-069, AC-073
//	T-058 post-image persisted before the CAS, written once ........ AC-070
//	T-059 before-hash check on every forward write ................. AC-071
//	T-064 operator branch move before and after the commit point ... AC-076
//	T-083 holder-restore-deferred: warning, persisted, re-attempted  AC-096
//
// It also carries two supporting groups that no cell owns alone: the
// forward-retry holder invariant (§9.9, §9.11) and the per-row repository
// context of §6.1 steps 4-5 (AC-034), whose cross-repo closure CELL is owned
// by reparent_plan_build_test.go.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// A real workspace fixture: one repository, a three-entry stack, and the
// feature layout of the requested mode. Every assertion below runs against
// real Git, real worktrees and real artifacts — there is no Git shim here,
// because the behaviours under test (CAS atomicity, worktree detachment,
// pin survival) only exist in Git itself.
// ---------------------------------------------------------------------------

type reparentWorkspace struct {
	t    *testing.T
	Repo *reparentRepo
	Loc  ReparentLocation
	WS   Workspace
}

// newReparentWorkspace builds main -> pr1 -> pr2 -> pr3 with one commit per
// entry, plus the stack.yaml that records each edge's cutoff.
func newReparentWorkspace(t *testing.T, mode WorkspaceMode) *reparentWorkspace {
	t.Helper()
	repo := newReparentPrimitiveRepo(t)

	mainTip := repo.RevParse("main")
	repo.Git("switch", "-q", "-c", "pr1")
	pr1Tip := repo.Commit("pr1.txt", "pr1")
	repo.Git("switch", "-q", "-c", "pr2")
	repo.Commit("pr2.txt", "pr2")
	pr2Tip := repo.RevParse("HEAD")
	repo.Git("switch", "-q", "-c", "pr3")
	repo.Commit("pr3.txt", "pr3")
	repo.Git("switch", "-q", "main")

	var metadataRoot string
	if mode == ModeCheckout {
		metadataRoot = filepath.Join(repo.Dir, ".tws")
	} else {
		metadataRoot = canonicalize(t.TempDir())
	}
	featurePath := filepath.Join(metadataRoot, "features", "customer")
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}

	stack := Stack{Branches: []StackEntry{
		{Name: "pr1", Base: "refs/heads/main", LastBaseSHA: mainTip},
		{Name: "pr2", Base: "pr1", LastBaseSHA: pr1Tip},
		{Name: "pr3", Base: "pr2", LastBaseSHA: pr2Tip},
	}}
	data, err := yaml.Marshal(&stack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StackPath(featurePath), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ws := Workspace{RepoRoot: repo.Dir, Mode: mode, MetadataRoot: metadataRoot, StableID: "fixture"}
	return &reparentWorkspace{
		t:    t,
		Repo: repo,
		Loc:  ReparentLocationFor(ws, "customer", featurePath),
		WS:   ws,
	}
}

// planInput builds the route input for "reparent pr2 onto main".
func (w *reparentWorkspace) planInput() ReparentPlanInput {
	return ReparentPlanInput{
		Loc:        w.Loc,
		RepoRoot:   w.Repo.Dir,
		Route:      ReparentRouteFresh,
		Invocation: ReparentInvocationPlanOnly,
		Target:     "pr2",
		OntoToken:  "main",
		OntoKind:   "ref",
		Workspace: PlanWorkspace{
			Mode: string(w.Loc.Mode), StableID: reparentPlanFixtureString(w.WS.StableID), RepoRoot: w.Repo.Dir,
		},
		FetchPolicy:         SyncFetchDisabled,
		FetchDefaultApplied: true,
	}
}

// approvedInput plans once, takes the minted fingerprint and returns the
// execution input a fresh run is admitted with (§8.2: limits plus a token).
func (w *reparentWorkspace) approvedInput() (ReparentPlanInput, ReparentPlan) {
	w.t.Helper()
	limit := 50
	in := w.planInput()
	in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit, Present: map[string]bool{"max-replay-per-entry": true}}
	plan, _, err := PlanReparent(in)
	if err != nil {
		w.t.Fatalf("plan: %v", err)
	}
	if plan.Approval.Fingerprint == nil {
		w.t.Fatalf("plan minted no usable token: runnable=%v blockers=%+v", plan.Runnable, plan.Blockers)
	}
	in.Invocation = ReparentInvocationExecute
	in.Guard.Approve = *plan.Approval.Fingerprint
	return in, plan
}

func (w *reparentWorkspace) begin(in ReparentPlanInput) *ReparentRun {
	w.t.Helper()
	plan, req, err := PlanReparent(in)
	if err != nil {
		w.t.Fatalf("re-plan: %v", err)
	}
	run, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req})
	if err != nil {
		w.t.Fatalf("begin: %v", err)
	}
	return run
}

func (w *reparentWorkspace) loadStack() Stack {
	w.t.Helper()
	data, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
	if err != nil {
		w.t.Fatal(err)
	}
	var stack Stack
	if err := yaml.Unmarshal(data, &stack); err != nil {
		w.t.Fatal(err)
	}
	return stack
}

func (w *reparentWorkspace) entry(name string) StackEntry {
	w.t.Helper()
	for _, e := range w.loadStack().Branches {
		if e.Name == name {
			return e
		}
	}
	w.t.Fatalf("entry %q vanished from stack.yaml", name)
	return StackEntry{}
}

// ---------------------------------------------------------------------------
// PlanReparent — the read-only route.
// ---------------------------------------------------------------------------

// The plan route's own facts: deferred rows, the token-minting rule, an unknown target and the collateral disclosure (AC-041, AC-046, AC-049).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestPlanReparentRoute(t *testing.T) {
	// --- TestPlanReparentBuildsADeferredRunnablePlan ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		log := reparentCaptureArgv(t)

		limit := 50
		in := w.planInput()
		in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
		plan, req, err := PlanReparent(in)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}

		if !plan.Runnable || len(plan.Blockers) != 0 {
			t.Fatalf("plan not runnable: %+v", plan.Blockers)
		}
		if plan.Target.Name != "pr2" || len(plan.Descendants) != 1 || plan.Descendants[0].Name != "pr3" {
			t.Fatalf("closure = %s + %+v", plan.Target.Name, plan.Descendants)
		}
		if plan.Target.DestinationBinding != ReparentBindingPinned || plan.Target.DestinationSHA == nil {
			t.Fatalf("target destination = %+v", plan.Target)
		}
		if *plan.Target.NewParent.StoredToken != "refs/heads/main" {
			t.Fatalf("stored token = %q, want the canonical full ref", *plan.Target.NewParent.StoredToken)
		}

		// §7.5a — every deferred cell is an explicit absence, never a guess.
		d := plan.Descendants[0]
		if d.DestinationBinding != ReparentBindingParentComputed {
			t.Fatalf("descendant binding = %q", d.DestinationBinding)
		}
		if d.DestinationSHA != nil || d.NewParent.SHA != nil {
			t.Fatal("a parent-computed row's destination cannot exist yet")
		}
		if plan.MetadataDelta.StackSHA256AfterExpected != nil || plan.MetadataDelta.PostImageKnown {
			t.Fatal("the post-image is not knowable while any row is parent-computed")
		}
		if plan.Strategy.RunID != nil || plan.Strategy.ComputationPath != nil {
			t.Fatal("a plan mints no run id and publishes no per-run path")
		}
		if plan.Summary.DeferredRows != 1 {
			t.Fatalf("deferred rows = %d", plan.Summary.DeferredRows)
		}
		if plan.Summary.Plannability != ReparentPlannabilityRows || !plan.Summary.HasWork {
			t.Fatalf("summary = %+v", plan.Summary)
		}
		if len(plan.Remote.Rows) != 2 {
			t.Fatalf("remote rows = %+v", plan.Remote.Rows)
		}
		targetRemote, descendantRemote := plan.Remote.Rows[0], plan.Remote.Rows[1]
		if targetRemote.PRBaseBefore == nil || *targetRemote.PRBaseBefore != "pr1" ||
			targetRemote.PRBaseAfter == nil || *targetRemote.PRBaseAfter != "main" {
			t.Fatalf("target remote bases = %+v", targetRemote)
		}
		if descendantRemote.PRBaseBefore == nil || *descendantRemote.PRBaseBefore != "pr2" ||
			descendantRemote.PRBaseAfter == nil || *descendantRemote.PRBaseAfter != "pr2" {
			t.Fatalf("descendant remote bases = %+v", descendantRemote)
		}
		wantGuidance := []string{
			ReparentGuidanceNoRemoteChange,
			"pr2: PR base pr1 -> main",
			ReparentGuidanceOrder,
			ReparentGuidanceForcePrefix + "pr2",
		}
		if strings.Join(plan.Remote.Guidance, "\n") != strings.Join(wantGuidance, "\n") {
			t.Fatalf("multi-row remote guidance golden:\n%s", strings.Join(plan.Remote.Guidance, "\n"))
		}

		// §7.5b — the argv template is run-id-free and path-free, and the
		// descendant's --onto operand is the literal placeholder.
		if strings.Join(plan.Target.Argv, " ") != strings.Join(reparentRowArgv(*plan.Target.DestinationSHA, derefString(plan.Target.Cutoff.ResolvedSHA)), " ") {
			t.Fatalf("target argv = %v", plan.Target.Argv)
		}
		if !containsString(d.Argv, "<computed-tip:pr2>") {
			t.Fatalf("descendant argv = %v", d.Argv)
		}
		for _, a := range append(append([]string{}, plan.Target.Argv...), d.Argv...) {
			if a == "-C" || strings.Contains(a, w.Repo.Dir) {
				t.Fatalf("argv template leaked a path or -C: %v", d.Argv)
			}
		}

		// §7.12 — a fresh plan with limits mints a usable token.
		if !plan.Approval.Usable || plan.Approval.Fingerprint == nil {
			t.Fatalf("approval = %+v", plan.Approval)
		}
		if !ReparentPlanAdmissible(plan) {
			t.Fatal("a clean plan with a usable token must be admissible")
		}

		// The builder is pure: the same request produces the same fingerprint.
		again, err := BuildReparentPlan(req)
		if err != nil {
			t.Fatal(err)
		}
		first, _ := ReparentPlanFingerprint(plan)
		second, _ := ReparentPlanFingerprint(again)
		if first != second {
			t.Fatal("BuildReparentPlan is not a pure function of its request")
		}

		reparentAssertNoDashC(t, *log)
		assertNoForbiddenReparentVerbs(t, *log)
	}(t)

	// --- TestPlanReparentWithoutLimitsMintsNoToken ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		plan, _, err := PlanReparent(w.planInput())
		if err != nil {
			t.Fatal(err)
		}
		if plan.Approval.Fingerprint != nil || plan.Approval.Usable {
			t.Fatalf("a plan without limits is a readable preview, not a mintable approval: %+v", plan.Approval)
		}
		if plan.Policy.LimitsSupplied || plan.Policy.LimitsOrigin != ReparentLimitsOriginNone {
			t.Fatalf("policy = %+v", plan.Policy)
		}
		if ReparentPlanAdmissible(plan) {
			t.Fatal("a plan with no usable token is not admissible")
		}
	}(t)

	// --- TestPlanReparentRefusesUnknownTarget ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in := w.planInput()
		in.Target = "nope"
		plan, _, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Refusal.Kind == nil || *plan.Refusal.Kind != ReparentRefusalTargetUnknown {
			t.Fatalf("refusal = %+v", plan.Refusal)
		}
		if plan.Runnable {
			t.Fatal("a refused plan is never runnable")
		}
	}(t)

	// --- TestPlanReparentDisclosesCollateralRefs ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		// A tag inside pr2's replay range: the replay rewrites the commit it
		// names, and this feature never moves it.
		w.Repo.Git("tag", "marker", "pr2")

		limit := 50
		in := w.planInput()
		in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
		plan, _, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, ref := range plan.Target.CollateralRefs {
			if ref.Ref == "refs/tags/marker" {
				found = true
			}
		}
		if !found {
			t.Fatalf("collateral refs = %+v", plan.Target.CollateralRefs)
		}
		if !hasReparentWarning(plan, ReparentWarnCollateralRefInReplayRange) {
			t.Fatalf("warnings = %+v", plan.Warnings)
		}
		if plan.Summary.CollateralBound == nil || *plan.Summary.CollateralBound != "upper" {
			t.Fatalf("collateral bound = %v", plan.Summary.CollateralBound)
		}

		// Executing leaves the tag exactly where it was: collateral refs are
		// never part of the transaction.
		before := w.Repo.RevParse("refs/tags/marker")
		in.Invocation = ReparentInvocationExecute
		in.Guard.Approve = *plan.Approval.Fingerprint
		run := w.begin(in)
		if err := RunReparent(run); err != nil {
			t.Fatalf("run: %v", err)
		}
		if after := w.Repo.RevParse("refs/tags/marker"); after != before {
			t.Fatalf("a collateral ref moved: %s -> %s", before, after)
		}
	}(t)
}

// ---------------------------------------------------------------------------
// §11.4 — BeginReparentRun's order, and crash window 1.
// ---------------------------------------------------------------------------

// §11.4's begin-run order, a drifted decision, and crash window 1 (AC-067, AC-074, AC-080).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestBeginReparentRunOrderAndRefusals(t *testing.T) {
	// --- TestBeginReparentRunOrder ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()

		var seen []string
		ReparentStepHook = func(stage ReparentStage, step string) error {
			seen = append(seen, string(stage)+":"+step)
			return nil
		}
		t.Cleanup(func() { ReparentStepHook = nil })

		run := w.begin(in)

		want := []string{
			"initializing:post-lock-resnapshot",
			"initializing:post-lock-plan-approved",
			"initializing:artifact-written",
			"initializing:compat-written",
		}
		if len(seen) != len(want) {
			t.Fatalf("begin steps = %v", seen)
		}
		for i := range want {
			if seen[i] != want[i] {
				t.Fatalf("begin step %d = %q, want %q", i, seen[i], want[i])
			}
		}

		if run.State.Stage != ReparentStagePreflight {
			t.Fatalf("stage after begin = %q, want preflight", run.State.Stage)
		}
		if !reparentRunIDShape(run.State.RunID) {
			t.Fatalf("run id = %q", run.State.RunID)
		}
		if len(run.State.Rows) != 2 {
			t.Fatalf("state rows = %+v", run.State.Rows)
		}
		if run.State.Rows[0].PreimageSHA == "" || run.State.Rows[0].OldPinRef == "" || run.State.Rows[0].NewPinRef == "" {
			t.Fatalf("row capture incomplete: %+v", run.State.Rows[0])
		}
		if run.State.StackBeforeBase64 == "" || run.State.StackSHA256Before == "" {
			t.Fatal("the exact stack.yaml bytes and their hash must be captured")
		}
		if run.State.MaxReplayPerEntry == nil || run.State.ApprovedFingerprint == "" {
			t.Fatal("the frozen limits and the approved fingerprint must be persisted")
		}
		if run.State.ScratchPath == "" || !strings.Contains(run.State.ScratchPath, ".reparent") {
			t.Fatalf("scratch path = %q", run.State.ScratchPath)
		}
		// The scratch sits BESIDE worktrees/, never inside it.
		if strings.Contains(run.State.ScratchPath, filepath.Join("worktrees", "")) {
			t.Fatalf("scratch must not live under worktrees/: %s", run.State.ScratchPath)
		}
		if !ClassifyReparentCompatArtifacts(w.Loc, run.State.RunID).Complete {
			t.Fatal("the compatibility envelope must exist after begin")
		}
	}(t)

	// The assembled transaction is validated before its first authoritative
	// write. A cross-field mutation therefore releases the just-acquired lock
	// and leaves no state, compatibility, remote, scratch, pin, ref, or stack
	// side effect.
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		plan, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		pre, err := captureReparentPreImage(in, req)
		if err != nil {
			t.Fatal(err)
		}
		st := newReparentState(
			in, plan, pre,
			strings.Repeat("a", 32), strings.Repeat("b", 32),
		)
		target := st.Row(st.TargetName)
		if target == nil {
			t.Fatal("assembled state has no target row")
		}
		target.BaseAfter = "refs/heads/forged"
		refsBefore := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/")
		stackBefore, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateReparentStateSemantics(w.Loc, st); err == nil {
			t.Fatal("invalid assembled state passed strict validation")
		}
		for _, path := range []string{
			ReparentStatePath(w.Loc),
			SyncRunStatePath(w.Loc.FeaturePath),
			SyncStatePath(w.Loc.FeaturePath),
			SyncRunGuardPath(w.Loc.FeaturePath),
			ReparentRemoteRecordPath(w.Loc),
		} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid assembled state left %s: %v", path, err)
			}
		}
		if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/"); got != refsBefore {
			t.Fatal("invalid assembled state moved a ref")
		}
		stackAfter, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil || !bytes.Equal(stackAfter, stackBefore) {
			t.Fatalf("invalid assembled state changed stack.yaml: %v", err)
		}
	}(t)

	// --- TestBeginAndRecoveryApplyRemoteClearsOnlyOnMutatingRoutes ---
	func(t *testing.T) {
		ReparentStepHook = nil
		t.Cleanup(func() {
			ReparentStepHook = nil
			SyncStateIOFault = nil
		})
		plantClearableRecord := func(w *reparentWorkspace, runIDByte string) []byte {
			t.Helper()
			tip := w.Repo.RevParse("pr2")
			w.Repo.Git("update-ref", "refs/remotes/origin/pr2", tip)
			rec := ReparentRemoteRecord{
				Feature: w.Loc.Feature,
				RunID:   strings.Repeat(runIDByte, 32),
				Entries: []ReparentRemoteEntry{{
					Name: "pr2", GitBranch: "pr2", Remote: ReparentRemoteName,
					RemoteRef: "refs/remotes/origin/pr2", NewTipSHA: tip,
					RemoteSHAAtWrite: w.Repo.RevParse("pr2^"),
					PRBaseBefore:     "pr1", PRBaseAfter: "main",
					State: ReparentRemoteStatePending,
				}},
			}
			if err := SaveReparentRemoteRecord(w.Loc, rec); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
			if err != nil {
				t.Fatal(err)
			}
			return data
		}

		w := newReparentWorkspace(t, ModeExternal)
		before := plantClearableRecord(w, "8")
		in, _ := w.approvedInput()
		plan, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if after, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc)); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("--plan changed the pending record: err=%v", err)
		}

		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "post-lock-plan-approved" {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err != errSimulatedCrash {
			t.Fatalf("pre-admission failure = %v", err)
		}
		if after, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc)); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("a failure before authoritative admission changed the pending record: err=%v", err)
		}

		ReparentStepHook = nil
		plan, req, err = PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "artifact-written" {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err != errSimulatedCrash {
			t.Fatalf("clearable-record window 1 = %v", err)
		}
		windowOne := LoadReparentState(w.Loc)
		if windowOne.Kind != ReparentStateOK || windowOne.State.Stage != ReparentStageInitializing ||
			windowOne.State.CompatArtifactsWritten || windowOne.State.RemoteClearPending {
			t.Fatalf("clearable-record window 1 state = %+v", windowOne)
		}
		if after, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc)); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("window 1 changed the prior remote record: err=%v", err)
		}
		ReparentStepHook = nil
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("window 1 abort: %v", err)
		}

		ReparentStepHook = nil
		plan, req, err = PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "remote-clear-intent-written" {
				return errSimulatedCrash
			}
			return nil
		}
		run, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req})
		if err != errSimulatedCrash {
			t.Fatalf("remote clear intent crash = %v", err)
		}
		pendingState := LoadReparentState(w.Loc).State
		if pendingState == nil || !pendingState.RemoteClearPending ||
			!pendingState.RemoteClearSourcePresent || pendingState.RemoteClearTargetPresent ||
			pendingState.RemoteRecordWritten || pendingState.RemoteRecordEmpty ||
			len(pendingState.RemoteFollowupEntries) != 0 {
			t.Fatalf("remote clear transition was not durably journaled: %+v", pendingState)
		}
		if after, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc)); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("intent-only crash changed the pending record: err=%v", err)
		}
		projected, err := projectReparentRemotePending(w.Loc, w.Repo.Dir, &req.Stack, pendingState)
		if err != nil || len(projected) != 0 {
			t.Fatalf("continuation projection disagreed with journaled clear target: %v (%v)", projected, err)
		}
		ReparentStepHook = nil
		removeFaultCalls := 0
		SyncStateIOFault = func(op, path string) error {
			if op == SyncIORemoveReparentRemote && path == ReparentRemoteRecordPath(w.Loc) {
				removeFaultCalls++
				if removeFaultCalls == 2 {
					return errors.New("post-remove remote clear fault")
				}
			}
			return nil
		}
		if _, err := AbortReparent(in); err == nil ||
			!strings.Contains(err.Error(), "post-remove remote clear fault") {
			t.Fatalf("post-remove clear recovery = %v", err)
		}
		if _, err := os.Stat(ReparentRemoteRecordPath(w.Loc)); !os.IsNotExist(err) {
			t.Fatalf("post-remove clear fault did not expose the target absence: %v", err)
		}
		if !run.State.RemoteRecordBeforeCaptured || !run.State.RemoteRecordBeforePresent ||
			!LoadReparentState(w.Loc).State.RemoteClearPending {
			t.Fatal("fresh execution did not preserve the prior remote record for rollback")
		}
		SyncStateIOFault = nil
		if _, err := AbortReparent(in); err != nil {
			t.Fatal(err)
		}
		if after, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc)); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("pre-commit abort did not restore prior protection byte-exactly: err=%v", err)
		}
		saveFaultIn, saveFaultPlan := w.approvedInput()
		_, saveFaultReq, err := PlanReparent(saveFaultIn)
		if err != nil {
			t.Fatal(err)
		}
		SyncStateIOFault = func(op, path string) error {
			if op == SyncIOWriteReparentState && path == ReparentStatePath(w.Loc) {
				load := LoadReparentState(w.Loc)
				if load.Kind == ReparentStateOK && load.State.RemoteClearPending {
					return errors.New("post-rename remote clear journal save fault")
				}
			}
			return nil
		}
		if _, err := BeginReparentRun(ReparentBeginInput{
			Input: saveFaultIn, Plan: saveFaultPlan, Request: saveFaultReq,
		}); err == nil || !strings.Contains(err.Error(), "remote clear journal save fault") {
			t.Fatalf("remote clear journal save fault = %v", err)
		}
		SyncStateIOFault = nil
		if pending := LoadReparentState(w.Loc).State; pending == nil || !pending.RemoteClearPending {
			t.Fatalf("post-rename journal save did not leave recoverable intent: %+v", pending)
		}
		if _, err := AbortReparent(saveFaultIn); err != nil {
			t.Fatalf("abort after journal save fault: %v", err)
		}
		if after, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc)); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("save-fault abort did not restore prior protection: %v", err)
		}

		recovery := newReparentWorkspace(t, ModeExternal)
		recoveryIn, recoveryPlan := recovery.approvedInput()
		_, recoveryReq, err := PlanReparent(recoveryIn)
		if err != nil {
			t.Fatal(err)
		}
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "artifact-written" {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := BeginReparentRun(ReparentBeginInput{
			Input: recoveryIn, Plan: recoveryPlan, Request: recoveryReq,
		}); err != errSimulatedCrash {
			t.Fatalf("recovery setup = %v", err)
		}
		ReparentStepHook = nil
		plantClearableRecord(recovery, "9")
		clearObservedAtDurablePreflight := false
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step != "remote-clear-intent-written" {
				return nil
			}
			load := LoadReparentState(recovery.Loc)
			clearObservedAtDurablePreflight = load.Kind == ReparentStateOK &&
				load.State.Stage == ReparentStagePreflight &&
				load.State.CompatArtifactsWritten &&
				ClassifyReparentCompatArtifacts(recovery.Loc, load.State.RunID).Complete
			return nil
		}
		recovered, err := openReparentRecovery(recoveryIn, ReparentRouteVerbContinue)
		ReparentStepHook = nil
		if err != nil {
			t.Fatal(err)
		}
		if !clearObservedAtDurablePreflight {
			t.Fatal("recovery cleared a prior remote record before durable preflight and compatibility")
		}
		if _, err := os.Stat(ReparentRemoteRecordPath(recovery.Loc)); !os.IsNotExist(err) {
			t.Fatalf("recovery did not apply the clear after reclaim: %v", err)
		}
		if !recovered.lockHeld {
			t.Fatal("recovery clear ran without retaining the reclaimed lock")
		}
		if _, err := AbortReparent(recoveryIn); err != nil {
			t.Fatal(err)
		}
	}(t)

	// --- TestBeginReparentRunRefusesDriftedDecision ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		plan, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}

		// A new commit on the target between the approval and the lock.
		w.Repo.Git("switch", "-q", "pr2")
		w.Repo.Commit("late.txt", "late")
		w.Repo.Git("switch", "-q", "main")

		_, err = BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req})
		guardErr, ok := err.(*PlanGuardRefusalError)
		if !ok {
			t.Fatalf("drifted decision = %v (%T)", err, err)
		}
		if guardErr.Kind != string(RefusalRevalidationMismatch) {
			t.Fatalf("kind = %q", guardErr.Kind)
		}
		if LoadReparentState(w.Loc).Kind != ReparentStateAbsent {
			t.Fatal("a refused begin leaves nothing behind")
		}
		if _, statErr := os.Stat(SyncRunGuardPath(w.Loc.FeaturePath)); !os.IsNotExist(statErr) {
			t.Fatal("a refused begin releases the lock")
		}
	}(t)

	// --- TestBeginReparentRunCrashWindowOneLeavesArtifactWithoutCompat ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()

		// Window 1 of §11.9: the artifact is written, the compatibility
		// artifacts are not, and nothing is mutated.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "artifact-written" {
				return errSimulatedCrash
			}
			return nil
		}
		plan, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err != errSimulatedCrash {
			t.Fatalf("begin error = %v", err)
		}
		ReparentStepHook = nil

		load := LoadReparentState(w.Loc)
		if load.Kind != ReparentStateOK || load.State.Stage != ReparentStageInitializing {
			t.Fatalf("artifact after window 1 = %+v", load)
		}
		if _, err := os.Stat(SyncRunStatePath(w.Loc.FeaturePath)); !os.IsNotExist(err) {
			t.Fatal("window 1 means the compatibility artifacts do NOT exist yet")
		}
		if got := w.Repo.RevParse("pr2"); got == "" {
			t.Fatal("nothing may be mutated in window 1")
		}
		if status := ClassifyReparentCompatArtifacts(w.Loc, load.State.RunID); status.Complete {
			t.Fatal("the envelope must classify incomplete")
		}

		resumePlan, err := PlanReparentContinue(in)
		if err != nil {
			t.Fatalf("continue plan after window 1: %v", err)
		}
		if !resumePlan.Runnable || resumePlan.Refusal.Kind != nil {
			t.Fatalf("owned missing compatibility must be repairable, not a plan blocker: %+v", resumePlan.Refusal)
		}
		for _, blocker := range resumePlan.Blockers {
			if blocker.Kind == ReparentRefusalCompatArtifactMissing {
				t.Fatalf("owned missing compatibility was advertised as fatal: %+v", blocker)
			}
		}

		// §11.2a — a --continue completes the envelope and finishes the run.
		run, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue after window 1: %v", err)
		}
		if run.State.Stage != ReparentStageCompleted {
			t.Fatalf("stage after continue = %q", run.State.Stage)
		}
		assertReparentSucceeded(t, w)
		refsAfter := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		if _, err := ContinueReparent(in); err == nil {
			t.Fatal("a second window-1 continue must find no run")
		}
		if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsAfter {
			t.Fatal("the second window-1 continue changed refs")
		}
	}(t)

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, plan := w.approvedInput()
		_, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "artifact-written" {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err != errSimulatedCrash {
			t.Fatalf("begin error = %v", err)
		}
		ReparentStepHook = nil
		refsBefore := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("window-1 abort: %v", err)
		}
		if _, err := AbortReparent(in); err == nil {
			t.Fatal("a second window-1 abort must find no run")
		}
		if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsBefore {
			t.Fatal("window-1 abort attempts changed refs")
		}
	}(t)
}

var errSimulatedCrash = &simulatedCrashError{}

type simulatedCrashError struct{}

func (e *simulatedCrashError) Error() string { return "simulated crash" }

// ---------------------------------------------------------------------------
// Forward execution — external and checkout.
// ---------------------------------------------------------------------------

func TestRunReparentExternalSuccess(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	log := reparentCaptureArgv(t)
	in, _ := w.approvedInput()

	mainTip := w.Repo.RevParse("main")
	pr2Before := w.Repo.RevParse("pr2")
	pr3Before := w.Repo.RevParse("pr3")

	run := w.begin(in)
	lateHolder := filepath.Join(canonicalize(t.TempDir()), "late-pr3-holder")
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step != "record-written" {
			return nil
		}
		w.Repo.Git("worktree", "add", "-q", lateHolder, "pr3")
		return errSimulatedCrash
	}
	if err := RunReparent(run); err != errSimulatedCrash {
		t.Fatalf("run before late-holder admission = %v", err)
	}
	ReparentStepHook = nil
	if len(run.State.ApprovedHolders) != 0 {
		t.Fatalf("late holder was adopted into the approved snapshot: %+v", run.State.ApprovedHolders)
	}
	_, err := ContinueReparent(in)
	var refusal *ReparentRefusalError
	if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalHolderUnsafe {
		t.Fatalf("late holder after crash = %v", err)
	}
	if !HasReparentState(w.Loc) {
		t.Fatal("late holder refusal must preserve state")
	}
	w.Repo.Git("worktree", "remove", lateHolder)
	run, err = ContinueReparent(in)
	if err != nil {
		t.Fatalf("continue after removing late holder: %v", err)
	}

	if run.State.Stage != ReparentStageCompleted {
		t.Fatalf("final stage = %q", run.State.Stage)
	}
	if !run.State.CommitPointReached {
		t.Fatal("a completed run has passed its commit point")
	}
	assertReparentSucceeded(t, w)

	// The refs moved to the computed tips, and the replayed commits are the
	// ones the cutoffs bounded.
	if got := w.Repo.RevParse("pr2"); got == pr2Before {
		t.Fatal("pr2 was not replayed")
	}
	if got := w.Repo.RevParse("pr3"); got == pr3Before {
		t.Fatal("pr3 was not replayed")
	}
	if parent := strings.ToLower(strings.TrimSpace(w.Repo.Git("rev-parse", "pr2^"))); parent != mainTip {
		t.Fatalf("pr2's parent = %s, want the destination %s", parent, mainTip)
	}
	if parent := strings.ToLower(strings.TrimSpace(w.Repo.Git("rev-parse", "pr3^"))); parent != w.Repo.RevParse("pr2") {
		t.Fatal("pr3 must sit on the recomputed pr2, not on the pre-image")
	}
	// pr1 is outside the closure and is untouched.
	if got := w.Repo.Git("log", "--oneline", "pr1"); !strings.Contains(got, "pr1.txt") {
		t.Fatalf("pr1 changed: %s", got)
	}

	// §10.1 — the exact post-state, and nothing else.
	if got := w.entry("pr2"); got.Base != "refs/heads/main" || got.LastBaseSHA != mainTip {
		t.Fatalf("pr2 metadata = %+v", got)
	}
	if got := w.entry("pr3"); got.Base != "pr2" || got.LastBaseSHA != w.Repo.RevParse("pr2") {
		t.Fatalf("pr3 metadata = %+v", got)
	}
	if got := w.entry("pr1"); got.Base != "refs/heads/main" {
		t.Fatalf("an entry outside the closure changed: %+v", got)
	}

	// The remote follow-up record: nothing was ever published in this
	// fixture, so every row is written cleared and the file is removed.
	rec, err := LoadReparentRemoteRecord(w.Loc)
	if err != nil {
		t.Fatal(err)
	}
	if rec != nil && len(rec.PendingEntries()) != 0 {
		t.Fatalf("unpublished branches must not leave pending entries: %+v", rec)
	}

	reparentAssertNoDashC(t, *log)
	assertNoForbiddenReparentVerbs(t, *log)
	assertReparentRebaseArgv(t, *log)
}

func TestRunReparentCheckoutSuccessRestoresTheCheckout(t *testing.T) {
	w := newReparentWorkspace(t, ModeCheckout)
	log := reparentCaptureArgv(t)
	in, _ := w.approvedInput()

	originalBranch := strings.TrimSpace(w.Repo.Git("symbolic-ref", "--short", "HEAD"))
	run := w.begin(in)
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "holders-detached" {
			return errSimulatedCrash
		}
		return nil
	}
	if err := RunReparent(run); err != errSimulatedCrash {
		t.Fatalf("run before computation-context drift = %v", err)
	}
	ReparentStepHook = nil
	computation := run.computationHolder()
	if computation == nil || computation.DetachTargetSHA == "" {
		t.Fatalf("computation context was not durably detached: %+v", computation)
	}
	w.Repo.Git("commit", "--allow-empty", "-q", "-m", "operator detached commit")
	_, err := ContinueReparent(in)
	var refusal *ReparentRefusalError
	if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalHolderUnsafe {
		t.Fatalf("clean detached computation-context drift = %v", err)
	}
	if !HasReparentState(w.Loc) {
		t.Fatal("computation-context drift must preserve state")
	}
	w.Repo.Git("switch", "-q", "--detach", computation.DetachTargetSHA)
	run, err = ContinueReparent(in)
	if err != nil {
		t.Fatalf("continue after repairing computation context: %v", err)
	}

	assertReparentSucceeded(t, w)
	if got := strings.TrimSpace(w.Repo.Git("symbolic-ref", "--short", "HEAD")); got != originalBranch {
		t.Fatalf("the checkout was left on %q, want %q", got, originalBranch)
	}
	// Checkout mode never creates a second physical checkout.
	for _, argv := range *log {
		if len(argv) >= 2 && argv[0] == "worktree" && argv[1] == "add" {
			t.Fatal("checkout mode must never run git worktree add")
		}
	}
	// Its own computation context is recorded once and excluded from the
	// still-attached drift check, which is what keeps every checkout-mode run
	// from refusing holder-unsafe against the HEAD it detached itself.
	if run.State.PreimageHolderExcluded == "" {
		t.Fatal("the checkout's own computation context must be recorded")
	}
	reparentAssertNoDashC(t, *log)
	assertNoForbiddenReparentVerbs(t, *log)
}

// ---------------------------------------------------------------------------
// §9.7 — conflict pause, then --continue.
// ---------------------------------------------------------------------------

// T-052, T-053: the conflict pause, its native-abort trap, the reparent resume and the abort arm (AC-065, AC-076).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentWaivedKindsAndFailureDomainPersistence(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-053", "conflict-abort")
	// --- TestRunReparentConflictPausesAndContinues ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)

		// Make main and pr2 touch the same file differently, so replaying pr2
		// onto main conflicts.
		w.Repo.Git("switch", "-q", "pr2")
		if err := os.WriteFile(filepath.Join(w.Repo.Dir, "clash.txt"), []byte("from pr2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("add", "clash.txt")
		w.Repo.Git("commit", "-q", "-m", "pr2 clash")
		w.Repo.Git("switch", "-q", "main")
		if err := os.WriteFile(filepath.Join(w.Repo.Dir, "clash.txt"), []byte("from main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("add", "clash.txt")
		w.Repo.Git("commit", "-q", "-m", "main clash")

		in, _ := w.approvedInput()
		run := w.begin(in)
		err := RunReparent(run)
		pause, ok := err.(*ReparentConflictPause)
		if !ok {
			t.Fatalf("expected a conflict pause, got %v", err)
		}
		msg := pause.Message()
		for _, want := range []string{
			"reparent paused: conflict while computing pr2 (pr2)",
			"resolve in: " + run.State.ScratchPath,
			"then run: git -C " + run.State.ScratchPath + " rebase --continue",
			"then run: tws stack reparent customer --continue",
			"to discard the whole reparent: tws stack reparent customer --abort",
		} {
			if !strings.Contains(msg, want) {
				t.Fatalf("pause message lacks %q:\n%s", want, msg)
			}
		}
		for _, line := range strings.Split(msg, "\n") {
			// A pause is excluded from §13.2's one-refusal-line rule: no line may
			// start with the `reparent:` refusal marker.
			if strings.HasPrefix(line, "reparent: ") {
				t.Fatalf("a conflict pause carries no refusal marker: %q", line)
			}
		}
		if LoadReparentState(w.Loc).State.Stage != ReparentStageConflictPaused {
			t.Fatal("the pause must be persisted")
		}

		// A --continue while the rebase is still in progress refuses
		// conflict-unresolved and never runs `rebase --continue` on the
		// operator's behalf.
		_, err = ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalConflictUnresolved {
			t.Fatalf("continue during a conflict = %v", err)
		}

		// Resolve exactly as the operator would, then continue.
		scratch := run.State.ScratchPath
		if err := os.WriteFile(filepath.Join(scratch, "clash.txt"), []byte("resolved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, scratch, "add", "clash.txt")
		gitInTest(t, scratch, "-c", "core.editor=true", "rebase", "--continue")
		paused := LoadReparentState(w.Loc).State
		conflictRow := paused.Row("pr2")
		if conflictRow == nil || conflictRow.ConflictCompletionRef == "" {
			t.Fatalf("conflict completion marker was not persisted: %+v", conflictRow)
		}
		if marker, ok, err := reparentResolveRef(w.Repo.Dir, conflictRow.ConflictCompletionRef); err != nil ||
			!ok || marker != gitInTest(t, scratch, "rev-parse", "HEAD") {
			t.Fatalf("native completion marker = %q ok=%v err=%v", marker, ok, err)
		}

		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue after resolution: %v", err)
		}
		if resumed.State.Stage != ReparentStageCompleted {
			t.Fatalf("stage after continue = %q", resumed.State.Stage)
		}
		assertReparentSucceeded(t, w)
		refsAfter := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		if _, err := ContinueReparent(in); err == nil {
			t.Fatal("a second conflict continue must find no run")
		}
		if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsAfter {
			t.Fatal("the second conflict continue changed refs")
		}
	}(t)

	// Destination-ancestor regression: ancestry alone cannot distinguish a
	// completed replay from native rebase --abort.
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		w.Repo.Git("switch", "-q", "pr1")
		if err := os.WriteFile(filepath.Join(w.Repo.Dir, "one.txt"), []byte("parent version\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("add", "one.txt")
		w.Repo.Git("commit", "-q", "-m", "parent changes one")
		parentTip := w.Repo.RevParse("HEAD")
		w.Repo.Git("switch", "-q", "main")
		w.Repo.Git("branch", "-f", "pr2", "pr1")
		w.Repo.Git("switch", "-q", "pr2")
		if err := os.WriteFile(filepath.Join(w.Repo.Dir, "one.txt"), []byte("target version\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("add", "one.txt")
		w.Repo.Git("commit", "-q", "-m", "target changes one")
		w.Repo.Git("switch", "-q", "main")
		stack := w.loadStack()
		stack.Branches = stack.Branches[:2]
		stack.Branches[1].LastBaseSHA = parentTip
		data, err := yaml.Marshal(&stack)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(StackPath(w.Loc.FeaturePath), data, 0o644); err != nil {
			t.Fatal(err)
		}

		pr2Before := w.Repo.RevParse("pr2")
		in, _ := w.approvedInput()
		run := w.begin(in)
		if _, ok := RunReparent(run).(*ReparentConflictPause); !ok {
			t.Fatal("expected destination-ancestor conflict")
		}
		load := LoadReparentState(w.Loc)
		if load.Kind != ReparentStateOK {
			t.Fatalf("state after transaction veto = %+v", load)
		}
		st := load.State
		if st.Row("pr2").CandidateCount != 1 {
			t.Fatalf("the regression requires a first-commit conflict, candidates=%d", st.Row("pr2").CandidateCount)
		}
		ancestor, err := reparentIsAncestor(w.Repo.Dir, st.NewParentSHA, st.OldParentSHA)
		if err != nil || !ancestor {
			t.Fatalf("destination ancestor fixture = %v (%v)", ancestor, err)
		}
		gitInTest(t, run.State.ScratchPath, "rebase", "--abort")
		_, err = ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalConflictUnresolved ||
			!strings.Contains(refusal.Detail, "rebase --abort") {
			t.Fatalf("native abort false success = %v", err)
		}
		reconciled := LoadReparentState(w.Loc).State
		if reconciled.Row("pr2").Stage != ReparentRowPending || reconciled.Row("pr2").PlannedNewSHA != "" {
			t.Fatalf("native abort must leave the row pending: %+v", reconciled.Row("pr2"))
		}
		if w.Repo.RevParse("pr2") != pr2Before {
			t.Fatal("native abort recovery moved the public ref")
		}
		if _, err := AbortReparent(in); err != nil {
			t.Fatal(err)
		}
		if w.Repo.RevParse("pr2") != pr2Before {
			t.Fatal("reparent abort did not preserve the pre-image")
		}
		if _, err := AbortReparent(in); err == nil {
			t.Fatal("a second abort must find no run")
		}

		// Re-run the same first-commit conflict and quit it. --quit leaves a
		// partial/destination HEAD and no rebase state, but it must not create
		// the native completion marker appended to the rebase todo.
		in, _ = w.approvedInput()
		run = w.begin(in)
		if _, ok := RunReparent(run).(*ReparentConflictPause); !ok {
			t.Fatal("expected first-commit conflict before quit")
		}
		quitState := LoadReparentState(w.Loc).State
		quitRow := quitState.Row("pr2")
		gitInTest(t, run.State.ScratchPath, "rebase", "--quit")
		if _, ok, err := reparentResolveRef(w.Repo.Dir, quitRow.ConflictCompletionRef); err != nil || ok {
			t.Fatalf("rebase --quit created completion marker: ok=%v err=%v", ok, err)
		}
		_, err = ContinueReparent(in)
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalConflictUnresolved {
			t.Fatalf("native quit false success = %v", err)
		}
		if w.Repo.RevParse("pr2") != pr2Before {
			t.Fatal("native quit recovery moved the public ref")
		}
		gitInTest(t, run.State.ScratchPath, "restore", "--source=HEAD", "--staged", "--worktree", ".")
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("abort after native quit refusal: %v", err)
		}
	}(t)

	// --- TestAbortReparentFromConflictPaused ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)

		w.Repo.Git("switch", "-q", "pr2")
		if err := os.WriteFile(filepath.Join(w.Repo.Dir, "clash.txt"), []byte("from pr2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("add", "clash.txt")
		w.Repo.Git("commit", "-q", "-m", "pr2 clash")
		w.Repo.Git("switch", "-q", "main")
		if err := os.WriteFile(filepath.Join(w.Repo.Dir, "clash.txt"), []byte("from main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("add", "clash.txt")
		w.Repo.Git("commit", "-q", "-m", "main clash")

		in, _ := w.approvedInput()
		pr2Before := w.Repo.RevParse("pr2")
		pr3Before := w.Repo.RevParse("pr3")

		run := w.begin(in)
		if _, ok := RunReparent(run).(*ReparentConflictPause); !ok {
			t.Fatal("expected a conflict pause")
		}
		scratch := run.State.ScratchPath
		if active, err := reparentRebaseInProgress(scratch); err != nil || !active {
			t.Fatal("fixture must leave a rebase in progress")
		}

		aborted, err := AbortReparent(in)
		if err != nil {
			t.Fatalf("abort from conflict-paused: %v", err)
		}
		if aborted.State.Stage != ReparentStageAborted {
			t.Fatalf("stage = %q", aborted.State.Stage)
		}
		if w.Repo.RevParse("pr2") != pr2Before || w.Repo.RevParse("pr3") != pr3Before {
			t.Fatal("a conflict abort restores every affected ref")
		}
		if _, err := os.Stat(scratch); !os.IsNotExist(err) {
			t.Fatal("the scratch worktree must be removed once it is clean")
		}
		assertReparentResidueRemoved(t, w, aborted.State.RunID)
	}(t)

	// Crash after Git has created rebase state but before conflict-paused was
	// durable. Computing-entry reconciliation must recover both verbs.
	for _, verb := range []string{"continue", "abort"} {
		t.Run("unrecorded-conflict-"+verb, func(t *testing.T) {
			t.Cleanup(func() { ReparentStepHook = nil })
			w := newReparentWorkspace(t, ModeExternal)
			w.Repo.Git("switch", "-q", "pr2")
			if err := os.WriteFile(filepath.Join(w.Repo.Dir, "clash.txt"), []byte("from pr2\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			w.Repo.Git("add", "clash.txt")
			w.Repo.Git("commit", "-q", "-m", "pr2 clash")
			w.Repo.Git("switch", "-q", "main")
			if err := os.WriteFile(filepath.Join(w.Repo.Dir, "clash.txt"), []byte("from main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			w.Repo.Git("add", "clash.txt")
			w.Repo.Git("commit", "-q", "-m", "main clash")

			in, _ := w.approvedInput()
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "conflict-detected-before-state:pr2" {
					return errSimulatedCrash
				}
				return nil
			}
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("%s crash setup = %v", verb, err)
			}
			ReparentStepHook = nil
			if st := LoadReparentState(w.Loc).State; st.Stage != ReparentStageComputing {
				t.Fatalf("%s pre-reconcile stage = %q", verb, st.Stage)
			}
			if active, err := reparentRebaseInProgress(run.State.ScratchPath); err != nil || !active {
				t.Fatalf("%s must retain the active rebase: active=%v err=%v", verb, active, err)
			}
			if verb == "continue" {
				resumePlan, err := PlanReparentContinue(in)
				if err != nil {
					t.Fatal(err)
				}
				if resumePlan.Runnable || !hasReparentBlocker(resumePlan, ReparentRefusalConflictUnresolved) {
					t.Fatalf("continue plan did not mirror the active conflict: %+v", resumePlan.Blockers)
				}
				_, err = ContinueReparent(in)
				var refusal *ReparentRefusalError
				if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalConflictUnresolved {
					t.Fatalf("continue did not reconcile to conflict-paused: %v", err)
				}
				if st := LoadReparentState(w.Loc).State; st.Stage != ReparentStageConflictPaused {
					t.Fatalf("reconciled stage = %q", st.Stage)
				}
				if _, err := AbortReparent(in); err != nil {
					t.Fatal(err)
				}
			} else if _, err := AbortReparent(in); err != nil {
				t.Fatalf("abort from unrecorded conflict: %v", err)
			}
			if HasReparentState(w.Loc) {
				t.Fatalf("%s left state after recovery", verb)
			}
		})
	}
	testReparentNativeGitFailureIsPersistedAndResumable(t)
	assertReparentMatrixBehavior(t, "T-052",
		"conflict-and-native-failure-contract",
	)
	assertReparentMatrixBehavior(t, "T-082",
		"waived-kinds-and-failure-domain",
	)
}

// ---------------------------------------------------------------------------
// §11.8 / §11.8a — abort before and after the commit point.
// ---------------------------------------------------------------------------

// The abort arms: the bounded rollback, the forward-only arm and the reported outcome (AC-076).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestAbortReparentArms(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-063",
		"abort-before-and-after-commit",
		"repeat-abort-original-detached",
		"postcommit-abort-preserves-remote-record",
		"abort-remote-restore-journal",
	)
	// --- TestAbortReparentBeforeCommitPointRollsBack ---
	func(t *testing.T) {
		t.Cleanup(func() {
			ReparentStepHook = nil
			SyncStateIOFault = nil
		})
		w := newReparentWorkspace(t, ModeCheckout)
		w.Repo.Git("switch", "--detach", "main")
		original := w.Repo.Commit("repeat-abort-original.txt", "original")
		in, _ := w.approvedInput()

		pr2Before := w.Repo.RevParse("pr2")
		pr3Before := w.Repo.RevParse("pr3")
		remoteRef := "refs/remotes/origin/pr2"
		w.Repo.Git("update-ref", remoteRef, pr2Before)
		priorRecord := ReparentRemoteRecord{
			Feature: w.Loc.Feature, RunID: strings.Repeat("a", 32),
			Entries: []ReparentRemoteEntry{{
				Name: "pr2", GitBranch: "pr2", Remote: ReparentRemoteName,
				RemoteRef: remoteRef, NewTipSHA: pr2Before, RemoteSHAAtWrite: pr2Before,
				PRBaseBefore: "pr1", PRBaseAfter: "main", State: ReparentRemoteStatePending,
			}},
		}
		if err := SaveReparentRemoteRecord(w.Loc, priorRecord); err != nil {
			t.Fatal(err)
		}
		priorRemoteBytes, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
		if err != nil {
			t.Fatal(err)
		}
		stackBefore, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}

		// Crash window 9: the remote record is written and the CAS has not run.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "before-cas" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil

		if w.Repo.RevParse("pr2") != pr2Before {
			t.Fatal("no public ref may move before the CAS")
		}

		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "abort-holders-restored" {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := AbortReparent(in); err != errSimulatedCrash {
			t.Fatalf("first abort = %v", err)
		}
		if got := w.Repo.RevParse("HEAD"); got != original {
			t.Fatalf("first abort restored HEAD %s, want %s", got, original)
		}
		st := LoadReparentState(w.Loc).State
		if st == nil || len(st.DetachedHolders) == 0 ||
			!st.DetachedHolders[0].Restored || !st.OriginalDetached {
			t.Fatalf("first abort did not persist restored detached context: %+v", st)
		}
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "abort-remote-restore-intent-written" {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := AbortReparent(in); err != errSimulatedCrash {
			t.Fatalf("second abort before remote restore = %v", err)
		}
		intent := LoadReparentState(w.Loc).State
		if !intent.RemoteRecordRestorePending ||
			!intent.RemoteRecordRestoreSourceCaptured ||
			!intent.RemoteRecordRestoreSourcePresent {
			t.Fatalf("remote restore source was not durably journaled: %+v", intent)
		}
		restoreSource, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
		if err != nil || bytes.Equal(restoreSource, priorRemoteBytes) {
			t.Fatalf("intent-only crash unexpectedly restored the prior record: %v", err)
		}
		foreign := priorRecord
		foreign.RunID = strings.Repeat("c", 32)
		if err := SaveReparentRemoteRecord(w.Loc, foreign); err != nil {
			t.Fatal(err)
		}
		foreignBytes, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AbortReparent(in); err == nil ||
			!strings.Contains(err.Error(), string(ReparentRefusalStateForeign)) {
			t.Fatalf("foreign post-intent record was not refused: %v", err)
		}
		if got, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc)); err != nil ||
			!bytes.Equal(got, foreignBytes) {
			t.Fatalf("foreign post-intent record was overwritten: %v", err)
		}
		if err := WriteReparentRemoteRecordBytes(w.Loc, restoreSource); err != nil {
			t.Fatal(err)
		}
		ReparentStepHook = nil
		remoteFaultCalls := 0
		SyncStateIOFault = func(op, path string) error {
			if op == SyncIOWriteReparentRemote && path == ReparentRemoteRecordPath(w.Loc) {
				remoteFaultCalls++
				if remoteFaultCalls == 2 {
					return errors.New("post-rename remote restore fault")
				}
			}
			return nil
		}
		if _, err := AbortReparent(in); err == nil ||
			!strings.Contains(err.Error(), "post-rename remote restore fault") {
			t.Fatalf("third abort remote restore fault = %v", err)
		}
		SyncStateIOFault = nil
		pending := LoadReparentState(w.Loc).State
		if !pending.RemoteRecordRestorePending || !pending.RemoteRecordRestoreSourceCaptured {
			t.Fatal("remote restore journal was not preserved after post-rename failure")
		}
		if got, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc)); err != nil ||
			!bytes.Equal(got, priorRemoteBytes) {
			t.Fatalf("post-rename restore did not expose exact prior bytes: %v", err)
		}

		aborted, err := AbortReparent(in)
		if err != nil {
			t.Fatalf("fourth abort: %v", err)
		}
		if aborted.State.Stage != ReparentStageAborted {
			t.Fatalf("stage after abort = %q", aborted.State.Stage)
		}
		if got := w.Repo.RevParse("pr2"); got != pr2Before {
			t.Fatalf("pr2 = %s, want the pre-image %s", got, pr2Before)
		}
		if got := w.Repo.RevParse("pr3"); got != pr3Before {
			t.Fatalf("pr3 = %s, want the pre-image %s", got, pr3Before)
		}
		stackAfter, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}
		if string(stackAfter) != string(stackBefore) {
			t.Fatalf("stack.yaml was not restored byte-exactly:\n%s", stackAfter)
		}
		if got := w.Repo.RevParse("HEAD"); got != original {
			t.Fatalf("second abort restored HEAD %s, want %s", got, original)
		}
		assertReparentResidueRemoved(t, w, aborted.State.RunID)
		main := w.Repo.RevParse("main")
		pr2Remote := "refs/remotes/origin/pr2"
		pr3Remote := "refs/remotes/origin/pr3"
		w.Repo.Git("update-ref", pr2Remote, pr2Before)
		w.Repo.Git("update-ref", pr3Remote, main)
		prior := ReparentRemoteRecord{
			Feature: w.Loc.Feature, RunID: strings.Repeat("b", 32),
			Entries: []ReparentRemoteEntry{
				{
					Name: "pr2", GitBranch: "pr2", Remote: ReparentRemoteName,
					RemoteRef: pr2Remote, NewTipSHA: pr2Before, RemoteSHAAtWrite: pr2Before,
					State: ReparentRemoteStatePending,
				},
				{
					Name: "pr3", GitBranch: "pr3", Remote: ReparentRemoteName,
					RemoteRef: pr3Remote, NewTipSHA: pr3Before, RemoteSHAAtWrite: main,
					State: ReparentRemoteStatePending,
				},
			},
		}
		if err := SaveReparentRemoteRecord(w.Loc, prior); err != nil {
			t.Fatal(err)
		}
		priorBytes, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
		if err != nil {
			t.Fatal(err)
		}
		in, _ = w.approvedInput()
		partialRun := w.begin(in)
		capturedPrior, err := base64.StdEncoding.DecodeString(partialRun.State.RemoteRecordBeforeBase64)
		if err != nil || !bytes.Equal(capturedPrior, priorBytes) {
			t.Fatalf("fresh admission did not capture the prior record before clearing it: %v\n%s", err, capturedPrior)
		}
		persistedBeforeAbort := LoadReparentState(w.Loc).State
		if persistedBeforeAbort == nil || !persistedBeforeAbort.RemoteRecordBeforeCaptured {
			t.Fatalf("fresh admission did not persist the remote preimage: %+v", persistedBeforeAbort)
		}
		progress, err := LoadReparentRemoteRecord(w.Loc)
		if err != nil {
			t.Fatal(err)
		}
		if progress == nil || progress.RunID != prior.RunID ||
			progress.Entries[0].State != ReparentRemoteStateCleared ||
			progress.Entries[1].State != ReparentRemoteStatePending {
			t.Fatalf("fresh admission did not persist the expected partial clear progress: %+v", progress)
		}

		restoreIntentSeen := false
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "abort-remote-restore-intent-written" {
				restoreIntentSeen = true
				persisted := LoadReparentState(w.Loc).State
				expected, decodeErr := base64.StdEncoding.DecodeString(persisted.RemoteRecordBeforeBase64)
				if decodeErr != nil || !bytes.Equal(expected, priorBytes) {
					t.Fatalf("abort restore intent lost the captured prior record: %v\n%s", decodeErr, expected)
				}
			}
			return nil
		}
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("abort after partial clear progress: %v", err)
		}
		ReparentStepHook = nil
		if !restoreIntentSeen {
			t.Fatal("abort skipped the remote restore intent")
		}
		restored, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(restored, priorBytes) {
			t.Fatalf("partial clear progress was not restored byte-exactly:\n%s", restored)
		}
		if _, err := AbortReparent(in); err == nil {
			t.Fatal("a fifth pre-commit abort must find no run")
		}
	}(t)

	// --- TestAbortReparentAfterCASBeforeMetadataRestoresPreImage ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		pr2Before := w.Repo.RevParse("pr2")
		pr3Before := w.Repo.RevParse("pr3")
		w.Repo.Git("update-ref", "refs/remotes/origin/pr2", pr2Before)
		w.Repo.Git("update-ref", "refs/remotes/origin/pr3", pr3Before)
		stackBefore, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}

		// Crash window 12: refs committed, metadata still holds the pre-image.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "after-cas" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil
		if w.Repo.RevParse("pr2") == pr2Before {
			t.Fatal("window 12 must have committed the forward ref transaction")
		}

		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "abort-remote-restored-before-complete" {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := AbortReparent(in); err != errSimulatedCrash {
			t.Fatalf("window 12 first abort = %v", err)
		}
		ReparentStepHook = nil
		pending := LoadReparentState(w.Loc).State
		if !pending.RemoteRecordRestorePending ||
			!pending.RemoteRecordRestoreSourceCaptured ||
			!pending.RemoteRecordRestoreSourcePresent {
			t.Fatal("absent-preimage remote restore source was not durable")
		}
		if _, err := os.Stat(ReparentRemoteRecordPath(w.Loc)); !os.IsNotExist(err) {
			t.Fatalf("absent-preimage restore did not remove current record: %v", err)
		}

		aborted, err := AbortReparent(in)
		if err != nil {
			t.Fatalf("window 12 second abort: %v", err)
		}
		if w.Repo.RevParse("pr2") != pr2Before || w.Repo.RevParse("pr3") != pr3Before {
			t.Fatal("window 12 abort did not restore every pre-image ref")
		}
		stackAfter, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stackAfter, stackBefore) {
			t.Fatal("window 12 abort changed pre-image stack.yaml bytes")
		}
		assertReparentResidueRemoved(t, w, aborted.State.RunID)
		refsAfter := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		if _, err := AbortReparent(in); err == nil {
			t.Fatal("a second window 12 abort must find no run")
		}
		if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsAfter {
			t.Fatal("the second window 12 abort changed refs")
		}
	}(t)

	// --- TestAbortReparentAfterCommitPointIsForwardOnly ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()

		// Crash window 13: metadata written, holders not restored, cleanup not
		// run — the commit point has passed.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "after-write" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil

		persisted := LoadReparentState(w.Loc).State
		row := persisted.RowsInOrder()[0]
		remoteRef := "refs/remotes/origin/" + row.GitBranch
		w.Repo.Git("update-ref", remoteRef, row.PlannedNewSHA)
		record := ReparentRemoteRecord{
			Feature: w.Loc.Feature,
			RunID:   persisted.RunID,
			Entries: []ReparentRemoteEntry{{
				Name: row.Name, GitBranch: row.GitBranch, Remote: ReparentRemoteName,
				RemoteRef: remoteRef, NewTipSHA: row.PlannedNewSHA,
				RemoteSHAAtWrite: row.PreimageSHA,
				PRBaseBefore:     "pr1", PRBaseAfter: "main",
				State: ReparentRemoteStatePending,
			}},
		}
		if err := SaveReparentRemoteRecord(w.Loc, record); err != nil {
			t.Fatal(err)
		}
		persisted.RemoteRecordWritten = true
		persisted.RemoteRecordEmpty = false
		persisted.RemoteFollowupEntries = []string{row.Name}
		if err := SaveReparentState(w.Loc, persisted); err != nil {
			t.Fatal(err)
		}
		remoteBefore, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
		if err != nil {
			t.Fatal(err)
		}

		pr2After := w.Repo.RevParse("pr2")
		stackAfter, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}

		var prose strings.Builder
		in.Writers = ReparentWriters{Prose: &prose}
		aborted, err := AbortReparent(in)
		if err != nil {
			t.Fatalf("abort after the commit point: %v", err)
		}
		if !aborted.State.CommitPointReached {
			t.Fatal("the commit point must be proven from disk")
		}
		if got := w.Repo.RevParse("pr2"); got != pr2After {
			t.Fatal("a post-commit --abort MUST NOT move a ref back")
		}
		live, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}
		if string(live) != string(stackAfter) {
			t.Fatal("a post-commit --abort MUST NOT rewrite stack.yaml")
		}
		if !strings.Contains(prose.String(), "already committed") {
			t.Fatalf("the operator must be told the reparent had already committed: %q", prose.String())
		}
		if remoteAfter, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc)); err != nil ||
			!bytes.Equal(remoteAfter, remoteBefore) {
			t.Fatalf("post-commit abort changed the remote follow-up record: %v", err)
		}
		assertReparentResidueRemoved(t, w, aborted.State.RunID)
		if _, err := AbortReparent(in); err == nil {
			t.Fatal("a second post-commit abort must find no run")
		}
	}(t)

	// --- TestAbortReparentReportsItsArm ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		reparentStallAt(t, w, in, "after-write")

		run, err := AbortReparent(in)
		if err != nil {
			t.Fatalf("abort: %v", err)
		}
		if !run.AbortWasForwardOnly() {
			t.Fatal("a post-commit abort is forward completion only and must say so")
		}

		w2 := newReparentWorkspace(t, ModeExternal)
		in2, _ := w2.approvedInput()
		reparentStallAt(t, w2, in2, "before-cas")
		rolled, err := AbortReparent(in2)
		if err != nil {
			t.Fatalf("abort: %v", err)
		}
		if rolled.AbortWasForwardOnly() {
			t.Fatal("a pre-commit abort really did roll back")
		}
	}(t)
}

// ---------------------------------------------------------------------------
// §9.12 / §10.5 — byte-exact metadata, drift, and the commit point.
// ---------------------------------------------------------------------------

// T-058, T-059: byte-exact metadata, drift, the commit point inferred from disk, and an undecidable one (AC-070, AC-071).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentMetadataAndCommitPoint(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-057", "metadata-exactness")
	assertReparentMatrixBehavior(t, "T-058", "post-image-before-cas")
	_ = "asserts AC-069 AC-073"
	// --- TestReparentMetadataIsByteExactAndDriftRefuses ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()

		// Stop after the post-image is persisted but before the CAS, then edit
		// stack.yaml underneath the run.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "before-cas" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil

		persisted := LoadReparentState(w.Loc).State
		if persisted.StackAfterBase64 == "" || persisted.StackSHA256AfterExpected == "" {
			t.Fatal("the post-image bytes must be durable BEFORE the CAS")
		}

		if err := os.WriteFile(StackPath(w.Loc.FeaturePath), []byte("branches: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalMetadataDrift {
			t.Fatalf("an edited stack.yaml must refuse metadata-drift, got %v", err)
		}
		if got := w.Repo.RevParse("pr2"); got != persisted.Rows[0].PreimageSHA {
			t.Fatal("a drifted run must not have moved a ref")
		}
	}(t)

	// --- TestReparentCommitPointIsInferredFromDisk ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()

		// Crash window 12: the CAS committed and the metadata write has not run.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "after-cas" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil

		mid := LoadReparentState(w.Loc).State
		if mid.CommitPointReached {
			t.Fatal("refs alone do not establish the commit point: stack.yaml still holds the pre-image")
		}

		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue: %v", err)
		}
		if !resumed.State.CommitPointReached {
			t.Fatal("the conjunction must be observed and persisted")
		}
		assertReparentSucceeded(t, w)
	}(t)

	// --- TestReparentAbortRefusesWhenMetadataIsUnreadable ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "after-write")

		pr2After := w.Repo.RevParse("pr2")
		pr3After := w.Repo.RevParse("pr3")
		if st.StackSHA256After == "" {
			t.Fatal("fixture must stall after the metadata rename")
		}

		// Replace stack.yaml with a directory: every read now fails with EISDIR,
		// deterministically and without depending on file permissions.
		path := StackPath(w.Loc.FeaturePath)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}

		_, err := AbortReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalMetadataDrift {
			t.Fatalf("abort = %v, want metadata-drift", err)
		}
		if w.Repo.RevParse("pr2") != pr2After || w.Repo.RevParse("pr3") != pr3After {
			t.Fatal("an abort that cannot decide the commit point moves NOTHING")
		}
		if LoadReparentState(w.Loc).Kind != ReparentStateOK {
			t.Fatal("state is preserved")
		}

		// A --continue is equally refused rather than guessing.
		if _, err := ContinueReparent(in); err == nil {
			t.Fatal("continue must not proceed on an undecidable commit point")
		}
	}(t)
}

func TestReparentContinueRefusesForeignRefBeforeCommitPoint(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	in, _ := w.approvedInput()

	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "before-cas" {
			return errSimulatedCrash
		}
		return nil
	}
	run := w.begin(in)
	if err := RunReparent(run); err != errSimulatedCrash {
		t.Fatalf("run error = %v", err)
	}
	ReparentStepHook = nil

	// Crash window 16: the operator moved an affected branch before the
	// commit point. Neither verb may erase that work.
	w.Repo.Git("update-ref", "refs/heads/pr3", w.Repo.RevParse("main"))

	_, err := ContinueReparent(in)
	var refusal *ReparentRefusalError
	if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalRefForeignValue {
		t.Fatalf("continue = %v", err)
	}
	if !strings.Contains(refusal.Detail, "pr3") {
		t.Fatalf("the refusal must name the branch: %s", refusal.Detail)
	}
	if _, err := ContinueReparent(in); !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalRefForeignValue {
		t.Fatalf("second continue = %v", err)
	}

	_, err = AbortReparent(in)
	if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalAbortForeignValue {
		t.Fatalf("abort = %v", err)
	}
	if w.Repo.RevParse("pr3") != w.Repo.RevParse("main") {
		t.Fatal("a refusing abort changes nothing at all")
	}
	if _, err := AbortReparent(in); !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalAbortForeignValue {
		t.Fatalf("second abort = %v", err)
	}
}

// ---------------------------------------------------------------------------
// §11.9 — the crash-window matrix, driven through the injected seam.
// ---------------------------------------------------------------------------

func TestReparentCrashWindowsResumeForward(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-061", "all-crash-windows-resume")
	_ = "asserts AC-074"
	windows := []struct {
		window int
		name   string
		step   string
	}{
		{2, "2 compat written, no context", "compat-written"},
		{3, "3 scratch created", "context-created"},
		{5, "5 row computed, pin not written", "computed:pr2"},
		{6, "6 row pinned, validation not run", "pinned:pr2"},
		{7, "7 all rows computed, no post-image", "validated:pr3"},
		{8, "8 post-image persisted, record not written", "post-image-persisted"},
		{9, "9 record written, CAS not started", "record-written"},
		{12, "12 CAS committed, metadata unwritten", "after-cas"},
		{13, "13 metadata written, holders unrestored", "after-write"},
		{14, "14 holders restored, cleanup incomplete", "holders-restored"},
	}
	for _, window := range windows {
		t.Run(window.name, func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			in, _ := w.approvedInput()

			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == window.step {
					return errSimulatedCrash
				}
				return nil
			}
			reparentCrashAt(t, w, in, window.step)
			ReparentStepHook = nil

			if LoadReparentState(w.Loc).Kind != ReparentStateOK {
				t.Fatal("every window must leave a decodable artifact")
			}
			assertReparentCrashWindowState(t, w, window.window)
			resumed, err := ContinueReparent(in)
			if err != nil {
				t.Fatalf("continue: %v", err)
			}
			if resumed.State.Stage != ReparentStageCompleted {
				t.Fatalf("stage after continue = %q", resumed.State.Stage)
			}
			assertReparentSucceeded(t, w)
			refsAfter := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			if _, err := ContinueReparent(in); err == nil {
				t.Fatal("a second continue must find no remaining run")
			}
			if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsAfter {
				t.Fatal("the second continue attempt changed refs")
			}
			if window.window == 2 {
				testReparentHolderDetachIntentRecoversBothWindows(t)
			}
		})
	}
}

func TestReparentCrashWindowsAbortCleanly(t *testing.T) {
	windows := []struct {
		window      int
		step        string
		forwardOnly bool
	}{
		{window: 1, step: "artifact-written"}, {window: 2, step: "compat-written"}, {window: 3, step: "context-created"},
		{window: 5, step: "computed:pr2"},
		{window: 6, step: "pinned:pr2"}, {window: 7, step: "validated:pr3"}, {window: 8, step: "post-image-persisted"},
		{window: 9, step: "record-written"}, {window: 14, step: "holders-restored", forwardOnly: true},
	}
	for _, window := range windows {
		t.Run(window.step, func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			in, _ := w.approvedInput()
			pr2Before := w.Repo.RevParse("pr2")
			pr3Before := w.Repo.RevParse("pr3")

			ReparentStepHook = func(stage ReparentStage, s string) error {
				if s == window.step {
					return errSimulatedCrash
				}
				return nil
			}
			reparentCrashAt(t, w, in, window.step)
			ReparentStepHook = nil
			assertReparentCrashWindowState(t, w, window.window)

			aborted, err := AbortReparent(in)
			if err != nil {
				t.Fatalf("abort: %v", err)
			}
			if window.forwardOnly {
				if !aborted.AbortWasForwardOnly() {
					t.Fatal("post-commit window must complete forward")
				}
			} else if w.Repo.RevParse("pr2") != pr2Before || w.Repo.RevParse("pr3") != pr3Before {
				t.Fatal("a pre-commit abort restores every affected ref to its pre-image")
			}
			assertReparentResidueRemoved(t, w, aborted.State.RunID)
			refsAfter := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			// Re-running --abort after it finished is refused by the state
			// classifier rather than acting twice.
			if _, err := AbortReparent(in); err == nil {
				t.Fatal("a completed abort leaves no run to abort again")
			}
			if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsAfter {
				t.Fatal("the second abort attempt changed refs")
			}
		})
	}
}

type reparentCrashWindowFixture struct {
	w               *reparentWorkspace
	in              ReparentPlanInput
	resolveConflict func()
	cleanup         func()
}

func TestReparentCrashWindowMatrixEveryVerbTwice(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-045", "pins-and-files-backend-cas")
	assertReparentMatrixBehavior(t, "T-047", "reference-transaction-prepare-veto")
	assertReparentMatrixBehavior(t, "T-057", "metadata-exactness")
	assertReparentMatrixBehavior(t, "T-058", "post-image-before-cas")
	assertReparentMatrixBehavior(t, "T-059", "metadata-before-hash-gate")
	assertReparentMatrixBehavior(t, "T-061", "all-crash-windows-resume")
	assertReparentMatrixBehavior(t, "T-062", "partial-commit-reconciliation")
	assertReparentMatrixBehavior(t, "T-064", "postcommit-operator-advance")
	assertReparentMatrixBehavior(t, "T-076", "remote-record-before-cas")
	_ = "asserts AC-074"
	for window := 1; window <= 17; window++ {
		for _, verb := range []string{"continue", "abort"} {
			t.Run(fmt.Sprintf("window-%02d/%s", window, verb), func(t *testing.T) {
				fixture := setupReparentCrashWindow(t, window)
				if fixture.cleanup != nil {
					t.Cleanup(fixture.cleanup)
				}
				assertReparentCrashWindowState(t, fixture.w, window)
				if window == 4 && verb == "continue" {
					fixture.resolveConflict()
				}

				refusalWindow := window == 15 && verb == "continue" || window == 16
				for attempt := 1; attempt <= 2; attempt++ {
					refsBefore := fixture.w.Repo.Git(
						"for-each-ref", "--format=%(refname) %(objectname)", "refs/heads",
					)
					stackBefore, err := os.ReadFile(StackPath(fixture.w.Loc.FeaturePath))
					if err != nil {
						t.Fatal(err)
					}
					if verb == "continue" {
						_, err = ContinueReparent(fixture.in)
					} else {
						_, err = AbortReparent(fixture.in)
					}

					switch {
					case refusalWindow:
						assertCrashWindowRefusal(t, window, verb, err)
						if !HasReparentState(fixture.w.Loc) {
							t.Fatalf("window %d %s attempt %d discarded refusal state", window, verb, attempt)
						}
					case attempt == 1:
						if err != nil {
							t.Fatalf("window %d %s: %v", window, verb, err)
						}
						if HasReparentState(fixture.w.Loc) {
							t.Fatalf("window %d %s left authoritative state", window, verb)
						}
						if verb == "continue" || window == 13 || window == 14 || window == 17 {
							assertReparentSucceeded(t, fixture.w)
						}
					default:
						if err == nil {
							t.Fatalf("window %d second %s found work after cleanup", window, verb)
						}
					}

					if attempt == 2 || refusalWindow {
						refsAfter := fixture.w.Repo.Git(
							"for-each-ref", "--format=%(refname) %(objectname)", "refs/heads",
						)
						stackAfter, readErr := os.ReadFile(StackPath(fixture.w.Loc.FeaturePath))
						if readErr != nil {
							t.Fatal(readErr)
						}
						if refsAfter != refsBefore || !bytes.Equal(stackAfter, stackBefore) {
							t.Fatalf("window %d %s attempt %d was not idempotent", window, verb, attempt)
						}
					}
				}

				// The two intentionally refusing cells retain state. Repair the
				// foreign value or use the owning abort verb so the fixture also
				// proves the refusal did not make recovery impossible.
				if refusalWindow {
					st := LoadReparentState(fixture.w.Loc).State
					if window == 16 {
						row := st.Row("pr3")
						fixture.w.Repo.Git("update-ref", "refs/heads/"+row.GitBranch, row.PreimageSHA)
					}
					if _, err := AbortReparent(fixture.in); err != nil {
						t.Fatalf("window %d cleanup abort: %v", window, err)
					}
				}
			})
		}
	}

	t.Run("metadata-write-gate", func(t *testing.T) {
		fixture := setupReparentCrashWindow(t, 12)
		if fixture.cleanup != nil {
			t.Cleanup(fixture.cleanup)
		}
		state := LoadReparentState(fixture.w.Loc).State
		drifted := append([]byte{}, []byte("# operator metadata edit\n")...)
		if err := os.WriteFile(StackPath(fixture.w.Loc.FeaturePath), drifted, 0o644); err != nil {
			t.Fatal(err)
		}
		for attempt := 1; attempt <= 2; attempt++ {
			_, err := ContinueReparent(fixture.in)
			var refusal *ReparentRefusalError
			if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalMetadataDrift {
				t.Fatalf("metadata gate attempt %d = %v", attempt, err)
			}
			if got, readErr := os.ReadFile(StackPath(fixture.w.Loc.FeaturePath)); readErr != nil ||
				!bytes.Equal(got, drifted) {
				t.Fatalf("metadata gate attempt %d overwrote the operator edit: %v", attempt, readErr)
			}
		}
		before, err := base64.StdEncoding.DecodeString(state.StackBeforeBase64)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(StackPath(fixture.w.Loc.FeaturePath), before, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := AbortReparent(fixture.in); err != nil {
			t.Fatalf("metadata gate cleanup abort: %v", err)
		}
	})
}

func assertCrashWindowRefusal(t *testing.T, window int, verb string, err error) {
	t.Helper()
	var refusal *ReparentRefusalError
	if !asReparentRefusal(err, &refusal) {
		t.Fatalf("window %d %s = %v, want typed refusal", window, verb, err)
	}
	switch window {
	case 15:
		if refusal.Kind != ReparentRefusalStatePresent {
			t.Fatalf("window 15 continue kind = %s", refusal.Kind)
		}
	case 16:
		want := ReparentRefusalRefForeignValue
		if verb == "abort" {
			want = ReparentRefusalAbortForeignValue
		}
		if refusal.Kind != want {
			t.Fatalf("window 16 %s kind = %s, want %s", verb, refusal.Kind, want)
		}
	default:
		t.Fatalf("window %d is not a refusing cell", window)
	}
}

func setupReparentCrashWindow(t *testing.T, window int) reparentCrashWindowFixture {
	t.Helper()
	w := newReparentWorkspace(t, ModeExternal)

	if window == 4 {
		w.Repo.Git("switch", "-q", "pr2")
		if err := os.WriteFile(filepath.Join(w.Repo.Dir, "crash-window-conflict.txt"), []byte("target\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("add", "crash-window-conflict.txt")
		w.Repo.Git("commit", "-q", "-m", "target conflict")
		w.Repo.Git("switch", "-q", "main")
		if err := os.WriteFile(filepath.Join(w.Repo.Dir, "crash-window-conflict.txt"), []byte("destination\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("add", "crash-window-conflict.txt")
		w.Repo.Git("commit", "-q", "-m", "destination conflict")
	}

	in, _ := w.approvedInput()
	fixture := reparentCrashWindowFixture{w: w, in: in}
	stall := func(step string) *ReparentState {
		return reparentStallAt(t, w, in, step)
	}

	switch window {
	case 1, 2:
		step := map[int]string{1: "artifact-written", 2: "compat-written"}[window]
		ReparentStepHook = func(stage ReparentStage, got string) error {
			if got == step {
				return errSimulatedCrash
			}
			return nil
		}
		plan, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err != errSimulatedCrash {
			t.Fatalf("window %d setup = %v", window, err)
		}
		ReparentStepHook = nil
	case 3:
		stall("context-created")
	case 4:
		run := w.begin(in)
		if _, ok := RunReparent(run).(*ReparentConflictPause); !ok {
			t.Fatal("window 4 setup did not pause in a real rebase conflict")
		}
		fixture.resolveConflict = func() {
			scratch := LoadReparentState(w.Loc).State.ScratchPath
			if err := os.WriteFile(filepath.Join(scratch, "crash-window-conflict.txt"), []byte("resolved\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitInTest(t, scratch, "add", "crash-window-conflict.txt")
			gitInTest(t, scratch, "-c", "core.editor=true", "rebase", "--continue")
		}
	case 5:
		stall("computed:pr2")
	case 6:
		stall("pinned:pr2")
	case 7:
		stall("validated:pr3")
	case 8:
		stall("post-image-persisted")
	case 9:
		stall("record-written")
	case 10:
		hook := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks/reference-transaction"
		writeExecutableForTest(t, hook,
			"#!/bin/sh\nif [ \"$1\" = \"prepared\" ]; then while read -r old new ref; do case \"$ref\" in refs/heads/pr3) exit 1;; esac; done; fi\nexit 0\n")
		run := w.begin(in)
		if err := RunReparent(run); err == nil {
			t.Fatal("window 10 setup did not fail transaction prepare")
		}
		if err := os.Remove(hook); err != nil {
			t.Fatal(err)
		}
	case 11:
		st := stall("before-cas")
		w.Repo.Git("update-ref", "refs/heads/pr2", st.Row("pr2").PlannedNewSHA)
	case 12:
		stall("after-cas")
	case 13:
		stall("after-write")
	case 14:
		stall("holders-restored")
	case 15:
		st := stall("after-cas")
		row := st.Row("pr2")
		w.Repo.Git("update-ref", "refs/heads/"+row.GitBranch, row.PreimageSHA, row.PlannedNewSHA)
		st.SetStage(ReparentStageAborting)
		st.AbortRows = []ReparentStateAbortRow{{
			Ref: "refs/heads/" + row.GitBranch, Restored: true,
			Classification: string(ReparentRefPlannedTip),
		}}
		if err := SaveReparentState(w.Loc, st); err != nil {
			t.Fatal(err)
		}
	case 16:
		st := stall("before-cas")
		row := st.Row("pr3")
		w.Repo.Git("update-ref", "refs/heads/"+row.GitBranch, w.Repo.RevParse("main"))
	case 17:
		st := stall("after-write")
		row := st.Row("pr3")
		tree := w.Repo.Git("rev-parse", row.PlannedNewSHA+"^{tree}")
		cmd := reparentTestGitCommand(t, w.Repo.Dir, "commit-tree", tree, "-p", row.PlannedNewSHA)
		cmd.Stdin = strings.NewReader("operator advance\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("window 17 descendant: %v\n%s", err, out)
		}
		w.Repo.Git("update-ref", "refs/heads/"+row.GitBranch, strings.TrimSpace(string(out)))
	default:
		t.Fatalf("unknown crash window %d", window)
	}
	fixture.cleanup = func() {
		ReparentStepHook = nil
		SyncStateIOFault = nil
	}
	return fixture
}

func reparentCrashAt(t *testing.T, w *reparentWorkspace, in ReparentPlanInput, step string) {
	t.Helper()
	if step == "artifact-written" || step == "compat-written" {
		plan, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err != errSimulatedCrash {
			t.Fatalf("begin crash at %s = %v", step, err)
		}
		return
	}
	run := w.begin(in)
	if err := RunReparent(run); err != errSimulatedCrash {
		t.Fatalf("run crash at %s = %v", step, err)
	}
}

func assertReparentCrashWindowState(t *testing.T, w *reparentWorkspace, window int) {
	t.Helper()
	load := LoadReparentState(w.Loc)
	if load.Kind != ReparentStateOK {
		t.Fatalf("window %d has no decodable state: %+v", window, load)
	}
	st := load.State
	switch window {
	case 1:
		if st.Stage != ReparentStageInitializing || st.CompatArtifactsWritten ||
			ClassifyReparentCompatArtifacts(w.Loc, st.RunID).Complete {
			t.Fatalf("window 1 must contain only authoritative state: %+v", st)
		}
		if _, err := os.Stat(st.ScratchPath); !os.IsNotExist(err) {
			t.Fatalf("window 1 unexpectedly created context: %v", err)
		}
	case 2:
		if st.Stage != ReparentStageInitializing || !st.CompatArtifactsWritten {
			t.Fatalf("window 2 must be compatibility-complete at initializing: %+v", st)
		}
		if !ClassifyReparentCompatArtifacts(w.Loc, st.RunID).Complete {
			t.Fatal("window 2 must have a complete compatibility envelope")
		}
		if _, err := os.Stat(st.ScratchPath); !os.IsNotExist(err) {
			t.Fatalf("window 2 must precede scratch/context creation: %v", err)
		}
		for _, row := range st.Rows {
			if row.Stage != ReparentRowPending || row.PlannedNewSHA != "" {
				t.Fatalf("window 2 row already computed: %+v", row)
			}
		}
	case 3:
		if st.Stage != ReparentStagePreflight {
			t.Fatalf("window 3 stage = %q", st.Stage)
		}
		if _, err := os.Stat(filepath.Join(st.ScratchPath, ".git")); err != nil {
			t.Fatalf("window 3 must have a scratch worktree: %v", err)
		}
		if _, ok, err := reparentResolveRef(w.Repo.Dir, st.Row("pr2").OldPinRef); err != nil || ok {
			t.Fatalf("window 3 must precede pin creation: ok=%v err=%v", ok, err)
		}
	case 4:
		if st.Stage != ReparentStageConflictPaused {
			t.Fatalf("window 4 stage = %q", st.Stage)
		}
		if active, err := reparentRebaseInProgress(st.ScratchPath); err != nil || !active {
			t.Fatalf("window 4 must retain native rebase state: active=%v err=%v", active, err)
		}
	case 5:
		if st.Stage != ReparentStageComputing || st.Row("pr2").Stage != ReparentRowComputed ||
			st.Row("pr2").PlannedNewSHA == "" || st.Row("pr3").Stage != ReparentRowPending {
			t.Fatalf("window 5 is not computed-before-pin: %+v", st.Rows)
		}
	case 6:
		if st.Stage != ReparentStageComputing || st.Row("pr2").Stage != ReparentRowPinned {
			t.Fatalf("window 6 is not pinned-before-validation: %+v", st.Rows)
		}
		for _, row := range st.Rows {
			if tip, ok, err := reparentResolveRef(w.Repo.Dir, row.OldPinRef); err != nil || !ok ||
				tip != row.PreimageSHA {
				t.Fatalf("window 6 old pin for %s = %q, ok=%v err=%v", row.Name, tip, ok, err)
			}
		}
		if tip, ok, err := reparentResolveRef(w.Repo.Dir, st.DestinationPinRef); err != nil || !ok ||
			tip != st.NewParentSHA {
			t.Fatalf("window 6 destination pin = %q, ok=%v err=%v", tip, ok, err)
		}
		if tip, ok, err := reparentResolveRef(w.Repo.Dir, st.Row("pr2").NewPinRef); err != nil || !ok ||
			tip != st.Row("pr2").PlannedNewSHA {
			t.Fatalf("window 6 computed pin = %q, ok=%v err=%v", tip, ok, err)
		}
	case 7:
		if st.Stage != ReparentStageComputing {
			t.Fatalf("window 7 stage = %q", st.Stage)
		}
		for _, row := range st.Rows {
			if row.Stage != ReparentRowValidated || row.PlannedNewSHA == "" {
				t.Fatalf("window 7 row not validated: %+v", row)
			}
		}
		if st.StackAfterBase64 != "" {
			t.Fatal("window 7 must precede post-image construction")
		}
	case 8:
		if st.Stage != ReparentStageBuildingPostImage || st.StackAfterBase64 == "" ||
			st.StackSHA256AfterExpected == "" || st.RemoteRecordWritten || st.RemoteRecordEmpty {
			t.Fatalf("window 8 state = %+v", st)
		}
	case 9:
		if st.Stage != ReparentStageWritingRemoteRecord ||
			(!st.RemoteRecordWritten && !st.RemoteRecordEmpty) || st.CASTransactionSucceeded {
			t.Fatalf("window 9 state = %+v", st)
		}
	case 10:
		if st.Stage != ReparentStageFailed ||
			st.FailureKind != string(ReparentRefusalRefTransactionMismatch) ||
			st.CASTransactionSucceeded {
			t.Fatalf("window 10 state = %+v", st)
		}
		for _, row := range st.Rows {
			if w.Repo.RevParse(row.GitBranch) != row.PreimageSHA {
				t.Fatalf("window 10 moved %s despite prepare veto", row.GitBranch)
			}
		}
	case 11:
		if st.Stage != ReparentStageCommittingRefs || st.CASTransactionSucceeded {
			t.Fatalf("window 11 state = %+v", st)
		}
		classes, err := ClassifyReparentRefs(w.Repo.Dir, st.RowsInOrder())
		if err != nil || len(classes) != 2 ||
			classes[0].Class != ReparentRefPlannedTip ||
			classes[1].Class != ReparentRefPreimage {
			t.Fatalf("window 11 classification = %+v err=%v", classes, err)
		}
	case 12:
		if st.Stage != ReparentStageCommittingRefs || !st.CASTransactionSucceeded {
			t.Fatalf("window 12 state = %+v", st)
		}
		for _, row := range st.Rows {
			if w.Repo.RevParse(row.GitBranch) != row.PlannedNewSHA {
				t.Fatalf("window 12 ref %s is not committed", row.GitBranch)
			}
		}
		data, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if sum := hex.EncodeToString(sliceSHA256(data)); err != nil || sum != st.StackSHA256Before {
			t.Fatalf("window 12 metadata is not the pre-image: %s %v", sum, err)
		}
	case 13:
		if st.Stage != ReparentStageWritingMetadata || st.StackSHA256After == "" {
			t.Fatalf("window 13 state = %+v", st)
		}
		data, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if sum := hex.EncodeToString(sliceSHA256(data)); err != nil || sum != st.StackSHA256AfterExpected {
			t.Fatalf("window 13 metadata is not the post-image: %s %v", sum, err)
		}
	case 14:
		if st.Stage != ReparentStageRestoringHolders || !st.CommitPointReached {
			t.Fatalf("window 14 state = %+v", st)
		}
	case 15:
		if st.Stage != ReparentStageAborting || len(st.AbortRows) != 1 ||
			!st.AbortRows[0].Restored {
			t.Fatalf("window 15 state = %+v", st)
		}
		classes, err := ClassifyReparentRefs(w.Repo.Dir, st.RowsInOrder())
		if err != nil || len(classes) != 2 ||
			classes[0].Class != ReparentRefPreimage ||
			classes[1].Class != ReparentRefPlannedTip {
			t.Fatalf("window 15 classification = %+v err=%v", classes, err)
		}
	case 16:
		classes, err := ClassifyReparentRefs(w.Repo.Dir, st.RowsInOrder())
		if err != nil || len(classes) != 2 || classes[1].Class != ReparentRefForeign {
			t.Fatalf("window 16 classification = %+v err=%v", classes, err)
		}
	case 17:
		if st.Stage != ReparentStageWritingMetadata ||
			st.StackSHA256After != st.StackSHA256AfterExpected {
			t.Fatalf("window 17 state = %+v", st)
		}
		live := w.Repo.RevParse("pr3")
		if live == st.Row("pr3").PlannedNewSHA {
			t.Fatal("window 17 did not advance the affected branch")
		}
		ancestor, err := reparentIsAncestor(w.Repo.Dir, st.Row("pr3").PlannedNewSHA, live)
		if err != nil || !ancestor {
			t.Fatalf("window 17 live tip is not an operator descendant: %v %v", ancestor, err)
		}
	default:
		t.Fatalf("no concrete state assertion for crash window %d", window)
	}
}

func TestReparentAbortIsIdempotentMidRollback(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	in, _ := w.approvedInput()
	pr2Before := w.Repo.RevParse("pr2")

	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "after-cas" {
			return errSimulatedCrash
		}
		return nil
	}
	run := w.begin(in)
	if err := RunReparent(run); err != errSimulatedCrash {
		t.Fatalf("run error = %v", err)
	}

	ReparentStepHook = nil

	// Crash window 15: a files-backend abort restored one ref before dying,
	// while another affected ref still holds its planned tip.
	st := LoadReparentState(w.Loc).State
	pr2 := st.Row("pr2")
	pr3 := st.Row("pr3")
	w.Repo.Git("update-ref", "refs/heads/pr2", pr2.PreimageSHA, pr2.PlannedNewSHA)
	st.SetStage(ReparentStageAborting)
	st.AbortRows = []ReparentStateAbortRow{{
		Ref:            "refs/heads/pr2",
		Restored:       true,
		Classification: string(ReparentRefPlannedTip),
	}}
	if err := SaveReparentState(w.Loc, st); err != nil {
		t.Fatal(err)
	}
	persisted := LoadReparentState(w.Loc).State
	if persisted.Stage != ReparentStageAborting || len(persisted.AbortRows) != 1 ||
		!persisted.AbortRows[0].Restored || persisted.AbortRows[0].Ref != "refs/heads/pr2" {
		t.Fatalf("window 15 abort progress was not durable: %+v", persisted)
	}
	classes, err := ClassifyReparentRefs(w.Repo.Dir, st.RowsInOrder())
	if err != nil {
		t.Fatal(err)
	}
	if classes[0].Class != ReparentRefPreimage || classes[1].Class != ReparentRefPlannedTip ||
		w.Repo.RevParse("pr3") != pr3.PlannedNewSHA {
		t.Fatalf("window 15 is not a partial rollback: %+v", classes)
	}

	// A --continue against an aborting run refuses and points back at --abort.
	_, err = ContinueReparent(in)
	var refusal *ReparentRefusalError
	if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalStatePresent {
		t.Fatalf("continue against an aborting run = %v", err)
	}
	if _, err := ContinueReparent(in); err == nil {
		t.Fatal("a second continue against an aborting run must still refuse")
	}

	aborted, err := AbortReparent(in)
	if err != nil {
		t.Fatalf("resumed abort: %v", err)
	}
	if w.Repo.RevParse("pr2") != pr2Before {
		t.Fatal("the resumed abort must finish the rollback")
	}
	assertReparentResidueRemoved(t, w, aborted.State.RunID)
	refsAfter := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	if _, err := AbortReparent(in); err == nil {
		t.Fatal("a second completed abort must find no run")
	}
	if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsAfter {
		t.Fatal("the second completed abort changed refs")
	}
}

// ---------------------------------------------------------------------------
// §9.4b — the just-in-time untracked gate.
// ---------------------------------------------------------------------------

// T-051: the untracked gate and the residue probes that authorise a force-remove (AC-064).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentWorkingTreeProbes(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-051", "working-tree-probes")
	w := newReparentWorkspace(t, ModeExternal)
	repo := w.Repo
	// --- TestReparentUntrackedGateRefusesCollisions ---
	func(t *testing.T) {
		tip := repo.RevParse("HEAD")

		if err := reparentUntrackedGate(repo.Dir, tip); err != nil {
			t.Fatalf("a clean worktree must pass the gate: %v", err)
		}

		// An untracked file that the target tree also carries would be
		// overwritten by the checkout.
		older := repo.RevParse("HEAD~2")
		if err := os.WriteFile(filepath.Join(repo.Dir, "three.txt"), []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		repo.Git("rm", "-q", "--cached", "three.txt")
		err := reparentUntrackedGate(repo.Dir, tip)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalUntrackedOverwrite {
			t.Fatalf("gate = %v", err)
		}
		if !strings.Contains(refusal.Detail, "three.txt") {
			t.Fatalf("the refusal must name every colliding path: %s", refusal.Detail)
		}
		repo.Git("checkout", "HEAD", "--", "three.txt")
		_ = older

		originalBranch := strings.TrimSpace(repo.Git("branch", "--show-current"))
		makeTarget := func(branch string, write func()) string {
			t.Helper()
			repo.Git("switch", "-q", "-c", branch)
			write()
			repo.Git("add", "-A")
			repo.Git("commit", "-q", "-m", branch)
			tip := repo.RevParse("HEAD")
			repo.Git("switch", "-q", originalBranch)
			repo.Git("branch", "-D", branch)
			return tip
		}
		dirTarget := makeTarget("gate-dir-target", func() {
			if err := os.MkdirAll(filepath.Join(repo.Dir, "collision-node"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo.Dir, "collision-node", "child.txt"), []byte("target\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		})
		fileTarget := makeTarget("gate-file-target", func() {
			if err := os.WriteFile(filepath.Join(repo.Dir, "collision-node"), []byte("target file\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		})
		symlinkTarget := makeTarget("gate-symlink-target", func() {
			if err := os.Symlink("target-destination", filepath.Join(repo.Dir, "collision-link")); err != nil {
				t.Fatal(err)
			}
		})
		assertCollision := func(treeish, want string) {
			t.Helper()
			err := reparentUntrackedGate(repo.Dir, treeish)
			var refusal *ReparentRefusalError
			if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalUntrackedOverwrite ||
				!strings.Contains(refusal.Detail, want) {
				t.Fatalf("prefix collision %q = %v", want, err)
			}
		}

		if err := os.WriteFile(filepath.Join(repo.Dir, "collision-node"), []byte("untracked file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		assertCollision(dirTarget, "collision-node -> collision-node/child.txt")
		if err := os.Remove(filepath.Join(repo.Dir, "collision-node")); err != nil {
			t.Fatal(err)
		}

		if err := os.MkdirAll(filepath.Join(repo.Dir, "collision-node"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo.Dir, "collision-node", "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		assertCollision(fileTarget, "collision-node/untracked.txt -> collision-node")
		if err := os.RemoveAll(filepath.Join(repo.Dir, "collision-node")); err != nil {
			t.Fatal(err)
		}

		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(repo.Dir, "collision-node")); err != nil {
			t.Fatal(err)
		}
		assertCollision(dirTarget, "collision-node -> collision-node/child.txt")
		if err := os.Remove(filepath.Join(repo.Dir, "collision-node")); err != nil {
			t.Fatal(err)
		}

		if err := os.MkdirAll(filepath.Join(repo.Dir, "collision-link"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo.Dir, "collision-link", "inside.txt"), []byte("untracked\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		assertCollision(symlinkTarget, "collision-link/inside.txt -> collision-link")
		if err := os.RemoveAll(filepath.Join(repo.Dir, "collision-link")); err != nil {
			t.Fatal(err)
		}
	}(t)

	// --- TestReparentResidueProbesReportFailures ---
	func(t *testing.T) {
		dir := canonicalize(t.TempDir())
		if _, err := measureReparentDirty(dir); err == nil {
			t.Fatal("a directory that is not a repository must report a probe failure")
		}
		if _, err := measureReparentUntracked(dir); err == nil {
			t.Fatal("a directory that is not a repository must report a probe failure")
		}
		if _, err := reparentContextResidue(dir); err == nil {
			t.Fatal("the residue proof must fail closed")
		}

		dirty, err := measureReparentDirty(repo.Dir)
		if err != nil || len(dirty) != 0 {
			t.Fatalf("a clean repository = %v (%v)", dirty, err)
		}
	}(t)

	// --- TestReparentScratchCleanlinessProbeFailurePreservesTheWorktree ---
	func(t *testing.T) {
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "holders-restored")

		// Break the scratch worktree's Git linkage: `git status` there now fails,
		// so its cleanliness cannot be proven either way.
		scratch := st.ScratchPath
		if err := os.WriteFile(filepath.Join(scratch, ".git"), []byte("gitdir: /nonexistent/reparent-probe\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := reparentContextResidue(scratch); err == nil {
			t.Fatal("fixture must make the cleanliness probe fail")
		}

		_, err := ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalProbeFailed {
			t.Fatalf("continue = %v, want probe-failed", err)
		}
		if !strings.Contains(refusal.Detail, scratch) {
			t.Fatalf("the refusal must name the worktree: %s", refusal.Detail)
		}
		if _, statErr := os.Stat(scratch); statErr != nil {
			t.Fatal("an unprovable worktree is never force-removed")
		}
		if LoadReparentState(w.Loc).Kind != ReparentStateOK {
			t.Fatal("state is preserved so the operator can inspect and resume")
		}

		// Once the linkage is repaired, cleanup completes.
		if err := os.Remove(filepath.Join(scratch, ".git")); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(scratch); err != nil {
			t.Fatal(err)
		}
		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue after repair: %v", err)
		}
		if resumed.State.Stage != ReparentStageCompleted {
			t.Fatalf("stage = %q", resumed.State.Stage)
		}
	}(t)
}

// ---------------------------------------------------------------------------
// Shared assertions.
// ---------------------------------------------------------------------------

func assertReparentSucceeded(t *testing.T, w *reparentWorkspace) {
	t.Helper()
	if LoadReparentState(w.Loc).Kind != ReparentStateAbsent {
		t.Fatal("a completed run deletes its artifact LAST, but it does delete it")
	}
	if _, err := os.Stat(SyncRunStatePath(w.Loc.FeaturePath)); !os.IsNotExist(err) {
		t.Fatal("the v4 compatibility payload must be removed")
	}
	if _, err := os.Stat(SyncStatePath(w.Loc.FeaturePath)); !os.IsNotExist(err) {
		t.Fatal("the legacy sentinel must be removed")
	}
	if w.Loc.Mode == ModeCheckout {
		if HasCheckoutLock(w.Loc.FeaturePath) {
			t.Fatal("the checkout lock must be released")
		}
	} else if _, err := os.Stat(SyncRunGuardPath(w.Loc.FeaturePath)); !os.IsNotExist(err) {
		t.Fatal("the sync run guard must be released")
	}
	pins := strings.TrimSpace(w.Repo.Git("for-each-ref", "--format=%(refname)", "refs/tws/reparent/**"))
	if pins != "" {
		t.Fatalf("pins survived cleanup: %s", pins)
	}
}

func assertReparentResidueRemoved(t *testing.T, w *reparentWorkspace, runID string) {
	t.Helper()
	if LoadReparentState(w.Loc).Kind != ReparentStateAbsent {
		t.Fatal("cleanup deletes the artifact last, but it does delete it")
	}
	if status := ClassifyReparentCompatArtifacts(w.Loc, runID); status.Complete {
		t.Fatal("the compatibility envelope must be gone")
	}
	pins := strings.TrimSpace(w.Repo.Git("for-each-ref", "--format=%(refname)", "refs/tws/reparent/**"))
	if pins != "" {
		t.Fatalf("pins survived cleanup: %s", pins)
	}
}

// assertNoForbiddenReparentVerbs audits the emitted argv log against §9.5.
func assertNoForbiddenReparentVerbs(t *testing.T, log [][]string) {
	t.Helper()
	for _, argv := range log {
		joined := strings.Join(argv, " ")
		for _, forbidden := range []string{
			"reset --hard", "replay", "--update-refs", "--autostash", "--rebase-merges ",
			"--apply", "push", "--force", "--discard-changes", "--ignore-other-worktrees",
		} {
			if strings.Contains(joined, forbidden) && !strings.Contains(joined, "--no-"+strings.TrimPrefix(forbidden, "--")) {
				// `worktree remove --force <scratch>` is the ONE permitted
				// --force, and only after the cleanliness proof of §11.6a.
				if forbidden == "--force" && len(argv) >= 2 && argv[0] == "worktree" && argv[1] == "remove" {
					continue
				}
				t.Fatalf("forbidden verb %q in emitted argv: git %s", forbidden, joined)
			}
		}
	}
}

// assertReparentRebaseArgv pins §9.4's explicit merge-backend rebase.
func assertReparentRebaseArgv(t *testing.T, log [][]string) {
	t.Helper()
	found := false
	for _, argv := range log {
		if !containsString(argv, "rebase") {
			continue
		}
		found = true
		joined := strings.Join(argv, " ")
		for _, want := range []string{
			"-c rebase.backend=merge", "-c rebase.updateRefs=false", "-c rebase.autoStash=false",
			"-c rebase.forkPoint=false", "-c rebase.rebaseMerges=false",
			"rebase --merge --no-fork-point --no-update-refs --no-autostash --no-rebase-merges --onto",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("rebase argv lacks %q: git %s", want, joined)
			}
		}
	}
	if !found {
		t.Fatal("no rebase argv was emitted")
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// §7.5 — collateral refs are disclosed, never moved.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// §9.6 / §9.8 — validation runs AFTER the row's tip is pinned, and residue
// the command created is never cleaned away.
// ---------------------------------------------------------------------------

func TestReparentValidationRunsAfterPinningAndRefusesResidue(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-048", "pin-before-validation")
	assertReparentMatrixBehavior(t, "T-050",
		"validation-and-residue",
		"preexisting-untracked-validation",
	)
	_ = "asserts AC-061 AC-063"
	w := newReparentWorkspace(t, ModeCheckout)
	preexisting := filepath.Join(w.Repo.Dir, "preexisting-untracked.txt")
	if err := os.WriteFile(preexisting, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := "if test -f pr3.txt; then printf 'left behind\\n' > residue.txt; else printf 'still allowed\\n' > preexisting-untracked.txt; fi"
	limit := 50
	in := w.planInput()
	in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
	// The validation identity is set BEFORE the token is minted: the
	// fingerprint binds validation.command_digest (field 31), so adding a
	// validation command after approval would — correctly — refuse
	// revalidation-mismatch at the lock.
	in.Validation = PlanValidationIdentity{
		Applies: true,
		Command: command,
		Source:  "config-workspace",
		Digest:  ValidationDigest(command),
	}
	plan, _, err := PlanReparent(in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Approval.Fingerprint == nil {
		t.Fatalf("plan minted no token: %+v", plan.Blockers)
	}
	in.Invocation = ReparentInvocationExecute
	in.Guard.Approve = *plan.Approval.Fingerprint

	run := w.begin(in)
	runErr := RunReparent(run)
	var refusal *ReparentRefusalError
	if !asReparentRefusal(runErr, &refusal) || refusal.Kind != ReparentRefusalValidationFailed {
		t.Fatalf("run = %v", runErr)
	}
	if !strings.Contains(refusal.Detail, "residue.txt") {
		t.Fatalf("the refusal must name the residue: %s", refusal.Detail)
	}
	if strings.Contains(refusal.Detail, "preexisting-untracked.txt") {
		t.Fatalf("preexisting untracked path was misclassified as new residue: %s", refusal.Detail)
	}
	if !strings.Contains(refusal.Detail, "Git did not produce this residue") {
		t.Fatalf("the refusal must say the command, not Git, produced it: %s", refusal.Detail)
	}

	// The target validation changed only a preexisting untracked path and
	// passed; the descendant created a new untracked path and stopped.
	st := LoadReparentState(w.Loc).State
	target := st.Row("pr2")
	if target.Stage != ReparentRowValidated {
		t.Fatalf("target row stage = %q, want validated", target.Stage)
	}
	row := st.Row("pr3")
	if row.Stage != ReparentRowPinned {
		t.Fatalf("residue row stage = %q, want pinned (pin precedes validation)", row.Stage)
	}
	if sha, ok, err := reparentResolveRef(w.Repo.Dir, row.NewPinRef); err != nil || !ok || sha != row.PlannedNewSHA {
		t.Fatalf("the computed tip must be pinned at the instant it exists: %q ok=%v err=%v", sha, ok, err)
	}
	// The residue is preserved: cleaning it would destroy operator data.
	if _, statErr := os.Stat(filepath.Join(w.Repo.Dir, "residue.txt")); statErr != nil {
		t.Fatal("the run must not clean, stash or reset residue the command created")
	}
	if got, err := os.ReadFile(preexisting); err != nil || string(got) != "still allowed\n" {
		t.Fatalf("preexisting untracked validation file = %q (%v)", got, err)
	}
	untrackedBaseline, err := measureReparentUntracked(w.Repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Repo.Dir, "pr3.txt"), []byte("tracked residue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trackedResidue, err := reparentValidationResidue(w.Repo.Dir, untrackedBaseline)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(trackedResidue, "pr3.txt") {
		t.Fatalf("tracked validation residue was not refused: %v", trackedResidue)
	}
	// No public ref moved.
	if w.Repo.RevParse("pr2") != target.PreimageSHA {
		t.Fatal("a validation failure never moves a public ref")
	}
	// The raw command lives ONLY in the 0600 artifact, never in the plan.
	if st.ValidationCommandRaw == "" {
		t.Fatal("the state artifact carries the raw command")
	}
	if plan.Policy.Validation.CommandDigest == nil || *plan.Policy.Validation.CommandDigest != in.Validation.Digest {
		t.Fatalf("the plan publishes only the digest: %+v", plan.Policy.Validation)
	}
	if !plan.Policy.Validation.RequiresCleanContext || plan.Policy.Validation.RunsOn != ReparentValidationRunsOn {
		t.Fatalf("validation policy = %+v", plan.Policy.Validation)
	}
}

// ---------------------------------------------------------------------------
// §9.9 — holder detachment and restoration, against a real linked worktree.
// ---------------------------------------------------------------------------

// T-028, T-083: detachment and restoration, and a deferred restore that is persisted and re-attempted (AC-038, AC-096).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentHolderLifecycle(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-028", "holder-lifecycle")
	assertReparentMatrixBehavior(t, "T-083", "holder-restore-deferral")
	// --- TestRunReparentDetachesAndRestoresHolders ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		log := reparentCaptureArgv(t)

		holder := filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
		w.Repo.Git("worktree", "add", "-q", holder, "pr3")
		if got := strings.TrimSpace(gitInTest(t, holder, "symbolic-ref", "--short", "HEAD")); got != "pr3" {
			t.Fatalf("fixture holder is on %q", got)
		}

		in, _ := w.approvedInput()
		run := w.begin(in)
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "holders-detached" {
				return errSimulatedCrash
			}
			return nil
		}
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run before holder drift = %v", err)
		}
		ReparentStepHook = nil
		st := LoadReparentState(w.Loc).State
		var detached *ReparentStateHolder
		for i := range st.DetachedHolders {
			if canonicalize(st.DetachedHolders[i].Path) == canonicalize(holder) {
				detached = &st.DetachedHolders[i]
			}
		}
		if detached == nil || detached.DetachTargetSHA == "" {
			t.Fatalf("holder detachment was not persisted: %+v", st.DetachedHolders)
		}
		gitInTest(t, holder, "commit", "--allow-empty", "-q", "-m", "operator detached commit")
		_, err := AbortReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalHolderUnsafe {
			t.Fatalf("clean detached holder drift during abort = %v", err)
		}
		if !HasReparentState(w.Loc) {
			t.Fatal("holder drift must preserve state")
		}
		gitInTest(t, holder, "switch", "-q", "--detach", detached.DetachTargetSHA)
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("abort after repairing holder: %v", err)
		}
		in, _ = w.approvedInput()
		run = w.begin(in)
		if err := RunReparent(run); err != nil {
			t.Fatalf("fresh run after holder-drift rollback: %v", err)
		}

		var recorded *ReparentStateHolder
		for i := range run.State.DetachedHolders {
			if run.State.DetachedHolders[i].HolderKind != "computation-context" {
				recorded = &run.State.DetachedHolders[i]
			}
		}
		if recorded == nil || recorded.GitBranch != "pr3" || !recorded.Restored {
			t.Fatalf("holder records = %+v", run.State.DetachedHolders)
		}
		// The worktree is back on its branch, which now points at the recomputed
		// tip — Git itself updated the working tree, with no reset anywhere.
		if got := strings.TrimSpace(gitInTest(t, holder, "symbolic-ref", "--short", "HEAD")); got != "pr3" {
			t.Fatalf("holder was left on %q, want pr3", got)
		}
		if got := strings.ToLower(strings.TrimSpace(gitInTest(t, holder, "rev-parse", "HEAD"))); got != w.Repo.RevParse("pr3") {
			t.Fatal("the restored holder must observe the new tip")
		}
		assertReparentSucceeded(t, w)
		assertNoForbiddenReparentVerbs(t, *log)
	}(t)

	// --- TestReparentDeferredHolderIsPersistedAndReattempted ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		holder := filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
		w.Repo.Git("worktree", "add", "-q", holder, "pr3")

		in, _ := w.approvedInput()
		var prose strings.Builder
		in.Writers = ReparentWriters{Prose: &prose}

		// The obstruction is created AFTER the commit point, in the window between
		// detaching the holder and re-attaching it — §9.9's own example: the
		// worktree "acquired tracked modifications while detached". pr1.txt exists
		// in the pre-image tree and is absent from the reparented one, so
		// re-attaching would have to delete a file the operator has edited, and
		// Git itself refuses. Past the commit point that is an advisory, never
		// the holder-unsafe refusal a pre-commit obstruction produces.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "after-write" {
				if err := os.WriteFile(filepath.Join(holder, "pr1.txt"), []byte("operator's own edit\n"), 0o644); err != nil {
					return err
				}
			}
			return nil
		}
		t.Cleanup(func() { ReparentStepHook = nil })
		run := w.begin(in)
		if err := RunReparent(run); err != nil {
			t.Fatalf("a deferred holder is an advisory, not a refusal: %v", err)
		}

		// The topology change committed...
		if !run.State.CommitPointReached {
			t.Fatal("the run must have passed its commit point")
		}
		// ...the deferral is persisted with its reason...
		st := LoadReparentState(w.Loc).State
		if st == nil {
			t.Fatal("the artifact must survive so a later --continue can re-attempt the holder")
		}
		if len(st.HolderRestoreDeferred) != 1 {
			t.Fatalf("deferred holders = %v", st.HolderRestoreDeferred)
		}
		var recorded *ReparentStateHolder
		for i := range st.DetachedHolders {
			if canonicalize(st.DetachedHolders[i].Path) == canonicalize(holder) {
				recorded = &st.DetachedHolders[i]
			}
		}
		if recorded == nil || recorded.Restored || recorded.RestoreReason == "" {
			t.Fatalf("the unrestored holder must be recorded with a reason: %+v", recorded)
		}
		// ...and the operator is told the warning kind and the exact next step.
		if !strings.Contains(prose.String(), ReparentWarnHolderRestoreDeferred) {
			t.Fatalf("the warning kind must be published:\n%s", prose.String())
		}
		if !strings.Contains(prose.String(), "--continue") {
			t.Fatalf("the operator must be told how to re-attempt:\n%s", prose.String())
		}
		printedRestore := fmt.Sprintf("git -C %s switch pr3", holder)
		if !strings.Contains(prose.String(), printedRestore) {
			t.Fatalf("the operator must be given the exact holder restore command %q:\n%s",
				printedRestore, prose.String())
		}
		if strings.Contains(prose.String(), string(ReparentRefusalHolderUnsafe)) {
			t.Fatal("past the commit point a holder is an advisory, never holder-unsafe")
		}
		for _, line := range strings.Split(strings.TrimSpace(prose.String()), "\n") {
			if strings.Contains(line, "holder") && strings.HasPrefix(line, "reparent:") &&
				!strings.HasPrefix(line, "reparent: "+ReparentWarnHolderRestoreDeferred+":") {
				t.Fatalf("holder deferral used an unowned prefix: %q", line)
			}
		}

		// Follow the printed command literally. The next continuation must
		// reconcile that correct manual restoration rather than reject it as
		// holder drift.
		ReparentStepHook = nil
		gitInTest(t, holder, "checkout", "--", "pr1.txt")
		gitInTest(t, holder, "switch", "pr3")
		stateBeforePlan, err := os.ReadFile(ReparentStatePath(w.Loc))
		if err != nil {
			t.Fatal(err)
		}
		resumePlan, err := PlanReparentContinue(in)
		if err != nil {
			t.Fatalf("continue plan after literal printed restoration: %v", err)
		}
		for _, blocker := range resumePlan.Blockers {
			if blocker.Kind == ReparentRefusalHolderUnsafe {
				t.Fatalf("continue plan rejected the literal printed restoration: %+v", blocker)
			}
		}
		stateAfterPlan, err := os.ReadFile(ReparentStatePath(w.Loc))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stateAfterPlan, stateBeforePlan) {
			t.Fatal("PlanReparentContinue wrote holder reconciliation state")
		}
		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue: %v", err)
		}
		if resumed.State.Stage != ReparentStageCompleted {
			t.Fatalf("stage = %q", resumed.State.Stage)
		}
		if got := strings.TrimSpace(gitInTest(t, holder, "symbolic-ref", "--short", "HEAD")); got != "pr3" {
			t.Fatalf("the holder was left on %q", got)
		}
		assertReparentSucceeded(t, w)
	}(t)
}

func TestReparentSwitchIntentCrashReconciliation(t *testing.T) {
	type scenario struct {
		name             string
		mode             WorkspaceMode
		stepPrefix       string
		action           string
		addHolder        bool
		originalDetached bool
	}
	scenarios := []scenario{
		{
			name: "checkout initial context detach", mode: ModeCheckout,
			stepPrefix: "computation-detach-intent-written", action: ReparentHolderSwitchDetach,
		},
		{
			name: "external computation switch", mode: ModeExternal,
			stepPrefix: "computation-switch-intent-written:pr2", action: ReparentHolderSwitchCompute,
		},
		{
			name: "linked holder detach", mode: ModeExternal, addHolder: true,
			stepPrefix: "holder-detach-intent-written:", action: ReparentHolderSwitchDetach,
		},
		{
			name: "linked holder restore", mode: ModeExternal, addHolder: true,
			stepPrefix: "holder-restore-intent-written:", action: ReparentHolderSwitchRestore,
		},
		{
			name: "checkout attached restore", mode: ModeCheckout,
			stepPrefix: "checkout-restore-intent-written", action: ReparentHolderSwitchRestore,
		},
		{
			name: "checkout detached restore", mode: ModeCheckout, originalDetached: true,
			stepPrefix: "checkout-restore-intent-written", action: ReparentHolderSwitchRestore,
		},
	}

	for _, tc := range scenarios {
		for _, verb := range []string{"continue", "abort"} {
			t.Run(tc.name+"/"+verb, func(t *testing.T) {
				w := newReparentWorkspace(t, tc.mode)
				holderPath := ""
				if tc.addHolder {
					holderPath = filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
					w.Repo.Git("worktree", "add", "-q", holderPath, "pr3")
				}
				if tc.originalDetached {
					w.Repo.Git("switch", "--detach", "HEAD")
				}
				originalPosition, err := measureReparentHolderPosition(w.Repo.Dir)
				if err != nil {
					t.Fatal(err)
				}
				in, _ := w.approvedInput()
				injected := errors.New("crash after holder switch before completion persistence")
				armed := false
				ReparentStepHook = func(stage ReparentStage, step string) error {
					if armed || !strings.HasPrefix(step, tc.stepPrefix) {
						return nil
					}
					armed = true
					SyncStateIOFault = func(op, path string) error {
						if op == SyncIOWriteReparentState && path == ReparentStatePath(w.Loc) {
							return injected
						}
						return nil
					}
					return nil
				}
				t.Cleanup(func() {
					ReparentStepHook = nil
					SyncStateIOFault = nil
				})
				run := w.begin(in)
				if err := RunReparent(run); !errors.Is(err, injected) {
					t.Fatalf("switch crash = %v", err)
				}
				ReparentStepHook = nil
				SyncStateIOFault = nil
				if !armed {
					t.Fatalf("switch step %q was never reached", tc.stepPrefix)
				}

				load := LoadReparentState(w.Loc)
				if load.Kind != ReparentStateOK {
					t.Fatalf("switch intent is not recoverable: %+v", load)
				}
				found := false
				for _, holder := range load.State.DetachedHolders {
					if holder.SwitchIntent != nil && holder.SwitchIntent.Action == tc.action {
						pos, posErr := measureReparentHolderPosition(holder.Path)
						if posErr != nil || !reparentHolderPositionMatches(
							pos,
							holder.SwitchIntent.DestinationBranch,
							holder.SwitchIntent.DestinationSHA,
						) {
							t.Fatalf("holder did not reach the persisted destination: pos=%+v err=%v intent=%+v",
								pos, posErr, holder.SwitchIntent)
						}
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("no durable %s switch intent remained: %+v", tc.action, load.State.DetachedHolders)
				}

				if verb == "continue" {
					_, err = ContinueReparent(in)
				} else {
					_, err = AbortReparent(in)
				}
				if err != nil {
					t.Fatalf("%s after switch crash: %v", verb, err)
				}
				if HasReparentState(w.Loc) {
					t.Fatalf("%s left authoritative state", verb)
				}
				if tc.mode == ModeCheckout {
					pos, err := measureReparentHolderPosition(w.Repo.Dir)
					if err != nil || pos != originalPosition {
						t.Fatalf("%s restored checkout to %+v, want %+v (err=%v)",
							verb, pos, originalPosition, err)
					}
				}
				if holderPath != "" {
					pos, err := measureReparentHolderPosition(holderPath)
					if err != nil || pos.Branch != "pr3" {
						t.Fatalf("%s restored linked holder to %+v (err=%v)", verb, pos, err)
					}
				}
			})
		}
	}
}

func TestReparentAdvancedManualHolderRestorationAcrossModesAndRecoveryVerbs(t *testing.T) {
	for _, mode := range []WorkspaceMode{ModeExternal, ModeCheckout} {
		for _, verb := range []string{"continue", "abort"} {
			t.Run(string(mode)+"/"+verb, func(t *testing.T) {
				w := newReparentWorkspace(t, mode)
				holderPath := w.Repo.Dir
				expectedBranch := "main"
				obstruction := "pr2.txt"
				if mode == ModeExternal {
					holderPath = filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
					w.Repo.Git("worktree", "add", "-q", holderPath, "pr3")
					expectedBranch = "pr3"
					obstruction = "pr1.txt"
				}

				in, _ := w.approvedInput()
				advanced := ""
				checkoutCollision := "operator-advance.txt"
				checkoutAdvancePath := filepath.Join(canonicalize(t.TempDir()), "advance-main")
				ReparentStepHook = func(stage ReparentStage, step string) error {
					if step != "after-write" {
						return nil
					}
					if mode == ModeExternal {
						return os.WriteFile(filepath.Join(holderPath, obstruction), []byte("operator edit\n"), 0o644)
					}
					w.Repo.Git("worktree", "add", "-q", checkoutAdvancePath, "main")
					if err := os.WriteFile(filepath.Join(checkoutAdvancePath, checkoutCollision), []byte("later\n"), 0o644); err != nil {
						return err
					}
					gitInTest(t, checkoutAdvancePath, "add", checkoutCollision)
					gitInTest(t, checkoutAdvancePath, "commit", "-q", "-m", "operator branch advance")
					advanced = strings.ToLower(strings.TrimSpace(gitInTest(t, checkoutAdvancePath, "rev-parse", "HEAD")))
					w.Repo.Git("worktree", "remove", "--force", checkoutAdvancePath)
					if err := os.WriteFile(filepath.Join(holderPath, checkoutCollision), []byte("untracked collision\n"), 0o644); err != nil {
						return err
					}
					return nil
				}
				t.Cleanup(func() { ReparentStepHook = nil })
				run := w.begin(in)
				if err := RunReparent(run); err != nil {
					t.Fatalf("deferred restore run: %v", err)
				}
				ReparentStepHook = nil
				load := LoadReparentState(w.Loc)
				st := load.State
				if st == nil || !st.CommitPointReached || len(st.HolderRestoreDeferred) == 0 {
					t.Fatalf("restore was not durably deferred after the commit point: %+v", load)
				}
				if mode == ModeExternal {
					gitInTest(t, holderPath, "checkout", "--", obstruction)
					advancePath := filepath.Join(canonicalize(t.TempDir()), "advance-"+expectedBranch)
					w.Repo.Git("worktree", "add", "-q", advancePath, expectedBranch)
					if err := os.WriteFile(filepath.Join(advancePath, "operator-advance.txt"), []byte("later\n"), 0o644); err != nil {
						t.Fatal(err)
					}
					gitInTest(t, advancePath, "add", "operator-advance.txt")
					gitInTest(t, advancePath, "commit", "-q", "-m", "operator branch advance")
					advanced = strings.ToLower(strings.TrimSpace(gitInTest(t, advancePath, "rev-parse", "HEAD")))
					w.Repo.Git("worktree", "remove", "--force", advancePath)
				} else if err := os.Remove(filepath.Join(holderPath, checkoutCollision)); err != nil {
					t.Fatal(err)
				}
				gitInTest(t, holderPath, "switch", expectedBranch)

				stateBeforePlan, err := os.ReadFile(ReparentStatePath(w.Loc))
				if err != nil {
					t.Fatal(err)
				}
				plan, err := PlanReparentContinue(in)
				if err != nil {
					t.Fatalf("continue plan after advanced-tip restoration: %v", err)
				}
				for _, blocker := range plan.Blockers {
					if blocker.Kind == ReparentRefusalHolderUnsafe {
						t.Fatalf("continue plan rejected expected branch at its live post-commit tip: %+v", blocker)
					}
				}
				stateAfterPlan, err := os.ReadFile(ReparentStatePath(w.Loc))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(stateAfterPlan, stateBeforePlan) {
					t.Fatal("continue planning persisted manual holder reconciliation")
				}

				var finished *ReparentRun
				if verb == "continue" {
					finished, err = ContinueReparent(in)
				} else {
					finished, err = AbortReparent(in)
				}
				if err != nil {
					t.Fatalf("%s after advanced-tip restoration: %v", verb, err)
				}
				if verb == "abort" && !finished.AbortWasForwardOnly() {
					t.Fatal("post-commit abort must remain forward-only")
				}
				if got := w.Repo.RevParse(expectedBranch); got != advanced {
					t.Fatalf("%s moved advanced %s from %s to %s", verb, expectedBranch, advanced, got)
				}
				pos, err := measureReparentHolderPosition(holderPath)
				if err != nil || pos.Branch != expectedBranch || pos.SHA != advanced {
					t.Fatalf("%s left holder at %+v, want %s/%s (err=%v)",
						verb, pos, expectedBranch, advanced, err)
				}
				assertReparentResidueRemoved(t, w, st.RunID)
			})
		}
	}
}

func TestReparentAdvancedManualRestorationPersistenceCrashRecovers(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	holder := filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
	w.Repo.Git("worktree", "add", "-q", holder, "pr3")
	in, _ := w.approvedInput()
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "after-write" {
			return os.WriteFile(filepath.Join(holder, "pr1.txt"), []byte("operator edit\n"), 0o644)
		}
		return nil
	}
	t.Cleanup(func() {
		ReparentStepHook = nil
		SyncStateIOFault = nil
	})
	run := w.begin(in)
	if err := RunReparent(run); err != nil {
		t.Fatalf("deferred restore run: %v", err)
	}
	ReparentStepHook = nil
	gitInTest(t, holder, "checkout", "--", "pr1.txt")

	advancePath := filepath.Join(canonicalize(t.TempDir()), "advance-pr3")
	w.Repo.Git("worktree", "add", "-q", advancePath, "pr3")
	if err := os.WriteFile(filepath.Join(advancePath, "operator-advance.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, advancePath, "add", "operator-advance.txt")
	gitInTest(t, advancePath, "commit", "-q", "-m", "operator branch advance")
	advanced := strings.ToLower(strings.TrimSpace(gitInTest(t, advancePath, "rev-parse", "HEAD")))
	w.Repo.Git("worktree", "remove", "--force", advancePath)
	gitInTest(t, holder, "switch", "pr3")

	injected := errors.New("crash after advanced holder restoration persistence")
	faultCalls := 0
	SyncStateIOFault = func(op, path string) error {
		if op != SyncIOWriteReparentState || path != ReparentStatePath(w.Loc) {
			return nil
		}
		faultCalls++
		if faultCalls == 2 {
			return injected
		}
		return nil
	}
	if _, err := ContinueReparent(in); !errors.Is(err, injected) {
		t.Fatalf("post-persistence crash = %v", err)
	}
	SyncStateIOFault = nil
	st := LoadReparentState(w.Loc).State
	if st == nil {
		t.Fatal("post-rename state did not survive")
	}
	var restored *ReparentStateHolder
	for i := range st.DetachedHolders {
		if canonicalize(st.DetachedHolders[i].Path) == canonicalize(holder) {
			restored = &st.DetachedHolders[i]
		}
	}
	if restored == nil || !restored.Restored || len(st.HolderRestoreDeferred) != 0 {
		t.Fatalf("advanced restoration was not persisted before the crash: holder=%+v deferred=%v",
			restored, st.HolderRestoreDeferred)
	}
	if _, err := ContinueReparent(in); err != nil {
		t.Fatalf("recovery after persisted advanced restoration: %v", err)
	}
	if got := w.Repo.RevParse("pr3"); got != advanced {
		t.Fatalf("recovery moved advanced pr3 from %s to %s", advanced, got)
	}
	assertReparentResidueRemoved(t, w, st.RunID)
}

func TestReparentPendingRestoreIntentAcceptsAdvancedLiveTip(t *testing.T) {
	for _, mode := range []WorkspaceMode{ModeExternal, ModeCheckout} {
		for _, verb := range []string{"continue", "abort"} {
			t.Run(string(mode)+"/"+verb, func(t *testing.T) {
				w := newReparentWorkspace(t, mode)
				holderPath := w.Repo.Dir
				expectedBranch := "main"
				stepPrefix := "checkout-restore-intent-written"
				if mode == ModeExternal {
					holderPath = filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
					w.Repo.Git("worktree", "add", "-q", holderPath, "pr3")
					expectedBranch = "pr3"
					stepPrefix = "holder-restore-intent-written:"
				}
				in, _ := w.approvedInput()
				injected := errors.New("crash after restore switch before completion persistence")
				armed := false
				ReparentStepHook = func(stage ReparentStage, step string) error {
					if armed || !strings.HasPrefix(step, stepPrefix) {
						return nil
					}
					armed = true
					SyncStateIOFault = func(op, path string) error {
						if op == SyncIOWriteReparentState && path == ReparentStatePath(w.Loc) {
							return injected
						}
						return nil
					}
					return nil
				}
				t.Cleanup(func() {
					ReparentStepHook = nil
					SyncStateIOFault = nil
				})
				run := w.begin(in)
				if err := RunReparent(run); !errors.Is(err, injected) {
					t.Fatalf("restore switch crash = %v", err)
				}
				ReparentStepHook = nil
				SyncStateIOFault = nil
				if !armed {
					t.Fatalf("restore intent step %q was never reached", stepPrefix)
				}

				load := LoadReparentState(w.Loc)
				if load.Kind != ReparentStateOK {
					t.Fatalf("restore intent is not recoverable: %+v", load)
				}
				foundIntent := false
				for _, holder := range load.State.DetachedHolders {
					if holder.SwitchIntent != nil && holder.SwitchIntent.Action == ReparentHolderSwitchRestore {
						foundIntent = true
					}
				}
				if !foundIntent {
					t.Fatalf("no pending restore intent remained: %+v", load.State.DetachedHolders)
				}

				if err := os.WriteFile(filepath.Join(holderPath, "advanced-after-intent.txt"), []byte("later\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitInTest(t, holderPath, "add", "advanced-after-intent.txt")
				gitInTest(t, holderPath, "commit", "-q", "-m", "advance after restore intent")
				advanced := strings.ToLower(strings.TrimSpace(gitInTest(t, holderPath, "rev-parse", "HEAD")))

				plan, err := PlanReparentContinue(in)
				if err != nil {
					t.Fatalf("continue plan with advanced restore intent: %v", err)
				}
				for _, blocker := range plan.Blockers {
					if blocker.Kind == ReparentRefusalHolderUnsafe {
						t.Fatalf("continue plan rejected pending restore intent at live tip: %+v", blocker)
					}
				}

				var finished *ReparentRun
				if verb == "continue" {
					finished, err = ContinueReparent(in)
				} else {
					finished, err = AbortReparent(in)
				}
				if err != nil {
					t.Fatalf("%s with advanced restore intent: %v", verb, err)
				}
				if verb == "abort" && !finished.AbortWasForwardOnly() {
					t.Fatal("advanced restore intent abort must be forward-only")
				}
				if got := w.Repo.RevParse(expectedBranch); got != advanced {
					t.Fatalf("%s moved advanced %s from %s to %s", verb, expectedBranch, advanced, got)
				}
				assertReparentResidueRemoved(t, w, load.State.RunID)
			})
		}
	}
}

func TestReparentManualRestorationStillRefusesDirtyExpectedBranch(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	holder := filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
	w.Repo.Git("worktree", "add", "-q", holder, "pr3")
	in, _ := w.approvedInput()
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "after-write" {
			return os.WriteFile(filepath.Join(holder, "pr1.txt"), []byte("operator edit\n"), 0o644)
		}
		return nil
	}
	t.Cleanup(func() { ReparentStepHook = nil })
	run := w.begin(in)
	if err := RunReparent(run); err != nil {
		t.Fatalf("deferred restore run: %v", err)
	}
	ReparentStepHook = nil
	gitInTest(t, holder, "checkout", "--", "pr1.txt")
	gitInTest(t, holder, "switch", "pr3")
	if err := os.WriteFile(filepath.Join(holder, "pr3.txt"), []byte("dirty restoration\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanReparentContinue(in)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, blocker := range plan.Blockers {
		if blocker.Kind == ReparentRefusalHolderUnsafe &&
			strings.Contains(blocker.Detail, "tracked modifications") {
			found = true
		}
	}
	if !found {
		t.Fatalf("dirty manual restoration was treated as safe: %+v", plan.Blockers)
	}
	gitInTest(t, holder, "checkout", "--", "pr3.txt")
	if _, err := ContinueReparent(in); err != nil {
		t.Fatalf("continue after cleaning manual restoration: %v", err)
	}
}

// §11.9 window 4, abort arm: --abort from conflict-paused runs
// `git rebase --abort` in the computation worktree before restoring anything.

// ---------------------------------------------------------------------------
// §11.9 windows 10, 11 and 17 — the three that need their own setup.
// ---------------------------------------------------------------------------

// Window 10: the CAS `prepare` fails. No public ref has moved, every holder is
// re-attached, and the run stays resumable.
// Two transaction-level crash windows: a vetoed prepare and a files-backend partial commit (AC-074, AC-075).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentCrashWindows10And11(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-047", "reference-transaction-prepare-veto")
	assertReparentMatrixBehavior(t, "T-062", "partial-commit-reconciliation")
	_ = "asserts AC-060"
	// --- TestReparentCrashWindow10CASPrepareFailure ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		pr2Before := w.Repo.RevParse("pr2")
		pr3Before := w.Repo.RevParse("pr3")

		// The veto is scoped to ONE affected branch, read from the hook's own
		// stdin, so the run's unrelated ref work — the scratch worktree's HEAD,
		// the pins, every `git switch` — proceeds normally and only the CAS's
		// `prepare` phase fails. A blanket veto would break worktree creation and
		// would never reach the transaction under test.
		hook := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks/reference-transaction"
		writeExecutableForTest(t, hook,
			"#!/bin/sh\n"+
				"if [ \"$1\" = \"prepared\" ]; then\n"+
				"  while read -r old new ref; do\n"+
				"    case \"$ref\" in refs/heads/pr3) echo 'veto' 1>&2; exit 1;; esac\n"+
				"  done\n"+
				"fi\n"+
				"exit 0\n")

		run := w.begin(in)
		err := RunReparent(run)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalRefTransactionMismatch {
			t.Fatalf("a vetoed prepare must refuse ref-transaction-mismatch, got %v", err)
		}
		if !refusal.StatePreserved {
			t.Fatal("with every holder restored the refusal carries state-preserved")
		}
		if w.Repo.RevParse("pr2") != pr2Before || w.Repo.RevParse("pr3") != pr3Before {
			t.Fatal("a failed prepare moves no public ref")
		}
		if LoadReparentState(w.Loc).Kind != ReparentStateOK {
			t.Fatal("the run must remain resumable")
		}

		// With the veto gone, --continue completes forward.
		if err := os.Remove(hook); err != nil {
			t.Fatal(err)
		}
		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue after a vetoed prepare: %v", err)
		}
		if resumed.State.Stage != ReparentStageCompleted {
			t.Fatalf("stage = %q", resumed.State.Stage)
		}
		assertReparentSucceeded(t, w)
		if _, err := ContinueReparent(in); err == nil {
			t.Fatal("a second prepare-failure continue must find no run")
		}
	}(t)

	// --- TestReparentCrashWindow11FilesBackendPartialCommit ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()

		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "before-cas" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil

		st := LoadReparentState(w.Loc).State
		pr2 := st.Row("pr2")
		pr3 := st.Row("pr3")
		if pr2.PlannedNewSHA == "" || pr3.PlannedNewSHA == "" {
			t.Fatalf("both rows must have computed: %+v", st.Rows)
		}
		// Exactly the state a crash during `commit` leaves on the files backend:
		// the first row moved, the second did not.
		w.Repo.Git("update-ref", "refs/heads/pr2", pr2.PlannedNewSHA)
		if w.Repo.RevParse("pr3") != pr3.PreimageSHA {
			t.Fatal("fixture must leave pr3 at its pre-image")
		}

		// The classification the recovery computes before writing anything.
		classes, err := ClassifyReparentRefs(w.Repo.Dir, st.RowsInOrder())
		if err != nil {
			t.Fatal(err)
		}
		if classes[0].Class != ReparentRefPlannedTip || classes[1].Class != ReparentRefPreimage {
			t.Fatalf("partial classification = %+v", classes)
		}
		lines := reparentForwardCASLines(st.RowsInOrder(), ReparentRefClassMap(classes))
		if lines[0].Verb != reparentCASVerifyVerb || lines[0].Old != pr2.PlannedNewSHA {
			t.Fatalf("the landed row must be verified at its planned tip, got %+v", lines[0])
		}
		if lines[1].Verb != reparentCASUpdateVerb || lines[1].Old != pr3.PreimageSHA {
			t.Fatalf("the unlanded row must be re-issued with its pre-image expectation, got %+v", lines[1])
		}

		var prose strings.Builder
		in.Writers = ReparentWriters{Prose: &prose}
		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue after a partial commit: %v", err)
		}
		if resumed.State.Stage != ReparentStageCompleted {
			t.Fatalf("stage = %q", resumed.State.Stage)
		}
		if !strings.Contains(prose.String(), "reparent-recovery: "+ReparentRecoveryPartialToken) {
			t.Fatalf("a partial commit is reported as recovery progress: %q", prose.String())
		}
		if w.Repo.RevParse("pr2") != pr2.PlannedNewSHA || w.Repo.RevParse("pr3") != pr3.PlannedNewSHA {
			t.Fatal("both rows must end at their planned tips")
		}
		assertReparentSucceeded(t, w)
		refsAfter := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		if _, err := ContinueReparent(in); err == nil {
			t.Fatal("a second partial-commit continue must find no run")
		}
		if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsAfter {
			t.Fatal("the second partial-commit continue changed refs")
		}
	}(t)

	// Window 10, abort arm.
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		before := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		hook := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks/reference-transaction"
		writeExecutableForTest(t, hook,
			"#!/bin/sh\nif [ \"$1\" = \"prepared\" ]; then while read -r old new ref; do case \"$ref\" in refs/heads/pr3) exit 1;; esac; done; fi\nexit 0\n")
		run := w.begin(in)
		if err := RunReparent(run); err == nil {
			t.Fatal("window 10 must fail prepare")
		}
		if err := os.Remove(hook); err != nil {
			t.Fatal(err)
		}
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("window 10 abort: %v", err)
		}
		if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != before {
			t.Fatal("window 10 abort did not preserve pre-image refs")
		}
		if _, err := AbortReparent(in); err == nil {
			t.Fatal("a second window 10 abort must find no run")
		}
	}(t)

	// Window 11, abort arm.
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		pr2Before, pr3Before := w.Repo.RevParse("pr2"), w.Repo.RevParse("pr3")
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "before-cas" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run: %v", err)
		}
		ReparentStepHook = nil
		st := LoadReparentState(w.Loc).State
		w.Repo.Git("update-ref", "refs/heads/pr2", st.Row("pr2").PlannedNewSHA)
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("window 11 abort: %v", err)
		}
		if w.Repo.RevParse("pr2") != pr2Before || w.Repo.RevParse("pr3") != pr3Before {
			t.Fatal("window 11 abort did not restore both refs")
		}
		if _, err := AbortReparent(in); err == nil {
			t.Fatal("a second window 11 abort must find no run")
		}
	}(t)
}

// Window 11: the files backend committed a SUBSET of the transaction. The
// recovery must classify every ref, re-issue only the rows whose update did
// not land, and complete forward — which is impossible if the recovery
// transaction re-sends `update <ref> <planned> <preimage>` for a row that
// already holds its planned tip, because that expected old value is stale and
// aborts the whole transaction during prepare.

// Window 17: the operator advanced an affected branch AFTER the commit point.
// Both verbs complete forward, report the movement, and move no ref.
func TestReparentCrashWindow17OperatorAdvancedAfterCommitPoint(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-064", "postcommit-operator-advance")
	_ = "asserts AC-076"
	for _, verb := range []string{"continue", "abort"} {
		t.Run(verb, func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			in, _ := w.approvedInput()

			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "after-write" {
					return errSimulatedCrash
				}
				return nil
			}
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("run error = %v", err)
			}
			ReparentStepHook = nil

			// The operator commits on top of the new pr3 tip.
			holder := filepath.Join(canonicalize(t.TempDir()), "work")
			w.Repo.Git("worktree", "add", "-q", holder, "pr3")
			if err := os.WriteFile(filepath.Join(holder, "later.txt"), []byte("later\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitInTest(t, holder, "add", "later.txt")
			gitInTest(t, holder, "commit", "-q", "-m", "operator work")
			advanced := strings.ToLower(strings.TrimSpace(gitInTest(t, holder, "rev-parse", "HEAD")))
			w.Repo.Git("worktree", "remove", "--force", holder)

			var prose strings.Builder
			in.Writers = ReparentWriters{Prose: &prose}
			var finished *ReparentRun
			var err error
			if verb == "continue" {
				finished, err = ContinueReparent(in)
			} else {
				finished, err = AbortReparent(in)
			}
			if err != nil {
				t.Fatalf("%s after the commit point: %v", verb, err)
			}
			if !finished.State.CommitPointReached {
				t.Fatal("a descendant of the planned tip is sufficient evidence that it landed")
			}
			if got := w.Repo.RevParse("pr3"); got != advanced {
				t.Fatalf("pr3 = %s, want the operator's own work %s: neither verb may move it", got, advanced)
			}
			if !strings.Contains(prose.String(), "advanced past its planned tip") {
				t.Fatalf("the later movement must be reported informationally: %q", prose.String())
			}
			assertReparentResidueRemoved(t, w, finished.State.RunID)
			if verb == "continue" {
				_, err = ContinueReparent(in)
			} else {
				_, err = AbortReparent(in)
			}
			if err == nil {
				t.Fatalf("second %s must find no remaining run", verb)
			}
			if got := w.Repo.RevParse("pr3"); got != advanced {
				t.Fatalf("second %s moved operator work to %s", verb, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// §8.5 — the post-lock re-snapshot measures NEW facts, not pre-lock ones.
// ---------------------------------------------------------------------------

func TestBeginReparentRunRefusesFactsThatAroseAfterThePlan(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-055", "post-lock-fact-revalidation")
	_ = "asserts AC-067"
	for _, tc := range []struct {
		name  string
		arise func(t *testing.T, w *reparentWorkspace, in *ReparentPlanInput)
		want  ReparentRefusalKind
	}{
		{
			name: "a session started inside the window",
			arise: func(t *testing.T, w *reparentWorkspace, in *ReparentPlanInput) {
				// The plan above was built with no probe and was clean; the
				// session starts now, inside the window, so every probe from
				// here on — including the post-lock re-snapshot's — sees it.
				in.SessionProbe = func([]string) ([]string, error) { return []string{"pr3"}, nil }
			},
			want: ReparentRefusalSessionLive,
		},
		{
			name: "the worktree went dirty inside the window",
			arise: func(t *testing.T, w *reparentWorkspace, in *ReparentPlanInput) {
				if err := os.WriteFile(filepath.Join(w.Repo.Dir, "one.txt"), []byte("edited\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: ReparentRefusalContextDirty,
		},
		{
			name: "a rebase started inside the window",
			arise: func(t *testing.T, w *reparentWorkspace, in *ReparentPlanInput) {
				dir := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir"))
				if err := os.MkdirAll(filepath.Join(dir, "rebase-merge"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: ReparentRefusalGitOperationInProgress,
		},
		{
			name: "a second worktree claimed an affected branch inside the window",
			arise: func(t *testing.T, w *reparentWorkspace, in *ReparentPlanInput) {
				first := filepath.Join(canonicalize(t.TempDir()), "one")
				w.Repo.Git("worktree", "add", "-q", "--detach", first, "pr3")
				gitInTest(t, first, "switch", "-q", "--ignore-other-worktrees", "pr3")
				second := filepath.Join(canonicalize(t.TempDir()), "two")
				w.Repo.Git("worktree", "add", "-q", "--detach", second, "pr3")
				gitInTest(t, second, "switch", "-q", "--ignore-other-worktrees", "pr3")
			},
			want: ReparentRefusalHolderUnsafe,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			in, _ := w.approvedInput()
			plan, req, err := PlanReparent(in)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Runnable {
				t.Fatalf("the pre-lock plan must be clean: %+v", plan.Blockers)
			}

			tc.arise(t, w, &in)

			_, err = BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req})
			var refusal *ReparentRefusalError
			if !asReparentRefusal(err, &refusal) || refusal.Kind != tc.want {
				t.Fatalf("begin = %v, want %s", err, tc.want)
			}
			// Nothing is left behind: no artifact, no compatibility file, and
			// the shared lock is released.
			if LoadReparentState(w.Loc).Kind != ReparentStateAbsent {
				t.Fatal("a refused begin leaves no state artifact")
			}
			if _, statErr := os.Stat(SyncRunStatePath(w.Loc.FeaturePath)); !os.IsNotExist(statErr) {
				t.Fatal("a refused begin writes no compatibility payload")
			}
			if _, statErr := os.Stat(SyncRunGuardPath(w.Loc.FeaturePath)); !os.IsNotExist(statErr) {
				t.Fatal("a refused begin releases the lock")
			}
		})
	}

	// Holder identity is part of the approved snapshot even when branch and
	// HEAD are unchanged.
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		oldPath := filepath.Join(canonicalize(t.TempDir()), "holder-old")
		newPath := filepath.Join(canonicalize(t.TempDir()), "holder-new")
		w.Repo.Git("worktree", "add", "-q", oldPath, "pr3")
		in, plan := w.approvedInput()
		_, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("worktree", "move", oldPath, newPath)
		movedPlan, _, err := PlanReparent(in)
		if err != nil || movedPlan.Approval.Fingerprint == nil ||
			*movedPlan.Approval.Fingerprint == *plan.Approval.Fingerprint {
			t.Fatalf("holder relocation did not change the approval fingerprint: %v", err)
		}
		_, err = BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req})
		var guard *PlanGuardRefusalError
		if !errors.As(err, &guard) || guard.Kind != string(RefusalRevalidationMismatch) ||
			!strings.Contains(guard.Detail, "holder identity changed") {
			t.Fatalf("holder relocation = %v", err)
		}
		if HasReparentState(w.Loc) {
			t.Fatal("holder identity mismatch wrote state")
		}
	}(t)

	// A symlink retarget to an equivalent clone keeps refs/stack bytes equal
	// but changes the canonical repository and common-dir identity.
	func(t *testing.T) {
		w, root := newReparentSecondaryRepoWorkspace(t)
		linkDir := canonicalize(t.TempDir())
		link := filepath.Join(linkDir, "repo-link")
		if err := os.Symlink(w.Repo.Dir, link); err != nil {
			t.Fatal(err)
		}
		stack := w.loadStack()
		for i := range stack.Branches {
			if stack.Branches[i].Name != "stranger" {
				stack.Branches[i].Repo = link
			}
		}
		if err := SaveStack(w.Loc.FeaturePath, stack); err != nil {
			t.Fatal(err)
		}
		limit := 50
		in := w.secondaryPlanInput(root.Dir)
		in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
		plan, req, err := PlanReparent(in)
		if err != nil || plan.Approval.Fingerprint == nil {
			t.Fatalf("symlink plan = %v %+v", err, plan.Blockers)
		}
		in.Invocation = ReparentInvocationExecute
		in.Guard.Approve = *plan.Approval.Fingerprint

		cloneDir := canonicalize(t.TempDir())
		clone := reparentTestGitCommand(t, "", "clone", "-q", w.Repo.Dir, cloneDir)
		if out, err := clone.CombinedOutput(); err != nil {
			t.Fatalf("clone equivalent repository: %v\n%s", err, out)
		}
		cloneRepo := &reparentRepo{t: t, Dir: cloneDir}
		for _, branch := range []string{"pr1", "pr2", "pr3"} {
			cloneRepo.Git("branch", branch, "origin/"+branch)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(cloneDir, link); err != nil {
			t.Fatal(err)
		}
		retargetedPlan, _, err := PlanReparent(in)
		if err != nil || retargetedPlan.Approval.Fingerprint == nil ||
			*retargetedPlan.Approval.Fingerprint == *plan.Approval.Fingerprint {
			t.Fatalf("repository retarget did not change the approval fingerprint: %v", err)
		}
		_, err = BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req})
		var guard *PlanGuardRefusalError
		if !errors.As(err, &guard) || guard.Kind != string(RefusalRevalidationMismatch) ||
			(!strings.Contains(guard.Detail, "target repository changed") &&
				!strings.Contains(guard.Detail, "repository/common-dir identity changed")) {
			t.Fatalf("symlink retarget = %v", err)
		}
		if HasReparentState(w.Loc) {
			t.Fatal("repository identity mismatch wrote state")
		}
	}(t)

	for _, verb := range []string{"abort", "continue"} {
		func(t *testing.T) {
			w := newReparentNoopWorkspace(t)
			limit := 50
			in := w.planInput()
			in.Target = "leaf"
			in.OntoToken = "base"
			in.OntoKind = "ref"
			in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
			plan, _, err := PlanReparent(in)
			if err != nil || plan.Approval.Fingerprint == nil {
				t.Fatalf("plan: %v %+v", err, plan.Blockers)
			}
			in.Invocation = ReparentInvocationExecute
			in.Guard.Approve = *plan.Approval.Fingerprint
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "before-cas" {
					return errSimulatedCrash
				}
				return nil
			}
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("run = %v", err)
			}
			ReparentStepHook = nil
			st := LoadReparentState(w.Loc).State
			if st.CASTransactionSucceeded {
				t.Fatal("the verify transaction has not run")
			}
			post, err := base64.StdEncoding.DecodeString(st.StackAfterBase64)
			if err != nil {
				t.Fatal(err)
			}
			if err := WriteStackBytesAtomic(w.Loc.FeaturePath, post); err != nil {
				t.Fatal(err)
			}
			if verb == "abort" {
				if _, err := AbortReparent(in); err != nil {
					t.Fatalf("abort: %v", err)
				}
				live, _ := os.ReadFile(StackPath(w.Loc.FeaturePath))
				if hex.EncodeToString(sliceSHA256(live)) != st.StackSHA256Before {
					t.Fatal("forged post-image must not turn an unverified all-noop run into a committed one")
				}
			} else {
				finished, err := ContinueReparent(in)
				if err != nil {
					t.Fatalf("continue: %v", err)
				}
				if !finished.State.CASTransactionSucceeded || !finished.State.CommitPointReached {
					t.Fatal("recovery must run the all-noop verify transaction before committing")
				}
			}
		}(t)
	}
}

// ---------------------------------------------------------------------------
// §8.3 — the just-in-time revalidation seam.
// ---------------------------------------------------------------------------

// The just-in-time seam: an approved plan whose inputs drifted, and the frozen limits enforced against freshly resolved counts (AC-067).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentJITRevalidation(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-043", "jit-destination-and-limit-revalidation")
	_ = "asserts AC-057"
	// --- TestReparentJITRevalidationRefusesDriftBeforeComputing ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()

		// Approve, take the lock, then let the closure drift before the first row
		// is computed. The fingerprint was already accepted, so only the JIT seam
		// can catch this.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "pins-written" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil

		// One more commit on pr3 changes that row's candidate inputs.
		w.Repo.Git("switch", "-q", "pr3")
		w.Repo.Commit("drift.txt", "drift")
		w.Repo.Git("switch", "-q", "main")

		resumePlan, err := PlanReparentContinue(in)
		if err != nil {
			t.Fatal(err)
		}
		if !resumePlan.Guard.WouldRefuse || resumePlan.Runnable || resumePlan.Approval.Usable {
			t.Fatalf("continue plan must publish the pending-row JIT refusal: %+v", resumePlan.Guard)
		}
		foundRevalidation := false
		for _, evaluation := range resumePlan.Guard.Evaluation {
			if evaluation.UnknownKind != nil && *evaluation.UnknownKind == string(RefusalRevalidationMismatch) {
				foundRevalidation = true
			}
		}
		if !foundRevalidation {
			t.Fatalf("continue plan guard evaluation = %+v", resumePlan.Guard.Evaluation)
		}

		_, err = ContinueReparent(in)
		guardErr, ok := err.(*PlanGuardRefusalError)
		if !ok {
			t.Fatalf("drift = %v (%T)", err, err)
		}
		if guardErr.Kind != string(RefusalRevalidationMismatch) {
			t.Fatalf("kind = %q", guardErr.Kind)
		}
		if !guardErr.StatePreserved {
			t.Fatal("the run stays resumable and reversible, so the refusal is state-preserved")
		}
		failed := LoadReparentState(w.Loc)
		if failed.Kind != ReparentStateOK || failed.State.Stage != ReparentStageFailed ||
			failed.State.ResumeStage != ReparentStageComputing ||
			failed.State.FailureDomain != ReparentFailureDomainPlanGuard ||
			failed.State.FailureKind != guardErr.Kind ||
			failed.State.FailureDetail != guardErr.Detail {
			t.Fatalf("JIT refusal was not durably attributed to plan-guard: %+v", failed)
		}
		// The drift is itself an operator ref move, so an --abort refuses
		// abort-foreign-value rather than erasing that work (§11.9 window 16)...
		_, abortErr := AbortReparent(in)
		var abortRefusal *ReparentRefusalError
		if !asReparentRefusal(abortErr, &abortRefusal) || abortRefusal.Kind != ReparentRefusalAbortForeignValue {
			t.Fatalf("abort with a moved branch = %v", abortErr)
		}
		// ...and once the operator puts the branch back, the run is still fully
		// reversible: the JIT refusal preserved every pin and artifact.
		st := LoadReparentState(w.Loc).State
		w.Repo.Git("update-ref", "refs/heads/pr3", st.Row("pr3").PreimageSHA)
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("abort after the branch was restored: %v", err)
		}
	}(t)

	// --- TestReparentJITEnforcesLimitsAgainstFreshCounts ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)

		// A total limit of 2 admits the plan (pr2 and pr3 carry one commit each).
		total := 2
		in := w.planInput()
		in.Guard = CheckoutPlanGuard{MaxTotal: &total}
		plan, _, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Approval.Fingerprint == nil {
			t.Fatalf("plan minted no token: %+v", plan.Blockers)
		}
		in.Invocation = ReparentInvocationExecute
		in.Guard.Approve = *plan.Approval.Fingerprint

		// Pause before the rows are computed, then inflate a LATER row. Its
		// candidate count is re-measured by the seam, so the cumulative total the
		// run is actually about to replay exceeds the frozen limit.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "computed:pr2" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil

		w.Repo.Git("switch", "-q", "pr3")
		w.Repo.Commit("more-1.txt", "more 1")
		w.Repo.Commit("more-2.txt", "more 2")
		w.Repo.Git("switch", "-q", "main")

		_, err = ContinueReparent(in)
		guardErr, ok := err.(*PlanGuardRefusalError)
		if !ok {
			t.Fatalf("inflated closure = %v (%T)", err, err)
		}
		// Either term may fire first; both are the frozen limits enforced against
		// FRESHLY resolved counts, which is the property under test.
		if guardErr.Kind != string(RefusalLimitTotal) && guardErr.Kind != string(RefusalRevalidationMismatch) {
			t.Fatalf("kind = %q", guardErr.Kind)
		}
		if !guardErr.StatePreserved {
			t.Fatal("the refusal must preserve state")
		}
		failed := LoadReparentState(w.Loc)
		if failed.Kind != ReparentStateOK || failed.State.Stage != ReparentStageFailed ||
			failed.State.ResumeStage != ReparentStageComputing ||
			failed.State.FailureDomain != ReparentFailureDomainPlanGuard ||
			failed.State.FailureKind != guardErr.Kind ||
			failed.State.FailureDetail != guardErr.Detail {
			t.Fatalf("limit refusal was not durably attributed to plan-guard: %+v", failed)
		}
	}(t)

}

func TestReparentApprovedOverLimitExactPlanEndToEnd(t *testing.T) {
	approved := func(t *testing.T, w *reparentWorkspace) ReparentPlanInput {
		t.Helper()
		zero := 0
		in := w.planInput()
		in.Guard = CheckoutPlanGuard{
			MaxPerEntry: &zero,
			MaxTotal:    &zero,
			Present: map[string]bool{
				"max-replay-per-entry": true,
				"max-replay-total":     true,
			},
		}
		plan, _, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Guard.WouldRefuseWithoutApproval || plan.Approval.Fingerprint == nil {
			t.Fatalf("over-limit plan did not publish an approvable exact decision: %+v", plan.Guard)
		}
		in.Invocation = ReparentInvocationExecute
		in.Guard.Approve = *plan.Approval.Fingerprint
		return in
	}

	t.Run("fresh exact approval succeeds", func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in := approved(t, w)
		run := w.begin(in)
		if len(run.State.WaivedEvaluationIDs) == 0 ||
			!containsString(run.State.WaivedEvaluationIDs, "max_replay_total") ||
			!containsRefusalKind(run.State.WaivedKinds, RefusalLimitPerEntry) ||
			!containsRefusalKind(run.State.WaivedKinds, RefusalLimitTotal) {
			t.Fatalf("persisted waivers = ids:%v kinds:%v", run.State.WaivedEvaluationIDs, run.State.WaivedKinds)
		}
		if err := RunReparent(run); err != nil {
			t.Fatalf("fresh approved over-limit run: %v", err)
		}
		assertReparentSucceeded(t, w)
	})

	t.Run("resume exact approval succeeds", func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in := approved(t, w)
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "pins-written" {
				return errSimulatedCrash
			}
			return nil
		}
		t.Cleanup(func() { ReparentStepHook = nil })
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("pause exact approved run: %v", err)
		}
		ReparentStepHook = nil
		plan, err := PlanReparentContinue(in)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Guard.WouldRefuse || len(plan.Approval.Covers.WaivedEvaluationIDs) == 0 ||
			len(plan.Approval.Covers.WaivedKinds) == 0 {
			t.Fatalf("resume plan lost persisted waivers: guard=%+v approval=%+v", plan.Guard, plan.Approval)
		}
		if _, err := ContinueReparent(in); err != nil {
			t.Fatalf("resume approved over-limit run: %v", err)
		}
		assertReparentSucceeded(t, w)
	})

	t.Run("altered count refuses", func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in := approved(t, w)
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "pins-written" {
				return errSimulatedCrash
			}
			return nil
		}
		t.Cleanup(func() { ReparentStepHook = nil })
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("pause approved run: %v", err)
		}
		ReparentStepHook = nil
		st := LoadReparentState(w.Loc).State
		row := st.Row("pr2")
		tree := w.Repo.RevParse(row.PreimageSHA + "^{tree}")
		extra := w.Repo.Git("commit-tree", tree, "-p", row.PreimageSHA, "-m", "approved count drift")
		original := w.Repo.Git("rev-list", "--no-merges", "--reverse", row.CutoffSHA+"..refs/heads/"+row.GitBranch)
		reparentGitFaultHook = func(dir string, args []string) (reparentGitResult, error, bool) {
			if canonicalize(dir) == canonicalize(w.Repo.Dir) && len(args) >= 4 &&
				args[0] == "rev-list" && args[1] == "--no-merges" && args[2] == "--reverse" &&
				args[len(args)-1] == row.CutoffSHA+"..refs/heads/"+row.GitBranch {
				return reparentGitResult{Stdout: []byte(original + "\n" + extra + "\n"), ExitCode: 0}, nil, true
			}
			return reparentGitResult{}, nil, false
		}
		t.Cleanup(func() { reparentGitFaultHook = nil })
		_, err := ContinueReparent(in)
		reparentGitFaultHook = nil
		var guard *PlanGuardRefusalError
		if !errors.As(err, &guard) || guard.Kind != string(RefusalRevalidationMismatch) || !guard.StatePreserved {
			t.Fatalf("altered approved count = %v", err)
		}
	})
}

func containsRefusalKind(values []RefusalKind, want RefusalKind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestReparentSupportingSafetyRegressions carries regression assertions that
// support multiple cells but own no normative T row. Keeping them outside a
// matrix-owning function makes scenario accounting match the authoritative
// T -> function table.
func TestReparentSupportingSafetyRegressions(t *testing.T) {
	testReparentRequiredProbesFailClosed(t)
	testReparentPostLockApprovalBindsExactStackBytes(t)
	testReparentPostLockSyncStateRecheckExcludesOnlyOwnLock(t)
	testReparentRecoveryRefusesForeignCompatibilityBeforeMutation(t)
	testReparentJITRechecksDestinationAncestryAndMerges(t)
	testReparentSymbolicAffectedRefsNeverReachCAS(t)
	testReparentComputedAndPinnedRowsBindHEADAndPin(t)
	testReparentHolderSafetyIsRecheckedImmediatelyBeforeCAS(t)
	testReparentAllNoopCASRequiresPersistedSuccess(t)
	testReparentCheckoutComputationDetachTarget(t)
	testReparentCASNoDerefRace(t)
	testReparentAbortHolderRestoreDefersCleanup(t)
	testReparentCheckoutRejectsRepoFields(t)
	testReparentRecoveryClearOrderingAndRemoteProof(t)
	testReparentCheckoutIdentityProbeFailures(t)
	testReparentScratchCleanupFailuresPreserveRecovery(t)
	testReparentFreshRefusesWindowOneState(t)
	testReparentPostRenameDurabilityRecovery(t)
	testReparentOriginalDetachedHeadPin(t)
}

// ---------------------------------------------------------------------------
// §9.7 — a native `git rebase --abort` can never become a false success.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// §12.3 — an all-cleared record is never left on disk.
// ---------------------------------------------------------------------------

// The record's write point, from this side of the boundary: written before the CAS when a branch is published, never left behind when every entry is cleared (AC-088).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentRemoteRecordTiming(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-076", "remote-record-before-cas")
	t.Cleanup(func() {
		ReparentStepHook = nil
		SyncStateIOFault = nil
	})
	// --- TestReparentRemoteRecordAllClearedIsNotWritten ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()

		// No branch in this fixture was ever published, so every entry is written
		// cleared and there is nothing to keep.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "record-written" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil

		st := LoadReparentState(w.Loc).State
		if st.RemoteRecordWritten {
			t.Fatal("an all-cleared record is not written")
		}
		if !st.RemoteRecordEmpty {
			t.Fatal("the run must persist the provably-empty proof the CAS gate needs")
		}
		if len(st.RemoteFollowupEntries) != 0 {
			t.Fatalf("nothing was recorded, so there is nothing to clean up: %v", st.RemoteFollowupEntries)
		}
		if _, err := os.Stat(ReparentRemoteRecordPath(w.Loc)); !os.IsNotExist(err) {
			t.Fatal("no permanent all-cleared record file may be left behind")
		}
		plan, err := PlanReparentContinue(in)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range plan.Remote.Rows {
			if row.Divergence != "no-upstream" {
				t.Fatalf("cleared missing tracking row %s divergence = %q", row.Name, row.Divergence)
			}
		}
		for _, line := range plan.Remote.Guidance {
			if strings.Contains(line, "( -> )") {
				t.Fatalf("cleared resumed guidance contains empty PR bases: %q", line)
			}
		}

		// The run still completes: the gate accepts the empty proof.
		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue: %v", err)
		}
		if resumed.State.Stage != ReparentStageCompleted {
			t.Fatalf("stage = %q", resumed.State.Stage)
		}
		if _, err := os.Stat(ReparentRemoteRecordPath(w.Loc)); !os.IsNotExist(err) {
			t.Fatal("a completed all-cleared run leaves no record")
		}
	}(t)

	// --- TestReparentRemoteRecordWrittenWhenAPublishedBranchExists ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		// pr2 is published; pr3 is not. The record must be written, with one
		// pending row and one cleared row.
		w.Repo.Git("update-ref", "refs/remotes/origin/pr2", w.Repo.RevParse("pr2"))

		in, _ := w.approvedInput()
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "record-written" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run error = %v", err)
		}
		ReparentStepHook = nil

		st := LoadReparentState(w.Loc).State
		if !st.RemoteRecordWritten || st.RemoteRecordEmpty {
			t.Fatalf("a published branch must produce a durable record: written=%v empty=%v",
				st.RemoteRecordWritten, st.RemoteRecordEmpty)
		}
		rec, err := LoadReparentRemoteRecord(w.Loc)
		if err != nil || rec == nil {
			t.Fatalf("record = %v (%v)", rec, err)
		}
		pending := rec.PendingEntries()
		if len(pending) != 1 || pending[0] != "pr2" {
			t.Fatalf("pending entries = %v", pending)
		}
		pr3, ok := rec.Entry("pr3")
		if !ok || pr3.Pending() {
			t.Fatalf("an unpublished branch is written cleared: %+v", pr3)
		}
		if pr3.PRBaseBefore != "pr2" || pr3.PRBaseAfter != "pr2" {
			t.Fatalf("descendant PR base must remain its parent Git branch: %+v", pr3)
		}
		if pr2, _ := rec.Entry("pr2"); pr2.PRBaseBefore != "pr1" || pr2.PRBaseAfter != "main" {
			t.Fatalf("the target's PR base change = %+v", pr2)
		}

		// Commit the run and crash after cleanup released the mutation lock but
		// before authoritative state deletion.
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "artifacts-removed" {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := ContinueReparent(in); err != errSimulatedCrash {
			t.Fatalf("pause after commit point: %v", err)
		}
		ReparentStepHook = nil
		st = LoadReparentState(w.Loc).State
		if st == nil || !st.CommitPointReached {
			t.Fatalf("post-commit clear fixture did not reach the commit point: %+v", st)
		}
		if _, err := os.Lstat(SyncRunGuardPath(w.Loc.FeaturePath)); !os.IsNotExist(err) {
			t.Fatalf("cleanup crash retained the mutation lock: %v", err)
		}
		w.Repo.Git("update-ref", "refs/remotes/origin/pr2", st.Row("pr2").PlannedNewSHA)
		stack := w.loadStack()
		if _, err := ApplyReparentRemoteClears(w.Loc, st.WorkspaceRepoRoot, &stack); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(ReparentRemoteRecordPath(w.Loc)); !os.IsNotExist(err) {
			t.Fatal("push clear did not delete the final remote record")
		}
		if _, err := ContinueReparent(in); err != nil {
			t.Fatalf("cleanup after post-lock push clear: %v", err)
		}
		for _, verb := range []string{"continue", "abort"} {
			var err error
			if verb == "continue" {
				_, err = ContinueReparent(in)
			} else {
				_, err = AbortReparent(in)
			}
			if err == nil || HasReparentState(w.Loc) {
				t.Fatalf("repeated %s wedged cleanup residue: err=%v", verb, err)
			}
		}
	}(t)
}

func TestReparentPostLockCleanupKeepsDisjointPendingRemoteRows(t *testing.T) {
	for _, verb := range []string{"continue", "abort"} {
		t.Run(verb, func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			w.Repo.Git("branch", "pr9", "main")
			stackWithPrior := w.loadStack()
			stackWithPrior.Branches = append(stackWithPrior.Branches, StackEntry{
				Name: "pr9", Base: "refs/heads/main", LastBaseSHA: w.Repo.RevParse("main"),
			})
			if err := SaveStack(w.Loc.FeaturePath, stackWithPrior); err != nil {
				t.Fatal(err)
			}
			prior := ReparentRemoteRecord{
				RecordVersion: ReparentRemoteRecordVersion,
				Feature:       w.Loc.Feature,
				RunID:         strings.Repeat("9", 32),
				CreatedAt:     reparentNow(),
				Entries: []ReparentRemoteEntry{{
					Name:             "pr9",
					GitBranch:        "pr9",
					Remote:           ReparentRemoteName,
					RemoteRef:        "refs/remotes/origin/pr9",
					NewTipSHA:        w.Repo.RevParse("main"),
					RemoteSHAAtWrite: w.Repo.RevParse("pr1"),
					PRBaseBefore:     "main",
					PRBaseAfter:      "main",
					State:            ReparentRemoteStatePending,
				}},
			}
			if err := SaveReparentRemoteRecord(w.Loc, prior); err != nil {
				t.Fatal(err)
			}
			w.Repo.Git("update-ref", "refs/remotes/origin/pr2", w.Repo.RevParse("pr2"))
			w.Repo.Git("update-ref", "refs/remotes/origin/pr3", w.Repo.RevParse("pr3"))

			in, _ := w.approvedInput()
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "artifacts-removed" {
					return errSimulatedCrash
				}
				return nil
			}
			t.Cleanup(func() { ReparentStepHook = nil })
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("cleanup crash = %v", err)
			}
			ReparentStepHook = nil

			st := LoadReparentState(w.Loc).State
			if st == nil || !st.CommitPointReached {
				t.Fatalf("cleanup residue is not post-commit: %+v", st)
			}
			for _, name := range []string{"pr2", "pr3"} {
				row := st.Row(name)
				w.Repo.Git("update-ref", "refs/remotes/origin/"+row.GitBranch, row.PlannedNewSHA)
			}
			stack := w.loadStack()
			if _, err := ApplyReparentRemoteClears(w.Loc, st.WorkspaceRepoRoot, &stack); err != nil {
				t.Fatal(err)
			}
			rec, err := LoadReparentRemoteRecord(w.Loc)
			if err != nil || rec == nil {
				t.Fatalf("retained record = %+v (%v)", rec, err)
			}
			pr9Before, ok := rec.Entry("pr9")
			if !ok || !pr9Before.Pending() {
				t.Fatalf("disjoint pending row was lost before recovery: %+v", rec.Entries)
			}
			for _, name := range []string{"pr2", "pr3"} {
				entry, ok := rec.Entry(name)
				if !ok || entry.Pending() {
					t.Fatalf("current-run row %s was not durably cleared: %+v", name, rec.Entries)
				}
			}
			recordBefore, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
			if err != nil {
				t.Fatal(err)
			}

			if verb == "continue" {
				_, err = ContinueReparent(in)
			} else {
				_, err = AbortReparent(in)
			}
			if err != nil {
				t.Fatalf("%s post-lock cleanup: %v", verb, err)
			}
			assertReparentResidueRemoved(t, w, st.RunID)
			recordAfter, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
			if err != nil {
				t.Fatalf("disjoint record was removed: %v", err)
			}
			if !bytes.Equal(recordAfter, recordBefore) {
				t.Fatalf("%s rewrote the retained disjoint record:\n--- before ---\n%s\n--- after ---\n%s",
					verb, recordBefore, recordAfter)
			}
			after, err := LoadReparentRemoteRecord(w.Loc)
			if err != nil || after == nil {
				t.Fatalf("load retained record: %+v (%v)", after, err)
			}
			pr9After, ok := after.Entry("pr9")
			if !ok || pr9After != pr9Before {
				t.Fatalf("disjoint pr9 changed: before=%+v after=%+v", pr9Before, pr9After)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// state.files — the five shipped facts are measured, not zeroed.
// ---------------------------------------------------------------------------

// state.files is measured, not zeroed, in both modes (AC-041).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentStateFileFacts(t *testing.T) {
	// --- TestReparentPlanPublishesMeasuredSyncFileFacts ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		run := w.begin(in)

		plan, _, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		files := plan.State.Files
		if files.ExternalRunPayload.Presence != "readable" {
			t.Fatalf("the v4 payload this run wrote must read as present: %+v", files.ExternalRunPayload)
		}
		if files.ExternalRunPayload.Feature == nil || *files.ExternalRunPayload.Feature != "customer" {
			t.Fatalf("payload feature = %v", files.ExternalRunPayload.Feature)
		}
		if len(files.ExternalRunPayload.Selected) != 2 {
			t.Fatalf("payload selected = %v", files.ExternalRunPayload.Selected)
		}
		if files.ExternalLegacyState.Presence != "readable" {
			t.Fatalf("legacy sentinel = %+v", files.ExternalLegacyState)
		}
		if files.ExternalRunGuard.Presence != "readable" || files.ExternalRunGuard.OwnerPID == nil {
			t.Fatalf("run guard = %+v", files.ExternalRunGuard)
		}
		if files.ExternalRunGuard.OwnerLive == nil || !*files.ExternalRunGuard.OwnerLive {
			t.Fatal("this process owns the guard, so it is live")
		}
		// Checkout-only rows are not applicable to an external run and are never
		// invented.
		if files.CheckoutTransaction.Applicable || files.CheckoutLock.Applicable {
			t.Fatalf("checkout facts must not apply to an external run: %+v", files)
		}
		if files.ReparentState.Presence != "readable" || files.ReparentState.RunID == nil {
			t.Fatalf("reparent state fact = %+v", files.ReparentState)
		}
		if files.ReparentState.Stage == nil || *files.ReparentState.Stage != string(run.State.Stage) {
			t.Fatalf("reparent state stage = %v", files.ReparentState.Stage)
		}

		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("abort: %v", err)
		}
	}(t)

	// --- TestReparentPlanPublishesMeasuredCheckoutFileFacts ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeCheckout)
		in, _ := w.approvedInput()
		run := w.begin(in)

		plan, _, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		files := plan.State.Files
		// The compatibility transaction is deliberately undecodable, and this
		// surface must still report it as READABLE state that belongs to this
		// feature — never as corrupt sync state an operator should remove.
		if files.CheckoutTransaction.Presence != "readable" {
			t.Fatalf("checkout transaction = %+v", files.CheckoutTransaction)
		}
		if files.CheckoutTransaction.UnreadableReason != nil {
			t.Fatalf("the run's own guard is not corrupt state: %v", *files.CheckoutTransaction.UnreadableReason)
		}
		if files.CheckoutTransaction.StateVersion != nil {
			t.Fatal("a string state_version has no int to publish")
		}
		if files.CheckoutTransaction.Feature == nil || *files.CheckoutTransaction.Feature != "customer" {
			t.Fatalf("transaction feature = %v", files.CheckoutTransaction.Feature)
		}
		if files.CheckoutLock.Presence != "readable" || files.CheckoutLock.OwnerPID == nil {
			t.Fatalf("checkout lock = %+v", files.CheckoutLock)
		}
		if files.ExternalRunPayload.Applicable || files.ExternalRunGuard.Applicable {
			t.Fatalf("external facts must not apply to a checkout run: %+v", files)
		}

		if err := RunReparent(run); err != nil {
			t.Fatalf("run: %v", err)
		}
	}(t)
}

// ---------------------------------------------------------------------------
// §11.9 — the complete crash-window ledger.
//
// The map holds the ACTUAL test functions, so a window whose test is renamed
// or deleted is a compile error rather than a silently missing row, and the
// ledger test proves all 17 windows are claimed. Several windows share the
// table-driven test that walks the linear ladder.
// ---------------------------------------------------------------------------

var reparentCrashWindowCoverage = map[int]func(*testing.T){
	1:  TestReparentCrashWindowMatrixEveryVerbTwice,
	2:  TestReparentCrashWindowMatrixEveryVerbTwice,
	3:  TestReparentCrashWindowMatrixEveryVerbTwice,
	4:  TestReparentCrashWindowMatrixEveryVerbTwice,
	5:  TestReparentCrashWindowMatrixEveryVerbTwice,
	6:  TestReparentCrashWindowMatrixEveryVerbTwice,
	7:  TestReparentCrashWindowMatrixEveryVerbTwice,
	8:  TestReparentCrashWindowMatrixEveryVerbTwice,
	9:  TestReparentCrashWindowMatrixEveryVerbTwice,
	10: TestReparentCrashWindowMatrixEveryVerbTwice,
	11: TestReparentCrashWindowMatrixEveryVerbTwice,
	12: TestReparentCrashWindowMatrixEveryVerbTwice,
	13: TestReparentCrashWindowMatrixEveryVerbTwice,
	14: TestReparentCrashWindowMatrixEveryVerbTwice,
	15: TestReparentCrashWindowMatrixEveryVerbTwice,
	16: TestReparentCrashWindowMatrixEveryVerbTwice,
	17: TestReparentCrashWindowMatrixEveryVerbTwice,
}

// reparentCrashWindowAbortArm records the windows whose --abort behaviour is
// exercised separately from their --continue behaviour.
var reparentCrashWindowAbortArm = map[int]func(*testing.T){
	1:  TestReparentCrashWindowMatrixEveryVerbTwice,
	2:  TestReparentCrashWindowMatrixEveryVerbTwice,
	3:  TestReparentCrashWindowMatrixEveryVerbTwice,
	4:  TestReparentCrashWindowMatrixEveryVerbTwice,
	5:  TestReparentCrashWindowMatrixEveryVerbTwice,
	6:  TestReparentCrashWindowMatrixEveryVerbTwice,
	7:  TestReparentCrashWindowMatrixEveryVerbTwice,
	8:  TestReparentCrashWindowMatrixEveryVerbTwice,
	9:  TestReparentCrashWindowMatrixEveryVerbTwice,
	10: TestReparentCrashWindowMatrixEveryVerbTwice,
	11: TestReparentCrashWindowMatrixEveryVerbTwice,
	12: TestReparentCrashWindowMatrixEveryVerbTwice,
	13: TestReparentCrashWindowMatrixEveryVerbTwice,
	14: TestReparentCrashWindowMatrixEveryVerbTwice,
	15: TestReparentCrashWindowMatrixEveryVerbTwice,
	16: TestReparentCrashWindowMatrixEveryVerbTwice,
	17: TestReparentCrashWindowMatrixEveryVerbTwice,
}

// reparentCrashWindowStateSetup records the concrete on-disk/live-ref shape
// asserted by each owner. It prevents a function pointer from claiming a
// window while exercising a neighboring hook or transaction state.
var reparentCrashWindowStateSetup = map[int]string{
	1:  "initializing; authoritative state present; compatibility absent; context absent",
	2:  "initializing; compatibility complete; context absent; every row pending",
	3:  "preflight; scratch context present; old and destination pins absent",
	4:  "computing or conflict-paused; native rebase state present",
	5:  "computing; first row computed; its new pin absent",
	6:  "computing; first row pinned; validation pending",
	7:  "computing; every row validated; post-image absent",
	8:  "building-post-image; post-image durable; remote record not written",
	9:  "writing-remote-record; record result durable; CAS not attempted",
	10: "committing-refs; transaction prepare vetoed; every ref at preimage",
	11: "committing-refs; a strict subset of refs at planned tips",
	12: "committing-refs; CAS success durable; refs planned; metadata preimage",
	13: "writing-metadata; metadata post-image; holder restoration pending",
	14: "restoring-holders; holders restored; cleanup not entered",
	15: "aborting; abort_rows records a restored row; another ref remains planned",
	16: "precommit; at least one affected ref is foreign",
	17: "postcommit; at least one affected ref advanced beyond planned",
}

func TestReparentCrashWindowLedgerIsComplete(t *testing.T) {
	_ = "asserts AC-074"
	if len(reparentCrashWindowCoverage) != 17 {
		t.Fatalf("the ledger claims %d windows; §11.9 has exactly 17", len(reparentCrashWindowCoverage))
	}
	for window := 1; window <= 17; window++ {
		if reparentCrashWindowCoverage[window] == nil {
			t.Fatalf("crash window %d has no --continue coverage", window)
		}
	}
	for window := range reparentCrashWindowCoverage {
		if window < 1 || window > 17 {
			t.Fatalf("window %d is outside §11.9's closed matrix", window)
		}
	}
	if len(reparentCrashWindowAbortArm) != 17 {
		t.Fatalf("the abort ledger claims %d windows; §11.9 has exactly 17", len(reparentCrashWindowAbortArm))
	}
	for window := 1; window <= 17; window++ {
		if reparentCrashWindowAbortArm[window] == nil {
			t.Fatalf("crash window %d has no --abort coverage", window)
		}
	}
	for window := range reparentCrashWindowAbortArm {
		if window < 1 || window > 17 {
			t.Fatalf("abort window %d is outside §11.9's closed matrix", window)
		}
	}
	if len(reparentCrashWindowStateSetup) != 17 {
		t.Fatalf("the setup ledger describes %d windows; §11.9 has exactly 17", len(reparentCrashWindowStateSetup))
	}
	for window := 1; window <= 17; window++ {
		if strings.TrimSpace(reparentCrashWindowStateSetup[window]) == "" {
			t.Fatalf("crash window %d has no concrete state setup", window)
		}
	}
	if got := reparentCrashWindowStateSetup[2]; got !=
		"initializing; compatibility complete; context absent; every row pending" {
		t.Fatalf("window 2 setup drifted to a non-normative state: %q", got)
	}
	if got := reparentCrashWindowStateSetup[15]; got !=
		"aborting; abort_rows records a restored row; another ref remains planned" {
		t.Fatalf("window 15 setup does not represent a partial rollback: %q", got)
	}
}

// A genuine decision drift — the closure moved, but nothing became unsafe —
// still refuses revalidation-mismatch, which is what makes the approved token
// a statement about the state the run actually mutates.

// ---------------------------------------------------------------------------
// §11.8a — the commit-point gate. From writing-metadata onward, NOTHING is
// written, restored or cleaned up unless the conjunction actually holds.
// ---------------------------------------------------------------------------

// reparentStallAt crashes the run at one step and returns the persisted state.
func reparentStallAt(t *testing.T, w *reparentWorkspace, in ReparentPlanInput, step string) *ReparentState {
	t.Helper()
	ReparentStepHook = func(stage ReparentStage, s string) error {
		if s == step {
			return errSimulatedCrash
		}
		return nil
	}
	run := w.begin(in)
	if err := RunReparent(run); err != errSimulatedCrash {
		ReparentStepHook = nil
		t.Fatalf("run error = %v", err)
	}
	ReparentStepHook = nil
	load := LoadReparentState(w.Loc)
	if load.Kind != ReparentStateOK {
		t.Fatalf("state after the stall = %+v", load)
	}
	return load.State
}

// A foreign ref at writing-metadata refuses and writes NOTHING: the metadata
// would otherwise publish a topology Git does not have, and its hash would
// then read as the commit point forever after.
// The commit-point gate's two verdicts at writing-metadata and its completion from metadata-written (AC-071, AC-075, AC-076).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentCommitGateAtWritingMetadata(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-059", "metadata-before-hash-gate")
	// --- TestReparentCommitGateRefusesForeignRefAtWritingMetadata ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "after-cas")
		stackBefore, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}

		// The operator lands their own commit on an affected branch.
		foreign := w.Repo.RevParse("main")
		w.Repo.Git("update-ref", "refs/heads/pr3", foreign)

		_, err = ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalRefForeignValue {
			t.Fatalf("continue = %v, want ref-foreign-value", err)
		}
		live, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}
		if string(live) != string(stackBefore) {
			t.Fatal("no metadata may be written while a ref is foreign")
		}
		if LoadReparentState(w.Loc).Kind != ReparentStateOK {
			t.Fatal("the run must be preserved, not deleted")
		}
		if w.Repo.RevParse("pr3") != foreign {
			t.Fatal("the operator's work is never overwritten")
		}
		if st.CommitPointReached {
			t.Fatal("fixture must stall before the commit point")
		}
	}(t)

	// --- TestReparentCommitGateFallsBackWhenARefIsStillPreImage ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "after-cas")

		// Put one row back at its pre-image, as a files-backend crash during
		// `commit` would.
		pr3 := st.Row("pr3")
		w.Repo.Git("update-ref", "refs/heads/pr3", pr3.PreimageSHA)

		var prose strings.Builder
		in.Writers = ReparentWriters{Prose: &prose}
		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue: %v", err)
		}
		if resumed.State.Stage != ReparentStageCompleted {
			t.Fatalf("stage = %q", resumed.State.Stage)
		}
		if w.Repo.RevParse("pr3") != pr3.PlannedNewSHA {
			t.Fatal("the fallback must complete the transaction forward")
		}
		assertReparentSucceeded(t, w)
	}(t)

	// --- TestReparentCommitGateCompletesPreImageAtMetadataWritten ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "after-cas")

		pr3 := st.Row("pr3")
		w.Repo.Git("update-ref", "refs/heads/pr3", pr3.PreimageSHA)
		st.Stage = ReparentStageMetadataWritten
		st.ResumeStage = ReparentStageMetadataWritten
		if err := SaveReparentState(w.Loc, st); err != nil {
			t.Fatal(err)
		}

		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue: %v", err)
		}
		if !resumed.State.CommitPointReached {
			t.Fatal("the conjunction must be established before completion")
		}
		if w.Repo.RevParse("pr3") != pr3.PlannedNewSHA {
			t.Fatal("the transaction must be completed forward")
		}
		assertReparentSucceeded(t, w)
	}(t)
}

// A pre-image ref at writing-metadata means the transaction did not fully
// land: the run falls back to committing-refs and completes it, rather than
// writing metadata for refs that never moved.

// The same gate protects metadata-written, restoring-holders and cleanup: a
// run that never reached the conjunction must not restore holders, must not
// delete its artifacts and must not report success.
func TestReparentCommitGateProtectsEveryPostMetadataStage(t *testing.T) {
	for _, stage := range []ReparentStage{
		ReparentStageMetadataWritten,
		ReparentStageRestoringHolders,
		ReparentStageCleanup,
		ReparentStageCompleted,
	} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			in, _ := w.approvedInput()
			st := reparentStallAt(t, w, in, "after-cas")

			// Forge exactly the wedge the review names: a stage that CLAIMS
			// the run is past the commit point while a ref is foreign and the
			// marker was never set.
			foreign := w.Repo.RevParse("main")
			w.Repo.Git("update-ref", "refs/heads/pr3", foreign)
			st.Stage = stage
			st.ResumeStage = stage
			st.CommitPointReached = false
			if err := SaveReparentState(w.Loc, st); err != nil {
				t.Fatal(err)
			}
			stackBefore, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
			if err != nil {
				t.Fatal(err)
			}

			_, err = ContinueReparent(in)
			var refusal *ReparentRefusalError
			want := ReparentRefusalStateCorrupt
			if stage == ReparentStageMetadataWritten {
				want = ReparentRefusalRefForeignValue
			}
			if !asReparentRefusal(err, &refusal) || refusal.Kind != want {
				t.Fatalf("continue at %s = %v, want %s", stage, err, want)
			}
			load := LoadReparentState(w.Loc)
			if stage == ReparentStageMetadataWritten && load.Kind != ReparentStateOK {
				t.Fatal("the recoverable artifact must survive: cleanup is gated on the commit point")
			}
			if stage != ReparentStageMetadataWritten && load.Kind != ReparentStateCorrupt {
				t.Fatalf("forged post-commit stage must remain corrupt, got %s", load.Kind)
			}
			if _, statErr := os.Stat(SyncRunStatePath(w.Loc.FeaturePath)); statErr != nil {
				t.Fatal("the compatibility envelope must survive too")
			}
			live, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
			if err != nil {
				t.Fatal(err)
			}
			if string(live) != string(stackBefore) {
				t.Fatal("no metadata may be written")
			}
			if w.Repo.RevParse("pr3") != foreign {
				t.Fatal("no ref may move")
			}
			pins := strings.TrimSpace(w.Repo.Git("for-each-ref", "--format=%(refname)", "refs/tws/reparent/**"))
			if pins == "" {
				t.Fatal("the pins must survive a refused recovery")
			}
		}(t)
	}
}

// A pre-image ref reached at metadata-written — the run crashed after the CAS
// and someone rolled a branch back — completes forward instead of cleaning up
// on the strength of a stage name.

// ---------------------------------------------------------------------------
// §11.6a step 2 — `--force` only after a PROVEN clean worktree.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// §11.8a — an unreadable stack.yaml can never be read as "before the commit
// point", because a bounded rollback would then move public refs back.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// §11.8a — the abort route reports which arm it took.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// §9.11 — an ALL-NO-OP closure still runs the whole transaction.
//
// AC-058's last clause: "If every row is a no-op row, the run still
// issues start / one ordered verify per row / prepare / commit; it MUST NOT
// skip the transaction, because the verification is the race barrier for the
// whole closure." Skipping it would leave the closure unverified precisely
// when nothing else checks it.
// ---------------------------------------------------------------------------

// newReparentNoopWorkspace builds a stack whose target has no commits of its
// own, so its replay range is empty and its computed tip equals its
// pre-image: base -> leaf, with leaf reparented from the ENTRY `base` onto the
// literal ref `refs/heads/base`. The stored token changes (so there is real
// metadata work) while no ref can move.
func newReparentNoopWorkspace(t *testing.T) *reparentWorkspace {
	t.Helper()
	repo := newReparentPrimitiveRepo(t)
	mainTip := repo.RevParse("main")
	repo.Git("switch", "-q", "-c", "base")
	baseTip := repo.Commit("base.txt", "base")
	repo.Git("switch", "-q", "-c", "leaf")
	repo.Git("switch", "-q", "main")

	metadataRoot := canonicalize(t.TempDir())
	featurePath := filepath.Join(metadataRoot, "features", "customer")
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	stack := Stack{Branches: []StackEntry{
		{Name: "base", Base: "refs/heads/main", LastBaseSHA: mainTip},
		{Name: "leaf", Base: "base", LastBaseSHA: baseTip},
	}}
	data, err := yaml.Marshal(&stack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StackPath(featurePath), data, 0o644); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{RepoRoot: repo.Dir, Mode: ModeExternal, MetadataRoot: metadataRoot, StableID: "fixture"}
	return &reparentWorkspace{t: t, Repo: repo, Loc: ReparentLocationFor(ws, "customer", featurePath), WS: ws}
}

// The transaction always runs, even all-no-op, and a committed one is never re-issued (AC-058).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentTransactionEnvelope(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-044", "single-cas-transaction-envelope")
	// --- TestRunReparentAllNoopClosureStillRunsTheTransaction ---
	func(t *testing.T) {
		w := newReparentNoopWorkspace(t)
		log := reparentCaptureArgv(t)

		// A reference-transaction hook records every phase Git actually ran, so
		// "the transaction happened" is proven by Git itself rather than by this
		// run's own bookkeeping.
		hookLog := filepath.Join(canonicalize(t.TempDir()), "transaction.log")
		hooks := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks"
		writeExecutableForTest(t, hooks+"/reference-transaction",
			"#!/bin/sh\necho \"state=$1\" >> "+hookLog+"\ncat >> "+hookLog+"\nexit 0\n")

		limit := 50
		in := w.planInput()
		in.Target = "leaf"
		in.OntoToken = "base"
		in.OntoKind = "ref"
		in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
		plan, _, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Approval.Fingerprint == nil {
			t.Fatalf("plan minted no token: %+v", plan.Blockers)
		}
		in.Invocation = ReparentInvocationExecute
		in.Guard.Approve = *plan.Approval.Fingerprint

		leafBefore := w.Repo.RevParse("leaf")
		run := w.begin(in)
		if err := RunReparent(run); err != nil {
			t.Fatalf("run: %v", err)
		}

		// Every row really is a no-op.
		if len(run.State.Rows) != 1 {
			t.Fatalf("closure = %+v", run.State.Rows)
		}
		row := run.State.Rows[0]
		if !row.Noop || row.PlannedNewSHA != row.PreimageSHA {
			t.Fatalf("the fixture must produce a no-op row: %+v", row)
		}
		if w.Repo.RevParse("leaf") != leafBefore {
			t.Fatal("a no-op row never moves its ref")
		}

		// ...and the transaction ran anyway.
		if !run.State.CASTransactionSucceeded {
			t.Fatal("the run must record that the verification transaction succeeded")
		}
		if len(run.State.CASRows) != 1 || !run.State.CASRows[0].Noop || !run.State.CASRows[0].Applied {
			t.Fatalf("cas_rows = %+v", run.State.CASRows)
		}
		sawStdin := false
		for _, argv := range *log {
			if len(argv) >= 2 && argv[0] == "update-ref" && containsString(argv, "--stdin") {
				sawStdin = true
			}
		}
		if !sawStdin {
			t.Fatal("no update-ref --stdin transaction was emitted at all")
		}
		recorded, err := os.ReadFile(hookLog)
		if err != nil {
			t.Fatalf("the reference-transaction hook never ran: %v", err)
		}
		for _, want := range []string{"state=prepared", "state=committed", "refs/heads/leaf"} {
			if !strings.Contains(string(recorded), want) {
				t.Fatalf("Git did not run the whole envelope over the closure (%q missing):\n%s", want, recorded)
			}
		}

		// The metadata work that made this a reparent at all did land.
		if got := w.entry("leaf"); got.Base != "refs/heads/base" {
			t.Fatalf("leaf metadata = %+v", got)
		}
		assertReparentSucceeded(t, w)
		reparentAssertNoDashC(t, *log)
	}(t)

	// --- TestReparentCommittedTransactionIsNotReIssuedOnResume ---
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "after-cas")
		if !st.CASTransactionSucceeded {
			t.Fatal("the successful transaction marker must be persisted after the child succeeds")
		}

		hookLog := filepath.Join(canonicalize(t.TempDir()), "transaction.log")
		hooks := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks"
		writeExecutableForTest(t, hooks+"/reference-transaction",
			"#!/bin/sh\necho \"state=$1\" >> "+hookLog+"\ncat >> "+hookLog+"\nexit 0\n")

		resumed, err := ContinueReparent(in)
		if err != nil {
			t.Fatalf("continue: %v", err)
		}
		if resumed.State.Stage != ReparentStageCompleted {
			t.Fatalf("stage = %q", resumed.State.Stage)
		}
		// Cleanup legitimately deletes this run's pins, which are refs too, so
		// the assertion is scoped to the CLOSURE's branches: none of them may
		// appear in a transaction issued after the resume.
		if data, err := os.ReadFile(hookLog); err == nil && strings.Contains(string(data), "refs/heads/") {
			t.Fatalf("a completed transaction must not be re-issued over refs/heads:\n%s", data)
		}
	}(t)

	// A forged post-image cannot make an unissued CAS look committed merely
	// because planned tips are strict ancestors of the untouched pre-images.
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "before-cas" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("forged post-image setup: %v", err)
		}
		ReparentStepHook = nil
		st := LoadReparentState(w.Loc).State
		for i := range st.Rows {
			st.Rows[i].PlannedNewSHA = w.Repo.RevParse(st.Rows[i].PreimageSHA + "^")
			st.Rows[i].Noop = false
		}
		st.CASTransactionSucceeded = false
		st.CommitPointReached = false
		after, err := base64.StdEncoding.DecodeString(st.StackAfterBase64)
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteStackBytesAtomic(w.Loc.FeaturePath, after); err != nil {
			t.Fatal(err)
		}
		if err := SaveReparentState(w.Loc, st); err != nil {
			t.Fatal(err)
		}
		_, err = AbortReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalStateCorrupt {
			t.Fatalf("abort forged post-image = %v, want strict state corruption", err)
		}
		if load := LoadReparentState(w.Loc); load.Kind != ReparentStateCorrupt {
			t.Fatalf("forged transaction relationships were not retained as corrupt: %+v", load)
		}
		for _, row := range st.Rows {
			if got := w.Repo.RevParse("refs/heads/" + row.GitBranch); got != row.PreimageSHA {
				t.Fatalf("%s moved to %s, want rollback/pre-image %s", row.Name, got, row.PreimageSHA)
			}
		}
	}(t)
}

// A resumed run that already issued its transaction does NOT re-issue it: the
// persisted evidence, not the classification, is what authorises the skip.

// ---------------------------------------------------------------------------
// T-013 / AC-016 — a SHA-256 repository, end to end.
// ---------------------------------------------------------------------------

// newReparentSHA256Workspace builds the same two-row stack as
// newReparentWorkspace in a `--object-format=sha256` repository. It SKIPS only
// when this Git cannot create one; a Git that can must run the whole cell.
func newReparentSHA256Workspace(t *testing.T) *reparentWorkspace {
	t.Helper()
	reparentCountGitLeafFor(t)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	reparentTestGitIdentity(t)
	dir := canonicalize(t.TempDir())
	probe := reparentTestGitCommand(t, "", "init", "--object-format=sha256", "-q", "-b", "main", dir)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("this git cannot create a sha256 repository: %v: %s", err, out)
	}
	repo := &reparentRepo{t: t, Dir: dir}
	repo.Commit("one.txt", "one")
	mainTip := repo.RevParse("main")
	repo.Git("switch", "-q", "-c", "pr1")
	pr1Tip := repo.Commit("pr1.txt", "pr1")
	repo.Git("switch", "-q", "-c", "pr2")
	repo.Commit("pr2.txt", "pr2")
	repo.Git("switch", "-q", "main")

	metadataRoot := canonicalize(t.TempDir())
	featurePath := filepath.Join(metadataRoot, "features", "customer")
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	stack := Stack{Branches: []StackEntry{
		{Name: "pr1", Base: "refs/heads/main", LastBaseSHA: mainTip},
		{Name: "pr2", Base: "pr1", LastBaseSHA: pr1Tip},
	}}
	data, err := yaml.Marshal(&stack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StackPath(featurePath), data, 0o644); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{RepoRoot: repo.Dir, Mode: ModeExternal, MetadataRoot: metadataRoot, StableID: "sha256"}
	return &reparentWorkspace{t: t, Repo: repo, Loc: ReparentLocationFor(ws, "customer", featurePath), WS: ws}
}

func TestReparentSHA256RepositoryEndToEnd(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-013",
		"sha256-end-to-end",
		"object-format-failure-stays-capability",
	)
	w := newReparentSHA256Workspace(t)
	log := reparentCaptureArgv(t)

	mainTip := w.Repo.RevParse("main")
	if len(mainTip) != 64 {
		t.Fatalf("the fixture is not a sha256 repository: HEAD is %d hex chars", len(mainTip))
	}

	limit := 50
	in := w.planInput()
	in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
	plan, _, err := PlanReparent(in)
	if err != nil {
		t.Fatal(err)
	}
	// AC-016: the probed width is 64 and every published OID is a full
	// 64-hex object id — never a 40-hex truncation and never a short token.
	if plan.Policy.OIDWidth == nil || *plan.Policy.OIDWidth != 64 {
		t.Fatalf("policy.oid_width = %v, want 64", plan.Policy.OIDWidth)
	}
	if plan.Target.DestinationSHA == nil || len(*plan.Target.DestinationSHA) != 64 {
		t.Fatalf("destination sha = %v", plan.Target.DestinationSHA)
	}
	if *plan.Target.NewParent.StoredToken != "refs/heads/main" {
		t.Fatalf("stored token = %q", *plan.Target.NewParent.StoredToken)
	}
	if plan.Target.Cutoff.ResolvedSHA == nil || len(*plan.Target.Cutoff.ResolvedSHA) != 64 {
		t.Fatalf("cutoff = %v", plan.Target.Cutoff.ResolvedSHA)
	}
	if plan.Target.Head.SHA == nil || len(*plan.Target.Head.SHA) != 64 {
		t.Fatalf("head sha = %v", plan.Target.Head.SHA)
	}
	// The fingerprint itself is domain-separated SHA-256 hex regardless of the
	// repository's object format, and it is stable across a rebuild.
	if plan.Approval.Fingerprint == nil || !reparentFingerprintHex.MatchString(*plan.Approval.Fingerprint) {
		t.Fatalf("fingerprint = %v", plan.Approval.Fingerprint)
	}
	again, _, err := PlanReparent(in)
	if err != nil {
		t.Fatal(err)
	}
	if *again.Approval.Fingerprint != *plan.Approval.Fingerprint {
		t.Fatal("the fingerprint must be stable over a sha256 repository too")
	}
	reparentGitFaultHook = func(_ string, args []string) (reparentGitResult, error, bool) {
		if len(args) == 2 && args[0] == "rev-parse" && args[1] == "--show-object-format" {
			err := errors.New("injected object-format failure")
			return reparentGitResult{ExitCode: 128, Stderr: []byte(err.Error())}, err, true
		}
		return reparentGitResult{}, nil, false
	}
	unavailable, _, err := PlanReparent(in)
	reparentGitFaultHook = nil
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.Policy.OIDWidth != nil ||
		unavailable.Summary.Plannability != ReparentPlannabilityUnavailable ||
		!hasReparentBlocker(unavailable, ReparentRefusalCapabilityUnsupported) ||
		hasReparentBlocker(unavailable, ReparentRefusalProbeFailed) {
		t.Fatalf("object-format failure lost its typed refusal: %+v", unavailable)
	}

	in.Invocation = ReparentInvocationExecute
	in.Guard.Approve = *plan.Approval.Fingerprint
	run := w.begin(in)
	if err := RunReparent(run); err != nil {
		t.Fatalf("run: %v", err)
	}

	if parent := strings.ToLower(strings.TrimSpace(w.Repo.Git("rev-parse", "pr2^"))); parent != mainTip {
		t.Fatalf("pr2's parent = %s, want %s", parent, mainTip)
	}
	entry := w.entry("pr2")
	if entry.Base != "refs/heads/main" || len(entry.LastBaseSHA) != 64 {
		t.Fatalf("metadata = %+v", entry)
	}
	for _, row := range run.State.Rows {
		if len(row.PreimageSHA) != 64 || len(row.PlannedNewSHA) != 64 {
			t.Fatalf("state row carries a non-sha256 oid: %+v", row)
		}
	}
	assertReparentSucceeded(t, w)
	reparentAssertNoDashC(t, *log)
}

var reparentFingerprintHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ---------------------------------------------------------------------------
// T-083 / AC-096 — holder-restore-deferred is a WARNING, is persisted, and is
// re-attempted by a later --continue. T-051's third clause (the untracked gate
// runs before holder re-attachment too) rides on the same fixture.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// T-046 / AC-059 — the CAS race under the reftable backend.
// ---------------------------------------------------------------------------

func TestReparentCASRaceUnderReftableBackend(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-046", "reftable-cas-race")
	_ = "asserts AC-059"
	reparentCountGitLeafFor(t)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	reparentTestGitIdentity(t)
	dir := canonicalize(t.TempDir())
	probe := reparentTestGitCommand(t, "", "init", "--ref-format=reftable", "-q", "-b", "main", dir)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("this git has no reftable backend: %v: %s", err, out)
	}
	repo := &reparentRepo{t: t, Dir: dir}
	repo.Commit("one.txt", "one")
	repo.Commit("two.txt", "two")
	repo.Branch("topic", "HEAD")
	tip := repo.RevParse("topic")
	older := repo.RevParse("HEAD~1")

	if got := probeReparentRefBackend(dir, ReparentGitCapabilities{CapRefBackendKnown: true}); got != ReparentRefBackendReftable {
		t.Fatalf("probed backend = %q, want reftable", got)
	}
	if !reparentBackendCrashAtomic(ReparentRefBackendReftable) {
		t.Fatal("reftable is the one crash-atomic backend")
	}

	// The race barrier is the backend-independent half of §9.11: a stale
	// expected old value aborts the WHOLE transaction during prepare.
	rows := []ReparentStateRow{
		{Name: "topic", GitBranch: "topic", PreimageSHA: tip, PlannedNewSHA: older, Order: 0},
		{Name: "main", GitBranch: "main", PreimageSHA: tip, PlannedNewSHA: older, Order: 1},
	}
	stale := []reparentCASLine{
		reparentCASUpdate("refs/heads/topic", older, strings.Repeat("0", len(tip))),
		reparentCASUpdate("refs/heads/main", older, tip),
	}
	if _, err := runReparentCAS(dir, "tws reparent reftable", stale); err == nil {
		t.Fatal("a stale expected old value must abort the transaction on reftable too")
	}
	if repo.RevParse("topic") != tip || repo.RevParse("main") != tip {
		t.Fatal("no ref may move when the transaction aborts")
	}

	// And the honest transaction commits, over the same backend.
	classes, err := ClassifyReparentRefs(dir, rows)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runReparentCAS(dir, "tws reparent reftable", reparentForwardCASLines(rows, ReparentRefClassMap(classes))); err != nil {
		t.Fatalf("the honest transaction must commit: %v", err)
	}
	if repo.RevParse("topic") != older || repo.RevParse("main") != older {
		t.Fatal("both rows must land")
	}
}

// ---------------------------------------------------------------------------
// T-052's second clause / AC-065 — a non-conflict native Git failure is not a
// conflict and not a reparent refusal: it is persisted as a native-git
// failure, the row stays pending, and the run resumes once the cause is gone.
// ---------------------------------------------------------------------------

func testReparentNativeGitFailureIsPersistedAndResumable(t *testing.T) {
	covers, ok := reflect.TypeOf(ReparentPlanApprovalCovers{}).FieldByName("WaivedKinds")
	if !ok || covers.Type != reflect.TypeOf([]RefusalKind{}) {
		t.Fatalf("waived_kinds is %v, want []RefusalKind", covers.Type)
	}
	w := newReparentWorkspace(t, ModeExternal)
	in, _ := w.approvedInput()

	// A pre-rebase hook that refuses makes `git rebase` exit non-zero while
	// leaving NO rebase in progress — the exact shape §9.7's last paragraph
	// separates from a conflict.
	hooks := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks"
	hook := hooks + "/pre-rebase"
	writeExecutableForTest(t, hook, "#!/bin/sh\necho 'pre-rebase refuses' 1>&2\nexit 1\n")

	run := w.begin(in)
	err := RunReparent(run)
	if err == nil {
		t.Fatal("a refusing pre-rebase hook must fail the run")
	}
	var refusal *ReparentRefusalError
	if asReparentRefusal(err, &refusal) {
		t.Fatalf("a native Git failure is not a reparent refusal kind: %v", refusal)
	}
	if _, isPause := err.(*ReparentConflictPause); isPause {
		t.Fatal("a failure with no rebase in progress is not a conflict pause")
	}

	st := LoadReparentState(w.Loc).State
	if st.Stage != ReparentStageFailed {
		t.Fatalf("stage = %q, want failed", st.Stage)
	}
	if st.ResumeStage != ReparentStageComputing {
		t.Fatalf("resume_stage = %q, want computing", st.ResumeStage)
	}
	if st.FailureDomain != ReparentFailureDomainNativeGit {
		t.Fatalf("failure_domain = %q", st.FailureDomain)
	}
	if st.FailureDetail == "" {
		t.Fatal("the captured native error must be persisted")
	}
	if row := st.Row("pr2"); row.Stage != ReparentRowPending || row.PlannedNewSHA != "" {
		t.Fatalf("the current row must stay pending: %+v", row)
	}
	pins := strings.TrimSpace(w.Repo.Git("for-each-ref", "--format=%(refname)", "refs/tws/reparent/**"))
	if pins == "" {
		t.Fatal("every pin and artifact must remain")
	}

	// Fix the cause, then resume.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	resumed, err := ContinueReparent(in)
	if err != nil {
		t.Fatalf("continue after the cause is gone: %v", err)
	}
	if resumed.State.Stage != ReparentStageCompleted {
		t.Fatalf("stage = %q", resumed.State.Stage)
	}
	assertReparentSucceeded(t, w)
}

// ---------------------------------------------------------------------------
// The forward-retry holder invariant (§9.9, §9.11, AC-058, AC-060, AC-096).
//
// A failed transaction re-attaches every holder it detached, so the operator
// is not left with a detached worktree. That means the NEXT attempt starts
// with those worktrees attached to the very branches it is about to move —
// and moving a ref out from under an attached worktree leaves that worktree's
// index and HEAD inconsistent. The invariant is therefore re-asserted before
// every forward CAS, not once at detaching-holders.
// ---------------------------------------------------------------------------

func TestReparentForwardRetryRedetachesHoldersBeforeTheCAS(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	holder := filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
	w.Repo.Git("worktree", "add", "-q", holder, "pr3")

	// A reference-transaction hook vetoes the first transaction and only the
	// first: the second attempt must succeed, so the test exercises the RETRY
	// rather than a permanent failure.
	hooks := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks"
	flag := filepath.Join(canonicalize(t.TempDir()), "veto-once")
	writeExecutableForTest(t, hooks+"/reference-transaction",
		"#!/bin/sh\n"+
			"if [ \"$1\" = \"prepared\" ]; then\n"+
			"  while read -r old new ref; do\n"+
			"    case \"$ref\" in refs/heads/pr3)\n"+
			"      if [ ! -f "+flag+" ]; then : > "+flag+"; echo 'veto once' 1>&2; exit 1; fi;;\n"+
			"    esac\n"+
			"  done\n"+
			"fi\n"+
			"exit 0\n")

	in, _ := w.approvedInput()
	run := w.begin(in)
	err := RunReparent(run)
	var refusal *ReparentRefusalError
	if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalRefTransactionMismatch {
		t.Fatalf("the vetoed transaction must refuse, got %v", err)
	}

	// The failed attempt put the operator's worktree back on its branch...
	if got := strings.TrimSpace(gitInTest(t, holder, "symbolic-ref", "--short", "HEAD")); got != "pr3" {
		t.Fatalf("a failed transaction must re-attach the holder, got %q", got)
	}
	st := LoadReparentState(w.Loc).State
	if st.ResumeStage != ReparentStageDetachingHolders {
		t.Fatalf("resume_stage = %q, want detaching-holders so the retry re-detaches", st.ResumeStage)
	}
	preimage := st.Row("pr3").PreimageSHA
	planned := st.Row("pr3").PlannedNewSHA

	// ...and the retry must detach it again before it moves the ref — even
	// when it re-enters at committing-refs directly. A crash between the
	// restore and the resume_stage rewind leaves exactly that state, and the
	// recorded holder is still in detached_holders[], so a run that trusted
	// its own bookkeeping would skip it and move the ref underneath it.
	st.Stage = ReparentStageCommittingRefs
	st.ResumeStage = ReparentStageCommittingRefs
	st.FailureDomain, st.FailureKind, st.FailureDetail = "", "", ""
	if err := SaveReparentState(w.Loc, st); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitInTest(t, holder, "symbolic-ref", "--short", "HEAD")); got != "pr3" {
		t.Fatalf("the fixture must re-enter with the holder ATTACHED, got %q", got)
	}

	gitInTest(t, holder, "switch", "--ignore-other-worktrees", "main")
	_, driftErr := ContinueReparent(in)
	var driftRefusal *ReparentRefusalError
	if !asReparentRefusal(driftErr, &driftRefusal) || driftRefusal.Kind != ReparentRefusalHolderUnsafe {
		t.Fatalf("operator branch switch must refuse holder drift: %v", driftErr)
	}
	if got := strings.TrimSpace(gitInTest(t, holder, "symbolic-ref", "--short", "HEAD")); got != "main" {
		t.Fatalf("retry overwrote the operator's checkout choice: %q", got)
	}
	if w.Repo.RevParse("pr3") != preimage {
		t.Fatal("holder drift refusal moved the affected ref")
	}
	gitInTest(t, holder, "switch", "--ignore-other-worktrees", "pr3")

	injectedSwitchSave := errors.New("crash after redetach switch")
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if !strings.HasPrefix(step, "holder-redetach-intent-written:") {
			return nil
		}
		SyncStateIOFault = func(op, path string) error {
			if op == SyncIOWriteReparentState && path == ReparentStatePath(w.Loc) {
				return injectedSwitchSave
			}
			return nil
		}
		return nil
	}
	if _, err := ContinueReparent(in); !errors.Is(err, injectedSwitchSave) {
		t.Fatalf("redetach switch crash = %v", err)
	}
	ReparentStepHook = nil
	SyncStateIOFault = nil
	crashed := LoadReparentState(w.Loc)
	if crashed.Kind != ReparentStateOK {
		t.Fatalf("redetach intent did not remain recoverable: %+v", crashed)
	}
	var crashedHolder *ReparentStateHolder
	for i := range crashed.State.DetachedHolders {
		if canonicalize(crashed.State.DetachedHolders[i].Path) == canonicalize(holder) {
			crashedHolder = &crashed.State.DetachedHolders[i]
			break
		}
	}
	position, positionErr := measureReparentHolderPosition(holder)
	if crashedHolder == nil || crashedHolder.SwitchIntent == nil ||
		crashedHolder.SwitchIntent.Action != ReparentHolderSwitchRedetach ||
		positionErr != nil || position.Branch != "" {
		t.Fatalf("redetach crash did not preserve destination HEAD plus intent: %+v", crashedHolder)
	}

	resumed, err := ContinueReparent(in)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if resumed.State.Stage != ReparentStageCompleted {
		t.Fatalf("stage = %q", resumed.State.Stage)
	}
	if w.Repo.RevParse("pr3") != planned || planned == preimage {
		t.Fatalf("pr3 = %s, want the planned tip %s", w.Repo.RevParse("pr3"), planned)
	}

	// The worktree is the assertion that matters: attached to its branch, at
	// the new tip, with a clean index — not a worktree whose HEAD says one
	// thing and whose index says another.
	if got := strings.TrimSpace(gitInTest(t, holder, "symbolic-ref", "--short", "HEAD")); got != "pr3" {
		t.Fatalf("the holder must end attached to pr3, got %q", got)
	}
	if got := strings.ToLower(strings.TrimSpace(gitInTest(t, holder, "rev-parse", "HEAD"))); got != planned {
		t.Fatalf("the holder observes %s, want the new tip %s", got, planned)
	}
	if status := strings.TrimSpace(gitInTest(t, holder, "status", "--porcelain")); status != "" {
		t.Fatalf("the holder's index and working tree must be consistent, got:\n%s", status)
	}
	if diff := strings.TrimSpace(gitInTest(t, holder, "diff", "--stat", "HEAD")); diff != "" {
		t.Fatalf("the holder must have no phantom diff against its own HEAD:\n%s", diff)
	}
	assertReparentSucceeded(t, w)
}

// ---------------------------------------------------------------------------
// Per-row repository context (§6.1 steps 4-5, §6.2, AC-034).
//
// Every row probe runs in the row's OWN context — StackEntry.Repo when set,
// else the workspace root — and the run mutates the TARGET's repository and no
// other. Without that, a stack whose entries name a secondary repository would
// be measured against the wrong one, the cross-repo and repo-unavailable gates
// could never fire, and the transaction could move refs in a repository the
// operator never named.
// ---------------------------------------------------------------------------

// newReparentSecondaryRepoWorkspace puts the whole closure in a SECOND
// repository, leaving the workspace root repository untouched and available as
// a witness.
func newReparentSecondaryRepoWorkspace(t *testing.T) (*reparentWorkspace, *reparentRepo) {
	t.Helper()
	root := newReparentPrimitiveRepo(t)

	secondary := canonicalize(t.TempDir())
	gitInTest(t, secondary, "init", "-q", "-b", "main", ".")
	other := &reparentRepo{t: t, Dir: secondary}
	other.Commit("s1.txt", "s1")
	mainTip := other.RevParse("main")
	other.Git("switch", "-q", "-c", "pr1")
	pr1Tip := other.Commit("pr1.txt", "pr1")
	other.Git("switch", "-q", "-c", "pr2")
	other.Commit("pr2.txt", "pr2")
	pr2Tip := other.RevParse("HEAD")
	other.Git("switch", "-q", "-c", "pr3")
	other.Commit("pr3.txt", "pr3")
	other.Git("switch", "-q", "main")

	metadataRoot := canonicalize(t.TempDir())
	featurePath := filepath.Join(metadataRoot, "features", "customer")
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	stack := Stack{Branches: []StackEntry{
		{Name: "pr1", Repo: secondary, Base: "refs/heads/main", LastBaseSHA: mainTip},
		{Name: "pr2", Repo: secondary, Base: "pr1", LastBaseSHA: pr1Tip},
		// A genuine child, in the SAME repository as its parent.
		{Name: "pr3", Repo: secondary, Base: "pr2", LastBaseSHA: pr2Tip},
		// A same-named entry in ANOTHER repository (the workspace root). It
		// is not a child of pr2 and must be neither pulled into the closure
		// nor refused: it is an unrelated stack.
		{Name: "stranger", Base: "pr2", LastBaseSHA: root.RevParse("main")},
	}}
	data, err := yaml.Marshal(&stack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StackPath(featurePath), data, 0o644); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{RepoRoot: root.Dir, Mode: ModeExternal, MetadataRoot: metadataRoot, StableID: "secondary"}
	w := &reparentWorkspace{t: t, Repo: other, Loc: ReparentLocationFor(ws, "customer", featurePath), WS: ws}
	// planInput's RepoRoot is the WORKSPACE root, exactly as the CLI supplies
	// it; resolving the target's own context is the code under test.
	w.WS.RepoRoot = root.Dir
	return w, root
}

func (w *reparentWorkspace) secondaryPlanInput(workspaceRoot string) ReparentPlanInput {
	in := w.planInput()
	in.RepoRoot = workspaceRoot
	in.Workspace.RepoRoot = workspaceRoot
	return in
}

// The per-row repository context: the closure is measured and mutated in the
// TARGET's repository, an unrelated same-named stack in another repository is
// ignored rather than refused, and a row whose repository cannot be read
// refuses repo-unavailable (§6.1 steps 4-5, §6.2, AC-034).
//
// §17.3 counts one t.Run leaf per real-Git cell, so both halves share one.
func TestReparentPerRowRepositoryContext(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-022",
		"per-row-repository-context",
		"repo-alias-resolver-refusal",
	)
	// --- TestReparentResolvesPerRowRepositoryContext ---
	func(t *testing.T) {
		w, root := newReparentSecondaryRepoWorkspace(t)

		limit := 50
		in := w.secondaryPlanInput(root.Dir)
		in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}

		rawStack := w.loadStack()
		aliasDir := filepath.Join(t.TempDir(), "secondary-alias")
		if err := os.Symlink(w.Repo.Dir, aliasDir); err != nil {
			t.Fatal(err)
		}
		w.Repo.Git("branch", "-m", "pr2", "user/pr2")
		w.Repo.Git("branch", "-m", "pr3", "user/pr3")
		aliasStack := rawStack
		aliasStack.Branches = append([]StackEntry{}, rawStack.Branches...)
		for i := range aliasStack.Branches {
			if aliasStack.Branches[i].Name == "pr2" {
				aliasStack.Branches[i].Branch = "user/pr2"
			}
			if aliasStack.Branches[i].Name == "pr3" {
				aliasStack.Branches[i].Repo = aliasDir
				aliasStack.Branches[i].Branch = "user/pr3"
			}
		}
		if err := SaveStack(w.Loc.FeaturePath, aliasStack); err != nil {
			t.Fatal(err)
		}
		aliasPlan, aliasReq, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if !hasReparentBlocker(aliasPlan, ReparentRefusalDestinationResolverDivergent) {
			t.Fatalf("same-repository alias edge was admitted: %+v", aliasPlan.Blockers)
		}
		if aliasPlan.Runnable {
			t.Fatal("an alias edge a later shipped sync would resolve differently must not be admitted")
		}
		aliasNames := []string{aliasPlan.Target.Name}
		for _, row := range aliasPlan.Descendants {
			aliasNames = append(aliasNames, row.Name)
		}
		if strings.Join(aliasNames, ",") != "pr2,pr3" {
			t.Fatalf("alias child disappeared instead of being refused: %v", aliasNames)
		}
		if len(aliasReq.Rows) != 2 || aliasReq.Rows[1].Repo != aliasDir ||
			aliasReq.Rows[1].GitBranch != "user/pr3" {
			t.Fatalf("alias/decoupled row facts were rewritten: %+v", aliasReq.Rows)
		}
		if resolved := ResolveSyncBase(aliasStack, GetBranch(aliasStack, "pr3"), aliasDir); resolved.Base != "pr2" ||
			resolved.Base == GetBranch(aliasStack, "pr2").GitBranch() {
			t.Fatalf("alias fixture did not expose the shipped resolver mismatch: %+v", resolved)
		}
		w.Repo.Git("branch", "-m", "user/pr2", "pr2")
		w.Repo.Git("branch", "-m", "user/pr3", "pr3")
		if err := SaveStack(w.Loc.FeaturePath, rawStack); err != nil {
			t.Fatal(err)
		}

		plan, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Runnable {
			t.Fatalf("the secondary-repository plan must be runnable: %+v", plan.Blockers)
		}

		// The closure is the genuine parent/child pair in the secondary
		// repository; the same-named entry in another repository is ignored.
		names := []string{plan.Target.Name}
		for _, d := range plan.Descendants {
			names = append(names, d.Name)
		}
		if strings.Join(names, ",") != "pr2,pr3" {
			t.Fatalf("closure = %v, want the genuine child only", names)
		}
		if hasReparentBlocker(plan, ReparentRefusalCrossRepoClosure) {
			t.Fatalf("an unrelated stack in another repository is ignored, never refused: %+v", plan.Blockers)
		}
		// Every row was measured in the SECONDARY repository.
		if req.TargetRepoRoot != w.Repo.Dir {
			t.Fatalf("target context = %q, want %q", req.TargetRepoRoot, w.Repo.Dir)
		}
		for _, row := range req.Rows {
			if row.ExecutionContext.Source != "entry-repo" {
				t.Fatalf("%s execution context source = %q", row.Name, row.ExecutionContext.Source)
			}

			if row.ExecutionContext.RepoRoot == nil || *row.ExecutionContext.RepoRoot != w.Repo.Dir {
				t.Fatalf("%s execution context = %v", row.Name, row.ExecutionContext.RepoRoot)
			}
			if row.HeadSHA != w.Repo.RevParse(row.GitBranch) {
				t.Fatalf("%s head %s was not measured in the secondary repository", row.Name, row.HeadSHA)
			}
		}
		foreign := w.secondaryPlanInput(root.Dir)
		foreign.OntoToken = "stranger"
		foreign.OntoKind = "entry"
		foreign.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
		foreignPlan, _, err := PlanReparent(foreign)
		if err != nil {
			t.Fatal(err)
		}
		if !hasReparentBlocker(foreignPlan, ReparentRefusalCrossRepoClosure) {
			t.Fatalf("a genuinely different-repository destination was not refused: %+v", foreignPlan.Blockers)
		}

		// Execution mutates the secondary repository and NOTHING in the workspace
		// root, which does not even have these branches.
		rootRefsBefore := root.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/")
		in.Invocation = ReparentInvocationExecute
		in.Guard.Approve = *plan.Approval.Fingerprint
		run := w.begin(in)
		if run.RepoRoot != w.Repo.Dir {
			t.Fatalf("the run's repository root = %q, want the target's %q", run.RepoRoot, w.Repo.Dir)
		}
		if err := RunReparent(run); err != nil {
			t.Fatalf("run: %v", err)
		}

		mainTip := w.Repo.RevParse("main")
		if parent := strings.ToLower(strings.TrimSpace(w.Repo.Git("rev-parse", "pr2^"))); parent != mainTip {
			t.Fatalf("pr2's parent = %s, want the secondary repository's main %s", parent, mainTip)
		}
		if got := root.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/"); got != rootRefsBefore {
			t.Fatalf("the workspace-root repository must not be touched:\n--- before ---\n%s\n--- after ---\n%s",
				rootRefsBefore, got)
		}
		if got := w.entry("pr2"); got.Base != "refs/heads/main" || got.LastBaseSHA != mainTip {
			t.Fatalf("metadata = %+v", got)
		}
		if got := w.entry("stranger"); got.Base != "pr2" || got.LastBaseSHA != root.RevParse("main") {
			t.Fatalf("an entry outside the closure changed: %+v", got)
		}
		assertReparentSucceeded(t, w)
	}(t)

	// --- TestReparentRefusesAnUnreadableRowRepository ---
	func(t *testing.T) {
		w, root := newReparentSecondaryRepoWorkspace(t)

		stack := w.loadStack()
		notARepo := canonicalize(t.TempDir())
		for i := range stack.Branches {
			if stack.Branches[i].Name == "pr3" {
				stack.Branches[i].Repo = notARepo
			}
		}
		data, err := yaml.Marshal(&stack)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(StackPath(w.Loc.FeaturePath), data, 0o644); err != nil {
			t.Fatal(err)
		}

		limit := 50
		in := w.secondaryPlanInput(root.Dir)
		in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
		plan, _, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		// pr3 now names a different repository, so it is no longer a child of
		// pr2 at all (§6.1 step 4) and the closure is the target alone.
		for _, d := range plan.Descendants {
			if d.Name == "pr3" {
				t.Fatal("an entry in another repository is not a logical child")
			}
		}

		// Point the TARGET at the non-repository instead: now the gate must fire.
		stack = w.loadStack()
		for i := range stack.Branches {
			if stack.Branches[i].Name == "pr2" {
				stack.Branches[i].Repo = notARepo
			}
		}
		data, err = yaml.Marshal(&stack)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(StackPath(w.Loc.FeaturePath), data, 0o644); err != nil {
			t.Fatal(err)
		}
		plan, _, err = PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		if !hasReparentBlocker(plan, ReparentRefusalRepoUnavailable) {
			t.Fatalf("blockers = %+v, want repo-unavailable", plan.Blockers)
		}
		if plan.Runnable {
			t.Fatal("a row whose repository cannot be read is never runnable")
		}
	}(t)
}

// A row whose configured repository is not a repository at all refuses
// repo-unavailable — a gate that could never fire while every row was probed
// in the workspace root.

func testReparentRequiredProbesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(args []string) bool
		want ReparentRefusalKind
	}{
		{
			name: "ancestry probe error",
			fail: func(args []string) bool { return len(args) > 0 && args[0] == "merge-base" },
			want: ReparentRefusalProbeFailed,
		},
		{
			name: "replay enumeration error",
			fail: func(args []string) bool {
				return len(args) > 2 && args[0] == "rev-list" &&
					args[1] == "--no-merges" && args[2] == "--reverse"
			},
			want: ReparentRefusalProbeFailed,
		},
		{
			name: "merge count error",
			fail: func(args []string) bool {
				return len(args) > 2 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "--merges"
			},
			want: ReparentRefusalProbeFailed,
		},
	} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			reparentGitFaultHook = func(_ string, args []string) (reparentGitResult, error, bool) {
				if !tc.fail(args) {
					return reparentGitResult{}, nil, false
				}
				err := errors.New("injected required-probe failure")
				return reparentGitResult{ExitCode: 2, Stderr: []byte(err.Error())}, err, true
			}
			plan, _, err := PlanReparent(w.planInput())
			reparentGitFaultHook = nil
			if err != nil {
				t.Fatal(err)
			}
			if !hasReparentBlocker(plan, tc.want) {
				t.Fatalf("blockers = %+v, want %s", plan.Blockers, tc.want)
			}
		}(t)
	}

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		reparentGitFaultHook = func(_ string, args []string) (reparentGitResult, error, bool) {
			if len(args) == 0 || args[0] != "merge-base" {
				return reparentGitResult{}, nil, false
			}
			return reparentGitResult{ExitCode: 1}, errors.New("exit status 1"), true
		}
		plan, _, err := PlanReparent(w.planInput())
		reparentGitFaultHook = nil
		if err != nil {
			t.Fatal(err)
		}
		if !hasReparentBlocker(plan, ReparentRefusalCutoffNotAncestor) {
			t.Fatalf("blockers = %+v, want cutoff-not-ancestor", plan.Blockers)
		}
	}(t)

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		reparentWorktreeInventoryHook = func(string) WorktreeInventory {
			return worktreeInventoryUnavailable(errors.New("injected malformed porcelain"))
		}
		plan, _, err := PlanReparent(w.planInput())
		reparentWorktreeInventoryHook = nil
		if err != nil {
			t.Fatal(err)
		}
		if !hasReparentBlocker(plan, ReparentRefusalHolderUnsafe) {
			t.Fatalf("blockers = %+v, want holder-unsafe", plan.Blockers)
		}
	}(t)

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "record-written" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run = %v", err)
		}
		ReparentStepHook = nil
		reparentWorktreeInventoryHook = func(string) WorktreeInventory {
			return worktreeInventoryUnavailable(errors.New("injected unavailable inventory"))
		}
		_, err := ContinueReparent(in)
		reparentWorktreeInventoryHook = nil
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalHolderUnsafe {
			t.Fatalf("continue = %v, want holder-unsafe", err)
		}
		if _, abortErr := AbortReparent(in); abortErr != nil {
			t.Fatalf("abort after restoring the probe: %v", abortErr)
		}
	}(t)
}

func testReparentPostLockApprovalBindsExactStackBytes(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	in, plan := w.approvedInput()
	_, req, err := PlanReparent(in)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "post-lock-plan-approved" {
			return os.WriteFile(StackPath(w.Loc.FeaturePath), append(original, []byte("# raced after approval\n")...), 0o644)
		}
		return nil
	}
	_, err = BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req})
	ReparentStepHook = nil
	var guard *PlanGuardRefusalError
	if !errors.As(err, &guard) || guard.Kind != string(RefusalRevalidationMismatch) {
		t.Fatalf("begin = %v, want revalidation-mismatch", err)
	}
	if LoadReparentState(w.Loc).Kind != ReparentStateAbsent {
		t.Fatal("the exact-byte mismatch must write no authoritative state")
	}
	if _, statErr := os.Stat(SyncRunGuardPath(w.Loc.FeaturePath)); !os.IsNotExist(statErr) {
		t.Fatal("the exact-byte mismatch must release the lock")
	}
	if _, statErr := os.Stat(SyncRunStatePath(w.Loc.FeaturePath)); !os.IsNotExist(statErr) {
		t.Fatal("the exact-byte mismatch must write no compatibility state")
	}
}

func testReparentPostLockSyncStateRecheckExcludesOnlyOwnLock(t *testing.T) {
	for _, mode := range []WorkspaceMode{ModeExternal, ModeCheckout} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, mode)
			in, plan := w.approvedInput()
			_, req, err := PlanReparent(in)
			if err != nil {
				t.Fatal(err)
			}
			foreign := []byte("feature: foreign\nstate_version: 2\n")
			var path string
			if mode == ModeExternal {
				path = SyncStatePath(w.Loc.FeaturePath)
			} else {
				path = CheckoutTransactionPath(w.Loc.FeaturePath)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "post-lock-resnapshot" {
					return os.WriteFile(path, foreign, 0o600)
				}
				return nil
			}
			_, err = BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req})
			ReparentStepHook = nil
			var refusal *ReparentRefusalError
			if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalSyncStatePresent {
				t.Fatalf("begin = %v, want sync-state-present", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != string(foreign) {
				t.Fatalf("foreign state changed: %v %q", readErr, got)
			}
			if LoadReparentState(w.Loc).Kind != ReparentStateAbsent {
				t.Fatal("the post-lock refusal wrote authoritative state")
			}
			lockPath := SyncRunGuardPath(w.Loc.FeaturePath)
			if mode == ModeCheckout {
				lockPath = CheckoutLockPath(w.Loc.FeaturePath)
			}
			if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
				t.Fatal("the post-lock refusal must release only its newly acquired lock")
			}
		}(t)
	}
}

func testReparentRecoveryRefusesForeignCompatibilityBeforeMutation(t *testing.T) {
	for _, mode := range []WorkspaceMode{ModeExternal, ModeCheckout} {
		for _, verb := range []string{"continue", "abort"} {
			func(t *testing.T) {
				w := newReparentWorkspace(t, mode)
				in, _ := w.approvedInput()
				ReparentStepHook = func(stage ReparentStage, step string) error {
					if step == "pins-written" {
						return errSimulatedCrash
					}
					return nil
				}
				run := w.begin(in)
				if err := RunReparent(run); err != errSimulatedCrash {
					t.Fatalf("run = %v", err)
				}
				ReparentStepHook = nil
				foreign := []byte("feature: foreign\nstate_version: 2\n")
				path := SyncStatePath(w.Loc.FeaturePath)
				if mode == ModeCheckout {
					path = CheckoutTransactionPath(w.Loc.FeaturePath)
				}
				if err := os.WriteFile(path, foreign, 0o600); err != nil {
					t.Fatal(err)
				}
				refsBefore := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
				stackBefore, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
				if err != nil {
					t.Fatal(err)
				}
				if verb == "continue" {
					_, err = ContinueReparent(in)
				} else {
					_, err = AbortReparent(in)
				}
				var refusal *ReparentRefusalError
				if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalSyncStatePresent {
					t.Fatalf("%s = %v, want sync-state-present", verb, err)
				}
				if got := w.Repo.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsBefore {
					t.Fatal("recovery changed a public ref before classifying compatibility ownership")
				}
				stackAfter, _ := os.ReadFile(StackPath(w.Loc.FeaturePath))
				if string(stackAfter) != string(stackBefore) {
					t.Fatal("recovery changed metadata before classifying compatibility ownership")
				}
				if got, _ := os.ReadFile(path); string(got) != string(foreign) {
					t.Fatal("recovery overwrote foreign compatibility bytes")
				}
			}(t)
		}
	}
}

func testReparentJITRechecksDestinationAncestryAndMerges(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook func(st *ReparentState, args []string) (reparentGitResult, error, bool)
		want ReparentRefusalKind
	}{
		{
			name: "destination",
			hook: func(st *ReparentState, args []string) (reparentGitResult, error, bool) {
				needle := st.NewParentSHA + "^{commit}"
				if len(args) > 0 && args[0] == "rev-parse" && containsString(args, needle) {
					err := errors.New("destination probe failed")
					return reparentGitResult{ExitCode: 2, Stderr: []byte(err.Error())}, err, true
				}
				return reparentGitResult{}, nil, false
			},
			want: ReparentRefusalProbeFailed,
		},
		{
			name: "cutoff ancestry",
			hook: func(_ *ReparentState, args []string) (reparentGitResult, error, bool) {
				if len(args) > 0 && args[0] == "merge-base" {
					return reparentGitResult{ExitCode: 1}, errors.New("exit status 1"), true
				}
				return reparentGitResult{}, nil, false
			},
			want: ReparentRefusalCutoffNotAncestor,
		},
		{
			name: "merge count",
			hook: func(_ *ReparentState, args []string) (reparentGitResult, error, bool) {
				if len(args) > 2 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "--merges" {
					return reparentGitResult{ExitCode: 0, Stdout: []byte("1\n")}, nil, true
				}
				return reparentGitResult{}, nil, false
			},
			want: ReparentRefusalMergeCommitInReplaySet,
		},
	} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			in, _ := w.approvedInput()
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "pins-written" {
					return errSimulatedCrash
				}
				return nil
			}
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("run = %v", err)
			}
			ReparentStepHook = nil
			st := LoadReparentState(w.Loc).State
			reparentGitFaultHook = func(_ string, args []string) (reparentGitResult, error, bool) {
				return tc.hook(st, args)
			}
			_, err := ContinueReparent(in)
			reparentGitFaultHook = nil
			var refusal *ReparentRefusalError
			if !asReparentRefusal(err, &refusal) || refusal.Kind != tc.want || !refusal.StatePreserved {
				t.Fatalf("continue = %+v, want state-preserved %s", err, tc.want)
			}
			if _, abortErr := AbortReparent(in); abortErr != nil {
				t.Fatalf("abort: %v", abortErr)
			}
		}(t)
	}
}

func testReparentSymbolicAffectedRefsNeverReachCAS(t *testing.T) {
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		mainBefore := w.Repo.RevParse("main")
		w.Repo.Git("symbolic-ref", "refs/heads/pr3", "refs/heads/main")
		plan, _, err := PlanReparent(w.planInput())
		if err != nil {
			t.Fatal(err)
		}
		if !hasReparentBlocker(plan, ReparentRefusalProbeFailed) {
			t.Fatalf("blockers = %+v, want a direct-ref probe refusal", plan.Blockers)
		}
		if w.Repo.RevParse("main") != mainBefore {
			t.Fatal("planning a symbolic child moved its out-of-closure target")
		}
	}(t)

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "record-written" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run = %v", err)
		}
		ReparentStepHook = nil
		st := LoadReparentState(w.Loc).State
		pr3Before := st.Row("pr3").PreimageSHA
		mainBefore := w.Repo.RevParse("main")
		w.Repo.Git("symbolic-ref", "refs/heads/pr3", "refs/heads/main")

		_, err := ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalProbeFailed {
			t.Fatalf("continue = %v, want direct-ref probe failure", err)
		}
		if w.Repo.RevParse("main") != mainBefore {
			t.Fatal("forward recovery moved the symbolic target")
		}
		_, err = AbortReparent(in)
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalProbeFailed {
			t.Fatalf("abort = %v, want direct-ref probe failure", err)
		}
		if w.Repo.RevParse("main") != mainBefore {
			t.Fatal("abort moved the symbolic target")
		}
		w.Repo.Git("symbolic-ref", "--delete", "refs/heads/pr3")
		w.Repo.Git("update-ref", "refs/heads/pr3", pr3Before)
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("abort after restoring a direct ref: %v", err)
		}
	}(t)
}

func testReparentComputedAndPinnedRowsBindHEADAndPin(t *testing.T) {
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "computed:pr2" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run = %v", err)
		}
		ReparentStepHook = nil
		gitInTest(t, run.State.ScratchPath, "switch", "--detach", "main")
		_, err := ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalHolderUnsafe || !refusal.StatePreserved {
			t.Fatalf("continue = %+v, want state-preserved holder-unsafe", err)
		}
		gitInTest(t, run.State.ScratchPath, "switch", "--detach", run.State.Row("pr2").PlannedNewSHA)
		if _, err := AbortReparent(in); err != nil {
			t.Fatal(err)
		}
	}(t)

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		limit := 50
		in := w.planInput()
		in.Validation = PlanValidationIdentity{
			Applies: true, Source: "config-workspace",
			Command: "git switch --detach HEAD^ >/dev/null",
			Digest:  ValidationDigest("git switch --detach HEAD^ >/dev/null"),
		}
		in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
		plan, _, err := PlanReparent(in)
		if err != nil || plan.Approval.Fingerprint == nil {
			t.Fatalf("plan: %v %+v", err, plan.Blockers)
		}
		in.Invocation = ReparentInvocationExecute
		in.Guard.Approve = *plan.Approval.Fingerprint
		run := w.begin(in)
		err = RunReparent(run)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalValidationFailed || !refusal.StatePreserved {
			t.Fatalf("validation = %+v, want state-preserved validation-failed", err)
		}
		gitInTest(t, run.State.ScratchPath, "switch", "--detach", run.State.Row("pr2").PlannedNewSHA)
		if _, err := AbortReparent(in); err != nil {
			t.Fatal(err)
		}
	}(t)
}

func testReparentHolderDetachIntentRecoversBothWindows(t *testing.T) {
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		holder := filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
		w.Repo.Git("worktree", "add", "-q", holder, "pr3")
		in, _ := w.approvedInput()
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if strings.HasPrefix(step, "holder-detached-before-complete:") {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run = %v", err)
		}
		ReparentStepHook = nil
		st := LoadReparentState(w.Loc).State
		var recorded *ReparentStateHolder
		for i := range st.DetachedHolders {
			if canonicalize(st.DetachedHolders[i].Path) == canonicalize(holder) {
				recorded = &st.DetachedHolders[i]
			}
		}
		if recorded == nil || recorded.DetachStatus != ReparentHolderDetached ||
			recorded.SwitchIntent != nil {
			t.Fatalf("detachment completion was not durable before the crash hook: %+v", st.DetachedHolders)
		}
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("abort: %v", err)
		}
		if got := strings.TrimSpace(gitInTest(t, holder, "symbolic-ref", "--short", "HEAD")); got != "pr3" {
			t.Fatalf("holder remained stranded: %q", got)
		}
	}(t)

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		holder := filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
		w.Repo.Git("worktree", "add", "-q", holder, "pr3")
		hooks := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks"
		flag := filepath.Join(canonicalize(t.TempDir()), "veto-once")
		writeExecutableForTest(t, hooks+"/reference-transaction",
			"#!/bin/sh\n"+
				"if [ \"$1\" = \"prepared\" ]; then\n"+
				"  while read -r old new ref; do\n"+
				"    case \"$ref\" in refs/heads/pr3)\n"+
				"      if [ ! -f "+flag+" ]; then : > "+flag+"; exit 1; fi;;\n"+
				"    esac\n"+
				"  done\n"+
				"fi\n"+
				"exit 0\n")
		in, _ := w.approvedInput()
		run := w.begin(in)
		if err := RunReparent(run); err == nil {
			t.Fatal("the first transaction must be vetoed")
		}
		load := LoadReparentState(w.Loc)
		if load.Kind != ReparentStateOK {
			t.Fatalf("state after transaction veto = %+v", load)
		}
		st := load.State
		if st.Stage != ReparentStageFailed || st.ResumeStage != ReparentStageDetachingHolders {
			t.Fatalf("transaction veto recovery stage = %s/%s", st.Stage, st.ResumeStage)
		}
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if strings.HasPrefix(step, "holder-detached-before-complete:") {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := ContinueReparent(in); err != errSimulatedCrash {
			t.Fatalf("continue = %v", err)
		}
		ReparentStepHook = nil
		st = LoadReparentState(w.Loc).State
		found := false
		for _, h := range st.DetachedHolders {
			if canonicalize(h.Path) == canonicalize(holder) &&
				h.DetachStatus == ReparentHolderDetached && h.SwitchIntent == nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("redetach intent was not durable: %+v", st.DetachedHolders)
		}
		if _, err := ContinueReparent(in); err != nil {
			t.Fatalf("second continue: %v", err)
		}
		if got := strings.TrimSpace(gitInTest(t, holder, "symbolic-ref", "--short", "HEAD")); got != "pr3" {
			t.Fatalf("holder remained stranded after redetach recovery: %q", got)
		}
	}(t)

	for _, verb := range []string{"continue", "abort"} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, ModeCheckout)
			original := w.Repo.RevParse("HEAD")
			w.Repo.Git("switch", "--detach", original)
			in, _ := w.approvedInput()
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "computation-detach-intent-written" {
					return errSimulatedCrash
				}
				return nil
			}
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("%s setup = %v", verb, err)
			}
			ReparentStepHook = nil
			st := LoadReparentState(w.Loc).State
			if len(st.DetachedHolders) != 1 {
				t.Fatalf("%s detached holders = %+v", verb, st.DetachedHolders)
			}
			holder := st.DetachedHolders[0]
			if !st.OriginalDetached || holder.HolderKind != "computation-context" ||
				holder.DetachStatus != ReparentHolderDetachIntent ||
				holder.DetachTargetSHA == "" || holder.SwitchIntent == nil ||
				holder.SwitchIntent.SourceSHA != original ||
				holder.SwitchIntent.DestinationSHA != holder.DetachTargetSHA ||
				w.Repo.RevParse("HEAD") != original {
				t.Fatalf("%s pre-switch intent = state:%+v holder:%+v head:%s", verb, st, holder, w.Repo.RevParse("HEAD"))
			}
			var err error
			if verb == "continue" {
				_, err = ContinueReparent(in)
			} else {
				_, err = AbortReparent(in)
			}
			if err != nil {
				t.Fatalf("%s from initially-detached pre-switch intent: %v", verb, err)
			}
			if got := w.Repo.RevParse("HEAD"); got != original {
				t.Fatalf("%s restored detached HEAD %s, want %s", verb, got, original)
			}
			if _, symErr := runReparentGit(w.Repo.Dir, nil, "symbolic-ref", "--quiet", "HEAD"); symErr == nil {
				t.Fatalf("%s attached an originally detached checkout", verb)
			}
			if HasReparentState(w.Loc) {
				t.Fatalf("%s left authoritative state", verb)
			}
		}(t)
	}
}

func testReparentHolderSafetyIsRecheckedImmediatelyBeforeCAS(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	holder := filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
	w.Repo.Git("worktree", "add", "-q", holder, "pr3")
	in, _ := w.approvedInput()
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "record-written" {
			return errSimulatedCrash
		}
		return nil
	}
	run := w.begin(in)
	if err := RunReparent(run); err != errSimulatedCrash {
		t.Fatalf("run = %v", err)
	}
	ReparentStepHook = nil
	if err := os.WriteFile(filepath.Join(holder, "pr3.txt"), []byte("dirty after approval\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ContinueReparent(in)
	var refusal *ReparentRefusalError
	if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalContextDirty {
		t.Fatalf("continue = %v, want context-dirty", err)
	}
	gitInTest(t, holder, "checkout", "--", "pr3.txt")
	if _, err := AbortReparent(in); err != nil {
		t.Fatalf("abort: %v", err)
	}
}

func testReparentAllNoopCASRequiresPersistedSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(t *testing.T, w *reparentWorkspace)
	}{
		{
			name: "crash before git",
			arm: func(t *testing.T, w *reparentWorkspace) {
				ReparentStepHook = func(stage ReparentStage, step string) error {
					if step == "before-cas" {
						return errSimulatedCrash
					}
					return nil
				}
				t.Cleanup(func() { ReparentStepHook = nil })
			},
		},
		{
			name: "hook veto",
			arm: func(t *testing.T, w *reparentWorkspace) {
				hooks := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks"
				flag := filepath.Join(canonicalize(t.TempDir()), "veto-once")
				writeExecutableForTest(t, hooks+"/reference-transaction",
					"#!/bin/sh\nif [ \"$1\" = \"prepared\" ] && [ ! -f "+flag+" ]; then : > "+flag+"; exit 1; fi\nexit 0\n")
			},
		},
	} {
		func(t *testing.T) {
			w := newReparentNoopWorkspace(t)
			hookLog := filepath.Join(canonicalize(t.TempDir()), "transactions.log")
			hooks := strings.TrimSpace(w.Repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks"
			if tc.name == "crash before git" {
				writeExecutableForTest(t, hooks+"/reference-transaction",
					"#!/bin/sh\necho \"state=$1\" >> "+hookLog+"\ncat >> "+hookLog+"\nexit 0\n")
			}
			tc.arm(t, w)
			limit := 50
			in := w.planInput()
			in.Target = "leaf"
			in.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
			plan, _, err := PlanReparent(in)
			if err != nil || plan.Approval.Fingerprint == nil {
				t.Fatalf("plan: %v %+v", err, plan.Blockers)
			}
			in.Invocation = ReparentInvocationExecute
			in.Guard.Approve = *plan.Approval.Fingerprint
			run := w.begin(in)
			firstErr := RunReparent(run)
			if firstErr == nil {
				t.Fatal("the first attempt must not succeed")
			}
			ReparentStepHook = nil
			st := LoadReparentState(w.Loc).State
			if st.CASTransactionSucceeded {
				t.Fatal("a pre-child crash or hook veto must not set the success marker")
			}

			if tc.name == "hook veto" {
				// Replace the one-shot veto with an observer for the retry.
				writeExecutableForTest(t, hooks+"/reference-transaction",
					"#!/bin/sh\necho \"state=$1\" >> "+hookLog+"\ncat >> "+hookLog+"\nexit 0\n")
			}
			if _, err := ContinueReparent(in); err != nil {
				t.Fatalf("continue: %v", err)
			}
			data, err := os.ReadFile(hookLog)
			if err != nil {
				t.Fatalf("the retry never reached update-ref: %v", err)
			}
			if !strings.Contains(string(data), "state=prepared") || !strings.Contains(string(data), "refs/heads/leaf") {
				t.Fatalf("the all-noop verify transaction was not re-run:\n%s", data)
			}
		}(t)
	}
}

func testReparentRemoteReplacementAbortRestoresPriorRun(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	remoteParent := canonicalize(t.TempDir())
	remote := filepath.Join(remoteParent, "origin.git")
	gitInTest(t, remoteParent, "init", "-q", "--bare", remote)
	w.Repo.Git("remote", "add", "origin", remote)
	w.Repo.Git("push", "-q", "origin", "pr2", "pr3")
	w.Repo.Git("fetch", "-q", "origin")

	firstInput, firstPlan := w.approvedInput()
	first := w.begin(firstInput)
	if err := RunReparent(first); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if firstPlan.Approval.Fingerprint == nil {
		t.Fatal("first plan must have been approved")
	}
	prior, present, err := ReadReparentRemoteRecordBytes(w.Loc)
	if err != nil || !present {
		t.Fatalf("first run must leave a pending record: %v", err)
	}
	priorRecord, err := LoadReparentRemoteRecord(w.Loc)
	if err != nil || priorRecord == nil {
		t.Fatalf("load prior record: %v", err)
	}
	w.Repo.Git("update-ref", "-d", "refs/remotes/origin/pr2")
	w.Repo.Git("update-ref", "-d", "refs/remotes/origin/pr3")

	second := w.planInput()
	second.OntoToken = "pr1"
	second.OntoKind = "entry"
	limit := 50
	second.Guard = CheckoutPlanGuard{MaxPerEntry: &limit}
	plan, _, err := PlanReparent(second)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Approval.Fingerprint == nil {
		t.Fatalf("second plan = %+v", plan.Blockers)
	}
	second.Invocation = ReparentInvocationExecute
	second.Guard.Approve = *plan.Approval.Fingerprint
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "record-written" {
			return errSimulatedCrash
		}
		return nil
	}
	run := w.begin(second)
	if err := RunReparent(run); err != errSimulatedCrash {
		t.Fatalf("second run = %v", err)
	}
	ReparentStepHook = nil
	merged, err := LoadReparentRemoteRecord(w.Loc)
	if err != nil || merged == nil {
		t.Fatalf("load merged record: %v", err)
	}
	for _, name := range []string{"pr2", "pr3"} {
		before, _ := priorRecord.Entry(name)
		after, _ := merged.Entry(name)
		if before.Pending() && (!after.Pending() || after.RemoteSHAAtWrite != before.RemoteSHAAtWrite) {
			t.Fatalf("%s lost prior publication evidence while tracking was missing: before=%+v after=%+v", name, before, after)
		}
	}
	if _, err := AbortReparent(second); err != nil {
		t.Fatalf("abort second run: %v", err)
	}
	restored, present, err := ReadReparentRemoteRecordBytes(w.Loc)
	if err != nil || !present {
		t.Fatalf("prior record must be restored: %v", err)
	}
	if string(restored) != string(prior) {
		t.Fatalf("the second run's abort did not restore the prior protection exactly:\n--- got ---\n%s\n--- want ---\n%s", restored, prior)
	}

	committed := w.begin(second)
	if err := RunReparent(committed); err != nil {
		t.Fatalf("commit second run: %v", err)
	}
	rec, err := LoadReparentRemoteRecord(w.Loc)
	if err != nil || rec == nil {
		t.Fatalf("committed replacement record: %v", err)
	}
	if decision := ReparentPushDecisionFor(rec, "pr2"); !decision.Applies || !decision.ForceIfIncludes {
		t.Fatalf("next push lost pending protection: %+v", decision)
	}
	w.Repo.Git("fetch", "-q", "origin")
	for _, entry := range rec.Entries {
		w.Repo.Git("switch", "-q", entry.GitBranch)
		w.Repo.Git("merge", "-q", "-s", "ours", "--no-edit", entry.RemoteRef)
	}
	w.Repo.Git("switch", "-q", "main")
	for _, entry := range rec.Entries {
		w.Repo.Git("push", "--force-with-lease", "--force-if-includes", "origin", entry.GitBranch)
	}
	w.Repo.Git("fetch", "-q", "origin")
	stack := w.loadStack()
	if _, err := ApplyReparentRemoteClears(w.Loc, w.Repo.Dir, &stack); err != nil {
		t.Fatalf("post-fetch clear: %v", err)
	}
	if _, err := os.Stat(ReparentRemoteRecordPath(w.Loc)); !os.IsNotExist(err) {
		t.Fatalf("positive fetch observation did not clear committed protection: %v", err)
	}
}

func TestReparentRemoteProofIsClosureScoped(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	w.Repo.Git("branch", "pr9", "pr1")
	stack := w.loadStack()
	stack.Branches = append(stack.Branches, StackEntry{
		Name: "pr9", Base: "pr1", LastBaseSHA: w.Repo.RevParse("pr1"),
	})
	if err := SaveStack(w.Loc.FeaturePath, stack); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"pr2", "pr3"} {
		w.Repo.Git("update-ref", "refs/remotes/origin/"+branch, w.Repo.RevParse(branch))
	}
	main := w.Repo.RevParse("main")
	w.Repo.Git("update-ref", "refs/remotes/origin/pr9", main)
	prior := ReparentRemoteRecord{
		Feature: w.Loc.Feature, RunID: strings.Repeat("9", 32),
		Entries: []ReparentRemoteEntry{{
			Name: "pr9", GitBranch: "pr9", Remote: ReparentRemoteName,
			RemoteRef: "refs/remotes/origin/pr9", NewTipSHA: w.Repo.RevParse("pr9"),
			RemoteSHAAtWrite: main, State: ReparentRemoteStatePending,
		}},
	}
	if err := SaveReparentRemoteRecord(w.Loc, prior); err != nil {
		t.Fatal(err)
	}

	in, _ := w.approvedInput()
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "record-written" {
			return errSimulatedCrash
		}
		return nil
	}
	run := w.begin(in)
	if err := RunReparent(run); err != errSimulatedCrash {
		t.Fatalf("record-stage crash = %v", err)
	}
	ReparentStepHook = nil
	load := LoadReparentState(w.Loc)
	if load.Kind != ReparentStateOK {
		t.Fatalf("merged record corrupted authoritative state: %+v", load)
	}
	if strings.Join(load.State.RemoteFollowupEntries, ",") != "pr2,pr3" {
		t.Fatalf("this run adopted disjoint pending rows: %v", load.State.RemoteFollowupEntries)
	}
	record, err := LoadReparentRemoteRecord(w.Loc)
	if err != nil || record == nil {
		t.Fatalf("load merged record: %v", err)
	}
	if entry, ok := record.Entry("pr9"); !ok || !entry.Pending() {
		t.Fatalf("the prior disjoint protection was not preserved: %+v", record)
	}

	w.Repo.Git("update-ref", "refs/remotes/origin/pr2", load.State.Row("pr2").PlannedNewSHA)
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "remote-clear-intent-written" {
			return errSimulatedCrash
		}
		return nil
	}
	if _, err := run.applyRemoteClearsJournaled(&stack); err != errSimulatedCrash {
		t.Fatalf("partial-clear crash = %v", err)
	}
	ReparentStepHook = nil
	if err := run.reconcileRemoteClearTransition(); err != nil {
		t.Fatalf("reconcile partial clear: %v", err)
	}
	if err := run.refreshRemoteRecordProof(); err != nil {
		t.Fatalf("refresh closure proof: %v", err)
	}
	load = LoadReparentState(w.Loc)
	if load.Kind != ReparentStateOK {
		t.Fatalf("partial clear corrupted authoritative state: %+v", load)
	}
	if strings.Join(load.State.RemoteFollowupEntries, ",") != "pr3" {
		t.Fatalf("partial clear proof escaped this run's closure: %v", load.State.RemoteFollowupEntries)
	}
	record, err = LoadReparentRemoteRecord(w.Loc)
	if err != nil || record == nil {
		t.Fatalf("load partially cleared record: %v", err)
	}
	if entry, ok := record.Entry("pr9"); !ok || !entry.Pending() {
		t.Fatalf("partial clear discarded prior disjoint protection: %+v", record)
	}
	if _, err := AbortReparent(in); err != nil {
		t.Fatalf("abort closure-scoped proof run: %v", err)
	}
	restored, err := LoadReparentRemoteRecord(w.Loc)
	if err != nil || restored == nil {
		t.Fatalf("load restored prior record: %v", err)
	}
	if len(restored.Entries) != 1 || restored.Entries[0].Name != "pr9" || !restored.Entries[0].Pending() {
		t.Fatalf("abort did not restore only the prior disjoint protection: %+v", restored)
	}
}

func testReparentCheckoutComputationDetachTarget(t *testing.T) {
	for _, verb := range []string{"continue", "abort"} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, ModeCheckout)
			w.Repo.Git("switch", "-q", "pr3")
			in, _ := w.approvedInput()
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if strings.HasPrefix(step, "holder-detached-before-complete:") {
					return errSimulatedCrash
				}
				return nil
			}
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("%s run = %v", verb, err)
			}
			ReparentStepHook = nil
			st := LoadReparentState(w.Loc).State
			global, _, globalErr := ReadCheckoutMutationLock(w.Loc.CheckoutStateDir)
			if globalErr != nil || global.Token != st.OwnerToken || global.Operation != "reparent" {
				t.Fatalf("%s global mutation lock = %+v (%v)", verb, global, globalErr)
			}
			if len(st.DetachedHolders) == 0 {
				t.Fatal("checkout computation detachment intent was not persisted")
			}
			h := st.DetachedHolders[0]
			if h.HolderKind != "computation-context" || h.DetachTargetSHA != st.Row("pr2").PreimageSHA {
				t.Fatalf("computation intent = %+v, want actual pr2 detachment target", h)
			}
			if h.PreimageSHA != st.OriginalHead || h.PreimageSHA == h.DetachTargetSHA {
				t.Fatalf("original holder identity and detachment target must stay distinct: %+v", h)
			}
			if verb == "continue" {
				if _, err := ContinueReparent(in); err != nil {
					t.Fatalf("continue: %v", err)
				}
			} else if _, err := AbortReparent(in); err != nil {
				t.Fatalf("abort: %v", err)
			}
			if branch := strings.TrimSpace(w.Repo.Git("symbolic-ref", "--short", "HEAD")); branch != "pr3" {
				t.Fatalf("%s left the checkout on %q, want original affected descendant pr3", verb, branch)
			}
			if _, _, err := ReadCheckoutMutationLock(w.Loc.CheckoutStateDir); !os.IsNotExist(err) {
				t.Fatalf("%s left the workspace-global mutation lock: %v", verb, err)
			}
		}(t)
	}
}

func testReparentCheckoutRecoveryLockTransferCrash(t *testing.T) {
	rewriteOwners := func(t *testing.T, w *reparentWorkspace, pid int) {
		t.Helper()
		st := LoadReparentState(w.Loc).State
		st.OwnerPID = pid
		if err := SaveReparentState(w.Loc, st); err != nil {
			t.Fatal(err)
		}
		featureLock := LockInfo{PID: pid, Created: reparentNow()}
		data, _ := yaml.Marshal(&featureLock)
		if err := os.WriteFile(CheckoutLockPath(w.Loc.FeaturePath), data, 0o600); err != nil {
			t.Fatal(err)
		}
		global, _, err := ReadCheckoutMutationLock(w.Loc.CheckoutStateDir)
		if err != nil {
			t.Fatal(err)
		}
		global.PID = pid
		data, _ = yaml.Marshal(global)
		if err := os.WriteFile(CheckoutMutationLockPath(w.Loc.CheckoutStateDir), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rewriteTransferredLocks := func(t *testing.T, w *reparentWorkspace, pid int) {
		t.Helper()
		info, err := ReadCheckoutLock(w.Loc.FeaturePath)
		if err != nil {
			t.Fatal(err)
		}
		info.PID = pid
		data, _ := yaml.Marshal(info)
		if err := os.WriteFile(CheckoutLockPath(w.Loc.FeaturePath), data, 0o600); err != nil {
			t.Fatal(err)
		}
		global, _, err := ReadCheckoutMutationLock(w.Loc.CheckoutStateDir)
		if err != nil {
			t.Fatal(err)
		}
		global.PID = pid
		data, _ = yaml.Marshal(global)
		if err := os.WriteFile(CheckoutMutationLockPath(w.Loc.CheckoutStateDir), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, verb := range []string{"continue", "abort"} {
		t.Run("feature-lock-transfer-"+verb, func(t *testing.T) {
			t.Cleanup(func() { ReparentStepHook = nil })
			w := newReparentWorkspace(t, ModeCheckout)
			in, plan := w.approvedInput()
			_, req, err := PlanReparent(in)
			if err != nil {
				t.Fatal(err)
			}
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "artifact-written" {
					return errSimulatedCrash
				}
				return nil
			}
			if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err != errSimulatedCrash {
				t.Fatalf("initial crash setup: %v", err)
			}
			ReparentStepHook = nil
			deadOriginal := reparentSpawnDeadPID(t)
			rewriteOwners(t, w, deadOriginal)
			global, _, err := ReadCheckoutMutationLock(w.Loc.CheckoutStateDir)
			if err != nil {
				t.Fatal(err)
			}
			authoritativeToken := global.Token
			global.Token = strings.Repeat("f", 32)
			data, _ := yaml.Marshal(global)
			if err := os.WriteFile(CheckoutMutationLockPath(w.Loc.CheckoutStateDir), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if verb == "continue" {
				_, err = ContinueReparent(in)
			} else {
				_, err = AbortReparent(in)
			}
			if err == nil || !strings.Contains(err.Error(), string(ReparentRefusalSyncStatePresent)) {
				t.Fatalf("foreign global token was not refused: %v", err)
			}
			if feature, err := ReadCheckoutLock(w.Loc.FeaturePath); err != nil || feature.PID != deadOriginal {
				t.Fatalf("foreign feature lock was stolen: %+v err=%v", feature, err)
			}
			global.Token = authoritativeToken
			data, _ = yaml.Marshal(global)
			if err := os.WriteFile(CheckoutMutationLockPath(w.Loc.CheckoutStateDir), data, 0o600); err != nil {
				t.Fatal(err)
			}

			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "recovery-locks-reclaimed" {
					return errSimulatedCrash
				}
				return nil
			}
			if verb == "continue" {
				_, err = ContinueReparent(in)
			} else {
				_, err = AbortReparent(in)
			}
			if err != errSimulatedCrash {
				t.Fatalf("transfer crash = %v", err)
			}
			ReparentStepHook = nil
			rewriteTransferredLocks(t, w, reparentSpawnDeadPID(t))

			if verb == "continue" {
				_, err = ContinueReparent(in)
			} else {
				_, err = AbortReparent(in)
			}
			if err != nil {
				t.Fatalf("%s after transferred-lock crash: %v", verb, err)
			}
			if HasReparentState(w.Loc) {
				t.Fatalf("%s left authoritative state", verb)
			}
		})
	}
}

func testReparentCASNoDerefRace(t *testing.T) {
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "record-written" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run: %v", err)
		}
		ReparentStepHook = nil
		st := LoadReparentState(w.Loc).State
		mainBefore := w.Repo.RevParse("main")
		ReparentCASBarrier = func() error {
			w.Repo.Git("symbolic-ref", "refs/heads/pr3", "refs/heads/main")
			return nil
		}
		_, err := ContinueReparent(in)
		ReparentCASBarrier = nil
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalRefTransactionMismatch {
			t.Fatalf("forward symbolic race = %v", err)
		}
		if w.Repo.RevParse("main") != mainBefore {
			t.Fatal("forward CAS followed a symbolic ref outside the closure")
		}
		w.Repo.Git("symbolic-ref", "--delete", "refs/heads/pr3")
		w.Repo.Git("update-ref", "refs/heads/pr3", st.Row("pr3").PreimageSHA)
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("cleanup abort: %v", err)
		}
	}(t)

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "after-cas" {
				return errSimulatedCrash
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("run: %v", err)
		}
		ReparentStepHook = nil
		st := LoadReparentState(w.Loc).State
		mainBefore := w.Repo.RevParse("main")
		ReparentCASBarrier = func() error {
			w.Repo.Git("symbolic-ref", "refs/heads/pr3", "refs/heads/main")
			return nil
		}
		_, err := AbortReparent(in)
		ReparentCASBarrier = nil
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalRefTransactionMismatch {
			t.Fatalf("abort symbolic race = %v", err)
		}
		if w.Repo.RevParse("main") != mainBefore {
			t.Fatal("abort CAS followed a symbolic ref outside the closure")
		}
		w.Repo.Git("symbolic-ref", "--delete", "refs/heads/pr3")
		w.Repo.Git("update-ref", "refs/heads/pr3", st.Row("pr3").PlannedNewSHA)
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("retry abort: %v", err)
		}
	}(t)
}

func testReparentAbortHolderRestoreDefersCleanup(t *testing.T) {
	for _, step := range []string{"after-cas", "after-write"} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			holder := filepath.Join(canonicalize(t.TempDir()), "pr3-holder")
			w.Repo.Git("worktree", "add", "-q", holder, "pr3")
			in, _ := w.approvedInput()
			ReparentStepHook = func(stage ReparentStage, got string) error {
				if got == step {
					return errSimulatedCrash
				}
				return nil
			}
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("%s run = %v", step, err)
			}
			ReparentStepHook = nil
			if err := os.WriteFile(filepath.Join(holder, "pr3.txt"), []byte("block restore\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			aborted, err := AbortReparent(in)
			if err != nil {
				t.Fatalf("%s abort: %v", step, err)
			}
			if aborted.State.Stage != ReparentStageAborting || len(aborted.State.HolderRestoreDeferred) == 0 {
				t.Fatalf("%s abort did not preserve retry state: %+v", step, aborted.State)
			}
			if load := LoadReparentState(w.Loc); load.Kind != ReparentStateOK {
				t.Fatalf("holder restore failure did not retain valid authoritative state: %+v", load)
			}
			gitInTest(t, holder, "checkout", "--", "pr3.txt")
			if _, err := AbortReparent(in); err != nil {
				t.Fatalf("%s retry abort: %v", step, err)
			}
			if LoadReparentState(w.Loc).Kind != ReparentStateAbsent {
				t.Fatal("successful holder retry did not clean up")
			}
		}(t)
	}
}

func testReparentCheckoutRejectsRepoFields(t *testing.T) {
	for _, name := range []string{"pr2", "pr3"} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, ModeCheckout)
			stack := w.loadStack()
			for i := range stack.Branches {
				if stack.Branches[i].Name == name {
					stack.Branches[i].Repo = filepath.Join(t.TempDir(), "other")
				}
			}
			data, err := yaml.Marshal(&stack)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(StackPath(w.Loc.FeaturePath), data, 0o644); err != nil {
				t.Fatal(err)
			}
			log := reparentCaptureArgv(t)
			plan, _, err := PlanReparent(w.planInput())
			if err != nil {
				t.Fatal(err)
			}
			if !hasReparentBlocker(plan, ReparentRefusalCrossRepoClosure) {
				t.Fatalf("%s repo blocker = %+v", name, plan.Blockers)
			}
			if len(*log) != 0 {
				t.Fatalf("checkout repo refusal must precede every Git probe, saw %v", *log)
			}
		}(t)
	}
}

func testReparentRecoveryClearOrderingAndRemoteProof(t *testing.T) {
	for _, mutation := range []string{"remove", "archive"} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, ModeExternal)
			w.Repo.Git("update-ref", "refs/remotes/origin/pr2", w.Repo.RevParse("pr2"))
			w.Repo.Git("update-ref", "refs/remotes/origin/pr3", w.Repo.RevParse("pr3"))
			in, _ := w.approvedInput()
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "record-written" {
					return errSimulatedCrash
				}
				return nil
			}
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("%s setup: %v", mutation, err)
			}
			ReparentStepHook = nil
			originalStack, err := os.ReadFile(StackPath(w.Loc.FeaturePath))
			if err != nil {
				t.Fatal(err)
			}
			recordBefore, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
			if err != nil {
				t.Fatal(err)
			}
			stack := w.loadStack()
			if mutation == "remove" {
				var kept []StackEntry
				for _, entry := range stack.Branches {
					if entry.Name != "pr2" {
						kept = append(kept, entry)
					}
				}
				stack.Branches = kept
			} else {
				for i := range stack.Branches {
					if stack.Branches[i].Name == "pr2" {
						stack.Branches[i].Archived = true
					}
				}
			}
			if err := SaveStack(w.Loc.FeaturePath, stack); err != nil {
				t.Fatal(err)
			}
			_, err = ContinueReparent(in)
			var refusal *ReparentRefusalError
			if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalMetadataDrift {
				t.Fatalf("%s drift = %v", mutation, err)
			}
			recordAfter, readErr := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
			if readErr != nil || !bytes.Equal(recordAfter, recordBefore) {
				t.Fatalf("%s drift cleared remote protection: err=%v", mutation, readErr)
			}
			if err := os.WriteFile(StackPath(w.Loc.FeaturePath), originalStack, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ContinueReparent(in); err != nil {
				t.Fatalf("%s restore then continue: %v", mutation, err)
			}
		}(t)
	}

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		w.Repo.Git("update-ref", "refs/remotes/origin/pr2", w.Repo.RevParse("pr2"))
		w.Repo.Git("update-ref", "refs/remotes/origin/pr3", w.Repo.RevParse("pr3"))
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "before-cas")
		record, err := os.ReadFile(ReparentRemoteRecordPath(w.Loc))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(ReparentRemoteRecordPath(w.Loc)); err != nil {
			t.Fatal(err)
		}
		_, err = ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalProbeFailed {
			t.Fatalf("missing pre-CAS remote proof = %v", err)
		}
		if got := w.Repo.RevParse("pr2"); got != st.Row("pr2").PreimageSHA {
			t.Fatal("missing remote proof allowed the CAS")
		}
		if err := os.WriteFile(ReparentRemoteRecordPath(w.Loc), record, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ContinueReparent(in); err != nil {
			t.Fatalf("restored remote proof: %v", err)
		}
	}(t)

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "before-cas")
		if !st.RemoteRecordEmpty {
			t.Fatal("fixture must have a provably empty remote record")
		}
		rec := ReparentRemoteRecord{
			Feature: w.Loc.Feature,
			RunID:   strings.Repeat("f", 32),
			Entries: []ReparentRemoteEntry{{
				Name: "pr2", GitBranch: "pr2", Remote: ReparentRemoteName,
				RemoteRef: "refs/remotes/origin/pr2",
				NewTipSHA: st.Row("pr2").PlannedNewSHA, RemoteSHAAtWrite: strings.Repeat("1", 40),
				PRBaseBefore: "pr1", PRBaseAfter: "main", State: ReparentRemoteStatePending,
			}},
		}
		if err := SaveReparentRemoteRecord(w.Loc, rec); err != nil {
			t.Fatal(err)
		}
		_, err := ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalProbeFailed {
			t.Fatalf("foreign record defeated empty proof = %v", err)
		}
		if err := RemoveReparentRemoteRecord(w.Loc); err != nil {
			t.Fatal(err)
		}
		if _, err := ContinueReparent(in); err != nil {
			t.Fatalf("restored empty proof: %v", err)
		}
	}(t)
}

func testReparentCheckoutIdentityProbeFailures(t *testing.T) {
	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeCheckout)
		head := w.Repo.RevParse("HEAD")
		w.Repo.Git("switch", "--detach", head)
		branch, measured, detached, err := measureReparentOriginal(w.Repo.Dir)
		if err != nil || branch != "" || !detached || measured != head {
			t.Fatalf("detached identity = branch:%q head:%q detached:%v err:%v", branch, measured, detached, err)
		}
	}(t)

	for _, fault := range []string{"symbolic", "head-empty"} {
		func(t *testing.T) {
			w := newReparentWorkspace(t, ModeCheckout)
			reparentGitFaultHook = func(_ string, args []string) (reparentGitResult, error, bool) {
				if fault == "symbolic" && len(args) >= 1 && args[0] == "symbolic-ref" {
					return reparentGitResult{Stderr: []byte("symbolic probe failed\n"), ExitCode: 128}, errors.New("probe failed"), true
				}
				if fault == "head-empty" && len(args) >= 1 && args[0] == "rev-parse" &&
					args[len(args)-1] == "HEAD^{commit}" {
					return reparentGitResult{ExitCode: 0}, nil, true
				}
				return reparentGitResult{}, nil, false
			}
			_, _, _, err := measureReparentOriginal(w.Repo.Dir)
			reparentGitFaultHook = nil
			if err == nil {
				t.Fatalf("%s identity fault was accepted", fault)
			}
		}(t)
	}

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeCheckout)
		run := &ReparentRun{
			RepoRoot: w.Repo.Dir,
			State: &ReparentState{
				OriginalDetached: true,
				DetachedHolders: []ReparentStateHolder{{
					Path: w.Repo.Dir, HolderKind: "computation-context",
				}},
			},
		}
		if err := run.restoreCheckoutContext(); err == nil {
			t.Fatal("restore accepted an empty original checkout identity")
		}
		if run.State.DetachedHolders[0].Restored {
			t.Fatal("failed empty-identity restore was marked complete")
		}
	}(t)
}

func testReparentScratchCleanupFailuresPreserveRecovery(t *testing.T) {
	assertPreserved := func(t *testing.T, w *reparentWorkspace, st *ReparentState) {
		t.Helper()
		if LoadReparentState(w.Loc).Kind != ReparentStateOK {
			t.Fatal("scratch cleanup failure removed authoritative state")
		}
		if pins := strings.TrimSpace(w.Repo.Git("for-each-ref", "--format=%(refname)",
			"refs/tws/reparent/"+st.RunID+"/")); pins == "" {
			t.Fatal("scratch cleanup failure removed recovery pins")
		}
		if _, err := os.Stat(SyncRunGuardPath(w.Loc.FeaturePath)); err != nil {
			t.Fatalf("scratch cleanup failure released the lock: %v", err)
		}
	}

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "holders-restored")
		w.Repo.Git("worktree", "remove", "--force", st.ScratchPath)
		if err := os.Symlink(st.ScratchPath, st.ScratchPath); err != nil {
			t.Fatal(err)
		}
		_, err := ContinueReparent(in)
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalProbeFailed {
			t.Fatalf("scratch stat error = %v", err)
		}
		assertPreserved(t, w, st)
		if err := os.Remove(st.ScratchPath); err != nil {
			t.Fatal(err)
		}
		if _, err := ContinueReparent(in); err != nil {
			t.Fatalf("scratch stat repair: %v", err)
		}
	}(t)

	func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "holders-restored")
		w.Repo.Git("worktree", "remove", "--force", st.ScratchPath)
		reparentGitFaultHook = func(_ string, args []string) (reparentGitResult, error, bool) {
			if len(args) >= 2 && args[0] == "worktree" && args[1] == "prune" {
				return reparentGitResult{Stderr: []byte("prune failed\n"), ExitCode: 128}, errors.New("prune failed"), true
			}
			return reparentGitResult{}, nil, false
		}
		_, err := ContinueReparent(in)
		reparentGitFaultHook = nil
		var refusal *ReparentRefusalError
		if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalProbeFailed {
			t.Fatalf("scratch prune failure = %v", err)
		}
		assertPreserved(t, w, st)
		if _, err := ContinueReparent(in); err != nil {
			t.Fatalf("scratch prune repair: %v", err)
		}
	}(t)
}

func testReparentFreshRefusesWindowOneState(t *testing.T) {
	w := newReparentWorkspace(t, ModeExternal)
	rec := ReparentRemoteRecord{
		Feature: w.Loc.Feature,
		RunID:   strings.Repeat("a", 32),
		Entries: []ReparentRemoteEntry{{
			Name: "pr2", GitBranch: "pr2", Remote: ReparentRemoteName,
			RemoteRef: "refs/remotes/origin/pr2", NewTipSHA: w.Repo.RevParse("pr2"),
			RemoteSHAAtWrite: strings.Repeat("1", 40), PRBaseBefore: "pr1", PRBaseAfter: "main",
			State: ReparentRemoteStatePending,
		}},
	}
	if err := SaveReparentRemoteRecord(w.Loc, rec); err != nil {
		t.Fatal(err)
	}
	in, plan := w.approvedInput()
	_, req, err := PlanReparent(in)
	if err != nil {
		t.Fatal(err)
	}
	ReparentStepHook = func(stage ReparentStage, step string) error {
		if step == "artifact-written" {
			return errSimulatedCrash
		}
		return nil
	}
	t.Cleanup(func() { ReparentStepHook = nil })
	if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err != errSimulatedCrash {
		t.Fatalf("window-one setup = %v", err)
	}
	ReparentStepHook = nil
	st := LoadReparentState(w.Loc).State
	dead := reparentSpawnDeadPID(t)
	st.OwnerPID = dead
	if err := SaveReparentState(w.Loc, st); err != nil {
		t.Fatal(err)
	}
	guard, err := ReadSyncRunGuard(w.Loc.FeaturePath)
	if err != nil {
		t.Fatal(err)
	}
	guard.PID = dead
	data, err := yaml.Marshal(guard)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SyncRunGuardPath(w.Loc.FeaturePath), data, 0o600); err != nil {
		t.Fatal(err)
	}
	stateBefore, _ := os.ReadFile(ReparentStatePath(w.Loc))
	guardBefore, _ := os.ReadFile(SyncRunGuardPath(w.Loc.FeaturePath))
	remoteBefore, _ := os.ReadFile(ReparentRemoteRecordPath(w.Loc))

	freshPlan, _, err := PlanReparent(in)
	if err != nil {
		t.Fatal(err)
	}
	if freshPlan.Runnable || freshPlan.Refusal.Kind == nil ||
		*freshPlan.Refusal.Kind != ReparentRefusalStatePresent ||
		len(freshPlan.Blockers) == 0 ||
		freshPlan.Blockers[0].Kind != ReparentRefusalStatePresent {
		t.Fatalf("fresh plan did not prioritize authoritative recovery state: %+v", freshPlan)
	}
	for _, blocker := range freshPlan.Blockers {
		if blocker.Kind == ReparentRefusalSyncStatePresent {
			t.Fatalf("compatibility evidence outranked authoritative state: %+v", freshPlan.Blockers)
		}
	}

	if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err == nil {
		t.Fatal("fresh execution reclaimed a recoverable window-one run")
	}
	for path, want := range map[string][]byte{
		ReparentStatePath(w.Loc):            stateBefore,
		SyncRunGuardPath(w.Loc.FeaturePath): guardBefore,
		ReparentRemoteRecordPath(w.Loc):     remoteBefore,
	} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("fresh refusal changed %s: %v", path, err)
		}
	}
}

func testReparentPostRenameDurabilityRecovery(t *testing.T) {
	t.Run("authoritative state", func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, plan := w.approvedInput()
		_, req, err := PlanReparent(in)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		SyncStateIOFault = func(op, path string) error {
			if op == SyncIOWriteReparentState {
				calls++
				if calls == 2 {
					return errors.New("post-rename state fault")
				}
			}
			return nil
		}
		if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err == nil {
			t.Fatal("post-rename state fault did not stop begin")
		}
		SyncStateIOFault = nil
		syncs := 0
		previous := reparentRecoverySyncDir
		reparentRecoverySyncDir = func(dir string) error {
			syncs++
			return syncDir(dir)
		}
		t.Cleanup(func() {
			SyncStateIOFault = nil
			reparentRecoverySyncDir = previous
		})
		if _, err := ContinueReparent(in); err != nil {
			t.Fatalf("recover visible authoritative state: %v", err)
		}
		if syncs == 0 {
			t.Fatal("recovery accepted visible authoritative state without syncing its directory")
		}
	})

	t.Run("forward metadata", func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		calls := 0
		SyncStateIOFault = func(op, path string) error {
			if op == SyncIOWriteStack {
				calls++
				if calls == 2 {
					return errors.New("post-rename stack fault")
				}
			}
			return nil
		}
		run := w.begin(in)
		if err := RunReparent(run); err == nil {
			t.Fatal("post-rename stack fault did not stop the run")
		}
		SyncStateIOFault = nil
		syncs := 0
		previous := reparentRecoverySyncDir
		reparentRecoverySyncDir = func(dir string) error {
			syncs++
			return syncDir(dir)
		}
		t.Cleanup(func() {
			SyncStateIOFault = nil
			reparentRecoverySyncDir = previous
		})
		if _, err := ContinueReparent(in); err != nil {
			t.Fatalf("recover visible post-image: %v", err)
		}
		if syncs < 2 {
			t.Fatal("recovery accepted visible post-image without syncing its parent directory")
		}
	})

	t.Run("abort preimage", func(t *testing.T) {
		w := newReparentWorkspace(t, ModeExternal)
		in, _ := w.approvedInput()
		st := reparentStallAt(t, w, in, "before-cas")
		post, err := base64.StdEncoding.DecodeString(st.StackAfterBase64)
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteStackBytesAtomic(w.Loc.FeaturePath, post); err != nil {
			t.Fatal(err)
		}
		calls := 0
		SyncStateIOFault = func(op, path string) error {
			if op == SyncIOWriteStack {
				calls++
				if calls == 2 {
					return errors.New("post-rename abort fault")
				}
			}
			return nil
		}
		if _, err := AbortReparent(in); err == nil {
			t.Fatal("post-rename abort fault did not stop rollback")
		}
		SyncStateIOFault = nil
		syncs := 0
		previous := reparentRecoverySyncDir
		reparentRecoverySyncDir = func(dir string) error {
			syncs++
			return syncDir(dir)
		}
		t.Cleanup(func() {
			SyncStateIOFault = nil
			reparentRecoverySyncDir = previous
		})
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("recover visible abort preimage: %v", err)
		}
		if syncs < 2 {
			t.Fatal("abort recovery accepted visible preimage without syncing its parent directory")
		}
	})

	for _, mode := range []WorkspaceMode{ModeExternal, ModeCheckout} {
		for _, verb := range []string{"continue", "abort"} {
			t.Run("compatibility-"+string(mode)+"-"+verb, func(t *testing.T) {
				w := newReparentWorkspace(t, mode)
				in, plan := w.approvedInput()
				_, req, err := PlanReparent(in)
				if err != nil {
					t.Fatal(err)
				}
				targetPath := CheckoutTransactionPath(w.Loc.FeaturePath)
				if mode == ModeExternal {
					targetPath = SyncStatePath(w.Loc.FeaturePath)
				}
				calls := 0
				SyncStateIOFault = func(op, path string) error {
					if op == SyncIOWriteReparentCompat && path == targetPath {
						calls++
						if calls == 2 {
							return errors.New("post-rename compat fault")
						}
					}
					return nil
				}
				if _, err := BeginReparentRun(ReparentBeginInput{Input: in, Plan: plan, Request: req}); err == nil {
					t.Fatal("post-rename compatibility fault did not stop begin")
				}
				SyncStateIOFault = nil
				syncs := 0
				previous := reparentRecoverySyncDir
				reparentRecoverySyncDir = func(dir string) error {
					syncs++
					return syncDir(dir)
				}
				t.Cleanup(func() {
					SyncStateIOFault = nil
					reparentRecoverySyncDir = previous
				})
				if verb == "continue" {
					_, err = ContinueReparent(in)
				} else {
					_, err = AbortReparent(in)
				}
				if err != nil {
					t.Fatalf("%s visible compatibility envelope: %v", verb, err)
				}
				if syncs < 2 {
					t.Fatal("recovery accepted visible compatibility bytes without syncing their directory")
				}
			})
		}
	}
}

func testReparentOriginalDetachedHeadPin(t *testing.T) {
	for _, verb := range []string{"continue", "abort"} {
		t.Run(verb, func(t *testing.T) {
			w := newReparentWorkspace(t, ModeCheckout)
			w.Repo.Git("switch", "--detach", "main")
			original := w.Repo.Commit("unreferenced-original.txt", "original")
			in, _ := w.approvedInput()
			ReparentStepHook = func(stage ReparentStage, step string) error {
				if step == "context-created" {
					return errSimulatedCrash
				}
				return nil
			}
			t.Cleanup(func() { ReparentStepHook = nil })
			run := w.begin(in)
			if err := RunReparent(run); err != errSimulatedCrash {
				t.Fatalf("%s setup = %v", verb, err)
			}
			ReparentStepHook = nil
			st := LoadReparentState(w.Loc).State
			if st.OriginalHeadPinRef == "" {
				t.Fatal("originally detached checkout has no run-owned HEAD pin")
			}
			if got := w.Repo.RevParse(st.OriginalHeadPinRef); got != original {
				t.Fatalf("original HEAD pin = %s, want %s", got, original)
			}
			w.Repo.Git("reflog", "expire", "--expire=now", "--all")
			w.Repo.Git("gc", "--prune=now")
			if got := w.Repo.RevParse(original); got != original {
				t.Fatalf("original detached commit was collected: %s", got)
			}
			var err error
			if verb == "continue" {
				_, err = ContinueReparent(in)
			} else {
				_, err = AbortReparent(in)
			}
			if err != nil {
				t.Fatalf("%s after aggressive gc: %v", verb, err)
			}
			if got := w.Repo.RevParse("HEAD"); got != original {
				t.Fatalf("%s restored HEAD %s, want %s", verb, got, original)
			}
			if _, ok, err := reparentResolveRef(w.Repo.Dir, st.OriginalHeadPinRef); err != nil || ok {
				t.Fatalf("%s cleanup retained original HEAD pin: ok=%v err=%v", verb, ok, err)
			}
		})
	}

	t.Run("recovery recreates missing pin", func(t *testing.T) {
		w := newReparentWorkspace(t, ModeCheckout)
		w.Repo.Git("switch", "--detach", "main")
		original := w.Repo.Commit("recreate-original.txt", "original")
		in, _ := w.approvedInput()
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "context-created" {
				return errSimulatedCrash
			}
			return nil
		}
		t.Cleanup(func() { ReparentStepHook = nil })
		run := w.begin(in)
		if err := RunReparent(run); err != errSimulatedCrash {
			t.Fatalf("missing-pin setup = %v", err)
		}
		ReparentStepHook = nil
		st := LoadReparentState(w.Loc).State
		w.Repo.Git("update-ref", "-d", st.OriginalHeadPinRef)
		ReparentStepHook = func(stage ReparentStage, step string) error {
			if step == "pins-written" {
				return errSimulatedCrash
			}
			return nil
		}
		if _, err := ContinueReparent(in); err != errSimulatedCrash {
			t.Fatalf("recreate-pin recovery = %v", err)
		}
		ReparentStepHook = nil
		if got := w.Repo.RevParse(st.OriginalHeadPinRef); got != original {
			t.Fatalf("recreated original HEAD pin = %s, want %s", got, original)
		}
		if _, err := AbortReparent(in); err != nil {
			t.Fatalf("cleanup recreated pin: %v", err)
		}
	})

}
