# Exploration

## Critical Files

- `internal/cli/open.go`, `close.go`
- `internal/cli/hooks.go`
- `internal/inject.go`
- `internal/workspace.go`
- `internal/checkout_sync.go` (lock/transaction guards)
- new `internal/checkout_session.go`
- checkout session integration tests under `internal/cli/`

## Reuse

- Workspace stable ID and checkout metadata/state roots
- StackEntry.GitBranch()
- checkout sync dirty/detached/Git-operation checks and lock paths
- existing agent command / Claude session detection / tmux helpers

## Tests

Use real temporary repos and fake agent executables. Cover direct launch/restoration, tmux open/close, stale/live ownership, sync contention, dirty/detached refusal, branch prefixes, context collision/cleanup, restoration failure, non-Claude agents, and paired external open/close regression.
