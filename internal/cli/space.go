package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func spaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "space",
		Short: "Manage workspace sibling space links",
		Long: `Record where tool-owned sibling spaces live (learning notes, ticket stores,
patch metadata, research, documentation) in <workspace-root>/spaces.yaml.

tws is authoritative for the location only. It never reads, writes, validates,
or deletes the content of a linked space, and 'tws space remove' never deletes
the target directory.`,
	}

	cmd.AddCommand(
		spaceAddCmd(),
		spaceListCmd(),
		spaceShowCmd(),
		spaceRemoveCmd(),
	)

	return cmd
}

// completeSpaceNames offers registered space names. Completion has no error
// channel, so failures yield no candidates.
func completeSpaceNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	anchor, err := internal.ResolveSpacesAnchor()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	views, err := internal.SpaceList(anchor, internal.SpaceListOptions{All: true})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	seen := make(map[string]bool, len(views.Views))
	var names []string
	for _, v := range views.Views {
		if seen[v.Name] {
			continue
		}
		seen[v.Name] = true
		names = append(names, v.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func registerSpaceScopeCompletions(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("feature",
		func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
		})
}

func spaceAddCmd() *cobra.Command {
	var kind string
	var description string
	var feature string

	cmd := &cobra.Command{
		Use:   "add <name> <path>",
		Short: "Register a sibling space",
		Long: `Register a sibling directory under a stable name.

The target must already exist and be a directory; it does not need to be a Git
repository. Targets inside the workspace root are stored workspace-relative;
targets outside are stored absolute. tws never creates the target.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			anchor, err := internal.ResolveSpacesAnchor()
			if err != nil {
				return err
			}

			entry, created, err := internal.SpaceAdd(anchor, internal.SpaceAddRequest{
				Name:        args[0],
				Path:        args[1],
				Kind:        kind,
				Description: description,
				Feature:     feature,
			})
			if err != nil {
				return err
			}

			if !created {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "already registered: %s\n", entry.Name)
				return nil
			}

			resolved := internal.SpaceResolvedPath(anchor, entry.Path)
			scope := ""
			if entry.Feature != "" {
				scope = fmt.Sprintf(" [feature: %s]", entry.Feature)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "registered: %s (%s)%s -> %s\n",
				entry.Name, entry.Kind, scope, resolved)
			return nil
		},
	}

	cmd.Flags().StringVar(&kind, "kind", "", "Space kind (e.g. learning, tickets, patching, research, docs)")
	cmd.Flags().StringVar(&description, "description", "", "Short description (max 200 chars, single line)")
	cmd.Flags().StringVar(&feature, "feature", "", "Scope the space to a feature")
	_ = cmd.MarkFlagRequired("kind")
	_ = cmd.RegisterFlagCompletionFunc("kind",
		func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return internal.SpaceConventionalKinds, cobra.ShellCompDirectiveNoFileComp
		})
	registerSpaceScopeCompletions(cmd)

	return cmd
}

func spaceListCmd() *cobra.Command {
	var feature string
	var all bool
	var kind string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered sibling spaces",
		Long: `List the sibling spaces registered in this workspace.

Scope. With no flags the listing follows your current location: it always shows
every workspace-wide entry, plus the entries of the feature you are inside when
one is auto-detected. Outside any feature the bare listing is already complete.
Pass --all for the complete registry regardless of location, --feature <name>
to scope to a specific feature instead of the detected one, and --kind <kind>
to filter by kind.

Output. The human listing always prints the resolved workspace root, its mode,
and the active scope before the results, so it is never ambiguous which file is
being read. --json prints the entries as a bare array with no header, and an
empty result is [].`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			anchor, err := internal.ResolveSpacesAnchor()
			if err != nil {
				return err
			}

			cwd, _ := os.Getwd()
			result, err := internal.SpaceList(anchor, internal.SpaceListOptions{
				Feature: feature,
				All:     all,
				Kind:    kind,
				Cwd:     cwd,
			})
			if err != nil {
				return err
			}
			views := result.Views
			if views == nil {
				views = []internal.SpaceView{}
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(views)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Workspace: %s (mode: %s, scope: %s)\n\n",
				anchor.Root, anchor.Mode, result.Scope())

			if len(views) == 0 {
				if result.Total == 0 {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(),
						"No spaces registered. Use 'tws space add <name> <path> --kind <kind>' to add one.")
					return nil
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(),
					"No spaces match the active filters (%d registered). Use 'tws space list --all' to see every entry.\n",
					result.Total)
				return nil
			}

			nameWidth, kindWidth := 0, 0
			for _, v := range views {
				if len(v.Name) > nameWidth {
					nameWidth = len(v.Name)
				}
				if len(v.Kind) > kindWidth {
					kindWidth = len(v.Kind)
				}
			}

			for _, v := range views {
				line := fmt.Sprintf("%-*s  %-*s  %s", nameWidth, v.Name, kindWidth, v.Kind, v.ResolvedPath)
				if v.Status != internal.SpaceStatusOK {
					line += fmt.Sprintf("  (%s)", v.Status)
				}
				if v.Feature != "" {
					line += fmt.Sprintf("  (feature: %s)", v.Feature)
				}
				if v.ScopeStatus == internal.SpaceScopeStatusFeatureMissing {
					line += "  (feature missing)"
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&feature, "feature", "", "Show workspace-wide entries plus this feature's entries")
	cmd.Flags().BoolVar(&all, "all", false, "Show every entry regardless of scope")
	cmd.Flags().StringVar(&kind, "kind", "", "Filter by kind")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.MarkFlagsMutuallyExclusive("all", "feature")
	registerSpaceScopeCompletions(cmd)
	_ = cmd.RegisterFlagCompletionFunc("kind",
		func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return internal.SpaceConventionalKinds, cobra.ShellCompDirectiveNoFileComp
		})

	return cmd
}

func spaceShowCmd() *cobra.Command {
	var feature string
	var workspace bool
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one registered sibling space",
		Long: `Show one registered sibling space.

A bare name resolves the unique entry with that name. When the same name exists
both workspace-wide and inside a feature, select the one you mean with
--workspace or --feature <name>.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSpaceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			anchor, err := internal.ResolveSpacesAnchor()
			if err != nil {
				return err
			}

			selector, err := internal.NewSpaceSelector(args[0], feature, workspace)
			if err != nil {
				return err
			}
			view, err := internal.SpaceShow(anchor, selector)
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(view)
			}

			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Name:        %s\n", view.Name)
			_, _ = fmt.Fprintf(out, "Kind:        %s\n", view.Kind)
			scope := "workspace"
			if view.Scope == internal.SpaceScopeFeature {
				scope = "feature " + view.Feature
				if view.ScopeStatus == internal.SpaceScopeStatusFeatureMissing {
					scope += " (missing)"
				}
			}
			_, _ = fmt.Fprintf(out, "Scope:       %s\n", scope)
			_, _ = fmt.Fprintf(out, "Path:        %s\n", view.Path)
			_, _ = fmt.Fprintf(out, "Resolved:    %s\n", view.ResolvedPath)
			_, _ = fmt.Fprintf(out, "Status:      %s\n", view.Status)
			if view.Description != "" {
				_, _ = fmt.Fprintf(out, "Description: %s\n", view.Description)
			}
			_, _ = fmt.Fprintf(out, "Added:       %s\n", view.AddedAt.Format("2006-01-02 15:04:05"))
			if view.UpdatedAt != nil {
				_, _ = fmt.Fprintf(out, "Updated:     %s\n", view.UpdatedAt.Format("2006-01-02 15:04:05"))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&feature, "feature", "", "Restrict to a feature scope")
	cmd.Flags().BoolVar(&workspace, "workspace", false, "Restrict to the workspace-wide scope")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.MarkFlagsMutuallyExclusive("feature", "workspace")
	registerSpaceScopeCompletions(cmd)

	return cmd
}

func spaceRemoveCmd() *cobra.Command {
	var feature string
	var workspace bool

	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a registered sibling space link",
		Long: `Remove the registry entry. The linked directory itself is never deleted.

A bare name resolves the unique entry with that name. When the same name exists
both workspace-wide and inside a feature, select the one you mean with
--workspace or --feature <name>.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSpaceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			anchor, err := internal.ResolveSpacesAnchor()
			if err != nil {
				return err
			}
			selector, err := internal.NewSpaceSelector(args[0], feature, workspace)
			if err != nil {
				return err
			}
			if err := internal.SpaceRemove(anchor, selector); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed space: %s\n", args[0])
			return nil
		},
	}

	cmd.Flags().StringVar(&feature, "feature", "", "Restrict to a feature scope")
	cmd.Flags().BoolVar(&workspace, "workspace", false, "Restrict to the workspace-wide scope")
	cmd.MarkFlagsMutuallyExclusive("feature", "workspace")
	registerSpaceScopeCompletions(cmd)

	return cmd
}
