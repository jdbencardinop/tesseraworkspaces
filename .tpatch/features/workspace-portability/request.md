# Feature Request: Add a way to sync or recreate workspace metadata across machines. When you push branches from one machine and pull on another, the workspace structure (stack.yaml, decisions, inject config) doesn't travel. Options: export/import commands, git-backed metadata in .tws/ inside the repo, or a tws clone command that recreates a workspace from existing branches. The goal is that when you git clone + fetch on a new machine, you can reconstruct the full tws workspace.

**Slug**: `workspace-portability`
**Created**: 2026-05-19T19:31:59Z

## Description

Add a way to sync or recreate workspace metadata across machines. When you push branches from one machine and pull on another, the workspace structure (stack.yaml, decisions, inject config) doesn't travel. Options: export/import commands, git-backed metadata in .tws/ inside the repo, or a tws clone command that recreates a workspace from existing branches. The goal is that when you git clone + fetch on a new machine, you can reconstruct the full tws workspace.
