package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func syncWithStack(feature, featurePath string, stack internal.Stack, sorted []internal.StackEntry) {
	skipped := make(map[string]bool)
	updatedByRef := make(map[string]bool)

	// Pass 1: rebase active branches with --update-refs
	for _, entry := range sorted {
		if skipped[entry.Name] {
			continue
		}

		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}

		base := resolveBase(entry.Base)

		err := internal.RunDir(path, "git", "rebase", "--update-refs", base)
		if err != nil {
			fmt.Println(formatSyncStatus(entry.Name, "active", "failed"))
			skipDescendants(stack, entry.Name, skipped)
		} else {
			fmt.Println(formatSyncStatus(entry.Name, "active", "synced"))
			markUpdatedAncestors(stack, entry.Name, featurePath, updatedByRef)
		}
	}

	// Pass 2: handle archived/missing branches
	for _, entry := range sorted {
		if skipped[entry.Name] {
			fmt.Println(formatSyncStatus(entry.Name, "skipped", "skipped"))
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
		err := internal.RunSilent("git", "rebase", base, entry.Name)
		if err != nil {
			_ = internal.RunSilent("git", "rebase", "--abort")
			fmt.Println(formatSyncStatus(entry.Name, "archived", "conflict"))
			fmt.Printf("    Restore with: tws new %s %s\n", feature, entry.Name)
			skipDescendants(stack, entry.Name, skipped)
		} else {
			fmt.Println(formatSyncStatus(entry.Name, "archived", "synced"))
		}
	}
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
		"synced":   "+",
		"failed":   "x",
		"skipped":  "-",
		"conflict": "!",
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
		internal.Must(internal.RunDir(path, "git", "rebase", "--update-refs", "origin/main"))
	}
}
