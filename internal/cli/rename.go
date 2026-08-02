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
		RunE: func(cmd *cobra.Command, args []string) error {
			oldName := args[0]
			newName := args[1]

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			var oldPath, newPath string
			if ws.Mode == internal.ModeCheckout {
				oldPath = ws.FeaturePath(oldName)
				newPath = ws.FeaturePath(newName)
			} else {
				oldPath = internal.FeaturePath(oldName)
				newPath = internal.FeaturePath(newName)
			}

			if _, err := os.Stat(oldPath); os.IsNotExist(err) {
				return fmt.Errorf("feature not found: %s", oldName)
			}

			if _, err := os.Stat(newPath); err == nil {
				return fmt.Errorf("feature already exists: %s", newName)
			}

			if err := os.Rename(oldPath, newPath); err != nil {
				return fmt.Errorf("error renaming: %w", err)
			}

			fmt.Printf("Renamed feature: %s → %s\n", oldName, newName)
			return nil
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
		RunE: func(cmd *cobra.Command, args []string) error {
			internal.RequireTool("git")

			feature := args[0]
			oldName := args[1]
			newName := args[2]

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			if ws.Mode == internal.ModeCheckout {
				return renameBranchCheckout(ws, feature, oldName, newName)
			}
			return renameBranchExternal(feature, oldName, newName)
		},
	}
}

// renameBranchCheckout renames a branch in checkout mode.
// Uses entry.GitBranch() for git operations (honoring prefixes).
// Short Name is the metadata identity; git branch may have a prefix.
// Metadata is updated only after git succeeds; rolled back if SaveStack fails.
func renameBranchCheckout(ws internal.Workspace, feature, oldName, newName string) error {
	featurePath := ws.FeaturePath(feature)

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return fmt.Errorf("no stack.yaml found for feature: %s", feature)
	}

	entry := internal.GetBranch(stack, oldName)
	if entry.Name == "" {
		return fmt.Errorf("branch %q not found in stack", oldName)
	}

	oldGitBranch := entry.GitBranch()

	// Compute new git branch name: preserve prefix structure.
	cfg := internal.LoadConfig()
	newGitBranch := newName
	if cfg.BranchPrefix != "" {
		newGitBranch = cfg.BranchPrefix + newName
	}

	// Git rename using GitBranch() (the actual branch name, possibly prefixed).
	if err := internal.RunDir(ws.RepoRoot, "git", "branch", "-m", oldGitBranch, newGitBranch); err != nil {
		return fmt.Errorf("error renaming git branch %s -> %s: %w", oldGitBranch, newGitBranch, err)
	}

	// Now mutate metadata (git succeeded).
	internal.RenameBranch(&stack, oldName, newName)
	// Update Branch field if the new git branch differs from the new name.
	for i := range stack.Branches {
		if stack.Branches[i].Name == newName {
			if newGitBranch != newName {
				stack.Branches[i].Branch = newGitBranch
			} else {
				stack.Branches[i].Branch = ""
			}
			break
		}
	}

	if err := internal.SaveStack(featurePath, stack); err != nil {
		if rollbackErr := internal.RunSilentDir(ws.RepoRoot, "git", "branch", "-m", newGitBranch, oldGitBranch); rollbackErr != nil {
			return fmt.Errorf("error saving stack: %w; rollback of git branch also failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("error saving stack (git branch rolled back): %w", err)
	}

	// Update decisions.yaml branch references.
	decisions, loadErr := internal.LoadDecisions(featurePath)
	if loadErr == nil {
		for i := range decisions.Entries {
			if decisions.Entries[i].Branch == oldName {
				decisions.Entries[i].Branch = newName
			}
		}
		_ = internal.SaveDecisions(featurePath, decisions)
	}

	fmt.Printf("Renamed branch: %s → %s (git: %s → %s, in feature %s)\n", oldName, newName, oldGitBranch, newGitBranch, feature)
	return nil
}

// renameBranchExternal renames a branch in external mode.
// Uses entry.GitBranch() for git operations.
func renameBranchExternal(feature, oldName, newName string) error {
	featurePath := internal.FeaturePath(feature)

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return fmt.Errorf("no stack.yaml found for feature: %s", feature)
	}

	if !internal.HasBranch(stack, oldName) {
		return fmt.Errorf("branch %q not found in stack", oldName)
	}

	entry := internal.GetBranch(stack, oldName)
	oldGitBranch := entry.GitBranch()

	// Compute new git branch name preserving prefix.
	cfg := internal.LoadConfig()
	newGitBranch := newName
	if cfg.BranchPrefix != "" {
		newGitBranch = cfg.BranchPrefix + newName
	}

	gitDir := resolveGitDir(featurePath, stack)
	if gitDir == "" {
		return fmt.Errorf("could not find a git context; run from inside the repo or ensure at least one active worktree exists")
	}

	oldPath := internal.WorktreePath(feature, oldName)
	newPath := internal.WorktreePath(feature, newName)

	if _, err := os.Stat(oldPath); err == nil {
		// Active worktree: remove, rename branch, re-add
		if err := internal.RunDir(gitDir, "git", "worktree", "remove", "--force", oldPath); err != nil {
			return fmt.Errorf("error removing worktree: %w", err)
		}
		if err := internal.RunDir(gitDir, "git", "branch", "-m", oldGitBranch, newGitBranch); err != nil {
			_ = internal.RunDir(gitDir, "git", "worktree", "add", oldPath, oldGitBranch)
			return fmt.Errorf("error renaming git branch: %w", err)
		}
		if err := internal.RunDir(gitDir, "git", "worktree", "add", newPath, newGitBranch); err != nil {
			return fmt.Errorf("error creating new worktree: %w", err)
		}

		target := internal.ResolveInjectInto("")
		if injectErr := internal.InjectFiles(featurePath, newPath, target); injectErr != nil {
			fmt.Printf("Warning: inject failed: %v\n", injectErr)
		}
	} else {
		// Archived: just rename the git branch
		if err := internal.RunDir(gitDir, "git", "branch", "-m", oldGitBranch, newGitBranch); err != nil {
			return fmt.Errorf("error renaming git branch: %w", err)
		}
	}

	// Now mutate metadata (git succeeded).
	internal.RenameBranch(&stack, oldName, newName)
	for i := range stack.Branches {
		if stack.Branches[i].Name == newName {
			if newGitBranch != newName {
				stack.Branches[i].Branch = newGitBranch
			} else {
				stack.Branches[i].Branch = ""
			}
			break
		}
	}

	if err := internal.SaveStack(featurePath, stack); err != nil {
		return fmt.Errorf("error saving stack: %w", err)
	}

	// Kill stale tmux session
	oldSession := sanitizeSessionName(feature + "/" + oldName)
	if sessionExists(oldSession) {
		_ = internal.RunSilent("tmux", "kill-session", "-t", oldSession)
	}

	// Update decisions.yaml branch references
	decisions, loadErr := internal.LoadDecisions(featurePath)
	if loadErr == nil {
		for i := range decisions.Entries {
			if decisions.Entries[i].Branch == oldName {
				decisions.Entries[i].Branch = newName
			}
		}
		_ = internal.SaveDecisions(featurePath, decisions)
	}

	fmt.Printf("Renamed branch: %s → %s (git: %s → %s, in feature %s)\n", oldName, newName, oldGitBranch, newGitBranch, feature)
	return nil
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
