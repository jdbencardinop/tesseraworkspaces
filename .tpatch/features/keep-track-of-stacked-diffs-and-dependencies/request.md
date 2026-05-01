# Feature Request: Our sync method currently just rebases every single branch we have a worktree for, but this might not be what we want, we might just need to advance some of the worktrees or take just changes in one worktree, check if we need one worktree per stack or one worktree per feature, we need to implement a better approach to handling a graph of dependencies in the changes

**Slug**: `keep-track-of-stacked-diffs-and-dependencies`
**Created**: 2026-05-01T06:01:49Z

## Description

Our sync method currently just rebases every single branch we have a worktree for, but this might not be what we want, we might just need to advance some of the worktrees or take just changes in one worktree, check if we need one worktree per stack or one worktree per feature, we need to implement a better approach to handling a graph of dependencies in the changes
