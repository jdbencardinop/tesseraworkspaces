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
	root := internal.FeaturePath(feature)

	entries, _ := os.ReadDir(filepath.Join(root, "worktrees"))

	internal.Must(internal.Run("git", "fetch"))

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, "worktrees", e.Name())
		fmt.Printf("Syncing worktree: %s\n", path)
		internal.Must(internal.RunDir(path, "git", "rebase", "--update-refs", "origin/main"))
	}
}
