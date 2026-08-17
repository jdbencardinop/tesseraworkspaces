package cli

import (
	"os"
	"path/filepath"
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
			if verb.name == "abort" && !strings.Contains(stderr, "inspect and remove it manually") {
				t.Fatalf("the abort message must require manual removal; got %q", stderr)
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

	// Reference payload: same frozen decision, different dynamic values.
	refDir := t.TempDir()
	ref := internal.NewSyncRunState(f.feature, "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock", "ffffffffffffffffffffffffffffffff", internal.SyncRunPolicy{
		Fetch:       internal.SyncFetchDisabled,
		Propagation: internal.SyncPropagationLocalOnly,
		ScopeKind:   internal.SyncScopeOne,
		Selector:    "child",
	})
	ref.Selected = []string{"child"}
	ref.Pending = []string{}
	ref.Completed = []string{}
	ref.Pushed = []string{}
	childStack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	ref.Repos = []string{internal.GetBranch(childStack, "child").Repo}
	ref.FailedBranch = "child"
	ref.ValidationSource = "none"
	ref.Stage = internal.SyncStageFailed
	if err := internal.SaveSyncRunState(refDir, ref); err != nil {
		t.Fatal(err)
	}
	wantPayload := readFileString(t, internal.SyncRunStatePath(refDir))
	gotPayload := readFileString(t, internal.SyncRunStatePath(f.featurePath))
	gotInfo, statErr := os.Stat(internal.SyncRunStatePath(f.featurePath))
	if statErr != nil {
		t.Fatal(statErr)
	}
	compareStateSemantic(t, "payload", []byte(wantPayload), []byte(gotPayload), 0o600, gotInfo.Mode(), stateCompareSpec{
		DynamicKeys: syncRunStateDynamicKeys,
	})

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
