package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func syncCmd() *cobra.Command {
	var verbose bool
	var push bool

	cmd := &cobra.Command{
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
			syncFeature(args[0], verbose)
			if push {
				fmt.Println()
				fmt.Println("Pushing...")
				pushFeature(args[0], false)
			}
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show full git fetch output")
	cmd.Flags().BoolVar(&push, "push", false, "Push all branches after syncing")

	return cmd
}

func syncFeature(feature string, verbose bool) {
	featurePath := internal.FeaturePath(feature)

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		fetchQuiet("", "", verbose)
		syncFallback(featurePath)
		return
	}

	// Fetch per-repo
	repos := internal.UniqueRepos(stack, featurePath)
	for repo, wtPath := range repos {
		fetchQuiet(repo, wtPath, verbose)
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	syncWithStack(feature, featurePath, stack, sorted)
}

func fetchQuiet(repo, wtPath string, verbose bool) {
	label := "default repo"
	if repo != "" {
		label = repo
	}

	if verbose {
		fmt.Printf("Fetching %s...\n", label)
		if wtPath != "" {
			_ = internal.RunDir(wtPath, "git", "fetch")
		} else if repo != "" {
			_ = internal.RunDir(repo, "git", "fetch")
		} else {
			_ = internal.Run("git", "fetch")
		}
	} else {
		fmt.Printf("Fetching %s... ", label)
		var err error
		if wtPath != "" {
			err = internal.RunSilentDir(wtPath, "git", "fetch")
		} else if repo != "" {
			err = internal.RunSilentDir(repo, "git", "fetch")
		} else {
			err = internal.RunSilent("git", "fetch")
		}
		if err != nil {
			fmt.Println("failed")
		} else {
			fmt.Println("done")
		}
	}
}
