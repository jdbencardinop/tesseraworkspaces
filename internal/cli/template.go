package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func templateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage workspace templates",
	}

	cmd.AddCommand(templateSyncCmd())

	return cmd
}

func templateSyncCmd() *cobra.Command {
	var templates []string
	var all bool

	cmd := &cobra.Command{
		Use:   "sync [feature]",
		Short: "Apply template files to existing feature(s)",
		Long:  "Copy template files into a feature's inject/ directory. Skips existing files. Use --all to sync all features.",
		Args:  cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			if all {
				features := internal.ListFeatures()
				if len(features) == 0 {
					fmt.Println("No features found.")
					return
				}
				for _, feature := range features {
					syncFeatureTemplate(feature, templates)
				}
				return
			}

			if len(args) == 0 {
				fmt.Println("Usage: tws template sync <feature> [--template <dir>]")
				fmt.Println("       tws template sync --all")
				os.Exit(1)
			}

			syncFeatureTemplate(args[0], templates)
		},
	}

	cmd.Flags().StringArrayVar(&templates, "template", nil, "Template directory (can be specified multiple times)")
	cmd.Flags().BoolVar(&all, "all", false, "Sync all features")

	return cmd
}

func syncFeatureTemplate(feature string, extraTemplates []string) {
	featurePath := internal.FeaturePath(feature)

	if _, err := os.Stat(featurePath); os.IsNotExist(err) {
		fmt.Printf("%s: not found\n", feature)
		return
	}

	injectDir := internal.InjectPath(featurePath)
	if err := os.MkdirAll(injectDir, 0755); err != nil {
		fmt.Printf("%s: error creating inject dir: %v\n", feature, err)
		return
	}

	fmt.Printf("%s:\n", feature)
	applyTemplates(injectDir, extraTemplates)

	// Re-sync inject into worktrees
	target := internal.ResolveInjectInto("")
	count, err := internal.InjectFilesForFeature(featurePath, target)
	if err != nil {
		fmt.Printf("  Warning: worktree inject failed: %v\n", err)
	} else if count > 0 {
		fmt.Printf("  Synced inject to %d worktree(s)\n", count)
	}
}
