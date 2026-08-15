# Feature Request: Add explicit non-agent launch surfaces to tws open without changing the configured agent_command: a machine-readable path-only mode suitable for shell integration, and a per-invocation command/editor mode for tools such as nvim in the selected worktree or feature directory, supporting direct and tmux execution where meaningful. Preserve the existing --no-agent human guidance and normal agent-session behavior; do not claim a child process can change the parent shell cwd; pass command arguments without shell eval; do not misreport a generic editor as semantic agent work; and keep lifecycle guards, injection, workspace-mode routing, and existing open output compatible.

**Slug**: `open-worktree-command`
**Created**: 2026-08-15T08:35:26Z

## Description

Add explicit non-agent launch surfaces to tws open without changing the configured agent_command: a machine-readable path-only mode suitable for shell integration, and a per-invocation command/editor mode for tools such as nvim in the selected worktree or feature directory, supporting direct and tmux execution where meaningful. Preserve the existing --no-agent human guidance and normal agent-session behavior; do not claim a child process can change the parent shell cwd; pass command arguments without shell eval; do not misreport a generic editor as semantic agent work; and keep lifecycle guards, injection, workspace-mode routing, and existing open output compatible.
