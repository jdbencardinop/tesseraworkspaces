# Feature Request: Investigate and implement automatic decision/message reading via agent hooks. Claude Code supports hooks in settings.json that trigger on events. Copilot and other agents may support file watchers. When a new decision is written to decisions.yaml, the agent in a sibling worktree could be notified automatically. This would make the cross-worktree communication more real-time without manual tws decisions checks.

**Slug**: `auto-read-decisions-hooks`
**Created**: 2026-05-14T05:46:01Z

## Description

Investigate and implement automatic decision/message reading via agent hooks. Claude Code supports hooks in settings.json that trigger on events. Copilot and other agents may support file watchers. When a new decision is written to decisions.yaml, the agent in a sibling worktree could be notified automatically. This would make the cross-worktree communication more real-time without manual tws decisions checks.

## Update: --all flag and auto_hooks config (v0.9.x)

Added tws hooks install --all to install hooks across all features at once. Added auto_hooks config option (tws config set auto_hooks true) that auto-installs Claude Code hooks on every tws new. This means users set it once and every new worktree gets hooks automatically.
