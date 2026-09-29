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

func TestSyncTransactionPostClaimStackDriftRefusesBeforeMutation(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	beforeRefs := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	ExternalSyncMutationGuardHook = func(string) error {
		stack, err := internal.LoadStack(f.featurePath)
		if err != nil {
			return err
		}
		stack.Branches = append(stack.Branches, internal.StackEntry{Name: "appeared", Base: "master"})
		return internal.SaveStack(f.featurePath, stack)
	}
	t.Cleanup(func() { ExternalSyncMutationGuardHook = nil })
	_, stderr, exit := runSync(t, f.feature, "--no-fetch")
	if exit == 0 || !strings.Contains(stderr, "stale selection or plan") {
		t.Fatalf("post-claim stack drift was accepted: exit=%d stderr=%s", exit, stderr)
	}
	if after := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); after != beforeRefs {
		t.Fatal("post-claim stack refusal moved refs")
	}
	for _, path := range []string{
		internal.SyncStatePath(f.featurePath),
		internal.SyncRunStatePath(f.featurePath),
		internal.SyncRunGuardPath(f.featurePath),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed setup left owned runtime artifact %s: %v", path, err)
		}
	}
}

func TestSyncTransactionPostClaimOrphanStateIsPreserved(t *testing.T) {
	f := newScopedFixture(t)
	orphan := internal.NewSyncState()
	orphan.FailedBranch = "orphan-owner"
	ExternalSyncMutationGuardHook = func(string) error {
		return internal.SaveSyncState(f.featurePath, orphan)
	}
	t.Cleanup(func() { ExternalSyncMutationGuardHook = nil })
	_, stderr, exit := runSync(t, f.feature, "--no-fetch")
	if exit == 0 || !strings.Contains(stderr, "recovery evidence appeared") {
		t.Fatalf("post-claim orphan state was overwritten: exit=%d stderr=%s", exit, stderr)
	}
	got, err := internal.LoadSyncState(f.featurePath)
	if err != nil || got.FailedBranch != orphan.FailedBranch {
		t.Fatalf("orphan state was not preserved: %+v %v", got, err)
	}
	if _, err := os.Lstat(internal.SyncRunStatePath(f.featurePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh payload overwrote orphan admission: %v", err)
	}
}

func TestSyncTransactionBirthRejectsLaterDirtyRowBeforeAnchorMoves(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	before := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	if err := os.WriteFile(f.wt("child")+"/child.txt", []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, exit := runSync(t, f.feature, "--no-fetch")
	if exit == 0 || !strings.Contains(stderr, "tracked modifications") {
		t.Fatalf("dirty later holder was not preflighted at birth: exit=%d stderr=%s", exit, stderr)
	}
	if after := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); after != before {
		t.Fatal("anchor moved before the later dirty holder refusal")
	}
}

func TestSyncTransactionPublicationUsesRecordedOIDAndFoldsResults(t *testing.T) {
	t.Run("foreign-advance-before-push", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		var foreign string
		TopLevelPushEntryBarrier = func(index int, entry internal.StackEntry) error {
			if index != 0 {
				return nil
			}
			gitRun(t, f.wt(entry.Name), "commit", "--allow-empty", "-m", "operator after sync")
			foreign = f.sha(t, entry.GitBranch())
			return nil
		}
		t.Cleanup(func() { TopLevelPushEntryBarrier = nil })
		_, stderr, exit := runSync(t, f.feature, "--no-fetch", "--push")
		if exit == 0 || !strings.Contains(stderr, "changed outside this sync transaction") {
			t.Fatalf("foreign source advance was publishable: exit=%d stderr=%s", exit, stderr)
		}
		if got := f.sha(t, "root"); got != foreign {
			t.Fatal("publication refusal erased the foreign local advance")
		}
		if strings.Contains(remoteRefs(t, f.remote), "refs/heads/root") {
			t.Fatal("foreign local advance reached the remote")
		}
	})

	t.Run("result-before-pushed", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		stop := errors.New("stop after durable publication result")
		internal.SyncTransactionStepHook = func(step string) error {
			if step == "publication-result:parent" {
				return stop
			}
			return nil
		}
		t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
		_, _, exit := runSync(t, f.feature, "--from", "parent", "--push")
		if exit == 0 {
			t.Fatal("publication result crash window was not reached")
		}
		internal.SyncTransactionStepHook = nil
		payload := loadPayload(t, f.featurePath)
		if len(payload.Pushed) != 0 || !internal.SyncTransactionEntryPublished(payload.Transaction, "parent") {
			t.Fatalf("fixture did not stop between result and Pushed: pushed=%v results=%+v", payload.Pushed, payload.Transaction.Publication.Results)
		}
		f.detachGuard(t)
		_, _, log := reparentPushCapture(t, func() {
			stdout, stderr, code := runSync(t, f.feature, "--continue")
			if code != 0 {
				t.Fatalf("continue: %d\n%s\n%s", code, stdout, stderr)
			}
		})
		for _, argv := range log.argv {
			if !argvHas(argv, "push") {
				continue
			}
			for _, arg := range argv {
				if strings.HasSuffix(arg, ":refs/heads/parent") {
					t.Fatalf("durably successful parent push was repeated: %v", argv)
				}
			}
		}
	})
}

func TestTransactionalPushArgvBindsRecordedOIDToDestination(t *testing.T) {
	intent := internal.SyncTransactionPushIntent{
		Entry: "child", RepoCommonDir: "/repo",
		SourceSHA: strings.Repeat("a", 40), DestinationRef: "refs/heads/child",
	}
	argv := transactionalPushArgv(internal.ReparentPushDecision{ForceIfIncludes: true}, intent)
	got := strings.Join(argv, " ")
	if !strings.Contains(got, "--force-with-lease --force-if-includes") ||
		argv[len(argv)-1] != intent.SourceSHA+":"+intent.DestinationRef {
		t.Fatalf("transactional push is not immutably source-bound: %v", argv)
	}
}

func TestSyncTransactionMissingStackRequiresExplicitConsent(t *testing.T) {
	t.Run("noninteractive-default-refuses", func(t *testing.T) {
		f := newScopedFixture(t)
		if err := os.Remove(internal.StackPath(f.featurePath)); err != nil {
			t.Fatal(err)
		}
		_, stderr, exit := runSync(t, f.feature)
		if exit == 0 || !strings.Contains(stderr, "Noninteractive execution refuses by default") {
			t.Fatalf("missing-stack default did not refuse: exit=%d stderr=%s", exit, stderr)
		}
	})

	t.Run("automation-flag", func(t *testing.T) {
		f := newScopedFixture(t)
		if err := os.Remove(internal.StackPath(f.featurePath)); err != nil {
			t.Fatal(err)
		}
		ExternalSyncMutationGuardHook = func(string) error { return errors.New("stop after explicit consent") }
		defer func() { ExternalSyncMutationGuardHook = nil }()
		_, stderr, exit := runSync(t, f.feature, "--allow-nontransactional")
		if exit == 0 || !strings.Contains(stderr, "no complete rollback") || !strings.Contains(stderr, "stop after explicit consent") {
			t.Fatalf("explicit missing-stack fallback failed: exit=%d stderr=%s", exit, stderr)
		}
	})

	t.Run("interactive-confirmation", func(t *testing.T) {
		f := newScopedFixture(t)
		if err := os.Remove(internal.StackPath(f.featurePath)); err != nil {
			t.Fatal(err)
		}
		oldInteractive := syncNontransactionalInteractive
		syncNontransactionalInteractive = func(*cobra.Command) bool { return true }
		ExternalSyncMutationGuardHook = func(string) error { return errors.New("stop after interactive consent") }
		defer func() {
			syncNontransactionalInteractive = oldInteractive
			ExternalSyncMutationGuardHook = nil
		}()
		var runErr error
		_, stderr := syncCaptureStreams(t, func() {
			cmd := syncCmd()
			cmd.SetArgs([]string{f.feature})
			cmd.SetIn(strings.NewReader("yes\n"))
			cmd.SetOut(os.Stdout)
			cmd.SetErr(os.Stderr)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			runErr = cmd.Execute()
		})
		if runErr == nil || !strings.Contains(runErr.Error(), "stop after interactive consent") ||
			!strings.Contains(stderr, "Continue with the nontransactional compatibility sync?") {
			t.Fatalf("interactive consent path failed: err=%v stderr=%s", runErr, stderr)
		}
	})

	t.Run("malformed-never-bypassed", func(t *testing.T) {
		f := newScopedFixture(t)
		if err := os.WriteFile(internal.StackPath(f.featurePath), []byte("branches: [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, stderr, exit := runSync(t, f.feature, "--allow-nontransactional")
		if exit == 0 || !strings.Contains(stderr, "exists but is unreadable or malformed") {
			t.Fatalf("malformed stack reached fallback: exit=%d stderr=%s", exit, stderr)
		}
	})

	t.Run("plan-combination-refuses-early", func(t *testing.T) {
		f := newScopedFixture(t)
		_, stderr, exit := runSync(t, f.feature, "--plan", "--allow-nontransactional")
		if exit == 0 || !strings.Contains(stderr, "only valid for a fresh unguarded full sync") {
			t.Fatalf("plan accepted bypass flag: exit=%d stderr=%s", exit, stderr)
		}
	})
}

func TestSyncTransactionCleanupOnlyIgnoresLaterStackEdits(t *testing.T) {
	for _, verb := range []string{"--continue", "--abort"} {
		t.Run(strings.TrimPrefix(verb, "--"), func(t *testing.T) {
			f := newScopedFixture(t)
			f.advanceRoot(t)
			stop := errors.New("stop after durable forward completion")
			internal.SyncTransactionStepHook = func(step string) error {
				if step == "cleanup-started" {
					return stop
				}
				return nil
			}
			_, _, exit := runSync(t, f.feature, "--no-fetch")
			internal.SyncTransactionStepHook = nil
			if exit == 0 {
				t.Fatal("cleanup crash window was not reached")
			}
			replacement := []byte("# operator metadata after completion\nbranches: []\n")
			if err := os.WriteFile(internal.StackPath(f.featurePath), replacement, 0o600); err != nil {
				t.Fatal(err)
			}
			f.detachGuard(t)
			stdout, stderr, exit := runSync(t, f.feature, verb)
			if exit != 0 || !strings.Contains(stdout, "finished cleanup only") {
				t.Fatalf("cleanup-only %s failed: %d\n%s\n%s", verb, exit, stdout, stderr)
			}
			got, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil || string(got) != string(replacement) {
				t.Fatalf("cleanup-only %s rewrote later metadata: %v", verb, err)
			}
		})
	}
}

func TestSyncTransactionPlainGuidanceIsPhaseAware(t *testing.T) {
	t.Run("external-published", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		internal.SyncTransactionStepHook = func(step string) error {
			if strings.HasPrefix(step, "publication-intent:") {
				return errors.New("stop at publication")
			}
			return nil
		}
		_, _, _ = runSync(t, f.feature, "--no-fetch", "--push")
		internal.SyncTransactionStepHook = nil
		f.detachGuard(t)
		_, stderr, exit := runSync(t, f.feature)
		if exit == 0 || !strings.Contains(stderr, "publication boundary") ||
			!strings.Contains(stderr, "--continue") || strings.Contains(stderr, "--abort") {
			t.Fatalf("published plain guidance was unsafe: exit=%d stderr=%s", exit, stderr)
		}
	})

	t.Run("external-rolling-back", func(t *testing.T) {
		f := newScopedFixture(t)
		f.advanceRoot(t)
		internal.SyncTransactionStepHook = func(step string) error {
			if strings.HasPrefix(step, "action-observed:") {
				return errors.New("retain forward state")
			}
			return nil
		}
		_, _, _ = runSync(t, f.feature, "--no-fetch")
		internal.SyncTransactionStepHook = nil
		f.detachGuard(t)
		internal.SyncTransactionStepHook = func(step string) error {
			if step == "rollback-started" {
				return errors.New("retain rolling-back state")
			}
			return nil
		}
		_, _, _ = runSync(t, f.feature, "--abort")
		internal.SyncTransactionStepHook = nil
		_, stderr, exit := runSync(t, f.feature)
		if exit == 0 || !strings.Contains(stderr, "rollback is in progress") ||
			!strings.Contains(stderr, "--abort") || strings.Contains(stderr, "--continue") {
			t.Fatalf("rolling-back plain guidance was unsafe: exit=%d stderr=%s", exit, stderr)
		}
	})

	t.Run("checkout-published", func(t *testing.T) {
		dir, featurePath := checkoutModeFixture(t)
		withCheckoutEnv(t, dir)
		if err := os.WriteFile(filepath.Join(dir, ".tws", "config.yaml"), []byte("workspace_mode: checkout\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeAndCommit(t, dir, "upstream-published.txt", "upstream\n", "advance upstream")
		opts := newModeOpts(dir, featurePath, internal.SyncRunPolicy{
			Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull,
			ScopeKind: internal.SyncScopeAll,
		})
		opts.Push = true
		gitRun(t, dir, "remote", "add", "origin", dir+"/missing.git")
		_ = internal.RunCheckoutSync(opts)
		_, stderr, exit := runSync(t, filepath.Base(featurePath))
		if exit == 0 || !strings.Contains(stderr, "publication boundary") ||
			!strings.Contains(stderr, "--continue") || strings.Contains(stderr, "--abort") {
			t.Fatalf("checkout published plain guidance was unsafe: exit=%d stderr=%s", exit, stderr)
		}
	})
}

func TestSyncTransactionMalformedEvidenceAlwaysPreservesAndInspects(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		f := newScopedFixture(t)
		malformed := []byte("state_version: [\n")
		if err := os.WriteFile(internal.SyncRunStatePath(f.featurePath), malformed, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{}, {"--continue"}, {"--abort"}} {
			_, stderr, exit := runSync(t, append([]string{f.feature}, args...)...)
			if exit == 0 || !strings.Contains(stderr, "preserve and inspect") ||
				strings.Contains(stderr, "remove it manually") {
				t.Fatalf("external malformed guidance for %v: exit=%d stderr=%s", args, exit, stderr)
			}
			got, err := os.ReadFile(internal.SyncRunStatePath(f.featurePath))
			if err != nil || string(got) != string(malformed) {
				t.Fatalf("external malformed evidence changed for %v: %v", args, err)
			}
		}
	})

	t.Run("checkout", func(t *testing.T) {
		dir, featurePath := checkoutModeFixture(t)
		withCheckoutEnv(t, dir)
		if err := os.WriteFile(filepath.Join(dir, ".tws", "config.yaml"), []byte("workspace_mode: checkout\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		malformed := []byte("state_version: [\n")
		if err := os.MkdirAll(filepath.Dir(internal.CheckoutTransactionPath(featurePath)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(internal.CheckoutTransactionPath(featurePath), malformed, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{}, {"--continue"}, {"--abort"}} {
			_, stderr, exit := runSync(t, append([]string{filepath.Base(featurePath)}, args...)...)
			if exit == 0 || !strings.Contains(stderr, "preserve and inspect") ||
				strings.Contains(stderr, "manually remove") {
				t.Fatalf("checkout malformed guidance for %v: exit=%d stderr=%s", args, exit, stderr)
			}
			got, err := os.ReadFile(internal.CheckoutTransactionPath(featurePath))
			if err != nil || string(got) != string(malformed) {
				t.Fatalf("checkout malformed evidence changed for %v: %v", args, err)
			}
		}
	})
}

func TestCheckoutSyncCleanupOnlyIgnoresRemovedStack(t *testing.T) {
	for _, verb := range []string{"continue", "abort"} {
		t.Run(verb, func(t *testing.T) {
			dir, feature := checkoutModeFixture(t)
			writeAndCommit(t, dir, "upstream-cleanup.txt", "upstream\n", "advance upstream")
			opts := newModeOpts(dir, feature, internal.SyncRunPolicy{
				Fetch: internal.SyncFetchDisabled, Propagation: internal.SyncPropagationFull,
				ScopeKind: internal.SyncScopeAll,
			})
			stop := errors.New("stop after durable forward completion")
			internal.SyncTransactionStepHook = func(step string) error {
				if step == "cleanup-started" {
					return stop
				}
				return nil
			}
			if err := internal.RunCheckoutSync(opts); !errors.Is(err, stop) {
				t.Fatalf("cleanup window: %v", err)
			}
			internal.SyncTransactionStepHook = nil
			t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
			if err := os.Remove(internal.StackPath(feature)); err != nil {
				t.Fatal(err)
			}
			var err error
			if verb == "continue" {
				err = internal.ContinueCheckoutSync(opts)
			} else {
				err = internal.AbortCheckoutSync(opts)
			}
			if err != nil {
				t.Fatalf("cleanup-only %s depended on removed stack: %v", verb, err)
			}
			if _, err := os.Lstat(internal.StackPath(feature)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cleanup-only %s recreated stack.yaml: %v", verb, err)
			}
		})
	}
}

func TestSyncTransactionGuardUpgradePreservesLegacyRoute(t *testing.T) {
	f := newScopedFixture(t)
	f.advanceRoot(t)
	stop := errors.New("retain transactional legacy payload")
	internal.SyncTransactionStepHook = func(step string) error {
		if strings.HasPrefix(step, "action-observed:") {
			return stop
		}
		return nil
	}
	_, _, exit := runSync(t, f.feature)
	internal.SyncTransactionStepHook = nil
	if exit == 0 {
		t.Fatal("legacy transactional fixture did not retain state")
	}
	payload := loadPayload(t, f.featurePath)
	if payload.Route != internal.RouteLegacy {
		t.Fatalf("fixture route=%q", payload.Route)
	}
	one := 1
	if err := upgradeGuardedSyncRunState(f.featurePath, payload, syncRunStateBirth{
		StateVersion: internal.SyncRunStateTransactionalGuardedVersion,
		MaxPerEntry:  &one,
	}); err != nil {
		t.Fatal(err)
	}
	reloaded := loadPayload(t, f.featurePath)
	if reloaded.Route != internal.RouteLegacy {
		t.Fatalf("v4->v5 upgrade drifted route to %q", reloaded.Route)
	}
}

func TestSyncTransactionLiveGuardGuidanceIsPhaseAware(t *testing.T) {
	for _, phase := range []string{"published", "rolling-back", "rollback-cleanup"} {
		t.Run(phase, func(t *testing.T) {
			f := newScopedFixture(t)
			internal.SyncTransactionStepHook = func(step string) error {
				if step == "snapshot-ready" {
					return errors.New("retain fresh transaction")
				}
				return nil
			}
			t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
			_, _, exit := runSync(t, f.feature, "--no-fetch")
			internal.SyncTransactionStepHook = nil
			if exit == 0 {
				t.Fatal("fixture did not preserve its live guard")
			}
			payload := loadPayload(t, f.featurePath)
			want, forbidden := "--abort", "--continue"
			if phase == "published" {
				intent, err := internal.SyncPreparePublication(payload.Transaction, f.repo, "root", "root")
				if err != nil {
					t.Fatal(err)
				}
				if err := internal.SyncBeginPublication(payload.Transaction, intent, func() error {
					return internal.SaveSyncRunState(f.featurePath, payload)
				}); err != nil {
					t.Fatal(err)
				}
				want, forbidden = "--continue", "--abort"
			} else {
				payload.Transaction.Rollback.StartedAt = payload.StartedAt
				payload.Transaction.Phase = internal.SyncTxnRollingBack
				if phase == "rollback-cleanup" {
					payload.Transaction.Phase = internal.SyncTxnCleanup
				}
				if err := internal.SaveSyncRunState(f.featurePath, payload); err != nil {
					t.Fatal(err)
				}
			}
			before := readFileString(t, internal.SyncRunStatePath(f.featurePath))
			for _, verb := range [][]string{nil, {"--continue"}, {"--abort"}} {
				_, stderr, exit := runSync(t, append([]string{f.feature}, verb...)...)
				if exit == 0 || !strings.Contains(stderr, "wait for it to exit") ||
					!strings.Contains(stderr, want) || strings.Contains(stderr, forbidden) {
					t.Fatalf("phase %s verb %v gives invalid live recovery guidance: %d %s", phase, verb, exit, stderr)
				}
				if readFileString(t, internal.SyncRunStatePath(f.featurePath)) != before {
					t.Fatal("live refusal rewrote recovery evidence")
				}
			}
		})
	}
}

func TestSyncTransactionDanglingCheckoutEvidenceIsNotAbsent(t *testing.T) {
	dir, featurePath := checkoutModeFixture(t)
	withCheckoutEnv(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".tws", "config.yaml"), []byte("workspace_mode: checkout\n"), 0600); err != nil {
		t.Fatal(err)
	}
	statePath := internal.CheckoutTransactionPath(featurePath)
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing-state.yaml")
	if err := os.Symlink(missing, statePath); err != nil {
		t.Fatal(err)
	}
	before := gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	_, stderr, exit := runSync(t, filepath.Base(featurePath))
	if exit == 0 || !strings.Contains(stderr, "preserve and inspect") {
		t.Fatalf("dangling recovery path was treated as absent: %d %s", exit, stderr)
	}
	if target, err := os.Readlink(statePath); err != nil || target != missing {
		t.Fatalf("recovery symlink was replaced: target=%s err=%v", target, err)
	}
	if gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") != before {
		t.Fatal("fresh sync moved branches despite unreadable existing recovery evidence")
	}
}

func assertRenderedValidatorMutationGuidance(t *testing.T, output string) {
	t.Helper()
	for _, required := range []string{"preserve", "journal", "working tree", "recover manually"} {
		if !strings.Contains(output, required) {
			t.Fatalf("validator-mutation output lacks %q:\n%s", required, output)
		}
	}
	for _, forbidden := range []string{"--continue", "--abort"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("validator-mutation output recommends %s:\n%s", forbidden, output)
		}
	}
}

func TestSyncTransactionValidatorMutationRenderedRecoveryGuidance(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		f := newScopedFixture(t)
		stack, err := internal.LoadStack(f.featurePath)
		if err != nil {
			t.Fatal(err)
		}
		for i := range stack.Branches {
			stack.Branches[i].Repo = ""
		}
		if err := internal.SaveStack(f.featurePath, stack); err != nil {
			t.Fatal(err)
		}
		writeTestCommandConfig(t, "git commit --allow-empty -m validator-mutation")
		f.advanceRoot(t)
		_, stderr, exit := runSync(t, f.feature, "--no-fetch")
		if exit == 0 || !strings.Contains(stderr, "recover manually") {
			t.Fatalf("actual external validator mutation was not retained: %d %s", exit, stderr)
		}

		_, liveStderr, exit := runSync(t, f.feature)
		if exit == 0 || !strings.Contains(liveStderr, "wait for it to exit") {
			t.Fatalf("live validator-mutation refusal missing: %d %s", exit, liveStderr)
		}
		assertRenderedValidatorMutationGuidance(t, liveStderr)

		if err := os.Chdir(filepath.Dir(f.featurePath)); err != nil {
			t.Fatal(err)
		}
		var statusExit int
		statusOut, statusErr := syncCaptureStreams(t, func() {
			statusExit = syncExecute(statusCmd, f.feature)
		})
		if statusExit != 0 {
			t.Fatalf("status failed: %d %s", statusExit, statusErr)
		}
		assertRenderedValidatorMutationGuidance(t, statusOut+statusErr)

		ws := internal.Workspace{
			RepoRoot:     f.repo,
			Mode:         internal.ModeExternal,
			MetadataRoot: filepath.Dir(f.featurePath),
		}
		var doctorErr error
		doctorOut := captureStdout(t, func() {
			_, doctorErr = checkFeatureE(ws, internal.LoadConfig(), f.feature)
		})
		if doctorErr != nil {
			t.Fatalf("doctor failed: %v", doctorErr)
		}
		assertRenderedValidatorMutationGuidance(t, doctorOut)
		if !strings.Contains(doctorOut, "ancestry stale") && !strings.Contains(doctorOut, "ancestry divergent") {
			t.Fatalf("doctor suppressed the validator-affected ancestry fact:\n%s", doctorOut)
		}
		for _, forbidden := range []string{"tws sync " + f.feature + " --", "git rebase"} {
			if strings.Contains(doctorOut, forbidden) {
				t.Fatalf("doctor emitted ancestry repair guidance %q beside manual recovery:\n%s", forbidden, doctorOut)
			}
		}

		f.detachGuard(t)
		_, staleStderr, exit := runSync(t, f.feature)
		if exit == 0 {
			t.Fatal("stale validator-mutated transaction was treated as a fresh run")
		}
		assertRenderedValidatorMutationGuidance(t, staleStderr)
	})

	t.Run("checkout", func(t *testing.T) {
		dir, featurePath := checkoutModeFixture(t)
		withCheckoutEnv(t, dir)
		if err := os.WriteFile(filepath.Join(dir, ".tws", "config.yaml"), []byte("workspace_mode: checkout\n"), 0600); err != nil {
			t.Fatal(err)
		}
		writeAndCommit(t, dir, "validator-upstream.txt", "upstream\n", "advance upstream")
		feature := filepath.Base(featurePath)
		_, stderr, exit := runSync(t, feature, "--test", "git commit --allow-empty -m validator-mutation")
		if exit == 0 || !strings.Contains(stderr, "recover manually") {
			t.Fatalf("actual checkout validator mutation was not retained: %d %s", exit, stderr)
		}

		_, plainStderr, exit := runSync(t, feature)
		if exit == 0 {
			t.Fatal("validator-mutated checkout transaction was treated as a fresh run")
		}
		assertRenderedValidatorMutationGuidance(t, plainStderr)

		var statusExit int
		statusOut, statusErr := syncCaptureStreams(t, func() {
			statusExit = syncExecute(statusCmd, feature)
		})
		if statusExit != 0 {
			t.Fatalf("checkout status failed: %d %s", statusExit, statusErr)
		}
		assertRenderedValidatorMutationGuidance(t, statusOut+statusErr)

		var doctorExit int
		doctorOut, doctorErr := syncCaptureStreams(t, func() {
			doctorExit = syncExecute(doctorCmd, feature)
		})
		if doctorExit != 0 {
			t.Fatalf("checkout doctor failed: %d %s", doctorExit, doctorErr)
		}
		assertRenderedValidatorMutationGuidance(t, doctorOut+doctorErr)
		if !strings.Contains(doctorOut, "ancestry=stale") && !strings.Contains(doctorOut, "ancestry=divergent") {
			t.Fatalf("checkout doctor suppressed the validator-affected ancestry fact:\n%s", doctorOut)
		}
		for _, forbidden := range []string{"tws sync " + feature + " --", "git rebase"} {
			if strings.Contains(doctorOut, forbidden) {
				t.Fatalf("checkout doctor emitted ancestry repair guidance %q beside manual recovery:\n%s", forbidden, doctorOut)
			}
		}
	})
}
