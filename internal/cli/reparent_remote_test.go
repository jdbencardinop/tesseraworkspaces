package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// Acceptance criteria carried by this file's cells: AC-086 (zero provider
// calls), AC-087 and AC-093 (the guidance content, its order, and `origin`
// everywhere), AC-088 (the record's write point), AC-089 (all three push paths
// with and without a record), AC-090 (the invocation-wide lease preflight),
// AC-091 (the dry run), AC-092 (record clearing and who may persist it).

// ---------------------------------------------------------------------------
// T-074 — the provider-call audit and published guidance.
// T-076 — the three live push paths.
// T-077 — the invocation-wide lease preflight.
// T-078 — the dry-run preview.
// T-079 — clearing without provider access.
// T-080 — the no-record byte-identity half.
//
// tws makes ZERO provider calls, never pushes implicitly, and never touches
// upstream configuration. What it does is leave a local record saying "these
// branches were rewritten and their pull requests still point at the old
// base", so the next push warns and strengthens its lease.
//
// Rule R-PUSH's most important property is its FIRST clause: with no record,
// or for an entry the record does not list, behaviour is byte-identical to
// today. Every cell here asserts that half as carefully as it asserts the
// active half.
// ---------------------------------------------------------------------------

// TestReparentRemote_NoRecordIsByteIdentical is rule 1 and AC-089/AC-099. It
// captures a full `tws push` twice — once with no record at all, once with a
// record that lists NO entry of this invocation — and requires identical
// bytes.
func TestReparentRemote_NoRecordIsByteIdentical(t *testing.T) {
	f := newReparentCustomerExternal(t)

	baseline, baselineErr := syncCaptureStreams(t, func() {
		if code := syncExecute(pushCmd, f.Feature); code != 0 {
			t.Errorf("exit = %d", code)
		}
	})

	// A record that lists an entry this feature does not have must change
	// nothing either: rule 1 is keyed per ENTRY, not per feature.
	reparentPlantRemoteRecord(t, f, "not-an-entry", "feat-absent", strings.Repeat("d", 40), strings.Repeat("c", 40))

	after, afterErr := syncCaptureStreams(t, func() {
		if code := syncExecute(pushCmd, f.Feature); code != 0 {
			t.Errorf("exit = %d", code)
		}
	})
	if baseline != after {
		t.Fatalf("an unlisted entry must keep today's stdout byte-for-byte:\n--- before ---\n%s\n--- after ---\n%s", baseline, after)
	}
	if strings.Contains(afterErr, "reparent-remote:") != strings.Contains(baselineErr, "reparent-remote:") {
		t.Fatalf("an unlisted entry must produce no reparent-remote line:\n%s", afterErr)
	}

}

// TestReparentRemote_PushWarnsAndStrengthensTheLease is rule R-PUSH steps 3
// and 4 on the external `tws push` path: one anchored warning on stderr, and
// the BARE lease plus --force-if-includes in the argv — never an explicit
// --force-with-lease=<ref>:<sha>.
func TestReparentRemote_PushWarnsAndStrengthensTheLease(t *testing.T) {
	_ = "asserts AC-089"
	assertReparentMatrixBehavior(t, "T-077", "push-warning-and-strengthened-lease")
	f := newReparentCustomerExternal(t)
	// Publish the branch so the entry is a PUBLISHED pending row: only those
	// reach the preflight, and only those need the strengthened lease.
	f.reparentGit(f.Repo, "push", "origin", "feat-pr2")
	f.reparentGit(f.Repo, "fetch", "origin")
	remote := f.SHA("refs/remotes/origin/feat-pr2")
	f.reparentGit(internal.WorktreePath(f.Feature, "pr2"), "commit", "--allow-empty", "-m", "local rewrite after publish")
	reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", f.SHA("feat-pr2"), remote)

	stdout, stderr, log := reparentPushCapture(t, func() {
		if code := syncExecute(pushCmd, f.Feature); code != 0 {
			t.Errorf("push exit = %d", code)
		}
	})

	want := "reparent-remote: pr2 was reparented (feat-pr1 -> master); retarget the pull request before publishing"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr must carry the anchored warning exactly once:\n%s", stderr)
	}
	if strings.Count(stderr, "reparent-remote: pr2 was reparented") != 1 {
		t.Fatalf("the warning must be printed exactly once:\n%s", stderr)
	}
	if strings.Contains(stdout, "reparent-remote:") {
		t.Fatalf("the warning belongs on stderr, never stdout:\n%s", stdout)
	}
	if !log.sawPair("--force-with-lease", "--force-if-includes") {
		t.Fatalf("a pending published entry must push with the bare lease plus --force-if-includes, saw:\n%s", strings.Join(log.lines(), "\n"))
	}
	for _, line := range log.lines() {
		if strings.Contains(line, "--force-with-lease=") {
			t.Fatalf("an explicit lease must never be used: %s", line)
		}
	}
	// The unlisted sibling keeps today's exact argv.
	sawBare := false
	for _, argv := range log.argv {
		if argvHas(argv, "feat-pr1") && !argvHas(argv, "--force-if-includes") {
			sawBare = true
		}
	}
	if !sawBare {
		t.Fatalf("an unlisted entry must keep the bare lease argv, saw:\n%s", strings.Join(log.lines(), "\n"))
	}
	testReparentTopLevelPushMutationBarrier(t)
}

func testReparentTopLevelPushMutationBarrier(t *testing.T) {
	t.Helper()
	// The top-level invocation holds the shared feature mutation lock across
	// preflight and every entry. A reparent attempting to enter between the
	// first and second pushes loses before it can write state, and the push
	// completes the whole already-preflighted set.
	race := newReparentCustomerExternal(t)
	in, plan, req := reparentApprovedBeginInput(t, race, "pr2", "master")
	barrierCalls := 0
	TopLevelPushEntryBarrier = func(index int, entry internal.StackEntry) error {
		if index != 1 {
			return nil
		}
		barrierCalls++
		_, beginErr := internal.BeginReparentRun(internal.ReparentBeginInput{Input: in, Plan: plan, Request: req})
		var refusal *internal.ReparentRefusalError
		if !errors.As(beginErr, &refusal) || refusal.Kind != internal.ReparentRefusalSyncStatePresent {
			t.Fatalf("reparent entered between push entries: %v", beginErr)
		}
		return nil
	}
	t.Cleanup(func() { TopLevelPushEntryBarrier = nil })
	_, stderr, log := reparentPushCapture(t, func() {
		if code := syncExecute(pushCmd, race.Feature); code != 0 {
			t.Errorf("serialized push exit = %d", code)
		}
	})
	TopLevelPushEntryBarrier = nil
	if barrierCalls != 1 {
		t.Fatalf("multi-entry barrier calls = %d, want one", barrierCalls)
	}
	pushes := 0
	for _, argv := range log.argv {
		if argvVerb(argv) == "push" {
			pushes++
		}
	}
	if pushes != 2 {
		t.Fatalf("serialized multi-entry push ran %d pushes, want both entries:\n%s", pushes, strings.Join(log.lines(), "\n"))
	}
	if strings.Contains(stderr, "feature mutation lock") || strings.Contains(stderr, "reparent:") {
		t.Fatalf("the no-record lock added operator-visible diagnostics: %q", stderr)
	}
	if internal.HasReparentState(race.Loc()) {
		t.Fatal("the losing reparent wrote authoritative state")
	}
	if _, err := os.Stat(internal.SyncRunGuardPath(race.FeaturePath)); !os.IsNotExist(err) {
		t.Fatalf("serialized push left its mutation lock: %v", err)
	}
}

// TestReparentRemote_PreflightRefusesBeforeAnyPush is rule R-PUSH step 2. The
// gate is INVOCATION-WIDE: pushing half a stack and only then discovering the
// lease cannot be strengthened is exactly the outcome it prevents.
func TestReparentRemote_PreflightRefusesBeforeAnyPush(t *testing.T) {
	_ = "asserts AC-090 AC-092"
	assertReparentMatrixBehavior(t, "T-078", "preflight-before-any-push")
	f := newReparentCustomerExternal(t)
	// A published entry whose remote-tracking ref does NOT resolve: the row
	// records a non-empty remote_sha_at_write, but nothing was ever fetched.
	reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", f.SHA("feat-pr2"), "0123456789abcdef0123456789abcdef01234567")

	_, stderr, log := reparentPushCapture(t, func() {
		if code := syncExecute(pushCmd, f.Feature); code != 1 {
			t.Errorf("a failed preflight must exit 1, got %d", code)
		}
	})
	// AC-094/§13.2: the refusal is ONE anchored line, marker first.
	if !reparentAnchoredRefusalRe(internal.ReparentRefusalRemoteFollowupUnsafeLease).MatchString(stderr) {
		t.Fatalf("stderr must carry the anchored `reparent: <kind>: <detail>` line:\n%s", stderr)
	}
	for _, want := range []string{"pr2", "2.30", "git fetch origin", "--force-if-includes"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("the refusal must name %q:\n%s", want, stderr)
		}
	}
	for _, argv := range log.argv {
		if argvHas(argv, "push") {
			t.Fatalf("the gate is invocation-wide: nothing may be pushed, saw %v", argv)
		}
	}

	reparentPlantState(t, f, internal.ReparentStageRefsCommitted)
	stack := f.Stack()
	sel := internal.SyncSelection{
		Entries: []internal.SyncSelectedEntry{{Name: "pr2", GitBranch: "feat-pr2"}},
		Names:   map[string]bool{"pr2": true},
	}
	payload := &internal.SyncRunState{Feature: f.Feature, Pushed: []string{}}
	err := pushScoped(f.Feature, externalSyncLayout{
		FeaturePath: f.FeaturePath, WorktreesRoot: filepath.Join(f.FeaturePath, "worktrees"),
	}, stack, sel, []string{"pr2"}, payload)
	if err == nil || !strings.Contains(err.Error(), "reparent-state-present") {
		t.Fatalf("scoped sync push must refuse while commit point is unproven: %v", err)
	}

	// Clearing precedes capability/lease preflight. An archived row no longer
	// needs protection, so its deliberately unsafe lease facts cannot refuse
	// the invocation.
	cleared := newReparentCustomerExternal(t)
	reparentPlantRemoteRecord(t, cleared, "pr2", "feat-pr2", cleared.SHA("feat-pr2"),
		"0123456789abcdef0123456789abcdef01234567")
	clearedStack := cleared.Stack()
	for i := range clearedStack.Branches {
		if clearedStack.Branches[i].Name == "pr2" {
			clearedStack.Branches[i].Archived = true
		}
	}
	if err := internal.SaveStack(cleared.FeaturePath, clearedStack); err != nil {
		t.Fatal(err)
	}
	cleared.reparentGit(cleared.Repo, "worktree", "remove", "--force", internal.WorktreePath(cleared.Feature, "pr2"))
	_, clearErr, clearLog := reparentPushCapture(t, func() {
		if code := syncExecute(pushCmd, cleared.Feature); code != 0 {
			t.Errorf("cleared preflight push exit = %d", code)
		}
	})
	if strings.Contains(clearErr, string(internal.ReparentRefusalRemoteFollowupUnsafeLease)) {
		t.Fatalf("archived row reached lease preflight:\n%s", clearErr)
	}
	if _, err := os.Stat(internal.ReparentRemoteRecordPath(cleared.Loc())); !os.IsNotExist(err) {
		t.Fatalf("archived record was not cleared before preflight: %v", err)
	}
	for _, argv := range clearLog.argv {
		if argvHas(argv, "feat-pr2") {
			t.Fatalf("archived row was pushed: %v", argv)
		}
	}

	scoped := newReparentCustomerExternal(t)
	reparentPlantRemoteRecord(t, scoped, "pr2", "feat-pr2", scoped.SHA("feat-pr2"),
		"0123456789abcdef0123456789abcdef01234567")
	scopedStack := scoped.Stack()
	for i := range scopedStack.Branches {
		if scopedStack.Branches[i].Name == "pr2" {
			scopedStack.Branches[i].Archived = true
		}
	}
	if err := internal.SaveStack(scoped.FeaturePath, scopedStack); err != nil {
		t.Fatal(err)
	}
	scoped.reparentGit(scoped.Repo, "worktree", "remove", "--force", internal.WorktreePath(scoped.Feature, "pr2"))
	scopedSel := internal.SyncSelection{
		Entries: []internal.SyncSelectedEntry{{Name: "pr2", GitBranch: "feat-pr2"}},
		Names:   map[string]bool{"pr2": true},
	}
	if err := pushScoped(scoped.Feature, externalSyncLayout{
		FeaturePath: scoped.FeaturePath, WorktreesRoot: filepath.Join(scoped.FeaturePath, "worktrees"),
	}, scopedStack, scopedSel, []string{"pr2"}, &internal.SyncRunState{Feature: scoped.Feature}); err != nil {
		t.Fatalf("scoped clearing before preflight: %v", err)
	}
	if _, err := os.Stat(internal.ReparentRemoteRecordPath(scoped.Loc())); !os.IsNotExist(err) {
		t.Fatalf("scoped path did not persist the preflight clear: %v", err)
	}
}

// TestReparentRemote_DryRunPreviewsWithoutWriting is §12.4a. A dry run exits
// 0, runs no `git push`, writes/marks/deletes nothing, renders the argv the
// real run WOULD use, and reports a failed preflight as a diagnostic rather
// than a refusal.
func TestReparentRemote_DryRunPreviewsWithoutWriting(t *testing.T) {
	_ = "asserts AC-091 AC-092"
	assertReparentMatrixBehavior(t, "T-079", "dry-run-no-write")
	t.Run("pending entry previews the strengthened argv", func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		f.reparentGit(f.Repo, "push", "origin", "feat-pr2")
		f.reparentGit(f.Repo, "fetch", "origin")
		remote := f.SHA("refs/remotes/origin/feat-pr2")
		f.reparentGit(internal.WorktreePath(f.Feature, "pr2"), "commit", "--allow-empty", "-m", "local rewrite after publish")
		reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", f.SHA("feat-pr2"), remote)

		before := reparentRecordBytes(t, f)
		stdout, stderr, log := reparentPushCapture(t, func() {
			if code := syncExecute(pushCmd, f.Feature, "--dry-run"); code != 0 {
				t.Errorf("a dry run must exit 0, got %d", code)
			}
		})
		if !strings.Contains(stdout, "  [~] pr2 (would push --force-with-lease --force-if-includes)") {
			t.Fatalf("the preview must name the argv the real run would use:\n%s", stdout)
		}
		if !strings.Contains(stdout, "  [~] pr1 (would push --force-with-lease)") {
			t.Fatalf("an unlisted entry must keep today's exact preview line and stream:\n%s", stdout)
		}
		if !strings.Contains(stderr, "reparent-remote: pr2 was reparented") {
			t.Fatalf("the dry run must still print the warning on stderr:\n%s", stderr)
		}
		for _, argv := range log.argv {
			if argvHas(argv, "push") {
				t.Fatalf("a dry run must run no git push, saw %v", argv)
			}
		}
		if after := reparentRecordBytes(t, f); after != before {
			t.Fatal("a dry run must not write, mark or delete the record")
		}
	})

	t.Run("failed preflight is a diagnostic, not a refusal", func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", f.SHA("feat-pr2"), "0123456789abcdef0123456789abcdef01234567")

		before := reparentRecordBytes(t, f)
		stdout, stderr := syncCaptureStreams(t, func() {
			if code := syncExecute(pushCmd, f.Feature, "--dry-run"); code != 0 {
				t.Errorf("a failed preview is still a successful preview: exit = %d", code)
			}
		})
		if !strings.Contains(stderr, "reparent-remote: would refuse: "+string(internal.ReparentRefusalRemoteFollowupUnsafeLease)) {
			t.Fatalf("the dry run must report the verdict as a diagnostic:\n%s", stderr)
		}
		if strings.Contains(stdout, "would refuse") {
			t.Fatalf("the diagnostic belongs on stderr:\n%s", stdout)
		}
		if after := reparentRecordBytes(t, f); after != before {
			t.Fatal("a failed dry-run preflight must still write nothing")
		}
	})

	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		f.reparentGit(f.Repo, "push", "origin", "feat-pr2")
		f.reparentGit(f.Repo, "fetch", "origin")
		reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", f.SHA("feat-pr2"), f.SHA("refs/remotes/origin/feat-pr2"))
		reparentPlantState(t, f, internal.ReparentStageRefsCommitted)
		before := reparentRecordBytes(t, f)
		_, stderr, log := reparentPushCapture(t, func() {
			if code := syncExecute(pushCmd, f.Feature, "--dry-run"); code != 0 {
				t.Errorf("pre-commit dry-run exit = %d, want 0", code)
			}
		})
		if !strings.Contains(stderr, "reparent-remote: would refuse: reparent-state-present:") {
			t.Fatalf("pre-commit dry-run must report would-refuse:\n%s", stderr)
		}
		for _, argv := range log.argv {
			if argvVerb(argv) == "push" {
				t.Fatalf("dry-run reached Git: %v", argv)
			}
		}
		if after := reparentRecordBytes(t, f); after != before {
			t.Fatal("pre-commit dry-run changed the remote record")
		}
	}(t)

	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", f.SHA("feat-pr2"),
			"0123456789abcdef0123456789abcdef01234567")
		stack := f.Stack()
		filtered := stack.Branches[:0]
		for _, entry := range stack.Branches {
			if entry.Name != "pr2" {
				filtered = append(filtered, entry)
			}
		}
		stack.Branches = filtered
		if err := internal.SaveStack(f.FeaturePath, stack); err != nil {
			t.Fatal(err)
		}
		before := reparentRecordBytes(t, f)
		stdout, stderr, log := reparentPushCapture(t, func() {
			if code := syncExecute(pushCmd, f.Feature, "--dry-run"); code != 0 {
				t.Errorf("entry-gone dry-run exit = %d", code)
			}
		})
		if strings.Contains(stderr, string(internal.ReparentRefusalRemoteFollowupUnsafeLease)) ||
			strings.Contains(stderr, "reparent-remote: pr2") {
			t.Fatalf("in-memory clearing did not precede dry-run preflight:\n%s", stderr)
		}
		if strings.Contains(stdout, "pr2") {
			t.Fatalf("removed entry appeared in dry-run output:\n%s", stdout)
		}
		for _, argv := range log.argv {
			if argvVerb(argv) == "push" {
				t.Fatalf("dry-run reached Git: %v", argv)
			}
		}
		if after := reparentRecordBytes(t, f); after != before {
			t.Fatal("dry-run persisted its in-memory clear")
		}
	}(t)
}

// TestReparentRemote_SuccessfulPushPersistsTheClear is rule 5 and §12.5: a
// real push path evaluates the clearing observations locally, with no fetch,
// and deletes the file once every entry is cleared.
func TestReparentRemote_SuccessfulPushPersistsTheClear(t *testing.T) {
	f := newReparentCustomerExternal(t)
	f.reparentGit(f.Repo, "push", "origin", "feat-pr2")
	f.reparentGit(f.Repo, "fetch", "origin")
	tip := f.SHA("feat-pr2")
	reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", tip, f.SHA("refs/remotes/origin/feat-pr2"))

	_, pushErr := syncCaptureStreams(t, func() {
		if code := syncExecute(pushCmd, f.Feature); code != 0 {
			t.Errorf("push exit = %d", code)
		}
	})
	_ = pushErr
	if _, err := os.Stat(internal.ReparentRemoteRecordPath(f.Loc())); !os.IsNotExist(err) {
		rec, loadErr := internal.LoadReparentRemoteRecord(f.Loc())
		t.Fatalf("a successful push of every pending entry must delete the record (stat err %v, record %+v, load %v)", err, rec, loadErr)
	}
}

func TestReparentRemote_SuccessfulPushSurfacesClearAndReloadFailures(t *testing.T) {
	newPendingPush := func(t *testing.T) (*reparentFixture, string) {
		t.Helper()
		f := newReparentCustomerExternal(t)
		worktree := internal.WorktreePath(f.Feature, "pr2")
		f.reparentGit(worktree, "push", "origin", "feat-pr2")
		f.reparentGit(worktree, "fetch", "origin")
		remoteBefore := f.SHA("refs/remotes/origin/feat-pr2")
		if err := os.WriteFile(filepath.Join(worktree, "post-record.txt"), []byte("new tip\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f.reparentGit(worktree, "add", "post-record.txt")
		f.reparentGit(worktree, "commit", "-m", "post-record tip")
		newTip := f.SHA("feat-pr2")
		reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", newTip, remoteBefore)
		return f, newTip
	}

	t.Run("remote clear persistence", func(t *testing.T) {
		f, newTip := newPendingPush(t)
		recordPath := internal.ReparentRemoteRecordPath(f.Loc())
		internal.SyncStateIOFault = func(op, path string) error {
			if op == internal.SyncIORemoveReparentRemote && path == recordPath {
				return errors.New("injected remote clear persistence failure")
			}
			return nil
		}
		t.Cleanup(func() { internal.SyncStateIOFault = nil })

		_, stderr := syncCaptureStreams(t, func() {
			if code := syncExecute(pushCmd, f.Feature); code != 1 {
				t.Errorf("push exit = %d, want 1 when clear persistence fails", code)
			}
		})
		internal.SyncStateIOFault = nil
		if !strings.Contains(stderr, "persist reparent remote follow-up clear after push") ||
			!strings.Contains(stderr, "injected remote clear persistence failure") {
			t.Fatalf("clear persistence failure was hidden:\n%s", stderr)
		}
		if got := f.SHA("refs/remotes/origin/feat-pr2"); got != newTip {
			t.Fatalf("the Git push itself did not succeed: remote=%s want=%s", got, newTip)
		}
		if _, err := os.Stat(recordPath); err != nil {
			t.Fatalf("failed clear persistence discarded the protection record: %v", err)
		}
	})

	t.Run("post-push stack reload", func(t *testing.T) {
		f, newTip := newPendingPush(t)
		stackPath := internal.StackPath(f.FeaturePath)
		TopLevelPushEntryBarrier = func(index int, entry internal.StackEntry) error {
			if entry.Name == "pr2" {
				return os.Remove(stackPath)
			}
			return nil
		}
		t.Cleanup(func() { TopLevelPushEntryBarrier = nil })

		_, stderr := syncCaptureStreams(t, func() {
			if code := syncExecute(pushCmd, f.Feature); code != 1 {
				t.Errorf("push exit = %d, want 1 when post-push stack reload fails", code)
			}
		})
		TopLevelPushEntryBarrier = nil
		if !strings.Contains(stderr, "reload stack after push before clearing reparent follow-up") {
			t.Fatalf("post-push LoadStack failure was hidden:\n%s", stderr)
		}
		if got := f.SHA("refs/remotes/origin/feat-pr2"); got != newTip {
			t.Fatalf("the Git push itself did not succeed: remote=%s want=%s", got, newTip)
		}
	})
}

// TestReparentRemote_CheckoutPushHonoursTheRule covers the third live path:
// `tws sync <feature> --push` in checkout mode, whose gitPush call sites are
// both wired.
func TestReparentRemote_CheckoutPushHonoursTheRule(t *testing.T) {
	f := newReparentCustomerCheckout(t)
	reparentPlantRemoteRecord(t, f, "pr2", "pr2", f.SHA("pr2"), "0123456789abcdef0123456789abcdef01234567")

	_, stderr, log := reparentPushCapture(t, func() {
		if code := syncExecute(syncCmd, f.Feature, "--push"); code != 1 {
			t.Errorf("a failed checkout preflight must exit 1, got %d", code)
		}
	})
	if !reparentAnchoredRefusalRe(internal.ReparentRefusalRemoteFollowupUnsafeLease).MatchString(stderr) {
		t.Fatalf("the checkout push path must refuse with the same anchored line:\n%s", stderr)
	}
	for _, argv := range log.argv {
		if argvHas(argv, "push") {
			t.Fatalf("the checkout gate is invocation-wide too: nothing may be pushed, saw %v", argv)
		}
	}

	cleared := newReparentCustomerCheckout(t)
	reparentPlantRemoteRecord(t, cleared, "pr2", "pr2", cleared.SHA("pr2"),
		"0123456789abcdef0123456789abcdef01234567")
	stack := cleared.Stack()
	filtered := stack.Branches[:0]
	for _, entry := range stack.Branches {
		if entry.Name != "pr2" {
			filtered = append(filtered, entry)
		}
	}
	stack.Branches = filtered
	if err := internal.SaveStack(cleared.FeaturePath, stack); err != nil {
		t.Fatal(err)
	}
	clearCode := 0
	_, clearStderr, _ := reparentPushCapture(t, func() {
		clearCode = syncExecute(syncCmd, cleared.Feature, "--push")
	})
	if clearCode != 1 || !strings.Contains(clearStderr, "push pr1") {
		t.Fatalf("checkout entry-gone route must reach the ordinary push after clearing: exit=%d\n%s", clearCode, clearStderr)
	}
	if strings.Contains(clearStderr, string(internal.ReparentRefusalRemoteFollowupUnsafeLease)) {
		t.Fatalf("checkout path preflighted a cleared row:\n%s", clearStderr)
	}
	if _, err := os.Stat(internal.ReparentRemoteRecordPath(cleared.Loc())); !os.IsNotExist(err) {
		t.Fatalf("checkout path did not persist the preflight clear: %v", err)
	}
}

func TestReparentRemote_PrePushPostClearReloadFailureRefusesBeforePush(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fixture  func(*testing.T) *reparentFixture
		branches map[string]string
		run      func(*reparentFixture) int
	}{
		{
			name: "external push", fixture: newReparentCustomerExternal,
			branches: map[string]string{"pr1": "feat-pr1", "pr2": "feat-pr2"},
			run:      func(f *reparentFixture) int { return syncExecute(pushCmd, f.Feature) },
		},
		{
			name: "external scoped sync push", fixture: newReparentCustomerExternal,
			branches: map[string]string{"pr1": "feat-pr1", "pr2": "feat-pr2"},
			run: func(f *reparentFixture) int {
				return syncExecute(syncCmd, f.Feature, "--push", "--no-fetch", "--only", "pr2")
			},
		},
		{
			name: "checkout sync push", fixture: newReparentCustomerCheckout,
			branches: map[string]string{"pr1": "pr1", "pr2": "pr2"},
			run: func(f *reparentFixture) int {
				return syncExecute(syncCmd, f.Feature, "--push")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			rec := reparentPlantRemoteRecord(t, f, "pr2", tc.branches["pr2"],
				f.SHA(tc.branches["pr2"]), strings.Repeat("1", 40))
			rec.Entries = append(rec.Entries, internal.ReparentRemoteEntry{
				Name:             "pr1",
				GitBranch:        tc.branches["pr1"],
				Remote:           internal.ReparentRemoteName,
				RemoteRef:        "refs/remotes/origin/" + tc.branches["pr1"],
				NewTipSHA:        f.SHA(tc.branches["pr1"]),
				RemoteSHAAtWrite: strings.Repeat("2", 40),
				PRBaseBefore:     "master",
				PRBaseAfter:      "master",
				State:            internal.ReparentRemoteStatePending,
			})
			if err := internal.SaveReparentRemoteRecord(f.Loc(), rec); err != nil {
				t.Fatal(err)
			}
			f.reparentGit(f.Repo, "update-ref",
				"refs/remotes/origin/"+tc.branches["pr2"], f.SHA(tc.branches["pr2"]))

			recordPath := internal.ReparentRemoteRecordPath(f.Loc())
			faultCalls := 0
			internal.SyncStateIOFault = func(op, path string) error {
				if op != internal.SyncIOWriteReparentRemote || path != recordPath {
					return nil
				}
				faultCalls++
				if faultCalls == 2 {
					return os.Chmod(path, 0)
				}
				return nil
			}
			t.Cleanup(func() {
				internal.SyncStateIOFault = nil
				_ = os.Chmod(recordPath, 0o600)
			})

			_, stderr, log := reparentPushCapture(t, func() {
				if code := tc.run(f); code != 1 {
					t.Errorf("push exit = %d, want 1 after the post-clear reload failure", code)
				}
			})
			internal.SyncStateIOFault = nil
			if err := os.Chmod(recordPath, 0o600); err != nil {
				t.Fatal(err)
			}
			if faultCalls != 2 {
				t.Fatalf("post-clear write fault calls = %d, want 2", faultCalls)
			}
			if !reparentAnchoredRefusalRe(internal.ReparentRefusalStateCorrupt).MatchString(stderr) {
				t.Fatalf("post-clear loader refusal was not anchored:\n%s", stderr)
			}
			for _, argv := range log.argv {
				if argvHas(argv, "push") {
					t.Fatalf("post-clear reload failure must refuse before every push, saw %v", argv)
				}
			}
			after, err := internal.LoadReparentRemoteRecord(f.Loc())
			if err != nil || after == nil {
				t.Fatalf("partly pending record was lost: %+v (%v)", after, err)
			}
			pr2, ok := after.Entry("pr2")
			if !ok || pr2.Pending() {
				t.Fatalf("the pre-push clear did not persist before reload failure: %+v", after.Entries)
			}
			pr1, ok := after.Entry("pr1")
			if !ok || !pr1.Pending() {
				t.Fatalf("remaining pending protection was lost: %+v", after.Entries)
			}
		})
	}
}

// TestReparentRemote_CheckoutTopLevelPushStaysUnsupported is §12.4's own
// parenthetical: `tws push` in checkout mode is NOT a fourth path and must
// keep failing before any Git push.
func TestReparentRemote_CheckoutTopLevelPushStaysUnsupported(t *testing.T) {
	f := newReparentCustomerCheckout(t)
	reparentPlantRemoteRecord(t, f, "pr2", "pr2", f.SHA("pr2"), f.SHA("pr2"))

	for _, args := range [][]string{{f.Feature}, {f.Feature, "--dry-run"}} {
		_, stderr, log := reparentPushCapture(t, func() {
			if code := syncExecute(pushCmd, args...); code != 1 {
				t.Errorf("tws push must stay unsupported in checkout mode, exit = %d", code)
			}
		})
		if !strings.Contains(stderr, "checkout mode") && !strings.Contains(stderr, "linked worktrees") {
			t.Fatalf("the refusal must be the shipped unsupported one, got:\n%s", stderr)
		}
		for _, argv := range log.argv {
			if argvHas(argv, "push") {
				t.Fatalf("checkout top-level push must reach no git push at all, saw %v", argv)
			}
		}
	}
}

// TestReparentRemote_PlanPublishesGuidanceAndMakesNoProviderCall is T-074 and
// §12.2: the guidance is published in order, names no plain `git push`, never
// binds an explicit lease to a freshly observed tip, and the document records
// zero provider calls.
func TestReparentRemote_PlanPublishesGuidanceAndMakesNoProviderCall(t *testing.T) {
	f := newReparentCustomerExternal(t)
	human, _ := f.Plan(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--max-replay-total", "50")

	for _, want := range []string{
		"provider calls=0",
		"tws changed no remote ref and no pull request.",
		"retarget the pull request first, then fetch and integrate remote work, then publish.",
		"if you force publish, use: git push --force-with-lease --force-if-includes origin feat-pr2",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("the plan must publish %q:\n%s", want, human)
		}
	}
	if strings.Contains(human, "--force-with-lease=") {
		t.Fatalf("guidance must never bind an explicit lease to a freshly observed tip:\n%s", human)
	}
	if strings.Contains(human, "implicit push=yes") {
		t.Fatalf("a reparent never pushes implicitly:\n%s", human)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// reparentPushLog records every git argv the shipped push helpers emit, by
// installing the process-level command wrapper the golden harness already
// owns.
type reparentPushLog struct{ argv [][]string }

func (l *reparentPushLog) lines() []string {
	out := make([]string, 0, len(l.argv))
	for _, argv := range l.argv {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

func (l *reparentPushLog) sawPair(first, second string) bool {
	for _, argv := range l.argv {
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == first && argv[i+1] == second {
				return true
			}
		}
	}
	return false
}

// argvHas reports whether one recorded argv contains a token.
func argvHas(argv []string, token string) bool {
	for _, a := range argv {
		if a == token {
			return true
		}
	}
	return false
}

// reparentPushCapture runs fn behind the shipped PATH git wrapper, so every
// argv the push helpers emit is recorded in the same sidecar log the frozen
// no-flag goldens already use. It captures both process streams too, so a
// single call answers "what was printed" and "what was run".
func reparentPushCapture(t *testing.T, fn func()) (stdout, stderr string, log *reparentPushLog) {
	t.Helper()
	w := newSyncGitWrapper(t, false)
	w.around(t, func() {
		stdout, stderr = syncCaptureStreams(t, fn)
	})
	log = &reparentPushLog{}
	for _, rec := range w.records(t) {
		log.argv = append(log.argv, rec.Argv)
	}
	return stdout, stderr, log
}

// reparentRecordBytes reads the record verbatim, or "" when it is absent, so a
// caller can prove a route wrote nothing.
func reparentRecordBytes(t *testing.T, f *reparentFixture) string {
	t.Helper()
	data, err := os.ReadFile(internal.ReparentRemoteRecordPath(f.Loc()))
	if err != nil {
		return ""
	}
	return string(data)
}

// TestReparentRemote_UntrustedRecordRefusesEveryRealPushPath is the fail-open
// hole rule R-PUSH cannot have: a record that EXISTS and cannot be read is not
// "no record". Reading it as one would silently restore the bare
// --force-with-lease argv for branches this tool knows were rewritten, on the
// strength of a file it failed to parse.
func TestReparentRemote_UntrustedRecordRefusesEveryRealPushPath(t *testing.T) {
	plant := func(t *testing.T, f *reparentFixture) {
		t.Helper()
		path := internal.ReparentRemoteRecordPath(f.Loc())
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("record_version: 99\nfeature: "+f.Feature+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = internal.RemoveReparentRemoteRecord(f.Loc()) })
	}

	t.Run("tws push refuses before any push", func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		plant(t, f)

		_, stderr, log := reparentPushCapture(t, func() {
			if code := syncExecute(pushCmd, f.Feature); code != 1 {
				t.Errorf("an untrusted record must exit 1, got %d", code)
			}
		})
		if !reparentAnchoredRefusalRe(internal.ReparentRefusalStateUnsupported).MatchString(stderr) {
			t.Fatalf("stderr must carry the anchored refusal line:\n%s", stderr)
		}
		for _, argv := range log.argv {
			if argvHas(argv, "push") {
				t.Fatalf("nothing may be pushed at all, saw %v", argv)
			}
		}
	})

	t.Run("tws push --dry-run reports it and exits 0", func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		plant(t, f)

		before, err := os.ReadFile(internal.ReparentRemoteRecordPath(f.Loc()))
		if err != nil {
			t.Fatal(err)
		}
		stdout, stderr, log := reparentPushCapture(t, func() {
			if code := syncExecute(pushCmd, f.Feature, "--dry-run"); code != 0 {
				t.Errorf("a failed preview is still a successful preview: exit = %d", code)
			}
		})
		if !strings.Contains(stderr, "reparent-remote: would refuse: "+string(internal.ReparentRefusalStateUnsupported)) {
			t.Fatalf("the dry run must report the verdict as a diagnostic:\n%s", stderr)
		}
		if strings.Contains(stdout, "would refuse") {
			t.Fatalf("the diagnostic belongs on stderr:\n%s", stdout)
		}
		for _, argv := range log.argv {
			if argvHas(argv, "push") {
				t.Fatalf("a dry run must run no git push, saw %v", argv)
			}
		}
		after, err := os.ReadFile(internal.ReparentRemoteRecordPath(f.Loc()))
		if err != nil || string(after) != string(before) {
			t.Fatal("an untrusted record is never rewritten or deleted")
		}
	})

	t.Run("checkout sync --push refuses before any push", func(t *testing.T) {
		f := newReparentCustomerCheckout(t)
		plant(t, f)

		_, stderr, log := reparentPushCapture(t, func() {
			if code := syncExecute(syncCmd, f.Feature, "--push"); code != 1 {
				t.Errorf("an untrusted record must exit 1, got %d", code)
			}
		})
		if !reparentAnchoredRefusalRe(internal.ReparentRefusalStateUnsupported).MatchString(stderr) {
			t.Fatalf("the checkout push path must refuse too, anchored:\n%s", stderr)
		}
		for _, argv := range log.argv {
			if argvHas(argv, "push") {
				t.Fatalf("nothing may be pushed at all, saw %v", argv)
			}
		}
	})

	t.Run("external sync --push refuses before any push", func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		plant(t, f)

		_, stderr, log := reparentPushCapture(t, func() {
			if code := syncExecute(syncCmd, f.Feature, "--push", "--no-fetch"); code == 0 {
				t.Error("an untrusted record must refuse the pushing sync")
			}
		})
		if !reparentAnchoredRefusalRe(internal.ReparentRefusalStateUnsupported).MatchString(stderr) {
			t.Fatalf("stderr must carry the anchored refusal line:\n%s", stderr)
		}
		for _, argv := range log.argv {
			if argvHas(argv, "push") {
				t.Fatalf("nothing may be pushed at all, saw %v", argv)
			}
		}
	})
}

// reparentAnchoredRefusalRe builds the §13.2 shape a push-path refusal must
// take: the marker anchored at the start of its own line, then the kind, then
// the detail. A bare `<kind>: <detail>` line, or a marker buried mid-line,
// fails it.
func reparentAnchoredRefusalRe(kind internal.ReparentRefusalKind) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^reparent: ` + regexp.QuoteMeta(string(kind)) + `: \S`)
}

// TestReparentRemote_NoRecordLeavesPushOutputByteIdentical pins the other half
// of rule 1: anchoring changed nothing for an invocation with no record.
// AC-089.
func TestReparentRemote_NoRecordLeavesPushOutputByteIdentical(t *testing.T) {
	f := newReparentCustomerExternal(t)
	stdout, stderr, _ := reparentPushCapture(t, func() {
		if code := syncExecute(pushCmd, f.Feature, "--dry-run"); code != 0 {
			t.Errorf("a record-free dry run must exit 0, got %d", code)
		}
	})
	if strings.Contains(stdout, "reparent") || strings.Contains(stderr, "reparent") {
		t.Fatalf("no record means no reparent output at all:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestReparentRemote_NoProviderBinaryIsEverInvoked is T-074's real assertion
// and AC-086. A renderer constant proves nothing about what the run EXECUTES,
// so this cell puts executable shims for `gh`, `az` and `glab` first on PATH
// and drives every reparent route plus the push path through them. A shim that
// runs writes its own argv to a log; any log line at all fails the cell.
func TestReparentRemote_NoProviderBinaryIsEverInvoked(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-074", "zero-provider-calls")
	f := newReparentCustomerExternal(t)
	log := reparentInstallProviderShims(t)

	// 1. plan
	if _, err := f.Plan(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--max-replay-total", "50"); err != "" && strings.Contains(err, "reparent:") {
		t.Fatalf("the plan route refused: %s", err)
	}

	// 2. a fresh execution that crashes mid-flight, so 3 and 4 have a run to
	//    resume and to abort.
	internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
		if stage == internal.ReparentStageCommittingRefs && step == "before-cas" {
			return &reparentInjectedFailure{}
		}
		return nil
	}
	if _, _, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 1 {
		internal.ReparentStepHook = nil
		t.Fatalf("the injected crash must fail, exit = %d", exit)
	}
	internal.ReparentStepHook = nil

	// 3. abort that run, which is the same topology the next step re-uses.
	if _, stderr, exit := f.Run(f.Feature, "--abort"); exit != 0 {
		t.Fatalf("--abort exit = %d:\n%s", exit, stderr)
	}

	// 4. a second crashed run, resumed to completion this time.
	internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
		if stage == internal.ReparentStageCommittingRefs && step == "before-cas" {
			return &reparentInjectedFailure{}
		}
		return nil
	}
	if _, _, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 1 {
		internal.ReparentStepHook = nil
		t.Fatalf("the injected crash must fail, exit = %d", exit)
	}
	internal.ReparentStepHook = nil
	if _, stderr, exit := f.Run(f.Feature, "--continue"); exit != 0 {
		t.Fatalf("--continue exit = %d:\n%s", exit, stderr)
	}

	// 5. the push path, with a pending record — the one place a provider call
	//    would be most tempting, and the place §12.1 forbids it outright.
	f.reparentGit(f.Repo, "push", "origin", "feat-pr2")
	f.reparentGit(f.Repo, "fetch", "origin")
	reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", f.SHA("feat-pr2"), f.SHA("refs/remotes/origin/feat-pr2"))
	reparentPushCapture(t, func() {
		_ = syncExecute(pushCmd, f.Feature)
	})

	if lines := log.lines(t); len(lines) != 0 {
		t.Fatalf("a provider binary was invoked %d time(s):\n%s", len(lines), strings.Join(lines, "\n"))
	}
}

// providerShimLog is the file every shim appends to.
type providerShimLog struct{ path string }

func (l providerShimLog) lines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// reparentInstallProviderShims puts executable `gh`, `az` and `glab` shims at
// the FRONT of PATH. Each shim records its own invocation and exits non-zero,
// so a run that shells out to one both leaves evidence and fails loudly.
func reparentInstallProviderShims(t *testing.T) providerShimLog {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "provider-calls.log")
	for _, name := range []string{"gh", "az", "glab"} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> " + logPath + "\nexit 127\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The shims must really be reachable, or the cell would assert nothing.
	if resolved, err := exec.LookPath("gh"); err != nil || filepath.Dir(resolved) != dir {
		t.Fatalf("the gh shim is not first on PATH: %q (%v)", resolved, err)
	}
	return providerShimLog{path: logPath}
}
