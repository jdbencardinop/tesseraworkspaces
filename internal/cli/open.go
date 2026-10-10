package cli

import (
	"errors"
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
				ws, err := internal.RequireWorkspace()
				if err != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				ws, err = managedTemplateExecutionWorkspace(ws)
				if err != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				features, err := ws.ListFeaturesResolved()
				if err != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				return features, cobra.ShellCompDirectiveNoFileComp
			case 1:
				ws, err := internal.RequireWorkspace()
				if err != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				context, err := resolveFeatureContext(ws, args[0])
				if err != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				return internal.ListBranchesAt(context.FeaturePath), cobra.ShellCompDirectiveNoFileComp
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
					if gerr := internal.GuardFeatureName(ws.MetadataRoot, feature); gerr != nil {
						return gerr
					}
					fp, ferr := ws.ResolveFeaturePath(feature)
					if ferr != nil {
						return ferr
					}
					managed, ferr := internal.ManagedTemplateStatePresent(fp)
					if ferr != nil {
						return ferr
					}
					var managedCfg resolvedManagedConfig
					if managed {
						managedCfg, ferr = resolveManagedConfig(ws)
						if ferr != nil {
							return ferr
						}
						if ferr := internal.ValidateManagedOrchestratorContext(
							ws, feature, fp, managedCfg.AgentCommand,
						); ferr != nil {
							return ferr
						}
					}
					// §14.2a: no session, no terminal, while a reparent is
					// recorded for this feature.
					if rerr := refuseOpenDuringReparent(ws, feature); rerr != nil {
						return rerr
					}
					if noAgent {
						fmt.Printf("cd %s\n", fp)
						return nil
					}
					return internal.WithCheckoutSessionLaunchIntent(ws, func() error {
						return internal.CheckoutFeatureDirSessionPreconditions(ws)
					}, func() error {
						fmt.Printf("Opening feature dir: %s\n", fp)
						// A feature directory is not a logical branch, so it
						// has no per-branch record; the workspace intent itself
						// remains live through the agent and shell.
						return openDirect(directOpenOpts{
							Path: fp, AgentCommand: managedCfg.AgentCommand,
						})
					})
				}

				return runCheckoutOpenResolved(ws, args, useTmux, noTmux, noAgent, cmd.Flags(), nil)
			}
			// Handle --all: tmux session with all worktrees
			if all {
				if len(args) < 1 {
					return fmt.Errorf("usage: tws open <feature> --all")
				}
				feature := args[0]
				ws, err = managedTemplateWorkspaceForFeature(ws, feature)
				if err != nil {
					return err
				}
				if gerr := internal.GuardFeatureName(ws.MetadataRoot, feature); gerr != nil {
					return gerr
				}
				resolved, resolveErr := ws.ResolveFeaturePath(feature)
				if resolveErr != nil {
					return resolveErr
				}
				featurePath := resolved
				if managed, managedErr := internal.ManagedTemplateStatePresent(resolved); managedErr != nil {
					return managedErr
				} else if managed {
					managedCfg, err := resolveManagedConfig(ws)
					if err != nil {
						return err
					}
					if err := internal.ValidateManagedOrchestratorContext(
						ws, feature, featurePath, managedCfg.AgentCommand,
					); err != nil {
						return err
					}
					if _, err := syncManagedAndLegacyInjectAll(
						cmd, ws, feature, featurePath,
						managedInjectTarget{Value: managedCfg.Config.InjectInto},
					); err != nil {
						return fmt.Errorf("managed inject sync failed: %w", err)
					}
				}
				return openAllAt(feature, featurePath, internal.ListBranchesAt(featurePath), func() error {
					return refuseOpenDuringReparent(ws, feature)
				})
			}

			// Handle --feature-dir: open the feature directory
			if featureDir {
				if len(args) < 1 {
					return fmt.Errorf("usage: tws open <feature> --feature-dir")
				}
				feature := args[0]
				ws, err = managedTemplateWorkspaceForFeature(ws, feature)
				if err != nil {
					return err
				}
				if gerr := internal.GuardFeatureName(ws.MetadataRoot, feature); gerr != nil {
					return gerr
				}
				resolved, resolveErr := ws.ResolveFeaturePath(feature)
				if resolveErr != nil {
					return resolveErr
				}
				path := resolved
				managed, managedErr := internal.ManagedTemplateStatePresent(resolved)
				if managedErr != nil {
					return managedErr
				}
				var managedCfg resolvedManagedConfig
				if managed {
					managedCfg, err = resolveManagedConfig(ws)
					if err != nil {
						return err
					}
					if err := internal.ValidateManagedOrchestratorContext(
						ws, feature, path, managedCfg.AgentCommand,
					); err != nil {
						return err
					}
				}
				if _, err := os.Stat(path); os.IsNotExist(err) {
					return fmt.Errorf("feature not found: %s", feature)
				}
				// The resolved path is authoritative for this route. Refuse
				// before the no-agent fast path and before any success prose;
				// the intent wrapper below repeats the check after publishing
				// its race-closing launch intent.
				if err := refuseExternalOpenDuringMutation(feature, path); err != nil {
					return err
				}
				if noAgent {
					fmt.Printf("cd %s\n", path)
					return nil
				}
				return withExternalSessionLaunchIntent(path, feature, nil, true, func() error {
					return refuseExternalOpenDuringMutation(feature, path)
				}, func() error {
					fmt.Printf("Opening feature dir: %s\n", path)
					// Untracked: a feature directory is not a logical branch,
					// so no long-lived direct session record is created.
					return openDirect(directOpenOpts{
						Path: path, AgentCommand: managedCfg.AgentCommand,
					})
				})
			}

			// Normal mode: open a specific worktree
			context, branch, err := resolveOpenArgs(ws, args)
			if err != nil {
				return err
			}
			ws = context.Workspace
			feature := context.Feature
			// §14.2a, after the picker has settled the identity: the exclusion
			// covers the 0-arg and 1-arg picker routes too.
			if rerr := refuseOpenDuringReparent(ws, feature); rerr != nil {
				return rerr
			}

			featurePath := context.FeaturePath
			path := filepath.Join(featurePath, "worktrees", filepath.FromSlash(branch))
			resolvedFeaturePath := context.FeaturePath
			managed, managedErr := internal.ManagedTemplateStatePresent(resolvedFeaturePath)
			if managedErr != nil {
				return managedErr
			}
			if managed {
				featurePath = resolvedFeaturePath
			}

			if _, err := os.Stat(path); os.IsNotExist(err) {
				return fmt.Errorf("worktree not found: %s", path)
			}

			// Re-sync inject files
			injectTarget := internal.ResolveInjectInto("")
			var managedCfg resolvedManagedConfig
			if managed {
				managedCfg, err = resolveManagedConfig(ws)
				if err != nil {
					return err
				}
				injectTarget = managedCfg.Config.InjectInto
				if err := syncManagedAndLegacyInject(
					cmd, ws, feature, featurePath, branch, path,
					managedInjectTarget{Value: injectTarget},
				); err != nil {
					return fmt.Errorf("managed inject sync failed: %w", err)
				}
			} else if err := internal.InjectFiles(featurePath, path, injectTarget); err != nil {
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
				if managed {
					cfg = managedCfg.Config
				}
				if cfg.UseTmux != nil {
					tmux = *cfg.UseTmux
				}
			}
			if noTmux {
				tmux = false
			}

			if tmux {
				return openWithTmuxResolved(feature, branch, path, featurePath, managedCfg.AgentCommand, func() error {
					return refuseOpenDuringReparent(ws, feature)
				})
			}
			// Warn if there's a stale tmux session
			session := sanitizeSessionName(feature + "/" + branch)
			if sessionExists(session) {
				fmt.Printf("Warning: tmux session %q exists for this worktree.\n", session)
				fmt.Printf("  Run 'tws close %s %s' to kill it, or use --tmux to attach.\n", feature, branch)
			}
			return openDirect(directOpenOpts{
				Path:         path,
				Feature:      feature,
				Name:         branch,
				GitBranch:    resolveDirectGitBranch(featurePath, branch),
				FeaturePath:  featurePath,
				AgentCommand: managedCfg.AgentCommand,
				FinalGuard: func() error {
					return refuseOpenDuringReparent(ws, feature)
				},
			})
		},
	}

	cmd.Flags().BoolVar(&useTmux, "tmux", false, "Wrap in tmux session")
	cmd.Flags().BoolVar(&noTmux, "no-tmux", false, "Skip tmux even if configured")
	cmd.Flags().BoolVar(&noAgent, "no-agent", false, "Just print the worktree path")
	cmd.Flags().BoolVar(&featureDir, "feature-dir", false, "Open the feature directory (orchestrator mode)")
	cmd.Flags().BoolVar(&all, "all", false, "Create tmux session with windows for all worktrees")

	return cmd
}

func resolveOpenArgs(ws internal.Workspace, args []string) (resolvedFeatureContext, string, error) {
	var feature, branch string
	var err error

	switch len(args) {
	case 2:
		context, err := resolveFeatureContext(ws, args[0])
		return context, args[1], err
	case 1:
		feature = args[0]
		context, err := resolveFeatureContext(ws, feature)
		if err != nil {
			return resolvedFeatureContext{}, "", err
		}
		branches := internal.ListBranchesAt(context.FeaturePath)
		if len(branches) == 0 {
			return resolvedFeatureContext{}, "", fmt.Errorf("no branches found for feature: %s", feature)
		}
		branch, err = pick("Select branch:", branches)
		if err != nil {
			return resolvedFeatureContext{}, "", err
		}
		return context, branch, nil
	case 0:
		executionWS, resolveErr := managedTemplateExecutionWorkspace(ws)
		if resolveErr != nil {
			return resolvedFeatureContext{}, "", resolveErr
		}
		features, listErr := executionWS.ListFeaturesResolved()
		if listErr != nil {
			return resolvedFeatureContext{}, "", listErr
		}
		if len(features) == 0 {
			return resolvedFeatureContext{}, "", fmt.Errorf("no features found. Use 'tws add <feature>' to create one")
		}
		feature, err = pick("Select feature:", features)
		if err != nil {
			return resolvedFeatureContext{}, "", err
		}
		context, err := resolveFeatureContext(executionWS, feature)
		if err != nil {
			return resolvedFeatureContext{}, "", err
		}
		branches := internal.ListBranchesAt(context.FeaturePath)
		if len(branches) == 0 {
			return resolvedFeatureContext{}, "", fmt.Errorf("no branches found for feature: %s", feature)
		}
		branch, err = pick("Select branch:", branches)
		if err != nil {
			return resolvedFeatureContext{}, "", err
		}
		return context, branch, nil
	}
	return resolvedFeatureContext{}, "", fmt.Errorf("unexpected args")
}

// openAll creates a tmux session with the feature dir as the first window
// and one window per active worktree.
// ExternalSessionLaunchIntentHook is a test-only seam after a tmux/all/feature
// launch intent is durable and before the final feature-mutation check.
var ExternalSessionLaunchIntentHook func() error

func withExternalSessionLaunchIntent(featurePath, feature string, names []string, featureWide bool, finalGuard, action func() error) (err error) {
	token, err := internal.CreateExternalSessionIntent(featurePath, feature, names, featureWide)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, internal.RemoveOwnedExternalSessionIntent(featurePath, token))
	}()
	if ExternalSessionLaunchIntentHook != nil {
		if err := ExternalSessionLaunchIntentHook(); err != nil {
			return err
		}
	}
	if finalGuard != nil {
		if err := finalGuard(); err != nil {
			return err
		}
	}
	if action == nil {
		return nil
	}
	return action()
}

func openAll(feature, featurePath string, finalGuard func() error) error {
	return openAllAt(feature, featurePath, internal.ListBranchesAt(featurePath), finalGuard)
}

func openAllAt(feature, featurePath string, branches []string, finalGuard func() error) error {
	if _, err := os.Stat(featurePath); os.IsNotExist(err) {
		return fmt.Errorf("feature not found: %s", feature)
	}
	return withExternalSessionLaunchIntent(featurePath, feature, nil, true, finalGuard, func() error {
		if _, err := exec.LookPath("tmux"); err != nil {
			return fmt.Errorf("required tool %q not found in PATH", "tmux")
		}
		session := sanitizeSessionName(feature)
		if sessionExists(session) {
			fmt.Printf("Attaching to existing session: %s\n", session)
			return internal.Run("tmux", "attach", "-t", session)
		}

		fmt.Printf("Creating tmux session: %s\n", session)
		if err := internal.Run("tmux", "new-session", "-d", "-s", session, "-c", featurePath, "-n", "orchestrator"); err != nil {
			return err
		}

		for _, branch := range branches {
			wtPath := filepath.Join(featurePath, "worktrees", filepath.FromSlash(branch))
			if _, err := os.Stat(wtPath); os.IsNotExist(err) {
				continue
			}
			windowName := sanitizeSessionName(branch)
			_ = internal.RunSilent("tmux", "new-window", "-t", session, "-n", windowName, "-c", wtPath)
		}
		_ = internal.RunSilent("tmux", "select-window", "-t", session+":orchestrator")
		return internal.Run("tmux", "attach", "-t", session)
	})
}

func openWithTmux(feature, branch, path, featurePath string, finalGuard func() error) error {
	return openWithTmuxResolved(feature, branch, path, featurePath, "", finalGuard)
}

func openWithTmuxResolved(
	feature, branch, path, featurePath, agentCommand string,
	finalGuard func() error,
) error {
	return withExternalSessionLaunchIntent(featurePath, feature, []string{branch}, false, finalGuard, func() error {
		if _, err := exec.LookPath("tmux"); err != nil {
			return fmt.Errorf("required tool %q not found in PATH", "tmux")
		}
		session := sanitizeSessionName(feature + "/" + branch)
		if sessionExists(session) {
			fmt.Printf("Attaching to existing session: %s\n", session)
			return internal.Run("tmux", "attach", "-t", session)
		}

		agentCmd := agentCommand
		if agentCmd == "" {
			cfg := internal.LoadConfig()
			agentCmd = cfg.GetAgentCommand()
		}
		if isClaudeAgent(agentCmd) && hasClaudeSession(path) {
			agentCmd += " -c"
		}

		fmt.Printf("Creating tmux session: %s\n", session)
		if err := internal.Run("tmux", "new-session", "-d", "-s", session, "-c", path); err != nil {
			return err
		}
		fmt.Printf("Running: %s\n", agentCmd)
		if err := internal.Run("tmux", "send-keys", "-t", session, agentCmd, "Enter"); err != nil {
			return err
		}
		return internal.Run("tmux", "attach", "-t", session)
	})
}

func sessionExists(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	err := cmd.Run()
	return err == nil
}

func sanitizeSessionName(s string) string {
	return internal.SanitizeTmuxName(s)
}

func isClaudeAgent(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	base := fields[0]
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
