package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func syncCmd() *cobra.Command {
	var verbose bool
	var push bool
	var cont bool
	var abort bool

	cmd := &cobra.Command{
		Use:   "sync <feature>",
		Short: "Rebase worktrees in dependency order",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			internal.RequireTool("git")
			feature := args[0]
			featurePath := internal.FeaturePath(feature)

			if abort {
				handleSyncAbort(featurePath)
				return
			}

			if cont {
				handleSyncContinue(feature, featurePath, verbose, push)
				return
			}

			// Check for existing state
			if internal.HasSyncState(featurePath) {
				state, _ := internal.LoadSyncState(featurePath)
				fmt.Printf("Previous sync incomplete (failed on: %s)\n", state.FailedBranch)
				fmt.Println("  Use --continue to resume after resolving conflicts")
				fmt.Println("  Use --abort to discard sync state and start fresh")
				os.Exit(1)
			}

			syncFeature(feature, verbose)

			if push {
				fmt.Println()
				fmt.Println("Pushing...")
				pushFeature(feature, false)
			}
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show full git fetch output")
	cmd.Flags().BoolVar(&push, "push", false, "Push all branches after syncing")
	cmd.Flags().BoolVar(&cont, "continue", false, "Resume after conflict resolution")
	cmd.Flags().BoolVar(&abort, "abort", false, "Discard sync state and start fresh")

	return cmd
}

func handleSyncAbort(featurePath string) {
	if !internal.HasSyncState(featurePath) {
		fmt.Println("Nothing to abort — no sync in progress.")
		return
	}
	internal.DeleteSyncState(featurePath)
	fmt.Println("Sync state cleared.")
}

func handleSyncContinue(feature, featurePath string, verbose, push bool) {
	state, err := internal.LoadSyncState(featurePath)
	if err != nil {
		fmt.Println("Nothing to continue — no sync in progress.")
		return
	}

	// Verify the failed branch's rebase was completed
	failedPath := internal.WorktreePath(feature, state.FailedBranch)
	if _, err := os.Stat(failedPath); err == nil {
		// Check if there's still a rebase in progress
		if isRebaseInProgress(failedPath) {
			fmt.Printf("Rebase still in progress in %s\n", state.FailedBranch)
			fmt.Println("  Resolve conflicts, run: git add . && git rebase --continue")
			fmt.Println("  Then run: tws sync <feature> --continue")
			os.Exit(1)
		}
	}

	fmt.Printf("Resuming sync (previously failed on: %s)\n", state.FailedBranch)

	// Mark failed branch as completed
	state.Completed = append(state.Completed, state.FailedBranch)
	fmt.Println(formatSyncStatus(state.FailedBranch, "active", "resolved"))

	// Load stack and continue with pending branches
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		fmt.Println("Error: could not load stack.yaml")
		os.Exit(1)
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// Build skip set from completed + skipped branches
	skip := make(map[string]bool)
	for _, b := range state.Completed {
		skip[b] = true
	}
	for _, b := range state.Skipped {
		skip[b] = true
	}

	// Continue with remaining branches
	syncWithStackFiltered(feature, featurePath, stack, sorted, skip)

	// Clean up state
	internal.DeleteSyncState(featurePath)
	fmt.Println("Sync complete.")

	if push {
		fmt.Println()
		fmt.Println("Pushing...")
		pushFeature(feature, false)
	}
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

func syncFeature(feature string, verbose bool) {
	featurePath := internal.FeaturePath(feature)

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		fetchQuiet("", "", verbose)
		syncFallback(featurePath)
		return
	}

	// Fetch per-repo
	repos := internal.UniqueRepos(stack, featurePath)
	for repo, wtPath := range repos {
		fetchQuiet(repo, wtPath, verbose)
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	syncWithStack(feature, featurePath, stack, sorted)
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
