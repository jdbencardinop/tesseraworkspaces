package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func addCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <feature>",
		Short: "Create a feature workspace",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			feature := args[0]
			root := internal.FeaturePath(feature)

			wsRoot := internal.TwsRoot()
			internal.Must(os.MkdirAll(filepath.Join(wsRoot, ".tws-workspace"), 0755))
			internal.Must(os.MkdirAll(filepath.Join(root, "worktrees"), 0755))
			internal.Must(os.WriteFile(filepath.Join(root, "FEATURE.md"), []byte("# "+feature+"\n"), 0644))

			// Create inject/ dir with default CLAUDE.local.md
			injectDir := internal.InjectPath(root)
			internal.Must(os.MkdirAll(injectDir, 0755))
			claudeLocal := filepath.Join(injectDir, "CLAUDE.local.md")
			if _, err := os.Stat(claudeLocal); os.IsNotExist(err) {
				internal.Must(os.WriteFile(claudeLocal, []byte("# "+feature+" - shared context\n\nThis file is symlinked into every worktree for this feature.\nEdit it in the workspace inject/ directory.\n"), 0644))
			}

			fmt.Println("Feature added:", feature)
		},
	}
}
