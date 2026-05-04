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
			internal.Must(os.WriteFile(filepath.Join(root, "CLAUDE.local.md"), []byte("# "+feature+" - CLAUDE shared context\n"), 0644))

			fmt.Println("Feature added:", feature)
		},
	}
}
