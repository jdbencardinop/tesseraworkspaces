# Analysis: checkout-existing-branch

## Summary

`tws new` currently always creates new branches (`-b`). Need to detect if the branch already exists and use the right git command. For branches already checked out in the main repo, use `--force`.

## Implementation

1. Check if branch exists: `git rev-parse --verify <branch>`
2. If exists: `git worktree add <path> <branch>` (no `-b`), with `--force` if needed
3. If new: current behavior (`-b`)
