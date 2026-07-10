# Implementation Record: open-feature-dir

**Recorded**: 2026-07-10T05:56:40Z
**Files changed**: 1
**Patch size**: 4082 bytes
**Capture mode**: working tree

## Change Summary

```
 .tpatch/FEATURES.md                           |  2 +-
 .tpatch/features/open-feature-dir/status.json |  7 ++-
 internal/cli/open.go                          | 85 ++++++++++++++++++++++++++-
 3 files changed, 88 insertions(+), 6 deletions(-)
```

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/open-feature-dir/artifacts/post-apply.patch
```

