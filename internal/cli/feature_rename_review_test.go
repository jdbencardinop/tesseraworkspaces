package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func featureRenameReviewCLI(t *testing.T) *injectFixture {
	t.Helper()
	f := newInjectFixture(t, "old")
	t.Chdir(f.repo)
	t.Setenv("TWS_ROOT", f.workspace)
	f.addWorktree(t, f.repo, "entry", "configured-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "configured-entry", Base: "main",
	}}}); err != nil {
		t.Fatal(err)
	}
	oldProbe := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() {
		managedTemplateTmuxProbe = oldProbe
		internal.FeatureRenameCheckpoint = nil
	})
	return f
}

func featureRenameReviewSnapshot(t *testing.T, root string) string {
	t.Helper()
	var snapshot strings.Builder
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
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
		fmt.Fprintf(&snapshot, "%s %v %x\n", path, info.Mode(), sha256.Sum256(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot.String()
}

func TestFeatureRenameReviewInterruptedRetryDoesNotFallback(t *testing.T) {
	for _, recovery := range []string{"continue", "abort"} {
		t.Run(recovery, func(t *testing.T) {
			f := featureRenameReviewCLI(t)
			selected := filepath.Join(f.root, "selected-workspace")
			t.Setenv("TWS_ROOT", selected)
			// A unique seed avoids add's established existence-based fallback
			// into the configured workspace that already owns "old".
			for _, command := range []struct {
				cmd  *cobra.Command
				args []string
			}{
				{addCmd(), []string{"selected-seed"}},
				{renameCmd(), []string{"feature", "selected-seed", "old"}},
				{newCmd(), []string{"old", "selected-entry", "--base", "main"}},
			} {
				if _, _, err := runManagedCobra(t, command.cmd, command.args, nil); err != nil {
					t.Fatalf("dual-root setup %v: %v", command.args, err)
				}
			}
			before := snapshotTree(t, f.featurePath)
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
			out, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new"}, nil)
			if err == nil || !strings.Contains(err.Error(), "pending") || strings.Contains(out, "Renamed feature:") {
				t.Fatalf("fresh retry escaped pending execution root: %q %v", out, err)
			}
			if snapshotTree(t, f.featurePath) != before {
				t.Fatal("fresh retry renamed or changed the configured fallback feature")
			}
			if _, err := os.Lstat(filepath.Join(f.workspace, "new")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fallback destination was created: %v", err)
			}
			beforeFiles := featureRenameReviewSnapshot(t, f.workspace)
			beforeRefs := syncSnapshotRefs(t, f.repo)
			remote := filepath.Join(f.root, "primary.git")
			beforeRemote := syncSnapshotRefs(t, remote)
			for _, command := range []struct {
				cmd  *cobra.Command
				args []string
			}{
				{pushCmd(), []string{"old", "--dry-run"}},
				{pushCmd(), []string{"old"}},
				{pushCmd(), []string{"old", "--only", "entry", "--dry-run"}},
				{pushCmd(), []string{"old", "--only", "entry"}},
				{pushCmd(), []string{"old", "--continue"}},
				{syncCmd(), []string{"old", "--no-fetch", "--local-only"}},
				{syncCmd(), []string{"old", "--plan", "--no-fetch"}},
			} {
				if _, _, err := runManagedCobra(t, command.cmd, command.args, nil); err == nil || !strings.Contains(err.Error(), "pending") {
					t.Fatalf("%s %v escaped pending execution root: %v", command.cmd.Name(), command.args, err)
				}
				if featureRenameReviewSnapshot(t, f.workspace) != beforeFiles ||
					syncSnapshotRefs(t, f.repo) != beforeRefs || syncSnapshotRefs(t, remote) != beforeRemote {
					t.Fatalf("%s changed fallback contents, local refs or local remote", command.cmd.Name())
				}
			}
			if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new", "--" + recovery}, nil); err != nil {
				t.Fatal(err)
			}
			final := "new"
			if recovery == "abort" {
				final = "old"
			}
			if _, err := internal.ResolveInjectWorktree(filepath.Join(selected, final), "selected-entry"); err != nil {
				t.Fatal(err)
			}
			if snapshotTree(t, f.featurePath) != before {
				t.Fatal("bound recovery changed the fallback workspace")
			}
		})
	}
}

func TestFeatureRenameReviewFallbackAdmissionRechecksSelectedRoot(t *testing.T) {
	f := featureRenameReviewCLI(t)
	selected := filepath.Join(f.root, "selected-empty")
	if err := os.Mkdir(selected, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_ROOT", selected)
	before := snapshotTree(t, f.featurePath)
	interleaved := false
	internal.FeatureRenameCheckpoint = func(step, path string) error {
		if interleaved || step != "before-lock-open" || filepath.Base(path) != "new" {
			return nil
		}
		interleaved = true
		if err := os.Mkdir(filepath.Join(selected, "old"), 0o755); err != nil {
			return err
		}
		// The selected workspace gains an interrupted rename after ordinary
		// existence resolution selected the configured fallback, but before
		// that fallback can acquire permission for its first effect.
		internal.FeatureRenameCheckpoint = func(step, _ string) error {
			if step == "after-move" {
				return internal.ErrFeatureRenameInterrupted
			}
			return nil
		}
		_, err := internal.RenameExternalFeature(internal.Workspace{
			Mode: internal.ModeExternal, RepoRoot: f.repo, MetadataRoot: selected,
		}, "old", "new", "", nil)
		internal.FeatureRenameCheckpoint = nil
		if !errors.Is(err, internal.ErrFeatureRenameInterrupted) {
			t.Fatalf("interleaved selected rename: %v", err)
		}
		return nil
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new"}, nil); err == nil || !interleaved {
		t.Fatalf("fallback raced a new selected-root journal: interleaved=%v err=%v", interleaved, err)
	}
	internal.FeatureRenameCheckpoint = nil
	if snapshotTree(t, f.featurePath) != before {
		t.Fatal("fallback proceeded after the selected-root journal appeared")
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new", "--abort"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameReviewFallbackRoutesRetainBothAdmissions(t *testing.T) {
	for _, route := range []string{"sync", "push"} {
		t.Run(route, func(t *testing.T) {
			f := featureRenameReviewCLI(t)
			selected := filepath.Join(f.root, "selected-root")
			if err := os.MkdirAll(filepath.Join(selected, "old"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TWS_ROOT", selected)
			beforeA, beforeB := featureRenameReviewSnapshot(t, f.workspace), featureRenameReviewSnapshot(t, selected)
			for _, command := range []struct {
				cmd  *cobra.Command
				args []string
			}{
				{pushCmd(), []string{"old", "--dry-run"}},
				{syncCmd(), []string{"old", "--plan", "--no-fetch"}},
				{stackReparentCmd(), []string{"old", "entry", "--onto", "main", "--plan", "--no-fetch"}},
			} {
				if _, _, err := runManagedCobra(t, command.cmd, command.args, nil); err != nil {
					t.Fatalf("healthy fallback preview %s: %v", command.cmd.Name(), err)
				}
				if featureRenameReviewSnapshot(t, f.workspace) != beforeA || featureRenameReviewSnapshot(t, selected) != beforeB {
					t.Fatalf("%s preview created namespace state", command.cmd.Name())
				}
			}
			stop := errors.New("stop under both namespace admissions")
			called := false
			check := func() error {
				called = true
				for _, root := range []string{selected, f.workspace} {
					_, err := internal.RenameExternalFeature(internal.Workspace{
						Mode: internal.ModeExternal, RepoRoot: f.repo, MetadataRoot: root,
					}, "old", "must-not-move", "", nil)
					if err == nil || !strings.Contains(err.Error(), "feature rename admission is busy") {
						t.Fatalf("%s released rename admission for %s before mutation: %v", route, root, err)
					}
				}
				return stop
			}
			oldSync, oldPush := ExternalSyncMutationGuardHook, TopLevelPushEntryBarrier
			t.Cleanup(func() {
				ExternalSyncMutationGuardHook, TopLevelPushEntryBarrier = oldSync, oldPush
			})
			var command *cobra.Command
			args := []string{"old"}
			if route == "sync" {
				command = syncCmd()
				args = append(args, "--no-fetch", "--local-only")
				ExternalSyncMutationGuardHook = func(string) error { return check() }
			} else {
				command = pushCmd()
				TopLevelPushEntryBarrier = func(int, internal.StackEntry) error { return check() }
			}
			if _, _, err := runManagedCobra(t, command, args, nil); !errors.Is(err, stop) || !called {
				t.Fatalf("fallback mutation admission seam: called=%v err=%v", called, err)
			}
		})
	}
}

func TestFeatureRenameReviewFallbackRoutesRecheckSelectedRoot(t *testing.T) {
	for _, route := range []string{"sync", "push"} {
		t.Run(route, func(t *testing.T) {
			f := featureRenameReviewCLI(t)
			selected := filepath.Join(f.root, "selected-root")
			if err := os.MkdirAll(filepath.Join(selected, "old"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TWS_ROOT", selected)
			before := featureRenameReviewSnapshot(t, f.workspace)
			beforeRefs := syncSnapshotRefs(t, f.repo)
			remote := filepath.Join(f.root, "primary.git")
			beforeRemote := syncSnapshotRefs(t, remote)
			interleaved := false
			internal.FeatureRenameCheckpoint = func(step, path string) error {
				if interleaved || step != "before-lock-open" || filepath.Base(path) != "old" {
					return nil
				}
				interleaved = true
				internal.FeatureRenameCheckpoint = func(step, _ string) error {
					if step == "after-move" {
						return internal.ErrFeatureRenameInterrupted
					}
					return nil
				}
				_, err := internal.RenameExternalFeature(internal.Workspace{
					Mode: internal.ModeExternal, RepoRoot: f.repo, MetadataRoot: selected,
				}, "old", "new", "", nil)
				internal.FeatureRenameCheckpoint = nil
				if !errors.Is(err, internal.ErrFeatureRenameInterrupted) {
					t.Fatalf("interleaved selected-root rename: %v", err)
				}
				return nil
			}
			command, args := pushCmd(), []string{"old"}
			if route == "sync" {
				command, args = syncCmd(), []string{"old", "--no-fetch", "--local-only"}
			}
			if _, _, err := runManagedCobra(t, command, args, nil); err == nil || !interleaved || !strings.Contains(err.Error(), "pending") {
				t.Fatalf("fallback raced selected-root recovery: interleaved=%v err=%v", interleaved, err)
			}
			internal.FeatureRenameCheckpoint = nil
			if featureRenameReviewSnapshot(t, f.workspace) != before ||
				syncSnapshotRefs(t, f.repo) != beforeRefs || syncSnapshotRefs(t, remote) != beforeRemote {
				t.Fatal("late selected-root journal allowed fallback mutation")
			}
			if _, err := internal.RenameExternalFeature(internal.Workspace{
				Mode: internal.ModeExternal, RepoRoot: f.repo, MetadataRoot: selected,
			}, "old", "new", "abort", nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFeatureRenameReviewDecisionsUseResolvedRoot(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent-selected-root", true: "existing-selected-root"}[exists], func(t *testing.T) {
			f := featureRenameReviewCLI(t)
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
			selected := filepath.Join(f.root, "other-workspace")
			if exists {
				if err := os.Mkdir(selected, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TWS_ROOT", selected)
			path := filepath.Join(f.workspace, "new")
			before := snapshotTree(t, path)
			for _, test := range []struct {
				cmd  *cobra.Command
				args []string
			}{
				{decideCmd(), []string{"new", "must not write", "--branch", "entry"}},
				{decisionsAckCmd(), []string{"new"}},
				{hooksInstallCmd(), []string{"new"}},
				{hooksRemoveCmd(), []string{"new"}},
				{exportCmd(), []string{"new", "-o", filepath.Join(path, "must-not-exist.yaml")}},
			} {
				if _, _, err := runManagedCobra(t, test.cmd, test.args, nil); err == nil || !strings.Contains(err.Error(), "pending") {
					t.Fatalf("%s missed actual-root pending rename: %v", test.cmd.Name(), err)
				}
			}
			if snapshotTree(t, path) != before {
				t.Fatal("metadata/context mutator wrote into the pending configured root")
			}
			t.Setenv("TWS_ROOT", f.workspace)
			if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new", "--continue"}, nil); err != nil {
				t.Fatal(err)
			}
			// An independently pending rename in B must not become an
			// unrelated blocker for RequireFeaturePath's configured A.
			if err := os.MkdirAll(filepath.Join(selected, "old"), 0o755); err != nil {
				t.Fatal(err)
			}
			internal.FeatureRenameCheckpoint = func(step, _ string) error {
				if step == "after-move" {
					return internal.ErrFeatureRenameInterrupted
				}
				return nil
			}
			other := internal.Workspace{Mode: internal.ModeExternal, RepoRoot: f.repo, MetadataRoot: selected}
			if _, err := internal.RenameExternalFeature(other, "old", "new", "", nil); !errors.Is(err, internal.ErrFeatureRenameInterrupted) {
				t.Fatal(err)
			}
			internal.FeatureRenameCheckpoint = nil
			t.Setenv("TWS_ROOT", selected)
			otherBefore := snapshotTree(t, filepath.Join(selected, "new"))
			if _, _, err := runManagedCobra(t, decideCmd(), []string{"new", "record in configured root", "--branch", "entry"}, nil); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runManagedCobra(t, decisionsAckCmd(), []string{"new"}, nil); err != nil {
				t.Fatal(err)
			}
			if snapshotTree(t, filepath.Join(selected, "new")) != otherBefore {
				t.Fatal("normal decision route mutated the override root")
			}
			decisions, err := internal.LoadDecisions(path)
			if err != nil || len(decisions.Entries) != 1 || decisions.Entries[0].Summary != "record in configured root" {
				t.Fatalf("decision did not reach its configured root: %+v %v", decisions, err)
			}
		})
	}
}

func TestFeatureRenameReviewInitAliasPhysicalContext(t *testing.T) {
	f := featureRenameReviewCLI(t)
	if err := os.Mkdir(filepath.Join(f.featurePath, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	namedConflictWrite(t, filepath.Join(f.featurePath, ".github", "copilot-instructions.md"), "user asset\n")
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
	alias := filepath.Join(f.root, "workspace-alias")
	if err := os.Symlink(f.workspace, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_ROOT", alias)
	path, err := filepath.EvalSymlinks(filepath.Join(f.workspace, "new"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(path)
	asset := filepath.Join(path, ".github", "copilot-instructions.md")
	if _, _, err := runManagedCobra(t, initCmd(), []string{"--agent", "copilot", "--force"}, nil); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("physical cwd bypassed the selected workspace alias: %v", err)
	}
	if namedConflictRead(t, asset) != "user asset\n" {
		t.Fatal("pending init overwrote a user asset")
	}
	t.Chdir(f.repo)
	if _, _, err := runManagedCobra(t, initCmd(), []string{"--agent", "copilot", "--force"}, nil); err != nil {
		t.Fatalf("outside-workspace init was blocked: %v", err)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new", "--continue"}, nil); err != nil {
		t.Fatal(err)
	}
	t.Chdir(path)
	if _, _, err := runManagedCobra(t, initCmd(), []string{"--agent", "copilot", "--force"}, nil); err != nil {
		t.Fatalf("healthy equivalent alias init failed: %v", err)
	}
	if namedConflictRead(t, asset) == "user asset\n" {
		t.Fatal("healthy init did not perform its explicitly requested overwrite")
	}
}

func TestFeatureRenameReviewInitContainingWorkspaceIgnoresUnrelatedOverride(t *testing.T) {
	for _, override := range []string{"absent", "existing", "unset", "alias"} {
		for _, pending := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pending=%v", override, pending), func(t *testing.T) {
				f := featureRenameReviewCLI(t)
				if err := os.Mkdir(filepath.Join(f.featurePath, ".github"), 0o755); err != nil {
					t.Fatal(err)
				}
				namedConflictWrite(t, filepath.Join(f.featurePath, ".github", "copilot-instructions.md"), "retain exact source instructions\n")
				path := f.featurePath
				if pending {
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
					path = filepath.Join(f.workspace, "new")
				}
				selected := filepath.Join(f.root, "unrelated-root")
				switch override {
				case "existing":
					if err := os.Mkdir(selected, 0o755); err != nil {
						t.Fatal(err)
					}
				case "unset":
					selected = ""
				case "alias":
					if err := os.Symlink(f.workspace, selected); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("TWS_ROOT", selected)
				before := featureRenameReviewSnapshot(t, f.workspace)
				t.Chdir(f.repo)
				if _, _, err := runManagedCobra(t, initCmd(), []string{"--agent", "copilot", "--force"}, nil); err != nil {
					t.Fatalf("genuine outside-workspace init failed: %v", err)
				}
				if featureRenameReviewSnapshot(t, f.workspace) != before {
					t.Fatal("outside-workspace init created admission state or changed feature contents")
				}
				physical, err := filepath.EvalSymlinks(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Chdir(physical)
				_, _, err = runManagedCobra(t, initCmd(), []string{"--agent", "copilot", "--force"}, nil)
				asset := namedConflictRead(t, filepath.Join(physical, ".github", "copilot-instructions.md"))
				if pending {
					if err == nil || !strings.Contains(err.Error(), "pending") || asset != "retain exact source instructions\n" {
						t.Fatalf("init bypassed the containing workspace: err=%v assetChanged=%v", err, asset != "retain exact source instructions\n")
					}
				} else if err != nil || asset == "retain exact source instructions\n" {
					t.Fatalf("healthy context init failed: err=%v assetChanged=%v", err, asset != "retain exact source instructions\n")
				}
				switch override {
				case "absent":
					if _, err := os.Lstat(selected); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("init created namespace state in unrelated absent override: %v", err)
					}
				case "existing":
					if entries, err := os.ReadDir(selected); err != nil || len(entries) != 0 {
						t.Fatalf("init changed unrelated override: %v %v", entries, err)
					}
				}
			})
		}
	}
}

func TestFeatureRenameReviewInitContextReplacementBeforeEffects(t *testing.T) {
	f := featureRenameReviewCLI(t)
	if err := os.Mkdir(filepath.Join(f.featurePath, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	namedConflictWrite(t, filepath.Join(f.featurePath, ".github", "copilot-instructions.md"), "retain original context\n")
	t.Setenv("TWS_ROOT", filepath.Join(f.root, "absent-override"))
	t.Chdir(f.featurePath)
	retired := filepath.Join(f.workspace, "retired-context")
	switched := false
	internal.FeatureRenameCheckpoint = func(step, path string) error {
		if switched || step != "before-lock-open" || filepath.Base(path) != "old" {
			return nil
		}
		switched = true
		if err := os.Rename(f.featurePath, retired); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(f.featurePath, ".github"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(f.featurePath, ".github", "copilot-instructions.md"), []byte("foreign context\n"), 0o644)
	}
	if _, _, err := runManagedCobra(t, initCmd(), []string{"--agent", "copilot", "--force"}, nil); err == nil || !switched {
		t.Fatalf("context directory replacement was admitted: switched=%v err=%v", switched, err)
	}
	internal.FeatureRenameCheckpoint = nil
	if namedConflictRead(t, filepath.Join(retired, ".github", "copilot-instructions.md")) != "retain original context\n" ||
		namedConflictRead(t, filepath.Join(f.featurePath, ".github", "copilot-instructions.md")) != "foreign context\n" {
		t.Fatal("replaced context command wrote to an unreserved binding")
	}
}

func TestFeatureRenameReviewInitAliasRetargetBeforeEffects(t *testing.T) {
	f := featureRenameReviewCLI(t)
	if err := os.Mkdir(filepath.Join(f.featurePath, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(f.featurePath, ".github", "copilot-instructions.md")
	namedConflictWrite(t, asset, "retain context asset\n")
	alias := filepath.Join(f.root, "workspace-alias")
	foreign := filepath.Join(f.root, "foreign")
	if err := os.Symlink(f.workspace, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_ROOT", alias)
	t.Chdir(f.featurePath)
	changed := false
	internal.FeatureRenameCheckpoint = func(step, path string) error {
		if changed || step != "before-lock-open" || filepath.Base(path) != "old" {
			return nil
		}
		changed = true
		if err := os.Rename(alias, alias+".original"); err != nil {
			return err
		}
		return os.Symlink(foreign, alias)
	}
	if _, _, err := runManagedCobra(t, initCmd(), []string{"--agent", "copilot", "--force"}, nil); err == nil || !changed {
		t.Fatalf("context alias drift was accepted: changed=%v err=%v", changed, err)
	}
	internal.FeatureRenameCheckpoint = nil
	if namedConflictRead(t, asset) != "retain context asset\n" {
		t.Fatal("drifted context command changed the original asset")
	}
	if entries, err := os.ReadDir(foreign); err != nil || len(entries) != 0 {
		t.Fatalf("drifted context command wrote into the foreign root: %v %v", entries, err)
	}
}
