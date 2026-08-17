package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// New-mode teardown (§8.5 steps 5-7) against a real directory: the three state
// artifacts are removed in the documented order, and an interruption between
// two steps leaves exactly the residue that ordering implies.
// ---------------------------------------------------------------------------

// withSyncStepHook installs the external crash-injection seam for one test and
// restores whatever was there before.
func withSyncStepHook(t *testing.T, hook func(stage internal.SyncRunStage, index int) error) {
	t.Helper()
	previous := internal.SyncStepHook
	internal.SyncStepHook = hook
	t.Cleanup(func() { internal.SyncStepHook = previous })
}

// newTeardownState materializes the three new-mode artifacts — sentinel,
// payload, guard — in a real temporary feature directory.
func newTeardownState(t *testing.T) string {
	t.Helper()
	featurePath := t.TempDir()

	if err := internal.ClaimSyncRunGuard(featurePath, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	sentinel := internal.NewSyncState()
	sentinel.FailedBranch = "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock"
	if err := internal.SaveSyncState(featurePath, sentinel); err != nil {
		t.Fatal(err)
	}
	payload := internal.NewSyncRunState("feature", sentinel.FailedBranch, "0123456789abcdef0123456789abcdef", internal.SyncRunPolicy{
		Fetch:       internal.SyncFetchDisabled,
		Propagation: internal.SyncPropagationFull,
		ScopeKind:   internal.SyncScopeAll,
	})
	if err := internal.SaveSyncRunState(featurePath, payload); err != nil {
		t.Fatal(err)
	}
	assertSyncArtifacts(t, featurePath, true, true, true)
	return featurePath
}

func syncArtifactExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

func assertSyncArtifacts(t *testing.T, featurePath string, wantSentinel, wantPayload, wantGuard bool) {
	t.Helper()
	for _, artifact := range []struct {
		label string
		path  string
		want  bool
	}{
		{"sentinel", internal.SyncStatePath(featurePath), wantSentinel},
		{"payload", internal.SyncRunStatePath(featurePath), wantPayload},
		{"guard", internal.SyncRunGuardPath(featurePath), wantGuard},
	} {
		if got := syncArtifactExists(t, artifact.path); got != artifact.want {
			t.Fatalf("%s present = %v, want %v (%s)", artifact.label, got, artifact.want, artifact.path)
		}
	}
}

// TestSyncTeardown_FinalizingHookZeroLeavesSentinelAndGuard pins the {sentinel,
// guard} residue: the payload is already gone, so the feature lands in cell 4.
func TestSyncTeardown_FinalizingHookZeroLeavesSentinelAndGuard(t *testing.T) {
	featurePath := newTeardownState(t)
	injected := fmt.Errorf("injected finalizing crash 0")
	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		if stage == internal.SyncStageFinalizing && index == 0 {
			return injected
		}
		return nil
	})

	err := clearSyncRunState(featurePath, true)
	if err == nil {
		t.Fatal("teardown must propagate the injected finalizing error")
	}
	if err.Error() != injected.Error() {
		t.Fatalf("err = %v, want %v", err, injected)
	}
	assertSyncArtifacts(t, featurePath, true, false, true)

	// The residue is exactly the unrecoverable cell 4 the classifier must see.
	state := internal.ClassifyExternalSyncState(featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 4 {
		t.Fatalf("cell = %d, want 4 (sentinel present, payload absent)", state.Cell)
	}
}

// TestSyncTeardown_FinalizingHookOneLeavesGuardOnly pins the guard-only
// residue: both state documents are gone and only the guard survives.
func TestSyncTeardown_FinalizingHookOneLeavesGuardOnly(t *testing.T) {
	featurePath := newTeardownState(t)
	injected := fmt.Errorf("injected finalizing crash 1")
	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		if stage == internal.SyncStageFinalizing && index == 1 {
			return injected
		}
		return nil
	})

	err := clearSyncRunState(featurePath, true)
	if err == nil || err.Error() != injected.Error() {
		t.Fatalf("err = %v, want %v", err, injected)
	}
	assertSyncArtifacts(t, featurePath, false, false, true)

	state := internal.ClassifyExternalSyncState(featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 1 {
		t.Fatalf("cell = %d, want 1 (no state document survives)", state.Cell)
	}
	if !state.HasGuardFile() {
		t.Fatal("the guard must be the only residue")
	}
}

// TestSyncTeardown_RemovesArtifactsInReverseOrder pins the successful order:
// payload, then sentinel, then guard.
func TestSyncTeardown_RemovesArtifactsInReverseOrder(t *testing.T) {
	featurePath := newTeardownState(t)
	type observation struct {
		sentinel bool
		payload  bool
		guard    bool
	}
	seen := make(map[int]observation)
	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		if stage != internal.SyncStageFinalizing {
			return nil
		}
		seen[index] = observation{
			sentinel: syncArtifactExists(t, internal.SyncStatePath(featurePath)),
			payload:  syncArtifactExists(t, internal.SyncRunStatePath(featurePath)),
			guard:    syncArtifactExists(t, internal.SyncRunGuardPath(featurePath)),
		}
		return nil
	})

	if err := clearSyncRunState(featurePath, true); err != nil {
		t.Fatalf("a clean teardown must succeed: %v", err)
	}
	if got, want := seen[0], (observation{sentinel: true, payload: false, guard: true}); got != want {
		t.Fatalf("at finalizing step 0 the payload alone is gone: got %+v, want %+v", got, want)
	}
	if got, want := seen[1], (observation{sentinel: false, payload: false, guard: true}); got != want {
		t.Fatalf("at finalizing step 1 the guard is still held: got %+v, want %+v", got, want)
	}
	assertSyncArtifacts(t, featurePath, false, false, false)
}

// TestSyncTeardown_ErrorPropagatesThroughTheRun pins that a run whose teardown
// is interrupted fails, keeps the residue the ordering implies, and is visible
// to the next invocation as the unrecoverable cell 4.
func TestSyncTeardown_ErrorPropagatesThroughTheRun(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		if stage == internal.SyncStageFinalizing && index == 0 {
			return fmt.Errorf("injected finalizing crash")
		}
		return nil
	})

	stdout, stderr, exit := runSync(t, f.feature, "--only", "parent")
	if exit == 0 {
		t.Fatalf("a failed teardown must fail the run:\n%s\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "injected finalizing crash") {
		t.Fatalf("stderr = %q, want the propagated teardown error", stderr)
	}
	assertSyncArtifacts(t, f.featurePath, true, false, true)

	// The next invocation sees the partial-state cell and refuses.
	internal.SyncStepHook = nil
	f.detachGuard(t)
	_, stderr, exit = runSync(t, f.feature, "--only", "parent")
	if exit == 0 {
		t.Fatal("cell 4 must refuse a new run")
	}
	if !strings.Contains(stderr, "a scoped sync left partial state") {
		t.Fatalf("stderr = %q, want the cell-4 message", stderr)
	}
}

// ---------------------------------------------------------------------------
// Setup (§8.5 steps 1-3) — the other half of the crash matrix. Each
// initializing hook index leaves exactly the artifacts step ordering implies,
// and the classifier reports exactly the predicted cell.
// ---------------------------------------------------------------------------

// runSetupCrash drives a real new-mode run whose setup is interrupted at the
// given initializing hook index, and returns the resulting stdout/stderr/exit.
func runSetupCrash(t *testing.T, f *scopedFixture, index int) (string, string, int) {
	t.Helper()
	withSyncStepHook(t, func(stage internal.SyncRunStage, i int) error {
		if stage == internal.SyncStageInitializing && i == index {
			return fmt.Errorf("injected initializing crash %d", index)
		}
		return nil
	})
	return runSync(t, f.feature, "--only", "parent", "--no-fetch")
}

// TestSyncSetup_InitializingHookZeroLeavesGuardOnly pins the first crash point
// of §8.5: the guard is claimed, nothing else exists, and the feature lands in
// cell 1 with guard-only residue.
func TestSyncSetup_InitializingHookZeroLeavesGuardOnly(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	before := f.sha(t, "parent")

	stdout, stderr, exit := runSetupCrash(t, f, 0)
	if exit == 0 {
		t.Fatalf("an interrupted setup must fail the run:\n%s\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "injected initializing crash 0") {
		t.Fatalf("stderr = %q, want the propagated setup error", stderr)
	}
	assertSyncArtifacts(t, f.featurePath, false, false, true)
	if got := f.sha(t, "parent"); got != before {
		t.Fatalf("a crash before the sentinel must rebase nothing: %s -> %s", before, got)
	}

	state := internal.ClassifyExternalSyncState(f.featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 1 {
		t.Fatalf("cell = %d, want 1 (no state document was written)", state.Cell)
	}
	if !state.HasGuardFile() {
		t.Fatal("the guard must be the only residue")
	}
}

// TestSyncSetup_InitializingHookOneLeavesSentinelAndGuard pins the second crash
// point: the sentinel exists, the payload does not, and the resulting cell 4 is
// indistinguishable from an interrupted finalization — by design.
func TestSyncSetup_InitializingHookOneLeavesSentinelAndGuard(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)

	if _, _, exit := runSetupCrash(t, f, 1); exit == 0 {
		t.Fatal("an interrupted setup must fail the run")
	}
	assertSyncArtifacts(t, f.featurePath, true, false, true)

	sentinel, err := internal.LoadSyncState(f.featurePath)
	if err != nil {
		t.Fatalf("the sentinel must be readable: %v", err)
	}
	if !strings.HasPrefix(sentinel.FailedBranch, "tws-scoped-sync-") {
		t.Fatalf("sentinel failed_branch = %q, want a marker", sentinel.FailedBranch)
	}

	state := internal.ClassifyExternalSyncState(f.featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 4 {
		t.Fatalf("cell = %d, want 4 (sentinel written, payload not yet)", state.Cell)
	}

	// The next invocation cannot tell setup from teardown, and says so.
	internal.SyncStepHook = nil
	f.detachGuard(t)
	_, stderr, exit := runSync(t, f.feature, "--only", "parent")
	if exit == 0 {
		t.Fatal("cell 4 must refuse a new run")
	}
	if !strings.Contains(stderr, "a scoped sync left partial state") {
		t.Fatalf("stderr = %q, want the cell-4 message", stderr)
	}
}

// TestSyncSetup_InitializingHookTwoLeavesAllThree pins the third crash point:
// sentinel, payload, and guard all exist before the first fetch, which is
// cell 5 with nothing yet rebased.
func TestSyncSetup_InitializingHookTwoLeavesAllThree(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	before := f.sha(t, "parent")

	if _, _, exit := runSetupCrash(t, f, 2); exit == 0 {
		t.Fatal("an interrupted setup must fail the run")
	}
	assertSyncArtifacts(t, f.featurePath, true, true, true)
	if got := f.sha(t, "parent"); got != before {
		t.Fatalf("a crash before the executor must rebase nothing: %s -> %s", before, got)
	}

	state := internal.ClassifyExternalSyncState(f.featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 5 {
		t.Fatalf("cell = %d, want 5 (sentinel + payload)", state.Cell)
	}
	payload, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatalf("the payload must be readable: %v", err)
	}
	if payload.Stage != internal.SyncStageInitializing {
		t.Fatalf("stage = %q, want initializing", payload.Stage)
	}
	if payload.FailedBranch != "" {
		t.Fatalf("failed_branch = %q, want empty: nothing has run yet", payload.FailedBranch)
	}
}

// TestSyncRebaseLoop_CrashLandsInFailedCellFive pins the fourth crash point: an
// interruption inside the rebase loop persists a FAILED payload naming the real
// entry, beside an untouched sentinel, which is a resumable cell 5.
func TestSyncRebaseLoop_CrashLandsInFailedCellFive(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)

	var sentinelDuringRun string
	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		if stage == internal.SyncStageRebasing && index == 0 {
			sentinelDuringRun = readFileString(t, internal.SyncStatePath(f.featurePath))
			return fmt.Errorf("injected rebase crash")
		}
		return nil
	})

	stdout, stderr, exit := runSync(t, f.feature, "--from", "parent", "--no-fetch")
	if exit == 0 {
		t.Fatalf("an interrupted rebase loop must fail the run:\n%s\n%s", stdout, stderr)
	}
	assertSyncArtifacts(t, f.featurePath, true, true, true)

	state := internal.ClassifyExternalSyncState(f.featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 5 {
		t.Fatalf("cell = %d, want 5", state.Cell)
	}
	payload, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatalf("the payload must record the failure: %v", err)
	}
	if payload.Stage != internal.SyncStageFailed {
		t.Fatalf("stage = %q, want failed", payload.Stage)
	}
	if payload.FailedBranch != "parent" {
		t.Fatalf("failed_branch = %q, want the REAL entry parent", payload.FailedBranch)
	}
	if strings.Join(payload.Pending, ",") != "child" {
		t.Fatalf("pending = %v, want the untouched remainder", payload.Pending)
	}

	// A new-mode failure never rewrites the sentinel.
	if got := readFileString(t, internal.SyncStatePath(f.featurePath)); got != sentinelDuringRun {
		t.Fatalf("the sentinel changed across a failure:\n--- during ---\n%s\n--- after ---\n%s", sentinelDuringRun, got)
	}
	if strings.Contains(readFileString(t, internal.SyncStatePath(f.featurePath)), "parent") {
		t.Fatal("the sentinel must never carry a resolvable name")
	}
}

// TestSyncCrashMatrix_HealthyRunNeverYieldsPayloadWithoutSentinel pins the
// normative consequence of the setup/teardown ordering: no crash point of a
// healthy run can produce cell 2.
func TestSyncCrashMatrix_HealthyRunNeverYieldsPayloadWithoutSentinel(t *testing.T) {
	for _, crash := range []struct {
		name  string
		stage internal.SyncRunStage
		index int
	}{
		{"initializing-0", internal.SyncStageInitializing, 0},
		{"initializing-1", internal.SyncStageInitializing, 1},
		{"initializing-2", internal.SyncStageInitializing, 2},
		{"rebasing-0", internal.SyncStageRebasing, 0},
		{"finalizing-0", internal.SyncStageFinalizing, 0},
		{"finalizing-1", internal.SyncStageFinalizing, 1},
	} {
		t.Run(crash.name, func(t *testing.T) {
			f := newScopedFixture(t)
			f.advanceRoot(t)
			withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
				if stage == crash.stage && index == crash.index {
					return fmt.Errorf("injected %s crash %d", crash.stage, crash.index)
				}
				return nil
			})

			if _, _, exit := runSync(t, f.feature, "--only", "parent", "--no-fetch"); exit == 0 {
				t.Fatal("every injected crash must fail the run")
			}
			state := internal.ClassifyExternalSyncState(f.featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: true})
			if state.Cell == 2 {
				t.Fatalf("%s produced {absent, valid}, which only an old --abort or tampering may produce", crash.name)
			}
			// The guard survives every crash point until it is released last.
			if state.Cell != 1 && !state.HasGuardFile() {
				t.Fatalf("%s released the guard before its state documents", crash.name)
			}
		})
	}
}

// literal DeleteSyncState, never fails, and never touches the new-mode
// artifacts or the crash-injection seam.
func TestSyncTeardown_NoFlagBranchIsUnchanged(t *testing.T) {
	featurePath := newTeardownState(t)
	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		t.Errorf("a no-flag teardown must not reach the step hook (%s/%d)", stage, index)
		return fmt.Errorf("unreachable")
	})

	if err := clearSyncRunState(featurePath, false); err != nil {
		t.Fatalf("the no-flag teardown never fails: %v", err)
	}
	assertSyncArtifacts(t, featurePath, false, true, true)
}
