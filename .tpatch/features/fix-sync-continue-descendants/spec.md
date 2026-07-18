# Specification: fix-sync-continue-descendants

## Problem

`--continue` loses deferred descendants and reports false completion.

## Acceptance Criteria

1. A→B→C conflict on B can be resolved, and `--continue` rebases C before reporting complete.
2. A later conflict on C retains state for another continuation.
3. Divergent sibling lineages are not lost.
4. Continue refuses while Git still has an active rebase.
5. Final completion requires every relevant parent head to be an ancestor of its child.
6. `--push` runs only after verified completion.
7. `go test ./internal/cli -run TestSyncContinue` passes using real temporary repos/worktrees.

## Out of Scope

New local-only sync modes and general reparenting.

## Plan

Evolve `SyncState`, refactor filtered sync to return completion/failure state, resume authoritative pending branches, and add repo-aware final ancestry verification.
