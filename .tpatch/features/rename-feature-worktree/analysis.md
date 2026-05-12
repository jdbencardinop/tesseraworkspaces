# Analysis: rename-feature-worktree

## Summary

Add tws rename for renaming features or branches. Two modes:
- `tws rename feature <old> <new>` — rename feature directory
- `tws rename branch <feature> <old> <new>` — rename branch in stack, worktree dir, optionally git branch

## Complexity

Branch rename is the tricky one — need to update:
1. stack.yaml entry name
2. stack.yaml base references from other branches
3. Worktree directory name
4. Git branch name
5. Kill/rename tmux session if active
6. decisions.yaml branch references

## Affected Areas

- New: `internal/cli/rename.go` — rename command with feature/branch subcommands
- `internal/stack.go` — helper to rename branch in stack (name + base refs)
- `internal/cli/root.go` — register command

## Acceptance Criteria

1. `tws rename feature <old> <new>` renames the feature directory
2. `tws rename branch <feature> <old> <new>` renames branch in stack.yaml, worktree dir, and git branch
3. Other branches' base references are updated
4. decisions.yaml entries referencing old branch are updated
5. Tmux sessions are killed for the old name
