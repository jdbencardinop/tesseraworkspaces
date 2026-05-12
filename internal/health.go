package internal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type HealthIssue struct {
	Branch  string
	Problem string
	Hint    string
}

func (h HealthIssue) String() string {
	s := fmt.Sprintf("  [!] %s: %s", h.Branch, h.Problem)
	if h.Hint != "" {
		s += fmt.Sprintf("\n      %s", h.Hint)
	}
	return s
}

// CheckWorktreeBranch verifies the worktree is on the expected branch.
func CheckWorktreeBranch(worktreePath, expectedBranch string) *HealthIssue {
	out, err := exec.Command("git", "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return &HealthIssue{
			Branch:  expectedBranch,
			Problem: "could not determine current branch",
			Hint:    fmt.Sprintf("check worktree at %s", worktreePath),
		}
	}

	actual := strings.TrimSpace(string(out))
	if actual != expectedBranch {
		return &HealthIssue{
			Branch:  expectedBranch,
			Problem: fmt.Sprintf("on branch %q, expected %q", actual, expectedBranch),
			Hint:    fmt.Sprintf("run: cd %s && git checkout %s", worktreePath, expectedBranch),
		}
	}
	return nil
}

// CheckWorktreeDirty checks if the worktree has uncommitted changes.
func CheckWorktreeDirty(worktreePath, branch string) *HealthIssue {
	out, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain").Output()
	if err != nil {
		return nil
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		return &HealthIssue{
			Branch:  branch,
			Problem: "has uncommitted changes",
			Hint:    "commit or stash changes before syncing",
		}
	}
	return nil
}

// CheckWorktreeInjectLinks checks if inject symlinks are present.
func CheckWorktreeInjectLinks(featurePath, worktreePath, branch string) *HealthIssue {
	injectDir := InjectPath(featurePath)
	if _, err := os.Stat(injectDir); os.IsNotExist(err) {
		return nil // no inject dir, nothing to check
	}

	missing := 0
	_ = filepath.Walk(injectDir, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(injectDir, srcPath)
		if err != nil {
			return nil
		}
		destPath := filepath.Join(worktreePath, relPath)
		if _, err := os.Lstat(destPath); os.IsNotExist(err) {
			missing++
		}
		return nil
	})

	if missing > 0 {
		return &HealthIssue{
			Branch:  branch,
			Problem: fmt.Sprintf("%d inject file(s) missing", missing),
			Hint:    fmt.Sprintf("run: tws inject <feature> %s", branch),
		}
	}
	return nil
}

// CheckFeatureHealth runs all health checks for a feature and returns issues.
func CheckFeatureHealth(featurePath string) []HealthIssue {
	var issues []HealthIssue

	stack, err := LoadStack(featurePath)
	if err != nil {
		return issues
	}

	for _, entry := range stack.Branches {
		wtPath := filepath.Join(featurePath, "worktrees", entry.Name)
		if _, err := os.Stat(wtPath); os.IsNotExist(err) {
			continue // archived, skip
		}

		if issue := CheckWorktreeBranch(wtPath, entry.Name); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := CheckWorktreeDirty(wtPath, entry.Name); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := CheckWorktreeInjectLinks(featurePath, wtPath, entry.Name); issue != nil {
			issues = append(issues, *issue)
		}
	}

	return issues
}
