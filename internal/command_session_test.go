package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type commandTestRunner func(string, []string) error

func (f commandTestRunner) Run(dir string, command []string) error { return f(dir, command) }

type commandTestTmux struct {
	*testTmux
	tokens    map[string]string
	args      []string
	dir       string
	exists    func(string) error
	onCreate  func(string, string) error
	onAttach  func(string) error
	onOwned   func(string) error
	onKill    func(string) error
	createErr error
}

func newCommandTestTmux() *commandTestTmux {
	return &commandTestTmux{testTmux: newTestTmux(), tokens: map[string]string{}}
}

func (f *commandTestTmux) CommandSessionExists(name string) (bool, error) {
	if f.exists != nil {
		if err := f.exists(name); err != nil {
			return false, err
		}
	}
	return f.sessions[name], nil
}

func (f *commandTestTmux) NewCommandSession(name, dir string, command []string, token string) error {
	f.sessions[name], f.tokens[name] = true, token
	f.args, f.dir = append([]string(nil), command...), dir
	f.created = append(f.created, name)
	if f.onCreate != nil {
		if err := f.onCreate(name, token); err != nil {
			return err
		}
	}
	return f.createErr
}

func (f *commandTestTmux) OwnedCommandSession(name, token string) (bool, error) {
	if f.onOwned != nil {
		if err := f.onOwned(name); err != nil {
			return false, err
		}
	}
	if f.sessions[name] && f.tokens[name] != token {
		return false, errors.New("foreign tmux session owner")
	}
	return f.sessions[name], nil
}

func (f *commandTestTmux) AttachCommandSession(name, token string) error {
	if _, err := f.OwnedCommandSession(name, token); err != nil {
		return err
	}
	if f.onAttach != nil {
		return f.onAttach(name)
	}
	return f.attachErr
}

func (f *commandTestTmux) KillCommandSession(name, token string) error {
	if _, err := f.OwnedCommandSession(name, token); err != nil {
		return err
	}
	if f.onKill != nil {
		if err := f.onKill(name); err != nil {
			return err
		}
	}
	return f.KillSession(name)
}

func assertCheckoutCommandRetained(t *testing.T, ws Workspace, branch string) {
	t.Helper()
	if !HasCheckoutAgentSession(ws) {
		t.Fatal("command state was lost")
	}
	if _, err := os.Lstat(sessionLockDir(ws)); err != nil {
		t.Fatal("command lock was lost", err)
	}
	if got, _ := sessionCurrentBranch(ws.RepoRoot); got != branch {
		t.Fatalf("current branch=%q want=%q", got, branch)
	}
}

func assertCheckoutCommandCleaned(t *testing.T, ws Workspace) {
	t.Helper()
	if HasCheckoutAgentSession(ws) {
		t.Fatal("command state remains")
	}
	if _, err := os.Lstat(sessionLockDir(ws)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command lock remains: %v", err)
	}
	if got, _ := sessionCurrentBranch(ws.RepoRoot); got != "main" {
		t.Fatalf("not restored: %q", got)
	}
}

func TestCheckoutCommandDirectDurabilityAndObservation(t *testing.T) {
	repo, ws, entry := setupSessionRepo(t)
	context := filepath.Join(ws.FeaturePath("feature"), "inject", "context")
	if err := os.WriteFile(context, []byte("configured context"), 0600); err != nil {
		t.Fatal(err)
	}
	command := []string{"/absolute/editor", "", "literal ; argv", "line\nbreak", "--option"}
	calls := 0
	runner := commandTestRunner(func(dir string, args []string) error {
		calls++
		if dir != repo || !reflect.DeepEqual(args, command) {
			t.Fatalf("dir=%q argv=%q", dir, args)
		}
		state, err := LoadCheckoutAgentSession(ws)
		if err != nil || state.Stage != DirectStageCommand || state.GitBranch != entry.GitBranch() {
			t.Fatalf("state=%+v err=%v", state, err)
		}
		if _, err := loadCheckoutCommandOwnership(ws, state); err != nil {
			t.Fatal("state is not owned before child effects", err)
		}
		if got := gitS(t, repo, "branch", "--show-current"); got != entry.GitBranch() {
			t.Fatal("not activated", got)
		}
		if target, err := os.Readlink(filepath.Join(repo, "context")); err != nil || target != context {
			t.Fatalf("context target=%q err=%v", target, err)
		}
		report, err := BuildAgentStatusScoped(ws, "", "feature", nil)
		if err != nil {
			t.Fatal(err)
		}
		observation := report.Workspace.CheckoutSession
		if observation == nil || !observation.StageRecognized || observation.Stage == nil ||
			*observation.Stage != DirectStageCommand || observation.Agent != nil ||
			observation.AgentState != AgentStateUnknown || observation.Presence != PresencePresent {
			t.Fatalf("observation=%+v", observation)
		}
		raw, err := json.Marshal(report)
		if err != nil || bytes.Contains(raw, []byte(command[2])) || bytes.Contains(raw, []byte(state.LockToken)) {
			t.Fatalf("status leaked command/token or failed: %v", err)
		}
		if _, err := acquireAgentSessionLock(ws, newTestTmux()); err == nil {
			t.Fatal("second checkout owner admitted")
		}
		if err := CloseCheckoutSession(ws, "feature", entry.Name, nil); err == nil {
			t.Fatal("close interrupted a live direct command")
		}
		return nil
	})
	if err := OpenCheckoutCommand(ws, "feature", entry, command, runner, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("child calls=%d", calls)
	}
	assertCheckoutCommandCleaned(t, ws)
	if _, err := os.Lstat(filepath.Join(repo, "context")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("context remains: %v", err)
	}
}

func TestCheckoutCommandNonzeroRestores(t *testing.T) {
	_, ws, entry := setupSessionRepo(t)
	childErr := errors.New("exit status 9")
	err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, commandTestRunner(func(string, []string) error {
		return childErr
	}), nil, "", nil)
	if !errors.Is(err, childErr) {
		t.Fatalf("nonzero result=%v", err)
	}
	assertCheckoutCommandCleaned(t, ws)
}

func TestCheckoutCommandPersistencePrecedesActivationAndEffects(t *testing.T) {
	_, ws, entry := setupSessionRepo(t)
	failure := errors.New("disk full")
	CheckoutCommandStateWriteHook = func() error { return failure }
	t.Cleanup(func() { CheckoutCommandStateWriteHook = nil })
	calls := 0
	err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, commandTestRunner(func(string, []string) error {
		calls++
		return nil
	}), nil, "", nil)
	if !errors.Is(err, failure) || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	assertCheckoutCommandCleaned(t, ws)
}

func TestCheckoutCommandForeignStateAtPublication(t *testing.T) {
	_, ws, entry := setupSessionRepo(t)
	foreign := []byte(`{"foreign":"do not replace"}`)
	CheckoutCommandStateWriteHook = func() error {
		if err := os.MkdirAll(sessionStateDir(ws), 0700); err != nil {
			return err
		}
		return os.WriteFile(sessionStatePath(ws), foreign, 0600)
	}
	t.Cleanup(func() { CheckoutCommandStateWriteHook = nil })
	runner := &testAgent{}
	err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, runner, nil, "", nil)
	if err == nil || runner.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, runner.calls)
	}
	got, err := os.ReadFile(sessionStatePath(ws))
	if err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("foreign state overwritten: %q %v", got, err)
	}
	assertCheckoutCommandRetained(t, ws, "main")
}

func TestCheckoutCommandUnsafeCompletionRetainsEvidence(t *testing.T) {
	for _, change := range []string{"dirty", "detached", "branch", "original-head", "record", "lock", "link", "tracked-link"} {
		t.Run(change, func(t *testing.T) {
			repo, ws, entry := setupSessionRepo(t)
			inject := filepath.Join(ws.FeaturePath("feature"), "inject", "context")
			if err := os.WriteFile(inject, []byte("context"), 0600); err != nil {
				t.Fatal(err)
			}
			var recordAfter []byte
			wantBranch := entry.GitBranch()
			runner := commandTestRunner(func(string, []string) error {
				switch change {
				case "dirty":
					return os.WriteFile(filepath.Join(repo, "README"), []byte("user changes"), 0644)
				case "detached":
					gitS(t, repo, "switch", "--detach")
					wantBranch = ""
				case "branch":
					gitS(t, repo, "switch", "main")
					wantBranch = "main"
				case "original-head":
					gitS(t, repo, "commit", "--allow-empty", "-m", "new")
					gitS(t, repo, "branch", "-f", "main", "HEAD")
				case "record":
					state, err := LoadCheckoutAgentSession(ws)
					if err != nil {
						return err
					}
					state.OriginalBranch = "foreign"
					if err := SaveCheckoutAgentSession(ws, state); err != nil {
						return err
					}
					recordAfter, _ = os.ReadFile(sessionStatePath(ws))
				case "lock":
					return os.WriteFile(sessionLockOwnerPath(ws), []byte(`{"token":"foreign","pid":1}`), 0600)
				case "link":
					if err := os.Remove(filepath.Join(repo, "context")); err != nil {
						return err
					}
					return os.Symlink("foreign", filepath.Join(repo, "context"))
				case "tracked-link":
					gitS(t, repo, "add", "-f", "context")
					gitS(t, repo, "commit", "-m", "preserve context link")
				}
				return nil
			})
			err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, runner, nil, "", nil)
			if err == nil || !strings.Contains(err.Error(), "retained") {
				t.Fatalf("unsafe cleanup err=%v", err)
			}
			assertCheckoutCommandRetained(t, ws, wantBranch)
			if change != "link" {
				if target, err := os.Readlink(filepath.Join(repo, "context")); err != nil || target != inject {
					t.Fatalf("context was touched during unsafe cleanup: %q %v", target, err)
				}
			}
			if recordAfter != nil {
				got, _ := os.ReadFile(sessionStatePath(ws))
				if !bytes.Equal(got, recordAfter) {
					t.Fatal("foreign record changed")
				}
			}
		})
	}
}

func TestCheckoutCommandPreservesExistingExclusions(t *testing.T) {
	repo, ws, entry := setupSessionRepo(t)
	if err := os.WriteFile(filepath.Join(ws.FeaturePath("feature"), "inject", "context"), []byte("context"), 0600); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	if err := os.WriteFile(exclude, []byte(".tws/\ncontext\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, &testAgent{}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exclude)
	if err != nil || !bytes.Contains(data, []byte("context\n")) {
		t.Fatalf("preexisting exclude was removed: %q %v", data, err)
	}
}

func TestCheckoutCommandTmuxDurabilityLiteralArgsAndDetach(t *testing.T) {
	repo, ws, entry := setupSessionRepo(t)
	tmux := newCommandTestTmux()
	command := []string{"/editor path ;", "", "two words", "$(not evaluated)", "line\nbreak"}
	tmux.onCreate = func(name, token string) error {
		state, err := LoadCheckoutAgentSession(ws)
		if err != nil {
			return err
		}
		if state.Stage != DirectStageCommand || state.Mode != AgentSessionTmux || state.LockToken != token || state.TmuxSession != name {
			t.Fatalf("state before tmux child=%+v", state)
		}
		_, err = loadCheckoutCommandOwnership(ws, state)
		return err
	}
	if err := OpenCheckoutCommand(ws, "feature", entry, command, nil, tmux, "", nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tmux.args, command) || tmux.dir != repo || len(tmux.created) != 1 {
		t.Fatalf("tmux argv=%q dir=%q creates=%v", tmux.args, tmux.dir, tmux.created)
	}
	assertCheckoutCommandRetained(t, ws, entry.GitBranch())
	if err := CloseCheckoutSession(ws, "feature", entry.Name, tmux); err != nil {
		t.Fatal(err)
	}
	if len(tmux.killed) != 1 {
		t.Fatalf("killed=%v", tmux.killed)
	}
	assertCheckoutCommandCleaned(t, ws)
}

func TestCheckoutCommandTmuxEarlyExit(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "dirty"}[dirty], func(t *testing.T) {
			repo, ws, entry := setupSessionRepo(t)
			tmux := newCommandTestTmux()
			tmux.onCreate = func(name, token string) error {
				if !HasCheckoutAgentSession(ws) {
					t.Fatal("early command ran without durable state")
				}
				delete(tmux.sessions, name)
				if dirty {
					return os.WriteFile(filepath.Join(repo, "README"), []byte("early child effects"), 0644)
				}
				return nil
			}
			err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, nil, tmux, "", nil)
			if dirty {
				if err == nil || !strings.Contains(err.Error(), "dirty") {
					t.Fatalf("dirty early exit=%v", err)
				}
				assertCheckoutCommandRetained(t, ws, entry.GitBranch())
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertCheckoutCommandCleaned(t, ws)
			}
			if len(tmux.killed) != 0 {
				t.Fatalf("early exit killed a session: %v", tmux.killed)
			}
		})
	}
}

func TestCheckoutCommandTmuxAttachFailureAndCompletion(t *testing.T) {
	for _, outcome := range []string{"live-failure", "ended-failure", "ended-success"} {
		t.Run(outcome, func(t *testing.T) {
			_, ws, entry := setupSessionRepo(t)
			tmux := newCommandTestTmux()
			tmux.onAttach = func(name string) error {
				if strings.HasPrefix(outcome, "ended") {
					delete(tmux.sessions, name)
				}
				if strings.HasSuffix(outcome, "failure") {
					return errors.New("attach failed")
				}
				return nil
			}
			err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, nil, tmux, "", nil)
			if outcome == "ended-success" && err != nil || strings.HasSuffix(outcome, "failure") && err == nil {
				t.Fatalf("%s: %v", outcome, err)
			}
			if outcome == "live-failure" {
				assertCheckoutCommandRetained(t, ws, entry.GitBranch())
			} else {
				assertCheckoutCommandCleaned(t, ws)
			}
			if len(tmux.killed) != 0 {
				t.Fatal("attach cleanup killed a session")
			}
		})
	}
}

func TestCheckoutCommandTmuxCollisionAndUncertainty(t *testing.T) {
	for _, when := range []string{"before", "admission", "creation"} {
		t.Run(when, func(t *testing.T) {
			_, ws, entry := setupSessionRepo(t)
			tmux := newCommandTestTmux()
			name := CheckoutAgentSessionName(ws.StableID, "feature", entry.Name)
			switch when {
			case "before":
				tmux.sessions[name] = true
			case "admission":
				CheckoutSessionLaunchIntentHook = func() error {
					tmux.sessions[name] = true
					return nil
				}
				t.Cleanup(func() { CheckoutSessionLaunchIntentHook = nil })
			default:
				tmux.onCreate = func(name, token string) error {
					tmux.tokens[name] = "foreign"
					return errors.New("creation response lost")
				}
			}
			if err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, nil, tmux, "", nil); err == nil {
				t.Fatal("collision/uncertainty accepted")
			}
			if len(tmux.killed) != 0 || !tmux.sessions[name] {
				t.Fatal("preexisting/uncertain session was killed")
			}
			if when == "creation" {
				assertCheckoutCommandRetained(t, ws, entry.GitBranch())
				if err := CloseCheckoutSession(ws, "feature", entry.Name, tmux); err == nil {
					t.Fatal("close accepted a foreign tmux session")
				}
			} else {
				if len(tmux.created) != 0 {
					t.Fatal("collision launched command")
				}
				assertCheckoutCommandCleaned(t, ws)
			}
		})
	}
}

func TestCheckoutCommandCloseChecksOwnershipBeforeKill(t *testing.T) {
	for _, which := range []string{"record", "lock", "missing-tmux", "wrong-tmux", "missing-pid"} {
		t.Run(which, func(t *testing.T) {
			_, ws, entry := setupSessionRepo(t)
			tmux := newCommandTestTmux()
			if err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, nil, tmux, "", nil); err != nil {
				t.Fatal(err)
			}
			state, err := LoadCheckoutAgentSession(ws)
			if err != nil {
				t.Fatal(err)
			}
			if which == "lock" {
				if err := os.WriteFile(sessionLockOwnerPath(ws), []byte(`{"token":"foreign","pid":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				switch which {
				case "record":
					state.LockToken = "foreign"
				case "missing-tmux":
					state.TmuxSession = ""
				case "wrong-tmux":
					state.TmuxSession = "foreign"
				case "missing-pid":
					state.PID = 0
				}
				if err := SaveCheckoutAgentSession(ws, state); err != nil {
					t.Fatal(err)
				}
			}
			if err := CloseCheckoutSession(ws, "feature", entry.Name, tmux); err == nil {
				t.Fatal("foreign state accepted by close")
			}
			if len(tmux.killed) != 0 {
				t.Fatal("close killed before checking ownership")
			}
			assertCheckoutCommandRetained(t, ws, entry.GitBranch())
		})
	}
}

func TestCheckoutCommandLateFinalGuard(t *testing.T) {
	_, ws, entry := setupSessionRepo(t)
	runner := &testAgent{}
	checks := 0
	guard := func() error {
		checks++
		if checks == 3 {
			return errors.New("late guard refusal")
		}
		return nil
	}
	err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, runner, nil, "", guard)
	if err == nil || !strings.Contains(err.Error(), "late guard") || runner.calls != 0 {
		t.Fatalf("err=%v calls=%d checks=%d", err, runner.calls, checks)
	}
	assertCheckoutCommandCleaned(t, ws)
}

func TestCheckoutCommandFeatureDirForeignIntentRefuses(t *testing.T) {
	_, ws, _ := setupSessionRepo(t)
	CheckoutSessionLaunchIntentHook = func() error {
		return os.WriteFile(sessionLockOwnerPath(ws), []byte(`{"token":"foreign","pid":1}`), 0600)
	}
	t.Cleanup(func() { CheckoutSessionLaunchIntentHook = nil })
	launched := false
	err := WithCheckoutCommandLaunchIntent(ws, func() error {
		return CheckoutFeatureDirSessionPreconditions(ws)
	}, func() error {
		launched = true
		return nil
	})
	if err == nil || launched {
		t.Fatalf("err=%v launched=%v", err, launched)
	}
	owner, err := os.ReadFile(sessionLockOwnerPath(ws))
	if err != nil || !bytes.Contains(owner, []byte("foreign")) {
		t.Fatalf("foreign intent was removed: %s %v", owner, err)
	}
}

func TestCheckoutCommandInProgressFromOtherCWD(t *testing.T) {
	repo, ws, entry := setupSessionRepo(t)
	gitDir := filepath.Join(repo, ".git")
	head := gitS(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(gitDir, "CHERRY_PICK_HEAD"), []byte(head+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	runner := &testAgent{}
	err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, runner, nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "Git operation") || runner.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, runner.calls)
	}
	assertCheckoutCommandCleaned(t, ws)
}

func TestCheckoutCommandUnsafeStateDirectory(t *testing.T) {
	_, ws, entry := setupSessionRepo(t)
	foreign := t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(ws.MetadataRoot, "state")); err != nil {
		t.Fatal(err)
	}
	runner := &testAgent{}
	if err := OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, runner, nil, "", nil); err == nil || runner.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, runner.calls)
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 {
		t.Fatalf("foreign state directory changed: %v %v", entries, err)
	}
}

func TestCheckoutCommandPrepublicationOwnershipRetained(t *testing.T) {
	for _, branchless := range []bool{false, true} {
		for _, failure := range []string{"late-pid", "late-error", "capture-pid", "capture-error", "capture-invalid"} {
			t.Run(fmt.Sprintf("branchless=%t/%s", branchless, failure), func(t *testing.T) {
				_, ws, entry := setupSessionRepo(t)
				var expected checkoutSessionIntentSnapshot
				var token string
				changeOwner := func() error {
					before, present, err := loadCheckoutSessionIntentSnapshot(ws)
					if err != nil || !present {
						t.Fatalf("initial intent unavailable: %t %v", present, err)
					}
					owner, err := before.decodeOwner()
					if err != nil {
						t.Fatal(err)
					}
					token = owner.Token
					owner.PID = 1
					data, err := json.Marshal(owner)
					if err != nil {
						t.Fatal(err)
					}
					if failure == "capture-invalid" {
						data = append(data, []byte("invalid residue")...)
					}
					if err := os.WriteFile(sessionLockOwnerPath(ws), data, 0600); err != nil {
						t.Fatal(err)
					}
					expected, present, err = loadCheckoutSessionIntentSnapshot(ws)
					if err != nil || !present {
						t.Fatalf("changed intent unavailable: %t %v", present, err)
					}
					if strings.HasSuffix(failure, "error") {
						return errors.New("injected early failure")
					}
					return nil
				}
				if strings.HasPrefix(failure, "capture-") {
					CheckoutCommandIntentCaptureHook = changeOwner
				} else {
					CheckoutSessionLaunchIntentHook = changeOwner
				}
				t.Cleanup(func() {
					CheckoutCommandIntentCaptureHook = nil
					CheckoutSessionLaunchIntentHook = nil
				})
				runner := &testAgent{}
				var err error
				if branchless {
					err = WithCheckoutCommandLaunchIntent(ws, func() error {
						return CheckoutFeatureDirSessionPreconditions(ws)
					}, func() error {
						return runner.Run(ws.RepoRoot, []string{"/editor"})
					})
				} else {
					err = OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, runner, nil, "", nil)
				}
				if err == nil || runner.calls != 0 {
					t.Fatalf("err=%v child calls=%d", err, runner.calls)
				}
				after, present, readErr := loadCheckoutSessionIntentSnapshot(ws)
				if readErr != nil || !present || !os.SameFile(expected.dirInfo, after.dirInfo) ||
					!os.SameFile(expected.ownerInfo, after.ownerInfo) || !bytes.Equal(expected.ownerBytes, after.ownerBytes) {
					t.Fatalf("changed ownership evidence was modified: present=%t err=%v", present, readErr)
				}
				if !bytes.Contains(after.ownerBytes, []byte(token)) {
					t.Fatal("the regression must keep the original token")
				}
				if _, statErr := os.Lstat(sessionStatePath(ws)); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("session state was published: %v", statErr)
				}
				if branch, branchErr := sessionCurrentBranch(ws.RepoRoot); branchErr != nil || branch != "main" {
					t.Fatalf("checkout activated: %q %v", branch, branchErr)
				}
			})
		}
	}

}

func TestCheckoutCommandInitialCaptureFailureRetainsPaths(t *testing.T) {
	for _, branchless := range []bool{false, true} {
		t.Run(fmt.Sprint(branchless), func(t *testing.T) {
			_, ws, entry := setupSessionRepo(t)
			var dirInfo, ownerInfo os.FileInfo
			var before []byte
			kept := filepath.Join(sessionLockDir(ws), "retained-owner.json")
			CheckoutCommandIntentCaptureHook = func() error {
				var err error
				before, err = os.ReadFile(sessionLockOwnerPath(ws))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(sessionLockOwnerPath(ws), kept); err != nil {
					t.Fatal(err)
				}
				dirInfo, err = os.Lstat(sessionLockDir(ws))
				if err != nil {
					t.Fatal(err)
				}
				ownerInfo, err = os.Lstat(kept)
				if err != nil {
					t.Fatal(err)
				}
				return nil
			}
			t.Cleanup(func() { CheckoutCommandIntentCaptureHook = nil })
			runner := &testAgent{}
			var err error
			if branchless {
				err = WithCheckoutCommandLaunchIntent(ws, nil, func() error {
					return runner.Run(ws.RepoRoot, []string{"/editor"})
				})
			} else {
				err = OpenCheckoutCommand(ws, "feature", entry, []string{"/editor"}, runner, nil, "", nil)
			}
			if err == nil || !strings.Contains(err.Error(), "could not be captured") || runner.calls != 0 {
				t.Fatalf("err=%v child calls=%d", err, runner.calls)
			}
			afterDir, err := os.Lstat(sessionLockDir(ws))
			if err != nil || !os.SameFile(dirInfo, afterDir) {
				t.Fatalf("uncaptured intent directory changed: %v", err)
			}
			afterOwner, err := os.Lstat(kept)
			if err != nil || !os.SameFile(ownerInfo, afterOwner) {
				t.Fatalf("retained owner identity changed: %v", err)
			}
			after, err := os.ReadFile(kept)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("retained owner bytes changed: %v", err)
			}
			if _, err := os.Lstat(sessionLockOwnerPath(ws)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owner path was recreated: %v", err)
			}
			if HasCheckoutAgentSession(ws) {
				t.Fatal("command state was published without captured ownership")
			}
			if current, err := sessionCurrentBranch(ws.RepoRoot); err != nil || current != "main" {
				t.Fatalf("checkout activated: %q %v", current, err)
			}
		})
	}
}
