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
		RunE: func(cmd *cobra.Command, args []string) error {
			feature := args[0]
			featurePath, err := internal.RequireFeaturePath(feature)
			if err != nil {
				return err
			}

			injectDir := internal.InjectPath(featurePath)
			if _, err := os.Stat(injectDir); os.IsNotExist(err) {
				return fmt.Errorf("no inject/ directory found for feature: %s\nCreate it at: %s", feature, injectDir)
			}

			target := internal.ResolveInjectInto(into)

			if len(args) == 2 {
				branch := args[1]
				path, err := internal.RequireWorktreePath(feature, branch)
				if err != nil {
					return err
				}
				if _, err := os.Stat(path); os.IsNotExist(err) {
					return fmt.Errorf("worktree not found: %s/%s", feature, branch)
				}
				if err := internal.InjectFiles(featurePath, path, target); err != nil {
					return err
				}
				if target != "" && target != "." {
					fmt.Printf("Injected files into: %s/%s/%s\n", feature, branch, target)
				} else {
					fmt.Printf("Injected files into: %s/%s\n", feature, branch)
				}
			} else {
				count, err := internal.InjectFilesForFeature(featurePath, target)
				if err != nil {
					return err
				}
				fmt.Printf("Injected files into %d worktree(s)\n", count)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&into, "into", "", "Target subdirectory within worktree (default: root, or inject_into from config)")

	return cmd
}
