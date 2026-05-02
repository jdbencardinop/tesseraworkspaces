# Specification: delete-feature

## Acceptance Criteria

1. `tws delete <feature>` removes all worktrees and the feature directory
2. Git branches are preserved
3. Handles missing worktrees gracefully (already removed)
4. Runs `git worktree prune` after removal
5. Prints what was removed

## Minimal Changeset

1. New `internal/cli/delete.go`
2. Register `delete` in `cmd/tws/main.go`
