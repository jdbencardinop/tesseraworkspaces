package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func withStatusGitRecorder(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "git.log")
	shim := filepath.Join(dir, "git")
	script := `#!/bin/sh
{
  printf 'begin\n'
  printf 'locks\t%s\n' "${GIT_OPTIONAL_LOCKS-}"
  for arg in "$@"; do printf 'arg\t%s\n' "$arg"; done
  printf 'end\n'
} >> "$STATUS_GIT_LOG"
is_status=0
probe_path=
previous=
for arg in "$@"; do
  if [ "$arg" = "status" ]; then is_status=1; fi
  if [ "$previous" = "-C" ]; then probe_path=$arg; fi
  previous=$arg
done
if [ "${STATUS_DELAY_GIT_PATH-}" = "$probe_path" ] && [ "$is_status" = "1" ]; then
  sleep 2
fi
if [ "${STATUS_FAIL_GIT_PATH-}" = "$probe_path" ] && [ "$is_status" = "1" ]; then
  exit 42
fi
if [ "${STATUS_FAIL_GIT_STATUS-}" = "1" ] && [ "$is_status" = "1" ]; then
  exit 42
fi
case " $* " in
  *" worktree list --porcelain "*)
    if [ "${STATUS_FAIL_WORKTREE_LIST-}" = "1" ]; then exit 42; fi
    ;;
esac
exec "$STATUS_REAL_GIT" "$@"
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STATUS_GIT_LOG", logPath)
	t.Setenv("STATUS_REAL_GIT", realGit)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func withStatusTmuxRecorder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux.log")
	shim := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf 'tmux\n' >> "$STATUS_TMUX_LOG"
echo 'no server running' >&2
exit 1
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STATUS_TMUX_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readStatusLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func clearStatusLog(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
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
	for _, want := range []string{"always builds every feature", "builds only that feature", "five seconds", "agent_state", "needs_attention"} {
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

func TestStatusScopedDoesNotProbeUnrelatedFeatureAndGlobalStillDoes(t *testing.T) {
	repo := setupGitRepo(t, "main")
	root := withUnifiedWorkspaceEnv(t, repo)
	captureStdout(t, func() {
		if err := addExternal("auth", nil, "api", "main", false, false, false); err != nil {
			t.Fatal(err)
		}
		if err := addExternal("billing", nil, "pay", "main", false, false, false); err != nil {
			t.Fatal(err)
		}
	})
	withIdleTmuxOnPath(t)
	logPath := withStatusGitRecorder(t)
	billingPath := filepath.Join(root, "billing")
	billingWorktree := filepath.Join(billingPath, "worktrees", "pay")
	t.Setenv("STATUS_DELAY_GIT_PATH", billingWorktree)
	t.Setenv("STATUS_FAIL_GIT_PATH", billingWorktree)
	var scopedCounts []int

	for _, location := range []string{repo, root, filepath.Join(root, "auth")} {
		chdirForTest(t, location)
		clearStatusLog(t, logPath)
		start := time.Now()
		out, _, err := runStatus(t, "auth", "--json")
		if err != nil {
			t.Fatalf("%s: %v", location, err)
		}
		if elapsed := time.Since(start); elapsed >= 1500*time.Millisecond {
			t.Fatalf("%s: scoped status was delayed by the unrelated worktree: %s", location, elapsed)
		}
		var report internal.AgentStatusReport
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Features) != 1 || report.Features[0].Feature != "auth" {
			t.Fatalf("%s: scoped features = %+v", location, report.Features)
		}
		logged := readStatusLog(t, logPath)
		if strings.Contains(logged, billingPath) || strings.Contains(logged, "arg\tpay\n") {
			t.Fatalf("%s: scoped Git argv touched billing:\n%s", location, logged)
		}
		if strings.Count(logged, "locks\t0\n") != strings.Count(logged, "begin\n") {
			t.Fatalf("%s: status Git probe omitted GIT_OPTIONAL_LOCKS=0:\n%s", location, logged)
		}
		scopedCounts = append(scopedCounts, strings.Count(logged, "begin\n"))
	}

	t.Setenv("STATUS_DELAY_GIT_PATH", "")
	t.Setenv("STATUS_FAIL_GIT_PATH", "")
	chdirForTest(t, repo)
	clearStatusLog(t, logPath)
	if _, _, err := runStatus(t, "--json"); err != nil {
		t.Fatal(err)
	}
	globalLog := readStatusLog(t, logPath)
	if !strings.Contains(globalLog, billingPath) {
		t.Fatalf("the unchanged global report must probe billing:\n%s", globalLog)
	}
	globalCount := strings.Count(globalLog, "begin\n")

	clearStatusLog(t, logPath)
	if _, _, err := runStatus(t, "auth", "--json"); err != nil {
		t.Fatal(err)
	}
	scopedCount := strings.Count(readStatusLog(t, logPath), "begin\n")
	if scopedCount >= globalCount {
		t.Fatalf("actual Git invocation counts: scoped=%d global=%d", scopedCount, globalCount)
	}
	t.Logf("actual Git invocations: scoped repo/workspace/feature=%v, global repo=%d", scopedCounts, globalCount)
}

func TestStatusMissingStopsBeforeInventories(t *testing.T) {
	repo := setupGitRepo(t, "main")
	root := withUnifiedWorkspaceEnv(t, repo)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := internal.EnsureExternalWorkspaceMarker(root); err != nil {
		t.Fatal(err)
	}
	gitLog := withStatusGitRecorder(t)
	tmuxLog := withStatusTmuxRecorder(t)

	for _, location := range []string{repo, root} {
		chdirForTest(t, location)
		clearStatusLog(t, gitLog)
		clearStatusLog(t, tmuxLog)
		out, _, err := runStatus(t, "missing", "--json")
		if err == nil || !strings.Contains(err.Error(), "feature not found: missing") {
			t.Fatalf("%s: err = %v", location, err)
		}
		if out != "" {
			t.Fatalf("%s: stdout = %q", location, out)
		}
		logged := readStatusLog(t, gitLog)
		for _, forbidden := range []string{"arg\tstatus\n", "arg\tworktree\n", "arg\t--verify\n"} {
			if strings.Contains(logged, forbidden) {
				t.Fatalf("%s: missing feature dispatched %q:\n%s", location, forbidden, logged)
			}
		}
		if got := readStatusLog(t, tmuxLog); got != "" {
			t.Fatalf("%s: tmux ran before selected-feature validation: %q", location, got)
		}
	}
}

func TestStatusGitProbeFailureProjectsUnknown(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withUnifiedWorkspaceEnv(t, repo)
	withIdleTmuxOnPath(t)
	captureStdout(t, func() {
		if err := addExternal("auth", nil, "api", "main", false, false, false); err != nil {
			t.Fatal(err)
		}
	})
	withStatusGitRecorder(t)
	t.Setenv("STATUS_FAIL_GIT_STATUS", "1")

	out, _, err := runStatus(t, "auth", "--json")
	if err != nil {
		t.Fatalf("a failed observation still produces a report: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	workspace := doc["workspace"].(map[string]any)
	if workspace["dirty"] != nil {
		t.Fatalf("workspace dirty = %v, want null", workspace["dirty"])
	}
	feature := doc["features"].([]any)[0].(map[string]any)
	entry := feature["entries"].([]any)[0].(map[string]any)
	materialization := entry["materialization"].(map[string]any)
	if materialization["dirty"] != nil {
		t.Fatalf("entry dirty = %v, want null", materialization["dirty"])
	}
	foundWorkspace, foundEntry := false, false
	for _, raw := range doc["issues"].([]any) {
		issue := raw.(map[string]any)
		if issue["code"] != "git-probe-unavailable" {
			continue
		}
		foundWorkspace = foundWorkspace || issue["scope"] == "workspace"
		foundEntry = foundEntry || issue["scope"] == "entry"
	}
	if !foundWorkspace || !foundEntry {
		t.Fatalf("probe issues = %v", doc["issues"])
	}
}

func TestStatusScopedIgnoresUnrelatedCheckoutAmbiguity(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	withIdleTmuxOnPath(t)
	authPath := filepath.Join(repo, ".tws", "features", "auth")
	for _, path := range []string{
		authPath,
		filepath.Join(repo, ".tws", "features", "billing"),
		filepath.Join(repo, ".tws", "billing"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := internal.SaveStack(authPath, internal.Stack{Branches: []internal.StackEntry{{Name: "api", Base: "main"}}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(repo, ".tws", "features", "billing"),
		filepath.Join(repo, ".tws", "billing"),
	} {
		if err := internal.SaveStack(path, internal.Stack{Branches: []internal.StackEntry{{Name: "pay", Base: "main"}}}); err != nil {
			t.Fatal(err)
		}
	}
	gitInDir(t, repo, "branch", "api")

	out, _, err := runStatus(t, "auth", "--json")
	if err != nil {
		t.Fatalf("unrelated checkout ambiguity must not abort auth: %v", err)
	}
	var report internal.AgentStatusReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Features) != 1 || report.Features[0].Feature != "auth" {
		t.Fatalf("features = %+v", report.Features)
	}

	if out, _, err := runStatus(t, "--json"); err == nil || !strings.Contains(err.Error(), "ambiguous feature") {
		t.Fatalf("global topology failure = %v, stdout=%q", err, out)
	}
}

func TestStatusScopedCwdEquivalence(t *testing.T) {
	repo := setupGitRepo(t, "main")
	root := withUnifiedWorkspaceEnv(t, repo)
	withIdleTmuxOnPath(t)
	captureStdout(t, func() {
		if err := addExternal("auth", nil, "api", "main", false, false, false); err != nil {
			t.Fatal(err)
		}
	})
	worktree := filepath.Join(root, "auth", "worktrees", "api")
	nestedWorktree := filepath.Join(worktree, "nested")
	nestedFeature := filepath.Join(root, "auth", "notes")
	for _, dir := range []string{nestedWorktree, nestedFeature} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	locations := []string{repo, worktree, nestedWorktree, root, filepath.Join(root, "auth"), nestedFeature}

	var reference string
	for _, location := range locations {
		chdirForTest(t, location)
		out, _, err := runStatus(t, "auth", "--json")
		if err != nil {
			t.Fatalf("%s: %v", location, err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatal(err)
		}
		delete(doc, "generated_at")
		normalized, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if reference == "" {
			reference = string(normalized)
		} else if string(normalized) != reference {
			t.Fatalf("%s produced a different scoped document", location)
		}
	}
}

func TestStatusScopedCheckoutLegacyCwdEquivalence(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	withIdleTmuxOnPath(t)
	featurePath := filepath.Join(repo, ".tws", "auth")
	nested := filepath.Join(featurePath, "notes")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "api", Base: "main"}}}); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, repo, "branch", "api")

	var reference string
	for _, location := range []string{repo, featurePath, nested} {
		chdirForTest(t, location)
		out, _, err := runStatus(t, "auth", "--json")
		if err != nil {
			t.Fatalf("%s: %v", location, err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatal(err)
		}
		delete(doc, "generated_at")
		normalized, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if reference == "" {
			reference = string(normalized)
		} else if string(normalized) != reference {
			t.Fatalf("%s produced a different checkout scoped document", location)
		}
	}
}

func TestStatusScopedCheckoutSkipsWorktreeInventory(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	withCheckoutEnv(t, repo)
	withIdleTmuxOnPath(t)
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	featurePath := ws.FeaturePath("auth")
	nested := filepath.Join(featurePath, "notes")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{
		Branches: []internal.StackEntry{{Name: "api", Base: "main"}},
	}); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, repo, "branch", "api")
	if err := internal.SaveCheckoutAgentSession(ws, &internal.CheckoutAgentSession{
		SchemaVersion: 1,
		WorkspaceID:   ws.StableID,
		Feature:       "auth",
		Name:          "api",
		GitBranch:     "api",
		Mode:          internal.AgentSessionDirect,
		PID:           os.Getpid(),
		Stage:         internal.DirectStageAgent,
		RepoDir:       repo,
		LockToken:     "status-test",
	}); err != nil {
		t.Fatal(err)
	}
	lockDir := internal.CheckoutSessionIntentDir(ws)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, err := json.Marshal(map[string]any{
		"token":      "status-test",
		"pid":        os.Getpid(),
		"created_at": "2026-10-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "owner.json"), owner, 0o600); err != nil {
		t.Fatal(err)
	}

	logPath := withStatusGitRecorder(t)
	t.Setenv("STATUS_FAIL_WORKTREE_LIST", "1")
	for _, location := range []string{repo, nested} {
		chdirForTest(t, location)
		clearStatusLog(t, logPath)
		out, _, err := runStatus(t, "auth", "--json")
		if err != nil {
			t.Fatalf("%s: %v", location, err)
		}
		if logged := readStatusLog(t, logPath); strings.Contains(logged, "arg\tworktree\n") {
			t.Fatalf("%s: checkout scoped status dispatched worktree inventory:\n%s", location, logged)
		}
		var report internal.AgentStatusReport
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Features) != 1 || len(report.Features[0].Entries) != 1 {
			t.Fatalf("%s: scoped report = %+v", location, report.Features)
		}
		entry := report.Features[0].Entries[0]
		if entry.Materialization.RefExists == nil || !*entry.Materialization.RefExists ||
			entry.Materialization.State != internal.MaterializedPresent {
			t.Fatalf("%s: selected ref projection = %+v", location, entry.Materialization)
		}
		if report.Workspace.CheckoutSession == nil ||
			report.Workspace.CheckoutSession.Presence != internal.PresencePresent ||
			len(entry.Sessions) != 1 {
			t.Fatalf("%s: selected session projection = workspace=%+v entry=%+v",
				location, report.Workspace.CheckoutSession, entry.Sessions)
		}
		for _, issue := range report.Issues {
			if issue.Code == internal.IssueGitProbeUnavailable &&
				strings.Contains(issue.Message, "worktree inventory") {
				t.Fatalf("%s: skipped inventory produced an issue: %+v", location, issue)
			}
		}
	}
}

func TestStatusParentCancellationStopsResolution(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withUnifiedWorkspaceEnv(t, repo)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "git")
	script := `#!/bin/sh
case " $* " in
  *" rev-parse --git-common-dir "*) sleep 30 ;;
esac
exec "$STATUS_REAL_GIT" "$@"
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STATUS_REAL_GIT", realGit)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	cmd := statusCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"auth", "--json"})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	start := time.Now()
	err = cmd.Execute()
	if err == nil || out.Len() != 0 {
		t.Fatalf("canceled status: err=%v stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("parent cancellation took %s", elapsed)
	}
}
