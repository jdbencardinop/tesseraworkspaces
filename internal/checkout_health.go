package internal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------- Process / tmux seams ----------

// ProcessChecker abstracts PID liveness for testing.
type ProcessChecker interface {
	Alive(pid int) bool
}

// TmuxChecker abstracts tmux session liveness for testing.
type TmuxChecker interface {
	HasSession(name string) bool
}

type realProcessChecker struct{}

func (realProcessChecker) Alive(pid int) bool { return processAlive(pid) }

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
	ActiveGitOp string        `json:"active_git_op,omitempty"` // merge, rebase, cherry-pick, revert
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
	features, fErr := buildFeatureEntries(ws)
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

func gitDirty(repo string) bool {
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

func gitActiveOp(repo string) string {
	gitDir := filepath.Join(repo, ".git")
	// Check if .git is a file (worktree) — resolve the actual git dir
	if info, err := os.Stat(gitDir); err == nil && !info.IsDir() {
		data, readErr := os.ReadFile(gitDir)
		if readErr == nil {
			line := strings.TrimSpace(string(data))
			if after, ok := strings.CutPrefix(line, "gitdir: "); ok {
				if !filepath.IsAbs(after) {
					after = filepath.Join(repo, after)
				}
				gitDir = filepath.Clean(after)
			}
		}
	}
	checks := []struct {
		marker string
		name   string
	}{
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(gitDir, c.marker)); err == nil {
			return c.name
		}
	}
	return ""
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

func buildFeatureEntries(ws Workspace) ([]CheckoutFeatureEntry, error) {
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
		for _, se := range stack.Branches {
			entry := buildOneFeatureEntry(ws, feature, se, stack, currentBranch, sessionFeature, sessionName)
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func buildOneFeatureEntry(ws Workspace, feature string, se StackEntry, stack Stack, currentBranch, sessionFeature, sessionName string) CheckoutFeatureEntry {
	gitBranch := se.GitBranch()
	e := CheckoutFeatureEntry{
		Feature:   feature,
		Name:      se.Name,
		GitBranch: gitBranch,
		Archived:  se.Archived,
		Severity:  SeverityOK,
	}

	// Ref exists
	e.RefExists = gitRefExists(ws.RepoRoot, gitBranch)

	// Current — branch matches what's checked out
	e.Current = currentBranch == gitBranch

	// Session current
	if sessionFeature == feature && sessionName == se.Name {
		e.Current = true
	}

	// Base resolution — find parent entry or use literal ref
	baseGitBranch := se.Base
	isParentEntry := false
	for _, parent := range stack.Branches {
		if parent.Name == se.Base {
			baseGitBranch = parent.GitBranch()
			isParentEntry = true
			break
		}
	}
	e.BaseName = se.Base
	e.BaseGitBranch = baseGitBranch

	// Cross-repo check
	if se.Repo != "" {
		e.AncestryStatus = AncestryStatusCrossRepo
		e.Severity = SeverityInfo
		return e
	}

	// Ancestry classification
	if !e.RefExists {
		e.AncestryStatus = AncestryStatusMissing
		e.Severity = SeverityWarning
		return e
	}

	if !gitRefExists(ws.RepoRoot, baseGitBranch) {
		e.AncestryStatus = AncestryStatusMissing
		e.Severity = SeverityWarning
		return e
	}

	// Get heads for display
	e.LocalHead = gitShortSHA(ws.RepoRoot, gitBranch)
	if isParentEntry {
		e.ParentHead = gitShortSHA(ws.RepoRoot, baseGitBranch)
	} else {
		e.ParentHead = gitShortSHA(ws.RepoRoot, baseGitBranch)
	}

	// merge-base check
	mb, mbErr := gitMergeBase(ws.RepoRoot, gitBranch, baseGitBranch)
	if mbErr != nil {
		e.AncestryStatus = AncestryStatusMissing
		e.Severity = SeverityWarning
		return e
	}

	baseHead := gitFullSHA(ws.RepoRoot, baseGitBranch)
	if mb == baseHead {
		e.AncestryStatus = AncestryStatusCurrent
	} else if childBehind, _ := gitIsAncestor(ws.RepoRoot, gitBranch, baseGitBranch); childBehind {
		e.AncestryStatus = AncestryStatusStale
		e.Severity = SeverityWarning
	} else {
		e.AncestryStatus = AncestryStatusDivergent
		e.Severity = SeverityWarning
	}

	return e
}

func gitRefExists(repo, ref string) bool {
	return exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", ref).Run() == nil
}

func gitShortSHA(repo, ref string) string {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--short", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitFullSHA(repo, ref string) string {
	out, err := exec.Command("git", "-C", repo, "rev-parse", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitMergeBase(repo, a, b string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "merge-base", a, b).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
			if !f.RefExists {
				tags += " [ref-missing]"
			}
			fmt.Fprintf(&b, "  %s %s/%s", icon, f.Feature, f.Name)
			if f.GitBranch != f.Name {
				fmt.Fprintf(&b, " (git: %s)", f.GitBranch)
			}
			fmt.Fprintf(&b, " base=%s ancestry=%s%s", f.BaseName, f.AncestryStatus, tags)
			if f.LocalHead != "" {
				fmt.Fprintf(&b, " head=%s", f.LocalHead)
			}
			if f.ParentHead != "" {
				fmt.Fprintf(&b, " parent=%s", f.ParentHead)
			}
			b.WriteString("\n")
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
		for _, se := range stack.Branches {
			gitBranch := se.GitBranch()
			e := CheckoutListEntry{
				Feature:   feature,
				Name:      se.Name,
				GitBranch: gitBranch,
				Current:   currentBranch == gitBranch || (sessionFeature == feature && sessionName == se.Name),
				Archived:  se.Archived,
			}

			// Ancestry
			if se.Repo != "" {
				e.AncestryStatus = string(AncestryStatusCrossRepo)
			} else if !gitRefExists(ws.RepoRoot, gitBranch) {
				e.AncestryStatus = string(AncestryStatusMissing)
			} else {
				baseRef := se.Base
				for _, parent := range stack.Branches {
					if parent.Name == se.Base {
						baseRef = parent.GitBranch()
						break
					}
				}
				if !gitRefExists(ws.RepoRoot, baseRef) {
					e.AncestryStatus = string(AncestryStatusMissing)
				} else if mb, mbErr := gitMergeBase(ws.RepoRoot, gitBranch, baseRef); mbErr != nil {
					e.AncestryStatus = string(AncestryStatusMissing)
				} else if mb == gitFullSHA(ws.RepoRoot, baseRef) {
					e.AncestryStatus = string(AncestryStatusCurrent)
				} else {
					e.AncestryStatus = string(AncestryStatusStale)
				}
			}

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
			if e.AncestryStatus != "" && e.AncestryStatus != string(AncestryStatusCurrent) {
				tags += fmt.Sprintf(" [%s]", e.AncestryStatus)
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
