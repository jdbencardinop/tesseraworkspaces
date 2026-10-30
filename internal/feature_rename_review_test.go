package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFeatureRenameReviewMovingRootDependencyRefuses(t *testing.T) {
	f := newFeatureRenameFixture(t)
	path := f.worktree(t, f.repo, "entry", "entry", false)
	marker := featureRenameTestImage(t, filepath.Join(path, ".git"))
	alias := filepath.Join(f.root, "workspace-alias")
	if err := os.Symlink(f.old+"/..", alias); err != nil {
		t.Fatal(err)
	}
	f.ws.MetadataRoot = alias
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil || !strings.Contains(err.Error(), "depends on a moving feature") {
		t.Fatalf("moving root dependency accepted: %v", err)
	}
	if err := marker.verifyAt(marker.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
		t.Fatal(err)
	}
	if pending, err := featureRenamePendingPlans(alias); err != nil || len(pending) != 0 {
		t.Fatalf("refusal published invalid evidence: %v %v", pending, err)
	}
}

func TestFeatureRenameReviewJournalMovingDependencyRefuses(t *testing.T) {
	f := newFeatureRenameFixture(t)
	f.worktree(t, f.repo, "entry", "entry", false)
	alias := filepath.Join(f.root, "workspace-alias")
	if err := os.Symlink(f.ws.MetadataRoot, alias); err != nil {
		t.Fatal(err)
	}
	ws := f.ws
	ws.MetadataRoot = alias
	FeatureRenameCheckpoint = func(step, _ string) error {
		if step == "before-move" {
			return ErrFeatureRenameInterrupted
		}
		return nil
	}
	if _, err := RenameExternalFeature(ws, "old", "new", "", nil); !errors.Is(err, ErrFeatureRenameInterrupted) {
		t.Fatal(err)
	}
	FeatureRenameCheckpoint = nil
	plans, err := featureRenamePendingPlans(f.ws.MetadataRoot)
	if err != nil || len(plans) != 1 || len(plans[0].Aliases) != 1 {
		t.Fatalf("pending fixture: %v %v", plans, err)
	}
	plan := plans[0]
	plan.Aliases[0].Directories = append(plan.Aliases[0].Directories, plan.Feature)
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	featureRenameTestWrite(t, filepath.Join(plan.Operation.Path, "journal.json"), data, 0o600)
	for _, action := range []string{"continue", "abort"} {
		if _, err := RenameExternalFeature(ws, "old", "new", action, nil); err == nil || !strings.Contains(err.Error(), "depends on a moving feature") {
			t.Fatalf("malicious dependency not rejected by journal validation: %v", err)
		}
	}
	if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameReviewRawCommonTraversalRefusesBeforeJournal(t *testing.T) {
	f := newFeatureRenameFixture(t)
	path := f.worktree(t, f.repo, "entry", "entry", false)
	admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	raw := f.old + "/../../repo/.git\n"
	featureRenameTestWrite(t, filepath.Join(admin, "commondir"), []byte(raw), 0o644)
	record := featureRenameTestImage(t, filepath.Join(admin, "commondir"))
	featureRenameTestGit(t, path, "rev-parse", "--verify", "HEAD")
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil || !strings.Contains(err.Error(), "relocation-dependent Git record") {
		t.Fatalf("raw commondir traversal accepted: %v", err)
	}
	if err := record.verifyAt(record.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
		t.Fatal(err)
	}
	if pending, err := featureRenamePendingPlans(f.ws.MetadataRoot); err != nil || len(pending) != 0 {
		t.Fatalf("unsupported pointer poisoned admission: %v %v", pending, err)
	}
	held, err := AcquireExternalFeatureAdmission(f.ws.MetadataRoot, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameReviewRawCommonSymlinkTraversalRefuses(t *testing.T) {
	f := newFeatureRenameFixture(t)
	path := f.worktree(t, f.repo, "entry", "entry", false)
	admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(f.ws.MetadataRoot, "via-old")
	if err := os.Symlink(f.old, alias); err != nil {
		t.Fatal(err)
	}
	raw := alias + "/../../repo/.git\n"
	featureRenameTestWrite(t, filepath.Join(admin, "commondir"), []byte(raw), 0o644)
	var images []featureRenameFile
	for _, file := range []string{filepath.Join(path, ".git"), filepath.Join(admin, "gitdir"), filepath.Join(admin, "commondir")} {
		images = append(images, featureRenameTestImage(t, file))
	}
	featureRenameTestGit(t, path, "rev-parse", "--verify", "HEAD")
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil {
		t.Fatal("commondir traversal through an unrecorded symlink was accepted")
	}
	for _, image := range images {
		if err := image.verifyAt(image.Path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
		t.Fatalf("commondir refusal moved the original worktree: %v", err)
	}
	if _, err := os.Lstat(f.new); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commondir refusal created the new binding: %v", err)
	}
	if pending, err := featureRenamePendingPlans(f.ws.MetadataRoot); err != nil || len(pending) != 0 {
		t.Fatalf("unsupported raw symlink poisoned admission: %v %v", pending, err)
	}
	held, err := AcquireExternalFeatureAdmission(f.ws.MetadataRoot, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameReviewRawForwardSymlinkTraversalRefuses(t *testing.T) {
	f := newFeatureRenameFixture(t)
	path := f.worktree(t, f.repo, "entry", "entry", false)
	admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(f.ws.MetadataRoot, "via-old")
	if err := os.Symlink(f.old, alias); err != nil {
		t.Fatal(err)
	}
	raw := "gitdir: " + alias + "/../../repo/.git/worktrees/" + filepath.Base(admin) + "\n"
	featureRenameTestWrite(t, filepath.Join(path, ".git"), []byte(raw), 0o644)
	marker := featureRenameTestImage(t, filepath.Join(path, ".git"))
	featureRenameTestGit(t, path, "rev-parse", "--verify", "HEAD")
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil {
		t.Fatal("forward traversal through an unrecorded symlink was accepted")
	}
	if err := marker.verifyAt(marker.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
		t.Fatal(err)
	}
	if pending, err := featureRenamePendingPlans(f.ws.MetadataRoot); err != nil || len(pending) != 0 {
		t.Fatalf("unsupported forward pointer poisoned admission: %v %v", pending, err)
	}
}

func TestFeatureRenameReviewRawCommonSymlinkJournalRefuses(t *testing.T) {
	f := newFeatureRenameFixture(t)
	f.worktree(t, f.repo, "entry", "entry", false)
	FeatureRenameCheckpoint = func(step, _ string) error {
		if step == "before-move" {
			return ErrFeatureRenameInterrupted
		}
		return nil
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); !errors.Is(err, ErrFeatureRenameInterrupted) {
		t.Fatal(err)
	}
	FeatureRenameCheckpoint = nil
	plans, err := featureRenamePendingPlans(f.ws.MetadataRoot)
	if err != nil || len(plans) != 1 {
		t.Fatalf("pending fixture: %v %v", plans, err)
	}
	plan := plans[0]
	alias := filepath.Join(f.ws.MetadataRoot, "via-old")
	if err := os.Symlink(f.old, alias); err != nil {
		t.Fatal(err)
	}
	commonFile := plan.Worktrees[0].CommonFile.Path
	featureRenameTestWrite(t, commonFile, []byte(alias+"/../../repo/.git\n"), 0o644)
	plan.Worktrees[0].CommonFile = featureRenameTestImage(t, commonFile)
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	featureRenameTestWrite(t, filepath.Join(plan.Operation.Path, "journal.json"), data, 0o600)
	// Validate the header directly: event hashes must not be the only thing
	// rejecting an otherwise self-consistent unsupported raw record.
	if err := plan.validate(f.ws.MetadataRoot, plan.ID); err == nil {
		t.Fatal("journal validation admitted an unrecorded raw symlink traversal")
	}
	entries, err := os.ReadDir(plan.Operation.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "journal.json" {
			continue
		}
		eventPath := filepath.Join(plan.Operation.Path, entry.Name())
		file := featureRenameTestImage(t, eventPath)
		var event featureRenameEvent
		if err := featureRenameReadJSON(file.Data, &event); err != nil {
			t.Fatal(err)
		}
		event.PlanHash = digestBytes(data)
		image, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		featureRenameTestWrite(t, eventPath, image, 0o600)
	}
	if _, err := loadFeatureRename(f.ws.MetadataRoot, plan.ID); err == nil || !strings.Contains(err.Error(), "unrecorded traversed symlink") {
		t.Fatalf("hash-consistent journal bypassed raw component validation: %v", err)
	}
	for _, action := range []string{"continue", "abort"} {
		if _, err := RenameExternalFeature(f.ws, "old", "new", action, nil); err == nil {
			t.Fatalf("%s admitted a malformed raw record", action)
		}
	}
	if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameReviewRawCommonFrozenAliasesRemainSupported(t *testing.T) {
	for _, kind := range []string{"workspace-parent", "ambient"} {
		t.Run(kind, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			path := f.worktree(t, f.repo, "entry", "entry", false)
			admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(f.root, "known-alias")
			target, selected, raw := f.ws.MetadataRoot, alias, alias+"/../repo/.git\n"
			if kind == "ambient" {
				target, selected, raw = f.root, filepath.Join(alias, "selected-spaces"), alias+"/repo/.git\n"
			}
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			f.ws.MetadataRoot = selected
			featureRenameTestWrite(t, filepath.Join(admin, "commondir"), []byte(raw), 0o644)
			record := featureRenameTestImage(t, filepath.Join(admin, "commondir"))
			featureRenameTestGit(t, path, "rev-parse", "--verify", "HEAD")
			if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err != nil {
				t.Fatal(err)
			}
			if err := record.verifyAt(record.Path); err != nil {
				t.Fatal(err)
			}
			if _, err := ResolveInjectWorktree(f.new, "entry"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFeatureRenameReviewRegistryCollisionPreflight(t *testing.T) {
	f := newFeatureRenameFixture(t)
	f.worktree(t, f.repo, "entry", "entry", false)
	if err := os.Mkdir(f.new, 0o755); err != nil {
		t.Fatal(err)
	}
	anchor := testAnchor(t, f.ws.MetadataRoot, ModeExternal)
	for _, scope := range []string{"old", "new"} {
		target := filepath.Join(f.root, "outside-"+scope)
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "notes", Kind: "docs", Path: target, Feature: scope}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(f.new); err != nil {
		t.Fatal(err)
	}
	registry := featureRenameTestImage(t, SpacesFilePath(f.ws.MetadataRoot))
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); err == nil {
		t.Fatal("scope collision accepted")
	}
	if err := registry.verifyAt(registry.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(featureRenameJournalRoot(f.ws.MetadataRoot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid plan created discoverable journal namespace: %v", err)
	}
	for _, action := range []string{"continue", "abort"} {
		if _, err := RenameExternalFeature(f.ws, "old", "new", action, nil); err == nil || !strings.Contains(err.Error(), "no pending") {
			t.Fatalf("invalid operation acquired recovery authority: %v", err)
		}
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "unrelated", Kind: "docs", Path: f.repo}); err != nil {
		t.Fatalf("collision refusal poisoned unrelated registry writes: %v", err)
	}
}

func TestFeatureRenameReviewRecoverySyncsVisibleJournal(t *testing.T) {
	f := newFeatureRenameFixture(t)
	f.worktree(t, f.repo, "entry", "entry", false)
	FeatureRenameCheckpoint = func(step, _ string) error {
		if step == "before-move" {
			return ErrFeatureRenameInterrupted
		}
		return nil
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); !errors.Is(err, ErrFeatureRenameInterrupted) {
		t.Fatal(err)
	}
	failed := false
	syncErr := errors.New("journal sync failure")
	FeatureRenameCheckpoint = func(step, _ string) error {
		if step == "before-journal-file-sync" {
			failed = true
			return syncErr
		}
		return nil
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "continue", nil); !errors.Is(err, syncErr) || !failed {
		t.Fatalf("visible journal bytes were used without a durability barrier: failed=%v err=%v", failed, err)
	}
	FeatureRenameCheckpoint = nil
	if _, err := ResolveInjectWorktree(f.old, "entry"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(f.new); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed journal sync allowed movement: %v", err)
	}
	if _, err := RenameExternalFeature(f.ws, "old", "new", "abort", nil); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureRenameReviewJournalSyncRefusesFileSwitch(t *testing.T) {
	for _, kind := range []string{"fifo", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			FeatureRenameCheckpoint = func(step, _ string) error {
				if step == "before-move" {
					return ErrFeatureRenameInterrupted
				}
				return nil
			}
			if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); !errors.Is(err, ErrFeatureRenameInterrupted) {
				t.Fatal(err)
			}
			FeatureRenameCheckpoint = nil
			plans, err := featureRenamePendingPlans(f.ws.MetadataRoot)
			if err != nil || len(plans) != 1 {
				t.Fatalf("missing pending operation: %v %v", plans, err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "-test.run=^TestFeatureRenameReviewJournalSyncFileSwitchChild$")
			child.WaitDelay = time.Second
			child.Env = append(os.Environ(),
				"TWS_FEATURE_RENAME_SYNC_TEST_ROOT="+f.ws.MetadataRoot,
				"TWS_FEATURE_RENAME_SYNC_TEST_ID="+plans[0].ID,
				"TWS_FEATURE_RENAME_SYNC_TEST_KIND="+kind,
			)
			out, err := child.CombinedOutput()
			if ctx.Err() != nil || err != nil {
				t.Fatalf("journal durability open blocked or admitted a switched file: %v %v\n%s", ctx.Err(), err, out)
			}
			path := filepath.Join(plans[0].Operation.Path, "journal.json")
			info, err := os.Lstat(path)
			if err != nil || info.Mode().IsRegular() {
				t.Fatalf("unexpected journal evidence was removed/replaced: %v", err)
			}
			if _, err := os.Stat(path + ".original"); err != nil {
				t.Fatal("original journal evidence was not retained")
			}
		})
	}
}

func TestFeatureRenameReviewJournalSyncFileSwitchChild(t *testing.T) {
	root := os.Getenv("TWS_FEATURE_RENAME_SYNC_TEST_ROOT")
	if root == "" {
		t.Skip("bounded subprocess-only fixture")
	}
	tx, err := loadFeatureRename(root, os.Getenv("TWS_FEATURE_RENAME_SYNC_TEST_ID"))
	if err != nil {
		t.Fatal(err)
	}
	switched, synced := false, false
	FeatureRenameCheckpoint = func(step, path string) error {
		if step == "before-journal-file-sync" {
			synced = true
		}
		if switched || step != "before-journal-file-open" {
			return nil
		}
		switched = true
		if err := os.Rename(path, path+".original"); err != nil {
			return err
		}
		if os.Getenv("TWS_FEATURE_RENAME_SYNC_TEST_KIND") == "fifo" {
			return unix.Mkfifo(path, 0o600)
		}
		return os.Symlink(filepath.Base(path)+".original", path)
	}
	if err := tx.syncJournalEvidence(); err == nil || !switched || synced {
		t.Fatalf("switched journal file was admitted for sync: switched=%v synced=%v err=%v", switched, synced, err)
	}
}

func TestFeatureRenameReviewReplayDurabilityBarriers(t *testing.T) {
	for _, effect := range []string{
		"move", "preserve.0", "publish.0", "preserve.1", "publish.1",
		"unpublish.1", "restore.1", "unpublish.0", "restore.0", "move-back",
		"cleanup-file.0", "cleanup.0", "cleanup.1",
	} {
		t.Run(effect, func(t *testing.T) {
			f := newFeatureRenameFixture(t)
			path := f.worktree(t, f.repo, "entry", "entry", false)
			f.registry(t)
			admin, err := resolveInjectGitDir(path, filepath.Join(path, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			kind, number, _ := strings.Cut(effect, ".")
			index, _ := strconv.Atoi(number)
			active := filepath.Join(admin, "gitdir")
			parent := admin
			if index == 1 {
				active, parent = SpacesFilePath(f.ws.MetadataRoot), f.ws.MetadataRoot
			}
			rollback := kind == "unpublish" || kind == "restore" || kind == "move-back"
			if rollback {
				FeatureRenameCheckpoint = func(step, _ string) error {
					if step == "before-complete" {
						return ErrFeatureRenameInterrupted
					}
					return nil
				}
				if _, err := RenameExternalFeature(f.ws, "old", "new", "", nil); !errors.Is(err, ErrFeatureRenameInterrupted) {
					t.Fatal(err)
				}
			}

			stopped := false
			FeatureRenameCheckpoint = func(step, path string) error {
				if stopped {
					return nil
				}
				match := false
				switch kind {
				case "move", "move-back":
					want := f.new
					if rollback {
						want = f.old
					}
					match = step == "after-rename-before-sync" && path == want
					parent = f.ws.MetadataRoot
				case "preserve", "unpublish":
					leaf := "original"
					if kind == "unpublish" {
						leaf = "published"
					}
					match = step == "after-rename-before-sync" && filepath.Base(path) == leaf &&
						strings.HasSuffix(filepath.Base(filepath.Dir(path)), "-"+number)
				case "publish":
					match = step == "after-pointer-link-before-sync."+number && path == active
				case "restore":
					match = step == "after-rename-before-sync" && path == active
				case "cleanup-file":
					match = step == "after-cleanup-remove-before-sync" && filepath.Base(path) == "cleanup-original"
					if match {
						parent = filepath.Dir(path)
					}
				case "cleanup":
					match = step == "after-cleanup-directory-remove-before-sync" && strings.HasSuffix(filepath.Base(path), "-"+number)
				}
				if match {
					stopped = true
					return ErrFeatureRenameInterrupted
				}
				return nil
			}
			recovery := ""
			if rollback {
				recovery = "abort"
			}
			if _, err := RenameExternalFeature(f.ws, "old", "new", recovery, nil); !errors.Is(err, ErrFeatureRenameInterrupted) || !stopped {
				t.Fatalf("did not stop between syscall and fsync: stopped=%v err=%v", stopped, err)
			}
			unfinished := effect
			if kind == "cleanup-file" {
				unfinished = "cleanup.0"
			}
			failSync := errors.New("directory sync refused")
			armed, failed := false, false
			FeatureRenameCheckpoint = func(step, path string) error {
				if step == "before-"+unfinished {
					armed = true
				}
				if armed && step == "before-directory-sync" && path == parent {
					failed = true
					return failSync
				}
				return nil
			}
			if _, err := RenameExternalFeature(f.ws, "old", "new", "continue", nil); !errors.Is(err, failSync) || !failed {
				t.Fatalf("visible %s bypassed its repeated durability barrier: failed=%v err=%v", effect, failed, err)
			}
			plans, err := featureRenamePendingPlans(f.ws.MetadataRoot)
			if err != nil || len(plans) != 1 {
				t.Fatalf("lost pending durability evidence: %v %v", plans, err)
			}
			tx, err := loadFeatureRename(f.ws.MetadataRoot, plans[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if tx.has(unfinished, "done") || tx.has("cleaned", "done") ||
				!strings.HasPrefix(kind, "cleanup") && tx.has("complete", "done") {
				t.Fatalf("failed fsync recorded completion of %s", unfinished)
			}
			synced := false
			armed = false
			FeatureRenameCheckpoint = func(step, path string) error {
				if step == "before-"+unfinished {
					armed = true
				}
				if armed && step == "after-directory-sync" && path == parent {
					synced = true
				}
				return nil
			}
			result, err := RenameExternalFeature(f.ws, "old", "new", "continue", nil)
			if err != nil || !synced {
				t.Fatalf("retry did not sync required parent: synced=%v err=%v", synced, err)
			}
			final := f.new
			if rollback {
				final = f.old
			}
			if result.Path != final {
				t.Fatalf("wrong recovery binding %s", result.Path)
			}
			if _, err := ResolveInjectWorktree(final, "entry"); err != nil {
				t.Fatal(err)
			}
			if err := CheckExternalFeatureRenamePending(f.ws.MetadataRoot, "old", "new"); err != nil {
				t.Fatal(fmt.Errorf("completed durability recovery remains pending: %w", err))
			}
		})
	}
}
