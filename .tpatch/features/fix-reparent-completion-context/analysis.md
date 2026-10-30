# Reparent completion context: analysis

## Boundary and baseline

Implement GitHub #7 in the already registered
`fix-reparent-completion-context` boundary, based on
`54f214f080f82ac66b44e3d16de4cf7f8f285419`. Scoped publication is landed,
pushed, and main-CI verified there; its rc.8 tag/release CI is still pending
when this preparation is authored. The user authorized autonomous Path B
work and a separate RC for each completed issue. Keep this implementation
isolated, and do not land/publish it before the preceding release is verified.

The defect remains present. `reparentEntryCandidates` uses `RequireWorkspace`
and `ws.ResolveFeaturePath`, ignoring the execution-selected external layout.
`reparentBranchCandidates` has no feature/target inputs and inventories the
caller/default workspace repository. Explicit execution instead uses
`requireReparentWorkspace`, `resolveExternalSyncLayout`, and
`ResolveReparentTargetRepository`. Thus completion can name entries from the
wrong same-named feature and refs from the wrong repository, while explicit
execution retains its own safety checks.

## Smallest correction

Resolve one completion context using the existing execution helpers. Derive
logical suggestions from the full selected stack and existing closure logic;
derive Git suggestions from a known target's actual repository. Do not append
caller-repository branches after feature/target context fails.

Without a known target, positional logical entry completion remains available.
Do not guess a Git repository for destination-ref suggestions before the target
is known. Preserve declaration-order logical suggestions followed by sorted
Git branch suggestions where context is complete, including existing logical
target/descendant exclusion and archive eligibility. Do not redesign literal
ref eligibility or reparent execution.

Completion remains silent, read-only and local-only. It must not take writer
reservations, create journals, fetch, move refs/indexes, launch sessions or
repair metadata. Existing selected-root rename/publication refusals remain
applicable without creating state.

Hard parents are `safe-reparent-restack`, the existing command/target resolver;
`fix-external-feature-rename-backpointers`, its selected-root admission;
and `scoped-push-selection`, the current shared fallback/publication guard.
Do not change dependencies to hide known historical tpatch verifier limits.
No dependency upgrade or storage-format change is required.
