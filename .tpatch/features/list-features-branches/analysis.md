# Analysis: list-features-branches

## Summary

Add `tws list` (aliased as `tws ls`) to show all features and their branches in the current workspace. Reads the workspace directory structure and stack.yaml files.

## Affected Areas

- **New: `internal/cli/list.go`** — List() function
- **`cmd/tws/main.go`** — register `list` and `ls` subcommands, update help

## Acceptance Criteria

1. `tws list` and `tws ls` show all features with their branches
2. Shows branch base from stack.yaml when available
3. Shows worktree status (exists on disk or archived/missing)
4. Works with empty workspaces (no features yet)
5. Handles features with no stack.yaml (just lists worktree dirs)
