package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func retainedTransactionalPayload(t *testing.T, f *scopedFixture) []byte {
	t.Helper()
	internal.SyncTransactionStepHook = func(step string) error {
		if step == "snapshot-ready" {
			return errors.New("retain unmutated transaction")
		}
		return nil
	}
	t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
	_, stderr, exit := runSync(t, f.feature, "--no-fetch")
	internal.SyncTransactionStepHook = nil
	if exit == 0 {
		t.Fatalf("fixture did not retain a transaction: %s", stderr)
	}
	payload := loadPayload(t, f.featurePath)
	if payload.Transaction == nil || !payload.Transaction.Ready {
		t.Fatal("fixture did not produce valid transactional evidence")
	}
	data, err := os.ReadFile(internal.SyncRunStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSyncTransactionOrphanTriggerPolicyPreservesRoute(t *testing.T) {
	for _, route := range []string{internal.RouteLegacy, internal.RouteNewMode} {
		state := internal.SyncExternalState{
			Cell: 2,
			Payload: &internal.SyncRunState{
				StateVersion: internal.SyncRunStateTransactionalVersion,
				Route:        route, Transaction: &internal.SyncTransaction{},
			},
		}
		if got, want := syncTriggersNeedV2(state), route == internal.RouteLegacy; got != want {
			t.Fatalf("transactional orphan route %s: requires new-mode refusal=%v, want %v", route, got, want)
		}
	}
}

func TestSyncTransactionMissingStackRacesRefuseBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"legacy-after-claim", "payload-after-claim", "stack-before-fallback"} {
		t.Run(scenario, func(t *testing.T) {
			f := newScopedFixture(t)
			var orphan []byte
			if scenario == "payload-after-claim" {
				orphan = retainedTransactionalPayload(t, f)
				for _, path := range []string{internal.SyncStatePath(f.featurePath), internal.SyncRunStatePath(f.featurePath), internal.SyncRunGuardPath(f.featurePath)} {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			}
			stack, err := os.ReadFile(internal.StackPath(f.featurePath))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(internal.StackPath(f.featurePath)); err != nil {
				t.Fatal(err)
			}
			beforeRefs := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			var evidencePath string
			var evidenceBytes []byte
			hook := func(string) error {
				switch scenario {
				case "legacy-after-claim":
					evidencePath = internal.SyncStatePath(f.featurePath)
					state := internal.NewSyncState()
					state.FailedBranch = "root"
					if err := internal.SaveSyncState(f.featurePath, state); err != nil {
						return err
					}
					var err error
					evidenceBytes, err = os.ReadFile(evidencePath)
					return err
				case "payload-after-claim":
					evidencePath, evidenceBytes = internal.SyncRunStatePath(f.featurePath), orphan
				case "stack-before-fallback":
					evidencePath, evidenceBytes = internal.StackPath(f.featurePath), stack
				}
				return os.WriteFile(evidencePath, evidenceBytes, 0600)
			}
			if scenario == "stack-before-fallback" {
				syncMissingStackPreMutationHook = hook
			} else {
				ExternalSyncMutationGuardHook = hook
			}
			t.Cleanup(func() {
				ExternalSyncMutationGuardHook = nil
				syncMissingStackPreMutationHook = nil
			})
			_, _, log := reparentPushCapture(t, func() {
				_, output, exit := runSync(t, f.feature, "--allow-nontransactional")
				if exit == 0 || !strings.Contains(output, "appeared") {
					t.Fatalf("fallback race did not refuse: %d %s", exit, output)
				}
			})
			for _, argv := range log.argv {
				for _, verb := range []string{"fetch", "rebase", "push", "checkout", "switch", "update-ref"} {
					if argvHas(argv, verb) {
						t.Fatalf("fallback race executed %s: %v", verb, argv)
					}
				}
			}
			after, err := os.ReadFile(evidencePath)
			if err != nil || string(after) != string(evidenceBytes) {
				t.Fatalf("fallback overwrote newly appeared evidence: %v", err)
			}
			if gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") != beforeRefs {
				t.Fatal("fallback moved refs alongside recovery evidence")
			}
			if _, err := os.Lstat(internal.SyncRunGuardPath(f.featurePath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused fallback retained its new guard: %v", err)
			}
		})
	}
}

func TestSyncTransactionPublishedOrphanContinuesForward(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		name := "unguarded"
		if guarded {
			name = "guarded"
		}
		t.Run(name, func(t *testing.T) {
			f := newScopedFixture(t)
			f.advanceRoot(t)
			internal.SyncTransactionStepHook = func(step string) error {
				if strings.HasPrefix(step, "publication-intent:") {
					return errors.New("interrupt before first push")
				}
				return nil
			}
			t.Cleanup(func() { internal.SyncTransactionStepHook = nil })
			args := []string{f.feature, "--no-fetch", "--push"}
			if guarded {
				args = append(args, "--max-replay-total", "50")
			}
			_, stderr, exit := runSync(t, args...)
			internal.SyncTransactionStepHook = nil
			if exit == 0 {
				t.Fatalf("publication intent interruption was not reached: %s", stderr)
			}
			f.detachGuard(t)
			if err := os.Remove(internal.SyncStatePath(f.featurePath)); err != nil {
				t.Fatal(err)
			}
			before := readFileString(t, internal.SyncRunStatePath(f.featurePath))
			_, stderr, exit = runSync(t, f.feature)
			if exit == 0 || !strings.Contains(stderr, "--continue") || strings.Contains(stderr, "--abort") {
				t.Fatalf("published orphan guidance is not forward-only: %d %s", exit, stderr)
			}
			if readFileString(t, internal.SyncRunStatePath(f.featurePath)) != before {
				t.Fatal("plain orphan refusal changed the journal")
			}
			if _, err := os.Lstat(internal.SyncStatePath(f.featurePath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("plain refusal reconstructed state without recovery authority")
			}
			stdout, stderr, exit := runSync(t, f.feature, "--continue")
			if exit != 0 {
				t.Fatalf("published orphan cannot continue: %d\n%s\n%s", exit, stdout, stderr)
			}
			for _, branch := range []string{"root", "parent", "child"} {
				if gitOutput(t, f.remote, "rev-parse", branch) != gitOutput(t, f.repo, "rev-parse", branch) {
					t.Fatalf("orphan continuation failed to publish %s", branch)
				}
			}
			f.stateFilesGone(t)
		})
	}
}

func TestSyncTransactionMixedEvidenceNeverRecommendsDeletion(t *testing.T) {
	f := newScopedFixture(t)
	retainedTransactionalPayload(t, f)
	f.detachGuard(t)
	legacy := internal.NewSyncState()
	legacy.FailedBranch = "parent"
	if err := internal.SaveSyncState(f.featurePath, legacy); err != nil {
		t.Fatal(err)
	}
	legacyBefore := readFileString(t, internal.SyncStatePath(f.featurePath))
	payloadBefore := readFileString(t, internal.SyncRunStatePath(f.featurePath))
	for _, verb := range [][]string{nil, {"--continue"}, {"--abort"}} {
		_, stderr, exit := runSync(t, append([]string{f.feature}, verb...)...)
		if exit == 0 || !strings.Contains(stderr, "preserve and inspect") ||
			strings.Contains(stderr, "remove") || strings.Contains(stderr, "delete") {
			t.Fatalf("mixed evidence gives unsafe guidance for %v: %d %s", verb, exit, stderr)
		}
		if readFileString(t, internal.SyncStatePath(f.featurePath)) != legacyBefore ||
			readFileString(t, internal.SyncRunStatePath(f.featurePath)) != payloadBefore {
			t.Fatal("mixed-state refusal changed recovery evidence")
		}
	}
}

func TestSyncTransactionDoctorPreservesValidatorCheckoutFacts(t *testing.T) {
	for _, command := range []string{"git switch --detach", "git switch validator-target"} {
		t.Run(command, func(t *testing.T) {
			f := newScopedFixture(t)
			gitRun(t, f.repo, "branch", "validator-target", "master")
			writeTestCommandConfig(t, command)
			_, stderr, exit := runSync(t, f.feature, "--only", "child", "--no-fetch")
			if exit == 0 || !strings.Contains(stderr, "recover manually") {
				t.Fatalf("validator did not change checkout identity: %d %s", exit, stderr)
			}
			ws := internal.Workspace{RepoRoot: f.repo, Mode: internal.ModeExternal, MetadataRoot: filepath.Dir(f.featurePath)}
			var doctorErr error
			output := captureStdout(t, func() {
				_, doctorErr = checkFeatureE(ws, internal.LoadConfig(), f.feature)
			})
			if doctorErr != nil {
				t.Fatal(doctorErr)
			}
			if !strings.Contains(output, "on branch") || !strings.Contains(output, "expected") ||
				!strings.Contains(output, "recover manually") {
				t.Fatalf("doctor lost facts or manual guidance: %s", output)
			}
			for _, forbidden := range []string{"git checkout", "git rebase", "tws sync", "--continue", "--abort"} {
				if strings.Contains(output, forbidden) {
					t.Fatalf("doctor recommends mutation %q alongside protected evidence: %s", forbidden, output)
				}
			}
		})
	}
}
