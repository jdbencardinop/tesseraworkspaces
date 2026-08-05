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
		RunE: func(cmd *cobra.Command, args []string) error {
			internal.RequireTool("git")
			return pushFeature(args[0], dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be pushed without pushing")

	return cmd
}

func pushFeature(feature string, dryRun bool) error {
	featurePath, err := internal.RequireFeaturePath(feature)
	if err != nil {
		return err
	}

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return fmt.Errorf("no stack.yaml found for feature: %s", feature)
	}

	for _, entry := range stack.Branches {
		path, err := internal.RequireWorktreePath(feature, entry.Name)
		if err != nil {
			return err
		}

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

		runErr := internal.RunDirClean(repoDir, "git", "push", "--force-with-lease", "origin", entry.Name)
		if runErr != nil {
			fmt.Printf("  [x] %s (push failed)\n", entry.Name)
		} else {
			fmt.Printf("  [+] %s (pushed)\n", entry.Name)
		}
	}
	return nil
}
