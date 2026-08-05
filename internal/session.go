package internal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const checkoutSessionSchema = 1

type AgentSessionMode string

const (
	AgentSessionDirect AgentSessionMode = "direct"
	AgentSessionTmux   AgentSessionMode = "tmux"
)

type SessionContextLink struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

type CheckoutAgentSession struct {
	SchemaVersion  int                  `json:"schema_version"`
	WorkspaceID    string               `json:"workspace_id"`
	Feature        string               `json:"feature"`
	Name           string               `json:"name"`
	GitBranch      string               `json:"git_branch"`
	OriginalBranch string               `json:"original_branch"`
	OriginalHEAD   string               `json:"original_head"`
	Mode           AgentSessionMode     `json:"mode"`
	PID            int                  `json:"pid,omitempty"`
	TmuxSession    string               `json:"tmux_session,omitempty"`
	Stage          string               `json:"stage"`
	StartedAt      string               `json:"started_at"`
	RepoDir        string               `json:"repo_dir"`
	Links          []SessionContextLink `json:"links,omitempty"`
	ExcludeEntries []string             `json:"exclude_entries,omitempty"`
	LockToken      string               `json:"lock_token"`
}

type sessionLockOwner struct {
	Token     string `json:"token"`
	PID       int    `json:"pid"`
	CreatedAt string `json:"created_at"`
}

type SessionAgentRunner interface {
	Run(dir string, command []string) error
}
type SessionShellRunner interface{ Run(dir string) error }
type SessionTmuxRunner interface {
	NewSession(name, dir string, command []string) error
	AttachSession(name string) error
	HasSession(name string) bool
	KillSession(name string) error
}

func runSessionCommand(dir string, command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

type RealSessionAgentRunner struct{}

func (RealSessionAgentRunner) Run(dir string, command []string) error {
	return runSessionCommand(dir, command)
}

type RealSessionShellRunner struct{}

func (RealSessionShellRunner) Run(dir string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return runSessionCommand(dir, []string{shell})
}

type RealSessionTmuxRunner struct{}

func (RealSessionTmuxRunner) NewSession(name, dir string, command []string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", dir}
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	return exec.Command("tmux", args...).Run()
}
func (RealSessionTmuxRunner) AttachSession(name string) error {
	cmd := exec.Command("tmux", "attach-session", "-t", name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
func (RealSessionTmuxRunner) HasSession(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}
func (RealSessionTmuxRunner) KillSession(name string) error {
	return exec.Command("tmux", "kill-session", "-t", name).Run()
}

func sessionStateDir(ws Workspace) string  { return filepath.Join(ws.MetadataRoot, "state", "sessions") }
func sessionStatePath(ws Workspace) string { return filepath.Join(sessionStateDir(ws), "active.json") }
func sessionLockDir(ws Workspace) string {
	return filepath.Join(ws.MetadataRoot, "state", "checkout-session.lock")
}
func sessionLockOwnerPath(ws Workspace) string {
	return filepath.Join(sessionLockDir(ws), "owner.json")
}

func CheckoutAgentSessionName(workspaceID, feature, name string) string {
	identity := workspaceID + "/" + feature + "/" + name
	sum := sha256.Sum256([]byte(identity))
	suffix := hex.EncodeToString(sum[:4])
	prefix := sanitizeSessionPart(workspaceID + "_" + feature + "_" + name)
	max := 64 - len(suffix) - 1
	if len(prefix) > max {
		prefix = prefix[:max]
	}
	return prefix + "_" + suffix
}
func sanitizeSessionPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func SaveCheckoutAgentSession(ws Workspace, s *CheckoutAgentSession) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicSessionWrite(sessionStatePath(ws), data, 0600)
}
func LoadCheckoutAgentSession(ws Workspace) (*CheckoutAgentSession, error) {
	data, err := os.ReadFile(sessionStatePath(ws))
	if err != nil {
		return nil, err
	}
	var s CheckoutAgentSession
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
func HasCheckoutAgentSession(ws Workspace) bool {
	_, err := os.Stat(sessionStatePath(ws))
	return err == nil
}
func atomicSessionWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-session-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name) //nolint:errcheck
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func acquireAgentSessionLock(ws Workspace, tmux SessionTmuxRunner) (string, error) {
	if err := os.MkdirAll(filepath.Dir(sessionLockDir(ws)), 0700); err != nil {
		return "", err
	}
	if err := os.Mkdir(sessionLockDir(ws), 0700); err != nil {
		if !os.IsExist(err) {
			return "", err
		}
		data, readErr := os.ReadFile(sessionLockOwnerPath(ws))
		if readErr != nil {
			return "", fmt.Errorf("checkout session lock is initializing or invalid")
		}
		var owner sessionLockOwner
		if json.Unmarshal(data, &owner) != nil || owner.PID <= 0 || owner.Token == "" {
			return "", fmt.Errorf("invalid checkout session lock; use tws close to recover")
		}
		if s, e := LoadCheckoutAgentSession(ws); e == nil {
			if s.Mode == AgentSessionTmux && s.TmuxSession != "" && tmux.HasSession(s.TmuxSession) {
				return "", fmt.Errorf("checkout session %s/%s is active", s.Feature, s.Name)
			}
			if s.Mode == AgentSessionDirect && processAlive(s.PID) {
				return "", fmt.Errorf("checkout session %s/%s is active", s.Feature, s.Name)
			}
		}
		if processAlive(owner.PID) {
			return "", fmt.Errorf("checkout session lock is held by live process %d", owner.PID)
		}
		current, _ := os.ReadFile(sessionLockOwnerPath(ws))
		if string(current) != string(data) {
			return "", fmt.Errorf("checkout session lock changed during recovery")
		}
		if err := os.RemoveAll(sessionLockDir(ws)); err != nil {
			return "", err
		}
		if err := os.Mkdir(sessionLockDir(ws), 0700); err != nil {
			return "", err
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		_ = os.RemoveAll(sessionLockDir(ws))
		return "", err
	}
	token := hex.EncodeToString(buf)
	owner := sessionLockOwner{Token: token, PID: os.Getpid(), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	data, _ := json.Marshal(owner)
	if err := atomicSessionWrite(sessionLockOwnerPath(ws), data, 0600); err != nil {
		_ = os.RemoveAll(sessionLockDir(ws))
		return "", err
	}
	return token, nil
}
func releaseAgentSessionLock(ws Workspace, token string) error {
	data, err := os.ReadFile(sessionLockOwnerPath(ws))
	if err != nil {
		return err
	}
	var owner sessionLockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return err
	}
	if owner.Token != token {
		return fmt.Errorf("checkout session lock ownership changed")
	}
	return os.RemoveAll(sessionLockDir(ws))
}
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

func CheckoutSessionPreconditions(ws Workspace, feature string, entry StackEntry) error {
	if entry.Archived {
		return fmt.Errorf("branch %q is archived", entry.Name)
	}
	if entry.Repo != "" {
		return fmt.Errorf("checkout sessions do not support multi-repo entries")
	}
	if VerifyGitRef(ws.RepoRoot, entry.GitBranch()) != nil {
		return fmt.Errorf("git branch %q does not exist", entry.GitBranch())
	}
	if _, err := sessionCurrentBranch(ws.RepoRoot); err != nil {
		return fmt.Errorf("HEAD is detached")
	}
	if sessionGitOperation(ws.RepoRoot) {
		return fmt.Errorf("another Git operation is in progress")
	}
	active, err := anyCheckoutSyncActive(ws.MetadataRoot)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("checkout sync is active in this repository")
	}
	dirty, err := sessionDirty(ws.RepoRoot)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("working tree is dirty; commit or stash changes first")
	}
	return nil
}
func anyCheckoutSyncActive(root string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(root, "state"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, "-checkout-sync.yaml") || strings.HasSuffix(n, "-checkout-sync.lock") {
			return true, nil
		}
	}
	return false, nil
}

func CheckoutSessionAgentCommand(configured, repo string) []string {
	parts := strings.Fields(configured)
	if len(parts) == 0 {
		parts = []string{"claude"}
	}
	base := parts[0]
	if (base == "claude" || base == "claude-dev" || base == "cc") && checkoutClaudeSessionExists(repo) {
		parts = append(parts, "-c")
	}
	return parts
}
func checkoutClaudeSessionExists(repo string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	abs, _ := filepath.Abs(repo)
	encoded := strings.ReplaceAll(abs, string(filepath.Separator), "-")
	_, err = os.Stat(filepath.Join(home, ".claude", "projects", encoded))
	return err == nil
}

func PlanCheckoutSessionLinks(ws Workspace, feature, into string) ([]SessionContextLink, error) {
	featurePath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return nil, err
	}
	source := InjectPath(featurePath)
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return nil, nil
	}
	base := ws.RepoRoot
	if into != "" && into != "." {
		clean := filepath.Clean(into)
		if filepath.IsAbs(into) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("unsafe inject target %q", into)
		}
		base = filepath.Join(base, clean)
	}
	var links []SessionContextLink
	err = filepath.Walk(source, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(source, path)
		dest := filepath.Join(base, rel)
		if err := ensureSessionDestination(ws.RepoRoot, dest); err != nil {
			return err
		}
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("context injection collision: %s exists", dest)
		}
		if trackedPath(ws.RepoRoot, dest) {
			return fmt.Errorf("context injection collision: tracked path %s", dest)
		}
		links = append(links, SessionContextLink{Path: dest, Target: path})
		return nil
	})
	return links, err
}
func ensureSessionDestination(repo, dest string) error {
	absRepo, _ := filepath.Abs(repo)
	absDest, _ := filepath.Abs(dest)
	if absDest != absRepo && !strings.HasPrefix(absDest, absRepo+string(filepath.Separator)) {
		return fmt.Errorf("inject path escapes repository")
	}
	rel, _ := filepath.Rel(absRepo, filepath.Dir(absDest))
	cur := absRepo
	for _, p := range strings.Split(rel, string(filepath.Separator)) {
		if p == "" || p == "." {
			continue
		}
		cur = filepath.Join(cur, p)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("inject parent is a symlink: %s", cur)
		}
	}
	return nil
}
func trackedPath(repo, path string) bool {
	rel, _ := filepath.Rel(repo, path)
	cmd := exec.Command("git", "ls-files", "--error-unmatch", "--", rel)
	cmd.Dir = repo
	return cmd.Run() == nil
}

func ApplyCheckoutSessionLinks(repo string, links []SessionContextLink) ([]string, error) {
	var created []SessionContextLink
	var excludes []string
	for _, l := range links {
		if err := os.MkdirAll(filepath.Dir(l.Path), 0755); err != nil {
			rollbackSessionLinks(repo, created, excludes)
			return nil, err
		}
		if err := os.Symlink(l.Target, l.Path); err != nil {
			rollbackSessionLinks(repo, created, excludes)
			return nil, err
		}
		created = append(created, l)
		rel, _ := filepath.Rel(repo, l.Path)
		excludes = append(excludes, filepath.ToSlash(rel))
	}
	if err := addSessionExcludes(repo, excludes); err != nil {
		rollbackSessionLinks(repo, created, excludes)
		return nil, err
	}
	return excludes, nil
}
func CleanupCheckoutSessionLinks(repo string, links []SessionContextLink, excludes []string) error {
	var errs []error
	var removed []string
	for _, l := range links {
		target, err := os.Readlink(l.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if target != l.Target {
			continue
		}
		if err := os.Remove(l.Path); err != nil {
			errs = append(errs, err)
		} else {
			rel, _ := filepath.Rel(repo, l.Path)
			removed = append(removed, filepath.ToSlash(rel))
		}
	}
	if len(removed) > 0 {
		if err := removeSessionExcludes(repo, removed); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func addSessionExcludes(repo string, add []string) error {
	path := filepath.Join(repo, ".git", "info", "exclude")
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	seen := map[string]bool{}
	for _, l := range lines {
		seen[l] = true
	}
	for _, l := range add {
		if !seen[l] {
			lines = append(lines, l)
			seen[l] = true
		}
	}
	return atomicSessionWrite(path, []byte(strings.Join(lines, "\n")), 0644)
}
func removeSessionExcludes(repo string, remove []string) error {
	path := filepath.Join(repo, ".git", "info", "exclude")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, l := range remove {
		set[l] = true
	}
	var kept []string
	for _, l := range strings.Split(string(data), "\n") {
		if !set[l] {
			kept = append(kept, l)
		}
	}
	return atomicSessionWrite(path, []byte(strings.Join(kept, "\n")), 0644)
}
func rollbackSessionLinks(repo string, links []SessionContextLink, excludes []string) {
	for _, l := range links {
		_ = os.Remove(l.Path)
	}
	_ = removeSessionExcludes(repo, excludes)
}

func sessionCurrentBranch(repo string) (string, error) {
	return sessionGitOutput(repo, "symbolic-ref", "--short", "HEAD")
}
func sessionHEAD(repo string) (string, error) { return sessionGitOutput(repo, "rev-parse", "HEAD") }
func sessionGitOutput(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
func sessionSwitch(repo, branch string) error {
	cmd := exec.Command("git", "switch", branch)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git switch %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
	}
	return nil
}
func sessionDirty(repo string) (bool, error) {
	out, err := sessionGitOutput(repo, "status", "--porcelain")
	return out != "", err
}
func sessionGitOperation(repo string) bool {
	for _, p := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		path, err := sessionGitOutput(repo, "rev-parse", "--git-path", p)
		if err == nil {
			if _, e := os.Stat(path); e == nil {
				return true
			}
		}
	}
	return false
}

func OpenCheckoutDirect(ws Workspace, feature string, entry StackEntry, command []string, agent SessionAgentRunner, shell SessionShellRunner, into string) error {
	if err := CheckoutSessionPreconditions(ws, feature, entry); err != nil {
		return err
	}
	tmux := RealSessionTmuxRunner{}
	token, err := acquireAgentSessionLock(ws, tmux)
	if err != nil {
		return err
	}
	orig, err := sessionCurrentBranch(ws.RepoRoot)
	if err != nil {
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	head, err := sessionHEAD(ws.RepoRoot)
	if err != nil {
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	links, err := PlanCheckoutSessionLinks(ws, feature, into)
	if err != nil {
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	if err := sessionSwitch(ws.RepoRoot, entry.GitBranch()); err != nil {
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	excludes, err := ApplyCheckoutSessionLinks(ws.RepoRoot, links)
	if err != nil {
		_ = sessionSwitch(ws.RepoRoot, orig)
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	state := &CheckoutAgentSession{SchemaVersion: checkoutSessionSchema, WorkspaceID: ws.StableID, Feature: feature, Name: entry.Name, GitBranch: entry.GitBranch(), OriginalBranch: orig, OriginalHEAD: head, Mode: AgentSessionDirect, PID: os.Getpid(), Stage: "agent", StartedAt: time.Now().UTC().Format(time.RFC3339), RepoDir: ws.RepoRoot, Links: links, ExcludeEntries: excludes, LockToken: token}
	if err := SaveCheckoutAgentSession(ws, state); err != nil {
		_ = CleanupCheckoutSessionLinks(ws.RepoRoot, links, excludes)
		_ = sessionSwitch(ws.RepoRoot, orig)
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	if agent == nil {
		agent = RealSessionAgentRunner{}
	}
	if shell == nil {
		shell = RealSessionShellRunner{}
	}
	agentErr := agent.Run(ws.RepoRoot, command)
	var shellErr error
	if agentErr == nil {
		state.Stage = "shell"
		if err := SaveCheckoutAgentSession(ws, state); err != nil {
			return errors.Join(err, finishCheckoutSession(ws, state))
		}
		shellErr = shell.Run(ws.RepoRoot)
	}
	return errors.Join(wrapSessionErr("agent", agentErr), wrapSessionErr("shell", shellErr), finishCheckoutSession(ws, state))
}

func OpenCheckoutTmux(ws Workspace, feature string, entry StackEntry, command []string, tmux SessionTmuxRunner, into string) error {
	if err := CheckoutSessionPreconditions(ws, feature, entry); err != nil {
		return err
	}
	if tmux == nil {
		tmux = RealSessionTmuxRunner{}
	}
	token, err := acquireAgentSessionLock(ws, tmux)
	if err != nil {
		return err
	}
	orig, err := sessionCurrentBranch(ws.RepoRoot)
	if err != nil {
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	head, err := sessionHEAD(ws.RepoRoot)
	if err != nil {
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	links, err := PlanCheckoutSessionLinks(ws, feature, into)
	if err != nil {
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	if err := sessionSwitch(ws.RepoRoot, entry.GitBranch()); err != nil {
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	excludes, err := ApplyCheckoutSessionLinks(ws.RepoRoot, links)
	if err != nil {
		_ = sessionSwitch(ws.RepoRoot, orig)
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	name := CheckoutAgentSessionName(ws.StableID, feature, entry.Name)
	if err := tmux.NewSession(name, ws.RepoRoot, command); err != nil {
		_ = CleanupCheckoutSessionLinks(ws.RepoRoot, links, excludes)
		_ = sessionSwitch(ws.RepoRoot, orig)
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	if !tmux.HasSession(name) {
		_ = CleanupCheckoutSessionLinks(ws.RepoRoot, links, excludes)
		_ = sessionSwitch(ws.RepoRoot, orig)
		_ = releaseAgentSessionLock(ws, token)
		return fmt.Errorf("tmux session %q did not start", name)
	}
	state := &CheckoutAgentSession{SchemaVersion: checkoutSessionSchema, WorkspaceID: ws.StableID, Feature: feature, Name: entry.Name, GitBranch: entry.GitBranch(), OriginalBranch: orig, OriginalHEAD: head, Mode: AgentSessionTmux, TmuxSession: name, Stage: "tmux", StartedAt: time.Now().UTC().Format(time.RFC3339), RepoDir: ws.RepoRoot, Links: links, ExcludeEntries: excludes, LockToken: token}
	if err := SaveCheckoutAgentSession(ws, state); err != nil {
		_ = tmux.KillSession(name)
		_ = CleanupCheckoutSessionLinks(ws.RepoRoot, links, excludes)
		_ = sessionSwitch(ws.RepoRoot, orig)
		_ = releaseAgentSessionLock(ws, token)
		return err
	}
	if err := tmux.AttachSession(name); err != nil {
		if tmux.HasSession(name) {
			return fmt.Errorf("tmux attach failed but session %q remains active; use tws close %s %s: %w", name, feature, entry.Name, err)
		}
		return finishCheckoutSession(ws, state)
	}
	return nil
}

func CloseCheckoutSession(ws Workspace, feature, name string, tmux SessionTmuxRunner) error {
	state, err := LoadCheckoutAgentSession(ws)
	if err != nil {
		return err
	}
	if state.Feature != feature || state.Name != name {
		return fmt.Errorf("active checkout session is %s/%s", state.Feature, state.Name)
	}
	if tmux == nil {
		tmux = RealSessionTmuxRunner{}
	}
	if state.Mode == AgentSessionTmux && state.TmuxSession != "" && tmux.HasSession(state.TmuxSession) {
		if err := tmux.KillSession(state.TmuxSession); err != nil {
			return err
		}
	}
	if state.Mode == AgentSessionDirect && state.PID > 0 && state.PID != os.Getpid() && processAlive(state.PID) {
		return fmt.Errorf("direct checkout session is still active")
	}
	return finishCheckoutSession(ws, state)
}
func finishCheckoutSession(ws Workspace, state *CheckoutAgentSession) error {
	if err := restoreCheckoutSession(ws, state); err != nil {
		return err
	}
	if err := os.Remove(sessionStatePath(ws)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return releaseAgentSessionLock(ws, state.LockToken)
}
func restoreCheckoutSession(ws Workspace, state *CheckoutAgentSession) error {
	dirty, err := sessionDirty(ws.RepoRoot)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("cannot restore original branch: working tree is dirty; session state retained")
	}
	current, err := sessionCurrentBranch(ws.RepoRoot)
	if err != nil {
		return err
	}
	if current != state.GitBranch {
		return fmt.Errorf("cannot restore: current branch %q is not session branch %q", current, state.GitBranch)
	}
	if err := CleanupCheckoutSessionLinks(ws.RepoRoot, state.Links, state.ExcludeEntries); err != nil {
		return err
	}
	if err := sessionSwitch(ws.RepoRoot, state.OriginalBranch); err != nil {
		return err
	}
	if state.OriginalBranch != state.GitBranch {
		head, err := sessionHEAD(ws.RepoRoot)
		if err != nil {
			return err
		}
		if head != state.OriginalHEAD {
			return fmt.Errorf("original branch HEAD changed during session; manual recovery required")
		}
	}
	return nil
}
func wrapSessionErr(label string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", label, err)
}
