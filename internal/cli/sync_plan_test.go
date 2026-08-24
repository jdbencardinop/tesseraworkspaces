package cli

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// TestSyncPlan: external plan orchestration (spec §13.7a), rows-less
// runnability, the I14 preflight, the pre-acquisition snapshot, the
// --plan --continue no-fetch/remaining-work-only rule, and the §3.6 stream
// discipline (including renderPlanDocumentTo's own writer contract).
// ---------------------------------------------------------------------------

// planDoc decodes a --plan --json invocation's stdout into a generic map,
// failing loudly if it is not exactly one JSON value.
func planDoc(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout must decode as one JSON value: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout must carry exactly one JSON value, found a second: %s", stdout)
	}
	return doc
}

func planField(t *testing.T, doc map[string]any, path ...string) any {
	t.Helper()
	var cur any = doc
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not an object (at %q), doc=%v", path, p, p, doc)
		}
		cur, ok = m[p]
		if !ok {
			t.Fatalf("path %v: key %q missing, doc=%v", path, p, doc)
		}
	}
	return cur
}

// ---------------------------------------------------------------------------
// Orchestration order: exactly one inspection, one fetch, per invocation.
// ---------------------------------------------------------------------------

// TestSyncPlan_FreshRouteInspectsAndFetchesExactlyOnce proves runExternalPlan
// calls buildExternalPlan (hence InspectExternalPlan) exactly once and its
// own fetch loop runs exactly once per unique selected repo: the fixture has
// one repo, so "git fetch" must appear exactly once in the git command log,
// and runExternalPlan's own header print (printSyncModeHeaderTo, the single
// line immediately preceding its single buildExternalPlan/InspectExternalPlan
// call — spec §13.7a's own ordering) must appear exactly once on stderr. A
// second inspection pass would double one of these two independent,
// differently-sourced counters; neither is sensitive to the per-entry
// probing (config/status/--git-common-dir per stack entry) BuildRebasePlan's
// own entry measurement legitimately repeats once per stack entry regardless
// of how many times InspectExternalPlan itself ran.
func TestSyncPlan_FreshRouteInspectsAndFetchesExactlyOnce(t *testing.T) {
	f := newScopedFixture(t)
	w := newSyncGitWrapper(t, false)
	var stdout, stderr string
	var exit int
	w.around(t, func() {
		stdout, stderr, exit = runSync(t, f.feature, "--plan")
	})
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	if !strings.Contains(stdout, "Plan: route=") {
		t.Fatalf("stdout must carry the human plan document: %q", stdout)
	}

	fetches := 0
	for _, r := range w.records(t) {
		if r.Verb == "fetch" {
			fetches++
		}
	}
	if fetches != 1 {
		t.Fatalf("expected exactly one git fetch for the single-repo fixture, got %d", fetches)
	}
	if n := strings.Count(stderr, "Sync mode: "); n != 1 {
		t.Fatalf("expected exactly one \"Sync mode: \" header (one runExternalPlan/InspectExternalPlan pass), got %d in %q", n, stderr)
	}
	if n := strings.Count(stdout, "Plan: route="); n != 1 {
		t.Fatalf("expected exactly one rendered plan document (one renderPlanDocument call), got %d in %q", n, stdout)
	}
}

// TestSyncPlan_NoFetchStillInspectsExactlyOnceWithoutFetching proves the
// inspection pass itself is unconditional (always exactly one, evidenced by
// exactly one mode header), while the fetch step is conditioned on policy
// alone: --no-fetch must produce zero "git fetch" invocations.
func TestSyncPlan_NoFetchStillInspectsExactlyOnceWithoutFetching(t *testing.T) {
	f := newScopedFixture(t)
	w := newSyncGitWrapper(t, false)
	var stdout, stderr string
	var exit int
	w.around(t, func() {
		stdout, stderr, exit = runSync(t, f.feature, "--plan", "--no-fetch")
	})
	if exit != 0 {
		t.Fatal("a --no-fetch plan must succeed")
	}
	fetches := 0
	for _, r := range w.records(t) {
		if r.Verb == "fetch" {
			fetches++
		}
	}
	if fetches != 0 {
		t.Fatalf("--no-fetch must never fetch, got %d", fetches)
	}
	if n := strings.Count(stderr, "Sync mode: "); n != 1 {
		t.Fatalf("expected exactly one \"Sync mode: \" header (one inspection pass) even with --no-fetch, got %d in %q", n, stderr)
	}
	if n := strings.Count(stdout, "Plan: route="); n != 1 {
		t.Fatalf("expected exactly one rendered plan document, got %d in %q", n, stdout)
	}
}

// ---------------------------------------------------------------------------
// Rows-less runnability: plannability empty/unavailable, correct refusal.kind.
// ---------------------------------------------------------------------------

// TestSyncPlan_UnavailableRowsPublishTheRightRefusalKind proves both an
// unreadable stack.yaml and a cyclic one leave RowsAvailable false (so
// summary.plannability is "unavailable" and entries[] is empty), yet each
// publishes ITS OWN distinct refusal.kind — plan-unavailable for the read
// failure, stack-unsortable for the cycle — never conflating the two, and
// both do so at exit 0 (a --plan never hard-fails the process over a
// document-describable fact).
func TestSyncPlan_UnavailableRowsPublishTheRightRefusalKind(t *testing.T) {
	t.Run("unreadable-stack", func(t *testing.T) {
		f := newScopedFixture(t)
		if err := os.WriteFile(internal.StackPath(f.featurePath), []byte("not: [valid: yaml: :::"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("a --plan must exit 0 even over an unreadable stack: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		if got := planField(t, doc, "summary", "plannability"); got != "unavailable" {
			t.Fatalf("plannability = %v, want \"unavailable\"", got)
		}
		if got := planField(t, doc, "refusal", "kind"); got != "plan-unavailable" {
			t.Fatalf("refusal.kind = %v, want \"plan-unavailable\"", got)
		}
		if got := planField(t, doc, "entries"); len(got.([]any)) != 0 {
			t.Fatalf("entries must be empty when rows are unavailable, got %v", got)
		}
	})

	t.Run("cyclic-stack", func(t *testing.T) {
		f := newScopedFixture(t)
		stack, err := internal.LoadStack(f.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		for i := range stack.Branches {
			if stack.Branches[i].Name == "root" {
				stack.Branches[i].Base = "child" // root -> child -> parent -> root
			}
		}
		if err := internal.SaveStack(f.featurePath, stack); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch")
		if exit != 0 {
			t.Fatalf("a --plan must exit 0 even over a cyclic stack: exit=%d stderr=%q", exit, stderr)
		}
		doc := planDoc(t, stdout)
		if got := planField(t, doc, "summary", "plannability"); got != "unavailable" {
			t.Fatalf("plannability = %v, want \"unavailable\"", got)
		}
		if got := planField(t, doc, "refusal", "kind"); got != "stack-unsortable" {
			t.Fatalf("refusal.kind = %v, want \"stack-unsortable\" (distinct from the unreadable-stack case)", got)
		}
		if got := planField(t, doc, "entries"); len(got.([]any)) != 0 {
			t.Fatalf("entries must be empty when rows are unavailable, got %v", got)
		}
	})
}

// TestSyncPlan_EmptyRowsPublishNullRefusal proves a stack that loads, sorts
// and selects cleanly but has zero branches is a DIFFERENT rows-less shape:
// plannability "empty" (not "unavailable"), a null refusal.kind (there is
// nothing to refuse — an empty stack is trivially runnable), and runnable
// true.
func TestSyncPlan_EmptyRowsPublishNullRefusal(t *testing.T) {
	f := newScopedFixture(t)
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: nil}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	doc := planDoc(t, stdout)
	if got := planField(t, doc, "summary", "plannability"); got != "empty" {
		t.Fatalf("plannability = %v, want \"empty\"", got)
	}
	if got := planField(t, doc, "refusal", "kind"); got != nil {
		t.Fatalf("refusal.kind = %v, want null", got)
	}
	if got := planField(t, doc, "runnable"); got != true {
		t.Fatalf("runnable = %v, want true (an empty stack has nothing to refuse)", got)
	}
}

// ---------------------------------------------------------------------------
// The I14 preflight: --plan describes it in the document, at exit 0, rather
// than hard-failing the process the way the executing route does
// (TestSyncScoped_NoFetchUnresolvableBaseIsAPreflightFatal).
// ---------------------------------------------------------------------------

func TestSyncPlan_I14PreflightIsDescribedNotEnforced(t *testing.T) {
	f := newScopedFixture(t)
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range stack.Branches {
		if stack.Branches[i].Name == "child" {
			stack.Branches[i].Base = "does-not-exist"
		}
	}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runSync(t, f.feature, "--plan", "--json", "--only", "child", "--no-fetch")
	if exit != 0 {
		t.Fatalf("a --plan must never hard-fail over I14: exit=%d stderr=%q", exit, stderr)
	}
	doc := planDoc(t, stdout)
	if got := planField(t, doc, "refusal", "kind"); got != "preflight-refused" {
		t.Fatalf("refusal.kind = %v, want \"preflight-refused\"", got)
	}
	wantDetail := `base "does-not-exist" for stack entry "child" does not resolve locally; drop --no-fetch or fetch manually first`
	if got := planField(t, doc, "refusal", "detail"); got != wantDetail {
		t.Fatalf("refusal.detail = %v, want %q", got, wantDetail)
	}
	if got := planField(t, doc, "runnable"); got != false {
		t.Fatalf("runnable = %v, want false", got)
	}
	// Unlike the rows-less cases above, I14 still resolves rows: the entry
	// is described (with an unresolved base), never suppressed.
	if got := planField(t, doc, "summary", "plannability"); got != "rows" {
		t.Fatalf("plannability = %v, want \"rows\" (I14 still describes the entry)", got)
	}
	entries := planField(t, doc, "entries").([]any)
	if len(entries) != 1 {
		t.Fatalf("expected exactly one described entry, got %d", len(entries))
	}

	// Confirm the executing route still hard-fails identically to before:
	// --plan changes nothing about I14's own executing behaviour.
	f2 := newScopedFixture(t)
	stack2, err := internal.LoadStack(f2.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range stack2.Branches {
		if stack2.Branches[i].Name == "child" {
			stack2.Branches[i].Base = "does-not-exist"
		}
	}
	if err := internal.SaveStack(f2.featurePath, stack2); err != nil {
		t.Fatal(err)
	}
	if _, _, exit := runSync(t, f2.feature, "--only", "child", "--no-fetch"); exit == 0 {
		t.Fatal("the executing (non-plan) route must still hard-fail I14")
	}
}

// ---------------------------------------------------------------------------
// The pre-acquisition snapshot: state.snapshot is captured before any lock
// or guard acquisition, and names the CURRENT process.
// ---------------------------------------------------------------------------

func TestSyncPlan_PreAcquisitionSnapshotNamesThisProcess(t *testing.T) {
	f := newScopedFixture(t)
	stdout, stderr, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	doc := planDoc(t, stdout)
	if got := planField(t, doc, "state", "snapshot", "taken_before_acquisition"); got != true {
		t.Fatalf("state.snapshot.taken_before_acquisition = %v, want true", got)
	}
	gotPID, ok := planField(t, doc, "state", "snapshot", "self_pid").(float64)
	if !ok {
		t.Fatalf("state.snapshot.self_pid must be a number, doc=%v", doc)
	}
	if int(gotPID) != os.Getpid() {
		t.Fatalf("state.snapshot.self_pid = %d, want this test process's own pid %d", int(gotPID), os.Getpid())
	}
}

// ---------------------------------------------------------------------------
// --plan --continue: never fetches, describes remaining work only.
// ---------------------------------------------------------------------------

// TestSyncPlan_ContinuePlanNeverFetchesAndDescribesRemainingWorkOnly builds
// a genuine scoped conflict (root+parent complete, child pending), then
// proves --plan --continue (a) never issues a git fetch even though fetch
// policy defaults to enabled and no --no-fetch was supplied, (b) reports the
// suppression cause as a continuation fact, and (c) describes ONLY the
// still-pending entry — never re-describing the already-completed ones as
// if they were part of a fresh plan.
func TestSyncPlan_ContinuePlanNeverFetchesAndDescribesRemainingWorkOnly(t *testing.T) {
	f := newScopedFixture(t)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child", "--no-fetch"); exit == 0 {
		t.Fatal("expected the seeded conflict to stop the run")
	}
	f.detachGuard(t)
	resolveRebase(t, f.wt("child"))

	w := newSyncGitWrapper(t, false)
	var stdout, stderr string
	var exit int
	w.around(t, func() {
		stdout, stderr, exit = runSync(t, f.feature, "--plan", "--continue", "--json")
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
	entries := planField(t, doc, "entries").([]any)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	if len(names) != 1 || names[0] != "child" {
		t.Fatalf("entries = %v, want exactly [\"child\"] (the sole still-pending entry)", names)
	}
}

// ---------------------------------------------------------------------------
// Stream discipline (spec §3.6): human document + "Sync mode:"/fetch prose
// split across stdout/stderr; --json is exactly one value plus one newline.
// ---------------------------------------------------------------------------

func TestSyncPlan_HumanDocumentIsStdoutOnlyModeHeaderAndFetchProseAreStderrOnly(t *testing.T) {
	f := newScopedFixture(t)
	stdout, stderr, exit := runSync(t, f.feature, "--plan")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	if !strings.HasPrefix(stdout, "Plan: route=") {
		t.Fatalf("stdout must open with the plan document header, got %q", stdout)
	}
	if !strings.Contains(stdout, "Approval fingerprint:") {
		t.Fatalf("stdout must carry the document's own approval-fingerprint tail, got %q", stdout)
	}
	if strings.Contains(stdout, "Sync mode:") {
		t.Fatalf("the mode header must never reach stdout: %q", stdout)
	}
	if strings.Contains(stdout, "Fetching ") {
		t.Fatalf("fetch prose must never reach stdout: %q", stdout)
	}
	if !strings.HasPrefix(stderr, "Sync mode: ") {
		t.Fatalf("stderr must open with the mode header, got %q", stderr)
	}
	if !strings.Contains(stderr, "Fetching ") {
		t.Fatalf("stderr must carry the fetch prose, got %q", stderr)
	}
}

func TestSyncPlan_JSONModeIsExactlyOneValuePlusOneNewlineOnStdout(t *testing.T) {
	f := newScopedFixture(t)
	stdout, stderr, exit := runSync(t, f.feature, "--plan", "--json", "--no-fetch")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	if !strings.HasSuffix(stdout, "}\n") {
		tail := stdout
		if len(tail) > 8 {
			tail = tail[len(tail)-8:]
		}
		t.Fatalf("stdout must end with the JSON value's own closing brace and one trailing newline, got %q", tail)
	}
	if strings.Count(stdout, "\n") != 1 {
		t.Fatalf("stdout must carry exactly one newline (the encoder's own trailing one), got %d in %q", strings.Count(stdout, "\n"), stdout)
	}
	_ = planDoc(t, stdout) // decodes cleanly as exactly one JSON value
	if !strings.HasPrefix(stderr, "Sync mode: ") {
		t.Fatalf("the mode header must still reach stderr under --json: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// renderPlanDocumentTo's complete writer contract (§3.6 row 6, §23.1 item 4's
// io.Writer half): exactly one Write on success; the sole Write failing with
// (0, error); the sole Write SHORT-writing with (n < len(p), error) — after
// which exactly n bytes have been retained and NO retry is issued; and an
// encoder failure, forced through internal.PlanEncodeFault, producing ZERO
// Write calls. Neither renderer can be made to fail from data alone (every
// member of RebasePlan is a string, integer, bool or pointer thereto), which
// is precisely why that seam exists.
// ---------------------------------------------------------------------------

// countingWriter records every Write call's argument length and byte
// content, so "exactly one Write" is a real, counted assertion rather than
// an assumption.
type countingWriter struct {
	calls [][]byte
}

func (w *countingWriter) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	w.calls = append(w.calls, cp)
	return len(p), nil
}

var errPlanDocWriterFault = errors.New("injected writer fault")

type faultyWriter struct {
	calls int
}

func (w *faultyWriter) Write(p []byte) (int, error) {
	w.calls++
	return 0, errPlanDocWriterFault
}

func TestSyncPlan_RenderPlanDocumentToWritesExactlyOnce(t *testing.T) {
	plan := internal.RebasePlan{SchemaVersion: 1, Route: internal.RouteLegacy, Feature: "feature"}

	for _, jsonMode := range []bool{false, true} {
		name := "text"
		if jsonMode {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			out := &countingWriter{}
			errW := &countingWriter{}
			if err := renderPlanDocumentTo(out, errW, plan, jsonMode); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(out.calls) != 1 {
				t.Fatalf("stdout Write called %d times, want exactly 1", len(out.calls))
			}
			if len(errW.calls) != 0 {
				t.Fatalf("renderPlanDocumentTo must never write to the stderr writer, got %d calls", len(errW.calls))
			}
			var want []byte
			var err error
			if jsonMode {
				want, err = internal.MarshalRebasePlan(plan)
			} else {
				want, err = internal.FormatRebasePlan(plan)
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(out.calls[0]) != string(want) {
				t.Fatalf("written bytes differ from the renderer's own output:\n got=%q\nwant=%q", out.calls[0], want)
			}
		})
	}
}

func TestSyncPlan_RenderPlanDocumentToReturnsWriterErrorUnchanged(t *testing.T) {
	plan := internal.RebasePlan{SchemaVersion: 1, Route: internal.RouteLegacy, Feature: "feature"}

	for _, jsonMode := range []bool{false, true} {
		name := "text"
		if jsonMode {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			out := &faultyWriter{}
			errW := &countingWriter{}
			err := renderPlanDocumentTo(out, errW, plan, jsonMode)
			if !errors.Is(err, errPlanDocWriterFault) {
				t.Fatalf("err = %v, want the writer's own sentinel error unchanged", err)
			}
			if out.calls != 1 {
				t.Fatalf("expected exactly one (failing) Write attempt, got %d", out.calls)
			}
		})
	}
}

// shortWriter accepts a prefix of every buffer it is handed and then reports
// a short write, which io.Writer's contract requires to be accompanied by a
// non-nil error. It records both the retained bytes and the call count, so
// "exactly n bytes retained, no retry" is a counted assertion.
type shortWriter struct {
	accept   int
	calls    int
	retained []byte
}

var errPlanDocShortWrite = errors.New("injected short write")

func (w *shortWriter) Write(p []byte) (int, error) {
	w.calls++
	n := w.accept
	if n > len(p) {
		n = len(p)
	}
	w.retained = append(w.retained, p[:n]...)
	return n, errPlanDocShortWrite
}

// TestSyncPlan_RenderPlanDocumentToShortWriteRetainsExactlyNBytes drives the
// sole-Write short-write cell in both render modes: the writer keeps exactly
// the prefix it accepted, the error is returned unchanged, and the renderer
// never retries the remainder on any stream.
func TestSyncPlan_RenderPlanDocumentToShortWriteRetainsExactlyNBytes(t *testing.T) {
	plan := internal.RebasePlan{SchemaVersion: 1, Route: internal.RouteLegacy, Feature: "feature"}

	for _, jsonMode := range []bool{false, true} {
		name := "text"
		if jsonMode {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			var want []byte
			var err error
			if jsonMode {
				want, err = internal.MarshalRebasePlan(plan)
			} else {
				want, err = internal.FormatRebasePlan(plan)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(want) < 4 {
				t.Fatalf("fixture document is too small (%d bytes) to short-write meaningfully", len(want))
			}
			accept := len(want) / 2

			out := &shortWriter{accept: accept}
			errW := &countingWriter{}
			renderErr := renderPlanDocumentTo(out, errW, plan, jsonMode)
			if !errors.Is(renderErr, errPlanDocShortWrite) {
				t.Fatalf("err = %v, want the writer's own short-write error unchanged", renderErr)
			}
			if out.calls != 1 {
				t.Fatalf("Write called %d times, want exactly 1: a short write must never be retried", out.calls)
			}
			if len(out.retained) != accept {
				t.Fatalf("retained %d bytes, want exactly the %d the writer accepted", len(out.retained), accept)
			}
			if string(out.retained) != string(want[:accept]) {
				t.Fatalf("retained bytes are not the document's own prefix:\n got=%q\nwant=%q", out.retained, want[:accept])
			}
			if len(errW.calls) != 0 {
				t.Fatalf("a failing stdout write must never fall back to stderr, got %d calls", len(errW.calls))
			}
		})
	}
}

// TestSyncPlan_RenderPlanDocumentToEncoderFailureWritesNothing drives §3.6
// row 6: when the renderer itself fails, the error is returned and ZERO
// bytes reach either stream.
func TestSyncPlan_RenderPlanDocumentToEncoderFailureWritesNothing(t *testing.T) {
	plan := internal.RebasePlan{SchemaVersion: 1, Route: internal.RouteLegacy, Feature: "feature"}
	injected := errors.New("injected encoder fault")

	for _, jsonMode := range []bool{false, true} {
		name := "text"
		if jsonMode {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			internal.PlanEncodeFault = func() error { return injected }
			t.Cleanup(func() { internal.PlanEncodeFault = nil })

			out := &countingWriter{}
			errW := &countingWriter{}
			err := renderPlanDocumentTo(out, errW, plan, jsonMode)
			if !errors.Is(err, injected) {
				t.Fatalf("err = %v, want the renderer's own error unchanged", err)
			}
			if len(out.calls) != 0 {
				t.Fatalf("an encoding failure must write ZERO bytes, got %d Write call(s)", len(out.calls))
			}
			if len(errW.calls) != 0 {
				t.Fatalf("an encoding failure must not write to stderr either, got %d call(s)", len(errW.calls))
			}
		})
	}
}
