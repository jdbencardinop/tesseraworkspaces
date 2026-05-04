package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

func SetVersion(v string) {
	version = v
}

func Execute() int {
	rootCmd := &cobra.Command{
		Use:     "tws",
		Short:   "tesseraworkspaces — feature-scoped workspaces with stacked git worktrees",
		Version: version,
	}

	rootCmd.AddCommand(
		addCmd(),
		newCmd(),
		openCmd(),
		syncCmd(),
		stackCmd(),
		deleteCmd(),
		listCmd(),
		archiveCmd(),
		initCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
