# Implementation Record: fix-managed-archive-inode-fixture

**Recorded**: 2026-10-31T12:47:44Z
**Files changed**: 1
**Patch size**: 1427 bytes
**Capture mode**: working-tree-all
**Pathspecs**: internal/cli/named_template_final_regression_test.go

## Change Summary

```
 internal/cli/named_template_final_regression_test.go | 18 ++++++++++++++++++
 1 file changed, 18 insertions(+)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: internal/cli/named_template_final_regression_test.go
- **claim_ids**: (none)
- **base_commit**: `caee4aa4aaec7154d35c3e5ffe29faf6de3a146a`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-managed-archive-inode-fixture/artifacts/post-apply.patch
```

