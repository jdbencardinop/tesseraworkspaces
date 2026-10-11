package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

type managedTemplateFlags struct {
	worktreeTemplate     string
	worktreeTemplateSet  bool
	orchestratorTemplate string
	orchestratorSet      bool
	agent                string
	agentSet             bool
	allowRemoval         bool
	resolveConflicts     bool
}

type classifiedTemplateArgs struct {
	legacyDirs []string
	named      string
	namedSet   bool
}

type heldManagedTemplateLocks struct {
	template []*internal.ManagedTemplateLock
	external []struct {
		path  string
		token string
	}
	checkoutStateDir string
	checkoutToken    string
}

type managedFeatureRenameReservation struct {
	workspace        internal.Workspace
	feature          string
	featurePath      string
	externalToken    string
	checkoutStateDir string
	checkoutToken    string
}

var managedTemplateInputIsTTY = managedTemplateTerminalInput

var managedTemplatePostConsentHook func() error
var managedTemplateTmuxProbe internal.TmuxInventoryProbe

type resolvedFeatureContext struct {
	Workspace   internal.Workspace
	Feature     string
	FeaturePath string
}

type resolvedManagedConfig struct {
	Config       internal.Config
	AgentCommand string
}

type managedInjectTarget struct {
	Value    string
	Explicit bool
}

func resolveManagedConfig(ws internal.Workspace) (resolvedManagedConfig, error) {
	cfg, err := internal.LoadConfigForRepoE(ws.RepoRoot)
	if err != nil {
		return resolvedManagedConfig{}, err
	}
	return resolvedManagedConfig{Config: cfg, AgentCommand: cfg.GetAgentCommand()}, nil
}

func managedTemplateExecutionWorkspace(ws internal.Workspace) (internal.Workspace, error) {
	if ws.Mode != internal.ModeExternal {
		return ws, nil
	}
	selectedRoot := filepath.Clean(internal.TwsRoot())
	selected, err := internal.ResolveExternalWorkspaceAt(selectedRoot)
	managedOwner, found, ownerErr := internal.ResolveManagedTemplateOwnerWorkspace(selectedRoot, ws.RepoRoot)
	if ownerErr != nil {
		return internal.Workspace{}, ownerErr
	}
	if found {
		if err == nil {
			if sameResolvedRepoRoot(managedOwner.RepoRoot, selected.RepoRoot) {
				return managedOwner, nil
			}
			return internal.Workspace{}, fmt.Errorf(
				"managed external workspace %s belongs to repository %s, not configured repository %s",
				selectedRoot, managedOwner.RepoRoot, selected.RepoRoot,
			)
		}
		if !sameResolvedRepoRoot(managedOwner.RepoRoot, ws.RepoRoot) {
			secondary, inspectErr := externalWorkspaceUsesRepoAsSecondary(selectedRoot, ws.RepoRoot)
			if inspectErr != nil {
				return internal.Workspace{}, inspectErr
			}
			if !secondary {
				return internal.Workspace{}, fmt.Errorf(
					"managed external workspace %s belongs to repository %s, not unrelated repository %s",
					selectedRoot, managedOwner.RepoRoot, ws.RepoRoot,
				)
			}
		}
		return managedOwner, nil
	}
	if err == nil {
		return selected, nil
	}
	fresh, freshErr := freshExternalWorkspaceRoot(selectedRoot)
	if freshErr != nil {
		return internal.Workspace{}, freshErr
	}
	if fresh {
		ws.MetadataRoot = selectedRoot
		return ws, nil
	}
	secondary, inspectErr := externalWorkspaceUsesRepoAsSecondary(selectedRoot, ws.RepoRoot)
	if inspectErr != nil {
		return internal.Workspace{}, inspectErr
	}
	if !secondary && !strings.Contains(err.Error(), "multiple default repositories") {
		ws.MetadataRoot = selectedRoot
		return ws, nil
	}
	return internal.Workspace{}, fmt.Errorf(
		"cannot determine the primary repository for established external workspace %s from repository %s: %w",
		selectedRoot, ws.RepoRoot, err,
	)
}

func freshExternalWorkspaceRoot(root string) (bool, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading external workspace root %s: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("external workspace root %s is not a regular directory", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, fmt.Errorf("reading external workspace root %s: %w", root, err)
	}
	return len(entries) == 0, nil
}

func externalWorkspaceUsesRepoAsSecondary(workspaceRoot, repoRoot string) (bool, error) {
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return false, fmt.Errorf("reading external workspace root %s: %w", workspaceRoot, err)
	}
	for _, feature := range entries {
		if !feature.IsDir() || feature.Name() == ".tws-workspace" {
			continue
		}
		featurePath := filepath.Join(workspaceRoot, feature.Name())
		if _, err := os.Lstat(internal.StackPath(featurePath)); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return false, fmt.Errorf("reading stack metadata for feature %s: %w", feature.Name(), err)
		}
		stack, err := internal.LoadStack(featurePath)
		if err != nil {
			return false, fmt.Errorf("reading stack metadata for feature %s: %w", feature.Name(), err)
		}
		for _, entry := range stack.Branches {
			if entry.Repo == "" {
				continue
			}
			entryRepo, err := internal.MainRepoRootIn(entry.Repo)
			if err != nil {
				return false, fmt.Errorf("resolving repository for stack entry %s/%s: %w",
					feature.Name(), entry.Name, err)
			}
			if sameResolvedRepoRoot(entryRepo, repoRoot) {
				return true, nil
			}
		}
	}
	return false, nil
}

func managedTemplateWorkspaceForFeature(ws internal.Workspace, feature string) (internal.Workspace, error) {
	if ws.Mode != internal.ModeExternal {
		return ws, nil
	}
	selectedRoot := filepath.Clean(internal.TwsRoot())
	if err := internal.GuardFeatureName(selectedRoot, feature); err != nil {
		return internal.Workspace{}, err
	}
	selectedFeaturePath := filepath.Join(selectedRoot, filepath.FromSlash(feature))
	if _, err := os.Lstat(selectedFeaturePath); err == nil {
		present, stateErr := internal.ManagedTemplateStatePresent(selectedFeaturePath)
		if stateErr != nil {
			return internal.Workspace{}, stateErr
		}
		if !present {
			selected, resolveErr := internal.ResolveExternalWorkspaceAt(selectedRoot)
			if resolveErr == nil {
				return selected, nil
			}
			ws.Mode = internal.ModeExternal
			ws.MetadataRoot = selectedRoot
			return ws, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return internal.Workspace{}, err
	}
	selected, selectedErr := managedTemplateExecutionWorkspace(ws)
	if selectedErr != nil {
		return internal.Workspace{}, selectedErr
	}
	if filepath.Clean(selected.MetadataRoot) == filepath.Clean(ws.MetadataRoot) {
		return selected, nil
	}
	selectedPath, err := selected.ResolveFeaturePath(feature)
	if err != nil {
		return internal.Workspace{}, err
	}
	if _, err := os.Lstat(selectedPath); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return selected, nil
	}
	currentPath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return internal.Workspace{}, err
	}
	if _, err := os.Lstat(currentPath); err == nil || !errors.Is(err, fs.ErrNotExist) {
		current, resolveErr := internal.ResolveExternalWorkspaceAt(ws.MetadataRoot)
		if resolveErr == nil {
			return current, nil
		}
		return ws, nil
	}
	return selected, nil
}

func resolveFeatureContext(ws internal.Workspace, feature string) (resolvedFeatureContext, error) {
	resolvedWS, err := managedTemplateWorkspaceForFeature(ws, feature)
	if err != nil {
		return resolvedFeatureContext{}, err
	}
	if err := internal.GuardFeatureName(resolvedWS.MetadataRoot, feature); err != nil {
		return resolvedFeatureContext{}, err
	}
	featurePath, err := resolvedWS.ResolveFeaturePath(feature)
	if err != nil {
		return resolvedFeatureContext{}, err
	}
	return resolvedFeatureContext{
		Workspace: resolvedWS, Feature: feature, FeaturePath: featurePath,
	}, nil
}

func managedCompletionFeatures() []string {
	ws, err := internal.RequireWorkspace()
	if err != nil {
		return nil
	}
	ws, err = managedTemplateExecutionWorkspace(ws)
	if err != nil {
		return nil
	}
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		return nil
	}
	return features
}

func managedCompletionBranches(feature string) []string {
	ws, err := internal.RequireWorkspace()
	if err != nil {
		return nil
	}
	context, err := resolveFeatureContext(ws, feature)
	if err != nil {
		return nil
	}
	return internal.ListBranchesAt(context.FeaturePath)
}

func classifyTemplateArgs(repoRoot string, values []string) (classifiedTemplateArgs, error) {
	var out classifiedTemplateArgs
	for _, value := range values {
		if strings.HasPrefix(value, internal.TemplateScopeRepo+":") ||
			strings.HasPrefix(value, internal.TemplateScopeGlobal+":") {
			if _, err := internal.ParseTemplateReference(value); err != nil {
				return classifiedTemplateArgs{}, err
			}
			if out.namedSet {
				return classifiedTemplateArgs{}, errors.New("only one named --template package may be selected")
			}
			out.named = value
			out.namedSet = true
			continue
		}
		info, err := os.Stat(value)
		if err == nil && info.IsDir() {
			out.legacyDirs = append(out.legacyDirs, value)
			continue
		}
		explicitPath := filepath.IsAbs(value) || filepath.VolumeName(value) != "" ||
			strings.ContainsAny(value, `/\`) || strings.HasPrefix(value, ".")
		if explicitPath || (err == nil && !info.IsDir()) {
			out.legacyDirs = append(out.legacyDirs, value)
			continue
		}
		if _, err := internal.ParseTemplateReference(value); err != nil {
			return classifiedTemplateArgs{}, fmt.Errorf("--template %q is neither a directory path nor a valid named template: %w", value, err)
		}
		if out.namedSet {
			return classifiedTemplateArgs{}, errors.New("only one named --template package may be selected")
		}
		out.named = value
		out.namedSet = true
	}
	if out.namedSet && len(out.legacyDirs) > 0 {
		return classifiedTemplateArgs{}, errors.New("cannot mix directory templates with a named template package")
	}
	return out, nil
}

func managedTemplateDefaultsConfigured(repoRoot string) (bool, error) {
	globalCfg, repoCfg, err := internal.LoadTemplateConfigLayersE(repoRoot)
	if err != nil {
		return false, err
	}
	return repoCfg.WorktreeTemplate != "" || repoCfg.OrchestratorTemplate != "" ||
		globalCfg.WorktreeTemplate != "" || globalCfg.OrchestratorTemplate != "", nil
}

func managedFlagsFromCommand(cmd *cobra.Command, worktree, orchestrator, agent string, allow, resolve bool) managedTemplateFlags {
	return managedTemplateFlags{
		worktreeTemplate:     worktree,
		worktreeTemplateSet:  cmd.Flags().Changed("worktree-template"),
		orchestratorTemplate: orchestrator,
		orchestratorSet:      cmd.Flags().Changed("orchestrator-template"),
		agent:                agent,
		agentSet:             cmd.Flags().Changed("agent"),
		allowRemoval:         allow,
		resolveConflicts:     resolve,
	}
}

func managedPlanRequest(
	ws internal.Workspace,
	feature, featurePath string,
	roles []internal.ManagedTemplateRole,
	flags managedTemplateFlags,
) internal.ManagedTemplatePlanRequest {
	return internal.ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath, Roles: roles,
		Worktree: internal.ManagedTemplateRoleRequest{
			Reference: flags.worktreeTemplate, ReferenceSet: flags.worktreeTemplateSet,
		},
		Orchestrator: internal.ManagedTemplateRoleRequest{
			Reference: flags.orchestratorTemplate, ReferenceSet: flags.orchestratorSet,
		},
		Agent: flags.agent, AgentSet: flags.agentSet,
		ResolveConflicts: flags.resolveConflicts,
	}
}

func applyNamedAlias(flags *managedTemplateFlags, classified classifiedTemplateArgs) error {
	if !classified.namedSet {
		return nil
	}
	if flags.worktreeTemplateSet || flags.orchestratorSet {
		return errors.New("the named --template alias cannot be combined with role template flags")
	}
	flags.worktreeTemplate = classified.named
	flags.worktreeTemplateSet = true
	flags.orchestratorTemplate = classified.named
	flags.orchestratorSet = true
	return nil
}

func confirmManagedTemplateRemovals(
	cmd *cobra.Command,
	plans []*internal.ManagedTemplatePlan,
	allow bool,
	reader *bufio.Reader,
) error {
	var removals []internal.ManagedTemplateRemoval
	for _, plan := range plans {
		removals = append(removals, plan.Removals...)
	}
	if len(removals) == 0 {
		return nil
	}
	sort.Slice(removals, func(i, j int) bool {
		if removals[i].Feature != removals[j].Feature {
			return removals[i].Feature < removals[j].Feature
		}
		if removals[i].Role != removals[j].Role {
			return removals[i].Role < removals[j].Role
		}
		if removals[i].Kind != removals[j].Kind {
			return removals[i].Kind < removals[j].Kind
		}
		return removals[i].Path < removals[j].Path
	})
	errOut := managedTemplateCheckedWriter{cmd.ErrOrStderr()}
	if _, err := fmt.Fprintln(errOut, "Managed template update would remove these unchanged TWS-managed paths:"); err != nil {
		return err
	}
	for _, removal := range removals {
		if _, err := fmt.Fprintf(errOut, "  feature=%s role=%s kind=%s path=%s\n",
			removal.Feature, removal.Role, removal.Kind, removal.Path); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(errOut, "Remove these unchanged TWS-managed files and apply the template update? [y/N] "); err != nil {
		return err
	}
	if allow {
		if _, err := fmt.Fprintln(errOut); err != nil {
			return err
		}
		return nil
	}
	input := cmd.InOrStdin()
	if !managedTemplateInputIsTTY(input) {
		if _, err := fmt.Fprintln(errOut); err != nil {
			return err
		}
		return errors.New("managed template removal requires an interactive y/yes answer or --allow-managed-removal")
	}
	answer, err := reader.ReadString('\n')
	if errors.Is(err, io.EOF) {
		if _, writeErr := fmt.Fprintln(errOut); writeErr != nil {
			return writeErr
		}
		return errors.New("managed template update cancelled: no affirmative response")
	}
	if err != nil {
		if _, writeErr := fmt.Fprintln(errOut); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return fmt.Errorf("reading managed template removal confirmation: %w", err)
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		return errors.New("managed template update cancelled")
	}
	return nil
}

func executeManagedTemplatePlans(
	cmd *cobra.Command,
	plans []*internal.ManagedTemplatePlan,
) (err error) {
	return executeManagedTemplatePlansWithReservations(cmd, plans, nil, nil)
}

func executeManagedTemplatePlansWithReservations(
	cmd *cobra.Command,
	plans []*internal.ManagedTemplatePlan,
	lifecycle map[string]*internal.ManagedFeatureLifecycleLock,
	modeReservations map[string]*managedFeatureRenameReservation,
) (err error) {
	if len(plans) == 0 {
		return nil
	}
	sort.Slice(plans, func(i, j int) bool {
		return plans[i].Request.Feature < plans[j].Request.Feature
	})
	held, err := acquireManagedTemplateReservations(plans, lifecycle, modeReservations)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, releaseManagedTemplateReservations(held))
	}()
	for i, plan := range plans {
		if err := recheckManagedTemplateSessions(plan); err != nil {
			return err
		}
		replanned, err := internal.ReplanManagedTemplate(plan)
		if err != nil {
			return fmt.Errorf("%s: managed template plan changed after reservation: %w", plan.Request.Feature, err)
		}
		if err := internal.ValidateManagedTemplateReplan(
			plan, replanned, held.template[i],
		); err != nil {
			return fmt.Errorf("%s: %w", plan.Request.Feature, err)
		}
		plans[i] = replanned
	}
	for i, plan := range plans {
		if cmd != nil {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: applying managed template roles\n", plan.Request.Feature); err != nil {
				return managedTemplateBatchProgressError(plans, i, err)
			}
		}
		if err := internal.ApplyManagedTemplatePlan(plan); err != nil {
			return managedTemplateBatchProgressError(plans, i, err)
		}
	}
	return nil
}

func managedTemplateBatchProgressError(plans []*internal.ManagedTemplatePlan, index int, cause error) error {
	if len(plans) == 1 {
		return fmt.Errorf("%s: %w", plans[index].Request.Feature, cause)
	}
	var completed, notStarted []string
	for i, plan := range plans {
		if i < index {
			completed = append(completed, plan.Request.Feature)
		} else if i > index {
			notStarted = append(notStarted, plan.Request.Feature)
		}
	}
	return fmt.Errorf("%s: %w (managed batch: completed features=%q; not started=%q)",
		plans[index].Request.Feature, cause, completed, notStarted)
}

func acquireManagedTemplateReservations(
	plans []*internal.ManagedTemplatePlan,
	lifecycle map[string]*internal.ManagedFeatureLifecycleLock,
	modeReservations map[string]*managedFeatureRenameReservation,
) (*heldManagedTemplateLocks, error) {
	held := &heldManagedTemplateLocks{}
	releaseOnError := func(cause error) (*heldManagedTemplateLocks, error) {
		return nil, errors.Join(cause, releaseManagedTemplateReservations(held))
	}
	for _, plan := range plans {
		var lock *internal.ManagedTemplateLock
		var err error
		if heldLifecycle := lifecycle[plan.Request.FeaturePath]; heldLifecycle != nil {
			lock, err = internal.AcquireManagedTemplateLockWithLifecycle(
				plan.Request.FeaturePath, heldLifecycle,
			)
		} else {
			lock, err = internal.AcquireManagedTemplateLock(plan.Request.FeaturePath)
		}
		if err != nil {
			return releaseOnError(fmt.Errorf("%s: %w", plan.Request.Feature, err))
		}
		held.template = append(held.template, lock)
	}
	ws := plans[0].Request.Workspace
	if ws.Mode == internal.ModeCheckout {
		if modeReservations[plans[0].Request.FeaturePath] != nil {
			return held, nil
		}
		token := held.template[0].Token()
		statePath := filepath.Join(plans[0].Request.FeaturePath, ".tws-template-operation")
		if _, err := internal.AcquireCheckoutMutationLock(
			ws.CheckoutStateDir(), token, plans[0].Request.Feature, "sync", statePath,
		); err != nil {
			return releaseOnError(fmt.Errorf("claim checkout mutation reservation for managed templates: %w", err))
		}
		held.checkoutStateDir = ws.CheckoutStateDir()
		held.checkoutToken = token
		return held, nil
	}
	for i, plan := range plans {
		if modeReservations[plan.Request.FeaturePath] != nil {
			continue
		}
		token := held.template[i].Token()
		if err := internal.ClaimSyncRunGuard(plan.Request.FeaturePath, token); err != nil {
			return releaseOnError(fmt.Errorf("%s: claim feature mutation reservation: %w", plan.Request.Feature, err))
		}
		held.external = append(held.external, struct {
			path  string
			token string
		}{path: plan.Request.FeaturePath, token: token})
		if err := recheckManagedTemplateSyncEvidence(plan, token); err != nil {
			return releaseOnError(err)
		}
		loc := internal.ReparentLocation{
			Mode: internal.ModeExternal, Feature: plan.Request.Feature, FeaturePath: plan.Request.FeaturePath,
		}
		if internal.HasReparentState(loc) {
			return releaseOnError(fmt.Errorf("%s: stack reparent recovery is active; recover it before changing managed templates", plan.Request.Feature))
		}
	}
	return held, nil
}

func recheckManagedTemplateSyncEvidence(plan *internal.ManagedTemplatePlan, token string) error {
	state := internal.ClassifyExternalSyncState(plan.Request.FeaturePath, internal.SyncClassifyOpts{
		AlwaysReadGuard: true,
	})
	switch {
	case state.GuardSymlink:
		return fmt.Errorf("%s: sync mutation guard became a symbolic link", plan.Request.Feature)
	case state.GuardErr != nil:
		return fmt.Errorf("%s: sync mutation guard is unreadable after claim: %w", plan.Request.Feature, state.GuardErr)
	case state.Guard == nil:
		return fmt.Errorf("%s: sync mutation guard disappeared after claim", plan.Request.Feature)
	case state.Guard.Token != token || state.Guard.PID != os.Getpid():
		return fmt.Errorf("%s: sync mutation guard ownership changed after claim", plan.Request.Feature)
	}
	if state.Cell == 1 {
		return nil
	}
	switch {
	case state.PayloadErr != nil:
		return fmt.Errorf("%s: scoped sync recovery evidence at %s is unreadable or unsupported: %w; recover it before changing managed templates",
			plan.Request.Feature, state.PayloadPath, state.PayloadErr)
	case state.Payload != nil:
		return fmt.Errorf("%s: scoped sync recovery evidence exists at %s; use sync recovery before changing managed templates",
			plan.Request.Feature, state.PayloadPath)
	case state.LegacyErr != nil:
		return fmt.Errorf("%s: legacy sync recovery evidence at %s is unreadable or corrupt: %w; recover it before changing managed templates",
			plan.Request.Feature, state.LegacyPath, state.LegacyErr)
	default:
		return fmt.Errorf("%s: legacy sync recovery evidence exists at %s; use sync recovery before changing managed templates",
			plan.Request.Feature, state.LegacyPath)
	}
}

func releaseManagedTemplateReservations(held *heldManagedTemplateLocks) error {
	if held == nil {
		return nil
	}
	var errs []error
	for i := len(held.external) - 1; i >= 0; i-- {
		errs = append(errs, internal.ReleaseOwnedSyncRunGuard(held.external[i].path, held.external[i].token))
	}
	if held.checkoutStateDir != "" {
		errs = append(errs, internal.ReleaseCheckoutMutationLock(held.checkoutStateDir, held.checkoutToken))
	}
	for i := len(held.template) - 1; i >= 0; i-- {
		errs = append(errs, held.template[i].Release())
	}
	return errors.Join(errs...)
}

func acquireManagedFeatureRenameReservation(
	ws internal.Workspace,
	feature, featurePath string,
	managed bool,
) (*managedFeatureRenameReservation, error) {
	if residue, err := internal.ManagedTemplateRecoveryResidue(featurePath); err != nil {
		return nil, err
	} else if residue != "" {
		return nil, fmt.Errorf("managed template recovery evidence exists at %s; resolve it before renaming the feature", residue)
	}
	reservation := &managedFeatureRenameReservation{
		workspace: ws, feature: feature, featurePath: featurePath,
	}
	fail := func(cause error) (*managedFeatureRenameReservation, error) {
		return nil, errors.Join(cause, reservation.Release())
	}
	plan := &internal.ManagedTemplatePlan{
		Request: internal.ManagedTemplatePlanRequest{
			Workspace: ws, Feature: feature, FeaturePath: featurePath,
		},
	}
	if ws.Mode == internal.ModeCheckout {
		token, err := newSyncOwnerToken()
		if err != nil {
			return fail(err)
		}
		statePath := filepath.Join(featurePath, ".tws-template-rename")
		if _, err := internal.AcquireCheckoutMutationLock(
			ws.CheckoutStateDir(), token, feature, "sync", statePath,
		); err != nil {
			return fail(fmt.Errorf("claim checkout mutation reservation for managed feature rename: %w", err))
		}
		reservation.checkoutStateDir = ws.CheckoutStateDir()
		reservation.checkoutToken = token
		loc := internal.ReparentLocationFor(ws, feature, featurePath)
		if internal.HasReparentState(loc) {
			return fail(fmt.Errorf("%s: stack reparent recovery is active; recover it before changing feature lifecycle", feature))
		}
		if managed {
			if err := recheckManagedTemplateSessions(plan); err != nil {
				return fail(err)
			}
		}
		return reservation, nil
	}
	token, err := newSyncOwnerToken()
	if err != nil {
		return fail(err)
	}
	if err := internal.ClaimSyncRunGuard(featurePath, token); err != nil {
		return fail(fmt.Errorf("claim feature mutation reservation for managed feature rename: %w", err))
	}
	reservation.externalToken = token
	if err := recheckManagedTemplateSyncEvidence(plan, token); err != nil {
		return fail(err)
	}
	loc := internal.ReparentLocation{
		Mode: internal.ModeExternal, Feature: feature, FeaturePath: featurePath,
	}
	if internal.HasReparentState(loc) {
		return fail(fmt.Errorf("%s: stack reparent recovery is active; recover it before renaming the managed feature", feature))
	}
	if managed {
		if err := recheckManagedTemplateSessions(plan); err != nil {
			return fail(err)
		}
	}
	return reservation, nil
}

func (reservation *managedFeatureRenameReservation) Moved(feature, featurePath string) {
	if reservation == nil {
		return
	}
	reservation.feature = feature
	reservation.featurePath = featurePath
}

func (reservation *managedFeatureRenameReservation) Release() error {
	if reservation == nil {
		return nil
	}
	var errs []error
	if reservation.externalToken != "" {
		errs = append(errs, internal.ReleaseOwnedSyncRunGuard(
			reservation.featurePath, reservation.externalToken,
		))
		reservation.externalToken = ""
	}
	if reservation.checkoutStateDir != "" {
		errs = append(errs, internal.ReleaseCheckoutMutationLock(
			reservation.checkoutStateDir, reservation.checkoutToken,
		))
		reservation.checkoutStateDir = ""
	}
	return errors.Join(errs...)
}

func recheckManagedTemplateSessions(plan *internal.ManagedTemplatePlan) error {
	ws := plan.Request.Workspace
	if ws.Mode == internal.ModeCheckout {
		if present, pid, err := internal.CheckoutSessionIntent(ws); err != nil {
			return fmt.Errorf("checkout session launch intent is unverifiable: %w", err)
		} else if present {
			return fmt.Errorf("checkout session launch is active (pid %d); close it before changing managed templates", pid)
		}
		present, err := internal.CheckoutAgentSessionPresence(ws)
		if err != nil {
			return fmt.Errorf("checkout session state is unverifiable: %w", err)
		}
		if present {
			return errors.New("a checkout agent session is active; close it before changing managed templates")
		}
		return nil
	}
	affected := append([]string(nil), plan.AffectedWorktrees...)
	if len(affected) == 0 {
		stack, err := internal.LoadStack(plan.Request.FeaturePath)
		if err == nil {
			for _, entry := range stack.Branches {
				if !entry.Archived {
					affected = append(affected, entry.Name)
				}
			}
		}
	}
	if len(affected) == 0 {
		affected = []string{"<feature>"}
	}
	blocking, err := reparentExternalLiveSessions(
		plan.Request.FeaturePath, plan.Request.Feature, affected, managedTemplateTmuxProbe,
	)
	if err != nil {
		return fmt.Errorf("external session state is unverifiable: %w", err)
	}
	if len(blocking) > 0 {
		return fmt.Errorf("active external session(s) block managed template changes: %s", strings.Join(blocking, ", "))
	}
	return nil
}

func planManagedTemplateForFeature(
	ws internal.Workspace,
	feature string,
	roles []internal.ManagedTemplateRole,
	flags managedTemplateFlags,
) (*internal.ManagedTemplatePlan, error) {
	featurePath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return nil, err
	}
	request := managedPlanRequest(ws, feature, featurePath, roles, flags)
	return internal.PlanManagedTemplate(request)
}

type managedInjectDestination struct {
	path string
}

func verifiedManagedInjectNoop(
	plan *internal.ManagedTemplatePlan,
	destinations []managedInjectDestination,
) (bool, error) {
	if plan.HasEffects() {
		return false, nil
	}
	excluded, err := internal.ManagedTemplateOwnedWorktreePaths(plan.Request.FeaturePath)
	if err != nil {
		return false, err
	}
	wouldChange := func() (bool, error) {
		for _, destination := range destinations {
			change, err := internal.InjectFilesExcludingWouldChange(
				plan.Request.FeaturePath, destination.path, "", excluded,
			)
			if err != nil {
				return false, err
			}
			if change {
				return true, nil
			}
		}
		return false, nil
	}
	if change, err := wouldChange(); err != nil || change {
		return false, err
	}
	replanned, err := internal.ReplanManagedTemplate(plan)
	if err != nil {
		return false, err
	}
	if replanned.Fingerprint != plan.Fingerprint || replanned.HasEffects() {
		return false, errors.New("managed inject plan drifted while verifying a no-op")
	}
	if change, err := wouldChange(); err != nil || change {
		return false, err
	}
	if plan.Request.Workspace.Mode == internal.ModeExternal {
		if err := internal.RefuseSyncIfReparentActive(internal.ReparentLocation{
			Mode:        internal.ModeExternal,
			Feature:     plan.Request.Feature,
			FeaturePath: plan.Request.FeaturePath,
		}); err != nil {
			return false, err
		}
	}
	return true, nil
}

func syncManagedAndLegacyInject(
	cmd *cobra.Command,
	ws internal.Workspace,
	feature, featurePath, worktreeName, worktreePath string,
	target managedInjectTarget,
) error {
	return syncManagedAndLegacyInjectWithLifecycle(
		cmd, ws, feature, featurePath, worktreeName, worktreePath, target, nil, nil,
	)
}

type preparedManagedWorktreeRetirement struct {
	plan *internal.ManagedTemplatePlan
}

func prepareManagedWorktreeLinkRetirement(
	ws internal.Workspace,
	feature, featurePath, worktree string,
) (*preparedManagedWorktreeRetirement, error) {
	plan, err := internal.PlanManagedTemplate(internal.ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:          []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
		RetireWorktree: worktree,
	})
	if err != nil {
		return nil, err
	}
	if !internal.ManagedTemplatePlanRetiresWorktreeLinks(plan) {
		return nil, nil
	}
	return &preparedManagedWorktreeRetirement{plan: plan}, nil
}

func (retirement *preparedManagedWorktreeRetirement) publishAfterRemoval() error {
	if retirement == nil {
		return nil
	}
	return internal.ApplyManagedTemplateRetirementAfterRemoval(retirement.plan)
}

func (retirement *preparedManagedWorktreeRetirement) restoreAfterRollback() error {
	if retirement == nil {
		return nil
	}
	return internal.RestoreManagedTemplateRetirementAfterRollback(retirement.plan)
}

func syncManagedAndLegacyInjectWithLifecycle(
	cmd *cobra.Command,
	ws internal.Workspace,
	feature, featurePath, worktreeName, worktreePath string,
	target managedInjectTarget,
	lifecycle *internal.ManagedFeatureLifecycleLock,
	modeReservation *managedFeatureRenameReservation,
) error {
	present, err := internal.ManagedTemplateStatePresent(featurePath)
	if err != nil {
		return err
	}
	if !present {
		return internal.InjectFiles(featurePath, worktreePath, target.Value)
	}
	state, err := internal.LoadManagedTemplateState(featurePath)
	if err != nil {
		return err
	}
	if _, ok := state.Roles[string(internal.ManagedTemplateRoleWorktree)]; !ok {
		return internal.InjectFiles(featurePath, worktreePath, target.Value)
	}
	request := internal.ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:               []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
		LinkMaterializeOnly: true, OnlyWorktree: worktreeName,
		InjectInto: target.Value, InjectIntoSet: target.Explicit,
	}
	plan, err := internal.PlanManagedTemplate(request)
	if err != nil {
		return err
	}
	if noop, err := verifiedManagedInjectNoop(plan, []managedInjectDestination{{path: worktreePath}}); err != nil {
		return err
	} else if noop {
		return nil
	}
	lifecycleByPath := map[string]*internal.ManagedFeatureLifecycleLock(nil)
	if lifecycle != nil {
		lifecycleByPath = map[string]*internal.ManagedFeatureLifecycleLock{
			featurePath: lifecycle,
		}
	}
	modeByPath := map[string]*managedFeatureRenameReservation(nil)
	if modeReservation != nil {
		modeByPath = map[string]*managedFeatureRenameReservation{
			featurePath: modeReservation,
		}
	}
	if err := executeManagedTemplatePlansWithReservations(
		cmd, []*internal.ManagedTemplatePlan{plan}, lifecycleByPath, modeByPath,
	); err != nil {
		return err
	}
	excluded, err := internal.ManagedTemplateOwnedWorktreePaths(featurePath)
	if err != nil {
		return err
	}
	return internal.InjectFilesExcluding(featurePath, worktreePath, "", excluded)
}

func syncManagedAndLegacyInjectAll(
	cmd *cobra.Command,
	ws internal.Workspace,
	feature, featurePath string,
	target managedInjectTarget,
) (int, error) {
	present, err := internal.ManagedTemplateStatePresent(featurePath)
	if err != nil {
		return 0, err
	}
	if !present {
		return internal.InjectFilesForFeature(featurePath, target.Value)
	}
	state, err := internal.LoadManagedTemplateState(featurePath)
	if err != nil {
		return 0, err
	}
	if _, ok := state.Roles[string(internal.ManagedTemplateRoleWorktree)]; !ok {
		return internal.InjectFilesForFeature(featurePath, target.Value)
	}
	request := internal.ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:               []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
		LinkMaterializeOnly: true,
		InjectInto:          target.Value, InjectIntoSet: target.Explicit,
	}
	plan, err := internal.PlanManagedTemplate(request)
	if err != nil {
		return 0, err
	}
	initialTargets, err := internal.DiscoverInjectWorktrees(featurePath)
	if err != nil {
		return 0, err
	}
	destinations := make([]managedInjectDestination, 0, len(initialTargets))
	for _, target := range initialTargets {
		destinations = append(destinations, managedInjectDestination{path: target.Path})
	}
	if noop, err := verifiedManagedInjectNoop(plan, destinations); err != nil {
		return 0, err
	} else if noop {
		return len(initialTargets), nil
	}
	if err := executeManagedTemplatePlans(cmd, []*internal.ManagedTemplatePlan{plan}); err != nil {
		return 0, err
	}
	excluded, err := internal.ManagedTemplateOwnedWorktreePaths(featurePath)
	if err != nil {
		return 0, err
	}
	targets, err := internal.DiscoverInjectWorktrees(featurePath)
	if err != nil {
		return 0, err
	}
	var injectErrs []error
	for _, worktree := range targets {
		if err := internal.InjectFilesExcluding(featurePath, worktree.Path, "", excluded); err != nil {
			injectErrs = append(injectErrs, fmt.Errorf("%s: %w", worktree.Name, err))
		}
	}
	return len(targets), errors.Join(injectErrs...)
}

func featureExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("feature path %s is not a regular directory", path)
	}
	return true, nil
}
