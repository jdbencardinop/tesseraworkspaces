package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// AC 35 — pinned pre-change goldens for the existing commands
//
// This harness is written and captured against the unmodified production tree,
// before `internal/agent_status.go` and `internal/checkout_health.go` are
// touched. `internal/cli/testdata/existing_commands/**` is therefore
// pre-change evidence: a regeneration that alters a committed golden IS the
// regression this criterion exists to catch and must never be re-baselined.
// ---------------------------------------------------------------------------

// goldenRegenEnv gates regeneration of the committed pre-change goldens.
const goldenRegenEnv = "TWS_REGEN_EXISTING_GOLDENS"

// goldenFixedDate pins author and committer dates so every fixture commit has
// a byte-stable object ID across runs and machines. The existing helpers
// (setupGitRepoCheckout/gitInDir, setupGitRepo) pin only identity, not dates,
// so a golden built with them would rot on the next run.
const goldenFixedDate = "2020-01-01T00:00:00+00:00"

// goldenBuilder runs date-pinned Git commands for the AC 35 fixtures. It
// reproduces the package-cli setupGitRepoCheckout/gitInDir and setupGitRepo
// patterns (package-internal helpers are unreachable from package cli) and
// adds the fixed dates those helpers lack.
type goldenBuilder struct {
	t    *testing.T
	home string
}

func newGoldenBuilder(t *testing.T) *goldenBuilder {
	t.Helper()
	// Process-level neutralization: production Git calls set no cmd.Env and
	// inherit the test process, so a host GIT_CONFIG_COUNT injecting
	// safe.bareRepository=explicit would otherwise be baked into a permanent
	// golden. GIT_CONFIG_COUNT=0 makes every injected pair inert.
	t.Setenv("GIT_CONFIG_COUNT", "0")
	return &goldenBuilder{t: t, home: t.TempDir()}
}

func (b *goldenBuilder) env() []string {
	return append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=0",
		"HOME="+b.home,
		"GIT_AUTHOR_NAME=tws test",
		"GIT_AUTHOR_EMAIL=tws@example.test",
		"GIT_COMMITTER_NAME=tws test",
		"GIT_COMMITTER_EMAIL=tws@example.test",
		"GIT_AUTHOR_DATE="+goldenFixedDate,
		"GIT_COMMITTER_DATE="+goldenFixedDate,
	)
}

func (b *goldenBuilder) git(dir string, args ...string) string {
	b.t.Helper()
	out, err := b.tryGit(dir, args...)
	if err != nil {
		b.t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return out
}

// tryGit runs a Git command that is allowed to fail, such as the deliberately
// conflicting rebase of the rebase-in-progress fixture.
func (b *goldenBuilder) tryGit(dir string, args ...string) (string, error) {
	b.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = b.env()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func goldenWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------- fixture model ----------

type goldenStackEntry struct {
	name     string
	branch   string
	base     string
	repo     string
	archived bool
}

func goldenStackYAML(entries []goldenStackEntry) string {
	var b strings.Builder
	b.WriteString("branches:\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "  - name: %s\n", e.name)
		if e.branch != "" {
			fmt.Fprintf(&b, "    branch: %s\n", e.branch)
		}
		fmt.Fprintf(&b, "    base: %s\n", e.base)
		if e.repo != "" {
			fmt.Fprintf(&b, "    repo: %s\n", e.repo)
		}
		if e.archived {
			b.WriteString("    archived: true\n")
		}
	}
	return b.String()
}

// goldenFixture is one built repository plus every absolute path the
// normalizer must replace with a fixed token.
type goldenFixture struct {
	mode          internal.WorkspaceMode
	root          string
	repo          string
	remote        string
	metaRoot      string
	featurePath   string
	worktreesRoot string
	entries       []goldenStackEntry
	extra         map[string]string // absolute path -> token
}

func (fx *goldenFixture) addExtra(path, token string) {
	if fx.extra == nil {
		fx.extra = map[string]string{}
	}
	fx.extra[path] = token
}

const goldenFeature = "auth"

// goldenExternalBase builds the shared external-mode fixture: a real
// repository with a real local bare remote, two stacked branches, and two
// linked worktrees below the feature's worktrees directory.
func goldenExternalBase(b *goldenBuilder) *goldenFixture {
	t := b.t
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")

	b.git(root, "init", "--bare", "--initial-branch=main", remote)
	b.git(root, "init", "--initial-branch=main", repo)
	goldenWrite(t, filepath.Join(repo, "README.md"), "base\n")
	b.git(repo, "add", "README.md")
	b.git(repo, "commit", "-m", "initial")
	b.git(repo, "remote", "add", "origin", remote)
	b.git(repo, "push", "-u", "origin", "main")
	b.git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
	b.git(repo, "remote", "set-head", "origin", "-a")

	b.git(repo, "branch", "models", "main")
	b.git(repo, "branch", "api", "models")

	metaRoot := repo + ".tws"
	featurePath := filepath.Join(metaRoot, goldenFeature)
	worktreesRoot := filepath.Join(featurePath, "worktrees")
	if err := os.MkdirAll(filepath.Join(metaRoot, ".tws-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	b.git(repo, "worktree", "add", filepath.Join(worktreesRoot, "models"), "models")
	b.git(repo, "worktree", "add", filepath.Join(worktreesRoot, "api"), "api")

	fx := &goldenFixture{
		mode:          internal.ModeExternal,
		root:          root,
		repo:          repo,
		remote:        remote,
		metaRoot:      metaRoot,
		featurePath:   featurePath,
		worktreesRoot: worktreesRoot,
		entries: []goldenStackEntry{
			{name: "models", base: "main"},
			{name: "api", base: "models"},
		},
	}
	fx.addExtra(remote, "<REMOTE>")
	return fx
}

// goldenCheckoutBase builds the shared checkout-mode fixture: one physical
// checkout, repository-local .tws metadata, and two logical branches.
func goldenCheckoutBase(b *goldenBuilder) *goldenFixture {
	t := b.t
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")

	b.git(root, "init", "--bare", "--initial-branch=main", remote)
	b.git(root, "init", "--initial-branch=main", repo)
	goldenWrite(t, filepath.Join(repo, "README.md"), "base\n")
	b.git(repo, "add", "README.md")
	b.git(repo, "commit", "-m", "initial")
	b.git(repo, "remote", "add", "origin", remote)
	b.git(repo, "push", "-u", "origin", "main")
	b.git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
	b.git(repo, "remote", "set-head", "origin", "-a")

	b.git(repo, "branch", "models", "main")
	b.git(repo, "branch", "api", "models")

	metaRoot := filepath.Join(repo, ".tws")
	goldenWrite(t, filepath.Join(metaRoot, "config.yaml"), "workspace_mode: checkout\n")
	featurePath := filepath.Join(metaRoot, "features", goldenFeature)
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}

	fx := &goldenFixture{
		mode:        internal.ModeCheckout,
		root:        root,
		repo:        repo,
		remote:      remote,
		metaRoot:    metaRoot,
		featurePath: featurePath,
		entries: []goldenStackEntry{
			{name: "models", base: "main"},
			{name: "api", base: "models"},
		},
	}
	fx.addExtra(remote, "<REMOTE>")
	return fx
}

// goldenLinkedWorktreeDir is where checkout-mode fixtures place the real Git
// worktrees they need (prunable, locked). Checkout mode never creates them
// itself; they exist so the shared worktree inventory sees the same porcelain
// shapes in both modes.
func goldenLinkedWorktreeDir(fx *goldenFixture, name string) string {
	return filepath.Join(fx.root, "wt-"+name)
}

// goldenCase is one cell of the §11.4 rule 5 fixture matrix.
type goldenCase struct {
	name  string
	apply func(b *goldenBuilder, fx *goldenFixture)
}

// goldenPrimaryWorktree is the path whose state a mutation case changes: the
// api linked worktree in external mode, the single checkout otherwise.
func goldenPrimaryWorktree(fx *goldenFixture) string {
	if fx.mode == internal.ModeExternal {
		return filepath.Join(fx.worktreesRoot, "api")
	}
	return fx.repo
}

func goldenCases() []goldenCase {
	return []goldenCase{
		{name: "clean", apply: func(b *goldenBuilder, fx *goldenFixture) {}},
		{name: "dirty", apply: func(b *goldenBuilder, fx *goldenFixture) {
			goldenWrite(b.t, filepath.Join(goldenPrimaryWorktree(fx), "README.md"), "dirty\n")
		}},
		{name: "detached", apply: func(b *goldenBuilder, fx *goldenFixture) {
			b.git(goldenPrimaryWorktree(fx), "checkout", "--detach")
		}},
		{name: "rebase", apply: func(b *goldenBuilder, fx *goldenFixture) {
			goldenSetupConflictedRebase(b, fx)
		}},
		{name: "prunable", apply: func(b *goldenBuilder, fx *goldenFixture) {
			b.git(fx.repo, "branch", "gone", "main")
			path := goldenLinkedWorktreeDir(fx, "gone")
			if fx.mode == internal.ModeExternal {
				path = filepath.Join(fx.worktreesRoot, "gone")
				fx.entries = append(fx.entries, goldenStackEntry{name: "gone", base: "main"})
			}
			b.git(fx.repo, "worktree", "add", path, "gone")
			if err := os.RemoveAll(path); err != nil {
				b.t.Fatal(err)
			}
		}},
		{name: "locked", apply: func(b *goldenBuilder, fx *goldenFixture) {
			b.git(fx.repo, "branch", "lockedbr", "main")
			path := goldenLinkedWorktreeDir(fx, "locked")
			if fx.mode == internal.ModeExternal {
				path = filepath.Join(fx.worktreesRoot, "locked")
				fx.entries = append(fx.entries, goldenStackEntry{name: "locked", branch: "lockedbr", base: "main"})
			}
			b.git(fx.repo, "worktree", "add", path, "lockedbr")
			b.git(fx.repo, "worktree", "lock", "--reason", "busy testing", path)
		}},
		{name: "bare", apply: func(b *goldenBuilder, fx *goldenFixture) {
			// A bare main worktree with an in-tree .git directory: the
			// porcelain gains a `bare` block while MainRepoRootIn still
			// resolves the repository root.
			b.git(fx.repo, "config", "core.bare", "true")
		}},
		{name: "missing", apply: func(b *goldenBuilder, fx *goldenFixture) {
			if fx.mode == internal.ModeExternal {
				if err := os.RemoveAll(filepath.Join(fx.worktreesRoot, "api")); err != nil {
					b.t.Fatal(err)
				}
				b.git(fx.repo, "worktree", "prune")
				return
			}
			b.git(fx.repo, "branch", "-D", "api")
		}},
		{name: "duplicate-branch", apply: func(b *goldenBuilder, fx *goldenFixture) {
			fx.entries = append(fx.entries, goldenStackEntry{name: "api-mirror", branch: "api", base: "models"})
		}},
		{name: "archived", apply: func(b *goldenBuilder, fx *goldenFixture) {
			b.git(fx.repo, "branch", "old", "main")
			fx.entries = append(fx.entries, goldenStackEntry{name: "old", base: "main", archived: true})
		}},
		{name: "cross-repo", apply: func(b *goldenBuilder, fx *goldenFixture) {
			other := filepath.Join(fx.root, "other")
			b.git(fx.root, "init", "--initial-branch=main", other)
			goldenWrite(b.t, filepath.Join(other, "README.md"), "other\n")
			b.git(other, "add", "README.md")
			b.git(other, "commit", "-m", "other initial")
			fx.addExtra(other, "<OTHER>")
			fx.entries = append(fx.entries, goldenStackEntry{name: "wiki", base: "main", repo: other})
		}},
		{name: "repo-unavailable", apply: func(b *goldenBuilder, fx *goldenFixture) {
			// An entry name that escapes the feature worktrees directory makes
			// the shared repository resolution report `unavailable` instead of
			// probing anything, without touching the workspace itself.
			fx.entries = append(fx.entries, goldenStackEntry{name: "../escape", base: "main"})
		}},
	}
}

// goldenSetupConflictedRebase leaves a real interrupted rebase behind, so the
// active-Git-operation marker lives in a per-worktree Git directory in
// external mode and in the common directory in checkout mode.
func goldenSetupConflictedRebase(b *goldenBuilder, fx *goldenFixture) {
	b.t.Helper()
	if fx.mode == internal.ModeExternal {
		models := filepath.Join(fx.worktreesRoot, "models")
		api := filepath.Join(fx.worktreesRoot, "api")
		goldenWrite(b.t, filepath.Join(models, "conflict.txt"), "models\n")
		b.git(models, "add", "conflict.txt")
		b.git(models, "commit", "-m", "models change")
		goldenWrite(b.t, filepath.Join(api, "conflict.txt"), "api\n")
		b.git(api, "add", "conflict.txt")
		b.git(api, "commit", "-m", "api change")
		if _, err := b.tryGit(api, "rebase", "models"); err == nil {
			b.t.Fatal("the rebase fixture must conflict")
		}
		return
	}
	b.git(fx.repo, "checkout", "models")
	goldenWrite(b.t, filepath.Join(fx.repo, "conflict.txt"), "models\n")
	b.git(fx.repo, "add", "conflict.txt")
	b.git(fx.repo, "commit", "-m", "models change")
	b.git(fx.repo, "checkout", "api")
	goldenWrite(b.t, filepath.Join(fx.repo, "conflict.txt"), "api\n")
	b.git(fx.repo, "add", "conflict.txt")
	b.git(fx.repo, "commit", "-m", "api change")
	if _, err := b.tryGit(fx.repo, "rebase", "models"); err == nil {
		b.t.Fatal("the rebase fixture must conflict")
	}
}

func goldenBuildFixture(t *testing.T, mode internal.WorkspaceMode, c goldenCase) *goldenFixture {
	t.Helper()
	b := newGoldenBuilder(t)
	var fx *goldenFixture
	if mode == internal.ModeExternal {
		fx = goldenExternalBase(b)
	} else {
		fx = goldenCheckoutBase(b)
	}
	c.apply(b, fx)
	goldenWrite(t, filepath.Join(fx.featurePath, "stack.yaml"), goldenStackYAML(fx.entries))
	return fx
}

// ---------- fixture environment ----------

// stackStatusGoldenEnv installs the pinned fixture environment used
// identically at pre-change capture and at post-change assertion, and returns
// the resolved workspace whose exported StableID the normalizer replaces.
func stackStatusGoldenEnv(t *testing.T, repo string) internal.Workspace {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	withIdleTmuxOnPath(t)
	_ = withUnifiedWorkspaceEnv(t, repo)
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatalf("fixture workspace must resolve: %v", err)
	}
	if ws.StableID == "" {
		t.Fatalf("fixture workspace has no stable ID: %+v", ws)
	}
	return ws
}

// ---------- normalization ----------

type goldenReplacement struct {
	from string
	to   string
}

func goldenReplacements(t *testing.T, fx *goldenFixture, ws internal.Workspace) []goldenReplacement {
	t.Helper()
	pairs := map[string]string{}
	add := func(path, token string) {
		if path == "" {
			return
		}
		pairs[path] = token
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			pairs[resolved] = token
		}
	}
	if fx.worktreesRoot != "" {
		add(fx.worktreesRoot, "<WORKTREES>")
	}
	add(fx.metaRoot, "<META>")
	add(ws.MetadataRoot, "<META>")
	add(fx.repo, "<REPO>")
	add(ws.RepoRoot, "<REPO>")
	for path, token := range fx.extra {
		add(path, token)
	}
	add(fx.root, "<ROOT>")
	add(os.Getenv("HOME"), "<HOME>")
	add(os.Getenv("XDG_DATA_HOME"), "<XDG_DATA>")

	out := make([]goldenReplacement, 0, len(pairs))
	for from, to := range pairs {
		out = append(out, goldenReplacement{from: from, to: to})
	}
	// Longest first, so a nested root normalizes deterministically and a
	// shorter prefix can never corrupt a longer path.
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].from) != len(out[j].from) {
			return len(out[i].from) > len(out[j].from)
		}
		return out[i].from < out[j].from
	})
	return out
}

func goldenApplyReplacements(reps []goldenReplacement, stableID, s string) string {
	for _, r := range reps {
		s = strings.ReplaceAll(s, r.from, r.to)
	}
	// Exactly one stable-ID rule, by literal match on the value production
	// derived from the canonical repository root. A [0-9a-f]{16} regex is
	// forbidden: it would also rewrite abbreviated commit SHAs, which the
	// date-pinned fixtures exist to compare verbatim.
	if stableID != "" {
		s = strings.ReplaceAll(s, stableID, "<STABLE_ID>")
	}
	return s
}

func goldenAssertNoResidual(t *testing.T, label, stableID, s string) {
	t.Helper()
	for _, tempRoot := range goldenTempRoots() {
		if strings.Contains(s, tempRoot) {
			t.Fatalf("%s still contains an unnormalized temporary path rooted at %s:\n%s", label, tempRoot, s)
		}
	}
	if stableID != "" && strings.Contains(s, stableID) {
		t.Fatalf("%s still contains the fixture stable ID %s:\n%s", label, stableID, s)
	}
}

func goldenTempRoots() []string {
	roots := []string{filepath.Clean(os.TempDir())}
	if resolved, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		roots = append(roots, filepath.Clean(resolved))
	}
	return roots
}

func goldenNormalizeText(t *testing.T, label string, reps []goldenReplacement, stableID, s string) string {
	t.Helper()
	out := goldenApplyReplacements(reps, stableID, s)
	goldenAssertNoResidual(t, label, stableID, out)
	return out
}

// goldenNormalizeJSON decodes the document, asserts generated_at is present
// and deletes it — the only key removed — then path- and stable-ID-normalizes
// every remaining value and re-encodes. Raw byte equality of a wall-clock
// document is impossible and is not claimed.
func goldenNormalizeJSON(t *testing.T, label string, reps []goldenReplacement, stableID, raw string) string {
	t.Helper()
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", label, err, raw)
	}
	if _, ok := doc["generated_at"]; !ok {
		t.Fatalf("%s must carry generated_at before normalization", label)
	}
	delete(doc, "generated_at")

	normalized := goldenNormalizeValue(reps, stableID, doc)
	encoded, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out := string(encoded) + "\n"
	goldenAssertNoResidual(t, label, stableID, out)
	return out
}

func goldenNormalizeValue(reps []goldenReplacement, stableID string, v any) any {
	switch typed := v.(type) {
	case map[string]any:
		for k, inner := range typed {
			typed[k] = goldenNormalizeValue(reps, stableID, inner)
		}
		return typed
	case []any:
		for i, inner := range typed {
			typed[i] = goldenNormalizeValue(reps, stableID, inner)
		}
		return typed
	case string:
		return goldenApplyReplacements(reps, stableID, typed)
	default:
		return v
	}
}

// ---------- surface capture ----------

func goldenExitCode(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

// goldenRunStatus captures `tws status` through cmd.OutOrStdout(), which is
// where statusCmd writes.
func goldenRunStatus(t *testing.T, args ...string) (string, int) {
	t.Helper()
	out, _, err := runStatus(t, args...)
	return out, goldenExitCode(err)
}

// goldenRunStdoutCommand captures a command that prints with bare fmt to the
// process stdout, which is what `tws doctor` and `tws list` do.
func goldenRunStdoutCommand(t *testing.T, build func() *cobra.Command, args ...string) (string, int) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		cmd := build()
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	return out, goldenExitCode(err)
}

// goldenPkgDir is the package directory captured at init, before any fixture
// chdirs. Golden paths must not follow the working directory into a fixture.
var goldenPkgDir = func() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}()

func goldenGoldenPath(fixture, surface string) string {
	return filepath.Join(goldenPkgDir, "testdata", "existing_commands", fixture, surface)
}

func goldenCompareOrWrite(t *testing.T, fixture, surface, body string, exit int) {
	t.Helper()
	artifact := fmt.Sprintf("exit: %d\n%s", exit, body)
	path := goldenGoldenPath(fixture, surface)
	if os.Getenv(goldenRegenEnv) == "1" {
		goldenWrite(t, path, artifact)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing pinned golden %s: %v (regenerate with %s=1 only against the pre-change tree)", path, err, goldenRegenEnv)
	}
	if string(want) != artifact {
		t.Fatalf("golden %s changed.\n--- want ---\n%s\n--- got ---\n%s", path, want, artifact)
	}
}

// TestStackStatus_ExistingCommandsUnchanged is the surface half of AC 35. It
// runs `tws status`, `tws status --json`, `tws doctor`, and `tws list` over
// the whole §11.4 rule 5 fixture matrix and compares them against goldens
// captured at the parent commit, before any production edit.
func TestStackStatus_ExistingCommandsUnchanged(t *testing.T) {
	modes := []struct {
		label string
		mode  internal.WorkspaceMode
	}{
		{label: "external", mode: internal.ModeExternal},
		{label: "checkout", mode: internal.ModeCheckout},
	}
	for _, m := range modes {
		for _, c := range goldenCases() {
			fixture := m.label + "-" + c.name
			t.Run(fixture, func(t *testing.T) {
				fx := goldenBuildFixture(t, m.mode, c)
				ws := stackStatusGoldenEnv(t, fx.repo)
				reps := goldenReplacements(t, fx, ws)

				humanStatus, statusExit := goldenRunStatus(t)
				goldenCompareOrWrite(t, fixture, "status.txt",
					goldenNormalizeText(t, fixture+"/status.txt", reps, ws.StableID, humanStatus), statusExit)

				jsonStatus, jsonExit := goldenRunStatus(t, "--json")
				goldenCompareOrWrite(t, fixture, "status.json",
					goldenNormalizeJSON(t, fixture+"/status.json", reps, ws.StableID, jsonStatus), jsonExit)

				doctorOut, doctorExit := goldenRunStdoutCommand(t, doctorCmd)
				goldenCompareOrWrite(t, fixture, "doctor.txt",
					goldenNormalizeText(t, fixture+"/doctor.txt", reps, ws.StableID, doctorOut), doctorExit)

				listOut, listExit := goldenRunStdoutCommand(t, listCmd)
				goldenCompareOrWrite(t, fixture, "list.txt",
					goldenNormalizeText(t, fixture+"/list.txt", reps, ws.StableID, listOut), listExit)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Command surface fixtures
// ---------------------------------------------------------------------------

type stackStatusFixture struct {
	t             *testing.T
	root          string
	repo          string
	metaRoot      string
	worktreesRoot string
}

// newStackStatusCLIFixture builds a real external workspace with a real local
// bare remote and installs the workspace environment the command resolves.
func newStackStatusCLIFixture(t *testing.T, feature string) *stackStatusFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	b := newGoldenBuilder(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	b.git(root, "init", "--bare", "--initial-branch=main", remote)
	b.git(root, "init", "--initial-branch=main", repo)
	goldenWrite(t, filepath.Join(repo, "README.md"), "base\n")
	b.git(repo, "add", "README.md")
	b.git(repo, "commit", "-m", "initial")
	b.git(repo, "remote", "add", "origin", remote)
	b.git(repo, "push", "-u", "origin", "main")
	b.git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
	b.git(repo, "remote", "set-head", "origin", "-a")
	b.git(repo, "branch", "models", "main")
	b.git(repo, "branch", "api", "models")

	metaRoot := repo + ".tws"
	featurePath := filepath.Join(metaRoot, feature)
	worktreesRoot := filepath.Join(featurePath, "worktrees")
	if err := os.MkdirAll(worktreesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	b.git(repo, "worktree", "add", filepath.Join(worktreesRoot, "models"), "models")
	b.git(repo, "worktree", "add", filepath.Join(worktreesRoot, "api"), "api")
	goldenWrite(t, filepath.Join(featurePath, "stack.yaml"),
		"branches:\n  - name: models\n    base: main\n  - name: api\n    base: models\n")

	fx := &stackStatusFixture{t: t, root: root, repo: repo, metaRoot: metaRoot, worktreesRoot: worktreesRoot}
	withIdleTmuxOnPath(t)
	_ = withUnifiedWorkspaceEnv(t, repo)
	return fx
}

// stackStatusAddFeature writes a second feature's stack.yaml.
func stackStatusAddFeature(t *testing.T, metaRoot, feature, body string) string {
	t.Helper()
	path := filepath.Join(metaRoot, feature)
	if err := os.MkdirAll(filepath.Join(path, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	goldenWrite(t, filepath.Join(path, "stack.yaml"), body)
	return path
}

// runStackStatus executes the child command through Cobra writers and reports
// stdout, stderr, and the error.
func runStackStatus(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := stackStatusCmd()
	var out, errOut bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// runStackParent executes `tws stack ...` through the parent command, which is
// where the legacy tree and the child routing both live.
func runStackParent(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := stackCmd()
	var out, errOut bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// ---------------------------------------------------------------------------
// Command surface and legacy preservation (AC 1-8)
// ---------------------------------------------------------------------------

func TestStackStatus_LegacyTreeUnchanged(t *testing.T) {
	fx := newStackStatusCLIFixture(t, "auth")

	legacy := captureStdout(t, func() {
		cmd := stackCmd()
		cmd.SetArgs([]string{"auth"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("legacy tree must exit 0, got %v", err)
		}
	})
	want := "(main)\n└── models\n    └── api\n"
	if legacy != want {
		t.Fatalf("legacy tree bytes changed.\nwant %q\ngot  %q", want, legacy)
	}

	stackStatusAddFeature(t, fx.metaRoot, "cyc",
		"branches:\n  - name: a\n    base: b\n  - name: b\n    base: a\n")
	cycle := captureStdout(t, func() {
		cmd := stackCmd()
		cmd.SetArgs([]string{"cyc"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("a cycle still exits 0, got %v", err)
		}
	})
	if !strings.Contains(cycle, "Warning: cycle detected in stack.yaml\n") {
		t.Fatalf("cycle warning missing: %q", cycle)
	}

	if err := os.MkdirAll(filepath.Join(fx.metaRoot, "nostack"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := runStackParent(t, "nostack")
	if err == nil || err.Error() != "no stack.yaml found for feature: nostack" {
		t.Fatalf("err = %v", err)
	}
}

func TestStackStatus_HelpDrift(t *testing.T) {
	_ = newStackStatusCLIFixture(t, "auth")

	out, _, err := runStackParent(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Available Commands:") || !strings.Contains(out, "status") {
		t.Fatalf("the accepted help drift is missing:\n%s", out)
	}
	if !strings.Contains(out, "stack [command]") {
		t.Fatalf("the usage line must gain the [command] form:\n%s", out)
	}

	// In-process, Cobra routes the usage block through the out writer and the
	// error line through the err writer; in production both reach stderr.
	usageOut, errOut, err := runStackParent(t, "a", "b")
	if err == nil || err.Error() != "accepts 1 arg(s), received 2" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(usageOut+errOut, "Usage:") {
		t.Fatalf("an arity error keeps its usage block: %q / %q", usageOut, errOut)
	}
	if !strings.Contains(errOut, "accepts 1 arg(s), received 2") {
		t.Fatalf("the error line must reach the err writer: %q", errOut)
	}
}

func TestStackStatus_ZeroArgHint_NoStatusFeature(t *testing.T) {
	_ = newStackStatusCLIFixture(t, "auth")
	out, errOut, err := runStackStatus(t)
	if err == nil || err.Error() != "accepts 1 arg(s), received 0" {
		t.Fatalf("err = %v", err)
	}
	if out != "" && !strings.Contains(out, "Usage:") {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(out+errOut, "Usage:") {
		t.Fatalf("an arity error keeps its usage block: %q / %q", out, errOut)
	}
}

func TestStackStatus_ZeroArgHint_StatusFeatureExists(t *testing.T) {
	fx := newStackStatusCLIFixture(t, "auth")
	stackStatusAddFeature(t, fx.metaRoot, "status", "branches:\n  - name: models\n    base: main\n")

	_, _, err := runStackStatus(t)
	if err == nil {
		t.Fatal("a zero-argument invocation must fail")
	}
	for _, want := range []string{"accepts 1 arg(s), received 0", `tws stack status status`, `tws stack -- status`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("hint %q missing from %q", want, err.Error())
		}
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("the hint must be a single line: %q", err.Error())
	}
}

func TestStackStatus_LiteralStatusFeature(t *testing.T) {
	fx := newStackStatusCLIFixture(t, "auth")
	stackStatusAddFeature(t, fx.metaRoot, "status", "branches:\n  - name: models\n    base: main\n")

	tree := captureStdout(t, func() {
		cmd := stackCmd()
		cmd.SetArgs([]string{"--", "status"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("the escape hatch must exit 0, got %v", err)
		}
	})
	if tree != "(main)\n└── models\n" {
		t.Fatalf("legacy tree for the literal feature = %q", tree)
	}

	out, _, err := runStackParent(t, "status", "status")
	if err != nil {
		t.Fatalf("tws stack status status must exit 0, got %v", err)
	}
	if !strings.Contains(out, "Stack status: status (mode: external)") {
		t.Fatalf("report = %q", out)
	}
}

func TestStackStatus_Completion(t *testing.T) {
	fx := newStackStatusCLIFixture(t, "auth")
	stackStatusAddFeature(t, fx.metaRoot, "status", "branches:\n  - name: models\n    base: main\n")

	parent := stackCmd()
	names, directive := parent.ValidArgsFunction(parent, nil, "")
	statusCount := 0
	for _, name := range names {
		if name == "status" {
			statusCount++
		}
	}
	if statusCount != 0 {
		t.Fatalf("the parent must not add a second `status` candidate: %v", names)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive = %v", directive)
	}
	if len(names) == 0 {
		t.Fatalf("the parent still completes other features: %v", names)
	}

	child := stackStatusCmd()
	childNames, childDirective := child.ValidArgsFunction(child, nil, "")
	found := false
	for _, name := range childNames {
		if name == "status" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the child keeps `status` discoverable: %v", childNames)
	}
	if childDirective != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("child directive = %v", childDirective)
	}
	if extra, d := child.ValidArgsFunction(child, []string{"auth"}, ""); extra != nil || d != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("the child completes only the first argument: %v / %v", extra, d)
	}

	// Cobra itself contributes exactly one `status` candidate at the parent
	// position, so the aggregate stays unambiguous.
	root := &cobra.Command{Use: "tws"}
	root.AddCommand(stackCmd())
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"__complete", "stack", ""})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	aggregate := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Fields(line) != nil && strings.HasPrefix(line, "status") {
			aggregate++
		}
	}
	if aggregate != 1 {
		t.Fatalf("exactly one `status` candidate must be offered:\n%s", buf.String())
	}
}

func TestStackStatus_Writers(t *testing.T) {
	_ = newStackStatusCLIFixture(t, "auth")

	var human, jsonOut string
	leaked := captureStdout(t, func() {
		human, _, _ = runStackStatus(t, "auth")
		jsonOut, _, _ = runStackStatus(t, "auth", "--json")
	})
	if leaked != "" {
		t.Fatalf("no byte of the report may reach process stdout: %q", leaked)
	}
	if !strings.HasPrefix(human, "Stack status: auth") {
		t.Fatalf("human report = %q", human)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatalf("json report is invalid: %v\n%s", err, jsonOut)
	}
	if doc["schema_version"].(float64) != 1 {
		t.Fatalf("schema_version = %v", doc["schema_version"])
	}
}

func TestStackStatus_SilenceUsage(t *testing.T) {
	_ = newStackStatusCLIFixture(t, "auth")

	out, errOut, err := runStackStatus(t, "no-such-feature")
	if err == nil || err.Error() != "feature not found: no-such-feature" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out+errOut, "Usage:") {
		t.Fatalf("a runtime failure prints no usage block: %q / %q", out, errOut)
	}

	out, errOut, err = runStackStatus(t, "a", "b")
	if err == nil {
		t.Fatal("an arity error must fail")
	}
	if !strings.Contains(out+errOut, "Usage:") {
		t.Fatalf("an arity error keeps its usage block: %q / %q", out, errOut)
	}
}

// ---------------------------------------------------------------------------
// Fatal boundary and exit semantics (AC 9, 10)
// ---------------------------------------------------------------------------

func TestStackStatus_FatalEmptyStdout(t *testing.T) {
	fx := newStackStatusCLIFixture(t, "auth")

	if err := os.MkdirAll(filepath.Join(fx.metaRoot, "nostack"), 0o755); err != nil {
		t.Fatal(err)
	}
	unreadable := stackStatusAddFeature(t, fx.metaRoot, "unreadable", "branches: []\n")
	if err := os.Chmod(filepath.Join(unreadable, "stack.yaml"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(unreadable, "stack.yaml"), 0o644) })
	stackStatusAddFeature(t, fx.metaRoot, "badyaml", "branches: [\n")
	stackStatusAddFeature(t, fx.metaRoot, "dupname", "branches:\n  - name: a\n    base: main\n  - name: a\n    base: main\n")
	stackStatusAddFeature(t, fx.metaRoot, "emptyname", "branches:\n  - name: \"\"\n    base: main\n")
	writeSpaces(t, fx.metaRoot, registeredLearningFixture("notes"))

	cases := []struct {
		name    string
		feature string
		want    string
		prefix  bool
	}{
		{name: "missing directory", feature: "ghost", want: "feature not found: ghost"},
		{name: "unsafe name", feature: "../evil", prefix: true, want: `feature name "../evil"`},
		{name: "space owned name", feature: "notes", prefix: true, want: "notes"},
		{name: "missing stack", feature: "nostack", want: "no stack.yaml found for feature: nostack"},
		{name: "unreadable stack", feature: "unreadable", prefix: true, want: "stack.yaml unreadable for feature unreadable: "},
		{name: "invalid yaml", feature: "badyaml", prefix: true, want: "stack.yaml invalid for feature badyaml: "},
		{name: "duplicate entry name", feature: "dupname", want: "invalid stack.yaml for feature dupname: duplicate entry name a"},
		{name: "empty entry name", feature: "emptyname", want: "invalid stack.yaml for feature emptyname: entry 0: empty name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if os.Geteuid() == 0 && tc.feature == "unreadable" {
				t.Skip("root can read a 0000 file")
			}
			for _, args := range [][]string{{tc.feature}, {tc.feature, "--json"}} {
				out, _, err := runStackStatus(t, args...)
				if err == nil {
					t.Fatalf("%v must fail", args)
				}
				if out != "" {
					t.Fatalf("%v wrote %q to stdout; a fatal failure writes nothing", args, out)
				}
				if tc.prefix {
					if !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("err = %q, want it to contain %q", err.Error(), tc.want)
					}
					continue
				}
				if err.Error() != tc.want {
					t.Fatalf("err = %q, want %q", err.Error(), tc.want)
				}
			}
		})
	}
}

func TestStackStatus_ReportableExitZero(t *testing.T) {
	fx := newStackStatusCLIFixture(t, "auth")
	b := newGoldenBuilder(t)

	other := filepath.Join(fx.root, "other")
	b.git(fx.root, "init", "--initial-branch=main", other)
	goldenWrite(t, filepath.Join(other, "README.md"), "other\n")
	b.git(other, "add", "README.md")
	b.git(other, "commit", "-m", "other initial")

	b.git(fx.repo, "branch", "gonebr", "main")
	gonePath := filepath.Join(fx.worktreesRoot, "gonebr")
	b.git(fx.repo, "worktree", "add", gonePath, "gonebr")
	if err := os.RemoveAll(gonePath); err != nil {
		t.Fatal(err)
	}
	// Advance the parent so the child edge is stale, then leave an
	// uncommitted change so the same worktree is dirty.
	models := filepath.Join(fx.worktreesRoot, "models")
	goldenWrite(t, filepath.Join(models, "advance.txt"), "advance\n")
	b.git(models, "add", "advance.txt")
	b.git(models, "commit", "-m", "models advance")
	goldenWrite(t, filepath.Join(models, "dirty.txt"), "dirty\n")
	goldenWrite(t, filepath.Join(fx.metaRoot, "auth", "stack.yaml"),
		"branches:\n"+
			"  - name: models\n    base: main\n"+
			"  - name: api\n    base: models\n"+
			"  - name: gonebr\n    base: main\n"+
			"  - name: missingref\n    base: main\n"+
			"  - name: baseless\n"+
			"  - name: wiki\n    base: main\n    repo: "+other+"\n")

	out, _, err := runStackStatus(t, "auth", "--json")
	if err != nil {
		t.Fatalf("every reportable state exits 0, got %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	states := map[string]bool{}
	for _, raw := range doc["entries"].([]any) {
		e := raw.(map[string]any)
		if s, ok := e["ancestry"].(map[string]any)["status"].(string); ok {
			states[s] = true
		} else {
			states["unevaluated"] = true
		}
		states["m:"+e["materialization"].(map[string]any)["state"].(string)] = true
	}
	for _, want := range []string{"stale", "missing", "cross-repo-unsupported", "unevaluated",
		"m:present", "m:missing", "m:prunable-missing", "m:cross-repo-unsupported"} {
		if !states[want] {
			t.Fatalf("the fixture must produce %q, got %v", want, states)
		}
	}
}

// ---------------------------------------------------------------------------
// Read-only proof (AC 34)
// ---------------------------------------------------------------------------

type stackStatusSnapshot struct {
	refs     map[string]string
	files    map[string]string
	indexes  map[string]string
	trees    map[string]string
	markers  map[string]bool
	packed   map[string]string
	fetchHed map[string]string
}

func stackStatusGitDirs(t *testing.T, repo string, worktrees []string) map[string]string {
	t.Helper()
	dirs := map[string]string{repo: filepath.Join(repo, ".git")}
	for _, wt := range worktrees {
		data, err := os.ReadFile(filepath.Join(wt, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		target := strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir: ")
		if !filepath.IsAbs(target) {
			target = filepath.Join(wt, target)
		}
		dirs[wt] = filepath.Clean(target)
	}
	return dirs
}

func stackStatusFileState(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		return "absent"
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return fmt.Sprintf("mode=%s size=%d mtime=%d sha=%x", info.Mode(), info.Size(),
		info.ModTime().UnixNano(), sha256.Sum256(data))
}

func stackStatusTakeSnapshot(t *testing.T, b *goldenBuilder, repo, metaRoot string, worktrees []string) stackStatusSnapshot {
	t.Helper()
	snap := stackStatusSnapshot{
		refs: map[string]string{}, files: map[string]string{}, indexes: map[string]string{},
		trees: map[string]string{}, markers: map[string]bool{}, packed: map[string]string{},
		fetchHed: map[string]string{},
	}
	targets := append([]string{repo}, worktrees...)
	for _, dir := range targets {
		snap.refs[dir+"|all"] = b.git(dir, "rev-parse", "--all")
		snap.refs[dir+"|refs"] = b.git(dir, "for-each-ref", "refs/heads", "refs/remotes", "refs/tags")
		out, _ := b.tryGit(dir, "reflog", "--all")
		snap.refs[dir+"|reflog"] = out
		snap.trees[dir] = snapshotTreeIgnoringGitLocks(t, dir)
	}
	snap.trees[metaRoot] = snapshotTreeIgnoringGitLocks(t, metaRoot)

	for label, gitDir := range stackStatusGitDirs(t, repo, worktrees) {
		snap.indexes[label] = stackStatusFileState(t, filepath.Join(gitDir, "index"))
		snap.packed[label] = stackStatusFileState(t, filepath.Join(gitDir, "packed-refs"))
		snap.fetchHed[label] = stackStatusFileState(t, filepath.Join(gitDir, "FETCH_HEAD"))
		for _, marker := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG"} {
			_, err := os.Stat(filepath.Join(gitDir, marker))
			snap.markers[label+"|"+marker] = err == nil
		}
	}
	snap.files[metaRoot] = stackStatusFileState(t, filepath.Join(metaRoot, "auth", "stack.yaml"))
	// The common packed-refs and FETCH_HEAD of the main repository are also
	// compared through the main entry above.
	return snap
}

func stackStatusDiffSnapshots(t *testing.T, label string, before, after stackStatusSnapshot) {
	t.Helper()
	cmp := func(kind string, a, b map[string]string) {
		for key, want := range a {
			if b[key] != want {
				t.Fatalf("%s: %s[%s] changed:\nbefore %s\nafter  %s", label, kind, key, want, b[key])
			}
		}
		if len(a) != len(b) {
			t.Fatalf("%s: %s key set changed", label, kind)
		}
	}
	cmp("refs", before.refs, after.refs)
	cmp("files", before.files, after.files)
	cmp("indexes", before.indexes, after.indexes)
	cmp("trees", before.trees, after.trees)
	cmp("packed-refs", before.packed, after.packed)
	cmp("FETCH_HEAD", before.fetchHed, after.fetchHed)
	for key, want := range before.markers {
		if after.markers[key] != want {
			t.Fatalf("%s: marker %s changed", label, key)
		}
	}
}

func TestStackStatus_ReadOnlySnapshots(t *testing.T) {
	fx := newStackStatusCLIFixture(t, "auth")
	b := newGoldenBuilder(t)
	worktrees := []string{
		filepath.Join(fx.worktreesRoot, "models"),
		filepath.Join(fx.worktreesRoot, "api"),
	}

	for _, args := range [][]string{{"auth"}, {"auth", "--json"}} {
		before := stackStatusTakeSnapshot(t, b, fx.repo, fx.metaRoot, worktrees)
		if _, _, err := runStackStatus(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		after := stackStatusTakeSnapshot(t, b, fx.repo, fx.metaRoot, worktrees)
		stackStatusDiffSnapshots(t, strings.Join(args, " "), before, after)
	}

	// Control (a): the index mtime comparison really is sensitive — a status
	// probe without GIT_OPTIONAL_LOCKS=0 does change it.
	gitDir := filepath.Join(fx.repo, ".git")
	goldenWrite(t, filepath.Join(fx.repo, "touch.txt"), "touch\n")
	b.git(fx.repo, "add", "touch.txt")
	b.git(fx.repo, "commit", "-m", "touched")
	if err := os.Chtimes(filepath.Join(fx.repo, "touch.txt"), time.Now().Add(2*time.Second), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	beforeIndex := stackStatusFileState(t, filepath.Join(gitDir, "index"))
	unlocked := exec.Command("git", "-C", fx.repo, "status", "--porcelain")
	unlocked.Env = b.env()
	if out, err := unlocked.CombinedOutput(); err != nil {
		t.Fatalf("control status run failed: %v\n%s", err, out)
	}
	if stackStatusFileState(t, filepath.Join(gitDir, "index")) == beforeIndex {
		t.Skip("this filesystem does not refresh the index on a plain git status; the mtime control is inconclusive")
	}

	// Control (b): a non-lock file written under .git IS detected.
	before := stackStatusTakeSnapshot(t, b, fx.repo, fx.metaRoot, worktrees)
	goldenWrite(t, filepath.Join(gitDir, "tws", "probe"), "probe\n")
	after := stackStatusTakeSnapshot(t, b, fx.repo, fx.metaRoot, worktrees)
	if before.trees[fx.repo] == after.trees[fx.repo] {
		t.Fatal("a non-lock file under .git must be detected by the traversal")
	}
	if err := os.RemoveAll(filepath.Join(gitDir, "tws")); err != nil {
		t.Fatal(err)
	}

	// Control (c): a transient .git lock file is NOT detected.
	before = stackStatusTakeSnapshot(t, b, fx.repo, fx.metaRoot, worktrees)
	lock := filepath.Join(gitDir, "objects", "maintenance.lock")
	goldenWrite(t, lock, "")
	mid := stackStatusTakeSnapshot(t, b, fx.repo, fx.metaRoot, worktrees)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if before.trees[fx.repo] != mid.trees[fx.repo] {
		t.Fatal("a transient .git lock file must be excluded during traversal")
	}
}

// ---------------------------------------------------------------------------
// End-to-end process budget (AC 36 rule 5)
// ---------------------------------------------------------------------------

type cliInvocation struct {
	pwd   string
	locks string
	args  []string
}

func (i cliInvocation) key() string { return i.pwd + "\x00" + strings.Join(i.args, "\x00") }

type cliRecorder struct {
	t    *testing.T
	path string
}

func installCLIGitRecorder(t *testing.T) *cliRecorder {
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
	return &cliRecorder{t: t, path: record}
}

func (r *cliRecorder) reset() {
	r.t.Helper()
	if err := os.WriteFile(r.path, nil, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *cliRecorder) invocations() []cliInvocation {
	r.t.Helper()
	data, err := os.ReadFile(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []cliInvocation
	cur := cliInvocation{}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "end":
			out = append(out, cur)
			cur = cliInvocation{}
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

func cliHasArg(inv cliInvocation, want string) bool {
	for _, a := range inv.args {
		if a == want {
			return true
		}
	}
	return false
}

var cliFullSHAPair = regexp.MustCompile(`^[0-9a-f]{40}\.\.\.[0-9a-f]{40}$`)

func TestStackStatus_ProcessBudgetEndToEnd(t *testing.T) {
	_ = newStackStatusCLIFixture(t, "auth")
	rec := installCLIGitRecorder(t)

	// I: the pre-builder infrastructure prefix, measured by a control run.
	rec.reset()
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	cfg := internal.LoadConfig()
	infra := rec.invocations()
	showToplevel, gitCommonDir := 0, 0
	for _, inv := range infra {
		switch {
		case cliHasArg(inv, "rev-parse") && cliHasArg(inv, "--show-toplevel"):
			showToplevel++
		case cliHasArg(inv, "rev-parse") && cliHasArg(inv, "--git-common-dir"):
			gitCommonDir++
		default:
			t.Fatalf("the CLI prefix must start no other process: git %s", strings.Join(inv.args, " "))
		}
	}
	if showToplevel != 2 || gitCommonDir != 1 {
		t.Fatalf("I = %d show-toplevel + %d git-common-dir, want 2 + 1", showToplevel, gitCommonDir)
	}

	featurePath, err := ws.ResolveFeaturePath("auth")
	if err != nil {
		t.Fatal(err)
	}
	stack, err := internal.LoadStackForStatus(featurePath, "auth")
	if err != nil {
		t.Fatal(err)
	}

	// A: the shipped evaluator's own processes, measured by a control run.
	rec.reset()
	internal.FeatureStackEdges(ws, cfg, "auth", featurePath, stack)
	control := rec.invocations()

	// The full RunE must record exactly I + A + 2 + C + D.
	rec.reset()
	out, _, runErr := runStackStatus(t, "auth")
	if runErr != nil {
		t.Fatalf("run failed: %v", runErr)
	}
	if !strings.HasPrefix(out, "Stack status: auth") {
		t.Fatalf("report = %q", out)
	}
	measured := rec.invocations()

	remaining := map[string]int{}
	for _, inv := range append(append([]cliInvocation{}, infra...), control...) {
		remaining[inv.key()]++
	}
	var remainder []cliInvocation
	for _, inv := range measured {
		if remaining[inv.key()] > 0 {
			remaining[inv.key()]--
			continue
		}
		remainder = append(remainder, inv)
	}
	for key, left := range remaining {
		if left > 0 {
			t.Fatalf("control invocation %q is missing %d time(s)", key, left)
		}
	}

	forEachRef, worktreeList, revList, dirty := 0, 0, 0, 0
	for _, inv := range remainder {
		switch {
		case cliHasArg(inv, "for-each-ref"):
			forEachRef++
		case cliHasArg(inv, "worktree") && cliHasArg(inv, "list"):
			worktreeList++
		case cliHasArg(inv, "rev-list") && cliFullSHAPair.MatchString(inv.args[len(inv.args)-1]):
			revList++
		case cliHasArg(inv, "status") && cliHasArg(inv, "--porcelain") && inv.locks == "0":
			dirty++
		default:
			t.Fatalf("unclassified status-added invocation: git %s (locks=%q)", strings.Join(inv.args, " "), inv.locks)
		}
	}
	if forEachRef != 1 || worktreeList != 1 {
		t.Fatalf("inventories = %d/%d, want 1/1", forEachRef, worktreeList)
	}
	if revList != 2 || dirty != 2 {
		t.Fatalf("C = %d, D = %d, want 2 and 2", revList, dirty)
	}
	if len(measured) != len(infra)+len(control)+2+revList+dirty {
		t.Fatalf("end-to-end total = %d, want I(%d) + A(%d) + 2 + %d + %d",
			len(measured), len(infra), len(control), revList, dirty)
	}
}

// ---------------------------------------------------------------------------
// Object format neutrality, real-Git half (AC 23a)
// ---------------------------------------------------------------------------

func stackStatusSupportsSHA256(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "--object-format=sha256", "probe")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0", "HOME="+dir)
	return cmd.Run() == nil
}

func TestStackStatus_RealSHA256Repository(t *testing.T) {
	if !stackStatusSupportsSHA256(t) {
		t.Skip("this git cannot create a SHA-256 repository")
	}
	t.Setenv("GIT_CONFIG_COUNT", "0")
	b := newGoldenBuilder(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	b.git(root, "init", "--bare", "--object-format=sha256", "--initial-branch=main", remote)
	b.git(root, "init", "--object-format=sha256", "--initial-branch=main", repo)
	goldenWrite(t, filepath.Join(repo, "README.md"), "base\n")
	b.git(repo, "add", "README.md")
	b.git(repo, "commit", "-m", "initial")
	b.git(repo, "remote", "add", "origin", remote)
	b.git(repo, "push", "-u", "origin", "main")
	b.git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
	b.git(repo, "remote", "set-head", "origin", "-a")
	b.git(repo, "branch", "api", "main")
	b.git(repo, "push", "-u", "origin", "api")

	metaRoot := repo + ".tws"
	worktreesRoot := filepath.Join(metaRoot, "auth", "worktrees")
	if err := os.MkdirAll(worktreesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	b.git(repo, "worktree", "add", filepath.Join(worktreesRoot, "api"), "api")
	goldenWrite(t, filepath.Join(metaRoot, "auth", "stack.yaml"), "branches:\n  - name: api\n    base: main\n")

	withIdleTmuxOnPath(t)
	_ = withUnifiedWorkspaceEnv(t, repo)

	inv := internal.BuildWorktreeInventory(repo)
	if !inv.Available || inv.Err != nil {
		t.Fatalf("a SHA-256 repository keeps the worktree inventory available: %+v", inv)
	}
	head := inv.Records[0].Head
	if head == nil || len(*head) != 64 {
		t.Fatalf("HEAD must be a 64-hex object id, got %v", head)
	}
	branchInv := internal.BuildBranchRefInventory(repo)
	if !branchInv.Available || branchInv.Err != nil {
		t.Fatalf("a SHA-256 repository keeps the branch-ref inventory available: %+v", branchInv)
	}

	out, _, err := runStackStatus(t, "auth", "--json")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	entry := doc["entries"].([]any)[0].(map[string]any)
	m := entry["materialization"].(map[string]any)
	if m["state"] != "present" {
		t.Fatalf("materialization = %v", m)
	}
	if m["checked_out_branch"] != "api" {
		t.Fatalf("checked_out_branch = %v", m["checked_out_branch"])
	}
	if m["detached"] != false {
		t.Fatalf("detached = %v", m["detached"])
	}
	up := entry["upstream"].(map[string]any)
	if up["configured"] != true || up["state"] != "equal" || up["display"] != "origin/api" {
		t.Fatalf("upstream = %v", up)
	}

	// tws status materialization and prunable semantics are unchanged.
	agentOut, _, err := runStatus(t, "--json")
	if err != nil {
		t.Fatalf("tws status failed: %v", err)
	}
	var agent map[string]any
	if err := json.Unmarshal([]byte(agentOut), &agent); err != nil {
		t.Fatal(err)
	}
	feature := agent["features"].([]any)[0].(map[string]any)
	agentEntry := feature["entries"].([]any)[0].(map[string]any)
	if agentEntry["materialization"].(map[string]any)["state"] != "present" {
		t.Fatalf("tws status materialization = %v", agentEntry["materialization"])
	}
}
