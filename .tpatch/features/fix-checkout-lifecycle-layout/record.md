# Implementation Record: fix-checkout-lifecycle-layout

**Recorded**: 2026-08-05T05:18:20Z
**Files changed**: 13
**Patch size**: 48887 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 assets/skills/claude/tesseraworkspaces/SKILL.md | 12 ++++
 internal/cli/init.go                            | 92 ++++++-------------------
 internal/cli/list.go                            | 53 +++++++-------
 internal/cli/root.go                            |  3 +
 internal/workspace.go                           |  6 +-
 5 files changed, 67 insertions(+), 99 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `70a9bd845425983f1cce80bd2483c2b31aec80b7`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-checkout-lifecycle-layout/artifacts/post-apply.patch
```

