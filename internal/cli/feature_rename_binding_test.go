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

func TestFeatureRenameAmbientAdmissionCLI(t *testing.T) {
	for _, spelling := range []string{"ordinary-temp-spelling", "root-alias", "parent-alias"} {
		t.Run(spelling, func(t *testing.T) {
			f := newInjectFixture(t, "demo")
			t.Chdir(f.repo)
			f.addWorktree(t, f.repo, "entry", "demo-entry")
			physical, err := filepath.EvalSymlinks(f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			root := f.workspace // /var/... in macOS's ordinary test environment.
			switch spelling {
			case "root-alias":
				root = filepath.Join(f.root, "workspace-alias")
				if err := os.Symlink(physical, root); err != nil {
					t.Fatal(err)
				}
			case "parent-alias":
				alias := filepath.Join(f.root, "parent-alias")
				if err := os.Symlink(filepath.Dir(physical), alias); err != nil {
					t.Fatal(err)
				}
				root = filepath.Join(alias, filepath.Base(physical))
			}
			t.Setenv("TWS_ROOT", root)
			oldProbe := managedTemplateTmuxProbe
			managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
			t.Cleanup(func() { managedTemplateTmuxProbe = oldProbe })
			for _, test := range []struct {
				cmd  *cobra.Command
				args []string
			}{
				{templateSyncCmd(), []string{"demo"}},
				{openCmd(), []string{"demo", "--feature-dir", "--no-agent"}},
				{addCmd(), []string{"another"}},
				{spaceCmd(), []string{"add", "notes", f.home, "--kind", "docs"}},
				{spaceCmd(), []string{"remove", "notes", "--workspace"}},
				{renameCmd(), []string{"feature", "demo", "renamed"}},
				{templateSyncCmd(), []string{"renamed"}},
				{openCmd(), []string{"renamed", "--feature-dir", "--no-agent"}},
				{injectCmd(), []string{"renamed"}},
			} {
				if _, _, err := runManagedCobra(t, test.cmd, test.args, nil); err != nil {
					t.Fatalf("%s %v rejected supported root spelling %s: %v", test.cmd.Name(), test.args, root, err)
				}
			}
			if _, err := internal.ResolveInjectWorktree(filepath.Join(physical, "renamed"), "entry"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFeatureRenameCLIRetainsExecutionAliasBinding(t *testing.T) {
	f := newInjectFixture(t, "old")
	t.Chdir(f.repo)
	f.addWorktree(t, f.repo, "entry", "old-entry")
	physical, err := filepath.EvalSymlinks(f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	rootAlias := filepath.Join(f.root, "workspace-alias")
	if err := os.Symlink(physical, rootAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_ROOT", rootAlias)
	oldProbe := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() {
		managedTemplateTmuxProbe = oldProbe
		internal.FeatureRenameCheckpoint = nil
	})
	foreign := filepath.Join(f.root, "foreign-workspace")
	if err := os.Mkdir(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	internal.FeatureRenameCheckpoint = func(step, _ string) error {
		if step != "before-move" {
			return nil
		}
		if err := os.Rename(rootAlias, rootAlias+".original"); err != nil {
			return err
		}
		return os.Symlink(foreign, rootAlias)
	}
	out, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new"}, nil)
	if err == nil || !strings.Contains(err.Error(), "alias") || strings.Contains(out, "Renamed feature:") {
		t.Fatalf("canonical CLI context lost its selected alias: %q %v", out, err)
	}
	internal.FeatureRenameCheckpoint = nil
	if _, err := os.Lstat(filepath.Join(physical, "old")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(foreign, ".tws-workspace")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retargeted root received operation state: %v", err)
	}
	t.Setenv("TWS_ROOT", physical)
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new", "--continue"}, nil); err == nil {
		t.Fatal("physical root spelling bypassed the recorded alias identity")
	}
	if err := os.Rename(rootAlias, rootAlias+".foreign"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(rootAlias+".original", rootAlias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "old", "new", "--abort"}, nil); err != nil {
		t.Fatal(err)
	}
}
