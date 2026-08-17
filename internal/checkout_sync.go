package internal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------- Stage and Failure enums ----------

// CheckoutStage tracks exactly where a checkout-sync transaction is.
type CheckoutStage string

const (
	StagePlanned    CheckoutStage = "planned"
	StageSwitched   CheckoutStage = "switched"
	StageRebasing   CheckoutStage = "rebasing"
	StageConflict   CheckoutStage = "conflict"
	StageRebased    CheckoutStage = "rebased"
	StageValidating CheckoutStage = "validating"
	StageCompleted  CheckoutStage = "completed"
	StageRestoring  CheckoutStage = "restoring"
)

// FailureKind describes the category of failure.
type FailureKind string

const (
	FailNone         FailureKind = ""
	FailConflict     FailureKind = "conflict"
	FailValidation   FailureKind = "validation"
	FailInterruption FailureKind = "interruption"
	FailSwitch       FailureKind = "switch"
	FailPersistence  FailureKind = "persistence"
	FailAncestry     FailureKind = "ancestry"
	FailRestoration  FailureKind = "restoration"
)

// ---------- Plan entries ----------

// CheckoutPlanEntry describes one branch to rebase.
type CheckoutPlanEntry struct {
	Name        string `yaml:"name,omitempty"` // logical StackEntry.Name
	Branch      string `yaml:"branch"`
	Base        string `yaml:"base"`          // resolved git branch name for base
	LastBaseSHA string `yaml:"last_base_sha"` // SHA of base at previous sync
	NewBaseSHA  string `yaml:"new_base_sha"`  // current SHA of base branch
	PreSHA      string `yaml:"pre_sha"`       // branch HEAD before rebase
	PostSHA     string `yaml:"post_sha"`      // branch HEAD after rebase (filled on success)
}

// ---------- Transaction state ----------

// CheckoutTransaction is the persisted state of an in-progress checkout sync.
type CheckoutTransaction struct {
	// Version — absent means 1 (legacy: no-fetch x full x all).
	StateVersion int `yaml:"state_version,omitempty"`

	// Identity
	Feature     string `yaml:"feature"`
	StartedAt   string `yaml:"started_at"`
	LockPID     int    `yaml:"lock_pid"`
	LockCreated string `yaml:"lock_created"`

	// Invocation context (persisted for --continue)
	Push        bool   `yaml:"push"`
	TestCommand string `yaml:"test_command,omitempty"`

	// Original state for restoration
	OriginalBranch string `yaml:"original_branch"`
	OriginalHEAD   string `yaml:"original_head"`

	// Plan
	Plan []CheckoutPlanEntry `yaml:"plan"`

	// Progress
	CurrentIndex int           `yaml:"current_index"` // index into Plan
	Stage        CheckoutStage `yaml:"stage"`
	FailureKind  FailureKind   `yaml:"failure_kind,omitempty"`
	FailureMsg   string        `yaml:"failure_msg,omitempty"`

	// Completed entries
	CompletedIndices []int `yaml:"completed_indices,omitempty"`

	// Frozen new-mode decision. All omitempty, so a legacy transaction loaded
	// from disk round-trips without gaining any of these keys.
	FetchPolicy       string   `yaml:"fetch_policy,omitempty"`
	PropagationPolicy string   `yaml:"propagation_policy,omitempty"`
	ScopeKind         string   `yaml:"scope_kind,omitempty"`
	ScopeSelector     string   `yaml:"scope_selector,omitempty"`
	Selected          []string `yaml:"selected,omitempty"`
	ValidationSource  string   `yaml:"validation_source,omitempty"`
}

// ---------- Paths ----------

func checkoutStateDir(featurePath string) string {
	featuresDir := filepath.Dir(filepath.Clean(featurePath))
	metadataRoot := filepath.Dir(featuresDir)
	return filepath.Join(metadataRoot, "state")
}

func CheckoutTransactionPath(featurePath string) string {
	return filepath.Join(checkoutStateDir(featurePath), filepath.Base(featurePath)+"-checkout-sync.yaml")
}

func CheckoutLockPath(featurePath string) string {
	return filepath.Join(checkoutStateDir(featurePath), filepath.Base(featurePath)+"-checkout-sync.lock")
}

// ---------- Persistence ----------

func LoadCheckoutTransaction(featurePath string) (*CheckoutTransaction, error) {
	data, err := os.ReadFile(CheckoutTransactionPath(featurePath))
	if err != nil {
		return nil, err
	}
	var tx CheckoutTransaction
	if err := yaml.Unmarshal(data, &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

func SaveCheckoutTransaction(featurePath string, tx *CheckoutTransaction) error {
	data, err := yaml.Marshal(tx)
	if err != nil {
		return err
	}
	return atomicWriteFile(CheckoutTransactionPath(featurePath), data, 0600)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tws-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func DeleteCheckoutTransaction(featurePath string) {
	os.Remove(CheckoutTransactionPath(featurePath)) //nolint:errcheck
}

func HasCheckoutTransaction(featurePath string) bool {
	_, err := os.Stat(CheckoutTransactionPath(featurePath))
	return err == nil
}

// ---------- Lock management ----------

type LockInfo struct {
	PID     int    `yaml:"pid"`
	Created string `yaml:"created"`
}

func AcquireCheckoutLock(featurePath string) error {
	lockPath := CheckoutLockPath(featurePath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return fmt.Errorf("create lock directory: %w", err)
	}
	if err := writeLockExclusive(lockPath); err == nil {
		return nil
	} else if !os.IsExist(err) {
		return err
	}

	data, err := os.ReadFile(lockPath)
	if err != nil {
		return fmt.Errorf("read existing checkout-sync lock: %w", err)
	}
	var info LockInfo
	if err := yaml.Unmarshal(data, &info); err != nil {
		return fmt.Errorf("invalid checkout-sync lock; inspect %s and use --abort to recover: %w", lockPath, err)
	}
	if info.PID <= 0 {
		return fmt.Errorf("checkout-sync lock is being initialized or is invalid; retry or inspect %s", lockPath)
	}
	if isProcessAlive(info.PID) {
		return fmt.Errorf("checkout-sync lock held by live process %d (created %s); cannot steal live lock", info.PID, info.Created)
	}
	if HasCheckoutTransaction(featurePath) {
		return fmt.Errorf("stale lock from PID %d detected with existing transaction; use --continue or --abort to recover", info.PID)
	}
	if err := removeLockIfUnchanged(lockPath, data); err != nil {
		return fmt.Errorf("reclaim stale checkout-sync lock: %w", err)
	}
	if err := writeLockExclusive(lockPath); err != nil {
		return fmt.Errorf("acquire checkout-sync lock after stale recovery: %w", err)
	}
	return nil
}

func writeLockExclusive(lockPath string) error {
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	info := LockInfo{PID: os.Getpid(), Created: time.Now().UTC().Format(time.RFC3339)}
	data, err := yaml.Marshal(&info)
	if err == nil {
		_, err = file.Write(data)
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(lockPath)
		return err
	}
	return closeErr
}

func removeLockIfUnchanged(lockPath string, expected []byte) error {
	current, err := os.ReadFile(lockPath)
	if err != nil {
		return err
	}
	if string(current) != string(expected) {
		return fmt.Errorf("lock changed while checking staleness")
	}
	return os.Remove(lockPath)
}

func ReleaseCheckoutLock(featurePath string) {
	os.Remove(CheckoutLockPath(featurePath)) //nolint:errcheck
}

func HasCheckoutLock(featurePath string) bool {
	_, err := os.Stat(CheckoutLockPath(featurePath))
	return err == nil
}

// forceAcquireCheckoutLock reclaims the lock for --continue/--abort.
// A live lock owned by another process is never stolen.
func forceAcquireCheckoutLock(featurePath string) error {
	lockPath := CheckoutLockPath(featurePath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return fmt.Errorf("create lock directory: %w", err)
	}
	data, err := os.ReadFile(lockPath)
	if os.IsNotExist(err) {
		return writeLockExclusive(lockPath)
	}
	if err != nil {
		return fmt.Errorf("read checkout-sync lock: %w", err)
	}
	var info LockInfo
	if err := yaml.Unmarshal(data, &info); err != nil {
		return fmt.Errorf("invalid checkout-sync lock %s: %w", lockPath, err)
	}
	if info.PID <= 0 {
		return fmt.Errorf("checkout-sync lock is being initialized or is invalid; retry or inspect %s", lockPath)
	}
	if info.PID != os.Getpid() && isProcessAlive(info.PID) {
		return fmt.Errorf("lock held by live process %d; cannot reclaim", info.PID)
	}
	if err := removeLockIfUnchanged(lockPath, data); err != nil {
		return fmt.Errorf("reclaim checkout-sync lock: %w", err)
	}
	return writeLockExclusive(lockPath)
}

func ReadCheckoutLock(featurePath string) (*LockInfo, error) {
	data, err := os.ReadFile(CheckoutLockPath(featurePath))
	if err != nil {
		return nil, err
	}
	var info LockInfo
	if err := yaml.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without actually sending a signal
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// ---------- StepHook for testing ----------

// StepHook is a global hook that, if non-nil, is called at each stage transition.
// Tests can set this to inject interruptions (return non-nil error to simulate crash).
var StepHook func(stage CheckoutStage, branchIndex int) error

// ---------- Git helpers ----------

func gitResolveRef(repoDir, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitCurrentBranch(repoDir string) (string, error) {
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not on a branch (detached HEAD?): %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitCheckout(repoDir, branch string) error {
	cmd := exec.Command("git", "checkout", branch)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("checkout %s: %s: %w", branch, string(out), err)
	}
	return nil
}

func gitRebaseOnto(repoDir, newBase, oldBase string) error {
	cmd := exec.Command("git", "rebase", "--no-fork-point", "--onto", newBase, oldBase)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := string(out)
		if strings.Contains(outStr, "CONFLICT") || strings.Contains(outStr, "could not apply") {
			return &RebaseConflictError{Output: outStr}
		}
		return fmt.Errorf("rebase --onto %s %s: %s: %w", newBase, oldBase, outStr, err)
	}
	return nil
}

func gitPlainRebase(repoDir, base string) error {
	cmd := exec.Command("git", "rebase", "--no-fork-point", base)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := string(out)
		if strings.Contains(outStr, "CONFLICT") || strings.Contains(outStr, "could not apply") {
			return &RebaseConflictError{Output: outStr}
		}
		return fmt.Errorf("rebase %s: %s: %w", base, outStr, err)
	}
	return nil
}

func gitIsAncestor(repoDir, ancestor, descendant string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = repoDir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func gitRebaseInProgress(repoDir string) bool {
	return gitPathExists(repoDir, "rebase-merge") || gitPathExists(repoDir, "rebase-apply")
}

func gitOperationInProgress(repoDir string) bool {
	if gitRebaseInProgress(repoDir) {
		return true
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if gitPathExists(repoDir, name) {
			return true
		}
	}
	return false
}

func gitPathExists(repoDir, name string) bool {
	path, err := checkoutGitOutput(repoDir, "rev-parse", "--git-path", name)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoDir, path)
	}
	_, err = os.Stat(path)
	return err == nil
}

func gitWorkingTreeDirty(repoDir string) (bool, error) {
	out, err := checkoutGitOutput(repoDir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func checkoutGitOutput(repoDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitPush(repoDir, branch string) error {
	cmd := exec.Command("git", "push", "--force-with-lease", "origin", branch)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("push %s: %s: %w", branch, string(out), err)
	}
	return nil
}

// RebaseConflictError indicates a rebase stopped due to conflicts.
type RebaseConflictError struct {
	Output string
}

func (e *RebaseConflictError) Error() string {
	return "rebase conflict: " + e.Output
}

// ---------- Plan builder ----------

// BuildCheckoutPlan creates the rebase plan from the stack, resolving SHAs.
//
// A zero selection means "the whole stack" — the frozen no-flag path. With a
// selection it plans only the selected entries, preserving TopoSort order and
// every existing skip rule, and under local-only it excludes anchors entirely.
func BuildCheckoutPlan(repoDir string, stack Stack, sel SyncSelection) ([]CheckoutPlanEntry, error) {
	sorted, err := TopoSort(stack)
	if err != nil {
		return nil, err
	}
	scoped := sel.Names != nil
	localOnly := sel.Policy.Propagation == SyncPropagationLocalOnly

	var plan []CheckoutPlanEntry
	for _, entry := range sorted {
		if entry.Archived {
			continue
		}
		if scoped && !sel.Names[entry.Name] {
			continue
		}
		role := sel.Role(entry.Name)
		if scoped && localOnly && role == SyncRoleAnchor {
			continue
		}
		branch := entry.GitBranch()
		base := entry.Base
		if base == "" {
			continue // root base uses current default; skip
		}
		if scoped && localOnly && role == SyncRolePropagated {
			if parent := GetBranch(stack, entry.Base); parent.Name != "" {
				base = parent.GitBranch()
			}
		}

		newBaseSHA, err := gitResolveRef(repoDir, base)
		if err != nil {
			return nil, fmt.Errorf("resolve base %q for %q: %w", base, branch, err)
		}

		preSHA, err := gitResolveRef(repoDir, branch)
		if err != nil {
			return nil, fmt.Errorf("resolve branch %q: %w", branch, err)
		}

		plan = append(plan, CheckoutPlanEntry{
			Name:        entry.Name,
			Branch:      branch,
			Base:        base,
			LastBaseSHA: entry.LastBaseSHA,
			NewBaseSHA:  newBaseSHA,
			PreSHA:      preSHA,
		})
	}
	return plan, nil
}

// printSyncModeHeader prints the sync-modes header. Its bytes are identical to
// package cli's printSyncModeHeader, which serves the external half.
func printSyncModeHeader(p SyncRunPolicy) {
	fmt.Printf("Sync mode: fetch=%s propagation=%s scope=%s\n", p.Fetch, p.Propagation, p.ScopeLabel())
}

// printLocalOnlyNoOp prints one `[-]` line per selected anchor, then
// `Nothing to propagate.` only when the plan is empty. The literal must stay
// byte-identical to package cli's formatSyncStatus rendering.
func printLocalOnlyNoOp(sel SyncSelection, plan []CheckoutPlanEntry) {
	if sel.Policy.Propagation != SyncPropagationLocalOnly {
		return
	}
	anchors := sel.Anchors()
	if len(anchors) == 0 {
		return
	}
	for _, anchor := range anchors {
		fmt.Printf("  [-] %s (no in-stack parent edge to propagate)\n", anchor.Name)
	}
	if len(plan) == 0 {
		fmt.Println("Nothing to propagate.")
	}
}

// ---------- Transaction executor ----------

// CheckoutSyncOpts configures a checkout sync run.
type CheckoutSyncOpts struct {
	Feature     string
	FeaturePath string
	RepoDir     string
	Push        bool
	TestCommand string
	Verbose     bool

	// Frozen sync-modes decision. A zero Policy with NewMode == false is the
	// legacy default (no-fetch x full x all).
	Policy   SyncRunPolicy
	NewMode  bool
	Continue bool            // --continue was supplied
	Abort    bool            // --abort was supplied
	Changed  map[string]bool // "fetch","no-fetch","full","local-only","only","from","push"
}

// RunCheckoutSync executes the full checkout-sync transaction.
func RunCheckoutSync(opts CheckoutSyncOpts) error {
	if HasCheckoutTransaction(opts.FeaturePath) {
		return fmt.Errorf("checkout sync transaction already exists; use --continue or --abort")
	}
	if gitOperationInProgress(opts.RepoDir) {
		return fmt.Errorf("another Git operation is in progress; complete or abort it before checkout sync")
	}
	dirty, err := gitWorkingTreeDirty(opts.RepoDir)
	if err != nil {
		return fmt.Errorf("check working tree: %w", err)
	}
	if dirty {
		return fmt.Errorf("working tree is dirty; commit or stash changes before checkout sync")
	}

	// Preconditions
	originalBranch, err := gitCurrentBranch(opts.RepoDir)
	if err != nil {
		return fmt.Errorf("cannot sync from detached HEAD: %w", err)
	}
	originalHEAD, err := gitResolveRef(opts.RepoDir, "HEAD")
	if err != nil {
		return err
	}

	// New-mode read-only preflight (I9, I10-I13, I14) — before the lock, so a
	// refusal leaves no lock, no transaction, and no write of any kind.
	var preloaded *Stack
	var sel SyncSelection
	if opts.NewMode {
		stack, loadErr := LoadStack(opts.FeaturePath)
		if loadErr != nil {
			return fmt.Errorf("sync modes require a stack; feature %q has no readable stack.yaml", opts.Feature)
		}
		sel, err = ResolveSyncSelection(stack, opts.Policy, SyncSelectionOpts{
			Mode:    ModeCheckout,
			NewMode: true,
			Feature: opts.Feature,
		})
		if err != nil {
			return err
		}
		if err := verifyCheckoutBasesLocally(opts, stack, sel); err != nil {
			return err
		}
		preloaded = &stack
	}

	// Lock
	if err := AcquireCheckoutLock(opts.FeaturePath); err != nil {
		return err
	}

	if opts.NewMode {
		printSyncModeHeader(opts.Policy)
		if opts.Policy.Fetch == SyncFetchEnabled {
			fmt.Printf("Fetching %s... ", "default repo")
			if fetchErr := RunSilentDir(opts.RepoDir, "git", "fetch"); fetchErr != nil {
				fmt.Println("failed")
			} else {
				fmt.Println("done")
			}
		}
	}

	// Load stack
	var stack Stack
	if preloaded != nil {
		stack = *preloaded
	} else {
		stack, err = LoadStack(opts.FeaturePath)
		if err != nil {
			ReleaseCheckoutLock(opts.FeaturePath)
			return fmt.Errorf("load stack: %w", err)
		}
	}

	// Build plan
	plan, err := BuildCheckoutPlan(opts.RepoDir, stack, sel)
	if err != nil {
		ReleaseCheckoutLock(opts.FeaturePath)
		return fmt.Errorf("build plan: %w", err)
	}

	if opts.NewMode {
		printLocalOnlyNoOp(sel, plan)
	}

	if len(plan) == 0 {
		ReleaseCheckoutLock(opts.FeaturePath)
		return nil
	}

	// Create transaction
	tx := &CheckoutTransaction{
		Feature:        opts.Feature,
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		LockPID:        os.Getpid(),
		LockCreated:    time.Now().UTC().Format(time.RFC3339),
		Push:           opts.Push,
		TestCommand:    opts.TestCommand,
		OriginalBranch: originalBranch,
		OriginalHEAD:   originalHEAD,
		Plan:           plan,
		CurrentIndex:   0,
		Stage:          StagePlanned,
	}
	if opts.NewMode {
		tx.StateVersion = CheckoutTransactionVersion
		tx.FetchPolicy = string(opts.Policy.Fetch)
		tx.PropagationPolicy = string(opts.Policy.Propagation)
		tx.ScopeKind = string(opts.Policy.ScopeKind)
		tx.ScopeSelector = opts.Policy.Selector
		tx.Selected = sel.SelectedNames()
		tx.ValidationSource = "none"
		if opts.TestCommand != "" {
			tx.ValidationSource = "flag"
		}
	}

	// Persist BEFORE switching
	if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
		ReleaseCheckoutLock(opts.FeaturePath)
		return fmt.Errorf("persist transaction: %w", err)
	}

	return executeTransaction(opts, tx)
}

// verifyCheckoutBasesLocally is the checkout half of the I14 no-fetch preflight.
func verifyCheckoutBasesLocally(opts CheckoutSyncOpts, stack Stack, sel SyncSelection) error {
	if opts.Policy.Fetch != SyncFetchDisabled {
		return nil
	}
	localOnly := opts.Policy.Propagation == SyncPropagationLocalOnly
	for _, entry := range sel.Entries {
		if entry.Archived || entry.Base == "" {
			continue
		}
		ref := entry.Base
		if entry.Role == SyncRoleAnchor {
			if localOnly {
				continue
			}
		} else if parent := GetBranch(stack, entry.Base); parent.Name != "" {
			ref = parent.GitBranch()
		}
		if err := VerifyGitRef(opts.RepoDir, ref); err != nil {
			return fmt.Errorf("base %q for stack entry %q does not resolve locally; drop --no-fetch or fetch manually first", ref, entry.Name)
		}
	}
	return nil
}

// ContinueCheckoutSync resumes a previously interrupted transaction.
func ContinueCheckoutSync(opts CheckoutSyncOpts) error {
	tx, err := LoadCheckoutTransaction(opts.FeaturePath)
	if err != nil {
		return fmt.Errorf("no transaction to continue: %w", err)
	}
	if tx.StateVersion > CheckoutTransactionVersion {
		return fmt.Errorf("checkout sync transaction state version %d is newer than %d; upgrade tws or remove %s",
			tx.StateVersion, CheckoutTransactionVersion, CheckoutTransactionPath(opts.FeaturePath))
	}

	if tx.StateVersion >= CheckoutTransactionVersion {
		if err := checkoutContinueMismatches(opts, tx); err != nil {
			return err
		}
		if err := checkoutSelectedStillPresent(opts, tx); err != nil {
			return err
		}
	} else if opts.Push && !tx.Push {
		return fmt.Errorf("cannot add --push to an existing transaction that was started without it; persisted push=%v wins", tx.Push)
	}

	// For continue, we forcibly reclaim the lock (we own the transaction)
	if err := forceAcquireCheckoutLock(opts.FeaturePath); err != nil {
		return err
	}
	tx.LockPID = os.Getpid()

	// Use persisted values
	opts.Push = tx.Push
	opts.TestCommand = tx.TestCommand

	if tx.StateVersion >= CheckoutTransactionVersion {
		printSyncModeHeader(transactionPolicy(tx))
	}

	return resumeTransaction(opts, tx)
}

// transactionPolicy reads the frozen decision back out of a v2 transaction.
func transactionPolicy(tx *CheckoutTransaction) SyncRunPolicy {
	return SyncRunPolicy{
		Fetch:       SyncFetchPolicy(tx.FetchPolicy),
		Propagation: SyncPropagationPolicy(tx.PropagationPolicy),
		ScopeKind:   SyncScopeKind(tx.ScopeKind),
		Selector:    tx.ScopeSelector,
	}
}

// checkoutContinueMismatches applies §10.5 rules 2, 3, and 5 to a v2 resume.
func checkoutContinueMismatches(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	changed := opts.Changed
	persisted := transactionPolicy(tx)
	if changed["fetch"] || changed["no-fetch"] {
		if opts.Policy.Fetch != persisted.Fetch {
			return checkoutContinueMismatch("fetch", string(persisted.Fetch), string(opts.Policy.Fetch))
		}
	}
	if changed["full"] || changed["local-only"] {
		if opts.Policy.Propagation != persisted.Propagation {
			return checkoutContinueMismatch("propagation", string(persisted.Propagation), string(opts.Policy.Propagation))
		}
	}
	if changed["only"] || changed["from"] {
		if opts.Policy.ScopeLabel() != persisted.ScopeLabel() {
			return checkoutContinueMismatch("scope", persisted.ScopeLabel(), opts.Policy.ScopeLabel())
		}
	}
	if changed["push"] && opts.Push != tx.Push {
		return checkoutContinueMismatch("push", fmt.Sprintf("%v", tx.Push), fmt.Sprintf("%v", opts.Push))
	}
	return nil
}

func checkoutContinueMismatch(axis, started, requested string) error {
	return fmt.Errorf("cannot change %s on --continue: the run was started with %s=%s and this invocation requests %s", axis, axis, started, requested)
}

// checkoutSelectedStillPresent enforces §10.5 rule 7.
func checkoutSelectedStillPresent(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	if len(tx.Selected) == 0 {
		return nil
	}
	stack, err := LoadStack(opts.FeaturePath)
	if err != nil {
		// A v2 resume cannot verify its selection without the stack, and
		// silently proceeding would resume a scope nothing can confirm.
		return fmt.Errorf("load stack: %w", err)
	}
	for _, name := range tx.Selected {
		if !HasBranch(stack, name) {
			return fmt.Errorf("selected stack entry %q no longer exists in stack; use --abort", name)
		}
	}
	return nil
}

// AbortCheckoutSync safely aborts and restores original state.
func AbortCheckoutSync(opts CheckoutSyncOpts) error {
	tx, err := LoadCheckoutTransaction(opts.FeaturePath)
	if err != nil {
		return fmt.Errorf("no transaction to abort: %w", err)
	}
	// Deferred I7: without a trigger flag, --continue --abort is only refused
	// against new-mode state. A legacy transaction keeps today's behaviour,
	// where --abort wins and --continue is ignored.
	if opts.Continue && tx.StateVersion >= CheckoutTransactionVersion {
		return fmt.Errorf("--continue and --abort are mutually exclusive")
	}

	if err := forceAcquireCheckoutLock(opts.FeaturePath); err != nil {
		return err
	}

	// Abort any in-progress rebase
	if gitRebaseInProgress(opts.RepoDir) {
		cmd := exec.Command("git", "rebase", "--abort")
		cmd.Dir = opts.RepoDir
		_ = cmd.Run()
	}

	// Restore original branch
	if err := restoreOriginal(opts, tx); err != nil {
		return fmt.Errorf("abort restoration failed: %w; manual recovery may be needed", err)
	}

	DeleteCheckoutTransaction(opts.FeaturePath)
	ReleaseCheckoutLock(opts.FeaturePath)
	return nil
}

// ---------- Transaction execution ----------

func executeTransaction(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	for tx.CurrentIndex < len(tx.Plan) {
		if err := processBranch(opts, tx); err != nil {
			return err
		}
		tx.CompletedIndices = append(tx.CompletedIndices, tx.CurrentIndex)
		tx.CurrentIndex++
		tx.Stage = StagePlanned
		if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
			return fmt.Errorf("persist progress: %w", err)
		}
	}

	return finalizeTransaction(opts, tx)
}

func resumeTransaction(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	switch tx.Stage {
	case StageConflict:
		// User must have resolved the rebase
		if gitRebaseInProgress(opts.RepoDir) {
			return fmt.Errorf("rebase still in progress; finish resolving conflicts then run --continue again")
		}
		// Verify ancestry: base must be ancestor of current branch
		entry := tx.Plan[tx.CurrentIndex]
		ok, err := gitIsAncestor(opts.RepoDir, entry.NewBaseSHA, entry.Branch)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("after conflict resolution, %s is not descended from base %s", entry.Branch, entry.Base)
		}
		// Record post-SHA
		postSHA, err := gitResolveRef(opts.RepoDir, entry.Branch)
		if err != nil {
			return err
		}
		tx.Plan[tx.CurrentIndex].PostSHA = postSHA
		tx.Stage = StageRebased
		if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
			return err
		}
		// Fall through to validation
		return resumeFromRebased(opts, tx)

	case StageSwitched:
		// Interrupted after switch but before rebase
		return resumeFromSwitched(opts, tx)

	case StageRebased:
		// Interrupted after rebase, before validation
		return resumeFromRebased(opts, tx)

	case StageValidating:
		// Re-run validation
		return resumeFromValidating(opts, tx)

	case StageRestoring:
		// Retry restoration
		return resumeFromRestoring(opts, tx)

	case StagePlanned, StageRebasing:
		// Planned or mid-rebase without conflict: re-execute from current
		if gitRebaseInProgress(opts.RepoDir) {
			// Treat as conflict
			tx.Stage = StageConflict
			tx.FailureKind = FailConflict
			if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
				return err
			}
			return fmt.Errorf("rebase in progress; resolve conflicts then --continue")
		}
		return executeTransaction(opts, tx)

	case StageCompleted:
		// Already done
		return finalizeCleanup(opts, tx)

	default:
		return fmt.Errorf("unknown stage %q", tx.Stage)
	}
}

func resumeFromSwitched(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	// Still need to rebase the current entry, then continue
	if err := doRebase(opts, tx); err != nil {
		return err
	}
	return resumeFromRebased(opts, tx)
}

func resumeFromRebased(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	// Verify ancestry
	entry := tx.Plan[tx.CurrentIndex]
	ok, err := gitIsAncestor(opts.RepoDir, entry.NewBaseSHA, entry.Branch)
	if err != nil {
		return err
	}
	if !ok {
		tx.FailureKind = FailAncestry
		tx.FailureMsg = fmt.Sprintf("%s not descended from %s after rebase", entry.Branch, entry.Base)
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return errors.New(tx.FailureMsg)
	}

	// Run validation if test command
	if opts.TestCommand != "" {
		tx.Stage = StageValidating
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		if StepHook != nil {
			if err := StepHook(StageValidating, tx.CurrentIndex); err != nil {
				tx.FailureKind = FailInterruption
				_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
				return err
			}
		}
		if err := runValidation(opts); err != nil {
			tx.FailureKind = FailValidation
			tx.FailureMsg = err.Error()
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return fmt.Errorf("validation failed for %s: %w", entry.Branch, err)
		}
	}

	// Mark this entry complete
	tx.CompletedIndices = append(tx.CompletedIndices, tx.CurrentIndex)
	tx.CurrentIndex++
	tx.Stage = StagePlanned
	tx.FailureKind = FailNone
	tx.FailureMsg = ""
	if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
		return err
	}

	// Continue remaining
	return executeTransaction(opts, tx)
}

func resumeFromValidating(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	// Re-run validation
	if opts.TestCommand != "" {
		if err := runValidation(opts); err != nil {
			tx.FailureKind = FailValidation
			tx.FailureMsg = err.Error()
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return fmt.Errorf("validation still failing for %s: %w", tx.Plan[tx.CurrentIndex].Branch, err)
		}
	}
	// Mark complete and continue
	tx.CompletedIndices = append(tx.CompletedIndices, tx.CurrentIndex)
	tx.CurrentIndex++
	tx.Stage = StagePlanned
	tx.FailureKind = FailNone
	tx.FailureMsg = ""
	if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
		return err
	}
	return executeTransaction(opts, tx)
}

func resumeFromRestoring(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	if err := restoreOriginal(opts, tx); err != nil {
		tx.FailureKind = FailRestoration
		tx.FailureMsg = err.Error()
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return fmt.Errorf("restoration retry failed: %w", err)
	}
	return finalizeCleanup(opts, tx)
}

// ---------- Single branch processing ----------

func processBranch(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	entry := &tx.Plan[tx.CurrentIndex]

	// Re-resolve base SHA (it may have changed if base was rebased earlier in this transaction)
	newBaseSHA, err := gitResolveRef(opts.RepoDir, entry.Base)
	if err != nil {
		return fmt.Errorf("re-resolve base %q: %w", entry.Base, err)
	}
	entry.NewBaseSHA = newBaseSHA

	// StepHook: planned
	if StepHook != nil {
		if err := StepHook(StagePlanned, tx.CurrentIndex); err != nil {
			tx.FailureKind = FailInterruption
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return err
		}
	}

	// Switch to branch
	tx.Stage = StageSwitched
	if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
		return err
	}
	if err := gitCheckout(opts.RepoDir, entry.Branch); err != nil {
		tx.FailureKind = FailSwitch
		tx.FailureMsg = err.Error()
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return err
	}

	if StepHook != nil {
		if err := StepHook(StageSwitched, tx.CurrentIndex); err != nil {
			tx.FailureKind = FailInterruption
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return err
		}
	}

	// Rebase
	if err := doRebase(opts, tx); err != nil {
		return err
	}

	// Verify ancestry
	ok, err := gitIsAncestor(opts.RepoDir, entry.NewBaseSHA, entry.Branch)
	if err != nil {
		return err
	}
	if !ok {
		tx.FailureKind = FailAncestry
		tx.FailureMsg = fmt.Sprintf("%s not descended from %s", entry.Branch, entry.Base)
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return errors.New(tx.FailureMsg)
	}

	// Validation
	if opts.TestCommand != "" {
		tx.Stage = StageValidating
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		if StepHook != nil {
			if err := StepHook(StageValidating, tx.CurrentIndex); err != nil {
				tx.FailureKind = FailInterruption
				_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
				return err
			}
		}
		if err := runValidation(opts); err != nil {
			tx.FailureKind = FailValidation
			tx.FailureMsg = err.Error()
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return fmt.Errorf("validation failed for %s: %w", entry.Branch, err)
		}
	}

	return nil
}

func doRebase(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	entry := &tx.Plan[tx.CurrentIndex]

	tx.Stage = StageRebasing
	if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
		return err
	}

	if StepHook != nil {
		if err := StepHook(StageRebasing, tx.CurrentIndex); err != nil {
			tx.FailureKind = FailInterruption
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return err
		}
	}

	// Amend-aware: if LastBaseSHA differs from NewBaseSHA, use --onto
	var rebaseErr error
	if entry.LastBaseSHA != "" && entry.LastBaseSHA != entry.NewBaseSHA {
		rebaseErr = gitRebaseOnto(opts.RepoDir, entry.NewBaseSHA, entry.LastBaseSHA)
	} else {
		rebaseErr = gitPlainRebase(opts.RepoDir, entry.Base)
	}

	if rebaseErr != nil {
		var conflictErr *RebaseConflictError
		if errors.As(rebaseErr, &conflictErr) {
			tx.Stage = StageConflict
			tx.FailureKind = FailConflict
			tx.FailureMsg = conflictErr.Error()
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return fmt.Errorf("conflict rebasing %s: resolve then --continue", entry.Branch)
		}
		tx.FailureKind = FailSwitch
		tx.FailureMsg = rebaseErr.Error()
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return rebaseErr
	}

	// Record post-SHA
	postSHA, err := gitResolveRef(opts.RepoDir, "HEAD")
	if err != nil {
		return err
	}
	entry.PostSHA = postSHA
	tx.Stage = StageRebased
	_ = SaveCheckoutTransaction(opts.FeaturePath, tx)

	if StepHook != nil {
		if err := StepHook(StageRebased, tx.CurrentIndex); err != nil {
			tx.FailureKind = FailInterruption
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return err
		}
	}

	return nil
}

// ---------- Finalization ----------

func finalizeTransaction(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	// Update stack.yaml LastBaseSHA atomically
	stack, err := LoadStack(opts.FeaturePath)
	if err != nil {
		return fmt.Errorf("reload stack for LastBaseSHA update: %w", err)
	}
	for _, pe := range tx.Plan {
		for i := range stack.Branches {
			// Attribute by logical Name when the plan carries it (C3); an old
			// transaction with no name falls back to the first GitBranch match.
			if pe.Name != "" {
				if stack.Branches[i].Name == pe.Name {
					stack.Branches[i].LastBaseSHA = pe.NewBaseSHA
					break
				}
				continue
			}
			if stack.Branches[i].GitBranch() == pe.Branch {
				stack.Branches[i].LastBaseSHA = pe.NewBaseSHA
				break
			}
		}
	}
	if err := SaveStack(opts.FeaturePath, stack); err != nil {
		tx.FailureKind = FailPersistence
		tx.FailureMsg = "failed to update stack.yaml: " + err.Error()
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return fmt.Errorf("update stack LastBaseSHA: %w", err)
	}

	// Verify final ancestry for all entries
	for _, pe := range tx.Plan {
		ok, err := gitIsAncestor(opts.RepoDir, pe.NewBaseSHA, pe.Branch)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("final ancestry check failed: %s not descendant of %s", pe.Branch, pe.Base)
		}
	}

	// Restore original branch
	tx.Stage = StageRestoring
	_ = SaveCheckoutTransaction(opts.FeaturePath, tx)

	if StepHook != nil {
		if err := StepHook(StageRestoring, tx.CurrentIndex); err != nil {
			tx.FailureKind = FailInterruption
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return err
		}
	}

	if err := restoreOriginal(opts, tx); err != nil {
		tx.FailureKind = FailRestoration
		tx.FailureMsg = err.Error()
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return fmt.Errorf("restoration failed: %w; use --continue to retry", err)
	}

	// Push if requested
	if opts.Push {
		for _, pe := range tx.Plan {
			if err := gitPush(opts.RepoDir, pe.Branch); err != nil {
				tx.FailureKind = FailPersistence
				tx.FailureMsg = "push failed: " + err.Error()
				tx.Stage = StageCompleted // branches are done, just push failed
				_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
				return fmt.Errorf("push %s: %w; re-run --continue to retry push", pe.Branch, err)
			}
		}
	}

	return finalizeCleanup(opts, tx)
}

func finalizeCleanup(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	// If push still pending on completed stage
	if tx.Stage == StageCompleted && opts.Push {
		for _, pe := range tx.Plan {
			if err := gitPush(opts.RepoDir, pe.Branch); err != nil {
				tx.FailureMsg = "push retry failed: " + err.Error()
				_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
				return fmt.Errorf("push %s: %w", pe.Branch, err)
			}
		}
	}

	tx.Stage = StageCompleted
	DeleteCheckoutTransaction(opts.FeaturePath)
	ReleaseCheckoutLock(opts.FeaturePath)
	return nil
}

// ---------- Restoration ----------

func restoreOriginal(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	// Check if original branch was in the plan (legitimately rebased)
	var inPlan bool
	for _, pe := range tx.Plan {
		if pe.Branch == tx.OriginalBranch {
			inPlan = true
			break
		}
	}

	if err := gitCheckout(opts.RepoDir, tx.OriginalBranch); err != nil {
		return fmt.Errorf("restore checkout %s: %w", tx.OriginalBranch, err)
	}

	if !inPlan {
		// Verify HEAD matches original (no sync should have changed it)
		currentHEAD, err := gitResolveRef(opts.RepoDir, "HEAD")
		if err != nil {
			return err
		}
		if currentHEAD != tx.OriginalHEAD {
			return fmt.Errorf("original branch %s HEAD changed during transaction (was %s, now %s); manual recovery needed",
				tx.OriginalBranch, shortCheckoutSHA(tx.OriginalHEAD), shortCheckoutSHA(currentHEAD))
		}
	}
	// If in plan: branch was legitimately rebased, restore without resetting

	return nil
}

func shortCheckoutSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// ---------- Validation ----------

func runValidation(opts CheckoutSyncOpts) error {
	if opts.TestCommand == "" {
		return nil
	}
	cmd := exec.Command("sh", "-c", opts.TestCommand)
	cmd.Dir = opts.RepoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", opts.TestCommand, string(out))
	}
	return nil
}

// ---------- Helpers ----------

func pidStr(pid int) string {
	return strconv.Itoa(pid)
}

var _ = pidStr // suppress unused warning

// TestIsAncestor is exported for testing.
func TestIsAncestor(repoDir, ancestor, descendant string) (bool, error) {
	return gitIsAncestor(repoDir, ancestor, descendant)
}

// MarshalLockInfo marshals lock info for testing.
func MarshalLockInfo(info *LockInfo) ([]byte, error) {
	return yaml.Marshal(info)
}
