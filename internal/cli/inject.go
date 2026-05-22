package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func injectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inject <feature> [branch]",
		Short: "Sync inject/ files into worktrees",
		Long: `Re-sync shared files from inject/ into worktrees. With no branch, syncs all worktrees.

Injected files are symlinked into the worktree root. Since these are
not tracked by git, they will appear as untracked files in git status.

Tip: To keep git status clean, either:
  - Add injected filenames to .gitignore (e.g., CLAUDE.local.md)
  - Place files inside inject/ in a gitignored subfolder
    (e.g., inject/.context/file.md → worktree/.context/file.md)
  - Or nest inside an already-ignored dir like inject/.claude/`,
		Args: cobra.RangeArgs(1, 2),
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
			feature := args[0]
			featurePath := internal.FeaturePath(feature)

			if _, err := os.Stat(featurePath); os.IsNotExist(err) {
				fmt.Printf("Feature not found: %s\n", feature)
				os.Exit(1)
			}

			injectDir := internal.InjectPath(featurePath)
			if _, err := os.Stat(injectDir); os.IsNotExist(err) {
				fmt.Printf("No inject/ directory found for feature: %s\n", feature)
				fmt.Printf("Create it at: %s\n", injectDir)
				os.Exit(1)
			}

			if len(args) == 2 {
				// Single branch
				branch := args[1]
				path := internal.WorktreePath(feature, branch)
				if _, err := os.Stat(path); os.IsNotExist(err) {
					fmt.Printf("Worktree not found: %s/%s\n", feature, branch)
					os.Exit(1)
				}
				if err := internal.InjectFiles(featurePath, path); err != nil {
					fmt.Printf("Error: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Injected files into: %s/%s\n", feature, branch)
			} else {
				// All worktrees
				count, err := internal.InjectFilesForFeature(featurePath)
				if err != nil {
					fmt.Printf("Error: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Injected files into %d worktree(s)\n", count)
			}
		},
	}
}
