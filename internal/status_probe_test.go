package internal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeStatusProbeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func statusProbePIDGone(pid int) bool {
	err := syscall.Kill(pid, syscall.Signal(0))
	return errors.Is(err, syscall.ESRCH)
}

func waitForStatusProbePIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if statusProbePIDGone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status probe child pid %d is still alive", pid)
}

func TestStatusProbeDefaultBudgets(t *testing.T) {
	if statusSubprocessTimeout != 5*time.Second {
		t.Fatalf("per-subprocess timeout = %s, want 5s", statusSubprocessTimeout)
	}
	if statusInvocationTimeout != 30*time.Second {
		t.Fatalf("invocation timeout = %s, want 30s", statusInvocationTimeout)
	}
}

func TestStatusProbeCompletedExitRejectsUncertainExitOne(t *testing.T) {
	script := writeStatusProbeScript(t, "exit 1\n")
	var exitErr *exec.ExitError
	if err := exec.Command(script).Run(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a real exit-code-one fixture, got %v", err)
	}
	cases := []struct {
		name      string
		probe     *statusProbeError
		completed bool
	}{
		{"completed", &statusProbeError{err: exitErr}, true},
		{"deadline-race", &statusProbeError{err: errors.Join(context.DeadlineExceeded, exitErr), timedOut: true}, false},
		{"cancellation-race", &statusProbeError{err: errors.Join(context.Canceled, exitErr), canceled: true}, false},
		{"pipe-expiry", &statusProbeError{err: errors.Join(exitErr, exec.ErrWaitDelay), pipeExpired: true}, false},
		{"drain-failure", &statusProbeError{err: errors.Join(exitErr, os.ErrClosed)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := completedStatusExit(tc.probe) != nil; got != tc.completed {
				t.Fatalf("completed=%v, want %v for %v", got, tc.completed, tc.probe)
			}
			if got := completedTmuxNoServerFailure(tc.probe, []byte("no server running")); got != tc.completed {
				t.Fatalf("no-server=%v, want %v for %v", got, tc.completed, tc.probe)
			}
		})
	}
}

func TestStatusProbeBudgetPerCommandAndTotalExpiry(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "invocations.log")
	t.Setenv("STATUS_PROBE_LOG", logPath)
	slow := writeStatusProbeScript(t, "printf 'slow\\n' >> \"$STATUS_PROBE_LOG\"\nsleep 30\n")
	fast := writeStatusProbeScript(t, "printf 'fast\\n' >> \"$STATUS_PROBE_LOG\"\n")

	budget := newStatusProbeBudget(context.Background(), 250*time.Millisecond, 2*time.Second)
	defer budget.Close()
	if _, _, err := budget.run(slow); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("slow probe error = %v", err)
	}
	if _, _, err := budget.run(fast); err != nil {
		t.Fatalf("a per-command timeout must not consume the remaining total budget: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "slow\nfast\n" {
		t.Fatalf("actual dispatch log = %q", got)
	}

	totalLog := filepath.Join(t.TempDir(), "total.log")
	t.Setenv("STATUS_PROBE_LOG", totalLog)
	total := newStatusProbeBudget(context.Background(), time.Second, 250*time.Millisecond)
	defer total.Close()
	if _, _, err := total.run(slow); err == nil {
		t.Fatal("the total budget must cancel the running probe")
	}
	if _, _, err := total.run(fast); err == nil {
		t.Fatal("no command may dispatch after total expiry")
	}
	if total.launchedCount() != 1 {
		t.Fatalf("launched commands = %d, want 1", total.launchedCount())
	}
	data, err = os.ReadFile(totalLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "slow\n" {
		t.Fatalf("actual post-expiry dispatch log = %q", got)
	}
}

func TestStatusProbeBudgetKillsChildrenHoldingPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group cleanup is Unix-specific")
	}
	childFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("STATUS_CHILD_PID_FILE", childFile)
	script := writeStatusProbeScript(t, "sleep 30 &\nprintf '%s\\n' \"$!\" > \"$STATUS_CHILD_PID_FILE\"\nexit 0\n")

	budget := newStatusProbeBudget(context.Background(), 2*time.Second, 3*time.Second)
	defer budget.Close()
	start := time.Now()
	if _, _, err := budget.run(script); err == nil {
		t.Fatal("a child-held output pipe must be surfaced as an unavailable probe")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("child-held pipe cleanup took %s", elapsed)
	}
	data, err := os.ReadFile(childFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	waitForStatusProbePIDGone(t, pid)
}

func TestRealTmuxInventoryNonzeroParentPipeLeakIsUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group cleanup is Unix-specific")
	}
	dir := t.TempDir()
	childFile := filepath.Join(dir, "child.pid")
	shim := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
echo 'no server running' >&2
sleep 30 &
printf '%s\n' "$!" > "$STATUS_CHILD_PID_FILE"
exit 1
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STATUS_CHILD_PID_FILE", childFile)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	budget := newStatusProbeBudget(context.Background(), 2*time.Second, 3*time.Second)
	defer budget.Close()

	snapshot := (RealTmuxInventory{budget: budget}).Snapshot()
	if snapshot.Err == nil || !strings.Contains(snapshot.Err.Error(), "output pipe cleanup timed out") {
		t.Fatalf("no-server text must not hide inherited-pipe expiry: %+v", snapshot)
	}
	data, err := os.ReadFile(childFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if process, findErr := os.FindProcess(pid); findErr == nil {
			_ = process.Kill()
		}
	})
	waitForStatusProbePIDGone(t, pid)
}

func TestStatusProbeBudgetHonorsParentCancellation(t *testing.T) {
	script := writeStatusProbeScript(t, "sleep 30\n")
	parent, cancel := context.WithCancel(context.Background())
	budget := newStatusProbeBudget(parent, 5*time.Second, 30*time.Second)
	defer budget.Close()
	time.AfterFunc(60*time.Millisecond, cancel)

	start := time.Now()
	if _, _, err := budget.run(script); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("parent cancellation error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("parent cancellation took %s", elapsed)
	}
	if _, _, err := budget.run(script); err == nil {
		t.Fatal("parent cancellation must prevent later dispatch")
	}
	if budget.launchedCount() != 1 {
		t.Fatalf("launched commands = %d, want 1", budget.launchedCount())
	}
}

func TestStatusProbeTmuxNoServerTextDoesNotOverrideTimeout(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "output-written")
	shim := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
echo 'no server running' >&2
printf 'written\n' > "$STATUS_TMUX_MARKER"
sleep 30
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STATUS_TMUX_MARKER", marker)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	budget := newStatusProbeBudget(context.Background(), 500*time.Millisecond, 2*time.Second)
	defer budget.Close()

	snapshot := (RealTmuxInventory{budget: budget}).Snapshot()
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "written\n" {
		t.Fatalf("tmux wrapper did not reach its no-server output marker: data=%q err=%v", data, err)
	}
	if snapshot.Err == nil || !strings.Contains(snapshot.Err.Error(), "timed out") {
		t.Fatalf("timeout must remain unavailable even if partial output resembles no-server: %+v", snapshot)
	}
}

func TestStatusTmuxPermissionFailureIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "tmux")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\necho 'error connecting to /var/run/tmux/default (Permission denied)' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	budget := newStatusProbeBudget(context.Background(), time.Second, 2*time.Second)
	defer budget.Close()

	snapshot := (RealTmuxInventory{budget: budget}).Snapshot()
	if snapshot.Err == nil {
		t.Fatalf("an inaccessible tmux server is not proven absent: %+v", snapshot)
	}
}

func TestRealTmuxInventoryRetainsSessionsWhenPaneProbeTimesOut(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
case "$1" in
  list-sessions) printf 'tws-auth\n' ;;
  list-panes) sleep 30 ;;
esac
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	budget := newStatusProbeBudget(context.Background(), 300*time.Millisecond, 2*time.Second)
	defer budget.Close()

	snapshot := (RealTmuxInventory{budget: budget}).Snapshot()
	if !snapshot.Available || !snapshot.ServerRunning || !snapshot.Sessions["tws-auth"] {
		t.Fatalf("session evidence was lost: %+v", snapshot)
	}
	if snapshot.Err != nil || snapshot.PanesErr == nil || snapshot.PanesAvailable {
		t.Fatalf("pane timeout classification = %+v", snapshot)
	}
}

func TestStatusWorkspaceResolutionUsesSharedBudget(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, filepath.Dir(repo), "init", "--initial-branch=main", repo)
	gitInTest(t, repo, "commit", "--allow-empty", "-m", "init")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "git")
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
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	budget := newStatusProbeBudget(context.Background(), 250*time.Millisecond, 350*time.Millisecond)
	defer budget.Close()
	start := time.Now()
	if _, _, err := ResolveStatusWorkspaceWithBudget("", budget); err == nil ||
		!strings.Contains(err.Error(), "Git probe timed out") {
		t.Fatalf("resolution error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("bounded config/workspace resolution took %s", elapsed)
	}
}

func TestStatusWorkspaceScopedInferenceUsesOnlySelectedFeature(t *testing.T) {
	root := t.TempDir()
	makeRepo := func(name string) string {
		t.Helper()
		repo := filepath.Join(root, name)
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, root, "init", "--initial-branch=main", repo)
		gitInTest(t, repo, "commit", "--allow-empty", "-m", "init")
		return repo
	}
	authRepo := makeRepo("auth-repo")
	billingRepo := makeRepo("billing-repo")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, workspaceMarker), 0o755); err != nil {
		t.Fatal(err)
	}
	addFeature := func(feature, name, repo string) {
		t.Helper()
		featurePath := filepath.Join(workspace, feature)
		worktreePath := filepath.Join(featurePath, "worktrees", name)
		if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := SaveStack(featurePath, Stack{Branches: []StackEntry{{Name: name, Base: "main"}}}); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, repo, "worktree", "add", "-b", name, worktreePath)
	}
	addFeature("auth", "api", authRepo)
	addFeature("billing", "pay", billingRepo)

	t.Setenv("HOME", t.TempDir())
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}

	scopedBudget := NewStatusProbeBudget(context.Background())
	defer scopedBudget.Close()
	ws, degraded, err := ResolveStatusWorkspaceWithBudget("auth", scopedBudget)
	if err != nil {
		t.Fatal(err)
	}
	if degraded != "" || ws.RepoRoot != canonicalize(authRepo) {
		t.Fatalf("scoped workspace = %+v, degraded=%q", ws, degraded)
	}

	globalBudget := NewStatusProbeBudget(context.Background())
	defer globalBudget.Close()
	global, degraded, err := ResolveStatusWorkspaceWithBudget("", globalBudget)
	if err != nil {
		t.Fatal(err)
	}
	if global.RepoRoot != "" || !strings.Contains(degraded, "multiple default repositories") {
		t.Fatalf("global ambiguity = %+v, degraded=%q", global, degraded)
	}
}

func TestStatusWorkspaceConfiguredCandidateAmbiguityIsDegraded(t *testing.T) {
	root := t.TempDir()
	makeRepo := func(name string) string {
		t.Helper()
		repo := filepath.Join(root, name)
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, root, "init", "--initial-branch=main", repo)
		gitInTest(t, repo, "commit", "--allow-empty", "-m", "init")
		return repo
	}
	siblingRepo := makeRepo("source")
	configuredRepo := makeRepo("configured")
	metadataRoot := siblingRepo + ".tws"
	featurePath := filepath.Join(metadataRoot, "auth")
	if err := os.MkdirAll(filepath.Join(metadataRoot, workspaceMarker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveStack(featurePath, Stack{}); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := SaveConfigFile(ConfigPath(), Config{Workspaces: map[string]string{configuredRepo: metadataRoot}}); err != nil {
		t.Fatal(err)
	}
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(metadataRoot); err != nil {
		t.Fatal(err)
	}

	budget := NewStatusProbeBudget(context.Background())
	defer budget.Close()
	ws, degraded, err := ResolveStatusWorkspaceWithBudget("auth", budget)
	if err != nil {
		t.Fatal(err)
	}
	if ws.RepoRoot != "" || !strings.Contains(degraded, "multiple default repositories") ||
		!strings.Contains(degraded, canonicalize(siblingRepo)) ||
		!strings.Contains(degraded, canonicalize(configuredRepo)) {
		t.Fatalf("ambiguous configured workspace = %+v, degraded=%q", ws, degraded)
	}
}
