# Feature Request: Introduce an explicit persisted workspace mode enum with `external` (current sibling multi-worktree backend, default) and `checkout` (future in-repo single-checkout backend). Add a resolved Workspace value carrying stable identity, mode, repository root, metadata root, and capability flags. Route current path resolution through the external backend without changing any existing outputs or behavior. Add extensive characterization and regression tests before checkout lifecycle behavior is implemented. Update embedded skills when agent-facing behavior changes.

**Slug**: `workspace-mode-foundation`
**Created**: 2026-07-29T05:23:05Z

## Description

Introduce an explicit persisted workspace mode enum with `external` (current sibling multi-worktree backend, default) and `checkout` (future in-repo single-checkout backend). Add a resolved Workspace value carrying stable identity, mode, repository root, metadata root, and capability flags. Route current path resolution through the external backend without changing any existing outputs or behavior. Add extensive characterization and regression tests before checkout lifecycle behavior is implemented. Update embedded skills when agent-facing behavior changes.
