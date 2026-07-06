# Implementation Record: per-file-inject-routing

**Recorded**: 2026-07-06T05:41:09Z
**Files changed**: 6
**Patch size**: 10574 bytes
**Capture mode**: committed range
**Base commit**: HEAD~1
**Upper bound**: HEAD

## Change Summary

```
 .tpatch/FEATURES.md                                  | 4 ++--
 .tpatch/features/branch-name-decoupling/status.json  | 7 ++++---
 .tpatch/features/per-file-inject-routing/status.json | 7 ++++---
 3 files changed, 10 insertions(+), 8 deletions(-)
```

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/per-file-inject-routing/artifacts/post-apply.patch
```

*Patch was captured as a committed diff from `HEAD~1` to `HEAD`.*
