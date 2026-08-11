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
		Long: `Check workspace and stack health. With no args, checks all features.

Warnings such as a dirty checkout, active Git operation, stale ancestry, or
recoverable lock/session state are reported with exit 0 for interactive use.
Corrupt or unreadable persisted state returns a non-zero exit status.`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Checkout mode dispatch
			ws, wsErr := internal.RequireWorkspace()
			if wsErr == nil && ws.Mode == internal.ModeCheckout {
				feature := ""
				if len(args) == 1 {
					feature = args[0]
				}
				return runCheckoutDoctor(ws, feature)
			}

			if len(args) == 1 {
				_, err := checkFeatureE(args[0])
				return err
			}

			// Check all features
			features, listErr := internal.ListFeaturesE()
			if listErr != nil {
				return listErr
			}
			if len(features) == 0 {
				fmt.Println("No features found.")
				return nil
			}

			totalIssues := 0
			for _, feature := range features {
				issues, err := checkFeatureE(feature)
				if err != nil {
					return err
				}
				totalIssues += issues
			}

			if totalIssues == 0 {
				fmt.Println("\nAll healthy.")
			} else {
				fmt.Printf("\n%d issue(s) found.\n", totalIssues)
			}
			return nil
		},
	}
}

func checkFeatureE(feature string) (int, error) {
	featurePath, err := internal.RequireFeaturePath(feature)
	if err != nil {
		return 0, err
	}

	if _, err := os.Stat(featurePath); os.IsNotExist(err) {
		fmt.Printf("%s: not found\n", feature)
		return 0, nil
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

	return len(issues), nil
}

func runCheckoutDoctor(ws internal.Workspace, feature string) error {
	report, err := internal.BuildCheckoutHealthReport(ws, nil)
	if err != nil {
		return err
	}
	if feature != "" {
		if err := report.FilterFeature(feature); err != nil {
			return err
		}
	}
	fmt.Print(internal.FormatCheckoutHealth(report))
	if report.HasErrors() {
		return fmt.Errorf("checkout workspace has corrupt or unreadable state")
	}
	return nil
}
