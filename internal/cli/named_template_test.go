package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

type managedTemplateTestTmuxProbe struct{}

func (managedTemplateTestTmuxProbe) Snapshot() internal.TmuxSnapshot {
	return internal.TmuxSnapshot{Available: true, Sessions: map[string]bool{}}
}

type managedTemplateReadError struct{}

func (managedTemplateReadError) Read([]byte) (int, error) {
	return 0, errors.New("confirmation read failed")
}

type managedCLIFixture struct {
	root string
	home string
	repo string
	ws   internal.Workspace
}

func newManagedCLIFixture(t *testing.T) *managedCLIFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[user]\n\tname = Managed CLI Test\n\temail = managed-cli@example.test\n\tuseConfigOnly = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")

	repo := filepath.Join(root, "repo")
	gitInDirCreate(t, repo)
	if err := os.MkdirAll(filepath.Join(repo, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".tws", "config.yaml"), []byte("workspace_mode: checkout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}

	oldTTY := managedTemplateInputIsTTY
	oldHook := managedTemplatePostConsentHook
	oldTmux := managedTemplateTmuxProbe
	managedTemplatePostConsentHook = nil
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() {
		managedTemplateInputIsTTY = oldTTY
		managedTemplatePostConsentHook = oldHook
		managedTemplateTmuxProbe = oldTmux
	})
	return &managedCLIFixture{root: root, home: home, repo: repo, ws: ws}
}

func gitInDirCreate(t *testing.T, repo string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, repo, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, repo, "add", "README.md")
	gitInDir(t, repo, "commit", "-m", "initial")
}

func managedCLIPackage(t *testing.T, root string, files map[string]string, manifest string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(root, "template.yaml"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func (f *managedCLIFixture) replaceGlobalTemplate(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	if _, err := internal.ResolveTemplateReference(f.repo, "global:"+name); err == nil {
		if err := internal.RemoveNamedTemplate("", internal.TemplateScopeGlobal, name); err != nil {
			t.Fatal(err)
		}
	}
	source := managedCLIPackage(t, filepath.Join(f.root, "source-"+name+"-"+strings.ReplaceAll(files["inject/shared.md"], "\n", "_")), files, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, name, source); err != nil {
		t.Fatal(err)
	}
	resolved, err := internal.ResolveTemplateReference(f.repo, "global:"+name)
	if err != nil {
		t.Fatal(err)
	}
	return resolved.Path
}

func runManagedCobra(t *testing.T, cmd *cobra.Command, args []string, input io.Reader) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if input != nil {
		cmd.SetIn(input)
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func fakeManagedTmuxOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "tmux.log")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + log + "\n" +
		"if [ \"$1\" = \"has-session\" ]; then exit 1; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestNamedTemplateCLIRegistryCommands(t *testing.T) {
	f := newManagedCLIFixture(t)
	source := managedCLIPackage(t, filepath.Join(f.root, "registry-source"), map[string]string{
		"inject/context.md": "context\n",
	}, "")

	stdout, _, err := runManagedCobra(t, templateCmd(), []string{"add", "review", source}, nil)
	if err != nil || !strings.Contains(stdout, "Added global template review") {
		t.Fatalf("template add: out=%q err=%v", stdout, err)
	}
	stdout, _, err = runManagedCobra(t, templateCmd(), []string{"list"}, nil)
	if err != nil || !strings.Contains(stdout, "review\tglobal effective") {
		t.Fatalf("template list: out=%q err=%v", stdout, err)
	}
	stdout, _, err = runManagedCobra(t, templateCmd(), []string{"show", "review"}, nil)
	if err != nil || !strings.Contains(stdout, "scope: global") || !strings.Contains(stdout, "context.md") {
		t.Fatalf("template show: out=%q err=%v", stdout, err)
	}
	if err := os.MkdirAll(filepath.Join(f.repo, "global:review"), 0o755); err != nil {
		t.Fatal(err)
	}
	classified, err := classifyTemplateArgs(f.repo, []string{"global:review"})
	if err != nil || !classified.namedSet || len(classified.legacyDirs) != 0 {
		t.Fatalf("scope-qualified reference did not disambiguate an existing directory: %+v err=%v", classified, err)
	}
	stdout, _, err = runManagedCobra(t, templateCmd(), []string{"remove", "review"}, nil)
	if err != nil || !strings.Contains(stdout, "Removed global template review") {
		t.Fatalf("template remove: out=%q err=%v", stdout, err)
	}
	if _, err := internal.ResolveTemplateReference(f.repo, "global:review"); err == nil {
		t.Fatal("template remove left the registry entry")
	}
}

func TestNamedTemplateRemoveCompletionMatchesModifiedScope(t *testing.T) {
	f := newManagedCLIFixture(t)
	for _, scope := range []string{internal.TemplateScopeGlobal, internal.TemplateScopeRepo} {
		repoRoot := ""
		if scope == internal.TemplateScopeRepo {
			repoRoot = f.repo
		}
		for _, name := range []string{"shared", scope + "-only"} {
			source := managedCLIPackage(t, filepath.Join(f.root, scope+"-"+name), map[string]string{
				"inject/context.md": scope + "\n",
			}, "")
			if err := internal.AddNamedTemplate(repoRoot, scope, name, source); err != nil {
				t.Fatal(err)
			}
		}
	}
	globalCmd := templateRegistryRemoveCmd()
	globalNames, directive := globalCmd.ValidArgsFunction(globalCmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp ||
		!managedTestContains(globalNames, "shared") ||
		!managedTestContains(globalNames, "global-only") ||
		managedTestContains(globalNames, "repo-only") ||
		managedTestContains(globalNames, "global:shared") {
		t.Fatalf("global remove completion mismatched target registry: %v directive=%v", globalNames, directive)
	}
	repoCmd := templateRegistryRemoveCmd()
	if err := repoCmd.Flags().Set("repo", "true"); err != nil {
		t.Fatal(err)
	}
	repoNames, directive := repoCmd.ValidArgsFunction(repoCmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp ||
		!managedTestContains(repoNames, "shared") ||
		!managedTestContains(repoNames, "repo-only") ||
		managedTestContains(repoNames, "global-only") ||
		managedTestContains(repoNames, "repo:shared") {
		t.Fatalf("repo remove completion mismatched target registry: %v directive=%v", repoNames, directive)
	}
}

func TestManagedTemplateConsentMatrixAndPostConsentDrift(t *testing.T) {
	f := newManagedCLIFixture(t)
	f.replaceGlobalTemplate(t, "managed", map[string]string{
		"inject/shared.md":   "v1\n",
		"inject/obsolete.md": "remove me\n",
		"feature/notes.md":   "notes\n",
	})
	if _, _, err := runManagedCobra(t, addCmd(), []string{"feature", "--template", "global:managed", "--agent", "claude"}, nil); err != nil {
		t.Fatal(err)
	}
	featurePath := f.ws.FeaturePath("feature")
	f.replaceGlobalTemplate(t, "managed", map[string]string{
		"inject/shared.md": "v2\n",
		"feature/notes.md": "notes\n",
	})
	before := snapshotTree(t, featurePath)

	tests := []struct {
		name  string
		tty   bool
		input io.Reader
		want  string
	}{
		{name: "non-tty", input: strings.NewReader("yes\n"), want: "requires an interactive"},
		{name: "default-empty", tty: true, input: strings.NewReader("\n"), want: "cancelled"},
		{name: "explicit-no", tty: true, input: strings.NewReader("no\n"), want: "cancelled"},
		{name: "eof", tty: true, input: strings.NewReader("yes"), want: "no affirmative response"},
		{name: "read-error", tty: true, input: managedTemplateReadError{}, want: "confirmation read failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			managedTemplateInputIsTTY = func(io.Reader) bool { return tc.tty }
			_, stderr, err := runManagedCobra(t, templateSyncCmd(), []string{"feature"}, tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("consent result: err=%v stderr=%q", err, stderr)
			}
			for _, want := range []string{
				"feature=feature role=worktree kind=file",
				filepath.Join(featurePath, "inject", "obsolete.md"),
				"Remove these unchanged TWS-managed files and apply the template update? [y/N] ",
			} {
				if !strings.Contains(stderr, want) {
					t.Fatalf("warning missing %q:\n%s", want, stderr)
				}
			}
			if after := snapshotTree(t, featurePath); after != before {
				t.Fatalf("declined update mutated feature:\nBEFORE\n%s\nAFTER\n%s", before, after)
			}
		})
	}

	managedTemplateInputIsTTY = func(io.Reader) bool { return true }
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature"}, strings.NewReader("yes\n")); err != nil {
		t.Fatalf("affirmative consent failed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(featurePath, "inject", "obsolete.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete managed file remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(featurePath, "inject", "shared.md")); err != nil || string(data) != "v2\n" {
		t.Fatalf("managed update not applied: %q %v", data, err)
	}

	f.replaceGlobalTemplate(t, "managed", map[string]string{
		"inject/shared.md":     "v3\n",
		"inject/automation.md": "temporary\n",
		"feature/notes.md":     "notes\n",
	})
	managedTemplateInputIsTTY = func(io.Reader) bool { return false }
	if _, stderr, err := runManagedCobra(t, templateSyncCmd(), []string{"feature"}, nil); err != nil || strings.Contains(stderr, "[y/N]") {
		t.Fatalf("no-removal update prompted or failed: err=%v stderr=%q", err, stderr)
	}
	f.replaceGlobalTemplate(t, "managed", map[string]string{
		"inject/shared.md": "v4\n",
		"feature/notes.md": "notes\n",
	})
	_, stderr, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--allow-managed-removal"}, nil)
	if err != nil {
		t.Fatalf("automation consent failed: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "automation.md") || !strings.Contains(stderr, "[y/N]") {
		t.Fatalf("automation consent omitted exact warning: %q", stderr)
	}

	f.replaceGlobalTemplate(t, "managed", map[string]string{
		"inject/shared.md": "v5\n",
		"inject/drift.md":  "temporary\n",
		"feature/notes.md": "notes\n",
	})
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature"}, nil); err != nil {
		t.Fatal(err)
	}
	registryPath := f.replaceGlobalTemplate(t, "managed", map[string]string{
		"inject/shared.md": "v6\n",
		"feature/notes.md": "notes\n",
	})
	before = snapshotTree(t, featurePath)
	managedTemplateInputIsTTY = func(io.Reader) bool { return true }
	managedTemplatePostConsentHook = func() error {
		return os.WriteFile(filepath.Join(registryPath, "inject", "shared.md"), []byte("source drift\n"), 0o644)
	}
	_, _, err = runManagedCobra(t, templateSyncCmd(), []string{"feature"}, strings.NewReader("yes\n"))
	managedTemplatePostConsentHook = nil
	if err == nil || !strings.Contains(err.Error(), "drifted") {
		t.Fatalf("post-consent source drift was accepted: %v", err)
	}
	if after := snapshotTree(t, featurePath); after != before {
		t.Fatal("post-consent drift refusal mutated feature assets or ownership")
	}
}

func TestManagedTemplateBatchPreflightBeforeLegacyEffects(t *testing.T) {
	f := newManagedCLIFixture(t)
	f.replaceGlobalTemplate(t, "batch", map[string]string{
		"inject/shared.md": "v1\n",
	})
	for _, feature := range []string{"alpha", "beta"} {
		if _, _, err := runManagedCobra(t, addCmd(), []string{feature, "--template", "global:batch", "--agent", "claude"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := addCheckout(f.ws, "legacy", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	legacySentinel := filepath.Join(f.ws.FeaturePath("legacy"), "inject", "user.md")
	if err := os.WriteFile(legacySentinel, []byte("user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	alphaShared := filepath.Join(f.ws.FeaturePath("alpha"), "inject", "shared.md")
	betaShared := filepath.Join(f.ws.FeaturePath("beta"), "inject", "shared.md")
	if err := os.WriteFile(betaShared, []byte("operator edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.replaceGlobalTemplate(t, "batch", map[string]string{
		"inject/shared.md": "v2\n",
	})
	_, _, err := runManagedCobra(t, templateSyncCmd(), []string{"--all"}, nil)
	if err == nil || !strings.Contains(err.Error(), "content digest changed") {
		t.Fatalf("batch preflight accepted modified owned file: %v", err)
	}
	if data, _ := os.ReadFile(alphaShared); string(data) != "v1\n" {
		t.Fatalf("earlier managed feature changed before full preflight: %q", data)
	}
	if data, _ := os.ReadFile(legacySentinel); string(data) != "user\n" {
		t.Fatalf("legacy feature changed before managed preflight completed: %q", data)
	}
}

func TestManagedTemplateSyncAllRejectsMixedDirectoryAndManagedSelectors(t *testing.T) {
	f := newManagedCLIFixture(t)
	for _, feature := range []string{"alpha", "beta"} {
		if err := addCheckout(f.ws, feature, nil, "", "", false, false, false); err != nil {
			t.Fatal(err)
		}
	}
	legacy := managedCLIPackage(t, filepath.Join(f.root, "legacy-all"), map[string]string{
		"new.md": "must not be copied\n",
	}, "")
	before := map[string]string{}
	for _, feature := range []string{"alpha", "beta"} {
		before[feature] = snapshotTree(t, f.ws.FeaturePath(feature))
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		"--all", "--template", legacy, "--agent", "copilot",
	}, nil); err == nil {
		t.Fatal("sync --all accepted a legacy directory with a managed selector")
	}
	for _, feature := range []string{"alpha", "beta"} {
		if after := snapshotTree(t, f.ws.FeaturePath(feature)); after != before[feature] {
			t.Fatalf("invalid mixed --all selectors changed feature %s", feature)
		}
	}
}

func TestManagedTemplateStateSurvivesFeatureRename(t *testing.T) {
	f := newManagedCLIFixture(t)
	f.replaceGlobalTemplate(t, "rename", map[string]string{"inject/shared.md": "shared\n"})
	if _, _, err := runManagedCobra(t, addCmd(), []string{"before", "--template", "global:rename"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "before", "after"}, nil); err != nil {
		t.Fatal(err)
	}
	state, err := internal.LoadManagedTemplateState(f.ws.FeaturePath("after"))
	if err != nil {
		t.Fatal(err)
	}
	role := state.Roles["worktree"]
	if role.Source.Scope != internal.TemplateScopeGlobal || len(role.Files) == 0 {
		t.Fatalf("renamed feature lost relative managed state: %+v", state)
	}
	data, err := os.ReadFile(internal.ManagedTemplateStatePath(f.ws.FeaturePath("after")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), f.ws.FeaturePath("before")) {
		t.Fatal("managed state persisted an absolute pre-rename feature path")
	}
}

func TestManagedTemplateFeatureRenameRefusesActiveWriterAndCheckoutSession(t *testing.T) {
	t.Run("active managed writer", func(t *testing.T) {
		f := newManagedCLIFixture(t)
		if _, _, err := runManagedCobra(t, addCmd(), []string{"busy-writer", "--agent", "claude"}, nil); err != nil {
			t.Fatal(err)
		}
		featurePath := f.ws.FeaturePath("busy-writer")
		lock, err := internal.AcquireManagedTemplateLock(featurePath)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release() //nolint:errcheck
		if _, _, err := runManagedCobra(t, renameCmd(), []string{
			"feature", "busy-writer", "moved",
		}, nil); err == nil || !strings.Contains(err.Error(), "already active") {
			t.Fatalf("feature rename accepted an active managed writer: %v", err)
		}
		if _, err := os.Stat(featurePath); err != nil {
			t.Fatalf("active-writer refusal moved original feature: %v", err)
		}
		if _, err := os.Lstat(f.ws.FeaturePath("moved")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("active-writer refusal created destination feature: %v", err)
		}
	})

	t.Run("active checkout session", func(t *testing.T) {
		f := newManagedCLIFixture(t)
		if _, _, err := runManagedCobra(t, addCmd(), []string{"busy-session", "--agent", "claude"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := internal.SaveCheckoutAgentSession(f.ws, &internal.CheckoutAgentSession{
			SchemaVersion: 1, WorkspaceID: f.ws.StableID,
			Feature: "busy-session", Name: "entry", GitBranch: "entry",
			PID: os.Getpid(),
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.Remove(filepath.Join(f.ws.MetadataRoot, "state", "sessions", "active.json"))
		})
		if _, _, err := runManagedCobra(t, renameCmd(), []string{
			"feature", "busy-session", "moved",
		}, nil); err == nil || !strings.Contains(err.Error(), "checkout agent session is active") {
			t.Fatalf("feature rename accepted an active checkout session: %v", err)
		}
		if _, err := os.Stat(f.ws.FeaturePath("busy-session")); err != nil {
			t.Fatalf("active-session refusal moved original feature: %v", err)
		}
	})
}

func TestManagedTemplateInjectOwnsLaterWorktreeLink(t *testing.T) {
	f := newInjectFixture(t, "managed-links")
	oldTmux := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })

	source := managedCLIPackage(t, filepath.Join(f.root, "managed-source"), map[string]string{
		"inject/context.md": "context\n",
	}, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "managed-links", source); err != nil {
		t.Fatal(err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := internal.PlanManagedTemplate(internal.ManagedTemplatePlanRequest{
		Workspace: ws, Feature: f.feature, FeaturePath: f.featurePath,
		Roles:    []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
		Worktree: internal.ManagedTemplateRoleRequest{Reference: "global:managed-links", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := internal.ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	worktree := f.addWorktree(t, f.repo, "nested/api", "managed-api")
	stack := internal.Stack{Branches: []internal.StackEntry{{Name: "nested/api", Branch: "managed-api"}}}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}
	if _, err := runInjectCommand(t, f.feature, "nested/api"); err != nil {
		t.Fatalf("managed inject command failed: %v", err)
	}
	link := filepath.Join(worktree, "context.md")
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("later worktree link not materialized: info=%v err=%v", info, err)
	}
	state, err := internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, owned := range state.Roles["worktree"].Links {
		if owned.Worktree == "nested/api" && owned.Path == "context.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("later worktree link was not recorded: %+v", state.Roles["worktree"].Links)
	}
}

func TestManagedTemplateExplicitRootInjectOverrideWinsNonRootConfig(t *testing.T) {
	for _, branchSpecific := range []bool{true, false} {
		name := "all"
		if branchSpecific {
			name = "branch"
		}
		t.Run(name, func(t *testing.T) {
			f := newInjectFixture(t, "explicit-root-"+name)
			t.Setenv("TWS_ROOT", f.workspace)
			writeInjectTWSConfig(t, f.home, f.repo, f.workspace, ".context")
			if err := os.MkdirAll(filepath.Join(f.repo, ".tws"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.repo, ".tws", "config.yaml"), []byte("inject_into: .context\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			worktree := f.addWorktree(t, f.repo, "entry", "explicit-root-"+name+"-entry")
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: "entry", Branch: "explicit-root-" + name + "-entry",
			}}}); err != nil {
				t.Fatal(err)
			}
			ws, err := internal.RequireWorkspace()
			if err != nil {
				t.Fatal(err)
			}
			plan, err := internal.PlanManagedTemplate(internal.ManagedTemplatePlanRequest{
				Workspace: ws, Feature: f.feature, FeaturePath: f.featurePath,
				Roles: []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
				Agent: "claude", AgentSet: true,
				InjectInto: ".", InjectIntoSet: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := internal.ApplyManagedTemplatePlan(plan); err != nil {
				t.Fatal(err)
			}
			args := []string{f.feature}
			if branchSpecific {
				args = append(args, "entry")
			}
			if _, err := runInjectCommand(t, args...); err == nil ||
				!strings.Contains(err.Error(), "repository-root injection") {
				t.Fatalf("configured non-root injection was accepted without override: %v", err)
			}
			cmd := injectCmd()
			args = append(args, "--into", ".")
			if _, _, err := runManagedCobra(t, cmd, args, nil); err != nil {
				t.Fatalf("explicit --into . did not override configured non-root target: %v", err)
			}
			if _, err := os.Readlink(filepath.Join(worktree, "CLAUDE.local.md")); err != nil {
				t.Fatalf("explicit root injection did not materialize native link: %v", err)
			}
		})
	}
}

func TestManagedTemplatePrimaryRepoContextFromSecondaryWorktree(t *testing.T) {
	f := newInjectFixture(t, "primary-context")
	t.Setenv("TWS_ROOT", f.workspace)
	secondary := createInjectRepo(t, f.root, "secondary")
	secondaryWorktree := f.addWorktree(t, secondary, "docs", "primary-context-docs")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "docs", Branch: "primary-context-docs", Repo: secondary,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.repo, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(secondary, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	primarySource := managedCLIPackage(t, filepath.Join(f.root, "primary-template"), map[string]string{
		"inject/context.md": "primary\n",
	}, "")
	secondarySource := managedCLIPackage(t, filepath.Join(f.root, "secondary-template"), map[string]string{
		"inject/context.md": "secondary\n",
	}, "")
	if err := internal.AddNamedTemplate(f.repo, internal.TemplateScopeRepo, "roles", primarySource); err != nil {
		t.Fatal(err)
	}
	if err := internal.AddNamedTemplate(secondary, internal.TemplateScopeRepo, "roles", secondarySource); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, ".tws", "config.yaml"), []byte(
		"worktree_template: repo:roles\nagent_command: claude\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondary, ".tws", "config.yaml"), []byte(
		"worktree_template: repo:roles\nagent_command: copilot\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(secondaryWorktree); err != nil {
		t.Fatal(err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	context, err := resolveFeatureContext(ws, f.feature)
	if err != nil {
		t.Fatal(err)
	}
	primaryCanonical, err := filepath.EvalSymlinks(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	workspaceCanonical, err := filepath.EvalSymlinks(f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(context.Workspace.RepoRoot) != filepath.Clean(primaryCanonical) ||
		filepath.Clean(context.Workspace.MetadataRoot) != filepath.Clean(workspaceCanonical) {
		t.Fatalf("secondary worktree selected incoherent primary context: %+v", context.Workspace)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "worktree",
	}, nil); err != nil {
		t.Fatal(err)
	}
	state, err := internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	role := state.Roles[string(internal.ManagedTemplateRoleWorktree)]
	if role.Agent != "claude" || role.Source.Scope != internal.TemplateScopeRepo ||
		role.Source.Name != "roles" {
		t.Fatalf("managed role captured secondary defaults or registry: %+v", role)
	}
	data, err := os.ReadFile(filepath.Join(f.featurePath, "inject", "context.md"))
	if err != nil || string(data) != "primary\n" {
		t.Fatalf("primary repo template was not selected: %q %v", data, err)
	}
	showCmd := templateRegistryShowCmd()
	names, directive := showCmd.ValidArgsFunction(showCmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp || !managedTestContains(names, "repo:roles") {
		t.Fatalf("role completion did not use primary repo registry: %v directive=%v", names, directive)
	}
}

func TestManagedTemplatePrimaryRepoContextRefusesMissingOrConflictingOwner(t *testing.T) {
	t.Run("missing established owner", func(t *testing.T) {
		f := newInjectFixture(t, "owner-missing")
		secondary := createInjectRepo(t, f.root, "secondary")
		t.Setenv("TWS_ROOT", f.workspace)
		if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
			Name: "secondary", Branch: "secondary", Repo: secondary,
		}}}); err != nil {
			t.Fatal(err)
		}
		if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(secondary); err != nil {
			t.Fatal(err)
		}
		ws, err := internal.RequireWorkspace()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := managedTemplateExecutionWorkspace(ws); err == nil ||
			!strings.Contains(err.Error(), "cannot determine the primary repository") {
			t.Fatalf("missing primary owner silently adopted secondary context: %v", err)
		}
	})

	t.Run("conflicting established owners", func(t *testing.T) {
		f := newInjectFixture(t, "owner-conflict")
		secondary := createInjectRepo(t, f.root, "secondary")
		t.Setenv("TWS_ROOT", f.workspace)
		if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{
			Workspaces: map[string]string{
				f.repo:    f.workspace,
				secondary: f.workspace,
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(secondary); err != nil {
			t.Fatal(err)
		}
		ws, err := internal.RequireWorkspace()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := managedTemplateExecutionWorkspace(ws); err == nil ||
			!strings.Contains(err.Error(), "multiple default repositories") {
			t.Fatalf("conflicting primary owners silently adopted secondary context: %v", err)
		}
	})

	t.Run("fresh root keeps known source repository", func(t *testing.T) {
		f := newInjectFixture(t, "fresh-owner")
		freshRoot := filepath.Join(f.root, "fresh-workspace")
		t.Setenv("TWS_ROOT", freshRoot)
		if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(f.repo); err != nil {
			t.Fatal(err)
		}
		ws, err := internal.RequireWorkspace()
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := managedTemplateExecutionWorkspace(ws)
		if err != nil {
			t.Fatalf("fresh root did not retain known source repository: %v", err)
		}
		primary, err := filepath.EvalSymlinks(f.repo)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Clean(resolved.RepoRoot) != filepath.Clean(primary) ||
			filepath.Clean(resolved.MetadataRoot) != filepath.Clean(freshRoot) {
			t.Fatalf("fresh root resolved incoherently: %+v", resolved)
		}
	})
}

func TestManagedTemplateConflictingRootsKeepResolvedContext(t *testing.T) {
	f := newInjectFixture(t, "same")
	rootB := os.Getenv("TWS_ROOT")
	if err := internal.EnsureExternalWorkspaceMarker(rootB); err != nil {
		t.Fatal(err)
	}
	managedSource := managedCLIPackage(t, filepath.Join(f.root, "root-a-template"), map[string]string{
		"inject/managed.md": "root-a\n",
	}, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "root-a", managedSource); err != nil {
		t.Fatal(err)
	}
	wsA := internal.Workspace{RepoRoot: f.repo, Mode: internal.ModeExternal, MetadataRoot: f.workspace}
	plan, err := internal.PlanManagedTemplate(internal.ManagedTemplatePlanRequest{
		Workspace: wsA, Feature: f.feature, FeaturePath: f.featurePath,
		Roles:    []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
		Worktree: internal.ManagedTemplateRoleRequest{Reference: "global:root-a", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := internal.ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	featureB := filepath.Join(rootB, f.feature)
	if err := os.MkdirAll(filepath.Join(featureB, "inject"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featureB, "inject", "legacy.md"), []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featureB, internal.Stack{Branches: []internal.StackEntry{{
		Name: "root-b-entry", Branch: "root-b-entry",
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "root-a-entry", Branch: "root-a-entry",
	}}}); err != nil {
		t.Fatal(err)
	}

	legacy := managedCLIPackage(t, filepath.Join(f.root, "legacy-explicit"), map[string]string{
		"selected.md": "root-b\n",
	}, "")
	beforeA := snapshotTree(t, f.featurePath)
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--template", legacy,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if afterA := snapshotTree(t, f.featurePath); afterA != beforeA {
		t.Fatal("legacy sync preflighted TWS_ROOT B but mutated managed root A")
	}
	if data, err := os.ReadFile(filepath.Join(featureB, "inject", "selected.md")); err != nil || string(data) != "root-b\n" {
		t.Fatalf("legacy sync did not mutate selected root B: %q %v", data, err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	context, branch, err := resolveOpenArgs(ws, []string{f.feature})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(context.FeaturePath) != filepath.Clean(featureB) || branch != "root-b-entry" {
		t.Fatalf("one-argument open resolved branches from another root: context=%+v branch=%q", context, branch)
	}

	output := filepath.Join(f.root, "same.yaml")
	if _, _, err := runManagedCobra(t, exportCmd(), []string{f.feature, "-o", output}, nil); err != nil {
		t.Fatal(err)
	}
	exported, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(exported), "root-b-entry") ||
		strings.Contains(string(exported), "root-a-entry") {
		t.Fatalf("export reacquired another root: %q %v", exported, err)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{
		"feature", f.feature, "renamed",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rootB, "renamed")); err != nil {
		t.Fatalf("feature rename did not use resolved root B: %v", err)
	}
	if _, err := os.Stat(f.featurePath); err != nil {
		t.Fatalf("feature rename incorrectly moved managed root A: %v", err)
	}
}

func TestManagedTemplateLegacyOpenUsesResolvedExternalRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		all  bool
	}{
		{name: "feature-dir", args: []string{"legacy-open", "--feature-dir", "--no-agent"}},
		{name: "all", args: []string{"legacy-open", "--all"}, all: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = newInjectFixture(t, "configured-only")
			selectedRoot := os.Getenv("TWS_ROOT")
			if err := internal.EnsureExternalWorkspaceMarker(selectedRoot); err != nil {
				t.Fatal(err)
			}
			featurePath := filepath.Join(selectedRoot, "legacy-open")
			if err := os.MkdirAll(filepath.Join(featurePath, "worktrees", "entry"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: "entry", Branch: "entry",
			}}}); err != nil {
				t.Fatal(err)
			}
			if tc.all {
				log := fakeManagedTmuxOnPath(t)
				if _, _, err := runManagedCobra(t, openCmd(), tc.args, nil); err != nil {
					t.Fatalf("legacy --all open failed: %v", err)
				}
				data, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), "new-session -d -s legacy-open -c "+featurePath+" -n orchestrator") {
					t.Fatalf("legacy --all used wrong tmux feature directory:\n%s", data)
				}
				return
			}
			var commandErr error
			stdout := captureStdout(t, func() {
				_, _, commandErr = runManagedCobra(t, openCmd(), tc.args, nil)
			})
			if commandErr != nil {
				t.Fatalf("legacy feature-dir open failed: %v", commandErr)
			}
			if !strings.Contains(stdout, "cd "+featurePath) {
				t.Fatalf("legacy feature-dir open omitted resolved path: %q", stdout)
			}
		})
	}
}

func TestManagedTemplateBranchRenameUsesResolvedExternalRoot(t *testing.T) {
	f := newInjectFixture(t, "branch-root")
	rootB := os.Getenv("TWS_ROOT")
	if err := internal.EnsureExternalWorkspaceMarker(rootB); err != nil {
		t.Fatal(err)
	}
	featureB := filepath.Join(rootB, f.feature)
	worktreeB := filepath.Join(featureB, "worktrees", "entry")
	if err := os.MkdirAll(filepath.Dir(worktreeB), 0o755); err != nil {
		t.Fatal(err)
	}
	injectGit(t, f.repo, "branch", "root-b-branch", "main")
	injectGit(t, f.repo, "worktree", "add", worktreeB, "root-b-branch")
	if err := internal.SaveStack(featureB, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "root-b-branch",
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "root-a-branch",
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{
		"branch", f.feature, "entry", "renamed",
	}, nil); err != nil {
		t.Fatal(err)
	}
	stackB, err := internal.LoadStack(featureB)
	if err != nil || internal.GetBranch(stackB, "renamed").Name == "" {
		t.Fatalf("branch rename did not update resolved root B: %+v %v", stackB, err)
	}
	stackA, err := internal.LoadStack(f.featurePath)
	if err != nil || internal.GetBranch(stackA, "entry").Name == "" {
		t.Fatalf("branch rename mutated root A metadata: %+v %v", stackA, err)
	}
}

func TestManagedTemplateBranchRenameSecondaryRepoMaterialized(t *testing.T) {
	f := newInjectFixture(t, "secondary-materialized")
	t.Setenv("TWS_ROOT", f.workspace)
	secondary := createInjectRepo(t, f.root, "secondary")
	if err := os.MkdirAll(filepath.Join(f.repo, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(secondary, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, ".tws", "config.yaml"), []byte(
		"branch_prefix: primary-\nagent_command: claude\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondary, ".tws", "config.yaml"), []byte(
		"branch_prefix: secondary-\ninject_into: .context\nagent_command: copilot\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	const oldBranch = "same-name"
	primaryBefore := injectGit(t, f.repo, "rev-parse", "main")
	secondaryBefore := injectGit(t, secondary, "rev-parse", "main")
	injectGit(t, f.repo, "branch", oldBranch, "main")
	injectGit(t, secondary, "branch", oldBranch, "main")

	oldPath := filepath.Join(f.featurePath, "worktrees", "entry")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	injectGit(t, secondary, "worktree", "add", oldPath, oldBranch)
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: oldBranch, Repo: secondary,
	}}}); err != nil {
		t.Fatal(err)
	}
	oldTmux := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "worktree", "--agent", "claude",
	}, nil); err != nil {
		t.Fatal(err)
	}

	observer := filepath.Join(f.root, "secondary-observer")
	injectGit(t, secondary, "worktree", "add", "-b", "observer", observer, "main")
	if err := os.Chdir(observer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{
		"branch", f.feature, "entry", "renamed",
	}, nil); err != nil {
		t.Fatal(err)
	}

	if got := injectGit(t, f.repo, "rev-parse", oldBranch); got != primaryBefore {
		t.Fatalf("primary same-named branch moved: got %s want %s", got, primaryBefore)
	}
	if internal.VerifyGitRef(f.repo, "primary-renamed") == nil {
		t.Fatal("primary repository gained renamed branch")
	}
	if internal.VerifyGitRef(secondary, oldBranch) == nil {
		t.Fatal("secondary old branch still exists")
	}
	if got := injectGit(t, secondary, "rev-parse", "primary-renamed"); got != secondaryBefore {
		t.Fatalf("secondary branch renamed to wrong commit: got %s want %s", got, secondaryBefore)
	}
	newPath := filepath.Join(f.featurePath, "worktrees", "renamed")
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("secondary worktree was not recreated at renamed path: %v", err)
	}
	if _, err := os.Readlink(filepath.Join(newPath, "CLAUDE.local.md")); err != nil {
		t.Fatalf("managed reinjection did not follow secondary rename: %v", err)
	}
	state, err := internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, link := range state.Roles[string(internal.ManagedTemplateRoleWorktree)].Links {
		if link.Worktree == "renamed" {
			found = true
		}
		if link.Worktree == "entry" {
			t.Fatalf("old managed link identity survived secondary rename: %+v", link)
		}
	}
	if !found {
		t.Fatalf("renamed secondary worktree link was not recorded: %+v",
			state.Roles[string(internal.ManagedTemplateRoleWorktree)].Links)
	}
}

func TestManagedTemplateBranchRenameSecondaryRepoArchived(t *testing.T) {
	f := newInjectFixture(t, "secondary-archived")
	t.Setenv("TWS_ROOT", f.workspace)
	secondary := createInjectRepo(t, f.root, "secondary")
	if err := os.MkdirAll(filepath.Join(f.repo, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(secondary, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, ".tws", "config.yaml"), []byte(
		"branch_prefix: primary-\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondary, ".tws", "config.yaml"), []byte(
		"branch_prefix: secondary-\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	const oldBranch = "same-archived"
	primaryBefore := injectGit(t, f.repo, "rev-parse", "main")
	secondaryBefore := injectGit(t, secondary, "rev-parse", "main")
	injectGit(t, f.repo, "branch", oldBranch, "main")
	injectGit(t, secondary, "branch", oldBranch, "main")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: oldBranch, Repo: secondary, Archived: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.repo); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{
		"branch", f.feature, "entry", "renamed-archived",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if got := injectGit(t, f.repo, "rev-parse", oldBranch); got != primaryBefore {
		t.Fatalf("primary archived same-named branch moved: got %s want %s", got, primaryBefore)
	}
	if internal.VerifyGitRef(f.repo, "primary-renamed-archived") == nil {
		t.Fatal("primary repository gained archived renamed branch")
	}
	if internal.VerifyGitRef(secondary, oldBranch) == nil {
		t.Fatal("secondary archived old branch still exists")
	}
	if got := injectGit(t, secondary, "rev-parse", "primary-renamed-archived"); got != secondaryBefore {
		t.Fatalf("secondary archived branch renamed to wrong commit: got %s want %s", got, secondaryBefore)
	}
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	entry := internal.GetBranch(stack, "renamed-archived")
	if entry.Name == "" || !entry.Archived || entry.Repo != secondary ||
		entry.Branch != "primary-renamed-archived" {
		t.Fatalf("archived secondary metadata changed incorrectly: %+v", entry)
	}
}

func TestNamedTemplateExternalAddHonorsExecutionRootAndOwnsInitialLinks(t *testing.T) {
	f := newInjectFixture(t, "managed-add")
	if err := os.RemoveAll(f.featurePath); err != nil {
		t.Fatal(err)
	}
	selectedFeaturePath := filepath.Join(os.Getenv("TWS_ROOT"), f.feature)
	oldTmux := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })
	source := managedCLIPackage(t, filepath.Join(f.root, "managed-add-source"), map[string]string{
		"inject/context.md": "context\n",
	}, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "managed-add", source); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, addCmd(), []string{
		f.feature, "--template", "global:managed-add", "--agent", "claude",
		"--new", "root", "--base", "main",
	}, nil); err != nil {
		t.Fatalf("managed external add failed: %v", err)
	}
	if _, err := os.Stat(internal.ManagedTemplateStatePath(selectedFeaturePath)); err != nil {
		t.Fatalf("managed state was not written to the execution-selected TWS_ROOT: %v", err)
	}
	worktree := filepath.Join(selectedFeaturePath, "worktrees", "root")
	if info, err := os.Lstat(filepath.Join(worktree, "context.md")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("initial managed worktree link missing: info=%v err=%v", info, err)
	}
	if _, err := os.Lstat(f.featurePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed add ignored TWS_ROOT and wrote to the configured workspace: %v", err)
	}
}

func TestManagedTemplateDefaultsConfigAndLegacyNonMigration(t *testing.T) {
	f := newManagedCLIFixture(t)
	if err := addCheckout(f.ws, "legacy", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	f.replaceGlobalTemplate(t, "default-worktree", map[string]string{
		"inject/default.md": "default\n",
	})
	if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{
		AgentCommand:     "copilot",
		WorktreeTemplate: "global:default-worktree",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, addCmd(), []string{"new-default"}, nil); err != nil {
		t.Fatalf("configured default did not opt new feature into managed mode: %v", err)
	}
	state, err := internal.LoadManagedTemplateState(f.ws.FeaturePath("new-default"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Roles["worktree"].Source.Name != "default-worktree" ||
		state.Roles["orchestrator"].Source.Kind != "builtin" ||
		state.Roles["worktree"].Agent != "copilot" {
		t.Fatalf("configured/default selections were not persisted: %+v", state)
	}
	for _, path := range []string{
		filepath.Join(f.ws.FeaturePath("new-default"), "inject", ".github", "instructions", "tws-worktree.instructions.md"),
		filepath.Join(f.ws.FeaturePath("new-default"), "AGENTS.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing Copilot native guidance %s: %v", path, err)
		}
	}

	if _, _, err := runManagedCobra(t, addCmd(), []string{"legacy"}, nil); err != nil {
		t.Fatalf("existing legacy feature add failed: %v", err)
	}
	if present, err := internal.ManagedTemplateStatePresent(f.ws.FeaturePath("legacy")); err != nil || present {
		t.Fatalf("role defaults implicitly migrated existing legacy feature: present=%v err=%v", present, err)
	}

	var commandErr error
	stdout := captureStdout(t, func() {
		cmd := configShowCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		commandErr = cmd.Execute()
	})
	err = commandErr
	if err != nil || !strings.Contains(stdout, "worktree_template: global:default-worktree") ||
		!strings.Contains(stdout, "orchestrator_template: (builtin when managed)") {
		t.Fatalf("config show omitted role defaults: out=%q err=%v", stdout, err)
	}
	stdout = captureStdout(t, func() {
		cmd := configGetCmd()
		cmd.SetArgs([]string{"worktree_template"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		commandErr = cmd.Execute()
	})
	err = commandErr
	if err != nil || strings.TrimSpace(stdout) != "global:default-worktree" {
		t.Fatalf("config get worktree_template: out=%q err=%v", stdout, err)
	}
	setCmd := configSetCmd()
	values, directive := setCmd.ValidArgsFunction(setCmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp ||
		!managedTestContains(values, "worktree_template") ||
		!managedTestContains(values, "orchestrator_template") {
		t.Fatalf("config completion omitted role keys: values=%v directive=%v", values, directive)
	}

}

func TestManagedTemplateExplicitRendererTracksLaunchConfiguration(t *testing.T) {
	f := newManagedCLIFixture(t)
	const initialCommand = "custom-agent-wrapper --profile copilot"
	if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{AgentCommand: initialCommand}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, addCmd(), []string{"explicit-renderer", "--agent", "copilot"}, nil); err != nil {
		t.Fatal(err)
	}
	featurePath := f.ws.FeaturePath("explicit-renderer")
	state, err := internal.LoadManagedTemplateState(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	worktreeRole := state.Roles[string(internal.ManagedTemplateRoleWorktree)]
	if !worktreeRole.AgentExplicit || worktreeRole.AgentCommandAtSelect != initialCommand {
		t.Fatalf("explicit renderer provenance was not persisted: %+v", worktreeRole)
	}
	before := snapshotTree(t, featurePath)
	if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{
		AgentCommand: "custom-agent-wrapper --profile changed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"explicit-renderer"}, nil); err == nil ||
		!strings.Contains(err.Error(), "agent_command is now") {
		t.Fatalf("later launch configuration change was not diagnosed: %v", err)
	}
	if _, err := internal.PlanCheckoutSessionLinks(f.ws, "explicit-renderer", ""); err == nil ||
		!strings.Contains(err.Error(), "agent_command is now") {
		t.Fatalf("checkout context accepted changed launch configuration: %v", err)
	}
	if after := snapshotTree(t, featurePath); after != before {
		t.Fatal("launch-configuration mismatch changed managed assets or state")
	}
}

func managedTestContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestManagedTemplateMalformedConfigAndMutationReservations(t *testing.T) {
	t.Run("malformed-config", func(t *testing.T) {
		f := newManagedCLIFixture(t)
		if err := os.MkdirAll(filepath.Dir(internal.ConfigPath()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(internal.ConfigPath(), []byte("worktree_template: [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, addCmd(), []string{"new-feature"}, nil); err == nil ||
			!strings.Contains(err.Error(), "parsing config") {
			t.Fatalf("managed default resolution suppressed malformed config: %v", err)
		}
		if err := addCheckout(f.ws, "legacy", nil, "", "", false, false, false); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, addCmd(), []string{"legacy"}, nil); err != nil {
			t.Fatalf("legacy existing add should preserve best-effort config behavior: %v", err)
		}
	})

	t.Run("checkout-mutation-lock", func(t *testing.T) {
		f := newManagedCLIFixture(t)
		f.replaceGlobalTemplate(t, "locked", map[string]string{"inject/shared.md": "v1\n"})
		if _, _, err := runManagedCobra(t, addCmd(), []string{"feature", "--template", "global:locked"}, nil); err != nil {
			t.Fatal(err)
		}
		ownerState := filepath.Join(f.root, "foreign-sync-state.yaml")
		if _, err := internal.AcquireCheckoutMutationLock(
			f.ws.CheckoutStateDir(), "foreign-token", "other", "sync", ownerState,
		); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = internal.ReleaseCheckoutMutationLock(f.ws.CheckoutStateDir(), "foreign-token")
		})
		before := snapshotTree(t, f.ws.FeaturePath("feature"))
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature"}, nil); err == nil ||
			!strings.Contains(err.Error(), "checkout mutation lock held by live") {
			t.Fatalf("foreign checkout mutation did not block managed writer: %v", err)
		}
		if after := snapshotTree(t, f.ws.FeaturePath("feature")); after != before {
			t.Fatal("mutation-lock refusal changed managed feature")
		}
		if err := internal.ReleaseCheckoutMutationLock(f.ws.CheckoutStateDir(), "foreign-token"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestManagedTemplatePostReservationSessionRaceRefuses(t *testing.T) {
	f := newInjectFixture(t, "session-race")
	oldTmux := managedTemplateTmuxProbe
	oldHook := managedTemplatePostConsentHook
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() {
		managedTemplateTmuxProbe = oldTmux
		managedTemplatePostConsentHook = oldHook
	})

	source := managedCLIPackage(t, filepath.Join(f.root, "race-source"), map[string]string{
		"inject/shared.md": "v1\n",
	}, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "race", source); err != nil {
		t.Fatal(err)
	}
	worktree := f.addWorktree(t, f.repo, "root", "race-root")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "root", Branch: "race-root"}}}); err != nil {
		t.Fatal(err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := internal.PlanManagedTemplate(internal.ManagedTemplatePlanRequest{
		Workspace: ws, Feature: f.feature, FeaturePath: f.featurePath,
		Roles:    []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
		Worktree: internal.ManagedTemplateRoleRequest{Reference: "global:race", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := internal.ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	resolved, err := internal.ResolveTemplateReference(f.repo, "global:race")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resolved.Path, "inject", "shared.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, f.featurePath)
	var intentToken string
	managedTemplatePostConsentHook = func() error {
		var err error
		intentToken, err = internal.CreateExternalSessionIntent(f.featurePath, f.feature, []string{"root"}, false)
		return err
	}
	_, _, err = runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--scope", "worktree"}, nil)
	managedTemplatePostConsentHook = nil
	if intentToken != "" {
		_ = internal.RemoveOwnedExternalSessionIntent(f.featurePath, intentToken)
	}
	if err == nil || !strings.Contains(err.Error(), "active external session") {
		t.Fatalf("post-reservation session race was accepted: %v", err)
	}
	if after := snapshotTree(t, f.featurePath); after != before {
		t.Fatal("session-race refusal changed feature assets or ownership")
	}
	if data, err := os.ReadFile(filepath.Join(worktree, "shared.md")); err != nil || string(data) != "v1\n" {
		t.Fatalf("session-race refusal changed linked content: %q %v", data, err)
	}
}

func TestManagedTemplatePendingLedgerFailsClosedBeforeLegacyDispatch(t *testing.T) {
	f := newInjectFixture(t, "pending-ledger")
	t.Setenv("TWS_ROOT", f.workspace)
	worktree := f.addWorktree(t, f.repo, "entry", "pending-ledger-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "pending-ledger-entry",
	}}}); err != nil {
		t.Fatal(err)
	}
	oldTmux := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })

	source := managedCLIPackage(t, filepath.Join(f.root, "pending-v1"), map[string]string{
		"inject/context.md": "v1\n",
	}, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "pending", source); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, addCmd(), []string{
		f.feature, "--template", "global:pending", "--agent", "copilot",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := internal.RemoveNamedTemplate("", internal.TemplateScopeGlobal, "pending"); err != nil {
		t.Fatal(err)
	}
	source = managedCLIPackage(t, filepath.Join(f.root, "pending-v2"), map[string]string{
		"inject/context.md": "v2\n",
	}, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "pending", source); err != nil {
		t.Fatal(err)
	}

	var durableIntentPath, durablePrivatePath string
	internal.ManagedTemplateIntentDurableHook = func(effect, original, private, intentPath string) error {
		if effect != "preserve-active" || original != ".tws-templates.json" {
			return nil
		}
		if _, err := os.Stat(internal.ManagedTemplateStatePath(f.featurePath)); err != nil {
			return fmt.Errorf("ledger moved before durable intent hook: %w", err)
		}
		privatePath := filepath.Join(f.featurePath, filepath.FromSlash(private))
		if _, err := os.Lstat(privatePath); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recovery object existed before durable preserve intent: %v", err)
		}
		data, err := os.ReadFile(intentPath)
		if err != nil {
			return err
		}
		var intent map[string]any
		if err := json.Unmarshal(data, &intent); err != nil {
			return err
		}
		expected, ok := intent["expected_file"].(map[string]any)
		rootIdentity, rootOK := intent["root_identity"].(map[string]any)
		if !ok || !rootOK || intent["kind"] != "file" ||
			intent["private_path"] != private || expected["hash"] == "" ||
			expected["mode"] == float64(0) || expected["device"] == float64(0) ||
			expected["inode"] == float64(0) || rootIdentity["device"] == float64(0) ||
			rootIdentity["inode"] == float64(0) {
			return fmt.Errorf("durable ledger intent is incomplete: %s", data)
		}
		durableIntentPath = intentPath
		durablePrivatePath = privatePath
		return nil
	}
	internal.ManagedTemplateApplyStepHook = func(step, path string) error {
		if step == "after-state-preserve" {
			return errors.New("simulated process interruption after ledger preservation")
		}
		return nil
	}
	_, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature}, nil)
	internal.ManagedTemplateApplyStepHook = nil
	internal.ManagedTemplateIntentDurableHook = nil
	if err == nil || !strings.Contains(err.Error(), "after ledger preservation") {
		t.Fatalf("ledger preservation checkpoint did not stop: %v", err)
	}
	if durableIntentPath == "" || durablePrivatePath == "" {
		t.Fatal("ledger preserve intent was not observed before mutation")
	}
	if _, err := os.Lstat(internal.ManagedTemplateStatePath(f.featurePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ledger unexpectedly remained active after preservation checkpoint: %v", err)
	}
	if _, err := os.Stat(durablePrivatePath); err != nil {
		t.Fatalf("durable intent does not identify the preserved ledger: %v", err)
	}
	if _, err := os.Stat(durableIntentPath); err != nil {
		t.Fatalf("durable intent record disappeared after interruption: %v", err)
	}
	intentEntries, err := os.ReadDir(filepath.Dir(durableIntentPath))
	if err != nil {
		t.Fatal(err)
	}
	effects := make(map[string]bool)
	nextSequence := 0
	for _, entry := range intentEntries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "intent-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(durableIntentPath), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var intent map[string]any
		if err := json.Unmarshal(data, &intent); err != nil {
			t.Fatal(err)
		}
		if intent["sequence"] != float64(nextSequence) {
			t.Fatalf("intent sequence is not append-only and contiguous: %s", data)
		}
		nextSequence++
		effect, _ := intent["effect"].(string)
		effects[effect] = true
		rootIdentity, rootOK := intent["root_identity"].(map[string]any)
		if intent["original_path"] == "" || intent["private_path"] == "" ||
			intent["kind"] == "" || !rootOK ||
			rootIdentity["device"] == float64(0) || rootIdentity["inode"] == float64(0) {
			t.Fatalf("intent omits reconstructible path/root evidence: %s", data)
		}
		switch intent["kind"] {
		case "file":
			expected, ok := intent["expected_file"].(map[string]any)
			if !ok || expected["hash"] == "" || expected["mode"] == float64(0) {
				t.Fatalf("file intent omits expected hash/mode: %s", data)
			}
		case "link":
			expected, ok := intent["expected_link"].(map[string]any)
			if !ok || expected["target"] == "" {
				t.Fatalf("link intent omits expected target: %s", data)
			}
		default:
			t.Fatalf("intent has unknown kind: %s", data)
		}
	}
	for _, effect := range []string{"stage-create", "publish-active", "preserve-active"} {
		if !effects[effect] {
			t.Fatalf("pending evidence omitted %s intent: effects=%v", effect, effects)
		}
	}
	recoveryPath, err := internal.ManagedTemplateRecoveryResidue(f.featurePath)
	if err != nil || recoveryPath == "" {
		t.Fatalf("pending recovery evidence was not discoverable: path=%q err=%v", recoveryPath, err)
	}

	assertRecoveryRefusal := func(label string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "managed template recovery evidence exists") {
			t.Fatalf("%s did not fail closed on pending ledger evidence: %v", label, err)
		}
	}
	before := snapshotTree(t, f.featurePath)
	_, _, err = runManagedCobra(t, templateSyncCmd(), []string{f.feature}, nil)
	assertRecoveryRefusal("no-flag template sync", err)
	_, _, err = runManagedCobra(t, addCmd(), []string{f.feature}, nil)
	assertRecoveryRefusal("no-flag add", err)
	_, err = runInjectCommand(t, f.feature, "entry")
	assertRecoveryRefusal("inject", err)
	_, _, err = runManagedCobra(t, openCmd(), []string{f.feature, "entry", "--no-agent"}, nil)
	assertRecoveryRefusal("open", err)
	exportPath := filepath.Join(f.root, "pending-export.yaml")
	_, _, err = runManagedCobra(t, exportCmd(), []string{f.feature, "-o", exportPath}, nil)
	assertRecoveryRefusal("export", err)
	if _, err := os.Lstat(exportPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed export wrote output despite pending recovery: %v", err)
	}
	if after := snapshotTree(t, f.featurePath); after != before {
		t.Fatal("pending-ledger dispatch refusal changed feature assets or evidence")
	}
	for _, legacyPath := range []string{
		filepath.Join(f.featurePath, "inject", "CLAUDE.local.md"),
		filepath.Join(f.featurePath, ".claude"),
	} {
		if _, err := os.Lstat(legacyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending ledger dispatched to legacy and created %s: %v", legacyPath, err)
		}
	}
	if _, err := os.Readlink(filepath.Join(worktree, ".github", "instructions", "tws-worktree.instructions.md")); err != nil {
		t.Fatalf("existing managed Copilot link was disturbed: %v", err)
	}

	legacyFeature := "legacy-safe"
	legacyPath := filepath.Join(f.workspace, legacyFeature)
	if err := os.MkdirAll(filepath.Join(legacyPath, "inject"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(legacyPath, "inject", "user.md")
	if err := os.WriteFile(legacyFile, []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyBefore := snapshotTree(t, legacyPath)
	_, _, err = runManagedCobra(t, templateSyncCmd(), []string{"--all"}, nil)
	assertRecoveryRefusal("mixed --all template sync", err)
	if after := snapshotTree(t, legacyPath); after != legacyBefore {
		t.Fatal("mixed --all recovery refusal mutated a genuine legacy feature")
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{legacyFeature}, nil); err != nil {
		t.Fatalf("genuine legacy feature without recovery residue was refused: %v", err)
	}
}

func TestManagedTemplateWriterRefusesOrphanRecoverableSyncEvidence(t *testing.T) {
	const marker = "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock"
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, featurePath, feature string)
	}{
		{
			name: "valid scoped payload",
			setup: func(t *testing.T, featurePath, feature string) {
				t.Helper()
				state := internal.NewSyncRunState(feature, marker, "orphan-owner", internal.SyncRunPolicy{})
				if err := internal.SaveSyncRunState(featurePath, state); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "legacy sentinel",
			setup: func(t *testing.T, featurePath, feature string) {
				t.Helper()
				if err := internal.SaveSyncState(featurePath, &internal.SyncState{
					StartedAt: "2026-10-09T00:00:00Z", FailedBranch: marker,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInjectFixture(t, "orphan-control")
			oldTmux := managedTemplateTmuxProbe
			managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
			t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "orchestrator", "--agent", "claude",
			}, nil); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, f.featurePath, f.feature)
			before := snapshotTree(t, f.featurePath)
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "orchestrator", "--agent", "copilot", "--allow-managed-removal",
			}, nil); err == nil || !strings.Contains(err.Error(), "sync recovery evidence") {
				t.Fatalf("managed writer accepted orphan recovery evidence: %v", err)
			}
			if after := snapshotTree(t, f.featurePath); after != before {
				t.Fatal("orphan recovery refusal changed feature assets or recovery evidence")
			}
			if _, err := os.Lstat(internal.SyncRunGuardPath(f.featurePath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("managed writer left its temporary sync guard behind: %v", err)
			}
		})
	}
}

func TestManagedTemplateVerifiedNoopStillRefusesReparentState(t *testing.T) {
	f := newInjectFixture(t, "noop-reparent")
	t.Setenv("TWS_ROOT", f.workspace)
	worktree := f.addWorktree(t, f.repo, "entry", "noop-reparent-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "noop-reparent-entry",
	}}}); err != nil {
		t.Fatal(err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := internal.PlanManagedTemplate(internal.ManagedTemplatePlanRequest{
		Workspace: ws, Feature: f.feature, FeaturePath: f.featurePath,
		Roles: []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
		Agent: "claude", AgentSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := internal.ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(worktree, "CLAUDE.local.md")); err != nil {
		t.Fatal(err)
	}
	loc := internal.ReparentLocation{
		Mode: internal.ModeExternal, Feature: f.feature, FeaturePath: f.featurePath,
	}
	if err := os.WriteFile(internal.ReparentStatePath(loc), []byte("state_version: [broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, f.featurePath)
	if _, err := runInjectCommand(t, f.feature, "entry"); err == nil ||
		!strings.Contains(err.Error(), "stack reparent is in progress") {
		t.Fatalf("verified no-op bypassed reparent state: %v", err)
	}
	if after := snapshotTree(t, f.featurePath); after != before {
		t.Fatal("no-op reparent refusal changed managed assets or recovery state")
	}
}

func TestManagedTemplateRemovalWarningIncludesOwnedExternalLinks(t *testing.T) {
	f := newInjectFixture(t, "link-removal")
	oldTTY := managedTemplateInputIsTTY
	oldTmux := managedTemplateTmuxProbe
	managedTemplateInputIsTTY = func(io.Reader) bool { return true }
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() {
		managedTemplateInputIsTTY = oldTTY
		managedTemplateTmuxProbe = oldTmux
	})

	source := managedCLIPackage(t, filepath.Join(f.root, "link-removal-source"), map[string]string{
		"inject/obsolete.md": "obsolete\n",
	}, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "link-removal", source); err != nil {
		t.Fatal(err)
	}
	worktree := f.addWorktree(t, f.repo, "root", "link-removal-root")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "root", Branch: "link-removal-root"}}}); err != nil {
		t.Fatal(err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := internal.PlanManagedTemplate(internal.ManagedTemplatePlanRequest{
		Workspace: ws, Feature: f.feature, FeaturePath: f.featurePath,
		Roles:    []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
		Worktree: internal.ManagedTemplateRoleRequest{Reference: "global:link-removal", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := internal.ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := internal.RemoveNamedTemplate("", internal.TemplateScopeGlobal, "link-removal"); err != nil {
		t.Fatal(err)
	}
	empty := managedCLIPackage(t, filepath.Join(f.root, "link-removal-empty"), nil, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "link-removal", empty); err != nil {
		t.Fatal(err)
	}

	before := snapshotTree(t, f.featurePath)
	_, stderr, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--scope", "worktree"}, strings.NewReader("no\n"))
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("link retirement cancellation failed: %v", err)
	}
	for _, want := range []string{
		"kind=link",
		filepath.Join(worktree, "obsolete.md"),
		"kind=file",
		filepath.Join(f.featurePath, "inject", "obsolete.md"),
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("removal warning omitted %q:\n%s", want, stderr)
		}
	}
	if after := snapshotTree(t, f.featurePath); after != before {
		t.Fatal("declined external-link retirement changed feature")
	}

	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--scope", "worktree"}, strings.NewReader("yes\n")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(worktree, "obsolete.md"),
		filepath.Join(f.featurePath, "inject", "obsolete.md"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("consented retirement left %s: %v", path, err)
		}
	}
}
