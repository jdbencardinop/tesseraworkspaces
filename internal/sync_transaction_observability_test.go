package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const observabilityOwnerToken = "abcdef0123456789abcdef0123456789"

func newExternalTransactionalObservabilityFixture(t *testing.T) (Workspace, string, *SyncRunState) {
	t.Helper()
	entry := StackEntry{Name: "child", Branch: "child", Base: "main"}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{entry})
	worktree := addExternalWorktree(t, ws, featurePath, entry)
	stack, err := LoadStack(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := CaptureSyncTransaction(SyncTransactionBeginInput{
		FeaturePath:       featurePath,
		Feature:           "auth",
		Mode:              ModeExternal,
		WorkspaceRepoRoot: ws.RepoRoot,
		Stack:             stack,
		Selected:          []string{"child"},
		EntryRepoDirs:     map[string]string{"child": worktree},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := NewSyncRunState("auth", statusMarker, observabilityOwnerToken, SyncRunPolicy{
		Fetch: SyncFetchDisabled, Propagation: SyncPropagationFull, ScopeKind: SyncScopeAll,
	})
	payload.StateVersion = SyncRunStateTransactionalVersion
	payload.Route = RouteNewMode
	payload.Selected = []string{"child"}
	payload.Pending = []string{"child"}
	payload.Repos = []string{""}
	payload.ValidationSource = "none"
	payload.Transaction = tx
	writeStatusSentinel(t, featurePath)
	if err := SaveSyncRunState(featurePath, payload); err != nil {
		t.Fatal(err)
	}
	if err := PinSyncTransaction(tx, func() error { return SaveSyncRunState(featurePath, payload) }); err != nil {
		t.Fatal(err)
	}
	writeStatusGuard(t, featurePath, 999091, observabilityOwnerToken)
	return ws, featurePath, payload
}

func newCheckoutTransactionalObservabilityFixture(t *testing.T) (Workspace, string, *CheckoutTransaction) {
	t.Helper()
	f := newTransactionalLoadFixture(t)
	tx := f.checkoutState(t)
	if err := SaveCheckoutTransaction(f.featurePath, tx); err != nil {
		t.Fatal(err)
	}
	if err := PinSyncTransaction(tx.Transaction, func() error {
		return SaveCheckoutTransaction(f.featurePath, tx)
	}); err != nil {
		t.Fatal(err)
	}
	metadataRoot := filepath.Dir(filepath.Dir(f.featurePath))
	ws := Workspace{
		RepoRoot:     canonicalize(f.repo),
		Mode:         ModeCheckout,
		MetadataRoot: canonicalize(metadataRoot),
		StableID:     stableID(canonicalize(f.repo)),
		Caps:         capsFor(ModeCheckout),
	}
	return ws, f.featurePath, tx
}

func setExternalObservabilityRecovery(t *testing.T, featurePath string, payload *SyncRunState, kind string) {
	t.Helper()
	switch kind {
	case "forward":
	case "published":
		payload.Push = true
		intent, err := SyncPreparePublication(payload.Transaction, payload.Transaction.Repositories[0].Root, "child", "child")
		if err != nil {
			t.Fatal(err)
		}
		if err := SyncBeginPublication(payload.Transaction, intent, func() error {
			return SaveSyncRunState(featurePath, payload)
		}); err != nil {
			t.Fatal(err)
		}
		return
	case "rolling-back", "cleanup":
		payload.Transaction.Rollback.StartedAt = time.Now().UTC().Format(time.RFC3339)
		payload.Transaction.Phase = SyncTxnRollingBack
		if kind == "cleanup" {
			payload.Transaction.Phase = SyncTxnCleanup
		}
	default:
		t.Fatalf("unknown recovery kind %q", kind)
	}
	if err := SaveSyncRunState(featurePath, payload); err != nil {
		t.Fatal(err)
	}
}

func setCheckoutObservabilityRecovery(t *testing.T, featurePath string, tx *CheckoutTransaction, kind string) {
	t.Helper()
	switch kind {
	case "forward":
	case "published":
		tx.Push = true
		intent, err := SyncPreparePublication(tx.Transaction, tx.Transaction.Repositories[0].Root, "child", "child")
		if err != nil {
			t.Fatal(err)
		}
		if err := SyncBeginPublication(tx.Transaction, intent, func() error {
			return SaveCheckoutTransaction(featurePath, tx)
		}); err != nil {
			t.Fatal(err)
		}
		return
	case "rolling-back", "cleanup":
		tx.Transaction.Rollback.StartedAt = time.Now().UTC().Format(time.RFC3339)
		tx.Transaction.Phase = SyncTxnRollingBack
		if kind == "cleanup" {
			tx.Transaction.Phase = SyncTxnCleanup
		}
	default:
		t.Fatalf("unknown recovery kind %q", kind)
	}
	if err := SaveCheckoutTransaction(featurePath, tx); err != nil {
		t.Fatal(err)
	}
}

func assertRecoveryGuidance(t *testing.T, guidance, kind string) {
	t.Helper()
	switch kind {
	case "published":
		if !strings.Contains(guidance, "--continue") || strings.Contains(guidance, "--abort") {
			t.Fatalf("published guidance = %q, want forward-only --continue", guidance)
		}
	case "rolling-back", "cleanup":
		if !strings.Contains(guidance, "--abort") || strings.Contains(guidance, "--continue") {
			t.Fatalf("rollback guidance = %q, want only --abort", guidance)
		}
	case "forward":
		if !strings.Contains(guidance, "--continue") || !strings.Contains(guidance, "--abort") {
			t.Fatalf("forward guidance = %q, want continue or abort", guidance)
		}
	default:
		t.Fatalf("unknown recovery kind %q", kind)
	}
}

func assertValidatorMutationGuidance(t *testing.T, guidance string) {
	t.Helper()
	for _, required := range []string{"preserve", "journal", "working tree", "manually"} {
		if !strings.Contains(guidance, required) {
			t.Fatalf("validator-mutation guidance %q lacks %q", guidance, required)
		}
	}
	for _, forbidden := range []string{"--continue", "--abort"} {
		if strings.Contains(guidance, forbidden) {
			t.Fatalf("validator-mutation guidance %q recommends %s", guidance, forbidden)
		}
	}
}

func recordExternalValidatorMutation(t *testing.T, featurePath string, payload *SyncRunState) {
	t.Helper()
	repo := payload.Transaction.Repositories[0].Root
	save := func() error { return SaveSyncRunState(featurePath, payload) }
	if err := BeginSyncValidation(payload.Transaction, repo, "child", repo, save); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, repo, "commit", "--allow-empty", "-qm", "validator moved main")
	if err := CompleteSyncValidation(payload.Transaction, nil, save); err == nil {
		t.Fatal("validator ref mutation was not detected")
	}
	payload.Stage = SyncStageFailed
	payload.FailedBranch = "child"
	if err := SaveSyncRunState(featurePath, payload); err != nil {
		t.Fatal(err)
	}
}

func recordCheckoutValidatorMutation(t *testing.T, featurePath string, tx *CheckoutTransaction) {
	t.Helper()
	repo := tx.Transaction.Repositories[0].Root
	save := func() error { return SaveCheckoutTransaction(featurePath, tx) }
	if err := BeginSyncValidation(tx.Transaction, repo, "child", repo, save); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, repo, "commit", "--allow-empty", "-qm", "validator moved main")
	if err := CompleteSyncValidation(tx.Transaction, nil, save); err == nil {
		t.Fatal("validator ref mutation was not detected")
	}
	tx.Stage = StagePlanned
	tx.FailureKind = FailValidation
	tx.FailureMsg = "validator changed refs"
	if err := SaveCheckoutTransaction(featurePath, tx); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionalSyncObservabilityExternalRecoveryGuidance(t *testing.T) {
	for _, kind := range []string{"forward", "published", "rolling-back", "cleanup"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			ws, featurePath, payload := newExternalTransactionalObservabilityFixture(t)
			setExternalObservabilityRecovery(t, featurePath, payload, kind)
			proc := fakeProcessProber{probe: map[int]ProcessLiveness{999091: ProcessDead}}
			report := buildStatus(t, ws, statusOpts(proc, nil))
			issue := hasIssue(report, IssueSyncStale)
			if issue == nil || issue.Guidance == nil {
				t.Fatalf("transactional external status issue = %+v", issue)
			}
			assertRecoveryGuidance(t, *issue.Guidance, kind)
			if kind == "cleanup" && !strings.Contains(issue.Message, "cleanup") {
				t.Fatalf("cleanup message = %q", issue.Message)
			}

			health := ExternalSyncRecoveryHealthIssues("auth", featurePath, false)
			if len(health) != 1 {
				t.Fatalf("external doctor issues = %+v", health)
			}
			assertRecoveryGuidance(t, health[0].Hint, kind)
		})
	}
}

func TestTransactionalSyncObservabilityCheckoutRecoveryGuidance(t *testing.T) {
	for _, kind := range []string{"forward", "published", "rolling-back", "cleanup"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			ws, featurePath, tx := newCheckoutTransactionalObservabilityFixture(t)
			setCheckoutObservabilityRecovery(t, featurePath, tx, kind)

			doctor, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{
				Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(doctor.Sync) != 1 {
				t.Fatalf("checkout doctor sync reports = %+v", doctor.Sync)
			}
			report := doctor.Sync[0]
			if report.Liveness != "stale" || report.recovery == nil {
				t.Fatalf("checkout doctor projection = %+v", report)
			}
			assertRecoveryGuidance(t, report.Guidance, kind)
			if kind == "cleanup" && !strings.Contains(report.recovery.message, "cleanup") {
				t.Fatalf("cleanup message = %q", report.recovery.message)
			}

			status := buildStatus(t, ws, statusOpts(nil, nil))
			issue := hasIssue(status, IssueSyncStale)
			if issue == nil || issue.Guidance == nil {
				t.Fatalf("transactional checkout status issue = %+v", issue)
			}
			assertRecoveryGuidance(t, *issue.Guidance, kind)
		})
	}
}

func TestTransactionalSyncObservabilityValidatorMutationRequiresManualRecovery(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		ws, featurePath, payload := newExternalTransactionalObservabilityFixture(t)
		recordExternalValidatorMutation(t, featurePath, payload)

		proc := fakeProcessProber{probe: map[int]ProcessLiveness{999091: ProcessDead}}
		status := buildStatus(t, ws, statusOpts(proc, nil))
		featureIssue := hasIssue(status, IssueSyncStale)
		if featureIssue == nil || featureIssue.Guidance == nil {
			t.Fatalf("external validator-mutation feature issue = %+v", featureIssue)
		}
		assertValidatorMutationGuidance(t, *featureIssue.Guidance)
		entryIssue := hasIssue(status, IssueSyncFailedBranch)
		if entryIssue == nil || entryIssue.Guidance == nil {
			t.Fatalf("external validator-mutation entry issue = %+v", entryIssue)
		}
		assertValidatorMutationGuidance(t, *entryIssue.Guidance)

		health := ExternalSyncRecoveryHealthIssues("auth", featurePath, false)
		if len(health) != 1 {
			t.Fatalf("external validator-mutation doctor issues = %+v", health)
		}
		assertValidatorMutationGuidance(t, health[0].Hint)
	})

	t.Run("checkout", func(t *testing.T) {
		ws, featurePath, tx := newCheckoutTransactionalObservabilityFixture(t)
		recordCheckoutValidatorMutation(t, featurePath, tx)

		doctor, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{
			Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(doctor.Sync) != 1 || doctor.Sync[0].recovery == nil {
			t.Fatalf("checkout validator-mutation doctor sync = %+v", doctor.Sync)
		}
		assertValidatorMutationGuidance(t, doctor.Sync[0].Guidance)
		foundProtectedAncestry := false
		for _, entry := range doctor.Features {
			if entry.AncestryStatus != AncestryStatusCurrent {
				foundProtectedAncestry = true
				if entry.Reason == "" || entry.Guidance != "" {
					t.Fatalf("protected checkout ancestry lost reason or retained repair guidance: %+v", entry)
				}
			}
		}
		if !foundProtectedAncestry {
			t.Fatal("validator mutation fixture did not produce a non-current ancestry fact")
		}

		status := buildStatus(t, ws, statusOpts(nil, nil))
		featureIssue := hasIssue(status, IssueSyncStale)
		if featureIssue == nil || featureIssue.Guidance == nil {
			t.Fatalf("checkout validator-mutation status issue = %+v", featureIssue)
		}
		assertValidatorMutationGuidance(t, *featureIssue.Guidance)
		failureIssue := hasIssue(status, IssueSyncFailed)
		if failureIssue == nil || failureIssue.Guidance == nil {
			t.Fatalf("checkout validator-mutation failure issue = %+v", failureIssue)
		}
		assertValidatorMutationGuidance(t, *failureIssue.Guidance)
		if issue := hasIssue(status, IssueSyncCurrentBranch); issue != nil {
			t.Fatalf("validator-mutated checkout emitted a misleading current-work issue: %+v", issue)
		}
	})
}

func TestTransactionalSyncObservabilityCorruptEvidenceFailsClosed(t *testing.T) {
	t.Run("external-v4", func(t *testing.T) {
		ws, featurePath, _ := newExternalTransactionalObservabilityFixture(t)
		path := SyncRunStatePath(featurePath)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, []byte("unknown_transactional_field: true\n")...)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		status := buildStatus(t, ws, statusOpts(nil, nil))
		issue := hasIssue(status, IssueSyncStateInvalid)
		if issue == nil || issue.Guidance == nil ||
			!strings.Contains(*issue.Guidance, "preserve and inspect") ||
			strings.Contains(*issue.Guidance, "--abort") {
			t.Fatalf("corrupt external guidance = %+v", issue)
		}
		health := ExternalSyncRecoveryHealthIssues("auth", featurePath, false)
		if len(health) != 1 || health[0].Severity != SeverityError ||
			!strings.Contains(health[0].Hint, "preserve and inspect") {
			t.Fatalf("corrupt external doctor projection = %+v", health)
		}
	})

	t.Run("external-malformed-before-version", func(t *testing.T) {
		ws, featurePath, _ := newExternalTransactionalObservabilityFixture(t)
		path := SyncRunStatePath(featurePath)
		if err := os.WriteFile(path, []byte("state_version: [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		status := buildStatus(t, ws, statusOpts(nil, nil))
		issue := hasIssue(status, IssueSyncStateInvalid)
		if issue == nil || issue.Guidance == nil ||
			!strings.Contains(*issue.Guidance, "preserve and inspect") ||
			strings.Contains(*issue.Guidance, "remove") {
			t.Fatalf("malformed external guidance = %+v", issue)
		}
		health := ExternalSyncRecoveryHealthIssues("auth", featurePath, false)
		if len(health) != 1 || !strings.Contains(health[0].Hint, "preserve and inspect") ||
			strings.Contains(health[0].Hint, "remove") {
			t.Fatalf("malformed external doctor projection = %+v", health)
		}
	})

	t.Run("checkout-v5", func(t *testing.T) {
		ws, featurePath, tx := newCheckoutTransactionalObservabilityFixture(t)
		limit := 1
		tx.StateVersion = CheckoutTransactionTransactionalGuardedVersion
		tx.PlanGuarded = true
		tx.MaxReplayTotal = &limit
		if err := SaveCheckoutTransaction(featurePath, tx); err != nil {
			t.Fatal(err)
		}
		path := CheckoutTransactionPath(featurePath)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, []byte("unknown_transactional_field: true\n")...)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		doctor, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{
			Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(doctor.Sync) != 1 {
			t.Fatalf("checkout doctor sync reports = %+v", doctor.Sync)
		}
		report := doctor.Sync[0]
		if report.Liveness != "invalid" ||
			!strings.Contains(report.Guidance, "preserve and inspect") ||
			strings.Contains(report.Guidance, "--abort") {
			t.Fatalf("corrupt checkout doctor projection = %+v", report)
		}
		status := buildStatus(t, ws, statusOpts(nil, nil))
		issue := hasIssue(status, IssueSyncInvalid)
		if issue == nil || issue.Guidance == nil ||
			!strings.Contains(*issue.Guidance, "preserve and inspect") ||
			strings.Contains(*issue.Guidance, "--abort") {
			t.Fatalf("corrupt checkout status guidance = %+v", issue)
		}
	})

	t.Run("checkout-malformed-before-version", func(t *testing.T) {
		ws, featurePath, _ := newCheckoutTransactionalObservabilityFixture(t)
		path := CheckoutTransactionPath(featurePath)
		if err := os.WriteFile(path, []byte("state_version: [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		doctor, err := BuildCheckoutHealthReport(ws, &CheckoutHealthOpts{
			Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(doctor.Sync) != 1 || !strings.Contains(doctor.Sync[0].Guidance, "preserve and inspect") ||
			strings.Contains(doctor.Sync[0].Guidance, "remove") {
			t.Fatalf("malformed checkout doctor projection = %+v", doctor.Sync)
		}
		status := buildStatus(t, ws, statusOpts(nil, nil))
		issue := hasIssue(status, IssueSyncInvalid)
		if issue == nil || issue.Guidance == nil ||
			!strings.Contains(*issue.Guidance, "preserve and inspect") ||
			strings.Contains(*issue.Guidance, "remove") {
			t.Fatalf("malformed checkout status guidance = %+v", issue)
		}
	})
}

func TestTransactionalSyncObservabilityNoStateCompatibility(t *testing.T) {
	external, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "child", Base: "main"}})
	status := buildStatus(t, external, statusOpts(nil, nil))
	if feature := findFeature(t, status, "auth"); feature.Sync != nil {
		t.Fatalf("external no-state projection = %+v", feature.Sync)
	}
	if issues := ExternalSyncRecoveryHealthIssues("auth", featurePath, false); len(issues) != 0 {
		t.Fatalf("external no-state doctor issues = %+v", issues)
	}

	f := newTransactionalLoadFixture(t)
	metadataRoot := filepath.Dir(filepath.Dir(f.featurePath))
	checkout := Workspace{
		RepoRoot: f.repo, Mode: ModeCheckout, MetadataRoot: metadataRoot,
		StableID: stableID(canonicalize(f.repo)), Caps: capsFor(ModeCheckout),
	}
	status = buildStatus(t, checkout, statusOpts(nil, nil))
	if feature := findFeature(t, status, "feature"); feature.Sync != nil {
		t.Fatalf("checkout no-state projection = %+v", feature.Sync)
	}
	doctor, err := BuildCheckoutHealthReport(checkout, &CheckoutHealthOpts{
		Proc: fakeProcessChecker{}, Tmux: fakeTmuxChecker{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(doctor.Sync) != 0 {
		t.Fatalf("checkout no-state doctor reports = %+v", doctor.Sync)
	}
}
