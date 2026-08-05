package internal

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnableCheckoutMode is the single, error-returning helper that both
// `tws init --mode checkout` and `tws enable --mode checkout` use.
// It:
//  1. Validates that repoRoot is the main worktree (rejects linked worktrees).
//  2. Creates .tws/, .tws/features/, .tws/state/ idempotently.
//  3. Writes/updates .tws/config.yaml preserving existing values.
//  4. Adds .tws/ to .git/info/exclude idempotently.
func EnableCheckoutMode(repoRoot string) error {
	if err := RejectLinkedWorktree(repoRoot); err != nil {
		return err
	}

	twsDir := filepath.Join(repoRoot, ".tws")
	featuresDir := filepath.Join(twsDir, "features")
	stateDir := filepath.Join(twsDir, "state")

	for _, d := range []string{twsDir, featuresDir, stateDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}

	configPath := filepath.Join(twsDir, "config.yaml")
	cfg := LoadRepoConfig(configPath)
	cfg.WorkspaceMode = "checkout"
	if err := SaveRepoConfig(configPath, cfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	if err := AddGitLocalExclude(repoRoot, ".tws/"); err != nil {
		return fmt.Errorf("updating git exclude: %w", err)
	}

	return nil
}

// EnableExternalMode writes config for external mode without creating
// checkout-specific directories.
func EnableExternalMode(repoRoot string) error {
	if err := RejectLinkedWorktree(repoRoot); err != nil {
		return err
	}

	twsDir := filepath.Join(repoRoot, ".tws")
	if err := os.MkdirAll(twsDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", twsDir, err)
	}

	configPath := filepath.Join(twsDir, "config.yaml")
	cfg := LoadRepoConfig(configPath)
	cfg.WorkspaceMode = "external"
	return SaveRepoConfig(configPath, cfg)
}

// AddGitLocalExclude adds a pattern to .git/info/exclude idempotently.
// Returns an error if .git is a file (linked worktree) rather than a directory.
func AddGitLocalExclude(repoRoot, pattern string) error {
	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return fmt.Errorf(".git not found in %s: %w", repoRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(".git is a file (linked worktree); exclude must be set from the main worktree")
	}

	excludeDir := filepath.Join(repoRoot, ".git", "info")
	if err := os.MkdirAll(excludeDir, 0755); err != nil {
		return err
	}
	excludePath := filepath.Join(excludeDir, "exclude")

	// Check if already present.
	if f, err := os.Open(excludePath); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == pattern {
				f.Close() //nolint:errcheck
				return nil
			}
		}
		f.Close() //nolint:errcheck
	}

	// Read existing to check trailing newline.
	existing, _ := os.ReadFile(excludePath)

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck

	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		_, _ = f.WriteString("\n")
	}
	_, err = f.WriteString(pattern + "\n")
	return err
}

// RejectLinkedWorktree checks that repoRoot contains a .git directory
// (not a .git file, which indicates a linked worktree).
func RejectLinkedWorktree(repoRoot string) error {
	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return fmt.Errorf("cannot stat .git in %s: %w", repoRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is a linked worktree (.git is a file); enable must be run from the main worktree", repoRoot)
	}
	return nil
}
