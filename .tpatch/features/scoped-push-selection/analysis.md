# Scoped standalone push: analysis

## Summary and baseline

GitHub #6 is not implemented by standalone `push` in v1.2.18-rc.7
(`d38b442f91873e2bfa20fe5e4dc3b49ec7c0f69d`). Its older issue body refers
to v1.2.16; current scoped sync-plus-push already exists and is resumable.
The missing boundary is selected standalone publication, not another selector.
Implementation base is `38a7baf6981c305f2f3c1cbe23b1db3406ee90e0`, a
separately validated documentation-only rc.7 release-handoff prerequisite.

The user chose new scoped support in both modes, publication of selected
active refs without materialized worktrees, and independent durable
continuation. They subsequently authorized autonomous implementation,
review, landing, push, and an RC after CI. Existing unscoped behavior,
including standalone checkout's worktree refusal, remains unchanged.

## Existing seams and compatibility

- `internal/cli/push.go:pushCmd` exposes only `--dry-run`.
  `pushFeatureSerialized` retains selected-root and actual-root rename
  admission and holds `ClaimSyncRunGuard` through publication. `pushEntries`
  is intentionally lenient, skips missing worktrees and can return success
  after failures. Filtering it is not the new executor.
- `pushFeatureCheckout` reaches `RequireWorktreePath` and refuses. New
  selectors opt into publication-only behavior, never physical checkout.
- `internal/sync_selection.go:ResolveSyncSelection` already owns logical
  membership, descendant traversal, ordering, archived-selector, checkout
  cross-repository and duplicate-Git-branch rules. Keep one selector.
  Rebase propagation roles do not determine standalone push eligibility.
- `pushScoped` publishes completed selected sync entries, records `Pushed`
  and resumes through sync's own state. `SyncPreparePublication` binds
  source/destination to a transaction that also validates ref/holder images.
  Do not fabricate these rebase/rollback obligations for standalone push.
  Checkout `finalizeTransaction` and `finalizeCleanup` already persist
  publication attempts and skip confirmed successes.
- `PrepareReparentPushEnvelope` and `PersistClears` evaluate every follow-up
  row. `ObserveReparentRemoteClear` treats an absent stack entry as removed.
  Filtering the stack first would wrongly clear unselected rows. Add a
  selected-only observation/clearing seam over the full valid record/stack,
  for scoped standalone and scoped sync publication; preserve all-row callers.
- `ResolveExternalWorkspaceAt` scans features/worktrees to infer ownership.
  It is not a scoped owner resolver. Use verified selected context, explicit
  workspace mapping or an unambiguous owning repository, not a neighbor.
- `resolveExternalSyncLayout` checks selected pending rename names before
  fallback and retains `selectionRoot`. Keep both admissions through effects.
  Do not reintroduce eager generic resolution or early-refusal regressions.

## Lease decision and grounded evidence

Freeze expected remote values from selected local tracking evidence,
including explicit expected absence. Remote reads may diagnose or reconcile
uncertainty, never grant a newer lease. No implicit fetch occurs. Push one
pinned source OID to one destination with an exact expected-OID lease.

Do not simply append `--force-if-includes` to an explicit lease. An isolated
Git 2.55.0 probe with a local repository and bare remote confirmed that bare
lease plus inclusion rejects an unintegrated remote advance with either a
branch or OID source, whereas an explicit expected-OID lease plus that flag
accepts it. Explicit stale OIDs still reject after tracking advances.

Official source confirms this:
<https://github.com/git/git/blob/v2.55.0/remote.c#L2696-L2868>.
Git considers branch reflog tips down through the first entry older than the
latest tracking-ref reflog timestamp, then tests remote-tip reachability.
The first older entry is included; indiscriminately scanning all historical
entries is not equivalent.

For the new exact-lease route, independently prove a conservative inclusion
witness for pending published reparent rows, binding it to the frozen lease,
actual branch and source plan. Preserve needed evidence/objects across retry
or refuse if unprovable. Never claim an inert flag provides protection.
Existing unscoped/sync bare-lease argv remain unchanged. Real-Git comparisons
must cover valid rewritten branches and unintegrated remote advances.

## State and effects

Standalone publication has no local rebase to roll back. Use its own strict,
versioned state: durable per-entry intent before network mutation and durable
success before the next entry. Freeze mode, feature/root/repository identities,
selection/order, sources, destinations, endpoint binding and lease/inclusion
evidence. Keep credentials out of journal/output identity fields. Retain
needed objects with exact owned private refs if necessary, never branch edits.

Failed/disconnected push is not proof the server changed nothing. Continue
reconciles only the recorded destination: intended source confirms completion;
original expected value permits retry with the same authority; anything else
refuses. Never repush a durable success even if that remote later moves.
No cross-ref/cross-repository atomicity or remote rollback is claimed.

Cancellation abandons unfinished publication and cleans only exact owned
reservations/private refs. Preserve evidence on incomplete cleanup. Fresh
invocation never silently adopts/broadens pending state. Integrate current
mutator admission without masquerading as sync/reparent/template recovery.
Do not use older binaries while publication is pending; do not claim universal
old-binary blocking without proof.

## Dependencies and non-goals

Hard parents: `push-branches` (legacy command), `sync-modes` (shared selection),
`sync-transactional-abort` (current publication/recovery),
`safe-reparent-restack` (inclusion/follow-up), and
`fix-external-feature-rename-backpointers` (root/name admission).
Checkout and multi-repository foundations are transitively included.

The standalone route requires Git 2.45+ so it can pass the enforced global
`--no-lazy-fetch` option. Read-only inspection of public `git.c` confirms
v2.43/v2.44 lack it and v2.45+ support it; merely setting its environment
variable on older Git cannot guarantee a local-only preview. Unsupported Git
therefore refuses the new route rather than risking a fetch. Legacy and sync
Git argument/compatibility paths are unchanged; no Go dependency change or
tool installation was needed in the implementation environment.

No automatic sync/rebase, new arbitrary remote/refspec CLI, PR mutation,
all-stack rewrite, archived publication or general repair belongs here.
No dependency upgrade is required. Historical canonical-parent verification
remains separately owned by `repair-historical-tpatch-artifacts`; do not
distort the DAG or shipped evidence to hide it.
