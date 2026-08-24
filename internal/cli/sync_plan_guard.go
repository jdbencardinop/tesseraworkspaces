package cli

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// sync-plan-guard — the package-cli half of the rebase-plan-guard feature
// (spec §3): the five control flags, their §3.4 validation ladder, the
// external plan-only route (§13.7a), the guarded execution routes, and the
// writer-taking render/marker plumbing (§3.6) shared by both workspace
// modes.
// ---------------------------------------------------------------------------

// planGuardControlFlags is the closed key set of planGuardOptions.Present, in
// the source order of spec §3.1.
var planGuardControlFlags = []string{"plan", "json", "max-replay-per-entry", "max-replay-total", "approve-plan"}

// planGuardOptions is the control-flag envelope resolvePlanGuardOptions
// produces. It is a strict cli-side mirror of internal.CheckoutPlanGuard,
// minus the runtime-only PersistedGuarded field.
type planGuardOptions struct {
	Plan        bool
	JSON        bool
	MaxPerEntry *int // nil == absent; *v == 0 is a real limit
	MaxTotal    *int
	Approve     string
	Present     map[string]bool // "plan","json","max-replay-per-entry","max-replay-total","approve-plan"
}

// Armed reports whether this invocation armed something: at least one limit
// is present, or a token was supplied. It mirrors
// internal.CheckoutPlanGuard.Armed() exactly.
func (o planGuardOptions) Armed() bool {
	return o.MaxPerEntry != nil || o.MaxTotal != nil || o.Approve != ""
}

// checkoutGuard converts to the internal-owned type both executors and the
// plan builder require, so no cli type ever crosses the package boundary
// (§19.1, §19.2). PersistedGuarded always starts false here: only the
// checkout --continue dispatch (checkout_sync.go) and the external guarded
// continuation handlers set it, each from its own persisted-guarded fact.
func (o planGuardOptions) checkoutGuard() internal.CheckoutPlanGuard {
	return internal.CheckoutPlanGuard{
		Plan:        o.Plan,
		JSON:        o.JSON,
		MaxPerEntry: o.MaxPerEntry,
		MaxTotal:    o.MaxTotal,
		Approve:     o.Approve,
		Present:     o.Present,
	}
}

// checkoutGuardPersisted is checkoutGuard with PersistedGuarded set from the
// caller's own persisted-guarded fact — the external continuation handlers'
// own analogue of checkout_sync.go's opts.PlanGuard.PersistedGuarded
// assignment. Without it, EvaluatePlanGuard's own Guarded() gate would be a
// silent no-op on a --continue that re-supplies no limit flag of its own,
// even though the persisted run is already guarded.
func (o planGuardOptions) checkoutGuardPersisted(persisted bool) internal.CheckoutPlanGuard {
	g := o.checkoutGuard()
	g.PersistedGuarded = persisted
	return g
}

// resolvePlanGuardOptions performs the §3.4 incompatibility matrix in its
// exact evaluation order and returns byte-exact messages. It is called once,
// immediately after resolveSyncPolicy and before mode dispatch (§3.3 step 3),
// so both workspace modes reject identically. It reads only the five control
// flags plus cmd.Flags().Changed("continue")/("abort"): no state, no stack,
// no repository.
func resolvePlanGuardOptions(cmd *cobra.Command) (planGuardOptions, error) {
	present := make(map[string]bool, len(planGuardControlFlags))
	for _, name := range planGuardControlFlags {
		present[name] = cmd.Flags().Changed(name)
	}

	opts := planGuardOptions{Present: present}
	opts.Plan, _ = cmd.Flags().GetBool("plan")
	opts.JSON, _ = cmd.Flags().GetBool("json")
	opts.Approve, _ = cmd.Flags().GetString("approve-plan")

	// Row 1: --json requires --plan. Checked unconditionally, first, so a
	// --json --abort invocation (row 6 of the matrix) is refused with this
	// same message rather than the --abort combination message below.
	if present["json"] && !opts.Plan {
		return planGuardOptions{}, fmt.Errorf("--json requires --plan")
	}

	cont := syncBoolFlag(cmd, "continue")
	abort := syncBoolFlag(cmd, "abort")

	// Rows 2-5: every other control flag is incompatible with --abort.
	if abort {
		switch {
		case present["plan"]:
			return planGuardOptions{}, fmt.Errorf("--plan cannot be combined with --abort")
		case present["max-replay-per-entry"]:
			return planGuardOptions{}, fmt.Errorf("--max-replay-per-entry cannot be combined with --abort")
		case present["max-replay-total"]:
			return planGuardOptions{}, fmt.Errorf("--max-replay-total cannot be combined with --abort")
		case present["approve-plan"]:
			return planGuardOptions{}, fmt.Errorf("--approve-plan cannot be combined with --abort")
		}
	}

	// Rows 7-8: a supplied limit must be zero or greater. Row 9 (a
	// non-integer value) is pflag's own parse error and never reaches RunE.
	if present["max-replay-per-entry"] {
		v, _ := cmd.Flags().GetInt("max-replay-per-entry")
		if v < 0 {
			return planGuardOptions{}, fmt.Errorf("--max-replay-per-entry must be zero or greater")
		}
		opts.MaxPerEntry = &v
	}
	if present["max-replay-total"] {
		v, _ := cmd.Flags().GetInt("max-replay-total")
		if v < 0 {
			return planGuardOptions{}, fmt.Errorf("--max-replay-total must be zero or greater")
		}
		opts.MaxTotal = &v
	}

	// Rows 10-11: --approve-plan's syntactic shape, then its presence-
	// requires-limits rule (never enforced on --continue, regardless of
	// --plan).
	if present["approve-plan"] {
		token := strings.TrimSpace(opts.Approve)
		if !planApproveTokenShape.MatchString(token) {
			return planGuardOptions{}, fmt.Errorf("--approve-plan requires a 64-character lowercase hex fingerprint")
		}
		opts.Approve = token
		if !cont && opts.MaxPerEntry == nil && opts.MaxTotal == nil {
			return planGuardOptions{}, fmt.Errorf("--approve-plan requires --max-replay-per-entry or --max-replay-total")
		}
	}

	return opts, nil
}

// planApproveTokenShape is --approve-plan's exact syntactic domain,
// ^[0-9a-f]{64}$ (spec §3.1 rule 2). BuildRebasePlan itself performs the
// bytewise comparison against the computed fingerprint; this shape check is
// the only validation resolvePlanGuardOptions performs on the token.
var planApproveTokenShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ---------------------------------------------------------------------------
// Render/marker/refusal plumbing (spec §3.6)
// ---------------------------------------------------------------------------

// renderPlanDocument is a one-line wrapper passing cmd.OutOrStdout() and
// cmd.ErrOrStderr() to renderPlanDocumentTo.
func renderPlanDocument(cmd *cobra.Command, plan internal.RebasePlan, jsonMode bool) error {
	return renderPlanDocumentTo(cmd.OutOrStdout(), cmd.ErrOrStderr(), plan, jsonMode)
}

// renderPlanDocumentTo is the writer-taking render dispatch: the sole caller
// of FormatRebasePlan/MarshalRebasePlan and the sole binder of document bytes
// to a stream anywhere in the tree. It performs exactly one stdout.Write of
// the complete buffer — no retry, no fallback stream — and returns the
// renderer's error without writing anything when encoding/formatting failed
// (§3.6 row 6). stderr is accepted for signature symmetry with this
// feature's other two-writer helpers; neither renderer emits an operational
// byte for this function to forward, so it is otherwise unused here.
func renderPlanDocumentTo(stdout, stderr io.Writer, plan internal.RebasePlan, jsonMode bool) error {
	_ = stderr
	var buf []byte
	var err error
	if jsonMode {
		buf, err = internal.MarshalRebasePlan(plan)
	} else {
		buf, err = internal.FormatRebasePlan(plan)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(buf)
	return err
}

// writePlanGuardMarker prints the anchored guard marker line (§6.4) — the
// ONE format string `plan-guard: %s\n` of e.Error() — and nothing else.
func writePlanGuardMarker(w io.Writer, e *internal.PlanGuardRefusalError) {
	_, _ = fmt.Fprintf(w, "plan-guard: %s\n", e.Error())
}

// planGuardRefusal prints the anchored marker for a guard-owned refusal and
// returns err unchanged. Every other error passes through untouched and
// marker-free, exactly as spec §3.6 rows 4-7, 10 require. It is the single
// wrap point both the external and the checkout RunE dispatch apply to their
// own executing-route return.
func planGuardRefusal(cmd *cobra.Command, err error) error {
	var refusal *internal.PlanGuardRefusalError
	if errors.As(err, &refusal) {
		writePlanGuardMarker(cmd.ErrOrStderr(), refusal)
	}
	return err
}

// ---------------------------------------------------------------------------
// planGuardRun — the executor-side guard carrier (spec §12.2b, §12.3)
// ---------------------------------------------------------------------------

// planGuardRun is the guarded executor's own JIT revalidation state: the
// approved plan's per-entry rows (keyed by name), the effective limits, the
// running replay total THIS invocation has accumulated, and whether state
// has already been preserved by an earlier invocation's own progress. A nil
// *planGuardRun is the unguarded path throughout syncWithStackScoped: every
// JIT seam is itself gated on guard != nil, so a nil guard leaves every
// shipped byte and process untouched.
type planGuardRun struct {
	req            internal.RebasePlanRequest
	approved       map[string]internal.PlanEntry
	limits         internal.PlanGuardLimits
	replayedTotal  int
	statePreserved bool
}

// newPlanGuardRun builds the carrier from the already-built, already-
// evaluated plan. Its limits are the document's OWN plan.Guard.Limits — the
// same reconciled per-entry/total values RevalidatePlanGuardEntry must
// enforce against — never re-resolved a second time from opts/payload.
// statePreserved starts true only for a --continue that reclaimed an
// existing, already-guarded run: a branch may already have moved in a PRIOR
// invocation before this one's own first revalidation ever runs. It starts
// false for a fresh run and flips true after this invocation's own first
// successful revalidate (spec's Long text: "before any branch has moved
// unless that line says state-preserved:").
func newPlanGuardRun(req internal.RebasePlanRequest, plan internal.RebasePlan, statePreserved bool) *planGuardRun {
	approved := make(map[string]internal.PlanEntry, len(plan.Entries))
	for _, e := range plan.Entries {
		approved[e.Name] = e
	}
	limits := internal.PlanGuardLimits{PerEntry: plan.Guard.Limits.MaxReplayPerEntry, Total: plan.Guard.Limits.MaxReplayTotal}
	return &planGuardRun{req: req, approved: approved, limits: limits, statePreserved: statePreserved}
}

// revalidate re-verifies one entry immediately before its rebase runs
// (spec §12.2b/§12.3's JIT seam), through internal.RevalidatePlanGuardEntry
// alone — never internal.RevalidatePlanEntry directly. A name absent from
// the approved plan (never expected on a guarded route, since Remaining is
// always a subset of the approved entries) never blocks: there is nothing
// approved to compare against, so revalidation has nothing to say about it.
// The running replay total accumulates the count the seam FRESHLY resolved,
// never the approved row's own recorded value: an `upstream-deferred` row is
// approved with a null count, so accumulating the approved value would add
// zero for exactly the rows §10.4's deferral policy exists to re-measure and
// would let a run walk past max_replay_total unrefused.
func (g *planGuardRun) revalidate(name string) error {
	if g == nil {
		return nil
	}
	approved, ok := g.approved[name]
	if !ok {
		return nil
	}
	res, err := internal.RevalidatePlanGuardEntry(internal.RevalidatePlanGuardEntryRequest{
		Request:        g.req,
		Approved:       approved,
		Limits:         g.limits,
		ReplayedTotal:  g.replayedTotal,
		StatePreserved: g.statePreserved,
	})
	if err != nil {
		return err
	}
	g.replayedTotal += res.CandidateCount
	g.statePreserved = true
	return nil
}

// planLayout converts the external layout to the internal-owned
// RebasePlanLayout (§9.0): RepoRoot stays "" on the external route, exactly
// as the checkout twin leaves WorktreesRoot "".
func planLayout(l externalSyncLayout) internal.RebasePlanLayout {
	return internal.RebasePlanLayout{FeaturePath: l.FeaturePath, WorktreesRoot: l.WorktreesRoot}
}

// externalWorkspace projects internal.Workspace into the plan's display-only
// PlanWorkspace (§8.3). It re-derives no identity: ws is already this
// invocation's own single RequireWorkspace() result.
func externalWorkspace(ws internal.Workspace) internal.PlanWorkspace {
	var stableID *string
	if ws.StableID != "" {
		id := ws.StableID
		stableID = &id
	}
	return internal.PlanWorkspace{Mode: string(internal.ModeExternal), StableID: stableID, RepoRoot: ws.RepoRoot}
}

// ---------------------------------------------------------------------------
// External plan inspection (spec §13.7a) — the read-only measurement pass
// mirroring internal.InspectCheckoutPlan/CheckoutPlanInspectionRequest
// field-for-field, substituting the external state/selection/stage types.
// ---------------------------------------------------------------------------

// ExternalPlanInspectionRequest bundles InspectExternalPlan's inputs.
type ExternalPlanInspectionRequest struct {
	Feature string
	Layout  externalSyncLayout
	Ws      internal.Workspace
	Opts    planGuardOptions
	Policy  internal.SyncRunPolicy
	NewMode bool
	Push    bool
	Verbose bool
	Changed map[string]bool

	// Continue is this invocation's own --continue flag. It is supplied by
	// the caller (runExternalPlan), which already resolved it identically to
	// the executing dispatch.
	Continue bool

	// PersistedGuarded is the caller's own persisted-guarded fact (§13.6 rule
	// 4d): true whenever a --continue is resuming an already-guarded
	// persisted run (a scoped payload at state_version >= 3, or a guarded
	// legacy sentinel), independent of whether this invocation re-supplies
	// any limit flag of its own. It is never true on a fresh run.
	PersistedGuarded bool

	// Classified is classifySyncState's own already-produced verdict —
	// required, exactly mirroring InspectExternalPlanState's own contract:
	// never recomputed here.
	Classified internal.SyncExternalState

	// Sentinel is the ONE guarded-legacy sentinel view of §13.6 rule 2c,
	// evaluated by the §19.2 dispatch on cell 4 alone and threaded in. It is
	// the zero value (verdict "", i.e. not applicable) on every other cell,
	// which never consults it, and this pipeline never decodes a sentinel of
	// its own. On the cell-4 RECOVERY arm it is the resumed subject: its
	// intent block supplies the universe, the pending set, the effective
	// limits and the frozen validation command (§19.2, §12.8b).
	Sentinel internal.GuardedLegacySentinelView
}

// resumableSentinel is the cell-4 recovery arm's own subject, or nil on
// every other cell: the sentinel this invocation may resume, exactly as
// GuardedLegacySentinelResumable decided it.
func (req ExternalPlanInspectionRequest) resumableSentinel() *internal.GuardedLegacySentinel {
	if req.Classified.Cell != 4 || req.Sentinel.Verdict != internal.SentinelValid {
		return nil
	}
	return req.Sentinel.Sentinel
}

// checkoutGuard combines req.Opts' own control-flag mirror with req's own
// persisted-guarded fact, exactly as planGuardOptions.checkoutGuardPersisted
// does — the single conversion both externalPlanRequest's Guard field and
// buildGuardedExternalPlan's EvaluatePlanGuard call must use, so neither one
// can drift from the other.
func (req ExternalPlanInspectionRequest) checkoutGuard() internal.CheckoutPlanGuard {
	return req.Opts.checkoutGuardPersisted(req.PersistedGuarded)
}

// ExternalPlanInspection is InspectExternalPlan's own result: every fact
// externalPlanRequest needs to construct a RebasePlanRequest without
// re-probing Git state a second time.
type ExternalPlanInspection struct {
	Continue     bool
	Version      internal.GitVersion
	Capabilities internal.GitCapabilities

	State internal.ExternalPlanState

	Stack    internal.Stack
	StackErr error
	Order    []internal.StackEntry
	SortErr  error

	Selection         internal.SyncSelection
	SelectionResolved bool
	SelectionErr      error

	Remaining  []string
	StageFacts []internal.PlanStageFact

	BasePreflight internal.PlanBasePreflight
	FetchPlan     internal.PlanFetchPlan
	Gates         []internal.PlanGateResult

	Limits         internal.PlanGuardLimits
	LimitConflicts []internal.PlanGuardLimitConflict
}

// InspectExternalPlan is the external route's own read-only inspection,
// mirroring internal.InspectCheckoutPlan's ordered pass: version/
// capabilities, state, the fresh-run precondition gates, the reconciled
// guard limits, the stack/order/selection triple, and — continue route only
// — the remaining-entries projection, or — fresh route only — the I14 base
// preflight and the pre-fetch context enumeration.
func InspectExternalPlan(req ExternalPlanInspectionRequest) ExternalPlanInspection {
	var insp ExternalPlanInspection
	insp.Continue = req.Continue
	insp.Capabilities, insp.Version, _ = internal.ProbeGitCapabilities()
	insp.State = internal.InspectExternalPlanState(req.Layout.FeaturePath, internal.ExternalPlanStateOpts{Classified: req.Classified})
	// §16 rules 3a/3b, at their required position: immediately after the
	// version probe, ABOVE this route's own fetch and above every config
	// read. Rule 3b is argv-derived and decided from mode and scope here,
	// before any row is built: only an external UNSCOPED (scope=all) run
	// publishes a pass-1 `--update-refs` argv (§9.3).
	insp.Gates = append(
		internal.CapabilityGates(insp.Version, insp.Capabilities, externalArgvNeedsUpdateRefs(req)),
		externalPreconditionGates(req)...)

	payload := req.Classified.Payload
	sentinel := req.resumableSentinel()
	insp.Limits = resolveExternalGuardLimits(req.Opts, payload, sentinel)
	insp.LimitConflicts = resolveExternalLimitConflicts(payload, sentinel, req.Opts)

	stack, stackErr := internal.LoadStack(req.Layout.FeaturePath)
	if stackErr != nil {
		insp.StackErr = stackErr
		return insp
	}
	insp.Stack = stack

	order, sortErr := internal.TopoSort(stack)
	if sortErr != nil {
		insp.SortErr = sortErr
		return insp
	}
	insp.Order = order

	// A --continue with an existing scoped payload reflects the run's frozen
	// `selected` list (scopedSelectionFromPayloadOrder), exactly as the
	// executing continue routes do, rather than re-deriving scope from this
	// invocation's own flags: syncContinueMismatches only refuses an
	// *explicit* re-specification that disagrees with the payload, so an
	// unspecified flag's default must never silently re-scope the described
	// plan away from what execution will actually select.
	var sel internal.SyncSelection
	var selErr error
	switch {
	case req.Continue && payload != nil:
		sel, selErr = scopedSelectionFromPayloadOrder(stack, order, payload, req.Feature, req.Ws.Mode)
	case req.Continue && sentinel != nil:
		// §19.2's recovery arm: the resumed universe is the sentinel's own
		// intent block — the full-stack legacy selection the interrupted run
		// froze — never this invocation's flags, which the interception has
		// already refused if they carried a trigger.
		sel, selErr = legacySelectionFromOrder(stack, order, req.Policy, req.Feature, req.Ws.Mode)
	default:
		sel, selErr = internal.ResolveSyncSelectionFromOrder(stack, order, req.Policy, internal.SyncSelectionOpts{
			Mode:    internal.ModeExternal,
			NewMode: req.NewMode,
			Feature: req.Feature,
		})
	}
	if selErr != nil {
		insp.SelectionErr = selErr
		return insp
	}
	insp.Selection = sel
	insp.SelectionResolved = true

	route := externalEffectiveRoute(req, payload)
	if sentinel != nil {
		// The recovery arm's remaining set is the sentinel's OWN pending
		// intent (§12.8b, §19.2 step 4), narrowed to the entries the current
		// stack still has: the backup sentinel's `.sync-state.yaml` carries
		// empty pending/completed lists by construction, so the shipped
		// legacy projection would otherwise read "nothing is done" and
		// republish the entire universe.
		insp.Remaining = sentinelRemaining(sentinel, sel)
	} else {
		insp.Remaining = internal.RemainingRebaseEntries(
			route,
			planLayout(req.Layout),
			internal.RemainingEntriesState{Mode: internal.ModeExternal, External: insp.State},
			order, sel,
		)
	}

	if req.Continue {
		insp.StageFacts = externalStageFacts(insp.Remaining)
		return insp
	}

	insp.BasePreflight = externalBasePreflight(req.Layout, stack, sel)

	if req.Policy.Fetch == internal.SyncFetchEnabled {
		insp.FetchPlan = externalFetchPlan(req.Layout, stack, sel, req.NewMode, insp.Capabilities)
	}

	return insp
}

// externalEffectiveRoute derives this invocation's own effective route
// (§13.6 rule 4), mirroring internal's own checkout twin: a --continue of an
// already-persisted payload inherits ITS OWN route (PayloadNewMode, never
// re-derived from req.NewMode, which a continuation invocation need not even
// repeat); a fresh run's route is req.NewMode directly.
// externalArgvNeedsUpdateRefs is §16 rule 3b's argv-derived predicate,
// answered from mode and scope BEFORE any row is built, because the gate
// itself must sit above the fetch: the external pass-1 `scope=all` shapes are
// the only argv this feature ever publishes carrying `--update-refs`
// (`internal/cli/sync_helpers.go`'s unscoped rebase-arg construction, §9.3),
// so a SCOPED run (`--only`/`--from`) and every checkout run never need it.
func externalArgvNeedsUpdateRefs(req ExternalPlanInspectionRequest) bool {
	return !req.Policy.Scoped()
}

func externalEffectiveRoute(req ExternalPlanInspectionRequest, payload *internal.SyncRunState) string {
	if req.Continue && payload != nil {
		if internal.PayloadNewMode(payload) {
			return internal.RouteNewMode
		}
		return internal.RouteLegacy
	}
	if req.NewMode {
		return internal.RouteNewMode
	}
	return internal.RouteLegacy
}

// externalRequestedRoute is RebasePlanRequest.RequestedRoute: "" when the
// command line's own requested route equals the effective route, the
// requested route string otherwise.
func externalRequestedRoute(newMode bool, effective string) string {
	requested := internal.RouteLegacy
	if newMode {
		requested = internal.RouteNewMode
	}
	if requested == effective {
		return ""
	}
	return requested
}

// externalRouteTriggers is RebasePlanRequest.RouteTriggers: sorted Changed
// trigger names, mirroring internal's checkoutRouteTriggers exactly.
func externalRouteTriggers(changed map[string]bool) []string {
	names := make([]string, 0, len(changed))
	for _, name := range syncTriggerFlags {
		if changed[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// externalPreconditionGates evaluates the fresh-run, pre-lock precondition
// this invocation's own shipped executor would check before ever touching
// the filesystem for a run guard (spec §13.7a): syncCellRefusal's own
// verdict for this cell/verb is the single source of it, so this gate is a
// PROJECTION of that shipped ladder, never a second implementation of it. It
// is a silent no-op on --continue, which has already passed it by
// definition.
func externalPreconditionGates(req ExternalPlanInspectionRequest) []internal.PlanGateResult {
	if sentinel := req.resumableSentinel(); sentinel != nil {
		// §12.8b: a VALID guarded backup sentinel never reaches
		// syncCellRefusal, so the gate publishes the sentence the executing
		// twin raises for THIS verb instead of the shipped cell-4 one: the
		// plain-verb preservation sentence, and nothing at all on a
		// continuation, whose own flag validation the continuation gate
		// projects.
		if req.Continue {
			return nil
		}
		return []internal.PlanGateResult{{
			ID: "external-state-refused", Applies: true, Failed: true,
			Kind: internal.RefusalStateRefused,
			Detail: fmt.Sprintf(
				"a guarded sync was interrupted while recording state for %q; the previous sync state is preserved inside %s — use --continue to resume it, or --abort to discard it",
				req.Feature, req.Sentinel.Path),
		}}
	}
	if req.Continue {
		return nil
	}
	err := syncCellRefusal(syncVerbPlain, req.Feature, req.Layout, req.Classified)
	return []internal.PlanGateResult{{
		ID: "external-state-refused", Applies: true, Failed: err != nil,
		Kind:   internal.RefusalStateRefused,
		Detail: syncErrTextOrEmpty(err),
	}}
}

func syncErrTextOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func externalGatesFailed(gates []internal.PlanGateResult) bool {
	for _, g := range gates {
		if g.Applies && g.Failed {
			return true
		}
	}
	return false
}

// externalBasePreflight is I14, projected: it calls firstUnresolvedSelectedBase
// (sync.go), the shipped verifySelectedBasesLocally's own locator, so the
// document publishes the identical verdict the executing route itself would
// reach — never a second implementation of the locator.
func externalBasePreflight(layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection) internal.PlanBasePreflight {
	if sel.Policy.Fetch != internal.SyncFetchDisabled {
		return internal.PlanBasePreflight{}
	}
	entry, ref, found := firstUnresolvedSelectedBase(layout, stack, sel)
	if !found {
		return internal.PlanBasePreflight{Applies: true}
	}
	return internal.PlanBasePreflight{
		Applies: true, Failed: true, Entry: entry, Ref: ref,
		Detail: fmt.Sprintf("base %q for stack entry %q does not resolve locally; drop --no-fetch or fetch manually first", ref, entry),
	}
}

// externalStageFacts derives StageFacts for the --continue route: one
// disclosure row per Remaining entry. External's continuation model has no
// per-entry Git-level "stage" the way a checkout transaction does — every
// remaining entry's own next command is always a rebase, since external
// never pauses mid-entry between a completed rebase and a still-pending
// validation/push the way a checkout transaction's Stage can.
func externalStageFacts(remaining []string) []internal.PlanStageFact {
	facts := make([]internal.PlanStageFact, 0, len(remaining))
	for _, name := range remaining {
		autostash := false
		facts = append(facts, internal.PlanStageFact{
			Entry: name, Stage: "planned", NextCommand: "git rebase",
			RebaseIsNext: true, AutostashApplies: &autostash,
		})
	}
	return facts
}

// ---------------------------------------------------------------------------
// Guard-limit reconciliation (external analogue of internal's
// resolveCheckoutGuardLimits/resolveCheckoutLimitConflicts)
// ---------------------------------------------------------------------------

// originLimit mirrors internal's own unexported helper of the same name: the
// unarmed value ({nil, "none"}) whenever v is nil, the supplied origin
// otherwise.
func originLimit(v *int, origin string) internal.PlanGuardLimit {
	if v == nil {
		return internal.PlanGuardLimit{Value: nil, Origin: "none"}
	}
	return internal.PlanGuardLimit{Value: v, Origin: origin}
}

// externalPersistedGuarded mirrors internal's checkoutRecoveryIsGuarded for
// the external payload: guarded exactly at state_version 3.
func externalPersistedGuarded(payload *internal.SyncRunState) bool {
	return payload != nil && payload.StateVersion >= internal.SyncRunStateGuardedVersion
}

// resolveExternalLimit mirrors internal.resolveCheckoutLimit for the
// external route's own persisted carrier, a *internal.SyncRunState rather
// than a *internal.CheckoutTransaction.
func resolveExternalLimit(payload *internal.SyncRunState, guarded bool, persisted, supplied *int) internal.PlanGuardLimit {
	switch {
	case payload == nil:
		return originLimit(supplied, "flags")
	case guarded && persisted != nil:
		return originLimit(persisted, "persisted-transaction")
	case guarded:
		return originLimit(supplied, "flags-persisted-continuation")
	default:
		return originLimit(supplied, "flags-legacy-continuation")
	}
}

func resolveExternalGuardLimits(opts planGuardOptions, payload *internal.SyncRunState, sentinel *internal.GuardedLegacySentinel) internal.PlanGuardLimits {
	if payload == nil && sentinel != nil {
		// §12.8b/§13.2: the cell-4 recovery arm inherits or confirms the
		// sentinel's persisted effective limits, published with the SAME
		// origin an envelope publishes, because the intent block IS the
		// payload the interrupted setup had already committed to. A
		// different supplied value never re-arms the run: it becomes a rank
		// 7 conflict row below, with the persisted value still effective.
		return internal.PlanGuardLimits{
			PerEntry: sentinelLimit(sentinel.MaxReplayPerEntry, opts.MaxPerEntry),
			Total:    sentinelLimit(sentinel.MaxReplayTotal, opts.MaxTotal),
		}
	}
	guarded := externalPersistedGuarded(payload)
	var perEntryPersisted, totalPersisted *int
	if payload != nil {
		perEntryPersisted, totalPersisted = payload.MaxReplayPerEntry, payload.MaxReplayTotal
	}
	return internal.PlanGuardLimits{
		PerEntry: resolveExternalLimit(payload, guarded, perEntryPersisted, opts.MaxPerEntry),
		Total:    resolveExternalLimit(payload, guarded, totalPersisted, opts.MaxTotal),
	}
}

// sentinelLimit is §13.2's per-key table over a sentinel's intent block: a
// persisted value is effective with origin persisted-payload whether this
// invocation confirmed it, supplied a different one (a conflict row, decided
// below) or supplied none at all; an unpersisted key falls back to this
// invocation's own flag.
func sentinelLimit(persisted, supplied *int) internal.PlanGuardLimit {
	if persisted != nil {
		return originLimit(persisted, "persisted-payload")
	}
	return originLimit(supplied, "flags")
}

func externalLimitConflict(key string, persisted *int, persistedOrigin string, supplied *int) *internal.PlanGuardLimitConflict {
	if supplied == nil || persisted == nil || *supplied == *persisted {
		return nil
	}
	return &internal.PlanGuardLimitConflict{Key: key, EffectiveValue: persisted, EffectiveOrigin: persistedOrigin, SuppliedValue: supplied}
}

func resolveExternalLimitConflicts(payload *internal.SyncRunState, sentinel *internal.GuardedLegacySentinel, opts planGuardOptions) []internal.PlanGuardLimitConflict {
	if payload == nil && sentinel != nil {
		var out []internal.PlanGuardLimitConflict
		if c := externalLimitConflict("max_replay_per_entry", sentinel.MaxReplayPerEntry, "persisted-payload", opts.MaxPerEntry); c != nil {
			out = append(out, *c)
		}
		if c := externalLimitConflict("max_replay_total", sentinel.MaxReplayTotal, "persisted-payload", opts.MaxTotal); c != nil {
			out = append(out, *c)
		}
		return out
	}
	if !externalPersistedGuarded(payload) {
		return nil
	}
	var out []internal.PlanGuardLimitConflict
	if c := externalLimitConflict("max_replay_per_entry", payload.MaxReplayPerEntry, "persisted-transaction", opts.MaxPerEntry); c != nil {
		out = append(out, *c)
	}
	if c := externalLimitConflict("max_replay_total", payload.MaxReplayTotal, "persisted-transaction", opts.MaxTotal); c != nil {
		out = append(out, *c)
	}
	return out
}

// externalValidationIdentity is RebasePlanRequest.Validation for the
// external route (§15): external never accepts a --test flag (that flag is
// checkout-only), so its effective command is always config's own
// TestCommand — the fresh route's own frozen-at-birth value, or the
// persisted payload's TestCommand on --continue, mirroring
// handleScopedSyncContinue's/runScopedSync's own "config today, frozen
// henceforth" rule (§7.7).
func externalValidationIdentity(payload *internal.SyncRunState, sentinel *internal.GuardedLegacySentinel, isContinue bool) internal.PlanValidationIdentity {
	command := internal.LoadConfig().TestCommand
	source := "config"
	switch {
	case isContinue && payload != nil:
		command = payload.TestCommand
		source = "persisted-transaction"
	case isContinue && sentinel != nil:
		// §19.2's recovery arm: the frozen validation command is the
		// sentinel's own intent block, never today's config.
		command = sentinel.TestCommand
		source = "persisted-transaction"
	}
	if command == "" {
		return internal.PlanValidationIdentity{Applies: false, Source: "none"}
	}
	return internal.PlanValidationIdentity{Applies: true, Command: command, Source: source, Digest: internal.ValidationDigest(command)}
}

// externalContinuationGate is RebasePlanRequest.ContinuationGate (§13.7a): a
// read-only projection of handleScopedSyncContinue's own dispatch gates,
// never a second implementation of them — the detail sentence comes from
// calling the shipped syncContinueMismatches directly.
func externalContinuationGate(req ExternalPlanInspectionRequest, state internal.ExternalPlanState, payload *internal.SyncRunState, sentinel *internal.GuardedLegacySentinel, stack internal.Stack) internal.PlanContinuationGate {
	policy, changed, push := req.Policy, req.Changed, req.Push
	if payload == nil {
		if !req.Continue {
			return internal.PlanContinuationGate{}
		}
		if sentinel == nil {
			// A continuation with no persisted subject at all: the gate
			// publishes the SHIPPED refusal its executing twin raises, so the
			// rows-less document always names its own cause (§13.7a rule 4).
			// The cell ladder answers first, exactly as the dispatch consults
			// it first; where it stays silent — cells 1 and 7, whose legacy
			// state this route has no payload for — the shipped continuation
			// handler's own sentence is the cause.
			if err := syncCellRefusal(syncVerbContinue, req.Feature, req.Layout, req.Classified); err != nil {
				return internal.PlanContinuationGate{Applies: true, Failed: true, Axis: "state", Detail: err.Error()}
			}
			if !internal.HasSyncState(req.Layout.FeaturePath) {
				return internal.PlanContinuationGate{
					Applies: true, Failed: true, Axis: "state",
					Detail: "nothing to continue — no sync in progress",
				}
			}
			// A `.sync-state.yaml` the classifier decoded but this
			// invocation's own inspector could NOT establish as a resumable
			// subject — a symlinked artefact, one past the read cap, an
			// empty or undecodable document, a non-regular file — leaves the
			// continuation with nothing to filter its remaining work
			// against. RowsAvailable already answers "no" there, so the gate
			// must answer "no" too and say exactly what it saw: a rows-less
			// document that named no cause is the one shape §13.7a rule 4
			// forbids.
			if !externalLegacySubject(state) {
				return internal.PlanContinuationGate{
					Applies: true, Failed: true, Axis: "state",
					Detail: legacySubjectUnavailableDetail(req.Layout, state),
				}
			}
			return internal.PlanContinuationGate{Applies: true}
		}
		// §12.8b: the cell-4 interception owns this document's flag
		// validation, because I20 can no longer see it. The projection
		// raises the SHIPPED I20 sentence its executing twin would raise —
		// composed here, exactly as there, and never a new sentence.
		if syncTriggerFlagSupplied(changed) {
			return internal.PlanContinuationGate{
				Applies: true, Failed: true, Axis: "mismatch", Detail: errSyncModeFlagsNeedV2,
			}
		}
		return internal.PlanContinuationGate{Applies: true}
	}
	if err := syncContinueMismatches(payload, policy, changed, push); err != nil {
		return internal.PlanContinuationGate{Applies: true, Failed: true, Axis: "mismatch", Detail: err.Error()}
	}
	for _, name := range payload.Selected {
		if !internal.HasBranch(stack, name) {
			return internal.PlanContinuationGate{
				Applies: true, Failed: true, Axis: "selection",
				Detail: fmt.Sprintf("selected stack entry %q no longer exists in stack; use --abort", name),
			}
		}
	}
	return internal.PlanContinuationGate{Applies: true}
}

// externalRowsAvailable is RebasePlanRequest.RowsAvailable for this route
// (§13.7a rule 4): the fresh arm needs stack/order/selection resolved; a
// continuation additionally needs a persisted subject to filter its
// remaining work against — the v2/v3 payload of cells 5/7, or the VALID
// guarded-legacy sentinel of the cell-4 recovery arm, whose intent block is
// the payload the interrupted setup had already committed to (§12.8b,
// §19.2). A continuation with neither is the one rows-less continuation
// shape, and its cause is published by the state gate above.
func externalRowsAvailable(insp ExternalPlanInspection, payload *internal.SyncRunState, sentinel *internal.GuardedLegacySentinel) bool {
	resolved := insp.StackErr == nil && insp.SortErr == nil && insp.SelectionErr == nil
	if !resolved {
		return false
	}
	if insp.Continue {
		return payload != nil || sentinel != nil || externalLegacySubject(insp.State)
	}
	return true
}

// legacySubjectUnavailableDetail names, precisely, why the legacy artefact
// beside this continuation is not a resumable subject: the path, the
// presence token this invocation measured, the unreadable reason where the
// presence has one, and the underlying error verbatim where the inspector
// captured one. It invents no verdict — every field is a value
// InspectExternalPlanState already published in state.files — so an operator
// reading blockers[] and state.files sees the same fact twice, never two
// different stories.
func legacySubjectUnavailableDetail(layout externalSyncLayout, state internal.ExternalPlanState) string {
	legacy := state.Files.LegacyState
	path := internal.SyncStatePath(layout.FeaturePath)
	detail := fmt.Sprintf("sync state at %s cannot be resumed: it is %s", path, legacyPresenceWord(legacy))
	if legacy.Err != nil {
		detail += fmt.Sprintf(": %v", legacy.Err)
	}
	return detail + "; inspect it and use --abort to discard it"
}

// legacyPresenceWord renders one presence/reason pair as the phrase the
// detail sentence needs, including the marker case — a backup sentinel this
// invocation could not verify is not a legacy subject either.
func legacyPresenceWord(legacy internal.PlanLegacySyncStateFile) string {
	switch legacy.Presence {
	case internal.PlanPresenceSymlink:
		return "a symlink, not a regular state document"
	case internal.PlanPresenceAbsent:
		return "absent"
	case internal.PlanPresenceNotApplicable:
		return "not applicable to this route"
	case internal.PlanPresenceUnreadable:
		if legacy.UnreadableReason != nil {
			return fmt.Sprintf("unreadable (%s)", *legacy.UnreadableReason)
		}
		return "unreadable"
	default:
		if legacy.MarkerPresent {
			return "an interrupted guarded setup's marker rather than a resumable legacy state"
		}
		return "not a resumable legacy state"
	}
}

// externalLegacySubject reports whether this continuation's subject is a real
// (non-sentinel) legacy `.sync-state.yaml` — the cell-7 arm of §19.2, whose
// resumed subject is the value the classifier already decoded. Its Completed
// list is exactly what RemainingRebaseEntries reads on the legacy route, so
// such a continuation has real remaining rows to publish and is never
// rows-less.
func externalLegacySubject(state internal.ExternalPlanState) bool {
	legacy := state.Files.LegacyState
	return state.Applicable && legacy.Presence == internal.PlanPresenceReadable &&
		legacy.State != nil && !legacy.MarkerPresent
}

// externalFetchPlan enumerates every repo the described route's own fetch
// loop would touch — the whole stack's repos on a legacy route, the
// selection's own repos on a new-mode route — and, per context, its measured
// fetch effect before any fetch runs. It then runs the SAME shared
// suppression ladder internal.checkoutFetchPlan runs
// (internal.ResolveFetchSuppression), so both modes model every §11.2 cause
// with identical row shapes and identical freshness tokens: cause 4
// (fetch-context-indeterminate, reachable here because an external run can
// enumerate several contexts), cause 5 (submodule-reach-indeterminate) and
// cause 6 (local-branch-checked-out). A context whose effect could not be
// established leaves Effect nil, which is disclosure — never a measured
// hazard — but does make a multi-context plan indeterminate, exactly as
// §11.4 requires.
func externalFetchPlan(layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection, newMode bool, caps internal.GitCapabilities) internal.PlanFetchPlan {
	var repos map[string]string
	if newMode {
		sub := internal.Stack{Branches: selectedRealEntries(stack, sel)}
		repos = internal.UniqueRepos(sub, layout.FeaturePath)
	} else {
		repos = internal.UniqueRepos(stack, layout.FeaturePath)
	}
	tokens := make([]string, 0, len(repos))
	for token := range repos {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)

	plan := internal.PlanFetchPlan{Applies: true}

	// Pass one: establish every context's roots, so the holder inventories
	// below can be built ONCE PER CANONICAL COMMON DIR through
	// internal.BuildPlanHolderIndex — the single holder-index producer — and
	// never once per repo token. Two tokens that are two linked worktrees of
	// one repository share one `git worktree list`.
	type fetchContext struct {
		ctx       internal.PlanFetchContext
		root      string
		commonDir string
		measured  bool
	}
	contexts := make([]fetchContext, 0, len(tokens))
	ids := internal.PlanContextIdentities{}
	need := make([]string, 0, len(tokens))
	for _, token := range tokens {
		root := token
		source := "entry-repo"
		if root == "" {
			root = repos[token]
			source = "worktree"
			if root == "" {
				root = layout.WorktreesRoot
				source = "workspace-repo-root"
			}
		}
		fc := fetchContext{
			ctx: internal.PlanFetchContext{
				RepoToken: token, Root: root, Source: source,
				Candidates: []internal.PlanFetchCandidate{{ContextRoot: root, ContextSource: source}},
			},
			root: root,
		}
		if _, commonDir, err := internal.MeasureContextRoots(root); err == nil {
			fc.ctx.CommonDir = commonDir
			fc.commonDir, fc.measured = commonDir, true
			if _, seen := ids[root]; !seen {
				ids[root] = internal.PlanContextIdentity{ContextID: root, RepoRoot: root, CommonDir: commonDir}
				need = append(need, root)
			}
		}
		contexts = append(contexts, fc)
	}
	holderIndex := internal.BuildPlanHolderIndex(ids, need)

	for _, fc := range contexts {
		if fc.measured {
			// §16 rule 3a: a host below 2.26 cannot produce the ordered
			// inventory at all, so it is never asked for — the capability
			// gate has already refused the document above this point.
			var inv internal.GitConfigInventory
			if caps.CapConfigShowScope {
				inv = internal.ProbeGitConfigInventory(fc.root)
			}
			effect := internal.ResolveFetchEffect(inv, fc.root, fc.commonDir, caps, holderIndex.ByContext[fc.root])
			fc.ctx.Effect = &effect
		}
		plan.Contexts = append(plan.Contexts, fc.ctx)
	}
	sup := internal.ResolveFetchSuppression(plan.Contexts)
	plan.Suppressed, plan.Controlled, plan.Blockers = sup.Suppressed, sup.Controlled, sup.Blockers
	return plan
}

// externalPushFacts is RebasePlanRequest.PushFacts for the external route,
// generalizing internal.checkoutPushFacts (exactly one repo) into N
// contexts, one per repo the selection's own materialized entries touch. It
// is a complete no-op, never touching Git, whenever this invocation does not
// intend to push at all.
func externalPushFacts(push bool, layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection) internal.PlanPushFacts {
	if !push {
		return internal.PlanPushFacts{}
	}
	sub := internal.Stack{Branches: selectedRealEntries(stack, sel)}
	repos := internal.UniqueRepos(sub, layout.FeaturePath)
	if len(repos) == 0 {
		return internal.PlanPushFacts{}
	}
	tokens := make([]string, 0, len(repos))
	for token := range repos {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)

	caps, _, _ := internal.ProbeGitCapabilities()
	facts := internal.PlanPushFacts{
		Applies: true, Phase: "plan-point",
		Identities:      internal.PlanContextIdentities{},
		Materialization: map[string]string{},
		Remotes:         map[string]internal.PlanPushRemoteFacts{},
	}
	for _, token := range tokens {
		root := token
		if root == "" {
			root = repos[token]
		}
		if root == "" {
			continue
		}
		// The ONE ordered inventory this push context reads (§14.1a rule 7,
		// §22.27a): ResolvePushContext's remote ladder and the mapping read
		// below both consume this single value, and MeasurePushRemoteFacts
		// never probes again. A host below 2.26 asks for nothing.
		var inv internal.GitConfigInventory
		if caps.CapConfigShowScope {
			inv = internal.ProbeGitConfigInventory(root)
		}
		_, commonDir, measureErr := internal.MeasureContextRoots(root)
		if measureErr != nil {
			continue
		}
		ctx, identity, remoteName, err := internal.ResolvePushContext(root, "workspace-repo-root", "materialized", inv, caps)
		if err != nil {
			continue
		}
		remoteFacts := internal.MeasurePushRemoteFacts(ctx.ContextID, commonDir, remoteName, inv, caps)
		facts.Contexts = append(facts.Contexts, ctx)
		facts.Identities[identity.ContextID] = identity
		facts.Materialization[token] = ctx.Materialization
		facts.Remotes[token] = remoteFacts
	}
	return facts
}

// externalPlanRequest is the shared assembly step runExternalPlan needs:
// every RebasePlanRequest member InspectExternalPlan's own measurement pass
// already settled, mirroring internal.checkoutPlanRequest's own assembly
// field-for-field for the external route.
func externalPlanRequest(req ExternalPlanInspectionRequest, insp ExternalPlanInspection, fetchOutcome internal.PlanFetchOutcome) internal.RebasePlanRequest {
	payload := req.Classified.Payload
	sentinel := req.resumableSentinel()
	route := externalEffectiveRoute(req, payload)
	rowsAvailable := externalRowsAvailable(insp, payload, sentinel)

	var stackPtr *internal.Stack
	if rowsAvailable {
		s := insp.Stack
		stackPtr = &s
	}

	push := req.Push
	pushSource := "flag"
	switch {
	case req.Continue && payload != nil:
		push = payload.Push
		pushSource = "persisted-transaction"
	case req.Continue && sentinel != nil:
		// The recovery arm's intent block is the run's own committed
		// provenance, exactly as a payload is on the envelope arm.
		push = sentinel.Push
		pushSource = "persisted-transaction"
	}

	return internal.RebasePlanRequest{
		Layout:                    planLayout(req.Layout),
		Mode:                      internal.ModeExternal,
		Feature:                   req.Feature,
		Workspace:                 externalWorkspace(req.Ws),
		Route:                     route,
		RequestedRoute:            externalRequestedRoute(req.NewMode, route),
		RouteTriggers:             externalRouteTriggers(req.Changed),
		Invocation:                "plan-only",
		Policy:                    req.Policy,
		PolicyFetchDefaultApplied: !req.Changed["fetch"] && !req.Changed["no-fetch"],
		Push:                      push,
		PushSource:                pushSource,
		Guard:                     req.checkoutGuard(),
		Limits:                    insp.Limits,
		LimitConflicts:            insp.LimitConflicts,
		Validation:                externalValidationIdentity(payload, sentinel, req.Continue),
		Approve:                   req.Opts.Approve,
		Stack:                     stackPtr,
		Order:                     insp.Order,
		SortErr:                   insp.SortErr,
		StackErr:                  insp.StackErr,
		Selection:                 insp.Selection,
		SelectionResolved:         insp.SelectionResolved,
		SelectionErr:              insp.SelectionErr,
		RowsAvailable:             rowsAvailable,
		Continue:                  req.Continue,
		Remaining:                 insp.Remaining,
		StageFacts:                insp.StageFacts,
		Changed:                   req.Changed,
		ContinuationGate:          externalContinuationGate(req, insp.State, payload, sentinel, insp.Stack),
		Fetch:                     fetchOutcome,
		FetchPlan:                 insp.FetchPlan,
		PushFacts:                 internal.RefreshPushTrackingRefs(externalPushFacts(push, req.Layout, insp.Stack, insp.Selection), fetchOutcome),
		BasePreflight:             externalPreflightWithCompletionGate(req, insp, push, rowsAvailable),
		Version:                   insp.Version,
		Capabilities:              insp.Capabilities,
		ExternalState:             insp.State,
		Gates:                     insp.Gates,
	}
}

// externalBasePreflight is insp.BasePreflight, plus the ONE cell §7.1's rank-4
// rule 2 adds for the external completion gate (§13.5, §13.7a rule 15,
// §22.12b (iii)).
//
// The gate `Sync incomplete; stale stack edges remain:` is a POSTCONDITION:
// it runs AFTER the executor's rebases, so on any document that still has a
// rebase row the answer this invocation would give is not yet decided and the
// stale edge is published as the row's own `ancestry` and nothing more. In
// exactly one cell — a ROWS-LESS PUSH-ONLY continuation, where no rebase of
// this invocation can change the answer — the projection is total, and the
// document publishes it as rank 4 `preflight-refused` with the shipped
// sentences byte for byte.
//
// staleStackEdgesFiltered is evaluated in no other cell of this route.
func externalPreflightWithCompletionGate(req ExternalPlanInspectionRequest, insp ExternalPlanInspection, push, rowsAvailable bool) internal.PlanBasePreflight {
	if insp.BasePreflight.Failed || !req.Continue || !push {
		return insp.BasePreflight
	}
	if rowsAvailable && len(insp.Remaining) > 0 {
		return insp.BasePreflight // a rebase row remains: the postcondition is not yet decided
	}
	stale := staleStackEdgesFiltered(req.Layout.WorktreesRoot, insp.Stack, nil)
	if len(stale) == 0 {
		return insp.BasePreflight
	}
	detail := "Sync incomplete; stale stack edges remain:"
	for _, edge := range stale {
		detail += "\n  [!] " + edge
	}
	return internal.PlanBasePreflight{Applies: true, Failed: true, Detail: detail}
}

// externalPlanArgs bundles the measurement inputs both runExternalPlan and
// buildGuardedExternalPlan share, so a guarded execution's own admission
// plan (built to feed internal.EvaluatePlanGuard) and its --plan twin are
// always the product of the identical pipeline.
type externalPlanArgs struct {
	Feature  string
	Layout   externalSyncLayout
	Ws       internal.Workspace
	Policy   internal.SyncRunPolicy
	NewMode  bool
	Push     bool
	Verbose  bool
	Changed  map[string]bool
	Opts     planGuardOptions
	State    internal.SyncExternalState
	Continue bool

	// Sentinel is the §19.2 dispatch's own cell-4 sentinel view, threaded
	// straight into ExternalPlanInspectionRequest.Sentinel. It is the zero
	// value everywhere else.
	Sentinel internal.GuardedLegacySentinelView

	// PersistedGuarded is threaded straight into
	// ExternalPlanInspectionRequest.PersistedGuarded (§13.6 rule 4d). It is
	// false on every fresh route and on the plan-only route's own
	// measurement (a --plan never adjudicates; it only describes), and true
	// only when a guarded executor is resuming an already-guarded persisted
	// run.
	PersistedGuarded bool
}

// buildExternalPlan runs InspectExternalPlan, fetches wherever the described
// route's own policy would (writing every prose/child byte to prose, which
// callers already route to cmd.ErrOrStderr() on the plan-only route or to
// the shipped stdout/stderr pair on a guarded execution's own admission
// pass), and returns the fully built document. It never renders and never
// evaluates the guard: both are the caller's own responsibility.
func externalPlanInspectionRequest(args externalPlanArgs) ExternalPlanInspectionRequest {
	return ExternalPlanInspectionRequest{
		Feature: args.Feature, Layout: args.Layout, Ws: args.Ws, Opts: args.Opts, Policy: args.Policy,
		NewMode: args.NewMode, Push: args.Push, Verbose: args.Verbose, Changed: args.Changed,
		Continue: args.Continue, Classified: args.State, PersistedGuarded: args.PersistedGuarded,
		Sentinel: args.Sentinel,
	}
}

// inspectExternalPlanFor is the ONE InspectExternalPlan call an invocation
// makes (§10.7 rule 1a, §16.1): every consumer below — the shipped I14
// refusal composer, the guard seam, the document build and the executor —
// reads the value it returns instead of re-probing. It is the reason a
// guarded route's own prelude no longer re-loads the stack or re-sorts it.
func inspectExternalPlanFor(args externalPlanArgs) ExternalPlanInspection {
	return InspectExternalPlan(externalPlanInspectionRequest(args))
}

// buildExternalPlanFrom performs the fetch (where the described route
// fetches) and the document build over an ALREADY-produced inspection, so
// no caller inspects twice.
func buildExternalPlanFrom(args externalPlanArgs, insp ExternalPlanInspection, prose io.Writer) (internal.RebasePlan, internal.RebasePlanRequest, error) {
	req := externalPlanInspectionRequest(args)

	var fetchOutcome internal.PlanFetchOutcome
	if !args.Continue && args.Policy.Fetch == internal.SyncFetchEnabled && insp.FetchPlan.Applies &&
		len(insp.FetchPlan.Blockers) == 0 && externalRowsAvailable(insp, args.State.Payload, req.resumableSentinel()) &&
		!externalGatesFailed(insp.Gates) && !insp.BasePreflight.Failed {
		if args.NewMode {
			fetchOutcome = fetchScopedReposTo(prose, prose, args.Layout, insp.Stack, insp.Selection, args.Verbose, insp.FetchPlan)
		} else {
			fetchOutcome = fetchStackReposTo(prose, prose, args.Layout, insp.Stack, args.Verbose, insp.FetchPlan)
		}
	}

	planReq := externalPlanRequest(req, insp, fetchOutcome)
	plan, err := internal.BuildRebasePlan(planReq)
	return plan, planReq, err
}

// buildExternalPlan is the plan-only route's one-shot inspect-then-build.
func buildExternalPlan(args externalPlanArgs, prose io.Writer) (internal.RebasePlan, internal.RebasePlanRequest, ExternalPlanInspection, error) {
	insp := inspectExternalPlanFor(args)
	plan, planReq, err := buildExternalPlanFrom(args, insp, prose)
	return plan, planReq, insp, err
}

// runExternalPlan is the external route's plan-only dispatch (spec §13.7a):
// it never mutates, describes either the fresh or the --continue route
// depending on cont, and fetches wherever that described route's own policy
// would fetch — writing every fetch/prose byte to cmd.ErrOrStderr(), never
// cmd.OutOrStdout(), so stdout carries the document alone.
func runExternalPlan(cmd *cobra.Command, feature string, layout externalSyncLayout, ws internal.Workspace, policy internal.SyncRunPolicy, newMode, push, verbose bool, changed map[string]bool, opts planGuardOptions, state internal.SyncExternalState, cont bool) error {
	prose := cmd.ErrOrStderr()
	printSyncModeHeaderTo(prose, policy)

	plan, _, _, err := buildExternalPlan(externalPlanArgs{
		Feature: feature, Layout: layout, Ws: ws, Policy: policy, NewMode: newMode,
		Push: push, Verbose: verbose, Changed: changed, Opts: opts, State: state, Continue: cont,
		Sentinel: externalPlanSentinelView(layout, feature, state),
	}, prose)
	if err != nil {
		return err
	}
	return renderPlanDocument(cmd, plan, opts.JSON)
}

// externalPlanSentinelView is the plan route's own ONE read of the §13.6
// rule 2c sentinel view, taken only on cell 4 — the single cell §19.2 lets
// anything consult it — so a --plan describes the same subject its executing
// twin would resume. It adjudicates nothing: the value is threaded into the
// inspection exactly as the executing dispatch threads its own already-read
// view, and no other cell reads a sentinel at all.
func externalPlanSentinelView(layout externalSyncLayout, feature string, state internal.SyncExternalState) internal.GuardedLegacySentinelView {
	if state.Cell != 4 {
		return internal.GuardedLegacySentinelView{}
	}
	return internal.InspectGuardedLegacySentinel(layout.FeaturePath, feature)
}

// buildGuardedExternalPlan is a guarded execution's own pre-mutation
// admission pass (spec §4.6, §7): the identical InspectExternalPlan +
// externalPlanRequest + BuildRebasePlan pipeline runExternalPlan itself
// uses, so a guarded execution is refused by exactly the verdict its own
// --plan twin would report, followed by internal.EvaluatePlanGuard's own
// admission predicate. Unlike the plan-only route it writes its prose
// straight to the shipped stdout/stderr pair (out/errw), since a guarded
// execution that passes admission goes on to print the identical shipped
// bytes an unguarded run would. The returned RebasePlanRequest is
// newPlanGuardRun's own frozen-at-birth request: a guarded executor never
// rebuilds it a second time.
func buildGuardedExternalPlan(out, errw io.Writer, args externalPlanArgs, insp ExternalPlanInspection) (internal.RebasePlan, internal.RebasePlanRequest, error) {
	plan, planReq, err := buildExternalPlanFrom(args, insp, errw)
	if err != nil {
		return internal.RebasePlan{}, planReq, err
	}
	if err := syncSnapshotBarrier(args.Layout.FeaturePath); err != nil {
		return plan, planReq, err
	}
	if err := internal.EvaluatePlanGuard(plan, args.Opts.checkoutGuardPersisted(args.PersistedGuarded)); err != nil {
		return plan, planReq, err
	}
	return plan, planReq, nil
}

// legacySelectionFromOrder is the FULL-STACK legacy universe of §13.3: every
// entry of the current stack, in this invocation's one TopoSort order. It is
// the selection the cell-4 recovery arm resumes, because a legacy run never
// narrowed its universe in the first place — the shipped legacy executor
// selects every name — and the sentinel's own intent block is a full-stack
// universe by construction (§13.6 rule 2c).
func legacySelectionFromOrder(stack internal.Stack, order []internal.StackEntry, policy internal.SyncRunPolicy, feature string, mode internal.WorkspaceMode) (internal.SyncSelection, error) {
	return internal.ResolveSyncSelectionFromOrder(stack, order, policy, internal.SyncSelectionOpts{
		Mode:    mode,
		NewMode: false,
		Feature: feature,
	})
}

// sentinelRemaining projects the cell-4 recovery arm's remaining work: the
// sentinel's own PendingIntent, in the selection's execution order, keeping
// only names the current stack still carries. A name the operator removed
// from stack.yaml between the crash and the resume is silently absent from
// the remaining set — it is not a rebase this invocation can perform — and
// is never invented back into the plan.
func sentinelRemaining(sentinel *internal.GuardedLegacySentinel, sel internal.SyncSelection) []string {
	pending := make(map[string]bool, len(sentinel.PendingIntent))
	for _, name := range sentinel.PendingIntent {
		pending[name] = true
	}
	out := make([]string, 0, len(sentinel.PendingIntent))
	for _, e := range sel.Entries {
		if pending[e.Name] {
			out = append(out, e.Name)
		}
	}
	return out
}
