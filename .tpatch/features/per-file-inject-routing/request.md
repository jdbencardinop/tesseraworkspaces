# Feature Request: Support per-file inject targets via an inject.yaml mapping. Currently inject_into is a single global value. Users need CLAUDE.local.md at worktree root (agent auto-discovery) but planning docs in a gitignored subfolder (dev/). inject.yaml would map files to destinations: CLAUDE.local.md → ., *.html → dev/, planning/ → dev/planning/. Directory-mirroring mode: inject/dev/foo lands at worktree/dev/foo naturally. This unblocks F1, F2, F4 from user feedback.

**Slug**: `per-file-inject-routing`
**Created**: 2026-07-01T04:23:12Z

## Description

Support per-file inject targets via an inject.yaml mapping. Currently inject_into is a single global value. Users need CLAUDE.local.md at worktree root (agent auto-discovery) but planning docs in a gitignored subfolder (dev/). inject.yaml would map files to destinations: CLAUDE.local.md → ., *.html → dev/, planning/ → dev/planning/. Directory-mirroring mode: inject/dev/foo lands at worktree/dev/foo naturally. This unblocks F1, F2, F4 from user feedback.
