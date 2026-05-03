package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func Archive(args []string) {
	if len(args) < 2 {
		println("Usage: tws archive <feature> <branch>")
		return
	}

	internal.RequireTool("git")

	feature := args[0]
	branch := args[1]

	featurePath := internal.FeaturePath(feature)

	// Verify branch is in the stack
	stack, err := internal.LoadStack(featurePath)
	if err != nil || !internal.HasBranch(stack, branch) {
		fmt.Printf("Branch %q not found in feature %q stack\n", branch, feature)
		os.Exit(1)
	}

	path := internal.WorktreePath(feature, branch)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("Already archived: %s\n", branch)
		return
	}

	// Remove the worktree
	err = internal.Run("git", "worktree", "remove", path)
	if err != nil {
		// Try force remove if dirty
		err = internal.Run("git", "worktree", "remove", "--force", path)
		if err != nil {
			fmt.Printf("Error removing worktree: %v\n", err)
			os.Exit(1)
		}
	}

	// Prune stale refs
	_ = internal.RunSilent("git", "worktree", "prune")

	fmt.Printf("Archived: %s (branch preserved, restore with: tws new %s %s)\n", branch, feature, branch)
}
