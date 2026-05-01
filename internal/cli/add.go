package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func Add(args []string) {
	if len(args) < 1 {
		println("Usage: tws add <feature>")
		return
	}

	feature := args[0]
	root := internal.FeaturePath(feature)

	// Ensure .tws-workspace marker exists in workspace root
	wsRoot := internal.TwsRoot()
	internal.Must(os.MkdirAll(filepath.Join(wsRoot, ".tws-workspace"), 0755))

	// TODO: make worktress a constant in internal package
	internal.Must(os.MkdirAll(filepath.Join(root, "worktrees"), 0755))

	// TODO: either add a slug arg or pass the entire feature description and slugify it here
	internal.Must(os.WriteFile(filepath.Join(root, "FEATURE.md"), []byte("# "+feature+"\n"), 0644))

	// TODO: Add a template for other files, here we just create a CLAUDE.local.md file
	internal.Must(os.WriteFile(filepath.Join(root, "CLAUDE.local.md"), []byte("# "+feature+" - CLAUDE shared context\n"), 0644))

	fmt.Println("Feature added:", feature)
}
