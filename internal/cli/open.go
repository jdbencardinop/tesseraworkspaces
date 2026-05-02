package cli

import (
	"fmt"
	"os"
	"os/exec"
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

	// Launch claude (without -c, since this is a fresh session)
	internal.Must(internal.Run("tmux", "send-keys", "-t", session, "claude", "Enter"))

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
