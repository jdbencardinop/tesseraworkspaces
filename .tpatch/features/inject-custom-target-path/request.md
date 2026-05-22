# Feature Request: Add --into flag to tws inject for specifying a custom target subdirectory inside the worktree. Currently inject/ maps to the worktree root. With --into, users can target a gitignored subfolder like .context/ or .claude/. Example: tws inject auth --into .context would symlink inject/ contents into <worktree>/.context/ instead of <worktree>/. Users can work around this today by nesting folders inside inject/ (e.g., inject/.context/file.md) but this should be documented better and the --into flag adds clarity.

**Slug**: `inject-custom-target-path`
**Created**: 2026-05-22T23:17:28Z

## Description

Add --into flag to tws inject for specifying a custom target subdirectory inside the worktree. Currently inject/ maps to the worktree root. With --into, users can target a gitignored subfolder like .context/ or .claude/. Example: tws inject auth --into .context would symlink inject/ contents into <worktree>/.context/ instead of <worktree>/. Users can work around this today by nesting folders inside inject/ (e.g., inject/.context/file.md) but this should be documented better and the --into flag adds clarity.
