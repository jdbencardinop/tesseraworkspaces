# Feature Request: The CLAUDE.local.md at the feature level is a start for shared context across worktrees, but we need a mechanism for agents to advertise design decisions and breaking changes to sibling branches. Add a changes.log or decisions.yaml at the feature level where agents write design decisions that affect sibling branches. Agents in child branches can read these and decide if they need to adapt. This is the cross-worktree semantic communication layer that no existing stacked-diff tool provides.

**Slug**: `cross-worktree-agent-context`
**Created**: 2026-05-01T09:03:28Z

## Description

The CLAUDE.local.md at the feature level is a start for shared context across worktrees, but we need a mechanism for agents to advertise design decisions and breaking changes to sibling branches. Add a changes.log or decisions.yaml at the feature level where agents write design decisions that affect sibling branches. Agents in child branches can read these and decide if they need to adapt. This is the cross-worktree semantic communication layer that no existing stacked-diff tool provides.
