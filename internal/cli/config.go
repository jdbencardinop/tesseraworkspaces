package cli

import (
	"fmt"
	"strconv"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}

	cmd.AddCommand(configShowCmd())
	cmd.AddCommand(configSetCmd())
	cmd.AddCommand(configGetCmd())

	return cmd
}

func configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show resolved configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, repoRoot, err := resolvedConfigForCommand()
			if err != nil {
				return err
			}

			fmt.Println("# Resolved configuration (global + per-repo)")
			fmt.Printf("agent_command: %s\n", cfg.GetAgentCommand())
			if cfg.UseTmux != nil {
				fmt.Printf("use_tmux: %v\n", *cfg.UseTmux)
			} else {
				fmt.Println("use_tmux: false (default)")
			}
			if cfg.InjectInto != "" {
				fmt.Printf("inject_into: %s\n", cfg.InjectInto)
			} else {
				fmt.Println("inject_into: . (default, worktree root)")
			}
			if cfg.TestCommand != "" {
				fmt.Printf("test_command: %s\n", cfg.TestCommand)
			} else {
				fmt.Println("test_command: (none)")
			}
			if cfg.AutoHooks != nil {
				fmt.Printf("auto_hooks: %v\n", *cfg.AutoHooks)
			} else {
				fmt.Println("auto_hooks: false (default)")
			}
			if cfg.BranchPrefix != "" {
				fmt.Printf("branch_prefix: %s\n", cfg.BranchPrefix)
			} else {
				fmt.Println("branch_prefix: (none)")
			}
			if cfg.WorktreeTemplate != "" {
				fmt.Printf("worktree_template: %s\n", cfg.WorktreeTemplate)
			} else {
				fmt.Println("worktree_template: (builtin when managed)")
			}
			if cfg.OrchestratorTemplate != "" {
				fmt.Printf("orchestrator_template: %s\n", cfg.OrchestratorTemplate)
			} else {
				fmt.Println("orchestrator_template: (builtin when managed)")
			}
			if len(cfg.Workspaces) > 0 {
				fmt.Println("workspaces:")
				for k, v := range cfg.Workspaces {
					fmt.Printf("  %s: %s\n", k, v)
				}
			}
			fmt.Println()
			fmt.Println("# Paths")
			fmt.Printf("global: %s\n", internal.ConfigPath())
			repoPath := internal.RepoConfigPathFor(repoRoot)
			if repoPath != "" {
				fmt.Printf("repo:   %s\n", repoPath)
			}
			return nil
		},
	}
}

func configSetCmd() *cobra.Command {
	var repo bool

	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Args:  cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return []string{"agent_command", "use_tmux", "inject_into", "test_command", "auto_hooks", "branch_prefix", "worktree_template", "orchestrator_template"}, cobra.ShellCompDirectiveNoFileComp
			}
			if len(args) == 1 && args[0] == "agent_command" {
				return []string{"claude", "opencode", "aider", "copilot"}, cobra.ShellCompDirectiveNoFileComp
			}
			if len(args) == 1 && args[0] == "use_tmux" {
				return []string{"true", "false"}, cobra.ShellCompDirectiveNoFileComp
			}
			if len(args) == 1 && (args[0] == "worktree_template" || args[0] == "orchestrator_template") {
				return namedTemplateFlagCompletion(cmd, args, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			path := internal.ConfigPath()
			if repo {
				_, repoRoot, err := resolvedConfigForCommand()
				if err != nil {
					return err
				}
				path = internal.RepoConfigPathFor(repoRoot)
				if path == "" {
					return fmt.Errorf("not inside an owning git repository")
				}
			}

			cfg := internal.LoadConfigFile(path)

			switch key {
			case "agent_command":
				cfg.AgentCommand = value
			case "use_tmux":
				b, err := strconv.ParseBool(value)
				if err != nil {
					return fmt.Errorf("use_tmux must be true or false, got %q", value)
				}
				cfg.UseTmux = &b
			case "inject_into":
				cfg.InjectInto = value
			case "test_command":
				cfg.TestCommand = value
			case "auto_hooks":
				b, err := strconv.ParseBool(value)
				if err != nil {
					return fmt.Errorf("auto_hooks must be true or false, got %q", value)
				}
				cfg.AutoHooks = &b
			case "branch_prefix":
				cfg.BranchPrefix = value
			case "worktree_template":
				if err := internal.ValidateTemplateReferenceSyntax(value); err != nil {
					return fmt.Errorf("invalid worktree_template: %w", err)
				}
				cfg.WorktreeTemplate = value
			case "orchestrator_template":
				if err := internal.ValidateTemplateReferenceSyntax(value); err != nil {
					return fmt.Errorf("invalid orchestrator_template: %w", err)
				}
				cfg.OrchestratorTemplate = value
			default:
				return fmt.Errorf("unknown key: %s (valid: agent_command, use_tmux, inject_into, test_command, auto_hooks, branch_prefix, worktree_template, orchestrator_template)", key)
			}

			if err := internal.SaveConfigFile(path, cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			scope := "global"
			if repo {
				scope = "repo"
			}
			fmt.Printf("Set %s = %s (%s: %s)\n", key, value, scope, path)
			return nil
		},
	}

	cmd.Flags().BoolVar(&repo, "repo", false, "Edit owning-repository config instead of global")

	return cmd
}

func configGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Get a resolved config value",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return []string{"agent_command", "use_tmux", "inject_into", "test_command", "auto_hooks", "branch_prefix", "worktree_template", "orchestrator_template"}, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := resolvedConfigForCommand()
			if err != nil {
				return err
			}

			switch args[0] {
			case "agent_command":
				fmt.Println(cfg.GetAgentCommand())
			case "use_tmux":
				if cfg.UseTmux != nil {
					fmt.Println(*cfg.UseTmux)
				} else {
					fmt.Println("false")
				}
			case "inject_into":
				if cfg.InjectInto != "" {
					fmt.Println(cfg.InjectInto)
				} else {
					fmt.Println(".")
				}
			case "test_command":
				if cfg.TestCommand != "" {
					fmt.Println(cfg.TestCommand)
				} else {
					fmt.Println("(none)")
				}
			case "auto_hooks":
				if cfg.AutoHooks != nil {
					fmt.Println(*cfg.AutoHooks)
				} else {
					fmt.Println("false")
				}
			case "branch_prefix":
				if cfg.BranchPrefix != "" {
					fmt.Println(cfg.BranchPrefix)
				} else {
					fmt.Println("(none)")
				}
			case "worktree_template":
				if cfg.WorktreeTemplate != "" {
					fmt.Println(cfg.WorktreeTemplate)
				} else {
					fmt.Println("(builtin when managed)")
				}
			case "orchestrator_template":
				if cfg.OrchestratorTemplate != "" {
					fmt.Println(cfg.OrchestratorTemplate)
				} else {
					fmt.Println("(builtin when managed)")
				}
			default:
				return fmt.Errorf("unknown key: %s (valid: agent_command, use_tmux, inject_into, test_command, auto_hooks, branch_prefix, worktree_template, orchestrator_template)", args[0])
			}
			return nil
		},
	}
}

func resolvedConfigForCommand() (internal.Config, string, error) {
	ws, err := internal.RequireWorkspace()
	if err != nil {
		return internal.LoadConfig(), "", nil
	}
	ws, err = managedTemplateExecutionWorkspace(ws)
	if err != nil {
		return internal.Config{}, "", err
	}
	cfg, err := internal.LoadConfigForRepoE(ws.RepoRoot)
	if err != nil {
		return internal.Config{}, "", err
	}
	return cfg, ws.RepoRoot, nil
}
