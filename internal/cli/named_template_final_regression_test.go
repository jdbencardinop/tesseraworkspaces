package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func copyManagedTreeForTest(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		target := filepath.Join(destination, rel)
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func installManagedLaunchFakes(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "launch.log")
	for _, name := range []string{"claude", "copilot", "managed-shell"} {
		script := "#!/bin/sh\n" +
			"echo " + name + " \"$@\" >> " + log + "\n" +
			"exit 0\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tmux := "#!/bin/sh\n" +
		"echo tmux \"$@\" >> " + log + "\n" +
		"if [ \"$1\" = \"has-session\" ]; then exit 1; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(tmux), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SHELL", filepath.Join(dir, "managed-shell"))
	return log
}

type managedSecondaryFixture struct {
	inject    *injectFixture
	secondary string
	observer  string
	worktree  string
}

func newManagedSecondaryFixture(t *testing.T, feature string, primaryCfg, secondaryCfg internal.Config) managedSecondaryFixture {
	t.Helper()
	f := newInjectFixture(t, feature)
	t.Setenv("TWS_ROOT", f.workspace)
	secondary := createInjectRepo(t, f.root, "secondary")
	for repo, cfg := range map[string]internal.Config{f.repo: primaryCfg, secondary: secondaryCfg} {
		if err := os.MkdirAll(filepath.Join(repo, ".tws"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := internal.SaveConfigFile(internal.RepoConfigPathFor(repo), cfg); err != nil {
			t.Fatal(err)
		}
		injectGit(t, repo, "add", ".tws/config.yaml")
		injectGit(t, repo, "commit", "-m", "test repo config")
	}
	worktree := f.addWorktree(t, f.repo, "entry", feature+"-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: feature + "-entry",
	}}}); err != nil {
		t.Fatal(err)
	}
	oldTmux := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		feature, "--scope", "both",
	}, nil); err != nil {
		t.Fatal(err)
	}
	observer := filepath.Join(f.root, "secondary-observer")
	injectGit(t, secondary, "worktree", "add", "-b", feature+"-observer", observer, "main")
	if err := os.Chdir(observer); err != nil {
		t.Fatal(err)
	}
	return managedSecondaryFixture{inject: f, secondary: secondary, observer: observer, worktree: worktree}
}

func TestManagedTemplateOwningConfigFlowsToLaunchAndInjection(t *testing.T) {
	falseValue := false
	trueValue := true
	t.Run("direct primary config", func(t *testing.T) {
		fx := newManagedSecondaryFixture(t, "config-direct",
			internal.Config{AgentCommand: "claude", UseTmux: &falseValue},
			internal.Config{AgentCommand: "copilot", UseTmux: &trueValue, InjectInto: ".context"},
		)
		log := installManagedLaunchFakes(t)
		if _, _, err := runManagedCobra(t, openCmd(), []string{fx.inject.feature, "entry"}, nil); err != nil {
			t.Fatalf("managed direct open did not use owning config snapshot: %v", err)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "claude") || strings.Contains(text, "copilot") ||
			strings.Contains(text, "tmux new-session") {
			t.Fatalf("managed direct launch used secondary config:\n%s", text)
		}
		if _, err := os.Readlink(filepath.Join(fx.worktree, "CLAUDE.local.md")); err != nil {
			t.Fatalf("primary root injection was not preserved: %v", err)
		}
	})

	t.Run("tmux primary config", func(t *testing.T) {
		fx := newManagedSecondaryFixture(t, "config-tmux",
			internal.Config{AgentCommand: "claude", UseTmux: &trueValue},
			internal.Config{AgentCommand: "copilot", UseTmux: &falseValue},
		)
		log := installManagedLaunchFakes(t)
		if _, _, err := runManagedCobra(t, openCmd(), []string{fx.inject.feature, "entry"}, nil); err != nil {
			t.Fatalf("managed tmux open did not use owning config snapshot: %v", err)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "tmux new-session") ||
			!strings.Contains(text, "tmux send-keys -t config-tmux-entry claude Enter") ||
			strings.Contains(text, "tmux send-keys -t config-tmux-entry copilot") {
			t.Fatalf("managed tmux launch used secondary config:\n%s", text)
		}
	})

	t.Run("derived nonroot refuses but explicit root succeeds", func(t *testing.T) {
		fx := newManagedSecondaryFixture(t, "config-into",
			internal.Config{AgentCommand: "claude"},
			internal.Config{AgentCommand: "copilot"},
		)
		if err := internal.SaveConfigFile(internal.RepoConfigPathFor(fx.inject.repo), internal.Config{
			AgentCommand: "claude", InjectInto: ".context",
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, openCmd(), []string{
			fx.inject.feature, "entry", "--no-agent",
		}, nil); err == nil || !strings.Contains(err.Error(), "configured inject_into") {
			t.Fatalf("derived primary nonroot policy was bypassed: %v", err)
		}
		if _, _, err := runManagedCobra(t, injectCmd(), []string{
			fx.inject.feature, "entry", "--into", ".other",
		}, nil); err == nil || !strings.Contains(err.Error(), "explicit --into") {
			t.Fatalf("explicit nonroot refusal did not explain flag precedence: %v", err)
		}
		if _, _, err := runManagedCobra(t, injectCmd(), []string{
			fx.inject.feature, "entry", "--into", ".",
		}, nil); err != nil {
			t.Fatalf("explicit root override did not supersede primary config: %v", err)
		}
	})
}

func TestManagedTemplateOwningConfigCommandsUsePrimaryRepository(t *testing.T) {
	fx := newManagedSecondaryFixture(t, "config-command",
		internal.Config{AgentCommand: "claude"},
		internal.Config{AgentCommand: "copilot"},
	)
	primarySource := managedCLIPackage(t, filepath.Join(fx.inject.root, "primary-config-template"), map[string]string{
		"inject/primary.md": "primary\n",
	}, "")
	secondarySource := managedCLIPackage(t, filepath.Join(fx.inject.root, "secondary-config-template"), map[string]string{
		"inject/secondary.md": "secondary\n",
	}, "")
	if err := internal.AddNamedTemplate(fx.inject.repo, internal.TemplateScopeRepo, "primary", primarySource); err != nil {
		t.Fatal(err)
	}
	if err := internal.AddNamedTemplate(fx.secondary, internal.TemplateScopeRepo, "secondary", secondarySource); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveConfigFile(internal.RepoConfigPathFor(fx.inject.repo), internal.Config{
		AgentCommand: "claude", WorktreeTemplate: "repo:primary",
	}); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveConfigFile(internal.RepoConfigPathFor(fx.secondary), internal.Config{
		AgentCommand: "copilot", WorktreeTemplate: "repo:secondary",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fx.observer, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveConfigFile(internal.RepoConfigPathFor(fx.observer), internal.Config{
		AgentCommand: "copilot", WorktreeTemplate: "repo:secondary",
	}); err != nil {
		t.Fatal(err)
	}
	var showErr error
	show := captureStdout(t, func() {
		_, _, showErr = runManagedCobra(t, configShowCmd(), nil, nil)
	})
	if showErr != nil || !strings.Contains(show, "agent_command: claude") ||
		strings.Contains(show, "agent_command: copilot") {
		t.Fatalf("config show used secondary repository: out=%q err=%v", show, showErr)
	}
	var getErr error
	got := captureStdout(t, func() {
		_, _, getErr = runManagedCobra(t, configGetCmd(), []string{"worktree_template"}, nil)
	})
	if getErr != nil || strings.TrimSpace(got) != "repo:primary" {
		t.Fatalf("config get used secondary repository: out=%q err=%v", got, getErr)
	}
	setCmd := configSetCmd()
	values, directive := setCmd.ValidArgsFunction(setCmd, []string{"worktree_template"}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp || !managedTestContains(values, "repo:primary") ||
		managedTestContains(values, "repo:secondary") {
		t.Fatalf("config completion used wrong repository: values=%v directive=%v", values, directive)
	}
	var setErr error
	_ = captureStdout(t, func() {
		_, _, setErr = runManagedCobra(t, configSetCmd(), []string{
			"orchestrator_template", "repo:primary", "--repo",
		}, nil)
	})
	if setErr != nil {
		t.Fatal(setErr)
	}
	primaryCfg := internal.LoadConfigFile(internal.RepoConfigPathFor(fx.inject.repo))
	secondaryCfg := internal.LoadConfigFile(internal.RepoConfigPathFor(fx.secondary))
	if primaryCfg.OrchestratorTemplate != "repo:primary" ||
		secondaryCfg.OrchestratorTemplate != "" {
		t.Fatalf("config set --repo wrote wrong repository: primary=%+v secondary=%+v", primaryCfg, secondaryCfg)
	}
}

func TestManagedTemplateOrchestratorLaunchAdmission(t *testing.T) {
	t.Run("inferred later command change", func(t *testing.T) {
		f := newManagedCLIFixture(t)
		source := managedCLIPackage(t, filepath.Join(f.root, "orchestrator-inferred"), map[string]string{
			"feature/notes.md": "notes\n",
		}, "")
		if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "orchestrator-inferred", source); err != nil {
			t.Fatal(err)
		}
		if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{AgentCommand: "claude"}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, addCmd(), []string{
			"inferred", "--template", "global:orchestrator-inferred",
		}, nil); err != nil {
			t.Fatal(err)
		}
		if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{AgentCommand: "copilot"}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, openCmd(), []string{
			"inferred", "--feature-dir", "--no-agent",
		}, nil); err == nil || !strings.Contains(err.Error(), "saved orchestrator rendering agent") {
			t.Fatalf("checkout feature-dir bypassed inferred orchestrator provenance: %v", err)
		}
	})

	t.Run("explicit different renderer remains until actual command changes", func(t *testing.T) {
		_ = newManagedCLIFixture(t)
		if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{AgentCommand: "claude"}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, addCmd(), []string{
			"explicit", "--agent", "copilot",
		}, nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, openCmd(), []string{
			"explicit", "--feature-dir", "--no-agent",
		}, nil); err != nil {
			t.Fatalf("unchanged explicit renderer was rejected: %v", err)
		}
		if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{AgentCommand: "claude --changed"}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, openCmd(), []string{
			"explicit", "--feature-dir", "--no-agent",
		}, nil); err == nil || !strings.Contains(err.Error(), "agent_command is now") {
			t.Fatalf("later actual command change bypassed explicit orchestrator provenance: %v", err)
		}
	})

	t.Run("modified external orchestrator and all", func(t *testing.T) {
		f := newInjectFixture(t, "orchestrator-external")
		t.Setenv("TWS_ROOT", f.workspace)
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
			f.feature, "--scope", "orchestrator", "--agent", "claude",
		}, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.featurePath, "CLAUDE.md"), []byte("operator edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, openCmd(), []string{
			f.feature, "--feature-dir", "--no-agent",
		}, nil); err == nil || !strings.Contains(err.Error(), "modified") {
			t.Fatalf("external feature-dir accepted modified orchestrator guidance: %v", err)
		}
		log := fakeManagedTmuxOnPath(t)
		if _, _, err := runManagedCobra(t, openCmd(), []string{f.feature, "--all"}, nil); err == nil {
			t.Fatal("external --all accepted modified orchestrator guidance")
		}
		if _, err := os.Lstat(log); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orchestrator refusal reached tmux: %v", err)
		}
	})

	t.Run("checkout pending recovery fast path", func(t *testing.T) {
		f := newManagedCLIFixture(t)
		if _, _, err := runManagedCobra(t, addCmd(), []string{
			"pending", "--agent", "claude",
		}, nil); err != nil {
			t.Fatal(err)
		}
		featurePath := f.ws.FeaturePath("pending")
		recovery := filepath.Join(featurePath, ".tws-template-recovery", "pending")
		if err := os.MkdirAll(recovery, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(internal.ManagedTemplateStatePath(featurePath),
			filepath.Join(recovery, "old-ledger")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, openCmd(), []string{
			"pending", "--feature-dir", "--no-agent",
		}, nil); err == nil || !strings.Contains(err.Error(), "managed template recovery evidence exists") {
			t.Fatalf("checkout feature-dir pending recovery fast path succeeded: %v", err)
		}
	})
}

func TestManagedTemplateNewUsesControllingRepositoryAndPreflights(t *testing.T) {
	t.Run("omitted and explicit repo", func(t *testing.T) {
		fx := newManagedSecondaryFixture(t, "managed-new",
			internal.Config{AgentCommand: "claude"},
			internal.Config{AgentCommand: "copilot"},
		)
		if _, _, err := runManagedCobra(t, newCmd(), []string{
			fx.inject.feature, "primary-entry", "--base", "main",
		}, nil); err != nil {
			t.Fatal(err)
		}
		primaryPath := filepath.Join(fx.inject.featurePath, "worktrees", "primary-entry")
		primaryRoot, err := internal.MainRepoRootIn(primaryPath)
		if err != nil {
			t.Fatal(err)
		}
		wantPrimary, err := internal.MainRepoRootIn(fx.inject.repo)
		if err != nil {
			t.Fatal(err)
		}
		if !sameResolvedRepoRoot(primaryRoot, wantPrimary) {
			t.Fatalf("omitted --repo created managed worktree in secondary repo: got %s want %s", primaryRoot, wantPrimary)
		}
		stack, err := internal.LoadStack(fx.inject.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		if entry := internal.GetBranch(stack, "primary-entry"); entry.Repo != "" {
			t.Fatalf("primary managed entry persisted a secondary repo: %+v", entry)
		}

		if _, _, err := runManagedCobra(t, newCmd(), []string{
			fx.inject.feature, "secondary-entry", "--base", "main", "--repo", fx.secondary,
		}, nil); err != nil {
			t.Fatal(err)
		}
		secondaryPath := filepath.Join(fx.inject.featurePath, "worktrees", "secondary-entry")
		secondaryRoot, err := internal.MainRepoRootIn(secondaryPath)
		if err != nil {
			t.Fatal(err)
		}
		wantSecondary, err := internal.MainRepoRootIn(fx.secondary)
		if err != nil {
			t.Fatal(err)
		}
		if !sameResolvedRepoRoot(secondaryRoot, wantSecondary) {
			t.Fatalf("explicit --repo did not select secondary repository: got %s want %s", secondaryRoot, wantSecondary)
		}
		stack, err = internal.LoadStack(fx.inject.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		if entry := internal.GetBranch(stack, "secondary-entry"); entry.Repo == "" ||
			!sameResolvedRepoRoot(entry.Repo, wantSecondary) {
			t.Fatalf("explicit secondary repository was not persisted: %+v", entry)
		}
	})

	t.Run("context refusal before Git", func(t *testing.T) {
		fx := newManagedSecondaryFixture(t, "managed-new-refusal",
			internal.Config{AgentCommand: "claude"},
			internal.Config{AgentCommand: "copilot"},
		)
		if err := internal.SaveConfigFile(internal.RepoConfigPathFor(fx.inject.repo), internal.Config{
			AgentCommand: "claude", InjectInto: ".context",
		}); err != nil {
			t.Fatal(err)
		}
		before, err := internal.LoadStack(fx.inject.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, newCmd(), []string{
			fx.inject.feature, "blocked", "--base", "main",
		}, nil); err == nil || !strings.Contains(err.Error(), "repository-root injection") {
			t.Fatalf("managed new did not preflight context: %v", err)
		}
		if internal.VerifyGitRef(fx.inject.repo, "blocked") == nil ||
			internal.VerifyGitRef(fx.secondary, "blocked") == nil {
			t.Fatal("managed context refusal created a Git branch")
		}
		if _, err := os.Lstat(filepath.Join(fx.inject.featurePath, "worktrees", "blocked")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed context refusal created a worktree: %v", err)
		}
		after, err := internal.LoadStack(fx.inject.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		if len(after.Branches) != len(before.Branches) || internal.HasBranch(after, "blocked") {
			t.Fatalf("managed context refusal changed stack metadata: before=%+v after=%+v", before, after)
		}
	})

	t.Run("managed add new from secondary cwd", func(t *testing.T) {
		fx := newManagedSecondaryFixture(t, "managed-add-parent",
			internal.Config{AgentCommand: "claude"},
			internal.Config{AgentCommand: "copilot"},
		)
		if _, _, err := runManagedCobra(t, addCmd(), []string{
			"managed-add-new", "--agent", "claude", "--new", "root", "--base", "main",
		}, nil); err != nil {
			t.Fatal(err)
		}
		featurePath := filepath.Join(fx.inject.workspace, "managed-add-new")
		worktreePath := filepath.Join(featurePath, "worktrees", "root")
		got, err := internal.MainRepoRootIn(worktreePath)
		if err != nil {
			t.Fatal(err)
		}
		want, err := internal.MainRepoRootIn(fx.inject.repo)
		if err != nil {
			t.Fatal(err)
		}
		if !sameResolvedRepoRoot(got, want) {
			t.Fatalf("managed add -n used secondary cwd repository: got %s want %s", got, want)
		}
		stack, err := internal.LoadStack(featurePath)
		if err != nil {
			t.Fatal(err)
		}
		if entry := internal.GetBranch(stack, "root"); entry.Repo != "" {
			t.Fatalf("managed add -n persisted wrong repo: %+v", entry)
		}
	})
}

func TestManagedTemplateLifecycleReservationsProtectAdoptionAndRecovery(t *testing.T) {
	t.Run("first adoption blocks feature rename", func(t *testing.T) {
		f := newInjectFixture(t, "first-adoption")
		t.Setenv("TWS_ROOT", f.workspace)
		lock, err := internal.AcquireManagedTemplateLock(f.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release() //nolint:errcheck
		if _, _, err := runManagedCobra(t, renameCmd(), []string{
			"feature", f.feature, "moved",
		}, nil); err == nil {
			t.Fatal("feature rename moved a first-adoption writer before ledger publication")
		}
		if _, err := os.Stat(f.featurePath); err != nil {
			t.Fatalf("first-adoption refusal lost original feature: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(f.workspace, "moved")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("first-adoption refusal created rename destination: %v", err)
		}
	})

	for _, operation := range []string{"rename", "archive"} {
		t.Run("pending worktree recovery blocks "+operation, func(t *testing.T) {
			f := newInjectFixture(t, "pending-"+operation)
			t.Setenv("TWS_ROOT", f.workspace)
			worktree := f.addWorktree(t, f.repo, "entry", "pending-"+operation+"-entry")
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: "entry", Branch: "pending-" + operation + "-entry",
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
			recovery := filepath.Join(worktree, ".tws-template-recovery", "pending")
			if err := os.MkdirAll(recovery, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(recovery, "pending.json"), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			headBefore := injectGit(t, f.repo, "rev-parse", "pending-"+operation+"-entry")
			var commandErr error
			if operation == "rename" {
				_, _, commandErr = runManagedCobra(t, renameCmd(), []string{
					"branch", f.feature, "entry", "renamed",
				}, nil)
			} else {
				_, _, commandErr = runManagedCobra(t, archiveCmd(), []string{
					f.feature, "entry",
				}, nil)
			}
			if commandErr == nil || !strings.Contains(commandErr.Error(), "recovery evidence") {
				t.Fatalf("%s did not refuse pending worktree recovery: %v", operation, commandErr)
			}
			if got := injectGit(t, f.repo, "rev-parse", "pending-"+operation+"-entry"); got != headBefore {
				t.Fatalf("%s moved the Git ref before recovery refusal: got %s want %s", operation, got, headBefore)
			}
			if _, err := os.Stat(worktree); err != nil {
				t.Fatalf("%s removed the worktree before recovery refusal: %v", operation, err)
			}
			if _, err := os.Stat(filepath.Join(recovery, "pending.json")); err != nil {
				t.Fatalf("%s removed pending recovery evidence: %v", operation, err)
			}
			stackAfter, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			if string(stackAfter) != string(stackBefore) {
				t.Fatalf("%s changed stack before recovery refusal", operation)
			}
		})
	}
}

func TestManagedTemplateLifecycleMutationsRespectModeReservations(t *testing.T) {
	for _, operation := range []string{"rename", "archive"} {
		for _, blockedBy := range []string{"live mutation", "reparent recovery"} {
			t.Run("external "+operation+" refuses "+blockedBy, func(t *testing.T) {
				f := newInjectFixture(t, "external-"+operation+"-"+strings.ReplaceAll(blockedBy, " ", "-"))
				t.Setenv("TWS_ROOT", f.workspace)
				worktree := f.addWorktree(t, f.repo, "entry", f.feature+"-entry")
				if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
					Name: "entry", Branch: f.feature + "-entry",
				}}}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
					f.feature, "--scope", "worktree", "--agent", "claude",
				}, nil); err != nil {
					t.Fatal(err)
				}
				stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
				if err != nil {
					t.Fatal(err)
				}
				headBefore := injectGit(t, f.repo, "rev-parse", f.feature+"-entry")
				var reservationEvidence string
				if blockedBy == "live mutation" {
					if err := internal.ClaimSyncRunGuard(f.featurePath, "lifecycle-reservation-test"); err != nil {
						t.Fatal(err)
					}
					reservationEvidence = internal.SyncRunGuardPath(f.featurePath)
					t.Cleanup(func() {
						_ = internal.ReleaseOwnedSyncRunGuard(f.featurePath, "lifecycle-reservation-test")
					})
				} else {
					loc := internal.ReparentLocation{
						Mode: internal.ModeExternal, Feature: f.feature, FeaturePath: f.featurePath,
					}
					reservationEvidence = internal.ReparentStatePath(loc)
					if err := os.MkdirAll(filepath.Dir(reservationEvidence), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(reservationEvidence, []byte("state_version: [broken\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				evidenceBefore, err := os.ReadFile(reservationEvidence)
				if err != nil {
					t.Fatal(err)
				}
				var commandErr error
				if operation == "rename" {
					_, _, commandErr = runManagedCobra(t, renameCmd(), []string{
						"branch", f.feature, "entry", "renamed",
					}, nil)
				} else {
					_, _, commandErr = runManagedCobra(t, archiveCmd(), []string{f.feature, "entry"}, nil)
				}
				if commandErr == nil {
					t.Fatalf("%s crossed %s", operation, blockedBy)
				}
				if got := injectGit(t, f.repo, "rev-parse", f.feature+"-entry"); got != headBefore {
					t.Fatalf("%s changed the Git ref under %s: got %s want %s", operation, blockedBy, got, headBefore)
				}
				if _, err := os.Stat(worktree); err != nil {
					t.Fatalf("%s changed the worktree under %s: %v", operation, blockedBy, err)
				}
				stackAfter, err := os.ReadFile(internal.StackPath(f.featurePath))
				if err != nil {
					t.Fatal(err)
				}
				if string(stackAfter) != string(stackBefore) {
					t.Fatalf("%s changed stack metadata under %s", operation, blockedBy)
				}
				evidenceAfter, err := os.ReadFile(reservationEvidence)
				if err != nil || string(evidenceAfter) != string(evidenceBefore) {
					t.Fatalf("%s changed reservation/recovery evidence under %s: %v", operation, blockedBy, err)
				}
			})

			t.Run("checkout "+operation+" refuses "+blockedBy, func(t *testing.T) {
				f := newManagedCLIFixture(t)
				feature := "checkout-" + operation + "-" + strings.ReplaceAll(blockedBy, " ", "-")
				if _, _, err := runManagedCobra(t, addCmd(), []string{
					feature, "--agent", "claude",
				}, nil); err != nil {
					t.Fatal(err)
				}
				if err := createCheckoutBranch(f.ws, feature, "entry", "main", false); err != nil {
					t.Fatal(err)
				}
				featurePath := f.ws.FeaturePath(feature)
				stackBefore, err := os.ReadFile(internal.StackPath(featurePath))
				if err != nil {
					t.Fatal(err)
				}
				headBefore := injectGit(t, f.repo, "rev-parse", "entry")
				var reservationEvidence string
				if blockedBy == "live mutation" {
					statePath := filepath.Join(featurePath, ".test-live-mutation")
					if _, err := internal.AcquireCheckoutMutationLock(
						f.ws.CheckoutStateDir(), "lifecycle-reservation-test", feature, "sync", statePath,
					); err != nil {
						t.Fatal(err)
					}
					reservationEvidence = internal.CheckoutMutationLockPath(f.ws.CheckoutStateDir())
					t.Cleanup(func() {
						_ = internal.ReleaseCheckoutMutationLock(f.ws.CheckoutStateDir(), "lifecycle-reservation-test")
					})
				} else {
					loc := internal.ReparentLocationFor(f.ws, feature, featurePath)
					reservationEvidence = internal.ReparentStatePath(loc)
					if err := os.MkdirAll(filepath.Dir(reservationEvidence), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(reservationEvidence, []byte("state_version: [broken\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				evidenceBefore, err := os.ReadFile(reservationEvidence)
				if err != nil {
					t.Fatal(err)
				}
				var commandErr error
				if operation == "rename" {
					_, _, commandErr = runManagedCobra(t, renameCmd(), []string{
						"branch", feature, "entry", "renamed",
					}, nil)
				} else {
					_, _, commandErr = runManagedCobra(t, archiveCmd(), []string{feature, "entry"}, nil)
				}
				if commandErr == nil {
					t.Fatalf("%s crossed %s", operation, blockedBy)
				}
				if got := injectGit(t, f.repo, "rev-parse", "entry"); got != headBefore {
					t.Fatalf("%s changed the Git ref under %s: got %s want %s", operation, blockedBy, got, headBefore)
				}
				stackAfter, err := os.ReadFile(internal.StackPath(featurePath))
				if err != nil {
					t.Fatal(err)
				}
				if string(stackAfter) != string(stackBefore) {
					t.Fatalf("%s changed stack metadata under %s", operation, blockedBy)
				}
				evidenceAfter, err := os.ReadFile(reservationEvidence)
				if err != nil || string(evidenceAfter) != string(evidenceBefore) {
					t.Fatalf("%s changed reservation/recovery evidence under %s: %v", operation, blockedBy, err)
				}
			})
		}
	}
}

func TestManagedTemplateBranchRenameStrictlyPreflightsContext(t *testing.T) {
	for _, scenario := range []string{"malformed ledger", "changed launch command", "nonroot injection"} {
		t.Run(scenario, func(t *testing.T) {
			f := newInjectFixture(t, "rename-preflight-"+strings.ReplaceAll(scenario, " ", "-"))
			t.Setenv("TWS_ROOT", f.workspace)
			worktree := f.addWorktree(t, f.repo, "entry", f.feature+"-entry")
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: "entry", Branch: f.feature + "-entry",
			}}}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "worktree", "--agent", "claude",
			}, nil); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "malformed ledger":
				if err := os.WriteFile(
					internal.ManagedTemplateStatePath(f.featurePath),
					[]byte("{broken\n"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			case "changed launch command":
				if err := internal.SaveConfigFile(
					internal.RepoConfigPathFor(f.repo),
					internal.Config{AgentCommand: "copilot"},
				); err != nil {
					t.Fatal(err)
				}
			case "nonroot injection":
				if err := internal.SaveConfigFile(
					internal.RepoConfigPathFor(f.repo),
					internal.Config{AgentCommand: "claude", InjectInto: ".nested"},
				); err != nil {
					t.Fatal(err)
				}
			}
			stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			headBefore := injectGit(t, f.repo, "rev-parse", f.feature+"-entry")
			if _, _, err := runManagedCobra(t, renameCmd(), []string{
				"branch", f.feature, "entry", "renamed",
			}, nil); err == nil {
				t.Fatalf("rename crossed %s preflight", scenario)
			}
			if got := injectGit(t, f.repo, "rev-parse", f.feature+"-entry"); got != headBefore {
				t.Fatalf("rename changed original ref before %s refusal: got %s want %s", scenario, got, headBefore)
			}
			if internal.VerifyGitRef(f.repo, "renamed") == nil {
				t.Fatalf("rename created destination ref before %s refusal", scenario)
			}
			if _, err := os.Stat(worktree); err != nil {
				t.Fatalf("rename removed worktree before %s refusal: %v", scenario, err)
			}
			if _, err := os.Lstat(filepath.Join(f.featurePath, "worktrees", "renamed")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rename created worktree destination before %s refusal: %v", scenario, err)
			}
			stackAfter, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			if string(stackAfter) != string(stackBefore) {
				t.Fatalf("rename changed stack before %s refusal", scenario)
			}
		})
	}
}

func TestManagedTemplateArchiveReactivateRefreshesLinkIdentity(t *testing.T) {
	f := newInjectFixture(t, "archive-reactivate")
	t.Setenv("TWS_ROOT", f.workspace)
	worktree := f.addWorktree(t, f.repo, "entry", "archive-reactivate-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "archive-reactivate-entry",
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
	state, err := internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	var oldDevice, oldInode uint64
	for _, link := range state.Roles[string(internal.ManagedTemplateRoleWorktree)].Links {
		if link.Worktree == "entry" && link.Path == "CLAUDE.local.md" {
			oldDevice, oldInode = link.Device, link.Inode
		}
	}
	if oldInode == 0 {
		t.Fatal("initial managed link identity was not recorded")
	}
	if _, _, err := runManagedCobra(t, archiveCmd(), []string{f.feature, "entry"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive left materialized worktree: %v", err)
	}
	state, err = internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range state.Roles[string(internal.ManagedTemplateRoleWorktree)].Links {
		if link.Worktree == "entry" {
			t.Fatalf("archive retained stale managed link ownership: %+v", link)
		}
	}
	if _, _, err := runManagedCobra(t, newCmd(), []string{
		f.feature, "entry",
	}, nil); err != nil {
		t.Fatalf("reactivating archived managed entry failed: %v", err)
	}
	if _, _, err := runManagedCobra(t, openCmd(), []string{
		f.feature, "entry", "--no-agent",
	}, nil); err != nil {
		t.Fatalf("opening reactivated managed worktree failed: %v", err)
	}
	state, err = internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	var fresh bool
	for _, link := range state.Roles[string(internal.ManagedTemplateRoleWorktree)].Links {
		if link.Worktree == "entry" && link.Path == "CLAUDE.local.md" {
			fresh = link.Device != oldDevice || link.Inode != oldInode
		}
	}
	if !fresh {
		t.Fatalf("reactivated worktree retained stale link identity: %+v",
			state.Roles[string(internal.ManagedTemplateRoleWorktree)].Links)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{
		"branch", f.feature, "entry", "renamed",
	}, nil); err != nil {
		t.Fatalf("renaming reactivated managed worktree failed: %v", err)
	}
	renamedPath := filepath.Join(f.featurePath, "worktrees", "renamed")
	if _, err := os.Readlink(filepath.Join(renamedPath, "CLAUDE.local.md")); err != nil {
		t.Fatalf("renamed reactivated worktree was not reinjected: %v", err)
	}
	state, err = internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range state.Roles[string(internal.ManagedTemplateRoleWorktree)].Links {
		if link.Worktree == "entry" {
			t.Fatalf("rename retained archived/reactivated link ownership: %+v", link)
		}
	}
}

func TestManagedTemplateMissingLinksDoNotAdoptReplacementWorktree(t *testing.T) {
	f := newInjectFixture(t, "replacement-worktree")
	t.Setenv("TWS_ROOT", f.workspace)
	worktree := f.addWorktree(t, f.repo, "entry", "replacement-worktree-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "replacement-worktree-entry",
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "worktree", "--agent", "claude",
	}, nil); err != nil {
		t.Fatal(err)
	}
	ledgerBefore, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := internal.RunDir(f.repo, "git", "worktree", "remove", "--force", worktree); err != nil {
		t.Fatal(err)
	}
	if err := internal.RunDir(f.repo, "git", "worktree", "add", worktree, "replacement-worktree-entry"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(worktree, "CLAUDE.local.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement worktree unexpectedly retained managed leaf: %v", err)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "worktree",
	}, nil); err == nil || !strings.Contains(err.Error(), "Git worktree identity changed") {
		t.Fatalf("missing recorded leaves adopted an out-of-band replacement worktree: %v", err)
	}
	ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
	if err != nil || string(ledgerAfter) != string(ledgerBefore) {
		t.Fatalf("replacement-worktree refusal changed managed state: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(worktree, "CLAUDE.local.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement-worktree refusal published a managed leaf: %v", err)
	}
	stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	headBefore := injectGit(t, f.repo, "rev-parse", "replacement-worktree-entry")
	if _, _, err := runManagedCobra(t, renameCmd(), []string{
		"branch", f.feature, "entry", "renamed",
	}, nil); err == nil || !strings.Contains(err.Error(), "ownership retirement") {
		t.Fatalf("branch rename retired ownership from replacement worktree: %v", err)
	}
	if got := injectGit(t, f.repo, "rev-parse", "replacement-worktree-entry"); got != headBefore {
		t.Fatalf("branch rename changed original ref before ownership refusal: got %s want %s", got, headBefore)
	}
	if internal.VerifyGitRef(f.repo, "renamed") == nil {
		t.Fatal("branch rename created destination ref before ownership refusal")
	}
	stackAfter, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil || string(stackAfter) != string(stackBefore) {
		t.Fatalf("branch rename changed stack before ownership refusal: %v", err)
	}
	ledgerAfter, err = os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
	if err != nil || string(ledgerAfter) != string(ledgerBefore) {
		t.Fatalf("branch rename changed ledger before ownership refusal: %v", err)
	}
}

func TestManagedTemplateArchiveRetirementFailureAfterRemovalRetainsEvidence(t *testing.T) {
	f := newInjectFixture(t, "archive-retirement-failure")
	t.Setenv("TWS_ROOT", f.workspace)
	worktree := f.addWorktree(t, f.repo, "entry", "archive-retirement-failure-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "archive-retirement-failure-entry",
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "worktree", "--agent", "claude",
	}, nil); err != nil {
		t.Fatal(err)
	}
	stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	ledgerBefore, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	headBefore := injectGit(t, f.repo, "rev-parse", "archive-retirement-failure-entry")
	internal.ManagedTemplateApplyStepHook = func(step, path string) error {
		if step == "before-state" {
			return errors.New("retirement publication stopped")
		}
		return nil
	}
	_, _, archiveErr := runManagedCobra(t, archiveCmd(), []string{f.feature, "entry"}, nil)
	internal.ManagedTemplateApplyStepHook = nil
	if archiveErr == nil || !strings.Contains(archiveErr.Error(), "retirement publication stopped") {
		t.Fatalf("archive did not surface failed post-removal retirement publication: %v", archiveErr)
	}
	if _, err := os.Lstat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive reported post-removal publication failure without removing worktree: %v", err)
	}
	if got := injectGit(t, f.repo, "rev-parse", "archive-retirement-failure-entry"); got != headBefore {
		t.Fatalf("archive changed Git ref before link-retirement publication: got %s want %s", got, headBefore)
	}
	stackAfter, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil || string(stackAfter) != string(stackBefore) {
		t.Fatalf("archive changed stack before link-retirement publication: %v", err)
	}
	ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
	if err != nil || string(ledgerAfter) != string(ledgerBefore) {
		t.Fatalf("archive changed active ledger after failed publication: %v", err)
	}
	if residue, err := internal.ManagedTemplateRecoveryResidue(f.featurePath); err != nil || residue == "" {
		t.Fatalf("failed post-removal link retirement did not retain recovery evidence: residue=%q err=%v", residue, err)
	}
}

func TestManagedTemplateLockedWorktreeFailureDoesNotPublishRetirement(t *testing.T) {
	for _, operation := range []string{"archive", "rename"} {
		t.Run(operation, func(t *testing.T) {
			f := newInjectFixture(t, "locked-"+operation)
			t.Setenv("TWS_ROOT", f.workspace)
			gitBranch := "locked-" + operation + "-entry"
			worktree := f.addWorktree(t, f.repo, "entry", gitBranch)
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: "entry", Branch: gitBranch,
			}}}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "worktree", "--agent", "claude",
			}, nil); err != nil {
				t.Fatal(err)
			}
			ledgerBefore, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			linkPath := filepath.Join(worktree, "CLAUDE.local.md")
			linkBefore, err := os.Lstat(linkPath)
			if err != nil {
				t.Fatal(err)
			}
			targetBefore, err := os.Readlink(linkPath)
			if err != nil {
				t.Fatal(err)
			}
			headBefore := injectGit(t, f.repo, "rev-parse", gitBranch)
			injectGit(t, f.repo, "worktree", "lock", "--reason", "retirement failure test", worktree)
			t.Cleanup(func() {
				_ = internal.RunSilentDir(f.repo, "git", "worktree", "unlock", worktree)
			})

			var commandErr error
			if operation == "archive" {
				_, _, commandErr = runManagedCobra(t, archiveCmd(), []string{f.feature, "entry"}, nil)
			} else {
				_, _, commandErr = runManagedCobra(t, renameCmd(), []string{
					"branch", f.feature, "entry", "renamed",
				}, nil)
			}
			if commandErr == nil || !strings.Contains(commandErr.Error(), "removing worktree") {
				t.Fatalf("%s did not surface locked-worktree removal failure: %v", operation, commandErr)
			}
			ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
			if err != nil || string(ledgerAfter) != string(ledgerBefore) {
				t.Fatalf("%s published link retirement before failed Git removal: %v", operation, err)
			}
			stackAfter, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil || string(stackAfter) != string(stackBefore) {
				t.Fatalf("%s changed stack before failed Git removal: %v", operation, err)
			}
			linkAfter, err := os.Lstat(linkPath)
			if err != nil || !os.SameFile(linkBefore, linkAfter) {
				t.Fatalf("%s changed managed link identity before failed Git removal: %v", operation, err)
			}
			if got, err := os.Readlink(linkPath); err != nil || got != targetBefore {
				t.Fatalf("%s changed managed link target before failed Git removal: %q %v", operation, got, err)
			}
			if got := injectGit(t, f.repo, "rev-parse", gitBranch); got != headBefore {
				t.Fatalf("%s changed Git ref before failed removal: got %s want %s", operation, got, headBefore)
			}
			if operation == "rename" && internal.VerifyGitRef(f.repo, "renamed") == nil {
				t.Fatal("failed rename created destination Git ref")
			}
			if _, err := os.Stat(worktree); err != nil {
				t.Fatalf("%s removed locked worktree: %v", operation, err)
			}
			if residue, err := internal.ManagedTemplateRecoveryResidue(f.featurePath); err != nil || residue != "" {
				t.Fatalf("%s left managed recovery despite failed Git removal: residue=%q err=%v",
					operation, residue, err)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "worktree",
			}, nil); err != nil {
				t.Fatalf("%s failure left links unrecognized by subsequent sync: %v", operation, err)
			}
		})
	}
}

type managedRenameFailureFixture struct {
	inject       *injectFixture
	worktree     string
	gitBranch    string
	ledgerBefore []byte
	stackBefore  []byte
	stateBefore  internal.ManagedTemplateState
	headBefore   string
}

func newManagedRenameFailureFixture(t *testing.T, name string) managedRenameFailureFixture {
	t.Helper()
	f := newInjectFixture(t, name)
	t.Setenv("TWS_ROOT", f.workspace)
	gitBranch := name + "-entry"
	worktree := f.addWorktree(t, f.repo, "entry", gitBranch)
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: gitBranch,
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "worktree", "--agent", "claude",
	}, nil); err != nil {
		t.Fatal(err)
	}
	ledgerBefore, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	return managedRenameFailureFixture{
		inject: f, worktree: worktree, gitBranch: gitBranch,
		ledgerBefore: ledgerBefore, stackBefore: stackBefore, stateBefore: stateBefore,
		headBefore: injectGit(t, f.repo, "rev-parse", gitBranch),
	}
}

func assertManagedRenameOriginalContext(t *testing.T, fx managedRenameFailureFixture) {
	t.Helper()
	if got := injectGit(t, fx.inject.repo, "rev-parse", fx.gitBranch); got != fx.headBefore {
		t.Fatalf("original branch changed: got %s want %s", got, fx.headBefore)
	}
	if _, err := os.Stat(fx.worktree); err != nil {
		t.Fatalf("original worktree is unavailable: %v", err)
	}
	if _, err := os.Readlink(filepath.Join(fx.worktree, "CLAUDE.local.md")); err != nil {
		t.Fatalf("original managed link is unavailable: %v", err)
	}
	stackAfter, err := os.ReadFile(internal.StackPath(fx.inject.featurePath))
	if err != nil || string(stackAfter) != string(fx.stackBefore) {
		t.Fatalf("rename changed stack metadata: %v", err)
	}
	if internal.VerifyGitRef(fx.inject.repo, "renamed") == nil {
		t.Fatal("rename left destination branch")
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		fx.inject.feature, "--scope", "worktree",
	}, nil); err != nil {
		t.Fatalf("restored managed context does not pass ordinary sync: %v", err)
	}
}

func TestManagedTemplateBranchRenamePreflightsDestinationCollisions(t *testing.T) {
	t.Run("destination Git ref", func(t *testing.T) {
		fx := newManagedRenameFailureFixture(t, "rename-ref-collision")
		injectGit(t, fx.inject.repo, "branch", "renamed", "main")
		ledgerBefore := append([]byte(nil), fx.ledgerBefore...)
		linkPath := filepath.Join(fx.worktree, "CLAUDE.local.md")
		linkBefore, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, renameCmd(), []string{
			"branch", fx.inject.feature, "entry", "renamed",
		}, nil); err == nil || !strings.Contains(err.Error(), "destination Git branch") {
			t.Fatalf("rename did not preflight destination Git ref: %v", err)
		}
		ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(fx.inject.featurePath))
		if err != nil || string(ledgerAfter) != string(ledgerBefore) {
			t.Fatalf("ref collision changed managed ledger: %v", err)
		}
		stackAfter, err := os.ReadFile(internal.StackPath(fx.inject.featurePath))
		if err != nil || string(stackAfter) != string(fx.stackBefore) {
			t.Fatalf("ref collision changed stack metadata: %v", err)
		}
		if got := injectGit(t, fx.inject.repo, "rev-parse", fx.gitBranch); got != fx.headBefore {
			t.Fatalf("ref collision changed original branch: got %s want %s", got, fx.headBefore)
		}
		if _, err := os.Stat(fx.worktree); err != nil {
			t.Fatalf("ref collision removed original worktree: %v", err)
		}
		if linkAfter, err := os.Lstat(linkPath); err != nil || !os.SameFile(linkBefore, linkAfter) {
			t.Fatalf("ref collision changed managed link identity: %v", err)
		}
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
			fx.inject.feature, "--scope", "worktree",
		}, nil); err != nil {
			t.Fatalf("ref collision disturbed managed links: %v", err)
		}
	})

	t.Run("destination path", func(t *testing.T) {
		fx := newManagedRenameFailureFixture(t, "rename-path-collision")
		newPath := filepath.Join(fx.inject.featurePath, "worktrees", "renamed")
		if err := os.MkdirAll(newPath, 0o755); err != nil {
			t.Fatal(err)
		}
		userFile := filepath.Join(newPath, "user.txt")
		if err := os.WriteFile(userFile, []byte("user\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(fx.worktree, "CLAUDE.local.md")
		linkBefore, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, renameCmd(), []string{
			"branch", fx.inject.feature, "entry", "renamed",
		}, nil); err == nil || !strings.Contains(err.Error(), "destination worktree path") {
			t.Fatalf("rename did not preflight destination path: %v", err)
		}
		if data, err := os.ReadFile(userFile); err != nil || string(data) != "user\n" {
			t.Fatalf("path collision changed user destination: %q %v", data, err)
		}
		ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(fx.inject.featurePath))
		if err != nil || string(ledgerAfter) != string(fx.ledgerBefore) {
			t.Fatalf("path collision changed managed ledger: %v", err)
		}
		stackAfter, err := os.ReadFile(internal.StackPath(fx.inject.featurePath))
		if err != nil || string(stackAfter) != string(fx.stackBefore) {
			t.Fatalf("path collision changed stack metadata: %v", err)
		}
		if got := injectGit(t, fx.inject.repo, "rev-parse", fx.gitBranch); got != fx.headBefore {
			t.Fatalf("path collision changed original branch: got %s want %s", got, fx.headBefore)
		}
		if linkAfter, err := os.Lstat(linkPath); err != nil || !os.SameFile(linkBefore, linkAfter) {
			t.Fatalf("path collision changed managed link identity: %v", err)
		}
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
			fx.inject.feature, "--scope", "worktree",
		}, nil); err != nil {
			t.Fatalf("path collision disturbed managed links: %v", err)
		}
	})
}

func TestManagedTemplateBranchRenameReconcilesContextAfterLaterGitFailures(t *testing.T) {
	for _, failureStep := range []string{"branch rename", "new worktree add"} {
		t.Run(failureStep, func(t *testing.T) {
			fx := newManagedRenameFailureFixture(t, "rename-failure-"+strings.ReplaceAll(failureStep, " ", "-"))
			oldHook := renameBranchExternalStepHook
			renameBranchExternalStepHook = func(step string) error {
				if (failureStep == "branch rename" && step == "before-branch-rename") ||
					(failureStep == "new worktree add" && step == "before-new-worktree-add") {
					return errors.New("injected " + failureStep + " failure")
				}
				return nil
			}
			t.Cleanup(func() { renameBranchExternalStepHook = oldHook })
			if _, _, err := runManagedCobra(t, renameCmd(), []string{
				"branch", fx.inject.feature, "entry", "renamed",
			}, nil); err == nil || !strings.Contains(err.Error(), "injected "+failureStep+" failure") {
				t.Fatalf("rename did not surface injected failure: %v", err)
			}
			renameBranchExternalStepHook = oldHook
			assertManagedRenameOriginalContext(t, fx)
			stateAfter, err := internal.LoadManagedTemplateState(fx.inject.featurePath)
			if err != nil {
				t.Fatal(err)
			}
			var before, after internal.ManagedTemplateOwnedLink
			for _, link := range fx.stateBefore.Roles[string(internal.ManagedTemplateRoleWorktree)].Links {
				if link.Worktree == "entry" && link.Path == "CLAUDE.local.md" {
					before = link
				}
			}
			for _, link := range stateAfter.Roles[string(internal.ManagedTemplateRoleWorktree)].Links {
				if link.Worktree == "entry" && link.Path == "CLAUDE.local.md" {
					after = link
				}
			}
			if after.Inode == 0 || (after.Inode == before.Inode &&
				after.WorktreeInode == before.WorktreeInode &&
				after.GitDirInode == before.GitDirInode) {
				t.Fatalf("rollback restored stale managed identities: before=%+v after=%+v", before, after)
			}
			if residue, err := internal.ManagedTemplateRecoveryResidue(fx.inject.featurePath); err != nil || residue != "" {
				t.Fatalf("successful rollback reconciliation left recovery evidence: residue=%q err=%v", residue, err)
			}
		})
	}
}

func TestManagedTemplateBranchRenameRollbackFailurePreservesEvidence(t *testing.T) {
	t.Run("worktree restore failure", func(t *testing.T) {
		fx := newManagedRenameFailureFixture(t, "rename-rollback-add-failure")
		oldHook := renameBranchExternalStepHook
		renameBranchExternalStepHook = func(step string) error {
			switch step {
			case "before-branch-rename":
				return errors.New("injected branch rename failure")
			case "before-rollback-worktree-add":
				return errors.New("injected rollback worktree failure")
			default:
				return nil
			}
		}
		t.Cleanup(func() { renameBranchExternalStepHook = oldHook })
		_, _, err := runManagedCobra(t, renameCmd(), []string{
			"branch", fx.inject.feature, "entry", "renamed",
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "injected rollback worktree failure") {
			t.Fatalf("rollback failure was hidden: %v", err)
		}
		if _, statErr := os.Lstat(fx.worktree); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed rollback reported original worktree restored: %v", statErr)
		}
		if residue, residueErr := internal.ManagedTemplateRecoveryResidue(fx.inject.featurePath); residueErr != nil || residue == "" {
			t.Fatalf("failed rollback did not preserve evidence: residue=%q err=%v", residue, residueErr)
		}
	})

	t.Run("foreign original ref drift", func(t *testing.T) {
		fx := newManagedRenameFailureFixture(t, "rename-rollback-ref-drift")
		if err := os.WriteFile(filepath.Join(fx.inject.repo, "main-change.txt"), []byte("main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		injectGit(t, fx.inject.repo, "add", "main-change.txt")
		injectGit(t, fx.inject.repo, "commit", "-m", "advance main")
		foreignHead := injectGit(t, fx.inject.repo, "rev-parse", "main")
		oldHook := renameBranchExternalStepHook
		renameBranchExternalStepHook = func(step string) error {
			switch step {
			case "before-new-worktree-add":
				return errors.New("injected new worktree failure")
			case "before-rollback-rename":
				injectGit(t, fx.inject.repo, "branch", fx.gitBranch, "main")
			}
			return nil
		}
		t.Cleanup(func() { renameBranchExternalStepHook = oldHook })
		_, _, err := runManagedCobra(t, renameCmd(), []string{
			"branch", fx.inject.feature, "entry", "renamed",
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "partial") {
			t.Fatalf("foreign ref drift was not reported as a partial outcome: %v", err)
		}
		if got := injectGit(t, fx.inject.repo, "rev-parse", fx.gitBranch); got != foreignHead {
			t.Fatalf("rollback overwrote foreign original ref: got %s want %s", got, foreignHead)
		}
		if got := injectGit(t, fx.inject.repo, "rev-parse", "renamed"); got != fx.headBefore {
			t.Fatalf("renamed ref lost attributable original commit: got %s want %s", got, fx.headBefore)
		}
		if residue, residueErr := internal.ManagedTemplateRecoveryResidue(fx.inject.featurePath); residueErr != nil || residue == "" {
			t.Fatalf("foreign ref drift did not preserve evidence: residue=%q err=%v", residue, residueErr)
		}
	})
}

func managedRenameRecoveryRecord(t *testing.T, featurePath string) map[string]any {
	t.Helper()
	recovery, err := internal.ManagedTemplateRecoveryResidue(featurePath)
	if err != nil || recovery == "" {
		t.Fatalf("managed rename recovery evidence missing: path=%q err=%v", recovery, err)
	}
	var recordPath string
	if err := filepath.Walk(recovery, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() && info.Name() == "rename.json" {
			recordPath = path
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if recordPath == "" {
		t.Fatalf("managed rename recovery record missing below %s", recovery)
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"stage", "completed_stages", "pending_stages", "old_git_branch",
		"new_git_branch", "old_path", "new_path", "expected_oid",
		"stack_preimage_sha256", "owner",
	} {
		if record[field] == nil || record[field] == "" {
			t.Fatalf("managed rename recovery record omits %s: %s", field, data)
		}
	}
	return record
}

func TestManagedTemplateBranchRenameRollbackRevalidatesOwnedSources(t *testing.T) {
	for _, drift := range []string{"edit", "delete", "chmod", "symlink"} {
		t.Run(drift, func(t *testing.T) {
			fx := newManagedRenameFailureFixture(t, "rename-source-"+drift)
			source := filepath.Join(fx.inject.featurePath, "inject", "CLAUDE.local.md")
			oldHook := renameBranchExternalStepHook
			renameBranchExternalStepHook = func(step string) error {
				switch step {
				case "before-branch-rename":
					return errors.New("injected branch rename failure")
				case "before-rollback-worktree-add":
					switch drift {
					case "edit":
						return os.WriteFile(source, []byte("operator edit\n"), 0o644)
					case "delete":
						return os.Remove(source)
					case "chmod":
						return os.Chmod(source, 0o600)
					case "symlink":
						backup := source + ".operator"
						if err := os.Rename(source, backup); err != nil {
							return err
						}
						return os.Symlink(filepath.Base(backup), source)
					}
				}
				return nil
			}
			t.Cleanup(func() { renameBranchExternalStepHook = oldHook })
			_, _, err := runManagedCobra(t, renameCmd(), []string{
				"branch", fx.inject.feature, "entry", "renamed",
			}, nil)
			renameBranchExternalStepHook = oldHook
			if err == nil || !strings.Contains(err.Error(), "partial outcome") {
				t.Fatalf("rollback source %s drift did not fail with durable partial outcome: %v", drift, err)
			}
			if _, err := os.Stat(fx.worktree); err != nil {
				t.Fatalf("rollback source %s drift lost restored worktree: %v", drift, err)
			}
			if _, err := os.Lstat(filepath.Join(fx.worktree, "CLAUDE.local.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rollback source %s drift exposed a managed link: %v", drift, err)
			}
			ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(fx.inject.featurePath))
			if err != nil || string(ledgerAfter) != string(fx.ledgerBefore) {
				t.Fatalf("rollback source %s drift refreshed ownership: %v", drift, err)
			}
			switch drift {
			case "edit":
				if data, readErr := os.ReadFile(source); readErr != nil || string(data) != "operator edit\n" {
					t.Fatalf("operator edit was not preserved: %q %v", data, readErr)
				}
			case "delete":
				if _, statErr := os.Lstat(source); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("operator deletion was not preserved: %v", statErr)
				}
			case "chmod":
				info, statErr := os.Stat(source)
				if statErr != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("operator chmod was not preserved: info=%v err=%v", info, statErr)
				}
			case "symlink":
				if info, statErr := os.Lstat(source); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("operator symlink was not preserved: info=%v err=%v", info, statErr)
				}
			}
			managedRenameRecoveryRecord(t, fx.inject.featurePath)
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				fx.inject.feature, "--scope", "worktree",
			}, nil); err == nil || !strings.Contains(err.Error(), "recovery evidence") {
				t.Fatalf("rollback source %s drift did not fail closed afterward: %v", drift, err)
			}
		})
	}

	t.Run("after stage before publication", func(t *testing.T) {
		fx := newManagedRenameFailureFixture(t, "rename-source-prepublish")
		source := filepath.Join(fx.inject.featurePath, "inject", "CLAUDE.local.md")
		oldRenameHook := renameBranchExternalStepHook
		oldApplyHook := internal.ManagedTemplateApplyStepHook
		renameBranchExternalStepHook = func(step string) error {
			if step == "before-branch-rename" {
				return errors.New("injected branch rename failure")
			}
			return nil
		}
		internal.ManagedTemplateApplyStepHook = func(step, path string) error {
			if step == "before-link" {
				return os.WriteFile(source, []byte("late operator edit\n"), 0o644)
			}
			return nil
		}
		t.Cleanup(func() {
			renameBranchExternalStepHook = oldRenameHook
			internal.ManagedTemplateApplyStepHook = oldApplyHook
		})
		_, _, err := runManagedCobra(t, renameCmd(), []string{
			"branch", fx.inject.feature, "entry", "renamed",
		}, nil)
		renameBranchExternalStepHook = oldRenameHook
		internal.ManagedTemplateApplyStepHook = oldApplyHook
		if err == nil || !strings.Contains(err.Error(), "partial outcome") {
			t.Fatalf("late rollback source drift did not fail with partial outcome: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(fx.worktree, "CLAUDE.local.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("late rollback source drift exposed a managed link: %v", err)
		}
		ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(fx.inject.featurePath))
		if err != nil || string(ledgerAfter) != string(fx.ledgerBefore) {
			t.Fatalf("late rollback source drift refreshed ownership: %v", err)
		}
		managedRenameRecoveryRecord(t, fx.inject.featurePath)
	})
}

func TestManagedTemplateBranchRenamePostGitFailuresPreserveEvidence(t *testing.T) {
	t.Run("before stack publication", func(t *testing.T) {
		fx := newManagedRenameFailureFixture(t, "rename-before-stack")
		oldHook := renameBranchExternalStepHook
		renameBranchExternalStepHook = func(step string) error {
			if step == "before-stack-publication" {
				return errors.New("injected stack publication failure")
			}
			return nil
		}
		t.Cleanup(func() { renameBranchExternalStepHook = oldHook })
		_, _, err := runManagedCobra(t, renameCmd(), []string{
			"branch", fx.inject.feature, "entry", "renamed",
		}, nil)
		renameBranchExternalStepHook = oldHook
		if err == nil || !strings.Contains(err.Error(), "partial outcome") ||
			!strings.Contains(err.Error(), "injected stack publication failure") {
			t.Fatalf("before-stack failure lacked durable partial outcome: %v", err)
		}
		if got := injectGit(t, fx.inject.repo, "rev-parse", "renamed"); got != fx.headBefore {
			t.Fatalf("completed renamed ref changed: got %s want %s", got, fx.headBefore)
		}
		if internal.VerifyGitRef(fx.inject.repo, fx.gitBranch) == nil {
			t.Fatal("before-stack failure left original Git ref")
		}
		if _, err := os.Stat(filepath.Join(fx.inject.featurePath, "worktrees", "renamed")); err != nil {
			t.Fatalf("before-stack failure lost completed new worktree: %v", err)
		}
		stackAfter, err := os.ReadFile(internal.StackPath(fx.inject.featurePath))
		if err != nil || string(stackAfter) != string(fx.stackBefore) {
			t.Fatalf("before-stack failure changed stack preimage: %v", err)
		}
		record := managedRenameRecoveryRecord(t, fx.inject.featurePath)
		if record["stage"] != "stack-publication" {
			t.Fatalf("wrong partial stage: %+v", record)
		}
		if _, _, err := runManagedCobra(t, renameCmd(), []string{
			"branch", fx.inject.feature, "entry", "again",
		}, nil); err == nil || !strings.Contains(err.Error(), "recovery evidence") {
			t.Fatalf("next rename did not fail closed: %v", err)
		}
	})

	t.Run("actual stack filesystem write failure", func(t *testing.T) {
		fx := newManagedRenameFailureFixture(t, "rename-stack-filesystem")
		stackPath := internal.StackPath(fx.inject.featurePath)
		backup := stackPath + ".operator-backup"
		oldHook := renameBranchExternalStepHook
		renameBranchExternalStepHook = func(step string) error {
			if step != "before-stack-publication" {
				return nil
			}
			if err := os.Rename(stackPath, backup); err != nil {
				return err
			}
			return os.Mkdir(stackPath, 0o755)
		}
		t.Cleanup(func() { renameBranchExternalStepHook = oldHook })
		_, _, err := runManagedCobra(t, renameCmd(), []string{
			"branch", fx.inject.feature, "entry", "renamed",
		}, nil)
		renameBranchExternalStepHook = oldHook
		if err == nil || !strings.Contains(err.Error(), "partial outcome") ||
			!strings.Contains(err.Error(), "saving stack") {
			t.Fatalf("actual stack write failure lacked durable partial outcome: %v", err)
		}
		if data, readErr := os.ReadFile(backup); readErr != nil || string(data) != string(fx.stackBefore) {
			t.Fatalf("stack preimage was not preserved by atomic publication: %q %v", data, readErr)
		}
		if info, statErr := os.Stat(stackPath); statErr != nil || !info.IsDir() {
			t.Fatalf("operator filesystem change was not preserved: info=%v err=%v", info, statErr)
		}
		managedRenameRecoveryRecord(t, fx.inject.featurePath)
	})

	t.Run("retirement publication", func(t *testing.T) {
		fx := newManagedRenameFailureFixture(t, "rename-retirement-publication")
		oldHook := internal.ManagedTemplateApplyStepHook
		internal.ManagedTemplateApplyStepHook = func(step, path string) error {
			if step == "before-state" {
				return errors.New("injected retirement publication failure")
			}
			return nil
		}
		t.Cleanup(func() { internal.ManagedTemplateApplyStepHook = oldHook })
		_, _, err := runManagedCobra(t, renameCmd(), []string{
			"branch", fx.inject.feature, "entry", "renamed",
		}, nil)
		internal.ManagedTemplateApplyStepHook = oldHook
		if err == nil || !strings.Contains(err.Error(), "partial outcome") ||
			!strings.Contains(err.Error(), "retirement publication") {
			t.Fatalf("retirement publication failure bypassed partial evidence: %v", err)
		}
		if got := injectGit(t, fx.inject.repo, "rev-parse", "renamed"); got != fx.headBefore {
			t.Fatalf("retirement failure lost completed renamed ref: got %s want %s", got, fx.headBefore)
		}
		if _, err := os.Stat(filepath.Join(fx.inject.featurePath, "worktrees", "renamed")); err != nil {
			t.Fatalf("retirement failure lost completed new worktree: %v", err)
		}
		managedRenameRecoveryRecord(t, fx.inject.featurePath)
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
			fx.inject.feature,
		}, nil); err == nil || !strings.Contains(err.Error(), "recovery evidence") {
			t.Fatalf("retirement failure did not block later managed operation: %v", err)
		}
	})
}

func TestManagedTemplateArchivedBranchRenameMetadataFailurePreservesEvidence(t *testing.T) {
	f := newInjectFixture(t, "archived-metadata-failure")
	t.Setenv("TWS_ROOT", f.workspace)
	secondary := createInjectRepo(t, f.root, "archived-secondary")
	if err := internal.SaveConfigFile(internal.RepoConfigPathFor(f.repo), internal.Config{
		AgentCommand: "claude", BranchPrefix: "primary-",
	}); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveConfigFile(internal.RepoConfigPathFor(secondary), internal.Config{
		AgentCommand: "copilot", BranchPrefix: "secondary-",
	}); err != nil {
		t.Fatal(err)
	}
	const oldGitBranch = "decoupled-archived-ref"
	injectGit(t, f.repo, "branch", oldGitBranch, "main")
	injectGit(t, secondary, "branch", oldGitBranch, "main")
	primaryBefore := injectGit(t, f.repo, "rev-parse", oldGitBranch)
	secondaryBefore := injectGit(t, secondary, "rev-parse", oldGitBranch)
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: oldGitBranch, Repo: secondary, Archived: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "both", "--agent", "claude",
	}, nil); err != nil {
		t.Fatal(err)
	}
	stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	ledgerBefore, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	oldHook := renameBranchExternalStepHook
	renameBranchExternalStepHook = func(step string) error {
		if step == "before-stack-publication" {
			return errors.New("injected archived stack publication failure")
		}
		return nil
	}
	t.Cleanup(func() { renameBranchExternalStepHook = oldHook })
	_, _, err = runManagedCobra(t, renameCmd(), []string{
		"branch", f.feature, "entry", "renamed-archived",
	}, nil)
	renameBranchExternalStepHook = oldHook
	if err == nil || !strings.Contains(err.Error(), "partial outcome") ||
		!strings.Contains(err.Error(), "archived stack publication") {
		t.Fatalf("archived managed metadata failure lacked durable partial outcome: %v", err)
	}
	if got := injectGit(t, f.repo, "rev-parse", oldGitBranch); got != primaryBefore {
		t.Fatalf("archived rename changed primary same-named ref: got %s want %s", got, primaryBefore)
	}
	if internal.VerifyGitRef(secondary, oldGitBranch) == nil {
		t.Fatal("archived rename left old secondary ref")
	}
	if got := injectGit(t, secondary, "rev-parse", "primary-renamed-archived"); got != secondaryBefore {
		t.Fatalf("archived renamed ref has wrong commit: got %s want %s", got, secondaryBefore)
	}
	stackAfter, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil || string(stackAfter) != string(stackBefore) {
		t.Fatalf("archived metadata failure changed stack preimage: %v", err)
	}
	ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
	if err != nil || string(ledgerAfter) != string(ledgerBefore) {
		t.Fatalf("archived metadata failure changed ownership ledger: %v", err)
	}
	record := managedRenameRecoveryRecord(t, f.featurePath)
	if record["stage"] != "stack-publication" {
		t.Fatalf("archived partial outcome recorded wrong stage: %+v", record)
	}
	completed, _ := record["completed_stages"].([]any)
	if len(completed) != 1 || completed[0] != "branch-renamed" {
		t.Fatalf("archived partial outcome fabricated worktree stages: %+v", record)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature}, nil); err == nil ||
		!strings.Contains(err.Error(), "recovery evidence") {
		t.Fatalf("archived partial outcome did not block later managed operation: %v", err)
	}
}

func TestManagedTemplateBranchRenameRejectsOverlappingWorktreePaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		oldName string
		newName string
	}{
		{name: "descendant", oldName: "entry", newName: "entry/renamed"},
		{name: "ancestor", oldName: "entry/child", newName: "entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInjectFixture(t, "rename-overlap-"+tc.name)
			t.Setenv("TWS_ROOT", f.workspace)
			gitBranch := "rename-overlap-" + tc.name
			worktree := f.addWorktree(t, f.repo, tc.oldName, gitBranch)
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: tc.oldName, Branch: gitBranch,
			}}}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "worktree", "--agent", "claude",
			}, nil); err != nil {
				t.Fatal(err)
			}
			ledgerBefore, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			linkPath := filepath.Join(worktree, "CLAUDE.local.md")
			linkBefore, err := os.Lstat(linkPath)
			if err != nil {
				t.Fatal(err)
			}
			headBefore := injectGit(t, f.repo, "rev-parse", gitBranch)
			if _, _, err := runManagedCobra(t, renameCmd(), []string{
				"branch", f.feature, tc.oldName, tc.newName,
			}, nil); err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("%s rename did not refuse overlapping geometry: %v", tc.name, err)
			}
			if got := injectGit(t, f.repo, "rev-parse", gitBranch); got != headBefore {
				t.Fatalf("%s overlap changed original ref: got %s want %s", tc.name, got, headBefore)
			}
			if _, err := os.Stat(worktree); err != nil {
				t.Fatalf("%s overlap removed original worktree: %v", tc.name, err)
			}
			if linkAfter, err := os.Lstat(linkPath); err != nil || !os.SameFile(linkBefore, linkAfter) {
				t.Fatalf("%s overlap changed managed link identity: %v", tc.name, err)
			}
			ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
			if err != nil || string(ledgerAfter) != string(ledgerBefore) {
				t.Fatalf("%s overlap changed ledger: %v", tc.name, err)
			}
			stackAfter, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil || string(stackAfter) != string(stackBefore) {
				t.Fatalf("%s overlap changed stack: %v", tc.name, err)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "worktree",
			}, nil); err != nil {
				t.Fatalf("%s overlap disturbed subsequent sync: %v", tc.name, err)
			}
		})
	}

	t.Run("prefix and disjoint slash controls", func(t *testing.T) {
		for _, tc := range []struct {
			oldName string
			newName string
		}{
			{oldName: "entry", newName: "entry-two"},
			{oldName: "deep/entry", newName: "other/renamed"},
		} {
			f := newInjectFixture(t, "rename-disjoint-"+strings.ReplaceAll(tc.oldName, "/", "-"))
			t.Setenv("TWS_ROOT", f.workspace)
			gitBranch := "rename-disjoint-" + strings.ReplaceAll(tc.oldName, "/", "-")
			f.addWorktree(t, f.repo, tc.oldName, gitBranch)
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: tc.oldName, Branch: gitBranch,
			}}}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "worktree", "--agent", "claude",
			}, nil); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, renameCmd(), []string{
				"branch", f.feature, tc.oldName, tc.newName,
			}, nil); err != nil {
				t.Fatalf("disjoint rename %q -> %q was refused: %v", tc.oldName, tc.newName, err)
			}
		}
	})
}

func TestManagedTemplateBranchRenameRejectsPhysicalCaseAliasOverlap(t *testing.T) {
	t.Run("identity helper controls", func(t *testing.T) {
		root := t.TempDir()
		entry := filepath.Join(root, "entry")
		if err := os.MkdirAll(entry, 0o755); err != nil {
			t.Fatal(err)
		}
		if overlap, err := managedRenamePathsOverlap(entry, filepath.Join(entry, ".", "child")); err != nil || !overlap {
			t.Fatalf("dot-normalized descendant was not detected: overlap=%v err=%v", overlap, err)
		}
		if overlap, err := managedRenamePathsOverlap(entry, filepath.Join(root, "entry-two")); err != nil || overlap {
			t.Fatalf("sibling prefix was treated as overlap: overlap=%v err=%v", overlap, err)
		}
	})

	for _, tc := range []struct {
		name    string
		oldName string
		newName string
	}{
		{name: "case descendant", oldName: "entry", newName: "ENTRY/renamed"},
		{name: "case ancestor", oldName: "entry/child", newName: "ENTRY"},
		{name: "case equality", oldName: "entry", newName: "ENTRY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInjectFixture(t, "rename-case-"+strings.ReplaceAll(tc.name, " ", "-"))
			t.Setenv("TWS_ROOT", f.workspace)
			probe := filepath.Join(f.root, "case-probe")
			if err := os.Mkdir(probe, 0o755); err != nil {
				t.Fatal(err)
			}
			lower, err := os.Stat(probe)
			if err != nil {
				t.Fatal(err)
			}
			upper, err := os.Stat(filepath.Join(f.root, "CASE-PROBE"))
			if err != nil || !os.SameFile(lower, upper) {
				t.Skip("requires a case-insensitive filesystem")
			}

			const gitBranch = "old-entry-ref"
			worktree := f.addWorktree(t, f.repo, tc.oldName, gitBranch)
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: tc.oldName, Branch: gitBranch,
			}}}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "worktree", "--agent", "claude",
			}, nil); err != nil {
				t.Fatal(err)
			}
			stackBefore, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			ledgerBefore, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			linkPath := filepath.Join(worktree, "CLAUDE.local.md")
			linkBefore, err := os.Lstat(linkPath)
			if err != nil {
				t.Fatal(err)
			}
			headBefore := injectGit(t, f.repo, "rev-parse", gitBranch)
			if _, _, err := runManagedCobra(t, renameCmd(), []string{
				"branch", f.feature, tc.oldName, tc.newName,
			}, nil); err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("physical case alias was not refused before Git effects: %v", err)
			}
			if got := injectGit(t, f.repo, "rev-parse", gitBranch); got != headBefore {
				t.Fatalf("case alias changed original ref: got %s want %s", got, headBefore)
			}
			if _, err := os.Stat(worktree); err != nil {
				t.Fatalf("case alias removed original worktree: %v", err)
			}
			if linkAfter, err := os.Lstat(linkPath); err != nil || !os.SameFile(linkBefore, linkAfter) {
				t.Fatalf("case alias changed managed link identity: %v", err)
			}
			stackAfter, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil || string(stackAfter) != string(stackBefore) {
				t.Fatalf("case alias changed stack: %v", err)
			}
			ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(f.featurePath))
			if err != nil || string(ledgerAfter) != string(ledgerBefore) {
				t.Fatalf("case alias changed ledger: %v", err)
			}
			if residue, err := internal.ManagedTemplateRecoveryResidue(f.featurePath); err != nil || residue != "" {
				t.Fatalf("case alias refusal left recovery evidence: residue=%q err=%v", residue, err)
			}
		})
	}
}

func TestManagedTemplateBranchRenameRecomputesSlashDepthLinkTargets(t *testing.T) {
	f := newInjectFixture(t, "rename-slash-depth")
	t.Setenv("TWS_ROOT", f.workspace)
	oldName := "deep/entry"
	worktree := f.addWorktree(t, f.repo, oldName, "rename-slash-depth-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: oldName, Branch: "rename-slash-depth-entry",
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "worktree", "--agent", "claude",
	}, nil); err != nil {
		t.Fatal(err)
	}
	oldTarget, err := os.Readlink(filepath.Join(worktree, "CLAUDE.local.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{
		"branch", f.feature, oldName, "renamed",
	}, nil); err != nil {
		t.Fatalf("managed slash-depth rename failed: %v", err)
	}
	newPath := filepath.Join(f.featurePath, "worktrees", "renamed")
	newLink := filepath.Join(newPath, "CLAUDE.local.md")
	newTarget, err := os.Readlink(newLink)
	if err != nil {
		t.Fatal(err)
	}
	expectedTarget, err := filepath.Rel(
		filepath.Dir(newLink),
		filepath.Join(f.featurePath, "inject", "CLAUDE.local.md"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if newTarget != expectedTarget || newTarget == oldTarget {
		t.Fatalf("slash-depth rename retained wrong link target: old=%q new=%q want=%q",
			oldTarget, newTarget, expectedTarget)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "worktree",
	}, nil); err != nil {
		t.Fatalf("slash-depth renamed context failed ordinary sync: %v", err)
	}
}

func TestManagedTemplateArchiveResolvesAbsentSelectedFeatureButPreservesSelectedLegacy(t *testing.T) {
	setupManaged := func(t *testing.T, feature string) (*injectFixture, string) {
		t.Helper()
		f := newInjectFixture(t, feature)
		t.Setenv("TWS_ROOT", f.workspace)
		worktree := f.addWorktree(t, f.repo, "entry", feature+"-managed-entry")
		if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
			Name: "entry", Branch: feature + "-managed-entry",
		}}}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
			f.feature, "--scope", "worktree", "--agent", "claude",
		}, nil); err != nil {
			t.Fatal(err)
		}
		return f, worktree
	}

	t.Run("selected feature absent uses configured managed feature", func(t *testing.T) {
		f, managedWorktree := setupManaged(t, "archive-resolved-managed")
		selectedRoot := filepath.Join(f.root, "selected-empty")
		if err := internal.EnsureExternalWorkspaceMarker(selectedRoot); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TWS_ROOT", selectedRoot)
		if _, _, err := runManagedCobra(t, archiveCmd(), []string{f.feature, "entry"}, nil); err != nil {
			t.Fatalf("archive did not resolve configured managed feature: %v", err)
		}
		if _, err := os.Lstat(managedWorktree); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("archive left configured managed worktree: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(selectedRoot, f.feature)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("archive created or targeted absent selected feature: %v", err)
		}
	})

	t.Run("selected legacy feature wins over configured managed feature", func(t *testing.T) {
		f, managedWorktree := setupManaged(t, "archive-selected-legacy")
		selectedRoot := filepath.Join(f.root, "selected-legacy")
		if err := internal.EnsureExternalWorkspaceMarker(selectedRoot); err != nil {
			t.Fatal(err)
		}
		selectedFeature := filepath.Join(selectedRoot, f.feature)
		selectedWorktree := filepath.Join(selectedFeature, "worktrees", "entry")
		if err := os.MkdirAll(filepath.Dir(selectedWorktree), 0o755); err != nil {
			t.Fatal(err)
		}
		injectGit(t, f.repo, "branch", "archive-selected-legacy-branch", "main")
		injectGit(t, f.repo, "worktree", "add", selectedWorktree, "archive-selected-legacy-branch")
		if err := internal.SaveStack(selectedFeature, internal.Stack{Branches: []internal.StackEntry{{
			Name: "entry", Branch: "archive-selected-legacy-branch",
		}}}); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TWS_ROOT", selectedRoot)
		if _, _, err := runManagedCobra(t, archiveCmd(), []string{f.feature, "entry"}, nil); err != nil {
			t.Fatalf("archive changed selected legacy behavior: %v", err)
		}
		if _, err := os.Lstat(selectedWorktree); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("archive did not target selected legacy worktree: %v", err)
		}
		if _, err := os.Stat(managedWorktree); err != nil {
			t.Fatalf("archive retargeted configured managed feature: %v", err)
		}
		state, err := internal.LoadManagedTemplateState(f.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, link := range state.Roles[string(internal.ManagedTemplateRoleWorktree)].Links {
			found = found || link.Worktree == "entry"
		}
		if !found {
			t.Fatal("selected legacy archive retired configured managed link ownership")
		}
	})
}

func TestLegacyLifecycleIgnoresManagedStrictConfigAndSessionAdmission(t *testing.T) {
	writeMalformedRepoConfig := func(t *testing.T, repo string) {
		t.Helper()
		path := internal.RepoConfigPathFor(repo)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("agent_command: [broken\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, operation := range []string{"rename", "archive"} {
		t.Run("external malformed config "+operation, func(t *testing.T) {
			repo, _ := setupExternalFeatureWithBranch(t)
			writeMalformedRepoConfig(t, repo)
			var err error
			if operation == "rename" {
				err = renameBranchExternal("auth", "api", "renamed")
			} else {
				err = archiveExternal("auth", "api")
			}
			if err != nil {
				t.Fatalf("legacy external %s loaded strict managed config: %v", operation, err)
			}
		})

		t.Run("checkout malformed config "+operation, func(t *testing.T) {
			repo := setupGitRepoCheckout(t)
			ws := requireWorkspaceForTest(t, repo)
			if err := addCheckout(ws, "legacy", nil, "", "", false, false, false); err != nil {
				t.Fatal(err)
			}
			if err := createCheckoutBranch(ws, "legacy", "entry", "main", false); err != nil {
				t.Fatal(err)
			}
			writeMalformedRepoConfig(t, repo)
			var err error
			if operation == "rename" {
				err = renameBranchCheckout(ws, "legacy", "entry", "renamed")
			} else {
				err = archiveCheckout(ws, "legacy", "entry")
			}
			if err != nil {
				t.Fatalf("legacy checkout %s loaded strict managed config: %v", operation, err)
			}
		})
	}

	t.Run("legacy archive keeps tmux cleanup semantics", func(t *testing.T) {
		_, featurePath := setupExternalFeatureWithBranch(t)
		session := sanitizeSessionName("auth/api")
		oldProbe := managedTemplateTmuxProbe
		managedTemplateTmuxProbe = reparentTmuxProbe{snapshot: internal.TmuxSnapshot{
			Available: true,
			Sessions:  map[string]bool{session: true},
		}}
		t.Cleanup(func() { managedTemplateTmuxProbe = oldProbe })
		log := fakeTmuxOnPath(t)
		if err := archiveExternal("auth", "api"); err != nil {
			t.Fatalf("legacy archive inherited managed tmux admission: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(featurePath, "worktrees", "api")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy archive did not remove worktree: %v", err)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "kill-session -t "+session) {
			t.Fatalf("legacy archive did not preserve tmux cleanup behavior: %s", data)
		}
	})
}

func TestManagedTemplateArchivedEntryRestoresStoredRepoAndBranch(t *testing.T) {
	f := newInjectFixture(t, "archived-secondary")
	t.Setenv("TWS_ROOT", f.workspace)
	secondary := createInjectRepo(t, f.root, "secondary")
	const gitBranch = "secondary-kept"
	injectGit(t, f.repo, "branch", gitBranch, "main")
	injectGit(t, secondary, "branch", gitBranch, "main")
	primaryBefore := injectGit(t, f.repo, "rev-parse", gitBranch)
	secondaryBefore := injectGit(t, secondary, "rev-parse", gitBranch)
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: gitBranch, Repo: secondary, Archived: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.repo, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveConfigFile(internal.RepoConfigPathFor(f.repo), internal.Config{
		BranchPrefix: "changed-",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "worktree", "--agent", "claude",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, newCmd(), []string{f.feature, "entry"}, nil); err != nil {
		t.Fatalf("archived secondary entry was not restored from stored identity: %v", err)
	}
	worktree := filepath.Join(f.featurePath, "worktrees", "entry")
	gotRepo, err := internal.MainRepoRootIn(worktree)
	if err != nil {
		t.Fatal(err)
	}
	wantRepo, err := internal.MainRepoRootIn(secondary)
	if err != nil {
		t.Fatal(err)
	}
	if !sameResolvedRepoRoot(gotRepo, wantRepo) {
		t.Fatalf("archived entry materialized from primary repo: got %s want %s", gotRepo, wantRepo)
	}
	if got := injectGit(t, f.repo, "rev-parse", gitBranch); got != primaryBefore {
		t.Fatalf("primary same-named branch changed: got %s want %s", got, primaryBefore)
	}
	if got := injectGit(t, secondary, "rev-parse", gitBranch); got != secondaryBefore {
		t.Fatalf("secondary stored branch changed: got %s want %s", got, secondaryBefore)
	}
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	entry := internal.GetBranch(stack, "entry")
	if entry.Archived || entry.Branch != gitBranch || !sameResolvedRepoRoot(entry.Repo, secondary) {
		t.Fatalf("archived entry metadata was rewritten from current defaults: %+v", entry)
	}
	if internal.VerifyGitRef(f.repo, "changed-entry") == nil ||
		internal.VerifyGitRef(secondary, "changed-entry") == nil {
		t.Fatal("reactivation created a prefixed branch instead of restoring stored branch")
	}
	if _, _, err := runManagedCobra(t, archiveCmd(), []string{f.feature, "entry"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, newCmd(), []string{
		f.feature, "entry", "--repo", f.repo,
	}, nil); err == nil || !strings.Contains(err.Error(), "different repository") {
		t.Fatalf("conflicting explicit repo was accepted for archived entry: %v", err)
	}
}

func TestManagedTemplateInjectHelpExplainsRootOverride(t *testing.T) {
	cmd := injectCmd()
	if !strings.Contains(cmd.Long, "--into . overrides") {
		t.Fatalf("inject long help omits managed root override precedence: %q", cmd.Long)
	}
	flag := cmd.Flag("into")
	if flag == nil || !strings.Contains(flag.Usage, "--into . overrides") {
		t.Fatalf("inject --into help omits managed root override precedence: %+v", flag)
	}
}

func TestManagedTemplateCompletionUsesResolvedOwningContext(t *testing.T) {
	fx := newManagedSecondaryFixture(t, "primary-feature",
		internal.Config{AgentCommand: "claude"},
		internal.Config{AgentCommand: "copilot"},
	)
	secondaryWS, err := internal.ResolveCurrentWorkspaceE(
		fx.secondary, internal.LoadConfigForRepo(fx.secondary),
	)
	if err != nil {
		t.Fatal(err)
	}
	secondaryFeature := secondaryWS.FeaturePath("secondary-feature")
	if err := os.MkdirAll(secondaryFeature, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(secondaryFeature, internal.Stack{Branches: []internal.StackEntry{{
		Name: "secondary-branch", Branch: "secondary-branch",
	}}}); err != nil {
		t.Fatal(err)
	}
	for name, repo := range map[string]string{
		"primary-template":   fx.inject.repo,
		"secondary-template": fx.secondary,
	} {
		source := managedCLIPackage(t, filepath.Join(fx.inject.root, name), map[string]string{
			"inject/context.md": name + "\n",
		}, "")
		if err := internal.AddNamedTemplate(repo, internal.TemplateScopeRepo, name, source); err != nil {
			t.Fatal(err)
		}
	}

	for name, cmd := range map[string]*cobra.Command{
		"template sync": templateSyncCmd(),
		"inject":        injectCmd(),
		"new":           newCmd(),
	} {
		features, directive := cmd.ValidArgsFunction(cmd, nil, "")
		if directive != cobra.ShellCompDirectiveNoFileComp ||
			!managedTestContains(features, fx.inject.feature) ||
			managedTestContains(features, "secondary-feature") {
			t.Fatalf("%s feature completion used secondary cwd: %v directive=%v", name, features, directive)
		}
	}

	for name, cmd := range map[string]*cobra.Command{
		"inject":        injectCmd(),
		"new":           newCmd(),
		"archive":       archiveCmd(),
		"rename branch": renameBranchCmd(),
	} {
		branches, directive := cmd.ValidArgsFunction(cmd, []string{fx.inject.feature}, "")
		if directive != cobra.ShellCompDirectiveNoFileComp ||
			!managedTestContains(branches, "entry") ||
			managedTestContains(branches, "secondary-branch") {
			t.Fatalf("%s branch completion used secondary cwd: %v directive=%v", name, branches, directive)
		}
	}
	for name, cmd := range map[string]*cobra.Command{
		"add":  addCmd(),
		"sync": templateSyncCmd(),
	} {
		fn, ok := cmd.GetFlagCompletionFunc("template")
		if !ok || fn == nil {
			t.Fatalf("%s --template has no named/directory completion", name)
		}
		values, directive := fn(cmd, nil, "")
		if directive == cobra.ShellCompDirectiveNoFileComp ||
			!managedTestContains(values, "repo:primary-template") ||
			managedTestContains(values, "repo:secondary-template") {
			t.Fatalf("%s --template completion is incoherent: values=%v directive=%v", name, values, directive)
		}
		seen := make(map[string]bool)
		for _, value := range values {
			if seen[value] {
				t.Fatalf("%s --template completion repeated %q", name, value)
			}
			seen[value] = true
		}
	}
}

func TestManagedTemplateUnresolvedWorkspaceFallbackFailsClosed(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "managed"
		if pending {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			f := newInjectFixture(t, "outside-"+name)
			t.Setenv("TWS_ROOT", f.workspace)
			oldTmux := managedTemplateTmuxProbe
			managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
			t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
				f.feature, "--scope", "orchestrator", "--agent", "claude",
			}, nil); err != nil {
				t.Fatal(err)
			}
			if pending {
				recovery := filepath.Join(f.featurePath, ".tws-template-recovery", "pending")
				if err := os.MkdirAll(recovery, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(internal.ManagedTemplateStatePath(f.featurePath),
					filepath.Join(recovery, "old-ledger")); err != nil {
					t.Fatal(err)
				}
			}
			outside := t.TempDir()
			if err := os.Chdir(outside); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{f.feature}, {"--all"}} {
				if _, _, err := runManagedCobra(t, templateSyncCmd(), args, nil); err == nil {
					t.Fatalf("unresolved %s sync %v exited successfully", name, args)
				}
			}
		})
	}

	t.Run("proven legacy keeps compatibility result", func(t *testing.T) {
		f := newInjectFixture(t, "outside-legacy")
		t.Setenv("TWS_ROOT", f.workspace)
		if err := os.Chdir(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature}, nil); err != nil {
			t.Fatalf("proven legacy fallback changed compatibility result: %v", err)
		}
	})
}

func TestManagedTemplateConsentBindsExactFeatureRootIdentity(t *testing.T) {
	f := newManagedCLIFixture(t)
	f.replaceGlobalTemplate(t, "root-consent", map[string]string{
		"inject/keep.md":     "keep\n",
		"inject/obsolete.md": "obsolete\n",
	})
	if _, _, err := runManagedCobra(t, addCmd(), []string{
		"root-consent", "--template", "global:root-consent", "--agent", "claude",
	}, nil); err != nil {
		t.Fatal(err)
	}
	featurePath := f.ws.FeaturePath("root-consent")
	f.replaceGlobalTemplate(t, "root-consent", map[string]string{
		"inject/keep.md": "keep\n",
	})
	before := snapshotTree(t, featurePath)
	original := featurePath + "-original"
	managedTemplatePostConsentHook = func() error {
		if err := os.Rename(featurePath, original); err != nil {
			return err
		}
		copyManagedTreeForTest(t, original, featurePath)
		return nil
	}
	_, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		"root-consent", "--allow-managed-removal",
	}, nil)
	managedTemplatePostConsentHook = nil
	if err == nil || !strings.Contains(err.Error(), "root identity") {
		t.Fatalf("removal consent accepted a byte-identical replacement root: %v", err)
	}
	if got := snapshotTree(t, original); got != before {
		t.Fatal("original consented root changed after identity drift")
	}
	if got := snapshotTree(t, featurePath); got != before {
		t.Fatal("replacement root changed after identity drift refusal")
	}
}

func TestManagedTemplateBranchlessOwnerBindingRefusesUnrelatedRepo(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(gitConfig, []byte(
		"[user]\n\tname = Owner Test\n\temail = owner@example.test\n\tuseConfigOnly = true\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	repoA := createInjectRepo(t, root, "owner-a")
	repoB := createInjectRepo(t, root, "owner-b")
	workspace := filepath.Join(root, "custom-unmapped-root")
	t.Setenv("TWS_ROOT", workspace)
	t.Chdir(repoA)
	if _, _, err := runManagedCobra(t, addCmd(), []string{
		"branchless", "--agent", "claude",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "branchless", "worktrees")); err != nil {
		t.Fatalf("branchless managed feature was not created: %v", err)
	}
	if err := os.Chdir(repoB); err != nil {
		t.Fatal(err)
	}
	beforeB := injectGit(t, repoB, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads")
	if _, _, err := runManagedCobra(t, newCmd(), []string{
		"branchless", "owner-probe", "--base", "main",
	}, nil); err == nil || !strings.Contains(err.Error(), "belongs to repository") {
		t.Fatalf("unrelated repository was accepted as managed owner: %v", err)
	}
	afterB := injectGit(t, repoB, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads")
	if afterB != beforeB || internal.VerifyGitRef(repoB, "owner-probe") == nil {
		t.Fatal("unrelated repository refs changed despite owner refusal")
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"branchless"}, nil); err == nil ||
		!strings.Contains(err.Error(), "belongs to repository") {
		t.Fatalf("unrelated repository template sync was accepted: %v", err)
	}
	if _, _, err := runManagedCobra(t, configShowCmd(), nil, nil); err == nil ||
		!strings.Contains(err.Error(), "belongs to repository") {
		t.Fatalf("unrelated repository config command was accepted: %v", err)
	}
	if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{
		Workspaces: map[string]string{repoB: workspace},
	}); err != nil {
		t.Fatal(err)
	}
	bConfigPath := internal.RepoConfigPathFor(repoB)
	if _, _, err := runManagedCobra(t, configSetCmd(), []string{
		"worktree_template", "repo:mapped-owner", "--repo",
	}, nil); err == nil || !strings.Contains(err.Error(), "belongs to repository") {
		t.Fatalf("mapped unrelated repository config mutation was accepted: %v", err)
	}
	if _, err := os.Lstat(bConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mapped owner conflict created unrelated repository config: %v", err)
	}
	registrySource := managedCLIPackage(t, filepath.Join(root, "mapped-registry-source"), map[string]string{
		"inject/context.md": "mapped\n",
	}, "")
	if err := internal.AddNamedTemplate(
		repoB, internal.TemplateScopeRepo, "mapped-keep", registrySource,
	); err != nil {
		t.Fatal(err)
	}
	bRegistry, err := internal.RepoTemplateRegistryRoot(repoB)
	if err != nil {
		t.Fatal(err)
	}
	registryBefore := snapshotTree(t, bRegistry)
	if _, _, err := runManagedCobra(t, templateRegistryAddCmd(), []string{
		"mapped-add", registrySource, "--repo",
	}, nil); err == nil || !strings.Contains(err.Error(), "belongs to repository") {
		t.Fatalf("mapped unrelated repository template add was accepted: %v", err)
	}
	if _, _, err := runManagedCobra(t, templateRegistryRemoveCmd(), []string{
		"mapped-keep", "--repo",
	}, nil); err == nil || !strings.Contains(err.Error(), "belongs to repository") {
		t.Fatalf("mapped unrelated repository template remove was accepted: %v", err)
	}
	if got := snapshotTree(t, bRegistry); got != registryBefore {
		t.Fatal("mapped owner conflict changed unrelated repository registry")
	}
	if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	movedRepoA := repoA + "-moved"
	if err := os.Rename(repoA, movedRepoA); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(movedRepoA); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"branchless"}, nil); err != nil {
		t.Fatalf("relocated owning repository could not refresh branchless feature: %v", err)
	}
	state, err := internal.LoadManagedTemplateState(filepath.Join(workspace, "branchless"))
	if err != nil {
		t.Fatal(err)
	}
	if !sameResolvedRepoRoot(state.Owner.RepoRoot, movedRepoA) {
		t.Fatalf("relocated owning repository path was not refreshed: %+v", state.Owner)
	}
	if _, _, err := runManagedCobra(t, newCmd(), []string{
		"branchless", "owner-probe", "--base", "main",
	}, nil); err != nil {
		t.Fatalf("original owning repository could not operate branchless feature: %v", err)
	}
	if internal.VerifyGitRef(movedRepoA, "owner-probe") != nil {
		t.Fatal("original owning repository did not receive branch")
	}
}

func TestManagedTemplateOwnerFingerprintRefreshesForSameCommonDir(t *testing.T) {
	for _, change := range []string{"remote add", "remote set-url"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			gitConfig := filepath.Join(root, "gitconfig")
			if err := os.WriteFile(gitConfig, []byte(
				"[user]\n\tname = Owner Remote Test\n\temail = owner-remote@example.test\n\tuseConfigOnly = true\n",
			), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_COUNT", "0")

			repo := filepath.Join(root, "repo")
			if change == "remote add" {
				gitInDirCreate(t, repo)
			} else {
				repo = createInjectRepo(t, root, "repo")
			}
			workspace := filepath.Join(root, "custom-workspace")
			t.Setenv("TWS_ROOT", workspace)
			t.Chdir(repo)
			if _, _, err := runManagedCobra(t, addCmd(), []string{
				"owner-remote", "--agent", "claude",
			}, nil); err != nil {
				t.Fatal(err)
			}
			featurePath := filepath.Join(workspace, "owner-remote")
			before, err := internal.LoadManagedTemplateState(featurePath)
			if err != nil {
				t.Fatal(err)
			}

			remote := filepath.Join(root, "updated-origin.git")
			injectGit(t, "", "init", "--bare", remote)
			if change == "remote add" {
				injectGit(t, repo, "remote", "add", "origin", remote)
			} else {
				injectGit(t, repo, "remote", "set-url", "origin", remote)
			}
			injectGit(t, repo, "push", "-u", "origin", "main")
			injectGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
			if got := internal.ComputeGitIdentity(repo); got == before.Owner.GitIdentity {
				t.Fatal("remote change did not alter auxiliary owner fingerprint")
			}

			if _, _, err := runManagedCobra(t, configSetCmd(), []string{
				"test_command", "true", "--repo",
			}, nil); err != nil {
				t.Fatalf("same local owner could not update repo config after %s: %v", change, err)
			}
			source := managedCLIPackage(t, filepath.Join(root, "owner-registry-source"), map[string]string{
				"inject/context.md": "owner\n",
			}, "")
			if _, _, err := runManagedCobra(t, templateRegistryAddCmd(), []string{
				"owner-registry", source, "--repo",
			}, nil); err != nil {
				t.Fatalf("same local owner could not add repo template after %s: %v", change, err)
			}
			if _, _, err := runManagedCobra(t, templateRegistryRemoveCmd(), []string{
				"owner-registry", "--repo",
			}, nil); err != nil {
				t.Fatalf("same local owner could not remove repo template after %s: %v", change, err)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"owner-remote"}, nil); err != nil {
				t.Fatalf("same local owner could not refresh feature after %s: %v", change, err)
			}
			after, err := internal.LoadManagedTemplateState(featurePath)
			if err != nil {
				t.Fatal(err)
			}
			if after.Owner.GitDirDevice != before.Owner.GitDirDevice ||
				after.Owner.GitDirInode != before.Owner.GitDirInode {
				t.Fatal("same local owner refresh changed exact common-directory identity")
			}
			if after.Owner.GitIdentity != internal.ComputeGitIdentity(repo) {
				t.Fatalf("auxiliary owner fingerprint was not refreshed: %+v", after.Owner)
			}
			if _, _, err := runManagedCobra(t, newCmd(), []string{
				"owner-remote", "owner-branch", "--base", "main",
			}, nil); err != nil {
				t.Fatalf("same local owner could not create branch after %s: %v", change, err)
			}
			if internal.VerifyGitRef(repo, "owner-branch") != nil {
				t.Fatal("same local owner operation did not create branch")
			}

			if change == "remote set-url" {
				clone := filepath.Join(root, "same-origin-clone")
				injectGit(t, "", "clone", remote, clone)
				injectGit(t, clone, "remote", "set-url", "origin",
					injectGit(t, repo, "remote", "get-url", "origin"))
				if got := internal.ComputeGitIdentity(clone); got != after.Owner.GitIdentity {
					t.Fatalf("same-origin clone fixture has different auxiliary fingerprint: got %s want %s",
						got, after.Owner.GitIdentity)
				}
				ledgerBefore, err := os.ReadFile(internal.ManagedTemplateStatePath(featurePath))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chdir(clone); err != nil {
					t.Fatal(err)
				}
				if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"owner-remote"}, nil); err == nil ||
					!strings.Contains(err.Error(), "belongs to repository") {
					t.Fatalf("same-origin clone was accepted as local owner: %v", err)
				}
				ledgerAfter, err := os.ReadFile(internal.ManagedTemplateStatePath(featurePath))
				if err != nil || string(ledgerAfter) != string(ledgerBefore) {
					t.Fatalf("same-origin clone changed owner state: %v", err)
				}
			}
		})
	}
}

func TestManagedTemplateOwnerScanFailsClosedOnPendingOrCorruptEvidence(t *testing.T) {
	for _, evidence := range []string{"pending recovery", "empty recovery", "malformed ledger", "unreadable ledger"} {
		t.Run(evidence, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			gitConfig := filepath.Join(root, "gitconfig")
			if err := os.WriteFile(gitConfig, []byte(
				"[user]\n\tname = Owner Evidence Test\n\temail = owner-evidence@example.test\n\tuseConfigOnly = true\n",
			), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_COUNT", "0")

			repoA := createInjectRepo(t, root, "owner-a")
			repoB := createInjectRepo(t, root, "owner-b")
			workspace := filepath.Join(root, "custom-workspace")
			t.Setenv("TWS_ROOT", workspace)
			t.Chdir(repoA)
			if _, _, err := runManagedCobra(t, addCmd(), []string{
				"owner-evidence", "--agent", "claude",
			}, nil); err != nil {
				t.Fatal(err)
			}
			if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{
				Workspaces: map[string]string{repoB: workspace},
			}); err != nil {
				t.Fatal(err)
			}

			source := managedCLIPackage(t, filepath.Join(root, "registry-source"), map[string]string{
				"inject/context.md": "registry\n",
			}, "")
			if err := internal.AddNamedTemplate(
				repoB, internal.TemplateScopeRepo, "keep", source,
			); err != nil {
				t.Fatal(err)
			}
			registryRoot, err := internal.RepoTemplateRegistryRoot(repoB)
			if err != nil {
				t.Fatal(err)
			}
			registryBefore := snapshotTree(t, registryRoot)
			configPath := internal.RepoConfigPathFor(repoB)
			featurePath := filepath.Join(workspace, "owner-evidence")
			ledgerPath := internal.ManagedTemplateStatePath(featurePath)
			ledgerBytes, err := os.ReadFile(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}

			switch evidence {
			case "pending recovery":
				recovery := filepath.Join(featurePath, ".tws-template-recovery", "interrupted")
				if err := os.MkdirAll(recovery, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(ledgerPath, filepath.Join(recovery, "old-ledger")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(recovery, "pending.json"), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty recovery":
				if err := os.MkdirAll(filepath.Join(featurePath, ".tws-template-recovery"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "malformed ledger":
				if err := os.WriteFile(ledgerPath, []byte("{broken\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unreadable ledger":
				if err := os.Chmod(ledgerPath, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(ledgerPath, 0o600) })
			}
			var featureBefore string
			if evidence != "unreadable ledger" {
				featureBefore = snapshotTree(t, featurePath)
			}
			t.Chdir(repoB)

			assertRefused := func(label string, err error) {
				t.Helper()
				if err == nil {
					t.Fatalf("%s accepted unverifiable managed owner evidence", label)
				}
			}
			_, _, err = runManagedCobra(t, configSetCmd(), []string{
				"test_command", "true", "--repo",
			}, nil)
			assertRefused("config set --repo", err)
			_, _, err = runManagedCobra(t, templateRegistryAddCmd(), []string{
				"new-entry", source, "--repo",
			}, nil)
			assertRefused("template add --repo", err)
			_, _, err = runManagedCobra(t, templateRegistryRemoveCmd(), []string{
				"keep", "--repo",
			}, nil)
			assertRefused("template remove --repo", err)

			if _, err := os.Lstat(configPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owner inference refusal created unrelated repo config: %v", err)
			}
			if got := snapshotTree(t, registryRoot); got != registryBefore {
				t.Fatal("owner inference refusal changed unrelated repo registry")
			}
			if evidence != "unreadable ledger" {
				if got := snapshotTree(t, featurePath); got != featureBefore {
					t.Fatal("owner inference refusal changed managed evidence")
				}
			} else {
				info, err := os.Stat(ledgerPath)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0 {
					t.Fatalf("owner inference refusal changed unreadable ledger mode: %v", info.Mode())
				}
				if err := os.Chmod(ledgerPath, 0o600); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(ledgerPath)
				if err != nil || string(got) != string(ledgerBytes) {
					t.Fatalf("owner inference refusal changed unreadable ledger: %v", err)
				}
			}
		})
	}
}
