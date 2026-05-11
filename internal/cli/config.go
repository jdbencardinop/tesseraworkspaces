package cli

import (
	"fmt"
	"os"
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
		Run: func(cmd *cobra.Command, args []string) {
			cfg := internal.LoadConfig()

			fmt.Println("# Resolved configuration (global + per-repo)")
			fmt.Printf("agent_command: %s\n", cfg.GetAgentCommand())
			if cfg.UseTmux != nil {
				fmt.Printf("use_tmux: %v\n", *cfg.UseTmux)
			} else {
				fmt.Println("use_tmux: false (default)")
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
			repoPath := internal.RepoConfigPath()
			if repoPath != "" {
				fmt.Printf("repo:   %s\n", repoPath)
			}
		},
	}
}

func configSetCmd() *cobra.Command {
	var repo bool

	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value (keys: agent_command, use_tmux)",
		Args:  cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return []string{"agent_command", "use_tmux"}, cobra.ShellCompDirectiveNoFileComp
			}
			if len(args) == 1 && args[0] == "agent_command" {
				return []string{"claude", "opencode", "aider", "copilot"}, cobra.ShellCompDirectiveNoFileComp
			}
			if len(args) == 1 && args[0] == "use_tmux" {
				return []string{"true", "false"}, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			key, value := args[0], args[1]

			path := internal.ConfigPath()
			if repo {
				path = internal.RepoConfigPath()
				if path == "" {
					fmt.Println("Error: not inside a git repository")
					os.Exit(1)
				}
			}

			cfg := internal.LoadConfigFile(path)

			switch key {
			case "agent_command":
				cfg.AgentCommand = value
			case "use_tmux":
				b, err := strconv.ParseBool(value)
				if err != nil {
					fmt.Printf("Error: use_tmux must be true or false, got %q\n", value)
					os.Exit(1)
				}
				cfg.UseTmux = &b
			default:
				fmt.Printf("Unknown key: %s (valid: agent_command, use_tmux)\n", key)
				os.Exit(1)
			}

			if err := internal.SaveConfigFile(path, cfg); err != nil {
				fmt.Printf("Error saving config: %v\n", err)
				os.Exit(1)
			}

			scope := "global"
			if repo {
				scope = "repo"
			}
			fmt.Printf("Set %s = %s (%s: %s)\n", key, value, scope, path)
		},
	}

	cmd.Flags().BoolVar(&repo, "repo", false, "Edit per-repo config instead of global")

	return cmd
}

func configGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Get a resolved config value",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return []string{"agent_command", "use_tmux"}, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			cfg := internal.LoadConfig()

			switch args[0] {
			case "agent_command":
				fmt.Println(cfg.GetAgentCommand())
			case "use_tmux":
				if cfg.UseTmux != nil {
					fmt.Println(*cfg.UseTmux)
				} else {
					fmt.Println("false")
				}
			default:
				fmt.Printf("Unknown key: %s (valid: agent_command, use_tmux)\n", args[0])
				os.Exit(1)
			}
		},
	}
}
