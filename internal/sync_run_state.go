package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// sync-modes — external new-mode state (spec §8) and the one shared read-only
// external-state classifier (§11.1).
// ---------------------------------------------------------------------------

// SyncRunStateVersion is the external payload state_version.
const SyncRunStateVersion = 2

// CheckoutTransactionVersion is the checkout transaction state_version.
const CheckoutTransactionVersion = 2

// SyncRunStage is the closed, exhaustive stage enum of the v2 payload.
type SyncRunStage string

const (
	SyncStageInitializing SyncRunStage = "initializing"
	SyncStageFetching     SyncRunStage = "fetching"
	SyncStageRebasing     SyncRunStage = "rebasing"
	SyncStageValidating   SyncRunStage = "validating"
	SyncStagePushing      SyncRunStage = "pushing"
	SyncStageFinalizing   SyncRunStage = "finalizing"
	SyncStageFailed       SyncRunStage = "failed"
)

// SyncRunState is the authoritative v2 payload of one new-mode external run.
type SyncRunState struct {
	StateVersion int          `yaml:"state_version"`
	Feature      string       `yaml:"feature"`
	StartedAt    string       `yaml:"started_at"`
	UpdatedAt    string       `yaml:"updated_at"`
	Marker       string       `yaml:"marker"`
	OwnerToken   string       `yaml:"owner_token"`
	Stage        SyncRunStage `yaml:"stage"`

	FetchPolicy       SyncFetchPolicy       `yaml:"fetch_policy"`
	PropagationPolicy SyncPropagationPolicy `yaml:"propagation_policy"`
	ScopeKind         SyncScopeKind         `yaml:"scope_kind"`
	ScopeSelector     string                `yaml:"scope_selector,omitempty"`

	Selected         []string `yaml:"selected"`
	Push             bool     `yaml:"push"`
	TestCommand      string   `yaml:"test_command,omitempty"`
	ValidationSource string   `yaml:"validation_source,omitempty"`

	FailedBranch string   `yaml:"failed_branch"`
	Pending      []string `yaml:"pending"`
	Completed    []string `yaml:"completed"`
	Pushed       []string `yaml:"pushed"`
	Repos        []string `yaml:"repos"`
}

// Policy re-reads the frozen decision of the run.
func (s *SyncRunState) Policy() SyncRunPolicy {
	return SyncRunPolicy{
		Fetch:       s.FetchPolicy,
		Propagation: s.PropagationPolicy,
		ScopeKind:   s.ScopeKind,
		Selector:    s.ScopeSelector,
	}
}

// SyncRunGuard is the run ownership guard.
type SyncRunGuard struct {
	PID          int    `yaml:"pid"`
	Created      string `yaml:"created"`
	Token        string `yaml:"token"`
	StateVersion int    `yaml:"state_version"`
}

// SyncStepHook is the external crash-injection seam. It is nil in production
// and MUST NOT be reachable from the CLI.
var SyncStepHook func(stage SyncRunStage, index int) error

// syncProcessAlive is the substitutable liveness predicate for the sync path.
// tws status never touches it: it injects through SyncClassifyOpts.Alive.
var syncProcessAlive = isProcessAlive

// ---------- paths ----------

// SyncRunStatePath is the v2 payload path.
func SyncRunStatePath(featurePath string) string {
	return filepath.Join(featurePath, ".sync-state.v2.yaml")
}

// SyncRunGuardPath is the run guard path.
func SyncRunGuardPath(featurePath string) string {
	return filepath.Join(featurePath, ".sync-run.lock")
}

// ---------- payload persistence ----------

// LoadSyncRunState reads and validates the v2 payload.
func LoadSyncRunState(featurePath string) (*SyncRunState, error) {
	data, err := os.ReadFile(SyncRunStatePath(featurePath))
	if err != nil {
		return nil, err
	}
	var s SyncRunState
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.StateVersion != SyncRunStateVersion {
		return nil, fmt.Errorf("unsupported scoped sync state version %d", s.StateVersion)
	}
	return &s, nil
}

// SaveSyncRunState writes the payload atomically at mode 0600 and refreshes
// updated_at.
func SaveSyncRunState(featurePath string, s *SyncRunState) error {
	s.StateVersion = SyncRunStateVersion
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return atomicWriteFile(SyncRunStatePath(featurePath), data, 0600)
}

// DeleteSyncRunState removes the payload.
func DeleteSyncRunState(featurePath string) {
	os.Remove(SyncRunStatePath(featurePath)) //nolint:errcheck
}

// HasSyncRunState reports whether a payload file exists.
func HasSyncRunState(featurePath string) bool {
	_, err := os.Lstat(SyncRunStatePath(featurePath))
	return err == nil
}

// NewSyncRunState builds a fresh payload for one run.
func NewSyncRunState(feature, marker, token string, policy SyncRunPolicy) *SyncRunState {
	now := time.Now().UTC().Format(time.RFC3339)
	return &SyncRunState{
		StateVersion:      SyncRunStateVersion,
		Feature:           feature,
		StartedAt:         now,
		UpdatedAt:         now,
		Marker:            marker,
		OwnerToken:        token,
		Stage:             SyncStageInitializing,
		FetchPolicy:       policy.Fetch,
		PropagationPolicy: policy.Propagation,
		ScopeKind:         policy.ScopeKind,
		ScopeSelector:     policy.Selector,
		Selected:          []string{},
		Pending:           []string{},
		Completed:         []string{},
		Pushed:            []string{},
		Repos:             []string{},
	}
}

// ---------- guard ----------

// ClaimSyncRunGuard claims the run guard with O_EXCL, the same idiom as
// writeLockExclusive. It never steals a live guard and refuses a stale guard
// that still has a payload.
func ClaimSyncRunGuard(featurePath, token string) error {
	path := SyncRunGuardPath(featurePath)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create sync guard directory: %w", err)
	}
	if err := writeSyncGuardExclusive(path, token); err == nil {
		return nil
	} else if !os.IsExist(err) {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read existing sync guard: %w", err)
	}
	var guard SyncRunGuard
	if err := yaml.Unmarshal(data, &guard); err != nil {
		return fmt.Errorf("invalid sync guard; inspect %s and use --abort to recover: %w", path, err)
	}
	if guard.PID <= 0 {
		return fmt.Errorf("sync guard is being initialized or is invalid; retry or inspect %s", path)
	}
	if syncProcessAlive(guard.PID) {
		return fmt.Errorf("a scoped sync is already running for %q (pid %d, started %s); wait for it or use --continue/--abort after it exits", filepath.Base(featurePath), guard.PID, guard.Created)
	}
	if HasSyncRunState(featurePath) {
		return fmt.Errorf("stale sync guard from PID %d with existing scoped state; use --continue or --abort to recover", guard.PID)
	}
	if err := removeLockIfUnchanged(path, data); err != nil {
		return fmt.Errorf("reclaim stale sync guard: %w", err)
	}
	return writeSyncGuardExclusive(path, token)
}

// ReclaimSyncRunGuard reclaims the guard for --continue/--abort. A live guard
// owned by another process is never reclaimed.
func ReclaimSyncRunGuard(featurePath, token string) error {
	path := SyncRunGuardPath(featurePath)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create sync guard directory: %w", err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeSyncGuardExclusive(path, token)
	}
	if err != nil {
		return fmt.Errorf("read sync guard: %w", err)
	}
	var guard SyncRunGuard
	if err := yaml.Unmarshal(data, &guard); err != nil {
		return fmt.Errorf("invalid sync guard %s: %w", path, err)
	}
	if guard.PID <= 0 {
		return fmt.Errorf("sync guard is being initialized or is invalid; retry or inspect %s", path)
	}
	if guard.PID != os.Getpid() && syncProcessAlive(guard.PID) {
		return fmt.Errorf("sync guard held by live process %d; cannot reclaim", guard.PID)
	}
	if err := removeLockIfUnchanged(path, data); err != nil {
		return fmt.Errorf("reclaim sync guard: %w", err)
	}
	return writeSyncGuardExclusive(path, token)
}

// ReadSyncRunGuard reads the guard read-only.
func ReadSyncRunGuard(featurePath string) (*SyncRunGuard, error) {
	data, err := os.ReadFile(SyncRunGuardPath(featurePath))
	if err != nil {
		return nil, err
	}
	var guard SyncRunGuard
	if err := yaml.Unmarshal(data, &guard); err != nil {
		return nil, err
	}
	return &guard, nil
}

// ReleaseSyncRunGuard removes the guard. It is the last teardown step.
func ReleaseSyncRunGuard(featurePath string) {
	os.Remove(SyncRunGuardPath(featurePath)) //nolint:errcheck
}

func writeSyncGuardExclusive(path, token string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	guard := SyncRunGuard{
		PID:          os.Getpid(),
		Created:      time.Now().UTC().Format(time.RFC3339),
		Token:        token,
		StateVersion: SyncRunStateVersion,
	}
	data, err := yaml.Marshal(&guard)
	if err == nil {
		_, err = file.Write(data)
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	return closeErr
}

// ---------- marker ----------

var syncMarkerRe = regexp.MustCompile(`^tws-scoped-sync-[0-9a-f]{32}\.lock$`)

// isSyncMarker recognises a per-run sentinel marker. It is used ONLY for
// classification and has exactly one caller, ClassifyExternalSyncState.
func isSyncMarker(s string) bool { return syncMarkerRe.MatchString(s) }

// ---------- the shared classifier ----------

// SyncStateCell is one cell of the §8.6 matrix, 1-12.
type SyncStateCell int

// SyncExternalState is the decoded, read-only view of a feature's external
// sync state. It performs no mutation, takes no guard, and repairs nothing.
type SyncExternalState struct {
	Cell       SyncStateCell
	Legacy     *SyncState
	LegacyErr  error
	Marker     string
	Payload    *SyncRunState
	PayloadErr error
	Guard      *SyncRunGuard
	GuardLive  bool
	GuardErr   error

	// Symlink facts, recorded from the single os.Lstat this classifier performs
	// per consulted path. They are facts, not policy: the classifier never
	// refuses, and package cli applies I18 from these fields without
	// re-Lstat-ing anything.
	LegacyPath     string
	PayloadPath    string
	GuardPath      string
	LegacySymlink  bool
	PayloadSymlink bool
	GuardSymlink   bool
}

// SyncClassifyOpts controls only when the guard file is opened and how liveness
// is decided. Neither changes the returned cell: the guard is precedence and
// context, not an axis.
type SyncClassifyOpts struct {
	AlwaysReadGuard bool
	Alive           func(pid int) bool
}

type legacyKind int

const (
	legacyAbsent legacyKind = iota
	legacySentinel
	legacyReal
	legacyInvalid
)

type payloadKind int

const (
	payloadAbsent payloadKind = iota
	payloadValid
	payloadUnreadable
)

// ClassifyExternalSyncState reads the legacy path, the payload, and (per opts)
// the guard, and returns the §8.6 cell plus every decoded value. It is
// read-only and never returns an error for "absent"; absence is expressed by
// the cell.
func ClassifyExternalSyncState(featurePath string, opts SyncClassifyOpts) SyncExternalState {
	st := SyncExternalState{
		LegacyPath:  SyncStatePath(featurePath),
		PayloadPath: SyncRunStatePath(featurePath),
		GuardPath:   SyncRunGuardPath(featurePath),
	}
	alive := opts.Alive
	if alive == nil {
		alive = syncProcessAlive
	}

	// a. legacy path — exactly one Lstat, then today's follow/read.
	lk := legacyAbsent
	if info, err := os.Lstat(st.LegacyPath); err == nil {
		st.LegacySymlink = info.Mode()&os.ModeSymlink != 0
		state, loadErr := LoadSyncState(featurePath)
		switch {
		case loadErr != nil || state == nil:
			lk = legacyInvalid
			st.LegacyErr = loadErr
			if st.LegacyErr == nil {
				st.LegacyErr = fmt.Errorf("empty sync state document")
			}
		case isSyncMarker(state.FailedBranch):
			lk = legacySentinel
			st.Legacy = state
			st.Marker = state.FailedBranch
		default:
			lk = legacyReal
			st.Legacy = state
		}
	}

	// b. payload — the single added ordinary runtime-state-path read. A
	// symlink is never followed, never read, and never trusted as content.
	pk := payloadAbsent
	if info, err := os.Lstat(st.PayloadPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			st.PayloadSymlink = true
			pk = payloadUnreadable
			st.PayloadErr = fmt.Errorf("refusing to follow symlink at %s", st.PayloadPath)
		} else {
			payload, loadErr := LoadSyncRunState(featurePath)
			if loadErr != nil || payload == nil {
				pk = payloadUnreadable
				st.PayloadErr = loadErr
				if st.PayloadErr == nil {
					st.PayloadErr = fmt.Errorf("empty scoped sync state document")
				}
			} else {
				pk = payloadValid
				st.Payload = payload
			}
		}
	}

	// c. guard — read only when the run is a new-mode run, or the legacy file
	// decoded to a sentinel, or a payload was found.
	if opts.AlwaysReadGuard || lk == legacySentinel || pk != payloadAbsent {
		if info, err := os.Lstat(st.GuardPath); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				st.GuardSymlink = true
				st.GuardErr = fmt.Errorf("refusing to follow symlink at %s", st.GuardPath)
			} else {
				guard, readErr := ReadSyncRunGuard(featurePath)
				if readErr != nil || guard == nil {
					st.GuardErr = readErr
					if st.GuardErr == nil {
						st.GuardErr = fmt.Errorf("empty sync guard document")
					}
				} else {
					st.Guard = guard
					st.GuardLive = guard.PID > 0 && alive(guard.PID)
					if st.GuardLive && st.Payload != nil && st.Payload.OwnerToken != "" && guard.Token != st.Payload.OwnerToken {
						st.GuardLive = false
					}
				}
			}
		}
	}

	st.Cell = syncStateCell(lk, pk)
	return st
}

// syncStateCell maps the two disk axes onto the 12-cell matrix of §8.6.
func syncStateCell(lk legacyKind, pk payloadKind) SyncStateCell {
	base := map[legacyKind]int{legacyAbsent: 1, legacySentinel: 4, legacyReal: 7, legacyInvalid: 10}[lk]
	return SyncStateCell(base + int(pk))
}

// GuardForeign reports a guard whose token does not match the payload's.
func (s SyncExternalState) GuardForeign() bool {
	return s.Guard != nil && s.Payload != nil && s.Payload.OwnerToken != "" && s.Guard.Token != s.Payload.OwnerToken
}

// HasGuardFile reports whether a guard file exists at all (readable or not).
func (s SyncExternalState) HasGuardFile() bool {
	return s.Guard != nil || s.GuardErr != nil || s.GuardSymlink
}
