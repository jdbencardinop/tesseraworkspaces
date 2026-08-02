# Implementation Record: checkout-workspace-lifecycle

**Recorded**: 2026-08-02T18:10:41Z
**Files changed**: 18
**Patch size**: 95981 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/FEATURES.md                             |   1 +
 assets/skills/claude/tesseraworkspaces/SKILL.md |  16 ++
 assets/skills/copilot/tws.prompt.md             |  14 ++
 internal/cli/add.go                             | 152 ++++++++++----
 internal/cli/archive.go                         | 112 ++++++++---
 internal/cli/close.go                           |  21 +-
 internal/cli/delete.go                          | 187 +++++++++++++++---
 internal/cli/export.go                          |  68 ++++---
 internal/cli/hooks.go                           |  24 ++-
 internal/cli/importcmd.go                       | 197 ++++++++++++++-----
 internal/cli/init.go                            | 159 ++++++++++++---
 internal/cli/new.go                             | 110 ++++++++++-
 internal/cli/open.go                            |  34 ++--
 internal/cli/rename.go                          | 250 +++++++++++++++++-------
 internal/cli/sync.go                            |   9 +
 internal/config.go                              |  10 +
 internal/stack.go                               |   3 +-
 internal/workspace.go                           |  52 +++++
 18 files changed, 1121 insertions(+), 298 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `f464e48b62ecfb94a342e160375d21a4d746b07d`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/checkout-workspace-lifecycle/artifacts/post-apply.patch
```

