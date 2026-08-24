package internal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ============================================================================
// PlanPresence / PlanUnreadableReason — the closed §4.5 enums (§12.5a).
// ============================================================================

// PlanPresence is the closed presence enum every state.files.* row (and its
// own non-file counterparts) publishes.
type PlanPresence string

const (
	PlanPresenceAbsent        PlanPresence = "absent"
	PlanPresenceReadable      PlanPresence = "readable"
	PlanPresenceUnreadable    PlanPresence = "unreadable"
	PlanPresenceSymlink       PlanPresence = "symlink"
	PlanPresenceNotApplicable PlanPresence = "not-applicable"
)

// PlanUnreadableReason is the closed reason enum, non-nil iff Presence ==
// PlanPresenceUnreadable.
type PlanUnreadableReason string

const (
	UnreadableNotRegularFile PlanUnreadableReason = "not-regular-file"
	UnreadableIOError        PlanUnreadableReason = "io-error"
	UnreadableDecodeError    PlanUnreadableReason = "decode-error"
	UnreadableEmptyDocument  PlanUnreadableReason = "empty-document"
)

// PlanFilePresence is the three-member schema header every state.files row
// begins with, plus one non-published member. It is embedded, never used on
// its own as a row type.
type PlanFilePresence struct {
	Applicable       bool                  // false on the inapplicable rows of §12.5a rule 5
	Presence         PlanPresence          // never ""; PlanPresenceNotApplicable exactly when !Applicable
	UnreadableReason *PlanUnreadableReason // non-nil iff Presence == PlanPresenceUnreadable
	Err              error                 // NOT published: the verbatim loader/Lstat/read error,
	// kept so a projected gate can print the shipped sentence
	// without a second read (§12.5a rule 8). nil unless
	// Presence == PlanPresenceUnreadable.
}

// PlanSnapshotFacts is state.snapshot: {taken_before_acquisition, self_pid},
// measured at route entry before any acquire/create/reclaim and carried
// unchanged into the guard seam (§12.1 rule 1, §12.5a rule 3).
type PlanSnapshotFacts struct {
	TakenBeforeAcquisition bool // always true; the position is the guarantee
	SelfPID                int
}

// ============================================================================
// The five concrete artefact rows (§12.5a). There is no generic artefact
// type: PlanArtefactPresence is withdrawn and never declared anywhere in this
// tree.
// ============================================================================

// PlanCheckoutTransactionFile is state.files.checkout_transaction.
type PlanCheckoutTransactionFile struct {
	PlanFilePresence
	Transaction *CheckoutTransaction // nil unless Presence == PlanPresenceReadable
	// StateVersion is the raw persisted state_version recovered even when the
	// rest of the document did not decode, so a future-version or otherwise
	// partially-corrupt document says "I found a version I cannot use" rather
	// than "absent" (§12.5a field table). It is populated only in that rescue
	// case; when Transaction != nil the version travels as
	// Transaction.StateVersion instead, and this field stays nil.
	StateVersion *int
}

// PlanCheckoutLockFile is state.files.checkout_lock.
type PlanCheckoutLockFile struct {
	PlanFilePresence
	Lock  *LockInfo // the shipped decoded .checkout-sync.lock; nil unless readable
	Alive *bool     // pid > 0 && pid != self && isProcessAlive(pid); nil unless readable
	Self  *bool     // pid == self; nil unless readable
}

// PlanLegacySyncStateFile is state.files.external_legacy_state.
type PlanLegacySyncStateFile struct {
	PlanFilePresence
	State         *SyncState // nil unless readable
	MarkerPresent bool       // the shipped sentinel test; false on every non-readable row
	Marker        string     // the sentinel value; "" on every non-readable/non-sentinel row
	CompletedLen  int        // 0 on every non-readable row
	PendingLen    int        // 0 on every non-readable row
}

// PlanSyncRunPayloadFile is state.files.external_payload.
type PlanSyncRunPayloadFile struct {
	PlanFilePresence
	Payload *SyncRunState // nil unless readable; decoded directly (never via
	// LoadSyncRunState) so a future state_version publishes
	// verbatim rather than being rejected by its version gate
	OwnerTokenPresent bool // the token is non-empty; the token itself is never published
	SelectedLen       int  // 0 on every non-readable row
	CompletedLen      int  // 0 on every non-readable row
	PushedLen         int  // 0 on every non-readable row
}

// PlanSyncRunGuardFile is state.files.external_run_guard.
type PlanSyncRunGuardFile struct {
	PlanFilePresence
	Guard *SyncRunGuard // nil unless readable; the guard's own state_version stays
	// 2 in every cell and is not a route signal
	Alive               *bool // raw-PID liveness; nil unless readable
	Self                *bool // pid == self; nil unless readable
	TokenMatchesPayload *bool // equality with the payload's owner token (inverse polarity
	// of GuardForeign()); nil when either side is
	// absent/unreadable or the payload token is empty
}

// ============================================================================
// PlanWorktreeFacts, PlanGitOp, PlanHeadFacts — the checkout-only
// non-file rows of state.* (§4.5, §12.5a).
// ============================================================================

// PlanWorktreeFacts is state.worktree.
type PlanWorktreeFacts struct {
	Applies bool
	Dirty   *bool // nil exactly when the probe failed
	ProbeOK bool
	Err     error // the probe's verbatim error; nil when ProbeOK
}

// PlanGitOp is state.git_op. It is a runtime-snapshot type, so it is declared
// here with its siblings rather than in internal/rebase_plan.go.
type PlanGitOp struct {
	Applies    bool
	InProgress bool // true IFF a shipped gitOperationInProgress marker exists: the four
	// operations rebase/merge/cherry-pick/revert over five paths
	Kind       string // none | rebase | merge | cherry-pick | revert | bisect | probe-failed
	KindSource string // executor-markers | active-op-probe | "" (=> null)
}

// PlanHeadFacts is state.head.
type PlanHeadFacts struct {
	Applies  bool
	Detached bool
	Branch   string // "" when detached
	SHA      string
	Err      error // the verbatim gitCurrentBranch/gitResolveRef error; nil on success
}

// ============================================================================
// The checkout snapshot (§12.5a).
// ============================================================================

// CheckoutPlanFiles carries the same five rows in the same §4.5 key order;
// the three external rows are the not-applicable values of §12.5a rule 5.
type CheckoutPlanFiles struct {
	CheckoutTransaction PlanCheckoutTransactionFile
	CheckoutLock        PlanCheckoutLockFile
	LegacyState         PlanLegacySyncStateFile // not applicable here
	Payload             PlanSyncRunPayloadFile  // not applicable here
	RunGuard            PlanSyncRunGuardFile    // not applicable here
}

// CheckoutPlanState is the checkout twin of ExternalPlanState and maps 1:1
// onto §4.5. It is produced once per controlled checkout invocation — by
// InspectCheckoutPlanState on the plan route and directly by the guarded
// executing route (§12.2d) — and consumed as RebasePlanRequest.CheckoutState.
type CheckoutPlanState struct {
	Applicable bool // false on an external route
	Snapshot   PlanSnapshotFacts
	Files      CheckoutPlanFiles
	Worktree   PlanWorktreeFacts // state.worktree
	GitOp      PlanGitOp         // state.git_op
	Head       PlanHeadFacts     // state.head
}

// CheckoutPlanStateOpts controls the checkout inspector's liveness seam.
type CheckoutPlanStateOpts struct {
	Alive   func(pid int) bool // nil => the shipped liveness probe; a §23.1 test seam
	SelfPID int                // 0 => os.Getpid()
}

// CheckoutPlanStateVerdict is the structured, already-measured file verdict
// the projected gates need. It exists for exactly one reason: a gate must be
// able to print a shipped sentence that wraps a loader error —
// `no transaction to continue: %w` (internal/checkout_sync.go:721) and every
// AcquireCheckoutLock/forceAcquireCheckoutLock arm of §13.7 gate j — WITHOUT
// reading the artefact a second time. Every member is verbatim; none is
// re-derived, re-worded or re-read.
type CheckoutPlanStateVerdict struct {
	TransactionErr     error        // LoadCheckoutTransaction-equivalent error, verbatim; nil when it loaded
	TransactionPresent bool         // an entry exists at the path at all (Lstat, not Stat)
	LockErr            error        // the lock read/decode error, verbatim; nil when absent or readable
	LockPresence       PlanPresence // echoed so a caller need not reach into Files
	WorktreeErr        error        // gitWorkingTreeDirty's error, verbatim
	HeadErr            error        // the HEAD probe's error, verbatim
}

// LiveForeignLock is the §11.2 cause-3 / §7.1 rank-3 observation for
// checkout: a checkout-sync lock this snapshot recorded, live, and not this
// process. The "not self" conjunct already lives inside Alive's own
// definition (pid != self), so this is a direct read of that fact.
func (s CheckoutPlanState) LiveForeignLock() bool {
	f := s.Files.CheckoutLock
	return f.Presence == PlanPresenceReadable && f.Alive != nil && *f.Alive
}

// ============================================================================
// The external snapshot (§12.5).
// ============================================================================

// ExternalPlanFiles carries exactly the five state.files rows of §4.5, in
// that key order. On an external route the two checkout rows are
// {Applicable: false, Presence: not-applicable}.
type ExternalPlanFiles struct {
	CheckoutTransaction PlanCheckoutTransactionFile // not applicable here
	CheckoutLock        PlanCheckoutLockFile        // not applicable here
	LegacyState         PlanLegacySyncStateFile     // .sync-state.yaml — real state or sentinel
	Payload             PlanSyncRunPayloadFile      // .sync-state.v2.yaml
	RunGuard            PlanSyncRunGuardFile        // .sync-run.lock
}

// ExternalPlanStateOpts controls the external inspector's liveness seam and
// consumes the classifier's verdict — REQUIRED, never recomputed here.
type ExternalPlanStateOpts struct {
	Classified SyncExternalState  // REQUIRED: the result cli's classifySyncState already produced
	Alive      func(pid int) bool // nil => the shipped syncProcessAlive probe; a §23.1 test seam
	SelfPID    int                // 0 => os.Getpid()
}

// ExternalPlanState is the plan/guard route's own safe snapshot of the five
// §4.5 runtime artefacts. It COMPOSES the shipped classifier rather than
// replacing it: Classified is consumed verbatim, and the presence/decode
// vocabulary the document needs is added around it.
type ExternalPlanState struct {
	Applicable bool              // false on a checkout route (§12.5a rule 5)
	Snapshot   PlanSnapshotFacts // state.snapshot
	Classified SyncExternalState // the classifier's verdict, verbatim
	Cell       SyncStateCell     // state.external_cell.cell, echoed from Classified.Cell
	Files      ExternalPlanFiles
}

// LiveForeignOwner is the §11.2 cause-3 / §7.1 rank-3 observation for
// external: a run guard this snapshot recorded, live, and not this process.
// Ownership of the underlying bytes travels separately as
// Files.RunGuard.TokenMatchesPayload; this answers only "is a foreign run
// holding the feature?".
func (s ExternalPlanState) LiveForeignOwner() bool {
	f := s.Files.RunGuard
	return f.Presence == PlanPresenceReadable && f.Alive != nil && *f.Alive
}

// ============================================================================
// Shared artefact-presence primitive (§12.5/§12.5a binding rule 1) — both
// inspectors reduce every one of their five artefacts to exactly this
// procedure, built on ReadArtefactPath (internal/rebase_plan_probe.go): Lstat
// first, a symlink is recorded and never followed, a non-regular path or an
// oversized one never becomes a read attempt, and an empty document is
// distinguished from a genuine decode failure before either inspector
// attempts its own artefact-specific decode.
// ============================================================================

const (
	// stateArtefactReadCap bounds a full state document read (checkout
	// transaction, legacy sync state, v2/v3 payload): generous for the
	// largest plausible stack (hundreds of branch names) while still
	// refusing to read an unbounded or hostile file.
	stateArtefactReadCap = 1 << 20 // 1 MiB

	// lockArtefactReadCap bounds the small, fixed-shape lock/guard documents
	// (pid, created, token): a few hundred bytes in practice.
	lockArtefactReadCap = 1 << 16 // 64 KiB
)

// notApplicablePresence is the {Applicable: false, Presence: not-applicable}
// header every inapplicable state.files.* row carries (§12.5a rule 5); it is
// never zero-valued, because PlanPresenceNotApplicable is not PlanPresence's zero
// value.
func notApplicablePresence() PlanFilePresence {
	return PlanFilePresence{Applicable: false, Presence: PlanPresenceNotApplicable}
}

// decodeErrorPresence is the {Presence: unreadable, UnreadableReason:
// decode-error} header an artefact-specific yaml.Unmarshal failure produces.
func decodeErrorPresence(err error) PlanFilePresence {
	reason := UnreadableDecodeError
	return PlanFilePresence{Applicable: true, Presence: PlanPresenceUnreadable, UnreadableReason: &reason, Err: err}
}

// isEmptyYAMLDocument reports "zero bytes or a document with no keys"
// (§12.5a binding rule 1): trivially true for empty/whitespace-only content,
// and otherwise decided by decoding into a bare map — a comments-only
// document, or a literal `null`, decodes to a nil/empty map exactly as a
// truly empty file does. A document this probe cannot even parse as a
// mapping is left for the artefact's own typed decode to reject with
// decode-error instead.
func isEmptyYAMLDocument(content string) bool {
	if strings.TrimSpace(content) == "" {
		return true
	}
	var probe map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &probe); err != nil {
		return false
	}
	return len(probe) == 0
}

// probeArtefact runs the shared Lstat-first, symlink-refusing, size-capped
// procedure every state.files.* row begins with, returning the common header
// plus the raw content when (and only when) the artefact is a genuine
// decode candidate. ok is false whenever the caller must not attempt to
// decode: header alone is the complete verdict in that case.
func probeArtefact(path string, maxBytes int64) (header PlanFilePresence, content string, ok bool) {
	outcome := ReadArtefactPath(path, maxBytes)
	switch {
	case outcome.IsSymlink:
		return PlanFilePresence{Applicable: true, Presence: PlanPresenceSymlink}, "", false
	case outcome.NotRegular:
		reason := UnreadableNotRegularFile
		return PlanFilePresence{Applicable: true, Presence: PlanPresenceUnreadable, UnreadableReason: &reason}, "", false
	case outcome.TooLarge:
		reason := UnreadableIOError
		err := fmt.Errorf("artefact %s exceeds the %d-byte read cap", path, maxBytes)
		return PlanFilePresence{Applicable: true, Presence: PlanPresenceUnreadable, UnreadableReason: &reason, Err: err}, "", false
	case outcome.Err != nil:
		reason := UnreadableIOError
		return PlanFilePresence{Applicable: true, Presence: PlanPresenceUnreadable, UnreadableReason: &reason, Err: outcome.Err}, "", false
	case !outcome.Exists:
		return PlanFilePresence{Applicable: true, Presence: PlanPresenceAbsent}, "", false
	case isEmptyYAMLDocument(outcome.Content):
		reason := UnreadableEmptyDocument
		return PlanFilePresence{Applicable: true, Presence: PlanPresenceUnreadable, UnreadableReason: &reason}, "", false
	default:
		return PlanFilePresence{Applicable: true, Presence: PlanPresenceReadable}, outcome.Content, true
	}
}

// ============================================================================
// Per-artefact inspectors. Each performs exactly the probeArtefact read above
// plus, at most, one artefact-specific yaml.Unmarshal of the content it
// already holds — never a second file read (§12.5 step 6). Where a caller
// already holds a successfully-decoded value for the very same path (the
// external inspector's classifier reuse, §12.5 step 4), that value is used
// directly and this inspector's own decode is skipped entirely.
// ============================================================================

// inspectCheckoutTransactionFile is state.files.checkout_transaction, always
// produced by the checkout route (never reused from elsewhere: there is no
// classifier for the checkout artefacts).
func inspectCheckoutTransactionFile(path string) PlanCheckoutTransactionFile {
	header, content, ok := probeArtefact(path, stateArtefactReadCap)
	file := PlanCheckoutTransactionFile{PlanFilePresence: header}
	if !ok {
		return file
	}
	var tx CheckoutTransaction
	if err := yaml.Unmarshal([]byte(content), &tx); err != nil {
		file.PlanFilePresence = decodeErrorPresence(err)
		// Rescue a bare state_version even though the full transaction shape
		// did not decode, so a future-version (or otherwise partially
		// corrupt) document says "I found a version I cannot use" rather
		// than publishing absent (§12.5a field table). Decoding the same
		// in-memory content a second time here is not a second file read.
		var probe struct {
			StateVersion int `yaml:"state_version"`
		}
		if probeErr := yaml.Unmarshal([]byte(content), &probe); probeErr == nil && probe.StateVersion != 0 {
			v := probe.StateVersion
			file.StateVersion = &v
		}
		return file
	}
	file.Transaction = &tx
	return file
}

// inspectCheckoutLockFile is state.files.checkout_lock.
func inspectCheckoutLockFile(path string, selfPID int, alive func(int) bool) PlanCheckoutLockFile {
	header, content, ok := probeArtefact(path, lockArtefactReadCap)
	file := PlanCheckoutLockFile{PlanFilePresence: header}
	if !ok {
		return file
	}
	var lock LockInfo
	if err := yaml.Unmarshal([]byte(content), &lock); err != nil {
		file.PlanFilePresence = decodeErrorPresence(err)
		return file
	}
	file.Lock = &lock
	isAlive := lock.PID > 0 && lock.PID != selfPID && alive(lock.PID)
	isSelf := lock.PID == selfPID
	file.Alive = &isAlive
	file.Self = &isSelf
	return file
}

// inspectLegacyStateFile is state.files.external_legacy_state. classified is
// Classified.Legacy — non-nil exactly when ClassifyExternalSyncState already
// decoded this same path successfully; when non-nil it is reused directly
// rather than re-decoded (§12.5 step 4).
func inspectLegacyStateFile(path string, classified *SyncState) PlanLegacySyncStateFile {
	header, content, ok := probeArtefact(path, stateArtefactReadCap)
	file := PlanLegacySyncStateFile{PlanFilePresence: header}
	if !ok {
		return file
	}
	state := classified
	if state == nil {
		var decoded SyncState
		if err := yaml.Unmarshal([]byte(content), &decoded); err != nil {
			file.PlanFilePresence = decodeErrorPresence(err)
			return file
		}
		state = &decoded
	}
	file.State = state
	if isSyncMarker(state.FailedBranch) {
		file.MarkerPresent = true
		file.Marker = state.FailedBranch
	}
	file.CompletedLen = len(state.Completed)
	file.PendingLen = len(state.Pending)
	return file
}

// inspectSyncRunPayloadFile is state.files.external_payload. classified is
// Classified.Payload, reused exactly as inspectLegacyStateFile reuses
// Classified.Legacy. The fallback decode below is this inspector's OWN,
// direct yaml.Unmarshal into SyncRunState rather than a call to
// LoadSyncRunState, so a state_version that loader's hard version gate would
// reject (a future 3, ahead of this tree's LoadSyncRunState) still publishes
// verbatim instead of being reported as unreadable.
func inspectSyncRunPayloadFile(path string, classified *SyncRunState) PlanSyncRunPayloadFile {
	header, content, ok := probeArtefact(path, stateArtefactReadCap)
	file := PlanSyncRunPayloadFile{PlanFilePresence: header}
	if !ok {
		return file
	}
	payload := classified
	if payload == nil {
		var decoded SyncRunState
		if err := yaml.Unmarshal([]byte(content), &decoded); err != nil {
			file.PlanFilePresence = decodeErrorPresence(err)
			return file
		}
		payload = &decoded
	}
	file.Payload = payload
	file.OwnerTokenPresent = payload.OwnerToken != ""
	file.SelectedLen = len(payload.Selected)
	file.CompletedLen = len(payload.Completed)
	file.PushedLen = len(payload.Pushed)
	return file
}

// inspectSyncRunGuardFile is state.files.external_run_guard. classified is
// Classified.Guard, reused exactly as the other two external artefacts reuse
// their classifier value — but this artefact's OWN presence procedure always
// runs regardless of what the classifier did, because
// ClassifyExternalSyncState only opens the guard conditionally
// (AlwaysReadGuard, a sentinel, or a payload), while this inspector must
// inspect it unconditionally on every route (§12.5 binding rule 1).
// payloadFile is this same invocation's already-built Payload row, consulted
// only for TokenMatchesPayload.
func inspectSyncRunGuardFile(path string, classified *SyncRunGuard, selfPID int, alive func(int) bool, payloadFile PlanSyncRunPayloadFile) PlanSyncRunGuardFile {
	header, content, ok := probeArtefact(path, lockArtefactReadCap)
	file := PlanSyncRunGuardFile{PlanFilePresence: header}
	if !ok {
		return file
	}
	guard := classified
	if guard == nil {
		var decoded SyncRunGuard
		if err := yaml.Unmarshal([]byte(content), &decoded); err != nil {
			file.PlanFilePresence = decodeErrorPresence(err)
			return file
		}
		guard = &decoded
	}
	file.Guard = guard
	isAlive := guard.PID > 0 && guard.PID != selfPID && alive(guard.PID)
	isSelf := guard.PID == selfPID
	file.Alive = &isAlive
	file.Self = &isSelf
	if payloadFile.Presence == PlanPresenceReadable && payloadFile.Payload != nil && payloadFile.Payload.OwnerToken != "" {
		matches := guard.Token == payloadFile.Payload.OwnerToken
		file.TokenMatchesPayload = &matches
	}
	return file
}

// ============================================================================
// Checkout-only non-file probes: worktree, git_op, head (§4.5).
// ============================================================================

// probeWorktreeFacts is state.worktree.
func probeWorktreeFacts(repoDir string) PlanWorktreeFacts {
	facts := PlanWorktreeFacts{Applies: true}
	dirty, err := gitWorkingTreeDirty(repoDir)
	if err != nil {
		facts.Err = err
		return facts
	}
	facts.ProbeOK = true
	facts.Dirty = &dirty
	return facts
}

// gitPathExistsChecked mirrors gitPathExists (internal/checkout_sync.go),
// which swallows every failure into a bare false, but surfaces the
// underlying `git rev-parse --git-path` invocation failure instead of
// discarding it. It exists solely so the BISECT_START active-op probe below
// can distinguish "the marker is absent" from "the probe itself failed",
// which the git_op.kind domain publishes as the distinct probe-failed member.
func gitPathExistsChecked(repoDir, name string) (bool, error) {
	path, err := checkoutGitOutput(repoDir, "rev-parse", "--git-path", name)
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoDir, path)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return false, nil
		}
		return false, statErr
	}
	return true, nil
}

// probeGitOpKind is state.git_op.{in_progress,kind,kind_source}. It reuses
// the shipped gitOperationInProgress/gitRebaseInProgress/gitPathExists
// definitions verbatim for the four executor-marker kinds (§13.7 gate c),
// and only reaches for the BISECT_START active-op probe when none of those
// five paths exists — bisect and probe-failed always carry in_progress:false
// (§4.5, §19.23).
func probeGitOpKind(repoDir string) (kind, source string, inProgress bool) {
	if gitOperationInProgress(repoDir) {
		switch {
		case gitRebaseInProgress(repoDir):
			return "rebase", "executor-markers", true
		case gitPathExists(repoDir, "MERGE_HEAD"):
			return "merge", "executor-markers", true
		case gitPathExists(repoDir, "CHERRY_PICK_HEAD"):
			return "cherry-pick", "executor-markers", true
		default:
			return "revert", "executor-markers", true
		}
	}
	found, err := gitPathExistsChecked(repoDir, "BISECT_START")
	switch {
	case err != nil:
		return "probe-failed", "active-op-probe", false
	case found:
		return "bisect", "active-op-probe", false
	default:
		return "none", "active-op-probe", false
	}
}

// probeHeadFacts is state.head. gitCurrentBranch's failure is the standard
// git-plumbing signal for a detached HEAD (`symbolic-ref --short HEAD` fails
// exactly when HEAD is not a symbolic ref), so it is read as Detached rather
// than as an error; Err is reserved for a genuine gitResolveRef failure (an
// unborn or otherwise unresolvable HEAD).
func probeHeadFacts(repoDir string) PlanHeadFacts {
	facts := PlanHeadFacts{Applies: true}
	branch, err := gitCurrentBranch(repoDir)
	if err != nil {
		facts.Detached = true
	} else {
		facts.Branch = branch
	}
	sha, err := gitResolveRef(repoDir, "HEAD")
	if err != nil {
		facts.Err = err
		return facts
	}
	facts.SHA = sha
	return facts
}

// ============================================================================
// The two top-level producers (§12.5/§12.5a). Each is pure-read: no lock is
// taken, nothing is written, nothing is fetched, and no refusal is raised —
// every failure is carried home as a field on the returned value(s) instead.
// ============================================================================

// InspectCheckoutPlanState is the checkout snapshot producer. It performs
// pure reads only: the Lstat-first transaction/lock inspection and the
// shipped gitOperationInProgress/gitWorkingTreeDirty/gitCurrentBranch/
// gitResolveRef HEAD probes. Called exactly once per controlled checkout
// invocation (rule 7).
func InspectCheckoutPlanState(opts CheckoutSyncOpts, o CheckoutPlanStateOpts) (CheckoutPlanState, CheckoutPlanStateVerdict) {
	self := o.SelfPID
	if self == 0 {
		self = os.Getpid()
	}
	alive := o.Alive
	if alive == nil {
		alive = isProcessAlive
	}

	txFile := inspectCheckoutTransactionFile(CheckoutTransactionPath(opts.FeaturePath))
	lockFile := inspectCheckoutLockFile(CheckoutLockPath(opts.FeaturePath), self, alive)
	worktree := probeWorktreeFacts(opts.RepoDir)
	kind, source, inProgress := probeGitOpKind(opts.RepoDir)
	head := probeHeadFacts(opts.RepoDir)

	state := CheckoutPlanState{
		Applicable: true,
		Snapshot:   PlanSnapshotFacts{TakenBeforeAcquisition: true, SelfPID: self},
		Files: CheckoutPlanFiles{
			CheckoutTransaction: txFile,
			CheckoutLock:        lockFile,
			LegacyState:         PlanLegacySyncStateFile{PlanFilePresence: notApplicablePresence()},
			Payload:             PlanSyncRunPayloadFile{PlanFilePresence: notApplicablePresence()},
			RunGuard:            PlanSyncRunGuardFile{PlanFilePresence: notApplicablePresence()},
		},
		Worktree: worktree,
		GitOp:    PlanGitOp{Applies: true, InProgress: inProgress, Kind: kind, KindSource: source},
		Head:     head,
	}
	verdict := CheckoutPlanStateVerdict{
		TransactionErr:     txFile.Err,
		TransactionPresent: txFile.Presence != PlanPresenceAbsent,
		LockErr:            lockFile.Err,
		LockPresence:       lockFile.Presence,
		WorktreeErr:        worktree.Err,
		HeadErr:            head.Err,
	}
	return state, verdict
}

// InspectExternalPlanState is the external snapshot producer. It composes
// opts.Classified (the classifier's already-decoded verdict, REQUIRED and
// never recomputed) with the independent, uniform Lstat-first presence
// procedure every state.files.* row requires — including an unconditional
// probe of the run-guard path, since ClassifyExternalSyncState only opens it
// conditionally (AlwaysReadGuard, a legacy sentinel, or a non-absent
// payload), while this inspector must inspect it on every route regardless
// (§12.5 binding rule 1). featurePath identifies the invocation the caller's
// classification was already scoped to; every artefact path this inspector
// reads is instead taken from opts.Classified itself, so a reused decoded
// value can never be paired with a path other than the one it was decoded
// from. Called exactly once per controlled external invocation (rule 7).
func InspectExternalPlanState(featurePath string, opts ExternalPlanStateOpts) ExternalPlanState {
	_ = featurePath // paths are sourced from opts.Classified; see doc comment above
	self := opts.SelfPID
	if self == 0 {
		self = os.Getpid()
	}
	alive := opts.Alive
	if alive == nil {
		alive = syncProcessAlive
	}

	legacyFile := inspectLegacyStateFile(opts.Classified.LegacyPath, opts.Classified.Legacy)
	payloadFile := inspectSyncRunPayloadFile(opts.Classified.PayloadPath, opts.Classified.Payload)
	guardFile := inspectSyncRunGuardFile(opts.Classified.GuardPath, opts.Classified.Guard, self, alive, payloadFile)

	return ExternalPlanState{
		Applicable: true,
		Snapshot:   PlanSnapshotFacts{TakenBeforeAcquisition: true, SelfPID: self},
		Classified: opts.Classified,
		Cell:       opts.Classified.Cell,
		Files: ExternalPlanFiles{
			CheckoutTransaction: PlanCheckoutTransactionFile{PlanFilePresence: notApplicablePresence()},
			CheckoutLock:        PlanCheckoutLockFile{PlanFilePresence: notApplicablePresence()},
			LegacyState:         legacyFile,
			Payload:             payloadFile,
			RunGuard:            guardFile,
		},
	}
}
