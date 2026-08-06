package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func setupRegistryTestEnv(t *testing.T) (home string, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	home = filepath.Join(tmpDir, "home")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatalf("creating test home: %v", err)
	}

	xdgData := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(xdgData, 0755); err != nil {
		t.Fatalf("creating test XDG data dir: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdgData)

	cleanup = func() {} // t.Setenv auto-restores; t.TempDir auto-cleans
	return home, cleanup
}

func createCLITestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	commands := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git setup %v: %v\n%s", args, err, out)
		}
	}
	testFile := filepath.Join(dir, "README")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return dir
}

func TestRegistryAddCmd(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"add", repo, "--alias", "test-ws"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryListCmd_JSON(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	if _, _, err := internal.RegistryAdd(repo, "listed"); err != nil {
		t.Fatalf("pre-adding entry: %v", err)
	}

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	var entries []internal.RegistryEntry
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("parsing JSON: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestRegistryShowCmd(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	if _, _, err := internal.RegistryAdd(repo, "showme"); err != nil {
		t.Fatalf("pre-adding entry: %v", err)
	}

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "showme"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRemoveCmd(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	if _, _, err := internal.RegistryAdd(repo, "removecli"); err != nil {
		t.Fatalf("pre-adding entry: %v", err)
	}

	cmd := registryCmd()
	cmd.SetArgs([]string{"remove", "removecli"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	entries, err := internal.RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("expected empty after remove")
	}
}

func TestRegistryPruneCmd_RequiresForceNonTTY(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	if _, _, err := internal.RegistryAdd(repo, "willprune"); err != nil {
		t.Fatalf("pre-adding entry: %v", err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("removing repo: %v", err)
	}

	// In test environment stdout is not a TTY, so --force is required.
	cmd := registryCmd()
	cmd.SetArgs([]string{"prune", "--missing"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error without --force in non-TTY")
	}

	cmd = registryCmd()
	cmd.SetArgs([]string{"prune", "--missing", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrySubcommands(t *testing.T) {
	cmd := registryCmd()

	subCmds := make(map[string]bool)
	for _, c := range cmd.Commands() {
		subCmds[c.Name()] = true
	}

	expected := []string{"add", "list", "show", "alias", "check", "repair", "remove", "prune"}
	for _, name := range expected {
		if !subCmds[name] {
			t.Errorf("missing subcommand: %s", name)
		}
	}
}

// ---------- JSON shape ----------

func TestRegistryListCmd_EmptyJSONIsArray(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("expected empty JSON array, got %q", got)
	}
}

func TestRegistryCheckCmd_EmptyJSONIsArray(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"check", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("expected empty JSON array, got %q", got)
	}
}

func TestRegistryListCmd_JSONKeyShape(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	if _, _, err := internal.RegistryAdd(repo, "shaped"); err != nil {
		t.Fatal(err)
	}

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("parsing JSON: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(raw))
	}
	assertExactKeys(t, raw[0], "id", "path", "aliases", "kind", "git_identity", "marker_id", "added_at", "updated_at")
}

func TestRegistryCheckCmd_JSONKeyShape(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	if _, _, err := internal.RegistryAdd(repo, "shaped"); err != nil {
		t.Fatal(err)
	}

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"check", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("parsing JSON: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("expected 1 result, got %d", len(raw))
	}
	assertExactKeys(t, raw[0], "entry", "status")

	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw[0]["entry"], &entry); err != nil {
		t.Fatal(err)
	}
	assertExactKeys(t, entry, "id", "path", "aliases", "kind", "git_identity", "marker_id", "added_at", "updated_at")
}

func assertExactKeys(t *testing.T, obj map[string]json.RawMessage, want ...string) {
	t.Helper()
	expected := make(map[string]bool, len(want))
	for _, k := range want {
		expected[k] = true
		if _, ok := obj[k]; !ok {
			t.Errorf("missing JSON key %q", k)
		}
	}
	for k := range obj {
		if !expected[k] {
			t.Errorf("unexpected JSON key %q", k)
		}
	}
}

// ---------- Human output ----------

func TestRegistryCheckCmd_EmptyHumanOutput(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"check"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "no workspaces registered" {
		t.Fatalf("expected 'no workspaces registered', got %q", got)
	}
}

func TestRegistryCheckCmd_MissingPrintsRecoveryHint(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	entry, _, err := internal.RegistryAdd(repo, "hinted")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(entry.Path); err != nil {
		t.Fatal(err)
	}

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"check", "hinted"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "missing") {
		t.Fatalf("expected missing status, got %q", got)
	}
	if !strings.Contains(got, "hint:") || !strings.Contains(got, "registry repair") {
		t.Fatalf("expected a recovery hint, got %q", got)
	}
}

func TestRegistryCommands_WriteToCommandOutput(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"add", []string{"add", repo, "--alias", "routed"}, "registered:"},
		{"alias", []string{"alias", "routed", "extra"}, "added alias"},
		{"remove", []string{"remove", "routed"}, "removed from registry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := registryCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("expected %q on command output, got %q", tc.want, out.String())
			}
		})
	}
}

func TestRegistryRemoveCmd_AbsentRegistryFails(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"remove", "nothing-here"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error when removing from an absent registry")
	}
	if !strings.Contains(err.Error(), "no entry matching") {
		t.Fatalf("expected selector-not-found error, got: %v", err)
	}
	if strings.Contains(out.String(), "removed from registry") {
		t.Fatal("must never print success when nothing was removed")
	}
}

// ---------- init --register ----------

func TestInitCmd_RegisterEnrollsRepo(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	cmd := initCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--register", "--register-alias", "inited"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	entry, err := internal.RegistryResolve("inited")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("expected --register to enroll the repository")
	}
	if entry.Kind != internal.RegistryKindRepo {
		t.Fatalf("expected repo kind, got %s", entry.Kind)
	}
	if !strings.Contains(out.String(), "registered in global registry") {
		t.Fatalf("expected registration confirmation on command output, got %q", out.String())
	}
}

func TestInitCmd_RegisterAliasRequiresRegister(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	cmd := initCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--register-alias", "orphan"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--register-alias requires --register") {
		t.Fatalf("expected --register-alias to require --register, got: %v", err)
	}
}

func TestInitCmd_RegisterRejectsInvalidAlias(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	cmd := initCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--register", "--register-alias", "bad alias"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an invalid registration alias to be rejected")
	}

	entries, err := internal.RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid alias must not enroll anything, got %d entries", len(entries))
	}
}

func TestInitCmd_RegisterOutsideGitGivesContextualError(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	plain := t.TempDir()
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(plain); err != nil {
		t.Fatal(err)
	}

	cmd := initCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--register"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected --register outside a Git repository to fail")
	}
	if !strings.Contains(err.Error(), "Git repository") {
		t.Fatalf("expected a contextual not-a-Git-repository error, got: %v", err)
	}

	// Init itself still ran to completion before registration was attempted.
	if _, statErr := os.Stat(filepath.Join(plain, ".claude", "skills", "tesseraworkspaces", "SKILL.md")); statErr != nil {
		t.Fatalf("registration must happen only after a successful init: %v", statErr)
	}
}

// ---------- External workspaces created by `tws new` ----------

func TestRegistryAdd_ExternalWorkspaceCreatedByNew(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := setupGitRepo(t, "master")
	wsRoot := filepath.Join(t.TempDir(), "workspace")
	t.Setenv("TWS_ROOT", wsRoot)
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	if err := createWorktree("feature", "branch", "", repo, false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	markerDir := filepath.Join(wsRoot, ".tws-workspace")
	if info, err := os.Stat(markerDir); err != nil || !info.IsDir() {
		t.Fatalf("tws new must produce the standard external workspace marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(markerDir, "workspace-id")); !os.IsNotExist(err) {
		t.Fatalf("tws new must not create an enrollment identity, got: %v", err)
	}
	if _, err := os.Stat(internal.WorktreePath("feature", "branch")); err != nil {
		t.Fatalf("worktree path changed: %v", err)
	}

	entry, created, err := internal.RegistryAdd(wsRoot, "from-new")
	if err != nil {
		t.Fatalf("registering a workspace created by tws new: %v", err)
	}
	if !created {
		t.Fatal("expected a new registry entry")
	}
	if entry.Kind != internal.RegistryKindExternalWorkspace {
		t.Fatalf("expected external-workspace kind, got %s", entry.Kind)
	}
	if entry.MarkerID == "" {
		t.Fatal("expected a persistent marker identity")
	}
	if result := internal.RegistryCheck(entry); result.Status != internal.StatusOK {
		t.Fatalf("expected ok, got %s (%s)", result.Status, result.Detail)
	}
}

func TestRegistryCheckCmd_SelectorJSONIsArray(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	if _, _, err := internal.RegistryAdd(repo, "selected"); err != nil {
		t.Fatal(err)
	}

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"check", "selected", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	var results []internal.CheckResult
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("selector JSON must be an array: %v\n%s", err, out.String())
	}
	if len(results) != 1 || results[0].Status != internal.StatusOK {
		t.Fatalf("expected one ok result, got %+v", results)
	}
}

func TestRegistryCheckCmd_HumanOutputIncludesPath(t *testing.T) {
	_, cleanup := setupRegistryTestEnv(t)
	defer cleanup()

	repo := createCLITestRepo(t)
	entry, _, err := internal.RegistryAdd(repo, "visible")
	if err != nil {
		t.Fatal(err)
	}

	cmd := registryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"check", "visible"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), entry.Path) {
		t.Fatalf("check output must identify the workspace path, got %q", out.String())
	}
}
