package cli

import (
	"fmt"
	"io"
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

			// Create inject/ dir
			injectDir := internal.InjectPath(root)
			internal.Must(os.MkdirAll(injectDir, 0755))

			// Copy template into inject/ if available
			templateDir := internal.TemplatePath()
			if templateDir != "" {
				count, err := copyDir(templateDir, injectDir)
				if err != nil {
					fmt.Printf("Warning: template copy failed: %v\n", err)
				} else if count > 0 {
					fmt.Printf("Copied %d file(s) from template\n", count)
				}
			}

			// Add default CLAUDE.local.md if not provided by template
			claudeLocal := filepath.Join(injectDir, "CLAUDE.local.md")
			if _, err := os.Stat(claudeLocal); os.IsNotExist(err) {
				internal.Must(os.WriteFile(claudeLocal, []byte("# "+feature+" - shared context\n\nThis file is symlinked into every worktree for this feature.\nEdit it in the workspace inject/ directory.\n"), 0644))
			}

			fmt.Println("Feature added:", feature)
		},
	}
}

// copyDir copies all files from src to dst, preserving directory structure.
// Does not overwrite existing files. Returns the count of files copied.
func copyDir(src, dst string) (int, error) {
	count := 0
	err := filepath.Walk(src, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, srcPath)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		// Skip if exists
		if _, err := os.Stat(dstPath); err == nil {
			return nil
		}

		// Copy file
		in, err := os.Open(srcPath)
		if err != nil {
			return err
		}
		defer in.Close() //nolint:errcheck

		out, err := os.Create(dstPath)
		if err != nil {
			return err
		}
		defer out.Close() //nolint:errcheck

		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}
