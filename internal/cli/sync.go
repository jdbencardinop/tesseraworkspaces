package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func syncCmd() *cobra.Command {
	var verbose bool
	var push bool
	var cont bool
	var abort bool
	var testCmd string

	cmd := &cobra.Command{
		Use:   "sync <feature>",
		Short: "Rebase branches in dependency order",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			internal.RequireTool("git")

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			if ws.Mode == internal.ModeCheckout {
				return runCheckoutSync(args[0], push, testCmd, cont, abort, verbose)
			}

			feature := args[0]
			// One guard covers the plain, --abort, and --continue paths.
			// syncFeature carries none: it has no error channel and would
			// degrade the message to "sync incomplete".
			if err := internal.GuardFeatureName(internal.TwsRoot(), feature); err != nil {
				return err
			}
			featurePath, fpErr := ws.ResolveFeaturePath(feature)
			if fpErr != nil {
				return fpErr
			}

			if abort {
				return handleSyncAbort(feature, featurePath)
			}
			if cont {
				return handleSyncContinue(feature, featurePath, push)
			}
			if internal.HasSyncState(featurePath) {
				state, _ := internal.LoadSyncState(featurePath)
				return fmt.Errorf("previous sync incomplete (failed on: %s); use --continue or --abort", state.FailedBranch)
			}

			result := syncFeature(feature, verbose)
			if !result.Complete {
				return fmt.Errorf("sync incomplete")
			}
			fmt.Println("Sync complete.")
			if push {
				fmt.Println("\nPushing...")
				if err := pushFeature(feature, false); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show full git fetch output")
	cmd.Flags().BoolVar(&push, "push", false, "Push all branches after syncing")
	cmd.Flags().BoolVar(&cont, "continue", false, "Resume after conflict resolution")
	cmd.Flags().BoolVar(&abort, "abort", false, "Discard sync state and start fresh")
	cmd.Flags().StringVar(&testCmd, "test", "", "Validation command to run after each rebase (checkout mode)")

	return cmd
}

func handleSyncAbort(feature, featurePath string) error {
	state, err := internal.LoadSyncState(featurePath)
	if err != nil {
		fmt.Println("Nothing to abort — no sync in progress.")
		return nil
	}
	if state.FailedBranch != "" {
		path := internal.WorktreePath(feature, state.FailedBranch)
		if isRebaseInProgress(path) {
			_ = internal.RunSilentDir(path, "git", "rebase", "--abort")
		}
	}
	internal.DeleteSyncState(featurePath)
	fmt.Println("Sync state cleared.")
	return nil
}

func handleSyncContinue(feature, featurePath string, push bool) error {
	state, err := internal.LoadSyncState(featurePath)
	if err != nil {
		return fmt.Errorf("nothing to continue — no sync in progress")
	}
	failedPath := internal.WorktreePath(feature, state.FailedBranch)
	if state.FailedBranch != "" && isRebaseInProgress(failedPath) {
		return fmt.Errorf("rebase still in progress in %s; resolve conflicts, run git add . && git rebase --continue, then retry", state.FailedBranch)
	}

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return fmt.Errorf("load stack: %w", err)
	}
	if state.FailedBranch != "" {
		failedEntry := internal.GetBranch(stack, state.FailedBranch)
		if failedEntry.Name == "" {
			return fmt.Errorf("failed branch %q no longer exists in stack", state.FailedBranch)
		}
		if !branchContainsConfiguredParent(feature, stack, failedEntry) {
			return fmt.Errorf("resolved branch %s still does not contain its configured parent %s", failedEntry.Name, failedEntry.Base)
		}
		fmt.Println(formatSyncStatus(state.FailedBranch, "active", "resolved"))
	}

	done := make(map[string]bool)
	for _, name := range state.Completed {
		done[name] = true
	}
	if state.FailedBranch != "" {
		done[state.FailedBranch] = true
	}
	sorted, err := internal.TopoSort(stack)
	if err != nil {
		return err
	}
	fmt.Printf("Resuming sync with %d pending branch(es)\n", len(state.Pending))
	result := syncWithStackFiltered(feature, featurePath, stack, sorted, done)
	if !result.Complete {
		return fmt.Errorf("sync incomplete")
	}
	fmt.Println("Sync complete.")
	if push {
		fmt.Println("\nPushing...")
		if err := pushFeature(feature, false); err != nil {
			return err
		}
	}
	return nil
}

func branchContainsConfiguredParent(feature string, stack internal.Stack, child internal.StackEntry) bool {
	parent := internal.GetBranch(stack, child.Base)
	if parent.Name == "" || !sameStackRepo(parent.Repo, child.Repo) {
		return true
	}
	path := internal.WorktreePath(feature, child.Name)
	return internal.RunSilentDir(path, "git", "merge-base", "--is-ancestor", parent.GitBranch(), child.GitBranch()) == nil
}

func isRebaseInProgress(worktreePath string) bool {
	// Check for .git/rebase-merge or .git/rebase-apply
	gitDir := internal.RunSilent("git", "-C", worktreePath, "rev-parse", "--git-dir")
	if gitDir != nil {
		return false
	}
	// Simpler: check if git status shows rebase
	err := internal.RunSilent("git", "-C", worktreePath, "rebase", "--show-current-patch")
	return err == nil
}

func syncFeature(feature string, verbose bool) syncResult {
	featurePath := internal.FeaturePath(feature)

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		fetchQuiet("", "", verbose)
		syncFallback(featurePath)
		return syncResult{Complete: true}
	}

	repos := internal.UniqueRepos(stack, featurePath)
	for repo, wtPath := range repos {
		fetchQuiet(repo, wtPath, verbose)
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return syncResult{}
	}
	return syncWithStack(feature, featurePath, stack, sorted)
}

func fetchQuiet(repo, wtPath string, verbose bool) {
	label := "default repo"
	if repo != "" {
		label = repo
	}

	if verbose {
		fmt.Printf("Fetching %s...\n", label)
		if wtPath != "" {
			_ = internal.RunDir(wtPath, "git", "fetch")
		} else if repo != "" {
			_ = internal.RunDir(repo, "git", "fetch")
		} else {
			_ = internal.Run("git", "fetch")
		}
	} else {
		fmt.Printf("Fetching %s... ", label)
		var err error
		if wtPath != "" {
			err = internal.RunSilentDir(wtPath, "git", "fetch")
		} else if repo != "" {
			err = internal.RunSilentDir(repo, "git", "fetch")
		} else {
			err = internal.RunSilent("git", "fetch")
		}
		if err != nil {
			fmt.Println("failed")
		} else {
			fmt.Println("done")
		}
	}
}
