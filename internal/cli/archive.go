package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func archiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "archive <feature> <branch>",
		Short: "Remove worktree from disk, keep branch and metadata",
		Args:  cobra.ExactArgs(2),
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
			branch := args[1]

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			if ws.Mode == internal.ModeCheckout {
				return archiveCheckout(ws, feature, branch)
			}
			return archiveExternal(feature, branch)
		},
	}
}

// archiveCheckout marks a branch as archived in metadata only.
// The git branch is preserved; no worktree to remove.
func archiveCheckout(ws internal.Workspace, feature, branch string) error {
	if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return err
	}

	featurePath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return err
	}

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return fmt.Errorf("no stack.yaml found for feature: %s", feature)
	}

	found := false
	for i := range stack.Branches {
		if stack.Branches[i].Name == branch {
			if stack.Branches[i].Archived {
				fmt.Printf("Branch %s is already archived\n", branch)
				return nil
			}
			stack.Branches[i].Archived = true
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("branch %q not found in stack for feature %s", branch, feature)
	}

	if err := internal.SaveStack(featurePath, stack); err != nil {
		return fmt.Errorf("error saving stack: %w", err)
	}

	fmt.Printf("Archived branch: %s (git branch preserved)\n", branch)
	return nil
}

// archiveExternal removes the worktree from disk but preserves the git branch.
func archiveExternal(feature, branch string) error {
	if err := internal.GuardFeatureName(internal.TwsRoot(), feature); err != nil {
		return err
	}

	featurePath := internal.FeaturePath(feature)
	path := internal.WorktreePath(feature, branch)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Mark as archived in stack if not already.
		stack, loadErr := internal.LoadStack(featurePath)
		if loadErr == nil {
			for i := range stack.Branches {
				if stack.Branches[i].Name == branch {
					stack.Branches[i].Archived = true
					break
				}
			}
			_ = internal.SaveStack(featurePath, stack)
		}
		fmt.Printf("Worktree already removed: %s\n", path)
		return nil
	}

	// Use git worktree remove to cleanly detach.
	if err := internal.Run("git", "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("error removing worktree: %w", err)
	}

	// Mark as archived in stack.
	stack, loadErr := internal.LoadStack(featurePath)
	if loadErr == nil {
		for i := range stack.Branches {
			if stack.Branches[i].Name == branch {
				stack.Branches[i].Archived = true
				break
			}
		}
		_ = internal.SaveStack(featurePath, stack)
	}

	// Kill tmux session if running.
	session := sanitizeSessionName(feature + "/" + branch)
	if sessionExists(session) {
		_ = internal.RunSilent("tmux", "kill-session", "-t", session)
	}

	fmt.Printf("Archived worktree: %s → %s (branch preserved)\n", feature, branch)
	return nil
}
