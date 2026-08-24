package internal

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func RequireTool(name string) {
	if _, err := exec.LookPath(name); err != nil {
		fmt.Printf("Error: required tool %q not found in PATH\n", name)
		os.Exit(1)
	}
}

func MainRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return MainRepoRootIn(cwd)
}

// MainRepoRootIn returns the primary repository root for a repo or linked worktree.
func MainRepoRootIn(path string) (string, error) {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}
	gitDir = filepath.Clean(gitDir)

	if filepath.Base(gitDir) == ".git" {
		return filepath.Dir(gitDir), nil
	}
	return filepath.Dir(gitDir), nil
}

func RepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func BranchExists(branch string) bool {
	err := exec.Command("git", "rev-parse", "--verify", branch).Run()
	return err == nil
}

// DefaultBranch returns the current repository's default branch name.
// Deprecated for repo-aware flows; prefer DefaultBranchIn.
func DefaultBranch() string {
	branch, err := DefaultBranchIn("")
	if err != nil {
		return "main"
	}
	return branch
}

// DefaultBranchIn returns the selected repository's default branch name.
// It prefers origin/HEAD, then the repository's initial branch, and finally main.
func DefaultBranchIn(repoPath string) (string, error) {
	args := []string{"rev-parse", "--abbrev-ref", "origin/HEAD"}
	if repoPath != "" {
		args = append([]string{"-C", repoPath}, args...)
	}
	if out, err := exec.Command("git", args...).Output(); err == nil {
		branch := strings.TrimSpace(string(out))
		return strings.TrimPrefix(branch, "origin/"), nil
	}

	args = []string{"symbolic-ref", "--short", "HEAD"}
	if repoPath != "" {
		args = append([]string{"-C", repoPath}, args...)
	}
	if out, err := exec.Command("git", args...).Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	return "main", nil
}

// VerifyGitRef checks that ref resolves to a commit in the selected repository.
func VerifyGitRef(repoPath, ref string) error {
	args := []string{"rev-parse", "--verify", ref + "^{commit}"}
	if repoPath != "" {
		args = append([]string{"-C", repoPath}, args...)
	}
	return exec.Command("git", args...).Run()
}

// GitRepoRootIn validates repoPath and returns its top-level directory.
func GitRepoRootIn(repoPath string) (string, error) {
	args := []string{"rev-parse", "--show-toplevel"}
	if repoPath != "" {
		args = append([]string{"-C", repoPath}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// AbsPath returns the absolute path, resolving relative paths.
func AbsPath(path string) (string, error) {
	return filepath.Abs(path)
}

func Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

func RunDir(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Dir = dir

	return cmd.Run()
}

// RunTo is Run with stdout/stderr taken from the caller instead of hard-wired
// to os.Stdout/os.Stderr. It is otherwise byte-identical to Run.
func RunTo(stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// RunDirTo is RunDir with stdout/stderr taken from the caller instead of
// hard-wired to os.Stdout/os.Stderr. It is otherwise byte-identical to RunDir.
func RunDirTo(stdout, stderr io.Writer, dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	cmd.Dir = dir

	return cmd.Run()
}

// RunDirClean runs a command in a directory, filtering git hint/warning noise
// from stderr. Actual errors are still shown.
func RunDirClean(dir string, name string, args ...string) error {
	return runWithFilteredStderr(dir, name, args...)
}

func runWithFilteredStderr(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Dir = dir

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// Read stderr and filter
	buf := make([]byte, 4096)
	for {
		n, readErr := stderr.Read(buf)
		if n > 0 {
			lines := strings.Split(string(buf[:n]), "\n")
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}
				// Filter git hint and advice lines
				if strings.HasPrefix(trimmed, "hint:") ||
					strings.HasPrefix(trimmed, "Disable this message") {
					continue
				}
				// Reformat cherry-pick skip warnings
				if strings.Contains(trimmed, "skipped previously applied commit") {
					fmt.Fprintf(os.Stderr, "    (skipped duplicate commit)\n")
					continue
				}
				fmt.Fprintln(os.Stderr, line)
			}
		}
		if readErr != nil {
			break
		}
	}

	return cmd.Wait()
}

func RunSilent(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

func RunSilentDir(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Run()
}

// IsPrunableWorktree checks if a branch has a stale (prunable) worktree entry,
// meaning the directory was deleted but git still tracks it.
func IsPrunableWorktree(branch string) bool {
	out, err := exec.Command("git", "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "branch refs/heads/"+branch {
			// Check surrounding lines in the same worktree block for "prunable"
			for j := i - 3; j <= i+2 && j < len(lines); j++ {
				if j < 0 {
					continue
				}
				if strings.HasPrefix(lines[j], "prunable") {
					return true
				}
			}
		}
	}
	return false
}

func Must(err error) {
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
