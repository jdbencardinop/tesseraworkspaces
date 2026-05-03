package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func Sync(args []string) {
	if len(args) < 1 {
		println("Usage: tws sync <feature>")
		return
	}

	internal.RequireTool("git")

	feature := args[0]
	featurePath := internal.FeaturePath(feature)

	internal.Must(internal.Run("git", "fetch"))

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		syncFallback(featurePath)
		return
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	skipped := make(map[string]bool)
	updatedByRef := make(map[string]bool)

	// Pass 1: rebase active branches with --update-refs
	for _, entry := range sorted {
		if skipped[entry.Name] {
			continue
		}

		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// Archived — handle in pass 2
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

	// Pass 2: handle archived/missing branches not yet updated
	for _, entry := range sorted {
		if skipped[entry.Name] {
			fmt.Println(formatSyncStatus(entry.Name, "skipped", "skipped"))
			continue
		}

		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); err == nil {
			// Active — already handled in pass 1
			continue
		}

		// Detect missing (stale) vs archived (clean)
		if internal.IsPrunableWorktree(entry.Name) {
			fmt.Printf("  [?] %s (missing — stale worktree ref, run: tws archive %s %s or tws new %s %s)\n",
				entry.Name, feature, entry.Name, feature, entry.Name)
			continue
		}

		if updatedByRef[entry.Name] {
			fmt.Println(formatSyncStatus(entry.Name, "archived", "synced"))
			continue
		}

		// Optimistic rebase without worktree
		base := resolveBase(entry.Base)
		err := internal.RunSilent("git", "rebase", base, entry.Name)
		if err != nil {
			internal.RunSilent("git", "rebase", "--abort")
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

// markUpdatedAncestors walks up the stack from a branch and marks any archived
// ancestors as updated (they were handled by --update-refs).
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
