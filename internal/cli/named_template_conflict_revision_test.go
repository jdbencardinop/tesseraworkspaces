package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func namedConflictCaseAlias(t *testing.T, path string) string {
	t.Helper()
	original, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path)))
	if alias == path {
		t.Fatalf("fixture does not supply a distinct case alias: %s", path)
	}
	info, err := os.Lstat(alias)
	if errors.Is(err, os.ErrNotExist) {
		t.Skipf("filesystem is case-sensitive: existing directory %q has no case alias %q", path, alias)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(original, info) {
		t.Fatalf("case-alias fixture unexpectedly resolves to a different physical directory: %s", alias)
	}
	return alias
}

func TestNamedTemplateConflictEditorPhysicalAliasRoots(t *testing.T) {
	for _, protected := range []string{"metadata", "worktree", "registry"} {
		for _, location := range []string{"equal", "descendant"} {
			t.Run(protected+"/"+location, func(t *testing.T) {
				feature := "feature"
				var featurePath, root, suffix, localPath string
				if protected == "metadata" {
					f := newInjectFixture(t, feature)
					t.Setenv("TWS_ROOT", f.workspace)
					t.Chdir(f.repo)
					f.addWorktree(t, f.repo, "nested/entry", "physical-alias-entry")
					if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
						Name: "nested/entry", Branch: "physical-alias-entry",
					}}}); err != nil {
						t.Fatal(err)
					}
					old := managedTemplateTmuxProbe
					managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
					t.Cleanup(func() { managedTemplateTmuxProbe = old })
					if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{feature, "--scope", "worktree"}, nil); err != nil {
						t.Fatal(err)
					}
					featurePath, root = f.featurePath, f.workspace
					suffix = filepath.Join(feature, "inject")
					localPath = filepath.Join(featurePath, "inject", "CLAUDE.local.md")
				} else {
					f, path, source := newNamedConflictFixture(t, nil)
					featurePath, root = path, f.repo
					suffix = filepath.Join(".tws", "features", feature, "inject")
					localPath = filepath.Join(path, "inject", "shared.md")
					if protected == "registry" {
						root = filepath.Dir(source)
						suffix = filepath.Join(filepath.Base(source), "inject")
					}
				}
				base := namedConflictCaseAlias(t, root)
				if location == "descendant" {
					base = filepath.Join(base, suffix)
				}
				namedConflictWrite(t, localPath, "local customization\n")
				beforeFeature, beforeRoot := snapshotTree(t, featurePath), snapshotTree(t, root)
				t.Setenv("TMPDIR", base)
				t.Setenv("VISUAL", "deterministic-atomic-editor")
				fakeNamedConflictTTY(t)
				called := false
				old := managedTemplateRunEditor
				t.Cleanup(func() { managedTemplateRunEditor = old })
				managedTemplateRunEditor = func(command *exec.Cmd) error {
					called = true
					path := command.Args[len(command.Args)-1]
					if err := os.WriteFile(path+".save", []byte("edited candidate\n"), 0o600); err != nil {
						return err
					}
					return os.Rename(path+".save", path)
				}
				_, _, err := runManagedCobra(t, templateSyncCmd(), []string{
					feature, "--scope", "worktree", "--resolve-conflicts",
				}, strings.NewReader("edit\nyes\n"))
				if err == nil || !strings.Contains(err.Error(), "scratch must be outside") || called {
					t.Fatalf("physical %s alias reached scratch/editor execution: called=%v err=%v", protected, called, err)
				}
				if snapshotTree(t, featurePath) != beforeFeature || snapshotTree(t, root) != beforeRoot {
					t.Fatal("physical scratch exclusion created files or changed managed/registry content before refusal")
				}
			})
		}
	}
}

func TestNamedTemplateConflictEditorMissingAndInvalidProtectedRoots(t *testing.T) {
	for _, kind := range []string{"missing-protected", "invalid-protected", "missing-base"} {
		t.Run(kind, func(t *testing.T) {
			f := newManagedCLIFixture(t)
			base := filepath.Join(f.root, "scratch")
			if kind != "missing-base" {
				if err := os.Mkdir(base, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TMPDIR", base)
			registry, err := internal.GlobalTemplateRegistryRoot()
			if err != nil {
				t.Fatal(err)
			}
			if kind == "invalid-protected" {
				if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(registry, []byte("not a directory\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			plans := []*internal.ManagedTemplatePlan{{Request: internal.ManagedTemplatePlanRequest{
				Workspace: f.ws, Feature: "missing-feature", FeaturePath: f.ws.FeaturePath("missing-feature"),
			}}}
			scratch, err := newManagedTemplateEditorScratch(plans, []byte("current\n"), []byte("incoming\n"))
			switch kind {
			case "missing-protected":
				if err != nil {
					t.Fatalf("absent unused protected roots rejected safe outside staging: %v", err)
				}
				if err := scratch.cleanup(); err != nil {
					t.Fatal(err)
				}
				if err := scratch.Close(); err != nil {
					t.Fatal(err)
				}
			case "invalid-protected":
				if err == nil || !strings.Contains(err.Error(), "protected editor root") || scratch != nil {
					t.Fatalf("invalid protected-root inspection did not fail explicitly: %v", err)
				}
			case "missing-base":
				if !errors.Is(err, os.ErrNotExist) || scratch != nil {
					t.Fatalf("missing scratch base was silently created or masked: %v", err)
				}
			}
		})
	}
}
