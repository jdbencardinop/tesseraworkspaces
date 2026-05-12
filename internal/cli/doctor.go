package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [feature]",
		Short: "Run health checks on workspaces",
		Long:  "Check branch consistency, uncommitted changes, and inject symlinks. With no args, checks all features.",
		Args:  cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 1 {
				checkFeature(args[0])
				return
			}

			// Check all features
			features := internal.ListFeatures()
			if len(features) == 0 {
				fmt.Println("No features found.")
				return
			}

			totalIssues := 0
			for _, feature := range features {
				issues := checkFeature(feature)
				totalIssues += issues
			}

			if totalIssues == 0 {
				fmt.Println("\nAll healthy.")
			} else {
				fmt.Printf("\n%d issue(s) found.\n", totalIssues)
			}
		},
	}
}

func checkFeature(feature string) int {
	featurePath := internal.FeaturePath(feature)

	if _, err := os.Stat(featurePath); os.IsNotExist(err) {
		fmt.Printf("%s: not found\n", feature)
		return 0
	}

	issues := internal.CheckFeatureHealth(featurePath)

	if len(issues) == 0 {
		// Count active worktrees
		wtDir := filepath.Join(featurePath, "worktrees")
		entries, _ := os.ReadDir(wtDir)
		active := 0
		for _, e := range entries {
			if e.IsDir() {
				active++
			}
		}
		fmt.Printf("%s: healthy (%d active worktree(s))\n", feature, active)
	} else {
		fmt.Printf("%s: %d issue(s)\n", feature, len(issues))
		for _, issue := range issues {
			fmt.Println(issue)
		}
	}

	return len(issues)
}
