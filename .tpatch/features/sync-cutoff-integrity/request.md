# Feature Request: Fix GitHub issue #4 defect 2: make every sync row use and validate the correct old-parent cutoff so restacking replays only commits unique to that entry and never replays already-present master history. Audit LastBaseSHA attribution and absence semantics, require cutoff existence and ancestry before --onto, prevent broad plain-rebase fallback in stale-parent cases, expose the chosen cutoff and provenance in plans/status, and cover full/local-only external and checkout stacks with amended parents.

**Slug**: `sync-cutoff-integrity`
**Created**: 2026-09-12T15:42:23Z

## Description

Fix GitHub issue #4 defect 2: make every sync row use and validate the correct old-parent cutoff so restacking replays only commits unique to that entry and never replays already-present master history. Audit LastBaseSHA attribution and absence semantics, require cutoff existence and ancestry before --onto, prevent broad plain-rebase fallback in stale-parent cases, expose the chosen cutoff and provenance in plans/status, and cover full/local-only external and checkout stacks with amended parents.
