package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

type pushSelectionFlags struct {
	policy   internal.SyncRunPolicy
	selected bool
	cont     bool
	cancel   bool
	dryRun   bool
}

func resolvePushSelectionFlags(cmd *cobra.Command) (pushSelectionFlags, error) {
	flags := pushSelectionFlags{
		cont: syncBoolFlag(cmd, "continue"), cancel: syncBoolFlag(cmd, "cancel"), dryRun: syncBoolFlag(cmd, "dry-run"),
		policy: internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationLocalOnly},
	}
	only, from := cmd.Flags().Changed("only"), cmd.Flags().Changed("from")
	flags.selected = only || from || cmd.Flags().Changed("continue") || cmd.Flags().Changed("cancel")
	if only && from {
		return flags, errors.New("--only and --from are mutually exclusive")
	}
	if cmd.Flags().Changed("continue") && cmd.Flags().Changed("cancel") {
		return flags, errors.New("--continue and --cancel are mutually exclusive")
	}
	for _, name := range []string{"continue", "cancel"} {
		if cmd.Flags().Changed(name) && !syncBoolFlag(cmd, name) {
			return flags, fmt.Errorf("--%s does not take an explicit false value", name)
		}
	}
	for _, name := range []string{"only", "from"} {
		if cmd.Flags().Changed(name) && strings.TrimSpace(syncStringFlag(cmd, name)) == "" {
			return flags, fmt.Errorf("--%s requires a stack entry name", name)
		}
	}
	if (flags.cont || flags.cancel) && (only || from) {
		return flags, errors.New("--continue and --cancel use the frozen publication plan; do not supply --only or --from")
	}
	if flags.cancel && flags.dryRun {
		return flags, errors.New("--cancel cannot be combined with --dry-run; use --continue --dry-run to inspect")
	}
	if only {
		flags.policy.ScopeKind, flags.policy.Selector = internal.SyncScopeOne, syncStringFlag(cmd, "only")
	}
	if from {
		flags.policy.ScopeKind, flags.policy.Selector = internal.SyncScopeSubtree, syncStringFlag(cmd, "from")
	}
	return flags, nil
}

type scopedPushContext struct {
	workspace internal.Workspace
	layout    externalSyncLayout
	location  internal.ScopedPushLocation
	config    internal.Config
}

// Unlike RequireWorkspace's legacy external fallback, this resolver never
// inventories other features or their worktrees to infer an owner. Layout is
// resolved first; selected repository proof is a separate, bounded step.
func resolveScopedPushContext(feature string, recovery bool) (scopedPushContext, error) {
	var context scopedPushContext
	cwd, err := os.Getwd()
	if err != nil {
		return context, err
	}
	context.config = internal.LoadConfig()
	root, rootErr := internal.MainRepoRoot()
	if rootErr == nil {
		context.workspace, err = internal.ResolveCurrentWorkspaceE(root, context.config)
		if err != nil {
			return context, err
		}
	}
	selected := os.Getenv("TWS_ROOT")
	if selected == "" {
		selected = internal.DetectWorkspaceRoot(cwd, context.config)
	}
	if selected == "" && rootErr == nil {
		selected = context.workspace.MetadataRoot
	}
	if rootErr != nil {
		if selected == "" {
			return context, errors.New("scoped push requires an explicit workspace or a verified repository context")
		}
		context.workspace = internal.Workspace{Mode: internal.ModeExternal, MetadataRoot: selected}
	}
	ws := context.workspace
	if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return context, err
	}
	var featurePath string
	if ws.Mode == internal.ModeCheckout {
		featurePath, err = ws.ResolveFeaturePath(feature)
		if err != nil {
			return context, err
		}
		selected = ""
	} else {
		selected, err = filepath.Abs(selected)
		if err != nil {
			return context, err
		}
		// #5's selected-name check must happen before any configured fallback,
		// even when the selected name currently has no stack.yaml.
		if err := internal.CheckExternalFeatureRenamePending(selected, feature); err != nil {
			return context, err
		}
		if err := internal.GuardFeatureName(selected, feature); err != nil {
			return context, err
		}
		if recovery {
			context.layout, err = resolveScopedPushRecoveryLayout(ws, selected, feature)
			if err != nil {
				return context, err
			}
		}
		if context.layout.FeaturePath == "" {
			context.layout, err = resolveExternalSyncLayoutWith(ws, selected, feature, internal.LoadScopedPushStack, true)
			if err != nil {
				return context, err
			}
		}
		featurePath = context.layout.FeaturePath
	}
	if info, err := os.Lstat(featurePath); err != nil {
		return context, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return context, errors.New("selected publication feature must be a physical directory, not a symlink")
	}
	featurePath, err = filepath.EvalSymlinks(featurePath)
	if err != nil {
		return context, fmt.Errorf("selected feature is unavailable: %w", err)
	}
	metadataRoot := ws.MetadataRoot
	if ws.Mode == internal.ModeExternal {
		metadataRoot = filepath.Dir(featurePath)
		context.layout.FeaturePath = featurePath
		context.layout.WorktreesRoot = filepath.Join(featurePath, "worktrees")
	} else {
		metadataRoot, err = filepath.EvalSymlinks(metadataRoot)
		if err != nil {
			return context, err
		}
	}
	context.location = internal.ScopedPushLocation{
		Mode: ws.Mode, Feature: feature, FeaturePath: featurePath, MetadataRoot: metadataRoot, SelectionRoot: selected,
	}
	return context, nil
}

func resolveScopedPushRecoveryLayout(ws internal.Workspace, selected, feature string) (externalSyncLayout, error) {
	configured, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return externalSyncLayout{}, err
	}
	var terminal *externalSyncLayout
	seen := map[string]bool{}
	for _, path := range []string{filepath.Join(selected, feature), configured} {
		if seen[filepath.Clean(path)] {
			continue
		}
		seen[filepath.Clean(path)] = true
		loc := internal.ScopedPushLocation{
			Mode: internal.ModeExternal, Feature: feature, FeaturePath: path,
			MetadataRoot: filepath.Dir(path), SelectionRoot: selected,
		}
		state, err := internal.LoadScopedPushRecoveryCandidate(loc)
		if err != nil {
			return externalSyncLayout{}, err
		}
		if state == nil {
			continue
		}
		matches, err := internal.ScopedPushRecoverySelectionMatches(loc, state)
		if err != nil {
			return externalSyncLayout{}, err
		}
		if !matches {
			if filepath.Clean(path) == filepath.Join(selected, feature) && !state.Terminal() {
				return externalSyncLayout{}, errors.New("publication execution-selected root changed")
			}
			continue
		}
		layout := newExternalSyncLayout(path)
		if filepath.Clean(path) != filepath.Join(selected, feature) {
			layout.selectionRoot = selected
		}
		if !state.Terminal() {
			return layout, nil
		}
		if terminal == nil {
			terminal = &layout
		}
	}
	if terminal != nil {
		return *terminal, nil
	}
	return externalSyncLayout{}, errors.New("no matching standalone publication journal to recover")
}

func scopedPushSamePath(a, b string) bool {
	a, errA := filepath.EvalSymlinks(a)
	b, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && a == b
}

func scopedPushMainRepository(path string) (internal.ScopedPushRepository, error) {
	repo, err := internal.ScopedPushRepositoryAt(path)
	if err != nil {
		return repo, err
	}
	// A conventional primary repository is stable even if the selected
	// worktree used to prove ownership is subsequently unmaterialized.
	main := repo.Common.Path
	if filepath.Base(main) == ".git" {
		main = filepath.Dir(main)
	}
	owner, err := internal.ScopedPushRepositoryAt(main)
	if err != nil || owner.Common.Identity != repo.Common.Identity || owner.Common.Path != repo.Common.Path {
		return internal.ScopedPushRepository{}, errors.Join(err, errors.New("cannot prove the selected worktree's owning repository"))
	}
	return owner, nil
}

func resolveScopedPushRepositories(context scopedPushContext, selection internal.SyncSelection) (map[string]internal.ScopedPushRepository, string, error) {
	repositories := make(map[string]internal.ScopedPushRepository)
	defaultNeeded := false
	for _, entry := range selection.Entries {
		if entry.Archived {
			continue
		}
		if entry.Repo == "" {
			defaultNeeded = true
			continue
		}
		if !filepath.IsAbs(entry.Repo) {
			return nil, "", fmt.Errorf("selected repository for %s must have an absolute, verifiable path", entry.Name)
		}
		repo, err := internal.ScopedPushRepositoryAt(entry.Repo)
		if err != nil {
			return nil, "", err
		}
		repositories[entry.Name] = repo
	}
	if !defaultNeeded {
		return repositories, "", nil
	}
	var owner internal.ScopedPushRepository
	if context.location.Mode == internal.ModeCheckout {
		var err error
		owner, err = internal.ScopedPushRepositoryAt(context.workspace.RepoRoot)
		if err != nil {
			return nil, "", err
		}
	} else {
		candidates := make(map[string]internal.ScopedPushRepository)
		add := func(path string) error {
			repo, err := scopedPushMainRepository(path)
			if err != nil {
				return err
			}
			candidates[repo.Common.Path] = repo
			return nil
		}
		for repo, root := range context.config.Workspaces {
			if scopedPushSamePath(root, context.location.MetadataRoot) {
				if err := add(repo); err != nil {
					return nil, "", fmt.Errorf("configured default owner is unavailable: %w", err)
				}
			}
		}
		if conventional, ok := strings.CutSuffix(context.location.MetadataRoot, ".tws"); ok {
			if _, err := os.Stat(conventional); err == nil {
				if err := add(conventional); err != nil {
					return nil, "", err
				}
			}
		}
		if context.workspace.RepoRoot != "" && scopedPushSamePath(context.workspace.MetadataRoot, context.location.MetadataRoot) {
			if err := add(context.workspace.RepoRoot); err != nil {
				return nil, "", err
			}
		}
		for _, entry := range selection.Entries {
			if entry.Archived || entry.Repo != "" {
				continue
			}
			path, err := internal.ScopedPushWorktreePath(context.layout.FeaturePath, entry.Name)
			if err != nil {
				return nil, "", err
			}
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, "", err
			}
			if err := add(path); err != nil {
				return nil, "", fmt.Errorf("selected worktree owner is unverifiable: %w", err)
			}
		}
		if len(candidates) != 1 {
			return nil, "", errors.New("cannot prove one default owner from selected context; configure an explicit workspace mapping or selected repository (unrelated worktrees are not searched)")
		}
		for _, repo := range candidates {
			owner = repo
		}
	}
	for _, entry := range selection.Entries {
		if !entry.Archived && entry.Repo == "" {
			repositories[entry.Name] = owner
		}
	}
	return repositories, owner.Directory.Path, nil
}

func runSelectedPush(cmd *cobra.Command, feature string, flags pushSelectionFlags) (retErr error) {
	context, err := resolveScopedPushContext(feature, flags.cont || flags.cancel)
	if err != nil {
		return err
	}
	loc := context.location
	selectionRoot := context.layout.selectionRoot
	if loc.Mode == internal.ModeExternal && !scopedPushSamePath(loc.SelectionRoot, loc.MetadataRoot) {
		selectionRoot = loc.SelectionRoot
	}
	selectedAdmission, err := featureRenameSelectionAdmission(selectionRoot, feature, flags.dryRun)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, selectedAdmission.Release()) }()
	admission, err := featureRenameRouteAdmission(loc.Mode, feature, loc.FeaturePath, flags.dryRun)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, admission.Release()) }()
	opts := internal.ScopedPushOptions{
		Location: loc, Continue: flags.cont, Cancel: flags.cancel, DryRun: flags.dryRun,
		Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
		Verify: func() error {
			return errors.Join(selectedAdmission.Verify(), admission.Verify(),
				refuseSelectedPublication(selectionRoot, feature))
		},
	}
	if !flags.cont && !flags.cancel {
		if err := internal.RefusePendingScopedPush(loc); err != nil {
			return err
		}
		opts.Stack, err = internal.LoadScopedPushStack(loc.FeaturePath)
		if err != nil {
			return err
		}
		opts.Selection, err = internal.ResolveSyncSelection(opts.Stack, flags.policy, internal.SyncSelectionOpts{
			Mode: loc.Mode, NewMode: true, Feature: feature,
		})
		if err != nil {
			return err
		}
		opts.Repositories, opts.DefaultRepo, err = resolveScopedPushRepositories(context, opts.Selection)
		if err != nil {
			return err
		}
	}
	return internal.RunScopedPush(opts)
}

func pushEntryCompletion(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	context, err := resolveScopedPushContext(args[0], false)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	stack, err := internal.LoadScopedPushStack(context.location.FeaturePath)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, entry := range stack.Branches {
		if !entry.Archived {
			names = append(names, entry.Name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
