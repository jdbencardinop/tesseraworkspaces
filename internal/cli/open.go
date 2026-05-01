package cli

import "github.com/jdbencardinop/tesseraworkspaces/internal"

func Open(args []string) {
	if len(args) < 2 {
		println("Usage: tws open <feature> <branch>")
		return
	}

	internal.RequireTool("tmux")

	feature := args[0]
	branch := args[1]

	path := internal.WorktreePath(feature, branch)
	session := feature + "__" + branch

	// TODO: we should validate if tmux is installed and available in PATH
	// we should consider making this an interface and allowing users to choose between different terminal multiplexers
	// we could probably do one of our own that is more lightweight and doesn't require tmux, but for now we can just use tmux or tmuxinator

	// Create tmux session if it doesn't exist
	internal.Run("tmux", "has-session", "-t", session)
	// if not exist, create it
	internal.Run("tmux", "new-session", "-d", "-s", session, "-c", path)

	// Configure tmux session with some default panes and commands
	// TODO: we should probably move this to a template file and copy it over instead of creating it from scratch

	// TODO: this pane should only be shown for the first time, we can probably check if the user has already opened this session before and skip this part if they have, we can store this information in a file in the feature directory or in a global config file
	internal.Run("tmux", "send-keys", "-t", session, "clear", "Enter")
	internal.Run("tmux", "send-keys", "-t", session, "echo 'Welcome to your Tesseraspaces session!'", "Enter")
	internal.Run("tmux", "send-keys", "-t", session, "echo 'Feature: "+feature+"'", "Enter")
	internal.Run("tmux", "send-keys", "-t", session, "echo 'Branch: "+branch+"'", "Enter")
	internal.Run("tmux", "send-keys", "-t", session, "echo 'Use tmux commands to manage your panes and windows.'", "Enter")

	// Start the normal session in the first window

	// Start a new window to put the rest of the stuff in
	internal.Run("tmux", "new-window", "-t", session, "-n", "claude", "-c", path)
	// Start claude
	// TODO: we should probably check if the user has claude installed and available in PATH
	// TODO: we could probably think about adding more providers like codex, copilot-cli or opencode
	internal.Run("tmux", "send-keys", "-t", session, ":1", "claude -c", "C-m")

	// TODO: we should probably check if the session is already attached to avoid attaching to it multiple times and causing issues
	internal.Must(internal.Run("tmux", "attach", "-t", session))
}
