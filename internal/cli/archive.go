package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
				return managedCompletionFeatures(), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return managedCompletionBranches(args[0]), cobra.ShellCompDirectiveNoFileComp
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
func archiveCheckout(ws internal.Workspace, feature, branch string) (retErr error) {
	if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return err
	}

	featurePath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return err
	}
	lifecycle, err := internal.AcquireManagedFeatureLifecycleLock(featurePath)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lifecycle.Release()) }()
	managed, err := internal.ManagedTemplateStatePresent(featurePath)
	if err != nil {
		return err
	}
	reservation, err := acquireManagedFeatureRenameReservation(ws, feature, featurePath, managed)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, reservation.Release()) }()
	if managed {
		cfg, err := internal.LoadConfigForRepoE(ws.RepoRoot)
		if err != nil {
			return err
		}
		if err := internal.ValidateManagedCheckoutContextResolved(
			ws, feature, featurePath, cfg.GetAgentCommand(), cfg.InjectInto, false,
		); err != nil {
			return err
		}
		if err := internal.ValidateManagedOrchestratorContext(
			ws, feature, featurePath, cfg.GetAgentCommand(),
		); err != nil {
			return err
		}
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
	root := internal.TwsRoot()
	if err := internal.GuardFeatureName(root, feature); err != nil {
		return err
	}
	featurePath := filepath.Join(root, feature)
	_, rawErr := os.Lstat(featurePath)
	rawExists := rawErr == nil
	if rawErr != nil && !errors.Is(rawErr, os.ErrNotExist) {
		return rawErr
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		return err
	}
	if rawExists {
		managed, err := internal.ManagedTemplateStatePresent(featurePath)
		if err != nil {
			return err
		}
		if !managed {
			ws.Mode = internal.ModeExternal
			ws.MetadataRoot = root
			return archiveExternalAt(ws, feature, featurePath, branch)
		}
	}
	context, err := resolveFeatureContext(ws, feature)
	if err != nil {
		return err
	}
	return archiveExternalAt(context.Workspace, feature, context.FeaturePath, branch)
}

func archiveExternalAt(
	ws internal.Workspace,
	feature, featurePath, branch string,
) (retErr error) {
	if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return err
	}
	lifecycle, err := internal.AcquireManagedFeatureLifecycleLock(featurePath)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lifecycle.Release()) }()
	managed, err := internal.ManagedTemplateStatePresent(featurePath)
	if err != nil {
		return err
	}
	reservation, err := acquireManagedFeatureRenameReservation(ws, feature, featurePath, managed)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, reservation.Release()) }()
	if managed {
		cfg, err := internal.LoadConfigForRepoE(ws.RepoRoot)
		if err != nil {
			return err
		}
		if err := internal.ValidateManagedCheckoutContextResolved(
			ws, feature, featurePath, cfg.GetAgentCommand(), cfg.InjectInto, false,
		); err != nil {
			return err
		}
		if err := internal.ValidateManagedOrchestratorContext(
			ws, feature, featurePath, cfg.GetAgentCommand(),
		); err != nil {
			return err
		}
	}
	path := filepath.Join(featurePath, "worktrees", filepath.FromSlash(branch))
	if residue, err := internal.ManagedTemplateRecoveryResidue(path); err != nil {
		return err
	} else if residue != "" {
		return fmt.Errorf("managed template recovery evidence exists at %s; resolve it before archiving the branch", residue)
	}

	// Archiving removes the worktree a live session's path points at, i.e.
	// the agent's own working directory. The guard precedes the
	// worktree-absent branch below, which also mutates stack.yaml.
	if gerr := guardDirectRecords(os.Stdout, featurePath, "archive", feature+"/"+branch,
		directRecordTargetForBranch(feature, branch)); gerr != nil {
		return gerr
	}
	var retirement *preparedManagedWorktreeRetirement
	if managed {
		retirement, err = prepareManagedWorktreeLinkRetirement(
			ws, feature, featurePath, branch,
		)
		if err != nil {
			return fmt.Errorf("preparing managed link ownership retirement for archive: %w", err)
		}
	}

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
	repoRoot := ws.RepoRoot
	if stack, loadErr := internal.LoadStack(featurePath); loadErr == nil {
		if entry := internal.GetBranch(stack, branch); entry.Name != "" {
			resolved, resolveErr := resolveBranchRenameRepo(ws, entry, path, true)
			if resolveErr != nil {
				return resolveErr
			}
			repoRoot = resolved
		}
	}
	if err := internal.RunDir(repoRoot, "git", "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("error removing worktree: %w", err)
	}
	if err := retirement.publishAfterRemoval(); err != nil {
		return fmt.Errorf("publishing managed link ownership retirement after archive removal: %w", err)
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
