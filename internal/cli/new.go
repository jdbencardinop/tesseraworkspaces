package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func newCmd() *cobra.Command {
	var base string
	var force bool
	var repo string

	cmd := &cobra.Command{
		Use:   "new <feature> <branch>",
		Short: "Create a worktree branch",
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
			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			if ws.Mode == internal.ModeCheckout {
				if repo != "" {
					return fmt.Errorf("--repo is not supported in checkout mode; checkout workspaces use exactly one repository")
				}
				return createCheckoutBranch(ws, args[0], args[1], base, force)
			}
			return createWorktree(args[0], args[1], base, repo, force)
		},
	}

	cmd.Flags().StringVar(&base, "base", "", "Parent branch, tag, or commit (default: selected repo's origin/HEAD)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force checkout of already checked-out branch")
	cmd.Flags().StringVar(&repo, "repo", "", "Source repository path (for cross-repo worktrees)")

	return cmd
}

// createCheckoutBranch creates a git branch for checkout mode (no worktree).
// Atomic: if metadata persistence fails after branch creation, the branch is
// rolled back (only if this operation created it). Archived entries are
// reactivated. Mismatched existing entries are rejected.
func createCheckoutBranch(ws internal.Workspace, feature, name, requestedBase string, force bool) error {
	if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return err
	}

	featurePath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return err
	}

	// Pre-validate feature directory exists.
	if _, err := os.Stat(featurePath); os.IsNotExist(err) {
		return fmt.Errorf("feature %q not found; run 'tws add %s' first", feature, feature)
	}

	stack, _ := internal.LoadStack(featurePath)

	repoRoot := ws.RepoRoot

	baseName, baseRef, err := resolveCreationBase(repoRoot, "", stack, requestedBase)
	if err != nil {
		return err
	}

	cfg := internal.LoadConfig()
	gitBranch := name
	if cfg.BranchPrefix != "" {
		gitBranch = cfg.BranchPrefix + name
	}

	branchExisted := internal.VerifyGitRef(repoRoot, gitBranch) == nil
	if !branchExisted && gitBranch != name && internal.VerifyGitRef(repoRoot, name) == nil {
		gitBranch = name
		branchExisted = true
	}

	// Check for existing stack entry.
	existing := internal.GetBranch(stack, name)
	if existing.Name != "" {
		// If archived, reactivate.
		if existing.Archived {
			for i := range stack.Branches {
				if stack.Branches[i].Name == name {
					stack.Branches[i].Archived = false
					break
				}
			}
			if err := internal.SaveStack(featurePath, stack); err != nil {
				return fmt.Errorf("failed to unarchive branch: %w", err)
			}
			fmt.Printf("Reactivated archived branch: %s (git: %s)\n", name, existing.GitBranch())
			return nil
		}
		// Existing non-archived entry: validate consistency.
		expectedBranch := gitBranch
		if existing.Branch != "" {
			expectedBranch = existing.Branch
		}
		if existing.GitBranch() != expectedBranch && existing.GitBranch() != gitBranch {
			return fmt.Errorf("branch %q already registered with git branch %q (expected %q); use rename or delete first",
				name, existing.GitBranch(), gitBranch)
		}
		fmt.Printf("Branch %s already registered (git: %s)\n", name, existing.GitBranch())
		return nil
	}

	// Create git branch if it does not exist.
	if !branchExisted {
		if err := internal.RunDir(repoRoot, "git", "branch", gitBranch, baseRef); err != nil {
			return fmt.Errorf("creating git branch %s: %w", gitBranch, err)
		}
	}

	// Register in stack.
	entry := internal.StackEntry{
		Name: name,
		Base: baseName,
	}
	if gitBranch != name {
		entry.Branch = gitBranch
	}
	stack.Branches = append(stack.Branches, entry)

	if err := internal.SaveStack(featurePath, stack); err != nil {
		// Rollback: only delete branch if we created it.
		if !branchExisted {
			_ = internal.RunSilentDir(repoRoot, "git", "branch", "-d", gitBranch)
		}
		return fmt.Errorf("failed to persist stack (branch rolled back): %w", err)
	}

	fmt.Printf("Branch created: %s (git: %s, base: %s)\n", name, gitBranch, baseName)
	return nil
}

// openCheckoutTmux opens a tmux session for a checkout-mode branch.
func openCheckoutTmux(ws internal.Workspace, feature, branch string) {
	session := sanitizeSessionName(feature + "/" + branch)
	fmt.Printf("Checkout branch: use 'git checkout %s' then 'tmux new-session -s %s'\n", branch, session)
}

// createWorktree is the shared logic for creating a worktree branch (external mode).
// Used by both tws new and tws add -n. Explicit base refs are literal;
// an omitted base resolves from the selected repository's origin/HEAD.
func createWorktree(feature, name, requestedBase, repoPath string, force bool) error {
	wsRoot := internal.TwsRoot()
	if err := internal.GuardFeatureName(wsRoot, feature); err != nil {
		return err
	}

	featurePath := internal.FeaturePath(feature)
	path := internal.WorktreePath(feature, name)

	if err := internal.EnsureExternalWorkspaceMarker(wsRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(featurePath, 0755); err != nil {
		return err
	}

	stack, _ := internal.LoadStack(featurePath)
	repoRoot, storedRepo, err := resolveSourceRepo(featurePath, stack, repoPath)
	if err != nil {
		return err
	}

	baseName, baseRef, err := resolveCreationBase(repoRoot, storedRepo, stack, requestedBase)
	if err != nil {
		return err
	}

	cfg := internal.LoadConfig()
	gitBranch := name
	if cfg.BranchPrefix != "" {
		gitBranch = cfg.BranchPrefix + name
	}

	branchExists := internal.VerifyGitRef(repoRoot, gitBranch) == nil
	if !branchExists && gitBranch != name && internal.VerifyGitRef(repoRoot, name) == nil {
		gitBranch = name
		branchExists = true
	}

	if branchExists {
		if isCheckedOutIn(repoRoot, gitBranch) && !force {
			return fmt.Errorf("branch %q is already checked out in another worktree; use --force to check it out anyway", gitBranch)
		}

		gitArgs := []string{"worktree", "add"}
		if force {
			gitArgs = append(gitArgs, "--force")
		}
		gitArgs = append(gitArgs, path, gitBranch)
		if err := internal.RunDir(repoRoot, "git", gitArgs...); err != nil {
			return err
		}
	} else {
		if err := internal.RunDir(repoRoot, "git", "worktree", "add", path, "-b", gitBranch, baseRef); err != nil {
			return err
		}
	}

	if !internal.HasBranch(stack, name) {
		entry := internal.StackEntry{
			Name: name,
			Base: baseName,
			Repo: storedRepo,
		}
		if gitBranch != name {
			entry.Branch = gitBranch
		}
		stack.Branches = append(stack.Branches, entry)
		if err := internal.SaveStack(featurePath, stack); err != nil {
			return err
		}
	}

	target := internal.ResolveInjectInto("")
	if err := internal.InjectFiles(featurePath, path, target); err != nil {
		fmt.Printf("Warning: inject failed: %v\n", err)
	}

	if cfg.AutoHooks != nil && *cfg.AutoHooks {
		if err := installHooksForWorktree(path, feature); err != nil {
			fmt.Printf("Warning: auto-hooks install failed: %v\n", err)
		}
	}

	if storedRepo != "" {
		fmt.Printf("Worktree created: %s (base: %s, repo: %s)\n", path, baseName, storedRepo)
	} else {
		fmt.Printf("Worktree created: %s (base: %s)\n", path, baseName)
	}
	return nil
}

func resolveSourceRepo(featurePath string, stack internal.Stack, requestedRepo string) (string, string, error) {
	if requestedRepo != "" {
		absRepo, err := internal.AbsPath(requestedRepo)
		if err != nil {
			return "", "", fmt.Errorf("invalid repo path %s: %w", requestedRepo, err)
		}
		repoRoot, err := internal.GitRepoRootIn(absRepo)
		if err != nil {
			return "", "", fmt.Errorf("%s is not a Git repository", requestedRepo)
		}
		return repoRoot, repoRoot, nil
	}

	if repoRoot, err := internal.MainRepoRoot(); err == nil {
		return repoRoot, "", nil
	}

	type candidate struct {
		root       string
		storedRepo string
	}
	candidates := make(map[string]candidate)
	for _, entry := range stack.Branches {
		if entry.Repo != "" {
			root, err := internal.GitRepoRootIn(entry.Repo)
			if err == nil {
				candidates[root] = candidate{root: root, storedRepo: root}
			}
			continue
		}

		worktreePath := filepath.Join(featurePath, "worktrees", entry.Name)
		if _, err := os.Stat(worktreePath); err != nil {
			continue
		}
		root, err := internal.MainRepoRootIn(worktreePath)
		if err == nil {
			candidates[root] = candidate{root: root}
		}
	}

	if len(candidates) == 1 {
		for _, c := range candidates {
			return c.root, c.storedRepo, nil
		}
	}
	if len(candidates) > 1 {
		return "", "", fmt.Errorf("feature uses multiple repositories; specify --repo")
	}
	return "", "", fmt.Errorf("could not infer source repository; run from a Git repo or specify --repo")
}

func resolveCreationBase(repoRoot, storedRepo string, stack internal.Stack, requestedBase string) (string, string, error) {
	if requestedBase == "" {
		baseName, err := internal.DefaultBranchIn(repoRoot)
		if err != nil {
			return "", "", err
		}
		remoteRef := "origin/" + baseName
		if internal.VerifyGitRef(repoRoot, remoteRef) == nil {
			return baseName, remoteRef, nil
		}
		if internal.VerifyGitRef(repoRoot, baseName) == nil {
			return baseName, baseName, nil
		}
		return "", "", fmt.Errorf("default branch %q does not exist in %s", baseName, repoRoot)
	}

	baseRef := requestedBase
	if parent := internal.GetBranch(stack, requestedBase); parent.Name != "" {
		if !sameStackRepo(parent.Repo, storedRepo) {
			return "", "", fmt.Errorf("base %q belongs to a different repository", requestedBase)
		}
		baseRef = parent.GitBranch()
	}
	if err := internal.VerifyGitRef(repoRoot, baseRef); err != nil {
		return "", "", fmt.Errorf("base ref %q does not exist in %s", requestedBase, repoRoot)
	}
	return requestedBase, baseRef, nil
}

func sameStackRepo(a, b string) bool {
	if a == "" && b == "" {
		return true
	}
	return a == b
}

func isCheckedOutIn(repoRoot, branch string) bool {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.TrimSpace(line) == "branch refs/heads/"+branch {
			return true
		}
	}
	return false
}
