# Implementation Record: sync-transactional-abort

**Recorded**: 2026-09-29T04:59:33Z
**Files changed**: 47
**Patch size**: 496200 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .../features/sync-transactional-abort/status.json  |  14 +-
 CHANGELOG.md                                       |  23 +
 README.md                                          |  55 +-
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |  38 +-
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  29 +-
 assets/skills/copilot/tws.prompt.md                |  26 +-
 docs/cheatsheet.md                                 |  27 +-
 internal/agent_status.go                           |  74 ++-
 internal/checkout_health.go                        | 187 ++++++-
 internal/checkout_sync.go                          | 565 +++++++++++++++----
 internal/checkout_sync_plan_test.go                | 184 ++++---
 internal/cli/checkout_sync.go                      |  38 +-
 internal/cli/checkout_sync_modes_test.go           | 112 ++--
 internal/cli/doctor.go                             |  18 +-
 internal/cli/push.go                               |  64 ++-
 internal/cli/sync.go                               | 600 +++++++++++++++++----
 internal/cli/sync_downgrade_test.go                |  64 +--
 internal/cli/sync_golden_test.go                   | 182 +++++--
 internal/cli/sync_guarded_state_test.go            |  85 ++-
 internal/cli/sync_helpers.go                       | 213 +++++++-
 internal/cli/sync_modes.go                         | 268 ++++++++-
 internal/cli/sync_modes_test.go                    |  16 +-
 internal/cli/sync_plan_guard.go                    |   3 +-
 internal/cli/sync_plan_guard_test.go               |  35 +-
 internal/cli/sync_push_resume_test.go              |  29 +-
 internal/cli/sync_recovery_test.go                 |   4 +-
 internal/cli/sync_scoped_test.go                   |  10 +-
 internal/cli/sync_state_matrix_test.go             | 135 ++++-
 internal/cli/sync_validation_test.go               | 125 ++++-
 internal/cli/testdata/rebase_plan/sync_help.txt    |  25 +-
 .../sync_noflag/external-fallback/argv.log         |   6 -
 .../sync_noflag/external-fallback/stderr.txt       |   3 +-
 .../sync_noflag/external-fallback/stdout.txt       |   7 +-
 internal/exec.go                                   |   5 +
 internal/health.go                                 |  45 ++
 internal/sync_run_state.go                         |  76 ++-
 internal/syncstate.go                              |  53 ++
 37 files changed, 2810 insertions(+), 633 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `a04a830d7c3e8df18f1b6e010f457136e23995e7`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/sync-transactional-abort/artifacts/post-apply.patch
```

