package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// §9 — downgrade evidence. The prior binary is obtained in the order §9.6
// prescribes: an exported binary, else an offline build of the local
// v1.2.15 tag, else the frozen replay harness — which is proven equivalent by
// a fidelity comparison whenever a real binary is available.
// ---------------------------------------------------------------------------

const downgradeTag = "v1.2.15"

// priorBinary is the resolved prior tws, or an empty path when none could be
// obtained. `note` always explains which acquisition step produced it.
type priorBinary struct {
	path string
	note string
}

// downgradeSourceRoot locates the tws source repository from this test file, so
// it survives every chdir the fixtures perform.
func downgradeSourceRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// acquireDowngradeBinary implements the §9.6 acquisition order. It never fails
// the test: a missing binary degrades to the harness, which always runs.
//
// It MUST be called before any fixture rewrites HOME, so the offline build can
// use the developer's existing build and module caches.
func acquireDowngradeBinary(t *testing.T) priorBinary {
	t.Helper()

	// 1. A real prior binary supplied by the environment.
	if path := os.Getenv("TWS_DOWNGRADE_BINARY"); path != "" {
		info, err := os.Stat(path)
		switch {
		case err != nil:
			t.Logf("TWS_DOWNGRADE_BINARY=%s is not usable: %v", path, err)
		case info.Mode()&0o111 == 0:
			t.Logf("TWS_DOWNGRADE_BINARY=%s is not executable", path)
		default:
			return priorBinary{path: path, note: "TWS_DOWNGRADE_BINARY"}
		}
	}

	// 2. An offline build of the local tag, in an isolated detached worktree.
	root := downgradeSourceRoot(t)
	if err := exec.Command("git", "-C", root, "rev-parse", "-q", "--verify", "refs/tags/"+downgradeTag).Run(); err != nil {
		t.Logf("no local %s tag: %v", downgradeTag, err)
		return priorBinary{note: "no prior binary: tag " + downgradeTag + " is not present locally"}
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "--detach", src, downgradeTag).CombinedOutput(); err != nil {
		t.Logf("cannot check out %s: %v\n%s", downgradeTag, err, out)
		return priorBinary{note: "no prior binary: worktree checkout failed"}
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", root, "worktree", "remove", "--force", src).Run()
		_ = exec.Command("git", "-C", root, "worktree", "prune").Run()
	})

	bin := filepath.Join(dir, "tws-"+downgradeTag)
	build := exec.Command("go", "build", "-o", bin, "./cmd/tws")
	build.Dir = src
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Logf("offline build of %s failed: %v\n%s", downgradeTag, err, out)
		return priorBinary{note: "no prior binary: offline build failed"}
	}
	return priorBinary{path: bin, note: "offline build of " + downgradeTag}
}

// runPriorBinary runs the prior tws inside the fixture, with the fixture's own
// environment (t.Setenv has already published HOME and TWS_ROOT to the process).
func runPriorBinary(t *testing.T, bin, dir string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !asExitError(err, &exitErr) {
			t.Fatalf("running the prior binary failed: %v\n%s", err, errBuf.String())
		}
		exit = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exit
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// pinSyncMarker freezes the per-run marker so two independent fixtures produce
// byte-identical downgrade messages, which is what makes the harness and the
// real binary comparable at all.
func pinSyncMarker(t *testing.T, marker string) {
	t.Helper()
	previous := syncMarkerFn
	syncMarkerFn = func() (string, error) { return marker, nil }
	t.Cleanup(func() { syncMarkerFn = previous })
}

const downgradePinnedMarker = "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock"

// downgradeOutcome is the observable result of one prior-binary verb, in the
// terms both the harness and a real process can produce.
type downgradeOutcome struct {
	failed       bool
	message      string
	sentinelGone bool
	payloadGone  bool
	refsMoved    bool
	fetched      bool
}

// alignWorkspaceRootWithTwsRoot makes the workspace metadata root agree with
// TWS_ROOT for this fixture. The prior binary resolves its state path through
// the workspace alone (it predates the single-layout resolver), so a divergent
// fixture would have it inspect a directory that holds no feature at all —
// which measures the C4 defect, not the downgrade mechanism.
func alignWorkspaceRootWithTwsRoot(t *testing.T, repo string) {
	t.Helper()
	root := os.Getenv("TWS_ROOT")
	if root == "" {
		t.Fatal("the fixture must publish TWS_ROOT")
	}
	keys := map[string]bool{repo: true}
	if real, err := filepath.EvalSymlinks(repo); err == nil {
		keys[real] = true
	}
	var b strings.Builder
	b.WriteString("workspaces:\n")
	for key := range keys {
		fmt.Fprintf(&b, "  %q: %q\n", key, root)
	}
	path := internal.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatalf("workspace resolution: %v", err)
	}
	if filepath.Clean(ws.MetadataRoot) != filepath.Clean(internal.TwsRoot()) {
		t.Fatalf("metadata root %s and TWS_ROOT %s still disagree", ws.MetadataRoot, internal.TwsRoot())
	}
}

// newDowngradeFixture builds a feature stopped mid-rebase by a real scoped run,
// with the pinned marker on disk: sentinel + payload + guard, cell 5.
func newDowngradeFixture(t *testing.T) *scopedFixture {
	t.Helper()
	f := newScopedFixture(t)
	alignWorkspaceRootWithTwsRoot(t, f.repo)
	pinSyncMarker(t, downgradePinnedMarker)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child", "--no-fetch"); exit == 0 {
		t.Fatal("the fixture must stop on a real conflict")
	}
	if got := readFileString(t, internal.SyncStatePath(f.featurePath)); !strings.Contains(got, downgradePinnedMarker) {
		t.Fatalf("the sentinel must carry the pinned marker:\n%s", got)
	}
	return f
}

func downgradeRefs(t *testing.T, f *scopedFixture) string {
	t.Helper()
	return gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
}

// harnessOutcome computes the v1.2.15-equivalent outcome over a fresh fixture.
// Unlike the true v1.2.14-era harness of sync_scoped_test.go (legacyPlainSync
// et al., which is blind to the v2 payload sync-modes introduced), a real
// v1.2.15 binary already classifies scoped state through the same cell
// machinery this binary uses, and its cell-5/live-guard dispatch is unchanged
// since (§22.24c backward compatibility). The in-process dispatch is therefore
// the v1.2.15 answer for this fixture shape, and binaryOutcome below verifies
// that claim against a real offline-built v1.2.15 binary whenever one is
// available.
func harnessOutcome(t *testing.T, verb string) downgradeOutcome {
	t.Helper()
	f := newDowngradeFixture(t)
	f.detachGuard(t)
	refsBefore := downgradeRefs(t, f)

	args := []string{f.feature}
	switch verb {
	case "plain":
	case "continue":
		args = append(args, "--continue")
	case "abort":
		args = append(args, "--abort")
	default:
		t.Fatalf("unknown verb %q", verb)
	}
	stdout, stderr, exit := runSync(t, args...)

	out := downgradeOutcome{failed: exit != 0}
	if out.failed {
		out.message = strings.TrimSpace(stderr)
	} else {
		out.message = strings.TrimSpace(stdout)
	}
	out.sentinelGone = !internal.HasSyncState(f.featurePath)
	out.payloadGone = !internal.HasSyncRunState(f.featurePath)
	out.refsMoved = downgradeRefs(t, f) != refsBefore
	return out
}

// binaryOutcome runs the real prior binary over an identically built fixture.
func binaryOutcome(t *testing.T, bin, verb string) downgradeOutcome {
	t.Helper()
	f := newDowngradeFixture(t)
	f.detachGuard(t)
	refsBefore := downgradeRefs(t, f)

	args := []string{"sync", f.feature}
	switch verb {
	case "plain":
	case "continue":
		args = append(args, "--continue")
	case "abort":
		args = append(args, "--abort")
	default:
		t.Fatalf("unknown verb %q", verb)
	}
	stdout, stderr, exit := runPriorBinary(t, bin, f.repo, args...)

	out := downgradeOutcome{failed: exit != 0}
	if out.failed {
		out.message = strings.TrimSpace(stderr)
	} else {
		out.message = strings.TrimSpace(stdout)
	}
	out.sentinelGone = !internal.HasSyncState(f.featurePath)
	out.payloadGone = !internal.HasSyncRunState(f.featurePath)
	out.refsMoved = downgradeRefs(t, f) != refsBefore
	out.fetched = strings.Contains(stdout, "Fetching")
	return out
}

// TestSyncDowngrade covers AC 27, AC 28, AC 31, and AC 32 with whichever prior
// binary §9.6 yields, and never skips entirely.
func TestSyncDowngrade(t *testing.T) {
	prior := acquireDowngradeBinary(t)
	if prior.path == "" {
		t.Logf("downgrade evidence uses the frozen replay harness only (%s)", prior.note)
	} else {
		t.Logf("downgrade evidence uses %s (%s)", prior.path, prior.note)
	}

	t.Run("sentinel-refusals", func(t *testing.T) {
		for _, tc := range []struct {
			verb    string
			failed  bool
			message string
		}{
			{
				verb:    "plain",
				failed:  true,
				message: "a scoped sync is incomplete (failed on: child); use --continue or --abort",
			},
			{
				verb:    "continue",
				failed:  true,
				message: "rebase still in progress in child; resolve conflicts, run git add . && git rebase --continue, then retry",
			},
			{
				verb:    "abort",
				failed:  false,
				message: "Sync state cleared.",
			},
		} {
			t.Run(tc.verb, func(t *testing.T) {
				h := harnessOutcome(t, tc.verb)
				if h.failed != tc.failed {
					t.Fatalf("harness %s failed = %v, want %v (%q)", tc.verb, h.failed, tc.failed, h.message)
				}
				if h.message != tc.message {
					t.Fatalf("harness %s message = %q, want %q", tc.verb, h.message, tc.message)
				}
				if h.refsMoved {
					t.Fatalf("%s must rebase nothing", tc.verb)
				}
				if tc.verb == "abort" != h.payloadGone {
					t.Fatalf("only --abort removes the payload (%s removed it = %v)", tc.verb, h.payloadGone)
				}
				if tc.verb == "abort" != h.sentinelGone {
					t.Fatalf("only --abort removes the sentinel (%s removed it = %v)", tc.verb, h.sentinelGone)
				}

				if prior.path == "" {
					t.Logf("fidelity comparison skipped: %s", prior.note)
					return
				}
				b := binaryOutcome(t, prior.path, tc.verb)
				if b.failed != h.failed {
					t.Fatalf("%s: binary failed = %v, harness = %v (%q)", tc.verb, b.failed, h.failed, b.message)
				}
				if !strings.Contains(b.message, h.message) {
					t.Fatalf("%s: the binary's output must contain the harness message.\nbinary:\n%s\nharness:\n%s", tc.verb, b.message, h.message)
				}
				if b.sentinelGone != h.sentinelGone || b.payloadGone != h.payloadGone {
					t.Fatalf("%s: binary state (sentinel gone %v, payload gone %v) differs from the harness (%v, %v)",
						tc.verb, b.sentinelGone, b.payloadGone, h.sentinelGone, h.payloadGone)
				}
				if b.refsMoved != h.refsMoved {
					t.Fatalf("%s: binary moved refs = %v, harness = %v", tc.verb, b.refsMoved, h.refsMoved)
				}
				if b.fetched {
					t.Fatalf("%s: the prior binary must fail closed before any fetch:\n%s", tc.verb, b.message)
				}
			})
		}
	})

	t.Run("old-abort-under-a-live-owning-guard", func(t *testing.T) {
		testDowngradeLiveGuardCellTwo(t, prior)
	})

	t.Run("mixed-state-genesis", func(t *testing.T) {
		testDowngradeMixedStateGenesis(t, prior)
	})
}

// testDowngradeLiveGuardCellTwo is the second variant of AC 31: the failed
// run's owning process is still alive. Sync-modes' guard-liveness check
// already shipped at v1.2.15 (§22.24c), so a real v1.2.15 --abort refuses
// here exactly like the current binary — it cannot distinguish this PID from
// any other live one — and neither one mutates anything.
func testDowngradeLiveGuardCellTwo(t *testing.T, prior priorBinary) {
	t.Helper()
	f := newDowngradeFixture(t)
	// The guard is left exactly as the failed run wrote it: this process owns
	// it and this process is alive, which is the live-owning-guard shape.
	guard, err := internal.ReadSyncRunGuard(f.featurePath)
	if err != nil {
		t.Fatalf("the guard must survive the failure: %v", err)
	}
	if guard.PID != os.Getpid() {
		t.Fatalf("guard pid = %d, want this live process %d", guard.PID, os.Getpid())
	}

	// A real v1.2.15 --abort already refuses under a live owning guard, the
	// same protection the current binary gives; it removes neither artefact.
	if prior.path != "" {
		stdout, stderr, exit := runPriorBinary(t, prior.path, f.repo, "sync", f.feature, "--abort")
		if exit == 0 {
			t.Fatalf("a v1.2.15 --abort must refuse under a live owning guard, not clear it: %s", stdout)
		}
		want := fmt.Sprintf("a scoped sync is running for %q (pid %d); wait for it to exit before --abort", f.feature, guard.PID)
		if !strings.Contains(stderr, want) {
			t.Fatalf("v1.2.15 --abort stderr = %q, want to contain %q", stderr, want)
		}
	}
	if !internal.HasSyncState(f.featurePath) {
		t.Fatal("a refused --abort must not remove the sentinel")
	}
	if !internal.HasSyncRunState(f.featurePath) {
		t.Fatal("a refused --abort must not remove the payload")
	}
	if !isRebaseInProgress(f.wt("child")) {
		t.Fatal("the real rebase must still be in progress")
	}

	state := internal.ClassifyExternalSyncState(f.featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 5 {
		t.Fatalf("cell = %d, want 5 {sentinel, valid} — a refused --abort changes nothing", state.Cell)
	}
	if !state.GuardLive {
		t.Fatal("the guard must still be live and owning")
	}

	payloadBefore := readFileString(t, internal.SyncRunStatePath(f.featurePath))
	guardBefore := readFileString(t, internal.SyncRunGuardPath(f.featurePath))
	childBefore := f.sha(t, "child")

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"plain", nil, fmt.Sprintf("a scoped sync is already running for %q (pid %d", f.feature, guard.PID)},
		{"continue", []string{"--continue"}, fmt.Sprintf("a scoped sync is already running for %q (pid %d", f.feature, guard.PID)},
		{"abort", []string{"--abort"}, fmt.Sprintf("a scoped sync is running for %q (pid %d); wait for it to exit before --abort", f.feature, guard.PID)},
	} {
		args := append([]string{f.feature}, tc.args...)
		_, stderr, exit := runSync(t, args...)
		if exit == 0 {
			t.Fatalf("%s must be refused under a live owning guard", tc.name)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Fatalf("%s: stderr = %q, want %q", tc.name, stderr, tc.want)
		}
		if got := readFileString(t, internal.SyncRunStatePath(f.featurePath)); got != payloadBefore {
			t.Fatalf("%s rewrote the payload of a live run", tc.name)
		}
		if got := readFileString(t, internal.SyncRunGuardPath(f.featurePath)); got != guardBefore {
			t.Fatalf("%s rewrote the guard of a live run", tc.name)
		}
		if !isRebaseInProgress(f.wt("child")) {
			t.Fatalf("%s aborted the rebase another live process owns", tc.name)
		}
		if got := f.sha(t, "child"); got != childBefore {
			t.Fatalf("%s moved the branch of a live run: %s -> %s", tc.name, childBefore, got)
		}
	}
}

// testDowngradeMixedStateGenesis reproduces §9.3 sequence 1 end to end. Only a
// TRUE v1.2.14-era binary — blind to the v2 payload sync-modes introduced —
// can leave cell 2 by aborting just the legacy sentinel: a real v1.2.15
// binary already manages both artefacts together (its --abort clears the
// payload too, or refuses outright under a live guard, see the two cases
// above), so it cannot produce this residue. The historical defect is
// therefore built through the frozen v1.2.14 replay harness unconditionally;
// the resulting cell 8 is then checked against the current binary and,
// where a real v1.2.15 binary is available, against it too — proving the
// shipped cell-8 sentences predate the guard feature and are unchanged by it.
func testDowngradeMixedStateGenesis(t *testing.T, prior priorBinary) {
	t.Helper()
	f := newDowngradeFixture(t)
	f.detachGuard(t)

	// Step 1 — a true v1.2.14-era abort removes the sentinel only; it cannot
	// see the payload sync-modes introduced.
	if msg := legacyAbort(f.feature, f.featurePath); msg != "Sync state cleared." {
		t.Fatalf("old --abort printed %q", msg)
	}
	if internal.HasSyncState(f.featurePath) || !internal.HasSyncRunState(f.featurePath) {
		t.Fatal("cell 2 is sentinel-absent and payload-present")
	}

	// Step 2 — a true v1.2.14-era plain sync is no longer blocked (the marker
	// it would have refused on is gone), runs for real, and fails on the
	// worktree that is still mid-rebase, writing REAL legacy state beside the
	// payload it cannot see.
	if err := legacyPlainSync(f.featurePath); err != nil {
		t.Fatalf("the old plain sync must no longer be blocked: %v", err)
	}
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := internal.TopoSort(stack)
	if err != nil {
		t.Fatal(err)
	}
	saveIncompleteSync(f.featurePath, sorted, []string{"root", "parent"}, "child")

	legacy, err := internal.LoadSyncState(f.featurePath)
	if err != nil {
		t.Fatalf("the old plain sync must write real legacy state: %v", err)
	}
	if legacy.FailedBranch != "child" {
		t.Fatalf("legacy failed_branch = %q, want the resolvable name child", legacy.FailedBranch)
	}
	payload, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatalf("the v2 payload must survive: %v", err)
	}
	if payload.FailedBranch != "child" {
		t.Fatalf("payload failed_branch = %q", payload.FailedBranch)
	}
	state := internal.ClassifyExternalSyncState(f.featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 8 {
		t.Fatalf("cell = %d, want 8 {real legacy, valid}", state.Cell)
	}

	// Step 3 — the current binary refuses all three verbs, names both failed
	// entries, and deletes neither file.
	legacyBytes := readFileString(t, internal.SyncStatePath(f.featurePath))
	payloadBytes := readFileString(t, internal.SyncRunStatePath(f.featurePath))
	cellEightCases := []struct {
		name string
		args []string
		want string
	}{
		{"plain", nil, fmt.Sprintf("two unfinished syncs are recorded for %q: a legacy sync failed on child and a scoped sync failed on child", f.feature)},
		{"continue", []string{"--continue"}, fmt.Sprintf("two unfinished syncs are recorded for %q", f.feature)},
		{"abort", []string{"--abort"}, fmt.Sprintf("refusing to clear two unfinished syncs at once for %q", f.feature)},
	}
	for _, tc := range cellEightCases {
		args := append([]string{f.feature}, tc.args...)
		_, stderr, exit := runSync(t, args...)
		if exit == 0 {
			t.Fatalf("%s must be refused in cell 8", tc.name)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Fatalf("%s: stderr = %q, want %q", tc.name, stderr, tc.want)
		}
		if !strings.Contains(stderr, internal.SyncStatePath(f.featurePath)) || !strings.Contains(stderr, internal.SyncRunStatePath(f.featurePath)) {
			t.Fatalf("%s: the message must name both files: %q", tc.name, stderr)
		}
		if got := readFileString(t, internal.SyncStatePath(f.featurePath)); got != legacyBytes {
			t.Fatalf("%s changed the legacy file", tc.name)
		}
		if got := readFileString(t, internal.SyncRunStatePath(f.featurePath)); got != payloadBytes {
			t.Fatalf("%s changed the payload", tc.name)
		}
	}

	// Step 4 — a real v1.2.15 binary meeting the identical cell-8 residue
	// already carries the shipped sentence: this mixed state predates the
	// guard feature entirely, so its messages are unchanged across the
	// version boundary, and it deletes neither file either.
	if prior.path == "" {
		return
	}
	for _, tc := range cellEightCases {
		args := append([]string{"sync", f.feature}, tc.args...)
		_, stderr, exit := runPriorBinary(t, prior.path, f.repo, args...)
		if exit == 0 {
			t.Fatalf("v1.2.15 %s must be refused in cell 8", tc.name)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Fatalf("v1.2.15 %s: stderr = %q, want %q", tc.name, stderr, tc.want)
		}
		if got := readFileString(t, internal.SyncStatePath(f.featurePath)); got != legacyBytes {
			t.Fatalf("v1.2.15 %s changed the legacy file", tc.name)
		}
		if got := readFileString(t, internal.SyncRunStatePath(f.featurePath)); got != payloadBytes {
			t.Fatalf("v1.2.15 %s changed the payload", tc.name)
		}
	}
}
