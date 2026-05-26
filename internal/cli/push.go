package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func pushCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "push <feature>",
		Short: "Push all branches in a feature with --force-with-lease",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			internal.RequireTool("git")
			pushFeature(args[0], dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be pushed without pushing")

	return cmd
}

func pushFeature(feature string, dryRun bool) {
	featurePath := internal.FeaturePath(feature)

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		fmt.Printf("No stack.yaml found for feature: %s\n", feature)
		os.Exit(1)
	}

	for _, entry := range stack.Branches {
		path := internal.WorktreePath(feature, entry.Name)

		// Skip archived branches
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Printf("  [-] %s (archived, skipped)\n", entry.Name)
			continue
		}

		if dryRun {
			fmt.Printf("  [~] %s (would push --force-with-lease)\n", entry.Name)
			continue
		}

		// Determine repo context
		repoDir := path
		if entry.Repo != "" {
			repoDir = entry.Repo
		}

		err := internal.RunDirClean(repoDir, "git", "push", "--force-with-lease", "origin", entry.Name)
		if err != nil {
			fmt.Printf("  [x] %s (push failed)\n", entry.Name)
		} else {
			fmt.Printf("  [+] %s (pushed)\n", entry.Name)
		}
	}
}
