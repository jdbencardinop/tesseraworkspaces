# Exploration: sync cutoff integrity

## Existing execution and metadata paths

- `internal/stack.go`
  - `StackEntry.LastBaseSHA` is the only durable replay boundary.
  - `UpdateBaseSHA`, `SaveStack`, and `SyncWriteStackValue` are the current
    writer surfaces. Transactional sync already snapshots byte-exact
    `stack.yaml` content for abort.
- `internal/cli/new.go`
  - `createCheckoutBranch` resolves a creation token, creates a branch when it
    is absent, then appends a stack entry without `LastBaseSHA`.
  - `createWorktree` has the same omission. It also registers pre-existing
    branches, so initialization must be gated on `!branchExists`.
  - `resolveCreationBase` returns a moving token. A new helper must peel it to
    one full commit and both branch creation and metadata must consume that
    object ID.
- `internal/cli/sync_helpers.go`
  - Materialized external rows select an `--onto` arm only when a nonempty
    record differs from the live base; otherwise they use plain rebase.
  - Archived rows always use plain `git rebase <base> <branch>`, never consume
    `LastBaseSHA`, and never refresh metadata.
  - `markUpdatedAncestors` assumes every archived graph ancestor moved under
    `--update-refs`; it does not inspect the action's actual ref before/after
    images.
- `internal/checkout_sync.go`
  - `CheckoutPlanEntry` already freezes branch and base preimages, but carries
    only raw `LastBaseSHA`.
  - `processBranch` refreshes `NewBaseSHA` after earlier parents move.
  - `doRebase` falls back to plain rebase when the record is absent or equals
    the destination.
  - `finalizeTransaction` currently writes every plan row's `NewBaseSHA`,
    including rows whose completion semantics need to remain explicit.
- `internal/cli/sync.go`
  - `finalizeObservedExternalAction` completes interrupted materialized rows
    and writes a live base resolution after action reconciliation.
  - Continue paths reconcile the transaction before resuming the executor.

## Existing transaction evidence

- `internal/sync_transaction.go`
  - `CaptureSyncTransaction` freezes exact `stack.yaml` bytes, every local
    branch preimage, worktree holders, repository roots/common dirs, and
    selected logical-name attribution.
  - `BeginSyncGitAction`/`CompleteSyncGitAction` persist action-level before
    and after ref images and already provide the authoritative basis for
    collateral movement.
  - `SyncAllowedRebaseRefs` measures the candidate range used to permit real
    `--update-refs` collateral.
  - The transaction is the appropriate shared durable carrier for per-entry
    cutoff evidence. New optional evidence fields will make strict older
    readers reject post-freeze documents; pre-freeze/legacy documents without
    evidence must refuse before another Git mutation unless safe evidence can
    be reconstructed from their durable preimages.
- `internal/sync_transaction_validate.go`,
  `internal/sync_transaction_load.go`, and `internal/sync_run_state.go`
  - Transactional state versions 4/5 are strict-decoded and relationship
    validated. New cutoff rows must be covered by the same identity, object-ID,
    selection, and action-progress validation without weakening reparent
    compatibility sentinels or cleanup-only recovery.

## Existing planning and readback

- `internal/rebase_planner.go`
  - `ExecutionOrder` predicts `UpdatedByRef` from graph position; this must not
    be reused as proof of actual completion.
  - `effectiveUpstream` falls back to the destination/base snapshot whenever a
    recorded cutoff is absent.
- `internal/rebase_plan_build.go`
  - `buildCutoff` reports raw records as `recorded-by-sync`, checks only
    resolvability, and leaves archived external rows as `not_used`.
  - `entryBlockers` already has the plan refusal integration point.
- `internal/rebase_plan.go`, `internal/rebase_plan_fingerprint.go`, and
  `internal/rebase_plan_render.go`
  - `PlanEntryCutoff` is the additive output surface. The effective cutoff,
    decision source, validity, and reason must participate in the guarded
    fingerprint and human rendering.
- `internal/stack_ancestry.go`
  - The evaluator already resolves child, parent, recorded cutoff, and
    merge-base in one repository with no fetch. It is the correct read-only
    integration point for the shared cutoff resolver.
- `internal/stack_status.go`, `internal/health.go`, and
  `internal/checkout_health.go`
  - Stack status projects evaluator evidence verbatim. Doctor/checkout doctor
    project `StackEdge` notes and guidance, so an additive cutoff decision and
    invalid-cutoff note can be shared without a second graph implementation.

## Cutoff proof

One typed resolver can operate on a repository plus exact frozen parent and
child object IDs:

1. Resolve both IDs as commits in the same measured common-dir identity.
2. With no record, accept only when parent is an ancestor of child; choose that
   exact parent with source `parent-tip-ancestor`.
3. With a record, resolve it to a full commit and require it to be an ancestor
   of child.
4. Compute the exact parent/child merge-base only as a negative proof. If the
   record is a strict ancestor of that shared commit, the record demonstrably
   predates history already shared by both sides and is invalid. The merge-base
   is never selected as a replacement boundary.
5. Return raw record state, effective object ID, source, validity, reason, and
   proof object IDs. Execution, plans, and readback consume this same result.

## Minimal implementation boundary

1. Add `internal/sync_cutoff.go` with the typed resolver, refusal formatting,
   transaction freeze/lookup/revalidation helpers, exact collateral movement
   projection, and focused unit tests.
2. Extend `SyncTransaction` with validated per-selected-entry cutoff evidence.
   Fresh checkout captures it before the transaction is persisted. Fresh
   external sync freezes it after the configured fetch and before the first
   branch action. Continue refuses legacy/unfrozen evidence before mutation.
3. Change external materialized, external archived, checkout fresh, and
   checkout resumed rebases to always use the validated effective cutoff in an
   honest `--onto` range. Revalidate immediately before the action.
4. Replace positional `markUpdatedAncestors` completion with actual
   `SyncTransactionAction.BeforeRefs`/`AfterRefs` movement. Refresh metadata
   for each observed successful target/collateral row only after parent
   ancestry verification.
5. Seed exact creation cutoffs only in the new-branch arms of
   `internal/cli/new.go`.
6. Extend rebase plans/fingerprints/rendering and stack
   ancestry/status/doctor projections with truthful raw/effective/source/
   validity fields.
7. Update `README.md`, `docs/cheatsheet.md`,
   `assets/skills/claude/tesseraworkspaces/SKILL.md`, command help, and their
   documentation tests.

## Approved collateral transition and literal-destination policy

- A collateral row has two distinct facts after `--update-refs`: the old
  cutoff consumed by that replay and the destination Git actually applied.
  For a fixed tag/object parent at C, a primary replay C..primary onto U may
  prove the collateral transition C-to-U. The successful collateral write
  records U because that is the new old-cutoff for the row.
- A later explicit sync does not reinterpret U as the configured destination.
  The configured tag/object still resolves to C, so planner and executor must
  publish and run `--onto C U`, replaying only U..child. After verified success
  metadata becomes C. This applies in external and checkout modes, including a
  scoped materialized external follow-up.
- Reusing a primary destination at the action seam is allowed only when the
  collateral cutoff exactly equals the primary replay cutoff and the parent
  transition proof succeeds. A moving local parent instead requires its exact
  `AfterRefs` counterpart.
- If those proofs do not establish the actual collateral boundary transition,
  the approved policy is conservative refusal before the affected rebase. The
  diagnostic names the entry, cutoff, parent/ref, failed equality/containment
  proof, and leaves phase-correct transaction recovery evidence. Sync does not
  disable collateral silently or widen the replay.

## Certification review fixes

- `finalizeExternalSuccessfulAction` already writes metadata for the primary
  and every selected ref in the observed action postimage. The executor must
  append those same affected names to durable `Completed` progress in the
  primary save, while retaining the normal per-row status output. Recovery can
  then skip the already-finalized action even after a crash immediately after
  progress persistence.
- Omitting `--update-refs` is not a negative override: Git configuration can
  turn it back on. Because both rebase flags require Git 2.38, every
  collateral-disabled argv uses the backwards-compatible Git-global override
  `-c rebase.updateRefs=false rebase`; `rebaseArgv`, planner collateral
  projection, fingerprints, JIT comparisons, archived execution, checkout
  execution, and legacy checkout helpers must match. Only actual positive
  `--update-refs` rows receive the 2.38 capability gate.
- `StackEdge` and `CheckoutFeatureEntry` already carry raw/effective/source/
  validity/reason cutoff fields. External and checkout doctor formatters need
  one sanitized evidence line only when evaluation occurred; unevaluated rows
  must not invent null decisions.
- `parent-advanced-no-base-record` cannot recommend ordinary sync because the
  cutoff resolver deterministically refuses. Guidance must direct inspection
  and known-history `last_base_sha` repair. Direct cutoff refusal must expose
  sanitized configured parent ref, frozen parent OID, logical/Git identity,
  and repository context.

## Test locations

- `internal/sync_cutoff_test.go`: missing, correct, nonexistent, nonancestor,
  stale-shared-history, repository mismatch, and exact collateral ref images.
- `internal/cli/new_integration_test.go`: exact base creation for external and
  checkout, decoupled names, existing/reactivated entries, and moving-ref
  seam.
- `internal/cli/sync_*_test.go`: process-level external full/local-only,
  scoped/default, guarded/unarmed, archived refresh/restore, collateral,
  continue/abort/validation, and multi-repository behavior.
- `internal/checkout_sync*_test.go` and
  `internal/cli/checkout_sync*_test.go`: corresponding checkout execution,
  conflict completion, and recovery evidence behavior.
- `internal/rebase_plan*_test.go` and `internal/cli/sync_plan*_test.go`:
  effective cutoff schema, exact range/count/argv, fingerprint, refusal
  precedence, and no-write/no-fetch plan behavior.
- `internal/stack_ancestry_test.go`, `internal/stack_status_test.go`,
  `internal/cli/stack_status_test.go`, and doctor tests: truthful readback.

## Non-goals confirmed

No safe-reparent behavior change or reverse dependency; no issue #1/#3/#2,
Lane B, tss, mailbox, remote publication, landing, commit, tag, or release
work.
