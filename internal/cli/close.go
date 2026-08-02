package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func closeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close <feature> <branch>",
		Short: "Kill tmux session for a worktree",
		Args:  cobra.ExactArgs(2),
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
			if ws.Mode == internal.ModeCheckout {
				return fmt.Errorf("close requires linked worktrees; not supported in checkout mode")
			}

			feature := args[0]
			branch := args[1]

			session := sanitizeSessionName(feature + "/" + branch)

			if !sessionExists(session) {
				return fmt.Errorf("no tmux session found for %s/%s", feature, branch)
			}

			if err := internal.Run("tmux", "kill-session", "-t", session); err != nil {
				return fmt.Errorf("error killing session: %w", err)
			}

			fmt.Printf("Closed tmux session: %s\n", session)
			return nil
		},
	}
}

// TmuxSessionName returns the tmux session name for a feature/branch pair.
func TmuxSessionName(feature, branch string) string {
	return sanitizeSessionName(feature + "/" + branch)
}
