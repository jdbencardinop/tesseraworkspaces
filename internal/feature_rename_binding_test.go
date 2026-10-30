package internal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFeatureRenameSpecialLockRefusesWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"fifo", "fifo-after-type-check", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(ExternalMarkerDir(f.ws.MetadataRoot), featureRenameLockDir, ".journals")
			if kind != "fifo-after-type-check" {
				if err := os.Rename(lock, lock+".original"); err != nil {
					t.Fatal(err)
				}
				if kind == "fifo" {
					if err := unix.Mkfifo(lock, 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(".journals.original", lock); err != nil {
					t.Fatal(err)
				}
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "-test.run=^TestFeatureRenameSpecialLockChild$")
			child.WaitDelay = time.Second
			child.Env = append(os.Environ(),
				"TWS_FEATURE_RENAME_LOCK_TEST_ROOT="+f.ws.MetadataRoot,
				"TWS_FEATURE_RENAME_LOCK_TEST_KIND="+kind,
			)
			out, err := child.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("special-file admission blocked instead of refusing: %v\n%s", ctx.Err(), out)
			}
			if err != nil {
				t.Fatalf("special-file subprocess: %v\n%s", err, out)
			}
			info, err := os.Lstat(lock)
			if err != nil || info.Mode().IsRegular() {
				t.Fatalf("invalid lock was removed/replaced: %v", err)
			}
			if _, err := os.Stat(lock + ".original"); err != nil {
				t.Fatal("original lock evidence was not preserved")
			}
		})
	}
}

func TestFeatureRenameSpecialLockChild(t *testing.T) {
	root := os.Getenv("TWS_FEATURE_RENAME_LOCK_TEST_ROOT")
	if root == "" {
		t.Skip("bounded subprocess-only fixture")
	}
	if os.Getenv("TWS_FEATURE_RENAME_LOCK_TEST_KIND") == "fifo-after-type-check" {
		FeatureRenameCheckpoint = func(step, path string) error {
			if step != "before-lock-open" || filepath.Base(path) != ".journals" {
				return nil
			}
			if err := os.Rename(path, path+".original"); err != nil {
				return err
			}
			return unix.Mkfifo(path, 0o600)
		}
	}
	held, err := AcquireExternalFeatureAdmission(root, "new")
	if err == nil {
		_ = held.Release()
		t.Fatal("special lock was admitted")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("unexpected special-file refusal: %v", err)
	}
}

func TestFeatureRenameMacOSAmbientRootBinding(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS ambient alias")
	}
	// Read-only: exercise the actual system spelling independently of TMPDIR.
	want, err := filepath.EvalSymlinks("/var")
	if err != nil {
		t.Fatal(err)
	}
	directory, alias, err := resolveFeatureRenameRoot("/var")
	if err != nil || alias == nil || directory.Path != want {
		t.Fatalf("ambient /var binding: %+v %+v %v", directory, alias, err)
	}
	if err := alias.verify(); err != nil {
		t.Fatal(err)
	}
	if root, err := featureRenameOpenRoot("/var"); err == nil {
		_ = root.Close()
		t.Fatal("the descendant no-follow primitive was weakened")
	}
}

func TestFeatureRenameRootAliasSharesAdmissionAndRecovery(t *testing.T) {
	for _, action := range []string{"continue", "abort"} {
		t.Run(action, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			ambient := filepath.Join(f.root, "ambient")
			if err := os.Symlink(f.root, ambient); err != nil {
				t.Fatal(err)
			}
			aliasWS := f.ws
			aliasWS.MetadataRoot = filepath.Join(ambient, "selected-spaces")
			aliasWS.RepoRoot = filepath.Join(ambient, "repo")
			tree := filepath.Join(aliasWS.MetadataRoot, "old", "worktrees", "entry")
			featureRenameTestGit(t, f.repo, "worktree", "add", "-b", "entry", tree, "main")
			admin, err := resolveInjectGitDir(tree, filepath.Join(tree, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			// Exercise both pointer records with ambient spellings, regardless
			// of which spelling this Git version writes by default.
			admin = canonicalize(admin)
			aliasAdmin := filepath.Join(ambient, "repo", ".git", "worktrees", filepath.Base(admin))
			featureRenameTestWrite(t, filepath.Join(tree, ".git"), []byte("gitdir: "+aliasAdmin+"\n"), 0o644)
			featureRenameTestWrite(t, filepath.Join(admin, "gitdir"), []byte(filepath.Join(tree, ".git")+"\n"), 0o644)
			FeatureRenameCheckpoint = func(step, _ string) error {
				if step == "after-admission" {
					if held, err := AcquireExternalFeatureAdmission(f.ws.MetadataRoot, "old"); err == nil {
						_ = held.Release()
						t.Fatal("physical and alias spellings acquired different name locks")
					}
				}
				if step == "after-move" {
					return ErrFeatureRenameInterrupted
				}
				return nil
			}
			if _, err := RenameExternalFeature(aliasWS, "old", "new", "", nil); !errors.Is(err, ErrFeatureRenameInterrupted) {
				t.Fatal(err)
			}
			FeatureRenameCheckpoint = nil
			if held, err := AcquireExternalFeatureAdmission(aliasWS.MetadataRoot, "new"); err == nil {
				_ = held.Release()
				t.Fatal("alias spelling bypassed the pending journal")
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
		})
	}
}

func TestFeatureRenameRootAliasRetargetAndReplacementRefuse(t *testing.T) {
	for _, when := range []string{"after-admission", "before-move", "after-move"} {
		for _, replacement := range []string{"foreign-target", "same-target-new-link"} {
			t.Run(when+"/"+replacement, func(t *testing.T) {
				f := newFeatureRenameFixture(t)
				f.worktree(t, f.repo, "entry", "entry", false)
				alias := filepath.Join(f.root, "workspace-alias")
				if err := os.Symlink(f.ws.MetadataRoot, alias); err != nil {
					t.Fatal(err)
				}
				foreign := filepath.Join(f.root, "foreign")
				featureRenameTestWrite(t, filepath.Join(foreign, "nested", "user"), []byte("foreign\n"), 0o644)
				ws := f.ws
				ws.MetadataRoot = alias
				changed := false
				FeatureRenameCheckpoint = func(step, _ string) error {
					if step != when || changed {
						return nil
					}
					changed = true
					if err := os.Rename(alias, alias+".original"); err != nil {
						return err
					}
					target := foreign
					if replacement == "same-target-new-link" {
						target = f.ws.MetadataRoot
					}
					return os.Symlink(target, alias)
				}
				if _, err := RenameExternalFeature(ws, "old", "new", "", nil); err == nil || !changed {
					t.Fatalf("alias replacement was adopted: changed=%v err=%v", changed, err)
				}
				FeatureRenameCheckpoint = nil
				if data, err := os.ReadFile(filepath.Join(foreign, "nested", "user")); err != nil || string(data) != "foreign\n" {
					t.Fatalf("foreign target changed: %q %v", data, err)
				}
				if _, err := os.Lstat(filepath.Join(foreign, ".tws-workspace")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("late retarget created foreign admission state: %v", err)
				}
				if when != "after-admission" {
					if _, err := RenameExternalFeature(f.ws, "old", "new", "continue", nil); err == nil {
						t.Fatal("physical spelling bypassed frozen alias evidence")
					}
				}
				// Retain both link objects; restore the original inode rather
				// than unlink/recreate and hope the filesystem does not reuse it.
				if err := os.Rename(alias, alias+".foreign"); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(alias+".original", alias); err != nil {
					t.Fatal(err)
				}
				recovery := "abort"
				if when == "after-admission" {
					recovery = ""
				}
				if _, err := RenameExternalFeature(ws, "old", "new", recovery, nil); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestFeatureRenameAliasDoesNotAdoptSelectedSymlink(t *testing.T) {
	f := newFeatureRenameFixture(t)
	f.worktree(t, f.repo, "entry", "entry", false)
	alias := filepath.Join(f.root, "workspace-alias")
	if err := os.Symlink(f.ws.MetadataRoot, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.old, filepath.Join(f.root, "retained-feature")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.root, "retained-feature"), f.old); err != nil {
		t.Fatal(err)
	}
	f.ws.MetadataRoot = alias
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil {
		t.Fatal("root alias support normalized a selected feature symlink into adoption")
	}
	info, err := os.Lstat(f.old)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("source symlink was replaced: %v", err)
	}
}

func TestFeatureRenameRootAliasKeepsAdministrativeDescendantsNoFollow(t *testing.T) {
	f := newFeatureRenameFixture(t)
	f.worktree(t, f.repo, "entry", "entry", false)
	rootAlias := filepath.Join(f.root, "workspace-alias")
	if err := os.Symlink(f.ws.MetadataRoot, rootAlias); err != nil {
		t.Fatal(err)
	}
	adminParent := filepath.Join(f.repo, ".git", "worktrees")
	if err := os.Rename(adminParent, adminParent+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("worktrees.original", adminParent); err != nil {
		t.Fatal(err)
	}
	f.ws.MetadataRoot = rootAlias
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil {
		t.Fatal("administrative descendant symlink was normalized into adoption")
	}
	if _, err := os.Lstat(f.old); err != nil {
		t.Fatal("unsupported descendant layout was moved")
	}
}

func TestFeatureRenameStaleAliasRegistrationStillRefuses(t *testing.T) {
	f := newFeatureRenameFixture(t)
	path := f.worktree(t, f.repo, "entry", "entry", false)
	admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(f.root, "ambient")
	if err := os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	featureRenameTestWrite(t, filepath.Join(admin, "gitdir"),
		[]byte(filepath.Join(alias, "selected-spaces", "old", "worktrees", "entry", ".git")+"\n"), 0o644)
	if err := os.Rename(path, filepath.Join(f.root, "retained-worktree")); err != nil {
		t.Fatal(err)
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("canonical invocation missed a stale alias registration: %v", err)
	}
}

func TestFeatureRenameAdmissionJoinsConcurrentLockCreator(t *testing.T) {
	for _, winnerKind := range []string{"regular", "fifo", "symlink"} {
		t.Run(winnerKind, func(t *testing.T) {
			path := t.TempDir()
			parent, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close() //nolint:errcheck
			var winner *os.File
			published := false
			openat := func(dir int, name string, flags int, mode uint32) (int, error) {
				if !published {
					published = true
					switch winnerKind {
					case "regular":
						fd, err := unix.Openat(dir, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
						if err != nil {
							return -1, err
						}
						winner = os.NewFile(uintptr(fd), filepath.Join(path, name))
						t.Cleanup(func() { _ = winner.Close() })
					case "fifo":
						if err := unix.Mkfifo(filepath.Join(path, name), mode); err != nil {
							return -1, err
						}
					case "symlink":
						if err := os.Symlink("untouched-target", filepath.Join(path, name)); err != nil {
							return -1, err
						}
					}
				}
				if flags&unix.O_CREAT != 0 && flags&unix.O_EXCL == 0 {
					// Model the observed Darwin concurrent-create outcome:
					// the winner exists, but a racing nonexclusive create
					// with O_NOFOLLOW reports ENOENT.
					return -1, unix.ENOENT
				}
				return unix.Openat(dir, name, flags, mode)
			}
			flags := unix.O_RDWR | unix.O_CREAT | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
			fd, err := featureRenameOpenLock(parent, ".registry", flags, openat)
			if winnerKind != "regular" {
				if err == nil {
					_ = unix.Close(fd)
					t.Fatal("competing special-file publication was adopted")
				}
				if !strings.Contains(err.Error(), "invalid feature rename lock") {
					t.Fatalf("expected special-file refusal, got %v", err)
				}
				if _, err := os.Lstat(filepath.Join(path, ".registry")); err != nil {
					t.Fatal("unexpected winner was removed")
				}
				return
			}
			if err != nil {
				t.Fatalf("could not join the competing regular lock: %v", err)
			}
			joined := os.NewFile(uintptr(fd), ".registry")
			defer joined.Close() //nolint:errcheck
			before, err := winner.Stat()
			if err != nil {
				t.Fatal(err)
			}
			after, err := joined.Stat()
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("concurrent creators split the lock domain: %v", err)
			}
		})
	}
}

func TestFeatureRenameAdmissionDoesNotRecreateDisappearingLock(t *testing.T) {
	path := t.TempDir()
	lock := filepath.Join(path, ".registry")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close() //nolint:errcheck
	existingOpens := 0
	openat := func(dir int, name string, flags int, mode uint32) (int, error) {
		if flags&unix.O_EXCL == 0 {
			existingOpens++
			if existingOpens == 1 {
				if err := os.Rename(lock, lock+".preserved"); err != nil {
					return -1, err
				}
			}
		}
		return unix.Openat(dir, name, flags, mode)
	}
	flags := unix.O_RDWR | unix.O_CREAT | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := featureRenameOpenLock(parent, ".registry", flags, openat)
	if err == nil {
		_ = unix.Close(fd)
		t.Fatal("disappearing lock was silently recreated")
	}
	if !errors.Is(err, unix.ENOENT) || existingOpens != 1 {
		t.Fatalf("unexpected retry or error: opens=%d err=%v", existingOpens, err)
	}
	if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a replacement lock inode was created: %v", err)
	}
	if _, err := os.Stat(lock + ".preserved"); err != nil {
		t.Fatal(err)
	}
}
