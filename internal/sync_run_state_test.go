package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Marker grammar (§8.2). Recognition is unexported, so these live in-package.
// ---------------------------------------------------------------------------

func TestIsSyncMarker_Grammar(t *testing.T) {
	const good = "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock"
	if !isSyncMarker(good) {
		t.Fatalf("%q must be recognised", good)
	}
	if len(good) != 53 {
		t.Fatalf("marker length = %d, want 53", len(good))
	}
	bad := []string{
		"",
		"parent",
		"tws-scoped-sync-.lock",
		"tws-scoped-sync-0123456789abcdef0123456789abcde.lock",   // 31 hex
		"tws-scoped-sync-0123456789abcdef0123456789abcdef0.lock", // 33 hex
		"tws-scoped-sync-0123456789ABCDEF0123456789ABCDEF.lock",  // upper case
		"tws-scoped-sync-0123456789abcdef0123456789abcdef",       // no .lock
		"x" + good,
		good + "x",
		"tws-scoped-sync-0123456789abcdef0123456789abcdeg.lock", // non-hex
	}
	for _, s := range bad {
		if isSyncMarker(s) {
			t.Fatalf("%q must not be recognised as a marker", s)
		}
	}
}

func TestSyncMarker_IsSafeSinglePathComponent(t *testing.T) {
	const marker = "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock"
	if strings.ContainsAny(marker, "/\\\x00") {
		t.Fatal("marker must be a safe single path component")
	}
	if marker == "." || marker == ".." || strings.HasPrefix(marker, "-") {
		t.Fatal("marker must be neither a dot entry nor an option-looking token")
	}
	if filepath.Base(marker) != marker {
		t.Fatal("marker must not traverse")
	}
	if !strings.HasSuffix(marker, ".lock") {
		t.Fatal("the .lock suffix is what makes git check-ref-format reject the marker")
	}
}

// ---------------------------------------------------------------------------
// Payload persistence (§8.4)
// ---------------------------------------------------------------------------

func testPolicy() SyncRunPolicy {
	return SyncRunPolicy{
		Fetch:       SyncFetchDisabled,
		Propagation: SyncPropagationLocalOnly,
		ScopeKind:   SyncScopeSubtree,
		Selector:    "parent",
	}
}

func TestSyncRunState_RoundTripAndMode(t *testing.T) {
	dir := t.TempDir()
	payload := NewSyncRunState("auth", "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock", "tok", testPolicy())
	payload.Selected = []string{"parent", "child"}
	payload.Pending = []string{"child"}
	payload.Completed = []string{"parent"}
	payload.FailedBranch = "child"
	payload.Push = true
	if err := SaveSyncRunState(dir, payload); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SyncRunStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("payload mode = %04o, want 0600", info.Mode().Perm())
	}
	got, err := LoadSyncRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Feature != "auth" || got.FailedBranch != "child" || !got.Push {
		t.Fatalf("payload round-trip lost data: %+v", got)
	}
	if got.Policy() != testPolicy() {
		t.Fatalf("policy round-trip = %+v, want %+v", got.Policy(), testPolicy())
	}
	if !HasSyncRunState(dir) {
		t.Fatal("HasSyncRunState must see the payload")
	}
	DeleteSyncRunState(dir)
	if HasSyncRunState(dir) {
		t.Fatal("payload must be gone after delete")
	}
}

func TestSyncRunState_UnsupportedVersionRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(SyncRunStatePath(dir), []byte("state_version: 99\nfeature: auth\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSyncRunState(dir); err == nil {
		t.Fatal("a payload with an unknown state_version must be refused")
	}
}

func TestSyncState_SentinelShapeIsLegacyCompatible(t *testing.T) {
	dir := t.TempDir()
	sentinel := NewSyncState()
	sentinel.FailedBranch = "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock"
	sentinel.Pending = []string{}
	sentinel.Completed = []string{}
	sentinel.Skipped = []string{}
	if err := SaveSyncState(dir, sentinel); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SyncStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("sentinel mode = %04o, want 0644", info.Mode().Perm())
	}
	data, err := os.ReadFile(SyncStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"started_at:", "failed_branch:", "pending: []", "completed: []", "skipped: []"} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("sentinel must be shape-identical to a legacy state file; missing %q in:\n%s", key, data)
		}
	}
	// It must still decode with the legacy loader, so an old binary fails
	// closed with a clean message instead of dereferencing nil.
	state, err := LoadSyncState(dir)
	if err != nil || state == nil {
		t.Fatalf("sentinel must remain decodable by the legacy loader: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Run guard (§8.3)
// ---------------------------------------------------------------------------

func TestSyncRunGuard_ClaimAndRelease(t *testing.T) {
	dir := t.TempDir()
	if err := ClaimSyncRunGuard(dir, "tok"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SyncRunGuardPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("guard mode = %04o, want 0600", info.Mode().Perm())
	}
	guard, err := ReadSyncRunGuard(dir)
	if err != nil {
		t.Fatal(err)
	}
	if guard.PID != os.Getpid() || guard.Token != "tok" || guard.StateVersion != SyncRunStateVersion {
		t.Fatalf("guard = %+v", guard)
	}
	ReleaseSyncRunGuard(dir)
	if _, err := os.Stat(SyncRunGuardPath(dir)); err == nil {
		t.Fatal("guard must be gone after release")
	}
}

func TestSyncRunGuard_LiveGuardIsNeverStolen(t *testing.T) {
	dir := t.TempDir()
	restore := stubSyncProcessAlive(t, func(int) bool { return true })
	defer restore()
	if err := ClaimSyncRunGuard(dir, "first"); err != nil {
		t.Fatal(err)
	}
	err := ClaimSyncRunGuard(dir, "second")
	if err == nil {
		t.Fatal("a live guard must never be stolen")
	}
	if !strings.Contains(err.Error(), "a scoped sync is already running") {
		t.Fatalf("I16 = %q", err.Error())
	}
	if err := ReclaimSyncRunGuard(dir, "second"); err != nil {
		t.Fatalf("the owning PID may always reclaim its own guard: %v", err)
	}
}

func TestSyncRunGuard_StaleGuardWithPayloadRefused(t *testing.T) {
	dir := t.TempDir()
	restore := stubSyncProcessAlive(t, func(int) bool { return false })
	defer restore()
	if err := ClaimSyncRunGuard(dir, "first"); err != nil {
		t.Fatal(err)
	}
	if err := SaveSyncRunState(dir, NewSyncRunState("auth", "m", "first", testPolicy())); err != nil {
		t.Fatal(err)
	}
	err := ClaimSyncRunGuard(dir, "second")
	if err == nil || !strings.Contains(err.Error(), "stale sync guard from PID") {
		t.Fatalf("a stale guard with an existing payload must be refused; got %v", err)
	}
	// A stale guard with no payload is reclaimed silently.
	DeleteSyncRunState(dir)
	if err := ClaimSyncRunGuard(dir, "second"); err != nil {
		t.Fatalf("a stale guard with no payload is reclaimed silently: %v", err)
	}
}

func TestSyncRunGuard_LiveForeignGuardIsNeverReclaimed(t *testing.T) {
	dir := t.TempDir()
	writeGuardFile(t, dir, 999999, "other")
	restore := stubSyncProcessAlive(t, func(int) bool { return true })
	defer restore()
	err := ReclaimSyncRunGuard(dir, "mine")
	if err == nil || !strings.Contains(err.Error(), "cannot reclaim") {
		t.Fatalf("a live foreign guard must never be reclaimed; got %v", err)
	}
}

func TestSyncRunGuard_InvalidPIDIsInvalidNotStale(t *testing.T) {
	dir := t.TempDir()
	writeGuardFile(t, dir, 0, "tok")
	err := ClaimSyncRunGuard(dir, "mine")
	if err == nil || !strings.Contains(err.Error(), "being initialized or is invalid") {
		t.Fatalf("pid <= 0 is invalid, not stale; got %v", err)
	}
	if err := ReclaimSyncRunGuard(dir, "mine"); err == nil {
		t.Fatal("reclaim must refuse an invalid guard too")
	}
}

// ---------------------------------------------------------------------------
// The 12-cell classifier (§8.6)
// ---------------------------------------------------------------------------

func writeGuardFile(t *testing.T, dir string, pid int, token string) {
	t.Helper()
	body := "pid: " + itoa(pid) + "\ncreated: \"2026-01-01T00:00:00Z\"\ntoken: \"" + token + "\"\nstate_version: 2\n"
	if err := os.WriteFile(SyncRunGuardPath(dir), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

func stubSyncProcessAlive(t *testing.T, fn func(int) bool) func() {
	t.Helper()
	old := syncProcessAlive
	syncProcessAlive = fn
	return func() { syncProcessAlive = old }
}

const cellMarker = "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock"

func writeLegacySentinel(t *testing.T, dir string) {
	t.Helper()
	s := NewSyncState()
	s.FailedBranch = cellMarker
	s.Pending = []string{}
	s.Completed = []string{}
	s.Skipped = []string{}
	if err := SaveSyncState(dir, s); err != nil {
		t.Fatal(err)
	}
}

func writeRealLegacy(t *testing.T, dir, failed string) {
	t.Helper()
	s := NewSyncState()
	s.FailedBranch = failed
	if err := SaveSyncState(dir, s); err != nil {
		t.Fatal(err)
	}
}

func writeCorruptLegacy(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(SyncStatePath(dir), []byte("pending: [oops\n\t- broken\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeValidPayload(t *testing.T, dir, failed, token string) {
	t.Helper()
	p := NewSyncRunState("auth", cellMarker, token, testPolicy())
	p.Selected = []string{"parent", "child"}
	p.Pending = []string{"child"}
	p.Completed = []string{"parent"}
	p.FailedBranch = failed
	if err := SaveSyncRunState(dir, p); err != nil {
		t.Fatal(err)
	}
}

func writeUnreadablePayload(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(SyncRunStatePath(dir), []byte("state_version: 77\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyExternalSyncState_TwelveCells(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		cell  SyncStateCell
	}{
		{"1-absent-absent", func(t *testing.T, dir string) {}, 1},
		{"2-absent-valid", func(t *testing.T, dir string) { writeValidPayload(t, dir, "child", "tok") }, 2},
		{"3-absent-unreadable", func(t *testing.T, dir string) { writeUnreadablePayload(t, dir) }, 3},
		{"4-sentinel-absent", func(t *testing.T, dir string) { writeLegacySentinel(t, dir) }, 4},
		{"5-sentinel-valid", func(t *testing.T, dir string) {
			writeLegacySentinel(t, dir)
			writeValidPayload(t, dir, "child", "tok")
		}, 5},
		{"6-sentinel-unreadable", func(t *testing.T, dir string) {
			writeLegacySentinel(t, dir)
			writeUnreadablePayload(t, dir)
		}, 6},
		{"7-real-absent", func(t *testing.T, dir string) { writeRealLegacy(t, dir, "parent") }, 7},
		{"8-real-valid", func(t *testing.T, dir string) {
			writeRealLegacy(t, dir, "parent")
			writeValidPayload(t, dir, "child", "tok")
		}, 8},
		{"9-real-unreadable", func(t *testing.T, dir string) {
			writeRealLegacy(t, dir, "parent")
			writeUnreadablePayload(t, dir)
		}, 9},
		{"10-corrupt-absent", func(t *testing.T, dir string) { writeCorruptLegacy(t, dir) }, 10},
		{"11-corrupt-valid", func(t *testing.T, dir string) {
			writeCorruptLegacy(t, dir)
			writeValidPayload(t, dir, "child", "tok")
		}, 11},
		{"12-corrupt-unreadable", func(t *testing.T, dir string) {
			writeCorruptLegacy(t, dir)
			writeUnreadablePayload(t, dir)
		}, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			st := ClassifyExternalSyncState(dir, SyncClassifyOpts{AlwaysReadGuard: true, Alive: func(int) bool { return false }})
			if st.Cell != tc.cell {
				t.Fatalf("cell = %d, want %d", st.Cell, tc.cell)
			}
			// An empty legacy failed_branch is real legacy state, never a sentinel.
			if tc.cell == 4 || tc.cell == 5 || tc.cell == 6 {
				if st.Marker != cellMarker {
					t.Fatalf("sentinel cells must expose the marker; got %q", st.Marker)
				}
			} else if st.Marker != "" {
				t.Fatalf("non-sentinel cell %d exposed marker %q", tc.cell, st.Marker)
			}
		})
	}
}

func TestClassifyExternalSyncState_EmptyFailedBranchIsRealLegacy(t *testing.T) {
	dir := t.TempDir()
	writeRealLegacy(t, dir, "")
	st := ClassifyExternalSyncState(dir, SyncClassifyOpts{})
	if st.Cell != 7 {
		t.Fatalf("an empty failed_branch is today's stale-edge state (cell 7); got %d", st.Cell)
	}
}

func TestClassifyExternalSyncState_GuardLiveness(t *testing.T) {
	dir := t.TempDir()
	writeLegacySentinel(t, dir)
	writeValidPayload(t, dir, "child", "tok")
	writeGuardFile(t, dir, 4242, "tok")

	live := ClassifyExternalSyncState(dir, SyncClassifyOpts{Alive: func(pid int) bool { return pid == 4242 }})
	if !live.GuardLive || live.Guard == nil || live.Guard.PID != 4242 {
		t.Fatalf("expected a live owning guard, got %+v", live.Guard)
	}
	stale := ClassifyExternalSyncState(dir, SyncClassifyOpts{Alive: func(int) bool { return false }})
	if stale.GuardLive {
		t.Fatal("a dead PID must classify as stale")
	}
	if stale.Cell != 5 {
		t.Fatal("the guard is precedence and context, never a cell axis")
	}

	// A foreign token is never authoritative, even when the PID is alive.
	writeGuardFile(t, dir, 4242, "other")
	foreign := ClassifyExternalSyncState(dir, SyncClassifyOpts{Alive: func(int) bool { return true }})
	if foreign.GuardLive {
		t.Fatal("a guard whose token does not match the payload is never live-owning")
	}
	if !foreign.GuardForeign() {
		t.Fatal("GuardForeign must report the token mismatch")
	}
}

func TestClassifyExternalSyncState_GuardReadGating(t *testing.T) {
	dir := t.TempDir()
	writeGuardFile(t, dir, 4242, "tok")

	// Cell 1 with no payload and no sentinel: a no-flag run never opens the
	// guard, not even to Lstat it.
	quiet := ClassifyExternalSyncState(dir, SyncClassifyOpts{})
	if quiet.HasGuardFile() {
		t.Fatal("a no-flag run with no payload and no sentinel must not consult the guard")
	}
	loud := ClassifyExternalSyncState(dir, SyncClassifyOpts{AlwaysReadGuard: true, Alive: func(int) bool { return true }})
	if loud.Guard == nil {
		t.Fatal("AlwaysReadGuard must open the guard")
	}
}

func TestClassifyExternalSyncState_SymlinkFacts(t *testing.T) {
	if os.Getenv("TWS_SKIP_SYMLINK") == "1" {
		t.Skip("symlinks unavailable")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.yaml")
	if err := os.WriteFile(target, []byte("state_version: 2\nfeature: auth\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, SyncRunStatePath(dir)); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.Symlink(target, SyncRunGuardPath(dir)); err != nil {
		t.Fatal(err)
	}
	st := ClassifyExternalSyncState(dir, SyncClassifyOpts{AlwaysReadGuard: true})
	if !st.PayloadSymlink || st.Payload != nil || st.PayloadErr == nil {
		t.Fatalf("a payload symlink is recorded, never followed: %+v", st)
	}
	if !st.GuardSymlink || st.Guard != nil || st.GuardLive {
		t.Fatalf("a guard symlink is recorded, never followed: %+v", st)
	}
	if st.Cell != 3 {
		t.Fatalf("a symlinked payload classifies as unreadable (cell 3); got %d", st.Cell)
	}
	if st.PayloadPath != SyncRunStatePath(dir) || st.GuardPath != SyncRunGuardPath(dir) || st.LegacyPath != SyncStatePath(dir) {
		t.Fatal("the classifier must record the three consulted paths")
	}
}

func TestClassifyExternalSyncState_LegacySymlinkIsRecordedAndStillFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "legacy.yaml")
	s := NewSyncState()
	s.FailedBranch = "parent"
	if err := SaveSyncState(filepath.Dir(target), s); err != nil {
		t.Fatal(err)
	}
	// SaveSyncState writes .sync-state.yaml; move it aside and link to it.
	if err := os.Rename(SyncStatePath(dir), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, SyncStatePath(dir)); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	st := ClassifyExternalSyncState(dir, SyncClassifyOpts{})
	if !st.LegacySymlink {
		t.Fatal("the legacy symlink fact must be recorded")
	}
	if st.Legacy == nil || st.Legacy.FailedBranch != "parent" {
		t.Fatal("the legacy read still follows the link — frozen behaviour")
	}
	if st.Cell != 7 {
		t.Fatalf("cell = %d, want 7", st.Cell)
	}
}

func TestSyncRunState_PathsAreJoinedFromOneRoot(t *testing.T) {
	dir := filepath.Join("a", "b")
	if SyncRunStatePath(dir) != filepath.Join(dir, ".sync-state.v2.yaml") {
		t.Fatal("payload path")
	}
	if SyncRunGuardPath(dir) != filepath.Join(dir, ".sync-run.lock") {
		t.Fatal("guard path")
	}
	if SyncStatePath(dir) != filepath.Join(dir, ".sync-state.yaml") {
		t.Fatal("legacy path")
	}
}
