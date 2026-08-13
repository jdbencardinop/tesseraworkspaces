# Implementation Record: stack-status

**Recorded**: 2026-08-13T10:31:07Z
**Files changed**: 114
**Patch size**: 396740 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/FEATURES.md                                |   2 +-
 .tpatch/features/stack-status/spec.md              |  22 ++-
 .tpatch/features/stack-status/status.json          |  14 +-
 CHANGELOG.md                                       |  47 ++++++
 README.md                                          |   1 +
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |  12 +-
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  30 ++++
 assets/skills/copilot/tws.prompt.md                |  14 +-
 docs/cheatsheet.md                                 |  10 ++
 docs/configuration.md                              |   6 +
 docs/engineering-workflow.md                       |   8 +-
 docs/roadmap.md                                    |  19 ++-
 internal/agent_status.go                           | 162 ++++++++++++++++--
 internal/agent_status_test.go                      | 182 +++++++++++++++++++++
 internal/checkout_health.go                        |  45 ++---
 internal/cli/stack.go                              |  19 ++-
 internal/stack_ancestry_test.go                    |  11 +-
 17 files changed, 519 insertions(+), 85 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `2ef1428c6a8bfad2eb43c1b7d45dae91e65e437c`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/stack-status/artifacts/post-apply.patch
```

