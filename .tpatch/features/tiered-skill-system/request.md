# Feature Request: Investigate and design a 3-tier skill system: (1) worktree-level skills — injected into each worktree, agents work on code within a branch. (2) feature-level skills — in the feature folder, an orchestrator agent can manage other agents, coordinate decisions, track progress across branches. (3) global tws skills — installed via tws init, knows how to create features and workspaces from anywhere. The feature-level orchestrator is the novel part — it could run tws commands to sync, check decisions, and direct agents in worktrees. Needs investigation into whether this orchestration pattern is practical with current agent capabilities.

**Slug**: `tiered-skill-system`
**Created**: 2026-05-26T19:42:45Z

## Description

Investigate and design a 3-tier skill system: (1) worktree-level skills — injected into each worktree, agents work on code within a branch. (2) feature-level skills — in the feature folder, an orchestrator agent can manage other agents, coordinate decisions, track progress across branches. (3) global tws skills — installed via tws init, knows how to create features and workspaces from anywhere. The feature-level orchestrator is the novel part — it could run tws commands to sync, check decisions, and direct agents in worktrees. Needs investigation into whether this orchestration pattern is practical with current agent capabilities.
