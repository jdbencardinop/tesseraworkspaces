# Feature Request: Restore the external workspace contract after workspace-mode refactoring: feature-oriented commands must resolve an external workspace and its default source repository when invoked from the external workspace root, a feature directory, or nested docs/inject paths, not only from Git repos/worktrees. Use the .tws-workspace marker, sibling repo convention, stack/worktree metadata fallback, and explicit ambiguity errors. Preserve checkout-mode resolution and multi-repo behavior. Add a command-location regression matrix for stack, sync, decisions, doctor, list, inject, and other feature commands.

**Slug**: `fix-external-feature-dir-resolution`
**Created**: 2026-08-05T22:41:20Z

## Description

Restore the external workspace contract after workspace-mode refactoring: feature-oriented commands must resolve an external workspace and its default source repository when invoked from the external workspace root, a feature directory, or nested docs/inject paths, not only from Git repos/worktrees. Use the .tws-workspace marker, sibling repo convention, stack/worktree metadata fallback, and explicit ambiguity errors. Preserve checkout-mode resolution and multi-repo behavior. Add a command-location regression matrix for stack, sync, decisions, doctor, list, inject, and other feature commands.
