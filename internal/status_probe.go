package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	statusSubprocessTimeout = 5 * time.Second
	statusInvocationTimeout = 30 * time.Second
	statusProcessWaitDelay  = 250 * time.Millisecond
)

// StatusProbeBudget owns the subprocess lifetime of one status invocation.
// Filesystem reads remain synchronous and are intentionally outside this
// cancellation contract.
type StatusProbeBudget struct {
	ctx      context.Context
	cancel   context.CancelFunc
	deadline time.Time
	perProbe time.Duration

	mu       sync.Mutex
	launched int
}

func NewStatusProbeBudget(parent context.Context) *StatusProbeBudget {
	return newStatusProbeBudget(parent, statusSubprocessTimeout, statusInvocationTimeout)
}

func newStatusProbeBudget(parent context.Context, perProbe, total time.Duration) *StatusProbeBudget {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, total)
	deadline, _ := ctx.Deadline()
	return &StatusProbeBudget{
		ctx:      ctx,
		cancel:   cancel,
		deadline: deadline,
		perProbe: perProbe,
	}
}

func (b *StatusProbeBudget) Close() {
	if b != nil && b.cancel != nil {
		b.cancel()
	}
}

func (b *StatusProbeBudget) Stopped() bool {
	if b == nil {
		return false
	}
	return b.ctx.Err() != nil || time.Until(b.deadline) <= 0
}

func (b *StatusProbeBudget) launchedCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.launched
}

type statusProbeError struct {
	command     string
	err         error
	timedOut    bool
	canceled    bool
	pipeExpired bool
}

func (e *statusProbeError) Error() string {
	switch {
	case e.timedOut:
		return e.command + " timed out"
	case e.canceled:
		return e.command + " canceled"
	case e.pipeExpired:
		return fmt.Sprintf("%s output pipe cleanup timed out: %v", e.command, e.err)
	default:
		return fmt.Sprintf("%s failed: %v", e.command, e.err)
	}
}

func (e *statusProbeError) Unwrap() error { return e.err }

func completedStatusExit(err error) *exec.ExitError {
	var probeErr *statusProbeError
	if !errors.As(err, &probeErr) || probeErr.timedOut || probeErr.canceled || probeErr.pipeExpired {
		return nil
	}
	// A joined drain or context failure is not a completed observation,
	// even when it also contains a normal process exit code.
	exitErr, _ := probeErr.err.(*exec.ExitError)
	return exitErr
}

func statusProbeProblem(err error) string {
	var probeErr *statusProbeError
	if errors.As(err, &probeErr) {
		switch {
		case probeErr.timedOut:
			return "timed out"
		case probeErr.canceled:
			return "was canceled"
		}
	}
	return err.Error()
}

func (b *StatusProbeBudget) run(name string, args ...string) ([]byte, []byte, error) {
	if b == nil {
		owned := NewStatusProbeBudget(context.Background())
		defer owned.Close()
		return owned.run(name, args...)
	}
	if err := b.ctx.Err(); err != nil {
		return nil, nil, &statusProbeError{command: name, err: err, timedOut: errors.Is(err, context.DeadlineExceeded), canceled: !errors.Is(err, context.DeadlineExceeded)}
	}

	remaining := time.Until(b.deadline)
	if remaining <= 0 {
		return nil, nil, &statusProbeError{command: name, err: context.DeadlineExceeded, timedOut: true}
	}
	limit := b.perProbe
	if remaining < limit {
		limit = remaining
	}
	probeCtx, cancel := context.WithTimeout(b.ctx, limit)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, name, args...)
	configureStatusCommand(cmd)
	cmd.WaitDelay = statusProcessWaitDelay
	if name == "git" {
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, nil, &statusProbeError{command: name, err: err}
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, nil, &statusProbeError{command: name, err: err}
	}
	closePipes := func() {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	b.mu.Lock()
	if b.ctx.Err() != nil || time.Until(b.deadline) <= 0 {
		b.mu.Unlock()
		closePipes()
		err := b.ctx.Err()
		if err == nil {
			err = context.DeadlineExceeded
		}
		return nil, nil, &statusProbeError{command: name, err: err, timedOut: errors.Is(err, context.DeadlineExceeded), canceled: !errors.Is(err, context.DeadlineExceeded)}
	}
	b.mu.Unlock()

	if err := cmd.Start(); err != nil {
		closePipes()
		return nil, nil, &statusProbeError{command: name, err: err}
	}
	b.mu.Lock()
	b.launched++
	b.mu.Unlock()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	type pipeResult struct{ err error }
	drained := make(chan pipeResult, 2)
	go func() {
		_, copyErr := io.Copy(&stdout, stdoutReader)
		drained <- pipeResult{err: copyErr}
	}()
	go func() {
		_, copyErr := io.Copy(&stderr, stderrReader)
		drained <- pipeResult{err: copyErr}
	}()

	waitErr := cmd.Wait()
	timer := time.NewTimer(statusProcessWaitDelay)
	drainedCount := 0
	var drainErr error
	pipeExpired := false
	for drainedCount < 2 {
		select {
		case result := <-drained:
			drainedCount++
			if result.err != nil {
				drainErr = errors.Join(drainErr, result.err)
			}
		case <-timer.C:
			pipeExpired = true
			_ = terminateStatusCommand(cmd)
			_ = stdoutReader.Close()
			_ = stderrReader.Close()
			for drainedCount < 2 {
				result := <-drained
				drainedCount++
				if result.err != nil && !errors.Is(result.err, os.ErrClosed) {
					drainErr = errors.Join(drainErr, result.err)
				}
			}
		}
	}
	if !timer.Stop() && !pipeExpired {
		<-timer.C
	}
	_ = stdoutReader.Close()
	_ = stderrReader.Close()

	if pipeExpired {
		drainErr = errors.Join(drainErr, exec.ErrWaitDelay)
	}
	if drainErr != nil {
		waitErr = errors.Join(waitErr, drainErr)
	}
	if probeCtx.Err() != nil {
		return stdout.Bytes(), stderr.Bytes(), &statusProbeError{
			command:     name,
			err:         errors.Join(probeCtx.Err(), waitErr),
			timedOut:    errors.Is(probeCtx.Err(), context.DeadlineExceeded),
			canceled:    errors.Is(probeCtx.Err(), context.Canceled),
			pipeExpired: pipeExpired,
		}
	}
	if waitErr != nil {
		return stdout.Bytes(), stderr.Bytes(), &statusProbeError{command: name, err: waitErr, pipeExpired: pipeExpired}
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

func statusMainRepoRootIn(path string, budget *StatusProbeBudget) (string, error) {
	out, _, err := budget.run("git", "-C", path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}
	return filepath.Dir(filepath.Clean(gitDir)), nil
}

func statusCurrentBranch(path string, budget *StatusProbeBudget) (string, *bool, error) {
	out, _, err := budget.run("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", nil, err
	}
	branch := strings.TrimSpace(string(out))
	if branch != "HEAD" {
		return branch, boolPtr(false), nil
	}
	detached := boolPtr(true)
	short, _, shortErr := budget.run("git", "-C", path, "rev-parse", "--short", "HEAD")
	if shortErr != nil {
		return "HEAD", detached, shortErr
	}
	return strings.TrimSpace(string(short)), detached, nil
}

func statusDirty(path string, budget *StatusProbeBudget) (*bool, error) {
	out, _, err := budget.run("git", "-C", path, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	return boolPtr(len(bytes.TrimSpace(out)) > 0), nil
}

func statusRefExists(path, ref string, budget *StatusProbeBudget) (*bool, error) {
	_, _, err := budget.run("git", "-C", path, "rev-parse", "--verify", "--quiet", ref)
	if err == nil {
		return boolPtr(true), nil
	}
	if exitErr := completedStatusExit(err); exitErr != nil && exitErr.ExitCode() == 1 {
		return boolPtr(false), nil
	}
	return nil, err
}

// ResolveStatusWorkspaceWithBudget is the status-only workspace resolver. It
// avoids LoadConfig's hidden Git call and, for named external reports, limits
// repository inference to configured, sibling, and selected-feature evidence.
func ResolveStatusWorkspaceWithBudget(feature string, budget *StatusProbeBudget) (Workspace, string, error) {
	owned := false
	if budget == nil {
		budget = NewStatusProbeBudget(context.Background())
		owned = true
	}
	if owned {
		defer budget.Close()
	}

	cwd, err := os.Getwd()
	if err != nil {
		return Workspace{}, "", err
	}
	globalCfg := loadConfigFile(ConfigPath())
	repoRoot, repoErr := statusMainRepoRootIn(cwd, budget)
	if repoErr == nil {
		cfg := mergeConfig(globalCfg, loadConfigFile(RepoConfigPathFor(repoRoot)))
		ws, wsErr := ResolveCurrentWorkspaceE(repoRoot, cfg)
		if wsErr != nil {
			return Workspace{}, "", wsErr
		}
		ws.MetadataRoot = canonicalize(ws.MetadataRoot)
		if feature != "" {
			if _, selectErr := selectStatusFeature(ws, feature); selectErr != nil {
				return Workspace{}, "", selectErr
			}
		}
		return ws, "", nil
	}

	metadataRoot := DetectWorkspaceRoot(cwd, globalCfg)
	if metadataRoot == "" || !metadataRootExists(metadataRoot) {
		var probeErr *statusProbeError
		if errors.As(repoErr, &probeErr) && (probeErr.timedOut || probeErr.canceled) {
			return Workspace{}, "", fmt.Errorf("status workspace resolution failed because the Git probe %s", statusProbeProblem(repoErr))
		}
		if budget.Stopped() {
			return Workspace{}, "", fmt.Errorf("status workspace resolution stopped before a workspace could be established")
		}
		return Workspace{}, "", fmt.Errorf("not inside a git repository or tws workspace")
	}
	metadataRoot = canonicalize(metadataRoot)
	provisional := Workspace{
		Mode:         ModeExternal,
		MetadataRoot: metadataRoot,
		Caps:         capsFor(ModeExternal),
	}
	selectedPath := ""
	if feature != "" {
		selectedPath, err = selectStatusFeature(provisional, feature)
		if err != nil {
			return Workspace{}, "", err
		}
	}

	repoRoot, inferErr := inferStatusExternalRepoRoot(metadataRoot, globalCfg, selectedPath, budget)
	if inferErr != nil {
		return provisional, inferErr.Error(), nil
	}
	provisional.RepoRoot = canonicalize(repoRoot)
	provisional.StableID = stableID(provisional.RepoRoot)
	return provisional, "", nil
}

func ResolveStatusWorkspace() (Workspace, string, error) {
	budget := NewStatusProbeBudget(context.Background())
	defer budget.Close()
	return ResolveStatusWorkspaceWithBudget("", budget)
}

func selectStatusFeature(ws Workspace, feature string) (string, error) {
	if err := GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return "", err
	}
	path, err := ws.ResolveFeaturePathOrLegacy(feature)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("feature not found: %s", feature)
	}
	return path, nil
}

func inferStatusExternalRepoRoot(metadataRoot string, cfg Config, selectedPath string, budget *StatusProbeBudget) (string, error) {
	metadataRoot = canonicalize(metadataRoot)
	candidatePaths := map[string]bool{}
	addPath := func(path string) {
		if strings.TrimSpace(path) != "" {
			candidatePaths[cleanAbsolute(path)] = true
		}
	}

	for repo, configuredRoot := range cfg.Workspaces {
		if canonicalize(configuredRoot) == metadataRoot {
			addPath(repo)
		}
	}
	if siblingRepo, ok := strings.CutSuffix(metadataRoot, ".tws"); ok {
		addPath(siblingRepo)
	}
	if selectedPath != "" {
		if stack, err := LoadStack(selectedPath); err == nil {
			for _, entry := range stack.Branches {
				if entry.Repo != "" || entry.Archived {
					continue
				}
				worktreePath := filepath.Join(selectedPath, "worktrees", entry.Name)
				if info, statErr := os.Stat(worktreePath); statErr == nil && info.IsDir() {
					addPath(worktreePath)
				}
			}
		}
	} else {
		entries, _ := os.ReadDir(metadataRoot)
		for _, featureEntry := range entries {
			if !featureEntry.IsDir() || featureEntry.Name() == workspaceMarker {
				continue
			}
			featurePath := filepath.Join(metadataRoot, featureEntry.Name())
			stack, err := LoadStack(featurePath)
			if err != nil {
				continue
			}
			for _, entry := range stack.Branches {
				if entry.Repo != "" || entry.Archived {
					continue
				}
				worktreePath := filepath.Join(featurePath, "worktrees", entry.Name)
				if info, statErr := os.Stat(worktreePath); statErr == nil && info.IsDir() {
					addPath(worktreePath)
				}
			}
		}
	}

	paths := make([]string, 0, len(candidatePaths))
	for path := range candidatePaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	roots := map[string]bool{}
	var uncertain []string
	for _, path := range paths {
		root, err := statusMainRepoRootIn(path, budget)
		if err == nil {
			roots[canonicalize(root)] = true
			continue
		}
		var probeErr *statusProbeError
		if errors.As(err, &probeErr) && (probeErr.timedOut || probeErr.canceled) {
			uncertain = append(uncertain, fmt.Sprintf("%s (%s)", path, statusProbeProblem(err)))
		}
	}
	if len(uncertain) > 0 {
		return "", fmt.Errorf("cannot fully validate default repository candidates for external workspace %s: %s",
			metadataRoot, strings.Join(uncertain, ", "))
	}
	if len(roots) == 1 {
		for root := range roots {
			return root, nil
		}
	}
	if len(roots) > 1 {
		values := make([]string, 0, len(roots))
		for root := range roots {
			values = append(values, root)
		}
		sort.Strings(values)
		return "", fmt.Errorf("external workspace %s maps to multiple default repositories (%s); run from a worktree or repository",
			metadataRoot, strings.Join(values, ", "))
	}
	return "", fmt.Errorf("cannot determine source repository for external workspace %s; run from a worktree or configure the workspace path", metadataRoot)
}
