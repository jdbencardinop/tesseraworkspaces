package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func syncWithStack(feature, featurePath string, stack internal.Stack, sorted []internal.StackEntry) {
	syncWithStackFiltered(feature, featurePath, stack, sorted, nil)
}

func syncWithStackFiltered(feature, featurePath string, stack internal.Stack, sorted []internal.StackEntry, alreadyDone map[string]bool) {
	skipped := make(map[string]bool)
	updatedByRef := make(map[string]bool)
	completed := make([]string, 0)

	// Collect already-done branches from --continue
	if alreadyDone == nil {
		alreadyDone = make(map[string]bool)
	}

	// Pass 1: rebase active branches with --update-refs
	for _, entry := range sorted {
		if skipped[entry.Name] || alreadyDone[entry.Name] {
			continue
		}

		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}

		// Health check: verify worktree is on the expected branch
		if issue := internal.CheckWorktreeBranch(path, entry.Name); issue != nil {
			fmt.Println(formatSyncStatus(entry.Name, "active", "wrong-branch"))
			fmt.Printf("    %s\n", issue.Problem)
			fmt.Printf("    %s\n", issue.Hint)
			skipDescendants(stack, entry.Name, skipped)
			continue
		}

		base := resolveBase(entry.Base)

		// Determine git context for resolving SHAs
		gitContext := path
		if entry.Repo != "" {
			gitContext = entry.Repo
		}

		// Build rebase args — use --onto if we have a previous base SHA
		// to avoid ghost conflicts from amended commits
		var rebaseArgs []string
		currentBaseSHA := internal.GetBranchSHA(gitContext, base)
		if entry.LastBaseSHA != "" && currentBaseSHA != "" && entry.LastBaseSHA != currentBaseSHA {
			// Base has changed (amended/rebased) — use --onto to skip stale commits
			rebaseArgs = []string{"rebase", "--update-refs", "--onto", base, entry.LastBaseSHA}
		} else {
			rebaseArgs = []string{"rebase", "--update-refs", base}
		}

		err := internal.RunDirClean(path, "git", rebaseArgs...)
		if err != nil {
			fmt.Println(formatSyncStatus(entry.Name, "active", "conflict"))

			// Collect pending branches
			pending := collectPending(sorted, entry.Name, skipped, alreadyDone, completed)
			skippedList := collectSkippedNames(stack, entry.Name)

			// Save state for --continue
			state := internal.NewSyncState()
			state.FailedBranch = entry.Name
			state.Pending = pending
			state.Completed = completed
			state.Skipped = skippedList
			_ = internal.SaveSyncState(featurePath, state)

			fmt.Printf("    Resolve conflicts in: %s\n", path)
			fmt.Println("    Then run: git add . && git rebase --continue")
			fmt.Printf("    Resume with: tws sync %s --continue\n", feature)
			skipDescendants(stack, entry.Name, skipped)
			return
		}

		// Post-rebase validation
		if !runValidation(path, entry.Name) {
			skipDescendants(stack, entry.Name, skipped)
		} else {
			fmt.Println(formatSyncStatus(entry.Name, "active", "synced"))
			markUpdatedAncestors(stack, entry.Name, featurePath, updatedByRef)
			completed = append(completed, entry.Name)

			// Update last_base_sha for next sync
			newBaseSHA := internal.GetBranchSHA(gitContext, base)
			if newBaseSHA != "" {
				internal.UpdateBaseSHA(&stack, entry.Name, newBaseSHA)
				_ = internal.SaveStack(featurePath, stack)
			}
		}
	}

	// Pass 2: handle archived/missing branches
	for _, entry := range sorted {
		if skipped[entry.Name] || alreadyDone[entry.Name] {
			if skipped[entry.Name] {
				fmt.Println(formatSyncStatus(entry.Name, "skipped", "skipped"))
			}
			continue
		}

		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); err == nil {
			continue
		}

		if internal.IsPrunableWorktree(entry.Name) {
			fmt.Printf("  [?] %s (missing — stale worktree ref, run: tws archive %s %s or tws new %s %s)\n",
				entry.Name, feature, entry.Name, feature, entry.Name)
			continue
		}

		if updatedByRef[entry.Name] {
			fmt.Println(formatSyncStatus(entry.Name, "archived", "synced"))
			continue
		}

		base := resolveBase(entry.Base)

		rebaseDir := ""
		if entry.Repo != "" {
			rebaseDir = entry.Repo
		}

		var rebaseErr error
		if rebaseDir != "" {
			rebaseErr = internal.RunSilentDir(rebaseDir, "git", "rebase", base, entry.Name)
		} else {
			rebaseErr = internal.RunSilent("git", "rebase", base, entry.Name)
		}

		if rebaseErr != nil {
			if rebaseDir != "" {
				_ = internal.RunSilentDir(rebaseDir, "git", "rebase", "--abort")
			} else {
				_ = internal.RunSilent("git", "rebase", "--abort")
			}
			fmt.Println(formatSyncStatus(entry.Name, "archived", "conflict"))
			fmt.Printf("    Restore with: tws new %s %s\n", feature, entry.Name)
			skipDescendants(stack, entry.Name, skipped)
		} else {
			fmt.Println(formatSyncStatus(entry.Name, "archived", "synced"))
		}
	}

	// Clean up state if we completed everything
	internal.DeleteSyncState(featurePath)
}

func collectPending(sorted []internal.StackEntry, failedBranch string, skipped, done map[string]bool, completed []string) []string {
	completedSet := make(map[string]bool)
	for _, b := range completed {
		completedSet[b] = true
	}

	found := false
	var pending []string
	for _, entry := range sorted {
		if entry.Name == failedBranch {
			found = true
			continue
		}
		if found && !skipped[entry.Name] && !done[entry.Name] && !completedSet[entry.Name] {
			pending = append(pending, entry.Name)
		}
	}
	return pending
}

func collectSkippedNames(stack internal.Stack, branch string) []string {
	descs := internal.Descendants(stack, branch)
	var names []string
	for d := range descs {
		names = append(names, d)
	}
	return names
}

func resolveBase(base string) string {
	if base == "main" {
		return "origin/main"
	}
	return base
}

func markUpdatedAncestors(stack internal.Stack, branch string, featurePath string, updated map[string]bool) {
	entryMap := make(map[string]internal.StackEntry)
	for _, e := range stack.Branches {
		entryMap[e.Name] = e
	}

	current := branch
	for {
		entry, ok := entryMap[current]
		if !ok {
			break
		}
		parent, ok := entryMap[entry.Base]
		if !ok {
			break
		}
		parentPath := filepath.Join(featurePath, "worktrees", parent.Name)
		if _, err := os.Stat(parentPath); os.IsNotExist(err) {
			updated[parent.Name] = true
		}
		current = parent.Name
	}
}

func skipDescendants(stack internal.Stack, branch string, skipped map[string]bool) {
	descs := internal.Descendants(stack, branch)
	for d := range descs {
		skipped[d] = true
	}
	if len(descs) > 0 {
		fmt.Printf("    Skipping descendants: %s\n", internal.DescendantsList(stack, branch))
	}
}

func formatSyncStatus(name, mode, status string) string {
	symbols := map[string]string{
		"synced":            "+",
		"failed":            "x",
		"skipped":           "-",
		"conflict":          "!",
		"resolved":          "~",
		"wrong-branch":      "?",
		"validation-failed": "x",
	}
	sym := symbols[status]
	if sym == "" {
		sym = "?"
	}
	return fmt.Sprintf("  [%s] %s (%s)", sym, name, mode)
}

func syncFallback(featurePath string) {
	entries, _ := os.ReadDir(filepath.Join(featurePath, "worktrees"))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(featurePath, "worktrees", e.Name())
		fmt.Printf("Syncing worktree: %s\n", path)
		internal.Must(internal.RunDirClean(path, "git", "rebase", "--update-refs", "origin/main"))
	}
}

// runValidation runs the configured test_command after a successful rebase.
func runValidation(worktreePath, branchName string) bool {
	cfg := internal.LoadConfig()
	if cfg.TestCommand == "" {
		return true
	}

	fmt.Printf("    validating %s: %s... ", branchName, cfg.TestCommand)
	parts := strings.Fields(cfg.TestCommand)
	err := internal.RunSilentDir(worktreePath, parts[0], parts[1:]...)
	if err != nil {
		fmt.Println("FAILED")
		fmt.Println(formatSyncStatus(branchName, "active", "validation-failed"))
		return false
	}
	fmt.Println("ok")
	return true
}
