# Implementation Record: fix-space-test-git-maintenance-race

**Recorded**: 2026-08-11T15:05:03Z
**Files changed**: 1
**Patch size**: 13302 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 internal/cli/space_test.go | 315 ++++++++++++++++++++++++++++++++++++++++++++-
 1 file changed, 313 insertions(+), 2 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `218ddecafd0f9e0860d3f19e5b1f4fbfc708b503`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-space-test-git-maintenance-race/artifacts/post-apply.patch
```

