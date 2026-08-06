# Implementation Record: workspace-registry

**Recorded**: 2026-08-06T04:45:02Z
**Files changed**: 15
**Patch size**: 116471 bytes
**Capture mode**: working-tree-all
**Pathspecs**: README.md,assets/skills/claude/tesseraworkspaces/SKILL.md,assets/skills/copilot/tws.prompt.md,internal/cli/add.go,internal/cli/checkout_lifecycle_test.go,internal/cli/importcmd.go,internal/cli/init.go,internal/cli/new.go,internal/cli/registry.go,internal/cli/registry_test.go,internal/cli/root.go,internal/registry.go,internal/registry_lock_unix.go,internal/registry_test.go,internal/workspace.go

## Change Summary

```
 README.md                                       |  37 ++++++-
 assets/skills/claude/tesseraworkspaces/SKILL.md |  27 +++++
 assets/skills/copilot/tws.prompt.md             |  19 ++++
 internal/cli/add.go                             |   4 +-
 internal/cli/checkout_lifecycle_test.go         |   4 +-
 internal/cli/importcmd.go                       |   4 +-
 internal/cli/init.go                            |  33 ++++--
 internal/cli/new.go                             |   3 +
 internal/cli/root.go                            |   1 +
 internal/workspace.go                           | 136 +++++++++++++++++++++++-
 10 files changed, 249 insertions(+), 19 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: README.md, assets/skills/claude/tesseraworkspaces/SKILL.md, assets/skills/copilot/tws.prompt.md, internal/cli/add.go, internal/cli/checkout_lifecycle_test.go, internal/cli/importcmd.go, internal/cli/init.go, internal/cli/new.go, internal/cli/registry.go, internal/cli/registry_test.go, internal/cli/root.go, internal/registry.go, internal/registry_lock_unix.go, internal/registry_test.go, internal/workspace.go
- **claim_ids**: (none)
- **base_commit**: `246c930936a64fc2d7d2f9ddaf129c698f2f413b`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/workspace-registry/artifacts/post-apply.patch
```

