package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func syncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync <feature>",
		Short: "Rebase worktrees in dependency order",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			internal.RequireTool("git")
			syncFeature(args[0])
		},
	}
}

func syncFeature(feature string) {
	featurePath := internal.FeaturePath(feature)

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		// No stack — single fetch + fallback
		internal.Must(internal.Run("git", "fetch"))
		syncFallback(featurePath)
		return
	}

	// Fetch per-repo: find an active worktree for each repo to use as git context
	repos := internal.UniqueRepos(stack, featurePath)
	for repo, wtPath := range repos {
		if wtPath == "" {
			// No active worktree for this repo — try fetching from cwd
			if repo == "" {
				fmt.Println("Fetching default repo...")
				_ = internal.Run("git", "fetch")
			} else {
				fmt.Printf("Fetching %s...\n", repo)
				_ = internal.RunDir(repo, "git", "fetch")
			}
		} else {
			if repo == "" {
				fmt.Println("Fetching default repo...")
			} else {
				fmt.Printf("Fetching %s...\n", repo)
			}
			_ = internal.RunDir(wtPath, "git", "fetch")
		}
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	syncWithStack(feature, featurePath, stack, sorted)
}
