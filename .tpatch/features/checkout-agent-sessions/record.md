# Implementation Record: checkout-agent-sessions

**Recorded**: 2026-08-05T11:05:13Z
**Files changed**: 9
**Patch size**: 44669 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 assets/skills/claude/tesseraworkspaces/SKILL.md | 12 ++++++++-
 assets/skills/copilot/tws.prompt.md             |  4 ++-
 internal/cli/close.go                           | 33 ++++++++++++++++++++++---
 internal/cli/open.go                            | 26 ++++++++++++++++++-
 4 files changed, 68 insertions(+), 7 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `f526f8fe0494c68b8be7f5dab8b2d32c9001dfe7`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/checkout-agent-sessions/artifacts/post-apply.patch
```

