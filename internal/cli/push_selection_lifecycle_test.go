package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

func publicationLegacyLayout(t *testing.T, f *publicationFixture) internal.Workspace {
	t.Helper()
	legacy := filepath.Join(f.root, "feature")
	if err := os.Rename(f.featurePath, legacy); err != nil {
		t.Fatal(err)
	}
	f.featurePath = legacy
	return internal.Workspace{Mode: internal.ModeCheckout, RepoRoot: f.repo, MetadataRoot: f.root}
}

func publicationMigrate(ws internal.Workspace, all bool) error {
	if !all {
		return internal.MigrateFeatureLayout(ws, "feature")
	}
	result := internal.MigrateAllFeatures(ws)
	if len(result.Errors) != 0 {
		return errors.New(strings.Join(result.Errors, "; "))
	}
	return nil
}

func TestScopedPushPendingCheckoutMigrationExclusion(t *testing.T) {
	for _, all := range []bool{false, true} {
		for _, guard := range []string{"missing", "stale", "late-state"} {
			t.Run(fmt.Sprintf("all=%v/%s", all, guard), func(t *testing.T) {
				f := newPublicationFixture(t, internal.ModeCheckout)
				ws := publicationLegacyLayout(t, f)
				restore := rejectPushOf(t, f.remote, "B")
				defer restore()
				if _, _, err := runPublication(t, "--only", "B"); err == nil {
					t.Fatal("expected genuine pending checkout publication")
				}
				state := f.state(t)
				statePath := internal.ScopedPushStatePath(f.location())
				if guard == "stale" {
					body, err := yaml.Marshal(internal.CheckoutMutationLock{
						PID: spawnDeadPID(t), Created: "2026-10-30T10:00:00Z", Token: state.Plan.Token,
						Feature: "feature", Operation: "publication", StatePath: statePath,
					})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(internal.CheckoutMutationLockPath(ws.CheckoutStateDir()), body, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				before := publicationFiles(t, f.featurePath, statePath, f.remote)
				if guard == "late-state" {
					if err := os.Rename(statePath, statePath+".saved"); err != nil {
						t.Fatal(err)
					}
					restored := false
					internal.ScopedPushCheckpoint = func(step string, _ int) error {
						if step == "after-checkout-metadata-guard" && !restored {
							restored = true
							return os.Rename(statePath+".saved", statePath)
						}
						return nil
					}
					t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
				}
				if err := publicationMigrate(ws, all); err == nil {
					t.Fatal("layout migration bypassed pending publication")
				}
				internal.ScopedPushCheckpoint = nil
				if !reflect.DeepEqual(before, publicationFiles(t, f.featurePath, statePath, f.remote)) {
					t.Fatal("refused migration moved the feature or changed pending evidence")
				}
				if _, err := os.Lstat(filepath.Join(f.root, "features", "feature")); !os.IsNotExist(err) {
					t.Fatal("refused migration created a destination feature")
				}
			})
		}
	}
}

func TestScopedPushMigrationHoldsExclusionThroughMovement(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeCheckout)
			ws := publicationLegacyLayout(t, f)
			called := false
			internal.ScopedPushCheckpoint = func(step string, _ int) error {
				if step == "before-checkout-feature-move" {
					called = true
					if _, _, err := runPublication(t, "--only", "B"); err == nil || !strings.Contains(err.Error(), "mutation lock") {
						t.Fatalf("publication entered an active layout move: %v", err)
					}
				}
				return nil
			}
			t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
			if err := publicationMigrate(ws, all); err != nil || !called {
				t.Fatalf("migration/exclusion: called=%v err=%v", called, err)
			}
			if got := remoteRefs(t, f.remote); got != "refs/heads/main" {
				t.Fatal("racing publication contacted the remote")
			}
		})
	}
}

func TestScopedPushCheckoutTerminalReceiptRetirementAndNameReuse(t *testing.T) {
	for _, action := range []string{"rename", "delete", "migrate-one", "migrate-all"} {
		t.Run(action, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeCheckout)
			var ws internal.Workspace
			if strings.HasPrefix(action, "migrate") {
				ws = publicationLegacyLayout(t, f)
			}
			if _, _, err := runPublication(t, "--only", "B"); err != nil {
				t.Fatal(err)
			}
			state := f.state(t)
			path := internal.ScopedPushStatePath(f.location())
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			// Keep the old directory inode alive across delete/recreation.
			held, err := os.Open(f.featurePath)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close() //nolint:errcheck
			switch action {
			case "rename":
				_, _, err = runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil)
			case "delete":
				_, _, err = runManagedCobra(t, deleteCmd(), []string{"feature"}, nil)
			default:
				err = publicationMigrate(ws, action == "migrate-all")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("terminal receipt still reserves its live feature name")
			}
			historyPath := filepath.Join(f.root, "state", "publication-history", state.Plan.Token+".json")
			history, err := os.ReadFile(historyPath)
			if err != nil || !bytes.Equal(body, history) {
				t.Fatal("retirement did not preserve exact historical bytes")
			}
			info, err := os.Stat(historyPath)
			if err != nil || !os.SameFile(original, info) {
				t.Fatal("retirement lost the proven receipt inode")
			}
			f.featurePath = filepath.Join(f.root, "features", "feature")
			if err := os.MkdirAll(f.featurePath, 0o755); err != nil {
				t.Fatal(err)
			}
			gitRun(t, f.repo, "branch", "replacement", "B")
			f.stack = internal.Stack{Branches: []internal.StackEntry{{Name: "replacement", Base: "main"}}}
			f.saveStack(t)
			if out, stderr, err := runPublication(t, "--only", "replacement"); err != nil {
				t.Fatalf("retired history blocked name reuse: %v\n%s\n%s", err, out, stderr)
			}
			after, err := os.ReadFile(historyPath)
			if err != nil || !bytes.Equal(after, body) {
				t.Fatal("fresh publication rewrote retained history")
			}
		})
	}
}

func TestScopedPushCheckoutReceiptRetirementPreservesForeignEvidence(t *testing.T) {
	for _, conflict := range []string{"source-copy", "destination-copy", "history-copy"} {
		t.Run(conflict, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeCheckout)
			if _, _, err := runPublication(t, "--only", "B"); err != nil {
				t.Fatal(err)
			}
			state := f.state(t)
			path := internal.ScopedPushStatePath(f.location())
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch conflict {
			case "source-copy":
				if err := os.Rename(f.featurePath, f.featurePath+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.featurePath, 0o755); err != nil {
					t.Fatal(err)
				}
				f.saveStack(t)
			case "destination-copy":
				if err := os.WriteFile(filepath.Join(f.root, "state", "renamed-push.v1.json"), body, 0o600); err != nil {
					t.Fatal(err)
				}
			case "history-copy":
				dir := filepath.Join(f.root, "state", "publication-history")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, state.Plan.Token+".json"), body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := publicationFiles(t, f.featurePath, filepath.Join(f.root, "state"))
			if conflict == "destination-copy" {
				_, _, err = runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil)
			} else {
				_, _, err = runManagedCobra(t, deleteCmd(), []string{"feature"}, nil)
			}
			if err == nil {
				t.Fatal("foreign publication evidence was adopted or retired")
			}
			if !reflect.DeepEqual(before, publicationFiles(t, f.featurePath, filepath.Join(f.root, "state"))) {
				t.Fatal("refusal changed a foreign receipt or feature")
			}
		})
	}
}

func TestScopedPushCheckoutReceiptRetirementRepeatsDurability(t *testing.T) {
	for _, step := range []string{"after-terminal-receipt-link", "after-terminal-receipt-remove"} {
		t.Run(step, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeCheckout)
			if _, _, err := runPublication(t, "--only", "B"); err != nil {
				t.Fatal(err)
			}

			state := f.state(t)
			path := internal.ScopedPushStatePath(f.location())
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			fired := false
			internal.ScopedPushCheckpoint = func(current string, _ int) error {
				if current == step && !fired {
					fired = true
					return errors.New("interrupted receipt retirement")
				}
				return nil
			}
			t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
			if _, _, err := runManagedCobra(t, deleteCmd(), []string{"feature"}, nil); err == nil || !fired {
				t.Fatal("retirement fault did not stop feature deletion")
			}
			if _, err := os.Stat(f.featurePath); err != nil {
				t.Fatal("feature moved before receipt retirement was durable")
			}
			internal.ScopedPushCheckpoint = nil
			if _, _, err := runManagedCobra(t, deleteCmd(), []string{"feature"}, nil); err != nil {
				t.Fatalf("owned receipt retirement could not resume: %v", err)
			}
			history, err := os.ReadFile(filepath.Join(f.root, "state", "publication-history", state.Plan.Token+".json"))
			if err != nil || !bytes.Equal(history, body) {
				t.Fatal("interrupted retirement lost history")
			}
		})
	}
}

func TestScopedPushSyncUsesAvailableSelectedContextWithMissingAnchor(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeExternal)
	gitRun(t, f.repo, "push", "origin", "B")
	record := publicationFollowup(t, f, "B", f.oids["B"], f.oids["A"], true)
	gitRun(t, f.repo, "worktree", "move", f.worktree("A"), f.worktree("A")+".away")
	restore := rejectPushOf(t, f.remote, "D")
	out, stderr, exit := runSync(t, "feature", "--from", "A", "--local-only", "--no-fetch", "--push")
	if exit == 0 || !strings.Contains(out, "B (pushed)") || strings.Contains(stderr, "chdir") {
		t.Fatalf("missing anchor blocked the available selected publisher: exit=%d\n%s\n%s", exit, out, stderr)
	}
	restore()
	guard, err := internal.ReadSyncRunGuard(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	guard.PID = spawnDeadPID(t)
	body, err := yaml.Marshal(guard)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(internal.SyncRunGuardPath(f.featurePath), body, 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, exit = runSync(t, "feature", "--continue")
	if exit != 0 {
		t.Fatalf("publication retry reused the missing anchor context: exit=%d\n%s\n%s", exit, out, stderr)
	}
	after, err := internal.LoadReparentRemoteRecord(f.location().ReparentLocation())
	if err != nil || after == nil || after.Entries[1] != record.Entries[1] || after.CreatedAt != record.CreatedAt {
		t.Fatal("selected sync changed or probed the unavailable neighboring record")
	}
	if got := gitOutput(t, f.repo, "rev-parse", "A"); got != f.oids["A"] {
		t.Fatal("local-only sync advanced its missing anchor")
	}
}

func TestCheckoutMetadataRenameCompensatesPostMoveSyncFailure(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeCheckout)
	if err := os.Mkdir(filepath.Join(f.featurePath, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := "version: 1\nspaces:\n  - name: notes\n    kind: docs\n    path: features/feature/notes\n    feature: feature\n    added_at: 2026-10-30T11:00:00Z\n"
	writeSpaces(t, f.root, registry)
	original, err := os.Stat(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.root, "features", "renamed")
	injected := errors.New("post-syscall directory sync failure")
	fired, registryWrites := false, 0
	internal.SpacesSaveHook = func(string) error { registryWrites++; return nil }
	internal.ScopedPushCheckpoint = func(step string, _ int) error {
		if step == "after-checkout-feature-rename-before-sync" && !fired {
			fired = true
			info, err := os.Stat(target)
			if err != nil || !os.SameFile(original, info) {
				t.Fatal("fault did not occur after the actual rename syscall")
			}
			return injected
		}
		return nil
	}
	t.Cleanup(func() { internal.ScopedPushCheckpoint = nil; internal.SpacesSaveHook = nil })
	_, _, err = runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil)
	if !errors.Is(err, injected) || !fired {
		t.Fatalf("post-move failure was swallowed: %v", err)
	}
	info, err := os.Stat(f.featurePath)
	if err != nil || !os.SameFile(original, info) {
		t.Fatal("already-moved feature was not safely compensated")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("compensated destination still exists")
	}
	body, err := os.ReadFile(internal.SpacesFilePath(f.root))
	if err != nil || string(body) != registry || registryWrites != 0 {
		t.Fatal("registry disagrees with the compensated directory")
	}
	stateDir := filepath.Join(f.root, "state")
	pending, err := filepath.Glob(filepath.Join(stateDir, ".checkout-metadata-*"))
	if err != nil || len(pending) != 0 {
		t.Fatalf("settled compensation retained pending evidence: %v %v", pending, err)
	}
	internal.ScopedPushCheckpoint = nil
	if _, _, err := runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(internal.SpacesFilePath(f.root))
	if err != nil || !strings.Contains(string(body), "features/renamed/notes") || !strings.Contains(string(body), "feature: renamed") {
		t.Fatal("successful retry did not publish coherent registry paths")
	}
}

func TestCheckoutMetadataMigrationRollsBackCurrentMovedItem(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeCheckout)
			ws := publicationLegacyLayout(t, f)
			names := []string{"feature"}
			if all {
				names = append(names, "second")
				if err := os.Mkdir(filepath.Join(f.root, "second"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			original := map[string]os.FileInfo{}
			for _, name := range names {
				info, err := os.Stat(filepath.Join(f.root, name))
				if err != nil {
					t.Fatal(err)
				}
				original[name] = info
			}
			shared := filepath.Join(f.repo, "shared-notes")
			if err := os.Mkdir(shared, 0o700); err != nil {
				t.Fatal(err)
			}
			registry := fmt.Sprintf("version: 1\nspaces:\n  - name: notes\n    kind: docs\n    path: %q\n    feature: feature\n    added_at: 2026-10-30T11:00:00Z\n", shared)
			writeSpaces(t, f.root, registry)
			index := len(names) - 1
			fired := false
			internal.ScopedPushCheckpoint = func(step string, move int) error {
				if step == "after-checkout-feature-rename-before-sync" && move == index && !fired {
					fired = true
					moved, err := os.Stat(filepath.Join(f.root, "features", names[index]))
					if err != nil || !os.SameFile(original[names[index]], moved) {
						t.Fatal("current failed item was not actually moved")
					}
					return errors.New("post-move sync failed")
				}
				return nil
			}
			t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
			if err := publicationMigrate(ws, all); err == nil || !fired {
				t.Fatalf("migration failed to surface the injected sync error: %v", err)
			}
			for _, name := range names {
				info, err := os.Stat(filepath.Join(f.root, name))
				if err != nil || !os.SameFile(original[name], info) {
					t.Fatalf("rollback omitted moved item %s", name)
				}
				if _, err := os.Lstat(filepath.Join(f.root, "features", name)); !os.IsNotExist(err) {
					t.Fatalf("rollback left destination %s", name)
				}
			}
			body, err := os.ReadFile(internal.SpacesFilePath(f.root))
			if err != nil || string(body) != registry {
				t.Fatal("migration changed the registry during compensation")
			}
			internal.ScopedPushCheckpoint = nil
			if err := publicationMigrate(ws, all); err != nil {
				t.Fatalf("settled migration could not retry: %v", err)
			}
		})
	}
}

func TestCheckoutMetadataMovePreservesForeignPathAndRecoveryEvidence(t *testing.T) {
	f := newPublicationFixture(t, internal.ModeCheckout)
	original, err := os.Stat(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.root, "features", "renamed")
	registry := "version: 1\nspaces: []\n"
	writeSpaces(t, f.root, registry)
	fired := false
	internal.ScopedPushCheckpoint = func(step string, _ int) error {
		if step == "after-checkout-feature-rename-before-sync" && !fired {
			fired = true
			if err := os.Mkdir(f.featurePath, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(f.featurePath, "foreign"), []byte("must survive"), 0o600); err != nil {
				return err
			}
			return errors.New("post-move failure with reoccupied source")
		}
		return nil
	}
	t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
	_, _, err = runManagedCobra(t, renameCmd(), []string{"feature", "feature", "renamed"}, nil)
	if err == nil || !fired {
		t.Fatal("foreign compensation target did not refuse")
	}
	foreign, err := os.ReadFile(filepath.Join(f.featurePath, "foreign"))
	if err != nil || string(foreign) != "must survive" {
		t.Fatal("compensation overwrote the foreign path")
	}
	info, err := os.Stat(target)
	if err != nil || !os.SameFile(info, original) {
		t.Fatal("unresolved original feature inode was lost")
	}
	stateDir := filepath.Join(f.root, "state")
	evidence, err := filepath.Glob(filepath.Join(stateDir, ".checkout-metadata-*"))
	if err != nil || len(evidence) != 1 {
		t.Fatalf("missing explicit movement evidence: %v %v", evidence, err)
	}
	var recorded struct {
		Kind           string `json:"kind"`
		Completed      bool   `json:"completed"`
		RegistryBefore []byte `json:"registry_before"`
	}
	body, err := os.ReadFile(evidence[0])
	if err != nil || json.Unmarshal(body, &recorded) != nil || recorded.Kind != "tws-checkout-metadata-moves" ||
		recorded.Completed || string(recorded.RegistryBefore) != registry {
		t.Fatal("unresolved evidence did not preserve the registry and movement state")
	}
	if _, err := os.Stat(internal.CheckoutMutationLockPath(stateDir)); err != nil {
		t.Fatal("unresolved effects released their reservation")
	}
	if err := os.Remove(internal.CheckoutMutationLockPath(stateDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := internal.AcquireCheckoutMutationLock(stateDir, strings.Repeat("e", 32), "other", "sync",
		filepath.Join(stateDir, "other-checkout-sync.yaml")); err == nil {
		t.Fatal("missing lock allowed unresolved movement evidence to be bypassed")
	}
}

func TestCheckoutMetadataMoveRetainsGuardAfterEvidenceReadFailure(t *testing.T) {
	for _, replacement := range []string{"missing", "fifo"} {
		t.Run(replacement, func(t *testing.T) {
			f := newPublicationFixture(t, internal.ModeCheckout)
			ws := publicationLegacyLayout(t, f)
			if err := os.Mkdir(filepath.Join(f.root, "second"), 0o700); err != nil {
				t.Fatal(err)
			}
			original, err := os.Stat(f.featurePath)
			if err != nil {
				t.Fatal(err)
			}
			stateDir := filepath.Join(f.root, "state")
			fired := false
			var evidencePath, preserved string
			internal.ScopedPushCheckpoint = func(step string, index int) error {
				if step != "after-checkout-metadata-evidence-write" || index != 1 || fired {
					return nil
				}
				fired = true
				paths, err := filepath.Glob(filepath.Join(stateDir, ".checkout-metadata-*"))
				if err != nil || len(paths) != 1 {
					t.Fatal("second move intent was not published")
				}
				evidencePath = paths[0]
				preserved = filepath.Join(stateDir, "preserved-movement-evidence")
				if err := os.Rename(evidencePath, preserved); err != nil {
					return err
				}
				if replacement == "fifo" {
					return unix.Mkfifo(evidencePath, 0o600)
				}
				return nil
			}
			t.Cleanup(func() { internal.ScopedPushCheckpoint = nil })
			start := time.Now()
			if err := publicationMigrate(ws, true); err == nil || !fired {
				t.Fatalf("unobserved replacement was accepted: %v", err)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatal("special movement evidence blocked instead of refusing")
			}
			info, err := os.Stat(filepath.Join(f.root, "features", "feature"))
			if err != nil || !os.SameFile(original, info) {
				t.Fatal("already-moved directory was lost after evidence read failure")
			}
			if _, err := os.Stat(filepath.Join(f.root, "second")); err != nil {
				t.Fatal("a move ran without its verified durable intent")
			}
			if _, err := os.Stat(internal.CheckoutMutationLockPath(stateDir)); err != nil {
				t.Fatal("failed journal read discarded the reservation for an unresolved move")
			}
			if _, err := os.Stat(preserved); err != nil {
				t.Fatal("preserved original evidence was removed")
			}
			info, err = os.Lstat(evidencePath)
			if replacement == "fifo" {
				if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
					t.Fatal("foreign replacement was overwritten or removed")
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("missing evidence was implicitly recreated")
			}
		})
	}
}
