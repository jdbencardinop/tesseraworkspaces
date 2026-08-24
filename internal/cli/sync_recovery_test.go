package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// TestSyncRecovery: the three no-flag recovery cells (spec §12.8/§12.8a) —
// handleSyncAbortCell's case-1 and case-7 arms, and the cell-4 guarded-
// sentinel interception's absent verdict.
// ---------------------------------------------------------------------------

// TestSyncRecovery_Case1AbsentIsUnchanged proves the case-1 arm's absent rung
// is byte-identical to today's plain "nothing to abort" behaviour: a feature
// with no guard, no sentinel and no payload at all.
func TestSyncRecovery_Case1AbsentIsUnchanged(t *testing.T) {
	f := newScopedFixture(t)
	stdout, stderr, exit := runSync(t, f.feature, "--abort")
	if exit != 0 {
		t.Fatalf("an absent guard must not refuse --abort: exit=%d stderr=%q", exit, stderr)
	}
	if !strings.Contains(stdout, "Nothing to abort — no sync in progress.") {
		t.Fatalf("stdout = %q, want the unchanged absent message", stdout)
	}
	f.stateFilesGone(t)
}

// TestSyncRecovery_Case1DeadGuardReleases proves the new case-1 rung this
// session's fix wires up: a guard survives alone (no sentinel, no payload)
// because the process that claimed it crashed between ClaimSyncRunGuard and
// the first state write. --abort must now actually clear it, using a real,
// independently-verified-dead PID rather than the fixed 999999 convention.
func TestSyncRecovery_Case1DeadGuardReleases(t *testing.T) {
	f := newScopedFixture(t)
	dead := spawnDeadPID(t)
	if err := internal.ClaimSyncRunGuard(f.featurePath, "case1-dead-token"); err != nil {
		t.Fatal(err)
	}
	f.detachGuardPreservingBytes(t, dead)

	stdout, stderr, exit := runSync(t, f.feature, "--abort")
	if exit != 0 {
		t.Fatalf("a dead guard alone must be released: exit=%d stderr=%q", exit, stderr)
	}
	want := fmt.Sprintf("Stale sync guard from PID %d cleared; no sync state was present.\n", dead)
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	f.stateFilesGone(t)
}

// TestSyncRecovery_Case1LiveOwnerVsSelfTwins is the "live-owner vs self"
// contrast for case 1: a guard whose PID is a genuinely live FOREIGN process
// refuses with the wait-for-it-to-exit sentence, while a guard whose PID is
// this process's own refuses with the distinct self-recorded sentence —
// ReleaseStaleSyncRunGuard's default AllowSelfPID:false. Both refusals are
// non-destructive: the guard file is byte-for-byte unchanged afterward,
// which is what "a refusal preserves state" means operationally here.
func TestSyncRecovery_Case1LiveOwnerVsSelfTwins(t *testing.T) {
	t.Run("live-foreign", func(t *testing.T) {
		f := newScopedFixture(t)
		livePID, cleanup := spawnLivePID(t)
		defer cleanup()
		if err := internal.ClaimSyncRunGuard(f.featurePath, "case1-live-token"); err != nil {
			t.Fatal(err)
		}
		f.detachGuardPreservingBytes(t, livePID)
		before, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
		if err != nil {
			t.Fatal(err)
		}

		_, stderr, exit := runSync(t, f.feature, "--abort")
		if exit == 0 {
			t.Fatal("a live foreign guard must refuse --abort")
		}
		want := fmt.Sprintf("a scoped sync is running for %q (pid %d); wait for it to exit before --abort", f.feature, livePID)
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
		after, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("a refusal must preserve the guard byte-for-byte:\n before=%q\n after=%q", before, after)
		}
	})

	t.Run("self", func(t *testing.T) {
		f := newScopedFixture(t)
		if err := internal.ClaimSyncRunGuard(f.featurePath, "case1-self-token"); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
		if err != nil {
			t.Fatal(err)
		}
		selfPID := os.Getpid()

		_, stderr, exit := runSync(t, f.feature, "--abort")
		if exit == 0 {
			t.Fatal("a self-recorded guard must refuse --abort under case 1 (AllowSelfPID is false there)")
		}
		want := fmt.Sprintf("sync guard at %s records this process (pid %d); it was not claimed by this invocation — inspect it and remove it manually",
			internal.SyncRunGuardPath(f.featurePath), selfPID)
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
		after, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("a refusal must preserve the guard byte-for-byte:\n before=%q\n after=%q", before, after)
		}
	})
}

// TestSyncRecovery_Case1SymlinkAndInvalidPIDRefuseWithoutTouchingDisk covers
// the two remaining case-1 rungs that need no live process at all: a guard
// path that is a symlink (never followed, never read), and a guard document
// whose pid field is non-positive (still being initialized, or hand-
// corrupted). Both refuse and leave every byte on disk untouched.
func TestSyncRecovery_Case1SymlinkAndInvalidPIDRefuseWithoutTouchingDisk(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		f := newScopedFixture(t)
		guardPath := internal.SyncRunGuardPath(f.featurePath)
		if err := os.MkdirAll(f.featurePath, 0o755); err != nil {
			t.Fatal(err)
		}
		target := guardPath + ".target"
		if err := os.WriteFile(target, []byte("pid: 4242\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, guardPath); err != nil {
			t.Fatal(err)
		}

		_, stderr, exit := runSync(t, f.feature, "--abort")
		if exit == 0 {
			t.Fatal("a symlinked guard path must refuse --abort")
		}
		if !strings.Contains(stderr, "runtime state path is a symlink") {
			t.Fatalf("stderr = %q, want the symlink refusal", stderr)
		}
		info, err := os.Lstat(guardPath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("the symlink itself must survive a refusal untouched")
		}
	})

	t.Run("invalid-pid", func(t *testing.T) {
		f := newScopedFixture(t)
		guardPath := internal.SyncRunGuardPath(f.featurePath)
		if err := os.MkdirAll(f.featurePath, 0o755); err != nil {
			t.Fatal(err)
		}
		body := []byte("pid: 0\ncreated: \"2020-01-01T00:00:00Z\"\ntoken: \"deadbeef\"\nstate_version: 2\n")
		if err := os.WriteFile(guardPath, body, 0o600); err != nil {
			t.Fatal(err)
		}

		_, stderr, exit := runSync(t, f.feature, "--abort")
		if exit == 0 {
			t.Fatal("a non-positive pid must refuse --abort")
		}
		if !strings.Contains(stderr, "sync guard is being initialized or is invalid; retry or inspect") {
			t.Fatalf("stderr = %q, want the invalid-pid refusal", stderr)
		}
		after, err := os.ReadFile(guardPath)
		if err != nil || string(after) != string(body) {
			t.Fatalf("a refusal must preserve the guard byte-for-byte: got %q, want %q", after, body)
		}
	})
}

// ---------------------------------------------------------------------------
// Case 7 — a real (non-sentinel) legacy .sync-state.yaml beside a stale guard.
// ---------------------------------------------------------------------------

// legacyRealStateFixture writes a genuine (non-marker) legacy sentinel
// directly, the same way TestSyncScoped_I20RefusesTriggerFlagsOnLegacyContinue
// does, so classifySyncState reports cell 7 (legacyReal + payloadAbsent).
func legacyRealStateFixture(t *testing.T, f *scopedFixture) {
	t.Helper()
	state := internal.NewSyncState()
	state.FailedBranch = "parent"
	if err := internal.SaveSyncState(f.featurePath, state); err != nil {
		t.Fatal(err)
	}
}

// TestSyncRecovery_Case7DeadGuardClearsBoth proves the case-7 combined
// message: a dead guard beside a real legacy sentinel releases the guard AND
// performs the ordinary legacy abort in the same invocation, saying both
// happened.
func TestSyncRecovery_Case7DeadGuardClearsBoth(t *testing.T) {
	f := newScopedFixture(t)
	legacyRealStateFixture(t, f)
	dead := spawnDeadPID(t)
	if err := internal.ClaimSyncRunGuard(f.featurePath, "case7-dead-token"); err != nil {
		t.Fatal(err)
	}
	f.detachGuardPreservingBytes(t, dead)

	stdout, stderr, exit := runSync(t, f.feature, "--abort")
	if exit != 0 {
		t.Fatalf("case 7 with a dead guard must succeed: exit=%d stderr=%q", exit, stderr)
	}
	want := fmt.Sprintf("Sync state cleared; stale sync guard from PID %d cleared.\n", dead)
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	f.stateFilesGone(t)
}

// TestSyncRecovery_Case7SelfPIDReleases is the case-7 twin of case 1's
// self-pid refusal: case 7 passes AllowSelfPID:true (an --abort never claims
// a guard, so a self-recorded PID cannot be a live concurrent owner), so the
// SAME condition that refuses under case 1 actually releases under case 7.
func TestSyncRecovery_Case7SelfPIDReleases(t *testing.T) {
	f := newScopedFixture(t)
	legacyRealStateFixture(t, f)
	if err := internal.ClaimSyncRunGuard(f.featurePath, "case7-self-token"); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runSync(t, f.feature, "--abort")
	if exit != 0 {
		t.Fatalf("case 7 must allow releasing its own self-recorded guard: exit=%d stderr=%q", exit, stderr)
	}
	if !strings.HasPrefix(stdout, "Sync state cleared; stale sync guard from PID ") {
		t.Fatalf("stdout = %q, want the case-7 combined message", stdout)
	}
	f.stateFilesGone(t)
}

// TestSyncRecovery_Case7FoundFalseFallsBackToGuardOnlyMessage drives
// handleLegacyGuardedAbort directly (white-box): if the legacy sentinel
// vanished by the time this arm's own abortLegacySyncState runs — the same
// kind of classify/act race §13.7a documents for cell 4 — found is false and
// the guard-only fallback sentence is used instead of the combined one.
func TestSyncRecovery_Case7FoundFalseFallsBackToGuardOnlyMessage(t *testing.T) {
	f := newScopedFixture(t)
	dead := spawnDeadPID(t)
	if err := internal.ClaimSyncRunGuard(f.featurePath, "case7-vanished-token"); err != nil {
		t.Fatal(err)
	}
	f.detachGuardPreservingBytes(t, dead)
	// No .sync-state.yaml at all: abortLegacySyncState will report found=false.

	stdout, stderr := syncCaptureStreams(t, func() {
		if err := handleLegacyGuardedAbort(f.feature, f.layout); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	_ = stderr
	want := fmt.Sprintf("Stale sync guard from PID %d cleared; no sync state was present.\n", dead)
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	f.stateFilesGone(t)
}

// ---------------------------------------------------------------------------
// Cell 4 — the guarded-legacy-sentinel interception's absent verdict.
// ---------------------------------------------------------------------------

// TestSyncRecovery_Cell4AbsentVerdictReclassifiesAndDispatchesDirectly drives
// dispatchGuardedLegacySentinel directly (white-box) with a deliberately
// stale `state` argument that claims cell 4, while the real, on-disk feature
// is entirely empty (a genuine cell-1 shape). Because the SentinelAbsent
// branch performs its own fresh classifySyncState and redispatches through
// dispatchClassifiedSync directly — never re-entering this interceptor — the
// observed outcome must match the REAL disk contents (cell 1, plain
// "nothing to continue"), not whatever the stale argument implied.
func TestSyncRecovery_Cell4AbsentVerdictReclassifiesAndDispatchesDirectly(t *testing.T) {
	f := newScopedFixture(t)
	staleState := internal.SyncExternalState{Cell: 4} // deliberately wrong: disk has nothing at all

	cmd := syncCmd()
	handled, err := dispatchGuardedLegacySentinel(cmd, f.feature, f.layout, internal.Workspace{Mode: internal.ModeExternal}, internal.SyncRunPolicy{}, false, false, false, true, false, map[string]bool{}, planGuardOptions{}, staleState)
	if !handled {
		t.Fatal("the absent verdict must report handled=true")
	}
	if err == nil || !strings.Contains(err.Error(), "nothing to continue") {
		t.Fatalf("err = %v, want the real cell-1 --continue refusal (proving a fresh classify, not the stale cell-4 argument)", err)
	}
	f.stateFilesGone(t)
}

// TestSyncRecovery_Cell4PlainRunRefusalPreservesState constructs a genuine
// SentinelValid, resumable cell-4 state through the exact same production
// primitive runGuardedLegacySync itself calls at birth
// (setupGuardedLegacyRunState), then reproduces §13.2a's crash window 2 —
// `{backup sentinel, guard}` with no payload — by removing the payload the
// setup wrote, exactly as a process killed between step 3 and step 4 would
// have left the directory. This then proves a subsequent plain (no
// --continue/--abort) invocation refuses without removing or rewriting a
// single byte of either the sentinel or the guard — the concrete,
// behavioural meaning of "a refusal preserves state."
func TestSyncRecovery_Cell4PlainRunRefusalPreservesState(t *testing.T) {
	f := newScopedFixture(t)
	buildResumableCell4Fixture(t, f)

	sentinelBefore, err := os.ReadFile(internal.SyncStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	guardBefore, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runSync(t, f.feature)
	if exit == 0 {
		t.Fatalf("a resumable guarded sentinel must refuse a plain run: stdout=%q", stdout)
	}
	// §12.8b's plain-verb row, byte for byte: it names the preserved
	// document's own path and both recovery verbs, and it is NOT the bare
	// "partial state" wording.
	wantRefusal := fmt.Sprintf(
		"a guarded sync was interrupted while recording state for %q; the previous sync state is preserved inside %s — use --continue to resume it, or --abort to discard it",
		f.feature, internal.SyncStatePath(f.featurePath))
	if !strings.Contains(stderr, wantRefusal) {
		t.Fatalf("stderr = %q, want the byte-exact §12.8b plain-verb refusal %q", stderr, wantRefusal)
	}

	sentinelAfter, err := os.ReadFile(internal.SyncStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	guardAfter, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(sentinelBefore) != string(sentinelAfter) {
		t.Fatalf("the refusal must not touch the sentinel:\n before=%q\n after=%q", sentinelBefore, sentinelAfter)
	}
	if string(guardBefore) != string(guardAfter) {
		t.Fatalf("the refusal must not touch the guard:\n before=%q\n after=%q", guardBefore, guardAfter)
	}
}

// TestSyncRecovery_Cell4AbortDiscardsTheBackupByteExactly is §12.8b's
// `--abort` row driven at RUNTIME through the real `tws sync <f> --abort`
// dispatch over a REAL crash residue (buildResumableCell4Fixture), asserting
// the one changed byte-sequence exactly and in full.
//
// --abort here is a CLEAR verb, never a restore verb: it discards a document
// `--continue` could still have resumed. The spec therefore forbids the bare
// shipped `Sync state cleared.` — which would hide that destruction — and
// forbids §12.8's and §12.8a's guard-only lines, which claim something else
// happened. Every one of those three is asserted absent here, so a
// regression that merely reverts the sentence fails on its own.
func TestSyncRecovery_Cell4AbortDiscardsTheBackupByteExactly(t *testing.T) {
	f := newScopedFixture(t)
	buildResumableCell4Fixture(t, f)

	stdout, stderr, exit := runSync(t, f.feature, "--abort")
	if exit != 0 {
		t.Fatalf("a cell-4 --abort over a valid guarded sentinel must succeed: exit=%d stderr=%q", exit, stderr)
	}
	const want = "Sync state cleared; the interrupted guarded setup's backup of the previous sync state was discarded.\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want exactly %q", stdout, want)
	}
	if strings.Contains(stdout, "plan-guard:") {
		t.Fatalf("the discard line is not a plan-guard marker: %q", stdout)
	}
	for _, forbidden := range []string{
		"Sync state cleared.\n",
		"stale sync guard from PID",
		"no sync state was present",
	} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("stdout = %q must not contain %q: it would claim something else happened", stdout, forbidden)
		}
	}
	f.stateFilesGone(t)
}

// TestSyncRecovery_Cell4AbortRefusesWhenAPayloadAppearedUnderIt pins the one
// removal step §12.8b keeps from the shipped cell-4 abort verbatim: where a
// scoped payload appeared beside the sentinel while aborting, the shipped
// refusal fires and NOTHING is removed — the discard sentence is never
// printed, and the backup survives for a re-run.
func TestSyncRecovery_Cell4AbortRefusesWhenAPayloadAppearedUnderIt(t *testing.T) {
	f := newScopedFixture(t)
	buildResumableCell4Fixture(t, f)

	sentinelBefore, err := os.ReadFile(internal.SyncStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	marker, err := syncMarkerFn()
	if err != nil {
		t.Fatal(err)
	}
	// The payload really does appear DURING the abort: syncClassifierBarrier
	// fires between the classifier's cell-4 verdict and the interception's
	// own sentinel read, which is exactly the window §13.7a names.
	t.Cleanup(func() { syncClassifierBarrierHook = nil })
	syncClassifierBarrierHook = func(featurePath string) error {
		syncClassifierBarrierHook = nil
		return internal.SaveSyncRunState(featurePath,
			internal.NewSyncRunState(f.feature, marker, "payload-token", internal.SyncRunPolicy{}))
	}

	stdout, stderr, exit := runSync(t, f.feature, "--abort")
	if exit == 0 {
		t.Fatalf("a payload appearing under the sentinel must refuse: stdout=%q", stdout)
	}
	wantRefusal := fmt.Sprintf("scoped sync state appeared at %s while aborting; re-run: tws sync %s --abort",
		internal.SyncRunStatePath(f.featurePath), f.feature)
	if !strings.Contains(stderr, wantRefusal) {
		t.Fatalf("stderr = %q, want the shipped refusal %q", stderr, wantRefusal)
	}
	if strings.Contains(stdout, "discarded") {
		t.Fatalf("a refusing abort must never claim the backup was discarded: %q", stdout)
	}
	sentinelAfter, err := os.ReadFile(internal.SyncStatePath(f.featurePath))
	if err != nil {
		t.Fatalf("the refusal must leave the backup sentinel in place: %v", err)
	}
	if string(sentinelBefore) != string(sentinelAfter) {
		t.Fatalf("the refusal must not touch the sentinel:\n before=%q\n after=%q", sentinelBefore, sentinelAfter)
	}
}

// ---------------------------------------------------------------------------
// §23.1 seam 2a — the classifier -> cell-4 interception race.
// ---------------------------------------------------------------------------

// buildResumableCell4Fixture produces the §13.2a crash-window-2 residue
// — {backup sentinel, guard}, no payload — the ONLY way that residue is ever
// really produced: by a REAL child process that is genuinely terminated
// (os.Exit) inside setupGuardedLegacyRunState at SyncStageInitializing hook
// index 1, after the sentinel has landed and before the payload does.
//
// It is deliberately NOT built by running the setup to completion and then
// deleting the payload: that path runs every defer, every rollback and every
// in-process cleanup the real crash provably never runs, so it can only ever
// prove that the CLEANED-UP shape dispatches, never that the shape a real
// crash leaves does.
//
// The guard the crashed child claimed is then made unambiguously dead
// through detachGuardPreservingBytes over a spawnDeadPID — a real process
// that really exited and was polled until the OS agreed it is gone —
// rewriting ONLY the recorded pid line and leaving every other byte the real
// production marshaler wrote exactly as it wrote it. The fixed 999999
// convention is never used here: this fixture is about what a real crash
// leaves behind, and a hand-built guard file is not that.
func buildResumableCell4Fixture(t *testing.T, f *scopedFixture) (deadPID int) {
	t.Helper()
	return buildResumableCell4FixtureWithLimit(t, f, nil)
}

// buildResumableCell4FixtureWithLimit is buildResumableCell4Fixture with one
// added degree of freedom: the effective max_replay_total the interrupted
// setup had already committed to. nil reproduces the unarmed residue every
// other crash-window test uses, byte for byte.
func buildResumableCell4FixtureWithLimit(t *testing.T, f *scopedFixture, maxTotal *int) (deadPID int) {
	t.Helper()
	stack, err := internal.LoadStack(f.layout.FeaturePath)
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := internal.TopoSort(stack)
	if err != nil {
		t.Fatal(err)
	}
	universe := make([]string, 0, len(sorted))
	for _, entry := range sorted {
		universe = append(universe, entry.Name)
	}
	marker, err := syncMarkerFn()
	if err != nil {
		t.Fatal(err)
	}
	token, err := newSyncOwnerToken()
	if err != nil {
		t.Fatal(err)
	}
	in := crashSetupInputs{feature: f.feature, marker: marker, token: token, universe: universe, maxTotal: maxTotal}

	exitCode, output := runCrashChild(t, f.featurePath, in, 1, false)
	if exitCode != 1 {
		t.Fatalf("the child must terminate via its real os.Exit(1) hook at initializing index 1, got exit=%d\noutput:\n%s", exitCode, output)
	}

	view := internal.InspectGuardedLegacySentinel(f.featurePath, f.feature)
	if view.Verdict != internal.SentinelValid {
		t.Fatalf("a real crash at index 1 must leave a valid guarded backup sentinel, got verdict=%q", view.Verdict)
	}
	if internal.HasSyncRunState(f.featurePath) {
		t.Fatal("crash window 2 must leave NO payload beside the sentinel")
	}
	if _, err := os.Lstat(internal.SyncRunGuardPath(f.featurePath)); err != nil {
		t.Fatalf("the crashed child's claimed guard must survive its death: %v", err)
	}
	deadPID = spawnDeadPID(t)
	f.detachGuardPreservingBytes(t, deadPID)
	return deadPID
}

// TestSyncRecovery_ClassifierBarrierRaceReclassifiesExactlyOnce drives the
// §23.1 seam 2a race deterministically: syncClassifierBarrierHook fires in
// the window between classifySyncState's cell-4 verdict and
// dispatchGuardedLegacySentinel's own InspectGuardedLegacySentinel read, so
// a hook that mutates .sync-state.yaml there simulates a second actor acting
// on the feature directory before the interception ever looks at it again.
// Every subtest starts from the SAME genuine, resumable cell-4 fixture
// (buildResumableCell4Fixture), removes the sentinel from inside the hook —
// forcing dispatchGuardedLegacySentinel's own read to see SentinelAbsent —
// and varies what the second actor leaves the GUARD as, covering all four
// shapes the spec's vanished-state row names: no guard at all, a stale
// (dead-PID) guard, a live foreign guard, and a self-recorded guard. Each
// subtest asserts:
//   - syncReclassifyCount incremented by EXACTLY 1 (the SentinelAbsent arm's
//     documented "at most once" contract, made executable),
//   - the barrier itself fired exactly once (it is the interception's own
//     entry gate: a second firing would be direct, executable proof of
//     re-entry, so asserting it never happens is how "the interception is
//     never re-entered" is checked here rather than merely asserted in
//     prose),
//   - the outcome is the ORDINARY answer of the state the second read
//     finds — never the stale cell-4 message the first, now-invalid
//     classify implied.
func TestSyncRecovery_ClassifierBarrierRaceReclassifiesExactlyOnce(t *testing.T) {
	t.Run("no-guard-at-all", func(t *testing.T) {
		t.Run("continue", func(t *testing.T) {
			f := newScopedFixture(t)
			buildResumableCell4Fixture(t, f)
			fires := 0
			syncClassifierBarrierHook = func(featurePath string) error {
				fires++
				if err := internal.RemoveSyncState(featurePath); err != nil {
					return err
				}
				return internal.RemoveSyncRunGuard(featurePath)
			}
			t.Cleanup(func() { syncClassifierBarrierHook = nil })

			before := syncReclassifyCount.Load()
			_, stderr, exit := runSync(t, f.feature, "--continue")
			after := syncReclassifyCount.Load()

			if exit != 1 {
				t.Fatalf("exit = %d, want 1: stderr=%q", exit, stderr)
			}
			if !strings.Contains(stderr, "nothing to continue — no sync in progress") {
				t.Fatalf("stderr = %q, want the ordinary cell-1 --continue refusal", stderr)
			}
			if delta := after - before; delta != 1 {
				t.Fatalf("syncReclassifyCount delta = %d, want exactly 1", delta)
			}
			if fires != 1 {
				t.Fatalf("barrier fires = %d, want exactly 1 (a second firing would mean re-entry)", fires)
			}
			f.stateFilesGone(t)
		})

		t.Run("abort", func(t *testing.T) {
			f := newScopedFixture(t)
			buildResumableCell4Fixture(t, f)
			fires := 0
			syncClassifierBarrierHook = func(featurePath string) error {
				fires++
				if err := internal.RemoveSyncState(featurePath); err != nil {
					return err
				}
				return internal.RemoveSyncRunGuard(featurePath)
			}
			t.Cleanup(func() { syncClassifierBarrierHook = nil })

			before := syncReclassifyCount.Load()
			stdout, stderr, exit := runSync(t, f.feature, "--abort")
			after := syncReclassifyCount.Load()

			if exit != 0 {
				t.Fatalf("exit = %d, want 0: stderr=%q", exit, stderr)
			}
			if !strings.Contains(stdout, "Nothing to abort — no sync in progress.") {
				t.Fatalf("stdout = %q, want the ordinary cell-1 --abort message", stdout)
			}
			if delta := after - before; delta != 1 {
				t.Fatalf("syncReclassifyCount delta = %d, want exactly 1", delta)
			}
			if fires != 1 {
				t.Fatalf("barrier fires = %d, want exactly 1 (a second firing would mean re-entry)", fires)
			}
			f.stateFilesGone(t)
		})
	})

	t.Run("dead-guard", func(t *testing.T) {
		f := newScopedFixture(t)
		deadPID := buildResumableCell4Fixture(t, f)
		fires := 0
		syncClassifierBarrierHook = func(featurePath string) error {
			fires++
			// Only the sentinel vanishes; the pre-existing dead-PID guard
			// (buildResumableCell4Fixture's own byte-preserving detach) survives.
			return internal.RemoveSyncState(featurePath)
		}
		t.Cleanup(func() { syncClassifierBarrierHook = nil })

		before := syncReclassifyCount.Load()
		stdout, stderr, exit := runSync(t, f.feature, "--abort")
		after := syncReclassifyCount.Load()

		if exit != 0 {
			t.Fatalf("exit = %d, want 0: stderr=%q", exit, stderr)
		}
		want := fmt.Sprintf("Stale sync guard from PID %d cleared; no sync state was present.\n", deadPID)
		if stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
		if delta := after - before; delta != 1 {
			t.Fatalf("syncReclassifyCount delta = %d, want exactly 1", delta)
		}
		if fires != 1 {
			t.Fatalf("barrier fires = %d, want exactly 1 (a second firing would mean re-entry)", fires)
		}
		f.stateFilesGone(t)
	})

	t.Run("live-foreign-guard", func(t *testing.T) {
		f := newScopedFixture(t)
		buildResumableCell4Fixture(t, f)
		livePID, cleanup := spawnLivePID(t)
		defer cleanup()
		fires := 0
		syncClassifierBarrierHook = func(featurePath string) error {
			fires++
			if err := internal.RemoveSyncState(featurePath); err != nil {
				return err
			}
			f.detachGuardPreservingBytes(t, livePID)
			return nil
		}
		t.Cleanup(func() { syncClassifierBarrierHook = nil })

		before := syncReclassifyCount.Load()
		_, stderr, exit := runSync(t, f.feature, "--abort")
		after := syncReclassifyCount.Load()

		if exit == 0 {
			t.Fatal("a live foreign guard must refuse --abort")
		}
		want := fmt.Sprintf("a scoped sync is running for %q (pid %d); wait for it to exit before --abort", f.feature, livePID)
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
		if delta := after - before; delta != 1 {
			t.Fatalf("syncReclassifyCount delta = %d, want exactly 1", delta)
		}
		if fires != 1 {
			t.Fatalf("barrier fires = %d, want exactly 1 (a second firing would mean re-entry)", fires)
		}
		// A refusal preserves state: the guard the second actor installed
		// survives untouched (never claimed away from it).
		guardAfter, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(guardAfter), fmt.Sprintf("pid: %d", livePID)) {
			t.Fatalf("guard = %q, want it to still record pid %d", guardAfter, livePID)
		}
	})

	t.Run("self-owned-guard", func(t *testing.T) {
		f := newScopedFixture(t)
		buildResumableCell4Fixture(t, f)
		fires := 0
		syncClassifierBarrierHook = func(featurePath string) error {
			fires++
			if err := internal.RemoveSyncState(featurePath); err != nil {
				return err
			}
			f.detachGuardPreservingBytes(t, os.Getpid())
			return nil
		}
		t.Cleanup(func() { syncClassifierBarrierHook = nil })

		before := syncReclassifyCount.Load()
		_, stderr, exit := runSync(t, f.feature, "--abort")
		after := syncReclassifyCount.Load()

		if exit == 0 {
			t.Fatal("a self-recorded guard must refuse --abort under case 1 (AllowSelfPID is false there)")
		}
		want := fmt.Sprintf("sync guard at %s records this process (pid %d); it was not claimed by this invocation — inspect it and remove it manually",
			internal.SyncRunGuardPath(f.featurePath), os.Getpid())
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
		if delta := after - before; delta != 1 {
			t.Fatalf("syncReclassifyCount delta = %d, want exactly 1", delta)
		}
		if fires != 1 {
			t.Fatalf("barrier fires = %d, want exactly 1 (a second firing would mean re-entry)", fires)
		}
		guardAfter, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(guardAfter), fmt.Sprintf("pid: %d", os.Getpid())) {
			t.Fatalf("guard = %q, want it to still record this process's own pid %d", guardAfter, os.Getpid())
		}
	})
}

// TestSyncRecovery_ClassifierBarrierCannotSpin is the NO-LOOP proof item 2
// requires: a barrier hook that removes .sync-state.yaml — the exact action
// that forces dispatchGuardedLegacySentinel's own read to observe
// SentinelAbsent — is exercised across N independent rounds, rebuilding the
// fixture to a fresh, valid, resumable cell-4 sentinel before each one.
//
// A hook cannot make a SINGLE round satisfy both "reclassifies exactly
// once" and "recreates a valid guarded sentinel" at the same time:
// dispatchGuardedLegacySentinel calls internal.InspectGuardedLegacySentinel
// exactly once, synchronously, immediately after syncClassifierBarrier
// returns, so whatever the hook leaves behind on disk IS what that one read
// sees. If the hook leaves a valid sentinel in place, the read reports
// SentinelValid (not Absent) and syncReclassifyCount.Add(1) is never
// reached at all — a delta of 0, not 1. "Recreates a valid guarded
// sentinel each time it fires" is therefore proven here across REPEATED
// exposure instead, which is what a no-spin claim actually needs: each of N
// rounds independently reaches the interception, the SAME barrier hook
// removes the sentinel (forcing that round's own absence read and its own
// single reclassification), the command terminates and answers correctly
// in one pass, and the fixture is then rebuilt to a fresh valid cell-4
// sentinel before the next round begins. If the interception could ever
// loop, retry internally, or accumulate state across the race recurring
// over and over, some round would show more than one reclassification or
// more than one barrier firing, or the command would fail to terminate;
// none of that happens in any of the N rounds below.
func TestSyncRecovery_ClassifierBarrierCannotSpin(t *testing.T) {
	f := newScopedFixture(t)
	t.Cleanup(func() { syncClassifierBarrierHook = nil })

	const rounds = 3
	for round := 0; round < rounds; round++ {
		deadPID := buildResumableCell4Fixture(t, f)

		fires := 0
		syncClassifierBarrierHook = func(featurePath string) error {
			fires++
			return internal.RemoveSyncState(featurePath)
		}

		before := syncReclassifyCount.Load()
		stdout, stderr, exit := runSync(t, f.feature, "--abort")
		after := syncReclassifyCount.Load()

		if exit != 0 {
			t.Fatalf("round %d: exit = %d, want 0: stderr=%q", round, exit, stderr)
		}
		want := fmt.Sprintf("Stale sync guard from PID %d cleared; no sync state was present.\n", deadPID)
		if stdout != want {
			t.Fatalf("round %d: stdout = %q, want %q", round, stdout, want)
		}
		if delta := after - before; delta != 1 {
			t.Fatalf("round %d: syncReclassifyCount delta = %d, want exactly 1 (no accumulation, no spin)", round, delta)
		}
		if fires != 1 {
			t.Fatalf("round %d: barrier fires = %d, want exactly 1 (no spin)", round, fires)
		}
		f.stateFilesGone(t)
	}
}

// ---------------------------------------------------------------------------
// §23.1 seam 2 — the run's one state snapshot -> guard seam window.
// ---------------------------------------------------------------------------

// TestSyncRecovery_SnapshotBarrierDecidesOverThePreMutationSnapshot drives a
// FRESH guarded external run (runGuardedLegacySync, reached because
// --max-replay-total arms the guard and no scope-trigger flag requests new
// mode) through syncSnapshotBarrierHook — the window inside
// buildGuardedExternalPlan between the plan built from the run's one
// InspectExternalPlan snapshot and internal.EvaluatePlanGuard's own
// admission verdict over that plan. internal.EvaluatePlanGuard is a pure
// function of the already-built plan (it reads only plan.Refusal.Kind,
// plan.Runnable and plan.Guard.ExecuteBlockedBy, and makes zero filesystem
// calls of its own), so a hook that plants a live, foreign .sync-run.lock
// in this exact window cannot change admission's verdict: admission still
// passes over the pre-mutation snapshot, and the invocation proceeds to
// setupGuardedLegacyRunState's OWN, later, independent
// internal.ClaimSyncRunGuard call, which DOES re-read the filesystem and
// genuinely finds the planted live guard.
//
// The black-box proof is the shape of the failure: no "plan-guard: " marker
// line — planGuardRefusal/writePlanGuardMarker print that only for a
// *internal.PlanGuardRefusalError, and admission never produced one — paired
// with the later claim's own plain "already running" sentence. A refusal
// for a completely different, later reason than admission is exactly what
// "decided over the snapshot" means operationally.
func TestSyncRecovery_SnapshotBarrierDecidesOverThePreMutationSnapshot(t *testing.T) {
	f := newScopedFixture(t)
	livePID, cleanup := spawnLivePID(t)
	defer cleanup()

	fires := 0
	syncSnapshotBarrierHook = func(featurePath string) error {
		fires++
		body := fmt.Sprintf("pid: %d\ncreated: %q\ntoken: %q\nstate_version: 2\n", livePID, "2020-01-01T00:00:00Z", "racer-token")
		return os.WriteFile(internal.SyncRunGuardPath(featurePath), []byte(body), 0o600)
	}
	t.Cleanup(func() { syncSnapshotBarrierHook = nil })

	stdout, stderr, exit := runSync(t, f.feature, "--no-fetch", "--max-replay-total", "5")
	if exit == 0 {
		t.Fatalf("the later real claim must refuse once the barrier plants a live foreign guard: stdout=%q", stdout)
	}
	if strings.Contains(stderr, "plan-guard: ") {
		t.Fatalf("stderr = %q, must NOT carry a plan-guard marker: admission itself must have passed over the pre-mutation snapshot", stderr)
	}
	want := fmt.Sprintf("a scoped sync is already running for %q (pid %d, started %s)", f.feature, livePID, "2020-01-01T00:00:00Z")
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to contain the later claim's own refusal %q", stderr, want)
	}
	if fires != 1 {
		t.Fatalf("barrier fires = %d, want exactly 1", fires)
	}

	// The planted racer guard is the only artifact left behind, untouched
	// (never claimed away from it): no sentinel or payload was ever written,
	// since setup's own claim failed before either of those later steps.
	guardBytes, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(guardBytes), fmt.Sprintf("pid: %d", livePID)) {
		t.Fatalf("guard = %q, want it to still be the racer's own guard", guardBytes)
	}
	if _, err := os.Lstat(internal.SyncStatePath(f.featurePath)); err == nil {
		t.Fatal("no sentinel must have been written: setup must have failed before its own sentinel step")
	}
	if internal.HasSyncRunState(f.featurePath) {
		t.Fatal("no payload must have been written")
	}
}

// ---------------------------------------------------------------------------
// §23.1 seam 3 — the reclaimability verdict -> compare-and-swap window.
// ---------------------------------------------------------------------------

// TestSyncRecovery_ReclaimBarrierRace drives internal.SyncReclaimBarrier, the
// window inside internal.ReclaimSyncRunGuard between
// checkSyncRunGuardReclaimable's verdict and the compare-and-swap
// (removeLockIfUnchanged) that acts on it. Neither subtest needs a real Git
// fixture: ReclaimSyncRunGuard is pure file I/O over a feature directory, so
// a bare t.TempDir() is the whole subject, matching the existing
// newTeardownState convention in this package.
func TestSyncRecovery_ReclaimBarrierRace(t *testing.T) {
	t.Run("guard-changes-fails-closed", func(t *testing.T) {
		featurePath := t.TempDir()
		// A self-owned guard passes checkSyncRunGuardReclaimable trivially
		// (guard.PID == os.Getpid() short-circuits its liveness check),
		// which keeps this subtest deterministic without a spawned process.
		if err := internal.ClaimSyncRunGuard(featurePath, "original-token"); err != nil {
			t.Fatal(err)
		}
		guardPath := internal.SyncRunGuardPath(featurePath)
		racerBytes := []byte(fmt.Sprintf("pid: %d\ncreated: %q\ntoken: %q\nstate_version: 2\n", os.Getpid(), "2020-01-01T00:00:00Z", "racer-token"))

		fires := 0
		internal.SyncReclaimBarrier = func(fp string) error {
			fires++
			if fp != featurePath {
				t.Fatalf("barrier featurePath = %q, want %q", fp, featurePath)
			}
			// The second actor's own race: the guard's bytes change to
			// something checkSyncRunGuardReclaimable never saw, inside the
			// window between its verdict and the compare-and-swap.
			return os.WriteFile(guardPath, racerBytes, 0o600)
		}
		t.Cleanup(func() { internal.SyncReclaimBarrier = nil })

		err := internal.ReclaimSyncRunGuard(featurePath, "reclaim-token")
		if err == nil {
			t.Fatal("a guard that changed inside the barrier window must fail closed")
		}
		if !strings.Contains(err.Error(), "reclaim sync guard") {
			t.Fatalf("err = %v, want it to contain %q", err, "reclaim sync guard")
		}
		if fires != 1 {
			t.Fatalf("barrier fires = %d, want exactly 1", fires)
		}
		after, readErr := os.ReadFile(guardPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(after) != string(racerBytes) {
			t.Fatalf("the guard must not be stolen: got %q, want the racer's own untouched bytes %q", after, racerBytes)
		}
	})

	t.Run("owner-dies-race-succeeds", func(t *testing.T) {
		featurePath := t.TempDir()
		dead := spawnDeadPID(t)
		guardPath := internal.SyncRunGuardPath(featurePath)
		body := fmt.Sprintf("pid: %d\ncreated: %q\ntoken: %q\nstate_version: 2\n", dead, "2020-01-01T00:00:00Z", "dead-owner-token")
		if err := os.WriteFile(guardPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		fires := 0
		internal.SyncReclaimBarrier = func(string) error {
			fires++
			return nil
		}
		t.Cleanup(func() { internal.SyncReclaimBarrier = nil })

		if err := internal.ReclaimSyncRunGuard(featurePath, "new-owner-token"); err != nil {
			t.Fatalf("reclaiming a provably dead owner's guard must succeed: %v", err)
		}
		if fires != 1 {
			t.Fatalf("barrier fires = %d, want exactly 1", fires)
		}
		guard, err := internal.ReadSyncRunGuard(featurePath)
		if err != nil {
			t.Fatal(err)
		}
		if guard.Token != "new-owner-token" {
			t.Fatalf("guard.Token = %q, want %q (the reclaim must have actually taken it over)", guard.Token, "new-owner-token")
		}
		if guard.PID != os.Getpid() {
			t.Fatalf("guard.PID = %d, want this process's own pid %d (writeSyncGuardExclusive always stamps the current process)", guard.PID, os.Getpid())
		}
	})
}

// ---------------------------------------------------------------------------
// §13.2a crash windows — real out-of-process termination, and the
// returning-hook companion.
// ---------------------------------------------------------------------------

// crashSetupInputs is the one (feature, marker, token, universe) tuple both
// the real-crash child process (item 5) and the in-process returning-hook
// test (item 6) drive setupGuardedLegacyRunState with, so any difference in
// outcome between the two can only be the crash mechanism itself, never a
// difference in what was asked for.
type crashSetupInputs struct {
	feature  string
	marker   string
	token    string
	universe []string

	// maxTotal is the EFFECTIVE max_replay_total the interrupted setup had
	// already committed to, or nil for the unarmed fixtures every approved
	// crash-window test uses. It exists so a cell-4 fixture can carry a real
	// persisted limit and the §12.8b limit table (inherit / confirm /
	// conflict) becomes drivable end-to-end.
	maxTotal *int
}

// limitTotalEnv renders maxTotal for the child's environment: "" when the
// setup was unarmed, so an absent variable and an unarmed run are the same
// thing on both sides of the process boundary.
func (in crashSetupInputs) limitTotalEnv() string {
	if in.maxTotal == nil {
		return ""
	}
	return strconv.Itoa(*in.maxTotal)
}

func newCrashSetupInputs(t *testing.T) crashSetupInputs {
	t.Helper()
	marker, err := syncMarkerFn()
	if err != nil {
		t.Fatal(err)
	}
	token, err := newSyncOwnerToken()
	if err != nil {
		t.Fatal(err)
	}
	return crashSetupInputs{
		feature:  "crash-feature",
		marker:   marker,
		token:    token,
		universe: []string{"root", "parent", "child"},
	}
}

// carry mirrors carriedGuardedLegacyState's own production computation
// (sync.go's cell-7 caller derives its carry the same way, from the real
// loaded legacy state) so a continuation-arm sentinel's PriorPendingCount
// always agrees with the prior document's actual len(Pending) — the exact
// invariant InspectGuardedLegacySentinel's corrupt-detection ladder checks.
// On the fresh arm there is no prior document, so the carry is the zero
// value, matching sync.go's own fresh-route call site.
func (in crashSetupInputs) carry(continuation bool) guardedLegacyCarry {
	if !continuation {
		return guardedLegacyCarry{}
	}
	return carriedGuardedLegacyState(crashPriorLegacyState())
}

func (in crashSetupInputs) pending(continuation bool) []string {
	return guardedLegacySetupPending(in.universe, in.carry(continuation))
}

func (in crashSetupInputs) birth() syncRunStateBirth {
	return syncRunStateBirth{
		StateVersion: internal.SyncRunStateGuardedVersion, Route: internal.RouteLegacy,
		MaxTotal: in.maxTotal,
	}
}

// crashPriorLegacyState is the ONE fixed prior legacy document both the
// parent (which writes it to disk via writeCrashPriorLegacyState) and the
// child (which must derive the identical guardedLegacyCarry a real cell-7
// continuation caller would compute) build from, so the two processes can
// never drift apart on what the continuation arm's subject actually is.
func crashPriorLegacyState() *internal.SyncState {
	prior := internal.NewSyncState()
	prior.FailedBranch = "parent"
	prior.Completed = []string{"root"}
	prior.Pending = []string{"child"}
	return prior
}

// writeCrashPriorLegacyState optionally writes a real, pre-existing legacy
// .sync-state.yaml at featurePath — the continuation arm's subject, which a
// crash or a returning hook must either leave completely untouched or
// restore byte-for-byte — and returns its exact bytes. On the fresh arm it
// writes nothing and returns nil.
func writeCrashPriorLegacyState(t *testing.T, featurePath string, continuation bool) []byte {
	t.Helper()
	if !continuation {
		return nil
	}
	if err := internal.SaveSyncState(featurePath, crashPriorLegacyState()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(internal.SyncStatePath(featurePath))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The environment discriminator runCrashChild and TestSyncRecoveryCrashChild
// agree on: the crash index, the continuation arm, and the whole
// crashSetupInputs fixture, communicated entirely by environment variable
// across the process boundary.
const (
	crashChildIndexEnv        = "TWS_SYNC_RECOVERY_CRASH_INDEX"
	crashChildFeaturePathEnv  = "TWS_SYNC_RECOVERY_CRASH_FEATURE_PATH"
	crashChildFeatureEnv      = "TWS_SYNC_RECOVERY_CRASH_FEATURE"
	crashChildMarkerEnv       = "TWS_SYNC_RECOVERY_CRASH_MARKER"
	crashChildTokenEnv        = "TWS_SYNC_RECOVERY_CRASH_TOKEN"
	crashChildUniverseEnv     = "TWS_SYNC_RECOVERY_CRASH_UNIVERSE"
	crashChildLimitTotalEnv   = "TWS_SYNC_RECOVERY_CRASH_MAX_TOTAL"
	crashChildContinuationEnv = "TWS_SYNC_RECOVERY_CRASH_CONTINUATION"
)

// runCrashChild re-executes the current test binary restricted to
// TestSyncRecoveryCrashChild alone (via -test.run, anchored so it cannot
// also match some other test), communicating the crash index and fixture
// entirely through the environment. The child installs internal.SyncStepHook
// to call os.Exit(1) at the requested (SyncStageInitializing, index)
// boundary and calls setupGuardedLegacyRunState for real, in a genuinely
// separate OS process: nothing about its crash — no defer, no rollback, no
// in-process cleanup of any kind — ever runs, which is the one thing a
// returning hook (item 6, below) cannot reproduce.
func runCrashChild(t *testing.T, featurePath string, in crashSetupInputs, index int, continuation bool) (exitCode int, output string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSyncRecoveryCrashChild$")
	continuationVal := ""
	if continuation {
		continuationVal = "1"
	}
	cmd.Env = append(os.Environ(),
		crashChildIndexEnv+"="+strconv.Itoa(index),
		crashChildFeaturePathEnv+"="+featurePath,
		crashChildFeatureEnv+"="+in.feature,
		crashChildMarkerEnv+"="+in.marker,
		crashChildTokenEnv+"="+in.token,
		crashChildUniverseEnv+"="+strings.Join(in.universe, ","),
		crashChildContinuationEnv+"="+continuationVal,
		crashChildLimitTotalEnv+"="+in.limitTotalEnv(),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), string(out)
	}
	t.Fatalf("failed to run the crash child process: %v\noutput:\n%s", err, out)
	return -1, string(out)
}

// TestSyncRecoveryCrashChild is the re-exec target runCrashChild drives, not
// an ordinary test in its own right: run without
// TWS_SYNC_RECOVERY_CRASH_INDEX set — which is how the mandated
// `-run 'TestSyncRecovery'` itself discovers and runs it as a normal top-
// level test — it does nothing and passes trivially, the same "return
// immediately unless the discriminator env var is set" idiom the standard
// library's own os/exec_test.go TestHelperProcess uses to let a test binary
// safely re-exec itself as a controlled child process. With the env var
// set, it installs a REAL crash hook and calls setupGuardedLegacyRunState,
// terminating via os.Exit before ever returning to the testing package —
// a genuine process death, not a returned error.
func TestSyncRecoveryCrashChild(t *testing.T) {
	idxStr := os.Getenv(crashChildIndexEnv)
	if idxStr == "" {
		return
	}
	index, err := strconv.Atoi(idxStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid crash index %q: %v\n", idxStr, err)
		os.Exit(2)
	}
	featurePath := os.Getenv(crashChildFeaturePathEnv)
	feature := os.Getenv(crashChildFeatureEnv)
	marker := os.Getenv(crashChildMarkerEnv)
	token := os.Getenv(crashChildTokenEnv)
	universe := strings.Split(os.Getenv(crashChildUniverseEnv), ",")
	continuation := os.Getenv(crashChildContinuationEnv) == "1"

	internal.SyncStepHook = func(stage internal.SyncRunStage, hookIndex int) error {
		if stage == internal.SyncStageInitializing && hookIndex == index {
			os.Exit(1)
		}
		return nil
	}

	layout := newExternalSyncLayout(featurePath)
	in := crashSetupInputs{feature: feature, marker: marker, token: token, universe: universe}
	if raw := os.Getenv(crashChildLimitTotalEnv); raw != "" {
		total, convErr := strconv.Atoi(raw)
		if convErr != nil {
			fmt.Fprintf(os.Stderr, "invalid crash limit %q: %v\n", raw, convErr)
			os.Exit(2)
		}
		in.maxTotal = &total
	}
	_, _, err = setupGuardedLegacyRunState(layout, in.feature, in.marker, in.token, in.universe, in.pending(continuation), false, "", "none", in.birth(), in.carry(continuation))
	if err != nil {
		fmt.Fprintf(os.Stderr, "setupGuardedLegacyRunState returned an error instead of crashing at index %d: %v\n", index, err)
		os.Exit(3)
	}
	fmt.Fprintf(os.Stderr, "setupGuardedLegacyRunState returned successfully instead of crashing at index %d\n", index)
	os.Exit(4)
}

// TestSyncRecovery_GuardedLegacySetupCrashResidue is the mandatory REAL
// out-of-process proof for every §13.2a crash window inside
// setupGuardedLegacyRunState's four SyncStageInitializing boundaries: 3 the
// capture, then 0 the claim, 1 the sentinel, 2 the payload. Each subtest
// spawns runCrashChild, which re-executes this test binary as a fresh OS
// process that installs a real os.Exit(1) hook and calls
// setupGuardedLegacyRunState for real: the process is genuinely killed at
// the requested boundary, so no defer, no rollback and no in-process
// cleanup of any kind ever runs. The parent then inspects the on-disk
// residue exactly the way §13.2a names it, on both the fresh arm (nothing
// preceded this run) and the continuation arm (a real prior legacy
// .sync-state.yaml existed first).
func TestSyncRecovery_GuardedLegacySetupCrashResidue(t *testing.T) {
	arms := []struct {
		name         string
		continuation bool
	}{
		{"fresh", false},
		{"continuation", true},
	}

	for _, arm := range arms {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			for _, index := range []int{3, 0, 1, 2} {
				index := index
				t.Run(fmt.Sprintf("index=%d", index), func(t *testing.T) {
					featurePath := t.TempDir()
					in := newCrashSetupInputs(t)
					priorBytes := writeCrashPriorLegacyState(t, featurePath, arm.continuation)

					exitCode, output := runCrashChild(t, featurePath, in, index, arm.continuation)
					if exitCode != 1 {
						t.Fatalf("want the child to terminate via its real os.Exit(1) crash hook at index %d, got exit=%d\noutput:\n%s", index, exitCode, output)
					}

					guardBytes, guardErr := os.ReadFile(internal.SyncRunGuardPath(featurePath))
					guardPresent := guardErr == nil
					legacyBytes, legacyErr := os.ReadFile(internal.SyncStatePath(featurePath))
					legacyPresent := legacyErr == nil
					payloadPresent := internal.HasSyncRunState(featurePath)

					checkSubjectUntouched := func() {
						t.Helper()
						if arm.continuation {
							if !legacyPresent || string(legacyBytes) != string(priorBytes) {
								t.Fatalf("index %d must leave the prior legacy state untouched:\n got=%q\n want=%q", index, legacyBytes, priorBytes)
							}
						} else if legacyPresent {
							t.Fatalf("index %d on the fresh arm must leave no legacy state, found:\n%s", index, legacyBytes)
						}
					}

					switch index {
					case 3:
						// The capture boundary: nothing written or claimed at all.
						if guardPresent {
							t.Fatalf("index 3 (the capture boundary) must claim no guard, found:\n%s", guardBytes)
						}
						if payloadPresent {
							t.Fatal("index 3 (the capture boundary) must write no payload")
						}
						checkSubjectUntouched()
					case 0:
						// After the claim, before the sentinel: {guard} plus the
						// untouched subject.
						if !guardPresent {
							t.Fatal("index 0 (after the claim) must leave the claimed guard behind")
						}
						if payloadPresent {
							t.Fatal("index 0 (before the sentinel) must write no payload")
						}
						checkSubjectUntouched()
					case 1:
						// After the sentinel, before the payload: {backup sentinel,
						// guard}, no payload, and the prior document recoverable
						// byte-for-byte from the sentinel's own fields.
						if !guardPresent {
							t.Fatal("index 1 (after the sentinel) must leave the claimed guard behind")
						}
						if payloadPresent {
							t.Fatal("index 1 (before the payload) must write no payload")
						}
						view := internal.InspectGuardedLegacySentinel(featurePath, in.feature)
						if view.Verdict != internal.SentinelValid {
							t.Fatalf("index 1 must leave a valid backup sentinel, got verdict=%q", view.Verdict)
						}
						if arm.continuation {
							if !view.Sentinel.PriorLegacyPresent {
								t.Fatal("index 1 on the continuation arm must record a prior legacy document")
							}
							decoded, err := base64.StdEncoding.DecodeString(view.Sentinel.PriorLegacyBase64)
							if err != nil {
								t.Fatalf("prior_legacy_base64 must decode: %v", err)
							}
							if string(decoded) != string(priorBytes) {
								t.Fatalf("prior_legacy_base64 must recover the original document byte-for-byte:\n got=%q\n want=%q", decoded, priorBytes)
							}
							sum := sha256.Sum256(priorBytes)
							if view.Sentinel.PriorLegacySHA256 != hex.EncodeToString(sum[:]) {
								t.Fatalf("prior_legacy_sha256 = %q, want the sha256 of the original document (%x)", view.Sentinel.PriorLegacySHA256, sum)
							}
						} else if view.Sentinel.PriorLegacyPresent {
							t.Fatal("index 1 on the fresh arm must record no prior legacy document")
						}
					case 2:
						// After the payload: {payload, backup sentinel, guard}.
						if !guardPresent {
							t.Fatal("index 2 (after the payload) must leave the claimed guard behind")
						}
						if !payloadPresent {
							t.Fatal("index 2 (after the payload) must leave the payload behind")
						}
						view := internal.InspectGuardedLegacySentinel(featurePath, in.feature)
						if view.Verdict != internal.SentinelValid {
							t.Fatalf("index 2 must leave a valid backup sentinel, got verdict=%q", view.Verdict)
						}
						payload, err := internal.LoadSyncRunState(featurePath)
						if err != nil {
							t.Fatalf("index 2's payload must load: %v", err)
						}
						if payload.StateVersion != internal.SyncRunStateGuardedVersion || payload.Route != internal.RouteLegacy {
							t.Fatalf("index 2's payload must be the guarded legacy envelope, got state_version=%d route=%q", payload.StateVersion, payload.Route)
						}
					}
				})
			}
		})
	}
}

// TestSyncRecovery_GuardedLegacySetupReturningHookLeavesNoResidue is the
// companion item 6 requires: at each of the SAME four §13.2a boundaries, a
// hook that RETURNS an error — rather than crashing the process — drives
// setupGuardedLegacyRunState's own rollbackGuardedLegacyRunState, which must
// undo everything this invocation created (no payload, no sentinel, no
// guard) and, on the continuation arm, restore the original
// .sync-state.yaml bytes exactly, verified here by SHA-256 as well as a
// direct byte comparison. A failure that returns rather than crashes is the
// one case production code itself can, and must, clean up after — the
// direct behavioural contrast with TestSyncRecovery_GuardedLegacySetupCrashResidue's
// real termination above.
func TestSyncRecovery_GuardedLegacySetupReturningHookLeavesNoResidue(t *testing.T) {
	arms := []struct {
		name         string
		continuation bool
	}{
		{"fresh", false},
		{"continuation", true},
	}

	for _, arm := range arms {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			for _, index := range []int{3, 0, 1, 2} {
				index := index
				t.Run(fmt.Sprintf("index=%d", index), func(t *testing.T) {
					featurePath := t.TempDir()
					in := newCrashSetupInputs(t)
					priorBytes := writeCrashPriorLegacyState(t, featurePath, arm.continuation)
					var priorSum [32]byte
					if arm.continuation {
						priorSum = sha256.Sum256(priorBytes)
					}

					injected := fmt.Errorf("injected returning-hook failure at index %d", index)
					withSyncStepHook(t, func(stage internal.SyncRunStage, hookIndex int) error {
						if stage == internal.SyncStageInitializing && hookIndex == index {
							return injected
						}
						return nil
					})

					layout := newExternalSyncLayout(featurePath)
					_, _, err := setupGuardedLegacyRunState(layout, in.feature, in.marker, in.token, in.universe, in.pending(arm.continuation), false, "", "none", in.birth(), in.carry(arm.continuation))
					if err == nil {
						t.Fatalf("index %d: setupGuardedLegacyRunState must return the injected error", index)
					}
					if !strings.Contains(err.Error(), injected.Error()) {
						t.Fatalf("index %d: err = %v, want it to contain the injected cause %q", index, err, injected.Error())
					}

					// No residue at all: wantSentinel tracks mere PRESENCE at
					// .sync-state.yaml, which is exactly arm.continuation here —
					// on the continuation arm the untouched-or-restored prior
					// document is expected to still be there; on the fresh arm
					// nothing must remain.
					assertSyncArtifacts(t, featurePath, arm.continuation, false, false)

					if arm.continuation {
						after, readErr := os.ReadFile(internal.SyncStatePath(featurePath))
						if readErr != nil {
							t.Fatalf("index %d: the original legacy state must be restored, but it is unreadable: %v", index, readErr)
						}
						afterSum := sha256.Sum256(after)
						if afterSum != priorSum {
							t.Fatalf("index %d: restored .sync-state.yaml sha256 = %x, want %x (the original document, byte-for-byte)", index, afterSum, priorSum)
						}
						if string(after) != string(priorBytes) {
							t.Fatalf("index %d: restored .sync-state.yaml bytes differ from the original:\n got=%q\n want=%q", index, after, priorBytes)
						}
					}
				})
			}
		})
	}
}

// TestSyncRecovery_Cell4ContinueResumesAndCompletes is §12.8b's `--continue`
// verb row, driven end-to-end over a REAL crash-window-2 residue: the child
// process really died inside the guarded legacy setup between step 3 and
// step 4 (buildResumableCell4Fixture), leaving `{backup sentinel, guard}`
// and NO payload. A flagless `--continue` over that document MUST resume —
// "matching or flagless --continue resumes ... and no flag is required to
// reach it" is binding — run the pending work through the payload-aware
// executor, and end at the guarded teardown that removes payload, sentinel
// and run guard.
//
// It is the cell the rows-availability rule used to get wrong: a
// continuation whose persisted subject is a sentinel rather than a payload
// still HAS a subject, so the document publishes rows and the guard admits
// the run instead of refusing it as `state-refused`.
func TestSyncRecovery_Cell4ContinueResumesAndCompletes(t *testing.T) {
	f := newScopedFixture(t)
	buildResumableCell4Fixture(t, f)

	view := internal.InspectGuardedLegacySentinel(f.featurePath, f.feature)
	if view.Verdict != internal.SentinelValid || view.Sentinel == nil {
		t.Fatalf("fixture verdict = %q, want a valid sentinel", view.Verdict)
	}
	pending := append([]string(nil), view.Sentinel.PendingIntent...)
	if len(pending) == 0 {
		t.Fatal("the fixture must leave real pending work, or the resume proves nothing")
	}

	// (a) the document the resume is admitted by: rows, runnable, no refusal.
	planOut, _, planExit := runSync(t, f.feature, "--plan", "--json", "--continue")
	if planExit != 0 {
		t.Fatalf("--plan always exits 0: exit=%d", planExit)
	}
	doc := planDoc(t, planOut)
	if got, _ := planField(t, doc, "summary", "plannability").(string); got != "rows" {
		t.Fatalf("plannability = %q, want rows: the sentinel IS this continuation's persisted subject", got)
	}
	if got := planField(t, doc, "runnable"); got != true {
		t.Fatalf("runnable = %v, want true", got)
	}
	if got := planField(t, doc, "refusal", "kind"); got != nil {
		t.Fatalf("refusal.kind = %v, want null", got)
	}
	names := map[string]bool{}
	for _, raw := range planField(t, doc, "entries").([]any) {
		names[raw.(map[string]any)["name"].(string)] = true
	}
	for _, name := range pending {
		if !names[name] {
			t.Fatalf("entries[] = %v, want the sentinel's own pending intent %v", names, pending)
		}
	}
	if len(names) != len(pending) {
		t.Fatalf("entries[] = %v, want EXACTLY the sentinel's pending intent %v: the recovery arm re-plans nothing", names, pending)
	}

	// (b) the run itself completes through the payload-aware executor.
	stdout, stderr, exit := runSyncExecute(t, f.feature, "--continue")
	if exit != 0 {
		t.Fatalf("a flagless --continue over a valid backup sentinel must resume: exit=%d stderr=%q stdout=%q", exit, stderr, stdout)
	}
	if !strings.Contains(stdout, "Sync complete.") {
		t.Fatalf("stdout = %q, want the shipped legacy tail", stdout)
	}
	for _, name := range pending {
		if !strings.Contains(stdout, name) {
			t.Fatalf("stdout = %q, want every pending entry %q to have been processed", stdout, name)
		}
	}
	// §12.8b's prose table: an interrupted FRESH guarded legacy setup has no
	// prior state, so the recovery arm prints NO resume line at all.
	if view.Sentinel.PriorLegacyPresent {
		t.Fatal("this fixture must be the fresh-setup sentinel")
	}
	if strings.Contains(stdout, "Resuming sync with") {
		t.Fatalf("stdout = %q must print no resume line for a fresh-setup sentinel (§22.24j)", stdout)
	}

	// (c) the guarded teardown removed all three artefacts.
	f.stateFilesGone(t)
}

// TestSyncRecovery_Cell4TriggerFlagsRaiseTheShippedI20Sentence is §12.8b's
// binding flag-validation row. A valid backup sentinel never reaches
// syncCellRefusal or the I20 gate below it, so the INTERCEPTION owns the
// check: any of the six mode-trigger flags refuses with the shipped I20
// sentence, byte for byte, on all three verbs, removing nothing and writing
// nothing — while a flagless invocation of the same verb proceeds. The
// --plan route projects the identical sentence as its gate's own detail.
func TestSyncRecovery_Cell4TriggerFlagsRaiseTheShippedI20Sentence(t *testing.T) {
	triggers := [][]string{
		{"--no-fetch"},
		{"--fetch"},
		{"--full"},
		{"--local-only"},
		{"--only", "child"},
		{"--from", "child"},
	}
	verbs := []string{"--continue", "--abort", ""}

	for _, verb := range verbs {
		for _, trigger := range triggers {
			name := strings.TrimPrefix(verb, "--") + "_" + strings.TrimPrefix(trigger[0], "--")
			if verb == "" {
				name = "plain_" + strings.TrimPrefix(trigger[0], "--")
			}
			t.Run(name, func(t *testing.T) {
				f := newScopedFixture(t)
				buildResumableCell4Fixture(t, f)
				sentinelBefore, err := os.ReadFile(internal.SyncStatePath(f.featurePath))
				if err != nil {
					t.Fatal(err)
				}
				guardBefore, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
				if err != nil {
					t.Fatal(err)
				}

				args := []string{f.feature}
				if verb != "" {
					args = append(args, verb)
				}
				args = append(args, trigger...)
				_, stderr, exit := runSyncExecute(t, args...)
				if exit != 1 {
					t.Fatalf("a trigger flag over a valid backup sentinel must refuse: exit=%d stderr=%q", exit, stderr)
				}
				// --abort is refused ABOVE the interception by the shipped I8
				// command-line check, which owns that combination and says so
				// in its own words; every other verb reaches the interception,
				// which composes I20's sentence byte for byte.
				wantLine := "Error: " + errSyncModeFlagsNeedV2
				if verb == "--abort" {
					wantLine = "Error: --abort cannot be combined with " + trigger[0]
				}
				if !strings.Contains(stderr, wantLine) {
					t.Fatalf("stderr = %q, want %q byte for byte", stderr, wantLine)
				}
				if strings.Contains(stderr, "plan-guard:") {
					t.Fatalf("the I20 refusal is a shipped sentence, never a plan-guard marker: %q", stderr)
				}
				if strings.Contains(stderr, "discarded") {
					t.Fatalf("a refusing invocation must never claim the backup was discarded: %q", stderr)
				}

				sentinelAfter, err := os.ReadFile(internal.SyncStatePath(f.featurePath))
				if err != nil {
					t.Fatalf("the refusal must remove nothing: %v", err)
				}
				guardAfter, err := os.ReadFile(internal.SyncRunGuardPath(f.featurePath))
				if err != nil {
					t.Fatalf("the refusal must remove nothing: %v", err)
				}
				if string(sentinelBefore) != string(sentinelAfter) || string(guardBefore) != string(guardAfter) {
					t.Fatal("the refusal must leave both artefacts byte-identical")
				}
				if internal.HasSyncRunState(f.featurePath) {
					t.Fatal("the refusal must write no payload")
				}
			})
		}
	}

	t.Run("plan_projects_the_same_sentence_at_the_same_gate", func(t *testing.T) {
		f := newScopedFixture(t)
		buildResumableCell4Fixture(t, f)
		stdout, _, exit := runSync(t, f.feature, "--plan", "--json", "--continue", "--only", "child")
		if exit != 0 {
			t.Fatalf("--plan always exits 0, got %d", exit)
		}
		doc := planDoc(t, stdout)
		found := false
		for _, raw := range planField(t, doc, "blockers").([]any) {
			b := raw.(map[string]any)
			if b["kind"] != "state-refused" {
				continue
			}
			if b["detail"] == errSyncModeFlagsNeedV2 {
				found = true
			}
		}
		if !found {
			t.Fatalf("the plan must project the I20 sentence as a rank 3 state-refused blocker, got %v", doc["blockers"])
		}
		if got := planField(t, doc, "runnable"); got != false {
			t.Fatalf("runnable = %v, want false under a projected I20 refusal", got)
		}
		// A --plan writes nothing at all: both artefacts survive it.
		if _, err := os.Lstat(internal.SyncStatePath(f.featurePath)); err != nil {
			t.Fatalf("a --plan must remove nothing: %v", err)
		}
		if internal.HasSyncRunState(f.featurePath) {
			t.Fatal("a --plan must write no payload")
		}
	})

	t.Run("the_interception_itself_checks_all_three_verbs", func(t *testing.T) {
		// I8 refuses `--abort` + trigger above the interception, so the
		// interception's own "check runs once, before the verb's arm, for all
		// three verbs" contract is asserted where it lives: calling the
		// dispatch directly with the abort verb and a trigger flag present.
		for _, verb := range []struct {
			name        string
			cont, abort bool
		}{
			{"continue", true, false},
			{"abort", false, true},
			{"plain", false, false},
		} {
			t.Run(verb.name, func(t *testing.T) {
				f := newScopedFixture(t)
				buildResumableCell4Fixture(t, f)
				state, err := classifySyncState(f.featurePath, true)
				if err != nil {
					t.Fatal(err)
				}
				if state.Cell != 4 {
					t.Fatalf("cell = %d, want 4", state.Cell)
				}
				changed := map[string]bool{"only": true}
				handled, err := dispatchGuardedLegacySentinel(syncCmd(), f.feature, f.layout,
					internal.Workspace{Mode: internal.ModeExternal}, internal.SyncRunPolicy{},
					true, false, false, verb.cont, verb.abort, changed, planGuardOptions{}, state)
				if !handled {
					t.Fatal("the interception owns this document on every verb")
				}
				if err == nil || err.Error() != errSyncModeFlagsNeedV2 {
					t.Fatalf("err = %v, want the shipped I20 sentence", err)
				}
				if _, statErr := os.Lstat(internal.SyncStatePath(f.featurePath)); statErr != nil {
					t.Fatalf("the refusal must remove nothing: %v", statErr)
				}
				if internal.HasSyncRunState(f.featurePath) {
					t.Fatal("the refusal must write no payload")
				}
			})
		}
	})

	t.Run("flagless_verbs_still_reach_their_arm", func(t *testing.T) {
		f := newScopedFixture(t)
		buildResumableCell4Fixture(t, f)
		_, stderr, exit := runSyncExecute(t, f.feature, "--abort")
		if exit != 0 {
			t.Fatalf("a flagless --abort must still clear the backup: exit=%d stderr=%q", exit, stderr)
		}
		f.stateFilesGone(t)
	})
}

// TestSyncRecovery_SyncTriggersNeedV2HasNoCell4Arm is §13.6 rule 4a/§25.82's
// placement invariant, asserted two ways: behaviourally, over every payload
// shape at cell 4 (the predicate answers false there, because the cell is
// not its business), and structurally, over the production source (the
// switch has no `case 4`, and the function takes no sentinel view). A second
// cell-4 site inside I20 is exactly the contradiction the interception was
// introduced to remove.
func TestSyncRecovery_SyncTriggersNeedV2HasNoCell4Arm(t *testing.T) {
	payloads := []*internal.SyncRunState{
		nil,
		{StateVersion: 1},
		{StateVersion: 2},
		{StateVersion: 3, Route: internal.RouteLegacy},
		{StateVersion: 3, Route: internal.RouteNewMode},
	}
	for i, p := range payloads {
		if got := syncTriggersNeedV2(internal.SyncExternalState{Cell: 4, Payload: p}); got {
			t.Fatalf("payload %d: syncTriggersNeedV2(cell 4) = true; cell 4 is the interception's business, not I20's", i)
		}
	}

	src := readCLISource(t, "sync_modes.go")
	body := funcBodySource(t, src, "func syncTriggersNeedV2(")
	for _, forbidden := range []string{"case 4", "Sentinel", "GuardedLegacy"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("syncTriggersNeedV2 must not mention %q: it has no cell-4 arm and takes no sentinel view", forbidden)
		}
	}
	// And the interception really is the one site that composes the sentence
	// for this cell.
	dispatch := funcBodySource(t, readCLISource(t, "sync.go"), "func dispatchGuardedLegacySentinel(")
	if !strings.Contains(dispatch, "syncTriggerFlagSupplied(changed)") || !strings.Contains(dispatch, "errSyncModeFlagsNeedV2") {
		t.Fatal("the cell-4 interception must compose the I20 sentence itself, above the gate that can no longer see this document")
	}
}

// readCLISource reads one production file of package cli for a structural
// assertion.
func readCLISource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// funcBodySource returns the source of one function, from its signature line
// to the first line that closes it at column zero.
func funcBodySource(t *testing.T, src, signature string) string {
	t.Helper()
	i := strings.Index(src, signature)
	if i < 0 {
		t.Fatalf("function %q not found", signature)
	}
	rest := src[i:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("function %q is not terminated", signature)
	}
	return rest[:end]
}

// TestSyncRecovery_Cell4LimitTableIsTheSentinelsOwn is §12.8b's limit table,
// driven over a REAL crash residue whose interrupted setup had already
// committed to `max_replay_total: 5`:
//
//	absent    -> INHERIT: the persisted value is effective with origin
//	             persisted-payload, and a flagless --continue resumes;
//	equal     -> CONFIRM: same effective value, no conflict row;
//	different -> CONFLICT: the persisted value stays effective, a rank 7
//	             guard-limit-mismatch blocker and a guard.limit_conflicts[]
//	             row name both values, and no I20 sentence is involved.
func TestSyncRecovery_Cell4LimitTableIsTheSentinelsOwn(t *testing.T) {
	persisted := 5
	limitsOfDoc := func(t *testing.T, doc map[string]any) map[string]any {
		t.Helper()
		guard := planField(t, doc, "guard").(map[string]any)
		limits := guard["limits"].(map[string]any)
		return limits["max_replay_total"].(map[string]any)
	}

	plan := func(t *testing.T, extra ...string) map[string]any {
		t.Helper()
		f := newScopedFixture(t)
		buildResumableCell4FixtureWithLimit(t, f, &persisted)
		args := append([]string{f.feature, "--plan", "--json", "--continue"}, extra...)
		stdout, _, exit := runSync(t, args...)
		if exit != 0 {
			t.Fatalf("--plan always exits 0, got %d", exit)
		}
		return planDoc(t, stdout)
	}

	t.Run("absent_inherits", func(t *testing.T) {
		doc := plan(t)
		total := limitsOfDoc(t, doc)
		if got, _ := total["value"].(float64); int(got) != persisted {
			t.Fatalf("max_replay_total.value = %v, want the persisted %d", total["value"], persisted)
		}
		if total["origin"] != "persisted-payload" {
			t.Fatalf("origin = %v, want persisted-payload", total["origin"])
		}
		guard := planField(t, doc, "guard").(map[string]any)
		if rows, _ := guard["limit_conflicts"].([]any); len(rows) != 0 {
			t.Fatalf("limit_conflicts = %v, want [] when nothing was supplied", rows)
		}
		if got, _ := planField(t, doc, "summary", "plannability").(string); got != "rows" {
			t.Fatalf("plannability = %q, want rows: a flagless --continue over this document resumes", got)
		}
	})

	t.Run("equal_confirms", func(t *testing.T) {
		doc := plan(t, "--max-replay-total", strconv.Itoa(persisted))
		total := limitsOfDoc(t, doc)
		if got, _ := total["value"].(float64); int(got) != persisted {
			t.Fatalf("max_replay_total.value = %v, want %d", total["value"], persisted)
		}
		if total["origin"] != "persisted-payload" {
			t.Fatalf("origin = %v, want persisted-payload", total["origin"])
		}
		guard := planField(t, doc, "guard").(map[string]any)
		if rows, _ := guard["limit_conflicts"].([]any); len(rows) != 0 {
			t.Fatalf("limit_conflicts = %v, want [] when the supplied value CONFIRMS the persisted one", rows)
		}
	})

	t.Run("different_conflicts", func(t *testing.T) {
		const supplied = 99
		doc := plan(t, "--max-replay-total", strconv.Itoa(supplied))
		total := limitsOfDoc(t, doc)
		if got, _ := total["value"].(float64); int(got) != persisted {
			t.Fatalf("max_replay_total.value = %v, want the PERSISTED %d to stay effective", total["value"], persisted)
		}
		guard := planField(t, doc, "guard").(map[string]any)
		rows, _ := guard["limit_conflicts"].([]any)
		if len(rows) != 1 {
			t.Fatalf("limit_conflicts = %v, want exactly one row", rows)
		}
		row := rows[0].(map[string]any)
		if row["key"] != "max_replay_total" || row["effective_origin"] != "persisted-payload" {
			t.Fatalf("conflict row = %v, want the persisted-payload origin on max_replay_total", row)
		}
		if got, _ := row["effective_value"].(float64); int(got) != persisted {
			t.Fatalf("effective_value = %v, want %d", row["effective_value"], persisted)
		}
		if got, _ := row["supplied_value"].(float64); int(got) != supplied {
			t.Fatalf("supplied_value = %v, want %d", row["supplied_value"], supplied)
		}
		found := false
		for _, raw := range planField(t, doc, "blockers").([]any) {
			b := raw.(map[string]any)
			if b["kind"] == "guard-limit-mismatch" {
				found = true
				if strings.Contains(b["detail"].(string), errSyncModeFlagsNeedV2) {
					t.Fatal("a limit conflict is a rank 7 row, never an I20 sentence")
				}
			}
		}
		if !found {
			t.Fatalf("want a rank 7 guard-limit-mismatch blocker, got %v", doc["blockers"])
		}
	})

	t.Run("matching_limit_resumes_at_runtime", func(t *testing.T) {
		f := newScopedFixture(t)
		buildResumableCell4FixtureWithLimit(t, f, &persisted)
		stdout, stderr, exit := runSyncExecute(t, f.feature, "--continue", "--max-replay-total", strconv.Itoa(persisted))
		if exit != 0 {
			t.Fatalf("a MATCHING limit must resume: exit=%d stderr=%q", exit, stderr)
		}
		if !strings.Contains(stdout, "Sync complete.") {
			t.Fatalf("stdout = %q, want the shipped legacy tail", stdout)
		}
		f.stateFilesGone(t)
	})
}
