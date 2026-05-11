# Implementation Record: fix-auto-select-behavior

**Recorded**: 2026-05-11T04:01:04Z
**Files changed**: 2
**Patch size**: 1988 bytes

## Change Summary

```
 .tpatch/FEATURES.md                                   |  4 ++--
 .tpatch/features/fix-auto-select-behavior/status.json |  7 ++++---
 .tpatch/features/fix-open-cwd-after-exit/status.json  | 12 ++++++++----
 3 files changed, 14 insertions(+), 9 deletions(-)
```

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-auto-select-behavior/artifacts/post-apply.patch
```

*Patch was captured as a committed diff from `HEAD~1` to `HEAD`.*
