package cli

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// docsRepoRoot is the relative path from this package's test working
// directory (go test always runs with cwd set to the package's own source
// directory, verified directly against this repository) up to the
// repository root, where README.md, CHANGELOG.md, docs/ and assets/ live.
const docsRepoRoot = "../.."

// readRepoDoc reads a repository-root-relative documentation file. It fails
// the test immediately if the file cannot be read, since every caller in
// this file asserts real, present content — never an absent-is-fine case.
func readRepoDoc(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(docsRepoRoot, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

// normalizeProse strips the Markdown emphasis markers this feature's docs
// use (bold, backtick code spans) and collapses all whitespace — including
// the hard line wraps CHANGELOG.md and the two planning-prose docs use —
// into single spaces, then lowercases. This lets a quoted sentence that a
// human-authored file wraps across two physical lines be matched as one
// contiguous phrase, per spec.md §22.33-i-a's "matched semantically
// (case-insensitive, whitespace- and emphasis-normalised)" rule.
func normalizeProse(s string) string {
	s = strings.NewReplacer("**", "", "`", "", "_", "").Replace(s)
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// ---------------------------------------------------------------------------
// TestSyncPlanDocs_HelpSnapshot
// ---------------------------------------------------------------------------

// TestSyncPlanDocs_HelpSnapshot asserts `tws sync --help`, captured through
// production Execute() exactly as the task's marker-assertion rule requires
// (syncExecute silences and reprints errors itself and is not valid for this
// kind of snapshot; runSyncExecute drives the real cli.Execute() in
// root.go), matches internal/cli/testdata/rebase_plan/sync_help.txt byte for
// byte. That fixture was captured from production output and carries the
// full Long plus every flag help line, so this one comparison covers
// spec.md §22.33a-(b)'s "asserted to contain the five flag help lines and
// the Long byte for byte" clause in its strongest form: exact identity of
// the whole captured stream, not a substring check.
func TestSyncPlanDocs_HelpSnapshot(t *testing.T) {
	stdout, stderr, exit := runSyncExecute(t, "--help")
	if exit != 0 {
		t.Fatalf("tws sync --help must exit 0, got %d (stderr=%q)", exit, stderr)
	}
	if stderr != "" {
		t.Fatalf("tws sync --help must write nothing to stderr, got %q", stderr)
	}
	want, err := os.ReadFile("testdata/rebase_plan/sync_help.txt")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if stdout != string(want) {
		t.Fatalf("tws sync --help drifted from the frozen snapshot at testdata/rebase_plan/sync_help.txt:\n--- got ---\n%s\n--- want ---\n%s", stdout, string(want))
	}
}

// ---------------------------------------------------------------------------
// TestSyncPlanDocs_FlagWorkflowSurfacesCarryAllFiveLiterals
// ---------------------------------------------------------------------------

// TestSyncPlanDocs_FlagWorkflowSurfacesCarryAllFiveLiterals implements
// spec.md §22.33-i: README.md, docs/cheatsheet.md, CHANGELOG.md and the
// three assets/skills/** documents each carry all five literals this
// feature owns. embed.go, the fourth file under assets/skills/, is a
// //go:embed directive with no prose and is deliberately excluded — the
// spec's own six-file list names only the three Markdown skill documents.
func TestSyncPlanDocs_FlagWorkflowSurfacesCarryAllFiveLiterals(t *testing.T) {
	literals := []string{"--plan", "--max-replay-per-entry", "--max-replay-total", "--approve-plan", "plan-guard:"}
	surfaces := []string{
		"README.md",
		"docs/cheatsheet.md",
		"CHANGELOG.md",
		"assets/skills/claude/tesseraworkspaces/SKILL.md",
		"assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md",
		"assets/skills/copilot/tws.prompt.md",
	}
	for _, rel := range surfaces {
		t.Run(rel, func(t *testing.T) {
			content := readRepoDoc(t, rel)
			for _, lit := range literals {
				if !strings.Contains(content, lit) {
					t.Errorf("%s must contain the literal %q", rel, lit)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestSyncPlanDocs_PlanningProseSurfacesCarryShippedTargetAndNoFlagLiteral
// ---------------------------------------------------------------------------

// TestSyncPlanDocs_PlanningProseSurfacesCarryShippedTargetAndNoFlagLiteral
// implements spec.md §22.33-ii's two semantic predicates for docs/roadmap.md
// and docs/engineering-workflow.md — the planning-prose surfaces the
// documentation gate deliberately never grep for a flag literal: (1) the
// shipped feature names sit inside the shipped list/paragraph and strictly
// before the current-target sentence (never inside a backlog list or the
// current-target sentence itself), and (2) the NEXT target occupies that
// current-target position. It also asserts the negative half of §22.33-ii's
// own rule: neither file may carry any of the five flag literals.
//
// Released safety features stay in the shipped list. Post-release
// prioritization advances to scoped status after sync safety and injection
// correctness ship; the negative flag-literal contract is unchanged.
func TestSyncPlanDocs_PlanningProseSurfacesCarryShippedTargetAndNoFlagLiteral(t *testing.T) {
	literals := []string{"--plan", "--max-replay-per-entry", "--max-replay-total", "--approve-plan", "plan-guard:"}

	assertNoFlagLiteral := func(t *testing.T, rel, content string) {
		t.Helper()
		for _, lit := range literals {
			if strings.Contains(content, lit) {
				t.Errorf("%s is planning prose and must never contain the flag literal %q", rel, lit)
			}
		}
	}

	t.Run("docs/roadmap.md", func(t *testing.T) {
		content := readRepoDoc(t, "docs/roadmap.md")
		ci := strings.ToLower(content)

		if n := strings.Count(ci, "rebase plan guard"); n != 1 {
			t.Fatalf(`docs/roadmap.md must name "rebase plan guard" exactly once, found %d`, n)
		}
		guardIdx := strings.Index(ci, "rebase plan guard")
		reparentIdx := strings.Index(ci, "safe reparent/restack")
		targetIdx := strings.Index(ci, "current target:")
		backlogIdx := strings.Index(ci, "prioritized tws backlog")
		if guardIdx < 0 || reparentIdx < 0 || targetIdx < 0 || backlogIdx < 0 {
			t.Fatalf("docs/roadmap.md is missing one of the required anchors (guard=%d reparent=%d target=%d backlog=%d)", guardIdx, reparentIdx, targetIdx, backlogIdx)
		}
		if guardIdx >= reparentIdx || reparentIdx >= targetIdx || targetIdx >= backlogIdx {
			t.Fatalf("docs/roadmap.md must order: shipped \"rebase plan guard\" (%d) before shipped \"safe reparent/restack\" (%d) before \"Current target:\" (%d) before the prioritized backlog heading (%d)", guardIdx, reparentIdx, targetIdx, backlogIdx)
		}

		targetTail := ci[targetIdx : targetIdx+min(80, len(ci)-targetIdx)]
		if !strings.Contains(targetTail, "scoped status projection (issue #3)") {
			t.Fatalf(`docs/roadmap.md's "Current target:" sentence must name scoped status projection (#3), got: %q`, targetTail)
		}
		if !strings.Contains(normalizeProse(content), "ordinary sync safety (#4) is fully released and verified in v1.2.18-rc.2") {
			t.Fatal("docs/roadmap.md must retain the verified ordinary-sync release milestone before advancing the target")
		}
		if !strings.Contains(normalizeProse(content), "slash-containing worktree injection (#1) is fully released and verified in v1.2.18-rc.3") {
			t.Fatal("docs/roadmap.md must retain the verified injection release milestone before advancing the target")
		}
		previous := backlogIdx
		for _, issue := range []string{"[#4:", "[#1:", "[#3:", "[#2:"} {
			index := strings.Index(ci, issue)
			if index <= previous {
				t.Fatalf("docs/roadmap.md must order the backlog as #4, #1, #3, #2; %s is at %d after %d", issue, index, previous)
			}
			previous = index
		}

		normalized := normalizeProse(content)
		for _, claim := range []string{
			"three separate effects",
			"git refs, stack.yaml metadata, and worktree/index state",
			"ref commit is race-atomic",
			"crash-atomic",
			"only on the reftable backend",
			"exactly one commit point",
		} {
			if !strings.Contains(normalized, claim) {
				t.Errorf("docs/roadmap.md must state the reparent atomicity boundary %q", claim)
			}
		}
		for _, falseClaim := range []string{
			"refs and stack.yaml metadata atomically",
			"git refs and stack.yaml metadata atomically",
		} {
			if strings.Contains(normalized, falseClaim) {
				t.Errorf("docs/roadmap.md must not claim combined refs/metadata atomicity: %q", falseClaim)
			}
		}

		assertNoFlagLiteral(t, "docs/roadmap.md", content)
	})

	t.Run("docs/engineering-workflow.md", func(t *testing.T) {
		content := readRepoDoc(t, "docs/engineering-workflow.md")
		ci := strings.ToLower(content)

		if n := strings.Count(ci, "rebase plan guard"); n != 1 {
			t.Fatalf(`docs/engineering-workflow.md must name "rebase plan guard" exactly once, found %d`, n)
		}
		if n := strings.Count(ci, "safe reparent/restack"); n != 1 {
			t.Fatalf(`docs/engineering-workflow.md must name "safe reparent/restack" exactly once, found %d`, n)
		}
		shippedIdx := strings.Index(ci, "current shipped checkout slices")
		guardIdx := strings.Index(ci, "rebase plan guard")
		reparentIdx := strings.Index(ci, "safe reparent/restack")
		nextFeatureIdx := strings.Index(ci, "next roadmap work:")
		if shippedIdx < 0 || guardIdx < 0 || reparentIdx < 0 || nextFeatureIdx < 0 {
			t.Fatalf("docs/engineering-workflow.md is missing one of the required anchors (shipped=%d guard=%d reparent=%d next=%d)", shippedIdx, guardIdx, reparentIdx, nextFeatureIdx)
		}
		if shippedIdx >= guardIdx || guardIdx >= reparentIdx || reparentIdx >= nextFeatureIdx {
			t.Fatalf("docs/engineering-workflow.md must order: the shipped-slices heading (%d) before \"rebase plan guard\" (%d) before slice 13's \"safe reparent/restack\" (%d) before \"Next roadmap work:\" (%d)", shippedIdx, guardIdx, reparentIdx, nextFeatureIdx)
		}

		nextTail := ci[nextFeatureIdx : nextFeatureIdx+min(80, len(ci)-nextFeatureIdx)]
		if !strings.Contains(nextTail, "scoped status projection (issue #3)") {
			t.Fatalf(`docs/engineering-workflow.md's "Next roadmap work:" sentence must name scoped status projection (#3), got: %q`, nextTail)
		}
		if !strings.Contains(normalizeProse(content), "ordinary sync safety (#4) is fully released and verified in v1.2.18-rc.2") {
			t.Fatal("docs/engineering-workflow.md must retain the verified ordinary-sync release milestone before advancing the target")
		}
		if !strings.Contains(normalizeProse(content), "slash-containing worktree injection (#1) is fully released and verified in v1.2.18-rc.3") {
			t.Fatal("docs/engineering-workflow.md must retain the verified injection release milestone before advancing the target")
		}

		assertNoFlagLiteral(t, "docs/engineering-workflow.md", content)
	})
}

func TestReparentDocs_NormativeClaimsAcrossEightSurfaces(t *testing.T) {
	_ = "asserts AC-100"
	assertReparentMatrixBehavior(t, "T-086", "documentation-surfaces")
	surfaces := []string{
		"README.md",
		"docs/cheatsheet.md",
		"CHANGELOG.md",
		"assets/skills/claude/tesseraworkspaces/SKILL.md",
		"assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md",
		"assets/skills/copilot/tws.prompt.md",
		"docs/roadmap.md",
		"docs/engineering-workflow.md",
	}
	for _, rel := range surfaces {
		t.Run(rel, func(t *testing.T) {
			raw := readRepoDoc(t, rel)
			content := normalizeProse(raw)
			if strings.Contains(content, "no concurrent reader ever sees half") {
				t.Errorf("%s must not claim reader snapshot isolation", rel)
			}
			for _, contradiction := range []string{
				"no-flag run takes no lock",
				"git refs, stack.yaml metadata, and runtime state",
				"refs, stack.yaml, runtime state",
				"refs, stack.yaml metadata, and runtime state",
			} {
				if strings.Contains(content, contradiction) {
					t.Errorf("%s carries stale reparent/sync prose %q", rel, contradiction)
				}
			}
			for _, paragraph := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n\n") {
				normalized := normalizeProse(paragraph)
				if strings.Contains(normalized, "reparent") && strings.Contains(normalized, "moves nothing") {
					t.Errorf("%s must not claim a reparent plan moves nothing; external policy may fetch", rel)
				}
			}
			if !strings.Contains(content,
				"moves no branch and writes no tws state; may fetch according to policy") {
				t.Errorf("%s must carry the exact reparent plan side-effect wording", rel)
			}
			for _, claim := range []string{
				"stack-entry destination stores",
				"logical entry name",
				"named literal ref stores",
				"full refs/...",
				"raw object",
				"full lowercase oid",
				"final mutation check",
				"feature mutation lock",
				"top-level external push",
				"every mutating external sync",
				"scans every feature's recoverable checkout sync/reparent state",
				"do not use an older tws while any reparent is active or recoverable",
				"worktree/index state",
			} {
				if !strings.Contains(content, claim) {
					t.Errorf("%s must carry the canonical destination-storage claim %q", rel, claim)
				}
			}
		})
	}

	for _, rel := range []string{
		"README.md",
		"assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md",
	} {
		content := normalizeProse(readRepoDoc(t, rel))
		for _, claim := range []string{
			"concurrent write to any expected-old ref",
			"aborts the ref transaction during prepare",
			"not reader snapshot isolation",
			"crash-atomic only on",
			"refs, stack.yaml, and worktree/index state are separate effects",
			"forward",
		} {
			if !strings.Contains(content, claim) {
				t.Errorf("%s must carry the complete §9.13 atomicity claim %q", rel, claim)
			}
		}
	}

	cheatsheet := normalizeProse(readRepoDoc(t, "docs/cheatsheet.md"))
	for _, claim := range []string{
		"authoritative recorded lastbasesha",
		"cannot override",
		"unresolvable record",
	} {
		if !strings.Contains(cheatsheet, claim) {
			t.Errorf("docs/cheatsheet.md must state %q", claim)
		}
	}

	orchestrator := normalizeProse(readRepoDoc(t, "assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md"))
	fresh := `plan.runnable == true && plan.guard.wouldrefuse == false && plan.guard.executeblockedby == [] && plan.refusal.kind == null && plan.approval.usable == true`
	resume := `plan.route == "continue" && plan.runnable == true && plan.guard.wouldrefuse == false && plan.guard.executeblockedby == [] && plan.refusal.kind == null && plan.approval.scope == "resume"`
	if !strings.Contains(orchestrator, fresh) {
		t.Errorf("the orchestrator must embed the exact §7.12 fresh predicate")
	}
	if !strings.Contains(orchestrator, resume) {
		t.Errorf("the orchestrator must embed the exact §7.12a continue predicate")
	}
	if strings.Contains(orchestrator, `plan.summary.plannability == "ok"`) {
		t.Error(`the fresh predicate must not use the nonexistent plannability value "ok"`)
	}
	if !strings.Contains(orchestrator, "reparent plan's target and descendants[]") {
		t.Error("the orchestrator must describe reparent rows as target + descendants[]")
	}
	if strings.Contains(orchestrator, "reparent plan's entries[]") {
		t.Error("the orchestrator must not reuse sync's entries[] shape for reparent")
	}

	readme := normalizeProse(readRepoDoc(t, "README.md"))
	if got := strings.Count(readme, "a refusal tws already performs"); got != 1 {
		t.Errorf("README.md must contain one unsplit refusal paragraph, found %d copies", got)
	}

	for _, rel := range []string{
		"README.md",
		"docs/cheatsheet.md",
		"assets/skills/claude/tesseraworkspaces/SKILL.md",
		"assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md",
		"assets/skills/copilot/tws.prompt.md",
	} {
		content := normalizeProse(readRepoDoc(t, rel))
		for _, claim := range []string{"same replay limit", "limitless preview", "null fingerprint"} {
			if !strings.Contains(content, claim) {
				t.Errorf("%s must state %q", rel, claim)
			}
		}
		raw := readRepoDoc(t, rel)
		lines := strings.Split(raw, "\n")
		for i, line := range lines {
			if !strings.Contains(line, "tws stack reparent") || !strings.Contains(line, "--plan") {
				continue
			}
			command := line
			for j := i + 1; j < len(lines) && j <= i+2 && strings.HasSuffix(strings.TrimSpace(command), "\\"); j++ {
				command += " " + lines[j]
			}
			if !strings.Contains(command, "--max-replay-per-entry") && !strings.Contains(command, "--max-replay-total") {
				t.Errorf("%s has a limitless reparent plan example: %s", rel, command)
			}
		}
	}
}

func TestReparentDocs_GuardedWorkflowExecutes(t *testing.T) {
	f := newReparentCustomerExternal(t)
	const limit = "20"
	human, prose := f.Plan(
		f.Feature, "pr2", "--onto", "master",
		"--max-replay-total", limit,
	)
	fingerprint := reparentFingerprint(t, human+"\n"+prose)
	if fingerprint == "" {
		t.Fatal("documented bounded preview minted no fingerprint")
	}
	stdout, stderr, exit := f.Run(
		f.Feature, "pr2", "--onto", "master",
		"--max-replay-total", limit,
		"--approve-plan", fingerprint,
	)
	if exit != 0 || stdout != "" {
		t.Fatalf("documented plan/approve/execute workflow = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
}

// ---------------------------------------------------------------------------
// TestSyncPlanDocs_ChangelogRecoveryCellsAreSeparateItems
// ---------------------------------------------------------------------------

// changelogUnreleasedBullets splits CHANGELOG.md's "## Unreleased" section
// into its top-level "- " bulleted items, so callers can assert content
// scoped to one bullet rather than the whole section (which would let two
// merged cells masquerade as three).
func changelogUnreleasedBullets(t *testing.T) []string {
	t.Helper()
	content := readRepoDoc(t, "CHANGELOG.md")
	start := strings.Index(content, "## Unreleased")
	if start < 0 {
		t.Fatal("CHANGELOG.md must have an \"## Unreleased\" section")
	}
	rest := content[start:]
	headingRe := regexp.MustCompile(`(?m)^## `)
	headings := headingRe.FindAllStringIndex(rest, -1)
	block := rest
	if len(headings) >= 2 {
		block = rest[:headings[1][0]]
	}
	bulletRe := regexp.MustCompile(`(?m)^- `)
	locs := bulletRe.FindAllStringIndex(block, -1)
	if len(locs) == 0 {
		t.Fatal("no top-level bullets found in CHANGELOG.md's Unreleased section")
	}
	bullets := make([]string, 0, len(locs))
	for i, loc := range locs {
		end := len(block)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		bullets = append(bullets, block[loc[0]:end])
	}
	return bullets
}

// TestSyncPlanDocs_ChangelogRecoveryCellsAreSeparateItems implements
// spec.md §22.33-i-a: the CHANGELOG.md Unreleased entry must disclose all
// three no-flag recovery cells (cell 1, cell 7, cell 4 — sync_recovery_test.go's
// Case1/Case7/Cell4 coverage) as three separate bullets, each carrying its
// own required content. The spec states plainly that "a check that finds
// cell 1 and cell 7 but not cell 4 fails"; this test encodes that literally
// by asserting each cell's disclosure independently, so dropping any one
// cell's content — or merging two cells into one bullet — fails on its own,
// never masked by the other two passing.
func TestSyncPlanDocs_ChangelogRecoveryCellsAreSeparateItems(t *testing.T) {
	full := strings.ToLower(readRepoDoc(t, "CHANGELOG.md"))
	for _, marker := range []string{"recovery cell 1", "recovery cell 7", "recovery cell 4"} {
		if n := strings.Count(full, marker); n != 1 {
			t.Fatalf("CHANGELOG.md must name %q exactly once, found %d", marker, n)
		}
	}

	bullets := changelogUnreleasedBullets(t)
	find := func(marker string) (int, string) {
		for i, b := range bullets {
			if strings.Contains(strings.ToLower(b), marker) {
				return i, b
			}
		}
		t.Fatalf("no CHANGELOG.md bullet in ## Unreleased names %q", marker)
		return -1, ""
	}

	idx1, bullet1 := find("recovery cell 1")
	idx7, bullet7 := find("recovery cell 7")
	idx4, bullet4 := find("recovery cell 4")

	if idx1 == idx7 || idx1 == idx4 || idx7 == idx4 {
		t.Fatalf("recovery cells 1, 7 and 4 must be three separate CHANGELOG bullets; got bullet indices %d, %d, %d", idx1, idx7, idx4)
	}

	n1 := normalizeProse(bullet1)
	if !strings.Contains(n1, "nothing to abort") || !strings.Contains(n1, "no sync in progress") {
		t.Errorf("cell 1 bullet must disclose the previous \"Nothing to abort — no sync in progress.\" wording:\n%s", bullet1)
	}
	if !strings.Contains(n1, "stale sync guard from pid <pid> cleared; no sync state was present.") {
		t.Errorf("cell 1 bullet must quote the new stale-guard message (normalized):\n%s", bullet1)
	}

	n7 := normalizeProse(bullet7)
	if !strings.Contains(n7, "clears both") {
		t.Errorf("cell 7 bullet must disclose that --abort now clears both artefacts:\n%s", bullet7)
	}
	if !strings.Contains(n7, "sync state cleared; stale sync guard from pid <pid> cleared.") {
		t.Errorf("cell 7 bullet must quote the combined-clear message (normalized):\n%s", bullet7)
	}

	n4 := normalizeProse(bullet4)
	if !strings.Contains(n4, "--continue") || !strings.Contains(n4, "--abort") {
		t.Errorf("cell 4 bullet must name both recovery verbs, --continue and --abort:\n%s", bullet4)
	}
	if !strings.Contains(n4, "the interrupted guarded setup's backup of the previous sync state was discarded") {
		t.Errorf("cell 4 bullet must quote the discard sentence (normalized):\n%s", bullet4)
	}
	if !strings.Contains(n4, "refuses the same residue or discards the backup") {
		t.Errorf("cell 4 bullet must disclose the older-release downgrade note:\n%s", bullet4)
	}
}

// TestSyncPlanDocs_ChangelogCheckoutDowngradeSentenceIsContinueOnly pins the
// one attribution §13.6 rule 5's old-binary table makes and the CHANGELOG
// must not blur: the checkout version-comparison sentence
// `checkout sync transaction state version 3 is newer than 2; upgrade tws or
// remove <path>` is the OLD binary's `--continue` answer alone. A FRESH old
// binary run over the same transaction never reaches that comparison — it
// refuses earlier, at HasCheckoutTransaction, with the shipped
// `previous checkout-sync incomplete; use --continue or --abort`.
//
// A CHANGELOG that attributes the version sentence to "fresh/--continue"
// would tell an operator to expect words a fresh run never prints, so the
// two sentences are asserted to be attributed separately here, and the
// version sentence is asserted NOT to be attributed to a fresh run.
func TestSyncPlanDocs_ChangelogCheckoutDowngradeSentenceIsContinueOnly(t *testing.T) {
	const versionSentence = "checkout sync transaction state version 3 is newer than 2; upgrade tws or remove <path>"
	const freshSentence = "previous checkout-sync incomplete; use --continue or --abort"

	full := normalizeProse(readRepoDoc(t, "CHANGELOG.md"))
	if !strings.Contains(full, strings.ToLower(versionSentence)) {
		t.Fatalf("CHANGELOG.md must quote the checkout downgrade sentence %q", versionSentence)
	}

	bullets := changelogUnreleasedBullets(t)
	var owner string
	for _, b := range bullets {
		if strings.Contains(normalizeProse(b), strings.ToLower(versionSentence)) {
			if owner != "" {
				t.Fatalf("the checkout downgrade sentence must live in exactly one Unreleased bullet")
			}
			owner = normalizeProse(b)
		}
	}
	if owner == "" {
		t.Fatal("no ## Unreleased bullet quotes the checkout downgrade sentence")
	}

	// The version sentence must be attributed to --continue...
	idx := strings.Index(owner, strings.ToLower(versionSentence))
	lead := owner[:idx]
	if !strings.Contains(lead, "--continue") {
		t.Errorf("the checkout downgrade sentence must be attributed to --continue:\n%s", owner)
	}
	// ...and never jointly to a fresh run.
	for _, blurred := range []string{"fresh/--continue", "fresh/`--continue`", "fresh or --continue", "--continue/fresh"} {
		if strings.Contains(owner, blurred) {
			t.Errorf("the checkout downgrade sentence must not be attributed to %q: a fresh run never reaches the version comparison:\n%s", blurred, owner)
		}
	}
	// If the fresh cell is mentioned at all, it must carry its OWN sentence.
	if strings.Contains(owner, "fresh") && !strings.Contains(owner, strings.ToLower(freshSentence)) {
		t.Errorf("a bullet that mentions the fresh checkout cell must quote its own sentence %q:\n%s", freshSentence, owner)
	}
}

// ===========================================================================
// §22.34 — the gate suite is declared, runnable and non-vacuous.
// ===========================================================================

// criterion34GateCommands is §22.34's own list, verbatim: the full gate
// suite, in the spec's order.
var criterion34GateCommands = []string{
	"gofmt -l internal cmd",
	"go test ./... -count=1",
	"go vet ./...",
	"golangci-lint run ./...",
	"make build",
	"git diff --check",
	"tpatch feature deps --validate-all",
}

// criterion34FocusedSelectors is §22.34's "focused gates while implementing"
// block: the -run selectors an implementer is told to use. A selector that
// matches no declared test in the corpus is a documentation lie, so each is
// asserted non-vacuous below.
var criterion34FocusedSelectors = []string{
	"TestRebasePlan|TestPlanFingerprint|TestPlanSchema|TestRemainingRebaseEntries",
	"TestSyncPlan|TestPlanGuard|TestSyncNoFlag_",
	"TestSyncScoped_|TestCheckoutSyncModes_",
}

// declaredTestNames parses every `func TestXxx(` declaration under the two
// packages this feature touches, by scanning the source rather than by
// running anything.
func declaredTestNames(t *testing.T) []string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`)
	var names []string
	for _, dir := range []string{"internal", "internal/cli"} {
		entries, err := os.ReadDir(filepath.Join(docsRepoRoot, dir))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(docsRepoRoot, dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
			}
			for _, m := range re.FindAllStringSubmatch(string(data), -1) {
				names = append(names, m[1])
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("found no declared test functions at all; the scan is broken")
	}
	return names
}

// TestSyncPlanDocs_Criterion22_34_GateSuiteIsDeclaredAndNonVacuous is
// §22.34's executable owner. The suite itself is run by the outer harness;
// what a test inside the repository can and must own is that every gate the
// criterion names really EXISTS here and that none of its selectors is
// vacuous:
//
//   - `gofmt -l internal cmd` is executed for real and must report nothing;
//   - `make build` names a real Makefile target, and the Makefile also
//     declares the `test` and `lint` targets the suite leans on;
//   - `git diff --check` is executed for real against the working tree;
//   - each focused `-run` selector matches at least one declared test in
//     `internal/` or `internal/cli/`;
//   - the frozen-path gate of criterion 1 still holds: `TestSyncNoFlag_`
//     matches, and the fixture directory still contains exactly 126 files.
//
// A gate command list that drifts from the spec's own block, or a selector
// that stops matching anything, fails here.
func TestSyncPlanDocs_Criterion22_34_GateSuiteIsDeclaredAndNonVacuous(t *testing.T) {
	if len(criterion34GateCommands) != 7 {
		t.Fatalf("§22.34 lists 7 gate commands, this table has %d", len(criterion34GateCommands))
	}

	t.Run("gofmt_reports_nothing", func(t *testing.T) {
		out, err := exec.Command("gofmt", "-l",
			filepath.Join(docsRepoRoot, "internal"), filepath.Join(docsRepoRoot, "cmd")).CombinedOutput()
		if err != nil {
			t.Fatalf("gofmt -l internal cmd: %v\n%s", err, out)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Fatalf("gofmt -l internal cmd reported unformatted files:\n%s", out)
		}
	})

	t.Run("make_targets_exist", func(t *testing.T) {
		makefile := readRepoDoc(t, "Makefile")
		for _, target := range []string{"build:", "test:", "lint:"} {
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target)).MatchString(makefile) {
				t.Fatalf("Makefile declares no %q target, but §22.34's suite invokes it", target)
			}
		}
	})

	t.Run("git_diff_check_is_clean", func(t *testing.T) {
		cmd := exec.Command("git", "diff", "--check")
		cmd.Dir = docsRepoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git diff --check failed: %v\n%s", err, out)
		}
	})

	t.Run("focused_selectors_are_non_vacuous", func(t *testing.T) {
		names := declaredTestNames(t)
		for _, selector := range criterion34FocusedSelectors {
			re, err := regexp.Compile(selector)
			if err != nil {
				t.Fatalf("selector %q does not compile: %v", selector, err)
			}
			matched := 0
			for _, n := range names {
				if re.MatchString(n) {
					matched++
				}
			}
			if matched == 0 {
				t.Fatalf("focused selector %q matches no declared test: the documented gate is vacuous", selector)
			}
		}
	})

	t.Run("frozen_path_gate_still_holds", func(t *testing.T) {
		names := declaredTestNames(t)
		noflag := 0
		for _, n := range names {
			if strings.HasPrefix(n, "TestSyncNoFlag_") {
				noflag++
			}
		}
		if noflag == 0 {
			t.Fatal("no TestSyncNoFlag_ test is declared; §22 criterion 1's frozen-path gate would be vacuous")
		}
		files := 0
		root := filepath.Join(docsRepoRoot, "internal", "cli", "testdata", "sync_noflag")
		if err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				files++
			}
			return nil
		}); err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
		if files != 126 {
			t.Fatalf("internal/cli/testdata/sync_noflag holds %d files, want the frozen 126", files)
		}
	})
}

// ---------------------------------------------------------------------------
// TestSyncPlanDocs_NegativeCheckHasExactlyTwoHalves
// ---------------------------------------------------------------------------

// bannedDocPhrases are the three substrings spec.md §22.33a forbids: an
// unqualified "--plan mutates nothing" claim, a bare `from` scope-value
// spelling, and a ninth warning kind. None of these are flag literals, so
// they are checked separately from TestSyncPlanDocs_FlagWorkflowSurfacesCarryAllFiveLiterals.
var bannedDocPhrases = []string{"without mutating anything", "scope=from", "nine warning"}

// TestSyncPlanDocs_NegativeCheckHasExactlyTwoHalves implements spec.md
// §22.33a's negative check exactly as scoped: "exactly two halves, and no
// third". Half (a) is a corpus grep over README.md, CHANGELOG.md, docs/ and
// assets/ only — never internal/, never cmd/, never a _test.go file, never
// a testdata directory — because tests legitimately contain every banned
// phrase as negative fixtures, so an unscoped grep would forbid the very
// tests that enforce this criterion. Half (b) is the exact tws sync --help
// output, asserted never to contain any of the three phrases as a
// substring.
func TestSyncPlanDocs_NegativeCheckHasExactlyTwoHalves(t *testing.T) {
	t.Run("prose-and-assets", func(t *testing.T) {
		// This walk is built to match the spec's corpus by construction: it
		// only ever reads README.md, CHANGELOG.md and files under docs/ and
		// assets/, and it skips any directory named "testdata" that it
		// encounters. It never touches internal/, cmd/, or any _test.go
		// file — those simply are not among the four roots below.
		roots := []string{
			filepath.Join(docsRepoRoot, "README.md"),
			filepath.Join(docsRepoRoot, "CHANGELOG.md"),
			filepath.Join(docsRepoRoot, "docs"),
			filepath.Join(docsRepoRoot, "assets"),
		}
		var files []string
		for _, root := range roots {
			info, err := os.Stat(root)
			if err != nil {
				t.Fatalf("stat %s: %v", root, err)
			}
			if !info.IsDir() {
				files = append(files, root)
				continue
			}
			if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					if d.Name() == "testdata" {
						return filepath.SkipDir
					}
					return nil
				}
				files = append(files, path)
				return nil
			}); err != nil {
				t.Fatalf("walking %s: %v", root, err)
			}
		}
		if len(files) < 5 {
			t.Fatalf("corpus walk found only %d files, expected the README, CHANGELOG and docs/+assets/ trees", len(files))
		}

		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			content := string(data)
			for _, phrase := range bannedDocPhrases {
				if strings.Contains(content, phrase) {
					t.Errorf("%s must not contain the banned phrase %q", path, phrase)
				}
			}
		}

		// Positive control: the Go tree legitimately contains a banned
		// phrase as a negative-fixture string (internal/rebase_plan_render_test.go
		// asserts a rendered Policy line never prints "scope=from"). This
		// file is deliberately outside every root above; if it had been
		// included, this exact check would fail — which is the reason
		// spec.md §22.33a scopes the corpus away from the Go tree at all,
		// rather than an assumption this test takes on faith.
		goFixture := filepath.Join("..", "rebase_plan_render_test.go")
		data, err := os.ReadFile(goFixture)
		if err != nil {
			t.Fatalf("reading %s: %v", goFixture, err)
		}
		if !strings.Contains(string(data), "scope=from") {
			t.Fatalf("%s no longer contains the banned literal \"scope=from\" as a negative-fixture string; the corpus-exclusion rationale (spec.md §22.33a) has nothing left to guard against", goFixture)
		}
	})

	t.Run("help-output", func(t *testing.T) {
		stdout, _, exit := runSyncExecute(t, "--help")
		if exit != 0 {
			t.Fatalf("tws sync --help must exit 0, got %d", exit)
		}
		for _, phrase := range bannedDocPhrases {
			if strings.Contains(stdout, phrase) {
				t.Errorf("tws sync --help must not contain the banned phrase %q:\n%s", phrase, stdout)
			}
		}
	})
}
