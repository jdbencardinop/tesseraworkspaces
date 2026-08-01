# Implementation Record: workspace-mode-foundation

**Recorded**: 2026-08-01T13:13:23Z
**Files changed**: 4
**Patch size**: 21446 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/FEATURES.md |  1 +
 internal/config.go  | 23 +++++++++++++----------
 internal/paths.go   | 16 ++++++----------
 3 files changed, 20 insertions(+), 20 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `477db4e98ddbfca416a6dc8c68a57461ef31c6bf`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/workspace-mode-foundation/artifacts/post-apply.patch
```

