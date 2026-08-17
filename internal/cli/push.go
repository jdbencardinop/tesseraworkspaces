package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func pushCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "push <feature>",
		Short: "Push all branches in a feature with --force-with-lease",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			internal.RequireTool("git")
			feature := args[0]

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			// The sibling-space guard used to arrive through
			// internal.RequireFeaturePath; the external arm no longer calls it,
			// so the command owns the guard explicitly, before any layout work.
			if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
				return err
			}
			if ws.Mode == internal.ModeCheckout {
				return pushFeatureCheckout(feature, dryRun)
			}
			layout, err := resolveExternalSyncLayout(ws, internal.TwsRoot(), feature)
			if err != nil {
				return err
			}
			return pushFeature(feature, layout, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be pushed without pushing")

	return cmd
}

// pushFeature pushes every entry of an external feature. It takes the resolved
// layout so the push half of a run can never target a different root than the
// rebase half.
func pushFeature(feature string, layout externalSyncLayout, dryRun bool) error {
	stack, err := internal.LoadStack(layout.FeaturePath)
	if err != nil {
		return fmt.Errorf("no stack.yaml found for feature: %s", feature)
	}
	return pushEntries(layout, stack.Branches, dryRun)
}

// pushScoped is the sync-side push of a new-mode run, for every scope. It is
// strict and payload-aware: it pushes only the entries this run selected AND
// successfully rebased, in selection order, records every success in the
// payload before the next push is attempted, and stops at the first failure so
// `--continue` can retry exactly the entries that were never pushed.
func pushScoped(feature string, layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection, completed []string, payload *internal.SyncRunState) error {
	if payload == nil {
		return fmt.Errorf("internal error: new-mode push without run state for %s", feature)
	}
	rebased := make(map[string]bool, len(completed))
	for _, name := range completed {
		rebased[name] = true
	}
	alreadyPushed := make(map[string]bool, len(payload.Pushed))
	for _, name := range payload.Pushed {
		alreadyPushed[name] = true
	}

	for _, selected := range sel.Entries {
		if !rebased[selected.Name] || alreadyPushed[selected.Name] {
			continue
		}
		entry := internal.GetBranch(stack, selected.Name)
		if entry.Name == "" {
			continue
		}
		path := layout.WorktreePath(entry.Name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// Same skip as the legacy loop: an archived entry has no worktree
			// and is never pushed by tws.
			fmt.Printf("  [-] %s (archived, skipped)\n", entry.Name)
			continue
		}

		repoDir := path
		if entry.Repo != "" {
			repoDir = entry.Repo
		}
		if err := internal.RunDirClean(repoDir, "git", "push", "--force-with-lease", "origin", entry.GitBranch()); err != nil {
			fmt.Printf("  [x] %s (push failed)\n", entry.Name)
			saveScopedPushFailure(layout.FeaturePath, payload, entry.Name)
			return fmt.Errorf("push failed for %s; fix the remote problem, then resume with: tws sync %s --continue", entry.Name, feature)
		}
		fmt.Printf("  [+] %s (pushed)\n", entry.Name)

		// The logical name is recorded only after Git succeeded, and the
		// payload is persisted before the next push is attempted.
		payload.Pushed = append(payload.Pushed, entry.Name)
		if err := internal.SaveSyncRunState(layout.FeaturePath, payload); err != nil {
			return fmt.Errorf("record pushed entry %s: %w", entry.Name, err)
		}
	}
	return nil
}

// pushEntries is the legacy push loop of `tws push` and of every no-flag run:
// it prints per-entry failures and returns nil, which is exactly the
// compatibility behaviour a new-mode run must not have.
func pushEntries(layout externalSyncLayout, entries []internal.StackEntry, dryRun bool) error {
	for _, entry := range entries {
		path := layout.WorktreePath(entry.Name)

		// Skip archived branches
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Printf("  [-] %s (archived, skipped)\n", entry.Name)
			continue
		}

		if dryRun {
			fmt.Printf("  [~] %s (would push --force-with-lease)\n", entry.Name)
			continue
		}

		// Determine repo context
		repoDir := path
		if entry.Repo != "" {
			repoDir = entry.Repo
		}

		runErr := internal.RunDirClean(repoDir, "git", "push", "--force-with-lease", "origin", entry.GitBranch())
		if runErr != nil {
			fmt.Printf("  [x] %s (push failed)\n", entry.Name)
		} else {
			fmt.Printf("  [+] %s (pushed)\n", entry.Name)
		}
	}
	return nil
}

// pushFeatureCheckout holds the pre-sync-modes push body verbatim. Checkout
// mode keeps failing with ErrWorktreeUnsupported and a nonzero exit; neither
// the layout resolver nor the GitBranch() ref fix reaches it.
func pushFeatureCheckout(feature string, dryRun bool) error {
	featurePath, err := internal.RequireFeaturePath(feature)
	if err != nil {
		return err
	}

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return fmt.Errorf("no stack.yaml found for feature: %s", feature)
	}

	for _, entry := range stack.Branches {
		path, err := internal.RequireWorktreePath(feature, entry.Name)
		if err != nil {
			return err
		}

		// Skip archived branches
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Printf("  [-] %s (archived, skipped)\n", entry.Name)
			continue
		}

		if dryRun {
			fmt.Printf("  [~] %s (would push --force-with-lease)\n", entry.Name)
			continue
		}

		// Determine repo context
		repoDir := path
		if entry.Repo != "" {
			repoDir = entry.Repo
		}

		runErr := internal.RunDirClean(repoDir, "git", "push", "--force-with-lease", "origin", entry.Name)
		if runErr != nil {
			fmt.Printf("  [x] %s (push failed)\n", entry.Name)
		} else {
			fmt.Printf("  [+] %s (pushed)\n", entry.Name)
		}
	}
	return nil
}
