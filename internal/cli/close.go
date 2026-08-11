package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func closeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close [feature] [branch]",
		Short: "Close a worktree session (tmux or checkout)",
		Args:  cobra.RangeArgs(0, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			ws, err := internal.RequireWorkspace()
			if err == nil && ws.Mode == internal.ModeCheckout {
				state, loadErr := internal.LoadCheckoutAgentSession(ws)
				if loadErr != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				if len(args) == 0 {
					return []string{state.Feature}, cobra.ShellCompDirectiveNoFileComp
				}
				if len(args) == 1 && args[0] == state.Feature {
					return []string{state.Name}, cobra.ShellCompDirectiveNoFileComp
				}
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			switch len(args) {
			case 0:
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return internal.ListBranches(args[0]), cobra.ShellCompDirectiveNoFileComp
			default:
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		},
		// Deliberately guard-free: the external branch only builds a tmux
		// session name and kills that session — it joins no root and creates,
		// reads, or removes nothing under TwsRoot(). The checkout branch reads
		// the recorded session rather than a caller-supplied name. A registered
		// space therefore cannot be reached through this command.
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			// Checkout mode: delegate to checkout close
			if ws.Mode == internal.ModeCheckout {
				if cerr := runCheckoutClose(ws, args); cerr != nil {
					return cerr
				}
				fmt.Println("Checkout session closed.")
				return nil
			}

			// External mode: requires exactly 2 args
			if len(args) != 2 {
				return fmt.Errorf("usage: tws close <feature> <branch>")
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
