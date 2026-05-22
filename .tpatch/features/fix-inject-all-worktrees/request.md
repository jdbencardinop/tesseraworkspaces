# Feature Request: Bug: tws inject only populates the first worktree in a feature, not all stacked worktrees. Reported by a user with 3 stacked worktrees where only the first got inject symlinks. Investigate whether InjectFilesForFeature iterates all worktree dirs correctly, especially with stacked branch structures.

**Slug**: `fix-inject-all-worktrees`
**Created**: 2026-05-22T23:17:25Z

## Description

Bug: tws inject only populates the first worktree in a feature, not all stacked worktrees. Reported by a user with 3 stacked worktrees where only the first got inject symlinks. Investigate whether InjectFilesForFeature iterates all worktree dirs correctly, especially with stacked branch structures.
