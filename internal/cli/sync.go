package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func syncCmd() *cobra.Command {
	var verbose bool
	var push bool
	var cont bool
	var abort bool
	var testCmd string
	var doFetch bool
	var noFetch bool
	var full bool
	var localOnly bool
	var only string
	var from string

	cmd := &cobra.Command{
		Use:   "sync <feature>",
		Short: "Rebase branches in dependency order",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			internal.RequireTool("git")

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			// Pure command-line checks (I1-I8) run before mode dispatch so both
			// modes reject identically and identically early.
			policy, newMode, changed, err := resolveSyncPolicy(cmd, ws.Mode)
			if err != nil {
				return err
			}

			if ws.Mode == internal.ModeCheckout {
				return runCheckoutSync(ws, internal.CheckoutSyncOpts{
					Feature:     args[0],
					Push:        push,
					TestCommand: testCmd,
					Verbose:     verbose,
					Policy:      policy,
					NewMode:     newMode,
					Continue:    cont,
					Abort:       abort,
					Changed:     changed,
				})
			}

			feature := args[0]
			// One guard covers the plain, --abort, and --continue paths.
			// syncFeature carries none: it has no error channel and would
			// degrade the message to "sync incomplete".
			twsRoot := internal.TwsRoot()
			if err := internal.GuardFeatureName(twsRoot, feature); err != nil {
				return err
			}
			layout, layoutErr := resolveExternalSyncLayout(ws, twsRoot, feature)
			if layoutErr != nil {
				return layoutErr
			}

			state, stateErr := classifySyncState(layout.FeaturePath, newMode)
			if stateErr != nil {
				return stateErr
			}
			// Deferred I7: cont && abort survived the command-line block, so no
			// trigger flag was supplied. Refuse only against new-mode state.
			if cont && abort && (state.Marker != "" || state.Payload != nil) {
				return errSyncContinueAbort()
			}

			if abort {
				if err := syncCellRefusal(syncVerbAbort, feature, layout, state); err != nil {
					return err
				}
				return handleSyncAbortCell(feature, layout, state)
			}
			if cont {
				if err := syncCellRefusal(syncVerbContinue, feature, layout, state); err != nil {
					return err
				}
				if newMode && (state.Cell == 1 || state.Cell == 7) {
					return fmt.Errorf("%s", errSyncModeFlagsNeedV2)
				}
				if state.Cell == 5 {
					return handleScopedSyncContinue(feature, layout, ws, push, policy, changed, state)
				}
				return handleSyncContinue(feature, layout, push)
			}
			if err := syncCellRefusal(syncVerbPlain, feature, layout, state); err != nil {
				return err
			}
			if internal.HasSyncState(layout.FeaturePath) {
				legacy, _ := internal.LoadSyncState(layout.FeaturePath)
				return fmt.Errorf("previous sync incomplete (failed on: %s); use --continue or --abort", legacy.FailedBranch)
			}

			if newMode {
				return runScopedSync(feature, layout, ws, policy, push, verbose)
			}

			result := syncFeature(feature, layout, verbose)
			if !result.Complete {
				return fmt.Errorf("sync incomplete")
			}
			fmt.Println("Sync complete.")
			if push {
				fmt.Println("\nPushing...")
				if err := pushFeature(feature, layout, false); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show full git fetch output")
	cmd.Flags().BoolVar(&push, "push", false, "Push all branches after syncing")
	cmd.Flags().BoolVar(&cont, "continue", false, "Resume after conflict resolution")
	cmd.Flags().BoolVar(&abort, "abort", false, "Discard sync state and start fresh")
	cmd.Flags().StringVar(&testCmd, "test", "", "Validation command to run after each rebase (checkout mode)")
	cmd.Flags().BoolVar(&doFetch, "fetch", false, "Fetch before planning (external default)")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Plan and rebase from local refs only; no automatic network input (checkout default)")
	cmd.Flags().BoolVar(&full, "full", false, "Advance anchors onto their configured base (default)")
	cmd.Flags().BoolVar(&localOnly, "local-only", false, "Replay local parent tips into children; never advance an anchor")
	cmd.Flags().StringVar(&only, "only", "", "Sync exactly one stack entry by its logical name")
	cmd.Flags().StringVar(&from, "from", "", "Sync one stack entry and its descendant closure, by logical name")

	_ = cmd.RegisterFlagCompletionFunc("only", syncEntryCompletion)
	_ = cmd.RegisterFlagCompletionFunc("from", syncEntryCompletion)

	return cmd
}

// runScopedSync executes §3.6 steps 10-17 of a new-mode external run.
func runScopedSync(feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy, push, verbose bool) error {
	stack, err := internal.LoadStack(layout.FeaturePath)
	if err != nil {
		return fmt.Errorf("sync modes require a stack; feature %q has no readable stack.yaml", feature)
	}
	sel, err := internal.ResolveSyncSelection(stack, policy, internal.SyncSelectionOpts{
		Mode:    ws.Mode,
		NewMode: true,
		Feature: feature,
	})
	if err != nil {
		return err
	}
	if err := verifySelectedBasesLocally(layout, stack, sel); err != nil {
		return err
	}

	marker, err := syncMarkerFn()
	if err != nil {
		return err
	}
	if err := syncMarkerCollision(stack, marker); err != nil {
		return err
	}
	token, err := newSyncOwnerToken()
	if err != nil {
		return err
	}

	testCommand := internal.LoadConfig().TestCommand
	validationSource := "none"
	if testCommand != "" {
		validationSource = "config"
	}

	payload, err := setupSyncRunState(layout, feature, marker, token, sel, push, testCommand, validationSource)
	if err != nil {
		return err
	}

	printSyncModeHeader(policy)

	run := &syncRunContext{Policy: policy, Sel: sel, Payload: payload}
	result := syncFeatureScoped(feature, layout, verbose, stack, run)
	if !result.Complete {
		return fmt.Errorf("sync incomplete")
	}
	fmt.Println("Sync complete.")
	if push {
		// The payload, the sentinel, and the guard stay on disk when a scoped
		// push fails, so --continue can retry exactly the entries that were
		// never pushed.
		if err := runNewModePush(feature, layout, stack, sel, result.Completed, payload); err != nil {
			return err
		}
	}
	return finalizeScopedSyncRun(layout, payload)
}

// runNewModePush is the §7.6 push half of a new-mode run. A `scope=all` run
// keeps the legacy whole-feature push, so "`--push` pushes the feature" is
// unchanged when nothing was scoped: every materialized entry is considered —
// including an anchor a local-only run deliberately did not rebase — and the
// legacy lenient per-entry behaviour is preserved. `only` and `subtree` scopes
// use the strict, payload-aware, resumable push.
func runNewModePush(feature string, layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection, completed []string, payload *internal.SyncRunState) error {
	fmt.Println("\nPushing...")
	payload.Stage = internal.SyncStagePushing
	if err := internal.SaveSyncRunState(layout.FeaturePath, payload); err != nil {
		return fmt.Errorf("record push stage: %w", err)
	}
	if sel.Policy.ScopeKind == internal.SyncScopeAll {
		return pushFeature(feature, layout, false)
	}
	return pushScoped(feature, layout, stack, sel, completed, payload)
}

// finalizeScopedSyncRun records the finalizing stage immediately before
// teardown — never earlier — and then tears the run's state down. A teardown
// error is propagated verbatim: no later artifact is cleared behind it.
func finalizeScopedSyncRun(layout externalSyncLayout, payload *internal.SyncRunState) error {
	payload.Stage = internal.SyncStageFinalizing
	if err := internal.SaveSyncRunState(layout.FeaturePath, payload); err != nil {
		return fmt.Errorf("record finalizing stage: %w", err)
	}
	return clearSyncRunState(layout.FeaturePath, true)
}

// verifySelectedBasesLocally is the I14 no-fetch preflight. It probes only the
// base refs the selected plan will actually use.
func verifySelectedBasesLocally(layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection) error {
	if sel.Policy.Fetch != internal.SyncFetchDisabled {
		return nil
	}
	for _, entry := range sel.Entries {
		if entry.Role == internal.SyncRoleAnchor && sel.Policy.Propagation == internal.SyncPropagationLocalOnly {
			continue
		}
		real := internal.GetBranch(stack, entry.Name)
		repoCtx := syncRepoContext(layout, real)
		ref := resolveEntryBase(stack, real, repoCtx)
		if ref == "" {
			continue
		}
		if err := internal.VerifyGitRef(repoCtx, ref); err != nil {
			return fmt.Errorf("base %q for stack entry %q does not resolve locally; drop --no-fetch or fetch manually first", ref, entry.Name)
		}
	}
	return nil
}

func handleSyncAbortCell(feature string, layout externalSyncLayout, state internal.SyncExternalState) error {
	if state.GuardForeign() {
		return fmt.Errorf("sync guard %s does not belong to the recorded scoped run; inspect it and remove it manually", state.GuardPath)
	}
	switch state.Cell {
	case 2, 5:
		if state.Payload != nil && state.Payload.FailedBranch != "" {
			path := layout.WorktreePath(state.Payload.FailedBranch)
			if isRebaseInProgress(path) {
				_ = internal.RunSilentDir(path, "git", "rebase", "--abort")
			}
		}
		internal.DeleteSyncRunState(layout.FeaturePath)
		if state.Cell == 5 {
			internal.DeleteSyncState(layout.FeaturePath)
		}
		internal.ReleaseSyncRunGuard(layout.FeaturePath)
		fmt.Println("Sync state cleared.")
		return nil
	case 4:
		if internal.HasSyncRunState(layout.FeaturePath) {
			return fmt.Errorf("scoped sync state appeared at %s while aborting; re-run: tws sync %s --abort",
				internal.SyncRunStatePath(layout.FeaturePath), feature)
		}
		internal.DeleteSyncState(layout.FeaturePath)
		internal.ReleaseSyncRunGuard(layout.FeaturePath)
		fmt.Println("Sync state cleared.")
		return nil
	}
	return handleSyncAbort(feature, layout)
}

func handleSyncAbort(feature string, layout externalSyncLayout) error {
	state, err := internal.LoadSyncState(layout.FeaturePath)
	if err != nil {
		fmt.Println("Nothing to abort — no sync in progress.")
		return nil
	}
	if state.FailedBranch != "" {
		path := layout.WorktreePath(state.FailedBranch)
		if isRebaseInProgress(path) {
			_ = internal.RunSilentDir(path, "git", "rebase", "--abort")
		}
	}
	internal.DeleteSyncState(layout.FeaturePath)
	fmt.Println("Sync state cleared.")
	return nil
}

func handleSyncContinue(feature string, layout externalSyncLayout, push bool) error {
	state, err := internal.LoadSyncState(layout.FeaturePath)
	if err != nil {
		return fmt.Errorf("nothing to continue — no sync in progress")
	}
	failedPath := layout.WorktreePath(state.FailedBranch)
	if state.FailedBranch != "" && isRebaseInProgress(failedPath) {
		return fmt.Errorf("rebase still in progress in %s; resolve conflicts, run git add . && git rebase --continue, then retry", state.FailedBranch)
	}

	stack, err := internal.LoadStack(layout.FeaturePath)
	if err != nil {
		return fmt.Errorf("load stack: %w", err)
	}
	if state.FailedBranch != "" {
		failedEntry := internal.GetBranch(stack, state.FailedBranch)
		if failedEntry.Name == "" {
			return fmt.Errorf("failed branch %q no longer exists in stack", state.FailedBranch)
		}
		if !branchContainsConfiguredParent(layout.WorktreesRoot, stack, failedEntry) {
			return fmt.Errorf("resolved branch %s still does not contain its configured parent %s", failedEntry.Name, failedEntry.Base)
		}
		fmt.Println(formatSyncStatus(state.FailedBranch, "active", "resolved"))
	}

	done := make(map[string]bool)
	for _, name := range state.Completed {
		done[name] = true
	}
	if state.FailedBranch != "" {
		done[state.FailedBranch] = true
	}
	sorted, err := internal.TopoSort(stack)
	if err != nil {
		return err
	}
	fmt.Printf("Resuming sync with %d pending branch(es)\n", len(state.Pending))
	result := syncWithStackFiltered(feature, layout, stack, sorted, done)
	if !result.Complete {
		return fmt.Errorf("sync incomplete")
	}
	fmt.Println("Sync complete.")
	if push {
		fmt.Println("\nPushing...")
		if err := pushFeature(feature, layout, false); err != nil {
			return err
		}
	}
	return nil
}

// handleScopedSyncContinue resumes cell 5 — the only resumable new-mode cell.
func handleScopedSyncContinue(feature string, layout externalSyncLayout, ws internal.Workspace, push bool, policy internal.SyncRunPolicy, changed map[string]bool, state internal.SyncExternalState) error {
	payload := state.Payload
	if payload == nil {
		return fmt.Errorf("nothing to continue — no sync in progress")
	}
	if err := syncContinueMismatches(payload, policy, changed, push); err != nil {
		return err
	}

	failedPath := layout.WorktreePath(payload.FailedBranch)
	if payload.FailedBranch != "" && isRebaseInProgress(failedPath) {
		return fmt.Errorf("rebase still in progress in %s; resolve conflicts, run git add . && git rebase --continue, then retry", payload.FailedBranch)
	}

	stack, err := internal.LoadStack(layout.FeaturePath)
	if err != nil {
		return fmt.Errorf("load stack: %w", err)
	}
	sel, err := scopedSelectionFromPayload(stack, payload, feature, ws.Mode)
	if err != nil {
		return err
	}

	if payload.FailedBranch != "" && !payloadCompleted(payload, payload.FailedBranch) {
		failedEntry := internal.GetBranch(stack, payload.FailedBranch)
		if failedEntry.Name == "" {
			return fmt.Errorf("failed branch %q no longer exists in stack", payload.FailedBranch)
		}
		if !branchContainsConfiguredParent(layout.WorktreesRoot, stack, failedEntry) {
			return fmt.Errorf("resolved branch %s still does not contain its configured parent %s", failedEntry.Name, failedEntry.Base)
		}
		fmt.Println(formatSyncStatus(payload.FailedBranch, "active", "resolved"))
	}

	if state.GuardForeign() {
		return fmt.Errorf("sync guard %s does not belong to the recorded scoped run; inspect it and remove it manually", state.GuardPath)
	}
	if err := internal.ReclaimSyncRunGuard(layout.FeaturePath, payload.OwnerToken); err != nil {
		return err
	}

	done := make(map[string]bool)
	for _, name := range payload.Completed {
		done[name] = true
	}
	if payload.FailedBranch != "" {
		done[payload.FailedBranch] = true
	}
	sorted, err := internal.TopoSort(stack)
	if err != nil {
		return err
	}

	printSyncModeHeader(payload.Policy())
	fmt.Printf("Resuming sync with %d pending branch(es)\n", len(payload.Pending))

	run := &syncRunContext{Policy: payload.Policy(), Sel: sel, Payload: payload}
	result := syncWithStackScoped(feature, layout, stack, sorted, done, run)
	if !result.Complete {
		return fmt.Errorf("sync incomplete")
	}
	fmt.Println("Sync complete.")
	if payload.Push {
		if err := runNewModePush(feature, layout, stack, sel, result.Completed, payload); err != nil {
			return err
		}
	}
	return finalizeScopedSyncRun(layout, payload)
}

// syncContinueMismatches applies §10.5 rules 2, 3, and 5 to an external v2
// resume.
func syncContinueMismatches(payload *internal.SyncRunState, policy internal.SyncRunPolicy, changed map[string]bool, push bool) error {
	if changed["fetch"] || changed["no-fetch"] {
		if policy.Fetch != payload.FetchPolicy {
			return syncContinueMismatch("fetch", string(payload.FetchPolicy), string(policy.Fetch))
		}
	}
	if changed["full"] || changed["local-only"] {
		if policy.Propagation != payload.PropagationPolicy {
			return syncContinueMismatch("propagation", string(payload.PropagationPolicy), string(policy.Propagation))
		}
	}
	if changed["only"] || changed["from"] {
		want := payload.Policy().ScopeLabel()
		if policy.ScopeLabel() != want {
			return syncContinueMismatch("scope", want, policy.ScopeLabel())
		}
	}
	if changed["push"] && push != payload.Push {
		return syncContinueMismatch("push", fmt.Sprintf("%v", payload.Push), fmt.Sprintf("%v", push))
	}
	return nil
}

func syncContinueMismatch(axis, started, requested string) error {
	return fmt.Errorf("cannot change %s on --continue: the run was started with %s=%s and this invocation requests %s", axis, axis, started, requested)
}

// payloadCompleted reports whether an entry already finished its rebase in the
// interrupted run. A push failure records the entry in BOTH `completed` and
// `failed_branch`: its rebase and its validation already succeeded, so the
// resume must neither re-check the parent edge, nor re-run validation, nor
// print `[~] resolved` for it — it goes straight to retrying the entries that
// were never pushed.
func payloadCompleted(payload *internal.SyncRunState, name string) bool {
	for _, done := range payload.Completed {
		if done == name {
			return true
		}
	}
	return false
}

// scopedSelectionFromPayload rebuilds the run's selection from the payload's
// materialized `selected` list, so a stack.yaml edit between failure and
// --continue cannot silently re-scope the run.
func scopedSelectionFromPayload(stack internal.Stack, payload *internal.SyncRunState, feature string, mode internal.WorkspaceMode) (internal.SyncSelection, error) {
	for _, name := range payload.Selected {
		if !internal.HasBranch(stack, name) {
			return internal.SyncSelection{}, fmt.Errorf("selected stack entry %q no longer exists in stack; use --abort", name)
		}
	}
	sel, err := internal.ResolveSyncSelection(stack, payload.Policy(), internal.SyncSelectionOpts{
		Mode:    mode,
		NewMode: true,
		Feature: feature,
	})
	if err != nil {
		return internal.SyncSelection{}, err
	}
	keep := make(map[string]bool, len(payload.Selected))
	for _, name := range payload.Selected {
		keep[name] = true
	}
	filtered := internal.SyncSelection{Policy: sel.Policy, Names: make(map[string]bool, len(keep))}
	repoSet := make(map[string]bool)
	for _, entry := range sel.Entries {
		if !keep[entry.Name] {
			continue
		}
		filtered.Entries = append(filtered.Entries, entry)
		filtered.Names[entry.Name] = true
		repoSet[entry.Repo] = true
	}
	for repo := range repoSet {
		filtered.Repos = append(filtered.Repos, repo)
	}
	sort.Strings(filtered.Repos)
	return filtered, nil
}

func branchContainsConfiguredParent(worktreesRoot string, stack internal.Stack, child internal.StackEntry) bool {
	parent := internal.GetBranch(stack, child.Base)
	if parent.Name == "" || !sameStackRepo(parent.Repo, child.Repo) {
		return true
	}
	path := externalSyncLayout{WorktreesRoot: worktreesRoot}.WorktreePath(child.Name)
	return internal.RunSilentDir(path, "git", "merge-base", "--is-ancestor", parent.GitBranch(), child.GitBranch()) == nil
}

func isRebaseInProgress(worktreePath string) bool {
	// Check for .git/rebase-merge or .git/rebase-apply
	gitDir := internal.RunSilent("git", "-C", worktreePath, "rev-parse", "--git-dir")
	if gitDir != nil {
		return false
	}
	// Simpler: check if git status shows rebase
	err := internal.RunSilent("git", "-C", worktreePath, "rebase", "--show-current-patch")
	return err == nil
}

func syncFeature(feature string, layout externalSyncLayout, verbose bool) syncResult {
	stack, err := internal.LoadStack(layout.FeaturePath)
	if err != nil {
		fetchQuiet("", "", verbose)
		syncFallback(layout)
		return syncResult{Complete: true}
	}

	repos := internal.UniqueRepos(stack, layout.FeaturePath)
	for repo, wtPath := range repos {
		fetchQuiet(repo, wtPath, verbose)
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return syncResult{}
	}
	return syncWithStack(feature, layout, stack, sorted)
}

// syncFeatureScoped is syncFeature for a new-mode run: the stack is already
// loaded and validated, and the fetch loop is restricted to the repos the
// selection actually touches.
func syncFeatureScoped(feature string, layout externalSyncLayout, verbose bool, stack internal.Stack, run *syncRunContext) syncResult {
	if run.Policy.Fetch == internal.SyncFetchEnabled {
		run.Payload.Stage = internal.SyncStageFetching
		_ = internal.SaveSyncRunState(layout.FeaturePath, run.Payload)
		sub := internal.Stack{Branches: selectedRealEntries(stack, run.Sel)}
		for repo, wtPath := range internal.UniqueRepos(sub, layout.FeaturePath) {
			fetchQuiet(repo, wtPath, verbose)
		}
	}
	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return syncResult{}
	}
	return syncWithStackScoped(feature, layout, stack, sorted, nil, run)
}

// selectedRealEntries returns the real StackEntry values of the selection, in
// selection order. Executors never rebuild entries from SyncSelectedEntry:
// that would drop LastBaseSHA and destroy the amend-aware replay.
func selectedRealEntries(stack internal.Stack, sel internal.SyncSelection) []internal.StackEntry {
	out := make([]internal.StackEntry, 0, len(sel.Entries))
	for _, entry := range sel.Entries {
		real := internal.GetBranch(stack, entry.Name)
		if real.Name == "" {
			continue
		}
		out = append(out, real)
	}
	return out
}

// syncRepoContext is the §13.4 rule 3 repo context: the entry's Repo when set,
// otherwise its materialized worktree path, otherwise "".
func syncRepoContext(layout externalSyncLayout, entry internal.StackEntry) string {
	if entry.Repo != "" {
		return entry.Repo
	}
	path := layout.WorktreePath(entry.Name)
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

func fetchQuiet(repo, wtPath string, verbose bool) {
	label := "default repo"
	if repo != "" {
		label = repo
	}

	if verbose {
		fmt.Printf("Fetching %s...\n", label)
		if wtPath != "" {
			_ = internal.RunDir(wtPath, "git", "fetch")
		} else if repo != "" {
			_ = internal.RunDir(repo, "git", "fetch")
		} else {
			_ = internal.Run("git", "fetch")
		}
	} else {
		fmt.Printf("Fetching %s... ", label)
		var err error
		if wtPath != "" {
			err = internal.RunSilentDir(wtPath, "git", "fetch")
		} else if repo != "" {
			err = internal.RunSilentDir(repo, "git", "fetch")
		} else {
			err = internal.RunSilent("git", "fetch")
		}
		if err != nil {
			fmt.Println("failed")
		} else {
			fmt.Println("done")
		}
	}
}
