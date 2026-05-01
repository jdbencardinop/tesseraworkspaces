package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraspaces/internal"
)

func New(args []string) {
	if len(args) < 2 {
		println("Usage: ts new <feature> <branch>")
		return
	}

	feature := args[0]
	branch := args[1]

	path := internal.WorktreePath(feature, branch)

	// Create the worktree directory if it doesn't exist
	// TODO: we should validate if Worktruink is installed and available in PATH
	// we should consider making this an interface and allowing users to choose between different git worktree implementations
	// we could probably do one of our own that is more lightweight and doesn't require git, but for now we can just use worktrunk
	// https://github.com/max-sixty/worktrunk
	internal.Must(internal.Run("wt", "switch", "-c", branch))

	// TODO: we should probably move this to a template file and copy it over instead of creating it from scratch
	// also figure out what does .cl stand for and if we need it or if we can just use the CLAUDE.local.md file we created in the add command
	internal.Must(os.MkdirAll(filepath.Join(path, ".cl"), 0755))

	fmt.Println("Worktree created for feature:", feature, "branch:", branch)
}
