package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func stackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stack <feature>",
		Short: "Show branch dependency tree",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				// Cobra already contributes the `status` subcommand at this
				// position; two candidates spelled identically with different
				// meanings is worse than one. `tws stack -- status` always
				// reaches a feature literally named `status`.
				features := internal.ListFeatures()
				deduped := make([]string, 0, len(features))
				for _, name := range features {
					if name == "status" {
						continue
					}
					deduped = append(deduped, name)
				}
				return deduped, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			feature := args[0]
			featurePath, err := internal.RequireFeaturePath(feature)
			if err != nil {
				return err
			}

			stack, err := internal.LoadStack(featurePath)
			if err != nil {
				return fmt.Errorf("no stack.yaml found for feature: %s", feature)
			}

			if _, err := internal.TopoSort(stack); err != nil {
				fmt.Printf("Warning: %v\n", err)
			}

			internal.PrintTree(stack)
			return nil
		},
	}

	cmd.AddCommand(stackStatusCmd())
	return cmd
}
