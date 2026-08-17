# Implementation Record: sync-modes

**Recorded**: 2026-08-17T23:47:54Z
**Files changed**: 166
**Patch size**: 494344 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/FEATURES.md                                |   2 +-
 .tpatch/features/sync-modes/spec.md                |  66 +--
 .tpatch/features/sync-modes/status.json            |  14 +-
 CHANGELOG.md                                       | 100 +++++
 README.md                                          |  33 +-
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |  14 +
 assets/skills/claude/tesseraworkspaces/SKILL.md    |   6 +-
 assets/skills/copilot/tws.prompt.md                |   6 +-
 docs/cheatsheet.md                                 |  30 ++
 docs/engineering-workflow.md                       |  10 +-
 docs/roadmap.md                                    |  21 +-
 internal/agent_status.go                           | 142 +++++++
 internal/checkout_sync.go                          | 254 ++++++++++-
 internal/cli/checkout_lifecycle_test.go            |   2 +
 internal/cli/checkout_sync.go                      |  38 +-
 internal/cli/checkout_sync_test.go                 |   3 +-
 internal/cli/importcmd.go                          |   7 +-
 internal/cli/new.go                                |   5 +-
 internal/cli/push.go                               | 126 +++++-
 internal/cli/sync.go                               | 472 +++++++++++++++++++--
 internal/cli/sync_continue_integration_test.go     |   4 +-
 internal/cli/sync_helpers.go                       | 244 +++++++++--
 internal/syncstate.go                              |   2 +-
 23 files changed, 1452 insertions(+), 149 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `cf8a1d9fe3419430099c06cb3ecdf9bf21e34b65`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/sync-modes/artifacts/post-apply.patch
```

