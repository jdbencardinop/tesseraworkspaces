package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

var renameBranchExternalStepHook func(step string) error

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
			context, err := resolveFeatureContext(ws, oldName)
			if err != nil {
				return err
			}
			ws = context.Workspace
			root := ws.MetadataRoot

			oldPath := context.FeaturePath
			newPath := ws.FeaturePath(newName)

			if _, err := os.Stat(oldPath); os.IsNotExist(err) {
				return fmt.Errorf("feature not found: %s", oldName)
			}
			lifecycle, err := internal.AcquireManagedFeatureLifecycleLock(oldPath)
			if err != nil {
				return err
			}
			defer func() { retErr = errors.Join(retErr, lifecycle.Release()) }()
			managed, err := internal.ManagedTemplateStatePresent(oldPath)
			if err != nil {
				return err
			}
			managedReservation, err := acquireManagedFeatureRenameReservation(ws, oldName, oldPath, managed)
			if err != nil {
				return err
			}
			defer func() { retErr = errors.Join(retErr, managedReservation.Release()) }()

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
			managedReservation.Moved(newName, newPath)

			if commitErr := tx.Commit(); commitErr != nil {
				if rollbackErr := os.Rename(newPath, oldPath); rollbackErr != nil {
					return errors.Join(commitErr, fmt.Errorf(
						"rollback failed: feature directory is now %s on disk while %s still refers to %q; repair with 'tws space remove' and 'tws space add': %w",
						newPath, internal.SpacesFilePath(root), oldName, rollbackErr))
				}
				managedReservation.Moved(oldName, oldPath)
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
				return managedCompletionFeatures(), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return managedCompletionBranches(args[0]), cobra.ShellCompDirectiveNoFileComp
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
			context, err := resolveFeatureContext(ws, feature)
			if err != nil {
				return err
			}
			ws = context.Workspace

			if ws.Mode == internal.ModeCheckout {
				return renameBranchCheckoutAt(ws, feature, context.FeaturePath, oldName, newName)
			}
			return renameBranchExternalAt(ws, feature, context.FeaturePath, oldName, newName)
		},
	}
}

func renameBranchCheckout(ws internal.Workspace, feature, oldName, newName string) error {
	featurePath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return err
	}
	return renameBranchCheckoutAt(ws, feature, featurePath, oldName, newName)
}

// renameBranchCheckout renames a branch in checkout mode.
// Uses entry.GitBranch() for git operations (honoring prefixes).
// Short Name is the metadata identity; git branch may have a prefix.
// Metadata is updated only after git succeeds; rolled back if SaveStack fails.
func renameBranchCheckoutAt(ws internal.Workspace, feature, featurePath, oldName, newName string) (retErr error) {
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
	cfg := internal.LoadConfigForRepo(ws.RepoRoot)
	if managed {
		cfg, err = internal.LoadConfigForRepoE(ws.RepoRoot)
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

	entry := internal.GetBranch(stack, oldName)
	if entry.Name == "" {
		return fmt.Errorf("branch %q not found in stack", oldName)
	}

	oldGitBranch := entry.GitBranch()

	// Compute new git branch name: preserve prefix structure.
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

func renameBranchExternal(feature, oldName, newName string) error {
	ws, err := internal.RequireWorkspace()
	if err != nil {
		return err
	}
	context, err := resolveFeatureContext(ws, feature)
	if err != nil {
		return err
	}
	return renameBranchExternalAt(context.Workspace, feature, context.FeaturePath, oldName, newName)
}

// renameBranchExternal renames a branch in external mode.
// Uses entry.GitBranch() for git operations.
func renameBranchExternalAt(ws internal.Workspace, feature, featurePath, oldName, newName string) (retErr error) {
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
	cfg := internal.LoadConfigForRepo(ws.RepoRoot)
	if managed {
		cfg, err = internal.LoadConfigForRepoE(ws.RepoRoot)
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
	newGitBranch := newName
	if cfg.BranchPrefix != "" {
		newGitBranch = cfg.BranchPrefix + newName
	}

	oldPath := filepath.Join(featurePath, "worktrees", filepath.FromSlash(oldName))
	newPath := filepath.Join(featurePath, "worktrees", filepath.FromSlash(newName))
	materialized := false

	if _, err := os.Stat(oldPath); err == nil {
		if residue, err := internal.ManagedTemplateRecoveryResidue(oldPath); err != nil {
			return err
		} else if residue != "" {
			return fmt.Errorf("managed template recovery evidence exists at %s; resolve it before renaming the branch", residue)
		}
		materialized = true
		verifiedPath, resolveErr := internal.ResolveInjectWorktree(featurePath, oldName)
		if resolveErr != nil {
			return fmt.Errorf("resolving selected worktree %q: %w", oldName, resolveErr)
		}
		oldPath = verifiedPath
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking selected worktree %s: %w", oldPath, err)
	}
	targetRepo, err := resolveBranchRenameRepo(ws, entry, oldPath, materialized)
	if err != nil {
		return err
	}
	var oldBranchOID string
	if managed {
		if oldName != newName && internal.HasBranch(stack, newName) {
			return fmt.Errorf("destination branch name %q is already registered in stack", newName)
		}
		var oldExists bool
		var branchErr error
		oldBranchOID, oldExists, branchErr = localGitBranchOID(targetRepo, oldGitBranch)
		if branchErr != nil {
			return branchErr
		}
		if !oldExists {
			return fmt.Errorf("stored Git branch %q does not exist in %s", oldGitBranch, targetRepo)
		}
		if newGitBranch != oldGitBranch {
			if _, newExists, branchErr := localGitBranchOID(targetRepo, newGitBranch); branchErr != nil {
				return branchErr
			} else if newExists {
				return fmt.Errorf("destination Git branch %q already exists in %s", newGitBranch, targetRepo)
			}
		}
		if materialized {
			if overlap, overlapErr := managedRenamePathsOverlap(oldPath, newPath); overlapErr != nil {
				return overlapErr
			} else if overlap {
				return fmt.Errorf(
					"managed worktree rename paths overlap as ancestor/descendant paths: %s and %s",
					oldPath, newPath,
				)
			}
			if _, pathErr := os.Lstat(newPath); pathErr == nil {
				return fmt.Errorf("destination worktree path already exists: %s", newPath)
			} else if !errors.Is(pathErr, os.ErrNotExist) {
				return fmt.Errorf("checking destination worktree path %s: %w", newPath, pathErr)
			}
		}
	}
	var retirement *preparedManagedWorktreeRetirement
	if managed {
		retirement, err = prepareManagedWorktreeLinkRetirement(
			ws, feature, featurePath, oldName,
		)
		if err != nil {
			return fmt.Errorf("preparing managed link ownership retirement for branch rename: %w", err)
		}
	}
	var recoveryContext *managedRenameRecoveryContext
	if managed {
		recoveryContext, err = newManagedRenameRecoveryContext(
			featurePath, feature, targetRepo,
			oldName, newName, oldGitBranch, newGitBranch,
			oldPath, newPath, oldBranchOID,
		)
		if err != nil {
			return err
		}
	}
	stackCompletedStages := []string{"branch-renamed"}
	stackPendingStages := []string{"stack-publication"}
	if materialized {
		stackCompletedStages = []string{
			"old-worktree-removed", "branch-renamed",
			"new-worktree-created", "link-retirement",
		}
		stackPendingStages = []string{"stack-publication", "managed-reinjection"}
	}

	if materialized {
		// Active worktree: remove, rename branch, re-add
		if err := internal.RunDir(targetRepo, "git", "worktree", "remove", "--force", oldPath); err != nil {
			return fmt.Errorf("error removing worktree: %w", err)
		}
		if newGitBranch != oldGitBranch {
			if err := runRenameBranchExternalStep("before-branch-rename", func() error {
				return internal.RunDir(targetRepo, "git", "branch", "-m", oldGitBranch, newGitBranch)
			}); err != nil {
				if managed {
					rollbackErr := rollbackManagedMaterializedBranchRename(
						retirement, recoveryContext, targetRepo,
						oldPath, newPath, oldGitBranch, newGitBranch, oldBranchOID,
					)
					return errors.Join(fmt.Errorf("error renaming git branch: %w", err), rollbackErr)
				}
				restoreErr := internal.RunDir(targetRepo, "git", "worktree", "add", oldPath, oldGitBranch)
				return errors.Join(fmt.Errorf("error renaming git branch: %w", err),
					wrapRenameRollbackError("restoring original worktree", restoreErr))
			}
		}
		if err := runRenameBranchExternalStep("before-new-worktree-add", func() error {
			return internal.RunDir(targetRepo, "git", "worktree", "add", newPath, newGitBranch)
		}); err != nil {
			if managed {
				rollbackErr := rollbackManagedMaterializedBranchRename(
					retirement, recoveryContext, targetRepo,
					oldPath, newPath, oldGitBranch, newGitBranch, oldBranchOID,
				)
				return errors.Join(fmt.Errorf("error creating new worktree: %w", err), rollbackErr)
			}
			renameBackErr := internal.RunDir(targetRepo, "git", "branch", "-m", newGitBranch, oldGitBranch)
			restoreErr := internal.RunDir(targetRepo, "git", "worktree", "add", oldPath, oldGitBranch)
			return errors.Join(fmt.Errorf("error creating new worktree: %w", err),
				wrapRenameRollbackError("restoring original branch name", renameBackErr),
				wrapRenameRollbackError("restoring original worktree", restoreErr))
		}
		if err := retirement.publishAfterRemoval(); err != nil {
			if managed {
				return managedRenamePartialOutcome(
					recoveryContext, "retirement-publication",
					[]string{"old-worktree-removed", "branch-renamed", "new-worktree-created"},
					[]string{"link-retirement", "stack-publication", "managed-reinjection"},
					fmt.Errorf("publishing managed link ownership retirement after completed branch Git effects: %w", err),
				)
			}
			return fmt.Errorf("publishing managed link ownership retirement after completed branch Git effects: %w", err)
		}

	} else {
		// Archived: just rename the git branch
		if err := internal.RunDir(targetRepo, "git", "branch", "-m", oldGitBranch, newGitBranch); err != nil {
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

	if managed {
		if err := runRenameBranchExternalStep("before-stack-publication", func() error { return nil }); err != nil {
			return managedRenamePartialOutcome(
				recoveryContext, "stack-publication",
				stackCompletedStages, stackPendingStages,
				err,
			)
		}
		if err := internal.SaveStackAtomic(featurePath, stack); err != nil {
			return managedRenamePartialOutcome(
				recoveryContext, "stack-publication",
				stackCompletedStages, stackPendingStages,
				fmt.Errorf("error saving stack: %w", err),
			)
		}
	} else if err := internal.SaveStack(featurePath, stack); err != nil {
		return fmt.Errorf("error saving stack: %w", err)
	}
	if materialized {
		target := cfg.InjectInto
		managedPresent, managedErr := internal.ManagedTemplateStatePresent(featurePath)
		if managedErr != nil {
			if managed {
				return managedRenamePartialOutcome(
					recoveryContext, "managed-context-validation",
					[]string{"old-worktree-removed", "branch-renamed", "new-worktree-created", "link-retirement", "stack-publication"},
					[]string{"managed-reinjection"}, managedErr,
				)
			}
			return managedErr
		}
		if managed && !managedPresent {
			return managedRenamePartialOutcome(
				recoveryContext, "managed-context-validation",
				[]string{"old-worktree-removed", "branch-renamed", "new-worktree-created", "link-retirement", "stack-publication"},
				[]string{"managed-reinjection"},
				errors.New("managed ownership state disappeared after branch Git effects"),
			)
		}
		if managedPresent {
			if err := syncManagedAndLegacyInjectWithLifecycle(
				nil, ws, feature, featurePath, newName, newPath,
				managedInjectTarget{Value: target}, lifecycle, reservation,
			); err != nil {
				if managed {
					return managedRenamePartialOutcome(
						recoveryContext, "managed-reinjection",
						[]string{"old-worktree-removed", "branch-renamed", "new-worktree-created", "link-retirement", "stack-publication"},
						[]string{"managed-reinjection"},
						fmt.Errorf("managed inject failed after branch rename: %w", err),
					)
				}
				return fmt.Errorf("managed inject failed after branch rename: %w", err)
			}
		} else if injectErr := internal.InjectFiles(featurePath, newPath, target); injectErr != nil {
			fmt.Printf("Warning: inject failed: %v\n", injectErr)
		}
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

func resolveBranchRenameRepo(
	ws internal.Workspace,
	entry internal.StackEntry,
	materializedPath string,
	materialized bool,
) (string, error) {
	repoPath := ws.RepoRoot
	if entry.Repo != "" {
		repoPath = entry.Repo
	}
	targetRepo, err := internal.MainRepoRootIn(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolving repository for stack entry %q from %s: %w",
			entry.Name, repoPath, err)
	}
	if !materialized {
		return targetRepo, nil
	}
	actualRepo, err := internal.MainRepoRootIn(materializedPath)
	if err != nil {
		return "", fmt.Errorf("resolving repository for materialized stack entry %q: %w", entry.Name, err)
	}
	if !sameResolvedRepoRoot(targetRepo, actualRepo) {
		return "", fmt.Errorf("stack entry %q records repository %s but its worktree belongs to %s",
			entry.Name, targetRepo, actualRepo)
	}
	return targetRepo, nil
}

func sameResolvedRepoRoot(a, b string) bool {
	resolve := func(path string) string {
		if evaluated, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Clean(evaluated)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		return filepath.Clean(absolute)
	}
	return resolve(a) == resolve(b)
}

func managedRenamePathsOverlap(a, b string) (bool, error) {
	absolute := func(path string) (string, error) {
		value, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		return filepath.Clean(value), nil
	}
	a, err := absolute(a)
	if err != nil {
		return false, err
	}
	b, err = absolute(b)
	if err != nil {
		return false, err
	}
	if a == b {
		return true, nil
	}
	isDescendant := func(parent, child string) (bool, error) {
		relative, err := filepath.Rel(parent, child)
		if err != nil {
			return false, err
		}
		return relative != "." && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
	}
	if descendant, err := isDescendant(a, b); err != nil || descendant {
		return descendant, err
	}
	if descendant, err := isDescendant(b, a); err != nil || descendant {
		return descendant, err
	}

	oldInfo, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("reading original worktree identity %s: %w", a, err)
	}
	nearestExisting := func(path string) (os.FileInfo, int, error) {
		missingComponents := 0
		for {
			info, statErr := os.Stat(path)
			if statErr == nil {
				return info, missingComponents, nil
			}
			if !errors.Is(statErr, os.ErrNotExist) {
				return nil, 0, statErr
			}
			parent := filepath.Dir(path)
			if parent == path {
				return nil, 0, fmt.Errorf("no existing ancestor for %s", path)
			}
			path = parent
			missingComponents++
		}
	}
	newAncestorInfo, missing, err := nearestExisting(b)
	if err != nil {
		return false, fmt.Errorf("resolving destination worktree ancestry %s: %w", b, err)
	}
	if os.SameFile(oldInfo, newAncestorInfo) {
		return true, nil
	}
	if missing != 0 {
		return false, nil
	}
	for current := a; ; current = filepath.Dir(current) {
		info, statErr := os.Stat(current)
		if statErr != nil {
			return false, fmt.Errorf("reading original worktree ancestry %s: %w", current, statErr)
		}
		if os.SameFile(info, newAncestorInfo) {
			return true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return false, nil
}

func runRenameBranchExternalStep(step string, run func() error) error {
	if renameBranchExternalStepHook != nil {
		if err := renameBranchExternalStepHook(step); err != nil {
			return err
		}
	}
	return run()
}

func localGitBranchOID(repoRoot, branch string) (string, bool, error) {
	check := exec.Command("git", "-C", repoRoot, "check-ref-format", "--branch", branch)
	if output, err := check.CombinedOutput(); err != nil {
		return "", false, fmt.Errorf("validating Git branch %q: %s", branch,
			strings.TrimSpace(string(output)))
	}
	ref := "refs/heads/" + branch
	cmd := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", ref)
	err := cmd.Run()
	if err == nil {
		resolve := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", ref+"^{commit}")
		output, resolveErr := resolve.Output()
		if resolveErr != nil {
			return "", false, fmt.Errorf("resolving Git branch %q after presence check: %w", branch, resolveErr)
		}
		return strings.TrimSpace(string(output)), true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, fmt.Errorf("checking Git branch %q: %w", branch, err)
}

func verifyManagedRenameRollbackWorktree(
	targetRepo, oldPath, oldGitBranch, expectedOID string,
) error {
	actualRepo, err := internal.MainRepoRootIn(oldPath)
	if err != nil {
		return err
	}
	if !sameResolvedRepoRoot(actualRepo, targetRepo) {
		return fmt.Errorf("restored worktree belongs to %s, not %s", actualRepo, targetRepo)
	}
	branchCmd := exec.Command("git", "-C", oldPath, "symbolic-ref", "--short", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return fmt.Errorf("reading restored worktree branch: %w", err)
	}
	if branch := strings.TrimSpace(string(branchOutput)); branch != oldGitBranch {
		return fmt.Errorf("restored worktree is on branch %q, not %q", branch, oldGitBranch)
	}
	headCmd := exec.Command("git", "-C", oldPath, "rev-parse", "HEAD")
	headOutput, err := headCmd.Output()
	if err != nil {
		return fmt.Errorf("reading restored worktree commit: %w", err)
	}
	if head := strings.TrimSpace(string(headOutput)); head != expectedOID {
		return fmt.Errorf("restored worktree commit is %s, not %s", head, expectedOID)
	}
	return nil
}

type managedRenameRecoveryContext struct {
	featurePath string
	feature     string
	evidence    internal.ManagedTemplateBranchRenameRecoveryEvidence
}

func newManagedRenameRecoveryContext(
	featurePath, feature, targetRepo,
	oldName, newName, oldGitBranch, newGitBranch,
	oldPath, newPath, expectedOID string,
) (*managedRenameRecoveryContext, error) {
	stackPreimage, err := os.ReadFile(internal.StackPath(featurePath))
	if err != nil {
		return nil, fmt.Errorf("reading stack preimage for managed branch rename: %w", err)
	}
	ledger, err := os.ReadFile(internal.ManagedTemplateStatePath(featurePath))
	if err != nil {
		return nil, fmt.Errorf("reading ownership preimage for managed branch rename: %w", err)
	}
	state, err := internal.LoadManagedTemplateState(featurePath)
	if err != nil {
		return nil, err
	}
	return &managedRenameRecoveryContext{
		featurePath: featurePath,
		feature:     feature,
		evidence: internal.ManagedTemplateBranchRenameRecoveryEvidence{
			TargetRepo: targetRepo,
			OldName:    oldName, NewName: newName,
			OldGitBranch: oldGitBranch, NewGitBranch: newGitBranch,
			OldPath: oldPath, NewPath: newPath, ExpectedOID: expectedOID,
			StackPreimage: stackPreimage, OriginalLedger: ledger,
			Owner: state.Owner,
		},
	}, nil
}

func managedRenamePartialOutcome(
	context *managedRenameRecoveryContext,
	stage string,
	completed, pending []string,
	cause error,
) error {
	if context == nil {
		return fmt.Errorf("managed branch rename partial outcome during %s: %w", stage, cause)
	}
	evidenceRecord := context.evidence
	evidenceRecord.Stage = stage
	evidenceRecord.CompletedStages = append([]string(nil), completed...)
	evidenceRecord.PendingStages = append([]string(nil), pending...)
	evidence, evidenceErr := internal.RecordManagedTemplateBranchRenameRecoveryEvidence(
		context.featurePath, context.feature, evidenceRecord,
	)
	if evidenceErr != nil {
		return errors.Join(
			fmt.Errorf("managed branch rename partial outcome during %s: %w", stage, cause),
			fmt.Errorf("recording managed recovery evidence failed: %w", evidenceErr),
		)
	}
	return fmt.Errorf("managed branch rename partial outcome during %s; recovery evidence retained at %s: %w",
		stage, evidence, cause)
}

func rollbackManagedMaterializedBranchRename(
	retirement *preparedManagedWorktreeRetirement,
	context *managedRenameRecoveryContext,
	targetRepo, oldPath, newPath, oldGitBranch, newGitBranch, expectedOID string,
) error {
	if err := runRenameBranchExternalStep("before-rollback-rename", func() error { return nil }); err != nil {
		return managedRenamePartialOutcome(context, "rollback-ref-precheck",
			[]string{"old-worktree-removed"}, []string{"rollback-ref", "rollback-worktree", "rollback-managed-context"}, err)
	}
	if _, err := os.Lstat(oldPath); err == nil {
		return managedRenamePartialOutcome(context, "rollback-path-conflict",
			[]string{"old-worktree-removed"}, []string{"rollback-worktree", "rollback-managed-context"},
			fmt.Errorf("original worktree path was recreated concurrently: %s", oldPath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return managedRenamePartialOutcome(context, "rollback-path-check",
			[]string{"old-worktree-removed"}, []string{"rollback-worktree", "rollback-managed-context"}, err)
	}
	if _, err := os.Lstat(newPath); err == nil {
		return managedRenamePartialOutcome(context, "rollback-destination-conflict",
			[]string{"old-worktree-removed"}, []string{"rollback-ref", "rollback-worktree", "rollback-managed-context"},
			fmt.Errorf("destination worktree path exists after failed creation: %s", newPath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return managedRenamePartialOutcome(context, "rollback-destination-check",
			[]string{"old-worktree-removed"}, []string{"rollback-ref", "rollback-worktree", "rollback-managed-context"}, err)
	}

	oldOID, oldExists, err := localGitBranchOID(targetRepo, oldGitBranch)
	if err != nil {
		return managedRenamePartialOutcome(context, "rollback-old-ref-check",
			[]string{"old-worktree-removed"}, []string{"rollback-ref", "rollback-worktree", "rollback-managed-context"}, err)
	}
	newOID, newExists, err := localGitBranchOID(targetRepo, newGitBranch)
	if err != nil {
		return managedRenamePartialOutcome(context, "rollback-new-ref-check",
			[]string{"old-worktree-removed"}, []string{"rollback-ref", "rollback-worktree", "rollback-managed-context"}, err)
	}
	switch {
	case oldExists && oldOID == expectedOID:
		// The branch rename did not take effect. A foreign destination ref is
		// left untouched; restoring the original worktree remains safe.
	case !oldExists && newExists && newOID == expectedOID:
		if err := internal.RunDir(
			targetRepo, "git", "branch", "-m", newGitBranch, oldGitBranch,
		); err != nil {
			return managedRenamePartialOutcome(context, "rollback-branch-rename",
				[]string{"old-worktree-removed", "new-branch-created"},
				[]string{"rollback-ref", "rollback-worktree", "rollback-managed-context"}, err)
		}
	default:
		return managedRenamePartialOutcome(context, "rollback-ref-drift",
			[]string{"old-worktree-removed"}, []string{"rollback-ref", "rollback-worktree", "rollback-managed-context"},
			fmt.Errorf("expected commit %s; old=%q present=%v new=%q present=%v",
				expectedOID, oldOID, oldExists, newOID, newExists))
	}
	oldOID, oldExists, err = localGitBranchOID(targetRepo, oldGitBranch)
	if err != nil || !oldExists || oldOID != expectedOID {
		if err == nil {
			err = fmt.Errorf("restored branch is %q present=%v, expected %s", oldOID, oldExists, expectedOID)
		}
		return managedRenamePartialOutcome(context, "rollback-restored-ref-check",
			[]string{"old-worktree-removed", "original-ref-restored"},
			[]string{"rollback-worktree", "rollback-managed-context"}, err)
	}
	if err := runRenameBranchExternalStep("before-rollback-worktree-add", func() error {
		return internal.RunDir(targetRepo, "git", "worktree", "add", oldPath, oldGitBranch)
	}); err != nil {
		return managedRenamePartialOutcome(context, "rollback-worktree-add",
			[]string{"old-worktree-removed", "original-ref-restored"},
			[]string{"rollback-worktree", "rollback-managed-context"}, err)
	}
	if err := verifyManagedRenameRollbackWorktree(
		targetRepo, oldPath, oldGitBranch, expectedOID,
	); err != nil {
		return managedRenamePartialOutcome(context, "rollback-worktree-identity",
			[]string{"old-worktree-removed", "original-ref-restored", "original-worktree-restored"},
			[]string{"rollback-managed-context"}, err)
	}
	if err := retirement.restoreAfterRollback(); err != nil {
		return managedRenamePartialOutcome(context, "rollback-managed-context",
			[]string{"old-worktree-removed", "original-ref-restored", "original-worktree-restored"},
			[]string{"rollback-managed-context"}, err)
	}
	return nil
}

func wrapRenameRollbackError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s failed: %w", action, err)
}
