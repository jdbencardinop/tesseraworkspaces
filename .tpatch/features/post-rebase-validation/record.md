# Implementation Record: post-rebase-validation

**Recorded**: 2026-05-26T22:11:40Z
**Files changed**: 7
**Patch size**: 11974 bytes
**Capture mode**: committed range
**Base commit**: HEAD~1
**Upper bound**: HEAD

## Collision Override

**Reason**: bundled with quiet-fetch in same commit

This patch is byte-identical to the canonical post-apply.patch of:

- `clean-git-output` — sha256=86b879708429... bytes=11974 files=7
- `quiet-fetch-output` — sha256=86b879708429... bytes=11974 files=7

## Change Summary

```
 .tpatch/FEATURES.md                                 |  8 ++++----
 .tpatch/features/clean-git-output/status.json       | 12 ++++++++----
 .tpatch/features/post-rebase-validation/status.json |  7 ++++---
 .tpatch/features/push-branches/status.json          |  7 ++++---
 .tpatch/features/quiet-fetch-output/status.json     | 12 ++++++++----
 5 files changed, 28 insertions(+), 18 deletions(-)
```

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/post-rebase-validation/artifacts/post-apply.patch
```

*Patch was captured as a committed diff from `HEAD~1` to `HEAD`.*
