package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func deleteCmd() *cobra.Command {
	var deleteBranches bool
	var forceDeleteBranches bool

	cmd := &cobra.Command{
		Use:     "delete <feature>",
		Aliases: []string{"rm"},
		Short:   "Remove feature and all worktrees",
		Args:    cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			feature := args[0]

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			if ws.Mode == internal.ModeCheckout {
				return deleteCheckout(ws, feature, deleteBranches, forceDeleteBranches)
			}
			return deleteExternal(feature, deleteBranches, forceDeleteBranches)
		},
	}

	cmd.Flags().BoolVar(&deleteBranches, "delete-branches", false, "Also delete git branches (uses safe -d; unmerged branches are skipped)")
	cmd.Flags().BoolVar(&forceDeleteBranches, "force-delete-branches", false, "Force-delete git branches even if unmerged (uses -D)")

	return cmd
}

// deleteCheckout deletes a feature in checkout mode.
// --delete-branches validates all targets before deleting any.
// Refuses to delete the currently checked-out branch.
func deleteCheckout(ws internal.Workspace, feature string, deleteBranches, forceDelete bool) error {
	featurePath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return err
	}

	if _, err := os.Stat(featurePath); os.IsNotExist(err) {
		return fmt.Errorf("feature not found: %s", feature)
	}

	stack, _ := internal.LoadStack(featurePath)

	if deleteBranches || forceDelete {
		// Determine the current branch to refuse deletion.
		currentBranch := currentGitBranch(ws.RepoRoot)

		backups, err := validateCheckoutBranchDeletion(ws.RepoRoot, stack, currentBranch, forceDelete)
		if err != nil {
			return err
		}
		flag := "-d"
		if forceDelete {
			flag = "-D"
		}
		var deleted []branchBackup
		for _, backup := range backups {
			if err := internal.RunSilentDir(ws.RepoRoot, "git", "branch", flag, backup.name); err != nil {
				rollbackErr := restoreDeletedBranches(ws.RepoRoot, deleted)
				if rollbackErr != nil {
					return fmt.Errorf("failed to delete git branch %q: %w; rollback also failed: %v", backup.name, err, rollbackErr)
				}
				return fmt.Errorf("failed to delete git branch %q: %w; deleted branches restored and feature metadata preserved", backup.name, err)
			}
			deleted = append(deleted, backup)
		}
	}

	// Delete feature metadata.
	if err := os.RemoveAll(featurePath); err != nil {
		return fmt.Errorf("error removing feature directory: %w", err)
	}

	fmt.Printf("Deleted feature: %s\n", feature)
	return nil
}

type branchBackup struct {
	name string
	sha  string
}

func validateCheckoutBranchDeletion(repoRoot string, stack internal.Stack, currentBranch string, force bool) ([]branchBackup, error) {
	backups := make([]branchBackup, 0, len(stack.Branches))
	for _, entry := range stack.Branches {
		branch := entry.GitBranch()
		if branch == currentBranch {
			return nil, fmt.Errorf("cannot delete branch %q: it is the currently checked-out branch; switch to another branch first", branch)
		}
		shaBytes, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", branch+"^{commit}").Output()
		if err != nil {
			return nil, fmt.Errorf("cannot delete branch %q: ref does not resolve", branch)
		}
		if !force && internal.RunSilentDir(repoRoot, "git", "merge-base", "--is-ancestor", branch, "HEAD") != nil {
			return nil, fmt.Errorf("cannot safely delete unmerged branch %q; use --force-delete-branches", branch)
		}
		backups = append(backups, branchBackup{name: branch, sha: strings.TrimSpace(string(shaBytes))})
	}
	return backups, nil
}

func restoreDeletedBranches(repoRoot string, backups []branchBackup) error {
	var failures []string
	for _, backup := range backups {
		if err := internal.RunSilentDir(repoRoot, "git", "branch", backup.name, backup.sha); err != nil {
			failures = append(failures, backup.name)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("could not restore branches: %s", strings.Join(failures, ", "))
	}
	return nil
}

// deleteExternal deletes a feature in external mode.
func deleteExternal(feature string, deleteBranches, forceDelete bool) error {
	featurePath := internal.FeaturePath(feature)

	if _, err := os.Stat(featurePath); os.IsNotExist(err) {
		return fmt.Errorf("feature not found: %s", feature)
	}

	stack, _ := internal.LoadStack(featurePath)

	if deleteBranches || forceDelete {
		// Pre-validate: check current branch.
		currentBranch := ""
		if repoRoot, err := internal.MainRepoRoot(); err == nil {
			currentBranch = currentGitBranch(repoRoot)
		}

		var toDelete []string
		for _, entry := range stack.Branches {
			gitBranch := entry.GitBranch()
			if gitBranch == currentBranch {
				return fmt.Errorf("cannot delete branch %q: it is the currently checked-out branch; switch to another branch first", gitBranch)
			}
			toDelete = append(toDelete, gitBranch)
		}

		flag := "-d"
		if forceDelete {
			flag = "-D"
		}
		for _, branch := range toDelete {
			if err := internal.RunSilent("git", "branch", flag, branch); err != nil {
				return fmt.Errorf("failed to delete git branch %q: %w; feature metadata preserved", branch, err)
			}
		}
	}

	// Remove worktrees
	for _, entry := range stack.Branches {
		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); err == nil {
			_ = internal.Run("git", "worktree", "remove", "--force", path)
		}
	}

	if err := os.RemoveAll(featurePath); err != nil {
		return fmt.Errorf("error removing feature directory: %w", err)
	}

	fmt.Printf("Deleted feature: %s\n", feature)
	return nil
}

// currentGitBranch returns the current branch name for the given repo root.
func currentGitBranch(repoRoot string) string {
	out, err := exec.Command("git", "-C", repoRoot, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
