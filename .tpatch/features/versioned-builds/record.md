# Implementation Record: versioned-builds

**Recorded**: 2026-05-02T04:54:06Z
**Files changed**: 3
**Patch size**: 2546 bytes

## Change Summary

```
 .tpatch/FEATURES.md                           | 2 +-
 .tpatch/features/versioned-builds/status.json | 7 ++++---
 2 files changed, 5 insertions(+), 4 deletions(-)
```

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/versioned-builds/artifacts/post-apply.patch
```

*Patch was captured as a committed diff from `HEAD~1` to `HEAD`.*
