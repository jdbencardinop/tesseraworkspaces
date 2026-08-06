package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func registryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Manage the global workspace registry",
	}

	cmd.AddCommand(
		registryAddCmd(),
		registryListCmd(),
		registryShowCmd(),
		registryAliasCmd(),
		registryCheckCmd(),
		registryRepairCmd(),
		registryRemoveCmd(),
		registryPruneCmd(),
	)

	return cmd
}

func registryAddCmd() *cobra.Command {
	var alias string
	cmd := &cobra.Command{
		Use:   "add [path]",
		Short: "Register a workspace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}

			entry, created, err := internal.RegistryAdd(path, alias)
			if err != nil {
				return err
			}

			if created {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "registered: %s (%s)\n", entry.ID, entry.Path)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "already registered: %s (%s)\n", entry.ID, entry.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&alias, "alias", "", "Alias for the workspace")
	return cmd
}

func registryListCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := internal.RegistryList()
			if err != nil {
				return err
			}
			if entries == nil {
				entries = []internal.RegistryEntry{}
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			if len(entries) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no workspaces registered")
				return nil
			}

			for _, e := range entries {
				aliases := ""
				if len(e.Aliases) > 0 {
					aliases = " [" + strings.Join(e.Aliases, ", ") + "]"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  %-18s  %s%s\n", e.ID, e.Kind, e.Path, aliases)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func registryShowCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "show <selector>",
		Short: "Show details of a registered workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entry, err := internal.RegistryResolve(args[0])
			if err != nil {
				return err
			}
			if entry == nil {
				return fmt.Errorf("no entry matching %q", args[0])
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(entry)
			}

			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "ID:          %s\n", entry.ID)
			_, _ = fmt.Fprintf(w, "Path:        %s\n", entry.Path)
			_, _ = fmt.Fprintf(w, "Kind:        %s\n", entry.Kind)
			if len(entry.Aliases) > 0 {
				_, _ = fmt.Fprintf(w, "Aliases:     %s\n", strings.Join(entry.Aliases, ", "))
			}
			if entry.GitIdentity != "" {
				_, _ = fmt.Fprintf(w, "Identity:    %s\n", entry.GitIdentity)
			}
			if entry.MarkerID != "" {
				_, _ = fmt.Fprintf(w, "Marker:      %s\n", entry.MarkerID)
			}
			_, _ = fmt.Fprintf(w, "Added:       %s\n", entry.AddedAt.Format("2006-01-02 15:04:05"))
			if !entry.UpdatedAt.IsZero() {
				_, _ = fmt.Fprintf(w, "Updated:     %s\n", entry.UpdatedAt.Format("2006-01-02 15:04:05"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func registryAliasCmd() *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "alias <selector> <alias>",
		Short: "Add or remove an alias",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := internal.RegistryAlias(args[0], args[1], remove); err != nil {
				return err
			}
			if remove {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed alias %q\n", args[1])
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "added alias %q\n", args[1])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "Remove the alias instead of adding")
	return cmd
}

func registryCheckCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "check [selector]",
		Short: "Check health of registered workspaces",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				entry, err := internal.RegistryResolve(args[0])
				if err != nil {
					return err
				}
				if entry == nil {
					return fmt.Errorf("no entry matching %q", args[0])
				}
				result := internal.RegistryCheck(entry)
				if jsonOutput {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode([]internal.CheckResult{result})
				}
				printCheckResult(cmd.OutOrStdout(), result)
				return nil
			}

			entries, err := internal.RegistryList()
			if err != nil {
				return err
			}

			results := []internal.CheckResult{}
			for i := range entries {
				results = append(results, internal.RegistryCheck(&entries[i]))
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(results)
			}

			if len(results) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no workspaces registered")
				return nil
			}

			for _, r := range results {
				printCheckResult(cmd.OutOrStdout(), r)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

// printCheckResult renders one human-readable check result plus a concise
// recovery hint. Hints are human-output only; JSON output is unchanged.
func printCheckResult(w io.Writer, r internal.CheckResult) {
	_, _ = fmt.Fprintf(w, "%s  %s: %s", r.Entry.ID, r.Entry.Path, r.Status)
	if r.Detail != "" {
		_, _ = fmt.Fprintf(w, " (%s)", r.Detail)
	}
	_, _ = fmt.Fprintln(w)
	if hint := checkRecoveryHint(r); hint != "" {
		_, _ = fmt.Fprintf(w, "  hint: %s\n", hint)
	}
}

func checkRecoveryHint(r internal.CheckResult) string {
	switch r.Status {
	case internal.StatusMissing:
		return fmt.Sprintf("run 'tws registry repair %s <new-path>' if it moved, or 'tws registry prune --missing' to drop it", r.Entry.ID)
	case internal.StatusMismatched, internal.StatusInvalid:
		return fmt.Sprintf("run 'tws registry repair %s <path>' (add --allow-identity-change if the target was intentionally replaced)", r.Entry.ID)
	default:
		return ""
	}
}

func registryRepairCmd() *cobra.Command {
	var allowIdentityChange bool
	cmd := &cobra.Command{
		Use:   "repair <selector> <new-path>",
		Short: "Move a registry entry to a validated new path",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := internal.RegistryRepair(args[0], args[1], allowIdentityChange); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "registry entry repaired")
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowIdentityChange, "allow-identity-change", false, "accept a different kind or repository identity at the new path")
	return cmd
}

func registryRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <selector>",
		Short: "Remove a workspace from the registry (does not delete files)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := internal.RegistryRemove(args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "removed from registry")
			return nil
		},
	}
	return cmd
}

func registryPruneCmd() *cobra.Command {
	var force bool
	var missing bool
	cmd := &cobra.Command{
		Use:   "prune --missing",
		Short: "Remove entries whose paths no longer exist",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !missing {
				return fmt.Errorf("--missing is required; registry prune only removes missing targets")
			}
			if !isTerminal() && !force {
				return fmt.Errorf("non-TTY environment requires --force")
			}

			removed, err := internal.RegistryPrune()
			if err != nil {
				return err
			}

			if len(removed) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "nothing to prune")
				return nil
			}

			for _, e := range removed {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "pruned: %s (%s)\n", e.ID, e.Path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&missing, "missing", false, "Remove entries whose targets are missing")
	cmd.Flags().BoolVar(&force, "force", false, "Required for non-TTY environments")
	return cmd
}

// isTerminal checks if stdout is a terminal.
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
