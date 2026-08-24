# Implementation Record: rebase-plan-guard

**Recorded**: 2026-08-24T21:21:58Z
**Files changed**: 48
**Patch size**: 1841264 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/FEATURES.md                                |    2 +-
 .tpatch/features/rebase-plan-guard/status.json     |   14 +-
 CHANGELOG.md                                       |   83 +
 README.md                                          |   39 +
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |   28 +
 assets/skills/claude/tesseraworkspaces/SKILL.md    |    2 +
 assets/skills/copilot/tws.prompt.md                |    4 +
 docs/cheatsheet.md                                 |   29 +
 docs/engineering-workflow.md                       |   16 +-
 docs/roadmap.md                                    |   20 +-
 internal/checkout_sync.go                          |  494 +++++-
 internal/cli/checkout_sync.go                      |   62 +-
 internal/cli/checkout_sync_modes_test.go           | 1627 ++++++++++++++++++++
 internal/cli/checkout_sync_test.go                 |    2 +-
 internal/cli/sync.go                               | 1174 ++++++++++++--
 internal/cli/sync_downgrade_test.go                |  178 ++-
 internal/cli/sync_helpers.go                       |  109 +-
 internal/cli/sync_modes.go                         |  593 ++++++-
 internal/cli/sync_modes_test.go                    |    2 +-
 internal/cli/sync_scoped_test.go                   |   99 ++
 internal/exec.go                                   |   24 +
 internal/exec_clean_test.go                        |  122 ++
 internal/sync_run_state.go                         |  251 ++-
 internal/sync_selection.go                         |   13 +-
 internal/syncstate.go                              |  378 ++++-
 25 files changed, 5075 insertions(+), 290 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `e0bd2b1421d8acfaf6e9ac506f8c0a3b3f742995`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/rebase-plan-guard/artifacts/post-apply.patch
```

