package internal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "--initial-branch=main")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return dir
}

func TestEnableCheckoutMode_CreatesLayout(t *testing.T) {
	dir := initGitRepo(t)

	if err := EnableCheckoutMode(dir); err != nil {
		t.Fatal(err)
	}

	// Verify directories.
	for _, sub := range []string{".tws", ".tws/features", ".tws/state"} {
		path := filepath.Join(dir, sub)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected %s to exist: %v", sub, err)
		} else if !info.IsDir() {
			t.Errorf("expected %s to be a directory", sub)
		}
	}

	// Verify config.
	data, err := os.ReadFile(filepath.Join(dir, ".tws", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "workspace_mode: checkout") {
		t.Errorf("config missing workspace_mode: checkout, got: %s", data)
	}

	// Verify git exclude.
	exclude, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exclude), ".tws/") {
		t.Errorf("exclude missing .tws/, got: %s", exclude)
	}
}

func TestEnableCheckoutMode_Idempotent(t *testing.T) {
	dir := initGitRepo(t)

	// Enable twice.
	if err := EnableCheckoutMode(dir); err != nil {
		t.Fatal(err)
	}
	if err := EnableCheckoutMode(dir); err != nil {
		t.Fatal(err)
	}

	// Exclude should not be duplicated.
	data, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	count := strings.Count(string(data), ".tws/")
	if count != 1 {
		t.Errorf("expected exactly 1 .tws/ in exclude, got %d", count)
	}
}

func TestEnableCheckoutMode_PreservesConfig(t *testing.T) {
	dir := initGitRepo(t)

	// Write config with branch_prefix first.
	twsDir := filepath.Join(dir, ".tws")
	if err := os.MkdirAll(twsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(twsDir, "config.yaml"), []byte("branch_prefix: ws/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := EnableCheckoutMode(dir); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(twsDir, "config.yaml"))
	if !strings.Contains(string(data), "branch_prefix: ws/") {
		t.Errorf("branch_prefix not preserved: %s", data)
	}
	if !strings.Contains(string(data), "workspace_mode: checkout") {
		t.Errorf("workspace_mode not set: %s", data)
	}
}

func TestEnableCheckoutMode_RejectsLinkedWorktree(t *testing.T) {
	dir := initGitRepo(t)

	// Create initial commit so we can add worktrees.
	cmd := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir,
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t.com")
	if err := cmd.Run(); err != nil {
		t.Skipf("git commit failed: %v", err)
	}

	wtDir := filepath.Join(t.TempDir(), "wt")
	cmd = exec.Command("git", "-C", dir, "worktree", "add", wtDir, "-b", "wt-branch")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git worktree add: %v", err)
	}

	err := EnableCheckoutMode(wtDir)
	if err == nil {
		t.Fatal("expected error for linked worktree")
	}
	if !strings.Contains(err.Error(), "linked worktree") {
		t.Errorf("expected linked worktree error, got: %v", err)
	}
}

func TestEnableCheckoutMode_SubdirResolvesRoot(t *testing.T) {
	dir := initGitRepo(t)

	// Create a subdir and try to enable from the resolved repo root.
	subdir := filepath.Join(dir, "sub", "deep")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	// MainRepoRootIn should resolve to dir.
	root, err := MainRepoRootIn(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnableCheckoutMode(root); err != nil {
		t.Fatal(err)
	}

	// Verify it was created at the repo root, not the subdir.
	if _, err := os.Stat(filepath.Join(dir, ".tws", "features")); err != nil {
		t.Errorf("expected .tws/features at repo root: %v", err)
	}
}

func TestEnableExternalMode_DoesNotCreateCheckoutDirs(t *testing.T) {
	dir := initGitRepo(t)

	if err := EnableExternalMode(dir); err != nil {
		t.Fatal(err)
	}

	// features/ and state/ should NOT exist for external mode.
	if _, err := os.Stat(filepath.Join(dir, ".tws", "features")); err == nil {
		t.Error("external mode should not create .tws/features")
	}
	if _, err := os.Stat(filepath.Join(dir, ".tws", "state")); err == nil {
		t.Error("external mode should not create .tws/state")
	}
}

func TestAddGitLocalExclude_IdempotentMultipleCalls(t *testing.T) {
	dir := initGitRepo(t)

	for i := 0; i < 3; i++ {
		if err := AddGitLocalExclude(dir, ".tws/"); err != nil {
			t.Fatal(err)
		}
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	count := strings.Count(string(data), ".tws/")
	if count != 1 {
		t.Errorf("expected 1 occurrence of .tws/, got %d in: %s", count, data)
	}
}
