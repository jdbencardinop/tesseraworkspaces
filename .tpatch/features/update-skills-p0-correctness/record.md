# Implementation Record: update-skills-p0-correctness

**Recorded**: 2026-07-21T14:34:49Z
**Files changed**: 2
**Patch size**: 3945 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/FEATURES.md                             |  1 +
 assets/skills/claude/tesseraworkspaces/SKILL.md | 14 +++++++++-----
 assets/skills/copilot/tws.prompt.md             |  9 +++++----
 3 files changed, 15 insertions(+), 9 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `1defd28fd8dfe7937e31e432517117c35bf60655`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/update-skills-p0-correctness/artifacts/post-apply.patch
```

