# Feature Request: Add a workspace-level registry for tool-owned sibling spaces such as learning, tickets, patching, research, and documentation. Store dynamic links in <workspace-root>/spaces.yaml rather than embedding paths in skills. Each entry has a stable name, kind, path, optional description, and optional feature scope. Add `tws space add/list/show/remove` and no-arg auto-detection from repos, feature dirs, and worktrees. Skills should teach agents to discover links with `tws space list`; tws must not own the linked tool's schema or lifecycle. Support absolute paths and workspace-relative paths, validate existence without requiring all targets to be Git repos, and keep a higher-level multi-project hub out of scope.

**Slug**: `workspace-sibling-links`
**Created**: 2026-07-22T13:16:02Z

## Description

Add a workspace-level registry for tool-owned sibling spaces such as learning, tickets, patching, research, and documentation. Store dynamic links in <workspace-root>/spaces.yaml rather than embedding paths in skills. Each entry has a stable name, kind, path, optional description, and optional feature scope. Add `tws space add/list/show/remove` and no-arg auto-detection from repos, feature dirs, and worktrees. Skills should teach agents to discover links with `tws space list`; tws must not own the linked tool's schema or lifecycle. Support absolute paths and workspace-relative paths, validate existence without requiring all targets to be Git repos, and keep a higher-level multi-project hub out of scope.
