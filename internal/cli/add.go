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
	var templates []string
	var newBranch string
	var base string
	var open bool
	var force bool
	var useTmux bool

	cmd := &cobra.Command{
		Use:   "add <feature>",
		Short: "Create a feature workspace",
		Long: `Create a feature workspace with an inject/ directory for shared files.

Files in inject/ are symlinked into every worktree. Use -n to also
create a first worktree branch, and --open to launch the agent.

Note: injected files appear as untracked in git status. Add them to
.gitignore or place them in an already-ignored subfolder.`,
		Args: cobra.ExactArgs(1),
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

			// Apply templates
			applyTemplates(injectDir, templates)

			// Add default CLAUDE.local.md if not provided by any template
			claudeLocal := filepath.Join(injectDir, "CLAUDE.local.md")
			if _, err := os.Stat(claudeLocal); os.IsNotExist(err) {
				internal.Must(os.WriteFile(claudeLocal, []byte("# "+feature+" - shared context\n\nThis file is symlinked into every worktree for this feature.\nEdit it in the workspace inject/ directory.\n"), 0644))
			}

			fmt.Println("Feature added:", feature)

			// Quick start: create worktree if -n specified
			if newBranch != "" {
				createWorktree(feature, newBranch, base, force)

				if open {
					path := internal.WorktreePath(feature, newBranch)
					if useTmux {
						openWithTmux(feature, newBranch, path)
					} else {
						openDirect(path)
					}
				}
			}
		},
	}

	cmd.Flags().StringArrayVar(&templates, "template", nil, "Template directory to copy into inject/ (can be specified multiple times)")
	cmd.Flags().StringVarP(&newBranch, "new", "n", "", "Also create a worktree branch")
	cmd.Flags().StringVar(&base, "base", "main", "Base branch for the new worktree (used with -n)")
	cmd.Flags().BoolVar(&open, "open", false, "Open the worktree after creation (used with -n)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force checkout of already checked-out branch")
	cmd.Flags().BoolVar(&useTmux, "tmux", false, "Open in tmux session (used with --open)")

	return cmd
}

// applyTemplates copies files from the configured template dir and any
// explicit --template dirs into the inject directory.
func applyTemplates(injectDir string, extraTemplates []string) {
	// Configured default template first
	defaultTemplate := internal.TemplatePath()
	if defaultTemplate != "" {
		count, err := copyDir(defaultTemplate, injectDir)
		if err != nil {
			fmt.Printf("Warning: default template copy failed: %v\n", err)
		} else if count > 0 {
			fmt.Printf("Copied %d file(s) from default template\n", count)
		}
	}

	// Then explicit --template dirs (layered in order)
	for _, tmplDir := range extraTemplates {
		info, err := os.Stat(tmplDir)
		if err != nil || !info.IsDir() {
			fmt.Printf("Warning: template directory not found: %s\n", tmplDir)
			continue
		}
		count, err := copyDir(tmplDir, injectDir)
		if err != nil {
			fmt.Printf("Warning: template copy failed for %s: %v\n", tmplDir, err)
		} else if count > 0 {
			fmt.Printf("Copied %d file(s) from %s\n", count, tmplDir)
		}
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

		// Skip if exists (conflict resolution is a separate feature)
		if _, err := os.Stat(dstPath); err == nil {
			fmt.Printf("  skip: %s (exists)\n", relPath)
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
