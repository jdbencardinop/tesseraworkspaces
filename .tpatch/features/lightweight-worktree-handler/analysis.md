# Analysis: lightweight-worktree-handler

## Summary

The current `ts new` command calls `wt switch -c branch` without controlling where the worktree lands — worktrunk uses its default path template. We need to either:
- Configure worktrunk's `worktree-path` per-project to place worktrees under feature folders, OR
- Use `git worktree add` directly with explicit paths and keep worktrunk for switching/management

worktrunk supports a `worktree-path` template in its config (`~/.config/worktrunk/config.toml`) and per-project overrides. However, our model is **feature-scoped**: worktrees go under `<workspace>/<feature>/worktrees/<branch>/`, which doesn't map cleanly to worktrunk's template (it has no concept of "feature").

**Decision**: Use `git worktree add` directly for creation (we control the path), and keep worktrunk optional for switching between worktrees. This makes the tool work without worktrunk installed while still supporting it for users who want its extras.

## Affected Areas

- `internal/cli/new.go` — Replace `wt switch -c` with `git worktree add <path> -b <branch>`
- `internal/cli/sync.go` — Fix: run git commands inside each worktree dir (use `RunDir`)
- `internal/exec.go` — Add `RequireTool()` for validating tool availability
- `README.md` — Update requirements (worktrunk becomes optional)
- `docs/configuration.md` — Document worktree model and optional worktrunk integration

## Current Problems

1. `ts new` runs `wt switch -c branch` — worktree lands in worktrunk's default location, not under the feature folder
2. `ts new` computes `WorktreePath()` but never uses it for the actual worktree creation
3. `ts sync` runs `git rebase` in cwd, not in each worktree — rebases the wrong thing
4. No validation that required tools (git, tmux) are installed

## Design Decisions (Resolved)

1. **Use `git worktree add` directly** — we need explicit path control that worktrunk's templates can't express (feature-scoped paths)
2. **worktrunk becomes optional** — users can still use `wt switch` to navigate between worktrees, but creation/removal goes through `git worktree add/remove`
3. **Validate tools on startup** — `git` is required, `tmux` required for `ts open`, `wt` optional
4. **Fix sync** — use `RunDir()` to run git operations inside each worktree

## Acceptance Criteria

1. `ts new <feature> <branch>` creates a git worktree at `<workspace>/<feature>/worktrees/<branch>/`
2. The created worktree is a real git worktree (verified by `git worktree list`)
3. `ts sync` runs rebase inside each worktree directory, not cwd
4. `ts new` and `ts open` validate required tools before proceeding
5. worktrunk is no longer required — documented as optional enhancement
6. All existing tests pass, new tests for worktree creation logic
