package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// agentStatusSchema is the version of the `tws status --json` document. It is
// bumped only for a breaking change (a removed key, a changed type, a narrowed
// enum). Adding a key, an enum value, or an issue code is additive.
const agentStatusSchema = 1

// ---------- Axes ----------

// RuntimePresence answers "is a tws-owned runtime alive?". It is derived only
// from records tws itself wrote plus one tmux inventory; a runtime tws did not
// launch is `absent`, never inferred.
type RuntimePresence string

const (
	PresencePresent RuntimePresence = "present"
	PresenceAbsent  RuntimePresence = "absent"
	PresenceStale   RuntimePresence = "stale"
	PresenceUnknown RuntimePresence = "unknown"
)

// AgentState answers "what is the agent doing?". It is a permanently separate
// axis from RuntimePresence and is always AgentStateUnknown at this version:
// tws launches agents but does not observe their turns.
//
// A later provider populates it. Note that the provider's `ready` means "the
// agent finished its turn and awaits input"; it is NOT tws `idle`, which is
// the structural residue of "no warnings and no live runtime".
type AgentState string

const (
	AgentStateWorking AgentState = "working"
	AgentStateReady   AgentState = "ready"
	AgentStateBlocked AgentState = "blocked"
	AgentStateDone    AgentState = "done"
	AgentStateUnknown AgentState = "unknown"
)

// AttentionStatus is the structural rollup. It is authoritative, unlike
// AgentState, and inherits upward: a bad entry makes its feature and the
// workspace need attention, so one polled field can never miss a branch.
type AttentionStatus string

const (
	AttentionNeedsAttention AttentionStatus = "needs_attention"
	AttentionActive         AttentionStatus = "active"
	AttentionIdle           AttentionStatus = "idle"
)

// SessionKind distinguishes the four observable runtime shapes.
type SessionKind string

const (
	SessionKindCheckoutDirect SessionKind = "checkout-direct"
	SessionKindCheckoutTmux   SessionKind = "checkout-tmux"
	SessionKindExternalDirect SessionKind = "external-direct"
	SessionKindExternalTmux   SessionKind = "external-tmux"
)

// IssueScope is the single home of an issue.
type IssueScope string

const (
	ScopeWorkspace IssueScope = "workspace"
	ScopeFeature   IssueScope = "feature"
	ScopeEntry     IssueScope = "entry"
)

// ---------- Issue codes (exhaustive and closed) ----------

// Workspace-scoped codes.
const (
	IssueWorkspaceDegraded          = "workspace-degraded"
	IssueSessionOrphanLock          = "session-orphan-lock"
	IssueSessionStateInvalid        = "session-state-invalid"
	IssueSessionStateUnsupported    = "session-state-unsupported"
	IssueSessionLockMissing         = "session-lock-missing"
	IssueSessionLockInvalid         = "session-lock-invalid"
	IssueSessionUnattributed        = "session-unattributed"
	IssueSessionStageUnrecognized   = "session-stage-unrecognized"
	IssueSessionWorkspaceIDMismatch = "session-workspace-id-mismatch"
	IssueRepoDirty                  = "repo-dirty"
	IssueRepoDirtyBlocking          = "repo-dirty-blocking"
	IssueRepoDetached               = "repo-detached"
	IssueRepoGitOp                  = "repo-git-op"
	IssueTmuxMissing                = "tmux-missing"
	IssueTmuxUnverifiable           = "tmux-unverifiable"
)

// Feature-scoped codes. IssueTmuxPathMismatch and IssueTmuxPanesUnverified are
// also used at entry scope for the per-branch tmux session.
const (
	IssueStackMissing              = "stack-missing"
	IssueStackInvalid              = "stack-invalid"
	IssueSyncInProgress            = "sync-in-progress"
	IssueSyncStale                 = "sync-stale"
	IssueSyncInvalid               = "sync-invalid"
	IssueSyncFailed                = "sync-failed"
	IssueSyncStatePresent          = "sync-state-present"
	IssueSyncStateInvalid          = "sync-state-invalid"
	IssueDirectRecordOrphanBranch  = "direct-record-orphan-branch"
	IssueDirectRecordDirUnreadable = "direct-record-dir-unreadable"
	IssueTmuxPathMismatch          = "tmux-path-mismatch"
	IssueTmuxPanesUnverified       = "tmux-panes-unverified"
)

// Entry-scoped codes.
const (
	IssueWorktreeMissing         = "worktree-missing"
	IssueWorktreePrunableMissing = "worktree-prunable-missing"
	IssueWorktreeUnreadable      = "worktree-unreadable"
	IssueWorktreeWrongBranch     = "worktree-wrong-branch"
	IssueWorktreeDirty           = "worktree-dirty"
	IssueWorktreeDirtyBlocking   = "worktree-dirty-blocking"
	IssueRefMissing              = "ref-missing"
	IssueRefMissingArchived      = "ref-missing-archived"
	IssueCrossRepoUnsupported    = "cross-repo-unsupported"
	IssueDirectRecordStale       = "direct-record-stale"
	IssueDirectRecordUnknown     = "direct-record-unknown"
	IssueDirectRecordInvalid     = "direct-record-invalid"
	IssueDirectRecordUnsupported = "direct-record-unsupported"
	IssueSessionOwnerDead        = "session-owner-dead"
	IssueSessionOwnerUnknown     = "session-owner-unknown"
	IssueSessionTmuxGone         = "session-tmux-gone"
	IssueSyncFailedBranch        = "sync-failed-branch"
	IssueSyncCurrentBranch       = "sync-current-branch"
)

// AgentStatusIssueCodes is the closed set of codes any status document may
// contain. Adding a code requires editing this list and the spec table.
var AgentStatusIssueCodes = []string{
	IssueWorkspaceDegraded,
	IssueSessionOrphanLock,
	IssueSessionStateInvalid,
	IssueSessionStateUnsupported,
	IssueSessionLockMissing,
	IssueSessionLockInvalid,
	IssueSessionUnattributed,
	IssueSessionStageUnrecognized,
	IssueSessionWorkspaceIDMismatch,
	IssueRepoDirty,
	IssueRepoDirtyBlocking,
	IssueRepoDetached,
	IssueRepoGitOp,
	IssueTmuxMissing,
	IssueTmuxUnverifiable,
	IssueStackMissing,
	IssueStackInvalid,
	IssueSyncInProgress,
	IssueSyncStale,
	IssueSyncInvalid,
	IssueSyncFailed,
	IssueSyncStatePresent,
	IssueSyncStateInvalid,
	IssueDirectRecordOrphanBranch,
	IssueDirectRecordDirUnreadable,
	IssueTmuxPathMismatch,
	IssueTmuxPanesUnverified,
	IssueWorktreeMissing,
	IssueWorktreePrunableMissing,
	IssueWorktreeUnreadable,
	IssueWorktreeWrongBranch,
	IssueWorktreeDirty,
	IssueWorktreeDirtyBlocking,
	IssueRefMissing,
	IssueRefMissingArchived,
	IssueCrossRepoUnsupported,
	IssueDirectRecordStale,
	IssueDirectRecordUnknown,
	IssueDirectRecordInvalid,
	IssueDirectRecordUnsupported,
	IssueSessionOwnerDead,
	IssueSessionOwnerUnknown,
	IssueSessionTmuxGone,
	IssueSyncFailedBranch,
	IssueSyncCurrentBranch,
}

// ---------- Document ----------

// AgentStatusIssue is one signal, stored exactly once in the flat
// AgentStatusReport.Issues slice. Levels carry only a rollup, never a copy.
type AgentStatusIssue struct {
	Code     string           `json:"code"`
	Severity CheckoutSeverity `json:"severity"`
	Scope    IssueScope       `json:"scope"`
	Feature  *string          `json:"feature"`
	Name     *string          `json:"name"`
	Message  string           `json:"message"`
	Guidance *string          `json:"guidance"`
}

// AttentionRollup is the per-level verdict. IssueCount and Codes are always
// own-scope only, so `{"status":"needs_attention","issue_count":0,"codes":[]}`
// is the normal, machine-readable statement "nothing is wrong here, something
// is wrong below".
type AttentionRollup struct {
	Status     AttentionStatus `json:"status"`
	IssueCount int             `json:"issue_count"`
	Codes      []string        `json:"codes"`
}

// SessionObservation is one runtime fact. It is a purpose-built projection:
// no raw CheckoutAgentSession is ever serialized, so no lock token, owner
// token, link list, transcript, prompt, or argv can leak.
type SessionObservation struct {
	Kind            SessionKind     `json:"kind"`
	Presence        RuntimePresence `json:"presence"`
	AgentState      AgentState      `json:"agent_state"`
	Stage           *string         `json:"stage"`
	StageRecognized bool            `json:"stage_recognized"`
	OwnerPID        *int            `json:"owner_pid"`
	ChildPID        *int            `json:"child_pid"`
	Liveness        *string         `json:"liveness"`
	TmuxSession     *string         `json:"tmux_session"`
	Path            *string         `json:"path"`
	Agent           *string         `json:"agent"`
	StartedAt       *string         `json:"started_at"`
	UpdatedAt       *string         `json:"updated_at"`
	RecordID        *string         `json:"record_id"`
	RecordState     string          `json:"record_state"`
	Detail          *string         `json:"detail"`
}

// EntryMaterialization describes whether and how a logical branch exists on
// disk. It is an object, never a bare string, because what materialization
// means differs by workspace mode.
type EntryMaterialization struct {
	Kind             string  `json:"kind"`
	State            string  `json:"state"`
	Path             *string `json:"path"`
	RefExists        *bool   `json:"ref_exists"`
	CheckedOutBranch *string `json:"checked_out_branch"`
	Dirty            *bool   `json:"dirty"`
}

// Materialization kinds and states.
const (
	MaterializationWorktree = "worktree"
	MaterializationRef      = "ref"

	MaterializedPresent         = "present"
	MaterializedArchived        = "archived"
	MaterializedMissing         = "missing"
	MaterializedPrunableMissing = "prunable-missing"
	MaterializedCrossRepo       = "cross-repo-unsupported"
	MaterializedUnknown         = "unknown"
)

// SessionCounts summarizes an entry's session observations.
type SessionCounts struct {
	Total   int `json:"total"`
	Live    int `json:"live"`
	Stale   int `json:"stale"`
	Unknown int `json:"unknown"`
	Invalid int `json:"invalid"`
}

// AgentStatusEntry is one StackEntry, in stack.yaml order, archived included.
type AgentStatusEntry struct {
	Feature           string               `json:"feature"`
	Name              string               `json:"name"`
	GitBranch         string               `json:"git_branch"`
	Base              string               `json:"base"`
	BaseGitBranch     string               `json:"base_git_branch"`
	Repo              *string              `json:"repo"`
	Archived          bool                 `json:"archived"`
	IsCurrentCheckout *bool                `json:"is_current_checkout"`
	Materialization   EntryMaterialization `json:"materialization"`
	Sessions          []SessionObservation `json:"sessions"`
	SessionCounts     SessionCounts        `json:"session_counts"`
	UnreadDecisions   int                  `json:"unread_decisions"`
	RuntimePresence   RuntimePresence      `json:"runtime_presence"`
	AgentState        AgentState           `json:"agent_state"`
	Attention         AttentionRollup      `json:"attention"`
	FeatureAttention  bool                 `json:"feature_attention"`
}

// AgentStatusFeatureSync is a discriminated projection of a sync transaction
// or an external sync state; never the raw type.
type AgentStatusFeatureSync struct {
	Kind          string   `json:"kind"`
	Stage         *string  `json:"stage"`
	Liveness      *string  `json:"liveness"`
	FailureReason *string  `json:"failure_reason"`
	CurrentBranch *string  `json:"current_branch"`
	FailedBranch  *string  `json:"failed_branch"`
	LockPID       *int     `json:"lock_pid"`
	LockLive      *bool    `json:"lock_live"`
	Pending       []string `json:"pending"`
	Completed     []string `json:"completed"`
	Skipped       []string `json:"skipped"`
}

// Feature stack states.
const (
	StackStateOK      = "ok"
	StackStateMissing = "missing"
	StackStateInvalid = "invalid"
)

// AgentStatusFeature is one feature of the resolved workspace.
type AgentStatusFeature struct {
	Feature         string                  `json:"feature"`
	Path            string                  `json:"path"`
	StackState      string                  `json:"stack_state"`
	Sync            *AgentStatusFeatureSync `json:"sync"`
	FeatureTmux     *SessionObservation     `json:"feature_tmux"`
	Entries         []AgentStatusEntry      `json:"entries"`
	RuntimePresence RuntimePresence         `json:"runtime_presence"`
	AgentState      AgentState              `json:"agent_state"`
	Attention       AttentionRollup         `json:"attention"`
}

// TmuxStatus is the workspace-level tmux inventory summary.
type TmuxStatus struct {
	Available        bool `json:"available"`
	ServerRunning    bool `json:"server_running"`
	SessionCount     int  `json:"session_count"`
	PathVerification bool `json:"path_verification"`
}

// AgentStatusWorkspace describes the resolved workspace. In external mode the
// Git fields describe the source repository, not any worktree.
type AgentStatusWorkspace struct {
	Mode            WorkspaceMode       `json:"mode"`
	StableID        *string             `json:"stable_id"`
	RepoRoot        *string             `json:"repo_root"`
	MetadataRoot    string              `json:"metadata_root"`
	Degraded        bool                `json:"degraded"`
	DegradedReason  *string             `json:"degraded_reason"`
	Branch          *string             `json:"branch"`
	Detached        *bool               `json:"detached"`
	Dirty           *bool               `json:"dirty"`
	ActiveGitOp     *string             `json:"active_git_op"`
	Tmux            TmuxStatus          `json:"tmux"`
	CheckoutSession *SessionObservation `json:"checkout_session"`
	RuntimePresence RuntimePresence     `json:"runtime_presence"`
	AgentState      AgentState          `json:"agent_state"`
	Attention       AttentionRollup     `json:"attention"`
}

// AgentStatusSummary counts what is present in the (possibly filtered)
// document. The three attention counters and the four runtime counters each
// describe entries only and each sum exactly to Entries.
type AgentStatusSummary struct {
	Features       int `json:"features"`
	Entries        int `json:"entries"`
	NeedsAttention int `json:"needs_attention"`
	Active         int `json:"active"`
	Idle           int `json:"idle"`
	RuntimePresent int `json:"runtime_present"`
	RuntimeStale   int `json:"runtime_stale"`
	RuntimeUnknown int `json:"runtime_unknown"`
	RuntimeAbsent  int `json:"runtime_absent"`
	Issues         int `json:"issues"`
	Warnings       int `json:"warnings"`
	Errors         int `json:"errors"`
}

// AgentStatusReport is the versioned status document.
type AgentStatusReport struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   string               `json:"generated_at"`
	Workspace     AgentStatusWorkspace `json:"workspace"`
	Features      []AgentStatusFeature `json:"features"`
	Issues        []AgentStatusIssue   `json:"issues"`
	Summary       AgentStatusSummary   `json:"summary"`
}

// AgentStatusOpts injects the three seams the builder has. There is
// deliberately no base-lineage option (that belongs to the stack doctor) and
// no working-directory option: nothing in the builder may observe the process
// working directory.
type AgentStatusOpts struct {
	Proc ProcessProber
	Tmux TmuxInventoryProbe
	Now  func() time.Time
}

func defaultAgentStatusOpts() AgentStatusOpts {
	return AgentStatusOpts{
		Proc: realProcessChecker{},
		Tmux: RealTmuxInventory{},
		Now:  time.Now,
	}
}

// ---------- tmux inventory seam ----------

// TmuxPane is one tmux pane with its current working directory.
type TmuxPane struct {
	Session string
	Path    string
}

// TmuxSnapshot is a single tmux inventory taken once per invocation. There is
// no per-entry `tmux has-session`.
type TmuxSnapshot struct {
	Available      bool
	ServerRunning  bool
	Sessions       map[string]bool
	Panes          []TmuxPane
	PanesAvailable bool
	Err            error
}

// TmuxInventoryProbe abstracts the tmux inventory for testing.
type TmuxInventoryProbe interface {
	Snapshot() TmuxSnapshot
}

// RealTmuxInventory shells out to tmux exactly twice per invocation.
type RealTmuxInventory struct{}

func (RealTmuxInventory) Snapshot() TmuxSnapshot {
	snap := TmuxSnapshot{Sessions: map[string]bool{}}
	if _, err := exec.LookPath("tmux"); err != nil {
		return snap
	}
	snap.Available = true

	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").CombinedOutput()
	if err != nil {
		text := strings.ToLower(string(out))
		if strings.Contains(text, "no server running") || strings.Contains(text, "error connecting to") {
			return snap
		}
		snap.Err = fmt.Errorf("tmux list-sessions: %s: %w", strings.TrimSpace(string(out)), err)
		return snap
	}
	snap.ServerRunning = true
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			snap.Sessions[name] = true
		}
	}

	panes, paneErr := exec.Command("tmux", "list-panes", "-a", "-F", "#{session_name}\t#{pane_current_path}").Output()
	if paneErr != nil {
		return snap
	}
	snap.PanesAvailable = true
	for _, line := range strings.Split(string(panes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		snap.Panes = append(snap.Panes, TmuxPane{Session: parts[0], Path: parts[1]})
	}
	return snap
}

// ---------- worktree inventory ----------

// WorktreeInventory is a single `git worktree list --porcelain` result. It
// replaces IsPrunableWorktree for status because that helper runs without -C
// and therefore fails when invoked from an external workspace root.
type WorktreeInventory struct {
	Available bool
	ByBranch  map[string]string
	Prunable  map[string]bool
}

// BuildWorktreeInventory runs one `git -C <repoRoot> worktree list
// --porcelain`. An empty repoRoot or a Git failure yields Available == false,
// which makes prunability unknown rather than false.
func BuildWorktreeInventory(repoRoot string) WorktreeInventory {
	inv := WorktreeInventory{ByBranch: map[string]string{}, Prunable: map[string]bool{}}
	if repoRoot == "" {
		return inv
	}
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return inv
	}
	inv.Available = true

	var curPath, curBranch string
	var curPrunable bool
	flush := func() {
		if curBranch != "" {
			if curPrunable {
				inv.Prunable[curBranch] = true
			} else {
				inv.ByBranch[curBranch] = curPath
			}
		}
		curPath, curBranch, curPrunable = "", "", false
	}
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			curPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			curBranch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			curPrunable = true
		}
	}
	flush()
	return inv
}

// ---------- rollups ----------

// RollupPresence folds observations with the precedence
// present > unknown > stale > absent. An unverifiable session might be alive,
// so `unknown` outranks `stale`: claiming stale would be a stronger statement
// than the evidence supports.
func RollupPresence(values []RuntimePresence) RuntimePresence {
	seen := map[RuntimePresence]bool{}
	for _, v := range values {
		seen[v] = true
	}
	switch {
	case seen[PresencePresent]:
		return PresencePresent
	case seen[PresenceUnknown]:
		return PresenceUnknown
	case seen[PresenceStale]:
		return PresenceStale
	default:
		return PresenceAbsent
	}
}

// RollupAttention returns the attention status for one level.
//
//	own                 — issues homed at this exact level (scope+feature+name
//	                      match), never a descendant's issues.
//	childNeedsAttention — true when any immediate child level already rolled up
//	                      to needs_attention. Always false for an entry.
func RollupAttention(presence RuntimePresence, own []AgentStatusIssue, childNeedsAttention bool) AttentionStatus {
	if childNeedsAttention || anyWarningOrError(own) {
		return AttentionNeedsAttention
	}
	if presence == PresencePresent {
		return AttentionActive
	}
	return AttentionIdle
}

func anyWarningOrError(issues []AgentStatusIssue) bool {
	for _, i := range issues {
		if i.Severity == SeverityWarning || i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// ---------- workspace resolution ----------

// ResolveStatusWorkspace resolves the workspace the same way RequireWorkspace
// does, with one difference: an external workspace root whose source
// repository cannot be inferred yields a degraded workspace plus a reason
// instead of an error, so a report can still be produced.
//
// Both roots are canonicalized so the document is byte-identical from every
// supported working directory.
func ResolveStatusWorkspace() (Workspace, string, error) {
	cfg := LoadConfig()
	if repoRoot, err := MainRepoRoot(); err == nil {
		ws, wsErr := ResolveCurrentWorkspaceE(repoRoot, cfg)
		if wsErr != nil {
			return Workspace{}, "", wsErr
		}
		ws.MetadataRoot = canonicalize(ws.MetadataRoot)
		return ws, "", nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return Workspace{}, "", err
	}
	metadataRoot := DetectWorkspaceRoot(cwd, cfg)
	if metadataRoot == "" || !metadataRootExists(metadataRoot) {
		return Workspace{}, "", fmt.Errorf("not inside a git repository or tws workspace")
	}
	repoRoot, inferErr := inferExternalRepoRoot(metadataRoot, cfg)
	if inferErr != nil {
		return Workspace{
			RepoRoot:     "",
			Mode:         ModeExternal,
			MetadataRoot: canonicalize(metadataRoot),
			StableID:     "",
			Caps:         capsFor(ModeExternal),
		}, inferErr.Error(), nil
	}
	canonRepo := canonicalize(repoRoot)
	return Workspace{
		RepoRoot:     canonRepo,
		Mode:         ModeExternal,
		MetadataRoot: canonicalize(metadataRoot),
		StableID:     stableID(canonRepo),
		Caps:         capsFor(ModeExternal),
	}, "", nil
}

// ---------- builder ----------

type pendingEntryIssue struct {
	feature  string
	name     string
	code     string
	severity CheckoutSeverity
	message  string
	guidance string
}

type statusBuilder struct {
	ws     Workspace
	opts   AgentStatusOpts
	report *AgentStatusReport
	tmux   TmuxSnapshot
	wt     WorktreeInventory

	// checkout session state carried from projection (phase A) to
	// attribution (phase B). Phase B performs no I/O.
	sessionFeature string
	sessionName    string
	// sessionAttributable gates phase B entirely. It is set only once the
	// record has been parsed, its schema is supported, and the probe that
	// produced the observation was trustworthy enough to name a branch. An
	// untrustworthy record stays workspace-only: attributing it would put an
	// `unknown` presence on an entry that owns no warning, which would break
	// the "stale|unknown ⇒ needs_attention" invariant.
	sessionAttributable bool
	sessionPending      *pendingEntryIssue
	tmuxRecordSeen      bool
}

func (b *statusBuilder) issue(code string, sev CheckoutSeverity, scope IssueScope, feature, name, message, guidance string) {
	iss := AgentStatusIssue{
		Code:     code,
		Severity: sev,
		Scope:    scope,
		Message:  message,
	}
	if feature != "" {
		iss.Feature = strPtr(feature)
	}
	if name != "" {
		iss.Name = strPtr(name)
	}
	if guidance != "" {
		iss.Guidance = strPtr(guidance)
	}
	b.report.Issues = append(b.report.Issues, iss)
}

// BuildAgentStatus produces the whole status report. It is strictly
// read-only: it never mutates a lock, a record, a stack, or Git state.
func BuildAgentStatus(ws Workspace, degradedReason string, opts *AgentStatusOpts) (*AgentStatusReport, error) {
	resolved := defaultAgentStatusOpts()
	if opts != nil {
		if opts.Proc != nil {
			resolved.Proc = opts.Proc
		}
		if opts.Tmux != nil {
			resolved.Tmux = opts.Tmux
		}
		if opts.Now != nil {
			resolved.Now = opts.Now
		}
	}

	// 1. Metadata-root precondition. ListFeaturesResolved swallows its
	// os.ReadDir errors and would return an empty, successful list for an
	// unreadable workspace, which is silence about topology.
	info, statErr := os.Stat(ws.MetadataRoot)
	switch {
	case statErr != nil:
		return nil, fmt.Errorf("workspace metadata root unreadable: %s: %w", ws.MetadataRoot, statErr)
	case !info.IsDir():
		return nil, fmt.Errorf("workspace metadata root unreadable: %s: not a directory", ws.MetadataRoot)
	}
	if _, err := os.ReadDir(ws.MetadataRoot); err != nil {
		return nil, fmt.Errorf("workspace metadata root unreadable: %s: %w", ws.MetadataRoot, err)
	}

	// 2. Topology listing. An untrusted spaces.yaml is fatal here.
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		return nil, err
	}

	b := &statusBuilder{
		ws:   ws,
		opts: resolved,
		report: &AgentStatusReport{
			SchemaVersion: agentStatusSchema,
			GeneratedAt:   resolved.Now().UTC().Format(time.RFC3339),
		},
	}

	// 3/4. One tmux snapshot and one worktree inventory per invocation.
	b.tmux = resolved.Tmux.Snapshot()
	b.wt = BuildWorktreeInventory(ws.RepoRoot)

	// 5. Workspace header and the issues those fields alone determine.
	b.buildWorkspaceHeader(degradedReason)

	// 6. Checkout session, phase A: projection.
	if ws.Mode == ModeCheckout {
		b.projectCheckoutSession()
		b.emitRepoDirtyIssue(b.report.Workspace.Dirty, b.report.Workspace.CheckoutSession != nil)
	}
	b.emitTmuxWorkspaceIssue()

	// 7. Features and entries.
	for _, feature := range features {
		featureView, ferr := b.buildFeature(feature)
		if ferr != nil {
			return nil, ferr
		}
		b.report.Features = append(b.report.Features, featureView)
	}

	// 8. Checkout session, phase B: attribution. No I/O.
	if ws.Mode == ModeCheckout {
		b.attributeCheckoutSession()
	}

	// 9-10. Rollups then counters.
	recomputeAgentStatusRollups(b.report)
	recomputeAgentStatusSummary(b.report)
	NormalizeAgentStatus(b.report)
	return b.report, nil
}

func (b *statusBuilder) buildWorkspaceHeader(degradedReason string) {
	ws := b.ws
	w := AgentStatusWorkspace{
		Mode:            ws.Mode,
		MetadataRoot:    ws.MetadataRoot,
		AgentState:      AgentStateUnknown,
		RuntimePresence: PresenceAbsent,
		Tmux: TmuxStatus{
			Available:        b.tmux.Available,
			ServerRunning:    b.tmux.ServerRunning,
			SessionCount:     len(b.tmux.Sessions),
			PathVerification: b.tmux.PanesAvailable,
		},
	}
	if ws.StableID != "" {
		w.StableID = strPtr(ws.StableID)
	}
	if ws.RepoRoot != "" {
		w.RepoRoot = strPtr(ws.RepoRoot)
	}
	if degradedReason != "" {
		w.Degraded = true
		w.DegradedReason = strPtr(degradedReason)
	}
	b.report.Workspace = w

	if degradedReason != "" {
		b.issue(IssueWorkspaceDegraded, SeverityWarning, ScopeWorkspace, "", "",
			"workspace repository could not be determined: "+degradedReason,
			"run tws doctor and check the workspace marker and default repositories")
	}

	if ws.RepoRoot == "" {
		return
	}

	branch, detached := healthCurrentBranch(ws.RepoRoot)
	if branch != "" {
		b.report.Workspace.Branch = strPtr(branch)
	}
	b.report.Workspace.Detached = boolPtr(detached)
	b.report.Workspace.Dirty = boolPtr(gitDirty(ws.RepoRoot))
	if op := gitActiveOp(ws.RepoRoot); op != "" {
		b.report.Workspace.ActiveGitOp = strPtr(op)
		b.issue(IssueRepoGitOp, SeverityWarning, ScopeWorkspace, "", "",
			fmt.Sprintf("a %s is in progress in %s", op, ws.RepoRoot),
			"finish or abort the in-progress git operation")
	}
	if detached {
		b.issue(IssueRepoDetached, SeverityInfo, ScopeWorkspace, "", "",
			fmt.Sprintf("HEAD is detached in %s", ws.RepoRoot), "")
	}
}

// emitTmuxWorkspaceIssue reports an unusable tmux inventory once, at workspace
// scope. No evidence means no observation: a tmux-free workspace must not read
// needs_attention on every branch.
func (b *statusBuilder) emitTmuxWorkspaceIssue() {
	switch {
	case !b.tmux.Available:
		if b.tmuxRecordSeen {
			b.issue(IssueTmuxUnverifiable, SeverityWarning, ScopeWorkspace, "", "",
				"a tmux session record exists but the tmux binary is not in PATH",
				"start tmux or run: tws close")
			return
		}
		b.issue(IssueTmuxMissing, SeverityInfo, ScopeWorkspace, "", "",
			"tmux is not installed or not in PATH; tmux sessions cannot be observed", "")
	case b.tmux.Err != nil:
		b.issue(IssueTmuxUnverifiable, SeverityWarning, ScopeWorkspace, "", "",
			"tmux inventory unavailable: "+b.tmux.Err.Error(),
			"start tmux or run: tws close")
	}
}

// tmuxUsable reports whether the inventory can answer a name query at all.
func (b *statusBuilder) tmuxUsable() bool { return b.tmux.Available && b.tmux.Err == nil }

// verifyTmuxName checks a session name against the inventory and a canonical
// target directory. It returns nil when there is no evidence at all.
func (b *statusBuilder) verifyTmuxName(name, targetPath string) (presence RuntimePresence, issueCode, detail string, found bool) {
	if !b.tmuxUsable() || !b.tmux.Sessions[name] {
		return "", "", "", false
	}
	if !b.tmux.PanesAvailable {
		return PresenceUnknown, IssueTmuxPanesUnverified,
			fmt.Sprintf("tmux session %q exists but pane paths are unavailable, so the match is unverified", name), true
	}
	target := canonicalize(targetPath)
	for _, pane := range b.tmux.Panes {
		if pane.Session != name {
			continue
		}
		p := canonicalize(pane.Path)
		if p == target || strings.HasPrefix(p, target+string(filepath.Separator)) {
			return PresencePresent, "", "", true
		}
	}
	return PresenceUnknown, IssueTmuxPathMismatch,
		fmt.Sprintf("tmux session %q exists but no pane reports a working directory under %s; it may belong to another workspace, or its panes may have changed directory", name, targetPath), true
}

// ---------- checkout session ----------

var recognizedSessionStages = map[string]bool{
	DirectStageStarting: true,
	DirectStageAgent:    true,
	DirectStageShell:    true,
	"tmux":              true,
}

// projectCheckoutSession implements the checkout session decision table.
//
// The read order is mandatory and differs from buildSessionReport, which
// treats "file missing" and "file unparseable" identically because
// LoadCheckoutAgentSession returns one undifferentiated error. A corrupt
// active.json with no lock must be reported, not silently ignored.
func (b *statusBuilder) projectCheckoutSession() {
	statePath := sessionStatePath(b.ws)
	lockDir := sessionLockDir(b.ws)

	_, stateErr := os.Stat(statePath)
	stateExists := stateErr == nil || !errors.Is(stateErr, fs.ErrNotExist)
	_, lockErr := os.Stat(lockDir)
	lockExists := lockErr == nil || !errors.Is(lockErr, fs.ErrNotExist)

	if !stateExists {
		if lockExists {
			b.issue(IssueSessionOrphanLock, SeverityWarning, ScopeWorkspace, "", "",
				"session lock exists but no session state", "run: tws close")
		}
		return
	}

	obs := &SessionObservation{
		// The mode of an unparseable record is unknowable; direct is the
		// default checkout session mode, and record_state says the record
		// could not be trusted.
		Kind:        SessionKindCheckoutDirect,
		Presence:    PresenceUnknown,
		AgentState:  AgentStateUnknown,
		RecordState: string(DirectRecordOK),
	}

	data, readErr := os.ReadFile(statePath)
	var state CheckoutAgentSession
	parsed := false
	if readErr == nil {
		if err := json.Unmarshal(data, &state); err == nil {
			parsed = true
		}
	}
	if !parsed {
		obs.RecordState = string(DirectRecordInvalid)
		obs.Detail = strPtr("session state could not be read or parsed")
		b.report.Workspace.CheckoutSession = obs
		b.issue(IssueSessionStateInvalid, SeverityWarning, ScopeWorkspace, "", "",
			"checkout session state is unreadable or unparseable: "+statePath,
			"inspect or remove "+statePath)
		b.evaluateLock(lockExists)
		return
	}

	if state.Mode == AgentSessionTmux {
		obs.Kind = SessionKindCheckoutTmux
		b.tmuxRecordSeen = true
	}
	if state.Stage != "" {
		obs.Stage = strPtr(state.Stage)
	}
	obs.StageRecognized = recognizedSessionStages[state.Stage]
	if state.PID > 0 {
		obs.OwnerPID = intPtr(state.PID)
	}
	if state.TmuxSession != "" {
		obs.TmuxSession = strPtr(state.TmuxSession)
	}
	if state.RepoDir != "" {
		obs.Path = strPtr(state.RepoDir)
	}
	if state.StartedAt != "" {
		obs.StartedAt = strPtr(state.StartedAt)
	}

	if state.SchemaVersion > checkoutSessionSchema {
		obs.RecordState = string(DirectRecordUnsupported)
		b.report.Workspace.CheckoutSession = obs
		b.issue(IssueSessionStateUnsupported, SeverityWarning, ScopeWorkspace, "", "",
			fmt.Sprintf("checkout session state schema %d is newer than %d", state.SchemaVersion, checkoutSessionSchema),
			"written by a newer tws; upgrade tws")
		b.evaluateLock(lockExists)
		return
	}

	// The schema is supported, so the recorded identity may now be trusted.
	// Everything above this line stays workspace-only by construction.
	b.sessionFeature = state.Feature
	b.sessionName = state.Name
	b.sessionAttributable = true

	switch state.Mode {
	case AgentSessionTmux:
		switch {
		case !b.tmuxUsable():
			obs.Presence = PresenceUnknown
			// The workspace-scoped tmux-unverifiable issue is emitted once by
			// emitTmuxWorkspaceIssue, which knows a record needs verification,
			// and that workspace warning owns this unknown observation. The
			// observation therefore stays workspace-only: attaching an
			// `unknown` presence to an entry that owns no warning would make
			// the entry read `idle` while its runtime is unverifiable.
			b.sessionAttributable = false
		case state.TmuxSession != "" && b.tmux.Sessions[state.TmuxSession]:
			obs.Presence = PresencePresent
			obs.Liveness = strPtr(string(ProcessLive))
		default:
			obs.Presence = PresenceStale
			b.sessionPending = &pendingEntryIssue{
				feature: state.Feature, name: state.Name,
				code: IssueSessionTmuxGone, severity: SeverityWarning,
				message:  fmt.Sprintf("tmux session %q is gone", state.TmuxSession),
				guidance: "run: tws close",
			}
		}
	default:
		liveness := b.opts.Proc.Probe(state.PID)
		obs.Liveness = strPtr(string(liveness))
		switch liveness {
		case ProcessLive:
			obs.Presence = PresencePresent
		case ProcessDead:
			obs.Presence = PresenceStale
			b.sessionPending = &pendingEntryIssue{
				feature: state.Feature, name: state.Name,
				code: IssueSessionOwnerDead, severity: SeverityWarning,
				message:  fmt.Sprintf("session owner pid %d is dead", state.PID),
				guidance: "run: tws close",
			}
		default:
			obs.Presence = PresenceUnknown
			b.sessionPending = &pendingEntryIssue{
				feature: state.Feature, name: state.Name,
				code: IssueSessionOwnerUnknown, severity: SeverityWarning,
				message:  fmt.Sprintf("session owner pid %d could not be verified", state.PID),
				guidance: fmt.Sprintf("check pid %d; it may belong to another user", state.PID),
			}
		}
	}

	if !obs.StageRecognized {
		b.issue(IssueSessionStageUnrecognized, SeverityInfo, ScopeWorkspace, "", "",
			fmt.Sprintf("checkout session stage %q is not recognized by this tws", state.Stage), "")
	}
	if state.WorkspaceID != b.ws.StableID {
		b.issue(IssueSessionWorkspaceIDMismatch, SeverityInfo, ScopeWorkspace, "", "",
			"checkout session was recorded for a different workspace id", "")
	}

	if !lockExists {
		obs.Detail = strPtr("lock missing")
	}
	b.report.Workspace.CheckoutSession = obs
	b.evaluateLock(lockExists)
}

// evaluateLock reads owner.json for validity only. The owner token is reduced
// to a boolean on the spot and never reaches an observation field, an issue
// message, stdout, or stderr.
func (b *statusBuilder) evaluateLock(lockExists bool) {
	if !lockExists {
		b.issue(IssueSessionLockMissing, SeverityWarning, ScopeWorkspace, "", "",
			"checkout session state exists but the session lock is missing", "run: tws close")
		return
	}
	data, err := os.ReadFile(sessionLockOwnerPath(b.ws))
	if err != nil {
		b.issue(IssueSessionLockInvalid, SeverityWarning, ScopeWorkspace, "", "",
			"checkout session lock owner is unreadable", "run: tws close")
		return
	}
	var owner sessionLockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		b.issue(IssueSessionLockInvalid, SeverityWarning, ScopeWorkspace, "", "",
			"checkout session lock owner is unparseable", "run: tws close")
		return
	}
	pid, tokenEmpty := owner.PID, owner.Token == ""
	if pid <= 0 || tokenEmpty {
		b.issue(IssueSessionLockInvalid, SeverityWarning, ScopeWorkspace, "", "",
			"checkout session lock owner is incomplete", "run: tws close")
	}
}

// emitRepoDirtyIssue escalates checkout dirtiness only when a session record
// exists, because dirt is exactly what makes restoreCheckoutSession refuse.
func (b *statusBuilder) emitRepoDirtyIssue(dirty *bool, recordExists bool) {
	if dirty == nil || !*dirty {
		return
	}
	if recordExists {
		b.issue(IssueRepoDirtyBlocking, SeverityWarning, ScopeWorkspace, "", "",
			"checkout repository is dirty and a checkout session record exists, so tws close will refuse to restore",
			"commit or stash before: tws close")
		return
	}
	b.issue(IssueRepoDirty, SeverityInfo, ScopeWorkspace, "", "",
		"checkout repository has uncommitted changes", "")
}

// attributeCheckoutSession homes the projected observation on its entry.
// It performs no I/O: every probe result it needs was captured in phase A.
//
// An untrustworthy record (unparseable, unsupported schema, or a tmux record
// no tmux inventory could verify) is never attributed: it stays workspace-only
// and the workspace-scoped warning that already describes it owns the unknown
// observation.
func (b *statusBuilder) attributeCheckoutSession() {
	if b.sessionObsMissing() || !b.sessionAttributable {
		return
	}
	if b.sessionFeature == "" || b.sessionName == "" {
		// A parsed record that names no logical branch is unattributable in
		// exactly the same way a record naming a vanished branch is.
		b.issue(IssueSessionUnattributed, SeverityWarning, ScopeWorkspace, "", "",
			"checkout session state records no logical branch identity, so it matches no stack entry",
			"run: tws close")
		return
	}
	for fi := range b.report.Features {
		f := &b.report.Features[fi]
		if f.Feature != b.sessionFeature {
			continue
		}
		for ei := range f.Entries {
			e := &f.Entries[ei]
			if e.Name != b.sessionName {
				continue
			}
			e.Sessions = append(e.Sessions, *b.report.Workspace.CheckoutSession)
			if p := b.sessionPending; p != nil {
				b.issue(p.code, p.severity, ScopeEntry, p.feature, p.name, p.message, p.guidance)
			}
			return
		}
	}
	b.issue(IssueSessionUnattributed, SeverityWarning, ScopeWorkspace, "", "",
		fmt.Sprintf("checkout session records %s/%s, which matches no stack entry", b.sessionFeature, b.sessionName),
		"run: tws close")
}

func (b *statusBuilder) sessionObsMissing() bool {
	return b.report.Workspace.CheckoutSession == nil
}

// ---------- features ----------

func (b *statusBuilder) buildFeature(feature string) (AgentStatusFeature, error) {
	featurePath, err := b.ws.ResolveFeaturePath(feature)
	if err != nil {
		// Ambiguous topology is fatal: reporting other features while
		// silently picking one of two candidate directories would make the
		// whole document untrustworthy.
		return AgentStatusFeature{}, err
	}

	view := AgentStatusFeature{
		Feature:         feature,
		Path:            featurePath,
		StackState:      StackStateOK,
		AgentState:      AgentStateUnknown,
		RuntimePresence: PresenceAbsent,
	}

	stack, stackErr := LoadStack(featurePath)
	switch {
	case stackErr == nil:
	case errors.Is(stackErr, fs.ErrNotExist):
		view.StackState = StackStateMissing
		b.issue(IssueStackMissing, SeverityInfo, ScopeFeature, feature, "",
			"feature has no stack.yaml yet", fmt.Sprintf("run: tws new %s <branch>", feature))
	default:
		view.StackState = StackStateInvalid
		b.issue(IssueStackInvalid, SeverityWarning, ScopeFeature, feature, "",
			fmt.Sprintf("stack.yaml is unreadable or unparseable: %v", stackErr),
			"inspect "+StackPath(featurePath))
	}

	syncView, external := b.buildFeatureSync(feature, featurePath)
	view.Sync = syncView

	records := map[string][]LoadedDirectSession{}
	if b.ws.Mode == ModeExternal {
		inventory, invErr := ListDirectSessions(featurePath)
		if invErr != nil {
			// Silence about an unreadable record tree would understate the
			// feature: orphan detection below cannot run at all.
			b.issue(IssueDirectRecordDirUnreadable, SeverityWarning, ScopeFeature, feature, "",
				fmt.Sprintf("direct session records could not be enumerated: %v", invErr),
				"inspect "+DirectSessionsDir(featurePath))
		} else {
			records = inventory
		}
		view.FeatureTmux = b.buildFeatureTmux(feature, featurePath)
	}

	attributed := map[string]bool{}
	for _, se := range stack.Branches {
		entry := b.buildEntry(feature, featurePath, se, stack, external)
		if b.ws.Mode == ModeExternal {
			branchID := DirectSessionBranchID(feature, se.Name)
			attributed[branchID] = true
			b.appendDirectSessions(&entry, featurePath, branchID)
		}
		view.Entries = append(view.Entries, entry)
	}

	if b.ws.Mode == ModeExternal {
		for branchID := range records {
			if attributed[branchID] {
				continue
			}
			if len(records[branchID]) == 0 {
				// An empty <branch-id> directory is residue of a cleaned-up
				// session whose prune lost a race, not an unattributable
				// record. Reporting it would make a needs_attention verdict
				// out of zero records.
				continue
			}
			b.issue(IssueDirectRecordOrphanBranch, SeverityWarning, ScopeFeature, feature, "",
				fmt.Sprintf("direct session directory %s holds %d record(s) that match no current branch",
					filepath.Join(DirectSessionsDir(featurePath), branchID), len(records[branchID])),
				fmt.Sprintf("inspect %s; it belongs to a renamed, archived, or deleted branch",
					filepath.Join(DirectSessionsDir(featurePath), branchID)))
		}
	}

	b.attributeSyncBranch(&view, featurePath, external)
	return view, nil
}

// attributeSyncBranch homes the two entry-scoped sync signals. The identity
// axes differ: external sync state records tws names, while a checkout
// transaction plan records git branches.
func (b *statusBuilder) attributeSyncBranch(view *AgentStatusFeature, featurePath string, external *SyncState) {
	if external != nil && external.FailedBranch != "" {
		for i := range view.Entries {
			e := &view.Entries[i]
			if e.Name != external.FailedBranch {
				continue
			}
			b.issue(IssueSyncFailedBranch, SeverityWarning, ScopeEntry, e.Feature, e.Name,
				fmt.Sprintf("an unfinished sync failed on %s", e.Name),
				fmt.Sprintf("resolve the conflict in %s, then: tws sync %s --continue",
					filepath.Join(featurePath, "worktrees", e.Name), e.Feature))
			break
		}
	}
	if view.Sync != nil && view.Sync.Kind == "checkout" && view.Sync.CurrentBranch != nil {
		for i := range view.Entries {
			e := &view.Entries[i]
			if e.GitBranch != *view.Sync.CurrentBranch {
				continue
			}
			b.issue(IssueSyncCurrentBranch, SeverityInfo, ScopeEntry, e.Feature, e.Name,
				fmt.Sprintf("checkout sync is currently working on %s", e.GitBranch), "")
			break
		}
	}
}

// buildFeatureSync projects the checkout transaction (checkout mode) or the
// external .sync-state.yaml (external mode). The returned SyncState is the
// external one, used for dirty-blocking attribution.
func (b *statusBuilder) buildFeatureSync(feature, featurePath string) (*AgentStatusFeatureSync, *SyncState) {
	if b.ws.Mode == ModeCheckout {
		// checkoutStateDir() derives the state dir from the feature path,
		// which is wrong for legacy .tws/<feature> layouts. Use the workspace.
		stateDir := b.ws.CheckoutStateDir()
		txPath := filepath.Join(stateDir, feature+"-checkout-sync.yaml")
		if _, err := os.Stat(txPath); err != nil {
			return nil, nil
		}
		rep := buildOneSyncReport(feature, txPath, stateDir, proberAsChecker{b.opts.Proc})
		view := &AgentStatusFeatureSync{
			Kind:      "checkout",
			Liveness:  strPtr(rep.Liveness),
			Pending:   []string{},
			Completed: []string{},
			Skipped:   []string{},
		}
		if rep.Stage != "" {
			view.Stage = strPtr(rep.Stage)
		}
		if rep.FailureReason != "" {
			view.FailureReason = strPtr(rep.FailureReason)
		}
		if rep.CurrentBranch != "" {
			view.CurrentBranch = strPtr(rep.CurrentBranch)
		}
		if rep.LockPID > 0 {
			view.LockPID = intPtr(rep.LockPID)
		}
		if rep.Liveness != "invalid" {
			view.LockLive = boolPtr(rep.LockLive)
		}

		switch rep.Liveness {
		case "live":
			b.issue(IssueSyncInProgress, SeverityInfo, ScopeFeature, feature, "",
				fmt.Sprintf("checkout sync is in progress at stage %s", rep.Stage), "")
		case "stale":
			b.issue(IssueSyncStale, SeverityWarning, ScopeFeature, feature, "",
				fmt.Sprintf("checkout sync transaction is stale at stage %s", rep.Stage),
				fmt.Sprintf("run: tws sync %s --continue  or  tws sync %s --abort", feature, feature))
		default:
			b.issue(IssueSyncInvalid, SeverityWarning, ScopeFeature, feature, "",
				"checkout sync state is corrupt: "+rep.Guidance,
				fmt.Sprintf("corrupt sync state; inspect %s then rerun: tws sync %s --abort", stateDir, feature))
		}
		if rep.FailureReason != "" {
			b.issue(IssueSyncFailed, SeverityWarning, ScopeFeature, feature, "",
				"checkout sync recorded a failure: "+rep.FailureReason,
				fmt.Sprintf("run: tws sync %s --continue  or  tws sync %s --abort", feature, feature))
		}
		return view, nil
	}

	statePath := SyncStatePath(featurePath)
	if _, err := os.Stat(statePath); err != nil {
		return nil, nil
	}
	state, loadErr := LoadSyncState(featurePath)
	if loadErr != nil || state == nil {
		b.issue(IssueSyncStateInvalid, SeverityWarning, ScopeFeature, feature, "",
			fmt.Sprintf("sync state is unreadable or unparseable: %v", loadErr),
			"inspect "+statePath)
		return &AgentStatusFeatureSync{
			Kind:      "external",
			Liveness:  strPtr("invalid"),
			Pending:   []string{},
			Completed: []string{},
			Skipped:   []string{},
		}, nil
	}

	view := &AgentStatusFeatureSync{
		Kind:      "external",
		Pending:   append([]string{}, state.Pending...),
		Completed: append([]string{}, state.Completed...),
		Skipped:   append([]string{}, state.Skipped...),
	}
	if state.FailedBranch != "" {
		view.FailedBranch = strPtr(state.FailedBranch)
	} else {
		b.issue(IssueSyncStatePresent, SeverityInfo, ScopeFeature, feature, "",
			"an external sync state file is present", "")
	}
	return view, state
}

func (b *statusBuilder) buildFeatureTmux(feature, featurePath string) *SessionObservation {
	name := ExternalFeatureTmuxSessionName(feature)
	presence, code, detail, found := b.verifyTmuxName(name, featurePath)
	if !found {
		return nil
	}
	obs := &SessionObservation{
		Kind:            SessionKindExternalTmux,
		Presence:        presence,
		AgentState:      AgentStateUnknown,
		Stage:           strPtr("tmux"),
		StageRecognized: true,
		TmuxSession:     strPtr(name),
		Path:            strPtr(featurePath),
		RecordState:     string(DirectRecordOK),
	}
	if code != "" {
		obs.Detail = strPtr(detail)
		b.issue(code, SeverityWarning, ScopeFeature, feature, "", detail,
			"check which tmux session owns that name")
	}
	return obs
}

// ---------- entries ----------

func (b *statusBuilder) buildEntry(feature, featurePath string, se StackEntry, stack Stack, external *SyncState) AgentStatusEntry {
	gitBranch := se.GitBranch()
	entry := AgentStatusEntry{
		Feature:         feature,
		Name:            se.Name,
		GitBranch:       gitBranch,
		Base:            se.Base,
		BaseGitBranch:   se.Base,
		Archived:        se.Archived,
		AgentState:      AgentStateUnknown,
		RuntimePresence: PresenceAbsent,
		UnreadDecisions: len(UnreadDecisions(featurePath, se.Name)),
	}
	for _, parent := range stack.Branches {
		if parent.Name == se.Base {
			entry.BaseGitBranch = parent.GitBranch()
			break
		}
	}
	if se.Repo != "" {
		entry.Repo = strPtr(se.Repo)
	}

	if b.ws.Mode == ModeCheckout {
		b.buildCheckoutMaterialization(&entry, se)
	} else {
		b.buildExternalMaterialization(&entry, feature, featurePath, se, external)
	}
	return entry
}

func (b *statusBuilder) buildCheckoutMaterialization(entry *AgentStatusEntry, se StackEntry) {
	entry.Materialization = EntryMaterialization{Kind: MaterializationRef, State: MaterializedUnknown}
	entry.IsCurrentCheckout = b.currentCheckoutFlag(entry)

	if se.Repo != "" {
		entry.Materialization.State = MaterializedCrossRepo
		b.issue(IssueCrossRepoUnsupported, SeverityInfo, ScopeEntry, entry.Feature, entry.Name,
			fmt.Sprintf("entry targets repository %s; git probes are not supported", se.Repo), "")
		return
	}
	if b.ws.RepoRoot == "" {
		return
	}

	exists := gitRefExists(b.ws.RepoRoot, entry.GitBranch)
	entry.Materialization.RefExists = boolPtr(exists)
	if exists {
		entry.Materialization.State = MaterializedPresent
		return
	}
	entry.Materialization.State = MaterializedMissing
	if se.Archived {
		b.issue(IssueRefMissingArchived, SeverityInfo, ScopeEntry, entry.Feature, entry.Name,
			fmt.Sprintf("archived branch %q has no git ref", entry.GitBranch), "")
		return
	}
	b.issue(IssueRefMissing, SeverityWarning, ScopeEntry, entry.Feature, entry.Name,
		fmt.Sprintf("git branch %q does not exist", entry.GitBranch),
		fmt.Sprintf("run: tws new %s %s", entry.Feature, entry.Name))
}

// currentCheckoutFlag never fabricates a false: a detached or unreadable HEAD
// with no session record answers "unknown".
func (b *statusBuilder) currentCheckoutFlag(entry *AgentStatusEntry) *bool {
	if b.sessionFeature == entry.Feature && b.sessionName == entry.Name {
		return boolPtr(true)
	}
	w := b.report.Workspace
	if w.Branch == nil || (w.Detached != nil && *w.Detached) {
		return nil
	}
	return boolPtr(*w.Branch == entry.GitBranch)
}

func (b *statusBuilder) buildExternalMaterialization(entry *AgentStatusEntry, feature, featurePath string, se StackEntry, external *SyncState) {
	wtPath := filepath.Join(featurePath, "worktrees", se.Name)
	entry.Materialization = EntryMaterialization{Kind: MaterializationWorktree, State: MaterializedUnknown}

	if se.Repo != "" {
		entry.Materialization.State = MaterializedCrossRepo
		b.issue(IssueCrossRepoUnsupported, SeverityInfo, ScopeEntry, feature, se.Name,
			fmt.Sprintf("entry targets repository %s; git and worktree probes are not supported", se.Repo), "")
		b.appendBranchTmux(entry, wtPath)
		return
	}

	if b.ws.RepoRoot == "" {
		// Degraded workspace: the workspace-scoped warning already covers it.
		entry.Materialization.Path = strPtr(wtPath)
		b.appendBranchTmux(entry, wtPath)
		return
	}

	entry.Materialization.RefExists = boolPtr(gitRefExists(b.ws.RepoRoot, entry.GitBranch))

	info, statErr := os.Stat(wtPath)
	switch {
	case statErr == nil && info.IsDir():
		entry.Materialization.State = MaterializedPresent
		entry.Materialization.Path = strPtr(wtPath)
		b.probeWorktree(entry, wtPath, external)
	case statErr == nil || !errors.Is(statErr, fs.ErrNotExist):
		entry.Materialization.State = MaterializedUnknown
		entry.Materialization.Path = strPtr(wtPath)
		b.issue(IssueWorktreeUnreadable, SeverityWarning, ScopeEntry, feature, se.Name,
			fmt.Sprintf("worktree path %s is not a readable directory", wtPath),
			"inspect "+wtPath)
	case se.Archived:
		entry.Materialization.State = MaterializedArchived
	case b.wt.Available && b.wt.Prunable[entry.GitBranch]:
		entry.Materialization.State = MaterializedPrunableMissing
		b.issue(IssueWorktreePrunableMissing, SeverityWarning, ScopeEntry, feature, se.Name,
			fmt.Sprintf("worktree %s is gone and git still lists a prunable entry for %q", wtPath, entry.GitBranch),
			fmt.Sprintf("run: git worktree prune, then: tws add %s %s", feature, se.Name))
	default:
		entry.Materialization.State = MaterializedMissing
		b.issue(IssueWorktreeMissing, SeverityWarning, ScopeEntry, feature, se.Name,
			fmt.Sprintf("worktree %s does not exist", wtPath),
			fmt.Sprintf("run: tws add %s %s", feature, se.Name))
	}

	b.appendBranchTmux(entry, wtPath)
}

// probeWorktree fills checked_out_branch and dirty for a present worktree.
// Both helpers swallow their errors, so the dirty probe is gated on a
// successful branch probe: otherwise a broken checkout reads as clean.
func (b *statusBuilder) probeWorktree(entry *AgentStatusEntry, wtPath string, external *SyncState) {
	branch, detached := healthCurrentBranch(wtPath)
	if branch == "" {
		b.issue(IssueWorktreeUnreadable, SeverityWarning, ScopeEntry, entry.Feature, entry.Name,
			fmt.Sprintf("could not read the checked-out branch in %s", wtPath),
			"inspect "+wtPath)
		return
	}
	if !detached {
		entry.Materialization.CheckedOutBranch = strPtr(branch)
		if entry.GitBranch != "" && branch != entry.GitBranch {
			b.issue(IssueWorktreeWrongBranch, SeverityWarning, ScopeEntry, entry.Feature, entry.Name,
				fmt.Sprintf("worktree %s has %q checked out, expected %q", wtPath, branch, entry.GitBranch),
				fmt.Sprintf("run: git -C %s switch %s", wtPath, entry.GitBranch))
		}
	}

	dirty := gitDirty(wtPath)
	entry.Materialization.Dirty = boolPtr(dirty)
	if !dirty {
		return
	}
	if syncWantsBranch(external, entry.Name) {
		b.issue(IssueWorktreeDirtyBlocking, SeverityWarning, ScopeEntry, entry.Feature, entry.Name,
			fmt.Sprintf("worktree %s is dirty and an unfinished sync needs it", wtPath),
			fmt.Sprintf("commit or stash in %s before: tws sync %s", wtPath, entry.Feature))
		return
	}
	b.issue(IssueWorktreeDirty, SeverityInfo, ScopeEntry, entry.Feature, entry.Name,
		fmt.Sprintf("worktree %s has uncommitted changes", wtPath), "")
}

// syncWantsBranch matches on StackEntry.Name: external sync state records tws
// names, not git branches.
func syncWantsBranch(state *SyncState, name string) bool {
	if state == nil {
		return false
	}
	if state.FailedBranch == name {
		return true
	}
	for _, p := range state.Pending {
		if p == name {
			return true
		}
	}
	return false
}

func (b *statusBuilder) appendBranchTmux(entry *AgentStatusEntry, wtPath string) {
	name := ExternalTmuxSessionName(entry.Feature, entry.Name)
	presence, code, detail, found := b.verifyTmuxName(name, wtPath)
	if !found {
		return
	}
	obs := SessionObservation{
		Kind:            SessionKindExternalTmux,
		Presence:        presence,
		AgentState:      AgentStateUnknown,
		Stage:           strPtr("tmux"),
		StageRecognized: true,
		TmuxSession:     strPtr(name),
		Path:            strPtr(wtPath),
		RecordState:     string(DirectRecordOK),
	}
	if code != "" {
		obs.Detail = strPtr(detail)
		b.issue(code, SeverityWarning, ScopeEntry, entry.Feature, entry.Name, detail,
			"check which tmux session owns that name")
	}
	entry.Sessions = append(entry.Sessions, obs)
}

// appendDirectSessions reads the external per-invocation records for one
// branch. Checkout mode never reaches this path.
func (b *statusBuilder) appendDirectSessions(entry *AgentStatusEntry, featurePath, branchID string) {
	want := &DirectSessionIdentity{Feature: entry.Feature, Name: entry.Name}
	records, err := LoadDirectSessions(featurePath, branchID, want)
	if err != nil {
		b.issue(IssueDirectRecordInvalid, SeverityWarning, ScopeEntry, entry.Feature, entry.Name,
			fmt.Sprintf("direct session records could not be read: %v", err),
			"inspect "+filepath.Join(DirectSessionsDir(featurePath), branchID))
		return
	}

	for _, rec := range records {
		obs := SessionObservation{
			Kind:            SessionKindExternalDirect,
			AgentState:      AgentStateUnknown,
			StageRecognized: recognizedSessionStages[rec.Record.Stage],
			RecordState:     string(rec.State),
		}
		if rec.Record.Stage != "" {
			obs.Stage = strPtr(rec.Record.Stage)
		}
		if rec.Record.OwnerPID > 0 {
			obs.OwnerPID = intPtr(rec.Record.OwnerPID)
		}
		if rec.Record.ChildPID > 0 {
			obs.ChildPID = intPtr(rec.Record.ChildPID)
		}
		if rec.Record.Path != "" {
			obs.Path = strPtr(rec.Record.Path)
		}
		if rec.Record.Agent != "" {
			obs.Agent = strPtr(rec.Record.Agent)
		}
		if rec.Record.StartedAt != "" {
			obs.StartedAt = strPtr(rec.Record.StartedAt)
		}
		if rec.Record.UpdatedAt != "" {
			obs.UpdatedAt = strPtr(rec.Record.UpdatedAt)
		}
		if id := directRecordIDFor(rec); id != "" {
			obs.RecordID = strPtr(id)
		}
		if rec.Problem != "" {
			obs.Detail = strPtr(rec.Problem)
		}

		switch rec.State {
		case DirectRecordUnsupported:
			obs.Presence = PresenceUnknown
			b.issue(IssueDirectRecordUnsupported, SeverityWarning, ScopeEntry, entry.Feature, entry.Name,
				fmt.Sprintf("direct session record %s: %s", DirectRecordDisplayPath(rec.File), rec.Problem),
				"written by a newer tws; upgrade tws")
		case DirectRecordInvalid:
			obs.Presence = PresenceUnknown
			b.issue(IssueDirectRecordInvalid, SeverityWarning, ScopeEntry, entry.Feature, entry.Name,
				fmt.Sprintf("direct session record %s: %s", DirectRecordDisplayPath(rec.File), rec.Problem),
				"inspect "+DirectRecordDisplayPath(rec.File))
		default:
			liveness := b.opts.Proc.Probe(rec.Record.OwnerPID)
			obs.Liveness = strPtr(string(liveness))
			switch liveness {
			case ProcessLive:
				obs.Presence = PresencePresent
			case ProcessDead:
				obs.Presence = PresenceStale
				b.issue(IssueDirectRecordStale, SeverityWarning, ScopeEntry, entry.Feature, entry.Name,
					fmt.Sprintf("direct session record %s owner pid %d is dead (stage %s, started %s)",
						directRecordIDFor(rec), rec.Record.OwnerPID, rec.Record.Stage, rec.Record.StartedAt),
					fmt.Sprintf("run: tws close %s %s", entry.Feature, entry.Name))
			default:
				obs.Presence = PresenceUnknown
				b.issue(IssueDirectRecordUnknown, SeverityWarning, ScopeEntry, entry.Feature, entry.Name,
					fmt.Sprintf("direct session record %s owner pid %d could not be verified",
						directRecordIDFor(rec), rec.Record.OwnerPID),
					fmt.Sprintf("check pid %d; it may belong to another user", rec.Record.OwnerPID))
			}
		}
		entry.Sessions = append(entry.Sessions, obs)
	}
}

func directRecordIDFor(rec LoadedDirectSession) string {
	if id := DirectRecordID(rec.Record.Token); id != "" {
		return id
	}
	stem := strings.TrimSuffix(filepath.Base(rec.File), ".json")
	return DirectRecordID(stem)
}

// ---------- rollups and counters ----------

func recomputeAgentStatusRollups(r *AgentStatusReport) {
	featureOwnAttention := map[string]bool{}
	for _, iss := range r.Issues {
		if iss.Scope == ScopeFeature && iss.Feature != nil && (iss.Severity == SeverityWarning || iss.Severity == SeverityError) {
			featureOwnAttention[*iss.Feature] = true
		}
	}

	anyFeatureNeedsAttention := false
	for fi := range r.Features {
		f := &r.Features[fi]
		anyEntryNeedsAttention := false
		var entryPresences []RuntimePresence

		for ei := range f.Entries {
			e := &f.Entries[ei]
			e.SessionCounts = countSessions(e.Sessions)
			var presences []RuntimePresence
			for _, s := range e.Sessions {
				presences = append(presences, s.Presence)
			}
			e.RuntimePresence = RollupPresence(presences)
			own := ownIssues(r.Issues, ScopeEntry, e.Feature, e.Name)
			e.Attention = buildRollup(RollupAttention(e.RuntimePresence, own, false), own)
			e.FeatureAttention = featureOwnAttention[f.Feature]
			if e.Attention.Status == AttentionNeedsAttention {
				anyEntryNeedsAttention = true
			}
			entryPresences = append(entryPresences, e.RuntimePresence)
		}

		if f.FeatureTmux != nil {
			entryPresences = append(entryPresences, f.FeatureTmux.Presence)
		}
		f.RuntimePresence = RollupPresence(entryPresences)
		ownFeature := ownIssues(r.Issues, ScopeFeature, f.Feature, "")
		f.Attention = buildRollup(RollupAttention(f.RuntimePresence, ownFeature, anyEntryNeedsAttention), ownFeature)
		if f.Attention.Status == AttentionNeedsAttention {
			anyFeatureNeedsAttention = true
		}
	}

	var wsPresences []RuntimePresence
	for _, f := range r.Features {
		wsPresences = append(wsPresences, f.RuntimePresence)
	}
	if r.Workspace.CheckoutSession != nil {
		wsPresences = append(wsPresences, r.Workspace.CheckoutSession.Presence)
	}
	r.Workspace.RuntimePresence = RollupPresence(wsPresences)
	ownWorkspace := ownIssues(r.Issues, ScopeWorkspace, "", "")
	r.Workspace.Attention = buildRollup(
		RollupAttention(r.Workspace.RuntimePresence, ownWorkspace, anyFeatureNeedsAttention), ownWorkspace)
}

func ownIssues(issues []AgentStatusIssue, scope IssueScope, feature, name string) []AgentStatusIssue {
	var own []AgentStatusIssue
	for _, iss := range issues {
		if iss.Scope != scope {
			continue
		}
		if derefStr(iss.Feature) != feature || derefStr(iss.Name) != name {
			continue
		}
		own = append(own, iss)
	}
	return own
}

func buildRollup(status AttentionStatus, own []AgentStatusIssue) AttentionRollup {
	rollup := AttentionRollup{Status: status, Codes: []string{}}
	seen := map[string]bool{}
	for _, iss := range own {
		if iss.Severity == SeverityWarning || iss.Severity == SeverityError {
			rollup.IssueCount++
		}
		if !seen[iss.Code] {
			seen[iss.Code] = true
			rollup.Codes = append(rollup.Codes, iss.Code)
		}
	}
	sort.Strings(rollup.Codes)
	return rollup
}

func countSessions(sessions []SessionObservation) SessionCounts {
	c := SessionCounts{Total: len(sessions)}
	for _, s := range sessions {
		switch s.Presence {
		case PresencePresent:
			c.Live++
		case PresenceStale:
			c.Stale++
		case PresenceUnknown:
			c.Unknown++
		}
		if s.RecordState != string(DirectRecordOK) {
			c.Invalid++
		}
	}
	return c
}

func recomputeAgentStatusSummary(r *AgentStatusReport) {
	s := AgentStatusSummary{Features: len(r.Features)}
	for _, f := range r.Features {
		for _, e := range f.Entries {
			s.Entries++
			switch e.Attention.Status {
			case AttentionNeedsAttention:
				s.NeedsAttention++
			case AttentionActive:
				s.Active++
			default:
				s.Idle++
			}
			switch e.RuntimePresence {
			case PresencePresent:
				s.RuntimePresent++
			case PresenceStale:
				s.RuntimeStale++
			case PresenceUnknown:
				s.RuntimeUnknown++
			default:
				s.RuntimeAbsent++
			}
		}
	}
	s.Issues = len(r.Issues)
	for _, iss := range r.Issues {
		switch iss.Severity {
		case SeverityWarning:
			s.Warnings++
		case SeverityError:
			s.Errors++
		}
	}
	r.Summary = s
}

// ---------- filtering ----------

// FilterFeature narrows the document to one feature. Workspace-scoped issues
// and the workspace checkout session are deliberately kept: suppressing them
// would hide the very thing an operator must fix.
//
// workspace.runtime_presence is deliberately NOT recomputed: presence is a
// runtime fact about the whole workspace, and recomputing it would make
// `tws status auth` report a different runtime than `tws status` for the same
// instant.
func (r *AgentStatusReport) FilterFeature(feature string) error {
	var kept []AgentStatusFeature
	for _, f := range r.Features {
		if f.Feature == feature {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		return fmt.Errorf("feature not found: %s", feature)
	}
	r.Features = kept

	var issues []AgentStatusIssue
	for _, iss := range r.Issues {
		if iss.Scope == ScopeWorkspace || (iss.Feature != nil && *iss.Feature == feature) {
			issues = append(issues, iss)
		}
	}
	r.Issues = issues

	recomputeAgentStatusSummary(r)

	ownWorkspace := ownIssues(r.Issues, ScopeWorkspace, "", "")
	childNeedsAttention := kept[0].Attention.Status == AttentionNeedsAttention
	r.Workspace.Attention = buildRollup(
		RollupAttention(r.Workspace.RuntimePresence, ownWorkspace, childNeedsAttention), ownWorkspace)
	return nil
}

// ---------- normalization ----------

// NormalizeAgentStatus converts nil slices to empty slices and applies the
// deterministic ordering. It is idempotent and must run before encoding and
// before formatting.
func NormalizeAgentStatus(r *AgentStatusReport) {
	if r == nil {
		return
	}
	if r.Features == nil {
		r.Features = []AgentStatusFeature{}
	}
	if r.Issues == nil {
		r.Issues = []AgentStatusIssue{}
	}
	if r.Workspace.Attention.Codes == nil {
		r.Workspace.Attention.Codes = []string{}
	}
	for fi := range r.Features {
		f := &r.Features[fi]
		if f.Entries == nil {
			f.Entries = []AgentStatusEntry{}
		}
		if f.Attention.Codes == nil {
			f.Attention.Codes = []string{}
		}
		if f.Sync != nil {
			if f.Sync.Pending == nil {
				f.Sync.Pending = []string{}
			}
			if f.Sync.Completed == nil {
				f.Sync.Completed = []string{}
			}
			if f.Sync.Skipped == nil {
				f.Sync.Skipped = []string{}
			}
		}
		for ei := range f.Entries {
			e := &f.Entries[ei]
			if e.Sessions == nil {
				e.Sessions = []SessionObservation{}
			}
			if e.Attention.Codes == nil {
				e.Attention.Codes = []string{}
			}
			sortSessionObservations(e.Sessions)
		}
	}
	sort.SliceStable(r.Features, func(i, j int) bool { return r.Features[i].Feature < r.Features[j].Feature })
	sortAgentStatusIssues(r.Issues)
}

func sortSessionObservations(sessions []SessionObservation) {
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		as, bs := derefStr(a.StartedAt), derefStr(b.StartedAt)
		if (as == "") != (bs == "") {
			// Entries with a null started_at sort last.
			return bs == ""
		}
		if as != bs {
			return as < bs
		}
		if ar, br := derefStr(a.RecordID), derefStr(b.RecordID); ar != br {
			return ar < br
		}
		return a.Kind < b.Kind
	})
}

func sortAgentStatusIssues(issues []AgentStatusIssue) {
	rank := func(s IssueScope) int {
		switch s {
		case ScopeWorkspace:
			return 0
		case ScopeFeature:
			return 1
		default:
			return 2
		}
	}
	nullFirst := func(a, b *string) (int, bool) {
		switch {
		case a == nil && b == nil:
			return 0, false
		case a == nil:
			return -1, true
		case b == nil:
			return 1, true
		case *a != *b:
			return strings.Compare(*a, *b), true
		}
		return 0, false
	}
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if ra, rb := rank(a.Scope), rank(b.Scope); ra != rb {
			return ra < rb
		}
		if cmp, decided := nullFirst(a.Feature, b.Feature); decided {
			return cmp < 0
		}
		if cmp, decided := nullFirst(a.Name, b.Name); decided {
			return cmp < 0
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
}

// ---------- helpers ----------

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
func boolPtr(b bool) *bool    { return &b }

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------- human output ----------

// attentionIcon maps a rollup onto the severity glyph that carries the same
// urgency. It invents no new vocabulary.
func attentionIcon(s AttentionStatus) string {
	switch s {
	case AttentionNeedsAttention:
		return severityIcon(SeverityWarning)
	case AttentionActive:
		return severityIcon(SeverityInfo)
	default:
		return severityIcon(SeverityOK)
	}
}

func attentionLabel(s AttentionStatus) string {
	if s == AttentionNeedsAttention {
		return "attn"
	}
	return string(s)
}

// FormatAgentStatus renders the human view. Callers must run
// NormalizeAgentStatus first.
func FormatAgentStatus(r *AgentStatusReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Workspace: %s (mode: %s)\n", r.Workspace.MetadataRoot, r.Workspace.Mode)
	// The workspace verdict is always printed, never only when something is
	// wrong: it is the one field an operator polls, it inherits upward, and a
	// workspace-only fault has no table row to show it on.
	fmt.Fprintf(&b, "  Attention: %s %s\n",
		attentionIcon(r.Workspace.Attention.Status), r.Workspace.Attention.Status)
	if r.Workspace.StableID != nil {
		fmt.Fprintf(&b, "  ID:        %s\n", *r.Workspace.StableID)
	}
	if r.Workspace.RepoRoot != nil {
		fmt.Fprintf(&b, "  Repo:      %s\n", *r.Workspace.RepoRoot)
	}
	if r.Workspace.Degraded {
		fmt.Fprintf(&b, "  Degraded:  %s\n", derefStr(r.Workspace.DegradedReason))
	}
	fmt.Fprintf(&b, "  tmux:      %s\n", formatTmuxStatus(r.Workspace.Tmux))

	rows := statusRows(r)
	if len(r.Features) == 0 {
		// The same string tws list uses. Issue blocks and the tail still
		// follow: a workspace-scoped fault has no table row to live on.
		b.WriteString("\nNo features found. Use 'tws add <feature>' to create one.\n")
	} else if len(rows) > 0 {
		b.WriteString("\n")
		writeAgentStatusTable(&b, rows)
	}

	writeAgentStatusIssueBlocks(&b, r)
	writeAgentStatusTail(&b, r)
	return b.String()
}

// writeAgentStatusTable renders the branch table with per-column widths.
func writeAgentStatusTable(b *strings.Builder, rows [][]string) {
	header := []string{"STATUS", "BRANCH", "PRESENCE", "AGENT", "SESSIONS", "DETAIL"}
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	writeRow := func(cells []string) {
		var line strings.Builder
		for i, cell := range cells {
			if i == len(cells)-1 {
				line.WriteString(cell)
				break
			}
			fmt.Fprintf(&line, "%-*s  ", widths[i], cell)
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteString("\n")
	}
	writeRow(header)
	for _, row := range rows {
		writeRow(row)
	}
}

// writeAgentStatusIssueBlocks renders every issue in the document exactly
// once, grouped by its own home: branch, then feature, then workspace.
//
// The branch blocks exist because the table's DETAIL column is a summary: a
// row that reads `[!] attn` must never be the only trace of an entry-scoped
// issue, or its code, message, and — most importantly — its guidance would be
// reachable only through --json.
func writeAgentStatusIssueBlocks(b *strings.Builder, r *AgentStatusReport) {
	for _, f := range r.Features {
		for _, e := range f.Entries {
			own := ownIssues(r.Issues, ScopeEntry, e.Feature, e.Name)
			if len(own) == 0 {
				continue
			}
			fmt.Fprintf(b, "\nBranch: %s/%s\n", e.Feature, e.Name)
			for _, iss := range own {
				b.WriteString("  " + formatIssueLine(iss))
			}
		}
	}
	for _, f := range r.Features {
		own := ownIssues(r.Issues, ScopeFeature, f.Feature, "")
		if len(own) == 0 {
			continue
		}
		fmt.Fprintf(b, "\nFeature: %s\n", f.Feature)
		for _, iss := range own {
			b.WriteString("  " + formatIssueLine(iss))
		}
	}
	if own := ownIssues(r.Issues, ScopeWorkspace, "", ""); len(own) > 0 {
		b.WriteString("\nWorkspace:\n")
		for _, iss := range own {
			b.WriteString("  " + formatIssueLine(iss))
		}
	}
}

// writeAgentStatusTail states the counters that describe branches only. The
// workspace verdict is deliberately not repeated here: it is in the header,
// where it cannot be mistaken for a branch count.
func writeAgentStatusTail(b *strings.Builder, r *AgentStatusReport) {
	fmt.Fprintf(b, "\n%d branch(es): %d active, %d idle, %d needs attention. %d issue(s).\n",
		r.Summary.Entries, r.Summary.Active, r.Summary.Idle, r.Summary.NeedsAttention, r.Summary.Issues)
}

func formatIssueLine(iss AgentStatusIssue) string {
	line := fmt.Sprintf("%s %s: %s", severityIcon(iss.Severity), iss.Code, collapseIssueText(iss.Message))
	if iss.Guidance != nil {
		line += "; " + collapseIssueText(*iss.Guidance)
	}
	return line + "\n"
}

// collapseIssueText folds an embedded newline into a space. A message may
// quote a parser error that spans lines (yaml errors do), and one issue must
// occupy exactly one line so the block structure stays readable and greppable.
// The JSON document keeps the message verbatim.
func collapseIssueText(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.Join(strings.Fields(s), " ")
}

func formatTmuxStatus(t TmuxStatus) string {
	if !t.Available {
		return "not available"
	}
	if !t.ServerRunning {
		return "available (no server)"
	}
	return fmt.Sprintf("available (%d sessions)", t.SessionCount)
}

func statusRows(r *AgentStatusReport) [][]string {
	var rows [][]string
	for _, f := range r.Features {
		for _, e := range f.Entries {
			branch := f.Feature + "/" + e.Name
			if e.GitBranch != e.Name {
				branch += fmt.Sprintf(" (git: %s)", e.GitBranch)
			}
			rows = append(rows, []string{
				fmt.Sprintf("%s %s", attentionIcon(e.Attention.Status), attentionLabel(e.Attention.Status)),
				branch,
				string(e.RuntimePresence),
				string(e.AgentState),
				fmt.Sprintf("%d/%d", e.SessionCounts.Live, e.SessionCounts.Total),
				formatEntryDetail(e),
			})
		}
	}
	return rows
}

func formatEntryDetail(e AgentStatusEntry) string {
	var sessions []string
	for i, s := range e.Sessions {
		if i == 2 {
			sessions = append(sessions, fmt.Sprintf("+%d more (see --json)", len(e.Sessions)-2))
			break
		}
		sessions = append(sessions, formatSessionSummary(s))
	}

	var tags []string
	if e.Archived {
		tags = append(tags, "[archived]")
	}
	if e.IsCurrentCheckout != nil && *e.IsCurrentCheckout {
		tags = append(tags, "[current]")
	}
	switch e.Materialization.State {
	case MaterializedMissing:
		tags = append(tags, "[missing]")
	case MaterializedPrunableMissing:
		tags = append(tags, "[prunable-missing]")
	case MaterializedCrossRepo:
		tags = append(tags, "[cross-repo]")
	}
	if e.Materialization.CheckedOutBranch != nil && *e.Materialization.CheckedOutBranch != e.GitBranch {
		tags = append(tags, "[wrong-branch]")
	}
	if e.Materialization.Dirty != nil && *e.Materialization.Dirty {
		tags = append(tags, "[dirty]")
	}

	// Session summaries contain spaces, so a space-joined list of two of them
	// reads as one sentence. They are separated by "; "; the bracketed tags
	// are self-delimiting and stay space-joined.
	var parts []string
	if len(sessions) > 0 {
		parts = append(parts, strings.Join(sessions, "; "))
	}
	if len(tags) > 0 {
		parts = append(parts, strings.Join(tags, " "))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func formatSessionSummary(s SessionObservation) string {
	kind := "direct"
	if s.Kind == SessionKindCheckoutTmux || s.Kind == SessionKindExternalTmux {
		kind = "tmux"
	}
	if kind == "tmux" {
		return fmt.Sprintf("tmux %s %s", derefStr(s.TmuxSession), s.Presence)
	}
	if s.Presence == PresencePresent {
		pid := 0
		if s.ChildPID != nil {
			pid = *s.ChildPID
		} else if s.OwnerPID != nil {
			pid = *s.OwnerPID
		}
		return fmt.Sprintf("direct stage=%s pid %d", derefStr(s.Stage), pid)
	}
	owner := 0
	if s.OwnerPID != nil {
		owner = *s.OwnerPID
	}
	if id := derefStr(s.RecordID); id != "" {
		return fmt.Sprintf("direct record %s owner pid %d %s", id, owner, s.Presence)
	}
	return fmt.Sprintf("direct owner pid %d %s", owner, s.Presence)
}
