# Analysis: delete-feature

## Summary

Add `tws delete <feature>` to remove a feature and all its worktrees from disk. Branches are left in git. This is the inverse of `tws add` + `tws new`.

## Affected Areas

- **New: `internal/cli/delete.go`** — `Delete()` function
- **`cmd/tws/main.go`** — register `delete` subcommand

## Implementation

1. Load `stack.yaml` to find all tracked branches with worktrees
2. For each branch, run `git worktree remove <path>` (with `--force` if dirty)
3. Run `git worktree prune` to clean up stale refs
4. Remove the entire feature directory (`os.RemoveAll`)
5. Print summary of what was removed

## Acceptance Criteria

1. `tws delete <feature>` removes all worktrees via `git worktree remove`
2. The feature directory is deleted from the workspace
3. Git branches are NOT deleted
4. Works even if some worktrees are missing from disk (already removed manually)
5. Prompts or warns before destructive action
