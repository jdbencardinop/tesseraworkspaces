package internal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type HealthIssue struct {
	Branch   string
	Problem  string
	Hint     string
	Severity CheckoutSeverity // "" is treated as a warning
}

// EffectiveSeverity resolves the zero value to a warning, so every producer
// that predates the Severity field renders and counts exactly as before.
func (h HealthIssue) EffectiveSeverity() CheckoutSeverity {
	if h.Severity == "" {
		return SeverityWarning
	}
	return h.Severity
}

func (h HealthIssue) String() string {
	s := fmt.Sprintf("  %s %s: %s", severityIcon(h.EffectiveSeverity()), h.Branch, h.Problem)
	if h.Hint != "" {
		s += fmt.Sprintf("\n      %s", h.Hint)
	}
	return s
}

// CountHealthIssues counts only issues that need attention. Informational
// findings print but never change a total or an exit status.
func CountHealthIssues(issues []HealthIssue) int {
	n := 0
	for _, issue := range issues {
		switch issue.EffectiveSeverity() {
		case SeverityWarning, SeverityError:
			n++
		}
	}
	return n
}

// AncestryHealthIssues projects evaluated stack edges into external doctor
// issues. Repository-unavailable edges collapse to a single feature-scoped
// issue so an unresolvable repository cannot flood the output. Per-edge notes
// are projected too — including for `current` edges, whose base a sync path
// may still resolve differently — always as informational and never counted.
func AncestryHealthIssues(res StackRepoResolution, edges []StackEdge) []HealthIssue {
	var issues []HealthIssue
	repoUnavailableReported := false
	for _, edge := range edges {
		if edge.Status != AncestryStatusCurrent {
			problem := fmt.Sprintf("ancestry %s: %s", ancestryDisplayStatus(edge.Status), edge.Reason)
			if edge.Reason == ReasonRepoUnavailable {
				if repoUnavailableReported {
					continue
				}
				repoUnavailableReported = true
				issues = append(issues, HealthIssue{
					Branch:   "stack",
					Problem:  problem,
					Hint:     edge.Guidance,
					Severity: edge.Severity,
				})
				continue
			}
			issues = append(issues, HealthIssue{
				Branch:   edge.Name,
				Problem:  problem,
				Hint:     edge.Guidance,
				Severity: edge.Severity,
			})
		}
		for _, note := range edge.Notes {
			issues = append(issues, HealthIssue{
				Branch:   edge.Name,
				Problem:  fmt.Sprintf("ancestry note: %s", note.Kind),
				Hint:     note.Detail,
				Severity: SeverityInfo,
			})
		}
	}
	if res.Alternate != "" {
		issues = append(issues, HealthIssue{
			Branch: "stack",
			Problem: fmt.Sprintf("%s: ancestry evaluated against %s (source: %s)",
				RepoSourceMismatchLabel, ancestrySanitize(res.RepoDir, ancestryPathLimit), res.Source),
			Hint: fmt.Sprintf("the workspace also resolves to %s; check TWS_ROOT or the configured workspace path",
				ancestrySanitize(res.Alternate, ancestryPathLimit)),
			Severity: SeverityInfo,
		})
	}
	return issues
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
// Respects the configured inject_into target path.
func CheckWorktreeInjectLinks(featurePath, worktreePath, branch string) *HealthIssue {
	injectDir := InjectPath(featurePath)
	if _, err := os.Stat(injectDir); os.IsNotExist(err) {
		return nil // no inject dir, nothing to check
	}

	// Resolve inject target (respects inject_into config)
	targetBase := worktreePath
	injectInto := ResolveInjectInto("")
	if injectInto != "" && injectInto != "." {
		targetBase = filepath.Join(worktreePath, injectInto)
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
		destPath := filepath.Join(targetBase, relPath)
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

		if issue := CheckWorktreeBranch(wtPath, entry.GitBranch()); issue != nil {
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
