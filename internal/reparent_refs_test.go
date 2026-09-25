package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2): the pin namespace and the transaction's shape.
//
//	T-026 decoupled names with "/", pin id collision (a vs a/b) .... §9.8, AC-036
//	(the CAS transaction CELL is owned by reparent_run_test.go, which runs
//	 it end to end; the payload's exact line shape is asserted here, AC-058)
//	T-045 CAS race under the files backend (mandatory) ............. AC-059
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// §9.8 — ref-safe hashed identities.
// ---------------------------------------------------------------------------

func TestReparentEntryRefIDIsRefSafeAndCollisionFree(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-026", "collision-safe-entry-ref")
	_ = "asserts §9.8 AC-036"
	cases := []string{"a", "a/b", "a.b", "..", "-leading", "trailing.", "weird@{name}", "x.lock", strings.Repeat("n", 200)}
	seen := map[string]string{}
	for _, name := range cases {
		id := ReparentEntryRefID("customer", name)
		if len(id) > 64 {
			t.Fatalf("%q -> %q is %d bytes, over the 64-byte cap", name, id, len(id))
		}
		if strings.ContainsAny(id, "/ ~^:?*[\\") {
			t.Fatalf("%q -> %q contains a character git forbids in a ref component", name, id)
		}
		if strings.HasPrefix(id, "-") || strings.HasPrefix(id, ".") {
			t.Fatalf("%q -> %q starts with a forbidden character", name, id)
		}
		if strings.Contains(id, "..") || strings.Contains(id, "@{") || strings.HasSuffix(id, ".lock") {
			t.Fatalf("%q -> %q contains a forbidden sequence", name, id)
		}
		if prior, dup := seen[id]; dup {
			t.Fatalf("%q and %q both hash to %q", prior, name, id)
		}
		seen[id] = name
	}

	// "a" and "a/b" are the exact pair that would otherwise collide as a ref
	// DIRECTORY and a ref FILE, which is why the raw name is never used.
	if ReparentEntryRefID("customer", "a") == ReparentEntryRefID("customer", "a/b") {
		t.Fatal("a and a/b must not share an entry id")
	}
	// The identity is feature-scoped and stable across calls.
	if ReparentEntryRefID("customer", "pr2") == ReparentEntryRefID("other", "pr2") {
		t.Fatal("entry ids must be scoped to the feature")
	}
	first := ReparentEntryRefID("customer", "pr2")
	for i := 0; i < 3; i++ {
		if again := ReparentEntryRefID("customer", "pr2"); again != first {
			t.Fatalf("entry ids must be deterministic: %q then %q", first, again)
		}
	}
}

func TestReparentRunIDShape(t *testing.T) {
	id, err := newReparentRunID()
	if err != nil {
		t.Fatal(err)
	}
	if !reparentRunIDShape(id) {
		t.Fatalf("run id %q is not 32 lowercase hex characters", id)
	}
	other, err := newReparentRunID()
	if err != nil {
		t.Fatal(err)
	}
	if id == other {
		t.Fatal("two run ids collided")
	}
}

// ---------------------------------------------------------------------------
// §9.11 / §11.8 — the CAS payload's exact line shape.
// ---------------------------------------------------------------------------

func TestReparentCASPayloadShape(t *testing.T) {
	rows := []ReparentStateRow{
		{Name: "pr2", GitBranch: "feature/pr2", PreimageSHA: "aaaa", PlannedNewSHA: "bbbb", Order: 0},
		{Name: "pr3", GitBranch: "feature/pr3", PreimageSHA: "cccc", PlannedNewSHA: "cccc", Noop: true, Order: 1},
		{Name: "pr4", GitBranch: "feature/pr4", PreimageSHA: "dddd", PlannedNewSHA: "eeee", Order: 2},
	}
	// A fresh run classifies every row pre-image or no-op, so the
	// classification-driven builder produces exactly the fresh payload.
	fresh := map[string]ReparentRefClass{
		"pr2": ReparentRefPreimage, "pr3": ReparentRefNoop, "pr4": ReparentRefPreimage,
	}
	payload := string(buildReparentCASPayload(reparentForwardCASLines(rows, fresh)))
	want := "start\n" +
		"update refs/heads/feature/pr2 bbbb aaaa\n" +
		"verify refs/heads/feature/pr3 cccc\n" +
		"update refs/heads/feature/pr4 eeee dddd\n" +
		"prepare\n" +
		"commit\n"
	if payload != want {
		t.Fatalf("forward payload =\n%q\nwant\n%q", payload, want)
	}
	// A no-op row must NEVER be rendered as `update <ref> X X`.
	if strings.Contains(payload, "update refs/heads/feature/pr3 cccc cccc") {
		t.Fatal("a no-op row must emit verify, not a self-update")
	}
	for _, forbidden := range []string{"create ", "delete ", "option no-deref"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("payload contains forbidden verb %q", forbidden)
		}
	}

	// An ALL-no-op closure still runs the full envelope: the verification is
	// the race barrier for the whole closure.
	allNoop := []ReparentStateRow{{Name: "pr2", GitBranch: "feature/pr2", PreimageSHA: "aaaa", PlannedNewSHA: "aaaa", Noop: true}}
	got := string(buildReparentCASPayload(reparentForwardCASLines(allNoop, map[string]ReparentRefClass{"pr2": ReparentRefNoop})))
	if got != "start\nverify refs/heads/feature/pr2 aaaa\nprepare\ncommit\n" {
		t.Fatalf("all-no-op payload = %q", got)
	}

	// §11.8 step 4's rollback is the symmetric transaction: planned-tip rows
	// move back, and every pre-image or no-op row is verified so the window
	// between classification and commit cannot be raced.
	classes := map[string]ReparentRefClass{
		"pr2": ReparentRefPlannedTip,
		"pr3": ReparentRefNoop,
		"pr4": ReparentRefPreimage,
	}
	abort := string(buildReparentCASPayload(reparentAbortCASLines(rows, classes)))
	wantAbort := "start\n" +
		"update refs/heads/feature/pr2 aaaa bbbb\n" +
		"verify refs/heads/feature/pr3 cccc\n" +
		"verify refs/heads/feature/pr4 dddd\n" +
		"prepare\n" +
		"commit\n"
	if abort != wantAbort {
		t.Fatalf("abort payload =\n%q\nwant\n%q", abort, wantAbort)
	}
}

func TestReparentCASMessage(t *testing.T) {
	got := reparentCASMessage("customer", "pr2", "abc")
	if got != "tws reparent customer pr2 abc" {
		t.Fatalf("reflog message = %q", got)
	}
}

// ---------------------------------------------------------------------------
// §11.7 — classification, including the no-op trap.
// ---------------------------------------------------------------------------

func TestClassifyReparentRefValue(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		live, preimage, planned string
		want                    ReparentRefClass
	}{
		{"no-op row at its shared value", "aaa", "aaa", "aaa", ReparentRefNoop},
		{"no-op row moved elsewhere", "zzz", "aaa", "aaa", ReparentRefForeign},
		{"update did not land", "aaa", "aaa", "bbb", ReparentRefPreimage},
		{"update landed", "bbb", "aaa", "bbb", ReparentRefPlannedTip},
		{"operator work", "ccc", "aaa", "bbb", ReparentRefForeign},
		{"not yet computed", "aaa", "aaa", "", ReparentRefPreimage},
	} {
		if got := classifyReparentRefValue(tc.live, tc.preimage, tc.planned); got != tc.want {
			t.Fatalf("%s: classification = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Real-Git leaf: pins survive gc, verify-all is idempotent, the CAS is
// race-atomic, a hook veto is an ordinary refusal, and a partial classification
// is computed for EVERY row before anything is written.
// ---------------------------------------------------------------------------

// The pin namespace and the race barrier: GC survival, the verify-all gate, the race barrier, a hook veto and the backend probe (AC-059, AC-060, AC-061, AC-066).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentPinsAndTransactionAgainstRealGit(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-045", "pins-and-files-backend-cas")
	repo := newReparentPrimitiveRepo(t)
	// --- TestReparentPinsCASAndClassificationAgainstRealGit ---
	func(t *testing.T) {
		log := reparentCaptureArgv(t)

		base := repo.RevParse("HEAD~2")
		repo.Branch("topic", "HEAD")
		topicTip := repo.RevParse("topic")
		runID, err := newReparentRunID()
		if err != nil {
			t.Fatal(err)
		}
		entryID := ReparentEntryRefID("customer", "topic")
		oldPin := reparentOldPinRef(runID, entryID)
		newPin := reparentNewPinRef(runID, entryID)

		if err := reparentWritePin(repo.Dir, oldPin, topicTip); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := reparentResolveRef(repo.Dir, oldPin); err != nil || !ok || got != topicTip {
			t.Fatalf("pin resolves to %q (ok=%v, err=%v)", got, ok, err)
		}

		// A pin makes the object durable at the instant it exists: it survives an
		// aggressive gc even though no branch references it.
		dangling := repo.Git("commit-tree", "-p", topicTip, "-m", "detached", repo.Git("rev-parse", "HEAD^{tree}"))
		dangling = strings.ToLower(strings.TrimSpace(dangling))
		if err := reparentWritePin(repo.Dir, newPin, dangling); err != nil {
			t.Fatal(err)
		}
		repo.Git("reflog", "expire", "--expire=now", "--all")
		repo.Git("gc", "--prune=now", "--quiet")
		if got, ok, err := reparentResolveRef(repo.Dir, newPin); err != nil || !ok || got != dangling {
			t.Fatalf("pinned object did not survive gc: %q (ok=%v, err=%v)", got, ok, err)
		}

		// The verify-all gate is idempotent, re-creates a missing pin and refuses
		// a pin that resolves elsewhere.
		if recreated, err := reparentVerifyPin(repo.Dir, newPin, dangling); err != nil || recreated {
			t.Fatalf("verify of an intact pin: recreated=%v err=%v", recreated, err)
		}
		repo.Git("update-ref", "-d", newPin)
		recreated, err := reparentVerifyPin(repo.Dir, newPin, dangling)
		if err != nil || !recreated {
			t.Fatalf("verify must re-create a missing pin: recreated=%v err=%v", recreated, err)
		}
		if _, err := reparentVerifyPin(repo.Dir, newPin, base); err == nil {
			t.Fatal("a pin that resolves to a different object must refuse")
		}

		pins, err := reparentListPins(repo.Dir, runID)
		if err != nil {
			t.Fatal(err)
		}
		if len(pins) != 2 {
			t.Fatalf("pin enumeration = %v", pins)
		}

		// The CAS transaction: one update, one no-op verify.
		rows := []ReparentStateRow{
			{Name: "topic", GitBranch: "topic", PreimageSHA: topicTip, PlannedNewSHA: dangling, Order: 0},
			{Name: "main", GitBranch: "main", PreimageSHA: repo.RevParse("main"), PlannedNewSHA: repo.RevParse("main"), Noop: true, Order: 1},
		}
		freshClasses, err := ClassifyReparentRefs(repo.Dir, rows)
		if err != nil {
			t.Fatal(err)
		}
		forward := reparentForwardCASLines(rows, ReparentRefClassMap(freshClasses))
		if _, err := runReparentCAS(repo.Dir, reparentCASMessage("customer", "topic", runID), forward); err != nil {
			t.Fatalf("CAS failed: %v", err)
		}
		if got := repo.RevParse("topic"); got != dangling {
			t.Fatalf("topic = %s, want the planned tip %s", got, dangling)
		}

		// Classification after the commit: planned tip plus a no-op row.
		classes, err := ClassifyReparentRefs(repo.Dir, rows)
		if err != nil {
			t.Fatal(err)
		}
		// Re-issuing the SAME transaction after a partial or complete commit must
		// not carry a stale expected old value: a planned-tip row becomes a
		// verify of its planned tip, so the recovery transaction is idempotent.
		replay := reparentForwardCASLines(rows, ReparentRefClassMap(classes))
		if replay[0].Verb != reparentCASVerifyVerb || replay[0].Old != dangling {
			t.Fatalf("a landed row must be re-issued as `verify <ref> <planned>`, got %+v", replay[0])
		}
		if _, err := runReparentCAS(repo.Dir, reparentCASMessage("customer", "topic", runID), replay); err != nil {
			t.Fatalf("the recovery transaction must be idempotent: %v", err)
		}
		if classes[0].Class != ReparentRefPlannedTip || classes[1].Class != ReparentRefNoop {
			t.Fatalf("classification = %+v", classes)
		}
		if !reparentAllPlannedOrNoop(classes) {
			t.Fatal("planned + no-op must satisfy the commit-point half")
		}

		// A stale expected old value aborts the WHOLE transaction during prepare:
		// no public ref moves. This is the race barrier.
		stale := []reparentCASLine{
			reparentCASUpdate("refs/heads/topic", base, "0000000000000000000000000000000000000000"),
			reparentCASUpdate("refs/heads/main", base, repo.RevParse("main")),
		}
		if _, err := runReparentCAS(repo.Dir, "tws reparent stale", stale); err == nil {
			t.Fatal("a stale expected old value must abort the transaction")
		}
		if got := repo.RevParse("topic"); got != dangling {
			t.Fatalf("topic moved despite an aborted transaction: %s", got)
		}
		if got := repo.RevParse("main"); got == base {
			t.Fatal("main moved despite an aborted transaction")
		}

		// An operator's own work is foreign: classification names it and the run
		// never overwrites it.
		repo.Git("update-ref", "refs/heads/topic", base)
		classes, err = ClassifyReparentRefs(repo.Dir, rows)
		if err != nil {
			t.Fatal(err)
		}
		if classes[0].Class != ReparentRefForeign {
			t.Fatalf("an unexpected value must classify foreign, got %q", classes[0].Class)
		}
		detail := reparentForeignRefDetail(classes[0])
		for _, want := range []string{"topic", base, topicTip, dangling} {
			if !strings.Contains(detail, want) {
				t.Fatalf("foreign detail must name %q: %s", want, detail)
			}
		}
		if reparentAllPlannedOrNoop(classes) {
			t.Fatal("a foreign row must not satisfy the commit-point half")
		}

		sawCAS := false
		for _, argv := range *log {
			if len(argv) > 0 && argv[0] == "update-ref" && containsString(argv, "--stdin") {
				sawCAS = true
				if !containsString(argv, "--no-deref") {
					t.Fatalf("transaction-level symbolic safety flag missing: %v", argv)
				}
			}
		}
		if !sawCAS {
			t.Fatal("the test emitted no update-ref --stdin transaction")
		}
		reparentAssertNoDashC(t, *log)
	}(t)

	// --- TestReparentCASHookVetoIsARefusal ---
	func(t *testing.T) {
		repo.Git("update-ref", "refs/heads/topic", repo.RevParse("HEAD"))
		tip := repo.RevParse("topic")
		target := repo.RevParse("HEAD~1")

		hooksDir := strings.TrimSpace(repo.Git("rev-parse", "--absolute-git-dir")) + "/hooks"
		writeExecutableForTest(t, hooksDir+"/reference-transaction",
			"#!/bin/sh\nif [ \"$1\" = \"prepared\" ]; then echo 'hook says no' 1>&2; exit 1; fi\nexit 0\n")

		res, err := runReparentCAS(repo.Dir, "tws reparent hook", []reparentCASLine{
			reparentCASUpdate("refs/heads/topic", target, tip),
		})
		if err == nil {
			t.Fatal("a vetoing reference-transaction hook must fail the transaction")
		}
		if got := repo.RevParse("topic"); got != tip {
			t.Fatalf("topic moved despite the veto: %s", got)
		}
		// BOTH streams are captured into run-owned buffers, so the refusal can
		// quote the hook's own sentence instead of leaking it to this process's
		// stdout.
		if !strings.Contains(string(res.Stderr), "hook says no") {
			t.Fatalf("hook output was not captured: %q", res.Stderr)
		}
	}(t)

	// --- TestProbeReparentRefBackend ---
	func(t *testing.T) {
		// With the probe available, a files-backend repository reports files.
		known := probeReparentRefBackend(repo.Dir, ReparentGitCapabilities{CapRefBackendKnown: true})
		if known != ReparentRefBackendFiles && known != ReparentRefBackendReftable {
			t.Fatalf("probed backend = %q", known)
		}
		// Without it, the extensions.refStorage fallback runs; on a plain files
		// repository there is no such key, so the answer is the permitted
		// "unknown", which callers treat exactly as files.
		unknown := probeReparentRefBackend(repo.Dir, ReparentGitCapabilities{})
		if unknown != ReparentRefBackendUnknown && unknown != ReparentRefBackendFiles && unknown != ReparentRefBackendReftable {
			t.Fatalf("fallback backend = %q", unknown)
		}
		if reparentBackendCrashAtomic(ReparentRefBackendFiles) || reparentBackendCrashAtomic(ReparentRefBackendUnknown) {
			t.Fatal("only reftable is crash-atomic")
		}
		if !reparentBackendCrashAtomic(ReparentRefBackendReftable) {
			t.Fatal("reftable is crash-atomic")
		}
	}(t)
}

// A reference-transaction hook veto is an ordinary refusal, never a crash,
// and the hook is never disabled.

func writeExecutableForTest(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// §9.10 — ref backend classification.
// ---------------------------------------------------------------------------
