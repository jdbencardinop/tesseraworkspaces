package internal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testRegistryEnv sets up an isolated HOME and XDG_DATA_HOME for registry tests.
func testRegistryEnv(t *testing.T) (home string, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	home = filepath.Join(tmpDir, "home")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatalf("creating test home: %v", err)
	}

	xdgData := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(xdgData, 0755); err != nil {
		t.Fatalf("creating test XDG_DATA_HOME: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdgData)

	cleanup = func() {} // t.Setenv auto-restores; t.TempDir auto-cleans
	return home, cleanup
}

// createTestGitRepo creates a temporary git repo for registry testing.
func createTestGitRepo(t *testing.T) string {
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

	// Create initial commit for identity
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

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(resolved)
}

// createTestCheckoutWorkspace creates a Git repo with explicit checkout mode.
func createTestCheckoutWorkspace(t *testing.T) string {
	t.Helper()
	dir := createTestGitRepo(t)
	if err := SaveConfigFile(filepath.Join(dir, ".tws", "config.yaml"), Config{WorkspaceMode: string(ModeCheckout)}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRegistryAdd_NewEntry(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	entry, created, err := RegistryAdd(repo, "myalias")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected entry to be newly created")
	}
	if entry.Path != canonicalTestPath(t, repo) {
		t.Fatalf("expected path %s, got %s", canonicalTestPath(t, repo), entry.Path)
	}
	if entry.Kind != RegistryKindRepo {
		t.Fatalf("expected repo kind, got %s", entry.Kind)
	}
	if !containsAlias(entry.Aliases, "myalias") {
		t.Fatal("expected alias myalias")
	}
	if entry.GitIdentity == "" {
		t.Fatal("expected git identity to be computed")
	}
}

func TestRegistryAdd_Idempotent(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	entry1, created1, err := RegistryAdd(repo, "first")
	if err != nil {
		t.Fatal(err)
	}
	if !created1 {
		t.Fatal("expected first add to create")
	}

	entry2, created2, err := RegistryAdd(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("expected second add to be idempotent")
	}
	if entry1.ID != entry2.ID {
		t.Fatal("expected same ID for idempotent add")
	}
}

func TestRegistryAdd_DuplicateAlias(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo1 := createTestGitRepo(t)
	repo2 := createTestGitRepo(t)

	_, _, err := RegistryAdd(repo1, "shared")
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = RegistryAdd(repo2, "shared")
	if err == nil {
		t.Fatal("expected error for duplicate alias")
	}
}

func TestRegistryAdd_AliasOnExisting(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	_, _, err := RegistryAdd(repo, "a1")
	if err != nil {
		t.Fatal(err)
	}

	entry, created, err := RegistryAdd(repo, "a2")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected idempotent (not created)")
	}
	if !containsAlias(entry.Aliases, "a1") || !containsAlias(entry.Aliases, "a2") {
		t.Fatal("expected both aliases")
	}
}

func TestRegistryAdd_CheckoutWorkspace(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	ws := createTestCheckoutWorkspace(t)
	entry, created, err := RegistryAdd(ws, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected new entry")
	}
	if entry.Kind != RegistryKindCheckoutWorkspace || entry.MarkerID == "" {
		t.Fatalf("expected checkout workspace identity, got %+v", entry)
	}
}

func TestRegistryRemove(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	_, _, err := RegistryAdd(repo, "removeme")
	if err != nil {
		t.Fatal(err)
	}

	if err := RegistryRemove("removeme"); err != nil {
		t.Fatal(err)
	}

	entries, err := RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("expected empty registry after remove")
	}
}

func TestRegistryRemove_NeverDeletesTarget(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	_, _, err := RegistryAdd(repo, "nodelete")
	if err != nil {
		t.Fatal(err)
	}

	if err := RegistryRemove("nodelete"); err != nil {
		t.Fatal(err)
	}

	// Verify the directory still exists
	if _, err := os.Stat(repo); err != nil {
		t.Fatalf("workspace directory should still exist: %v", err)
	}
}

func TestRegistryResolve(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	entry, _, err := RegistryAdd(repo, "findme")
	if err != nil {
		t.Fatal(err)
	}

	// Resolve by alias
	found, err := RegistryResolve("findme")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != entry.ID {
		t.Fatal("expected to find entry by alias")
	}

	// Resolve by path
	found, err = RegistryResolve(repo)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != entry.ID {
		t.Fatal("expected to find entry by path")
	}

	// Resolve by ID
	found, err = RegistryResolve(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != entry.ID {
		t.Fatal("expected to find entry by ID")
	}
}

func TestRegistryResolve_Ambiguous(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo1 := createTestGitRepo(t)
	repo2 := createTestGitRepo(t)

	// Create two entries. Set same alias on one, use ID collision scenario
	_, _, err := RegistryAdd(repo1, "unique1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = RegistryAdd(repo2, "unique2")
	if err != nil {
		t.Fatal(err)
	}

	// Resolve a non-existent selector
	found, err := RegistryResolve("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatal("expected nil for missing selector")
	}
}

func TestRegistryCheck(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	entry, _, err := RegistryAdd(repo, "checkme")
	if err != nil {
		t.Fatal(err)
	}

	result := RegistryCheck(entry)
	if result.Status != StatusOK {
		t.Fatalf("expected OK, got %s: %s", result.Status, result.Detail)
	}
}

func TestRegistryCheck_Missing(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	entry := &RegistryEntry{
		ID:   "fake",
		Path: "/nonexistent/path/12345",
	}
	result := RegistryCheck(entry)
	if result.Status != StatusMissing {
		t.Fatalf("expected Missing, got %s", result.Status)
	}
}

func TestRegistryRepair_MovedSameIdentity(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	entry, _, err := RegistryAdd(repo, "repairme")
	if err != nil {
		t.Fatal(err)
	}
	moved := repo + "-moved"
	if err := os.Rename(repo, moved); err != nil {
		t.Fatal(err)
	}
	if err := RegistryRepair("repairme", moved, false); err != nil {
		t.Fatalf("repair moved repo: %v", err)
	}
	repaired, err := RegistryResolve("repairme")
	if err != nil {
		t.Fatal(err)
	}
	if repaired.ID != entry.ID || repaired.Path != canonicalTestPath(t, moved) {
		t.Fatalf("repair did not preserve identity: %+v", repaired)
	}
}

func TestRegistryRepair_IdentityChangeRequiresFlag(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()
	repo1 := createTestGitRepo(t)
	repo2 := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo2, "different.txt"), []byte("different"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = repo2
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "different")
	cmd.Dir = repo2
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", "https://example.test/different.git")
	cmd.Dir = repo2
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	if _, _, err := RegistryAdd(repo1, "repairme"); err != nil {
		t.Fatal(err)
	}
	if err := RegistryRepair("repairme", repo2, false); err == nil {
		t.Fatal("expected identity mismatch error")
	}
	if err := RegistryRepair("repairme", repo2, true); err != nil {
		t.Fatalf("allow identity change: %v", err)
	}
}

func TestRegistryPrune(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo1 := createTestGitRepo(t)
	repo2 := createTestGitRepo(t)
	if _, _, err := RegistryAdd(repo1, "keep"); err != nil {
		t.Fatalf("adding repo1: %v", err)
	}
	if _, _, err := RegistryAdd(repo2, "prune-me"); err != nil {
		t.Fatalf("adding repo2: %v", err)
	}
	expectedRemoved := canonicalTestPath(t, repo2)

	if err := os.RemoveAll(repo2); err != nil {
		t.Fatalf("removing repo2: %v", err)
	}

	removed, err := RegistryPrune()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("expected 1 removed, got %d", len(removed))
	}
	if removed[0].Path != expectedRemoved {
		t.Fatalf("expected pruned path %s, got %s", expectedRemoved, removed[0].Path)
	}

	entries, err := RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatal("expected 1 remaining entry")
	}
}

func TestRegistryAlias(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	_, _, err := RegistryAdd(repo, "orig")
	if err != nil {
		t.Fatal(err)
	}

	if err := RegistryAlias(repo, "newalias", false); err != nil {
		t.Fatal(err)
	}

	entry, err := RegistryResolve("newalias")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("expected entry via new alias")
	}

	if err := RegistryAlias(repo, "newalias", true); err != nil {
		t.Fatal(err)
	}

	entry, err = RegistryResolve("newalias")
	if err != nil {
		t.Fatal(err)
	}
	if entry != nil {
		t.Fatal("expected alias to be removed")
	}
}

func TestRegistryConcurrency(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			repo := createTestGitRepo(t)
			alias := fmt.Sprintf("concurrent-%d", idx)
			_, _, errs[idx] = RegistryAdd(repo, alias)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	entries, err := RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != n {
		t.Fatalf("expected %d entries, got %d (concurrent no-lost-update)", n, len(entries))
	}
}

func TestRegistryPermissions(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	_, _, err := RegistryAdd(repo, "perms")
	if err != nil {
		t.Fatal(err)
	}

	// Check registry directory permissions
	dir := registryDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Fatalf("expected dir perm 0700, got %o", perm)
	}

	// Check registry file permissions
	info, err = os.Stat(registryPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected file perm 0600, got %o", perm)
	}
}

func TestReadAbsenceCreatesNoFiles(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	// Reading from a nonexistent registry should not create any files.
	entries, err := RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if entries != nil {
		t.Fatal("expected nil entries for absent registry")
	}

	// Verify no files were created
	dir := registryDir()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("registry dir should not exist after read, got: %v", err)
	}
}

func TestValidateAlias(t *testing.T) {
	tests := []struct {
		alias string
		valid bool
	}{
		{"myalias", true},
		{"my-alias", true},
		{"my.alias", true},
		{"my_alias", true},
		{"UPPER", true},
		{"123", true},
		{"a", true},
		{"", false},
		{"has spaces", false},
		{"has/slash", false},
		{string(make([]byte, 65)), false},
	}
	for _, tc := range tests {
		err := ValidateAlias(tc.alias)
		if tc.valid && err != nil {
			t.Errorf("expected %q valid, got error: %v", tc.alias, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("expected %q invalid, got nil error", tc.alias)
		}
	}
}

func TestComputeGitIdentity_Stable(t *testing.T) {
	repo := createTestGitRepo(t)
	id1 := ComputeGitIdentity(repo)
	time.Sleep(10 * time.Millisecond)
	id2 := ComputeGitIdentity(repo)
	if id1 != id2 {
		t.Fatalf("identity not stable: %s != %s", id1, id2)
	}
	if id1 == "" {
		t.Fatal("expected non-empty identity")
	}
}

func TestRegistryList_Empty(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	entries, err := RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if entries != nil {
		t.Fatal("expected nil entries for absent registry")
	}
}

// createTestExternalWorkspace creates a temporary external workspace root
// carrying the standard `.tws-workspace` marker directory.
func createTestExternalWorkspace(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(dir, workspaceMarker), 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeRawRegistry(t *testing.T, content string) string {
	t.Helper()
	if err := os.MkdirAll(registryDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := registryPath()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------- Alias validation and collision ----------

func TestRegistryAdd_RejectsInvalidAlias(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	for _, alias := range []string{"has space", "has/slash", string(make([]byte, 65))} {
		if _, _, err := RegistryAdd(repo, alias); err == nil {
			t.Fatalf("expected invalid alias %q to be rejected", alias)
		}
	}

	entries, err := RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid alias must not enroll anything, got %d entries", len(entries))
	}
}

func TestRegistryAdd_RejectsAliasShadowingEntryID(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo1 := createTestGitRepo(t)
	repo2 := createTestGitRepo(t)

	first, _, err := RegistryAdd(repo1, "first")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := RegistryAdd(repo2, first.ID); err == nil {
		t.Fatal("expected alias shadowing an entry ID to be rejected")
	}

	// Resolution must remain unambiguous.
	resolved, err := RegistryResolve(first.ID)
	if err != nil {
		t.Fatalf("ID selector became ambiguous: %v", err)
	}
	if resolved == nil || resolved.ID != first.ID {
		t.Fatal("expected ID selector to resolve to its own entry")
	}
}

func TestRegistryAdd_RejectsAliasShadowingRegisteredPath(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo1 := createTestGitRepo(t)
	repo2 := createTestGitRepo(t)
	first, _, err := RegistryAdd(repo1, "one")
	if err != nil {
		t.Fatal(err)
	}
	// Paths contain '/' so they are rejected by alias syntax first; assert the
	// collision guard still refuses them explicitly.
	reg := &RegistryFile{Version: registryVersion, Entries: []RegistryEntry{*first}}
	if err := checkAliasCollision(reg, first.Path, ""); err == nil {
		t.Fatal("expected alias colliding with a registered path to be rejected")
	}
	if _, _, err := RegistryAdd(repo2, first.Path); err == nil {
		t.Fatal("expected path-shaped alias to be rejected")
	}
}

func TestRegistryAlias_RejectsShadowingIDAndDuplicates(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo1 := createTestGitRepo(t)
	repo2 := createTestGitRepo(t)
	first, _, err := RegistryAdd(repo1, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := RegistryAdd(repo2, "two"); err != nil {
		t.Fatal(err)
	}

	if err := RegistryAlias("two", first.ID, false); err == nil {
		t.Fatal("expected alias shadowing an entry ID to be rejected")
	}
	if err := RegistryAlias("two", "one", false); err == nil {
		t.Fatal("expected duplicate alias to be rejected")
	}
	if err := RegistryAlias("two", "bad alias", false); err == nil {
		t.Fatal("expected invalid alias to be rejected")
	}
}

// ---------- Same-path replacement ----------

func TestRegistryAdd_SamePathReplacedTargetIsRejected(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	ws := createTestExternalWorkspace(t)
	first, _, err := RegistryAdd(ws, "ext")
	if err != nil {
		t.Fatal(err)
	}
	if first.MarkerID == "" {
		t.Fatal("expected external workspace to receive a persistent marker")
	}

	// Replace the workspace in place with a brand-new one.
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, workspaceMarker), 0755); err != nil {
		t.Fatal(err)
	}

	_, _, err = RegistryAdd(ws, "")
	if err == nil {
		t.Fatal("expected replaced target at a registered path to be rejected")
	}
	if !strings.Contains(err.Error(), "--allow-identity-change") {
		t.Fatalf("expected repair guidance in error, got: %v", err)
	}

	stored, err := RegistryResolve("ext")
	if err != nil {
		t.Fatal(err)
	}
	if stored.MarkerID != first.MarkerID {
		t.Fatal("rejected add must not overwrite the recorded identity")
	}
}

func TestRegistryAdd_SamePathPopulatesEmptyHints(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	entry, _, err := RegistryAdd(repo, "hints")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a legacy entry lacking identity hints.
	lock, err := AcquireRegistryLock()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := readRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reg.Entries[0].GitIdentity = ""
	if err := saveRegistry(reg); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}

	updated, created, err := RegistryAdd(repo, "")
	if err != nil {
		t.Fatalf("re-adding a same-path entry with empty hints must succeed: %v", err)
	}
	if created {
		t.Fatal("expected idempotent add")
	}
	if updated.GitIdentity != entry.GitIdentity {
		t.Fatal("expected previously-empty git identity hint to be populated")
	}
}

// ---------- Schema validation on read ----------

func TestReadRegistry_RejectsFutureVersionWithoutMutation(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	raw := "version: 2\nentries: []\nfuture_field: something\n"
	path := writeRawRegistry(t, raw)

	if _, err := RegistryList(); err == nil {
		t.Fatal("expected future schema version to be rejected")
	} else if !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("expected version error, got: %v", err)
	}

	repo := createTestGitRepo(t)
	if _, _, err := RegistryAdd(repo, "nope"); err == nil {
		t.Fatal("expected add to refuse a future-version registry")
	}
	if err := RegistryRemove("nope"); err == nil {
		t.Fatal("expected remove to refuse a future-version registry")
	}
	if _, err := RegistryPrune(); err == nil {
		t.Fatal("expected prune to refuse a future-version registry")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != raw {
		t.Fatalf("future-version registry was mutated:\n%s", after)
	}
}

func TestReadRegistry_RejectsMalformedAndZeroVersion(t *testing.T) {
	cases := map[string]string{
		"empty file":      "",
		"zero version":    "version: 0\nentries: []\n",
		"missing version": "entries: []\n",
		"broken yaml":     "version: 1\nentries: [\n",
		"unknown field":   "version: 1\nentries: []\nsurprise: 1\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, cleanup := testRegistryEnv(t)
			defer cleanup()
			writeRawRegistry(t, content)
			if _, err := RegistryList(); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}

func TestReadRegistry_RejectsBrokenInvariants(t *testing.T) {
	cases := map[string]string{
		"empty id": "version: 1\nentries:\n  - id: \"\"\n    path: /tmp/a\n    kind: repo\n",
		"duplicate id": "version: 1\nentries:\n" +
			"  - id: aaaa\n    path: /tmp/a\n    kind: repo\n" +
			"  - id: aaaa\n    path: /tmp/b\n    kind: repo\n",
		"duplicate path": "version: 1\nentries:\n" +
			"  - id: aaaa\n    path: /tmp/a\n    kind: repo\n" +
			"  - id: bbbb\n    path: /tmp/a\n    kind: repo\n",
		"relative path":      "version: 1\nentries:\n  - id: aaaa\n    path: relative/path\n    kind: repo\n",
		"non-canonical path": "version: 1\nentries:\n  - id: aaaa\n    path: /tmp/a/../a\n    kind: repo\n",
		"unknown kind":       "version: 1\nentries:\n  - id: aaaa\n    path: /tmp/a\n    kind: bogus\n",
		"invalid alias":      "version: 1\nentries:\n  - id: aaaa\n    path: /tmp/a\n    kind: repo\n    aliases: [\"bad alias\"]\n",
		"duplicate alias": "version: 1\nentries:\n" +
			"  - id: aaaa\n    path: /tmp/a\n    kind: repo\n    aliases: [dup]\n" +
			"  - id: bbbb\n    path: /tmp/b\n    kind: repo\n    aliases: [dup]\n",
		"alias shadows id": "version: 1\nentries:\n" +
			"  - id: aaaa\n    path: /tmp/a\n    kind: repo\n" +
			"  - id: bbbb\n    path: /tmp/b\n    kind: repo\n    aliases: [aaaa]\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, cleanup := testRegistryEnv(t)
			defer cleanup()
			writeRawRegistry(t, content)
			if _, err := RegistryList(); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}

func TestValidateRegistry_AcceptsHealthyRegistry(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	writeRawRegistry(t, "version: 1\nentries:\n"+
		"  - id: aaaa\n    path: /tmp/a\n    kind: repo\n    aliases: [alpha]\n"+
		"  - id: bbbb\n    path: /tmp/b\n    kind: external-workspace\n    aliases: [beta]\n")

	entries, err := RegistryList()
	if err != nil {
		t.Fatalf("healthy registry must load: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

// ---------- Persistent marker identity ----------

func TestRegistryAdd_CreatesPersistentCheckoutMarker(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	ws := createTestCheckoutWorkspace(t)
	entry, _, err := RegistryAdd(ws, "co")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ReadWorkspaceMarkerID(CheckoutMarkerDir(canonicalTestPath(t, ws)))
	if err != nil {
		t.Fatal(err)
	}
	if stored == "" || stored != entry.MarkerID {
		t.Fatalf("expected persisted marker %q to match entry marker %q", stored, entry.MarkerID)
	}

	info, err := os.Stat(filepath.Join(CheckoutMarkerDir(canonicalTestPath(t, ws)), workspaceMarkerIDFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected marker perm 0600, got %o", perm)
	}
}

func TestRegistryCheck_IsReadOnlyAndDoesNotCreateMarkers(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestCheckoutWorkspace(t)
	entry := &RegistryEntry{
		ID:          "manual0000000000",
		Path:        canonicalTestPath(t, repo),
		Kind:        RegistryKindCheckoutWorkspace,
		GitIdentity: ComputeGitIdentity(repo),
	}

	if result := RegistryCheck(entry); result.Status != StatusOK {
		t.Fatalf("expected ok, got %s (%s)", result.Status, result.Detail)
	}
	if _, err := os.Stat(filepath.Join(CheckoutMarkerDir(canonicalTestPath(t, repo)), workspaceMarkerIDFile)); !os.IsNotExist(err) {
		t.Fatal("registry check must never create marker files")
	}
}

func TestRegistryCheck_MissingMarkerAfterEnrollmentIsMismatched(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	ws := createTestCheckoutWorkspace(t)
	entry, _, err := RegistryAdd(ws, "co")
	if err != nil {
		t.Fatal(err)
	}
	markerFile := filepath.Join(CheckoutMarkerDir(entry.Path), workspaceMarkerIDFile)
	if err := os.Remove(markerFile); err != nil {
		t.Fatal(err)
	}

	result := RegistryCheck(entry)
	if result.Status != StatusMismatched {
		t.Fatalf("expected mismatched, got %s (%s)", result.Status, result.Detail)
	}
	if !strings.Contains(result.Detail, "marker") {
		t.Fatalf("expected marker detail, got %q", result.Detail)
	}
}

func TestRegistryRepair_MovedCheckoutKeepsIdentityWithoutFlag(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	ws := createTestCheckoutWorkspace(t)
	entry, _, err := RegistryAdd(ws, "moved-checkout")
	if err != nil {
		t.Fatal(err)
	}

	moved := ws + "-moved"
	if err := os.Rename(ws, moved); err != nil {
		t.Fatal(err)
	}
	if got := RegistryCheck(entry); got.Status != StatusMissing {
		t.Fatalf("expected missing before repair, got %s", got.Status)
	}

	if err := RegistryRepair("moved-checkout", moved, false); err != nil {
		t.Fatalf("moved checkout with the same marker must repair without --allow-identity-change: %v", err)
	}

	repaired, err := RegistryResolve("moved-checkout")
	if err != nil {
		t.Fatal(err)
	}
	if repaired.ID != entry.ID || repaired.MarkerID != entry.MarkerID {
		t.Fatalf("repair must preserve stable identity: %+v", repaired)
	}
	if repaired.Path != canonicalTestPath(t, moved) {
		t.Fatalf("expected path %s, got %s", canonicalTestPath(t, moved), repaired.Path)
	}
	if got := RegistryCheck(repaired); got.Status != StatusOK {
		t.Fatalf("expected ok after repair, got %s (%s)", got.Status, got.Detail)
	}
}

func TestRegistryRepair_MovedExternalWorkspaceKeepsIdentityWithoutFlag(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	ws := createTestExternalWorkspace(t)
	entry, _, err := RegistryAdd(ws, "moved-external")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Kind != RegistryKindExternalWorkspace || entry.MarkerID == "" {
		t.Fatalf("expected external workspace with marker, got %+v", entry)
	}

	moved := ws + "-moved"
	if err := os.Rename(ws, moved); err != nil {
		t.Fatal(err)
	}
	if err := RegistryRepair("moved-external", moved, false); err != nil {
		t.Fatalf("moved external workspace must repair without --allow-identity-change: %v", err)
	}

	repaired, err := RegistryResolve("moved-external")
	if err != nil {
		t.Fatal(err)
	}
	if repaired.MarkerID != entry.MarkerID {
		t.Fatal("marker identity must survive a move")
	}
}

func TestRegistryRepair_ReplacedExternalWorkspaceRequiresFlag(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	ws := createTestExternalWorkspace(t)
	entry, _, err := RegistryAdd(ws, "replaced")
	if err != nil {
		t.Fatal(err)
	}

	replacement := filepath.Join(t.TempDir(), "replacement")
	if err := os.MkdirAll(filepath.Join(replacement, workspaceMarker), 0755); err != nil {
		t.Fatal(err)
	}
	if marker, err := ReadWorkspaceMarkerID(ExternalMarkerDir(replacement)); err != nil || marker != "" {
		t.Fatalf("replacement should start without an identity marker, got %q, %v", marker, err)
	}

	if err := RegistryRepair("replaced", replacement, false); err == nil {
		t.Fatal("expected a replaced workspace to require --allow-identity-change")
	}
	if err := RegistryRepair("replaced", replacement, true); err != nil {
		t.Fatalf("explicit identity change must be allowed: %v", err)
	}
	updated, err := RegistryResolve("replaced")
	if err != nil {
		t.Fatal(err)
	}
	if updated.MarkerID == entry.MarkerID {
		t.Fatal("expected the replacement marker to be recorded")
	}
	if updated.MarkerID == "" {
		t.Fatal("expected explicit replacement repair to establish a new marker identity")
	}
	if got := RegistryCheck(updated); got.Status != StatusOK {
		t.Fatalf("expected repaired replacement to check ok, got %s (%s)", got.Status, got.Detail)
	}
}

func TestRegistryRemoveAndPrune_NeverDeleteMarkerFiles(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	kept := createTestExternalWorkspace(t)
	gone := createTestExternalWorkspace(t)
	keptEntry, _, err := RegistryAdd(kept, "kept")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := RegistryAdd(gone, "gone"); err != nil {
		t.Fatal(err)
	}
	markerFile := filepath.Join(ExternalMarkerDir(keptEntry.Path), workspaceMarkerIDFile)

	if err := RegistryRemove("kept"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markerFile); err != nil {
		t.Fatalf("registry remove must never delete marker files: %v", err)
	}

	if _, _, err := RegistryAdd(kept, "kept"); err != nil {
		t.Fatalf("re-adding a removed workspace must reuse its marker: %v", err)
	}
	readded, err := RegistryResolve("kept")
	if err != nil {
		t.Fatal(err)
	}
	if readded.MarkerID != keptEntry.MarkerID {
		t.Fatal("expected the persisted marker identity to be reused after remove/re-add")
	}

	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := RegistryPrune(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markerFile); err != nil {
		t.Fatalf("registry prune must never delete marker files: %v", err)
	}
}

func TestRegistryRemove_AbsentRegistryReturnsNotFound(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	err := RegistryRemove("anything")
	if err == nil {
		t.Fatal("expected removing from an absent registry to fail")
	}
	if !strings.Contains(err.Error(), "no entry matching") {
		t.Fatalf("expected selector-not-found error, got: %v", err)
	}
}

// ---------- Linked worktree normalization ----------

func TestRegistryAdd_NormalizesLinkedWorktreeToMainRepoRoot(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	worktree := filepath.Join(t.TempDir(), "linked-wt")
	cmd := exec.Command("git", "worktree", "add", "-b", "linked", worktree)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	entry, created, err := RegistryAdd(worktree, "linked-ws")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected a new entry")
	}
	if entry.Path != canonicalTestPath(t, repo) {
		t.Fatalf("linked worktree must normalize to main repo root %s, got %s", canonicalTestPath(t, repo), entry.Path)
	}

	// Enrolling the main root afterwards must be idempotent, not a second entry.
	if _, createdAgain, err := RegistryAdd(repo, ""); err != nil {
		t.Fatal(err)
	} else if createdAgain {
		t.Fatal("expected the main repo root to resolve to the existing entry")
	}
	entries, err := RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 entry, got %d", len(entries))
	}
}

// ---------- Marker helpers ----------

func TestEnsureWorkspaceMarkerID_IsStableAndIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".tws")
	first, err := EnsureWorkspaceMarkerID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !markerIDRegexp.MatchString(first) {
		t.Fatalf("expected opaque hex marker, got %q", first)
	}
	second, err := EnsureWorkspaceMarkerID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("marker identity is not stable: %s != %s", first, second)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != workspaceMarkerIDFile {
		t.Fatalf("expected only the marker file to remain, got %v", entries)
	}
}

func TestReadWorkspaceMarkerID_MalformedIsRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".tws")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, workspaceMarkerIDFile), []byte("not-a-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorkspaceMarkerID(dir); err == nil {
		t.Fatal("expected malformed marker to be rejected")
	}
}

func TestRegistryAdd_PlainRepoUsesPersistentMarkerAcrossGitHintChanges(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	entry, created, err := RegistryAdd(repo, "plain")
	if err != nil {
		t.Fatal(err)
	}
	if !created || entry.MarkerID == "" {
		t.Fatalf("expected a new plain-repo entry with a persistent marker, got %+v", entry)
	}

	markerDir, err := GitMarkerDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ReadWorkspaceMarkerID(markerDir)
	if err != nil {
		t.Fatal(err)
	}
	if stored != entry.MarkerID {
		t.Fatalf("expected stored marker %q, got %q", entry.MarkerID, stored)
	}

	for _, args := range [][]string{
		{"git", "remote", "add", "origin", "https://example.test/one.git"},
		{"git", "remote", "set-url", "origin", "https://example.test/two.git"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if got := RegistryCheck(entry); got.Status != StatusOK {
			t.Fatalf("mutable Git hints must not override matching marker identity: %s (%s)", got.Status, got.Detail)
		}
	}

	readded, created, err := RegistryAdd(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if created || readded.ID != entry.ID {
		t.Fatal("re-adding after a Git hint change must remain idempotent")
	}
}

func TestRegistryAdd_FailedReaddDoesNotMintMissingMarker(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestCheckoutWorkspace(t)
	entry, _, err := RegistryAdd(repo, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(CheckoutMarkerDir(entry.Path), workspaceMarkerIDFile)
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}

	if _, _, err := RegistryAdd(repo, ""); err == nil {
		t.Fatal("expected re-add with a missing recorded marker to fail")
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("failed re-add must not mint a replacement marker, got: %v", err)
	}
}

func TestRegistryAdd_MovedWorkspaceRequiresRepairInsteadOfDuplicate(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	ws := createTestExternalWorkspace(t)
	entry, _, err := RegistryAdd(ws, "original")
	if err != nil {
		t.Fatal(err)
	}
	moved := ws + "-moved"
	if err := os.Rename(ws, moved); err != nil {
		t.Fatal(err)
	}

	_, _, err = RegistryAdd(moved, "duplicate")
	if err == nil || !strings.Contains(err.Error(), "registry repair") {
		t.Fatalf("expected moved-target repair guidance, got: %v", err)
	}
	entries, err := RegistryList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("moved workspace must not be enrolled twice: %+v", entries)
	}
}

func TestRegistryAdd_ConcurrentSameTargetIsIdempotent(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	created := make([]bool, n)
	ids := make([]string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			entry, wasCreated, err := RegistryAdd(repo, "")
			errs[idx] = err
			created[idx] = wasCreated
			if entry != nil {
				ids[idx] = entry.ID
			}
		}(i)
	}
	wg.Wait()

	createdCount := 0
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("concurrent add %d failed: %v", i, errs[i])
		}
		if created[i] {
			createdCount++
		}
		if ids[i] == "" || ids[i] != ids[0] {
			t.Fatalf("concurrent adds returned different IDs: %v", ids)
		}
	}
	if createdCount != 1 {
		t.Fatalf("expected exactly one creator, got %d", createdCount)
	}
}

func TestRegistryAdd_WorkspaceModeSwitchRefreshesKind(t *testing.T) {
	_, cleanup := testRegistryEnv(t)
	defer cleanup()

	repo := createTestGitRepo(t)
	entry, _, err := RegistryAdd(repo, "mode-switch")
	if err != nil {
		t.Fatal(err)
	}
	originalMarker := entry.MarkerID

	if err := EnableCheckoutMode(repo); err != nil {
		t.Fatal(err)
	}
	if got := RegistryCheck(entry); got.Status != StatusOK {
		t.Fatalf("mode switch with the same marker must remain healthy: %s (%s)", got.Status, got.Detail)
	}
	checkoutEntry, created, err := RegistryAdd(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if created || checkoutEntry.ID != entry.ID || checkoutEntry.Kind != RegistryKindCheckoutWorkspace {
		t.Fatalf("checkout mode switch must refresh the existing entry: %+v", checkoutEntry)
	}
	if checkoutEntry.MarkerID != originalMarker {
		t.Fatal("workspace mode switch must preserve marker identity")
	}

	if err := EnableExternalMode(repo); err != nil {
		t.Fatal(err)
	}
	repoEntry, created, err := RegistryAdd(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if created || repoEntry.ID != entry.ID || repoEntry.Kind != RegistryKindRepo {
		t.Fatalf("external mode switch must refresh the existing entry: %+v", repoEntry)
	}
	if repoEntry.MarkerID != originalMarker {
		t.Fatal("reverse workspace mode switch must preserve marker identity")
	}
}
