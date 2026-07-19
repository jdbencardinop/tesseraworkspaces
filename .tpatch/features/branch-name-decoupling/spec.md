# Specification: branch-name-decoupling

## Acceptance Criteria

1. Stack entries may store a short tws `name` and a distinct Git `branch`.
2. Worktree directories use the short name and remain flat when Git branch names contain slashes.
3. `branch_prefix` can create policy-compliant Git branch names while preserving short tws names.
4. Doctor and Git-facing operations resolve the actual branch through `StackEntry.GitBranch()`.
5. Existing entries without `branch` remain backward compatible.

## Historical Scope Note

This specification documents the already-landed implementation. Preserving arbitrary non-injected untracked files during rename remained outside the landed patch and is tracked separately if needed.
