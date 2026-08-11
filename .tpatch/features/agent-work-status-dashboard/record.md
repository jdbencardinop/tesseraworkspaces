# Implementation Record: agent-work-status-dashboard

**Recorded**: 2026-08-11T13:57:20Z
**Files changed**: 31
**Patch size**: 335676 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .../agent-work-status-dashboard/status.json        |  97 +++++++++++++-
 CHANGELOG.md                                       |  78 +++++++++++
 README.md                                          |   1 +
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |  15 +++
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  53 ++++++++
 assets/skills/copilot/tws.prompt.md                |  17 ++-
 docs/cheatsheet.md                                 |  15 +++
 docs/engineering-workflow.md                       |  10 +-
 docs/roadmap.md                                    |  32 ++++-
 internal/checkout_health.go                        |  59 ++++++++-
 internal/cli/add.go                                |  10 +-
 internal/cli/archive.go                            |   8 ++
 internal/cli/close.go                              | 145 ++++++++++++++++++---
 internal/cli/delete.go                             |  17 +++
 internal/cli/open.go                               |  72 +++-------
 internal/cli/rename.go                             |  21 +++
 internal/cli/root.go                               |   1 +
 internal/cli/space_guard_test.go                   |  60 +++++++--
 internal/session.go                                |  22 +++-
 internal/spaces.go                                 |  25 +++-
 internal/spaces_test.go                            |  85 ++++++++++++
 21 files changed, 735 insertions(+), 108 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `2e5ec183ef56bc5d8475a6931abb1afbc86932bb`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/agent-work-status-dashboard/artifacts/post-apply.patch
```

