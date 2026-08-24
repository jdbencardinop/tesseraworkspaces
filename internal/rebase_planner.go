package internal

import (
	"os"
	"sort"
	"strings"
)

// ============================================================================
// rebase_planner.go — the pure rebase-plan decision layer (§9-§11, §14.1a).
//
// Every function here either (a) decides an output from facts its caller
// already measured, with no I/O of its own, or (b) is one of the six named
// push producers, which legitimately call the read-helpers of
// internal/rebase_plan_probe.go because measuring a push context's remote
// facts IS their job. Nothing in this file re-implements a probe that file
// already owns, and nothing here re-implements §7.1/§7.2 precedence outside
// SelectPrimaryRefusal.
//
// internal/rebase_plan_build.go is the one caller: BuildRebasePlan composes
// these functions per entry and assembles the twenty-five document keys. It
// does not exist in this file, and this file declares no document-assembly
// logic of its own.
// ============================================================================

// ============================================================================
// EntryContexts (§4.4, §9.1a Group A) — per-entry role, materialization and
// the base_context/execution_context pair.
// ============================================================================

// EntryContextInput is one stack entry's already-known facts EntryContexts
// needs to resolve its Group-A context members. It never re-loads the stack
// or the selection: both are handed in, exactly as BuildRebasePlan already
// holds them from RebasePlanRequest.
type EntryContextInput struct {
	Entry     StackEntry
	Mode      WorkspaceMode
	Layout    RebasePlanLayout
	Selection SyncSelection
}

// EntryContextResult is EntryContexts' total output: the Group-A members of
// one entries[] row, plus the concrete PlanContextIdentity BuildRebasePlan
// accumulates into the document's one shared identity table (§14.1a).
type EntryContextResult struct {
	Role            string // anchor | propagated (mirrors SyncSelectedEntry.Role)
	Materialization string // materialized | archived-metadata | worktree-missing | prunable

	ExecutionDir      string // the directory measured for ExecutionContext; "" iff unusable
	ExecutionSource   string
	ExecutionIdentity PlanContextIdentity
	ExecutionContext  PlanContext
	ExecutionErr      error // non-nil iff the context could not be established at all

	// The base half is measured in its OWN directory, which is NOT always the
	// execution directory: §4.2's context-source table splits them on exactly
	// one arm — external pass 1 with a non-empty Repo, where the base is
	// resolved in `entry-repo` while the command runs in `worktree`. That is
	// the same ladder the shipped executor's own resolveEntryBase uses
	// (§10.7 rule 3), mirrored rather than guessed.
	BaseDir      string // "" iff the base context is not-applicable or the process cwd
	BaseSource   string
	BaseIdentity PlanContextIdentity
	BaseContext  PlanContext
	BaseErr      error // non-nil iff the base context could not be established

	// StoreDivergent is rank 5.15 base-execution-store-divergent (external
	// only): both contexts established, and their measured canonical common
	// dirs differ. SameStoreSplit is the non-blocking warning
	// base-execution-context-split: two DISTINCT contexts sharing one common
	// dir. The two are mutually exclusive by construction.
	StoreDivergent bool
	SameStoreSplit bool
}

// classifyMaterialization is the one §3 four-value materialization
// classifier, shared by EntryContexts and ExecutionOrder so the two never
// disagree about which of an entry's two passes it belongs to. Checkout mode
// has no separate worktree tree, so its one repository is always
// materialized; external mode consults the stack's own Archived flag first —
// a retired entry is always inert at plan time, independent of whatever a
// stale directory on disk might still say — then the worktree directory,
// then the administrative git-worktree-prune state (internal/exec.go's
// IsPrunableWorktree, the same predicate pass 2 of the shipped executor
// consults, internal/cli/sync_helpers.go).
func classifyMaterialization(mode WorkspaceMode, entry StackEntry, layout RebasePlanLayout) string {
	if mode == ModeCheckout {
		return "materialized"
	}
	if entry.Archived {
		return "archived-metadata"
	}
	if path := layout.WorktreePath(entry.Name); path != "" {
		if _, err := os.Stat(path); err == nil {
			return "materialized"
		}
	}
	if IsPrunableWorktree(entry.GitBranch()) {
		return "prunable"
	}
	return "worktree-missing"
}

// EntryContexts resolves one entries[] row's role, materialization and
// context pair (§9.1a, §14.1a). It performs exactly one context measurement
// (EstablishContextIdentity, internal/rebase_plan_probe.go) — the same
// primitive the six push producers and the config/holder probes key their
// own context_id lookups from.
func EntryContexts(in EntryContextInput) EntryContextResult {
	role := string(in.Selection.Role(in.Entry.Name))
	materialization := classifyMaterialization(in.Mode, in.Entry, in.Layout)

	var dir, source string
	switch {
	case in.Mode == ModeCheckout:
		dir, source = in.Layout.RepoRoot, "workspace-repo-root"
	case materialization == "materialized":
		dir, source = in.Layout.WorktreePath(in.Entry.Name), "worktree"
	case in.Entry.Repo != "":
		dir, source = in.Entry.Repo, "entry-repo"
	default:
		dir, source = "", "process-cwd"
	}

	// §4.2's base-context ladder, mirrored from the shipped executor's own
	// resolveEntryBase call sites (§10.7 rule 3): the entry's Repo, else its
	// materialized worktree, else the process cwd. Checkout resolves both
	// halves in the one workspace repository root.
	baseDir, baseSource := dir, source
	if in.Mode == ModeExternal {
		switch {
		case in.Entry.Repo != "":
			baseDir, baseSource = in.Entry.Repo, "entry-repo"
		case materialization == "materialized":
			baseDir, baseSource = in.Layout.WorktreePath(in.Entry.Name), "worktree"
		default:
			baseDir, baseSource = "", "process-cwd"
		}
	}

	result := EntryContextResult{
		Role: role, Materialization: materialization,
		ExecutionDir: dir, ExecutionSource: source,
		BaseDir: baseDir, BaseSource: baseSource,
	}

	identity, err := EstablishContextIdentity(dir, source)
	if err != nil {
		result.ExecutionErr = err
		result.ExecutionContext = PlanContext{Source: source}
	} else {
		result.ExecutionIdentity = identity
		result.ExecutionContext = PlanContext{ContextID: &identity.ContextID, RepoRoot: &identity.RepoRoot, Source: source}
	}

	if in.Entry.Base == "" {
		result.BaseContext = PlanContext{Source: "not-applicable"}
		result.BaseDir, result.BaseSource = "", "not-applicable"
		return result
	}

	if baseDir == dir && baseSource == source {
		// One directory, one measurement: the two halves are the same
		// context and MUST NOT cost a second probe.
		result.BaseIdentity, result.BaseErr = result.ExecutionIdentity, result.ExecutionErr
		result.BaseContext = result.ExecutionContext
		return result
	}

	baseIdentity, baseErr := EstablishContextIdentity(baseDir, baseSource)
	if baseErr != nil {
		result.BaseErr = baseErr
		result.BaseContext = PlanContext{Source: baseSource}
		return result
	}
	result.BaseIdentity = baseIdentity
	result.BaseContext = PlanContext{ContextID: &baseIdentity.ContextID, RepoRoot: &baseIdentity.RepoRoot, Source: baseSource}

	if result.ExecutionErr == nil && baseIdentity.ContextID != identity.ContextID {
		if baseIdentity.CommonDir != identity.CommonDir {
			// Rank 5.15 base-execution-store-divergent: external only,
			// blocking. Two different object stores on one row means the
			// SHA the base half resolves is not a commit the execution half
			// can even name.
			result.StoreDivergent = in.Mode == ModeExternal
		} else {
			// Two distinct contexts sharing one common dir: the
			// non-blocking base-execution-context-split warning.
			result.SameStoreSplit = true
		}
	}
	return result
}

// ============================================================================
// ResolveSyncBase (§10) — mirrors internal/cli/sync_helpers.go's
// resolveEntryBase/resolveBase exactly, without importing package cli.
// ============================================================================

// ResolveSyncBaseResult is ResolveSyncBase's total output.
type ResolveSyncBaseResult struct {
	Base             string // "" iff Kind == "none"
	IsRemoteTracking bool   // true iff Base was rewritten to "<remote>/<default-branch>"
	Kind             string // stack-entry | literal-ref | none (PlanEntryBase.Kind)
	DependsOnName    string // the configured stack parent's logical name; "" unless Kind == stack-entry
}

// ResolveSyncBase mirrors package cli's resolveEntryBase/resolveBase
// (internal/cli/sync_helpers.go) exactly: a same-repo stack parent's own
// GitBranch() wins outright; otherwise the configured base is resolved
// through resolveBase's default-branch-to-remote-tracking rewrite. repoCtx is
// the directory DefaultBranchIn resolves the repository default branch in —
// entry.Repo for external pass 2, the worktree path for pass 1, Layout.
// RepoRoot for checkout — exactly as the two mirrored call sites use it.
func ResolveSyncBase(stack Stack, entry StackEntry, repoCtx string) ResolveSyncBaseResult {
	if entry.Base == "" {
		return ResolveSyncBaseResult{Kind: "none"}
	}
	parent := GetBranch(stack, entry.Base)
	if parent.Name != "" && SameStackRepo(parent.Repo, entry.Repo) {
		return ResolveSyncBaseResult{Base: parent.GitBranch(), Kind: "stack-entry", DependsOnName: parent.Name}
	}
	kind, dependsOn := "literal-ref", ""
	if parent.Name != "" {
		// A same-named stack entry exists but in a different repository:
		// still a configured stack dependency, just not one resolveEntryBase
		// can use directly (§10) — resolveBase treats entry.Base as a
		// literal ref from here on, exactly as the shipped executor does.
		kind, dependsOn = "stack-entry", parent.Name
	}
	var defaultBranch string
	if repoCtx == "" {
		defaultBranch = DefaultBranch()
	} else {
		defaultBranch, _ = DefaultBranchIn(repoCtx)
	}
	if entry.Base == defaultBranch {
		return ResolveSyncBaseResult{Base: "origin/" + defaultBranch, IsRemoteTracking: true, Kind: kind, DependsOnName: dependsOn}
	}
	return ResolveSyncBaseResult{Base: entry.Base, Kind: kind, DependsOnName: dependsOn}
}

// ============================================================================
// ExecutionOrder (§10, §13.3) — the two-pass active/missing execution
// sequence, mirroring internal/cli/sync_helpers.go's syncWithStackScoped
// exactly: one full topo-ordered pass over active worktrees, then a second
// full topo-ordered pass over archived/missing ones.
// ============================================================================

// OrderedExecution is one selected entry's place in the run, annotated with
// everything RebaseStrategy needs to pick its pass-dependent argv shape.
type OrderedExecution struct {
	Entry           StackEntry
	Pass            int    // 1 (active worktree, implicit HEAD) | 2 (archived/missing, explicit branch)
	Materialization string // materialized | archived-metadata | worktree-missing | prunable
	SkipAnchor      bool   // role == anchor && propagation == local-only (§10)
	UpdatedByRef    bool   // this row's ref already moves transitively via an earlier pass-1 --update-refs rebase
}

// ExecutionOrder resolves the run's ordered execution sequence over the
// caller's own TopoSort result (order), restricted to the selection. mode ==
// ModeCheckout always yields every selected entry as Pass 1 (checkout has no
// separate archived/missing worktree concept — the transaction's own Plan
// order already reflects it, internal/checkout_sync.go's BuildCheckoutPlan).
func ExecutionOrder(mode WorkspaceMode, stack Stack, order []StackEntry, layout RebasePlanLayout, sel SyncSelection) []OrderedExecution {
	scoped := sel.Policy.Scoped()
	localOnly := sel.Policy.Propagation == SyncPropagationLocalOnly

	out := make([]OrderedExecution, 0, len(order))
	idx := make(map[string]int, len(order))
	for _, entry := range order {
		if !sel.Names[entry.Name] {
			continue
		}
		materialization := classifyMaterialization(mode, entry, layout)
		pass := 1
		if mode == ModeExternal && materialization != "materialized" {
			pass = 2
		}
		role := sel.Role(entry.Name)
		idx[entry.Name] = len(out)
		out = append(out, OrderedExecution{
			Entry:           entry,
			Pass:            pass,
			Materialization: materialization,
			SkipAnchor:      role == SyncRoleAnchor && localOnly,
		})
	}

	// A pass-1, unscoped rebase always runs with --update-refs (§10, mirrored
	// from sync_helpers.go's unconditional rebaseArgs construction), which
	// carries every same-repo ancestor still "underneath" it in the replayed
	// range along for the ride — exactly the set markUpdatedAncestors marks
	// incrementally at runtime. A plan can compute the same set up front,
	// since every pass-1 row is assumed to succeed on the intended path.
	if mode == ModeExternal && !scoped {
		for i := range out {
			if out[i].Pass != 1 || out[i].SkipAnchor {
				continue
			}
			current := out[i].Entry
			for {
				parent := GetBranch(stack, current.Base)
				if parent.Name == "" || !SameStackRepo(parent.Repo, current.Repo) {
					break
				}
				if j, ok := idx[parent.Name]; ok && out[j].Pass != 1 {
					out[j].UpdatedByRef = true
				}
				current = parent
			}
		}
	}
	return out
}

// ============================================================================
// DestinationDeferred (§10) — the propagated-and-still-pending predicate.
// ============================================================================

// DestinationDeferred reports entries[].destination.deferred: true exactly
// when this is a propagated row whose configured parent is itself part of
// this run's execution set, so the final destination SHA depends on an
// outcome this plan cannot observe yet (the parent's own rebase has not run).
func DestinationDeferred(role string, parentName string, execution []OrderedExecution) bool {
	if role != string(SyncRolePropagated) || parentName == "" {
		return false
	}
	for _, oe := range execution {
		if oe.Entry.Name == parentName {
			return true
		}
	}
	return false
}

// ============================================================================
// ReplayUpstream (§5, §2.15 Group D) — the fourteen-member replay row.
// ============================================================================

// ReplayUpstreamInput is everything ReplayUpstream needs to resolve one
// entries[].replay row for one entry. Every boolean names a precondition in
// firstReplayHazard's own rank order (§5); the four candidate probes
// (internal/rebase_plan_probe.go) run only once every precondition clears.
type ReplayUpstreamInput struct {
	Skipped bool // this row's own strategy is one of the four skipped-* tokens ⇒ not-executed

	RebaseMergesConfigValid bool // repositories[].config.rebase_merges.Status == "valid" for this context
	RebaseMergesRecreates   bool // that config value, decoded, says merges are recreated along this arm

	ContextUsable bool   // the execution context resolved (repo available)
	ExecDir       string // where the four candidate probes run; "" iff !ContextUsable

	HeadUsable bool // entries[].head.state == "present" (branch resolves to a SHA)
	GitBranch  string

	BaseUnset      bool // PlanEntryBase.Kind == "none"
	BaseRefMissing bool // Base resolved to a string but does not exist as a ref
	UpstreamRef    string
	UpstreamSHA    string // "" iff BaseRefMissing

	Deferred bool // DestinationDeferred's verdict for this row

	CutoffUsage string // "used" | "not_used" (entries[].cutoff.usage)
	CutoffState string // absent | present | unresolvable; "" when CutoffUsage == "not_used"
	// CutoffResolvedSHA is entries[].cutoff.resolved_sha — the peeled recorded
	// cutoff. It is "" unless CutoffState == "present".
	CutoffResolvedSHA string
}

// effectiveUpstream is §10.1 rule 1: where a recorded cutoff is present AND
// this arm really hands it to Git (cutoff.usage == "used"), the replay's
// source boundary is that cutoff in BOTH workspace modes — never the
// destination. Every other row falls back to the base ref's own current
// resolution. The returned pair is (upstream_ref, upstream_sha); the ref is
// the cutoff for an `onto` arm and base.ref for a plain one (§4.2).
func effectiveUpstream(in ReplayUpstreamInput) (ref, sha string) {
	if in.CutoffUsage == "used" && in.CutoffState == "present" && in.CutoffResolvedSHA != "" {
		return in.CutoffResolvedSHA, in.CutoffResolvedSHA
	}
	return in.UpstreamRef, in.UpstreamSHA
}

// ReplayUpstreamResult is ReplayUpstream's total output: the fourteen
// entries[].replay members.
type ReplayUpstreamResult struct {
	UpstreamRef            *string
	UpstreamSHA            *string
	UpstreamProvenance     string
	Determinacy            string
	Reason                 *string
	Range                  *string
	CandidateCount         *int
	FirstCandidate         *PlanReplayCandidate
	Commits                []string
	CommitsListed          *int
	CommitsTruncated       *bool
	CandidateDigest        *string
	MayDropPatchEquivalent *bool
	MayDropBecomesEmpty    bool // always true (§5)
}

// firstReplayHazard is the ONE §5 eleven-token precedence ladder: the first
// hazard that applies, checked in rank order, decides both determinacy and
// reason. Nothing else in this tree re-implements this order.
func firstReplayHazard(in ReplayUpstreamInput) (determinacy string, reason *string) {
	tok := func(s string) *string { return &s }
	switch {
	case in.Skipped:
		return "not-applicable", tok("not-executed")
	case in.RebaseMergesConfigValid && in.RebaseMergesRecreates:
		return "unknown", tok("merge-recreation")
	case !in.ContextUsable:
		return "unknown", tok("repo-unavailable")
	case !in.HeadUsable:
		return "unknown", tok("head-ref-missing")
	case in.BaseUnset:
		return "unknown", tok("base-unset")
	case in.BaseRefMissing:
		return "unknown", tok("base-ref-missing")
	case in.CutoffUsage == "used" && in.CutoffState == "unresolvable":
		return "unknown", tok("cutoff-unresolvable")
	case in.Deferred:
		return "unknown", tok("upstream-deferred")
	case in.CutoffUsage == "used" && in.CutoffState == "absent":
		return "snapshot", tok("no-recorded-cutoff")
	case in.CutoffUsage == "not_used" && in.CutoffState == "present":
		return "snapshot", tok("cutoff-not-used-on-arm")
	default:
		return "exact", nil
	}
}

// upstreamProvenanceFor derives replay.upstream_provenance from the same
// hazard reason firstReplayHazard already computed (§5's four-value domain):
// a recorded cutoff actually consumed, a live read of the base ref standing
// in for one, a deferred upstream, or unknown for every other hazard.
func upstreamProvenanceFor(reason *string, cutoffUsage string) string {
	if reason == nil {
		if cutoffUsage == "used" {
			return "recorded-cutoff"
		}
		return "base-ref-snapshot"
	}
	switch *reason {
	case "upstream-deferred":
		return "base-ref-deferred"
	case "no-recorded-cutoff", "cutoff-not-used-on-arm":
		return "base-ref-snapshot"
	default:
		return "unknown"
	}
}

// ReplayUpstream resolves one entries[].replay row (§5). determinacy ==
// "exact" or "snapshot" both attempt the four candidate probes — a snapshot
// is still a real, best-effort candidate list, just not cutoff-anchored;
// every other determinacy publishes null candidate data outright. A
// candidate-probe failure encountered after committing to exact/snapshot
// downgrades this row to (unknown, probe-failed) rather than propagating a
// Go error: one entry's transient probe failure must never abort the whole
// plan build.
func ReplayUpstream(in ReplayUpstreamInput) ReplayUpstreamResult {
	determinacy, reason := firstReplayHazard(in)
	result := ReplayUpstreamResult{Determinacy: determinacy, Reason: reason, MayDropBecomesEmpty: true}
	result.UpstreamProvenance = upstreamProvenanceFor(reason, in.CutoffUsage)

	if determinacy != "exact" && determinacy != "snapshot" {
		return result
	}
	upstreamRef, upstreamSHA := effectiveUpstream(in)
	if upstreamRef != "" {
		ref := upstreamRef
		result.UpstreamRef = &ref
	}
	if upstreamSHA != "" {
		sha := upstreamSHA
		result.UpstreamSHA = &sha
	}

	// Every candidate probe takes the FULL upstream SHA as its boundary
	// operand (§5 rule 1), never a ref name or an abbreviation: a ref would
	// re-resolve inside Git and could name a different commit than the one
	// this row published.
	count, err := ProbeCandidateCount(in.ExecDir, upstreamSHA, in.GitBranch)
	if err != nil {
		return downgradeReplayToProbeFailed(result)
	}
	first, err := ProbeFirstCandidate(in.ExecDir, upstreamSHA, in.GitBranch, count)
	if err != nil {
		return downgradeReplayToProbeFailed(result)
	}
	stream, err := ProbeCandidateStream(in.ExecDir, upstreamSHA, in.GitBranch)
	if err != nil {
		return downgradeReplayToProbeFailed(result)
	}
	mayDrop, err := ProbeMayDropPatchEquivalent(in.ExecDir, upstreamSHA, in.GitBranch)
	if err != nil {
		return downgradeReplayToProbeFailed(result)
	}

	rng := upstreamSHA + ".." + in.GitBranch
	result.Range = &rng
	result.CandidateCount = &count
	result.FirstCandidate = first
	result.Commits = stream.Commits
	if result.Commits == nil {
		result.Commits = []string{}
	}
	listed := stream.Listed
	result.CommitsListed = &listed
	truncated := stream.Truncated
	result.CommitsTruncated = &truncated
	digest := stream.CandidateDigest
	result.CandidateDigest = &digest
	result.MayDropPatchEquivalent = mayDrop
	return result
}

// downgradeReplayToProbeFailed collapses an exact/snapshot row that failed a
// candidate probe partway through into the rank-2f (unknown, probe-failed)
// total value, discarding any partial candidate data already attached.
func downgradeReplayToProbeFailed(result ReplayUpstreamResult) ReplayUpstreamResult {
	reason := "probe-failed"
	return ReplayUpstreamResult{
		UpstreamRef:         result.UpstreamRef,
		UpstreamSHA:         result.UpstreamSHA,
		UpstreamProvenance:  "unknown",
		Determinacy:         "unknown",
		Reason:              &reason,
		MayDropBecomesEmpty: true,
	}
}

// ============================================================================
// RemainingRebaseEntries (§13.3) — the continuation-aware selection filter.
// ============================================================================

// RemainingEntriesState is the "state" RemainingRebaseEntries reads to tell
// already-completed selection members apart from ones a continuation still
// owes. It is this file's own carrier, not a redeclaration of either
// producer type in internal/rebase_plan_state.go: a single positional
// parameter must serve both workspace modes, and ExternalPlanState /
// CheckoutPlanState are distinct, mutually exclusive types neither of which
// alone can.
type RemainingEntriesState struct {
	Mode     WorkspaceMode
	External ExternalPlanState
	Checkout CheckoutPlanState
}

// RemainingRebaseEntries is §13.3: on a fresh run every selected name is
// remaining, in selection order (never re-sorted — §4.8 keeps several
// producer-ordered arrays as-is, and the executors consume Remaining as a
// sequence, not a set); on --continue, the already-recorded completion state
// removes whatever it names. layout and order are accepted to match the
// caller's fixed five-parameter signature; this implementation derives
// completion from state's own artefacts rather than the filesystem or the
// raw topo order, both already folded into sel.
func RemainingRebaseEntries(route string, layout RebasePlanLayout, state RemainingEntriesState, order []StackEntry, sel SyncSelection) []string {
	remaining := make([]string, 0, len(sel.Entries))
	for _, e := range sel.Entries {
		remaining = append(remaining, e.Name)
	}

	var done map[string]bool
	switch state.Mode {
	case ModeCheckout:
		done = doneNamesForCheckout(state.Checkout)
	default:
		done = doneNamesForRoute(route, state.External)
	}
	if len(done) == 0 {
		return remaining
	}
	out := make([]string, 0, len(remaining))
	for _, name := range remaining {
		if !done[name] {
			out = append(out, name)
		}
	}
	return out
}

// doneNamesForCheckout reads the persisted transaction's own progress
// cursor (§12.5a): every plan row strictly before CurrentIndex has already
// been rebased.
func doneNamesForCheckout(state CheckoutPlanState) map[string]bool {
	if !state.Applicable {
		return nil
	}
	tx := state.Files.CheckoutTransaction
	if tx.Presence != PlanPresenceReadable || tx.Transaction == nil {
		return nil
	}
	plan := tx.Transaction.Plan
	cutoff := tx.Transaction.CurrentIndex
	if cutoff > len(plan) {
		cutoff = len(plan)
	}
	if cutoff <= 0 {
		return nil
	}
	done := make(map[string]bool, cutoff)
	for i := 0; i < cutoff; i++ {
		name := plan[i].Name
		if name == "" {
			name = plan[i].Branch
		}
		done[name] = true
	}
	return done
}

// doneNamesForRoute selects the one file shape route actually persists
// completion into (§12.5a): the new-mode v2 payload's own Completed list on
// RouteNewMode, the legacy sentinel's Completed list otherwise.
func doneNamesForRoute(route string, state ExternalPlanState) map[string]bool {
	if !state.Applicable {
		return nil
	}
	var names []string
	if route == RouteNewMode {
		if p := state.Files.Payload; p.Presence == PlanPresenceReadable && p.Payload != nil {
			names = p.Payload.Completed
		}
	} else {
		if l := state.Files.LegacyState; l.Presence == PlanPresenceReadable && l.State != nil {
			names = l.State.Completed
		}
	}
	if len(names) == 0 {
		return nil
	}
	done := make(map[string]bool, len(names))
	for _, n := range names {
		done[n] = true
	}
	return done
}

// ============================================================================
// RebaseStrategy (§2.15 Group C) — the nine-token strategy totality.
// ============================================================================

// RebaseStrategyInput is everything RebaseStrategy needs to decide one
// entries[] row's Group C members. Every field is already resolved by the
// caller (ResolveSyncBase, ExecutionOrder, EntryContexts, GetBranchSHA, the
// config probes) — RebaseStrategy performs no I/O of its own.
type RebaseStrategyInput struct {
	Mode WorkspaceMode

	// Skip is "" when a real strategy must be computed; otherwise it is one
	// of the four skipped-* tokens, decided by the caller from
	// OrderedExecution/ResolveSyncBase facts (skipped-no-base, skipped-
	// anchor, skipped-archived, skipped-updated-ref).
	Skip string

	// Pass is 1 (active worktree, implicit HEAD, --update-refs-eligible) or
	// 2 (archived/missing worktree, explicit branch argument). Ignored for
	// Mode == ModeCheckout.
	Pass int

	GitBranch      string
	BaseResolved   bool
	Base           string
	LastBaseSHA    string
	CurrentBaseSHA string // "" when unresolvable

	// HeadUsable is entries[].head.state == "present" (§9.3's "missing/
	// unresolvable head" row). It only changes ModeCheckout's outcome —
	// `BuildCheckoutPlan`'s own gitResolveRef fails eagerly, so a plan
	// mirrors that with `unknown` — external still computes its normal arm
	// and lets the caller attach the blocking head-ref-missing blocker
	// instead (§9.3).
	HeadUsable bool

	Scoped bool

	// BaseMayMoveBeforeExecution is true exactly when Base names a
	// remote-tracking ref this run's own fetch policy could still move
	// between plan and execution — the condition that turns an otherwise
	// deterministic onto/plain choice into `conditional` (§10, §11.3).
	BaseMayMoveBeforeExecution bool

	ContextUsable bool

	// CheckoutOnto is the checkout-only onto/plain predicate, already
	// resolved by the caller from one CheckoutPlanEntry exactly as
	// internal/checkout_sync.go's doRebase computes it:
	// entry.LastBaseSHA != "" && entry.LastBaseSHA != entry.NewBaseSHA.
	CheckoutOnto bool

	// Effective-backend inputs (§11.7's total seven-row table, §16 rule 3a).
	//
	// BackendConfigReadable is row 2's own input: false exactly when the
	// ordered config inventory for this EXECUTION CONTEXT could not be read,
	// which makes effective_backend null whatever else is known.
	// BackendConfigValue/BackendConfigValid carry the effective
	// `rebase.backend` value and whether its final occurrence parsed;
	// an absent or invalid value takes row 5.
	BackendConfigReadable bool
	BackendConfigValue    string
	BackendConfigValid    bool

	// CapDefaultBackendMerge is TRI-STATE, because rows 3-5, row 6 and row 7
	// are three different answers: Known false means the host predates 2.26
	// (row 6, apply); Known true opens rows 3-5; !Known is row 7 (null),
	// an unparseable or unobtainable `git --version`.
	CapDefaultBackendMerge      bool
	CapDefaultBackendMergeKnown bool
}

// RebaseStrategyResult is RebaseStrategy's total output.
type RebaseStrategyResult struct {
	Strategy         string
	Condition        *string
	Argv             []string     // nullable — [] for every skipped-* row, null for conditional/unknown
	ArgvAlternatives *[2][]string // non-nil only for conditional
	EffectiveBackend *string      // nil unless a strategy actually runs
}

// rebaseArgv freezes the exact tokenized argv the shipped executors run:
// internal/cli/sync_helpers.go's two rebase-arg-building blocks (external),
// internal/checkout_sync.go's gitRebaseOnto/gitPlainRebase (checkout). This
// is the one place either could drift from what RebaseStrategy publishes.
func rebaseArgv(mode WorkspaceMode, base, branch, lastBaseSHA string, onto, scoped bool) []string {
	if mode == ModeCheckout {
		if onto {
			return []string{"rebase", "--no-fork-point", "--onto", base, lastBaseSHA}
		}
		return []string{"rebase", "--no-fork-point", base}
	}
	if branch != "" {
		// external pass 2: explicit branch, never --update-refs, never --onto.
		return []string{"rebase", base, branch}
	}
	// external pass 1: implicit HEAD.
	if scoped {
		if onto {
			return []string{"rebase", "--onto", base, lastBaseSHA}
		}
		return []string{"rebase", base}
	}
	if onto {
		return []string{"rebase", "--update-refs", "--onto", base, lastBaseSHA}
	}
	return []string{"rebase", "--update-refs", base}
}

// mergeForcingArgvOptions is §11.7 row 1's closed token list: the options
// `git rebase` itself refuses under the apply backend, so an argv carrying
// any one of them runs under the merge backend whatever the configuration
// says. `--keep-base` is deliberately ABSENT: it is not in git rebase's
// merge-backend incompatibility list and is itself incompatible with
// `--onto`, which every tws `onto` argv carries. `--root` forces merge only
// WITHOUT `--onto`, which is why it is handled separately below rather than
// listed here.
var mergeForcingArgvOptions = []string{
	"--update-refs", "--rebase-merges", "-r", "--interactive", "-i",
	"--exec", "-x", "--autosquash", "--merge", "-m",
	"--strategy", "--strategy-option", "-X",
	"--no-keep-empty", "--reapply-cherry-picks", "--no-reapply-cherry-picks",
	"--trailer",
}

// argvForcesMergeBackend is §11.7 row 1's predicate over the argv THIS ROW
// PUBLISHES. It matches exact tokens plus the two `=`-taking spellings
// (`--empty=`, and the `--exec=`/`--strategy=` families), so a row whose
// argv carries none of them falls through to the configuration rows. Only
// the `--update-refs` token is reachable from a tws-issued argv in v1 (the
// external `scope=all` arm's materialized pass-1 rows); the rest are listed
// for totality, so a later feature that builds one is classified correctly
// rather than silently misclassified.
func argvForcesMergeBackend(argv []string) bool {
	for _, tok := range argv {
		if strings.HasPrefix(tok, "--empty=") {
			return true
		}
		for _, opt := range mergeForcingArgvOptions {
			if tok == opt || (strings.HasPrefix(opt, "--") && strings.HasPrefix(tok, opt+"=")) {
				return true
			}
		}
	}
	// `--root` forces merge only when no `--onto` accompanies it.
	if argvHasFlag(argv, "--root") && !argvHasFlag(argv, "--onto") {
		return true
	}
	return false
}

// publishedArgv is the argv a row really publishes: the single argv for a
// determinate strategy, and the FIRST alternative for a `conditional` row,
// whose two alternatives always agree on whether they carry a merge-forcing
// option (both unscoped pass-1 alternatives carry --update-refs; neither
// scoped alternative does).
func publishedArgv(r RebaseStrategyResult) []string {
	if r.Argv != nil {
		return r.Argv
	}
	if r.ArgvAlternatives != nil {
		return r.ArgvAlternatives[0]
	}
	return nil
}

// effectiveBackendFor is §11.7's TOTAL seven-row table, evaluated top-down,
// first match wins. It answers exactly one question: which rebase backend
// will run the command THIS ROW publishes?
//
//  1. the published argv carries a merge-forcing option           -> merge
//  2. this execution context's ordered config inventory was unread -> null
//  3. effective rebase.backend == "apply", 2.26 capability true    -> apply
//  4. effective rebase.backend == "merge", 2.26 capability true    -> merge
//  5. rebase.backend absent/invalid,       2.26 capability true    -> merge
//  6. the 2.26 capability is known FALSE                           -> apply
//  7. the 2.26 capability is UNKNOWN                               -> null
//
// Configuration NEVER forces the merge backend: `rebase.updateRefs` and
// `rebase.rebaseMerges` are not inputs to this table in any cell, because
// Git selects the merge backend from the command line it is given and from
// `rebase.backend` alone. Publishing `merge` for a configured
// `rebase.updateRefs = true` under `rebase.backend = apply` would invert the
// two rules this value exists to gate.
func effectiveBackendFor(in RebaseStrategyInput, argv []string) *string {
	merge, apply := "merge", "apply"
	if argvForcesMergeBackend(argv) {
		return &merge
	}
	if !in.BackendConfigReadable {
		return nil
	}
	if !in.CapDefaultBackendMergeKnown {
		return nil
	}
	if !in.CapDefaultBackendMerge {
		return &apply
	}
	if in.BackendConfigValid {
		switch in.BackendConfigValue {
		case "apply":
			return &apply
		case "merge":
			return &merge
		}
	}
	return &merge
}

// RebaseStrategy is the total nine-token strategy decision (§2.15 Group C):
// skipped-no-base | skipped-anchor | skipped-archived | skipped-updated-ref |
// onto | plain | plain-explicit-branch | conditional | unknown.
func RebaseStrategy(in RebaseStrategyInput) RebaseStrategyResult {
	if in.Skip != "" {
		return RebaseStrategyResult{Strategy: in.Skip, Argv: []string{}}
	}
	if !in.ContextUsable {
		return RebaseStrategyResult{Strategy: "unknown"}
	}
	// §9.3's "missing/unresolvable head" row: only checkout turns this into
	// `unknown` (BuildCheckoutPlan's own gitResolveRef fails eagerly);
	// external keeps computing its normal arm below and the caller attaches
	// the blocking head-ref-missing blocker separately.
	if in.Mode == ModeCheckout && !in.HeadUsable {
		return RebaseStrategyResult{Strategy: "unknown"}
	}
	if !in.BaseResolved {
		// §9.3 row "unset base": checkout skips (non-blocking); external
		// keeps Base == "" and falls through to compute plain / plain-
		// explicit-branch over the quoted empty token, blocking base-unset
		// is the caller's job (never this function's).
		if in.Mode == ModeCheckout {
			return RebaseStrategyResult{Strategy: "skipped-no-base", Argv: []string{}}
		}
	}
	if in.Mode == ModeCheckout && in.BaseResolved && in.CurrentBaseSHA == "" {
		// §9.3 row "materialized/present cutoff/unresolvable destination":
		// checkout fails eagerly (`unknown`); external still returns `plain`
		// below (onto's own predicate already requires CurrentBaseSHA != "")
		// and lets Git itself fail at execution time.
		return RebaseStrategyResult{Strategy: "unknown"}
	}

	if in.Mode == ModeCheckout {
		strategy := "plain"
		ontoOperand := in.Base
		if in.CheckoutOnto {
			strategy = "onto"
			// §9.3's frozen checkout shape is
			// `rebase --no-fork-point --onto <NewBaseSHA> <LastBaseSHA>`:
			// internal/checkout_sync.go's executor calls
			// gitRebaseOnto(opts.RepoDir, entry.NewBaseSHA, entry.LastBaseSHA),
			// so the published --onto operand is the resolved destination
			// SHA, never the base ref name. The plain arm keeps entry.Base
			// verbatim, exactly as gitPlainRebase passes it.
			ontoOperand = in.CurrentBaseSHA
		}
		argv := rebaseArgv(ModeCheckout, ontoOperand, "", in.LastBaseSHA, in.CheckoutOnto, false)
		return RebaseStrategyResult{Strategy: strategy, Argv: argv, EffectiveBackend: effectiveBackendFor(in, argv)}
	}

	if in.Pass == 2 {
		argv := rebaseArgv(ModeExternal, in.Base, in.GitBranch, "", false, false)
		return RebaseStrategyResult{Strategy: "plain-explicit-branch", Argv: argv, EffectiveBackend: effectiveBackendFor(in, argv)}
	}

	// Pass 1: the onto/plain choice depends on comparing LastBaseSHA against
	// the base's current position (§10, mirrored from sync_helpers.go's
	// rebaseArgs construction).
	onto := in.LastBaseSHA != "" && in.CurrentBaseSHA != "" && in.LastBaseSHA != in.CurrentBaseSHA
	if !onto {
		argv := rebaseArgv(ModeExternal, in.Base, "", in.LastBaseSHA, false, in.Scoped)
		return RebaseStrategyResult{Strategy: "plain", Argv: argv, EffectiveBackend: effectiveBackendFor(in, argv)}
	}
	ontoArgv := rebaseArgv(ModeExternal, in.Base, "", in.LastBaseSHA, true, in.Scoped)
	if in.BaseMayMoveBeforeExecution {
		plainArgv := rebaseArgv(ModeExternal, in.Base, "", in.LastBaseSHA, false, in.Scoped)
		alts := [2][]string{ontoArgv, plainArgv}
		condition := "base-may-move-before-execution"
		// A conditional row publishes two argvs; both alternatives of a tws
		// argv agree on whether they carry a merge-forcing option (the
		// unscoped pass-1 pair both carry --update-refs, the scoped pair
		// neither), so the row's backend is that shared answer.
		return RebaseStrategyResult{Strategy: "conditional", Condition: &condition, ArgvAlternatives: &alts, EffectiveBackend: effectiveBackendFor(in, ontoArgv)}
	}
	return RebaseStrategyResult{Strategy: "onto", Argv: ontoArgv, EffectiveBackend: effectiveBackendFor(in, ontoArgv)}
}

// ============================================================================
// GateBlockers, GateControlledTokens (§4.6, §7.1, §7.5) — the
// controlled-path gate ladder's projection into blockers[] and
// guard.execute_blocked_by[].
// ============================================================================

// PlanGateVerdict is one controlled-path gate's own verdict for one
// candidate row: whether it fired, which RefusalKind names it under §7.1,
// which ControlledPathBlocker token owns it under §4.6, the entry it
// concerns (nil for a document-level gate), a sanitized detail sentence, and
// whether the described route's own ladder actually consults this gate at
// all. §4.6 fixes guard.execute_blocked_by[] membership by ownership, never
// by consequence, and blockers[] membership by whether the described route's
// own ladder would refuse on it — a verdict can be Fired without
// OnGuardedRoute (a route that never reaches this precondition), and
// GateBlockers/GateControlledTokens apply exactly that distinction.
type PlanGateVerdict struct {
	Fired          bool
	Kind           RefusalKind
	Controlled     ControlledPathBlocker
	Entry          *string
	Detail         string
	OnGuardedRoute bool
}

// GateBlockers projects the fired, on-route verdicts into blockers[] rows:
// a verdict contributes a row exactly where the route's own ladder would
// actually refuse on it, never merely because the underlying condition is
// true. Sorting/deduplication is SelectPrimaryRefusal's job, not this one's.
func GateBlockers(verdicts []PlanGateVerdict) []PlanBlocker {
	out := make([]PlanBlocker, 0, len(verdicts))
	for _, v := range verdicts {
		if !v.Fired || !v.OnGuardedRoute {
			continue
		}
		out = append(out, PlanBlocker{Kind: v.Kind, Entry: v.Entry, Detail: v.Detail})
	}
	return out
}

// GateControlledTokens projects the fired verdicts into
// guard.execute_blocked_by[]: every fired gate contributes its
// ControlledPathBlocker token regardless of whether this route's own ladder
// consults it, because the five controlled-path tokens are non-waivable
// preconditions the guard owns outright. The result is sorted and
// deduplicated in the canonical ControlledPathBlockers order (§4.8), never
// nil.
func GateControlledTokens(verdicts []PlanGateVerdict) []ControlledPathBlocker {
	fired := make(map[ControlledPathBlocker]bool, len(verdicts))
	for _, v := range verdicts {
		if v.Fired {
			fired[v.Controlled] = true
		}
	}
	out := make([]ControlledPathBlocker, 0, len(fired))
	for _, tok := range ControlledPathBlockers {
		if fired[tok] {
			out = append(out, tok)
		}
	}
	return out
}

// ============================================================================
// The six push producers (§14.1a) — ResolvePushContext, MeasurePushRemoteFacts,
// RefreshPushTrackingRefs, PushContextRefreshed, ResolvePushLease and
// PushTargets. The first five build up one PlanPushFacts value (the ONE
// push-fact carrier, RebasePlanRequest.PushFacts); PushTargets is the one of
// the six BuildRebasePlan calls directly, over the resulting facts.
//
// PlanPushFacts.Materialization and .Remotes are both keyed by repo token
// (StackEntry.Repo / PlanPushTarget.Repo — "" names the default repository,
// exactly as it does throughout this package), a convention fixed here by
// this file as the sole implementer of the code that populates and reads
// both maps: no other file in this tree writes to either map.
// ============================================================================

// ResolvePushContext is producer 1 of 6: it resolves one push context's
// identity and its own remote-resolution ladder answer, mirroring the same
// head-branch/remote-name ladder ResolveFetchEffect's remote resolution
// already uses (internal/rebase_plan_probe.go), so push and fetch can never
// disagree about which remote a context targets.
func ResolvePushContext(dir, rootSource, materialization string, inv GitConfigInventory, caps GitCapabilities) (ctx PlanPushContext, identity PlanContextIdentity, remoteName string, err error) {
	identity, err = EstablishContextIdentity(dir, rootSource)
	if err != nil {
		return PlanPushContext{}, PlanContextIdentity{}, "", err
	}
	headBranch := ResolveHeadBranch(dir)
	remoteName = ResolveRemoteName(inv, headBranch, caps)
	ctx = PlanPushContext{
		ContextID:       &identity.ContextID,
		RepoRoot:        identity.RepoRoot,
		Source:          rootSource,
		Materialization: materialization,
	}
	return ctx, identity, remoteName, nil
}

// MeasurePushRemoteFacts is producer 2 of 6 (§14.1a phase one): the ordered
// fetch-refspec read for one context/remote pair, before any tracking-ref
// read. TrackingReadOK/TrackingRefs/FetchedThisRun stay at their zero values
// here — RefreshPushTrackingRefs is the only producer that ever sets them —
// so a caller that skips that second phase (no push intent, or a policy that
// never refreshes tracking refs) still gets a well-formed, honestly
// "not yet read" facts value.
func MeasurePushRemoteFacts(contextID *string, commonDir, remoteName string, inv GitConfigInventory, caps GitCapabilities) PlanPushRemoteFacts {
	facts := PlanPushRemoteFacts{
		ContextID:     contextID,
		CommonDir:     commonDir,
		RemoteName:    remoteName,
		TrackingPhase: "not-applicable",
	}
	// §14.1a rule 5a: MappingReadOK asserts that BOTH halves of the mapping
	// question were really answered — the context's identity was established
	// (a nil ContextID is exactly "it was not") and the ordered config
	// inventory this mapping is derived from was readable. Where either is
	// missing there is no mapping to report: the facts stay
	// {MappingReadOK: false, RemoteExists: false, FetchRefspecs: []}, which
	// is what puts the row in §14.2's `expectation: unknown` cell beside its
	// rank 5.9 `probe-failed` blocker, rather than in the null-OID `absent`
	// cell a "read and empty" answer would claim.
	//
	// The inventory is the caller's OWN already-held ordered read (§22.27a's
	// one-inventory-per-context budget): this producer never re-probes it.
	if contextID == nil || !inv.Available {
		facts.FetchRefspecs = []PlanRefspec{}
		return facts
	}
	remote, found := ResolveFetchRemote(inv, commonDir, remoteName, caps)
	facts.MappingReadOK = true
	facts.RemoteExists = found
	if found {
		facts.FetchRefspecs = remote.Refspecs
	} else {
		facts.FetchRefspecs = []PlanRefspec{}
	}
	return facts
}

// RefreshPushTrackingRefs is producer 3 of 6 (§14.1a rule 7): the ONE
// remote-tracking baseline read of this invocation, performed over the WHOLE
// PlanPushFacts value that MeasurePushRemoteFacts's phase-one reads produced,
// and placed immediately after this route's fetch decision has been carried
// out — after the fetch child on a fetching arm, at the plan point on a
// no-fetch, suppressed, refusing or continuation arm — and always BEFORE
// BuildRebasePlan, because the lease's expectation/ref/SHA are approval-tuple
// members (§8.3): a pre-fetch baseline would bind a token to refs this same
// invocation then moved.
//
// A value whose Applies is false is neither measured nor refreshed: it keeps
// Phase "not-applicable" and travels to the builder unchanged, at a cost of
// zero ref reads.
//
// Per context it stamps TrackingPhase from a three-value ladder, first match
// wins — `unread` when the inventory could not be read, else `post-fetch`
// when this invocation really attempted a fetch child, else `plan-point` —
// and sets FetchedThisRun from PushContextRefreshed conjoined with
// MappingReadOK && TrackingReadOK && TrackingPhase == "post-fetch", so an
// unread half can never be labelled fresh. A context whose ContextID is nil
// has no mapping and therefore no destination to baseline: it is visited like
// any other, reads NOTHING, and keeps TrackingReadOK false with TrackingPhase
// `unread` — rule 5a's already-declared unknown cell, never a second one.
func RefreshPushTrackingRefs(facts PlanPushFacts, fetched PlanFetchOutcome) PlanPushFacts {
	if !facts.Applies {
		return facts
	}
	phase := "plan-point"
	if fetched.Attempted {
		phase = "post-fetch"
	}
	refreshed := make(map[string]PlanPushRemoteFacts, len(facts.Remotes))
	for token, rf := range facts.Remotes {
		if rf.ContextID == nil {
			rf.TrackingReadOK = false
			rf.TrackingRefs = nil
			rf.TrackingPhase = "unread"
			rf.FetchedThisRun = false
			refreshed[token] = rf
			continue
		}
		refs, err := ReadRemoteTrackingRefs(facts.Identities[*rf.ContextID].RepoRoot, rf.RemoteName)
		rf.TrackingReadOK = err == nil
		if err == nil {
			rf.TrackingRefs = refs
			rf.TrackingPhase = phase
		} else {
			rf.TrackingRefs = nil
			rf.TrackingPhase = "unread"
		}
		rf.FetchedThisRun = rf.MappingReadOK && rf.TrackingReadOK &&
			rf.TrackingPhase == "post-fetch" && PushContextRefreshed(fetched, rf.CommonDir)
		refreshed[token] = rf
	}
	facts.Remotes = refreshed
	facts.Phase = phase
	return facts
}

// PushContextRefreshed is the closed four-conjunct predicate behind
// FetchedThisRun (§14.1a rule 8). It joins on the CANONICAL COMMON DIR, not
// on context_id: a context_id hashes the canonical top level as well, so two
// linked worktrees of one repository carry two different ids while sharing
// one set of remote-tracking refs — and both are therefore refreshed by the
// single fetch §11.4's collapse rule performs.
//
// All four conjuncts are required of the SAME row: (i) a fetch child really
// ran there and exited 0; (ii) it resolved a remote and contacted it, since
// an unresolved-remote fetch writes no remote-tracking ref; (iii) one of the
// remotes it contacted is named `origin`, because the lease baseline is
// origin's tracking ref — a context whose branch.<name>.remote is `upstream`
// leaves origin exactly as stale as it was; (iv) the row's canonical common
// dir is this push context's.
func PushContextRefreshed(fetched PlanFetchOutcome, commonDir string) bool {
	if commonDir == "" {
		return false
	}
	want := canonicalPath(commonDir)
	for _, repo := range fetched.Repos {
		if !repo.Attempted || !repo.OK || repo.Effect == nil || !repo.Effect.Contacted {
			continue
		}
		if repo.ContextCommonDir == "" || canonicalPath(repo.ContextCommonDir) != want {
			continue
		}
		for _, r := range repo.Effect.Remotes {
			if r.Name == "origin" {
				return true
			}
		}
	}
	return false
}

// mapThroughRefspec maps ref through one fetch refspec's src pattern to its
// dst pattern, applying Git's single trailing "*" wildcard substitution. It
// reuses refMatchesPattern (internal/rebase_plan_probe.go) for the match test
// itself, so the two can never disagree about what "matches" means.
func mapThroughRefspec(rs PlanRefspec, ref string) (string, bool) {
	if !refMatchesPattern(rs.Src, ref) {
		return "", false
	}
	if !strings.HasSuffix(rs.Dst, "*") || !strings.HasSuffix(rs.Src, "*") {
		return rs.Dst, true
	}
	wildcard := strings.TrimPrefix(ref, strings.TrimSuffix(rs.Src, "*"))
	return strings.TrimSuffix(rs.Dst, "*") + wildcard, true
}

// ResolvePushLease is producer 5 of 6 (§2.9, §14.1a): the with-lease
// expectation derivation. mode is always "implicit-remote-tracking" (the
// only mode this feature ever publishes). It reproduces §14.2's five closed
// cells and no others:
//
//  1. positive mapping + mapped tracking ref present  -> sha,     mapped ref, value, fresh only post-fetch+fetched
//  2. positive mapping + mapped tracking ref absent   -> absent,  mapped ref, null,  fresh (the absence was read)
//  3. positive mapping + tracking inventory unread    -> absent,  mapped ref, null,  possibly-stale
//  4. mapping read, no positive mapping               -> absent,  null,       null,  fresh (it really maps nothing)
//  5. mapping unreadable (MappingReadOK: false)       -> unknown, null,       null,  possibly-stale
//
// `unknown` is reachable in EXACTLY cell 5 — an unread mapping, which always
// accompanies that context's rank 5.9 `probe-failed` blocker. A mapping that
// WAS read and matched nothing is `absent`, never `unknown`: publishing
// `unknown` there would report a real null-OID expectation as unmodelled.
// A matching negative refspec never removes the mapping (Git's refspec query
// ignores negative entries); it forces `possibly-stale` unconditionally,
// because no fetch of that configuration can ever refresh the baseline.
func ResolvePushLease(facts PlanPushRemoteFacts, gitBranch string) PlanLease {
	lease := PlanLease{Mode: "implicit-remote-tracking", Expectation: "unknown", ExpectedSHAFreshness: "possibly-stale"}
	if !facts.MappingReadOK {
		return lease // cell 5
	}
	src := "refs/heads/" + gitBranch

	negativeMatch := false
	var dst string
	mapped := false
	for _, rs := range facts.FetchRefspecs {
		if rs.Negative {
			if refMatchesPattern(rs.Src, src) {
				negativeMatch = true
			}
			continue
		}
		if !mapped {
			if d, ok := mapThroughRefspec(rs, src); ok {
				dst, mapped = d, true
			}
		}
	}

	switch {
	case !mapped:
		// Cell 4: the configuration really was read and it really maps
		// nothing, so Git expects the null OID and the read is this
		// invocation's own.
		lease.Expectation = "absent"
		lease.ExpectedSHAFreshness = "fresh"
	case !facts.TrackingReadOK:
		// Cell 3: Git's second step cannot be performed, so it falls back to
		// the null OID — but nothing was read, so nothing may be called
		// fresh.
		expectedRef := dst
		lease.ExpectedRef = &expectedRef
		lease.Expectation = "absent"
	default:
		expectedRef := dst
		lease.ExpectedRef = &expectedRef
		short := strings.TrimPrefix(dst, "refs/remotes/"+facts.RemoteName+"/")
		if sha, ok := facts.TrackingRefs[short]; ok {
			// Cell 1.
			sha := sha
			lease.Expectation, lease.ExpectedSHA = "sha", &sha
			if facts.TrackingPhase == "post-fetch" && facts.FetchedThisRun {
				lease.ExpectedSHAFreshness = "fresh"
			}
		} else {
			// Cell 2: the absence itself was read by this invocation, after
			// its own fetch decision was carried out.
			lease.Expectation = "absent"
			lease.ExpectedSHAFreshness = "fresh"
		}
	}

	if negativeMatch {
		lease.ExpectedSHAFreshness = "possibly-stale"
	}
	return lease
}

// PushTargets is producer 6 of 6 (§14.1a): the ordered push.targets[]
// projection over the run's own remaining, not-yet-pushed entries. It is the
// one push producer BuildRebasePlan calls directly; the other five feed
// req.Facts before BuildRebasePlan ever runs.
func PushTargets(req PlanPushRequest) []PlanPushTarget {
	if !req.Intent {
		return []PlanPushTarget{}
	}
	remainingSet := make(map[string]bool, len(req.Remaining))
	for _, n := range req.Remaining {
		remainingSet[n] = true
	}

	out := make([]PlanPushTarget, 0, len(req.Selection.Entries))
	for _, e := range req.Selection.Entries {
		if !remainingSet[e.Name] || req.AlreadyPushed[e.Name] {
			continue
		}
		repoToken := e.Repo
		remoteFacts := req.Facts.Remotes[repoToken]
		var contextID, repoRoot *string
		if remoteFacts.ContextID != nil {
			if identity, ok := req.Facts.Identities[*remoteFacts.ContextID]; ok {
				cid, root := identity.ContextID, identity.RepoRoot
				contextID, repoRoot = &cid, &root
			}
		}
		target := PlanPushTarget{
			Repo:            repoToken,
			ContextID:       contextID,
			RepoRoot:        repoRoot,
			Materialization: req.Facts.Materialization[repoToken],
			GitBranch:       e.GitBranch,
			Remote:          "origin",
			Ref:             "refs/heads/" + e.GitBranch,
			Force:           "with-lease",
			Lease:           ResolvePushLease(remoteFacts, e.GitBranch),
			Scope:           req.Scope,
		}
		if req.Mode == ModeCheckout {
			// §2.8 row 1: push.targets[].repo is "" on every checkout-mode row.
			target.Repo = ""
		}
		out = append(out, target)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.ContextID == nil) != (b.ContextID == nil) {
			return a.ContextID == nil
		}
		if a.ContextID != nil && *a.ContextID != *b.ContextID {
			return *a.ContextID < *b.ContextID
		}
		return a.GitBranch < b.GitBranch
	})
	return out
}

// ============================================================================
// SelectPrimaryRefusal (§7.1, §7.2) — the SOLE precedence + collapse
// implementation. Nothing else in this tree re-implements it.
// ============================================================================

// refusalRank is RefusalKinds' own index, computed once so
// SelectPrimaryRefusal's comparisons are O(1) rather than a linear scan per
// blocker.
var refusalRank = buildRefusalRank()

func buildRefusalRank() map[RefusalKind]int {
	m := make(map[RefusalKind]int, len(RefusalKinds))
	for i, k := range RefusalKinds {
		m[k] = i
	}
	return m
}

// sortAndDedupBlockers applies §4.8's blockers[] rule: sort by (rank, entry
// nil-first, sanitized detail), then collapse exact-tuple duplicates. It
// never mutates its argument's backing array and never returns nil.
func sortAndDedupBlockers(blockers []PlanBlocker) []PlanBlocker {
	cp := make([]PlanBlocker, len(blockers))
	copy(cp, blockers)
	sort.SliceStable(cp, func(i, j int) bool {
		a, b := cp[i], cp[j]
		if ra, rb := refusalRank[a.Kind], refusalRank[b.Kind]; ra != rb {
			return ra < rb
		}
		if (a.Entry == nil) != (b.Entry == nil) {
			return a.Entry == nil
		}
		if a.Entry != nil && b.Entry != nil && *a.Entry != *b.Entry {
			return *a.Entry < *b.Entry
		}
		return a.Detail < b.Detail
	})
	out := make([]PlanBlocker, 0, len(cp))
	for i, b := range cp {
		if i > 0 {
			p := cp[i-1]
			if p.Kind == b.Kind && blockerEntryEqual(p.Entry, b.Entry) && p.Detail == b.Detail {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

// blockerEntryEqual compares two blockers[].entry values, both of which are
// nil exactly for document-level facts.
func blockerEntryEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// SelectPrimaryRefusal is the SOLE implementation of §7.1 precedence and §7.2
// collapse. It returns the single highest-precedence kind among blockers —
// the zero value when blockers is empty — and the exact-tuple-deduplicated,
// §4.8-ordered blockers slice every caller must publish verbatim as
// RebasePlan.Blockers.
func SelectPrimaryRefusal(blockers []PlanBlocker) (kind RefusalKind, ordered []PlanBlocker) {
	ordered = sortAndDedupBlockers(blockers)
	if len(ordered) == 0 {
		return "", ordered
	}
	return ordered[0].Kind, ordered
}
