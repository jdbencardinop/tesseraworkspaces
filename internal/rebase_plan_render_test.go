package internal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// Fixtures
// ============================================================================

// renderFixturePlan returns a minimally valid RebasePlan with every
// document-level field FormatRebasePlan/MarshalRebasePlan touch populated to
// a distinguishable, non-zero value. Entries/Blockers/Warnings/ConfigIssues/
// Guard.ExecuteBlockedBy start empty ([]) and are overridden per test.
func renderFixturePlan() RebasePlan {
	return RebasePlan{
		SchemaVersion:  1,
		Route:          RouteNewMode,
		RequestedRoute: nil,
		RouteTriggers:  []string{},
		Invocation:     "plan-only",
		Workspace:      PlanWorkspace{Mode: string(ModeCheckout), RepoRoot: "/display/workspace-root"},
		Feature:        "rebase-plan-guard",
		Policy: PlanPolicy{
			Fetch:       "auto",
			Propagation: "full",
			ScopeKind:   "all",
		},
		Freshness:      "current",
		Repositories:   []PlanRepository{},
		Runnable:       true,
		Blockers:       []PlanBlocker{},
		Warnings:       []PlanWarning{},
		EncodingIssues: []PlanEncodingIssue{},
		ConfigIssues:   []PlanConfigIssue{},
		Entries:        []PlanEntry{},
		Guard:          PlanGuardBlock{WouldRefuse: false, ExecuteBlockedBy: []ControlledPathBlocker{}},
		Refusal:        PlanRefusal{},
		Approval:       PlanApproval{},
	}
}

// renderFixtureEntryFull returns a fully populated, non-skipped PlanEntry:
// every one of the eleven rendered fields has a distinguishable "normal"
// value, none of them a fixed absent/unknown spelling.
func renderFixtureEntryFull() PlanEntry {
	return PlanEntry{
		Name:      "pr2",
		GitBranch: "feat/pr2",
		Base: PlanEntryBase{
			Name: strPtr("master"),
			Kind: "stack-entry",
			Ref:  strPtr("master"),
		},
		Destination: PlanEntryDestination{
			SHA: strPtr("dddddddddddd1111222233334444555566667777"),
		},
		Cutoff: PlanEntryCutoff{
			RecordedSHA: strPtr("bbbbbbbbbbbb1111222233334444555566667777"),
			State:       strPtr("present"),
		},
		Replay: PlanEntryReplay{
			UpstreamSHA:    strPtr("bbbbbbbbbbbb1111222233334444555566667777"),
			Range:          strPtr("bbbbbbbbbbbb1111222233334444555566667777..feat/pr2"),
			CandidateCount: intPtr(1),
			FirstCandidate: &PlanReplayCandidate{
				SHA:     "cccccccccccc1111222233334444555566667777",
				Subject: "C",
			},
		},
		Strategy:         "onto",
		ExecutionContext: PlanContext{RepoRoot: strPtr("/display/execution-root")},
		Mutation:         PlanEntryMutation{},
	}
}

func mustFormat(t *testing.T, plan RebasePlan) string {
	t.Helper()
	out, err := FormatRebasePlan(plan)
	if err != nil {
		t.Fatalf("FormatRebasePlan: %v", err)
	}
	return string(out)
}

// entryRowLines extracts the two physical lines belonging to the Nth (0-based)
// entries[] row from a full FormatRebasePlan document, failing the test if
// the "Entries:" header or that row's lines are not where expected.
func entryRowLines(t *testing.T, doc string, n int) (line1, line2 string) {
	t.Helper()
	lines := strings.Split(doc, "\n")
	headerIdx := -1
	for i, l := range lines {
		if l == "Entries:" {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		t.Fatalf("no \"Entries:\" header found in:\n%s", doc)
	}
	i1 := headerIdx + 1 + 2*n
	i2 := i1 + 1
	if i2 >= len(lines) {
		t.Fatalf("row %d not present after \"Entries:\" header in:\n%s", n, doc)
	}
	return lines[i1], lines[i2]
}

// ============================================================================
// Document-level framing (§6, three lines)
// ============================================================================

func TestRebasePlanRender_FramingLines(t *testing.T) {
	plan := renderFixturePlan()
	plan.Route = RouteLegacy
	plan.Workspace.Mode = string(ModeExternal)
	plan.Invocation = "plan-only"
	plan.Policy = PlanPolicy{Fetch: "no-fetch", Propagation: "local-only", ScopeKind: "all"}
	plan.Freshness = "fetched"

	doc := mustFormat(t, plan)
	lines := strings.Split(doc, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 framing lines, got %d:\n%s", len(lines), doc)
	}
	want := []string{
		"Plan: route=legacy mode=external invocation=plan-only",
		"Policy: fetch=no-fetch propagation=local-only scope=all",
		"Freshness: fetched",
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("framing line %d = %q, want %q", i, lines[i], w)
		}
	}
}

func TestRebasePlanRender_ScopeLabelNeverPrintsFromToken(t *testing.T) {
	cases := []struct {
		name string
		kind string
		sel  *string
		want string
	}{
		{"all", "all", nil, "scope=all"},
		{"one-with-selector", "one", strPtr("pr2"), "scope=only:pr2"},
		{"subtree-with-selector", "subtree", strPtr("pr2"), "scope=subtree:pr2"},
		{"unrecognized-kind-falls-back-to-all", "from", strPtr("pr2"), "scope=all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := renderFixturePlan()
			plan.Policy.ScopeKind = tc.kind
			plan.Policy.Selector = tc.sel
			doc := mustFormat(t, plan)
			lines := strings.Split(doc, "\n")
			if !strings.Contains(lines[1], tc.want) {
				t.Errorf("Policy line = %q, want it to contain %q", lines[1], tc.want)
			}
			if strings.Contains(lines[1], "scope=from") || strings.Contains(lines[1], "from:") {
				t.Errorf("Policy line must never print the bare %q token: %q", "from", lines[1])
			}
		})
	}
}

// ============================================================================
// §6.1 Entries block
// ============================================================================

func TestRebasePlanRender_EntriesBlockEmpty(t *testing.T) {
	plan := renderFixturePlan()
	doc := mustFormat(t, plan)
	if !strings.Contains(doc, "Entries: none\n") {
		t.Fatalf("expected exactly \"Entries: none\" for an empty entry set:\n%s", doc)
	}
	if strings.Contains(doc, "Entries:\n") {
		t.Fatalf("an empty entry set must not also print the \"Entries:\" header:\n%s", doc)
	}
}

func TestRebasePlanRender_EntryRowElevenFieldsAndLiteralArrow(t *testing.T) {
	plan := renderFixturePlan()
	plan.Entries = []PlanEntry{renderFixtureEntryFull()}
	doc := mustFormat(t, plan)

	line1, line2 := entryRowLines(t, doc, 0)
	wantLine1 := "  - pr2 [feat/pr2] base master \u2192 master@dddddddddddd cutoff bbbbbbbbbbbb upstream bbbbbbbbbbbb strategy onto"
	wantLine2 := `    candidates 1 range bbbbbbbbbbbb..feat/pr2 first cccccccccccc "C"`
	if line1 != wantLine1 {
		t.Errorf("entry row line 1 =\n%q\nwant\n%q", line1, wantLine1)
	}
	if line2 != wantLine2 {
		t.Errorf("entry row line 2 =\n%q\nwant\n%q", line2, wantLine2)
	}
	if !strings.Contains(line1, "\u2192") {
		t.Fatalf("line 1 must contain the literal U+2192 arrow: %q", line1)
	}
	if strings.Contains(line1, "->") || strings.Contains(line1, "=>") {
		t.Errorf("line 1 must use the literal arrow rune, not an ASCII substitute: %q", line1)
	}

	// Exactly eleven printed field values across the two lines: name,
	// git_branch, configured, resolved, destination, recorded, effective,
	// strategy, count, range, first.
	fields := []string{"pr2", "feat/pr2", "master", "master", "dddddddddddd", "bbbbbbbbbbbb", "bbbbbbbbbbbb", "onto", "1", "bbbbbbbbbbbb..feat/pr2", `cccccccccccc "C"`}
	if len(fields) != 11 {
		t.Fatalf("test fixture assumption broken: want 11 field values, have %d", len(fields))
	}
	for _, f := range fields {
		if !strings.Contains(line1+"\n"+line2, f) {
			t.Errorf("expected field value %q somewhere in the row, got:\n%s\n%s", f, line1, line2)
		}
	}
}

func TestRebasePlanRender_HeaderWordIsCandidatesNeverAppliesOrWillBeApplied(t *testing.T) {
	plan := renderFixturePlan()
	plan.Entries = []PlanEntry{renderFixtureEntryFull()}
	doc := mustFormat(t, plan)
	if !strings.Contains(doc, "candidates 1") {
		t.Fatalf("expected the header word \"candidates\":\n%s", doc)
	}
	if strings.Contains(doc, "applies") || strings.Contains(doc, "will be applied") {
		t.Errorf("rendering must never claim commits \"apply\"/\"will be applied\":\n%s", doc)
	}
}

func TestRebasePlanRender_EntryRowFixedAbsentSpellings(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(e *PlanEntry)
		wantSeg string
	}{
		{
			name:    "base.kind none -> <none> configured",
			mutate:  func(e *PlanEntry) { e.Base.Name = nil },
			wantSeg: "base <none>",
		},
		{
			name:    "base.ref null -> <unresolved> resolved",
			mutate:  func(e *PlanEntry) { e.Base.Ref = nil },
			wantSeg: "\u2192 <unresolved>@",
		},
		{
			name: "destination deferred -> post-rebase(<depends_on>)",
			mutate: func(e *PlanEntry) {
				e.Destination = PlanEntryDestination{Deferred: true, DependsOn: strPtr("pr1")}
			},
			wantSeg: "post-rebase(pr1)",
		},
		{
			name: "destination deferred with no depends_on -> post-rebase(<none>)",
			mutate: func(e *PlanEntry) {
				e.Destination = PlanEntryDestination{Deferred: true, DependsOn: nil}
			},
			wantSeg: "post-rebase(<none>)",
		},
		{
			name:    "destination unresolved when sha null and not deferred",
			mutate:  func(e *PlanEntry) { e.Destination = PlanEntryDestination{} },
			wantSeg: "@<unresolved> cutoff",
		},
		{
			name:    "cutoff recorded_sha absent -> <none>",
			mutate:  func(e *PlanEntry) { e.Cutoff = PlanEntryCutoff{} },
			wantSeg: "cutoff <none>",
		},
		{
			name: "cutoff unresolvable -> (unresolvable) suffix",
			mutate: func(e *PlanEntry) {
				e.Cutoff = PlanEntryCutoff{RecordedSHA: strPtr("bbbbbbbbbbbb1111222233334444555566667777"), State: strPtr("unresolvable")}
			},
			wantSeg: "bbbbbbbbbbbb (unresolvable)",
		},
		{
			name: "cutoff state null -> (unverified) suffix",
			mutate: func(e *PlanEntry) {
				e.Cutoff = PlanEntryCutoff{RecordedSHA: strPtr("bbbbbbbbbbbb1111222233334444555566667777"), State: nil}
			},
			wantSeg: "bbbbbbbbbbbb (unverified)",
		},
		{
			name:    "effective unknown when upstream_sha null",
			mutate:  func(e *PlanEntry) { e.Replay.UpstreamSHA = nil },
			wantSeg: "upstream <unknown>",
		},
		{
			name:    "count unknown when candidate_count null",
			mutate:  func(e *PlanEntry) { e.Replay.CandidateCount = nil },
			wantSeg: "candidates <unknown>",
		},
		{
			name:    "range none when replay.range null",
			mutate:  func(e *PlanEntry) { e.Replay.Range = nil },
			wantSeg: "range <none>",
		},
		{
			name: "first none for known-empty range",
			mutate: func(e *PlanEntry) {
				e.Replay.CandidateCount = intPtr(0)
				e.Replay.FirstCandidate = nil
			},
			wantSeg: "first <none>",
		},
		{
			name: "first unknown for unknown range",
			mutate: func(e *PlanEntry) {
				e.Replay.CandidateCount = nil
				e.Replay.FirstCandidate = nil
			},
			wantSeg: "first <unknown>",
		},
		{
			name: "first unknown when candidate_count > 0 but first_candidate missing",
			mutate: func(e *PlanEntry) {
				e.Replay.CandidateCount = intPtr(2)
				e.Replay.FirstCandidate = nil
			},
			wantSeg: "first <unknown>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := renderFixtureEntryFull()
			tc.mutate(&entry)
			plan := renderFixturePlan()
			plan.Entries = []PlanEntry{entry}
			doc := mustFormat(t, plan)
			if !strings.Contains(doc, tc.wantSeg) {
				t.Errorf("expected %q in rendered document:\n%s", tc.wantSeg, doc)
			}
		})
	}
}

func TestRebasePlanRender_SkippedRowSpellings(t *testing.T) {
	entry := renderFixtureEntryFull()
	entry.Strategy = "skipped-updated-ref"
	// Real skipped-row producers leave these fields as the shipped code
	// does: replay facts absent/zeroed, range unset.
	entry.Replay.UpstreamSHA = nil
	entry.Replay.Range = nil
	entry.Replay.CandidateCount = nil
	entry.Replay.FirstCandidate = nil

	plan := renderFixturePlan()
	plan.Entries = []PlanEntry{entry}
	doc := mustFormat(t, plan)
	line1, line2 := entryRowLines(t, doc, 0)

	if !strings.Contains(line1, "@<not-executed> cutoff") {
		t.Errorf("skipped row destination must be <not-executed>: %q", line1)
	}
	if !strings.Contains(line1, "upstream <not-executed> strategy skipped-updated-ref") {
		t.Errorf("skipped row effective must be <not-executed> and strategy must survive verbatim: %q", line1)
	}
	if !strings.Contains(line2, "candidates 0 range <none>") {
		t.Errorf("skipped row must print \"candidates 0 range <none>\": %q", line2)
	}
}

func TestRebasePlanRender_MultipleEntriesPreserveCanonicalOrder(t *testing.T) {
	first := renderFixtureEntryFull()
	first.Name, first.GitBranch = "pr1", "feat/pr1"
	second := renderFixtureEntryFull()
	second.Name, second.GitBranch = "pr2", "feat/pr2"

	plan := renderFixturePlan()
	plan.Entries = []PlanEntry{first, second}
	doc := mustFormat(t, plan)
	idxPR1 := strings.Index(doc, "- pr1 ")
	idxPR2 := strings.Index(doc, "- pr2 ")
	if idxPR1 < 0 || idxPR2 < 0 {
		t.Fatalf("both rows must be present:\n%s", doc)
	}
	if idxPR1 > idxPR2 {
		t.Errorf("rows must render in the given (canonical) plan.Entries order, pr1 before pr2:\n%s", doc)
	}
}

// ============================================================================
// §6.2 Head-after-run block
// ============================================================================

func TestRebasePlanRender_HeadAfterRunNone(t *testing.T) {
	plan := renderFixturePlan()
	entry := renderFixtureEntryFull()
	entry.Mutation = PlanEntryMutation{WillSwitchHead: false}
	plan.Entries = []PlanEntry{entry}
	doc := mustFormat(t, plan)
	if !strings.Contains(doc, "HEAD after run: none\n") {
		t.Fatalf("expected \"HEAD after run: none\" when no row switches HEAD:\n%s", doc)
	}
}

func TestRebasePlanRender_HeadAfterRunCheckoutRestoredRowContributesNoLine(t *testing.T) {
	plan := renderFixturePlan()
	entry := renderFixtureEntryFull()
	entry.Mutation = PlanEntryMutation{WillSwitchHead: true, WillLeaveHeadOn: strPtr("feat/pr2"), HeadRestoredByRun: true}
	plan.Entries = []PlanEntry{entry}
	doc := mustFormat(t, plan)
	if !strings.Contains(doc, "HEAD after run: none\n") {
		t.Fatalf("a checkout row with head_restored_by_run:true must contribute no line:\n%s", doc)
	}
}

func TestRebasePlanRender_HeadAfterRunSortedLastSwitchWinsPerContext(t *testing.T) {
	// Two execution contexts (repo roots), out-of-order so the sort is
	// exercised; each context has two switching rows so "last wins" is
	// exercised too.
	rowFor := func(root, leaveOn string) PlanEntry {
		e := renderFixtureEntryFull()
		e.ExecutionContext = PlanContext{RepoRoot: strPtr(root)}
		e.Mutation = PlanEntryMutation{WillSwitchHead: true, WillLeaveHeadOn: strPtr(leaveOn), HeadRestoredByRun: false}
		return e
	}
	plan := renderFixturePlan()
	plan.Entries = []PlanEntry{
		rowFor("/repo/zeta", "first-branch"),
		rowFor("/repo/alpha", "will-be-overwritten"),
		rowFor("/repo/zeta", "second-branch"), // last switching row for /repo/zeta wins
		rowFor("/repo/alpha", "alpha-final"),  // last switching row for /repo/alpha wins
	}
	doc := mustFormat(t, plan)

	idxHeader := strings.Index(doc, "HEAD after run:\n")
	if idxHeader < 0 {
		t.Fatalf("expected a populated \"HEAD after run:\" header:\n%s", doc)
	}
	wantAlpha := "  - /repo/alpha: alpha-final\n"
	wantZeta := "  - /repo/zeta: second-branch\n"
	idxAlpha := strings.Index(doc, wantAlpha)
	idxZeta := strings.Index(doc, wantZeta)
	if idxAlpha < 0 {
		t.Errorf("expected %q (last-switch wins) in:\n%s", wantAlpha, doc)
	}
	if idxZeta < 0 {
		t.Errorf("expected %q (last-switch wins) in:\n%s", wantZeta, doc)
	}
	if idxAlpha >= 0 && idxZeta >= 0 && idxAlpha > idxZeta {
		t.Errorf("roots must be sorted bytewise: /repo/alpha before /repo/zeta, got alpha@%d zeta@%d", idxAlpha, idxZeta)
	}
	if strings.Contains(doc, "will-be-overwritten") || strings.Contains(doc, "first-branch") {
		t.Errorf("only the last switching row per context may appear:\n%s", doc)
	}
}

// ============================================================================
// §6.2a Blockers / warnings blocks
// ============================================================================

func TestRebasePlanRender_BlockersWarningsBlocksNone(t *testing.T) {
	plan := renderFixturePlan()
	doc := mustFormat(t, plan)
	if !strings.Contains(doc, "Plan blockers: none\n") {
		t.Errorf("expected \"Plan blockers: none\":\n%s", doc)
	}
	if !strings.Contains(doc, "Plan warnings: none\n") {
		t.Errorf("expected \"Plan warnings: none\":\n%s", doc)
	}
}

func TestRebasePlanRender_BlockersWarningsBlocksTotalAndDocumentLabel(t *testing.T) {
	plan := renderFixturePlan()
	plan.Blockers = []PlanBlocker{
		{Kind: RefusalLimitPerEntry, Entry: strPtr("pr2"), Detail: "pr2 replays 40 candidates (limit 10)"},
		{Kind: RefusalPlanUnavailable, Entry: nil, Detail: "stack.yaml unreadable"},
	}
	plan.Warnings = []PlanWarning{
		{Kind: "untracked-present", Entry: strPtr("pr2"), Detail: "untracked files present"},
	}
	doc := mustFormat(t, plan)

	wantBlockerDoc := "  - plan-unavailable [<document>]: stack.yaml unreadable\n"
	wantBlockerEntry := "  - limit-per-entry [pr2]: pr2 replays 40 candidates (limit 10)\n"
	wantWarning := "  - untracked-present [pr2]: untracked files present\n"
	if !strings.Contains(doc, wantBlockerDoc) {
		t.Errorf("expected the document-level blocker line with <document> label:\n%s\ngot:\n%s", wantBlockerDoc, doc)
	}
	if !strings.Contains(doc, wantBlockerEntry) {
		t.Errorf("expected the entry-scoped blocker line:\n%s\ngot:\n%s", wantBlockerEntry, doc)
	}
	if !strings.Contains(doc, wantWarning) {
		t.Errorf("expected the warning line:\n%s\ngot:\n%s", wantWarning, doc)
	}
	if strings.Contains(doc, "Plan blockers: none") || strings.Contains(doc, "Plan warnings: none") {
		t.Errorf("a non-empty blockers/warnings set must not also print the \"none\" spelling:\n%s", doc)
	}
}

func TestRebasePlanRender_WarningsNeverHiddenOnRefusalOrNotRunnable(t *testing.T) {
	plan := renderFixturePlan()
	plan.Runnable = false
	plan.Refusal = PlanRefusal{Kind: refusalKindPtr(RefusalStateRefused), Detail: strPtr("dirty tree")}
	plan.Warnings = []PlanWarning{{Kind: "checkout-dirty-present", Entry: nil, Detail: "dirty tree present"}}
	doc := mustFormat(t, plan)
	if !strings.Contains(doc, "  - checkout-dirty-present [<document>]: dirty tree present\n") {
		t.Errorf("warnings must render even when runnable:false and a refusal is present:\n%s", doc)
	}
}

func refusalKindPtr(k RefusalKind) *RefusalKind { return &k }

// ============================================================================
// §6.3 The four-part tail
// ============================================================================

func TestRebasePlanRender_TailPart1StatusLines(t *testing.T) {
	cases := []struct {
		name        string
		runnable    bool
		wouldRefuse bool
		refusal     *RefusalKind
		wantLines   []string
	}{
		{
			name: "runnable, no refusal", runnable: true, wouldRefuse: false, refusal: nil,
			wantLines: []string{"Plan runnable: yes", "Guard would refuse: no", "Refusal kind: none"},
		},
		{
			name: "not runnable, guard would refuse, refusal kind present", runnable: false, wouldRefuse: true,
			refusal:   refusalKindPtr(RefusalLimitTotal),
			wantLines: []string{"Plan runnable: no", "Guard would refuse: yes", "Refusal kind: limit-total"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := renderFixturePlan()
			plan.Runnable = tc.runnable
			plan.Guard.WouldRefuse = tc.wouldRefuse
			if tc.refusal != nil {
				plan.Refusal = PlanRefusal{Kind: tc.refusal, Detail: strPtr("detail")}
			}
			doc := mustFormat(t, plan)
			for _, w := range tc.wantLines {
				if !strings.Contains(doc, w+"\n") {
					t.Errorf("expected tail line %q in:\n%s", w, doc)
				}
			}
		})
	}
}

func TestRebasePlanRender_TailPart2ConfigurationIssues(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		plan := renderFixturePlan()
		doc := mustFormat(t, plan)
		if !strings.Contains(doc, "Configuration issues: none\n") {
			t.Errorf("expected \"Configuration issues: none\":\n%s", doc)
		}
	})

	t.Run("populated rows, in issue_id order, byte-exact format", func(t *testing.T) {
		plan := renderFixturePlan()
		plan.ConfigIssues = []PlanConfigIssue{
			{
				IssueID: "1111111111111111111111111111111111111111111111111111111111111111",
				Key:     "rebase.rebaseMerges", Source: "local", RouteCommand: "rebase",
				RawValuePresent: true, SanitizedValue: strPtr("bogus-value"),
			},
			{
				IssueID: "2222222222222222222222222222222222222222222222222222222222222222",
				Key:     "rebase.backend", Source: "global", RouteCommand: "rebase",
				RawValuePresent: false,
			},
			{
				IssueID: "3333333333333333333333333333333333333333333333333333333333333333",
				Key:     "rebase.autoStash", Source: "local", RouteCommand: "rebase",
				RawValuePresent: true, SanitizedValue: strPtr(""),
			},
		}
		doc := mustFormat(t, plan)
		want1 := "  - 1111111111111111111111111111111111111111111111111111111111111111 rebase.rebaseMerges [local, rebase]: bogus-value\n"
		want2 := "  - 2222222222222222222222222222222222222222222222222222222222222222 rebase.backend [global, rebase]: <valueless>\n"
		want3 := `  - 3333333333333333333333333333333333333333333333333333333333333333 rebase.autoStash [local, rebase]: ""` + "\n"
		for _, w := range []string{want1, want2, want3} {
			if !strings.Contains(doc, w) {
				t.Errorf("expected config-issue line:\n%q\nin:\n%s", w, doc)
			}
		}
		i1 := strings.Index(doc, want1)
		i2 := strings.Index(doc, want2)
		i3 := strings.Index(doc, want3)
		if i1 >= i2 || i2 >= i3 {
			t.Errorf("config_issues rows must render in issue_id order: got offsets %d,%d,%d", i1, i2, i3)
		}
	})
}

func TestRebasePlanRender_TailPart3GuardedExecutionBlockersTokenOnly(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		plan := renderFixturePlan()
		doc := mustFormat(t, plan)
		if !strings.Contains(doc, "Guarded execution blockers: none\n") {
			t.Errorf("expected \"Guarded execution blockers: none\":\n%s", doc)
		}
	})

	t.Run("populated, token-only, no detail suffix", func(t *testing.T) {
		plan := renderFixturePlan()
		plan.Guard.ExecuteBlockedBy = []ControlledPathBlocker{ControlledFetchContextIndeterminate, ControlledLiveOwnerConcurrency}
		doc := mustFormat(t, plan)
		want1 := "  - fetch-context-indeterminate\n"
		want2 := "  - live-owner-concurrency\n"
		if !strings.Contains(doc, want1) {
			t.Errorf("expected %q in:\n%s", want1, doc)
		}
		if !strings.Contains(doc, want2) {
			t.Errorf("expected %q in:\n%s", want2, doc)
		}
		if strings.Contains(doc, "fetch-context-indeterminate:") || strings.Contains(doc, "fetch-context-indeterminate ") {
			t.Errorf("guarded execution blocker lines must be token-only, no \": <detail>\" suffix:\n%s", doc)
		}
	})
}

func TestRebasePlanRender_TailPart4ApprovalFingerprintAlwaysOnALineContainingIt(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		plan := renderFixturePlan()
		doc := mustFormat(t, plan)
		if !strings.Contains(doc, "Approval fingerprint: none\n") {
			t.Errorf("expected \"Approval fingerprint: none\":\n%s", doc)
		}
	})

	t.Run("present, on a line containing (never bare) the token", func(t *testing.T) {
		fp := strings.Repeat("ab", 32)
		plan := renderFixturePlan()
		plan.Approval.Fingerprint = strPtr(fp)
		doc := mustFormat(t, plan)
		want := "Approval fingerprint: " + fp + "\n"
		if !strings.Contains(doc, want) {
			t.Errorf("expected %q in:\n%s", want, doc)
		}
		lines := strings.Split(doc, "\n")
		for _, l := range lines {
			if l == fp {
				t.Errorf("the bare fingerprint must never appear on its own line: %q", l)
			}
		}
	})
}

func TestRebasePlanRender_TailIsAlwaysLastInFixedOrder(t *testing.T) {
	plan := renderFixturePlan()
	plan.Entries = []PlanEntry{renderFixtureEntryFull()}
	plan.Blockers = []PlanBlocker{{Kind: RefusalLimitTotal, Entry: nil, Detail: "detail"}}
	plan.Warnings = []PlanWarning{{Kind: "untracked-present", Entry: nil, Detail: "detail"}}
	doc := mustFormat(t, plan)

	order := []string{"Plan:", "Policy:", "Freshness:", "Entries:", "HEAD after run:", "Plan blockers:", "Plan warnings:", "Plan runnable:"}
	lastIdx := -1
	for _, marker := range order {
		idx := strings.Index(doc, marker)
		if idx < 0 {
			t.Fatalf("expected marker %q somewhere in the document:\n%s", marker, doc)
		}
		if idx <= lastIdx {
			t.Errorf("marker %q out of the fixed block order (idx=%d, previous=%d):\n%s", marker, idx, lastIdx, doc)
		}
		lastIdx = idx
	}
	// The tail's own four labelled lines must themselves be in fixed order,
	// after everything else.
	tailOrder := []string{"Plan runnable:", "Guard would refuse:", "Refusal kind:", "Configuration issues:", "Guarded execution blockers:", "Approval fingerprint:"}
	lastIdx = -1
	for _, marker := range tailOrder {
		idx := strings.Index(doc, marker)
		if idx < 0 {
			t.Fatalf("expected tail marker %q in the document:\n%s", marker, doc)
		}
		if idx <= lastIdx {
			t.Errorf("tail marker %q out of fixed order:\n%s", marker, doc)
		}
		lastIdx = idx
	}
	if !strings.HasSuffix(strings.TrimRight(doc, "\n"), "Approval fingerprint: none") {
		t.Errorf("the approval-fingerprint line must be the document's last line:\n%s", doc)
	}
}

// ============================================================================
// 12-char SHA formatting, zero `rev-parse --short` process
// ============================================================================

func TestRebasePlanRender_ShortSHAIsTwelveLowercaseHexNoGitProcessSpawned(t *testing.T) {
	ssNeutralizeGitConfig(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	gitInTest(t, dir, "init", "--initial-branch=main")
	gitInTest(t, dir, "commit", "--allow-empty", "-m", "init")
	fullSHA := gitInTest(t, dir, "rev-parse", "HEAD")
	if len(fullSHA) != 40 {
		t.Fatalf("test fixture assumption broken: want a 40-char real commit SHA, got %q", fullSHA)
	}
	wantShort := strings.ToLower(fullSHA)[:12]

	entry := renderFixtureEntryFull()
	entry.Destination.SHA = strPtr(strings.ToUpper(fullSHA)) // force-uppercase to prove lowercasing
	entry.Cutoff.RecordedSHA = strPtr(fullSHA)
	entry.Cutoff.State = strPtr("present")
	entry.Replay.UpstreamSHA = strPtr(fullSHA)
	entry.Replay.FirstCandidate.SHA = fullSHA

	plan := renderFixturePlan()
	plan.Entries = []PlanEntry{entry}

	// Make `git` unreachable for the duration of the render call: if
	// FormatRebasePlan ever shelled out to `git rev-parse --short`, this
	// would surface as an error or panic rather than a silently wrong
	// value, because there is no git binary anywhere on PATH.
	emptyBin := filepath.Join(t.TempDir(), "empty-bin")
	if err := os.MkdirAll(emptyBin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", emptyBin)

	doc := mustFormat(t, plan)
	if !strings.Contains(doc, wantShort) {
		t.Errorf("expected the 12-char lowercase short SHA %q derived from the real commit %q in:\n%s", wantShort, fullSHA, doc)
	}
	if strings.Contains(doc, strings.ToUpper(fullSHA)[:12]) {
		t.Errorf("short SHA must be forced to lowercase, found an uppercase rendering in:\n%s", doc)
	}
	if strings.Contains(doc, fullSHA) {
		t.Errorf("the full 40-char SHA must never appear verbatim in the human document, only its first 12 chars:\n%s", doc)
	}
}

func TestRebasePlanRender_ShortSHATruncatesAtTwelveAndPassesThroughShortInput(t *testing.T) {
	if got := shortSHA("ABCDEF012345678999999999"); got != "abcdef012345" {
		t.Errorf("shortSHA(24-char input) = %q, want the first 12 lowercased chars %q", got, "abcdef012345")
	}
	if got := shortSHA("abc123"); got != "abc123" {
		t.Errorf("shortSHA(6-char input) = %q, want it returned as-is (lowercased): %q", got, "abc123")
	}
	if got := shortSHA(""); got != "" {
		t.Errorf("shortSHA(\"\") = %q, want \"\"", got)
	}
}

// ============================================================================
// MarshalRebasePlan (§4, §13) — exactly one JSON value, one trailing newline
// ============================================================================

func TestRebasePlanRender_MarshalRebasePlanExactlyOneJSONValuePlusOneNewline(t *testing.T) {
	plan := renderFixturePlan()
	plan.Entries = []PlanEntry{renderFixtureEntryFull()}
	out, err := MarshalRebasePlan(plan)
	if err != nil {
		t.Fatalf("MarshalRebasePlan: %v", err)
	}

	if n := bytes.Count(out, []byte("\n")); n != 1 {
		t.Fatalf("output contains %d newline byte(s), want exactly 1", n)
	}
	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Fatalf("output must end with the single trailing newline")
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	var v1 any
	if err := dec.Decode(&v1); err != nil {
		t.Fatalf("decoding the first JSON value: %v", err)
	}
	if dec.More() {
		t.Fatalf("a second JSON value follows the first — output must contain exactly one")
	}
	// Confirm EOF (nothing but the consumed whitespace/newline remains).
	var v2 any
	if err := dec.Decode(&v2); err == nil {
		t.Fatalf("expected EOF decoding past the single JSON value, got a second value: %#v", v2)
	}
}

func TestRebasePlanRender_MarshalRebasePlanIsCompactAndHTMLUnescaped(t *testing.T) {
	plan := renderFixturePlan()
	plan.Blockers = []PlanBlocker{{Kind: RefusalLimitTotal, Entry: nil, Detail: "a<b && c>d"}}
	out, err := MarshalRebasePlan(plan)
	if err != nil {
		t.Fatalf("MarshalRebasePlan: %v", err)
	}
	body := string(out)
	if strings.Contains(body, "\n ") || strings.Contains(body, "  ") {
		t.Errorf("output must be compact (no indentation), got:\n%s", body)
	}
	if !strings.Contains(body, "a<b && c>d") {
		t.Errorf("HTML-unescaped characters (<, >, &) must survive verbatim, got:\n%s", body)
	}
	if strings.Contains(body, `\u003c`) || strings.Contains(body, `\u0026`) {
		t.Errorf("output must not HTML-escape <, >, & as \\u003c/\\u0026, got:\n%s", body)
	}
}

func TestRebasePlanRender_MarshalRebasePlanRoundTripsAndNormalizesNeverNullArrays(t *testing.T) {
	// A minimal light cross-check that the render/marshal boundary honours
	// the never-null-array contract (exhaustively covered per-key in
	// rebase_plan_test.go); here we only confirm the specific field
	// normalizeRebasePlanArrays is documented to touch on Entries[].Notes,
	// using a genuinely nil slice as input.
	plan := renderFixturePlan()
	entry := renderFixtureEntryFull()
	entry.Notes = nil
	plan.Entries = []PlanEntry{entry}

	out, err := MarshalRebasePlan(plan)
	if err != nil {
		t.Fatalf("MarshalRebasePlan: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	entries, ok := decoded["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("decoded entries = %#v, want a one-element array", decoded["entries"])
	}
	row, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("decoded entries[0] is not an object: %#v", entries[0])
	}
	notes, present := row["notes"]
	if !present {
		t.Fatalf("entries[0].notes key must be present")
	}
	notesArr, ok := notes.([]any)
	if !ok {
		t.Fatalf("entries[0].notes = %#v (%T), want a JSON array (never null)", notes, notes)
	}
	if len(notesArr) != 0 {
		t.Fatalf("entries[0].notes = %#v, want an empty array for a nil Go slice", notesArr)
	}
}
