package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// deadOwnerPID is a pid no test process owns, so a seeded record is classified
// provably stale by the real prober and would be removed by a close that got
// past the guard.
const deadOwnerPID = 99998

// fakeTmuxOnPath installs a tmux stub that records every invocation, and
// returns the log path. Its existence proves a command reached tmux; its
// absence proves the refusal preceded any tmux probe or kill.
func fakeTmuxOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "tmux-invocations.log")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func requireNoTmuxInvocation(t *testing.T, log string) {
	t.Helper()
	if data, err := os.ReadFile(log); err == nil {
		t.Fatalf("tmux was invoked before the refusal: %s", data)
	}
}

// outsideFeatureFixture plants a feature-shaped tree one level above the
// workspace root, holding one direct session record for
// (feature="../outside", branch="branch"). Any command that joins the
// caller-supplied name under the root reaches exactly this tree.
func outsideFeatureFixture(t *testing.T, root string) string {
	t.Helper()
	outside := mustMkdir(t, filepath.Join(filepath.Dir(root), "outside"))
	mustMkdir(t, filepath.Join(outside, "worktrees"))
	if err := os.WriteFile(filepath.Join(outside, "keep.md"), []byte("outside bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	seedRecord(t, outside, "../outside", "branch", deadOwnerPID)
	return outside
}

// TestExternalCloseRefusesTraversalFeatureName pins that `tws close ../outside
// branch` is refused by the feature-name guard before any path under
// TwsRoot() is joined, so no record outside the workspace is read or removed
// and tmux is never touched.
func TestExternalCloseRefusesTraversalFeatureName(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	mustMkdir(t, root)
	tmuxLog := fakeTmuxOnPath(t)

	outside := outsideFeatureFixture(t, root)
	before := snapshotRecordTree(t, outside)

	cmd := closeCmd()
	cmd.SetArgs([]string{"../outside", "branch"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("close must refuse a traversing feature name (output: %s)", out.String())
	}
	if want := `feature name "../outside" contains path separator`; err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if after := snapshotRecordTree(t, outside); after != before {
		t.Fatalf("the tree outside the workspace changed:\n%s\n---\n%s", before, after)
	}
	requireNoTmuxInvocation(t, tmuxLog)

	// Negative control: the guard is what protects the outside tree. Calling
	// the guard-free internal close with the same name does reach it.
	proc := fakeCLIProber{probe: map[int]internal.ProcessLiveness{deadOwnerPID: internal.ProcessDead}}
	tmux := &fakeTmuxOps{exists: map[string]bool{}}
	var controlOut bytes.Buffer
	_ = runExternalClose(&controlOut, "../outside", "branch", proc, tmux)
	if after := snapshotRecordTree(t, outside); after == before {
		t.Fatal("the fixture proves nothing: an unguarded close left the outside record in place")
	}
}

// TestExternalCloseRefusesEveryUnsafeFeatureSpelling covers the other unsafe
// name classes the shared guard now rejects, each before any filesystem or
// tmux action.
func TestExternalCloseRefusesEveryUnsafeFeatureSpelling(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	mustMkdir(t, root)
	tmuxLog := fakeTmuxOnPath(t)
	outside := outsideFeatureFixture(t, root)
	before := snapshotRecordTree(t, outside)

	cases := map[string]string{
		"..":            `feature name ".." contains path traversal`,
		".":             `feature name "." is reserved`,
		"sub/feature":   `feature name "sub/feature" contains path separator`,
		"../../escape":  `feature name "../../escape" contains path separator`,
		".hidden":       `feature name ".hidden" conflicts with reserved directory`,
		"features":      `feature name "features" conflicts with reserved directory`,
		"spaces.yaml":   `feature name "spaces.yaml" conflicts with reserved directory`,
		"nested/../out": `feature name "nested/../out" contains path separator`,
	}
	for feature, want := range cases {
		t.Run(feature, func(t *testing.T) {
			cmd := closeCmd()
			cmd.SetArgs([]string{feature, "branch"})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SilenceUsage = true
			err := cmd.Execute()
			if err == nil || err.Error() != want {
				t.Fatalf("close %q err = %v, want %q", feature, err, want)
			}
		})
	}

	if after := snapshotRecordTree(t, outside); after != before {
		t.Fatalf("the tree outside the workspace changed:\n%s\n---\n%s", before, after)
	}
	requireNoTmuxInvocation(t, tmuxLog)
	// Positive control: a legal feature name is not refused and still reaches
	// tmux, so the refusals above are the guard and not a broken fixture.
	cmd := closeCmd()
	cmd.SetArgs([]string{"acme", "branch"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("a valid feature name must keep its behaviour, got %v", err)
	}
	data, err := os.ReadFile(tmuxLog)
	if err != nil || !strings.Contains(string(data), "has-session") {
		t.Fatalf("a valid feature name must reach tmux: %s (%v)", data, err)
	}
}

// TestStatusRefusesTraversalFeatureName pins the same refusal on the other
// surface this feature newly guards: `tws status` with a caller-supplied name.
func TestStatusRefusesTraversalFeatureName(t *testing.T) {
	repo := setupGitRepo(t, "master")
	root := withUnifiedWorkspaceEnv(t, repo)
	mustMkdir(t, root)
	tmuxLog := fakeTmuxOnPath(t)
	outside := outsideFeatureFixture(t, root)
	before := snapshotRecordTree(t, outside)

	for _, feature := range []string{"../outside", "..", "sub/feature", "features"} {
		out, _, err := runStatus(t, feature)
		if err == nil {
			t.Fatalf("status %q must be refused (output: %s)", feature, out)
		}
		if !strings.Contains(err.Error(), "feature name "+strconv.Quote(feature)) {
			t.Fatalf("status %q err = %v", feature, err)
		}
		if out != "" {
			t.Fatalf("status %q printed a report before refusing: %s", feature, out)
		}
	}
	if _, _, err := runStatus(t, "--json", "../outside"); err == nil {
		t.Fatal("--json must be refused identically")
	}

	if after := snapshotRecordTree(t, outside); after != before {
		t.Fatalf("the tree outside the workspace changed:\n%s\n---\n%s", before, after)
	}
	requireNoTmuxInvocation(t, tmuxLog)
}
