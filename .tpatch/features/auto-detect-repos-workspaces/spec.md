# Specification: auto-detect-repos-workspaces

## Acceptance Criteria

1. Running `ts` from inside a git worktree resolves to the main repo's workspace
2. Running `ts` from inside a workspace folder (with `.ts-workspace` marker) works identically to running from the repo root
3. Config path matching works as fallback when marker file is absent
4. `~/ts` path heuristic works as final fallback when no config exists
5. `ts add` creates the `.ts-workspace` marker directory in the workspace root
6. No regressions — all existing tests pass, existing resolution chain unchanged

## Implementation Plan

1. **Add `MainRepoRoot()` to `internal/exec.go`**
   - Use `git rev-parse --git-common-dir` to get the main repo's `.git` path
   - Resolve to the repo root (parent of `.git`)
   - Fall back to `RepoRoot()` if not in a worktree (common-dir == `.git`)

2. **Add workspace detection to `internal/paths.go`**
   - `DetectWorkspaceRoot(cwd string, cfg Config) string` — returns workspace root or empty string
   - Tier 1: Walk up from cwd looking for `.ts-workspace` directory
   - Tier 2: Check if cwd is a subpath of any configured workspace in `cfg.Workspaces`
   - Tier 3: Check if cwd is inside `~/ts`

3. **Update `resolveTsRoot()`**
   - Add cwd parameter
   - Before the existing 4-step chain, check if `DetectWorkspaceRoot()` returns a result
   - If inside a workspace, return that root directly (we're already in a workspace, no need to resolve)

4. **Update `ts add` in `internal/cli/add.go`**
   - After creating the feature directory, create `.ts-workspace` in the workspace root if it doesn't already exist

5. **Update `internal/paths.go` to use `MainRepoRoot()` instead of `RepoRoot()`**
   - So that worktree detection feeds the correct repo path into config lookup and sibling resolution

6. **Tests**
   - Test `MainRepoRoot()` vs `RepoRoot()` behavior
   - Test `DetectWorkspaceRoot()` with marker file, config match, and ~/ts heuristic
   - Test that `resolveTsRoot()` short-circuits when already inside a workspace
