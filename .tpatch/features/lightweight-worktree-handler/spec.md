# Specification: lightweight-worktree-handler

## Acceptance Criteria

1. `ts new <feature> <branch>` creates a git worktree at `<workspace>/<feature>/worktrees/<branch>/` using `git worktree add`
2. The created worktree is a real git worktree (verifiable via `git worktree list`)
3. `ts sync <feature>` runs fetch + rebase inside each worktree directory using `RunDir()`
4. Required tools (`git`) are validated before commands that need them; `tmux` validated before `ts open`
5. worktrunk is no longer a required dependency — removed from README requirements
6. All existing tests pass, new tests cover worktree creation path logic
7. `.cl` directory creation in `ts new` is replaced by copying the feature's `CLAUDE.local.md` as a symlink or left for the user

## Implementation Plan

1. **Add `RequireTool()` to `internal/exec.go`**
   - Uses `exec.LookPath()` to check if a binary is on PATH
   - Returns a clear error message if missing

2. **Rewrite `internal/cli/new.go`**
   - Validate `git` is available
   - Compute worktree path via `internal.WorktreePath(feature, branch)`
   - Create parent dirs, then run `git worktree add <path> -b <branch>`
   - Remove the `.cl` dir creation (was a TODO placeholder)
   - Print the created worktree path

3. **Fix `internal/cli/sync.go`**
   - Use `internal.RunDir(path, ...)` to run `git rebase` inside each worktree
   - Only iterate actual directories (skip files)

4. **Update `internal/cli/open.go`**
   - Validate `tmux` is available before launching session

5. **Update `README.md`**
   - Remove worktrunk from requirements
   - Note it as optional

6. **Tests**
   - `RequireTool()` with a known binary and a missing binary
