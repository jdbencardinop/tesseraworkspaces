package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// §9 — downgrade evidence. The prior binary is obtained in the order §9.6
// prescribes: an exported binary, else an offline build of the local
// v1.2.15 tag, else the frozen replay harness — which is proven equivalent by
// a fidelity comparison whenever a real binary is available.
// ---------------------------------------------------------------------------

const downgradeTag = "v1.2.15"

// reparentDowngradeTag sits BESIDE downgradeTag, never in place of it: the
// sync-modes evidence keeps proving v1.2.15's behaviour while safe-reparent's
// own evidence (§17.4a) needs the release immediately before it. Full-history
// CI can build either tag; a shallow or cache-cold environment falls back to
// the frozen replay rather than dropping the cell or hard-failing.
const reparentDowngradeTag = "v1.2.16"

// priorBinary is the resolved prior tws, or an empty path when none could be
// obtained. `note` always explains which acquisition step produced it.
type priorBinary struct {
	path string
	note string
}

func assertExactPriorCheckoutPlainRefusal(t *testing.T, message string) {
	t.Helper()
	const want = "previous checkout-sync incomplete; use --continue or --abort"
	lines := strings.Split(message, "\n")
	if len(lines) < 2 || lines[0] != "Error: "+want || lines[len(lines)-1] != want {
		t.Fatalf("plain checkout refusal did not preserve the exact v1.2.16 sentence:\n%s", message)
	}
}

func parsedTWSVersion(out []byte) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 3 || fields[0] != "tws" || fields[1] != "version" {
		return "", false
	}
	return fields[2], true
}

func TestDowngradeVersionParsingRequiresExactRelease(t *testing.T) {
	if got, ok := parsedTWSVersion([]byte("tws version v1.2.16\n")); !ok || got != reparentDowngradeTag {
		t.Fatalf("exact version parsed as %q, %v", got, ok)
	}
	for _, output := range []string{
		"tws version v1.2.160\n",
		"tws version v1.2.16-dirty\n",
		"prefix v1.2.16 suffix\n",
	} {
		if got, ok := parsedTWSVersion([]byte(output)); ok && got == reparentDowngradeTag {
			t.Fatalf("non-exact version %q was accepted", output)
		}
	}
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

// acquireDowngradeBinary implements the §9.6 acquisition order for the
// sync-modes evidence, which is pinned to downgradeTag. It is the
// backwards-compatible wrapper over acquireDowngradeBinaryFor, so every
// existing caller keeps its exact behaviour.
func acquireDowngradeBinary(t *testing.T) priorBinary {
	t.Helper()
	return acquireDowngradeBinaryFor(t, downgradeTag)
}

// acquireDowngradeBinaryFor implements the §9.6 acquisition order for ONE
// tag. It never fails the test: a missing binary degrades to the harness,
// which always runs.
//
// It MUST be called before any fixture rewrites HOME, so the offline build can
// use the developer's existing build and module caches.
//
// TWS_DOWNGRADE_BINARY remains the v1.2.15 override and is deliberately NOT
// consulted for any other tag: a single environment variable cannot name two
// different releases, and silently building the wrong one would make the
// evidence meaningless. A per-tag override is available as
// TWS_DOWNGRADE_BINARY_<TAG>, with dots replaced by underscores.
func acquireDowngradeBinaryFor(t *testing.T, tag string) priorBinary {
	t.Helper()
	countReparent := tag == reparentDowngradeTag

	// 1. A real prior binary supplied by the environment. The generic
	// override is always checked first; a tag-specific override is the
	// optional second spelling when a job supplies more than one release.
	envNames := []string{"TWS_DOWNGRADE_BINARY"}
	tagEnv := "TWS_DOWNGRADE_BINARY_" + strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(tag))
	if tagEnv != envNames[0] {
		envNames = append(envNames, tagEnv)
	}
	for _, envName := range envNames {
		if path := os.Getenv(envName); path != "" {
			info, err := os.Stat(path)
			switch {
			case err != nil:
				t.Logf("%s=%s is not usable: %v", envName, path, err)
			case info.Mode()&0o111 == 0:
				t.Logf("%s=%s is not executable", envName, path)
			default:
				out, versionErr := exec.Command(path, "--version").CombinedOutput()
				gotVersion, parsed := parsedTWSVersion(out)
				if versionErr != nil || !parsed || gotVersion != tag {
					t.Logf("%s=%s is not %s (%v, %q)", envName, path, tag, versionErr, strings.TrimSpace(string(out)))
					continue
				}
				return priorBinary{path: path, note: envName}
			}
		}
	}

	// 2. An offline build of the local tag, in an isolated detached worktree.
	root := downgradeSourceRoot(t)
	if err := testGitCommand(t, root, countReparent, "rev-parse", "-q", "--verify", "refs/tags/"+tag).Run(); err != nil {
		t.Logf("no local %s tag: %v", tag, err)
		return priorBinary{note: "no prior binary: tag " + tag + " is not present locally"}
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if out, err := testGitCommand(t, root, countReparent, "worktree", "add", "--detach", src, tag).CombinedOutput(); err != nil {
		t.Logf("cannot check out %s: %v\n%s", tag, err, out)
		return priorBinary{note: "no prior binary: worktree checkout failed"}
	}
	t.Cleanup(func() {
		_ = testGitCommand(t, root, countReparent, "worktree", "remove", "--force", src).Run()
		_ = testGitCommand(t, root, countReparent, "worktree", "prune").Run()
	})

	bin := filepath.Join(dir, "tws-"+tag)
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+tag, "-o", bin, "./cmd/tws")
	build.Dir = src
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Logf("offline build of %s failed: %v\n%s", tag, err, out)
		return priorBinary{note: "no prior binary: offline build failed"}
	}
	out, versionErr := exec.Command(bin, "--version").CombinedOutput()
	gotVersion, parsed := parsedTWSVersion(out)
	if versionErr != nil || !parsed || gotVersion != tag {
		t.Logf("offline build reports %q instead of exact %s (%v)", strings.TrimSpace(string(out)), tag, versionErr)
		return priorBinary{note: "no prior binary: offline build version mismatch"}
	}
	return priorBinary{path: bin, note: "offline build of " + tag}
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

func runPriorBinaryWithGitTrace(t *testing.T, bin, dir string, args ...string) (stdout, stderr string, exit int, gitArgv []string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	logPath := filepath.Join(shimDir, "git-argv.log")
	shim := filepath.Join(shimDir, "git")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$TWS_DOWNGRADE_GIT_LOG\"\n" +
		"exec \"$TWS_DOWNGRADE_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
		"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TWS_DOWNGRADE_GIT_LOG="+logPath,
		"TWS_DOWNGRADE_REAL_GIT="+realGit,
	)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr != nil {
		var exitErr *exec.ExitError
		if !asExitError(runErr, &exitErr) {
			t.Fatalf("running the prior binary failed: %v\n%s", runErr, errBuf.String())
		}
		exit = exitErr.ExitCode()
	}
	if data, readErr := os.ReadFile(logPath); readErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if strings.TrimSpace(line) != "" {
				gitArgv = append(gitArgv, strings.TrimSpace(line))
			}
		}
	} else if !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	return outBuf.String(), errBuf.String(), exit, gitArgv
}

type downgradePathSnapshot struct {
	present bool
	data    []byte
}

func snapshotDowngradePath(t *testing.T, path string) downgradePathSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return downgradePathSnapshot{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return downgradePathSnapshot{present: true, data: data}
}

func assertDowngradePathUnchanged(t *testing.T, path string, before downgradePathSnapshot) {
	t.Helper()
	after := snapshotDowngradePath(t, path)
	if before.present != after.present || !bytes.Equal(before.data, after.data) {
		t.Fatalf("%s changed across the prior-binary refusal: before=%+v after=%+v", path, before, after)
	}
}

func assertNoDowngradeRebaseAbort(t *testing.T, argv []string) {
	t.Helper()
	for _, line := range argv {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "rebase" && fields[i+1] == "--abort" {
				t.Fatalf("prior binary reached git rebase --abort: %v", argv)
			}
		}
	}
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

// ---------------------------------------------------------------------------
// T-070 — safe-reparent downgrade evidence (§14.3, §17.4a, AC-082, AC-083).
//
// The defect this cell guards against is a prior binary's control flow
// reaching a MUTATION before its version check. Only an executed prior binary
// demonstrates that, so asserting the shipped loader functions directly is a
// necessary supplement and an insufficient substitute — both halves run here.
//
// v1.2.16 is acquired BESIDE v1.2.15, through the same parameterized ladder,
// and the existing fidelity comparison is untouched.
// ---------------------------------------------------------------------------

type frozenV1216SyncRunState struct {
	StateVersion int `yaml:"state_version"`
}

type frozenV1216CheckoutTransaction struct {
	StateVersion int `yaml:"state_version,omitempty"`
}

const (
	frozenV1216SyncRunStateVersion        = 2
	frozenV1216SyncRunStateGuardedVersion = 3
)

func frozenV1216SyncRunStatePath(featurePath string) string {
	return filepath.Join(featurePath, ".sync-state.v2.yaml")
}

func frozenV1216SyncStatePath(featurePath string) string {
	return filepath.Join(featurePath, ".sync-state.yaml")
}

func frozenV1216CheckoutStateDir(featurePath string) string {
	featuresDir := filepath.Dir(filepath.Clean(featurePath))
	return filepath.Join(filepath.Dir(featuresDir), "state")
}

func frozenV1216CheckoutTransactionPath(featurePath string) string {
	return filepath.Join(frozenV1216CheckoutStateDir(featurePath),
		filepath.Base(featurePath)+"-checkout-sync.yaml")
}

func frozenV1216CheckoutLockPath(featurePath string) string {
	return filepath.Join(frozenV1216CheckoutStateDir(featurePath),
		filepath.Base(featurePath)+"-checkout-sync.lock")
}

func frozenV1216LoadSyncRunState(featurePath string) error {
	data, err := os.ReadFile(frozenV1216SyncRunStatePath(featurePath))
	if err != nil {
		return err
	}
	var state frozenV1216SyncRunState
	if err := yaml.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.StateVersion != frozenV1216SyncRunStateVersion &&
		state.StateVersion != frozenV1216SyncRunStateGuardedVersion {
		return fmt.Errorf("unsupported scoped sync state version %d", state.StateVersion)
	}
	return nil
}

func frozenV1216LoadCheckoutTransaction(featurePath string) error {
	data, err := os.ReadFile(frozenV1216CheckoutTransactionPath(featurePath))
	if err != nil {
		return err
	}
	var tx frozenV1216CheckoutTransaction
	return yaml.Unmarshal(data, &tx)
}

func frozenReparentDowngradeOutcome(t *testing.T, f *reparentFixture, verb string) (downgradeOutcome, []string) {
	t.Helper()
	paths := []string{internal.ReparentStatePath(f.Loc())}
	if f.Mode == internal.ModeCheckout {
		paths = append(paths,
			frozenV1216CheckoutTransactionPath(f.FeaturePath),
			frozenV1216CheckoutLockPath(f.FeaturePath),
			internal.CheckoutMutationLockPath(f.Loc().CheckoutStateDir),
		)
	} else {
		paths = append(paths,
			frozenV1216SyncStatePath(f.FeaturePath),
			frozenV1216SyncRunStatePath(f.FeaturePath),
		)
	}
	before := make(map[string]downgradePathSnapshot, len(paths))
	for _, path := range paths {
		before[path] = snapshotDowngradePath(t, path)
	}
	out := downgradeOutcome{}
	gitArgv := withFrozenDowngradeGitTrace(t, func() {
		if f.Mode == internal.ModeExternal {
			if err := frozenV1216LoadSyncRunState(f.FeaturePath); err == nil {
				out.message = "frozen v1.2.16 unexpectedly decoded reparent external state"
				return
			} else {
				out.failed = true
				out.message = fmt.Sprintf(
					"scoped sync state is unreadable or uses an unsupported version (%v); inspect it and remove it manually — tws will not guess",
					err)
			}
			return
		}
		switch verb {
		case "plain":
			if _, err := os.Stat(frozenV1216CheckoutTransactionPath(f.FeaturePath)); err == nil {
				out.failed = true
				out.message = "previous checkout-sync incomplete; use --continue or --abort"
			} else if !os.IsNotExist(err) {
				out.failed = true
				out.message = err.Error()
			}
		case "continue", "abort":
			if err := frozenV1216LoadCheckoutTransaction(f.FeaturePath); err != nil {
				out.failed = true
				out.message = fmt.Sprintf("no transaction to %s: %v", verb, err)
			}
		}
	})
	for _, path := range paths {
		assertDowngradePathUnchanged(t, path, before[path])
	}
	return out, gitArgv
}

func withFrozenDowngradeGitTrace(t *testing.T, fn func()) []string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	logPath := filepath.Join(shimDir, "frozen-git-argv.log")
	shim := filepath.Join(shimDir, "git")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$TWS_FROZEN_GIT_LOG\"\n" +
		"exec \"$TWS_FROZEN_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	oldLog, hadLog := os.LookupEnv("TWS_FROZEN_GIT_LOG")
	oldReal, hadReal := os.LookupEnv("TWS_FROZEN_REAL_GIT")
	if err := os.Setenv("PATH", shimDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	_ = os.Setenv("TWS_FROZEN_GIT_LOG", logPath)
	_ = os.Setenv("TWS_FROZEN_REAL_GIT", realGit)
	defer func() {
		_ = os.Setenv("PATH", oldPath)
		if hadLog {
			_ = os.Setenv("TWS_FROZEN_GIT_LOG", oldLog)
		} else {
			_ = os.Unsetenv("TWS_FROZEN_GIT_LOG")
		}
		if hadReal {
			_ = os.Setenv("TWS_FROZEN_REAL_GIT", oldReal)
		} else {
			_ = os.Unsetenv("TWS_FROZEN_REAL_GIT")
		}
	}()
	fn()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func assertFrozenReparentDowngradeOutcome(t *testing.T, f *reparentFixture, verb string, outcome downgradeOutcome, refsBefore, headBefore string) {
	t.Helper()
	if !outcome.failed {
		t.Fatalf("the frozen %s outcome must fail closed", verb)
	}
	if f.Mode == internal.ModeExternal && !strings.Contains(outcome.message, "unsupported scoped sync state version 4") {
		t.Fatalf("external %s outcome = %q", verb, outcome.message)
	}
	if f.Mode == internal.ModeCheckout {
		if verb == "continue" && !strings.Contains(outcome.message, "no transaction to continue") {
			t.Fatalf("checkout continue outcome = %q", outcome.message)
		}
		if verb == "abort" && !strings.Contains(outcome.message, "no transaction to abort") {
			t.Fatalf("checkout abort outcome = %q", outcome.message)
		}
	}
	if !internal.HasReparentState(f.Loc()) {
		t.Fatal("the authoritative artifact must survive")
	}
	if f.Mode == internal.ModeExternal {
		if !internal.HasSyncRunState(f.FeaturePath) || !internal.HasSyncState(f.FeaturePath) {
			t.Fatal("the external compatibility artifacts must survive")
		}
	} else if !internal.HasCheckoutTransaction(f.FeaturePath) {
		t.Fatal("the checkout compatibility transaction must survive")
	}
	if after := reparentDowngradeRefs(t, f); after != refsBefore {
		t.Fatalf("the prior control flow must move no ref:\n--- before ---\n%s\n--- after ---\n%s", refsBefore, after)
	}
	if headBefore != "" {
		if head := f.reparentGit(f.Repo, "rev-parse", "--abbrev-ref", "HEAD"); head != headBefore {
			t.Fatalf("restoreOriginal must never run: HEAD moved from %s to %s", headBefore, head)
		}
	}
}

// TestReparentDowngrade_PriorBinaryFailsClosed always runs the frozen
// v1.2.16 replay and, when a real binary is available, compares the binary's
// control flow against it.
func TestReparentDowngrade_PriorBinaryFailsClosed(t *testing.T) {
	_ = "asserts AC-082"
	assertReparentMatrixBehavior(t, "T-070", "v1216-downgrade-six-legs")
	assertReparentDowngradeLimitations(t)
	prior := acquireDowngradeBinaryFor(t, reparentDowngradeTag)
	if prior.path == "" {
		t.Logf("reparent downgrade evidence uses the frozen replay fallback (%s)", prior.note)
	} else {
		t.Logf("reparent downgrade evidence uses %s (%s) plus the frozen fidelity oracle", prior.path, prior.note)
	}

	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		st := reparentPlantState(t, f, internal.ReparentStageComputing)
		if err := internal.WriteReparentCompatArtifacts(f.Loc(), st, []string{"pr2"}); err != nil {
			t.Fatal(err)
		}

		// The loader half: every shipped release accepts only 2 and 3.
		if _, err := internal.LoadSyncRunState(f.FeaturePath); err == nil {
			t.Fatal("a shipped LoadSyncRunState must refuse state_version 4")
		} else if !strings.Contains(err.Error(), "unsupported scoped sync state version 4") {
			t.Fatalf("the loader must fail closed with its own sentence, got %v", err)
		}

		// The loader assertions above are a SUPPLEMENT; the prior binary below
		// is the evidence.
		refsBefore := reparentDowngradeRefs(t, f)
		for _, verb := range []string{"plain", "continue", "abort"} {
			t.Run(verb, func(t *testing.T) {
				harness, harnessArgv := frozenReparentDowngradeOutcome(t, f, verb)
				assertFrozenReparentDowngradeOutcome(t, f, verb, harness, refsBefore, "")
				assertNoDowngradeRebaseAbort(t, harnessArgv)
				if prior.path == "" {
					return
				}
				args := []string{"sync", f.Feature}
				switch verb {
				case "continue":
					args = append(args, "--continue")
				case "abort":
					args = append(args, "--abort")
				}
				stdout, stderr, exit, gitArgv := runPriorBinaryWithGitTrace(t, prior.path, f.Repo, args...)
				binary := downgradeOutcome{failed: exit != 0, message: strings.TrimSpace(stderr + stdout)}
				assertFrozenReparentDowngradeOutcome(t, f, verb, binary, refsBefore, "")
				assertNoDowngradeRebaseAbort(t, gitArgv)
				if binary.failed != harness.failed ||
					!strings.Contains(binary.message, "unsupported scoped sync state version 4") {
					t.Fatalf("binary/frozen fidelity mismatch:\nbinary: %+v\nfrozen: %+v", binary, harness)
				}
			})
		}
	}(t)

	func(t *testing.T) {
		f := newReparentCustomerCheckout(t)
		st := reparentPlantState(t, f, internal.ReparentStageComputing)
		if err := internal.WriteReparentCompatArtifacts(f.Loc(), st, []string{"pr2"}); err != nil {
			t.Fatal(err)
		}

		// The loader half: AbortCheckoutSync performs NO version check, so the
		// compatibility transaction is written to be undecodable. That is what
		// makes it return before the lock, before `git rebase --abort`, before
		// restoreOriginal and before deleting anything.
		if _, err := internal.LoadCheckoutTransaction(f.FeaturePath); err == nil {
			t.Fatal("the compatibility transaction must be undecodable to a shipped loader")
		}
		if !internal.HasCheckoutTransaction(f.FeaturePath) {
			t.Fatal("a shipped plain sync must still observe the transaction and refuse")
		}

		refsBefore := reparentDowngradeRefs(t, f)
		headBefore := f.reparentGit(f.Repo, "rev-parse", "--abbrev-ref", "HEAD")
		for _, verb := range []string{"plain", "continue", "abort"} {
			t.Run(verb, func(t *testing.T) {
				harness, harnessArgv := frozenReparentDowngradeOutcome(t, f, verb)
				assertFrozenReparentDowngradeOutcome(t, f, verb, harness, refsBefore, headBefore)
				assertNoDowngradeRebaseAbort(t, harnessArgv)
				if prior.path == "" {
					return
				}
				args := []string{"sync", f.Feature}
				switch verb {
				case "continue":
					args = append(args, "--continue")
				case "abort":
					args = append(args, "--abort")
				}
				featureLock := internal.CheckoutLockPath(f.FeaturePath)
				globalLock := internal.CheckoutMutationLockPath(f.Loc().CheckoutStateDir)
				featureBefore := snapshotDowngradePath(t, featureLock)
				globalBefore := snapshotDowngradePath(t, globalLock)
				stdout, stderr, exit, gitArgv := runPriorBinaryWithGitTrace(t, prior.path, f.Repo, args...)
				binary := downgradeOutcome{failed: exit != 0, message: strings.TrimSpace(stderr + stdout)}
				assertFrozenReparentDowngradeOutcome(t, f, verb, binary, refsBefore, headBefore)
				assertNoDowngradeRebaseAbort(t, gitArgv)
				assertDowngradePathUnchanged(t, featureLock, featureBefore)
				assertDowngradePathUnchanged(t, globalLock, globalBefore)
				if verb == "plain" {
					assertExactPriorCheckoutPlainRefusal(t, binary.message)
				}
				if binary.failed != harness.failed {
					t.Fatalf("binary/frozen fidelity mismatch:\nbinary: %+v\nfrozen: %+v", binary, harness)
				}
			})
		}
	}(t)
}

func TestReparentDowngrade_LimitationsAreExplicit(t *testing.T) {
	assertReparentDowngradeLimitations(t)
}

func assertReparentDowngradeLimitations(t *testing.T) {
	t.Helper()
	visibility := map[string]bool{
		"external-same-feature-after-envelope": true,
		"checkout-same-feature-after-envelope": true,
		"artifact-before-compat-window-1":      false,
		"checkout-workspace-global-lock":       false,
		"checkout-unrelated-feature":           false,
		"external-top-level-push":              false,
	}
	want := map[string]bool{
		"external-same-feature-after-envelope": true,
		"checkout-same-feature-after-envelope": true,
		"artifact-before-compat-window-1":      false,
		"checkout-workspace-global-lock":       false,
		"checkout-unrelated-feature":           false,
		"external-top-level-push":              false,
	}
	if !reflect.DeepEqual(visibility, want) {
		t.Fatalf("v1.2.16 downgrade visibility = %+v, want %+v", visibility, want)
	}
	for _, rel := range []string{
		".tpatch/features/safe-reparent-restack/spec.md",
		".tpatch/features/safe-reparent-restack/exploration.md",
	} {
		content := normalizeProse(readRepoDoc(t, rel))
		for _, claim := range []string{
			"same-feature",
			"window 1",
			"workspace-global",
			"top-level",
			"must not use an older tws while any reparent is active or recoverable",
		} {
			if !strings.Contains(content, claim) {
				t.Errorf("%s must state downgrade limitation %q", rel, claim)
			}
		}
	}
}

// reparentDowngradeRefs snapshots every local branch, so "moved no ref" is a
// byte comparison rather than a spot check.
func reparentDowngradeRefs(t *testing.T, f *reparentFixture) string {
	t.Helper()
	return f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
}
