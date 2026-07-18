# Analysis: fix-sync-branch-identity

## Summary

Branch-name decoupling introduced separate tws names and Git branch names, but active sync validation still compares the checkout against `StackEntry.Name`. Prefixed branches therefore fail health validation even when correct. Git operations must use `GitBranch()` while paths and display output continue using `Name`.

## Compatibility

Fully backward compatible because `GitBranch()` falls back to `Name` when no explicit branch mapping exists.

## Risks

Audit all sync/ref code paths to avoid partial conversion. User output should retain short names and include full Git names only where needed for diagnosis.
