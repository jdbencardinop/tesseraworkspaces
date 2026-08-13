package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------- fakes ----------

type fakeTmuxInventory struct{ snap TmuxSnapshot }

func (f fakeTmuxInventory) Snapshot() TmuxSnapshot { return f.snap }

func emptyTmux() TmuxInventoryProbe {
	return fakeTmuxInventory{snap: TmuxSnapshot{Available: true, ServerRunning: true, Sessions: map[string]bool{}, PanesAvailable: true}}
}

func fixedNow() func() time.Time {
	return func() time.Time { return time.Date(2026, 8, 11, 9, 4, 8, 0, time.UTC) }
}

func statusOpts(proc ProcessProber, tmux TmuxInventoryProbe) *AgentStatusOpts {
	if proc == nil {
		proc = fakeProcessProber{}
	}
	if tmux == nil {
		tmux = emptyTmux()
	}
	return &AgentStatusOpts{Proc: proc, Tmux: tmux, Now: fixedNow()}
}

// ---------- external fixture ----------

// setupExternalStatusWorkspace builds a real Git repository with an external
// metadata root and one linked worktree for feature/branch.
func setupExternalStatusWorkspace(t *testing.T, feature string, entries []StackEntry) (Workspace, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, root, "init", "--initial-branch=main", repo)
	gitInTest(t, repo, "commit", "--allow-empty", "-m", "init")

	metadataRoot := repo + ".tws"
	featurePath := filepath.Join(metadataRoot, feature)
	if err := os.MkdirAll(filepath.Join(featurePath, "worktrees"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := SaveStack(featurePath, Stack{Branches: entries}); err != nil {
		t.Fatal(err)
	}

	ws := Workspace{
		RepoRoot:     canonicalize(repo),
		Mode:         ModeExternal,
		MetadataRoot: canonicalize(metadataRoot),
		StableID:     stableID(canonicalize(repo)),
		Caps:         capsFor(ModeExternal),
	}
	return ws, filepath.Join(ws.MetadataRoot, feature)
}

func addExternalWorktree(t *testing.T, ws Workspace, featurePath string, entry StackEntry) string {
	t.Helper()
	wtPath := filepath.Join(featurePath, "worktrees", entry.Name)
	gitInTest(t, ws.RepoRoot, "worktree", "add", "-b", entry.GitBranch(), wtPath)
	return wtPath
}

func buildStatus(t *testing.T, ws Workspace, opts *AgentStatusOpts) *AgentStatusReport {
	t.Helper()
	report, err := BuildAgentStatus(ws, "", opts)
	if err != nil {
		t.Fatalf("BuildAgentStatus: %v", err)
	}
	return report
}

func findEntry(t *testing.T, r *AgentStatusReport, feature, name string) AgentStatusEntry {
	t.Helper()
	for _, f := range r.Features {
		if f.Feature != feature {
			continue
		}
		for _, e := range f.Entries {
			if e.Name == name {
				return e
			}
		}
	}
	t.Fatalf("entry %s/%s not found", feature, name)
	return AgentStatusEntry{}
}

func findFeature(t *testing.T, r *AgentStatusReport, feature string) AgentStatusFeature {
	t.Helper()
	for _, f := range r.Features {
		if f.Feature == feature {
			return f
		}
	}
	t.Fatalf("feature %s not found", feature)
	return AgentStatusFeature{}
}

func issueCodes(r *AgentStatusReport) []string {
	var codes []string
	for _, iss := range r.Issues {
		codes = append(codes, iss.Code)
	}
	sort.Strings(codes)
	return codes
}

func hasIssue(r *AgentStatusReport, code string) *AgentStatusIssue {
	for i := range r.Issues {
		if r.Issues[i].Code == code {
			return &r.Issues[i]
		}
	}
	return nil
}

func encodeStatus(t *testing.T, r *AgentStatusReport) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ---------- closed vocabularies ----------

func TestAgentStatusIssueCodeClosure(t *testing.T) {
	seen := map[string]bool{}
	for _, code := range AgentStatusIssueCodes {
		if seen[code] {
			t.Fatalf("duplicate issue code %q", code)
		}
		seen[code] = true
	}
	if len(AgentStatusIssueCodes) != 45 {
		t.Fatalf("issue code table has %d entries, want 45", len(AgentStatusIssueCodes))
	}
	for _, removed := range []string{"sync-lock-invalid", "feature-tmux-unknown"} {
		if seen[removed] {
			t.Fatalf("deleted code %q must not exist", removed)
		}
	}
}

func TestRollupAttentionTruthTable(t *testing.T) {
	info := []AgentStatusIssue{{Severity: SeverityInfo}}
	warn := []AgentStatusIssue{{Severity: SeverityWarning}}
	cases := []struct {
		presence RuntimePresence
		own      []AgentStatusIssue
		child    bool
		want     AttentionStatus
	}{
		{PresenceAbsent, nil, false, AttentionIdle},
		{PresencePresent, nil, false, AttentionActive},
		{PresencePresent, info, false, AttentionActive},
		{PresencePresent, warn, false, AttentionNeedsAttention},
		{PresenceAbsent, nil, true, AttentionNeedsAttention},
		{PresencePresent, nil, true, AttentionNeedsAttention},
	}
	for i, c := range cases {
		if got := RollupAttention(c.presence, c.own, c.child); got != c.want {
			t.Fatalf("case %d: got %q, want %q", i, got, c.want)
		}
	}
}

func TestRollupPresencePrecedence(t *testing.T) {
	cases := []struct {
		in   []RuntimePresence
		want RuntimePresence
	}{
		{nil, PresenceAbsent},
		{[]RuntimePresence{PresenceAbsent}, PresenceAbsent},
		{[]RuntimePresence{PresenceStale, PresenceAbsent}, PresenceStale},
		{[]RuntimePresence{PresenceStale, PresenceUnknown}, PresenceUnknown},
		{[]RuntimePresence{PresenceStale, PresenceUnknown, PresencePresent}, PresencePresent},
	}
	for i, c := range cases {
		if got := RollupPresence(c.in); got != c.want {
			t.Fatalf("case %d: got %q, want %q", i, got, c.want)
		}
	}
}

// ---------- external topology ----------

func TestAgentStatusExternalMaterialization(t *testing.T) {
	entries := []StackEntry{
		{Name: "api", Branch: "jd/api", Base: "main"},
		{Name: "docs", Base: "api", Archived: true},
		{Name: "gone", Base: "main"},
		{Name: "other", Base: "main", Repo: "/elsewhere"},
	}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])

	r := buildStatus(t, ws, statusOpts(nil, nil))

	api := findEntry(t, r, "auth", "api")
	if api.GitBranch != "jd/api" || api.Name != "api" {
		t.Fatalf("decoupled identity lost: %+v", api)
	}
	if api.Materialization.State != MaterializedPresent {
		t.Fatalf("api materialization = %+v", api.Materialization)
	}
	if api.Materialization.CheckedOutBranch == nil || *api.Materialization.CheckedOutBranch != "jd/api" {
		t.Fatalf("checked out branch = %v", api.Materialization.CheckedOutBranch)
	}
	if api.IsCurrentCheckout != nil {
		t.Fatal("is_current_checkout must be null in external mode")
	}
	if api.Attention.Status != AttentionIdle {
		t.Fatalf("api attention = %+v", api.Attention)
	}

	docs := findEntry(t, r, "auth", "docs")
	if docs.Materialization.State != MaterializedArchived || docs.Attention.Status != AttentionIdle {
		t.Fatalf("archived entry = %+v / %+v", docs.Materialization, docs.Attention)
	}

	gone := findEntry(t, r, "auth", "gone")
	if gone.Materialization.State != MaterializedMissing {
		t.Fatalf("missing entry = %+v", gone.Materialization)
	}
	if gone.Attention.Status != AttentionNeedsAttention {
		t.Fatalf("missing worktree must need attention: %+v", gone.Attention)
	}

	other := findEntry(t, r, "auth", "other")
	if other.Materialization.State != MaterializedCrossRepo {
		t.Fatalf("cross-repo entry = %+v", other.Materialization)
	}
	if other.Attention.Status != AttentionIdle {
		t.Fatal("cross-repo is info only and must not need attention")
	}
	if other.Materialization.RefExists != nil {
		t.Fatal("cross-repo must short-circuit every git probe")
	}

	// The feature and workspace inherit from the missing worktree without
	// owning an issue of their own.
	f := findFeature(t, r, "auth")
	if f.Attention.Status != AttentionNeedsAttention || f.Attention.IssueCount != 0 || len(f.Attention.Codes) != 0 {
		t.Fatalf("feature rollup = %+v", f.Attention)
	}
	if r.Workspace.Attention.Status != AttentionNeedsAttention || r.Workspace.Attention.IssueCount != 0 {
		t.Fatalf("workspace rollup = %+v", r.Workspace.Attention)
	}
	if r.Summary.NeedsAttention+r.Summary.Active+r.Summary.Idle != r.Summary.Entries {
		t.Fatalf("summary sum identity broken: %+v", r.Summary)
	}
}

func TestAgentStatusExternalWrongBranchAndDirty(t *testing.T) {
	entries := []StackEntry{{Name: "api", Branch: "jd/api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	wtPath := addExternalWorktree(t, ws, featurePath, entries[0])
	gitInTest(t, wtPath, "switch", "-c", "somewhere-else")
	if err := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	r := buildStatus(t, ws, statusOpts(nil, nil))
	if hasIssue(r, IssueWorktreeWrongBranch) == nil {
		t.Fatalf("expected worktree-wrong-branch, got %v", issueCodes(r))
	}
	dirty := hasIssue(r, IssueWorktreeDirty)
	if dirty == nil || dirty.Severity != SeverityInfo {
		t.Fatalf("expected an info worktree-dirty, got %v", issueCodes(r))
	}

	// A sync that wants this branch escalates dirt to a warning.
	if err := SaveSyncState(featurePath, &SyncState{FailedBranch: "api"}); err != nil {
		t.Fatal(err)
	}
	r = buildStatus(t, ws, statusOpts(nil, nil))
	if blocking := hasIssue(r, IssueWorktreeDirtyBlocking); blocking == nil || blocking.Severity != SeverityWarning {
		t.Fatalf("expected worktree-dirty-blocking, got %v", issueCodes(r))
	}
	// External sync state uses the tws name axis, not the git branch.
	failed := hasIssue(r, IssueSyncFailedBranch)
	if failed == nil || failed.Name == nil || *failed.Name != "api" {
		t.Fatalf("sync-failed-branch must attach to the tws name: %+v", failed)
	}
	f := findFeature(t, r, "auth")
	if f.Sync == nil || f.Sync.Kind != "external" || f.Sync.FailedBranch == nil || *f.Sync.FailedBranch != "api" {
		t.Fatalf("external sync projection = %+v", f.Sync)
	}
	if f.Sync.Liveness != nil || f.Sync.Stage != nil || f.Sync.LockPID != nil {
		t.Fatal("external sync has no stage, liveness, or lock")
	}
}

func TestAgentStatusExternalSyncStateInvalid(t *testing.T) {
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	if err := os.WriteFile(SyncStatePath(featurePath), []byte("pending: [\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r := buildStatus(t, ws, statusOpts(nil, nil))
	if hasIssue(r, IssueSyncStateInvalid) == nil {
		t.Fatalf("expected sync-state-invalid, got %v", issueCodes(r))
	}
	f := findFeature(t, r, "auth")
	if f.Sync == nil || f.Sync.Liveness == nil || *f.Sync.Liveness != "invalid" {
		t.Fatalf("sync projection = %+v", f.Sync)
	}
}

func TestAgentStatusStackStates(t *testing.T) {
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", []StackEntry{{Name: "api", Base: "main"}})
	other := filepath.Join(ws.MetadataRoot, "billing")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}

	r := buildStatus(t, ws, statusOpts(nil, nil))
	billing := findFeature(t, r, "billing")
	if billing.StackState != StackStateMissing || len(billing.Entries) != 0 {
		t.Fatalf("billing = %+v", billing)
	}
	missing := hasIssue(r, IssueStackMissing)
	if missing == nil || missing.Severity != SeverityInfo {
		t.Fatalf("stack-missing must be info, got %v", issueCodes(r))
	}
	if billing.Attention.Status != AttentionIdle {
		t.Fatalf("an empty but valid feature stays idle: %+v", billing.Attention)
	}

	// A corrupt stack is a warning and never hides the other feature.
	if err := os.WriteFile(StackPath(featurePath), []byte("branches: [\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r = buildStatus(t, ws, statusOpts(nil, nil))
	auth := findFeature(t, r, "auth")
	if auth.StackState != StackStateInvalid || len(auth.Entries) != 0 {
		t.Fatalf("auth = %+v", auth)
	}
	if auth.Attention.Status != AttentionNeedsAttention {
		t.Fatalf("stack-invalid must need attention: %+v", auth.Attention)
	}
	if findFeature(t, r, "billing").StackState != StackStateMissing {
		t.Fatal("a corrupt feature must not hide its siblings")
	}
}

// ---------- direct records ----------

func TestAgentStatusDirectRecords(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])

	liveToken := mustCreateRecord(t, featurePath, DirectSessionRecord{
		Feature: "auth", Name: "api", Path: featurePath, Agent: "claude", Stage: DirectStageAgent,
	})
	branchID := DirectSessionBranchID("auth", "api")
	deadToken := mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: "api"})
	if err := UpdateDirectSession(featurePath, branchID, deadToken, func(r *DirectSessionRecord) {
		r.OwnerPID = 999002
		r.StartedAt = "2020-01-01T00:00:00Z"
	}); err != nil {
		t.Fatal(err)
	}

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{os.Getpid(): ProcessLive, 999002: ProcessDead}}
	before := snapshotDir(t, DirectSessionsDir(featurePath))
	r := buildStatus(t, ws, statusOpts(proc, nil))

	api := findEntry(t, r, "auth", "api")
	if api.RuntimePresence != PresencePresent {
		t.Fatalf("one live record makes the entry present: %q", api.RuntimePresence)
	}
	if api.Attention.Status != AttentionNeedsAttention {
		t.Fatalf("a dead sibling still needs attention: %+v", api.Attention)
	}
	if api.SessionCounts.Total != 2 || api.SessionCounts.Live != 1 || api.SessionCounts.Stale != 1 {
		t.Fatalf("session counts = %+v", api.SessionCounts)
	}
	stale := hasIssue(r, IssueDirectRecordStale)
	if stale == nil || stale.Scope != ScopeEntry {
		t.Fatalf("expected an entry-scoped direct-record-stale, got %v", issueCodes(r))
	}
	if !strings.Contains(stale.Message, DirectRecordID(deadToken)) {
		t.Fatalf("stale message must name the record id: %q", stale.Message)
	}
	// The full token never appears anywhere in the encoded document.
	doc := encodeStatus(t, r)
	if bytes.Contains(doc, []byte(liveToken)) || bytes.Contains(doc, []byte(deadToken)) {
		t.Fatal("a full ownership token must never be emitted")
	}
	if !bytes.Contains(doc, []byte(DirectRecordID(liveToken))) {
		t.Fatal("record_id prefix must be emitted")
	}
	// status never removes a record, not even a provably dead one.
	if after := snapshotDir(t, DirectSessionsDir(featurePath)); after != before {
		t.Fatalf("status must be read-only:\n%s\n---\n%s", before, after)
	}

	// An artificially old but live record still reads present.
	if err := UpdateDirectSession(featurePath, branchID, liveToken, func(rec *DirectSessionRecord) {
		rec.StartedAt = "2020-01-01T00:00:00Z"
	}); err != nil {
		t.Fatal(err)
	}
	r = buildStatus(t, ws, statusOpts(proc, nil))
	if findEntry(t, r, "auth", "api").RuntimePresence != PresencePresent {
		t.Fatal("record age must never move a live record to stale")
	}
}

func TestAgentStatusDirectRecordUnknownAndInvalid(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	branchID := DirectSessionBranchID("auth", "api")
	token := mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: "api"})
	if err := UpdateDirectSession(featurePath, branchID, token, func(r *DirectSessionRecord) {
		r.OwnerPID = 999003
	}); err != nil {
		t.Fatal(err)
	}
	proc := fakeProcessProber{probe: map[int]ProcessLiveness{999003: ProcessUnknown}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	api := findEntry(t, r, "auth", "api")
	if api.RuntimePresence != PresenceUnknown || api.Attention.Status != AttentionNeedsAttention {
		t.Fatalf("EPERM record = %q / %+v", api.RuntimePresence, api.Attention)
	}
	if hasIssue(r, IssueDirectRecordUnknown) == nil {
		t.Fatalf("expected direct-record-unknown, got %v", issueCodes(r))
	}

	// A future schema is unsupported, not invalid.
	dir := filepath.Join(DirectSessionsDir(featurePath), branchID)
	future := strings.Repeat("9", 32)
	if err := os.WriteFile(filepath.Join(dir, future+".json"),
		[]byte(`{"schema_version":99,"token":"`+future+`","feature":"auth","name":"api","owner_pid":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	r = buildStatus(t, ws, statusOpts(proc, nil))
	if hasIssue(r, IssueDirectRecordUnsupported) == nil {
		t.Fatalf("expected direct-record-unsupported, got %v", issueCodes(r))
	}
}

func TestAgentStatusOrphanBranchDirectory(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	// A record for a branch that was renamed away.
	mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: "renamed-away"})

	r := buildStatus(t, ws, statusOpts(nil, nil))
	orphan := hasIssue(r, IssueDirectRecordOrphanBranch)
	if orphan == nil || orphan.Scope != ScopeFeature || orphan.Feature == nil || *orphan.Feature != "auth" {
		t.Fatalf("expected a feature-scoped orphan issue, got %v", issueCodes(r))
	}
	if orphan.Name != nil {
		t.Fatal("a feature-scoped issue has no name")
	}
	api := findEntry(t, r, "auth", "api")
	if len(api.Sessions) != 0 {
		t.Fatal("orphan records must attach to no entry")
	}
	if findFeature(t, r, "auth").Attention.Status != AttentionNeedsAttention {
		t.Fatal("an orphan directory must make the feature need attention")
	}
	if _, err := os.Stat(DirectSessionsDir(featurePath)); err != nil {
		t.Fatalf("status must leave the orphan directory on disk: %v", err)
	}
}

// ---------- checkout topology and session ----------

func TestAgentStatusCheckoutMaterialization(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	addStackEntries(t, ws, "auth", []StackEntry{
		{Name: "api", Branch: "jd/api", Base: "main"},
		{Name: "gone", Base: "main"},
		{Name: "old", Base: "main", Archived: true},
	})
	gitInTest(t, dir, "branch", "jd/api")

	r := buildStatus(t, ws, statusOpts(nil, nil))
	api := findEntry(t, r, "auth", "api")
	if api.Materialization.Kind != MaterializationRef || api.Materialization.State != MaterializedPresent {
		t.Fatalf("api materialization = %+v", api.Materialization)
	}
	if api.Materialization.Path != nil || api.Materialization.Dirty != nil {
		t.Fatal("checkout entries have no per-entry path or dirtiness")
	}
	if api.IsCurrentCheckout == nil || *api.IsCurrentCheckout {
		t.Fatalf("is_current_checkout = %v, want false", api.IsCurrentCheckout)
	}

	gone := findEntry(t, r, "auth", "gone")
	if gone.Materialization.State != MaterializedMissing || gone.Attention.Status != AttentionNeedsAttention {
		t.Fatalf("missing ref = %+v / %+v", gone.Materialization, gone.Attention)
	}
	old := findEntry(t, r, "auth", "old")
	if old.Attention.Status != AttentionIdle {
		t.Fatal("an archived entry with a vanished ref blocks nobody")
	}
	if hasIssue(r, IssueRefMissingArchived) == nil {
		t.Fatalf("expected ref-missing-archived, got %v", issueCodes(r))
	}

	// The checked-out branch reads as current.
	gitInTest(t, dir, "switch", "jd/api")
	r = buildStatus(t, ws, statusOpts(nil, nil))
	api = findEntry(t, r, "auth", "api")
	if api.IsCurrentCheckout == nil || !*api.IsCurrentCheckout {
		t.Fatalf("is_current_checkout = %v, want true", api.IsCurrentCheckout)
	}

	// A detached HEAD never fabricates a false.
	head := gitInTest(t, dir, "rev-parse", "HEAD")
	gitInTest(t, dir, "checkout", "--detach", head)
	r = buildStatus(t, ws, statusOpts(nil, nil))
	if findEntry(t, r, "auth", "api").IsCurrentCheckout != nil {
		t.Fatal("a detached HEAD must answer unknown, not false")
	}
	if hasIssue(r, IssueRepoDetached) == nil {
		t.Fatalf("expected repo-detached, got %v", issueCodes(r))
	}
}

func TestAgentStatusCheckoutSessionMatrix(t *testing.T) {
	writeSession := func(t *testing.T, ws Workspace, body string) {
		t.Helper()
		path := sessionStatePath(ws)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeLock := func(t *testing.T, ws Workspace, owner string) {
		t.Helper()
		if err := os.MkdirAll(sessionLockDir(ws), 0700); err != nil {
			t.Fatal(err)
		}
		if owner != "" {
			if err := os.WriteFile(sessionLockOwnerPath(ws), []byte(owner), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("orphan lock", func(t *testing.T) {
		dir, ws := setupHealthTestRepo(t)
		addFeatureToRepo(t, ws, "auth", "api", "main")
		gitInTest(t, dir, "branch", "api")
		writeLock(t, ws, `{"token":"t","pid":1,"created_at":"x"}`)
		r := buildStatus(t, ws, statusOpts(nil, nil))
		iss := hasIssue(r, IssueSessionOrphanLock)
		if iss == nil || iss.Scope != ScopeWorkspace || iss.Feature != nil || iss.Name != nil {
			t.Fatalf("expected a workspace-scoped orphan lock, got %v", issueCodes(r))
		}
		if r.Workspace.Attention.IssueCount != 1 {
			t.Fatalf("workspace issue count = %d", r.Workspace.Attention.IssueCount)
		}
		if findEntry(t, r, "auth", "api").Attention.Status == AttentionNeedsAttention {
			t.Fatal("a workspace-owned issue must never smear down onto a branch")
		}
		if findFeature(t, r, "auth").Attention.Status == AttentionNeedsAttention {
			t.Fatal("a workspace-owned issue must never smear down onto a feature")
		}
		if r.Workspace.CheckoutSession != nil {
			t.Fatal("an orphan lock produces no observation")
		}
	})

	t.Run("state invalid without lock", func(t *testing.T) {
		_, ws := setupHealthTestRepo(t)
		addFeatureToRepo(t, ws, "auth", "api", "main")
		writeSession(t, ws, "not json")
		r := buildStatus(t, ws, statusOpts(nil, nil))
		if hasIssue(r, IssueSessionStateInvalid) == nil {
			t.Fatalf("a corrupt active.json with no lock must be reported, got %v", issueCodes(r))
		}
		if hasIssue(r, IssueSessionOrphanLock) != nil {
			t.Fatal("state-invalid must not be reported as an orphan lock")
		}
		if r.Workspace.CheckoutSession == nil || r.Workspace.CheckoutSession.RecordState != string(DirectRecordInvalid) {
			t.Fatalf("observation = %+v", r.Workspace.CheckoutSession)
		}
		if r.Workspace.CheckoutSession.Presence != PresenceUnknown {
			t.Fatal("an unparseable record is unknown, never absent")
		}
	})

	t.Run("state unsupported", func(t *testing.T) {
		_, ws := setupHealthTestRepo(t)
		addFeatureToRepo(t, ws, "auth", "api", "main")
		writeSession(t, ws, `{"schema_version":99,"feature":"auth","name":"api","mode":"direct","pid":1}`)
		writeLock(t, ws, `{"token":"t","pid":1,"created_at":"x"}`)
		r := buildStatus(t, ws, statusOpts(nil, nil))
		if hasIssue(r, IssueSessionStateUnsupported) == nil {
			t.Fatalf("expected session-state-unsupported, got %v", issueCodes(r))
		}
	})

	t.Run("lock missing and owner dead", func(t *testing.T) {
		_, ws := setupHealthTestRepo(t)
		addFeatureToRepo(t, ws, "auth", "api", "main")
		writeSession(t, ws, `{"schema_version":1,"feature":"auth","name":"api","mode":"direct","pid":999010,"stage":"agent"}`)
		proc := fakeProcessProber{probe: map[int]ProcessLiveness{999010: ProcessDead}}
		r := buildStatus(t, ws, statusOpts(proc, nil))
		if hasIssue(r, IssueSessionLockMissing) == nil {
			t.Fatalf("expected session-lock-missing, got %v", issueCodes(r))
		}
		dead := hasIssue(r, IssueSessionOwnerDead)
		if dead == nil || dead.Scope != ScopeEntry || dead.Name == nil || *dead.Name != "api" {
			t.Fatalf("session-owner-dead must attach to the entry: %+v", dead)
		}
		api := findEntry(t, r, "auth", "api")
		if api.RuntimePresence != PresenceStale || api.Attention.Status != AttentionNeedsAttention {
			t.Fatalf("entry = %q / %+v", api.RuntimePresence, api.Attention)
		}
		if len(api.Sessions) != 1 || api.Sessions[0].Kind != SessionKindCheckoutDirect {
			t.Fatalf("attribution failed: %+v", api.Sessions)
		}
	})

	t.Run("lock invalid on empty token", func(t *testing.T) {
		_, ws := setupHealthTestRepo(t)
		addFeatureToRepo(t, ws, "auth", "api", "main")
		writeSession(t, ws, `{"schema_version":1,"feature":"auth","name":"api","mode":"direct","pid":999011,"stage":"agent"}`)
		writeLock(t, ws, `{"token":"","pid":5,"created_at":"x"}`)
		proc := fakeProcessProber{probe: map[int]ProcessLiveness{999011: ProcessLive}}
		r := buildStatus(t, ws, statusOpts(proc, nil))
		if hasIssue(r, IssueSessionLockInvalid) == nil {
			t.Fatalf("expected session-lock-invalid, got %v", issueCodes(r))
		}
	})

	t.Run("unattributed and stage unrecognized", func(t *testing.T) {
		_, ws := setupHealthTestRepo(t)
		addFeatureToRepo(t, ws, "auth", "api", "main")
		writeSession(t, ws, `{"schema_version":1,"feature":"ghost","name":"x","mode":"direct","pid":999012,"stage":"weird"}`)
		writeLock(t, ws, `{"token":"t","pid":5,"created_at":"x"}`)
		proc := fakeProcessProber{probe: map[int]ProcessLiveness{999012: ProcessDead}}
		r := buildStatus(t, ws, statusOpts(proc, nil))
		if hasIssue(r, IssueSessionUnattributed) == nil {
			t.Fatalf("expected session-unattributed, got %v", issueCodes(r))
		}
		if hasIssue(r, IssueSessionOwnerDead) != nil {
			t.Fatal("an unattributable verdict has no entry to live on")
		}
		stage := hasIssue(r, IssueSessionStageUnrecognized)
		if stage == nil || stage.Severity != SeverityInfo {
			t.Fatalf("expected an info session-stage-unrecognized, got %v", issueCodes(r))
		}
		obs := r.Workspace.CheckoutSession
		if obs == nil || obs.StageRecognized || obs.Stage == nil || *obs.Stage != "weird" {
			t.Fatalf("raw stage must survive verbatim: %+v", obs)
		}
	})

	t.Run("tmux record gone", func(t *testing.T) {
		_, ws := setupHealthTestRepo(t)
		addFeatureToRepo(t, ws, "auth", "api", "main")
		writeSession(t, ws, `{"schema_version":1,"feature":"auth","name":"api","mode":"tmux","tmux_session":"s","stage":"tmux"}`)
		writeLock(t, ws, `{"token":"t","pid":5,"created_at":"x"}`)
		r := buildStatus(t, ws, statusOpts(nil, nil))
		if hasIssue(r, IssueSessionTmuxGone) == nil {
			t.Fatalf("expected session-tmux-gone, got %v", issueCodes(r))
		}

		// The same record with tmux unavailable is unverifiable, not gone.
		noTmux := fakeTmuxInventory{snap: TmuxSnapshot{Sessions: map[string]bool{}}}
		r = buildStatus(t, ws, statusOpts(nil, noTmux))
		if hasIssue(r, IssueTmuxUnverifiable) == nil {
			t.Fatalf("expected tmux-unverifiable, got %v", issueCodes(r))
		}
		if hasIssue(r, IssueTmuxMissing) != nil {
			t.Fatal("a record needing verification upgrades tmux-missing to tmux-unverifiable")
		}
		if r.Workspace.CheckoutSession.Presence != PresenceUnknown {
			t.Fatal("an unverifiable tmux record is unknown")
		}
	})

	t.Run("dirty repo escalates only with a record", func(t *testing.T) {
		dir, ws := setupHealthTestRepo(t)
		addFeatureToRepo(t, ws, "auth", "api", "main")
		if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		r := buildStatus(t, ws, statusOpts(nil, nil))
		if iss := hasIssue(r, IssueRepoDirty); iss == nil || iss.Severity != SeverityInfo {
			t.Fatalf("expected an info repo-dirty, got %v", issueCodes(r))
		}
		writeSession(t, ws, `{"schema_version":1,"feature":"auth","name":"api","mode":"direct","pid":999013,"stage":"agent"}`)
		writeLock(t, ws, `{"token":"t","pid":5,"created_at":"x"}`)
		proc := fakeProcessProber{probe: map[int]ProcessLiveness{999013: ProcessLive}}
		r = buildStatus(t, ws, statusOpts(proc, nil))
		if iss := hasIssue(r, IssueRepoDirtyBlocking); iss == nil || iss.Severity != SeverityWarning {
			t.Fatalf("expected repo-dirty-blocking, got %v", issueCodes(r))
		}
	})
}

func TestAgentStatusRedactsEverySecret(t *testing.T) {
	_, ws := setupHealthTestRepo(t)
	addFeatureToRepo(t, ws, "auth", "api", "main")
	path := sessionStatePath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"schema_version":1,"feature":"auth","name":"api","mode":"direct","pid":999020,"stage":"agent",` +
		`"lock_token":"SECRETLOCKTOKEN","links":[{"path":"/a","target":"/b"}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionLockDir(ws), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionLockOwnerPath(ws), []byte(`{"token":"SECRETOWNERTOKEN","pid":7,"created_at":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{999020: ProcessLive}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	doc := encodeStatus(t, r)
	human := []byte(FormatAgentStatus(r))
	for _, secret := range []string{"SECRETLOCKTOKEN", "SECRETOWNERTOKEN"} {
		if bytes.Contains(doc, []byte(secret)) {
			t.Fatalf("%s leaked into the JSON document", secret)
		}
		if bytes.Contains(human, []byte(secret)) {
			t.Fatalf("%s leaked into the human output", secret)
		}
	}
	for _, key := range []string{`"lock_token"`, `"token"`, `"links"`, `"env"`, `"command"`, `"argv"`, `"prompt"`} {
		if bytes.Contains(doc, []byte(key)) {
			t.Fatalf("forbidden key %s present in the document", key)
		}
	}
}

func TestAgentStatusCheckoutIgnoresDirectRecords(t *testing.T) {
	_, ws := setupHealthTestRepo(t)
	addFeatureToRepo(t, ws, "auth", "api", "main")
	featurePath := ws.FeaturePath("auth")
	// Hand-planted external state under a checkout metadata root. Records are
	// external-only, so nothing may observe it. The owner pid is deliberately
	// live so any accidental read would be loud.
	branchID := DirectSessionBranchID("auth", "api")
	dir := filepath.Join(featurePath, ".sessions", branchID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("7", 32)
	body := `{"schema_version":1,"token":"` + token + `","feature":"auth","name":"api","owner_pid":` +
		strconv.Itoa(os.Getpid()) + `,"stage":"agent"}`
	if err := os.WriteFile(filepath.Join(dir, token+".json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, filepath.Join(featurePath, ".sessions"))

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{os.Getpid(): ProcessLive}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	api := findEntry(t, r, "auth", "api")
	if len(api.Sessions) != 0 || api.SessionCounts.Total != 0 {
		t.Fatalf("checkout mode must not observe direct records: %+v", api.Sessions)
	}
	for _, code := range issueCodes(r) {
		if strings.HasPrefix(code, "direct-record") {
			t.Fatalf("checkout mode must emit no direct-record issue, got %q", code)
		}
	}
	doc := encodeStatus(t, r)
	if bytes.Contains(doc, []byte(token)) || bytes.Contains(doc, []byte(branchID)) {
		t.Fatal("planted record identifiers must not appear in checkout output")
	}
	if after := snapshotDir(t, filepath.Join(featurePath, ".sessions")); after != before {
		t.Fatal("the planted tree must be byte-identical afterwards")
	}
}

// ---------- sync, worktree inventory, git operations ----------

func TestAgentStatusCheckoutSyncProjection(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	addStackEntries(t, ws, "auth", []StackEntry{{Name: "api", Branch: "jd/api", Base: "main"}})
	gitInTest(t, dir, "branch", "jd/api")
	stateDir := ws.CheckoutStateDir()
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	tx := `feature: auth
stage: conflict
lock_pid: 999030
failure_msg: rebase conflict
current_index: 0
plan:
  - branch: jd/api
    base: main
`
	if err := os.WriteFile(filepath.Join(stateDir, "auth-checkout-sync.yaml"), []byte(tx), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "auth-checkout-sync.lock"), []byte("pid: 999030\ncreated: x\n"), 0600); err != nil {
		t.Fatal(err)
	}

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{999030: ProcessDead}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	f := findFeature(t, r, "auth")
	if f.Sync == nil || f.Sync.Kind != "checkout" {
		t.Fatalf("sync projection = %+v", f.Sync)
	}
	if f.Sync.Liveness == nil || *f.Sync.Liveness != "stale" {
		t.Fatalf("liveness = %v", f.Sync.Liveness)
	}
	if f.Sync.FailedBranch != nil || len(f.Sync.Pending) != 0 {
		t.Fatal("a checkout transaction has no failed branch and projects no plan")
	}
	if hasIssue(r, IssueSyncStale) == nil || hasIssue(r, IssueSyncFailed) == nil {
		t.Fatalf("expected sync-stale and sync-failed, got %v", issueCodes(r))
	}
	if f.Attention.Status != AttentionNeedsAttention {
		t.Fatalf("feature attention = %+v", f.Attention)
	}
	// A feature-level fault never smears down.
	api := findEntry(t, r, "auth", "api")
	if api.Attention.Status == AttentionNeedsAttention {
		t.Fatal("a feature-level sync fault must not mark an innocent branch")
	}
	if !api.FeatureAttention {
		t.Fatal("feature_attention must mirror the feature's own-scope warning")
	}
	// The checkout plan uses the git branch axis.
	current := hasIssue(r, IssueSyncCurrentBranch)
	if current == nil || current.Name == nil || *current.Name != "api" {
		t.Fatalf("sync-current-branch must match on GitBranch(): %+v", current)
	}

	// A corrupt transaction collapses into one warning-severity code.
	if err := os.WriteFile(filepath.Join(stateDir, "auth-checkout-sync.yaml"), []byte("\tnot: [yaml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r = buildStatus(t, ws, statusOpts(proc, nil))
	invalid := hasIssue(r, IssueSyncInvalid)
	if invalid == nil || invalid.Severity != SeverityWarning {
		t.Fatalf("expected a warning sync-invalid, got %v", issueCodes(r))
	}
	if r.Summary.Errors != 0 {
		t.Fatalf("no baseline code emits error severity, got %d", r.Summary.Errors)
	}
}

func TestBuildWorktreeInventory(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	wtPath := addExternalWorktree(t, ws, featurePath, entries[0])

	inv := BuildWorktreeInventory(ws.RepoRoot)
	if !inv.Available {
		t.Fatal("inventory must be available for a real repo")
	}
	if inv.ByBranch["api"] == "" {
		t.Fatalf("inventory = %+v", inv)
	}
	if inv.Prunable["api"] {
		t.Fatal("a live worktree is not prunable")
	}

	// Removing the directory behind Git's back makes the entry prunable.
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatal(err)
	}
	inv = BuildWorktreeInventory(ws.RepoRoot)
	if !inv.Prunable["api"] {
		t.Fatalf("expected a prunable entry, got %+v", inv)
	}

	r := buildStatus(t, ws, statusOpts(nil, nil))
	api := findEntry(t, r, "auth", "api")
	if api.Materialization.State != MaterializedPrunableMissing {
		t.Fatalf("materialization = %+v", api.Materialization)
	}
	if hasIssue(r, IssueWorktreePrunableMissing) == nil {
		t.Fatalf("expected worktree-prunable-missing, got %v", issueCodes(r))
	}

	if empty := BuildWorktreeInventory(""); empty.Available {
		t.Fatal("an empty repo root yields an unavailable inventory")
	}
}

func TestGitActiveOpDetectsBisect(t *testing.T) {
	dir, ws := setupHealthTestRepo(t)
	addFeatureToRepo(t, ws, "auth", "api", "main")
	if err := os.WriteFile(filepath.Join(dir, ".git", "BISECT_LOG"), []byte("git bisect start\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if op := gitActiveOp(dir); op != "bisect" {
		t.Fatalf("gitActiveOp = %q, want bisect", op)
	}
	r := buildStatus(t, ws, statusOpts(nil, nil))
	if r.Workspace.ActiveGitOp == nil || *r.Workspace.ActiveGitOp != "bisect" {
		t.Fatalf("active_git_op = %v", r.Workspace.ActiveGitOp)
	}
	if hasIssue(r, IssueRepoGitOp) == nil {
		t.Fatalf("expected repo-git-op, got %v", issueCodes(r))
	}
	// An existing marker still wins its own name.
	if err := os.Remove(filepath.Join(dir, ".git", "BISECT_LOG")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "REVERT_HEAD"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if op := gitActiveOp(dir); op != "revert" {
		t.Fatalf("gitActiveOp = %q, want revert", op)
	}
}

// ---------- tmux ----------

func TestAgentStatusTmuxTable(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	wtPath := addExternalWorktree(t, ws, featurePath, entries[0])
	name := ExternalTmuxSessionName("auth", "api")

	t.Run("missing binary emits no observation", func(t *testing.T) {
		r := buildStatus(t, ws, statusOpts(nil, fakeTmuxInventory{snap: TmuxSnapshot{Sessions: map[string]bool{}}}))
		api := findEntry(t, r, "auth", "api")
		if len(api.Sessions) != 0 {
			t.Fatalf("no evidence means no observation, got %+v", api.Sessions)
		}
		iss := hasIssue(r, IssueTmuxMissing)
		if iss == nil || iss.Severity != SeverityInfo || iss.Scope != ScopeWorkspace {
			t.Fatalf("expected a workspace info tmux-missing, got %v", issueCodes(r))
		}
		if api.Attention.Status != AttentionIdle {
			t.Fatal("a tmux-free workspace must not make every branch need attention")
		}
	})

	t.Run("inventory error", func(t *testing.T) {
		snap := TmuxSnapshot{Available: true, Sessions: map[string]bool{}, Err: errBoom{}}
		r := buildStatus(t, ws, statusOpts(nil, fakeTmuxInventory{snap: snap}))
		if hasIssue(r, IssueTmuxUnverifiable) == nil {
			t.Fatalf("expected tmux-unverifiable, got %v", issueCodes(r))
		}
		if len(findEntry(t, r, "auth", "api").Sessions) != 0 {
			t.Fatal("an unusable inventory produces no per-branch observation")
		}
	})

	t.Run("verified by pane path", func(t *testing.T) {
		snap := TmuxSnapshot{
			Available: true, ServerRunning: true, PanesAvailable: true,
			Sessions: map[string]bool{name: true},
			Panes:    []TmuxPane{{Session: name, Path: wtPath}},
		}
		r := buildStatus(t, ws, statusOpts(nil, fakeTmuxInventory{snap: snap}))
		api := findEntry(t, r, "auth", "api")
		if len(api.Sessions) != 1 || api.Sessions[0].Presence != PresencePresent {
			t.Fatalf("sessions = %+v", api.Sessions)
		}
		if api.Attention.Status != AttentionActive {
			t.Fatalf("a verified session is active: %+v", api.Attention)
		}
	})

	t.Run("path mismatch", func(t *testing.T) {
		snap := TmuxSnapshot{
			Available: true, ServerRunning: true, PanesAvailable: true,
			Sessions: map[string]bool{name: true},
			Panes:    []TmuxPane{{Session: name, Path: t.TempDir()}},
		}
		r := buildStatus(t, ws, statusOpts(nil, fakeTmuxInventory{snap: snap}))
		api := findEntry(t, r, "auth", "api")
		if len(api.Sessions) != 1 || api.Sessions[0].Presence != PresenceUnknown {
			t.Fatalf("sessions = %+v", api.Sessions)
		}
		if hasIssue(r, IssueTmuxPathMismatch) == nil {
			t.Fatalf("expected tmux-path-mismatch, got %v", issueCodes(r))
		}
		if api.Attention.Status != AttentionNeedsAttention {
			t.Fatal("unverifiable evidence must need attention")
		}
	})

	t.Run("panes unavailable", func(t *testing.T) {
		snap := TmuxSnapshot{
			Available: true, ServerRunning: true, PanesAvailable: false,
			Sessions: map[string]bool{name: true},
		}
		r := buildStatus(t, ws, statusOpts(nil, fakeTmuxInventory{snap: snap}))
		if hasIssue(r, IssueTmuxPanesUnverified) == nil {
			t.Fatalf("expected tmux-panes-unverified, got %v", issueCodes(r))
		}
	})

	t.Run("feature-wide session", func(t *testing.T) {
		featureName := ExternalFeatureTmuxSessionName("auth")
		snap := TmuxSnapshot{
			Available: true, ServerRunning: true, PanesAvailable: true,
			Sessions: map[string]bool{featureName: true},
			Panes:    []TmuxPane{{Session: featureName, Path: featurePath}},
		}
		r := buildStatus(t, ws, statusOpts(nil, fakeTmuxInventory{snap: snap}))
		f := findFeature(t, r, "auth")
		if f.FeatureTmux == nil || f.FeatureTmux.Presence != PresencePresent {
			t.Fatalf("feature_tmux = %+v", f.FeatureTmux)
		}
		if f.RuntimePresence != PresencePresent {
			t.Fatalf("the feature rolls up its --all session: %q", f.RuntimePresence)
		}
	})
}

type errBoom struct{}

func (errBoom) Error() string { return "tmux inventory failed" }

// ---------- degraded workspace, preconditions, filtering ----------

func TestAgentStatusDegradedWorkspace(t *testing.T) {
	metadataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(metadataRoot, "auth"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := SaveStack(filepath.Join(metadataRoot, "auth"), Stack{Branches: []StackEntry{{Name: "api", Base: "main"}}}); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Mode: ModeExternal, MetadataRoot: metadataRoot, Caps: capsFor(ModeExternal)}

	report, err := BuildAgentStatus(ws, "cannot determine source repository", statusOpts(nil, nil))
	if err != nil {
		t.Fatalf("a degraded workspace must still produce a report: %v", err)
	}
	if !report.Workspace.Degraded || report.Workspace.DegradedReason == nil {
		t.Fatalf("workspace = %+v", report.Workspace)
	}
	if report.Workspace.RepoRoot != nil || report.Workspace.StableID != nil {
		t.Fatal("repo_root and stable_id must be null, never empty strings")
	}
	if report.Workspace.Branch != nil || report.Workspace.Dirty != nil || report.Workspace.ActiveGitOp != nil {
		t.Fatal("no git-derived workspace field may be filled without a repo root")
	}
	api := findEntry(t, report, "auth", "api")
	if api.Materialization.State != MaterializedUnknown || api.Materialization.RefExists != nil {
		t.Fatalf("materialization = %+v", api.Materialization)
	}
	if api.Attention.Status != AttentionIdle {
		t.Fatal("the degraded warning lives at workspace scope, not on a branch")
	}
	iss := hasIssue(report, IssueWorkspaceDegraded)
	if iss == nil || iss.Severity != SeverityWarning {
		t.Fatalf("expected a workspace-degraded warning, got %v", issueCodes(report))
	}
	if report.Workspace.Attention.Status != AttentionNeedsAttention {
		t.Fatal("a degraded workspace needs attention")
	}
}

func TestAgentStatusMetadataRootPrecondition(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "nope.tws")
	ws := Workspace{Mode: ModeExternal, MetadataRoot: missing, Caps: capsFor(ModeExternal)}
	if _, err := BuildAgentStatus(ws, "", statusOpts(nil, nil)); err == nil {
		t.Fatal("a missing metadata root must be fatal")
	} else if !strings.Contains(err.Error(), "workspace metadata root unreadable") {
		t.Fatalf("err = %v", err)
	}

	// A file where a directory is expected is equally fatal.
	asFile := filepath.Join(root, "file.tws")
	if err := os.WriteFile(asFile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	ws.MetadataRoot = asFile
	if _, err := BuildAgentStatus(ws, "", statusOpts(nil, nil)); err == nil {
		t.Fatal("a non-directory metadata root must be fatal")
	}

	// An empty but readable metadata root is a normal, empty report.
	ws.MetadataRoot = t.TempDir()
	report, err := BuildAgentStatus(ws, "", statusOpts(nil, nil))
	if err != nil {
		t.Fatalf("an empty workspace is not an error: %v", err)
	}
	if len(report.Features) != 0 || report.Summary.Entries != 0 {
		t.Fatalf("report = %+v", report.Summary)
	}
	if report.Features == nil || report.Issues == nil {
		t.Fatal("lists are never null")
	}
}

func TestAgentStatusFilterFeature(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	// A second feature with a missing worktree, whose issue must be dropped.
	billing := filepath.Join(ws.MetadataRoot, "billing")
	if err := os.MkdirAll(filepath.Join(billing, "worktrees"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := SaveStack(billing, Stack{Branches: []StackEntry{{Name: "core", Base: "main"}}}); err != nil {
		t.Fatal(err)
	}
	// A workspace-scoped issue that must survive the filter.
	if err := os.MkdirAll(sessionLockDir(ws), 0700); err != nil {
		t.Fatal(err)
	}

	r := buildStatus(t, ws, statusOpts(nil, nil))
	presenceBefore := r.Workspace.RuntimePresence
	if err := r.FilterFeature("auth"); err != nil {
		t.Fatal(err)
	}
	if len(r.Features) != 1 || r.Features[0].Feature != "auth" {
		t.Fatalf("features = %+v", r.Features)
	}
	for _, iss := range r.Issues {
		if iss.Feature != nil && *iss.Feature != "auth" {
			t.Fatalf("an unrelated scoped issue survived the filter: %+v", iss)
		}
	}
	if r.Summary.Features != 1 || r.Summary.Entries != 1 {
		t.Fatalf("summary = %+v", r.Summary)
	}
	if r.Workspace.RuntimePresence != presenceBefore {
		t.Fatal("workspace runtime presence is deliberately not recomputed under a filter")
	}
	if err := r.FilterFeature("nosuch"); err == nil || !strings.Contains(err.Error(), "feature not found: nosuch") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentStatusFilterKeepsWorkspaceIssues(t *testing.T) {
	_, ws := setupHealthTestRepo(t)
	addFeatureToRepo(t, ws, "auth", "api", "main")
	if err := os.MkdirAll(sessionLockDir(ws), 0700); err != nil {
		t.Fatal(err)
	}
	r := buildStatus(t, ws, statusOpts(nil, nil))
	if err := r.FilterFeature("auth"); err != nil {
		t.Fatal(err)
	}
	if hasIssue(r, IssueSessionOrphanLock) == nil {
		t.Fatal("a workspace orphan lock must survive a feature filter")
	}
	if r.Workspace.Attention.Status != AttentionNeedsAttention {
		t.Fatal("workspace attention must be re-derived from the surviving issues")
	}
	if r.Summary.Warnings == 0 {
		t.Fatal("workspace-scoped warnings are counted in the filtered summary")
	}
}

// ---------- JSON contract ----------

func TestAgentStatusJSONContract(t *testing.T) {
	entries := []StackEntry{{Name: "api", Branch: "jd/api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: "api", Stage: DirectStageAgent})

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{os.Getpid(): ProcessLive}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	doc := encodeStatus(t, r)

	if !bytes.HasSuffix(doc, []byte("\n")) {
		t.Fatal("the encoder must end with a newline")
	}
	if !bytes.Contains(doc, []byte("\n  \"schema_version\": 1")) {
		t.Fatal("the encoder must use two-space indentation")
	}

	var decoded map[string]any
	if err := json.Unmarshal(doc, &decoded); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "report", decoded, []string{"schema_version", "generated_at", "workspace", "features", "issues", "summary"})
	assertKeys(t, "workspace", decoded["workspace"].(map[string]any), []string{
		"mode", "stable_id", "repo_root", "metadata_root", "degraded", "degraded_reason", "branch",
		"detached", "dirty", "active_git_op", "tmux", "checkout_session", "runtime_presence",
		"agent_state", "attention",
	})
	feature := decoded["features"].([]any)[0].(map[string]any)
	assertKeys(t, "feature", feature, []string{
		"feature", "path", "stack_state", "sync", "feature_tmux", "entries",
		"runtime_presence", "agent_state", "attention",
	})
	entry := feature["entries"].([]any)[0].(map[string]any)
	assertKeys(t, "entry", entry, []string{
		"feature", "name", "git_branch", "base", "base_git_branch", "repo", "archived",
		"is_current_checkout", "materialization", "sessions", "session_counts", "unread_decisions",
		"runtime_presence", "agent_state", "attention", "feature_attention",
	})
	assertKeys(t, "materialization", entry["materialization"].(map[string]any), []string{
		"kind", "state", "path", "ref_exists", "checked_out_branch", "dirty",
	})
	session := entry["sessions"].([]any)[0].(map[string]any)
	assertKeys(t, "session", session, []string{
		"kind", "presence", "agent_state", "stage", "stage_recognized", "owner_pid", "child_pid",
		"liveness", "tmux_session", "path", "agent", "started_at", "updated_at", "record_id",
		"record_state", "detail",
	})

	// agent_state is unknown everywhere, and the exact key sets asserted
	// above already prove no extra key exists at any level.
	var walk func(any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			if s, ok := node["agent_state"]; ok && s != "unknown" {
				t.Fatalf("agent_state = %v, want unknown", s)
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(decoded)

	if !strings.HasSuffix(r.GeneratedAt, "Z") || len(r.GeneratedAt) != 20 {
		t.Fatalf("generated_at = %q", r.GeneratedAt)
	}

	// Two consecutive builds differ only in generated_at.
	second := buildStatus(t, ws, &AgentStatusOpts{Proc: proc, Tmux: emptyTmux(), Now: func() time.Time { return time.Now() }})
	firstCopy, secondCopy := *r, *second
	firstCopy.GeneratedAt, secondCopy.GeneratedAt = "", ""
	if !bytes.Equal(encodeStatus(t, &firstCopy), encodeStatus(t, &secondCopy)) {
		t.Fatal("two polls with no intervening change must be identical apart from generated_at")
	}
}

func TestAgentStatusMalformedTimestampSurvivesVerbatim(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	branchID := DirectSessionBranchID("auth", "api")
	token := mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: "api"})
	if err := UpdateDirectSession(featurePath, branchID, token, func(r *DirectSessionRecord) {
		r.StartedAt = "not-a-time"
	}); err != nil {
		t.Fatal(err)
	}
	proc := fakeProcessProber{probe: map[int]ProcessLiveness{os.Getpid(): ProcessLive}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	api := findEntry(t, r, "auth", "api")
	if len(api.Sessions) != 1 || api.Sessions[0].StartedAt == nil || *api.Sessions[0].StartedAt != "not-a-time" {
		t.Fatalf("a stored timestamp must be emitted verbatim: %+v", api.Sessions)
	}
}

func TestAgentStatusPresenceInvariant(t *testing.T) {
	entries := []StackEntry{
		{Name: "dead", Base: "main"},
		{Name: "eperm", Base: "main"},
		{Name: "healthy", Base: "main"},
	}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	for _, e := range entries {
		addExternalWorktree(t, ws, featurePath, e)
	}
	for name, pid := range map[string]int{"dead": 999040, "eperm": 999041} {
		branchID := DirectSessionBranchID("auth", name)
		token := mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: name})
		if err := UpdateDirectSession(featurePath, branchID, token, func(r *DirectSessionRecord) {
			r.OwnerPID = pid
		}); err != nil {
			t.Fatal(err)
		}
	}
	proc := fakeProcessProber{probe: map[int]ProcessLiveness{999040: ProcessDead, 999041: ProcessUnknown}}
	r := buildStatus(t, ws, statusOpts(proc, nil))

	for _, f := range r.Features {
		for _, e := range f.Entries {
			if e.RuntimePresence == PresenceStale || e.RuntimePresence == PresenceUnknown {
				if e.Attention.Status != AttentionNeedsAttention {
					t.Fatalf("%s is %q but %q", e.Name, e.RuntimePresence, e.Attention.Status)
				}
			}
		}
	}
	if r.Summary.NeedsAttention+r.Summary.Active+r.Summary.Idle != r.Summary.Entries {
		t.Fatalf("attention counters must sum to entries: %+v", r.Summary)
	}
	if r.Summary.RuntimePresent+r.Summary.RuntimeStale+r.Summary.RuntimeUnknown+r.Summary.RuntimeAbsent != r.Summary.Entries {
		t.Fatalf("runtime counters must sum to entries: %+v", r.Summary)
	}
	if r.Summary.RuntimeStale != 1 || r.Summary.RuntimeUnknown != 1 {
		t.Fatalf("summary = %+v", r.Summary)
	}
}

func TestFormatAgentStatusGlyphsAndEmptyWorkspace(t *testing.T) {
	if got := attentionIcon(AttentionNeedsAttention); got != "[!]" {
		t.Fatalf("needs_attention glyph = %q", got)
	}
	if got := attentionIcon(AttentionActive); got != "[i]" {
		t.Fatalf("active glyph = %q", got)
	}
	if got := attentionIcon(AttentionIdle); got != "[ok]" {
		t.Fatalf("idle glyph = %q", got)
	}
	if got := severityIcon(SeverityError); got != "[E]" {
		t.Fatalf("error glyph = %q", got)
	}

	ws := Workspace{Mode: ModeExternal, MetadataRoot: t.TempDir(), Caps: capsFor(ModeExternal)}
	r := buildStatus(t, ws, statusOpts(nil, nil))
	out := FormatAgentStatus(r)
	if !strings.Contains(out, "No features found. Use 'tws add <feature>' to create one.") {
		t.Fatalf("empty workspace output = %q", out)
	}
}

// ---------- helpers ----------

func assertKeys(t *testing.T, label string, node map[string]any, want []string) {
	t.Helper()
	var got []string
	for k := range node {
		got = append(got, k)
	}
	sort.Strings(got)
	sorted := append([]string{}, want...)
	sort.Strings(sorted)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		t.Fatalf("%s keys = %v, want %v", label, got, sorted)
	}
}

// snapshotDir maps each relative path under dir to a content hash and mode, so
// a test can assert a byte-identical tree rather than merely a path set.
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			lines = append(lines, rel+"|dir|"+info.Mode().Perm().String())
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines = append(lines, rel+"|"+string(data)+"|"+info.Mode().Perm().String())
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// TestAgentStatusNeverLeaksDirectToken drives every direct-record shape that
// reaches operator-facing output — ok, stale, invalid, unsupported, and an
// unreadable file whose OS error embeds the record path — and asserts that
// neither the JSON document nor the human view carries a full ownership token
// or a forbidden token key, while the 8-character record id is still present.
func TestAgentStatusNeverLeaksDirectToken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any mode, so the unreadable-record row cannot be staged")
	}
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	branchID := DirectSessionBranchID("auth", "api")
	dir := filepath.Join(DirectSessionsDir(featurePath), branchID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}

	// Five distinct 32-hex tokens, one per record shape.
	live := knownDirectToken
	stale := "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d"
	invalid := "2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e"
	unsupported := "3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f"
	unreadable := "4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f90"
	tokens := []string{live, stale, invalid, unsupported, unreadable}

	write := func(token, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, token+".json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rec := func(token string, pid int, schema int) string {
		return fmt.Sprintf(`{"schema_version":%d,"token":%q,"feature":"auth","name":"api","owner_pid":%d,`+
			`"stage":"agent","started_at":"2020-01-01T00:00:00Z"}`, schema, token, pid)
	}
	write(live, rec(live, 999040, 1))
	write(stale, rec(stale, 999041, 1))
	write(invalid, `{broken`)
	write(unsupported, rec(unsupported, 999043, 99))
	write(unreadable, rec(unreadable, 999044, 1))
	if err := os.Chmod(filepath.Join(dir, unreadable+".json"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, unreadable+".json"), 0600) })

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{
		999040: ProcessLive,
		999041: ProcessDead,
	}}
	r := buildStatus(t, ws, statusOpts(proc, nil))

	// Every shape must actually have been observed, or the test would pass
	// vacuously.
	codes := strings.Join(issueCodes(r), ",")
	for _, want := range []string{IssueDirectRecordStale, IssueDirectRecordInvalid, IssueDirectRecordUnsupported} {
		if !strings.Contains(codes, want) {
			t.Fatalf("expected %s to be exercised, got %v", want, codes)
		}
	}

	doc := encodeStatus(t, r)
	human := []byte(FormatAgentStatus(r))
	for _, token := range tokens {
		if bytes.Contains(doc, []byte(token)) {
			t.Fatalf("token %s... leaked into the JSON document", token[:8])
		}
		if bytes.Contains(human, []byte(token)) {
			t.Fatalf("token %s... leaked into the human output", token[:8])
		}
	}
	// A bare 32-hex run is what an unredacted token looks like whatever its
	// value, so no such run may appear on either surface.
	for name, surface := range map[string][]byte{"json": doc, "human": human} {
		if m := fullTokenRe.Find(surface); m != nil {
			t.Fatalf("%s output carries a full 32-hex token %q", name, m)
		}
	}
	for _, key := range []string{`"token"`, `"lock_token"`, `"argv"`, `"prompt"`, `"env"`, `"command"`} {
		if bytes.Contains(doc, []byte(key)) {
			t.Fatalf("forbidden key %s present in the document", key)
		}
	}
	// The correlation prefix is still published for every readable record.
	for _, token := range []string{live, stale, invalid, unsupported, unreadable} {
		if !bytes.Contains(doc, []byte(DirectRecordID(token))) {
			t.Fatalf("record id %s must be published", DirectRecordID(token))
		}
	}
	// The redacted display form is what an issue points the operator at.
	if !bytes.Contains(doc, []byte(DirectRecordID(invalid)+"*.json")) {
		t.Fatalf("the invalid-record issue must cite the redacted display path:\n%s", doc)
	}
}

// ---------- human output ----------

// TestFormatAgentStatusWorkspaceOnlyAttention covers the case an earlier
// draft made invisible: nothing is wrong with any branch, so every row reads
// idle and the branch-scoped tail reads "0 needs attention", while the
// workspace itself needs attention because of a workspace-owned issue.
func TestFormatAgentStatusWorkspaceOnlyAttention(t *testing.T) {
	_, ws := setupHealthTestRepo(t)
	addFeatureToRepo(t, ws, "auth", "api", "main")
	gitInTest(t, ws.RepoRoot, "branch", "api")
	if err := os.MkdirAll(sessionLockDir(ws), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionLockOwnerPath(ws), []byte(`{"token":"t","pid":1,"created_at":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}

	r := buildStatus(t, ws, statusOpts(nil, nil))
	if r.Workspace.Attention.Status != AttentionNeedsAttention {
		t.Fatalf("workspace attention = %q", r.Workspace.Attention.Status)
	}
	if r.Summary.NeedsAttention != 0 {
		t.Fatalf("no branch may need attention: %+v", r.Summary)
	}

	out := FormatAgentStatus(r)
	if !strings.Contains(out, "Attention: [!] needs_attention") {
		t.Fatalf("the header must always carry the workspace verdict:\n%s", out)
	}
	if !strings.Contains(out, "\nWorkspace:\n  [!] "+IssueSessionOrphanLock+": ") {
		t.Fatalf("the workspace block must name the issue:\n%s", out)
	}
	if !strings.Contains(out, "run: tws close") {
		t.Fatalf("the workspace block must carry its guidance:\n%s", out)
	}
	if !strings.Contains(out, "1 branch(es): 0 active, 1 idle, 0 needs attention.") {
		t.Fatalf("the tail counts branches only:\n%s", out)
	}
}

// TestFormatAgentStatusFeatureInheritedAttention asserts that a feature-only
// fault is rendered in its own block, leaves every branch row idle, and still
// reaches the header verdict by upward inheritance.
func TestFormatAgentStatusFeatureInheritedAttention(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	if err := os.WriteFile(SyncStatePath(featurePath), []byte("::not yaml::\n\t- x"), 0644); err != nil {
		t.Fatal(err)
	}

	r := buildStatus(t, ws, statusOpts(nil, nil))
	if hasIssue(r, IssueSyncStateInvalid) == nil {
		t.Fatalf("expected sync-state-invalid, got %v", issueCodes(r))
	}
	api := findEntry(t, r, "auth", "api")
	if api.Attention.Status == AttentionNeedsAttention {
		t.Fatal("a feature-owned issue must never smear onto a branch")
	}
	if !api.FeatureAttention {
		t.Fatal("feature_attention must mirror the feature's own-scope warning")
	}

	out := FormatAgentStatus(r)
	if !strings.Contains(out, "Attention: [!] needs_attention") {
		t.Fatalf("workspace attention must be inherited and printed:\n%s", out)
	}
	if !strings.Contains(out, "\nFeature: auth\n  [!] "+IssueSyncStateInvalid+": ") {
		t.Fatalf("the feature block must name the issue:\n%s", out)
	}
	if !strings.Contains(out, "0 needs attention.") {
		t.Fatalf("no branch needs attention:\n%s", out)
	}
}

// TestFormatAgentStatusRendersEveryEntryIssue asserts the hard rule: a row
// that reads needs_attention always has a Branch block naming every one of its
// issues, with the code, the message, and the guidance. No guidance may be
// reachable only through --json.
func TestFormatAgentStatusRendersEveryEntryIssue(t *testing.T) {
	entries := []StackEntry{
		{Name: "api", Base: "main"},
		{Name: "gone", Base: "main"},
	}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	branchID := DirectSessionBranchID("auth", "api")
	token := mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: "api"})
	if err := UpdateDirectSession(featurePath, branchID, token, func(rec *DirectSessionRecord) {
		rec.OwnerPID = 999050
	}); err != nil {
		t.Fatal(err)
	}

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{999050: ProcessDead}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	out := FormatAgentStatus(r)

	// "gone" has no worktree, "api" has a stale record: both need attention.
	for _, name := range []string{"api", "gone"} {
		e := findEntry(t, r, "auth", name)
		if e.Attention.Status != AttentionNeedsAttention {
			t.Fatalf("%s attention = %q", name, e.Attention.Status)
		}
		if !strings.Contains(out, "\nBranch: auth/"+name+"\n") {
			t.Fatalf("missing a Branch block for %s:\n%s", name, out)
		}
	}
	for _, iss := range r.Issues {
		if iss.Scope != ScopeEntry {
			continue
		}
		if !strings.Contains(out, iss.Code+": "+iss.Message) {
			t.Fatalf("entry issue %s was never rendered:\n%s", iss.Code, out)
		}
		if iss.Guidance != nil && !strings.Contains(out, *iss.Guidance) {
			t.Fatalf("guidance for %s is reachable only through --json:\n%s", iss.Code, out)
		}
	}
	// Every needs-attention row is backed by a rendered block.
	for _, f := range r.Features {
		for _, e := range f.Entries {
			if e.Attention.Status != AttentionNeedsAttention {
				continue
			}
			if len(ownIssues(r.Issues, ScopeEntry, e.Feature, e.Name)) == 0 {
				continue
			}
			if !strings.Contains(out, "\nBranch: "+e.Feature+"/"+e.Name+"\n") {
				t.Fatalf("a needs-attention row without a block: %s/%s\n%s", e.Feature, e.Name, out)
			}
		}
	}
}

// TestFormatAgentStatusSeparatesSessionSummaries asserts the DETAIL column
// separates two space-containing session summaries with "; " so they cannot
// be read as one sentence.
func TestFormatAgentStatusSeparatesSessionSummaries(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	branchID := DirectSessionBranchID("auth", "api")
	for _, pid := range []int{999060, 999061} {
		token := mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: "api", Stage: DirectStageAgent})
		if err := UpdateDirectSession(featurePath, branchID, token, func(rec *DirectSessionRecord) {
			rec.OwnerPID = pid
		}); err != nil {
			t.Fatal(err)
		}
	}
	proc := fakeProcessProber{probe: map[int]ProcessLiveness{999060: ProcessDead, 999061: ProcessDead}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	if findEntry(t, r, "auth", "api").SessionCounts.Total != 2 {
		t.Fatal("two records should be observed")
	}
	out := FormatAgentStatus(r)
	if !strings.Contains(out, "; direct record ") {
		t.Fatalf("two session summaries must be separated by \"; \":\n%s", out)
	}
}

// ---------- orphan directories ----------

// TestAgentStatusEmptyOrphanDirectoryIsSilent covers the residue an
// interrupted prune leaves behind: an empty <branch-id> directory holds no
// record, so it can make nothing need attention.
func TestAgentStatusEmptyOrphanDirectoryIsSilent(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	empty := filepath.Join(DirectSessionsDir(featurePath), DirectSessionBranchID("auth", "renamed-away"))
	if err := os.MkdirAll(empty, 0700); err != nil {
		t.Fatal(err)
	}

	r := buildStatus(t, ws, statusOpts(nil, nil))
	if hasIssue(r, IssueDirectRecordOrphanBranch) != nil {
		t.Fatalf("an empty orphan directory holds no record: %v", issueCodes(r))
	}
	if findFeature(t, r, "auth").Attention.Status == AttentionNeedsAttention {
		t.Fatal("an empty directory must not make the feature need attention")
	}
	if _, err := os.Stat(empty); err != nil {
		t.Fatalf("status must leave the directory on disk: %v", err)
	}

	// One real record in the same directory still reports.
	mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: "renamed-away"})
	r = buildStatus(t, ws, statusOpts(nil, nil))
	if hasIssue(r, IssueDirectRecordOrphanBranch) == nil {
		t.Fatalf("a populated orphan directory must still report: %v", issueCodes(r))
	}
}

// TestAgentStatusUnreadableRecordRootIsReported asserts the feature-wide
// inventory failure is reported rather than swallowed.
func TestAgentStatusUnreadableRecordRootIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any mode")
	}
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	root := DirectSessionsDir(featurePath)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })

	r := buildStatus(t, ws, statusOpts(nil, nil))
	iss := hasIssue(r, IssueDirectRecordDirUnreadable)
	if iss == nil || iss.Scope != ScopeFeature || iss.Feature == nil || *iss.Feature != "auth" {
		t.Fatalf("expected a feature-scoped direct-record-dir-unreadable, got %v", issueCodes(r))
	}
	if findFeature(t, r, "auth").Attention.Status != AttentionNeedsAttention {
		t.Fatal("an unreadable record root must make the feature need attention")
	}
}

// ---------- checkout unknown-presence invariant ----------

// setupCheckoutSessionRepo builds a checkout workspace whose entry has a real
// git ref, so no ref-missing warning can mask the verdict under test.
func setupCheckoutSessionRepo(t *testing.T, feature, name string) (string, Workspace) {
	t.Helper()
	dir, ws := setupHealthTestRepo(t)
	addFeatureToRepo(t, ws, feature, name, "main")
	gitInTest(t, dir, "branch", name)
	return dir, ws
}

func writeCheckoutSessionState(t *testing.T, ws Workspace, body string) {
	t.Helper()
	path := sessionStatePath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeCheckoutSessionLock(t *testing.T, ws Workspace) {
	t.Helper()
	if err := os.MkdirAll(sessionLockDir(ws), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionLockOwnerPath(ws), []byte(`{"token":"t","pid":1,"created_at":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentStatusCheckoutUntrustworthyRecordStaysWorkspaceOnly(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		tmux      TmuxInventoryProbe
		wantIssue string
	}{
		{
			name:      "unparseable state",
			body:      "not json",
			wantIssue: IssueSessionStateInvalid,
		},
		{
			name:      "unsupported schema",
			body:      `{"schema_version":99,"feature":"auth","name":"api","mode":"direct","pid":1,"stage":"agent"}`,
			wantIssue: IssueSessionStateUnsupported,
		},
		{
			name:      "tmux record with unusable inventory",
			body:      `{"schema_version":1,"feature":"auth","name":"api","mode":"tmux","tmux_session":"s","stage":"tmux"}`,
			tmux:      fakeTmuxInventory{snap: TmuxSnapshot{Sessions: map[string]bool{}}},
			wantIssue: IssueTmuxUnverifiable,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ws := setupCheckoutSessionRepo(t, "auth", "api")
			writeCheckoutSessionState(t, ws, c.body)
			writeCheckoutSessionLock(t, ws)

			r := buildStatus(t, ws, statusOpts(nil, c.tmux))
			if hasIssue(r, c.wantIssue) == nil {
				t.Fatalf("expected %s, got %v", c.wantIssue, issueCodes(r))
			}
			obs := r.Workspace.CheckoutSession
			if obs == nil || obs.Presence != PresenceUnknown {
				t.Fatalf("the observation must be unknown at workspace scope: %+v", obs)
			}
			api := findEntry(t, r, "auth", "api")
			if len(api.Sessions) != 0 {
				t.Fatalf("an untrustworthy record must not be attributed to an entry: %+v", api.Sessions)
			}
			if api.RuntimePresence != PresenceAbsent {
				t.Fatalf("entry presence = %q, want absent", api.RuntimePresence)
			}
			if api.Attention.Status != AttentionIdle {
				t.Fatalf("entry attention = %q, want idle", api.Attention.Status)
			}
			if r.Workspace.RuntimePresence != PresenceUnknown {
				t.Fatalf("workspace presence = %q, want unknown", r.Workspace.RuntimePresence)
			}
			if r.Workspace.Attention.Status != AttentionNeedsAttention {
				t.Fatalf("the workspace warning must own the unknown observation: %q", r.Workspace.Attention.Status)
			}
			for _, iss := range r.Issues {
				if iss.Scope == ScopeEntry {
					t.Fatalf("no entry-scoped issue may exist here, got %s", iss.Code)
				}
			}
		})
	}
}

func TestAgentStatusCheckoutEmptyIdentityIsUnattributed(t *testing.T) {
	_, ws := setupCheckoutSessionRepo(t, "auth", "api")
	writeCheckoutSessionState(t, ws, `{"schema_version":1,"feature":"","name":"","mode":"direct","pid":999070,"stage":"agent"}`)
	writeCheckoutSessionLock(t, ws)

	proc := fakeProcessProber{probe: map[int]ProcessLiveness{999070: ProcessLive}}
	r := buildStatus(t, ws, statusOpts(proc, nil))
	iss := hasIssue(r, IssueSessionUnattributed)
	if iss == nil || iss.Scope != ScopeWorkspace || iss.Feature != nil || iss.Name != nil {
		t.Fatalf("a parsed record with no identity must be unattributed, got %v", issueCodes(r))
	}
	if len(findEntry(t, r, "auth", "api").Sessions) != 0 {
		t.Fatal("an identity-less record attaches to no entry")
	}
}

// TestAgentStatusCheckoutPresenceInvariant is the checkout twin of
// TestAgentStatusPresenceInvariant: every stale or unknown presence, at every
// level, coincides with needs_attention, and the summary counters still sum to
// the entry count. Every entry has a real git ref so no ref-missing warning
// can make the assertion pass vacuously.
func TestAgentStatusCheckoutPresenceInvariant(t *testing.T) {
	cases := []struct {
		name string
		body string
		pid  int
		live ProcessLiveness
		tmux TmuxInventoryProbe
	}{
		{name: "owner dead", body: `{"schema_version":1,"feature":"auth","name":"api","mode":"direct","pid":999080,"stage":"agent"}`, pid: 999080, live: ProcessDead},
		{name: "owner eperm", body: `{"schema_version":1,"feature":"auth","name":"api","mode":"direct","pid":999081,"stage":"agent"}`, pid: 999081, live: ProcessUnknown},
		{name: "tmux gone", body: `{"schema_version":1,"feature":"auth","name":"api","mode":"tmux","tmux_session":"s","stage":"tmux"}`},
		{name: "tmux unverifiable", body: `{"schema_version":1,"feature":"auth","name":"api","mode":"tmux","tmux_session":"s","stage":"tmux"}`,
			tmux: fakeTmuxInventory{snap: TmuxSnapshot{Sessions: map[string]bool{}}}},
		{name: "state invalid", body: "not json"},
		{name: "state unsupported", body: `{"schema_version":99,"feature":"auth","name":"api","mode":"direct","pid":1,"stage":"agent"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ws := setupCheckoutSessionRepo(t, "auth", "api")
			writeCheckoutSessionState(t, ws, c.body)
			writeCheckoutSessionLock(t, ws)

			var proc ProcessProber
			if c.pid != 0 {
				proc = fakeProcessProber{probe: map[int]ProcessLiveness{c.pid: c.live}}
			}
			r := buildStatus(t, ws, statusOpts(proc, c.tmux))

			// Nothing may be silently healthy: the record is never usable.
			if r.Workspace.Attention.Status != AttentionNeedsAttention {
				t.Fatalf("workspace attention = %q\nissues %v", r.Workspace.Attention.Status, issueCodes(r))
			}
			assertNoSilentPresence := func(level string, presence RuntimePresence, status AttentionStatus) {
				t.Helper()
				if presence == PresenceStale || presence == PresenceUnknown {
					if status != AttentionNeedsAttention {
						t.Fatalf("%s is %q but %q\nissues %v", level, presence, status, issueCodes(r))
					}
				}
			}
			assertNoSilentPresence("workspace", r.Workspace.RuntimePresence, r.Workspace.Attention.Status)
			for _, f := range r.Features {
				assertNoSilentPresence("feature "+f.Feature, f.RuntimePresence, f.Attention.Status)
				for _, e := range f.Entries {
					assertNoSilentPresence("entry "+e.Feature+"/"+e.Name, e.RuntimePresence, e.Attention.Status)
					if e.Materialization.RefExists == nil || !*e.Materialization.RefExists {
						t.Fatalf("the fixture must have a real ref for %s, or ref-missing masks the verdict", e.Name)
					}
				}
			}
			if r.Summary.NeedsAttention+r.Summary.Active+r.Summary.Idle != r.Summary.Entries {
				t.Fatalf("attention counters must sum to entries: %+v", r.Summary)
			}
			if r.Summary.RuntimePresent+r.Summary.RuntimeStale+r.Summary.RuntimeUnknown+r.Summary.RuntimeAbsent != r.Summary.Entries {
				t.Fatalf("runtime counters must sum to entries: %+v", r.Summary)
			}
		})
	}
}

// ---------- external materialization edge cases ----------

func TestAgentStatusExternalWorktreeEdgeCases(t *testing.T) {
	t.Run("path is not a directory", func(t *testing.T) {
		entries := []StackEntry{{Name: "api", Base: "main"}}
		ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
		wtPath := filepath.Join(featurePath, "worktrees", "api")
		if err := os.MkdirAll(filepath.Dir(wtPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wtPath, []byte("not a worktree"), 0644); err != nil {
			t.Fatal(err)
		}
		r := buildStatus(t, ws, statusOpts(nil, nil))
		api := findEntry(t, r, "auth", "api")
		if api.Materialization.State != MaterializedUnknown {
			t.Fatalf("state = %q, want unknown", api.Materialization.State)
		}
		if api.Materialization.Path == nil || *api.Materialization.Path != wtPath {
			t.Fatalf("path = %v", api.Materialization.Path)
		}
		if api.Materialization.Dirty != nil || api.Materialization.CheckedOutBranch != nil {
			t.Fatal("a non-directory must be probed for nothing")
		}
		iss := hasIssue(r, IssueWorktreeUnreadable)
		if iss == nil || iss.Scope != ScopeEntry {
			t.Fatalf("expected an entry-scoped worktree-unreadable, got %v", issueCodes(r))
		}
		if api.Attention.Status != AttentionNeedsAttention {
			t.Fatalf("attention = %q", api.Attention.Status)
		}
	})

	t.Run("rev-parse failure in a present directory", func(t *testing.T) {
		entries := []StackEntry{{Name: "api", Base: "main"}}
		ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
		// A real directory that is not a Git worktree: healthCurrentBranch
		// fails, so the dirty probe must not run and claim "clean".
		wtPath := filepath.Join(featurePath, "worktrees", "api")
		if err := os.MkdirAll(wtPath, 0755); err != nil {
			t.Fatal(err)
		}
		r := buildStatus(t, ws, statusOpts(nil, nil))
		api := findEntry(t, r, "auth", "api")
		if api.Materialization.State != MaterializedPresent {
			t.Fatalf("state = %q, want present", api.Materialization.State)
		}
		if api.Materialization.Dirty != nil {
			t.Fatalf("a failed branch probe must not report dirtiness: %v", *api.Materialization.Dirty)
		}
		if api.Materialization.CheckedOutBranch != nil {
			t.Fatalf("checked_out_branch = %v", *api.Materialization.CheckedOutBranch)
		}
		if hasIssue(r, IssueWorktreeUnreadable) == nil {
			t.Fatalf("expected worktree-unreadable, got %v", issueCodes(r))
		}
		if hasIssue(r, IssueWorktreeWrongBranch) != nil {
			t.Fatal("an unreadable worktree can never be on the wrong branch")
		}
	})

	t.Run("detached worktree", func(t *testing.T) {
		entries := []StackEntry{{Name: "api", Base: "main"}}
		ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
		wtPath := addExternalWorktree(t, ws, featurePath, entries[0])
		head := gitInTest(t, wtPath, "rev-parse", "HEAD")
		gitInTest(t, wtPath, "checkout", "--detach", head)

		r := buildStatus(t, ws, statusOpts(nil, nil))
		api := findEntry(t, r, "auth", "api")
		if api.Materialization.State != MaterializedPresent {
			t.Fatalf("state = %q, want present", api.Materialization.State)
		}
		if api.Materialization.CheckedOutBranch != nil {
			t.Fatalf("a detached worktree names no branch, got %q", *api.Materialization.CheckedOutBranch)
		}
		if hasIssue(r, IssueWorktreeWrongBranch) != nil {
			t.Fatal("a detached HEAD is not a wrong branch")
		}
		if api.Materialization.Dirty == nil {
			t.Fatal("a detached but readable worktree is still probed for dirtiness")
		}
		if api.Attention.Status != AttentionIdle {
			t.Fatalf("attention = %q, want idle\nissues %v", api.Attention.Status, issueCodes(r))
		}
	})
}

// ---------- JSON key-set snapshots ----------

// assertAgentStatusKeySets walks a decoded document and asserts the exact key
// set of every level it reaches. It is the shared body of the populated and
// empty snapshots in both workspace modes.
func assertAgentStatusKeySets(t *testing.T, doc map[string]any) {
	t.Helper()
	assertKeys(t, "report", doc, []string{"schema_version", "generated_at", "workspace", "features", "issues", "summary"})
	assertKeys(t, "workspace", doc["workspace"].(map[string]any), []string{
		"mode", "stable_id", "repo_root", "metadata_root", "degraded", "degraded_reason", "branch",
		"detached", "dirty", "active_git_op", "tmux", "checkout_session", "runtime_presence",
		"agent_state", "attention",
	})
	assertKeys(t, "workspace.tmux", doc["workspace"].(map[string]any)["tmux"].(map[string]any), []string{
		"available", "server_running", "session_count", "path_verification",
	})
	assertKeys(t, "workspace.attention", doc["workspace"].(map[string]any)["attention"].(map[string]any), []string{
		"status", "issue_count", "codes",
	})
	assertKeys(t, "summary", doc["summary"].(map[string]any), []string{
		"features", "entries", "needs_attention", "active", "idle", "runtime_present",
		"runtime_stale", "runtime_unknown", "runtime_absent", "issues", "warnings", "errors",
	})
	if session, ok := doc["workspace"].(map[string]any)["checkout_session"].(map[string]any); ok {
		assertKeys(t, "workspace.checkout_session", session, agentStatusSessionKeys)
	}
	for _, raw := range doc["features"].([]any) {
		feature := raw.(map[string]any)
		assertKeys(t, "feature", feature, []string{
			"feature", "path", "stack_state", "sync", "feature_tmux", "entries",
			"runtime_presence", "agent_state", "attention",
		})
		assertKeys(t, "feature.attention", feature["attention"].(map[string]any), []string{
			"status", "issue_count", "codes",
		})
		if sync, ok := feature["sync"].(map[string]any); ok {
			assertKeys(t, "feature.sync", sync, []string{
				"kind", "stage", "liveness", "failure_reason", "current_branch", "failed_branch",
				"lock_pid", "lock_live", "pending", "completed", "skipped",
			})
		}
		if tmux, ok := feature["feature_tmux"].(map[string]any); ok {
			assertKeys(t, "feature.feature_tmux", tmux, agentStatusSessionKeys)
		}
		for _, rawEntry := range feature["entries"].([]any) {
			entry := rawEntry.(map[string]any)
			assertKeys(t, "entry", entry, []string{
				"feature", "name", "git_branch", "base", "base_git_branch", "repo", "archived",
				"is_current_checkout", "materialization", "sessions", "session_counts",
				"unread_decisions", "runtime_presence", "agent_state", "attention", "feature_attention",
			})
			assertKeys(t, "materialization", entry["materialization"].(map[string]any), []string{
				"kind", "state", "path", "ref_exists", "checked_out_branch", "dirty",
			})
			assertKeys(t, "session_counts", entry["session_counts"].(map[string]any), []string{
				"total", "live", "stale", "unknown", "invalid",
			})
			assertKeys(t, "entry.attention", entry["attention"].(map[string]any), []string{
				"status", "issue_count", "codes",
			})
			for _, rawSession := range entry["sessions"].([]any) {
				assertKeys(t, "session", rawSession.(map[string]any), agentStatusSessionKeys)
			}
		}
	}
	for _, rawIssue := range doc["issues"].([]any) {
		assertKeys(t, "issue", rawIssue.(map[string]any), []string{
			"code", "severity", "scope", "feature", "name", "message", "guidance",
		})
	}
}

var agentStatusSessionKeys = []string{
	"kind", "presence", "agent_state", "stage", "stage_recognized", "owner_pid", "child_pid",
	"liveness", "tmux_session", "path", "agent", "started_at", "updated_at", "record_id",
	"record_state", "detail",
}

func decodeStatus(t *testing.T, r *AgentStatusReport) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(encodeStatus(t, r), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestAgentStatusJSONKeySetSnapshots asserts the exact key set at every level
// for the three shapes a consumer must never see drift: a populated external
// document, a populated checkout document, and an empty workspace.
func TestAgentStatusJSONKeySetSnapshots(t *testing.T) {
	t.Run("external populated", func(t *testing.T) {
		entries := []StackEntry{{Name: "api", Branch: "jd/api", Base: "main"}}
		ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
		addExternalWorktree(t, ws, featurePath, entries[0])
		mustCreateRecord(t, featurePath, DirectSessionRecord{Feature: "auth", Name: "api", Stage: DirectStageAgent})
		if err := os.WriteFile(SyncStatePath(featurePath), []byte("pending: [api]\n"), 0644); err != nil {
			t.Fatal(err)
		}
		tmux := fakeTmuxInventory{snap: TmuxSnapshot{
			Available: true, ServerRunning: true, PanesAvailable: true,
			Sessions: map[string]bool{
				ExternalTmuxSessionName("auth", "api"): true,
				ExternalFeatureTmuxSessionName("auth"): true,
			},
			Panes: []TmuxPane{
				{Session: ExternalTmuxSessionName("auth", "api"), Path: filepath.Join(featurePath, "worktrees", "api")},
				{Session: ExternalFeatureTmuxSessionName("auth"), Path: featurePath},
			},
		}}
		proc := fakeProcessProber{probe: map[int]ProcessLiveness{os.Getpid(): ProcessLive}}
		r := buildStatus(t, ws, statusOpts(proc, tmux))
		doc := decodeStatus(t, r)
		feature := doc["features"].([]any)[0].(map[string]any)
		if feature["sync"] == nil || feature["feature_tmux"] == nil {
			t.Fatalf("the populated fixture must exercise sync and feature_tmux: %v", feature)
		}
		if len(doc["issues"].([]any)) == 0 {
			t.Fatal("the populated fixture must carry at least one issue")
		}
		assertAgentStatusKeySets(t, doc)
	})

	t.Run("checkout populated", func(t *testing.T) {
		dir, ws := setupHealthTestRepo(t)
		addStackEntries(t, ws, "auth", []StackEntry{{Name: "api", Branch: "jd/api", Base: "main"}})
		gitInTest(t, dir, "branch", "jd/api")
		writeCheckoutSessionState(t, ws, `{"schema_version":1,"feature":"auth","name":"api","mode":"direct","pid":999090,"stage":"agent","started_at":"2020-01-01T00:00:00Z"}`)
		writeCheckoutSessionLock(t, ws)
		proc := fakeProcessProber{probe: map[int]ProcessLiveness{999090: ProcessLive}}
		r := buildStatus(t, ws, statusOpts(proc, nil))
		doc := decodeStatus(t, r)
		workspace := doc["workspace"].(map[string]any)
		if workspace["checkout_session"] == nil {
			t.Fatal("the checkout fixture must carry a checkout_session")
		}
		entry := doc["features"].([]any)[0].(map[string]any)["entries"].([]any)[0].(map[string]any)
		if len(entry["sessions"].([]any)) != 1 {
			t.Fatalf("the checkout session must be attributed: %v", entry["sessions"])
		}
		assertAgentStatusKeySets(t, doc)
	})

	t.Run("empty workspace", func(t *testing.T) {
		ws := Workspace{Mode: ModeExternal, MetadataRoot: t.TempDir(), Caps: capsFor(ModeExternal)}
		r := buildStatus(t, ws, statusOpts(nil, nil))
		doc := decodeStatus(t, r)
		if len(doc["features"].([]any)) != 0 {
			t.Fatal("the empty fixture must have no feature")
		}
		workspace := doc["workspace"].(map[string]any)
		for _, key := range []string{"checkout_session", "repo_root", "stable_id", "branch", "detached", "dirty", "active_git_op", "degraded_reason"} {
			if workspace[key] != nil {
				t.Fatalf("workspace.%s = %v, want null", key, workspace[key])
			}
		}
		assertAgentStatusKeySets(t, doc)
	})
}

// TestFormatAgentStatusKeepsOneIssuePerLine asserts a message that quotes a
// multi-line parser error still occupies exactly one line in the human view,
// while the JSON document keeps it verbatim.
func TestFormatAgentStatusKeepsOneIssuePerLine(t *testing.T) {
	entries := []StackEntry{{Name: "api", Base: "main"}}
	ws, featurePath := setupExternalStatusWorkspace(t, "auth", entries)
	addExternalWorktree(t, ws, featurePath, entries[0])
	if err := os.WriteFile(SyncStatePath(featurePath), []byte("::bad"), 0644); err != nil {
		t.Fatal(err)
	}
	r := buildStatus(t, ws, statusOpts(nil, nil))
	iss := hasIssue(r, IssueSyncStateInvalid)
	if iss == nil || !strings.Contains(iss.Message, "\n") {
		t.Skip("this yaml build does not produce a multi-line parser error")
	}
	out := FormatAgentStatus(r)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  [") && strings.Contains(line, IssueSyncStateInvalid) {
			if !strings.Contains(line, "inspect ") {
				t.Fatalf("the whole issue must be on one line: %q", line)
			}
			return
		}
	}
	t.Fatalf("the issue line was never rendered:\n%s", out)
}

// TestBuildWorktreeInventory_Additive pins the new Records/ByPath surface while
// asserting that Available, ByBranch (keys and raw porcelain path values), and
// Prunable keep exactly their pre-feature values.
func TestBuildWorktreeInventory_Additive(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "0")
	// Git always records a symlink-free worktree path, so a canonical fixture
	// root keeps the real-Git assertions exact; the raw/canonical split of the
	// legacy ByBranch value is pinned at the parser below.
	root := canonicalize(t.TempDir())
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, root, "init", "--initial-branch=main", repo)
	gitInTest(t, repo, "commit", "--allow-empty", "-m", "init")
	for _, branch := range []string{"attached", "detachedbr", "lockedbr", "gonebr"} {
		gitInTest(t, repo, "branch", branch, "main")
	}
	attached := filepath.Join(root, "wt-attached")
	detached := filepath.Join(root, "wt-detached")
	locked := filepath.Join(root, "wt-locked")
	gone := filepath.Join(root, "wt-gone")
	gitInTest(t, repo, "worktree", "add", attached, "attached")
	gitInTest(t, repo, "worktree", "add", detached, "detachedbr")
	gitInTest(t, detached, "checkout", "--detach")
	gitInTest(t, repo, "worktree", "add", locked, "lockedbr")
	gitInTest(t, repo, "worktree", "lock", "--reason", "busy testing", locked)
	gitInTest(t, repo, "worktree", "add", gone, "gonebr")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	inv := BuildWorktreeInventory(repo)
	if !inv.Available || inv.Err != nil {
		t.Fatalf("inventory = %+v", inv)
	}
	if len(inv.Records) != 5 {
		t.Fatalf("one record per block, got %d: %+v", len(inv.Records), inv.Records)
	}
	if len(inv.ByPath) != len(inv.Records) {
		t.Fatalf("ByPath must key every record: %+v", inv.ByPath)
	}

	main := inv.ByPath[canonicalize(repo)]
	if main.Head == nil || !stackStatusObjectID.MatchString(*main.Head) {
		t.Fatalf("main record head = %v", main.Head)
	}
	if main.BranchRef == nil || *main.BranchRef != "refs/heads/main" {
		t.Fatalf("main record branch = %v", main.BranchRef)
	}
	if main.Detached == nil || *main.Detached {
		t.Fatalf("main record detached = %v", main.Detached)
	}

	det := inv.ByPath[canonicalize(detached)]
	if det.Detached == nil || !*det.Detached || det.BranchRef != nil {
		t.Fatalf("detached record = %+v", det)
	}

	lock := inv.ByPath[canonicalize(locked)]
	if !lock.Locked || lock.LockReason == nil || *lock.LockReason != "busy testing" {
		t.Fatalf("locked record = %+v", lock)
	}

	prunable := inv.ByPath[canonicalize(gone)]
	if !prunable.Prunable || prunable.PrunableReason == nil || *prunable.PrunableReason == "" {
		t.Fatalf("prunable record = %+v", prunable)
	}

	if inv.ByBranch["attached"] != attached {
		t.Fatalf("ByBranch value must stay the raw porcelain path: %q vs %q", inv.ByBranch["attached"], attached)
	}
	if got := inv.ByPath[canonicalize(attached)].Path; got != canonicalize(attached) {
		t.Fatalf("Record.Path must be canonical, got %q", got)
	}
	for _, rec := range inv.Records {
		if rec.Path != canonicalize(rec.Path) {
			t.Fatalf("every Record.Path must be canonical, got %q", rec.Path)
		}
	}

	// The legacy ByBranch value stays the raw porcelain string while
	// Record.Path and the ByPath key are canonicalized, asserted on a path
	// whose temp root differs before and after filepath.EvalSymlinks.
	realRoot := filepath.Join(root, "real")
	linkRoot := filepath.Join(root, "link")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rawPath := filepath.Join(linkRoot, "wt")
	if err := os.MkdirAll(rawPath, 0o755); err != nil {
		t.Fatal(err)
	}
	split := parseWorktreeInventory([]byte("worktree " + rawPath + "\nbranch refs/heads/split\n\n"))
	if !split.Available {
		t.Fatalf("split fixture = %+v", split)
	}
	if canonicalize(rawPath) == rawPath {
		t.Fatalf("the split fixture must differ before and after EvalSymlinks: %q", rawPath)
	}
	if split.ByBranch["split"] != rawPath {
		t.Fatalf("ByBranch value = %q, want the raw porcelain path %q", split.ByBranch["split"], rawPath)
	}
	if _, ok := split.ByPath[canonicalize(rawPath)]; !ok {
		t.Fatalf("ByPath must be keyed by the canonical path: %+v", split.ByPath)
	}
	if !inv.Prunable["gonebr"] {
		t.Fatal("a prunable branch stays in Prunable")
	}
	if _, ok := inv.ByBranch["gonebr"]; ok {
		t.Fatal("a prunable branch must not land in ByBranch")
	}
	if _, ok := inv.ByBranch["detachedbr"]; ok {
		t.Fatal("a detached worktree contributes no branch key")
	}
}

// TestBuildWorktreeInventory_FailClosed pins the deliberate hardening: on
// malformed porcelain the whole inventory is invalidated instead of publishing
// a partial map. A well-formed 64-hex HEAD is valid and must never be
// re-tightened into a 40-length rule.
func TestBuildWorktreeInventory_FailClosed(t *testing.T) {
	sha1 := strings.Repeat("a", 40)
	sha256 := strings.Repeat("b", 64)
	cases := []struct {
		name    string
		payload string
		valid   bool
	}{
		{name: "no worktree line", payload: "HEAD " + sha1 + "\nbranch refs/heads/a\n\n"},
		{name: "empty worktree path", payload: "worktree \nHEAD " + sha1 + "\nbranch refs/heads/a\n\n"},
		{name: "whitespace only worktree path", payload: "worktree \t  \nHEAD " + sha1 + "\nbranch refs/heads/a\n\n"},
		{name: "duplicate path", payload: "worktree /x\nbranch refs/heads/a\n\nworktree /x\nbranch refs/heads/b\n\n"},
		{name: "malformed branch ref", payload: "worktree /x\nbranch heads/a\n\n"},
		{name: "empty branch remainder", payload: "worktree /x\nbranch refs/heads/\n\n"},
		{name: "empty head", payload: "worktree /x\nHEAD \nbranch refs/heads/a\n\n"},
		{name: "non hex head", payload: "worktree /x\nHEAD ZZZZ\nbranch refs/heads/a\n\n"},
		{name: "branch then detached", payload: "worktree /x\nbranch refs/heads/a\ndetached\n\n"},
		{name: "detached then branch", payload: "worktree /x\ndetached\nbranch refs/heads/a\n\n"},
		{name: "sha1 head is valid", payload: "worktree /x\nHEAD " + sha1 + "\nbranch refs/heads/a\n\n", valid: true},
		{name: "sha256 head is valid", payload: "worktree /x\nHEAD " + sha256 + "\nbranch refs/heads/a\n\n", valid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv := parseWorktreeInventory([]byte(tc.payload))
			if tc.valid {
				if !inv.Available || inv.Err != nil {
					t.Fatalf("a well-formed payload must stay available: %+v", inv)
				}
				if inv.Records[0].Head == nil || !strings.Contains(tc.payload, *inv.Records[0].Head) {
					t.Fatalf("the object id must be stored verbatim: %v", inv.Records[0].Head)
				}
				return
			}
			if inv.Available || inv.Err == nil {
				t.Fatalf("malformed porcelain must fail closed: %+v", inv)
			}
			if len(inv.Records) != 0 || len(inv.ByPath) != 0 || len(inv.ByBranch) != 0 || len(inv.Prunable) != 0 {
				t.Fatalf("a failed inventory publishes no partial map: %+v", inv)
			}
		})
	}

	// Real porcelain ends with a blank line after the last block; the trailing
	// empty split element must be a no-op rather than a violation.
	trailing := parseWorktreeInventory([]byte("worktree /x\nHEAD " + sha1 + "\nbranch refs/heads/a\n\n\n"))
	if !trailing.Available {
		t.Fatalf("a trailing blank line must not invalidate the inventory: %+v", trailing)
	}
	// A bare main worktree carries neither a branch nor a detached line.
	bare := parseWorktreeInventory([]byte("worktree /x\nbare\n\n"))
	if !bare.Available || bare.Records[0].Detached != nil || !bare.Records[0].Bare {
		t.Fatalf("bare record = %+v", bare)
	}
	if empty := BuildWorktreeInventory(""); empty.Available || empty.Err == nil {
		t.Fatalf("an empty repo root yields an unavailable inventory with a cause: %+v", empty)
	}
}
