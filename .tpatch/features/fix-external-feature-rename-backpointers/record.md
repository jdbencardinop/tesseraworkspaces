# Implementation Record: fix-external-feature-rename-backpointers

**Recorded**: 2026-10-30T03:41:17Z
**Files changed**: 48
**Patch size**: 341151 bytes
**Capture mode**: working-tree-all
**Pathspecs**: CHANGELOG.md,README.md,assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md,assets/skills/claude/tesseraworkspaces/SKILL.md,assets/skills/copilot/tws.prompt.md,docs/cheatsheet.md,docs/configuration.md,docs/engineering-workflow.md,docs/roadmap.md,internal/cli/add.go,internal/cli/archive.go,internal/cli/close.go,internal/cli/decide.go,internal/cli/decisions.go,internal/cli/delete.go,internal/cli/direct_open.go,internal/cli/export.go,internal/cli/feature_rename.go,internal/cli/feature_rename_binding_test.go,internal/cli/feature_rename_review_test.go,internal/cli/feature_rename_route_test.go,internal/cli/feature_rename_test.go,internal/cli/hooks.go,internal/cli/importcmd.go,internal/cli/init.go,internal/cli/inject.go,internal/cli/managed_template.go,internal/cli/named_template_conflict_docs_test.go,internal/cli/new.go,internal/cli/open.go,internal/cli/push.go,internal/cli/rename.go,internal/cli/stack_reparent.go,internal/cli/sync.go,internal/cli/sync_modes.go,internal/cli/sync_plan_docs_test.go,internal/cli/template.go,internal/feature_rename.go,internal/feature_rename_admission.go,internal/feature_rename_binding_test.go,internal/feature_rename_fs.go,internal/feature_rename_plan.go,internal/feature_rename_review_test.go,internal/feature_rename_roots.go,internal/feature_rename_safety_test.go,internal/feature_rename_state.go,internal/feature_rename_test.go,internal/spaces.go

## Change Summary

```
 CHANGELOG.md                                       | 33 +++++++-
 README.md                                          | 91 ++++++++++++++++++++--
 .../claude/tesseraworkspaces-orchestrator/SKILL.md | 36 ++++++++-
 assets/skills/claude/tesseraworkspaces/SKILL.md    | 35 ++++++++-
 assets/skills/copilot/tws.prompt.md                | 36 ++++++++-
 docs/cheatsheet.md                                 | 43 ++++++++--
 docs/configuration.md                              | 58 +++++++++++++-
 docs/engineering-workflow.md                       | 38 +++++++--
 docs/roadmap.md                                    | 42 ++++++++--
 internal/cli/add.go                                | 14 +++-
 internal/cli/archive.go                            |  9 ++-
 internal/cli/close.go                              |  4 +-
 internal/cli/decide.go                             | 14 +++-
 internal/cli/decisions.go                          | 11 ++-
 internal/cli/delete.go                             | 13 +++-
 internal/cli/direct_open.go                        | 10 ++-
 internal/cli/export.go                             |  8 +-
 internal/cli/hooks.go                              | 54 ++++++++-----
 internal/cli/importcmd.go                          |  8 +-
 internal/cli/init.go                               |  2 +-
 internal/cli/inject.go                             |  9 ++-
 internal/cli/managed_template.go                   |  9 ++-
 internal/cli/named_template_conflict_docs_test.go  |  3 +-
 internal/cli/new.go                                | 11 ++-
 internal/cli/open.go                               | 24 +++++-
 internal/cli/push.go                               | 10 +++
 internal/cli/rename.go                             | 62 +++++++++++++--
 internal/cli/stack_reparent.go                     | 15 +++-
 internal/cli/sync.go                               | 12 ++-
 internal/cli/sync_modes.go                         | 16 +++-
 internal/cli/sync_plan_docs_test.go                | 16 +++-
 internal/cli/template.go                           | 18 ++++-
 internal/spaces.go                                 | 23 ++++++
 33 files changed, 679 insertions(+), 108 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: CHANGELOG.md, README.md, assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md, assets/skills/claude/tesseraworkspaces/SKILL.md, assets/skills/copilot/tws.prompt.md, docs/cheatsheet.md, docs/configuration.md, docs/engineering-workflow.md, docs/roadmap.md, internal/cli/add.go, internal/cli/archive.go, internal/cli/close.go, internal/cli/decide.go, internal/cli/decisions.go, internal/cli/delete.go, internal/cli/direct_open.go, internal/cli/export.go, internal/cli/feature_rename.go, internal/cli/feature_rename_binding_test.go, internal/cli/feature_rename_review_test.go, internal/cli/feature_rename_route_test.go, internal/cli/feature_rename_test.go, internal/cli/hooks.go, internal/cli/importcmd.go, internal/cli/init.go, internal/cli/inject.go, internal/cli/managed_template.go, internal/cli/named_template_conflict_docs_test.go, internal/cli/new.go, internal/cli/open.go, internal/cli/push.go, internal/cli/rename.go, internal/cli/stack_reparent.go, internal/cli/sync.go, internal/cli/sync_modes.go, internal/cli/sync_plan_docs_test.go, internal/cli/template.go, internal/feature_rename.go, internal/feature_rename_admission.go, internal/feature_rename_binding_test.go, internal/feature_rename_fs.go, internal/feature_rename_plan.go, internal/feature_rename_review_test.go, internal/feature_rename_roots.go, internal/feature_rename_safety_test.go, internal/feature_rename_state.go, internal/feature_rename_test.go, internal/spaces.go
- **claim_ids**: (none)
- **base_commit**: `6819a4e7a641396bce206022110b3b63351ac73d`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-external-feature-rename-backpointers/artifacts/post-apply.patch
```

