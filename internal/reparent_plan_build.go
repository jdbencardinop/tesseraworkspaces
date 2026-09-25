package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ============================================================================
// BuildReparentPlan — the safe-reparent plan builder (§6, §7, §8).
//
// It is a PURE function of its inputs. It spawns no process, performs no
// fetch, reads no file and consults no live ref: every Git-measured fact
// reaches it through ReparentRequest, which is what makes the fingerprint a
// --plan publishes and the fingerprint a fresh execution recomputes at
// admission byte-identical by construction.
//
// It deliberately never touches the sync builder's world: no RebasePlanRequest
// is synthesized, BuildRebasePlan is not called, RevalidationDigest and
// RevalidatePlanGuardEntry are not reused (both are PlanEntry/RebasePlanRequest
// typed), and mergeBaseIsAncestor is not called (reparentIsAncestor is the
// boundary's own ancestor probe, and its answer arrives here as data).
// ============================================================================

// ReparentRowProbe is one closure row's MEASURED facts. The executor fills it
// with read-only probes before the builder runs; the just-in-time
// revalidation of §8.3 re-measures the same shape and compares digests, so
// one type serves both and the two can never drift.
type ReparentRowProbe struct {
	Name      string
	GitBranch string
	Repo      string
	Role      string
	Order     int
	Archived  bool

	// Head is the row's pre-image tip. HeadFound false is branch-ref-missing.
	HeadSHA   string
	HeadFound bool

	Materialization  string
	ExecutionContext PlanContext
	CommonDir        string

	Cutoff ReparentPlanCutoff

	// CutoffIsAncestor is `git merge-base --is-ancestor <cutoff> <branch>`,
	// already run. A false value is the hard cutoff-not-ancestor refusal:
	// Git would accept the non-ancestor boundary with exit 0 and silently
	// replay a broader set.
	CutoffIsAncestor       bool
	CutoffAncestorProbed   bool
	MergeCommitsInRange    *int
	Replay                 PlanEntryReplay
	Ancestry               PlanAncestry
	CollateralRefs         []PlanCollateralRef
	Notes                  []string
	UntrackedPaths         []string
	DirtyPaths             []string
	GitOperationInProgress string

	// Remote facts, measured from local refs only. No provider is contacted.
	RemoteRef          string
	RemoteSHA          string
	UpstreamConfigured bool
	Divergence         string
	ProviderHint       string
}

// ReparentHolderProbe is one measured worktree that holds an affected branch.
type ReparentHolderProbe struct {
	GitBranch              string
	HolderKind             string
	HolderPath             string
	PreimageSHA            string
	Prunable               bool
	Duplicate              bool
	DirtyPaths             []string
	GitOperationInProgress string

	// Excluded marks checkout mode's own computation context: §9.3 detached
	// it deliberately, so the §9.9 drift check must skip it or every
	// checkout-mode run refuses holder-unsafe against its own HEAD.
	Excluded bool
}

// ReparentRequest is BuildReparentPlan's whole input. It is a fresh struct,
// never a RebasePlanRequest: that type is sync-bound by construction and
// consumed only by BuildRebasePlan, and filling it with placeholders would
// make "RebasePlan is unchanged by this feature" depend on a second caller.
type ReparentRequest struct {
	Route      string // fresh | continue
	Invocation string // plan-only | execute

	Workspace PlanWorkspace
	Mode      WorkspaceMode
	Feature   string

	Stack       Stack
	StackBytes  []byte
	StackSHA256 string

	TargetName        string
	OntoKindRequested string
	RequestedToken    string
	CutoffSupplied    bool
	CutoffToken       string

	// Destination is ResolveReparentDestination's single result: the plan,
	// the executor, the CAS values, the metadata write and the
	// post-condition all read this one answer.
	Destination          ReparentDestination
	DestinationResolved  bool
	DestinationAgreement ReparentResolverAgreement
	OldParent            ReparentParent

	// OldParentTipSHA is the pre-image tip the old configured parent
	// resolved to; "" when it did not resolve.
	OldParentTipSHA string

	// DestinationIsAncestorOfOldParent is §7.11's surprising-but-legal case,
	// already probed.
	DestinationIsAncestorOfOldParent bool

	// DefaultBranch is the repository's default branch name, used only for
	// the destination-not-remote-rewritten disclosure.
	DefaultBranch string

	// TargetRepoRoot is the Git context the TARGET row resolves to: its
	// StackEntry.Repo when set, else the workspace root. Every genuine
	// closure row must share its common dir (§6.1 step 5), and the executor
	// mutates that repository and no other.
	TargetRepoRoot string
	// RepoIdentities maps logical entry names to canonical Git common-dir
	// identities measured before closure/destination graph construction.
	RepoIdentities map[string]string

	Rows    []ReparentRowProbe
	Holders []ReparentHolderProbe

	OriginalBranch   string
	OriginalHead     string
	OriginalDetached bool

	FetchPolicy         SyncFetchPolicy
	FetchDefaultApplied bool
	Fetch               PlanFetch
	FetchOutcome        PlanFetchOutcome

	Validation PlanValidationIdentity

	Caps         GitCapabilities
	ReparentCaps ReparentGitCapabilities

	// CapsProbed says whether `git --version` was actually run. A document
	// that refused before the probe — an unknown target, an unsortable stack
	// — publishes no capability verdict at all rather than an invented
	// "observed: " line with nothing after it.
	CapsProbed     bool
	GitVersionLine string
	RefBackend     string
	OIDWidth       int

	Guard  CheckoutPlanGuard
	Limits PlanGuardLimits

	Repositories  []PlanRepository
	Snapshot      PlanStateSnapshot
	StateFiles    ReparentPlanStateFiles
	StateWorktree PlanStateWorktree
	StateGitOp    PlanStateGitOp
	StateHead     PlanStateHead
	Exclusion     ReparentPlanExclusion

	// AuthoritativeStatePresent means LoadReparentState found an artifact of
	// any verdict. Its typed blocker must take precedence over the matching
	// compatibility envelope, which is evidence of that run rather than a
	// separate sync-state refusal.
	AuthoritativeStatePresent bool

	// RemotePending carries the logical names of still-pending follow-up
	// record entries, which raise remote-followup-pending.
	RemotePending []string

	// RemoteRecordPath is published by followup_record.path when the run
	// would write one.
	RemoteRecordPath string

	// ApprovedFingerprint is the persisted fresh-plan fingerprint, published
	// as audit evidence on the continue route only.
	ApprovedFingerprint string

	// Resumable is the continue route's own admissibility input.
	Resumable bool

	// LiveSessions names affected entries with a live (or unverifiable)
	// tws-owned session; each is a session-live blocker.
	LiveSessions []string

	// Blockers and Warnings carry findings the resolution stage already made
	// — an unresolvable destination, an ambiguous cutoff, a capability
	// refusal — so the document publishes them in the one ranked list.
	Blockers []ReparentPlanBlocker
	Warnings []ReparentPlanWarning

	EncodingIssues []PlanEncodingIssue
	ConfigIssues   []PlanConfigIssue

	// UpdateRefsConfigured is a truthy rebase.updateRefs in user or
	// repository config: neutralized by the explicit -c overrides, and
	// disclosed as a warning.
	UpdateRefsConfigured bool
}

// ============================================================================
// row.argv — the canonical, run-id-free, path-free template (§7.5b)
// ============================================================================

// reparentDescendantOperand is the literal placeholder a descendant row's
// --onto operand carries in the document and in the fingerprint preimage.
func reparentDescendantOperand(parentName string) string {
	return "<computed-tip:" + parentName + ">"
}

// reparentRowArgv is the canonical template. It contains no `-C`, no scratch
// path and no run id: execution selects its directory with exec.Cmd.Dir, and
// materialization replaces exactly one element.
func reparentRowArgv(ontoOperand, cutoff string) []string {
	return []string{
		"git",
		"-c", "rebase.backend=merge",
		"-c", "rebase.updateRefs=false",
		"-c", "rebase.autoStash=false",
		"-c", "rebase.forkPoint=false",
		"-c", "rebase.rebaseMerges=false",
		"rebase", "--merge", "--no-fork-point",
		"--no-update-refs", "--no-autostash",
		"--no-rebase-merges",
		"--onto", ontoOperand, cutoff,
	}
}

// materializeReparentArgv produces materialized_argv by replacing the single
// placeholder operand. For a target row the template already carries the
// pinned OID and the result is byte-identical to argv.
func materializeReparentArgv(argv []string, operand string) ([]string, error) {
	out := make([]string, len(argv))
	copy(out, argv)
	replaced := 0
	for i, a := range out {
		if strings.HasPrefix(a, "<computed-tip:") && strings.HasSuffix(a, ">") {
			out[i] = operand
			replaced++
		}
	}
	if replaced > 1 {
		return nil, fmt.Errorf("argv template carries %d placeholders; exactly one is allowed", replaced)
	}
	for _, a := range out {
		if a == "-C" {
			return nil, fmt.Errorf("materialized argv must never contain -C")
		}
	}
	return out, nil
}

// ============================================================================
// Pure gates
// ============================================================================

// reparentIdentityBlockers is §6.1 step 2: duplicate logical names and
// duplicate git branches among non-archived entries. Both are pure and run
// before any Git child process.
func reparentIdentityBlockers(stack Stack) []ReparentPlanBlocker {
	var out []ReparentPlanBlocker
	seenName := map[string]bool{}
	seenBranch := map[string]string{}
	for _, e := range stack.Branches {
		if seenName[e.Name] {
			out = append(out, ReparentPlanBlocker{
				Kind:   ReparentRefusalDuplicateEntryName,
				Detail: fmt.Sprintf("stack.yaml declares %q more than once", e.Name),
			})
		}
		seenName[e.Name] = true
		if e.Archived {
			continue
		}
		branch := e.GitBranch()
		if prior, dup := seenBranch[branch]; dup {
			out = append(out, ReparentPlanBlocker{
				Kind:   ReparentRefusalDuplicateGitBranch,
				Detail: fmt.Sprintf("%q and %q both resolve to git branch %q", prior, e.Name, branch),
			})
		}
		seenBranch[branch] = e.Name
	}
	return out
}

// reparentPostImageStack applies the target's new stored Base to a copy of
// the stack. It is the graph §4.5's cycle check sorts and the shape §10.1's
// metadata delta describes; nothing else in the entry is touched.
func reparentPostImageStack(stack Stack, target, storedToken string) Stack {
	out := Stack{Branches: make([]StackEntry, len(stack.Branches))}
	copy(out.Branches, stack.Branches)
	for i := range out.Branches {
		if out.Branches[i].Name == target {
			out.Branches[i].Base = storedToken
		}
	}
	return out
}

// reparentGraphBlockers is §4.5, evaluated over the POST-IMAGE logical graph
// and never over Git ancestry.
func reparentGraphBlockers(stack Stack, target string, dest ReparentDestination, closure []string, repoIdentities map[string]string) []ReparentPlanBlocker {
	var out []ReparentPlanBlocker
	if dest.Kind == ReparentParentKindStackEntry && dest.EntryName != "" {
		if dest.EntryName == target {
			out = append(out, ReparentPlanBlocker{
				Kind:   ReparentRefusalDestinationSelf,
				Detail: fmt.Sprintf("%q cannot be its own parent", target),
			})
		}
		for _, name := range closure {
			if name == target {
				continue
			}
			if name == dest.EntryName {
				out = append(out, ReparentPlanBlocker{
					Kind:   ReparentRefusalDestinationDescendant,
					Detail: fmt.Sprintf("%q is a descendant of %q; reparenting onto it would invert the stack", dest.EntryName, target),
				})
			}
		}
	}
	if dest.StoredToken != "" {
		post := reparentPostImageStack(stack, target, dest.StoredToken)
		if _, err := ReparentClosureOrderByRepoIdentity(post, target, repoIdentities); err != nil {
			out = append(out, ReparentPlanBlocker{
				Kind:   ReparentRefusalDestinationCycle,
				Detail: fmt.Sprintf("the post-image stack does not sort: %v", err),
			})
		}
	}
	return out
}

// ReparentSameParentVerdict is §4.6's answer.
type ReparentSameParentVerdict struct {
	Same    bool
	NoWork  bool
	Blocker *ReparentPlanBlocker
}

// reparentSameParentGuidance is §4.6's exact stale-edge sentence.
const reparentSameParentGuidance = "the configured parent is unchanged; replay this edge with: tws sync <feature> --from <entry>"

// evaluateReparentSameParent implements §4.6: same stored token AND same
// resolved SHA is "same parent"; a current edge then means no work and a
// non-current edge refuses destination-same-parent-stale rather than
// pretending a reparent is the right tool for a stale replay.
func evaluateReparentSameParent(currentBase, storedToken, currentParentSHA, destSHA string, status *AncestryStatus) ReparentSameParentVerdict {
	if currentBase != storedToken || currentParentSHA == "" || currentParentSHA != destSHA {
		return ReparentSameParentVerdict{}
	}
	if status != nil && *status == AncestryStatusCurrent {
		return ReparentSameParentVerdict{Same: true, NoWork: true}
	}
	b := ReparentPlanBlocker{
		Kind:   ReparentRefusalDestinationSameParentStale,
		Detail: reparentSameParentGuidance,
	}
	return ReparentSameParentVerdict{Same: true, Blocker: &b}
}

// reparentScopeBlockers is §6.2's table, over already-measured facts.
func reparentScopeBlockers(req ReparentRequest) []ReparentPlanBlocker {
	var out []ReparentPlanBlocker
	targetCommonDir := ""
	for _, row := range req.Rows {
		if row.Role == ReparentRoleTarget {
			targetCommonDir = row.CommonDir
		}
	}
	for _, row := range req.Rows {
		name := row.Name
		entry := GetBranch(req.Stack, row.Name)
		parent := GetBranch(req.Stack, entry.Base)
		if parent.Name != "" &&
			!SameStackRepo(entry.Repo, parent.Repo) &&
			req.RepoIdentities[entry.Name] != "" &&
			req.RepoIdentities[entry.Name] == req.RepoIdentities[parent.Name] {
			out = append(out, ReparentPlanBlocker{
				Kind: ReparentRefusalDestinationResolverDivergent, Entry: &name,
				Detail: fmt.Sprintf("%q uses repository token %q while parent %q uses %q; aliases of one repository are unsupported until every stack resolver compares canonical repository identity",
					entry.Name, entry.Repo, parent.Name, parent.Repo),
			})
		}
		if row.Archived {
			kind := ReparentRefusalAffectedArchived
			if row.Role == ReparentRoleTarget {
				kind = ReparentRefusalTargetArchived
			}
			out = append(out, ReparentPlanBlocker{
				Kind: kind, Entry: &name,
				Detail: fmt.Sprintf("%q is archived; restore it before reparenting", name),
			})
		}
		if !row.HeadFound {
			out = append(out, ReparentPlanBlocker{
				Kind: ReparentRefusalBranchRefMissing, Entry: &name,
				Detail: fmt.Sprintf("refs/heads/%s does not resolve", row.GitBranch),
			})
		}
		if targetCommonDir != "" && row.CommonDir != "" && row.CommonDir != targetCommonDir {
			out = append(out, ReparentPlanBlocker{
				Kind: ReparentRefusalCrossRepoClosure, Entry: &name,
				Detail: fmt.Sprintf("%q resolves to repository %s, not the target's %s", name, row.CommonDir, targetCommonDir),
			})
		}
		if row.GitOperationInProgress != "" {
			out = append(out, ReparentPlanBlocker{
				Kind: ReparentRefusalGitOperationInProgress, Entry: &name,
				Detail: fmt.Sprintf("a %s is in progress in the worktree consulted for %q", row.GitOperationInProgress, name),
			})
		}
		if len(row.DirtyPaths) > 0 {
			out = append(out, ReparentPlanBlocker{
				Kind: ReparentRefusalContextDirty, Entry: &name,
				Detail: fmt.Sprintf("the worktree consulted for %q has tracked modifications: %s", name, strings.Join(row.DirtyPaths, ", ")),
			})
		}
		if row.CutoffAncestorProbed && !row.CutoffIsAncestor {
			out = append(out, ReparentPlanBlocker{
				Kind: ReparentRefusalCutoffNotAncestor, Entry: &name,
				Detail: fmt.Sprintf("%s is not an ancestor of %s; replaying from it would rewrite commits this run never examined",
					derefString(row.Cutoff.ResolvedSHA), row.GitBranch),
			})
		}
		if row.MergeCommitsInRange != nil && *row.MergeCommitsInRange > 0 {
			out = append(out, ReparentPlanBlocker{
				Kind: ReparentRefusalMergeCommitInReplaySet, Entry: &name,
				Detail: fmt.Sprintf("%d merge commit(s) lie in %q's replay range; v1 always passes --no-rebase-merges and will not silently flatten them",
					*row.MergeCommitsInRange, name),
			})
		}
	}
	for _, h := range req.Holders {
		if h.GitOperationInProgress != "" {
			out = append(out, ReparentPlanBlocker{
				Kind:   ReparentRefusalGitOperationInProgress,
				Detail: fmt.Sprintf("a %s is in progress in affected holder %s", h.GitOperationInProgress, h.HolderPath),
			})
		}
		if len(h.DirtyPaths) > 0 {
			out = append(out, ReparentPlanBlocker{
				Kind:   ReparentRefusalContextDirty,
				Detail: fmt.Sprintf("affected holder %s has tracked modifications: %s", h.HolderPath, strings.Join(h.DirtyPaths, ", ")),
			})
		}
		if h.Excluded {
			continue
		}
		if h.Prunable {
			out = append(out, ReparentPlanBlocker{
				Kind:   ReparentRefusalHolderUnsafe,
				Detail: fmt.Sprintf("%s is held by the prunable worktree %s; prune it before reparenting", h.GitBranch, h.HolderPath),
			})
		}
		if h.Duplicate {
			out = append(out, ReparentPlanBlocker{
				Kind:   ReparentRefusalHolderUnsafe,
				Detail: fmt.Sprintf("%s is held by more than one worktree", h.GitBranch),
			})
		}
	}
	for _, name := range req.LiveSessions {
		n := name
		out = append(out, ReparentPlanBlocker{
			Kind: ReparentRefusalSessionLive, Entry: &n,
			Detail: fmt.Sprintf("%q has a live or unverifiable tws session; close it before reparenting", name),
		})
	}
	if req.CapsProbed && !req.Caps.CapRebaseUpdateRefs {
		out = append(out, ReparentPlanBlocker{
			Kind: ReparentRefusalCapabilityUnsupported,
			Detail: fmt.Sprintf("safe reparent requires git >= 2.38; observed: %s",
				strings.TrimSpace(req.GitVersionLine)),
		})
	}
	return out
}

// reparentWarningRows is §7.11's warning domain, over measured facts. A
// warning never refuses, so this list has no precedence.
func reparentWarningRows(req ReparentRequest) []ReparentPlanWarning {
	var out []ReparentPlanWarning
	dest := req.Destination
	if dest.Resolution == ReparentResolutionRefRemote {
		out = append(out, ReparentPlanWarning{
			Kind:   ReparentWarnDestinationRemoteTracking,
			Detail: fmt.Sprintf("%s is a remote-tracking ref; it moves only when you fetch", dest.StoredToken),
		})
	}
	if req.DefaultBranch != "" && strings.TrimSpace(req.RequestedToken) == req.DefaultBranch {
		out = append(out, ReparentPlanWarning{
			Kind: ReparentWarnDestinationNotRemoteRewritten,
			Detail: fmt.Sprintf("stored refs/heads/%s, not refs/remotes/%s/%s: future syncs of this edge use the local ref, never the fetched one",
				req.DefaultBranch, ReparentRemoteName, req.DefaultBranch),
		})
	}
	if req.DestinationIsAncestorOfOldParent {
		out = append(out, ReparentPlanWarning{
			Kind:   ReparentWarnDestinationAncestorOfOldParent,
			Detail: "the destination is an ancestor of the old parent tip; old-parent content leaves this subtree",
		})
	}
	if req.UpdateRefsConfigured {
		out = append(out, ReparentPlanWarning{
			Kind:   ReparentWarnCollateralUpdateRefsConfig,
			Detail: "rebase.updateRefs is configured truthy; this run neutralizes it with an explicit -c override",
		})
	}
	for _, row := range req.Rows {
		name := row.Name
		if len(row.CollateralRefs) > 0 {
			out = append(out, ReparentPlanWarning{
				Kind: ReparentWarnCollateralRefInReplayRange, Entry: &name,
				Detail: fmt.Sprintf("%d ref(s) outside the closure point into %q's replay range and are not moved", len(row.CollateralRefs), name),
			})
		}
		if len(row.UntrackedPaths) > 0 {
			out = append(out, ReparentPlanWarning{
				Kind: ReparentWarnUntrackedPresent, Entry: &name,
				Detail: fmt.Sprintf("%d untracked path(s) in the worktree consulted for %q; the just-in-time gate re-checks them before every switch", len(row.UntrackedPaths), name),
			})
		}
		if row.Divergence != "" && row.Divergence != "none" && row.Divergence != "no-upstream" {
			out = append(out, ReparentPlanWarning{
				Kind: ReparentWarnRemoteDivergenceExpected, Entry: &name,
				Detail: fmt.Sprintf("%s and %s will diverge; the remote still holds the pre-reparent history", row.GitBranch, row.RemoteRef),
			})
		}
	}
	for _, h := range req.Holders {
		if h.Excluded || h.Prunable || h.Duplicate {
			continue
		}
		out = append(out, ReparentPlanWarning{
			Kind:   ReparentWarnHolderDetachRequired,
			Detail: fmt.Sprintf("%s is held by %s and is detached for the transaction, then re-attached", h.GitBranch, h.HolderPath),
		})
	}
	for _, name := range req.RemotePending {
		n := name
		out = append(out, ReparentPlanWarning{
			Kind: ReparentWarnRemoteFollowupPending, Entry: &n,
			Detail: fmt.Sprintf("%q already has a pending reparent follow-up record entry", name),
		})
	}
	if req.RefBackend == ReparentRefBackendFiles || req.RefBackend == ReparentRefBackendUnknown {
		out = append(out, ReparentPlanWarning{
			Kind:   ReparentWarnFilesBackendNotCrashAtomic,
			Detail: "the ref transaction is race-atomic but not crash-atomic on this backend; recovery classifies every affected ref individually",
		})
	}
	return out
}

// ============================================================================
// Limit algebra adapters (§8.1, P-16)
//
// The OUTPUT type PlanGuardEvaluation is reused verbatim — §7.2 permits it
// and §7.3 field 23 requires it. Only the input row type and the emitted
// blocker type are reparent-owned, because the shipped ladder is
// PlanEntry/PlanSummary/PlanBlocker typed and reusing it would drag the sync
// refusal domain into this document.
// ============================================================================

func reparentGuardEvaluationRows(limits PlanGuardLimits, rows []ReparentPlanRow, summary ReparentPlanSummary, plannability string) []PlanGuardEvaluation {
	if plannability == ReparentPlannabilityUnavailable {
		return []PlanGuardEvaluation{}
	}
	var out []PlanGuardEvaluation
	if limits.PerEntry.Value != nil {
		for _, r := range rows {
			out = append(out, reparentPerEntryEvaluationRow(*limits.PerEntry.Value, r))
		}
	}
	if limits.Total.Value != nil {
		out = append(out, reparentTotalEvaluationRow(*limits.Total.Value, rows, summary))
	}
	return ensureSlice(out)
}

func reparentPerEntryEvaluationRow(limit int, row ReparentPlanRow) PlanGuardEvaluation {
	name := row.Name
	out := PlanGuardEvaluation{
		ID:    "max_replay_per_entry:" + name,
		Limit: limit,
		Basis: "per-entry",
		Entry: &name,
	}
	if row.Replay.Determinacy == "unknown" {
		kind := "not-resolvable"
		if row.Replay.Reason != nil && *row.Replay.Reason == "upstream-deferred" {
			kind = "deferred-resolvable"
		}
		out.Verdict = "unknown"
		out.UnknownKind = &kind
		return out
	}
	value := 0
	if row.Replay.CandidateCount != nil {
		value = *row.Replay.CandidateCount
	}
	out.Value = &value
	if value > limit {
		out.Verdict = "exceeded"
	} else {
		out.Verdict = "within-limit"
	}
	return out
}

func reparentTotalEvaluationRow(limit int, rows []ReparentPlanRow, summary ReparentPlanSummary) PlanGuardEvaluation {
	lowerBound := 0
	if summary.TotalCandidatesLowerBound != nil {
		lowerBound = *summary.TotalCandidatesLowerBound
	}
	value := lowerBound
	out := PlanGuardEvaluation{ID: "max_replay_total", Limit: limit, Basis: "total", Value: &value}

	unknown := summary.RowsWithUnknownCandidates
	if unknown == 0 {
		if value > limit {
			out.Verdict = "exceeded"
		} else {
			out.Verdict = "within-limit"
		}
		return out
	}

	out.Basis = "lower-bound"
	u := unknown
	out.UnknownEntries = &u
	if value > limit {
		out.Verdict = "exceeded"
		return out
	}
	kind := "not-resolvable"
	deferred := true
	for _, r := range rows {
		if r.Replay.Determinacy != "unknown" {
			continue
		}
		if r.Replay.Reason == nil || *r.Replay.Reason != "upstream-deferred" {
			deferred = false
		}
	}
	if deferred {
		kind = "deferred-resolvable"
	}
	out.Verdict = "unknown"
	out.UnknownKind = &kind
	return out
}

// reparentLimitRefusals maps every exceeded evaluation row to the guard
// refusal it means. The refusals are deliberately NOT ReparentPlanBlockers:
// limit-per-entry and limit-total belong to the reused sync guard domain and
// are deliberately absent from the closed 49-member ReparentRefusalKinds, so
// publishing them as blockers would put a kind with no rank at the head of
// SelectPrimaryReparentRefusal's order and would break §13.1's closed domain.
// They travel instead as *PlanGuardRefusalError, printed with the identical
// shipped `plan-guard: <kind>: <detail>` line, and are disclosed in the
// document through guard.would_refuse and guard.evaluation[].
func reparentLimitRefusals(rows []PlanGuardEvaluation) []*PlanGuardRefusalError {
	var out []*PlanGuardRefusalError
	for _, r := range rows {
		if r.Verdict != "exceeded" {
			continue
		}
		if r.Basis == "per-entry" {
			out = append(out, &PlanGuardRefusalError{
				Kind:   string(RefusalLimitPerEntry),
				Detail: fmt.Sprintf("%s: %s candidate(s) exceeds the effective per-entry replay limit of %d", derefString(r.Entry), intOrNone(r.Value), r.Limit),
			})
			continue
		}
		out = append(out, &PlanGuardRefusalError{
			Kind:   string(RefusalLimitTotal),
			Detail: fmt.Sprintf("%s candidate(s) exceeds the effective total replay limit of %d", intOrNone(r.Value), r.Limit),
		})
	}
	return out
}

// ReparentLimitRefusal is the ONE limit refusal an execution route returns:
// the first exceeded row, in evaluation order. A plan route never calls it —
// a preview discloses the exceedance and mints no usable token instead.
func ReparentLimitRefusal(plan ReparentPlan, g CheckoutPlanGuard) error {
	if !g.Guarded() || g.Approve != "" {
		return nil
	}
	if refusals := reparentLimitRefusals(plan.Guard.Evaluation); len(refusals) > 0 {
		return refusals[0]
	}
	return nil
}

// reparentLimitWaivedKinds is approval.covers.waived_kinds: the ONLY values it
// carries are the two sync limit kinds, which is exactly why the field is
// typed over the sync domain and why those kinds are deliberately absent from
// ReparentRefusalKinds.
func reparentLimitWaivedKinds(rows []PlanGuardEvaluation) []RefusalKind {
	var out []RefusalKind
	perEntry, total := false, false
	for _, r := range rows {
		if r.Verdict != "exceeded" {
			continue
		}
		if r.Basis == "per-entry" {
			perEntry = true
		} else {
			total = true
		}
	}
	if perEntry {
		out = append(out, RefusalLimitPerEntry)
	}
	if total {
		out = append(out, RefusalLimitTotal)
	}
	return ensureSlice(out)
}

// RevalidateReparentRow is §8.3's just-in-time seam: before a row is
// computed, its candidate inputs are re-measured and compared against the
// digest the approved plan bound. A divergence is a guard refusal, in the
// sync guard's own rendering, at whatever stage it occurs — the run stays
// resumable and reversible.
func RevalidateReparentRow(row ReparentPlanRow, approvedDigest string, live ReparentRowProbe) (PlanGuardEvaluation, error) {
	fresh := row
	fresh.Replay = live.Replay
	fresh.Head = PlanEntryHead{}
	if live.HeadFound {
		sha := live.HeadSHA
		state := "present"
		fresh.Head = PlanEntryHead{State: &state, SHA: &sha}
	}
	fresh.Cutoff = live.Cutoff
	digest, err := ReparentRevalidationDigest(fresh)
	if err != nil {
		return PlanGuardEvaluation{}, err
	}
	name := row.Name
	eval := PlanGuardEvaluation{
		ID:      "revalidation:" + name,
		Basis:   "per-entry",
		Entry:   &name,
		Verdict: "within-limit",
	}
	if live.Replay.CandidateCount != nil {
		v := *live.Replay.CandidateCount
		eval.Value = &v
	}
	if approvedDigest != "" && digest != approvedDigest {
		eval.Verdict = "exceeded"
		return eval, &PlanGuardRefusalError{
			Kind:           string(RefusalRevalidationMismatch),
			Detail:         fmt.Sprintf("%s: the replay inputs changed since the plan was approved", name),
			StatePreserved: true,
		}
	}
	return eval, nil
}

// ============================================================================
// Fetch projections (§3.8) — pure
// ============================================================================

// reparentFetchContext builds the PlanFetchContext for this run's single
// repository. Pure: no process, no network.
func reparentFetchContext(repoRoot, repoToken, commonDir, source string) PlanFetchContext {
	return PlanFetchContext{
		RepoToken:  repoToken,
		Root:       repoRoot,
		CommonDir:  commonDir,
		Source:     source,
		Candidates: []PlanFetchCandidate{},
	}
}

// reparentFetchPlan projects the declared policy into the reused PlanFetch
// domain. No-fetch is local-only, not a suppression; continuation is the
// existing persisted-transaction suppression.
func reparentFetchPlan(route string, policy SyncFetchPolicy, defaultApplied bool, ctx PlanFetchContext) PlanFetch {
	out := PlanFetch{
		Attempted:    false,
		Outcome:      "skipped",
		PolicySource: "flag",
		Repos:        []PlanFetchRepo{},
	}
	if route == ReparentRouteContinue {
		out.PolicySource = "persisted-transaction"
		cause := "not-refreshed-continuation"
		out.SuppressionCause = &cause
		out.MutatedRemoteTrackingRefs = fetchMutatedRemoteTrackingRefs(out.Repos)
		out.MutatedLocalBranches = fetchMutatedLocalBranches(out.Repos)
		return out
	}
	if defaultApplied {
		out.PolicySource = "route-default"
	}
	if policy != SyncFetchEnabled {
		out.MutatedRemoteTrackingRefs = fetchMutatedRemoteTrackingRefs(out.Repos)
		out.MutatedLocalBranches = fetchMutatedLocalBranches(out.Repos)
		return out
	}
	suppression := ResolveFetchSuppression([]PlanFetchContext{ctx})
	if suppression.Suppressed != "" {
		cause := suppression.Suppressed
		out.SuppressionCause = &cause
		out.Repos = []PlanFetchRepo{reparentFetchRepoFromContext(ctx)}
	}
	out.MutatedRemoteTrackingRefs = fetchMutatedRemoteTrackingRefs(out.Repos)
	out.MutatedLocalBranches = fetchMutatedLocalBranches(out.Repos)
	return out
}

// reparentFetchProjection folds a measured outcome into the fetch block,
// copying every repository/effect fact and deriving outcome, mutation and
// freshness inputs through the same helpers as the sync planner.
func reparentFetchProjection(plan PlanFetch, outcome PlanFetchOutcome) PlanFetch {
	if !outcome.Applies {
		plan.Repos = ensureSlice(plan.Repos)
		if plan.MutatedRemoteTrackingRefs == nil {
			plan.MutatedRemoteTrackingRefs = fetchMutatedRemoteTrackingRefs(plan.Repos)
		}
		if plan.MutatedLocalBranches == nil {
			plan.MutatedLocalBranches = fetchMutatedLocalBranches(plan.Repos)
		}
		return plan
	}
	if outcome.Suppressed != "" {
		cause := outcome.Suppressed
		plan.SuppressionCause = &cause
	}
	plan.Repos = make([]PlanFetchRepo, 0, len(outcome.Repos))
	for _, row := range outcome.Repos {
		plan.Repos = append(plan.Repos, PlanFetchRepo{
			RepoToken:         row.RepoToken,
			ContextRoot:       row.ContextRoot,
			ContextCommonDir:  row.ContextCommonDir,
			ContextSource:     row.ContextSource,
			Effect:            row.Effect,
			ContextCandidates: ensureSlice(row.ContextCandidates),
			Attempted:         row.Attempted,
			OK:                row.OK,
		})
	}
	plan.Attempted = false
	for _, row := range plan.Repos {
		if row.Attempted {
			plan.Attempted = true
			break
		}
	}
	plan.Outcome = fetchOutcomeString(plan.Repos)
	plan.MutatedRemoteTrackingRefs = fetchMutatedRemoteTrackingRefs(plan.Repos)
	plan.MutatedLocalBranches = fetchMutatedLocalBranches(plan.Repos)
	return plan
}

func reparentFetchRepoFromContext(ctx PlanFetchContext) PlanFetchRepo {
	return PlanFetchRepo{
		RepoToken:         ctx.RepoToken,
		ContextRoot:       ctx.Root,
		ContextCommonDir:  ctx.CommonDir,
		ContextSource:     ctx.Source,
		Effect:            ctx.Effect,
		ContextCandidates: ensureSlice(ctx.Candidates),
	}
}

// ============================================================================
// BuildReparentPlan
// ============================================================================

// BuildReparentPlan assembles the §7 document from measured inputs. It never
// refuses by returning an error: a refusal is a document fact, published in
// blockers[] and refusal, so a --plan route can print exactly what an
// execution route would have refused.
func BuildReparentPlan(req ReparentRequest) (ReparentPlan, error) {
	fetch := req.Fetch
	if fetch.PolicySource == "" {
		fetch = reparentFetchPlan(req.Route, req.FetchPolicy, req.FetchDefaultApplied, PlanFetchContext{})
	}
	plan := ReparentPlan{
		SchemaVersion:  ReparentPlanSchemaVersion,
		Route:          req.Route,
		Invocation:     req.Invocation,
		Workspace:      req.Workspace,
		Feature:        req.Feature,
		Fetch:          reparentFetchProjection(fetch, req.FetchOutcome),
		Repositories:   ensureSlice(req.Repositories),
		EncodingIssues: ensureSlice(req.EncodingIssues),
		ConfigIssues:   ensureSlice(req.ConfigIssues),
	}
	// freshness reuses the sync domain VERBATIM, through the shipped ladder,
	// so this document can never invent a thirteenth value.
	plan.Freshness = buildFreshness(plan.Fetch)

	limitsSupplied := req.Limits.PerEntry.Value != nil || req.Limits.Total.Value != nil
	limitsOrigin := ReparentLimitsOriginNone
	switch {
	case !limitsSupplied:
	case req.Route == ReparentRouteContinue:
		limitsOrigin = ReparentLimitsOriginPersisted
	default:
		limitsOrigin = ReparentLimitsOriginFlags
	}
	plan.Policy = ReparentPlanPolicy{
		Fetch:               string(req.FetchPolicy),
		FetchDefaultApplied: req.FetchDefaultApplied,
		OntoKindRequested:   req.OntoKindRequested,
		OIDWidth:            reparentOptionalInt(req.OIDWidth, req.OIDWidth == 40 || req.OIDWidth == 64),
		CutoffSupplied:      req.CutoffSupplied,
		LimitsSupplied:      limitsSupplied,
		LimitsOrigin:        limitsOrigin,
		Validation: ReparentPlanValidation{
			Applies:              req.Validation.Applies,
			Source:               reparentValidationSource(req.Validation),
			CommandDigest:        reparentOptionalString(req.Validation.Digest, req.Validation.Applies),
			RunsOn:               ReparentValidationRunsOn,
			RequiresCleanContext: true,
		},
	}

	blockers := append([]ReparentPlanBlocker{}, req.Blockers...)
	blockers = append(blockers, reparentIdentityBlockers(req.Stack)...)
	blockers = append(blockers, reparentScopeBlockers(req)...)

	closureNames := make([]string, 0, len(req.Rows))
	for _, r := range req.Rows {
		closureNames = append(closureNames, r.Name)
	}
	if req.DestinationResolved {
		blockers = append(blockers, reparentGraphBlockers(req.Stack, req.TargetName, req.Destination, closureNames, req.RepoIdentities)...)
	}

	// §4.6 — same-parent, evaluated on the target row only.
	noWork := false
	for _, row := range req.Rows {
		if row.Role != ReparentRoleTarget {
			continue
		}
		verdict := evaluateReparentSameParent(
			derefString(req.OldParent.StoredToken),
			req.Destination.StoredToken,
			derefString(req.OldParent.SHA),
			req.Destination.SHA,
			row.Ancestry.Status,
		)
		if verdict.NoWork {
			noWork = true
		}
		if verdict.Blocker != nil {
			blockers = append(blockers, *verdict.Blocker)
		}
	}
	if noWork {
		blockers = suppressReparentNoWorkOnlyBlockers(blockers)
	}

	rows := make([]ReparentPlanRow, 0, len(req.Rows))
	for _, probe := range req.Rows {
		rows = append(rows, reparentBuildRow(req, probe))
	}
	if len(rows) > 0 {
		plan.Target = rows[0]
		plan.Descendants = ensureSlice(rows[1:])
	} else {
		plan.Descendants = []ReparentPlanRow{}
	}

	plan.MetadataDelta = reparentMetadataDelta(req, rows, noWork)
	plan.Strategy = reparentStrategyBlock(req)
	plan.Holders = reparentHoldersBlock(req)
	plan.Remote = reparentRemoteBlock(req, rows)
	stateFiles := req.StateFiles
	stateFiles.ExternalRunPayload.Selected = ensureSlice(stateFiles.ExternalRunPayload.Selected)
	plan.State = ReparentPlanState{
		Snapshot:            req.Snapshot,
		Files:               stateFiles,
		Worktree:            req.StateWorktree,
		GitOp:               req.StateGitOp,
		Head:                req.StateHead,
		Exclusion:           req.Exclusion,
		ApprovedFingerprint: reparentOptionalString(req.ApprovedFingerprint, req.Route == ReparentRouteContinue && req.ApprovedFingerprint != ""),
	}

	plan.Summary = reparentSummary(rows, noWork, len(blockers) > 0)
	if !noWork && reparentBlockersMakePlanUnavailable(blockers) {
		plan.Summary.Plannability = ReparentPlannabilityUnavailable
		plan.Summary.HasWork = false
	}
	if noWork {
		n := req.TargetName
		blockers = appendReparentNoWorkBlocker(blockers, req.Invocation, n)
	}

	evaluations := reparentGuardEvaluationRows(req.Limits, rows, plan.Summary, plan.Summary.Plannability)
	if noWork {
		evaluations = []PlanGuardEvaluation{}
	}
	limitRefusals := reparentLimitRefusals(evaluations)
	waived := reparentLimitWaivedKinds(evaluations)
	approvedWaives := req.Guard.Approve != ""

	kind, ordered := SelectPrimaryReparentRefusal(blockers)
	if req.AuthoritativeStatePresent {
		kind, ordered = prioritizeReparentAuthoritativeStateBlocker(kind, ordered)
	}
	plan.Blockers = ensureSlice(ordered)
	plan.Warnings = ensureSlice(reparentWarningRows(req))
	if kind != "" {
		k := kind
		detail := ordered[0].Detail
		plan.Refusal = ReparentPlanRefusal{Kind: &k, Detail: &detail}
	}
	plan.Runnable = len(plan.Blockers) == 0 && plan.Summary.HasWork

	plan.Guard = PlanGuardBlock{
		Limits: PlanGuardLimitSet{
			MaxReplayPerEntry: req.Limits.PerEntry,
			MaxReplayTotal:    req.Limits.Total,
		},
		LimitConflicts:             []PlanGuardLimitConflict{},
		Evaluation:                 evaluations,
		IndeterminacyPolicy:        "jit-deferred",
		WouldRefuseWithoutApproval: len(limitRefusals) > 0,
		WouldRefuse:                len(limitRefusals) > 0 && !approvedWaives,
		ExecuteBlockedBy:           []ControlledPathBlocker{},
	}

	plan.Approval = reparentApproval(req, plan, waived, evaluations)
	return plan, nil
}

func prioritizeReparentAuthoritativeStateBlocker(
	kind ReparentRefusalKind,
	ordered []ReparentPlanBlocker,
) (ReparentRefusalKind, []ReparentPlanBlocker) {
	for i, blocker := range ordered {
		switch blocker.Kind {
		case ReparentRefusalStatePresent, ReparentRefusalStateUnsupported,
			ReparentRefusalStateCorrupt, ReparentRefusalStateForeign:
			if i == 0 {
				return blocker.Kind, ordered
			}
			out := make([]ReparentPlanBlocker, 0, len(ordered))
			out = append(out, blocker)
			out = append(out, ordered[:i]...)
			out = append(out, ordered[i+1:]...)
			return blocker.Kind, out
		}
	}
	return kind, ordered
}

func reparentBlockersMakePlanUnavailable(blockers []ReparentPlanBlocker) bool {
	for _, blocker := range blockers {
		switch blocker.Kind {
		case ReparentRefusalPlanUnavailable,
			ReparentRefusalStackUnsortable,
			ReparentRefusalDuplicateEntryName,
			ReparentRefusalDuplicateGitBranch,
			ReparentRefusalTargetUnknown,
			ReparentRefusalCrossRepoClosure,
			ReparentRefusalRepoUnavailable:
			return true
		}
	}
	return false
}

func suppressReparentNoWorkOnlyBlockers(blockers []ReparentPlanBlocker) []ReparentPlanBlocker {
	out := make([]ReparentPlanBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		switch blocker.Kind {
		case ReparentRefusalCutoffAbsent,
			ReparentRefusalCutoffUnresolvable,
			ReparentRefusalCutoffConflict,
			ReparentRefusalCutoffNotAncestor,
			ReparentRefusalDescendantCutoffAbsent,
			ReparentRefusalDescendantCutoffOverrideUnsupported,
			ReparentRefusalMergeCommitInReplaySet,
			ReparentRefusalCapabilityUnsupported:
			continue
		}
		out = append(out, blocker)
	}
	return out
}

// appendReparentNoWorkBlocker adds the no-work refusal on an EXECUTION route
// only: a plan route publishes plannability "no-work" with a null refusal, so
// a preview of an already-current edge is a readable answer rather than an
// error document.
func appendReparentNoWorkBlocker(blockers []ReparentPlanBlocker, invocation, target string) []ReparentPlanBlocker {
	if invocation != ReparentInvocationExecute {
		return blockers
	}
	name := target
	return append(blockers, ReparentPlanBlocker{
		Kind:   ReparentRefusalNoWork,
		Entry:  &name,
		Detail: fmt.Sprintf("%q already has this parent and its edge is current; there is nothing to replay", target),
	})
}

func reparentValidationSource(v PlanValidationIdentity) string {
	if !v.Applies || v.Source == "" {
		return "none"
	}
	return v.Source
}

func reparentOptionalString(v string, when bool) *string {
	if !when || v == "" {
		return nil
	}
	out := v
	return &out
}

func reparentOptionalInt(v int, when bool) *int {
	if !when {
		return nil
	}
	out := v
	return &out
}

// reparentBuildRow projects one measured probe into a document row. The
// target binds its destination to the pinned OID; every descendant binds to
// the computed tip of its configured parent, whose SHA cannot exist yet and
// is therefore an explicit null (§7.5a).
func reparentBuildRow(req ReparentRequest, probe ReparentRowProbe) ReparentPlanRow {
	row := ReparentPlanRow{
		Role:                probe.Role,
		Order:               probe.Order,
		Name:                probe.Name,
		GitBranch:           probe.GitBranch,
		Repo:                probe.Repo,
		Materialization:     probe.Materialization,
		ExecutionContext:    probe.ExecutionContext,
		Cutoff:              probe.Cutoff,
		Strategy:            ReparentRowStrategy,
		EffectiveBackend:    ReparentRowBackend,
		Replay:              probe.Replay,
		Ancestry:            probe.Ancestry,
		CollateralRefs:      ensureSlice(probe.CollateralRefs),
		Notes:               ensureSlice(probe.Notes),
		MergeCommitsInRange: probe.MergeCommitsInRange,
	}
	if probe.HeadFound {
		sha := probe.HeadSHA
		state := "present"
		row.Head = PlanEntryHead{State: &state, SHA: &sha}
	} else {
		state := "missing"
		row.Head = PlanEntryHead{State: &state}
	}

	entryID := ReparentEntryRefID(req.Feature, probe.Name)
	row.Pins = ReparentRowPins{
		EntryID:       entryID,
		OldRefPattern: ReparentPinNamespaceShape + "/old/" + entryID,
		NewRefPattern: ReparentPinNamespaceShape + "/new/" + entryID,
	}

	cutoff := derefString(probe.Cutoff.ResolvedSHA)
	if probe.Role == ReparentRoleTarget {
		row.OldParent = req.OldParent
		row.NewParent = req.Destination.Parent()
		row.NewParent.ResolverAgreement = req.DestinationAgreement
		row.DestinationBinding = ReparentBindingPinned
		row.DestinationSHA = reparentOptionalString(req.Destination.SHA, req.Destination.SHA != "")
		row.Argv = reparentRowArgv(req.Destination.SHA, cutoff)
		return row
	}

	parentName := reparentConfiguredParent(req.Stack, probe.Name)
	old := reparentEntryParent(req.Stack, probe.Name)
	if parentSHA := reparentRowPreimageSHA(req.Rows, parentName); parentSHA != "" {
		sha := parentSHA
		old.SHA = &sha
	}
	row.OldParent = old
	// A descendant's CONFIGURED parent does not change: only its parent's
	// tip moves. Its new_parent therefore repeats every old_parent cell and
	// carries an explicit null sha (§7.5a).
	row.NewParent = old
	row.NewParent.SHA = nil
	row.DestinationBinding = ReparentBindingParentComputed
	row.DestinationParent = reparentOptionalString(parentName, parentName != "")
	row.DestinationSHA = nil
	row.Argv = reparentRowArgv(reparentDescendantOperand(parentName), cutoff)
	return row
}

func reparentRowPreimageSHA(rows []ReparentRowProbe, name string) string {
	for _, row := range rows {
		if row.Name == name {
			return row.HeadSHA
		}
	}
	return ""
}

// reparentConfiguredParent is one row's configured parent name.
func reparentConfiguredParent(stack Stack, name string) string {
	for _, e := range stack.Branches {
		if e.Name == name {
			return e.Base
		}
	}
	return ""
}

// reparentEntryParent projects a row's configured parent edge, as stored.
func reparentEntryParent(stack Stack, name string) ReparentParent {
	base := reparentConfiguredParent(stack, name)
	out := ReparentParent{
		Kind:       ReparentParentKindNone,
		Resolution: ReparentResolutionUnresolved,
		Candidates: []string{},
	}
	if base == "" {
		return out
	}
	stored := base
	out.StoredToken = &stored
	if parent := GetBranch(stack, base); parent.Name != "" {
		ref := "refs/heads/" + parent.GitBranch()
		out.Kind = ReparentParentKindStackEntry
		out.Ref = &ref
		out.Resolution = ReparentResolutionEntry
		if parent.Repo != "" {
			repo := parent.Repo
			out.Repo = &repo
		}
		return out
	}
	out.Kind = ReparentParentKindLiteralRef
	out.Resolution = ReparentResolutionRefOther
	return out
}

// reparentMetadataDelta is §10.1's exact post-state: the target's Base becomes
// the stored token and its LastBaseSHA becomes the pinned destination OID;
// every descendant keeps its Base and takes its parent row's post-replay tip,
// which is deferred. No other field of any entry is written, and no entry
// outside the closure changes at all.
func reparentMetadataDelta(req ReparentRequest, rows []ReparentPlanRow, noWork bool) ReparentPlanMetadataDelta {
	out := ReparentPlanMetadataDelta{
		Entries:                      []ReparentPlanMetadataEntry{},
		StackSHA256Before:            req.StackSHA256,
		Writer:                       ReparentMetadataWriter,
		WritePoint:                   ReparentMetadataWritePoint,
		PostImageKnown:               false,
		EntriesOutsideClosureChanged: false,
	}
	deferred := false
	for _, row := range rows {
		entry := GetBranch(req.Stack, row.Name)
		cell := ReparentPlanMetadataEntry{
			Name:              row.Name,
			BaseBefore:        entry.Base,
			BaseAfter:         entry.Base,
			LastBaseSHABefore: reparentOptionalString(entry.LastBaseSHA, entry.LastBaseSHA != ""),
		}
		if noWork {
			cell.LastBaseSHAAfter = reparentOptionalString(entry.LastBaseSHA, entry.LastBaseSHA != "")
			cell.LastBaseSHAAfterSource = ReparentLastBaseSourceUnchanged
			cell.Changed = false
			out.Entries = append(out.Entries, cell)
			continue
		}
		if row.Role == ReparentRoleTarget {
			cell.BaseAfter = req.Destination.StoredToken
			cell.LastBaseSHAAfter = reparentOptionalString(req.Destination.SHA, req.Destination.SHA != "")
			cell.LastBaseSHAAfterSource = ReparentLastBaseSourcePinned
		} else {
			cell.LastBaseSHAAfter = nil
			cell.LastBaseSHAAfterSource = ReparentLastBaseSourceReplay
			deferred = true
		}
		cell.Changed = row.Role == ReparentRoleDescendant ||
			cell.BaseBefore != cell.BaseAfter ||
			derefString(cell.LastBaseSHABefore) != derefString(cell.LastBaseSHAAfter)
		out.Entries = append(out.Entries, cell)
	}
	if !noWork && !deferred && len(rows) > 0 {
		post := reparentPostImageStack(req.Stack, req.TargetName, req.Destination.StoredToken)
		for i := range post.Branches {
			if post.Branches[i].Name == req.TargetName {
				post.Branches[i].LastBaseSHA = req.Destination.SHA
			}
		}
		if data, err := yaml.Marshal(&post); err == nil {
			sum := sha256.Sum256(data)
			hash := hex.EncodeToString(sum[:])
			out.StackSHA256AfterExpected = &hash
			out.PostImageKnown = true
		}
	}
	return out
}

func reparentStrategyBlock(req ReparentRequest) ReparentPlanStrategy {
	context := ReparentComputationExternal
	if req.Mode == ModeCheckout {
		context = ReparentComputationCheckout
	}
	crashAtomic := reparentBackendCrashAtomic(req.RefBackend)
	recovery := ReparentRecoveryRowByRow
	if crashAtomic {
		recovery = ReparentRecoveryTransactional
	}
	return ReparentPlanStrategy{
		Kind:                ReparentStrategyKind,
		RunID:               nil,
		ComputationContext:  context,
		ComputationPath:     nil,
		Backend:             ReparentRowBackend,
		UsesGitReplay:       false,
		ForbiddenOperations: ReparentForbiddenOperations,
		PinNamespace:        ReparentPinNamespaceShape,
		Atomicity: ReparentPlanAtomicity{
			RefBackend:                      req.RefBackend,
			RefCommit:                       ReparentRefCommitKind,
			RefCommitRaceAtomic:             true,
			RefCommitCrashAtomic:            &crashAtomic,
			MetadataWriteAtomic:             true,
			MetadataWriteDurable:            true,
			CombinedAtomic:                  false,
			PartialCommitRecovery:           recovery,
			CommitPoint:                     ReparentCommitPoint,
			ReferenceTransactionHookMayVeto: true,
		},
	}
}

func reparentHoldersBlock(req ReparentRequest) ReparentPlanHolders {
	out := ReparentPlanHolders{
		Applies:          len(req.Holders) > 0,
		OriginalBranch:   reparentOptionalString(req.OriginalBranch, req.OriginalBranch != ""),
		OriginalHead:     reparentOptionalString(req.OriginalHead, req.OriginalHead != ""),
		OriginalDetached: req.OriginalDetached,
		RestorePoint:     ReparentHolderRestorePoint,
		Rows:             []ReparentPlanHolder{},
	}
	for _, h := range req.Holders {
		holderPath := h.HolderPath
		if holderPath != "" {
			holderPath = canonicalize(holderPath)
		}
		row := ReparentPlanHolder{
			GitBranch:   h.GitBranch,
			HolderKind:  h.HolderKind,
			HolderPath:  reparentOptionalString(holderPath, holderPath != ""),
			PreimageSHA: reparentOptionalString(h.PreimageSHA, h.PreimageSHA != ""),
			Action:      "detach-and-restore",
			Safe:        true,
		}
		switch {
		case h.Excluded:
			row.Action = "already-detached"
			path := h.HolderPath
			out.PreimageHolderExcluded = &path
		case h.Prunable || h.Duplicate:
			row.Action = "refuse"
			row.Safe = false
			reason := "the worktree is prunable"
			if h.Duplicate {
				reason = "the branch is held by more than one worktree"
			}
			row.Reason = &reason
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

func reparentRemoteBlock(req ReparentRequest, rows []ReparentPlanRow) ReparentPlanRemote {
	out := ReparentPlanRemote{
		ProviderCalls: 0,
		ImplicitPush:  false,
		Remote:        ReparentRemoteName,
		Rows:          []ReparentPlanRemoteRow{},
	}
	byName := map[string]ReparentRowProbe{}
	for _, p := range req.Rows {
		byName[p.Name] = p
	}
	var entries []string
	for _, row := range rows {
		probe := byName[row.Name]
		remoteRow := ReparentPlanRemoteRow{
			Name:               row.Name,
			GitBranch:          row.GitBranch,
			Remote:             ReparentRemoteName,
			RemoteRef:          reparentOptionalString(probe.RemoteRef, probe.RemoteRef != ""),
			RemoteSHA:          reparentOptionalString(probe.RemoteSHA, probe.RemoteSHA != ""),
			UpstreamConfigured: probe.UpstreamConfigured,
			Divergence:         reparentDivergenceOrUnknown(probe.Divergence),
			ProviderHint:       reparentProviderHintOrUnknown(probe.ProviderHint),
		}
		if row.Role == ReparentRoleTarget {
			remoteRow.PRBaseBefore = reparentPRBaseForParent(req.Stack, row.OldParent)
			remoteRow.PRBaseAfter = reparentPRBaseForParent(req.Stack, reparentDestinationParent(req.Destination))
		} else if row.DestinationParent != nil {
			parent := reparentPRBase(req.Stack, *row.DestinationParent)
			remoteRow.PRBaseBefore = parent
			remoteRow.PRBaseAfter = parent
		}
		out.Rows = append(out.Rows, remoteRow)
		if probe.RemoteRef != "" {
			entries = append(entries, row.Name)
		}
	}
	out.Guidance = ReparentRemoteGuidance(out.Rows)
	out.FollowupRecord = ReparentPlanRemoteFollowup{
		WillWrite:  len(rows) > 0,
		WritePoint: ReparentRemoteRecordWritePoint,
		Path:       reparentOptionalString(req.RemoteRecordPath, req.RemoteRecordPath != "" && len(rows) > 0),
		Entries:    ensureSlice(entries),
		ClearRule:  reparentRemoteClearRule,
	}
	return out
}

func reparentDestinationParent(dest ReparentDestination) ReparentParent {
	return ReparentParent{
		StoredToken: reparentOptionalString(dest.StoredToken, dest.StoredToken != ""),
		Kind:        dest.Kind,
		Ref:         dest.Ref,
		SHA:         reparentOptionalString(dest.SHA, dest.SHA != ""),
	}
}

func reparentPRBaseForParent(stack Stack, parent ReparentParent) *string {
	if ref := derefString(parent.Ref); ref != "" {
		if base := reparentPRBaseToken(ref); base != "" {
			return &base
		}
		return nil
	}
	return reparentPRBase(stack, derefString(parent.StoredToken))
}

// reparentRemoteClearRule is followup_record.clear_rule's constant text: the
// four local observations of §12.5, stated so an operator never has to guess
// what clears a row.
const reparentRemoteClearRule = "cleared locally, with no fetch and no provider call, when refs/remotes/origin/<branch> equals the recorded new tip, when it contains that tip, when a tws push of the entry succeeds and its post-push tracking value satisfies either, or when the entry leaves stack.yaml or is archived"

func reparentDivergenceOrUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func reparentProviderHintOrUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// reparentPRBase renders a parent token as a branch name suitable for a pull
// request base. An object-id destination has no branch name and is null.
func reparentPRBase(stack Stack, token string) *string {
	if token == "" {
		return nil
	}
	if parent := GetBranch(stack, token); parent.Name != "" {
		out := parent.GitBranch()
		return &out
	}
	if strings.HasPrefix(token, "refs/heads/") {
		out := strings.TrimPrefix(token, "refs/heads/")
		return &out
	}
	if strings.HasPrefix(token, "refs/remotes/") {
		parts := strings.SplitN(strings.TrimPrefix(token, "refs/remotes/"), "/", 2)
		if len(parts) == 2 {
			out := parts[1]
			return &out
		}
	}
	if strings.HasPrefix(token, "refs/") {
		return nil
	}
	if reparentHexToken.MatchString(token) {
		return nil
	}
	out := token
	return &out
}

func reparentSummary(rows []ReparentPlanRow, noWork, hasBlockers bool) ReparentPlanSummary {
	out := ReparentPlanSummary{
		Plannability:   ReparentPlannabilityRows,
		HasWork:        len(rows) > 0 && !noWork,
		Rows:           len(rows),
		CollateralRefs: []PlanCollateralRef{},
	}
	if len(rows) > 0 {
		out.Descendants = len(rows) - 1
	}
	switch {
	case len(rows) == 0:
		out.Plannability = ReparentPlannabilityUnavailable
	case noWork:
		out.Plannability = ReparentPlannabilityNoWork
	}

	total := 0
	lowerBound := 0
	maxEntry := 0
	unknown := 0
	for _, row := range rows {
		if row.DestinationBinding == ReparentBindingParentComputed {
			out.DeferredRows++
		}
		if row.Replay.CandidateCount == nil || row.Replay.Determinacy == "unknown" {
			unknown++
		} else {
			c := *row.Replay.CandidateCount
			total += c
			lowerBound += c
			if c > maxEntry {
				maxEntry = c
			}
		}
		out.CollateralRefs = append(out.CollateralRefs, row.CollateralRefs...)
	}
	out.RowsWithUnknownCandidates = unknown
	out.MaxEntryCandidates = reparentOptionalInt(maxEntry, len(rows) > 0)
	out.TotalCandidates = reparentOptionalInt(total, unknown == 0 && len(rows) > 0)
	out.TotalCandidatesLowerBound = reparentOptionalInt(lowerBound, len(rows) > 0)
	if len(rows) > 0 {
		// The enumeration is the local ref space, so the published union is
		// an UPPER bound, exactly as the sync summary's own token says.
		upper := "upper"
		out.CollateralBound = &upper
	}
	sort.SliceStable(out.CollateralRefs, func(i, j int) bool {
		if out.CollateralRefs[i].Repo != out.CollateralRefs[j].Repo {
			return out.CollateralRefs[i].Repo < out.CollateralRefs[j].Repo
		}
		return out.CollateralRefs[i].Ref < out.CollateralRefs[j].Ref
	})
	return out
}

// reparentApproval implements §7.12 and §7.12a. A fresh document mints a
// usable token only when it has work, carries no blockers and was built with
// at least one limit; a continue document mints nothing at all and publishes
// the persisted fingerprint as audit evidence in state, never as an approval.
func reparentApproval(req ReparentRequest, plan ReparentPlan, waived []RefusalKind, evaluations []PlanGuardEvaluation) ReparentPlanApproval {
	covers := ReparentPlanApprovalCovers{
		Scope:               ReparentApprovalScopeNone,
		WaivedEvaluationIDs: []string{},
		WaivedKinds:         ensureSlice(waived),
		HardBlockersWaived:  false,
		HasWork:             plan.Summary.HasWork,
		RequiresLimits:      req.Route != ReparentRouteContinue,
		EncodingSafe:        len(plan.EncodingIssues) == 0,
	}
	for _, e := range evaluations {
		if e.Verdict == "exceeded" && req.Guard.Approve != "" {
			covers.WaivedEvaluationIDs = append(covers.WaivedEvaluationIDs, e.ID)
		}
	}

	if req.Route == ReparentRouteContinue {
		covers.Scope = ReparentApprovalScopeResume
		covers.Note = "a resume inherits its gate from the approved fresh run; no new token is minted or accepted"
		return ReparentPlanApproval{
			Fingerprint: nil,
			Usable:      req.Resumable && len(plan.Blockers) == 0 && !plan.Guard.WouldRefuse,
			Scope:       ReparentApprovalScopeResume,
			Supplied:    false,
			Accepted:    nil,
			Covers:      covers,
		}
	}

	usable := plan.Runnable && plan.Summary.HasWork && plan.Policy.LimitsSupplied && len(plan.Blockers) == 0
	scope := ReparentApprovalScopeNone
	if usable {
		scope = ReparentApprovalScopeFresh
	}
	covers.Scope = scope
	covers.Note = "approves exactly this plan: this destination, these cutoffs, this closure order and these pre-image tips"

	out := ReparentPlanApproval{
		Usable:   usable,
		Scope:    scope,
		Supplied: req.Guard.Approve != "",
		Covers:   covers,
	}
	if usable {
		// The fingerprint binds approval.scope (field 33), so the scope this
		// approval publishes must already be on the document the preimage is
		// computed from — otherwise a --plan and the execution that recomputes
		// it would bind different values for the same decision.
		bound := plan
		bound.Approval = out
		fp, err := ReparentPlanFingerprint(bound)
		if err == nil {
			out.Fingerprint = &fp
			if out.Supplied {
				accepted := req.Guard.Approve == fp
				out.Accepted = &accepted
			}
		}
	}
	return out
}

// reparentPlanRefusalError projects a document's primary refusal into the
// typed error an execution route returns. It returns nil for a document that
// refuses nothing, so a caller writes one `if refusal != nil`.
func reparentPlanRefusalError(plan ReparentPlan) *ReparentRefusalError {
	if plan.Refusal.Kind == nil {
		return nil
	}
	out := &ReparentRefusalError{Kind: *plan.Refusal.Kind}
	if plan.Refusal.Detail != nil {
		out.Detail = *plan.Refusal.Detail
	}
	for _, b := range plan.Blockers {
		if b.Kind == *plan.Refusal.Kind && b.Detail == out.Detail {
			out.Entry = b.Entry
			break
		}
	}
	return out
}

// AdmitFreshReparent applies the pre-lock execution gates to the exact plan a
// fresh invocation just built. Reparent-owned blockers stay in the reparent
// refusal domain; limits and approval stay in the shared plan-guard domain.
func AdmitFreshReparent(plan ReparentPlan, g CheckoutPlanGuard) error {
	if refusal := reparentPlanRefusalError(plan); refusal != nil {
		return refusal
	}
	if err := ReparentLimitRefusal(plan, g); err != nil {
		return err
	}
	if !plan.Runnable {
		return reparentRefusal(ReparentRefusalPlanUnavailable, "the described run is not runnable")
	}
	if plan.Approval.Fingerprint == nil || g.Approve == "" || *plan.Approval.Fingerprint != g.Approve {
		return &PlanGuardRefusalError{
			Kind:   string(RefusalApprovalMismatch),
			Detail: "the supplied approval does not match this reparent plan; re-run --plan and approve the new fingerprint",
		}
	}
	return nil
}

// ReparentPlanAdmissible is §7.12 / §7.12a as ONE predicate, so automation
// and this codebase decide admission identically. Exit status is never an
// admission input.
func ReparentPlanAdmissible(plan ReparentPlan) bool {
	if !plan.Runnable || plan.Guard.WouldRefuse || len(plan.Guard.ExecuteBlockedBy) > 0 || plan.Refusal.Kind != nil {
		return false
	}
	if plan.Route == ReparentRouteContinue {
		return plan.Approval.Scope == ReparentApprovalScopeResume
	}
	return plan.Approval.Usable
}
