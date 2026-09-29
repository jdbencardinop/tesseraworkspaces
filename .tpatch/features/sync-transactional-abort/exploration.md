# Exploration: transactional ordinary-sync abort

## Current failure path

### External mode

- `internal/cli/sync.go:dispatchOrdinarySync` sends no-flag runs through `syncFeature` with no durable payload, while new-mode and guarded routes use `.sync-state.yaml`, `.sync-state.v2.yaml`, and `.sync-run.lock`.
- `internal/cli/sync_helpers.go:syncWithStackScoped` rebases materialized entries with `git rebase --update-refs` on full-scope runs, rebases archived entries by naming the branch, and writes `LastBaseSHA` after each successful materialized row through unchecked `SaveStack`. Earlier completed refs and metadata are therefore already committed when a later row fails.
- `internal/cli/sync.go:handleSyncAbortCell`, `handleSyncAbort`, and `abortLegacySyncState` abort only the active rebase and delete runtime files. They do not restore completed or collateral refs, exact stack bytes, or changed holders.
- `internal/cli/sync_modes.go:setupSyncRunState`, `saveScopedSyncFailure`, and `clearSyncRunState` provide an existing ownership guard, compatibility sentinel, versioned payload, atomic persistence, teardown ordering, and crash hooks, but the payload contains progress only, not rollback evidence. Several error paths currently ignore state-write and cleanup errors.
- `internal/cli/push.go:pushScoped` persists successful pushes but not an intent before the first attempt. Legacy sync uses `pushFeature`/`pushEntries`, whose historical lenient failure behavior can remove all local recovery state even when a push failed.

### Checkout mode

- `internal/checkout_sync.go:RunCheckoutSync` validates the single physical checkout, acquires the workspace-global and feature locks, optionally fetches, builds a plan, and persists `CheckoutTransaction` before switching branches. The plan has selected branch `PreSHA` values but no exact metadata bytes, GC pins, collateral inventory, repository identity, or rollback phase.
- `processBranch` and `doRebase` persist coarse stages around checkout/rebase. `finalizeTransaction` writes all `LastBaseSHA` values only after the rebases, restores the original checkout, optionally pushes, and deletes the transaction.
- `AbortCheckoutSync` aborts an active rebase, calls `restoreOriginal`, deletes the transaction, and releases locks. `restoreOriginal` intentionally does not rewind an original branch that was in the plan, so completed selected refs remain advanced.
- `ContinueCheckoutSync` has version/downgrade gates and global reservation recovery that must remain authoritative. `StageCompleted` is also the current push-retry state.

## Existing primitives to reuse

- `internal/stack.go:WriteStackBytesAtomic` and `SaveStackAtomic` provide byte-exact, file-and-directory-durable metadata writes.
- `internal/checkout_sync.go:atomicWriteFile`, the sync guard helpers in `internal/sync_run_state.go`, and checkout mutation-lock helpers provide existing lock ownership and atomic state conventions.
- `internal/reparent_refs.go` demonstrates collision-safe run refs, pin verification, `git update-ref --no-deref --stdin` compare-and-swap transactions, and live-ref classification. The sync feature needs a small sync-specific equivalent because those helpers and state types are intentionally reparent-private.
- `internal/agent_status.go:BuildWorktreeInventory` provides canonical worktree path, branch, detached, and HEAD observations for holder snapshots.
- `internal/reparent_state.go:LoadReparentState` demonstrates strict known-field decoding and absent/unsupported/corrupt/foreign classification. New sync evidence should follow that loading pattern without treating reparent compatibility documents as sync-owned rollback state.
- `internal/reparent_exec.go` demonstrates durable intent-before-effect transitions, holder detach/restore, metadata preimage restoration, and resumable cleanup. Ordinary sync should reuse the patterns, not the reparent state machine.

## Planned state model

Add a shared `SyncTransaction` embedded in new external payloads and checkout transactions. It records:

- a random run ID; workspace mode, feature, workspace repository root, canonical repository roots/common dirs, object width, and frozen selected logical names;
- exact `stack.yaml` bytes/hash and the last run-owned metadata post-image;
- every local branch ref present in each touched repository at birth, including selected attribution, preimage, last observed run-owned value, and a run-owned `refs/tws/sync/<run-id>/...` pin;
- all worktree holders in each touched repository, including original attached/detached identity and last observed run-owned position;
- one durable action intent at a time for fetch/rebase/metadata/push and its authoritative observed completion; rollback phase and per-repository/ref/metadata/holder/cleanup progress;
- a monotonic publication marker written before the first actual push attempt.

Fresh runs use new recovery versions that older binaries reject. Existing external v2/v3 payloads, checkout v0/v2/v3 transactions, and real `.sync-state.yaml` artifacts remain legacy: continue behaves as before and abort prints an explicit warning that earlier branch tips and metadata cannot be fully restored.

## Mutation insertion points

1. **Birth:** after the existing external/checkout locks are acquired and the stack/selection is known, but before fetch or rebase, capture exact metadata and all local heads/holders for each selected repository, write the new payload/transaction, create every pin, verify them, and persist readiness.
2. **Fetch:** preflight configured fetch refspec destinations. Refuse a fetch that can write local branch refs (`refs/heads/*`) before executing it; ordinary remote-tracking/tag/FETCH_HEAD updates remain input refreshes, not local rollback effects.
3. **Rebase:** immediately before each Git rebase, verify current heads/holders equal the journal's expected values, compute the allowed ref set (selected branch plus replay-range collateral refs for `--update-refs`), persist intent, execute Git, then compare all local heads and holder positions and persist observed post-images even on failure.
4. **Conflict continuation:** reconcile an outstanding rebase intent from the actual rebase state, refs, and holder position. A still-running rebase remains user-actionable; a finished rebase records the observed result before validation/descendant work.
5. **Metadata:** persist source and target hashes/bytes before `WriteStackBytesAtomic`, then verify the live bytes and persist completion. External per-row updates and checkout final updates use this path.
6. **Push:** finish push preflight first; immediately before the first `git push` child, persist monotonic publication intent. Preserve state on first or later push failure and resume only remaining pushes. Abort refuses once this marker exists.
7. **Abort:** load strictly, reclaim existing locks, preflight every required repository/ref/holder/metadata image without mutation, abort an owned active rebase, detach clean holders attached to moved refs, restore refs with per-repository CAS transactions, restore exact metadata, restore holder positions, delete pins, then remove compatibility/state/lock artifacts. Persist progress before and after each step so repeated abort is idempotent.
8. **Continue:** refuse when rollback has begun; reconcile pending action state before resuming the existing frozen plan. Published runs continue forward and never become locally abortable again.

Approved completion clarification: after all forward effects succeed, persist
`completion: forward-complete` before deleting any GC-protection pin. A crash
in that final cleanup is cleanup-only under either recovery verb, with no ref
or metadata rollback. A refused admission uses
`completion: cancelled-before-mutation` instead, and rollback cleanup remains
distinguished by its durable rollback-start timestamp.

## Compatibility and observability insertion points

- Update version constants/derivations in `internal/sync_run_state.go`, `internal/checkout_sync.go`, and `internal/cli/sync_plan_guard.go`; preserve exact support for old v2/v3 state and add strict semantics for the transactional versions.
- Extend `internal/agent_status.go` and checkout sync status/readback to identify transactional pending, rolling-back, partial, and published recovery, with guidance that does not offer abort after publication.
- Update `internal/cli/sync.go` help, `README.md`, `CHANGELOG.md`, `docs/cheatsheet.md`, and the directly embedded sync skill text discovered under `.claude/skills/`/`.github/skills/`.

## Tests

- Add shared transaction unit tests for strict loading, pins, action reconciliation, ref CAS, metadata restoration, holder safety, publication, cleanup, and crash hooks.
- Extend real-repository CLI fixtures in `internal/cli` for the six-branch conflict-after-anchor case in external and checkout modes with exact all-ref and metadata assertions.
- Cover no-flag/full/local-only/only/from, guarded/unguarded, archived/decoupled/multi-repository, `--update-refs` collateral branches, validation/conflict failures, successful continuation, later user ref movement, dirty holders, corrupt/missing/newer state, publication-first-attempt, legacy warnings, and repeated abort after injected transition failures.
- Existing sync mode, guard, recovery, downgrade, push-resume, checkout, status, reparent exclusion, and golden tests remain regression coverage. Required validation is the focused suites followed by the repository-wide sequential gates from `spec.md`.

## Boundaries confirmed

No cutoff-selection change is needed here: `LastBaseSHA`/`--onto` choice remains owned by `sync-cutoff-integrity`. No remote ref is rolled back. No standalone `tws push`, safe-reparent transaction, Lane B, tesserasessions, mailbox, status-projection, injection, or template feature is reimplemented.
