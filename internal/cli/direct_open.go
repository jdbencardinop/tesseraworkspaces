package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// directProcess is a started child process. It is start/wait shaped rather
// than run shaped because the session record needs the child PID between
// start and wait.
//
// Terminate only signals; the caller always reaps with Wait, so a process is
// waited for exactly once whichever path ends the session.
type directProcess interface {
	PID() int
	Wait() error
	Terminate() error
}

// directRunner starts a process in a directory.
type directRunner interface {
	Start(dir string, command []string) (directProcess, error)
}

// directSessionStore is the record persistence seam.
type directSessionStore interface {
	Create(featurePath string, rec internal.DirectSessionRecord) (string, error)
	Update(featurePath, branchID, token string, mutate func(*internal.DirectSessionRecord)) error
	RemoveOwned(featurePath, branchID, token string) error
}

// directOpenOpts fully describes one direct open. An empty Feature or Name
// means an untracked open: no record is created, read, updated, or removed.
type directOpenOpts struct {
	Path        string
	Feature     string
	Name        string
	GitBranch   string
	FeaturePath string
	Runner      directRunner
	Shell       directRunner
	LookPath    func(string) (string, error)
	Store       directSessionStore
	Out         io.Writer
	Err         io.Writer
}

// ---------- real implementations ----------

// directTerminateGrace is the bounded window between SIGTERM and SIGKILL.
const directTerminateGrace = 5 * time.Second

// realDirectProcess wraps one started child. Wait is idempotent because the
// caller — not Terminate — owns reaping: Terminate only signals, so a
// terminated child is still waited for exactly once by openDirect.
type realDirectProcess struct {
	cmd      *exec.Cmd
	waitOnce sync.Once
	waitErr  error
	done     chan struct{}
	doneOnce sync.Once
}

func newRealDirectProcess(cmd *exec.Cmd) *realDirectProcess {
	return &realDirectProcess{cmd: cmd, done: make(chan struct{})}
}

func (p *realDirectProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Wait reaps the child exactly once and releases any pending SIGKILL
// escalation, so no kill can land on a pid the OS has already recycled.
func (p *realDirectProcess) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		p.doneOnce.Do(func() { close(p.done) })
	})
	return p.waitErr
}

// Terminate sends SIGTERM and arms a bounded escalation to SIGKILL. It never
// waits: waiting here would race the caller's own Wait for the same child, and
// exec.Cmd.Wait is not safe to call twice. The escalation goroutine exits
// immediately once Wait completes, so a child that honoured SIGTERM is never
// killed late.
func (p *realDirectProcess) Terminate() error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	go func() {
		timer := time.NewTimer(directTerminateGrace)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
			_ = p.cmd.Process.Kill()
		}
	}()
	return nil
}

type realDirectRunner struct{}

func (realDirectRunner) Start(dir string, command []string) (directProcess, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return newRealDirectProcess(cmd), nil
}

type realDirectSessionStore struct{}

func (realDirectSessionStore) Create(featurePath string, rec internal.DirectSessionRecord) (string, error) {
	return internal.CreateDirectSession(featurePath, rec)
}

func (realDirectSessionStore) Update(featurePath, branchID, token string, mutate func(*internal.DirectSessionRecord)) error {
	return internal.UpdateDirectSession(featurePath, branchID, token, mutate)
}

func (realDirectSessionStore) RemoveOwned(featurePath, branchID, token string) error {
	return internal.RemoveOwnedDirectSession(featurePath, branchID, token)
}

// resolveDirectGitBranch resolves the Git branch of a tracked open from
// stack.yaml. A missing or unreadable stack yields the empty string and the
// open proceeds: refusing to open an agent because an advisory metadata file
// is unreadable would make the record a hard dependency of the primary
// workflow.
func resolveDirectGitBranch(featurePath, name string) string {
	if featurePath == "" || name == "" {
		return ""
	}
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return ""
	}
	entry := internal.GetBranch(stack, name)
	if entry.Name == "" {
		return ""
	}
	return entry.GitBranch()
}

// openDirect runs the configured agent in a directory, then drops the user
// into an interactive shell there.
//
// For a tracked open it also maintains one per-invocation direct session
// record: created before anything is spawned, updated at each stage
// transition, and removed by token on normal exit.
func openDirect(opts directOpenOpts) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	errOut := opts.Err
	if errOut == nil {
		errOut = os.Stderr
	}
	runner := opts.Runner
	if runner == nil {
		runner = realDirectRunner{}
	}
	shellRunner := opts.Shell
	if shellRunner == nil {
		shellRunner = realDirectRunner{}
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	store := opts.Store
	if store == nil {
		store = realDirectSessionStore{}
	}
	tracked := opts.Feature != "" && opts.Name != "" && opts.FeaturePath != ""

	// Step 1: resolve the agent command before anything is written, so a
	// missing agent binary never leaves a record behind.
	cfg := internal.LoadConfig()
	agentCmd := cfg.GetAgentCommand()
	parts := strings.Fields(agentCmd)
	if len(parts) == 0 {
		return fmt.Errorf("agent_command is empty; set agent_command in .tws/config.yaml")
	}
	if isClaudeAgent(agentCmd) && hasClaudeSession(opts.Path) {
		agentCmd += " -c"
		parts = strings.Fields(agentCmd)
	}
	if _, err := lookPath(parts[0]); err != nil {
		return fmt.Errorf("agent %q not found in PATH", parts[0])
	}

	// Step 2: create the record before spawning anything, so the window in
	// which a live child is unrecorded is closed by construction.
	var branchID, token string
	startedAt := ""
	if tracked {
		branchID = internal.DirectSessionBranchID(opts.Feature, opts.Name)
		rec := internal.DirectSessionRecord{
			Feature:   opts.Feature,
			Name:      opts.Name,
			GitBranch: opts.GitBranch,
			Path:      opts.Path,
			Agent:     parts[0],
			Stage:     internal.DirectStageStarting,
		}
		created, err := store.Create(opts.FeaturePath, rec)
		if err != nil {
			return err
		}
		token = created
	}
	removeOwned := func() {
		if !tracked || token == "" {
			return
		}
		if err := store.RemoveOwned(opts.FeaturePath, branchID, token); err != nil {
			_, _ = fmt.Fprintf(errOut, "Warning: could not remove session record: %v\n", err)
		}
	}

	// Step 3-4: announce and start the agent.
	_, _ = fmt.Fprintf(out, "Opening: %s\nRunning: %s\n", opts.Path, agentCmd)
	agent, err := runner.Start(opts.Path, parts)
	if err != nil {
		removeOwned()
		return err
	}

	// Step 5: record the agent stage. A persistence failure this early means
	// the store is broken for the session's whole lifetime and the agent has
	// produced no interactive state worth preserving.
	if tracked {
		childPID := agent.PID()
		if err := store.Update(opts.FeaturePath, branchID, token, func(rec *internal.DirectSessionRecord) {
			rec.Stage = internal.DirectStageAgent
			rec.ChildPID = childPID
			startedAt = rec.StartedAt
		}); err != nil {
			// Terminate signals; Wait reaps. Both are explicit here so the
			// child is never left as a zombie and no kill can land after the
			// pid has been released.
			termErr := agent.Terminate()
			_ = agent.Wait()
			removeOwned()
			return errors.Join(err, termErr)
		}
	}

	// Step 6: a non-zero agent exit does not abort the shell transition.
	if waitErr := agent.Wait(); waitErr != nil {
		_, _ = fmt.Fprintf(out, "Agent exited: %v\n", waitErr)
	}

	// Step 7: record the shell stage before starting the shell.
	if tracked {
		err := store.Update(opts.FeaturePath, branchID, token, func(rec *internal.DirectSessionRecord) {
			rec.Stage = internal.DirectStageShell
			rec.ChildPID = 0
			if startedAt == "" {
				startedAt = rec.StartedAt
			}
		})
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist):
			// A benign race: close, rename, archive, delete, or an operator
			// removed a record believed stale while the owner was between
			// stages. Never a reason to end an interactive session.
			_, _ = fmt.Fprintln(errOut, "Warning: session record was removed; recreating")
			recreated, createErr := store.Create(opts.FeaturePath, internal.DirectSessionRecord{
				Feature:   opts.Feature,
				Name:      opts.Name,
				GitBranch: opts.GitBranch,
				Path:      opts.Path,
				Agent:     parts[0],
				Stage:     internal.DirectStageShell,
				StartedAt: startedAt,
			})
			if createErr != nil {
				_, _ = fmt.Fprintf(errOut, "Warning: continuing without a session record: %v\n", createErr)
				tracked = false
				token = ""
			} else {
				token = recreated
			}
		default:
			removeOwned()
			return err
		}
	}

	// Step 8: start the shell so the user stays in the worktree.
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	_, _ = fmt.Fprintf(out, "Dropped into shell at: %s\n", opts.Path)
	sh, err := shellRunner.Start(opts.Path, []string{shell})
	if err != nil {
		removeOwned()
		return err
	}

	// Step 9: child_pid is detail only and owner_pid is already correct, so a
	// failure here warns rather than terminating a shell the user is typing
	// in. This is the one deliberate asymmetry with step 5.
	if tracked {
		shellPID := sh.PID()
		if err := store.Update(opts.FeaturePath, branchID, token, func(rec *internal.DirectSessionRecord) {
			rec.ChildPID = shellPID
		}); err != nil {
			_, _ = fmt.Fprintf(errOut, "Warning: could not update session record (child pid): %v\n", err)
		}
	}

	// Step 10: wait, then remove exactly the token-owned file.
	_ = sh.Wait()
	removeOwned()
	return nil
}

// ---------- lifecycle record guard ----------

// directRecordTargetsForFeature enumerates every <branch-id> directory of a
// feature with a nil identity, which is what a feature-wide scan needs: it
// does not know which branch each directory belongs to and must not fabricate
// one.
func directRecordTargetsForFeature(featurePath string) ([]internal.DirectSessionTarget, error) {
	inventory, err := internal.ListDirectSessions(featurePath)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(inventory))
	for id := range inventory {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	targets := make([]internal.DirectSessionTarget, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, internal.DirectSessionTarget{BranchID: id})
	}
	return targets, nil
}

// directRecordTargetForBranch is the single-branch form, which supplies an
// identity so a hash-collided record from another branch surfaces as invalid
// instead of being treated as this branch's record.
func directRecordTargetForBranch(feature, name string) []internal.DirectSessionTarget {
	return []internal.DirectSessionTarget{{
		BranchID: internal.DirectSessionBranchID(feature, name),
		Want:     &internal.DirectSessionIdentity{Feature: feature, Name: name},
	}}
}

// guardDirectRecords refuses a destructive operation while any record is not
// provably dead, then removes the provably stale ones.
//
// Unlike tws close, unknown (EPERM) and invalid records block here: close is
// non-destructive to identity, while rename, archive, and delete destroy or
// relocate it, so anything not provably dead must block.
//
// The cleanup line is written through out rather than to process stdout so a
// test can assert it without capturing a global.
func guardDirectRecords(out io.Writer, featurePath, verb, subject string, targets []internal.DirectSessionTarget) error {
	if out == nil {
		out = os.Stdout
	}
	if len(targets) == 0 {
		return nil
	}
	blocking, stale, err := internal.GuardDirectSessionsFor(featurePath, targets, nil)
	if err != nil {
		return err
	}
	if len(blocking) > 0 {
		var b strings.Builder
		_, _ = fmt.Fprintf(&b, "cannot %s %s: %d direct session record(s) are live or unverifiable; tws never kills a direct process",
			verb, subject, len(blocking))
		for _, rec := range blocking {
			_, _ = fmt.Fprintf(&b, "\n  %s", internal.DescribeDirectSession(rec))
		}
		b.WriteString("\nexit the session(s), then retry; inspect with: tws status --json")
		return errors.New(b.String())
	}
	removed, err := internal.RemoveStaleDirectSessions(featurePath, stale)
	if err != nil {
		return err
	}
	if removed > 0 {
		_, _ = fmt.Fprintf(out, "Removed %d stale direct session record(s).\n", removed)
	}
	return nil
}
