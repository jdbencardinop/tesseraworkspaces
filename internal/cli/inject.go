package cli

import (
	"errors"
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
		Long: `Re-sync shared files from inject/ into external linked worktrees.
With no branch, syncs every materialized worktree discovered for the feature,
including logical names that contain slashes. Checkout mode is unsupported
because it has one physical checkout and no linked worktrees.

Injected files are symlinked into the worktree. By default they go to
the worktree root. Use --into to target a subdirectory (e.g., --into .context).
For managed worktree guidance, root injection is required: an explicit
--into . overrides a configured non-root inject_into for this invocation,
while an explicit non-root --into is refused.

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
				return managedCompletionFeatures(), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return managedCompletionBranches(args[0]), cobra.ShellCompDirectiveNoFileComp
			default:
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			feature := args[0]
			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			ws, err = managedTemplateWorkspaceForFeature(ws, feature)
			if err != nil {
				return err
			}
			featurePath, err := internal.ResolveFeaturePathFor(ws, feature)
			if err != nil {
				return err
			}
			if ws.Mode == internal.ModeCheckout {
				return internal.ErrWorktreeUnsupported
			}

			injectDir := internal.InjectPath(featurePath)
			if _, err := os.Stat(injectDir); os.IsNotExist(err) {
				return fmt.Errorf("no inject/ directory found for feature: %s\nCreate it at: %s", feature, injectDir)
			}

			managed, err := internal.ManagedTemplateStatePresent(featurePath)
			if err != nil {
				return err
			}
			target := internal.ResolveInjectInto(into)
			managedTarget := managedInjectTarget{Value: target}
			if managed {
				cfg, err := resolveManagedConfig(ws)
				if err != nil {
					return err
				}
				managedTarget.Value = cfg.Config.InjectInto
				managedTarget.Explicit = cmd.Flags().Changed("into")
				if managedTarget.Explicit {
					managedTarget.Value = into
				}
				target = managedTarget.Value
			}

			if len(args) == 2 {
				branch := args[1]
				path, err := internal.ResolveInjectWorktree(featurePath, branch)
				if errors.Is(err, internal.ErrInjectWorktreeNotFound) {
					return fmt.Errorf("worktree not found: %s/%s", feature, branch)
				}
				if err != nil {
					return err
				}
				if managed {
					if err := syncManagedAndLegacyInject(
						cmd, ws, feature, featurePath, branch, path, managedTarget,
					); err != nil {
						return err
					}
				} else if err := internal.InjectFiles(featurePath, path, target); err != nil {
					return err
				}
				if target != "" && target != "." {
					fmt.Printf("Injected files into: %s/%s/%s\n", feature, branch, target)
				} else {
					fmt.Printf("Injected files into: %s/%s\n", feature, branch)
				}
			} else {
				var count int
				if managed {
					count, err = syncManagedAndLegacyInjectAll(
						cmd, ws, feature, featurePath, managedTarget,
					)
				} else {
					count, err = internal.InjectFilesForFeature(featurePath, target)
				}
				if err != nil {
					return err
				}
				fmt.Printf("Injected files into %d worktree(s)\n", count)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&into, "into", "", "Target subdirectory; managed guidance accepts only root, and --into . overrides configured inject_into")

	return cmd
}
