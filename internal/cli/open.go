package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func openCmd() *cobra.Command {
	var useTmux bool
	var noTmux bool
	var noAgent bool

	cmd := &cobra.Command{
		Use:   "open [feature] [branch]",
		Short: "Open worktree and run agent",
		Long:  "Open a worktree and run the configured agent. With no args, shows an interactive picker.",
		Args:  cobra.RangeArgs(0, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 0:
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return internal.ListBranches(args[0]), cobra.ShellCompDirectiveNoFileComp
			default:
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		},
		Run: func(cmd *cobra.Command, args []string) {
			feature, branch, err := resolveOpenArgs(args)
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
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

			// Resolve tmux preference
			tmux := useTmux
			if !cmd.Flags().Changed("tmux") && !noTmux {
				cfg := internal.LoadConfig()
				if cfg.UseTmux != nil {
					tmux = *cfg.UseTmux
				}
			}
			if noTmux {
				tmux = false
			}

			if tmux {
				openWithTmux(feature, branch, path)
			} else {
				openDirect(path)
			}
		},
	}

	cmd.Flags().BoolVar(&useTmux, "tmux", false, "Wrap in tmux session")
	cmd.Flags().BoolVar(&noTmux, "no-tmux", false, "Skip tmux even if configured")
	cmd.Flags().BoolVar(&noAgent, "no-agent", false, "Just print the worktree path")

	return cmd
}

func resolveOpenArgs(args []string) (string, string, error) {
	var feature, branch string
	var err error

	switch len(args) {
	case 2:
		return args[0], args[1], nil
	case 1:
		feature = args[0]
		branches := internal.ListBranches(feature)
		if len(branches) == 0 {
			return "", "", fmt.Errorf("no branches found for feature: %s", feature)
		}
		branch, err = pick("Select branch:", branches)
		if err != nil {
			return "", "", err
		}
		return feature, branch, nil
	case 0:
		features := internal.ListFeatures()
		if len(features) == 0 {
			return "", "", fmt.Errorf("no features found. Use 'tws add <feature>' to create one")
		}
		feature, err = pick("Select feature:", features)
		if err != nil {
			return "", "", err
		}
		branches := internal.ListBranches(feature)
		if len(branches) == 0 {
			return "", "", fmt.Errorf("no branches found for feature: %s", feature)
		}
		branch, err = pick("Select branch:", branches)
		if err != nil {
			return "", "", err
		}
		return feature, branch, nil
	}
	return "", "", fmt.Errorf("unexpected args")
}

func openDirect(path string) {
	cfg := internal.LoadConfig()
	agentCmd := cfg.GetAgentCommand()

	if isClaudeAgent(agentCmd) && hasClaudeSession(path) {
		agentCmd = agentCmd + " -c"
	}

	parts := strings.Fields(agentCmd)
	binary, err := exec.LookPath(parts[0])
	if err != nil {
		fmt.Printf("Error: agent %q not found in PATH\n", parts[0])
		os.Exit(1)
	}

	fmt.Printf("Opening: %s\nRunning: %s\n", path, agentCmd)

	if err := os.Chdir(path); err != nil {
		fmt.Printf("Error: could not cd to %s: %v\n", path, err)
		os.Exit(1)
	}

	if err := syscall.Exec(binary, parts, os.Environ()); err != nil {
		fmt.Printf("Error: could not exec %s: %v\n", agentCmd, err)
		os.Exit(1)
	}
}

func openWithTmux(feature, branch, path string) {
	internal.RequireTool("tmux")

	session := sanitizeSessionName(feature + "/" + branch)

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
