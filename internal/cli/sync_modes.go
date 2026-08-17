package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

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

func syncSymlinkError(path string) error {
	return fmt.Errorf("refusing to use %s: runtime state path is a symlink", path)
}

// syncCellRefusal returns the §8.7 message for a cell/verb pair, or nil when
// the verb may proceed in that cell. Guard precedence is applied on top of the
// cell for rows 2, 4, and 5 only.
func syncCellRefusal(verb syncVerb, feature string, layout externalSyncLayout, st internal.SyncExternalState) error {
	switch st.Cell {
	case 2, 4, 5:
		if st.GuardLive && st.Guard != nil {
			if verb == syncVerbAbort {
				return fmt.Errorf("a scoped sync is running for %q (pid %d); wait for it to exit before --abort", feature, st.Guard.PID)
			}
			return fmt.Errorf("a scoped sync is already running for %q (pid %d, started %s); wait for it or use --continue/--abort after it exits", feature, st.Guard.PID, st.Guard.Created)
		}
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

// ---------------------------------------------------------------------------
// New-mode setup, teardown, and failure persistence (spec §8.5)
// ---------------------------------------------------------------------------

// setupSyncRunState performs §8.5 setup: guard, sentinel, payload — in that
// exact order, and it is the run's first side effect.
func setupSyncRunState(layout externalSyncLayout, feature, marker, token string, sel internal.SyncSelection, push bool, testCommand, validationSource string) (*internal.SyncRunState, error) {
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

// printSyncModeHeader prints the §3.7 header. Its bytes are identical to
// internal.printSyncModeHeader's, which serves the checkout half.
func printSyncModeHeader(p internal.SyncRunPolicy) {
	fmt.Printf("Sync mode: fetch=%s propagation=%s scope=%s\n", p.Fetch, p.Propagation, p.ScopeLabel())
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
