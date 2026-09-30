# Implementation Record: sync-cutoff-integrity

**Recorded**: 2026-09-30T20:41:09Z
**Files changed**: 84
**Patch size**: 415462 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/features/sync-cutoff-integrity/analysis.md |  47 +++
 .tpatch/features/sync-cutoff-integrity/status.json |  18 +-
 CHANGELOG.md                                       |  40 ++
 README.md                                          |  47 +++
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |  24 +-
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  24 +-
 assets/skills/copilot/tws.prompt.md                |  40 +-
 docs/cheatsheet.md                                 |  36 +-
 internal/checkout_health.go                        |  44 ++-
 internal/checkout_sync.go                          |  70 +++-
 internal/checkout_sync_plan_test.go                |  48 +--
 internal/cli/checkout_lifecycle_test.go            |  55 +++
 internal/cli/checkout_sync_modes_test.go           | 365 ++++++++++++++-----
 internal/cli/checkout_sync_test.go                 |  26 +-
 internal/cli/new.go                                |  66 +++-
 internal/cli/new_integration_test.go               |  58 ++-
 internal/cli/stack_status_test.go                  |   4 +
 internal/cli/sync.go                               | 250 ++++++++++---
 internal/cli/sync_continue_integration_test.go     |  24 +-
 internal/cli/sync_golden_test.go                   |  57 ++-
 internal/cli/sync_guarded_state_test.go            | 266 +++-----------
 internal/cli/sync_helpers.go                       | 405 +++++++++++++--------
 internal/cli/sync_modes.go                         |  20 +-
 internal/cli/sync_plan_guard.go                    |  59 +--
 internal/cli/sync_plan_guard_test.go               |  16 +-
 internal/cli/sync_plan_integration_test.go         | 244 ++++++++-----
 internal/cli/sync_recovery_test.go                 |  51 +--
 internal/cli/sync_state_matrix_test.go             |   6 +-
 internal/cli/sync_transaction_test.go              | 211 +++++++++++
 .../existing_commands/checkout-archived/doctor.txt |   3 +
 .../existing_commands/checkout-bare/doctor.txt     |   2 +
 .../existing_commands/checkout-clean/doctor.txt    |   2 +
 .../checkout-cross-repo/doctor.txt                 |   2 +
 .../existing_commands/checkout-detached/doctor.txt |   2 +
 .../existing_commands/checkout-dirty/doctor.txt    |   2 +
 .../checkout-duplicate-branch/doctor.txt           |   3 +
 .../existing_commands/checkout-locked/doctor.txt   |   2 +
 .../existing_commands/checkout-missing/doctor.txt  |   1 +
 .../existing_commands/checkout-prunable/doctor.txt |   2 +
 .../existing_commands/checkout-rebase/doctor.txt   |   5 +-
 .../checkout-repo-unavailable/doctor.txt           |   2 +
 .../existing_commands/external-archived/doctor.txt |   3 +
 .../existing_commands/external-bare/doctor.txt     |   2 +
 .../existing_commands/external-clean/doctor.txt    |   2 +
 .../external-cross-repo/doctor.txt                 |   2 +
 .../existing_commands/external-detached/doctor.txt |   2 +
 .../existing_commands/external-dirty/doctor.txt    |   2 +
 .../external-duplicate-branch/doctor.txt           |   3 +
 .../existing_commands/external-locked/doctor.txt   |   3 +
 .../existing_commands/external-missing/doctor.txt  |   2 +
 .../existing_commands/external-prunable/doctor.txt |   3 +
 .../existing_commands/external-rebase/doctor.txt   |   6 +-
 .../external-repo-unavailable/doctor.txt           |   2 +
 internal/cli/testdata/rebase_plan/sync_help.txt    |   9 +
 .../sync_noflag/checkout-conflict/stack.yaml       |   3 +
 .../sync_noflag/external-abort-empty/stack.yaml    |   3 +
 .../sync_noflag/external-conflict/stack.yaml       |   1 +
 .../sync_noflag/external-stale-edge/argv.log       |  21 --
 .../sync_noflag/external-stale-edge/stack.yaml     |  17 +-
 .../external-stale-edge/state-sync-state.yaml      |   8 +-
 .../sync_noflag/external-stale-edge/stderr.txt     |   2 +-
 .../sync_noflag/external-stale-edge/stdout.txt     |   5 -
 internal/health.go                                 |  51 ++-
 internal/rebase_plan.go                            |  16 +-
 internal/rebase_plan_build.go                      | 214 +++++++++--
 internal/rebase_plan_fingerprint.go                |  10 +
 internal/rebase_plan_fingerprint_test.go           |  15 +-
 internal/rebase_plan_guard.go                      |  15 +-
 internal/rebase_plan_render.go                     |  22 +-
 internal/rebase_plan_render_test.go                |   9 +-
 internal/rebase_planner.go                         | 113 +++---
 internal/rebase_planner_test.go                    | 119 +++---
 internal/stack.go                                  |  16 +-
 internal/stack_ancestry.go                         |  38 +-
 internal/stack_ancestry_test.go                    | 118 +++++-
 internal/stack_status.go                           |  44 ++-
 internal/stack_status_test.go                      |   3 +-
 internal/stack_test.go                             |  19 +
 internal/sync_selection.go                         |   3 +-
 internal/sync_transaction.go                       | 356 +++++++++++++++---
 internal/sync_transaction_holder.go                |  70 +++-
 internal/sync_transaction_validate.go              | 220 +++++++++++
 82 files changed, 3171 insertions(+), 1050 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `18135cbdc1cc593d02ef2d909d04b299f19c3838`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/sync-cutoff-integrity/artifacts/post-apply.patch
```

