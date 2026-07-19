# Implementation Record: fix-new-base-checkout

**Recorded**: 2026-07-19T16:45:30Z
**Files changed**: 4
**Patch size**: 20849 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 internal/cli/add.go |   8 +--
 internal/cli/new.go | 193 ++++++++++++++++++++++++++++++++++++----------------
 internal/exec.go    |  86 +++++++++++++++++------
 3 files changed, 204 insertions(+), 83 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `841c895f8671ef5769f9d0645ac6da3c6d95cac3`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-new-base-checkout/artifacts/post-apply.patch
```

