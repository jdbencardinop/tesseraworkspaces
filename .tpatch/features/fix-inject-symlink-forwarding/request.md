# Feature Request: Bug: When a symlink is placed in the inject/ directory (pointing to a file outside the workspace), tws inject creates a copy or fails instead of forwarding the symlink into the worktree. Users want to place symlinks in inject/ so that worktrees reference the same external target, preserving the symlink chain rather than copying the resolved content.

**Slug**: `fix-inject-symlink-forwarding`
**Created**: 2026-05-22T23:17:15Z

## Description

Bug: When a symlink is placed in the inject/ directory (pointing to a file outside the workspace), tws inject creates a copy or fails instead of forwarding the symlink into the worktree. Users want to place symlinks in inject/ so that worktrees reference the same external target, preserving the symlink chain rather than copying the resolved content.
