# Implementation Record: named-feature-templates

**Recorded**: 2026-10-10T03:46:41Z
**Files changed**: 41
**Patch size**: 648064 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/FEATURES.md                                |   2 +-
 .../features/named-feature-templates/status.json   |  25 +-
 CHANGELOG.md                                       |  39 ++
 README.md                                          | 120 +++-
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |  47 ++
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  48 +-
 assets/skills/copilot/tws.prompt.md                |  48 +-
 docs/cheatsheet.md                                 |  72 ++-
 docs/configuration.md                              |  52 +-
 docs/engineering-workflow.md                       |  31 +-
 docs/roadmap.md                                    |  45 +-
 go.mod                                             |   7 +-
 go.sum                                             |   2 +
 internal/cli/add.go                                | 155 ++++-
 internal/cli/archive.go                            | 137 +++-
 internal/cli/config.go                             | 103 ++-
 internal/cli/direct_open.go                        |  32 +-
 internal/cli/export.go                             |  45 +-
 internal/cli/inject.go                             |  47 +-
 internal/cli/new.go                                | 156 ++++-
 internal/cli/open.go                               | 234 +++++--
 internal/cli/open_checkout.go                      |  46 +-
 internal/cli/rename.go                             | 693 +++++++++++++++++++--
 internal/cli/sync_plan_docs_test.go                |  18 +-
 internal/cli/template.go                           | 521 +++++++++++++++-
 internal/config.go                                 |  82 ++-
 internal/inject.go                                 |  20 +
 internal/paths.go                                  |   7 +
 internal/session.go                                |   3 +
 internal/workspace.go                              |  22 +
 30 files changed, 2581 insertions(+), 278 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `947032d1e7e1014dbc6a4f0f5e7de4611db02b03`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/named-feature-templates/artifacts/post-apply.patch
```

