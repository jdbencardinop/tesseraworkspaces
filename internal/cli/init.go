package cli

import (
	"fmt"
	"os"
	"path/filepath"

	skills "github.com/jdbencardinop/tesseraworkspaces/assets/skills"
	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func initCmd() *cobra.Command {
	var agent string
	var force bool
	var mode string
	var register bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install agent skills into current repo",
		Long: `Installs the tesseraworkspaces agent skill into the current repo.

Detects Claude Code or GitHub Copilot and installs the appropriate file.
Use --agent to override detection. Use --mode to set workspace_mode.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			// --register with checkout mode should fail early.
			if register && mode == "checkout" {
				return fmt.Errorf("--register is not supported with --mode checkout: workspace registry is not yet implemented")
			}

			// Even without checkout, --register is not implemented.
			if register {
				return fmt.Errorf("--register is not yet implemented; workspace registry is planned for a future release")
			}

			// Set workspace_mode if --mode is specified.
			if mode != "" {
				repoRoot, err := internal.MainRepoRoot()
				if err != nil {
					return fmt.Errorf("not inside a git repository: %w", err)
				}
				if err := enableWorkspaceMode(repoRoot, mode); err != nil {
					return err
				}
			}

			// Agent skill installation
			detected := detectAgent(cwd)
			target := agent
			if target == "" {
				target = detected
			}

			switch target {
			case "claude":
				installFile(filepath.Join(cwd, ".claude", "skills", "tesseraworkspaces", "SKILL.md"), skills.ClaudeSkill, force)
			case "copilot":
				installFile(filepath.Join(cwd, ".github", "copilot-instructions.md"), skills.CopilotSkill, force)
			default:
				installFile(filepath.Join(cwd, ".claude", "skills", "tesseraworkspaces", "SKILL.md"), skills.ClaudeSkill, force)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "Agent type: claude, copilot (default: auto-detect)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing skill file")
	cmd.Flags().StringVar(&mode, "mode", "", "Set workspace mode: external, checkout")
	cmd.Flags().BoolVar(&register, "register", false, "Register this workspace in the global registry (not yet implemented)")

	return cmd
}

// enableWorkspaceMode delegates to the unified internal helpers.
// Both init and enable use the same code path.
func enableWorkspaceMode(repoRoot, mode string) error {
	switch mode {
	case "checkout":
		if err := internal.EnableCheckoutMode(repoRoot); err != nil {
			return err
		}
		fmt.Printf("Workspace mode set to: %s\n", mode)
		return nil
	case "external":
		if err := internal.EnableExternalMode(repoRoot); err != nil {
			return err
		}
		fmt.Printf("Workspace mode set to: %s\n", mode)
		return nil
	default:
		return fmt.Errorf("invalid mode %q; supported: external, checkout", mode)
	}
}

// addGitExclude adds a pattern to .git/info/exclude idempotently.
// Delegates to the unified internal helper.
func addGitExclude(repoRoot, pattern string) error {
	return internal.AddGitLocalExclude(repoRoot, pattern)
}

func detectAgent(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ".claude")); err == nil {
		return "claude"
	}
	if _, err := os.Stat(filepath.Join(dir, ".github", "copilot-instructions.md")); err == nil {
		return "copilot"
	}
	return "claude"
}

func installFile(relPath string, content []byte, force bool) bool {
	if _, err := os.Stat(relPath); err == nil && !force {
		fmt.Printf("  exists: %s (use --force to overwrite)\n", relPath)
		return false
	}

	if err := os.MkdirAll(filepath.Dir(relPath), 0755); err != nil {
		fmt.Printf("  error: could not create directory for %s: %v\n", relPath, err)
		return false
	}

	if err := os.WriteFile(relPath, content, 0644); err != nil {
		fmt.Printf("  error: could not write %s: %v\n", relPath, err)
		return false
	}

	fmt.Printf("  installed: %s\n", relPath)
	return true
}
