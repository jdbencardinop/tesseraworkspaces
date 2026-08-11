package cli

import (
	"encoding/json"
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status [feature]",
		Short: "Show agent work status for every logical branch",
		Long: `Report what tws knows about each logical branch: whether it is materialized,
whether a tws-launched session is running, and whether anything needs
attention.

Scope. With no argument the report always covers every feature in the resolved
workspace, from any working directory. It is never scoped by your current
location — pass a feature name to filter. (This deliberately differs from
'tws space list', which is cwd-scoped.)

Your working directory selects which workspace is resolved; it never changes
what the report says about that workspace. Run from the repository, from a
worktree, or from the workspace root and the reported features, entries, and
issues are identical.

Two axes, never collapsed. 'runtime_presence' answers "is a tws-owned runtime
alive?" (present, absent, stale, unknown). 'agent_state' answers "what is the
agent doing?" (working, ready, blocked, done, unknown) and is always 'unknown'
at this version: tws launches agents but does not observe their turns. Use
'needs_attention', which is authoritative, to decide where to intervene.

Exit status is 0 whenever a report was produced, including when branches need
attention or operational state is stale or corrupt. A non-zero exit means no
report could be produced at all.

--json prints one versioned document with a stable key set; absent values are
null and lists are never null.`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag and argument errors keep their usage block; a runtime
			// failure must not spray usage into a polled surface's output.
			cmd.SilenceUsage = true

			ws, degradedReason, err := internal.ResolveStatusWorkspace()
			if err != nil {
				return err
			}

			feature := ""
			if len(args) == 1 {
				feature = args[0]
				// Guard the caller-supplied name against a registered space
				// before any path join, stat, record read, or tmux probe. The
				// guard root is the root the subsequent reads join against.
				if gerr := internal.GuardFeatureName(ws.MetadataRoot, feature); gerr != nil {
					return gerr
				}
			}

			report, err := internal.BuildAgentStatus(ws, degradedReason, nil)
			if err != nil {
				return err
			}
			if feature != "" {
				if ferr := report.FilterFeature(feature); ferr != nil {
					return ferr
				}
			}
			internal.NormalizeAgentStatus(report)

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			_, _ = fmt.Fprint(cmd.OutOrStdout(), internal.FormatAgentStatus(report))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}
