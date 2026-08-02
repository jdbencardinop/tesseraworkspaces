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
	var all bool

	cmd := &cobra.Command{
		Use:   "install [feature]",
		Short: "Install Claude Code hooks for auto-reading decisions",
		Long: `Install hooks into .claude/settings.local.json so that Claude Code
automatically checks for new decisions on session start and resume.

Use --all to install hooks across all features.
Set auto_hooks: true in config to auto-install on every tws new.`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ws, wsErr := internal.RequireWorkspace()
			if wsErr != nil {
				fmt.Printf("Error: %v\n", wsErr)
				return
			}
			if ws.Mode == internal.ModeCheckout {
				fmt.Println("Error: hooks install requires linked worktrees; not supported in checkout mode")
				return
			}

			if all {
				features := internal.ListFeatures()
				if len(features) == 0 {
					fmt.Println("No features found.")
					return
				}
				for _, f := range features {
					fmt.Printf("%s:\n", f)
					installHooksForFeature(f)
				}
				return
			}

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

			installHooksForFeature(feature)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Install hooks for all features")

	return cmd
}

func installHooksForFeature(feature string) {
	featurePath := internal.FeaturePath(feature)
	stack, _ := internal.LoadStack(featurePath)

	installed := 0
	for _, entry := range stack.Branches {
		wtPath := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(wtPath); os.IsNotExist(err) {
			continue
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
	}
}

func hooksRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove [feature]",
		Short: "Remove auto-read decision hooks",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			if ws.Mode == internal.ModeCheckout {
				return fmt.Errorf("hooks remove requires linked worktrees; not supported in checkout mode")
			}

			var feature string
			if len(args) > 0 {
				feature = args[0]
			} else {
				feature, _ = internal.DetectFeatureFromCwd()
				if feature == "" {
					return fmt.Errorf("could not detect feature; specify a feature or run from inside a worktree")
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
			return nil
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
