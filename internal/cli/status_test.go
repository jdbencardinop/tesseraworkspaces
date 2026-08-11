package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/pflag"
)

// withIdleTmuxOnPath makes the tmux inventory deterministic on every platform:
// developer machines and the Ubuntu runners have tmux, the macOS runner does
// not, so an unfixed status test observes a different issue set per host. The
// stub prepends a temporary executable to PATH and reproduces the exact
// no-server condition RealTmuxInventory recognizes, yielding Available=true,
// ServerRunning=false, no error, and therefore no tmux issue at all.
func withIdleTmuxOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\necho 'no server running' >&2\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// WriteFile perms are subject to umask; the stub must be executable.
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got, err := exec.LookPath("tmux"); err != nil || got != stub {
		t.Fatalf("tmux must resolve to the stub, got %q (%v)", got, err)
	}
	snap := internal.RealTmuxInventory{}.Snapshot()
	if !snap.Available || snap.ServerRunning || snap.Err != nil || len(snap.Sessions) != 0 {
		t.Fatalf("the stub must produce an idle inventory, got %+v", snap)
	}
}

// withoutTmuxOnPath is the opposite fixture: a PATH that still resolves git —
// status needs real Git inventories — but provably cannot resolve tmux.
func withoutTmuxOnPath(t *testing.T) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("the test environment must provide git: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git must stay reachable: %v", err)
	}
	if got, err := exec.LookPath("tmux"); err == nil {
		t.Fatalf("tmux must be unreachable, resolved %q", got)
	}
	if snap := (internal.RealTmuxInventory{}).Snapshot(); snap.Available {
		t.Fatalf("the inventory must report tmux unavailable, got %+v", snap)
	}
}

func runStatus(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := statusCmd()
	var out, errOut bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestStatusHelpSurface(t *testing.T) {
	cmd := statusCmd()
	if cmd.Short != "Show agent work status for every logical branch" {
		t.Fatalf("Short = %q", cmd.Short)
	}
	for _, want := range []string{"always covers every feature", "agent_state", "needs_attention"} {
		if !strings.Contains(cmd.Long, want) {
			t.Fatalf("Long text is missing %q", want)
		}
	}
	flags := 0
	cmd.Flags().VisitAll(func(*pflag.Flag) { flags++ })
	if f := cmd.Flags().Lookup("json"); f == nil || f.Usage != "Output as JSON" {
		t.Fatalf("--json flag = %+v", f)
	}
	if flags != 1 {
		t.Fatalf("status must declare exactly one flag, got %d", flags)
	}
}

func TestStatusRejectsUnknownFlag(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withUnifiedWorkspaceEnv(t, repo)
	_, _, err := runStatus(t, "--not-a-flag")
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --not-a-flag") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusEmptyWorkspace(t *testing.T) {
	repo := setupGitRepo(t, "main")
	root := withUnifiedWorkspaceEnv(t, repo)
	withIdleTmuxOnPath(t)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}

	out, _, err := runStatus(t)
	if err != nil {
		t.Fatalf("an empty workspace exits 0: %v", err)
	}
	if !strings.Contains(out, "No features found. Use 'tws add <feature>' to create one.") {
		t.Fatalf("output = %q", out)
	}

	out, _, err = runStatus(t, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if jErr := json.Unmarshal([]byte(out), &doc); jErr != nil {
		t.Fatalf("invalid JSON: %v\n%s", jErr, out)
	}
	if doc["schema_version"] != float64(1) {
		t.Fatalf("schema_version = %v", doc["schema_version"])
	}
	if len(doc["features"].([]any)) != 0 || len(doc["issues"].([]any)) != 0 {
		t.Fatalf("features/issues must be empty arrays, got %v / %v", doc["features"], doc["issues"])
	}
	if !strings.HasSuffix(out, "\n") || !strings.Contains(out, "\n  \"schema_version\": 1") {
		t.Fatal("the encoder must use two-space indent and end with a newline")
	}
}

func TestStatusFeatureFilterAndNotFound(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withUnifiedWorkspaceEnv(t, repo)
	captureStdout(t, func() {
		if err := addExternal("auth", nil, "api", "main", false, false, false); err != nil {
			t.Fatalf("addExternal: %v", err)
		}
	})

	out, _, err := runStatus(t, "auth", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if jErr := json.Unmarshal([]byte(out), &doc); jErr != nil {
		t.Fatal(jErr)
	}
	if len(doc["features"].([]any)) != 1 {
		t.Fatalf("a filter narrows features[] to one element, got %v", doc["features"])
	}

	out, _, err = runStatus(t, "nosuch")
	if err == nil || !strings.Contains(err.Error(), "feature not found: nosuch") {
		t.Fatalf("err = %v", err)
	}
	if out != "" {
		t.Fatalf("nothing may be written to stdout on the error path, got %q", out)
	}
}

func TestStatusIsCwdIndependent(t *testing.T) {
	repo := setupGitRepo(t, "main")
	root := withUnifiedWorkspaceEnv(t, repo)
	withIdleTmuxOnPath(t)
	captureStdout(t, func() {
		if err := addExternal("auth", nil, "api", "main", false, false, false); err != nil {
			t.Fatalf("addExternal: %v", err)
		}
	})
	worktree := filepath.Join(root, "auth", "worktrees", "api")
	nested := filepath.Join(worktree, "nested")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}

	locations := map[string]string{
		"repo root":         repo,
		"worktree root":     worktree,
		"nested worktree":   nested,
		"workspace root":    root,
		"feature directory": filepath.Join(root, "auth"),
	}
	// The whole document is compared, not merely features[]: workspace,
	// issues, and summary are equally cwd-independent, and generated_at is
	// the only key allowed to differ between two polls.
	var reference, referenceLabel string
	for label, dir := range locations {
		chdirForTest(t, dir)
		out, _, err := runStatus(t, "--json")
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		var doc map[string]any
		if jErr := json.Unmarshal([]byte(out), &doc); jErr != nil {
			t.Fatalf("%s: %v", label, jErr)
		}
		if _, ok := doc["generated_at"]; !ok {
			t.Fatalf("%s: generated_at must exist before it is removed", label)
		}
		delete(doc, "generated_at")
		normalized, mErr := json.Marshal(doc)
		if mErr != nil {
			t.Fatal(mErr)
		}
		if reference == "" {
			reference, referenceLabel = string(normalized), label
			// The comparison must not be vacuous: the fixture has a feature,
			// an entry, and a resolved repository root.
			if len(doc["features"].([]any)) == 0 {
				t.Fatal("the fixture must report at least one feature")
			}
			if doc["workspace"].(map[string]any)["repo_root"] == nil {
				t.Fatal("the fixture must resolve a repository root")
			}
			continue
		}
		if string(normalized) != reference {
			t.Fatalf("%s produced a different document from %s:\n%s\n---\n%s",
				label, referenceLabel, normalized, reference)
		}
	}
}

func TestStatusGuardsRegisteredSpaceName(t *testing.T) {
	repo := setupGitRepo(t, "main")
	root := withUnifiedWorkspaceEnv(t, repo)
	spaceDir := filepath.Join(root, "learning")
	if err := os.MkdirAll(spaceDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeSpaces(t, root, registeredLearningFixture(spaceDir))
	before := snapshotTreeIgnoringLock(t, root)

	out, _, err := runStatus(t, "learning")
	if err == nil {
		t.Fatal("status must refuse a registered space name")
	}
	if out != "" {
		t.Fatalf("stdout must stay empty, got %q", out)
	}
	if after := snapshotTreeIgnoringLock(t, root); after != before {
		t.Fatal("a refused status must have zero side effects")
	}
}

func TestStatusFailsClosedOnMalformedSpaces(t *testing.T) {
	for _, fixture := range malformedSpacesFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			repo := setupGitRepo(t, "main")
			root := withUnifiedWorkspaceEnv(t, repo)
			fixture.install(t, root)

			for _, args := range [][]string{{}, {"--json"}, {"auth"}, {"auth", "--json"}} {
				out, _, err := runStatus(t, args...)
				if err == nil {
					t.Fatalf("status %v must fail closed", args)
				}
				if out != "" {
					t.Fatalf("status %v wrote to stdout: %q", args, out)
				}
			}
		})
	}
}

func TestStatusReportsDirectRecordsAndExitsZero(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withUnifiedWorkspaceEnv(t, repo)
	withIdleTmuxOnPath(t)
	captureStdout(t, func() {
		if err := addExternal("auth", nil, "api", "main", false, false, false); err != nil {
			t.Fatalf("addExternal: %v", err)
		}
	})
	featurePath := internal.FeaturePath("auth")
	seedRecord(t, featurePath, "auth", "api", 900301) // dead: no such pid

	out, _, err := runStatus(t, "--json")
	if err != nil {
		t.Fatalf("a branch that needs attention still exits 0: %v", err)
	}
	var doc map[string]any
	if jErr := json.Unmarshal([]byte(out), &doc); jErr != nil {
		t.Fatal(jErr)
	}
	workspace := doc["workspace"].(map[string]any)
	attention := workspace["attention"].(map[string]any)
	if attention["status"] != "needs_attention" {
		t.Fatalf("workspace attention = %v", attention)
	}
	if attention["issue_count"] != float64(0) || len(attention["codes"].([]any)) != 0 {
		t.Fatalf("issue_count and codes stay own-scope: %v", attention)
	}
	issues := doc["issues"].([]any)
	found := 0
	for _, raw := range issues {
		if raw.(map[string]any)["code"] == "direct-record-stale" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("an issue has exactly one home, found %d", found)
	}

	// The human view also exits 0 and shows the branch.
	human, _, err := runStatus(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human, "auth/api") || !strings.Contains(human, "attn") {
		t.Fatalf("human output = %q", human)
	}
}

// TestStatusReportsTmuxMissingWhenTmuxIsAbsent pins the other half of the
// deterministic pair: with tmux provably off PATH, status still exits 0 and
// states the absence once, as a workspace-scoped info issue.
func TestStatusReportsTmuxMissingWhenTmuxIsAbsent(t *testing.T) {
	repo := setupGitRepo(t, "main")
	root := withUnifiedWorkspaceEnv(t, repo)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	withoutTmuxOnPath(t)

	out, _, err := runStatus(t, "--json")
	if err != nil {
		t.Fatalf("a tmux-free workspace exits 0: %v", err)
	}
	var doc map[string]any
	if jErr := json.Unmarshal([]byte(out), &doc); jErr != nil {
		t.Fatalf("invalid JSON: %v\n%s", jErr, out)
	}
	issues := doc["issues"].([]any)
	if len(issues) != 1 {
		t.Fatalf("tmux absence is the only issue, got %v", issues)
	}
	issue := issues[0].(map[string]any)
	if issue["code"] != "tmux-missing" || issue["severity"] != "info" || issue["scope"] != "workspace" {
		t.Fatalf("issue = %v", issue)
	}
	if issue["feature"] != nil || issue["name"] != nil {
		t.Fatalf("a workspace issue names no branch: %v", issue)
	}
	attention := doc["workspace"].(map[string]any)["attention"].(map[string]any)
	if attention["status"] != "idle" {
		t.Fatalf("an info issue must not make the workspace need attention: %v", attention)
	}
}
