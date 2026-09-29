package internal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type syncTransactionFixture struct {
	repo, feature, journal string
	before, after          string
	intent                 []byte
	tx                     *SyncTransaction
}

func newSyncTransactionFixture(t *testing.T) *syncTransactionFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@test.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@test.com")
	t.Setenv("GIT_EDITOR", "true")
	root := canonicalize(t.TempDir())
	f := &syncTransactionFixture{
		repo: filepath.Join(root, "repo"), feature: filepath.Join(root, "feature"),
		journal: filepath.Join(root, "journal.yaml"),
	}
	if err := os.MkdirAll(f.repo, 0700); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, f.repo, "init", "-q", "-b", "main")
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.repo, name), []byte(name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, f.repo, "add", name)
		gitInTest(t, f.repo, "commit", "-qm", name)
	}
	write("base")
	base := gitInTest(t, f.repo, "rev-parse", "HEAD")
	gitInTest(t, f.repo, "switch", "-qc", "topic")
	write("topic")
	f.before = gitInTest(t, f.repo, "rev-parse", "HEAD")
	gitInTest(t, f.repo, "switch", "-q", "main")
	write("advance")
	gitInTest(t, f.repo, "switch", "-q", "topic")
	stack := Stack{Branches: []StackEntry{{Name: "entry", Branch: "topic", Base: "main", LastBaseSHA: base}}}
	if err := os.MkdirAll(f.feature, 0700); err != nil {
		t.Fatal(err)
	}
	if err := SaveStack(f.feature, stack); err != nil {
		t.Fatal(err)
	}
	var err error
	f.tx, err = CaptureSyncTransaction(SyncTransactionBeginInput{
		FeaturePath: f.feature, Feature: "feature", Mode: ModeExternal,
		WorkspaceRepoRoot: f.repo, Stack: stack, Selected: []string{"entry"},
		EntryRepoDirs: map[string]string{"entry": f.repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(); err != nil {
		t.Fatal(err)
	}
	if err := PinSyncTransaction(f.tx, f.save); err != nil {
		t.Fatal(err)
	}
	if err := BeginSyncGitActionWithContextRef(f.tx, f.repo, "rebase", "entry", f.repo,
		"refs/heads/topic", []string{"refs/heads/topic"}, f.save); err != nil {
		t.Fatal(err)
	}
	f.intent, err = os.ReadFile(f.journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunSyncRebaseSilent(f.tx, f.repo, "rebase", "--onto", "main", base); err != nil {
		t.Fatal(err)
	}
	f.after = gitInTest(t, f.repo, "rev-parse", "topic")
	if f.after == f.before {
		t.Fatal("fixture rebase did not move topic")
	}
	if err := CompleteSyncGitAction(f.tx, nil, f.save); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *syncTransactionFixture) save() error {
	if err := ValidateSyncTransaction(f.tx, "feature", ModeExternal); err != nil {
		return err
	}
	data, err := yaml.Marshal(f.tx)
	if err != nil {
		return err
	}
	return durableWriteFile(f.journal, data, 0600)
}

func (f *syncTransactionFixture) reload(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(f.journal)
	if err != nil {
		t.Fatal(err)
	}
	var tx SyncTransaction
	if err := yaml.Unmarshal(data, &tx); err != nil {
		t.Fatal(err)
	}
	f.tx = &tx
}

func TestSyncTransactionRollbackCrashWindows(t *testing.T) {
	for _, window := range []string{
		"rollback-started", "rollback-holder-detach-intent:",
		"rollback-holder-detach-applied:", "rollback-holder-detached:", "rollback-refs-intent:",
		"rollback-refs-applied:", "rollback-refs-restored:", "rollback-metadata-intent",
		"rollback-metadata-applied",
		"rollback-metadata-restored", "rollback-holder-restore-intent:",
		"rollback-holder-restore-applied:", "rollback-holder-restored:", "rollback-pin-delete-intent:",
		"rollback-pin-deleted:",
	} {
		t.Run(window, func(t *testing.T) {
			f := newSyncTransactionFixture(t)
			preimage, err := os.ReadFile(StackPath(f.feature))
			if err != nil {
				t.Fatal(err)
			}
			if err := SyncWriteStack(f.feature, f.tx, append(preimage, '\n'), f.save); err != nil {
				t.Fatal(err)
			}
			crash := errors.New("injected rollback crash")
			reached := false
			SyncTransactionStepHook = func(step string) error {
				if !reached && strings.HasPrefix(step, window) {
					reached = true
					return crash
				}
				return nil
			}
			t.Cleanup(func() { SyncTransactionStepHook = nil })
			if err := AbortSyncTransaction(f.tx, f.save, nil); !errors.Is(err, crash) {
				t.Fatalf("window %s: %v", window, err)
			}
			SyncTransactionStepHook = nil
			if !reached {
				t.Fatal("crash window was not executed")
			}
			f.reload(t)
			if !SyncTransactionRollingBack(f.tx) {
				t.Fatal("rollback progress must exclude forward continuation, including cleanup")
			}
			if err := AbortSyncTransaction(f.tx, f.save, nil); err != nil {
				t.Fatalf("resume abort: %v", err)
			}
			if err := AbortSyncTransaction(f.tx, f.save, nil); err != nil {
				t.Fatalf("repeat abort: %v", err)
			}
			if got := gitInTest(t, f.repo, "rev-parse", "topic"); got != f.before {
				t.Fatalf("topic=%s want preimage %s", got, f.before)
			}
			if got := gitInTest(t, f.repo, "symbolic-ref", "--short", "HEAD"); got != "topic" {
				t.Fatalf("holder remained on %s", got)
			}
			if gitInTest(t, f.repo, "status", "--porcelain") != "" {
				t.Fatal("holder contents differ after rollback")
			}
		})
	}
}

func TestSyncTransactionForeignWorkAfterPartialRollback(t *testing.T) {
	f := newSyncTransactionFixture(t)
	crash := errors.New("stop after ref restoration")
	SyncTransactionStepHook = func(step string) error {
		if strings.HasPrefix(step, "rollback-refs-restored:") {
			return crash
		}

		return nil
	}
	t.Cleanup(func() { SyncTransactionStepHook = nil })
	if err := AbortSyncTransaction(f.tx, f.save, nil); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	SyncTransactionStepHook = nil
	f.reload(t)
	tree := gitInTest(t, f.repo, "rev-parse", "topic^{tree}")
	foreign := gitInTest(t, f.repo, "commit-tree", tree, "-p", f.before, "-m", "operator work")
	gitInTest(t, f.repo, "update-ref", "refs/heads/topic", foreign, f.before)
	if err := AbortSyncTransaction(f.tx, f.save, nil); err == nil {
		t.Fatal("later user work after partial rollback was accepted")
	}
	if got := gitInTest(t, f.repo, "rev-parse", "topic"); got != foreign {
		t.Fatal("foreign work was overwritten")
	}
}

func TestSyncTransactionPendingIntentDoesNotClaimOperatorCommit(t *testing.T) {
	f := newSyncTransactionFixture(t)
	if err := BeginSyncGitActionWithContextRef(f.tx, f.repo, "rebase", "entry", f.repo,
		"refs/heads/topic", []string{"refs/heads/topic"}, f.save); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, f.repo, "commit", "--allow-empty", "-qm", "later operator commit")
	foreign := gitInTest(t, f.repo, "rev-parse", "topic")
	f.reload(t)
	if err := ReconcileSyncGitAction(f.tx, f.save); err == nil {
		t.Fatal("pending rebase intent adopted an operator commit as sync-owned work")
	}
	if err := AbortSyncTransaction(f.tx, f.save, nil); err == nil {
		t.Fatal("abort erased unowned work after rebase-intent crash")
	}
	if got := gitInTest(t, f.repo, "rev-parse", "topic"); got != foreign {
		t.Fatal("operator commit was erased")
	}
}

func TestSyncTransactionUnobservedRebaseAbort(t *testing.T) {
	f := newSyncTransactionFixture(t)
	if err := durableWriteFile(f.journal, f.intent, 0600); err != nil {
		t.Fatal(err)
	}
	f.reload(t)
	if err := AbortSyncTransaction(f.tx, f.save, nil); err != nil {
		t.Fatalf("native rebase completed before progress persistence: %v", err)
	}
	if got := gitInTest(t, f.repo, "rev-parse", "topic"); got != f.before {
		t.Fatalf("topic=%s, want %s", got, f.before)
	}
}

func TestSyncTransactionRejectsContradictoryEvidence(t *testing.T) {
	f := newSyncTransactionFixture(t)
	valid, err := yaml.Marshal(f.tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*SyncTransaction)
	}{
		{"unknown-selected", func(tx *SyncTransaction) { tx.Selected = []string{"missing"} }},
		{"wrong-pin", func(tx *SyncTransaction) { tx.Repositories[0].Refs[0].PinRef += "-foreign" }},
		{"zero-oid", func(tx *SyncTransaction) { tx.Repositories[0].Refs[0].PreimageSHA = strings.Repeat("0", 40) }},
		{"rollback-without-intent", func(tx *SyncTransaction) { tx.Phase = SyncTxnRollingBack }},
		{"publication-without-intent", func(tx *SyncTransaction) {
			tx.Publication.Results = []SyncTransactionPushResult{{Entry: "entry", Success: true}}
		}},
		{"false-restoration", func(tx *SyncTransaction) { tx.Repositories[0].RefsRestored = true }},
		{"metadata-intent", func(tx *SyncTransaction) { tx.Metadata.IntentPending = true }},
		{"unknown-action-ref", func(tx *SyncTransaction) { tx.Actions[0].AllowedRefs = []string{"refs/heads/other"} }},
		{"missing-ref-image", func(tx *SyncTransaction) { tx.Actions[0].BeforeRefs = nil }},
		{"foreign-holder-image", func(tx *SyncTransaction) { tx.Actions[0].BeforeHolders[0].Path = "/elsewhere" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tx SyncTransaction
			if err := yaml.Unmarshal(valid, &tx); err != nil {
				t.Fatal(err)
			}
			tc.edit(&tx)
			if err := ValidateSyncTransaction(&tx, "feature", ModeExternal); err == nil {
				t.Fatal("contradictory state was accepted")
			}
		})
	}
}

func TestSyncTransactionPublicationNeverRollsBack(t *testing.T) {
	f := newSyncTransactionFixture(t)
	intent, err := SyncPreparePublication(f.tx, f.repo, "entry", "topic")
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncBeginPublication(f.tx, intent, f.save); err != nil {
		t.Fatal(err)
	}
	if err := SyncFinishPublicationAttempt(f.tx, "entry", errors.New("push outcome unknown"), f.save); err != nil {
		t.Fatal(err)
	}
	f.reload(t)
	if err := AbortSyncTransaction(f.tx, f.save, nil); err == nil || !strings.Contains(err.Error(), "publication boundary") {
		t.Fatalf("publication rollback refusal: %v", err)
	}
	if got := gitInTest(t, f.repo, "rev-parse", "topic"); got != f.after {
		t.Fatal("published tip changed during refused abort")
	}
}

func TestSyncTransactionRejectsMetadataChangedAfterRollback(t *testing.T) {
	f := newSyncTransactionFixture(t)
	original, err := os.ReadFile(StackPath(f.feature))
	if err != nil {
		t.Fatal(err)
	}
	post := append(append([]byte(nil), original...), '\n')
	if err := SyncWriteStack(f.feature, f.tx, post, f.save); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop after metadata restoration")
	SyncTransactionStepHook = func(step string) error {
		if step == "rollback-metadata-restored" {
			return stop
		}
		return nil
	}
	t.Cleanup(func() { SyncTransactionStepHook = nil })
	if err := AbortSyncTransaction(f.tx, f.save, nil); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	SyncTransactionStepHook = nil
	f.reload(t)
	if err := os.WriteFile(StackPath(f.feature), post, 0600); err != nil {
		t.Fatal(err)
	}
	if err := AbortSyncTransaction(f.tx, f.save, nil); err == nil {
		t.Fatal("metadata changed after its completed rollback was claimed as restored")
	}
}

func TestSyncTransactionPinsSurviveGC(t *testing.T) {
	f := newSyncTransactionFixture(t)
	gitInTest(t, f.repo, "reflog", "expire", "--expire=now", "--all")
	gitInTest(t, f.repo, "gc", "--prune=now")
	gitInTest(t, f.repo, "cat-file", "-e", f.before+"^{commit}")
	f.reload(t)
	if err := AbortSyncTransaction(f.tx, f.save, nil); err != nil {
		t.Fatalf("rollback lost a pinned preimage to GC: %v", err)
	}
	if pins := gitInTest(t, f.repo, "for-each-ref", "--format=%(refname)", "refs/tws/sync"); pins != "" {
		t.Fatalf("completed rollback leaked pins: %s", pins)
	}
}

func TestSyncTransactionSymbolicBranchesRefused(t *testing.T) {
	f := newSyncTransactionFixture(t)
	gitInTest(t, f.repo, "symbolic-ref", "refs/heads/alias", "refs/heads/topic")
	if _, err := syncTransactionListHeads(f.repo); err == nil {
		t.Fatal("symbolic branch was accepted as direct rollback ownership")
	}
}

func (f *syncTransactionFixture) newSnapshot(t *testing.T) {
	t.Helper()
	if err := CompleteSyncTransaction(f.tx, f.save); err != nil {
		t.Fatal(err)
	}
	stack, err := LoadStack(f.feature)
	if err != nil {
		t.Fatal(err)
	}
	f.tx, err = CaptureSyncTransaction(SyncTransactionBeginInput{
		FeaturePath: f.feature, Feature: "feature", Mode: ModeExternal,
		WorkspaceRepoRoot: f.repo, Stack: stack, Selected: []string{"entry"},
		EntryRepoDirs: map[string]string{"entry": f.repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(); err != nil {
		t.Fatal(err)
	}
	if err := PinSyncTransaction(f.tx, f.save); err != nil {
		t.Fatal(err)
	}
}

func TestSyncTransactionDetachedOriginalHEADSurvivesGC(t *testing.T) {
	f := newSyncTransactionFixture(t)
	tree := gitInTest(t, f.repo, "rev-parse", "topic^{tree}")
	orphan := gitInTest(t, f.repo, "commit-tree", tree, "-p", "topic", "-m", "detached operator context")
	gitInTest(t, f.repo, "switch", "--detach", orphan)
	f.newSnapshot(t)
	if err := BeginSyncGitActionWithContextRef(f.tx, f.repo, "rebase", "entry", f.repo,
		"refs/heads/topic", []string{"refs/heads/topic"}, f.save); err != nil {
		t.Fatal(err)
	}
	if err := RunSyncRebaseSilent(f.tx, f.repo, "rebase", "main", "topic"); err != nil {
		t.Fatal(err)
	}
	if err := CompleteSyncGitAction(f.tx, nil, f.save); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, f.repo, "reflog", "expire", "--expire=now", "--all")
	gitInTest(t, f.repo, "gc", "--prune=now")
	gitInTest(t, f.repo, "cat-file", "-e", orphan+"^{commit}")
	if err := AbortSyncTransaction(f.tx, f.save, nil); err != nil {
		t.Fatal(err)
	}
	if got := gitInTest(t, f.repo, "rev-parse", "HEAD"); got != orphan {
		t.Fatalf("restored detached HEAD=%s, want %s", got, orphan)
	}
	if refs := gitInTest(t, f.repo, "for-each-ref", "--format=%(refname)", "refs/tws/sync"); refs != "" {
		t.Fatalf("snapshot pins leaked: %s", refs)
	}
}

func TestSyncTransactionCollateralRefRollback(t *testing.T) {
	f := newSyncTransactionFixture(t)
	gitInTest(t, f.repo, "branch", "collateral", "topic")
	gitInTest(t, f.repo, "switch", "main")
	if err := os.WriteFile(filepath.Join(f.repo, "main-next"), []byte("next\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, f.repo, "add", "main-next")
	gitInTest(t, f.repo, "commit", "-qm", "next base")
	gitInTest(t, f.repo, "switch", "topic")
	f.newSnapshot(t)
	allowed, err := SyncAllowedRebaseRefs(f.tx, f.repo, "refs/heads/topic", "main", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginSyncGitAction(f.tx, f.repo, "rebase", "entry", f.repo, allowed, f.save); err != nil {
		t.Fatal(err)
	}
	if err := RunSyncRebaseSilent(f.tx, f.repo, "rebase", "--update-refs", "main"); err != nil {
		t.Fatal(err)
	}
	if err := CompleteSyncGitAction(f.tx, nil, f.save); err != nil {
		t.Fatal(err)
	}
	if got := gitInTest(t, f.repo, "rev-parse", "collateral"); got == f.after {
		t.Fatal("fixture did not exercise collateral movement")
	}
	if err := AbortSyncTransaction(f.tx, f.save, nil); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"topic", "collateral"} {
		if got := gitInTest(t, f.repo, "rev-parse", ref); got != f.after {
			t.Fatalf("%s=%s, want %s", ref, got, f.after)
		}
	}
}

func TestSyncTransactionFetchDestinations(t *testing.T) {
	f := newSyncTransactionFixture(t)
	for _, tc := range []struct {
		refspec string
		allowed bool
	}{
		{"+refs/heads/*:refs/remotes/origin/*", true},
		{"+refs/tags/*:refs/tags/*", true},
		{"refs/heads/main", true},
		{"+refs/heads/main:refs/heads/main", false},
		{"+refs/heads/main:main", false},
		{"+refs/*:refs/*", false},
	} {
		gitInTest(t, f.repo, "config", "--replace-all", "remote.origin.fetch", tc.refspec)
		if err := ValidateSyncFetchDoesNotWriteLocalBranches(f.repo); (err == nil) != tc.allowed {
			t.Fatalf("refspec %s: allowed=%v err=%v", tc.refspec, tc.allowed, err)
		}
	}
}

func TestSyncTransactionMultiRepositoryRollback(t *testing.T) {
	for _, foreignWork := range []bool{false, true} {
		name := "resume-after-first-repository"
		if foreignWork {
			name = "refuse-before-any-repository"
		}
		t.Run(name, func(t *testing.T) {
			left, right := newSyncTransactionFixture(t), newSyncTransactionFixture(t)
			for _, f := range []*syncTransactionFixture{left, right} {
				if err := CompleteSyncTransaction(f.tx, f.save); err != nil {
					t.Fatal(err)
				}
				gitInTest(t, f.repo, "switch", "main")
				if err := os.WriteFile(filepath.Join(f.repo, "next-base"), []byte("next\n"), 0600); err != nil {
					t.Fatal(err)
				}
				gitInTest(t, f.repo, "add", "next-base")
				gitInTest(t, f.repo, "commit", "-qm", "next base")
				gitInTest(t, f.repo, "switch", "topic")
			}
			stack := Stack{Branches: []StackEntry{
				{Name: "entry", Branch: "topic", Base: "main", Repo: left.repo},
				{Name: "other", Branch: "topic", Base: "main", Repo: right.repo},
			}}
			if err := SaveStack(left.feature, stack); err != nil {
				t.Fatal(err)
			}
			var err error
			left.tx, err = CaptureSyncTransaction(SyncTransactionBeginInput{
				FeaturePath: left.feature, Feature: "feature", Mode: ModeExternal,
				WorkspaceRepoRoot: left.repo, Stack: stack, Selected: []string{"entry", "other"},
				EntryRepoDirs: map[string]string{"entry": left.repo, "other": right.repo},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := PinSyncTransaction(left.tx, left.save); err != nil {
				t.Fatal(err)
			}
			for i, f := range []*syncTransactionFixture{left, right} {
				entry := []string{"entry", "other"}[i]
				if err := BeginSyncGitAction(left.tx, f.repo, "rebase", entry, f.repo, []string{"refs/heads/topic"}, left.save); err != nil {
					t.Fatal(err)
				}
				if err := RunSyncRebaseSilent(left.tx, f.repo, "rebase", "main"); err != nil {
					t.Fatal(err)
				}
				if err := CompleteSyncGitAction(left.tx, nil, left.save); err != nil {
					t.Fatal(err)
				}
			}

			if foreignWork {
				leftTip := gitInTest(t, left.repo, "rev-parse", "topic")
				gitInTest(t, right.repo, "commit", "--allow-empty", "-qm", "operator work")
				rightTip := gitInTest(t, right.repo, "rev-parse", "topic")
				if err := AbortSyncTransaction(left.tx, left.save, nil); err == nil {
					t.Fatal("foreign work was not refused")
				}
				if gitInTest(t, left.repo, "rev-parse", "topic") != leftTip ||
					gitInTest(t, right.repo, "rev-parse", "topic") != rightTip {
					t.Fatal("preflight refusal modified one of the repositories")
				}
				return
			}
			stop := errors.New("stop after one repository")
			SyncTransactionStepHook = func(step string) error {
				if strings.HasPrefix(step, "rollback-refs-restored:") {
					return stop
				}
				return nil
			}
			t.Cleanup(func() { SyncTransactionStepHook = nil })
			if err := AbortSyncTransaction(left.tx, left.save, nil); !errors.Is(err, stop) {
				t.Fatal(err)
			}
			SyncTransactionStepHook = nil
			left.reload(t)
			if err := AbortSyncTransaction(left.tx, left.save, nil); err != nil {
				t.Fatal(err)
			}
			for _, f := range []*syncTransactionFixture{left, right} {
				if got := gitInTest(t, f.repo, "rev-parse", "topic"); got != f.after {
					t.Fatalf("repository %s restored %s, want %s", f.repo, got, f.after)
				}
			}
		})
	}
}

func TestSyncTransactionForwardCompletionCleanupIsNotRollback(t *testing.T) {
	for _, window := range []string{"cleanup-started", "rollback-pin-delete-intent:", "rollback-pin-deleted:"} {
		t.Run(window, func(t *testing.T) {
			f := newSyncTransactionFixture(t)
			stop := errors.New("stop during forward cleanup")
			SyncTransactionStepHook = func(step string) error {
				if strings.HasPrefix(step, window) {
					return stop
				}
				return nil
			}
			t.Cleanup(func() { SyncTransactionStepHook = nil })
			if err := CompleteSyncTransaction(f.tx, f.save); !errors.Is(err, stop) {
				t.Fatalf("cleanup window not reached: %v", err)
			}
			SyncTransactionStepHook = nil
			f.reload(t)
			if !SyncTransactionCleanupOnly(f.tx) {
				t.Fatal("completion was not durable before pin cleanup")
			}
			gitInTest(t, f.repo, "commit", "--allow-empty", "-qm", "operator after sync completion")
			userTip := gitInTest(t, f.repo, "rev-parse", "topic")
			userMetadata := []byte("# operator after sync completion\nbranches: []\n")
			if err := os.WriteFile(StackPath(f.feature), userMetadata, 0600); err != nil {
				t.Fatal(err)
			}
			if err := AbortSyncTransaction(f.tx, f.save, nil); err != nil {
				t.Fatalf("late abort should finish cleanup without rollback: %v", err)
			}
			if got := gitInTest(t, f.repo, "rev-parse", "topic"); got != userTip {
				t.Fatal("late abort rewound operator work")
			}
			gotMetadata, err := os.ReadFile(StackPath(f.feature))
			if err != nil || string(gotMetadata) != string(userMetadata) {
				t.Fatal("late abort overwrote operator metadata")
			}
		})
	}
}
