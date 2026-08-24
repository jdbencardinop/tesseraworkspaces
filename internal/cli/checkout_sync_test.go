package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------- Test helpers ----------

func setupCheckoutSyncRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRunCS(t, dir, "init", "--initial-branch=main")
	gitRunCS(t, dir, "config", "user.email", "test@test.com")
	gitRunCS(t, dir, "config", "user.name", "Test")
	writeFileCS(t, dir, ".gitignore", ".tws/\n")
	writeFileCS(t, dir, "README.md", "# root\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "initial")
	return dir
}

func gitRunCS(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %s\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFileCS(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func gitSHA(t *testing.T, dir, ref string) string {
	t.Helper()
	return gitRunCS(t, dir, "rev-parse", ref)
}

func setupFeaturePath(t *testing.T, dir string) string {
	t.Helper()
	fp := filepath.Join(dir, ".tws", "features", "test-feature")
	if err := os.MkdirAll(fp, 0755); err != nil {
		t.Fatal(err)
	}
	return fp
}

func createStackBranch(t *testing.T, dir, branch, base, file, content string) {
	t.Helper()
	gitRunCS(t, dir, "checkout", base)
	gitRunCS(t, dir, "checkout", "-b", branch)
	writeFileCS(t, dir, file, content)
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "commit on "+branch)
}

func saveTestStack(t *testing.T, featurePath string, entries []internal.StackEntry) {
	t.Helper()
	stack := internal.Stack{Branches: entries}
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}
}

func clearStepHook(t *testing.T) {
	t.Helper()
	internal.StepHook = nil
	t.Cleanup(func() { internal.StepHook = nil })
}

// ---------- Test: preconditions ----------

func TestCheckoutSyncRefusesDirtyWorkingTree(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)
	writeFileCS(t, dir, "dirty.txt", "dirty\n")

	err := internal.RunCheckoutSync(internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir})
	if err == nil || !strings.Contains(err.Error(), "working tree is dirty") {
		t.Fatalf("expected dirty-tree refusal, got %v", err)
	}
	if internal.HasCheckoutLock(fp) || internal.HasCheckoutTransaction(fp) {
		t.Fatal("dirty-tree refusal created checkout state")
	}
}

func TestCheckoutSyncRefusesExistingGitOperation(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)
	mergeHead := filepath.Join(dir, ".git", "MERGE_HEAD")
	if err := os.WriteFile(mergeHead, []byte(gitSHA(t, dir, "HEAD")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := internal.RunCheckoutSync(internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir})
	if err == nil || !strings.Contains(err.Error(), "Git operation is in progress") {
		t.Fatalf("expected active-operation refusal, got %v", err)
	}
}

func TestCheckoutLockExclusiveAcquisition(t *testing.T) {
	fp := filepath.Join(t.TempDir(), ".tws", "features", "feature")
	if err := os.MkdirAll(fp, 0755); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- internal.AcquireCheckoutLock(fp)
		}()
	}
	close(start)
	err1, err2 := <-results, <-results
	if (err1 == nil) == (err2 == nil) {
		t.Fatalf("expected exactly one lock acquisition success, got %v and %v", err1, err2)
	}
	internal.ReleaseCheckoutLock(fp)
}

func TestCheckoutStateStoredOutsideFeatureDirectory(t *testing.T) {
	fp := filepath.Join(t.TempDir(), ".tws", "features", "feature")
	if err := os.MkdirAll(fp, 0755); err != nil {
		t.Fatal(err)
	}
	tx := &internal.CheckoutTransaction{Feature: "feature", Stage: internal.StagePlanned}
	if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(filepath.Dir(filepath.Dir(fp)), "state") + string(filepath.Separator)
	if got := internal.CheckoutTransactionPath(fp); !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("transaction path %s is not under %s", got, wantPrefix)
	}
}

func TestCheckoutSyncRefusesExistingTransactionBeforeLock(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)
	tx := &internal.CheckoutTransaction{Feature: "test-feature", OriginalBranch: "main", OriginalHEAD: gitSHA(t, dir, "HEAD"), Stage: internal.StagePlanned}
	if err := internal.SaveCheckoutTransaction(fp, tx); err != nil {
		t.Fatal(err)
	}

	err := internal.RunCheckoutSync(internal.CheckoutSyncOpts{Feature: "test-feature", FeaturePath: fp, RepoDir: dir})
	if err == nil || !strings.Contains(err.Error(), "transaction already exists") {
		t.Fatalf("expected existing-transaction refusal, got %v", err)
	}
	if internal.HasCheckoutLock(fp) {
		t.Fatal("existing transaction acquired a new lock")
	}
}

// ---------- Test: basic rebase ----------

func TestCheckoutSync_BasicRebase(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	// Create branch child on top of main
	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	childSHA := gitSHA(t, dir, "child")

	// Add commit to main (simulating upstream update)
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance main")
	mainSHA := gitSHA(t, dir, "main")

	// Stack: child depends on main
	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	if err := internal.RunCheckoutSync(opts); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Verify child is rebased onto main
	ok, _ := internal.TestIsAncestor(dir, mainSHA, "child")
	if !ok {
		t.Error("child should be descendant of main after rebase")
	}

	// Verify child SHA changed
	newChildSHA := gitSHA(t, dir, "child")
	if newChildSHA == childSHA {
		t.Error("child SHA should have changed after rebase")
	}

	// Verify stack.yaml updated LastBaseSHA
	stack, _ := internal.LoadStack(fp)
	for _, e := range stack.Branches {
		if e.Name == "child" && e.LastBaseSHA != mainSHA {
			t.Errorf("LastBaseSHA not updated: got %s, want %s", e.LastBaseSHA, mainSHA)
		}
	}

	// Verify original branch restored
	branch := gitRunCS(t, dir, "symbolic-ref", "--short", "HEAD")
	if branch != "main" {
		t.Errorf("should be on main, got %s", branch)
	}

	// Verify no transaction/lock remnants
	if internal.HasCheckoutTransaction(fp) {
		t.Error("transaction should be cleaned up")
	}
	if internal.HasCheckoutLock(fp) {
		t.Error("lock should be released")
	}
}

// ---------- Test: amend-aware rebase ----------

func TestCheckoutSync_AmendAwareRebase(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	// Create parent and child
	createStackBranch(t, dir, "parent", "main", "parent.txt", "parent\n")
	parentSHA1 := gitSHA(t, dir, "parent")
	createStackBranch(t, dir, "child", "parent", "child.txt", "child\n")

	// Save stack with LastBaseSHA pointing to parent's current SHA
	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "parent", Base: "main"},
		{Name: "child", Base: "parent", LastBaseSHA: parentSHA1},
	})

	// Now amend parent (force-push simulation)
	gitRunCS(t, dir, "checkout", "parent")
	writeFileCS(t, dir, "parent.txt", "parent amended\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "--amend", "-m", "amended parent")
	parentSHA2 := gitSHA(t, dir, "parent")

	if parentSHA1 == parentSHA2 {
		t.Fatal("amend should change SHA")
	}

	// Go back to main
	gitRunCS(t, dir, "checkout", "main")

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	if err := internal.RunCheckoutSync(opts); err != nil {
		t.Fatalf("amend-aware sync failed: %v", err)
	}

	// Child should now be on top of amended parent (no ghost replay)
	ok, _ := internal.TestIsAncestor(dir, parentSHA2, "child")
	if !ok {
		t.Error("child should descend from amended parent")
	}

	// Old parent commit should NOT be ancestor (no ghost commits)
	ok2, _ := internal.TestIsAncestor(dir, parentSHA1, "child")
	if ok2 {
		t.Error("old parent SHA should not be in child's history (ghost replay detected)")
	}
}

// ---------- Test: conflict and continue ----------

func TestCheckoutSync_ConflictAndContinue(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	// Create conflicting scenario
	createStackBranch(t, dir, "child", "main", "file.txt", "child content\n")
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "file.txt", "main content\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "conflict on main")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected conflict, got: %v", err)
	}

	// Transaction should exist with conflict stage
	tx, loadErr := internal.LoadCheckoutTransaction(fp)
	if loadErr != nil {
		t.Fatalf("transaction should exist: %v", loadErr)
	}
	if tx.Stage != internal.StageConflict {
		t.Errorf("stage should be conflict, got %s", tx.Stage)
	}

	// Resolve conflict manually
	writeFileCS(t, dir, "file.txt", "resolved\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "rebase", "--continue")

	// Now continue
	if err := internal.ContinueCheckoutSync(opts); err != nil {
		t.Fatalf("continue after conflict resolution failed: %v", err)
	}

	// Verify clean state
	if internal.HasCheckoutTransaction(fp) {
		t.Error("transaction should be cleaned up after continue")
	}
}

// ---------- Test: stage interruption and continue ----------

func TestCheckoutSync_InterruptionAfterSwitch(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Interrupt after switch
	internal.StepHook = func(stage internal.CheckoutStage, idx int) error {
		if stage == internal.StageSwitched {
			return fmt.Errorf("injected interruption")
		}
		return nil
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("expected interruption error")
	}

	// Clear hook and continue
	internal.StepHook = nil

	if err := internal.ContinueCheckoutSync(opts); err != nil {
		t.Fatalf("continue after switch interruption failed: %v", err)
	}

	// Should be complete
	if internal.HasCheckoutTransaction(fp) {
		t.Error("should be clean after continue")
	}
}

func TestCheckoutSync_InterruptionAfterRebase(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Interrupt after rebase
	internal.StepHook = func(stage internal.CheckoutStage, idx int) error {
		if stage == internal.StageRebased {
			return fmt.Errorf("injected interruption")
		}
		return nil
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("expected interruption")
	}

	internal.StepHook = nil

	// Continue: should verify ancestry then complete
	if err := internal.ContinueCheckoutSync(opts); err != nil {
		t.Fatalf("continue after rebase interruption: %v", err)
	}
	if internal.HasCheckoutTransaction(fp) {
		t.Error("should be clean")
	}
}

func TestCheckoutSync_InterruptionAfterValidation(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Interrupt during restoring stage
	internal.StepHook = func(stage internal.CheckoutStage, idx int) error {
		if stage == internal.StageRestoring {
			return fmt.Errorf("injected interruption")
		}
		return nil
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
		TestCommand: "true",
	}

	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("expected interruption")
	}

	internal.StepHook = nil

	// Continue should retry restoration
	if err := internal.ContinueCheckoutSync(opts); err != nil {
		t.Fatalf("continue after restoration interruption: %v", err)
	}
	if internal.HasCheckoutTransaction(fp) {
		t.Error("should be clean")
	}
}

// ---------- Test: validation failure and retry ----------

func TestCheckoutSync_ValidationFailureAndRetry(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Use a gate file: validation fails until the file exists
	gateFile := filepath.Join(dir, ".validation-pass")
	testScript := fmt.Sprintf("test -f %s", gateFile)

	// First attempt: validation fails (gate file doesn't exist)
	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
		TestCommand: testScript,
	}

	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("expected validation failure")
	}

	tx, _ := internal.LoadCheckoutTransaction(fp)
	if tx.Stage != internal.StageValidating {
		t.Errorf("stage should be validating, got %s", tx.Stage)
	}
	if tx.FailureKind != internal.FailValidation {
		t.Errorf("failure kind should be validation, got %s", tx.FailureKind)
	}

	// Create gate file so validation passes on retry
	writeFileCS(t, dir, ".validation-pass", "ok")

	// Continue uses persisted test command which now succeeds
	if err := internal.ContinueCheckoutSync(opts); err != nil {
		t.Fatalf("continue with fixed validation: %v", err)
	}
	if internal.HasCheckoutTransaction(fp) {
		t.Error("should be clean")
	}
}

// ---------- Test: persisted push/test context ----------

func TestCheckoutSync_PersistedContext(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Interrupt to persist state
	internal.StepHook = func(stage internal.CheckoutStage, idx int) error {
		if stage == internal.StageSwitched {
			return fmt.Errorf("injected")
		}
		return nil
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
		Push:        true,
		TestCommand: "echo hello",
	}

	_ = internal.RunCheckoutSync(opts)
	internal.StepHook = nil

	// Verify persisted
	tx, _ := internal.LoadCheckoutTransaction(fp)
	if !tx.Push {
		t.Error("push should be persisted")
	}
	if tx.TestCommand != "echo hello" {
		t.Errorf("test command should be persisted, got %q", tx.TestCommand)
	}

	// Continue with conflicting push should error
	optsConflict := opts
	optsConflict.Push = true // same, no conflict
	// But if we tried to add push=true when it was false, that would fail
	// Clean up for this test (push semantics: persisted wins)
	internal.DeleteCheckoutTransaction(fp)
	internal.ReleaseCheckoutLock(fp)
}

// ---------- Test: reject conflicting push on continue ----------

func TestCheckoutSync_RejectPushOnContinue(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Create a transaction without push
	internal.StepHook = func(stage internal.CheckoutStage, idx int) error {
		if stage == internal.StageSwitched {
			return fmt.Errorf("injected")
		}
		return nil
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
		Push:        false,
	}

	_ = internal.RunCheckoutSync(opts)
	internal.StepHook = nil

	// Try continue with push=true (should be rejected)
	opts.Push = true
	err := internal.ContinueCheckoutSync(opts)
	if err == nil {
		t.Fatal("should reject conflicting push on continue")
	}
	if !strings.Contains(err.Error(), "push") {
		t.Errorf("error should mention push conflict: %v", err)
	}

	// Cleanup
	internal.DeleteCheckoutTransaction(fp)
	internal.ReleaseCheckoutLock(fp)
}

// ---------- Test: stale lock without transaction ----------

func TestCheckoutSync_StaleLockNoTransaction(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Create a stale lock from a dead PID
	lockInfo := internal.LockInfo{PID: 999999, Created: "2020-01-01T00:00:00Z"}
	data, _ := internal.MarshalLockInfo(&lockInfo)
	if err := os.MkdirAll(filepath.Dir(internal.CheckoutLockPath(fp)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(internal.CheckoutLockPath(fp), data, 0600); err != nil {
		t.Fatal(err)
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	// Should succeed (stale lock reclaimed since no transaction)
	if err := internal.RunCheckoutSync(opts); err != nil {
		t.Fatalf("should reclaim stale lock without transaction: %v", err)
	}
}

// ---------- Test: stale lock with transaction ----------

func TestCheckoutSync_StaleLockWithTransaction(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Create stale lock + transaction
	lockInfo := internal.LockInfo{PID: 999999, Created: "2020-01-01T00:00:00Z"}
	data, _ := internal.MarshalLockInfo(&lockInfo)
	if err := os.MkdirAll(filepath.Dir(internal.CheckoutLockPath(fp)), 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(internal.CheckoutLockPath(fp), data, 0600)

	tx := &internal.CheckoutTransaction{
		Feature:   "test-feature",
		StartedAt: "2020-01-01T00:00:00Z",
		Stage:     internal.StageSwitched,
	}
	_ = internal.SaveCheckoutTransaction(fp, tx)

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	// Should fail, requiring --continue/--abort
	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("should require --continue/--abort with stale lock + transaction")
	}
	if !strings.Contains(err.Error(), "--continue") {
		t.Errorf("error should mention --continue: %v", err)
	}

	// Cleanup
	internal.DeleteCheckoutTransaction(fp)
	internal.ReleaseCheckoutLock(fp)
}

// ---------- Test: live lock rejection ----------

func TestCheckoutSync_LiveLock(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})
	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")

	// Create lock from current PID (live)
	lockInfo := internal.LockInfo{PID: os.Getpid(), Created: "2020-01-01T00:00:00Z"}
	data, marshalErr := internal.MarshalLockInfo(&lockInfo)
	if marshalErr != nil {
		t.Fatalf("marshal lock: %v", marshalErr)
	}
	lockPath := internal.CheckoutLockPath(fp)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(lockPath, data, 0600); writeErr != nil {
		t.Fatalf("write lock: %v", writeErr)
	}
	// Verify lock exists
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("lock file not found after write: %v", statErr)
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("should refuse to steal live lock")
	}
	if !strings.Contains(err.Error(), "live process") {
		t.Errorf("error should mention live process: %v", err)
	}

	// Cleanup
	internal.ReleaseCheckoutLock(fp)
}

// ---------- Test: original branch restoration ----------

func TestCheckoutSync_OriginalBranchRestoration(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	// Start on a different branch than anything in the stack
	gitRunCS(t, dir, "checkout", "-b", "my-work")
	writeFileCS(t, dir, "work.txt", "work\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "my work")
	origSHA := gitSHA(t, dir, "my-work")

	// Create stack branches
	gitRunCS(t, dir, "checkout", "main")
	gitRunCS(t, dir, "checkout", "-b", "child")
	writeFileCS(t, dir, "child.txt", "child\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "child")

	// Advance main
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance")

	// Go back to our work branch
	gitRunCS(t, dir, "checkout", "my-work")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	if err := internal.RunCheckoutSync(opts); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Should be back on my-work with same HEAD
	branch := gitRunCS(t, dir, "symbolic-ref", "--short", "HEAD")
	if branch != "my-work" {
		t.Errorf("should restore to my-work, got %s", branch)
	}
	currentSHA := gitSHA(t, dir, "HEAD")
	if currentSHA != origSHA {
		t.Errorf("original HEAD changed: was %s, now %s", origSHA, currentSHA)
	}
}

// ---------- Test: original branch in plan (legitimately rebased) ----------

func TestCheckoutSync_OriginalBranchInPlan(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	// Create child branch and start on it
	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	// Stay on child (original branch IS in the plan)

	// Advance main
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance main")

	// Back to child
	gitRunCS(t, dir, "checkout", "child")
	origSHA := gitSHA(t, dir, "child")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	if err := internal.RunCheckoutSync(opts); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Should restore to child but with rebased HEAD (not reset)
	branch := gitRunCS(t, dir, "symbolic-ref", "--short", "HEAD")
	if branch != "child" {
		t.Errorf("should restore to child, got %s", branch)
	}
	newSHA := gitSHA(t, dir, "child")
	if newSHA == origSHA {
		t.Error("child should have been rebased (new SHA)")
	}
}

// ---------- Test: detached HEAD refused ----------

func TestCheckoutSync_DetachedHEADRefused(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	// Detach HEAD
	sha := gitSHA(t, dir, "main")
	gitRunCS(t, dir, "checkout", sha)

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("should refuse detached HEAD")
	}
	if !strings.Contains(err.Error(), "detached") {
		t.Errorf("error should mention detached: %v", err)
	}
}

// ---------- Test: abort restores safely ----------

func TestCheckoutSync_Abort(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Interrupt after switch
	internal.StepHook = func(stage internal.CheckoutStage, idx int) error {
		if stage == internal.StageSwitched {
			return fmt.Errorf("injected")
		}
		return nil
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	_ = internal.RunCheckoutSync(opts)
	internal.StepHook = nil

	// Abort
	if err := internal.AbortCheckoutSync(opts); err != nil {
		t.Fatalf("abort failed: %v", err)
	}

	// Should be back on main
	branch := gitRunCS(t, dir, "symbolic-ref", "--short", "HEAD")
	if branch != "main" {
		t.Errorf("should restore to main after abort, got %s", branch)
	}

	if internal.HasCheckoutTransaction(fp) {
		t.Error("transaction should be removed")
	}
	if internal.HasCheckoutLock(fp) {
		t.Error("lock should be released")
	}
}

// ---------- Test: second conflict (multi-branch stack) ----------

func TestCheckoutSync_SecondConflict(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	// Create two branches that will conflict
	createStackBranch(t, dir, "b1", "main", "shared.txt", "b1 content\n")
	gitRunCS(t, dir, "checkout", "main")
	gitRunCS(t, dir, "checkout", "-b", "b2")
	writeFileCS(t, dir, "shared2.txt", "b2 content\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "b2")

	// Advance main with conflicting content
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "shared.txt", "main conflict\n")
	writeFileCS(t, dir, "shared2.txt", "main conflict2\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "conflict on main")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "b1", Base: "main"},
		{Name: "b2", Base: "main"},
	})

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	// First conflict
	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("expected conflict")
	}

	// Resolve first conflict
	writeFileCS(t, dir, "shared.txt", "resolved\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "rebase", "--continue")

	// Continue - second branch might also conflict
	err = internal.ContinueCheckoutSync(opts)
	if err != nil {
		if strings.Contains(err.Error(), "conflict") {
			// Resolve second conflict
			writeFileCS(t, dir, "shared2.txt", "resolved2\n")
			gitRunCS(t, dir, "add", ".")
			gitRunCS(t, dir, "rebase", "--continue")
			err = internal.ContinueCheckoutSync(opts)
		}
	}
	if err != nil {
		t.Fatalf("should succeed after resolving conflicts: %v", err)
	}
}

// ---------- Test: restoration retry ----------

func TestCheckoutSync_RestorationRetry(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	// Interrupt at restoration
	callCount := 0
	internal.StepHook = func(stage internal.CheckoutStage, idx int) error {
		if stage == internal.StageRestoring {
			callCount++
			if callCount == 1 {
				return fmt.Errorf("injected restoration failure")
			}
		}
		return nil
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("expected restoration interruption")
	}

	// Continue should retry restoration
	internal.StepHook = nil
	if err := internal.ContinueCheckoutSync(opts); err != nil {
		t.Fatalf("restoration retry failed: %v", err)
	}

	branch := gitRunCS(t, dir, "symbolic-ref", "--short", "HEAD")
	if branch != "main" {
		t.Errorf("should be on main, got %s", branch)
	}
}

// ---------- Test: final ancestry verification ----------

func TestCheckoutSync_FinalAncestry(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	// Create a chain: main -> parent -> child
	createStackBranch(t, dir, "parent", "main", "parent.txt", "parent\n")
	createStackBranch(t, dir, "child", "parent", "child.txt", "child\n")

	// Advance main
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "new.txt", "new\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "advance")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "parent", Base: "main"},
		{Name: "child", Base: "parent"},
	})

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
	}

	if err := internal.RunCheckoutSync(opts); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Verify full chain ancestry
	mainSHA := gitSHA(t, dir, "main")
	parentSHA := gitSHA(t, dir, "parent")

	ok, _ := internal.TestIsAncestor(dir, mainSHA, "parent")
	if !ok {
		t.Error("parent should descend from main")
	}
	ok, _ = internal.TestIsAncestor(dir, parentSHA, "child")
	if !ok {
		t.Error("child should descend from parent")
	}
}

// ---------- Test: push failure propagation ----------

func TestCheckoutSync_PushFailurePropagation(t *testing.T) {
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	fp := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "child", "main", "child.txt", "child\n")
	gitRunCS(t, dir, "checkout", "main")

	saveTestStack(t, fp, []internal.StackEntry{
		{Name: "child", Base: "main"},
	})

	opts := internal.CheckoutSyncOpts{
		Feature:     "test-feature",
		FeaturePath: fp,
		RepoDir:     dir,
		Push:        true, // no remote configured - will fail
	}

	err := internal.RunCheckoutSync(opts)
	if err == nil {
		t.Fatal("push should fail (no remote)")
	}
	if !strings.Contains(err.Error(), "push") {
		t.Errorf("error should mention push: %v", err)
	}

	// Transaction should be retained for retry
	if !internal.HasCheckoutTransaction(fp) {
		t.Error("transaction should be retained after push failure")
	}

	// Cleanup
	internal.DeleteCheckoutTransaction(fp)
	internal.ReleaseCheckoutLock(fp)
}

// ---------- Test: external sync regression (worktree mode unchanged) ----------

func TestCheckoutSync_ExternalSyncUnchanged(t *testing.T) {
	clearStepHook(t)
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	if err := createWorktree("external-feature", "parent", "master", repo, false); err != nil {
		t.Fatal(err)
	}
	parentPath := internal.WorktreePath("external-feature", "parent")
	writeAndCommit(t, parentPath, "parent.txt", "parent-v1\n", "parent v1")

	if err := createWorktree("external-feature", "child", "parent", repo, false); err != nil {
		t.Fatal(err)
	}
	childPath := internal.WorktreePath("external-feature", "child")
	writeAndCommit(t, childPath, "child.txt", "child\n", "child")

	writeAndCommit(t, parentPath, "parent.txt", "parent-v2\n", "parent v2")
	parentHead := gitOutput(t, parentPath, "rev-parse", "parent")

	externalPath := internal.FeaturePath("external-feature")
	result := syncFeature("external-feature", externalSyncLayout{FeaturePath: externalPath, WorktreesRoot: filepath.Join(externalPath, "worktrees")}, false, nil)
	if !result.Complete {
		t.Fatalf("external sync incomplete: %+v", result)
	}
	if internal.RunSilentDir(childPath, "git", "merge-base", "--is-ancestor", parentHead, "child") != nil {
		t.Fatal("external child does not contain updated parent")
	}
}
