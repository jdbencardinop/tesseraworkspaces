# Specification: fix-sync-branch-identity

## Problem

Sync validates active worktrees against the short tws name instead of the actual Git branch.

## Acceptance Criteria

1. A stack entry with `name: pr1` and `branch: user/feature/pr1` passes sync branch validation when that Git branch is checked out.
2. A genuinely wrong checked-out branch is rejected.
3. Archived/ref operations use `GitBranch()`.
4. Existing stacks without `branch` remain unchanged.
5. `go test ./internal/cli -run TestSyncBranchIdentity` passes.

## Out of Scope

Changing worktree path naming or branch-prefix generation.

## Plan

Audit active and archived sync Git operations in `internal/cli/sync_helpers.go`, convert branch-ref arguments to `GitBranch()`, and add focused tests.
