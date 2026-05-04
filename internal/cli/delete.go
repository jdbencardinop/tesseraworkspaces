package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func deleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <feature>",
		Aliases: []string{"rm"},
		Short:   "Remove feature and all worktrees",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			internal.RequireTool("git")

			feature := args[0]
			featurePath := internal.FeaturePath(feature)

			if _, err := os.Stat(featurePath); os.IsNotExist(err) {
				fmt.Printf("Feature not found: %s\n", feature)
				os.Exit(1)
			}

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

			_ = internal.Run("git", "worktree", "prune")

			if err := os.RemoveAll(featurePath); err != nil {
				fmt.Printf("Error removing feature directory: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("Feature deleted: %s (branches preserved in git)\n", feature)
		},
	}
}
