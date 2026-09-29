package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncTransactionCaptureRejectsStaleStackSnapshot(t *testing.T) {
	f := newSyncTransactionFixture(t)
	stack, err := LoadStack(f.feature)
	if err != nil {
		t.Fatal(err)
	}
	live := stack
	live.Branches = append(live.Branches, StackEntry{Name: "appeared", Base: "main"})
	if err := SaveStack(f.feature, live); err != nil {
		t.Fatal(err)
	}
	_, err = CaptureSyncTransaction(SyncTransactionBeginInput{
		FeaturePath: f.feature, Feature: "feature", Mode: ModeExternal,
		WorkspaceRepoRoot: f.repo, Stack: stack, Selected: []string{"entry"},
		EntryRepoDirs: map[string]string{"entry": f.repo},
	})
	if err == nil || !strings.Contains(err.Error(), "stale selection or plan") {
		t.Fatalf("stale stack snapshot was accepted: %v", err)
	}
}

func TestSyncTransactionBirthPreflightsEveryHolder(t *testing.T) {
	f := newSyncTransactionFixture(t)
	f.newSnapshot(t)
	if err := os.WriteFile(f.repo+"/topic", []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PreflightSyncTransactionBirth(f.tx); err == nil || !strings.Contains(err.Error(), "tracked modifications") {
		t.Fatalf("dirty birth holder was accepted: %v", err)
	}
}

func TestSyncTransactionPendingContextChangeNeedsNativeEvidence(t *testing.T) {
	f := newSyncTransactionFixture(t)
	if err := BeginSyncGitAction(f.tx, f.repo, "rebase", "entry", f.repo,
		[]string{"refs/heads/topic"}, f.save); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, f.repo, "switch", "--detach", "HEAD")
	detached := gitInTest(t, f.repo, "rev-parse", "HEAD")
	f.reload(t)
	if err := ReconcileSyncGitAction(f.tx, f.save); err == nil || !strings.Contains(err.Error(), "native transition evidence") {
		t.Fatalf("operator checkout change was adopted: %v", err)
	}
	if got := gitInTest(t, f.repo, "rev-parse", "HEAD"); got != detached {
		t.Fatal("reconciliation undid the operator checkout choice")
	}
	if _, _, code, _ := syncTransactionGit(f.repo, nil, "symbolic-ref", "-q", "--short", "HEAD"); code == 0 {
		t.Fatal("operator detached HEAD was reattached")
	}
}

func TestSyncTransactionValidatorMutationIsPreserved(t *testing.T) {
	f := newSyncTransactionFixture(t)
	if err := BeginSyncValidation(f.tx, f.repo, "entry", f.repo, f.save); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, f.repo, "commit", "--allow-empty", "-qm", "validator mutation")
	foreign := gitInTest(t, f.repo, "rev-parse", "topic")
	if err := CompleteSyncValidation(f.tx, nil, f.save); err == nil || !strings.Contains(err.Error(), "recover manually") {
		t.Fatalf("validator mutation was accepted: %v", err)
	}
	f.reload(t)
	if err := AbortSyncTransaction(f.tx, f.save, nil); err == nil || !strings.Contains(err.Error(), "recover manually") {
		t.Fatalf("abort guessed how to undo validator mutation: %v", err)
	}
	if got := gitInTest(t, f.repo, "rev-parse", "topic"); got != foreign {
		t.Fatal("validator-created ref movement was erased")
	}
}

func TestSyncTransactionPublicationRetryKeepsRecordedSource(t *testing.T) {
	f := newSyncTransactionFixture(t)
	intent, err := SyncPreparePublication(f.tx, f.repo, "entry", "topic")
	if err != nil {
		t.Fatal(err)
	}
	if intent.SourceSHA != f.after {
		t.Fatalf("publication source=%s want %s", intent.SourceSHA, f.after)
	}
	if err := SyncBeginPublication(f.tx, intent, f.save); err != nil {
		t.Fatal(err)
	}
	if err := SyncFinishPublicationAttempt(f.tx, "entry", os.ErrDeadlineExceeded, f.save); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, f.repo, "commit", "--allow-empty", "-qm", "foreign advance")
	if _, err := SyncPreparePublication(f.tx, f.repo, "entry", "topic"); err == nil {
		t.Fatal("publication retry accepted a foreign local branch advance")
	}
	if got := f.tx.Publication.Intents[0].SourceSHA; got != f.after {
		t.Fatalf("recorded publication source drifted to %s", got)
	}
}

func TestSyncTransactionSentinelRestorationIsExclusive(t *testing.T) {
	for _, scenario := range []string{"absent", "appeared", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionalLoadFixture(t)
			payload := f.externalState(t)
			path := SyncStatePath(f.featurePath)
			preserved := []byte("operator evidence\n")
			target := filepath.Join(t.TempDir(), "other-state")
			switch scenario {
			case "appeared":
				previous := SyncStateIOFault
				SyncStateIOFault = func(op, requested string) error {
					if op == SyncIOWriteSentinel && requested == path {
						return os.WriteFile(path, preserved, 0600)
					}
					return nil
				}
				t.Cleanup(func() { SyncStateIOFault = previous })
			case "symlink":
				if err := os.WriteFile(target, preserved, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			err := RestoreTransactionalSyncSentinel(f.featurePath, payload)
			if scenario == "absent" {
				if err != nil {
					t.Fatal(err)
				}
				sentinel, err := LoadTransactionalSyncSentinel(f.featurePath)
				if err != nil || sentinel.FailedBranch != payload.Marker {
					t.Fatalf("restored marker was incomplete: %+v %v", sentinel, err)
				}
			} else {
				if err == nil {
					t.Fatal("restoration overwrote an occupied recovery path")
				}
				data, err := os.ReadFile(path)
				if err != nil || string(data) != string(preserved) {
					t.Fatalf("existing recovery evidence changed: %v", err)
				}
				if scenario == "symlink" {
					if link, err := os.Readlink(path); err != nil || link != target {
						t.Fatalf("restoration replaced symlink: %s %v", link, err)
					}
					if _, err := LoadTransactionalSyncSentinel(f.featurePath); err == nil {
						t.Fatal("transactional sentinel loader followed a symlink")
					}
				}
			}
		})
	}
}
