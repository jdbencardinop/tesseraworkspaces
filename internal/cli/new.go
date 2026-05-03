package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func New(args []string) {
	if len(args) < 2 {
		println("Usage: tws new <feature> <branch> [--base <parent>] [--force]")
		return
	}

	internal.RequireTool("git")

	feature := args[0]
	branch := args[1]
	base := "main"
	force := false

	// Parse flags
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--base":
			if i+1 < len(args) {
				base = args[i+1]
				i++
			}
		case "--force", "-f":
			force = true
		}
	}

	featurePath := internal.FeaturePath(feature)
	path := internal.WorktreePath(feature, branch)

	// Ensure parent directory exists
	internal.Must(os.MkdirAll(featurePath, 0755))

	// Create the worktree at the feature-scoped path
	repoRoot, err := internal.MainRepoRoot()
	if err != nil {
		fmt.Println("Error: must be run from inside a git repository")
		os.Exit(1)
	}

	if internal.BranchExists(branch) {
		// Check if it's currently checked out somewhere
		if isCheckedOut(branch) && !force {
			fmt.Printf("Warning: branch %q is already checked out in another worktree.\n", branch)
			fmt.Println("Use --force to check it out anyway.")
			os.Exit(1)
		}

		gitArgs := []string{"worktree", "add"}
		if force {
			gitArgs = append(gitArgs, "--force")
		}
		gitArgs = append(gitArgs, path, branch)
		internal.Must(internal.RunDir(repoRoot, "git", gitArgs...))
	} else {
		// New branch
		internal.Must(internal.RunDir(repoRoot, "git", "worktree", "add", path, "-b", branch))
	}

	// Register branch in stack.yaml (idempotent — skip if already tracked)
	stack, _ := internal.LoadStack(featurePath)
	if !internal.HasBranch(stack, branch) {
		stack.Branches = append(stack.Branches, internal.StackEntry{Name: branch, Base: base})
		internal.Must(internal.SaveStack(featurePath, stack))
	}

	fmt.Printf("Worktree created: %s (base: %s)\n", path, base)
}

// isCheckedOut checks if a branch is currently checked out in any worktree.
func isCheckedOut(branch string) bool {
	out, err := exec.Command("git", "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "branch refs/heads/"+branch {
			return true
		}
	}
	return false
}
