# Implementation Record: fix-checkout-feature-path-routing

**Recorded**: 2026-08-05T09:17:51Z
**Files changed**: 21
**Patch size**: 33775 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 internal/cli/archive.go                 |   5 +-
 internal/cli/checkout_lifecycle_test.go | 124 ++++++++++++++++++++++++++++++--
 internal/cli/checkout_sync.go           |   5 +-
 internal/cli/decide.go                  |  15 ++--
 internal/cli/decisions.go               |  36 +++++-----
 internal/cli/delete.go                  |   5 +-
 internal/cli/doctor.go                  |  25 ++++---
 internal/cli/export.go                  |   7 +-
 internal/cli/hooks.go                   |  22 ++++--
 internal/cli/inject.go                  |  29 ++++----
 internal/cli/new.go                     |   5 +-
 internal/cli/push.go                    |  24 ++++---
 internal/cli/rename.go                  |  17 +++--
 internal/cli/stack.go                   |  12 ++--
 internal/cli/sync.go                    |  13 +++-
 internal/cli/template.go                |   7 +-
 internal/paths.go                       |  25 +++++--
 internal/resolve.go                     |  30 ++++++++
 internal/resolve_test.go                |  44 ++++++++++++
 internal/workspace.go                   |  22 ++++--
 internal/workspace_test.go              |  31 +++++++-
 21 files changed, 392 insertions(+), 111 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `09d0974761ecf314d8682495c2454f4931a3dfe1`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/fix-checkout-feature-path-routing/artifacts/post-apply.patch
```

