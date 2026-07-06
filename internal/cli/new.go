package cli

import (
	"fmt"
	"os"
	"os/exec"
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
		Run: func(cmd *cobra.Command, args []string) {
			if !cmd.Flags().Changed("base") {
				base = internal.DefaultBranch()
			}
			createWorktree(args[0], args[1], base, repo, force)
		},
	}

	cmd.Flags().StringVar(&base, "base", "", "Parent branch for stacking (default: repo's default branch)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force checkout of already checked-out branch")
	cmd.Flags().StringVar(&repo, "repo", "", "Source repository path (for cross-repo worktrees)")

	return cmd
}

// createWorktree is the shared logic for creating a worktree branch.
// Used by both tws new and tws add -n.
// repoPath is the source repo (empty = current repo).
func createWorktree(feature, name, base, repoPath string, force bool) {
	internal.RequireTool("git")

	featurePath := internal.FeaturePath(feature)
	path := internal.WorktreePath(feature, name) // worktree dir uses short name

	internal.Must(os.MkdirAll(featurePath, 0755))

	// Resolve the actual git branch name (apply prefix if configured)
	cfg := internal.LoadConfig()
	gitBranch := name
	if cfg.BranchPrefix != "" {
		gitBranch = cfg.BranchPrefix + name
	}

	// Determine which repo to use
	var repoRoot string
	if repoPath != "" {
		absRepo, err := internal.AbsPath(repoPath)
		if err != nil {
			fmt.Printf("Error: invalid repo path %s: %v\n", repoPath, err)
			os.Exit(1)
		}
		repoRoot = absRepo
		repoPath = absRepo
	} else {
		root, err := internal.MainRepoRoot()
		if err != nil {
			fmt.Println("Error: must be run from inside a git repository")
			os.Exit(1)
		}
		repoRoot = root
	}

	// Check if branch exists in the target repo (try both prefixed and unprefixed)
	branchExists := internal.RunSilent("git", "-C", repoRoot, "rev-parse", "--verify", gitBranch) == nil
	if !branchExists && gitBranch != name {
		// Try the unprefixed name (user may have created the branch manually)
		if internal.RunSilent("git", "-C", repoRoot, "rev-parse", "--verify", name) == nil {
			gitBranch = name // use the existing unprefixed branch
			branchExists = true
		}
	}

	if branchExists {
		if isCheckedOutIn(repoRoot, gitBranch) && !force {
			fmt.Printf("Warning: branch %q is already checked out in another worktree.\n", gitBranch)
			fmt.Println("Use --force to check it out anyway.")
			os.Exit(1)
		}

		gitArgs := []string{"worktree", "add"}
		if force {
			gitArgs = append(gitArgs, "--force")
		}
		gitArgs = append(gitArgs, path, gitBranch)
		internal.Must(internal.RunDir(repoRoot, "git", gitArgs...))
	} else {
		internal.Must(internal.RunDir(repoRoot, "git", "worktree", "add", path, "-b", gitBranch))
	}

	stack, _ := internal.LoadStack(featurePath)
	if !internal.HasBranch(stack, name) {
		entry := internal.StackEntry{
			Name: name,
			Base: base,
			Repo: repoPath,
		}
		// Only store Branch if it differs from Name
		if gitBranch != name {
			entry.Branch = gitBranch
		}
		stack.Branches = append(stack.Branches, entry)
		internal.Must(internal.SaveStack(featurePath, stack))
	}

	// Inject shared files into the worktree
	target := internal.ResolveInjectInto("")
	if err := internal.InjectFiles(featurePath, path, target); err != nil {
		fmt.Printf("Warning: inject failed: %v\n", err)
	}

	// Auto-install hooks if configured
	if cfg.AutoHooks != nil && *cfg.AutoHooks {
		if err := installHooksForWorktree(path, feature); err != nil {
			fmt.Printf("Warning: auto-hooks install failed: %v\n", err)
		}
	}

	if repoPath != "" {
		fmt.Printf("Worktree created: %s (base: %s, repo: %s)\n", path, base, repoPath)
	} else {
		fmt.Printf("Worktree created: %s (base: %s)\n", path, base)
	}
}

// isCheckedOutIn checks if a branch is checked out in any worktree of the given repo.
func isCheckedOutIn(repoDir, branch string) bool {
	out, err := exec.Command("git", "-C", repoDir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "branch refs/heads/"+branch {
			return true
		}
	}
	return false
}
