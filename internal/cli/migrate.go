package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func migrateLayoutCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "migrate-layout [feature]",
		Short: "Migrate features from legacy to new checkout layout",
		Long: `Migrate feature directories from legacy layout (.tws/<feature>) to new
layout (.tws/features/<feature>).

Use --all to migrate all legacy features at once with preflight collision
checks and rollback on failure. Without --all, specify a single feature name.

Rejects symlinks, unsafe feature names, and path traversal attempts.
Idempotent: already-migrated features are skipped.`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			ws, err := internal.RequireWorkspace()
			if err != nil || ws.Mode != internal.ModeCheckout || len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return ws.LegacyFeatureNames(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			if ws.Mode != internal.ModeCheckout {
				return fmt.Errorf("migrate-layout requires checkout mode (current: %s)", ws.Mode)
			}

			if all {
				result := internal.MigrateAllFeatures(ws)
				for _, name := range result.Migrated {
					fmt.Printf("  migrated: %s\n", name)
				}
				for _, name := range result.Skipped {
					fmt.Printf("  skipped (already migrated): %s\n", name)
				}
				if len(result.Errors) > 0 {
					for _, e := range result.Errors {
						fmt.Printf("  error: %s\n", e)
					}
					return fmt.Errorf("migration failed with %d error(s)", len(result.Errors))
				}
				if len(result.Migrated) == 0 && len(result.Skipped) == 0 {
					fmt.Println("Nothing to migrate.")
				}
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a feature name or use --all")
			}

			if err := internal.MigrateFeatureLayout(ws, args[0]); err != nil {
				return err
			}
			fmt.Printf("Migrated %s to new layout.\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Migrate all legacy features")

	return cmd
}
