# Analysis: auto-detect-repos-workspaces

## Summary

With user-defined-workdir now implemented, `TsRoot()` already auto-detects the current git repo via `RepoRoot()` and resolves the workspace sibling. This feature extends that by handling two additional scenarios:

1. **Running `ts` from inside a worktree** — `git rev-parse --show-toplevel` returns the worktree root, not the main repo. We need to resolve back to the main repo to find the correct workspace.
2. **Running `ts` from inside a workspace folder** (e.g., `../myapp.ts/auth/worktrees/branch-a/`) — we should detect this and resolve back to the parent workspace root so all commands work the same regardless of where you are.

## Affected Areas

- `internal/exec.go` — `RepoRoot()` needs to handle worktree detection (use `git rev-parse --git-common-dir` or `git worktree list` to find the main repo)
- `internal/paths.go` — `TsRoot()` may need a workspace-detection path: if cwd is inside a `*.ts/` directory, resolve the workspace root from the path
- `internal/paths_test.go` — new tests for worktree and workspace detection

## Compatibility

**Status**: safe

Builds directly on top of user-defined-workdir. The resolution chain gets smarter but the fallback behavior is unchanged.

## Current Behavior

- Inside main repo checkout → works (repo-relative sibling)
- Inside a worktree → `RepoRoot()` returns worktree path, not main repo → wrong workspace
- Inside a workspace folder → `RepoRoot()` may fail or return wrong path → falls back to `~/ts`

## Desired Behavior

- Inside main repo checkout → unchanged
- Inside a worktree → resolve to main repo, then resolve workspace normally
- Inside a workspace folder → detect `*.ts/` pattern in cwd, resolve workspace root directly

## Design Decisions (Resolved)

1. **Worktree detection**: Use `git rev-parse --git-common-dir` to find the main repo's `.git` dir, then resolve the actual repo root from that.
2. **Workspace detection** (3-tier):
   - **Primary**: Check for `.ts-workspace` marker file walking up from cwd
   - **Fallback 1**: If a global config exists, check if cwd is a prefix of any configured workspace path
   - **Fallback 2**: Check if cwd is inside `~/ts` (global default)

## Acceptance Criteria

1. Running `ts` from inside a git worktree resolves to the main repo's workspace
2. Running `ts` from inside a workspace folder (with `.ts-workspace` marker) works the same as from the repo root
3. Config path matching works as fallback when marker file is absent
4. `~/ts` heuristic works as final fallback
5. `ts add` creates the `.ts-workspace` marker in the workspace root
6. No regressions — existing behavior from outside worktrees/workspaces is unchanged
