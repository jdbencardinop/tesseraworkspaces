package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func syncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync <feature>",
		Short: "Rebase worktrees in dependency order",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			internal.RequireTool("git")
			syncFeature(args[0])
		},
	}
}

func syncFeature(feature string) {
	featurePath := internal.FeaturePath(feature)

	internal.Must(internal.Run("git", "fetch"))

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		syncFallback(featurePath)
		return
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	syncWithStack(feature, featurePath, stack, sorted)
}
