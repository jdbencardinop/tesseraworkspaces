# Implementation Record: scoped-push-selection

**Recorded**: 2026-10-30T13:37:55Z
**Files changed**: 42
**Patch size**: 347790 bytes
**Capture mode**: working-tree-all
**Pathspecs**: CHANGELOG.md,README.md,assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md,assets/skills/claude/tesseraworkspaces/SKILL.md,assets/skills/copilot/tws.prompt.md,docs/cheatsheet.md,docs/configuration.md,docs/engineering-workflow.md,docs/roadmap.md,internal/checkout_metadata_move.go,internal/checkout_sync.go,internal/cli/delete.go,internal/cli/feature_rename.go,internal/cli/feature_rename_review_test.go,internal/cli/managed_template.go,internal/cli/new.go,internal/cli/push.go,internal/cli/push_selection.go,internal/cli/push_selection_docs_test.go,internal/cli/push_selection_lifecycle_test.go,internal/cli/push_selection_roots_test.go,internal/cli/push_selection_safety_test.go,internal/cli/push_selection_test.go,internal/cli/rename.go,internal/cli/reparent_observability.go,internal/cli/sync.go,internal/cli/sync_modes.go,internal/cli/sync_plan_docs_test.go,internal/cli/testdata/rebase_plan/sync_help.txt,internal/feature_rename.go,internal/migrate.go,internal/reparent_remote.go,internal/reparent_state.go,internal/scoped_push.go,internal/scoped_push_git.go,internal/scoped_push_guard.go,internal/scoped_push_lifecycle.go,internal/scoped_push_state.go,internal/session.go,internal/session_test.go,internal/sync_run_state.go,internal/template_fs_unix.go

## Change Summary

```
 CHANGELOG.md                                       |  37 ++++
 README.md                                          | 105 +++++++++-
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |  61 +++++-
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  57 +++++-
 assets/skills/copilot/tws.prompt.md                |  61 +++++-
 docs/cheatsheet.md                                 |  62 +++++-
 docs/configuration.md                              | 105 ++++++++++
 docs/engineering-workflow.md                       |  10 +-
 docs/roadmap.md                                    |   4 +-
 internal/checkout_sync.go                          |  96 ++++++++-
 internal/cli/delete.go                             |  33 ++-
 internal/cli/feature_rename.go                     |  24 ++-
 internal/cli/feature_rename_review_test.go         |   3 +
 internal/cli/managed_template.go                   |  30 +--
 internal/cli/new.go                                |  17 +-
 internal/cli/push.go                               |  84 +++++++-
 internal/cli/rename.go                             |  39 +++-
 internal/cli/reparent_observability.go             |   5 +
 internal/cli/sync.go                               |   2 +-
 internal/cli/sync_modes.go                         |  44 +++-
 internal/cli/sync_plan_docs_test.go                |  10 +-
 internal/cli/testdata/rebase_plan/sync_help.txt    |   2 +-
 internal/feature_rename.go                         |   3 +
 internal/migrate.go                                | 109 ++++++++--
 internal/reparent_remote.go                        | 106 ++++++++--
 internal/reparent_state.go                         |  12 ++
 internal/session.go                                |  15 ++
 internal/session_test.go                           | 225 +++++++++++++++++++++
 internal/sync_run_state.go                         |  24 ++-
 internal/template_fs_unix.go                       |   9 +
 30 files changed, 1281 insertions(+), 113 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: CHANGELOG.md, README.md, assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md, assets/skills/claude/tesseraworkspaces/SKILL.md, assets/skills/copilot/tws.prompt.md, docs/cheatsheet.md, docs/configuration.md, docs/engineering-workflow.md, docs/roadmap.md, internal/checkout_metadata_move.go, internal/checkout_sync.go, internal/cli/delete.go, internal/cli/feature_rename.go, internal/cli/feature_rename_review_test.go, internal/cli/managed_template.go, internal/cli/new.go, internal/cli/push.go, internal/cli/push_selection.go, internal/cli/push_selection_docs_test.go, internal/cli/push_selection_lifecycle_test.go, internal/cli/push_selection_roots_test.go, internal/cli/push_selection_safety_test.go, internal/cli/push_selection_test.go, internal/cli/rename.go, internal/cli/reparent_observability.go, internal/cli/sync.go, internal/cli/sync_modes.go, internal/cli/sync_plan_docs_test.go, internal/cli/testdata/rebase_plan/sync_help.txt, internal/feature_rename.go, internal/migrate.go, internal/reparent_remote.go, internal/reparent_state.go, internal/scoped_push.go, internal/scoped_push_git.go, internal/scoped_push_guard.go, internal/scoped_push_lifecycle.go, internal/scoped_push_state.go, internal/session.go, internal/session_test.go, internal/sync_run_state.go, internal/template_fs_unix.go
- **claim_ids**: (none)
- **base_commit**: `38a7baf6981c305f2f3c1cbe23b1db3406ee90e0`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/scoped-push-selection/artifacts/post-apply.patch
```

