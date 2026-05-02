package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func Open(args []string) {
	if len(args) < 2 {
		println("Usage: tws open <feature> <branch> [--tmux] [--no-tmux] [--no-agent]")
		return
	}

	feature := args[0]
	branch := args[1]

	// Parse flags
	useTmux := false
	tmuxFlagSet := false
	noAgent := false

	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--tmux":
			useTmux = true
			tmuxFlagSet = true
		case "--no-tmux":
			useTmux = false
			tmuxFlagSet = true
		case "--no-agent":
			noAgent = true
		}
	}

	// If no flag, check config
	if !tmuxFlagSet {
		cfg := internal.LoadConfig()
		if cfg.UseTmux != nil {
			useTmux = *cfg.UseTmux
		}
	}

	path := internal.WorktreePath(feature, branch)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("Worktree not found: %s\n", path)
		os.Exit(1)
	}

	if noAgent {
		fmt.Printf("cd %s\n", path)
		fmt.Println("Run your agent manually from there.")
		return
	}

	if useTmux {
		openWithTmux(feature, branch, path)
	} else {
		openDirect(path)
	}
}

// openDirect changes into the worktree directory and execs the agent.
func openDirect(path string) {
	cfg := internal.LoadConfig()
	agentCmd := cfg.GetAgentCommand()

	// Claude-specific: use -c if session exists
	if isClaudeAgent(agentCmd) && hasClaudeSession(path) {
		agentCmd = agentCmd + " -c"
	}

	// Find the agent binary
	parts := strings.Fields(agentCmd)
	binary, err := exec.LookPath(parts[0])
	if err != nil {
		fmt.Printf("Error: agent %q not found in PATH\n", parts[0])
		os.Exit(1)
	}

	fmt.Printf("Opening: %s\nRunning: %s\n", path, agentCmd)

	// Change to worktree dir and exec the agent (replaces this process)
	if err := os.Chdir(path); err != nil {
		fmt.Printf("Error: could not cd to %s: %v\n", path, err)
		os.Exit(1)
	}

	// syscall.Exec replaces the current process
	if err := syscall.Exec(binary, parts, os.Environ()); err != nil {
		fmt.Printf("Error: could not exec %s: %v\n", agentCmd, err)
		os.Exit(1)
	}
}

// openWithTmux wraps the agent in a tmux session.
func openWithTmux(feature, branch, path string) {
	internal.RequireTool("tmux")

	session := sanitizeSessionName(feature + "/" + branch)

	// Check if session already exists
	if sessionExists(session) {
		fmt.Printf("Attaching to existing session: %s\n", session)
		internal.Must(internal.Run("tmux", "attach", "-t", session))
		return
	}

	cfg := internal.LoadConfig()
	agentCmd := cfg.GetAgentCommand()

	if isClaudeAgent(agentCmd) && hasClaudeSession(path) {
		agentCmd = agentCmd + " -c"
	}

	fmt.Printf("Creating tmux session: %s\n", session)
	internal.Must(internal.Run("tmux", "new-session", "-d", "-s", session, "-c", path))

	fmt.Printf("Running: %s\n", agentCmd)
	internal.Must(internal.Run("tmux", "send-keys", "-t", session, agentCmd, "Enter"))

	internal.Must(internal.Run("tmux", "attach", "-t", session))
}

func sessionExists(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	err := cmd.Run()
	return err == nil
}

func sanitizeSessionName(s string) string {
	r := strings.NewReplacer(".", "_", ":", "_", "/", "-")
	return r.Replace(s)
}

func isClaudeAgent(cmd string) bool {
	base := strings.Fields(cmd)[0]
	return base == "claude" || base == "claude-dev" || base == "cc"
}

func hasClaudeSession(workdir string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	absPath, err := filepath.Abs(workdir)
	if err != nil {
		return false
	}

	encoded := strings.ReplaceAll(absPath, string(filepath.Separator), "-")
	projectDir := filepath.Join(home, ".claude", "projects", encoded)

	info, err := os.Stat(projectDir)
	if err != nil {
		return false
	}
	return info.IsDir()
}
