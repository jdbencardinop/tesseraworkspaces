# Implementation Record: fix-external-feature-dir-resolution

**Recorded**: 2026-08-05T23:18:11Z
**Files changed**: 3
**Patch size**: 9962 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 internal/workspace.go      | 86 +++++++++++++++++++++++++++++++++++++++++++---
 internal/workspace_test.go | 63 +++++++++++++++++++++++++++++++++
 2 files changed, 145 insertions(+), 4 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `a043650b83c5c2186002a3c010905505343f0e46`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-external-feature-dir-resolution/artifacts/post-apply.patch
```

