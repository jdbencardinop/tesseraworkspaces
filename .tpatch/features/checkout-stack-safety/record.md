# Implementation Record: checkout-stack-safety

**Recorded**: 2026-08-03T05:21:18Z
**Files changed**: 7
**Patch size**: 73643 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/FEATURES.md                             |  1 +
 assets/skills/claude/tesseraworkspaces/SKILL.md | 11 ++++++++++-
 assets/skills/copilot/tws.prompt.md             |  4 +++-
 internal/cli/checkout_lifecycle_test.go         |  8 +++-----
 internal/cli/sync.go                            |  6 ++++--
 5 files changed, 21 insertions(+), 9 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `16e862b0f07a746e89283ec19186a991326bc9d2`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/checkout-stack-safety/artifacts/post-apply.patch
```

