package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
)

// ============================================================================
// rebase-plan-guard — the plan-guard surface (§3.2, §13.7, §13.7a)
//
// This file is the home of the plan-guard surface: CheckoutPlanGuard,
// PlanFetchRepoResult, PlanFetchOutcome and PlanFetchContext (the carriers
// this feature's checkout/state plumbing already needed); PlanGuardRefusalError,
// PlanWriters, PlanFetchPlan, PlanGateResult, PlanGuardLimits,
// PlanValidationIdentity and PlanStageFact (the additional carriers §9.1a's
// group-5 gap deferred here); and EvaluatePlanGuard, RevalidatePlanGuardEntry,
// the CheckoutPlanInspection* family, InspectCheckoutPlan, PlanCheckoutRebase,
// BuildCheckoutRebasePlan and BuildCheckoutContinuationPlan — the checkout
// plan-only route's own producers and the guard's two evaluators. Nothing in
// this file imports github.com/spf13/cobra or internal/cli (§19.1, §19.2):
// package cli projects its own flag/writer types into the carriers below at
// the call site, never the reverse.
// ============================================================================

// CheckoutPlanGuard is the internal-owned value the checkout executor
// receives for its plan-guard control flags, so no cli type crosses the
// package boundary and internal never imports Cobra (§19.1, §19.2).
type CheckoutPlanGuard struct {
	Plan        bool
	JSON        bool
	MaxPerEntry *int
	MaxTotal    *int
	Approve     string
	Present     map[string]bool

	// PersistedGuarded is NOT a control flag: it is set only by the checkout
	// --continue dispatch, from TransactionGuarded(tx), so a guarded run's
	// persisted limits stay load-bearing on a flagless resume (§13.6 rule 4d).
	// The zero value always leaves it false, and no external route and no
	// fresh run ever sets it.
	PersistedGuarded bool
}

// Armed reports whether this invocation armed something: at least one limit
// is present, or a token was supplied.
func (g CheckoutPlanGuard) Armed() bool {
	return g.MaxPerEntry != nil || g.MaxTotal != nil || g.Approve != ""
}

// Guarded reports whether the guarded route applies: this invocation armed a
// limit or token, or the persisted transaction was already guarded — and this
// is not a plan-only invocation.
func (g CheckoutPlanGuard) Guarded() bool {
	return (g.Armed() || g.PersistedGuarded) && !g.Plan
}

// PlanFetchRepoResult is one fetch context's observed outcome. The
// writer-taking fetch helpers RETURN it — never void, never a bare bool — so
// fetch.repos[] is a record of what happened, not a re-derivation. It has
// exactly these eight fields, one per §4.4a row member.
type PlanFetchRepoResult struct {
	RepoToken         string               // StackEntry.Repo token; "" is a value (§4.4a)
	ContextRoot       string               // the directory the child ran in, or would have
	ContextCommonDir  string               // the PRE-MEASURED --git-common-dir; "" ⇒ null
	ContextSource     string               // worktree | entry-repo | process-cwd | workspace-repo-root
	ContextCandidates []PlanFetchCandidate // NEVER nil; [] when the enumeration produced none
	Effect            *PlanFetchEffect     // enumerated BEFORE the fetch, copied from the context
	Attempted         bool                 // a git fetch child really ran
	OK                bool                 // the child's exit status; meaningful ONLY when Attempted
}

// PlanFetchOutcome is what this invocation's plan-path fetch observed, handed
// to the builder so the document's fetch/freshness facts describe its own
// fetch. One shape serves both modes: checkout produces exactly one row with
// RepoToken "" and ContextSource workspace-repo-root.
type PlanFetchOutcome struct {
	Applies    bool                  // the described route's effective policy fetches at all
	Attempted  bool                  // at least one Repos row has Attempted true
	Suppressed string                // "" or EXACTLY ONE §11.2 cause token
	Repos      []PlanFetchRepoResult // never nil
}

// PlanFetchContext is ONE enumerated fetch context with its measured effect,
// produced BEFORE any network call. It is the only carrier of an effect into
// a fetch and out of it.
type PlanFetchContext struct {
	RepoToken  string               // "" on the checkout route
	Root       string               // the directory the fetch child will run in
	CommonDir  string               // measured --git-common-dir; "" when unmeasurable
	Source     string               // worktree | entry-repo | process-cwd | workspace-repo-root
	Effect     *PlanFetchEffect     // nil exactly when it could not be established
	Candidates []PlanFetchCandidate // the §4.4a disclosure rows for this context
}

// ============================================================================
// PlanGuardRefusalError (§6.4) — the guarded-execution refusal error
// ============================================================================

// PlanGuardRefusalError is the error EvaluatePlanGuard and
// RevalidatePlanGuardEntry return in place of proceeding: a guarded
// invocation's own refusal, carrying no "plan-guard: " prefix in either
// field — the caller's own message framing owns that, never this type.
type PlanGuardRefusalError struct {
	Kind           string // the RefusalKind (or ControlledPathBlocker) token, verbatim
	Detail         string // the human sentence, verbatim; never itself prefixed
	StatePreserved bool   // true once THIS invocation has rebased an entry, or reclaimed a run guard
}

// Error composes exactly "<kind>: [state-preserved: ]<detail>" (§6.4): the
// state-preserved marker is present, in that exact position, iff
// StatePreserved is true, and absent otherwise. Neither branch prepends
// "plan-guard: " — that framing, where wanted, is the caller's own.
func (e *PlanGuardRefusalError) Error() string {
	if e.StatePreserved {
		return fmt.Sprintf("%s: state-preserved: %s", e.Kind, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Detail)
}

// ============================================================================
// PlanWriters (§13.7) — the plan-only checkout route's sole writer surface
// ============================================================================

// PlanWriters is PlanCheckoutRebase's only writer parameter: Prose is where a
// fetch's "Fetching ... done/failed" line and any other human-readable, non-
// document prose goes. A plan-only route never writes a document byte to it
// and never touches os.Stdout/os.Stderr directly (§13.7 rule 1); package cli
// decides where Prose is ultimately connected.
type PlanWriters struct {
	Prose io.Writer
}

// ============================================================================
// PlanFetchPlan (§11.1, §11.2, §11.4-§11.6) — the pre-fetch enumeration a
// controlled route resolves, effect and all, before it ever decides to fetch
// ============================================================================

// PlanFetchPlan is the pre-fetch enumeration InspectCheckoutPlan resolves:
// every candidate fetch context and its measured effect, and whether that
// effect itself suppresses the fetch (ranks 5.05/5.06) before any network
// call. Applies is false only where the route cannot reach a fetch decision
// at all (a rows-less document, §11.2 cause 2); Contexts is never nil once
// Applies is true, and is exactly one entry — context_source:
// workspace-repo-root — on every checkout route (§11.1).
type PlanFetchPlan struct {
	Applies    bool
	Contexts   []PlanFetchContext
	Blockers   []PlanBlocker
	Controlled []ControlledPathBlocker
	Suppressed string // "" or one of §11.2's causes 4-6 tokens
}

// ============================================================================
// PlanGateResult (§13.7, §13.7a) — one pre-fetch/pre-mutation gate's verdict
// ============================================================================

// PlanGateResult is one ordered §13.7/§13.7a gate's outcome: ID is a
// caller-local, never-published label for tests and call-site clarity (the
// document itself carries no gate IDs); Applies is false when this gate is
// not on the invocation's own route at all; Failed, meaningful only when
// Applies, is the gate's own verdict; Kind/Entry/Detail describe the
// resulting blocker exactly as §7.1 would publish it (Entry "" for a
// document-level fact); Controlled is never nil and carries every
// controlled-path token this specific gate's failure represents, even where
// the gate's own Kind stays the zero value because the described route never
// reflects the fact as a blocker at all (§4.6's independence rule).
type PlanGateResult struct {
	ID         string
	Applies    bool
	Failed     bool
	Kind       RefusalKind
	Entry      string
	Detail     string
	Controlled []ControlledPathBlocker
}

// ============================================================================
// PlanGuardLimits (§2.17.1) — the effective, already-reconciled guard limits
// ============================================================================

// PlanGuardLimits is RebasePlanRequest.Limits: the effective
// max-replay-per-entry/max-replay-total pair a controlled invocation
// resolved ABOVE BuildRebasePlan (flag vs. persisted-transaction
// reconciliation happens in InspectCheckoutPlan, never in the builder
// itself), in the same {value, origin} shape guard.limits publishes. Both
// members at their zero value ({nil, ""}) is indistinguishable from "no
// limit was ever armed" — buildGuard treats a nil Origin exactly as "none".
type PlanGuardLimits struct {
	PerEntry PlanGuardLimit
	Total    PlanGuardLimit
}

// ============================================================================
// PlanValidationIdentity (§15) — intent.validation's identity inputs
// ============================================================================

// PlanValidationIdentity is RebasePlanRequest.Validation: the test-command
// validation identity §15 requires the document to publish without ever
// publishing the raw command itself. Command is intentionally never a
// RebasePlan member — only Digest, its SHA-256, is; Applies false leaves
// Digest "" and Command is then meaningless (never read by the builder).
type PlanValidationIdentity struct {
	Applies bool
	Command string // NEVER published; Digest is derived from this and published instead
	Source  string
	Digest  string // 64-lower-hex SHA-256 of Command; "" iff !Applies
}

// ValidationDigest computes intent.validation.command_digest's own value —
// the 64-lower-hex SHA-256 of the raw test command — without ever handing
// the command itself back to a caller that might publish it. It is exported
// so package cli can populate PlanValidationIdentity.Digest without either
// side re-implementing SHA-256 hex encoding.
func ValidationDigest(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}

// ============================================================================
// PlanStageFact (§13.3, §13.7a) — one continuation entry's next-command facts
// ============================================================================

// PlanStageFact is one entries[]-scoped continuation disclosure row: what a
// --continue invocation's own shipped ladder would run next for one still-
// remaining entry, projected (never re-decided) from that ladder. Stage is
// the shipped stage token the entry is paused at; NextCommand is the exact
// argv the shipped continuation would issue next; RebaseIsNext is true when
// that command is itself a rebase (rather than, say, a plain checkout);
// StandaloneCheckout is true when the next command is a bare branch
// checkout with no rebase attached; AutostashApplies is nil where the
// autostash question does not arise for this entry's own next command.
type PlanStageFact struct {
	Entry              string
	Stage              string
	NextCommand        string
	RebaseIsNext       bool
	StandaloneCheckout bool
	AutostashApplies   *bool
}

// ============================================================================
// Checkout fetch-context enumeration (§11.1, §11.4-§11.6) — the single
// workspace-repo-root candidate every controlled checkout route resolves,
// and its own reach for ranks 5.05/5.06, BEFORE any network call.
// ============================================================================

// FetchSuppression is the per-plan suppression verdict of §11.2 causes 4-6:
// the cause token, the controlled-path tokens it publishes and the blockers
// it raises. It is produced by ONE shared ladder — ResolveFetchSuppression —
// so the external and checkout routes cannot diverge on which measured
// effect suppresses a fetch, nor on the exact row shape they publish for it.
type FetchSuppression struct {
	Suppressed string
	Controlled []ControlledPathBlocker
	Blockers   []PlanBlocker
}

// ResolveFetchSuppression is the ONE suppression ladder both modes run over
// an already-enumerated context list, in the fixed rank order 5.0 →
// 5.05 → 5.06 (§11.2 causes 4, 5, 6):
//
//   - cause 4, rank 5.0 fetch-context-indeterminate: two or more enumerated
//     contexts that do not collapse. Contexts collapse only when BOTH the
//     canonical absolute common dir AND the resolved effect agree (§11.4);
//     head_branch is disclosure and is excluded from the predicate. A single
//     candidate never reaches this rank, which is why the checkout route —
//     which resolves exactly one context — can never fire it;
//   - cause 5, rank 5.05 fetch-submodule-reach-indeterminate: unconditional
//     recursion (mode: yes) whose reach could not be bounded, on ANY
//     enumerated context;
//   - cause 6, rank 5.06 fetch-local-branch-checked-out: a positive refspec
//     covering refs/heads/** whose covered local-branch inventory is
//     non-empty or unknown, on ANY enumerated context.
//
// A context whose effect could not be established at all contributes to
// neither 5.05 nor 5.06 — an unmeasurable effect is not a measured hazard —
// but it does take part in the equivalence test of cause 4, where it cannot
// be shown to agree with anything and therefore makes a multi-context plan
// indeterminate, exactly as §11.4 requires ("one that cannot be shown to
// agree").
func ResolveFetchSuppression(contexts []PlanFetchContext) FetchSuppression {
	if len(contexts) == 0 {
		return FetchSuppression{}
	}
	if len(contexts) > 1 && !fetchContextsCollapse(contexts) {
		return FetchSuppression{
			Suppressed: "not-refreshed-context-indeterminate",
			Controlled: []ControlledPathBlocker{ControlledFetchContextIndeterminate},
			Blockers: []PlanBlocker{{
				Kind:   RefusalFetchContextIndeterminate,
				Detail: "the enumerated fetch contexts do not agree on the repository a single fetch would refresh; scope the run with a determinate --only/--from, make the contexts agree on their common dir and resolved effect, or use --no-fetch",
			}},
		}
	}
	for _, ctx := range contexts {
		if ctx.Effect == nil {
			continue
		}
		if fetchSubmoduleReachIndeterminate(*ctx.Effect) {
			return FetchSuppression{
				Suppressed: "not-refreshed-submodule-reach-indeterminate",
				Controlled: []ControlledPathBlocker{ControlledFetchSubmoduleReach},
				Blockers: []PlanBlocker{{
					Kind:   RefusalFetchSubmoduleReach,
					Detail: "unconditional submodule recursion's reach could not be bounded",
				}},
			}
		}
	}
	for _, ctx := range contexts {
		if ctx.Effect == nil {
			continue
		}
		if fetchLocalBranchCheckedOut(ctx.Effect.LocalBranchDestinations) {
			return FetchSuppression{
				Suppressed: "not-refreshed-local-branch-checked-out",
				Controlled: []ControlledPathBlocker{ControlledFetchLocalBranchHeld},
				Blockers: []PlanBlocker{{
					Kind:   RefusalFetchLocalBranchCheckedOut,
					Detail: "the fetch would write a covered refs/heads/** destination a worktree holds, or holds are unknown",
				}},
			}
		}
	}
	return FetchSuppression{}
}

// fetchContextsCollapse is §11.4's context-equivalence predicate: every
// enumerated context must agree on the canonical absolute common dir AND on
// the resolved effect — remotes[] (names, urls, refspecs,
// prune/prune_tags/tag_opt), the local-branch destinations and the submodule
// recursion. head_branch is deliberately excluded: two linked worktrees of
// one common dir sitting on different branches still collapse wherever their
// resolved effect agrees, which is the ordinary stack. A context whose
// common dir or effect could not be measured cannot be shown to agree and
// therefore never collapses.
func fetchContextsCollapse(contexts []PlanFetchContext) bool {
	first := contexts[0]
	if first.CommonDir == "" || first.Effect == nil {
		return false
	}
	for _, ctx := range contexts[1:] {
		if ctx.CommonDir == "" || ctx.Effect == nil {
			return false
		}
		if ctx.CommonDir != first.CommonDir {
			return false
		}
		if !fetchEffectsEquivalent(*first.Effect, *ctx.Effect) {
			return false
		}
	}
	return true
}

// fetchEffectsEquivalent compares exactly the resolved members §11.4 names,
// with head_branch — the one disclosure-only member — excluded.
func fetchEffectsEquivalent(a, b PlanFetchEffect) bool {
	x, y := a, b
	x.HeadBranch, y.HeadBranch = nil, nil
	return reflect.DeepEqual(x, y)
}

// fetchSubmoduleReachIndeterminate is rank 5.05 (§11.5): the fail-closed line
// stays exactly at unconditional recursion (mode: yes) whose reach could not
// be bounded; an on-demand or no-recursion context never refuses here.
func fetchSubmoduleReachIndeterminate(effect PlanFetchEffect) bool {
	return effect.SubmoduleRecursion.Mode == "yes" && effect.SubmoduleRecursion.Reach == "unknown"
}

// fetchLocalBranchCheckedOut is rank 5.06 (§11.6): a positive refspec
// covering refs/heads/** whose covered local-branch inventory is non-empty
// OR unknown. local_branch_destinations.patterns[] is already filtered
// (resolveLocalBranchDestinations/refspecsToPatterns, §11.6) to exactly the
// covering positive refspecs, so a non-empty Patterns list alone establishes
// the "covering refspec" half of the predicate; Branches/Held nil (as
// opposed to non-nil-but-empty) is that function's own "unknown" signal.
func fetchLocalBranchCheckedOut(lbd PlanLocalBranchDestinations) bool {
	if len(lbd.Patterns) == 0 {
		return false
	}
	if lbd.Branches == nil || lbd.Held == nil {
		return true
	}
	return len(lbd.Held) > 0
}

// checkoutFetchPlan enumerates the checkout route's one fetch context — the
// workspace repository root, context_source: workspace-repo-root — and its
// measured fetch effect (head_branch, remotes, refspecs, prune/tag mappings,
// local_branch_destinations including held[], and submodule_recursion
// including its reach) before any fetch runs, so a cause-5/6 document can
// still publish the whole measured row (§11.1). Cause 4
// (fetch-context-indeterminate) can never fire here: checkout resolves
// exactly one candidate, and a single candidate is always trivially equal to
// itself (§11.4), so it is intentionally absent from this function's
// blockers regardless of what the effect turns out to be.
func checkoutFetchPlan(repoDir string, caps GitCapabilities) PlanFetchPlan {
	ctx := PlanFetchContext{
		RepoToken: "",
		Root:      repoDir,
		Source:    "workspace-repo-root",
		Candidates: []PlanFetchCandidate{
			{ContextRoot: repoDir, ContextSource: "workspace-repo-root"},
		},
	}

	_, commonDir, err := MeasureContextRoots(repoDir)
	if err != nil {
		return PlanFetchPlan{Applies: true, Contexts: []PlanFetchContext{ctx}}
	}
	ctx.CommonDir = commonDir

	// §16 rule 3a: below 2.26 the ordered inventory cannot be produced, and
	// the capability gate has already refused this document.
	var inv GitConfigInventory
	if caps.CapConfigShowScope {
		inv = ProbeGitConfigInventory(repoDir)
	}
	// The holder inventory is produced by BuildPlanHolderIndex — the single
	// holder-index producer, keyed by canonical common dir (§14.4 rule 4) —
	// never by a direct BuildBranchHolderInventory call, so checkout's one
	// fetch context can never disagree with the plan's own holder facts
	// about what "one repository" is.
	ids := PlanContextIdentities{commonDir: {ContextID: commonDir, RepoRoot: repoDir, CommonDir: commonDir}}
	holders := BuildPlanHolderIndex(ids, []string{commonDir}).ByContext[commonDir]
	effect := ResolveFetchEffect(inv, repoDir, commonDir, caps, holders)
	ctx.Effect = &effect

	plan := PlanFetchPlan{Applies: true, Contexts: []PlanFetchContext{ctx}}
	sup := ResolveFetchSuppression(plan.Contexts)
	plan.Suppressed, plan.Controlled, plan.Blockers = sup.Suppressed, sup.Controlled, sup.Blockers
	return plan
}

// ============================================================================
// EvaluatePlanGuard (§4.6, §7, §13.7) — the pre-mutation admission verdict
// ============================================================================

// EvaluatePlanGuard is the guarded route's single pre-mutation gate. plan is
// the already-built document — BuildRebasePlan's own guard/approval/refusal
// verdicts, never re-measured here; g is this invocation's own control
// flags, consulted only to make this function a safe no-op when the
// invocation was never guarded at all (g.Guarded() false): there is then no
// admission predicate to enforce and nothing to refuse.
//
// Where the run IS guarded, the admission predicate is §4.6's own: a
// document with a non-nil refusal.kind, or runnable: false, or a non-empty
// guard.execute_blocked_by[] refuses; anything else proceeds. The three
// checks are deliberately independent rather than folded into one boolean:
// refusal.kind already reflects every blockers[] entry INCLUDING every
// guard-introduced rank 7-12 blocker BuildRebasePlan folds in before calling
// SelectPrimaryRefusal, so it is checked first and covers guard.would_refuse
// as a strict subset (a blocker always exists wherever would_refuse is
// true); runnable guards defensively against a document that is somehow not
// runnable for a rank 1-6 reason yet carries no refusal.kind, which
// SelectPrimaryRefusal's own contract never actually produces but this
// function does not trust blindly; and execute_blocked_by[] is checked last
// because §4.6 declares it independent of both — a controlled-path token
// the described route's own blockers/refusal never reflects at all (the
// symlinked-artefact cell) must still refuse a guarded mutation.
func EvaluatePlanGuard(plan RebasePlan, g CheckoutPlanGuard) error {
	if !g.Guarded() {
		return nil
	}
	if plan.Refusal.Kind != nil {
		detail := ""
		if plan.Refusal.Detail != nil {
			detail = *plan.Refusal.Detail
		}
		return &PlanGuardRefusalError{Kind: string(*plan.Refusal.Kind), Detail: detail}
	}
	if !plan.Runnable {
		return &PlanGuardRefusalError{Kind: string(RefusalStateRefused), Detail: "the described run is not runnable"}
	}
	if len(plan.Guard.ExecuteBlockedBy) > 0 {
		token := plan.Guard.ExecuteBlockedBy[0]
		return &PlanGuardRefusalError{
			Kind:   string(controlledPathBlockerKind(token)),
			Detail: controlledPathBlockerDetail(token),
		}
	}
	return nil
}

// controlledPathBlockerKind maps a controlled-path token to the RefusalKind
// §7.1 ranks it at, so the guard seam's marker names the KIND an operator
// reads in the document rather than the token's own spelling. The two
// owner-signalling tokens are the rank-3 `state-refused` pair; the three
// fetch-scoped tokens keep their own kinds.
func controlledPathBlockerKind(token ControlledPathBlocker) RefusalKind {
	switch token {
	case ControlledFetchContextIndeterminate:
		return RefusalFetchContextIndeterminate
	case ControlledFetchLocalBranchHeld:
		return RefusalFetchLocalBranchCheckedOut
	case ControlledFetchSubmoduleReach:
		return RefusalFetchSubmoduleReach
	default:
		return RefusalStateRefused
	}
}

// controlledPathBlockerDetail names, for EvaluatePlanGuard's own refusal
// error, which controlled-path fact a token represents. It is prose for a Go
// error message only — never a document field — so it need not match any
// blockers[].detail sentence verbatim.
func controlledPathBlockerDetail(token ControlledPathBlocker) string {
	switch token {
	case ControlledFetchContextIndeterminate:
		return "the fetch context could not be shown to be unambiguous"
	case ControlledFetchLocalBranchHeld:
		return "the fetch would write a covered refs/heads/** destination a worktree holds, or holds are unknown"
	case ControlledFetchSubmoduleReach:
		return "unconditional submodule recursion's reach could not be bounded"
	case ControlledLiveOwnerConcurrency:
		return "a pre-existing live foreign process owns this feature"
	case ControlledOwnerArtefactUndecodable:
		return "an owner-signalling artefact could not be decoded"
	default:
		return string(token)
	}
}

// ============================================================================
// RevalidatePlanGuardEntry (§10, §11.8) — the JIT re-measurement seam
// ============================================================================

// RevalidatePlanGuardEntryRequest bundles RevalidatePlanGuardEntry's inputs.
type RevalidatePlanGuardEntryRequest struct {
	// Request is the same-subject RebasePlanRequest RevalidatePlanEntry
	// itself requires: a fresh call site rebuilds it exactly as the
	// original plan build did, so this seam re-measures against CURRENT Git
	// state rather than replaying the approved document's own recorded
	// facts.
	Request RebasePlanRequest

	// Approved is the one entries[] row this invocation is immediately
	// about to run a Git command for.
	Approved PlanEntry

	// Limits is the effective guard limits this invocation is bound by.
	Limits PlanGuardLimits

	// ReplayedTotal is the count of candidates THIS invocation has already
	// rebased over, before Approved — the one running total neither the
	// approved document nor a fresh re-probe of Approved alone carries,
	// and the only state max_replay_total's JIT judgement needs beyond
	// what a fresh measurement of Approved itself already yields.
	ReplayedTotal int

	// StatePreserved is supplied truthfully by the caller: true once this
	// invocation has rebased at least one entry, or has reclaimed a run
	// guard, this run — a fact only the mutation loop itself knows.
	StatePreserved bool
}

// PlanGuardRevalidation is RevalidatePlanGuardEntry's typed JIT result. It
// exists because the running `max_replay_total` judgement of §10.3 row 1
// ("a deferred/unknown fact resolves into a count that newly exceeds an
// effective limit") is only correct when the caller accumulates the count
// this seam FRESHLY resolved, not the approved row's own recorded one. An
// `upstream-deferred` row is approved with `replay.candidate_count: null`,
// so a carrier that accumulated the approved value would add 0 for exactly
// the rows the deferral policy exists to re-measure, and a total limit
// strictly between the anchor's count and the run's real multi-row total
// would never fire.
type PlanGuardRevalidation struct {
	// Entry is the freshly re-measured row RevalidatePlanEntry produced —
	// the same value the drift comparison and the limit judgement were
	// made against, exposed so a caller never re-probes to learn it.
	Entry PlanEntry

	// CandidateCount is the freshly resolved replay count for Entry. It is
	// 0 only when Entry.Replay.CandidateCount is genuinely nil (a still-
	// unresolved unknown), which CandidateCountKnown distinguishes from a
	// resolved zero.
	CandidateCount int

	// CandidateCountKnown reports whether the fresh re-measurement resolved
	// a count at all. false means the row is still an unknown at this seam
	// and contributes nothing to the run's replay total.
	CandidateCountKnown bool
}

// RevalidatePlanGuardEntry is the JIT seam evaluator: immediately before
// Approved's own Git command runs, it re-measures Approved's row (via
// RevalidatePlanEntry, base/destination SHA, cutoff, candidate count/digest
// through RevalidationDigest), separately re-verifies the two collateral-
// class members a single-row digest comparison cannot correctly compare
// (§11.8), and judges the freshly re-measured candidate count against the
// effective limits and this run's own prior replay total. It returns the
// freshly resolved count for the caller's own running total, together with
// nil to proceed or a *PlanGuardRefusalError — revalidation-mismatch, or a
// newly-exceeded limit-per-entry/limit-total — with StatePreserved set from
// the request, never invented here.
func RevalidatePlanGuardEntry(req RevalidatePlanGuardEntryRequest) (PlanGuardRevalidation, error) {
	reval, err := RevalidatePlanEntry(req.Request, req.Approved)
	if err != nil {
		// §22.33i (v-e-1): an input the seam could not READ — the unreadable
		// stack.yaml reload of §25.102 input 3 — is rank 5.9 probe-failed
		// before any Git mutation, never a silent proceed and never a rank 9
		// mismatch derived from an unmeasured value.
		return PlanGuardRevalidation{}, &PlanGuardRefusalError{
			Kind:           string(RefusalProbeFailed),
			Detail:         fmt.Sprintf("revalidate entry %s: %v", req.Approved.Name, err),
			StatePreserved: req.StatePreserved,
		}
	}
	result := PlanGuardRevalidation{Entry: reval.Entry}
	if reval.Entry.Replay.CandidateCount != nil {
		result.CandidateCount = *reval.Entry.Replay.CandidateCount
		result.CandidateCountKnown = true
	}

	if reval.Drifted {
		return result, &PlanGuardRefusalError{
			Kind: string(RefusalRevalidationMismatch), Detail: reval.Blocker.Detail,
			StatePreserved: req.StatePreserved,
		}
	}

	// An UNMEASURED input outranks a mismatch derived from it: rank 5.9
	// probe-failed fires before the rank 9 comparison (§22.33i (v-e-1)).
	if reval.ProbeFailed {
		return result, &PlanGuardRefusalError{
			Kind: string(RefusalProbeFailed), Detail: reval.ProbeFailedDetail,
			StatePreserved: req.StatePreserved,
		}
	}

	if drifted, detail := collateralDrifted(req.Approved, reval.Entry); drifted {
		return result, &PlanGuardRefusalError{
			Kind: string(RefusalRevalidationMismatch), Detail: detail,
			StatePreserved: req.StatePreserved,
		}
	}

	if blocker := newlyExceededLimit(reval.Entry, req.Limits, req.ReplayedTotal); blocker != nil {
		return result, &PlanGuardRefusalError{
			Kind: string(blocker.Kind), Detail: blocker.Detail,
			StatePreserved: req.StatePreserved,
		}
	}
	return result, nil
}

// collateralDrifted re-verifies the two collateral-class members a single
// entry re-probe cannot correctly compare through RevalidatePlanEntry's own
// neutralized digest (§11.8, neutralizeRevalidationCollateral's own doc
// comment): CollateralMechanism and CollateralRefs, the set of refs this
// entry's own rebase would replay past. fresh already carries values
// computed by the SAME buildEntries/computeCollateral pass RevalidatePlanEntry
// ran to produce it, so no further Git probing happens here — only the
// comparison against the approved snapshot is this function's job.
// collateral_exposed is deliberately excluded: recomputing it correctly
// needs every STRICTLY EARLIER row of the ORIGINAL run in execution order,
// which neither the approved row nor a fresh re-probe of one entry
// reconstructs, and it is a disclosure member describing a DIFFERENT row's
// hazard, never a safety precondition on THIS entry's own mutation.
func collateralDrifted(approved, fresh PlanEntry) (bool, string) {
	if !stringPtrEqual(approved.CollateralMechanism, fresh.CollateralMechanism) {
		return true, fmt.Sprintf("entry %s's collateral mechanism changed since approval", approved.Name)
	}
	if !collateralRefsEqual(approved.CollateralRefs, fresh.CollateralRefs) {
		return true, fmt.Sprintf("entry %s's collateral refs changed since approval", approved.Name)
	}
	return false, ""
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func collateralRefsEqual(a, b []PlanCollateralRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// newlyExceededLimit judges the freshly re-measured candidate count against
// the effective limits and this run's own prior replay total — the state
// neither the approved row nor a fresh re-probe alone carries. It returns
// nil when nothing newly exceeds, never re-litigating a limit the approved
// document's own guard.evaluation[] already waived or accepted: a JIT limit
// refusal is by definition about count drift SINCE that evaluation, not a
// re-run of it.
func newlyExceededLimit(fresh PlanEntry, limits PlanGuardLimits, replayedTotal int) *PlanBlocker {
	count := 0
	if fresh.Replay.CandidateCount != nil {
		count = *fresh.Replay.CandidateCount
	}
	if limits.PerEntry.Value != nil && count > *limits.PerEntry.Value {
		name := fresh.Name
		return &PlanBlocker{
			Kind:   RefusalLimitPerEntry,
			Entry:  &name,
			Detail: fmt.Sprintf("%s replays %d candidates (limit %d)", fresh.Name, count, *limits.PerEntry.Value),
		}
	}
	if limits.Total.Value != nil {
		projected := replayedTotal + count
		if projected > *limits.Total.Value {
			return &PlanBlocker{
				Kind:   RefusalLimitTotal,
				Detail: fmt.Sprintf("the run replays %d candidates (limit %d)", projected, *limits.Total.Value),
			}
		}
	}
	return nil
}

// ============================================================================
// CheckoutPlanInspection (§13.7) — the checkout plan-only route's own
// read-only measurement pass, above and separate from BuildRebasePlan
// ============================================================================

// CheckoutPlanInspectionRequest bundles InspectCheckoutPlan's inputs.
type CheckoutPlanInspectionRequest struct {
	Opts CheckoutSyncOpts

	// StateOpts controls the checkout state inspector's liveness seam; the
	// zero value selects the shipped liveness probe and os.Getpid() (§23.1).
	StateOpts CheckoutPlanStateOpts
}

// CheckoutPlanInspection is InspectCheckoutPlan's own result: every fact
// BuildCheckoutRebasePlan/BuildCheckoutContinuationPlan need to construct a
// RebasePlanRequest without re-probing Git state a second time. Fields past
// the first hard error in the ordered sequence below (Stack/Order/Selection)
// stay at their zero value — the caller's own document build still proceeds
// with what was measured, exactly as a document-level StackErr/SortErr/
// SelectionErr already does inside BuildRebasePlan itself.
type CheckoutPlanInspection struct {
	Continue     bool
	Version      GitVersion
	Capabilities GitCapabilities

	State   CheckoutPlanState
	Verdict CheckoutPlanStateVerdict

	Stack    Stack
	StackErr error
	Order    []StackEntry
	SortErr  error

	Selection         SyncSelection
	SelectionResolved bool
	SelectionErr      error

	// PlanEntries is buildCheckoutPlanFrom's own result — the persistable
	// []CheckoutPlanEntry a fresh run would write into a transaction. It is
	// never populated on --continue: a continuation replays the ALREADY-
	// persisted plan, never re-derives one (§13.3).
	PlanEntries  []CheckoutPlanEntry
	PlanBuildErr error

	Remaining      []string
	StageFacts     []PlanStageFact
	BasePreflight  PlanBasePreflight
	FetchPlan      PlanFetchPlan
	Gates          []PlanGateResult
	Limits         PlanGuardLimits
	LimitConflicts []PlanGuardLimitConflict
}

// InspectCheckoutPlan is the checkout route's own §13.7 ordered read-only
// inspection: version/capabilities, state (transaction/lock/worktree/head),
// the fresh-run precondition gates, the stack/order/selection triple, the
// reconciled guard limits, and — fresh-route only — the I14 base preflight
// and the pre-fetch context/effect enumeration. It performs no mutation and
// no network call of its own; checkoutFetchPlan measures a fetch's WOULD-BE
// effect, never runs one.
func InspectCheckoutPlan(req CheckoutPlanInspectionRequest) CheckoutPlanInspection {
	opts := req.Opts
	var insp CheckoutPlanInspection
	insp.Continue = opts.Continue
	insp.Capabilities, insp.Version, _ = ProbeGitCapabilities()
	insp.State, insp.Verdict = InspectCheckoutPlanState(opts, req.StateOpts)
	// §16 rules 3a/3b, at their required position: immediately after the
	// version probe, above the fetch and above every config read. Checkout
	// never passes --update-refs (§11.8), so rule 3b never applies here.
	insp.Gates = append(CapabilityGates(insp.Version, insp.Capabilities, false),
		append(checkoutPreconditionGates(opts), checkoutLockLadderGates(opts, insp.State)...)...)

	tx := insp.State.Files.CheckoutTransaction.Transaction
	insp.Limits = resolveCheckoutGuardLimits(opts.PlanGuard, tx)
	insp.LimitConflicts = resolveCheckoutLimitConflicts(tx, opts.PlanGuard)

	stack, stackErr := LoadStack(opts.FeaturePath)
	if stackErr != nil {
		insp.StackErr = stackErr
		return insp
	}
	insp.Stack = stack

	order, sortErr := TopoSort(stack)
	if sortErr != nil {
		insp.SortErr = sortErr
		return insp
	}
	insp.Order = order

	sel, selErr := ResolveSyncSelectionFromOrder(stack, order, opts.Policy, SyncSelectionOpts{
		Mode:    ModeCheckout,
		NewMode: opts.NewMode,
		Feature: opts.Feature,
	})
	if selErr != nil {
		insp.SelectionErr = selErr
		return insp
	}
	insp.Selection = sel
	insp.SelectionResolved = true

	insp.Remaining = RemainingRebaseEntries(
		checkoutEffectiveRoute(opts, tx),
		RebasePlanLayout{FeaturePath: opts.FeaturePath, RepoRoot: opts.RepoDir},
		RemainingEntriesState{Mode: ModeCheckout, Checkout: insp.State},
		order, sel,
	)

	if opts.Continue {
		insp.StageFacts = checkoutStageFacts(insp.State, insp.Remaining)
		return insp
	}

	// Fresh route only below this point: --continue never re-derives a plan,
	// never re-evaluates I14, and never fetches (§13.3, §11.1).
	planEntries, planErr := buildCheckoutPlanFrom(opts.RepoDir, stack, order, sel)
	if planErr != nil {
		insp.PlanBuildErr = planErr
		return insp
	}
	insp.PlanEntries = planEntries
	insp.BasePreflight = checkoutBasePreflight(opts, stack, sel)

	if opts.Policy.Fetch == SyncFetchEnabled {
		insp.FetchPlan = checkoutFetchPlan(opts.RepoDir, insp.Capabilities)
	}

	return insp
}

// checkoutEffectiveRoute derives this invocation's own effective route
// (§13.6 rule 4): a continuation of an already-persisted transaction
// inherits ITS OWN route (txNewMode, never re-derived from opts.NewMode,
// which a continuation invocation may not even repeat); a fresh run's route
// is opts.NewMode directly.
func checkoutEffectiveRoute(opts CheckoutSyncOpts, tx *CheckoutTransaction) string {
	if opts.Continue && tx != nil {
		if txNewMode(tx) {
			return RouteNewMode
		}
		return RouteLegacy
	}
	if opts.NewMode {
		return RouteNewMode
	}
	return RouteLegacy
}

// checkoutRouteTriggers is RebasePlanRequest.RouteTriggers: the sorted names
// of every Changed trigger flag this invocation actually supplied.
func checkoutRouteTriggers(changed map[string]bool) []string {
	names := make([]string, 0, len(changed))
	for k, v := range changed {
		if v {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}

// checkoutPushSource and checkoutPushIntent derive intent.push/push_source
// (§4.6, §3's four-member domain minus the external-only persisted-payload
// member): a fresh run's own --push flag is the whole story; a continuation
// of an already new-mode transaction resumes ITS OWN persisted intent; a
// continuation of a legacy transaction still resumes the persisted intent
// (every CheckoutTransaction has always persisted Push, §13.1) but is
// tagged with the legacy-continuation origin token the domain reserves for
// exactly this case.
func checkoutPushSource(opts CheckoutSyncOpts, tx *CheckoutTransaction) string {
	if !opts.Continue {
		return "flag"
	}
	if tx != nil && txNewMode(tx) {
		return "persisted-transaction"
	}
	return "flag-legacy-continuation"
}

func checkoutPushIntent(opts CheckoutSyncOpts, tx *CheckoutTransaction) bool {
	if opts.Continue && tx != nil {
		return tx.Push
	}
	return opts.Push
}

// checkoutBasePreflight is I14 (§10.7 rule 1a): the shipped
// firstUnresolvedCheckoutBase locator, projected into PlanBasePreflight with
// the shipped verifyCheckoutBasesLocally sentence, verbatim. It is called
// only on the fresh route (I14 is evaluated at most once per run, pre-lock,
// and never re-reached on --continue, mirroring RunCheckoutSync's own
// single call site).
func checkoutBasePreflight(opts CheckoutSyncOpts, stack Stack, sel SyncSelection) PlanBasePreflight {
	entry, ref, found := firstUnresolvedCheckoutBase(opts, stack, sel)
	if !found {
		return PlanBasePreflight{Applies: true}
	}
	return PlanBasePreflight{
		Applies: true,
		Failed:  true,
		Entry:   entry,
		Ref:     ref,
		Detail:  fmt.Sprintf("base %q for stack entry %q does not resolve locally; drop --no-fetch or fetch manually first", ref, entry),
	}
}

// CapabilityGates is §16 rules 3a/3b evaluated at the ONE position both
// rules require: immediately after the version probe, ABOVE this route's own
// fetch and above every config read. Each fires as a DOCUMENT-LEVEL
// (entry: null) rank 5.9 `probe-failed` gate — non-waivable, like every
// rank 1-10 fact — so the document is `runnable: false` and the route's
// fetch is suppressed by the same gate ladder every other pre-fetch gate
// uses. There is no per-row variant of either rule.
//
//   - rule 3a, `config --list --show-scope` (2.26): a host below the gate
//     cannot produce the ordered inventory this document's whole
//     repositories[].config surface is derived from, so no row may publish a
//     configuration verdict at all;
//   - rule 3b, `rebase --update-refs` (2.38): argv-DERIVED — it fires only
//     where at least one actual planned argv row of THIS invocation would
//     carry the option, which the caller decides from mode and scope before
//     any row is built (checkout never passes it, §11.8).
//
// An unestablished version (`Version.OK` false) fails both gates: a
// capability that could not be measured is never assumed present.
func CapabilityGates(version GitVersion, caps GitCapabilities, argvNeedsUpdateRefs bool) []PlanGateResult {
	established := version.Probed && version.OK
	showScope := established && caps.CapConfigShowScope
	updateRefs := established && caps.CapRebaseUpdateRefs

	var gates []PlanGateResult
	if !showScope {
		gates = append(gates, PlanGateResult{
			ID: "capability-config-show-scope", Applies: true, Failed: true,
			Kind: RefusalProbeFailed,
			Detail: "git config --list --show-scope requires Git 2.26 or newer; observed " +
				gitVersionLabel(version),
		})
	}
	if argvNeedsUpdateRefs && !updateRefs {
		gates = append(gates, PlanGateResult{
			ID: "capability-rebase-update-refs", Applies: true, Failed: true,
			Kind: RefusalProbeFailed,
			Detail: "this invocation's planned argv carries --update-refs, which requires Git 2.38 or newer; observed " +
				gitVersionLabel(version),
		})
	}
	return gates
}

// gitVersionLabel renders the observed version for a capability gate's own
// detail sentence, naming an unestablished probe honestly rather than
// printing a zero version.
func gitVersionLabel(v GitVersion) string {
	if !v.Probed {
		return "no probe"
	}
	if !v.OK {
		return "an unparseable git --version"
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// checkoutPreconditionGates evaluates the three fresh-run, pre-lock
// preconditions RunCheckoutSync itself checks before ever touching the
// filesystem for a transaction (§13.7 gates 1-3): no persisted transaction
// may already exist, no other Git operation may be in progress, and the
// working tree must be clean. Detail is the shipped sentence, verbatim, so
// a Gates-derived blockers[] row reads identically to the legacy executor's
// own refusal. All three are silent no-ops on --continue, which has already
// passed them by definition (it owns the transaction and the lock).
func checkoutPreconditionGates(opts CheckoutSyncOpts) []PlanGateResult {
	if opts.Continue {
		return nil
	}
	dirty, _ := gitWorkingTreeDirty(opts.RepoDir) // an unmeasurable tree simply does not fire this gate
	return []PlanGateResult{
		{
			ID: "checkout-transaction-exists", Applies: true, Failed: HasCheckoutTransaction(opts.FeaturePath),
			Kind:   RefusalStateRefused,
			Detail: "checkout sync transaction already exists; use --continue or --abort",
		},
		{
			ID: "checkout-operation-in-progress", Applies: true, Failed: gitOperationInProgress(opts.RepoDir),
			Kind:   RefusalStateRefused,
			Detail: "another Git operation is in progress; complete or abort it before checkout sync",
		},
		{
			ID: "checkout-working-tree-dirty", Applies: true, Failed: dirty,
			Kind:   RefusalStateRefused,
			Detail: "working tree is dirty; commit or stash changes before checkout sync",
		},
	}
}

// checkoutLockLadderGates is §13.7 gate j: the projection of the WHOLE
// native `.checkout-sync.lock` ladder — AcquireCheckoutLock on the fresh arm,
// forceAcquireCheckoutLock on the continuation arm — with each row's own
// SHIPPED sentence reproduced byte for byte, including its operands.
//
// It is a snapshot projection, never a re-decision: it reads only the
// already-measured PlanCheckoutLockFile of §12.5a and never opens the lock
// again. Three classes of native sentence are deliberately NOT projected,
// because they describe write races and future writes rather than snapshot
// facts: `reclaim stale checkout-sync lock: %w`, `reclaim checkout-sync
// lock: %w`, `acquire checkout-sync lock after stale recovery: %w` and
// `create lock directory: %w`.
//
// Two rows of the native ladder are unreachable from here and are therefore
// absent by construction:
//
//   - row 5a, a SELF-PID lock, refuses on the FRESH arm only (its sentence is
//     the initialized-or-invalid one, since a self PID is never "live
//     foreign"); the continuation arm reclaims its own lock and publishes no
//     blocker at all;
//   - row 6, `stale lock from PID %d detected with existing transaction`, is
//     unreachable below gate b: HasCheckoutTransaction is a bare os.Stat
//     evaluated ABOVE the lock, so a dead foreign lock beside a transaction
//     takes gate b's `previous checkout-sync incomplete; use --continue or
//     --abort` sentence instead.
//
// A SYMLINKED lock synthesizes no native sentence at all: the shipped ladder
// reads straight through the link, so only the controlled-path token of
// §12.5 describes it (gateVerdicts owns that), never a blocker here.
func checkoutLockLadderGates(opts CheckoutSyncOpts, state CheckoutPlanState) []PlanGateResult {
	lock := state.Files.CheckoutLock
	if !lock.Applicable {
		return nil
	}
	lockPath := CheckoutLockPath(opts.FeaturePath)
	id := "checkout-lock-ladder"
	fail := func(detail string) []PlanGateResult {
		return []PlanGateResult{{ID: id, Applies: true, Failed: true, Kind: RefusalStateRefused, Detail: detail}}
	}

	switch lock.Presence {
	case PlanPresenceAbsent, PlanPresenceNotApplicable, PlanPresenceSymlink:
		// Absent refuses nothing; a symlink is the controlled-token cell and
		// synthesizes no native sentence.
		return nil
	case PlanPresenceUnreadable:
		reason := UnreadableIOError
		if lock.UnreadableReason != nil {
			reason = *lock.UnreadableReason
		}
		err := lock.Err
		if err == nil {
			// The empty-document row carries no loader error of its own (the
			// emptiness IS the finding), so the projected `%w` operand is the
			// same sentence the shipped decoder produces for a document with
			// no content.
			err = errors.New("EOF")
		}
		switch reason {
		case UnreadableDecodeError, UnreadableEmptyDocument:
			if opts.Continue {
				return fail(fmt.Sprintf("invalid checkout-sync lock %s: %v", lockPath, err))
			}
			return fail(fmt.Sprintf("invalid checkout-sync lock; inspect %s and use --abort to recover: %v", lockPath, err))
		default:
			// io-error and not-a-file (a directory) both take the read arm.
			if opts.Continue {
				return fail(fmt.Sprintf("read checkout-sync lock: %v", err))
			}
			return fail(fmt.Sprintf("read existing checkout-sync lock: %v", err))
		}
	}

	// Readable. The shipped ladder's own order: invalid PID first, then a
	// live holder, then (fresh only) a self-recorded PID, then nothing.
	if lock.Lock == nil {
		return nil
	}
	if lock.Lock.PID <= 0 {
		return fail(fmt.Sprintf("checkout-sync lock is being initialized or is invalid; retry or inspect %s", lockPath))
	}
	if lock.Alive != nil && *lock.Alive {
		if opts.Continue {
			return fail(fmt.Sprintf("lock held by live process %d; cannot reclaim", lock.Lock.PID))
		}
		return fail(fmt.Sprintf("checkout-sync lock held by live process %d (created %s); cannot steal live lock",
			lock.Lock.PID, lock.Lock.Created))
	}
	if lock.Self != nil && *lock.Self && !opts.Continue {
		// Row 5a: writeLockExclusive fails, the PID is this process, so the
		// shipped fresh ladder cannot call it a live FOREIGN holder and takes
		// the initialized-or-invalid arm. The continuation arm reclaims it.
		return fail(fmt.Sprintf("checkout-sync lock is being initialized or is invalid; retry or inspect %s", lockPath))
	}
	// A dead, foreign lock with no transaction relation: the reclaim is
	// projected as succeeding, so no blocker at all.
	return nil
}

// checkoutStageFacts derives StageFacts for the --continue route: one
// disclosure row per Remaining entry projecting (never re-deciding) the
// shipped continuation ladder's own next command for that entry, purely
// from the persisted transaction's own Stage/CurrentIndex/Plan — the same
// inputs resumeTransaction (checkout_sync.go) itself switches on.
// AutostashApplies mirrors autostashApplies' own §18.4 rule exactly
// (rebase_plan_build.go) for the overlapping continuation cases. It is
// deliberately silent on whether a later row's own rebase turns out to be a
// structural no-op — that answer needs this run's own entries[].strategy,
// which does not exist until BuildRebasePlan's buildEntries has run — so
// StandaloneCheckout here is always the complement of RebaseIsNext, a real
// but necessarily coarser signal than a caller already holding entries[]
// could give.
func checkoutStageFacts(state CheckoutPlanState, remaining []string) []PlanStageFact {
	tx := state.Files.CheckoutTransaction.Transaction
	if tx == nil || len(remaining) == 0 {
		return nil
	}
	index := make(map[string]int, len(tx.Plan))
	for i, e := range tx.Plan {
		index[e.Name] = i
	}

	facts := make([]PlanStageFact, 0, len(remaining))
	for _, name := range remaining {
		idx, known := index[name]
		atCurrent := known && idx == tx.CurrentIndex

		fact := PlanStageFact{Entry: name, Stage: string(StagePlanned)}
		rebaseIsNext := true
		autostash := true
		if atCurrent {
			fact.Stage = string(tx.Stage)
			rebaseIsNext = checkoutStageRebasesNext(tx.Stage)
			autostash = checkoutStageAutostashApplies(tx.Stage)
		}

		fact.RebaseIsNext = rebaseIsNext
		fact.StandaloneCheckout = !rebaseIsNext
		fact.NextCommand = "git checkout " + name
		if rebaseIsNext {
			fact.NextCommand = "git rebase"
		}
		fact.AutostashApplies = &autostash
		facts = append(facts, fact)
	}
	return facts
}

// checkoutStageRebasesNext answers whether resumeTransaction's own switch
// over tx.Stage (checkout_sync.go) reaches a NEW git rebase for the
// current-index row before this entry's row is done: true at
// StageSwitched/StagePlanned/StageRebasing (resumeFromSwitched/
// executeTransaction's own rebase step is still ahead) and at StageConflict
// (a rebase --continue-equivalent is the very next Git command, via
// resumeTransaction's own StageConflict arm); false at StageRebased/
// StageValidating/StageRestoring/StageCompleted, where the rebase itself
// already ran and only ancestry/validation/restoration/cleanup remains.
func checkoutStageRebasesNext(stage CheckoutStage) bool {
	switch stage {
	case StageSwitched, StagePlanned, StageRebasing, StageConflict:
		return true
	default:
		return false
	}
}

// checkoutStageAutostashApplies is autostashApplies' own §18.4 continuation
// rule (rebase_plan_build.go), restated here for a row whose only known
// facts are the persisted transaction's stage — never re-decided, just
// evaluated from the same three-stage set.
func checkoutStageAutostashApplies(stage CheckoutStage) bool {
	switch stage {
	case StageSwitched, StagePlanned, StageRebasing:
		return true
	default:
		return false
	}
}

// originLimit is one guard.limits.* member's {value, origin}: the unarmed
// value ({nil, "none"}) whenever v is nil, the supplied origin otherwise.
func originLimit(v *int, origin string) PlanGuardLimit {
	if v == nil {
		return PlanGuardLimit{Value: nil, Origin: "none"}
	}
	return PlanGuardLimit{Value: v, Origin: origin}
}

// resolveCheckoutLimit derives ONE guard.limits.* member per §13.6 rule 4d
// and §3's six-member origin enum: a fresh run (tx nil) has only its own
// supplied flag; a continuation of an ALREADY-guarded transaction keeps
// THAT transaction's own persisted value wherever it armed this specific
// key (the reconciliation rule "persisted value stays effective", §2.17.2),
// falling back to this continuation's own supplied flag, tagged as a
// persisted-continuation arm, only where the persisted transaction never
// armed this key; a continuation of a legacy (not-yet-guarded) transaction
// has no persisted value to defer to at all, so this continuation's own
// supplied flag is the only source, tagged with the upgrade-specific
// origin.
func resolveCheckoutLimit(tx *CheckoutTransaction, guarded bool, persisted, supplied *int) PlanGuardLimit {
	switch {
	case tx == nil:
		return originLimit(supplied, "flags")
	case guarded && persisted != nil:
		return originLimit(persisted, "persisted-transaction")
	case guarded:
		return originLimit(supplied, "flags-persisted-continuation")
	default:
		return originLimit(supplied, "flags-legacy-continuation")
	}
}

// resolveCheckoutGuardLimits is RebasePlanRequest.Limits' own reconciliation
// (§13.2a): the effective, already-reconciled {max_replay_per_entry,
// max_replay_total} pair, never the raw supplied flags alone.
func resolveCheckoutGuardLimits(g CheckoutPlanGuard, tx *CheckoutTransaction) PlanGuardLimits {
	guarded := checkoutRecoveryIsGuarded(tx)
	var perEntryPersisted, totalPersisted *int
	if tx != nil {
		perEntryPersisted, totalPersisted = tx.MaxReplayPerEntry, tx.MaxReplayTotal
	}
	return PlanGuardLimits{
		PerEntry: resolveCheckoutLimit(tx, guarded, perEntryPersisted, g.MaxPerEntry),
		Total:    resolveCheckoutLimit(tx, guarded, totalPersisted, g.MaxTotal),
	}
}

// limitConflict is one guard.limit_conflicts[] row (§2.17.2): a conflict
// exists only where a PERSISTED value already existed at claim time AND
// this continuation supplied a DIFFERENT one — a continuation arming a
// previously-unarmed key is a fresh arm, never a conflict.
func limitConflict(key string, persisted *int, persistedOrigin string, supplied *int) *PlanGuardLimitConflict {
	if supplied == nil || persisted == nil || *supplied == *persisted {
		return nil
	}
	return &PlanGuardLimitConflict{Key: key, EffectiveValue: persisted, EffectiveOrigin: persistedOrigin, SuppliedValue: supplied}
}

// resolveCheckoutLimitConflicts is RebasePlanRequest.LimitConflicts: never
// populated for a fresh run or a continuation of a not-yet-guarded
// transaction, since neither has a persisted armed value to conflict with.
func resolveCheckoutLimitConflicts(tx *CheckoutTransaction, g CheckoutPlanGuard) []PlanGuardLimitConflict {
	if !checkoutRecoveryIsGuarded(tx) {
		return nil
	}
	var out []PlanGuardLimitConflict
	if c := limitConflict("max_replay_per_entry", tx.MaxReplayPerEntry, "persisted-transaction", g.MaxPerEntry); c != nil {
		out = append(out, *c)
	}
	if c := limitConflict("max_replay_total", tx.MaxReplayTotal, "persisted-transaction", g.MaxTotal); c != nil {
		out = append(out, *c)
	}
	return out
}

// checkoutWorkspace is RebasePlan.Workspace: mode/repo_root are always
// available from opts alone; stable_id is best-effort via the same
// workspace-identity subsystem the rest of the tree uses (internal/workspace.go)
// and stays nil rather than refusing the whole plan when it cannot be
// established.
func checkoutWorkspace(opts CheckoutSyncOpts) PlanWorkspace {
	ws := ResolveCurrentWorkspace(opts.RepoDir, LoadConfig())
	var stableID *string
	if ws.StableID != "" {
		id := ws.StableID
		stableID = &id
	}
	return PlanWorkspace{Mode: string(ModeCheckout), StableID: stableID, RepoRoot: opts.RepoDir}
}

// checkoutPushFacts is RebasePlanRequest.PushFacts for the checkout route
// (§14.1a rule 9): checkout is always exactly one repository, so this
// measures exactly one context/remote-facts pair, keyed by the repo token
// PushTargets itself looks up for every checkout-mode entry — "" (§2.8 row
// 1). It is a complete no-op, never touching Git, whenever this invocation
// does not intend to push at all.
func checkoutPushFacts(opts CheckoutSyncOpts, caps GitCapabilities, commonDir string) PlanPushFacts {
	if !opts.Push {
		return PlanPushFacts{}
	}
	// The ONE ordered inventory this push context reads (§14.1a rule 7):
	// both the remote ladder and MeasurePushRemoteFacts consume it.
	var inv GitConfigInventory
	if caps.CapConfigShowScope {
		inv = ProbeGitConfigInventory(opts.RepoDir)
	}
	ctx, identity, remoteName, err := ResolvePushContext(opts.RepoDir, "workspace-repo-root", "materialized", inv, caps)
	if err != nil {
		return PlanPushFacts{Applies: true, Phase: "plan-point"}
	}
	facts := MeasurePushRemoteFacts(ctx.ContextID, commonDir, remoteName, inv, caps)
	return PlanPushFacts{
		Applies:         true,
		Phase:           "plan-point",
		Identities:      PlanContextIdentities{identity.ContextID: identity},
		Contexts:        []PlanPushContext{ctx},
		Materialization: map[string]string{"": ctx.Materialization},
		Remotes:         map[string]PlanPushRemoteFacts{"": facts},
	}
}

// checkoutValidationIdentity is RebasePlanRequest.Validation (§15): checkout
// always freezes its effective test command — the fresh run's own --test
// value, or the persisted transaction's value on --continue, mirroring
// ContinueCheckoutSync's own "opts.TestCommand = tx.TestCommand" rule
// (checkout_sync.go:920) — and never re-reads config per entry the way
// unguarded legacy external does. The raw command travels only as far as
// this struct; buildIntent (rebase_plan_build.go) publishes Digest alone.
func checkoutValidationIdentity(opts CheckoutSyncOpts, tx *CheckoutTransaction) PlanValidationIdentity {
	command := opts.TestCommand
	source := "cli-test"
	if opts.Continue && tx != nil {
		command = tx.TestCommand
		source = "persisted-transaction"
	}
	if command == "" {
		return PlanValidationIdentity{Applies: false, Source: "none"}
	}
	return PlanValidationIdentity{Applies: true, Command: command, Source: source, Digest: ValidationDigest(command)}
}

// checkoutRequestedRoute is RebasePlanRequest.RequestedRoute (§13.6 rule 4):
// "" whenever this invocation's own nominal route (opts.NewMode) already
// equals the effective one, which is always true on a fresh run and true on
// a continuation whose persisted transaction agrees with the flag this
// invocation happened to carry; it surfaces only the disagreement a
// continuation's own inherited route can create.
func checkoutRequestedRoute(opts CheckoutSyncOpts, effective string) string {
	requested := RouteLegacy
	if opts.NewMode {
		requested = RouteNewMode
	}
	if requested == effective {
		return ""
	}
	return requested
}

// checkoutContinueMismatchAxis mirrors checkoutContinueMismatches' own
// four-way axis check (checkout_sync.go), read-only: every sentence it can
// produce is generated by calling the shipped checkoutContinueMismatch
// formatter directly, so the detail text is never re-authored — only the
// axis token is surfaced structurally, for a document a shipped `error`
// cannot carry.
func checkoutContinueMismatchAxis(opts CheckoutSyncOpts, tx *CheckoutTransaction) (axis, detail string, mismatched bool) {
	changed := opts.Changed
	persisted := transactionPolicy(tx)
	switch {
	case (changed["fetch"] || changed["no-fetch"]) && opts.Policy.Fetch != persisted.Fetch:
		return "fetch", checkoutContinueMismatch("fetch", string(persisted.Fetch), string(opts.Policy.Fetch)).Error(), true
	case (changed["full"] || changed["local-only"]) && opts.Policy.Propagation != persisted.Propagation:
		return "propagation", checkoutContinueMismatch("propagation", string(persisted.Propagation), string(opts.Policy.Propagation)).Error(), true
	case (changed["only"] || changed["from"]) && opts.Policy.ScopeLabel() != persisted.ScopeLabel():
		return "scope", checkoutContinueMismatch("scope", persisted.ScopeLabel(), opts.Policy.ScopeLabel()).Error(), true
	case changed["push"] && opts.Push != tx.Push:
		return "push", checkoutContinueMismatch("push", fmt.Sprintf("%v", tx.Push), fmt.Sprintf("%v", opts.Push)).Error(), true
	default:
		return "", "", false
	}
}

// checkoutContinuationGate is RebasePlanRequest.ContinuationGate (§13.7a
// rule 9): a read-only projection of ContinueCheckoutSync's own dispatch
// gates (checkout_sync.go), never a second implementation of them — every
// detail sentence it can produce comes from calling the shipped
// checkoutContinueMismatch/transactionPolicy helpers directly. It applies
// only on --continue with an already-decoded transaction; "no transaction
// to continue" and "state version too new" surface through StackErr/Gates
// elsewhere, since this gate only covers the mismatch/selection checks that
// need a decoded transaction to evaluate at all.
func checkoutContinuationGate(opts CheckoutSyncOpts, state CheckoutPlanState, tx *CheckoutTransaction, stack Stack) PlanContinuationGate {
	if !opts.Continue {
		return PlanContinuationGate{}
	}
	if tx == nil {
		// A continuation with no decoded transaction publishes the SHIPPED
		// no-transaction refusal as its own cause, so the rows-less document
		// always names why it cannot be planned (§13.7a rule 4).
		detail := "no transaction to continue"
		if err := state.Files.CheckoutTransaction.Err; err != nil {
			detail = fmt.Sprintf("%s: %v", detail, err)
		}
		return PlanContinuationGate{Applies: true, Failed: true, Axis: "state", Detail: detail}
	}
	if TransactionNewMode(tx) {
		if axis, detail, mismatched := checkoutContinueMismatchAxis(opts, tx); mismatched {
			return PlanContinuationGate{Applies: true, Failed: true, Axis: axis, Detail: detail}
		}
		for _, name := range tx.Selected {
			if !HasBranch(stack, name) {
				return PlanContinuationGate{
					Applies: true, Failed: true, Axis: "selection",
					Detail: fmt.Sprintf("selected stack entry %q no longer exists in stack; use --abort", name),
				}
			}
		}
	} else if opts.Push && !tx.Push {
		return PlanContinuationGate{
			Applies: true, Failed: true, Axis: "push",
			Detail: fmt.Sprintf("cannot add --push to an existing transaction that was started without it; persisted push=%v wins", tx.Push),
		}
	}
	return PlanContinuationGate{Applies: true}
}

// checkoutRowsAvailable is RebasePlanRequest.RowsAvailable (§13.7 rule 4):
// the fresh arm needs Stack/Order/Selection all resolved; the continuation
// arm additionally needs a decoded transaction, since Remaining/entries[]
// filtering has nothing meaningful to filter against otherwise.
func checkoutRowsAvailable(insp CheckoutPlanInspection) bool {
	resolved := insp.StackErr == nil && insp.SortErr == nil && insp.SelectionErr == nil
	if !resolved {
		return false
	}
	if insp.Continue {
		return insp.State.Files.CheckoutTransaction.Transaction != nil
	}
	return true
}

// checkoutPlanRequest is the shared assembly step BuildCheckoutRebasePlan and
// BuildCheckoutContinuationPlan both need: every RebasePlanRequest member
// InspectCheckoutPlan's own measurement pass already settled, independent of
// the fetch/fresh-vs-continuation branching each caller still does on its
// own. fetchOutcome is threaded in rather than measured here because only
// the fresh caller ever has one to report.
func checkoutPlanRequest(opts CheckoutSyncOpts, insp CheckoutPlanInspection, fetchOutcome PlanFetchOutcome) RebasePlanRequest {
	tx := insp.State.Files.CheckoutTransaction.Transaction
	route := checkoutEffectiveRoute(opts, tx)
	rowsAvailable := checkoutRowsAvailable(insp)

	var stackPtr *Stack
	if rowsAvailable {
		s := insp.Stack
		stackPtr = &s
	}

	_, commonDir, _ := MeasureContextRoots(opts.RepoDir)

	return RebasePlanRequest{
		Layout:                    RebasePlanLayout{FeaturePath: opts.FeaturePath, RepoRoot: opts.RepoDir},
		Mode:                      ModeCheckout,
		Feature:                   opts.Feature,
		Workspace:                 checkoutWorkspace(opts),
		Route:                     route,
		RequestedRoute:            checkoutRequestedRoute(opts, route),
		RouteTriggers:             checkoutRouteTriggers(opts.Changed),
		Invocation:                "plan-only",
		Policy:                    opts.Policy,
		PolicyFetchDefaultApplied: !opts.Changed["fetch"] && !opts.Changed["no-fetch"],
		Push:                      checkoutPushIntent(opts, tx),
		PushSource:                checkoutPushSource(opts, tx),
		Guard:                     opts.PlanGuard,
		Limits:                    insp.Limits,
		LimitConflicts:            insp.LimitConflicts,
		Validation:                checkoutValidationIdentity(opts, tx),
		Approve:                   opts.PlanGuard.Approve,
		Stack:                     stackPtr,
		Order:                     insp.Order,
		SortErr:                   insp.SortErr,
		StackErr:                  insp.StackErr,
		Selection:                 insp.Selection,
		SelectionResolved:         insp.SelectionResolved,
		SelectionErr:              insp.SelectionErr,
		RowsAvailable:             rowsAvailable,
		Continue:                  opts.Continue,
		Remaining:                 insp.Remaining,
		StageFacts:                insp.StageFacts,
		Changed:                   opts.Changed,
		ContinuationGate:          checkoutContinuationGate(opts, insp.State, tx, insp.Stack),
		Fetch:                     fetchOutcome,
		FetchPlan:                 insp.FetchPlan,
		PushFacts:                 RefreshPushTrackingRefs(checkoutPushFacts(opts, insp.Capabilities, commonDir), fetchOutcome),
		BasePreflight:             insp.BasePreflight,
		Version:                   insp.Version,
		Capabilities:              insp.Capabilities,
		CheckoutState:             insp.State,
		Gates:                     insp.Gates,
	}
}

// PlanCheckoutRebase is the checkout route's plan-only entry point (§13.7):
// it performs the ordered gates, fetches at most once and only under an
// effective --fetch policy on the fresh route (never on --continue), and
// returns the resulting RebasePlan value to its caller in package cli. It
// writes no document byte of its own and never touches os.Stdout/
// os.Stderr — w.Prose is the only writer it is given, and it is used only
// for the same "Fetching origin ... done/failed" line
// fetchCheckoutRepoTo already prints on the shipped mutating route.
func PlanCheckoutRebase(opts CheckoutSyncOpts, w PlanWriters) (RebasePlan, error) {
	if opts.Continue {
		return BuildCheckoutContinuationPlan(opts)
	}
	return BuildCheckoutRebasePlan(opts, w)
}

// BuildCheckoutRebasePlan covers the fresh checkout route (§13.7): it
// inspects Git/transaction/lock state, performs the pre-lock precondition
// gates and the I14 base preflight, measures (never assumes) the pre-fetch
// context/effect enumeration, performs the run's own at-most-once fetch
// when the effective policy calls for one and nothing already blocks it,
// and assembles + builds the resulting RebasePlanRequest.
func BuildCheckoutRebasePlan(opts CheckoutSyncOpts, w PlanWriters) (RebasePlan, error) {
	insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})

	var fetchOutcome PlanFetchOutcome
	if opts.Policy.Fetch == SyncFetchEnabled && insp.FetchPlan.Applies && len(insp.FetchPlan.Blockers) == 0 &&
		checkoutRowsAvailable(insp) && !checkoutGatesFailed(insp.Gates) && !insp.BasePreflight.Failed && !insp.State.LiveForeignLock() {
		fetchOutcome = fetchCheckoutRepoTo(w.Prose, PlanFetchContext{
			RepoToken:  "",
			Root:       opts.RepoDir,
			CommonDir:  insp.FetchPlan.Contexts[0].CommonDir,
			Source:     "workspace-repo-root",
			Candidates: insp.FetchPlan.Contexts[0].Candidates,
		})
	}

	req := checkoutPlanRequest(opts, insp, fetchOutcome)
	return BuildRebasePlan(req)
}

// checkoutGatesFailed reports whether any applicable §13.7 precondition gate
// (checkoutPreconditionGates' own three rows) failed — one of §11.2 cause
// 2's own "any pre-fetch gate the described executor raises above its own
// fetch" members, and, independently, a reason BuildCheckoutRebasePlan must
// never issue a real fetch against a tree its own gates are about to refuse
// over.
func checkoutGatesFailed(gates []PlanGateResult) bool {
	for _, g := range gates {
		if g.Applies && g.Failed {
			return true
		}
	}
	return false
}

// BuildCheckoutContinuationPlan covers the --continue route (§13.7a): it
// never fetches (§11.1 rule: a continuation has already passed its one
// fetch opportunity or never had one) and never re-derives a plan — its
// entries are the persisted transaction's own remaining work, via
// RemainingRebaseEntries, exactly as InspectCheckoutPlan already measured.
func BuildCheckoutContinuationPlan(opts CheckoutSyncOpts) (RebasePlan, error) {
	insp := InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})
	req := checkoutPlanRequest(opts, insp, PlanFetchOutcome{})
	return BuildRebasePlan(req)
}
