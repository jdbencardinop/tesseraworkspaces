package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func TestCreateWorktreeFromExplicitLocalBase(t *testing.T) {
	repo := setupGitRepo(t, "master")
	masterSHA := gitOutput(t, repo, "rev-parse", "master")

	gitRun(t, repo, "switch", "-c", "dirty-head")
	writeAndCommit(t, repo, "dirty.txt", "dirty\n", "dirty head")
	if got := gitOutput(t, repo, "rev-parse", "HEAD"); got == masterSHA {
		t.Fatal("test setup did not move HEAD away from master")
	}

	withWorkspaceEnv(t, repo)
	if err := createWorktree("feature", "local-base", "master", repo, false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	assertWorktreeHEAD(t, "feature", "local-base", masterSHA)
	stack, err := internal.LoadStack(internal.FeaturePath("feature"))
	if err != nil {
		t.Fatal(err)
	}
	if got := internal.GetBranch(stack, "local-base").Base; got != "master" {
		t.Fatalf("base = %q, want master", got)
	}
}

func TestCreateWorktreeFromExplicitRemoteTagAndSHA(t *testing.T) {
	repo := setupGitRepo(t, "master")
	masterSHA := gitOutput(t, repo, "rev-parse", "master")
	gitRun(t, repo, "tag", "base-tag", masterSHA)
	withWorkspaceEnv(t, repo)

	cases := []struct {
		name string
		base string
	}{
		{name: "remote-base", base: "origin/master"},
		{name: "tag-base", base: "base-tag"},
		{name: "sha-base", base: masterSHA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := createWorktree("feature", tc.name, tc.base, repo, false); err != nil {
				t.Fatalf("createWorktree: %v", err)
			}
			assertWorktreeHEAD(t, "feature", tc.name, masterSHA)
		})
	}
}

func TestCreateWorktreeDefaultUsesSelectedRepoOriginHEAD(t *testing.T) {
	repo := setupGitRepo(t, "master")
	masterSHA := gitOutput(t, repo, "rev-parse", "master")
	gitRun(t, repo, "switch", "-c", "other")
	writeAndCommit(t, repo, "other.txt", "other\n", "other")
	withWorkspaceEnv(t, repo)

	if err := createWorktree("feature", "default-base", "", repo, false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}
	assertWorktreeHEAD(t, "feature", "default-base", masterSHA)

	stack, _ := internal.LoadStack(internal.FeaturePath("feature"))
	if got := internal.GetBranch(stack, "default-base").Base; got != "master" {
		t.Fatalf("base = %q, want master", got)
	}
}

func TestCreateWorktreeInvalidBaseDoesNotWriteMetadata(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	err := createWorktree("feature", "bad-base", "does-not-exist", repo, false)
	if err == nil {
		t.Fatal("expected invalid base error")
	}
	if _, statErr := os.Stat(internal.WorktreePath("feature", "bad-base")); !os.IsNotExist(statErr) {
		t.Fatalf("worktree unexpectedly exists: %v", statErr)
	}
	if stack, loadErr := internal.LoadStack(internal.FeaturePath("feature")); loadErr == nil && internal.HasBranch(stack, "bad-base") {
		t.Fatal("invalid branch was written to stack")
	}
}

func TestCreateWorktreeInfersRepoFromFeatureDirectory(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	if err := createWorktree("feature", "first", "master", repo, false); err != nil {
		t.Fatalf("first worktree: %v", err)
	}
	featurePath := internal.FeaturePath("feature")
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(featurePath); err != nil {
		t.Fatal(err)
	}

	if err := createWorktree("feature", "second", "master", "", false); err != nil {
		t.Fatalf("feature-dir createWorktree: %v", err)
	}
	assertWorktreeHEAD(t, "feature", "second", gitOutput(t, repo, "rev-parse", "master"))
}

func TestResolveSourceRepoRejectsAmbiguousFeature(t *testing.T) {
	repoA := setupGitRepo(t, "master")
	repoB := setupGitRepo(t, "main")
	featurePath := t.TempDir()
	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "a", Base: "master", Repo: repoA},
		{Name: "b", Base: "main", Repo: repoB},
	}}

	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(featurePath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveSourceRepo(featurePath, stack, ""); err == nil || !strings.Contains(err.Error(), "multiple repositories") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
}

func setupGitRepo(t *testing.T, defaultBranch string) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	gitRun(t, "", "init", "--bare", remote)
	gitRun(t, "", "init", "-b", defaultBranch, repo)
	gitRun(t, repo, "config", "user.name", "TWS Test")
	gitRun(t, repo, "config", "user.email", "tws@example.test")
	writeAndCommit(t, repo, "README.md", "base\n", "initial")
	gitRun(t, repo, "remote", "add", "origin", remote)
	gitRun(t, repo, "push", "-u", "origin", defaultBranch)
	gitRun(t, remote, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)
	gitRun(t, repo, "remote", "set-head", "origin", "-a")
	return repo
}

func withWorkspaceEnv(t *testing.T, repo string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TWS_ROOT", filepath.Join(t.TempDir(), "workspace"))
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
}

func assertWorktreeHEAD(t *testing.T, feature, name, want string) {
	t.Helper()
	got := gitOutput(t, internal.WorktreePath(feature, name), "rev-parse", "HEAD")
	if got != want {
		t.Fatalf("worktree HEAD = %s, want %s", got, want)
	}
}

func writeAndCommit(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", name)
	gitRun(t, repo, "commit", "-m", message)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
