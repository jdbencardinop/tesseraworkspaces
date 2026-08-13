package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Shared fixtures
// ---------------------------------------------------------------------------

// ssNeutralizeGitConfig makes the injected host GIT_CONFIG_KEY_n/VALUE_n pairs
// inert for the whole test process. Production Git calls set no cmd.Env and
// inherit this process, so a builder-local append would harden only fixture
// construction and leave every production probe running under a host
// safe.bareRepository=explicit.
func ssNeutralizeGitConfig(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
}

type ssFixture struct {
	t           *testing.T
	root        string
	repo        string
	remote      string
	feature     string
	featurePath string
	ws          Workspace
}

func ssCommit(t *testing.T, dir, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, dir, "add", name)
	gitInTest(t, dir, "commit", "-m", message)
}

// ssNewExternalFixture builds a real external workspace: a repository, a real
// local bare remote, and a feature directory with a worktrees root.
func ssNewExternalFixture(t *testing.T, feature string) *ssFixture {
	t.Helper()
	ssNeutralizeGitConfig(t)
	// A canonical root keeps every path join symlink-free, so the shipped
	// worktree containment guard behaves as it does in a real workspace.
	root := canonicalize(t.TempDir())
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	gitInTest(t, root, "init", "--initial-branch=main", repo)
	ssCommit(t, repo, "README.md", "base\n", "initial")
	gitInTest(t, repo, "remote", "add", "origin", remote)
	gitInTest(t, repo, "push", "-u", "origin", "main")
	gitInTest(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	gitInTest(t, repo, "remote", "set-head", "origin", "-a")

	metadataRoot := canonicalize(repo + ".tws")
	featurePath := filepath.Join(metadataRoot, feature)
	if err := os.MkdirAll(filepath.Join(featurePath, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{
		RepoRoot:     canonicalize(repo),
		Mode:         ModeExternal,
		MetadataRoot: metadataRoot,
		StableID:     stableID(canonicalize(repo)),
		Caps:         capsFor(ModeExternal),
	}
	return &ssFixture{t: t, root: root, repo: repo, remote: remote, feature: feature, featurePath: featurePath, ws: ws}
}

// ssNewCheckoutFixture builds a real checkout workspace with one physical
// checkout and repository-local metadata.
func ssNewCheckoutFixture(t *testing.T, feature string) *ssFixture {
	t.Helper()
	ssNeutralizeGitConfig(t)
	repo, ws := setupHealthTestRepo(t)
	featurePath := ws.FeaturePath(feature)
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	return &ssFixture{t: t, root: filepath.Dir(repo), repo: repo, feature: feature, featurePath: featurePath, ws: ws}
}

func (fx *ssFixture) stack(entries ...StackEntry) Stack {
	return Stack{Branches: entries}
}

func (fx *ssFixture) worktree(name, branch string) string {
	fx.t.Helper()
	path := filepath.Join(fx.featurePath, "worktrees", name)
	gitInTest(fx.t, fx.repo, "worktree", "add", path, branch)
	return canonicalize(path)
}

func (fx *ssFixture) build(stack Stack) *StackStatusReport {
	fx.t.Helper()
	report, err := BuildStackStatus(fx.ws, Config{}, fx.feature, fx.featurePath, stack)
	if err != nil {
		fx.t.Fatalf("BuildStackStatus: %v", err)
	}
	NormalizeStackStatus(report)
	return report
}

// ssCommitOnBranch commits on a branch, using its linked worktree when Git
// already has one checked out there.
func ssCommitOnBranch(t *testing.T, repo, branch, name, content, message string) {
	t.Helper()
	if wt := ssWorktreeForBranch(t, repo, branch); wt != "" {
		ssCommit(t, wt, name, content, message)
		return
	}
	current := gitInTest(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	gitInTest(t, repo, "checkout", branch)
	ssCommit(t, repo, name, content, message)
	gitInTest(t, repo, "checkout", current)
}

func ssWorktreeForBranch(t *testing.T, repo, branch string) string {
	t.Helper()
	inv := BuildWorktreeInventory(repo)
	if !inv.Available {
		return ""
	}
	for _, rec := range inv.Records {
		if rec.BranchRef != nil && *rec.BranchRef == "refs/heads/"+branch {
			return rec.Path
		}
	}
	return ""
}

func ssEntry(t *testing.T, r *StackStatusReport, name string) StackStatusEntry {
	t.Helper()
	for _, e := range r.Entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("entry %q not found in %v", name, ssEntryNames(r))
	return StackStatusEntry{}
}

func ssEntryNames(r *StackStatusReport) []string {
	names := make([]string, 0, len(r.Entries))
	for _, e := range r.Entries {
		names = append(names, e.Name)
	}
	return names
}

func ssDecode(t *testing.T, r *StackStatusReport) map[string]any {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func ssKeys(t *testing.T, v any, path string) []string {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %T", path, v)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func ssAssertKeys(t *testing.T, v any, path string, want ...string) {
	t.Helper()
	got := ssKeys(t, v, path)
	if len(got) != len(want) {
		t.Fatalf("%s keys = %v, want %v", path, sortedCopy(got), sortedCopy(want))
	}
	wanted := map[string]bool{}
	for _, k := range want {
		wanted[k] = true
	}
	for _, k := range got {
		if !wanted[k] {
			t.Fatalf("%s has unexpected key %q (want %v)", path, k, sortedCopy(want))
		}
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func ssSourceFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// Git process recorder (AC 33, AC 36)
// ---------------------------------------------------------------------------

type ssInvocation struct {
	pwd   string
	locks string
	args  []string
}

func (i ssInvocation) key() string {
	return i.pwd + "\x00" + strings.Join(i.args, "\x00")
}

func (i ssInvocation) String() string {
	return fmt.Sprintf("git %s (pwd=%s locks=%q)", strings.Join(i.args, " "), i.pwd, i.locks)
}

type ssRecorder struct {
	t    *testing.T
	path string
}

// ssInstallGitRecorder prepends a `git` shim that records the working
// directory, GIT_OPTIONAL_LOCKS, and the full argv of every invocation before
// exec'ing the real binary. Classification is by provenance measured with
// control runs, never by argv shape: the shipped evaluator emits
// infrastructure-shaped argv of its own.
func ssInstallGitRecorder(t *testing.T) *ssRecorder {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("the test environment must provide git: %v", err)
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "record.log")
	script := fmt.Sprintf(`#!/bin/sh
{
  printf 'pwd\t%%s\n' "$(pwd -P)"
  printf 'locks\t%%s\n' "${GIT_OPTIONAL_LOCKS-}"
  for a in "$@"; do printf 'arg\t%%s\n' "$a"; done
  printf 'end\n'
} >> %q
exec %q "$@"
`, record, real)
	shim := filepath.Join(dir, "git")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, lookErr := exec.LookPath("git"); lookErr != nil || got != shim {
		t.Fatalf("git must resolve to the shim, got %q (%v)", got, lookErr)
	}
	return &ssRecorder{t: t, path: record}
}

func (r *ssRecorder) reset() {
	r.t.Helper()
	if err := os.WriteFile(r.path, nil, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *ssRecorder) invocations() []ssInvocation {
	r.t.Helper()
	data, err := os.ReadFile(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []ssInvocation
	cur := ssInvocation{}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "end":
			out = append(out, cur)
			cur = ssInvocation{}
		case strings.HasPrefix(line, "pwd\t"):
			cur.pwd = strings.TrimPrefix(line, "pwd\t")
		case strings.HasPrefix(line, "locks\t"):
			cur.locks = strings.TrimPrefix(line, "locks\t")
		case strings.HasPrefix(line, "arg\t"):
			cur.args = append(cur.args, strings.TrimPrefix(line, "arg\t"))
		}
	}
	return out
}

// ssSubtractControl removes one occurrence of every control invocation from
// the measured run and returns the surviving control subsequence plus the
// unclassified remainder.
func ssSubtractControl(t *testing.T, control, measured []ssInvocation) ([]ssInvocation, []ssInvocation) {
	t.Helper()
	remaining := map[string]int{}
	for _, c := range control {
		remaining[c.key()]++
	}
	var matched, remainder []ssInvocation
	for _, m := range measured {
		if remaining[m.key()] > 0 {
			remaining[m.key()]--
			matched = append(matched, m)
			continue
		}
		remainder = append(remainder, m)
	}
	for key, left := range remaining {
		if left > 0 {
			t.Fatalf("control invocation %q is missing %d time(s) from the measured run", key, left)
		}
	}
	if strings.Join(ssInvocationKeys(matched), "|") != strings.Join(ssInvocationKeys(control), "|") {
		t.Fatalf("the control subsequence changed order:\n control = %v\n matched = %v", control, matched)
	}
	return matched, remainder
}

var ssFullSHAPair = regexp.MustCompile(`^[0-9a-f]{40}\.\.\.[0-9a-f]{40}$`)

// ssClassifyRemainder counts the four status-added forms and fails on anything
// else, which is what forbids a future per-row branch, ref-existence, or
// upstream process.
func ssClassifyRemainder(t *testing.T, remainder []ssInvocation) (forEachRef, worktreeList, revList, dirty int) {
	t.Helper()
	for _, inv := range remainder {
		joined := strings.Join(inv.args, " ")
		switch {
		case ssHasArg(inv, "for-each-ref") && ssHasArg(inv, "--format="+stackStatusRefFormat):
			forEachRef++
		case ssHasArg(inv, "worktree") && ssHasArg(inv, "list") && ssHasArg(inv, "--porcelain"):
			worktreeList++
		case ssHasArg(inv, "rev-list") && ssHasArg(inv, "--left-right") && ssHasArg(inv, "--count") &&
			ssFullSHAPair.MatchString(inv.args[len(inv.args)-1]):
			revList++
		case ssHasArg(inv, "status") && ssHasArg(inv, "--porcelain") && inv.locks == "0":
			dirty++
		default:
			t.Fatalf("unclassified status-added invocation: git %s (locks=%q)", joined, inv.locks)
		}
	}
	return
}

func ssHasArg(inv ssInvocation, want string) bool {
	for _, a := range inv.args {
		if a == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Verbatim copies of the parent commit's helpers (AC 35, package-internal half)
// ---------------------------------------------------------------------------

// legacyGitDirty is the pre-feature gitDirty, verbatim. It deliberately runs
// without GIT_OPTIONAL_LOCKS=0, so it must never run inside the read-only
// snapshot proof.
func legacyGitDirty(repo string) bool {
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// legacyGitActiveOp is the pre-feature gitActiveOp, verbatim.
func legacyGitActiveOp(repo string) string {
	gitDir := filepath.Join(repo, ".git")
	if info, err := os.Stat(gitDir); err == nil && !info.IsDir() {
		data, readErr := os.ReadFile(gitDir)
		if readErr == nil {
			line := strings.TrimSpace(string(data))
			if after, ok := strings.CutPrefix(line, "gitdir: "); ok {
				if !filepath.IsAbs(after) {
					after = filepath.Join(repo, after)
				}
				gitDir = filepath.Clean(after)
			}
		}
	}
	checks := []struct {
		marker string
		name   string
	}{
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
		{"BISECT_LOG", "bisect"},
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(gitDir, c.marker)); err == nil {
			return c.name
		}
	}
	return ""
}

type legacyWorktreeInventory struct {
	Available bool
	ByBranch  map[string]string
	Prunable  map[string]bool
}

// legacyBuildWorktreeInventory is the pre-feature BuildWorktreeInventory,
// verbatim apart from its result type.
func legacyBuildWorktreeInventory(repoRoot string) legacyWorktreeInventory {
	inv := legacyWorktreeInventory{ByBranch: map[string]string{}, Prunable: map[string]bool{}}
	if repoRoot == "" {
		return inv
	}
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return inv
	}
	inv.Available = true

	var curPath, curBranch string
	var curPrunable bool
	flush := func() {
		if curBranch != "" {
			if curPrunable {
				inv.Prunable[curBranch] = true
			} else {
				inv.ByBranch[curBranch] = curPath
			}
		}
		curPath, curBranch, curPrunable = "", "", false
	}
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			curPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			curBranch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			curPrunable = true
		}
	}
	flush()
	return inv
}

// ssInstallPorcelainShim replaces only `worktree list --porcelain` with a
// fabricated payload and execs the real Git for every other invocation, so a
// malformed-porcelain fixture can be observed end to end without faking any
// other Git behaviour.
func ssInstallPorcelainShim(t *testing.T, payload string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("the test environment must provide git: %v", err)
	}
	dir := t.TempDir()
	payloadFile := filepath.Join(dir, "porcelain.txt")
	if err := os.WriteFile(payloadFile, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
for a in "$@"; do
  if [ "$a" = "--porcelain" ]; then
    for b in "$@"; do
      if [ "$b" = "worktree" ]; then
        cat %q
        exit 0
      fi
    done
  fi
done
exec %q "$@"
`, payloadFile, real)
	shim := filepath.Join(dir, "git")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shim, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, lookErr := exec.LookPath("git"); lookErr != nil || got != shim {
		t.Fatalf("git must resolve to the shim, got %q (%v)", got, lookErr)
	}
}

// ---------------------------------------------------------------------------
// Stack loading and structural validation (spec §5, §5.1)
// ---------------------------------------------------------------------------

func TestStackStatus_ValidateStackForStatus(t *testing.T) {
	cases := []struct {
		name    string
		stack   Stack
		wantErr string
	}{
		{name: "empty branches is valid", stack: Stack{}},
		{name: "duplicate git branch is valid", stack: Stack{Branches: []StackEntry{
			{Name: "a", Branch: "shared", Base: "main"},
			{Name: "b", Branch: "shared", Base: "main"},
		}}},
		{name: "cycle is valid", stack: Stack{Branches: []StackEntry{
			{Name: "a", Base: "b"}, {Name: "b", Base: "a"},
		}}},
		{name: "empty name", stack: Stack{Branches: []StackEntry{{Name: "a", Base: "main"}, {Base: "main"}}},
			wantErr: "entry 1: empty name"},
		{name: "duplicate name", stack: Stack{Branches: []StackEntry{{Name: "a", Base: "main"}, {Name: "a", Base: "main"}}},
			wantErr: "duplicate entry name a"},
		{name: "unsafe name", stack: Stack{Branches: []StackEntry{{Name: "../evil", Base: "main"}}},
			wantErr: "unsafe entry name ../evil"},
		{name: "dot name", stack: Stack{Branches: []StackEntry{{Name: ".", Base: "main"}}},
			wantErr: "unsafe entry name ."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStackForStatus(tc.stack)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestStackStatus_LoadStackClassification(t *testing.T) {
	feature := "auth"

	t.Run("missing", func(t *testing.T) {
		dir := t.TempDir()
		_, err := LoadStackForStatus(dir, feature)
		if err == nil || err.Error() != "no stack.yaml found for feature: auth" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can read a 0000 file")
		}
		dir := t.TempDir()
		path := StackPath(dir)
		if err := os.WriteFile(path, []byte("branches: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
		_, err := LoadStackForStatus(dir, feature)
		if err == nil || !strings.HasPrefix(err.Error(), "stack.yaml unreadable for feature auth: ") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(StackPath(dir), []byte("branches: [\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadStackForStatus(dir, feature)
		if err == nil || !strings.HasPrefix(err.Error(), "stack.yaml invalid for feature auth: ") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("structurally invalid", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(StackPath(dir), []byte("branches:\n  - name: a\n    base: main\n  - name: a\n    base: main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadStackForStatus(dir, feature)
		if err == nil || err.Error() != "invalid stack.yaml for feature auth: duplicate entry name a" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("valid empty", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(StackPath(dir), []byte("branches: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stack, err := LoadStackForStatus(dir, feature)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(stack.Branches) != 0 {
			t.Fatalf("stack = %+v", stack)
		}
	})
}

// ---------------------------------------------------------------------------
// Schema (AC 11, 12, 13, 14, 14a, 15, 16)
// ---------------------------------------------------------------------------

func TestStackStatus_NoStackStateKey(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	gitInTest(t, fx.repo, "branch", "api", "main")
	fx.worktree("api", "api")
	r := fx.build(fx.stack(StackEntry{Name: "api", Base: "main"}))

	doc := ssDecode(t, r)
	ssAssertKeys(t, doc, "$", "schema_version", "workspace", "feature", "entries", "summary")
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "stack_state") {
		t.Fatalf("document must carry no stack_state key: %s", raw)
	}
	if strings.Contains(string(raw), "generated_at") {
		t.Fatalf("document must carry no generated timestamp: %s", raw)
	}
	if doc["schema_version"].(float64) != 1 {
		t.Fatalf("schema_version = %v", doc["schema_version"])
	}
}

func TestStackStatus_EmptyStack(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	r := fx.build(Stack{})

	if r.Summary.Entries != 0 {
		t.Fatalf("summary.entries = %d", r.Summary.Entries)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Fatalf("entries must encode as [] and never null: %s", raw)
	}
	human := FormatStackStatus(r)
	if !strings.Contains(human, "No branches tracked in stack.yaml.") {
		t.Fatalf("human output = %s", human)
	}
	if strings.Contains(human, "BRANCH") {
		t.Fatalf("an empty stack prints no table: %s", human)
	}
}

func ssAssertEntryKeys(t *testing.T, entry any) {
	t.Helper()
	ssAssertKeys(t, entry, "entry",
		"name", "git_branch", "archived", "repo", "base", "ref_exists", "heads",
		"base_record", "ancestry", "repo_source", "parent_counts", "materialization",
		"is_current_checkout", "upstream")
	m := entry.(map[string]any)
	ssAssertKeys(t, m["base"], "entry.base", "name", "kind", "ref")
	ssAssertKeys(t, m["heads"], "entry.heads",
		"local", "local_short", "parent", "parent_short", "merge_base", "merge_base_short")
	ssAssertKeys(t, m["base_record"], "entry.base_record", "sha", "commit", "short", "state")
	ssAssertKeys(t, m["ancestry"], "entry.ancestry", "status", "reason", "severity", "guidance", "notes")
	ssAssertKeys(t, m["parent_counts"], "entry.parent_counts", "ahead", "behind")
	ssAssertKeys(t, m["materialization"], "entry.materialization",
		"kind", "state", "path", "checked_out_branch", "detached", "dirty", "active_git_op")
	ssAssertKeys(t, m["upstream"], "entry.upstream",
		"configured", "ref", "display", "state", "ahead", "behind", "local_only")
}

func ssAssertReportKeys(t *testing.T, doc map[string]any, mode WorkspaceMode) {
	t.Helper()
	ssAssertKeys(t, doc, "$", "schema_version", "workspace", "feature", "entries", "summary")
	ws := doc["workspace"].(map[string]any)
	ssAssertKeys(t, ws, "$.workspace", "mode", "stable_id", "metadata_root", "repository", "external", "checkout")
	ssAssertKeys(t, ws["repository"], "$.workspace.repository", "dir", "source", "alternate")
	if mode == ModeExternal {
		if ws["checkout"] != nil {
			t.Fatalf("external mode must carry a null checkout block: %v", ws["checkout"])
		}
		ssAssertKeys(t, ws["external"], "$.workspace.external", "worktrees_root")
	} else {
		if ws["external"] != nil {
			t.Fatalf("checkout mode must carry a null external block: %v", ws["external"])
		}
		ssAssertKeys(t, ws["checkout"], "$.workspace.checkout", "path", "branch", "detached", "dirty", "active_git_op")
	}
	summary := doc["summary"].(map[string]any)
	ssAssertKeys(t, summary, "$.summary", "entries", "ancestry", "materialization", "upstream", "unknown", "local_only")
	ssAssertKeys(t, summary["ancestry"], "$.summary.ancestry",
		"current", "stale", "divergent", "missing", "cross_repo_unsupported", "unevaluated")
	ssAssertKeys(t, summary["materialization"], "$.summary.materialization",
		"present", "archived", "missing", "prunable_missing", "cross_repo_unsupported", "unknown")
	ssAssertKeys(t, summary["upstream"], "$.summary.upstream",
		"none", "equal", "ahead", "behind", "diverged", "gone", "unknown")
	ssAssertKeys(t, summary["unknown"], "$.summary.unknown", "ref_exists", "parent_counts", "dirty", "active_git_op")
	for _, e := range doc["entries"].([]any) {
		ssAssertEntryKeys(t, e)
	}
}

func TestStackStatus_KeySet(t *testing.T) {
	if strings.Contains(ssSourceFile(t, "stack_status.go"), "omitempty") {
		t.Fatal("internal/stack_status.go must contain no omitempty tag")
	}

	t.Run("external populated", func(t *testing.T) {
		fx := ssNewExternalFixture(t, "auth")
		gitInTest(t, fx.repo, "branch", "api", "main")
		fx.worktree("api", "api")
		r := fx.build(fx.stack(
			StackEntry{Name: "api", Base: "main"},
			StackEntry{Name: "gone", Base: "main"},
		))
		ssAssertReportKeys(t, ssDecode(t, r), ModeExternal)
	})

	t.Run("external empty", func(t *testing.T) {
		fx := ssNewExternalFixture(t, "auth")
		ssAssertReportKeys(t, ssDecode(t, fx.build(Stack{})), ModeExternal)
	})

	t.Run("checkout populated", func(t *testing.T) {
		fx := ssNewCheckoutFixture(t, "auth")
		gitInTest(t, fx.repo, "branch", "api", "main")
		r := fx.build(fx.stack(StackEntry{Name: "api", Base: "main"}))
		ssAssertReportKeys(t, ssDecode(t, r), ModeCheckout)
	})

	t.Run("checkout empty", func(t *testing.T) {
		fx := ssNewCheckoutFixture(t, "auth")
		ssAssertReportKeys(t, ssDecode(t, fx.build(Stack{})), ModeCheckout)
	})
}

func TestStackStatus_NullsNotZeros(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	other := filepath.Join(fx.root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, fx.root, "init", "--initial-branch=main", other)
	ssCommit(t, other, "README.md", "other\n", "other initial")

	r := fx.build(fx.stack(
		StackEntry{Name: "baseless"},
		StackEntry{Name: "wiki", Base: "main", Repo: other},
		StackEntry{Name: "missingref", Base: "main"},
	))
	doc := ssDecode(t, r)
	entries := doc["entries"].([]any)

	for _, name := range []string{"baseless", "wiki"} {
		e := ssEntry(t, r, name)
		if e.RefExists != nil {
			t.Fatalf("%s ref_exists = %v, want null", name, *e.RefExists)
		}
		if e.Heads.Local != nil || e.Heads.Parent != nil || e.Heads.MergeBase != nil {
			t.Fatalf("%s heads = %+v, want nulls", name, e.Heads)
		}
		if e.BaseRecord.State != nil {
			t.Fatalf("%s base_record.state = %v, want null", name, *e.BaseRecord.State)
		}
		if e.ParentCounts.Ahead != nil || e.ParentCounts.Behind != nil {
			t.Fatalf("%s parent_counts = %+v, want nulls", name, e.ParentCounts)
		}
	}

	missing := ssEntry(t, r, "missingref")
	if missing.RefExists == nil || *missing.RefExists {
		t.Fatalf("a probed missing ref reports false, got %v", missing.RefExists)
	}
	if missing.ParentCounts.Ahead != nil {
		t.Fatalf("a missing child starts no count process")
	}

	// Every nulled scalar must decode as JSON null, not "" / 0 / false.
	for i, raw := range entries {
		m := raw.(map[string]any)
		if m["name"] == "wiki" {
			up := m["upstream"].(map[string]any)
			for _, key := range []string{"configured", "ref", "display", "state", "ahead", "behind"} {
				if up[key] != nil {
					t.Fatalf("cross-repo upstream.%s = %v, want null", key, up[key])
				}
			}
			if up["local_only"] != true {
				t.Fatalf("cross-repo upstream.local_only = %v", up["local_only"])
			}
		}
		if m["ancestry"].(map[string]any)["notes"] == nil {
			t.Fatalf("entry %d notes must be [] and never null", i)
		}
	}
}

func TestStackStatus_MetadataRootAsHeld(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	gitInTest(t, fx.repo, "branch", "api", "main")
	fx.worktree("api", "api")
	stack := fx.stack(StackEntry{Name: "api", Base: "main"})
	canonical := fx.build(stack)

	// A configured workspace root is stored verbatim and may legitimately be
	// non-canonical; the report must not re-canonicalize it.
	link := filepath.Join(fx.root, "meta-link")
	if err := os.Symlink(fx.ws.MetadataRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	held := fx.ws
	held.MetadataRoot = link
	report, err := BuildStackStatus(held, Config{}, fx.feature, filepath.Join(link, fx.feature), stack)
	if err != nil {
		t.Fatal(err)
	}
	NormalizeStackStatus(report)

	if report.Workspace.MetadataRoot != link {
		t.Fatalf("metadata_root = %q, want the held value %q", report.Workspace.MetadataRoot, link)
	}
	if got := report.Workspace.External.WorktreesRoot; got != canonicalize(got) {
		t.Fatalf("worktrees_root %q must still be canonical", got)
	}
	gotState := ssEntry(t, report, "api").Materialization.State
	wantState := ssEntry(t, canonical, "api").Materialization.State
	if gotState != wantState {
		t.Fatalf("materialization = %q, want the canonical twin's %q", gotState, wantState)
	}
}

func TestStackStatus_Deterministic(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	gitInTest(t, fx.repo, "branch", "api", "main")
	fx.worktree("api", "api")
	stack := fx.stack(StackEntry{Name: "api", Base: "main"}, StackEntry{Name: "gone", Base: "main"})

	firstJSON, err := json.MarshalIndent(fx.build(stack), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.MarshalIndent(fx.build(stack), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("json output is not byte-identical:\n%s\n---\n%s", firstJSON, secondJSON)
	}
	firstHuman := FormatStackStatus(fx.build(stack))
	secondHuman := FormatStackStatus(fx.build(stack))
	if firstHuman != secondHuman {
		t.Fatalf("human output is not byte-identical across runs:\n%s\n---\n%s", firstHuman, secondHuman)
	}
	if strings.Contains(string(firstJSON), "generated") {
		t.Fatalf("no generated_at-like key may exist: %s", firstJSON)
	}
}

func TestStackStatus_RowOrder(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	gitInTest(t, fx.repo, "branch", "api", "main")
	// YAML order differs from topological order, contains a cycle, and holds
	// two entries that share one Git branch.
	stack := fx.stack(
		StackEntry{Name: "child", Base: "parent"},
		StackEntry{Name: "cycle-a", Base: "cycle-b"},
		StackEntry{Name: "cycle-b", Base: "cycle-a"},
		StackEntry{Name: "parent", Base: "main"},
		StackEntry{Name: "api", Base: "main"},
		StackEntry{Name: "api-mirror", Branch: "api", Base: "main"},
	)
	r := fx.build(stack)

	want := []string{"child", "cycle-a", "cycle-b", "parent", "api", "api-mirror"}
	if strings.Join(ssEntryNames(r), ",") != strings.Join(want, ",") {
		t.Fatalf("row order = %v, want %v", ssEntryNames(r), want)
	}
	a := ssEntry(t, r, "api")
	b := ssEntry(t, r, "api-mirror")
	if a.Name == b.Name || a.GitBranch != b.GitBranch {
		t.Fatalf("duplicate rows must stay distinct by name and share a branch: %+v / %+v", a, b)
	}
	aj, err := json.Marshal(a.Upstream)
	if err != nil {
		t.Fatal(err)
	}
	bj, err := json.Marshal(b.Upstream)
	if err != nil {
		t.Fatal(err)
	}
	if string(aj) != string(bj) {
		t.Fatalf("duplicate rows must share identical upstream facts: %s vs %s", aj, bj)
	}
	if strings.Contains(FormatStackStatus(r), "cycle detected") {
		t.Fatal("the status report never warns about cycles")
	}
}

// ---------------------------------------------------------------------------
// Ancestry projection (AC 17, 18, 19, 20)
// ---------------------------------------------------------------------------

func TestStackStatus_AncestryFieldForField(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	other := filepath.Join(fx.root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, fx.root, "init", "--initial-branch=main", other)
	ssCommit(t, other, "README.md", "other\n", "other initial")

	gitInTest(t, fx.repo, "branch", "models", "main")
	gitInTest(t, fx.repo, "branch", "api", "models")
	fx.worktree("models", "models")
	fx.worktree("api", "api")
	ssCommitOnBranch(t, fx.repo, "models", "models.txt", "models\n", "models change")

	stack := fx.stack(
		StackEntry{Name: "models", Base: "main"},
		StackEntry{Name: "api", Base: "models"},
		StackEntry{Name: "baseless"},
		StackEntry{Name: "wiki", Base: "main", Repo: other},
		StackEntry{Name: "missingref", Base: "main"},
	)
	r := fx.build(stack)
	edges, _ := FeatureStackEdges(fx.ws, Config{}, fx.feature, fx.featurePath, stack)
	if len(edges) != len(r.Entries) {
		t.Fatalf("edge count %d != row count %d", len(edges), len(r.Entries))
	}

	for i, edge := range edges {
		row := r.Entries[i]
		if row.Name != edge.Name || row.GitBranch != edge.GitBranch || row.Archived != edge.Archived {
			t.Fatalf("row %d identity = %+v, edge = %+v", i, row, edge)
		}
		if stackStatusDeref(row.Repo) != edge.Repo {
			t.Fatalf("row %d repo = %v, edge %q", i, row.Repo, edge.Repo)
		}
		if stackStatusDeref(row.Base.Name) != edge.BaseName || row.Base.Kind != edge.BaseKind ||
			stackStatusDeref(row.Base.Ref) != edge.BaseRef {
			t.Fatalf("row %d base = %+v, edge = %+v", i, row.Base, edge)
		}
		if stackStatusDeref(row.Heads.Local) != edge.LocalHead ||
			stackStatusDeref(row.Heads.LocalShort) != edge.LocalHeadShort ||
			stackStatusDeref(row.Heads.Parent) != edge.ParentHead ||
			stackStatusDeref(row.Heads.ParentShort) != edge.ParentHeadShort ||
			stackStatusDeref(row.Heads.MergeBaseShort) != edge.MergeBaseShort {
			t.Fatalf("row %d heads = %+v, edge = %+v", i, row.Heads, edge)
		}
		if (row.Heads.MergeBase == nil) != (edge.MergeBase == nil) {
			t.Fatalf("row %d merge_base nullability differs from the edge", i)
		}
		if edge.MergeBase != nil && *row.Heads.MergeBase != *edge.MergeBase {
			t.Fatalf("row %d merge_base = %q, edge %q", i, *row.Heads.MergeBase, *edge.MergeBase)
		}
		if row.Heads.MergeBase != nil && row.Heads.MergeBase == edge.MergeBase {
			t.Fatalf("row %d must copy the merge base value, not alias the edge pointer", i)
		}
		if stackStatusDeref(row.BaseRecord.SHA) != edge.LastBaseSHA ||
			stackStatusDeref(row.BaseRecord.Commit) != edge.LastBaseCommit ||
			stackStatusDeref(row.BaseRecord.Short) != edge.LastBaseShort ||
			stackStatusDeref(row.BaseRecord.State) != string(edge.BaseRecord) {
			t.Fatalf("row %d base_record = %+v, edge = %+v", i, row.BaseRecord, edge)
		}
		if stackStatusDeref(row.Ancestry.Status) != string(edge.Status) ||
			row.Ancestry.Reason != edge.Reason || row.Ancestry.Severity != edge.Severity ||
			stackStatusDeref(row.Ancestry.Guidance) != edge.Guidance {
			t.Fatalf("row %d ancestry = %+v, edge = %+v", i, row.Ancestry, edge)
		}
		if len(row.Ancestry.Notes) != len(edge.Notes) {
			t.Fatalf("row %d notes = %+v, edge = %+v", i, row.Ancestry.Notes, edge.Notes)
		}
		for n := range edge.Notes {
			if row.Ancestry.Notes[n].Kind != edge.Notes[n].Kind || row.Ancestry.Notes[n].Detail != edge.Notes[n].Detail {
				t.Fatalf("row %d note %d = %+v, edge = %+v", i, n, row.Ancestry.Notes[n], edge.Notes[n])
			}
		}
		if row.RepoSource != edge.RepoSource {
			t.Fatalf("row %d repo_source = %q, edge %q", i, row.RepoSource, edge.RepoSource)
		}
		if row.Ancestry.Severity == SeverityError {
			t.Fatalf("row %d must never carry error severity", i)
		}
	}

	if got := ssEntry(t, r, "api").Ancestry.Status; got == nil || *got != string(AncestryStatusStale) {
		t.Fatalf("api ancestry = %v, want stale", got)
	}
}

func TestStackStatus_RefExistsFollowsRefProbed(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	other := filepath.Join(fx.root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, fx.root, "init", "--initial-branch=main", other)
	ssCommit(t, other, "README.md", "other\n", "other initial")

	gitInTest(t, fx.repo, "branch", "baseless", "main")
	gitInTest(t, fx.repo, "branch", "api", "main")
	fx.worktree("api", "api")

	stack := fx.stack(
		StackEntry{Name: "api", Base: "main"},
		StackEntry{Name: "baseless"},
		StackEntry{Name: "wiki", Base: "main", Repo: other},
	)
	r := fx.build(stack)
	edges, _ := FeatureStackEdges(fx.ws, Config{}, fx.feature, fx.featurePath, stack)

	for i, edge := range edges {
		row := r.Entries[i]
		if !edge.RefProbed {
			if row.RefExists != nil {
				t.Fatalf("row %d is unprobed and must report null ref_exists", i)
			}
			continue
		}
		if row.RefExists == nil || *row.RefExists != edge.RefExists {
			t.Fatalf("row %d ref_exists = %v, edge %v", i, row.RefExists, edge.RefExists)
		}
	}

	// A base-unset row is deliberately unprobed even though the branch-ref
	// inventory holds a record for its branch: inventory evidence supplies
	// upstream configuration only.
	inv := BuildBranchRefInventory(fx.ws.RepoRoot)
	if !inv.Available {
		t.Fatalf("inventory must be available: %v", inv.Err)
	}
	if _, ok := inv.ByRef["refs/heads/baseless"]; !ok {
		t.Fatalf("the fixture must hold a branch-ref record for baseless: %+v", inv.ByRef)
	}
	baseless := ssEntry(t, r, "baseless")
	if baseless.RefExists != nil || baseless.Heads.Local != nil || baseless.BaseRecord.State != nil ||
		baseless.ParentCounts.Ahead != nil || baseless.ParentCounts.Behind != nil {
		t.Fatalf("base-unset row must stay unevaluated: %+v", baseless)
	}
	if baseless.Upstream.Configured == nil {
		t.Fatal("a base-unset row still receives upstream configuration")
	}
}

func TestStackStatus_EdgeSliceTotality(t *testing.T) {
	stack := Stack{Branches: []StackEntry{
		{Name: "a", Base: "main"},
		{Name: "b", Base: "a"},
		{Name: "c", Base: "b"},
	}}
	short := []StackEdge{{Feature: "auth", Name: "a", GitBranch: "a", Status: AncestryStatusCurrent}}

	edges := ancestryEdgesFor("auth", stack, short)
	if len(edges) != len(stack.Branches) {
		t.Fatalf("edges = %d, want %d", len(edges), len(stack.Branches))
	}
	for i, edge := range edges {
		row := stackStatusProjectEdge(stack.Branches[i], edge)
		if row.Ancestry.Status != nil {
			t.Fatalf("row %d must be unevaluated, got %q", i, *row.Ancestry.Status)
		}
		if row.RefExists != nil {
			t.Fatalf("row %d must not claim ref existence", i)
		}
		if ancestryDisplayStatus(AncestryStatus(stackStatusDeref(row.Ancestry.Status))) != "unevaluated" {
			t.Fatalf("row %d must render unevaluated", i)
		}
	}
}

func TestStackStatus_UnrelatedHistories(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	gitInTest(t, fx.repo, "checkout", "--orphan", "orphan")
	ssCommit(t, fx.repo, "orphan.txt", "orphan\n", "orphan root")
	ssCommit(t, fx.repo, "orphan2.txt", "orphan2\n", "orphan second")
	gitInTest(t, fx.repo, "checkout", "main")
	ssCommit(t, fx.repo, "main.txt", "main\n", "main second")

	r := fx.build(fx.stack(StackEntry{Name: "orphan", Base: "main"}))
	row := ssEntry(t, r, "orphan")

	if row.Ancestry.Status == nil || *row.Ancestry.Status != string(AncestryStatusDivergent) {
		t.Fatalf("ancestry = %v, want divergent", row.Ancestry.Status)
	}
	if row.Ancestry.Reason != ReasonUnrelatedHistories {
		t.Fatalf("reason = %q", row.Ancestry.Reason)
	}
	if row.Heads.MergeBase != nil {
		t.Fatalf("merge_base = %v, want null", *row.Heads.MergeBase)
	}
	if row.ParentCounts.Ahead == nil || row.ParentCounts.Behind == nil {
		t.Fatalf("unrelated histories still count both sides: %+v", row.ParentCounts)
	}
	if *row.ParentCounts.Ahead == 0 || *row.ParentCounts.Behind == 0 {
		t.Fatalf("parent counts = %d/%d, want two real non-zero totals",
			*row.ParentCounts.Ahead, *row.ParentCounts.Behind)
	}
}

// ---------------------------------------------------------------------------
// Branch-ref inventory (AC 21, 22, 23, 23a, 24, 25)
// ---------------------------------------------------------------------------

// ssUpstreamFixture builds every real upstream state against a real local bare
// remote, without any fetch.
func ssUpstreamFixture(t *testing.T) *ssFixture {
	t.Helper()
	fx := ssNewExternalFixture(t, "auth")
	repo := fx.repo

	gitInTest(t, repo, "branch", "equalbr", "main")
	gitInTest(t, repo, "push", "-u", "origin", "equalbr")

	gitInTest(t, repo, "branch", "ahead", "main")
	gitInTest(t, repo, "push", "-u", "origin", "ahead")
	ssCommitOnBranch(t, repo, "ahead", "ahead.txt", "ahead\n", "ahead change")

	gitInTest(t, repo, "branch", "behindb", "main")
	gitInTest(t, repo, "push", "-u", "origin", "behindb")
	gitInTest(t, repo, "push", "origin", "ahead:refs/heads/behindb")

	gitInTest(t, repo, "branch", "div", "main")
	gitInTest(t, repo, "push", "-u", "origin", "div")
	ssCommitOnBranch(t, repo, "div", "div.txt", "div\n", "div change")
	gitInTest(t, repo, "push", "--force", "origin", "ahead:refs/heads/div")

	gitInTest(t, repo, "branch", "gonebr", "main")
	gitInTest(t, repo, "push", "-u", "origin", "gonebr")
	gitInTest(t, repo, "push", "origin", "--delete", "gonebr")

	gitInTest(t, repo, "branch", "noups", "main")

	gitInTest(t, repo, "branch", "badremote", "main")
	gitInTest(t, repo, "config", "branch.badremote.remote", "nosuchremote")
	gitInTest(t, repo, "config", "branch.badremote.merge", "refs/heads/badremote")

	gitInTest(t, repo, "branch", "localups", "main")
	gitInTest(t, repo, "config", "branch.localups.remote", ".")
	gitInTest(t, repo, "config", "branch.localups.merge", "refs/heads/main")
	ssCommitOnBranch(t, repo, "localups", "localups.txt", "localups\n", "localups change")

	return fx
}

func TestStackStatus_UpstreamStatesRealGit(t *testing.T) {
	fx := ssUpstreamFixture(t)
	names := []string{"equalbr", "ahead", "behindb", "div", "gonebr", "noups", "badremote", "localups"}
	entries := make([]StackEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, StackEntry{Name: name, Base: "main"})
	}
	r := fx.build(Stack{Branches: entries})

	type want struct {
		configured bool
		state      string
		display    string
		ahead      *int
		behind     *int
	}
	cases := map[string]want{
		"equalbr":   {configured: true, state: StackStatusUpstreamEqual, display: "origin/equalbr", ahead: intPtr(0), behind: intPtr(0)},
		"ahead":     {configured: true, state: StackStatusUpstreamAhead, display: "origin/ahead", ahead: intPtr(1), behind: intPtr(0)},
		"behindb":   {configured: true, state: StackStatusUpstreamBehind, display: "origin/behindb", ahead: intPtr(0), behind: intPtr(1)},
		"div":       {configured: true, state: StackStatusUpstreamDiverged, display: "origin/div", ahead: intPtr(1), behind: intPtr(1)},
		"gonebr":    {configured: true, state: StackStatusUpstreamGone, display: "origin/gonebr"},
		"noups":     {configured: false, state: StackStatusUpstreamNone},
		"badremote": {configured: false, state: StackStatusUpstreamNone},
		"localups":  {configured: true, state: StackStatusUpstreamAhead, display: "main", ahead: intPtr(1), behind: intPtr(0)},
	}

	for name, w := range cases {
		row := ssEntry(t, r, name)
		up := row.Upstream
		if !up.LocalOnly {
			t.Fatalf("%s: local_only must be true on every successful row", name)
		}
		if up.Configured == nil || *up.Configured != w.configured {
			t.Fatalf("%s: configured = %v, want %v", name, up.Configured, w.configured)
		}
		if up.State == nil || *up.State != w.state {
			t.Fatalf("%s: state = %v, want %q", name, up.State, w.state)
		}
		if w.display == "" {
			if up.Display != nil || up.Ref != nil {
				t.Fatalf("%s: display/ref must be null, got %v/%v", name, up.Display, up.Ref)
			}
		} else if up.Display == nil || *up.Display != w.display {
			t.Fatalf("%s: display = %v, want %q", name, up.Display, w.display)
		}
		if (w.ahead == nil) != (up.Ahead == nil) || (w.behind == nil) != (up.Behind == nil) {
			t.Fatalf("%s: counts = %v/%v, want %v/%v", name, up.Ahead, up.Behind, w.ahead, w.behind)
		}
		if w.ahead != nil && (*up.Ahead != *w.ahead || *up.Behind != *w.behind) {
			t.Fatalf("%s: counts = %d/%d, want %d/%d", name, *up.Ahead, *up.Behind, *w.ahead, *w.behind)
		}
	}

	// none, equal, and gone are three distinct states and are never conflated.
	if r.Summary.Upstream.None != 2 || r.Summary.Upstream.Equal != 1 || r.Summary.Upstream.Gone != 1 {
		t.Fatalf("summary upstream = %+v", r.Summary.Upstream)
	}
	if got := ssEntry(t, r, "localups").Upstream.Ref; got == nil || *got != "refs/heads/main" {
		t.Fatalf("a local-branch upstream keeps its full ref, got %v", got)
	}
}

func TestStackStatus_RefInventoryRawFormat(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	rec := ssInstallGitRecorder(t)
	rec.reset()
	inv := BuildBranchRefInventory(fx.ws.RepoRoot)
	if !inv.Available {
		t.Fatalf("inventory must be available: %v", inv.Err)
	}

	invocations := rec.invocations()
	if len(invocations) != 1 {
		t.Fatalf("the inventory must be one process, got %v", invocations)
	}
	args := strings.Join(invocations[0].args, " ")
	if !strings.Contains(args, "--format="+stackStatusRefFormat) {
		t.Fatalf("argv = %s", args)
	}
	for _, forbidden := range []string{"|", "%(refname:short)", "%(upstream:short)"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("argv must not contain %q: %s", forbidden, args)
		}
	}
	if !strings.Contains(args, "refs/heads/") {
		t.Fatalf("argv must scope the scan to refs/heads/: %s", args)
	}

	raw := []byte("refs/heads/a\x00" + strings.Repeat("a", 40) + "\x00\x00\x00\n" +
		"refs/heads/b\x00" + strings.Repeat("b", 40) + "\x00refs/remotes/origin/b\x00\x00=\n")
	if !strings.Contains(string(raw), "\x00") || !strings.Contains(string(raw), "\n") {
		t.Fatal("the fixture bytes must carry NUL fields and newline record separators")
	}
	records, err := parseBranchRefInventory(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
}

func TestStackStatus_RefInventoryFailClosed(t *testing.T) {
	sha := strings.Repeat("a", 40)
	cases := []struct {
		name string
		raw  string
	}{
		{"wrong field count", "refs/heads/a\x00" + sha + "\x00\x00\n"},
		{"ref without refs/heads prefix", "refs/tags/a\x00" + sha + "\x00\x00\x00\n"},
		{"empty branch remainder", "refs/heads/\x00" + sha + "\x00\x00\x00\n"},
		{"empty object id", "refs/heads/a\x00\x00\x00\x00\n"},
		{"non-hex object id", "refs/heads/a\x00zzzz\x00\x00\x00\n"},
		{"uppercase object id", "refs/heads/a\x00" + strings.ToUpper(sha) + "\x00\x00\x00\n"},
		{"upstream without refs prefix", "refs/heads/a\x00" + sha + "\x00origin/a\x00\x00=\n"},
		{"unaccepted pair equal with track", "refs/heads/a\x00" + sha + "\x00refs/remotes/origin/a\x00[ahead 1]\x00=\n"},
		{"unaccepted pair gone with marker", "refs/heads/a\x00" + sha + "\x00refs/remotes/origin/a\x00[gone]\x00>\n"},
		{"unaccepted pair unknown marker", "refs/heads/a\x00" + sha + "\x00refs/remotes/origin/a\x00[ahead 1]\x00!\n"},
		{"tracking without upstream", "refs/heads/a\x00" + sha + "\x00\x00[ahead 1]\x00>\n"},
		{"non numeric ahead", "refs/heads/a\x00" + sha + "\x00refs/remotes/origin/a\x00[ahead x]\x00>\n"},
		{"negative ahead", "refs/heads/a\x00" + sha + "\x00refs/remotes/origin/a\x00[ahead -1]\x00>\n"},
		{"malformed diverged", "refs/heads/a\x00" + sha + "\x00refs/remotes/origin/a\x00[ahead 1 behind 2]\x00<>\n"},
		{"interior empty record", "refs/heads/a\x00" + sha + "\x00\x00\x00\n\nrefs/heads/b\x00" + sha + "\x00\x00\x00\n"},
		{"duplicate key", "refs/heads/a\x00" + sha + "\x00\x00\x00\nrefs/heads/a\x00" + sha + "\x00\x00\x00\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseBranchRefInventory([]byte(tc.raw)); err == nil {
				t.Fatal("the whole inventory must be invalidated")
			}
		})
	}

	// A failed inventory nulls every upstream fact and leaves ancestry intact.
	fx := ssNewExternalFixture(t, "auth")
	gitInTest(t, fx.repo, "branch", "api", "main")
	stack := fx.stack(StackEntry{Name: "api", Base: "main"})
	good := fx.build(stack)

	inv := BranchRefInventory{ByRef: map[string]BranchRefRecord{}, Err: errors.New("invalid")}
	up := stackStatusUpstreamFor(stack.Branches[0], StackEdge{ChildRef: "refs/heads/api"}, inv)
	if up.Configured != nil || up.State != nil || up.Ahead != nil || up.Behind != nil || up.Ref != nil || up.Display != nil {
		t.Fatalf("an unavailable inventory nulls every upstream fact: %+v", up)
	}
	if !up.LocalOnly {
		t.Fatal("local_only stays true")
	}
	if got := ssEntry(t, good, "api").Ancestry.Status; got == nil {
		t.Fatal("ancestry must be unaffected by the inventory")
	}
}

func TestStackStatus_InventoryObjectFormatNeutral(t *testing.T) {
	sha1 := strings.Repeat("a", 40)
	sha256 := strings.Repeat("b", 64)

	t.Run("branch ref parser", func(t *testing.T) {
		for _, id := range []string{sha1, sha256} {
			records, err := parseBranchRefInventory([]byte("refs/heads/a\x00" + id + "\x00\x00\x00\n"))
			if err != nil {
				t.Fatalf("object id %q must parse: %v", id, err)
			}
			if records["refs/heads/a"].ObjectID != id {
				t.Fatalf("object id must be stored verbatim, got %q", records["refs/heads/a"].ObjectID)
			}
		}
		for _, id := range []string{"", "ZZ"} {
			if _, err := parseBranchRefInventory([]byte("refs/heads/a\x00" + id + "\x00\x00\x00\n")); err == nil {
				t.Fatalf("object id %q must fail closed", id)
			}
		}
	})

	t.Run("worktree parser", func(t *testing.T) {
		for _, id := range []string{sha1, sha256} {
			inv := parseWorktreeInventory([]byte("worktree /tmp/x\nHEAD " + id + "\nbranch refs/heads/a\n\n"))
			if !inv.Available {
				t.Fatalf("object id %q must keep the inventory available: %v", id, inv.Err)
			}
			rec := inv.Records[0]
			if rec.Head == nil || *rec.Head != id {
				t.Fatalf("HEAD must be stored verbatim, got %v", rec.Head)
			}
		}
		for _, id := range []string{"", "ZZ"} {
			inv := parseWorktreeInventory([]byte("worktree /tmp/x\nHEAD " + id + "\nbranch refs/heads/a\n\n"))
			if inv.Available {
				t.Fatalf("HEAD %q must fail closed", id)
			}
		}
	})

	t.Run("source boundary", func(t *testing.T) {
		src := ssSourceFile(t, "stack_status.go")
		if strings.Contains(src, "[0-9a-f]{40}") {
			t.Fatal("internal/stack_status.go must carry no 40-hex literal")
		}
		start := strings.Index(src, "// stackStatusParentCounts counts")
		body := strings.Index(src, "func stackStatusParentCounts")
		if start < 0 || body < 0 {
			t.Fatal("the parent-count precondition block must exist")
		}
		end := body + strings.Index(src[body:], "\n}\n")
		inside := strings.Count(src[start:end], "ancestryFullSHA")
		if inside == 0 || inside != strings.Count(src, "ancestryFullSHA") {
			t.Fatalf("ancestryFullSHA must live only in the parent-count precondition, %d of %d uses are inside it",
				inside, strings.Count(src, "ancestryFullSHA"))
		}
		if strings.Contains(ssSourceFile(t, "agent_status.go"), "ancestryFullSHA") {
			t.Fatal("the worktree parser must stay object-format neutral")
		}
	})
}

func TestStackStatus_BranchTagCollision(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	gitInTest(t, fx.repo, "branch", "dup", "main")
	ssCommitOnBranch(t, fx.repo, "dup", "dup.txt", "dup\n", "dup change")
	gitInTest(t, fx.repo, "push", "-u", "origin", "dup")
	gitInTest(t, fx.repo, "tag", "dup", "main")

	r := fx.build(fx.stack(StackEntry{Name: "dup", Base: "main"}))
	row := ssEntry(t, r, "dup")
	if row.Upstream.Ref == nil || *row.Upstream.Ref != "refs/remotes/origin/dup" {
		t.Fatalf("the row must join on the branch, got %v", row.Upstream.Ref)
	}
	if row.Heads.Local == nil {
		t.Fatal("the branch head must resolve")
	}
	human := FormatStackStatus(r)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(human, "is ambiguous") || strings.Contains(string(raw), "is ambiguous") {
		t.Fatal("ambiguity warnings must never leak into output")
	}
}

func TestStackStatus_JoinKeyEqualsChildRef(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	other := filepath.Join(fx.root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, fx.root, "init", "--initial-branch=main", other)
	ssCommit(t, other, "README.md", "other\n", "other initial")
	gitInTest(t, fx.repo, "branch", "jd/api", "main")

	stack := fx.stack(
		StackEntry{Name: "api", Branch: "jd/api", Base: "main"},
		StackEntry{Name: "wiki", Base: "main", Repo: other},
	)
	r := fx.build(stack)
	edges, _ := FeatureStackEdges(fx.ws, Config{}, fx.feature, fx.featurePath, stack)

	for i, edge := range edges {
		if !edge.RefProbed {
			continue
		}
		if derived := "refs/heads/" + stack.Branches[i].GitBranch(); derived != edge.ChildRef {
			t.Fatalf("row %d derived key %q != ChildRef %q", i, derived, edge.ChildRef)
		}
	}
	wiki := ssEntry(t, r, "wiki")
	if wiki.Upstream.Configured != nil || wiki.Upstream.State != nil || wiki.Upstream.Ref != nil ||
		wiki.Upstream.Display != nil || wiki.Upstream.Ahead != nil || wiki.Upstream.Behind != nil {
		t.Fatalf("a cross-repo row performs no lookup: %+v", wiki.Upstream)
	}
	if !wiki.Upstream.LocalOnly {
		t.Fatal("local_only stays true on a cross-repo row")
	}
}

// ---------------------------------------------------------------------------
// Materialization and mode truth (AC 28, 29, 30, 31)
// ---------------------------------------------------------------------------

func TestStackStatus_ExternalMaterializationMatrix(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	other := filepath.Join(fx.root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, fx.root, "init", "--initial-branch=main", other)
	ssCommit(t, other, "README.md", "other\n", "other initial")

	for _, branch := range []string{"attached", "detachedwt", "wrongbranch", "elsewhere", "goneWt", "missing", "archivedbr", "shared"} {
		gitInTest(t, fx.repo, "branch", branch, "main")
	}
	attached := fx.worktree("attached", "attached")
	detached := fx.worktree("detachedwt", "detachedwt")
	gitInTest(t, detached, "checkout", "--detach")
	wrong := fx.worktree("wrongbranch", "wrongbranch")
	gitInTest(t, wrong, "checkout", "elsewhere")
	gonePath := fx.worktree("goneWt", "goneWt")
	if err := os.RemoveAll(gonePath); err != nil {
		t.Fatal(err)
	}
	sharedPath := fx.worktree("shared", "shared")

	rec := ssInstallGitRecorder(t)
	rec.reset()
	stack := fx.stack(
		StackEntry{Name: "attached", Base: "main"},
		StackEntry{Name: "detachedwt", Base: "main"},
		StackEntry{Name: "wrongbranch", Base: "main"},
		StackEntry{Name: "goneWt", Base: "main"},
		StackEntry{Name: "missing", Base: "main"},
		StackEntry{Name: "archivedbr", Base: "main", Archived: true},
		StackEntry{Name: "../escape", Base: "main"},
		StackEntry{Name: "shared", Base: "main"},
		StackEntry{Name: "shared-mirror", Branch: "shared", Base: "main"},
		StackEntry{Name: "wiki", Base: "main", Repo: other},
	)
	r := fx.build(stack)

	type want struct {
		state      string
		path       string
		checkedOut string
		detached   *bool
	}
	cases := map[string]want{
		"attached":      {state: StackStatusMaterializedPresent, path: attached, checkedOut: "attached", detached: boolPtr(false)},
		"detachedwt":    {state: StackStatusMaterializedPresent, path: canonicalize(detached), detached: boolPtr(true)},
		"wrongbranch":   {state: StackStatusMaterializedPresent, path: canonicalize(wrong), checkedOut: "elsewhere", detached: boolPtr(false)},
		"goneWt":        {state: StackStatusMaterializedPrunableMissing, path: canonicalize(gonePath), checkedOut: "goneWt", detached: boolPtr(false)},
		"missing":       {state: StackStatusMaterializedMissing},
		"archivedbr":    {state: StackStatusMaterializedArchived},
		"../escape":     {state: StackStatusMaterializedUnknown},
		"shared":        {state: StackStatusMaterializedPresent, path: canonicalize(sharedPath), checkedOut: "shared", detached: boolPtr(false)},
		"shared-mirror": {state: StackStatusMaterializedMissing},
		"wiki":          {state: StackStatusMaterializedCrossRepo},
	}
	for name, w := range cases {
		row := ssEntry(t, r, name)
		m := row.Materialization
		if m.Kind != StackStatusKindWorktree {
			t.Fatalf("%s: kind = %q, want worktree", name, m.Kind)
		}
		if m.State != w.state {
			t.Fatalf("%s: state = %q, want %q", name, m.State, w.state)
		}
		if w.path == "" {
			if m.Path != nil {
				t.Fatalf("%s: path = %q, want null", name, *m.Path)
			}
		} else if m.Path == nil || *m.Path != w.path {
			t.Fatalf("%s: path = %v, want %q", name, m.Path, w.path)
		}
		if w.checkedOut == "" {
			if m.CheckedOutBranch != nil {
				t.Fatalf("%s: checked_out_branch = %q, want null", name, *m.CheckedOutBranch)
			}
		} else if m.CheckedOutBranch == nil || *m.CheckedOutBranch != w.checkedOut {
			t.Fatalf("%s: checked_out_branch = %v, want %q", name, m.CheckedOutBranch, w.checkedOut)
		}
		if (w.detached == nil) != (m.Detached == nil) {
			t.Fatalf("%s: detached = %v, want %v", name, m.Detached, w.detached)
		}
		if w.detached != nil && *m.Detached != *w.detached {
			t.Fatalf("%s: detached = %v, want %v", name, *m.Detached, *w.detached)
		}
		if w.state != StackStatusMaterializedPresent && (m.Dirty != nil || m.ActiveGitOp != nil) {
			t.Fatalf("%s: only a present row is probed, got %+v", name, m)
		}
	}

	// No per-row branch probe exists anywhere in the captured argv.
	for _, inv := range rec.invocations() {
		joined := strings.Join(inv.args, " ")
		for _, forbidden := range []string{"symbolic-ref", "--abbrev-ref HEAD", "show-ref", "branch --list"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("per-row branch probe detected: git %s", joined)
			}
		}
		if strings.Contains(joined, other) {
			t.Fatalf("no invocation may name the cross-repo path: git %s", joined)
		}
	}
}

func TestStackStatus_ExternalIsCurrentCheckoutAlwaysNull(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	gitInTest(t, fx.repo, "branch", "api", "main")
	path := fx.worktree("api", "api")
	_ = path

	r := fx.build(fx.stack(
		StackEntry{Name: "api", Base: "main"},
		StackEntry{Name: "main-mirror", Branch: "main", Base: "main"},
		StackEntry{Name: "missing", Base: "main"},
	))
	for _, row := range r.Entries {
		if row.IsCurrentCheckout != nil {
			t.Fatalf("%s: is_current_checkout = %v, want null in external mode", row.Name, *row.IsCurrentCheckout)
		}
	}
	human := FormatStackStatus(r)
	for _, line := range strings.Split(human, "\n") {
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "BRANCH") {
			continue
		}
		fields := strings.Fields(line)
		flags := fields[len(fields)-1]
		for _, token := range strings.Split(flags, ",") {
			if token == "current" {
				t.Fatalf("the FLAGS column never carries `current` in external mode:\n%s", human)
			}
		}
	}
}

func TestStackStatus_ExternalStrayDirectory(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	gitInTest(t, fx.repo, "branch", "api", "main")
	stray := filepath.Join(fx.featurePath, "worktrees", "api")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}

	entries := []StackEntry{{Name: "api", Base: "main"}}
	if err := SaveStack(fx.featurePath, Stack{Branches: entries}); err != nil {
		t.Fatal(err)
	}
	r := fx.build(Stack{Branches: entries})
	if got := ssEntry(t, r, "api").Materialization.State; got != StackStatusMaterializedMissing {
		t.Fatalf("stack status reports the porcelain truth, got %q", got)
	}

	agent, err := BuildAgentStatus(fx.ws, "", statusOpts(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	NormalizeAgentStatus(agent)
	if got := findEntry(t, agent, "auth", "api").Materialization.State; got != MaterializedPresent {
		t.Fatalf("tws status keeps its os.Stat semantics, got %q", got)
	}
}

func TestStackStatus_CheckoutModeTruth(t *testing.T) {
	t.Run("attached", func(t *testing.T) {
		fx := ssNewCheckoutFixture(t, "auth")
		gitInTest(t, fx.repo, "branch", "api", "main")
		gitInTest(t, fx.repo, "checkout", "api")
		r := fx.build(fx.stack(
			StackEntry{Name: "api", Base: "main"},
			StackEntry{Name: "api-mirror", Branch: "api", Base: "main"},
			StackEntry{Name: "other", Base: "main"},
			StackEntry{Name: "baseless"},
		))
		c := r.Workspace.Checkout
		if c == nil || c.Branch == nil || *c.Branch != "api" {
			t.Fatalf("checkout branch = %+v", c)
		}
		if c.Detached == nil || *c.Detached {
			t.Fatalf("checkout detached = %v, want false", c.Detached)
		}
		if c.Dirty == nil || c.ActiveGitOp == nil || *c.ActiveGitOp != StackStatusOpNone {
			t.Fatalf("checkout probes = %+v", c)
		}
		if r.Workspace.External != nil {
			t.Fatal("checkout mode carries a null external block")
		}
		for _, name := range []string{"api", "api-mirror"} {
			row := ssEntry(t, r, name)
			if row.Materialization.Kind != StackStatusKindRef || row.Materialization.Path != nil {
				t.Fatalf("%s materialization = %+v", name, row.Materialization)
			}
			if row.Materialization.CheckedOutBranch == nil || *row.Materialization.CheckedOutBranch != "api" {
				t.Fatalf("%s checked_out_branch = %v", name, row.Materialization.CheckedOutBranch)
			}
			if row.Materialization.Dirty == nil || row.Materialization.ActiveGitOp == nil {
				t.Fatalf("%s must copy the checkout probes: %+v", name, row.Materialization)
			}
			if row.IsCurrentCheckout == nil || !*row.IsCurrentCheckout {
				t.Fatalf("%s is_current_checkout = %v", name, row.IsCurrentCheckout)
			}
		}
		other := ssEntry(t, r, "other")
		if other.Materialization.CheckedOutBranch != nil || other.Materialization.Dirty != nil ||
			other.Materialization.ActiveGitOp != nil {
			t.Fatalf("a non-current row gets null probes: %+v", other.Materialization)
		}
		if other.IsCurrentCheckout == nil || *other.IsCurrentCheckout {
			t.Fatalf("other is_current_checkout = %v, want false", other.IsCurrentCheckout)
		}
		if other.Materialization.State != StackStatusMaterializedMissing {
			t.Fatalf("a missing ref reports missing, got %q", other.Materialization.State)
		}
		if got := ssEntry(t, r, "baseless").Materialization.State; got != StackStatusMaterializedUnknown {
			t.Fatalf("a base-unset row reports unknown, got %q", got)
		}
		if got := ssEntry(t, r, "api").Materialization.State; got != StackStatusMaterializedPresent {
			t.Fatalf("an existing ref reports present, got %q", got)
		}
	})

	t.Run("cross-repo row equal to the checked-out branch", func(t *testing.T) {
		fx := ssNewCheckoutFixture(t, "auth")
		gitInTest(t, fx.repo, "branch", "api", "main")
		gitInTest(t, fx.repo, "checkout", "api")
		other := filepath.Join(t.TempDir(), "other")
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, filepath.Dir(other), "init", "--initial-branch=main", other)
		ssCommit(t, other, "README.md", "other\n", "other initial")

		rec := ssInstallGitRecorder(t)
		rec.reset()
		r := fx.build(fx.stack(
			StackEntry{Name: "api", Base: "main"},
			// The foreign row deliberately names this checkout's branch.
			StackEntry{Name: "wiki", Branch: "api", Base: "main", Repo: other},
		))
		if c := r.Workspace.Checkout; c == nil || c.Branch == nil || *c.Branch != "api" {
			t.Fatalf("the fixture must have branch api checked out, got %+v", c)
		}
		wiki := ssEntry(t, r, "wiki")
		m := wiki.Materialization
		if m.State != StackStatusMaterializedCrossRepo {
			t.Fatalf("wiki state = %q, want %q", m.State, StackStatusMaterializedCrossRepo)
		}
		if m.CheckedOutBranch != nil || m.Detached != nil || m.Dirty != nil ||
			m.ActiveGitOp != nil || m.Path != nil {
			t.Fatalf("a cross-repo row claims no local checkout fact: %+v", m)
		}
		if wiki.IsCurrentCheckout != nil {
			t.Fatalf("wiki is_current_checkout = %v, want null", *wiki.IsCurrentCheckout)
		}
		for _, inv := range rec.invocations() {
			if joined := strings.Join(inv.args, " "); strings.Contains(joined, other) {
				t.Fatalf("a cross-repo row started a process: git %s", joined)
			}
		}
	})

	t.Run("detached", func(t *testing.T) {
		fx := ssNewCheckoutFixture(t, "auth")
		gitInTest(t, fx.repo, "branch", "api", "main")
		gitInTest(t, fx.repo, "checkout", "--detach")
		r := fx.build(fx.stack(StackEntry{Name: "api", Base: "main"}))
		c := r.Workspace.Checkout
		if c.Branch != nil {
			t.Fatalf("detached checkout branch = %v, want null", *c.Branch)
		}
		if c.Detached == nil || !*c.Detached {
			t.Fatalf("detached = %v, want true", c.Detached)
		}
		for _, row := range r.Entries {
			if row.IsCurrentCheckout != nil {
				t.Fatalf("%s is_current_checkout = %v, want null while detached", row.Name, *row.IsCurrentCheckout)
			}
		}
	})

	t.Run("unavailable inventory", func(t *testing.T) {
		fx := ssNewCheckoutFixture(t, "auth")
		gitInTest(t, fx.repo, "branch", "api", "main")
		ssInstallPorcelainShim(t, "worktree\n\n")
		r := fx.build(fx.stack(StackEntry{Name: "api", Base: "main"}))
		c := r.Workspace.Checkout
		if c.Branch != nil || c.Detached != nil {
			t.Fatalf("an unavailable inventory claims no branch or attachment: %+v", c)
		}
		for _, row := range r.Entries {
			if row.IsCurrentCheckout != nil {
				t.Fatalf("%s is_current_checkout = %v, want null", row.Name, *row.IsCurrentCheckout)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Probes, no-fetch, legacy equivalence (AC 32, 33, 35, 37)
// ---------------------------------------------------------------------------

func TestStackStatus_ProbesAreTriState(t *testing.T) {
	ssNeutralizeGitConfig(t)

	t.Run("no git entry", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := probeActiveGitOp(dir); err == nil {
			t.Fatal("a path with no .git must error, never report none")
		}
	})

	gitFileCases := map[string]string{
		"no gitdir prefix":      "not a gitdir pointer\n",
		"empty gitdir target":   "gitdir: \n",
		"missing gitdir target": "gitdir: /nonexistent/tws/gitdir\n",
	}
	for name, content := range gitFileCases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if op, err := probeActiveGitOp(dir); err == nil {
				t.Fatalf("must error, got %q", op)
			}
			if gitActiveOp(dir) != "" {
				t.Fatal("the wrapper still collapses to the legacy empty string")
			}
		})
	}

	t.Run("gitdir target is a regular file", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+target+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if op, err := probeActiveGitOp(dir); err == nil {
			t.Fatalf("must error, got %q", op)
		}
		if gitActiveOp(dir) != "" {
			t.Fatal("the wrapper still collapses to the legacy empty string")
		}
	})

	t.Run("marker stat fails", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can stat inside a 0000 directory")
		}
		dir := t.TempDir()
		gitDir := filepath.Join(dir, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(gitDir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })
		if _, statErr := os.Stat(filepath.Join(gitDir, "rebase-merge")); statErr == nil || errors.Is(statErr, fs.ErrNotExist) {
			t.Skip("this filesystem does not produce a non-ENOENT marker stat error")
		}
		if op, err := probeActiveGitOp(dir); err == nil {
			t.Fatalf("a non-ENOENT marker stat is an error, got %q", op)
		}
		if gitActiveOp(dir) != "" {
			t.Fatal("the wrapper still collapses to the legacy empty string")
		}
	})

	t.Run("healthy and rebasing", func(t *testing.T) {
		fx := ssNewExternalFixture(t, "auth")
		op, err := probeActiveGitOp(fx.repo)
		if err != nil || op != StackStatusOpNone {
			t.Fatalf("a healthy worktree yields (none, nil), got (%q, %v)", op, err)
		}
		dirty, err := probeDirty(fx.repo)
		if err != nil || dirty {
			t.Fatalf("a clean worktree yields (false, nil), got (%v, %v)", dirty, err)
		}

		gitInTest(t, fx.repo, "branch", "models", "main")
		gitInTest(t, fx.repo, "branch", "api", "main")
		wt := fx.worktree("api", "api")
		ssCommitOnBranch(t, fx.repo, "models", "conflict.txt", "models\n", "models change")
		ssCommit(t, wt, "conflict.txt", "api\n", "api change")
		cmd := exec.Command("git", "-C", wt, "rebase", "models")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+wt)
		if out, rebaseErr := cmd.CombinedOutput(); rebaseErr == nil {
			t.Fatalf("the fixture rebase must conflict: %s", out)
		}
		op, err = probeActiveGitOp(wt)
		if err != nil || op != "rebase" {
			t.Fatalf("an interrupted rebase yields (rebase, nil), got (%q, %v)", op, err)
		}
		if gitActiveOp(wt) != "rebase" {
			t.Fatal("the wrapper must still report rebase")
		}
	})

	t.Run("dirty probe errors", func(t *testing.T) {
		if _, err := probeDirty(""); err == nil {
			t.Fatal("an empty path must error without starting a process")
		}
		if gitDirty("") {
			t.Fatal("the wrapper still collapses to false")
		}
		fx := ssNewExternalFixture(t, "auth")
		if _, err := probeDirty(fx.remote); err == nil {
			t.Fatal("git status in a bare repository must error")
		}
		if gitDirty(fx.remote) {
			t.Fatal("the wrapper still collapses to false")
		}
	})

	t.Run("report level nulls", func(t *testing.T) {
		fx := ssNewExternalFixture(t, "auth")
		gitInTest(t, fx.repo, "branch", "api", "main")
		wt := fx.worktree("api", "api")
		// A worktree whose .git pointer target is gone: Git still lists it, so
		// the row is present, but both probes fail and must report null.
		if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /nonexistent/tws/gitdir\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := fx.build(fx.stack(StackEntry{Name: "api", Base: "main"}))
		row := ssEntry(t, r, "api")
		if row.Materialization.State != StackStatusMaterializedPresent {
			t.Skipf("this Git reports the broken worktree as %q; the null-probe rule is covered by the unit cases above", row.Materialization.State)
		}
		if row.Materialization.Dirty != nil {
			t.Fatalf("dirty = %v, want null", *row.Materialization.Dirty)
		}
		if row.Materialization.ActiveGitOp != nil {
			t.Fatalf("active_git_op = %v, want null", *row.Materialization.ActiveGitOp)
		}
		if r.Summary.Unknown.Dirty != 1 || r.Summary.Unknown.ActiveGitOp != 1 {
			t.Fatalf("summary unknown = %+v", r.Summary.Unknown)
		}
	})

	t.Run("healthy report level", func(t *testing.T) {
		fx := ssNewExternalFixture(t, "auth")
		gitInTest(t, fx.repo, "branch", "api", "main")
		fx.worktree("api", "api")
		r := fx.build(fx.stack(StackEntry{Name: "api", Base: "main"}))
		row := ssEntry(t, r, "api")
		if row.Materialization.Dirty == nil || *row.Materialization.Dirty {
			t.Fatalf("dirty = %v, want false", row.Materialization.Dirty)
		}
		if row.Materialization.ActiveGitOp == nil || *row.Materialization.ActiveGitOp != StackStatusOpNone {
			t.Fatalf("active_git_op = %v, want none", row.Materialization.ActiveGitOp)
		}
	})
}

func TestStackStatus_NoFetchShim(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	other := filepath.Join(fx.root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, fx.root, "init", "--initial-branch=main", other)
	ssCommit(t, other, "README.md", "other\n", "other initial")
	gitInTest(t, fx.repo, "branch", "api", "main")
	fx.worktree("api", "api")

	rec := ssInstallGitRecorder(t)
	rec.reset()
	fx.build(fx.stack(
		StackEntry{Name: "api", Base: "main"},
		StackEntry{Name: "wiki", Base: "main", Repo: other},
	))

	forbidden := []string{"fetch", "ls-remote", "push", "update-ref", "reset", "checkout", "switch", "rebase", "gc", "prune"}
	for _, inv := range rec.invocations() {
		for _, arg := range inv.args {
			for _, verb := range forbidden {
				if arg == verb {
					t.Fatalf("forbidden verb %q in: git %s", verb, strings.Join(inv.args, " "))
				}
			}
		}
		joined := strings.Join(inv.args, " ")
		if strings.Contains(joined, "worktree prune") {
			t.Fatalf("worktree prune is forbidden: git %s", joined)
		}
		if strings.Contains(joined, other) {
			t.Fatalf("no invocation may name the cross-repo path: git %s", joined)
		}
	}

	for _, name := range []string{"stack_status.go", "cli/stack_status.go"} {
		src := ssSourceFile(t, name)
		if strings.Contains(src, "--fetch") {
			t.Fatalf("%s must carry no --fetch flag", name)
		}
		if strings.Contains(src, "patch-id") || strings.Contains(src, "patch_id") {
			t.Fatalf("%s must carry no patch identity surface", name)
		}
		for _, verb := range []string{`"fetch"`, `"ls-remote"`, `"update-ref"`, `"push"`} {
			if strings.Contains(src, verb) {
				t.Fatalf("%s must not name %s", name, verb)
			}
		}
	}
}

func TestStackStatus_ParentCounts(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	other := filepath.Join(fx.root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, fx.root, "init", "--initial-branch=main", other)
	ssCommit(t, other, "README.md", "other\n", "other initial")

	gitInTest(t, fx.repo, "branch", "models", "main")
	gitInTest(t, fx.repo, "branch", "api", "models")
	ssCommitOnBranch(t, fx.repo, "models", "m1.txt", "m1\n", "models one")
	ssCommitOnBranch(t, fx.repo, "models", "m2.txt", "m2\n", "models two")
	ssCommitOnBranch(t, fx.repo, "api", "a1.txt", "a1\n", "api one")

	rec := ssInstallGitRecorder(t)
	rec.reset()
	r := fx.build(fx.stack(
		StackEntry{Name: "models", Base: "main"},
		StackEntry{Name: "api", Base: "models"},
		StackEntry{Name: "baseless"},
		StackEntry{Name: "wiki", Base: "main", Repo: other},
		StackEntry{Name: "missingchild", Base: "main"},
		StackEntry{Name: "models-missing-base", Branch: "models", Base: "no-such-base"},
	))

	invocations := rec.invocations()

	api := ssEntry(t, r, "api")
	if api.ParentCounts.Ahead == nil || api.ParentCounts.Behind == nil {
		t.Fatalf("api parent counts = %+v", api.ParentCounts)
	}
	independent := gitInTest(t, fx.repo, "rev-list", "--left-right", "--count",
		stackStatusDeref(api.Heads.Local)+"..."+stackStatusDeref(api.Heads.Parent))
	if got := fmt.Sprintf("%d\t%d", *api.ParentCounts.Ahead, *api.ParentCounts.Behind); got != independent {
		t.Fatalf("parent counts = %q, independent run = %q", got, independent)
	}

	for _, name := range []string{"baseless", "wiki", "missingchild", "models-missing-base"} {
		row := ssEntry(t, r, name)
		if row.ParentCounts.Ahead != nil || row.ParentCounts.Behind != nil {
			t.Fatalf("%s must report two nulls, got %+v", name, row.ParentCounts)
		}
	}

	counts := 0
	for _, inv := range invocations {
		if ssHasArg(inv, "rev-list") && ssHasArg(inv, "--left-right") {
			counts++
			last := inv.args[len(inv.args)-1]
			if !ssFullSHAPair.MatchString(last) {
				t.Fatalf("both operands must be 40-hex, got %q", last)
			}
		}
	}
	if counts != 2 {
		t.Fatalf("only the two eligible rows may start a count process, got %d", counts)
	}

	// A forced command failure reports two nulls, never zeros.
	if a, b := stackStatusParentCounts(fx.repo, strings.Repeat("f", 40), strings.Repeat("e", 40)); a != nil || b != nil {
		t.Fatalf("a failed rev-list reports nulls, got %v/%v", a, b)
	}
	if a, b := stackStatusParentCounts("", strings.Repeat("a", 40), strings.Repeat("b", 40)); a != nil || b != nil {
		t.Fatal("an empty repository dir starts no process")
	}
	if a, b := stackStatusParentCounts(fx.repo, "abc", strings.Repeat("b", 40)); a != nil || b != nil {
		t.Fatal("a non-40-hex operand starts no process")
	}
}

// ---------------------------------------------------------------------------
// Legacy equivalence and divergence (AC 35, package-internal half)
// ---------------------------------------------------------------------------

func ssAssertInventoryEquivalence(t *testing.T, label, repoRoot string) {
	t.Helper()
	legacy := legacyBuildWorktreeInventory(repoRoot)
	current := BuildWorktreeInventory(repoRoot)
	if legacy.Available != current.Available {
		t.Fatalf("%s: Available = %v, legacy %v", label, current.Available, legacy.Available)
	}
	if len(legacy.ByBranch) != len(current.ByBranch) {
		t.Fatalf("%s: ByBranch = %v, legacy %v", label, current.ByBranch, legacy.ByBranch)
	}
	for branch, path := range legacy.ByBranch {
		if current.ByBranch[branch] != path {
			t.Fatalf("%s: ByBranch[%q] = %q, legacy %q (raw porcelain path must be preserved)",
				label, branch, current.ByBranch[branch], path)
		}
	}
	if len(legacy.Prunable) != len(current.Prunable) {
		t.Fatalf("%s: Prunable = %v, legacy %v", label, current.Prunable, legacy.Prunable)
	}
	for branch, prunable := range legacy.Prunable {
		if current.Prunable[branch] != prunable {
			t.Fatalf("%s: Prunable[%q] = %v, legacy %v", label, branch, current.Prunable[branch], prunable)
		}
	}
}

func ssAssertProbeEquivalence(t *testing.T, label, path string) {
	t.Helper()
	if got, want := gitDirty(path), legacyGitDirty(path); got != want {
		t.Fatalf("%s: gitDirty = %v, legacy %v", label, got, want)
	}
	if got, want := gitActiveOp(path), legacyGitActiveOp(path); got != want {
		t.Fatalf("%s: gitActiveOp = %q, legacy %q", label, got, want)
	}
}

func TestStackStatus_LegacyProbeEquivalence(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	for _, branch := range []string{"attached", "detachedwt", "lockedbr", "gonebr", "dup"} {
		gitInTest(t, fx.repo, "branch", branch, "main")
	}
	attached := fx.worktree("attached", "attached")
	detached := fx.worktree("detachedwt", "detachedwt")
	gitInTest(t, detached, "checkout", "--detach")
	locked := fx.worktree("lockedbr", "lockedbr")
	gitInTest(t, fx.repo, "worktree", "lock", "--reason", "busy testing", locked)
	gone := fx.worktree("gonebr", "gonebr")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	t.Run("equivalence set", func(t *testing.T) {
		ssAssertInventoryEquivalence(t, "real porcelain", fx.repo)
		ssAssertInventoryEquivalence(t, "empty repo root", "")
		ssAssertInventoryEquivalence(t, "non-repository", fx.root)

		// The bare main worktree case: an in-tree .git plus core.bare, which is
		// the only real shape that emits a `bare` porcelain block.
		bareRoot := t.TempDir()
		bare := filepath.Join(bareRoot, "repo")
		if err := os.MkdirAll(bare, 0o755); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, bareRoot, "init", "--initial-branch=main", bare)
		ssCommit(t, bare, "README.md", "bare\n", "initial")
		gitInTest(t, bare, "branch", "linked", "main")
		gitInTest(t, bare, "worktree", "add", filepath.Join(bareRoot, "wt"), "linked")
		gitInTest(t, bare, "config", "core.bare", "true")
		ssAssertInventoryEquivalence(t, "bare main worktree", bare)
		if inv := BuildWorktreeInventory(bare); !inv.Available || len(inv.Records) != 2 || !inv.Records[0].Bare {
			t.Fatalf("the bare block must be retained: %+v", inv.Records)
		}

		for label, path := range map[string]string{
			"clean worktree":  attached,
			"detached":        detached,
			"locked":          locked,
			"missing":         gone,
			"repo root":       fx.repo,
			"bare repository": fx.remote,
		} {
			ssAssertProbeEquivalence(t, label, path)
		}

		// gitDirty("") is unreachable in production: every caller guards an
		// empty repository root first. The probe now refuses it instead of
		// letting `git -C ""` report the ambient repository, and the wrapper
		// still collapses to the legacy false.
		if gitDirty("") {
			t.Fatal("an empty repository root must never report dirty")
		}

		// Probe-hardening fixtures still collapse to the legacy values.
		hard := t.TempDir()
		ssAssertProbeEquivalence(t, "no .git", hard)
		for name, content := range map[string]string{
			"malformed gitdir": "not a pointer\n",
			"empty gitdir":     "gitdir: \n",
			"missing target":   "gitdir: /nonexistent/tws/gitdir\n",
		} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			ssAssertProbeEquivalence(t, name, dir)
		}

		// Dirty state must agree on a genuinely modified worktree.
		if err := os.WriteFile(filepath.Join(attached, "README.md"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ssAssertProbeEquivalence(t, "dirty worktree", attached)
	})

	t.Run("sha256 equivalence", func(t *testing.T) {
		if !ssSupportsSHA256(t) {
			t.Skip("this git cannot create a SHA-256 repository")
		}
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, root, "init", "--object-format=sha256", "--initial-branch=main", repo)
		ssCommit(t, repo, "README.md", "sha256\n", "initial")
		gitInTest(t, repo, "branch", "linked", "main")
		gitInTest(t, repo, "worktree", "add", filepath.Join(root, "wt"), "linked")

		inv := BuildWorktreeInventory(repo)
		if !inv.Available {
			t.Fatalf("a SHA-256 repository must keep the inventory available: %v", inv.Err)
		}
		head := inv.Records[0].Head
		if head == nil || len(*head) != 64 {
			t.Fatalf("HEAD must be a verbatim 64-hex object id, got %v", head)
		}
		ssAssertInventoryEquivalence(t, "sha256", repo)
	})

	t.Run("divergence set", func(t *testing.T) {
		payloads := map[string]string{
			"no worktree line": "HEAD " + strings.Repeat("a", 40) + "\nbranch refs/heads/a\n\n",
			"empty worktree path": "worktree \nHEAD " + strings.Repeat("a", 40) +
				"\nbranch refs/heads/a\n\n",
			"blank worktree path": "worktree \t  \nHEAD " + strings.Repeat("a", 40) +
				"\nbranch refs/heads/a\n\n",
			"duplicate path":    "worktree /x\nbranch refs/heads/a\n\nworktree /x\nbranch refs/heads/b\n\n",
			"malformed branch":  "worktree /x\nbranch heads/a\n\n",
			"empty head":        "worktree /x\nHEAD \nbranch refs/heads/a\n\n",
			"non hex head":      "worktree /x\nHEAD ZZZZ\nbranch refs/heads/a\n\n",
			"branch + detached": "worktree /x\nbranch refs/heads/a\ndetached\n\n",
		}
		for name, payload := range payloads {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				ssInstallPorcelainShim(t, payload)
				current := BuildWorktreeInventory(dir)
				if current.Available || current.Err == nil {
					t.Fatalf("the new inventory must fail closed, got %+v", current)
				}
				if len(current.Records) != 0 || len(current.ByPath) != 0 ||
					len(current.ByBranch) != 0 || len(current.Prunable) != 0 {
					t.Fatalf("a failed inventory publishes no partial map: %+v", current)
				}
				// The legacy parser is tolerant on exactly these inputs; the
				// divergence is deliberate and is never asserted as equality.
				_ = legacyBuildWorktreeInventory(dir)
			})
		}
	})

	t.Run("unavailable inventory consumer branch", func(t *testing.T) {
		entries := []StackEntry{{Name: "api", Base: "main"}}
		ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
		wtPath := addExternalWorktree(t, ws, featurePath, entries[0])
		if err := os.RemoveAll(wtPath); err != nil {
			t.Fatal(err)
		}
		ssInstallPorcelainShim(t, "worktree /x\nbranch heads/a\n\n")
		if inv := BuildWorktreeInventory(ws.RepoRoot); inv.Available {
			t.Fatal("the shim must invalidate the inventory")
		}
		report := buildStatus(t, ws, statusOpts(nil, nil))
		NormalizeAgentStatus(report)
		api := findEntry(t, report, "auth", "api")
		if api.Materialization.State != MaterializedMissing {
			t.Fatalf("BuildAgentStatus must take its existing unavailable-inventory branch, got %q",
				api.Materialization.State)
		}
		known := map[string]bool{}
		for _, code := range AgentStatusIssueCodes {
			known[code] = true
		}
		for _, issue := range report.Issues {
			if !known[issue.Code] {
				t.Fatalf("no new issue code may appear: %q", issue.Code)
			}
			switch issue.Severity {
			case SeverityOK, SeverityInfo, SeverityWarning, SeverityError:
			default:
				t.Fatalf("no new severity may appear: %q", issue.Severity)
			}
		}
	})
}

func ssSupportsSHA256(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "--object-format=sha256", "probe")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0", "HOME="+dir)
	return cmd.Run() == nil
}

// ---------------------------------------------------------------------------
// Process budget (AC 36 rules 1-4)
// ---------------------------------------------------------------------------

func TestStackStatus_ProcessBudget(t *testing.T) {
	fx := ssNewExternalFixture(t, "auth")
	other := filepath.Join(fx.root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, fx.root, "init", "--initial-branch=main", other)
	ssCommit(t, other, "README.md", "other\n", "other initial")

	gitInTest(t, fx.repo, "branch", "models", "main")
	gitInTest(t, fx.repo, "branch", "api", "models")
	fx.worktree("models", "models")
	fx.worktree("api", "api")
	ssCommitOnBranch(t, fx.repo, "models", "m.txt", "m\n", "models change")

	stack := fx.stack(
		StackEntry{Name: "models", Base: "main"},
		StackEntry{Name: "api", Base: "models"},
	)

	rec := ssInstallGitRecorder(t)

	// Rule 1: the fixture is prepared and the control run is stable.
	rec.reset()
	FeatureStackEdges(fx.ws, Config{}, fx.feature, fx.featurePath, stack)
	controlA := rec.invocations()
	rec.reset()
	FeatureStackEdges(fx.ws, Config{}, fx.feature, fx.featurePath, stack)
	controlB := rec.invocations()
	if strings.Join(ssInvocationKeys(controlA), "|") != strings.Join(ssInvocationKeys(controlB), "|") {
		t.Fatalf("the control run is not stable:\n%v\n%v", controlA, controlB)
	}

	// Rules 2 and 3: the builder run is A plus exactly the status-added forms.
	rec.reset()
	report := fx.build(stack)
	measured := rec.invocations()
	_, remainder := ssSubtractControl(t, controlA, measured)
	forEachRef, worktreeList, revList, dirty := ssClassifyRemainder(t, remainder)

	wantC, wantD := 0, 0
	for _, row := range report.Entries {
		if row.Heads.Local != nil && row.Heads.Parent != nil {
			wantC++
		}
		if row.Materialization.State == StackStatusMaterializedPresent {
			wantD++
		}
	}
	if forEachRef != 1 || worktreeList != 1 {
		t.Fatalf("the two inventories run exactly once each, got %d/%d", forEachRef, worktreeList)
	}
	if revList != wantC {
		t.Fatalf("parent counts = %d, want C = %d", revList, wantC)
	}
	if dirty != wantD {
		t.Fatalf("dirty probes = %d, want D = %d", dirty, wantD)
	}
	if len(remainder) != 2+wantC+wantD {
		t.Fatalf("status-added total = %d, want 2 + %d + %d", len(remainder), wantC, wantD)
	}
	if len(measured) != len(controlA)+2+wantC+wantD {
		t.Fatalf("builder total = %d, want A(%d) + 2 + %d + %d", len(measured), len(controlA), wantC, wantD)
	}

	// Rule 4: growing E while C and D stay fixed does not grow the remainder,
	// and no invocation names a row's branch, ref, upstream, or worktree path.
	grown := fx.stack(
		StackEntry{Name: "models", Base: "main"},
		StackEntry{Name: "api", Base: "models"},
		StackEntry{Name: "x1", Base: "main", Repo: other},
		StackEntry{Name: "x2", Base: "main", Repo: other},
		StackEntry{Name: "x3", Base: "main", Repo: other},
	)
	rec.reset()
	FeatureStackEdges(fx.ws, Config{}, fx.feature, fx.featurePath, grown)
	grownControl := rec.invocations()
	rec.reset()
	fx.build(grown)
	_, grownRemainder := ssSubtractControl(t, grownControl, rec.invocations())
	if len(grownRemainder) != len(remainder) {
		t.Fatalf("remainder grew with E: %d vs %d", len(grownRemainder), len(remainder))
	}
	for _, inv := range grownRemainder {
		joined := strings.Join(inv.args, " ")
		if strings.Contains(joined, other) {
			t.Fatalf("a cross-repo row started a process: git %s", joined)
		}
		// The classified dirty probe legitimately names a present worktree
		// path; no remainder invocation may name a branch, a ref, or an
		// upstream ref.
		for _, needle := range []string{"refs/heads/models", "refs/heads/api", "refs/remotes/"} {
			if strings.Contains(joined, needle) {
				t.Fatalf("per-row probe detected in the remainder: git %s", joined)
			}
		}
	}

	// A repo-unavailable workspace yields an empty remainder: no inventory, no
	// count, and no dirty probe runs when repository.dir is empty.
	degraded := fx.ws
	degraded.RepoRoot = filepath.Join(fx.root, "no-such-repo")
	degraded.MetadataRoot = filepath.Join(fx.root, "no-such-meta")
	crossOnly := Stack{Branches: []StackEntry{{Name: "x1", Base: "main", Repo: other}}}
	degradedFeaturePath := filepath.Join(degraded.MetadataRoot, fx.feature)
	rec.reset()
	FeatureStackEdges(degraded, Config{}, fx.feature, degradedFeaturePath, crossOnly)
	degradedControl := rec.invocations()
	rec.reset()
	degradedReport, err := BuildStackStatus(degraded, Config{}, fx.feature, degradedFeaturePath, crossOnly)
	if err != nil {
		t.Fatal(err)
	}
	if degradedReport.Workspace.Repository.Dir != nil {
		t.Fatalf("the degraded fixture must have no repository dir, got %q", *degradedReport.Workspace.Repository.Dir)
	}
	_, degradedRemainder := ssSubtractControl(t, degradedControl, rec.invocations())
	if len(degradedRemainder) != 0 {
		t.Fatalf("an unavailable repository adds no process, got %v", degradedRemainder)
	}
	for _, inv := range rec.invocations() {
		if strings.Contains(strings.Join(inv.args, " "), other) {
			t.Fatalf("no invocation may name the cross-repo path: git %s", strings.Join(inv.args, " "))
		}
	}
}

func ssInvocationKeys(in []ssInvocation) []string {
	out := make([]string, 0, len(in))
	for _, i := range in {
		out = append(out, i.key())
	}
	return out
}

// ---------------------------------------------------------------------------
// Human output (AC 38, 39, 40)
// ---------------------------------------------------------------------------

// ssGrammarReport is a hand-built report covering every ancestry state, every
// materialization state, every upstream state, an archived row, a cross-repo
// row, a decoupled git name, and a duplicate branch.
func ssGrammarReport() *StackStatusReport {
	entry := func(name, gitBranch string) StackStatusEntry {
		return StackStatusEntry{
			Name:            name,
			GitBranch:       gitBranch,
			Base:            StackStatusBase{Name: strPtr("main"), Kind: StackBaseLiteralRef, Ref: strPtr("main")},
			Ancestry:        StackStatusAncestry{Severity: SeverityOK, Notes: []StackStatusNote{}},
			Materialization: StackStatusMaterialization{Kind: StackStatusKindWorktree},
			Upstream:        StackStatusUpstream{LocalOnly: true},
		}
	}

	models := entry("auth-models", "auth-models")
	models.RefExists = boolPtr(true)
	models.Heads = StackStatusHeads{Local: strPtr(strings.Repeat("1", 40)), LocalShort: strPtr("1a2b3c4"),
		Parent: strPtr(strings.Repeat("1", 40)), ParentShort: strPtr("1a2b3c4")}
	models.BaseRecord.State = strPtr(string(StackBaseRecordPresent))
	models.Ancestry.Status = strPtr(string(AncestryStatusCurrent))
	models.Ancestry.Reason = ReasonParentContained
	models.ParentCounts = StackStatusParentCounts{Ahead: intPtr(0), Behind: intPtr(0)}
	models.Materialization.State = StackStatusMaterializedPresent
	models.Materialization.Detached = boolPtr(false)
	models.Materialization.Dirty = boolPtr(false)
	models.Materialization.ActiveGitOp = strPtr(StackStatusOpNone)
	models.Upstream = StackStatusUpstream{Configured: boolPtr(true), Ref: strPtr("refs/remotes/origin/models"),
		Display: strPtr("origin/models"), State: strPtr(StackStatusUpstreamEqual),
		Ahead: intPtr(0), Behind: intPtr(0), LocalOnly: true}

	api := entry("auth-api", "jd/api")
	api.RefExists = boolPtr(true)
	api.Heads = StackStatusHeads{Local: strPtr(strings.Repeat("9", 40)), LocalShort: strPtr("9f8e7d6"),
		Parent: strPtr(strings.Repeat("4", 40)), ParentShort: strPtr("4c3b2a1"),
		MergeBase: strPtr(strings.Repeat("5", 40)), MergeBaseShort: strPtr("5c4b3a2")}
	api.BaseRecord = StackStatusBaseRecord{SHA: strPtr("recorded"), Commit: strPtr(strings.Repeat("7", 40)),
		Short: strPtr("7777777"), State: strPtr(string(StackBaseRecordPresent))}
	api.Ancestry.Status = strPtr(string(AncestryStatusStale))
	api.Ancestry.Reason = ReasonParentAdvanced
	api.Ancestry.Severity = SeverityWarning
	api.Ancestry.Guidance = strPtr("run: tws sync auth")
	api.Ancestry.Notes = []StackStatusNote{{Kind: NoteBaseIdentityRemoteMismatch, Detail: "base identity differs"}}
	api.ParentCounts = StackStatusParentCounts{Ahead: intPtr(3), Behind: intPtr(1)}
	api.Materialization.State = StackStatusMaterializedPresent
	api.Materialization.Path = strPtr("/w/auth/worktrees/auth-api")
	api.Materialization.CheckedOutBranch = strPtr("jd/api")
	api.Materialization.Detached = boolPtr(false)
	api.Materialization.Dirty = boolPtr(true)
	api.Materialization.ActiveGitOp = strPtr("rebase")
	api.Upstream = StackStatusUpstream{Configured: boolPtr(true), Ref: strPtr("refs/remotes/origin/api"),
		Display: strPtr("origin/api"), State: strPtr(StackStatusUpstreamAhead),
		Ahead: intPtr(2), Behind: intPtr(0), LocalOnly: true}

	mirror := entry("auth-mirror", "jd/api")
	mirror.RefExists = boolPtr(true)
	mirror.Ancestry.Status = strPtr(string(AncestryStatusDivergent))
	mirror.Ancestry.Reason = ReasonUnrelatedHistories
	mirror.Ancestry.Severity = SeverityWarning
	mirror.Materialization.State = StackStatusMaterializedPrunableMissing
	mirror.Upstream = StackStatusUpstream{Configured: boolPtr(true), Ref: strPtr("refs/remotes/origin/api"),
		Display: strPtr("origin/api"), State: strPtr(StackStatusUpstreamDiverged),
		Ahead: intPtr(1), Behind: intPtr(2), LocalOnly: true}

	legacy := entry("auth-legacy", "auth-legacy")
	legacy.RefExists = boolPtr(false)
	legacy.Heads.ParentShort = strPtr("4c3b2a1")
	legacy.Ancestry.Status = strPtr(string(AncestryStatusMissing))
	legacy.Ancestry.Reason = ReasonChildRefMissing
	legacy.Ancestry.Severity = SeverityWarning
	legacy.Materialization.State = StackStatusMaterializedMissing

	archived := entry("auth-archived", "auth-archived")
	archived.Archived = true
	archived.RefExists = boolPtr(true)
	archived.Ancestry.Status = strPtr(string(AncestryStatusStale))
	archived.Ancestry.Reason = ReasonBaseRecordUnresolvable
	archived.Ancestry.Severity = SeverityInfo
	archived.BaseRecord.State = strPtr(string(StackBaseRecordUnresolvable))
	archived.Materialization.State = StackStatusMaterializedArchived
	archived.Upstream = StackStatusUpstream{Configured: boolPtr(true), Ref: strPtr("refs/remotes/origin/archived"),
		Display: strPtr("origin/archived"), State: strPtr(StackStatusUpstreamBehind),
		Ahead: intPtr(0), Behind: intPtr(4), LocalOnly: true}

	wiki := entry("auth-wiki", "auth-wiki")
	wiki.Repo = strPtr("../wiki")
	wiki.Ancestry.Status = strPtr(string(AncestryStatusCrossRepo))
	wiki.Ancestry.Reason = ReasonCrossRepo
	wiki.Ancestry.Severity = SeverityInfo
	wiki.Materialization.State = StackStatusMaterializedCrossRepo

	orphan := entry("auth-orphan", "auth-orphan")
	orphan.Base = StackStatusBase{Kind: StackBaseNone}
	orphan.Ancestry.Reason = ReasonBaseUnset
	orphan.Ancestry.Severity = SeverityInfo
	orphan.Materialization.State = StackStatusMaterializedUnknown
	orphan.Upstream = StackStatusUpstream{Configured: boolPtr(true), Ref: strPtr("refs/remotes/origin/orphan"),
		Display: strPtr("origin/orphan"), State: strPtr(StackStatusUpstreamGone), LocalOnly: true}

	noups := entry("auth-noups", "auth-noups")
	noups.RefExists = boolPtr(true)
	noups.Ancestry.Status = strPtr(string(AncestryStatusCurrent))
	noups.Ancestry.Reason = ReasonParentContained
	noups.Materialization.State = StackStatusMaterializedPresent
	noups.Upstream = StackStatusUpstream{Configured: boolPtr(false), State: strPtr(StackStatusUpstreamNone), LocalOnly: true}

	entries := []StackStatusEntry{models, api, mirror, legacy, archived, wiki, orphan, noups}
	return &StackStatusReport{
		SchemaVersion: stackStatusSchema,
		Feature:       "auth",
		Workspace: StackStatusWorkspace{
			Mode:         ModeExternal,
			StableID:     strPtr("3f2a1b0c9d8e7f60"),
			MetadataRoot: "/w",
			Repository:   StackStatusRepository{Dir: strPtr("/r"), Source: StackRepoWorktree, Alternate: strPtr("/alt")},
			External:     &StackStatusExternal{WorktreesRoot: "/w/auth/worktrees"},
		},
		Entries: entries,
		Summary: stackStatusSummarize(entries),
	}
}

func TestStackStatus_HumanGrammar(t *testing.T) {
	r := ssGrammarReport()
	NormalizeStackStatus(r)
	got := FormatStackStatus(r)

	want := `Stack status: auth (mode: external)
  Workspace:  /w
  Repository: /r (source: worktree)
  Alternate:  /alt (repo-source-mismatch)
  Worktrees:  /w/auth/worktrees

BRANCH                     ANCESTRY                HEAD     PARENT   A/B  UPSTREAM                  MATERIALIZATION         FLAGS
auth-models                current                 1a2b3c4  1a2b3c4  0/0  equal:origin/models       present                 -
auth-api (git: jd/api)     stale                   9f8e7d6  4c3b2a1  3/1  ahead+2:origin/api        present                 dirty,op=rebase
      reason: parent-advanced last-base=7777777 merge-base=5c4b3a2
      run: tws sync auth
      note: base identity differs
      path: /w/auth/worktrees/auth-api
      checked-out: jd/api
auth-mirror (git: jd/api)  divergent               -        -        -    diverged+1-2:origin/api   prunable-missing        -
      reason: unrelated-histories
auth-legacy                missing                 -        4c3b2a1  -    ?                         missing                 ref-missing
      reason: child-ref-missing
auth-archived              stale                   -        -        -    behind-4:origin/archived  archived                archived
      reason: base-record-unresolvable base-record=unresolvable
auth-wiki                  cross-repo-unsupported  -        -        -    ?                         cross-repo-unsupported  cross-repo,ref?
      reason: cross-repo
auth-orphan                unevaluated             -        -        -    gone:origin/orphan        unknown                 ref?
      reason: base-unset
auth-noups                 current                 -        -        -    none                      present                 dirty?,op?

Summary:
  entries: 8
  ancestry: current=2 stale=2 divergent=1 missing=1 cross-repo-unsupported=1 unevaluated=1
  materialization: present=3 archived=1 missing=1 prunable-missing=1 cross-repo-unsupported=1 unknown=1
  upstream: none=1 equal=1 ahead=1 behind=1 diverged=1 gone=1 unknown=2
  unknown: ref-exists=2 parent-counts=6 dirty=6 active-op=6

Local-only report: no fetch was performed. Upstream and parent counts describe local refs only.
`
	if got != want {
		t.Fatalf("human output mismatch.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}

	lines := strings.Split(got, "\n")
	if lines[len(lines)-2] != "Local-only report: no fetch was performed. Upstream and parent counts describe local refs only." {
		t.Fatalf("the footer must be the final line, got %q", lines[len(lines)-2])
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "      ") || line == "" {
			continue
		}
		if !strings.Contains(line, "  ") {
			continue
		}
		for _, cell := range strings.Split(line, "  ") {
			cell = strings.TrimSpace(cell)
			if cell == "" || strings.Contains(cell, "(git: ") || strings.Contains(cell, "(source:") ||
				strings.Contains(cell, "(repo-source-mismatch)") || strings.HasPrefix(line, "Stack status:") ||
				strings.HasPrefix(line, "Summary:") || strings.HasPrefix(line, "  ") ||
				strings.HasPrefix(line, "Local-only") {
				continue
			}
			if strings.Contains(cell, " ") {
				t.Fatalf("only the BRANCH cell may carry a space, found %q in %q", cell, line)
			}
		}
	}
}

func TestStackStatus_HumanSanitization(t *testing.T) {
	longName := strings.Repeat("x", 300)
	entry := StackStatusEntry{
		Name:            "bad\x01name",
		GitBranch:       longName,
		Base:            StackStatusBase{Name: strPtr(longName), Kind: StackBaseLiteralRef, Ref: strPtr(longName)},
		Ancestry:        StackStatusAncestry{Status: strPtr(string(AncestryStatusCurrent)), Reason: ReasonParentContained, Severity: SeverityOK, Notes: []StackStatusNote{}},
		Materialization: StackStatusMaterialization{Kind: StackStatusKindWorktree, State: StackStatusMaterializedMissing},
		Upstream:        StackStatusUpstream{LocalOnly: true},
	}
	r := &StackStatusReport{
		SchemaVersion: stackStatusSchema,
		Feature:       "auth",
		Workspace: StackStatusWorkspace{Mode: ModeExternal, MetadataRoot: "/w",
			Repository: StackStatusRepository{Source: StackRepoUnavailable},
			External:   &StackStatusExternal{WorktreesRoot: "/w/auth/worktrees"}},
		Entries: []StackStatusEntry{entry},
	}
	r.Summary = stackStatusSummarize(r.Entries)
	NormalizeStackStatus(r)

	human := FormatStackStatus(r)
	if strings.Contains(human, "\x01") {
		t.Fatal("a control character must be replaced in human output")
	}
	if !strings.Contains(human, "bad?name") {
		t.Fatalf("the control character must render as ?: %s", human)
	}
	if !strings.Contains(human, "…") {
		t.Fatalf("an over-long value must be truncated with an ellipsis: %s", human)
	}
	if strings.Contains(human, strings.Repeat("x", 121)) {
		t.Fatal("human output must not exceed the sanitize limit")
	}
	if !strings.Contains(human, "unavailable (source: unavailable)") {
		t.Fatalf("a null repository dir renders unavailable: %s", human)
	}

	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), longName) {
		t.Fatal("JSON must carry the raw value verbatim")
	}
	if strings.Contains(string(raw), "\x01") {
		t.Fatal("the encoded JSON must contain no raw control byte")
	}
	if !strings.Contains(string(raw), `\u0001`) {
		t.Fatalf("the control character must be escaped, not dropped: %s", raw)
	}
}

func TestStackStatus_UnevaluatedVocabulary(t *testing.T) {
	entry := StackStatusEntry{
		Name:            "orphan",
		GitBranch:       "orphan",
		Base:            StackStatusBase{Kind: StackBaseNone},
		Ancestry:        StackStatusAncestry{Reason: ReasonBaseUnset, Severity: SeverityInfo, Notes: []StackStatusNote{}},
		Materialization: StackStatusMaterialization{Kind: StackStatusKindWorktree, State: StackStatusMaterializedUnknown},
		Upstream:        StackStatusUpstream{LocalOnly: true},
	}
	r := &StackStatusReport{
		SchemaVersion: stackStatusSchema,
		Feature:       "auth",
		Workspace: StackStatusWorkspace{Mode: ModeExternal, MetadataRoot: "/w",
			Repository: StackStatusRepository{Source: StackRepoUnavailable},
			External:   &StackStatusExternal{WorktreesRoot: "/w/auth/worktrees"}},
		Entries: []StackStatusEntry{entry},
	}
	r.Summary = stackStatusSummarize(r.Entries)
	NormalizeStackStatus(r)

	human := FormatStackStatus(r)
	if !strings.Contains(human, "unevaluated") {
		t.Fatalf("a null status renders unevaluated: %s", human)
	}
	if strings.Contains(human, "base-record=") {
		t.Fatalf("a null base_record.state prints no base-record token: %s", human)
	}
	if r.Summary.Ancestry.Unevaluated != 1 {
		t.Fatalf("summary = %+v", r.Summary.Ancestry)
	}
}
