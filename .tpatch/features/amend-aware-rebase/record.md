# Implementation Record: amend-aware-rebase

**Recorded**: 2026-05-27T02:36:45Z
**Files changed**: 2
**Patch size**: 3380 bytes
**Capture mode**: committed range
**Base commit**: HEAD~1
**Upper bound**: HEAD

## Change Summary

```
 .tpatch/FEATURES.md                             | 2 +-
 .tpatch/features/amend-aware-rebase/status.json | 7 ++++---
 2 files changed, 5 insertions(+), 4 deletions(-)
```

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/amend-aware-rebase/artifacts/post-apply.patch
```

*Patch was captured as a committed diff from `HEAD~1` to `HEAD`.*
