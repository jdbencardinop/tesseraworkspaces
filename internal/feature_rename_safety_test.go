package internal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeatureRenameUnsupportedInventoryRefusesBeforeMove(t *testing.T) {
	tests := []struct {
		name string
		edit func(*testing.T, featureRenameFixture, string, string)
		want string
	}{
		{"unlisted", func(t *testing.T, f featureRenameFixture, _, _ string) {
			if err := SaveStack(f.old, Stack{}); err != nil {
				t.Fatal(err)
			}
		}, "unlisted"},
		{"archived-materialized", func(t *testing.T, f featureRenameFixture, _, _ string) {
			if err := SaveStack(f.old, Stack{Branches: []StackEntry{{Name: "entry", Branch: "git-entry", Archived: true}}}); err != nil {
				t.Fatal(err)
			}
		}, "archived-but-materialized"},
		{"missing-registered", func(t *testing.T, f featureRenameFixture, path, _ string) {
			if err := os.Rename(path, filepath.Join(f.root, "retained-worktree")); err != nil {
				t.Fatal(err)
			}
		}, "stale"},
		{"copied-marker", func(t *testing.T, f featureRenameFixture, path, _ string) {
			file := featureRenameTestImage(t, filepath.Join(path, ".git"))
			featureRenameTestWrite(t, filepath.Join(f.old, "worktrees", "copy", ".git"), file.Data, 0o644)
		}, "backpointer"},
		{"nested-primary", func(t *testing.T, f featureRenameFixture, _, _ string) {
			path := filepath.Join(f.old, "notes", "nested")
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			featureRenameTestGit(t, path, "init")
		}, "unsupported"},
		{"nested-bare", func(t *testing.T, f featureRenameFixture, _, _ string) {
			featureRenameTestGit(t, f.old, "init", "--bare", "bare.git")
		}, "inside moving feature"},
		{"populated-submodule", func(t *testing.T, f featureRenameFixture, path, _ string) {
			featureRenameTestGit(t, path, "-c", "protocol.file.allow=always", "submodule", "add", f.repo, "sub")
		}, "submodule"},
		{"path-config", func(t *testing.T, f featureRenameFixture, _, _ string) {
			featureRenameTestGit(t, f.repo, "config", "core.hooksPath", "local-hooks")
		}, "path-bearing"},
		{"index-lock", func(t *testing.T, _ featureRenameFixture, _, admin string) {
			featureRenameTestWrite(t, filepath.Join(admin, "index.lock"), []byte("owner\n"), 0o600)
		}, "lock"},
		{"git-operation", func(t *testing.T, _ featureRenameFixture, _, admin string) {
			featureRenameTestWrite(t, filepath.Join(admin, "MERGE_HEAD"), []byte("pending\n"), 0o600)
		}, "operation"},
		{"worktree-lock", func(t *testing.T, f featureRenameFixture, path, _ string) {
			featureRenameTestGit(t, f.repo, "worktree", "lock", path)
		}, "lock"},
		{"forward-symlink", func(t *testing.T, _ featureRenameFixture, path, _ string) {
			marker := filepath.Join(path, ".git")
			if err := os.Rename(marker, marker+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(".git.original", marker); err != nil {
				t.Fatal(err)
			}
		}, "symlink"},
		{"selected-path-symlink", func(t *testing.T, f featureRenameFixture, path, _ string) {
			if err := SaveStack(f.old, Stack{Branches: []StackEntry{{Name: "entry", Branch: "git-entry"}}}); err != nil {
				t.Fatal(err)
			}
			retained := filepath.Join(f.root, "retained-worktree")
			if err := os.Rename(path, retained); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(retained, path); err != nil {
				t.Fatal(err)
			}
		}, "symlink"},
		{"broken-backpointer", func(t *testing.T, _ featureRenameFixture, _, admin string) {
			featureRenameTestWrite(t, filepath.Join(admin, "gitdir"), []byte("/not/this/worktree/.git\n"), 0o644)
		}, "backpointer"},
		{"unknown-stack", func(t *testing.T, f featureRenameFixture, _, _ string) {
			featureRenameTestWrite(t, StackPath(f.old), []byte("branches: [invalid yaml\n"), 0o644)
		}, "stack"},
		{"template-recovery", func(t *testing.T, f featureRenameFixture, _, _ string) {
			if err := os.MkdirAll(filepath.Join(f.old, managedTemplateRecoveryName), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "recovery"},
		{"sync-recovery", func(t *testing.T, f featureRenameFixture, _, _ string) {
			featureRenameTestWrite(t, SyncStatePath(f.old), []byte("unknown\n"), 0o600)
		}, "recovery"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			path := f.worktree(t, f.repo, "entry", "git-entry", false)
			admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			test.edit(t, f, path, admin)
			before, err := featureRenameDirectoryAt(f.old)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q refusal, got %v", test.want, err)
			}
			if err := before.verify(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(f.new); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsupported layout was moved: %v", err)
			}
			if entries, err := os.ReadDir(featureRenameJournalRoot(f.ws.MetadataRoot)); err == nil && len(entries) != 0 {
				t.Fatalf("unsupported layout acquired a recovery journal: %v", entries)
			}
		})
	}
}

func TestFeatureRenameEmptyArchivedAndLegacy(t *testing.T) {
	for _, layout := range []string{"empty", "archived", "legacy"} {
		t.Run(layout, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			switch layout {
			case "archived":
				if err := SaveStack(f.old, Stack{Branches: []StackEntry{{Name: "old/entry", Branch: "unmaterialized", Archived: true}}}); err != nil {
					t.Fatal(err)
				}
			case "legacy":
				f.worktree(t, f.repo, "nested/entry", "git-legacy", true)
			}
			featureRenameTestWrite(t, filepath.Join(f.old, "nested", "user"), []byte("user data\n"), 0o640)
			file := featureRenameTestImage(t, filepath.Join(f.old, "nested", "user"))
			if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err != nil {
				t.Fatal(err)
			}
			if err := file.verifyAt(filepath.Join(f.new, "nested", "user")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(SpacesFilePath(f.ws.MetadataRoot)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rename created a registry: %v", err)
			}
		})
	}
}

func TestFeatureRenameRelativeExecutionRoot(t *testing.T) {
	f := newFeatureRenameFixture(t)
	f.worktree(t, f.repo, "entry", "git-entry", false)
	t.Chdir(f.root)
	f.ws.MetadataRoot = "selected-spaces"
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInjectWorktree(f.new, "entry"); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameLateEmptyDestinationIsNeverReplaced(t *testing.T) {
	f := newFeatureRenameFixture(t)
	path := f.worktree(t, f.repo, "entry", "git-entry", false)
	marker := featureRenameTestImage(t, filepath.Join(path, ".git"))
	var occupant featureRenameDirectory
	FeatureRenameCheckpoint = func(step, destination string) error {
		if step == "before-move-publication" {
			if err := os.Mkdir(destination, 0o755); err != nil {
				return err
			}
			var err error
			occupant, err = featureRenameDirectoryAt(destination)
			return err
		}
		return nil
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil {
		t.Fatal("late empty directory was replaced")
	}
	FeatureRenameCheckpoint = nil
	if err := occupant.verify(); err != nil {
		t.Fatal(err)
	}
	if err := marker.verifyAt(marker.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.new, filepath.Join(f.root, "retained-occupant")); err != nil {
		t.Fatal(err)
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "continue", nil); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameForeignEditsRefuseUnsafeRollbackAndRecovery(t *testing.T) {
	for _, kind := range []string{"old-occupant", "new-replacement", "admin-replacement", "root-replacement", "pointer-replacement", "registry-edit", "registry-appearance", "journal-residue"} {
		t.Run(kind, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			path := f.worktree(t, f.repo, "entry", "git-entry", false)
			if kind != "registry-appearance" {
				f.registry(t)
			}
			admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			foreignPath := ""
			var foreign []byte
			FeatureRenameCheckpoint = func(step, _ string) error {
				if step != "after-move" {
					return nil
				}
				switch kind {
				case "old-occupant":
					foreignPath = filepath.Join(f.old, "foreign")
				case "new-replacement":
					if err := os.Rename(f.new, filepath.Join(f.root, "retained-original-feature")); err != nil {
						return err
					}
					foreignPath = filepath.Join(f.new, "foreign")
				case "admin-replacement":
					if err := os.Rename(admin, admin+".retained"); err != nil {
						return err
					}
					foreignPath = filepath.Join(admin, "foreign")
				case "root-replacement":
					if err := os.Rename(f.ws.MetadataRoot, f.ws.MetadataRoot+".retained"); err != nil {
						return err
					}
					foreignPath = filepath.Join(f.ws.MetadataRoot, "foreign")
				case "pointer-replacement":
					foreignPath = filepath.Join(admin, "gitdir")
					before := featureRenameTestImage(t, foreignPath)
					if err := os.Rename(foreignPath, foreignPath+".retained"); err != nil {
						return err
					}
					foreign = before.Data
				case "registry-edit", "registry-appearance":
					foreignPath = SpacesFilePath(f.ws.MetadataRoot)
					foreign = []byte("version: 1\nspaces: []\n# concurrent edit\n")
				case "journal-residue":
					plans, err := featureRenamePendingPlans(f.ws.MetadataRoot)
					if err != nil || len(plans) != 1 {
						return fmt.Errorf("finding operation: %v", err)
					}
					foreignPath = filepath.Join(plans[0].Operation.Path, "foreign.txt")
				}
				if len(foreign) == 0 {
					foreign = []byte("foreign data must survive\n")
				}
				featureRenameTestWrite(t, foreignPath, foreign, 0o600)
				return errors.New("stop after foreign edit")
			}
			if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil {
				t.Fatal("unknown state reported success")
			}
			FeatureRenameCheckpoint = nil
			for _, action := range []string{"continue", "abort"} {
				if _, err := RenameExternalFeature(f.ws, "old", "new", action, nil); err == nil {
					t.Fatalf("%s adopted unknown state", action)
				}
				got, err := os.ReadFile(foreignPath)
				if err != nil || !bytes.Equal(got, foreign) {
					t.Fatalf("foreign data changed: %q %v", got, err)
				}
			}
		})
	}
}

func TestFeatureRenameRecoveryNamesRepositoryAndJournalValidation(t *testing.T) {
	f := newFeatureRenameFixture(t)
	f.worktree(t, f.repo, "entry", "git-entry", false)
	for _, action := range []string{"continue", "abort"} {
		if _, err := RenameExternalFeature(f.ws, "old", "new", action, nil); err == nil {
			t.Fatal("no-journal recovery succeeded")
		}
	}
	FeatureRenameCheckpoint = func(step, _ string) error {
		if step == "after-move" {
			return ErrFeatureRenameInterrupted
		}
		return nil
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); !errors.Is(err, ErrFeatureRenameInterrupted) {
		t.Fatal(err)
	}
	FeatureRenameCheckpoint = nil
	for _, names := range [][2]string{{"old", "other"}, {"new", "old"}, {"elsewhere", "new"}} {
		if _, err := RenameExternalFeature(f.ws, names[0], names[1], "continue", nil); err == nil {
			t.Fatalf("wrong names %v accepted", names)
		}
	}
	other := f.ws
	other.RepoRoot = featureRenameTestRepository(t, f.root, "unrelated")
	if _, err := RenameExternalFeature(other, "old", "new", "continue", nil); err == nil || !strings.Contains(err.Error(), "controlling repository") {
		t.Fatalf("wrong controlling repository accepted: %v", err)
	}
	plans, err := featureRenamePendingPlans(f.ws.MetadataRoot)
	if err != nil || len(plans) != 1 {
		t.Fatalf("pending: %v %v", plans, err)
	}
	journal := filepath.Join(plans[0].Operation.Path, "journal.json")
	before := featureRenameTestImage(t, journal)
	for _, mutated := range [][]byte{
		bytes.Replace(before.Data, []byte(`"version": 1`), []byte(`"version": 999`), 1),
		bytes.Replace(before.Data, []byte(`"kind": "backpointer"`), []byte(`"kind": "arbitrary-file"`), 1),
		append(append([]byte(nil), before.Data...), []byte(`{}`)...),
	} {
		featureRenameTestWrite(t, journal, mutated, 0o600)
		if _, err := RenameExternalFeature(f.ws, "old", "new", "continue", nil); err == nil {
			t.Fatal("unknown/malformed journal granted mutation authority")
		}
		featureRenameTestWrite(t, journal, before.Data, 0o600)
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "abort", nil); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameAdmissionClosesPreJournalRaceAndScopesPendingNames(t *testing.T) {
	f := newFeatureRenameFixture(t)
	f.worktree(t, f.repo, "entry", "git-entry", false)
	FeatureRenameCheckpoint = func(step, _ string) error {
		if step == "after-admission" {
			for _, name := range []string{"old", "new"} {
				if admission, err := AcquireExternalFeatureAdmission(f.ws.MetadataRoot, name); err == nil {
					_ = admission.Release()
					t.Fatalf("%s entered before journal publication", name)
				}
			}
			unrelated, err := AcquireExternalFeatureAdmission(f.ws.MetadataRoot, "unrelated")
			if err != nil {
				t.Fatal(err)
			}
			if err := unrelated.Release(); err != nil {
				t.Fatal(err)
			}
		}
		if step == "after-move" {
			return ErrFeatureRenameInterrupted
		}
		return nil
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); !errors.Is(err, ErrFeatureRenameInterrupted) {
		t.Fatal(err)
	}
	FeatureRenameCheckpoint = nil
	for _, name := range []string{"old", "new"} {
		if admission, err := AcquireExternalFeatureAdmission(f.ws.MetadataRoot, name); err == nil {
			_ = admission.Release()
			t.Fatalf("pending %s entered", name)
		}
	}
	unrelated, err := AcquireExternalFeatureAdmission(f.ws.MetadataRoot, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if err := unrelated.Release(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpaceAdd(SpacesAnchor{Root: f.ws.MetadataRoot, Canon: f.ws.MetadataRoot, Mode: ModeExternal},
		SpaceAddRequest{Name: "new-registry", Kind: "docs", Path: f.root}); err == nil {
		t.Fatal("registry appeared while rename owns its absent preimage")
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "continue", nil); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameForwardPointerPreservationGapRecovers(t *testing.T) {
	for _, action := range []string{"continue", "abort"} {
		t.Run(action, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			path := f.worktree(t, f.repo, "entry", "git-entry", false)
			admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(f.old, admin)
			if err != nil {
				t.Fatal(err)
			}
			// This valid absolute record traverses the old feature before
			// reaching the stationary admin directory. Unlike normal absolute
			// and relative records, it needs a forward-pointer replacement.
			marker := filepath.Join(path, ".git")
			featureRenameTestWrite(t, marker, []byte("gitdir: "+f.old+"/"+relative+"\n"), 0o644)
			original := featureRenameTestImage(t, marker)
			FeatureRenameCheckpoint = func(step, _ string) error {
				if step == "after-preserve.0" {
					return ErrFeatureRenameInterrupted
				}
				return nil
			}
			if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); !errors.Is(err, ErrFeatureRenameInterrupted) {
				t.Fatal(err)
			}
			FeatureRenameCheckpoint = nil
			if _, err := os.Lstat(filepath.Join(f.new, "worktrees", "entry", ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("forward preservation checkpoint did not leave its recorded gap: %v", err)
			}
			if _, err := RenameExternalFeature(f.ws, "old", "new", action, nil); err != nil {
				t.Fatal(err)
			}
			final := f.new
			if action == "abort" {
				final = f.old
				if err := original.verifyAt(marker); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ResolveInjectWorktree(final, "entry"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFeatureRenameCleanupInterruptionAndUnknownResidue(t *testing.T) {
	for _, step := range []string{"after-cleanup-capture.0.original", "after-cleanup-remove.0.original", "after-cleanup-remove.0.next", "foreign-residue"} {
		t.Run(step, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			f.worktree(t, f.repo, "entry", "git-entry", false)
			foreignPath := ""
			FeatureRenameCheckpoint = func(got, path string) error {
				if step == "foreign-residue" && got == "before-cleanup.0" {
					foreignPath = filepath.Join(path, "user", "nested")
					featureRenameTestWrite(t, foreignPath, []byte("retain me\n"), 0o600)
					return nil
				}
				if got == step {
					return ErrFeatureRenameInterrupted
				}
				return nil
			}
			if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil {
				t.Fatal("cleanup failure reported normal success")
			}
			FeatureRenameCheckpoint = nil
			_, err := RenameExternalFeature(f.ws, "old", "new", "continue", nil)
			if step == "foreign-residue" {
				if err == nil {
					t.Fatal("cleanup swept unknown residue")
				}
				data, readErr := os.ReadFile(foreignPath)
				if readErr != nil || string(data) != "retain me\n" {
					t.Fatalf("nested foreign data was lost: %q %v", data, readErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if _, err := ResolveInjectWorktree(f.new, "entry"); err != nil {
				t.Fatalf("completed cleanup changed Git bindings: %v", err)
			}
		})
	}
}

func TestFeatureRenameAbruptProcessExitReclaimsOnlyItsReservation(t *testing.T) {
	for _, action := range []string{"continue", "abort"} {
		t.Run(action, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			f.worktree(t, f.repo, "entry", "git-entry", true)
			f.registry(t)
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(binary, "-test.run=^TestFeatureRenameCrashChild$")
			child.Env = append(os.Environ(), "TWS_FEATURE_RENAME_TEST_CHILD="+f.root)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 37 {
				t.Fatalf("child did not exit at the real move checkpoint: %v\n%s", err, output)
			}
			if _, err := os.Lstat(SyncRunGuardPath(f.new)); err != nil {
				t.Fatalf("crash did not retain the moved mutation reservation: %v", err)
			}
			if _, err := os.Lstat(f.old); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("child did not move the feature: %v", err)
			}
			if _, err := RenameExternalFeature(f.ws, "old", "new", action, nil); err != nil {
				t.Fatal(err)
			}
			final := f.new
			if action == "abort" {
				final = f.old
			}
			if _, err := ResolveInjectWorktree(final, "entry"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(SyncRunGuardPath(final)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("recovery left its reservation at the final path: %v", err)
			}
		})
	}
}

func TestFeatureRenameCrashChild(t *testing.T) {
	root := os.Getenv("TWS_FEATURE_RENAME_TEST_CHILD")
	if root == "" {
		t.Skip("subprocess-only crash fixture")
	}
	FeatureRenameCheckpoint = func(step, _ string) error {
		if step == "after-move" {
			os.Exit(37) // Deliberately bypass defers, as process loss would.
		}
		return nil
	}
	_, err := RenameExternalFeature(Workspace{
		Mode: ModeExternal, RepoRoot: filepath.Join(root, "repo"),
		MetadataRoot: filepath.Join(root, "selected-spaces"),
	}, "old", "new", "", nil)
	t.Fatalf("child did not reach crash checkpoint: %v", err)
}
