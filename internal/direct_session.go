package internal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// directSessionSchema is the on-disk schema version of a direct session
// record. A record with a higher version is reported as unsupported rather
// than parsed.
const directSessionSchema = 1

// Direct session stages. "starting" exists only in external records: it marks
// the window between record creation and a successful agent spawn.
const (
	DirectStageStarting = "starting"
	DirectStageAgent    = "agent"
	DirectStageShell    = "shell"
)

// directSessionsDirName is the hidden per-feature directory holding
// per-invocation direct session records. It is dot-prefixed, so
// isReservedDir already keeps it out of every feature listing.
const directSessionsDirName = ".sessions"

var directRecordFileRe = regexp.MustCompile(`^[0-9a-f]{32}\.json$`)

// DirectSessionRecord is one external `tws open` invocation running an agent
// directly (no tmux). It is advisory liveness data, never a lock.
//
// It deliberately stores no argv, no prompt, no transcript, and no
// environment: Agent holds the bare binary token only.
type DirectSessionRecord struct {
	SchemaVersion int    `json:"schema_version"`
	Token         string `json:"token"`
	Feature       string `json:"feature"`
	Name          string `json:"name"`
	GitBranch     string `json:"git_branch"`
	Path          string `json:"path"`
	Agent         string `json:"agent,omitempty"`
	OwnerPID      int    `json:"owner_pid"`
	ChildPID      int    `json:"child_pid,omitempty"`
	Stage         string `json:"stage"`
	StartedAt     string `json:"started_at"`
	UpdatedAt     string `json:"updated_at"`
}

// DirectRecordState classifies how trustworthy a loaded record is.
type DirectRecordState string

const (
	DirectRecordOK          DirectRecordState = "ok"
	DirectRecordInvalid     DirectRecordState = "invalid"
	DirectRecordUnsupported DirectRecordState = "unsupported"
)

// DirectSessionIdentity is the (feature, name) pair a caller expects every
// record in a <branch-id> directory to carry. It exists because <branch-id>
// is a truncated hash, so a collision must be detected rather than silently
// merging two logical branches.
type DirectSessionIdentity struct {
	Feature string
	Name    string
}

// LoadedDirectSession is one record as found on disk, with its validation
// verdict. An invalid record is returned, never dropped: reporting it is the
// product.
type LoadedDirectSession struct {
	Record   DirectSessionRecord
	File     string
	BranchID string
	State    DirectRecordState
	Problem  string
}

// DirectSessionTarget names one <branch-id> directory and the identity its
// records must carry. A nil Want skips identity matching, which is what a
// feature-wide inventory scan needs: it does not know which branch each
// directory belongs to and must not fabricate one.
type DirectSessionTarget struct {
	BranchID string
	Want     *DirectSessionIdentity
}

// DirectSessionsDir returns the hidden records directory for a feature.
func DirectSessionsDir(featurePath string) string {
	return filepath.Join(featurePath, directSessionsDirName)
}

// DirectSessionBranchID returns the directory name holding the records of one
// logical branch. The raw name is never used as a path component because
// StackEntry.Name may contain path-hostile characters.
func DirectSessionBranchID(feature, name string) string {
	return hashedSessionID(feature+"/"+name, feature+"_"+name)
}

func directSessionBranchDir(featurePath, branchID string) string {
	return filepath.Join(DirectSessionsDir(featurePath), branchID)
}

func directSessionFile(featurePath, branchID, token string) string {
	return filepath.Join(directSessionBranchDir(featurePath, branchID), token+".json")
}

func newDirectSessionToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating direct session token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func directSessionNow() string { return time.Now().UTC().Format(time.RFC3339) }

// CreateDirectSession mints a fresh ownership token and writes one record
// atomically. It never overwrites an existing file.
//
// StartedAt is preserved when the caller supplies it (used by the shell-stage
// recreate path); otherwise it is set to now.
func CreateDirectSession(featurePath string, rec DirectSessionRecord) (string, error) {
	if featurePath == "" {
		return "", fmt.Errorf("direct session record requires a feature path")
	}
	if rec.Feature == "" || rec.Name == "" {
		return "", fmt.Errorf("direct session record requires a feature and branch name")
	}
	token, err := newDirectSessionToken()
	if err != nil {
		return "", err
	}
	branchID := DirectSessionBranchID(rec.Feature, rec.Name)
	dir := directSessionBranchDir(featurePath, branchID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("creating direct session directory %s: %w", dir, err)
	}

	now := directSessionNow()
	rec.SchemaVersion = directSessionSchema
	rec.Token = token
	rec.OwnerPID = os.Getpid()
	if rec.Stage == "" {
		rec.Stage = DirectStageStarting
	}
	if rec.StartedAt == "" {
		rec.StartedAt = now
	}
	rec.UpdatedAt = now

	path := directSessionFile(featurePath, branchID, token)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("direct session record already exists: %s", DirectRecordDisplayPath(path))
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	if err := atomicSessionWrite(path, data, 0600); err != nil {
		return "", redactDirectRecordErr(path,
			fmt.Errorf("writing direct session record %s: %w", DirectRecordDisplayPath(path), err))
	}
	return token, nil
}

// UpdateDirectSession re-reads the token-owned record, applies mutate,
// refreshes UpdatedAt, and rewrites it atomically. A missing record returns an
// fs.ErrNotExist-wrapping error so callers can distinguish a benign removal
// race from a broken store.
func UpdateDirectSession(featurePath, branchID, token string, mutate func(*DirectSessionRecord)) error {
	path := directSessionFile(featurePath, branchID, token)
	data, err := os.ReadFile(path)
	if err != nil {
		return redactDirectRecordErr(path,
			fmt.Errorf("reading direct session record %s: %w", DirectRecordDisplayPath(path), err))
	}
	var rec DirectSessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return redactDirectRecordErr(path,
			fmt.Errorf("parsing direct session record %s: %w", DirectRecordDisplayPath(path), err))
	}
	if rec.Token != token {
		return fmt.Errorf("direct session record %s is owned by another token", DirectRecordDisplayPath(path))
	}
	if mutate != nil {
		mutate(&rec)
	}
	rec.Token = token
	rec.UpdatedAt = directSessionNow()
	out, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return redactDirectRecordErr(path, atomicSessionWrite(path, out, 0600))
}

// LoadDirectSessions reads every record in one <branch-id> directory.
//
// want is the identity each record must carry; a nil want skips identity
// matching entirely. An invalid record never aborts enumeration of its
// siblings.
func LoadDirectSessions(featurePath, branchID string, want *DirectSessionIdentity) ([]LoadedDirectSession, error) {
	dir := directSessionBranchDir(featurePath, branchID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading direct session directory %s: %w", dir, err)
	}

	var loaded []LoadedDirectSession
	for _, e := range entries {
		if e.IsDir() || !directRecordFileRe.MatchString(e.Name()) {
			continue
		}
		if info, infoErr := e.Info(); infoErr == nil && !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		item := LoadedDirectSession{File: path, BranchID: branchID, State: DirectRecordOK}

		data, readErr := os.ReadFile(path)
		if errors.Is(readErr, fs.ErrNotExist) {
			// The owner exited between ReadDir and ReadFile. Benign race.
			continue
		}
		if readErr != nil {
			item.State = DirectRecordInvalid
			item.Problem = "unreadable: " + redactDirectRecordText(readErr.Error(), path)
			loaded = append(loaded, item)
			continue
		}
		if err := json.Unmarshal(data, &item.Record); err != nil {
			item.State = DirectRecordInvalid
			item.Problem = "unparseable: " + err.Error()
			loaded = append(loaded, item)
			continue
		}
		if item.Record.SchemaVersion > directSessionSchema {
			item.State = DirectRecordUnsupported
			item.Problem = fmt.Sprintf("schema version %d is newer than %d", item.Record.SchemaVersion, directSessionSchema)
			loaded = append(loaded, item)
			continue
		}
		stem := e.Name()[:len(e.Name())-len(".json")]
		if item.Record.Token != stem {
			item.State = DirectRecordInvalid
			item.Problem = "token mismatch"
			loaded = append(loaded, item)
			continue
		}
		if want != nil && (item.Record.Feature != want.Feature || item.Record.Name != want.Name) {
			item.State = DirectRecordInvalid
			item.Problem = "identity mismatch"
			loaded = append(loaded, item)
			continue
		}
		if item.Record.OwnerPID <= 0 {
			item.State = DirectRecordInvalid
			item.Problem = "invalid owner pid"
			loaded = append(loaded, item)
			continue
		}
		loaded = append(loaded, item)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].File < loaded[j].File })
	return loaded, nil
}

// ListDirectSessions returns every record of a feature keyed by <branch-id>.
// It never invents an identity to check against, so a directory belonging to a
// renamed or deleted branch is returned as found.
func ListDirectSessions(featurePath string) (map[string][]LoadedDirectSession, error) {
	root := DirectSessionsDir(featurePath)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading direct session directory %s: %w", root, err)
	}
	out := make(map[string][]LoadedDirectSession)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		records, loadErr := LoadDirectSessions(featurePath, e.Name(), nil)
		if loadErr != nil {
			return nil, loadErr
		}
		out[e.Name()] = records
	}
	return out, nil
}

// RemoveOwnedDirectSession unlinks exactly one record, and only when the
// recorded token matches. Empty parent directories are pruned best-effort;
// ENOTEMPTY (a concurrent sibling still holds a record) is expected.
func RemoveOwnedDirectSession(featurePath, branchID, token string) error {
	path := directSessionFile(featurePath, branchID, token)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		pruneDirectSessionDirs(featurePath, branchID)
		return nil
	}
	if err != nil {
		return redactDirectRecordErr(path,
			fmt.Errorf("reading direct session record %s: %w", DirectRecordDisplayPath(path), err))
	}
	var rec DirectSessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return redactDirectRecordErr(path,
			fmt.Errorf("parsing direct session record %s: %w", DirectRecordDisplayPath(path), err))
	}
	if rec.Token != token {
		// Not ours; leave it alone.
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return redactDirectRecordErr(path,
			fmt.Errorf("removing direct session record %s: %w", DirectRecordDisplayPath(path), err))
	}
	pruneDirectSessionDirs(featurePath, branchID)
	return nil
}

// pruneDirectSessionDirs removes the <branch-id> and .sessions directories
// when they are empty. Failures are ignored: a non-empty directory is the
// normal concurrent case and an unremovable one is merely untidy.
func pruneDirectSessionDirs(featurePath, branchID string) {
	_ = os.Remove(directSessionBranchDir(featurePath, branchID))
	_ = os.Remove(DirectSessionsDir(featurePath))
}

// GuardDirectSessionsFor reports records that block a destructive operation.
//
// blocking = live OR unknown(EPERM) OR State != ok. This is deliberately
// stricter than `tws close`: close is non-destructive to identity (it kills a
// tmux session and unlinks provably dead files), so leaving an unverifiable
// record in place is harmless. rename, archive, and delete destroy or relocate
// identity, so anything not provably dead must block.
func GuardDirectSessionsFor(featurePath string, targets []DirectSessionTarget, proc ProcessProber) (blocking []LoadedDirectSession, stale []LoadedDirectSession, err error) {
	if proc == nil {
		proc = realProcessChecker{}
	}
	for _, target := range targets {
		records, loadErr := LoadDirectSessions(featurePath, target.BranchID, target.Want)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		for _, rec := range records {
			if rec.State != DirectRecordOK {
				blocking = append(blocking, rec)
				continue
			}
			switch proc.Probe(rec.Record.OwnerPID) {
			case ProcessLive, ProcessUnknown:
				blocking = append(blocking, rec)
			default:
				stale = append(stale, rec)
			}
		}
	}
	return blocking, stale, nil
}

// RemoveStaleDirectSessions unlinks provably stale records one file at a time,
// re-verifying each record's own token. It never sweeps a directory.
func RemoveStaleDirectSessions(featurePath string, stale []LoadedDirectSession) (int, error) {
	removed := 0
	var errs []error
	for _, rec := range stale {
		if rec.Record.Token == "" || rec.BranchID == "" {
			continue
		}
		before := rec.File
		if err := RemoveOwnedDirectSession(featurePath, rec.BranchID, rec.Record.Token); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := os.Stat(before); errors.Is(err, fs.ErrNotExist) {
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

// DescribeDirectSession renders one record for an operator-facing refusal or
// cleanup message. It never prints the full ownership token.
func DescribeDirectSession(rec LoadedDirectSession) string {
	id := DirectRecordID(rec.Record.Token)
	if id == "" {
		id = "unknown"
	}
	desc := fmt.Sprintf("record %s (owner pid %d, stage %s)", id, rec.Record.OwnerPID, rec.Record.Stage)
	if rec.Record.ChildPID > 0 {
		desc += fmt.Sprintf(" child pid %d", rec.Record.ChildPID)
	}
	if rec.Record.StartedAt != "" {
		desc += ", started " + rec.Record.StartedAt
	}
	if rec.State != DirectRecordOK {
		desc += fmt.Sprintf(" [%s: %s]", rec.State, rec.Problem)
	}
	return desc + " at " + DirectRecordDisplayPath(rec.File)
}

// DirectRecordID returns the correlation prefix of an ownership token: the
// first 8 of its 32 hex characters. The token grants nothing — it is an
// ownership tag, not a capability — so a prefix is safe to publish.
func DirectRecordID(token string) string {
	if len(token) < 8 {
		return ""
	}
	return token[:8]
}

// DirectRecordDisplayPath renders a record file path for operator-facing
// output as <dir>/<record-id>*.json.
//
// A record's basename is its full ownership token, so the real path may never
// reach JSON, human output, stdout, stderr, or a returned error. Callers keep
// the real path for I/O and pass it through this helper only when printing.
func DirectRecordDisplayPath(file string) string {
	if file == "" {
		return ""
	}
	dir, base := filepath.Split(file)
	stem := strings.TrimSuffix(base, ".json")
	id := DirectRecordID(stem)
	if id == "" {
		id = "unknown"
	}
	return filepath.Join(dir, id+"*.json")
}

// redactDirectRecordText replaces every occurrence of a record's real path and
// of its bare token with the redacted forms. It exists because errors raised
// below this package (*fs.PathError, *os.LinkError) embed the full path in
// their own message, which no format-string discipline at the call site can
// prevent.
func redactDirectRecordText(text, file string) string {
	if file == "" || text == "" {
		return text
	}
	text = strings.ReplaceAll(text, file, DirectRecordDisplayPath(file))
	stem := strings.TrimSuffix(filepath.Base(file), ".json")
	if id := DirectRecordID(stem); id != "" {
		text = strings.ReplaceAll(text, stem, id+"*")
	}
	return text
}

// redactedDirectRecordError carries a redacted message while keeping the
// original cause reachable through errors.Is and errors.As.
type redactedDirectRecordError struct {
	msg   string
	cause error
}

func (e *redactedDirectRecordError) Error() string { return e.msg }
func (e *redactedDirectRecordError) Unwrap() error { return e.cause }

// redactDirectRecordErr is the single choke point every record-path error
// passes through before it is returned to a caller.
func redactDirectRecordErr(file string, err error) error {
	if err == nil {
		return nil
	}
	msg := redactDirectRecordText(err.Error(), file)
	if msg == err.Error() {
		return err
	}
	return &redactedDirectRecordError{msg: msg, cause: err}
}
