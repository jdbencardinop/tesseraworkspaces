package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func enableCmd() *cobra.Command {
	var mode string

	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Enable tws in the current repository",
		Long: `Enable tws workspace mode for the current repository.

Checkout mode stores metadata under .tws/ inside the repo and adds .tws/ to
.git/info/exclude (local ignore). External mode stores metadata in a sibling
directory.

This command must be run from the main worktree (not a linked worktree).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := internal.MainRepoRoot()
			if err != nil {
				return fmt.Errorf("not inside a git repository: %w", err)
			}

			switch mode {
			case "checkout":
				if err := internal.EnableCheckoutMode(repoRoot); err != nil {
					return err
				}
				fmt.Printf("Checkout mode enabled in %s\n", repoRoot)
				fmt.Println("  .tws/config.yaml — workspace config")
				fmt.Println("  .tws/features/   — feature metadata")
				fmt.Println("  .tws/state/      — workspace state")
				fmt.Println("  .git/info/exclude — .tws/ added to local ignore")
			case "external":
				if err := internal.EnableExternalMode(repoRoot); err != nil {
					return err
				}
				fmt.Printf("External mode enabled in %s\n", repoRoot)
			default:
				return fmt.Errorf("--mode is required: use 'checkout' or 'external'")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&mode, "mode", "", "Workspace mode: checkout, external (required)")
	_ = cmd.MarkFlagRequired("mode")
	_ = cmd.RegisterFlagCompletionFunc("mode", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"checkout", "external"}, cobra.ShellCompDirectiveNoFileComp
	})

	return cmd
}

func modeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mode",
		Short: "Show current workspace mode",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			fmt.Println(ws.Mode)
			return nil
		},
	}
}
