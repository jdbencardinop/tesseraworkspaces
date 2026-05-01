package internal

import (
	"fmt"
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
	// git-common-dir returns the .git dir of the main repo even from a worktree.
	// In a non-worktree checkout it returns ".git" (relative).
	out, err := exec.Command("git", "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))

	if gitDir == ".git" {
		// Not inside a worktree — fall back to normal detection.
		return RepoRoot()
	}

	// gitDir is absolute (e.g. /path/to/repo/.git), resolve to parent.
	return filepath.Dir(filepath.Clean(gitDir)), nil
}

func RepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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

func Must(err error) {
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
