package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func stackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stack <feature>",
		Short: "Show branch dependency tree",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			feature := args[0]
			featurePath := internal.FeaturePath(feature)

			stack, err := internal.LoadStack(featurePath)
			if err != nil {
				fmt.Println("No stack.yaml found for feature:", feature)
				os.Exit(1)
			}

			if _, err := internal.TopoSort(stack); err != nil {
				fmt.Printf("Warning: %v\n", err)
			}

			internal.PrintTree(stack)
		},
	}
}
