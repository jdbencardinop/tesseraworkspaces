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
	"github.com/spf13/cobra"
)

func TestFeatureRenameManagedV1V2RecoveryAndSubsequentLifecycle(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		for _, recovery := range []string{"fresh", "continue", "abort"} {
			t.Run(version+"/"+recovery, func(t *testing.T) {
				noTmux := false
				fx := newManagedSecondaryFixture(t, "rename-owned",
					internal.Config{AgentCommand: "claude", UseTmux: &noTmux},
					internal.Config{AgentCommand: "copilot", UseTmux: &noTmux, InjectInto: ".other"},
				)
				f := fx.inject
				t.Chdir(fx.observer)
				installManagedLaunchFakes(t)
				if _, _, err := runManagedCobra(t, newCmd(), []string{
					f.feature, "nested/secondary", "--base", "main", "--repo", fx.secondary,
				}, nil); err != nil {
					t.Fatal(err)
				}
				if version == "v2" {
					namedConflictWrite(t, filepath.Join(f.featurePath, "inject", "CLAUDE.local.md"), "kept worktree guidance\n")
					namedConflictWrite(t, filepath.Join(f.featurePath, "CLAUDE.md"), "kept orchestrator guidance\n")
					fakeNamedConflictTTY(t)
					if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{
						f.feature, "--resolve-conflicts",
					}, strings.NewReader("keep\nkeep\n")); err != nil {
						t.Fatal(err)
					}
					managedTemplateInputIsTTY = managedTemplateTerminalInput
				}
				ledger := namedConflictRead(t, internal.ManagedTemplateStatePath(f.featurePath))
				var beforeState internal.ManagedTemplateState
				if err := json.Unmarshal([]byte(ledger), &beforeState); err != nil {
					t.Fatal(err)
				}
				source := namedConflictRead(t, filepath.Join(f.featurePath, "inject", "CLAUDE.local.md"))
				oldLink := filepath.Join(f.featurePath, "worktrees", "nested", "secondary", "CLAUDE.local.md")
				linkInfo, err := os.Lstat(oldLink)
				if err != nil {
					t.Fatal(err)
				}
				linkTarget, err := os.Readlink(oldLink)
				if err != nil {
					t.Fatal(err)
				}
				if recovery != "fresh" {
					internal.FeatureRenameCheckpoint = func(step, _ string) error {
						if step == "after-publish.0" {
							return internal.ErrFeatureRenameInterrupted
						}
						return nil
					}
					t.Cleanup(func() { internal.FeatureRenameCheckpoint = nil })
				}
				out, _, err := runManagedCobra(t, renameCmd(), []string{"feature", f.feature, "renamed"}, nil)
				if recovery != "fresh" {
					if !errors.Is(err, internal.ErrFeatureRenameInterrupted) || strings.Contains(out, "Renamed feature:") {
						t.Fatalf("interruption reported success: %q %v", out, err)
					}
					internal.FeatureRenameCheckpoint = nil
					out, _, err = runManagedCobra(t, renameCmd(), []string{
						"feature", f.feature, "renamed", "--" + recovery,
					}, nil)
				}
				if err != nil {
					t.Fatalf("rename/recovery: %s %v", out, err)
				}
				name := "renamed"
				if recovery == "abort" {
					name = f.feature
				}
				path := filepath.Join(f.workspace, name)
				if got := namedConflictRead(t, internal.ManagedTemplateStatePath(path)); got != ledger {
					t.Fatal("rename/recovery rewrote the v1/v2 ledger")
				}
				link := filepath.Join(path, "worktrees", "nested", "secondary", "CLAUDE.local.md")
				afterInfo, err := os.Lstat(link)
				if err != nil || !os.SameFile(linkInfo, afterInfo) {
					t.Fatalf("managed link identity changed: %v", err)
				}
				afterTarget, err := os.Readlink(link)
				if err != nil || linkTarget != afterTarget {
					t.Fatalf("managed link mapping changed: %q %v", afterTarget, err)
				}
				for _, test := range []struct {
					cmd  *cobra.Command
					args []string
				}{
					{injectCmd(), []string{name, "--into", "."}},
					{openCmd(), []string{name, "nested/secondary", "--no-agent"}},
					{archiveCmd(), []string{name, "nested/secondary"}},
					{newCmd(), []string{name, "nested/secondary"}},
					{injectCmd(), []string{name}},
				} {
					if _, _, err := runManagedCobra(t, test.cmd, test.args, nil); err != nil {
						t.Fatalf("post-rename %s %v requires manual Git repair: %v", test.cmd.Name(), test.args, err)
					}
				}
				if got := namedConflictRead(t, filepath.Join(path, "inject", "CLAUDE.local.md")); got != source {
					t.Fatal("non-rendering lifecycle changed effective guidance")
				}
				state, err := internal.LoadManagedTemplateState(path)
				if err != nil {
					t.Fatal(err)
				}
				if version == "v2" && state.Version != 2 {
					t.Fatal("lifecycle discarded v2 decisions")
				}
				for role, owned := range state.Roles {
					beforeFiles, err := json.Marshal(beforeState.Roles[role].Files)
					if err != nil {
						t.Fatal(err)
					}
					afterFiles, err := json.Marshal(owned.Files)
					if err != nil || !bytes.Equal(beforeFiles, afterFiles) {
						t.Fatalf("non-rendering lifecycle changed remembered decisions: %s %v", role, err)
					}
				}
				beforeSync := namedConflictRead(t, internal.ManagedTemplateStatePath(path))
				_, _, err = runManagedCobra(t, templateSyncCmd(), []string{name}, nil)
				if version == "v2" && recovery != "abort" {
					if err == nil {
						t.Fatal("renamed native guidance proposal silently reused old consent")
					}
					if namedConflictRead(t, internal.ManagedTemplateStatePath(path)) != beforeSync ||
						namedConflictRead(t, filepath.Join(path, "inject", "CLAUDE.local.md")) != source {
						t.Fatal("refused proposal changed ownership or effective content")
					}
				} else if err != nil {
					t.Fatalf("ordinary template sync after rename: %v", err)
				}
			})
		}
	}
}

func TestFeatureRenameCLIPendingAdmissionAndRecoveryFlags(t *testing.T) {
	f := newInjectFixture(t, "old")
	t.Setenv("TWS_ROOT", f.workspace)
	t.Chdir(f.repo)
	f.addWorktree(t, f.repo, "entry", "git-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "entry", Branch: "git-entry", Base: "main"}}}); err != nil {
		t.Fatal(err)
	}
	oldProbe := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = oldProbe; internal.FeatureRenameCheckpoint = nil })
	for _, flags := range [][]string{{"--continue"}, {"--abort"}, {"--continue", "--abort"}} {
		args := append([]string{"feature", "old", "new"}, flags...)
		if _, _, err := runManagedCobra(t, renameCmd(), args, nil); err == nil {
			t.Fatalf("invalid/no-journal recovery accepted: %v", flags)
		}
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"branch", "old", "entry", "other", "--continue"}, nil); err == nil {
		t.Fatal("branch rename acquired feature recovery flags")
	}
	internal.FeatureRenameCheckpoint = func(step, _ string) error {
		if step == "after-move" {
			return internal.ErrFeatureRenameInterrupted
		}
		return nil
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new"}, nil); !errors.Is(err, internal.ErrFeatureRenameInterrupted) {
		t.Fatal(err)
	}
	internal.FeatureRenameCheckpoint = nil
	for _, name := range []string{"old", "new"} {
		for _, test := range []struct {
			cmd  *cobra.Command
			args []string
		}{
			{addCmd(), []string{name}},
			{newCmd(), []string{name, "racing-entry", "--base", "main"}},
			{openCmd(), []string{name, "entry", "--no-agent"}},
			{openCmd(), []string{name, "--feature-dir", "--no-agent"}},
			{injectCmd(), []string{name}},
			{templateSyncCmd(), []string{name}},
			{renameCmd(), []string{"feature", name, "third"}},
			{renameCmd(), []string{"branch", name, "entry", "other"}},
			{archiveCmd(), []string{name, "entry"}},
			{deleteCmd(), []string{name}},
			{syncCmd(), []string{name, "--no-fetch"}},
			{syncCmd(), []string{name, "--continue"}},
			{pushCmd(), []string{name}},
			{hooksInstallCmd(), []string{name}},
			{hooksRemoveCmd(), []string{name}},
		} {
			if _, _, err := runManagedCobra(t, test.cmd, test.args, nil); err == nil {
				t.Fatalf("pending name admitted: %s %v", test.cmd.Name(), test.args)
			}
		}
	}
	if _, _, err := runManagedCobra(t, addCmd(), []string{"unrelated"}, nil); err != nil {
		t.Fatalf("pending rename blocked unrelated feature: %v", err)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new", "--continue"}, nil); err != nil {
		t.Fatalf("recovery could not find journal without old directory: %v", err)
	}
	if _, err := internal.ResolveInjectWorktree(filepath.Join(f.workspace, "new"), "entry"); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameCLIHoldsAdmissionAcrossNormalCommandPreflight(t *testing.T) {
	f := newInjectFixture(t, "old")
	t.Setenv("TWS_ROOT", f.workspace)
	t.Chdir(f.repo)
	previous := creationResolvedHook
	t.Cleanup(func() { creationResolvedHook = previous })
	called := false
	creationResolvedHook = func(_, _, _ string) error {
		called = true
		if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new"}, nil); err == nil {
			t.Fatal("rename crossed an admitted new command's preflight/effect boundary")
		}
		return nil
	}
	if _, _, err := runManagedCobra(t, newCmd(), []string{"old", "entry", "--base", "main"}, nil); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("creation checkpoint not reached")
	}
	if _, err := internal.ResolveInjectWorktree(f.featurePath, "entry"); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameCheckoutFlagsRefuseWithoutMutation(t *testing.T) {
	f := newManagedCLIFixture(t)
	if _, _, err := runManagedCobra(t, addCmd(), []string{"old"}, nil); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, f.ws.MetadataRoot)
	for _, flag := range []string{"--continue", "--abort"} {
		if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new", flag}, nil); err == nil || !strings.Contains(err.Error(), "external-only") {
			t.Fatalf("checkout recovery accepted: %v", err)
		}
	}
	if snapshotTree(t, f.ws.MetadataRoot) != before {
		t.Fatal("checkout refusal mutated metadata")
	}
}

func TestFeatureRenamePreservesUnacknowledgedManagedEdits(t *testing.T) {
	fx := newManagedSecondaryFixture(t, "unacknowledged-rename", internal.Config{AgentCommand: "claude"}, internal.Config{AgentCommand: "copilot"})
	f := fx.inject
	t.Chdir(f.repo)
	namedConflictWrite(t, filepath.Join(f.featurePath, "inject", "CLAUDE.local.md"), "not acknowledged\n")
	ledger := namedConflictRead(t, internal.ManagedTemplateStatePath(f.featurePath))
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", f.feature, "renamed"}, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.workspace, "renamed")
	if namedConflictRead(t, internal.ManagedTemplateStatePath(path)) != ledger ||
		namedConflictRead(t, filepath.Join(path, "inject", "CLAUDE.local.md")) != "not acknowledged\n" {
		t.Fatal("rename altered or acknowledged the user's edit")
	}
	if _, _, err := runManagedCobra(t, injectCmd(), []string{"renamed"}, nil); err == nil {
		t.Fatal("rename silently acknowledged a managed edit")
	}
}

type featureRenameUncertainTmux struct{}

func (featureRenameUncertainTmux) Snapshot() internal.TmuxSnapshot {
	return internal.TmuxSnapshot{Err: errors.New("tmux inventory unavailable")}
}

func TestFeatureRenameSessionExclusionAndPublicRecordErrors(t *testing.T) {
	for _, kind := range []string{"direct", "invalid-direct", "launch-intent", "uncertain-tmux"} {
		t.Run(kind, func(t *testing.T) {
			f := newInjectFixture(t, "session-rename")
			t.Setenv("TWS_ROOT", f.workspace)
			t.Chdir(f.repo)
			f.addWorktree(t, f.repo, "entry", "session-entry")
			oldProbe := managedTemplateTmuxProbe
			managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
			t.Cleanup(func() { managedTemplateTmuxProbe = oldProbe })
			token := ""
			switch kind {
			case "direct", "invalid-direct":
				token = seedRecord(t, f.featurePath, f.feature, "entry", os.Getpid())
				if kind == "invalid-direct" {
					path := filepath.Join(internal.DirectSessionsDir(f.featurePath), internal.DirectSessionBranchID(f.feature, "entry"), token+".json")
					namedConflictWrite(t, path, `{"schema_version":999,"secret":"must-not-print"}`)
				}
			case "launch-intent":
				if _, err := internal.CreateExternalSessionIntent(f.featurePath, f.feature, []string{"entry"}, false); err != nil {
					t.Fatal(err)
				}
			case "uncertain-tmux":
				managedTemplateTmuxProbe = featureRenameUncertainTmux{}
			}
			before := snapshotRecordTree(t, f.featurePath)
			var publicError error
			if token != "" {
				physical, err := filepath.EvalSymlinks(f.featurePath)
				if err != nil {
					t.Fatal(err)
				}
				targets, err := directRecordTargetsForFeature(physical)
				if err != nil {
					t.Fatal(err)
				}
				publicError = guardDirectRecords(&bytes.Buffer{}, physical, "rename feature", f.feature, targets)
			}
			_, _, err := runManagedCobra(t, renameCmd(), []string{"feature", f.feature, "new"}, nil)
			if err == nil {
				t.Fatal("live or unverifiable session was relocated")
			}
			if publicError != nil && !strings.Contains(err.Error(), publicError.Error()) {
				t.Fatalf("public direct-session refusal changed: %v; want %v", err, publicError)
			}
			if strings.Contains(err.Error(), "must-not-print") || token != "" && strings.Contains(err.Error(), token) {
				t.Fatal("rename leaked private direct-session record content or owner token")
			}
			if snapshotRecordTree(t, f.featurePath) != before {
				t.Fatal("session refusal changed feature content")
			}
		})
	}
}

func TestFeatureRenameDocsAndHelp(t *testing.T) {
	var out bytes.Buffer
	command := renameCmd()
	command.SetOut(&out)
	command.SetArgs([]string{"feature", "--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"--continue", "--abort", "same old/new names", "older tws", "dirty work", "bound journal"} {
		if !strings.Contains(out.String(), phrase) {
			t.Errorf("feature rename help is missing %q", phrase)
		}
	}
	for _, path := range []string{
		"README.md", "docs/cheatsheet.md", "docs/configuration.md", "CHANGELOG.md",
		"assets/skills/claude/tesseraworkspaces/SKILL.md",
		"assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md",
		"assets/skills/copilot/tws.prompt.md",
	} {
		content := normalizeProse(readRepoDoc(t, path))
		for _, phrase := range []string{"released in v1.2.18-rc.7", "--continue", "--abort", "future", "older tws", "separate effects", "old-path registrations"} {
			if !strings.Contains(content, phrase) {
				t.Errorf("%s must explain feature rename %q", path, phrase)
			}
		}
	}
}
