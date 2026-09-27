# Feature Request: Fix GitHub issue #1: feature-wide 'tws inject <feature>' must discover every materialized worktree when a logical entry name contains '/' instead of treating the first path segment as the worktree root. Resolve worktree targets from stack.yaml and the mode-aware layout (or verified recursive .git markers), never inject into intermediate directories, preserve single-segment behavior, and add external/checkout coverage for slash-containing logical names and decoupled Git branches.

**Slug**: `fix-inject-slash-worktree-discovery`
**GitHub issue**: [#1](https://github.com/jdbencardinop/tesseraworkspaces/issues/1)
**Created**: 2026-09-12T15:42:23Z

## Description

Fix GitHub issue #1: feature-wide 'tws inject <feature>' must discover every materialized worktree when a logical entry name contains '/' instead of treating the first path segment as the worktree root. Resolve worktree targets from stack.yaml and the mode-aware layout (or verified recursive .git markers), never inject into intermediate directories, preserve single-segment behavior, and add external/checkout coverage for slash-containing logical names and decoupled Git branches.
