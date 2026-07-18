# Analysis: fix-new-base-checkout

## Summary

New worktree branches currently start from the selected repository's current `HEAD`; `--base` only updates `stack.yaml`. This can silently inherit unrelated local commits. The fix must resolve and validate the base in the selected repository before any worktree or stack mutation.

## Compatibility

Backward compatible for existing branches. For new branches, behavior changes to match the documented `--base` contract. Explicit refs remain literal; omitted bases use the selected repository's `origin/HEAD`.

## Risks

- Base/default detection must run in the `--repo` source repository, not process CWD.
- Feature-directory invocation may be ambiguous for multi-repo features.
- Existing branches must not be reset to the requested base.
- Validation failure must leave no partial worktree or stack entry.
