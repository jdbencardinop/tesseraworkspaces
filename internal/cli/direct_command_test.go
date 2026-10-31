package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

type directCommandTestProcess struct {
	wait func() error
}

func (p directCommandTestProcess) PID() int         { return os.Getpid() }
func (p directCommandTestProcess) Wait() error      { return p.wait() }
func (p directCommandTestProcess) Terminate() error { return nil }

type directCommandTestRunner func(string, []string) (directProcess, error)

func (r directCommandTestRunner) Start(path string, command []string) (directProcess, error) {
	return r(path, command)
}

func TestDirectCommandLiveRecordAndMutationExclusion(t *testing.T) {
	f := newInjectFixture(t, "feature")
	path := f.addWorktree(t, "", "one", "actual")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "one", Branch: "actual", Base: "main"}}}); err != nil {
		t.Fatal(err)
	}
	opts := directCommandOptions{Path: path, FeaturePath: f.featurePath, Feature: f.feature, Name: "one", GitBranch: "actual",
		Command: []string{"/editor", "private literal argv"}}
	calls := 0
	opts.Runner = directCommandTestRunner(func(string, []string) (directProcess, error) {
		calls++
		record := loadOnlyRecord(t, f.featurePath, f.feature, "one")
		if record.Stage != internal.DirectStageCommand || record.Agent != "" {
			t.Fatalf("pre-spawn record=%+v", record)
		}
		return directCommandTestProcess{wait: func() error {
			record := loadOnlyRecord(t, f.featurePath, f.feature, "one")
			if record.ChildPID != os.Getpid() {
				t.Fatal("child PID missing")
			}
			blocking, _, err := internal.GuardDirectSessionsFor(f.featurePath, directRecordTargetForBranch(f.feature, "one"), nil)
			if err != nil || len(blocking) != 1 {
				t.Fatalf("mutation exclusion: blocking=%v err=%v", blocking, err)
			}
			report, err := internal.BuildAgentStatusScoped(internal.Workspace{
				Mode: internal.ModeExternal, RepoRoot: f.repo, MetadataRoot: f.workspace,
			}, "", f.feature, nil)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(report)
			if err != nil || !bytes.Contains(data, []byte(`"stage":"command"`)) ||
				!bytes.Contains(data, []byte(`"stage_recognized":true`)) ||
				!bytes.Contains(data, []byte(`"agent_state":"unknown"`)) ||
				!bytes.Contains(data, []byte(`"agent":null`)) ||
				bytes.Contains(data, []byte(opts.Command[1])) || bytes.Contains(data, []byte(record.Token)) {
				t.Fatalf("command observation invalid or leaked: %s %v", data, err)
			}
			return nil
		}}, nil
	})
	if err := openDirectCommand(opts); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(recordFiles(t, f.featurePath)) != 0 {
		t.Fatalf("calls=%d files=%v", calls, recordFiles(t, f.featurePath))
	}
}

func TestDirectCommandPersistenceFailures(t *testing.T) {
	for _, stage := range []string{"create", "before-spawn", "after-spawn", "remove"} {
		t.Run(stage, func(t *testing.T) {
			f := newInjectFixture(t, "feature")
			path := f.addWorktree(t, "", "one", "actual")
			var calls []string
			store := &fakeDirectStore{calls: &calls}
			broken := errors.New("store failed")
			switch stage {
			case "create":
				store.failCreate = map[int]error{1: broken}
			case "before-spawn":
				store.failUpdate = map[int]error{1: broken}
			case "after-spawn":
				store.failUpdate = map[int]error{2: broken}
			case "remove":
				store.failUpdate = map[int]error{3: broken}
			}
			opts := directCommandOptions{
				Path: path, FeaturePath: f.featurePath, Feature: f.feature, Name: "one", Command: []string{"/editor"},
				Store: store, Runner: &fakeDirectRunner{calls: &calls, label: "command", pid: os.Getpid()},
			}
			err := openDirectCommand(opts)
			if !errors.Is(err, broken) {
				t.Fatalf("err=%v calls=%v", err, calls)
			}
			text := strings.Join(calls, "\n")
			if (stage == "create" || stage == "before-spawn") && strings.Contains(text, "command.start") {
				t.Fatalf("spawn after persistence refusal: %v", calls)
			}
			if stage == "after-spawn" && (!strings.Contains(text, "command.terminate") || strings.Count(text, "command.wait") != 1) {
				t.Fatalf("failed update did not terminate/reap exactly once: %v", calls)
			}
			if stage == "remove" && len(recordFiles(t, f.featurePath)) != 1 {
				t.Fatal("unverifiable record was deleted")
			}
			if strings.Contains(text, "shell") || strings.Contains(text, "agent") {
				t.Fatalf("command used agent/shell: %v", calls)
			}
		})
	}
}

func TestDirectCommandForeignRecordBeforeLaunch(t *testing.T) {
	f := newInjectFixture(t, "feature")
	path := f.addWorktree(t, "", "one", "actual")
	var foreignPath string
	DirectSessionLaunchIntentHook = func() error {
		files := recordFiles(t, f.featurePath)
		foreignPath = filepath.Join(internal.DirectSessionsDir(f.featurePath), files[0])
		record := loadOnlyRecord(t, f.featurePath, f.feature, "one")
		record.Token = "foreign"
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return os.WriteFile(foreignPath, data, 0600)
	}
	t.Cleanup(func() { DirectSessionLaunchIntentHook = nil })
	var calls []string
	err := openDirectCommand(directCommandOptions{
		Path: path, FeaturePath: f.featurePath, Feature: f.feature, Name: "one", Command: []string{"/editor"},
		Runner: &fakeDirectRunner{calls: &calls, label: "command"},
	})
	if err == nil || len(calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, calls)
	}
	data, err := os.ReadFile(foreignPath)
	if err != nil || !bytes.Contains(data, []byte(`"token":"foreign"`)) {
		t.Fatalf("foreign record lost: %s %v", data, err)
	}
}

func TestDirectCommandExitFailureHasNoFollowup(t *testing.T) {
	f := newInjectFixture(t, "feature")
	path := f.addWorktree(t, "", "one", "actual")
	var calls []string
	childErr := errors.New("exit status 31")
	err := openDirectCommand(directCommandOptions{
		Path: path, FeaturePath: f.featurePath, Feature: f.feature, Name: "one", Command: []string{"/editor"},
		Runner: &fakeDirectRunner{calls: &calls, label: "command", waitErr: childErr},
	})
	if !errors.Is(err, childErr) || len(calls) != 2 || !strings.HasPrefix(calls[0], "command.start") || calls[1] != "command.wait" {
		t.Fatalf("err=%v calls=%v", err, calls)
	}
	assertNotExists(t, internal.DirectSessionsDir(f.featurePath))
}

func TestDirectCommandNULRefusesBeforePersistence(t *testing.T) {
	f := newInjectFixture(t, "feature")
	path := f.addWorktree(t, "", "one", "actual")
	var calls []string
	err := openDirectCommand(directCommandOptions{
		Path: path, FeaturePath: f.featurePath, Feature: f.feature, Name: "one", Command: []string{"/editor", "bad\x00argument"},
		Store: &fakeDirectStore{calls: &calls}, Runner: &fakeDirectRunner{calls: &calls, label: "command"},
	})
	if err == nil || len(calls) != 0 {
		t.Fatalf("err=%v effects=%v", err, calls)
	}
	assertNotExists(t, internal.DirectSessionsDir(f.featurePath))
}
