# Feature Request: Fix GitHub issue #3: 'tws status <feature>' must validate and build only the requested feature plus the minimum workspace-level evidence instead of building every feature and filtering afterward. Unrelated slow, missing, corrupt, or unreadable feature metadata and worktrees must not delay or fail a scoped report. Add bounded Git/tmux/process probe contexts that degrade unavailable facts to explicit issues without changing all-workspace output or the versioned status schema.

**Slug**: `scoped-status-projection`
**Created**: 2026-09-12T15:42:23Z

## Description

Fix GitHub issue #3: 'tws status <feature>' must validate and build only the requested feature plus the minimum workspace-level evidence instead of building every feature and filtering afterward. Unrelated slow, missing, corrupt, or unreadable feature metadata and worktrees must not delay or fail a scoped report. Add bounded Git/tmux/process probe contexts that degrade unavailable facts to explicit issues without changing all-workspace output or the versioned status schema.
