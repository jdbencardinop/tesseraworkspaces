package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// Post-change assertions over the pre-change evidence and the new-mode state
// documents. These use the harness pieces that only make sense once production
// has changed.
// ---------------------------------------------------------------------------

// TestSyncTeeSet_IsClosed pins that the wrapper tees exactly the argv shapes
// the comparator's carve-outs need to read a resolved value from.
func TestSyncTeeSet_IsClosed(t *testing.T) {
	if len(syncTeedShapes) != 3 {
		t.Fatalf("the tee set is closed at three shapes; got %v", syncTeedShapes)
	}
	want := map[string]bool{
		c4ContainmentProbe:       true,
		c4DefaultBranchProbeHead: true,
		c4DefaultBranchProbeSym:  true,
	}
	for _, shape := range syncTeedShapes {
		if !want[shape] {
			t.Fatalf("unexpected teed shape %q", shape)
		}
		delete(want, shape)
	}
	if len(want) != 0 {
		t.Fatalf("missing teed shapes: %v", want)
	}
}

// TestSyncDeclaredC1_PostChange asserts the declared corrupt-legacy-state
// behaviour change against the committed pre-change evidence: all three verbs
// changed, and none of them deletes anything.
func TestSyncDeclaredC1_PostChange(t *testing.T) {
	for _, verb := range []struct {
		name string
		args []string
	}{
		{"plain", nil},
		{"continue", []string{"--continue"}},
		{"abort", []string{"--abort"}},
	} {
		t.Run(verb.name, func(t *testing.T) {
			f := newScopedFixture(t)
			corrupt := "pending: [oops\n\t- broken\n"
			if err := os.WriteFile(internal.SyncStatePath(f.featurePath), []byte(corrupt), 0o644); err != nil {
				t.Fatal(err)
			}
			refsBefore := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)")

			args := append([]string{f.feature}, verb.args...)
			_, stderr, exit := runSync(t, args...)

			if exit != 1 {
				t.Fatalf("cell 10 fails closed at exit 1; got %d\n%s", exit, stderr)
			}
			wantPrefix := "sync state at " + internal.SyncStatePath(f.featurePath) + " is unreadable:"
			if !strings.Contains(stderr, wantPrefix) {
				t.Fatalf("stderr = %q, want a message naming the file", stderr)
			}
			if !strings.Contains(stderr, "preserve and inspect") || strings.Contains(stderr, "remove it manually") {
				t.Fatalf("corrupt evidence must be preserved for inspection; got %q", stderr)
			}
			// tws never deletes state it could not read.
			if got := readFileString(t, internal.SyncStatePath(f.featurePath)); got != corrupt {
				t.Fatalf("the corrupt file must be left untouched; got %q", got)
			}
			if refsAfter := gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)"); refsAfter != refsBefore {
				t.Fatal("a fail-closed refusal must move no ref")
			}

			// The declared change is real: the pre-change evidence recorded a
			// different exit code for every verb.
			evidence := syncReadEvidence(t, filepath.Join("declared_c1", verb.name), "stderr.txt")
			preExit := strings.SplitN(evidence, "\n", 2)[0]
			if preExit == "exit: 1" && verb.name != "continue" {
				t.Fatalf("%s: pre-change evidence already exited 1, so the declared change is not visible", verb.name)
			}
		})
	}
}

// TestSyncScoped_StateDocumentShapes compares the two new-mode state documents
// against independently produced references, so every static field is pinned
// and every dynamic field is checked by shape rather than by value.
func TestSyncScoped_StateDocumentShapes(t *testing.T) {
	f := newScopedFixture(t)
	f.makeConflict(t)
	if _, _, exit := runSync(t, f.feature, "--only", "child", "--no-fetch", "--local-only"); exit == 0 {
		t.Fatal("expected a conflict")
	}

	gotPayload := readFileString(t, internal.SyncRunStatePath(f.featurePath))
	gotInfo, statErr := os.Stat(internal.SyncRunStatePath(f.featurePath))
	if statErr != nil {
		t.Fatal(statErr)
	}
	if gotInfo.Mode().Perm() != 0o600 {
		t.Fatalf("payload mode = %o, want 0600", gotInfo.Mode().Perm())
	}
	loaded, err := internal.LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatalf("strict loader rejected generated payload: %v", err)
	}
	if loaded.StateVersion != internal.SyncRunStateTransactionalVersion ||
		loaded.Route != internal.RouteNewMode || loaded.PlanGuarded ||
		loaded.Transaction == nil {
		t.Fatalf("transactional wrapper = %+v", loaded)
	}
	if loaded.FetchPolicy != internal.SyncFetchDisabled ||
		loaded.PropagationPolicy != internal.SyncPropagationLocalOnly ||
		loaded.ScopeKind != internal.SyncScopeOne || loaded.ScopeSelector != "child" ||
		!slices.Equal(loaded.Selected, []string{"child"}) ||
		loaded.FailedBranch != "child" || len(loaded.Pending) != 0 ||
		len(loaded.Completed) != 0 || loaded.ValidationSource != "none" {
		t.Fatalf("frozen wrapper decision/progress = %+v", loaded)
	}

	childStack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	refDir := filepath.Join(t.TempDir(), f.feature)
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stackBytes, err := os.ReadFile(internal.StackPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(internal.StackPath(refDir), stackBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	referenceTx, err := internal.CaptureSyncTransaction(internal.SyncTransactionBeginInput{
		FeaturePath:       refDir,
		Feature:           f.feature,
		Mode:              internal.ModeExternal,
		WorkspaceRepoRoot: f.repo,
		Stack:             childStack,
		Selected:          []string{"child"},
		EntryRepoDirs:     map[string]string{"child": f.wt("child")},
	})
	if err != nil {
		t.Fatal(err)
	}
	actualTx := loaded.Transaction
	if actualTx.EvidenceVersion != internal.SyncTransactionEvidenceVersion ||
		actualTx.WorkspaceMode != string(internal.ModeExternal) ||
		actualTx.Feature != f.feature || actualTx.Phase != internal.SyncTxnFailed ||
		!actualTx.Ready || !slices.Equal(actualTx.Selected, referenceTx.Selected) ||
		actualTx.Metadata.BeforeSHA256 != referenceTx.Metadata.BeforeSHA256 ||
		len(actualTx.Repositories) != len(referenceTx.Repositories) {
		t.Fatalf("transaction identity/preimage differs from independent capture:\nactual=%+v\nreference=%+v", actualTx, referenceTx)
	}
	if len(actualTx.Actions) != 1 {
		t.Fatalf("actions = %+v, want one conflict action", actualTx.Actions)
	}
	action := actualTx.Actions[0]
	if action.Entry != "child" || action.Status != internal.SyncTxnActionIntent ||
		len(action.RefLogAnchors) == 0 || action.ContextReflogAnchor == "" {
		t.Fatalf("conflict action lacks ownership anchors: %+v", action)
	}
	for _, repo := range actualTx.Repositories {
		if len(repo.Refs) == 0 || len(repo.Holders) == 0 {
			t.Fatalf("repository evidence is incomplete: %+v", repo)
		}
		for _, ref := range repo.Refs {
			if !ref.Pinned || ref.PreimageSHA == "" || ref.ExpectedSHA == "" || ref.PinRef == "" {
				t.Fatalf("ref evidence is incomplete: %+v", ref)
			}
		}
	}

	doc := decodeYAMLDoc(t, "payload", []byte(gotPayload))
	wantWrapperKeys := []string{
		"state_version", "feature", "started_at", "updated_at", "marker",
		"owner_token", "stage", "fetch_policy", "propagation_policy",
		"scope_kind", "scope_selector", "selected", "push",
		"validation_source", "failed_branch", "pending", "completed",
		"pushed", "repos", "route", "transaction",
	}
	if got := mappingKeys(doc); !slices.Equal(got, wantWrapperKeys) {
		t.Fatalf("payload wrapper keys = %v, want %v", got, wantWrapperKeys)
	}
	txNode := mappingValue(doc, "transaction")
	wantTransactionKeys := []string{
		"evidence_version", "run_id", "created_at", "updated_at",
		"workspace_mode", "feature", "workspace_repo_root", "phase", "ready",
		"selected", "metadata", "repositories", "actions", "publication", "rollback",
	}
	if got := mappingKeys(txNode); !slices.Equal(got, wantTransactionKeys) {
		t.Fatalf("transaction keys = %v, want %v", got, wantTransactionKeys)
	}
	if got := mappingKeys(mappingValue(txNode, "metadata")); !slices.Equal(got,
		[]string{"path", "before_base64", "before_sha256", "expected_sha256"}) {
		t.Fatalf("metadata keys = %v", got)
	}
	actionNode := mappingValue(txNode, "actions").Content[0]
	wantActionKeys := []string{
		"sequence", "kind", "entry", "repo_common_dir", "context_path",
		"context_before", "context_ref", "allowed_refs", "before_refs", "before_holders", "ref_log_anchors",
		"context_reflog_anchor", "status",
	}
	if got := mappingKeys(actionNode); !slices.Equal(got, wantActionKeys) {
		t.Fatalf("action keys = %v, want %v", got, wantActionKeys)
	}

	// Reference guard: same shape, different dynamic values.
	guardDir := t.TempDir()
	if err := internal.ClaimSyncRunGuard(guardDir, "ffffffffffffffffffffffffffffffff"); err != nil {
		t.Fatal(err)
	}
	wantGuard := readFileString(t, internal.SyncRunGuardPath(guardDir))
	gotGuard := readFileString(t, internal.SyncRunGuardPath(f.featurePath))
	guardInfo, err := os.Stat(internal.SyncRunGuardPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	compareStateSemantic(t, "guard", []byte(wantGuard), []byte(gotGuard), 0o600, guardInfo.Mode(), stateCompareSpec{
		DynamicKeys: syncRunGuardDynamicKeys,
	})

	// The sentinel keeps the legacy shape and mode.
	sentinelInfo, err := os.Stat(internal.SyncStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	if sentinelInfo.Mode().Perm() != 0o644 {
		t.Fatalf("sentinel mode = %04o, want 0644", sentinelInfo.Mode().Perm())
	}
}

// TestSyncScoped_RefusalWritesNothing asserts a validation refusal leaves the
// feature directory byte-for-byte identical, .sync-run.lock included.
func TestSyncScoped_RefusalWritesNothing(t *testing.T) {
	f := newScopedFixture(t)
	before := syncSnapshotFiles(t, f.featurePath)

	for _, args := range [][]string{
		{f.feature, "--only", "nope"},
		{f.feature, "--only", "child", "--from", "parent"},
		{f.feature, "--fetch", "--no-fetch"},
		{f.feature, "--abort", "--local-only"},
	} {
		if _, _, exit := runSync(t, args...); exit == 0 {
			t.Fatalf("%v must be refused", args)
		}
		if after := syncSnapshotFiles(t, f.featurePath); after != before {
			t.Fatalf("%v mutated the feature directory:\n--- before ---\n%s\n--- after ---\n%s", args, before, after)
		}
	}
}
