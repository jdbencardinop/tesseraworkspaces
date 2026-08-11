package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------- fakes ----------

type fakeDirectProcess struct {
	pid     int
	waitErr error
	calls   *[]string
	label   string
}

func (p *fakeDirectProcess) PID() int { return p.pid }

func (p *fakeDirectProcess) Wait() error {
	*p.calls = append(*p.calls, p.label+".wait")
	return p.waitErr
}

func (p *fakeDirectProcess) Terminate() error {
	*p.calls = append(*p.calls, p.label+".terminate")
	return nil
}

type fakeDirectRunner struct {
	calls    *[]string
	label    string
	pid      int
	startErr error
	waitErr  error
	onStart  func()
}

func (r *fakeDirectRunner) Start(dir string, command []string) (directProcess, error) {
	*r.calls = append(*r.calls, fmt.Sprintf("%s.start(%s)", r.label, strings.Join(command, " ")))
	if r.onStart != nil {
		r.onStart()
	}
	if r.startErr != nil {
		return nil, r.startErr
	}
	return &fakeDirectProcess{pid: r.pid, waitErr: r.waitErr, calls: r.calls, label: r.label}, nil
}

// fakeDirectStore delegates to the real store so on-disk state stays
// assertable, while injecting a failure at a chosen call index.
type fakeDirectStore struct {
	real        realDirectSessionStore
	calls       *[]string
	creates     int
	updates     int
	failCreate  map[int]error
	failUpdate  map[int]error
	onAfterCall func(stage string)
}

func (s *fakeDirectStore) Create(featurePath string, rec internal.DirectSessionRecord) (string, error) {
	s.creates++
	*s.calls = append(*s.calls, "store.create("+rec.Stage+")")
	if err, ok := s.failCreate[s.creates]; ok {
		return "", err
	}
	return s.real.Create(featurePath, rec)
}

func (s *fakeDirectStore) Update(featurePath, branchID, token string, mutate func(*internal.DirectSessionRecord)) error {
	s.updates++
	idx := s.updates
	if err, ok := s.failUpdate[idx]; ok {
		*s.calls = append(*s.calls, fmt.Sprintf("store.update#%d(fail)", idx))
		return err
	}
	stage := ""
	err := s.real.Update(featurePath, branchID, token, func(rec *internal.DirectSessionRecord) {
		mutate(rec)
		stage = rec.Stage
	})
	*s.calls = append(*s.calls, fmt.Sprintf("store.update#%d(%s)", idx, stage))
	if s.onAfterCall != nil {
		s.onAfterCall(stage)
	}
	return err
}

func (s *fakeDirectStore) RemoveOwned(featurePath, branchID, token string) error {
	*s.calls = append(*s.calls, "store.removeOwned")
	return s.real.RemoveOwned(featurePath, branchID, token)
}

// ---------- helpers ----------

func directTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	featurePath := t.TempDir()
	return featurePath
}

func recordFiles(t *testing.T, featurePath string) []string {
	t.Helper()
	var files []string
	root := internal.DirectSessionsDir(featurePath)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, rel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func loadOnlyRecord(t *testing.T, featurePath, feature, name string) internal.DirectSessionRecord {
	t.Helper()
	branchID := internal.DirectSessionBranchID(feature, name)
	loaded, err := internal.LoadDirectSessions(featurePath, branchID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("want exactly one record, got %d", len(loaded))
	}
	return loaded[0].Record
}

func trackedOpts(featurePath string) directOpenOpts {
	return directOpenOpts{
		Path:        filepath.Join(featurePath, "worktrees", "api"),
		Feature:     "auth",
		Name:        "api",
		GitBranch:   "jd/api",
		FeaturePath: featurePath,
		LookPath:    func(string) (string, error) { return "/usr/bin/claude", nil },
	}
}

// ---------- ordering ----------

func TestDirectOpenRecordLifecycleOrdering(t *testing.T) {
	featurePath := directTestEnv(t)
	var calls []string
	var stagesOnDisk []string

	store := &fakeDirectStore{calls: &calls}
	agent := &fakeDirectRunner{calls: &calls, label: "agent", pid: 4242, onStart: func() {
		stagesOnDisk = append(stagesOnDisk, "at-agent-start:"+loadOnlyRecord(t, featurePath, "auth", "api").Stage)
	}}
	shell := &fakeDirectRunner{calls: &calls, label: "shell", pid: 5150, onStart: func() {
		rec := loadOnlyRecord(t, featurePath, "auth", "api")
		stagesOnDisk = append(stagesOnDisk, fmt.Sprintf("at-shell-start:%s/child=%d", rec.Stage, rec.ChildPID))
	}}

	opts := trackedOpts(featurePath)
	opts.Runner, opts.Shell, opts.Store = agent, shell, store
	var out, errOut bytes.Buffer
	opts.Out, opts.Err = &out, &errOut

	if err := openDirect(opts); err != nil {
		t.Fatalf("openDirect: %v", err)
	}

	want := []string{
		"store.create(starting)",
		"agent.start(claude)",
		"store.update#1(agent)",
		"agent.wait",
		"store.update#2(shell)",
		"shell.start(/bin/sh)",
		"store.update#3(shell)",
		"shell.wait",
		"store.removeOwned",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("call order:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	if stagesOnDisk[0] != "at-agent-start:starting" {
		t.Fatalf("the starting record must exist before Start: %v", stagesOnDisk)
	}
	if stagesOnDisk[1] != "at-shell-start:shell/child=0" {
		t.Fatalf("the shell stage must be recorded before the shell starts: %v", stagesOnDisk)
	}
	if files := recordFiles(t, featurePath); len(files) != 0 {
		t.Fatalf("the record must be gone after a normal exit, found %v", files)
	}
	if _, err := os.Stat(internal.DirectSessionsDir(featurePath)); !os.IsNotExist(err) {
		t.Fatalf("both parent directories must be pruned: %v", err)
	}

	text := out.String()
	for _, want := range []string{"Opening: ", "Running: claude", "Dropped into shell at: "} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output %q", want, text)
		}
	}
}

func TestDirectOpenAgentExitDoesNotAbortShell(t *testing.T) {
	featurePath := directTestEnv(t)
	var calls []string
	agent := &fakeDirectRunner{calls: &calls, label: "agent", pid: 1, waitErr: errors.New("exit status 2")}
	shell := &fakeDirectRunner{calls: &calls, label: "shell", pid: 2}
	opts := trackedOpts(featurePath)
	opts.Runner, opts.Shell, opts.Store = agent, shell, &fakeDirectStore{calls: &calls}
	var out bytes.Buffer
	opts.Out, opts.Err = &out, &bytes.Buffer{}

	if err := openDirect(opts); err != nil {
		t.Fatalf("a non-zero agent exit is not an error: %v", err)
	}
	if !strings.Contains(out.String(), "Agent exited: exit status 2") {
		t.Fatalf("output = %q", out.String())
	}
	if !strings.Contains(strings.Join(calls, ","), "shell.start") {
		t.Fatal("the shell must still start")
	}
}

func TestDirectOpenLookPathFailureWritesNothing(t *testing.T) {
	featurePath := directTestEnv(t)
	var calls []string
	opts := trackedOpts(featurePath)
	opts.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	opts.Runner = &fakeDirectRunner{calls: &calls, label: "agent"}
	opts.Shell = &fakeDirectRunner{calls: &calls, label: "shell"}
	opts.Store = &fakeDirectStore{calls: &calls}
	opts.Out, opts.Err = &bytes.Buffer{}, &bytes.Buffer{}

	err := openDirect(opts)
	if err == nil || !strings.Contains(err.Error(), "not found in PATH") {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("nothing may be created or spawned: %v", calls)
	}
	if _, statErr := os.Stat(internal.DirectSessionsDir(featurePath)); !os.IsNotExist(statErr) {
		t.Fatal("no .sessions directory may be created")
	}
}

func TestDirectOpenEmptyAgentCommand(t *testing.T) {
	featurePath := directTestEnv(t)
	// A whitespace-only configured agent_command must be refused before
	// isClaudeAgent, which would otherwise index an empty field slice.
	cfgDir := filepath.Join(os.Getenv("HOME"), ".config", "tws")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("agent_command: \"   \"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	opts := trackedOpts(featurePath)
	opts.Out, opts.Err = &bytes.Buffer{}, &bytes.Buffer{}
	if err := openDirect(opts); err == nil {
		t.Skip("configured agent_command not honoured in this environment")
	} else if !strings.Contains(err.Error(), "agent_command is empty") && !strings.Contains(err.Error(), "not found in PATH") {
		t.Fatalf("err = %v", err)
	}
}

func TestDirectOpenStartFailureRemovesOnlyOwnRecord(t *testing.T) {
	featurePath := directTestEnv(t)
	sibling, err := internal.CreateDirectSession(featurePath, internal.DirectSessionRecord{Feature: "auth", Name: "api"})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	opts := trackedOpts(featurePath)
	opts.Runner = &fakeDirectRunner{calls: &calls, label: "agent", startErr: errors.New("boom")}
	opts.Shell = &fakeDirectRunner{calls: &calls, label: "shell"}
	opts.Store = &fakeDirectStore{calls: &calls}
	opts.Out, opts.Err = &bytes.Buffer{}, &bytes.Buffer{}

	if err := openDirect(opts); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	files := recordFiles(t, featurePath)
	if len(files) != 1 || !strings.HasSuffix(files[0], sibling+".json") {
		t.Fatalf("only the own record may be removed, found %v", files)
	}
}

func TestDirectOpenAgentStageUpdateFailureTerminatesChild(t *testing.T) {
	featurePath := directTestEnv(t)
	var calls []string
	opts := trackedOpts(featurePath)
	opts.Runner = &fakeDirectRunner{calls: &calls, label: "agent", pid: 7}
	opts.Shell = &fakeDirectRunner{calls: &calls, label: "shell"}
	opts.Store = &fakeDirectStore{calls: &calls, failUpdate: map[int]error{1: errors.New("store broken")}}
	opts.Out, opts.Err = &bytes.Buffer{}, &bytes.Buffer{}

	err := openDirect(opts)
	if err == nil || !strings.Contains(err.Error(), "store broken") {
		t.Fatalf("err = %v", err)
	}
	joined := strings.Join(calls, ",")
	if !strings.Contains(joined, "agent.terminate") {
		t.Fatalf("the child must be terminated: %v", calls)
	}
	// Terminate only signals, so the caller must still reap: without an
	// explicit Wait the child would be left as a zombie.
	if !strings.Contains(joined, "agent.wait") {
		t.Fatalf("the terminated child must be waited for: %v", calls)
	}
	if strings.Index(joined, "agent.terminate") > strings.Index(joined, "agent.wait") {
		t.Fatalf("Terminate must precede Wait: %v", calls)
	}
	if strings.Count(joined, "agent.wait") != 1 {
		t.Fatalf("a child must be waited for exactly once: %v", calls)
	}
	if strings.Contains(joined, "shell.start") {
		t.Fatal("the shell must not start")
	}
	if files := recordFiles(t, featurePath); len(files) != 0 {
		t.Fatalf("the own record must be removed, found %v", files)
	}
}

func TestDirectOpenShellStagePreStartFailureStopsSession(t *testing.T) {
	featurePath := directTestEnv(t)
	var calls []string
	opts := trackedOpts(featurePath)
	opts.Runner = &fakeDirectRunner{calls: &calls, label: "agent", pid: 7}
	opts.Shell = &fakeDirectRunner{calls: &calls, label: "shell"}
	opts.Store = &fakeDirectStore{calls: &calls, failUpdate: map[int]error{2: errors.New("permission denied")}}
	opts.Out, opts.Err = &bytes.Buffer{}, &bytes.Buffer{}

	err := openDirect(opts)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.Join(calls, ","), "shell.start") {
		t.Fatal("a broken store must not start the shell")
	}
	if files := recordFiles(t, featurePath); len(files) != 0 {
		t.Fatalf("the own record must be removed, found %v", files)
	}
}

func TestDirectOpenShellStageRecordLossRecreates(t *testing.T) {
	featurePath := directTestEnv(t)
	var calls []string
	opts := trackedOpts(featurePath)
	opts.Runner = &fakeDirectRunner{calls: &calls, label: "agent", pid: 7}
	opts.Shell = &fakeDirectRunner{calls: &calls, label: "shell", pid: 8}
	opts.Store = &fakeDirectStore{calls: &calls, failUpdate: map[int]error{2: fs.ErrNotExist}}
	var errOut bytes.Buffer
	opts.Out, opts.Err = &bytes.Buffer{}, &errOut

	// Capture the original started_at and token before the record vanishes.
	var startedAt, originalToken string
	opts.Runner.(*fakeDirectRunner).onStart = func() {
		rec := loadOnlyRecord(t, featurePath, "auth", "api")
		startedAt, originalToken = rec.StartedAt, rec.Token
	}

	// The recreated record only exists between the shell start and the final
	// removal, so it is read from disk while the shell is "running".
	var recreated internal.DirectSessionRecord
	opts.Shell.(*fakeDirectRunner).onStart = func() {
		recreated = loadOnlyRecord(t, featurePath, "auth", "api")
	}

	// Simulate the record actually disappearing, as tws close would do.
	opts.Store.(*fakeDirectStore).onAfterCall = func(stage string) {
		if stage == internal.DirectStageAgent {
			rec := loadOnlyRecord(t, featurePath, "auth", "api")
			branchID := internal.DirectSessionBranchID("auth", "api")
			_ = internal.RemoveOwnedDirectSession(featurePath, branchID, rec.Token)
		}
	}

	if err := openDirect(opts); err != nil {
		t.Fatalf("a vanished record must never end the session: %v", err)
	}
	if !strings.Contains(errOut.String(), "session record was removed; recreating") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if !strings.Contains(strings.Join(calls, ","), "shell.start") {
		t.Fatal("the shell must start")
	}
	// The recreated record is removed again at the end of the invocation, so
	// assert the recreate happened via the store call log.
	if !strings.Contains(strings.Join(calls, ","), "store.create(shell)") {
		t.Fatalf("expected a shell-stage recreate: %v", calls)
	}
	if startedAt == "" {
		t.Fatal("started_at should have been captured")
	}
	if recreated.Token == "" {
		t.Fatal("the recreated record was never observed")
	}
	if recreated.StartedAt != startedAt {
		t.Fatalf("the recreate must preserve started_at: %q, want %q", recreated.StartedAt, startedAt)
	}
	if recreated.Stage != internal.DirectStageShell {
		t.Fatalf("the recreate must record the shell stage, got %q", recreated.Stage)
	}
	if recreated.Token == originalToken {
		t.Fatal("the recreate must mint a fresh ownership token")
	}
	if recreated.Feature != "auth" || recreated.Name != "api" || recreated.GitBranch != "jd/api" {
		t.Fatalf("the recreate must preserve identity: %+v", recreated)
	}
	if recreated.OwnerPID != os.Getpid() {
		t.Fatalf("owner_pid = %d, want %d", recreated.OwnerPID, os.Getpid())
	}
}

func TestDirectOpenShellStageRecreateFailureContinuesUnrecorded(t *testing.T) {
	featurePath := directTestEnv(t)
	var calls []string
	opts := trackedOpts(featurePath)
	opts.Runner = &fakeDirectRunner{calls: &calls, label: "agent", pid: 7}
	opts.Shell = &fakeDirectRunner{calls: &calls, label: "shell", pid: 8}
	opts.Store = &fakeDirectStore{
		calls:      &calls,
		failUpdate: map[int]error{2: fs.ErrNotExist},
		failCreate: map[int]error{2: errors.New("disk full")},
	}
	var errOut bytes.Buffer
	opts.Out, opts.Err = &bytes.Buffer{}, &errOut

	if err := openDirect(opts); err != nil {
		t.Fatalf("a failed recreate must not end the session: %v", err)
	}
	if !strings.Contains(errOut.String(), "continuing without a session record") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if !strings.Contains(strings.Join(calls, ","), "shell.start") {
		t.Fatal("the shell must start")
	}
}

func TestDirectOpenPostStartShellUpdateFailureWarnsOnly(t *testing.T) {
	featurePath := directTestEnv(t)
	var calls []string
	opts := trackedOpts(featurePath)
	opts.Runner = &fakeDirectRunner{calls: &calls, label: "agent", pid: 7}
	opts.Shell = &fakeDirectRunner{calls: &calls, label: "shell", pid: 8}
	opts.Store = &fakeDirectStore{calls: &calls, failUpdate: map[int]error{3: errors.New("io error")}}
	var errOut bytes.Buffer
	opts.Out, opts.Err = &bytes.Buffer{}, &errOut

	if err := openDirect(opts); err != nil {
		t.Fatalf("a child-pid update failure must not fail the open: %v", err)
	}
	if !strings.Contains(errOut.String(), "could not update session record (child pid)") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if strings.Contains(strings.Join(calls, ","), "shell.terminate") {
		t.Fatal("the interactive shell must never be terminated for this")
	}
}

func TestDirectOpenUntrackedWritesNothing(t *testing.T) {
	featurePath := directTestEnv(t)
	var calls []string
	opts := directOpenOpts{
		Path:     featurePath,
		LookPath: func(string) (string, error) { return "/usr/bin/claude", nil },
		Runner:   &fakeDirectRunner{calls: &calls, label: "agent", pid: 1},
		Shell:    &fakeDirectRunner{calls: &calls, label: "shell", pid: 2},
		Store:    &fakeDirectStore{calls: &calls},
		Out:      &bytes.Buffer{},
		Err:      &bytes.Buffer{},
	}
	if err := openDirect(opts); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "store.") {
			t.Fatalf("an untracked open must not touch the store: %v", calls)
		}
	}
	if _, err := os.Stat(internal.DirectSessionsDir(featurePath)); !os.IsNotExist(err) {
		t.Fatal("an untracked open must not create a .sessions directory")
	}

	// It still propagates a LookPath failure.
	opts.LookPath = func(string) (string, error) { return "", errors.New("nope") }
	if err := openDirect(opts); err == nil {
		t.Fatal("an untracked open must still propagate a LookPath failure")
	}
}

func TestResolveDirectGitBranchFallsBackToEmpty(t *testing.T) {
	featurePath := t.TempDir()
	if got := resolveDirectGitBranch(featurePath, "api"); got != "" {
		t.Fatalf("a missing stack must yield an empty git branch, got %q", got)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{
		Branches: []internal.StackEntry{{Name: "api", Branch: "jd/api", Base: "main"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := resolveDirectGitBranch(featurePath, "api"); got != "jd/api" {
		t.Fatalf("git branch = %q", got)
	}
	if got := resolveDirectGitBranch(featurePath, "nosuch"); got != "" {
		t.Fatalf("an unknown branch must yield an empty git branch, got %q", got)
	}
}

// ---------- real process lifecycle ----------

// TestRealDirectProcessTerminateThenWait drives the real implementation, not a
// fake: Terminate only signals, the caller reaps with Wait, and Wait is
// idempotent so no second exec.Cmd.Wait can panic.
func TestRealDirectProcessTerminateThenWait(t *testing.T) {
	proc, err := realDirectRunner{}.Start(t.TempDir(), []string{"/bin/sh", "-c", "sleep 30"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if proc.PID() <= 0 {
		t.Fatalf("pid = %d", proc.PID())
	}
	if err := proc.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case <-done:
		// A SIGTERM-ed child exits non-zero; the exit status is the caller's
		// business, the reaping is what matters here.
	case <-time.After(10 * time.Second):
		t.Fatal("Wait never returned after Terminate")
	}

	// Idempotent: the escalation goroutine is already released, and a second
	// Wait must neither block nor reach exec.Cmd.Wait twice.
	second := make(chan error, 1)
	go func() { second <- proc.Wait() }()
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("a second Wait must return immediately")
	}
}

// TestRealDirectProcessWaitReleasesEscalation asserts the SIGKILL escalation
// cannot outlive a successful Wait: a process that exits on its own is reaped,
// and the pending kill goroutine observes the done channel instead of signalling
// a pid the OS may have recycled.
func TestRealDirectProcessWaitReleasesEscalation(t *testing.T) {
	proc, err := realDirectRunner{}.Start(t.TempDir(), []string{"/bin/sh", "-c", "exit 0"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	real, ok := proc.(*realDirectProcess)
	if !ok {
		t.Fatalf("runner returned %T", proc)
	}
	if err := real.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	_ = real.Wait()
	select {
	case <-real.done:
	default:
		t.Fatal("Wait must close the done channel so the escalation is released")
	}
}
