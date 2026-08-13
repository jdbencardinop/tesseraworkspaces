package internal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// ---------- Process / tmux seams ----------

// ProcessChecker abstracts PID liveness for testing.
type ProcessChecker interface {
	Alive(pid int) bool
}

// ProcessLiveness is the three-valued result of probing a PID. It exists
// because a bare Signal(0) collapses "no such process" and "not permitted"
// into one answer, which misreports a live process owned by another user.
type ProcessLiveness string

const (
	// ProcessLive means a process with that PID exists. It does not prove the
	// process is the one that was recorded: PID reuse is an accepted limit.
	ProcessLive ProcessLiveness = "live"
	// ProcessDead means the process provably does not exist.
	ProcessDead ProcessLiveness = "dead"
	// ProcessUnknown means liveness could not be determined (for example
	// EPERM, when the PID belongs to another user).
	ProcessUnknown ProcessLiveness = "unknown"
)

// ProcessProber abstracts three-valued PID liveness for testing.
type ProcessProber interface {
	Probe(pid int) ProcessLiveness
}

// proberAsChecker adapts a ProcessProber to the two-valued ProcessChecker
// expected by the existing health helpers, so no second liveness
// implementation is needed.
type proberAsChecker struct{ p ProcessProber }

func (a proberAsChecker) Alive(pid int) bool { return a.p.Probe(pid) == ProcessLive }

// TmuxChecker abstracts tmux session liveness for testing.
type TmuxChecker interface {
	HasSession(name string) bool
}

type realProcessChecker struct{}

func (realProcessChecker) Probe(pid int) ProcessLiveness {
	if pid <= 0 {
		return ProcessDead
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return ProcessDead
	}
	err = p.Signal(syscall.Signal(0))
	switch {
	case err == nil:
		return ProcessLive
	case errors.Is(err, os.ErrProcessDone), errors.Is(err, syscall.ESRCH):
		return ProcessDead
	default:
		// EPERM and anything else: not provably dead.
		return ProcessUnknown
	}
}

func (r realProcessChecker) Alive(pid int) bool { return r.Probe(pid) == ProcessLive }

// NewProcessProber returns the real three-valued PID prober. It exists so
// internal/cli can inject a fake without exporting realProcessChecker.
func NewProcessProber() ProcessProber { return realProcessChecker{} }

type realTmuxChecker struct{}

func (realTmuxChecker) HasSession(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

// ---------- Report severity ----------

// CheckoutSeverity distinguishes informational notes from actionable warnings.
type CheckoutSeverity string

const (
	SeverityOK      CheckoutSeverity = "ok"
	SeverityInfo    CheckoutSeverity = "info"
	SeverityWarning CheckoutSeverity = "warning"
	SeverityError   CheckoutSeverity = "error"
)

// ---------- Report sections ----------

// CheckoutWorkspaceHeader describes the workspace identity and current repo state.
type CheckoutWorkspaceHeader struct {
	Mode        WorkspaceMode `json:"mode"`
	StableID    string        `json:"stable_id"`
	RepoRoot    string        `json:"repo_root"`
	Metadata    string        `json:"metadata_root"`
	Branch      string        `json:"branch"`
	Detached    bool          `json:"detached"`
	Dirty       bool          `json:"dirty"`
	ActiveGitOp string        `json:"active_git_op,omitempty"` // merge, rebase, cherry-pick, revert, bisect
}

// CheckoutSyncReport describes a single checkout-sync state entry found in .tws/state.
type CheckoutSyncReport struct {
	Feature       string           `json:"feature"`
	Stage         string           `json:"stage"`
	FailureReason string           `json:"failure_reason,omitempty"`
	CurrentBranch string           `json:"current_branch,omitempty"`
	LockPID       int              `json:"lock_pid,omitempty"`
	LockLive      bool             `json:"lock_live"`
	Liveness      string           `json:"liveness"` // live, stale, invalid
	Guidance      string           `json:"guidance,omitempty"`
	Severity      CheckoutSeverity `json:"severity"`
}

// CheckoutSessionReport describes the checkout agent session and lock state.
type CheckoutSessionReport struct {
	Active      bool             `json:"active"`
	Feature     string           `json:"feature,omitempty"`
	Name        string           `json:"name,omitempty"`
	Mode        AgentSessionMode `json:"mode,omitempty"`
	PID         int              `json:"pid,omitempty"`
	TmuxSession string           `json:"tmux_session,omitempty"`
	OwnerLive   bool             `json:"owner_live"`
	LockHeld    bool             `json:"lock_held"`
	Liveness    string           `json:"liveness,omitempty"` // live, stale, mismatch
	Guidance    string           `json:"guidance,omitempty"`
	Severity    CheckoutSeverity `json:"severity"`
}

// AncestryStatus classifies the relationship between a branch and its base.
type AncestryStatus string

const (
	AncestryStatusCurrent   AncestryStatus = "current"
	AncestryStatusStale     AncestryStatus = "stale"
	AncestryStatusMissing   AncestryStatus = "missing"
	AncestryStatusDivergent AncestryStatus = "divergent"
	AncestryStatusCrossRepo AncestryStatus = "cross-repo-unsupported"
)

// CheckoutFeatureEntry describes a single stack entry in checkout mode.
type CheckoutFeatureEntry struct {
	Feature        string           `json:"feature"`
	Name           string           `json:"name"`
	GitBranch      string           `json:"git_branch"`
	Archived       bool             `json:"archived"`
	RefExists      bool             `json:"ref_exists"`
	Current        bool             `json:"current"`
	BaseName       string           `json:"base_name"`
	BaseGitBranch  string           `json:"base_git_branch"`
	AncestryStatus AncestryStatus   `json:"ancestry_status"`
	LocalHead      string           `json:"local_head,omitempty"`
	ParentHead     string           `json:"parent_head,omitempty"`
	Severity       CheckoutSeverity `json:"severity"`

	LocalHeadFull  string              `json:"local_head_full,omitempty"`
	ParentHeadFull string              `json:"parent_head_full,omitempty"`
	BaseKind       StackBaseKind       `json:"base_kind,omitempty"`
	BaseRef        string              `json:"base_ref,omitempty"`
	LastBaseSHA    string              `json:"last_base_sha,omitempty"`
	LastBaseShort  string              `json:"last_base_short,omitempty"`
	BaseRecord     StackBaseRecord     `json:"base_record,omitempty"`
	MergeBase      *string             `json:"merge_base"`
	MergeBaseShort string              `json:"merge_base_short,omitempty"`
	Reason         StackAncestryReason `json:"reason,omitempty"`
	Guidance       string              `json:"guidance,omitempty"`
	Notes          []StackEdgeNote     `json:"notes,omitempty"`
	RepoSource     StackRepoSource     `json:"repo_source,omitempty"`
}

// CheckoutContextLinkReport describes a single session context link.
type CheckoutContextLinkReport struct {
	Path     string           `json:"path"`
	Target   string           `json:"target"`
	Status   string           `json:"status"` // healthy, missing, replaced, not-symlink
	Severity CheckoutSeverity `json:"severity"`
}

// CheckoutHealthReport is the full read-only checkout doctor report.
type CheckoutHealthReport struct {
	Header   CheckoutWorkspaceHeader     `json:"header"`
	Sync     []CheckoutSyncReport        `json:"sync,omitempty"`
	Session  *CheckoutSessionReport      `json:"session,omitempty"`
	Features []CheckoutFeatureEntry      `json:"features,omitempty"`
	Links    []CheckoutContextLinkReport `json:"links,omitempty"`
	Issues   int                         `json:"issues"`
}

func (r *CheckoutHealthReport) HasErrors() bool {
	for _, sync := range r.Sync {
		if sync.Severity == SeverityError {
			return true
		}
	}
	if r.Session != nil && r.Session.Severity == SeverityError {
		return true
	}
	for _, entry := range r.Features {
		if entry.Severity == SeverityError {
			return true
		}
	}
	for _, link := range r.Links {
		if link.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (r *CheckoutHealthReport) FilterFeature(feature string) error {
	var features []CheckoutFeatureEntry
	for _, entry := range r.Features {
		if entry.Feature == feature {
			features = append(features, entry)
		}
	}
	if len(features) == 0 {
		return fmt.Errorf("feature not found: %s", feature)
	}
	r.Features = features
	var sync []CheckoutSyncReport
	for _, entry := range r.Sync {
		if entry.Feature == feature {
			sync = append(sync, entry)
		}
	}
	r.Sync = sync
	if r.Session != nil && r.Session.Feature != feature {
		r.Session = nil
		r.Links = nil
	}
	r.Issues = countIssues(r)
	return nil
}

// ---------- Builder ----------

// CheckoutHealthOpts configures the health report builder for testing.
type CheckoutHealthOpts struct {
	Proc ProcessChecker
	Tmux TmuxChecker
}

func defaultOpts() CheckoutHealthOpts {
	return CheckoutHealthOpts{
		Proc: realProcessChecker{},
		Tmux: realTmuxChecker{},
	}
}

// BuildCheckoutHealthReport generates a read-only checkout health report.
// It never mutates Git state, locks, or files.
func BuildCheckoutHealthReport(ws Workspace, opts *CheckoutHealthOpts) (*CheckoutHealthReport, error) {
	if opts == nil {
		d := defaultOpts()
		opts = &d
	}
	if opts.Proc == nil {
		opts.Proc = realProcessChecker{}
	}
	if opts.Tmux == nil {
		opts.Tmux = realTmuxChecker{}
	}

	report := &CheckoutHealthReport{}

	// 1. Workspace header
	header, err := buildHeader(ws)
	if err != nil {
		return nil, fmt.Errorf("cannot read workspace: %w", err)
	}
	report.Header = header

	// 2. Sync state entries
	report.Sync = buildSyncReports(ws, opts.Proc)

	// 3. Session report
	report.Session = buildSessionReport(ws, opts.Proc, opts.Tmux)

	// 4. Feature entries
	cfg := LoadConfig()
	features, fErr := buildFeatureEntries(ws, cfg)
	if fErr != nil {
		return nil, fmt.Errorf("cannot list features: %w", fErr)
	}
	report.Features = features

	// 5. Context links
	report.Links = buildContextLinks(ws)

	// 6. Count issues
	report.Issues = countIssues(report)

	return report, nil
}

// ---------- Header ----------

func buildHeader(ws Workspace) (CheckoutWorkspaceHeader, error) {
	h := CheckoutWorkspaceHeader{
		Mode:     ws.Mode,
		StableID: ws.StableID,
		RepoRoot: ws.RepoRoot,
		Metadata: ws.MetadataRoot,
	}

	// Current branch / detached
	branch, detached := healthCurrentBranch(ws.RepoRoot)
	h.Branch = branch
	h.Detached = detached

	// Dirty
	h.Dirty = gitDirty(ws.RepoRoot)

	// Active Git operation
	h.ActiveGitOp = gitActiveOp(ws.RepoRoot)

	return h, nil
}

func healthCurrentBranch(repo string) (string, bool) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", false
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" {
		// detached — try to get short SHA
		sha, sErr := exec.Command("git", "-C", repo, "rev-parse", "--short", "HEAD").Output()
		if sErr == nil {
			return strings.TrimSpace(string(sha)), true
		}
		return "HEAD", true
	}
	return b, false
}

// gitDirty preserves its legacy contract exactly: a probe failure collapses to
// "not dirty" for the existing callers. The probe itself is error-returning
// and read-only (GIT_OPTIONAL_LOCKS=0), so the index is no longer refreshed.
func gitDirty(repo string) bool {
	dirty, err := probeDirty(repo)
	if err != nil {
		return false
	}
	return dirty
}

// gitActiveOp preserves its legacy contract exactly: a missing .git, an
// unreadable or malformed gitdir pointer, a vanished or non-directory gitdir
// target, and a non-ENOENT marker stat all produced "" before (no marker was
// found) and still produce "" now (the probe errors).
func gitActiveOp(repo string) string {
	op, err := probeActiveGitOp(repo)
	if err != nil || op == StackStatusOpNone {
		return ""
	}
	return op
}

// ---------- Sync ----------

func buildSyncReports(ws Workspace, proc ProcessChecker) []CheckoutSyncReport {
	stateDir := ws.CheckoutStateDir()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil
	}

	var reports []CheckoutSyncReport
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Checkout sync transactions: <feature>-checkout-sync.yaml
		if !strings.HasSuffix(name, "-checkout-sync.yaml") {
			continue
		}
		feature := strings.TrimSuffix(name, "-checkout-sync.yaml")
		txPath := filepath.Join(stateDir, name)
		report := buildOneSyncReport(feature, txPath, stateDir, proc)
		reports = append(reports, report)
	}
	return reports
}

func buildOneSyncReport(feature, txPath, stateDir string, proc ProcessChecker) CheckoutSyncReport {
	r := CheckoutSyncReport{
		Feature:  feature,
		Severity: SeverityOK,
	}

	data, err := os.ReadFile(txPath)
	if err != nil {
		r.Liveness = "invalid"
		r.Severity = SeverityError
		r.Guidance = "state file unreadable; manually remove " + txPath
		return r
	}

	var tx CheckoutTransaction
	if err := yaml.Unmarshal(data, &tx); err != nil {
		r.Liveness = "invalid"
		r.Severity = SeverityError
		r.Guidance = "corrupt transaction state; manually remove " + txPath
		return r
	}

	r.Stage = string(tx.Stage)
	// Derive current branch from plan if available
	if tx.CurrentIndex >= 0 && tx.CurrentIndex < len(tx.Plan) {
		r.CurrentBranch = tx.Plan[tx.CurrentIndex].Branch
	}
	if tx.FailureMsg != "" {
		r.FailureReason = tx.FailureMsg
	} else if tx.FailureKind != "" {
		r.FailureReason = string(tx.FailureKind)
	}
	r.LockPID = tx.LockPID

	// Check lock file
	lockName := feature + "-checkout-sync.lock"
	lockPath := filepath.Join(stateDir, lockName)
	lockData, lockErr := os.ReadFile(lockPath)
	if lockErr != nil {
		// Transaction exists but no lock — stale
		r.Liveness = "stale"
		r.LockLive = false
		r.Severity = SeverityWarning
		r.Guidance = fmt.Sprintf("stale sync transaction; run: tws sync %s --abort", feature)
		return r
	}

	var lock LockInfo
	if yaml.Unmarshal(lockData, &lock) != nil || lock.PID <= 0 {
		r.Liveness = "invalid"
		r.Severity = SeverityError
		r.Guidance = "corrupt lock file; manually remove " + lockPath
		return r
	}

	r.LockPID = lock.PID
	if proc.Alive(lock.PID) {
		r.LockLive = true
		r.Liveness = "live"
		r.Severity = SeverityInfo
	} else {
		r.LockLive = false
		r.Liveness = "stale"
		r.Severity = SeverityWarning
		if tx.FailureMsg != "" {
			r.Guidance = fmt.Sprintf("sync failed (%s) at stage %s; run: tws sync %s --continue  or  tws sync %s --abort", tx.FailureMsg, tx.Stage, feature, feature)
		} else {
			r.Guidance = fmt.Sprintf("stale sync lock (pid %d dead) at stage %s; run: tws sync %s --continue  or  tws sync %s --abort", lock.PID, tx.Stage, feature, feature)
		}
	}
	return r
}

// ---------- Session ----------

func buildSessionReport(ws Workspace, proc ProcessChecker, tmux TmuxChecker) *CheckoutSessionReport {
	r := &CheckoutSessionReport{Severity: SeverityOK}

	state, err := LoadCheckoutAgentSession(ws)
	if err != nil {
		// No active session state
		// Check for orphan lock
		if _, lockErr := os.Stat(sessionLockDir(ws)); lockErr == nil {
			r.LockHeld = true
			r.Liveness = "mismatch"
			r.Severity = SeverityWarning
			r.Guidance = "session lock exists but no session state; run: tws close"
			return r
		}
		return nil
	}

	r.Active = true
	r.Feature = state.Feature
	r.Name = state.Name
	r.Mode = state.Mode
	r.PID = state.PID
	r.TmuxSession = state.TmuxSession

	// Check lock
	_, lockErr := os.Stat(sessionLockDir(ws))
	r.LockHeld = lockErr == nil

	// Check liveness
	switch state.Mode {
	case AgentSessionDirect:
		if proc.Alive(state.PID) {
			r.OwnerLive = true
			r.Liveness = "live"
			r.Severity = SeverityInfo
		} else {
			r.OwnerLive = false
			r.Liveness = "stale"
			r.Severity = SeverityWarning
			r.Guidance = fmt.Sprintf("session owner pid %d is dead; run: tws close", state.PID)
		}
	case AgentSessionTmux:
		if state.TmuxSession != "" && tmux.HasSession(state.TmuxSession) {
			r.OwnerLive = true
			r.Liveness = "live"
			r.Severity = SeverityInfo
		} else {
			r.OwnerLive = false
			r.Liveness = "stale"
			r.Severity = SeverityWarning
			r.Guidance = fmt.Sprintf("tmux session %q is gone; run: tws close", state.TmuxSession)
		}
	}

	// Check state/lock mismatch
	if r.Active && !r.LockHeld {
		r.Liveness = "mismatch"
		r.Severity = SeverityWarning
		r.Guidance = "session state exists but lock is missing; run: tws close"
	} else if !r.Active && r.LockHeld {
		r.Liveness = "mismatch"
		r.Severity = SeverityWarning
		r.Guidance = "session lock exists but no session state; run: tws close"
	}

	return r
}

// ---------- Features ----------

func buildFeatureEntries(ws Workspace, cfg Config) ([]CheckoutFeatureEntry, error) {
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		return nil, err
	}

	// Also load session to mark current
	var sessionFeature, sessionName string
	if sess, sErr := LoadCheckoutAgentSession(ws); sErr == nil {
		sessionFeature = sess.Feature
		sessionName = sess.Name
	}

	// Current branch for "current" detection
	currentBranch, _ := healthCurrentBranch(ws.RepoRoot)

	// Deliberately guard-free: features came from ListFeaturesResolved, which
	// already excluded space-owned names and failed closed on untrusted
	// spaces.yaml, so this continue cannot mask a spaces failure.
	var entries []CheckoutFeatureEntry
	for _, feature := range features {
		fp, resolveErr := ws.ResolveFeaturePath(feature)
		if resolveErr != nil {
			continue
		}
		stack, sErr := LoadStack(fp)
		if sErr != nil {
			continue
		}
		edges, _ := FeatureStackEdges(ws, cfg, feature, fp, stack)
		edges = ancestryEdgesFor(feature, stack, edges)
		for i, se := range stack.Branches {
			entry := buildOneFeatureEntry(feature, se, edges[i], currentBranch, sessionFeature, sessionName)
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// ancestryEdgesFor guarantees one edge per stack entry. FeatureStackEdges is
// already total over stack.Branches; a short slice would otherwise render the
// unmatched entries from a zero StackEdge, which reads as an evaluated
// `current` verdict with no severity. Falling back to an explicit unevaluated
// projection keeps the output honest instead.
func ancestryEdgesFor(feature string, stack Stack, edges []StackEdge) []StackEdge {
	if len(edges) == len(stack.Branches) {
		return edges
	}
	return UnevaluatedStackEdges(feature, stack, ReasonRepoUnavailable,
		"ancestry evaluation returned no result for this stack")
}

func buildOneFeatureEntry(feature string, se StackEntry, edge StackEdge, currentBranch, sessionFeature, sessionName string) CheckoutFeatureEntry {
	gitBranch := se.GitBranch()
	e := CheckoutFeatureEntry{
		Feature:   feature,
		Name:      se.Name,
		GitBranch: gitBranch,
		Archived:  se.Archived,
	}

	// Ref existence comes from the evaluator's peeled child probe, so the flag
	// and the classification are backed by one process and cannot disagree.
	e.RefExists = edge.RefExists

	// Current — branch matches what's checked out
	e.Current = currentBranch == gitBranch

	// Session current
	if sessionFeature == feature && sessionName == se.Name {
		e.Current = true
	}

	e.BaseName = edge.BaseName
	switch edge.BaseKind {
	case StackBaseStackEntry:
		e.BaseGitBranch = strings.TrimPrefix(edge.BaseRef, "refs/heads/")
	case StackBaseLiteralRef:
		e.BaseGitBranch = edge.BaseName
	default:
		e.BaseGitBranch = ""
	}

	e.AncestryStatus = edge.Status
	e.Severity = edge.Severity
	if e.Severity == "" {
		// An edge that reached no severity is informational, never a silent
		// zero value that renders as an unlabelled icon.
		e.Severity = SeverityInfo
	}
	e.LocalHead = edge.LocalHeadShort
	e.ParentHead = edge.ParentHeadShort
	e.LocalHeadFull = edge.LocalHead
	e.ParentHeadFull = edge.ParentHead
	e.BaseKind = edge.BaseKind
	e.BaseRef = edge.BaseRef
	e.LastBaseSHA = edge.LastBaseSHA
	e.LastBaseShort = edge.LastBaseShort
	e.BaseRecord = edge.BaseRecord
	e.MergeBase = edge.MergeBase
	e.MergeBaseShort = edge.MergeBaseShort
	e.Reason = edge.Reason
	e.Guidance = edge.Guidance
	e.Notes = edge.Notes
	e.RepoSource = edge.RepoSource

	return e
}

func gitRefExists(repo, ref string) bool {
	return exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", ref).Run() == nil
}

// ---------- Context Links ----------

func buildContextLinks(ws Workspace) []CheckoutContextLinkReport {
	state, err := LoadCheckoutAgentSession(ws)
	if err != nil || len(state.Links) == 0 {
		return nil
	}

	var reports []CheckoutContextLinkReport
	for _, link := range state.Links {
		r := inspectContextLink(link)
		reports = append(reports, r)
	}
	return reports
}

func inspectContextLink(link SessionContextLink) CheckoutContextLinkReport {
	r := CheckoutContextLinkReport{
		Path:     link.Path,
		Target:   link.Target,
		Severity: SeverityOK,
	}

	info, err := os.Lstat(link.Path)
	if err != nil {
		r.Status = "missing"
		r.Severity = SeverityWarning
		return r
	}

	if info.Mode()&os.ModeSymlink == 0 {
		r.Status = "not-symlink"
		r.Severity = SeverityWarning
		return r
	}

	actual, readErr := os.Readlink(link.Path)
	if readErr != nil {
		r.Status = "missing"
		r.Severity = SeverityWarning
		return r
	}

	if actual != link.Target {
		r.Status = "replaced"
		r.Severity = SeverityWarning
		return r
	}

	r.Status = "healthy"
	return r
}

// ---------- Issue counting ----------

func countIssues(r *CheckoutHealthReport) int {
	n := 0
	if r.Header.Dirty {
		n++
	}
	if r.Header.ActiveGitOp != "" {
		n++
	}
	for _, s := range r.Sync {
		if s.Severity == SeverityWarning || s.Severity == SeverityError {
			n++
		}
	}
	if r.Session != nil && (r.Session.Severity == SeverityWarning || r.Session.Severity == SeverityError) {
		n++
	}
	for _, f := range r.Features {
		if f.Severity == SeverityWarning || f.Severity == SeverityError {
			n++
		}
	}
	for _, l := range r.Links {
		if l.Severity == SeverityWarning || l.Severity == SeverityError {
			n++
		}
	}
	return n
}

// ---------- Formatted output ----------

// FormatCheckoutHealth produces concise human-readable output.
func FormatCheckoutHealth(r *CheckoutHealthReport) string {
	var b strings.Builder

	// Header
	fmt.Fprintf(&b, "Workspace: %s (mode: %s)\n", r.Header.Metadata, r.Header.Mode)
	fmt.Fprintf(&b, "  ID:   %s\n", r.Header.StableID)
	fmt.Fprintf(&b, "  Repo: %s\n", r.Header.RepoRoot)
	if r.Header.Detached {
		fmt.Fprintf(&b, "  HEAD: %s (detached)\n", r.Header.Branch)
	} else {
		fmt.Fprintf(&b, "  Branch: %s\n", r.Header.Branch)
	}
	if r.Header.Dirty {
		b.WriteString("  [!] Working tree is dirty\n")
	}
	if r.Header.ActiveGitOp != "" {
		fmt.Fprintf(&b, "  [!] Active Git operation: %s\n", r.Header.ActiveGitOp)
	}

	// Sync
	if len(r.Sync) > 0 {
		b.WriteString("\nSync:\n")
		for _, s := range r.Sync {
			icon := severityIcon(s.Severity)
			fmt.Fprintf(&b, "  %s %s: stage=%s liveness=%s", icon, s.Feature, s.Stage, s.Liveness)
			if s.FailureReason != "" {
				fmt.Fprintf(&b, " failure=%s", s.FailureReason)
			}
			if s.LockPID > 0 {
				fmt.Fprintf(&b, " lock-pid=%d", s.LockPID)
			}
			b.WriteString("\n")
			if s.Guidance != "" {
				fmt.Fprintf(&b, "      %s\n", s.Guidance)
			}
		}
	}

	// Session
	if r.Session != nil {
		b.WriteString("\nSession:\n")
		if !r.Session.Active && r.Session.LockHeld {
			fmt.Fprintf(&b, "  [!] orphan lock — %s\n", r.Session.Guidance)
		} else if r.Session.Active {
			icon := severityIcon(r.Session.Severity)
			fmt.Fprintf(&b, "  %s %s/%s mode=%s liveness=%s", icon, r.Session.Feature, r.Session.Name, r.Session.Mode, r.Session.Liveness)
			if r.Session.PID > 0 {
				fmt.Fprintf(&b, " pid=%d", r.Session.PID)
			}
			if r.Session.TmuxSession != "" {
				fmt.Fprintf(&b, " tmux=%s", r.Session.TmuxSession)
			}
			b.WriteString("\n")
			if r.Session.Guidance != "" {
				fmt.Fprintf(&b, "      %s\n", r.Session.Guidance)
			}
		}
	}

	// Features
	if len(r.Features) > 0 {
		b.WriteString("\nFeatures:\n")
		for _, f := range r.Features {
			icon := severityIcon(f.Severity)
			tags := ""
			if f.Archived {
				tags += " [archived]"
			}
			if f.Current {
				tags += " [current]"
			}
			// Only a ref probe that actually ran can report a missing ref;
			// cross-repo and unevaluated edges are never probed locally.
			if !f.RefExists && f.AncestryStatus != AncestryStatusCrossRepo && f.AncestryStatus != "" {
				tags += " [ref-missing]"
			}
			fmt.Fprintf(&b, "  %s %s/%s", icon, f.Feature, f.Name)
			if f.GitBranch != f.Name {
				fmt.Fprintf(&b, " (git: %s)", f.GitBranch)
			}
			fmt.Fprintf(&b, " base=%s ancestry=%s%s", f.BaseName, ancestryDisplayStatus(f.AncestryStatus), tags)
			if f.LocalHead != "" {
				fmt.Fprintf(&b, " head=%s", f.LocalHead)
			}
			if f.ParentHead != "" {
				fmt.Fprintf(&b, " parent=%s", f.ParentHead)
			}
			b.WriteString("\n")
			for _, line := range checkoutFeatureDetailLines(f) {
				fmt.Fprintf(&b, "      %s\n", line)
			}
		}
	}

	// Context links
	if len(r.Links) > 0 {
		b.WriteString("\nContext Links:\n")
		for _, l := range r.Links {
			icon := severityIcon(l.Severity)
			fmt.Fprintf(&b, "  %s %s -> %s [%s]\n", icon, l.Path, l.Target, l.Status)
		}
	}

	// Summary
	if r.Issues == 0 {
		b.WriteString("\nAll healthy.\n")
	} else {
		fmt.Fprintf(&b, "\n%d issue(s) found.\n", r.Issues)
	}

	return b.String()
}

// checkoutFeatureDetailLines renders the additive indented detail lines that
// follow an entry line: at most one reason line, one guidance line, and one
// note line. The `base-record=` token is printed only when the record was
// actually consulted, so an edge that never reached the record cannot claim a
// verdict about it.
func checkoutFeatureDetailLines(f CheckoutFeatureEntry) []string {
	var lines []string
	if f.AncestryStatus != AncestryStatusCurrent {
		reason := fmt.Sprintf("reason: %s", f.Reason)
		if f.LastBaseShort != "" {
			reason += fmt.Sprintf(" last-base=%s", f.LastBaseShort)
		}
		if f.MergeBase != nil {
			reason += fmt.Sprintf(" merge-base=%s", f.MergeBaseShort)
		}
		if f.BaseRecord != "" && f.BaseRecord != StackBaseRecordPresent {
			reason += fmt.Sprintf(" base-record=%s", f.BaseRecord)
		}
		lines = append(lines, reason)
	}
	if f.Guidance != "" {
		lines = append(lines, f.Guidance)
	}
	for _, note := range f.Notes {
		lines = append(lines, fmt.Sprintf("note: %s", note.Detail))
	}
	return lines
}

func severityIcon(s CheckoutSeverity) string {
	switch s {
	case SeverityOK:
		return "[ok]"
	case SeverityInfo:
		return "[i]"
	case SeverityWarning:
		return "[!]"
	case SeverityError:
		return "[E]"
	default:
		return "[ ]"
	}
}

// ---------- Checkout list helpers ----------

// CheckoutListEntry represents one entry for the checkout list view.
type CheckoutListEntry struct {
	Feature        string `json:"feature"`
	Name           string `json:"name"`
	GitBranch      string `json:"git_branch"`
	Current        bool   `json:"current"`
	Archived       bool   `json:"archived"`
	AncestryStatus string `json:"ancestry_status"`
	SessionActive  bool   `json:"session_active"`
}

// BuildCheckoutList builds the checkout list view entries.
func BuildCheckoutList(ws Workspace) ([]CheckoutListEntry, error) {
	cfg := LoadConfig()
	return buildCheckoutListEntries(ws, cfg)
}

func buildCheckoutListEntries(ws Workspace, cfg Config) ([]CheckoutListEntry, error) {
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		return nil, err
	}

	currentBranch, _ := healthCurrentBranch(ws.RepoRoot)

	var sessionFeature, sessionName string
	if sess, sErr := LoadCheckoutAgentSession(ws); sErr == nil {
		sessionFeature = sess.Feature
		sessionName = sess.Name
	}

	// Deliberately guard-free, for the same reason as buildFeatureEntries.
	var entries []CheckoutListEntry
	for _, feature := range features {
		fp, resolveErr := ws.ResolveFeaturePath(feature)
		if resolveErr != nil {
			continue
		}
		stack, sErr := LoadStack(fp)
		if sErr != nil {
			continue
		}
		edges, _ := FeatureStackEdges(ws, cfg, feature, fp, stack)
		edges = ancestryEdgesFor(feature, stack, edges)
		for i, se := range stack.Branches {
			gitBranch := se.GitBranch()
			e := CheckoutListEntry{
				Feature:   feature,
				Name:      se.Name,
				GitBranch: gitBranch,
				Current:   currentBranch == gitBranch || (sessionFeature == feature && sessionName == se.Name),
				Archived:  se.Archived,
			}

			// Ancestry — same evaluator as doctor, so the two cannot disagree.
			e.AncestryStatus = string(edges[i].Status)

			// Session
			if sessionFeature == feature && sessionName == se.Name {
				e.SessionActive = true
			}

			entries = append(entries, e)
		}
	}
	return entries, nil
}

// FormatCheckoutList formats the checkout list for display.
func FormatCheckoutList(ws Workspace, entries []CheckoutListEntry) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Workspace: %s (mode: %s)\n\n", ws.MetadataRoot, ws.Mode)

	if len(entries) == 0 {
		b.WriteString("No features found. Use 'tws add <feature>' to create one.\n")
		return b.String()
	}

	// Group by feature
	featureEntries := make(map[string][]CheckoutListEntry)
	var featureOrder []string
	for _, e := range entries {
		if _, ok := featureEntries[e.Feature]; !ok {
			featureOrder = append(featureOrder, e.Feature)
		}
		featureEntries[e.Feature] = append(featureEntries[e.Feature], e)
	}
	for _, feature := range featureOrder {
		fEntries := featureEntries[feature]
		fmt.Fprintf(&b, "%s\n", feature)
		for i, e := range fEntries {
			connector := "├──"
			if i == len(fEntries)-1 {
				connector = "└──"
			}

			// Branch display
			branchDisplay := e.Name
			if e.GitBranch != e.Name {
				branchDisplay += fmt.Sprintf(" (git: %s)", e.GitBranch)
			}

			tags := ""
			if e.Current {
				tags += " *"
			}
			if e.Archived {
				tags += " [archived]"
			}
			if AncestryStatus(e.AncestryStatus) != AncestryStatusCurrent {
				tags += fmt.Sprintf(" [%s]", ancestryDisplayStatus(AncestryStatus(e.AncestryStatus)))
			}
			if e.SessionActive {
				tags += " [session]"
			}

			fmt.Fprintf(&b, "  %s %s%s\n", connector, branchDisplay, tags)
		}
		b.WriteString("\n")
	}

	return b.String()
}
