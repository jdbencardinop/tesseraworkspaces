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
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, wsErr := internal.RequireWorkspace()
			if wsErr != nil {
				return wsErr
			}
			if ws.Mode == internal.ModeCheckout {
				return fmt.Errorf("hooks install requires linked worktrees; not supported in checkout mode")
			}

			if all {
				features, listErr := internal.ListFeaturesE()
				if listErr != nil {
					return listErr
				}
				if len(features) == 0 {
					fmt.Println("No features found.")
					return nil
				}
				for _, f := range features {
					fmt.Printf("%s:\n", f)
					installHooksForFeature(f)
				}
				return nil
			}

			var feature string
			if len(args) > 0 {
				feature = args[0]
			} else {
				feature, _ = internal.DetectFeatureFromCwd()
				if feature == "" {
					return fmt.Errorf("could not detect feature. Run from inside a worktree or specify: tws hooks install <feature>")
				}
			}

			// installHooksForFeature has no error channel and prints to
			// stdout, so the guard must be evaluated here to exit nonzero.
			if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
				return err
			}

			installHooksForFeature(feature)
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Install hooks for all features")

	return cmd
}

func installHooksForFeature(feature string) {
	featurePath, err := internal.RequireFeaturePath(feature)
	if err != nil {
		fmt.Printf("  [x] %s: %v\n", feature, err)
		return
	}
	stack, _ := internal.LoadStack(featurePath)

	installed := 0
	for _, entry := range stack.Branches {
		wtPath, err := internal.RequireWorktreePath(feature, entry.Name)
		if err != nil {
			fmt.Printf("  [x] %s: %v\n", entry.Name, err)
			return
		}
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

			featurePath, fpErr := internal.RequireFeaturePath(feature)
			if fpErr != nil {
				return fpErr
			}
			stack, _ := internal.LoadStack(featurePath)

			for _, entry := range stack.Branches {
				wtPath, wtErr := internal.RequireWorktreePath(feature, entry.Name)
				if wtErr != nil {
					return wtErr
				}
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
