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

Scope. With no argument the report always builds every feature in the resolved
workspace. With a feature argument it builds only that feature plus genuine
workspace evidence, including a shared checkout session even when that session
belongs to another feature. It does not read or probe other feature stacks,
worktrees, or session records. Scope is never inferred from your current
location. (This deliberately differs from 'tws space list', which is
cwd-scoped.)

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

Git and tmux subprocesses are limited to five seconds each and share one
thirty-second invocation budget beginning with workspace resolution. A failed
or timed-out observation is reported as null/unknown with an issue, never as
clean or absent. Ordinary filesystem reads are synchronous and are not
interruptible by that subprocess budget.

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

			feature := ""
			if len(args) == 1 {
				feature = args[0]
			}

			budget := internal.NewStatusProbeBudget(cmd.Context())
			defer budget.Close()
			ws, degradedReason, err := internal.ResolveStatusWorkspaceWithBudget(feature, budget)
			if err != nil {
				return err
			}
			opts := &internal.AgentStatusOpts{
				Budget: budget,
				ResolveReparentPath: func(feature, ordinaryPath string) (string, error) {
					return reparentFeaturePathFor(ws, feature)
				},
			}
			var report *internal.AgentStatusReport
			if feature != "" {
				report, err = internal.BuildAgentStatusScoped(ws, degradedReason, feature, opts)
			} else {
				report, err = internal.BuildAgentStatus(ws, degradedReason, opts)
			}
			if err != nil {
				return err
			}
			// §11.10 rule 1: the anchored line precedes every stdout write.
			// The report itself is already built read-only; this is a pure
			// projection of what it measured.
			for _, f := range report.Features {
				if f.Reparent != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), f.Reparent.ObservabilityLine())
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
