package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ============================================================================
// FormatRebasePlan (§6) — the stdout human document.
//
// FormatRebasePlan trusts that plan's canonically-ordered arrays (entries,
// blockers, warnings, config_issues, guard.execute_blocked_by) are already in
// §4.8 order — the same trust PlanFingerprint and MarshalRebasePlan place in
// their input — and never re-sorts a row array itself (only the derived
// head-after-run grouping, which has no JSON twin of its own, is sorted here,
// exactly as §6.2 requires).
//
// Every rendered SHA is the first twelve lowercase hex characters of the
// `--json` twin's full SHA (§5 rule 11, §6.1): this file never shells out to
// git, never uses a field's own pre-computed "short" twin. Free-text fields
// (name, git_branch, base.name, first_candidate.subject, blockers[]/
// warnings[].entry, and the head-after-run branch label) are sanitized with
// ancestrySanitize at the render boundary (internal/stack_ancestry.go:551),
// using stackStatusSanitizeLimit (internal/stack_status.go:29); already-
// sanitized document fields (repo_root, blockers[]/warnings[].detail,
// config_issues[].sanitized_value) are re-used as-is, never re-sanitized.
// ============================================================================

// FormatRebasePlan renders plan as the §6 human document: the three framing
// lines, the entries block, the head-after-run block, the blockers and
// warnings blocks, and the four-part tail, in that fixed order. It never
// calls fmt.Print*, never takes an io.Writer, and never fails in practice —
// the error return exists so a future rendering precondition can be enforced
// without an API break.
func FormatRebasePlan(plan RebasePlan) ([]byte, error) {
	if PlanEncodeFault != nil {
		if err := PlanEncodeFault(); err != nil {
			return nil, err
		}
	}
	var b strings.Builder
	formatRebasePlanFraming(&b, plan)
	formatRebasePlanEntries(&b, plan.Entries)
	formatRebasePlanHeadAfterRun(&b, plan.Entries)
	formatRebasePlanBlockers(&b, plan.Blockers)
	formatRebasePlanWarnings(&b, plan.Warnings)
	formatRebasePlanTail(&b, plan)
	return []byte(b.String()), nil
}

// shortSHA returns the first twelve lowercase hex characters of a full SHA.
func shortSHA(sha string) string {
	sha = strings.ToLower(sha)
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

// sanitizeText is the shared render-time free-text sanitizer: every field the
// eleven-field spelling table (§6.1) or the blockers/warnings block (§6.2a)
// marks "sanitized" goes through this one call.
func sanitizeText(s string) string {
	return ancestrySanitize(s, stackStatusSanitizeLimit)
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// ============================================================================
// Framing (§6, L1567-1580)
// ============================================================================

func formatRebasePlanFraming(b *strings.Builder, plan RebasePlan) {
	fmt.Fprintf(b, "Plan: route=%s mode=%s invocation=%s\n", plan.Route, plan.Workspace.Mode, plan.Invocation)
	fmt.Fprintf(b, "Policy: fetch=%s propagation=%s scope=%s\n", plan.Policy.Fetch, plan.Policy.Propagation, rebasePlanScopeLabel(plan.Policy))
	fmt.Fprintf(b, "Freshness: %s\n", plan.Freshness)
}

// rebasePlanScopeLabel reimplements SyncRunPolicy.ScopeLabel() byte-for-byte
// (internal/sync_selection.go:148-157) over PlanPolicy, since RebasePlan
// carries only the document-shaped policy, never the shipped runtime type.
func rebasePlanScopeLabel(policy PlanPolicy) string {
	selector := ""
	if policy.Selector != nil {
		selector = *policy.Selector
	}
	switch policy.ScopeKind {
	case "one":
		return "only:" + selector
	case "subtree":
		return "subtree:" + selector
	default:
		return "all"
	}
}

// ============================================================================
// §6.1 Entries block
// ============================================================================

func formatRebasePlanEntries(b *strings.Builder, entries []PlanEntry) {
	if len(entries) == 0 {
		b.WriteString("Entries: none\n")
		return
	}
	b.WriteString("Entries:\n")
	for _, entry := range entries {
		formatRebasePlanEntryRow(b, entry)
	}
}

func formatRebasePlanEntryRow(b *strings.Builder, entry PlanEntry) {
	isSkipped := strings.HasPrefix(entry.Strategy, "skipped-")
	name := sanitizeText(entry.Name)
	gitBranch := sanitizeText(entry.GitBranch)

	configured := "<none>"
	if entry.Base.Name != nil {
		configured = sanitizeText(*entry.Base.Name)
	}
	resolved := "<unresolved>"
	if entry.Base.Ref != nil {
		resolved = *entry.Base.Ref
	}

	fmt.Fprintf(b, "  - %s [%s] base %s \u2192 %s@%s cutoff %s upstream %s strategy %s\n",
		name, gitBranch, configured, resolved,
		rebasePlanEntryDestination(entry, isSkipped),
		rebasePlanEntryRecorded(entry),
		rebasePlanEntryEffective(entry, isSkipped),
		entry.Strategy)
	fmt.Fprintf(b, "    candidates %s range %s first %s\n",
		rebasePlanEntryCount(entry, isSkipped),
		rebasePlanEntryRange(entry, gitBranch),
		rebasePlanEntryFirst(entry))
}

// rebasePlanEntryDestination is the eleven-field table's <destination>.
func rebasePlanEntryDestination(entry PlanEntry, isSkipped bool) string {
	switch {
	case isSkipped:
		return "<not-executed>"
	case entry.Destination.Deferred:
		dependsOn := "<none>"
		if entry.Destination.DependsOn != nil {
			dependsOn = sanitizeText(*entry.Destination.DependsOn)
		}
		return "post-rebase(" + dependsOn + ")"
	case entry.Destination.SHA != nil:
		return shortSHA(*entry.Destination.SHA)
	default:
		return "<unresolved>"
	}
}

// rebasePlanEntryRecorded is the eleven-field table's <recorded>.
func rebasePlanEntryRecorded(entry PlanEntry) string {
	if entry.Cutoff.RecordedSHA == nil {
		return "<none>"
	}
	recorded := shortSHA(*entry.Cutoff.RecordedSHA)
	switch {
	case entry.Cutoff.State != nil && *entry.Cutoff.State == "unresolvable":
		recorded += " (unresolvable)"
	case entry.Cutoff.State == nil:
		recorded += " (unverified)"
	}
	return recorded
}

// rebasePlanEntryEffective is the eleven-field table's <effective>.
func rebasePlanEntryEffective(entry PlanEntry, isSkipped bool) string {
	if isSkipped {
		return "<not-executed>"
	}
	if entry.Replay.UpstreamSHA == nil {
		return "<unknown>"
	}
	return shortSHA(*entry.Replay.UpstreamSHA)
}

// rebasePlanEntryCount is the eleven-field table's <count>.
func rebasePlanEntryCount(entry PlanEntry, isSkipped bool) string {
	if isSkipped {
		return "0"
	}
	if entry.Replay.CandidateCount == nil {
		return "<unknown>"
	}
	return strconv.Itoa(*entry.Replay.CandidateCount)
}

// rebasePlanEntryRange is the eleven-field table's <range>: composed from
// replay.upstream_sha and the sanitized git_branch, never a copy of
// replay.range (which carries the full upstream SHA).
func rebasePlanEntryRange(entry PlanEntry, sanitizedGitBranch string) string {
	if entry.Replay.Range == nil {
		return "<none>"
	}
	upstream := "<unknown>"
	if entry.Replay.UpstreamSHA != nil {
		upstream = shortSHA(*entry.Replay.UpstreamSHA)
	}
	return upstream + ".." + sanitizedGitBranch
}

// rebasePlanEntryFirst is the eleven-field table's <first>.
func rebasePlanEntryFirst(entry PlanEntry) string {
	if entry.Replay.CandidateCount == nil {
		return "<unknown>"
	}
	if *entry.Replay.CandidateCount == 0 {
		return "<none>"
	}
	if entry.Replay.FirstCandidate == nil {
		return "<unknown>"
	}
	fc := entry.Replay.FirstCandidate
	return shortSHA(fc.SHA) + ` "` + sanitizeText(fc.Subject) + `"`
}

// ============================================================================
// §6.2 Head-after-run block
// ============================================================================

func formatRebasePlanHeadAfterRun(b *strings.Builder, entries []PlanEntry) {
	branchByRoot := make(map[string]string)
	for _, entry := range entries {
		if !entry.Mutation.WillSwitchHead || entry.Mutation.HeadRestoredByRun {
			continue
		}
		if entry.Mutation.WillLeaveHeadOn == nil {
			continue
		}
		root := ""
		if entry.ExecutionContext.RepoRoot != nil {
			root = *entry.ExecutionContext.RepoRoot
		}
		// The last switching, non-restored row of this context wins — plan.Entries
		// is iterated in its given (canonical §4.8) order.
		branchByRoot[root] = sanitizeText(*entry.Mutation.WillLeaveHeadOn)
	}
	if len(branchByRoot) == 0 {
		b.WriteString("HEAD after run: none\n")
		return
	}
	roots := make([]string, 0, len(branchByRoot))
	for root := range branchByRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	b.WriteString("HEAD after run:\n")
	for _, root := range roots {
		fmt.Fprintf(b, "  - %s: %s\n", root, branchByRoot[root])
	}
}

// ============================================================================
// §6.2a Blockers / warnings blocks
// ============================================================================

func formatRebasePlanBlockers(b *strings.Builder, blockers []PlanBlocker) {
	if len(blockers) == 0 {
		b.WriteString("Plan blockers: none\n")
		return
	}
	b.WriteString("Plan blockers:\n")
	for _, blocker := range blockers {
		fmt.Fprintf(b, "  - %s [%s]: %s\n", string(blocker.Kind), rebasePlanEntryLabel(blocker.Entry), blocker.Detail)
	}
}

func formatRebasePlanWarnings(b *strings.Builder, warnings []PlanWarning) {
	if len(warnings) == 0 {
		b.WriteString("Plan warnings: none\n")
		return
	}
	b.WriteString("Plan warnings:\n")
	for _, warning := range warnings {
		fmt.Fprintf(b, "  - %s [%s]: %s\n", warning.Kind, rebasePlanEntryLabel(warning.Entry), warning.Detail)
	}
}

// rebasePlanEntryLabel is a blockers[]/warnings[] row's <entry>: the sanitized
// logical entry name, or the literal <document> token when entry is null.
func rebasePlanEntryLabel(entry *string) string {
	if entry == nil {
		return "<document>"
	}
	return sanitizeText(*entry)
}

// ============================================================================
// §6.3 The four-part tail
// ============================================================================

func formatRebasePlanTail(b *strings.Builder, plan RebasePlan) {
	fmt.Fprintf(b, "Plan runnable: %s\n", yesNo(plan.Runnable))
	fmt.Fprintf(b, "Guard would refuse: %s\n", yesNo(plan.Guard.WouldRefuse))
	refusalKind := "none"
	if plan.Refusal.Kind != nil {
		refusalKind = string(*plan.Refusal.Kind)
	}
	fmt.Fprintf(b, "Refusal kind: %s\n", refusalKind)
	formatRebasePlanConfigIssues(b, plan.ConfigIssues)
	formatRebasePlanGuardedExecutionBlockers(b, plan.Guard.ExecuteBlockedBy)
	fingerprint := "none"
	if plan.Approval.Fingerprint != nil {
		fingerprint = *plan.Approval.Fingerprint
	}
	fmt.Fprintf(b, "Approval fingerprint: %s\n", fingerprint)
}

// formatRebasePlanConfigIssues is tail Part 2.
func formatRebasePlanConfigIssues(b *strings.Builder, issues []PlanConfigIssue) {
	if len(issues) == 0 {
		b.WriteString("Configuration issues: none\n")
		return
	}
	b.WriteString("Configuration issues:\n")
	for _, issue := range issues {
		fmt.Fprintf(b, "  - %s %s [%s, %s]: %s\n",
			issue.IssueID, issue.Key, issue.Source, issue.RouteCommand, rebasePlanConfigIssueValue(issue))
	}
}

// rebasePlanConfigIssueValue is the config-issues row's <value-field>.
func rebasePlanConfigIssueValue(issue PlanConfigIssue) string {
	if !issue.RawValuePresent {
		return "<valueless>"
	}
	if issue.SanitizedValue == nil {
		return "<valueless>"
	}
	if *issue.SanitizedValue == "" {
		return `""`
	}
	return *issue.SanitizedValue
}

// formatRebasePlanGuardedExecutionBlockers is tail Part 3: token-only, no
// per-token prose.
func formatRebasePlanGuardedExecutionBlockers(b *strings.Builder, blockers []ControlledPathBlocker) {
	if len(blockers) == 0 {
		b.WriteString("Guarded execution blockers: none\n")
		return
	}
	b.WriteString("Guarded execution blockers:\n")
	for _, blocker := range blockers {
		fmt.Fprintf(b, "  - %s\n", string(blocker))
	}
}

// ============================================================================
// MarshalRebasePlan (§4, §13) — the --json document.
// ============================================================================

// PlanEncodeFault is §23.1 item 4's document-encoder half: a nil-in-
// production seam that forces MarshalRebasePlan/FormatRebasePlan to fail
// before producing any bytes, so a test can assert the §3.6 row 6 contract —
// an encoding failure returns the renderer's error and performs ZERO writes
// on the document stream. Neither encoder can be made to fail from data
// alone (every member is a string, integer, bool or pointer thereto), so
// this seam is the only way to reach that arm.
var PlanEncodeFault func() error

// MarshalRebasePlan renders plan as the canonical --json document: compact
// (no indentation), HTML-unescaped, with every "never-null array" (the
// eighteen of spec.md §4.7) normalized to [] instead of null, and
// exactly one trailing newline in the returned slice. It trusts plan's arrays
// are already in canonical §4.8 order (see the FormatRebasePlan doc comment
// above) and never re-sorts them; it only ever repairs nil-vs-empty, never
// row content or order.
func MarshalRebasePlan(plan RebasePlan) ([]byte, error) {
	if PlanEncodeFault != nil {
		if err := PlanEncodeFault(); err != nil {
			return nil, err
		}
	}
	normalized := normalizeRebasePlanArrays(plan)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalized); err != nil {
		return nil, err
	}
	// json.Encoder.Encode already appends exactly one trailing '\n'.
	return buf.Bytes(), nil
}

// normalizeRebasePlanArrays returns a copy of plan with every never-null array
// field backed by an allocated (possibly empty) slice, never a nil one. It
// never touches a "nullable array — exception" field (replay.commits,
// entries[].collateral_refs, summary.collateral_refs, fetch_effect.remotes,
// submodule_recursion.submodules, local_branch_destinations.branches/held/
// patterns, remotes[].refspecs, argv/argv_alternatives): those stay whatever
// the caller set, null included. fetch.repos[].context_candidates[] is
// omitted here because PlanFetchRepo.MarshalJSON already normalizes it on
// every encode, regardless of this pass.
func normalizeRebasePlanArrays(plan RebasePlan) RebasePlan {
	plan.RouteTriggers = ensureSlice(plan.RouteTriggers)
	plan.Repositories = ensureSlice(plan.Repositories)
	plan.Blockers = ensureSlice(plan.Blockers)
	plan.Warnings = ensureSlice(plan.Warnings)
	plan.EncodingIssues = ensureSlice(plan.EncodingIssues)
	plan.ConfigIssues = ensureSlice(plan.ConfigIssues)
	plan.Fetch.Repos = ensureSlice(plan.Fetch.Repos)
	plan.Push.Targets = ensureSlice(plan.Push.Targets)
	plan.Push.BlockedBy = ensureSlice(plan.Push.BlockedBy)
	plan.Restore.BlockedBy = ensureSlice(plan.Restore.BlockedBy)
	plan.Guard.Evaluation = ensureSlice(plan.Guard.Evaluation)
	plan.Guard.LimitConflicts = ensureSlice(plan.Guard.LimitConflicts)
	plan.Guard.ExecuteBlockedBy = ensureSlice(plan.Guard.ExecuteBlockedBy)
	plan.Approval.Covers.WaivedEvaluationIDs = ensureSlice(plan.Approval.Covers.WaivedEvaluationIDs)
	plan.Approval.Covers.WaivedKinds = ensureSlice(plan.Approval.Covers.WaivedKinds)

	entries := make([]PlanEntry, len(plan.Entries))
	for i, entry := range plan.Entries {
		entry.Notes = ensureSlice(entry.Notes)
		entries[i] = entry
	}
	plan.Entries = entries

	return plan
}

// ensureSlice returns s when non-nil, and a freshly allocated empty slice
// otherwise — the one helper every never-null array field normalizes
// through, so encoding/json's nil-slice-becomes-null default can never leak
// into the wire document.
func ensureSlice[T any](s []T) []T {
	if s != nil {
		return s
	}
	return []T{}
}
