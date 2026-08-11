package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------- fakes ----------

type fakeCLIProber struct {
	probe map[int]internal.ProcessLiveness
}

func (f fakeCLIProber) Probe(pid int) internal.ProcessLiveness {
	if v, ok := f.probe[pid]; ok {
		return v
	}
	return internal.ProcessDead
}

type fakeTmuxOps struct {
	exists map[string]bool
	calls  []string
	killed []string
}

func (f *fakeTmuxOps) Exists(name string) bool {
	f.calls = append(f.calls, "exists:"+name)
	return f.exists[name]
}

func (f *fakeTmuxOps) Kill(name string) error {
	f.calls = append(f.calls, "kill:"+name)
	f.killed = append(f.killed, name)
	return nil
}

// snapshotRecordTree maps each relative path to a content hash and mode, so a
// test can assert a byte-identical tree rather than merely a path set.
func snapshotRecordTree(t *testing.T, dir string) string {
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

// seedRecord writes one record with a chosen owner pid.
func seedRecord(t *testing.T, featurePath, feature, name string, ownerPID int) string {
	t.Helper()
	token, err := internal.CreateDirectSession(featurePath, internal.DirectSessionRecord{
		Feature: feature, Name: name, Stage: internal.DirectStageAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	branchID := internal.DirectSessionBranchID(feature, name)
	if err := internal.UpdateDirectSession(featurePath, branchID, token, func(rec *internal.DirectSessionRecord) {
		rec.OwnerPID = ownerPID
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

// ---------- close matrix ----------

func TestExternalCloseRefusesWhileARecordIsLive(t *testing.T) {
	featurePath := t.TempDir()
	t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
	feature := filepath.Base(featurePath)

	seedRecord(t, featurePath, feature, "api", 900101) // live
	seedRecord(t, featurePath, feature, "api", 900102) // stale sibling
	before := snapshotRecordTree(t, internal.DirectSessionsDir(featurePath))

	proc := fakeCLIProber{probe: map[int]internal.ProcessLiveness{
		900101: internal.ProcessLive,
		900102: internal.ProcessDead,
	}}
	tmux := &fakeTmuxOps{exists: map[string]bool{internal.ExternalTmuxSessionName(feature, "api"): true}}
	var out bytes.Buffer

	err := runExternalClose(&out, feature, "api", proc, tmux)
	if err == nil {
		t.Fatal("a live record must refuse the close")
	}
	if !strings.Contains(err.Error(), "900101") {
		t.Fatalf("the refusal must name the live owner pid: %v", err)
	}
	if len(tmux.killed) != 0 {
		t.Fatalf("no tmux session may be killed: %v", tmux.calls)
	}
	if after := snapshotRecordTree(t, internal.DirectSessionsDir(featurePath)); after != before {
		t.Fatalf("no record may be removed, not even a stale sibling:\n%s\n---\n%s", before, after)
	}
}

func TestExternalCloseCleansStaleThenKillsTmux(t *testing.T) {
	featurePath := t.TempDir()
	t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
	feature := filepath.Base(featurePath)
	seedRecord(t, featurePath, feature, "api", 900103)

	proc := fakeCLIProber{probe: map[int]internal.ProcessLiveness{900103: internal.ProcessDead}}
	session := internal.ExternalTmuxSessionName(feature, "api")
	tmux := &fakeTmuxOps{exists: map[string]bool{session: true}}
	var out bytes.Buffer

	if err := runExternalClose(&out, feature, "api", proc, tmux); err != nil {
		t.Fatal(err)
	}
	if len(tmux.killed) != 1 || tmux.killed[0] != session {
		t.Fatalf("tmux kill = %v", tmux.calls)
	}
	text := out.String()
	if !strings.Contains(text, "Removed 1 stale direct session record(s)") {
		t.Fatalf("output = %q", text)
	}
	if !strings.Contains(text, "Closed tmux session: "+session) {
		t.Fatalf("the existing tmux output must be preserved: %q", text)
	}
	if _, err := os.Stat(internal.DirectSessionsDir(featurePath)); !os.IsNotExist(err) {
		t.Fatal("empty record directories must be pruned")
	}
}

func TestExternalCloseCleansStaleWithoutTmux(t *testing.T) {
	featurePath := t.TempDir()
	t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
	feature := filepath.Base(featurePath)
	seedRecord(t, featurePath, feature, "api", 900104)

	proc := fakeCLIProber{probe: map[int]internal.ProcessLiveness{900104: internal.ProcessDead}}
	tmux := &fakeTmuxOps{exists: map[string]bool{}}
	var out bytes.Buffer

	if err := runExternalClose(&out, feature, "api", proc, tmux); err != nil {
		t.Fatalf("cleaning stale records is a success: %v", err)
	}
	if strings.Contains(out.String(), "no tmux session found") {
		t.Fatalf("output = %q", out.String())
	}
	if !strings.Contains(out.String(), "Removed 1 stale direct session record(s)") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestExternalCloseWithoutRecordsIsUnchanged(t *testing.T) {
	featurePath := t.TempDir()
	t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
	feature := filepath.Base(featurePath)
	session := internal.ExternalTmuxSessionName(feature, "api")

	// Row 4: byte-for-byte identical to the pre-feature behaviour.
	tmux := &fakeTmuxOps{exists: map[string]bool{session: true}}
	var out bytes.Buffer
	if err := runExternalClose(&out, feature, "api", fakeCLIProber{}, tmux); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Closed tmux session: "+session+"\n" {
		t.Fatalf("output = %q", out.String())
	}

	// Row 5: the verbatim existing error string.
	tmux = &fakeTmuxOps{exists: map[string]bool{}}
	out.Reset()
	err := runExternalClose(&out, feature, "api", fakeCLIProber{}, tmux)
	if err == nil || err.Error() != "no tmux session found for "+feature+"/api" {
		t.Fatalf("err = %v", err)
	}
	if out.String() != "" {
		t.Fatalf("output = %q", out.String())
	}
}

// TestExternalCloseReportsUnverifiableRecords covers the three shapes that are
// neither live nor provably dead: an unparseable record, an EPERM owner, and
// the no-tmux case where nothing at all could be acted on.
func TestExternalCloseReportsUnverifiableRecords(t *testing.T) {
	plantInvalid := func(t *testing.T, featurePath, feature string) {
		t.Helper()
		branchID := internal.DirectSessionBranchID(feature, "api")
		dir := filepath.Join(internal.DirectSessionsDir(featurePath), branchID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		token := strings.Repeat("5", 32)
		if err := os.WriteFile(filepath.Join(dir, token+".json"), []byte("{broken"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("invalid record with tmux is reported and kept", func(t *testing.T) {
		featurePath := t.TempDir()
		t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
		feature := filepath.Base(featurePath)
		plantInvalid(t, featurePath, feature)
		before := snapshotRecordTree(t, internal.DirectSessionsDir(featurePath))

		session := internal.ExternalTmuxSessionName(feature, "api")
		tmux := &fakeTmuxOps{exists: map[string]bool{session: true}}
		var out bytes.Buffer
		if err := runExternalClose(&out, feature, "api", fakeCLIProber{}, tmux); err != nil {
			t.Fatalf("an invalid record must not block the tmux kill: %v", err)
		}
		text := out.String()
		if !strings.Contains(text, "could not be verified and were left in place") {
			t.Fatalf("the unverifiable record must be reported: %q", text)
		}
		// Reported before the tmux outcome, never after.
		if strings.Index(text, "could not be verified") > strings.Index(text, "Closed tmux session") {
			t.Fatalf("records must be reported before tmux handling: %q", text)
		}
		if after := snapshotRecordTree(t, internal.DirectSessionsDir(featurePath)); after != before {
			t.Fatal("an unverifiable record is never removed")
		}
	})

	t.Run("eperm owner is reported and kept", func(t *testing.T) {
		featurePath := t.TempDir()
		t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
		feature := filepath.Base(featurePath)
		seedRecord(t, featurePath, feature, "api", 900120)
		before := snapshotRecordTree(t, internal.DirectSessionsDir(featurePath))

		proc := fakeCLIProber{probe: map[int]internal.ProcessLiveness{900120: internal.ProcessUnknown}}
		session := internal.ExternalTmuxSessionName(feature, "api")
		tmux := &fakeTmuxOps{exists: map[string]bool{session: true}}
		var out bytes.Buffer
		if err := runExternalClose(&out, feature, "api", proc, tmux); err != nil {
			t.Fatalf("an EPERM record must not block the tmux kill: %v", err)
		}
		if len(tmux.killed) != 1 {
			t.Fatalf("the tmux session should still be killed: %v", tmux.calls)
		}
		if !strings.Contains(out.String(), "could not be verified and were left in place") {
			t.Fatalf("output = %q", out.String())
		}
		if !strings.Contains(out.String(), "owner pid 900120") {
			t.Fatalf("the unverifiable record must be described: %q", out.String())
		}
		if after := snapshotRecordTree(t, internal.DirectSessionsDir(featurePath)); after != before {
			t.Fatal("an EPERM record is never removed")
		}
	})

	t.Run("no tmux and nothing removed is actionable", func(t *testing.T) {
		featurePath := t.TempDir()
		t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
		feature := filepath.Base(featurePath)
		plantInvalid(t, featurePath, feature)
		seedRecord(t, featurePath, feature, "api", 900121)

		proc := fakeCLIProber{probe: map[int]internal.ProcessLiveness{900121: internal.ProcessUnknown}}
		tmux := &fakeTmuxOps{exists: map[string]bool{}}
		var out bytes.Buffer
		err := runExternalClose(&out, feature, "api", proc, tmux)
		if err == nil {
			t.Fatal("nothing could be acted on, so close must not report success")
		}
		msg := err.Error()
		for _, want := range []string{
			"no tmux session found for " + feature + "/api",
			"2 direct session record(s) could not be verified",
			"tws status --json",
		} {
			if !strings.Contains(msg, want) {
				t.Fatalf("error must mention %q, got %q", want, msg)
			}
		}
		if msg == "no tmux session found for "+feature+"/api" {
			t.Fatal("the flat no-tmux error hides the remaining records")
		}
		if !strings.Contains(out.String(), "could not be verified and were left in place") {
			t.Fatalf("the records must still be listed: %q", out.String())
		}
	})

	t.Run("unverifiable records never leak a full token", func(t *testing.T) {
		featurePath := t.TempDir()
		t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
		feature := filepath.Base(featurePath)
		token := seedRecord(t, featurePath, feature, "api", 900122)

		proc := fakeCLIProber{probe: map[int]internal.ProcessLiveness{900122: internal.ProcessUnknown}}
		var out bytes.Buffer
		err := runExternalClose(&out, feature, "api", proc, &fakeTmuxOps{exists: map[string]bool{}})
		if err == nil {
			t.Fatal("expected the actionable no-tmux error")
		}
		for what, text := range map[string]string{"close stdout": out.String(), "close error": err.Error()} {
			if strings.Contains(text, token) {
				t.Fatalf("%s leaked the full ownership token: %q", what, text)
			}
			if m := cliFullTokenRe.FindString(text); m != "" {
				t.Fatalf("%s leaked a full 32-hex token %q: %q", what, m, text)
			}
		}
		if !strings.Contains(out.String(), token[:8]) {
			t.Fatalf("the report must carry the record id %q: %q", token[:8], out.String())
		}
	})
}

func TestExternalCloseIgnoresInvalidRecords(t *testing.T) {
	featurePath := t.TempDir()
	t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
	feature := filepath.Base(featurePath)
	branchID := internal.DirectSessionBranchID(feature, "api")
	dir := filepath.Join(internal.DirectSessionsDir(featurePath), branchID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("3", 32)
	if err := os.WriteFile(filepath.Join(dir, token+".json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	before := snapshotRecordTree(t, internal.DirectSessionsDir(featurePath))

	session := internal.ExternalTmuxSessionName(feature, "api")
	tmux := &fakeTmuxOps{exists: map[string]bool{session: true}}
	var out bytes.Buffer
	if err := runExternalClose(&out, feature, "api", fakeCLIProber{}, tmux); err != nil {
		t.Fatalf("an invalid record must not block the tmux kill: %v", err)
	}
	if len(tmux.killed) != 1 {
		t.Fatal("the tmux session should still be killed")
	}
	if after := snapshotRecordTree(t, internal.DirectSessionsDir(featurePath)); after != before {
		t.Fatal("an invalid record is never removed")
	}
}

// ---------- guard regressions ----------

func TestExternalCloseGuardsRegisteredSpaceName(t *testing.T) {
	repo := setupGitRepo(t, "main")
	root := withUnifiedWorkspaceEnv(t, repo)

	spaceDir := filepath.Join(root, "learning")
	if err := os.MkdirAll(spaceDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeSpaces(t, root, registeredLearningFixture(spaceDir))
	// A record planted inside the space directory must be untouched.
	if err := os.MkdirAll(filepath.Join(spaceDir, ".sessions", "x"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spaceDir, ".sessions", "x", strings.Repeat("1", 32)+".json"),
		[]byte(`{"schema_version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	before := snapshotRecordTree(t, spaceDir)

	cmd := closeCmd()
	cmd.SetArgs([]string{"learning", "api"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("close must refuse a registered space name")
	}
	var conflict *internal.ErrSpaceNameConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want ErrSpaceNameConflict", err)
	}
	if after := snapshotRecordTree(t, spaceDir); after != before {
		t.Fatal("the space directory must be byte-identical afterwards")
	}
}

func TestExternalCloseFailsClosedOnMalformedSpaces(t *testing.T) {
	for _, fixture := range malformedSpacesFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			repo := setupGitRepo(t, "main")
			root := withUnifiedWorkspaceEnv(t, repo)
			fixture.install(t, root)

			cmd := closeCmd()
			cmd.SetArgs([]string{"auth", "api"})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			err := cmd.Execute()
			if err == nil {
				t.Fatal("close must fail closed on untrusted spaces metadata")
			}
			if !strings.Contains(err.Error(), "cannot verify registered spaces in ") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// ---------- lifecycle refuse-live ----------

func setupExternalFeatureWithBranch(t *testing.T) (string, string) {
	t.Helper()
	repo := setupGitRepo(t, "main")
	withUnifiedWorkspaceEnv(t, repo)
	captureStdout(t, func() {
		if err := addExternal("auth", nil, "api", "main", false, false, false); err != nil {
			t.Fatalf("addExternal: %v", err)
		}
	})
	return repo, internal.FeaturePath("auth")
}

func TestLifecycleVerbsRefuseLiveRecords(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"rename branch", func(t *testing.T) error { return renameBranchExternal("auth", "api", "api2") }},
		{"archive", func(t *testing.T) error { return archiveExternal("auth", "api") }},
		{"delete", func(t *testing.T) error { return deleteExternal("auth", false, false) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, featurePath := setupExternalFeatureWithBranch(t)
			seedRecord(t, featurePath, "auth", "api", os.Getpid())
			before := snapshotRecordTree(t, featurePath)

			var err error
			_ = captureStdout(t, func() { err = c.run(t) })
			if err == nil {
				t.Fatal("a live record must refuse the operation")
			}
			if !strings.Contains(err.Error(), "tws never kills a direct process") {
				t.Fatalf("err = %v", err)
			}
			if after := snapshotRecordTree(t, featurePath); after != before {
				t.Fatal("the tree must be byte-identical after a refusal")
			}
			// No Git command ran: the branch and worktree still exist.
			if !branchExistsInDir(t, repo, "api") {
				t.Fatal("the git branch must still exist")
			}
			if _, statErr := os.Stat(internal.WorktreePath("auth", "api")); statErr != nil {
				t.Fatalf("the worktree must still exist: %v", statErr)
			}
		})
	}
}

func TestLifecycleVerbsCleanStaleThenProceed(t *testing.T) {
	repo, featurePath := setupExternalFeatureWithBranch(t)
	seedRecord(t, featurePath, "auth", "api", 900201)

	var err error
	out := captureStdout(t, func() { err = archiveExternal("auth", "api") })
	if err != nil {
		t.Fatalf("a provably stale record must not block: %v (%s)", err, out)
	}
	if _, statErr := os.Stat(internal.DirectSessionsDir(featurePath)); !os.IsNotExist(statErr) {
		t.Fatal("stale records and their directories must be removed")
	}
	if !branchExistsInDir(t, repo, "api") {
		t.Fatal("archive preserves the git branch")
	}
	if _, statErr := os.Stat(internal.WorktreePath("auth", "api")); !os.IsNotExist(statErr) {
		t.Fatal("archive removes the worktree")
	}
}

func TestRenameFeatureRefusesLiveRecords(t *testing.T) {
	_, featurePath := setupExternalFeatureWithBranch(t)
	seedRecord(t, featurePath, "auth", "api", os.Getpid())
	before := snapshotRecordTree(t, featurePath)

	cmd := renameCmd()
	cmd.SetArgs([]string{"feature", "auth", "auth2"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	var err error
	_ = captureStdout(t, func() { err = cmd.Execute() })
	if err == nil || !strings.Contains(err.Error(), "tws never kills a direct process") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(internal.FeaturePath("auth2")); !os.IsNotExist(statErr) {
		t.Fatal("no rename may have occurred")
	}
	if after := snapshotRecordTree(t, featurePath); after != before {
		t.Fatal("the tree must be byte-identical after a refusal")
	}
}

func TestLifecycleVerbsUnchangedWithoutRecords(t *testing.T) {
	repo, featurePath := setupExternalFeatureWithBranch(t)
	if _, err := os.Stat(internal.DirectSessionsDir(featurePath)); !os.IsNotExist(err) {
		t.Fatal("no records should exist")
	}
	var err error
	_ = captureStdout(t, func() { err = archiveExternal("auth", "api") })
	if err != nil {
		t.Fatalf("archive without records must behave exactly as before: %v", err)
	}
	if !branchExistsInDir(t, repo, "api") {
		t.Fatal("archive preserves the git branch")
	}
}

// ---------- checkout mode never consults external direct records ----------

// knownCLIDirectToken is a fixed, valid 32-hex ownership token so tests can
// name the exact string that must never be read or emitted.
const knownCLIDirectToken = "5f5e1a2b3c4d5e6f7a8b9c0d1e2f3a4b"

var cliFullTokenRe = regexp.MustCompile(`[0-9a-f]{32}`)

// plantDirectRecord hand-writes one record. mode 0000 makes it unreadable, so
// any code that inventories or guards records fails loudly instead of
// silently succeeding.
func plantDirectRecord(t *testing.T, featurePath, feature, name, token string, mode os.FileMode) string {
	t.Helper()
	branchID := internal.DirectSessionBranchID(feature, name)
	recDir := filepath.Join(featurePath, ".sessions", branchID)
	if err := os.MkdirAll(recDir, 0700); err != nil {
		t.Fatal(err)
	}
	// A live owner pid: an accidental guard would refuse, an accidental
	// status read would report it present.
	body := `{"schema_version":1,"token":"` + token + `","feature":"` + feature + `","name":"` + name +
		`","owner_pid":` + strconv.Itoa(os.Getpid()) + `,"stage":"agent","started_at":"2020-01-01T00:00:00Z"}`
	file := filepath.Join(recDir, token+".json")
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if mode != 0600 {
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(file, 0600) })
	}
	return file
}

// setupCheckoutFeatureWithPlantedRecord builds a real checkout-mode
// repository holding feature auth with logical branch api, and plants one
// external direct session record under it.
func setupCheckoutFeatureWithPlantedRecord(t *testing.T, mode os.FileMode) (string, internal.Workspace, string) {
	t.Helper()
	dir := setupGitRepoCheckout(t)
	ws := requireWorkspaceForTest(t, dir)
	featurePath := ws.FeaturePath("auth")
	if err := os.MkdirAll(featurePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{
		Branches: []internal.StackEntry{{Name: "api", Base: "main"}},
	}); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, dir, "branch", "api")
	plantDirectRecord(t, featurePath, "auth", "api", knownCLIDirectToken, mode)
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return dir, ws, featurePath
}

// TestCheckoutLifecycleNeverConsultsDirectRecords is the approved criterion:
// checkout mode does not consult external direct records. Delete, rename,
// archive, and status must neither read, remove, nor react to a planted
// record, and must leave the record tree byte-identical — except for delete,
// which removes the feature the operator asked to delete.
func TestCheckoutLifecycleNeverConsultsDirectRecords(t *testing.T) {
	nonDestructive := []struct {
		name string
		run  func(t *testing.T, ws internal.Workspace) error
	}{
		{"archive", func(t *testing.T, ws internal.Workspace) error {
			return archiveCheckout(ws, "auth", "api")
		}},
		{"rename branch", func(t *testing.T, ws internal.Workspace) error {
			return renameBranchCheckout(ws, "auth", "api", "api2")
		}},
		{"status", func(t *testing.T, ws internal.Workspace) error {
			out, _, err := runStatus(t, "--json")
			if err != nil {
				return err
			}
			if strings.Contains(out, knownCLIDirectToken) {
				t.Fatalf("a planted record must not appear in checkout status: %s", out)
			}
			if strings.Contains(out, "direct-record") {
				t.Fatalf("checkout status must emit no direct-record issue: %s", out)
			}
			human, _, herr := runStatus(t)
			if herr != nil {
				return herr
			}
			if strings.Contains(human, knownCLIDirectToken) || strings.Contains(human, "direct ") {
				t.Fatalf("human status leaked a planted record: %s", human)
			}
			return nil
		}},
	}
	for _, c := range nonDestructive {
		t.Run(c.name, func(t *testing.T) {
			_, ws, featurePath := setupCheckoutFeatureWithPlantedRecord(t, 0600)
			sessions := filepath.Join(featurePath, ".sessions")
			before := snapshotRecordTree(t, sessions)

			var err error
			_ = captureStdout(t, func() { err = c.run(t, ws) })
			if err != nil {
				t.Fatalf("checkout %s must ignore direct records entirely: %v", c.name, err)
			}
			if after := snapshotRecordTree(t, sessions); after != before {
				t.Fatalf("the planted subtree must be byte-identical:\n%s\n---\n%s", before, after)
			}
		})
	}

	// Renaming the feature moves the whole directory, so the record subtree
	// relocates with it and must arrive byte-identical: nothing read it,
	// nothing removed it, nothing refused because of it.
	t.Run("rename feature", func(t *testing.T) {
		_, ws, featurePath := setupCheckoutFeatureWithPlantedRecord(t, 0600)
		before := snapshotRecordTree(t, filepath.Join(featurePath, ".sessions"))

		cmd := renameCmd()
		cmd.SetArgs([]string{"feature", "auth", "auth2"})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		var err error
		_ = captureStdout(t, func() { err = cmd.Execute() })
		if err != nil {
			t.Fatalf("checkout rename must ignore direct records entirely: %v", err)
		}
		moved := filepath.Join(ws.FeaturePath("auth2"), ".sessions")
		if after := snapshotRecordTree(t, moved); after != before {
			t.Fatalf("the record subtree must relocate byte-identically:\n%s\n---\n%s", before, after)
		}
	})

	// Delete is the blocker case. The record is planted unreadable, so any
	// inventory or guard would classify it as blocking and refuse before the
	// normal checkout deletion ever ran.
	t.Run("delete does not guard on records", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads any mode, so an unreadable record cannot be staged")
		}
		_, ws, featurePath := setupCheckoutFeatureWithPlantedRecord(t, 0000)

		var err error
		out := captureStdout(t, func() { err = deleteCheckout(ws, "auth", false, false) })
		if err != nil {
			t.Fatalf("no direct-record guard or read may precede checkout deletion: %v", err)
		}
		if strings.Contains(out, "direct session record") || strings.Contains(out, "stale direct") {
			t.Fatalf("checkout delete must say nothing about records: %q", out)
		}
		if _, statErr := os.Stat(featurePath); !os.IsNotExist(statErr) {
			t.Fatal("the feature the operator asked to delete must be gone, records included")
		}
	})

	// The same with a readable, live record: still no refusal, still no
	// "removed stale record" chatter.
	t.Run("delete ignores a live readable record", func(t *testing.T) {
		_, ws, featurePath := setupCheckoutFeatureWithPlantedRecord(t, 0600)

		var err error
		out := captureStdout(t, func() { err = deleteCheckout(ws, "auth", false, false) })
		if err != nil {
			t.Fatalf("a live record must not refuse a checkout delete: %v", err)
		}
		if strings.Contains(out, "tws never kills a direct process") {
			t.Fatalf("checkout delete must never emit the external refusal: %q", out)
		}
		if _, statErr := os.Stat(featurePath); !os.IsNotExist(statErr) {
			t.Fatal("the feature must be deleted")
		}
	})
}

// ---------- ownership token redaction ----------

// assertRedactedRefusal fails when operator-facing text carries a full
// ownership token; the 8-character record id must be present instead.
func assertRedactedRefusal(t *testing.T, what, text string) {
	t.Helper()
	if strings.Contains(text, knownCLIDirectToken) {
		t.Fatalf("%s leaked the full ownership token: %q", what, text)
	}
	if m := cliFullTokenRe.FindString(text); m != "" {
		t.Fatalf("%s leaked a full 32-hex token %q: %q", what, m, text)
	}
	if !strings.Contains(text, knownCLIDirectToken[:8]) {
		t.Fatalf("%s must carry the record id %q: %q", what, knownCLIDirectToken[:8], text)
	}
}

// TestExternalRefusalsRedactOwnershipToken covers every operator-facing
// refusal that names a record: close and the three lifecycle verbs.
func TestExternalRefusalsRedactOwnershipToken(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		featurePath := t.TempDir()
		t.Setenv("TWS_ROOT", filepath.Dir(featurePath))
		feature := filepath.Base(featurePath)
		plantDirectRecord(t, featurePath, feature, "api", knownCLIDirectToken, 0600)

		proc := fakeCLIProber{probe: map[int]internal.ProcessLiveness{os.Getpid(): internal.ProcessLive}}
		tmux := &fakeTmuxOps{exists: map[string]bool{}}
		var out bytes.Buffer
		err := runExternalClose(&out, feature, "api", proc, tmux)
		if err == nil {
			t.Fatal("a live record must refuse the close")
		}
		assertRedactedRefusal(t, "close refusal", err.Error())
		assertRedactedRefusal(t, "close stdout+refusal", out.String()+err.Error())
	})

	lifecycle := []struct {
		name string
		run  func() error
	}{
		{"rename branch", func() error { return renameBranchExternal("auth", "api", "api2") }},
		{"archive", func() error { return archiveExternal("auth", "api") }},
		{"delete", func() error { return deleteExternal("auth", false, false) }},
	}
	for _, c := range lifecycle {
		t.Run(c.name, func(t *testing.T) {
			_, featurePath := setupExternalFeatureWithBranch(t)
			plantDirectRecord(t, featurePath, "auth", "api", knownCLIDirectToken, 0600)

			var err error
			out := captureStdout(t, func() { err = c.run() })
			if err == nil {
				t.Fatal("a live record must refuse the operation")
			}
			assertRedactedRefusal(t, c.name+" refusal", err.Error())
			if strings.Contains(out, knownCLIDirectToken) {
				t.Fatalf("%s leaked the token on stdout: %q", c.name, out)
			}
		})
	}
}

// TestDirectRecordFailureTextRedactsToken covers the create/update/remove
// wrappers reached through the CLI store seam.
func TestDirectRecordFailureTextRedactsToken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads and writes any mode")
	}
	featurePath := t.TempDir()
	feature, name := "auth", "api"
	branchID := internal.DirectSessionBranchID(feature, name)
	store := realDirectSessionStore{}

	file := plantDirectRecord(t, featurePath, feature, name, knownCLIDirectToken, 0000)

	updErr := store.Update(featurePath, branchID, knownCLIDirectToken, nil)
	if updErr == nil {
		t.Fatal("an unreadable record is an update error")
	}
	assertRedactedRefusal(t, "store.Update", updErr.Error())

	remErr := store.RemoveOwned(featurePath, branchID, knownCLIDirectToken)
	if remErr == nil {
		t.Fatal("an unreadable record is a remove error")
	}
	assertRedactedRefusal(t, "store.RemoveOwned", remErr.Error())

	// A read-only branch directory makes Create fail; its text must carry no
	// 32-hex token at all, since the freshly minted one is not yet known to
	// the operator.
	if err := os.Chmod(filepath.Dir(file), 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(file), 0700) })
	_, createErr := store.Create(featurePath, internal.DirectSessionRecord{Feature: feature, Name: name})
	if createErr == nil {
		t.Fatal("a read-only record directory is a create error")
	}
	if m := cliFullTokenRe.FindString(createErr.Error()); m != "" {
		t.Fatalf("store.Create leaked a full token %q: %v", m, createErr)
	}
}
