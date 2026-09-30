package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	StagePublishing CheckoutStage = "publishing"
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

	// Guarded envelope extension (§13.1, §13.6). Absent on every unguarded
	// transaction, so an unguarded run's on-disk bytes are unchanged.
	MaxReplayPerEntry *int   `yaml:"max_replay_per_entry,omitempty"`
	MaxReplayTotal    *int   `yaml:"max_replay_total,omitempty"`
	Route             string `yaml:"route,omitempty"`
	PlanGuarded       bool   `yaml:"plan_guarded,omitempty"`

	Transaction *SyncTransaction `yaml:"transaction,omitempty"`
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
	data, err := readSyncStateFile(CheckoutTransactionPath(featurePath))
	if err != nil {
		return nil, err
	}
	root, err := syncEnvelopeRoot(data)
	if err != nil {
		return nil, err
	}
	version, err := syncEnvelopeVersion(root)
	if err != nil {
		return nil, err
	}
	switch version {
	case 0, 1, CheckoutTransactionVersion, CheckoutTransactionGuardedVersion:
		if err := rejectLegacyTransactionalFields(root); err != nil {
			return nil, err
		}
		var tx CheckoutTransaction
		if err := yaml.Unmarshal(data, &tx); err != nil {
			return nil, err
		}
		if tx.Transaction != nil || tx.PlanGuarded {
			return nil, errors.New("legacy checkout sync state carries transactional evidence")
		}
		return &tx, nil
	case CheckoutTransactionTransactionalVersion, CheckoutTransactionTransactionalGuardedVersion:
		var tx CheckoutTransaction
		if err := decodeStrictSyncEnvelope(data, &tx); err != nil {
			return nil, err
		}
		if err := validateCheckoutTransactionEnvelope(featurePath, &tx); err != nil {
			return nil, fmt.Errorf("invalid transactional checkout sync state: %w", err)
		}
		return &tx, nil
	default:
		return nil, fmt.Errorf("unsupported checkout sync transaction state version %d", version)
	}
}

func SaveCheckoutTransaction(featurePath string, tx *CheckoutTransaction) error {
	if err := syncIOFault(SyncIOWriteTransaction, CheckoutTransactionPath(featurePath)); err != nil {
		return err
	}
	if tx == nil {
		return errors.New("checkout transaction is nil")
	}
	switch tx.StateVersion {
	case 0, 1, CheckoutTransactionVersion, CheckoutTransactionGuardedVersion:
		if tx.Transaction != nil || tx.PlanGuarded {
			return errors.New("legacy checkout sync state carries transactional evidence")
		}
	case CheckoutTransactionTransactionalVersion, CheckoutTransactionTransactionalGuardedVersion:
		if err := validateCheckoutTransactionEnvelope(featurePath, tx); err != nil {
			return fmt.Errorf("invalid transactional checkout sync state: %w", err)
		}
		tx.Transaction.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	default:
		return fmt.Errorf("unsupported checkout sync transaction state version %d", tx.StateVersion)
	}
	data, err := yaml.Marshal(tx)
	if err != nil {
		return err
	}
	return durableWriteFile(CheckoutTransactionPath(featurePath), data, 0600)
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

func RemoveCheckoutTransaction(featurePath string) error {
	path := CheckoutTransactionPath(featurePath)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove checkout sync transaction %s: %w", path, err)
	}
	return nil
}

func DeleteCheckoutTransaction(featurePath string) {
	_ = RemoveCheckoutTransaction(featurePath)
}

func HasCheckoutTransaction(featurePath string) bool {
	_, err := os.Lstat(CheckoutTransactionPath(featurePath))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func validateCheckoutStackRepos(featurePath string) error {
	stack, err := LoadStack(featurePath)
	if err != nil {
		return nil // preserve the route's existing load/error ordering
	}
	for _, entry := range stack.Branches {
		if strings.TrimSpace(entry.Repo) != "" {
			return fmt.Errorf("stack entry %q belongs to repository %q; checkout sync is single-repository (cross-repo-unsupported)",
				entry.Name, entry.Repo)
		}
	}
	return nil
}

// ---------- Route/guard derivation (§13.6) ----------

// txNewMode answers "is this persisted transaction a new-mode run?" nil-safe.
// A nil transaction is never new-mode, an explicit route decides outright,
// and an absent or unknown route falls back to the version — so for every
// transaction a shipped binary could have written, this reproduces the
// shipped tx.StateVersion >= CheckoutTransactionVersion comparison exactly
// (§13.6 rule 4).
func txNewMode(tx *CheckoutTransaction) bool {
	if tx == nil {
		return false
	}
	switch tx.Route {
	case RouteNewMode:
		return true
	case RouteLegacy:
		return false
	default:
		return tx.StateVersion >= CheckoutTransactionVersion
	}
}

// TransactionNewMode is the exported wrapper package cli calls. Both are
// nil-safe by construction, so no caller needs a nil check of its own.
func TransactionNewMode(tx *CheckoutTransaction) bool { return txNewMode(tx) }

// checkoutRecoveryIsNewMode answers "is this persisted transaction a new-mode
// run?" for both recovery verbs (--continue and --abort). It is exactly
// txNewMode under a name that says why it is being asked.
func checkoutRecoveryIsNewMode(tx *CheckoutTransaction) bool { return txNewMode(tx) }

// checkoutRecoveryIsGuarded answers "was this persisted transaction born
// guarded?". It is nil-safe by construction: an absent transaction is never
// guarded. It is deliberately NOT routed through txNewMode: route decides
// legacy-vs-new-mode semantics, version decides whether a guard was armed at
// birth, and a route: legacy v3 transaction is guarded (§13.6 rule 4d).
func checkoutRecoveryIsGuarded(tx *CheckoutTransaction) bool {
	return tx != nil && (tx.StateVersion == CheckoutTransactionGuardedVersion || tx.StateVersion == CheckoutTransactionTransactionalGuardedVersion || tx.PlanGuarded)
}

// TransactionGuarded is the exported wrapper package cli's dispatch calls.
func TransactionGuarded(tx *CheckoutTransaction) bool { return checkoutRecoveryIsGuarded(tx) }

// CheckoutTriggersNeedV2 keeps all three shipped arms: an unreadable or
// absent transaction refuses exactly as it does today, and the version
// comparison is replaced by — and only by — the route derivation. It takes
// the transaction the --continue arm already loaded, so the arm performs
// exactly one LoadCheckoutTransaction rather than one per predicate (§13.6
// rule 4a).
func CheckoutTriggersNeedV2(tx *CheckoutTransaction, loadErr error) bool {
	return loadErr != nil || tx == nil || !TransactionNewMode(tx)
}

// upgradeGuardedCheckoutTransaction is the §13.2a checkout upgrade: called
// from a guarded ContinueCheckoutSync arm immediately below the guard seam
// and above resumeTransaction. It sets StateVersion, the INHERITED Route
// (txNewMode(tx) on the value as loaded) and the two limit pointers on the
// in-memory transaction, leaves TestCommand and every other member untouched,
// calls the shipped SaveCheckoutTransaction once, and is a no-op returning
// nil when tx is nil or already guarded (including an already-v3
// transaction) — it is the only site that re-versions an existing
// transaction.
//
// Signature note: spec.md §13.2a's own draft gives this a third parameter
// `limits PlanGuardLimits`. PlanGuardLimits is one of the symbols explicitly
// reserved for the later agent's internal/rebase_plan_guard.go and is not yet
// declared anywhere in this tree, so this function takes the two limit
// pointers directly instead. Once PlanGuardLimits exists, its call site can
// pass its two members positionally, or this signature can be widened.
func upgradeGuardedCheckoutTransaction(featurePath string, tx *CheckoutTransaction, maxPerEntry, maxTotal *int) error {
	if tx == nil || checkoutRecoveryIsGuarded(tx) {
		return nil
	}
	inheritedNewMode := txNewMode(tx)
	if tx.Transaction != nil {
		tx.StateVersion = CheckoutTransactionTransactionalGuardedVersion
		tx.PlanGuarded = true
	} else {
		tx.StateVersion = CheckoutTransactionGuardedVersion
	}
	if inheritedNewMode {
		tx.Route = RouteNewMode
	} else {
		tx.Route = RouteLegacy
	}
	if maxPerEntry != nil {
		tx.MaxReplayPerEntry = maxPerEntry
	}
	if maxTotal != nil {
		tx.MaxReplayTotal = maxTotal
	}
	if err := SaveCheckoutTransaction(featurePath, tx); err != nil {
		return fmt.Errorf("persist transaction: %w", err)
	}
	return nil
}

// ---------- Lock management ----------

type LockInfo struct {
	PID     int    `yaml:"pid"`
	Created string `yaml:"created"`
}

// CheckoutMutationLock serializes every checkout-mode mutation across the
// whole workspace. Feature-specific sync locks and compatibility artifacts
// remain in place for downgrade/recovery; this lock protects the one physical
// checkout they all share.
type CheckoutMutationLock struct {
	PID       int    `yaml:"pid"`
	Created   string `yaml:"created"`
	Token     string `yaml:"token"`
	Feature   string `yaml:"feature"`
	Operation string `yaml:"operation"` // sync | reparent
	StatePath string `yaml:"state_path"`
}

func CheckoutMutationLockPath(stateDir string) string {
	return filepath.Join(stateDir, "checkout-mutation.lock")
}

func checkoutSyncMutationToken(feature, startedAt, originalHEAD string) string {
	sum := sha256.Sum256([]byte("tws-checkout-sync-mutation\x00" + feature + "\x00" + startedAt + "\x00" + originalHEAD))
	return hex.EncodeToString(sum[:16])
}

func writeCheckoutMutationLockExclusive(path, token, feature, operation, statePath string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	info := CheckoutMutationLock{
		PID: os.Getpid(), Created: time.Now().UTC().Format(time.RFC3339),
		Token: token, Feature: feature, Operation: operation, StatePath: statePath,
	}
	data, marshalErr := yaml.Marshal(&info)
	if marshalErr == nil {
		_, marshalErr = file.Write(data)
	}
	if marshalErr == nil {
		marshalErr = file.Sync()
	}
	closeErr := file.Close()
	if marshalErr != nil {
		_ = os.Remove(path)
		return marshalErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	return syncDir(filepath.Dir(path))
}

func ReadCheckoutMutationLock(stateDir string) (*CheckoutMutationLock, []byte, error) {
	path := CheckoutMutationLockPath(stateDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var info CheckoutMutationLock
	if err := yaml.Unmarshal(data, &info); err != nil {
		return nil, data, err
	}
	if info.PID <= 0 || info.Token == "" || info.Feature == "" || info.StatePath == "" ||
		(info.Operation != "sync" && info.Operation != "reparent") {
		return nil, data, fmt.Errorf("invalid checkout mutation lock")
	}
	return &info, data, nil
}

var checkoutMutationLstat = os.Lstat

func checkoutMutationOwnerStateExists(stateDir string, info *CheckoutMutationLock) (bool, error) {
	if info == nil {
		return false, nil
	}
	var path string
	if info.StatePath != "" {
		path = info.StatePath
	} else {
		switch info.Operation {
		case "sync":
			path = filepath.Join(stateDir, info.Feature+"-checkout-sync.yaml")
		case "reparent":
			path = filepath.Join(stateDir, info.Feature+"-reparent.v1.yaml")
		}
	}
	fileInfo, err := checkoutMutationLstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("owner state path %s is a symbolic link", path)
	}
	return true, nil
}

func checkoutWorkspaceRecoverableStates(stateDirs []string) ([]string, error) {
	var paths []string
	seen := make(map[string]bool, len(stateDirs))
	for _, stateDir := range stateDirs {
		stateDir = filepath.Clean(stateDir)
		if stateDir == "" || seen[stateDir] {
			continue
		}
		seen[stateDir] = true
		entries, err := os.ReadDir(stateDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, "-checkout-sync.yaml") &&
				!strings.HasSuffix(name, "-reparent.v1.yaml") {
				continue
			}
			path := filepath.Join(stateDir, name)
			info, err := checkoutMutationLstat(path)
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("recoverable checkout mutation state %s is not a regular file", path)
			}
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func checkoutMutationStateDirs(stateDir, ownerStatePath string, additional []string) []string {
	stateDir = filepath.Clean(stateDir)
	dirs := []string{stateDir}
	parent := filepath.Dir(stateDir)
	if filepath.Base(parent) == ".tws" {
		dirs = append(dirs, filepath.Join(filepath.Dir(parent), "state"))
	} else {
		dirs = append(dirs, filepath.Join(parent, ".tws", "state"))
	}
	if ownerStatePath != "" {
		dirs = append(dirs, filepath.Dir(ownerStatePath))
	}
	return append(dirs, additional...)
}

func verifyCheckoutMutationReservation(stateDir, ownerStatePath string, additionalStateDirs, allowedStatePaths []string) error {
	paths, err := checkoutWorkspaceRecoverableStates(
		checkoutMutationStateDirs(stateDir, ownerStatePath, additionalStateDirs),
	)
	if err != nil {
		return fmt.Errorf("inspect workspace checkout mutation state: %w", err)
	}
	allowed := make(map[string]bool, len(allowedStatePaths))
	for _, path := range allowedStatePaths {
		if path != "" {
			allowed[filepath.Clean(path)] = true
		}
	}
	for _, path := range paths {
		if allowed[filepath.Clean(path)] {
			continue
		}
		return fmt.Errorf("workspace has recoverable checkout mutation state at %s; recover it before starting another checkout mutation", path)
	}
	return nil
}

func checkoutMutationRecoveryStatePaths(stateDir, feature, operation, ownerStatePath string, additionalStateDirs []string) []string {
	paths := []string{ownerStatePath}
	if operation == "reparent" {
		for _, dir := range checkoutMutationStateDirs(stateDir, ownerStatePath, additionalStateDirs) {
			paths = append(paths, filepath.Join(dir, feature+"-checkout-sync.yaml"))
		}
	}
	return paths
}

func releaseCheckoutMutationReservation(stateDir, token string, cause error) error {
	if releaseErr := ReleaseCheckoutMutationLock(stateDir, token); releaseErr != nil {
		return errors.Join(cause, releaseErr)
	}
	return cause
}

// AcquireCheckoutMutationLock claims a fresh workspace-global mutation lock.
// A dead orphan with no corresponding state is reclaimed; live, unreadable,
// or state-backed locks are foreign and fail closed.
func AcquireCheckoutMutationLock(stateDir, token, feature, operation, ownerStatePath string, additionalStateDirs ...string) ([]byte, error) {
	path := CheckoutMutationLockPath(stateDir)
	if err := writeCheckoutMutationLockExclusive(path, token, feature, operation, ownerStatePath); err == nil {
		if err := verifyCheckoutMutationReservation(stateDir, ownerStatePath, additionalStateDirs, nil); err != nil {
			return nil, releaseCheckoutMutationReservation(stateDir, token, err)
		}
		return os.ReadFile(path)
	} else if !os.IsExist(err) {
		return nil, err
	}
	info, data, err := ReadCheckoutMutationLock(stateDir)
	if err != nil {
		return nil, fmt.Errorf("invalid checkout mutation lock at %s: %w", path, err)
	}
	if isProcessAlive(info.PID) {
		return nil, fmt.Errorf("checkout mutation lock held by live %s for feature %q (pid %d, created %s)",
			info.Operation, info.Feature, info.PID, info.Created)
	}
	stateExists, stateErr := checkoutMutationOwnerStateExists(stateDir, info)
	if stateErr != nil {
		return nil, fmt.Errorf("inspect checkout mutation owner state %s: %w", info.StatePath, stateErr)
	}
	if stateExists {
		return nil, fmt.Errorf("stale checkout mutation lock belongs to recoverable %s for feature %q", info.Operation, info.Feature)
	}
	if err := removeLockIfUnchanged(path, data); err != nil {
		return nil, fmt.Errorf("reclaim stale checkout mutation lock: %w", err)
	}
	if err := writeCheckoutMutationLockExclusive(path, token, feature, operation, ownerStatePath); err != nil {
		return nil, err
	}
	if err := verifyCheckoutMutationReservation(stateDir, ownerStatePath, additionalStateDirs, nil); err != nil {
		return nil, releaseCheckoutMutationReservation(stateDir, token, err)
	}
	return os.ReadFile(path)
}

// ReclaimCheckoutMutationLock is recovery's token-bound form. It never steals
// a mismatched token, even from a dead process.
func ReclaimCheckoutMutationLock(stateDir, token, feature, operation, ownerStatePath string, additionalStateDirs ...string) ([]byte, error) {
	path := CheckoutMutationLockPath(stateDir)
	info, data, err := ReadCheckoutMutationLock(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		if err := writeCheckoutMutationLockExclusive(path, token, feature, operation, ownerStatePath); err != nil {
			return nil, err
		}
		if err := verifyCheckoutMutationReservation(
			stateDir, ownerStatePath, additionalStateDirs,
			checkoutMutationRecoveryStatePaths(stateDir, feature, operation, ownerStatePath, additionalStateDirs),
		); err != nil {
			return nil, releaseCheckoutMutationReservation(stateDir, token, err)
		}
		return os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid checkout mutation lock at %s: %w", path, err)
	}
	if info.Token != token || info.Feature != feature || info.Operation != operation || info.StatePath != ownerStatePath {
		return nil, fmt.Errorf("checkout mutation lock belongs to foreign %s for feature %q", info.Operation, info.Feature)
	}
	if info.PID != os.Getpid() && isProcessAlive(info.PID) {
		return nil, fmt.Errorf("checkout mutation lock held by live %s for feature %q (pid %d)", info.Operation, info.Feature, info.PID)
	}
	if err := removeLockIfUnchanged(path, data); err != nil {
		return nil, fmt.Errorf("reclaim checkout mutation lock: %w", err)
	}
	if err := writeCheckoutMutationLockExclusive(path, token, feature, operation, ownerStatePath); err != nil {
		return nil, err
	}
	if err := verifyCheckoutMutationReservation(
		stateDir, ownerStatePath, additionalStateDirs,
		checkoutMutationRecoveryStatePaths(stateDir, feature, operation, ownerStatePath, additionalStateDirs),
	); err != nil {
		return nil, releaseCheckoutMutationReservation(stateDir, token, err)
	}
	return os.ReadFile(path)
}

func ReleaseCheckoutMutationLock(stateDir, token string) error {
	path := CheckoutMutationLockPath(stateDir)
	info, data, err := ReadCheckoutMutationLock(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Token != token {
		return fmt.Errorf("checkout mutation lock ownership changed")
	}
	return removeLockIfUnchanged(path, data)
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

// CheckoutLockLiveError is the typed form of forceAcquireCheckoutLock's
// live-foreign-holder refusal. Its message is byte-identical to the sentence
// that arm has always produced, so every existing caller and every existing
// assertion sees exactly the same bytes; the type exists only so a caller
// that must classify THAT one failure — and not "invalid lock", "unreadable
// lock" or "mkdir failed" — can do so with errors.As instead of string
// matching.
type CheckoutLockLiveError struct {
	PID int
}

func (e *CheckoutLockLiveError) Error() string {
	return fmt.Sprintf("lock held by live process %d; cannot reclaim", e.PID)
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
		return &CheckoutLockLiveError{PID: info.PID}
	}
	if err := removeLockIfUnchanged(lockPath, data); err != nil {
		return fmt.Errorf("reclaim checkout-sync lock: %w", err)
	}
	return writeLockExclusive(lockPath)
}

// ReclaimCheckoutLock takes the checkout lock only when it is absent, already
// ours, or held by a dead PID. It never steals a live foreign lock.
//
// It is the exported form of forceAcquireCheckoutLock and reuses that single
// ladder outright — the same LockInfo, the same PID/liveness test, the same
// compare-bytes-before-remove, the same O_EXCL write. It deliberately does NOT
// pre-read the lock itself: a second read before the shared ladder's own read
// would be a race, and a second ladder would be a second safety model.
// A live foreign holder returns *CheckoutLockLiveError, which the reparent
// recovery routes surface as sync-state-present.
func ReclaimCheckoutLock(featurePath string) error {
	return forceAcquireCheckoutLock(featurePath)
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
	return err == nil || errors.Is(err, syscall.EPERM)
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
	cmd := exec.Command("git", SyncNoUpdateRefsRebaseArgs("--no-fork-point", "--onto", newBase, oldBase)...)
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
	cmd := exec.Command("git", SyncNoUpdateRefsRebaseArgs("--no-fork-point", base)...)
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

// gitPush pushes one branch. The reparentPushDecision parameter carries this
// entry's already-made rule R-PUSH decision (§12.4); its ZERO VALUE reproduces
// today's argv, output and exit status byte-for-byte, which is what keeps
// every push golden frozen on a feature with no pending remote record.
func gitPush(repoDir, branch string, d ReparentPushDecision, prose io.Writer) error {
	args := []string{"push", "--force-with-lease"}
	if d.ForceIfIncludes {
		args = append(args, "--force-if-includes")
	}
	args = append(args, "origin", branch)
	if d.Applies && d.WarnLine != "" && prose != nil {
		fmt.Fprintln(prose, d.WarnLine) //nolint:errcheck
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("push %s: %s: %w", branch, string(out), err)
	}
	return nil
}

func gitPushIntent(repoDir string, intent SyncTransactionPushIntent, d ReparentPushDecision, prose io.Writer) error {
	args := []string{"push", "--force-with-lease"}
	if d.ForceIfIncludes {
		args = append(args, "--force-if-includes")
	}
	args = append(args, "origin", intent.SourceSHA+":"+intent.DestinationRef)
	if d.Applies && d.WarnLine != "" && prose != nil {
		fmt.Fprintln(prose, d.WarnLine) //nolint:errcheck
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("push %s: %s: %w", intent.DestinationRef, string(out), err)
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

// buildCheckoutPlanFrom is the order-taking body of BuildCheckoutPlan. It
// NEVER sorts: order is the one TopoSort result of this invocation, and it is
// exactly what the shipped body iterated over.
//
// D1 fix (§9.2): any in-stack parent — any entry.Base for which
// GetBranch(stack, entry.Base) returns a named entry — now resolves through
// parent.GitBranch() on EVERY scope and EVERY propagation arm, not only the
// scoped local-only propagated arm the shipped body special-cased. A literal
// (non-stack-entry) base stays literal and keeps its gitResolveRef behaviour.
// StackEntry.Repo is still not consulted: no sameStackRepo conjunct is added,
// GetBranch matches on logical Name alone, exactly as the shipped arm did —
// checkout's one-physical-checkout posture is preserved verbatim.
func buildCheckoutPlanFrom(repoDir string, stack Stack, order []StackEntry, sel SyncSelection) ([]CheckoutPlanEntry, error) {
	scoped := sel.Names != nil
	localOnly := sel.Policy.Propagation == SyncPropagationLocalOnly

	var plan []CheckoutPlanEntry
	for _, entry := range order {
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
		base, ok := checkoutBaseTokenFor(stack, entry)
		if !ok {
			continue // root base uses current default; skip
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

// checkoutBaseTokenFor is the checkout executor's base-token selection,
// extracted verbatim from buildCheckoutPlanFrom so the shipped executor and
// safe-reparent's resolver-agreement adapter read the identical rule from one
// place rather than two that can drift apart silently.
//
// The rule, unchanged: an empty configured base is not a base at all (ok is
// false, reproducing the caller's `continue`); any in-stack parent — matched
// on logical Name alone, with StackEntry.Repo deliberately not consulted, as
// the shipped arm did — resolves through parent.GitBranch(); anything else
// stays the literal token. The caller keeps its own `branch := entry.GitBranch()`
// assignment and its own gitResolveRef call: neither moved.
func checkoutBaseTokenFor(stack Stack, entry StackEntry) (base string, ok bool) {
	base = entry.Base
	if base == "" {
		return "", false
	}
	if parent := GetBranch(stack, entry.Base); parent.Name != "" {
		base = parent.GitBranch()
	}
	return base, true
}

// BuildCheckoutPlan creates the rebase plan from the stack, resolving SHAs.
//
// A zero selection means "the whole stack" — the frozen no-flag path. With a
// selection it plans only the selected entries, preserving TopoSort order and
// every existing skip rule, and under local-only it excludes anchors entirely.
//
// BuildCheckoutPlan keeps its shipped signature, its shipped error and its
// shipped position: it is the sort's owner on the unguarded executing route.
func BuildCheckoutPlan(repoDir string, stack Stack, sel SyncSelection) ([]CheckoutPlanEntry, error) {
	sorted, err := TopoSort(stack)
	if err != nil {
		return nil, err
	}
	return buildCheckoutPlanFrom(repoDir, stack, sorted, sel)
}

// printSyncModeHeaderTo prints the sync-modes header to w. Its bytes are
// identical to package cli's printSyncModeHeader, which serves the external
// half.
func printSyncModeHeaderTo(w io.Writer, p SyncRunPolicy) {
	fmt.Fprintf(w, "Sync mode: fetch=%s propagation=%s scope=%s\n", p.Fetch, p.Propagation, p.ScopeLabel()) //nolint:errcheck
}

// printSyncModeHeader prints the sync-modes header to stdout.
func printSyncModeHeader(p SyncRunPolicy) {
	printSyncModeHeaderTo(os.Stdout, p)
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

	// PlanGuard carries the plan-guard control flags. It is a separate field,
	// never merged into Changed.
	PlanGuard CheckoutPlanGuard

	// Reparent is the feature's reparent artifact location, resolved by the
	// command route (which owns the workspace) and carried here so the push
	// loops can consult the §12.3 remote follow-up record without this package
	// re-deriving workspace identity. Its ZERO VALUE (Feature == "") is "no
	// record is reachable", which keeps every shipped push byte-identical.
	Reparent ReparentLocation

	// ReparentProse is where rule R-PUSH's `reparent-remote:` warning lines
	// go. A nil writer sends them to os.Stderr, which is where every other
	// checkout-sync diagnostic already goes.
	ReparentProse io.Writer

	// guard is the guarded execution route's own JIT revalidation carrier,
	// set by RunCheckoutSync/ContinueCheckoutSync immediately before they
	// hand off to executeTransaction/resumeTransaction. It is nil on every
	// unguarded run: every JIT seam is itself gated on guard != nil, so a
	// nil guard leaves every shipped byte and process untouched.
	guard *checkoutPlanGuardRun

	mutationLockToken string
}

func checkoutMutationStateDir(opts CheckoutSyncOpts) string {
	if opts.Reparent.CheckoutStateDir != "" {
		return opts.Reparent.CheckoutStateDir
	}
	return checkoutStateDir(opts.FeaturePath)
}

func releaseCheckoutSyncLocks(opts CheckoutSyncOpts) error {
	ReleaseCheckoutLock(opts.FeaturePath)
	if opts.mutationLockToken == "" {
		return nil
	}
	return ReleaseCheckoutMutationLock(checkoutMutationStateDir(opts), opts.mutationLockToken)
}

func checkoutSyncRecoveryMutationToken(opts CheckoutSyncOpts, tx *CheckoutTransaction) (string, error) {
	expected := checkoutSyncMutationToken(tx.Feature, tx.StartedAt, tx.OriginalHEAD)
	info, _, err := ReadCheckoutMutationLock(checkoutMutationStateDir(opts))
	if err == nil {
		wantState := CheckoutTransactionPath(opts.FeaturePath)
		if info.Token != expected || info.Feature != tx.Feature || info.Operation != "sync" || info.StatePath != wantState {
			return "", fmt.Errorf("checkout mutation lock belongs to foreign %s for feature %q", info.Operation, info.Feature)
		}
		return expected, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return expected, nil
}

// ---------------------------------------------------------------------------
// checkoutPlanGuardRun — the checkout executor's own JIT revalidation carrier
// (mirrors internal/cli/sync_plan_guard.go's planGuardRun for the checkout
// route, which cannot import package cli's copy since internal must never
// import internal/cli).
// ---------------------------------------------------------------------------

// checkoutPlanGuardRun is the guarded checkout executor's own JIT
// revalidation state: the approved plan's per-entry rows (keyed by name),
// the effective limits, the running replay total THIS invocation has
// accumulated, and whether state has already been preserved by an earlier
// invocation's own progress (or this invocation's own lock reclaim). A nil
// *checkoutPlanGuardRun is the unguarded path throughout RunCheckoutSync/
// ContinueCheckoutSync/processBranch/resumeFromSwitched: every JIT seam is
// itself gated on guard != nil, so a nil guard leaves every shipped byte and
// process untouched.
type checkoutPlanGuardRun struct {
	req            RebasePlanRequest
	approved       map[string]PlanEntry
	limits         PlanGuardLimits
	replayedTotal  int
	statePreserved bool
}

// newCheckoutPlanGuardRun builds the carrier from the already-built,
// already-evaluated plan. Its limits are the document's OWN
// plan.Guard.Limits — the same reconciled per-entry/total values
// RevalidatePlanGuardEntry must enforce against — never re-resolved a second
// time from opts. statePreserved starts true only for a --continue, which
// has already reclaimed the lock (and, for an armed resume, already
// persisted the guarded transaction) before this carrier is ever built; it
// starts false for a fresh run and flips true after this invocation's own
// first successful revalidate.
func newCheckoutPlanGuardRun(req RebasePlanRequest, plan RebasePlan, statePreserved bool) *checkoutPlanGuardRun {
	approved := make(map[string]PlanEntry, len(plan.Entries))
	for _, e := range plan.Entries {
		approved[e.Name] = e
	}
	limits := PlanGuardLimits{PerEntry: plan.Guard.Limits.MaxReplayPerEntry, Total: plan.Guard.Limits.MaxReplayTotal}
	return &checkoutPlanGuardRun{req: req, approved: approved, limits: limits, statePreserved: statePreserved}
}

// revalidate re-verifies one entry immediately before its rebase runs,
// through RevalidatePlanGuardEntry alone — never RevalidatePlanEntry
// directly. A name absent from the approved plan never blocks: there is
// nothing approved to compare against, so revalidation has nothing to say
// about it. A nil receiver is always a no-op, so every JIT call site can
// call this unconditionally. The running replay total accumulates the count
// the seam FRESHLY resolved, never the approved row's own recorded value:
// an `upstream-deferred` row is approved with a null count, so accumulating
// the approved value would add zero for exactly the rows §10.4's deferral
// policy exists to re-measure.
func (g *checkoutPlanGuardRun) revalidate(name string) error {
	if g == nil {
		return nil
	}
	approved, ok := g.approved[name]
	if !ok {
		return nil
	}
	res, err := RevalidatePlanGuardEntry(RevalidatePlanGuardEntryRequest{
		Request:        g.req,
		Approved:       approved,
		Limits:         g.limits,
		ReplayedTotal:  g.replayedTotal,
		StatePreserved: g.statePreserved,
	})
	if err != nil {
		return err
	}
	g.replayedTotal += res.CandidateCount
	g.statePreserved = true
	return nil
}

// fetchCheckoutRepoTo runs the checkout route's single fetch and writes its
// "Fetching ... done/failed" line to w, returning what it observed rather
// than a bare error, so a caller building a plan document can describe its
// own fetch.
func fetchCheckoutRepoTo(w io.Writer, ctx PlanFetchContext) PlanFetchOutcome {
	fmt.Fprintf(w, "Fetching %s... ", "default repo") //nolint:errcheck
	err := RunSilentDir(ctx.Root, "git", "fetch")
	ok := err == nil
	if ok {
		fmt.Fprintln(w, "done") //nolint:errcheck
	} else {
		fmt.Fprintln(w, "failed") //nolint:errcheck
	}
	candidates := ctx.Candidates
	if candidates == nil {
		candidates = []PlanFetchCandidate{}
	}
	return PlanFetchOutcome{
		Applies:   true,
		Attempted: true,
		Repos: []PlanFetchRepoResult{{
			RepoToken:         ctx.RepoToken,
			ContextRoot:       ctx.Root,
			ContextCommonDir:  ctx.CommonDir,
			ContextSource:     ctx.Source,
			ContextCandidates: candidates,
			Effect:            ctx.Effect,
			Attempted:         true,
			OK:                ok,
		}},
	}
}

// fetchCheckoutRepo is the shipped call site's discarding wrapper: it writes
// to os.Stdout and passes a zero PlanFetchContext beyond Root, exactly the
// fetch the shipped body always performed.
func fetchCheckoutRepo(repoDir string) PlanFetchOutcome {
	return fetchCheckoutRepoTo(os.Stdout, PlanFetchContext{Root: repoDir})
}

// RunCheckoutSync executes the full checkout-sync transaction.
func RunCheckoutSync(opts CheckoutSyncOpts) error {
	if opts.Policy.ScopeKind == "" {
		opts.Policy = SyncRunPolicy{Fetch: SyncFetchDisabled, Propagation: SyncPropagationFull, ScopeKind: SyncScopeAll}
	}
	if HasCheckoutTransaction(opts.FeaturePath) {
		return fmt.Errorf("checkout sync transaction already exists; use --continue or --abort")
	}
	if opts.NewMode {
		if err := validateCheckoutStackRepos(opts.FeaturePath); err != nil {
			return err
		}
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

	guarded := opts.PlanGuard.Guarded()

	// New-mode read-only preflight (I9, I10-I13, I14) — before the lock, so a
	// refusal leaves no lock, no transaction, and no write of any kind. A
	// guarded run takes its own, wider InspectCheckoutPlan-driven ladder
	// below instead, which also covers a guarded LEGACY run the same way:
	// a guarded checkout run and a --plan of the same invocation decide
	// state.* from one producer (§12.2d rule 5).
	var preloaded *Stack
	var sel SyncSelection
	if opts.NewMode && !guarded {
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

	// Guarded fresh route's own inspection + hand-check ladder (§12.2d fresh
	// arm), run above the lock so a refusal here leaves no lock, no
	// transaction, and no write of any kind. InspectCheckoutPlan is the same
	// producer PlanCheckoutRebase's --plan route already uses.
	var insp CheckoutPlanInspection
	if guarded {
		insp = InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})
		switch {
		case insp.StackErr != nil:
			if opts.NewMode {
				return fmt.Errorf("sync modes require a stack; feature %q has no readable stack.yaml", opts.Feature)
			}
			return fmt.Errorf("load stack: %w", insp.StackErr)
		case insp.SortErr != nil:
			if !opts.NewMode {
				return fmt.Errorf("build plan: %w", insp.SortErr)
			}
			return insp.SortErr
		case insp.SelectionErr != nil:
			return insp.SelectionErr
		case insp.BasePreflight.Failed:
			return errors.New(insp.BasePreflight.Detail)
		}
		sel = insp.Selection
	}

	// Resolve the full frozen selection for legacy no-flag runs too. Every
	// fresh ordinary sync now captures the same rollback evidence even though
	// only explicit sync-mode runs print the mode header.
	if !guarded && preloaded == nil {
		stack, loadErr := LoadStack(opts.FeaturePath)
		if loadErr != nil {
			return fmt.Errorf("load stack: %w", loadErr)
		}
		preloaded = &stack
	}
	var legacyOrder []StackEntry
	if sel.Names == nil {
		stack := insp.Stack
		if preloaded != nil {
			stack = *preloaded
		}
		legacyOrder, err = TopoSort(stack)
		if err != nil {
			return err
		}
		sel, err = ResolveSyncSelectionFromOrder(stack, legacyOrder, opts.Policy, SyncSelectionOpts{
			Mode: ModeCheckout, NewMode: opts.NewMode, Feature: opts.Feature,
		})
		if err != nil {
			return err
		}
	}
	if opts.Policy.Fetch == SyncFetchEnabled {
		if err := ValidateSyncFetchDoesNotWriteLocalBranches(opts.RepoDir); err != nil {
			return err
		}
	}

	// Workspace-global lock first, then the feature compatibility lock. Every
	// checkout mutator takes this order, so two features can never mutate the
	// one physical checkout concurrently and recovery cannot deadlock.
	startedAt := time.Now().UTC().Format(time.RFC3339)
	token := checkoutSyncMutationToken(opts.Feature, startedAt, originalHEAD)
	if _, err := AcquireCheckoutMutationLock(checkoutMutationStateDir(opts), token, opts.Feature, "sync",
		CheckoutTransactionPath(opts.FeaturePath)); err != nil {
		return err
	}
	opts.mutationLockToken = token
	if err := AcquireCheckoutLock(opts.FeaturePath); err != nil {
		_ = ReleaseCheckoutMutationLock(checkoutMutationStateDir(opts), token)
		return err
	}

	if opts.NewMode {
		printSyncModeHeader(opts.Policy)
	}

	// Fetch: a guarded run measures its own fetch context from insp (the
	// same FetchPlan/Gates/BasePreflight/State facts the --plan route
	// already reports) and reports the outcome to the guard-evaluation
	// document; an unguarded new-mode run keeps its shipped bare call.
	var fetchOutcome PlanFetchOutcome
	switch {
	case guarded:
		if opts.Policy.Fetch == SyncFetchEnabled && insp.FetchPlan.Applies && len(insp.FetchPlan.Blockers) == 0 &&
			checkoutRowsAvailable(insp) && !checkoutGatesFailed(insp.Gates) && !insp.BasePreflight.Failed && !insp.State.LiveForeignLock() {
			fetchOutcome = fetchCheckoutRepoTo(os.Stdout, PlanFetchContext{
				RepoToken:  "",
				Root:       opts.RepoDir,
				CommonDir:  insp.FetchPlan.Contexts[0].CommonDir,
				Source:     "workspace-repo-root",
				Candidates: insp.FetchPlan.Contexts[0].Candidates,
			})
		}
	case opts.NewMode:
		if opts.Policy.Fetch == SyncFetchEnabled {
			fetchCheckoutRepo(opts.RepoDir)
		}
	}

	// Load stack
	var stack Stack
	switch {
	case guarded:
		stack = insp.Stack
	case preloaded != nil:
		stack = *preloaded
	default:
		stack, err = LoadStack(opts.FeaturePath)
		if err != nil {
			_ = releaseCheckoutSyncLocks(opts)
			return fmt.Errorf("load stack: %w", err)
		}
	}

	// Build plan. A guarded run rebuilds from insp.Order (the sort this run
	// already performed above the lock) rather than re-sorting; an
	// unguarded run keeps its shipped BuildCheckoutPlan call, which sorts
	// the stack itself.
	var plan []CheckoutPlanEntry
	if guarded {
		plan, err = buildCheckoutPlanFrom(opts.RepoDir, stack, insp.Order, sel)
	} else if legacyOrder != nil {
		plan, err = buildCheckoutPlanFrom(opts.RepoDir, stack, legacyOrder, sel)
	} else {
		plan, err = BuildCheckoutPlan(opts.RepoDir, stack, sel)
	}
	if err != nil {
		_ = releaseCheckoutSyncLocks(opts)
		return fmt.Errorf("build plan: %w", err)
	}

	if opts.NewMode {
		printLocalOnlyNoOp(sel, plan)
	}

	if len(plan) == 0 {
		_ = releaseCheckoutSyncLocks(opts)
		return nil
	}

	// Guard seam (§13.2a, §13.6): build the guard-evaluation document from
	// the SAME insp/fetchOutcome this run already produced, then evaluate
	// it — after the plan build, before SaveCheckoutTransaction, so a
	// refusal here has moved no branch and written no transaction.
	var guardRun *checkoutPlanGuardRun
	if guarded {
		req := checkoutPlanRequest(opts, insp, fetchOutcome)
		guardPlan, berr := BuildRebasePlan(req)
		if berr != nil {
			_ = releaseCheckoutSyncLocks(opts)
			return berr
		}
		if gerr := EvaluatePlanGuard(guardPlan, opts.PlanGuard); gerr != nil {
			_ = releaseCheckoutSyncLocks(opts)
			return gerr
		}
		guardRun = newCheckoutPlanGuardRun(req, guardPlan, false)
	}

	// Create the versioned transaction and capture every rollback preimage
	// before the first checkout or branch mutation.
	tx := &CheckoutTransaction{
		StateVersion:      CheckoutTransactionTransactionalVersion,
		Feature:           opts.Feature,
		StartedAt:         startedAt,
		LockPID:           os.Getpid(),
		LockCreated:       time.Now().UTC().Format(time.RFC3339),
		Push:              opts.Push,
		TestCommand:       opts.TestCommand,
		OriginalBranch:    originalBranch,
		OriginalHEAD:      originalHEAD,
		Plan:              plan,
		CurrentIndex:      0,
		Stage:             StagePlanned,
		Route:             checkoutEffectiveRoute(opts, nil),
		FetchPolicy:       string(opts.Policy.Fetch),
		PropagationPolicy: string(opts.Policy.Propagation),
		ScopeKind:         string(opts.Policy.ScopeKind),
		ScopeSelector:     opts.Policy.Selector,
		Selected:          sel.SelectedNames(),
		ValidationSource:  "none",
	}
	if opts.TestCommand != "" {
		tx.ValidationSource = "flag"
	}
	if guarded {
		tx.StateVersion = CheckoutTransactionTransactionalGuardedVersion
		tx.PlanGuarded = true
		tx.MaxReplayPerEntry = opts.PlanGuard.MaxPerEntry
		tx.MaxReplayTotal = opts.PlanGuard.MaxTotal
	}
	entryRepos := make(map[string]string, len(sel.Entries))
	for _, entry := range sel.Entries {
		entryRepos[entry.Name] = opts.RepoDir
	}
	tx.Transaction, err = CaptureSyncTransaction(SyncTransactionBeginInput{
		FeaturePath: opts.FeaturePath, Feature: opts.Feature, Mode: ModeCheckout,
		WorkspaceRepoRoot: opts.RepoDir, Stack: stack, Selected: sel.SelectedNames(), EntryRepoDirs: entryRepos,
	})
	if err != nil {
		_ = releaseCheckoutSyncLocks(opts)
		return fmt.Errorf("capture sync rollback preimage: %w", err)
	}
	planByName := make(map[string]CheckoutPlanEntry, len(plan))
	for _, entry := range plan {
		planByName[entry.Name] = entry
	}
	cutoffInputs := make([]SyncCutoffFreezeInput, 0, len(sel.Entries))
	for _, selected := range sel.Entries {
		input := SyncCutoffFreezeInput{Entry: selected.Name, RepoDir: opts.RepoDir}
		if entry, ok := planByName[selected.Name]; ok {
			input.Applicable = true
			input.ParentRef = entry.Base
			input.ParentSHA = entry.NewBaseSHA
		}
		cutoffInputs = append(cutoffInputs, input)
	}
	if err := FreezeSyncCutoffs(tx.Transaction, stack, cutoffInputs, func() error { return nil }); err != nil {
		_ = releaseCheckoutSyncLocks(opts)
		return err
	}
	if err := PreflightSyncTransactionBirth(tx.Transaction); err != nil {
		_ = releaseCheckoutSyncLocks(opts)
		return fmt.Errorf("preflight sync rollback holders: %w", err)
	}

	// Persist before creating pins, then persist each pin as it is installed.
	if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
		_ = releaseCheckoutSyncLocks(opts)
		return fmt.Errorf("persist transaction: %w", err)
	}
	if err := PinSyncTransaction(tx.Transaction, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
		return err
	}

	opts.guard = guardRun
	return executeTransaction(opts, tx)
}

// firstUnresolvedCheckoutBase is the locator half of the checkout I14
// preflight (§10.7): the shipped loop, the shipped skip conjuncts, the
// shipped base resolution and the shipped VerifyGitRef probe, in the shipped
// order, stopping at the first failure and returning it rather than
// composing a sentence.
func firstUnresolvedCheckoutBase(opts CheckoutSyncOpts, stack Stack, sel SyncSelection) (entry, ref string, found bool) {
	if opts.Policy.Fetch != SyncFetchDisabled {
		return "", "", false
	}
	localOnly := opts.Policy.Propagation == SyncPropagationLocalOnly
	for _, e := range sel.Entries {
		if e.Archived || e.Base == "" {
			continue
		}
		r := e.Base
		if e.Role == SyncRoleAnchor {
			if localOnly {
				continue
			}
		} else if parent := GetBranch(stack, e.Base); parent.Name != "" {
			r = parent.GitBranch()
		}
		if err := VerifyGitRef(opts.RepoDir, r); err != nil {
			return e.Name, r, true
		}
	}
	return "", "", false
}

// verifyCheckoutBasesLocally is the checkout half of the I14 no-fetch preflight.
func verifyCheckoutBasesLocally(opts CheckoutSyncOpts, stack Stack, sel SyncSelection) error {
	if entry, ref, found := firstUnresolvedCheckoutBase(opts, stack, sel); found {
		return fmt.Errorf("base %q for stack entry %q does not resolve locally; drop --no-fetch or fetch manually first", ref, entry)
	}
	return nil
}

// ContinueCheckoutSync resumes a previously interrupted transaction.
func ContinueCheckoutSync(opts CheckoutSyncOpts) error {
	tx, err := LoadCheckoutTransaction(opts.FeaturePath)
	if err != nil {
		return fmt.Errorf("no transaction to continue: %w", err)
	}
	if tx.StateVersion > CheckoutTransactionTransactionalGuardedVersion {
		return fmt.Errorf("checkout sync transaction state version %d is newer than %d; upgrade tws, preserve the evidence, and inspect %s",
			tx.StateVersion, CheckoutTransactionTransactionalGuardedVersion, CheckoutTransactionPath(opts.FeaturePath))
	}
	cleanupOnly := SyncTransactionCleanupOnly(tx.Transaction)
	guarded := opts.PlanGuard.Guarded()
	var insp CheckoutPlanInspection
	if !cleanupOnly {
		legacyNoReplay := tx.Transaction == nil && tx.CurrentIndex == len(tx.Plan) &&
			(tx.Stage == StageRestoring || tx.Stage == StagePublishing || tx.Stage == StageCompleted)
		if tx.Transaction == nil && !legacyNoReplay {
			return fmt.Errorf("checkout sync recovery predates frozen cutoff evidence; preserve %s and use --abort or inspect the legacy transaction manually — continuation will not guess a replay boundary",
				CheckoutTransactionPath(opts.FeaturePath))
		}
		if tx.Transaction != nil {
			needsCutoff := tx.CurrentIndex < len(tx.Plan) &&
				tx.Stage != StageRestoring && tx.Stage != StagePublishing && tx.Stage != StageCompleted
			if needsCutoff {
				if err := RevalidateSyncCutoffs(tx.Transaction); err != nil {
					return err
				}
			} else if tx.Transaction.CutoffsReady {
				if err := RevalidateSyncCutoffs(tx.Transaction); err != nil {
					return err
				}
			}
		}
		if SyncTransactionRollingBack(tx.Transaction) {
			return fmt.Errorf("checkout sync rollback has started; forward continuation is disabled — re-run with --abort")
		}
		if TransactionNewMode(tx) {
			if err := checkoutContinueMismatches(opts, tx); err != nil {
				return err
			}
			if err := checkoutSelectedStillPresent(opts, tx); err != nil {
				return err
			}
		} else if opts.Push && !tx.Push {
			return fmt.Errorf("cannot add --push to an existing transaction that was started without it; persisted push=%v wins", tx.Push)
		}
		if guarded {
			insp = InspectCheckoutPlan(CheckoutPlanInspectionRequest{Opts: opts})
		}
	}

	// Reclaim the workspace-global lock before the feature lock, matching the
	// fresh-route order. Legacy transactions recover the token from the
	// matching global lock without changing their frozen on-disk schema.
	mutationToken, err := checkoutSyncRecoveryMutationToken(opts, tx)
	if err != nil {
		return err
	}
	if _, err := ReclaimCheckoutMutationLock(checkoutMutationStateDir(opts), mutationToken, tx.Feature, "sync",
		CheckoutTransactionPath(opts.FeaturePath)); err != nil {
		return err
	}
	opts.mutationLockToken = mutationToken
	// For continue, we forcibly reclaim the feature lock (we own the
	// transaction).
	if err := forceAcquireCheckoutLock(opts.FeaturePath); err != nil {
		_ = ReleaseCheckoutMutationLock(checkoutMutationStateDir(opts), mutationToken)
		return err
	}
	fresh, err := reloadAuthoritativeCheckoutTransaction(opts.FeaturePath, tx)
	if err != nil {
		return err
	}
	tx = fresh
	tx.LockPID = os.Getpid()

	if tx.Transaction != nil && SyncTransactionCleanupOnly(tx.Transaction) {
		if err := CompleteSyncTransaction(tx.Transaction, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
			return err
		}
		if err := RemoveCheckoutTransaction(opts.FeaturePath); err != nil {
			return err
		}
		if err := releaseCheckoutSyncLocks(opts); err != nil {
			return err
		}
		ReportSyncTransactionCleanup(tx.Transaction, os.Stdout)
		return nil
	}
	if tx.Transaction != nil {
		if tx.Transaction.Phase == SyncTxnCleanup {
			if err := CompleteSyncTransaction(tx.Transaction, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
				return err
			}
			if err := RemoveCheckoutTransaction(opts.FeaturePath); err != nil {
				return err
			}
			return releaseCheckoutSyncLocks(opts)
		}
		if _, _, active := SyncTransactionActiveRebase(tx.Transaction); !active {
			pending := len(tx.Transaction.Actions) > 0 &&
				tx.Transaction.Actions[len(tx.Transaction.Actions)-1].Status == SyncTxnActionIntent
			changed, probeErr := SyncTransactionPendingActionChanged(tx.Transaction)
			if probeErr != nil {
				return probeErr
			}
			if err := ReconcileSyncGitAction(tx.Transaction, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
				return err
			}
			if pending {
				if !changed {
					tx.Stage = StagePlanned
					if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
						return err
					}
				} else if tx.CurrentIndex < len(tx.Plan) && (tx.Stage == StageRebasing || tx.Stage == StageSwitched) {
					post, err := gitResolveRef(opts.RepoDir, tx.Plan[tx.CurrentIndex].Branch)
					if err != nil {
						return err
					}
					tx.Plan[tx.CurrentIndex].PostSHA = post
					tx.Stage = StageRebased
					if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
						return err
					}
				}
			}
		}
	}

	// Guard seam (§13.2a, §13.6 rule 4d): below the lock, in its shipped
	// continuation position. The lock is already reclaimed by this point,
	// so any refusal here always has StatePreserved: true.
	var guardRun *checkoutPlanGuardRun
	if guarded {
		req := checkoutPlanRequest(opts, insp, PlanFetchOutcome{})
		guardPlan, berr := BuildRebasePlan(req)
		if berr != nil {
			return berr
		}
		if gerr := EvaluatePlanGuard(guardPlan, opts.PlanGuard); gerr != nil {
			var refusal *PlanGuardRefusalError
			if errors.As(gerr, &refusal) {
				refusal.StatePreserved = true
			}
			return gerr
		}
		guardRun = newCheckoutPlanGuardRun(req, guardPlan, true)

		// Armed resume of a version-less/v2 transaction persists its
		// limits (§13.2a); a no-op when tx is already guarded.
		if err := upgradeGuardedCheckoutTransaction(opts.FeaturePath, tx, opts.PlanGuard.MaxPerEntry, opts.PlanGuard.MaxTotal); err != nil {
			return err
		}
	}

	// Use persisted values
	opts.Push = tx.Push
	opts.TestCommand = tx.TestCommand

	if TransactionNewMode(tx) {
		printSyncModeHeader(transactionPolicy(tx))
	}

	opts.guard = guardRun
	return resumeTransaction(opts, tx)
}

func reloadAuthoritativeCheckoutTransaction(featurePath string, expected *CheckoutTransaction) (*CheckoutTransaction, error) {
	fresh, err := LoadCheckoutTransaction(featurePath)
	if err != nil {
		return nil, fmt.Errorf("reload authoritative checkout sync transaction: %w", err)
	}
	expectedRunID, freshRunID := "", ""
	if expected.Transaction != nil {
		expectedRunID = expected.Transaction.RunID
	}
	if fresh.Transaction != nil {
		freshRunID = fresh.Transaction.RunID
	}
	if fresh.Feature != expected.Feature || fresh.StartedAt != expected.StartedAt ||
		fresh.StateVersion != expected.StateVersion || freshRunID != expectedRunID {
		return nil, errors.New("checkout sync transaction changed identity while its locks were being reclaimed; preserve the evidence and inspect it manually")
	}
	return fresh, nil
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
	if opts.Continue && checkoutRecoveryIsNewMode(tx) {
		return fmt.Errorf("--continue and --abort are mutually exclusive")
	}
	mutationToken, err := checkoutSyncRecoveryMutationToken(opts, tx)
	if err != nil {
		return err
	}
	if _, err := ReclaimCheckoutMutationLock(checkoutMutationStateDir(opts), mutationToken, tx.Feature, "sync",
		CheckoutTransactionPath(opts.FeaturePath)); err != nil {
		return err
	}
	opts.mutationLockToken = mutationToken
	if err := forceAcquireCheckoutLock(opts.FeaturePath); err != nil {
		_ = ReleaseCheckoutMutationLock(checkoutMutationStateDir(opts), mutationToken)
		return err
	}
	fresh, err := reloadAuthoritativeCheckoutTransaction(opts.FeaturePath, tx)
	if err != nil {
		return err
	}
	tx = fresh

	if tx.Transaction != nil {
		if err := AbortSyncTransaction(tx.Transaction, func() error {
			return SaveCheckoutTransaction(opts.FeaturePath, tx)
		}, os.Stdout); err != nil {
			return err
		}
		if err := RemoveCheckoutTransaction(opts.FeaturePath); err != nil {
			return err
		}
		return releaseCheckoutSyncLocks(opts)
	}

	// Legacy transactions have no closure snapshot. Preserve their historical
	// best-effort behavior, but state its limitation explicitly.
	if gitRebaseInProgress(opts.RepoDir) {
		cmd := exec.Command("git", "rebase", "--abort")
		cmd.Dir = opts.RepoDir
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("abort active rebase: %w", err)
		}
	}
	if err := restoreOriginal(opts, tx); err != nil {
		return fmt.Errorf("abort restoration failed: %w; manual recovery may be needed", err)
	}
	fmt.Println("Warning: legacy checkout sync state has no rollback snapshots; earlier branch movements and metadata cannot be fully restored.")
	if err := RemoveCheckoutTransaction(opts.FeaturePath); err != nil {
		return err
	}
	return releaseCheckoutSyncLocks(opts)
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

	case StagePublishing, StageCompleted:
		// Already done
		return finalizeCleanup(opts, tx)

	default:
		return fmt.Errorf("unknown stage %q", tx.Stage)
	}
}

func resumeFromSwitched(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	// JIT guard revalidation seam: this invocation is about to run its own
	// first Git command (the rebase) for the current entry, having resumed
	// past the branch-switch step processBranch's own seam already covers
	// in a fresh invocation. A nil guard (every unguarded run) is always a
	// no-op.
	entry := tx.Plan[tx.CurrentIndex]
	if err := opts.guard.revalidate(entry.Name); err != nil {
		return err
	}

	// Still need to rebase the current entry, then continue. A transactional
	// run already has the action intent written before the original switch.
	rebaseErr := doRebase(opts, tx)
	if tx.Transaction != nil {
		if err := CompleteSyncGitAction(tx.Transaction, rebaseErr, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
			return err
		}
	}
	if rebaseErr != nil {
		return rebaseErr
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
		if err := runCheckoutValidation(opts, tx, entry.Name); err != nil {
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
		if err := runCheckoutValidation(opts, tx, tx.Plan[tx.CurrentIndex].Name); err != nil {
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
	if err := restoreSyncOriginal(opts, tx); err != nil {
		tx.FailureKind = FailRestoration
		tx.FailureMsg = err.Error()
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return fmt.Errorf("restoration retry failed: %w", err)
	}
	if opts.Push {
		tx.Stage = StagePublishing
		if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
			return err
		}
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

	var cutoffDecision SyncCutoffDecision
	if tx.Transaction != nil {
		cutoffDecision, err = RevalidateSyncCutoffEntry(tx.Transaction, opts.RepoDir, entry.Name)
		if err != nil {
			return err
		}
	}

	// JIT guard revalidation seam: re-measure this entry against current
	// Git state immediately before its own first Git command in this
	// invocation runs, so drift between admission and execution refuses
	// rather than silently replaying a stale decision (§10, §11.8). A nil
	// guard (every unguarded run) is always a no-op.
	if err := opts.guard.revalidate(entry.Name); err != nil {
		return err
	}

	if tx.Transaction != nil {
		if err := BeginSyncRebaseAction(tx.Transaction, opts.RepoDir, entry.Name, opts.RepoDir,
			"refs/heads/"+entry.Branch,
			[]string{"refs/heads/" + entry.Branch}, entry.NewBaseSHA, cutoffDecision.EffectiveSHA,
			func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
			return err
		}
	}

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
	var checkoutErr error
	if tx.Transaction != nil {
		checkoutErr = RunSyncContextSwitch(tx.Transaction, opts.RepoDir, entry.Branch)
	} else {
		checkoutErr = gitCheckout(opts.RepoDir, entry.Branch)
	}
	if checkoutErr != nil {
		tx.FailureKind = FailSwitch
		tx.FailureMsg = checkoutErr.Error()
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return checkoutErr
	}

	if StepHook != nil {
		if err := StepHook(StageSwitched, tx.CurrentIndex); err != nil {
			tx.FailureKind = FailInterruption
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return err
		}
	}

	// Rebase
	rebaseErr := doRebase(opts, tx)
	if tx.Transaction != nil {
		if err := CompleteSyncGitAction(tx.Transaction, rebaseErr, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
			return err
		}
	}
	if rebaseErr != nil {
		return rebaseErr
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
		if err := runCheckoutValidation(opts, tx, entry.Name); err != nil {
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

	var rebaseErr error
	if tx.Transaction != nil {
		cutoff, err := SyncCutoffForEntry(tx.Transaction, entry.Name)
		if err != nil {
			return err
		}
		args := SyncNoUpdateRefsRebaseArgs("--no-fork-point", "--onto", entry.NewBaseSHA, cutoff.EffectiveSHA)
		rebaseErr = RunSyncRebaseSilent(tx.Transaction, opts.RepoDir, args...)
	} else if entry.LastBaseSHA != "" && entry.LastBaseSHA != entry.NewBaseSHA {
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
		destinationSHA, verifyErr := VerifySyncDestination(opts.RepoDir, pe.NewBaseSHA, "refs/heads/"+pe.Branch)
		if verifyErr != nil {
			return fmt.Errorf("verify destination cutoff for %s: %w", pe.Name, verifyErr)
		}
		for i := range stack.Branches {
			// Attribute by logical Name when the plan carries it (C3); an old
			// transaction with no name falls back to the first GitBranch match.
			if pe.Name != "" {
				if stack.Branches[i].Name == pe.Name {
					stack.Branches[i].LastBaseSHA = destinationSHA
					break
				}
				continue
			}
			if stack.Branches[i].GitBranch() == pe.Branch {
				stack.Branches[i].LastBaseSHA = destinationSHA
				break
			}
		}
	}
	var metadataErr error
	if tx.Transaction != nil {
		metadataErr = SyncWriteStackValue(opts.FeaturePath, tx.Transaction, stack, func() error {
			return SaveCheckoutTransaction(opts.FeaturePath, tx)
		})
	} else {
		metadataErr = SaveStack(opts.FeaturePath, stack)
	}
	if metadataErr != nil {
		tx.FailureKind = FailPersistence
		tx.FailureMsg = "failed to update stack.yaml: " + metadataErr.Error()
		if saveErr := SaveCheckoutTransaction(opts.FeaturePath, tx); saveErr != nil {
			return errors.Join(fmt.Errorf("update stack LastBaseSHA: %w", metadataErr), saveErr)
		}
		return fmt.Errorf("update stack LastBaseSHA: %w", metadataErr)
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

	if err := restoreSyncOriginal(opts, tx); err != nil {
		tx.FailureKind = FailRestoration
		tx.FailureMsg = err.Error()
		_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
		return fmt.Errorf("restoration failed: %w; use --continue to retry", err)
	}

	// Push if requested
	if opts.Push {
		tx.Stage = StagePublishing
		if err := SaveCheckoutTransaction(opts.FeaturePath, tx); err != nil {
			return fmt.Errorf("record publishing stage: %w", err)
		}
		// Rule R-PUSH's invocation-wide preflight runs ABOVE this loop, never
		// per entry: pushing half a stack and only then discovering the lease
		// cannot be strengthened is exactly the outcome it exists to prevent.
		env, preErr := reparentCheckoutPushEnvelope(opts, tx)
		if preErr != nil {
			tx.FailureKind = FailPersistence
			tx.FailureMsg = "push refused: " + preErr.Error()
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return preErr
		}
		for _, pe := range tx.Plan {
			if SyncTransactionEntryPublished(tx.Transaction, pe.Name) {
				continue
			}
			var publication SyncTransactionPushIntent
			if tx.Transaction != nil {
				prepared, prepErr := SyncPreparePublication(tx.Transaction, opts.RepoDir, pe.Name, pe.Branch)
				if prepErr != nil {
					return prepErr
				}
				publication = prepared
				if err := SyncBeginPublication(tx.Transaction, publication, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
					return err
				}
			}
			var pushErr error
			if tx.Transaction != nil {
				pushErr = gitPushIntent(opts.RepoDir, publication, env.Decision(pe.Name), opts.reparentProse())
			} else {
				pushErr = gitPush(opts.RepoDir, pe.Branch, env.Decision(pe.Name), opts.reparentProse())
			}
			if tx.Transaction != nil {
				if err := SyncFinishPublicationAttempt(tx.Transaction, pe.Name, pushErr, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
					return err
				}
			}
			if pushErr != nil {
				tx.FailureKind = FailPersistence
				tx.FailureMsg = "push failed: " + pushErr.Error()
				if saveErr := SaveCheckoutTransaction(opts.FeaturePath, tx); saveErr != nil {
					return errors.Join(fmt.Errorf("push %s: %w", pe.Branch, pushErr), saveErr)
				}
				return fmt.Errorf("push %s: %w; re-run --continue to retry push", pe.Branch, pushErr)
			}
		}
		if err := env.PersistClears(nil); err != nil {
			tx.FailureKind = FailPersistence
			tx.FailureMsg = "persist reparent remote follow-up clear after push: " + err.Error()
			tx.Stage = StageCompleted
			if saveErr := SaveCheckoutTransaction(opts.FeaturePath, tx); saveErr != nil {
				return errors.Join(err, fmt.Errorf("persist checkout recovery state: %w", saveErr))
			}
			return fmt.Errorf("persist reparent remote follow-up clear after push: %w", err)
		}
	}

	return finalizeCleanup(opts, tx)
}

func finalizeCleanup(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	// If push still pending on completed stage
	if (tx.Stage == StageCompleted || tx.Stage == StagePublishing) && opts.Push {
		// The retry loop is a second push INVOCATION point and carries its own
		// invocation-wide preflight, for the same reason the normal loop does.
		env, preErr := reparentCheckoutPushEnvelope(opts, tx)
		if preErr != nil {
			tx.FailureMsg = "push retry refused: " + preErr.Error()
			_ = SaveCheckoutTransaction(opts.FeaturePath, tx)
			return preErr
		}
		for _, pe := range tx.Plan {
			if SyncTransactionEntryPublished(tx.Transaction, pe.Name) {
				continue
			}
			var publication SyncTransactionPushIntent
			if tx.Transaction != nil {
				prepared, prepErr := SyncPreparePublication(tx.Transaction, opts.RepoDir, pe.Name, pe.Branch)
				if prepErr != nil {
					return prepErr
				}
				publication = prepared
				if err := SyncBeginPublication(tx.Transaction, publication, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
					return err
				}
			}
			var pushErr error
			if tx.Transaction != nil {
				pushErr = gitPushIntent(opts.RepoDir, publication, env.Decision(pe.Name), opts.reparentProse())
			} else {
				pushErr = gitPush(opts.RepoDir, pe.Branch, env.Decision(pe.Name), opts.reparentProse())
			}
			if tx.Transaction != nil {
				if err := SyncFinishPublicationAttempt(tx.Transaction, pe.Name, pushErr, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
					return err
				}
			}
			if pushErr != nil {
				tx.FailureMsg = "push retry failed: " + pushErr.Error()
				if saveErr := SaveCheckoutTransaction(opts.FeaturePath, tx); saveErr != nil {
					return errors.Join(fmt.Errorf("push %s: %w", pe.Branch, pushErr), saveErr)
				}
				return fmt.Errorf("push %s: %w", pe.Branch, pushErr)
			}
		}
		if err := env.PersistClears(nil); err != nil {
			tx.FailureMsg = "persist reparent remote follow-up clear after push retry: " + err.Error()
			if saveErr := SaveCheckoutTransaction(opts.FeaturePath, tx); saveErr != nil {
				return errors.Join(err, fmt.Errorf("persist checkout recovery state: %w", saveErr))
			}
			return fmt.Errorf("persist reparent remote follow-up clear after push retry: %w", err)
		}
	}

	tx.Stage = StageCompleted
	if tx.Transaction != nil {
		if err := CompleteSyncTransaction(tx.Transaction, func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) }); err != nil {
			return err
		}
	}
	if err := RemoveCheckoutTransaction(opts.FeaturePath); err != nil {
		return err
	}
	return releaseCheckoutSyncLocks(opts)
}

// ---------- Restoration ----------

func restoreSyncOriginal(opts CheckoutSyncOpts, tx *CheckoutTransaction) error {
	if tx.Transaction == nil {
		return restoreOriginal(opts, tx)
	}
	return RestoreSyncCheckoutContext(tx.Transaction, opts.RepoDir,
		func() error { return restoreOriginal(opts, tx) },
		func() error { return SaveCheckoutTransaction(opts.FeaturePath, tx) })
}

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

func runCheckoutValidation(opts CheckoutSyncOpts, tx *CheckoutTransaction, entry string) error {
	if opts.TestCommand == "" {
		return nil
	}
	if tx.Transaction != nil {
		if err := BeginSyncValidation(tx.Transaction, opts.RepoDir, entry, opts.RepoDir, func() error {
			return SaveCheckoutTransaction(opts.FeaturePath, tx)
		}); err != nil {
			return err
		}
	}
	commandErr := runValidation(opts)
	if tx.Transaction != nil {
		if err := CompleteSyncValidation(tx.Transaction, commandErr, func() error {
			return SaveCheckoutTransaction(opts.FeaturePath, tx)
		}); err != nil {
			return err
		}
	}
	return commandErr
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

// ---------------------------------------------------------------------------
// Rule R-PUSH (§12.4) — the checkout arm
// ---------------------------------------------------------------------------

// reparentProse is where rule R-PUSH's anchored warning lines go.
func (o CheckoutSyncOpts) reparentProse() io.Writer {
	if o.ReparentProse != nil {
		return o.ReparentProse
	}
	return os.Stderr
}

// reparentCheckoutPushEnvelope evaluates and persists local clearing
// observations under the checkout mutation lock, reloads the record, then runs
// invocation-wide lease preflight before the first push.
func reparentCheckoutPushEnvelope(opts CheckoutSyncOpts, tx *CheckoutTransaction) (ReparentPushEnvelope, error) {
	if refusal := ReparentPushMutationRefusal(opts.Reparent); refusal != nil {
		return ReparentPushEnvelope{Loc: opts.Reparent}, AnchorReparentRefusal(refusal)
	}
	env := LoadReparentPushEnvelope(opts.Reparent, opts.RepoDir)
	if refusal := env.LoadRefusal(); refusal != nil {
		// A record that exists and cannot be trusted refuses the whole
		// invocation before its first push, exactly like a lease that cannot
		// be strengthened. Both travel anchored, because this error is
		// rendered by the shipped checkout sync path, not by the reparent
		// command's own writer.
		return env, AnchorReparentRefusal(refusal)
	}
	if env.Record != nil {
		stack, err := LoadStack(opts.FeaturePath)
		if err != nil {
			return env, err
		}
		env, err = PrepareReparentPushEnvelope(opts.Reparent, opts.RepoDir, &stack, true)
		if err != nil {
			return env, err
		}
	}
	if !env.Active() {
		return env, nil
	}
	names := make([]string, 0, len(tx.Plan))
	for _, pe := range tx.Plan {
		names = append(names, pe.Name)
	}
	if pre := env.Preflight(names); pre.Refuse {
		return env, AnchorReparentRefusal(pre.Refusal())
	}
	return env, nil
}
