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

type featureRenameFixture struct {
	root string
	repo string
	ws   Workspace
	old  string
	new  string
}

func featureRenameTestGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

func featureRenameTestWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func newFeatureRenameFixture(t *testing.T) featureRenameFixture {
	t.Helper()
	// Private filesystem primitives take physical paths. Public alias coverage
	// supplies alternate root spellings explicitly, rather than weakening them.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	global := filepath.Join(home, "gitconfig")
	featureRenameTestWrite(t, global, []byte("[user]\nname = Feature Rename\nemail = rename@example.test\n"), 0o600)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_AUTHOR_NAME", "Feature Rename")
	t.Setenv("GIT_AUTHOR_EMAIL", "rename@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "Feature Rename")
	t.Setenv("GIT_COMMITTER_EMAIL", "rename@example.test")
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	repo := featureRenameTestRepository(t, root, "repo")
	workspace := filepath.Join(root, "selected-spaces")
	old := filepath.Join(workspace, "old")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { FeatureRenameCheckpoint = nil })
	return featureRenameFixture{
		root: root, repo: repo, old: old, new: filepath.Join(workspace, "new"),
		ws: Workspace{Mode: ModeExternal, RepoRoot: repo, MetadataRoot: workspace},
	}
}

func featureRenameTestRepository(t *testing.T, root, name string) string {
	t.Helper()
	bare := filepath.Join(root, name+".git")
	featureRenameTestGit(t, root, "init", "--bare", "--initial-branch=main", bare)
	repo := filepath.Join(root, name)
	featureRenameTestGit(t, root, "clone", bare, repo)
	featureRenameTestWrite(t, filepath.Join(repo, "tracked"), []byte("base\n"), 0o644)
	featureRenameTestWrite(t, filepath.Join(repo, ".gitignore"), []byte("ignored\n"), 0o644)
	featureRenameTestGit(t, repo, "add", ".")
	featureRenameTestGit(t, repo, "commit", "-m", "base")
	featureRenameTestGit(t, repo, "push", "-u", "origin", "main")
	return repo
}

func (f featureRenameFixture) worktree(t *testing.T, repo, name, branch string, relative bool) string {
	t.Helper()
	path := filepath.Join(f.old, "worktrees", name)
	featureRenameTestGit(t, repo, "worktree", "add", "-b", branch, path, "main")
	if relative {
		admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		forward, err := filepath.Rel(path, admin)
		if err != nil {
			t.Fatal(err)
		}
		back, err := filepath.Rel(admin, filepath.Join(path, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		featureRenameTestWrite(t, filepath.Join(path, ".git"), []byte("gitdir: "+forward+"\n"), 0o644)
		featureRenameTestWrite(t, filepath.Join(admin, "gitdir"), []byte(back+"\n"), 0o644)
	}
	return path
}

func featureRenameTestImage(t *testing.T, path string) featureRenameFile {
	t.Helper()
	file, err := featureRenameReadFile(path, true)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func (f featureRenameFixture) registry(t *testing.T) {
	t.Helper()
	featureRenameTestWrite(t, filepath.Join(f.old, "notes", "user"), []byte("notes\n"), 0o644)
	_, _, err := SpaceAdd(SpacesAnchor{Root: f.ws.MetadataRoot, Canon: f.ws.MetadataRoot, Mode: ModeExternal},
		SpaceAddRequest{Name: "notes", Kind: "docs", Feature: "old", Path: filepath.Join(f.old, "notes")})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameDirtyMultiRepoAndPointerStyles(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(fmt.Sprint(relative), func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			secondary := featureRenameTestRepository(t, f.root, "secondary")
			first := f.worktree(t, f.repo, "literal[1]/leaf", "different-git-branch", relative)
			second := f.worktree(t, secondary, "other", "secondary-git", !relative)
			if err := SaveStack(f.old, Stack{Branches: []StackEntry{
				{Name: "literal[1]/leaf", Branch: "different-git-branch", Base: "main"},
				{Name: "other", Branch: "secondary-git", Base: "main", Repo: secondary},
				{Name: "archived", Branch: "not-materialized", Archived: true, Base: "main"},
			}}); err != nil {
				t.Fatal(err)
			}
			f.registry(t)
			featureRenameTestWrite(t, filepath.Join(first, "tracked"), []byte("staged\n"), 0o644)
			featureRenameTestGit(t, first, "add", "tracked")
			featureRenameTestWrite(t, filepath.Join(first, "tracked"), []byte("unstaged\n"), 0o644)
			featureRenameTestWrite(t, filepath.Join(first, "untracked"), []byte{0, 1, '\n'}, 0o644)
			featureRenameTestWrite(t, filepath.Join(first, "ignored"), []byte("ignored\n"), 0o644)
			featureRenameTestWrite(t, filepath.Join(first, "ordinary-fixture", "HEAD"), []byte("not a Git repository\n"), 0o644)
			if err := os.Mkdir(filepath.Join(first, "ordinary-fixture", "objects"), 0o755); err != nil {
				t.Fatal(err)
			}
			featureRenameTestWrite(t, filepath.Join(second, "executable"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
			if err := os.Symlink("../untracked", filepath.Join(first, "user-link")); err != nil {
				t.Fatal(err)
			}
			var snapshots []featureRenameFile
			for _, name := range []string{"tracked", "untracked", "ignored", ".git", "ordinary-fixture/HEAD"} {
				snapshots = append(snapshots, featureRenameTestImage(t, filepath.Join(first, name)))
			}
			snapshots = append(snapshots, featureRenameTestImage(t, filepath.Join(second, "executable")))
			linkBefore, err := os.Lstat(filepath.Join(first, "user-link"))
			if err != nil {
				t.Fatal(err)
			}
			admin, err := resolveInjectGitDir(first, filepath.Join(first, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			indexBefore := featureRenameTestImage(t, filepath.Join(admin, "index"))
			featureBefore, err := featureRenameDirectoryAt(f.old)
			if err != nil {
				t.Fatal(err)
			}
			// A broken unrelated worktree must neither block nor get repaired.
			sibling := filepath.Join(f.root, "broken-sibling")
			featureRenameTestGit(t, f.repo, "worktree", "add", "-b", "sibling", sibling, "main")
			siblingAdmin, err := resolveInjectGitDir(sibling, filepath.Join(sibling, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			siblingBack := featureRenameTestImage(t, filepath.Join(siblingAdmin, "gitdir"))
			configBefore := featureRenameTestImage(t, filepath.Join(f.repo, ".git", "config"))
			refs := featureRenameTestGit(t, f.repo, "show-ref")
			featureRenameTestWrite(t, filepath.Join(sibling, ".git"), []byte("gitdir: /missing/unrelated\n"), 0o644)
			siblingBefore := featureRenameTestImage(t, filepath.Join(sibling, ".git"))
			if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err != nil {
				t.Fatal(err)
			}
			featureAfter, err := featureRenameDirectoryAt(f.new)
			if err != nil || featureBefore.Identity != featureAfter.Identity {
				t.Fatalf("directory identity changed: %v", err)
			}
			for _, snapshot := range snapshots {
				newPath := strings.Replace(snapshot.Path, f.old+string(filepath.Separator), f.new+string(filepath.Separator), 1)
				if err := snapshot.verifyAt(newPath); err != nil {
					t.Fatal(err)
				}
			}
			linkAfter, err := os.Lstat(filepath.Join(f.new, "worktrees", "literal[1]/leaf", "user-link"))
			if err != nil || !os.SameFile(linkBefore, linkAfter) {
				t.Fatalf("user symlink inode changed: %v", err)
			}
			if err := indexBefore.verifyAt(indexBefore.Path); err != nil {
				t.Fatal(err)
			}
			if err := siblingBefore.verifyAt(siblingBefore.Path); err != nil {
				t.Fatal(err)
			}
			if err := siblingBack.verifyAt(siblingBack.Path); err != nil {
				t.Fatal(err)
			}
			if err := configBefore.verifyAt(configBefore.Path); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(refs, featureRenameTestGit(t, f.repo, "show-ref")) {
				t.Fatal("rename changed refs")
			}
			for _, name := range []string{"literal[1]/leaf", "other"} {
				if _, err := ResolveInjectWorktree(f.new, name); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Lstat(f.old); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old feature remains: %v", err)
			}
			if err := CheckExternalFeatureRenamePending(f.ws.MetadataRoot, "old", "new"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFeatureRenameCheckpointsRecoveryAndRollback(t *testing.T) {
	steps := []string{
		"before-move", "after-move",
		"before-preserve.0", "after-preserve.0", "before-publish.0", "after-publish.0",
		"before-preserve.1", "after-preserve.1", "before-publish.1", "after-publish.1",
		"before-complete", "after-complete",
		"before-cleanup.0", "after-cleanup.0", "before-cleanup.1", "after-cleanup.1",
		"before-cleaned", "after-cleaned",
	}
	for _, step := range steps {
		for _, action := range []string{"ordinary", "continue", "abort"} {
			t.Run(step+"/"+action, func(t *testing.T) {
				f := newFeatureRenameFixture(t)
				path := f.worktree(t, f.repo, "entry", "git-entry", false)
				f.registry(t)
				registry := featureRenameTestImage(t, SpacesFilePath(f.ws.MetadataRoot))
				admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				pointer := featureRenameTestImage(t, filepath.Join(admin, "gitdir"))
				injected := errors.New("checkpoint failure")
				called := false
				FeatureRenameCheckpoint = func(got, _ string) error {
					if got != step || called {
						return nil
					}
					called = true
					if action == "ordinary" {
						return injected
					}
					return ErrFeatureRenameInterrupted
				}
				_, err = RenameExternalFeature(f.ws, "old", "new", "", nil)
				if err == nil || !called {
					t.Fatalf("checkpoint did not stop operation: called=%v err=%v", called, err)
				}
				FeatureRenameCheckpoint = nil
				committed := step == "after-complete" || strings.Contains(step, "cleanup.") || strings.HasSuffix(step, "cleaned")
				if action == "ordinary" && !committed {
					if err := pointer.verifyAt(pointer.Path); err != nil {
						t.Fatal(err)
					}
					if err := registry.verifyAt(registry.Path); err != nil {
						t.Fatal(err)
					}
					if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
						t.Fatal(err)
					}
					return
				}
				if action == "ordinary" {
					action = "continue"
				}
				if action == "abort" && committed {
					if _, err := RenameExternalFeature(f.ws, "old", "new", action, nil); err == nil {
						t.Fatal("abort undid a committed rename")
					}
					action = "continue"
				}
				result, err := RenameExternalFeature(f.ws, "old", "new", action, nil)
				if err != nil {
					t.Fatal(err)
				}
				final := f.new
				if action == "abort" {
					final = f.old
					if !result.Aborted {
						t.Fatal("abort returned forward success")
					}
					if err := pointer.verifyAt(pointer.Path); err != nil {
						t.Fatal(err)
					}
					if err := registry.verifyAt(registry.Path); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := ResolveInjectWorktree(final, "entry"); err != nil {
					t.Fatal(err)
				}
				if _, err := RenameExternalFeature(f.ws, "old", "new", action, nil); err == nil || !strings.Contains(err.Error(), "no pending") {
					t.Fatalf("repeated cleaned recovery was not explicit: %v", err)
				}
			})
		}
	}
}
