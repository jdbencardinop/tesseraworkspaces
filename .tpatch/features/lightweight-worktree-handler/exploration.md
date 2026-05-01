# Exploration: lightweight-worktree-handler

## Relevant Files

- **`internal/cli/new.go`** — Replace `wt switch -c` with `git worktree add`, remove `.cl` dir creation
- **`internal/cli/sync.go`** — Fix rebase to run inside each worktree via `RunDir()`
- **`internal/cli/open.go`** — Add `tmux` validation before launching session
- **`internal/exec.go`** — Add `RequireTool()` using `exec.LookPath()`
- **`README.md`** — Remove worktrunk requirement, mark as optional

## Minimal Changeset

1. **`internal/exec.go`** — Add `RequireTool(name string) error` that wraps `exec.LookPath()`.

2. **`internal/cli/new.go`** — Full rewrite:
   - Call `RequireTool("git")`
   - `os.MkdirAll` on worktree parent dir
   - `RunDir(repoRoot, "git", "worktree", "add", path, "-b", branch)`
   - Remove `.cl` directory creation

3. **`internal/cli/sync.go`** — Fix loop body:
   - Change `internal.Run("git", "rebase", ...)` to `internal.RunDir(path, "git", "rebase", ...)`
   - Filter `entries` to only directories

4. **`internal/cli/open.go`** — Add `RequireTool("tmux")` at top of function.

5. **`README.md`** — Remove worktrunk from requirements list, add note about optional integration.
