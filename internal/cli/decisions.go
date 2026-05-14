package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func decisionsCmd() *cobra.Command {
	var branch string
	var mine bool

	cmd := &cobra.Command{
		Use:   "decisions <feature>",
		Short: "List decisions for a feature",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			feature := args[0]
			featurePath := internal.FeaturePath(feature)

			decisions, err := internal.LoadDecisions(featurePath)
			if err != nil {
				fmt.Printf("No decisions found for feature: %s\n", feature)
				os.Exit(0)
			}

			if len(decisions.Entries) == 0 {
				fmt.Println("No decisions recorded yet.")
				return
			}

			// Auto-detect current branch for --mine
			myBranch := ""
			if mine {
				if b, err := currentBranch(); err == nil {
					myBranch = b
				}
			}

			count := 0
			for _, entry := range decisions.Entries {
				// Filter by source branch
				if branch != "" && entry.Branch != branch {
					continue
				}
				// Filter by relevance to current branch
				if mine && myBranch != "" && !entry.IsRelevantTo(myBranch) {
					continue
				}
				fmt.Println(entry)
				count++
			}

			if count == 0 {
				if mine {
					fmt.Println("No decisions relevant to your branch.")
				} else if branch != "" {
					fmt.Printf("No decisions from branch: %s\n", branch)
				}
			}
		},
	}

	cmd.Flags().StringVar(&branch, "branch", "", "Filter by source branch")
	cmd.Flags().BoolVar(&mine, "mine", false, "Show only decisions relevant to current branch")

	return cmd
}
