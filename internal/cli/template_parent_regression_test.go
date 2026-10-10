package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func TestManagedTemplateParentExplicitDirectoryOverridesDefaults(t *testing.T) {
	f := newManagedCLIFixture(t)
	f.replaceGlobalTemplate(t, "default", map[string]string{"inject/default.md": "default\n"})
	if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{
		WorktreeTemplate: "global:default",
	}); err != nil {
		t.Fatal(err)
	}
	legacy := managedCLIPackage(t, filepath.Join(f.root, "legacy-template"), map[string]string{
		"legacy.md": "legacy\n",
	}, "")
	if _, _, err := runManagedCobra(t, addCmd(), []string{"explicit-legacy", "--template", legacy}, nil); err != nil {
		t.Fatalf("explicit directory input must retain the legacy route despite managed defaults: %v", err)
	}
	featurePath := f.ws.FeaturePath("explicit-legacy")
	if present, err := internal.ManagedTemplateStatePresent(featurePath); err != nil || present {
		t.Fatalf("directory input opted into managed mode: present=%v err=%v", present, err)
	}
	if data, err := os.ReadFile(filepath.Join(featurePath, "inject", "legacy.md")); err != nil || string(data) != "legacy\n" {
		t.Fatalf("legacy payload was not copied: %q %v", data, err)
	}
}

func TestManagedTemplateParentSyncRejectsMixedDirectoryAndRoleFlags(t *testing.T) {
	for _, flags := range [][]string{
		{"--agent", "copilot"},
		{"--scope", "worktree"},
		{"--worktree-template", "global:roles"},
	} {
		t.Run(flags[0], func(t *testing.T) {
			f := newManagedCLIFixture(t)
			if err := addCheckout(f.ws, "legacy", nil, "", "", false, false, false); err != nil {
				t.Fatal(err)
			}
			f.replaceGlobalTemplate(t, "roles", map[string]string{"inject/role.md": "role\n"})
			legacy := managedCLIPackage(t, filepath.Join(f.root, "legacy-template"), map[string]string{
				"new.md": "must not be copied\n",
			}, "")
			before := snapshotTree(t, f.ws.FeaturePath("legacy"))
			args := append([]string{"legacy", "--template", legacy}, flags...)
			if _, _, err := runManagedCobra(t, templateSyncCmd(), args, nil); err == nil {
				t.Error("sync accepted mixed directory and managed selectors")
			}
			if after := snapshotTree(t, f.ws.FeaturePath("legacy")); after != before {
				t.Error("invalid mixed selectors changed feature assets")
			}
		})
	}
}

func TestManagedTemplateParentExplicitRenderingSelectionPersists(t *testing.T) {
	for _, command := range []string{"claude", "custom-agent-wrapper --profile copilot"} {
		t.Run(command, func(t *testing.T) {
			f := newManagedCLIFixture(t)
			if err := internal.SaveConfigFile(internal.ConfigPath(), internal.Config{AgentCommand: command}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, addCmd(), []string{"explicit-agent", "--agent", "copilot"}, nil); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"explicit-agent"}, nil); err != nil {
				t.Errorf("unchanged launch configuration invalidated the saved explicit renderer: %v", err)
			}
			if _, err := internal.PlanCheckoutSessionLinks(f.ws, "explicit-agent", ""); err != nil {
				t.Errorf("saved explicit renderer cannot be used by checkout sessions: %v", err)
			}
		})
	}
}

func TestManagedTemplateParentExternalCreationHonorsRootOverride(t *testing.T) {
	f := newInjectFixture(t, "seed")
	oldTmux := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })
	selectedRoot := os.Getenv("TWS_ROOT")
	source := managedCLIPackage(t, filepath.Join(f.root, "managed-source"), map[string]string{
		"inject/context.md": "context\n",
	}, "")
	if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "roles", source); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, addCmd(), []string{"legacy-root"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(selectedRoot, "legacy-root", "FEATURE.md")); err != nil {
		t.Fatalf("legacy root control failed: %v", err)
	}
	if _, _, err := runManagedCobra(t, addCmd(), []string{"managed-root", "--template", "global:roles"}, nil); err != nil {
		t.Fatal(err)
	}
	featurePath := filepath.Join(selectedRoot, "managed-root")
	if _, err := os.Stat(internal.ManagedTemplateStatePath(featurePath)); err != nil {
		t.Fatalf("named template selection changed add's explicit TWS_ROOT contract: %v", err)
	}
	if _, _, err := runManagedCobra(t, newCmd(), []string{"managed-root", "entry", "--base", "main"}, nil); err != nil {
		t.Fatalf("new did not follow the selected managed feature root: %v", err)
	}
	if _, err := os.Readlink(filepath.Join(featurePath, "worktrees", "entry", "context.md")); err != nil {
		t.Fatalf("managed links did not follow the selected feature root: %v", err)
	}
}

func TestManagedTemplateParentNoopOpenDoesNotBlockExistingSession(t *testing.T) {
	f := newInjectFixture(t, "reattach")
	t.Setenv("TWS_ROOT", f.workspace)
	worktree := f.addWorktree(t, f.repo, "entry", "reattach-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "reattach-entry",
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
	oldTmux := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = reparentTmuxProbe{snapshot: internal.TmuxSnapshot{
		Available: true, ServerRunning: true,
		Sessions: map[string]bool{internal.ExternalTmuxSessionName(f.feature, "entry"): true},
	}}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })
	before := snapshotTree(t, f.featurePath)
	if _, _, err := runManagedCobra(t, openCmd(), []string{f.feature, "entry", "--no-agent"}, nil); err != nil {
		t.Fatalf("no-op materialization blocks opening an already managed worktree: %v", err)
	}
	if after := snapshotTree(t, f.featurePath); after != before {
		t.Fatal("no-op open changed managed assets or ownership")
	}
}

func TestManagedTemplateParentOrphanSyncStateBlocksWriter(t *testing.T) {
	f := newInjectFixture(t, "sync-evidence")
	oldTmux := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldTmux })
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--scope", "orchestrator", "--agent", "claude"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.featurePath, ".sync-state.v2.yaml"), []byte("state_version: 999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, f.featurePath)
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
		f.feature, "--scope", "orchestrator", "--agent", "copilot", "--allow-managed-removal",
	}, nil); err == nil {
		t.Error("managed writer bypassed existing unclassifiable sync evidence because its lock was absent")
	}
	if after := snapshotTree(t, f.featurePath); after != before {
		t.Error("orphan-sync refusal did not preserve feature assets and recovery evidence")
	}
}
