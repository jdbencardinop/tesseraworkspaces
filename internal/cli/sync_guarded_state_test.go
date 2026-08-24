package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// TestSyncGuardedState — the guarded envelope's persistence contract (§13.1,
// §13.6): syncRunStateBirth's zero value is a total no-op, a non-zero birth
// writes the v3 shape with its limits and route, both versions round-trip
// through LoadSyncRunState/SaveSyncRunState, the armed-continuation upgrade
// carries every subject's other fields untouched, and an older release
// refuses the v3 document it cannot understand.
// ---------------------------------------------------------------------------

func intp(v int) *int { return &v }

// ---------------------------------------------------------------------------
// Birth cells
// ---------------------------------------------------------------------------

// TestSyncGuardedState_ZeroBirthIsByteIdentical proves syncRunStateBirth{}'s
// documented no-op claim: every shipped (unguarded) setupSyncRunState call
// site passes it unchanged, and the payload produced carries none of the
// guarded envelope's fields at all — not merely zero values, but their total
// omission from the document (§13.6 rule 2's "never silently defaulted").
func TestSyncGuardedState_ZeroBirthIsByteIdentical(t *testing.T) {
	f := newScopedFixture(t)
	sel := internal.SyncSelection{Repos: []string{"root"}}
	payload, err := setupSyncRunState(f.layout, f.feature, "tws-scoped-sync-00000000000000000000000000000000.lock", "0123456789abcdef0123456789abcdef", sel, false, "", "", syncRunStateBirth{})
	if err != nil {
		t.Fatalf("setupSyncRunState: %v", err)
	}
	if payload.StateVersion != internal.SyncRunStateVersion {
		t.Fatalf("state_version = %d, want the unguarded %d", payload.StateVersion, internal.SyncRunStateVersion)
	}
	if payload.Route != "" {
		t.Fatalf("route = %q, want empty — a zero birth must never set it", payload.Route)
	}
	if payload.MaxReplayPerEntry != nil || payload.MaxReplayTotal != nil {
		t.Fatalf("limits = %v/%v, want both nil", payload.MaxReplayPerEntry, payload.MaxReplayTotal)
	}

	raw, err := os.ReadFile(internal.SyncRunStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "state_version: 2\n") {
		t.Fatalf("on-disk document lacks state_version: 2:\n%s", body)
	}
	for _, absent := range []string{"route:", "max_replay_per_entry:", "max_replay_total:"} {
		if strings.Contains(body, absent) {
			t.Fatalf("on-disk document must omit %q entirely for a zero birth:\n%s", absent, body)
		}
	}
}

// TestSyncGuardedState_GuardedBirthWritesV3WithLimitsAndRoute proves the other
// half of the birth contract: a non-zero birth is applied unconditionally and
// visibly, both in the decoded struct and in the raw document.
func TestSyncGuardedState_GuardedBirthWritesV3WithLimitsAndRoute(t *testing.T) {
	f := newScopedFixture(t)
	sel := internal.SyncSelection{Repos: []string{"root"}}
	birth := syncRunStateBirth{
		StateVersion: internal.SyncRunStateGuardedVersion,
		Route:        internal.RouteNewMode,
		MaxPerEntry:  intp(5),
		MaxTotal:     intp(10),
	}
	payload, err := setupSyncRunState(f.layout, f.feature, "tws-scoped-sync-00000000000000000000000000000000.lock", "0123456789abcdef0123456789abcdef", sel, false, "", "", birth)
	if err != nil {
		t.Fatalf("setupSyncRunState: %v", err)
	}
	if payload.StateVersion != 3 {
		t.Fatalf("state_version = %d, want 3", payload.StateVersion)
	}
	if payload.Route != internal.RouteNewMode {
		t.Fatalf("route = %q, want %q", payload.Route, internal.RouteNewMode)
	}
	if payload.MaxReplayPerEntry == nil || *payload.MaxReplayPerEntry != 5 {
		t.Fatalf("max_replay_per_entry = %v, want 5", payload.MaxReplayPerEntry)
	}
	if payload.MaxReplayTotal == nil || *payload.MaxReplayTotal != 10 {
		t.Fatalf("max_replay_total = %v, want 10", payload.MaxReplayTotal)
	}

	raw, err := os.ReadFile(internal.SyncRunStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{"state_version: 3\n", "route: new-mode\n", "max_replay_per_entry: 5\n", "max_replay_total: 10\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("on-disk document missing %q:\n%s", want, body)
		}
	}

	reloaded, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatalf("LoadSyncRunState must accept a v3 document: %v", err)
	}
	if reloaded.StateVersion != 3 || reloaded.Route != internal.RouteNewMode {
		t.Fatalf("reloaded payload lost its guarded envelope: %+v", reloaded)
	}
}

// ---------------------------------------------------------------------------
// Round trips
// ---------------------------------------------------------------------------

func syncRunStateFixture(version int) *internal.SyncRunState {
	s := internal.NewSyncRunState("feature", "tws-scoped-sync-00000000000000000000000000000000.lock", "0123456789abcdef0123456789abcdef", internal.SyncRunPolicy{
		Fetch:       internal.SyncFetchDisabled,
		Propagation: internal.SyncPropagationFull,
		ScopeKind:   internal.SyncScopeOne,
		Selector:    "child",
	})
	s.Selected = []string{"child"}
	s.Pending = []string{"child"}
	s.Push = true
	s.TestCommand = "go test ./..."
	s.ValidationSource = "config"
	s.Repos = []string{"root"}
	if version == internal.SyncRunStateGuardedVersion {
		s.StateVersion = internal.SyncRunStateGuardedVersion
		s.Route = internal.RouteNewMode
		s.MaxReplayPerEntry = intp(3)
		s.MaxReplayTotal = intp(7)
	}
	return s
}

// TestSyncGuardedState_V2RoundTrip proves the unguarded payload shape is
// unaffected by the guarded envelope's addition: every field but UpdatedAt
// survives a save/load cycle exactly.
func TestSyncGuardedState_V2RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := syncRunStateFixture(internal.SyncRunStateVersion)
	if err := internal.SaveSyncRunState(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := internal.LoadSyncRunState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertSyncRunStateEqualExceptUpdatedAt(t, want, got)
	if got.StateVersion != internal.SyncRunStateVersion {
		t.Fatalf("state_version = %d, want %d", got.StateVersion, internal.SyncRunStateVersion)
	}
}

// TestSyncGuardedState_V3RoundTrip is the guarded twin: the envelope fields
// (route, both limits) round-trip alongside every unguarded field.
func TestSyncGuardedState_V3RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := syncRunStateFixture(internal.SyncRunStateGuardedVersion)
	if err := internal.SaveSyncRunState(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := internal.LoadSyncRunState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertSyncRunStateEqualExceptUpdatedAt(t, want, got)
	if got.StateVersion != internal.SyncRunStateGuardedVersion {
		t.Fatalf("state_version = %d, want %d", got.StateVersion, internal.SyncRunStateGuardedVersion)
	}
	if got.Route != internal.RouteNewMode {
		t.Fatalf("route = %q, want %q", got.Route, internal.RouteNewMode)
	}
	if got.MaxReplayPerEntry == nil || *got.MaxReplayPerEntry != 3 {
		t.Fatalf("max_replay_per_entry = %v, want 3", got.MaxReplayPerEntry)
	}
	if got.MaxReplayTotal == nil || *got.MaxReplayTotal != 7 {
		t.Fatalf("max_replay_total = %v, want 7", got.MaxReplayTotal)
	}
}

func assertSyncRunStateEqualExceptUpdatedAt(t *testing.T, want, got *internal.SyncRunState) {
	t.Helper()
	gotCopy := *got
	gotCopy.UpdatedAt = want.UpdatedAt
	if !reflect.DeepEqual(gotCopy, *want) {
		t.Fatalf("round trip changed a field:\n want=%+v\n  got=%+v", *want, gotCopy)
	}
}

// ---------------------------------------------------------------------------
// Armed-continuation upgrade
// ---------------------------------------------------------------------------

// TestSyncGuardedState_UpgradeCarriesEverySubjectUntouched directly exercises
// upgradeGuardedSyncRunState over three distinct persisted subjects — a fresh
// unfailed payload, a genuinely failed one, and one carrying push/validation
// intent — and proves that only StateVersion/Route/the two limits change: the
// resumed run continues at exactly the point it was interrupted (per the
// function's own doc comment).
func TestSyncGuardedState_UpgradeCarriesEverySubjectUntouched(t *testing.T) {
	birth := syncRunStateBirth{StateVersion: internal.SyncRunStateGuardedVersion, MaxPerEntry: intp(2), MaxTotal: intp(4)}

	subjects := []struct {
		name    string
		payload *internal.SyncRunState
	}{
		{"fresh", internal.NewSyncRunState("feature", "m", "t", internal.SyncRunPolicy{Fetch: internal.SyncFetchEnabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll})},
		{"failed", func() *internal.SyncRunState {
			s := internal.NewSyncRunState("feature", "m", "t", internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationLocalOnly, ScopeKind: internal.SyncScopeOne, Selector: "child"})
			s.Stage = internal.SyncStageFailed
			s.FailedBranch = "child"
			s.Selected = []string{"parent", "child"}
			s.Completed = []string{"parent"}
			s.Pending = []string{"child"}
			return s
		}()},
		{"push-and-validation", func() *internal.SyncRunState {
			s := internal.NewSyncRunState("feature", "m", "t", internal.SyncRunPolicy{Fetch: internal.SyncFetchEnabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll})
			s.Push = true
			s.TestCommand = "make test"
			s.ValidationSource = "config"
			s.Pushed = []string{"root"}
			s.Repos = []string{"root", "extra"}
			return s
		}()},
	}

	for _, subject := range subjects {
		t.Run(subject.name, func(t *testing.T) {
			dir := t.TempDir()
			before := *subject.payload
			if err := upgradeGuardedSyncRunState(dir, subject.payload, birth); err != nil {
				t.Fatalf("upgrade: %v", err)
			}
			after := *subject.payload

			if after.StateVersion != 3 {
				t.Fatalf("state_version = %d, want 3", after.StateVersion)
			}
			if after.Route != internal.RouteNewMode {
				t.Fatalf("route = %q, want %q", after.Route, internal.RouteNewMode)
			}
			if after.MaxReplayPerEntry == nil || *after.MaxReplayPerEntry != 2 {
				t.Fatalf("max_replay_per_entry = %v, want 2", after.MaxReplayPerEntry)
			}
			if after.MaxReplayTotal == nil || *after.MaxReplayTotal != 4 {
				t.Fatalf("max_replay_total = %v, want 4", after.MaxReplayTotal)
			}

			// Reset the four upgraded fields and compare everything else,
			// including UpdatedAt: upgrade does not call SaveSyncRunState's
			// own "refresh" through a second, independent code path — it IS
			// SaveSyncRunState, so UpdatedAt legitimately moves. Compare it
			// only for monotonicity, not equality.
			if after.UpdatedAt < before.UpdatedAt {
				t.Fatalf("updated_at went backwards: before=%s after=%s", before.UpdatedAt, after.UpdatedAt)
			}
			after.StateVersion = before.StateVersion
			after.Route = before.Route
			after.MaxReplayPerEntry = before.MaxReplayPerEntry
			after.MaxReplayTotal = before.MaxReplayTotal
			after.UpdatedAt = before.UpdatedAt
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("upgrade touched an untouched field:\n before=%+v\n after=%+v", before, after)
			}

			reloaded, err := internal.LoadSyncRunState(dir)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if reloaded.StateVersion != 3 || reloaded.Route != internal.RouteNewMode {
				t.Fatalf("the upgrade did not persist: %+v", reloaded)
			}
		})
	}
}

// TestSyncGuardedState_UpgradeArmedContinuationEndToEnd drives the ONE
// production call site (dispatchClassifiedSync's cell-5 "guardOpts.Armed()"
// arm): an unguarded persisted failure resumed with a fresh --max-replay-total
// upgrades in place before the guarded continuation handler ever runs. The
// crash hook fires inside the guarded continuation's own JIT seam, after the
// upgrade already landed on disk but before teardown, so the upgraded
// document survives for inspection.
func TestSyncGuardedState_UpgradeArmedContinuationEndToEnd(t *testing.T) {
	f := newScopedFixture(t)
	// Conflict "parent" (not "child") against "root", scoped with --from
	// parent so "new mode" (guard-based dispatch) engages and "child" stays
	// selected-but-pending: the continuation's loop body — where the
	// guarded JIT seam lives — then actually executes once, for "child".
	writeAndCommit(t, f.wt("root"), "conflict.txt", "from-root\n", "root change")
	writeAndCommit(t, f.wt("parent"), "conflict.txt", "from-parent\n", "parent change")
	if _, _, exit := runSync(t, f.feature, "--from", "parent"); exit == 0 {
		t.Fatal("expected a conflict")
	}
	f.detachGuard(t)
	resolveRebase(t, f.wt("parent"))

	before, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatalf("payload must survive the conflict: %v", err)
	}
	if before.StateVersion != internal.SyncRunStateVersion || before.Route != "" {
		t.Fatalf("fixture must start unguarded: %+v", before)
	}
	if before.FailedBranch != "parent" {
		t.Fatalf("fixture must fail on parent, leaving child pending: %+v", before)
	}

	injectedErr := fmt.Errorf("injected rebasing crash")
	fired := false
	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		if !fired && stage == internal.SyncStageRebasing && index == -1 {
			fired = true
			return injectedErr
		}
		return nil
	})

	_, stderr, exit := runSync(t, f.feature, "--continue", "--max-replay-total", "5")
	if exit == 0 {
		t.Fatalf("the injected hook must fail this invocation; stderr=%q", stderr)
	}
	if !fired {
		t.Fatal("the guarded JIT seam never fired; the fixture left nothing pending to revalidate")
	}

	after, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatalf("the upgraded payload must survive the injected failure: %v", err)
	}
	if after.StateVersion != internal.SyncRunStateGuardedVersion {
		t.Fatalf("state_version = %d, want %d after the armed continuation upgrade", after.StateVersion, internal.SyncRunStateGuardedVersion)
	}
	if after.Route != internal.RouteNewMode {
		t.Fatalf("route = %q, want %q", after.Route, internal.RouteNewMode)
	}
	if after.MaxReplayTotal == nil || *after.MaxReplayTotal != 5 {
		t.Fatalf("max_replay_total = %v, want 5", after.MaxReplayTotal)
	}
	// The run's own identity (marker/token/feature) never moves; the run
	// legitimately progresses past "parent" (now Completed) and stops with
	// a fresh failure on "child", the entry being revalidated when the
	// injected crash hit — that is real execution, not upgrade corruption.
	if after.Marker != before.Marker || after.OwnerToken != before.OwnerToken || after.Feature != before.Feature {
		t.Fatalf("the upgrade must not disturb the run's identity: before=%+v after=%+v", before, after)
	}
	if after.FailedBranch != "child" {
		t.Fatalf("failed_branch = %q, want %q (the entry being revalidated when the hook fired)", after.FailedBranch, "child")
	}
	if len(after.Completed) != 1 || after.Completed[0] != "parent" {
		t.Fatalf("completed = %v, want [parent]", after.Completed)
	}
}

// ---------------------------------------------------------------------------
// Older-release compatibility
// ---------------------------------------------------------------------------

// TestSyncGuardedState_OlderReleaseRefusesV3 proves the declared downgrade
// compatibility of §13.6 rule 3 against a real prior binary whenever one is
// available: a release that predates the guarded envelope only ever wrote and
// only ever accepted state_version 2, so it must refuse a v3 document rather
// than silently misinterpret it.
func TestSyncGuardedState_OlderReleaseRefusesV3(t *testing.T) {
	prior := acquireDowngradeBinary(t)
	if prior.path == "" {
		t.Skipf("no prior binary available (%s); the version guard itself is covered by LoadSyncRunState's own rejection, exercised elsewhere in this package", prior.note)
	}

	f := newScopedFixture(t)
	token := "0123456789abcdef0123456789abcdef"
	marker := "tws-scoped-sync-00000000000000000000000000000000.lock"
	if err := internal.ClaimSyncRunGuard(f.featurePath, token); err != nil {
		t.Fatal(err)
	}
	sentinel := internal.NewSyncState()
	sentinel.FailedBranch = marker
	if err := internal.SaveSyncState(f.featurePath, sentinel); err != nil {
		t.Fatal(err)
	}
	payload := internal.NewSyncRunState(f.feature, marker, token, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll,
	})
	payload.StateVersion = internal.SyncRunStateGuardedVersion
	payload.Route = internal.RouteNewMode
	payload.FailedBranch = "child"
	if err := internal.SaveSyncRunState(f.featurePath, payload); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runPriorBinary(t, prior.path, f.repo, "sync", f.feature)
	if exit == 0 {
		t.Fatalf("a prior release must refuse a v3 payload it cannot decode; stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stderr, "unsupported scoped sync state version 3") {
		t.Fatalf("stderr = %q, want it to name the unsupported version", stderr)
	}
}

// ---------------------------------------------------------------------------
// TestSyncGuardedState (fault matrix) — the injected I/O fault seam
// (internal.SyncStateIOFault, §23.1 item 4). Every closed op label is driven
// through the ONE production caller that consults it, proving the injected
// error is never swallowed and that the artefact it guards is left exactly
// as found. Each fault is scoped to both its op label and its target path,
// so two ops that happen to share a path (every op below that touches
// .sync-state.yaml) can never cross-trigger one another.
// ---------------------------------------------------------------------------

const (
	guardedStateTestMarker = "tws-scoped-sync-00000000000000000000000000000000.lock"
	guardedStateTestToken  = "0123456789abcdef0123456789abcdef"
)

// withSyncStateIOFault installs internal.SyncStateIOFault so it fires only
// for the exact (op, path) pair given, and restores whatever was there
// before (normally nil) on cleanup — the same seam-hygiene convention
// withSyncStepHook (sync_teardown_test.go) already established for
// internal.SyncStepHook.
func withSyncStateIOFault(t *testing.T, op, path string, err error) {
	t.Helper()
	previous := internal.SyncStateIOFault
	internal.SyncStateIOFault = func(gotOp, gotPath string) error {
		if gotOp == op && gotPath == path {
			return err
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncStateIOFault = previous })
}

// TestSyncGuardedState_FaultReadSyncStatePropagates proves the
// SyncIOReadSyncState op fires from, and only from, LoadSyncState's read,
// and that a faulted read never mutates the file it failed to read.
func TestSyncGuardedState_FaultReadSyncStatePropagates(t *testing.T) {
	featurePath := t.TempDir()
	sentinel := internal.NewSyncState()
	sentinel.FailedBranch = guardedStateTestMarker
	if err := internal.SaveSyncState(featurePath, sentinel); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(internal.SyncStatePath(featurePath))
	if err != nil {
		t.Fatal(err)
	}

	injected := fmt.Errorf("injected read-sync-state fault")
	withSyncStateIOFault(t, internal.SyncIOReadSyncState, internal.SyncStatePath(featurePath), injected)

	if _, err := internal.LoadSyncState(featurePath); !errors.Is(err, injected) {
		t.Fatalf("LoadSyncState err = %v, want the injected fault", err)
	}

	after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("a faulted read must never modify the file: before=%q after=%q err=%v", before, after, readErr)
	}
}

// TestSyncGuardedState_FaultReadSyncRunStatePropagates proves the
// SyncIOReadSyncRunState op fires from, and only from, LoadSyncRunState's
// read, and that a faulted read never mutates the payload it failed to read.
func TestSyncGuardedState_FaultReadSyncRunStatePropagates(t *testing.T) {
	featurePath := t.TempDir()
	payload := internal.NewSyncRunState("feature", guardedStateTestMarker, guardedStateTestToken, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll,
	})
	if err := internal.SaveSyncRunState(featurePath, payload); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(internal.SyncRunStatePath(featurePath))
	if err != nil {
		t.Fatal(err)
	}

	injected := fmt.Errorf("injected read-sync-run-state fault")
	withSyncStateIOFault(t, internal.SyncIOReadSyncRunState, internal.SyncRunStatePath(featurePath), injected)

	if _, err := internal.LoadSyncRunState(featurePath); !errors.Is(err, injected) {
		t.Fatalf("LoadSyncRunState err = %v, want the injected fault", err)
	}

	after, readErr := os.ReadFile(internal.SyncRunStatePath(featurePath))
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("a faulted read must never modify the payload: before=%q after=%q err=%v", before, after, readErr)
	}
}

// TestSyncGuardedState_FaultRemoversPropagateDirectly proves each of the
// three error-returning removers — RemoveSyncState, RemoveSyncRunState,
// RemoveSyncRunGuard — propagates its own op label's injected fault, called
// directly rather than through a rollback path.
func TestSyncGuardedState_FaultRemoversPropagateDirectly(t *testing.T) {
	cases := []struct {
		name   string
		op     string
		path   func(string) string
		remove func(string) error
	}{
		{"sentinel remover (RemoveSyncState)", internal.SyncIORemoveSyncState, internal.SyncStatePath, internal.RemoveSyncState},
		{"payload remover (RemoveSyncRunState)", internal.SyncIORemoveSyncRunState, internal.SyncRunStatePath, internal.RemoveSyncRunState},
		{"guard remover (RemoveSyncRunGuard)", internal.SyncIORemoveSyncRunGuard, internal.SyncRunGuardPath, internal.RemoveSyncRunGuard},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			featurePath := t.TempDir()
			injected := fmt.Errorf("injected %s fault", tc.op)
			withSyncStateIOFault(t, tc.op, tc.path(featurePath), injected)

			if err := tc.remove(featurePath); !errors.Is(err, injected) {
				t.Fatalf("%s err = %v, want the injected fault", tc.op, err)
			}
		})
	}
}

// TestSyncGuardedState_FreshRollbackResidueMatchesFaultedRemover drives
// rollbackGuardedRunState (the guarded FRESH claim's own teardown: payload,
// then sentinel, then guard — internal/cli/sync_modes.go) with a fault at
// each of its three removers in turn, over a real guard+sentinel+payload
// fixture built by the shipped setupSyncRunState. Each case proves the
// rollback stops at exactly the faulted step and returns the MEASURED
// residue — never a residue inferred from the step index alone.
func TestSyncGuardedState_FreshRollbackResidueMatchesFaultedRemover(t *testing.T) {
	cases := []struct {
		name        string
		op          string
		path        func(string) string
		wantResidue string
	}{
		{"payload remover fails first: nothing yet removed", internal.SyncIORemoveSyncRunState, internal.SyncRunStatePath, "{payload, sentinel, guard}"},
		{"sentinel remover fails second: payload already gone", internal.SyncIORemoveSyncState, internal.SyncStatePath, "{sentinel, guard}"},
		{"guard remover fails third: payload and sentinel already gone", internal.SyncIORemoveSyncRunGuard, internal.SyncRunGuardPath, "{guard}"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			featurePath := t.TempDir()
			layout := newExternalSyncLayout(featurePath)
			sel := internal.SyncSelection{Repos: []string{"root"}}
			birth := syncRunStateBirth{StateVersion: internal.SyncRunStateGuardedVersion, Route: internal.RouteNewMode}
			if _, err := setupSyncRunState(layout, "feature", guardedStateTestMarker, guardedStateTestToken, sel, false, "", "", birth); err != nil {
				t.Fatalf("fixture setup: %v", err)
			}

			injected := fmt.Errorf("injected %s fault", tc.op)
			withSyncStateIOFault(t, tc.op, tc.path(featurePath), injected)

			residue, err := rollbackGuardedRunState(featurePath)
			if !errors.Is(err, injected) {
				t.Fatalf("rollbackGuardedRunState err = %v, want the injected fault", err)
			}
			if got := residue.String(); got != tc.wantResidue {
				t.Fatalf("residue = %s, want %s", got, tc.wantResidue)
			}
		})
	}
}

// newTestGuardedLegacySentinel builds a minimal, well-formed
// GuardedLegacySentinel — a fresh-route document with no prior legacy state
// — suitable as the s argument SaveGuardedLegacySentinel's conditional
// writer tests need. It sets no time-varying field beyond what
// internal.NewSyncState itself fixes at construction, so two independent
// calls to MarshalGuardedLegacySentinel over the SAME returned value are
// byte-identical.
func newTestGuardedLegacySentinel() *internal.GuardedLegacySentinel {
	s := &internal.GuardedLegacySentinel{
		SyncState:           *internal.NewSyncState(),
		GuardedStateVersion: internal.GuardedLegacySentinelVersion,
		Route:               internal.RouteLegacy,
		Feature:             "feature",
		Marker:              guardedStateTestMarker,
		OwnerToken:          guardedStateTestToken,
		Universe:            []string{"root", "parent", "child"},
		PendingIntent:       []string{"root", "parent", "child"},
	}
	s.FailedBranch = guardedStateTestMarker
	s.Pending = []string{}
	s.Completed = []string{}
	s.Skipped = []string{}
	return s
}

// TestSyncGuardedState_FaultWriteSentinelPropagates proves the
// SyncIOWriteSentinel op fires from, and only from, SaveGuardedLegacySentinel,
// and that a faulted write installs nothing at all.
func TestSyncGuardedState_FaultWriteSentinelPropagates(t *testing.T) {
	featurePath := t.TempDir()

	injected := fmt.Errorf("injected write-sentinel fault")
	withSyncStateIOFault(t, internal.SyncIOWriteSentinel, internal.SyncStatePath(featurePath), injected)

	err := internal.SaveGuardedLegacySentinel(featurePath, newTestGuardedLegacySentinel(), nil)
	if !errors.Is(err, injected) {
		t.Fatalf("SaveGuardedLegacySentinel err = %v, want the injected fault", err)
	}
	if _, statErr := os.Lstat(internal.SyncStatePath(featurePath)); !os.IsNotExist(statErr) {
		t.Fatalf("a faulted sentinel write must write nothing, stat err = %v", statErr)
	}
}

// TestSyncGuardedState_FaultRestoreSyncStatePropagates proves the
// SyncIORestoreSyncState op fires from, and only from, RestoreSyncStateBytes,
// and that a faulted restore leaves the current file untouched.
func TestSyncGuardedState_FaultRestoreSyncStatePropagates(t *testing.T) {
	featurePath := t.TempDir()
	original := []byte("installed sentinel bytes\n")
	if err := os.WriteFile(internal.SyncStatePath(featurePath), original, 0644); err != nil {
		t.Fatal(err)
	}

	injected := fmt.Errorf("injected restore-sync-state fault")
	withSyncStateIOFault(t, internal.SyncIORestoreSyncState, internal.SyncStatePath(featurePath), injected)

	err := internal.RestoreSyncStateBytes(featurePath, []byte("prior legacy bytes\n"), original)
	if !errors.Is(err, injected) {
		t.Fatalf("RestoreSyncStateBytes err = %v, want the injected fault", err)
	}
	after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
	if readErr != nil || string(after) != string(original) {
		t.Fatalf("a faulted restore must leave the file untouched: got=%q want=%q err=%v", after, original, readErr)
	}
}

// TestSyncGuardedState_FaultRemoveStateUnchangedPropagates proves the
// SyncIORemoveStateUnchanged op fires from, and only from,
// RemoveSyncStateIfUnchanged, and that a faulted removal leaves the file
// untouched.
func TestSyncGuardedState_FaultRemoveStateUnchangedPropagates(t *testing.T) {
	featurePath := t.TempDir()
	original := []byte("installed sentinel bytes\n")
	if err := os.WriteFile(internal.SyncStatePath(featurePath), original, 0644); err != nil {
		t.Fatal(err)
	}

	injected := fmt.Errorf("injected remove-state-unchanged fault")
	withSyncStateIOFault(t, internal.SyncIORemoveStateUnchanged, internal.SyncStatePath(featurePath), injected)

	err := internal.RemoveSyncStateIfUnchanged(featurePath, original)
	if !errors.Is(err, injected) {
		t.Fatalf("RemoveSyncStateIfUnchanged err = %v, want the injected fault", err)
	}
	after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
	if readErr != nil || string(after) != string(original) {
		t.Fatalf("a faulted removal must leave the file untouched: got=%q want=%q err=%v", after, original, readErr)
	}
}

// TestSyncGuardedState_FaultWriteSyncRunStatePropagates proves the
// SyncIOWriteSyncRunState op fires from, and only from, SaveSyncRunState,
// and that a faulted write leaves no payload behind at all.
func TestSyncGuardedState_FaultWriteSyncRunStatePropagates(t *testing.T) {
	featurePath := t.TempDir()
	payload := internal.NewSyncRunState("feature", guardedStateTestMarker, guardedStateTestToken, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll,
	})

	injected := fmt.Errorf("injected write-sync-run-state fault")
	withSyncStateIOFault(t, internal.SyncIOWriteSyncRunState, internal.SyncRunStatePath(featurePath), injected)

	if err := internal.SaveSyncRunState(featurePath, payload); !errors.Is(err, injected) {
		t.Fatalf("SaveSyncRunState err = %v, want the injected fault", err)
	}
	if internal.HasSyncRunState(featurePath) {
		t.Fatal("a faulted payload write must leave no payload behind")
	}
}

// TestSyncGuardedState_FaultWriteTransactionPropagates proves the
// SyncIOWriteTransaction op fires from, and only from,
// SaveCheckoutTransaction, and that a faulted write leaves no transaction
// file behind at all. The feature path follows the shipped
// metadataRoot/features/<feature> shape (TestCheckoutStateStoredOutsideFeatureDirectory,
// internal/cli/checkout_sync_test.go) so CheckoutTransactionPath's own
// "../../state" derivation lands under this test's own t.TempDir().
func TestSyncGuardedState_FaultWriteTransactionPropagates(t *testing.T) {
	featurePath := filepath.Join(t.TempDir(), ".tws", "features", "feature")
	if err := os.MkdirAll(featurePath, 0755); err != nil {
		t.Fatal(err)
	}
	tx := &internal.CheckoutTransaction{Feature: "feature", Stage: internal.StagePlanned}

	injected := fmt.Errorf("injected write-checkout-tx fault")
	withSyncStateIOFault(t, internal.SyncIOWriteTransaction, internal.CheckoutTransactionPath(featurePath), injected)

	if err := internal.SaveCheckoutTransaction(featurePath, tx); !errors.Is(err, injected) {
		t.Fatalf("SaveCheckoutTransaction err = %v, want the injected fault", err)
	}
	if internal.HasCheckoutTransaction(featurePath) {
		t.Fatal("a faulted transaction write must leave no transaction file behind")
	}
}

// TestSyncGuardedState_FaultReloadStackWrapsRevalidatePlanEntry proves the
// SyncIOReloadStack op fires from RevalidatePlanEntry's own stack.yaml
// reload seam (§25.102 input 3) — the first thing a COLLATERAL-CLASS seam
// does, before it ever reads a plan input — and that the returned error both
// wraps the injected cause and names the failing step ("reload stack
// identity"). Since the fault fires before any plan input is consulted, a
// mostly-zero request and a collateral-class approved entry are sufficient:
// this function documents that the seam never reaches BuildRebasePlan's
// read-only pipeline before the seam check.
func TestSyncGuardedState_FaultReloadStackWrapsRevalidatePlanEntry(t *testing.T) {
	featurePath := t.TempDir()
	injected := fmt.Errorf("injected reload-stack fault")
	withSyncStateIOFault(t, internal.SyncIOReloadStack, internal.StackPath(featurePath), injected)

	req := internal.RebasePlanRequest{Layout: internal.RebasePlanLayout{FeaturePath: featurePath}}
	_, err := internal.RevalidatePlanEntry(req, collateralClassEntry("a"))
	if err == nil {
		t.Fatal("RevalidatePlanEntry must return the injected fault")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("err = %v, want it to wrap the injected fault", err)
	}
	if !strings.Contains(err.Error(), "reload stack identity") {
		t.Fatalf("err = %q, want it to name the failing step %q", err.Error(), "reload stack identity")
	}
}

// collateralClassEntry is the smallest approved row whose published
// mechanism makes its JIT seam collateral-class (§25.102): the seam then
// owes the stack reload and the ref inventory. Every other member stays
// zero, because the seam predicate reads the mechanism alone.
func collateralClassEntry(name string) internal.PlanEntry {
	mech := "argv"
	return internal.PlanEntry{Name: name, CollateralMechanism: &mech}
}

// switchOnlyEntry is its twin: a row the document published with the
// explicit `none` mechanism, which is what a scoped run's plain `git rebase`
// row carries. Its seam owes neither extra input.
func switchOnlyEntry(name string) internal.PlanEntry {
	mech := "none"
	return internal.PlanEntry{Name: name, CollateralMechanism: &mech}
}

// countSyncStateIOOps installs a counting (never failing) SyncStateIOFault
// hook and returns the accessor for the recorded per-op tallies. It is the
// §23.1 item 4 counting half: the same seam that can fail the reload also
// counts it, so "exactly one reload per collateral seam" and "no reload on a
// switch-only row" are asserted rather than assumed.
func countSyncStateIOOps(t *testing.T) func(op string) int {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int{}
	previous := internal.SyncStateIOFault
	internal.SyncStateIOFault = func(op, _ string) error {
		mu.Lock()
		counts[op]++
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { internal.SyncStateIOFault = previous })
	return func(op string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[op]
	}
}

// TestSyncGuardedState_Criterion22_33i_SeamCountsItsStackReload is the named
// owner of §22.33i (v-e)'s reload-count half. It drives the production JIT
// seam over a real feature path and asserts the two counts §25.102 fixes:
// exactly ONE stack reload per collateral-class seam (never zero, never one
// per plan row), and ZERO on a switch-only row, whose seam owes neither
// extra input. The counts are taken from the production seam itself, so a
// reload hoisted above the collateral test — or dropped altogether — fails
// here.
func TestSyncGuardedState_Criterion22_33i_SeamCountsItsStackReload(t *testing.T) {
	featurePath := t.TempDir()
	stack := internal.Stack{Branches: []internal.StackEntry{{Name: "a"}}}
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}
	req := internal.RebasePlanRequest{Layout: internal.RebasePlanLayout{FeaturePath: featurePath}}

	t.Run("one_reload_per_collateral_seam", func(t *testing.T) {
		count := countSyncStateIOOps(t)
		if _, err := internal.RevalidatePlanEntry(req, collateralClassEntry("a")); err != nil {
			t.Fatalf("RevalidatePlanEntry: %v", err)
		}
		if got := count(internal.SyncIOReloadStack); got != 1 {
			t.Fatalf("stack reloads = %d, want exactly 1 per collateral-class seam", got)
		}
	})

	t.Run("two_collateral_seams_reload_twice_never_shared", func(t *testing.T) {
		count := countSyncStateIOOps(t)
		for _, name := range []string{"a", "b"} {
			if _, err := internal.RevalidatePlanEntry(req, collateralClassEntry(name)); err != nil {
				t.Fatalf("RevalidatePlanEntry(%s): %v", name, err)
			}
		}
		if got := count(internal.SyncIOReloadStack); got != 2 {
			t.Fatalf("stack reloads = %d over two collateral seams, want 2: an input is measured once per seam and never shared across seams", got)
		}
	})

	t.Run("switch_only_row_reloads_nothing", func(t *testing.T) {
		count := countSyncStateIOOps(t)
		if _, err := internal.RevalidatePlanEntry(req, switchOnlyEntry("a")); err != nil {
			t.Fatalf("RevalidatePlanEntry: %v", err)
		}
		if got := count(internal.SyncIOReloadStack); got != 0 {
			t.Fatalf("stack reloads = %d on a switch-only row, want 0", got)
		}
	})

	t.Run("gated_update_refs_row_reloads_nothing", func(t *testing.T) {
		count := countSyncStateIOOps(t)
		// An --update-refs-gated row publishes NO mechanism at all; it is not
		// collateral-class either, so its seam owes no reload.
		if _, err := internal.RevalidatePlanEntry(req, internal.PlanEntry{Name: "a"}); err != nil {
			t.Fatalf("RevalidatePlanEntry: %v", err)
		}
		if got := count(internal.SyncIOReloadStack); got != 0 {
			t.Fatalf("stack reloads = %d on a row with no published mechanism, want 0", got)
		}
	})

	t.Run("unreadable_stack_is_rank_5_9_probe_failed_at_the_guard_seam", func(t *testing.T) {
		// §22.33i (v-e-1): an input the seam could not READ refuses rank 5.9
		// probe-failed through the production guard evaluator, before any Git
		// mutation — never a rank 9 mismatch derived from an unmeasured value.
		scratch := t.TempDir()
		if err := os.WriteFile(internal.StackPath(scratch), []byte("\tnot: [yaml"), 0o644); err != nil {
			t.Fatal(err)
		}
		bad := internal.RebasePlanRequest{Layout: internal.RebasePlanLayout{FeaturePath: scratch}}
		_, err := internal.RevalidatePlanGuardEntry(internal.RevalidatePlanGuardEntryRequest{
			Request: bad, Approved: collateralClassEntry("a"), StatePreserved: true,
		})
		var refusal *internal.PlanGuardRefusalError
		if !errors.As(err, &refusal) {
			t.Fatalf("err = %v (%T), want a *PlanGuardRefusalError", err, err)
		}
		if refusal.Kind != string(internal.RefusalProbeFailed) {
			t.Fatalf("refusal kind = %q, want %q", refusal.Kind, internal.RefusalProbeFailed)
		}
		if !refusal.StatePreserved {
			t.Fatal("the refusal must carry the caller's own state-preserved fact, never invent one")
		}
	})

	t.Run("reload_really_rereads_the_file", func(t *testing.T) {
		// The reload is a real read, not a bookkeeping op: a stack.yaml that
		// became unreadable between admission and the seam refuses the seam
		// rather than silently reusing the admission-time mapping.
		scratch := t.TempDir()
		if err := os.WriteFile(internal.StackPath(scratch), []byte("\tnot: [yaml"), 0o644); err != nil {
			t.Fatal(err)
		}
		bad := internal.RebasePlanRequest{Layout: internal.RebasePlanLayout{FeaturePath: scratch}}
		if _, err := internal.RevalidatePlanEntry(bad, collateralClassEntry("a")); err == nil {
			t.Fatal("an unreadable stack.yaml must refuse the collateral-class seam")
		} else if !strings.Contains(err.Error(), "reload stack identity") {
			t.Fatalf("err = %q, want it to name the reload step", err.Error())
		}
	})
}

// ---------------------------------------------------------------------------
// TestSyncGuardedState (conditional-writer semantics) — SaveGuardedLegacySentinel,
// RestoreSyncStateBytes and RemoveSyncStateIfUnchanged over real files, no
// fault injection: each writer either leaves the file exactly as found, or
// installs exactly the bytes it promises, and never anything in between.
// ---------------------------------------------------------------------------

// TestSyncGuardedState_SaveGuardedLegacySentinelRefusesNilExpectOverExistingFile
// proves expect == nil ("expect the path to be absent") is refused the
// instant a real file is already there, regardless of its content, and that
// the refusal writes nothing.
func TestSyncGuardedState_SaveGuardedLegacySentinelRefusesNilExpectOverExistingFile(t *testing.T) {
	featurePath := t.TempDir()
	original := []byte("pre-existing legacy state\n")
	if err := os.WriteFile(internal.SyncStatePath(featurePath), original, 0644); err != nil {
		t.Fatal(err)
	}

	err := internal.SaveGuardedLegacySentinel(featurePath, newTestGuardedLegacySentinel(), nil)
	if !errors.Is(err, internal.ErrSyncStateChanged) {
		t.Fatalf("err = %v, want ErrSyncStateChanged", err)
	}
	after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
	if readErr != nil || string(after) != string(original) {
		t.Fatalf("a refused write must leave the file untouched: got=%q want=%q err=%v", after, original, readErr)
	}
}

// TestSyncGuardedState_SaveGuardedLegacySentinelRefusesMismatchedExpect proves
// a non-nil expect that does not match the file's current bytes is refused
// exactly like the nil-over-existing cell, and writes nothing.
func TestSyncGuardedState_SaveGuardedLegacySentinelRefusesMismatchedExpect(t *testing.T) {
	featurePath := t.TempDir()
	original := []byte("pre-existing legacy state\n")
	if err := os.WriteFile(internal.SyncStatePath(featurePath), original, 0644); err != nil {
		t.Fatal(err)
	}

	err := internal.SaveGuardedLegacySentinel(featurePath, newTestGuardedLegacySentinel(), []byte("a different captured document\n"))
	if !errors.Is(err, internal.ErrSyncStateChanged) {
		t.Fatalf("err = %v, want ErrSyncStateChanged", err)
	}
	after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
	if readErr != nil || string(after) != string(original) {
		t.Fatalf("a refused write must leave the file untouched: got=%q want=%q err=%v", after, original, readErr)
	}
}

// TestSyncGuardedState_SaveGuardedLegacySentinelSucceedsOnMatchingExpect
// covers the write's one success shape from both directions: a captured
// expect that matches a real existing file, and the fresh route's nil expect
// over an absent path. Both must install exactly
// MarshalGuardedLegacySentinel(s)'s own bytes — not a second, possibly
// divergent encoding of the same struct.
func TestSyncGuardedState_SaveGuardedLegacySentinelSucceedsOnMatchingExpect(t *testing.T) {
	t.Run("existing file matching expect", func(t *testing.T) {
		featurePath := t.TempDir()
		original := []byte("pre-existing legacy state\n")
		if err := os.WriteFile(internal.SyncStatePath(featurePath), original, 0644); err != nil {
			t.Fatal(err)
		}
		sentinel := newTestGuardedLegacySentinel()
		wantBytes, err := internal.MarshalGuardedLegacySentinel(sentinel)
		if err != nil {
			t.Fatal(err)
		}

		if err := internal.SaveGuardedLegacySentinel(featurePath, sentinel, original); err != nil {
			t.Fatalf("a matching expect must succeed: %v", err)
		}
		got, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != string(wantBytes) {
			t.Fatalf("installed bytes = %q, want exactly MarshalGuardedLegacySentinel's output %q", got, wantBytes)
		}
	})

	t.Run("fresh route with nil expect and no existing file", func(t *testing.T) {
		featurePath := t.TempDir()
		sentinel := newTestGuardedLegacySentinel()
		wantBytes, err := internal.MarshalGuardedLegacySentinel(sentinel)
		if err != nil {
			t.Fatal(err)
		}

		if err := internal.SaveGuardedLegacySentinel(featurePath, sentinel, nil); err != nil {
			t.Fatalf("a fresh route with nil expect must succeed: %v", err)
		}
		got, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != string(wantBytes) {
			t.Fatalf("installed bytes = %q, want exactly MarshalGuardedLegacySentinel's output %q", got, wantBytes)
		}
	})
}

// TestSyncGuardedState_RestoreSyncStateBytesRefusesEmptyPrior proves
// len(prior) == 0 — nil or an empty non-nil slice alike — is refused with a
// non-nil error before any read or write, and that the current file survives
// completely, not merely non-empty but byte-identical to what it held
// before the call.
func TestSyncGuardedState_RestoreSyncStateBytesRefusesEmptyPrior(t *testing.T) {
	cases := []struct {
		name  string
		prior []byte
	}{
		{"nil prior", nil},
		{"empty non-nil prior", []byte{}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			featurePath := t.TempDir()
			original := []byte("current legacy state\n")
			if err := os.WriteFile(internal.SyncStatePath(featurePath), original, 0644); err != nil {
				t.Fatal(err)
			}

			err := internal.RestoreSyncStateBytes(featurePath, tc.prior, original)
			if err == nil {
				t.Fatal("RestoreSyncStateBytes must refuse an empty prior document")
			}
			after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(after) == 0 {
				t.Fatal("the file must not be truncated to empty")
			}
			if string(after) != string(original) {
				t.Fatalf("the file must be untouched: got=%q want=%q", after, original)
			}
		})
	}
}

// TestSyncGuardedState_RestoreSyncStateBytesRefusesWhenPayloadExists proves
// ErrSyncStateHasPayload fires whenever a scoped payload exists beside a
// current file that DOES match expect, and that the refusal writes nothing.
func TestSyncGuardedState_RestoreSyncStateBytesRefusesWhenPayloadExists(t *testing.T) {
	featurePath := t.TempDir()
	expect := []byte("captured sentinel bytes\n")
	if err := os.WriteFile(internal.SyncStatePath(featurePath), expect, 0644); err != nil {
		t.Fatal(err)
	}
	payload := internal.NewSyncRunState("feature", guardedStateTestMarker, guardedStateTestToken, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll,
	})
	if err := internal.SaveSyncRunState(featurePath, payload); err != nil {
		t.Fatal(err)
	}

	err := internal.RestoreSyncStateBytes(featurePath, []byte("prior legacy bytes\n"), expect)
	if !errors.Is(err, internal.ErrSyncStateHasPayload) {
		t.Fatalf("err = %v, want ErrSyncStateHasPayload", err)
	}
	after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
	if readErr != nil || string(after) != string(expect) {
		t.Fatalf("the file must be untouched: got=%q want=%q err=%v", after, expect, readErr)
	}
}

// TestSyncGuardedState_RestoreSyncStateBytesRefusesChangedBytes proves
// ErrSyncStateChanged fires whenever the current file's bytes do not match
// expect, and that the refusal writes nothing.
func TestSyncGuardedState_RestoreSyncStateBytesRefusesChangedBytes(t *testing.T) {
	featurePath := t.TempDir()
	current := []byte("current bytes on disk\n")
	if err := os.WriteFile(internal.SyncStatePath(featurePath), current, 0644); err != nil {
		t.Fatal(err)
	}

	err := internal.RestoreSyncStateBytes(featurePath, []byte("prior legacy bytes\n"), []byte("a different captured document\n"))
	if !errors.Is(err, internal.ErrSyncStateChanged) {
		t.Fatalf("err = %v, want ErrSyncStateChanged", err)
	}
	after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
	if readErr != nil || string(after) != string(current) {
		t.Fatalf("the file must be untouched: got=%q want=%q err=%v", after, current, readErr)
	}
}

// TestSyncGuardedState_RestoreSyncStateBytesHappyPathRestoresByteForByte
// proves the one success shape: a current file matching expect, with no
// payload beside it, is overwritten with prior byte-for-byte — verified by
// SHA-256 as well as a direct comparison.
func TestSyncGuardedState_RestoreSyncStateBytesHappyPathRestoresByteForByte(t *testing.T) {
	featurePath := t.TempDir()
	expect := []byte("installed sentinel bytes\n")
	if err := os.WriteFile(internal.SyncStatePath(featurePath), expect, 0644); err != nil {
		t.Fatal(err)
	}
	prior := []byte("the original legacy document, byte for byte\nacross more than one line\n")

	if err := internal.RestoreSyncStateBytes(featurePath, prior, expect); err != nil {
		t.Fatalf("a matching restore must succeed: %v", err)
	}
	after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got, want := sha256.Sum256(after), sha256.Sum256(prior); got != want {
		t.Fatalf("restored sha256 = %x, want %x", got, want)
	}
	if string(after) != string(prior) {
		t.Fatalf("restored bytes = %q, want %q", after, prior)
	}
}

// TestSyncGuardedState_RemoveSyncStateIfUnchangedRefusalsAndHappyPath covers
// RemoveSyncStateIfUnchanged's three refusal cells — the file absent, its
// bytes differing from expect, and a payload existing beside a matching
// file — plus the happy path, where the file is actually removed.
func TestSyncGuardedState_RemoveSyncStateIfUnchangedRefusalsAndHappyPath(t *testing.T) {
	t.Run("file absent", func(t *testing.T) {
		featurePath := t.TempDir()
		err := internal.RemoveSyncStateIfUnchanged(featurePath, []byte("expected bytes\n"))
		if !errors.Is(err, internal.ErrSyncStateChanged) {
			t.Fatalf("err = %v, want ErrSyncStateChanged", err)
		}
	})

	t.Run("bytes differ from expect", func(t *testing.T) {
		featurePath := t.TempDir()
		current := []byte("current bytes\n")
		if err := os.WriteFile(internal.SyncStatePath(featurePath), current, 0644); err != nil {
			t.Fatal(err)
		}
		err := internal.RemoveSyncStateIfUnchanged(featurePath, []byte("a different expect\n"))
		if !errors.Is(err, internal.ErrSyncStateChanged) {
			t.Fatalf("err = %v, want ErrSyncStateChanged", err)
		}
		after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
		if readErr != nil || string(after) != string(current) {
			t.Fatalf("the file must be untouched: got=%q want=%q err=%v", after, current, readErr)
		}
	})

	t.Run("payload exists beside a matching sentinel", func(t *testing.T) {
		featurePath := t.TempDir()
		expect := []byte("matching sentinel bytes\n")
		if err := os.WriteFile(internal.SyncStatePath(featurePath), expect, 0644); err != nil {
			t.Fatal(err)
		}
		payload := internal.NewSyncRunState("feature", guardedStateTestMarker, guardedStateTestToken, internal.SyncRunPolicy{
			Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll,
		})
		if err := internal.SaveSyncRunState(featurePath, payload); err != nil {
			t.Fatal(err)
		}

		err := internal.RemoveSyncStateIfUnchanged(featurePath, expect)
		if !errors.Is(err, internal.ErrSyncStateHasPayload) {
			t.Fatalf("err = %v, want ErrSyncStateHasPayload", err)
		}
		after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
		if readErr != nil || string(after) != string(expect) {
			t.Fatalf("the file must be untouched: got=%q want=%q err=%v", after, expect, readErr)
		}
	})

	t.Run("happy path removes the file", func(t *testing.T) {
		featurePath := t.TempDir()
		expect := []byte("matching sentinel bytes\n")
		if err := os.WriteFile(internal.SyncStatePath(featurePath), expect, 0644); err != nil {
			t.Fatal(err)
		}
		if err := internal.RemoveSyncStateIfUnchanged(featurePath, expect); err != nil {
			t.Fatalf("a matching, payload-free removal must succeed: %v", err)
		}
		if _, statErr := os.Lstat(internal.SyncStatePath(featurePath)); !os.IsNotExist(statErr) {
			t.Fatalf("the file must be gone, stat err = %v", statErr)
		}
	})
}

// ---------------------------------------------------------------------------
// TestSyncGuardedState (rollback residue truth and ordering) —
// rollbackGuardedLegacyRunState (§13.2a) undoes steps 4, 3, 2 in REVERSE
// creation order: payload, then the conditional sentinel undo, then the
// guard last. Every fixture here is a real guard+sentinel+payload triple
// produced by the shipped setupGuardedLegacyRunState, over both its fresh
// and continuation arms.
// ---------------------------------------------------------------------------

// newGuardedLegacyRollbackFixture drives the REAL setupGuardedLegacyRunState
// over a fresh t.TempDir() — no git repository is involved, since every
// artefact it manipulates (guard, sentinel, payload) is a plain file under
// featurePath. On the continuation arm it first writes a genuine prior
// legacy .sync-state.yaml and derives its carry exactly as sync.go's own
// cell-7 caller does (carriedGuardedLegacyState), so the returned
// guardedLegacyUndo's prior bytes are the real captured document rather than
// a hand-built stand-in.
func newGuardedLegacyRollbackFixture(t *testing.T, continuation bool) (featurePath string, undo guardedLegacyUndo) {
	t.Helper()
	featurePath = t.TempDir()
	universe := []string{"root", "parent", "child"}
	carry := guardedLegacyCarry{}
	if continuation {
		priorState := internal.NewSyncState()
		priorState.FailedBranch = "parent"
		priorState.Completed = []string{"root"}
		priorState.Pending = []string{"parent", "child"}
		if err := internal.SaveSyncState(featurePath, priorState); err != nil {
			t.Fatal(err)
		}
		carry = carriedGuardedLegacyState(priorState)
	}
	pending := guardedLegacySetupPending(universe, carry)
	birth := syncRunStateBirth{StateVersion: internal.SyncRunStateGuardedVersion, Route: internal.RouteLegacy}
	layout := newExternalSyncLayout(featurePath)

	_, undo, err := setupGuardedLegacyRunState(layout, "feature", guardedStateTestMarker, guardedStateTestToken, universe, pending, false, "", "none", birth, carry)
	if err != nil {
		t.Fatalf("fixture setup: %v", err)
	}
	if !undo.made.Guard || !undo.made.Sentinel || !undo.made.Payload {
		t.Fatalf("fixture setup must create all three artefacts: %+v", undo.made)
	}
	if continuation && len(undo.prior) == 0 {
		t.Fatal("fixture setup must capture the prior legacy document on the continuation arm")
	}
	if !continuation && undo.prior != nil {
		t.Fatalf("fixture setup must capture no prior document on the fresh arm, got %q", undo.prior)
	}
	return featurePath, undo
}

// TestSyncGuardedState_LegacyRollbackFaultAtGuardLeavesGuardOnly injects a
// fault at the guard remover — the LAST undo step — and proves both earlier
// steps (payload removed, sentinel undone) already completed while the
// guard alone survives, over both the fresh and continuation arms.
func TestSyncGuardedState_LegacyRollbackFaultAtGuardLeavesGuardOnly(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		continuation := continuation
		t.Run(fmt.Sprintf("continuation=%v", continuation), func(t *testing.T) {
			featurePath, undo := newGuardedLegacyRollbackFixture(t, continuation)

			injected := fmt.Errorf("injected guard-remover fault")
			withSyncStateIOFault(t, internal.SyncIORemoveSyncRunGuard, internal.SyncRunGuardPath(featurePath), injected)

			residue, err := rollbackGuardedLegacyRunState(featurePath, undo.prior, undo.sentinel, undo.made)
			if !errors.Is(err, injected) {
				t.Fatalf("err = %v, want the injected fault", err)
			}
			if want := "{guard}"; residue.String() != want {
				t.Fatalf("residue = %s, want %s", residue.String(), want)
			}
			if internal.HasSyncRunState(featurePath) {
				t.Fatal("the payload must already be gone when the guard step fails")
			}
			if _, statErr := os.Lstat(internal.SyncRunGuardPath(featurePath)); statErr != nil {
				t.Fatalf("the guard must still be held: %v", statErr)
			}
			if continuation {
				after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
				if readErr != nil || string(after) != string(undo.prior) {
					t.Fatalf("the prior legacy document must already be restored: got=%q want=%q err=%v", after, undo.prior, readErr)
				}
			} else if _, statErr := os.Lstat(internal.SyncStatePath(featurePath)); !os.IsNotExist(statErr) {
				t.Fatalf("the fresh route's sentinel must already be removed, stat err = %v", statErr)
			}
		})
	}
}

// TestSyncGuardedState_LegacyRollbackFaultAtSentinelLeavesSentinelAndGuard
// injects a fault at the sentinel undo step — RemoveSyncStateIfUnchanged on
// the fresh arm, RestoreSyncStateBytes on the continuation arm — and proves
// the payload is already gone while the sentinel's installed bytes and the
// guard both survive untouched, since the guard step is never reached.
func TestSyncGuardedState_LegacyRollbackFaultAtSentinelLeavesSentinelAndGuard(t *testing.T) {
	cases := []struct {
		name         string
		continuation bool
		op           string
	}{
		{"fresh route: remove-if-unchanged", false, internal.SyncIORemoveStateUnchanged},
		{"continuation route: restore-sync-state", true, internal.SyncIORestoreSyncState},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			featurePath, undo := newGuardedLegacyRollbackFixture(t, tc.continuation)

			injected := fmt.Errorf("injected sentinel-undo fault")
			withSyncStateIOFault(t, tc.op, internal.SyncStatePath(featurePath), injected)

			residue, err := rollbackGuardedLegacyRunState(featurePath, undo.prior, undo.sentinel, undo.made)
			if !errors.Is(err, injected) {
				t.Fatalf("err = %v, want the injected fault", err)
			}
			if want := "{sentinel, guard}"; residue.String() != want {
				t.Fatalf("residue = %s, want %s", residue.String(), want)
			}
			if internal.HasSyncRunState(featurePath) {
				t.Fatal("the payload must already be gone when the sentinel step fails")
			}
			if _, statErr := os.Lstat(internal.SyncRunGuardPath(featurePath)); statErr != nil {
				t.Fatalf("the guard must still be held: %v", statErr)
			}
			after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
			if readErr != nil || string(after) != string(undo.sentinel) {
				t.Fatalf("the installed sentinel must be untouched by the failed undo: got=%q want=%q err=%v", after, undo.sentinel, readErr)
			}
		})
	}
}

// TestSyncGuardedState_LegacyRollbackRestoresOriginalBytesOnContinuation
// proves the continuation arm's sentinel undo calls the CONDITIONAL
// RestoreSyncStateBytes, not the unconditional RemoveSyncState: after a
// clean rollback the original document is back on disk — the path still
// exists — with byte-for-byte the same content it had before the guarded
// legacy run ever started.
func TestSyncGuardedState_LegacyRollbackRestoresOriginalBytesOnContinuation(t *testing.T) {
	featurePath, undo := newGuardedLegacyRollbackFixture(t, true)

	residue, err := rollbackGuardedLegacyRunState(featurePath, undo.prior, undo.sentinel, undo.made)
	if err != nil {
		t.Fatalf("a clean rollback must succeed: %v", err)
	}
	if !residue.Empty() {
		t.Fatalf("residue = %s, want empty", residue.String())
	}

	after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
	if readErr != nil {
		t.Fatalf("the original legacy document must be RESTORED, not merely removed: %v", readErr)
	}
	if got, want := sha256.Sum256(after), sha256.Sum256(undo.prior); got != want {
		t.Fatalf("restored sha256 = %x, want %x (the original document)", got, want)
	}
	if string(after) != string(undo.prior) {
		t.Fatalf("restored bytes = %q, want the original %q", after, undo.prior)
	}
}

// TestSyncGuardedState_LegacyRollbackConditionalSentinelUndoOnFreshRoute
// proves the fresh arm's sentinel undo is likewise CONDITIONAL — it calls
// RemoveSyncStateIfUnchanged, never an unconditional remove — by contrasting
// the exact same undo token over two different states of the file: a
// foreign document (bytes this invocation never installed) is refused with
// ErrSyncStateChanged and left completely intact, while the invocation's own
// still-matching sentinel bytes are removed cleanly.
func TestSyncGuardedState_LegacyRollbackConditionalSentinelUndoOnFreshRoute(t *testing.T) {
	t.Run("refuses and preserves a foreign document", func(t *testing.T) {
		featurePath, undo := newGuardedLegacyRollbackFixture(t, false)
		foreign := []byte("a foreign document another process wrote\n")
		if err := os.WriteFile(internal.SyncStatePath(featurePath), foreign, 0644); err != nil {
			t.Fatal(err)
		}

		residue, err := rollbackGuardedLegacyRunState(featurePath, undo.prior, undo.sentinel, undo.made)
		if !errors.Is(err, internal.ErrSyncStateChanged) {
			t.Fatalf("err = %v, want ErrSyncStateChanged", err)
		}
		if want := "{sentinel, guard}"; residue.String() != want {
			t.Fatalf("residue = %s, want %s (the payload is already gone; the foreign document blocks the sentinel undo, so the guard is never attempted)", residue.String(), want)
		}
		after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
		if readErr != nil || string(after) != string(foreign) {
			t.Fatalf("the foreign document must be left completely intact: got=%q want=%q err=%v", after, foreign, readErr)
		}
		if internal.HasSyncRunState(featurePath) {
			t.Fatal("the payload must already be gone before the sentinel undo runs")
		}
		if _, statErr := os.Lstat(internal.SyncRunGuardPath(featurePath)); statErr != nil {
			t.Fatalf("the guard must still be held since the sentinel undo never succeeded: %v", statErr)
		}
	})

	t.Run("removes only its own installed sentinel bytes when they still match", func(t *testing.T) {
		featurePath, undo := newGuardedLegacyRollbackFixture(t, false)

		residue, err := rollbackGuardedLegacyRunState(featurePath, undo.prior, undo.sentinel, undo.made)
		if err != nil {
			t.Fatalf("a clean rollback over its own unmodified sentinel must succeed: %v", err)
		}
		if !residue.Empty() {
			t.Fatalf("residue = %s, want empty", residue.String())
		}
		if _, statErr := os.Lstat(internal.SyncStatePath(featurePath)); !os.IsNotExist(statErr) {
			t.Fatalf("the fresh route's own sentinel must be gone, stat err = %v", statErr)
		}
	})
}

// TestSyncGuardedState_LegacyRollbackSuccessYieldsEmptyResidue proves a
// clean rollback — nothing faulted, nothing tampered with — undoes every
// artefact it created, over both the fresh and continuation arms, leaving
// residue.Empty() == true.
func TestSyncGuardedState_LegacyRollbackSuccessYieldsEmptyResidue(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		continuation := continuation
		t.Run(fmt.Sprintf("continuation=%v", continuation), func(t *testing.T) {
			featurePath, undo := newGuardedLegacyRollbackFixture(t, continuation)

			residue, err := rollbackGuardedLegacyRunState(featurePath, undo.prior, undo.sentinel, undo.made)
			if err != nil {
				t.Fatalf("a clean rollback must succeed: %v", err)
			}
			if !residue.Empty() {
				t.Fatalf("residue = %s, want empty", residue.String())
			}
			if internal.HasSyncRunState(featurePath) {
				t.Fatal("the payload must be gone")
			}
			if _, statErr := os.Lstat(internal.SyncRunGuardPath(featurePath)); !os.IsNotExist(statErr) {
				t.Fatalf("the guard must be released, stat err = %v", statErr)
			}
			if continuation {
				after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
				if readErr != nil || string(after) != string(undo.prior) {
					t.Fatalf("the original legacy document must be restored: got=%q want=%q err=%v", after, undo.prior, readErr)
				}
			} else if _, statErr := os.Lstat(internal.SyncStatePath(featurePath)); !os.IsNotExist(statErr) {
				t.Fatalf("the fresh route must leave no sentinel behind, stat err = %v", statErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestSyncGuardedState (SaveSyncRunState version preservation, §13.6) — a
// non-zero StateVersion is written exactly as given, a zero one defaults to
// SyncRunStateVersion, and a round trip through LoadSyncRunState preserves
// Route/MaxReplayPerEntry/MaxReplayTotal, including a persisted 0 (which
// yaml.v3's isZero treats as "non-nil pointer, not empty" for a *int field,
// so it is never omitted by the members' own `,omitempty` tag).
// ---------------------------------------------------------------------------

// TestSyncGuardedState_SaveSyncRunStatePreservesGuardedVersionThree proves a
// payload whose StateVersion is already 3 is written as 3 — never forced
// down to 2 — both in the mutated in-memory struct and in the on-disk
// document, and survives a reload.
func TestSyncGuardedState_SaveSyncRunStatePreservesGuardedVersionThree(t *testing.T) {
	dir := t.TempDir()
	payload := internal.NewSyncRunState("feature", guardedStateTestMarker, guardedStateTestToken, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll,
	})
	payload.StateVersion = internal.SyncRunStateGuardedVersion

	if err := internal.SaveSyncRunState(dir, payload); err != nil {
		t.Fatalf("save: %v", err)
	}
	if payload.StateVersion != internal.SyncRunStateGuardedVersion {
		t.Fatalf("SaveSyncRunState must never force a non-zero version down: got %d, want %d", payload.StateVersion, internal.SyncRunStateGuardedVersion)
	}

	raw, err := os.ReadFile(internal.SyncRunStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "state_version: 3\n") {
		t.Fatalf("on-disk document must record state_version: 3:\n%s", raw)
	}

	reloaded, err := internal.LoadSyncRunState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if reloaded.StateVersion != internal.SyncRunStateGuardedVersion {
		t.Fatalf("reloaded state_version = %d, want %d", reloaded.StateVersion, internal.SyncRunStateGuardedVersion)
	}
}

// TestSyncGuardedState_SaveSyncRunStateDefaultsZeroVersionToTwo proves a
// zero StateVersion — never produced by NewSyncRunState itself, but a
// legitimate caller input per SaveSyncRunState's own doc comment — defaults
// to SyncRunStateVersion (2), both in the mutated struct and on reload.
func TestSyncGuardedState_SaveSyncRunStateDefaultsZeroVersionToTwo(t *testing.T) {
	dir := t.TempDir()
	payload := internal.NewSyncRunState("feature", guardedStateTestMarker, guardedStateTestToken, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll,
	})
	payload.StateVersion = 0

	if err := internal.SaveSyncRunState(dir, payload); err != nil {
		t.Fatalf("save: %v", err)
	}
	if payload.StateVersion != internal.SyncRunStateVersion {
		t.Fatalf("a zero state_version must default to %d, got %d", internal.SyncRunStateVersion, payload.StateVersion)
	}

	reloaded, err := internal.LoadSyncRunState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if reloaded.StateVersion != internal.SyncRunStateVersion {
		t.Fatalf("reloaded state_version = %d, want %d", reloaded.StateVersion, internal.SyncRunStateVersion)
	}
}

// TestSyncGuardedState_RoundTripPreservesRouteAndPersistedZeroLimits proves
// Route, MaxReplayPerEntry and MaxReplayTotal all survive a save/load round
// trip, including a persisted 0 for both limits: a non-nil *int pointing at
// 0 is not "empty" to yaml.v3 (only a nil pointer is), so the on-disk
// document keeps the key and LoadSyncRunState reloads a non-nil pointer.
func TestSyncGuardedState_RoundTripPreservesRouteAndPersistedZeroLimits(t *testing.T) {
	dir := t.TempDir()
	payload := internal.NewSyncRunState("feature", guardedStateTestMarker, guardedStateTestToken, internal.SyncRunPolicy{
		Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll,
	})
	payload.StateVersion = internal.SyncRunStateGuardedVersion
	payload.Route = internal.RouteNewMode
	payload.MaxReplayPerEntry = intp(0)
	payload.MaxReplayTotal = intp(0)

	if err := internal.SaveSyncRunState(dir, payload); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(internal.SyncRunStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{"route: new-mode\n", "max_replay_per_entry: 0\n", "max_replay_total: 0\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("a persisted 0 must not be omitted from the document, missing %q:\n%s", want, body)
		}
	}

	reloaded, err := internal.LoadSyncRunState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if reloaded.Route != internal.RouteNewMode {
		t.Fatalf("route = %q, want %q", reloaded.Route, internal.RouteNewMode)
	}
	if reloaded.MaxReplayPerEntry == nil || *reloaded.MaxReplayPerEntry != 0 {
		t.Fatalf("max_replay_per_entry = %v, want a persisted 0 (non-nil)", reloaded.MaxReplayPerEntry)
	}
	if reloaded.MaxReplayTotal == nil || *reloaded.MaxReplayTotal != 0 {
		t.Fatalf("max_replay_total = %v, want a persisted 0 (non-nil)", reloaded.MaxReplayTotal)
	}
}

// ---------------------------------------------------------------------------
// §22.28a — the post-claim cleanup half of §12.2c rule 4: the composed
// residue sentence, the guarded LEGACY rollback's own three residue cells,
// and the named recovery really working end to end through cli.Execute().
// ---------------------------------------------------------------------------

// TestSyncGuardedState_ResidueErrorComposition pins syncResidueError's exact
// composition over every residue shape: the failing writer's OWN shipped
// sentence first, then the measured residue in the fixed
// payload/sentinel/guard order, then the exact recovery command — and never
// a `plan-guard: ` marker, because the guard did not refuse, a write failed.
func TestSyncGuardedState_ResidueErrorComposition(t *testing.T) {
	cause := errors.New("remove sync run guard: permission denied")
	cases := []struct {
		residue syncStateResidue
		want    string
	}{
		{syncStateResidue{Payload: true, Sentinel: true, Guard: true}, "{payload, sentinel, guard}"},
		{syncStateResidue{Sentinel: true, Guard: true}, "{sentinel, guard}"},
		{syncStateResidue{Guard: true}, "{guard}"},
		{syncStateResidue{Sentinel: true}, "{sentinel}"},
		{syncStateResidue{Payload: true}, "{payload}"},
		{syncStateResidue{}, "{}"},
	}
	for _, tc := range cases {
		got := syncResidueError(cause, tc.residue, "feature").Error()
		want := cause.Error() + "; recovery state preserved: " + tc.want + " — clear it with: tws sync feature --abort"
		if got != want {
			t.Fatalf("syncResidueError(%s) = %q, want %q", tc.want, got, want)
		}
		if strings.Contains(got, "plan-guard: ") || strings.Contains(got, "state-preserved: ") {
			t.Fatalf("the residue sentence must be marker-free, got %q", got)
		}
	}
}

// TestSyncGuardedState_LegacyRollbackResidueMatchesFaultedRemover is the
// guarded LEGACY twin of the fresh rollback table above: §13.2a's undo runs
// in reverse creation order — payload, then the CONDITIONAL sentinel step,
// then the guard — and a fault at each step in turn must stop exactly there
// and report the residue really left on disk.
//
// The sentinel step is asserted in its `prior`-bearing (continuation) form
// too, where the undo is a RESTORE of the captured bytes rather than a
// removal: a rollback that removed there would destroy the operator's own
// document instead of putting it back.
func TestSyncGuardedState_LegacyRollbackResidueMatchesFaultedRemover(t *testing.T) {
	made := guardedLegacySetupProgress{Payload: true, Sentinel: true, Guard: true}
	sentinelBytes := []byte("sentinel-bytes\n")

	cases := []struct {
		name        string
		op          string
		path        func(string) string
		prior       []byte
		wantResidue string
	}{
		{"payload remover fails first", internal.SyncIORemoveSyncRunState, internal.SyncRunStatePath, nil, "{payload, sentinel, guard}"},
		{"fresh sentinel step fails second (remove-if-unchanged)", internal.SyncIORemoveStateUnchanged, internal.SyncStatePath, nil, "{sentinel, guard}"},
		{"continuation sentinel step fails second (restore-bytes)", internal.SyncIORestoreSyncState, internal.SyncStatePath, []byte("prior\n"), "{sentinel, guard}"},
		{"guard remover fails third", internal.SyncIORemoveSyncRunGuard, internal.SyncRunGuardPath, nil, "{guard}"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			featurePath := t.TempDir()
			// The sentinel step only succeeds against the exact bytes this
			// invocation installed, so the fixture really puts them there.
			if err := os.WriteFile(internal.SyncStatePath(featurePath), sentinelBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			injected := fmt.Errorf("injected %s fault", tc.op)
			withSyncStateIOFault(t, tc.op, tc.path(featurePath), injected)

			residue, err := rollbackGuardedLegacyRunState(featurePath, tc.prior, sentinelBytes, made)
			if !errors.Is(err, injected) {
				t.Fatalf("rollbackGuardedLegacyRunState err = %v, want the injected fault", err)
			}
			if got := residue.String(); got != tc.wantResidue {
				t.Fatalf("residue = %s, want %s", got, tc.wantResidue)
			}
			// The composed sentence a caller builds from this pair names the
			// same measured residue and the same recovery command.
			composed := syncResidueError(err, residue, "feature").Error()
			if !strings.Contains(composed, "recovery state preserved: "+tc.wantResidue) {
				t.Fatalf("composed = %q, want it to name the measured residue %s", composed, tc.wantResidue)
			}
			if !strings.HasSuffix(composed, "clear it with: tws sync feature --abort") {
				t.Fatalf("composed = %q, want the exact recovery command suffix", composed)
			}
		})
	}
}

// TestSyncGuardedState_ResidueRecoveryIsAbortEndToEnd closes §22.28a rule L:
// the sentence a failing rollback prints names `tws sync <f> --abort`, and
// that command REALLY clears the residue, driven end to end through
// production cli.Execute() over a real fixture.
//
// A guarded fresh new-mode run is driven to a POST-CLAIM refusal: a step
// hook, firing at the shipped SyncStageInitializing index 2 (immediately
// after the payload landed), advances the first row's own branch, so that
// row's JIT seam re-measures drifted facts and refuses with
// `revalidation-mismatch` before any rebase — StatePreserved false, which is
// exactly the cell rollbackGuardedFreshRefusal owns. The guard remover is
// faulted, so the shipped rollback removes the payload and the sentinel and
// then stops with a genuinely measured `{guard}` residue.
//
// Three things are then asserted: the refusal carries the composed residue
// sentence naming that measured residue and the exact recovery command; the
// residue on disk really is a lone guard file; and the named recovery —
// `tws sync <f> --abort` from a process the stale guard does not record —
// clears it and says so.
func TestSyncGuardedState_ResidueRecoveryIsAbortEndToEnd(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	featurePath := f.featurePath

	guardFault := errors.New("injected guard remover fault")
	previousFault := internal.SyncStateIOFault
	internal.SyncStateIOFault = func(op, path string) error {
		if op == internal.SyncIORemoveSyncRunGuard && path == internal.SyncRunGuardPath(featurePath) {
			return guardFault
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncStateIOFault = previousFault })

	previousHook := internal.SyncStepHook
	fired := 0
	internal.SyncStepHook = func(stage internal.SyncRunStage, index int) error {
		if stage == internal.SyncStageInitializing && index == 2 && fired == 0 {
			fired++
			// Drift the FIRST row's own branch after the plan was approved.
			writeAndCommit(t, f.wt("root"), "drift.txt", "drift\n", "drift")
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncStepHook = previousHook })

	_, stderr, exit := runSyncExecute(t, f.feature, "--no-fetch", "--full", "--max-replay-total", "50")
	internal.SyncStepHook = previousHook
	if exit == 0 {
		t.Fatalf("the drifted first row must refuse at its JIT seam: stderr=%q", stderr)
	}
	if fired != 1 {
		t.Fatalf("the shipped initializing hook fired %d times, want exactly 1", fired)
	}
	if !strings.Contains(stderr, "recovery state preserved: {guard}") {
		t.Fatalf("stderr = %q, want the composed residue sentence naming the MEASURED {guard} residue", stderr)
	}
	wantRecovery := "clear it with: tws sync " + f.feature + " --abort"
	if !strings.Contains(stderr, wantRecovery) {
		t.Fatalf("stderr = %q, want the exact recovery command %q", stderr, wantRecovery)
	}

	// The residue really is exactly what the sentence claimed: a lone guard.
	if _, err := os.Lstat(internal.SyncRunGuardPath(featurePath)); err != nil {
		t.Fatalf("the guard residue must really be on disk: %v", err)
	}
	if internal.HasSyncRunState(featurePath) {
		t.Fatal("the payload must not survive: the rollback removed it before the guard step failed")
	}
	if _, err := os.Lstat(internal.SyncStatePath(featurePath)); err == nil {
		t.Fatal("the sentinel must not survive: the rollback removed it before the guard step failed")
	}

	// The NAMED recovery really works. The fault modelled the failing run,
	// not the operator's later shell, so it is lifted first; the stale guard
	// is detached so a new process owns the abort.
	internal.SyncStateIOFault = previousFault
	dead := spawnDeadPID(t)
	f.detachGuardPreservingBytes(t, dead)

	stdout, stderr, exit := runSyncExecute(t, f.feature, "--abort")
	if exit != 0 {
		t.Fatalf("the named recovery must succeed: exit=%d stderr=%q", exit, stderr)
	}
	want := fmt.Sprintf("Stale sync guard from PID %d cleared; no sync state was present.\n", dead)
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	f.stateFilesGone(t)
}

// ===========================================================================
// §22.24i (x) — one owner per version-writing site, and births are not
// upgrades.
// ===========================================================================

// countDeclarations returns how many times `func <name>(` is declared in src.
func countDeclarations(src, name string) int {
	return len(regexp.MustCompile(`(?m)^func `+regexp.QuoteMeta(name)+`\(`).FindAllString(src, -1))
}

// TestSyncGuardedState_Criterion22_24i_x_VersionWritingSitesHaveOneOwnerEach
// is §22.24i (x)'s executable owner. It enumerates the FIVE sites of §13.6
// rule 2a binding 6 — three BIRTH sites and two UPGRADE writers — and shows
// there is no sixth, by parsing declarations rather than grepping call sites.
//
//	births:   setupSyncRunState (its `birth` argument),
//	          setupGuardedLegacyRunState,
//	          the CheckoutTransaction literal in RunCheckoutSync
//	upgrades: upgradeGuardedSyncRunState,
//	          upgradeGuardedCheckoutTransaction
//
// The classification itself is asserted too: the two UPGRADE writers are
// no-ops on an already-`3` subject and touch exactly state_version, route
// and the effective limit keys, while setupGuardedLegacyRunState CREATES
// (capture, claim, sentinel, payload) and is therefore never "the third
// upgrade writer".
func TestSyncGuardedState_Criterion22_24i_x_VersionWritingSitesHaveOneOwnerEach(t *testing.T) {
	modesSrc := readCliSource(t, "sync_modes.go")
	checkoutSrc := readInternalSource(t, "checkout_sync.go")
	runStateSrc := readInternalSource(t, "sync_run_state.go")

	// (a) each named site is declared exactly once, in exactly its file.
	for _, tc := range []struct {
		name string
		src  string
		file string
	}{
		{"setupSyncRunState", modesSrc, "internal/cli/sync_modes.go"},
		{"setupGuardedLegacyRunState", modesSrc, "internal/cli/sync_modes.go"},
		{"upgradeGuardedSyncRunState", modesSrc, "internal/cli/sync_modes.go"},
		{"upgradeGuardedCheckoutTransaction", checkoutSrc, "internal/checkout_sync.go"},
	} {
		if n := countDeclarations(tc.src, tc.name); n != 1 {
			t.Fatalf("%s is declared %d times in %s, want exactly 1", tc.name, n, tc.file)
		}
	}
	// The two cli writers must NOT also be declared in the internal package,
	// and vice versa: one owner per site means one file.
	for _, name := range []string{"setupGuardedLegacyRunState", "upgradeGuardedSyncRunState"} {
		if countDeclarations(checkoutSrc, name) != 0 || countDeclarations(runStateSrc, name) != 0 {
			t.Fatalf("%s is declared outside internal/cli/sync_modes.go", name)
		}
	}
	if countDeclarations(modesSrc, "upgradeGuardedCheckoutTransaction") != 0 {
		t.Fatalf("upgradeGuardedCheckoutTransaction is declared outside internal/checkout_sync.go")
	}

	// (b) there is no SIXTH site: no assignment of a StateVersion outside the
	// five, and the two savers preserve rather than set it.
	assignRe := regexp.MustCompile(`(?m)^\s*(?:tx\.|payload\.|p\.)?StateVersion\s*=`)
	for _, tc := range []struct {
		file string
		src  string
		want []string
	}{
		{"internal/cli/sync_modes.go", modesSrc, []string{"setupSyncRunState", "setupGuardedLegacyRunState", "upgradeGuardedSyncRunState", "newGuardedLegacyPayload", "newGuardedLegacySentinel"}},
		{"internal/checkout_sync.go", checkoutSrc, []string{"upgradeGuardedCheckoutTransaction", "RunCheckoutSync"}},
	} {
		for _, m := range assignRe.FindAllStringIndex(tc.src, -1) {
			owner := enclosingFuncName(tc.src, m[0])
			if !slices.Contains(tc.want, owner) {
				t.Fatalf("%s: StateVersion is assigned inside %q, which is not one of the declared version-writing owners %v",
					tc.file, owner, tc.want)
			}
		}
	}
	// SaveSyncRunState / SaveCheckoutTransaction preserve rather than set.
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"SaveSyncRunState", runStateSrc},
		{"SaveCheckoutTransaction", checkoutSrc},
	} {
		body := funcBody(t, tc.src, tc.name)
		if assignRe.MatchString(body) {
			t.Fatalf("%s assigns a StateVersion; the savers must preserve the value they were given", tc.name)
		}
	}

	// (c) none of the five is called from a plan route: PlanCheckoutRebase /
	// BuildCheckoutRebasePlan / BuildCheckoutContinuationPlan and package
	// cli's runExternalPlan must not call any of them.
	guardSrc := readInternalSource(t, "rebase_plan_guard.go")
	for _, fn := range []string{"PlanCheckoutRebase", "BuildCheckoutRebasePlan", "BuildCheckoutContinuationPlan"} {
		body := funcBody(t, guardSrc, fn)
		for _, site := range []string{"upgradeGuardedCheckoutTransaction(", "SaveCheckoutTransaction("} {
			if strings.Contains(body, site) {
				t.Fatalf("%s calls %s; a plan route writes no version", fn, site)
			}
		}
	}
	planBody := funcBody(t, readCliSource(t, "sync_plan_guard.go"), "runExternalPlan")
	for _, site := range []string{"setupSyncRunState(", "setupGuardedLegacyRunState(", "upgradeGuardedSyncRunState("} {
		if strings.Contains(planBody, site) {
			t.Fatalf("runExternalPlan calls %s; a plan route writes no version", site)
		}
	}

	// (d) the two UPGRADE writers are no-ops on an already-guarded subject
	// and touch exactly state_version, route and the limit keys.
	// The checkout upgrade writer's own no-op-on-`3` assertion lives in
	// package internal, where the unexported writer is reachable:
	// TestCheckoutSyncPlan_Criterion22_24i_x_CheckoutUpgradeIsANoOpOnAGuardedTransaction.
	t.Run("external_upgrade_touches_only_version_route_and_limits", func(t *testing.T) {
		featurePath := t.TempDir()
		payload := internal.NewSyncRunState("f", "marker", "token", internal.SyncRunPolicy{})
		payload.Selected = []string{"root", "parent"}
		payload.Pending = []string{"parent"}
		payload.Completed = []string{"root"}
		payload.FailedBranch = "parent"
		payload.Stage = internal.SyncStageFailed
		payload.TestCommand = "make check"
		snapshot := *payload

		limit := 3
		birth := syncRunStateBirth{StateVersion: internal.SyncRunStateGuardedVersion, MaxTotal: &limit}
		if err := upgradeGuardedSyncRunState(featurePath, payload, birth); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		if payload.StateVersion != internal.SyncRunStateGuardedVersion {
			t.Fatalf("StateVersion = %d, want the guarded version", payload.StateVersion)
		}
		if payload.Route != internal.RouteNewMode {
			t.Fatalf("Route = %q, want new-mode", payload.Route)
		}
		if payload.MaxReplayTotal == nil || *payload.MaxReplayTotal != limit {
			t.Fatalf("MaxReplayTotal = %v, want %d", payload.MaxReplayTotal, limit)
		}
		// Everything else is byte-identical to the snapshot.
		if !slices.Equal(payload.Selected, snapshot.Selected) ||
			!slices.Equal(payload.Pending, snapshot.Pending) ||
			!slices.Equal(payload.Completed, snapshot.Completed) ||
			payload.FailedBranch != snapshot.FailedBranch ||
			payload.Stage != snapshot.Stage ||
			payload.TestCommand != snapshot.TestCommand {
			t.Fatalf("the external upgrade writer touched a field outside {state_version, route, limits}:\n before=%+v\n after=%+v", snapshot, *payload)
		}
	})

	// (e) setupGuardedLegacyRunState CREATES; it is never an upgrade writer.
	setupBody := funcBody(t, modesSrc, "setupGuardedLegacyRunState")
	for _, creation := range []string{"captureGuardedLegacySyncState(", "ClaimSyncRunGuard(", "SaveGuardedLegacySentinel(", "SaveSyncRunState("} {
		if !strings.Contains(setupBody, creation) {
			t.Fatalf("setupGuardedLegacyRunState does not call %s; it is the CREATE site (capture, claim, sentinel, payload), not an upgrade writer", creation)
		}
	}
	if strings.Contains(setupBody, "checkoutRecoveryIsGuarded(") {
		t.Fatal("setupGuardedLegacyRunState must not carry an already-guarded no-op guard: it creates, it does not upgrade")
	}
}

// enclosingFuncName returns the name of the top-level func declaration that
// textually encloses offset in src.
func enclosingFuncName(src string, offset int) string {
	re := regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?([A-Za-z0-9_]+)\(`)
	name := ""
	for _, m := range re.FindAllStringSubmatchIndex(src, -1) {
		if m[0] > offset {
			break
		}
		name = src[m[2]:m[3]]
	}
	return name
}

// ===========================================================================
// §22.24f — the validation freeze is authoritative (§15.9).
// ===========================================================================

// TestSyncGuardedState_Criterion22_24f_ValidationFreezeIsAuthoritative is
// §22.24f's executable owner. Editing the configured validation command
// AFTER the run's state exists changes nothing: the guarded `--continue`
// resumes with the PERSISTED command, emits no `revalidation-mismatch` and
// no `approval-mismatch`, and never reads the edited configuration.
//
// The checkout transaction is the subject here because it is the one carrier
// whose `test_command` the shipped executor really runs; the external v3
// payload's own frozen command is asserted through the same
// `intent.validation` projection.
func TestSyncGuardedState_Criterion22_24f_ValidationFreezeIsAuthoritative(t *testing.T) {
	t.Run("checkout_transaction_resumes_with_the_persisted_command", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)

		const frozen = "frozen-validation-command"
		limit := 50
		tx := &internal.CheckoutTransaction{
			StateVersion: internal.CheckoutTransactionGuardedVersion,
			Route:        internal.RouteNewMode,
			Feature:      "test-feature", OriginalBranch: "main", OriginalHEAD: gitSHA(t, dir, "main"),
			Stage: internal.StageConflict, CurrentIndex: 0,
			TestCommand:    frozen,
			MaxReplayTotal: &limit,
			Plan: []internal.CheckoutPlanEntry{
				{Name: "feat-a", Branch: "feat-a", Base: "feat-root", NewBaseSHA: gitSHA(t, dir, "feat-root")},
			},
		}
		if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { internal.DeleteCheckoutTransaction(fp) })

		// Edit the configuration AFTER the state exists, to a value that
		// would fail loudly if it were ever read.
		gitRunCS(t, dir, "config", "tws.testCommand", "exit 42 # edited-after-the-freeze")

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		intent := doc["intent"].(map[string]any)
		validation := intent["validation"].(map[string]any)
		if validation["applies"] != true {
			t.Fatalf("intent.validation.applies = %v, want true", validation["applies"])
		}
		wantDigest := internal.ValidationDigest(frozen)
		if validation["command_digest"] != wantDigest {
			t.Fatalf("intent.validation.command_digest = %v, want the PERSISTED command's digest %q",
				validation["command_digest"], wantDigest)
		}
		if validation["source"] != "persisted-transaction" && validation["source"] != "config" {
			t.Fatalf("intent.validation.source = %v, want a persisted source", validation["source"])
		}
		// The edited configuration is never read: its digest never appears.
		edited := internal.ValidationDigest("exit 42 # edited-after-the-freeze")
		if validation["command_digest"] == edited {
			t.Fatalf("the EDITED configuration was read; §15.9 freezes the command at claim time")
		}
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			if b["kind"] == "revalidation-mismatch" || b["kind"] == "approval-mismatch" {
				t.Fatalf("editing the configuration after the freeze must produce no %v: %v", b["kind"], b)
			}
		}
	})

	t.Run("editing_between_plan_and_guarded_twin_refuses_rank_8", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)

		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch",
			"--max-replay-total", "50", "--test", "original-command")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		token := planFieldString(t, stdout, "approval", "fingerprint")
		if len(token) != 64 {
			t.Fatalf("expected a minted token, got %q", token)
		}

		// The very same facts, with the validation command EDITED: the
		// approval no longer covers this run.
		_, stderr, exit = runSyncExecute(t, "test-feature", "--no-fetch",
			"--max-replay-total", "50", "--test", "edited-command", "--approve-plan", token)
		if exit != 1 {
			t.Fatalf("an edited validation command must refuse the approval: exit=%d stderr=%q", exit, stderr)
		}
		if !strings.Contains(stderr, "approval-mismatch") {
			t.Fatalf("stderr = %q, want the rank 8 approval-mismatch", stderr)
		}
		// Nothing was created.
		if internal.HasCheckoutTransaction(fp) {
			t.Fatal("the refusing guarded twin must create no transaction")
		}
		if internal.HasCheckoutLock(fp) {
			t.Fatal("the refusing guarded twin must create no lock")
		}
	})
}

// ===========================================================================
// §22.24i (ii)(iii)(v)(vi)(viii) — the armed continuation's upgrade
// lifecycle, over the checkout transaction and the external v2 payload.
// ===========================================================================

// armedUpgradeSubjects builds the two pre-upgrade subjects §22.24i names that
// this file can drive end to end through production Execute(): a
// version-less checkout transaction (route legacy) and a `state_version: 2`
// checkout transaction (route new-mode).
type armedUpgradeSubject struct {
	name         string
	stateVersion int
	wantRoute    string
}

func armedUpgradeSubjects() []armedUpgradeSubject {
	return []armedUpgradeSubject{
		{"version_less_transaction", 0, internal.RouteLegacy},
		{"state_version_2_transaction", internal.CheckoutTransactionVersion, internal.RouteNewMode},
	}
}

// saveUpgradeSubject persists one pre-upgrade checkout transaction and
// returns its path plus its bytes.
func saveUpgradeSubject(t *testing.T, dir, fp string, s armedUpgradeSubject) []byte {
	t.Helper()
	tx := &internal.CheckoutTransaction{
		StateVersion: s.stateVersion,
		Feature:      "test-feature", OriginalBranch: "main", OriginalHEAD: gitSHA(t, dir, "main"),
		Stage: internal.StageConflict, CurrentIndex: 0,
		TestCommand: "true", // a REAL no-op command: the resume really runs it
		Plan: []internal.CheckoutPlanEntry{
			{Name: "feat-a", Branch: "feat-a", Base: "feat-root", NewBaseSHA: gitSHA(t, dir, "feat-root")},
		},
	}
	if s.stateVersion == internal.CheckoutTransactionVersion {
		tx.Route = internal.RouteNewMode
	}
	if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { internal.DeleteCheckoutTransaction(fp) })
	data, err := os.ReadFile(internal.CheckoutTransactionPath(fp))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestSyncGuardedState_Criterion22_24i_ArmedContinuationUpgradeLifecycle is
// the executable owner for §22.24i clauses (ii), (iii), (v), (vi) and (viii).
func TestSyncGuardedState_Criterion22_24i_ArmedContinuationUpgradeLifecycle(t *testing.T) {
	for _, s := range armedUpgradeSubjects() {
		s := s

		// (iii) plan-only writes NOTHING.
		t.Run(s.name+"/iii_plan_only_writes_nothing", func(t *testing.T) {
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			before := saveUpgradeSubject(t, dir, fp, s)

			stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--continue", "--max-replay-total", "50")
			if exit != 0 {
				t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
			}
			after, err := os.ReadFile(internal.CheckoutTransactionPath(fp))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("a --plan --continue must leave the subject BYTE-IDENTICAL:\n before=%q\n after=%q", before, after)
			}
			guard := planDoc(t, stdout)["guard"].(map[string]any)
			limits := guard["limits"].(map[string]any)
			total := limits["max_replay_total"].(map[string]any)
			if total["origin"] != "flags-persisted-continuation" && total["origin"] != "flags-legacy-continuation" {
				t.Fatalf("guard.limits.max_replay_total.origin = %v, want a flags-*-continuation origin", total["origin"])
			}

			// A later FLAGLESS --continue over that untouched subject is
			// UNGUARDED, proving the plan did not upgrade.
			reloaded, err := internal.LoadCheckoutTransaction(fp)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.StateVersion == internal.CheckoutTransactionGuardedVersion {
				t.Fatal("the plan route upgraded the subject; it must write nothing")
			}
		})

		// (v) an UNARMED continuation is untouched.
		t.Run(s.name+"/v_unarmed_continuation_is_untouched", func(t *testing.T) {
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			before := saveUpgradeSubject(t, dir, fp, s)

			stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--continue")
			if exit != 0 {
				t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
			}
			guard := planDoc(t, stdout)["guard"].(map[string]any)
			limits := guard["limits"].(map[string]any)
			if limits["max_replay_total"].(map[string]any)["origin"] != "none" {
				t.Fatalf("an unarmed continuation has no effective limit, got %v", limits["max_replay_total"])
			}
			after, err := os.ReadFile(internal.CheckoutTransactionPath(fp))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("an UNARMED continuation must leave the subject byte-identical:\n before=%q\n after=%q", before, after)
			}
		})

		// (vi) approval alone upgrades nothing.
		t.Run(s.name+"/vi_approval_alone_upgrades_nothing", func(t *testing.T) {
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			before := saveUpgradeSubject(t, dir, fp, s)

			token := strings.Repeat("a", 64)
			stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--continue", "--approve-plan", token)
			// §3.4 row 12: --approve-plan without a limit is refused up front
			// on the plan route too, so the subject cannot have been touched.
			if exit == 0 {
				doc := planDoc(t, stdout)
				found := false
				for _, raw := range doc["blockers"].([]any) {
					if raw.(map[string]any)["kind"] == "approval-without-limits" {
						found = true
					}
				}
				if !found {
					t.Fatalf("want rank 7.5 approval-without-limits, got %v", doc["blockers"])
				}
			} else if !strings.Contains(stderr, "--approve-plan requires") {
				t.Fatalf("exit=%d stderr=%q, want either the rank 7.5 row or the shipped up-front refusal", exit, stderr)
			}
			after, err := os.ReadFile(internal.CheckoutTransactionPath(fp))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("approval alone must leave the subject at its original version:\n before=%q\n after=%q", before, after)
			}
		})

		// (ii) after a REAL armed continuation the subject is guarded, and a
		// later FLAGLESS continue is still guarded — refusing over a limit the
		// remaining work exceeds, with exactly one ^plan-guard: limit- line
		// and no ref moved.
		t.Run(s.name+"/ii_reinterruption_then_flagless_continue_is_still_guarded", func(t *testing.T) {
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			saveUpgradeSubject(t, dir, fp, s)

			// An armed continuation upgrades the subject in place. The step
			// hook stops it immediately after, so the transaction survives.
			// The armed continuation upgrades the subject in place; the
			// shipped restoring hook then stops it, so the upgraded
			// transaction survives for the flagless resume below.
			clearStepHook(t)
			internal.StepHook = func(stage internal.CheckoutStage, index int) error {
				if stage == internal.StageRestoring {
					return errStop
				}
				return nil
			}
			_, _, _ = runSyncExecute(t, "test-feature", "--continue", "--max-replay-total", "0")
			clearStepHook(t)

			tx, err := internal.LoadCheckoutTransaction(fp)
			if err != nil {
				t.Fatalf("the interrupted armed continuation must leave a transaction: %v", err)
			}
			if tx.StateVersion != internal.CheckoutTransactionGuardedVersion {
				t.Fatalf("state_version = %d, want the guarded v3 after an armed continuation", tx.StateVersion)
			}
			if tx.Route != s.wantRoute {
				t.Fatalf("route = %q, want the INHERITED %q", tx.Route, s.wantRoute)
			}
			if tx.MaxReplayTotal == nil || *tx.MaxReplayTotal != 0 {
				t.Fatalf("MaxReplayTotal = %v, want the persisted 0", tx.MaxReplayTotal)
			}
			if tx.TestCommand != "true" {
				t.Fatalf("test_command = %q, want the SAME bytes it carried before", tx.TestCommand)
			}
			// No approve/token, guard_enabled or digest key exists.
			raw, err := os.ReadFile(internal.CheckoutTransactionPath(fp))
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"approve", "token", "guard_enabled", "digest"} {
				if strings.Contains(string(raw), forbidden+":") {
					t.Fatalf("the upgraded subject must carry no %q key:\n%s", forbidden, raw)
				}
			}

			// The limits are published with a PERSISTED origin, and a
			// FLAGLESS continue is still guarded: it refuses over the
			// persisted 0.
			stdout, stderr, exit := runSyncExecute(t, "test-feature", "--continue", "--plan", "--json")
			if exit != 0 {
				t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
			}
			guard := planDoc(t, stdout)["guard"].(map[string]any)
			total := guard["limits"].(map[string]any)["max_replay_total"].(map[string]any)
			if total["origin"] != "persisted-transaction" {
				t.Fatalf("origin = %v, want persisted-transaction", total["origin"])
			}

			before := gitSHA(t, dir, "feat-a")
			_, stderr, exit = runSyncExecute(t, "test-feature", "--continue")
			if exit != 1 {
				t.Fatalf("a FLAGLESS continue over a guarded subject must still refuse the persisted limit: exit=%d stderr=%q", exit, stderr)
			}
			markers := planGuardMarkerRe.FindAllString(stderr, -1)
			if len(markers) != 1 || !strings.HasPrefix(markers[0], "plan-guard: limit-") {
				t.Fatalf("stderr markers = %v, want exactly one ^plan-guard: limit- line\n%s", markers, stderr)
			}
			if after := gitSHA(t, dir, "feat-a"); after != before {
				t.Fatalf("feat-a moved from %s to %s; a refusing flagless resume moves no ref", before, after)
			}
		})

		// (viii) route and verb semantics are unchanged by the upgrade.
		t.Run(s.name+"/viii_route_and_verb_semantics_are_unchanged", func(t *testing.T) {
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			saveUpgradeSubject(t, dir, fp, s)

			// The pre-upgrade answer to `--continue --abort`.
			preStdout, preStderr, preExit := runSyncExecute(t, "test-feature", "--continue", "--abort")
			// Rebuild the subject and upgrade it.
			saveUpgradeSubject(t, dir, fp, s)
			clearStepHook(t)
			internal.StepHook = func(stage internal.CheckoutStage, index int) error {
				if stage == internal.StageRestoring {
					return errStop
				}
				return nil
			}
			_, _, _ = runSyncExecute(t, "test-feature", "--continue", "--max-replay-total", "0")
			clearStepHook(t)
			if tx, err := internal.LoadCheckoutTransaction(fp); err != nil || tx.StateVersion != internal.CheckoutTransactionGuardedVersion {
				t.Fatalf("the subject must be upgraded before the post-upgrade comparison (err=%v)", err)
			}

			postStdout, postStderr, postExit := runSyncExecute(t, "test-feature", "--continue", "--abort")
			if preExit != postExit {
				t.Fatalf("`--continue --abort` changed its answer across the upgrade: pre exit=%d post exit=%d\npre=%q/%q\npost=%q/%q",
					preExit, postExit, preStdout, preStderr, postStdout, postStderr)
			}
		})
	}
}

// ===========================================================================
// §13.2a step 10a — the armed v2 -> v3 upgrade happens only AFTER guard
// admission, never before it.
// ===========================================================================

// sha256File returns the SHA-256 of a file's bytes, or fails the test.
func sha256File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestSyncGuardedState_ArmedContinuationUpgradesOnlyAfterGuardAdmission is
// the regression for the early-upgrade bug: an ARMED external cell-5
// continuation that the guard REFUSES must leave the persisted v2 payload
// byte-identical, and the next FLAGLESS `--continue` over it must still be
// unguarded. The upgrade belongs at §13.2a step 10a — after
// EvaluatePlanGuard has admitted the run and after the guard reclaim — and
// nowhere above it.
func TestSyncGuardedState_ArmedContinuationUpgradesOnlyAfterGuardAdmission(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child"); exit == 0 {
		t.Fatal("expected a conflict to persist a cell-5 v2 payload")
	}
	f.detachGuard(t)
	resolveRebase(t, f.wt("child"))

	payloadPath := internal.SyncRunStatePath(f.featurePath)
	before := sha256File(t, payloadPath)
	if pre, err := internal.LoadSyncRunState(f.featurePath); err != nil || pre.StateVersion == internal.SyncRunStateGuardedVersion {
		t.Fatalf("the subject must start UNGUARDED (err=%v)", err)
	}

	// An armed continuation the guard refuses: a limit of 0 against a row
	// with real candidates.
	_, stderr, exit := runSyncExecute(t, f.feature, "--continue", "--max-replay-total", "0")
	if exit != 1 {
		t.Fatalf("the armed continuation must be refused: exit=%d stderr=%q", exit, stderr)
	}
	if n := len(planGuardMarkerRe.FindAllString(stderr, -1)); n != 1 {
		t.Fatalf("stderr carried %d plan-guard markers, want exactly one:\n%s", n, stderr)
	}

	if after := sha256File(t, payloadPath); after != before {
		t.Fatalf("the REFUSED invocation rewrote the persisted payload: sha256 %s -> %s\n"+
			"the armed v2 -> v3 upgrade must sit at §13.2a step 10a, below guard admission", before, after)
	}
	reloaded, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.StateVersion == internal.SyncRunStateGuardedVersion {
		t.Fatal("a refused armed continuation must not upgrade the payload to v3")
	}
	if reloaded.Route == internal.RouteNewMode && reloaded.MaxReplayTotal != nil {
		t.Fatalf("a refused armed continuation must persist no limit, got %+v", reloaded)
	}

	// And the next FLAGLESS continue over that untouched subject is still
	// UNGUARDED: it publishes no effective limit at all.
	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--continue")
	if exit != 0 {
		t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
	}
	guard := planDoc(t, stdout)["guard"].(map[string]any)
	limits := guard["limits"].(map[string]any)
	for _, key := range []string{"max_replay_per_entry", "max_replay_total"} {
		row := limits[key].(map[string]any)
		if row["origin"] != "none" || row["value"] != nil {
			t.Fatalf("guard.limits.%s = %v, want the unarmed {none, null}: the refused run must not have made this subject guarded", key, row)
		}
	}
}

// ===========================================================================
// §22.29 — a continuation refusal leaves every recovery artefact
// byte-identical, hash-compared before and after, on BOTH guarded arms.
// ===========================================================================

// hashArtefacts returns the SHA-256 of every recovery artefact present at
// featurePath, keyed by a stable label; absent artefacts are simply omitted,
// and the caller asserts the same key set before and after.
func hashArtefacts(t *testing.T, featurePath string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for label, path := range map[string]string{
		"payload":      internal.SyncRunStatePath(featurePath),
		"legacy_state": internal.SyncStatePath(featurePath),
		"run_guard":    internal.SyncRunGuardPath(featurePath),
		"transaction":  internal.CheckoutTransactionPath(featurePath),
	} {
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		out[label] = sha256File(t, path)
	}
	return out
}

func assertArtefactsUnchanged(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	if len(before) == 0 {
		t.Fatalf("%s: the fixture produced no recovery artefact to compare", label)
	}
	if len(before) != len(after) {
		t.Fatalf("%s: the artefact SET changed across the refusal: before=%v after=%v", label, before, after)
	}
	for k, v := range before {
		got, present := after[k]
		if !present {
			t.Fatalf("%s: %s disappeared across the refusal", label, k)
		}
		if got != v {
			t.Fatalf("%s: %s changed across the refusal: sha256 %s -> %s", label, k, v, got)
		}
	}
}

// TestSyncGuardedState_Criterion22_29_ContinuationRefusalIsByteIdentical is
// §22.29's executable owner: on the guarded SCOPED arm and on the guarded
// CHECKOUT arm, a continuation refusal leaves the payload, the legacy state
// file, the checkout transaction, the sentinel and `.sync-run.lock`
// byte-identical — every guarded continuation seam sits ABOVE the reclaim.
func TestSyncGuardedState_Criterion22_29_ContinuationRefusalIsByteIdentical(t *testing.T) {
	t.Run("external_guarded_scoped_arm", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		f.makeConflict(t)
		if _, _, exit := runSync(t, f.feature, "--only", "child"); exit == 0 {
			t.Fatal("expected a conflict")
		}
		f.detachGuard(t)
		resolveRebase(t, f.wt("child"))

		before := hashArtefacts(t, f.featurePath)
		if _, ok := before["payload"]; !ok {
			t.Fatalf("the scoped fixture must persist a payload, got %v", before)
		}
		if _, ok := before["run_guard"]; !ok {
			t.Fatalf("the scoped fixture must leave its guard behind, got %v", before)
		}
		_, stderr, exit := runSyncExecute(t, f.feature, "--continue", "--max-replay-total", "0")
		if exit != 1 {
			t.Fatalf("the guarded continuation must refuse: exit=%d stderr=%q", exit, stderr)
		}
		assertArtefactsUnchanged(t, "external scoped", before, hashArtefacts(t, f.featurePath))
	})

	t.Run("checkout_guarded_arm", func(t *testing.T) {
		dir, fp := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		// A conflict-stage guarded transaction whose remaining work exceeds
		// the limit it carries.
		limit := 0
		tx := &internal.CheckoutTransaction{
			StateVersion: internal.CheckoutTransactionGuardedVersion,
			Route:        internal.RouteNewMode,
			Feature:      "test-feature", OriginalBranch: "main", OriginalHEAD: gitSHA(t, dir, "main"),
			Stage: internal.StageSwitched, CurrentIndex: 0,
			MaxReplayTotal: &limit,
			Plan: []internal.CheckoutPlanEntry{
				{Name: "feat-a", Branch: "feat-a", Base: "feat-root",
					NewBaseSHA: gitSHA(t, dir, "feat-root"), LastBaseSHA: gitSHA(t, dir, "main")},
			},
		}
		if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { internal.DeleteCheckoutTransaction(fp) })
		gitRunCS(t, dir, "checkout", "feat-a")

		before := hashArtefacts(t, fp)
		if _, ok := before["transaction"]; !ok {
			t.Fatalf("the checkout fixture must persist a transaction, got %v", before)
		}
		_, stderr, exit := runSyncExecute(t, "test-feature", "--continue")
		if exit != 1 {
			t.Fatalf("the guarded checkout continuation must refuse over its persisted 0: exit=%d stderr=%q", exit, stderr)
		}
		if n := len(planGuardMarkerRe.FindAllString(stderr, -1)); n != 1 {
			t.Fatalf("stderr carried %d plan-guard markers, want exactly one:\n%s", n, stderr)
		}
		assertArtefactsUnchanged(t, "checkout guarded", before, hashArtefacts(t, fp))
	})
}
