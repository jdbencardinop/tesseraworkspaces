package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func CheckoutCommandEntry(ws Workspace, featurePath, name string) (StackEntry, error) {
	info, err := os.Lstat(StackPath(featurePath))
	if err != nil || !info.Mode().IsRegular() {
		return StackEntry{}, errors.New("checkout stack metadata is missing or is not a regular file")
	}
	stack, err := LoadStack(featurePath)
	if err != nil {
		return StackEntry{}, err
	}
	var found StackEntry
	for _, entry := range stack.Branches {
		if entry.Name == name {
			if found.Name != "" {
				return StackEntry{}, fmt.Errorf("duplicate logical branch %q", name)
			}
			found = entry
		}
	}
	if found.Name == "" {
		return found, fmt.Errorf("logical branch %q is not in the selected feature", name)
	}
	if found.Archived || found.Repo != "" {
		return found, errors.New("checkout open requires an active single-repository logical branch")
	}
	if _, err := sessionGitOutput(ws.RepoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+found.GitBranch()); err != nil {
		return found, fmt.Errorf("local checkout branch %q is missing", found.GitBranch())
	}
	return found, nil
}

func CheckoutCommandCurrentEntry(ws Workspace, entry StackEntry) error {
	current, err := sessionGitOutput(ws.RepoRoot, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || current != "refs/heads/"+entry.GitBranch() {
		return fmt.Errorf("path query requires logical entry %q to be the current attached checkout; it does not switch branches", entry.Name)
	}
	return nil
}

type checkoutCommandOwnership struct {
	state     *CheckoutAgentSession
	lock      checkoutSessionIntentSnapshot
	stateInfo os.FileInfo
	stateData []byte
}

func verifyCheckoutCommandIntent(ws Workspace, expected checkoutSessionIntentSnapshot, token string) error {
	current, present, err := loadCheckoutSessionIntentSnapshot(ws)
	if err != nil || !present {
		return errors.Join(errors.New("checkout command launch intent is missing or unverifiable"), err)
	}
	owner, err := current.decodeOwner()
	if err != nil || owner.Token != token || owner.PID != os.Getpid() ||
		!os.SameFile(expected.dirInfo, current.dirInfo) ||
		!os.SameFile(expected.ownerInfo, current.ownerInfo) ||
		!bytes.Equal(expected.ownerBytes, current.ownerBytes) {
		return errors.New("checkout command launch intent ownership changed; preserve the remaining evidence")
	}
	return nil
}

// CheckoutCommandIntentCaptureHook is a test-only seam after acquisition and
// before the launcher can establish its initial ownership snapshot.
var CheckoutCommandIntentCaptureHook func() error

func captureCheckoutCommandIntent(ws Workspace, token string) (checkoutSessionIntentSnapshot, error) {
	if CheckoutCommandIntentCaptureHook != nil {
		if err := CheckoutCommandIntentCaptureHook(); err != nil {
			return checkoutSessionIntentSnapshot{}, fmt.Errorf("checkout command intent capture failed; preserve the remaining evidence: %w", err)
		}
	}
	snapshot, present, err := loadCheckoutSessionIntentSnapshot(ws)
	if err != nil || !present {
		return checkoutSessionIntentSnapshot{}, errors.Join(
			errors.New("checkout command intent could not be captured; preserve the remaining evidence"), err)
	}
	if err := verifyCheckoutCommandIntent(ws, snapshot, token); err != nil {
		return checkoutSessionIntentSnapshot{}, err
	}
	return snapshot, nil
}

// WithCheckoutCommandLaunchIntent protects a branchless command without
// inventing a branch session. Unlike legacy open, it verifies its intent
// before effects and makes the directory publications durable.
func WithCheckoutCommandLaunchIntent(ws Workspace, finalCheck, launch func() error) (retErr error) {
	if err := commandSessionStateDirectories(ws, false); err != nil {
		return err
	}
	token, err := acquireAgentSessionLock(ws, RealSessionTmuxRunner{})
	if err != nil {
		return err
	}
	snapshot, err := captureCheckoutCommandIntent(ws, token)
	if err != nil {
		return err
	}
	defer func() {
		if err := verifyCheckoutCommandIntent(ws, snapshot, token); err != nil {
			retErr = errors.Join(retErr, err)
			return
		}
		retErr = errors.Join(retErr, removeCheckoutSessionIntentSnapshot(ws, snapshot))
	}()
	if CheckoutSessionLaunchIntentHook != nil {
		if err := CheckoutSessionLaunchIntentHook(); err != nil {
			return err
		}
	}
	if finalCheck != nil {
		if err := finalCheck(); err != nil {
			return err
		}
	}
	if err := verifyCheckoutCommandIntent(ws, snapshot, token); err != nil {
		return err
	}
	for _, dir := range []string{sessionLockDir(ws), filepath.Join(ws.MetadataRoot, "state"), ws.MetadataRoot} {
		if err := syncDir(dir); err != nil {
			return err
		}
	}
	if launch == nil {
		return nil
	}
	return launch()
}

func loadCheckoutCommandOwnership(ws Workspace, state *CheckoutAgentSession) (*checkoutCommandOwnership, error) {
	if state.SchemaVersion != checkoutSessionSchema || state.Stage != DirectStageCommand ||
		state.LockToken == "" || state.WorkspaceID != ws.StableID || state.RepoDir != ws.RepoRoot ||
		(state.Mode != AgentSessionDirect && state.Mode != AgentSessionTmux) ||
		state.Feature == "" || state.Name == "" || state.GitBranch == "" || state.OriginalBranch == "" ||
		state.OriginalHEAD == "" || state.PID <= 0 {
		return nil, errors.New("checkout command state identity is invalid; preserve it for manual reconciliation")
	}
	if err := validateFeatureName(state.Feature); err != nil {
		return nil, err
	}
	if state.Mode == AgentSessionTmux && state.TmuxSession != CheckoutAgentSessionName(ws.StableID, state.Feature, state.Name) ||
		state.Mode == AgentSessionDirect && state.TmuxSession != "" {
		return nil, errors.New("checkout command transport identity is invalid; state retained")
	}
	lock, present, err := loadCheckoutSessionIntentSnapshot(ws)
	if err != nil || !present {
		return nil, errors.Join(errors.New("checkout command lock is missing or unverifiable; state retained"), err)
	}
	owner, err := lock.decodeOwner()
	if err != nil || owner.Token != state.LockToken || owner.PID != state.PID {
		return nil, errors.New("checkout command lock ownership changed; state retained")
	}
	if err := commandSessionStateDirectories(ws, false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(sessionStatePath(ws))
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(errors.New("checkout command record is missing or not a regular file; state retained"), err)
	}
	data, err := os.ReadFile(sessionStatePath(ws))
	if err != nil {
		return nil, err
	}
	expected, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, expected) {
		return nil, errors.New("checkout command record changed; state retained")
	}
	return &checkoutCommandOwnership{state: state, lock: lock, stateInfo: info, stateData: data}, nil
}

func (owned *checkoutCommandOwnership) verify(ws Workspace) error {
	current, err := loadCheckoutCommandOwnership(ws, owned.state)
	if err != nil {
		return err
	}
	if !os.SameFile(owned.lock.dirInfo, current.lock.dirInfo) ||
		!os.SameFile(owned.lock.ownerInfo, current.lock.ownerInfo) ||
		!bytes.Equal(owned.lock.ownerBytes, current.lock.ownerBytes) ||
		!os.SameFile(owned.stateInfo, current.stateInfo) ||
		!bytes.Equal(owned.stateData, current.stateData) {
		return errors.New("checkout command ownership changed; preserve the record and lock")
	}
	return nil
}

func commandSessionStateDirectories(ws Workspace, create bool) error {
	for _, path := range []string{ws.MetadataRoot, filepath.Join(ws.MetadataRoot, "state"), sessionStateDir(ws)} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if create {
				if err := os.Mkdir(path, 0700); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("checkout command state directory is not a real directory")
		}
	}
	return nil
}

// CheckoutCommandStateWriteHook is a test-only persistence failure seam.
var CheckoutCommandStateWriteHook func() error

func createCheckoutCommandState(ws Workspace, state *CheckoutAgentSession) error {
	if CheckoutCommandStateWriteHook != nil {
		if err := CheckoutCommandStateWriteHook(); err != nil {
			return err
		}
	}
	if err := commandSessionStateDirectories(ws, true); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	// Exclusive publication cannot replace a foreign record appearing after
	// admission. A failed write keeps evidence and never permits child effects.
	file, err := os.OpenFile(sessionStatePath(ws), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	for _, dir := range []string{sessionStateDir(ws), sessionLockDir(ws), filepath.Join(ws.MetadataRoot, "state"), ws.MetadataRoot} {
		if err := syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func checkoutCommandGitOperation(repo string) (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		path, err := sessionGitOutput(repo, "rev-parse", "--git-path", name)
		if err != nil {
			return false, err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(repo, path)
		}
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func checkoutCommandPreconditions(ws Workspace, feature string, entry StackEntry) error {
	if active, err := checkoutCommandGitOperation(ws.RepoRoot); err != nil {
		return fmt.Errorf("Git operation state is unverifiable: %w", err)
	} else if active {
		return errors.New("another Git operation is in progress")
	}
	return CheckoutSessionPreconditions(ws, feature, entry)
}

// OpenCheckoutCommand uses the existing one-owner session record and lock.
// A non-nil tmux selects tmux; a nil runner selects the ordinary direct runner.
func OpenCheckoutCommand(ws Workspace, feature string, entry StackEntry, command []string, runner SessionAgentRunner, tmux SessionTmuxRunner, into string, finalGuard func() error) (retErr error) {
	if len(command) == 0 || !filepath.IsAbs(command[0]) {
		return errors.New("command executable must be bound before checkout activation")
	}
	for _, arg := range command {
		if strings.ContainsRune(arg, '\x00') {
			return errors.New("command argv cannot contain NUL")
		}
	}
	if err := commandSessionStateDirectories(ws, false); err != nil {
		return err
	}
	if err := checkoutCommandPreconditions(ws, feature, entry); err != nil {
		return err
	}
	name := CheckoutAgentSessionName(ws.StableID, feature, entry.Name)
	if tmux != nil {
		if err := RefuseCommandTmuxCollision(tmux, name); err != nil {
			return err
		}
	}
	token, err := acquireAgentSessionLock(ws, tmux)
	if err != nil {
		return err
	}
	initialLock, err := captureCheckoutCommandIntent(ws, token)
	if err != nil {
		return err
	}
	release := true
	defer func() {
		if release {
			if err := verifyCheckoutCommandIntent(ws, initialLock, token); err != nil {
				retErr = errors.Join(retErr, err)
				return
			}
			retErr = errors.Join(retErr, removeCheckoutSessionIntentSnapshot(ws, initialLock))
		}
	}()
	if CheckoutSessionLaunchIntentHook != nil {
		if err := CheckoutSessionLaunchIntentHook(); err != nil {
			return err
		}
	}
	if finalGuard != nil {
		if err := finalGuard(); err != nil {
			return err
		}
	}
	if err := verifyCheckoutCommandIntent(ws, initialLock, token); err != nil {
		return err
	}
	if err := checkoutCommandPreconditions(ws, feature, entry); err != nil {
		return err
	}
	if tmux != nil {
		if err := RefuseCommandTmuxCollision(tmux, name); err != nil {
			return err
		}
	}
	if err := verifyCheckoutCommandIntent(ws, initialLock, token); err != nil {
		return err
	}
	original, err := sessionCurrentBranch(ws.RepoRoot)
	if err != nil {
		return err
	}
	head, err := sessionHEAD(ws.RepoRoot)
	if err != nil {
		return err
	}
	links, err := PlanCheckoutSessionLinks(ws, feature, into)
	if err != nil {
		return err
	}
	excludes, err := newCommandExcludes(ws.RepoRoot, links)
	if err != nil {
		return err
	}
	state := &CheckoutAgentSession{
		SchemaVersion: checkoutSessionSchema, WorkspaceID: ws.StableID, Feature: feature,
		Name: entry.Name, GitBranch: entry.GitBranch(), OriginalBranch: original, OriginalHEAD: head,
		Mode: AgentSessionDirect, PID: os.Getpid(), Stage: DirectStageCommand,
		StartedAt: time.Now().UTC().Format(time.RFC3339), RepoDir: ws.RepoRoot,
		Links: links, ExcludeEntries: excludes, LockToken: token,
	}
	if tmux != nil {
		state.Mode, state.TmuxSession = AgentSessionTmux, name
	}
	if err := createCheckoutCommandState(ws, state); err != nil {
		if _, statErr := os.Lstat(sessionStatePath(ws)); !errors.Is(statErr, os.ErrNotExist) {
			release = false
			return fmt.Errorf("command state publication failed; preserve the record and lock for inspection (no command ran): %w", err)
		}
		return err
	}
	release = false
	owned, err := loadCheckoutCommandOwnership(ws, state)
	if err != nil {
		return err
	}
	if !os.SameFile(initialLock.dirInfo, owned.lock.dirInfo) || !os.SameFile(initialLock.ownerInfo, owned.lock.ownerInfo) ||
		!bytes.Equal(initialLock.ownerBytes, owned.lock.ownerBytes) {
		return errors.New("checkout command lock changed during state publication; state retained")
	}
	failBeforeLaunch := func(cause error) error {
		return errors.Join(cause, finishCheckoutCommandSession(ws, owned, true))
	}
	if err := owned.verify(ws); err != nil {
		return err
	}
	if finalGuard != nil {
		if err := finalGuard(); err != nil {
			return failBeforeLaunch(err)
		}
	}
	if err := CheckoutSessionMutationPreconditions(ws); err != nil {
		return failBeforeLaunch(err)
	}
	if err := sessionSwitch(ws.RepoRoot, entry.GitBranch()); err != nil {
		return failBeforeLaunch(err)
	}
	if _, err := ApplyCheckoutSessionLinks(ws.RepoRoot, links); err != nil {
		return failBeforeLaunch(err)
	}
	if err := owned.verify(ws); err != nil {
		return err
	}
	if finalGuard != nil {
		if err := finalGuard(); err != nil {
			return failBeforeLaunch(err)
		}
	}
	if err := CheckoutSessionMutationPreconditions(ws); err != nil {
		return failBeforeLaunch(err)
	}
	if tmux != nil {
		return runCheckoutTmuxCommand(ws, owned, command, tmux)
	}
	if runner == nil {
		runner = RealSessionAgentRunner{}
	}
	runErr := runner.Run(ws.RepoRoot, command)
	return errors.Join(wrapSessionErr("command", runErr), finishCheckoutCommandSession(ws, owned, false))
}

func runCheckoutTmuxCommand(ws Workspace, owned *checkoutCommandOwnership, command []string, runner SessionTmuxRunner) error {
	tmux, err := commandTmuxRunner(runner)
	if err != nil {
		return errors.Join(err, finishCheckoutCommandSession(ws, owned, true))
	}
	state := owned.state
	if err := RefuseCommandTmuxCollision(runner, state.TmuxSession); err != nil {
		// A session appeared after activation. It may already be writing this
		// checkout; neither restoration nor killing is safe.
		return fmt.Errorf("tmux collision after admission; checkout command state retained: %w", err)
	}
	startErr := tmux.NewCommandSession(state.TmuxSession, ws.RepoRoot, command, state.LockToken)
	present, probeErr := tmux.OwnedCommandSession(state.TmuxSession, state.LockToken)
	if probeErr != nil {
		return errors.Join(startErr, fmt.Errorf("tmux startup ownership is uncertain; checkout state retained: %w", probeErr))
	}
	if !present {
		return errors.Join(startErr, finishCheckoutCommandSession(ws, owned, false))
	}
	if startErr != nil {
		return fmt.Errorf("tmux startup failed but the owned command is live; checkout state retained; inspect or use tws close: %w", startErr)
	}
	if err := owned.verify(ws); err != nil {
		return err
	}
	attachErr := tmux.AttachCommandSession(state.TmuxSession, state.LockToken)
	present, probeErr = tmux.OwnedCommandSession(state.TmuxSession, state.LockToken)
	if probeErr != nil {
		return errors.Join(attachErr, probeErr)
	}
	if present {
		if attachErr != nil {
			return fmt.Errorf("tmux attach failed but the command remains active; use tws close %s %s: %w", state.Feature, state.Name, attachErr)
		}
		return owned.verify(ws)
	}
	return errors.Join(wrapSessionErr("tmux attach (child exit status unavailable)", attachErr), finishCheckoutCommandSession(ws, owned, false))
}

func newCommandExcludes(repo string, links []SessionContextLink) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	existing := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		existing[line] = true
	}
	var owned []string
	for _, link := range links {
		rel, err := filepath.Rel(repo, link.Path)
		if err != nil {
			return nil, err
		}
		name := filepath.ToSlash(rel)
		if !existing[name] {
			owned = append(owned, name)
		}
	}
	return owned, nil
}

func finishCheckoutCommandSession(ws Workspace, owned *checkoutCommandOwnership, unstarted bool) error {
	retained := func(err error) error {
		return fmt.Errorf("cannot restore checkout command; session state retained: %w; commit/stash work or reconcile the recorded branch/ownership, then use tws close", err)
	}
	if err := owned.verify(ws); err != nil {
		return retained(err)
	}
	state := owned.state
	current, err := sessionCurrentBranch(ws.RepoRoot)
	if err != nil {
		return retained(errors.New("HEAD is detached"))
	}
	if current != state.GitBranch && (!unstarted || current != state.OriginalBranch) {
		return retained(errors.New("the attached branch changed"))
	}
	if err := CheckoutSessionMutationPreconditions(ws); err != nil {
		return retained(err)
	}
	if active, err := checkoutCommandGitOperation(ws.RepoRoot); err != nil {
		return retained(err)
	} else if active {
		return retained(errors.New("another Git operation is in progress"))
	}
	dirty, err := sessionDirty(ws.RepoRoot)
	if err != nil {
		return retained(err)
	}
	if dirty {
		return retained(errors.New("working tree is dirty"))
	}
	if state.OriginalBranch != state.GitBranch {
		head, err := sessionGitOutput(ws.RepoRoot, "rev-parse", "--verify", "refs/heads/"+state.OriginalBranch)
		if err != nil || head != state.OriginalHEAD {
			return retained(errors.New("original branch HEAD changed or disappeared"))
		}
	}
	for _, link := range state.Links {
		if err := ensureSessionDestination(ws.RepoRoot, link.Path); err != nil {
			return retained(err)
		}
		if trackedPath(ws.RepoRoot, link.Path) {
			return retained(fmt.Errorf("context link %s became tracked; preserve it", link.Path))
		}
		target, err := os.Readlink(link.Path)
		if errors.Is(err, os.ErrNotExist) && unstarted {
			continue
		}
		if err != nil || target != link.Target {
			return retained(fmt.Errorf("context link %s changed or disappeared", link.Path))
		}
	}
	if err := owned.verify(ws); err != nil {
		return retained(err)
	}
	for _, link := range state.Links {
		if err := os.Remove(link.Path); err != nil && (!unstarted || !errors.Is(err, os.ErrNotExist)) {
			return retained(err)
		}
	}
	if len(state.ExcludeEntries) > 0 {
		if err := removeSessionExcludes(ws.RepoRoot, state.ExcludeEntries); err != nil {
			return retained(err)
		}
	}
	if current != state.OriginalBranch {
		if err := sessionSwitch(ws.RepoRoot, state.OriginalBranch); err != nil {
			return retained(err)
		}
		if head, err := sessionHEAD(ws.RepoRoot); err != nil || head != state.OriginalHEAD {
			return retained(errors.New("original branch HEAD changed during restoration"))
		}
		if current, err := sessionCurrentBranch(ws.RepoRoot); err != nil || current != state.OriginalBranch {
			return retained(errors.New("attached branch changed during restoration"))
		}
		if dirty, err := sessionDirty(ws.RepoRoot); err != nil || dirty {
			return retained(errors.Join(errors.New("checkout became dirty or unverifiable during restoration"), err))
		}
	}
	if err := owned.verify(ws); err != nil {
		return retained(err)
	}
	if err := os.Remove(sessionStatePath(ws)); err != nil {
		return retained(err)
	}
	if err := syncDir(sessionStateDir(ws)); err != nil {
		return fmt.Errorf("checkout restored and command record removed, but cleanup durability failed; preserve the remaining lock: %w", err)
	}
	if err := releaseAgentSessionLock(ws, state.LockToken); err != nil {
		return fmt.Errorf("checkout restored but command lock cleanup failed: %w", err)
	}
	return syncDir(filepath.Join(ws.MetadataRoot, "state"))
}

func closeCheckoutCommandSession(ws Workspace, state *CheckoutAgentSession, runner SessionTmuxRunner) error {
	owned, err := loadCheckoutCommandOwnership(ws, state)
	if err != nil {
		return err
	}
	if state.Mode == AgentSessionDirect {
		if state.PID > 0 && processAlive(state.PID) {
			return errors.New("direct checkout command is still active; exit it before closing")
		}
	} else {
		tmux, err := commandTmuxRunner(runner)
		if err != nil {
			return err
		}
		present, err := tmux.OwnedCommandSession(state.TmuxSession, state.LockToken)
		if err != nil {
			return err
		}
		if present {
			if err := owned.verify(ws); err != nil {
				return err
			}
			if err := tmux.KillCommandSession(state.TmuxSession, state.LockToken); err != nil {
				return err
			}
			if present, err := tmux.OwnedCommandSession(state.TmuxSession, state.LockToken); err != nil || present {
				return errors.Join(errors.New("tmux command termination could not be confirmed; state retained"), err)
			}
		}
	}
	return finishCheckoutCommandSession(ws, owned, false)
}
