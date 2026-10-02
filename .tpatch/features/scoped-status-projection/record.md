# Implementation Record: scoped-status-projection

**Recorded**: 2026-10-02T07:24:56Z
**Files changed**: 20
**Patch size**: 107328 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .../features/scoped-status-projection/status.json  |  14 +-
 CHANGELOG.md                                       |  13 +
 README.md                                          |  16 +
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |   7 +-
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  12 +-
 assets/skills/copilot/tws.prompt.md                |   6 +-
 docs/cheatsheet.md                                 |  10 +-
 internal/agent_status.go                           | 440 +++++++++++++-----
 internal/agent_status_test.go                      | 330 +++++++++++++-
 internal/checkout_health.go                        |   4 -
 internal/cli/reparent_exclusion_test.go            |  30 ++
 internal/cli/status.go                             |  50 ++-
 internal/cli/status_test.go                        | 493 ++++++++++++++++++++-
 .../existing_commands/checkout-bare/status.json    |  26 +-
 .../existing_commands/checkout-bare/status.txt     |   7 +-
 .../existing_commands/external-bare/status.json    |  26 +-
 .../existing_commands/external-bare/status.txt     |   7 +-
 17 files changed, 1330 insertions(+), 161 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `8d0e4bc12b00326e4f5aa419939de7b8f64001ef`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/scoped-status-projection/artifacts/post-apply.patch
```

