# Feature Request: Add explicit non-agent launch surfaces to tws open without changing the configured agent_command: a machine-readable path-only mode suitable for shell integration, and a per-invocation command/editor mode for tools such as nvim in the selected worktree or feature directory, supporting direct and tmux execution where meaningful. Preserve the existing --no-agent human guidance and normal agent-session behavior; do not claim a child process can change the parent shell cwd; pass command arguments without shell eval; do not misreport a generic editor as semantic agent work; and keep lifecycle guards, injection, workspace-mode routing, and existing open output compatible.

**Slug**: `open-worktree-command`
**Created**: 2026-08-15T08:35:26Z

## Description

Add explicit non-agent launch surfaces to tws open without changing the configured agent_command: a machine-readable path-only mode suitable for shell integration, and a per-invocation command/editor mode for tools such as nvim in the selected worktree or feature directory, supporting direct and tmux execution where meaningful. Preserve the existing --no-agent human guidance and normal agent-session behavior; do not claim a child process can change the parent shell cwd; pass command arguments without shell eval; do not misreport a generic editor as semantic agent work; and keep lifecycle guards, injection, workspace-mode routing, and existing open output compatible.

## Tracking

- GitHub: [#10](https://github.com/jdbencardinop/tesseraworkspaces/issues/10).
- Independent Path B boundary after verified `v1.2.19-rc.2` at
  `034126331db73522bb98d1f1a1fc08e146a68fe5`.
- Concrete mode, argv, observation and lifecycle decisions are defined in
  `spec.md`; the original non-agent scope and legacy compatibility remain.
