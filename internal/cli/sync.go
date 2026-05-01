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

	feature := args[0]
	root := internal.FeaturePath(feature)

	entries, _ := os.ReadDir(filepath.Join(root, "worktrees"))

	internal.Must(internal.Run("git", "fetch"))

	for _, e := range entries {
		path := filepath.Join(root, "worktrees", e.Name())
		fmt.Printf("Syncing worktree: %s\n", path)
		// TODO: verify the behaviour of this, we might not need to rebase everything everytime
		// TODO: configure remote and branch in case we want to change the target branch or remote
		internal.Must(internal.Run("git", "rebase", "--update-refs", "origin/main"))
	}
}
