# Implementation Record: fix-status-tmux-test-portability

**Recorded**: 2026-08-11T14:23:44Z
**Files changed**: 1
**Patch size**: 5128 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 internal/cli/status_test.go | 94 +++++++++++++++++++++++++++++++++++++++++++++
 1 file changed, 94 insertions(+)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `a8696d95116ab75822c3aa10acdff4969f2d26c1`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-status-tmux-test-portability/artifacts/post-apply.patch
```

