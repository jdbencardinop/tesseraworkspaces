package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func templateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage workspace templates",
	}

	cmd.AddCommand(
		templateRegistryAddCmd(),
		templateRegistryListCmd(),
		templateRegistryShowCmd(),
		templateRegistryRemoveCmd(),
		templateSyncCmd(),
	)

	return cmd
}

func templateRegistryAddCmd() *cobra.Command {
	var repo bool
	cmd := &cobra.Command{
		Use:   "add <name> <path>",
		Short: "Copy a validated package into the named template registry",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope := internal.TemplateScopeGlobal
			repoRoot := ""
			if repo {
				ws, err := internal.RequireWorkspace()
				if err != nil {
					return fmt.Errorf("--repo requires an unambiguous owning repository: %w", err)
				}
				ws, err = managedTemplateExecutionWorkspace(ws)
				if err != nil {
					return fmt.Errorf("--repo requires an unambiguous owning repository: %w", err)
				}
				scope = internal.TemplateScopeRepo
				repoRoot = ws.RepoRoot
			}
			if err := internal.AddNamedTemplate(repoRoot, scope, args[0], args[1]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Added %s template %s\n", scope, args[0])
			return err
		},
	}
	cmd.Flags().BoolVar(&repo, "repo", false, "Use the owning-repository registry instead of the global registry")
	return cmd
}

func templateRegistryListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List named templates and their origin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := optionalTemplateRepoRoot()
			if err != nil {
				return err
			}
			entries, err := internal.ListNamedTemplates(repoRoot)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No named templates found.")
				return err
			}
			for _, entry := range entries {
				effective := " shadowed"
				if entry.Effective {
					effective = " effective"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s%s\t%s\n", entry.Name, entry.Scope, effective, entry.Path); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func templateRegistryShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a named template's resolved origin and package contents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := optionalTemplateRepoRoot()
			if err != nil {
				return err
			}
			resolved, pkg, err := internal.ShowNamedTemplate(repoRoot, args[0])
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "name: %s\nscope: %s\npath: %s\n",
				resolved.Reference.Name, resolved.Reference.Scope, resolved.Path); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "feature:"); err != nil {
				return err
			}
			for _, file := range pkg.Feature {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", file.Path); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "inject:"); err != nil {
				return err
			}
			for _, file := range pkg.Inject {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", file.Path); err != nil {
					return err
				}
			}
			if pkg.Manifest.Version != 0 {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "manifest_version: %d\n", pkg.Manifest.Version); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.ValidArgsFunction = namedTemplateCompletion
	return cmd
}

func templateRegistryRemoveCmd() *cobra.Command {
	var repo bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove one named registry entry without changing features",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope := internal.TemplateScopeGlobal
			repoRoot := ""
			if repo {
				ws, err := internal.RequireWorkspace()
				if err != nil {
					return fmt.Errorf("--repo requires an unambiguous owning repository: %w", err)
				}
				ws, err = managedTemplateExecutionWorkspace(ws)
				if err != nil {
					return fmt.Errorf("--repo requires an unambiguous owning repository: %w", err)
				}
				scope = internal.TemplateScopeRepo
				repoRoot = ws.RepoRoot
			}
			if err := internal.RemoveNamedTemplate(repoRoot, scope, args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Removed %s template %s\n", scope, args[0])
			return err
		},
	}
	cmd.Flags().BoolVar(&repo, "repo", false, "Use the owning-repository registry instead of the global registry")
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		scope := internal.TemplateScopeGlobal
		repoRoot := ""
		if repo {
			ws, err := internal.RequireWorkspace()
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			ws, err = managedTemplateExecutionWorkspace(ws)
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			scope = internal.TemplateScopeRepo
			repoRoot = ws.RepoRoot
		}
		entries, err := internal.ListNamedTemplates(repoRoot)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var names []string
		for _, entry := range entries {
			if entry.Scope == scope {
				names = append(names, entry.Name)
			}
		}
		sort.Strings(names)
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}

func namedTemplateCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return namedTemplateFlagCompletion(cmd, args, toComplete)
}

func namedTemplateFlagCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	repoRoot, err := optionalTemplateRepoRoot()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	entries, err := internal.ListNamedTemplates(repoRoot)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Scope+":"+entry.Name)
		if entry.Effective {
			names = append(names, entry.Name)
		}
	}
	sort.Strings(names)
	return names, cobra.ShellCompDirectiveNoFileComp
}

func optionalTemplateRepoRoot() (string, error) {
	ws, err := internal.RequireWorkspace()
	if err == nil {
		resolved, resolveErr := managedTemplateExecutionWorkspace(ws)
		if resolveErr != nil {
			return "", resolveErr
		}
		return resolved.RepoRoot, nil
	}
	if _, repoErr := internal.MainRepoRoot(); repoErr == nil {
		return "", err
	}
	return "", nil
}

func templateSyncCmd() *cobra.Command {
	var templates []string
	var all bool
	var scope string
	var worktreeTemplate string
	var orchestratorTemplate string
	var agent string
	var allowManagedRemoval bool

	cmd := &cobra.Command{
		Use:   "sync [feature]",
		Short: "Apply legacy or managed template files to existing feature(s)",
		Long: `Refresh feature templates.

Directory-valued --template inputs retain legacy copy-if-missing behavior.
Named packages, role flags, --agent, or an explicit --scope use managed,
ownership-tracked updates. Managed features default to both roles.`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return managedCompletionFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return errors.New("--all cannot be combined with a feature argument")
			}
			if !all && len(args) == 0 {
				return fmt.Errorf("usage: tws template sync <feature> [--template <dir-or-name>]\n       tws template sync --all")
			}
			classified, err := classifyTemplateArgs("", templates)
			if err != nil {
				return err
			}
			flags := managedFlagsFromCommand(cmd, worktreeTemplate, orchestratorTemplate, agent, allowManagedRemoval)
			if err := applyNamedAlias(&flags, classified); err != nil {
				return err
			}
			scopeChanged := cmd.Flags().Changed("scope")
			if len(classified.legacyDirs) > 0 &&
				(flags.worktreeTemplateSet || flags.orchestratorSet || flags.agentSet || scopeChanged) {
				return errors.New("cannot combine a legacy directory --template with managed role, agent, or scope selectors")
			}
			roles, err := managedTemplateRolesForScope(scope, scopeChanged)
			if err != nil {
				return err
			}
			if flags.worktreeTemplateSet && !containsManagedRole(roles, internal.ManagedTemplateRoleWorktree) {
				return errors.New("--worktree-template is outside the selected --scope")
			}
			if flags.orchestratorSet && !containsManagedRole(roles, internal.ManagedTemplateRoleOrchestrator) {
				return errors.New("--orchestrator-template is outside the selected --scope")
			}
			if classified.namedSet && len(roles) != 2 {
				return errors.New("the named --template alias selects both roles and requires --scope both")
			}
			managedExplicit := classified.namedSet || flags.worktreeTemplateSet ||
				flags.orchestratorSet || flags.agentSet || scopeChanged
			ws, wsErr := internal.RequireWorkspace()
			if wsErr != nil {
				if managedExplicit {
					return wsErr
				}
				return syncTemplatesWithoutWorkspace(
					args, all, classified.legacyDirs, wsErr,
				)
			}
			features := args
			if all {
				ws, err = managedTemplateExecutionWorkspace(ws)
				if err != nil {
					return err
				}

				features, err = ws.ListFeaturesResolved()
				if err != nil {
					return err
				}
				if len(features) == 0 {
					fmt.Println("No features found.")
					return nil
				}
			}
			sort.Strings(features)
			var plans []*internal.ManagedTemplatePlan
			var legacy []resolvedFeatureContext
			for _, feature := range features {
				featureWS := ws
				if !all {
					featureWS, err = managedTemplateWorkspaceForFeature(ws, feature)
					if err != nil {
						return err
					}
				}
				if err := internal.GuardFeatureName(featureWS.MetadataRoot, feature); err != nil {
					return err
				}
				featurePath, err := featureWS.ResolveFeaturePath(feature)
				if err != nil {
					return err
				}
				exists, err := featureExists(featurePath)
				if err != nil {
					return err
				}
				if !exists {
					return fmt.Errorf("feature not found: %s", feature)
				}
				managedState, err := internal.ManagedTemplateStatePresent(featurePath)
				if err != nil {
					return err
				}
				if len(classified.legacyDirs) > 0 {
					if managedState {
						return fmt.Errorf("%s is managed; directory templates cannot silently downgrade it", feature)
					}
					legacy = append(legacy, resolvedFeatureContext{
						Workspace: featureWS, Feature: feature, FeaturePath: featurePath,
					})
					continue
				}
				if !managedExplicit && !managedState {
					legacy = append(legacy, resolvedFeatureContext{
						Workspace: featureWS, Feature: feature, FeaturePath: featurePath,
					})
					continue
				}
				plan, err := internal.PlanManagedTemplate(managedPlanRequest(featureWS, feature, featurePath, roles, flags))
				if err != nil {
					return fmt.Errorf("%s: %w", feature, err)
				}
				plans = append(plans, plan)
			}
			if err := confirmManagedTemplateRemovals(cmd, plans, flags.allowRemoval); err != nil {
				return err
			}
			if managedTemplatePostConsentHook != nil {
				if err := managedTemplatePostConsentHook(); err != nil {
					return err
				}
			}
			if err := executeManagedTemplatePlans(cmd, plans); err != nil {
				return err
			}
			for _, plan := range plans {
				if containsManagedRole(plan.Request.Roles, internal.ManagedTemplateRoleWorktree) &&
					plan.Request.Workspace.Mode == internal.ModeExternal {
					if err := injectUnmanagedFilesAfterManagedSync(plan); err != nil {
						return fmt.Errorf("%s: managed files were applied but user inject files could not be synchronized: %w",
							plan.Request.Feature, err)
					}
				}
			}
			for _, context := range legacy {
				syncFeatureTemplateAt(context.Feature, context.FeaturePath, classified.legacyDirs)
			}
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&templates, "template", nil, "Legacy template directory or one named template package")
	cmd.Flags().BoolVar(&all, "all", false, "Sync all features")
	cmd.Flags().StringVar(&scope, "scope", "both", "Managed role scope: worktree, orchestrator, or both")
	cmd.Flags().StringVar(&worktreeTemplate, "worktree-template", "", "Named template reference for worktree guidance")
	cmd.Flags().StringVar(&orchestratorTemplate, "orchestrator-template", "", "Named template reference for orchestrator guidance")
	cmd.Flags().StringVar(&agent, "agent", "", "Rendering agent: claude or copilot")
	cmd.Flags().BoolVar(&allowManagedRemoval, "allow-managed-removal", false, "Consent to removal of listed unchanged managed paths")
	_ = cmd.RegisterFlagCompletionFunc("scope", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"worktree", "orchestrator", "both"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("agent", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"claude", "copilot"}, cobra.ShellCompDirectiveNoFileComp
	})
	for _, name := range []string{"worktree-template", "orchestrator-template"} {
		_ = cmd.RegisterFlagCompletionFunc(name, namedTemplateFlagCompletion)
	}
	_ = cmd.RegisterFlagCompletionFunc("template", namedTemplateAliasCompletion)

	return cmd
}

func namedTemplateAliasCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	values, directive := namedTemplateFlagCompletion(cmd, args, toComplete)
	if directive == cobra.ShellCompDirectiveError {
		return nil, directive
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, cobra.ShellCompDirectiveDefault
}

func syncTemplatesWithoutWorkspace(
	args []string,
	all bool,
	legacyDirs []string,
	workspaceErr error,
) error {
	root := internal.TwsRoot()
	features := args
	if all {
		var err error
		features, err = internal.ListFeaturesE()
		if err != nil {
			return err
		}
		if len(features) == 0 {
			fmt.Println("No features found.")
			return nil
		}
	}
	sort.Strings(features)
	type legacyCandidate struct {
		feature string
		path    string
	}
	candidates := make([]legacyCandidate, 0, len(features))
	for _, feature := range features {
		if err := internal.GuardFeatureName(root, feature); err != nil {
			return err
		}
		path := filepath.Join(root, feature)
		exists, err := featureExists(path)
		if err != nil {
			return err
		}
		if !exists {
			if !all {
				syncFeatureTemplate(feature, legacyDirs)
				return nil
			}
			continue
		}
		present, err := internal.ManagedTemplateStatePresent(path)
		if err != nil {
			return err
		}
		if present {
			return fmt.Errorf("cannot resolve owning repository for managed feature %q: %w",
				feature, workspaceErr)
		}
		candidates = append(candidates, legacyCandidate{feature: feature, path: path})
	}
	for _, candidate := range candidates {
		syncFeatureTemplateAt(candidate.feature, candidate.path, legacyDirs)
	}
	return nil
}

func managedTemplateRolesForScope(scope string, explicit bool) ([]internal.ManagedTemplateRole, error) {
	if !explicit && scope == "" {
		scope = "both"
	}
	switch scope {
	case "worktree":
		return []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree}, nil
	case "orchestrator":
		return []internal.ManagedTemplateRole{internal.ManagedTemplateRoleOrchestrator}, nil
	case "both", "":
		return []internal.ManagedTemplateRole{
			internal.ManagedTemplateRoleWorktree,
			internal.ManagedTemplateRoleOrchestrator,
		}, nil
	default:
		return nil, fmt.Errorf("invalid --scope %q (want worktree, orchestrator, or both)", scope)
	}
}

func containsManagedRole(roles []internal.ManagedTemplateRole, want internal.ManagedTemplateRole) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}

func injectUnmanagedFilesAfterManagedSync(plan *internal.ManagedTemplatePlan) error {
	excluded, err := internal.ManagedTemplateOwnedWorktreePaths(plan.Request.FeaturePath)
	if err != nil {
		return err
	}
	targets, err := internal.DiscoverInjectWorktrees(plan.Request.FeaturePath)
	if err != nil {
		return err
	}
	var errs []error
	for _, target := range targets {
		if err := internal.InjectFilesExcluding(plan.Request.FeaturePath, target.Path, "", excluded); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", target.Name, err))
		}
	}
	return errors.Join(errs...)
}

func syncFeatureTemplate(feature string, extraTemplates []string) {
	featurePath, err := internal.RequireFeaturePath(feature)
	if err != nil {
		fmt.Printf("%s: %v\n", feature, err)
		return
	}
	syncFeatureTemplateAt(feature, featurePath, extraTemplates)
}

func syncFeatureTemplateAt(feature, featurePath string, extraTemplates []string) {
	injectDir := internal.InjectPath(featurePath)
	if err := os.MkdirAll(injectDir, 0755); err != nil {
		fmt.Printf("%s: error creating inject dir: %v\n", feature, err)
		return
	}

	fmt.Printf("%s:\n", feature)
	applyTemplates(injectDir, extraTemplates)

	target := internal.ResolveInjectInto("")
	count, err := internal.InjectFilesForFeature(featurePath, target)
	if err != nil {
		fmt.Printf("  Warning: worktree inject failed: %v\n", err)
	} else if count > 0 {
		fmt.Printf("  Synced inject to %d worktree(s)\n", count)
	}
}
