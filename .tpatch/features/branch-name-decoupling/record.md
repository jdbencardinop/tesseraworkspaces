# Implementation Record: branch-name-decoupling

**Recorded**: 2026-07-06T05:41:09Z
**Files changed**: 6
**Patch size**: 10574 bytes
**Capture mode**: committed range
**Base commit**: HEAD~1
**Upper bound**: HEAD

## Collision Override

**Reason**: bundled in same commit

This patch is byte-identical to the canonical post-apply.patch of:

- `per-file-inject-routing` — sha256=688499a75643... bytes=10574 files=6

## Change Summary

```
 .tpatch/FEATURES.md                                  |  4 ++--
 .tpatch/features/branch-name-decoupling/status.json  |  7 ++++---
 .tpatch/features/per-file-inject-routing/status.json | 12 ++++++++----
 3 files changed, 14 insertions(+), 9 deletions(-)
```

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/branch-name-decoupling/artifacts/post-apply.patch
```

*Patch was captured as a committed diff from `HEAD~1` to `HEAD`.*
