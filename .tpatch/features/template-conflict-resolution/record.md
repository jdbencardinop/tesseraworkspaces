# Implementation Record: template-conflict-resolution

**Recorded**: 2026-10-11T16:22:36Z
**Files changed**: 28
**Patch size**: 231530 bytes
**Capture mode**: working-tree-all
**Pathspecs**: CHANGELOG.md,README.md,assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md,assets/skills/claude/tesseraworkspaces/SKILL.md,assets/skills/copilot/tws.prompt.md,docs/cheatsheet.md,docs/configuration.md,docs/engineering-workflow.md,docs/roadmap.md,internal/cli/add.go,internal/cli/managed_template.go,internal/cli/managed_template_conflict.go,internal/cli/managed_template_editor_unix.go,internal/cli/named_template_conflict_docs_test.go,internal/cli/named_template_conflict_lifecycle_test.go,internal/cli/named_template_conflict_phase_test.go,internal/cli/named_template_conflict_revision_test.go,internal/cli/named_template_conflict_test.go,internal/cli/named_template_final_regression_test.go,internal/cli/named_template_test.go,internal/cli/sync_plan_docs_test.go,internal/cli/template.go,internal/template_conflict.go,internal/template_conflict_fs_unix.go,internal/template_conflict_revision_test.go,internal/template_conflict_test.go,internal/template_managed.go,internal/template_preserve.go

## Change Summary

```
 CHANGELOG.md                                       |  37 +++++
 README.md                                          |  80 +++++++++-
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |  48 +++++-
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  45 +++++-
 assets/skills/copilot/tws.prompt.md                |  55 ++++++-
 docs/cheatsheet.md                                 |  50 +++++-
 docs/configuration.md                              |  62 ++++++++
 docs/engineering-workflow.md                       |  59 +++++--
 docs/roadmap.md                                    |  82 +++++++---
 internal/cli/add.go                                |  25 ++-
 internal/cli/managed_template.go                   |  41 +++--
 .../cli/named_template_final_regression_test.go    |   4 +-
 internal/cli/named_template_test.go                |   9 +-
 internal/cli/sync_plan_docs_test.go                |  34 +++-
 internal/cli/template.go                           |  37 ++++-
 internal/template_managed.go                       | 176 +++++++++++++++++++--
 internal/template_preserve.go                      |  79 +++++++--
 17 files changed, 801 insertions(+), 122 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: CHANGELOG.md, README.md, assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md, assets/skills/claude/tesseraworkspaces/SKILL.md, assets/skills/copilot/tws.prompt.md, docs/cheatsheet.md, docs/configuration.md, docs/engineering-workflow.md, docs/roadmap.md, internal/cli/add.go, internal/cli/managed_template.go, internal/cli/managed_template_conflict.go, internal/cli/managed_template_editor_unix.go, internal/cli/named_template_conflict_docs_test.go, internal/cli/named_template_conflict_lifecycle_test.go, internal/cli/named_template_conflict_phase_test.go, internal/cli/named_template_conflict_revision_test.go, internal/cli/named_template_conflict_test.go, internal/cli/named_template_final_regression_test.go, internal/cli/named_template_test.go, internal/cli/sync_plan_docs_test.go, internal/cli/template.go, internal/template_conflict.go, internal/template_conflict_fs_unix.go, internal/template_conflict_revision_test.go, internal/template_conflict_test.go, internal/template_managed.go, internal/template_preserve.go
- **claim_ids**: (none)
- **base_commit**: `9c634f2a0e8c9e89e620060ec24a6a1df736f446`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/template-conflict-resolution/artifacts/post-apply.patch
```

