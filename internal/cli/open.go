package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func openCmd() *cobra.Command {
	var useTmux bool
	var noTmux bool
	var noAgent bool
	var featureDir bool
	var all bool

	cmd := &cobra.Command{
		Use:   "open [feature] [branch]",
		Short: "Open worktree and run agent",
		Long: `Open a worktree and run the configured agent. With no args, shows an interactive picker.

Use --feature-dir to open the feature directory itself (orchestrator mode).
Use --all to create a tmux session with windows for every worktree in the feature.`,
		Args: cobra.RangeArgs(0, 2),
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
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			// Checkout mode: delegate to checkout session flow
			if ws.Mode == internal.ModeCheckout {
				if all {
					return fmt.Errorf("--all not supported in checkout mode")
				}

				// --feature-dir handled before branch resolution
				if featureDir {
					if len(args) < 1 {
						return fmt.Errorf("usage: tws open <feature> --feature-dir")
					}
					feature := args[0]
					fp, ferr := ws.ResolveFeaturePath(feature)
					if ferr != nil {
						return ferr
					}
					if noAgent {
						fmt.Printf("cd %s\n", fp)
						return nil
					}
					fmt.Printf("Opening feature dir: %s\n", fp)
					openDirect(fp)
					return nil
				}

				return runCheckoutOpen(ws, args, useTmux, noTmux, noAgent, cmd.Flags())
			}

			// Handle --all: tmux session with all worktrees
			if all {
				if len(args) < 1 {
					return fmt.Errorf("usage: tws open <feature> --all")
				}
				openAll(args[0])
				return nil
			}

			// Handle --feature-dir: open the feature directory
			if featureDir {
				if len(args) < 1 {
					return fmt.Errorf("usage: tws open <feature> --feature-dir")
				}
				feature := args[0]
				path := internal.FeaturePath(feature)
				if _, err := os.Stat(path); os.IsNotExist(err) {
					return fmt.Errorf("feature not found: %s", feature)
				}
				if noAgent {
					fmt.Printf("cd %s\n", path)
					return nil
				}
				fmt.Printf("Opening feature dir: %s\n", path)
				openDirect(path)
				return nil
			}

			// Normal mode: open a specific worktree
			feature, branch, err := resolveOpenArgs(args)
			if err != nil {
				return err
			}

			path := internal.WorktreePath(feature, branch)

			if _, err := os.Stat(path); os.IsNotExist(err) {
				return fmt.Errorf("worktree not found: %s", path)
			}

			// Re-sync inject files
			featurePath := internal.FeaturePath(feature)
			injectTarget := internal.ResolveInjectInto("")
			if err := internal.InjectFiles(featurePath, path, injectTarget); err != nil {
				fmt.Printf("Warning: inject sync failed: %v\n", err)
			}

			// Show unread decisions count
			unread := internal.UnreadDecisions(featurePath, branch)
			if len(unread) > 0 {
				targeted := 0
				for _, d := range unread {
					if d.To != "" {
						targeted++
					}
				}
				msg := fmt.Sprintf("  %d new decision(s)", len(unread))
				if targeted > 0 {
					msg += fmt.Sprintf(" (%d for you)", targeted)
				}
				msg += fmt.Sprintf(" (run: tws decisions show %s)", feature)
				fmt.Println(msg)
			}

			if noAgent {
				fmt.Printf("cd %s\n", path)
				fmt.Println("Run your agent manually from there.")
				return nil
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
				// Warn if there's a stale tmux session
				session := sanitizeSessionName(feature + "/" + branch)
				if sessionExists(session) {
					fmt.Printf("Warning: tmux session %q exists for this worktree.\n", session)
					fmt.Printf("  Run 'tws close %s %s' to kill it, or use --tmux to attach.\n", feature, branch)
				}
				openDirect(path)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&useTmux, "tmux", false, "Wrap in tmux session")
	cmd.Flags().BoolVar(&noTmux, "no-tmux", false, "Skip tmux even if configured")
	cmd.Flags().BoolVar(&noAgent, "no-agent", false, "Just print the worktree path")
	cmd.Flags().BoolVar(&featureDir, "feature-dir", false, "Open the feature directory (orchestrator mode)")
	cmd.Flags().BoolVar(&all, "all", false, "Create tmux session with windows for all worktrees")

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
	if _, err := exec.LookPath(parts[0]); err != nil {
		fmt.Printf("Error: agent %q not found in PATH\n", parts[0])
		os.Exit(1)
	}

	fmt.Printf("Opening: %s\nRunning: %s\n", path, agentCmd)

	// Run agent as subprocess in the worktree directory
	agent := exec.Command(parts[0], parts[1:]...)
	agent.Dir = path
	agent.Stdin = os.Stdin
	agent.Stdout = os.Stdout
	agent.Stderr = os.Stderr

	if err := agent.Run(); err != nil {
		fmt.Printf("Agent exited: %v\n", err)
	}

	// Spawn an interactive shell in the worktree dir so the user stays there
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	fmt.Printf("Dropped into shell at: %s\n", path)
	sh := exec.Command(shell)
	sh.Dir = path
	sh.Stdin = os.Stdin
	sh.Stdout = os.Stdout
	sh.Stderr = os.Stderr
	_ = sh.Run()
}

// openAll creates a tmux session with the feature dir as the first window
// and one window per active worktree.
func openAll(feature string) {
	internal.RequireTool("tmux")

	featurePath := internal.FeaturePath(feature)
	if _, err := os.Stat(featurePath); os.IsNotExist(err) {
		fmt.Printf("Feature not found: %s\n", feature)
		os.Exit(1)
	}

	session := sanitizeSessionName(feature)

	// Kill existing session if any
	if sessionExists(session) {
		fmt.Printf("Attaching to existing session: %s\n", session)
		internal.Must(internal.Run("tmux", "attach", "-t", session))
		return
	}

	// Create session with feature dir as first window
	fmt.Printf("Creating tmux session: %s\n", session)
	internal.Must(internal.Run("tmux", "new-session", "-d", "-s", session, "-c", featurePath, "-n", "orchestrator"))

	// Add a window for each active worktree
	branches := internal.ListBranches(feature)
	for _, branch := range branches {
		wtPath := internal.WorktreePath(feature, branch)
		if _, err := os.Stat(wtPath); os.IsNotExist(err) {
			continue // archived
		}
		windowName := sanitizeSessionName(branch)
		_ = internal.RunSilent("tmux", "new-window", "-t", session, "-n", windowName, "-c", wtPath)
	}

	// Select the orchestrator window
	_ = internal.RunSilent("tmux", "select-window", "-t", session+":orchestrator")

	// Attach
	internal.Must(internal.Run("tmux", "attach", "-t", session))
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
