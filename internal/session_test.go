package internal

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type testAgent struct {
	err   error
	calls int
	dir   string
	args  []string
}

func (f *testAgent) Run(dir string, args []string) error {
	f.calls++
	f.dir = dir
	f.args = append([]string(nil), args...)
	return f.err
}

type testShell struct {
	err   error
	calls int
}

func (f *testShell) Run(string) error { f.calls++; return f.err }

type testTmux struct {
	sessions  map[string]bool
	created   []string
	attachErr error
	vanish    bool
	killed    []string
}

func newTestTmux() *testTmux { return &testTmux{sessions: map[string]bool{}} }
func (f *testTmux) NewSession(name, dir string, args []string) error {
	f.sessions[name] = true
	f.created = append(f.created, strings.Join(args, "\x00"))
	return nil
}
func (f *testTmux) AttachSession(name string) error {
	if f.vanish {
		delete(f.sessions, name)
	}
	return f.attachErr
}
func (f *testTmux) HasSession(name string) bool { return f.sessions[name] }
func (f *testTmux) KillSession(name string) error {
	delete(f.sessions, name)
	f.killed = append(f.killed, name)
	return nil
}

func setupSessionRepo(t *testing.T) (string, Workspace, StackEntry) {
	t.Helper()
	repo := t.TempDir()
	gitS(t, repo, "init", "-b", "main")
	gitS(t, repo, "config", "user.name", "Test")
	gitS(t, repo, "config", "user.email", "t@e")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	gitS(t, repo, "add", "README")
	gitS(t, repo, "commit", "-m", "base")
	gitS(t, repo, "branch", "feature-branch")
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte(".tws/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{RepoRoot: repo, MetadataRoot: filepath.Join(repo, ".tws"), Mode: ModeCheckout, StableID: "ws-123"}
	fp := ws.FeaturePath("feature")
	if err := os.MkdirAll(filepath.Join(fp, "inject"), 0755); err != nil {
		t.Fatal(err)
	}
	entry := StackEntry{Name: "short", Branch: "feature-branch", Base: "main"}
	if err := SaveStack(fp, Stack{Branches: []StackEntry{entry}}); err != nil {
		t.Fatal(err)
	}
	return repo, ws, entry
}
func gitS(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSessionNameStableBounded(t *testing.T) {
	a := CheckoutAgentSessionName("x", strings.Repeat("feature", 20), strings.Repeat("branch", 20))
	b := CheckoutAgentSessionName("x", strings.Repeat("feature", 20), strings.Repeat("branch", 20))
	if a != b || len(a) > 64 {
		t.Fatalf("%q %q", a, b)
	}
	if a == CheckoutAgentSessionName("x", strings.Repeat("feature", 20), strings.Repeat("branch", 19)+"z") {
		t.Fatal("collision")
	}
}
func TestSessionLockExactlyOneWinner(t *testing.T) {
	_, ws, _ := setupSessionRepo(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := acquireAgentSessionLock(ws, newTestTmux()); results <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("wins=%d", wins)
	}
}
func TestDirectSessionRestores(t *testing.T) {
	repo, ws, e := setupSessionRepo(t)
	a := &testAgent{}
	sh := &testShell{}
	if err := OpenCheckoutDirect(ws, "feature", e, []string{"agent"}, a, sh, ""); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 || sh.calls != 1 {
		t.Fatal("missing calls")
	}
	if got := gitS(t, repo, "branch", "--show-current"); got != "main" {
		t.Fatal(got)
	}
	if HasCheckoutAgentSession(ws) {
		t.Fatal("state remains")
	}
}
func TestDirectAgentFailureNoShell(t *testing.T) {
	repo, ws, e := setupSessionRepo(t)
	a := &testAgent{err: errors.New("boom")}
	sh := &testShell{}
	err := OpenCheckoutDirect(ws, "feature", e, []string{"agent"}, a, sh, "")
	if err == nil || sh.calls != 0 {
		t.Fatalf("err=%v shell=%d", err, sh.calls)
	}
	if gitS(t, repo, "branch", "--show-current") != "main" {
		t.Fatal("not restored")
	}
}
func TestTmuxSessionCloseRestores(t *testing.T) {
	repo, ws, e := setupSessionRepo(t)
	tm := newTestTmux()
	if err := OpenCheckoutTmux(ws, "feature", e, []string{"agent", "a b", ";x"}, tm, ""); err != nil {
		t.Fatal(err)
	}
	if tm.created[0] != "agent\x00a b\x00;x" {
		t.Fatalf("%q", tm.created)
	}
	if gitS(t, repo, "branch", "--show-current") != "feature-branch" {
		t.Fatal("not active")
	}
	if err := CloseCheckoutSession(ws, "feature", "short", tm); err != nil {
		t.Fatal(err)
	}
	if gitS(t, repo, "branch", "--show-current") != "main" {
		t.Fatal("not restored")
	}
}
func TestTmuxAttachFailureAliveRetains(t *testing.T) {
	repo, ws, e := setupSessionRepo(t)
	tm := newTestTmux()
	tm.attachErr = errors.New("attach")
	err := OpenCheckoutTmux(ws, "feature", e, []string{"agent"}, tm, "")
	if err == nil {
		t.Fatal("expected")
	}
	if gitS(t, repo, "branch", "--show-current") != "feature-branch" {
		t.Fatal("restored unexpectedly")
	}
	if !HasCheckoutAgentSession(ws) {
		t.Fatal("state missing")
	}
}
func TestContextCollisionAndCleanup(t *testing.T) {
	repo, ws, _ := setupSessionRepo(t)
	src := InjectPath(ws.FeaturePath("feature"))
	if err := os.WriteFile(filepath.Join(src, "CLAUDE.local.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	links, err := PlanCheckoutSessionLinks(ws, "feature", "")
	if err != nil {
		t.Fatal(err)
	}
	ex, err := ApplyCheckoutSessionLinks(repo, links)
	if err != nil {
		t.Fatal(err)
	}
	if err := CleanupCheckoutSessionLinks(repo, links, ex); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(repo, "CLAUDE.local.md")); !os.IsNotExist(err) {
		t.Fatal("link remains")
	}
}
func TestContextUnsafeInto(t *testing.T) {
	_, ws, _ := setupSessionRepo(t)
	if _, err := PlanCheckoutSessionLinks(ws, "feature", "../bad"); err == nil {
		t.Fatal("expected")
	}
}
func TestSessionRejectsAnySyncState(t *testing.T) {
	_, ws, e := setupSessionRepo(t)
	other := ws.FeaturePath("other")
	if err := os.MkdirAll(filepath.Dir(CheckoutTransactionPath(other)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CheckoutTransactionPath(other), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckoutSessionPreconditions(ws, "feature", e); err == nil || !strings.Contains(err.Error(), "sync") {
		t.Fatalf("%v", err)
	}
}
func TestClaudeSessionDetection(t *testing.T) {
	repo, _, _ := setupSessionRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	abs, _ := filepath.Abs(repo)
	encoded := strings.ReplaceAll(abs, string(filepath.Separator), "-")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects", encoded), 0755); err != nil {
		t.Fatal(err)
	}
	args := CheckoutSessionAgentCommand("claude", repo)
	if args[len(args)-1] != "-c" {
		t.Fatalf("%v", args)
	}
	if len(CheckoutSessionAgentCommand("copilot", repo)) != 1 {
		t.Fatal("copilot flags")
	}
}
