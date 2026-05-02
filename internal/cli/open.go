package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func Open(args []string) {
	if len(args) < 2 {
		println("Usage: tws open <feature> <branch>")
		return
	}

	internal.RequireTool("tmux")

	feature := args[0]
	branch := args[1]

	path := internal.WorktreePath(feature, branch)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("Worktree not found: %s\n", path)
		os.Exit(1)
	}

	session := sanitizeSessionName(feature + "/" + branch)

	// Check if session already exists
	if sessionExists(session) {
		fmt.Printf("Attaching to existing session: %s\n", session)
		internal.Must(internal.Run("tmux", "attach", "-t", session))
		return
	}

	// Create new session in the worktree directory
	fmt.Printf("Creating session: %s\n", session)
	internal.Must(internal.Run("tmux", "new-session", "-d", "-s", session, "-c", path))

	// Launch claude — use -c (continue) if a session exists for this worktree
	claudeCmd := "claude"
	if hasClaudeSession(path) {
		claudeCmd = "claude -c"
	}
	fmt.Printf("Running: %s\n", claudeCmd)
	internal.Must(internal.Run("tmux", "send-keys", "-t", session, claudeCmd, "Enter"))

	// Attach
	internal.Must(internal.Run("tmux", "attach", "-t", session))
}

func sessionExists(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	err := cmd.Run()
	return err == nil
}

// sanitizeSessionName replaces characters tmux doesn't allow in session names.
func sanitizeSessionName(s string) string {
	r := strings.NewReplacer(".", "_", ":", "_", "/", "-")
	return r.Replace(s)
}

// hasClaudeSession checks if Claude Code has an existing session for the given
// working directory by looking for a project folder in ~/.claude/projects/.
// Claude encodes paths by replacing / with - and prepending -.
func hasClaudeSession(workdir string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	absPath, err := filepath.Abs(workdir)
	if err != nil {
		return false
	}

	// Claude encodes project paths as: -Users-name-projects-repo
	encoded := strings.ReplaceAll(absPath, string(filepath.Separator), "-")
	projectDir := filepath.Join(home, ".claude", "projects", encoded)

	info, err := os.Stat(projectDir)
	if err != nil {
		return false
	}
	return info.IsDir()
}
