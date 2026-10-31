package cli

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

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func runOpenModeCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := openCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(args)
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	return out, err
}

func setupOpenCheckoutCLI(t *testing.T) (string, internal.Workspace) {
	t.Helper()
	repo, ws := setupSessionCLI(t)
	repo = canonicalOpenFixturePath(t, repo)
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte(".tws/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return repo, ws
}

func canonicalOpenFixturePath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func newOpenInjectFixture(t *testing.T, feature string) *injectFixture {
	t.Helper()
	f := newInjectFixture(t, feature)
	for _, path := range []*string{&f.root, &f.home, &f.repo, &f.workspace, &f.featurePath} {
		*path = canonicalOpenFixturePath(t, *path)
	}
	// Declare one physical fixture spelling, independent of macOS's /var
	// alias. Output and child argv assertions below remain exact.
	writeInjectTWSConfig(t, f.home, f.repo, f.workspace, "")
	t.Setenv("HOME", f.home)
	t.Setenv("TWS_ROOT", filepath.Join(f.root, "decoy-workspace"))
	return f
}

type openPathSnapshot struct {
	info os.FileInfo
	data string
}

func snapshotOpenPaths(t *testing.T, root string) map[string]openPathSnapshot {
	t.Helper()
	snapshot := map[string]openPathSnapshot{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		var data []byte
		if info.Mode().IsRegular() {
			data, err = os.ReadFile(path)
		} else if info.Mode()&os.ModeSymlink != 0 {
			var target string
			target, err = os.Readlink(path)
			data = []byte(target)
		}
		if err != nil {
			return err
		}
		snapshot[path] = openPathSnapshot{info: info, data: string(data)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertOpenPathsUnchanged(t *testing.T, root string, before map[string]openPathSnapshot) {
	t.Helper()
	after := snapshotOpenPaths(t, root)
	if len(before) != len(after) {
		t.Fatalf("path inventory changed: %d -> %d", len(before), len(after))
	}
	for path, was := range before {
		now, ok := after[path]
		if !ok || !os.SameFile(was.info, now.info) || was.info.Mode() != now.info.Mode() ||
			!was.info.ModTime().Equal(now.info.ModTime()) || was.data != now.data {
			t.Fatalf("path query/refusal changed %s", path)
		}
	}
}

func TestOpenCommandPureValidation(t *testing.T) {
	f := newOpenInjectFixture(t, "feature")
	f.addWorktree(t, "", "one", "actual")
	for _, args := range [][]string{
		{"feature", "one", "--print-path", "--command", "/usr/bin/true"},
		{"feature", "one", "--print-path", "--no-agent"},
		{"feature", "one", "--print-path", "--all"},
		{"feature", "one", "--print-path", "--tmux=false"},
		{"feature", "one", "--print-path", "--no-tmux=false"},
		{"feature", "one", "--print-path", "--", "child"},
		{"feature", "one", "--command", "/usr/bin/true", "--tmux", "--no-tmux"},
		{"feature", "one", "--command", ""},
		{"feature", "one", "--command", "no-such-open-command"},
		{"feature", "one", "--command", "bad\x00command"},
		{"feature", "one", "--command", "/usr/bin/true", "--", "bad\x00argument"},
		{"feature", "one", "--command", "/usr/bin/true", "not-after-separator"},
		{"feature", "--command", "/usr/bin/true"},
		{"--print-path"},
		{"feature", "one", "--feature-dir", "--command", "/usr/bin/true"},
		{"feature", "../unsafe", "--command", "/usr/bin/true"},
		{"", "one", "--print-path"},
	} {
		t.Run(fmt.Sprintf("%q", args), func(t *testing.T) {
			before := snapshotOpenPaths(t, f.root)
			out, err := runOpenModeCommand(t, args...)
			if err == nil || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			assertOpenPathsUnchanged(t, f.root, before)
		})
	}
}

func TestOpenPrintPathExternalSelectedAndFallback(t *testing.T) {
	f := newOpenInjectFixture(t, "feature")
	want := f.addWorktree(t, "", "review/topic", "decoupled")
	secondary := createInjectRepo(t, f.root, "secondary")
	other := f.addWorktree(t, secondary, "docs/api", "docs-branch")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{
		{Name: "review/topic", Branch: "decoupled", Base: "main"},
		{Name: "docs/api", Branch: "docs-branch", Repo: secondary, Base: "main"},
		{Name: "unmaterialized", Branch: "missing", Repo: "/unavailable/unrelated"},
	}}); err != nil {
		t.Fatal(err)
	}
	f.writeInjectFile(t, "context", "must not be injected")
	if err := os.WriteFile(filepath.Join(want, "dirty"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := internal.CreateDirectSession(f.featurePath, internal.DirectSessionRecord{
		Feature: f.feature, Name: "review/topic", Path: want, Stage: internal.DirectStageCommand,
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ name, path string }{{"review/topic", want}, {"docs/api", other}, {"", f.featurePath}} {
		before := snapshotOpenPaths(t, f.root)
		args := []string{f.feature, route.name, "--print-path"}
		if route.name == "" {
			args = []string{f.feature, "--feature-dir", "--print-path"}
		}
		out, err := runOpenModeCommand(t, args...)
		if err != nil || out != route.path+"\n" {
			t.Fatalf("out=%q err=%v want=%q", out, err, route.path)
		}
		assertOpenPathsUnchanged(t, f.root, before)
	}
	injectGit(t, want, "switch", "--detach")
	beforeDetached := snapshotOpenPaths(t, f.root)
	if out, err := runOpenModeCommand(t, f.feature, "review/topic", "--print-path"); err != nil || out != want+"\n" {
		t.Fatalf("external detached observation: out=%q err=%v", out, err)
	}
	assertOpenPathsUnchanged(t, f.root, beforeDetached)
	// An existing selected no-stack feature is authoritative, not fallback.
	selected := filepath.Join(f.root, "selected")
	selectedFeature := filepath.Join(selected, f.feature)
	if err := os.MkdirAll(filepath.Join(selectedFeature, "worktrees"), 0755); err != nil {
		t.Fatal(err)
	}
	selectedPath := filepath.Join(selectedFeature, "worktrees", "review", "topic")
	injectGit(t, f.repo, "worktree", "add", "-b", "selected-actual", selectedPath, "main")
	t.Setenv("TWS_ROOT", selected)
	before := snapshotOpenPaths(t, f.root)
	out, err := runOpenModeCommand(t, f.feature, "review/topic", "--print-path")
	if err != nil || out != selectedPath+"\n" {
		t.Fatalf("selected legacy route: out=%q err=%v", out, err)
	}
	assertOpenPathsUnchanged(t, f.root, before)
}

func TestOpenPrintPathCheckoutObservation(t *testing.T) {
	repo, ws := setupOpenCheckoutCLI(t)
	out, err := runOpenModeCommand(t, "feature", "one", "--print-path")
	if err == nil || out != "" || !strings.Contains(err.Error(), "current attached") {
		t.Fatalf("inactive: out=%q err=%v", out, err)
	}
	gitInDir(t, repo, "switch", "one")
	if err := os.WriteFile(filepath.Join(repo, "dirty"), []byte("ordinary dirt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveCheckoutAgentSession(ws, &internal.CheckoutAgentSession{
		SchemaVersion: 1, WorkspaceID: ws.StableID, Feature: "feature", Name: "one", GitBranch: "one",
		Mode: internal.AgentSessionDirect, PID: os.Getpid(), Stage: internal.DirectStageCommand,
	}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"feature", "one", "--print-path"}, {"feature", "--feature-dir", "--print-path"}} {
		before := snapshotOpenPaths(t, repo)
		out, err := runOpenModeCommand(t, args...)
		want := repo
		if len(args) == 3 && args[1] == "--feature-dir" {
			want = ws.FeaturePath("feature")
		}
		if err != nil || out != want+"\n" {
			t.Fatalf("out=%q err=%v want=%q", out, err, want)
		}
		assertOpenPathsUnchanged(t, repo, before)
	}
}

func TestOpenPrintPathCheckoutLegacyAndRefusals(t *testing.T) {
	repo, ws := setupOpenCheckoutCLI(t)
	fp := ws.FeaturePath("feature")
	legacy := filepath.Join(ws.MetadataRoot, "feature")
	if err := os.Rename(fp, legacy); err != nil {
		t.Fatal(err)
	}
	out, err := runOpenModeCommand(t, "feature", "--feature-dir", "--print-path")
	if err != nil || out != legacy+"\n" {
		t.Fatalf("legacy out=%q err=%v", out, err)
	}
	for _, entry := range []internal.StackEntry{
		{Name: "one", Archived: true},
		{Name: "one", Repo: repo},
		{Name: "one", Branch: "missing"},
	} {
		if err := internal.SaveStack(legacy, internal.Stack{Branches: []internal.StackEntry{entry}}); err != nil {
			t.Fatal(err)
		}
		before := snapshotOpenPaths(t, repo)
		if out, err := runOpenModeCommand(t, "feature", "one", "--print-path"); err == nil || out != "" {
			t.Fatalf("entry=%+v out=%q err=%v", entry, out, err)
		}
		assertOpenPathsUnchanged(t, repo, before)
	}
}

func TestOpenPrintPathRetainedMutationEvidence(t *testing.T) {
	for _, evidence := range []string{"sync", "reparent", "publication", "metadata-move"} {
		t.Run(evidence, func(t *testing.T) {
			repo, ws := setupOpenCheckoutCLI(t)
			path := filepath.Join(ws.CheckoutStateDir(), "other-checkout-sync.yaml")
			switch evidence {
			case "reparent":
				path = filepath.Join(ws.CheckoutStateDir(), "other-reparent.v1.yaml")
			case "publication":
				path = internal.ScopedPushStatePath(internal.ScopedPushLocation{
					Mode: internal.ModeCheckout, Feature: "other", FeaturePath: ws.FeaturePath("other"), MetadataRoot: ws.MetadataRoot,
				})
			case "metadata-move":
				path = filepath.Join(ws.CheckoutStateDir(), ".checkout-metadata-"+strings.Repeat("a", 32))
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("retained evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			before := snapshotOpenPaths(t, repo)
			if out, err := runOpenModeCommand(t, "feature", "--feature-dir", "--print-path"); err == nil || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			assertOpenPathsUnchanged(t, repo, before)
		})
	}
}

func TestOpenPrintPathSelectedPublicationPrecedesFallback(t *testing.T) {
	for _, selectedExists := range []bool{false, true} {
		t.Run(fmt.Sprint(selectedExists), func(t *testing.T) {
			f := newOpenInjectFixture(t, "feature")
			f.addWorktree(t, "", "one", "actual")
			selected := filepath.Join(f.root, "selected")
			path := internal.ScopedPushStatePath(internal.ScopedPushLocation{
				Mode: internal.ModeExternal, Feature: f.feature, FeaturePath: filepath.Join(selected, f.feature), MetadataRoot: selected,
			})
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if selectedExists {
				injectGit(t, f.repo, "worktree", "add", "-b", "selected-one", filepath.Join(selected, f.feature, "worktrees", "one"), "main")
			}
			if err := os.WriteFile(path, []byte("unverifiable publication"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TWS_ROOT", selected)
			before := snapshotOpenPaths(t, f.root)
			for _, mode := range [][]string{{"--print-path"}, {"--command", "/usr/bin/true"}} {
				out, err := runOpenModeCommand(t, append([]string{f.feature, "one"}, mode...)...)
				if err == nil || out != "" || !strings.Contains(err.Error(), "scoped push evidence") {
					t.Fatalf("out=%q err=%v", out, err)
				}
			}
			assertOpenPathsUnchanged(t, f.root, before)
		})
	}
}

func TestOpenPrintPathUnsafeTargets(t *testing.T) {
	for _, kind := range []string{"missing", "archived", "symlink", "backpointer", "control-path"} {
		t.Run(kind, func(t *testing.T) {
			f := newOpenInjectFixture(t, "feature")
			path := f.addWorktree(t, "", "one", "actual")
			switch kind {
			case "missing":
				if err := os.Rename(path, path+"-away"); err != nil {
					t.Fatal(err)
				}
			case "archived":
				if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "one", Archived: true}}}); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+"-away"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-away", path); err != nil {
					t.Fatal(err)
				}
			case "backpointer":
				gitDir := injectGit(t, path, "rev-parse", "--absolute-git-dir")
				if err := os.WriteFile(filepath.Join(gitDir, "gitdir"), []byte(filepath.Join(f.repo, ".git")), 0644); err != nil {
					t.Fatal(err)
				}
			case "control-path":
				f.feature = "bad\x7fname"
				f.featurePath = filepath.Join(f.workspace, f.feature)
				if err := os.MkdirAll(f.featurePath, 0755); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{f.feature, "one", "--print-path"}
			if kind == "control-path" {
				args = []string{f.feature, "--feature-dir", "--print-path"}
			}
			before := snapshotOpenPaths(t, f.root)
			if out, err := runOpenModeCommand(t, args...); err == nil || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			assertOpenPathsUnchanged(t, f.root, before)
		})
	}
}

func TestOpenPrintPathLateGuard(t *testing.T) {
	f := newOpenInjectFixture(t, "feature")
	f.addWorktree(t, "", "one", "actual")
	OpenPrintPathBeforeOutputHook = func() error {
		return os.WriteFile(internal.SyncStatePath(f.featurePath), []byte("late sync"), 0600)
	}
	t.Cleanup(func() { OpenPrintPathBeforeOutputHook = nil })
	if out, err := runOpenModeCommand(t, f.feature, "one", "--print-path"); err == nil || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	assertNotExists(t, internal.DirectSessionsDir(f.featurePath))
	assertNotExists(t, internal.ExternalSessionIntentsDir(f.featurePath))
}

func TestOpenCommandPreservesLegacyLogicalWhitespace(t *testing.T) {
	f := newOpenInjectFixture(t, "feature")
	path := f.addWorktree(t, "", " review", "actual")
	for _, flags := range [][]string{{"--print-path"}, {"--command", "/bin/pwd", "--no-tmux"}} {
		out, err := runOpenModeCommand(t, append([]string{f.feature, " review"}, flags...)...)
		if err != nil || out != path+"\n" {
			t.Fatalf("logical identity changed: out=%q err=%v", out, err)
		}
	}
	assertNotExists(t, internal.ExternalSessionIntentsDir(f.featurePath))
}

func writeOpenProgram(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCommandLiteralArgvAndCallerBinding(t *testing.T) {
	f := newOpenInjectFixture(t, "feature")
	path := f.addWorktree(t, "", "review/one", "actual")
	f.writeInjectFile(t, "context", "context remains configured")
	program := "editor ; 'literal'"
	writeOpenProgram(t, filepath.Join(f.repo, program), `printf '%s\000' "$PWD" "$@" > "$OPEN_CAPTURE"`)
	writeOpenProgram(t, filepath.Join(path, program), `exit 88`)
	capture := filepath.Join(f.root, "argv")
	t.Setenv("OPEN_CAPTURE", capture)
	configPath := filepath.Join(f.home, ".config", "tws", "config.yaml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config = append(config, []byte("agent_command: missing-agent --must-not-run\n")...)
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(f.root, "forbidden-shell")
	writeOpenProgram(t, shell, `touch "$OPEN_CAPTURE.shell"`)
	t.Setenv("SHELL", shell)
	literal := []string{"", "two words", "'quoted'", `"double"`, "$HOME", "*.go", "; touch not-created", "$(exit 17)", "--option", "line\nbreak"}
	args := []string{f.feature, "review/one", "--no-tmux", "--command", "./" + program, "--"}
	out, err := runOpenModeCommand(t, append(args, literal...)...)
	if err != nil {
		t.Fatalf("out=%q err=%v", out, err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(append([]string{path}, literal...), "\x00") + "\x00"
	if string(data) != want {
		t.Fatalf("argv=%q want=%q", data, want)
	}
	assertNotExists(t, capture+".shell")
	assertNotExists(t, internal.DirectSessionsDir(f.featurePath))
	assertNotExists(t, internal.ExternalSessionIntentsDir(f.featurePath))
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(config, after) {
		t.Fatalf("config changed: %v", err)
	}
	if strings.Contains(out, literal[1]) || strings.Contains(out, "Running:") {
		t.Fatalf("raw command was echoed: %q", out)
	}
}

func TestOpenCommandPATHDotRefusesWithoutEffects(t *testing.T) {
	f := newOpenInjectFixture(t, "feature")
	f.addWorktree(t, "", "one", "actual")
	writeOpenProgram(t, filepath.Join(f.repo, "editor"), "exit 0")
	t.Setenv("PATH", ".")
	t.Setenv("GODEBUG", "execerrdot=1")
	before := snapshotOpenPaths(t, f.root)
	if out, err := runOpenModeCommand(t, f.feature, "one", "--command", "editor"); err == nil || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	assertOpenPathsUnchanged(t, f.root, before)
}

func TestOpenCommandFeatureDirBothModes(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		f := newOpenInjectFixture(t, "feature")
		config := filepath.Join(f.home, ".config", "tws", "config.yaml")
		data, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config, append(data, []byte("use_tmux: true\n")...), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := runOpenModeCommand(t, f.feature, "--feature-dir", "--command", "/bin/pwd")
		if err != nil || out != f.featurePath+"\n" {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
	t.Run("checkout-dirty", func(t *testing.T) {
		repo, ws := setupOpenCheckoutCLI(t)
		if err := os.WriteFile(filepath.Join(repo, "dirty"), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		before := gitInDir(t, repo, "symbolic-ref", "HEAD")
		out, err := runOpenModeCommand(t, "feature", "--feature-dir", "--command", "/bin/pwd")
		if err != nil || out != ws.FeaturePath("feature")+"\n" {
			t.Fatalf("out=%q err=%v", out, err)
		}
		if got := gitInDir(t, repo, "symbolic-ref", "HEAD"); got != before {
			t.Fatalf("branch changed: %s", got)
		}
		assertNotExists(t, internal.CheckoutSessionIntentDir(ws))
		beforeFiles := snapshotOpenPaths(t, repo)
		out, err = runOpenModeCommand(t, "feature", "--feature-dir", "--command", "/usr/bin/true", "--tmux")
		if err == nil || out != "" {
			t.Fatalf("tmux feature dir out=%q err=%v", out, err)
		}
		assertOpenPathsUnchanged(t, repo, beforeFiles)
	})
}

func TestOpenCommandCheckoutRestoresAndFailsNormally(t *testing.T) {
	repo, ws := setupOpenCheckoutCLI(t)
	for _, executable := range []string{"/usr/bin/true", "/usr/bin/false"} {
		out, err := runOpenModeCommand(t, "feature", "one", "--command", executable, "--no-tmux")
		if (err != nil) != (executable == "/usr/bin/false") || out != "" {
			t.Fatalf("%s: out=%q err=%v", executable, out, err)
		}
		if got := gitInDir(t, repo, "branch", "--show-current"); got != "main" {
			t.Fatalf("not restored: %q", got)
		}
		if internal.HasCheckoutAgentSession(ws) {
			t.Fatal("record remains")
		}
		assertNotExists(t, internal.CheckoutSessionIntentDir(ws))
	}
}

func TestOpenCommandLateIntentGuards(t *testing.T) {
	for _, mode := range []string{"external", "checkout"} {
		t.Run(mode, func(t *testing.T) {
			var statePath, root string
			if mode == "external" {
				f := newOpenInjectFixture(t, "feature")
				path := f.addWorktree(t, "", "one", "actual")
				root = f.root
				f.writeInjectFile(t, "context", "not injected")
				statePath = internal.SyncStatePath(f.featurePath)
				ExternalSessionLaunchIntentHook = func() error {
					assertNotExists(t, filepath.Join(path, "context"))
					return os.WriteFile(statePath, []byte("late sync"), 0600)
				}
				t.Cleanup(func() { ExternalSessionLaunchIntentHook = nil })
			} else {
				repo, ws := setupOpenCheckoutCLI(t)
				root = repo
				statePath = filepath.Join(ws.CheckoutStateDir(), "other-reparent.v1.yaml")
				internal.CheckoutSessionLaunchIntentHook = func() error {
					return os.WriteFile(statePath, []byte("late reparent"), 0600)
				}
				t.Cleanup(func() { internal.CheckoutSessionLaunchIntentHook = nil })
			}
			program := filepath.Join(t.TempDir(), "must-not-run")
			marker := filepath.Join(root, "ran")
			t.Setenv("OPEN_MARKER", marker)
			writeOpenProgram(t, program, `touch "$OPEN_MARKER"`)
			out, err := runOpenModeCommand(t, "feature", "one", "--command", program, "--no-tmux")
			if err == nil || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			assertNotExists(t, marker)
			if _, err := os.ReadFile(statePath); err != nil {
				t.Fatal("late evidence was removed", err)
			}
		})
	}
}

func TestOpenCommandCompletionAfterSeparator(t *testing.T) {
	f := newOpenInjectFixture(t, "feature")
	f.addWorktree(t, "", "one", "actual")
	cmd := openCmd()
	if err := cmd.ParseFlags([]string{"feature", "--feature-dir", "--command", "/usr/bin/true", "--", ""}); err != nil {
		t.Fatal(err)
	}
	out, directive := cmd.ValidArgsFunction(cmd, []string{"feature"}, "")
	if len(out) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("completion=%v directive=%v", out, directive)
	}
	ordinary := openCmd()
	got, _ := ordinary.ValidArgsFunction(ordinary, []string{"feature"}, "")
	if !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("ordinary completion=%v", got)
	}
	root := &cobra.Command{Use: "tws"}
	root.AddCommand(openCmd())
	stdout, _, err := runManagedCobra(t, root, []string{
		"__complete", "open", "feature", "--feature-dir", "--command", "/usr/bin/true", "--", "",
	}, nil)
	if err != nil || strings.Contains(stdout, "one") || !strings.Contains(stdout, ":4") {
		t.Fatalf("actual command completion=%q err=%v", stdout, err)
	}
}

func TestOpenCommandForeignIntentIsRetained(t *testing.T) {
	f := newOpenInjectFixture(t, "feature")
	f.addWorktree(t, "", "one", "actual")
	var foreignPath string
	ExternalSessionLaunchIntentHook = func() error {
		dir := internal.ExternalSessionIntentsDir(f.featurePath)
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 {
			return errors.New("expected one intent")
		}
		foreignPath = filepath.Join(dir, entries[0].Name())
		return os.WriteFile(foreignPath, []byte(`{"token":"foreign"}`), 0600)
	}
	t.Cleanup(func() { ExternalSessionLaunchIntentHook = nil })
	out, err := runOpenModeCommand(t, "feature", "one", "--command", "/usr/bin/true", "--no-tmux")
	if err == nil || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	data, err := os.ReadFile(foreignPath)
	if err != nil || string(data) != `{"token":"foreign"}` {
		t.Fatalf("foreign intent changed: %q %v", data, err)
	}
}

func TestOpenCommandManagedRendererIsNotTheExecutable(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		f := newOpenInjectFixture(t, "feature")
		t.Setenv("TWS_ROOT", f.workspace)
		path := f.addWorktree(t, "", "one", "actual")
		if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "one", Branch: "actual", Base: "main"}}}); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(f.home, ".config", "tws", "config.yaml")
		config, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		config = append(config, []byte("agent_command: unavailable-agent-wrapper --profile retained\n")...)
		if err := os.WriteFile(configPath, config, 0600); err != nil {
			t.Fatal(err)
		}
		oldProbe := managedTemplateTmuxProbe
		managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
		t.Cleanup(func() { managedTemplateTmuxProbe = oldProbe })
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--scope", "both", "--agent", "copilot"}, nil); err != nil {
			t.Fatal(err)
		}
		ledgerPath := filepath.Join(f.featurePath, ".tws-templates.json")
		ledger, err := os.ReadFile(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		// This forces injection through the reserved (not no-op) managed path,
		// while the launch intent must remain held.
		f.writeInjectFile(t, "legacy-extra", "additional user context")
		for _, args := range [][]string{
			{f.feature, "one", "--command", "/usr/bin/true", "--no-tmux"},
			{f.feature, "--feature-dir", "--command", "/usr/bin/true"},
		} {
			if out, err := runOpenModeCommand(t, args...); err != nil {
				t.Fatalf("out=%q err=%v", out, err)
			}
		}
		if data, err := os.ReadFile(filepath.Join(path, "legacy-extra")); err != nil || string(data) != "additional user context" {
			t.Fatalf("legacy injection failed: %q %v", data, err)
		}
		if after, err := os.ReadFile(ledgerPath); err != nil || !bytes.Equal(after, ledger) {
			t.Fatalf("command changed saved renderer/ledger: %v", err)
		}
		if after, err := os.ReadFile(configPath); err != nil || !bytes.Equal(after, config) {
			t.Fatalf("command changed configured agent: %v", err)
		}
		state, err := internal.LoadManagedTemplateState(f.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(internal.InjectPath(f.featurePath), filepath.FromSlash(state.Roles["worktree"].Files[0].Path))
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		local := append(data, []byte("\nlocal owned edit\n")...)
		if err := os.WriteFile(source, local, 0644); err != nil {
			t.Fatal(err)
		}
		if out, err := runOpenModeCommand(t, f.feature, "one", "--print-path"); err != nil || out != path+"\n" {
			t.Fatalf("query should bypass injection conflict: out=%q err=%v", out, err)
		}
		if out, err := runOpenModeCommand(t, f.feature, "one", "--command", "/usr/bin/true", "--no-tmux"); err == nil {
			t.Fatalf("command ignored owned-source conflict: out=%q", out)
		}
		if after, err := os.ReadFile(source); err != nil || !bytes.Equal(after, local) {
			t.Fatalf("local owned content changed: %v", err)
		}
	})
	t.Run("checkout", func(t *testing.T) {
		f := newManagedCLIFixture(t)
		if err := os.WriteFile(filepath.Join(f.repo, ".git", "info", "exclude"), []byte(".tws/\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{AgentCommand: "missing-agent-wrapper --profile retained"}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, addCmd(), []string{"managed-command", "--agent", "copilot"}, nil); err != nil {
			t.Fatal(err)
		}
		gitInDir(t, f.repo, "branch", "one")
		fp := f.ws.FeaturePath("managed-command")
		if err := internal.SaveStack(fp, internal.Stack{Branches: []internal.StackEntry{{Name: "one", Base: "main"}}}); err != nil {
			t.Fatal(err)
		}
		before := snapshotTree(t, fp)
		if out, err := runOpenModeCommand(t, "managed-command", "one", "--command", "/usr/bin/true", "--no-tmux"); err != nil {
			t.Fatalf("out=%q err=%v", out, err)
		}
		if after := snapshotTree(t, fp); before != after {
			t.Fatal("checkout command changed managed sources/renderer")
		}
	})
}

func TestOpenCommandChangedIntentIdentityRetained(t *testing.T) {
	if field := os.Getenv("TWS_TEST_MUTATE_OPEN_INTENT"); field != "" {
		dir := os.Getenv("TWS_TEST_OPEN_INTENT_DIR")
		files, err := os.ReadDir(dir)
		if err != nil || len(files) != 1 {
			t.Fatalf("intent inventory: %v %v", files, err)
		}
		path := filepath.Join(dir, files[0].Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var intent internal.ExternalSessionIntent
		if err := json.Unmarshal(data, &intent); err != nil {
			t.Fatal(err)
		}
		switch field {
		case "owner":
			intent.OwnerPID = 1
		case "scope":
			if intent.Scope == internal.ExternalSessionIntentFeature {
				intent.Scope, intent.Names = internal.ExternalSessionIntentBranches, []string{"foreign"}
			} else {
				intent.Scope, intent.Names = internal.ExternalSessionIntentFeature, nil
			}
		default:
			t.Fatalf("unexpected test field %q", field)
		}
		data, err = json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("TWS_TEST_OPEN_INTENT_RECEIPT"), data, 0600); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, featureDir := range []bool{false, true} {
		for _, field := range []string{"owner", "scope"} {
			t.Run(fmt.Sprintf("feature-dir=%t/%s", featureDir, field), func(t *testing.T) {
				f := newOpenInjectFixture(t, "feature")
				f.addWorktree(t, "", "one", "actual")
				dir := internal.ExternalSessionIntentsDir(f.featurePath)
				receipt := filepath.Join(f.root, "foreign-intent.json")
				t.Setenv("TWS_TEST_MUTATE_OPEN_INTENT", field)
				t.Setenv("TWS_TEST_OPEN_INTENT_DIR", dir)
				t.Setenv("TWS_TEST_OPEN_INTENT_RECEIPT", receipt)
				selectors := []string{f.feature, "one"}
				if featureDir {
					selectors = []string{f.feature, "--feature-dir"}
				}
				args := append(selectors, "--command", executable, "--no-tmux", "--",
					"-test.run=^TestOpenCommandChangedIntentIdentityRetained$", "-test.paniconexit0=false")
				out, err := runOpenModeCommand(t, args...)
				if err == nil || !strings.Contains(err.Error(), "ownership changed") || out != "" {
					t.Fatalf("out=%q err=%v", out, err)
				}
				files, err := os.ReadDir(dir)
				if err != nil || len(files) != 1 {
					t.Fatalf("changed-identity intent was not retained: %v %v", files, err)
				}
				data, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
				if err != nil {
					t.Fatal(err)
				}
				want, err := os.ReadFile(receipt)
				if err != nil || !bytes.Equal(data, want) {
					t.Fatalf("foreign intent bytes changed: %v", err)
				}
				var intent internal.ExternalSessionIntent
				if err := json.Unmarshal(data, &intent); err != nil ||
					intent.Token != strings.TrimSuffix(files[0].Name(), ".json") {
					t.Fatalf("test must retain the original token: %+v %v", intent, err)
				}
			})
		}
	}
}
