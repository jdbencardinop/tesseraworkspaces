package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	skills "github.com/jdbencardinop/tesseraworkspaces/assets/skills"
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
		RunE: func(cmd *cobra.Command, args []string) error {
			feature := args[0]

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			if ws.Mode == internal.ModeCheckout {
				return addCheckout(ws, feature, templates, newBranch, base, force, open, useTmux)
			}
			return addExternal(feature, templates, newBranch, base, force, open, useTmux)
		},
	}

	cmd.Flags().StringArrayVar(&templates, "template", nil, "Template directory to copy into inject/ (can be specified multiple times)")
	cmd.Flags().StringVarP(&newBranch, "new", "n", "", "Also create a worktree branch")
	cmd.Flags().StringVar(&base, "base", "", "Base branch for the new worktree (default: repo's default branch)")
	cmd.Flags().BoolVar(&open, "open", false, "Open the worktree after creation (used with -n)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force checkout of already checked-out branch")
	cmd.Flags().BoolVar(&useTmux, "tmux", false, "Open in tmux session (used with --open)")

	return cmd
}

// addExternal preserves existing external-mode add semantics.
func addExternal(feature string, templates []string, newBranch, base string, force, open, useTmux bool) error {
	wsRoot := internal.TwsRoot()
	if err := internal.GuardFeatureName(wsRoot, feature); err != nil {
		return err
	}

	root := internal.FeaturePath(feature)

	if err := internal.EnsureExternalWorkspaceMarker(wsRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "worktrees"), 0755); err != nil {
		return fmt.Errorf("creating worktrees directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "FEATURE.md"), []byte("# "+feature+"\n"), 0644); err != nil {
		return fmt.Errorf("creating FEATURE.md: %w", err)
	}

	injectDir := internal.InjectPath(root)
	if err := os.MkdirAll(injectDir, 0755); err != nil {
		return fmt.Errorf("creating inject directory: %w", err)
	}

	applyTemplates(injectDir, templates)

	claudeLocal := filepath.Join(injectDir, "CLAUDE.local.md")
	if _, err := os.Stat(claudeLocal); os.IsNotExist(err) {
		if err := os.WriteFile(claudeLocal, []byte("# "+feature+" - shared context\n\nThis file is symlinked into every worktree for this feature.\nEdit it in the workspace inject/ directory.\n"), 0644); err != nil {
			return fmt.Errorf("creating CLAUDE.local.md: %w", err)
		}
	}

	orchPath := filepath.Join(root, ".claude", "skills", "tesseraworkspaces-orchestrator", "SKILL.md")
	installFile(orchPath, skills.ClaudeOrchestratorSkill, false)

	fmt.Println("Feature added:", feature)

	if newBranch != "" {
		if err := createWorktree(feature, newBranch, base, "", force); err != nil {
			return err
		}
		if open {
			path := internal.WorktreePath(feature, newBranch)
			if useTmux {
				openWithTmux(feature, newBranch, path)
			} else {
				openDirect(path)
			}
		}
	}
	return nil
}

// addCheckout creates durable feature assets under .tws/features/<feature>
// for checkout mode. Idempotent: re-running on an existing feature updates
// only missing assets.
func addCheckout(ws internal.Workspace, feature string, templates []string, newBranch, base string, force, open, useTmux bool) error {
	if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return err
	}

	root := ws.FeaturePath(feature)

	// Create the feature directory (no worktrees/ subdirectory in checkout mode).
	if err := os.MkdirAll(root, 0755); err != nil {
		return fmt.Errorf("creating feature directory: %w", err)
	}

	// FEATURE.md
	featureMD := filepath.Join(root, "FEATURE.md")
	if _, err := os.Stat(featureMD); os.IsNotExist(err) {
		if err := os.WriteFile(featureMD, []byte("# "+feature+"\n"), 0644); err != nil {
			return fmt.Errorf("creating FEATURE.md: %w", err)
		}
	}

	// inject/ directory
	injectDir := internal.InjectPath(root)
	if err := os.MkdirAll(injectDir, 0755); err != nil {
		return fmt.Errorf("creating inject directory: %w", err)
	}

	applyTemplates(injectDir, templates)

	// Default CLAUDE.local.md if no template provided one
	claudeLocal := filepath.Join(injectDir, "CLAUDE.local.md")
	if _, err := os.Stat(claudeLocal); os.IsNotExist(err) {
		if err := os.WriteFile(claudeLocal, []byte("# "+feature+" - shared context\n\nThis file is maintained in the checkout workspace.\n"), 0644); err != nil {
			return fmt.Errorf("creating CLAUDE.local.md: %w", err)
		}
	}

	// Orchestrator skill in feature .claude/skills/
	orchPath := filepath.Join(root, ".claude", "skills", "tesseraworkspaces-orchestrator", "SKILL.md")
	installFile(orchPath, skills.ClaudeOrchestratorSkill, false)

	fmt.Println("Feature added:", feature)

	// In checkout mode, -n creates a branch (not a worktree).
	if newBranch != "" {
		if err := createCheckoutBranch(ws, feature, newBranch, base, force); err != nil {
			return err
		}
		if open {
			// In checkout mode, "open" switches to branch in the repo root.
			if useTmux {
				openCheckoutTmux(ws, feature, newBranch)
			} else {
				fmt.Printf("Branch %s created. Use 'git checkout %s' to switch.\n", newBranch, newBranch)
			}
		}
	}
	return nil
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
