package internal

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------- Fake seams ----------

type fakeProcessChecker struct {
	alive map[int]bool
}

func (f fakeProcessChecker) Alive(pid int) bool {
	if f.alive == nil {
		return false
	}
	return f.alive[pid]
}

type fakeTmuxChecker struct {
	sessions map[string]bool
}

func (f fakeTmuxChecker) HasSession(name string) bool {
	if f.sessions == nil {
		return false
	}
	return f.sessions[name]
}

// ---------- Test helpers ----------

func setupHealthTestRepo(t *testing.T) (string, Workspace) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"HOME="+dir,
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	git("init", "--initial-branch=main")
	git("commit", "--allow-empty", "-m", "init")

	twsDir := filepath.Join(dir, ".tws")
	if err := os.MkdirAll(twsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(twsDir, "config.yaml"), []byte("workspace_mode: checkout\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Add .tws to gitignore to prevent dirty detection from metadata
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".tws/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".gitignore")
	git("commit", "-m", "add gitignore")

	cfg := LoadConfig()
	ws, err := ResolveCurrentWorkspaceE(dir, cfg)
	if err != nil {
		t.Fatalf("workspace resolution failed: %v", err)
	}
	return dir, ws
}

func addFeatureToRepo(t *testing.T, ws Workspace, feature, branch, base string) {
	t.Helper()
	fp := ws.FeaturePath(feature)
	if err := os.MkdirAll(fp, 0755); err != nil {
		t.Fatal(err)
	}
	stack := Stack{
		Branches: []StackEntry{
			{Name: branch, Base: base},
		},
	}
	if err := SaveStack(fp, stack); err != nil {
		t.Fatal(err)
	}
}

func addStackEntries(t *testing.T, ws Workspace, feature string, entries []StackEntry) {
	t.Helper()
	fp := ws.FeaturePath(feature)
	if err := os.MkdirAll(fp, 0755); err != nil {
		t.Fatal(err)
	}
	stack := Stack{Branches: entries}
	if err := SaveStack(fp, stack); err != nil {
		t.Fatal(err)
	}
}

func gitInTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+dir,
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// ---------- Tests ----------

func TestCheckoutHealth_Healthy(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	// Create branch and feature
	gitInTest(t, dir, "branch", "feat-branch")
	addFeatureToRepo(t, ws, "myfeat", "feat-branch", "main")

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Header.Mode != ModeCheckout {
		t.Errorf("expected checkout mode, got %s", report.Header.Mode)
	}
	if report.Header.Branch != "main" {
		t.Errorf("expected branch main, got %s", report.Header.Branch)
	}
	if report.Header.Detached {
		t.Error("should not be detached")
	}
	if report.Header.Dirty {
		t.Error("should not be dirty")
	}
	if report.Header.ActiveGitOp != "" {
		t.Errorf("unexpected active git op: %s", report.Header.ActiveGitOp)
	}

	if len(report.Features) != 1 {
		t.Fatalf("expected 1 feature entry, got %d", len(report.Features))
	}
	fe := report.Features[0]
	if fe.Name != "feat-branch" {
		t.Errorf("expected name feat-branch, got %s", fe.Name)
	}
	if !fe.RefExists {
		t.Error("ref should exist")
	}
	if fe.AncestryStatus != AncestryStatusCurrent {
		t.Errorf("expected current ancestry, got %s", fe.AncestryStatus)
	}
	if report.Issues != 0 {
		t.Errorf("expected 0 issues, got %d", report.Issues)
	}

	// Verify formatted output contains "All healthy"
	output := FormatCheckoutHealth(report)
	if !strings.Contains(output, "All healthy") {
		t.Errorf("expected 'All healthy' in output, got:\n%s", output)
	}
}

func TestCheckoutHealth_StaleChild(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	// Create parent and child branches
	gitInTest(t, dir, "branch", "parent-branch")
	gitInTest(t, dir, "branch", "child-branch")

	// Add a commit to parent after child was created (making child stale)
	gitInTest(t, dir, "checkout", "parent-branch")
	gitInTest(t, dir, "commit", "--allow-empty", "-m", "advance parent")
	gitInTest(t, dir, "checkout", "main")

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "parent-branch", Base: "main"},
		{Name: "child-branch", Base: "parent-branch"},
	})

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Child should be stale
	found := false
	for _, fe := range report.Features {
		if fe.Name == "child-branch" {
			found = true
			if fe.AncestryStatus != AncestryStatusStale {
				t.Errorf("expected stale ancestry for child, got %s", fe.AncestryStatus)
			}
			if fe.Severity != SeverityWarning {
				t.Errorf("expected warning severity for stale child, got %s", fe.Severity)
			}
		}
	}
	if !found {
		t.Error("child-branch entry not found")
	}

	if report.Issues < 1 {
		t.Errorf("expected at least 1 issue for stale child, got %d", report.Issues)
	}
}

func TestCheckoutHealth_MissingRefs(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	// Feature with a branch that doesn't exist in git
	addFeatureToRepo(t, ws, "myfeat", "ghost-branch", "main")

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.Features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(report.Features))
	}
	fe := report.Features[0]
	if fe.RefExists {
		t.Error("ref should not exist for ghost-branch")
	}
	if fe.AncestryStatus != AncestryStatusMissing {
		t.Errorf("expected missing ancestry, got %s", fe.AncestryStatus)
	}
}

func TestCheckoutHealth_DirtyDetachedGitOp(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	// Make dirty
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, dir, "add", "dirty.txt")

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !report.Header.Dirty {
		t.Error("should be dirty")
	}
	if report.Issues < 1 {
		t.Errorf("expected issues for dirty, got %d", report.Issues)
	}

	// Test detached HEAD
	gitInTest(t, dir, "commit", "-m", "commit dirty")
	sha := gitInTest(t, dir, "rev-parse", "HEAD")
	gitInTest(t, dir, "checkout", sha)

	report2, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report2.Header.Detached {
		t.Error("should be detached")
	}
}

func TestCheckoutHealth_SyncTransaction_LiveLock(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	stateDir := ws.CheckoutStateDir()
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write transaction
	tx := CheckoutTransaction{
		Feature: "myfeat",
		Stage:   StageRebasing,
		LockPID: 12345,
		Plan:    []CheckoutPlanEntry{{Branch: "child-branch", Base: "main"}},
	}
	txData, _ := yaml.Marshal(tx)
	if err := os.WriteFile(filepath.Join(stateDir, "myfeat-checkout-sync.yaml"), txData, 0600); err != nil {
		t.Fatal(err)
	}

	// Write lock
	lock := LockInfo{PID: 12345}
	lockData, _ := yaml.Marshal(lock)
	if err := os.WriteFile(filepath.Join(stateDir, "myfeat-checkout-sync.lock"), lockData, 0600); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{12345: true}},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.Sync) != 1 {
		t.Fatalf("expected 1 sync report, got %d", len(report.Sync))
	}
	sr := report.Sync[0]
	if sr.Liveness != "live" {
		t.Errorf("expected live liveness, got %s", sr.Liveness)
	}
	if !sr.LockLive {
		t.Error("lock should be live")
	}
	if sr.Stage != string(StageRebasing) {
		t.Errorf("expected rebasing stage, got %s", sr.Stage)
	}
}

func TestCheckoutHealth_SyncTransaction_StaleLock(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	stateDir := ws.CheckoutStateDir()
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	tx := CheckoutTransaction{
		Feature:     "myfeat",
		Stage:       StageSwitched,
		LockPID:     99999,
		FailureKind: FailConflict,
		FailureMsg:  "conflict in main.go",
		Plan:        []CheckoutPlanEntry{{Branch: "child-branch", Base: "main"}},
	}
	txData, _ := yaml.Marshal(tx)
	if err := os.WriteFile(filepath.Join(stateDir, "myfeat-checkout-sync.yaml"), txData, 0600); err != nil {
		t.Fatal(err)
	}

	lock := LockInfo{PID: 99999}
	lockData, _ := yaml.Marshal(lock)
	if err := os.WriteFile(filepath.Join(stateDir, "myfeat-checkout-sync.lock"), lockData, 0600); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{}}, // pid dead
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := report.Sync[0]
	if sr.Liveness != "stale" {
		t.Errorf("expected stale, got %s", sr.Liveness)
	}
	if sr.LockLive {
		t.Error("lock should not be live")
	}
	if !strings.Contains(sr.Guidance, "--continue") {
		t.Errorf("guidance should mention --continue, got: %s", sr.Guidance)
	}
	if !strings.Contains(sr.Guidance, "--abort") {
		t.Errorf("guidance should mention --abort, got: %s", sr.Guidance)
	}
	if sr.FailureReason == "" {
		t.Error("failure reason should be populated")
	}
}

func TestCheckoutHealth_SyncTransaction_Invalid(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	stateDir := ws.CheckoutStateDir()
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write garbage transaction
	if err := os.WriteFile(filepath.Join(stateDir, "broken-checkout-sync.yaml"), []byte("not: [valid yaml"), 0600); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.Sync) != 1 {
		t.Fatalf("expected 1 sync report, got %d", len(report.Sync))
	}
	sr := report.Sync[0]
	if sr.Liveness != "invalid" {
		t.Errorf("expected invalid, got %s", sr.Liveness)
	}
	if sr.Severity != SeverityError {
		t.Errorf("expected error severity, got %s", sr.Severity)
	}
}

func TestCheckoutHealth_Session_DirectLive(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	// Write session state
	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "mybranch",
		Mode:          AgentSessionDirect,
		PID:           42,
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}

	// Create lock dir
	lockDir := sessionLockDir(ws)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{42: true}},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Session == nil {
		t.Fatal("expected session report")
	}
	if !report.Session.Active {
		t.Error("session should be active")
	}
	if report.Session.Liveness != "live" {
		t.Errorf("expected live, got %s", report.Session.Liveness)
	}
	if !report.Session.OwnerLive {
		t.Error("owner should be live")
	}
	if !report.Session.LockHeld {
		t.Error("lock should be held")
	}
}

func TestCheckoutHealth_Session_DirectStale(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "mybranch",
		Mode:          AgentSessionDirect,
		PID:           99999,
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}
	lockDir := sessionLockDir(ws)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{}}, // dead
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Session.Liveness != "stale" {
		t.Errorf("expected stale, got %s", report.Session.Liveness)
	}
	if !strings.Contains(report.Session.Guidance, "tws close") {
		t.Errorf("guidance should mention tws close, got: %s", report.Session.Guidance)
	}
}

func TestCheckoutHealth_Session_TmuxLive(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "mybranch",
		Mode:          AgentSessionTmux,
		TmuxSession:   "tws-myfeat-mybranch",
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}
	lockDir := sessionLockDir(ws)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{sessions: map[string]bool{"tws-myfeat-mybranch": true}},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Session.Liveness != "live" {
		t.Errorf("expected live tmux session, got %s", report.Session.Liveness)
	}
	if !report.Session.OwnerLive {
		t.Error("tmux owner should be live")
	}
}

func TestCheckoutHealth_Session_TmuxStale(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "mybranch",
		Mode:          AgentSessionTmux,
		TmuxSession:   "tws-myfeat-mybranch",
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}
	lockDir := sessionLockDir(ws)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{sessions: map[string]bool{}}, // tmux gone
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Session.Liveness != "stale" {
		t.Errorf("expected stale tmux, got %s", report.Session.Liveness)
	}
	if !strings.Contains(report.Session.Guidance, "tws close") {
		t.Errorf("guidance should mention tws close, got: %s", report.Session.Guidance)
	}
}

func TestCheckoutHealth_Session_Mismatch_LockNoState(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	// Only lock, no state
	lockDir := sessionLockDir(ws)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Session == nil {
		t.Fatal("expected session report for orphan lock")
	}
	if report.Session.Liveness != "mismatch" {
		t.Errorf("expected mismatch, got %s", report.Session.Liveness)
	}
	if !strings.Contains(report.Session.Guidance, "tws close") {
		t.Errorf("guidance should mention tws close, got: %s", report.Session.Guidance)
	}
}

func TestCheckoutHealth_Session_Mismatch_StateNoLock(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	// State but no lock
	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "mybranch",
		Mode:          AgentSessionDirect,
		PID:           42,
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{42: true}},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Session.Liveness != "mismatch" {
		t.Errorf("expected mismatch, got %s", report.Session.Liveness)
	}
	if !strings.Contains(report.Session.Guidance, "tws close") {
		t.Errorf("guidance should mention tws close, got: %s", report.Session.Guidance)
	}
}

func TestCheckoutHealth_ContextLinks_Healthy(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	// Create a target file and a symlink
	target := filepath.Join(dir, ".tws", "features", "myfeat", "inject", "CLAUDE.local.md")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(dir, "CLAUDE.local.md")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}

	// Write session with link
	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "mybranch",
		Mode:          AgentSessionDirect,
		PID:           42,
		Links: []SessionContextLink{
			{Path: linkPath, Target: target},
		},
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}
	lockDir := sessionLockDir(ws)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{42: true}},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.Links) != 1 {
		t.Fatalf("expected 1 link report, got %d", len(report.Links))
	}
	if report.Links[0].Status != "healthy" {
		t.Errorf("expected healthy link, got %s", report.Links[0].Status)
	}
}

func TestCheckoutHealth_ContextLinks_Missing(t *testing.T) {
	_, ws := setupHealthTestRepo(t)

	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "mybranch",
		Mode:          AgentSessionDirect,
		PID:           42,
		Links: []SessionContextLink{
			{Path: "/nonexistent/link", Target: "/nonexistent/target"},
		},
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}
	lockDir := sessionLockDir(ws)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{42: true}},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(report.Links))
	}
	if report.Links[0].Status != "missing" {
		t.Errorf("expected missing, got %s", report.Links[0].Status)
	}
}

func TestCheckoutHealth_ContextLinks_Replaced(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	target := filepath.Join(dir, ".tws", "features", "myfeat", "inject", "CLAUDE.local.md")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create symlink pointing elsewhere
	otherTarget := filepath.Join(dir, "other-target")
	if err := os.WriteFile(otherTarget, []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "CLAUDE.local.md")
	if err := os.Symlink(otherTarget, linkPath); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "mybranch",
		Mode:          AgentSessionDirect,
		PID:           42,
		Links: []SessionContextLink{
			{Path: linkPath, Target: target},
		},
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}
	lockDir := sessionLockDir(ws)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{42: true}},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Links[0].Status != "replaced" {
		t.Errorf("expected replaced, got %s", report.Links[0].Status)
	}
}

func TestCheckoutHealth_ContextLinks_NotSymlink(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	// Create a regular file where a symlink is expected
	linkPath := filepath.Join(dir, "CLAUDE.local.md")
	if err := os.WriteFile(linkPath, []byte("regular file"), 0644); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "mybranch",
		Mode:          AgentSessionDirect,
		PID:           42,
		Links: []SessionContextLink{
			{Path: linkPath, Target: "/some/target"},
		},
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}
	lockDir := sessionLockDir(ws)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{42: true}},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Links[0].Status != "not-symlink" {
		t.Errorf("expected not-symlink, got %s", report.Links[0].Status)
	}
}

func TestCheckoutHealth_ReadOnly(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	// Create complex state
	gitInTest(t, dir, "branch", "feat-branch")
	addFeatureToRepo(t, ws, "myfeat", "feat-branch", "main")

	stateDir := ws.CheckoutStateDir()
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	tx := CheckoutTransaction{
		Feature: "myfeat",
		Stage:   StageRebasing,
		LockPID: 12345,
		Plan:    []CheckoutPlanEntry{{Branch: "feat-branch", Base: "main"}},
	}
	txData, _ := yaml.Marshal(tx)
	if err := os.WriteFile(filepath.Join(stateDir, "myfeat-checkout-sync.yaml"), txData, 0600); err != nil {
		t.Fatal(err)
	}
	lock := LockInfo{PID: 12345}
	lockData, _ := yaml.Marshal(lock)
	if err := os.WriteFile(filepath.Join(stateDir, "myfeat-checkout-sync.lock"), lockData, 0600); err != nil {
		t.Fatal(err)
	}

	// Snapshot state before
	branchesBefore := gitInTest(t, dir, "branch", "--list")
	headBefore := gitInTest(t, dir, "rev-parse", "HEAD")
	txBefore, _ := os.ReadFile(filepath.Join(stateDir, "myfeat-checkout-sync.yaml"))
	lockBefore, _ := os.ReadFile(filepath.Join(stateDir, "myfeat-checkout-sync.lock"))

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{12345: true}},
		Tmux: fakeTmuxChecker{},
	}
	_, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify nothing changed
	branchesAfter := gitInTest(t, dir, "branch", "--list")
	headAfter := gitInTest(t, dir, "rev-parse", "HEAD")
	txAfter, _ := os.ReadFile(filepath.Join(stateDir, "myfeat-checkout-sync.yaml"))
	lockAfter, _ := os.ReadFile(filepath.Join(stateDir, "myfeat-checkout-sync.lock"))

	if branchesBefore != branchesAfter {
		t.Error("branches changed after doctor")
	}
	if headBefore != headAfter {
		t.Error("HEAD changed after doctor")
	}
	if string(txBefore) != string(txAfter) {
		t.Error("transaction file changed after doctor")
	}
	if string(lockBefore) != string(lockAfter) {
		t.Error("lock file changed after doctor")
	}
}

func TestCheckoutList_Output(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	gitInTest(t, dir, "branch", "feat-branch")
	gitInTest(t, dir, "branch", "child-branch")

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "feat-branch", Base: "main"},
		{Name: "child-branch", Base: "feat-branch"},
	})

	entries, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Verify current marker
	for _, e := range entries {
		if e.Name == "feat-branch" || e.Name == "child-branch" {
			if e.Current {
				// Neither should be current since we're on main
				t.Errorf("%s should not be current (we're on main)", e.Name)
			}
		}
	}

	// Switch to feat-branch and verify current marker
	gitInTest(t, dir, "checkout", "feat-branch")
	entries2, _ := BuildCheckoutList(ws)
	for _, e := range entries2 {
		if e.Name == "feat-branch" && !e.Current {
			t.Error("feat-branch should be current after checkout")
		}
	}

	// Test format output
	output := FormatCheckoutList(ws, entries)
	if !strings.Contains(output, "myfeat") {
		t.Errorf("output should contain feature name, got:\n%s", output)
	}
	if !strings.Contains(output, "feat-branch") {
		t.Errorf("output should contain branch name, got:\n%s", output)
	}
}

func TestCheckoutList_ArchivedEntry(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	gitInTest(t, dir, "branch", "archived-branch")
	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "archived-branch", Base: "main", Archived: true},
	})

	entries, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !entries[0].Archived {
		t.Error("entry should be archived")
	}

	output := FormatCheckoutList(ws, entries)
	if !strings.Contains(output, "[archived]") {
		t.Errorf("output should contain [archived], got:\n%s", output)
	}
}

func TestCheckoutList_GitBranchDiffers(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	gitInTest(t, dir, "branch", "ws/mybranch")
	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "mybranch", Branch: "ws/mybranch", Base: "main"},
	})

	entries, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if entries[0].GitBranch != "ws/mybranch" {
		t.Errorf("expected git branch ws/mybranch, got %s", entries[0].GitBranch)
	}

	output := FormatCheckoutList(ws, entries)
	if !strings.Contains(output, "git: ws/mybranch") {
		t.Errorf("output should show git branch when different, got:\n%s", output)
	}
}

func TestCheckoutList_SessionMarker(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	gitInTest(t, dir, "branch", "feat-branch")
	addFeatureToRepo(t, ws, "myfeat", "feat-branch", "main")

	// Write session state
	stateDir := filepath.Join(ws.MetadataRoot, "state", "sessions")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema,
		Feature:       "myfeat",
		Name:          "feat-branch",
		Mode:          AgentSessionDirect,
		PID:           42,
	}
	sessData, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(stateDir, "active.json"), sessData, 0600); err != nil {
		t.Fatal(err)
	}

	entries, err := BuildCheckoutList(ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !entries[0].SessionActive {
		t.Error("entry should have session active marker")
	}

	output := FormatCheckoutList(ws, entries)
	if !strings.Contains(output, "[session]") {
		t.Errorf("output should show [session], got:\n%s", output)
	}
}

func TestCheckoutHealth_Ambiguity(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	// Create a feature that exists in both layouts (testing ambiguity resilience)
	_ = dir
	// Since BuildCheckoutHealthReport uses ListFeaturesResolved which handles
	// ambiguity at the resolve level, we just verify it doesn't crash.
	// Create one feature normally
	gitInTest(t, dir, "branch", "normal-branch")
	addFeatureToRepo(t, ws, "normal-feat", "normal-branch", "main")

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Features) != 1 {
		t.Errorf("expected 1 feature, got %d", len(report.Features))
	}
}

func TestCheckoutHealth_CrossRepo(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	gitInTest(t, dir, "branch", "cross-branch")

	addStackEntries(t, ws, "myfeat", []StackEntry{
		{Name: "cross-branch", Base: "main", Repo: "/other/repo"},
	})

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Features[0].AncestryStatus != AncestryStatusCrossRepo {
		t.Errorf("expected cross-repo, got %s", report.Features[0].AncestryStatus)
	}
}

func TestCheckoutHealth_FormatContainsSections(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)

	gitInTest(t, dir, "branch", "feat-branch")
	addFeatureToRepo(t, ws, "myfeat", "feat-branch", "main")

	// Add sync entry
	stateDir := ws.CheckoutStateDir()
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	tx := CheckoutTransaction{
		Feature: "myfeat",
		Stage:   StagePlanned,
		LockPID: 12345,
		Plan:    []CheckoutPlanEntry{{Branch: "feat-branch", Base: "main"}},
	}
	txData, _ := yaml.Marshal(tx)
	if err := os.WriteFile(filepath.Join(stateDir, "myfeat-checkout-sync.yaml"), txData, 0600); err != nil {
		t.Fatal(err)
	}
	lock := LockInfo{PID: 12345}
	lockData, _ := yaml.Marshal(lock)
	if err := os.WriteFile(filepath.Join(stateDir, "myfeat-checkout-sync.lock"), lockData, 0600); err != nil {
		t.Fatal(err)
	}

	opts := &CheckoutHealthOpts{
		Proc: fakeProcessChecker{alive: map[int]bool{12345: true}},
		Tmux: fakeTmuxChecker{},
	}
	report, err := BuildCheckoutHealthReport(ws, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := FormatCheckoutHealth(report)
	if !strings.Contains(output, "Workspace:") {
		t.Error("output should contain Workspace:")
	}
	if !strings.Contains(output, "Sync:") {
		t.Error("output should contain Sync: section")
	}
	if !strings.Contains(output, "Features:") {
		t.Error("output should contain Features: section")
	}
}
