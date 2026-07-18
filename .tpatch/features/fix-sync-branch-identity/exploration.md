# Exploration: fix-sync-branch-identity

## Relevant Files

- `internal/cli/sync_helpers.go`: active health check, archived rebase refs, state/output
- `internal/health.go`: existing correct `GitBranch()` pattern
- new focused test in `internal/cli/sync_branch_identity_test.go`

## Minimal Change

Replace Git-facing uses of `entry.Name` with `entry.GitBranch()` while retaining `Name` for worktree paths and display labels.
