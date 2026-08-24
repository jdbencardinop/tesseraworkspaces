package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type SyncState struct {
	StartedAt    string   `yaml:"started_at"`
	FailedBranch string   `yaml:"failed_branch"`
	Pending      []string `yaml:"pending"`
	Completed    []string `yaml:"completed"`
	Skipped      []string `yaml:"skipped"`
}

func SyncStatePath(featurePath string) string {
	return filepath.Join(featurePath, ".sync-state.yaml")
}

func LoadSyncState(featurePath string) (*SyncState, error) {
	if err := syncIOFault(SyncIOReadSyncState, SyncStatePath(featurePath)); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(SyncStatePath(featurePath))
	if err != nil {
		return nil, err
	}
	var s SyncState
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func SaveSyncState(featurePath string, s *SyncState) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return atomicWriteFile(SyncStatePath(featurePath), data, 0644)
}

// RemoveSyncState removes the legacy state/sentinel file, returning any error
// other than "already gone" (§12.2c rule 2).
func RemoveSyncState(featurePath string) error {
	path := SyncStatePath(featurePath)
	if err := syncIOFault(SyncIORemoveSyncState, path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove sync state %s: %w", path, err)
	}
	return nil
}

func DeleteSyncState(featurePath string) {
	_ = RemoveSyncState(featurePath)
}

func HasSyncState(featurePath string) bool {
	_, err := os.Stat(SyncStatePath(featurePath))
	return err == nil
}

func NewSyncState() *SyncState {
	return &SyncState{
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// SyncStateIOFault is §23.1 item 4's injected reader/remover/restorer/writer
// seam. It is nil in production and MUST NOT be reachable from the CLI. A
// test sets it to force a NON-ErrNotExist I/O failure at a named operation
// on a named path, which is the only way to exercise the error arms of the
// state writers on a filesystem where permissions cannot be made to fail
// (root, ACLs, CI containers). Returning nil lets the real operation
// proceed.
//
// op is one of the closed labels below; path is the artefact's absolute
// path, so a fault can be scoped to exactly one file.
var SyncStateIOFault func(op, path string) error

// The closed op label domain of SyncStateIOFault.
const (
	SyncIOReadSyncState        = "read-sync-state"        // the .sync-state.yaml capture read
	SyncIOReadSyncRunState     = "read-sync-run-state"    // the payload read path
	SyncIORemoveSyncState      = "remove-sync-state"      // §12.2c rule 2 remover
	SyncIORemoveSyncRunState   = "remove-sync-run-state"  // §12.2c rule 2 remover
	SyncIORemoveSyncRunGuard   = "remove-sync-run-guard"  // §12.2c rule 2 remover
	SyncIOWriteSentinel        = "write-sentinel"         // SaveGuardedLegacySentinel
	SyncIORestoreSyncState     = "restore-sync-state"     // RestoreSyncStateBytes
	SyncIORemoveStateUnchanged = "remove-state-unchanged" // RemoveSyncStateIfUnchanged
	SyncIOWriteSyncRunState    = "write-sync-run-state"   // SaveSyncRunState
	SyncIOWriteTransaction     = "write-checkout-tx"      // SaveCheckoutTransaction
	SyncIOReloadStack          = "reload-stack"           // the JIT seam's stack.yaml reload
)

// syncIOFault consults the injected seam. It returns nil in production,
// where SyncStateIOFault is nil.
func syncIOFault(op, path string) error {
	if SyncStateIOFault == nil {
		return nil
	}
	return SyncStateIOFault(op, path)
}

// ---------- guarded legacy rollback: conditional restore/remove ----------

// ErrSyncStateChanged and ErrSyncStateHasPayload are the two distinguished
// "restored nothing" outcomes of RestoreSyncStateBytes, RemoveSyncStateIfUnchanged
// and SaveGuardedLegacySentinel: both mean the file on disk was left exactly
// as found.
var ErrSyncStateChanged = errors.New("sync state changed since it was captured")
var ErrSyncStateHasPayload = errors.New("a scoped sync payload exists beside the sync state")

// RestoreSyncStateBytes writes prior back over .sync-state.yaml through the
// shipped atomic writer, but ONLY when both preconditions hold:
//
//  1. the file's current bytes are byte-identical to expect — the exact
//     sentinel this invocation installed — so a restore can never clobber a
//     document another party wrote (ErrSyncStateChanged otherwise); and
//  2. no scoped payload exists beside it (HasSyncRunState is false), so a
//     restore can never place a REAL legacy state next to a live payload,
//     which would be classifier cell 8 (ErrSyncStateHasPayload otherwise).
//
// The result is therefore either the captured bytes or the value the file had
// before the attempt — never a partial document and never a cell-8 pair.
// len(prior) == 0 is a programming error and returns an error rather than
// truncating the file; the caller uses RemoveSyncStateIfUnchanged for the
// fresh cell, where there were no prior bytes at all. The guarded legacy
// rollback NEVER calls the unconditional RemoveSyncState.
func RestoreSyncStateBytes(featurePath string, prior, expect []byte) error {
	if len(prior) == 0 {
		return fmt.Errorf("restore sync state: refusing to write an empty prior document")
	}
	if err := syncIOFault(SyncIORestoreSyncState, SyncStatePath(featurePath)); err != nil {
		return err
	}
	current, err := os.ReadFile(SyncStatePath(featurePath))
	if err != nil || !bytes.Equal(current, expect) {
		return ErrSyncStateChanged
	}
	if HasSyncRunState(featurePath) {
		return ErrSyncStateHasPayload
	}
	return atomicWriteFile(SyncStatePath(featurePath), prior, 0644)
}

// RemoveSyncStateIfUnchanged is the fresh route's rollback counterpart of
// RestoreSyncStateBytes: it removes .sync-state.yaml only while its bytes are
// still expect AND no payload exists beside it, returning ErrSyncStateChanged
// or ErrSyncStateHasPayload otherwise and removing nothing.
func RemoveSyncStateIfUnchanged(featurePath string, expect []byte) error {
	if err := syncIOFault(SyncIORemoveStateUnchanged, SyncStatePath(featurePath)); err != nil {
		return err
	}
	current, err := os.ReadFile(SyncStatePath(featurePath))
	if err != nil || !bytes.Equal(current, expect) {
		return ErrSyncStateChanged
	}
	if HasSyncRunState(featurePath) {
		return ErrSyncStateHasPayload
	}
	return RemoveSyncState(featurePath)
}

// ---------- the guarded legacy sentinel (§13.6 rule 2c) ----------

// GuardedLegacySentinelVersion is the guarded legacy sentinel's own version.
// It is deliberately a SEPARATE constant from SyncRunStateGuardedVersion: the
// payload and the sentinel are different documents with different readers.
const GuardedLegacySentinelVersion = 3

// GuardedLegacySentinel is .sync-state.yaml as a guarded legacy run writes
// it. The shipped SyncState is embedded INLINE and written exactly as the
// shipped sentinel writes it, so a binary whose decoder ignores unknown keys
// sees a sentinel and nothing else, and fails closed on the marker in every
// cell.
type GuardedLegacySentinel struct {
	SyncState `yaml:",inline"` // started_at, failed_branch = the marker, pending/completed/
	// skipped = [], byte-for-byte the shipped sentinel shape

	GuardedStateVersion int    `yaml:"guarded_state_version"` // always 3
	Route               string `yaml:"route"`                 // always "legacy"
	Feature             string `yaml:"feature"`
	Marker              string `yaml:"marker"`      // MUST equal FailedBranch
	OwnerToken          string `yaml:"owner_token"` // the token step 2 claimed with
	CreatedAt           string `yaml:"created_at"`  // RFC3339 UTC
	WriterPID           int    `yaml:"writer_pid"`  // diagnostic only; never a verdict

	MaxReplayPerEntry *int   `yaml:"max_replay_per_entry,omitempty"` // the EFFECTIVE limits, so a
	MaxReplayTotal    *int   `yaml:"max_replay_total,omitempty"`     // persisted 0 survives
	Push              bool   `yaml:"push"`                           // provenance; the flag still wins
	TestCommand       string `yaml:"test_command,omitempty"`         // the FROZEN validation command
	ValidationSource  string `yaml:"validation_source,omitempty"`    // its provenance. There is NO
	// digest member and no validation_digest key: §15's identity is DERIVED
	// from TestCommand by whoever needs it (§13.1 rule 2, §25.3)

	Universe []string `yaml:"universe"` // the full-stack Selected list in
	// the invocation's ONE TopoSort order
	PendingIntent     []string `yaml:"pending_intent"`        // the remaining set of §13.3
	CarriedCompleted  []string `yaml:"carried_completed"`     // the prior state's Completed, verbatim
	CarriedFailed     string   `yaml:"carried_failed_branch"` // the prior state's FailedBranch
	PriorPendingCount int      `yaml:"prior_pending_count"`   // len(prior.Pending) — the number the
	// shipped resume prints (§12.3 row 7)

	PriorLegacyPresent bool   `yaml:"prior_legacy_present"`          // false on a FRESH run only
	PriorLegacySHA256  string `yaml:"prior_legacy_sha256,omitempty"` // 64-lower-hex over the bytes
	PriorLegacyBase64  string `yaml:"prior_legacy_base64,omitempty"` // std base64 of the CAPTURED
	// bytes, verbatim
}

// SaveGuardedLegacySentinel installs the sentinel at .sync-state.yaml through
// ONE conditional atomic write at the shipped 0644 mode. expect is the
// captured prior bytes; nil means "expect the path to be absent" (the fresh
// route). The write refuses — writing nothing, returning ErrSyncStateChanged
// — when the current file is not what expect says it is, so the path is
// either the old bytes/absent or the complete sentinel, never anything
// between.
// MarshalGuardedLegacySentinel renders the exact bytes
// SaveGuardedLegacySentinel installs. Both go through this one function, so
// the setup of §13.2a can hand its rollback the precise `expect` bytes it is
// about to write without re-deriving them from a second, possibly divergent
// encoder — and without silently swallowing a marshal failure.
func MarshalGuardedLegacySentinel(s *GuardedLegacySentinel) ([]byte, error) {
	data, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal guarded sentinel: %w", err)
	}
	return data, nil
}

func SaveGuardedLegacySentinel(featurePath string, s *GuardedLegacySentinel, expect []byte) error {
	path := SyncStatePath(featurePath)
	if err := syncIOFault(SyncIOWriteSentinel, path); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	switch {
	case err == nil:
		if expect == nil || !bytes.Equal(current, expect) {
			return ErrSyncStateChanged
		}
	case errors.Is(err, fs.ErrNotExist):
		if expect != nil {
			return ErrSyncStateChanged
		}
	default:
		return fmt.Errorf("read sync state: %w", err)
	}
	data, err := MarshalGuardedLegacySentinel(s)
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write sync sentinel: %w", err)
	}
	return nil
}

// GuardedLegacySentinelVerdict is the closed verdict domain of
// InspectGuardedLegacySentinel.
type GuardedLegacySentinelVerdict string

const (
	SentinelNotApplicable GuardedLegacySentinelVerdict = "not-applicable" // the zero value
	SentinelAbsent        GuardedLegacySentinelVerdict = "absent"
	SentinelNotGuarded    GuardedLegacySentinelVerdict = "not-guarded" // real legacy state, or
	// a PLAIN shipped sentinel
	SentinelValid        GuardedLegacySentinelVerdict = "valid"
	SentinelSymlink      GuardedLegacySentinelVerdict = "symlink"
	SentinelUnreadable   GuardedLegacySentinelVerdict = "unreadable"
	SentinelUnsupported  GuardedLegacySentinelVerdict = "unsupported-version"
	SentinelCorrupt      GuardedLegacySentinelVerdict = "corrupt"
	SentinelForeign      GuardedLegacySentinelVerdict = "foreign"
	SentinelHashMismatch GuardedLegacySentinelVerdict = "hash-mismatch"
)

// GuardedLegacySentinelView is the decided view. Sentinel and Prior are
// non-nil ONLY under SentinelValid, so no caller can resume from a document
// this ladder did not accept.
type GuardedLegacySentinelView struct {
	Verdict  GuardedLegacySentinelVerdict
	Path     string
	Sentinel *GuardedLegacySentinel // the decoded extension
	Prior    *SyncState             // the VERIFIED backed-up legacy state; nil when
	// PriorLegacyPresent is false
	Version int   // the raw guarded_state_version, for the unsupported sentence
	Err     error // the underlying I/O or decode error; sanitized by cli
}

// isLowerHex64 reports whether s is exactly 64 lowercase hex characters, the
// shape of a SHA-256 digest as this sentinel persists it.
func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// InspectGuardedLegacySentinel performs ONE Lstat and at most ONE read of a
// regular file, follows no symlink, and applies the ordered ladder of §13.6
// rule 2c: the first failing clause decides, and only SentinelValid resumes.
// It refuses nothing and prints nothing: internal decides, cli speaks.
func InspectGuardedLegacySentinel(featurePath, feature string) GuardedLegacySentinelView {
	path := SyncStatePath(featurePath)
	view := GuardedLegacySentinelView{Path: path}

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		view.Verdict = SentinelAbsent
		return view
	case err != nil:
		view.Verdict = SentinelUnreadable
		view.Err = err
		return view
	case info.Mode()&os.ModeSymlink != 0:
		view.Verdict = SentinelSymlink
		return view
	case !info.Mode().IsRegular():
		view.Verdict = SentinelUnreadable
		return view
	}

	data, err := os.ReadFile(path)
	if err != nil {
		view.Verdict = SentinelUnreadable
		view.Err = err
		return view
	}
	if len(data) == 0 {
		view.Verdict = SentinelUnreadable
		return view
	}
	var raw GuardedLegacySentinel
	if err := yaml.Unmarshal(data, &raw); err != nil {
		view.Verdict = SentinelUnreadable
		view.Err = err
		return view
	}

	if !isSyncMarker(raw.FailedBranch) {
		view.Verdict = SentinelNotGuarded
		return view
	}
	if raw.GuardedStateVersion == 0 {
		view.Verdict = SentinelNotGuarded
		return view
	}
	if raw.GuardedStateVersion != GuardedLegacySentinelVersion {
		view.Verdict = SentinelUnsupported
		view.Version = raw.GuardedStateVersion
		return view
	}
	if raw.Route != RouteLegacy || raw.Marker != raw.FailedBranch || !isSyncMarker(raw.Marker) ||
		raw.OwnerToken == "" || raw.Feature == "" {
		view.Verdict = SentinelCorrupt
		return view
	}
	if raw.Feature != feature {
		view.Verdict = SentinelForeign
		return view
	}

	if !raw.PriorLegacyPresent {
		if raw.PriorLegacyBase64 != "" || raw.PriorLegacySHA256 != "" || raw.PriorPendingCount != 0 ||
			len(raw.CarriedCompleted) != 0 || raw.CarriedFailed != "" {
			view.Verdict = SentinelCorrupt
			return view
		}
		view.Verdict = SentinelValid
		view.Sentinel = &raw
		return view
	}

	decoded, err := base64.StdEncoding.DecodeString(raw.PriorLegacyBase64)
	if err != nil {
		view.Verdict = SentinelHashMismatch
		return view
	}
	if !isLowerHex64(raw.PriorLegacySHA256) {
		view.Verdict = SentinelHashMismatch
		return view
	}
	sum := sha256.Sum256(decoded)
	if raw.PriorLegacySHA256 != hex.EncodeToString(sum[:]) {
		view.Verdict = SentinelHashMismatch
		return view
	}

	var prior SyncState
	if err := yaml.Unmarshal(decoded, &prior); err != nil {
		view.Verdict = SentinelCorrupt
		return view
	}
	if isSyncMarker(prior.FailedBranch) {
		view.Verdict = SentinelCorrupt
		return view
	}
	if raw.PriorPendingCount != len(prior.Pending) {
		view.Verdict = SentinelCorrupt
		return view
	}

	view.Verdict = SentinelValid
	view.Sentinel = &raw
	view.Prior = &prior
	return view
}

// GuardedLegacySentinelResumable is the ONE dispatch predicate (§19.2): a
// classifier cell 4 — sentinel present, payload absent — whose sentinel this
// ladder accepted. It is evaluated on NO other cell.
func GuardedLegacySentinelResumable(st SyncExternalState, v GuardedLegacySentinelView) bool {
	return st.Cell == 4 && v.Verdict == SentinelValid
}
