# Specification: archive-worktree

## Acceptance Criteria

1. `tws archive <feature> <branch>` removes worktree from disk, runs `git worktree prune`, keeps stack.yaml entry and git branch
2. `tws list` shows `[archived]` for branches in stack.yaml without worktrees on disk
3. `tws sync` passes `--update-refs` on active branch rebases, updating archived intermediates automatically
4. `tws sync` does optimistic `git rebase <base> <branch>` for standalone archived branches; `--abort` and warn on conflict
5. `tws new` skips stack.yaml append if branch already exists in the stack (idempotent restore)
6. Recovery: archive → sync conflict → `tws new` restore → resolve → sync again

## Implementation Plan

1. **`internal/stack.go`** — Add `HasBranch(stack, name) bool` helper
2. **`internal/exec.go`** — Add `RunSilent(name, args) error` that suppresses stdout/stderr (for optimistic rebase)
3. **New: `internal/cli/archive.go`** — Remove worktree via `git worktree remove`, run prune, print status
4. **`internal/cli/sync.go`** — Add `--update-refs` to active rebases. For archived branches: check if already handled by a child's `--update-refs`; if not, try optimistic rebase
5. **`internal/cli/new.go`** — Check `HasBranch()` before appending to stack.yaml
6. **`internal/cli/list.go`** — Change "missing" to "archived"
7. **`cmd/tws/main.go`** — Register `archive`, update help
