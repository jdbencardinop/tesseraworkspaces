# Feature Request: Add a managed named-template registry for feature creation. `tws template add <name> <path>` copies a template into global or per-repo tws storage; list/show/remove provide discovery and lifecycle management; `tws add <feature> --template <name>` resolves a stable template. Templates have explicit `feature/` assets copied into the feature root and `inject/` assets copied into the worktree injection source. Support feature-space orchestrator skills, docs, prompts, and shared inject files. Update embedded skills when agent-facing behavior changes.

**Slug**: `named-feature-templates`
**Created**: 2026-07-22T05:45:39Z

## Description

Add a managed named-template registry for feature creation. `tws template add <name> <path>` copies a template into global or per-repo tws storage; list/show/remove provide discovery and lifecycle management; `tws add <feature> --template <name>` resolves a stable template. Templates have explicit `feature/` assets copied into the feature root and `inject/` assets copied into the worktree injection source. Support feature-space orchestrator skills, docs, prompts, and shared inject files. Update embedded skills when agent-facing behavior changes.

GitHub issue #2 clarifies the role-aware template contract: worktree and feature-orchestrator templates must be independently selectable, have global and per-repository defaults, support 'tws template sync --scope worktree|orchestrator|both', render native Claude and Copilot instruction assets from the selected agent/role, refuse unmanaged file or symlink replacement, and optionally render role-filtered skill manifests without copying canonical skill bodies.
