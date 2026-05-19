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
			createWorktree(args[0], args[1], base, force)
		},
	}

	cmd.Flags().StringVar(&base, "base", "main", "Parent branch for stacking")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force checkout of already checked-out branch")

	return cmd
}

// createWorktree is the shared logic for creating a worktree branch.
// Used by both tws new and tws add -n.
func createWorktree(feature, branch, base string, force bool) {
	internal.RequireTool("git")

	featurePath := internal.FeaturePath(feature)
	path := internal.WorktreePath(feature, branch)

	internal.Must(os.MkdirAll(featurePath, 0755))

	repoRoot, err := internal.MainRepoRoot()
	if err != nil {
		fmt.Println("Error: must be run from inside a git repository")
		os.Exit(1)
	}

	if internal.BranchExists(branch) {
		if isCheckedOut(branch) && !force {
			fmt.Printf("Warning: branch %q is already checked out in another worktree.\n", branch)
			fmt.Println("Use --force to check it out anyway.")
			os.Exit(1)
		}

		gitArgs := []string{"worktree", "add"}
		if force {
			gitArgs = append(gitArgs, "--force")
		}
		gitArgs = append(gitArgs, path, branch)
		internal.Must(internal.RunDir(repoRoot, "git", gitArgs...))
	} else {
		internal.Must(internal.RunDir(repoRoot, "git", "worktree", "add", path, "-b", branch))
	}

	stack, _ := internal.LoadStack(featurePath)
	if !internal.HasBranch(stack, branch) {
		stack.Branches = append(stack.Branches, internal.StackEntry{Name: branch, Base: base})
		internal.Must(internal.SaveStack(featurePath, stack))
	}

	// Inject shared files into the worktree
	if err := internal.InjectFiles(featurePath, path); err != nil {
		fmt.Printf("Warning: inject failed: %v\n", err)
	}

	fmt.Printf("Worktree created: %s (base: %s)\n", path, base)
}

// isCheckedOut checks if a branch is currently checked out in any worktree.
func isCheckedOut(branch string) bool {
	out, err := exec.Command("git", "worktree", "list", "--porcelain").Output()
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
