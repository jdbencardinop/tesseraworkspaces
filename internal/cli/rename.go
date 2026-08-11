package cli

import (
	"errors"
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
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			oldName := args[0]
			newName := args[1]

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			root := ws.MetadataRoot

			var oldPath, newPath string
			oldPath, err = ws.ResolveFeaturePath(oldName)
			if err != nil {
				return err
			}
			newPath = ws.FeaturePath(newName)

			if _, err := os.Stat(oldPath); os.IsNotExist(err) {
				return fmt.Errorf("feature not found: %s", oldName)
			}

			// One locked read validates both names and stages the registry
			// rewrite. It runs before the destination-collision check so a
			// registered space directory reports the space conflict rather
			// than "feature already exists". With no spaces.yaml this is a
			// true no-op: no lock file, no registry file, nothing created.
			tx, err := internal.BeginSpacesFeatureRename(root, oldName, newName, oldPath, newPath)
			if err != nil {
				return err
			}
			defer func() { retErr = errors.Join(retErr, tx.Release()) }()

			if _, err := os.Stat(newPath); err == nil {
				return fmt.Errorf("feature already exists: %s", newName)
			}

			// External direct session records must not silently relocate: a
			// whole-directory rename would move every record so its recorded
			// feature and its <branch-id> hash disagree with its location.
			// Checkout mode never writes or reads them.
			if ws.Mode == internal.ModeExternal {
				targets, terr := directRecordTargetsForFeature(oldPath)
				if terr != nil {
					return terr
				}
				if gerr := guardDirectRecords(cmd.OutOrStdout(), oldPath, "rename feature", oldName, targets); gerr != nil {
					return gerr
				}
			}

			if err := os.Rename(oldPath, newPath); err != nil {
				return fmt.Errorf("error renaming: %w", err)
			}

			if commitErr := tx.Commit(); commitErr != nil {
				if rollbackErr := os.Rename(newPath, oldPath); rollbackErr != nil {
					return errors.Join(commitErr, fmt.Errorf(
						"rollback failed: feature directory is now %s on disk while %s still refers to %q; repair with 'tws space remove' and 'tws space add': %w",
						newPath, internal.SpacesFilePath(root), oldName, rollbackErr))
				}
				return commitErr
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
	if err := internal.GuardFeatureName(internal.TwsRoot(), feature); err != nil {
		return err
	}

	featurePath := internal.FeaturePath(feature)

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return fmt.Errorf("no stack.yaml found for feature: %s", feature)
	}

	if !internal.HasBranch(stack, oldName) {
		return fmt.Errorf("branch %q not found in stack", oldName)
	}

	// Renaming a branch changes <branch-id>, name, git_branch, and the
	// worktree path, so no record may survive the rewrite.
	if gerr := guardDirectRecords(os.Stdout, featurePath, "rename branch", feature+"/"+oldName,
		directRecordTargetForBranch(feature, oldName)); gerr != nil {
		return gerr
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
		// Reconstruct from featurePath directly
		path := featurePath + "/worktrees/" + entry.Name
		if _, err := os.Stat(path); err == nil {
			return path
		}
		if entry.Repo != "" {
			return entry.Repo
		}
	}

	// Fallback: try cwd
	if internal.BranchExists("HEAD") {
		cwd, _ := os.Getwd()
		return cwd
	}

	return ""
}
