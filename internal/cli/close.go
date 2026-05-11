package cli

import (
	"fmt"
	"os"

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
		Run: func(cmd *cobra.Command, args []string) {
			feature := args[0]
			branch := args[1]

			session := sanitizeSessionName(feature + "/" + branch)

			if !sessionExists(session) {
				fmt.Printf("No tmux session found for %s/%s\n", feature, branch)
				os.Exit(1)
			}

			err := internal.Run("tmux", "kill-session", "-t", session)
			if err != nil {
				fmt.Printf("Error killing session: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("Closed tmux session: %s\n", session)
		},
	}
}

// TmuxSessionName returns the tmux session name for a feature/branch pair.
func TmuxSessionName(feature, branch string) string {
	return sanitizeSessionName(feature + "/" + branch)
}
