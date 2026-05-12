# Analysis: worktree-health-check

## Summary

Add branch consistency validation to tws sync and tws list. Before rebasing, verify each worktree is on the branch that stack.yaml expects. If mismatched, warn and skip. Add a tws doctor command for full health checks.

## Checks

1. **Branch mismatch**: worktree is on a different branch than stack.yaml expects
2. **Dirty worktree**: uncommitted changes that would block rebase
3. **Missing inject symlinks**: inject/ files not present in worktree

## Affected Areas

- New: `internal/health.go` — CheckWorktreeHealth() returns issues
- New: `internal/cli/doctor.go` — tws doctor command
- `internal/cli/sync.go` — check branch before rebasing
- `internal/cli/list.go` — show warning indicator for unhealthy worktrees

## Acceptance Criteria

1. `tws sync` warns and skips worktrees on the wrong branch
2. `tws list` shows a warning indicator for mismatched branches
3. `tws doctor <feature>` runs all health checks and reports issues
4. `tws doctor` with no args checks all features
