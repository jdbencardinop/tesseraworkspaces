package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func hooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Manage agent hooks for auto-read decisions",
	}

	cmd.AddCommand(hooksInstallCmd())
	cmd.AddCommand(hooksRemoveCmd())

	return cmd
}

// Claude Code settings.json hook structure
type claudeSettings struct {
	Hooks map[string][]hookMatcher `json:"hooks,omitempty"`
}

type hookMatcher struct {
	Matcher string       `json:"matcher"`
	Hooks   []hookAction `json:"hooks"`
}

type hookAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func hooksInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install [feature]",
		Short: "Install Claude Code hooks for auto-reading decisions",
		Long: `Install hooks into .claude/settings.local.json so that Claude Code
automatically checks for new decisions on session start and resume.

Optionally watches decisions.yaml for real-time notifications via FileChanged hook.

Run from inside a worktree, or specify the feature name.`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			var feature string
			if len(args) > 0 {
				feature = args[0]
			} else {
				feature, _ = internal.DetectFeatureFromCwd()
				if feature == "" {
					fmt.Println("Could not detect feature. Run from inside a worktree or specify: tws hooks install <feature>")
					os.Exit(1)
				}
			}

			featurePath := internal.FeaturePath(feature)
			stack, _ := internal.LoadStack(featurePath)

			installed := 0
			for _, entry := range stack.Branches {
				wtPath := internal.WorktreePath(feature, entry.Name)
				if _, err := os.Stat(wtPath); os.IsNotExist(err) {
					continue // archived
				}

				if err := installHooksForWorktree(wtPath, feature); err != nil {
					fmt.Printf("  [x] %s: %v\n", entry.Name, err)
				} else {
					fmt.Printf("  [+] %s: hooks installed\n", entry.Name)
					installed++
				}
			}

			if installed > 0 {
				fmt.Printf("Installed hooks in %d worktree(s)\n", installed)
				fmt.Println("Claude Code will now check for decisions on session start/resume.")
			}
		},
	}
}

func hooksRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove [feature]",
		Short: "Remove auto-read decision hooks",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			var feature string
			if len(args) > 0 {
				feature = args[0]
			} else {
				feature, _ = internal.DetectFeatureFromCwd()
				if feature == "" {
					fmt.Println("Could not detect feature.")
					os.Exit(1)
				}
			}

			featurePath := internal.FeaturePath(feature)
			stack, _ := internal.LoadStack(featurePath)

			for _, entry := range stack.Branches {
				wtPath := internal.WorktreePath(feature, entry.Name)
				settingsPath := filepath.Join(wtPath, ".claude", "settings.local.json")
				if err := os.Remove(settingsPath); err == nil {
					fmt.Printf("  [-] %s: hooks removed\n", entry.Name)
				}
			}
		},
	}
}

func installHooksForWorktree(wtPath, feature string) error {
	settingsDir := filepath.Join(wtPath, ".claude")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		return err
	}

	settingsPath := filepath.Join(settingsDir, "settings.local.json")

	// Build the decisions check command
	showCmd := fmt.Sprintf("tws decisions show %s --mine 2>/dev/null || true", feature)
	ackHint := fmt.Sprintf("echo 'To acknowledge: tws decisions ack %s'", feature)
	fullCmd := showCmd + " && " + ackHint

	settings := claudeSettings{
		Hooks: map[string][]hookMatcher{
			"SessionStart": {
				{
					Matcher: "startup",
					Hooks: []hookAction{
						{Type: "command", Command: fullCmd, Timeout: 10},
					},
				},
				{
					Matcher: "resume",
					Hooks: []hookAction{
						{Type: "command", Command: showCmd, Timeout: 10},
					},
				},
			},
		},
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(settingsPath, data, 0644)
}
