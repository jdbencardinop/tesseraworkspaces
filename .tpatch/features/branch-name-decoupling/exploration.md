# Exploration: branch-name-decoupling

## Landed Files

- `internal/stack.go`: `StackEntry.Branch`, `GitBranch()`
- `internal/config.go`: `branch_prefix`
- `internal/cli/new.go`: prefixed Git branches with flat worktree names
- `internal/health.go`: actual Git branch validation
- `internal/cli/config.go`: branch prefix configuration
- `internal/cli/sync_helpers.go`: archived Git ref handling

## Historical Note

This artifact documents the already-landed implementation so current tpatch verification can evaluate dependent feature closures.
