package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func New(args []string) {
	if len(args) < 2 {
		println("Usage: tws new <feature> <branch> [--base <parent>]")
		return
	}

	internal.RequireTool("git")

	feature := args[0]
	branch := args[1]
	base := "main"

	// Parse --base flag
	for i := 2; i < len(args); i++ {
		if args[i] == "--base" && i+1 < len(args) {
			base = args[i+1]
			break
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

	internal.Must(internal.RunDir(repoRoot, "git", "worktree", "add", path, "-b", branch))

	// Register branch in stack.yaml
	stack, _ := internal.LoadStack(featurePath)
	stack.Branches = append(stack.Branches, internal.StackEntry{Name: branch, Base: base})
	internal.Must(internal.SaveStack(featurePath, stack))

	fmt.Printf("Worktree created: %s (base: %s)\n", path, base)
}
