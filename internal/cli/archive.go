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
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 0:
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return internal.ListBranches(args[0]), cobra.ShellCompDirectiveNoFileComp
			default:
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		},
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

			// Find the repo for this branch (for cross-repo worktrees)
			entry := internal.GetBranch(stack, branch)
			repoDir := entry.Repo
			if repoDir == "" {
				// Default repo — use the worktree itself as git context
				repoDir = path
			}

			err = internal.RunDir(repoDir, "git", "worktree", "remove", path)
			if err != nil {
				err = internal.RunDir(repoDir, "git", "worktree", "remove", "--force", path)
				if err != nil {
					fmt.Printf("Error removing worktree: %v\n", err)
					os.Exit(1)
				}
			}

			_ = internal.RunSilentDir(repoDir, "git", "worktree", "prune")

			fmt.Printf("Archived: %s (branch preserved, restore with: tws new %s %s)\n", branch, feature, branch)
		},
	}
}
