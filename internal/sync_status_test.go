package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// tws status is a read-only, marker-aware projection of the external sync
// state (§11.1). It never mutates, never exposes the marker, and never adds a
// key, an enum value, or a schema version.
// ---------------------------------------------------------------------------

const statusMarker = "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock"

func writeStatusSentinel(t *testing.T, featurePath string) {
	t.Helper()
	s := NewSyncState()
	s.FailedBranch = statusMarker
	s.Pending = []string{}
	s.Completed = []string{}
	s.Skipped = []string{}
	if err := SaveSyncState(featurePath, s); err != nil {
		t.Fatal(err)
	}
}

func writeStatusPayload(t *testing.T, featurePath, failed, token string) {
	t.Helper()
	p := NewSyncRunState("auth", statusMarker, token, SyncRunPolicy{
		Fetch: SyncFetchEnabled, Propagation: SyncPropagationFull, ScopeKind: SyncScopeOne, Selector: failed,
	})
	p.Selected = []string{failed}
	p.Pending = []string{failed}
	p.Completed = []string{}
	p.FailedBranch = failed
	p.Stage = SyncStageFailed
	if err := SaveSyncRunState(featurePath, p); err != nil {
		t.Fatal(err)
	}
}

func writeStatusGuard(t *testing.T, featurePath string, pid int, token string) {
	t.Helper()
	body := "pid: " + itoa(pid) + "\ncreated: \"2026-01-01T00:00:00Z\"\ntoken: \"" + token + "\"\nstate_version: 2\n"
	if err := os.WriteFile(SyncRunGuardPath(featurePath), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func syncStatusFileHashes(t *testing.T, featurePath string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, path := range []string{SyncStatePath(featurePath), SyncRunStatePath(featurePath), SyncRunGuardPath(featurePath)} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out[path] = string(data)
	}
	return out
}

func TestAgentStatus_ScopedSyncStaleProjectsRealNames(t *testing.T) {
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	writeStatusSentinel(t, featurePath)
	writeStatusPayload(t, featurePath, "api", "tok")
	writeStatusGuard(t, featurePath, 999002, "tok")
	before := syncStatusFileHashes(t, featurePath)

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{999002: ProcessDead}}
	r := buildStatus(t, ws, statusOpts(proc, nil))

	f := findFeature(t, r, "auth")
	if f.Sync == nil || f.Sync.Kind != "external" {
		t.Fatalf("sync projection = %+v", f.Sync)
	}
	if f.Sync.FailedBranch == nil || *f.Sync.FailedBranch != "api" {
		t.Fatalf("the projection must name the REAL entry, got %+v", f.Sync.FailedBranch)
	}
	if f.Sync.Liveness == nil || *f.Sync.Liveness != "stale" {
		t.Fatalf("liveness = %+v, want stale", f.Sync.Liveness)
	}
	if f.Sync.LockPID == nil || *f.Sync.LockPID != 999002 {
		t.Fatalf("lock_pid = %+v", f.Sync.LockPID)
	}
	if hasIssue(r, IssueSyncStale) == nil {
		t.Fatalf("expected sync-stale, got %v", issueCodes(r))
	}
	assertNoMarkerAnywhere(t, r)

	if after := syncStatusFileHashes(t, featurePath); len(after) != len(before) {
		t.Fatal("status must not delete state")
	} else {
		for path, body := range before {
			if after[path] != body {
				t.Fatalf("status rewrote %s", path)
			}
		}
	}
}

func TestAgentStatus_LiveGuardDominatesTransientCells(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, featurePath string)
	}{
		{"sentinel-only", func(t *testing.T, fp string) { writeStatusSentinel(t, fp) }},
		{"sentinel-and-payload", func(t *testing.T, fp string) {
			writeStatusSentinel(t, fp)
			writeStatusPayload(t, fp, "api", "tok")
		}},
		{"payload-only", func(t *testing.T, fp string) { writeStatusPayload(t, fp, "api", "tok") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
			tc.setup(t, featurePath)
			writeStatusGuard(t, featurePath, 4242, "tok")

			proc := fakeProcessProber{probe: map[int]ProcessLiveness{4242: ProcessLive}}
			r := buildStatus(t, ws, statusOpts(proc, nil))
			if hasIssue(r, IssueSyncInProgress) == nil {
				t.Fatalf("a live owning guard must project in-progress, got %v", issueCodes(r))
			}
			if hasIssue(r, IssueSyncStale) != nil || hasIssue(r, IssueSyncInvalid) != nil {
				t.Fatalf("no degenerate warning may fire under a live guard: %v", issueCodes(r))
			}
			f := findFeature(t, r, "auth")
			if f.Sync == nil || f.Sync.Liveness == nil || *f.Sync.Liveness != "live" {
				t.Fatalf("liveness = %+v", f.Sync)
			}
			assertNoMarkerAnywhere(t, r)
		})
	}
}

func TestAgentStatus_GuardOnlyResidue(t *testing.T) {
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	writeStatusGuard(t, featurePath, 999002, "tok")

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{999002: ProcessDead}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	iss := hasIssue(r, IssueSyncStale)
	if iss == nil {
		t.Fatalf("guard-only residue must be reported, got %v", issueCodes(r))
	}
	hint := ""
	if iss.Guidance != nil {
		hint = *iss.Guidance
	}
	if strings.Contains(hint, "--abort") {
		t.Fatalf("--abort does not clear guard-only residue; hint = %q", hint)
	}
	if !strings.Contains(hint, SyncRunGuardPath(featurePath)) {
		t.Fatalf("the hint must name the guard path; got %q", hint)
	}
}

func TestAgentStatus_Cell2NamesTheRealFailedEntry(t *testing.T) {
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	writeStatusPayload(t, featurePath, "api", "tok")

	r := buildStatus(t, ws, statusOpts(nil, nil))
	if hasIssue(r, IssueSyncInvalid) == nil {
		t.Fatalf("cell 2 with a stale guard is a warning, got %v", issueCodes(r))
	}
	f := findFeature(t, r, "auth")
	if f.Sync == nil || f.Sync.FailedBranch == nil || *f.Sync.FailedBranch != "api" {
		t.Fatalf("cell-2 projection = %+v", f.Sync)
	}
	assertNoMarkerAnywhere(t, r)
}

func TestAgentStatus_UnreadablePayloadIsReported(t *testing.T) {
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	if err := os.WriteFile(SyncRunStatePath(featurePath), []byte("state_version: 99\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := buildStatus(t, ws, statusOpts(nil, nil))
	if hasIssue(r, IssueSyncStateInvalid) == nil {
		t.Fatalf("an unreadable payload must be reported, got %v", issueCodes(r))
	}
}

func TestAgentStatus_Cell7And10DelegateToTodaysProjection(t *testing.T) {
	// cell 7 — real legacy state beside no payload.
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	state := NewSyncState()
	state.FailedBranch = "api"
	state.Pending = []string{"api"}
	if err := SaveSyncState(featurePath, state); err != nil {
		t.Fatal(err)
	}
	r := buildStatus(t, ws, statusOpts(nil, nil))
	f := findFeature(t, r, "auth")
	if f.Sync == nil || f.Sync.FailedBranch == nil || *f.Sync.FailedBranch != "api" {
		t.Fatalf("cell 7 must delegate unchanged: %+v", f.Sync)
	}
	if f.Sync.Liveness != nil || f.Sync.LockPID != nil {
		t.Fatalf("cell 7 gains no liveness keys: %+v", f.Sync)
	}
	if hasIssue(r, IssueSyncFailedBranch) == nil {
		t.Fatalf("attributeSyncBranch must still fire: %v", issueCodes(r))
	}

	// cell 10 — corrupt legacy state beside no payload.
	ws2, fp2 := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	if err := os.WriteFile(SyncStatePath(fp2), []byte("pending: [\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r2 := buildStatus(t, ws2, statusOpts(nil, nil))
	if hasIssue(r2, IssueSyncStateInvalid) == nil {
		t.Fatalf("cell 10 must keep today's issue, got %v", issueCodes(r2))
	}
}

func TestAgentStatus_NoStateAtAllStaysInvisible(t *testing.T) {
	ws, _ := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	r := buildStatus(t, ws, statusOpts(nil, nil))
	f := findFeature(t, r, "auth")
	if f.Sync != nil {
		t.Fatalf("cell 1 with no guard file projects nothing: %+v", f.Sync)
	}
}

func TestAgentStatus_SchemaAndKeysUnchanged(t *testing.T) {
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	writeStatusSentinel(t, featurePath)
	writeStatusPayload(t, featurePath, "api", "tok")
	writeStatusGuard(t, featurePath, 4242, "tok")

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{4242: ProcessLive}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	if r.SchemaVersion != agentStatusSchema {
		t.Fatalf("schema_version = %d, want %d", r.SchemaVersion, agentStatusSchema)
	}
	doc := decodeStatus(t, r)
	assertAgentStatusKeySets(t, doc)
}

func TestAgentStatus_ExternalProjectionLeavesStageNull(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, featurePath string)
	}{
		{
			name: "cell-5-stale",
			setup: func(t *testing.T, featurePath string) {
				writeStatusSentinel(t, featurePath)
				writeStatusPayload(t, featurePath, "api", "tok")
				writeStatusGuard(t, featurePath, 999003, "tok")
			},
		},
		{
			name: "cell-5-live",
			setup: func(t *testing.T, featurePath string) {
				writeStatusSentinel(t, featurePath)
				writeStatusPayload(t, featurePath, "api", "tok")
				writeStatusGuard(t, featurePath, 4242, "tok")
			},
		},
		{
			name: "cell-2",
			setup: func(t *testing.T, featurePath string) {
				writeStatusPayload(t, featurePath, "api", "tok")
			},
		},
		{
			name: "cell-4",
			setup: func(t *testing.T, featurePath string) {
				writeStatusSentinel(t, featurePath)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
			tc.setup(t, featurePath)

			proc := fakeProcessProber{probe: map[int]ProcessLiveness{999003: ProcessDead, 4242: ProcessLive}}
			r := buildStatus(t, ws, statusOpts(proc, nil))
			f := findFeature(t, r, "auth")
			if f.Sync == nil {
				t.Fatal("the external state must be projected")
			}
			// `stage` is the checkout transaction's enum. Populating it from the
			// external payload would add enum values at schema 1 (§11.1 rule 6),
			// and the pending/completed/failed/liveness keys already carry
			// everything the external projection needs.
			if f.Sync.Stage != nil {
				t.Fatalf("external stage = %q, want null", *f.Sync.Stage)
			}
			doc := decodeStatus(t, r)
			assertAgentStatusKeySets(t, doc)
			assertNoMarkerAnywhere(t, r)
		})
	}
}

func assertNoMarkerAnywhere(t *testing.T, r *AgentStatusReport) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "tws-scoped-sync-") {
		t.Fatalf("the marker must never be surfaced:\n%s", data)
	}
}

func TestAgentStatus_PayloadSymlinkIsProjectedAsUnreadable(t *testing.T) {
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	target := filepath.Join(featurePath, "elsewhere.yaml")
	if err := os.WriteFile(target, []byte("state_version: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, SyncRunStatePath(featurePath)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := buildStatus(t, ws, statusOpts(nil, nil))
	if hasIssue(r, IssueSyncStateInvalid) == nil {
		t.Fatalf("a symlinked payload is projected as unreadable, got %v", issueCodes(r))
	}
	// Status never refuses and never follows the link.
	info, err := os.Lstat(SyncRunStatePath(featurePath))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("status must leave the symlink in place")
	}
}
