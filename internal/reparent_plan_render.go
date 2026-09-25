package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ============================================================================
// Reparent plan rendering (§7.14)
//
// Two renderers, mirroring FormatRebasePlan / MarshalRebasePlan exactly in
// shape: one human document and one canonical JSON document, both of which go
// to stdout and nowhere else. The mode header, fetch prose and every refusal
// go to stderr, which is why neither function here takes a writer — package
// cli binds the returned bytes to the document stream in a single write.
//
// Deferred cells (§7.5a) render as the literal "(computed at replay)" in the
// human document and as null in JSON. They are never a blank, never a zero
// and never an invented SHA.
// ============================================================================

// MarshalReparentPlan renders plan as the canonical --json document: exactly
// one compact JSON value followed by exactly one "\n", HTML-unescaped, with
// every never-null array normalized to [] rather than null. It never re-sorts
// a row array; it only ever repairs nil-vs-empty.
func MarshalReparentPlan(plan ReparentPlan) ([]byte, error) {
	if PlanEncodeFault != nil {
		if err := PlanEncodeFault(); err != nil {
			return nil, err
		}
	}
	normalized := normalizeReparentPlanArrays(plan)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalized); err != nil {
		return nil, err
	}
	// json.Encoder.Encode already appends exactly one trailing '\n'.
	return buf.Bytes(), nil
}

// normalizeReparentPlanArrays returns a copy of plan with every never-null
// array backed by an allocated (possibly empty) slice.
//
// The reused sync types keep their own documented nullable-array exceptions:
// replay.commits stays whatever the builder set, because null there means
// "not probed" and [] means "probed, empty" — collapsing them would destroy a
// distinction the fingerprint and the revalidation digest both bind.
func normalizeReparentPlanArrays(plan ReparentPlan) ReparentPlan {
	plan.Descendants = ensureSlice(plan.Descendants)
	plan.Repositories = ensureSlice(plan.Repositories)
	plan.Blockers = ensureSlice(plan.Blockers)
	plan.Warnings = ensureSlice(plan.Warnings)
	plan.EncodingIssues = ensureSlice(plan.EncodingIssues)
	plan.ConfigIssues = ensureSlice(plan.ConfigIssues)
	plan.Fetch.Repos = ensureSlice(plan.Fetch.Repos)

	plan.MetadataDelta.Entries = ensureSlice(plan.MetadataDelta.Entries)
	plan.Strategy.ForbiddenOperations = ensureSlice(plan.Strategy.ForbiddenOperations)
	plan.Holders.Rows = ensureSlice(plan.Holders.Rows)
	plan.Remote.Rows = ensureSlice(plan.Remote.Rows)
	plan.Remote.Guidance = ensureSlice(plan.Remote.Guidance)
	plan.Remote.FollowupRecord.Entries = ensureSlice(plan.Remote.FollowupRecord.Entries)

	plan.Summary.CollateralRefs = ensureSlice(plan.Summary.CollateralRefs)
	plan.Guard.Evaluation = ensureSlice(plan.Guard.Evaluation)
	plan.Guard.LimitConflicts = ensureSlice(plan.Guard.LimitConflicts)
	plan.Guard.ExecuteBlockedBy = ensureSlice(plan.Guard.ExecuteBlockedBy)
	plan.Approval.Covers.WaivedEvaluationIDs = ensureSlice(plan.Approval.Covers.WaivedEvaluationIDs)
	plan.Approval.Covers.WaivedKinds = ensureSlice(plan.Approval.Covers.WaivedKinds)

	plan.Target = normalizeReparentRowArrays(plan.Target)
	descendants := make([]ReparentPlanRow, len(plan.Descendants))
	for i, row := range plan.Descendants {
		descendants[i] = normalizeReparentRowArrays(row)
	}
	plan.Descendants = descendants

	return plan
}

func normalizeReparentRowArrays(row ReparentPlanRow) ReparentPlanRow {
	row.Argv = ensureSlice(row.Argv)
	row.CollateralRefs = ensureSlice(row.CollateralRefs)
	row.Notes = ensureSlice(row.Notes)
	row.OldParent = normalizeReparentParentArrays(row.OldParent)
	row.NewParent = normalizeReparentParentArrays(row.NewParent)
	return row
}

func normalizeReparentParentArrays(parent ReparentParent) ReparentParent {
	parent.Candidates = ensureSlice(parent.Candidates)
	parent.ResolverAgreement.Resolvers = ensureSlice(parent.ResolverAgreement.Resolvers)
	return parent
}

// FormatReparentPlan renders plan as the §7.14 human document. The target is
// visually separated from the descendants because only the target's
// configured parent changes: every descendant keeps its parent and moves only
// because its parent's tip moved.
//
// It never calls fmt.Print*, never takes an io.Writer, and never shells out.
// The error return exists so a future rendering precondition can be enforced
// without an API break, and so the encode-fault seam the sync renderer
// already honours applies here too.
func FormatReparentPlan(plan ReparentPlan) ([]byte, error) {
	if PlanEncodeFault != nil {
		if err := PlanEncodeFault(); err != nil {
			return nil, err
		}
	}
	var b strings.Builder
	formatReparentHeader(&b, plan)
	formatReparentTarget(&b, plan.Target, plan.Summary.Plannability)
	formatReparentDescendants(&b, plan.Descendants, plan.Summary.Plannability)
	formatReparentMetadata(&b, plan.MetadataDelta, plan.Summary.Plannability)
	formatReparentStrategy(&b, plan.Strategy, plan.Summary.Plannability)
	formatReparentHolders(&b, plan.Holders)
	formatReparentRemote(&b, plan.Remote)
	formatReparentBlockers(&b, plan.Blockers)
	formatReparentWarnings(&b, plan.Warnings)
	formatReparentGuard(&b, plan)
	formatReparentApproval(&b, plan.Approval)
	return []byte(b.String()), nil
}

// reparentValue renders a nullable cell: a deferred or unknown value is the
// one literal "(computed at replay)" or "none", never a blank line that an
// operator could read as a zero.
func reparentValue(v *string) string {
	if v == nil {
		return "none"
	}
	if *v == "" {
		return "\"\""
	}
	return sanitizeText(*v)
}

// reparentDeferred renders a cell that is null because the value cannot exist
// yet, as opposed to null because it is absent.
func reparentDeferred(v *string) string {
	if v == nil {
		return ReparentDeferredRendering
	}
	return sanitizeText(*v)
}

func reparentShort(v *string) string {
	if v == nil {
		return ReparentDeferredRendering
	}
	return shortSHA(*v)
}

func reparentCount(v *int) string {
	if v == nil {
		return "unknown"
	}
	return strconv.Itoa(*v)
}

func formatReparentHeader(b *strings.Builder, plan ReparentPlan) {
	fmt.Fprintf(b, "reparent plan  (schema %d, route %s)\n", plan.SchemaVersion, plan.Route)
	fmt.Fprintf(b, "Feature: %s\n", sanitizeText(plan.Feature))
	fmt.Fprintf(b, "Workspace: mode=%s invocation=%s\n", plan.Workspace.Mode, plan.Invocation)
	fmt.Fprintf(b, "Policy: fetch=%s onto-kind=%s oid-width=%s cutoff-supplied=%s limits=%s\n",
		plan.Policy.Fetch, plan.Policy.OntoKindRequested, reparentCount(plan.Policy.OIDWidth),
		yesNo(plan.Policy.CutoffSupplied), plan.Policy.LimitsOrigin)
	fmt.Fprintf(b, "Summary: plannability=%s rows=%d descendants=%d deferred=%d\n",
		plan.Summary.Plannability, plan.Summary.Rows, plan.Summary.Descendants, plan.Summary.DeferredRows)
}

func formatReparentTarget(b *strings.Builder, row ReparentPlanRow, plannability string) {
	b.WriteString("target\n")
	fmt.Fprintf(b, "  %s (branch %s)\n", sanitizeText(row.Name), sanitizeText(row.GitBranch))
	fmt.Fprintf(b, "    old parent: requested=%s stored=%s kind=%s ref=%s sha=%s\n",
		reparentValue(row.OldParent.RequestedToken), reparentValue(row.OldParent.StoredToken),
		row.OldParent.Kind, reparentValue(row.OldParent.Ref), reparentShortOrNone(row.OldParent.SHA))
	pinned := reparentShort(row.NewParent.SHA)
	if row.NewParent.SHA == nil && plannability == ReparentPlannabilityUnavailable {
		pinned = "unavailable"
	}
	fmt.Fprintf(b, "    new parent: requested=%s stored=%s kind=%s ref=%s pinned=%s\n",
		reparentValue(row.NewParent.RequestedToken), reparentValue(row.NewParent.StoredToken),
		row.NewParent.Kind, reparentValue(row.NewParent.Ref), pinned)
	formatReparentResolverAgreement(b, row.NewParent.ResolverAgreement)
	formatReparentCutoff(b, row.Cutoff)
	formatReparentReplay(b, row)
}

func formatReparentResolverAgreement(b *strings.Builder, agreement ReparentResolverAgreement) {
	if !agreement.Checked {
		b.WriteString("    resolver agreement: not checked\n")
		return
	}
	verdict := "unknown"
	if agreement.Agreed != nil {
		verdict = yesNo(*agreement.Agreed)
	}
	fmt.Fprintf(b, "    resolver agreement: agreed=%s\n", verdict)
	for _, resolver := range agreement.Resolvers {
		fmt.Fprintf(b, "      %s: sha=%s agrees=%s%s\n",
			resolver.Name, reparentShortOrNone(resolver.SHA), yesNo(resolver.Agrees),
			reparentDetailSuffix(resolver.Detail))
	}
}

func reparentDetailSuffix(detail *string) string {
	if detail == nil || *detail == "" {
		return ""
	}
	return " (" + sanitizeText(*detail) + ")"
}

func reparentShortOrNone(v *string) string {
	if v == nil {
		return "none"
	}
	return shortSHA(*v)
}

func formatReparentCutoff(b *strings.Builder, cutoff ReparentPlanCutoff) {
	ancestor := "not probed"
	if cutoff.AncestorOfBranch != nil {
		ancestor = yesNo(*cutoff.AncestorOfBranch)
	}
	fmt.Fprintf(b, "    cutoff: sha=%s provenance=%s recorded=%s ancestor-of-branch=%s\n",
		reparentShortOrNone(cutoff.ResolvedSHA), cutoff.Provenance, cutoff.RecordedState, ancestor)
	if cutoff.Conflict != nil {
		fmt.Fprintf(b, "      conflict: %s\n", sanitizeText(*cutoff.Conflict))
	}
}

func formatReparentReplay(b *strings.Builder, row ReparentPlanRow) {
	first := "none"
	if row.Replay.FirstCandidate != nil {
		first = fmt.Sprintf("%s %s", shortSHA(row.Replay.FirstCandidate.SHA), sanitizeText(row.Replay.FirstCandidate.Subject))
	}
	fmt.Fprintf(b, "    replay: %s candidate(s), first %s, determinacy %s\n",
		reparentCount(row.Replay.CandidateCount), first, row.Replay.Determinacy)
	fmt.Fprintf(b, "    merge commits in range: %s\n", reparentCount(row.MergeCommitsInRange))
}

func formatReparentDescendants(b *strings.Builder, rows []ReparentPlanRow, plannability string) {
	fmt.Fprintf(b, "descendants (%d)\n", len(rows))
	for _, row := range rows {
		fmt.Fprintf(b, "  %s (branch %s)\n", sanitizeText(row.Name), sanitizeText(row.GitBranch))
		if row.DestinationParent != nil {
			fmt.Fprintf(b, "    destination: onto computed tip of %s\n", sanitizeText(*row.DestinationParent))
		} else if plannability == ReparentPlannabilityUnavailable {
			b.WriteString("    destination: unavailable\n")
		} else {
			fmt.Fprintf(b, "    destination: %s\n", reparentDeferred(row.DestinationSHA))
		}
		formatReparentCutoff(b, row.Cutoff)
		formatReparentReplay(b, row)
	}
}

func formatReparentMetadata(b *strings.Builder, delta ReparentPlanMetadataDelta, plannability string) {
	b.WriteString("metadata changes\n")
	fmt.Fprintf(b, "  stack.yaml before: %s\n", shortSHA(delta.StackSHA256Before))
	if reparentMetadataDeltaUnchanged(delta) {
		b.WriteString("  stack.yaml after:  unchanged\n")
	} else if delta.StackSHA256AfterExpected == nil && plannability == ReparentPlannabilityUnavailable {
		b.WriteString("  stack.yaml after:  unavailable\n")
	} else {
		fmt.Fprintf(b, "  stack.yaml after:  %s\n", reparentDeferredShort(delta.StackSHA256AfterExpected))
	}
	for _, entry := range delta.Entries {
		if !entry.Changed {
			continue
		}
		fmt.Fprintf(b, "  %s: base %s -> %s, last_base_sha %s -> %s\n",
			sanitizeText(entry.Name), sanitizeText(entry.BaseBefore), sanitizeText(entry.BaseAfter),
			reparentShortOrNone(entry.LastBaseSHABefore),
			reparentMetadataAfter(entry.LastBaseSHAAfter, entry.LastBaseSHAAfterSource, plannability))
	}
	fmt.Fprintf(b, "  writer: %s at %s (post-image known: %s)\n", delta.Writer, delta.WritePoint, yesNo(delta.PostImageKnown))
}

func reparentMetadataDeltaUnchanged(delta ReparentPlanMetadataDelta) bool {
	if len(delta.Entries) == 0 || delta.StackSHA256AfterExpected != nil || delta.PostImageKnown {
		return false
	}
	for _, entry := range delta.Entries {
		if entry.Changed || entry.BaseBefore != entry.BaseAfter ||
			derefString(entry.LastBaseSHABefore) != derefString(entry.LastBaseSHAAfter) ||
			entry.LastBaseSHAAfterSource != ReparentLastBaseSourceUnchanged {
			return false
		}
	}
	return true
}

func reparentDeferredShort(v *string) string {
	if v == nil {
		return ReparentDeferredRendering
	}
	return shortSHA(*v)
}

func reparentMetadataAfter(v *string, source, plannability string) string {
	if v != nil {
		return shortSHA(*v)
	}
	if plannability == ReparentPlannabilityUnavailable {
		return "unavailable"
	}
	if source == ReparentLastBaseSourceReplay {
		return ReparentDeferredRendering
	}
	return "none"
}

func reparentPlanAssignment(v *string, plannability string) string {
	if v != nil {
		return sanitizeText(*v)
	}
	if plannability == ReparentPlannabilityUnavailable {
		return "unavailable"
	}
	return "not assigned"
}

func formatReparentStrategy(b *strings.Builder, strategy ReparentPlanStrategy, plannability string) {
	b.WriteString("strategy and atomicity\n")
	fmt.Fprintf(b, "  kind=%s backend=%s context=%s\n", strategy.Kind, strategy.Backend, strategy.ComputationContext)
	fmt.Fprintf(b, "  run id: %s, computation path: %s\n",
		reparentPlanAssignment(strategy.RunID, plannability),
		reparentPlanAssignment(strategy.ComputationPath, plannability))
	fmt.Fprintf(b, "  pins: %s\n", strategy.PinNamespace)
	a := strategy.Atomicity
	crash := "unknown"
	if a.RefCommitCrashAtomic != nil {
		crash = yesNo(*a.RefCommitCrashAtomic)
	}
	fmt.Fprintf(b, "  refs: backend=%s commit=%s race-atomic=%s crash-atomic=%s recovery=%s\n",
		a.RefBackend, a.RefCommit, yesNo(a.RefCommitRaceAtomic), crash, a.PartialCommitRecovery)
	fmt.Fprintf(b, "  metadata: atomic=%s durable=%s; combined atomic=%s; commit point=%s\n",
		yesNo(a.MetadataWriteAtomic), yesNo(a.MetadataWriteDurable), yesNo(a.CombinedAtomic), a.CommitPoint)
}

func formatReparentHolders(b *strings.Builder, holders ReparentPlanHolders) {
	b.WriteString("holders\n")
	if !holders.Applies {
		b.WriteString("  not applicable\n")
		return
	}
	fmt.Fprintf(b, "  original head: branch=%s sha=%s detached=%s\n",
		reparentValue(holders.OriginalBranch), reparentShortOrNone(holders.OriginalHead), yesNo(holders.OriginalDetached))
	for _, row := range holders.Rows {
		fmt.Fprintf(b, "  %s: kind=%s action=%s safe=%s%s\n",
			sanitizeText(row.GitBranch), row.HolderKind, row.Action, yesNo(row.Safe), reparentDetailSuffix(row.Reason))
	}
	fmt.Fprintf(b, "  restore point: %s\n", holders.RestorePoint)
}

func formatReparentRemote(b *strings.Builder, remote ReparentPlanRemote) {
	b.WriteString("remote follow-up\n")
	fmt.Fprintf(b, "  remote=%s provider calls=%d implicit push=%s\n",
		remote.Remote, remote.ProviderCalls, yesNo(remote.ImplicitPush))
	for _, row := range remote.Rows {
		fmt.Fprintf(b, "  %s: divergence=%s pr base %s -> %s\n",
			sanitizeText(row.GitBranch), row.Divergence,
			reparentValue(row.PRBaseBefore), reparentValue(row.PRBaseAfter))
	}
	for _, line := range remote.Guidance {
		fmt.Fprintf(b, "  %s\n", sanitizeText(line))
	}
	fmt.Fprintf(b, "  record: will write=%s at %s\n", yesNo(remote.FollowupRecord.WillWrite), remote.FollowupRecord.WritePoint)
}

func formatReparentBlockers(b *strings.Builder, blockers []ReparentPlanBlocker) {
	if len(blockers) == 0 {
		b.WriteString("blockers: none\n")
		return
	}
	b.WriteString("blockers:\n")
	for _, blocker := range blockers {
		fmt.Fprintf(b, "  %s%s: %s\n", blocker.Kind, reparentEntrySuffix(blocker.Entry), blocker.Detail)
	}
}

func formatReparentWarnings(b *strings.Builder, warnings []ReparentPlanWarning) {
	if len(warnings) == 0 {
		b.WriteString("warnings: none\n")
		return
	}
	b.WriteString("warnings:\n")
	for _, warning := range warnings {
		fmt.Fprintf(b, "  %s%s: %s\n", warning.Kind, reparentEntrySuffix(warning.Entry), warning.Detail)
	}
}

func reparentEntrySuffix(entry *string) string {
	if entry == nil {
		return ""
	}
	return " [" + sanitizeText(*entry) + "]"
}

func formatReparentGuard(b *strings.Builder, plan ReparentPlan) {
	b.WriteString("guard\n")
	fmt.Fprintf(b, "  limits: per-entry=%s total=%s\n",
		reparentCount(plan.Guard.Limits.MaxReplayPerEntry.Value), reparentCount(plan.Guard.Limits.MaxReplayTotal.Value))
	fmt.Fprintf(b, "  would refuse: %s\n", yesNo(plan.Guard.WouldRefuse))
	if len(plan.Guard.ExecuteBlockedBy) > 0 {
		tokens := make([]string, 0, len(plan.Guard.ExecuteBlockedBy))
		for _, token := range plan.Guard.ExecuteBlockedBy {
			tokens = append(tokens, string(token))
		}
		fmt.Fprintf(b, "  execute blocked by: %s\n", strings.Join(tokens, ", "))
	}
	fmt.Fprintf(b, "  runnable: %s\n", yesNo(plan.Runnable))
	if plan.Refusal.Kind != nil {
		detail := ""
		if plan.Refusal.Detail != nil {
			detail = *plan.Refusal.Detail
		}
		fmt.Fprintf(b, "  refusal: %s: %s\n", *plan.Refusal.Kind, detail)
	}
}

func formatReparentApproval(b *strings.Builder, approval ReparentPlanApproval) {
	b.WriteString("approval\n")
	fmt.Fprintf(b, "  fingerprint: %s\n", reparentValue(approval.Fingerprint))
	accepted := "not supplied"
	if approval.Accepted != nil {
		accepted = yesNo(*approval.Accepted)
	}
	fmt.Fprintf(b, "  usable=%s scope=%s supplied=%s accepted=%s\n",
		yesNo(approval.Usable), approval.Scope, yesNo(approval.Supplied), accepted)
	fmt.Fprintf(b, "  covers: has_work=%s requires_limits=%s hard_blockers_waived=%s\n",
		yesNo(approval.Covers.HasWork), yesNo(approval.Covers.RequiresLimits), yesNo(approval.Covers.HardBlockersWaived))
	if approval.Covers.Note != "" {
		fmt.Fprintf(b, "  note: %s\n", sanitizeText(approval.Covers.Note))
	}
}
