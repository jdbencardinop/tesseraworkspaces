package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func decideCmd() *cobra.Command {
	var decisionType string
	var details string
	var branch string
	var to string

	cmd := &cobra.Command{
		Use:   "decide <feature> <summary>",
		Short: "Record a decision for sibling worktrees to see",
		Args:  cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			feature := args[0]
			summary := args[1]

			featurePath := internal.FeaturePath(feature)

			if _, err := os.Stat(featurePath); os.IsNotExist(err) {
				fmt.Printf("Feature not found: %s\n", feature)
				os.Exit(1)
			}

			// Auto-detect branch from current worktree if not specified
			if branch == "" {
				if b, err := currentBranch(); err == nil {
					branch = b
				} else {
					branch = "unknown"
				}
			}

			entry, err := internal.AddDecision(featurePath, branch, to, summary, decisionType, details)
			if err != nil {
				fmt.Printf("Error recording decision: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("Decision recorded:\n  %s\n", entry)
		},
	}

	cmd.Flags().StringVar(&decisionType, "type", "info", "Decision type (breaking, info, deprecation, review, question)")
	cmd.Flags().StringVar(&details, "details", "", "Longer explanation")
	cmd.Flags().StringVar(&branch, "branch", "", "Source branch (auto-detected if omitted)")
	cmd.Flags().StringVar(&to, "to", "", "Target branch (empty = broadcast to all)")

	return cmd
}

func currentBranch() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
