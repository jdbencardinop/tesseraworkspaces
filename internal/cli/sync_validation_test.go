package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// §7.7 — external validation. A no-flag run re-reads config on every entry,
// exactly as today. A new-mode run resolves the command once, persists it, and
// uses the persisted string for the rest of the run, so a --continue from a
// different shell or after a config edit cannot validate with a different
// command.
// ---------------------------------------------------------------------------

// writeTestCommandConfig writes the global tws config the fixture's HOME points
// at. An empty command removes the key entirely.
func writeTestCommandConfig(t *testing.T, command string) {
	t.Helper()
	path := internal.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	if command != "" {
		body = "test_command: " + command + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := internal.LoadConfig().TestCommand; got != command {
		t.Fatalf("config test_command = %q, want %q", got, command)
	}
}

// validationMarker is the file the frozen command creates in the worktree it
// validates, so "which command ran" is a fact on disk, not only a log line.
func validationMarker(t *testing.T, worktree, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(worktree, name))
	return err == nil
}

// TestSyncScoped_ValidationCommandIsFrozenAtStart drives a real scoped conflict
// with command A persisted, mutates (or clears) the config, and asserts the
// entry that is rebased on --continue still validates with A.
func TestSyncScoped_ValidationCommandIsFrozenAtStart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after string
	}{
		{name: "config-mutated", after: "touch B.marker"},
		{name: "config-cleared", after: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScopedFixture(t)
			writeTestCommandConfig(t, "touch A.marker")

			// Conflict on the root -> parent edge, so child stays pending and
			// is the entry --continue rebases and validates.
			writeAndCommit(t, f.wt("root"), "conflict.txt", "from-root\n", "root change")
			writeAndCommit(t, f.wt("parent"), "conflict.txt", "from-parent\n", "parent change")

			stdout, _, exit := runSync(t, f.feature, "--from", "root", "--no-fetch")
			if exit == 0 {
				t.Fatalf("the parent rebase must conflict:\n%s", stdout)
			}
			if !strings.Contains(stdout, "validating root: touch A.marker... ok") {
				t.Fatalf("the first entry must validate with the configured command:\n%s", stdout)
			}
			payload := loadPayload(t, f.featurePath)
			if payload.TestCommand != "touch A.marker" {
				t.Fatalf("payload test_command = %q, want the resolved command", payload.TestCommand)
			}
			if payload.ValidationSource != "config" {
				t.Fatalf("payload validation_source = %q, want config", payload.ValidationSource)
			}

			// The world changes underneath the interrupted run.
			writeTestCommandConfig(t, tc.after)
			f.detachGuard(t)
			resolveRebase(t, f.wt("parent"))

			stdout, stderr, exit := runSync(t, f.feature, "--continue")
			if exit != 0 {
				t.Fatalf("--continue must finish: exit=%d\n%s\n%s", exit, stdout, stderr)
			}
			if !strings.Contains(stdout, "validating child: touch A.marker... ok") {
				t.Fatalf("the resumed entry must validate with the FROZEN command:\n%s", stdout)
			}
			if strings.Contains(stdout, "B.marker") {
				t.Fatalf("the edited config must not reach a resumed run:\n%s", stdout)
			}
			if !validationMarker(t, f.wt("child"), "A.marker") {
				t.Fatal("the frozen command must actually have run in the resumed worktree")
			}
			if validationMarker(t, f.wt("child"), "B.marker") {
				t.Fatal("the edited command must never run")
			}
			f.stateFilesGone(t)
		})
	}
}

// TestSyncScoped_ValidationEmptyCommandStaysEmpty pins the other direction: a
// run started with no validation configured must not acquire one mid-run.
func TestSyncScoped_ValidationEmptyCommandStaysEmpty(t *testing.T) {
	f := newScopedFixture(t)
	writeTestCommandConfig(t, "")

	writeAndCommit(t, f.wt("root"), "conflict.txt", "from-root\n", "root change")
	writeAndCommit(t, f.wt("parent"), "conflict.txt", "from-parent\n", "parent change")
	if _, _, exit := runSync(t, f.feature, "--from", "root", "--no-fetch"); exit == 0 {
		t.Fatal("expected a conflict")
	}
	payload := loadPayload(t, f.featurePath)
	if payload.TestCommand != "" {
		t.Fatalf("payload test_command = %q, want the empty frozen decision", payload.TestCommand)
	}
	if payload.ValidationSource != "none" {
		t.Fatalf("payload validation_source = %q, want none", payload.ValidationSource)
	}

	writeTestCommandConfig(t, "touch LATE.marker")
	f.detachGuard(t)
	resolveRebase(t, f.wt("parent"))

	stdout, stderr, exit := runSync(t, f.feature, "--continue")
	if exit != 0 {
		t.Fatalf("--continue must finish: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if strings.Contains(stdout, "validating ") {
		t.Fatalf("a run frozen without validation must never validate:\n%s", stdout)
	}
	if validationMarker(t, f.wt("child"), "LATE.marker") {
		t.Fatal("a command configured after the run started must never run")
	}
}

// TestSyncScoped_ValidationFailureRecordsTheFailedStage pins the payload's
// stage handling around validation: `validating` while it runs, `failed` when
// it fails, and back to `rebasing` when it succeeds.
func TestSyncScoped_ValidationFailureRecordsTheFailedStage(t *testing.T) {
	f := newScopedFixture(t)
	writeTestCommandConfig(t, "git rev-parse --verify refs/heads/does-not-exist")
	f.advanceRoot(t)

	stdout, _, exit := runSync(t, f.feature, "--only", "parent", "--no-fetch")
	if exit == 0 {
		t.Fatalf("a failing validation must fail the run:\n%s", stdout)
	}
	if !strings.Contains(stdout, formatSyncStatus("parent", "active", "validation-failed")) {
		t.Fatalf("missing the validation-failed line:\n%s", stdout)
	}
	payload := loadPayload(t, f.featurePath)
	if payload.Stage != internal.SyncStageFailed {
		t.Fatalf("stage = %q, want failed", payload.Stage)
	}
	if payload.FailedBranch != "parent" {
		t.Fatalf("failed_branch = %q, want parent", payload.FailedBranch)
	}
}

// TestSyncScoped_ValidationStageIsRecordedWhileValidating reads the payload
// from inside the validation command itself, which is the only way to observe
// the transient stage without racing anything.
func TestSyncScoped_ValidationStageIsRecordedWhileValidating(t *testing.T) {
	f := newScopedFixture(t)
	writeTestCommandConfig(t, "true")
	f.advanceRoot(t)

	var seen []internal.SyncRunStage
	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		if stage == internal.SyncStageRebasing {
			// Sample the persisted stage right before every rebase, which is
			// where the previous entry's validation has just been restored.
			if payload, err := internal.LoadSyncRunState(f.featurePath); err == nil {
				seen = append(seen, payload.Stage)
			}
		}
		return nil
	})

	if _, stderr, exit := runSync(t, f.feature, "--from", "parent", "--no-fetch"); exit != 0 {
		t.Fatalf("the run must succeed: %s", stderr)
	}
	for _, stage := range seen {
		if stage != internal.SyncStageRebasing {
			t.Fatalf("a successful validation must restore the rebasing stage; saw %q", stage)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the rebase hook must have observed at least one persisted stage")
	}
}

// TestSyncScoped_PushFailureResumeSkipsResolvedAndValidation pins that a
// push-failure resume neither reports the completed entry as `resolved` nor
// re-runs its validation: the rebase and the validation already succeeded, so
// --continue goes straight to retrying the entries that were never pushed.
func TestSyncScoped_PushFailureResumeSkipsResolvedAndValidation(t *testing.T) {
	f := newScopedFixture(t)
	writeTestCommandConfig(t, "touch A.marker")
	f.advanceRoot(t)
	restoreRemote := rejectPushOf(t, f.remote, "child")

	stdout, _, exit := runSync(t, f.feature, "--from", "parent", "--push", "--no-fetch")
	if exit == 0 {
		t.Fatalf("a refused push must fail the run:\n%s", stdout)
	}
	if !strings.Contains(stdout, "validating child: touch A.marker... ok") {
		t.Fatalf("the first run validates every rebased entry:\n%s", stdout)
	}
	for _, name := range []string{"parent", "child"} {
		if err := os.Remove(filepath.Join(f.wt(name), "A.marker")); err != nil {
			t.Fatal(err)
		}
	}

	restoreRemote()
	f.detachGuard(t)
	stdout, stderr, exit := runSync(t, f.feature, "--continue")
	if exit != 0 {
		t.Fatalf("--continue must retry the unpushed entry: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if strings.Contains(stdout, formatSyncStatus("child", "active", "resolved")) {
		t.Fatalf("a completed entry must not be reported resolved:\n%s", stdout)
	}
	if strings.Contains(stdout, "validating ") {
		t.Fatalf("a push-failure resume must not re-validate anything:\n%s", stdout)
	}
	for _, name := range []string{"parent", "child"} {
		if validationMarker(t, f.wt(name), "A.marker") {
			t.Fatalf("%s was validated again on a push-failure resume", name)
		}
	}
	if !strings.Contains(stdout, "  [+] child (pushed)") {
		t.Fatalf("the unpushed entry must be retried:\n%s", stdout)
	}
	f.stateFilesGone(t)
}

// TestSyncNoFlag_ValidationStillReadsConfigEveryRun keeps the frozen path
// frozen: a no-flag run persists nothing and re-reads the config, including on
// --continue.
func TestSyncNoFlag_ValidationStillReadsConfigEveryRun(t *testing.T) {
	f := newScopedFixture(t)
	writeTestCommandConfig(t, "touch A.marker")
	writeAndCommit(t, f.wt("root"), "conflict.txt", "from-root\n", "root change")
	writeAndCommit(t, f.wt("parent"), "conflict.txt", "from-parent\n", "parent change")

	stdout, _, exit := runSync(t, f.feature)
	if exit == 0 {
		t.Fatalf("expected the parent conflict:\n%s", stdout)
	}
	if internal.HasSyncRunState(f.featurePath) {
		t.Fatal("a no-flag run must persist no v2 payload")
	}

	writeTestCommandConfig(t, "touch B.marker")
	resolveRebase(t, f.wt("parent"))
	stdout, stderr, exit := runSync(t, f.feature, "--continue")
	if exit != 0 {
		t.Fatalf("--continue must finish: exit=%d\n%s\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "validating child: touch B.marker... ok") {
		t.Fatalf("the frozen path re-reads config on every entry:\n%s", stdout)
	}
}
