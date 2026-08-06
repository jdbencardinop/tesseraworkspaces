# Implementation Record: checkout-doctor-observability

**Recorded**: 2026-08-06T00:55:10Z
**Files changed**: 6
**Patch size**: 71456 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 assets/skills/claude/tesseraworkspaces/SKILL.md | 21 ++++++++++++++-
 internal/cli/doctor.go                          | 35 +++++++++++++++++++++++--
 internal/cli/list.go                            | 14 ++++++++++
 3 files changed, 67 insertions(+), 3 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `ae7c93bfb95eecd56bdbaa77db8f4f03163b449c`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/checkout-doctor-observability/artifacts/post-apply.patch
```

