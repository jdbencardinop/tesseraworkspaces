# Implementation Record: auto-read-decisions-hooks

**Recorded**: 2026-05-27T05:32:58Z
**Files changed**: 4
**Patch size**: 6732 bytes
**Capture mode**: committed range
**Base commit**: HEAD~1
**Upper bound**: HEAD

## Change Summary

```
 .tpatch/FEATURES.md                                |   1 +
 .../artifacts/post-apply.patch                     | 450 ++++++++-------------
 2 files changed, 176 insertions(+), 275 deletions(-)
```

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/auto-read-decisions-hooks/artifacts/post-apply.patch
```

*Patch was captured as a committed diff from `HEAD~1` to `HEAD`.*
