package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func archiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "archive <feature> <branch>",
		Short: "Remove worktree from disk, keep branch ref",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			internal.RequireTool("git")

			feature := args[0]
			branch := args[1]

			featurePath := internal.FeaturePath(feature)

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

			err = internal.Run("git", "worktree", "remove", path)
			if err != nil {
				err = internal.Run("git", "worktree", "remove", "--force", path)
				if err != nil {
					fmt.Printf("Error removing worktree: %v\n", err)
					os.Exit(1)
				}
			}

			_ = internal.RunSilent("git", "worktree", "prune")

			fmt.Printf("Archived: %s (branch preserved, restore with: tws new %s %s)\n", branch, feature, branch)
		},
	}
}
