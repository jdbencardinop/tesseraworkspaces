# Implementation Record: fix-atomic-rename

**Recorded**: 2026-07-03T22:36:01Z
**Files changed**: 5
**Patch size**: 7437 bytes
**Capture mode**: committed range
**Base commit**: HEAD~1
**Upper bound**: HEAD

## Collision Override

**Reason**: bundled with fix-default-base-branch in same commit

This patch is byte-identical to the canonical post-apply.patch of:

- `fix-default-base-branch` — sha256=15b9dffdd33e... bytes=7437 files=5

## Change Summary

```
 .tpatch/FEATURES.md                                  |  4 ++--
 .tpatch/features/fix-atomic-rename/status.json       |  7 ++++---
 .tpatch/features/fix-default-base-branch/status.json | 12 ++++++++----
 3 files changed, 14 insertions(+), 9 deletions(-)
```

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-atomic-rename/artifacts/post-apply.patch
```

*Patch was captured as a committed diff from `HEAD~1` to `HEAD`.*
