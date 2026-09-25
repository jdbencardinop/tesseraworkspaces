package internal

import (
	"encoding/json"
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
	reparentRecordGitArgv(t, args...)
	t.Logf("session test git argv: git %s", strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_EDITOR=true")
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

func TestReparentSessionLaunchHandshake(t *testing.T) {
	reparentCountGitLeafFor(t)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	_, ws, e := setupSessionRepo(t)

	statePath := filepath.Join(ws.CheckoutStateDir(), "feature-reparent.v1.yaml")
	token := strings.Repeat("a", 32)
	if _, err := AcquireCheckoutMutationLock(ws.CheckoutStateDir(), token, "feature", "reparent", statePath); err != nil {
		t.Fatal(err)
	}
	agent := &testAgent{}
	shell := &testShell{}
	if err := OpenCheckoutDirect(ws, "feature", e, []string{"agent"}, agent, shell, ""); err == nil ||
		!strings.Contains(err.Error(), "sync") {
		t.Fatalf("session launch must lose to an existing mutation lock: %v", err)
	}
	if agent.calls != 0 {
		t.Fatal("the agent started despite a live checkout mutation")
	}
	if present, _, err := CheckoutSessionIntent(ws); err != nil || present {
		t.Fatalf("a refused launch must release its session intent: present=%v err=%v", present, err)
	}
	if err := ReleaseCheckoutMutationLock(ws.CheckoutStateDir(), token); err != nil {
		t.Fatal(err)
	}

	var mutationToken string
	CheckoutSessionLaunchIntentHook = func() error {
		present, _, err := CheckoutSessionIntent(ws)
		if err != nil || !present {
			t.Fatalf("the final-check hook must observe a durable session intent: present=%v err=%v", present, err)
		}
		mutationToken = strings.Repeat("b", 32)
		_, err = AcquireCheckoutMutationLock(ws.CheckoutStateDir(), mutationToken, "feature", "reparent", statePath)
		return err
	}
	agent = &testAgent{}
	err := OpenCheckoutDirect(ws, "feature", e, []string{"agent"}, agent, shell, "")
	CheckoutSessionLaunchIntentHook = nil
	if err == nil || !strings.Contains(err.Error(), "sync") {
		t.Fatalf("the final mutation check must refuse after publishing intent: %v", err)
	}
	if agent.calls != 0 {
		t.Fatal("the agent started after losing the session/reparent handshake")
	}
	if err := ReleaseCheckoutMutationLock(ws.CheckoutStateDir(), mutationToken); err != nil {
		t.Fatal(err)
	}

	if _, err := acquireAgentSessionLock(ws, newTestTmux()); err != nil {
		t.Fatal(err)
	}
	ownerPath := sessionLockOwnerPath(ws)
	data, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	var owner sessionLockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		t.Fatal(err)
	}
	owner.PID = reparentSpawnDeadPID(t)
	data, _ = json.Marshal(owner)
	if err := os.WriteFile(ownerPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, state := range []*CheckoutAgentSession{
		{
			SchemaVersion: checkoutSessionSchema, WorkspaceID: ws.StableID,
			Feature: "feature", Name: "feature-branch", GitBranch: "feature-branch",
			Mode: AgentSessionDirect, PID: reparentSpawnDeadPID(t), RepoDir: ws.RepoRoot,
		},
		{
			SchemaVersion: checkoutSessionSchema, WorkspaceID: ws.StableID,
			Feature: "feature", Name: "feature-branch", GitBranch: "feature-branch",
			Mode: AgentSessionTmux, TmuxSession: "missing-tmux", RepoDir: ws.RepoRoot,
		},
	} {
		if err := SaveCheckoutAgentSession(ws, state); err != nil {
			t.Fatal(err)
		}
		stateBefore, err := os.ReadFile(sessionStatePath(ws))
		if err != nil {
			t.Fatal(err)
		}
		if err := CleanupStaleCheckoutSessionIntent(ws); err == nil ||
			!strings.Contains(err.Error(), "close or recover") {
			t.Fatalf("%s state did not protect the dead launch intent: %v", state.Mode, err)
		}
		if _, err := acquireAgentSessionLock(ws, newTestTmux()); err == nil ||
			!strings.Contains(err.Error(), "close or recover") {
			t.Fatalf("%s state allowed a later launch to overwrite it: %v", state.Mode, err)
		}
		if after, err := os.ReadFile(sessionStatePath(ws)); err != nil ||
			string(after) != string(stateBefore) {
			t.Fatalf("%s state changed during refused cleanup/launch: %q (%v)", state.Mode, after, err)
		}
		if _, err := os.Stat(CheckoutSessionIntentDir(ws)); err != nil {
			t.Fatalf("%s state allowed intent removal: %v", state.Mode, err)
		}
		if err := os.Remove(sessionStatePath(ws)); err != nil {
			t.Fatal(err)
		}
	}
	if present, _, err := CheckoutSessionIntent(ws); err != nil || present {
		t.Fatalf("dead launcher intent must not remain a blocker: present=%v err=%v", present, err)
	}
	cleanupToken := strings.Repeat("c", 32)
	if _, err := AcquireCheckoutMutationLock(ws.CheckoutStateDir(), cleanupToken, "feature", "reparent", statePath); err != nil {
		t.Fatal(err)
	}
	if err := CleanupStaleCheckoutSessionIntent(ws); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(CheckoutSessionIntentDir(ws)); !os.IsNotExist(err) {
		t.Fatalf("post-lock stale intent cleanup left residue: %v", err)
	}
	if err := ReleaseCheckoutMutationLock(ws.CheckoutStateDir(), cleanupToken); err != nil {
		t.Fatal(err)
	}
	var admissionToken string
	CheckoutSessionLaunchIntentHook = func() error {
		admissionToken = strings.Repeat("d", 32)
		if _, err := AcquireCheckoutMutationLock(
			ws.CheckoutStateDir(), admissionToken, "feature", "reparent", statePath,
		); err != nil {
			return err
		}
		present, _, err := CheckoutSessionIntent(ws)
		if err != nil || !present {
			t.Fatalf("mutation admission did not observe the published feature-directory intent: present=%v err=%v", present, err)
		}
		return ReleaseCheckoutMutationLock(ws.CheckoutStateDir(), admissionToken)
	}
	checkIntent := func(phase string) {
		t.Helper()
		present, _, err := CheckoutSessionIntent(ws)
		if err != nil || !present {
			t.Fatalf("%s did not retain the checkout launch intent: present=%v err=%v", phase, present, err)
		}
	}
	err = WithCheckoutSessionLaunchIntent(ws, func() error {
		checkIntent("final check")
		return CheckoutFeatureDirSessionPreconditions(ws)
	}, func() error {
		checkIntent("agent")
		checkIntent("shell")
		return nil
	})
	CheckoutSessionLaunchIntentHook = nil
	if err != nil {
		t.Fatalf("winning feature-directory launch: %v", err)
	}
	if present, _, err := CheckoutSessionIntent(ws); err != nil || present {
		t.Fatalf("completed feature-directory launch left intent: present=%v err=%v", present, err)
	}
}

func TestCheckoutSessionIntentCleanupRejectsSymlinksAndChanges(t *testing.T) {
	t.Cleanup(func() { CheckoutSessionIntentCleanupHook = nil })
	ownerBytes := func(t *testing.T, token string) []byte {
		t.Helper()
		data, err := json.Marshal(sessionLockOwner{
			Token: token, PID: reparentSpawnDeadPID(t), CreatedAt: "2026-09-23T00:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	writeIntent := func(t *testing.T, ws Workspace, data []byte) {
		t.Helper()
		if err := os.MkdirAll(sessionLockDir(ws), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sessionLockOwnerPath(ws), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("symlink outside workspace is never followed", func(t *testing.T) {
		ws := Workspace{MetadataRoot: t.TempDir()}
		if err := os.MkdirAll(filepath.Dir(sessionLockDir(ws)), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		secret := ownerBytes(t, "outside-secret-token")
		outsideOwner := filepath.Join(outside, sessionLockOwnerName)
		if err := os.WriteFile(outsideOwner, secret, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, sessionLockDir(ws)); err != nil {
			t.Fatal(err)
		}

		present, _, err := CheckoutSessionIntent(ws)
		if !present || err == nil {
			t.Fatalf("symlink intent = present %v err %v, want unverifiable/live", present, err)
		}
		if strings.Contains(err.Error(), "outside-secret-token") {
			t.Fatalf("symlink target owner bytes leaked through the intent probe: %v", err)
		}
		if _, err := acquireAgentSessionLock(ws, newTestTmux()); err == nil {
			t.Fatal("session launch must refuse a symlinked intent directory")
		}
		if err := releaseAgentSessionLock(ws, "outside-secret-token"); err == nil {
			t.Fatal("session release must refuse a symlinked intent directory")
		}
		if err := CleanupStaleCheckoutSessionIntent(ws); err == nil {
			t.Fatal("stale cleanup must refuse a symlinked intent directory")
		}
		after, err := os.ReadFile(outsideOwner)
		if err != nil || string(after) != string(secret) {
			t.Fatalf("symlink target changed: %q (%v)", after, err)
		}
		if info, err := os.Lstat(sessionLockDir(ws)); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("intent symlink was removed or replaced: %v (%v)", info, err)
		}
	})

	t.Run("non-directory intent is unverifiable", func(t *testing.T) {
		ws := Workspace{MetadataRoot: t.TempDir()}
		if err := os.MkdirAll(filepath.Dir(sessionLockDir(ws)), 0o700); err != nil {
			t.Fatal(err)
		}
		body := []byte("not a directory\n")
		if err := os.WriteFile(sessionLockDir(ws), body, 0o600); err != nil {
			t.Fatal(err)
		}
		present, _, err := CheckoutSessionIntent(ws)
		if !present || err == nil {
			t.Fatalf("non-directory intent = present %v err %v, want unverifiable/live", present, err)
		}
		if err := CleanupStaleCheckoutSessionIntent(ws); err == nil {
			t.Fatal("stale cleanup must refuse a non-directory intent")
		}
		after, err := os.ReadFile(sessionLockDir(ws))
		if err != nil || string(after) != string(body) {
			t.Fatalf("non-directory intent changed: %q (%v)", after, err)
		}
	})

	t.Run("owner bytes change before removal", func(t *testing.T) {
		ws := Workspace{MetadataRoot: t.TempDir()}
		original := ownerBytes(t, "original-owner")
		writeIntent(t, ws, original)
		replacement := ownerBytes(t, "replacement-owner")
		CheckoutSessionIntentCleanupHook = func() error {
			return os.WriteFile(sessionLockOwnerPath(ws), replacement, 0o600)
		}
		err := CleanupStaleCheckoutSessionIntent(ws)
		CheckoutSessionIntentCleanupHook = nil
		if err == nil || !strings.Contains(err.Error(), "changed during stale cleanup") {
			t.Fatalf("changed owner cleanup = %v", err)
		}
		after, readErr := os.ReadFile(sessionLockOwnerPath(ws))
		if readErr != nil || string(after) != string(replacement) {
			t.Fatalf("replacement owner was removed or changed: %q (%v)", after, readErr)
		}
		if err := CleanupStaleCheckoutSessionIntent(ws); err != nil {
			t.Fatalf("cleanup of unchanged replacement: %v", err)
		}
	})

	t.Run("directory identity change before removal", func(t *testing.T) {
		ws := Workspace{MetadataRoot: t.TempDir()}
		owner := ownerBytes(t, "directory-owner")
		writeIntent(t, ws, owner)
		dir := sessionLockDir(ws)
		moved := dir + ".moved"
		CheckoutSessionIntentCleanupHook = func() error {
			if err := os.Rename(dir, moved); err != nil {
				return err
			}
			if err := os.Mkdir(dir, 0o700); err != nil {
				return err
			}
			return os.WriteFile(sessionLockOwnerPath(ws), owner, 0o600)
		}
		err := CleanupStaleCheckoutSessionIntent(ws)
		CheckoutSessionIntentCleanupHook = nil
		if err == nil || !strings.Contains(err.Error(), "changed during stale cleanup") {
			t.Fatalf("changed directory cleanup = %v", err)
		}
		for _, path := range []string{filepath.Join(dir, sessionLockOwnerName), filepath.Join(moved, sessionLockOwnerName)} {
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != string(owner) {
				t.Fatalf("identity-race owner %s changed: %q (%v)", path, after, readErr)
			}
		}
	})
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
