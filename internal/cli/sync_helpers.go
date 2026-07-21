package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

type syncResult struct {
	Complete bool
	Failed   string
}

func syncWithStack(feature, featurePath string, stack internal.Stack, sorted []internal.StackEntry) syncResult {
	return syncWithStackFiltered(feature, featurePath, stack, sorted, nil)
}

func syncWithStackFiltered(feature, featurePath string, stack internal.Stack, sorted []internal.StackEntry, alreadyDone map[string]bool) syncResult {
	updatedByRef := make(map[string]bool)
	completed := completedNames(alreadyDone)
	if alreadyDone == nil {
		alreadyDone = make(map[string]bool)
	}

	for _, entry := range sorted {
		if alreadyDone[entry.Name] {
			continue
		}
		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}

		if issue := checkSyncWorktreeBranch(path, entry); issue != nil {
			fmt.Println(formatSyncStatus(entry.Name, "active", "wrong-branch"))
			fmt.Printf("    %s\n    %s\n", issue.Problem, issue.Hint)
			return saveIncompleteSync(featurePath, sorted, completed, entry.Name)
		}

		base := resolveEntryBase(stack, entry)
		gitContext := path
		if entry.Repo != "" {
			gitContext = entry.Repo
		}
		currentBaseSHA := internal.GetBranchSHA(gitContext, base)
		rebaseArgs := []string{"rebase", "--update-refs", base}
		if entry.LastBaseSHA != "" && currentBaseSHA != "" && entry.LastBaseSHA != currentBaseSHA {
			rebaseArgs = []string{"rebase", "--update-refs", "--onto", base, entry.LastBaseSHA}
		}

		if err := internal.RunDirClean(path, "git", rebaseArgs...); err != nil {
			fmt.Println(formatSyncStatus(entry.Name, "active", "conflict"))
			fmt.Printf("    Resolve conflicts in: %s\n", path)
			fmt.Println("    Then run: git add . && git rebase --continue")
			fmt.Printf("    Resume with: tws sync %s --continue\n", feature)
			return saveIncompleteSync(featurePath, sorted, completed, entry.Name)
		}
		if !runValidation(path, entry.Name) {
			return saveIncompleteSync(featurePath, sorted, completed, entry.Name)
		}

		fmt.Println(formatSyncStatus(entry.Name, "active", "synced"))
		markUpdatedAncestors(stack, entry.Name, featurePath, updatedByRef)
		completed = append(completed, entry.Name)
		alreadyDone[entry.Name] = true
		if currentBaseSHA != "" {
			internal.UpdateBaseSHA(&stack, entry.Name, currentBaseSHA)
			_ = internal.SaveStack(featurePath, stack)
		}
	}

	for _, entry := range sorted {
		if alreadyDone[entry.Name] {
			continue
		}
		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if internal.IsPrunableWorktree(entry.GitBranch()) {
			fmt.Printf("  [?] %s (missing — run: tws archive %s %s or tws new %s %s)\n", entry.Name, feature, entry.Name, feature, entry.Name)
			return saveIncompleteSync(featurePath, sorted, completed, entry.Name)
		}
		if updatedByRef[entry.Name] {
			fmt.Println(formatSyncStatus(entry.Name, "archived", "synced"))
			completed = append(completed, entry.Name)
			alreadyDone[entry.Name] = true
			continue
		}

		base := resolveEntryBase(stack, entry)
		rebaseDir := entry.Repo
		var err error
		if rebaseDir != "" {
			err = internal.RunSilentDir(rebaseDir, "git", "rebase", base, entry.GitBranch())
		} else {
			err = internal.RunSilent("git", "rebase", base, entry.GitBranch())
		}
		if err != nil {
			if rebaseDir != "" {
				_ = internal.RunSilentDir(rebaseDir, "git", "rebase", "--abort")
			} else {
				_ = internal.RunSilent("git", "rebase", "--abort")
			}
			fmt.Println(formatSyncStatus(entry.Name, "archived", "conflict"))
			fmt.Printf("    Restore with: tws new %s %s\n", feature, entry.Name)
			return saveIncompleteSync(featurePath, sorted, completed, entry.Name)
		}
		fmt.Println(formatSyncStatus(entry.Name, "archived", "synced"))
		completed = append(completed, entry.Name)
		alreadyDone[entry.Name] = true
	}

	if stale := staleStackEdges(feature, stack); len(stale) > 0 {
		fmt.Println("Sync incomplete; stale stack edges remain:")
		for _, edge := range stale {
			fmt.Printf("  [!] %s\n", edge)
		}
		return saveIncompleteSync(featurePath, sorted, completed, "")
	}
	internal.DeleteSyncState(featurePath)
	return syncResult{Complete: true}
}

func saveIncompleteSync(featurePath string, sorted []internal.StackEntry, completed []string, failed string) syncResult {
	completedSet := make(map[string]bool, len(completed))
	for _, name := range completed {
		completedSet[name] = true
	}
	pending := make([]string, 0)
	for _, entry := range sorted {
		if entry.Name != failed && !completedSet[entry.Name] {
			pending = append(pending, entry.Name)
		}
	}
	state := internal.NewSyncState()
	state.FailedBranch = failed
	state.Pending = pending
	state.Completed = append([]string(nil), completed...)
	_ = internal.SaveSyncState(featurePath, state)
	return syncResult{Failed: failed}
}

func completedNames(done map[string]bool) []string {
	var names []string
	for name, ok := range done {
		if ok {
			names = append(names, name)
		}
	}
	return names
}

func staleStackEdges(feature string, stack internal.Stack) []string {
	var stale []string
	for _, child := range stack.Branches {
		parent := internal.GetBranch(stack, child.Base)
		if parent.Name == "" || !sameStackRepo(parent.Repo, child.Repo) {
			continue
		}
		childPath := internal.WorktreePath(feature, child.Name)
		if _, err := os.Stat(childPath); err != nil {
			continue
		}
		if internal.RunSilentDir(childPath, "git", "merge-base", "--is-ancestor", parent.GitBranch(), child.GitBranch()) != nil {
			stale = append(stale, fmt.Sprintf("%s does not contain parent %s", child.Name, parent.Name))
		}
	}
	return stale
}

func checkSyncWorktreeBranch(worktreePath string, entry internal.StackEntry) *internal.HealthIssue {
	return internal.CheckWorktreeBranch(worktreePath, entry.GitBranch())
}

func resolveEntryBase(stack internal.Stack, entry internal.StackEntry) string {
	if parent := internal.GetBranch(stack, entry.Base); parent.Name != "" && sameStackRepo(parent.Repo, entry.Repo) {
		return parent.GitBranch()
	}
	return resolveBase(entry.Base)
}

func resolveBase(base string) string {
	defaultBranch := internal.DefaultBranch()
	if base == defaultBranch {
		return "origin/" + defaultBranch
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

func formatSyncStatus(name, mode, status string) string {
	symbols := map[string]string{"synced": "+", "failed": "x", "skipped": "-", "conflict": "!", "resolved": "~", "wrong-branch": "?", "validation-failed": "x"}
	sym := symbols[status]
	if sym == "" {
		sym = "?"
	}
	return fmt.Sprintf("  [%s] %s (%s)", sym, name, mode)
}

func syncFallback(featurePath string) {
	entries, _ := os.ReadDir(filepath.Join(featurePath, "worktrees"))
	for _, e := range entries {
		if e.IsDir() {
			path := filepath.Join(featurePath, "worktrees", e.Name())
			fmt.Printf("Syncing worktree: %s\n", path)
			internal.Must(internal.RunDirClean(path, "git", "rebase", "--update-refs", "origin/main"))
		}
	}
}

func runValidation(worktreePath, branchName string) bool {
	cfg := internal.LoadConfig()
	if cfg.TestCommand == "" {
		return true
	}
	fmt.Printf("    validating %s: %s... ", branchName, cfg.TestCommand)
	parts := strings.Fields(cfg.TestCommand)
	if err := internal.RunSilentDir(worktreePath, parts[0], parts[1:]...); err != nil {
		fmt.Println("FAILED")
		fmt.Println(formatSyncStatus(branchName, "active", "validation-failed"))
		return false
	}
	fmt.Println("ok")
	return true
}
