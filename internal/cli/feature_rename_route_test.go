package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func TestFeatureRenameValidatedPreviewsDoNotCreateAdmissionFiles(t *testing.T) {
	f := newInjectFixture(t, "preview")
	t.Setenv("TWS_ROOT", f.workspace)
	t.Chdir(f.repo)
	f.addWorktree(t, f.repo, "entry", "preview-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "preview-entry", Base: "main",
	}}}); err != nil {
		t.Fatal(err)
	}
	namespace := filepath.Join(internal.ExternalMarkerDir(f.workspace), "feature-rename-locks")
	if _, err := os.Lstat(namespace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture unexpectedly reserved feature names: %v", err)
	}
	for _, test := range []struct {
		command *cobra.Command
		args    []string
	}{
		{syncCmd(), []string{f.feature, "--plan", "--no-fetch"}},
		{stackReparentCmd(), []string{f.feature, "entry", "--onto", "main", "--plan", "--no-fetch"}},
		{pushCmd(), []string{f.feature, "--dry-run"}},
	} {
		if _, _, err := runManagedCobra(t, test.command, test.args, nil); err != nil {
			t.Fatalf("preview %s %v: %v", test.command.Name(), test.args, err)
		}
		if _, err := os.Lstat(namespace); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s preview created rename admission state: %v", test.command.Name(), err)
		}
	}
}

func TestFeatureRenameValidatedSyncRetainsNameAdmission(t *testing.T) {
	f := newInjectFixture(t, "sync-admission")
	t.Setenv("TWS_ROOT", f.workspace)
	t.Chdir(f.repo)
	f.addWorktree(t, f.repo, "entry", "sync-admission-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "entry", Branch: "sync-admission-entry", Base: "main",
	}}}); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop after validated sync admission")
	called := false
	oldHook := ExternalSyncMutationGuardHook
	t.Cleanup(func() { ExternalSyncMutationGuardHook = oldHook })
	ExternalSyncMutationGuardHook = func(path string) error {
		called = true
		_, err := internal.RenameExternalFeature(internal.Workspace{
			Mode: internal.ModeExternal, RepoRoot: f.repo, MetadataRoot: filepath.Dir(path),
		}, f.feature, "must-not-move", "", nil)
		if err == nil || !strings.Contains(err.Error(), "feature rename admission is busy") {
			t.Fatalf("sync did not retain shared name admission through mutation: %v", err)
		}
		return stop
	}
	if _, _, err := runManagedCobra(t, syncCmd(), []string{f.feature, "--no-fetch"}, nil); !errors.Is(err, stop) || !called {
		t.Fatalf("mutation seam: called=%v err=%v", called, err)
	}
	if _, err := os.Stat(f.featurePath); err != nil {
		t.Fatal("concurrent rename moved the sync feature")
	}
}

func TestFeatureRenameValidatedReparentRetainsNameAdmission(t *testing.T) {
	f := newReparentCustomerExternal(t)
	stop := errors.New("stop before ref publication")
	called := false
	oldHook := internal.ReparentStepHook
	t.Cleanup(func() { internal.ReparentStepHook = oldHook })
	internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
		if stage != internal.ReparentStageCommittingRefs || step != "before-cas" {
			return nil
		}
		called = true
		_, err := internal.RenameExternalFeature(internal.Workspace{
			Mode: internal.ModeExternal, RepoRoot: f.Repo, MetadataRoot: filepath.Dir(f.FeaturePath),
		}, f.Feature, "must-not-move", "", nil)
		if err == nil || !strings.Contains(err.Error(), "feature rename admission is busy") {
			t.Fatalf("reparent did not retain shared name admission through mutation: %v", err)
		}
		return stop
	}
	_, stderr, exit := reparentExecute(t, f, "pr2", "master", "--no-fetch")
	if exit == 0 || !called || !strings.Contains(stderr, stop.Error()) {
		t.Fatalf("mutation seam: called=%v exit=%d stderr=%s", called, exit, stderr)
	}
	internal.ReparentStepHook = nil
	if _, stderr, exit := f.Run(f.Feature, "--abort"); exit != 0 {
		t.Fatalf("cleaning the paused reparent: %s", stderr)
	}
}
