package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func assertReparentCompletion(t *testing.T, flag string, args, want []string) {
	t.Helper()
	cmd, _, err := stackCmd().Find([]string{"reparent"})
	if err != nil {
		t.Fatal(err)
	}
	fn := cmd.ValidArgsFunction
	if flag != "" {
		var ok bool
		fn, ok = cmd.GetFlagCompletionFunc(flag)
		if !ok || fn == nil {
			t.Fatalf("--%s has no registered completion", flag)
		}
	}
	stdout, stderr := syncCaptureStreams(t, func() {
		got, directive := fn(cmd, args, "")
		if !reflect.DeepEqual(got, want) || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%v --%s: completion = %v (%v), want %v (NoFileComp)", args, flag, got, directive, want)
		}
	})
	if stdout != "" || stderr != "" {
		t.Fatalf("completion was not silent: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestStackReparentCompletion_SelectedFeatureAndTargetRepository(t *testing.T) {
	f := newInjectFixture(t, "feature")
	injectGit(t, f.repo, "branch", "caller-only")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "configured-only", Branch: "caller-only", Base: "main",
	}}}); err != nil {
		t.Fatal(err)
	}
	secondary := createInjectRepo(t, f.root, "secondary")
	canonicalSecondary, err := filepath.EvalSymlinks(secondary)
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"z-topic", "topic/target", "a-destination", "topic/child"} {
		injectGit(t, secondary, "branch", branch)
	}
	selectedRoot := filepath.Join(f.root, "selected")
	selectedFeature := mustMkdir(t, filepath.Join(selectedRoot, f.feature))
	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "z-logical", Branch: "z-topic", Base: "main", Repo: secondary},
		{Name: "logical/target", Branch: "topic/target", Base: "z-logical", Repo: secondary},
		{Name: "child/leaf", Branch: "topic/child", Base: "logical/target", Repo: secondary},
		{Name: "archived", Base: "main", Repo: secondary, Archived: true},
		{Name: "a-logical", Branch: "a-destination", Base: "main", Repo: secondary},
		{Name: "grandchild", Base: "child/leaf", Repo: secondary, Archived: true},
		{Name: "other-repo", Branch: "caller-only", Base: "logical/target", Repo: f.repo},
	}}
	if err := internal.SaveStack(selectedFeature, stack); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(selectedFeature, "worktrees", "logical", "target")
	injectGit(t, secondary, "worktree", "add", worktree, "topic/target")
	t.Setenv("TWS_ROOT", selectedRoot)
	logical := []string{"z-logical", "logical/target", "child/leaf", "archived", "a-logical", "grandchild", "other-repo"}
	destinations := []string{"z-logical", "archived", "a-logical", "other-repo", "a-destination", "main", "topic/child", "topic/target", "z-topic"}
	before := featureRenameReviewSnapshot(t, f.root)
	wrapper := newSyncGitWrapper(t, false)
	wrapper.around(t, func() {
		assertReparentCompletion(t, "", []string{f.feature}, logical)
		assertReparentCompletion(t, "onto", nil, nil)
		assertReparentCompletion(t, "onto", []string{f.feature}, logical)
		assertReparentCompletion(t, "onto", []string{f.feature, "missing-target"}, nil)
		assertReparentCompletion(t, "onto", []string{f.feature, "logical/target"}, destinations)
		stdout, stderr, code := runTWSExecute(t, "__complete", "stack", "reparent", f.feature, "logical/target", "--onto", "")
		if code != 0 || stdout != strings.Join(destinations, "\n")+"\n:4\n" ||
			(stderr != "" && !strings.Contains(stderr, "Completion ended with directive")) {
			t.Fatalf("Cobra completion: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		t.Run("linked-worktree", func(t *testing.T) {
			t.Chdir(worktree)
			assertReparentCompletion(t, "onto", []string{f.feature, "logical/target"}, destinations)
		})
	})
	inventories := 0
	for _, record := range wrapper.records(t) {
		switch record.Verb {
		case "rev-parse":
		case "for-each-ref":
			inventories++
			if !argvHas(record.Argv, canonicalSecondary) {
				t.Fatalf("branch inventory borrowed the caller repository: %v", record.Argv)
			}
		default:
			t.Fatalf("completion ran a non-inventory Git command: %v", record.Argv)
		}
	}
	if inventories != 3 {
		t.Fatalf("completion ran %d branch inventories, want one per known-target callback (3)", inventories)
	}
	if got := featureRenameReviewSnapshot(t, f.root); got != before {
		t.Fatal("completion changed repository, remote, worktree, or workspace files")
	}
	t.Run("configured-fallback", func(t *testing.T) {
		if err := os.Remove(internal.StackPath(selectedFeature)); err != nil {
			t.Fatal(err)
		}
		before := featureRenameReviewSnapshot(t, f.root)
		assertReparentCompletion(t, "", []string{f.feature}, []string{"configured-only"})
		assertReparentCompletion(t, "onto", []string{f.feature, "configured-only"}, []string{"caller-only", "main"})
		if featureRenameReviewSnapshot(t, f.root) != before {
			t.Fatal("completion changed the configured fallback")
		}
	})
}

func TestStackReparentCompletion_CheckoutLayouts(t *testing.T) {
	for _, layout := range []string{"namespaced", "legacy"} {
		t.Run(layout, func(t *testing.T) {
			f := newInjectFixture(t, "feature")
			root := mustMkdir(t, filepath.Join(f.repo, ".tws"))
			if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("workspace_mode: checkout\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			featurePath := filepath.Join(root, "features", f.feature)
			otherPath := filepath.Join(root, f.feature)
			if layout == "legacy" {
				featurePath, otherPath = otherPath, featurePath
			}
			mustMkdir(t, featurePath)
			injectGit(t, f.repo, "branch", "topic/target")
			stack := internal.Stack{Branches: []internal.StackEntry{
				{Name: "logical/target", Branch: "topic/target", Base: "main"},
				{Name: "archived", Base: "main", Archived: true},
			}}
			if err := internal.SaveStack(featurePath, stack); err != nil {
				t.Fatal(err)
			}
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "wrong-mode"}}}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TWS_ROOT", f.workspace)
			before := featureRenameReviewSnapshot(t, f.root)
			assertReparentCompletion(t, "", []string{f.feature}, []string{"logical/target", "archived"})
			assertReparentCompletion(t, "onto", []string{f.feature, "logical/target"}, []string{"archived", "main", "topic/target"})
			if featureRenameReviewSnapshot(t, f.root) != before {
				t.Fatal("checkout completion changed files")
			}
			stack.Branches[0].Repo = f.repo
			if err := internal.SaveStack(featurePath, stack); err != nil {
				t.Fatal(err)
			}
			assertReparentCompletion(t, "onto", []string{f.feature, "logical/target"}, nil)
			stack.Branches[0].Repo = ""
			if err := internal.SaveStack(featurePath, stack); err != nil {
				t.Fatal(err)
			}
			mustMkdir(t, otherPath)
			assertReparentCompletion(t, "", []string{f.feature}, nil)
			assertReparentCompletion(t, "onto", []string{f.feature, "logical/target"}, nil)
		})
	}
}

func TestStackReparentCompletion_UnavailableContext(t *testing.T) {
	f := newInjectFixture(t, "feature")
	t.Setenv("TWS_ROOT", f.workspace)
	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "target", Branch: "topic/target", Base: "main"},
		{Name: "destination", Base: "main"},
	}}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"missing", "../escape"} {
		assertReparentCompletion(t, "", []string{feature}, nil)
		assertReparentCompletion(t, "onto", []string{feature, "target"}, nil)
	}
	assertReparentCompletion(t, "onto", []string{f.feature, "unknown"}, nil)
	for _, test := range []struct {
		name string
		edit func(*internal.Stack)
	}{
		{"missing-repository", func(s *internal.Stack) { s.Branches[0].Repo = filepath.Join(f.root, "missing-repo") }},
		{"duplicate-name", func(s *internal.Stack) { s.Branches[1].Name = "target" }},
		{"duplicate-git-branch", func(s *internal.Stack) { s.Branches[1].Branch = "topic/target" }},
		{"cycle", func(s *internal.Stack) {
			s.Branches[0].Base = "destination"
			s.Branches[1].Base = "target"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := internal.Stack{Branches: append([]internal.StackEntry{}, stack.Branches...)}
			test.edit(&current)
			if err := internal.SaveStack(f.featurePath, current); err != nil {
				t.Fatal(err)
			}
			before := featureRenameReviewSnapshot(t, f.root)
			assertReparentCompletion(t, "onto", []string{f.feature, "target"}, nil)
			if featureRenameReviewSnapshot(t, f.root) != before {
				t.Fatal("unavailable completion changed files")
			}
		})
	}
	for _, contents := range []string{"branches: [invalid", ""} {
		if err := os.WriteFile(internal.StackPath(f.featurePath), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		assertReparentCompletion(t, "onto", []string{f.feature, "target"}, nil)
	}
	if err := os.Remove(internal.StackPath(f.featurePath)); err != nil {
		t.Fatal(err)
	}
	for _, unreadable := range []bool{false, true} {
		if unreadable {
			mustMkdir(t, internal.StackPath(f.featurePath))
		}
		assertReparentCompletion(t, "", []string{f.feature}, nil)
		assertReparentCompletion(t, "onto", []string{f.feature, "target"}, nil)
	}
	t.Run("no-workspace", func(t *testing.T) {
		t.Setenv("TWS_ROOT", "")
		t.Setenv("HOME", t.TempDir())
		t.Chdir(t.TempDir())
		assertReparentCompletion(t, "", []string{f.feature}, nil)
		assertReparentCompletion(t, "onto", []string{f.feature, "target"}, nil)
	})
}

func TestStackReparentCompletion_PendingSelectedRoot(t *testing.T) {
	for _, pending := range []string{"rename", "publication"} {
		t.Run(pending, func(t *testing.T) {
			f := newInjectFixture(t, "feature")
			if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "configured-only"}}}); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(f.root, "selected")
			if err := internal.EnsureExternalWorkspaceMarker(root); err != nil {
				t.Fatal(err)
			}
			featurePath := mustMkdir(t, filepath.Join(root, f.feature))
			if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{{
				Name: "target", Branch: "selected/target", Base: "main", Repo: f.repo,
			}}}); err != nil {
				t.Fatal(err)
			}
			injectGit(t, f.repo, "worktree", "add", filepath.Join(featurePath, "worktrees", "target"), "-b", "selected/target")
			t.Setenv("TWS_ROOT", root)
			switch pending {
			case "rename":
				oldHook := internal.FeatureRenameCheckpoint
				t.Cleanup(func() { internal.FeatureRenameCheckpoint = oldHook })
				internal.FeatureRenameCheckpoint = func(step, _ string) error {
					if step == "after-move" {
						return internal.ErrFeatureRenameInterrupted
					}
					return nil
				}
				_, err := internal.RenameExternalFeature(internal.Workspace{
					Mode: internal.ModeExternal, RepoRoot: f.repo, MetadataRoot: root,
				}, f.feature, "renamed", "", nil)
				internal.FeatureRenameCheckpoint = oldHook
				if !errors.Is(err, internal.ErrFeatureRenameInterrupted) {
					t.Fatalf("create interrupted rename: %v", err)
				}
			case "publication":
				restore := rejectPushOf(t, filepath.Join(f.root, "primary.git"), "selected/target")
				defer restore()
				if _, _, err := runPublication(t, "--only", "target"); err == nil {
					t.Fatal("expected rejected local publication")
				}
				state, err := internal.LoadScopedPushState(internal.ScopedPushLocation{
					Mode: internal.ModeExternal, Feature: f.feature, FeaturePath: featurePath, MetadataRoot: root,
				})
				if err != nil || state == nil || state.Terminal() {
					t.Fatalf("expected pending publication: state=%+v err=%v", state, err)
				}
				if err := os.Remove(internal.StackPath(featurePath)); err != nil {
					t.Fatal(err)
				}
			}
			before := featureRenameReviewSnapshot(t, f.root)
			assertReparentCompletion(t, "", []string{f.feature}, nil)
			assertReparentCompletion(t, "onto", []string{f.feature, "target"}, nil)
			if featureRenameReviewSnapshot(t, f.root) != before {
				t.Fatal("completion changed pending selected-root evidence or its configured fallback")
			}
		})
	}
}
