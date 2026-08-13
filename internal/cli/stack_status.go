package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func stackStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status <feature>",
		Short: "Show stack ancestry, materialization, and upstream status",
		Long: `Report, for every entry of a feature's stack in stack.yaml order: its logical
name and Git branch, local head, configured base and parent head, the recorded
last_base_sha verdict, ancestry state, materialization, dirty and in-progress
Git operation, upstream state, and ahead/behind counts against the parent.

Ancestry comes from the shared evaluator that 'tws doctor' and 'tws list' use,
so the same fixture always reports the same state. This command computes no
ancestry of its own.

Local-only. Nothing is fetched, written, or refreshed: upstream state describes
the configured upstream ref as it exists in this repository right now, and
parent counts compare local commits. A fact that cannot be established locally
is reported as unknown (null in JSON), never as clean, attached, zero, or
"no upstream".

Exit status is 0 whenever a report was produced, including for stale, divergent,
missing, cross-repo, or unevaluated edges and dirty worktrees. A non-zero exit
means no report was produced at all, and nothing is written to stdout.

A feature literally named 'status' is still reachable: 'tws stack -- status'
prints its legacy dependency tree and 'tws stack status status' reports its
stack status.

--json prints one versioned document with a stable key set; absent values are
null and lists are never null.`,
		Args: stackStatusArgs,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			// Deliberately unfiltered: 'tws stack status status' is valid and
			// must stay discoverable.
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Argument and flag errors keep their usage block; every runtime
			// failure prints only its message.
			cmd.SilenceUsage = true

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			cfg := internal.LoadConfig()

			feature := args[0]
			if gerr := internal.GuardFeatureName(ws.MetadataRoot, feature); gerr != nil {
				return gerr
			}

			featurePath, err := ws.ResolveFeaturePath(feature)
			if err != nil {
				return err
			}
			info, statErr := os.Stat(featurePath)
			if statErr != nil || !info.IsDir() {
				return fmt.Errorf("feature not found: %s", feature)
			}

			stack, err := internal.LoadStackForStatus(featurePath, feature)
			if err != nil {
				return err
			}

			report, err := internal.BuildStackStatus(ws, cfg, feature, featurePath, stack)
			if err != nil {
				return err
			}
			internal.NormalizeStackStatus(report)

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			_, _ = fmt.Fprint(cmd.OutOrStdout(), internal.FormatStackStatus(report))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

// stackStatusArgs is cobra.ExactArgs(1) plus one collision hint. Cobra selects
// the `status` child before the parent's RunE, so the parent can never see the
// collision and the hint has to live here.
func stackStatusArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		return nil
	}
	if len(args) > 1 {
		return cobra.ExactArgs(1)(cmd, args)
	}
	features, err := internal.ListFeaturesE()
	if err != nil {
		// The user's actual mistake is the missing argument. Argument
		// validation must not convert a workspace fault into a
		// usage-suppressed failure; that fault surfaces with its real message
		// as soon as an argument is supplied.
		return cobra.ExactArgs(1)(cmd, args)
	}
	for _, name := range features {
		if name == "status" {
			return fmt.Errorf(`accepts 1 arg(s), received 0: a feature named "status" exists; run "tws stack status status" for its stack status report, or "tws stack -- status" for its legacy dependency tree`)
		}
	}
	return cobra.ExactArgs(1)(cmd, args)
}
