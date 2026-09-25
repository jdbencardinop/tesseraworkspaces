package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// T-009, T-041, T-087 — the source audits.
//
// These three cells are SOURCE audits, and this file is their sole owner. The
// argv-shape assertions elsewhere in this package are behaviour twins that
// cite their own AC directly and deliberately never claim these T ids, so the
// bidirectional mapping assertion keeps reporting exactly one owner per cell.
//
// A source audit is scoped to §15's own file ledger: it reads the feature's
// own files, never the whole tree, so an unrelated helper somewhere else can
// never make it pass or fail by accident.
// ---------------------------------------------------------------------------

// reparentOwnedSources is §15's file ledger, in both packages. Every file this
// feature owns or edits for execution purposes is listed; the documentation
// and test surfaces are deliberately excluded.
var reparentOwnedSources = []string{
	"../reparent_plan.go",
	"../reparent_plan_build.go",
	"../reparent_plan_render.go",
	"../reparent_plan_fingerprint.go",
	"../reparent_destination.go",
	"../reparent_refs.go",
	"../reparent_remote.go",
	"../reparent_state.go",
	"../reparent_exec.go",
	"stack_reparent.go",
	"reparent_observability.go",
}

// readReparentSources returns each owned file's contents, keyed by path.
func readReparentSources(t *testing.T) map[string]string {
	t.Helper()
	out := make(map[string]string, len(reparentOwnedSources))
	for _, rel := range reparentOwnedSources {
		data, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("the file ledger names %s, which must exist: %v", rel, err)
		}
		out[rel] = string(data)
	}
	return out
}

// reparentCodeLines strips whole-line comments, so an audit measures CODE
// rather than the prose that explains why a verb is forbidden.
func reparentCodeLines(src string) []string {
	var out []string
	inBlock := false
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case inBlock:
			if strings.Contains(line, "*/") {
				inBlock = false
			}
			continue
		case strings.HasPrefix(line, "//"):
			continue
		case strings.HasPrefix(line, "/*"):
			if !strings.Contains(line, "*/") {
				inBlock = true
			}
			continue
		}
		out = append(out, line)
	}
	return out
}

// TestReparentSourceAudit_NoMergeBaseOutsideTheSharedLadder is T-009. The
// reparent boundary must not grow a second ancestry ladder: `merge-base` may
// appear only inside reparentIsAncestor, the one helper §4.3c's agreement
// check and §5's cutoff ladders both consume.
func TestReparentSourceAudit_NoMergeBaseOutsideTheSharedLadder(t *testing.T) {
	_ = "asserts AC-031"
	assertReparentMatrixBehavior(t, "T-009", "merge-base-shared-ladder")
	sources := readReparentSources(t)
	for rel, src := range sources {
		for _, line := range reparentCodeLines(src) {
			if !strings.Contains(line, `"merge-base"`) {
				continue
			}
			if rel != "../reparent_destination.go" {
				t.Fatalf("%s emits merge-base outside the shared ancestry helper: %s", rel, line)
			}
		}
	}

	// And the helper itself exists, so the assertion above is not vacuous.
	if !strings.Contains(sources["../reparent_destination.go"], "func reparentIsAncestor(") {
		t.Fatal("reparentIsAncestor must be the single ancestry helper the audit protects")
	}

	// Behaviour half: a real plan emits merge-base only with --is-ancestor,
	// and never with -C.
	f := newReparentCustomerExternal(t)
	log := reparentCaptureArgv(t)
	f.Plan(f.Feature, "pr2", "--onto", "master", "--no-fetch")
	sawMergeBase := false
	for _, argv := range log.Argv {
		if argvVerb(argv) != "merge-base" {
			continue
		}
		sawMergeBase = true
		if !argvHas(argv, "--is-ancestor") {
			t.Fatalf("every emitted merge-base must be the ancestry form, got %v", argv)
		}
	}
	if !sawMergeBase {
		t.Fatal("the deterministic customer plan must exercise the required ancestry probe")
	}
}

// TestReparentSourceAudit_ForbiddenVerbsAreAbsent is T-041. §9.5's forbidden
// list is asserted BOTH in source and in the argv this boundary actually
// emits, because either half alone is escapable.
func TestReparentSourceAudit_ForbiddenVerbsAreAbsent(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-041", "forbidden-verbs-source-and-argv")
	// Source half: the literal argv tokens must not appear as emitted
	// arguments anywhere in the owned files.
	forbiddenTokens := []string{
		`"--update-refs"`,
		`"--autostash"`,
		`"--rebase-merges"`,
		`"--fork-point"`,
		`"--apply"`,
	}
	for rel, src := range readReparentSources(t) {
		if rel == "stack_reparent.go" || rel == "reparent_observability.go" {
			// Package cli emits no reparent Git child at all; the runner is
			// the only spawner and it lives in package internal.
			continue
		}
		for _, line := range reparentCodeLines(src) {
			for _, token := range forbiddenTokens {
				if strings.Contains(line, token) {
					t.Fatalf("%s emits a forbidden argv token %s: %s", rel, token, line)
				}
			}
			if strings.Contains(line, `"reset"`) && strings.Contains(line, `"--hard"`) {
				t.Fatalf("%s emits git reset --hard: %s", rel, line)
			}
			// `git replay` is checked as an EMITTED argv only: the plan
			// schema legitimately carries a `replay` JSON key describing the
			// replay set, which is a document field, not a Git verb.
			if strings.Contains(line, `"replay"`) && strings.Contains(line, "runReparentGit(") {
				t.Fatalf("%s emits git replay: %s", rel, line)
			}
		}
	}

	// Behaviour half: a real execution emits none of them either. This cites
	// AC-052 / AC-055 directly and is the same cell's own evidence, not a
	// second owner.
	f := newReparentCustomerExternal(t)
	log := reparentCaptureArgv(t)
	if _, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch"); exit != 0 {
		t.Fatalf("execution exit = %d: %s", exit, stderr)
	}
	for _, forbidden := range []string{
		"--update-refs", "--autostash", "--rebase-merges", "--fork-point", "--apply",
	} {
		if log.Contains(forbidden) {
			t.Fatalf("the executor emitted the forbidden flag %q:\n%s", forbidden, strings.Join(log.Lines(), "\n"))
		}
	}
	if log.ContainsPair("reset", "--hard") {
		t.Fatalf("the executor emitted git reset --hard:\n%s", strings.Join(log.Lines(), "\n"))
	}
	for _, argv := range log.Argv {
		if verb := argvVerb(argv); verb == "push" || verb == "replay" {
			t.Fatalf("the executor emitted a forbidden verb %q", verb)
		}
		if argvHas(argv, "-C") {
			t.Fatalf("no reparent-emitted argv may carry -C, got %v", argv)
		}
	}
}

// TestReparentSourceAudit_SoleBaseRewriter is T-087. Exactly one function in
// the whole tree may assign StackEntry.Base as part of a reparent, and it must
// live in the reparent-owned metadata writer. Any second assignment would mean
// two writers can disagree about the recorded topology.
func TestReparentSourceAudit_SoleBaseRewriter(t *testing.T) {
	_ = "asserts §10"
	assertReparentMatrixBehavior(t, "T-087", "sole-base-rewriter")
	// The pattern deliberately excludes `==`: a comparison is not a rewrite.
	assign := regexp.MustCompile(`\.Base\s*=[^=]`)
	var offenders []string
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range reparentCodeLines(string(data)) {
			if !assign.MatchString(line) {
				continue
			}
			// Only reparent-owned rewrites are in scope: the shipped stack
			// editors (tws new, rename, migrate) legitimately set Base too.
			if strings.Contains(path, "reparent") {
				offenders = append(offenders, path+": "+line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) == 0 {
		t.Fatal("the reparent metadata writer must assign StackEntry.Base")
	}
	files := map[string]bool{}
	for _, o := range offenders {
		files[strings.SplitN(o, ":", 2)[0]] = true
	}
	// Exactly two reparent-owned sites may assign Base, and they are NOT
	// interchangeable:
	//
	//   - the PURE post-image projection in reparent_plan_build.go, which
	//     computes the bytes a write WOULD produce so the plan can publish and
	//     fingerprint them, and which must never write anything;
	//   - the DURABLE writer in reparent_exec.go, which is the sole rewriter
	//     that reaches the disk.
	//
	// Any third site would mean two writers can disagree about the recorded
	// topology.
	wantProjection := "../reparent_plan_build.go"
	wantWriter := "../reparent_exec.go"
	if len(files) != 2 || !files[wantProjection] || !files[wantWriter] {
		t.Fatalf("Base may be assigned only by the pure projection and the durable writer, found:\n%s",
			strings.Join(offenders, "\n"))
	}

	writers := regexp.MustCompile(`(SaveStackAtomic|WriteStackBytesAtomic|SaveStack)\(`)
	projection, err := os.ReadFile(wantProjection)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range reparentCodeLines(string(projection)) {
		if writers.MatchString(line) {
			t.Fatalf("%s is the PURE projection and must write nothing: %s", wantProjection, line)
		}
	}
	writer, err := os.ReadFile(wantWriter)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range reparentCodeLines(string(writer)) {
		if writers.MatchString(line) {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s must be the file that durably writes the rewritten stack", wantWriter)
	}
}

// TestReparentSourceAudit_PackageCliSpawnsNoReparentGitChild pins §1.1's
// boundary: every reparent Git child goes through the single runner in package
// internal, so package cli's own reparent files contain no exec of git at all.
func TestReparentSourceAudit_PackageCliSpawnsNoReparentGitChild(t *testing.T) {
	for _, rel := range []string{"stack_reparent.go", "reparent_observability.go"} {
		data, err := os.ReadFile(rel)
		if err != nil {
			t.Fatal(err)
		}

		for _, line := range reparentCodeLines(string(data)) {
			if strings.Contains(line, "exec.Command") {
				t.Fatalf("%s must spawn no process of its own: %s", rel, line)
			}
		}
	}
}

func TestReparentSourceAudit_PushClearKeepsWorkspaceDefaultRepo(t *testing.T) {
	data, err := os.ReadFile("push.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, forbidden := range []string{"clearDir :=", "WithRepoDir(clearDir)"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("push clear must not rebind the workspace default repository through %q", forbidden)
		}
	}
	if strings.Count(source, "env.PersistClears(&") < 2 {
		t.Fatal("both external push loops must persist clears through the original default-repository envelope")
	}
}
