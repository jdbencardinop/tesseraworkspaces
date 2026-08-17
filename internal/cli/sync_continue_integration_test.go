package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func TestSyncContinueResumesDescendants(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	createLinearStack(t, repo, "feature")

	featurePath := internal.FeaturePath("feature")
	parentPath := internal.WorktreePath("feature", "parent")
	childPath := internal.WorktreePath("feature", "child")
	parentOld := gitOutput(t, parentPath, "rev-parse", "HEAD")
	writeAndCommit(t, parentPath, "parent.txt", "parent-v2\n", "parent v2")
	parentNew := gitOutput(t, parentPath, "rev-parse", "HEAD")
	if parentOld == parentNew {
		t.Fatal("parent did not advance")
	}

	state := internal.NewSyncState()
	state.FailedBranch = "parent"
	state.Completed = []string{"root"}
	state.Pending = []string{"child"}
	if err := internal.SaveSyncState(featurePath, state); err != nil {
		t.Fatal(err)
	}

	if err := handleSyncContinue("feature", externalSyncLayout{FeaturePath: featurePath, WorktreesRoot: filepath.Join(featurePath, "worktrees")}, false); err != nil {
		t.Fatalf("handleSyncContinue: %v", err)
	}
	if internal.HasSyncState(featurePath) {
		t.Fatal("sync state was not cleared after complete continuation")
	}
	if internal.RunSilentDir(childPath, "git", "merge-base", "--is-ancestor", parentNew, "child") != nil {
		t.Fatal("child does not contain updated parent")
	}
}

func TestSyncContinueRetainsStateOnLaterFailure(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	createLinearStack(t, repo, "feature")

	featurePath := internal.FeaturePath("feature")
	parentPath := internal.WorktreePath("feature", "parent")
	childPath := internal.WorktreePath("feature", "child")
	writeAndCommit(t, parentPath, "conflict.txt", "from-parent\n", "parent change")
	writeAndCommit(t, childPath, "conflict.txt", "from-child\n", "child change")

	state := internal.NewSyncState()
	state.FailedBranch = "parent"
	state.Completed = []string{"root"}
	state.Pending = []string{"child"}
	if err := internal.SaveSyncState(featurePath, state); err != nil {
		t.Fatal(err)
	}

	if err := handleSyncContinue("feature", externalSyncLayout{FeaturePath: featurePath, WorktreesRoot: filepath.Join(featurePath, "worktrees")}, false); err == nil {
		t.Fatal("expected child conflict")
	}
	persisted, err := internal.LoadSyncState(featurePath)
	if err != nil {
		t.Fatalf("sync state not retained: %v", err)
	}
	if persisted.FailedBranch != "child" {
		t.Fatalf("failed branch = %q, want child", persisted.FailedBranch)
	}
}

func createLinearStack(t *testing.T, repo, feature string) {
	t.Helper()
	if err := createWorktree(feature, "root", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	rootPath := internal.WorktreePath(feature, "root")
	writeAndCommit(t, rootPath, "root.txt", "root\n", "root")

	if err := createWorktree(feature, "parent", "root", repo, false); err != nil {
		t.Fatal(err)
	}
	parentPath := internal.WorktreePath(feature, "parent")
	writeAndCommit(t, parentPath, "parent-only.txt", "parent\n", "parent")

	if err := createWorktree(feature, "child", "parent", repo, false); err != nil {
		t.Fatal(err)
	}
	childPath := internal.WorktreePath(feature, "child")
	writeAndCommit(t, childPath, "child.txt", "child\n", "child")

	// Ensure injected symlinks do not affect test cleanliness.
	for _, path := range []string{rootPath, parentPath, childPath} {
		_ = os.Remove(filepath.Join(path, "CLAUDE.local.md"))
	}
}
