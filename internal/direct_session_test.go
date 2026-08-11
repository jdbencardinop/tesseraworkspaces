package internal

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeProcessProber is the three-valued liveness seam used by status tests.
type fakeProcessProber struct {
	probe map[int]ProcessLiveness
}

func (f fakeProcessProber) Probe(pid int) ProcessLiveness {
	if f.probe == nil {
		return ProcessDead
	}
	if v, ok := f.probe[pid]; ok {
		return v
	}
	return ProcessDead
}

func mustCreateRecord(t *testing.T, featurePath string, rec DirectSessionRecord) string {
	t.Helper()
	token, err := CreateDirectSession(featurePath, rec)
	if err != nil {
		t.Fatalf("CreateDirectSession: %v", err)
	}
	return token
}

func TestCheckoutAgentSessionNameGolden(t *testing.T) {
	// Pins the pre-refactor output so the hashedSessionID extraction cannot
	// change any existing tmux session name.
	got := CheckoutAgentSessionName("ws", "feat", "name")
	want := "ws_feat_name_5edfbb6c"
	if got != want {
		t.Fatalf("CheckoutAgentSessionName = %q, want %q", got, want)
	}
}

func TestDirectSessionCreateUpdateLoadRemove(t *testing.T) {
	fp := t.TempDir()
	token := mustCreateRecord(t, fp, DirectSessionRecord{
		Feature: "auth", Name: "api", GitBranch: "jd/api", Path: fp + "/worktrees/api", Agent: "claude",
	})
	if len(token) != 32 {
		t.Fatalf("token = %q, want 32 hex chars", token)
	}
	branchID := DirectSessionBranchID("auth", "api")

	// Permissions: 0700 dirs, 0600 file, never group/other readable.
	for _, dir := range []string{DirectSessionsDir(fp), filepath.Join(DirectSessionsDir(fp), branchID)} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("%s perm = %v", dir, info.Mode().Perm())
		}
	}
	recPath := filepath.Join(DirectSessionsDir(fp), branchID, token+".json")
	info, err := os.Stat(recPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("record perm = %v", info.Mode().Perm())
	}

	loaded, err := LoadDirectSessions(fp, branchID, &DirectSessionIdentity{Feature: "auth", Name: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].State != DirectRecordOK {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded[0].Record.Stage != DirectStageStarting {
		t.Fatalf("stage = %q", loaded[0].Record.Stage)
	}
	if loaded[0].Record.OwnerPID != os.Getpid() {
		t.Fatalf("owner pid = %d", loaded[0].Record.OwnerPID)
	}
	if loaded[0].Record.GitBranch != "jd/api" {
		t.Fatalf("git branch = %q", loaded[0].Record.GitBranch)
	}

	if err := UpdateDirectSession(fp, branchID, token, func(r *DirectSessionRecord) {
		r.Stage = DirectStageAgent
		r.ChildPID = 4242
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _ = LoadDirectSessions(fp, branchID, nil)
	if loaded[0].Record.Stage != DirectStageAgent || loaded[0].Record.ChildPID != 4242 {
		t.Fatalf("after update: %+v", loaded[0].Record)
	}

	if err := RemoveOwnedDirectSession(fp, branchID, token); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(DirectSessionsDir(fp)); !os.IsNotExist(err) {
		t.Fatalf(".sessions should be pruned, got %v", err)
	}
}

func TestDirectSessionUpdateRefusesForeignToken(t *testing.T) {
	fp := t.TempDir()
	token := mustCreateRecord(t, fp, DirectSessionRecord{Feature: "auth", Name: "api"})
	branchID := DirectSessionBranchID("auth", "api")
	// Rewrite the record content under a different token value.
	path := filepath.Join(DirectSessionsDir(fp), branchID, token+".json")
	data, _ := os.ReadFile(path)
	var rec DirectSessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	rec.Token = strings.Repeat("f", 32)
	out, _ := json.Marshal(rec)
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateDirectSession(fp, branchID, token, nil); err == nil {
		t.Fatal("update must refuse a record owned by another token")
	}
}

func TestDirectSessionConcurrentRecordsSameBranch(t *testing.T) {
	fp := t.TempDir()
	first := mustCreateRecord(t, fp, DirectSessionRecord{Feature: "auth", Name: "api"})
	second := mustCreateRecord(t, fp, DirectSessionRecord{Feature: "auth", Name: "api"})
	if first == second {
		t.Fatal("two opens must mint distinct tokens")
	}
	branchID := DirectSessionBranchID("auth", "api")
	loaded, err := LoadDirectSessions(fp, branchID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("want 2 records, got %d", len(loaded))
	}

	inventory, err := ListDirectSessions(fp)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory[branchID]) != 2 {
		t.Fatalf("inventory = %+v", inventory)
	}

	// The first owner's cleanup removes only its own file; the branch dir
	// survives because a sibling still holds a record.
	if err := RemoveOwnedDirectSession(fp, branchID, first); err != nil {
		t.Fatal(err)
	}
	loaded, _ = LoadDirectSessions(fp, branchID, nil)
	if len(loaded) != 1 || loaded[0].Record.Token != second {
		t.Fatalf("sibling record must survive, got %+v", loaded)
	}
	if _, err := os.Stat(filepath.Join(DirectSessionsDir(fp), branchID)); err != nil {
		t.Fatalf("branch dir must survive while a sibling record exists: %v", err)
	}

	// Removing with a non-matching token is a no-op.
	if err := RemoveOwnedDirectSession(fp, branchID, strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	loaded, _ = LoadDirectSessions(fp, branchID, nil)
	if len(loaded) != 1 {
		t.Fatalf("non-matching token removal must be a no-op, got %d", len(loaded))
	}

	if err := RemoveOwnedDirectSession(fp, branchID, second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(DirectSessionsDir(fp)); !os.IsNotExist(err) {
		t.Fatalf("both dirs must be pruned, got %v", err)
	}
}

func TestDirectSessionLoadValidation(t *testing.T) {
	fp := t.TempDir()
	branchID := DirectSessionBranchID("auth", "api")
	dir := filepath.Join(DirectSessionsDir(fp), branchID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	valid := strings.Repeat("a", 32)
	bad := strings.Repeat("b", 32)
	unsupported := strings.Repeat("c", 32)
	mismatch := strings.Repeat("d", 32)
	zeroPID := strings.Repeat("e", 32)
	tokenMismatch := strings.Repeat("0", 32)

	write(valid+".json", `{"schema_version":1,"token":"`+valid+`","feature":"auth","name":"api","owner_pid":10,"stage":"agent"}`)
	write(bad+".json", `{not json`)
	write(unsupported+".json", `{"schema_version":99,"token":"`+unsupported+`","feature":"auth","name":"api","owner_pid":10}`)
	write(mismatch+".json", `{"schema_version":1,"token":"`+mismatch+`","feature":"auth","name":"other","owner_pid":10}`)
	write(zeroPID+".json", `{"schema_version":1,"token":"`+zeroPID+`","feature":"auth","name":"api","owner_pid":0}`)
	write(tokenMismatch+".json", `{"schema_version":1,"token":"`+valid+`","feature":"auth","name":"api","owner_pid":10}`)
	// Ignored: not 32 hex, and the atomic-write temp file.
	write("deadbeef.json", `{"schema_version":1}`)
	write(".tmp-session-xyz", `{"schema_version":1}`)

	loaded, err := LoadDirectSessions(fp, branchID, &DirectSessionIdentity{Feature: "auth", Name: "api"})
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]DirectRecordState{}
	for _, l := range loaded {
		states[strings.TrimSuffix(filepath.Base(l.File), ".json")] = l.State
	}
	if len(states) != 6 {
		t.Fatalf("want 6 records (ignoring deadbeef.json and the temp file), got %d: %+v", len(states), states)
	}
	want := map[string]DirectRecordState{
		valid:         DirectRecordOK,
		bad:           DirectRecordInvalid,
		unsupported:   DirectRecordUnsupported,
		mismatch:      DirectRecordInvalid,
		zeroPID:       DirectRecordInvalid,
		tokenMismatch: DirectRecordInvalid,
	}
	for k, v := range want {
		if states[k] != v {
			t.Fatalf("record %s state = %q, want %q", k[:4], states[k], v)
		}
	}

	// The same directory loaded with a nil want skips identity matching.
	loadedAny, err := LoadDirectSessions(fp, branchID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range loadedAny {
		if strings.HasPrefix(filepath.Base(l.File), mismatch) && l.State != DirectRecordOK {
			t.Fatalf("want == nil must skip identity matching, got %q (%s)", l.State, l.Problem)
		}
	}
}

func TestDirectSessionBranchIDPathSafety(t *testing.T) {
	id := DirectSessionBranchID("auth", "feat/api")
	if strings.ContainsAny(id, "/\\") {
		t.Fatalf("branch id %q must be a single path component", id)
	}
	// Two identities whose sanitized prefixes collide must still differ.
	a := DirectSessionBranchID("auth", "a/b")
	b := DirectSessionBranchID("auth", "a_b")
	if a == b {
		t.Fatalf("colliding prefixes must get distinct hash suffixes: %q", a)
	}
}

func TestGuardDirectSessionsFor(t *testing.T) {
	fp := t.TempDir()
	liveToken := mustCreateRecord(t, fp, DirectSessionRecord{Feature: "auth", Name: "api"})
	branchID := DirectSessionBranchID("auth", "api")
	// A second, dead record on the same branch.
	deadToken := mustCreateRecord(t, fp, DirectSessionRecord{Feature: "auth", Name: "api"})
	if err := UpdateDirectSession(fp, branchID, deadToken, func(r *DirectSessionRecord) {
		r.OwnerPID = 999001
	}); err != nil {
		t.Fatal(err)
	}
	proc := fakeProcessProber{probe: map[int]ProcessLiveness{os.Getpid(): ProcessLive, 999001: ProcessDead}}

	blocking, stale, err := GuardDirectSessionsFor(fp, []DirectSessionTarget{{BranchID: branchID}}, proc)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocking) != 1 || blocking[0].Record.Token != liveToken {
		t.Fatalf("blocking = %+v", blocking)
	}
	if len(stale) != 1 || stale[0].Record.Token != deadToken {
		t.Fatalf("stale = %+v", stale)
	}

	// EPERM and invalid records block too, unlike in tws close.
	proc = fakeProcessProber{probe: map[int]ProcessLiveness{os.Getpid(): ProcessUnknown, 999001: ProcessDead}}
	blocking, _, err = GuardDirectSessionsFor(fp, []DirectSessionTarget{{BranchID: branchID}}, proc)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocking) != 1 {
		t.Fatalf("EPERM must block, got %+v", blocking)
	}

	removed, err := RemoveStaleDirectSessions(fp, stale)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	remaining, _ := LoadDirectSessions(fp, branchID, nil)
	if len(remaining) != 1 || remaining[0].Record.Token != liveToken {
		t.Fatalf("only the stale record may be removed, got %+v", remaining)
	}
}

func TestDirectRecordIDPrefixOnly(t *testing.T) {
	token := "abcdef0123456789abcdef0123456789"
	if got := DirectRecordID(token); got != "abcdef01" {
		t.Fatalf("DirectRecordID = %q", got)
	}
	if DirectRecordID("short") != "" {
		t.Fatal("a short token has no record id")
	}
}

// ---------- ownership token redaction ----------

// knownDirectToken is a fixed, valid 32-hex ownership token. Tests use it so
// the exact string that must never be emitted is known up front.
const knownDirectToken = "5f5e1a2b3c4d5e6f7a8b9c0d1e2f3a4b"

// fullTokenRe matches any bare 32-hex run, which is what an unredacted
// ownership token looks like regardless of which token produced it.
var fullTokenRe = regexp.MustCompile(`[0-9a-f]{32}`)

// assertNoFullToken fails when text carries a full ownership token, and
// requires the 8-character record id to be present instead.
func assertNoFullToken(t *testing.T, what, text, token string) {
	t.Helper()
	if strings.Contains(text, token) {
		t.Fatalf("%s leaked the full ownership token: %q", what, text)
	}
	if m := fullTokenRe.FindString(text); m != "" {
		t.Fatalf("%s leaked a full 32-hex token %q: %q", what, m, text)
	}
	if id := DirectRecordID(token); id != "" && !strings.Contains(text, id) {
		t.Fatalf("%s must still carry the record id %q: %q", what, id, text)
	}
}

func TestDirectRecordDisplayPathRedactsToken(t *testing.T) {
	file := filepath.Join("/w", "auth", ".sessions", "bid", knownDirectToken+".json")
	got := DirectRecordDisplayPath(file)
	want := filepath.Join("/w", "auth", ".sessions", "bid", knownDirectToken[:8]+"*.json")
	if got != want {
		t.Fatalf("DirectRecordDisplayPath = %q, want %q", got, want)
	}
	if strings.Contains(got, knownDirectToken) {
		t.Fatal("the display path must never carry the full token")
	}
	if DirectRecordDisplayPath("") != "" {
		t.Fatal("an empty path stays empty")
	}
	// A basename too short to yield a record id is still never echoed raw.
	if got := DirectRecordDisplayPath(filepath.Join("/w", "ab.json")); got != filepath.Join("/w", "unknown*.json") {
		t.Fatalf("short stem = %q", got)
	}
}

func TestDirectSessionErrorTextNeverLeaksToken(t *testing.T) {
	fp := t.TempDir()
	branchID := DirectSessionBranchID("auth", "api")
	dir := filepath.Join(DirectSessionsDir(fp), branchID)
	file := filepath.Join(dir, knownDirectToken+".json")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}

	t.Run("update missing record", func(t *testing.T) {
		err := UpdateDirectSession(fp, branchID, knownDirectToken, nil)
		if err == nil {
			t.Fatal("a missing record is an error")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the fs.ErrNotExist contract must survive redaction: %v", err)
		}
		assertNoFullToken(t, "update-missing", err.Error(), knownDirectToken)
	})

	t.Run("unparseable record", func(t *testing.T) {
		if err := os.WriteFile(file, []byte("{broken"), 0600); err != nil {
			t.Fatal(err)
		}
		err := UpdateDirectSession(fp, branchID, knownDirectToken, nil)
		if err == nil {
			t.Fatal("an unparseable record is an error")
		}
		assertNoFullToken(t, "update-unparseable", err.Error(), knownDirectToken)

		err = RemoveOwnedDirectSession(fp, branchID, knownDirectToken)
		if err == nil {
			t.Fatal("an unparseable record is an error")
		}
		assertNoFullToken(t, "remove-unparseable", err.Error(), knownDirectToken)
	})

	t.Run("foreign token", func(t *testing.T) {
		other := strings.Repeat("c", 32)
		if err := os.WriteFile(file,
			[]byte(`{"schema_version":1,"token":"`+other+`","feature":"auth","name":"api","owner_pid":10}`), 0600); err != nil {
			t.Fatal(err)
		}
		err := UpdateDirectSession(fp, branchID, knownDirectToken, nil)
		if err == nil {
			t.Fatal("a foreign token is an error")
		}
		if strings.Contains(err.Error(), other) {
			t.Fatalf("the on-disk token must not be echoed either: %v", err)
		}
		assertNoFullToken(t, "update-foreign", err.Error(), knownDirectToken)
	})

	t.Run("unreadable record", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads any mode")
		}
		if err := os.WriteFile(file, []byte(`{"schema_version":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(file, 0600) })

		err := UpdateDirectSession(fp, branchID, knownDirectToken, nil)
		if err == nil {
			t.Fatal("an unreadable record is an error")
		}
		assertNoFullToken(t, "update-unreadable", err.Error(), knownDirectToken)

		err = RemoveOwnedDirectSession(fp, branchID, knownDirectToken)
		if err == nil {
			t.Fatal("an unreadable record is an error")
		}
		assertNoFullToken(t, "remove-unreadable", err.Error(), knownDirectToken)

		// The load verdict is operator-facing too: it reaches status JSON as
		// `detail` and refusal text through DescribeDirectSession.
		loaded, loadErr := LoadDirectSessions(fp, branchID, nil)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(loaded) != 1 || loaded[0].State != DirectRecordInvalid {
			t.Fatalf("loaded = %+v", loaded)
		}
		assertNoFullToken(t, "load-problem", loaded[0].Problem, knownDirectToken)
		assertNoFullToken(t, "describe", DescribeDirectSession(loaded[0]), knownDirectToken)
	})

	t.Run("create write failure", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into any mode")
		}
		roFP := t.TempDir()
		roDir := filepath.Join(DirectSessionsDir(roFP), branchID)
		if err := os.MkdirAll(roDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(roDir, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(roDir, 0700) })

		_, err := CreateDirectSession(roFP, DirectSessionRecord{Feature: "auth", Name: "api"})
		if err == nil {
			t.Fatal("a read-only record directory is an error")
		}
		if m := fullTokenRe.FindString(err.Error()); m != "" {
			t.Fatalf("create failure leaked a full token %q: %v", m, err)
		}
	})
}

func TestDescribeDirectSessionRedactsPath(t *testing.T) {
	fp := t.TempDir()
	branchID := DirectSessionBranchID("auth", "api")
	dir := filepath.Join(DirectSessionsDir(fp), branchID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, knownDirectToken+".json"),
		[]byte(`{"schema_version":1,"token":"`+knownDirectToken+
			`","feature":"auth","name":"api","owner_pid":10,"child_pid":11,"stage":"agent","started_at":"2020-01-01T00:00:00Z"}`),
		0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDirectSessions(fp, branchID, nil)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("loaded = %+v, err = %v", loaded, err)
	}
	desc := DescribeDirectSession(loaded[0])
	assertNoFullToken(t, "describe", desc, knownDirectToken)
	if !strings.Contains(desc, knownDirectToken[:8]+"*.json") {
		t.Fatalf("the redacted display path must be shown: %q", desc)
	}
}
