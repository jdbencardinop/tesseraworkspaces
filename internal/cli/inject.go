package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func injectCmd() *cobra.Command {
	var into string

	cmd := &cobra.Command{
		Use:   "inject <feature> [branch]",
		Short: "Sync inject/ files into worktrees",
		Long: `Re-sync shared files from inject/ into worktrees. With no branch, syncs all worktrees.

Injected files are symlinked into the worktree. By default they go to
the worktree root. Use --into to target a subdirectory (e.g., --into .context).

Set inject_into in config to change the default:
  tws config set inject_into .context
  tws config set inject_into .context --repo

Tip: To keep git status clean, either:
  - Add injected filenames to .gitignore (e.g., CLAUDE.local.md)
  - Use --into to target a gitignored subfolder (e.g., --into .context)
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

			target := internal.ResolveInjectInto(into)

			if len(args) == 2 {
				branch := args[1]
				path := internal.WorktreePath(feature, branch)
				if _, err := os.Stat(path); os.IsNotExist(err) {
					fmt.Printf("Worktree not found: %s/%s\n", feature, branch)
					os.Exit(1)
				}
				if err := internal.InjectFiles(featurePath, path, target); err != nil {
					fmt.Printf("Error: %v\n", err)
					os.Exit(1)
				}
				if target != "" && target != "." {
					fmt.Printf("Injected files into: %s/%s/%s\n", feature, branch, target)
				} else {
					fmt.Printf("Injected files into: %s/%s\n", feature, branch)
				}
			} else {
				count, err := internal.InjectFilesForFeature(featurePath, target)
				if err != nil {
					fmt.Printf("Error: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Injected files into %d worktree(s)\n", count)
			}
		},
	}

	cmd.Flags().StringVar(&into, "into", "", "Target subdirectory within worktree (default: root, or inject_into from config)")

	return cmd
}
