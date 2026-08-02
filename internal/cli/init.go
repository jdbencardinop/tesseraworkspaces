package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
				if err := enableWorkspaceMode(cwd, mode); err != nil {
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

// enableWorkspaceMode creates/updates .tws/config.yaml with the given mode.
// Preserves WorkspaceID (stable_id) and avoids duplicating exclude entries.
func enableWorkspaceMode(repoRoot, mode string) error {
	// Validate mode.
	switch mode {
	case "external", "checkout":
	default:
		return fmt.Errorf("invalid mode %q; supported: external, checkout", mode)
	}

	twsDir := filepath.Join(repoRoot, ".tws")
	if err := os.MkdirAll(twsDir, 0755); err != nil {
		return fmt.Errorf("creating .tws directory: %w", err)
	}

	configPath := filepath.Join(twsDir, "config.yaml")

	// Load existing config to preserve WorkspaceID and other settings.
	existingCfg := internal.LoadRepoConfig(configPath)
	existingCfg.WorkspaceMode = mode

	if err := internal.SaveRepoConfig(configPath, existingCfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	fmt.Printf("Workspace mode set to: %s\n", mode)

	// For checkout mode, add .tws/ to git exclude (idempotent).
	if mode == "checkout" {
		if err := addGitExclude(repoRoot, ".tws/"); err != nil {
			fmt.Printf("Warning: could not update git exclude: %v\n", err)
		}
	}

	return nil
}

// addGitExclude adds a pattern to .git/info/exclude idempotently.
func addGitExclude(repoRoot, pattern string) error {
	excludePath := filepath.Join(repoRoot, ".git", "info", "exclude")

	// Check if .git is a file (worktree) rather than a directory.
	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return fmt.Errorf(".git not found; is this a git repository?")
	}
	if !info.IsDir() {
		// .git is a file (worktree reference). We require main repo root.
		return fmt.Errorf(".git is a file (worktree); init --mode requires the main repository root")
	}

	// Ensure directory exists.
	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return err
	}

	// Read existing content and check for duplicates.
	existing, _ := os.ReadFile(excludePath)
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil // Already present.
		}
	}

	// Append the pattern.
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck

	content := string(existing)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		_, _ = f.WriteString("\n")
	}
	_, err = f.WriteString(pattern + "\n")
	return err
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
