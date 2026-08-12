package internal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---------- Ancestry test helpers ----------

func evalEdges(t *testing.T, repoDir string, entries ...StackEntry) []StackEdge {
	t.Helper()
	return evalEdgesWith(t, repoDir, StackAncestryOptions{}, entries...)
}

func evalEdgesWith(t *testing.T, repoDir string, opts StackAncestryOptions, entries ...StackEntry) []StackEdge {
	t.Helper()
	edges, err := EvaluateStackAncestry(repoDir, "myfeat", Stack{Branches: entries}, opts)
	if err != nil {
		t.Fatalf("EvaluateStackAncestry: %v", err)
	}
	if len(edges) != len(entries) {
		t.Fatalf("expected %d edges, got %d", len(entries), len(edges))
	}
	return edges
}

func edgeNamed(t *testing.T, edges []StackEdge, name string) StackEdge {
	t.Helper()
	for _, e := range edges {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("edge %q not found", name)
	return StackEdge{}
}

func sha(t *testing.T, dir, rev string) string {
	t.Helper()
	return gitInTest(t, dir, "rev-parse", rev)
}

// forkChild creates branch child at base and adds n empty commits to it,
// leaving HEAD on the repository's original branch.
func forkChild(t *testing.T, dir, child, base string, commits int) {
	t.Helper()
	original := gitInTest(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	gitInTest(t, dir, "checkout", "-b", child, base)
	for i := 0; i < commits; i++ {
		gitInTest(t, dir, "commit", "--allow-empty", "-m", fmt.Sprintf("%s-%d", child, i))
	}
	gitInTest(t, dir, "checkout", original)
}

// advance appends n empty commits to branch without leaving HEAD on it.
func advance(t *testing.T, dir, branch string, commits int) {
	t.Helper()
	original := gitInTest(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	gitInTest(t, dir, "checkout", branch)
	for i := 0; i < commits; i++ {
		gitInTest(t, dir, "commit", "--allow-empty", "-m", fmt.Sprintf("advance-%s-%d", branch, i))
	}
	gitInTest(t, dir, "checkout", original)
}

// secondRepo builds an independent real repository used for cross-repo tests.
func secondRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInTest(t, dir, "init", "--initial-branch=main")
	gitInTest(t, dir, "commit", "--allow-empty", "-m", "foreign init")
	return dir
}

// ---------- PATH shim (AC 41 / AC 43) ----------

type gitInvocation struct {
	dir  string
	args []string
}

type gitShim struct {
	record string
}

func withGitShim(t *testing.T) *gitShim {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not found: %v", err)
	}
	binDir := t.TempDir()
	record := filepath.Join(binDir, "invocations.log")
	script := "#!/bin/sh\n{\n  printf '%s' \"$(pwd -P)\"\n  for a in \"$@\"; do printf '\\034%s' \"$a\"; done\n  printf '\\n'\n} >> '" + record + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, nil, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &gitShim{record: record}
}

// reset truncates the record immediately before a measured call, so fixture
// Git commands never contaminate the assertions.
func (g *gitShim) reset(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(g.record, nil, 0644); err != nil {
		t.Fatal(err)
	}
}

func (g *gitShim) invocations(t *testing.T) []gitInvocation {
	t.Helper()
	data, err := os.ReadFile(g.record)
	if err != nil {
		t.Fatal(err)
	}
	var out []gitInvocation
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\x1c")
		out = append(out, gitInvocation{dir: parts[0], args: parts[1:]})
	}
	return out
}

var nonAncestryShapes = [][]string{
	{"rev-parse", "--abbrev-ref", "HEAD"},
	{"rev-parse", "--short", "HEAD"},
	{"status", "--porcelain"},
	{"rev-parse", "--show-toplevel"},
	{"rev-parse", "--git-common-dir"},
	{"rev-parse", "--abbrev-ref", "origin/HEAD"},
	{"symbolic-ref", "--short", "HEAD"},
}

func stripDashC(args []string) []string {
	if len(args) >= 2 && args[0] == "-C" {
		return args[2:]
	}
	return args
}

func isNonAncestryShape(args []string) bool {
	stripped := stripDashC(args)
	for _, shape := range nonAncestryShapes {
		if len(stripped) != len(shape) {
			continue
		}
		match := true
		for i := range shape {
			if stripped[i] != shape[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ancestryProbes returns the invocations that are direct ancestry Git probes.
func ancestryProbes(t *testing.T, shim *gitShim) []gitInvocation {
	t.Helper()
	var probes []gitInvocation
	for _, inv := range shim.invocations(t) {
		if isNonAncestryShape(inv.args) {
			continue
		}
		probes = append(probes, inv)
	}
	return probes
}

// ---------- AC 1-11: classification ----------

func TestStackAncestry_ParentContained(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "child", "main", 1)

	edge := evalEdges(t, dir, StackEntry{Name: "child", Base: "main"})[0]

	if edge.Status != AncestryStatusCurrent {
		t.Fatalf("status = %q, want current", edge.Status)
	}
	if edge.Reason != ReasonParentContained {
		t.Errorf("reason = %q, want parent-contained", edge.Reason)
	}
	if edge.Severity != SeverityOK {
		t.Errorf("severity = %q, want ok", edge.Severity)
	}
	if edge.Guidance != "" {
		t.Errorf("guidance = %q, want empty", edge.Guidance)
	}
	if edge.MergeBase == nil || *edge.MergeBase != edge.ParentHead {
		t.Errorf("merge base = %v, want parent head %s", edge.MergeBase, edge.ParentHead)
	}
}

func TestStackAncestry_ChildEqualsParent(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	gitInTest(t, dir, "branch", "child", "main")

	edge := evalEdges(t, dir, StackEntry{Name: "child", Base: "main"})[0]
	if edge.Status != AncestryStatusCurrent {
		t.Fatalf("status = %q, want current", edge.Status)
	}
}

func TestStackAncestry_ParentBackwardsInsideChild(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	older := sha(t, dir, "main")
	forkChild(t, dir, "parent", "main", 1)
	newerParent := sha(t, dir, "parent")
	forkChild(t, dir, "child", "parent", 1)
	gitInTest(t, dir, "branch", "-f", "parent", older)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: newerParent},
	), "child")

	if edge.Status != AncestryStatusCurrent {
		t.Fatalf("status = %q, want current (rule 1 must precede the base-record test)", edge.Status)
	}
	if edge.Reason != ReasonParentContained {
		t.Errorf("reason = %q, want parent-contained", edge.Reason)
	}
	if edge.Status == AncestryStatusDivergent {
		t.Error("a parent reset backwards inside the child must never be divergent")
	}
}

func TestStackAncestry_ParentFastForwardWithChildWork(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	recorded := sha(t, dir, "parent")
	forkChild(t, dir, "child", "parent", 1)
	advance(t, dir, "parent", 1)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded},
	), "child")

	if edge.Status != AncestryStatusStale {
		t.Fatalf("status = %q, want stale", edge.Status)
	}
	if edge.Reason != ReasonParentAdvanced {
		t.Errorf("reason = %q, want parent-advanced", edge.Reason)
	}
	if edge.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", edge.Severity)
	}
	if !strings.Contains(edge.Guidance, "tws sync") {
		t.Errorf("guidance %q should mention tws sync", edge.Guidance)
	}
	if strings.Contains(edge.Guidance, "--onto") {
		t.Errorf("guidance %q must not promise --onto for a fast-forward", edge.Guidance)
	}
}

func TestStackAncestry_ParentFastForwardNoChildWork(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	recorded := sha(t, dir, "parent")
	gitInTest(t, dir, "branch", "child", "parent")
	advance(t, dir, "parent", 1)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded},
	), "child")

	if edge.Status != AncestryStatusStale {
		t.Fatalf("status = %q, want stale", edge.Status)
	}
	if edge.Reason != ReasonParentAdvanced {
		t.Errorf("reason = %q, want parent-advanced", edge.Reason)
	}
}

// sidewaysRewrite force-moves parent to a commit outside the child's history
// and returns the recorded pre-move parent tip.
func sidewaysRewrite(t *testing.T, dir string) string {
	t.Helper()
	root := sha(t, dir, "main")
	forkChild(t, dir, "parent", "main", 1)
	recorded := sha(t, dir, "parent")
	forkChild(t, dir, "child", "parent", 1)
	forkChild(t, dir, "sideways", root, 1)
	gitInTest(t, dir, "branch", "-f", "parent", "sideways")
	return recorded
}

func TestStackAncestry_SidewaysRewrite(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	recorded := sidewaysRewrite(t, dir)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded},
	), "child")

	if edge.Status != AncestryStatusDivergent {
		t.Fatalf("status = %q, want divergent", edge.Status)
	}
	if edge.Reason != ReasonBaseRewritten {
		t.Errorf("reason = %q, want base-rewritten", edge.Reason)
	}
	if !strings.Contains(edge.Guidance, "git rebase --onto") {
		t.Errorf("guidance %q should carry the --onto recipe", edge.Guidance)
	}
	if edge.LastBaseShort == "" || !strings.Contains(edge.Guidance, edge.LastBaseShort) {
		t.Errorf("guidance %q should carry the short recorded base %q", edge.Guidance, edge.LastBaseShort)
	}
}

func TestStackAncestry_ParentAmended(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	recorded := sha(t, dir, "parent")
	forkChild(t, dir, "child", "parent", 1)
	gitInTest(t, dir, "checkout", "parent")
	gitInTest(t, dir, "commit", "--amend", "--allow-empty", "-m", "amended parent")
	gitInTest(t, dir, "checkout", "main")

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded},
	), "child")

	if edge.Status != AncestryStatusDivergent || edge.Reason != ReasonBaseRewritten {
		t.Fatalf("status/reason = %q/%q, want divergent/base-rewritten", edge.Status, edge.Reason)
	}
}

func TestStackAncestry_ParentRebased(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "otherbase", "main", 1)
	forkChild(t, dir, "parent", "main", 1)
	recorded := sha(t, dir, "parent")
	forkChild(t, dir, "child", "parent", 1)
	gitInTest(t, dir, "rebase", "--onto", "otherbase", "main", "parent")
	gitInTest(t, dir, "checkout", "main")

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded},
	), "child")

	if edge.Status != AncestryStatusDivergent || edge.Reason != ReasonBaseRewritten {
		t.Fatalf("status/reason = %q/%q, want divergent/base-rewritten", edge.Status, edge.Reason)
	}
}

func TestStackAncestry_SidewaysRewriteNoRecord(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	sidewaysRewrite(t, dir)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent"},
	), "child")

	if edge.Status != AncestryStatusStale {
		t.Fatalf("status = %q, want stale", edge.Status)
	}
	if edge.Reason != ReasonParentAdvancedNoBaseRecord {
		t.Errorf("reason = %q, want parent-advanced-no-base-record", edge.Reason)
	}
	if edge.BaseRecord != StackBaseRecordAbsent {
		t.Errorf("base record = %q, want absent", edge.BaseRecord)
	}
	if !strings.Contains(edge.Guidance, "verify the parent history was not rewritten") {
		t.Errorf("guidance %q must state the uncertainty honestly", edge.Guidance)
	}
}

func TestStackAncestry_UnrelatedHistories(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	gitInTest(t, dir, "checkout", "--orphan", "orph")
	gitInTest(t, dir, "commit", "--allow-empty", "-m", "orphan root")
	gitInTest(t, dir, "checkout", "main")

	edge := evalEdges(t, dir, StackEntry{Name: "orph", Base: "main"})[0]

	if edge.Status != AncestryStatusDivergent {
		t.Fatalf("status = %q, want divergent", edge.Status)
	}
	if edge.Reason != ReasonUnrelatedHistories {
		t.Errorf("reason = %q, want unrelated-histories", edge.Reason)
	}
	if edge.MergeBase != nil {
		t.Errorf("merge base = %v, want nil", *edge.MergeBase)
	}
	if edge.Status == AncestryStatusMissing {
		t.Error("unrelated histories must never report missing")
	}
	if !edge.RefExists || edge.ParentHead == "" {
		t.Error("both refs must be reported as existing")
	}
}

func TestStackAncestry_SyncTriggerNonEquivalence(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	recorded := sha(t, dir, "parent")
	gitInTest(t, dir, "branch", "child", "parent")
	advance(t, dir, "parent", 1)
	currentBaseSHA := sha(t, dir, "parent")

	entry := StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded}
	edge := edgeNamed(t, evalEdges(t, dir, StackEntry{Name: "parent", Base: "main"}, entry), "child")

	if edge.Status != AncestryStatusStale {
		t.Fatalf("status = %q, want stale", edge.Status)
	}
	// The sync paths select --onto on SHA inequality, so --onto selection is a
	// strict superset of divergent.
	if entry.LastBaseSHA == "" || entry.LastBaseSHA == currentBaseSHA {
		t.Fatal("expected the sync --onto predicate to be true for a stale edge")
	}
}

// ---------- AC 12-14: base record states ----------

func TestStackAncestry_BaseRecordAbsent(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	forkChild(t, dir, "child", "parent", 1)
	advance(t, dir, "parent", 1)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent"},
	), "child")

	if edge.Status != AncestryStatusStale {
		t.Fatalf("status = %q, want stale", edge.Status)
	}
	if edge.BaseRecord != StackBaseRecordAbsent {
		t.Errorf("base record = %q, want absent", edge.BaseRecord)
	}
	if edge.LastBaseCommit != "" {
		t.Errorf("last base commit = %q, want empty", edge.LastBaseCommit)
	}
	if edge.Status == AncestryStatusDivergent {
		t.Error("an absent base record must never produce divergent")
	}
}

func TestStackAncestry_BaseRecordPruned(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	forkChild(t, dir, "child", "parent", 1)
	advance(t, dir, "parent", 1)

	pruned := "0123456789abcdef0123456789abcdef01234567"
	edges, err := EvaluateStackAncestry(dir, "myfeat", Stack{Branches: []StackEntry{
		{Name: "parent", Base: "main"},
		{Name: "child", Base: "parent", LastBaseSHA: pruned},
	}}, StackAncestryOptions{})
	if err != nil {
		t.Fatalf("evaluator must not error on a pruned base record: %v", err)
	}
	edge := edgeNamed(t, edges, "child")

	if edge.Status != AncestryStatusStale {
		t.Fatalf("status = %q, want stale", edge.Status)
	}
	if edge.Reason != ReasonBaseRecordUnresolvable {
		t.Errorf("reason = %q, want base-record-unresolvable", edge.Reason)
	}
	if edge.BaseRecord != StackBaseRecordUnresolvable {
		t.Errorf("base record = %q, want unresolvable", edge.BaseRecord)
	}
	if edge.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", edge.Severity)
	}
	if !strings.Contains(edge.Guidance, "cannot be verified") {
		t.Errorf("guidance %q should say the strategy cannot be verified", edge.Guidance)
	}
	if strings.Contains(edge.Guidance, "--onto") {
		t.Errorf("guidance %q must not offer an --onto recipe it cannot resolve", edge.Guidance)
	}

	absent := ancestryGuidance(StackEdge{Feature: "myfeat", Reason: ReasonParentAdvancedNoBaseRecord}, "")
	probeFailed := ancestryGuidance(StackEdge{Feature: "myfeat", Reason: ReasonAncestryProbeFailed}, "boom")
	if edge.Guidance == absent || edge.Guidance == probeFailed {
		t.Error("the three unusable-base-record reasons must stay distinguishable")
	}
}

func TestStackAncestry_BaseRecordAnnotatedTagObject(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	gitInTest(t, dir, "tag", "-a", "sync-point", "-m", "sync point", "parent")
	tagObject := sha(t, dir, "sync-point")
	tagCommit := sha(t, dir, "sync-point^{commit}")
	if tagObject == tagCommit {
		t.Fatal("fixture must use an annotated tag object")
	}
	forkChild(t, dir, "child", "parent", 1)
	advance(t, dir, "parent", 1)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: tagObject},
	), "child")

	if edge.Status != AncestryStatusStale || edge.Reason != ReasonParentAdvanced {
		t.Fatalf("status/reason = %q/%q, want stale/parent-advanced", edge.Status, edge.Reason)
	}
	if edge.LastBaseCommit != tagCommit {
		t.Errorf("last base commit = %q, want the peeled commit %q", edge.LastBaseCommit, tagCommit)
	}
}

// ---------- AC 15-22: ref handling ----------

func TestStackAncestry_AnnotatedTagBaseCurrent(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	gitInTest(t, dir, "tag", "-a", "v1", "-m", "release one", "main")
	forkChild(t, dir, "child", "main", 1)

	edge := evalEdges(t, dir, StackEntry{Name: "child", Base: "v1"})[0]
	if edge.Status != AncestryStatusCurrent {
		t.Fatalf("status = %q, want current for an annotated tag base", edge.Status)
	}
	if edge.BaseKind != StackBaseLiteralRef {
		t.Errorf("base kind = %q, want literal-ref", edge.BaseKind)
	}
}

func TestStackAncestry_LiteralSHABase(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	base := sha(t, dir, "main")
	forkChild(t, dir, "child", "main", 1)

	edge := evalEdges(t, dir, StackEntry{Name: "child", Base: base})[0]
	if edge.Status != AncestryStatusCurrent {
		t.Fatalf("status = %q, want current", edge.Status)
	}
	if edge.BaseKind != StackBaseLiteralRef {
		t.Errorf("base kind = %q, want literal-ref", edge.BaseKind)
	}
}

func TestStackAncestry_BogusSHABase(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "child", "main", 1)

	edge := evalEdges(t, dir, StackEntry{Name: "child", Base: "0123456789abcdef0123456789abcdef01234567"})[0]
	if edge.Status != AncestryStatusMissing {
		t.Fatalf("status = %q, want missing", edge.Status)
	}
	if edge.Reason != ReasonBaseRefMissing {
		t.Errorf("reason = %q, want base-ref-missing", edge.Reason)
	}
}

func TestStackAncestry_DeletedBaseBranch(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	forkChild(t, dir, "child", "parent", 1)
	gitInTest(t, dir, "branch", "-D", "parent")

	edge := evalEdges(t, dir, StackEntry{Name: "child", Base: "parent"})[0]
	if edge.Status != AncestryStatusMissing || edge.Reason != ReasonBaseRefMissing {
		t.Fatalf("status/reason = %q/%q, want missing/base-ref-missing", edge.Status, edge.Reason)
	}
	if !edge.RefExists {
		t.Error("the child ref resolved and must still be reported as existing")
	}
	if edge.LocalHeadShort == "" {
		t.Error("the child head resolved before the base failed and must be reported")
	}
}

func TestStackAncestry_MissingChildRefActive(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)

	edge := evalEdges(t, dir, StackEntry{Name: "ghost", Base: "main"})[0]
	if edge.Status != AncestryStatusMissing || edge.Reason != ReasonChildRefMissing {
		t.Fatalf("status/reason = %q/%q, want missing/child-ref-missing", edge.Status, edge.Reason)
	}
	if edge.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", edge.Severity)
	}
	assertChildRefMissingGuidance(t, edge)
	if edge.LocalHead != "" || edge.ParentHead != "" {
		t.Error("a child-ref-missing edge reports no heads")
	}
}

// assertChildRefMissingGuidance pins the only two honest recoveries for a
// stack entry whose Git branch vanished. `tws new` is not one of them: the
// entry already exists, so the command cannot be the advertised shortcut.
func assertChildRefMissingGuidance(t *testing.T, edge StackEdge) {
	t.Helper()
	if strings.Contains(edge.Guidance, "tws new") {
		t.Errorf("guidance must not advertise a tws new shortcut for an entry that already exists: %q", edge.Guidance)
	}
	for _, want := range []string{"restore the branch", "remove and recreate the stack entry"} {
		if !strings.Contains(edge.Guidance, want) {
			t.Errorf("guidance %q must mention %q", edge.Guidance, want)
		}
	}
	found := false
	for _, span := range commandSpans(t, edge.Guidance) {
		if !strings.HasPrefix(span, "git branch ") {
			continue
		}
		found = true
		if strings.Contains(span, "…") {
			t.Errorf("restore command span was truncated: %q", span)
		}
		if !strings.Contains(span, edge.GitBranch) {
			t.Errorf("restore command span %q must name the full branch %q", span, edge.GitBranch)
		}
		if !strings.Contains(span, "<known-commit>") {
			t.Errorf("restore command span %q must show the commit placeholder", span)
		}
	}
	if !found {
		t.Errorf("expected a `git branch` restore example in %q", edge.Guidance)
	}
	assertSanitizedLine(t, edge.Guidance)
}

// TestStackAncestry_MissingChildRefLongBranchNotTruncated proves the restore
// example stays runnable for a branch name far past the display limit.
func TestStackAncestry_MissingChildRefLongBranchNotTruncated(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	longBranch := "feature/" + strings.Repeat("ghost-segment-", 6) + "end"

	edge := evalEdges(t, dir, StackEntry{Name: "ghost", Branch: longBranch, Base: "main"})[0]
	if edge.Reason != ReasonChildRefMissing {
		t.Fatalf("reason = %q, want child-ref-missing", edge.Reason)
	}
	assertChildRefMissingGuidance(t, edge)
}

// TestStackAncestry_MissingChildRefArchivedUnchanged pins that the archived
// wording is untouched and equally free of a creation shortcut.
func TestStackAncestry_MissingChildRefArchivedUnchanged(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)

	edge := evalEdges(t, dir, StackEntry{Name: "ghost", Base: "main", Archived: true})[0]
	if edge.Reason != ReasonChildRefMissing || edge.Severity != SeverityInfo {
		t.Fatalf("reason/severity = %q/%q, want child-ref-missing/info", edge.Reason, edge.Severity)
	}
	if edge.Guidance != "archived branch `ghost` has no git ref" {
		t.Errorf("archived guidance = %q", edge.Guidance)
	}
}

func TestStackAncestry_BranchTagCollision(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "sideways", "main", 1)
	gitInTest(t, dir, "tag", "dup", "sideways")
	forkChild(t, dir, "dup", "main", 1)
	branchSHA := sha(t, dir, "refs/heads/dup")
	tagSHA := sha(t, dir, "refs/tags/dup^{commit}")
	if branchSHA == tagSHA {
		t.Fatal("fixture must have branch and tag dup at different commits")
	}
	forkChild(t, dir, "kid", "refs/heads/dup", 1)

	edges := evalEdges(t, dir,
		StackEntry{Name: "dup", Base: "main"},
		StackEntry{Name: "kid", Base: "dup"},
	)
	dupEdge := edgeNamed(t, edges, "dup")
	kidEdge := edgeNamed(t, edges, "kid")

	if dupEdge.LocalHead != branchSHA {
		t.Errorf("child side resolved %q, want the branch %q", dupEdge.LocalHead, branchSHA)
	}
	if !dupEdge.RefExists {
		t.Error("branch dup exists")
	}
	if kidEdge.ParentHead != branchSHA {
		t.Errorf("parent side resolved %q, want the branch %q", kidEdge.ParentHead, branchSHA)
	}
	if kidEdge.Status != AncestryStatusCurrent {
		t.Errorf("kid status = %q, want current", kidEdge.Status)
	}

	_, ws := setupHealthTestRepo(t)
	_ = ws
	if strings.Contains(dupEdge.Guidance+kidEdge.Guidance, "ambiguous") {
		t.Error("ambiguity warnings must never leak into guidance")
	}
}

func TestStackAncestry_RenamedGitBranches(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "jd/core", "main", 1)
	forkChild(t, dir, "jd/api", "jd/core", 1)

	edges := evalEdgesWith(t, dir, StackAncestryOptions{BasePolicy: StackBasePolicyLiteralEntry},
		StackEntry{Name: "core", Branch: "jd/core", Base: "main"},
		StackEntry{Name: "api", Branch: "jd/api", Base: "core"},
	)
	api := edgeNamed(t, edges, "api")

	if api.BaseRef != "refs/heads/jd/core" {
		t.Errorf("base ref = %q, want refs/heads/jd/core", api.BaseRef)
	}
	if api.ChildRef != "refs/heads/jd/api" {
		t.Errorf("child ref = %q, want refs/heads/jd/api", api.ChildRef)
	}
	if api.BaseKind != StackBaseStackEntry {
		t.Errorf("base kind = %q, want stack-entry", api.BaseKind)
	}
	if api.Status != AncestryStatusCurrent {
		t.Errorf("status = %q, want current", api.Status)
	}
}

func TestStackAncestry_BaseUnset(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	gitInTest(t, dir, "branch", "loose", "main")

	edge := evalEdges(t, dir, StackEntry{Name: "loose", Base: ""})[0]
	if edge.Status != "" {
		t.Fatalf("status = %q, want the empty (not evaluated) status", edge.Status)
	}
	if edge.Reason != ReasonBaseUnset {
		t.Errorf("reason = %q, want base-unset", edge.Reason)
	}
	if edge.Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", edge.Severity)
	}
	if edge.RefProbed {
		t.Error("a base-unset edge must never be probed")
	}
	if edge.BaseKind != StackBaseNone {
		t.Errorf("base kind = %q, want none", edge.BaseKind)
	}

	addStackEntries(t, ws, "myfeat", []StackEntry{{Name: "loose", Base: ""}})
	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Issues != 0 {
		t.Errorf("issues = %d, want 0 for an informational entry", report.Issues)
	}
	if out := FormatCheckoutHealth(report); !strings.Contains(out, "ancestry=unevaluated") {
		t.Errorf("expected ancestry=unevaluated in:\n%s", out)
	}
	entries, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatal(err)
	}
	if out := FormatCheckoutList(ws, entries); !strings.Contains(out, "[unevaluated]") {
		t.Errorf("expected [unevaluated] tag in:\n%s", out)
	}
}

// ---------- AC 23-24: cross-repo ----------

func TestStackAncestry_CrossRepoNoGit(t *testing.T) {
	shim := withGitShim(t)
	dir, _ := setupHealthTestRepo(t)
	foreign := secondRepo(t)
	gitInTest(t, dir, "branch", "cross", "main")

	foreignRefsBefore := gitInTest(t, foreign, "rev-parse", "--all")
	foreignInfoBefore, err := os.Stat(filepath.Join(foreign, ".git"))
	if err != nil {
		t.Fatal(err)
	}

	shim.reset(t)
	edge := evalEdges(t, dir, StackEntry{Name: "cross", Base: "main", Repo: foreign})[0]
	probes := ancestryProbes(t, shim)

	if edge.Status != AncestryStatusCrossRepo || edge.Reason != ReasonCrossRepo {
		t.Fatalf("status/reason = %q/%q, want cross-repo-unsupported/cross-repo", edge.Status, edge.Reason)
	}
	if edge.Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", edge.Severity)
	}
	if edge.MergeBase != nil || edge.RefProbed {
		t.Error("a cross-repo edge is never probed and has no merge base")
	}
	if len(probes) != 0 {
		t.Errorf("expected zero ancestry probes, got %v", probes)
	}
	for _, inv := range shim.invocations(t) {
		if strings.Contains(strings.Join(inv.args, " "), foreign) || inv.dir == foreign {
			t.Errorf("no git process may name the foreign repository: %v", inv)
		}
	}

	if after := gitInTest(t, foreign, "rev-parse", "--all"); after != foreignRefsBefore {
		t.Error("foreign repository refs changed")
	}
	foreignInfoAfter, err := os.Stat(filepath.Join(foreign, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if !foreignInfoBefore.ModTime().Equal(foreignInfoAfter.ModTime()) {
		t.Error("foreign repository .git mtime changed")
	}
}

func TestStackAncestry_CrossRepoUncounted(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	foreign := secondRepo(t)
	gitInTest(t, dir, "branch", "cross", "main")
	gitInTest(t, dir, "branch", "plain", "main")

	opts := &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}}

	addStackEntries(t, ws, "myfeat", []StackEntry{{Name: "plain", Base: "main"}})
	without, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatal(err)
	}

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "plain", Base: "main"},
		{Name: "cross", Base: "main", Repo: foreign},
	})
	with, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatal(err)
	}

	if with.Issues != without.Issues {
		t.Errorf("issues = %d with cross-repo, %d without", with.Issues, without.Issues)
	}
	if with.HasErrors() {
		t.Error("ancestry must never produce an error severity")
	}
}

// ---------- AC 25-28: severity and counting ----------

func ancestrySeverityFixture(t *testing.T, archived bool) *CheckoutHealthReport {
	t.Helper()
	dir, ws := setupHealthTestRepo(t)
	recorded := sidewaysRewrite(t, dir)
	forkChild(t, dir, "stale-child", "main", 0)
	advance(t, dir, "main", 1)

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "ghost", Base: "main", Archived: archived},
		{Name: "stale-child", Base: "main", Archived: archived},
		{Name: "child", Base: "parent", LastBaseSHA: recorded, Archived: archived},
	})
	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestStackAncestry_ArchivedSeverityInfo(t *testing.T) {
	report := ancestrySeverityFixture(t, true)

	want := map[string]AncestryStatus{
		"ghost":       AncestryStatusMissing,
		"stale-child": AncestryStatusStale,
		"child":       AncestryStatusDivergent,
	}
	for _, f := range report.Features {
		if expected, ok := want[f.Name]; ok {
			if f.AncestryStatus != expected {
				t.Errorf("%s status = %q, want %q", f.Name, f.AncestryStatus, expected)
			}
			if f.Severity != SeverityInfo {
				t.Errorf("%s severity = %q, want info for an archived entry", f.Name, f.Severity)
			}
		}
	}
	if report.Issues != 0 {
		t.Errorf("issues = %d, want 0 — archived ancestry never inflates the count", report.Issues)
	}
	if report.HasErrors() {
		t.Error("archived ancestry must not be an error")
	}
}

func TestStackAncestry_ActiveSeverityWarning(t *testing.T) {
	report := ancestrySeverityFixture(t, false)

	warnings := 0
	for _, f := range report.Features {
		if f.Severity == SeverityWarning {
			warnings++
		}
	}
	if warnings != 3 {
		t.Fatalf("expected 3 warning entries, got %d", warnings)
	}
	if report.Issues != 3 {
		t.Errorf("issues = %d, want exactly one per warning edge", report.Issues)
	}
}

func TestStackAncestry_NeverError(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	recorded := sidewaysRewrite(t, dir)
	foreign := secondRepo(t)

	edges := evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded},
		StackEntry{Name: "ghost", Base: "main"},
		StackEntry{Name: "loose", Base: ""},
		StackEntry{Name: "cross", Base: "main", Repo: foreign},
		StackEntry{Name: "child", Base: "nowhere", Archived: true},
	)
	for _, e := range edges {
		if e.Severity == SeverityError {
			t.Errorf("edge %s has error severity", e.Name)
		}
		if e.Reason == "" {
			t.Errorf("edge %s has no reason", e.Name)
		}
	}
	for _, e := range UnevaluatedStackEdges("myfeat", Stack{Branches: []StackEntry{{Name: "a", Base: "main"}}}, ReasonRepoUnavailable, "no repo") {
		if e.Severity == SeverityError {
			t.Error("unevaluated edges must never be errors")
		}
	}
	assertNoSourceMatch(t, "stack_ancestry.go", `SeverityError`)
}

// ---------- AC 29-30, 34, 52: repository plumbing ----------

func TestStackAncestry_EmptyRepoDirRefused(t *testing.T) {
	shim := withGitShim(t)
	shim.reset(t)

	edges, err := EvaluateStackAncestry("", "myfeat", Stack{Branches: []StackEntry{{Name: "a", Base: "main"}}}, StackAncestryOptions{})
	if err == nil {
		t.Fatal("expected an error for an empty repository directory")
	}
	if !isRepoUnavailable(err) {
		t.Errorf("error %v is not ErrRepoUnavailable", err)
	}
	if edges != nil {
		t.Errorf("edges = %v, want nil", edges)
	}
	if got := shim.invocations(t); len(got) != 0 {
		t.Errorf("expected zero git processes, got %v", got)
	}
}

func TestStackAncestry_NonRepoDirRefused(t *testing.T) {
	shim := withGitShim(t)
	plain := t.TempDir()
	shim.reset(t)

	edges, err := EvaluateStackAncestry(plain, "myfeat", Stack{Branches: []StackEntry{{Name: "a", Base: "main"}}}, StackAncestryOptions{})
	if err == nil {
		t.Fatal("expected an error for a non-repository directory")
	}
	if !isRepoUnavailable(err) {
		t.Errorf("error %v is not ErrRepoUnavailable", err)
	}
	if edges != nil {
		t.Errorf("edges = %v, want nil", edges)
	}
	invocations := shim.invocations(t)
	if len(invocations) > 1 {
		t.Errorf("expected at most one validation probe, got %v", invocations)
	}
	for _, inv := range invocations {
		if !isNonAncestryShape(inv.args) {
			t.Errorf("unexpected ancestry probe %v", inv)
		}
	}
}

func TestStackAncestry_RepoSourceStamped(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	gitInTest(t, dir, "branch", "child", "main")
	addFeatureToRepo(t, ws, "myfeat", "child", "main")
	fp := ws.FeaturePath("myfeat")
	stack, err := LoadStack(fp)
	if err != nil {
		t.Fatal(err)
	}

	edges, res := FeatureStackEdges(ws, Config{}, "myfeat", fp, stack)
	if res.Source != StackRepoWorkspace {
		t.Errorf("checkout source = %q, want workspace", res.Source)
	}
	if res.Alternate != "" {
		t.Errorf("checkout mode must never compute an alternate, got %q", res.Alternate)
	}
	for _, e := range edges {
		if e.RepoSource != StackRepoWorkspace {
			t.Errorf("edge %s repo source = %q, want workspace", e.Name, e.RepoSource)
		}
	}
	assertCanonicalMainRoot(t, res.RepoDir)
}

func TestStackAncestry_CanonicalRepoRoots(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	gitInTest(t, dir, "branch", "child", "main")
	addFeatureToRepo(t, ws, "myfeat", "child", "main")
	fp := ws.FeaturePath("myfeat")
	stack, err := LoadStack(fp)
	if err != nil {
		t.Fatal(err)
	}

	res := ResolveStackAncestryRepo(ws, Config{}, fp, stack)
	assertCanonicalMainRoot(t, res.RepoDir)
	if res.Alternate != "" {
		assertCanonicalMainRoot(t, res.Alternate)
	}
}

func assertCanonicalMainRoot(t *testing.T, root string) {
	t.Helper()
	if root == "" {
		t.Fatal("expected a resolved repository root")
	}
	main, err := MainRepoRootIn(root)
	if err != nil {
		t.Fatalf("MainRepoRootIn(%q): %v", root, err)
	}
	if canonicalize(main) != root {
		t.Errorf("root %q is not a canonical main repository root (%q)", root, canonicalize(main))
	}
}

func isRepoUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrRepoUnavailable.Error())
}

func TestStackAncestry_UnsafeWorktreeNameFailsClosed(t *testing.T) {
	shim := withGitShim(t)
	featurePath := t.TempDir()
	ws := Workspace{Mode: ModeExternal}
	stack := Stack{Branches: []StackEntry{{Name: "../foreign", Base: "main"}}}

	shim.reset(t)
	edges, res := FeatureStackEdges(ws, Config{}, "myfeat", featurePath, stack)

	if res.RepoDir != "" || res.Source != StackRepoUnavailable {
		t.Fatalf("resolution = %+v, want unavailable", res)
	}
	if len(shim.invocations(t)) != 0 {
		t.Errorf("an unsafe entry name must never reach git: %v", shim.invocations(t))
	}
	if len(edges) != 1 || edges[0].Reason != ReasonRepoUnavailable {
		t.Fatalf("edges = %+v, want one repo-unavailable edge", edges)
	}
	if edges[0].RefProbed {
		t.Error("an unevaluated edge is never probed")
	}
}

// ---------- AC 35-37: base identity notes ----------

func TestStackAncestry_RemoteIdentityNote(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitInTest(t, dir, "init", "--bare", remote)
	gitInTest(t, dir, "remote", "add", "origin", remote)
	gitInTest(t, dir, "push", "-u", "origin", "main")
	gitInTest(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	// Advance origin/main behind tws's back so local main is behind it.
	forkChild(t, dir, "ahead", "main", 1)
	gitInTest(t, dir, "push", "origin", "ahead:main")
	gitInTest(t, dir, "fetch", "origin")

	forkChild(t, dir, "child", "main", 1)

	edge := evalEdgesWith(t, dir, StackAncestryOptions{BasePolicy: StackBasePolicyRemoteDefault},
		StackEntry{Name: "child", Base: "main"})[0]
	found := false
	for _, note := range edge.Notes {
		if note.Kind == NoteBaseIdentityRemoteMismatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a base-identity-remote-mismatch note, got %+v", edge.Notes)
	}
	if edge.ParentHead != sha(t, dir, "refs/heads/main") {
		t.Error("classification must still derive from the literal local base")
	}
	if edge.Status != AncestryStatusCurrent {
		t.Errorf("status = %q, want current", edge.Status)
	}
	if edge.Severity != SeverityOK {
		t.Errorf("severity = %q, notes must not change severity", edge.Severity)
	}
}

func TestStackAncestry_LiteralIdentityNote(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "jd/core", "main", 1)
	forkChild(t, dir, "core", "main", 2)
	forkChild(t, dir, "jd/api", "jd/core", 1)

	edges := evalEdgesWith(t, dir, StackAncestryOptions{BasePolicy: StackBasePolicyLiteralEntry},
		StackEntry{Name: "core", Branch: "jd/core", Base: "main"},
		StackEntry{Name: "api", Branch: "jd/api", Base: "core"},
	)
	api := edgeNamed(t, edges, "api")

	found := false
	for _, note := range api.Notes {
		if note.Kind == NoteBaseIdentityLiteralMismatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a base-identity-literal-mismatch note, got %+v", api.Notes)
	}
	if api.BaseRef != "refs/heads/jd/core" || api.ParentHead != sha(t, dir, "refs/heads/jd/core") {
		t.Error("classification must still use the parent entry's git branch")
	}
}

func TestStackAncestry_NoSpuriousNotes(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	forkChild(t, dir, "child", "parent", 1)

	for _, e := range evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent"},
	) {
		if len(e.Notes) != 0 {
			t.Errorf("edge %s emitted unexpected notes %+v", e.Name, e.Notes)
		}
	}
}

func TestStackAncestry_RepoSourceMismatchIsFeatureLevel(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	forkChild(t, dir, "jd/core", "main", 1)
	forkChild(t, dir, "core", "main", 2)
	forkChild(t, dir, "jd/api", "jd/core", 1)

	edges := evalEdges(t, dir,
		StackEntry{Name: "core", Branch: "jd/core", Base: "main"},
		StackEntry{Name: "api", Branch: "jd/api", Base: "core"},
	)
	for _, e := range edges {
		for _, note := range e.Notes {
			if note.Kind != NoteBaseIdentityRemoteMismatch && note.Kind != NoteBaseIdentityLiteralMismatch {
				t.Errorf("unexpected note kind %q", note.Kind)
			}
		}
	}

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "core", Branch: "jd/core", Base: "main"},
		{Name: "api", Branch: "jd/api", Base: "core"},
	})
	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	listEntries, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatal(err)
	}
	combined := FormatCheckoutHealth(report) + FormatCheckoutList(ws, listEntries)
	if strings.Contains(combined, RepoSourceMismatchLabel) {
		t.Errorf("checkout output must never contain %q:\n%s", RepoSourceMismatchLabel, combined)
	}

	if got := countSourceMatches(t, "stack_ancestry.go", `StackNoteKind = "`); got != 2 {
		t.Errorf("StackNoteKind constant count = %d, want 2", got)
	}
	assertNoSourceMatch(t, "checkout_health.go", RepoSourceMismatchLabel)
	assertNoSourceMatch(t, "checkout_health.go", "RepoSourceMismatchLabel")
	if got := countSourceMatches(t, "health.go", "RepoSourceMismatchLabel"); got != 1 {
		t.Errorf("health.go uses RepoSourceMismatchLabel %d times, want 1", got)
	}
	literal := 0
	for _, file := range nonTestGoFiles(t) {
		literal += countSourceMatches(t, file, RepoSourceMismatchLabel)
	}
	if literal != 1 {
		t.Errorf("the %q literal appears %d times, want only its declaration", RepoSourceMismatchLabel, literal)
	}
}

// ---------- AC 38-40, 56: doctor/list agreement, output, determinism ----------

func ancestryCorpusWorkspace(t *testing.T) (string, Workspace) {
	t.Helper()
	dir, ws := setupHealthTestRepo(t)
	recorded := sidewaysRewrite(t, dir)
	foreign := secondRepo(t)
	forkChild(t, dir, "fresh", "main", 1)

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "fresh", Base: "main"},
		{Name: "parent", Base: "main"},
		{Name: "child", Base: "parent", LastBaseSHA: recorded},
		{Name: "ghost", Base: "main"},
		{Name: "cross", Base: "main", Repo: foreign},
		{Name: "gone", Base: "main", Archived: true},
	})
	return dir, ws
}

func TestAncestryDoctorListAgree(t *testing.T) {
	_, ws := ancestryCorpusWorkspace(t)

	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	listEntries, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Features) != len(listEntries) {
		t.Fatalf("doctor has %d entries, list has %d", len(report.Features), len(listEntries))
	}
	for i := range report.Features {
		if string(report.Features[i].AncestryStatus) != listEntries[i].AncestryStatus {
			t.Errorf("entry %d (%s): doctor %q, list %q", i, report.Features[i].Name,
				report.Features[i].AncestryStatus, listEntries[i].AncestryStatus)
		}
	}
}

func TestCheckoutHealth_Determinism(t *testing.T) {
	_, ws := ancestryCorpusWorkspace(t)
	opts := &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}}

	first, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatal(err)
	}
	if FormatCheckoutHealth(first) != FormatCheckoutHealth(second) {
		t.Error("doctor output is not deterministic")
	}

	l1, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatal(err)
	}
	if FormatCheckoutList(ws, l1) != FormatCheckoutList(ws, l2) {
		t.Error("list output is not deterministic")
	}
}

func TestCheckoutHealth_AncestryDetailLines(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	root := sha(t, dir, "main")
	forkChild(t, dir, "child", "main", 1)
	recorded := root
	forkChild(t, dir, "sideways", "HEAD~1", 1)
	gitInTest(t, dir, "checkout", "child")
	gitInTest(t, dir, "branch", "-f", "main", "sideways")
	forkChild(t, dir, "fresh", "child", 0)

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "child", Base: "main", LastBaseSHA: recorded},
		{Name: "fresh", Base: "child"},
	})

	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	out := FormatCheckoutHealth(report)

	if !strings.Contains(out, "base=main ancestry=divergent") {
		t.Fatalf("expected the entry line tokens in:\n%s", out)
	}
	if !strings.Contains(out, "      reason: base-rewritten") {
		t.Errorf("expected an indented reason line in:\n%s", out)
	}
	if !strings.Contains(out, "git rebase --onto") {
		t.Errorf("expected the guidance line in:\n%s", out)
	}
	// The repair command must stay complete and name the child branch, even
	// though the surrounding prose is abbreviated.
	childEdge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "child", Base: "main", LastBaseSHA: recorded},
		StackEntry{Name: "fresh", Base: "child"},
	), "child")
	wantCmd := fmt.Sprintf("`git rebase --onto %s %s %s`", childEdge.BaseRef, childEdge.LastBaseCommit, childEdge.GitBranch)
	if !strings.Contains(out, wantCmd) {
		t.Errorf("expected the complete repair command %s in:\n%s", wantCmd, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ancestry=") && strings.Contains(line, "git rebase --onto") {
			t.Errorf("guidance must never be inline on the entry line: %q", line)
		}
	}

	freshLines := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "myfeat/fresh") {
			freshLines++
		}
	}
	if freshLines != 1 {
		t.Errorf("a current entry with no notes must produce exactly one line, got %d", freshLines)
	}
}

func TestCheckoutHealth_EdgeToEntryMapping(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	forkChild(t, dir, "jd-main", "main", 1)
	forkChild(t, dir, "kid", "jd-main", 1)
	advance(t, dir, "jd-main", 1)
	gitInTest(t, dir, "tag", "v1", "main")

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "main", Branch: "jd-main", Base: "v1"},
		{Name: "kid", Base: "main"},
	})

	cfg := LoadConfig()
	fp := ws.FeaturePath("myfeat")
	stack, err := LoadStack(fp)
	if err != nil {
		t.Fatal(err)
	}
	edges, _ := FeatureStackEdges(ws, cfg, "myfeat", fp, stack)

	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Features) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(report.Features))
	}

	hexRe := regexp.MustCompile(`[0-9a-f]{40}`)
	for i, entry := range report.Features {
		edge := edges[i]
		if entry.LocalHead != edge.LocalHeadShort || entry.ParentHead != edge.ParentHeadShort {
			t.Errorf("%s: heads must be the abbreviated forms", entry.Name)
		}
		if entry.LocalHeadFull != edge.LocalHead || entry.ParentHeadFull != edge.ParentHead {
			t.Errorf("%s: full heads must come from the edge", entry.Name)
		}
		if entry.LocalHead != "" && len(entry.LocalHead) >= 40 {
			t.Errorf("%s: local head %q is not abbreviated", entry.Name, entry.LocalHead)
		}
		if entry.LocalHeadFull != "" && !hexRe.MatchString(entry.LocalHeadFull) {
			t.Errorf("%s: full local head %q is not 40 hex", entry.Name, entry.LocalHeadFull)
		}
	}

	main := report.Features[0]
	if main.BaseGitBranch != "v1" || main.BaseName != "v1" || main.BaseRef != "v1" {
		t.Errorf("literal base mapping wrong: %+v", main)
	}
	kid := report.Features[1]
	if kid.BaseGitBranch != "jd-main" {
		t.Errorf("BaseGitBranch = %q, want the bare jd-main", kid.BaseGitBranch)
	}
	if kid.BaseRef != "refs/heads/jd-main" {
		t.Errorf("BaseRef = %q, want refs/heads/jd-main", kid.BaseRef)
	}
	if kid.MergeBase == nil || kid.MergeBaseShort == "" {
		t.Fatal("expected a merge base and its abbreviation")
	}
	if !hexRe.MatchString(*kid.MergeBase) {
		t.Errorf("merge base %q is not a full SHA", *kid.MergeBase)
	}

	out := FormatCheckoutHealth(report)
	if !strings.Contains(out, "head="+kid.LocalHead) || !strings.Contains(out, "parent="+kid.ParentHead) {
		t.Errorf("expected abbreviated head tokens in:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "myfeat/") || strings.HasPrefix(line, "      ") {
			// Backticked command spans are exempt: a repair command must name
			// a complete object, and truncating it there would be the bug.
			if hexRe.MatchString(stripCommandSpans(line)) {
				t.Errorf("no full SHA may be rendered outside a command: %q", line)
			}
		}
	}
}

// stripCommandSpans removes every backticked span from a rendered line.
func stripCommandSpans(line string) string {
	parts := strings.Split(line, "`")
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 0 {
			b.WriteString(part)
		}
	}
	return b.String()
}

// ---------- AC 41: read-only ----------

func TestStackAncestry_ReadOnly(t *testing.T) {
	shim := withGitShim(t)
	dir, ws := setupHealthTestRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitInTest(t, dir, "init", "--bare", remote)
	gitInTest(t, dir, "remote", "add", "origin", remote)
	gitInTest(t, dir, "push", "-u", "origin", "main")

	recorded := sidewaysRewrite(t, dir)
	// Move a remote branch behind tws's back.
	gitInTest(t, dir, "push", "origin", "sideways:main", "--force")
	gitInTest(t, dir, "fetch", "origin")

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "parent", Base: "main"},
		{Name: "child", Base: "parent", LastBaseSHA: recorded},
		{Name: "ghost", Base: "main"},
	})

	snapshot := func() string {
		var b strings.Builder
		b.WriteString(gitInTest(t, dir, "rev-parse", "--all"))
		b.WriteString(gitInTest(t, dir, "reflog", "--all"))
		b.WriteString(gitInTest(t, dir, "for-each-ref", "refs/remotes"))
		if _, err := os.Stat(filepath.Join(dir, ".git", "FETCH_HEAD")); err == nil {
			b.WriteString("FETCH_HEAD present\n")
		}
		b.WriteString(treeSnapshot(t, dir))
		b.WriteString(treeSnapshot(t, ws.MetadataRoot))
		data, err := os.ReadFile(StackPath(ws.FeaturePath("myfeat")))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		return b.String()
	}

	before := snapshot()
	shim.reset(t)

	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	_ = FormatCheckoutHealth(report)
	listEntries, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatal(err)
	}
	_ = FormatCheckoutList(ws, listEntries)

	invocations := shim.invocations(t)
	repoDir := canonicalize(dir)
	forbidden := []string{"fetch", "ls-remote", "--fork-point", "push", "update-ref", "reset", "checkout", "rebase", "gc", "status"}
	sawAncestry := false
	for _, inv := range invocations {
		if isNonAncestryShape(inv.args) {
			continue
		}
		sawAncestry = true
		joined := strings.Join(inv.args, " ")
		for _, verb := range forbidden {
			for _, arg := range inv.args {
				if arg == verb {
					t.Errorf("ancestry probe used forbidden verb %q: %v", verb, inv.args)
				}
			}
		}
		for _, arg := range inv.args {
			if arg == "-C" {
				t.Errorf("ancestry probe must not pass -C: %v", inv.args)
			}
		}
		if inv.dir != repoDir {
			t.Errorf("ancestry probe ran in %q, want %q (argv %v)", inv.dir, repoDir, inv.args)
		}
		if strings.Contains(joined, "^{commit}") && !strings.Contains(joined, "--end-of-options") {
			t.Errorf("ref-taking probe missing --end-of-options: %v", inv.args)
		}
		if len(inv.args) > 0 && inv.args[0] == "merge-base" {
			for _, arg := range inv.args[1:] {
				if arg == "--is-ancestor" {
					continue
				}
				if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(arg) {
					t.Errorf("merge-base received a non-SHA argument %q: %v", arg, inv.args)
				}
			}
		}
		if len(inv.args) >= 3 && inv.args[0] == "rev-parse" && inv.args[1] == "--verify" {
			if inv.args[2] != "--quiet" {
				t.Errorf("unexpected rev-parse --verify shape: %v", inv.args)
			}
		}
	}
	if !sawAncestry {
		t.Fatal("expected at least one ancestry probe in the measured window")
	}

	if after := snapshot(); after != before {
		t.Error("repository, metadata, or stack state changed during read-only evaluation")
	}
}

func treeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		fmt.Fprintf(&b, "%s|%v|%d\n", path, info.IsDir(), info.Size())
		return nil
	})
	return b.String()
}

// ---------- AC 42-43: exit semantics and process bounds ----------

func TestStackAncestry_ExitSemantics(t *testing.T) {
	dir, ws := ancestryCorpusWorkspace(t)
	_ = dir

	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.HasErrors() {
		t.Error("stale, divergent, missing, and cross-repo edges must all exit 0")
	}
}

func TestStackAncestry_ProcessBound(t *testing.T) {
	shim := withGitShim(t)
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "shared", "main", 1)
	for _, name := range []string{"c1", "c2", "c3", "c4"} {
		forkChild(t, dir, name, "shared", 1)
	}
	advance(t, dir, "shared", 1)

	entries := []StackEntry{{Name: "shared", Base: "main"}}
	for _, name := range []string{"c1", "c2", "c3", "c4"} {
		entries = append(entries, StackEntry{Name: name, Base: "shared"})
	}

	shim.reset(t)
	if _, err := EvaluateStackAncestry(dir, "myfeat", Stack{Branches: entries}, StackAncestryOptions{}); err != nil {
		t.Fatal(err)
	}
	probes := ancestryProbes(t, shim)

	if len(probes) > 10*len(entries) {
		t.Errorf("ancestry probes = %d, want at most %d", len(probes), 10*len(entries))
	}
	if got := countProbesWithArg(probes, "refs/heads/shared^{commit}"); got != 1 {
		t.Errorf("shared parent ref resolved %d times, want 1 (positive caching)", got)
	}
	shortCounts := map[string]int{}
	for _, p := range probes {
		if len(p.args) == 3 && p.args[0] == "rev-parse" && p.args[1] == "--short" {
			shortCounts[p.args[2]]++
		}
	}
	for full, n := range shortCounts {
		if n != 1 {
			t.Errorf("abbreviation of %s ran %d times, want 1", full, n)
		}
	}

	// Negative caching.
	ghost := []StackEntry{}
	for _, name := range []string{"shared", "c1", "c2", "c3", "c4"} {
		ghost = append(ghost, StackEntry{Name: name, Base: "ghost-base"})
	}
	shim.reset(t)
	if _, err := EvaluateStackAncestry(dir, "myfeat", Stack{Branches: ghost}, StackAncestryOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := countProbesWithArg(ancestryProbes(t, shim), "ghost-base^{commit}"); got != 1 {
		t.Errorf("unresolvable shared base probed %d times, want 1 (negative caching)", got)
	}

	// Zero ancestry probes for cross-repo and base-less stacks.
	foreign := secondRepo(t)
	crossEntries := []StackEntry{}
	looseEntries := []StackEntry{}
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("x%d", i)
		crossEntries = append(crossEntries, StackEntry{Name: name, Base: "main", Repo: foreign})
		looseEntries = append(looseEntries, StackEntry{Name: name, Base: ""})
	}
	for _, stack := range []Stack{{Branches: crossEntries}, {Branches: looseEntries}} {
		shim.reset(t)
		if _, err := EvaluateStackAncestry(dir, "myfeat", stack, StackAncestryOptions{}); err != nil {
			t.Fatal(err)
		}
		if got := ancestryProbes(t, shim); len(got) != 0 {
			t.Errorf("expected zero ancestry probes, got %v", got)
		}
	}
}

func countProbesWithArg(probes []gitInvocation, want string) int {
	n := 0
	for _, p := range probes {
		for _, arg := range p.args {
			if arg == want {
				n++
			}
		}
	}
	return n
}

// ---------- AC 46-48, 51, 53: structural greps ----------

func readSourceFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

func countSourceMatches(t *testing.T, rel, pattern string) int {
	t.Helper()
	re := regexp.MustCompile(pattern)
	return len(re.FindAllString(readSourceFile(t, rel), -1))
}

func assertNoSourceMatch(t *testing.T, rel, pattern string) {
	t.Helper()
	if got := countSourceMatches(t, rel, pattern); got != 0 {
		t.Errorf("%s: pattern %q matched %d times, want none", rel, pattern, got)
	}
}

func nonTestGoFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestStackAncestry_DeletedHelpersAreGone(t *testing.T) {
	deleted := regexp.MustCompile(`(^|[^\w])(gitMergeBase|gitFullSHA|gitShortSHA)([^\w]|$)`)
	prefixed := regexp.MustCompile(`(^|[^\w])gitMergeBase\w+`)
	refExists := regexp.MustCompile(`gitRefExists`)

	var refExistsFiles []string
	for _, file := range nonTestGoFiles(t) {
		body := readSourceFile(t, file)
		if deleted.MatchString(body) {
			t.Errorf("%s still references a deleted helper", file)
		}
		if prefixed.MatchString(body) {
			t.Errorf("%s introduces a symbol under a deleted helper's prefix", file)
		}
		if refExists.MatchString(body) {
			refExistsFiles = append(refExistsFiles, file)
		}
	}
	for _, file := range refExistsFiles {
		if file != "agent_status.go" && file != "checkout_health.go" {
			t.Errorf("unexpected gitRefExists caller in %s", file)
		}
	}
}

func TestStackAncestry_DisplayTokenCentralized(t *testing.T) {
	tokenHits := 0
	callSites := 0
	for _, file := range nonTestGoFiles(t) {
		body := readSourceFile(t, file)
		tokenHits += len(regexp.MustCompile(`"unevaluated"`).FindAllString(body, -1))
		callSites += len(regexp.MustCompile(`ancestryDisplayStatus\(`).FindAllString(body, -1))
		if strings.Contains(body, "evaluation-unavailable") {
			t.Errorf("%s uses the rejected evaluation-unavailable wording", file)
		}
	}
	if tokenHits != 1 {
		t.Errorf(`the "unevaluated" literal appears %d times, want 1`, tokenHits)
	}
	// One declaration plus exactly three call sites.
	if callSites != 4 {
		t.Errorf("ancestryDisplayStatus appears %d times, want 1 declaration + 3 call sites", callSites)
	}

	statusConsts := countSourceMatches(t, "checkout_health.go", `(?m)^\s*AncestryStatus\w*\s+AncestryStatus\s*=`)
	if statusConsts != 5 {
		t.Errorf("AncestryStatus constant count = %d, want exactly 5", statusConsts)
	}
}

func TestStackAncestry_NoForbiddenMachinery(t *testing.T) {
	for _, pattern := range []string{`rev-list`, `--count`, `--left-right`, `patch-id`, `f` + `etch`, `LoadConfig`} {
		assertNoSourceMatch(t, "stack_ancestry.go", pattern)
	}
}

func TestCheckoutBuilders_LoadConfigPlacement(t *testing.T) {
	body := readSourceFile(t, "checkout_health.go")
	if got := countSourceMatches(t, "checkout_health.go", `LoadConfig\(\)`); got != 2 {
		t.Errorf("checkout_health.go has %d LoadConfig calls, want exactly 2", got)
	}
	for _, fn := range []string{"buildFeatureEntries", "buildCheckoutListEntries", "buildOneFeatureEntry"} {
		if strings.Contains(functionBody(t, body, fn), "LoadConfig") {
			t.Errorf("%s must not load config", fn)
		}
	}
	doctor := readSourceFile(t, filepath.Join("cli", "doctor.go"))
	if got := len(regexp.MustCompile(`LoadConfig\(\)`).FindAllString(doctor, -1)); got != 1 {
		t.Errorf("cli/doctor.go has %d LoadConfig calls, want exactly 1", got)
	}
	if strings.Contains(functionBody(t, doctor, "checkFeatureE"), "LoadConfig") {
		t.Error("checkFeatureE must not load config")
	}
}

// functionBody returns the source of a top-level function by name.
func functionBody(t *testing.T, source, name string) string {
	t.Helper()
	idx := strings.Index(source, "func "+name+"(")
	if idx < 0 {
		t.Fatalf("function %s not found", name)
	}
	rest := source[idx:]
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// ---------- AC 54, 57: sanitization and guidance ----------

func TestStackAncestry_RecordedBaseSanitized(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	forkChild(t, dir, "child", "parent", 1)
	advance(t, dir, "parent", 1)

	hostile := "abc\ndef\tghi\x1b[31m" + strings.Repeat("z", 200)
	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: hostile},
	), "child")

	if edge.Reason != ReasonBaseRecordUnresolvable {
		t.Fatalf("reason = %q, want base-record-unresolvable", edge.Reason)
	}
	assertSanitizedLine(t, edge.Guidance)
	if !strings.Contains(edge.Guidance, `"`) {
		t.Error("an unresolvable recorded base must be rendered quoted")
	}
	if strings.Contains(edge.Guidance, strings.Repeat("z", 60)) {
		t.Error("recorded content must be truncated")
	}

	// The same rule for a hostile base and repo string.
	hostileEdges := evalEdges(t, dir,
		StackEntry{Name: "child", Base: hostile},
		StackEntry{Name: "other", Base: "main", Repo: "/tmp/\x1bevil\nrepo"},
	)
	for _, e := range hostileEdges {
		assertSanitizedLine(t, e.Guidance)
	}

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "parent", Base: "main"},
		{Name: "child", Base: "parent", LastBaseSHA: hostile},
	})
	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(FormatCheckoutHealth(report), "\x1b") {
		t.Error("formatted output must contain no raw escape sequence")
	}
}

func assertSanitizedLine(t *testing.T, guidance string) {
	t.Helper()
	if strings.ContainsAny(guidance, "\n\r\t\x1b") {
		t.Errorf("guidance %q contains a control character", guidance)
	}
}

func TestStackAncestry_ProbeFailedGuidanceDistinct(t *testing.T) {
	base := StackEdge{
		Feature:    "auth",
		Name:       "api",
		GitBranch:  "jd-api",
		BaseRef:    "refs/heads/jd-main",
		BaseName:   "main",
		BaseRecord: StackBaseRecordAbsent,
	}
	unresolvable := base
	unresolvable.Reason = ReasonBaseRecordUnresolvable
	unresolvable.BaseRecord = StackBaseRecordUnresolvable
	unresolvable.LastBaseSHA = "deadbeef"

	absent := base
	absent.Reason = ReasonParentAdvancedNoBaseRecord

	failed := base
	failed.Reason = ReasonAncestryProbeFailed

	gUnresolvable := ancestryGuidance(unresolvable, "")
	gAbsent := ancestryGuidance(absent, "")
	gFailed := ancestryGuidance(failed, "exit status 128")

	if gUnresolvable == gAbsent || gAbsent == gFailed || gUnresolvable == gFailed {
		t.Error("the three unusable-base-record reasons must produce three different strings")
	}
	if !strings.Contains(gFailed, "ancestry probe failed") || !strings.Contains(gFailed, "re-run: tws doctor") {
		t.Errorf("probe-failed guidance is wrong: %q", gFailed)
	}
	if strings.Contains(gFailed, "no recorded base commit") || strings.Contains(gFailed, "is not present in this repository") {
		t.Errorf("probe-failed guidance must make no claim about the recorded base: %q", gFailed)
	}

	allReasons := []StackAncestryReason{
		ReasonParentContained, ReasonParentAdvanced, ReasonParentAdvancedNoBaseRecord,
		ReasonBaseRecordUnresolvable, ReasonBaseRewritten, ReasonUnrelatedHistories,
		ReasonChildRefMissing, ReasonBaseRefMissing, ReasonCrossRepo, ReasonBaseUnset,
		ReasonRepoUnavailable, ReasonAncestryProbeFailed,
	}
	for _, reason := range allReasons {
		e := base
		e.Reason = reason
		e.Repo = "/other\nrepo"
		assertSanitizedLine(t, ancestryGuidance(e, "detail\nwith\nnewlines"))
	}
}

func TestStackAncestry_DisplayStatus(t *testing.T) {
	if ancestryDisplayStatus("") != ancestryUnevaluatedToken {
		t.Error("the empty status must render as the unevaluated token")
	}
	if ancestryDisplayStatus(AncestryStatusStale) != "stale" {
		t.Error("a known status renders verbatim")
	}
}

func TestStackAncestry_UnevaluatedEdgesKeepMeaning(t *testing.T) {
	stack := Stack{Branches: []StackEntry{
		{Name: "cross", Base: "main", Repo: "/foreign"},
		{Name: "loose", Base: ""},
		{Name: "plain", Base: "main"},
	}}
	edges := UnevaluatedStackEdges("myfeat", stack, ReasonRepoUnavailable, "no repository")
	if len(edges) != 3 {
		t.Fatalf("expected 3 edges, got %d", len(edges))
	}
	if edges[0].Status != AncestryStatusCrossRepo || edges[0].Reason != ReasonCrossRepo {
		t.Error("a cross-repo entry keeps its meaning when the repository is unavailable")
	}
	if edges[1].Reason != ReasonBaseUnset {
		t.Error("a base-less entry keeps its meaning when the repository is unavailable")
	}
	if edges[2].Reason != ReasonRepoUnavailable || edges[2].Status != "" {
		t.Error("a normal entry becomes unevaluated")
	}
	for _, e := range edges {
		if e.RepoSource != StackRepoUnavailable || e.RefProbed || e.MergeBase != nil {
			t.Errorf("edge %s violates the unevaluated contract: %+v", e.Name, e)
		}
		if e.Severity != SeverityInfo {
			t.Errorf("edge %s severity = %q, want info", e.Name, e.Severity)
		}
	}
}

func TestStackAncestry_StackBaseRefSelection(t *testing.T) {
	stack := Stack{Branches: []StackEntry{
		{Name: "core", Branch: "jd/core", Base: "main"},
		{Name: "api", Base: "core"},
	}}
	ref, kind := stackBaseRef(stack, stack.Branches[1])
	if ref != "refs/heads/jd/core" || kind != StackBaseStackEntry {
		t.Errorf("stack-entry base = %q/%q", ref, kind)
	}
	ref, kind = stackBaseRef(stack, stack.Branches[0])
	if ref != "main" || kind != StackBaseLiteralRef {
		t.Errorf("literal base = %q/%q", ref, kind)
	}
	ref, kind = stackBaseRef(stack, StackEntry{Name: "x"})
	if ref != "" || kind != StackBaseNone {
		t.Errorf("unset base = %q/%q", ref, kind)
	}
}

func TestStackAncestry_EvaluateStackEdge(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "child", "main", 1)
	stack := Stack{Branches: []StackEntry{{Name: "child", Base: "main"}}}

	edge, err := EvaluateStackEdge(dir, "myfeat", stack.Branches[0], stack, StackAncestryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if edge.Status != AncestryStatusCurrent {
		t.Errorf("status = %q, want current", edge.Status)
	}
	if _, err := EvaluateStackEdge("", "myfeat", stack.Branches[0], stack, StackAncestryOptions{}); err == nil {
		t.Error("expected the same validation on the single-edge form")
	}
}

func TestHealthIssue_ZeroValueSeverity(t *testing.T) {
	legacy := HealthIssue{Branch: "b", Problem: "p", Hint: "h"}
	if legacy.EffectiveSeverity() != SeverityWarning {
		t.Errorf("zero severity = %q, want warning", legacy.EffectiveSeverity())
	}
	if !strings.HasPrefix(legacy.String(), "  [!] b: p") {
		t.Errorf("legacy rendering changed: %q", legacy.String())
	}
	info := HealthIssue{Branch: "b", Problem: "p", Severity: SeverityInfo}
	if !strings.HasPrefix(info.String(), "  [i] b: p") {
		t.Errorf("info rendering wrong: %q", info.String())
	}
	issues := []HealthIssue{legacy, info, {Branch: "c", Problem: "q", Severity: SeverityWarning}}
	if got := CountHealthIssues(issues); got != 2 {
		t.Errorf("counted %d issues, want 2 (info never counts)", got)
	}
}

func TestAncestryHealthIssues_Mapping(t *testing.T) {
	edges := []StackEdge{
		{Name: "ok", Status: AncestryStatusCurrent, Reason: ReasonParentContained, Severity: SeverityOK},
		{Name: "a", Status: AncestryStatusStale, Reason: ReasonParentAdvanced, Severity: SeverityWarning, Guidance: "run sync"},
		{Name: "b", Reason: ReasonRepoUnavailable, Severity: SeverityInfo},
		{Name: "c", Reason: ReasonRepoUnavailable, Severity: SeverityInfo},
	}
	issues := AncestryHealthIssues(StackRepoResolution{RepoDir: "/repo", Source: StackRepoWorktree, Alternate: "/other"}, edges)

	if len(issues) != 3 {
		t.Fatalf("expected 3 issues (one warning, one collapsed repo-unavailable, one mismatch), got %d: %+v", len(issues), issues)
	}
	if issues[0].Branch != "a" || issues[0].Problem != "ancestry stale: parent-advanced" {
		t.Errorf("unexpected first issue: %+v", issues[0])
	}
	if issues[1].Branch != "stack" || !strings.Contains(issues[1].Problem, "ancestry unevaluated: repo-unavailable") {
		t.Errorf("unexpected collapsed issue: %+v", issues[1])
	}
	if issues[2].Branch != "stack" || !strings.HasPrefix(issues[2].Problem, RepoSourceMismatchLabel) {
		t.Errorf("unexpected mismatch issue: %+v", issues[2])
	}
	if issues[2].Severity != SeverityInfo {
		t.Errorf("mismatch issue severity = %q, want info", issues[2].Severity)
	}
	if got := CountHealthIssues(issues); got != 1 {
		t.Errorf("counted %d, want 1 — only the warning counts", got)
	}
}

// ---------- Revision: BaseRecord is a statement about an evaluation ----------

// TestStackAncestry_BaseRecordOnlyAfterEvaluation pins the rule that a
// BaseRecord verdict exists only once the recorded base was actually reached.
// Every earlier exit leaves the field at its zero value, even when the entry
// carries a nonempty LastBaseSHA, because no verdict was formed.
func TestStackAncestry_BaseRecordOnlyAfterEvaluation(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	foreign := secondRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	recorded := sha(t, dir, "refs/heads/main")

	edges := evalEdges(t, dir,
		StackEntry{Name: "cross", Base: "main", Repo: foreign, LastBaseSHA: recorded},
		StackEntry{Name: "loose", Base: "", LastBaseSHA: recorded},
		StackEntry{Name: "ghost", Base: "main", LastBaseSHA: recorded},
		StackEntry{Name: "parent", Base: "no-such-base", LastBaseSHA: recorded},
	)

	for _, want := range []struct {
		name   string
		reason StackAncestryReason
	}{
		{"cross", ReasonCrossRepo},
		{"loose", ReasonBaseUnset},
		{"ghost", ReasonChildRefMissing},
		{"parent", ReasonBaseRefMissing},
	} {
		e := edgeNamed(t, edges, want.name)
		if e.Reason != want.reason {
			t.Fatalf("%s: reason = %q, want %q", want.name, e.Reason, want.reason)
		}
		if e.BaseRecord != "" {
			t.Errorf("%s: base record = %q, want the zero value — the record was never consulted", want.name, e.BaseRecord)
		}
		if e.LastBaseCommit != "" || e.LastBaseShort != "" {
			t.Errorf("%s: peeled record must stay empty: %+v", want.name, e)
		}
	}

	for _, e := range UnevaluatedStackEdges("myfeat", Stack{Branches: []StackEntry{
		{Name: "cross", Base: "main", Repo: foreign, LastBaseSHA: recorded},
		{Name: "loose", Base: "", LastBaseSHA: recorded},
		{Name: "plain", Base: "main", LastBaseSHA: recorded},
	}}, ReasonRepoUnavailable, "no repository") {
		if e.BaseRecord != "" {
			t.Errorf("unevaluated edge %s claims base record %q", e.Name, e.BaseRecord)
		}
	}
}

// TestStackAncestry_BaseRecordAbsentOnlyWhenEvaluated is the positive half: a
// resolved child and base with an empty record does report `absent`.
func TestStackAncestry_BaseRecordAbsentOnlyWhenEvaluated(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	forkChild(t, dir, "child", "parent", 1)
	advance(t, dir, "parent", 1)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent"},
	), "child")
	if edge.BaseRecord != StackBaseRecordAbsent {
		t.Errorf("base record = %q, want absent", edge.BaseRecord)
	}
	if edge.Reason != ReasonParentAdvancedNoBaseRecord {
		t.Errorf("reason = %q, want parent-advanced-no-base-record", edge.Reason)
	}
}

// TestCheckoutHealth_NoBaseRecordDetailWhenUnevaluated proves the rendering
// consequence: no unevaluated entry line carries a `base-record=` token.
func TestCheckoutHealth_NoBaseRecordDetailWhenUnevaluated(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	foreign := secondRepo(t)
	recorded := sha(t, dir, "refs/heads/main")

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "cross", Base: "main", Repo: foreign, LastBaseSHA: recorded},
		{Name: "loose", Base: "", LastBaseSHA: recorded},
		{Name: "ghost", Base: "main", LastBaseSHA: recorded},
	})
	_ = dir

	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	out := FormatCheckoutHealth(report)
	if strings.Contains(out, "base-record=") {
		t.Errorf("no unevaluated entry may render a base-record verdict:\n%s", out)
	}
	if !strings.Contains(out, "reason: cross-repo") || !strings.Contains(out, "reason: base-unset") {
		t.Errorf("expected the reason lines in:\n%s", out)
	}
}

// ---------- Revision: commands stay complete and runnable ----------

// commandSpans returns the contents of every backticked span in s.
func commandSpans(t *testing.T, s string) []string {
	t.Helper()
	var spans []string
	parts := strings.Split(s, "`")
	for i := 1; i < len(parts); i += 2 {
		spans = append(spans, parts[i])
	}
	return spans
}

func TestStackAncestry_RepairCommandIsComplete(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	recorded := sidewaysRewrite(t, dir)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded},
	), "child")
	if edge.Reason != ReasonBaseRewritten {
		t.Fatalf("reason = %q, want base-rewritten", edge.Reason)
	}

	// The rebase target is the bare branch name on purpose: `git rebase`
	// checks out its third argument, and a fully-qualified `refs/heads/<b>`
	// does not resolve to a branch there — Git would detach HEAD and leave the
	// child branch untouched, making the printed "repair" a no-op.
	want := fmt.Sprintf("git rebase --onto %s %s %s", edge.BaseRef, edge.LastBaseCommit, edge.GitBranch)
	if !strings.Contains(edge.Guidance, "`"+want+"`") {
		t.Fatalf("guidance must carry the complete repair command %q, got %q", want, edge.Guidance)
	}
	if edge.ChildRef != "refs/heads/"+edge.GitBranch {
		t.Errorf("child ref = %q, want the explicit probed child branch ref", edge.ChildRef)
	}
	if strings.Contains(edge.Guidance, "--onto "+edge.BaseRef+" "+edge.LastBaseCommit+" "+edge.ChildRef) {
		t.Errorf("the rebase target must not be fully qualified, got %q", edge.Guidance)
	}
	if !strings.Contains(edge.Guidance, edge.LastBaseShort) {
		t.Error("the prose form of the recorded base must stay abbreviated")
	}
}

// TestStackAncestry_RepairCommandActuallyRepairs runs the printed command in a
// throwaway clone of the fixture and asserts that the child branch itself moved
// onto the rewritten parent. A command that detaches HEAD instead would leave
// the branch unchanged and this test would fail.
func TestStackAncestry_RepairCommandActuallyRepairs(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	recorded := sidewaysRewrite(t, dir)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded},
	), "child")
	if edge.Reason != ReasonBaseRewritten {
		t.Fatalf("reason = %q, want base-rewritten", edge.Reason)
	}

	var command string
	for _, span := range commandSpans(t, edge.Guidance) {
		if strings.HasPrefix(span, "git rebase --onto ") {
			command = span
		}
	}
	if command == "" {
		t.Fatalf("no rebase command span in %q", edge.Guidance)
	}

	// Run it from a branch that is not the child, so only an explicit target
	// can move the child branch.
	gitInTest(t, dir, "checkout", "main")
	args := strings.Fields(strings.TrimPrefix(command, "git "))
	gitInTest(t, dir, args...)

	// symbolic-ref fails on a detached HEAD, which is exactly the failure mode
	// a fully-qualified rebase target produces.
	head := gitInTest(t, dir, "symbolic-ref", "--quiet", "HEAD")
	if head != "refs/heads/"+edge.GitBranch {
		t.Errorf("HEAD = %q, want the child branch", head)
	}
	parentHead := sha(t, dir, "refs/heads/parent")
	contained, aErr := gitIsAncestor(dir, parentHead, sha(t, dir, "refs/heads/"+edge.GitBranch))
	if aErr != nil {
		t.Fatal(aErr)
	}
	if !contained {
		t.Error("the child branch was not actually replayed onto the rewritten parent")
	}
}

// TestStackAncestry_RepairWordingDoesNotOverpromise pins that the guidance
// offers the `--onto` command as an equivalent manual repair and never claims
// tws sync runs that exact command. The two sync paths differ: external sync
// adds `--update-refs` and rebases the checked-out worktree branch, checkout
// sync adds `--no-fork-point` and rebases onto the resolved new base SHA.
func TestStackAncestry_RepairWordingDoesNotOverpromise(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	recorded := sidewaysRewrite(t, dir)

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent", LastBaseSHA: recorded},
	), "child")
	if edge.Reason != ReasonBaseRewritten {
		t.Fatalf("reason = %q, want base-rewritten", edge.Reason)
	}
	for _, forbidden := range []string{"which tws sync selects automatically", "--update-refs", "--no-fork-point"} {
		if strings.Contains(edge.Guidance, forbidden) {
			t.Errorf("guidance must not claim a mode-specific sync strategy (%q): %q", forbidden, edge.Guidance)
		}
	}
	for _, want := range []string{"an equivalent manual repair is", "using the flags its own workspace mode requires"} {
		if !strings.Contains(edge.Guidance, want) {
			t.Errorf("guidance %q must contain %q", edge.Guidance, want)
		}
	}
}

// TestStackAncestry_LongRefsAreNotTruncatedInCommands is the regression: a
// branch and base name far longer than the display limit must still produce a
// runnable command with no ellipsis inside it.
func TestStackAncestry_LongRefsAreNotTruncatedInCommands(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	longParent := "feature/" + strings.Repeat("parent-segment-", 6) + "end"
	longChild := "feature/" + strings.Repeat("child-segment-", 6) + "end"

	root := sha(t, dir, "refs/heads/main")
	forkChild(t, dir, longParent, "main", 1)
	recorded := sha(t, dir, "refs/heads/"+longParent)
	forkChild(t, dir, longChild, longParent, 1)
	// Rewrite the parent sideways so the recorded base leaves its history.
	forkChild(t, dir, "sideways", root, 1)
	gitInTest(t, dir, "branch", "-f", longParent, "sideways")

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "p", Branch: longParent, Base: "main"},
		StackEntry{Name: "c", Branch: longChild, Base: "p", LastBaseSHA: recorded},
	), "c")
	if edge.Reason != ReasonBaseRewritten {
		t.Fatalf("reason = %q, want base-rewritten (guidance %q)", edge.Reason, edge.Guidance)
	}

	spans := commandSpans(t, edge.Guidance)
	found := false
	for _, span := range spans {
		if !strings.HasPrefix(span, "git rebase --onto ") {
			continue
		}
		found = true
		if strings.Contains(span, "…") {
			t.Errorf("command span was truncated: %q", span)
		}
		if !strings.Contains(span, longParent) || !strings.Contains(span, longChild) {
			t.Errorf("command span %q must name both full refs", span)
		}
		if !strings.Contains(span, edge.LastBaseCommit) || len(edge.LastBaseCommit) != 40 {
			t.Errorf("command span %q must name the full recorded commit", span)
		}
	}
	if !found {
		t.Fatalf("expected a rebase command span in %q", edge.Guidance)
	}
	assertSanitizedLine(t, edge.Guidance)
}

// TestStackAncestry_CrossRepoPathStaysUseful pins that a cross-repo path is
// sanitized but long enough to identify the repository it names.
func TestStackAncestry_CrossRepoPathStaysUseful(t *testing.T) {
	longPath := "/srv/" + strings.Repeat("nested-directory/", 5) + "other-repo"
	e := StackEdge{Name: "x", Repo: longPath, Reason: ReasonCrossRepo}
	guidance := ancestryGuidance(e, "")
	if !strings.Contains(guidance, longPath) {
		t.Errorf("cross-repo guidance must keep the whole path, got %q", guidance)
	}
	if !strings.Contains(guidance, string(AncestryStatusCrossRepo)) {
		t.Errorf("cross-repo guidance must name the reported token, got %q", guidance)
	}

	hostile := StackEdge{Name: "x", Repo: "/srv/\x1bevil\nrepo", Reason: ReasonCrossRepo}
	assertSanitizedLine(t, ancestryGuidance(hostile, ""))
}

// TestStackAncestry_CommandTokenQuoting pins the shell-safety rule for tokens
// that Git accepted but a shell would misread.
func TestStackAncestry_CommandTokenQuoting(t *testing.T) {
	if got := ancestryCommandToken("refs/heads/feature/a-b_c.d"); got != "refs/heads/feature/a-b_c.d" {
		t.Errorf("plain ref must not be quoted, got %q", got)
	}
	if got := ancestryCommandToken(":/some message"); got != "':/some message'" {
		t.Errorf("a token with a space must be quoted, got %q", got)
	}
	if got := ancestryCommandToken("a\nb"); strings.ContainsAny(got, "\n") {
		t.Errorf("control characters must be replaced, got %q", got)
	}
	if got := ancestryCommandToken(""); got != "" {
		t.Errorf("empty token = %q, want empty", got)
	}
}

// ---------- Revision: peeled output must be structurally a full SHA ----------

func TestStackAncestry_PeeledOutputMustBeFullSHA(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "child", "main", 1)
	full := sha(t, dir, "refs/heads/child")

	edge := evalEdges(t, dir, StackEntry{Name: "child", Base: "main"})[0]
	for _, got := range []string{edge.LocalHead, edge.ParentHead} {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(got) {
			t.Errorf("peeled commit %q is not a structurally valid object name", got)
		}
	}
	if edge.LocalHead != full {
		t.Errorf("local head = %q, want %q", edge.LocalHead, full)
	}
	if !ancestryFullSHA.MatchString(full) || ancestryFullSHA.MatchString(full[:12]) {
		t.Error("the structural SHA guard must accept only a 40-hex object name")
	}
}

// ---------- Revision: identity notes follow the caller's sync policy ----------

// remoteMismatchFixture builds a repository whose local default branch differs
// from origin/<default>, which is exactly what external sync would resolve.
func remoteMismatchFixture(t *testing.T) string {
	t.Helper()
	dir, _ := setupHealthTestRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitInTest(t, dir, "init", "--bare", remote)
	gitInTest(t, dir, "remote", "add", "origin", remote)
	gitInTest(t, dir, "push", "-u", "origin", "main")
	gitInTest(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	forkChild(t, dir, "ahead", "main", 1)
	gitInTest(t, dir, "push", "origin", "ahead:main")
	gitInTest(t, dir, "fetch", "origin")
	forkChild(t, dir, "child", "main", 1)
	return dir
}

// literalMismatchFixture builds a stack whose parent entry name also resolves
// as a literal ref, which is exactly what checkout sync would resolve.
func literalMismatchFixture(t *testing.T) (string, []StackEntry) {
	t.Helper()
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "jd/core", "main", 1)
	forkChild(t, dir, "core", "main", 2)
	forkChild(t, dir, "jd/api", "jd/core", 1)
	return dir, []StackEntry{
		{Name: "core", Branch: "jd/core", Base: "main"},
		{Name: "api", Branch: "jd/api", Base: "core"},
	}
}

func noteKinds(edges []StackEdge) []StackNoteKind {
	var kinds []StackNoteKind
	for _, e := range edges {
		for _, n := range e.Notes {
			kinds = append(kinds, n.Kind)
		}
	}
	return kinds
}

func TestStackAncestry_BasePolicyForMode(t *testing.T) {
	if got := StackBasePolicyForMode(ModeCheckout); got != StackBasePolicyLiteralEntry {
		t.Errorf("checkout policy = %q, want literal-entry", got)
	}
	if got := StackBasePolicyForMode(ModeExternal); got != StackBasePolicyRemoteDefault {
		t.Errorf("external policy = %q, want remote-default", got)
	}
	if got := StackBasePolicyForMode(""); got != StackBasePolicyRemoteDefault {
		t.Errorf("unset mode policy = %q, want the external default", got)
	}
}

// TestStackAncestry_PolicyNoneEmitsNoNotes pins the safe zero value: without an
// explicit policy the evaluator makes no claim about any sync path.
func TestStackAncestry_PolicyNoneEmitsNoNotes(t *testing.T) {
	remoteDir := remoteMismatchFixture(t)
	if kinds := noteKinds(evalEdges(t, remoteDir, StackEntry{Name: "child", Base: "main"})); len(kinds) != 0 {
		t.Errorf("policy none emitted %v", kinds)
	}
	literalDir, entries := literalMismatchFixture(t)
	if kinds := noteKinds(evalEdges(t, literalDir, entries...)); len(kinds) != 0 {
		t.Errorf("policy none emitted %v", kinds)
	}
}

// TestStackAncestry_PolicyNeverCrossesModes is the no-spurious-note rule: each
// policy may only report the mismatch its own sync path would cause, even on a
// fixture that would trigger the other one.
func TestStackAncestry_PolicyNeverCrossesModes(t *testing.T) {
	remoteDir := remoteMismatchFixture(t)
	literalDir, literalEntries := literalMismatchFixture(t)

	remoteOnRemote := noteKinds(evalEdgesWith(t, remoteDir,
		StackAncestryOptions{BasePolicy: StackBasePolicyRemoteDefault},
		StackEntry{Name: "child", Base: "main"}))
	if len(remoteOnRemote) != 1 || remoteOnRemote[0] != NoteBaseIdentityRemoteMismatch {
		t.Errorf("remote policy on a remote fixture = %v, want exactly one remote note", remoteOnRemote)
	}

	literalOnRemote := noteKinds(evalEdgesWith(t, remoteDir,
		StackAncestryOptions{BasePolicy: StackBasePolicyLiteralEntry},
		StackEntry{Name: "child", Base: "main"}))
	if len(literalOnRemote) != 0 {
		t.Errorf("checkout policy must never emit the external note, got %v", literalOnRemote)
	}

	literalOnLiteral := noteKinds(evalEdgesWith(t, literalDir,
		StackAncestryOptions{BasePolicy: StackBasePolicyLiteralEntry}, literalEntries...))
	if len(literalOnLiteral) != 1 || literalOnLiteral[0] != NoteBaseIdentityLiteralMismatch {
		t.Errorf("checkout policy on a literal fixture = %v, want exactly one literal note", literalOnLiteral)
	}

	remoteOnLiteral := noteKinds(evalEdgesWith(t, literalDir,
		StackAncestryOptions{BasePolicy: StackBasePolicyRemoteDefault}, literalEntries...))
	for _, kind := range remoteOnLiteral {
		if kind == NoteBaseIdentityLiteralMismatch {
			t.Error("external policy must never emit the checkout note")
		}
	}
}

// TestStackAncestry_CheckoutModeUsesLiteralPolicy proves the mode selection is
// actually wired through FeatureStackEdges and reaches checkout doctor output.
func TestStackAncestry_CheckoutModeUsesLiteralPolicy(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	forkChild(t, dir, "jd/core", "main", 1)
	forkChild(t, dir, "core", "main", 2)
	forkChild(t, dir, "jd/api", "jd/core", 1)

	entries := []StackEntry{
		{Name: "core", Branch: "jd/core", Base: "main"},
		{Name: "api", Branch: "jd/api", Base: "core"},
	}
	addStackEntries(t, ws, "myfeat", entries)

	edges, _ := FeatureStackEdges(ws, LoadConfig(), "myfeat", ws.FeaturePath("myfeat"), Stack{Branches: entries})
	kinds := noteKinds(edges)
	if len(kinds) != 1 || kinds[0] != NoteBaseIdentityLiteralMismatch {
		t.Fatalf("checkout mode notes = %v, want exactly one literal mismatch", kinds)
	}

	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	out := FormatCheckoutHealth(report)
	if !strings.Contains(out, "note: base name") {
		t.Errorf("expected the literal mismatch note line in:\n%s", out)
	}
	if strings.Contains(out, "tws sync resolves it as origin/") {
		t.Errorf("checkout output must never carry the external note:\n%s", out)
	}
}

// ---------- Revision: external repository resolution candidates ----------

// externalWorkspaceWithoutWorktrees builds an external metadata root holding a
// feature whose entries were never materialized.
func externalWorkspaceWithoutWorktrees(t *testing.T, metadataRoot string) (string, Stack) {
	t.Helper()
	featurePath := filepath.Join(metadataRoot, "myfeat")
	if err := os.MkdirAll(featurePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureExternalWorkspaceMarker(metadataRoot); err != nil {
		t.Fatal(err)
	}
	stack := Stack{Branches: []StackEntry{{Name: "child", Base: "main"}}}
	if err := SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}
	return featurePath, stack
}

// TestStackAncestry_ExternalWorkspaceCandidate covers candidate 2: with no
// worktree evidence at all, a valid ws.RepoRoot is the source.
func TestStackAncestry_ExternalWorkspaceCandidate(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "child", "main", 1)

	metadataRoot := filepath.Join(t.TempDir(), "detached-workspace")
	featurePath, stack := externalWorkspaceWithoutWorktrees(t, metadataRoot)

	ws := Workspace{Mode: ModeExternal, RepoRoot: dir, MetadataRoot: metadataRoot}
	res := ResolveStackAncestryRepo(ws, Config{}, featurePath, stack)
	if res.Source != StackRepoWorkspace {
		t.Fatalf("source = %q, want workspace (res %+v)", res.Source, res)
	}
	if res.RepoDir != canonicalize(dir) {
		t.Errorf("repo dir = %q, want %q", res.RepoDir, canonicalize(dir))
	}
	if res.Alternate != "" || res.Reason != "" {
		t.Errorf("a single successful candidate must report no alternate or reason: %+v", res)
	}

	edges, _ := FeatureStackEdges(ws, Config{}, "myfeat", featurePath, stack)
	if edges[0].Status != AncestryStatusCurrent || edges[0].RepoSource != StackRepoWorkspace {
		t.Errorf("edge = %+v, want a current edge stamped workspace", edges[0])
	}
}

// TestStackAncestry_ExternalInferredCandidate covers candidate 3: with no
// worktree evidence and no ws.RepoRoot, a `<repo>.tws` sibling metadata root
// still identifies the canonical main repository root.
func TestStackAncestry_ExternalInferredCandidate(t *testing.T) {
	dir, _ := setupHealthTestRepo(t)
	forkChild(t, dir, "child", "main", 1)

	metadataRoot := canonicalize(dir) + ".tws"
	t.Cleanup(func() { _ = os.RemoveAll(metadataRoot) })
	featurePath, stack := externalWorkspaceWithoutWorktrees(t, metadataRoot)

	ws := Workspace{Mode: ModeExternal, MetadataRoot: metadataRoot}
	res := ResolveStackAncestryRepo(ws, Config{}, featurePath, stack)
	if res.Source != StackRepoInferred {
		t.Fatalf("source = %q, want inferred (res %+v)", res.Source, res)
	}
	if res.RepoDir != canonicalize(dir) {
		t.Errorf("repo dir = %q, want the canonical main root %q", res.RepoDir, canonicalize(dir))
	}
	assertCanonicalMainRoot(t, res.RepoDir)

	edges, _ := FeatureStackEdges(ws, Config{}, "myfeat", featurePath, stack)
	if edges[0].Status != AncestryStatusCurrent || edges[0].RepoSource != StackRepoInferred {
		t.Errorf("edge = %+v, want a current edge stamped inferred", edges[0])
	}
}

// TestAncestryHealthIssues_MismatchPathsSanitized pins that the feature-level
// mismatch issue never echoes a raw control character from a recorded path.
func TestAncestryHealthIssues_MismatchPathsSanitized(t *testing.T) {
	res := StackRepoResolution{
		RepoDir:   "/srv/repo\x1b[31m",
		Source:    StackRepoWorktree,
		Alternate: "/srv/other\nrepo",
	}
	issues := AncestryHealthIssues(res, nil)
	if len(issues) != 1 {
		t.Fatalf("expected one mismatch issue, got %+v", issues)
	}
	if strings.ContainsAny(issues[0].Problem, "\n\r\t\x1b") || strings.ContainsAny(issues[0].Hint, "\n\r\t\x1b") {
		t.Errorf("mismatch issue is not sanitized: %+v", issues[0])
	}
	if !strings.Contains(issues[0].Problem, "/srv/repo") || !strings.Contains(issues[0].Hint, "/srv/other") {
		t.Errorf("mismatch issue lost the identifying path: %+v", issues[0])
	}
}

// TestAncestryHealthIssues_NotesProjected pins that notes reach external doctor
// for every edge, including `current` ones, and never change the count.
func TestAncestryHealthIssues_NotesProjected(t *testing.T) {
	edges := []StackEdge{
		{
			Name: "ok", Status: AncestryStatusCurrent, Reason: ReasonParentContained, Severity: SeverityOK,
			Notes: []StackEdgeNote{{Kind: NoteBaseIdentityRemoteMismatch, Detail: "remote detail"}},
		},
		{
			Name: "stale", Status: AncestryStatusStale, Reason: ReasonParentAdvanced, Severity: SeverityWarning,
			Guidance: "run sync",
			Notes:    []StackEdgeNote{{Kind: NoteBaseIdentityLiteralMismatch, Detail: "literal detail"}},
		},
	}
	issues := AncestryHealthIssues(StackRepoResolution{RepoDir: "/repo", Source: StackRepoWorkspace}, edges)
	if len(issues) != 3 {
		t.Fatalf("expected one note issue per edge plus the stale warning, got %+v", issues)
	}
	if issues[0].Branch != "ok" || issues[0].Problem != "ancestry note: base-identity-remote-mismatch" {
		t.Errorf("a current edge must still project its note: %+v", issues[0])
	}
	if issues[0].Hint != "remote detail" || issues[0].Severity != SeverityInfo {
		t.Errorf("note issue = %+v, want the detail as an info hint", issues[0])
	}
	if issues[1].Problem != "ancestry stale: parent-advanced" {
		t.Errorf("the edge issue must precede its note: %+v", issues[1])
	}
	if issues[2].Problem != "ancestry note: base-identity-literal-mismatch" {
		t.Errorf("unexpected third issue: %+v", issues[2])
	}
	if got := CountHealthIssues(issues); got != 1 {
		t.Errorf("counted %d, want 1 — notes are never counted", got)
	}
}

// ---------- Revision: probe failure reaches the surface end to end ----------

// TestStackAncestry_ProbeFailedEndToEnd replaces `git merge-base` with a shim
// that fails fatally, so the evaluator's degraded path is exercised through the
// real classifier and the real formatter rather than a synthetic edge.
func TestStackAncestry_ProbeFailedEndToEnd(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not found: %v", err)
	}
	dir, ws := setupHealthTestRepo(t)
	forkChild(t, dir, "parent", "main", 1)
	forkChild(t, dir, "child", "parent", 1)
	advance(t, dir, "parent", 1)
	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "parent", Base: "main"},
		{Name: "child", Base: "parent"},
	})

	binDir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"merge-base\" ]; then\n    echo 'fatal: simulated merge-base failure' >&2\n    exit 128\n  fi\ndone\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	edge := edgeNamed(t, evalEdges(t, dir,
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "child", Base: "parent"},
	), "child")
	if edge.Reason != ReasonAncestryProbeFailed {
		t.Fatalf("reason = %q, want ancestry-probe-failed", edge.Reason)
	}
	if edge.Status != "" {
		t.Errorf("status = %q, want the empty (not evaluated) status", edge.Status)
	}
	if edge.Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", edge.Severity)
	}
	if edge.MergeBase != nil {
		t.Error("a failed probe must publish no merge base")
	}
	if !strings.Contains(edge.Guidance, "ancestry probe failed") || !strings.Contains(edge.Guidance, "re-run: tws doctor myfeat") {
		t.Errorf("guidance = %q", edge.Guidance)
	}
	assertSanitizedLine(t, edge.Guidance)

	report, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{}})
	if err != nil {
		t.Fatalf("a failed probe must never fail the report: %v", err)
	}
	out := FormatCheckoutHealth(report)
	if !strings.Contains(out, "ancestry=unevaluated") || !strings.Contains(out, "reason: ancestry-probe-failed") {
		t.Errorf("expected the unevaluated rendering in:\n%s", out)
	}
	if report.HasErrors() {
		t.Error("a failed ancestry probe must never produce an error exit")
	}
}
