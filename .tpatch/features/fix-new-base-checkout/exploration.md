# Exploration: fix-new-base-checkout

## Relevant Files

- `internal/cli/new.go`: `newCmd`, `createWorktree`, `isCheckedOutIn`
- `internal/exec.go`: `DefaultBranch`, repo-scoped Git helpers
- `internal/stack.go`: `StackEntry.Repo`, `GitBranch`
- new `internal/cli/new_integration_test.go`

## Minimal Change

Resolve source repo first; resolve omitted default in that repo; validate explicit/literal ref; pass base to `git worktree add -b`; infer one repo from feature metadata when CWD is non-Git; write metadata only after Git success.
