package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraspaces/internal"
)

func Sync(args []string) {
	if len(args) < 1 {
		println("Usage: ts sync <feature>")
		return
	}

	internal.RequireTool("git")

	feature := args[0]
	featurePath := internal.FeaturePath(feature)

	internal.Must(internal.Run("git", "fetch"))

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		// No stack.yaml — fall back to rebasing all worktrees against origin/main
		syncFallback(featurePath)
		return
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	skipped := make(map[string]bool)

	for _, entry := range sorted {
		if skipped[entry.Name] {
			fmt.Println(internal.FormatBranchStatus(entry.Name, "skipped"))
			continue
		}

		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Println(internal.FormatBranchStatus(entry.Name, "skipped"))
			continue
		}

		base := entry.Base
		if base == "main" {
			base = "origin/main"
		}

		err := internal.RunDir(path, "git", "rebase", base)
		if err != nil {
			fmt.Println(internal.FormatBranchStatus(entry.Name, "failed"))
			// Skip all descendants
			descs := internal.Descendants(stack, entry.Name)
			for d := range descs {
				skipped[d] = true
			}
			fmt.Printf("    Skipping descendants: %s\n", internal.DescendantsList(stack, entry.Name))
		} else {
			fmt.Println(internal.FormatBranchStatus(entry.Name, "synced"))
		}
	}
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
