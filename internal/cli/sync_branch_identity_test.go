package cli

import (
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func TestSyncBranchIdentityUsesGitBranch(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	gitRun(t, repo, "branch", "user/feature/pr1", "master")
	path := internal.WorktreePath("feature", "pr1")
	gitRun(t, repo, "worktree", "add", path, "user/feature/pr1")

	entry := internal.StackEntry{Name: "pr1", Branch: "user/feature/pr1", Base: "master"}
	if issue := checkSyncWorktreeBranch(path, entry); issue != nil {
		t.Fatalf("correct Git branch rejected: %s", issue)
	}
}

func TestSyncBranchIdentityRejectsWrongGitBranch(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	gitRun(t, repo, "branch", "wrong", "master")
	path := internal.WorktreePath("feature", "pr1")
	gitRun(t, repo, "worktree", "add", path, "wrong")

	entry := internal.StackEntry{Name: "pr1", Branch: "user/feature/pr1", Base: "master"}
	if issue := checkSyncWorktreeBranch(path, entry); issue == nil {
		t.Fatal("wrong Git branch was accepted")
	}
}
