package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func decisionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decisions",
		Short: "View and manage decisions for a feature",
	}

	cmd.AddCommand(decisionsListCmd())
	cmd.AddCommand(decisionsAckCmd())

	return cmd
}

func decisionsListCmd() *cobra.Command {
	var branch string
	var mine bool
	var all bool

	cmd := &cobra.Command{
		Use:   "show [feature]",
		Short: "List decisions for a feature (unread by default, auto-detects feature if omitted)",
		Args:  cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			var feature, featurePath string
			if len(args) > 0 {
				feature = args[0]
				featurePath = internal.FeaturePath(feature)
			} else {
				feature, featurePath = internal.DetectFeatureFromCwd()
				if feature == "" {
					fmt.Println("Could not detect feature from current directory.")
					fmt.Println("Usage: tws decisions show <feature> or run from inside a worktree.")
					os.Exit(1)
				}
			}

			decisions, err := internal.LoadDecisions(featurePath)
			if err != nil {
				fmt.Printf("No decisions found for feature: %s\n", feature)
				return
			}

			if len(decisions.Entries) == 0 {
				fmt.Println("No decisions recorded yet.")
				return
			}

			// Auto-detect current branch for --mine and unread filtering
			myBranch := ""
			if b, err := currentBranch(); err == nil {
				myBranch = b
			}

			lastRead := 0
			if !all && myBranch != "" {
				lastRead = internal.LastReadID(featurePath, myBranch)
			}

			count := 0
			for _, entry := range decisions.Entries {
				if branch != "" && entry.Branch != branch {
					continue
				}
				if mine && myBranch != "" && !entry.IsRelevantTo(myBranch) {
					continue
				}
				if !all && myBranch != "" && entry.ID <= lastRead {
					continue
				}
				fmt.Println(entry)
				count++
			}

			if count == 0 {
				if all {
					fmt.Println("No decisions match filters.")
				} else {
					fmt.Println("No unread decisions. Use --all to see everything.")
				}
			}
		},
	}

	cmd.Flags().StringVar(&branch, "branch", "", "Filter by source branch")
	cmd.Flags().BoolVar(&mine, "mine", false, "Show only decisions relevant to current branch")
	cmd.Flags().BoolVar(&all, "all", false, "Show all decisions (including already read)")

	return cmd
}

func decisionsAckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ack [feature]",
		Short: "Mark all decisions as read for current branch",
		Args:  cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			var featurePath string
			if len(args) > 0 {
				featurePath = internal.FeaturePath(args[0])
			} else {
				_, featurePath = internal.DetectFeatureFromCwd()
				if featurePath == "" {
					fmt.Println("Could not detect feature. Usage: tws decisions ack <feature>")
					os.Exit(1)
				}
			}

			branch, err := currentBranch()
			if err != nil {
				fmt.Println("Error: could not detect current branch")
				os.Exit(1)
			}

			if err := internal.AckDecisions(featurePath, branch); err != nil {
				fmt.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("Marked all decisions as read for branch: %s\n", branch)
		},
	}
}
