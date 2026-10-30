package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"golang.org/x/sys/unix"
)

func publicationCommit(t *testing.T, f *publicationFixture, parent, message string) string {
	t.Helper()
	return gitOutput(t, f.repo, "commit-tree", gitOutput(t, f.repo, "rev-parse", "main^{tree}"), "-p", parent, "-m", message)
}

func publicationFollowup(t *testing.T, f *publicationFixture, name, newTip, remoteTip string, neighbor bool) internal.ReparentRemoteRecord {
	t.Helper()
	rec := internal.ReparentRemoteRecord{
		RecordVersion: internal.ReparentRemoteRecordVersion, Feature: "feature",
		RunID: strings.Repeat("b", 32), CreatedAt: "2026-10-30T06:00:00Z",
		Entries: []internal.ReparentRemoteEntry{{
			Name: name, GitBranch: name, Remote: "origin", RemoteRef: "refs/remotes/origin/" + name,
			NewTipSHA: newTip, RemoteSHAAtWrite: remoteTip, PRBaseBefore: "old-base", PRBaseAfter: "main",
			State: internal.ReparentRemoteStatePending,
		}},
	}
	if neighbor {
		missing := filepath.Join(f.repo, "unavailable-neighbor")
		f.stack.Branches[5].Repo = missing
		f.saveStack(t)
		rec.Entries = append(rec.Entries, internal.ReparentRemoteEntry{
			Name: "Y", GitBranch: "Y", Repo: missing, Remote: "origin", RemoteRef: "refs/remotes/origin/Y",
			NewTipSHA: f.oids["Y"], RemoteSHAAtWrite: f.oids["X"], PRBaseBefore: "main", PRBaseAfter: "X",
			State: internal.ReparentRemoteStatePending,
		})
	}
	if err := internal.SaveReparentRemoteRecord(f.location().ReparentLocation(), rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestScopedPushInclusionWitnessAndSelectedFollowup(t *testing.T) {
	for _, mode := range []internal.WorkspaceMode{internal.ModeExternal, internal.ModeCheckout} {
		t.Run(string(mode), func(t *testing.T) {
			f := newPublicationFixture(t, mode)
			t.Setenv("GIT_COMMITTER_DATE", "@1900000300 +0000")
			gitRun(t, f.repo, "push", "origin", "A")
			newTip := publicationCommit(t, f, f.oids["main"], "rewritten A")
			t.Setenv("GIT_COMMITTER_DATE", "@1900000400 +0000")
			gitRun(t, f.repo, "update-ref", "-m", "reparent", "refs/heads/A", newTip, f.oids["A"])
			record := publicationFollowup(t, f, "A", newTip, f.oids["A"], true)
			gitRun(t, f.repo, "push", "--dry-run", "--force-with-lease", "--force-if-includes", "origin", "A")
			before := publicationFiles(t, f.repo, f.root, f.remote)
			out, stderr, err := runPublication(t, "--only", "A", "--dry-run")
			if err != nil || !strings.Contains(out, "inclusion: witness="+f.oids["A"]) {
				t.Fatalf("valid frozen inclusion preview: %v\n%s\n%s", err, out, stderr)
			}
			if !reflect.DeepEqual(before, publicationFiles(t, f.repo, f.root, f.remote)) {
				t.Fatal("inclusion preview wrote state or cleared follow-up")
			}
			out, stderr, err = runPublication(t, "--only", "A")
			if err != nil {
				t.Fatalf("valid rewritten publication: %v\n%s\n%s", err, out, stderr)
			}
			if got := gitOutput(t, f.remote, "rev-parse", "A"); got != newTip {
				t.Fatal("valid rewrite not accepted by real Git")
			}
			after, err := internal.LoadReparentRemoteRecord(f.location().ReparentLocation())
			if err != nil || after == nil || after.CreatedAt != record.CreatedAt || after.Entries[1] != record.Entries[1] ||
				after.Entries[0].State != internal.ReparentRemoteStateCleared {
				t.Fatalf("selected-only clearing lost unavailable neighbor/timestamp: %+v %v", after, err)
			}
		})
	}
}

func TestScopedPushInclusionRefusesHistoricalTipOutsideCutoff(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	remoteAdvance := publicationCommit(t, f, f.oids["A"], "remote advance once seen long ago")
	t.Setenv("GIT_COMMITTER_DATE", "@1900000100 +0000")
	gitRun(t, f.repo, "update-ref", "-m", "old integration", "refs/heads/A", remoteAdvance)
	gitRun(t, f.repo, "push", "origin", "A")
	newTip := publicationCommit(t, f, f.oids["main"], "rewritten without remote advance")
	t.Setenv("GIT_COMMITTER_DATE", "@1900000200 +0000")
	gitRun(t, f.repo, "update-ref", "-m", "rewrite", "refs/heads/A", newTip)
	gitRun(t, f.repo, "update-ref", "-d", "refs/remotes/origin/A")
	t.Setenv("GIT_COMMITTER_DATE", "@1900000300 +0000")
	gitRun(t, f.repo, "fetch", "origin")
	publicationFollowup(t, f, "A", newTip, f.oids["A"], false)
	cmd := exec.Command("git", "-C", f.repo, "push", "--force-with-lease", "--force-if-includes", "origin", "A")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("real Git unexpectedly accepted out-of-range history:\n%s", output)
	}
	if out, stderr, err := runPublication(t, "--only", "A"); err == nil || !strings.Contains(err.Error(), "inclusion refused") {
		t.Fatalf("all-history inclusion incorrectly granted authority: %v\n%s\n%s", err, out, stderr)
	}
	if got := gitOutput(t, f.remote, "rev-parse", "A"); got != remoteAdvance {
		t.Fatal("unintegrated remote history was overwritten")
	}
}

func TestScopedPushInclusionRefusesUnseenRemoteAdvance(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	gitRun(t, f.repo, "push", "origin", "A")
	remoteAdvance := publicationCommit(t, f, f.oids["A"], "unseen remote work")
	gitRun(t, f.repo, "push", "origin", remoteAdvance+":refs/heads/A")
	newTip := publicationCommit(t, f, f.oids["main"], "rewritten without unseen work")
	gitRun(t, f.repo, "update-ref", "-m", "rewrite", "refs/heads/A", newTip)
	publicationFollowup(t, f, "A", newTip, f.oids["A"], false)
	cmd := exec.Command("git", "-C", f.repo, "push", "--force-with-lease", "--force-if-includes", "origin", "A")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("real Git accepted unseen remote advance:\n%s", output)
	}
	if _, _, err := runPublication(t, "--only", "A"); err == nil || !strings.Contains(err.Error(), "inclusion refused") {
		t.Fatalf("independent inclusion accepted unseen remote advance: %v", err)
	}
	if got := gitOutput(t, f.remote, "rev-parse", "A"); got != remoteAdvance {
		t.Fatal("unseen remote advance overwritten")
	}
}

func TestScopedPushExactLeaseSurvivesTrackingAdvanceBeforeAttempt(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	gitRun(t, f.repo, "push", "origin", "B")
	source := publicationCommit(t, f, f.oids["B"], "intended publication")
	advance := publicationCommit(t, f, f.oids["B"], "concurrent remote work")
	gitRun(t, f.repo, "update-ref", "refs/heads/B", source)
	fired := false
	internal.ScopedPushCheckpoint = func(step string, _ int) error {
		if step == "before-push" && !fired {
			fired = true
			gitRun(t, f.repo, "push", "origin", advance+":refs/heads/B")
			gitRun(t, f.repo, "fetch", "origin")
		}
		return nil
	}
	t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
	if out, stderr, err := runPublication(t, "--only", "B"); err == nil || !fired {
		t.Fatalf("exact stale lease was replaced with current tracking: %v\n%s\n%s", err, out, stderr)
	}
	internal.ScopedPushCheckpoint = nil
	if got := gitOutput(t, f.remote, "rev-parse", "B"); got != advance {
		t.Fatal("real Git accepted a stale expected-OID lease")
	}
	if state := f.state(t); state.Plan.Entries[0].Expected != f.oids["B"] || state.Rows[0] != "intent" {
		t.Fatal("failed attempt did not retain original lease and uncertainty")
	}
	if _, _, err := runPublication(t, "--continue"); err == nil {
		t.Fatal("continuation refreshed lease after fetch")
	}
}

func TestScopedPushCredentialsNeverEnterPreviewOrJournal(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	secret := "do-not-store-this-credential"
	gitRun(t, f.repo, "remote", "set-url", "origin", "https://user:"+secret+"@example.invalid/repo.git")
	out, stderr, err := runPublication(t, "--only", "B", "--dry-run")
	if err != nil || strings.Contains(out+stderr, secret) || strings.Contains(out+stderr, "https://") {
		t.Fatalf("credential leaked in preview: %v", err)
	}
	internal.ScopedPushCheckpoint = func(step string, _ int) error {
		if step == "before-push" {
			return errors.New("stop before contacting credential-bearing endpoint")
		}
		return nil
	}
	t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
	out, stderr, err = runPublication(t, "--only", "B")
	if err == nil || strings.Contains(out+stderr+err.Error(), secret) {
		t.Fatal("credential leaked in execution refusal")
	}
	body, err := os.ReadFile(internal.ScopedPushStatePath(f.location()))
	if err != nil || bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte("https://")) {
		t.Fatal("endpoint credentials were persisted")
	}
}

func TestScopedPushFrozenWitnessSurvivesReflogExpiry(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	t.Setenv("GIT_COMMITTER_DATE", "@1900000300 +0000")
	gitRun(t, f.repo, "push", "origin", "A")
	newTip := publicationCommit(t, f, f.oids["main"], "rewritten A")
	t.Setenv("GIT_COMMITTER_DATE", "@1900000400 +0000")
	gitRun(t, f.repo, "update-ref", "-m", "rewrite", "refs/heads/A", newTip)
	publicationFollowup(t, f, "A", newTip, f.oids["A"], false)
	internal.ScopedPushCheckpoint = func(step string, _ int) error {
		if step == "before-push" {
			return errors.New("stop with durable intent and protected witness")
		}
		return nil
	}
	t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
	if _, _, err := runPublication(t, "--only", "A"); err == nil {
		t.Fatal("did not stop before network")
	}
	frozen := f.state(t)
	internal.ScopedPushCheckpoint = nil
	gitRun(t, f.repo, "reflog", "expire", "--expire=all", "--all")
	gitRun(t, f.repo, "update-ref", "refs/remotes/origin/A", f.oids["B"])
	gitRun(t, f.repo, "gc", "--prune=now")
	out, stderr, err := runPublication(t, "--continue")
	if err != nil {
		t.Fatalf("frozen proof was not retained: %v\n%s\n%s", err, out, stderr)
	}
	if f.state(t).Plan.Entries[0].Expected != frozen.Plan.Entries[0].Expected {
		t.Fatal("continuation refreshed the frozen lease")
	}
	if got := gitOutput(t, f.remote, "rev-parse", "A"); got != newTip {
		t.Fatal("frozen valid inclusion failed to publish")
	}
}

func TestScopedPushAmendedFollowupSurvivesGCAndCleanupReplay(t *testing.T) {
	for _, recovery := range []string{"--continue", "--cancel"} {
		t.Run(recovery, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeExternal)
			t.Setenv("GIT_COMMITTER_DATE", "@1900000300 +0000")
			gitRun(t, f.repo, "push", "origin", "C")
			t.Setenv("GIT_COMMITTER_DATE", "@1900000400 +0000")
			gitRun(t, f.worktree("C"), "rebase", "--keep-empty", "--onto", "X", "A")
			rewrite := gitOutput(t, f.repo, "rev-parse", "C")
			f.stack.Branches[2].Base = "X"
			f.stack.Branches[2].LastBaseSHA = f.oids["X"]
			f.saveStack(t)
			record := publicationFollowup(t, f, "C", rewrite, f.oids["C"], true)
			t.Setenv("GIT_COMMITTER_DATE", "@1900000500 +0000")
			gitRun(t, f.worktree("C"), "commit", "--amend", "--allow-empty", "-m", "amended rewritten C")
			source := gitOutput(t, f.repo, "rev-parse", "C")
			if source == rewrite {
				t.Fatal("fixture did not amend away the follow-up tip")
			}
			internal.ScopedPushCheckpoint = func(step string, _ int) error {
				if step == "before-push" {
					return errors.New("pause after durable intent")
				}
				return nil
			}
			t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
			if _, _, err := runPublication(t, "--only", "C"); err == nil {
				t.Fatal("expected interrupted publication")
			}
			state := f.state(t)
			followupPin := -1
			for i, pin := range state.Plan.Pins {
				if pin.Kind == "followup" && pin.OID == rewrite {
					followupPin = i
				}
			}
			if followupPin < 0 {
				t.Fatal("cleanup-dependent rewritten tip was not protected")
			}
			gitRun(t, f.repo, "reflog", "expire", "--expire=all", "--all")
			gitRun(t, f.repo, "gc", "--prune=now")
			gitRun(t, f.repo, "cat-file", "-e", rewrite+"^{commit}")
			internal.ScopedPushCheckpoint = func(step string, index int) error {
				if step == "after-remove-object" && index == followupPin {
					return errors.New("lost process after follow-up ref deletion")
				}
				return nil
			}
			if _, _, err := runPublication(t, "--continue"); err == nil {
				t.Fatal("expected interrupted ref cleanup")
			}
			state = f.state(t)
			if state.Rows[0] != "succeeded" || state.FollowupDecisions["C"] != "keep" {
				t.Fatalf("follow-up decision was not durable before releasing R: %+v", state.FollowupDecisions)
			}
			gitRun(t, f.repo, "reflog", "expire", "--expire=all", "--all")
			gitRun(t, f.repo, "gc", "--prune=now")
			probe := exec.Command("git", "-C", f.repo, "cat-file", "-e", rewrite+"^{commit}")
			if err := probe.Run(); err == nil {
				t.Fatal("fixture did not actually prune the now-unprotected rewrite tip")
			}
			internal.ScopedPushCheckpoint = nil
			restore := rejectPushOf(t, f.remote, "C")
			defer restore()
			if out, stderr, err := runPublication(t, recovery); err != nil {
				t.Fatalf("cleanup re-read pruned follow-up evidence: %v\n%s\n%s", err, out, stderr)
			}
			if got := gitOutput(t, f.remote, "rev-parse", "C"); got != source {
				t.Fatal("cleanup replay changed confirmed publication")
			}
			after, err := internal.LoadReparentRemoteRecord(f.location().ReparentLocation())
			if err != nil || after == nil || !reflect.DeepEqual(*after, record) {
				t.Fatalf("unproved or unrelated follow-up rows changed: %+v %v", after, err)
			}
		})
	}
}

func TestScopedPushRejectsEndpointNamedRemoteAndLateCollision(t *testing.T) {
	for _, timing := range []string{"preview", "before-push", "reconcile"} {
		t.Run(timing, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeExternal)
			decoy := filepath.Join(filepath.Dir(f.repo), "decoy.git")
			second := filepath.Join(filepath.Dir(f.repo), "second.git")
			gitRun(t, "", "init", "--bare", decoy)
			gitRun(t, "", "init", "--bare", second)
			endpoint := "file://" + f.remote
			gitRun(t, f.repo, "remote", "set-url", "origin", endpoint)
			gitStdout := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", f.repo}, args...)...).Output()
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(string(out))
			}
			collide := func() {
				gitRun(t, f.repo, "config", "remote."+endpoint+".url", decoy)
				gitRun(t, f.repo, "config", "--add", "remote."+endpoint+".pushurl", decoy)
				gitRun(t, f.repo, "config", "--add", "remote."+endpoint+".pushurl", second)
				if got := gitStdout("remote", "get-url", "--push", "origin"); got != endpoint {
					t.Fatalf("fixture changed origin rather than shadowing its resolved endpoint: got %q, want %q", got, endpoint)
				}
			}
			t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
			switch timing {
			case "preview":
				collide()
				if got := gitStdout("ls-remote", "--get-url", endpoint); got != decoy {
					t.Fatal("fixture did not demonstrate remote-name lookup redirection")
				}
				if _, _, err := runPublication(t, "--only", "B", "--dry-run"); err == nil || !strings.Contains(err.Error(), "configured remote name") {
					t.Fatalf("endpoint-name collision was accepted: %v", err)
				}
			case "before-push":
				internal.ScopedPushCheckpoint = func(step string, _ int) error {
					if step == "before-push" {
						collide()
					}
					return nil
				}
				if _, _, err := runPublication(t, "--only", "B"); err == nil {
					t.Fatal("late remote-name collision was accepted")
				}
			case "reconcile":
				internal.ScopedPushCheckpoint = func(step string, _ int) error {
					if step == "after-push" {
						return errors.New("lost accepted result")
					}
					return nil
				}
				if _, _, err := runPublication(t, "--only", "B"); err == nil {
					t.Fatal("expected accepted-but-unrecorded state")
				}
				internal.ScopedPushCheckpoint = nil
				collide()
				if _, _, err := runPublication(t, "--continue"); err == nil {
					t.Fatal("reconciliation followed a shadowing remote name")
				}
			}
			if remoteRefs(t, decoy) != "" || remoteRefs(t, second) != "" {
				t.Fatal("publication reached a shadowing remote")
			}
		})
	}
}

func TestScopedPushRejectsSymlinkDependentParentTraversal(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(fmt.Sprint(absolute), func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeExternal)
			remotes := filepath.Join(t.TempDir(), "remotes")
			group := filepath.Join(remotes, "group")
			if err := os.MkdirAll(group, 0o700); err != nil {
				t.Fatal(err)
			}
			actual := filepath.Join(remotes, "upstream.git")
			decoy := filepath.Join(f.repo, "upstream.git")
			gitRun(t, "", "init", "--bare", actual)
			gitRun(t, "", "init", "--bare", decoy)
			if err := os.Symlink(group, filepath.Join(f.repo, "link")); err != nil {
				t.Fatal(err)
			}
			origin := "link/../upstream.git"
			if absolute {
				origin = f.repo + "/" + origin
			}
			gitRun(t, f.repo, "remote", "set-url", "origin", origin)
			gitRun(t, f.repo, "push", "origin", "A")
			if got := gitOutput(t, actual, "rev-parse", "A"); got != f.oids["A"] {
				t.Fatal("literal Git path semantics were not demonstrated")
			}
			for _, args := range [][]string{{"--only", "B", "--dry-run"}, {"--only", "B"}} {
				if _, _, err := runPublication(t, args...); err == nil || !strings.Contains(err.Error(), "parent traversal") {
					t.Fatalf("ambiguous local endpoint was accepted: %v", err)
				}
			}
			if remoteRefs(t, decoy) != "" || strings.Contains(remoteRefs(t, actual), "refs/heads/B") {
				t.Fatal("lexical path normalization redirected publication")
			}
		})
	}
}

func TestScopedPushConfigurationCannotBroadenPublication(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	gitRun(t, f.repo, "tag", "-a", "unselected-tag", "-m", "must not publish", "B")
	for key, value := range map[string]string{
		"push.followTags": "true", "remote.origin.mirror": "true", "push.default": "matching",
		"remote.origin.push": "refs/heads/*:refs/heads/*", "push.recurseSubmodules": "on-demand",
		"push.autoSetupRemote": "true",
	} {
		gitRun(t, f.repo, "config", key, value)
	}
	hook := filepath.Join(f.repo, ".git", "hooks", "pre-push")
	marker := filepath.Join(f.repo, "hook-was-run")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf enabled > '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := gitOutput(t, f.repo, "config", "--local", "--list")
	out, stderr, err := runPublication(t, "--only", "B")
	if err != nil {
		t.Fatalf("narrow push with broad Git defaults: %v\n%s\n%s", err, out, stderr)
	}
	if got := remoteRefs(t, f.remote); got != "refs/heads/B\nrefs/heads/main" {
		t.Fatalf("Git config broadened publication:\n%s", got)
	}
	if after := gitOutput(t, f.repo, "config", "--local", "--list"); after != config {
		t.Fatal("publication changed local tracking/upstream configuration")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("user pre-push hook was disabled")
	}
}

func TestScopedPushEndpointAndSourceDriftRefuse(t *testing.T) {
	for _, change := range []string{"source", "endpoint", "mapping", "ambiguous-pushurls"} {
		t.Run(change, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeExternal)
			restore := rejectPushOf(t, f.remote, "B")
			if _, _, err := runPublication(t, "--only", "B"); err == nil {
				t.Fatal("expected blocked publication")
			}
			restore()
			switch change {
			case "source":
				gitRun(t, f.repo, "update-ref", "refs/heads/B", f.oids["D"])
			case "endpoint":
				gitRun(t, f.repo, "remote", "set-url", "origin", f.remote+".different")
			case "mapping":
				gitRun(t, f.repo, "config", "--add", "remote.origin.fetch", "+refs/heads/B:refs/remotes/origin/B")
			case "ambiguous-pushurls":
				gitRun(t, f.repo, "config", "--add", "remote.origin.pushurl", f.remote)
				gitRun(t, f.repo, "config", "--add", "remote.origin.pushurl", f.remote)
			}
			if _, _, err := runPublication(t, "--continue"); err == nil {
				t.Fatal("changed frozen source/destination identity was accepted")
			}
			if strings.Contains(remoteRefs(t, f.remote), "refs/heads/B") {
				t.Fatal("refusal contacted/mutated destination")
			}
			if _, _, err := runPublication(t, "--cancel"); err != nil {
				t.Fatalf("cancellation unnecessarily required current endpoint/source authority: %v", err)
			}
		})
	}
}

func TestScopedPushRejectsEndpointRewritingAfterResolution(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	decoy := filepath.Join(filepath.Dir(f.repo), "decoy.git")
	gitRun(t, "", "init", "--bare", decoy)
	gitRun(t, f.repo, "remote", "set-url", "origin", "short:repo")
	gitRun(t, f.repo, "config", "url."+f.remote+".insteadOf", "short:repo")
	gitRun(t, f.repo, "config", "url."+decoy+".insteadOf", f.remote)
	if got := gitOutput(t, f.repo, "remote", "get-url", "--push", "origin"); got != f.remote {
		t.Fatalf("named endpoint = %q", got)
	}
	if got := gitOutput(t, f.repo, "ls-remote", "--get-url", f.remote); got != decoy {
		t.Fatalf("fixture did not demonstrate the second rewrite: %q", got)
	}
	for _, args := range [][]string{{"--only", "B", "--dry-run"}, {"--only", "B"}} {
		if _, _, err := runPublication(t, args...); err == nil || !strings.Contains(err.Error(), "rewritten again") {
			t.Fatalf("double-expanded endpoint was accepted: %v", err)
		}
	}
	if strings.Contains(remoteRefs(t, f.remote), "refs/heads/B") || remoteRefs(t, decoy) != "" {
		t.Fatal("publication escaped to a rewritten endpoint")
	}
	if _, err := os.Lstat(internal.ScopedPushStatePath(f.location())); !os.IsNotExist(err) {
		t.Fatal("unverifiable endpoint published a journal")
	}
}

func TestScopedPushPersistenceFaultsAndExactCleanup(t *testing.T) {
	for _, fault := range []string{"before-state-write", "after-state-install", "before-state-directory-sync", "before-private-ref-directory-sync", "after-remove-object", "after-release-guard"} {
		t.Run(fault, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeExternal)
			fired := false
			internal.ScopedPushCheckpoint = func(step string, _ int) error {
				if step == fault && !fired {
					// Keep an authoritative journal before injecting
					// failures in subsequent atomic writes.
					if step == "before-state-write" {
						if _, err := os.Stat(internal.ScopedPushStatePath(f.location())); err != nil {
							return nil
						}
					}
					fired = true
					return errors.New("injected durability/cleanup failure")
				}
				return nil
			}
			t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
			if _, _, err := runPublication(t, "--only", "B"); err == nil || !fired {
				t.Fatal("durability fault did not stop publication")
			}
			internal.ScopedPushCheckpoint = nil
			out, stderr, err := runPublication(t, "--continue")
			if err != nil {
				t.Fatalf("durability recovery: %v\n%s\n%s", err, out, stderr)
			}
			if f.state(t).Phase != "completed" {
				t.Fatal("durability failure was not recovered")
			}
		})
	}
}

func TestScopedPushVisibleSuccessRepeatsDurabilityBeforeNextEntry(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	fired := false
	internal.ScopedPushCheckpoint = func(step string, _ int) error {
		if step != "after-state-install" || fired {
			return nil
		}
		state, err := internal.LoadScopedPushState(f.location())
		if err != nil {
			return err
		}
		if state.Rows[0] == "succeeded" {
			fired = true
			return errors.New("success visible but directory durability failed")
		}
		return nil
	}
	t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
	if _, _, err := runPublication(t, "--from", "A"); err == nil || !fired {
		t.Fatal("success durability failure did not stop before the next entry")
	}
	if strings.Contains(remoteRefs(t, f.remote), "refs/heads/B") {
		t.Fatal("next entry published without durable preceding success")
	}
	barrier := false
	internal.ScopedPushCheckpoint = func(step string, index int) error {
		if step == "before-state-directory-sync" {
			barrier = true
		}
		if step == "before-push" && (!barrier || index == 0) {
			t.Fatal("continued without repeating durability or republished a confirmed row")
		}
		return nil
	}
	restore := rejectPushOf(t, f.remote, "A")
	defer restore()
	if out, stderr, err := runPublication(t, "--continue"); err != nil {
		t.Fatalf("visible success durability recovery: %v\n%s\n%s", err, out, stderr)
	}
}

func TestScopedPushForeignPrivateRefAndReplacedStateArePreserved(t *testing.T) {
	for _, foreign := range []string{"private-ref", "same-bytes-new-inode", "live-lock"} {
		t.Run(foreign, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeExternal)
			restore := rejectPushOf(t, f.remote, "B")
			if _, _, err := runPublication(t, "--only", "B"); err == nil {
				t.Fatal("expected failure")
			}
			restore()
			state := f.state(t)
			switch foreign {
			case "private-ref":
				ref := state.Plan.Pins[0].Ref
				gitRun(t, f.repo, "update-ref", ref, f.oids["X"])
				if _, _, err := runPublication(t, "--cancel"); err == nil {
					t.Fatal("foreign private ref was cleaned")
				}
				if got := gitOutput(t, f.repo, "rev-parse", ref); got != f.oids["X"] {
					t.Fatal("foreign protection ref changed")
				}
			case "same-bytes-new-inode":
				path := internal.ScopedPushStatePath(f.location())
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				fired := false
				internal.ScopedPushCheckpoint = func(step string, _ int) error {
					if step == "before-state-install" && !fired {
						fired = true
						// Retain the old inode so an allocator cannot reuse it.
						if err := os.Rename(path, path+".held-old-inode"); err != nil {
							return err
						}
						return os.WriteFile(path, body, 0o600)
					}
					return nil
				}
				t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
				if _, _, err := runPublication(t, "--cancel"); err == nil || !fired {
					t.Fatal("same bytes on a foreign inode were overwritten")
				}
				if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, body) {
					t.Fatal("replacement state was changed")
				}
			case "live-lock":
				pid, cleanup := spawnLivePID(t)
				defer cleanup()
				lock := []byte(fmtPublicationGuard(pid, state.Plan.Token))
				path := internal.SyncRunGuardPath(f.featurePath)
				if err := os.WriteFile(path, lock, 0o600); err != nil {
					t.Fatal(err)
				}
				if _, _, err := runPublication(t, "--cancel"); err == nil {
					t.Fatal("live publication lock stolen")
				}
				if body, err := os.ReadFile(path); err != nil || !bytes.Equal(body, lock) {
					t.Fatal("foreign live guard changed")
				}
			}
		})
	}
}

func fmtPublicationGuard(pid int, token string) string {
	return fmt.Sprintf("pid: %d\ncreated: \"2026-10-30T06:00:00Z\"\ntoken: %q\nstate_version: 2\n", pid, token)
}

func TestScopedPushSpecialAndCorruptStateRefusesPromptly(t *testing.T) {
	for _, object := range []string{"fifo", "directory", "symlink", "corrupt", "unsupported", "duplicate-key"} {
		t.Run(object, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeExternal)
			path := internal.ScopedPushStatePath(f.location())
			switch object {
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(f.repo, ".git", "config"), path); err != nil {
					t.Fatal(err)
				}
			default:
				body := "{broken"
				if object == "unsupported" {
					body = `{"kind":"tws-scoped-publication","version":999}`
				}
				if object == "duplicate-key" {
					body = `{"version":1,"version":1}`
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			start := time.Now()
			if _, _, err := runPublication(t, "--continue", "--dry-run"); err == nil {
				t.Fatal("untrusted state was accepted")
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("special state blocked for %s", elapsed)
			}
			if _, _, err := runPublication(t, "--cancel"); err == nil {
				t.Fatal("untrusted state was canceled/deleted")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("untrusted state was removed")
			}
		})
	}
}

func TestScopedPushRejectsEndpointGroupsWithGitKeySemantics(t *testing.T) {
	for _, timing := range []string{"fresh", "uppercase-variable", "late", "reconcile"} {
		t.Run(timing, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeExternal)
			remote := filepath.Join(filepath.Dir(f.repo), "receiver.GIT")
			if err := os.Rename(f.remote, remote); err != nil {
				t.Fatal(err)
			}
			f.remote = remote
			endpoint := "file://" + remote
			gitRun(t, f.repo, "remote", "set-url", "origin", endpoint)
			extra := filepath.Join(filepath.Dir(f.repo), "extra.git")
			gitRun(t, "", "init", "--bare", extra)
			gitRun(t, f.repo, "remote", "add", "extra", extra)
			gitRun(t, f.repo, "push", "extra", "X:B")
			group := func() {
				key := "remotes." + endpoint
				if timing == "uppercase-variable" || timing == "late" {
					key = strings.TrimSuffix(key, ".GIT") + ".git"
				}
				gitRun(t, f.repo, "config", key, "origin extra")
				got := gitOutput(t, f.repo, "config", "--get-all", "remotes."+endpoint)
				if got != "origin extra" {
					t.Fatal("fixture did not exercise Git's variable-component normalization")
				}
			}
			t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
			switch timing {
			case "fresh", "uppercase-variable":
				group()
				for _, args := range [][]string{{"--only", "B", "--dry-run"}, {"--only", "B"}} {
					if _, _, err := runPublication(t, args...); err == nil || !strings.Contains(err.Error(), "remote group") {
						t.Fatalf("endpoint group was accepted: %v", err)
					}
				}
				if _, err := os.Lstat(internal.ScopedPushStatePath(f.location())); !os.IsNotExist(err) {
					t.Fatal("group collision published a journal")
				}
			case "late":
				internal.ScopedPushCheckpoint = func(step string, _ int) error {
					if step == "before-push" {
						group()
					}
					return nil
				}
				if _, _, err := runPublication(t, "--only", "B"); err == nil || !strings.Contains(err.Error(), "remote group") {
					t.Fatalf("late group replaced an absence lease: %v", err)
				}
				if f.state(t).Plan.Entries[0].Expected != "" {
					t.Fatal("fixture did not retain the original absence lease")
				}
			case "reconcile":
				internal.ScopedPushCheckpoint = func(step string, _ int) error {
					if step == "after-push" {
						return errors.New("lost accepted result")
					}
					return nil
				}
				if _, _, err := runPublication(t, "--only", "B"); err == nil {
					t.Fatal("expected an uncertain result")
				}
				internal.ScopedPushCheckpoint = nil
				group()
				if _, _, err := runPublication(t, "--continue"); err == nil || !strings.Contains(err.Error(), "remote group") {
					t.Fatalf("recovery accepted endpoint group redirection: %v", err)
				}
			}
			if got := gitOutput(t, extra, "rev-parse", "B"); got != f.oids["X"] {
				t.Fatal("group dispatch overwrote an extra repository's existing ref")
			}
			if timing != "reconcile" && strings.Contains(remoteRefs(t, f.remote), "refs/heads/B") {
				t.Fatal("group refusal changed the intended remote")
			}
		})
	}
}
