# Implementation Record: safe-reparent-restack

**Recorded**: 2026-09-25T23:09:00Z
**Files changed**: 90
**Patch size**: 1829314 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .github/workflows/ci.yml                           |    2 +
 .../artifacts/apply-recipe.json                    |  253 +++--
 .../features/safe-reparent-restack/exploration.md  |  559 +++++++++--
 .tpatch/features/safe-reparent-restack/spec.md     | 1054 ++++++++++++++++----
 .tpatch/features/safe-reparent-restack/status.json |   14 +-
 CHANGELOG.md                                       |   91 +-
 README.md                                          |   91 +-
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |   90 ++
 assets/skills/claude/tesseraworkspaces/SKILL.md    |   50 +
 assets/skills/copilot/tws.prompt.md                |   16 +
 docs/cheatsheet.md                                 |   77 ++
 docs/engineering-workflow.md                       |   35 +-
 docs/roadmap.md                                    |   42 +-
 internal/agent_status.go                           |   85 +-
 internal/checkout_health.go                        |   34 +-
 internal/checkout_health_test.go                   |    1 +
 internal/checkout_sync.go                          |  580 ++++++++++-
 internal/checkout_sync_plan_test.go                |  368 +++++++
 internal/cli/add.go                                |    7 +-
 internal/cli/checkout_doctor_test.go               |    4 +-
 internal/cli/checkout_sync.go                      |    5 +
 internal/cli/checkout_sync_modes_test.go           |  136 ++-
 internal/cli/checkout_sync_test.go                 |    1 +
 internal/cli/direct_open.go                        |   19 +
 internal/cli/direct_open_test.go                   |   29 +
 internal/cli/doctor.go                             |   40 +-
 internal/cli/importcmd.go                          |   19 +-
 internal/cli/list.go                               |    9 +-
 internal/cli/new_integration_test.go               |    2 +
 internal/cli/open.go                               |  209 ++--
 internal/cli/push.go                               |  217 +++-
 internal/cli/stack.go                              |   12 +-
 internal/cli/stack_status.go                       |   14 +-
 internal/cli/status.go                             |   12 +
 internal/cli/sync.go                               |  308 +++---
 internal/cli/sync_downgrade_test.go                |  594 ++++++++++-
 internal/cli/sync_modes.go                         |   75 +-
 internal/cli/sync_plan_docs_test.go                |  236 ++++-
 internal/cli/sync_plan_guard.go                    |   27 +-
 internal/cli/sync_scoped_test.go                   |   28 +-
 internal/config.go                                 |   26 +-
 internal/git_capability.go                         |   43 +
 internal/git_capability_test.go                    |   73 ++
 internal/rebase_planner.go                         |   27 +-
 internal/session.go                                |  494 ++++++++-
 internal/session_test.go                           |  300 +++++-
 internal/stack.go                                  |  303 ++++++
 internal/stack_ancestry.go                         |   32 +
 internal/stack_status.go                           |   30 +-
 internal/stack_status_test.go                      |   15 +-
 internal/stack_test.go                             |  172 ++++
 internal/sync_run_state.go                         |   23 +
 internal/sync_run_state_test.go                    |   10 +-
 internal/syncstate.go                              |   12 +
 internal/workspace.go                              |  113 ++-
 55 files changed, 6342 insertions(+), 776 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `aef6239765a9314369b641b473d8b8a72b3f3749`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/safe-reparent-restack/artifacts/post-apply.patch
```

