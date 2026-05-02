# Feature Request: When opening a worktree for the first time via tws open, the tmux command runs "claude -c" which fails because there is no existing session to continue. On first open we should run "claude" (or the configured agent command) without the -c flag. Detect whether a session already exists to decide which variant to use.

**Slug**: `fix-initial-claude-session`
**Created**: 2026-05-02T04:36:30Z

## Description

When opening a worktree for the first time via tws open, the tmux command runs "claude -c" which fails because there is no existing session to continue. On first open we should run "claude" (or the configured agent command) without the -c flag. Detect whether a session already exists to decide which variant to use.
