package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// Acceptance criteria carried by this file's cells: AC-001 (the customer
// topology's end-to-end result), AC-032 and AC-033 (plan-then-execute parity
// and the success report), AC-056 (checkout mode reuses its one checkout),
// AC-069 (the issue #4 closure).

// ---------------------------------------------------------------------------
// T-001 / T-002 / T-003 — the end-to-end cells.
//
// Every assertion below runs the PRODUCTION command tree over a real Git
// repository: plan, approve, execute, then read the resulting refs and
// stack.yaml back off disk. These are the cells that prove the whole pipeline
// composes, so they deliberately assert observable outcomes — where each
// branch ended up and what stack.yaml records — rather than internal shapes
// the focused package-internal tests already own.
// ---------------------------------------------------------------------------

// reparentExecute runs the documented three-step workflow: plan, extract the
// fingerprint exactly as the docs prescribe, then execute with the same limit
// the plan carried.
func reparentExecute(t *testing.T, f *reparentFixture, target, onto string, extra ...string) (stdout, stderr string, exit int) {
	t.Helper()
	planArgs := append([]string{f.Feature, target, "--onto", onto, "--max-replay-total", "50"}, extra...)
	human, planErr := f.Plan(planArgs...)
	fingerprint := reparentFingerprint(t, human+"\n"+planErr)

	execArgs := append([]string{
		f.Feature, target, "--onto", onto,
		"--max-replay-total", "50",
		"--approve-plan", fingerprint,
	}, extra...)
	return f.Run(execArgs...)
}

// TestReparentE2E_CustomerTopologyExternal is T-001. The customer shape is the
// one that catches the regression this feature exists to prevent: `pr2`'s
// recorded cutoff is the ORIGINAL B, which master rewrote into B', so replay
// must carry exactly C — never {B, C} — onto the destination.
func TestReparentE2E_CustomerTopologyExternal(t *testing.T) {
	_ = "asserts AC-001 AC-032 AC-033"
	assertReparentMatrixBehavior(t, "T-001", "customer-external-plan-execute")
	f := newReparentCustomerExternal(t)

	human, prose := f.Plan(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--max-replay-total", "50")
	if !strings.Contains(human, "target") {
		t.Fatalf("the human plan must carry a target section:\n%s", human)
	}
	// §3.6: stdout carries the document and nothing else.
	if strings.Contains(human, "Fetching") {
		t.Fatalf("fetch prose belongs on stderr, not in the document:\n%s", human)
	}
	// §5.1: the recorded cutoff, not the destination, is the replay upstream.
	if !strings.Contains(human, f.B[:7]) {
		t.Fatalf("the plan must name the recorded cutoff %s:\n%s\n%s", f.B[:7], human, prose)
	}

	stdout, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch")
	if exit != 0 {
		t.Fatalf("execution exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	for _, want := range []string{
		"materialized argv:",
		"atomicity: the ref CAS was race-atomic",
		"tws changed no remote ref and no pull request.",
		"retarget the pull request first",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("fresh success must publish %q:\n%s", want, stderr)
		}
	}

	// The target now sits on the destination, and only C was replayed.
	pr2 := f.SHA("feat-pr2")
	master := f.SHA("master")
	if !reparentIsAncestorOf(t, f, master, pr2) {
		t.Fatalf("feat-pr2 (%s) is not a descendant of master (%s)", pr2, master)
	}
	replayed := strings.Fields(f.reparentGit(f.Repo, "rev-list", master+".."+pr2))
	if len(replayed) != 1 {
		t.Fatalf("exactly one commit (C) must be replayed, got %d: %v", len(replayed), replayed)
	}
	subject := f.reparentGit(f.Repo, "log", "-1", "--format=%s", replayed[0])
	if subject != "C" {
		t.Fatalf("the replayed commit must be C, got %q", subject)
	}

	// §10.1: stack.yaml records the new base as a FULL ref and the new cutoff.
	entry := f.Entry("pr2")
	if !strings.HasPrefix(entry.Base, "refs/") {
		t.Fatalf("the new base must be stored as a full ref, got %q", entry.Base)
	}
	if entry.LastBaseSHA != master {
		t.Fatalf("the new cutoff must be the destination tip %s, got %s", master, entry.LastBaseSHA)
	}

	// §11.6a: the artifact and its compatibility envelope are gone.
	assertReparentResidueGone(t, f)
}

// TestReparentE2E_CustomerTopologyCheckout is T-002, the checkout twin.
func TestReparentE2E_CustomerTopologyCheckout(t *testing.T) {
	_ = "asserts AC-032 AC-033 AC-056"
	assertReparentMatrixBehavior(t, "T-002", "customer-checkout-plan-execute")
	f := newReparentCustomerCheckout(t)
	assertCheckoutPlanSelectedAndCrossRepoPure(t, f)

	before := f.reparentGit(f.Repo, "rev-parse", "--abbrev-ref", "HEAD")

	stdout, stderr, exit := reparentExecute(t, f, "pr2", "main")
	if exit != 0 {
		t.Fatalf("execution exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}

	pr2 := f.SHA("pr2")
	main := f.SHA("main")
	if !reparentIsAncestorOf(t, f, main, pr2) {
		t.Fatalf("pr2 (%s) is not a descendant of main (%s)", pr2, main)
	}
	replayed := strings.Fields(f.reparentGit(f.Repo, "rev-list", main+".."+pr2))
	if len(replayed) != 1 {
		t.Fatalf("exactly one commit (C) must be replayed, got %d: %v", len(replayed), replayed)
	}

	// §9.3: the single physical checkout is returned to where it started.
	after := f.reparentGit(f.Repo, "rev-parse", "--abbrev-ref", "HEAD")
	if after != before {
		t.Fatalf("the checkout must be restored to %q, got %q", before, after)
	}

	entry := f.Entry("pr2")
	if !strings.HasPrefix(entry.Base, "refs/") {
		t.Fatalf("the new base must be stored as a full ref, got %q", entry.Base)
	}
	assertReparentResidueGone(t, f)
}

// TestReparentE2E_Issue4BranchingClosure is T-003. Reparenting `pr1` moves a
// closure that branches twice and is three deep on one arm; every descendant
// must end up on its own parent's NEW tip, in one run, with every row's
// metadata rewritten together.
func TestReparentE2E_Issue4BranchingClosure(t *testing.T) {
	_ = "asserts AC-033 AC-069"
	assertReparentMatrixBehavior(t, "T-003", "issue4-branching-closure-both-modes")
	for _, mode := range []string{"external", "checkout"} {
		t.Run(mode, func(t *testing.T) {
			var f *reparentFixture
			var dest string
			if mode == "external" {
				f = newReparentIssue4External(t)
				dest = "master"
			} else {
				f = newReparentIssue4Checkout(t)
				dest = "main"
			}

			extra := []string{}
			if mode == "external" {
				extra = append(extra, "--no-fetch")
			}
			stdout, stderr, exit := reparentExecute(t, f, "pr1", dest, extra...)
			if exit != 0 {
				t.Fatalf("execution exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
			}

			destSHA := f.SHA(dest)
			stack := f.Stack()
			branchOf := map[string]string{}
			for _, e := range stack.Branches {
				branchOf[e.Name] = e.GitBranch()
			}

			// Every row of the closure is a descendant of the destination.
			for _, name := range []string{"pr1", "pr2", "pr3", "pr4", "pr5", "pr6"} {
				tip := f.SHA(branchOf[name])
				if !reparentIsAncestorOf(t, f, destSHA, tip) {
					t.Fatalf("%s (%s) is not a descendant of %s (%s)", name, tip, dest, destSHA)
				}
			}
			// The fan-out is preserved: each descendant sits directly on its
			// own parent's new tip.
			for _, edge := range [][2]string{
				{"pr2", "pr1"}, {"pr3", "pr1"}, {"pr4", "pr1"},
				{"pr5", "pr3"}, {"pr6", "pr5"},
			} {
				child, parent := f.SHA(branchOf[edge[0]]), f.SHA(branchOf[edge[1]])
				if !reparentIsAncestorOf(t, f, parent, child) {
					t.Fatalf("%s is no longer a descendant of %s", edge[0], edge[1])
				}
			}
			// One commit per entry survives: nothing was duplicated or lost.
			for _, name := range []string{"pr2", "pr3", "pr4", "pr5", "pr6"} {
				parent := map[string]string{"pr2": "pr1", "pr3": "pr1", "pr4": "pr1", "pr5": "pr3", "pr6": "pr5"}[name]
				rng := f.SHA(branchOf[parent]) + ".." + f.SHA(branchOf[name])
				if n := len(strings.Fields(f.reparentGit(f.Repo, "rev-list", rng))); n != 1 {
					t.Fatalf("%s must carry exactly one commit above %s, got %d", name, parent, n)
				}
			}

			// §10.1: every moved row's cutoff is its parent's NEW tip, written
			// in one atomic stack.yaml write.
			for _, e := range stack.Branches {
				if e.LastBaseSHA == "" {
					t.Fatalf("entry %s lost its recorded cutoff", e.Name)
				}
			}
			assertReparentResidueGone(t, f)
		})
	}
}

// TestReparentE2E_DecoupledNamesAndSlashes proves issue #1's fact holds
// end-to-end: every logical name differs from its Git branch and contains a
// `/`, so any ref or path component derived from a raw Name would break.
func TestReparentE2E_DecoupledNamesAndSlashes(t *testing.T) {
	f := newReparentDecoupledExternal(t)
	f.reparentGit(f.Repo, "update-ref", "refs/remotes/origin/user/jd/api", f.SHA("user/jd/api"))
	f.reparentGit(f.Repo, "update-ref", "refs/remotes/origin/user/jd/web", f.SHA("user/jd/web"))
	human, prose := f.Plan(f.Feature, "team/web", "--onto", "master", "--no-fetch", "--max-replay-total", "50")
	if !strings.Contains(human, "PR base user/jd/api -> master") {
		t.Fatalf("plan used logical entry name instead of Git branch for PR base:\n%s", human)
	}
	token := reparentFingerprint(t, human+"\n"+prose)
	internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
		if step == "after-write" {
			return &reparentInjectedFailure{}
		}
		return nil
	}
	t.Cleanup(func() { internal.ReparentStepHook = nil })
	runStdout, runStderr, exit := f.Run(
		f.Feature, "team/web", "--onto", "master", "--no-fetch",
		"--max-replay-total", "50", "--approve-plan", token,
	)
	if exit != 1 {
		t.Fatalf("post-write crash exit = %d, want 1", exit)
	}
	internal.ReparentStepHook = nil
	rec, err := internal.LoadReparentRemoteRecord(f.Loc())
	if err != nil || rec == nil {
		t.Fatalf("load decoupled remote record: %v\nstdout:\n%s\nstderr:\n%s", err, runStdout, runStderr)
	}
	entryRecord, ok := rec.Entry("team/web")
	if !ok || entryRecord.PRBaseBefore != "user/jd/api" || entryRecord.PRBaseAfter != "master" {
		t.Fatalf("decoupled PR bases = %+v", entryRecord)
	}
	stdout, stderr, exit := f.Run(f.Feature, "--continue")
	if exit != 0 {
		t.Fatalf("continue exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stderr, "team/web: PR base user/jd/api -> master") {
		t.Fatalf("resumed success lost decoupled PR-base guidance:\n%s", stderr)
	}
	master := f.SHA("master")
	web := f.SHA("user/jd/web")
	if !reparentIsAncestorOf(t, f, master, web) {
		t.Fatalf("user/jd/web (%s) is not a descendant of master (%s)", web, master)
	}
	// The sibling that was NOT in the closure must not have moved.
	entry := f.Entry("team/api")
	if entry.Base != "master" {
		t.Fatalf("an entry outside the closure must keep its configured base, got %q", entry.Base)
	}
	assertReparentResidueGone(t, f)
}

// TestReparentE2E_PlanJSONIsTheOnlyStdout is AC-012's stream contract at the
// CLI boundary: the JSON document is exactly one value plus exactly one
// newline on stdout, and every operational byte is on stderr.
func TestReparentE2E_PlanJSONIsTheOnlyStdout(t *testing.T) {
	f := newReparentCustomerExternal(t)
	stdout, _ := f.Plan(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--json")

	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("the JSON document must end with exactly one newline:\n%q", stdout)
	}
	if strings.HasSuffix(stdout, "\n\n") {
		t.Fatalf("the JSON document must not end with two newlines:\n%q", stdout)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not exactly one JSON value: %v\n%s", err, stdout)
	}
	if got, ok := doc["schema_version"].(float64); !ok || int(got) != internal.ReparentPlanSchemaVersion {
		t.Fatalf("schema_version = %v, want %d", doc["schema_version"], internal.ReparentPlanSchemaVersion)
	}
	if _, ok := doc["reparent"]; ok {
		t.Fatal("the plan document must not carry a nested reparent key")
	}
}

// reparentIsAncestorOf answers the ancestry question with Git itself.
func reparentIsAncestorOf(t *testing.T, f *reparentFixture, ancestor, descendant string) bool {
	t.Helper()
	out, err := reparentRunAllowFail(t, f.Repo, "merge-base", "--is-ancestor", ancestor, descendant)
	_ = out
	return err == nil
}

// assertReparentResidueGone is §11.6a's cleanup contract: after a successful
// run nothing of the transaction survives — not the authoritative artifact,
// not the compatibility envelope, not the scratch tree.
func assertReparentResidueGone(t *testing.T, f *reparentFixture) {
	t.Helper()
	loc := f.Loc()
	if internal.HasReparentState(loc) {
		t.Fatalf("the authoritative artifact survived a successful run: %s", internal.ReparentStatePath(loc))
	}
	if f.Mode == internal.ModeExternal {
		if internal.HasSyncState(f.FeaturePath) {
			t.Fatal("the legacy sentinel survived a successful run")
		}
		if internal.HasSyncRunState(f.FeaturePath) {
			t.Fatal("the v4 compatibility payload survived a successful run")
		}
	} else if internal.HasCheckoutTransaction(f.FeaturePath) {
		t.Fatal("the compatibility transaction survived a successful run")
	}
}

// TestReparentE2E_NoRecordedCutoffRefusesThenAcceptsAnExplicitOne exercises
// §5.1's no-recorded-base rung end to end (AC-018). The ladder does NOT guess:
// with `last_base_sha` cleared it refuses `cutoff-absent` and names both
// remedies, and supplying --cutoff makes the identical run succeed.
func TestReparentE2E_NoRecordedCutoffRefusesThenAcceptsAnExplicitOne(t *testing.T) {
	f := newReparentNoCutoffExternal(t)
	if got := f.Entry("pr2").LastBaseSHA; got != "" {
		t.Fatalf("the fixture must clear the recorded cutoff, got %q", got)
	}

	// A plan still PRODUCES a document and exits 0 — exit status is never an
	// admission predicate — but it publishes the blocker and mints no token.
	human, prose := f.Plan(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--max-replay-total", "50")
	if !strings.Contains(human, "cutoff-absent") {
		t.Fatalf("the plan must publish the cutoff-absent blocker:\n%s\n%s", human, prose)
	}
	if !strings.Contains(human, "--cutoff <ref>") {
		t.Fatalf("the blocker must name the explicit-cutoff remedy:\n%s", human)
	}
	if token := reparentApprovalToken(human); reparentFingerprintShape.MatchString(token) {
		t.Fatalf("an unrunnable plan must mint no usable fingerprint, got %q", token)
	}

	// The identical run succeeds once the operator authors the boundary.
	stdout, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch", "--cutoff", f.B)
	if exit != 0 {
		t.Fatalf("an explicit cutoff must make the run admissible, exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	master := f.SHA("master")
	if !reparentIsAncestorOf(t, f, master, f.SHA("feat-pr2")) {
		t.Fatal("the target must end up on the destination")
	}
	replayed := strings.Fields(f.reparentGit(f.Repo, "rev-list", master+"..feat-pr2"))
	if len(replayed) != 1 {
		t.Fatalf("the operator-authored cutoff must bound the replay to C, got %d: %v", len(replayed), replayed)
	}
	if got := f.Entry("pr2").LastBaseSHA; got != master {
		t.Fatalf("last_base_sha = %q, want the destination tip %s", got, master)
	}
	assertReparentResidueGone(t, f)
}

// TestReparentE2E_StaleDescendantCutoffPausesOnConflict is issue #4's
// stale-cutoff half (AC-021, AC-057). A cutoff older than the destination's own
// rewritten history widens the replay set until it genuinely conflicts, and
// that is a PAUSE, not a refusal: it carries no marker, prints the exact
// resume instructions, and leaves a run an --abort can roll back completely.
func TestReparentE2E_StaleDescendantCutoffPausesOnConflict(t *testing.T) {
	f := newReparentStaleDescendantExternal(t)
	root := f.reparentGit(f.Repo, "rev-list", "--max-parents=0", "HEAD")
	if got := f.Entry("pr2").LastBaseSHA; got != root {
		t.Fatalf("the fixture must record a stale cutoff, got %q", got)
	}
	refsBefore := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")

	_, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch")
	if exit != 1 {
		t.Fatalf("a conflict pause must exit 1, got %d:\n%s", exit, stderr)
	}
	if reparentMarkerRe.MatchString(stderr) {
		t.Fatalf("a conflict is a PAUSE and must carry no reparent marker:\n%s", stderr)
	}
	if lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n"); len(lines) != 5 {
		t.Fatalf("the conflict pause must be exactly five lines once, got %d:\n%s", len(lines), stderr)
	}
	for _, want := range []string{
		"reparent paused: conflict while computing pr2 (feat-pr2)",
		"resolve in: ",
		"rebase --continue",
		"tws stack reparent " + f.Feature + " --continue",
		"to discard the whole reparent: tws stack reparent " + f.Feature + " --abort",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("the pause must print %q:\n%s", want, stderr)
		}
		if strings.Count(stderr, want) != 1 {
			t.Fatalf("the pause line %q must appear exactly once:\n%s", want, stderr)
		}
	}
	if !internal.HasReparentState(f.Loc()) {
		t.Fatal("a paused run must leave a resumable artifact")
	}

	// The rollback is complete: every public ref returns to its pre-image.
	stdout, stderr, exit := f.Run(f.Feature, "--abort")
	if exit != 0 {
		t.Fatalf("--abort exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if after := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); after != refsBefore {
		t.Fatalf("aborting a paused run must restore every ref:\n--- before ---\n%s\n--- after ---\n%s", refsBefore, after)
	}
	assertReparentResidueGone(t, f)
}

// TestReparentE2E_ReftableBackendIsReportedAndCrashAtomic is the reftable
// variant (AC-060). It SKIPS with a clear reason when the backend is
// unavailable; its files-backend twin — every other cell in this file — is
// mandatory and never skipped, because CI pins no Git version and a
// reftable-only assertion would silently vanish on the older matrix leg.
func TestReparentE2E_ReftableBackendIsReportedAndCrashAtomic(t *testing.T) {
	f := newReparentReftableCheckout(t)
	if f == nil {
		t.Skip("this git cannot create a reftable repository; the files-backend twin covers the same contract")
	}

	human, prose := f.Plan(f.Feature, "pr1", "--onto", "main", "--max-replay-total", "50")
	if !strings.Contains(human, "backend=reftable") {
		t.Fatalf("the plan must report the measured ref backend:\n%s\n%s", human, prose)
	}
	if !strings.Contains(human, "crash-atomic=yes") {
		t.Fatalf("a reftable transaction is crash-atomic and must say so:\n%s", human)
	}

	stdout, stderr, exit := reparentExecute(t, f, "pr1", "main")
	if exit != 0 {
		t.Fatalf("execution exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	main := f.SHA("main")
	for _, branch := range []string{"pr1", "pr2"} {
		if !reparentIsAncestorOf(t, f, main, f.SHA(branch)) {
			t.Fatalf("%s must end up on the destination", branch)
		}
	}
	assertReparentResidueGone(t, f)
}
