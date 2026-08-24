package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// sync-modes — the package-cli half of the feature (spec §3).
//
// This file owns: flag presence and axis resolution (I1-I8), the one external
// sync layout resolver (§3.11), marker generation (§8.2), the thin classifier
// wrapper that applies I18/deferred I7/I20 and the §8.7 message table, scoped
// failure persistence, the header, and the `--only`/`--from` completion.
// ---------------------------------------------------------------------------

// errSyncModeFlagsNeedV2 is the single I20 literal. It is used verbatim and
// identically by both workspace modes (spec §3.5).
const errSyncModeFlagsNeedV2 = "cannot use sync mode flags on --continue without v2 state; continue without them or abort and start a new run"

// syncTriggerFlags is the new-mode trigger set (spec §3.3), in the source order
// of §3.1. `--push`, `--test`, `--verbose`, `--continue`, and `--abort` are not
// triggers.
var syncTriggerFlags = []string{"fetch", "no-fetch", "full", "local-only", "only", "from"}

// syncPresenceFlags is the closed key set of the presence map.
var syncPresenceFlags = []string{"fetch", "no-fetch", "full", "local-only", "only", "from", "push"}

// resolveSyncPolicy reads the six axis/scope flags plus `--push` through
// cmd.Flags().Changed and produces the frozen decision of one run. It performs
// the pure command-line checks I1-I6, then I7 (only when a trigger flag is
// present), then I8 — in that exact order, and it is called before the mode
// dispatch so both modes reject identically (spec §3.6 step 3).
func resolveSyncPolicy(cmd *cobra.Command, mode internal.WorkspaceMode) (internal.SyncRunPolicy, bool, map[string]bool, error) {
	changed := make(map[string]bool, len(syncPresenceFlags))
	for _, name := range syncPresenceFlags {
		changed[name] = cmd.Flags().Changed(name)
	}

	newMode := false
	for _, name := range syncTriggerFlags {
		if changed[name] {
			newMode = true
			break
		}
	}

	var policy internal.SyncRunPolicy

	// I1-I3 — mutual exclusion.
	if changed["fetch"] && changed["no-fetch"] {
		return policy, newMode, changed, fmt.Errorf("--fetch and --no-fetch are mutually exclusive")
	}
	if changed["full"] && changed["local-only"] {
		return policy, newMode, changed, fmt.Errorf("--full and --local-only are mutually exclusive")
	}
	if changed["only"] && changed["from"] {
		return policy, newMode, changed, fmt.Errorf("--only and --from are mutually exclusive")
	}

	// I4 — axis selectors are presence-only; an explicit false is refused.
	for _, rule := range []struct {
		name string
		msg  string
	}{
		{"fetch", "--fetch does not take an explicit value; use --no-fetch to disable automatic fetch"},
		{"no-fetch", "--no-fetch does not take an explicit value; use --fetch to enable automatic fetch"},
		{"full", "--full does not take an explicit value; use --local-only to restrict propagation"},
		{"local-only", "--local-only does not take an explicit value; use --full to advance anchors"},
	} {
		if changed[rule.name] && !syncBoolFlag(cmd, rule.name) {
			return policy, newMode, changed, fmt.Errorf("%s", rule.msg)
		}
	}

	only := syncStringFlag(cmd, "only")
	from := syncStringFlag(cmd, "from")

	// I5/I6 — an explicitly supplied empty selector is refused.
	if changed["only"] && only == "" {
		return policy, newMode, changed, fmt.Errorf("--only requires a stack entry name")
	}
	if changed["from"] && from == "" {
		return policy, newMode, changed, fmt.Errorf("--from requires a stack entry name")
	}

	cont := syncBoolFlag(cmd, "continue")
	abort := syncBoolFlag(cmd, "abort")

	// I7 before I8 (spec §3.5). Without a trigger flag I7 is deferred to the
	// state-discrimination step, where legacy and new-mode state can be told
	// apart.
	if cont && abort && newMode {
		return policy, newMode, changed, errSyncContinueAbort()
	}
	if abort && newMode && !cont {
		return policy, newMode, changed, fmt.Errorf("--abort cannot be combined with %s; abort is defined by the persisted run", syncChangedTriggerList(changed))
	}

	policy.Fetch = internal.SyncFetchEnabled
	if mode == internal.ModeCheckout {
		policy.Fetch = internal.SyncFetchDisabled
	}
	if changed["fetch"] {
		policy.Fetch = internal.SyncFetchEnabled
	}
	if changed["no-fetch"] {
		policy.Fetch = internal.SyncFetchDisabled
	}

	policy.Propagation = internal.SyncPropagationFull
	if changed["local-only"] {
		policy.Propagation = internal.SyncPropagationLocalOnly
	}

	policy.ScopeKind = internal.SyncScopeAll
	switch {
	case changed["only"]:
		policy.ScopeKind = internal.SyncScopeOne
		policy.Selector = only
	case changed["from"]:
		policy.ScopeKind = internal.SyncScopeSubtree
		policy.Selector = from
	}

	return policy, newMode, changed, nil
}

// errSyncContinueAbort is the single I7 literal.
func errSyncContinueAbort() error {
	return fmt.Errorf("--continue and --abort are mutually exclusive")
}

// syncTriggerFlagSupplied answers "did THIS invocation supply any of the six
// mode-trigger flags?" over the one owned `changed` map — the same predicate
// resolveSyncPolicy derives newMode from, extracted so the §12.8b cell-4
// interception can ask it directly. The interception sits ABOVE I20, so it
// composes I20's own sentence itself; syncTriggersNeedV2 keeps no cell-4 arm
// and takes no sentinel view (§13.6 rule 4a, §25.82).
func syncTriggerFlagSupplied(changed map[string]bool) bool {
	for _, name := range syncTriggerFlags {
		if changed[name] {
			return true
		}
	}
	return false
}

// syncChangedTriggerList renders the I8 `%s` operand: the changed trigger flag
// names, sorted, comma-joined, each with the `--` prefix.
func syncChangedTriggerList(changed map[string]bool) string {
	var names []string
	for _, name := range syncTriggerFlags {
		if changed[name] {
			names = append(names, "--"+name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func syncBoolFlag(cmd *cobra.Command, name string) bool {
	v, err := cmd.Flags().GetBool(name)
	if err != nil {
		return false
	}
	return v
}

func syncStringFlag(cmd *cobra.Command, name string) string {
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		return ""
	}
	return v
}

// ---------------------------------------------------------------------------
// External sync layout (spec §3.11)
// ---------------------------------------------------------------------------

// externalSyncLayout is the one physical layout of one external sync run.
// Every path the run touches is derived from these two fields and nothing else.
type externalSyncLayout struct {
	FeaturePath   string // <root>/<feature>
	WorktreesRoot string // <root>/<feature>/worktrees
}

// WorktreePath is the only worktree derivation on the external path.
func (l externalSyncLayout) WorktreePath(name string) string {
	return filepath.Join(l.WorktreesRoot, name)
}

// resolveExternalSyncLayout resolves the single root of one external sync run.
//
// It issues no Git command and calls no workspace or root resolver of its own:
// twsRoot is always the value of the caller's own single internal.TwsRoot()
// call. Its only probes are at most two ordinary `<path>/stack.yaml` reads, and
// there are zero of them whenever the two candidates agree.
func resolveExternalSyncLayout(ws internal.Workspace, twsRoot, feature string) (externalSyncLayout, error) {
	b := filepath.Join(twsRoot, feature)
	a, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return externalSyncLayout{}, err
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return newExternalSyncLayout(a), nil
	}
	if _, loadErr := internal.LoadStack(b); loadErr == nil {
		return newExternalSyncLayout(b), nil
	}
	if _, loadErr := internal.LoadStack(a); loadErr == nil {
		return newExternalSyncLayout(a), nil
	}
	return newExternalSyncLayout(b), nil
}

func newExternalSyncLayout(featurePath string) externalSyncLayout {
	return externalSyncLayout{
		FeaturePath:   featurePath,
		WorktreesRoot: filepath.Join(featurePath, "worktrees"),
	}
}

// ---------------------------------------------------------------------------
// Marker generation (spec §8.2)
// ---------------------------------------------------------------------------

// newSyncMarker generates a per-run sentinel marker. Recognition lives in
// package internal (isSyncMarker); this is the generation half.
func newSyncMarker() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate sync marker: %w", err)
	}
	return "tws-scoped-sync-" + hex.EncodeToString(buf) + ".lock", nil
}

// syncMarkerFn is the package-private generation seam. Package-cli tests
// override it; nothing exported ever does.
var syncMarkerFn = newSyncMarker

// newSyncOwnerToken generates the run ownership token recorded in both the
// guard and the payload.
func newSyncOwnerToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate sync owner token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// syncMarkerCollision is the mandatory I17 pre-flight: the generated marker may
// equal neither a StackEntry.Name nor an entry.GitBranch().
func syncMarkerCollision(stack internal.Stack, marker string) error {
	for _, entry := range stack.Branches {
		if entry.Name == marker || entry.GitBranch() == marker {
			return fmt.Errorf("refusing to start: generated sync marker %q collides with stack entry %q; re-run", marker, entry.Name)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// State discrimination (spec §3.6 steps 7-9, §8.6, §8.7)
// ---------------------------------------------------------------------------

type syncVerb string

const (
	syncVerbPlain    syncVerb = "plain"
	syncVerbContinue syncVerb = "continue"
	syncVerbAbort    syncVerb = "abort"
)

// classifySyncState is the thin wrapper over internal.ClassifyExternalSyncState.
// It passes AlwaysReadGuard: newMode and the package default liveness
// predicate, and it applies the I18 symlink refusals from the recorded facts —
// never a second Lstat.
func classifySyncState(featurePath string, newMode bool) (internal.SyncExternalState, error) {
	st := internal.ClassifyExternalSyncState(featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: newMode})
	if err := syncSymlinkRefusal(st, newMode); err != nil {
		return st, err
	}
	return st, nil
}

// syncSymlinkRefusal applies I18 over the classifier's recorded symlink facts.
func syncSymlinkRefusal(st internal.SyncExternalState, newMode bool) error {
	handlingV2 := st.Marker != "" || st.Payload != nil || st.PayloadErr != nil || st.HasGuardFile()
	if st.LegacySymlink && (newMode || handlingV2) {
		return syncSymlinkError(st.LegacyPath)
	}
	if st.PayloadSymlink {
		return syncSymlinkError(st.PayloadPath)
	}
	if st.GuardSymlink {
		return syncSymlinkError(st.GuardPath)
	}
	return nil
}

// syncStateSymlinkOnly reports whether err is exactly the I18 symlink
// refusal — the one classifier error the plan route describes instead of
// propagating (§12.5, §22.13m (i)).
func syncStateSymlinkOnly(err error) bool {
	return err != nil && strings.Contains(err.Error(), "runtime state path is a symlink")
}

func syncSymlinkError(path string) error {
	return fmt.Errorf("refusing to use %s: runtime state path is a symlink", path)
}

// syncCellLiveGuardRefusal is the pure extraction of syncCellRefusal's own
// first switch: the guard-precedence check applied on top of cells 2, 4, and
// 5, independent of each cell's own message. It is called both by
// syncCellRefusal, as its own first statement, and by the cell-4 guarded-
// sentinel interception (sync.go), which must apply the identical
// live-guard precedence before it ever consults the sentinel.
func syncCellLiveGuardRefusal(feature string, st internal.SyncExternalState, verb syncVerb) error {
	switch st.Cell {
	case 2, 4, 5:
		if st.GuardLive && st.Guard != nil {
			if verb == syncVerbAbort {
				return fmt.Errorf("a scoped sync is running for %q (pid %d); wait for it to exit before --abort", feature, st.Guard.PID)
			}
			return fmt.Errorf("a scoped sync is already running for %q (pid %d, started %s); wait for it or use --continue/--abort after it exits", feature, st.Guard.PID, st.Guard.Created)
		}
	}
	return nil
}

// syncCellRefusal returns the §8.7 message for a cell/verb pair, or nil when
// the verb may proceed in that cell. Guard precedence is applied on top of the
// cell for rows 2, 4, and 5 only.
func syncCellRefusal(verb syncVerb, feature string, layout externalSyncLayout, st internal.SyncExternalState) error {
	if err := syncCellLiveGuardRefusal(feature, st, verb); err != nil {
		return err
	}

	switch st.Cell {
	case 1, 7:
		return nil
	case 2:
		if verb == syncVerbAbort {
			return nil
		}
		failed := syncPayloadFailed(st)
		return fmt.Errorf("a scoped sync record survives without its state file for %q: it failed on %s (worktree %s) and that rebase was never aborted. Resolve or abort it there, then run: tws sync %s --abort",
			feature, failed, layout.WorktreePath(failed), feature)
	case 4:
		if verb == syncVerbAbort {
			return nil
		}
		return fmt.Errorf("a scoped sync left partial state for %q: it was interrupted either while starting up or while finishing, and this cannot be distinguished on disk; work may or may not have been done. Inspect the worktrees, then run: tws sync %s --abort",
			feature, feature)
	case 5:
		if verb == syncVerbPlain {
			return fmt.Errorf("a scoped sync is incomplete (failed on: %s); use --continue or --abort", syncPayloadFailed(st))
		}
		return nil
	case 3, 6, 9, 12:
		return fmt.Errorf("scoped sync state at %s is unreadable or uses an unsupported version (%s); inspect it and remove it manually — tws will not guess",
			st.PayloadPath, syncErrText(st.PayloadErr))
	case 8:
		legacyFailed := ""
		if st.Legacy != nil {
			legacyFailed = st.Legacy.FailedBranch
		}
		if verb == syncVerbAbort {
			return fmt.Errorf("refusing to clear two unfinished syncs at once for %q: a legacy sync failed on %s and a scoped sync failed on %s; inspect %s and %s and remove them explicitly",
				feature, legacyFailed, syncPayloadFailed(st), st.LegacyPath, st.PayloadPath)
		}
		return fmt.Errorf("two unfinished syncs are recorded for %q: a legacy sync failed on %s and a scoped sync failed on %s; resolve both before syncing (inspect %s and %s)",
			feature, legacyFailed, syncPayloadFailed(st), st.LegacyPath, st.PayloadPath)
	case 10:
		if verb == syncVerbAbort {
			return fmt.Errorf("sync state at %s is unreadable: %v; inspect and remove it manually", st.LegacyPath, syncErrText(st.LegacyErr))
		}
		return fmt.Errorf("sync state at %s is unreadable: %v", st.LegacyPath, syncErrText(st.LegacyErr))
	case 11:
		failed := syncPayloadFailed(st)
		if verb == syncVerbAbort {
			return fmt.Errorf("refusing to clear unreadable sync state at %s while a scoped sync record beside it is still unfinished: it failed on %s (worktree %s); inspect both and remove %s explicitly",
				st.LegacyPath, failed, layout.WorktreePath(failed), st.PayloadPath)
		}
		return fmt.Errorf("sync state at %s is unreadable, and a scoped sync record beside it failed on %s (worktree %s); resolve or abort that rebase, then remove %s manually — tws will not guess",
			st.LegacyPath, failed, layout.WorktreePath(failed), st.PayloadPath)
	}
	return nil
}

func syncPayloadFailed(st internal.SyncExternalState) string {
	if st.Payload == nil {
		return ""
	}
	return st.Payload.FailedBranch
}

func syncErrText(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

// syncTriggersNeedV2 is I20's cell-aware predicate (spec §3.5): a resumable
// cell whose subject cannot satisfy a --continue that also supplies a
// sync-mode trigger flag. Cell 1/7 (no v2 state at all, or a real legacy-only
// state) always need v2; cell 5 needs it exactly when the persisted payload
// is not itself a new-mode run. The caller retains its own `newMode &&`
// prefix, exactly mirroring internal.CheckoutTriggersNeedV2's own external
// `opts.NewMode &&` wrapping. This predicate is never reached on cell 4: the
// guarded-sentinel interception fully owns that cell's own flag-conflict
// check before I20 is ever consulted.
func syncTriggersNeedV2(state internal.SyncExternalState) bool {
	switch state.Cell {
	case 1, 7:
		return true
	case 5:
		return !internal.PayloadNewMode(state.Payload)
	default:
		return false
	}
}

// syncStateRefusesContinueAbort is the pure extraction of the deferred-I7
// state predicate: a --continue --abort invocation that supplied no trigger
// flag is refused whenever the subject carries any new-mode-recognizable
// state at all — a marker or a payload — exactly as today. The caller
// retains its own `cont && abort &&` prefix.
func syncStateRefusesContinueAbort(state internal.SyncExternalState) bool {
	return state.Marker != "" || state.Payload != nil
}

// ---------------------------------------------------------------------------
// New-mode setup, teardown, and failure persistence (spec §8.5)
// ---------------------------------------------------------------------------

// syncRunStateBirth carries the guarded envelope's birth-time overrides for
// setupSyncRunState (§13.1, §13.6 rule 2). Its zero value changes nothing —
// every shipped (unguarded) call site passes it unchanged and the payload it
// produces is byte-identical to today's. StateVersion is applied only when
// non-zero: the birth decision of §13.6 rule 2 is made once by the caller,
// exactly as SaveSyncRunState's own doc comment requires, and is never
// silently defaulted here. Route/MaxPerEntry/MaxTotal are applied
// unconditionally, because their own Go zero values ("" and nil) are already
// what NewSyncRunState leaves them at, so an unguarded caller's zero-value
// birth is a true no-op for those three fields too.
type syncRunStateBirth struct {
	StateVersion int
	Route        string
	MaxPerEntry  *int
	MaxTotal     *int
}

// setupSyncRunState performs §8.5 setup: guard, sentinel, payload — in that
// exact order, and it is the run's first side effect.
func setupSyncRunState(layout externalSyncLayout, feature, marker, token string, sel internal.SyncSelection, push bool, testCommand, validationSource string, birth syncRunStateBirth) (*internal.SyncRunState, error) {
	if err := internal.ClaimSyncRunGuard(layout.FeaturePath, token); err != nil {
		return nil, err
	}
	if err := syncStepHook(internal.SyncStageInitializing, 0); err != nil {
		return nil, err
	}

	sentinel := internal.NewSyncState()
	sentinel.FailedBranch = marker
	sentinel.Pending = []string{}
	sentinel.Completed = []string{}
	sentinel.Skipped = []string{}
	if err := internal.SaveSyncState(layout.FeaturePath, sentinel); err != nil {
		internal.ReleaseSyncRunGuard(layout.FeaturePath)
		return nil, fmt.Errorf("write sync sentinel: %w", err)
	}
	if err := syncStepHook(internal.SyncStageInitializing, 1); err != nil {
		return nil, err
	}

	payload := internal.NewSyncRunState(feature, marker, token, sel.Policy)
	payload.Selected = sel.SelectedNames()
	payload.Pending = append([]string(nil), payload.Selected...)
	payload.Push = push
	payload.TestCommand = testCommand
	payload.ValidationSource = validationSource
	payload.Repos = append([]string(nil), sel.Repos...)
	if birth.StateVersion != 0 {
		payload.StateVersion = birth.StateVersion
	}
	payload.Route = birth.Route
	payload.MaxReplayPerEntry = birth.MaxPerEntry
	payload.MaxReplayTotal = birth.MaxTotal
	if err := internal.SaveSyncRunState(layout.FeaturePath, payload); err != nil {
		internal.DeleteSyncState(layout.FeaturePath)
		internal.ReleaseSyncRunGuard(layout.FeaturePath)
		return nil, fmt.Errorf("write scoped sync state: %w", err)
	}
	if err := syncStepHook(internal.SyncStageInitializing, 2); err != nil {
		return nil, err
	}
	return payload, nil
}

// clearSyncRunState performs §8.5 teardown in the exact reverse order:
// payload, sentinel, guard. A no-flag run keeps today's literal sentinel-free
// DeleteSyncState and can never fail.
//
// A new-mode teardown stops at the first failing step hook and returns that
// error, so an interruption between two teardown steps leaves exactly the
// residue that step ordering implies: {sentinel, guard} after step 0 and
// {guard} after step 1. Callers must propagate the error and must not clear
// any later artifact themselves.
func clearSyncRunState(featurePath string, newMode bool) error {
	if !newMode {
		internal.DeleteSyncState(featurePath)
		return nil
	}
	internal.DeleteSyncRunState(featurePath)
	if err := syncStepHook(internal.SyncStageFinalizing, 0); err != nil {
		return err
	}
	internal.DeleteSyncState(featurePath)
	if err := syncStepHook(internal.SyncStageFinalizing, 1); err != nil {
		return err
	}
	internal.ReleaseSyncRunGuard(featurePath)
	return nil
}

// ---------------------------------------------------------------------------
// Guarded-legacy setup, teardown, and upgrade (spec §12, §13.7a)
// ---------------------------------------------------------------------------

// guardedLegacyCarry is the continuation-only provenance a cell-7 guarded
// legacy setup must not lose (§13.6 rule 2a): the prior legacy state's
// Completed list verbatim in its persisted order, its FailedBranch, and its
// persisted pending COUNT — the number the shipped resume prose prints. The
// zero value is the fresh route, which carries nothing. The prior document's
// BYTES are deliberately absent: §13.2a step 1 makes the capture
// setupGuardedLegacyRunState's own responsibility, so no caller can hand it
// a stale or re-marshalled snapshot of a file the setup is about to replace.
type guardedLegacyCarry struct {
	Completed         []string
	FailedBranch      string
	PriorPendingCount int
}

// carriedGuardedLegacyState projects an already-loaded legacy state into the
// carry the cell-7 continuation arm supplies. It reads no file: the caller
// has already decoded the state for its own gates, and the bytes the
// rollback needs are captured by the setup itself.
func carriedGuardedLegacyState(legacy *internal.SyncState) guardedLegacyCarry {
	if legacy == nil {
		return guardedLegacyCarry{}
	}
	return guardedLegacyCarry{
		Completed:         append([]string(nil), legacy.Completed...),
		FailedBranch:      legacy.FailedBranch,
		PriorPendingCount: len(legacy.Pending),
	}
}

// captureGuardedLegacySyncState is §13.2a step 1: one os.ReadFile of
// .sync-state.yaml into the bytes the rollback will restore. The bytes the
// classifier already decoded are NOT reused — restoration needs the file's
// bytes, not a re-marshalled struct. fs.ErrNotExist is the FRESH cell and
// yields (nil, nil); every other error is returned wrapped, marker-free, so
// the arm refuses before it has written or claimed anything.
func captureGuardedLegacySyncState(featurePath string) ([]byte, error) {
	data, err := os.ReadFile(internal.SyncStatePath(featurePath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sync state: %w", err)
	}
	return data, nil
}

// guardedLegacySetupProgress records exactly what setupGuardedLegacyRunState
// created, so the rollback undoes what happened rather than what it assumes
// from a step index (§13.2a).
type guardedLegacySetupProgress struct {
	Guard    bool // step 2 claimed the run guard
	Sentinel bool // step 3 installed the backup sentinel
	Payload  bool // step 4 wrote the v3 payload
}

// guardedLegacySetupPending computes a guarded legacy sentinel's and
// payload's initial pending set: every entry of universe on a fresh run, or
// every entry of universe not already in carry.Completed on a cell-7
// upgrade — the carried FailedBranch is deliberately NOT excluded, since its
// own rebase is not yet known-good at the moment of the upgrade.
func guardedLegacySetupPending(universe []string, carry guardedLegacyCarry) []string {
	done := make(map[string]bool, len(carry.Completed))
	for _, name := range carry.Completed {
		done[name] = true
	}
	pending := make([]string, 0, len(universe))
	for _, name := range universe {
		if !done[name] {
			pending = append(pending, name)
		}
	}
	return pending
}

// newGuardedLegacySentinel assembles the one document §13.2a step 3 writes.
// The embedded SyncState carries the marker exactly as an unguarded sentinel
// does, so InspectGuardedLegacySentinel's own marker-shape check passes
// identically; the guarded envelope fields record this invocation's own
// identity, limits, and validation provenance, plus — on an upgrade — the
// captured prior document byte-for-byte with its SHA-256, which is what
// makes every crash window of §13.2a recoverable.
func newGuardedLegacySentinel(feature, marker, token string, universe, pending []string, birth syncRunStateBirth, push bool, testCommand, validationSource string, carry guardedLegacyCarry, prior []byte) *internal.GuardedLegacySentinel {
	s := &internal.GuardedLegacySentinel{
		SyncState:           *internal.NewSyncState(),
		GuardedStateVersion: internal.GuardedLegacySentinelVersion,
		Route:               internal.RouteLegacy,
		Feature:             feature,
		Marker:              marker,
		OwnerToken:          token,
		CreatedAt:           time.Now().UTC().Format(time.RFC3339),
		WriterPID:           os.Getpid(),
		MaxReplayPerEntry:   birth.MaxPerEntry,
		MaxReplayTotal:      birth.MaxTotal,
		Push:                push,
		TestCommand:         testCommand,
		ValidationSource:    validationSource,
		Universe:            append([]string(nil), universe...),
		PendingIntent:       append([]string(nil), pending...),
		CarriedCompleted:    append([]string(nil), carry.Completed...),
		CarriedFailed:       carry.FailedBranch,
		PriorPendingCount:   carry.PriorPendingCount,
		PriorLegacyPresent:  len(prior) > 0,
	}
	s.FailedBranch = marker
	s.Pending = []string{}
	s.Completed = []string{}
	s.Skipped = []string{}
	if len(prior) > 0 {
		sum := sha256.Sum256(prior)
		s.PriorLegacySHA256 = hex.EncodeToString(sum[:])
		s.PriorLegacyBase64 = base64.StdEncoding.EncodeToString(prior)
	}
	return s
}

// newGuardedLegacyPayload assembles the state_version: 3, route: legacy
// envelope §13.2a step 4 writes. Its policy members are the legacy route's
// own defaults — a legacy run has no sync-mode policy of its own — and its
// resume-bearing members are the four carried values of §13.2a: Selected is
// the full-stack legacy universe in this invocation's one TopoSort order,
// Pending the remaining set of §13.3 in that order, Completed the legacy
// state's Completed verbatim and FailedBranch its FailedBranch verbatim. The
// legacy state's Skipped list has no payload counterpart and is deliberately
// not carried: the shipped resume builds `done` from Completed ∪
// {FailedBranch} alone, and the list nevertheless survives verbatim inside
// the sentinel's byte-exact backup.
func newGuardedLegacyPayload(feature, marker, token string, universe, pending []string, birth syncRunStateBirth, push bool, testCommand, validationSource string, carry guardedLegacyCarry) *internal.SyncRunState {
	payload := internal.NewSyncRunState(feature, marker, token, internal.SyncRunPolicy{})
	payload.StateVersion = internal.SyncRunStateGuardedVersion
	payload.Route = internal.RouteLegacy
	payload.MaxReplayPerEntry = birth.MaxPerEntry
	payload.MaxReplayTotal = birth.MaxTotal
	payload.Push = push
	payload.TestCommand = testCommand
	payload.ValidationSource = validationSource
	payload.Selected = append([]string{}, universe...)
	payload.Pending = append([]string{}, pending...)
	payload.Completed = append([]string{}, carry.Completed...)
	payload.FailedBranch = carry.FailedBranch
	return payload
}

// syncStateResidue is what a rollback actually left on disk, measured rather
// than inferred from the step it stopped at (§12.2c rule 4a).
type syncStateResidue struct{ Payload, Sentinel, Guard bool }

// Empty reports a rollback that undid everything it created.
func (r syncStateResidue) Empty() bool { return !r.Payload && !r.Sentinel && !r.Guard }

// String renders the PRESENT members in the fixed order payload, sentinel,
// guard: "{payload, sentinel, guard}" | "{sentinel, guard}" | "{guard}" |
// "{sentinel}" | "{}" — any subset, always that order.
func (r syncStateResidue) String() string {
	parts := make([]string, 0, 3)
	if r.Payload {
		parts = append(parts, "payload")
	}
	if r.Sentinel {
		parts = append(parts, "sentinel")
	}
	if r.Guard {
		parts = append(parts, "guard")
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// syncResidueError composes the one sentence §12.2c rule 4 and §13.2a
// require when a rollback itself fails: the failing writer's own shipped
// sentence, the measured residue, and the exact recovery command. It is
// marker-free — the guard did not refuse, a write failed.
func syncResidueError(cause error, residue syncStateResidue, feature string) error {
	return fmt.Errorf("%s; recovery state preserved: %s — clear it with: tws sync %s --abort", cause.Error(), residue.String(), feature)
}

// rollbackGuardedRunState removes exactly the three artefacts a guarded
// fresh claim created, in the shipped teardown order, propagating the first
// failure (§12.2c rule 2). It stops at the first failing step — remover or
// injected hook alike — and returns the residue actually left on disk: the
// failing step's own artefact where the remover failed, plus every artefact
// of the steps it never attempted. It touches no pre-existing artefact,
// aborts no rebase and runs no restoration.
func rollbackGuardedRunState(featurePath string) (syncStateResidue, error) {
	residue := syncStateResidue{Payload: true, Sentinel: true, Guard: true}
	if err := internal.RemoveSyncRunState(featurePath); err != nil {
		return residue, err
	}
	residue.Payload = false
	if err := syncStepHook(internal.SyncStageFinalizing, 0); err != nil {
		return residue, err
	}
	if err := internal.RemoveSyncState(featurePath); err != nil {
		return residue, err
	}
	residue.Sentinel = false
	if err := syncStepHook(internal.SyncStageFinalizing, 1); err != nil {
		return residue, err
	}
	if err := internal.RemoveSyncRunGuard(featurePath); err != nil {
		return residue, err
	}
	residue.Guard = false
	return residue, nil
}

// rollbackGuardedLegacyRunState undoes steps 4, 3 and 2 of §13.2a in REVERSE
// CREATION ORDER — payload, then the sentinel replacement, then the guard —
// stopping at the first failure and returning the residue actually left on
// disk. Releasing the guard LAST is load-bearing: the prior document is put
// back while this invocation still holds the run guard, so no other new
// binary can claim it and start writing over a half-undone directory. The
// sentinel step is ALWAYS conditional and never an unconditional removal:
// RestoreSyncStateBytes(featurePath, prior, sentinel) where prior bytes were
// captured, and RemoveSyncStateIfUnchanged(featurePath, sentinel) where
// there were none (the fresh route), so it can only ever undo the exact
// bytes THIS invocation installed. It never aborts a rebase, never touches a
// pre-existing artefact it did not create, and never re-attempts a step it
// has already failed.
func rollbackGuardedLegacyRunState(featurePath string, prior, sentinel []byte, made guardedLegacySetupProgress) (syncStateResidue, error) {
	residue := syncStateResidue{Payload: made.Payload, Sentinel: made.Sentinel, Guard: made.Guard}
	if made.Payload {
		if err := internal.RemoveSyncRunState(featurePath); err != nil {
			return residue, err
		}
		residue.Payload = false
	}
	if made.Sentinel {
		var err error
		if len(prior) > 0 {
			err = internal.RestoreSyncStateBytes(featurePath, prior, sentinel)
		} else {
			err = internal.RemoveSyncStateIfUnchanged(featurePath, sentinel)
		}
		if err != nil {
			return residue, err
		}
		residue.Sentinel = false
	}
	if made.Guard {
		if err := internal.RemoveSyncRunGuard(featurePath); err != nil {
			return residue, err
		}
		residue.Guard = false
	}
	return residue, nil
}

// guardedLegacyUndo is the exact undo token §13.2a's rollback needs: the
// bytes captured at step 1, the bytes step 3 installed, and what was really
// created. setupGuardedLegacyRunState returns it so a refusal raised AFTER a
// successful setup — the guarded legacy continuation's own pre-mutation
// guard/JIT seam — can perform the same CONDITIONAL undo the setup would
// have performed for its own failure, rather than falling back on
// rollbackGuardedRunState's unconditional RemoveSyncState, which would
// destroy the operator's captured legacy document instead of restoring it
// (§13.2a, §22.24i (ix-a)).
type guardedLegacyUndo struct {
	prior    []byte
	sentinel []byte
	made     guardedLegacySetupProgress
}

// rollback performs the conditional reverse-order undo of §13.2a for a
// refusal raised above the first Git mutation but below a successful setup.
func (u guardedLegacyUndo) rollback(featurePath, feature string, refusal *internal.PlanGuardRefusalError) error {
	residue, err := rollbackGuardedLegacyRunState(featurePath, u.prior, u.sentinel, u.made)
	if err != nil {
		preserved := *refusal
		preserved.StatePreserved = true
		preserved.Detail = fmt.Sprintf("%s; recovery state preserved: %s — clear it with: tws sync %s --abort", refusal.Detail, residue.String(), feature)
		return &preserved
	}
	return refusal
}

// setupGuardedLegacyRunState writes the guarded LEGACY envelope over the
// exact four-step, crash-safe order of §13.2a: capture the CURRENT
// .sync-state.yaml bytes (absent ⇒ nil, which is every fresh run, since a
// plain run over a real legacy state is refused above this point) →
// ClaimSyncRunGuard (the shipped O_EXCL claim, FIRST, exactly as
// setupSyncRunState orders it, so two new binaries racing over one feature
// cannot both reach the replacement) → the CONDITIONAL atomic write of the
// BACKUP SENTINEL over that same path (an exclusive create where nothing was
// captured, a compare-and-swap over the captured bytes where something was)
// → the atomic state_version: 3, route: legacy payload write → return the
// payload. The steps fire the shipped SyncStageInitializing hooks, which
// name ARTEFACTS (0 = guard, 1 = sentinel, 2 = payload) and are therefore
// fired here in the order 3 → 0 → 1 → 2; the capture adds index 3.
//
// The captured bytes are owned by this function, not by its caller: on any
// failure at or below the claim — a writer error, a claim refusal or a
// boundary hook that RETURNS an error — it rolls back through
// rollbackGuardedLegacyRunState, so a failed setup leaves a byte-identical
// legacy state and no payload, sentinel or guard, and a failed ROLLBACK
// returns the measured residue instead of pretending. Residue is reachable
// only from a process that died, never from a returned error.
func setupGuardedLegacyRunState(layout externalSyncLayout, feature, marker, token string, universe, pending []string, push bool, testCommand, validationSource string, birth syncRunStateBirth, carry guardedLegacyCarry) (*internal.SyncRunState, guardedLegacyUndo, error) {
	featurePath := layout.FeaturePath

	// Step 1 — capture. Nothing has been written or claimed yet, so a
	// failure here leaves the run exactly as found.
	prior, err := captureGuardedLegacySyncState(featurePath)
	if err != nil {
		return nil, guardedLegacyUndo{}, err
	}
	if err := syncStepHook(internal.SyncStageInitializing, 3); err != nil {
		return nil, guardedLegacyUndo{}, err
	}

	sentinel := newGuardedLegacySentinel(feature, marker, token, universe, pending, birth, push, testCommand, validationSource, carry, prior)
	sentinelBytes, err := internal.MarshalGuardedLegacySentinel(sentinel)
	if err != nil {
		return nil, guardedLegacyUndo{}, err
	}

	made := guardedLegacySetupProgress{}
	fail := func(cause error) (*internal.SyncRunState, guardedLegacyUndo, error) {
		residue, rollbackErr := rollbackGuardedLegacyRunState(featurePath, prior, sentinelBytes, made)
		if rollbackErr != nil {
			return nil, guardedLegacyUndo{}, syncResidueError(rollbackErr, residue, feature)
		}
		return nil, guardedLegacyUndo{}, cause
	}

	// Step 2 — claim. This arm's first write and its mutual-exclusion point;
	// nothing else exists yet, so its own failure rolls nothing back.
	if err := internal.ClaimSyncRunGuard(featurePath, token); err != nil {
		return nil, guardedLegacyUndo{}, err
	}
	made.Guard = true
	if err := syncStepHook(internal.SyncStageInitializing, 0); err != nil {
		return fail(err)
	}

	// Step 3 — the conditional backup sentinel.
	if err := internal.SaveGuardedLegacySentinel(featurePath, sentinel, prior); err != nil {
		if errors.Is(err, internal.ErrSyncStateChanged) {
			return fail(fmt.Errorf("sync state at %s changed while starting a guarded resume; re-run: tws sync %s --continue", internal.SyncStatePath(featurePath), feature))
		}
		return fail(fmt.Errorf("write sync sentinel: %w", err))
	}
	made.Sentinel = true
	if err := syncStepHook(internal.SyncStageInitializing, 1); err != nil {
		return fail(err)
	}

	// Step 4 — the v3 payload.
	payload := newGuardedLegacyPayload(feature, marker, token, universe, pending, birth, push, testCommand, validationSource, carry)
	if err := internal.SaveSyncRunState(featurePath, payload); err != nil {
		return fail(fmt.Errorf("write scoped sync state: %w", err))
	}
	made.Payload = true
	if err := syncStepHook(internal.SyncStageInitializing, 2); err != nil {
		return fail(err)
	}
	return payload, guardedLegacyUndo{prior: prior, sentinel: sentinelBytes, made: made}, nil
}

// claimOrReclaimGuardedLegacyGuard performs §13.2a step 2 for the cell-4
// recovery arm, where the sentinel already exists. The choice follows the
// SNAPSHOT the classifier took — whether a `.sync-run.lock` was there at all
// — and never the provenance of the residue: crash window 2 and the
// `{sentinel, guard}` rollback residue reach the same cell and must answer
// identically. A present guard is RECLAIMED through the shipped
// compare-and-swap ladder (which refuses a live owner, foreign or
// self-recorded alike); an absent one — an operator removed the lock by hand
// — is CLAIMED through the shipped O_EXCL ladder.
func claimOrReclaimGuardedLegacyGuard(featurePath, token string, guardPresent bool) error {
	if guardPresent {
		return internal.ReclaimSyncRunGuard(featurePath, token)
	}
	return internal.ClaimSyncRunGuard(featurePath, token)
}

// writeGuardedLegacyRecoveryPayload performs §13.2a step 4 alone for the
// cell-4 recovery arm: the state_version: 3, route: legacy payload built
// from the sentinel's own verified intent block. It writes no sentinel — the
// backup sentinel is already on disk and is exactly what this arm is
// recovering — and it re-plans nothing.
func writeGuardedLegacyRecoveryPayload(featurePath string, sentinel *internal.GuardedLegacySentinel) (*internal.SyncRunState, error) {
	birth := syncRunStateBirth{
		StateVersion: internal.SyncRunStateGuardedVersion,
		Route:        internal.RouteLegacy,
		MaxPerEntry:  sentinel.MaxReplayPerEntry,
		MaxTotal:     sentinel.MaxReplayTotal,
	}
	carry := guardedLegacyCarry{
		Completed:         append([]string(nil), sentinel.CarriedCompleted...),
		FailedBranch:      sentinel.CarriedFailed,
		PriorPendingCount: sentinel.PriorPendingCount,
	}
	payload := newGuardedLegacyPayload(sentinel.Feature, sentinel.Marker, sentinel.OwnerToken,
		sentinel.Universe, sentinel.PendingIntent, birth, sentinel.Push,
		sentinel.TestCommand, sentinel.ValidationSource, carry)
	if err := syncStepHook(internal.SyncStageInitializing, 2); err != nil {
		return nil, err
	}
	if err := internal.SaveSyncRunState(featurePath, payload); err != nil {
		return nil, fmt.Errorf("write scoped sync state: %w", err)
	}
	return payload, nil
}

// rollbackGuardedFreshRefusal is §12.2c rule 2/3/4 applied to a guarded
// fresh route's own pre-mutation refusal: the rollback runs FIRST, so the
// marker line describes the state the operator will actually find, and a
// cleanup failure is reported honestly with the measured residue instead of
// being swallowed.
func rollbackGuardedFreshRefusal(featurePath, feature string, refusal *internal.PlanGuardRefusalError) error {
	residue, err := rollbackGuardedRunState(featurePath)
	if err != nil {
		preserved := *refusal
		preserved.StatePreserved = true
		preserved.Detail = fmt.Sprintf("%s; recovery state preserved: %s — clear it with: tws sync %s --abort", refusal.Detail, residue.String(), feature)
		return &preserved
	}
	return refusal
}

// upgradeGuardedSyncRunState upgrades an existing, unguarded new-mode payload
// (cell 5, arm b) to the guarded envelope in place: it records
// StateVersion/Route/the newly supplied limits and otherwise leaves
// Selected/Completed/Pending/FailedBranch/Stage untouched, so the resumed run
// continues at exactly the point it was interrupted. It never touches the
// sentinel beside the payload: GuardedLegacySentinelResumable's own
// cell-4-only dispatch never consults a new-mode run's sentinel, so an
// upgraded payload's crash-orphan recovery is unchanged from today's plain
// cell-4 handling.
func upgradeGuardedSyncRunState(featurePath string, payload *internal.SyncRunState, birth syncRunStateBirth) error {
	payload.StateVersion = birth.StateVersion
	payload.Route = internal.RouteNewMode
	payload.MaxReplayPerEntry = birth.MaxPerEntry
	payload.MaxReplayTotal = birth.MaxTotal
	return internal.SaveSyncRunState(featurePath, payload)
}

// saveScopedSyncFailure is the new-mode failure persistence path. It writes the
// payload only: saveIncompleteSync is never called by a new-mode run, because
// it would overwrite the sentinel with a resolvable name.
func saveScopedSyncFailure(featurePath string, payload *internal.SyncRunState, failed string, completed []string) {
	if payload == nil {
		return
	}
	payload.Stage = internal.SyncStageFailed
	payload.FailedBranch = failed
	payload.Completed = append([]string(nil), completed...)
	done := make(map[string]bool, len(completed))
	for _, name := range completed {
		done[name] = true
	}
	pending := make([]string, 0, len(payload.Selected))
	for _, name := range payload.Selected {
		if name != failed && !done[name] {
			pending = append(pending, name)
		}
	}
	payload.Pending = pending
	_ = internal.SaveSyncRunState(featurePath, payload)
}

// saveScopedPushFailure is the new-mode push failure persistence path. Unlike
// saveScopedSyncFailure it never recomputes `pending` or `completed`: the
// rebases already succeeded and only the push of `failed` is outstanding, so
// the payload keeps everything --continue needs to retry exactly the unpushed
// entries.
func saveScopedPushFailure(featurePath string, payload *internal.SyncRunState, failed string) {
	if payload == nil {
		return
	}
	payload.Stage = internal.SyncStageFailed
	payload.FailedBranch = failed
	_ = internal.SaveSyncRunState(featurePath, payload)
}

// ---------------------------------------------------------------------------
// Test-only seams (§23.1 items 2 and 2a). Both are nil in production, inject
// no verdict, and exist so a second process can really mutate the feature
// directory inside a window this command otherwise closes instantly.
// ---------------------------------------------------------------------------

// syncClassifierBarrierHook is §23.1 seam 2a: the window between
// classifySyncState and the cell-4 guarded-sentinel interception, so a test
// can create, remove or replace .sync-state.yaml between the two reads.
var syncClassifierBarrierHook func(featurePath string) error

func syncClassifierBarrier(featurePath string) error {
	if syncClassifierBarrierHook != nil {
		return syncClassifierBarrierHook(featurePath)
	}
	return nil
}

// syncSnapshotBarrierHook is §23.1 seam 2: the window between the run's one
// state snapshot (InspectExternalPlanState, inside InspectExternalPlan) and
// the guard seam that judges the document built from it.
var syncSnapshotBarrierHook func(featurePath string) error

func syncSnapshotBarrier(featurePath string) error {
	if syncSnapshotBarrierHook != nil {
		return syncSnapshotBarrierHook(featurePath)
	}
	return nil
}

// syncReclassifyCount counts §12.8b vanished-state re-classifications. The
// contract is "at most once per invocation", and this counter is what makes
// that assertion executable rather than aspirational.
var syncReclassifyCount atomic.Int64

// syncStepHook is the package-cli side of the external crash-injection seam.
func syncStepHook(stage internal.SyncRunStage, index int) error {
	if internal.SyncStepHook != nil {
		return internal.SyncStepHook(stage, index)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Header and no-op output (spec §3.7)
// ---------------------------------------------------------------------------

// printSyncModeHeaderTo is the writer-taking twin of printSyncModeHeader —
// the single body this feature's plan route and every executing route share.
// Its bytes are identical to internal.printSyncModeHeaderTo's, which serves
// the checkout half.
func printSyncModeHeaderTo(w io.Writer, p internal.SyncRunPolicy) {
	_, _ = fmt.Fprintf(w, "Sync mode: fetch=%s propagation=%s scope=%s\n", p.Fetch, p.Propagation, p.ScopeLabel())
}

// printSyncModeHeader prints the §3.7 header to stdout. It is now a one-line
// wrapper over printSyncModeHeaderTo, byte-identical to the pre-change body.
func printSyncModeHeader(p internal.SyncRunPolicy) {
	printSyncModeHeaderTo(os.Stdout, p)
}

// syncAnchorNoOpLine is the `[-]` line of the local-only no-op block. It reuses
// formatSyncStatus with the sentence in the mode slot; package internal repeats
// the same literal and the two must stay byte-identical.
func syncAnchorNoOpLine(name string) string {
	return formatSyncStatus(name, "no in-stack parent edge to propagate", "skipped")
}

// ---------------------------------------------------------------------------
// Completion (spec §3.8)
// ---------------------------------------------------------------------------

// syncEntryCompletion offers the non-archived logical entry names of the
// feature named by args[0]. Every error degrades to "no candidates": it never
// errors, never prints, and never exits.
func syncEntryCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	layout, err := resolveExternalSyncLayout(ws, internal.TwsRoot(), args[0])
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	stack, err := internal.LoadStack(layout.FeaturePath)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, entry := range stack.Branches {
		if entry.Archived {
			continue
		}
		names = append(names, entry.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
