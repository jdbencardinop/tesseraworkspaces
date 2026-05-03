package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func Delete(args []string) {
	if len(args) < 1 {
		println("Usage: tws delete <feature>")
		return
	}

	internal.RequireTool("git")

	feature := args[0]
	featurePath := internal.FeaturePath(feature)

	if _, err := os.Stat(featurePath); os.IsNotExist(err) {
		fmt.Printf("Feature not found: %s\n", feature)
		os.Exit(1)
	}

	// Remove worktrees tracked in stack.yaml
	stack, err := internal.LoadStack(featurePath)
	if err == nil {
		for _, entry := range stack.Branches {
			path := internal.WorktreePath(feature, entry.Name)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Printf("  Worktree already removed: %s\n", entry.Name)
				continue
			}
			err := internal.Run("git", "worktree", "remove", "--force", path)
			if err != nil {
				fmt.Printf("  Warning: failed to remove worktree %s: %v\n", entry.Name, err)
			} else {
				fmt.Printf("  Removed worktree: %s\n", entry.Name)
			}
		}
	}

	// Prune stale worktree refs
	_ = internal.Run("git", "worktree", "prune")

	// Remove the feature directory
	if err := os.RemoveAll(featurePath); err != nil {
		fmt.Printf("Error removing feature directory: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Feature deleted: %s (branches preserved in git)\n", feature)
}
