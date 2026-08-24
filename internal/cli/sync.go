package cli

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

// syncLongText is the exact, normative §3.1a command Long. Short stays
// "Rebase branches in dependency order", unchanged.
const syncLongText = `Rebase the feature's branches in dependency order.

--plan describes the rebase this invocation would perform and then exits. It moves
no branch, rewrites no working tree, and writes no tws runtime state. It is not
Git-write-free: a plan fetches exactly where the run it describes fetches — an
external plan fetches by default, a checkout plan only with --fetch — and may
update remote-tracking refs, FETCH_HEAD, tags and covered local branches. Add
--no-fetch for a fully local preview — that previews a different, new-mode route,
not the route a bare "tws sync <feature>" takes.

--max-replay-per-entry and --max-replay-total bound the work of this invocation
only — the whole plan on a fresh run, the remaining work on --continue — and are
never cumulative across resumes. --approve-plan takes the fingerprint printed by
--plan and requires at least one of those limits.

A guarded run — one carrying a replay limit or --approve-plan — records its limits
in state_version 3 recovery state, so an older tws release refuses to resume it
instead of resuming it without the guard. When the guard itself refuses, the run
exits 1 and writes one "plan-guard: <kind>: <detail>" line on stderr, before any
branch has moved unless that line says "state-preserved:". Refusals tws already
performs — a dirty tree, a held lock, a base that does not resolve locally, an
incomplete previous run — keep their own wording, exit 1 without that line, and
are never marked.`

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
	var plan bool
	var planJSON bool
	var maxPerEntry int
	var maxTotal int
	var approvePlan string

	cmd := &cobra.Command{
		Use:   "sync <feature>",
		Short: "Rebase branches in dependency order",
		Long:  syncLongText,
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			internal.RequireTool("git")

			// §3.3 step 3 / §3.6 row 4: the control-flag validation is a PURE
			// command-line check over cmd's own flags, so it runs ABOVE
			// subject resolution. An early control error therefore refuses
			// before any workspace probe — zero git child processes, zero
			// state read — which is exactly what §22 criteria 3-7 assert.
			guardOpts, err := resolvePlanGuardOptions(cmd)
			if err != nil {
				return err
			}

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
				return runCheckoutSync(cmd, ws, internal.CheckoutSyncOpts{
					Feature:     args[0],
					Push:        push,
					TestCommand: testCmd,
					Verbose:     verbose,
					Policy:      policy,
					NewMode:     newMode,
					Continue:    cont,
					Abort:       abort,
					Changed:     changed,
					PlanGuard:   guardOpts.checkoutGuard(),
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

			// Plan dispatch (§3.3 step 6): below classifySyncState, above
			// deferred I7. A --plan always reaches the document route.
			//
			// The I18 symlink refusal is a MUTATING-route gate: a symlinked
			// runtime-state artefact is a controlled-path fact the plan
			// DESCRIBES (`presence: symlink` plus the
			// owner-artefact-undecodable token, §12.5), never one it refuses
			// over, so the plan route consumes the classifier's own recorded
			// facts and lets the guarded twin be the refusing party (§22.13m).
			if guardOpts.Plan {
				if stateErr != nil && !syncStateSymlinkOnly(stateErr) {
					return stateErr
				}
				return runExternalPlan(cmd, feature, layout, ws, policy, newMode, push, verbose, changed, guardOpts, state, cont)
			}
			// §12.5/§22.13m: on the PLAIN verb the symlink fact is described
			// and adjudicated by the guard seam (guarded) or read straight
			// through by the shipped native ladder (unguarded); only the
			// recovery verbs keep the shipped I18 refusal, which they alone
			// are asserted on.
			if stateErr != nil && (!syncStateSymlinkOnly(stateErr) || cont || abort) {
				return stateErr
			}

			return planGuardRefusal(cmd, dispatchClassifiedSync(cmd, feature, layout, ws, policy, newMode, push, verbose, cont, abort, changed, guardOpts, state))
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
	cmd.Flags().BoolVar(&plan, "plan", false, "Describe the rebase this invocation would perform, then exit; moves no branch, working tree or tws state, but still fetches wherever the route it describes fetches (see --no-fetch)")
	cmd.Flags().BoolVar(&planJSON, "json", false, "Emit the plan document as JSON on stdout (requires --plan)")
	cmd.Flags().IntVar(&maxPerEntry, "max-replay-per-entry", 0, "Refuse before rebasing if any entry of this invocation replays more than N candidates (this invocation only; a guarded run records its limits in recovery state older tws releases refuse to resume)")
	cmd.Flags().IntVar(&maxTotal, "max-replay-total", 0, "Refuse before rebasing if this invocation replays more than N candidates in total (this invocation only; a guarded run records its limits in recovery state older tws releases refuse to resume)")
	cmd.Flags().StringVar(&approvePlan, "approve-plan", "", "Approve the exact plan with the fingerprint printed by --plan (requires a replay limit)")

	_ = cmd.RegisterFlagCompletionFunc("only", syncEntryCompletion)
	_ = cmd.RegisterFlagCompletionFunc("from", syncEntryCompletion)

	return cmd
}

// dispatchClassifiedSync is the classified-state half of RunE (spec §3.3
// step 6 onward, exploration §2.1 insertion points 5-9, 11): deferred I7,
// the live-guard precedence, the cell-4 guarded-sentinel interception, and
// the abort/continue/plain split — cell-1 guarded-fresh, cell-5 three-arm,
// and cell-7 two-arm dispatch included.
func dispatchClassifiedSync(cmd *cobra.Command, feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy, newMode, push, verbose, cont, abort bool, changed map[string]bool, guardOpts planGuardOptions, state internal.SyncExternalState) error {
	// Deferred I7 (insertion point 5): cont && abort survived the
	// command-line block, so no trigger flag was supplied. Refuse only
	// against new-mode state.
	if cont && abort && syncStateRefusesContinueAbort(state) {
		return errSyncContinueAbort()
	}

	// Insertion point 6: derive the one shipped syncVerb, apply the live-
	// guard precedence, then the cell-4 guarded-sentinel interception.
	verb := syncVerbPlain
	switch {
	case abort:
		verb = syncVerbAbort
	case cont:
		verb = syncVerbContinue
	}
	if err := syncCellLiveGuardRefusal(feature, state, verb); err != nil {
		return err
	}

	if state.Cell == 4 {
		if err := syncClassifierBarrier(layout.FeaturePath); err != nil {
			return err
		}
		handled, err := dispatchGuardedLegacySentinel(cmd, feature, layout, ws, policy, newMode, push, verbose, cont, abort, changed, guardOpts, state)
		if handled {
			return err
		}
		// internal.SentinelNotGuarded: fall through to the ordinary cell-4
		// flow below, byte-identical to today.
	}

	return dispatchOrdinarySync(cmd, feature, layout, ws, policy, newMode, push, verbose, cont, abort, changed, guardOpts, state)
}

// dispatchOrdinarySync is the ORDINARY post-interception route of §3.3: the
// abort/continue/plain split, with cell-1 guarded-fresh, the cell-5
// three-arm and the cell-7 two-arm dispatch. It is a separate function
// precisely so the vanished-sentinel row of §12.8b can dispatch its ONE
// re-classification here directly: this function evaluates no sentinel view
// and can therefore never re-enter the cell-4 interception, so a
// .sync-state.yaml that keeps appearing and disappearing cannot make the
// command loop, retry or spin (§12.8b rule 3).
func dispatchOrdinarySync(cmd *cobra.Command, feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy, newMode, push, verbose, cont, abort bool, changed map[string]bool, guardOpts planGuardOptions, state internal.SyncExternalState) error {
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
		// I20 (insertion point 7).
		if newMode && syncTriggersNeedV2(state) {
			return fmt.Errorf("%s", errSyncModeFlagsNeedV2)
		}
		switch {
		// Cell-5 three-arm dispatch (insertion point 8).
		case state.Cell == 5 && externalPersistedGuarded(state.Payload):
			return handleGuardedScopedSyncContinue(cmd, feature, layout, ws, push, policy, changed, guardOpts, state)
		case state.Cell == 5 && guardOpts.Armed():
			// The armed v2 -> v3 upgrade is NOT performed here. §13.2a places
			// it at step 10a — after EvaluatePlanGuard has admitted the run
			// and after the guard reclaim — so an invocation the guard
			// REFUSES leaves the persisted payload byte-identical and the
			// next flagless `--continue` over it is still unguarded.
			// handleGuardedScopedSyncContinue owns that one call site.
			return handleGuardedScopedSyncContinue(cmd, feature, layout, ws, push, policy, changed, guardOpts, state)
		case state.Cell == 5:
			return handleScopedSyncContinue(feature, layout, ws, push, policy, changed, state)
		// Cell-7 two-arm dispatch (insertion point 9).
		case state.Cell == 7 && guardOpts.Armed():
			return handleGuardedLegacySyncContinue(cmd, feature, layout, ws, push, policy, changed, guardOpts, state)
		default:
			return handleSyncContinue(feature, layout, push)
		}
	}
	if err := syncCellRefusal(syncVerbPlain, feature, layout, state); err != nil {
		return err
	}
	if internal.HasSyncState(layout.FeaturePath) {
		legacy, _ := internal.LoadSyncState(layout.FeaturePath)
		return fmt.Errorf("previous sync incomplete (failed on: %s); use --continue or --abort", legacy.FailedBranch)
	}

	// Guarded fresh routes (insertion point 11): opts.Armed() selects
	// runGuardedScopedSync/runGuardedLegacySync; unarmed keeps runScopedSync
	// and syncFeature byte-identical.
	if newMode {
		if guardOpts.Armed() {
			return runGuardedScopedSync(cmd, feature, layout, ws, policy, push, verbose, changed, guardOpts, state)
		}
		return runScopedSync(feature, layout, ws, policy, push, verbose)
	}
	if guardOpts.Armed() {
		return runGuardedLegacySync(cmd, feature, layout, ws, policy, push, verbose, changed, guardOpts, state)
	}

	result := syncFeature(feature, layout, verbose, nil)
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

// dispatchGuardedLegacySentinel is the cell-4 guarded-sentinel interception
// (spec §12, §13.7a; exploration insertion point 6). syncCellRefusal's own
// cell-4 arm unconditionally refuses a --continue and only allows --abort —
// correct for the ordinary "marker present, no payload" artifact, but wrong
// for a resumable guarded-legacy sentinel, which shares that same on-disk
// shape. handled is true whenever this function has fully answered the
// invocation itself; the caller must return err unchanged and never fall
// through to the ordinary dispatch in that case. handled is false only for
// internal.SentinelNotGuarded, where the ordinary cell-4 dispatch (and
// syncCellRefusal's existing cell-4 message) applies completely unchanged.
func dispatchGuardedLegacySentinel(cmd *cobra.Command, feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy, newMode, push, verbose, cont, abort bool, changed map[string]bool, guardOpts planGuardOptions, state internal.SyncExternalState) (bool, error) {
	view := internal.InspectGuardedLegacySentinel(layout.FeaturePath, feature)
	switch {
	case view.Verdict == internal.SentinelAbsent:
		// §12.8b's vanished-state row. The file the classifier saw is gone,
		// which is absence and not a guarded verdict: reclassify EXACTLY
		// once through the shipped classifier and dispatch that second state
		// through the ordinary, NON-INTERCEPTING route. Nothing here
		// re-reads InspectGuardedLegacySentinel, so a path that reappears
		// can never re-enter this interception (rule 3).
		syncReclassifyCount.Add(1)
		second, err := classifySyncState(layout.FeaturePath, newMode)
		if err != nil {
			return true, err
		}
		return true, dispatchOrdinarySync(cmd, feature, layout, ws, policy, newMode, push, verbose, cont, abort, changed, guardOpts, second)
	case internal.GuardedLegacySentinelResumable(state, view):
		// §12.8b (binding): the interception owns this document's flag
		// validation, because a valid backup sentinel never reaches
		// syncCellRefusal or the I20 gate below it. The check runs ONCE,
		// before the verb's arm, for all three verbs, and composes the
		// SHIPPED I20 sentence byte for byte — removing nothing and writing
		// nothing.
		if syncTriggerFlagSupplied(changed) {
			return true, fmt.Errorf("%s", errSyncModeFlagsNeedV2)
		}
		switch {
		case abort:
			return true, abortGuardedLegacySentinel(feature, layout)
		case cont:
			return true, resumeGuardedLegacySentinel(cmd, feature, layout, ws, policy, changed, guardOpts, state, view.Sentinel)
		default:
			return true, fmt.Errorf("a guarded sync was interrupted while recording state for %q; the previous sync state is preserved inside %s — use --continue to resume it, or --abort to discard it", feature, view.Path)
		}
	case view.Verdict == internal.SentinelNotGuarded:
		return false, nil
	default:
		return true, fmt.Errorf("guarded sync state at %s is unreadable or uses an unsupported version; inspect it and remove it manually — tws will not guess", view.Path)
	}
}

// resumeGuardedLegacySentinel resumes a crash-recovered, already-guarded
// legacy sentinel (cell 4, SentinelValid) directly — no upgrade, no second
// sentinel, no re-plan: the sentinel already carries this run's own
// Universe/PendingIntent/limits from its original birth. It performs only
// §13.2a step 2 (guard claim **or** reclaim) and step 4 (the v3 payload),
// skipping the replacement of step 3, and then enters the payload-aware
// executor so the resumed run persists progress into the envelope exactly as
// its interrupted predecessor would have.
func resumeGuardedLegacySentinel(cmd *cobra.Command, feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy, changed map[string]bool, opts planGuardOptions, state internal.SyncExternalState, sentinel *internal.GuardedLegacySentinel) error {
	args := externalPlanArgs{
		Feature: feature, Layout: layout, Ws: ws, Policy: policy, NewMode: false,
		Push: sentinel.Push, Verbose: false, Changed: changed, Opts: opts, State: state, Continue: true,
		Sentinel:         internal.GuardedLegacySentinelView{Verdict: internal.SentinelValid, Sentinel: sentinel, Path: internal.SyncStatePath(layout.FeaturePath)},
		PersistedGuarded: true,
	}
	insp := inspectExternalPlanFor(args)
	if insp.StackErr != nil {
		return fmt.Errorf("load stack: %w", insp.StackErr)
	}
	if insp.SortErr != nil {
		return insp.SortErr
	}

	plan, planReq, err := buildGuardedExternalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), args, insp)
	if err != nil {
		return err
	}
	guard := newPlanGuardRun(planReq, plan, true)

	// The choice between reclaim and claim follows the SNAPSHOT — whether a
	// guard file was there at all — and never which residue produced it: the
	// same cell is reachable from crash window 2 and from a `{sentinel,
	// guard}` rollback residue, and both must answer identically.
	if err := claimOrReclaimGuardedLegacyGuard(layout.FeaturePath, sentinel.OwnerToken, state.HasGuardFile()); err != nil {
		return err
	}

	done := make(map[string]bool, len(sentinel.Universe))
	pending := make(map[string]bool, len(sentinel.PendingIntent))
	for _, name := range sentinel.PendingIntent {
		pending[name] = true
	}
	for _, name := range sentinel.Universe {
		if !pending[name] {
			done[name] = true
		}
	}

	payload, err := writeGuardedLegacyRecoveryPayload(layout.FeaturePath, sentinel)
	if err != nil {
		return err
	}

	printSyncModeHeader(policy)
	// §12.8b's prose table, which is total over the sentinel's own
	// provenance: an interrupted CELL-7 setup resumes a real prior state and
	// prints the shipped resume line over the number that state persisted
	// (prior_pending_count — the same number the interrupted invocation
	// printed), while an interrupted FRESH guarded legacy setup has no prior
	// state and therefore prints no resume prose at all, exactly as the
	// guarded legacy fresh route prints none. Printing a resume line for a
	// fresh-setup sentinel — or printing len(PendingIntent), which is this
	// arm's remaining set and not the operator's number — fails §22.24j.
	if sentinel.PriorLegacyPresent {
		fmt.Printf("Resuming sync with %d pending branch(es)\n", sentinel.PriorPendingCount)
	}
	run := &syncRunContext{Route: internal.RouteLegacy, Payload: payload}
	result := syncWithStackScoped(feature, layout, insp.Stack, insp.Order, done, run, guard)
	if result.Refusal != nil {
		return result.Refusal
	}
	if !result.Complete {
		return fmt.Errorf("sync incomplete")
	}
	if err := clearSyncRunState(layout.FeaturePath, true); err != nil {
		return err
	}
	fmt.Println("Sync complete.")
	if sentinel.Push {
		fmt.Println("\nPushing...")
		if err := pushFeature(feature, layout, false); err != nil {
			return err
		}
	}
	return nil
}

// abortGuardedLegacySentinel discards a crash-recovered guarded legacy
// sentinel (§12.8b, verdict: valid, verb --abort). It performs the SHIPPED
// cell-4 removals, in the shipped order — the shipped payload-appeared
// refusal, then DeleteSyncState, then ReleaseSyncRunGuard — with exactly ONE
// changed byte-sequence: the line it prints.
//
// That line MUST NOT be the bare shipped `Sync state cleared.`: --abort here
// is a clear verb, never a restore verb, and the bare sentence would hide
// the destruction of a document `--continue` could still have resumed. It is
// not a `plan-guard:` marker either (§6.4).
func abortGuardedLegacySentinel(feature string, layout externalSyncLayout) error {
	if internal.HasSyncRunState(layout.FeaturePath) {
		return fmt.Errorf("scoped sync state appeared at %s while aborting; re-run: tws sync %s --abort",
			internal.SyncRunStatePath(layout.FeaturePath), feature)
	}
	internal.DeleteSyncState(layout.FeaturePath)
	internal.ReleaseSyncRunGuard(layout.FeaturePath)
	fmt.Println("Sync state cleared; the interrupted guarded setup's backup of the previous sync state was discarded.")
	return nil
}

// scopedFreshPrelude performs the read-only prelude of the SHIPPED,
// unguarded new-mode fresh route: load the stack, resolve the selection
// (which sorts inside ResolveSyncSelection), and run the I14 local-base
// preflight. It mutates nothing, and its two shipped sorts are exactly what
// §10.1's unguarded row records. The guarded twin does not call it: it
// consumes the one inspection instead (scopedPreludeFromInspection).
func scopedFreshPrelude(feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy) (internal.Stack, internal.SyncSelection, error) {
	stack, err := internal.LoadStack(layout.FeaturePath)
	if err != nil {
		return internal.Stack{}, internal.SyncSelection{}, fmt.Errorf("sync modes require a stack; feature %q has no readable stack.yaml", feature)
	}
	sel, err := internal.ResolveSyncSelection(stack, policy, internal.SyncSelectionOpts{
		Mode:    ws.Mode,
		NewMode: true,
		Feature: feature,
	})
	if err != nil {
		return internal.Stack{}, internal.SyncSelection{}, err
	}
	if err := verifySelectedBasesLocally(layout, stack, sel); err != nil {
		return internal.Stack{}, internal.SyncSelection{}, err
	}
	return stack, sel, nil
}

// runScopedSync executes §3.6 steps 10-17 of a new-mode external run.
func runScopedSync(feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy, push, verbose bool) error {
	stack, sel, err := scopedFreshPrelude(feature, layout, ws, policy)
	if err != nil {
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

	payload, err := setupSyncRunState(layout, feature, marker, token, sel, push, testCommand, validationSource, syncRunStateBirth{})
	if err != nil {
		return err
	}

	printSyncModeHeader(policy)

	run := &syncRunContext{Policy: policy, Sel: sel, Payload: payload}
	result := syncFeatureScoped(feature, layout, verbose, stack, run, nil)
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

// runGuardedScopedSync is the guarded twin of runScopedSync (spec §12,
// §13.7a; exploration insertion point 11): the identical read-only prelude,
// an admission plan built and evaluated through buildGuardedExternalPlan
// before any state is written — which already performs this run's own
// fetch, so syncFeatureScoped's own fetch loop is skipped for a guarded run
// — then the guarded setup/execute/push/teardown sequence.
func runGuardedScopedSync(cmd *cobra.Command, feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy, push, verbose bool, changed map[string]bool, opts planGuardOptions, state internal.SyncExternalState) error {
	args := externalPlanArgs{
		Feature: feature, Layout: layout, Ws: ws, Policy: policy, NewMode: true,
		Push: push, Verbose: verbose, Changed: changed, Opts: opts, State: state, Continue: false,
	}
	// The ONE inspection of this invocation (§10.7 rule 1a, §16.1): the
	// shipped prelude's refusals are composed from its values, so the stack
	// is loaded once, sorted once and the I14 locator evaluated once.
	insp := inspectExternalPlanFor(args)
	stack, sel, err := scopedPreludeFromInspection(feature, insp)
	if err != nil {
		return err
	}

	plan, planReq, err := buildGuardedExternalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), args, insp)
	if err != nil {
		return err
	}
	guard := newPlanGuardRun(planReq, plan, false)

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

	birth := syncRunStateBirth{
		StateVersion: internal.SyncRunStateGuardedVersion,
		Route:        internal.RouteNewMode,
		MaxPerEntry:  opts.MaxPerEntry,
		MaxTotal:     opts.MaxTotal,
	}
	payload, err := setupSyncRunState(layout, feature, marker, token, sel, push, testCommand, validationSource, birth)
	if err != nil {
		return err
	}

	printSyncModeHeader(policy)

	run := &syncRunContext{Policy: policy, Sel: sel, Payload: payload, Validation: validationIdentity(testCommand, validationSource)}
	result := syncFeatureScopedPlanned(feature, layout, stack, insp.Order, run, guard)
	if result.Refusal != nil {
		if !result.Refusal.StatePreserved {
			return rollbackGuardedFreshRefusal(layout.FeaturePath, feature, result.Refusal)
		}
		return result.Refusal
	}
	if !result.Complete {
		return fmt.Errorf("sync incomplete")
	}
	fmt.Println("Sync complete.")
	if push {
		if err := runNewModePush(feature, layout, stack, sel, result.Completed, payload); err != nil {
			return err
		}
	}
	return finalizeScopedSyncRun(layout, payload)
}

// runGuardedLegacySync is the guarded twin of the plain legacy fresh route
// (spec §12, §13.7a; exploration insertion point 11 legacy arm): an
// admission plan built and evaluated through buildGuardedExternalPlan before
// any state is written — which already performs this run's own fetch, so
// syncFeature's own fetch loop is skipped for a guarded run (guard != nil)
// — then setupGuardedLegacyRunState's single-file birth, execution through
// syncFeature, and an explicit guard release on success. A guarded legacy
// run never writes a .sync-state.v2.yaml payload, so its own
// syncWithStackScoped success path (run == nil) only clears the sentinel
// (clearSyncRunState(featurePath, false) never releases a guard); this
// function releases it explicitly, mirroring resumeGuardedLegacySentinel.
func runGuardedLegacySync(cmd *cobra.Command, feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy, push, verbose bool, changed map[string]bool, opts planGuardOptions, state internal.SyncExternalState) error {
	args := externalPlanArgs{
		Feature: feature, Layout: layout, Ws: ws, Policy: policy, NewMode: false,
		Push: push, Verbose: verbose, Changed: changed, Opts: opts, State: state, Continue: false,
	}
	insp := inspectExternalPlanFor(args)
	if insp.StackErr != nil {
		return &internal.PlanGuardRefusalError{Kind: string(internal.RefusalPlanUnavailable), Detail: insp.StackErr.Error()}
	}
	if insp.SortErr != nil {
		return insp.SortErr
	}
	stack, sorted := insp.Stack, insp.Order

	plan, planReq, err := buildGuardedExternalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), args, insp)
	if err != nil {
		return err
	}
	guard := newPlanGuardRun(planReq, plan, false)

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

	universe := make([]string, 0, len(sorted))
	for _, entry := range sorted {
		universe = append(universe, entry.Name)
	}
	pending := guardedLegacySetupPending(universe, guardedLegacyCarry{})

	birth := syncRunStateBirth{StateVersion: internal.SyncRunStateGuardedVersion, Route: internal.RouteLegacy, MaxPerEntry: opts.MaxPerEntry, MaxTotal: opts.MaxTotal}
	payload, undo, err := setupGuardedLegacyRunState(layout, feature, marker, token, universe, pending, push, testCommand, validationSource, birth, guardedLegacyCarry{})
	if err != nil {
		return err
	}

	printSyncModeHeader(policy)

	run := &syncRunContext{Route: internal.RouteLegacy, Payload: payload, Validation: validationIdentity(testCommand, validationSource)}
	result := syncWithStackScoped(feature, layout, stack, sorted, nil, run, guard)
	if result.Refusal != nil {
		// A refusal raised before this invocation moved anything owns its own
		// cleanup: the payload, the guarded sentinel and the run guard this
		// setup installed must not outlive it, which is what keeps
		// StatePreserved false.
		if !result.Refusal.StatePreserved {
			return undo.rollback(layout.FeaturePath, feature, result.Refusal)
		}
		return result.Refusal
	}
	if !result.Complete {
		return fmt.Errorf("sync incomplete")
	}
	if err := clearSyncRunState(layout.FeaturePath, true); err != nil {
		return err
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
// firstUnresolvedSelectedBase is I14's locator: the first selected entry
// whose base does not resolve locally, and the ref it tried. found is false
// when every applicable entry's base resolves (or there is none to check).
// It is the shared locator both verifySelectedBasesLocally and
// externalBasePreflight (sync_plan_guard.go) call, never a second
// implementation of it.
func firstUnresolvedSelectedBase(layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection) (entry, ref string, found bool) {
	for _, e := range sel.Entries {
		if e.Role == internal.SyncRoleAnchor && sel.Policy.Propagation == internal.SyncPropagationLocalOnly {
			continue
		}
		real := internal.GetBranch(stack, e.Name)
		repoCtx := syncRepoContext(layout, real)
		r := resolveEntryBase(stack, real, repoCtx)
		if r == "" {
			continue
		}
		if err := internal.VerifyGitRef(repoCtx, r); err != nil {
			return e.Name, r, true
		}
	}
	return "", "", false
}

func verifySelectedBasesLocally(layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection) error {
	if sel.Policy.Fetch != internal.SyncFetchDisabled {
		return nil
	}
	if entry, ref, found := firstUnresolvedSelectedBase(layout, stack, sel); found {
		return fmt.Errorf("base %q for stack entry %q does not resolve locally; drop --no-fetch or fetch manually first", ref, entry)
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
	case 1:
		return handleStaleSyncGuardAbort(feature, layout)
	case 7:
		return handleLegacyGuardedAbort(feature, layout)
	}
	return handleSyncAbort(feature, layout)
}

// handleStaleSyncGuardAbort implements the classifier cell-1 guard-only
// recovery arm (§12.8): a scoped setup's guard survives its own crash
// between ClaimSyncRunGuard and the sentinel write, with no legacy state and
// no payload beside it — a residue `--abort` otherwise reports as "nothing to
// abort" forever. internal.ReleaseStaleSyncRunGuard judges the guard with one
// Lstat, one read, the shipped PID ladder, and a compare-and-swap; cli
// composes every sentence of the eight-reason ladder, because two of them
// name the feature.
func handleStaleSyncGuardAbort(feature string, layout externalSyncLayout) error {
	release := internal.ReleaseStaleSyncRunGuard(layout.FeaturePath)
	switch release.Reason {
	case internal.SyncGuardAbsent:
		fmt.Println("Nothing to abort — no sync in progress.")
		return nil
	case internal.SyncGuardSymlink:
		return syncSymlinkError(release.Path)
	case internal.SyncGuardUnreadable:
		return fmt.Errorf("sync guard at %s is unreadable: %v; inspect and remove it manually", release.Path, syncErrText(release.Err))
	case internal.SyncGuardInvalidPID:
		return fmt.Errorf("sync guard is being initialized or is invalid; retry or inspect %s", release.Path)
	case internal.SyncGuardSelfPID:
		return fmt.Errorf("sync guard at %s records this process (pid %d); it was not claimed by this invocation — inspect it and remove it manually", release.Path, release.PID)
	case internal.SyncGuardLiveForeign:
		return fmt.Errorf("a scoped sync is running for %q (pid %d); wait for it to exit before --abort", feature, release.PID)
	case internal.SyncGuardReleased:
		fmt.Printf("Stale sync guard from PID %d cleared; no sync state was present.\n", release.PID)
		return nil
	case internal.SyncGuardChanged:
		return fmt.Errorf("sync guard at %s changed while aborting; re-run: tws sync %s --abort", release.Path, feature)
	}
	return fmt.Errorf("unhandled sync guard release reason %q", release.Reason)
}

// handleLegacyGuardedAbort implements the classifier cell-7 recovery arm
// (§12.8a): a real (non-sentinel) legacy .sync-state.yaml survives beside a
// stale guard — left either by an unguarded legacy sync landing on the exact
// residue §12.8 exists for, or by a guarded legacy setup's own first-crash-
// window rollback. internal.ReleaseStaleSyncRunGuardWith judges the guard
// with AllowSelfPID set (an abort never claims a guard, so a self-recorded
// PID is provably not a live owner) and this arm then performs the shipped
// cell-7 abort work itself through the print-free abortLegacySyncState,
// composing the combined sentence.
func handleLegacyGuardedAbort(feature string, layout externalSyncLayout) error {
	release := internal.ReleaseStaleSyncRunGuardWith(layout.FeaturePath, internal.SyncGuardReleaseOpts{AllowSelfPID: true})
	switch release.Reason {
	case internal.SyncGuardAbsent:
		return handleSyncAbort(feature, layout)
	case internal.SyncGuardSymlink:
		return syncSymlinkError(release.Path)
	case internal.SyncGuardUnreadable:
		return fmt.Errorf("sync guard at %s is unreadable: %v; inspect and remove it manually", release.Path, syncErrText(release.Err))
	case internal.SyncGuardInvalidPID:
		return fmt.Errorf("sync guard is being initialized or is invalid; retry or inspect %s", release.Path)
	case internal.SyncGuardLiveForeign:
		return fmt.Errorf("a scoped sync is running for %q (pid %d); wait for it to exit before --abort", feature, release.PID)
	case internal.SyncGuardChanged:
		return fmt.Errorf("sync guard at %s changed while aborting; re-run: tws sync %s --abort", release.Path, feature)
	case internal.SyncGuardReleased:
		found, err := abortLegacySyncState(layout)
		if err != nil {
			return err
		}
		if !found {
			fmt.Printf("Stale sync guard from PID %d cleared; no sync state was present.\n", release.PID)
			return nil
		}
		fmt.Printf("Sync state cleared; stale sync guard from PID %d cleared.\n", release.PID)
		return nil
	}
	return fmt.Errorf("unhandled sync guard release reason %q", release.Reason)
}

// handleSyncAbort is a printing wrapper over abortLegacySyncState: it prints
// the one message the presence or absence of a real legacy sentinel implies,
// and nothing else.
func handleSyncAbort(feature string, layout externalSyncLayout) error {
	found, err := abortLegacySyncState(layout)
	if err != nil {
		return err
	}
	if !found {
		fmt.Println("Nothing to abort — no sync in progress.")
		return nil
	}
	fmt.Println("Sync state cleared.")
	return nil
}

// abortLegacySyncState performs the non-printing half of a real legacy
// sentinel's abort: it aborts an in-progress rebase, if any, then deletes
// the sentinel. It reports whether a sentinel was present at all, so both
// handleSyncAbort and any future caller can print the identical shipped
// messages without duplicating this logic.
func abortLegacySyncState(layout externalSyncLayout) (bool, error) {
	state, err := internal.LoadSyncState(layout.FeaturePath)
	if err != nil {
		return false, nil
	}
	if state.FailedBranch != "" {
		path := layout.WorktreePath(state.FailedBranch)
		if isRebaseInProgress(path) {
			_ = internal.RunSilentDir(path, "git", "rebase", "--abort")
		}
	}
	internal.DeleteSyncState(layout.FeaturePath)
	return true, nil
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
	result := syncWithStackFiltered(feature, layout, stack, sorted, done, nil)
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
	result := syncWithStackScoped(feature, layout, stack, sorted, done, run, nil)
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

// handleGuardedScopedSyncContinue is the armed twin of
// handleScopedSyncContinue (cell-5 three-arm dispatch, arms b and c): the
// identical gates and mismatch checks, an admission plan built and
// evaluated through buildGuardedExternalPlan before the guard is reclaimed,
// and syncWithStackScoped driven with a real *planGuardRun.
func handleGuardedScopedSyncContinue(cmd *cobra.Command, feature string, layout externalSyncLayout, ws internal.Workspace, push bool, policy internal.SyncRunPolicy, changed map[string]bool, opts planGuardOptions, state internal.SyncExternalState) error {
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

	args := externalPlanArgs{
		Feature: feature, Layout: layout, Ws: ws, Policy: payload.Policy(), NewMode: true,
		Push: push, Verbose: false, Changed: changed, Opts: opts, State: state, Continue: true,
		PersistedGuarded: externalPersistedGuarded(payload),
	}
	insp := inspectExternalPlanFor(args)
	if insp.StackErr != nil {
		return fmt.Errorf("load stack: %w", insp.StackErr)
	}
	if insp.SortErr != nil {
		return insp.SortErr
	}
	if insp.SelectionErr != nil {
		return insp.SelectionErr
	}
	stack, sel := insp.Stack, insp.Selection

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

	plan, planReq, err := buildGuardedExternalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), args, insp)
	if err != nil {
		return err
	}
	guard := newPlanGuardRun(planReq, plan, externalPersistedGuarded(payload))

	if err := internal.ReclaimSyncRunGuard(layout.FeaturePath, payload.OwnerToken); err != nil {
		return err
	}

	// §13.2a step 10a: the armed v2 -> v3 upgrade, immediately after a
	// successful reclaim and before any header, prose or Git mutation.
	if opts.Armed() && !externalPersistedGuarded(payload) {
		birth := syncRunStateBirth{StateVersion: internal.SyncRunStateGuardedVersion, MaxPerEntry: opts.MaxPerEntry, MaxTotal: opts.MaxTotal}
		if err := upgradeGuardedSyncRunState(layout.FeaturePath, payload, birth); err != nil {
			return err
		}
	}

	done := make(map[string]bool)
	for _, name := range payload.Completed {
		done[name] = true
	}
	if payload.FailedBranch != "" {
		done[payload.FailedBranch] = true
	}
	sorted := insp.Order

	printSyncModeHeader(payload.Policy())
	fmt.Printf("Resuming sync with %d pending branch(es)\n", len(payload.Pending))

	run := &syncRunContext{Policy: payload.Policy(), Sel: sel, Payload: payload}
	result := syncWithStackScoped(feature, layout, stack, sorted, done, run, guard)
	if result.Refusal != nil {
		return result.Refusal
	}
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

// handleGuardedLegacySyncContinue is the armed twin of handleSyncContinue
// (cell-7 two-arm dispatch, arm b): a real, unguarded legacy failure (cell 7)
// resumed with guard flags supplied now. It performs the identical gates
// handleSyncContinue performs, builds and evaluates an admission plan
// through buildGuardedExternalPlan, captures the prior legacy sentinel's
// exact bytes, then upgrades it in place to a guarded sentinel — via
// setupGuardedLegacyRunState's own carry path — carrying forward the same
// Completed/FailedBranch/Pending state, before driving execution through
// syncWithStackScoped with a real *planGuardRun. Like runGuardedLegacySync,
// it releases the guard explicitly on success since the single-file
// sentinel's own success path (run == nil) never does.
func handleGuardedLegacySyncContinue(cmd *cobra.Command, feature string, layout externalSyncLayout, ws internal.Workspace, push bool, policy internal.SyncRunPolicy, changed map[string]bool, opts planGuardOptions, state internal.SyncExternalState) error {
	legacy, err := internal.LoadSyncState(layout.FeaturePath)
	if err != nil {
		return fmt.Errorf("nothing to continue — no sync in progress")
	}
	failedPath := layout.WorktreePath(legacy.FailedBranch)
	if legacy.FailedBranch != "" && isRebaseInProgress(failedPath) {
		return fmt.Errorf("rebase still in progress in %s; resolve conflicts, run git add . && git rebase --continue, then retry", legacy.FailedBranch)
	}

	args := externalPlanArgs{
		Feature: feature, Layout: layout, Ws: ws, Policy: policy, NewMode: false,
		Push: push, Verbose: false, Changed: changed, Opts: opts, State: state, Continue: true,
	}
	insp := inspectExternalPlanFor(args)
	if insp.StackErr != nil {
		return fmt.Errorf("load stack: %w", insp.StackErr)
	}
	if insp.SortErr != nil {
		return insp.SortErr
	}
	stack, sorted := insp.Stack, insp.Order

	if legacy.FailedBranch != "" {
		failedEntry := internal.GetBranch(stack, legacy.FailedBranch)
		if failedEntry.Name == "" {
			return fmt.Errorf("failed branch %q no longer exists in stack", legacy.FailedBranch)
		}
		if !branchContainsConfiguredParent(layout.WorktreesRoot, stack, failedEntry) {
			return fmt.Errorf("resolved branch %s still does not contain its configured parent %s", failedEntry.Name, failedEntry.Base)
		}
		fmt.Println(formatSyncStatus(legacy.FailedBranch, "active", "resolved"))
	}

	plan, planReq, err := buildGuardedExternalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), args, insp)
	if err != nil {
		return err
	}
	guard := newPlanGuardRun(planReq, plan, false)

	carry := carriedGuardedLegacyState(legacy)

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

	universe := make([]string, 0, len(sorted))
	for _, entry := range sorted {
		universe = append(universe, entry.Name)
	}
	pending := guardedLegacySetupPending(universe, carry)

	birth := syncRunStateBirth{StateVersion: internal.SyncRunStateGuardedVersion, Route: internal.RouteLegacy, MaxPerEntry: opts.MaxPerEntry, MaxTotal: opts.MaxTotal}
	payload, undo, err := setupGuardedLegacyRunState(layout, feature, marker, token, universe, pending, push, testCommand, validationSource, birth, carry)
	if err != nil {
		return err
	}

	done := make(map[string]bool)
	for _, name := range legacy.Completed {
		done[name] = true
	}
	if legacy.FailedBranch != "" {
		done[legacy.FailedBranch] = true
	}

	printSyncModeHeader(policy)
	fmt.Printf("Resuming sync with %d pending branch(es)\n", carry.PriorPendingCount)

	run := &syncRunContext{Route: internal.RouteLegacy, Payload: payload, Validation: validationIdentity(testCommand, validationSource)}
	result := syncWithStackScoped(feature, layout, stack, sorted, done, run, guard)
	if result.Refusal != nil {
		// Same pre-mutation rule as the guarded fresh route: an upgrade that
		// refuses without moving anything undoes exactly what it created —
		// payload, the conditional sentinel undo that restores the captured
		// legacy bytes, then the guard — and reports an honest residue if
		// any of those three steps itself fails.
		if !result.Refusal.StatePreserved {
			return undo.rollback(layout.FeaturePath, feature, result.Refusal)
		}
		return result.Refusal
	}
	if !result.Complete {
		return fmt.Errorf("sync incomplete")
	}
	if err := clearSyncRunState(layout.FeaturePath, true); err != nil {
		return err
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

// scopedSelectionFromPayloadOrder is scopedSelectionFromPayload's own
// order-taking half: the payload-existence check and the filter-to-
// payload.Selected projection, taking an already-sorted order so a caller
// that already holds this invocation's own TopoSort result (InspectExternalPlan)
// never sorts a second time. It is otherwise byte-identical to
// scopedSelectionFromPayload.
func scopedSelectionFromPayloadOrder(stack internal.Stack, order []internal.StackEntry, payload *internal.SyncRunState, feature string, mode internal.WorkspaceMode) (internal.SyncSelection, error) {
	for _, name := range payload.Selected {
		if !internal.HasBranch(stack, name) {
			return internal.SyncSelection{}, fmt.Errorf("selected stack entry %q no longer exists in stack; use --abort", name)
		}
	}
	sel, err := internal.ResolveSyncSelectionFromOrder(stack, order, payload.Policy(), internal.SyncSelectionOpts{
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

// scopedSelectionFromPayload rebuilds the run's selection from the payload's
// materialized `selected` list, so a stack.yaml edit between failure and
// --continue cannot silently re-scope the run. It is now a thin wrapper over
// scopedSelectionFromPayloadOrder, sorting its own order exactly as
// internal.ResolveSyncSelection itself does internally, so its result stays
// byte-identical.
func scopedSelectionFromPayload(stack internal.Stack, payload *internal.SyncRunState, feature string, mode internal.WorkspaceMode) (internal.SyncSelection, error) {
	order, err := internal.TopoSort(stack)
	if err != nil {
		return internal.SyncSelection{}, err
	}
	return scopedSelectionFromPayloadOrder(stack, order, payload, feature, mode)
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

// syncFeature performs a legacy (non-scoped) sync run. guard is nil on the
// shipped path (unguarded fresh legacy) and non-nil only from
// runGuardedLegacySync, which has already performed this run's own
// admission fetch — so this function's own fetch loop is skipped whenever
// guard != nil, and a guarded LoadStack failure is reported as a
// plan-unavailable refusal rather than silently degrading to syncFallback:
// syncFallback is unreachable from a guarded route.
func syncFeature(feature string, layout externalSyncLayout, verbose bool, guard *planGuardRun) syncResult {
	stack, err := internal.LoadStack(layout.FeaturePath)
	if err != nil {
		if guard != nil {
			return syncResult{Refusal: &internal.PlanGuardRefusalError{Kind: string(internal.RefusalPlanUnavailable), Detail: err.Error()}}
		}
		fetchQuiet("", "", verbose)
		syncFallback(layout)
		return syncResult{Complete: true}
	}

	if guard == nil {
		repos := internal.UniqueRepos(stack, layout.FeaturePath)
		for repo, wtPath := range repos {
			fetchQuiet(repo, wtPath, verbose)
		}
	}

	sorted, err := internal.TopoSort(stack)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return syncResult{}
	}
	return syncWithStack(feature, layout, stack, sorted, guard)
}

// syncFeatureScoped is syncFeature for a new-mode run: the stack is already
// loaded and validated, and the fetch loop is restricted to the repos the
// selection actually touches. guard is nil on the shipped path; a guarded
// caller (runGuardedScopedSync, handleGuardedScopedSyncContinue) has already
// performed this run's own admission fetch, so this function's own fetch
// loop — and the Fetching stage transition that precedes it — are skipped
// whenever guard != nil.
func syncFeatureScoped(feature string, layout externalSyncLayout, verbose bool, stack internal.Stack, run *syncRunContext, guard *planGuardRun) syncResult {
	if run.Policy.Fetch == internal.SyncFetchEnabled && guard == nil {
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
	return syncFeatureScopedPlanned(feature, layout, stack, sorted, run, guard)
}

// syncFeatureScopedPlanned is syncFeatureScoped without the fetch block and
// without its TopoSort, which it receives as `sorted` (exploration §2.3): a
// guarded route already fetched above its claim and already owns this
// invocation's single order, so calling this instead of syncFeatureScoped is
// what keeps the guarded new-mode fresh route at exactly ONE sort and
// exactly one fetch (§9.1a rule 3, §10.1).
func syncFeatureScopedPlanned(feature string, layout externalSyncLayout, stack internal.Stack, sorted []internal.StackEntry, run *syncRunContext, guard *planGuardRun) syncResult {
	return syncWithStackScoped(feature, layout, stack, sorted, nil, run, guard)
}

// scopedPreludeFromInspection composes the shipped new-mode fresh prelude's
// refusals from the ONE inspection this invocation already performed, in the
// shipped order (stack, selection, I14 preflight) and with the shipped
// sentences. It re-loads nothing, re-sorts nothing and re-probes no base
// ref: §10.7 rule 1a's locator already ran inside InspectExternalPlan, and
// its verdict travels as a value.
func scopedPreludeFromInspection(feature string, insp ExternalPlanInspection) (internal.Stack, internal.SyncSelection, error) {
	if insp.StackErr != nil {
		return internal.Stack{}, internal.SyncSelection{}, fmt.Errorf("sync modes require a stack; feature %q has no readable stack.yaml", feature)
	}
	if insp.SortErr != nil {
		return internal.Stack{}, internal.SyncSelection{}, insp.SortErr
	}
	if insp.SelectionErr != nil {
		return internal.Stack{}, internal.SyncSelection{}, insp.SelectionErr
	}
	if insp.BasePreflight.Failed {
		return internal.Stack{}, internal.SyncSelection{}, fmt.Errorf("%s", insp.BasePreflight.Detail)
	}
	return insp.Stack, insp.Selection, nil
}

// validationIdentity freezes this guarded run's own validation command at
// birth (§15.5a): the executor consumes it as a VALUE and never re-reads
// internal.LoadConfig() under the claim.
func validationIdentity(command, source string) internal.PlanValidationIdentity {
	return internal.PlanValidationIdentity{
		Applies: true,
		Command: command,
		Source:  source,
		Digest:  internal.ValidationDigest(command),
	}
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

// fetchQuietTo is fetchQuiet's writer-parameterized body: out receives the
// "Fetching X... done/failed" prose and the verbose child's own stdout;
// errw receives the verbose child's own stderr. A real execution passes
// os.Stdout/os.Stderr (fetchQuiet, below) so RunDirTo/RunTo reproduce
// RunDir/Run byte-for-byte; the plan route passes the same writer
// (cmd.ErrOrStderr()) for both, so a verbose child's output never reaches
// the real stdout while a plan document is being rendered there. ctx is the
// plan-side fetch context this row is measured against; it is the zero
// value on every real (non-plan) call, so RepoToken/ContextRoot/etc. are
// simply "" and Effect is nil — fields only the plan route's own
// PlanFetchOutcome folding reads.
func fetchQuietTo(out, errw io.Writer, repo, wtPath string, verbose bool, ctx internal.PlanFetchContext) internal.PlanFetchRepoResult {
	label := "default repo"
	if repo != "" {
		label = repo
	}

	var err error
	if verbose {
		fmt.Fprintf(out, "Fetching %s...\n", label) //nolint:errcheck
		if wtPath != "" {
			err = internal.RunDirTo(out, errw, wtPath, "git", "fetch")
		} else if repo != "" {
			err = internal.RunDirTo(out, errw, repo, "git", "fetch")
		} else {
			err = internal.RunTo(out, errw, "git", "fetch")
		}
	} else {
		fmt.Fprintf(out, "Fetching %s... ", label) //nolint:errcheck
		if wtPath != "" {
			err = internal.RunSilentDir(wtPath, "git", "fetch")
		} else if repo != "" {
			err = internal.RunSilentDir(repo, "git", "fetch")
		} else {
			err = internal.RunSilent("git", "fetch")
		}
		if err != nil {
			fmt.Fprintln(out, "failed") //nolint:errcheck
		} else {
			fmt.Fprintln(out, "done") //nolint:errcheck
		}
	}

	candidates := ctx.Candidates
	if candidates == nil {
		candidates = []internal.PlanFetchCandidate{}
	}
	return internal.PlanFetchRepoResult{
		RepoToken:         ctx.RepoToken,
		ContextRoot:       ctx.Root,
		ContextCommonDir:  ctx.CommonDir,
		ContextSource:     ctx.Source,
		ContextCandidates: candidates,
		Effect:            ctx.Effect,
		Attempted:         true,
		OK:                err == nil,
	}
}

// fetchQuiet is the shipped call site's discarding wrapper: it writes to
// os.Stdout/os.Stderr and passes a zero PlanFetchContext, exactly the fetch
// the shipped body always performed, and discards the row fetchQuietTo
// returns since no shipped caller ever folded it into a PlanFetchOutcome.
func fetchQuiet(repo, wtPath string, verbose bool) {
	fetchQuietTo(os.Stdout, os.Stderr, repo, wtPath, verbose, internal.PlanFetchContext{})
}

// fetchReposTo is the shared fetch loop both a real fetch (fetchStackReposTo/
// fetchScopedReposTo, called with os.Stdout/os.Stderr from a guarded
// executor) and the plan route's own pre-fetch enumeration (called with
// cmd.ErrOrStderr() for both writers) use: given the repo-token -> worktree-
// path map a fetch would touch, it fetches each in sorted-token order —
// exactly internal.checkoutFetchPlan's own enumeration order — and folds
// every attempted row into one PlanFetchOutcome.
func fetchReposTo(out, errw io.Writer, repos map[string]string, verbose bool, enumerated internal.PlanFetchPlan) internal.PlanFetchOutcome {
	// The enumerated plan is the ONE place a context's common dir and its
	// measured fetch effect are established, always BEFORE any child runs
	// (§11.1). Each attempted row copies them verbatim, so the outcome the
	// push baseline joins on (§14.1a rule 8's conjuncts ii-iv) describes the
	// fetch that really happened rather than an unmeasured stub.
	byToken := make(map[string]internal.PlanFetchContext, len(enumerated.Contexts))
	for _, c := range enumerated.Contexts {
		byToken[c.RepoToken] = c
	}

	tokens := make([]string, 0, len(repos))
	for token := range repos {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)

	outcome := internal.PlanFetchOutcome{Applies: true, Repos: []internal.PlanFetchRepoResult{}}
	for _, token := range tokens {
		wtPath := repos[token]
		root := token
		source := "entry-repo"
		if root == "" {
			root = wtPath
			source = "worktree"
		}
		ctx, enumeratedContext := byToken[token]
		if !enumeratedContext {
			ctx = internal.PlanFetchContext{RepoToken: token, Root: root, Source: source}
		}
		row := fetchQuietTo(out, errw, token, wtPath, verbose, ctx)
		outcome.Repos = append(outcome.Repos, row)
		if row.Attempted {
			outcome.Attempted = true
		}
	}
	return outcome
}

// fetchStackReposTo fetches every repo the whole stack touches — the legacy
// route's own fetch scope, both for a guarded fresh legacy run's admission
// fetch and for InspectExternalPlan's own legacy-route enumeration.
func fetchStackReposTo(out, errw io.Writer, layout externalSyncLayout, stack internal.Stack, verbose bool, enumerated internal.PlanFetchPlan) internal.PlanFetchOutcome {
	repos := internal.UniqueRepos(stack, layout.FeaturePath)
	return fetchReposTo(out, errw, repos, verbose, enumerated)
}

// fetchScopedReposTo fetches only the repos the selection's own materialized
// entries touch — the new-mode route's own fetch scope, both for a guarded
// scoped run's admission fetch and for InspectExternalPlan's own new-mode
// enumeration.
func fetchScopedReposTo(out, errw io.Writer, layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection, verbose bool, enumerated internal.PlanFetchPlan) internal.PlanFetchOutcome {
	sub := internal.Stack{Branches: selectedRealEntries(stack, sel)}
	repos := internal.UniqueRepos(sub, layout.FeaturePath)
	return fetchReposTo(out, errw, repos, verbose, enumerated)
}
