# Feature Request: Add safe direct and tmux agent sessions for checkout workspace mode. Activate the requested logical Git branch in the single physical checkout, enforce exclusive branch-owning session state, namespace sessions by stable workspace ID, inject feature context into ignored local paths, launch/resume the configured agent, and restore the original branch when a direct session exits or `tws close` ends a tmux session. Reject open --all, dirty/detached repos, active checkout sync transactions, multi-repo state, and concurrent branch sessions. Preserve external open/close behavior exactly. Update embedded skills.

**Slug**: `checkout-agent-sessions`
**Created**: 2026-08-05T02:12:42Z

## Description

Add safe direct and tmux agent sessions for checkout workspace mode. Activate the requested logical Git branch in the single physical checkout, enforce exclusive branch-owning session state, namespace sessions by stable workspace ID, inject feature context into ignored local paths, launch/resume the configured agent, and restore the original branch when a direct session exits or `tws close` ends a tmux session. Reject open --all, dirty/detached repos, active checkout sync transactions, multi-repo state, and concurrent branch sessions. Preserve external open/close behavior exactly. Update embedded skills.
