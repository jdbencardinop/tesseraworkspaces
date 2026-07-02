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

			// Load stack (don't mutate yet)
			stack, err := internal.LoadStack(featurePath)
			if err != nil {
				fmt.Printf("No stack.yaml found for feature: %s\n", feature)
				os.Exit(1)
			}

			if !internal.HasBranch(stack, oldName) {
				fmt.Printf("Branch %q not found in stack\n", oldName)
				os.Exit(1)
			}

			// Resolve git context: find an active worktree to run git from
			gitDir := resolveGitDir(featurePath, stack)
			if gitDir == "" {
				fmt.Println("Error: could not find a git context. Run from inside the repo or ensure at least one active worktree exists.")
				os.Exit(1)
			}

			// Step 1: Git operations FIRST (validate before mutating metadata)
			oldPath := internal.WorktreePath(feature, oldName)
			newPath := internal.WorktreePath(feature, newName)

			if _, err := os.Stat(oldPath); err == nil {
				// Active worktree: remove, rename branch, re-add
				if err := internal.RunDir(gitDir, "git", "worktree", "remove", "--force", oldPath); err != nil {
					fmt.Printf("Error removing worktree: %v\n", err)
					os.Exit(1)
				}
				if err := internal.RunDir(gitDir, "git", "branch", "-m", oldName, newName); err != nil {
					// Rollback: re-add old worktree
					_ = internal.RunDir(gitDir, "git", "worktree", "add", oldPath, oldName)
					fmt.Printf("Error renaming git branch: %v\n", err)
					os.Exit(1)
				}
				if err := internal.RunDir(gitDir, "git", "worktree", "add", newPath, newName); err != nil {
					fmt.Printf("Error creating new worktree: %v\n", err)
					os.Exit(1)
				}

				// Re-inject files into the new worktree
				target := internal.ResolveInjectInto("")
				if injectErr := internal.InjectFiles(featurePath, newPath, target); injectErr != nil {
					fmt.Printf("Warning: inject failed: %v\n", injectErr)
				}
			} else {
				// Archived: just rename the git branch
				if err := internal.RunDir(gitDir, "git", "branch", "-m", oldName, newName); err != nil {
					fmt.Printf("Error renaming git branch: %v\n", err)
					os.Exit(1)
				}
			}

			// Step 2: NOW mutate metadata (git succeeded)
			internal.RenameBranch(&stack, oldName, newName)
			internal.Must(internal.SaveStack(featurePath, stack))

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

// resolveGitDir finds a usable git directory from the feature's worktrees.
// Returns the path to an active worktree, or tries cwd as fallback.
func resolveGitDir(featurePath string, stack internal.Stack) string {
	// Try active worktrees first
	for _, entry := range stack.Branches {
		wtPath := internal.WorktreePath("", entry.Name)
		// Reconstruct from featurePath directly
		path := featurePath + "/worktrees/" + entry.Name
		if _, err := os.Stat(path); err == nil {
			return path
		}
		if entry.Repo != "" {
			return entry.Repo
		}
		_ = wtPath
	}

	// Fallback: try cwd
	if internal.BranchExists("HEAD") {
		cwd, _ := os.Getwd()
		return cwd
	}

	return ""
}
