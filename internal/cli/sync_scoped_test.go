package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// Behavioural matrix for new-mode external runs. Every fixture is a real Git
// repository with a real bare remote and real linked worktrees.
// ---------------------------------------------------------------------------

type scopedFixture struct {
	repo        string
	remote      string
	feature     string
	featurePath string
	layout      externalSyncLayout
}

func newScopedFixture(t *testing.T) *scopedFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	createLinearStack(t, repo, "feature")
	featurePath := internal.FeaturePath("feature")
	return &scopedFixture{
		repo:        repo,
		remote:      filepath.Join(filepath.Dir(repo), "remote.git"),
		feature:     "feature",
		featurePath: featurePath,
		layout: externalSyncLayout{
			FeaturePath:   featurePath,
			WorktreesRoot: filepath.Join(featurePath, "worktrees"),
		},
	}
}

func (f *scopedFixture) wt(name string) string { return f.layout.WorktreePath(name) }

func (f *scopedFixture) sha(t *testing.T, name string) string {
	t.Helper()
	return gitOutput(t, f.wt(name), "rev-parse", "HEAD")
}

func (f *scopedFixture) advanceRoot(t *testing.T) string {
	t.Helper()
	writeAndCommit(t, f.wt("root"), "root-v2.txt", "root-v2\n", "root v2")
	return f.sha(t, "root")
}

func runSync(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	stdout, stderr = syncCaptureStreams(t, func() {
		exit = syncExecute(syncCmd, args...)
	})
	return stdout, stderr, exit
}

// detachGuard simulates the process that started the run having exited: the
// guard file survives a failure by design (§8.3 rule 6), so a --continue or
// --abort from a NEW process sees a stale guard. In-process tests reproduce
// that by rewriting the recorded PID to the repository's dead-PID convention.
func (f *scopedFixture) detachGuard(t *testing.T) {
	t.Helper()
	guard, err := internal.ReadSyncRunGuard(f.featurePath)
	if err != nil {
		t.Fatalf("guard must exist after a failure: %v", err)
	}
	body := fmt.Sprintf("pid: 999999\ncreated: %q\ntoken: %q\nstate_version: 2\n", guard.Created, guard.Token)
	if err := os.WriteFile(internal.SyncRunGuardPath(f.featurePath), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *scopedFixture) stateFilesGone(t *testing.T) {
	t.Helper()
	for _, path := range []string{
		internal.SyncStatePath(f.featurePath),
		internal.SyncRunStatePath(f.featurePath),
		internal.SyncRunGuardPath(f.featurePath),
	} {
		if _, err := os.Lstat(path); err == nil {
			t.Fatalf("teardown must remove %s", path)
		}
	}
}

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

func TestSyncScoped_OnlySelectsOneEntry(t *testing.T) {
	f := newScopedFixture(t)
	rootSHA := f.advanceRoot(t)
	childBefore := f.sha(t, "child")
	stackBefore := readFileString(t, filepath.Join(f.featurePath, "stack.yaml"))

	stdout, stderr, exit := runSync(t, f.feature, "--only", "parent")
	if exit != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "Sync mode: fetch=fetch propagation=full scope=only:parent") {
		t.Fatalf("missing header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "[+] parent (active)") {
		t.Fatalf("parent must be synced:\n%s", stdout)
	}
	if strings.Contains(stdout, "] child (") {
		t.Fatalf("child is outside the scope and must not be touched:\n%s", stdout)
	}
	if internal.RunSilentDir(f.wt("parent"), "git", "merge-base", "--is-ancestor", rootSHA, "parent") != nil {
		t.Fatal("parent must contain the advanced root")
	}
	if got := f.sha(t, "child"); got != childBefore {
		t.Fatalf("an unselected entry moved: %s -> %s", childBefore, got)
	}
	stackAfter := readFileString(t, filepath.Join(f.featurePath, "stack.yaml"))
	assertUnselectedStackEntriesUnchanged(t, stackBefore, stackAfter, "parent")
	f.stateFilesGone(t)
}

func TestSyncScoped_FromIncludesTheNamedEntry(t *testing.T) {
	f := newScopedFixture(t)
	rootSHA := f.advanceRoot(t)

	stdout, stderr, exit := runSync(t, f.feature, "--from", "parent")
	if exit != 0 {
		t.Fatalf("exit = %d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "scope=subtree:parent") {
		t.Fatalf("missing subtree header:\n%s", stdout)
	}
	for _, name := range []string{"parent", "child"} {
		if !strings.Contains(stdout, "[+] "+name+" (active)") {
			t.Fatalf("%s must be in the subtree:\n%s", name, stdout)
		}
	}
	if internal.RunSilentDir(f.wt("child"), "git", "merge-base", "--is-ancestor", rootSHA, "child") != nil {
		t.Fatal("child must contain the advanced root through parent")
	}
	f.stateFilesGone(t)
}

func TestSyncScoped_UnknownAndArchivedSelectorsRefuse(t *testing.T) {
	f := newScopedFixture(t)
	_, stderr, exit := runSync(t, f.feature, "--only", "nope")
	if exit == 0 {
		t.Fatal("I10 must refuse")
	}
	want := `unknown stack entry "nope" in feature "feature"; run: tws stack status feature`
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
	// Nothing was written.
	f.stateFilesGone(t)
}

func TestSyncScoped_ScopedRunDropsUpdateRefs(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	before := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)")

	if _, _, exit := runSync(t, f.feature, "--only", "parent"); exit != 0 {
		t.Fatal("scoped run must succeed")
	}
	after := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)")

	changed := diffRefLines(before, after)
	for _, line := range changed {
		if !strings.Contains(line, "refs/heads/parent") {
			t.Fatalf("a scoped run moved an unrelated ref: %q\nbefore:\n%s\nafter:\n%s", line, before, after)
		}
	}
	if len(changed) == 0 {
		t.Fatal("the selected branch must have moved")
	}
}

// ---------------------------------------------------------------------------
// Propagation
// ---------------------------------------------------------------------------

func TestSyncScoped_LocalOnlyAnchorIsANoOpSuccess(t *testing.T) {
	f := newScopedFixture(t)
	rootBefore := f.sha(t, "root")

	stdout, stderr, exit := runSync(t, f.feature, "--only", "root", "--local-only")
	if exit != 0 {
		t.Fatalf("a no-op selection is a success, not an error: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "  [-] root (no in-stack parent edge to propagate)") {
		t.Fatalf("missing the no-op line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Nothing to propagate.") {
		t.Fatalf("missing the trailing no-op line:\n%s", stdout)
	}
	if got := f.sha(t, "root"); got != rootBefore {
		t.Fatal("local-only must never advance an anchor")
	}
	f.stateFilesGone(t)
}

func TestSyncScoped_LocalOnlyPropagatesWithoutAdvancingTheAnchor(t *testing.T) {
	f := newScopedFixture(t)
	// Move the remote default forward; local-only must ignore it.
	writeAndCommit(t, f.repo, "upstream.txt", "upstream\n", "upstream")
	gitRun(t, f.repo, "push", "origin", "master")
	gitRun(t, f.repo, "fetch", "origin")
	rootBefore := f.sha(t, "root")
	parentTip := f.sha(t, "parent")

	stdout, stderr, exit := runSync(t, f.feature, "--from", "root", "--local-only", "--no-fetch")
	if exit != 0 {
		t.Fatalf("exit = %d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "  [-] root (no in-stack parent edge to propagate)") {
		t.Fatalf("root is an anchor and must be skipped:\n%s", stdout)
	}
	if strings.Contains(stdout, "Nothing to propagate.") {
		t.Fatalf("propagation edges existed, so the trailing line must not print:\n%s", stdout)
	}
	if got := f.sha(t, "root"); got != rootBefore {
		t.Fatal("local-only must not advance the root from origin/master")
	}
	if internal.RunSilentDir(f.wt("child"), "git", "merge-base", "--is-ancestor", parentTip, "child") != nil {
		t.Fatal("child must contain the local parent tip")
	}
}

// ---------------------------------------------------------------------------
// Fetch policy
// ---------------------------------------------------------------------------

func TestSyncScoped_NoFetchIssuesNoAutomaticNetworkInput(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	if err := os.RemoveAll(f.remote); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runSync(t, f.feature, "--only", "parent", "--no-fetch")
	if exit != 0 {
		t.Fatalf("no-fetch must not need the remote: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if strings.Contains(stdout, "Fetching") {
		t.Fatalf("no-fetch must not fetch:\n%s", stdout)
	}
	if !strings.Contains(stdout, "fetch=no-fetch") {
		t.Fatalf("header must record the frozen fetch policy:\n%s", stdout)
	}
}

func TestSyncScoped_NoFetchUnresolvableBaseIsAPreflightFatal(t *testing.T) {
	f := newScopedFixture(t)
	// Point the child at a base that does not exist locally.
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range stack.Branches {
		if stack.Branches[i].Name == "child" {
			stack.Branches[i].Base = "does-not-exist"
		}
	}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}

	_, stderr, exit := runSync(t, f.feature, "--only", "child", "--no-fetch")
	if exit == 0 {
		t.Fatal("I14 must refuse before any mutation")
	}
	want := `base "does-not-exist" for stack entry "child" does not resolve locally; drop --no-fetch or fetch manually first`
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
	f.stateFilesGone(t)
}

func TestSyncScoped_FetchIsRestrictedToSelectedRepos(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	stdout, _, exit := runSync(t, f.feature, "--only", "parent", "--fetch")
	if exit != 0 {
		t.Fatal("scoped fetch run must succeed")
	}
	if strings.Count(stdout, "Fetching ") != 1 {
		t.Fatalf("fetch is once per unique selected repo:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

func (f *scopedFixture) makeConflict(t *testing.T) {
	t.Helper()
	writeAndCommit(t, f.wt("parent"), "conflict.txt", "from-parent\n", "parent change")
	writeAndCommit(t, f.wt("child"), "conflict.txt", "from-child\n", "child change")
}

func TestSyncScoped_ConflictWritesSentinelAndPayload(t *testing.T) {
	f := newScopedFixture(t)
	f.makeConflict(t)

	_, _, exit := runSync(t, f.feature, "--only", "child")
	if exit == 0 {
		t.Fatal("the conflicting child must stop the run")
	}

	legacy, err := internal.LoadSyncState(f.featurePath)
	if err != nil {
		t.Fatalf("the sentinel must survive a failure: %v", err)
	}
	if legacy.FailedBranch == "child" {
		t.Fatal("the sentinel must never be overwritten with a resolvable name")
	}
	if !strings.HasPrefix(legacy.FailedBranch, "tws-scoped-sync-") || !strings.HasSuffix(legacy.FailedBranch, ".lock") {
		t.Fatalf("sentinel failed_branch = %q, want a marker", legacy.FailedBranch)
	}
	payload, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatalf("the payload must record the failure: %v", err)
	}
	if payload.FailedBranch != "child" {
		t.Fatalf("payload failed_branch = %q, want the REAL name", payload.FailedBranch)
	}
	if payload.Stage != internal.SyncStageFailed {
		t.Fatalf("stage = %q, want failed", payload.Stage)
	}
	if payload.ScopeKind != internal.SyncScopeOne || payload.ScopeSelector != "child" {
		t.Fatalf("payload lost the frozen scope: %+v", payload)
	}
	if _, err := os.Stat(internal.SyncRunGuardPath(f.featurePath)); err != nil {
		t.Fatal("the guard must survive until teardown")
	}
}

func TestSyncScoped_PlainAndContinueAndAbortInCell5(t *testing.T) {
	f := newScopedFixture(t)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child"); exit == 0 {
		t.Fatal("expected a conflict")
	}
	f.detachGuard(t)

	// plain in cell 5 refuses and names the real entry.
	_, stderr, exit := runSync(t, f.feature)
	if exit == 0 || !strings.Contains(stderr, "a scoped sync is incomplete (failed on: child); use --continue or --abort") {
		t.Fatalf("cell-5 plain: exit=%d stderr=%q", exit, stderr)
	}

	// Resolve the conflict the way an operator would.
	resolveRebase(t, f.wt("child"))

	stdout, stderr, exit := runSync(t, f.feature, "--continue")
	if exit != 0 {
		t.Fatalf("cell-5 continue must resume: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "Sync mode: fetch=") {
		t.Fatalf("a v2 resume prints the header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Resuming sync with ") {
		t.Fatalf("missing the resume line:\n%s", stdout)
	}
	f.stateFilesGone(t)
}

func TestSyncScoped_AbortTearsDownInReverseOrder(t *testing.T) {
	f := newScopedFixture(t)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child"); exit == 0 {
		t.Fatal("expected a conflict")
	}
	f.detachGuard(t)

	stdout, stderr, exit := runSync(t, f.feature, "--abort")
	if exit != 0 {
		t.Fatalf("cell-5 abort must succeed: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "Sync state cleared.") {
		t.Fatalf("abort must print today's string:\n%s", stdout)
	}
	f.stateFilesGone(t)
}

func TestSyncScoped_I20RefusesTriggerFlagsOnLegacyContinue(t *testing.T) {
	f := newScopedFixture(t)

	// Cell 1 — no state at all.
	_, stderr, exit := runSync(t, f.feature, "--continue", "--local-only")
	if exit == 0 || !strings.Contains(stderr, errSyncModeFlagsNeedV2) {
		t.Fatalf("cell-1 I20: exit=%d stderr=%q", exit, stderr)
	}

	// Cell 7 — real legacy state.
	state := internal.NewSyncState()
	state.FailedBranch = "parent"
	if err := internal.SaveSyncState(f.featurePath, state); err != nil {
		t.Fatal(err)
	}
	_, stderr, exit = runSync(t, f.feature, "--continue", "--only", "child")
	if exit == 0 || !strings.Contains(stderr, errSyncModeFlagsNeedV2) {
		t.Fatalf("cell-7 I20: exit=%d stderr=%q", exit, stderr)
	}
	// A plain --continue against the same state is untouched by I20.
	_, stderr, exit = runSync(t, f.feature, "--continue")
	if strings.Contains(stderr, errSyncModeFlagsNeedV2) {
		t.Fatalf("a plain --continue is a no-flag invocation: %q", stderr)
	}
	_ = exit
}

func TestSyncScoped_ContinueMismatchIsRefused(t *testing.T) {
	f := newScopedFixture(t)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child", "--no-fetch"); exit == 0 {
		t.Fatal("expected a conflict")
	}
	f.detachGuard(t)
	resolveRebase(t, f.wt("child"))

	_, stderr, exit := runSync(t, f.feature, "--continue", "--fetch")
	if exit == 0 {
		t.Fatal("a conflicting axis on --continue must be refused")
	}
	want := "cannot change fetch on --continue: the run was started with fetch=no-fetch and this invocation requests fetch"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}

	// The matching value is accepted (idempotent, script-friendly).
	if _, stderr, exit := runSync(t, f.feature, "--continue", "--no-fetch"); exit != 0 {
		t.Fatalf("a matching axis must be accepted: %q", stderr)
	}
	f.stateFilesGone(t)
}

func TestSyncScoped_DeferredI7OnlyAgainstNewModeState(t *testing.T) {
	f := newScopedFixture(t)

	// Cell 1: today's precedence — abort wins, continue ignored.
	stdout, _, exit := runSync(t, f.feature, "--continue", "--abort")
	if exit != 0 {
		t.Fatal("without new-mode state --abort wins and --continue is ignored")
	}
	if !strings.Contains(stdout, "Nothing to abort — no sync in progress.") {
		t.Fatalf("frozen abort string missing:\n%s", stdout)
	}

	// Cell 5: deferred I7 fires.
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child"); exit == 0 {
		t.Fatal("expected a conflict")
	}
	_, stderr, exit := runSync(t, f.feature, "--continue", "--abort")
	if exit == 0 || !strings.Contains(stderr, "--continue and --abort are mutually exclusive") {
		t.Fatalf("deferred I7: exit=%d stderr=%q", exit, stderr)
	}
}

func TestSyncScoped_GuardRefusesASecondRun(t *testing.T) {
	f := newScopedFixture(t)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child"); exit == 0 {
		t.Fatal("expected a conflict")
	}
	// Rewrite the guard so it looks alive and owns the payload.
	payload, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("pid: %d\ncreated: \"2026-01-01T00:00:00Z\"\ntoken: \"%s\"\nstate_version: 2\n", os.Getpid(), payload.OwnerToken)
	if err := os.WriteFile(internal.SyncRunGuardPath(f.featurePath), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, exit := runSync(t, f.feature, "--local-only")
	if exit == 0 || !strings.Contains(stderr, "a scoped sync is already running for \"feature\"") {
		t.Fatalf("a live owning guard must refuse: exit=%d stderr=%q", exit, stderr)
	}
	_, stderr, exit = runSync(t, f.feature, "--abort")
	if exit == 0 || !strings.Contains(stderr, "wait for it to exit before --abort") {
		t.Fatalf("a live owning guard must refuse --abort too: exit=%d stderr=%q", exit, stderr)
	}
	if !internal.HasSyncRunState(f.featurePath) {
		t.Fatal("a refused --abort must mutate nothing")
	}
}

func TestSyncScoped_UnreadableGuardDoesNotBlockANoFlagRun(t *testing.T) {
	f := newScopedFixture(t)
	if err := os.WriteFile(internal.SyncRunGuardPath(f.featurePath), []byte("::: not yaml :::\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runSync(t, f.feature)
	if exit != 0 {
		t.Fatalf("a no-flag run never consults the guard: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "Sync complete.") {
		t.Fatalf("missing the frozen terminal line:\n%s", stdout)
	}
	if _, err := os.Stat(internal.SyncRunGuardPath(f.featurePath)); err != nil {
		t.Fatal("a no-flag run must leave the guard file alone")
	}
}

// ---------------------------------------------------------------------------
// Downgrade (§9): the frozen v1.2.14 replay harness
// ---------------------------------------------------------------------------

// legacyPlainSync transcribes v1.2.14's plain-sync state check verbatim.
func legacyPlainSync(featurePath string) error {
	if internal.HasSyncState(featurePath) {
		state, _ := internal.LoadSyncState(featurePath)
		return fmt.Errorf("previous sync incomplete (failed on: %s); use --continue or --abort", state.FailedBranch)
	}
	return nil
}

// legacyContinue transcribes v1.2.14's --continue pre-checks verbatim.
func legacyContinue(feature, featurePath string) error {
	state, err := internal.LoadSyncState(featurePath)
	if err != nil {
		return fmt.Errorf("nothing to continue — no sync in progress")
	}
	failedPath := internal.WorktreePath(feature, state.FailedBranch)
	if state.FailedBranch != "" && isRebaseInProgress(failedPath) {
		return fmt.Errorf("rebase still in progress in %s; resolve conflicts, run git add . && git rebase --continue, then retry", state.FailedBranch)
	}
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return fmt.Errorf("load stack: %w", err)
	}
	if state.FailedBranch != "" {
		if internal.GetBranch(stack, state.FailedBranch).Name == "" {
			return fmt.Errorf("failed branch %q no longer exists in stack", state.FailedBranch)
		}
	}
	return nil
}

// legacyAbort transcribes v1.2.14's --abort verbatim.
func legacyAbort(feature, featurePath string) string {
	state, err := internal.LoadSyncState(featurePath)
	if err != nil {
		return "Nothing to abort — no sync in progress."
	}
	if state.FailedBranch != "" {
		path := internal.WorktreePath(feature, state.FailedBranch)
		if isRebaseInProgress(path) {
			_ = internal.RunSilentDir(path, "git", "rebase", "--abort")
		}
	}
	internal.DeleteSyncState(featurePath)
	return "Sync state cleared."
}

func TestSyncScoped_DowngradeAgainstTheSentinel(t *testing.T) {
	f := newScopedFixture(t)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child"); exit == 0 {
		t.Fatal("expected a conflict")
	}
	f.detachGuard(t)
	sentinel, err := internal.LoadSyncState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	marker := sentinel.FailedBranch

	// An old plain sync fails closed before any Git mutation.
	err = legacyPlainSync(f.featurePath)
	want := fmt.Sprintf("previous sync incomplete (failed on: %s); use --continue or --abort", marker)
	if err == nil || err.Error() != want {
		t.Fatalf("old plain sync: got %v, want %q", err, want)
	}

	// An old --continue fails closed: the marker resolves to no stack entry.
	err = legacyContinue(f.feature, f.featurePath)
	want = fmt.Sprintf("failed branch %q no longer exists in stack", marker)
	if err == nil || err.Error() != want {
		t.Fatalf("old continue: got %v, want %q — a broad resume would be the failure this mechanism prevents", err, want)
	}

	// An old --abort deletes only the sentinel; the payload and the real
	// rebase survive. That is cell 2.
	if msg := legacyAbort(f.feature, f.featurePath); msg != "Sync state cleared." {
		t.Fatalf("old abort printed %q", msg)
	}
	if internal.HasSyncState(f.featurePath) {
		t.Fatal("old abort removes the sentinel")
	}
	if !internal.HasSyncRunState(f.featurePath) {
		t.Fatal("old abort cannot remove a payload format that postdates it")
	}

	// The new binary recognises the residue and names the REAL failed entry.
	_, stderr, exit := runSync(t, f.feature)
	if exit == 0 {
		t.Fatal("cell 2 must refuse")
	}
	if !strings.Contains(stderr, "a scoped sync record survives without its state file for \"feature\": it failed on child") {
		t.Fatalf("cell-2 message missing: %q", stderr)
	}
	if strings.Contains(stderr, marker) {
		t.Fatal("the marker must never be surfaced")
	}

	// The new --abort performs the §9.2 recovery.
	stdout, stderr, exit := runSync(t, f.feature, "--abort")
	if exit != 0 {
		t.Fatalf("cell-2 abort must recover: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "Sync state cleared.") {
		t.Fatalf("missing the abort string:\n%s", stdout)
	}
	f.stateFilesGone(t)
}

// ---------------------------------------------------------------------------
// Push
// ---------------------------------------------------------------------------

func TestSyncScoped_SelectedPushUsesGitBranch(t *testing.T) {
	f := newScopedFixture(t)
	// Decouple the child's Git branch from its logical name.
	gitRun(t, f.wt("child"), "branch", "-m", "child", "user/child")
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range stack.Branches {
		if stack.Branches[i].Name == "child" {
			stack.Branches[i].Branch = "user/child"
		}
	}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}
	f.advanceRoot(t)

	stdout, stderr, exit := runSync(t, f.feature, "--from", "parent", "--push")
	if exit != 0 {
		t.Fatalf("exit = %d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "[+] child (pushed)") {
		t.Fatalf("the selected child must be pushed:\n%s\n%s", stdout, stderr)
	}
	remoteRefs := gitOutput(t, f.remote, "for-each-ref", "--format=%(refname)")
	if !strings.Contains(remoteRefs, "refs/heads/user/child") {
		t.Fatalf("the pushed ref must be the Git branch, not the logical name:\n%s", remoteRefs)
	}
	if strings.Contains(remoteRefs, "refs/heads/child\n") {
		t.Fatalf("the logical name must never be pushed as a ref:\n%s", remoteRefs)
	}
	// root is outside the scope and must not be pushed.
	if strings.Contains(remoteRefs, "refs/heads/root") {
		t.Fatalf("a scoped push must not push unselected entries:\n%s", remoteRefs)
	}
}

func TestSyncScoped_StaleEdgesOutsideScopeAreInformational(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)

	stdout, _, exit := runSync(t, f.feature, "--only", "parent")
	if exit != 0 {
		t.Fatal("an out-of-scope stale edge must not change the exit code")
	}
	if !strings.Contains(stdout, "Stale stack edges outside this scope (unchanged by this run):") {
		t.Fatalf("missing the informational block:\n%s", stdout)
	}
	if !strings.Contains(stdout, "  [i] child does not contain parent parent") {
		t.Fatalf("informational payload must reuse the existing formatter:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func resolveRebase(t *testing.T, worktree string) {
	t.Helper()
	if !isRebaseInProgress(worktree) {
		return
	}
	if err := os.WriteFile(filepath.Join(worktree, "conflict.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, worktree, "add", "conflict.txt")
	cmd := internal.RunSilentDir(worktree, "git", "-c", "core.editor=true", "rebase", "--continue")
	if cmd != nil {
		t.Fatalf("could not finish the rebase in %s: %v", worktree, cmd)
	}
}

func diffRefLines(before, after string) []string {
	old := make(map[string]bool)
	for _, line := range strings.Split(before, "\n") {
		if strings.TrimSpace(line) != "" {
			old[line] = true
		}
	}
	var changed []string
	for _, line := range strings.Split(after, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !old[line] {
			changed = append(changed, line)
		}
	}
	return changed
}

// assertUnselectedStackEntriesUnchanged compares the serialized per-entry
// blocks of stack.yaml and requires every block that does not belong to a
// selected entry to be byte-identical.
func assertUnselectedStackEntriesUnchanged(t *testing.T, before, after string, selected ...string) {
	t.Helper()
	sel := make(map[string]bool, len(selected))
	for _, name := range selected {
		sel[name] = true
	}
	beforeBlocks := stackEntryBlocks(before)
	afterBlocks := stackEntryBlocks(after)
	for name, block := range beforeBlocks {
		if sel[name] {
			continue
		}
		if afterBlocks[name] != block {
			t.Fatalf("unselected entry %q changed:\n--- before ---\n%s\n--- after ---\n%s", name, block, afterBlocks[name])
		}
	}
}

func stackEntryBlocks(doc string) map[string]string {
	blocks := make(map[string]string)
	var name string
	var buf []string
	flush := func() {
		if name != "" {
			blocks[name] = strings.Join(buf, "\n")
		}
		name, buf = "", nil
	}
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name:") {
			flush()
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:"))
		}
		if name != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return blocks
}
