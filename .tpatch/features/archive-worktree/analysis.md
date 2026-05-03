# Analysis: archive-worktree

## Summary

Add `tws archive <feature> <branch>` to remove a worktree from disk while keeping the branch ref and its entry in `stack.yaml`. The dependency graph is preserved. Sync handles archived branches via `--update-refs` (when an active child exists) or optimistic `git rebase <base> <branch>` (when no active child). `tws new` becomes idempotent for `stack.yaml` to serve as the restore path.

## Affected Areas

- **New: `internal/cli/archive.go`** — `Archive()` function: remove worktree, prune, keep stack entry
- **`internal/cli/sync.go`** — Handle archived branches: use `--update-refs` for active branches, optimistic rebase for standalone archived branches
- **`internal/cli/new.go`** — Make stack.yaml registration idempotent (skip if entry exists)
- **`internal/cli/list.go`** — Change "missing" wording to "archived" for branches in stack.yaml without worktrees
- **`cmd/tws/main.go`** — Register `archive` subcommand, update help
- **`internal/stack.go`** — Add helper to check if branch exists in stack
- **`internal/exec.go`** — Add `RunSilent()` for capturing rebase output without printing

## Compatibility

**Status**: safe — behavioral change to sync improves correctness

Current sync skips archived branches silently. New behavior tries to keep them up to date via `--update-refs` or optimistic rebase, which is strictly better.

## Sync Strategy

For active branches: `RunDir(worktreePath, "git", "rebase", "--update-refs", base)`
- `--update-refs` automatically updates any archived branch refs between this branch and its base
- Explicit flag since we can't assume users have `rebase.updateRefs` in their git config

For archived branches with no active child below them:
- Try `git rebase <base> <branch>` (works without a worktree for clean rebases)
- On conflict: `git rebase --abort`, warn user to restore with `tws new`
- Skip descendants as usual

## Recovery Workflow

1. `tws sync auth` → conflict on archived `auth-middleware`
2. User runs `tws new auth auth-middleware` → restores worktree (idempotent, no stack.yaml duplicate)
3. User resolves conflicts in the worktree
4. Runs `tws sync auth` again → works

## Acceptance Criteria

1. `tws archive <feature> <branch>` removes worktree, keeps stack entry and git branch
2. `tws list` shows archived branches as `[archived]` not `[missing]`
3. `tws sync` uses `--update-refs` for active branches to keep archived intermediates in sync
4. `tws sync` does optimistic rebase for standalone archived branches, aborts on conflict
5. `tws new` is idempotent for stack.yaml (no duplicate entries on restore)
6. `git worktree prune` runs after archive
7. Full recovery workflow works: archive → sync conflict → restore → resolve → sync
