# Specification

## Acceptance Criteria

1. Checkout sync refuses dirty, detached, or already-rebasing repositories before changing branches.
2. A per-workspace lock prevents concurrent checkout lifecycle operations; stale locks are reported with explicit recovery.
3. `.tws/state/checkout-sync.yaml` persists original branch/HEAD, ordered branch plan, completed branches, current/failed branch, and requested push/validation context.
4. Sync switches/rebases logical branches sequentially in topological order and uses actual Git branch names.
5. On success, sync verifies all ancestry edges, restores the original branch, removes state/lock, and only then reports completion/pushes.
6. On conflict or validation failure, state remains; guidance explains resolution and `--continue`.
7. `--continue` resumes from the persisted plan and `--abort` aborts an active rebase, restores the original branch, and clears state/lock.
8. Process/failure injection after each transaction step is recoverable without losing commits or metadata.
9. External sync behavior and tests remain unchanged.

## Out of Scope

- Dirty-tree auto-stash.
- Detached-HEAD recovery beyond refusal.
- Multiple repositories or concurrent branch-owning agents.
- Checkout-mode open/session activation.
