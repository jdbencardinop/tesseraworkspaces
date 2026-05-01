# Exploration: auto-detect-repos-workspaces

## Relevant Files

- **`internal/exec.go`** — Add `MainRepoRoot()` using `git rev-parse --git-common-dir`
- **`internal/paths.go`** — Add `DetectWorkspaceRoot()`, update `resolveTsRoot()` signature and logic, switch from `RepoRoot()` to `MainRepoRoot()`
- **`internal/paths_test.go`** — Tests for workspace detection and worktree resolution
- **`internal/cli/add.go`** — Create `.ts-workspace` marker in workspace root on `ts add`

## Minimal Changeset

1. **`internal/exec.go`** — Add `MainRepoRoot()`: calls `git rev-parse --git-common-dir`, resolves to repo root. If result is `.git` (not a worktree), delegates to `RepoRoot()`.

2. **`internal/paths.go`**:
   - Add `DetectWorkspaceRoot(cwd string, cfg Config) string` with 3-tier detection
   - Update `resolveTsRoot()` to accept `cwd` param; check workspace detection before the existing 4-step chain
   - Replace `RepoRoot()` with `MainRepoRoot()` in the resolution chain

3. **`internal/cli/add.go`** — After `os.MkdirAll` for the feature, create `.ts-workspace` in `TsRoot()` if not present

4. **`internal/paths_test.go`** — Tests for `DetectWorkspaceRoot()` (marker, config match, ~/ts heuristic) and updated `resolveTsRoot()` with cwd awareness
