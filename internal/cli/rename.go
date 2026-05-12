package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func renameCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rename",
		Short: "Rename a feature or branch",
	}

	cmd.AddCommand(renameFeatureCmd())
	cmd.AddCommand(renameBranchCmd())

	return cmd
}

func renameFeatureCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "feature <old-name> <new-name>",
		Short: "Rename a feature",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			oldName := args[0]
			newName := args[1]

			oldPath := internal.FeaturePath(oldName)
			newPath := internal.FeaturePath(newName)

			if _, err := os.Stat(oldPath); os.IsNotExist(err) {
				fmt.Printf("Feature not found: %s\n", oldName)
				os.Exit(1)
			}

			if _, err := os.Stat(newPath); err == nil {
				fmt.Printf("Feature already exists: %s\n", newName)
				os.Exit(1)
			}

			if err := os.Rename(oldPath, newPath); err != nil {
				fmt.Printf("Error renaming: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("Renamed feature: %s → %s\n", oldName, newName)
		},
	}
}

func renameBranchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "branch <feature> <old-name> <new-name>",
		Short: "Rename a branch within a feature",
		Args:  cobra.ExactArgs(3),
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
			internal.RequireTool("git")

			feature := args[0]
			oldName := args[1]
			newName := args[2]

			featurePath := internal.FeaturePath(feature)

			// Update stack.yaml
			stack, err := internal.LoadStack(featurePath)
			if err != nil {
				fmt.Printf("No stack.yaml found for feature: %s\n", feature)
				os.Exit(1)
			}

			if !internal.RenameBranch(&stack, oldName, newName) {
				fmt.Printf("Branch %q not found in stack\n", oldName)
				os.Exit(1)
			}

			internal.Must(internal.SaveStack(featurePath, stack))

			// Rename worktree directory if active
			oldPath := internal.WorktreePath(feature, oldName)
			newPath := internal.WorktreePath(feature, newName)

			if _, err := os.Stat(oldPath); err == nil {
				// Remove worktree, rename git branch, re-add
				internal.Must(internal.Run("git", "worktree", "remove", "--force", oldPath))
				internal.Must(internal.Run("git", "branch", "-m", oldName, newName))
				internal.Must(internal.Run("git", "worktree", "add", newPath, newName))
			} else {
				// Archived — just rename the git branch
				internal.Must(internal.Run("git", "branch", "-m", oldName, newName))
			}

			// Kill stale tmux session
			oldSession := sanitizeSessionName(feature + "/" + oldName)
			if sessionExists(oldSession) {
				_ = internal.RunSilent("tmux", "kill-session", "-t", oldSession)
			}

			// Update decisions.yaml branch references
			decisions, err := internal.LoadDecisions(featurePath)
			if err == nil {
				for i := range decisions.Entries {
					if decisions.Entries[i].Branch == oldName {
						decisions.Entries[i].Branch = newName
					}
				}
				_ = internal.SaveDecisions(featurePath, decisions)
			}

			fmt.Printf("Renamed branch: %s → %s (in feature %s)\n", oldName, newName, feature)
		},
	}
}
