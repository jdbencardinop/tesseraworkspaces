package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// TestSyncPlanIntegration: production cli.Execute() over real repositories in
// BOTH workspace modes — the spec's own "customer topology" fixture (master/
// main = A-B'-D with B rewritten, pr2 = A-B-C, recorded cutoff B, exactly one
// replay candidate
// C), an executed guarded run's argv against its unguarded twin, external
// pass 2 (archived row), checkout fresh and --continue plan routes, and the
// plan-guard marker reaching stderr through production Execute().
// ---------------------------------------------------------------------------

// setupCustomerTopologyExternal builds the spec's required customer fixture
// in external mode — the TRUE merged-parent/history-rewrite topology of
// spec.md's own worked example, not a weakened linear one:
//
//	master = A - B' - D      (B was rewritten into B'; B is NOT an ancestor of D)
//	pr2    = A - B  - C      (a worktree branched at the ORIGINAL B)
//
// so B is the stale recorded cutoff, D is master's current tip, and
// `git rev-list B..feat-pr2` is exactly {C}. §10.1 rule 1 makes the recorded
// cutoff — not the destination — the replay upstream in BOTH arms, so the
// row must publish upstream B, range `<B>..feat-pr2`, candidate_count 1,
// first candidate C, destination D and argv `--onto D B`. Measuring against
// D instead would yield {B, C} (count 2), which is precisely the regression
// this topology exists to catch.
func setupCustomerTopologyExternal(t *testing.T) (repo, bSHA, dSHA, cSHA string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo = setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	// master: A-B (B is the fork point the stack later records as its cutoff).
	writeAndCommit(t, repo, "b.txt", "B\n", "B")
	bSHA = gitOutput(t, repo, "rev-parse", "HEAD")

	// pr2 forks AT the original B, in its own worktree, then adds C: A-B-C.
	worktree := internal.WorktreePath("feature", "pr2")
	gitRun(t, repo, "worktree", "add", worktree, "-b", "feat-pr2", "master")
	writeAndCommit(t, worktree, "c.txt", "C\n", "C")
	cSHA = gitOutput(t, worktree, "rev-parse", "HEAD")

	// master REWRITES B into B' (the merged-parent case: the upstream branch
	// re-created the shared commit), then advances to D: A-B'-D. feat-pr2
	// keeps the original B alive, but B is no longer reachable from master.
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("B prime\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "b.txt")
	gitRun(t, repo, "commit", "--amend", "-m", "B-prime")
	bPrimeSHA := gitOutput(t, repo, "rev-parse", "HEAD")
	if bPrimeSHA == bSHA {
		t.Fatalf("fixture is not a rewrite: B' (%s) equals B (%s)", bPrimeSHA, bSHA)
	}
	writeAndCommit(t, repo, "d.txt", "D\n", "D")
	dSHA = gitOutput(t, repo, "rev-parse", "HEAD")
	assertNotAncestor(t, repo, bSHA, dSHA)
	gitRun(t, repo, "push", "--force", "origin", "master")
	gitRun(t, repo, "fetch", "origin")

	featurePath := internal.FeaturePath("feature")
	if err := os.MkdirAll(featurePath, 0755); err != nil {
		t.Fatal(err)
	}
	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "pr2", Branch: "feat-pr2", Base: "master", LastBaseSHA: bSHA},
	}}
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}
	return repo, bSHA, dSHA, cSHA
}

// setupCustomerTopologyCheckout builds the same TRUE rewrite customer
// fixture (see setupCustomerTopologyExternal) in checkout mode: a single
// repository, no worktrees, no remote —
//
//	main = A - B' - D   (B rewritten into B'; B is NOT an ancestor of D)
//	pr2  = A - B  - C
//
// It finishes checked out on pr2 rather than main: internal.ResolveSyncBase
// applies the very same default-branch rewrite rule external mode uses,
// unconditioned on workspace mode, and internal.DefaultBranchIn falls back
// to the CURRENTLY CHECKED-OUT branch whenever no origin/HEAD exists (as
// here) — so checking out "main" itself before evaluating would make the
// fallback default branch collide with entry.Base ("main"), wrongly
// rewriting it to the nonexistent ref "origin/main".
func setupCustomerTopologyCheckout(t *testing.T) (dir, featurePath, bSHA, dSHA, cSHA string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir = setupCheckoutSyncRepo(t)

	// main: A-B (the fork point the stack later records as its cutoff).
	writeFileCS(t, dir, "b.txt", "B\n")
	gitRunCS(t, dir, "add", "b.txt")
	gitRunCS(t, dir, "commit", "-m", "B")
	bSHA = gitSHA(t, dir, "HEAD")

	// pr2 forks AT the original B, then adds C: A-B-C.
	gitRunCS(t, dir, "checkout", "-b", "pr2", "main")
	writeFileCS(t, dir, "c.txt", "C\n")
	gitRunCS(t, dir, "add", "c.txt")
	gitRunCS(t, dir, "commit", "-m", "C")
	cSHA = gitSHA(t, dir, "HEAD")

	// main REWRITES B into B', then advances to D: A-B'-D.
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "b.txt", "B prime\n")
	gitRunCS(t, dir, "add", "b.txt")
	gitRunCS(t, dir, "commit", "--amend", "-m", "B-prime")
	if bPrime := gitSHA(t, dir, "HEAD"); bPrime == bSHA {
		t.Fatalf("fixture is not a rewrite: B' (%s) equals B (%s)", bPrime, bSHA)
	}
	writeFileCS(t, dir, "d.txt", "D\n")
	gitRunCS(t, dir, "add", "d.txt")
	gitRunCS(t, dir, "commit", "-m", "D")
	dSHA = gitSHA(t, dir, "HEAD")
	assertNotAncestor(t, dir, bSHA, dSHA)

	// See the doc comment: leave HEAD on pr2, not main, before any --plan
	// evaluation runs.
	gitRunCS(t, dir, "checkout", "pr2")

	writeCheckoutModeMarker(t, dir)
	featurePath = setupFeaturePath(t, dir)
	saveTestStack(t, featurePath, []internal.StackEntry{
		{Name: "pr2", Base: "main", LastBaseSHA: bSHA},
	})
	return dir, featurePath, bSHA, dSHA, cSHA
}

// assertNotAncestor fails unless `ancestor` is genuinely UNREACHABLE from
// `descendant` — the load-bearing property of the rewrite topology above,
// asserted directly so a fixture that silently degenerates into the linear
// shape can never pass the customer tests.
func assertNotAncestor(t *testing.T, dir, ancestor, descendant string) {
	t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Fatalf("fixture broken: %s IS an ancestor of %s; the customer topology requires a real rewrite", ancestor, descendant)
	}
}

// writeCheckoutModeMarker writes the .tws/config.yaml a real CLI invocation
// needs to resolve checkout mode from cwd (internal.RequireWorkspace reads
// it before dispatch); direct internal.RunCheckoutSync callers never need
// this, since they pass RepoDir/FeaturePath explicitly, but every test in
// this file that drives production cli.Execute() does.
func writeCheckoutModeMarker(t *testing.T, dir string) {
	t.Helper()
	twsDir := filepath.Join(dir, ".tws")
	if err := os.MkdirAll(twsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(twsDir, "config.yaml"), []byte("workspace_mode: checkout\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Customer topology (spec.md's required fixture): old cutoff B, new base D,
// exactly one replay candidate C — in both human and --json form, external.
// ---------------------------------------------------------------------------

func TestSyncPlanIntegration_CustomerTopologyExternal(t *testing.T) {
	_, bSHA, dSHA, cSHA := setupCustomerTopologyExternal(t)
	bShort, dShort, cShort := shortSHAForTest(bSHA), shortSHAForTest(dSHA), shortSHAForTest(cSHA)

	t.Run("human", func(t *testing.T) {
		stdout, stderr, exit := runSyncExecute(t, "feature", "--plan", "--no-fetch")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		// §10.1 rule 1: the replay upstream is the RECORDED CUTOFF (B), not
		// the destination (D). The destination column still shows D.
		wantRow := "  - pr2 [feat-pr2] base master \u2192 origin/master@" + dShort + " cutoff " + bShort + " upstream " + bShort + " strategy onto\n"
		if !strings.Contains(stdout, wantRow) {
			t.Fatalf("entries row missing/mismatched.\nwant substring: %q\ngot stdout:\n%s", wantRow, stdout)
		}
		wantCandidates := "    candidates 1 range " + bShort + "..feat-pr2 first " + cShort + " \"C\"\n"
		if !strings.Contains(stdout, wantCandidates) {
			t.Fatalf("candidates row missing/mismatched.\nwant substring: %q\ngot stdout:\n%s", wantCandidates, stdout)
		}
	})

	t.Run("json", func(t *testing.T) {
		stdout, stderr, exit := runSyncExecute(t, "feature", "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		entries, ok := doc["entries"].([]any)
		if !ok || len(entries) != 1 {
			t.Fatalf("expected exactly one entry, got %#v", doc["entries"])
		}
		entry := entries[0].(map[string]any)

		base := entry["base"].(map[string]any)
		if got := base["ref"]; got != "origin/master" {
			t.Fatalf("base.ref = %v, want origin/master (external's default-branch rewrite)", got)
		}

		destination := entry["destination"].(map[string]any)
		if got := destination["sha"]; got != dSHA {
			t.Fatalf("destination.sha = %v, want new base D (%s)", got, dSHA)
		}

		cutoff := entry["cutoff"].(map[string]any)
		if got := cutoff["recorded_sha"]; got != bSHA {
			t.Fatalf("cutoff.recorded_sha = %v, want old cutoff B (%s)", got, bSHA)
		}

		replay := entry["replay"].(map[string]any)
		if got := replay["candidate_count"]; got != float64(1) {
			t.Fatalf("replay.candidate_count = %v, want 1 (the sole candidate C in B..feat-pr2)", got)
		}
		// §10.1 rule 1: a present, used, recorded cutoff IS the replay
		// upstream in both arms; the destination (D) never stands in for it.
		if got := replay["upstream_sha"]; got != bSHA {
			t.Fatalf("replay.upstream_sha = %v, want recorded cutoff B (%s), never destination D (%s)", got, bSHA, dSHA)
		}
		if got := replay["upstream_ref"]; got != bSHA {
			t.Fatalf("replay.upstream_ref = %v, want the cutoff B (%s) on an onto arm", got, bSHA)
		}
		if got := replay["upstream_provenance"]; got != "recorded-cutoff" {
			t.Fatalf("replay.upstream_provenance = %v, want recorded-cutoff", got)
		}
		if got := replay["determinacy"]; got != "exact" {
			t.Fatalf("replay.determinacy = %v, want exact", got)
		}
		// §4.2: replay.range is the FULL upstream SHA, never a ref name and
		// never an abbreviation.
		if got := replay["range"]; got != bSHA+"..feat-pr2" {
			t.Fatalf("replay.range = %v, want %q (full cutoff SHA .. git branch)", got, bSHA+"..feat-pr2")
		}
		first := replay["first_candidate"].(map[string]any)
		if got := first["sha"]; got != cSHA {
			t.Fatalf("replay.first_candidate.sha = %v, want the sole replay candidate C (%s)", got, cSHA)
		}
		if got := entry["strategy"]; got != "onto" {
			t.Fatalf("strategy = %v, want onto", got)
		}
		assertOntoArgv(t, entry, dSHA, bSHA, "origin/master")
	})

	// The executed guarded arm really runs `--onto D B`: the destination is
	// pinned to D's full SHA (§10.5) and the recorded-cutoff operand is B.
	t.Run("guarded-argv", func(t *testing.T) {
		w := newSyncGitWrapper(t, false)
		var stderr string
		var exit int
		w.around(t, func() {
			_, stderr, exit = runSyncExecute(t, "feature", "--no-fetch", "--max-replay-total", "5")
		})
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		var rebases [][]string
		for _, r := range w.records(t) {
			if r.Verb == "rebase" {
				rebases = append(rebases, r.Tail())
			}
		}
		if len(rebases) != 1 {
			t.Fatalf("expected exactly one rebase argv, got %#v", rebases)
		}
		i := slices.Index(rebases[0], "--onto")
		if i < 0 || i+2 >= len(rebases[0]) {
			t.Fatalf("guarded rebase argv = %#v, want an --onto <destination> <cutoff> arm", rebases[0])
		}
		if rebases[0][i+1] != dSHA {
			t.Fatalf("guarded --onto operand = %q, want the destination D pinned to its full SHA (%s)", rebases[0][i+1], dSHA)
		}
		if rebases[0][i+2] != bSHA {
			t.Fatalf("guarded cutoff operand = %q, want the recorded cutoff B (%s)", rebases[0][i+2], bSHA)
		}
		if i+3 != len(rebases[0]) {
			t.Fatalf("guarded rebase argv = %#v, want the cutoff operand last (`--onto D B`)", rebases[0])
		}
	})
}

// assertOntoArgv checks one plan row's published argv is the external
// pass-1 `onto` shape whose --onto operand is the destination and whose
// trailing operand is the recorded cutoff. The plan-only route publishes the
// destination REF (Git re-resolves it); §10.5's pinning applies to executed
// guarded argv only, which the guarded-argv subtest asserts separately.
func assertOntoArgv(t *testing.T, entry map[string]any, destinationSHA, cutoffSHA, destinationRef string) {
	t.Helper()
	raw, ok := entry["argv"].([]any)
	if !ok {
		t.Fatalf("argv = %#v, want a published array", entry["argv"])
	}
	var argv []string
	for _, tok := range raw {
		argv = append(argv, tok.(string))
	}
	if len(argv) < 2 || argv[len(argv)-2] != "--onto" && !slices.Contains(argv, "--onto") {
		t.Fatalf("argv = %#v, want an --onto arm", argv)
	}
	i := slices.Index(argv, "--onto")
	if i < 0 || i+2 >= len(argv)+1 {
		t.Fatalf("argv = %#v, want --onto <destination> <cutoff>", argv)
	}
	if argv[i+1] != destinationRef {
		t.Fatalf("argv --onto operand = %q, want the destination ref %q (resolving to %s)", argv[i+1], destinationRef, destinationSHA)
	}
	if argv[i+2] != cutoffSHA {
		t.Fatalf("argv cutoff operand = %q, want the recorded cutoff %s", argv[i+2], cutoffSHA)
	}
}

// TestSyncPlanIntegration_CustomerTopologyCheckout mirrors the external test
// above in checkout mode, asserting the base resolves LITERALLY (never
// rewritten to an "origin/..." remote-tracking form — checkout mode has no
// remote concept in ResolveSyncBase's rewrite rule).
func TestSyncPlanIntegration_CustomerTopologyCheckout(t *testing.T) {
	dir, _, bSHA, dSHA, cSHA := setupCustomerTopologyCheckout(t)
	withUnifiedWorkspaceEnv(t, dir)
	bShort, dShort, cShort := shortSHAForTest(bSHA), shortSHAForTest(dSHA), shortSHAForTest(cSHA)

	t.Run("human", func(t *testing.T) {
		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--no-fetch")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		wantRow := "  - pr2 [pr2] base main \u2192 main@" + dShort + " cutoff " + bShort + " upstream " + bShort + " strategy onto\n"
		if !strings.Contains(stdout, wantRow) {
			t.Fatalf("entries row missing/mismatched.\nwant substring: %q\ngot stdout:\n%s", wantRow, stdout)
		}
		wantCandidates := "    candidates 1 range " + bShort + "..pr2 first " + cShort + " \"C\"\n"
		if !strings.Contains(stdout, wantCandidates) {
			t.Fatalf("candidates row missing/mismatched.\nwant substring: %q\ngot stdout:\n%s", wantCandidates, stdout)
		}
	})

	t.Run("json", func(t *testing.T) {
		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		entries, ok := doc["entries"].([]any)
		if !ok || len(entries) != 1 {
			t.Fatalf("expected exactly one entry, got %#v", doc["entries"])
		}
		entry := entries[0].(map[string]any)

		base := entry["base"].(map[string]any)
		if got := base["ref"]; got != "main" {
			t.Fatalf("base.ref = %v, want the literal \"main\" (checked out on pr2, so no wrongful rewrite)", got)
		}

		destination := entry["destination"].(map[string]any)
		if got := destination["sha"]; got != dSHA {
			t.Fatalf("destination.sha = %v, want new base D (%s)", got, dSHA)
		}

		cutoff := entry["cutoff"].(map[string]any)
		if got := cutoff["recorded_sha"]; got != bSHA {
			t.Fatalf("cutoff.recorded_sha = %v, want old cutoff B (%s)", got, bSHA)
		}

		replay := entry["replay"].(map[string]any)
		if got := replay["candidate_count"]; got != float64(1) {
			t.Fatalf("replay.candidate_count = %v, want 1 (the sole candidate C in B..pr2)", got)
		}
		if got := replay["upstream_sha"]; got != bSHA {
			t.Fatalf("replay.upstream_sha = %v, want recorded cutoff B (%s), never destination D (%s)", got, bSHA, dSHA)
		}
		if got := replay["range"]; got != bSHA+"..pr2" {
			t.Fatalf("replay.range = %v, want %q (full cutoff SHA .. git branch)", got, bSHA+"..pr2")
		}
		if got := replay["upstream_provenance"]; got != "recorded-cutoff" {
			t.Fatalf("replay.upstream_provenance = %v, want recorded-cutoff", got)
		}
		first := replay["first_candidate"].(map[string]any)
		if got := first["sha"]; got != cSHA {
			t.Fatalf("replay.first_candidate.sha = %v, want the sole replay candidate C (%s)", got, cSHA)
		}
		// Checkout already pins --onto to the destination SHA on every route.
		assertOntoArgv(t, entry, dSHA, bSHA, dSHA)
	})
}

// shortSHAForTest mirrors internal's own shortSHA (first twelve lowercase hex
// characters) so this file's human-form assertions never depend on an
// unexported production helper.
func shortSHAForTest(sha string) string {
	sha = strings.ToLower(sha)
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

// ---------------------------------------------------------------------------
// An executed guarded run's argv is the MATCHED PAIR of its unguarded twin's
// (spec.md §10.5): every element is equal except the --onto operand, which a
// guarded execution path pins to the planned, JIT-revalidated full SHA where
// the unguarded twin passes the ref name. A row with no --onto arm — and every
// unguarded, golden-covered invocation — stays byte-identical.
// ---------------------------------------------------------------------------

func TestSyncPlanIntegration_GuardedRunArgvEqualsUnguardedTwin(t *testing.T) {
	captureRebaseArgv := func(extra ...string) [][]string {
		f := newScopedFixture(t)
		w := newSyncGitWrapper(t, false)
		var stderr string
		var exit int
		args := append([]string{f.feature, "--no-fetch"}, extra...)
		w.around(t, func() {
			_, stderr, exit = runSyncExecute(t, args...)
		})
		if exit != 0 {
			t.Fatalf("sync must succeed: exit=%d stderr=%q", exit, stderr)
		}
		var argvs [][]string
		for _, r := range w.records(t) {
			if r.Verb == "rebase" {
				argvs = append(argvs, r.Tail())
			}
		}
		return argvs
	}

	unguarded := captureRebaseArgv()
	guarded := captureRebaseArgv("--max-replay-total", "999")

	if len(unguarded) == 0 {
		t.Fatal("fixture produced no rebase invocations to compare")
	}
	if len(guarded) != len(unguarded) {
		t.Fatalf("guarded issued %d rebases, unguarded issued %d: guarded=%v unguarded=%v",
			len(guarded), len(unguarded), guarded, unguarded)
	}
	for i := range unguarded {
		assertMatchedRebaseArgv(t, i, unguarded[i], guarded[i])
	}
}

// assertMatchedRebaseArgv is §10.5's matched-pair predicate: the two argvs
// agree element for element, except that where the unguarded control passes a
// ref name after --onto the guarded run passes a full lowercase-hex SHA.
func assertMatchedRebaseArgv(t *testing.T, row int, unguarded, guarded []string) {
	t.Helper()
	if len(guarded) != len(unguarded) {
		t.Fatalf("rebase[%d] argv length diverged:\n  unguarded=%v\n  guarded=%v", row, unguarded, guarded)
	}
	ontoOperand := -1
	for i, tok := range unguarded {
		if tok == "--onto" && i+1 < len(unguarded) {
			ontoOperand = i + 1
			break
		}
	}
	for i := range unguarded {
		if i == ontoOperand {
			continue
		}
		if guarded[i] != unguarded[i] {
			t.Fatalf("rebase[%d] argv diverged outside the --onto operand at %d:\n  unguarded=%v\n  guarded=%v",
				row, i, unguarded, guarded)
		}
	}
	if ontoOperand < 0 {
		return
	}
	pinned := guarded[ontoOperand]
	if !isFullHexSHA(pinned) {
		t.Fatalf("rebase[%d] guarded --onto operand %q is not a pinned full SHA (unguarded passed %q)",
			row, pinned, unguarded[ontoOperand])
	}
	if pinned == unguarded[ontoOperand] {
		t.Fatalf("rebase[%d] guarded run did not pin its destination: both sides passed %q", row, pinned)
	}
}

func isFullHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// TestSyncPlanIntegration_GuardedRunPinsOntoDestination drives a row that
// really produces an --onto arm — a recorded LastBaseSHA that no longer equals
// the current base — and asserts §10.5's pinning inside ONE repository: the
// plan document publishes the shipped (unguarded) argv with the base REF name,
// and the guarded execution of that same row issues the identical argv with
// the --onto operand replaced by the resolved full SHA.
func TestSyncPlanIntegration_GuardedRunPinsOntoDestination(t *testing.T) {
	f := newScopedFixture(t)
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	// Record a cutoff that differs from the current base tip, which is exactly
	// the shipped amend-aware `--onto <base> <LastBaseSHA>` arm.
	internal.UpdateBaseSHA(&stack, "parent", f.sha(t, "root"))
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}
	f.advanceRoot(t)

	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("plan exit=%d stderr=%q", exit, stderr)
	}
	doc := planDoc(t, stdout)
	plannedArgv := map[string][]string{}
	for _, raw := range doc["entries"].([]any) {
		row := raw.(map[string]any)
		argv, ok := row["argv"].([]any)
		if !ok {
			continue
		}
		tokens := make([]string, 0, len(argv))
		for _, a := range argv {
			tokens = append(tokens, a.(string))
		}
		plannedArgv[row["name"].(string)] = tokens
	}
	planned, ok := plannedArgv["parent"]
	if !ok {
		t.Fatalf("plan published no argv for parent: %v", plannedArgv)
	}
	ontoIdx := -1
	for i, tok := range planned {
		if tok == "--onto" && i+1 < len(planned) {
			ontoIdx = i + 1
		}
	}
	if ontoIdx < 0 {
		t.Fatalf("fixture produced no --onto arm; planned argv = %v", planned)
	}
	if isFullHexSHA(planned[ontoIdx]) {
		t.Fatalf("the published (unguarded) argv must carry the base ref name, got %q", planned[ontoIdx])
	}

	w := newSyncGitWrapper(t, false)
	w.around(t, func() {
		_, stderr, exit = runSyncExecute(t, f.feature, "--no-fetch", "--max-replay-total", "999")
	})
	if exit != 0 {
		t.Fatalf("guarded sync must succeed: exit=%d stderr=%q", exit, stderr)
	}
	var executed []string
	for _, r := range w.records(t) {
		if r.Verb != "rebase" {
			continue
		}
		tail := r.Tail()
		for i, tok := range tail {
			if tok == "--onto" && i+1 < len(tail) && len(tail) == len(planned) {
				executed = tail
			}
		}
	}
	if executed == nil {
		t.Fatal("guarded run issued no --onto rebase to compare")
	}
	assertMatchedRebaseArgv(t, 0, planned, executed)
}

// ---------------------------------------------------------------------------
// External pass 2 (archived row): an Archived entry with a stored Repo (the
// "entry-repo" execution-context cell — see setupCustomerTopologyExternal's
// sibling comment on why an empty Repo can never reach this strategy) runs a
// plain explicit-branch rebase (no --onto) and never consults its cutoff.
// ---------------------------------------------------------------------------

func TestSyncPlanIntegration_ExternalPass2ArchivedRow(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	gitRun(t, repo, "checkout", "-b", "legacy", "master")
	writeAndCommit(t, repo, "legacy.txt", "legacy\n", "legacy work")
	gitRun(t, repo, "checkout", "master")

	featurePath := internal.FeaturePath("feature")
	if err := os.MkdirAll(featurePath, 0755); err != nil {
		t.Fatal(err)
	}
	// Repo must be a real absolute path, not "": EntryContexts leaves
	// ExecutionDir set verbatim from the raw input (never updated to the
	// identity's own measured repo root), so an empty Repo always yields
	// ExecutionDir=="" and therefore contextUsable=false (prepareEntry,
	// internal/rebase_plan_build.go) — collapsing strategy to "unknown"
	// before RebaseStrategy ever reaches its pass-2 branch. Only a non-empty
	// Repo (the "entry-repo" execution-context cell) can ever reach
	// "plain-explicit-branch".
	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "legacy", Base: "master", Repo: repo, Archived: true},
	}}
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runSyncExecute(t, "feature", "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	doc := planDoc(t, stdout)
	entries, ok := doc["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("expected exactly one entry, got %#v", doc["entries"])
	}
	entry := entries[0].(map[string]any)

	if got := entry["materialization"]; got != "archived-metadata" {
		t.Fatalf("materialization = %v, want archived-metadata", got)
	}
	if got := entry["strategy"]; got != "plain-explicit-branch" {
		t.Fatalf("strategy = %v, want plain-explicit-branch", got)
	}
	execCtx := entry["execution_context"].(map[string]any)
	if got := execCtx["source"]; got != "entry-repo" {
		t.Fatalf("execution_context.source = %v, want entry-repo (non-empty Repo)", got)
	}
	cutoff := entry["cutoff"].(map[string]any)
	if got := cutoff["usage"]; got != "not_used" {
		t.Fatalf("cutoff.usage = %v, want not_used (pass 2 never consults the cutoff)", got)
	}
	if argv, ok := entry["argv"].([]any); ok {
		for _, a := range argv {
			if s, _ := a.(string); s == "--onto" {
				t.Fatalf("pass-2 argv must never carry --onto: %v", argv)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Checkout fresh and --continue plan routes.
// ---------------------------------------------------------------------------

func TestSyncPlanIntegration_CheckoutFreshPlanRoute(t *testing.T) {
	dir, _ := checkoutModeFixture(t)
	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)

	stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	doc := planDoc(t, stdout)
	if got := doc["workspace"].(map[string]any)["mode"]; got != "checkout" {
		t.Fatalf("workspace.mode = %v, want checkout", got)
	}
	entries, ok := doc["entries"].([]any)
	if !ok || len(entries) != 3 {
		t.Fatalf("a fresh checkout plan must describe all three stack entries, got %#v", doc["entries"])
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	for _, want := range []string{"feat-root", "feat-a", "feat-b"} {
		if !slices.Contains(names, want) {
			t.Fatalf("entries = %v, missing %q", names, want)
		}
	}
}

// TestSyncPlanIntegration_CheckoutContinuePlanRoute interrupts a real
// checkout sync after its first entry (feat-root) fully completes
// (internal.StepHook always receives tx.CurrentIndex, which only advances
// once a full per-entry cycle finishes — internal/checkout_sync.go's
// processBranch — so the first StepHook callback observing branchIndex==1
// is necessarily feat-a's own StagePlanned, called only after feat-root's
// CurrentIndex increment), confirms a transaction persists, then drives
// --plan --continue through production cli.Execute() and asserts it never
// fetches and describes ONLY the still-pending entries (buildEntries
// filters req.Remaining under req.Continue, internal/rebase_plan_build.go).
func TestSyncPlanIntegration_CheckoutContinuePlanRoute(t *testing.T) {
	dir, fp := checkoutModeFixture(t)

	clearStepHook(t)
	internal.StepHook = func(stage internal.CheckoutStage, branchIndex int) error {
		if branchIndex == 1 {
			return errStop
		}
		return nil
	}
	policy := internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll}
	_, err := captureRun(t, func() error {
		return internal.RunCheckoutSync(newModeOpts(dir, fp, policy))
	})
	clearStepHook(t)
	if err == nil {
		t.Fatal("expected the step hook to interrupt the run before it completed")
	}
	if !internal.HasCheckoutTransaction(fp) {
		t.Fatal("expected a persisted checkout transaction after the interruption")
	}

	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)

	w := newSyncGitWrapper(t, false)
	var stdout, stderr string
	var exit int
	w.around(t, func() {
		stdout, stderr, exit = runSyncExecute(t, "test-feature", "--plan", "--continue", "--json")
	})
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	for _, r := range w.records(t) {
		if r.Verb == "fetch" {
			t.Fatalf("--plan --continue must never fetch, but saw: %+v", r)
		}
	}
	doc := planDoc(t, stdout)
	if got := planField(t, doc, "fetch", "attempted"); got != false {
		t.Fatalf("fetch.attempted = %v, want false", got)
	}
	entries, ok := doc["entries"].([]any)
	if !ok {
		t.Fatalf("expected an entries array, got %#v", doc["entries"])
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	if len(names) == 0 || len(names) >= 3 {
		t.Fatalf("--continue must describe remaining work only (fewer than all 3 entries), got %v", names)
	}
	if slices.Contains(names, "feat-root") {
		t.Fatalf("feat-root already completed before the interruption and must not be re-described: %v", names)
	}
}

// ---------------------------------------------------------------------------
// The plan-guard marker reaches stderr through production cli.Execute() in
// both external and checkout modes. Checkout mode's real (non--plan)
// execution now wires internal.EvaluatePlanGuard into RunCheckoutSync (a
// guard seam after the plan is built and before SaveCheckoutTransaction) and
// ContinueCheckoutSync (a guard seam after forceAcquireCheckoutLock and
// before resumeTransaction), and internal.RevalidatePlanGuardEntry into
// processBranch's re-resolution point and resumeFromSwitched — mirroring, in
// package internal (which may never import internal/cli), the external
// route's own buildGuardedExternalPlan/planGuardRun seams of sync.go and
// sync_helpers.go — so a checkout-mode marker now reaches stderr through a
// real execution exactly as the external one already does.
// ---------------------------------------------------------------------------

func TestSyncPlanIntegration_MarkerReachesStderrViaExecute(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	_, stderr, exit := runSyncExecute(t, f.feature, "--no-fetch", "--max-replay-total", "0")
	if exit != 1 {
		t.Fatalf("a tripped limit must refuse with exit 1, got %d (stderr=%q)", exit, stderr)
	}
	assertExactlyOnePlanGuardMarker(t, stderr)
}

// TestSyncPlanIntegration_CheckoutMarkerReachesStderrViaExecute is the
// checkout-mode counterpart of TestSyncPlanIntegration_MarkerReachesStderrViaExecute
// above: --plan --json correctly COMPUTES would_refuse:true against an armed
// limit (the shared guard.evaluation machinery, internal/rebase_plan_build.go's
// guardEvaluationRows, is mode-agnostic and runs for checkout too), and a
// real, executing run against the identical armed limit is now ALSO refused
// before any branch moves, printing exactly one "plan-guard: " marker line —
// because internal/checkout_sync.go's fresh-run dispatch now calls
// internal.EvaluatePlanGuard after the plan is built and before
// SaveCheckoutTransaction, the guard seam spec.md's file table for
// internal/checkout_sync.go always called for.
func TestSyncPlanIntegration_CheckoutMarkerReachesStderrViaExecute(t *testing.T) {
	dir, _ := checkoutModeFixture(t)
	// checkoutModeFixture leaves HEAD on "main", which collides with
	// feat-root's own configured base ("main") under ResolveSyncBase's
	// mode-unaware default-branch rewrite (see setupCustomerTopologyCheckout's
	// doc comment): with no remote configured,
	// internal.DefaultBranchIn falls back to the checked-out branch, and
	// entry.Base == that fallback wrongly triggers the same rewrite external
	// mode applies against a real remote. Checking out a different branch
	// first keeps feat-root's base resolution literal, so its own replay
	// candidate count is real or countable rather than unmeasured.
	gitRunCS(t, dir, "checkout", "feat-b")
	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)

	planStdout, planStderr, planExit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch", "--max-replay-total", "0")
	if planExit != 0 {
		t.Fatalf("--plan always exits 0 regardless of would_refuse: exit=%d stderr=%q", planExit, planStderr)
	}
	doc := planDoc(t, planStdout)
	guard := doc["guard"].(map[string]any)
	if got := guard["would_refuse"]; got != true {
		t.Fatalf("guard.would_refuse = %v, want true: the describe-side evaluation is armed and exceeded (limit 0)", got)
	}
	evaluation, ok := guard["evaluation"].([]any)
	if !ok || len(evaluation) == 0 {
		t.Fatalf("guard.evaluation[] must be non-empty when a limit is armed, got %#v", guard["evaluation"])
	}

	stdout, stderr, exit := runSyncExecute(t, "test-feature", "--no-fetch", "--max-replay-total", "0")
	if exit != 1 {
		t.Fatalf("a tripped limit must refuse with exit 1, got %d (stdout=%q stderr=%q)", exit, stdout, stderr)
	}
	assertExactlyOnePlanGuardMarker(t, stderr)
	if strings.Contains(stdout, "Checkout sync complete.") {
		t.Fatalf("a refused guarded run must never print completion, got stdout:\n%s", stdout)
	}
}

// assertExactlyOnePlanGuardMarker asserts stderr carries exactly one line of
// the form "plan-guard: <kind>: <detail>" (spec.md §6.4).
func assertExactlyOnePlanGuardMarker(t *testing.T, stderr string) {
	t.Helper()
	count := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "plan-guard: ") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one plan-guard marker line, found %d in stderr:\n%s", count, stderr)
	}
}

// ===========================================================================
// §22.33g — planner declarations, per-invocation TopoSort counts, and
// wrapper-direction source assertions.
// ===========================================================================

// TestSyncPlanIntegration_RebasePlanRequestAllFieldsExplicit constructs a
// RebasePlanRequest with EVERY exported field written explicitly, then
// asserts by reflection that the constructed value has no field left at its
// zero value (§22.33g (i)). The point is not the values: it is that a member
// added to the shared request type without updating this construction site
// fails the build (a missing key is a compile error only for the keyed
// literal below if the field is removed; a NEW field is caught by the
// reflection sweep, which is why both halves exist).
func TestSyncPlanIntegration_RebasePlanRequestAllFieldsExplicit(t *testing.T) {
	i := 1
	s := "x"
	b := true
	stack := internal.Stack{Branches: []internal.StackEntry{{Name: "root", Base: "master"}}}
	req := internal.RebasePlanRequest{
		// 1 — identity and boundary
		Layout:    internal.RebasePlanLayout{FeaturePath: "/fp", WorktreesRoot: "/fp/worktrees", RepoRoot: "/repo"},
		Mode:      internal.ModeExternal,
		Feature:   "feature",
		Workspace: internal.PlanWorkspace{Mode: string(internal.ModeExternal), StableID: &s, RepoRoot: "/repo"},

		// 2 — the effective run
		Route:                     internal.RouteLegacy,
		RequestedRoute:            internal.RouteNewMode,
		RouteTriggers:             []string{"only"},
		Invocation:                "plan-only",
		Policy:                    internal.SyncRunPolicy{Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll, Selector: "root"},
		PolicyFetchDefaultApplied: true,
		Push:                      true,
		PushSource:                "flag",
		Guard:                     internal.CheckoutPlanGuard{MaxPerEntry: &i, MaxTotal: &i, Approve: "tok", Plan: true, PersistedGuarded: true},
		Limits:                    internal.PlanGuardLimits{PerEntry: internal.PlanGuardLimit{Value: &i, Origin: "flag"}, Total: internal.PlanGuardLimit{Value: &i, Origin: "flag"}},
		LimitConflicts:            []internal.PlanGuardLimitConflict{{Key: "max_replay_total", EffectiveValue: &i, EffectiveOrigin: "persisted-payload", SuppliedValue: &i}},
		Validation:                internal.PlanValidationIdentity{Applies: true, Command: "make test", Source: "config", Digest: "d"},
		Approve:                   "tok",

		// 3 — the subject
		Stack:             &stack,
		Order:             stack.Branches,
		SortErr:           errSentinelForRequestSweep,
		StackErr:          errSentinelForRequestSweep,
		Selection:         internal.SyncSelection{Names: map[string]bool{"root": true}},
		SelectionResolved: true,
		SelectionErr:      errSentinelForRequestSweep,
		RowsAvailable:     true,

		// 4 — continuation inputs
		Continue:         true,
		Remaining:        []string{"root"},
		StageFacts:       []internal.PlanStageFact{{Stage: "rebasing"}},
		Changed:          map[string]bool{"only": true},
		ContinuationGate: internal.PlanContinuationGate{Applies: true, Failed: true, Axis: "scope", Detail: "d"},

		// 5 — what this invocation already measured
		Fetch:         internal.PlanFetchOutcome{Applies: true, Attempted: true},
		FetchPlan:     internal.PlanFetchPlan{Applies: true},
		PushFacts:     internal.PlanPushFacts{Applies: b},
		BasePreflight: internal.PlanBasePreflight{Applies: true, Failed: true, Entry: "root", Ref: "master", Detail: "d"},
		Version:       internal.GitVersion{Probed: true, OK: true, Major: 2, Minor: 39},
		Capabilities:  internal.GitCapabilitiesForVersion(internal.GitVersion{Probed: true, OK: true, Major: 2, Minor: 39}),
		ExternalState: internal.ExternalPlanState{Applicable: true, Cell: 5},
		CheckoutState: internal.CheckoutPlanState{Applicable: true},
		Gates:         []internal.PlanGateResult{{ID: "g", Applies: true}},
	}

	v := reflect.ValueOf(req)
	typ := v.Type()
	var zero []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		if v.Field(i).IsZero() {
			zero = append(zero, f.Name)
		}
	}
	if len(zero) != 0 {
		t.Fatalf("RebasePlanRequest fields left at their zero value by an explicit all-field construction: %v\n"+
			"every member of the shared request type must be written explicitly here, so a new member cannot be added without a decision about it", zero)
	}
}

var errSentinelForRequestSweep = errors.New("sentinel: an explicitly-set error member")

// TestSyncPlanIntegration_TopoSortWrapperDirection asserts the §22.33g (v)
// wrapper-direction rule at the SOURCE level: the sorting wrapper calls the
// order-taking body, never the reverse, and the order-taking bodies never
// call TopoSort at all.
func TestSyncPlanIntegration_TopoSortWrapperDirection(t *testing.T) {
	checkoutSrc := readInternalSource(t, "checkout_sync.go")
	selectionSrc := readInternalSource(t, "sync_selection.go")
	plannerSrc := readInternalSource(t, "rebase_planner.go")
	buildSrc := readInternalSource(t, "rebase_plan_build.go")
	guardSrc := readInternalSource(t, "rebase_plan_guard.go")
	cliPlanSrc := readCliSource(t, "sync_plan_guard.go")
	cliSyncSrc := readCliSource(t, "sync.go")

	// Direction: the wrapper sorts and delegates.
	assertFuncBodyContains(t, checkoutSrc, "BuildCheckoutPlan", "TopoSort(stack)")
	assertFuncBodyContains(t, checkoutSrc, "BuildCheckoutPlan", "buildCheckoutPlanFrom(")
	assertFuncBodyContains(t, selectionSrc, "ResolveSyncSelection", "TopoSort(stack)")
	assertFuncBodyContains(t, selectionSrc, "ResolveSyncSelection", "ResolveSyncSelectionFromOrder(")
	assertFuncBodyContains(t, cliSyncSrc, "scopedSelectionFromPayload", "scopedSelectionFromPayloadOrder(")

	// Direction: the order-taking bodies never sort.
	mustNotSort := []struct {
		src, fn string
	}{
		{checkoutSrc, "buildCheckoutPlanFrom"},
		{selectionSrc, "ResolveSyncSelectionFromOrder"},
		{plannerSrc, "ExecutionOrder"},
		{plannerSrc, "RemainingRebaseEntries"},
		{plannerSrc, "PushTargets"},
		{plannerSrc, "ResolvePushContext"},
		{plannerSrc, "DestinationDeferred"},
		{buildSrc, "BuildRebasePlan"},
		{cliSyncSrc, "syncFeatureScopedPlanned"},
		{cliSyncSrc, "handleGuardedScopedSyncContinue"},
		{cliSyncSrc, "handleGuardedLegacySyncContinue"},
		{cliSyncSrc, "runGuardedScopedSync"},
		{cliSyncSrc, "runGuardedLegacySync"},
		{cliSyncSrc, "resumeGuardedLegacySentinel"},
		{cliPlanSrc, "buildExternalPlanFrom"},
		{cliPlanSrc, "buildGuardedExternalPlan"},
		{cliSyncSrc, "scopedSelectionFromPayloadOrder"},
	}
	for _, c := range mustNotSort {
		body := funcBody(t, c.src, c.fn)
		if strings.Contains(body, "TopoSort(") {
			t.Errorf("%s MUST NOT call TopoSort (§9.1a rule 3), but its body does", c.fn)
		}
	}

	// The one owner per controlled route really does sort.
	assertFuncBodyContains(t, cliPlanSrc, "InspectExternalPlan", "TopoSort(stack)")
	assertFuncBodyContains(t, guardSrc, "InspectCheckoutPlan", "TopoSort(stack)")
}

// readInternalSource / readCliSource read a package source file relative to
// this test's own package directory.
func readInternalSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatalf("read internal/%s: %v", name, err)
	}
	return string(data)
}

func readCliSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read internal/cli/%s: %v", name, err)
	}
	return string(data)
}

// funcBody returns the source text of the named top-level function, from its
// `func <name>(` line to the closing brace in column 0.
func funcBody(t *testing.T, src, name string) string {
	t.Helper()
	marker := "\nfunc " + name + "("
	idx := strings.Index(src, marker)
	if idx < 0 {
		// Method or receiver form: `func (b *planBuilder) name(`.
		re := regexp.MustCompile(`\nfunc \([^)]*\) ` + regexp.QuoteMeta(name) + `\(`)
		loc := re.FindStringIndex(src)
		if loc == nil {
			t.Fatalf("function %q not found in source", name)
		}
		idx = loc[0]
	}
	rest := src[idx+1:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of function %q", name)
	}
	return rest[:end]
}

func assertFuncBodyContains(t *testing.T, src, name, needle string) {
	t.Helper()
	if !strings.Contains(funcBody(t, src, name), needle) {
		t.Errorf("%s's body must contain %q", name, needle)
	}
}

// ---------------------------------------------------------------------------
// §22.33g (iii)/(iv) — the PER-INVOCATION TopoSort counter.
//
// The count must be observed per invocation, not estimated from static call
// sites, and internal/stack.go's TopoSort is frozen byte-for-byte, so no
// production hook may be added to it. The mechanism is therefore the Go
// toolchain's own statement-count coverage: an out-of-process child runs
// exactly ONE named route under `-covermode=count -coverpkg=<internal>`, and
// the parent reads the execution count of TopoSort's entry block out of the
// resulting profile. One child per row of the §10.1 ledger gives the real
// per-invocation number for both the controlled and the unguarded twin.
// ---------------------------------------------------------------------------

// topoSortRouteEnv is the child-only discriminator. Its presence turns
// TestSyncPlanIntegrationTopoSortChild from a no-op into the single route
// runner; its absence prevents recursion when the parent's own `go test`
// invocation re-runs this package.
const topoSortRouteEnv = "TWS_TOPOSORT_ROUTE"

// TestSyncPlanIntegrationTopoSortChild is the child fixture. It is a no-op in
// an ordinary run: only the parent's `go test` sub-invocation sets
// topoSortRouteEnv, and it then drives exactly one §10.1 route to completion
// (or to its refusal) so the coverage counters describe ONE invocation.
func TestSyncPlanIntegrationTopoSortChild(t *testing.T) {
	route := os.Getenv(topoSortRouteEnv)
	if route == "" {
		t.Skip("child-only fixture: not selected by " + topoSortRouteEnv)
	}
	runTopoSortRoute(t, route)
}

// runTopoSortRoute drives exactly one route of the §10.1 ledger.
func runTopoSortRoute(t *testing.T, route string) {
	t.Helper()
	switch route {
	case "external-legacy-fresh-unguarded":
		// No trigger flag at all: this is the frozen no-flag legacy route.
		f := newScopedFixture(t)
		f.advanceRoot(t)
		runSyncExecute(t, f.feature)
	case "external-legacy-fresh-controlled":
		// A replay limit is NOT a trigger flag (§3.5), so this stays the
		// legacy route — armed.
		f := newScopedFixture(t)
		f.advanceRoot(t)
		runSyncExecute(t, f.feature, "--max-replay-total", "50")
	case "external-newmode-fresh-unguarded":
		f := newScopedFixture(t)
		f.advanceRoot(t)
		runSyncExecute(t, f.feature, "--no-fetch", "--full")
	case "external-newmode-fresh-controlled":
		f := newScopedFixture(t)
		f.advanceRoot(t)
		runSyncExecute(t, f.feature, "--no-fetch", "--full", "--max-replay-total", "50")
	case "external-plan-only-controlled":
		f := newScopedFixture(t)
		f.advanceRoot(t)
		runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
	case "checkout-legacy-fresh-unguarded":
		// No trigger flag: the frozen checkout legacy arm.
		dir, _ := checkoutModeFixture(t)
		gitRunCS(t, dir, "checkout", "feat-b")
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		runSyncExecute(t, "test-feature")
	case "checkout-newmode-fresh-unguarded":
		dir, _ := checkoutModeFixture(t)
		gitRunCS(t, dir, "checkout", "feat-b")
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		runSyncExecute(t, "test-feature", "--no-fetch")
	// A continuation needs a failed run to resume, and the coverage counter
	// is process-wide, so every continuation row is measured as the DELTA
	// between a route that performs setup PLUS the continuation and a
	// "-setup" route that performs the byte-identical setup and stops. The
	// two children run the same deterministic commands, so the difference is
	// exactly the continuation invocation's own count.
	case "external-scoped-conflict-setup":
		newScopedContinuationFixture(t, "--only", "child")
	case "external-scoped-continue-unguarded":
		// A new-mode (scoped) continuation with no control flag: the shipped
		// unguarded resume path, which sorts twice.
		f := newScopedContinuationFixture(t, "--only", "child")
		runSyncExecute(t, f.feature, "--continue")
	case "external-scoped-continue-controlled":
		// The SAME continuation, armed: the controlled route consumes the one
		// inspection's order and sorts exactly once.
		f := newScopedContinuationFixture(t, "--only", "child")
		runSyncExecute(t, f.feature, "--continue", "--max-replay-total", "50")
	case "external-legacy-conflict-setup":
		newScopedContinuationFixture(t)
	case "external-legacy-continue-controlled":
		// A LEGACY continuation (no trigger flag at conflict time, a replay
		// limit on the resume — a limit is not a trigger flag, §3.5).
		f := newScopedContinuationFixture(t)
		runSyncExecute(t, f.feature, "--continue", "--max-replay-total", "50")
	case "checkout-continue-controlled":
		// A checkout continuation sorts ZERO times: the persisted
		// transaction IS the run (§13.3, §13.7 rule 2).
		dir, _ := checkoutModeFixture(t)
		gitRunCS(t, dir, "checkout", "feat-b")
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		runSyncExecute(t, "test-feature", "--continue", "--max-replay-total", "50")
	case "checkout-fresh-controlled":
		dir, _ := checkoutModeFixture(t)
		gitRunCS(t, dir, "checkout", "feat-b")
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		runSyncExecute(t, "test-feature", "--no-fetch", "--max-replay-total", "50")
	case "checkout-plan-only-controlled":
		dir, _ := checkoutModeFixture(t)
		gitRunCS(t, dir, "checkout", "feat-b")
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
	default:
		t.Fatalf("unknown route %q", route)
	}
}

// newScopedContinuationFixture builds the external continuation subject every
// continuation row of the ledger resumes: a real conflicting run under
// setupArgs, its guard detached to the dead-PID convention so a new process
// owns the resume, and the conflict really resolved in the worktree. It
// performs no continuation of its own, so a "-setup" route and its
// continuation twin share a byte-identical prefix.
func newScopedContinuationFixture(t *testing.T, setupArgs ...string) *scopedFixture {
	t.Helper()
	f := newScopedFixture(t)
	f.makeConflict(t)
	args := append([]string{f.feature}, setupArgs...)
	if _, _, exit := runSync(t, args...); exit == 0 {
		t.Fatal("the setup run must conflict, or there is nothing to continue")
	}
	// A new-mode (scoped) setup claims .sync-run.lock and the guard survives
	// its failure by design; the frozen legacy route claims none at all, so
	// the detach is conditional on the artefact really being there.
	if _, err := os.Lstat(internal.SyncRunGuardPath(f.featurePath)); err == nil {
		f.detachGuard(t)
	}
	resolveRebase(t, f.wt("child"))
	return f
}

// TestSyncPlanIntegration_TopoSortPerInvocationCounts is §22.33g (iii)/(iv):
// one child per §10.1 row — FRESH and CONTINUATION, controlled and unguarded,
// in both workspace modes — each asserting the invocation's real TopoSort
// count. Controlled routes sort exactly ONCE; the unguarded twins keep their
// shipped counts (2 on the new-mode arms, 1 on the legacy/checkout-legacy
// arms), which this feature deliberately does not "improve"; and a checkout
// continuation sorts ZERO times, because the persisted transaction IS the
// run. The continuation rows are the ones a fresh-only table cannot reach:
// they are where the controlled route's single inspection has to replace two
// shipped sorts rather than one.
func TestSyncPlanIntegration_TopoSortPerInvocationCounts(t *testing.T) {
	if os.Getenv(topoSortRouteEnv) != "" {
		t.Skip("running as the coverage child")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("the per-invocation TopoSort counter needs the go toolchain on PATH: %v", err)
	}

	cases := []struct {
		route string
		// setup, when non-empty, names the route that performs this row's
		// byte-identical fixture setup and stops. The assertion is then on
		// the DELTA, which is the measured invocation's own count.
		setup string
		want  int
	}{
		{route: "external-legacy-fresh-unguarded", want: 1},
		{route: "external-legacy-fresh-controlled", want: 1},
		{route: "external-newmode-fresh-unguarded", want: 2},
		{route: "external-newmode-fresh-controlled", want: 1},
		{route: "external-plan-only-controlled", want: 1},
		{route: "checkout-legacy-fresh-unguarded", want: 1},
		{route: "checkout-newmode-fresh-unguarded", want: 2},
		{route: "checkout-fresh-controlled", want: 1},
		{route: "checkout-plan-only-controlled", want: 1},
		{route: "checkout-continue-controlled", want: 0},
		{route: "external-scoped-continue-unguarded", setup: "external-scoped-conflict-setup", want: 2},
		{route: "external-scoped-continue-controlled", setup: "external-scoped-conflict-setup", want: 1},
		{route: "external-legacy-continue-controlled", setup: "external-legacy-conflict-setup", want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.route, func(t *testing.T) {
			got := topoSortCountForRoute(t, goBin, tc.route)
			if tc.setup != "" {
				setupCount := topoSortCountForRoute(t, goBin, tc.setup)
				if got < setupCount {
					t.Fatalf("route %s counted %d sorts, fewer than its own setup route %s (%d): the two prefixes are no longer identical",
						tc.route, got, tc.setup, setupCount)
				}
				got -= setupCount
			}
			if got != tc.want {
				t.Fatalf("route %s performed %d TopoSort call(s) in one invocation, want %d", tc.route, got, tc.want)
			}
		})
	}
}

// topoSortCountForRoute runs one child under statement-count coverage and
// returns the execution count of TopoSort's entry block.
func topoSortCountForRoute(t *testing.T, goBin, route string) int {
	t.Helper()
	profile := filepath.Join(t.TempDir(), "cover.out")
	cmd := exec.Command(goBin, "test",
		"-run", "^TestSyncPlanIntegrationTopoSortChild$",
		"-covermode=count",
		"-coverpkg=github.com/jdbencardinop/tesseraworkspaces/internal",
		"-coverprofile="+profile,
		"-count=1",
		"github.com/jdbencardinop/tesseraworkspaces/internal/cli",
	)
	cmd.Env = append(os.Environ(), topoSortRouteEnv+"="+route)
	// The parent's own environment may carry command-scope git config that
	// breaks unrelated fixtures; the child inherits only what it needs.
	cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("coverage child for %s failed: %v\n%s", route, err, out)
	}
	return topoSortEntryBlockCount(t, profile)
}

// topoSortEntryBlockCount parses a Go coverage profile and returns the
// execution count of TopoSort's FIRST block in internal/stack.go. The
// function's line range is derived from the source itself, so the assertion
// survives an unrelated edit above or below it.
func topoSortEntryBlockCount(t *testing.T, profile string) int {
	t.Helper()
	src := readInternalSource(t, "stack.go")
	lines := strings.Split(src, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "func TopoSort(") {
			start = i + 1 // 1-based
			break
		}
	}
	if start < 0 {
		t.Fatal("internal/stack.go no longer declares func TopoSort(")
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if lines[i] == "}" {
			end = i + 1
			break
		}
	}

	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatalf("read coverage profile: %v", err)
	}
	blockRe := regexp.MustCompile(`^(\S+):(\d+)\.\d+,(\d+)\.\d+ \d+ (\d+)$`)
	best, bestLine, found := 0, 1<<30, false
	for _, line := range strings.Split(string(data), "\n") {
		m := blockRe.FindStringSubmatch(line)
		if m == nil || !strings.HasSuffix(m[1], "/internal/stack.go") {
			continue
		}
		s, _ := strconv.Atoi(m[2])
		e, _ := strconv.Atoi(m[3])
		if s < start || e > end {
			continue
		}
		c, _ := strconv.Atoi(m[4])
		if s < bestLine {
			bestLine, best, found = s, c, true
		}
	}
	if !found {
		// The whole package was instrumented but TopoSort's block never
		// appeared, which the toolchain does not do; a genuinely
		// zero-execution block still appears with count 0.
		t.Fatalf("no coverage block found inside TopoSort's line range [%d,%d]", start, end)
	}
	return best
}

// ---------------------------------------------------------------------------
// §22.32a/§22.32b/§16.3/§23.2: git-version capability probing through
// Execute(), the argv-derived --update-refs biconditional, and process-
// ceiling/config-budget/holder-count assertions — all driven through
// production cli.Execute() (runSyncExecute, sync_plan_guard_test.go) and,
// where a raw child-process trace is needed, the sync_golden_test.go argv
// sidecar (newSyncGitWrapper/around/records) — never a second wrapper.
// ---------------------------------------------------------------------------

// newGitVersionStubPATH builds a directory containing an executable "git"
// script that answers `git --version` with versionLine and otherwise execs
// realGit with the original argv — so a stubbed run still performs every
// real git operation the plan pipeline needs end to end; only its
// self-reported version differs. Callers MUST resolve realGit via
// exec.LookPath("git") ONCE, before any stub directory is ever prepended to
// PATH: resolving it later, after a first stub is already installed, would
// capture the STUB's own path instead of the real binary, and a second
// stub's "fallback" would then cascade into the first stub rather than into
// real git.
func newGitVersionStubPATH(t *testing.T, realGit, versionLine string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo " + strconv.Quote(versionLine) + "\n" +
		"  exit 0\n" +
		"fi\n" +
		"exec " + strconv.Quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write git version stub: %v", err)
	}
	return dir
}

// versionProbeIndices returns, in order, the indices within records at which
// a raw `git --version` invocation occurred. A bare `--version` call's own
// Tail()/Verb are both empty — sync_golden_test.go's Tail() treats every
// leading "-"-prefixed token (including "--version" itself) as a skipped
// global-option form and never reaches a "started" token — so detecting a
// probe requires scanning raw Argv for the literal token, the same idiom
// checkout_sync_modes_test.go's own checkoutSawGitVersionProbe already uses.
func versionProbeIndices(records []gitRecord) []int {
	var idx []int
	for i, r := range records {
		for _, a := range r.Argv {
			if a == "--version" {
				idx = append(idx, i)
				break
			}
		}
	}
	return idx
}

// recordExecContext returns the effective directory a git child process
// targeted: the value of a leading "-C <dir>" argv pair when present, else
// the wrapper's recorded Cwd. Cwd is the TEST PROCESS's fixed OS-level
// working directory (set once by the fixture's own os.Chdir, and never
// touched again for the rest of the run) — every entry/worktree-scoped call
// this tree issues in external mode instead carries an explicit "-C <dir>"
// naming its own execution context, so Cwd alone can never distinguish one
// execution context from another; it is only the correct fallback for the
// handful of calls issued with no -C at all (the bare `--version` probe, and
// workspace/layout discovery's very first rev-parse).
func recordExecContext(r gitRecord) string {
	if len(r.Argv) >= 2 && r.Argv[0] == "-C" {
		return r.Argv[1]
	}
	return r.Cwd
}

// / TestSyncPlanIntegration_RebaseUpdateRefsCapabilityGate drives §16 rule 3b:
// the 2.38 CapRebaseUpdateRefs gate is ARGV-DERIVED, so it is a precondition
// of exactly the rows whose OWN published argv carries --update-refs, and
// never a scope-wide one.
//
// The observables are the three §11.7/§11.8/§16 places the gate is required
// to surface, all read out of the production --plan --json document:
//   - blockers[]: a gated row raises rank 5.9 probe-failed, naming that row;
//   - entries[].effective_backend: null on a gated row (§11.7 row 2 — the
//     inventory was never reached), never a row-1 `merge` verdict from an
//     argv the host would reject;
//   - entries[].collateral_refs / collateral_mechanism: null on a gated row
//     (§11.8's "the row publishes the null collateral facts").
//
// The argv itself is deliberately UNCHANGED across the gate: the row still
// publishes the command the executor would build, and the gate refuses it
// rather than silently rewriting it into a different rebase.
//
// A checkout row never carries --update-refs, so no checkout row is ever
// gated — asserted here as the negative control, in both stubs.
func TestSyncPlanIntegration_RebaseUpdateRefsCapabilityGate(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("lookup real git: %v", err)
	}
	origPath := os.Getenv("PATH")

	probeUnderStub := func(t *testing.T, versionLine string) (internal.GitVersion, internal.GitCapabilities) {
		t.Helper()
		stubDir := newGitVersionStubPATH(t, realGit, versionLine)
		t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)
		v, err := internal.ProbeGitVersion()
		if err != nil {
			t.Fatalf("ProbeGitVersion under stub %q: %v", versionLine, err)
		}
		return v, internal.GitCapabilitiesForVersion(v)
	}

	planUnderStub := func(t *testing.T, versionLine string, args ...string) map[string]any {
		t.Helper()
		stubDir := newGitVersionStubPATH(t, realGit, versionLine)
		t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)
		stdout, stderr, exit := runSyncExecute(t, args...)
		if exit != 0 {
			t.Fatalf("runSyncExecute(%v) under stub %q: exit=%d stderr=%q", args, versionLine, exit, stderr)
		}
		return planDoc(t, stdout)
	}

	argvHasUpdateRefs := func(argv []any) bool {
		for _, a := range argv {
			if v, _ := a.(string); v == "--update-refs" {
				return true
			}
		}
		return false
	}

	t.Run("CapabilityIsDerivedFromTheProbedVersion", func(t *testing.T) {
		v37, caps37 := probeUnderStub(t, "git version 2.37.0")
		if !v37.OK || v37.Major != 2 || v37.Minor != 37 {
			t.Fatalf("2.37.0 stub: probed version = %+v, want OK with Major=2 Minor=37", v37)
		}
		if caps37.CapRebaseUpdateRefs {
			t.Fatal("2.37.0 stub: CapRebaseUpdateRefs = true, want false (below the 2.38 gate)")
		}
		v38, caps38 := probeUnderStub(t, "git version 2.38.0")
		if !v38.OK || v38.Major != 2 || v38.Minor != 38 {
			t.Fatalf("2.38.0 stub: probed version = %+v, want OK with Major=2 Minor=38", v38)
		}
		if !caps38.CapRebaseUpdateRefs {
			t.Fatal("2.38.0 stub: CapRebaseUpdateRefs = false, want true (at the gate)")
		}
	})

	t.Run("ExternalUpdateRefsRowIsGatedBelow238", func(t *testing.T) {
		setupCustomerTopologyExternal(t)

		doc37 := planUnderStub(t, "git version 2.37.0", "feature", "--plan", "--json", "--no-fetch")
		doc38 := planUnderStub(t, "git version 2.38.0", "feature", "--plan", "--json", "--no-fetch")

		rows37, _ := doc37["entries"].([]any)
		rows38, _ := doc38["entries"].([]any)
		if len(rows37) != 1 || len(rows38) != 1 {
			t.Fatalf("expected exactly one entry under both stubs, got %d/%d", len(rows37), len(rows38))
		}
		row37 := rows37[0].(map[string]any)
		row38 := rows38[0].(map[string]any)

		argv37, _ := row37["argv"].([]any)
		argv38, _ := row38["argv"].([]any)
		if !argvHasUpdateRefs(argv37) {
			t.Fatalf("this fixture's sole entry is external/unscoped/pass-1; its argv must carry --update-refs: %v", argv37)
		}
		if !reflect.DeepEqual(argv37, argv38) {
			t.Fatalf("the gate must REFUSE a row, never rewrite its argv: %v vs %v", argv37, argv38)
		}

		// Below the gate: effective_backend null, collateral null.
		if row37["effective_backend"] != nil {
			t.Fatalf("effective_backend = %v under 2.37, want null (§11.7 row 2: the inventory was never reached)", row37["effective_backend"])
		}
		if row37["collateral_mechanism"] != nil || row37["collateral_refs"] != nil {
			t.Fatalf("a gated row must publish null collateral facts, got mechanism=%v refs=%v",
				row37["collateral_mechanism"], row37["collateral_refs"])
		}
		// At the gate: the row is classified normally again.
		if row38["effective_backend"] == nil {
			t.Fatal("effective_backend = null under 2.38, want the row-1 merge verdict for an --update-refs argv")
		}

		// Below the gate: a rank 5.9 probe-failed blocker naming the row.
		blockers37, _ := doc37["blockers"].([]any)
		found := false
		for _, b := range blockers37 {
			m, _ := b.(map[string]any)
			if m["kind"] == "probe-failed" && strings.Contains(m["detail"].(string), "--update-refs") {
				found = true
			}
		}
		if !found {
			t.Fatalf("below the 2.38 gate an --update-refs row must raise rank 5.9 probe-failed, got blockers=%v", blockers37)
		}
		if r, _ := doc37["runnable"].(bool); r {
			t.Fatal("a rank 5.9 probe-failed document must not be runnable")
		}

		blockers38, _ := doc38["blockers"].([]any)
		for _, b := range blockers38 {
			m, _ := b.(map[string]any)
			if m["kind"] == "probe-failed" {
				t.Fatalf("at/above 2.38 the gate must not fire, got %v", m)
			}
		}
	})

	t.Run("CheckoutRowsAreNeverGated", func(t *testing.T) {
		dir, _, _, _, _ := setupCustomerTopologyCheckout(t)
		withUnifiedWorkspaceEnv(t, dir)
		for _, version := range []string{"git version 2.37.0", "git version 2.38.0"} {
			doc := planUnderStub(t, version, "test-feature", "--plan", "--json", "--no-fetch")
			rows, _ := doc["entries"].([]any)
			for _, r := range rows {
				row := r.(map[string]any)
				argv, _ := row["argv"].([]any)
				if argvHasUpdateRefs(argv) {
					t.Fatalf("checkout argv must never carry --update-refs: %v", argv)
				}
			}
			blockers, _ := doc["blockers"].([]any)
			for _, b := range blockers {
				m, _ := b.(map[string]any)
				if m["kind"] == "probe-failed" && strings.Contains(m["detail"].(string), "--update-refs") {
					t.Fatalf("no checkout row may be gated on CapRebaseUpdateRefs, got %v under %s", m, version)
				}
			}
		}
	})
}

// TestSyncPlanIntegration_VersionProbeOnceBeforePlanningOnControlledRoutes is
// §22.32a A2: on a CONTROLLED route — --plan --json (guard-free) and an
// armed --max-replay-total (guarded) — the invocation issues exactly one raw
// `git --version` child process (internal.ProbeGitCapabilities, the single
// call InspectExternalPlan makes), and it precedes every "planning phase"
// subcommand this tree issues afterward — checked here as preceding the
// FIRST `git config` call, which every traced route shows is the first
// planning-phase subcommand this tree issues (config inventory precedes
// rev-list/for-each-ref/worktree measurement in every recorded trace).
//
// It deliberately does NOT assert the probe is the invocation's first git
// child process of ANY kind: a handful of workspace/layout-discovery
// `rev-parse` calls (resolving which repository and layout this invocation
// even targets) legitimately precede it — every controlled-route trace this
// test records shows exactly 4 such calls before the probe (record index 4).
func TestSyncPlanIntegration_VersionProbeOnceBeforePlanningOnControlledRoutes(t *testing.T) {
	assertOnceBeforePlanning := func(t *testing.T, records []gitRecord) {
		t.Helper()
		probes := versionProbeIndices(records)
		if len(probes) != 1 {
			t.Fatalf("expected exactly one git --version probe, got %d at indices %v", len(probes), probes)
		}
		configIdx := -1
		for i, r := range records {
			if r.Verb == "config" {
				configIdx = i
				break
			}
		}
		if configIdx == -1 {
			t.Fatalf("expected at least one `git config` call in this trace to anchor the planning phase, found none")
		}
		if probes[0] >= configIdx {
			t.Fatalf("git --version probe at record %d did not precede the first planning-phase `git config` call at record %d", probes[0], configIdx)
		}
	}

	t.Run("plan_only", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		w := newSyncGitWrapper(t, false)
		var exit int
		w.around(t, func() {
			_, _, exit = runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		assertOnceBeforePlanning(t, w.records(t))
	})

	t.Run("armed_max_replay_total", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		w := newSyncGitWrapper(t, false)
		var exit int
		w.around(t, func() {
			_, _, exit = runSyncExecute(t, f.feature, "--no-fetch", "--max-replay-total", "999")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		assertOnceBeforePlanning(t, w.records(t))
	})
}

// TestSyncPlanIntegration_ZeroVersionProbesOnUnguardedRoutes is §22.32a A3:
// a genuinely unguarded, no-plan, no-armed-limit invocation must issue ZERO
// `git --version` probes in any workspace mode. internal/cli/sync.go only
// reaches InspectExternalPlan via runExternalPlan (--plan) or a guarded/
// armed route, and internal/checkout_sync.go only reaches
// InspectCheckoutPlan on its own --plan branch — so an external run with
// neither --plan nor an armed limit (both the new-mode-triggering --no-fetch
// form and the legacy, no-trigger-flag-at-all form), and a checkout run with
// no --plan, must never probe.
func TestSyncPlanIntegration_ZeroVersionProbesOnUnguardedRoutes(t *testing.T) {
	assertZero := func(t *testing.T, records []gitRecord) {
		t.Helper()
		if probes := versionProbeIndices(records); len(probes) != 0 {
			t.Fatalf("expected zero git --version probes, found %d at indices %v", len(probes), probes)
		}
	}

	t.Run("external_new_mode_unarmed", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		w := newSyncGitWrapper(t, false)
		var exit int
		w.around(t, func() {
			_, _, exit = runSyncExecute(t, f.feature, "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		assertZero(t, w.records(t))
	})

	t.Run("external_legacy_unarmed", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		w := newSyncGitWrapper(t, false)
		var exit int
		w.around(t, func() {
			_, _, exit = runSyncExecute(t, f.feature)
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		assertZero(t, w.records(t))
	})

	t.Run("checkout_unarmed", func(t *testing.T) {
		dir, _ := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		w := newSyncGitWrapper(t, false)
		var exit int
		w.around(t, func() {
			_, _, exit = runSyncExecute(t, "test-feature", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		assertZero(t, w.records(t))
	})
}

// TestSyncPlanIntegration_UpdateRefsArgvBiconditionalCorpusWide is §22.32b:
// over every entries[] row of a --plan --json document (and over BOTH
// argv_alternatives of any conditional row), it asserts the argv-derived
// biconditional "--update-refs" ∈ argv ⟺ the row is an EXTERNAL, UNSCOPED,
// PASS-1 materialized row on a host at/above Git 2.38.
//
// The host-version precondition is verified directly (internal.
// ProbeGitVersion + internal.GitCapabilitiesForVersion) rather than assumed,
// so this test fails loudly instead of passing vacuously if it is ever run
// on a pre-2.38 host. "External, unscoped, pass-1 materialized" is read
// entirely from each document's own JSON, never from which flags this test
// happened to pass: workspace.mode=="external" (never "checkout"),
// policy.scope_kind=="all" (never "one"/"subtree" — internal/rebase_plan.go's
// PlanPolicy.ScopeKind; scope_kind flips a row's `scoped` argument at
// rebaseArgv's own call sites in internal/rebase_plan_build.go), and
// strategy ∈ {"plain","onto","conditional"} (RebaseStrategy's three
// argv-publishing pass-1 shapes; "plain-explicit-branch" is pass 2 — it
// passes an explicit branch and rebaseArgv special-cases it before ever
// consulting `scoped` — and "unknown"/other non-materializing shapes publish
// no argv at all).
//
// Fixtures cover: external unscoped (newScopedFixture's 3-entry stack, all
// pass-1 "plain"), external scoped (--only parent, "plain" with
// scoped=true), external pass-2 (TestSyncPlanIntegration_ExternalPass2ArchivedRow's
// own archived-entry-with-Repo fixture shape, reaching
// "plain-explicit-branch"), external conditional
// (setupCustomerTopologyExternal's fixture WITH fetch enabled, whose
// recorded LastBaseSHA lags origin/master's current tip while
// IsRemoteTracking and SyncFetchEnabled both hold — internal/rebase_plan_
// build.go's BaseMayMoveBeforeExecution — reaching RebaseStrategy's
// "conditional" arm, confirmed to publish two argv_alternatives, both
// carrying --update-refs), and checkout (checkoutModeFixture's 3-entry
// stack, "plain"/"unknown").
func TestSyncPlanIntegration_UpdateRefsArgvBiconditionalCorpusWide(t *testing.T) {
	if v, err := internal.ProbeGitVersion(); err != nil || !internal.GitCapabilitiesForVersion(v).CapRebaseUpdateRefs {
		t.Fatalf("this test's host must be at/above Git 2.38 for the biconditional's precondition to hold; ProbeGitVersion=%+v err=%v", v, err)
	}

	pass1Strategies := map[string]bool{"plain": true, "onto": true, "conditional": true}

	assertRow := func(t *testing.T, doc map[string]any, row map[string]any) {
		t.Helper()
		wsMap, _ := doc["workspace"].(map[string]any)
		mode, _ := wsMap["mode"].(string)
		policyMap, _ := doc["policy"].(map[string]any)
		scopeKind, _ := policyMap["scope_kind"].(string)
		strategy, _ := row["strategy"].(string)
		expected := mode == "external" && scopeKind == "all" && pass1Strategies[strategy]

		var argvs [][]any
		if argv, ok := row["argv"].([]any); ok {
			argvs = append(argvs, argv)
		}
		if alts, ok := row["argv_alternatives"].([]any); ok {
			if strategy == "conditional" && len(alts) != 2 {
				t.Fatalf("conditional row %v must publish exactly two argv_alternatives, got %v", row["name"], alts)
			}
			for _, alt := range alts {
				if a, ok := alt.([]any); ok {
					argvs = append(argvs, a)
				}
			}
		}
		for _, argv := range argvs {
			has := false
			for _, a := range argv {
				if s, _ := a.(string); s == "--update-refs" {
					has = true
				}
			}
			if has != expected {
				t.Fatalf("row %v (mode=%s scope_kind=%s strategy=%s): --update-refs present=%v, want %v; argv=%v",
					row["name"], mode, scopeKind, strategy, has, expected, argv)
			}
		}
	}

	assertDoc := func(t *testing.T, stdout string) {
		t.Helper()
		doc := planDoc(t, stdout)
		entries, _ := doc["entries"].([]any)
		if len(entries) == 0 {
			t.Fatalf("expected at least one entry to exercise the biconditional, got none")
		}
		for _, e := range entries {
			assertRow(t, doc, e.(map[string]any))
		}
	}

	t.Run("external_unscoped", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		assertDoc(t, stdout)
	})

	t.Run("external_scoped", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch", "--only", "parent")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		assertDoc(t, stdout)
	})

	t.Run("external_pass2_archived", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_COUNT", "0")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		repo := setupGitRepo(t, "master")
		withWorkspaceEnv(t, repo)
		gitRun(t, repo, "checkout", "-b", "legacy", "master")
		writeAndCommit(t, repo, "legacy.txt", "legacy\n", "legacy work")
		gitRun(t, repo, "checkout", "master")

		featurePath := internal.FeaturePath("feature")
		if err := os.MkdirAll(featurePath, 0755); err != nil {
			t.Fatal(err)
		}
		stack := internal.Stack{Branches: []internal.StackEntry{
			{Name: "legacy", Base: "master", Repo: repo, Archived: true},
		}}
		if err := internal.SaveStack(featurePath, stack); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, exit := runSyncExecute(t, "feature", "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		assertDoc(t, stdout)
	})

	t.Run("external_conditional", func(t *testing.T) {
		setupCustomerTopologyExternal(t)
		// Deliberately no --no-fetch: BaseMayMoveBeforeExecution requires
		// fetch enabled (internal/rebase_plan_build.go), which is what turns
		// this fixture's "onto" row into "conditional".
		stdout, stderr, exit := runSyncExecute(t, "feature", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		entries, _ := doc["entries"].([]any)
		foundConditional := false
		for _, e := range entries {
			row := e.(map[string]any)
			if row["strategy"] == "conditional" {
				foundConditional = true
			}
		}
		if !foundConditional {
			t.Fatalf("expected this fixture (fetch enabled, recorded LastBaseSHA behind origin/master's current tip) to produce a conditional row, got entries=%v", entries)
		}
		assertDoc(t, stdout)
	})

	t.Run("checkout", func(t *testing.T) {
		dir, _ := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		stdout, stderr, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		assertDoc(t, stdout)
	})
}

// TestSyncPlanIntegration_ConfigBudgetPerExecutionContext is §16.3/§23.2 C1:
// over the argv sidecar of a --plan --json document, grouped by
// recordExecContext, it asserts EXACTLY one `config --list --show-scope -z`
// (internal.ProbeGitConfigInventory) per execution context that ever asks a
// config question, AT MOST two typed `config --type=bool --get <key>` reads
// per such context (internal.ProbeRepositoryConfig probes exactly
// rebase.updateRefs and rebase.autoStash — internal/rebase_plan_probe.go),
// that every config call in a context is accounted for by one of those two
// shapes (no third, unrecognized config-call shape sneaks through
// uncounted), and that no typed read of rebase.rebaseMerges appears
// ANYWHERE in the trace: that key's own doc comment states it is derived
// purely from the already-fetched inventory, never independently probed.
func TestSyncPlanIntegration_ConfigBudgetPerExecutionContext(t *testing.T) {
	assertBudget := func(t *testing.T, records []gitRecord, wantContexts int) {
		t.Helper()
		byContext := map[string][]gitRecord{}
		for _, r := range records {
			ctx := recordExecContext(r)
			byContext[ctx] = append(byContext[ctx], r)
		}
		configContexts := 0
		for ctx, recs := range byContext {
			inventoryCount, typedCount, totalConfigCalls := 0, 0, 0
			for _, r := range recs {
				tail := r.Tail()
				if len(tail) == 0 || tail[0] != "config" {
					continue
				}
				totalConfigCalls++
				switch {
				case len(tail) >= 4 && tail[1] == "--list" && tail[2] == "--show-scope" && tail[3] == "-z":
					inventoryCount++
				case len(tail) >= 3 && tail[1] == "--type=bool" && tail[2] == "--get":
					typedCount++
					if len(tail) >= 4 && tail[3] == "rebase.rebaseMerges" {
						t.Fatalf("context %s: unexpected typed read of rebase.rebaseMerges (derived-only key, never independently probed): %v", ctx, tail)
					}
				}
			}
			if totalConfigCalls != inventoryCount+typedCount {
				t.Fatalf("context %s: %d total config calls but only classified %d inventory + %d typed reads — found an unrecognized config call shape", ctx, totalConfigCalls, inventoryCount, typedCount)
			}
			if typedCount > 2 {
				t.Fatalf("context %s: %d typed config reads, want at most 2", ctx, typedCount)
			}
			if inventoryCount == 0 {
				continue // this context never asked a config question at all
			}
			configContexts++
			if inventoryCount != 1 {
				t.Fatalf("context %s: %d `config --list --show-scope -z` calls, want exactly 1", ctx, inventoryCount)
			}
		}
		if configContexts != wantContexts {
			t.Fatalf("%d execution contexts asked a config inventory question, want %d", configContexts, wantContexts)
		}
	}

	t.Run("external_three_worktrees_three_contexts", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		w := newSyncGitWrapper(t, false)
		var exit int
		w.around(t, func() {
			_, _, exit = runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		assertBudget(t, w.records(t), 3)
	})

	t.Run("checkout_one_repository_one_context", func(t *testing.T) {
		dir, _ := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		w := newSyncGitWrapper(t, false)
		var exit int
		w.around(t, func() {
			_, _, exit = runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		assertBudget(t, w.records(t), 1)
	})
}

// setupTwoWorktreeExternalFixture builds a TWO-row external stack whose two
// rows live in two DIFFERENT linked worktrees of ONE physical repository.
// Both execution contexts therefore resolve to the same canonical common
// dir, which is what makes the holder inventory shareable.
func setupTwoWorktreeExternalFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	if err := createWorktree("feature", "root", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, internal.WorktreePath("feature", "root"), "root.txt", "root\n", "root")
	if err := createWorktree("feature", "parent", "root", repo, false); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, internal.WorktreePath("feature", "parent"), "parent.txt", "parent\n", "parent")
	return repo
}

// setupTwoRepoExternalFixture builds a THREE-row external stack spread over
// TWO physically distinct repositories — two rows in two linked worktrees of
// repoA, one row in a worktree of repoB — so the run really has two
// canonical common dirs and cannot collapse to one holder inventory.
func setupTwoRepoExternalFixture(t *testing.T) (repoA, repoB string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repoA = setupGitRepo(t, "master")
	withWorkspaceEnv(t, repoA)
	repoB = setupGitRepo(t, "master")

	if err := createWorktree("feature", "a1", "master", repoA, false); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, internal.WorktreePath("feature", "a1"), "a1.txt", "a1\n", "a1")
	if err := createWorktree("feature", "a2", "a1", repoA, false); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, internal.WorktreePath("feature", "a2"), "a2.txt", "a2\n", "a2")
	if err := createWorktree("feature", "b1", "master", repoB, false); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, internal.WorktreePath("feature", "b1"), "b1.txt", "b1\n", "b1")
	return repoA, repoB
}

// countWorktreeListCalls counts `git worktree list --porcelain` processes —
// internal.BuildBranchHolderInventory's one process, and therefore the
// direct observable of §14.4 rule 4's "one inventory per canonical common
// dir" rule.
func countWorktreeListCalls(records []gitRecord) int {
	n := 0
	for _, r := range records {
		tail := r.Tail()
		if len(tail) >= 2 && tail[0] == "worktree" && tail[1] == "list" {
			n++
		}
	}
	return n
}

// TestSyncPlanIntegration_HolderCountsAcrossFixtures is §16.3/§23.2 C2: it
// asserts the number of `git worktree list --porcelain` calls (internal.
// BuildBranchHolderInventory, the ONE holder producer, reached only through
// internal.BuildPlanHolderIndex) a --plan --json invocation issues.
//
// The rule under test is §14.4 rule 4's: one inventory per distinct
// CANONICAL COMMON DIR, never one per execution directory. Every linked
// worktree of one repository shares one common dir and therefore one
// inventory; two physically distinct repositories cannot share one. The five
// fixtures below pin exactly that, 1/2/1/1/0:
//
//	checkout, one repository, three rows       -> 1
//	external, three rows over TWO repositories -> 2
//	external, three rows in three worktrees of ONE repository -> 1
//	external, two rows in two worktrees of ONE repository     -> 1
//	external, rows-less document               -> 0
//
// The two external one-repository cells are the regression: a cache keyed by
// the execution-directory string (a distinct string per worktree) produces 3
// and 2 there instead of 1 and 1.
func TestSyncPlanIntegration_HolderCountsAcrossFixtures(t *testing.T) {
	t.Run("checkout_one_repository", func(t *testing.T) {
		dir, _ := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		w := newSyncGitWrapper(t, false)
		var exit int
		w.around(t, func() {
			_, _, exit = runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		if got := countWorktreeListCalls(w.records(t)); got != 1 {
			t.Fatalf("checkout `worktree list --porcelain` calls = %d, want 1", got)
		}
	})

	t.Run("external_three_rows_over_two_repositories", func(t *testing.T) {
		setupTwoRepoExternalFixture(t)
		w := newSyncGitWrapper(t, false)
		var stdout string
		var exit int
		w.around(t, func() {
			stdout, _, exit = runSyncExecute(t, "feature", "--plan", "--json", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		if rows := len(planDoc(t, stdout)["entries"].([]any)); rows != 3 {
			t.Fatalf("fixture changed shape: %d rows, want 3", rows)
		}
		if got := countWorktreeListCalls(w.records(t)); got != 2 {
			t.Fatalf("two-repository `worktree list --porcelain` calls = %d, want 2 (one per canonical common dir, never one per row)", got)
		}
	})

	t.Run("external_three_worktrees_of_one_repository", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		w := newSyncGitWrapper(t, false)
		var exit int
		w.around(t, func() {
			_, _, exit = runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		if got := countWorktreeListCalls(w.records(t)); got != 1 {
			t.Fatalf("external `worktree list --porcelain` calls = %d, want 1 (three linked worktrees of ONE repository share ONE canonical common dir and therefore ONE inventory)", got)
		}
	})

	t.Run("external_two_worktrees_of_one_repository", func(t *testing.T) {
		setupTwoWorktreeExternalFixture(t)
		w := newSyncGitWrapper(t, false)
		var stdout string
		var exit int
		w.around(t, func() {
			stdout, _, exit = runSyncExecute(t, "feature", "--plan", "--json", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		if rows := len(planDoc(t, stdout)["entries"].([]any)); rows != 2 {
			t.Fatalf("fixture changed shape: %d rows, want 2", rows)
		}
		if got := countWorktreeListCalls(w.records(t)); got != 1 {
			t.Fatalf("two-worktree `worktree list --porcelain` calls = %d, want 1", got)
		}
	})

	t.Run("rows_less_plan_zero_holder_calls", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_COUNT", "0")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		repo := setupGitRepo(t, "master")
		withWorkspaceEnv(t, repo)
		gitRun(t, repo, "checkout", "-b", "legacy", "master")
		writeAndCommit(t, repo, "legacy.txt", "legacy\n", "legacy work")
		gitRun(t, repo, "checkout", "master")

		featurePath := internal.FeaturePath("feature")
		if err := os.MkdirAll(featurePath, 0755); err != nil {
			t.Fatal(err)
		}
		stack := internal.Stack{Branches: []internal.StackEntry{
			{Name: "legacy", Base: "master", Repo: repo, Archived: true},
		}}
		if err := internal.SaveStack(featurePath, stack); err != nil {
			t.Fatal(err)
		}

		w := newSyncGitWrapper(t, false)
		var stdout string
		var exit int
		w.around(t, func() {
			stdout, _, exit = runSyncExecute(t, "feature", "--plan", "--json", "--no-fetch", "--only", "legacy")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		doc := planDoc(t, stdout)
		if entries, _ := doc["entries"].([]any); len(entries) != 0 {
			t.Fatalf("expected a rows-less document (--only legacy excludes the archived-only entry), got entries=%v", entries)
		}
		if got := countWorktreeListCalls(w.records(t)); got != 0 {
			t.Fatalf("rows-less plan `worktree list --porcelain` calls = %d, want 0", got)
		}
	})
}

// gitProcessClass names the attribution bucket one recorded git process
// belongs to. Every process in a trace lands in exactly one bucket, so a
// per-class table both explains the total AND fails when a new, unclassified
// process shape appears.
func gitProcessClass(r gitRecord) string {
	tail := r.Tail()
	if len(tail) == 0 {
		return "version" // `git --version`, whose whole argv is a flag
	}
	switch tail[0] {
	case "worktree":
		return "holder-inventory"
	case "for-each-ref":
		return "branch-ref-inventory"
	case "config":
		if len(tail) >= 2 && tail[1] == "--list" {
			return "config-inventory"
		}
		return "config-typed-read"
	case "status":
		return "dirty-probe"
	case "ls-files":
		return "untracked-probe"
	case "rev-list":
		return "candidate-probe"
	case "log", "show":
		return "candidate-subject"
	case "merge-base":
		return "ancestry-probe"
	case "symbolic-ref":
		return "default-branch-probe"
	case "rev-parse":
		if len(tail) >= 2 {
			switch tail[1] {
			case "--show-toplevel", "--git-common-dir":
				return "context-roots"
			case "--git-path":
				return "in-progress-state"
			case "--short":
				return "short-sha"
			case "--abbrev-ref":
				return "default-branch-probe"
			}
		}
		return "ref-resolution"
	}
	return "unclassified:" + tail[0]
}

func gitProcessCensus(records []gitRecord) map[string]int {
	census := map[string]int{}
	for _, r := range records {
		census[gitProcessClass(r)]++
	}
	return census
}

func assertGitProcessCensus(t *testing.T, records []gitRecord, want map[string]int) {
	t.Helper()
	got := gitProcessCensus(records)
	classes := map[string]bool{}
	for k := range got {
		classes[k] = true
	}
	for k := range want {
		classes[k] = true
	}
	names := make([]string, 0, len(classes))
	for k := range classes {
		names = append(names, k)
	}
	sort.Strings(names)
	failed := false
	for _, name := range names {
		if got[name] != want[name] {
			t.Errorf("git process class %q = %d, want %d", name, got[name], want[name])
			failed = true
		}
	}
	wantTotal := 0
	for _, v := range want {
		wantTotal += v
	}
	if len(records) != wantTotal {
		t.Errorf("total git child processes = %d, want %d (the per-class table must account for EVERY process)", len(records), wantTotal)
		failed = true
	}
	if failed {
		t.Fatalf("full census: %v", got)
	}
}

// TestSyncPlanIntegration_GitProcessCeilingPerRow is §16.3/§23.2 C3: it
// asserts a --plan --json invocation's git child processes EXACTLY, class by
// class, with every process attributed — no slack allowance, no
// `rows*rate+allowance` inequality that a regression could hide inside.
//
// The attribution axes are the three the design actually has:
//
//   - per canonical COMMON DIR: `worktree list --porcelain` (holder
//     inventory) and `for-each-ref` (branch ref inventory) — one each, for
//     both fixtures, because both live in a single repository;
//   - per EXECUTION CONTEXT: one `config --list --show-scope -z` inventory
//     plus exactly two typed `config --type=bool --get` reads. External's
//     three worktrees are three contexts (3 and 6); checkout's single
//     repository is one context (1 and 2);
//   - per ROW: one `status` dirty probe and one `ls-files` untracked probe
//     per external row; checkout probes its single execution context once.
//
// Everything else is invocation-level prelude (workspace/layout discovery,
// context-root measurement, ref resolution, the one `git --version`
// capability probe) and is pinned here at its exact observed value. A change
// in any class fails with the class named, so the budget is re-derived by
// reading the failure, never by widening an allowance.
func TestSyncPlanIntegration_GitProcessCeilingPerRow(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		w := newSyncGitWrapper(t, false)
		var stdout string
		var exit int
		w.around(t, func() {
			stdout, _, exit = runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		doc := planDoc(t, stdout)
		entries, _ := doc["entries"].([]any)
		if rows := len(entries); rows != 3 {
			t.Fatalf("fixture changed shape: expected 3 rows, got %d — the census below must be re-derived", rows)
		}
		assertGitProcessCensus(t, w.records(t), map[string]int{
			"version":              1,  // one capability probe per invocation
			"holder-inventory":     1,  // one canonical common dir
			"branch-ref-inventory": 1,  // one canonical common dir
			"config-inventory":     3,  // one per execution context
			"config-typed-read":    6,  // two per execution context
			"dirty-probe":          3,  // one per row
			"untracked-probe":      3,  // one per row
			"candidate-probe":      4,  // rev-list count/first/stream/patch-equivalent on the one exact row
			"candidate-subject":    1,  // the one exact row's first-candidate subject
			"context-roots":        20, // --show-toplevel + --git-common-dir pairs
			"default-branch-probe": 2,
			"ref-resolution":       9,
		})
	})

	t.Run("checkout", func(t *testing.T) {
		dir, _ := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		w := newSyncGitWrapper(t, false)
		var stdout string
		var exit int
		w.around(t, func() {
			stdout, _, exit = runSyncExecute(t, "test-feature", "--plan", "--json", "--no-fetch")
		})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		doc := planDoc(t, stdout)
		entries, _ := doc["entries"].([]any)
		if rows := len(entries); rows != 3 {
			t.Fatalf("fixture changed shape: expected 3 rows, got %d — the census below must be re-derived", rows)
		}
		assertGitProcessCensus(t, w.records(t), map[string]int{
			"version":              1,
			"holder-inventory":     1, // one repository, one common dir
			"config-inventory":     1, // one execution context
			"config-typed-read":    2, // two per execution context
			"dirty-probe":          3,
			"untracked-probe":      1,
			"ancestry-probe":       3, // checkout's stack-wide ancestry pass
			"short-sha":            4,
			"in-progress-state":    11, // rebase-merge/rebase-apply/MERGE_HEAD/... probes
			"context-roots":        18,
			"default-branch-probe": 3,
			"ref-resolution":       21,
		})
	})
}

// ---------------------------------------------------------------------------
// The push lease baseline is measured AFTER this invocation's own fetch
// (§14.1a rules 7-8, §14.2), and `fresh` is reachable only because of it.
// ---------------------------------------------------------------------------

// leaseFreshnessByBranch projects one --plan --json document's push.targets[]
// into git_branch -> (expectation, expected_sha, expected_sha_freshness).
func leaseFreshnessByBranch(t *testing.T, stdout string) map[string][3]string {
	t.Helper()
	doc := planDoc(t, stdout)
	push, ok := doc["push"].(map[string]any)
	if !ok {
		t.Fatalf("push = %#v, want an object", doc["push"])
	}
	targets, ok := push["targets"].([]any)
	if !ok {
		t.Fatalf("push.targets = %#v, want an array", push["targets"])
	}
	out := map[string][3]string{}
	for _, raw := range targets {
		row := raw.(map[string]any)
		lease, ok := row["lease"].(map[string]any)
		if !ok {
			t.Fatalf("push.targets[].lease = %#v, want an object", row["lease"])
		}
		sha, _ := lease["expected_sha"].(string)
		out[row["git_branch"].(string)] = [3]string{
			lease["expectation"].(string), sha, lease["expected_sha_freshness"].(string),
		}
	}
	return out
}

// TestSyncPlanIntegration_PushLeaseFreshnessFollowsTheRealFetch drives the
// whole §14.1a rule 7/8 chain through production cli.Execute() over a real
// repository with a real remote: the enumerated fetch plan's per-context
// common dir and measured effect reach the fetch OUTCOME, the outcome reaches
// PushContextRefreshed, and RefreshPushTrackingRefs re-reads the baseline
// after the child ran with TrackingPhase post-fetch and FetchedThisRun true.
//
// The fixture is deliberately TWO LINKED WORKTREES of ONE repository, both of
// whose branches exist on origin: they carry two different context_ids and
// ONE canonical common dir, so rule 8's conjunct iv (join on the common dir,
// never on context_id) is what makes BOTH rows `fresh` off the single fetch
// §11.4's collapse rule performs.
//
// The --no-fetch control is the other half: the very same baseline, read at
// the plan point, is `possibly-stale` on both rows. Without it, a projection
// that hard-coded `fresh` would pass the first half.
func TestSyncPlanIntegration_PushLeaseFreshnessFollowsTheRealFetch(t *testing.T) {
	setupTwoWorktreeExternalFixture(t)
	gitRun(t, internal.WorktreePath("feature", "root"), "push", "-u", "origin", "root")
	gitRun(t, internal.WorktreePath("feature", "parent"), "push", "-u", "origin", "parent")

	rootSHA := gitOutput(t, internal.WorktreePath("feature", "root"), "rev-parse", "HEAD")
	parentSHA := gitOutput(t, internal.WorktreePath("feature", "parent"), "rev-parse", "HEAD")

	t.Run("fetching_run_publishes_fresh_on_both_linked_worktree_rows", func(t *testing.T) {
		stdout, stderr, exit := runSyncExecute(t, "feature", "--plan", "--json", "--push")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		fetch, _ := doc["fetch"].(map[string]any)
		if fetch == nil || fetch["attempted"] != true {
			t.Fatalf("fetch = %#v, want an attempted fetch", doc["fetch"])
		}
		repos, _ := fetch["repos"].([]any)
		if len(repos) != 1 {
			t.Fatalf("fetch.repos = %#v, want exactly one row", fetch["repos"])
		}
		repo := repos[0].(map[string]any)
		if repo["context_common_dir"] == nil {
			t.Fatal("fetch.repos[0].context_common_dir is null: the outcome must carry the PRE-MEASURED common dir, or no push context can ever join on it")
		}
		effect, _ := repo["fetch_effect"].(map[string]any)
		if effect == nil || effect["contacted"] != true {
			t.Fatalf("fetch.repos[0].fetch_effect = %#v, want a contacting effect copied from the enumerated context", repo["fetch_effect"])
		}

		leases := leaseFreshnessByBranch(t, stdout)
		want := map[string][3]string{
			"root":   {"sha", rootSHA, "fresh"},
			"parent": {"sha", parentSHA, "fresh"},
		}
		if len(leases) != len(want) {
			t.Fatalf("push.targets = %v, want exactly the two rows %v", leases, want)
		}
		for branch, w := range want {
			if leases[branch] != w {
				t.Fatalf("push.targets[%s].lease = %v, want %v (both linked worktrees share ONE canonical common dir, so the single fetch refreshes both)", branch, leases[branch], w)
			}
		}
	})

	t.Run("no_fetch_run_publishes_possibly_stale_on_the_same_rows", func(t *testing.T) {
		stdout, stderr, exit := runSyncExecute(t, "feature", "--plan", "--json", "--push", "--no-fetch")
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		fetch, _ := doc["fetch"].(map[string]any)
		if fetch == nil || fetch["attempted"] != false {
			t.Fatalf("fetch = %#v, want an un-attempted fetch", doc["fetch"])
		}
		leases := leaseFreshnessByBranch(t, stdout)
		want := map[string][3]string{
			"root":   {"sha", rootSHA, "possibly-stale"},
			"parent": {"sha", parentSHA, "possibly-stale"},
		}
		for branch, w := range want {
			if leases[branch] != w {
				t.Fatalf("push.targets[%s].lease = %v, want %v (a plan-point read is a real read, but never a refreshed one)", branch, leases[branch], w)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// External cause-5 / cause-6 fetch suppression publishes the WHOLE measured
// row (§11.1, §4.4a) — the disclosure regression.
// ---------------------------------------------------------------------------

// assertWholeSuppressedFetchRow is the shared §4.4a assertion for a cause-5
// or cause-6 external document: the fetch was NOT attempted, the document
// names exactly one suppression cause, and the row still carries every
// measured context fact plus the complete fetch_effect — a suppressed fetch
// discloses what it WOULD have done, and a row stripped to its token would
// tell an operator nothing about the hazard that stopped it.
func assertWholeSuppressedFetchRow(t *testing.T, stdout, wantCause string) map[string]any {
	t.Helper()
	doc := planDoc(t, stdout)
	fetch, _ := doc["fetch"].(map[string]any)
	if fetch == nil {
		t.Fatalf("fetch = %#v, want an object", doc["fetch"])
	}
	if fetch["attempted"] != false {
		t.Fatalf("fetch.attempted = %v, want false: a suppressed fetch runs no child", fetch["attempted"])
	}
	if fetch["suppression_cause"] != wantCause {
		t.Fatalf("fetch.suppression_cause = %v, want %q", fetch["suppression_cause"], wantCause)
	}
	if doc["freshness"] != wantCause {
		t.Fatalf("freshness = %v, want the same token %q: the cause introduces no second vocabulary", doc["freshness"], wantCause)
	}
	repos, _ := fetch["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("fetch.repos = %#v, want exactly one disclosed row", fetch["repos"])
	}
	row := repos[0].(map[string]any)
	if row["attempted"] != false {
		t.Fatalf("fetch.repos[0].attempted = %v, want false", row["attempted"])
	}
	if row["ok"] != nil {
		t.Fatalf("fetch.repos[0].ok = %v, want null on an un-attempted row", row["ok"])
	}
	for _, key := range []string{"repo_token", "context_root", "context_common_dir", "context_source"} {
		if row[key] == nil || row[key] == "" {
			t.Fatalf("fetch.repos[0].%s = %v, want the measured value: causes 5 and 6 publish the WHOLE row", key, row[key])
		}
	}
	candidates, _ := row["context_candidates"].([]any)
	if len(candidates) == 0 {
		t.Fatalf("fetch.repos[0].context_candidates = %#v, want the enumerated candidates", row["context_candidates"])
	}
	effect, _ := row["fetch_effect"].(map[string]any)
	if effect == nil {
		t.Fatal("fetch.repos[0].fetch_effect is null: a cause-5/6 row publishes the complete measured effect")
	}
	// A rank-5 fetch blocker of the matching kind is what actually stopped it.
	blockers, _ := doc["blockers"].([]any)
	if len(blockers) == 0 {
		t.Fatal("a suppressed fetch must publish its own blocker")
	}
	return effect
}

// TestSyncPlanIntegration_ExternalFetchSuppressionCause6PublishesTheWholeRow
// is cause 6 (`not-refreshed-local-branch-checked-out`) on the EXTERNAL
// route: a configured positive refspec writes into refs/heads/**, and the
// covered branches are really checked out in this feature's own worktrees,
// so no fetch may run — and the document still discloses the whole measured
// row, including local_branch_destinations with its patterns, branches and
// held[] rows.
func TestSyncPlanIntegration_ExternalFetchSuppressionCause6PublishesTheWholeRow(t *testing.T) {
	f := newScopedFixture(t)
	// A refspec that would overwrite local branches directly. The stack's
	// own branches are checked out in linked worktrees, so every one of them
	// is a holder `git fetch` would have to fight.
	gitRun(t, f.repo, "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")

	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json")
	if exit != 0 {
		t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
	}
	effect := assertWholeSuppressedFetchRow(t, stdout, "not-refreshed-local-branch-checked-out")

	lbd, _ := effect["local_branch_destinations"].(map[string]any)
	if lbd == nil {
		t.Fatalf("fetch_effect.local_branch_destinations = %#v, want the measured object", effect["local_branch_destinations"])
	}
	patterns, _ := lbd["patterns"].([]any)
	if len(patterns) == 0 {
		t.Fatalf("local_branch_destinations.patterns = %#v, want the covering positive refspec", lbd["patterns"])
	}
	held, _ := lbd["held"].([]any)
	if len(held) == 0 {
		t.Fatalf("local_branch_destinations.held = %#v, want the real worktree holders that caused the suppression", lbd["held"])
	}
	for _, raw := range held {
		row := raw.(map[string]any)
		for _, key := range []string{"branch", "worktree", "hold"} {
			if row[key] == nil || row[key] == "" {
				t.Fatalf("held[] row %#v is missing %q", row, key)
			}
		}
	}
}

// TestSyncPlanIntegration_ExternalFetchSuppressionCause5PublishesTheWholeRow
// is cause 5 (`not-refreshed-submodule-reach-indeterminate`) on the EXTERNAL
// route: unconditional submodule recursion is configured, and the reach walk
// cannot be bounded because a present gitlink's own `.git` artefact is a
// symlink the walk refuses to follow. The document still publishes the whole
// measured row, with submodule_recursion naming mode `yes` and reach
// `unknown`.
func TestSyncPlanIntegration_ExternalFetchSuppressionCause5PublishesTheWholeRow(t *testing.T) {
	f := newScopedFixture(t)
	gitRun(t, f.repo, "config", "fetch.recurseSubmodules", "yes")

	// A present gitlink whose .git is a symlink: listPresentGitlinks sees it,
	// and submodulePopulated refuses to follow the symlinked artefact, so the
	// reach is genuinely unbounded rather than merely large.
	sub := filepath.Join(f.repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.repo, filepath.Join(sub, ".git")); err != nil {
		t.Skipf("this fixture needs symlink support: %v", err)
	}
	gitRun(t, f.repo, "update-index", "--add", "--cacheinfo",
		"160000,1111111111111111111111111111111111111111,sub")

	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json")
	if exit != 0 {
		t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
	}
	effect := assertWholeSuppressedFetchRow(t, stdout, "not-refreshed-submodule-reach-indeterminate")

	rec, _ := effect["submodule_recursion"].(map[string]any)
	if rec == nil {
		t.Fatalf("fetch_effect.submodule_recursion = %#v, want the measured object", effect["submodule_recursion"])
	}
	if rec["mode"] != "yes" {
		t.Fatalf("submodule_recursion.mode = %v, want yes", rec["mode"])
	}
	if rec["reach"] != "unknown" {
		t.Fatalf("submodule_recursion.reach = %v, want unknown (the walk could not be bounded)", rec["reach"])
	}
}

// ===========================================================================
// §22.12b — the external completion gate is a POSTCONDITION, and the plan
// says so (§7.1 rank 4, §13.5, §13.7a rule 15).
// ===========================================================================

// staleEdgeFixture builds an external feature whose CHILD branch does not
// contain its configured parent — exactly the shape staleStackEdgesFiltered
// reports — by resetting the parent onto a divergent commit after the child
// forked from it.
func staleEdgeFixture(t *testing.T) *scopedFixture {
	t.Helper()
	f := newScopedFixture(t)
	// Move `parent` sideways: `child` forked from the old tip, so child no
	// longer contains parent's current tip.
	writeAndCommit(t, f.wt("parent"), "diverged.txt", "diverged\n", "parent diverges after child forked")
	return f
}

// staleEdgeAncestry returns the child row's ancestry object.
func staleEdgeAncestry(t *testing.T, stdout string) map[string]any {
	t.Helper()
	for _, e := range planDoc(t, stdout)["entries"].([]any) {
		if row := e.(map[string]any); row["name"] == "child" {
			return row["ancestry"].(map[string]any)
		}
	}
	t.Fatalf("no child row:\n%s", stdout)
	return nil
}

// TestSyncPlanIntegration_Criterion22_12b_CompletionGateIsAPostcondition is
// §22.12b's executable owner, clauses (i)-(v).
func TestSyncPlanIntegration_Criterion22_12b_CompletionGateIsAPostcondition(t *testing.T) {
	// (i) a plan over a stale edge on a REBASE row publishes the edge as
	// ancestry, no blocker of any rank, runnable true, and fetches normally.
	t.Run("i_plan_publishes_the_edge_as_ancestry_and_blocks_nothing", func(t *testing.T) {
		for _, args := range [][]string{
			{"--plan", "--json", "--no-fetch"},
			{"--plan", "--json", "--fetch"},
		} {
			args := args
			t.Run(strings.Join(args, "_"), func(t *testing.T) {
				f := staleEdgeFixture(t)
				stdout, stderr, exit := runSyncExecute(t, append([]string{f.feature}, args...)...)
				if exit != 0 {
					t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
				}
				doc := planDoc(t, stdout)
				if doc["runnable"] != true {
					t.Fatalf("runnable = %v, want true: a stale edge on a row this run will rebase blocks nothing", doc["runnable"])
				}
				for _, raw := range doc["blockers"].([]any) {
					b := raw.(map[string]any)
					detail, _ := b["detail"].(string)
					if strings.Contains(detail, "stale stack edges remain") {
						t.Fatalf("the completion gate must publish NO blocker on a rebase-row document: %v", b)
					}
				}
				if doc["freshness"] == "not-refreshed-no-plan-subject" {
					t.Fatal("a stale edge must never suppress the fetch as not-refreshed-no-plan-subject")
				}
				if a := staleEdgeAncestry(t, stdout); a["status"] == nil {
					t.Fatalf("the child row must publish its ancestry status, got %v", a)
				}
			})
		}
	})

	// (ii) the same holds for a --plan --continue whose remaining set still
	// contains a rebase row.
	t.Run("ii_plan_continue_with_a_rebase_row_blocks_nothing", func(t *testing.T) {
		f := staleEdgeFixture(t)
		f.makeConflict(t)
		if _, _, exit := runSync(t, f.feature, "--only", "child"); exit == 0 {
			t.Fatal("expected a conflict")
		}
		f.detachGuard(t)
		resolveRebase(t, f.wt("child"))

		stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--continue")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			if detail, _ := b["detail"].(string); strings.Contains(detail, "stale stack edges remain") {
				t.Fatalf("a continuation with a rebase row must publish no completion-gate blocker: %v", b)
			}
		}
	})

	// (iii) a ROWS-LESS PUSH-ONLY continuation publishes rank 4
	// preflight-refused with the shipped sentences byte for byte.
	t.Run("iii_rows_less_push_only_continuation_is_rank_4", func(t *testing.T) {
		f := staleEdgeFixture(t)
		gitRun(t, f.wt("root"), "push", "-u", "origin", "root")

		// A continuation payload whose remaining rebase work is empty and
		// whose push intent is real: the one cell the gate is total in.
		marker, err := syncMarkerFn()
		if err != nil {
			t.Fatal(err)
		}
		payload := internal.NewSyncRunState(f.feature, marker, "c12b-token", internal.SyncRunPolicy{
			Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull, ScopeKind: internal.SyncScopeAll,
		})
		payload.Selected = []string{"root", "parent", "child"}
		payload.Completed = []string{"root", "parent", "child"}
		payload.Pending = nil
		payload.Push = true
		payload.Stage = internal.SyncStagePushing
		if err := internal.SaveSyncRunState(f.featurePath, payload); err != nil {
			t.Fatal(err)
		}
		sentinel := internal.NewSyncState()
		sentinel.FailedBranch = marker
		sentinel.Pending = []string{}
		sentinel.Completed = []string{}
		sentinel.Skipped = []string{}
		if err := internal.SaveSyncState(f.featurePath, sentinel); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--continue", "--push")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		if doc["runnable"] != false {
			t.Fatalf("runnable = %v, want false", doc["runnable"])
		}
		found := false
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			if b["kind"] != "preflight-refused" {
				continue
			}
			detail, _ := b["detail"].(string)
			if !strings.Contains(detail, "Sync incomplete; stale stack edges remain:") {
				continue
			}
			found = true
			if !strings.Contains(detail, "\n  [!] ") {
				t.Fatalf("detail must carry the shipped `  [!] %%s does not contain parent %%s` line byte for byte:\n%s", detail)
			}
			if !strings.Contains(detail, "does not contain parent") {
				t.Fatalf("detail = %q, want the shipped edge sentence", detail)
			}
		}
		if !found {
			t.Fatalf("no rank 4 preflight-refused completion-gate blocker in %v", doc["blockers"])
		}
		push := doc["push"].(map[string]any)
		if push["executable"] != false {
			t.Fatalf("push.executable = %v, want false", push["executable"])
		}
		blockedBy, _ := push["blocked_by"].([]any)
		if !slices.ContainsFunc(blockedBy, func(v any) bool { return v == "preflight-refused" }) {
			t.Fatalf("push.blocked_by = %v, want preflight-refused", blockedBy)
		}
		if targets, _ := push["targets"].([]any); len(targets) == 0 {
			t.Fatalf("push.targets = %v, want a NON-EMPTY blast radius", push["targets"])
		}
		summary := doc["summary"].(map[string]any)
		if summary["plannability"] != "empty" {
			t.Fatalf("summary.plannability = %v, want empty", summary["plannability"])
		}
		if doc["freshness"] != "not-refreshed-continuation" {
			t.Fatalf("freshness = %v, want not-refreshed-continuation", doc["freshness"])
		}
	})

	// (iv) the EXECUTING twin of (i) really rebases; where the edge survives,
	// the shipped gate fires AFTER the rebases with its shipped bytes, exit
	// 1, no marker, and recovery state persisted.
	t.Run("iv_executing_twin_really_rebases_and_the_gate_is_a_postcondition", func(t *testing.T) {
		// The executing twin of (i): it really rebases, it is marker-free,
		// and the completion gate is evaluated STRICTLY AFTER those rebases —
		// which is why a stale edge on a row this run rebases is fixed by the
		// run rather than refused before it.
		f := staleEdgeFixture(t)
		before := f.sha(t, "child")
		stdout, stderr, exit := runSyncExecute(t, f.feature, "--no-fetch")
		if strings.Contains(stderr, "plan-guard: ") || strings.Contains(stdout, "plan-guard: ") {
			t.Fatalf("the shipped completion gate is marker-free: %q / %q", stdout, stderr)
		}
		if exit != 0 {
			t.Fatalf("the run must rebase the stale edge away and succeed: exit=%d stdout=%q", exit, stdout)
		}
		if after := f.sha(t, "child"); after == before {
			t.Fatal("the executing twin must really rebase the child row")
		}
		if strings.Contains(stdout, "Sync incomplete; stale stack edges remain:") {
			t.Fatalf("an edge the run itself fixed must not fire the postcondition:\n%s", stdout)
		}

		// The SURVIVING-edge half, over the same predicate: a SCOPED run
		// leaves every out-of-scope edge exactly as it found it, and the
		// shipped bytes for that condition are printed. The gate's own
		// refusing form is asserted byte-for-byte by clause (iii)'s document,
		// which projects the identical two sentences.
		g := staleEdgeFixture(t)
		stdout, stderr, exit = runSyncExecute(t, g.feature, "--no-fetch", "--only", "root")
		if strings.Contains(stderr, "plan-guard: ") || strings.Contains(stdout, "plan-guard: ") {
			t.Fatalf("the shipped scoped gate is marker-free: %q / %q", stdout, stderr)
		}
		if exit != 0 {
			t.Fatalf("a scoped run whose own edges are clean succeeds: exit=%d stdout=%q", exit, stdout)
		}
		if !strings.Contains(stdout, "Stale stack edges outside this scope (unchanged by this run):") {
			t.Fatalf("the surviving out-of-scope edge must be reported with the shipped sentence:\n%s", stdout)
		}
		if !strings.Contains(stdout, "  [i] child does not contain parent parent") {
			t.Fatalf("the surviving edge line must carry the shipped operands:\n%s", stdout)
		}

		// A run that DOES fail persists recovery state through syncFailure,
		// and both recovery verbs then work over it.
		h := staleEdgeFixture(t)
		h.makeConflict(t)
		if _, _, exit := runSyncExecute(t, h.feature, "--no-fetch"); exit == 0 {
			t.Fatal("expected the conflicting run to fail")
		}
		if !internal.HasSyncState(h.featurePath) && !internal.HasSyncRunState(h.featurePath) {
			t.Fatal("syncFailure must persist recovery state")
		}
		if _, err := os.Lstat(internal.SyncRunGuardPath(h.featurePath)); err == nil {
			h.detachGuard(t)
		}
		if _, _, exit := runSyncExecute(t, h.feature, "--abort"); exit != 0 {
			t.Fatalf("--abort over the persisted recovery state must work: exit=%d", exit)
		}
	})

	// (v) the source assertion: runExternalPlan's route evaluates
	// staleStackEdgesFiltered in NO cell other than (iii)'s.
	t.Run("v_source_assertion_one_evaluation_site", func(t *testing.T) {
		src := readCliSource(t, "sync_plan_guard.go")
		n := strings.Count(src, "staleStackEdgesFiltered(")
		if n != 1 {
			t.Fatalf("sync_plan_guard.go calls staleStackEdgesFiltered %d times, want exactly 1 (the rows-less push-only cell)", n)
		}
		body := funcBody(t, src, "externalPreflightWithCompletionGate")
		if !strings.Contains(body, "staleStackEdgesFiltered(") {
			t.Fatal("the one call site must be externalPreflightWithCompletionGate")
		}
		for _, guardText := range []string{"!req.Continue", "!push", "len(insp.Remaining) > 0"} {
			if !strings.Contains(body, guardText) {
				t.Fatalf("the evaluation must be scoped by %q", guardText)
			}
		}
	})
}

// ===========================================================================
// §22.8b — route_triggers[] is bytewise sorted, and one rule decides it.
// ===========================================================================

// routeTriggersOf runs one --plan --json invocation and returns its
// route_triggers array as a []string, element order preserved.
func routeTriggersOf(t *testing.T, feature string, args ...string) []string {
	t.Helper()
	stdout, stderr, exit := runSyncExecute(t, append([]string{feature, "--plan", "--json"}, args...)...)
	if exit != 0 {
		t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
	}
	raw, ok := planDoc(t, stdout)["route_triggers"].([]any)
	if !ok {
		t.Fatalf("route_triggers = %#v, want an array", planDoc(t, stdout)["route_triggers"])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

// TestSyncPlanIntegration_Criterion22_8b_RouteTriggersAreBytewiseSorted is
// §22.8b's executable owner: all SEVEN cells, driven through production
// cli.Execute().
func TestSyncPlanIntegration_Criterion22_8b_RouteTriggersAreBytewiseSorted(t *testing.T) {
	// The shipped slice order this criterion exists to prove is NOT used.
	sliceOrder := []string{"fetch", "no-fetch", "full", "local-only", "only", "from"}

	cells := []struct {
		name string
		args []string
		want []string
	}{
		{"maximal_triple", []string{"--no-fetch", "--local-only", "--from", "parent"}, []string{"from", "local-only", "no-fetch"}},
		{"companion_triple", []string{"--fetch", "--full", "--only", "parent"}, []string{"fetch", "full", "only"}},
		{"mixed_triple", []string{"--fetch", "--local-only", "--from", "parent"}, []string{"fetch", "from", "local-only"}},
		{"two_flag", []string{"--no-fetch", "--from", "parent"}, []string{"from", "no-fetch"}},
		{"legacy_empty", nil, []string{}},
	}
	for _, c := range cells {
		c := c
		t.Run(c.name, func(t *testing.T) {
			f := newScopedFixture(t)
			got := routeTriggersOf(t, f.feature, c.args...)
			if len(got) != len(c.want) {
				t.Fatalf("route_triggers = %v, want %v", got, c.want)
			}
			// Element by element, never as a set.
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("route_triggers[%d] = %q, want %q (whole array %v, want %v)", i, got[i], c.want[i], got, c.want)
				}
			}
			// And never the shipped syncTriggerFlags slice order.
			if len(got) > 1 {
				var inSliceOrder []string
				for _, f := range sliceOrder {
					if slices.Contains(got, f) {
						inSliceOrder = append(inSliceOrder, f)
					}
				}
				if slices.Equal(got, inSliceOrder) && !slices.IsSorted(got) {
					t.Fatalf("route_triggers = %v is the shipped slice order, not the bytewise sort", got)
				}
			}
			if !slices.IsSorted(got) {
				t.Fatalf("route_triggers = %v is not bytewise sorted", got)
			}
		})
	}

	t.Run("shell_order_invariance", func(t *testing.T) {
		f := newScopedFixture(t)
		a := routeTriggersOf(t, f.feature, "--no-fetch", "--local-only", "--from", "parent")
		g := newScopedFixture(t)
		b := routeTriggersOf(t, g.feature, "--from", "parent", "--local-only", "--no-fetch")
		if !slices.Equal(a, b) {
			t.Fatalf("two documents differing only in shell order published %v and %v", a, b)
		}
	})

	t.Run("fingerprint_non_membership", func(t *testing.T) {
		f := newScopedFixture(t)
		stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json",
			"--no-fetch", "--local-only", "--from", "parent", "--max-replay-total", "50")
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		fp := planFieldString(t, stdout, "approval", "fingerprint")
		if len(fp) != 64 {
			t.Fatalf("expected a minted token, got %q", fp)
		}
		// Rebuild the very same document with the array spelled in the OTHER
		// order and recompute the fingerprint over its pre-image: the digest
		// must be unchanged, because route_triggers is a non-member.
		var plan internal.RebasePlan
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatalf("decode the document into RebasePlan: %v", err)
		}
		reversed := append([]string(nil), plan.RouteTriggers...)
		slices.Reverse(reversed)
		if slices.Equal(reversed, plan.RouteTriggers) {
			t.Fatalf("the triple must have a distinct reverse order, got %v", plan.RouteTriggers)
		}
		before, err := internal.PlanFingerprintPreimage(plan)
		if err != nil {
			t.Fatalf("pre-image: %v", err)
		}
		plan.RouteTriggers = reversed
		after, err := internal.PlanFingerprintPreimage(plan)
		if err != nil {
			t.Fatalf("pre-image: %v", err)
		}
		if string(before) != string(after) {
			t.Fatal("route_triggers changed the fingerprint pre-image; §8.3 declares it a NON-member")
		}
	})

	t.Run("shipped_I8_sentence_is_untouched", func(t *testing.T) {
		f := newScopedFixture(t)
		_, stderr, exit := runSyncExecute(t, f.feature, "--abort", "--no-fetch", "--from", "parent")
		if exit == 0 {
			t.Fatal("--abort with trigger flags must refuse")
		}
		// syncChangedTriggerList's own `--`-prefixed, sorted list, byte for byte.
		want := "--abort cannot be combined with --from, --no-fetch; abort is defined by the persisted run"
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want the shipped I8 sentence %q", stderr, want)
		}
	})
}

// ===========================================================================
// §22.3/§22.4/§22.5/§22.6 — the early control errors touch NOTHING.
// ===========================================================================

// assertEarlyControlRefusal drives one invocation expected to be refused as a
// §3.6 row-4 early control error and asserts the three things that make it
// "early": exit 1 with the exact sentence, ZERO git child processes, and no
// state file of any kind created.
func assertEarlyControlRefusal(t *testing.T, f *scopedFixture, wantMsg string, args ...string) {
	t.Helper()
	w := newSyncGitWrapper(t, false)
	var stdout, stderr string
	var exit int
	w.around(t, func() {
		stdout, stderr, exit = runSyncExecute(t, append([]string{f.feature}, args...)...)
	})
	if exit != 1 {
		t.Fatalf("args %v: exit=%d, want 1 (stdout=%q stderr=%q)", args, exit, stdout, stderr)
	}
	if !strings.Contains(stderr, wantMsg) {
		t.Fatalf("args %v: stderr = %q, want %q", args, stderr, wantMsg)
	}
	if records := w.records(t); len(records) != 0 {
		t.Fatalf("args %v: an early control error must issue ZERO git processes, got %d: %v",
			args, len(records), records[0].Tail())
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("args %v: an early control error emits no stdout, got %q", args, stdout)
	}
	f.stateFilesGone(t)
}

// TestSyncPlanIntegration_Criterion22_3456_EarlyControlErrorsTouchNothing is
// the executable owner for §22 criteria 3, 4, 5 and 6: each early control
// error is refused BEFORE any probe — zero git processes, zero state.
func TestSyncPlanIntegration_Criterion22_3456_EarlyControlErrorsTouchNothing(t *testing.T) {
	token := strings.Repeat("a", 64)

	t.Run("criterion_3_json_requires_plan", func(t *testing.T) {
		f := newScopedFixture(t)
		assertEarlyControlRefusal(t, f, "--json requires --plan", "--json")
	})

	t.Run("criterion_4_abort_combinations", func(t *testing.T) {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"--plan", "--abort"}, "--plan cannot be combined with --abort"},
			{[]string{"--max-replay-per-entry", "1", "--abort"}, "--max-replay-per-entry cannot be combined with --abort"},
			{[]string{"--max-replay-total", "1", "--abort"}, "--max-replay-total cannot be combined with --abort"},
			{[]string{"--approve-plan", token, "--abort"}, "--approve-plan cannot be combined with --abort"},
		} {
			tc := tc
			t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
				f := newScopedFixture(t)
				assertEarlyControlRefusal(t, f, tc.want, tc.args...)
			})
		}
	})

	t.Run("criterion_5_negative_limits", func(t *testing.T) {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"--max-replay-per-entry", "-1"}, "--max-replay-per-entry must be zero or greater"},
			{[]string{"--max-replay-total", "-1"}, "--max-replay-total must be zero or greater"},
		} {
			tc := tc
			t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
				f := newScopedFixture(t)
				assertEarlyControlRefusal(t, f, tc.want, tc.args...)
			})
		}
	})

	t.Run("criterion_6_approve_plan_without_limits", func(t *testing.T) {
		const want = "--approve-plan requires --max-replay-per-entry or --max-replay-total"
		for _, args := range [][]string{
			{"--approve-plan", token},
			{"--plan", "--approve-plan", token},
		} {
			args := args
			t.Run(strings.Join(args, "_"), func(t *testing.T) {
				f := newScopedFixture(t)
				assertEarlyControlRefusal(t, f, want, args...)
			})
		}
		// Adding either limit makes both forms legal.
		for _, args := range [][]string{
			{"--approve-plan", token, "--max-replay-total", "5", "--no-fetch"},
			{"--plan", "--approve-plan", token, "--max-replay-per-entry", "5", "--no-fetch"},
		} {
			args := args
			t.Run("legal_with_a_limit_"+strings.Join(args, "_"), func(t *testing.T) {
				f := newScopedFixture(t)
				_, stderr, _ := runSyncExecute(t, append([]string{f.feature}, args...)...)
				if strings.Contains(stderr, want) {
					t.Fatalf("supplying a limit must make the form legal, got %q", stderr)
				}
			})
		}
	})
}

// TestSyncPlanIntegration_Criterion22_7_ApprovePlanTokenShape is §22.7's
// executable owner: `deadbeef` is rejected with the 64-hex message, and a
// valid digest with surrounding whitespace is ACCEPTED.
func TestSyncPlanIntegration_Criterion22_7_ApprovePlanTokenShape(t *testing.T) {
	const shapeErr = "--approve-plan requires a 64-character lowercase hex fingerprint"

	t.Run("deadbeef_is_rejected", func(t *testing.T) {
		f := newScopedFixture(t)
		assertEarlyControlRefusal(t, f, shapeErr, "--approve-plan", "deadbeef", "--max-replay-total", "5")
	})

	t.Run("surrounding_whitespace_is_accepted", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch", "--max-replay-total", "50")
		if exit != 0 {
			t.Fatalf("--plan must exit 0: exit=%d stderr=%q", exit, stderr)
		}
		fp := planFieldString(t, stdout, "approval", "fingerprint")
		if len(fp) != 64 {
			t.Fatalf("expected a minted token, got %q", fp)
		}
		padded := "  " + fp + "\t\n"
		stdout, stderr, exit = runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch",
			"--max-replay-total", "50", "--approve-plan", padded)
		if exit != 0 {
			t.Fatalf("a padded but valid digest must be accepted: exit=%d stderr=%q", exit, stderr)
		}
		if strings.Contains(stderr, shapeErr) {
			t.Fatalf("a padded but valid digest must not trip the shape check: %q", stderr)
		}
		approval := planDoc(t, stdout)["approval"].(map[string]any)
		if approval["supplied"] != true {
			t.Fatalf("approval.supplied = %v, want true", approval["supplied"])
		}
		if approval["accepted"] != true {
			t.Fatalf("approval.accepted = %v, want true for the document's own token", approval["accepted"])
		}
	})
}

// TestSyncPlanIntegration_Criterion22_9_JSONStdoutIsExactlyOneValue is
// §22.9's executable owner: stdout is exactly one JSON value followed by
// exactly one 0x0A and no other byte.
func TestSyncPlanIntegration_Criterion22_9_JSONStdoutIsExactlyOneValue(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("--plan must exit 0: exit=%d stderr=%q", exit, stderr)
	}
	if len(stdout) == 0 || stdout[len(stdout)-1] != 0x0A {
		t.Fatalf("stdout must end with exactly one 0x0A, got %q", stdout)
	}
	body := stdout[:len(stdout)-1]
	if strings.Contains(body, "\n") {
		t.Fatalf("stdout carries a second newline inside the value:\n%s", stdout)
	}
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(body))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not one JSON value: %v", err)
	}
	if dec.More() {
		t.Fatal("stdout carries a SECOND JSON value")
	}
	if doc["schema_version"] != float64(1) {
		t.Fatalf("schema_version = %v, want 1", doc["schema_version"])
	}
	// len(stdout) == len(document) + 1, exactly.
	if len(stdout) != len(body)+1 {
		t.Fatalf("len(stdout) = %d, want len(document)+1 = %d", len(stdout), len(body)+1)
	}
	if strings.TrimSpace(body) != body {
		t.Fatalf("the document carries leading or trailing whitespace: %q", body)
	}
}

// TestSyncPlanIntegration_Criterion22_12_MismatchedApprovalIsAcceptedFalse is
// §22 criterion 12's own third cell: a VALID-but-mismatched approval token
// publishes `approval.accepted: false` and still exits 0 on the plan route.
func TestSyncPlanIntegration_Criterion22_12_MismatchedApprovalIsAcceptedFalse(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	mismatched := strings.Repeat("b", 64)
	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch",
		"--max-replay-total", "50", "--approve-plan", mismatched)
	if exit != 0 {
		t.Fatalf("a --plan with a mismatched token still exits 0: exit=%d stderr=%q", exit, stderr)
	}
	approval := planDoc(t, stdout)["approval"].(map[string]any)
	if approval["supplied"] != true {
		t.Fatalf("approval.supplied = %v, want true", approval["supplied"])
	}
	if approval["accepted"] != false {
		t.Fatalf("approval.accepted = %v, want false for a valid-but-mismatched token", approval["accepted"])
	}
	if fp, _ := approval["fingerprint"].(string); fp == mismatched {
		t.Fatal("the document must publish its OWN fingerprint, not the supplied one")
	}
}

// TestSyncPlanIntegration_Criterion22_17_PerEntryLimitRefusesFortyCandidates
// is §22 criterion 17 / 17a: a REAL 40-candidate row under
// `--max-replay-per-entry 10`, driven through production cli.Execute(),
// refuses with exactly one marker and moves no ref.
func TestSyncPlanIntegration_Criterion22_17_PerEntryLimitRefusesFortyCandidates(t *testing.T) {
	f := newScopedFixture(t)
	// Forty real candidates on the root row, over its recorded cutoff. The
	// fixture already carries one commit of its own, so thirty-nine more make
	// exactly forty.
	for i := 0; i < 39; i++ {
		writeAndCommit(t, f.wt("root"), fmt.Sprintf("c%02d.txt", i), "x\n", fmt.Sprintf("c%02d", i))
	}

	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("--plan must exit 0: exit=%d stderr=%q", exit, stderr)
	}
	max := 0
	for _, e := range planDoc(t, stdout)["entries"].([]any) {
		replay := e.(map[string]any)["replay"].(map[string]any)
		if c, ok := replay["candidate_count"].(float64); ok && int(c) > max {
			max = int(c)
		}
	}
	if max != 40 {
		t.Fatalf("the fixture's largest known candidate_count = %d, want 40", max)
	}

	// The refusal must be decided UP FRONT, at admission: the armed document
	// itself publishes the exceeded evaluation row and would_refuse, so a
	// regression that disabled the admission-time verdict and left only the
	// JIT seam to catch the same row could not keep this test green.
	armed, _, armedExit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch", "--max-replay-per-entry", "10")
	if armedExit != 0 {
		t.Fatalf("--plan must exit 0: exit=%d", armedExit)
	}
	armedDoc := planDoc(t, armed)
	guard := planField(t, armedDoc, "guard").(map[string]any)
	rows, _ := guard["evaluation"].([]any)
	exceeded := false
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["basis"] != "per-entry" {
			continue
		}
		if row["verdict"] == "exceeded" {
			exceeded = true
			if got, _ := row["limit"].(float64); int(got) != 10 {
				t.Fatalf("evaluation row limit = %v, want the armed 10", row["limit"])
			}
			if got, _ := row["value"].(float64); int(got) != 40 {
				t.Fatalf("evaluation row value = %v, want the measured 40", row["value"])
			}
		}
	}
	if !exceeded {
		t.Fatalf("guard.evaluation[] = %v, want a per-entry row with verdict exceeded BEFORE any rebase runs", rows)
	}
	if got := guard["would_refuse"]; got != true {
		t.Fatalf("guard.would_refuse = %v, want true: the refusal is decided at admission, not at the JIT seam", got)
	}
	if got := planField(t, armedDoc, "refusal", "kind"); got != "limit-per-entry" {
		t.Fatalf("refusal.kind = %v, want limit-per-entry", got)
	}
	// Ranks 11/12 never force runnable:false (§7.1's own column), so the
	// admission refusal is carried by refusal.kind and would_refuse — which
	// is exactly why both are asserted here rather than the runnable bit.
	if got := planField(t, armedDoc, "runnable"); got != true {
		t.Fatalf("runnable = %v, want true: a limit is not a rank 1-10 fact", got)
	}

	before := map[string]string{}
	for _, n := range []string{"root", "parent", "child"} {
		before[n] = f.sha(t, n)
	}
	_, stderr, exit = runSyncExecute(t, f.feature, "--no-fetch", "--max-replay-per-entry", "10")
	if exit != 1 {
		t.Fatalf("40 candidates against a per-entry limit of 10 must refuse: exit=%d stderr=%q", exit, stderr)
	}
	if !strings.Contains(stderr, "limit-per-entry") {
		t.Fatalf("stderr = %q, want a limit-per-entry refusal", stderr)
	}
	assertExactlyOnePlanGuardMarker(t, stderr)
	for _, n := range []string{"root", "parent", "child"} {
		if after := f.sha(t, n); after != before[n] {
			t.Fatalf("%s moved from %s to %s; an up-front limit refusal moves no ref", n, before[n], after)
		}
	}
}

// ===========================================================================
// §11.8 collateral membership is RANGE membership — the real-Git regression.
// ===========================================================================

// TestSyncPlanIntegration_CollateralMembershipIsRangeMembership is the
// mutation-sensitive regression for §11.8's own membership test: a ref
// belongs in `collateral_refs` exactly when its tip lies inside
// `<upstream>..<branch>`, i.e. `isAncestor(tip, branch) &&
// !isAncestor(tip, upstream)`.
//
// The fixture puts BOTH cells the reversed spelling gets wrong on the same
// row of one real repository:
//
//   - a MERGED-SIDE ref whose tip is reachable from the branch but is NOT
//     descended from the upstream. `--update-refs` really moves it, so it
//     MUST be listed; the reversed test (`isAncestor(upstream, tip)`) omits
//     it;
//   - the recorded CUTOFF ref itself, which is the range's excluded left
//     endpoint. It is never replayed, so it MUST NOT be listed; the reversed
//     test includes it.
//
// A row whose listing satisfies only one of the two would pass a weaker
// assertion, so both are asserted on the same document.
func TestSyncPlanIntegration_CollateralMembershipIsRangeMembership(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	// The commit the side branch forks from — strictly BEFORE the cutoff, so
	// the cutoff is not an ancestor of the side tip and the reversed
	// ancestry test cannot reach it.
	preCutoff := gitOutput(t, repo, "rev-parse", "HEAD")

	// master advances so the row has a real recorded cutoff behind it.
	writeAndCommit(t, repo, "m1.txt", "m1\n", "m1")
	cutoff := gitOutput(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "branch", "the-cutoff-ref", cutoff)

	if err := createWorktree("feature", "row", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	wt := internal.WorktreePath("feature", "row")

	// A side branch forked from BEFORE the cutoff, then merged into the
	// row's own branch: its tip is reachable from the row but is NOT
	// descended from the upstream the row replays from.
	gitRun(t, wt, "checkout", "-q", "-b", "side", preCutoff)
	writeAndCommit(t, wt, "side.txt", "side\n", "side work")
	sideTip := gitOutput(t, wt, "rev-parse", "HEAD")
	if internal.RunSilentDir(wt, "git", "merge-base", "--is-ancestor", cutoff, sideTip) == nil {
		t.Fatalf("fixture is vacuous: the cutoff %s IS an ancestor of the side tip %s", cutoff, sideTip)
	}
	gitRun(t, wt, "checkout", "-q", "row")
	writeAndCommit(t, wt, "row.txt", "row\n", "row work")
	gitRun(t, wt, "merge", "--no-ff", "-m", "merge side", "side")

	// master moves past the cutoff, so the row is an `onto` arm whose
	// recorded cutoff really is behind its base.
	writeAndCommit(t, repo, "m2.txt", "m2\n", "m2")
	gitRun(t, repo, "push", "origin", "master")
	gitRun(t, repo, "fetch", "origin")

	featurePath := internal.FeaturePath("feature")
	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "row", Base: "master", Repo: repo, LastBaseSHA: cutoff},
	}}
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runSyncExecute(t, "feature", "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("--plan must exit 0: exit=%d stderr=%q", exit, stderr)
	}
	entries := planDoc(t, stdout)["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("expected exactly one row, got %#v", entries)
	}
	row := entries[0].(map[string]any)
	replay := row["replay"].(map[string]any)
	if replay["upstream_sha"] != cutoff {
		t.Fatalf("replay.upstream_sha = %v, want the recorded cutoff %s", replay["upstream_sha"], cutoff)
	}

	listed := map[string]string{}
	raw, ok := row["collateral_refs"].([]any)
	if !ok {
		t.Fatalf("collateral_refs = %#v, want a measured array", row["collateral_refs"])
	}
	for _, v := range raw {
		r := v.(map[string]any)
		listed[r["ref"].(string)] = r["sha"].(string)
	}

	// (a) the merged side ref IS collateral: its tip is inside the range.
	if got, present := listed["refs/heads/side"]; !present {
		t.Fatalf("collateral_refs = %v, want refs/heads/side: its tip %s is reachable from the row but NOT descended from the upstream %s — this is exactly the ref the reversed ancestry test omits",
			listed, sideTip, cutoff)
	} else if got != sideTip {
		t.Fatalf("collateral_refs[refs/heads/side].sha = %s, want %s", got, sideTip)
	}

	// (b) the cutoff ref is NOT collateral: it is the range's excluded left
	// endpoint, and the reversed test would include it.
	if _, present := listed["refs/heads/the-cutoff-ref"]; present {
		t.Fatalf("collateral_refs = %v must NOT list the recorded cutoff itself: `<upstream>..<branch>` excludes its left endpoint", listed)
	}

	// The mechanism itself is still argv-derived and unaffected.
	if row["collateral_mechanism"] != "argv" {
		t.Fatalf("collateral_mechanism = %v, want argv (the unscoped pass-1 shape carries --update-refs)", row["collateral_mechanism"])
	}
}

// ===========================================================================
// §16 rules 3a/3b — the two capability gates fire ABOVE the fetch and above
// every config read, at document level, with no per-row variant.
// ===========================================================================

// capabilityGateDoc drives one --plan --json invocation under a stubbed
// `git --version` and returns the document plus the sidecar records.
func capabilityGateDoc(t *testing.T, f *scopedFixture, realGit, versionLine string, args ...string) (map[string]any, []gitRecord) {
	t.Helper()
	origPath := os.Getenv("PATH")
	stubDir := newGitVersionStubPATH(t, realGit, versionLine)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)
	w := newSyncGitWrapper(t, false)
	var stdout, stderr string
	var exit int
	w.around(t, func() {
		stdout, stderr, exit = runSyncExecute(t, append([]string{f.feature, "--plan", "--json"}, args...)...)
	})
	if exit != 0 {
		t.Fatalf("--plan always exits 0 under %q: exit=%d stderr=%q", versionLine, exit, stderr)
	}
	return planDoc(t, stdout), w.records(t)
}

// documentLevelBlockersOfKind returns every entry:null blocker of one kind.
func documentLevelBlockersOfKind(t *testing.T, doc map[string]any, kind string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range doc["blockers"].([]any) {
		b := raw.(map[string]any)
		if b["kind"] == kind && b["entry"] == nil {
			out = append(out, b)
		}
	}
	return out
}

// TestSyncPlanIntegration_CapabilityGatesFireAboveTheFetch is the executable
// owner for §16 rules 3a/3b's REQUIRED position.
//
//	2.25 — below BOTH gates: one config-show-scope blocker and one
//	       update-refs blocker, both entry:null rank 5.9, runnable false,
//	       zero `git fetch` and zero `config --list --show-scope`;
//	2.37 — below only the 2.38 gate: exactly one update-refs blocker, and
//	       the fetch is still suppressed;
//	2.39 — above both: no capability blocker, and the DEFAULT-fetch route
//	       really fetches.
//
// Every cell also asserts there is NO per-row variant: no entry-scoped
// probe-failed blocker naming --update-refs.
func TestSyncPlanIntegration_CapabilityGatesFireAboveTheFetch(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("need a real git on PATH: %v", err)
	}

	countVerb := func(records []gitRecord, pred func([]string) bool) int {
		n := 0
		for _, r := range records {
			if pred(r.Tail()) {
				n++
			}
		}
		return n
	}
	isFetch := func(tail []string) bool { return len(tail) > 0 && tail[0] == "fetch" }
	isShowScope := func(tail []string) bool {
		return len(tail) >= 3 && tail[0] == "config" && tail[1] == "--list" && tail[2] == "--show-scope"
	}
	// assertCause2 is §11.2 cause 2's own attribution for a refusing
	// capability gate on a route whose effective policy is FETCH: the fetch
	// this route called for could not be reached, so the document publishes
	// the suppression and its freshness token — never the `local-only` shape
	// of a route that never wanted a fetch — with nothing attempted and the
	// outcome skipped.
	assertCause2 := func(t *testing.T, doc map[string]any) {
		t.Helper()
		fetch := planField(t, doc, "fetch").(map[string]any)
		if got := fetch["suppression_cause"]; got != "not-refreshed-no-plan-subject" {
			t.Fatalf("fetch.suppression_cause = %v, want not-refreshed-no-plan-subject: a refusing capability gate IS §11.2 cause 2", got)
		}
		if got := planField(t, doc, "freshness"); got != "not-refreshed-no-plan-subject" {
			t.Fatalf("freshness = %v, want not-refreshed-no-plan-subject (never local-only: this route's policy asked for a fetch)", got)
		}
		if got := fetch["attempted"]; got != false {
			t.Fatalf("fetch.attempted = %v, want false", got)
		}
		if got := fetch["outcome"]; got != "skipped" {
			t.Fatalf("fetch.outcome = %v, want skipped", got)
		}
		if repos, _ := fetch["repos"].([]any); len(repos) != 0 {
			t.Fatalf("fetch.repos = %v, want [] for a cause 1-3 suppression", repos)
		}
		// Rows are NOT suppressed with the fetch: the operator must still see
		// which argv the host would reject.
		if rows, _ := planField(t, doc, "summary", "plannability").(string); rows != "rows" {
			t.Fatalf("summary.plannability = %q, want rows: a capability gate suppresses the FETCH, never the document's rows", rows)
		}
		if entries, _ := planField(t, doc, "entries").([]any); len(entries) == 0 {
			t.Fatalf("entries[] is empty; a capability-gated document still publishes its rows")
		}
	}
	assertLocalOnly := func(t *testing.T, doc map[string]any) {
		t.Helper()
		fetch := planField(t, doc, "fetch").(map[string]any)
		if got := fetch["suppression_cause"]; got != nil {
			t.Fatalf("fetch.suppression_cause = %v, want null: a --no-fetch route suppressed nothing", got)
		}
		if got := planField(t, doc, "freshness"); got != "local-only" {
			t.Fatalf("freshness = %v, want local-only on a --no-fetch route", got)
		}
	}
	assertNoPerRowVariant := func(t *testing.T, doc map[string]any) {
		t.Helper()
		for _, raw := range doc["blockers"].([]any) {
			b := raw.(map[string]any)
			if b["entry"] == nil {
				continue
			}
			if detail, _ := b["detail"].(string); strings.Contains(detail, "--update-refs") {
				t.Fatalf("§16 rule 3b has NO per-row variant, got the row blocker %v", b)
			}
		}
	}

	t.Run("2.25_below_both_gates", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		doc, records := capabilityGateDoc(t, f, realGit, "git version 2.25.0")

		if doc["runnable"] != false {
			t.Fatalf("runnable = %v, want false", doc["runnable"])
		}
		gates := documentLevelBlockersOfKind(t, doc, "probe-failed")
		if len(gates) != 2 {
			t.Fatalf("want exactly two entry:null rank 5.9 gates (2.26 and 2.38), got %v", gates)
		}
		var sawScope, sawRefs bool
		for _, b := range gates {
			detail := b["detail"].(string)
			if strings.Contains(detail, "--show-scope") {
				sawScope = true
			}
			if strings.Contains(detail, "--update-refs") {
				sawRefs = true
			}
		}
		if !sawScope || !sawRefs {
			t.Fatalf("both capability gates must be published, got %v", gates)
		}
		if n := countVerb(records, isFetch); n != 0 {
			t.Fatalf("a refusing capability gate must suppress the fetch, got %d `git fetch`", n)
		}
		if n := countVerb(records, isShowScope); n != 0 {
			t.Fatalf("a host below 2.26 must issue ZERO `config --list --show-scope`, got %d", n)
		}
		assertCause2(t, doc)
		assertNoPerRowVariant(t, doc)

		// The same host under an explicit --no-fetch suppressed nothing.
		n := newScopedFixture(t)
		n.advanceRoot(t)
		noFetchDoc, noFetchRecords := capabilityGateDoc(t, n, realGit, "git version 2.25.0", "--no-fetch")
		assertLocalOnly(t, noFetchDoc)
		if got := countVerb(noFetchRecords, isFetch); got != 0 {
			t.Fatalf("a --no-fetch route must issue no fetch at all, got %d", got)
		}

		// The CHECKOUT twin attributes the same cause on its own fetching route.
		checkoutDoc := checkoutCapabilityGateDoc(t, realGit, "git version 2.25.0", "--fetch")
		assertCause2(t, checkoutDoc)
		// The gate is non-waivable: an armed guarded twin still refuses.
		g := newScopedFixture(t)
		g.advanceRoot(t)
		origPath := os.Getenv("PATH")
		stubDir := newGitVersionStubPATH(t, realGit, "git version 2.25.0")
		t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)
		_, stderr, exit := runSyncExecute(t, g.feature, "--max-replay-total", "50")
		if exit != 1 {
			t.Fatalf("the guarded twin must refuse below the gate: exit=%d stderr=%q", exit, stderr)
		}
		if !strings.Contains(stderr, "probe-failed") {
			t.Fatalf("stderr = %q, want the rank 5.9 probe-failed refusal", stderr)
		}
	})

	t.Run("2.37_below_only_the_update_refs_gate", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		doc, records := capabilityGateDoc(t, f, realGit, "git version 2.37.0")

		if doc["runnable"] != false {
			t.Fatalf("runnable = %v, want false", doc["runnable"])
		}
		gates := documentLevelBlockersOfKind(t, doc, "probe-failed")
		if len(gates) != 1 {
			t.Fatalf("want exactly ONE entry:null rank 5.9 gate at 2.37, got %v", gates)
		}
		if !strings.Contains(gates[0]["detail"].(string), "--update-refs") {
			t.Fatalf("the 2.37 gate must be the --update-refs one, got %v", gates[0])
		}
		if n := countVerb(records, isFetch); n != 0 {
			t.Fatalf("the 2.38 gate sits ABOVE the fetch, got %d `git fetch`", n)
		}
		assertCause2(t, doc)
		assertNoPerRowVariant(t, doc)

		// The guarded twin refuses over the SAME attribution, so an armed run
		// cannot slip past a gate that suppressed its own fetch.
		g2 := newScopedFixture(t)
		g2.advanceRoot(t)
		origPath := os.Getenv("PATH")
		stubDir := newGitVersionStubPATH(t, realGit, "git version 2.37.0")
		t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)
		_, stderr, exit := runSyncExecute(t, g2.feature, "--max-replay-total", "50")
		if exit != 1 || !strings.Contains(stderr, "probe-failed") {
			t.Fatalf("the 2.37 guarded twin must refuse rank 5.9: exit=%d stderr=%q", exit, stderr)
		}

		// A SCOPED run publishes no --update-refs argv at all, so the same
		// host is unaffected: rule 3b is argv-derived.
		g := newScopedFixture(t)
		g.advanceRoot(t)
		scoped, _ := capabilityGateDoc(t, g, realGit, "git version 2.37.0", "--only", "root")
		if len(documentLevelBlockersOfKind(t, scoped, "probe-failed")) != 0 {
			t.Fatalf("a SCOPED run publishes no --update-refs argv, so rule 3b must not fire: %v", scoped["blockers"])
		}
	})

	t.Run("2.39_above_both_gates_fetches_by_default", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		doc, records := capabilityGateDoc(t, f, realGit, "git version 2.39.0")
		if doc["runnable"] != true {
			t.Fatalf("runnable = %v, want true above both gates", doc["runnable"])
		}
		if n := len(documentLevelBlockersOfKind(t, doc, "probe-failed")); n != 0 {
			t.Fatalf("want no capability gate at 2.39, got %d", n)
		}
		if n := countVerb(records, isFetch); n != 1 {
			t.Fatalf("the DEFAULT-fetch route must issue exactly one `git fetch` above the gates, got %d", n)
		}
		fetch := planField(t, doc, "fetch").(map[string]any)
		if got := fetch["suppression_cause"]; got != nil {
			t.Fatalf("fetch.suppression_cause = %v, want null above both gates", got)
		}
		if got := fetch["attempted"]; got != true {
			t.Fatalf("fetch.attempted = %v, want true above both gates", got)
		}
	})
}

// checkoutCapabilityGateDoc is the checkout-mode twin of capabilityGateDoc:
// the same version stub, over a checkout-mode workspace, so §16's gates and
// their §11.2 cause-2 attribution are asserted in BOTH modes.
func checkoutCapabilityGateDoc(t *testing.T, realGit, versionLine string, args ...string) map[string]any {
	t.Helper()
	dir, _ := checkoutModeFixture(t)
	writeCheckoutModeMarker(t, dir)
	withUnifiedWorkspaceEnv(t, dir)
	origPath := os.Getenv("PATH")
	stubDir := newGitVersionStubPATH(t, realGit, versionLine)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)
	stdout, stderr, exit := runSyncExecute(t, append([]string{"test-feature", "--plan", "--json"}, args...)...)
	if exit != 0 {
		t.Fatalf("--plan always exits 0 under %q: exit=%d stderr=%q", versionLine, exit, stderr)
	}
	return planDoc(t, stdout)
}

// TestSyncPlanIntegration_PushContextReusesTheOneOrderedInventory is the
// executable owner for §22.27a's push half: the push mapping read REUSES the
// ordered inventory its own push context already holds, so a `--push`
// document adds EXACTLY ONE `config --list --show-scope -z` per distinct push
// context — never a second one for MeasurePushRemoteFacts on top of
// ResolvePushContext's own.
//
// The fixture is two linked worktrees of ONE repository, so the run has two
// execution contexts and exactly one push context.
func TestSyncPlanIntegration_PushContextReusesTheOneOrderedInventory(t *testing.T) {
	setupTwoWorktreeExternalFixture(t)

	inventoryReads := func(t *testing.T, args ...string) []string {
		t.Helper()
		w := newSyncGitWrapper(t, false)
		var exit int
		var stderr string
		w.around(t, func() {
			_, stderr, exit = runSyncExecute(t, append([]string{"feature", "--plan", "--json"}, args...)...)
		})
		if exit != 0 {
			t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
		}
		var cwds []string
		for _, r := range w.records(t) {
			tail := r.Tail()
			if len(tail) >= 4 && tail[0] == "config" && tail[1] == "--list" && tail[2] == "--show-scope" && tail[3] == "-z" {
				cwds = append(cwds, r.Cwd)
			}
		}
		return cwds
	}

	withoutPush := inventoryReads(t, "--no-fetch")
	withPush := inventoryReads(t, "--no-fetch", "--push")

	if len(withPush) != len(withoutPush)+1 {
		t.Fatalf("a --push document issued %d ordered inventory reads (%v); the same document without --push issued %d (%v).\n"+
			"--push adds EXACTLY ONE read, for its own single push context: MeasurePushRemoteFacts must reuse it rather than probe again (§14.1a rule 7, §22.27a)",
			len(withPush), withPush, len(withoutPush), withoutPush)
	}

	// The absolute budget, not just the delta: two linked worktrees are two
	// execution contexts, and --push adds its own single push context.
	if len(withoutPush) != 2 {
		t.Fatalf("a two-worktree document issued %d ordered inventory reads (%v), want exactly one per execution context",
			len(withoutPush), withoutPush)
	}
	if len(withPush) != 3 {
		t.Fatalf("the --push document issued %d ordered inventory reads (%v), want 3: two execution contexts plus ONE push context",
			len(withPush), withPush)
	}
}

// ---------------------------------------------------------------------------
// §23.1 item 6: a Git PATH wrapper that fails exactly one probe class and
// records cwd, argv, ENVIRONMENT and process count. The failure is one-shot:
// exactly one process of exactly one class fails, and every other process in
// the run — including later processes of the same class — is executed by the
// real git untouched.
// ---------------------------------------------------------------------------

// probeClassWrapperScript is the §23.1 item 6 wrapper. It records one block
// per invocation — cwd, the full argv and the environment variables that
// decide Git's own behaviour — and fails exactly one probe class, and only
// while the arm file exists, so an admission-time probe of the same class
// succeeds and the JIT seam's own probe fails. Every other process is
// executed by the real git untouched.
const probeClassWrapperScript = `#!/bin/sh
log="$TWS_PROBE_LOG"
real="$TWS_PROBE_REAL"
{
	printf 'cwd\t%s\n' "$PWD"
	printf 'argv'
	for a in "$@"; do printf '\t%s' "$a"; done
	printf '\n'
	for v in GIT_DIR GIT_WORK_TREE GIT_CONFIG_COUNT GIT_CONFIG_NOSYSTEM GIT_TERMINAL_PROMPT GIT_OPTIONAL_LOCKS; do
		eval "val=\$$v"
		printf 'env\t%s=%s\n' "$v" "$val"
	done
} >> "$log"
class=""
skip=0
for a in "$@"; do
	if [ "$skip" = "1" ]; then
		skip=0
		continue
	fi
	case "$a" in
	-C|-c) skip=1; continue ;;
	-*) continue ;;
	*) class="$a"; break ;;
	esac
done
if [ -n "$TWS_PROBE_FAIL_CLASS" ] && [ "$class" = "$TWS_PROBE_FAIL_CLASS" ] && [ -f "$TWS_PROBE_ARM" ]; then
	printf 'failed\t%s\n' "$class" >> "$log"
	rm -f "$TWS_PROBE_ARM"
	echo "fatal: injected $class failure" >&2
	exit 128
fi
exec "$real" "$@"
`

// probeClassWrapper is the installed seam.
type probeClassWrapper struct {
	dir     string
	log     string
	armPath string
}

// probeInvocation is one recorded process.
type probeInvocation struct {
	Cwd    string
	Argv   []string
	Env    map[string]string
	Failed bool
}

// Class is the first non-option argument — the probe class the wrapper keys
// its single failure on.
func (p probeInvocation) Class() string {
	skip := false
	for _, a := range p.Argv {
		if skip {
			skip = false
			continue
		}
		switch {
		case a == "-C" || a == "-c":
			skip = true
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// newProbeClassWrapper installs the wrapper on PATH for the duration of the
// test, resolving the real git first so the script never re-resolves it.
func newProbeClassWrapper(t *testing.T, failClass string) *probeClassWrapper {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("real git must be resolvable before the wrapper is installed: %v", err)
	}
	abs, err := filepath.Abs(realGit)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	w := &probeClassWrapper{
		dir:     dir,
		log:     filepath.Join(dir, "probe.log"),
		armPath: filepath.Join(dir, "armed"),
	}
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(probeClassWrapperScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TWS_PROBE_REAL", abs)
	t.Setenv("TWS_PROBE_LOG", w.log)
	t.Setenv("TWS_PROBE_ARM", w.armPath)
	t.Setenv("TWS_PROBE_FAIL_CLASS", failClass)
	return w
}

// arm makes the wrapper start failing its one class from this moment on.
func (w *probeClassWrapper) arm(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(w.armPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// invocations parses the recorded blocks in order.
func (w *probeClassWrapper) invocations(t *testing.T) []probeInvocation {
	t.Helper()
	data, err := os.ReadFile(w.log)
	if err != nil {
		t.Fatal(err)
	}
	var out []probeInvocation
	var cur *probeInvocation
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		switch fields[0] {
		case "cwd":
			out = append(out, probeInvocation{Cwd: fields[1], Env: map[string]string{}})
			cur = &out[len(out)-1]
		case "argv":
			if cur != nil {
				cur.Argv = append([]string{}, fields[1:]...)
			}
		case "env":
			if cur != nil {
				kv := strings.SplitN(fields[1], "=", 2)
				if len(kv) == 2 {
					cur.Env[kv[0]] = kv[1]
				}
			}
		case "failed":
			if cur != nil {
				cur.Failed = true
			}
		}
	}
	return out
}

// TestSyncPlanIntegration_Criterion22_33i_SeamForEachRefFailureRefuses is the
// named owner of §22.33i (v-e-1)'s failed-`for-each-ref` cell, and of §23.1
// item 6's own contract. A full-scope guarded run is admitted with a real
// token; the wrapper is armed at the moment the run reaches its rebasing
// stage, so the admission-time ref inventory succeeded and the JIT seam's
// own single `for-each-ref` is the one process that fails. The run must
// refuse rank 5.9 `probe-failed` before any `git rebase` runs, and every
// other process in the run must have executed untouched.
func TestSyncPlanIntegration_Criterion22_33i_SeamForEachRefFailureRefuses(t *testing.T) {
	f := newScopedFixture(t)
	planOut, _, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch", "--max-replay-total", "50")
	if exit != 0 {
		t.Fatalf("plan exit=%d", exit)
	}
	fp := planFieldString(t, planOut, "approval", "fingerprint")
	if len(fp) != 64 {
		t.Fatalf("no fingerprint minted: %q", fp)
	}

	w := newProbeClassWrapper(t, "for-each-ref")
	withSyncStepHook(t, func(stage internal.SyncRunStage, index int) error {
		if stage == internal.SyncStageRebasing {
			w.arm(t)
		}
		return nil
	})

	_, stderr, exit2 := runSyncExecute(t, f.feature, "--no-fetch", "--max-replay-total", "50", "--approve-plan", fp)

	invocations := w.invocations(t)
	if len(invocations) == 0 {
		t.Fatal("the wrapper recorded no processes: it was not on PATH for the run")
	}

	// (a) the wrapper really records the environment, not just argv/cwd.
	for _, inv := range invocations {
		if _, ok := inv.Env["GIT_CONFIG_COUNT"]; !ok {
			t.Fatalf("invocation %v recorded no environment", inv.Argv)
		}
		if inv.Cwd == "" {
			t.Fatalf("invocation %v recorded no cwd", inv.Argv)
		}
	}

	// (b) exactly one process failed, and it was the armed class.
	failures := 0
	for _, inv := range invocations {
		if inv.Failed {
			failures++
			if inv.Class() != "for-each-ref" {
				t.Fatalf("the wrapper failed a %q process; it must fail exactly the armed class", inv.Class())
			}
		}
	}
	if failures != 1 {
		t.Fatalf("%d processes failed, want exactly 1: the seam fails one probe class, once", failures)
	}

	// (c) the failure refuses the run, and no rebase runs after it.
	if exit2 == 0 {
		t.Fatalf("a failed seam ref inventory must refuse the run; stderr=%q", stderr)
	}
	failedAt := -1
	for i, inv := range invocations {
		if inv.Failed {
			failedAt = i
		}
	}
	for _, inv := range invocations[failedAt+1:] {
		if inv.Class() == "rebase" {
			t.Fatalf("a rebase ran after the seam's ref inventory failed: %v", inv.Argv)
		}
	}

	// (d) every other process ran for real: the wrapper leaves them intact,
	// which is what makes the refusal attributable to the one failed class.
	for i, inv := range invocations {
		if i != failedAt && inv.Failed {
			t.Fatalf("invocation %d (%v) also failed; the seam must fail exactly one process", i, inv.Argv)
		}
	}

	matches := planGuardMarkerRe.FindAllString(stderr, -1)
	if len(matches) != 1 {
		t.Fatalf("expected exactly one plan-guard marker line, got %d: %v", len(matches), matches)
	}
	if !strings.Contains(matches[0], "probe-failed") {
		t.Fatalf("marker = %q, want rank 5.9 probe-failed: an UNMEASURED ref inventory is never read as \"no collateral\"", matches[0])
	}
}

// ---------------------------------------------------------------------------
// §22.19: a token cannot waive any rank 1-10 fact — the runtime table.
// ---------------------------------------------------------------------------

// criterion19Case is one row of §22.19's seventeen-kind table: a hazard a
// REAL fixture installs, and the rank 1-10 kind the production document must
// publish for it. setup returns the feature the run is about and the argv
// (limits, route flags) the hazard needs, so a continuation-only kind and a
// fresh-run kind are driven by the same table.
type criterion19Case struct {
	kind  string
	setup func(t *testing.T) (feature string, args []string)
}

// planBlockerKinds lists blockers[].kind of a plan document, in document
// order, so a table row can assert its own kind is really published by
// production code rather than merely expected.
func planBlockerKinds(t *testing.T, doc map[string]any) []string {
	t.Helper()
	raw, ok := planField(t, doc, "blockers").([]any)
	if !ok {
		return nil
	}
	var kinds []string
	for _, b := range raw {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if k, ok := m["kind"].(string); ok {
			kinds = append(kinds, k)
		}
	}
	return kinds
}

// TestSyncPlanIntegration_Criterion22_19_TokenCannotWaiveRankOneToTenEndToEnd
// is §22.19's RUNTIME owner: for each of the seventeen rank 1-10 kinds, a
// real fixture installs the hazard, the production `--plan --json` document
// is asked for its own approval fingerprint, and the guarded run is then
// armed with THAT token — an approval the production code itself accepts
// (approval.accepted == true). Every row must still refuse, must never name
// its kind in approval.covers.waived_kinds, and must never set
// hard_blockers_waived. The positive control lives in the same package
// (TestSyncPlanGuard_GuardEvaluationLimitsAndWaiverDomains): the same
// accepted token really does waive the two limit kinds, so this table cannot
// pass by the waiver being a no-op.
func TestSyncPlanIntegration_Criterion22_19_TokenCannotWaiveRankOneToTenEndToEnd(t *testing.T) {
	cases := criterion19Cases()
	if len(cases) != 17 {
		t.Fatalf("§22.19 names 17 kinds, this table drives %d", len(cases))
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.kind] {
			t.Fatalf("kind %q is driven twice; each of the seventeen needs its own row", c.kind)
		}
		seen[c.kind] = true
	}

	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			feature, args := c.setup(t)

			// 1. The unarmed document decides which token this run can even
			//    present: a hazard that makes the tuple unmintable (rank 5.08
			//    identity-not-utf8) or a limitless continuation (rank 7.5)
			//    publishes approval.fingerprint: null, and no token can match.
			planArgs := append([]string{feature, "--plan", "--json"}, args...)
			planOut, _, _ := runSync(t, planArgs...)
			doc := planDoc(t, planOut)
			fp, _ := planField(t, doc, "approval", "fingerprint").(string)
			mintable := fp != ""
			if !mintable {
				if usable, _ := planField(t, doc, "approval", "usable").(bool); usable {
					t.Fatal("no fingerprint was minted, yet approval.usable is true")
				}
				fp = strings.Repeat("a", 64)
			}

			// 2. The ARMED document: the production run is given that token.
			armedOut, _, _ := runSync(t, append(append([]string{}, planArgs...), "--approve-plan", fp)...)
			armed := planDoc(t, armedOut)

			// The blocker survives the accepted token: it is neither removed
			// from blockers[] nor rewritten into a waived evaluation row.
			kinds := planBlockerKinds(t, armed)
			found := false
			for _, k := range kinds {
				if k == c.kind {
					found = true
				}
			}
			if !found {
				t.Fatalf("the armed document published blockers %v, want one of kind %q", kinds, c.kind)
			}

			accepted, hasAccepted := planField(t, armed, "approval", "accepted").(bool)
			if !hasAccepted {
				t.Fatalf("approval.accepted is null on a run that supplied a token")
			}
			if mintable && !accepted {
				t.Fatal("approval.accepted = false: the token this very document minted must match")
			}
			if hard, _ := planField(t, armed, "approval", "covers", "hard_blockers_waived").(bool); hard {
				t.Fatal("hard_blockers_waived must never be true: ranks 1-10 are not waivable")
			}
			waived, _ := planField(t, armed, "approval", "covers", "waived_kinds").([]any)
			for _, w := range waived {
				if s, _ := w.(string); s == c.kind {
					t.Fatalf("kind %q appears in waived_kinds; ranks 1-10 are non-waivable", c.kind)
				}
			}
			// Ranks 7/7.5 are non-hard rows (§7.1's own "forces runnable:false"
			// column), so runnable is asserted only where the rank forces it;
			// what every row must show is that the ACCEPTED token neither
			// removed the blocker nor waived the kind, and that the run
			// refuses.

			// 3. The guarded RUN, armed with the same token, still refuses.
			execArgs := append(append([]string{feature}, args...), "--approve-plan", fp)
			_, stderr, exit := runSyncExecute(t, execArgs...)
			if exit == 0 {
				t.Fatalf("the guarded run completed under a rank 1-10 fact %q; stderr=%q", c.kind, stderr)
			}
			// The refusal may be published either as this feature's own
			// plan-guard marker or as one of the shipped refusal sentences the
			// ladder already owned before the guard — both are refusals; what
			// must never happen is a second marker or a completed run.
			markers := criterion19MarkerRe.FindAllString(stderr, -1)
			if len(markers) > 1 {
				t.Fatalf("expected at most one plan-guard marker, got %d: %v", len(markers), markers)
			}
			if len(markers) == 1 && !strings.Contains(markers[0], ": ") {
				t.Fatalf("marker %q is not a kind: detail line", markers[0])
			}
		})
	}
}

// criterion19MarkerRe matches a plan-guard marker line whose kind may carry
// digits (identity-not-utf8), which the narrower golden-path regex cannot.
var criterion19MarkerRe = regexp.MustCompile(`(?m)^plan-guard: [a-z][a-z0-9-]*: .*$`)

// saveSwitchedCheckoutTransaction persists a checkout transaction parked in
// the `switched` stage with a pinned destination, which is the arm whose next
// command is the rebase of whatever HEAD is on (§13.3a rule 5).
func saveSwitchedCheckoutTransaction(t *testing.T, dir, fp string) {
	t.Helper()
	tx := &internal.CheckoutTransaction{
		StateVersion:   internal.CheckoutTransactionGuardedVersion,
		Route:          internal.RouteNewMode,
		Feature:        "test-feature",
		OriginalBranch: "main",
		OriginalHEAD:   gitSHA(t, dir, "main"),
		Stage:          internal.StageSwitched,
		Plan: []internal.CheckoutPlanEntry{
			{Name: "feat-a", Branch: "feat-a", Base: "feat-root", NewBaseSHA: gitSHA(t, dir, "feat-root")},
		},
	}
	if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { internal.DeleteCheckoutTransaction(fp) })
}

// installFailingProbeClass puts a git on PATH that fails EVERY invocation of
// one class and executes every other process for real — the fixture a
// candidate-probe downgrade (rank 2f (unknown, probe-failed)) needs.
func installFailingProbeClass(t *testing.T, class string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(realGit)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := `#!/bin/sh
c=""
skip=0
for a in "$@"; do
	if [ "$skip" = "1" ]; then skip=0; continue; fi
	case "$a" in
	-C|-c) skip=1; continue ;;
	-*) continue ;;
	*) c="$a"; break ;;
	esac
done
if [ "$c" = "$TWS_FAIL_CLASS" ]; then
	echo "fatal: injected $c failure" >&2
	exit 128
fi
exec "$TWS_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_REAL_GIT", abs)
	t.Setenv("TWS_FAIL_CLASS", class)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// criterion19Cases builds the seventeen hazards §22.19 enumerates, each from
// a real repository/state fixture and each driven through the production
// document. Where a kind is only reachable on a continuation, the row's own
// setup builds that continuation.
func criterion19Cases() []criterion19Case {
	limits := []string{"--no-fetch", "--max-replay-total", "50"}
	withLimits := func(extra ...string) []string { return append(append([]string{}, limits...), extra...) }

	return []criterion19Case{
		{kind: "state-refused", setup: func(t *testing.T) (string, []string) {
			// A persisted transaction a FRESH checkout run would have to
			// overwrite: gate 1 of §13.7 refuses it, rank 1.
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			saveGuardedCheckoutTransaction(t, dir, fp, nil, nil)
			return "test-feature", withLimits()
		}},
		{kind: "preflight-refused", setup: func(t *testing.T) (string, []string) {
			// §13.3a rule 5: a `switched` continuation whose HEAD is not the
			// persisted branch would rebase the wrong thing — rank 4.
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			saveSwitchedCheckoutTransaction(t, dir, fp)
			gitRunCS(t, dir, "checkout", "--detach", "main")
			return "test-feature", withLimits("--continue")
		}},
		{kind: "restore-blocked", setup: func(t *testing.T) (string, []string) {
			// The branch the run would restore HEAD to is checked out in
			// ANOTHER worktree, so the restore cannot execute — rank 3.
			dir, fp := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			saveGuardedCheckoutTransaction(t, dir, fp, nil, nil)
			gitRunCS(t, dir, "checkout", "--detach", "main")
			held := filepath.Join(t.TempDir(), "holds-main")
			gitRunCS(t, dir, "worktree", "add", held, "main")
			return "test-feature", withLimits("--continue")
		}},
		{kind: "repo-unavailable", setup: func(t *testing.T) (string, []string) {
			// The execution context a row needs cannot be established: its
			// worktree path exists but is not a Git working tree at all.
			f := newScopedFixture(t)
			if err := os.RemoveAll(f.wt("child")); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(f.wt("child"), 0o755); err != nil {
				t.Fatal(err)
			}
			return f.feature, withLimits()
		}},
		{kind: "head-ref-missing", setup: func(t *testing.T) (string, []string) {
			// The branch a row would rebase no longer resolves.
			f := newScopedFixture(t)
			detachAndDeleteBranch(t, f, "child")
			return f.feature, withLimits()
		}},
		{kind: "prunable-worktree", setup: func(t *testing.T) (string, []string) {
			// The worktree's administrative record survives its directory.
			f := newScopedFixture(t)
			if err := os.RemoveAll(f.wt("child")); err != nil {
				t.Fatal(err)
			}
			return f.feature, withLimits()
		}},
		{kind: "branch-checked-out-elsewhere", setup: func(t *testing.T) (string, []string) {
			// Only a row that SWITCHES HEAD onto the branch can be blocked by
			// another worktree holding it, which is the checkout route.
			dir, _ := checkoutModeFixture(t)
			writeCheckoutModeMarker(t, dir)
			withUnifiedWorkspaceEnv(t, dir)
			elsewhere := filepath.Join(t.TempDir(), "elsewhere")
			gitRunCS(t, dir, "worktree", "add", elsewhere, "feat-a")
			return "test-feature", withLimits()
		}},
		{kind: "context-dirty", setup: func(t *testing.T) (string, []string) {
			f := newScopedFixture(t)
			dirtyTrackedFile(t, f.wt("child"))
			return f.feature, withLimits()
		}},
		{kind: "base-ref-missing", setup: func(t *testing.T) (string, []string) {
			// The configured base of the root row no longer names a commit:
			// a real branch is recorded as the base and then deleted.
			f := newScopedFixture(t)
			gitInDir(t, f.repo, "branch", "phantom", "master")
			mutateStack(t, f, func(s *internal.Stack) {
				for i := range s.Branches {
					if s.Branches[i].Name == "root" {
						s.Branches[i].Base = "phantom"
					}
				}
			})
			gitInDir(t, f.repo, "branch", "-D", "phantom")
			return f.feature, withLimits()
		}},
		{kind: "cutoff-unresolvable", setup: func(t *testing.T) (string, []string) {
			// A recorded cutoff that no longer resolves.
			f := newScopedFixture(t)
			setStackLastBaseSHA(t, f, "child", "0123456789012345678901234567890123456789")
			return f.feature, withLimits()
		}},
		{kind: "probe-failed", setup: func(t *testing.T) (string, []string) {
			// An unobtainable git --version: the document-level rank 5.9 cause.
			f := newScopedFixture(t)
			installBrokenVersionProbe(t)
			return f.feature, withLimits()
		}},
		{kind: "invalid-git-config", setup: func(t *testing.T) (string, []string) {
			f := newScopedFixture(t)
			gitInDir(t, f.repo, "config", "rebase.updateRefs", "notabool")
			return f.feature, withLimits()
		}},
		{kind: "identity-not-utf8", setup: func(t *testing.T) (string, []string) {
			f := newScopedFixture(t)
			corruptStackIdentityEncoding(t, f)
			return f.feature, withLimits()
		}},
		{kind: "selection-hazard", setup: func(t *testing.T) (string, []string) {
			// Two selected rows naming the same git branch. The new-mode
			// selection ladder pre-refuses this (rank 2 selection-refused),
			// so the cell that reaches rank 6 is the guarded LEGACY route,
			// which arms a limit without any new-mode trigger flag.
			f := newScopedFixture(t)
			duplicateStackBranch(t, f)
			return f.feature, []string{"--max-replay-total", "50"}
		}},
		{kind: "guard-limit-mismatch", setup: func(t *testing.T) (string, []string) {
			// A continuation supplying a limit the persisted state disagrees
			// with: the persisted value stays effective and the conflict is a
			// rank 7 blocker.
			f := newScopedContinuationFixture(t, "--only", "child", "--no-fetch", "--max-replay-total", "7")
			return f.feature, []string{"--no-fetch", "--continue", "--only", "child", "--max-replay-total", "50"}
		}},
		{kind: "approval-without-limits", setup: func(t *testing.T) (string, []string) {
			// A continuation supplying --approve-plan with no effective limit.
			f := newScopedContinuationFixture(t, "--only", "child", "--no-fetch")
			return f.feature, []string{"--no-fetch", "--continue", "--only", "child"}
		}},
		{kind: "indeterminate-entry", setup: func(t *testing.T) (string, []string) {
			// A row whose replay count cannot be determined at all, under a
			// per-entry limit that therefore cannot be judged: rebase.merges
			// recreates merges, so no candidate range describes the replay.
			f := newScopedFixture(t)
			installFailingProbeClass(t, "rev-list")
			return f.feature, []string{"--no-fetch", "--max-replay-per-entry", "5"}
		}},
	}
}

// ---- §22.19 fixture installers -------------------------------------------

// mutateStack rewrites the feature's stack.yaml through the production
// loader/saver, so a hazard is installed exactly as a real edit would be.
func mutateStack(t *testing.T, f *scopedFixture, fn func(s *internal.Stack)) {
	t.Helper()
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	fn(&stack)
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}
}

// detachAndDeleteBranch removes a stack row's branch after detaching the
// worktree that holds it, so the row's head no longer resolves.
func detachAndDeleteBranch(t *testing.T, f *scopedFixture, name string) {
	t.Helper()
	gitInDir(t, f.wt(name), "checkout", "--detach", "HEAD")
	gitInDir(t, f.repo, "branch", "-D", name)
}

// dirtyTrackedFile introduces a real tracked modification.
func dirtyTrackedFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.WriteFile(path, []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("no tracked file to dirty in %s", dir)
}

// setStackLastBaseSHA records a cutoff that no longer resolves.
func setStackLastBaseSHA(t *testing.T, f *scopedFixture, name, sha string) {
	t.Helper()
	mutateStack(t, f, func(s *internal.Stack) {
		for i := range s.Branches {
			if s.Branches[i].Name == name {
				s.Branches[i].LastBaseSHA = sha
			}
		}
	})
}

// duplicateStackBranch makes two selected rows name the same git branch.
func duplicateStackBranch(t *testing.T, f *scopedFixture) {
	t.Helper()
	mutateStack(t, f, func(s *internal.Stack) {
		for i := range s.Branches {
			if s.Branches[i].Name == "child" {
				s.Branches[i].Branch = "parent"
			}
		}
	})
}

// corruptStackIdentityEncoding writes a bound identity that is not valid
// UTF-8, which makes the approval tuple unmintable.
func corruptStackIdentityEncoding(t *testing.T, f *scopedFixture) {
	t.Helper()
	mutateStack(t, f, func(s *internal.Stack) {
		for i := range s.Branches {
			if s.Branches[i].Name == "child" {
				s.Branches[i].Branch = "child" + string([]byte{0xff, 0xfe})
			}
		}
	})
}

// installBrokenVersionProbe puts a git on PATH whose `--version` fails,
// leaving every other subcommand intact: the document-level rank 5.9 cause.
func installBrokenVersionProbe(t *testing.T) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(realGit)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'git version not-a-version'; exit 0; fi\nexec \"$TWS_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_REAL_GIT", abs)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestSyncPlanIntegration_UnavailableDocumentsNameTheirCause is the RUNTIME
// half of §13.7a rule 4's consistency requirement: every rows-less document
// this command can actually produce publishes its own rank 1-5 cause, a
// non-null refusal and runnable:false — and the one document that used to
// publish the forbidden `unavailable` + `blockers: []` + `refusal: null`
// shape, a `--continue` over a VALID guarded backup sentinel, now publishes
// rows instead, because the sentinel IS that continuation's subject.
func TestSyncPlanIntegration_UnavailableDocumentsNameTheirCause(t *testing.T) {
	assertNamedCause := func(t *testing.T, doc map[string]any) {
		t.Helper()
		if got, _ := planField(t, doc, "summary", "plannability").(string); got != "unavailable" {
			t.Fatalf("plannability = %q, want unavailable", got)
		}
		kinds := planBlockerKinds(t, doc)
		if len(kinds) == 0 {
			t.Fatalf("an unavailable document published no cause at all: %v", doc["blockers"])
		}
		if got := planField(t, doc, "refusal", "kind"); got == nil {
			t.Fatalf("refusal.kind is null on an unavailable document whose blockers are %v", kinds)
		}
		if got := planField(t, doc, "runnable"); got != false {
			t.Fatalf("runnable = %v, want false", got)
		}
	}

	t.Run("external_continue_with_no_state_at_all", func(t *testing.T) {
		f := newScopedFixture(t)
		stdout, _, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch", "--continue")
		if exit != 0 {
			t.Fatalf("--plan always exits 0, got %d", exit)
		}
		doc := planDoc(t, stdout)
		assertNamedCause(t, doc)
		found := false
		for _, raw := range planField(t, doc, "blockers").([]any) {
			b := raw.(map[string]any)
			if b["kind"] == "state-refused" && strings.Contains(b["detail"].(string), "nothing to continue") {
				found = true
			}
		}
		if !found {
			t.Fatalf("want the SHIPPED continue refusal as the cause, got %v", doc["blockers"])
		}
	})

	t.Run("checkout_continue_with_no_transaction", func(t *testing.T) {
		dir, _ := checkoutModeFixture(t)
		writeCheckoutModeMarker(t, dir)
		withUnifiedWorkspaceEnv(t, dir)
		stdout, _, exit := runSyncExecute(t, "test-feature", "--plan", "--json", "--continue")
		if exit != 0 {
			t.Fatalf("--plan always exits 0, got %d", exit)
		}
		doc := planDoc(t, stdout)
		assertNamedCause(t, doc)
		found := false
		for _, raw := range planField(t, doc, "blockers").([]any) {
			b := raw.(map[string]any)
			if b["kind"] == "state-refused" && strings.Contains(b["detail"].(string), "no transaction to continue") {
				found = true
			}
		}
		if !found {
			t.Fatalf("want the SHIPPED no-transaction refusal as the cause, got %v", doc["blockers"])
		}
	})

	t.Run("external_stack_load_failure", func(t *testing.T) {
		f := newScopedFixture(t)
		if err := os.WriteFile(internal.StackPath(f.featurePath), []byte("\tnot: [yaml"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, _, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("--plan always exits 0, got %d", exit)
		}
		assertNamedCause(t, planDoc(t, stdout))
	})
}

// TestSyncPlanIntegration_ContinuationTotalityOverEveryLegacyArtefactShape is
// §13.7a rule 4's TOTALITY table: every on-disk shape a `.sync-state.yaml`
// can take under a `--continue`, driven through production Execute, with the
// one invariant that must hold for all of them —
//
//	plannability == "unavailable"  ⇒  at least one effective rank 1-5
//	                                  blocker, refusal.kind != null and
//	                                  runnable == false,
//
// so `unavailable` + `blockers: []` + `refusal: null` + `runnable: true` is
// unreachable by construction rather than by inspection of the cells someone
// happened to think of. The rows-availability rule and the gate that names
// the cause are asserted TOGETHER, because the defect this table exists to
// prevent is exactly the two disagreeing: an artefact the classifier decoded
// but this invocation's own inspector could not establish as a subject.
//
// The two shapes that used to slip through are driven explicitly: a legacy
// state past the 1 MiB artefact read cap, and a symlinked one.
func TestSyncPlanIntegration_ContinuationTotalityOverEveryLegacyArtefactShape(t *testing.T) {
	shapes := []struct {
		name string
		// install prepares the feature directory; wantRows says whether this
		// shape is a resumable subject at all.
		install  func(t *testing.T, f *scopedFixture)
		wantRows bool
	}{
		{name: "absent", install: func(t *testing.T, f *scopedFixture) {}},
		{
			name:     "real_legacy_state",
			install:  func(t *testing.T, f *scopedFixture) { legacyRealStateFixture(t, f) },
			wantRows: true,
		},
		{
			name:     "valid_guarded_backup_sentinel",
			install:  func(t *testing.T, f *scopedFixture) { buildResumableCell4Fixture(t, f) },
			wantRows: true,
		},
		{
			name: "oversized_past_the_read_cap",
			install: func(t *testing.T, f *scopedFixture) {
				legacyRealStateFixture(t, f)
				body, err := os.ReadFile(internal.SyncStatePath(f.featurePath))
				if err != nil {
					t.Fatal(err)
				}
				// A real, decodable document whose trailing comment pushes it
				// past the 1 MiB cap: the classifier can still read it, this
				// invocation's own bounded inspector cannot.
				padded := append(body, []byte("\n# "+strings.Repeat("p", 1<<20)+"\n")...)
				if err := os.WriteFile(internal.SyncStatePath(f.featurePath), padded, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlinked_state_document",
			install: func(t *testing.T, f *scopedFixture) {
				// A REAL, decodable state document that lives outside the
				// feature directory, reached only through a symlink.
				elsewhere := t.TempDir()
				state := internal.NewSyncState()
				state.FailedBranch = "parent"
				if err := internal.SaveSyncState(elsewhere, state); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(internal.SyncStatePath(elsewhere), internal.SyncStatePath(f.featurePath)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "empty_document",
			install: func(t *testing.T, f *scopedFixture) {
				if err := os.WriteFile(internal.SyncStatePath(f.featurePath), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "undecodable_document",
			install: func(t *testing.T, f *scopedFixture) {
				if err := os.WriteFile(internal.SyncStatePath(f.featurePath), []byte("\tnot: [yaml"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory_instead_of_a_file",
			install: func(t *testing.T, f *scopedFixture) {
				if err := os.MkdirAll(internal.SyncStatePath(f.featurePath), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			f := newScopedFixture(t)
			shape.install(t, f)

			stdout, _, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--continue")
			if exit != 0 {
				t.Fatalf("--plan always exits 0: exit=%d", exit)
			}
			doc := planDoc(t, stdout)
			plannability, _ := planField(t, doc, "summary", "plannability").(string)

			if shape.wantRows {
				if plannability != "rows" {
					t.Fatalf("plannability = %q, want rows: this shape IS a resumable subject", plannability)
				}
				return
			}
			if plannability != "unavailable" {
				t.Fatalf("plannability = %q, want unavailable: this shape is no subject at all", plannability)
			}

			// The invariant, asserted in full.
			kinds := planBlockerKinds(t, doc)
			if len(kinds) == 0 {
				t.Fatalf("unavailable with blockers[] EMPTY: the document names no cause at all")
			}
			effective := false
			for _, k := range kinds {
				if effectiveRankOneToFive(internal.RefusalKind(k)) {
					effective = true
				}
			}
			if !effective {
				t.Fatalf("blockers %v carry no effective rank 1-5 cause", kinds)
			}
			refusal, _ := planField(t, doc, "refusal", "kind").(string)
			if refusal == "" {
				t.Fatalf("refusal.kind is null while blockers are %v", kinds)
			}
			if got := planField(t, doc, "runnable"); got != false {
				t.Fatalf("runnable = %v, want false", got)
			}
			// The cause really is this artefact's own measured presence, not
			// a generic sentence: state.files and blockers[] tell one story.
			if !strings.Contains(strings.Join(planBlockerDetails(t, doc), "\n"), internal.SyncStatePath(f.featurePath)) &&
				!strings.Contains(strings.Join(planBlockerDetails(t, doc), "\n"), "nothing to continue") {
				t.Fatalf("blocker details %v name neither the artefact nor the shipped no-state sentence", planBlockerDetails(t, doc))
			}

			// And the guarded twin refuses rather than running on no subject.
			_, stderr, exit2 := runSyncExecute(t, f.feature, "--continue", "--max-replay-total", "50")
			if exit2 == 0 {
				t.Fatalf("a continuation with no resumable subject must refuse; stderr=%q", stderr)
			}
		})
	}
}

// effectiveRankOneToFive reports whether a published kind is one of §7.1's
// effective rank 1-5 causes — every kind at or before rank 5.9
// `probe-failed` in the closed RefusalKinds order, which is exactly the set
// that forces runnable:false.
func effectiveRankOneToFive(kind internal.RefusalKind) bool {
	rank, cutoff := -1, -1
	for i, k := range internal.RefusalKinds {
		if k == kind {
			rank = i
		}
		if k == internal.RefusalProbeFailed {
			cutoff = i
		}
	}
	return rank >= 0 && cutoff >= 0 && rank <= cutoff
}

// planBlockerDetails lists blockers[].detail in document order.
func planBlockerDetails(t *testing.T, doc map[string]any) []string {
	t.Helper()
	var out []string
	for _, raw := range planField(t, doc, "blockers").([]any) {
		if d, ok := raw.(map[string]any)["detail"].(string); ok {
			out = append(out, d)
		}
	}
	return out
}

// ===========================================================================
// §11.8 / §25.100: the JIT seam's collateral TOTALITY comparison.
// ===========================================================================

// collateralSeamFixture builds a real repository whose single row publishes a
// real collateral tuple array (mechanism `argv`, one stack-unowned side ref
// inside the candidate range), and returns the plan request and the freshly
// measured row the JIT seam re-measures against.
func collateralSeamFixture(t *testing.T) (internal.RebasePlanRequest, internal.PlanEntry) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	preCutoff := gitOutput(t, repo, "rev-parse", "HEAD")
	writeAndCommit(t, repo, "m1.txt", "m1\n", "m1")
	cutoff := gitOutput(t, repo, "rev-parse", "HEAD")

	if err := createWorktree("feature", "row", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	wt := internal.WorktreePath("feature", "row")
	gitRun(t, wt, "checkout", "-q", "-b", "side", preCutoff)
	writeAndCommit(t, wt, "side.txt", "side\n", "side work")
	gitRun(t, wt, "checkout", "-q", "row")
	writeAndCommit(t, wt, "row.txt", "row\n", "row work")
	rowWork := gitOutput(t, wt, "rev-parse", "HEAD")
	gitRun(t, wt, "merge", "--no-ff", "-m", "merge side", "side")

	// A SECOND ref inside the same candidate range, this one owned by the
	// stack: `owned` sits on the row's own work commit — descended from the
	// recorded cutoff and reachable from the row's tip — and it is a stack
	// entry, so its tuple publishes stack_owned: true. It is deliberately
	// given no worktree, because a ref any worktree holds is excluded from
	// collateral_refs entirely (§11.8's "minus worktree-held refs").
	gitRun(t, wt, "branch", "owned", rowWork)
	writeAndCommit(t, repo, "m2.txt", "m2\n", "m2")

	featurePath := internal.FeaturePath("feature")
	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "row", Base: "master", Repo: repo, LastBaseSHA: cutoff},
		// Archived: metadata-only, so the document still publishes exactly
		// one entries[] row while the branch is unambiguously stack-owned
		// (the ownership map is built from every stack branch, §11.8).
		{Name: "owned", Base: "row", Repo: repo, Archived: true},
	}}
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}

	args := externalPlanArgs{
		Feature: "feature",
		Layout:  newExternalSyncLayout(featurePath),
		Ws:      internal.Workspace{Mode: internal.ModeExternal, RepoRoot: repo},
		Policy: internal.SyncRunPolicy{
			Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull,
			ScopeKind: internal.SyncScopeAll,
		},
		Changed: map[string]bool{"no-fetch": true},
	}
	plan, planReq, _, err := buildExternalPlan(args, io.Discard)
	if err != nil {
		t.Fatalf("buildExternalPlan: %v", err)
	}
	// Two rows: the materialized `row` this seam is about, and the
	// metadata-only `owned` entry that makes its sibling ref stack-owned.
	if len(plan.Entries) != 2 {
		t.Fatalf("want exactly two rows (row + the metadata-only owner), got %d", len(plan.Entries))
	}
	var fresh internal.PlanEntry
	for _, e := range plan.Entries {
		if e.Name == "row" {
			fresh = e
		}
	}
	if fresh.Name != "row" {
		t.Fatalf("the fixture published no `row` entry: %v", plan.Entries)
	}
	if fresh.CollateralMechanism == nil || *fresh.CollateralMechanism != "argv" {
		t.Fatalf("collateral_mechanism = %v, want argv: the fixture must publish a collateral-class row", fresh.CollateralMechanism)
	}

	// The tuple array must carry BOTH ownership values, or the two
	// stack_owned drift cells below would mutate the same row in opposite
	// directions and could silently collapse into one another.
	owned, unowned := collateralRefsByOwnership(fresh)
	if len(owned) != 1 || len(unowned) != 1 {
		t.Fatalf("collateral_refs = %v, want exactly one stack-owned ref and one unowned ref", fresh.CollateralRefs)
	}
	if owned[0].Ref != "refs/heads/owned" {
		t.Fatalf("the stack-owned collateral ref is %q, want refs/heads/owned", owned[0].Ref)
	}
	if unowned[0].Ref != "refs/heads/side" {
		t.Fatalf("the unowned collateral ref is %q, want refs/heads/side", unowned[0].Ref)
	}
	return planReq, fresh
}

// collateralRefsByOwnership splits a row's tuple array on stack_owned, so a
// drift cell can name the exact row it mutates instead of an index that
// could quietly become the wrong one.
func collateralRefsByOwnership(e internal.PlanEntry) (owned, unowned []internal.PlanCollateralRef) {
	for _, ref := range e.CollateralRefs {
		if ref.StackOwned {
			owned = append(owned, ref)
		} else {
			unowned = append(unowned, ref)
		}
	}
	return owned, unowned
}

// setCollateralOwnership flips stack_owned on the one tuple whose ref name
// matches, and fails loudly when the fixture no longer carries it.
func setCollateralOwnership(t *testing.T, e *internal.PlanEntry, ref string, owned bool) {
	t.Helper()
	for i := range e.CollateralRefs {
		if e.CollateralRefs[i].Ref == ref {
			if e.CollateralRefs[i].StackOwned == owned {
				t.Fatalf("ref %s already publishes stack_owned=%v; this cell would mutate nothing", ref, owned)
			}
			e.CollateralRefs[i].StackOwned = owned
			return
		}
	}
	t.Fatalf("ref %s is not in the tuple array %v", ref, e.CollateralRefs)
}

// TestSyncPlanIntegration_JITCollateralDriftIsTotal is §25.100's own
// requirement, made executable: the JIT seam compares the WHOLE
// `{repo, ref, sha, stack_owned}` tuple array plus the mechanism, so every
// shape of drift refuses rank 9 `revalidation-mismatch` BEFORE any
// `git rebase`, and only an unchanged row proceeds.
//
// The approved side is a doctored copy of the FRESH measurement, so each
// cell differs from current Git state in exactly one way and in no other:
// added tuple, removed tuple, tip-only SHA drift in both directions,
// `stack_owned` flipped in both directions, and `null` ↔ known on both the
// refs array and the mechanism. Because RevalidatePlanEntry NEUTRALIZES the
// collateral members before its digest comparison, the only production code
// that can catch any of these is collateralDrifted — which is what makes the
// table mutation-sensitive rather than incidentally green.
func TestSyncPlanIntegration_JITCollateralDriftIsTotal(t *testing.T) {
	planReq, fresh := collateralSeamFixture(t)

	clone := func(e internal.PlanEntry) internal.PlanEntry {
		out := e
		out.CollateralRefs = append([]internal.PlanCollateralRef(nil), e.CollateralRefs...)
		if e.CollateralMechanism != nil {
			m := *e.CollateralMechanism
			out.CollateralMechanism = &m
		}
		return out
	}
	seam := func(t *testing.T, approved internal.PlanEntry) (internal.PlanGuardRevalidation, error) {
		t.Helper()
		w := newSyncGitWrapper(t, false)
		var res internal.PlanGuardRevalidation
		var err error
		w.around(t, func() {
			res, err = internal.RevalidatePlanGuardEntry(internal.RevalidatePlanGuardEntryRequest{
				Request: planReq, Approved: approved,
				Limits: internal.PlanGuardLimits{}, StatePreserved: false,
			})
		})
		for _, r := range w.records(t) {
			if tail := r.Tail(); len(tail) > 0 && tail[0] == "rebase" {
				t.Fatalf("the seam ran `git rebase %v`; every verdict is reached BEFORE any rebase", tail)
			}
		}
		return res, err
	}

	t.Run("unchanged_control_proceeds", func(t *testing.T) {
		res, err := seam(t, clone(fresh))
		if err != nil {
			t.Fatalf("an unchanged row must proceed, got %v", err)
		}
		if !res.CandidateCountKnown {
			t.Fatal("the seam must report the freshly resolved candidate count")
		}
	})

	drifts := []struct {
		name   string
		doctor func(e *internal.PlanEntry)
	}{
		{"added_tuple", func(e *internal.PlanEntry) {
			e.CollateralRefs = append(e.CollateralRefs, internal.PlanCollateralRef{
				Repo: e.CollateralRefs[0].Repo, Ref: "refs/heads/ghost",
				SHA: strings.Repeat("a", 40), StackOwned: false,
			})
		}},
		{"removed_tuple", func(e *internal.PlanEntry) {
			e.CollateralRefs = e.CollateralRefs[:len(e.CollateralRefs)-1]
		}},
		{"tip_sha_drift_downwards", func(e *internal.PlanEntry) {
			e.CollateralRefs[0].SHA = strings.Repeat("0", 40)
		}},
		{"tip_sha_drift_upwards", func(e *internal.PlanEntry) {
			e.CollateralRefs[0].SHA = strings.Repeat("f", 40)
		}},
		{"stack_owned_true_to_false", func(e *internal.PlanEntry) {
			// The STACK-OWNED ref is approved as unowned: the run was
			// admitted believing it would carry a ref the stack does not own.
			setCollateralOwnership(t, e, "refs/heads/owned", false)
		}},
		{"stack_owned_false_to_true", func(e *internal.PlanEntry) {
			// The mirror image, on the OTHER ref: an unowned ref approved as
			// stack-owned. The two cells therefore mutate different tuples in
			// opposite directions and cannot collapse into one another.
			setCollateralOwnership(t, e, "refs/heads/side", true)
		}},
		{"null_refs_against_known", func(e *internal.PlanEntry) {
			e.CollateralRefs = nil
		}},
		{"known_mechanism_against_null", func(e *internal.PlanEntry) {
			e.CollateralMechanism = nil
		}},
		{"mechanism_none_against_argv", func(e *internal.PlanEntry) {
			none := "none"
			e.CollateralMechanism = &none
		}},
	}

	doctored := map[string]string{}
	for _, d := range drifts {
		t.Run(d.name, func(t *testing.T) {
			approved := clone(fresh)
			d.doctor(&approved)
			doctored[d.name] = collateralTupleString(approved)
			if collateralTupleString(approved) == collateralTupleString(fresh) {
				t.Fatalf("the %q cell did not actually change the tuple array; it would prove nothing", d.name)
			}
			_, err := seam(t, approved)
			var refusal *internal.PlanGuardRefusalError
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v (%T), want a *PlanGuardRefusalError", err, err)
			}
			if refusal.Kind != string(internal.RefusalRevalidationMismatch) {
				t.Fatalf("refusal kind = %q, want rank 9 %q", refusal.Kind, internal.RefusalRevalidationMismatch)
			}
			if !strings.Contains(refusal.Detail, "collateral") {
				t.Fatalf("detail = %q, want it to name the collateral drift", refusal.Detail)
			}
		})
	}

	// The two ownership cells are distinct SHAPES, not one cell written
	// twice: they differ from the fresh measurement, from each other, and in
	// opposite directions — the true-to-false cell publishes one fewer
	// stack-owned tuple than the fresh row, the false-to-true cell one more.
	t.Run("the_two_ownership_cells_cannot_collapse", func(t *testing.T) {
		freshShape := collateralTupleString(fresh)
		trueToFalse, falseToTrue := doctored["stack_owned_true_to_false"], doctored["stack_owned_false_to_true"]
		if trueToFalse == "" || falseToTrue == "" {
			t.Fatal("both ownership cells must have run before this assertion")
		}
		if trueToFalse == falseToTrue {
			t.Fatalf("both ownership cells produced the SAME approved tuple array %q; they would prove one drift, not two", trueToFalse)
		}
		if trueToFalse == freshShape || falseToTrue == freshShape {
			t.Fatalf("an ownership cell equals the fresh shape %q and drifts nothing", freshShape)
		}

		countOwned := func(e internal.PlanEntry) int {
			owned, _ := collateralRefsByOwnership(e)
			return len(owned)
		}
		freshOwned := countOwned(fresh)
		lowered := clone(fresh)
		setCollateralOwnership(t, &lowered, "refs/heads/owned", false)
		raised := clone(fresh)
		setCollateralOwnership(t, &raised, "refs/heads/side", true)
		if got := countOwned(lowered); got != freshOwned-1 {
			t.Fatalf("the true-to-false cell publishes %d stack-owned tuples, want %d", got, freshOwned-1)
		}
		if got := countOwned(raised); got != freshOwned+1 {
			t.Fatalf("the false-to-true cell publishes %d stack-owned tuples, want %d", got, freshOwned+1)
		}
		// And each cell mutates the ref the OTHER one leaves alone.
		if collateralOwnershipOf(t, lowered, "refs/heads/side") != collateralOwnershipOf(t, fresh, "refs/heads/side") {
			t.Fatal("the true-to-false cell also touched the unowned ref")
		}
		if collateralOwnershipOf(t, raised, "refs/heads/owned") != collateralOwnershipOf(t, fresh, "refs/heads/owned") {
			t.Fatal("the false-to-true cell also touched the stack-owned ref")
		}
	})
}

// collateralOwnershipOf reports one tuple's stack_owned value by ref name.
func collateralOwnershipOf(t *testing.T, e internal.PlanEntry, ref string) bool {
	t.Helper()
	for _, r := range e.CollateralRefs {
		if r.Ref == ref {
			return r.StackOwned
		}
	}
	t.Fatalf("ref %s is not in the tuple array %v", ref, e.CollateralRefs)
	return false
}

// collateralTupleString renders the whole compared value — mechanism plus the
// tuple array — so a doctored cell can prove it really differs from the
// fresh measurement before the seam is asked about it.
func collateralTupleString(e internal.PlanEntry) string {
	mech := "<nil>"
	if e.CollateralMechanism != nil {
		mech = *e.CollateralMechanism
	}
	if e.CollateralRefs == nil {
		return mech + "|<nil>"
	}
	parts := make([]string, 0, len(e.CollateralRefs))
	for _, r := range e.CollateralRefs {
		parts = append(parts, fmt.Sprintf("%s/%s/%s/%v", r.Repo, r.Ref, r.SHA, r.StackOwned))
	}
	return mech + "|" + strings.Join(parts, ",")
}

// installFailingProbeClassAtDir puts a git on PATH that fails EVERY
// invocation of one class whose `-C` operand is exactly dir, and executes
// every other process — including the same class at any other directory —
// for real. It is the seam a per-CONTEXT probe failure needs: a push
// context's ordered inventory can be made unreadable while every execution
// context's own inventory still succeeds.
func installFailingProbeClassAtDir(t *testing.T, class, dir string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(realGit)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	stub := t.TempDir()
	script := `#!/bin/sh
c=""
target=""
skip=0
next_is_dir=0
for a in "$@"; do
	if [ "$next_is_dir" = "1" ]; then target="$a"; next_is_dir=0; skip=0; continue; fi
	if [ "$skip" = "1" ]; then skip=0; continue; fi
	case "$a" in
	-C) next_is_dir=1; continue ;;
	-c) skip=1; continue ;;
	-*) continue ;;
	*) if [ -z "$c" ]; then c="$a"; fi ;;
	esac
done
if [ "$c" = "$TWS_FAIL_CLASS" ] && { [ "$target" = "$TWS_FAIL_DIR" ] || [ "$target" = "$TWS_FAIL_DIR_ALT" ]; }; then
	echo "fatal: injected $c failure at $target" >&2
	exit 128
fi
exec "$TWS_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(stub, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_REAL_GIT", abs)
	t.Setenv("TWS_FAIL_CLASS", class)
	t.Setenv("TWS_FAIL_DIR", dir)
	t.Setenv("TWS_FAIL_DIR_ALT", canonical)
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestSyncPlanIntegration_PushMappingReadFailureIsRank59 is §14.1a rule 5a's
// own cell, driven over a REAL repository whose push context's ordered
// config inventory cannot be read while every execution context's own
// inventory still can:
//
//	MappingReadOK: false  ⇒  the target row is STILL published (a fact this
//	                         invocation could not measure is disclosed, never
//	                         dropped), its lease is §14.2's cell 5 — expectation
//	                         `unknown`, ref null, sha null, freshness
//	                         `possibly-stale` — and the document carries
//	                         EXACTLY ONE entry:null rank 5.9 `probe-failed`
//	                         blocker, runnable false, with `probe-failed` in
//	                         push.blocked_by. The guarded twin refuses.
func TestSyncPlanIntegration_PushMappingReadFailureIsRank59(t *testing.T) {
	f := newScopedFixture(t)
	// The push context is the repository root itself; the three execution
	// contexts are the worktrees, so failing the inventory at the repo root
	// alone isolates the mapping read.
	installFailingProbeClassAtDir(t, "config", f.repo)

	stdout, stderr, exit := runSyncExecute(t, f.feature, "--plan", "--json", "--no-fetch", "--push")
	if exit != 0 {
		t.Fatalf("--plan always exits 0: exit=%d stderr=%q", exit, stderr)
	}
	doc := planDoc(t, stdout)

	// (a) the target row survives the unreadable mapping.
	push := planField(t, doc, "push").(map[string]any)
	targets, _ := push["targets"].([]any)
	if len(targets) == 0 {
		t.Fatalf("push.targets = %v, want the measured rows: an unreadable mapping is disclosed, never dropped", push["targets"])
	}

	// (b) every lease is §14.2's unknown cell, in full.
	for _, raw := range targets {
		target := raw.(map[string]any)
		lease, ok := target["lease"].(map[string]any)
		if !ok {
			t.Fatalf("target %v carries no lease object", target)
		}
		if lease["expectation"] != "unknown" {
			t.Fatalf("lease.expectation = %v, want unknown when MappingReadOK is false", lease["expectation"])
		}
		if lease["expected_ref"] != nil {
			t.Fatalf("lease.expected_ref = %v, want null: nothing was read to map through", lease["expected_ref"])
		}
		if lease["expected_sha"] != nil {
			t.Fatalf("lease.expected_sha = %v, want null", lease["expected_sha"])
		}
		if lease["expected_sha_freshness"] != "possibly-stale" {
			t.Fatalf("lease.expected_sha_freshness = %v, want possibly-stale", lease["expected_sha_freshness"])
		}
	}

	// (c) exactly one entry:null rank 5.9 probe-failed blocker names it.
	probes := documentLevelBlockersOfKind(t, doc, "probe-failed")
	if len(probes) != 1 {
		t.Fatalf("want EXACTLY one document-level probe-failed blocker, got %d: %v", len(probes), probes)
	}
	if detail, _ := probes[0]["detail"].(string); !strings.Contains(detail, "remote mapping") {
		t.Fatalf("blocker detail = %q, want it to name the unread remote mapping", detail)
	}
	for _, raw := range planField(t, doc, "blockers").([]any) {
		b := raw.(map[string]any)
		if b["kind"] == "probe-failed" && b["entry"] != nil {
			t.Fatalf("§14.1a rule 5a's blocker is document-level; got entry=%v", b["entry"])
		}
	}

	// (d) the document is not runnable and the push says why.
	if got := planField(t, doc, "runnable"); got != false {
		t.Fatalf("runnable = %v, want false under a rank 5.9 cause", got)
	}
	blockedBy, _ := push["blocked_by"].([]any)
	found := false
	for _, raw := range blockedBy {
		if raw == "probe-failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("push.blocked_by = %v, want it to carry probe-failed", blockedBy)
	}
	if push["executable"] != false {
		t.Fatalf("push.executable = %v, want false", push["executable"])
	}

	// (e) the guarded twin refuses rather than pushing against an unknown lease.
	_, stderr2, exit2 := runSyncExecute(t, f.feature, "--no-fetch", "--push", "--max-replay-total", "50")
	if exit2 == 0 {
		t.Fatalf("the guarded twin must refuse: stderr=%q", stderr2)
	}
	if !strings.Contains(stderr2, "probe-failed") {
		t.Fatalf("stderr = %q, want the rank 5.9 probe-failed refusal", stderr2)
	}
}
