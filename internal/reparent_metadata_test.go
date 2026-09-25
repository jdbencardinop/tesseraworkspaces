package internal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2).
//
//	T-060 durableWriteFile fsync behaviour and both fault windows .. AC-072
// ---------------------------------------------------------------------------

// TestWriteStackBytesAtomic_WritesExactBytes proves the primitive writes the
// captured bytes verbatim. A pre-image restored by re-marshalling a decoded
// struct is not a restore — YAML round-tripping can reorder keys, drop
// comments and change quoting — so byte fidelity is the whole point.
func TestWriteStackBytesAtomic_WritesExactBytes(t *testing.T) {
	featurePath := filepath.Join(t.TempDir(), "feature")

	original := "# operator comment\nbranches:\n  - name: pr1\n    base: 'main'\n"
	if err := WriteStackBytesAtomic(featurePath, []byte(original)); err != nil {
		t.Fatalf("WriteStackBytesAtomic: %v", err)
	}

	got, err := os.ReadFile(StackPath(featurePath))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != original {
		t.Fatalf("stack.yaml = %q, want %q", got, original)
	}

	info, err := os.Stat(StackPath(featurePath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("stack.yaml mode = %o, want 644", perm)
	}
}

// TestSaveStackAtomic_MarshalsExactlyAsSaveStack proves the atomic writer is
// byte-compatible with the frozen SaveStack: same marshalling, different
// durability.
func TestSaveStackAtomic_MarshalsExactlyAsSaveStack(t *testing.T) {
	stack := Stack{Branches: []StackEntry{
		{Name: "pr1", Base: "main", LastBaseSHA: "abc"},
		{Name: "pr2", Branch: "feature/pr2", Base: "pr1", Repo: "/repo"},
	}}

	legacyPath := filepath.Join(t.TempDir(), "legacy")
	if err := os.MkdirAll(legacyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveStack(legacyPath, stack); err != nil {
		t.Fatalf("SaveStack: %v", err)
	}
	atomicPath := filepath.Join(t.TempDir(), "atomic")
	if err := SaveStackAtomic(atomicPath, stack); err != nil {
		t.Fatalf("SaveStackAtomic: %v", err)
	}

	legacyBytes, err := os.ReadFile(StackPath(legacyPath))
	if err != nil {
		t.Fatal(err)
	}
	atomicBytes, err := os.ReadFile(StackPath(atomicPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(legacyBytes) != string(atomicBytes) {
		t.Fatalf("SaveStackAtomic bytes differ from SaveStack:\n%q\n%q", atomicBytes, legacyBytes)
	}
}

// TestDurableWriteFile_FaultBeforeRenamePreservesPreviousFile proves window 1
// of the §10.3b seam: the destination still holds its previous bytes, so a
// caller can report "nothing was written" honestly.
func TestDurableWriteFile_FaultBeforeRenamePreservesPreviousFile(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-060", "durable-writer-faults")
	_ = "asserts AC-072"
	featurePath := filepath.Join(t.TempDir(), "feature")
	previous := "branches:\n  - name: pr1\n    base: main\n"
	if err := WriteStackBytesAtomic(featurePath, []byte(previous)); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected write-stack fault")
	calls := 0
	SyncStateIOFault = func(op, path string) error {
		if op != SyncIOWriteStack || path != StackPath(featurePath) {
			return nil
		}
		calls++
		if calls == 1 {
			return injected
		}
		return nil
	}
	t.Cleanup(func() { SyncStateIOFault = nil })

	err := WriteStackBytesAtomic(featurePath, []byte("branches: []\n"))
	if !errors.Is(err, injected) {
		t.Fatalf("error = %v, want the injected fault", err)
	}
	if calls != 1 {
		t.Fatalf("seam consulted %d times before the rename, want 1", calls)
	}
	got, err := os.ReadFile(StackPath(featurePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != previous {
		t.Fatalf("previous bytes did not survive: %q", got)
	}
	if leftovers := durableTempLeftovers(t, featurePath); leftovers != 0 {
		t.Fatalf("%d temp files were left behind", leftovers)
	}
}

// TestDurableWriteFile_FaultAfterRenameKeepsNewBytes proves window 2: the
// rename landed and only the directory fsync was skipped, so the new bytes
// are on disk and recovery must treat the write as "may have happened".
func TestDurableWriteFile_FaultAfterRenameKeepsNewBytes(t *testing.T) {
	featurePath := filepath.Join(t.TempDir(), "feature")
	if err := WriteStackBytesAtomic(featurePath, []byte("branches: []\n")); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected post-rename fault")
	calls := 0
	SyncStateIOFault = func(op, path string) error {
		if op != SyncIOWriteStack {
			return nil
		}
		calls++
		if calls == 2 {
			return injected
		}
		return nil
	}
	t.Cleanup(func() { SyncStateIOFault = nil })

	updated := "branches:\n  - name: pr2\n    base: refs/heads/main\n"
	err := WriteStackBytesAtomic(featurePath, []byte(updated))
	if !errors.Is(err, injected) {
		t.Fatalf("error = %v, want the injected fault", err)
	}
	if calls != 2 {
		t.Fatalf("seam consulted %d times, want 2 (before and after the rename)", calls)
	}
	got, err := os.ReadFile(StackPath(featurePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != updated {
		t.Fatalf("new bytes are not on disk after the rename: %q", got)
	}
}

// TestDurableWriteFile_TokenlessWriterNeverConsultsSeam proves the tokenless
// helper is inert with respect to the fault seam, so a caller that has no
// §10.3b token cannot accidentally inherit another artifact's injected fault.
func TestDurableWriteFile_TokenlessWriterNeverConsultsSeam(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "artifact.yaml")
	consulted := 0
	SyncStateIOFault = func(op, path string) error {
		consulted++
		return errors.New("must not be consulted")
	}
	t.Cleanup(func() { SyncStateIOFault = nil })

	if err := durableWriteFile(path, []byte("payload\n"), 0o600); err != nil {
		t.Fatalf("durableWriteFile: %v", err)
	}
	if consulted != 0 {
		t.Fatalf("seam consulted %d times, want 0", consulted)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
}

// TestSyncIOFaultTokens_AppendedNeverReordered pins the shipped eleven tokens
// and the four appended reparent tokens. Their order is part of the seam's
// contract: a test that injects "the tenth token" must keep meaning the same
// operation after this feature lands.
func TestSyncIOFaultTokens_AppendedNeverReordered(t *testing.T) {
	shipped := []string{
		SyncIOReadSyncState, SyncIOReadSyncRunState, SyncIORemoveSyncState,
		SyncIORemoveSyncRunState, SyncIORemoveSyncRunGuard, SyncIOWriteSentinel,
		SyncIORestoreSyncState, SyncIORemoveStateUnchanged, SyncIOWriteSyncRunState,
		SyncIOWriteTransaction, SyncIOReloadStack,
	}
	wantShipped := []string{
		"read-sync-state", "read-sync-run-state", "remove-sync-state",
		"remove-sync-run-state", "remove-sync-run-guard", "write-sentinel",
		"restore-sync-state", "remove-state-unchanged", "write-sync-run-state",
		"write-checkout-tx", "reload-stack",
	}
	for i := range shipped {
		if shipped[i] != wantShipped[i] {
			t.Fatalf("shipped token %d = %q, want %q", i, shipped[i], wantShipped[i])
		}
	}

	appended := []string{SyncIOWriteStack, SyncIOWriteReparentState, SyncIOWriteReparentCompat, SyncIOWriteReparentRemote}
	wantAppended := []string{"write-stack", "write-reparent-state", "write-reparent-compat", "write-reparent-remote"}
	for i := range appended {
		if appended[i] != wantAppended[i] {
			t.Fatalf("appended token %d = %q, want %q", i, appended[i], wantAppended[i])
		}
	}
}

// durableTempLeftovers counts the writer's own temp files in a directory.
func durableTempLeftovers(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tws-durable-") {
			count++
		}
	}
	return count
}
