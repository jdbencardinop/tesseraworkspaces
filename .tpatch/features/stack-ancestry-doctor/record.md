# Implementation Record: stack-ancestry-doctor

**Recorded**: 2026-08-12T12:03:39Z
**Files changed**: 15
**Patch size**: 198198 bytes
**Capture mode**: working-tree-all

## Change Summary

```
 .tpatch/features/stack-ancestry-doctor/analysis.md |  32 ++-
 .../features/stack-ancestry-doctor/exploration.md  |  71 ++++--
 .tpatch/features/stack-ancestry-doctor/spec.md     | 277 +++++++++++++++++----
 .tpatch/features/stack-ancestry-doctor/status.json |  14 +-
 CHANGELOG.md                                       |  63 +++++
 .../claude/tesseraworkspaces-orchestrator/SKILL.md |   4 +
 assets/skills/claude/tesseraworkspaces/SKILL.md    |  23 +-
 assets/skills/copilot/tws.prompt.md                |   2 +-
 docs/cheatsheet.md                                 |  10 +-
 docs/engineering-workflow.md                       |   9 +-
 docs/roadmap.md                                    |  23 +-
 internal/checkout_health.go                        | 229 +++++++++--------
 internal/cli/checkout_doctor_test.go               |   6 +-
 internal/cli/doctor.go                             |  54 +++-
 internal/health.go                                 |  84 ++++++-
 internal/resolve.go                                |  18 ++
 16 files changed, 684 insertions(+), 235 deletions(-)
```

## Capture Provenance

- **capture_mode**: `working-tree-all`
- **pathspecs**: (none)
- **claim_ids**: (none)
- **base_commit**: `f2a9be6c07805db9b7a024c0b369c0d51f847f37`
- **upper_commit**: `working-tree`

## Replay Instructions

To re-apply this feature to a clean checkout:

```bash
# From the feature's artifacts directory:
git apply .tpatch/features/stack-ancestry-doctor/artifacts/post-apply.patch
```

