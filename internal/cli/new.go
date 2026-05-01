package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraspaces/internal"
)

func New(args []string) {
	if len(args) < 2 {
		println("Usage: ts new <feature> <branch>")
		return
	}

	internal.RequireTool("git")

	feature := args[0]
	branch := args[1]

	path := internal.WorktreePath(feature, branch)

	// Ensure parent directory exists
	internal.Must(os.MkdirAll(internal.FeaturePath(feature), 0755))

	// Create the worktree at the feature-scoped path
	repoRoot, err := internal.MainRepoRoot()
	if err != nil {
		fmt.Println("Error: must be run from inside a git repository")
		os.Exit(1)
	}

	internal.Must(internal.RunDir(repoRoot, "git", "worktree", "add", path, "-b", branch))

	fmt.Println("Worktree created:", path)
}
