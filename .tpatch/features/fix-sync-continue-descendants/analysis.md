# Analysis: fix-sync-continue-descendants

## Summary

When a branch conflicts, current sync state stores all descendants as skipped. `--continue` treats them as permanently done, clears state, and reports completion even when descendants remain based on the old parent. Deferred descendants must return to pending after the failed branch is resolved.

## Compatibility

The persisted state schema can evolve compatibly by distinguishing pending/deferred work from terminal skips. Existing state files should be interpreted conservatively or rejected with guidance.

## Risks

- A second conflict must replace/persist state rather than being deleted by the caller.
- Completion must be based on actual ancestry, not loop termination.
- Multi-repo and decoupled branch names require repo-aware `GitBranch()` ancestry checks.
- `--push` must not run after partial continuation.
