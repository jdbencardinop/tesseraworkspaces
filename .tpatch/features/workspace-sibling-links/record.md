# Implementation Record: workspace-sibling-links

**Recorded**: 2026-08-11T03:30:15Z
**Files changed**: 40
**Patch size**: 304175 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .../features/workspace-sibling-links/status.json   |  21 +-
 CHANGELOG.md                                       |  47 ++++
 README.md                                          |  73 +++++
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |   8 +
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  34 +++
 assets/skills/copilot/tws.prompt.md                |  16 ++
 docs/engineering-workflow.md                       |   9 +-
 docs/roadmap.md                                    |  20 +-
 internal/checkout_health.go                        |   4 +
 internal/checkout_health_test.go                   |  54 ++++
 internal/cli/add.go                                |  10 +-
 internal/cli/archive.go                            |   8 +
 internal/cli/close.go                              |   5 +
 internal/cli/delete.go                             |  21 +-
 internal/cli/doctor.go                             |   5 +-
 internal/cli/export.go                             |   4 +
 internal/cli/hooks.go                              |  27 +-
 internal/cli/importcmd.go                          |  10 +-
 internal/cli/list.go                               |   2 +
 internal/cli/new.go                                |  11 +-
 internal/cli/open.go                               |  23 +-
 internal/cli/open_checkout.go                      |   7 +
 internal/cli/rename.go                             |  32 ++-
 internal/cli/root.go                               |   1 +
 internal/cli/sync.go                               |   6 +
 internal/cli/template.go                           |  26 +-
 internal/migrate.go                                |  64 ++++-
 internal/migrate_test.go                           | 304 +++++++++++++++++++++
 internal/paths.go                                  |  44 ++-
 internal/resolve.go                                |  72 ++++-
 internal/resolve_test.go                           |  46 ++++
 internal/session.go                                |   3 +
 32 files changed, 940 insertions(+), 77 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `ddc5aa03a36a8349d07833656cccedea9c986927`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/workspace-sibling-links/artifacts/post-apply.patch
```

