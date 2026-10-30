# Scoped standalone publication

## Boundary

Implement #6 over the analysis baseline. The user authorized the established
Path B process and one RC after CI. Add selected standalone publication and
directly dependent selected-only follow-up handling, not another sync/reparent
recovery protocol or a rewrite of no-selector push compatibility.

## CLI and selection

1. Add mutually exclusive logical-name `--only <entry>` and `--from <entry>`
   to `tws push <feature>`. Select exactly one entry or its descendant closure,
   parent before child, without ancestors/neighbors. Use the existing shared
   resolver, not another walk/sort. Use GitBranch() for actual Git identities.
2. Support both explicit modes on the new route. Old no-selector/no-recovery
   routes keep output, exit and publication behavior, including standalone
   checkout refusal. Pending new state may cause an explicit safety refusal,
   never a silent legacy fallback.
3. Keep shared unknown/archived selector, duplicate-branch and checkout
   cross-repository rules. Archived closure members stay selected but are
   reported skipped. Active missing worktrees are not archived: publish their
   verified local refs without materializing/switching worktrees. Missing
   refs or unprovable repository identity refuse.
4. Add explicit mutually exclusive `--continue` and `--cancel` for this new
   state, with no fresh selectors. Fresh scoped invocation never automatically
   resumes. No matching journal means refusal. No remote-rollback abort verb.
5. Support scoped and continuation `--dry-run`; reject cancel plus dry-run.
   Pure flag errors precede workspace/Git/network/state effects. Provide
   help and logical completion from the execution-selected feature, with
   silent read-only unavailable-context completion.

## Plan, identity and isolation

6. Resolve one physical selected feature layout and controlling workspace.
   Preserve TWS_ROOT precedence, valid fallback, selected pending-rename
   checks before fallback and both admissions through effects. Never borrow
   the caller's unrelated repository as an entry's owner.
7. Resolve selected repositories only. Default-owner proof may use verified
   controlling context, explicit mapping, conventional root or selected
   worktree evidence. Do not search other features/worktrees for a publisher.
   Refuse ambiguity and changed physical common-directory bindings.
8. Capture full branch refs, source commit OIDs and exact origin push
   destinations. Reject ambiguous multiple endpoints/unsupported mappings.
   Store endpoint fingerprints, not credentials. Revalidate effective
   endpoint/config identity before each network attempt.
9. Freeze membership/order, relevant selected metadata, selected repo/branch
   identities, sources, destinations and lease evidence. Added descendants
   never join continuation. Changed selected mapping/unconfirmed source
   refuses; completed entries remain skipped. Unrelated changes do not
   become publication targets or require a whole-repository ref snapshot.
10. No checkout, rebase, stash, clean, reset, upstream changes, stack edits,
    worktree creation or unrelated index inspection/refresh. Unrelated
    dirty/unavailable/active-agent worktrees alone do not block publication.
    Checkout stays one physical checkout; never take over HEAD or its session.
11. Fresh scoped dry-run is local-only/read-only. Show mode, selector/order,
    logical/Git names, source OIDs, destination refs, skips, local lease/
    inclusion evidence and refusals. Remote state is explicitly unverified.
    No remote contact, fetch, locks/state/private refs, tracking updates or
    follow-up clearing. Prevent lazy fetching during local object probes.
12. Continuation dry-run projects the persisted original scope, successes,
    uncertain attempts and remaining entries without remote reconciliation
    or saving state. Untrusted state refuses.

## Publication and leases

13. Preflight every eligible entry before the first publication. Push in
    dependency order, stop at the first rejection/uncertainty/persistence
    failure and return nonzero. No swallowed error, unguarded force,
    feature-wide fallback or selector re-resolution on retry.
14. Freeze exact lease authority from existing selected tracking evidence,
    not a remote read/fetch. Missing evidence grants expected absence only.
    Validate supported tracking mappings or refuse ambiguity. Expectations
    survive retry even after a background/manual fetch advances tracking.
15. Send a pinned source OID and exactly one full destination with an exact
    expected-value lease. Prevent Git config from broadening publication via
    tags, mirror/matching defaults, prune or recursive submodule pushes.
    Preserve user hooks; do not disable them to claim isolation.
16. Preserve pending reparent warnings and inclusion protection. Explicit
    expected-OID leases make Git's inclusion flag ineffective; independently
    prove a conservative frozen selected-branch witness. Respect the tracking
    reflog cutoff and qualifying branch reflog range, including the first
    older entry, or provide a demonstrably stricter valid proof. Unverifiable
    evidence refuses. Retain needed evidence/objects across interruption;
    never grant new authority on retry. Legacy/sync bare-lease argv stay intact.
17. A network observation diagnoses/reconciles but never refreshes authority.
    For the recorded endpoint/ref only: actual value equal to intended source
    can confirm an uncertain entry; original expected value/absence permits
    retry with the original lease; every other value refuses. Never overwrite
    later remote work or reset a confirmed success.

## Durable state and admission

18. Use strict independently typed/versioned standalone state, bound to token,
    mode/workspace, physical feature/root/repositories, selected entries,
    source/destination/lease/inclusion values and ordered outcomes. Validate
    known fields, version, structure and reachable phases. No fabricated
    sync/reparent/template payload or Git lock.
19. Hold appropriate mutation exclusion through state publication, network,
    follow-up and cleanup, plus rename admission. Integrate pending state
    with current mutator/recovery admission so sync/reparent/lifecycle cannot
    erase/adopt its evidence. Never steal live locks, kill sessions, block
    unrelated external features or regress early-refusal/process budgets.
20. Persist the plan and per-entry intent durably before remote mutation;
    persist success before the next entry. Retain frozen objects with exact
    owned private Git refs where needed; never change branch refs. Refuse
    collisions instead of adopting foreign files/private refs.
21. Disconnect, failed Git exit and success-recording failure may have
    published. Continue only frozen entries, skipping durable successes.
    Recovery must work after actual process death, not just returned errors.
    Never restart rebasing or silently rerun successful publication.
22. Cancel abandons remaining publication and reports confirmed/uncertain
    effects. No remote mutation, branch reset or rollback. Clean only exact
    owned state/reservations/private refs after identity/value checks;
    preserve evidence on mismatch/failure. Changed defaults/roots cannot
    redirect recovery. No clear-all or foreign-state cleanup.
23. State/lock reads refuse symlinks, directories, FIFOs and unsupported
    objects promptly. Use atomic publication, directory durability and exact
    ownership cleanup. Visible effects after a failed durability barrier are
    not durable completion; replay repeats necessary barriers.
24. Completion requires every eligible recorded entry confirmed or explicitly
    skipped, with cleanup finished or clearly recoverable. No cross-ref/
    cross-repository atomicity, arbitrary-writer snapshot isolation or remote
    rollback claim. Document consistent new-binary use while state remains,
    without pretending every old command recognizes the new state.

## Reparent follow-up and existing sync

25. Validate the complete follow-up record but probe/change selected rows
    only. Preserve unselected rows/timestamps; do not probe unavailable
    neighboring repositories. Keep the full stack for existence semantics;
    never filter it then mark neighbors absent/archived.
26. Use the selected-only seam in external/checkout scoped sync-plus-push and
    recovery/cleanup. Its frozen selector/completed/pushed sets remain owned
    by existing sync state. Unscoped publication and non-publication clearing
    remain compatible. Do not redesign transaction-wide integrity/rollback.
27. Preserve follow-up through partial failure and continuation/cancel.
    Successful publication clears only proven selected rows; cancellation
    never clears unrelated rows or declares uncertain remote effects absent.

## Acceptance and delivery

28. Real temporary repositories, bare remotes and worktrees must cover the
    forked A/B/C/D and X/Y fixture. Run
    `tws push <feature> --from A --dry-run`,
    `tws push <feature> --from A`, and `tws push <feature> --only B`.
    Assert exact destinations/order and preserved ancestor/neighbor refs,
    HEADs, indexes, dirty bytes, unavailable worktrees and agent records.
    Also cover `--from B`, slash logical names and decoupled Git branches.
29. Cover both modes, selected active refs without worktrees, missing refs,
    archived cases, multiple selected repos, duplicate refs, invalid/empty/
    conflicting flags, TWS_ROOT disagreement/fallback, pending selected-root
    rename, aliases and pure preview/completion. Keep old no-selector goldens.
30. Test stale leases after tracking advances, remote absence/existing refs,
    valid inclusion and unintegrated advances, selected follow-up with an
    unavailable neighbor, config that could broaden a push, and uncertainty
    after acceptance. Inspect real remote refs, not only wrapper argv.
31. Test partial success, actual process death, intent/result/cleanup fault
    seams, no success replay, changed descendants/sources/roots/endpoints,
    exact cancellation, corrupt/unsupported/special-file state, foreign
    locks/private refs and crash-safe admission. Retain scoped-sync conflict
    and publication-resume regressions.
32. Update help, README, cheatsheet, changelog, embedded guidance and prose
    assertions with scoped versus legacy/recovery semantics. Preserve earlier
    releases and planning-prose negative sync-flag contracts.
33. Use isolated implementation, parent audit and three scoped independent
    reviews of one immutable manifest. Run focused integration/CLI checks and
    full tests, vet, lint, build and diff/DAG gates. Record/replay recipe and
    patch at the exact base, prove new-path absence, and land one tpatch commit.
    Verify landed behavior/clean tree; push and wait for main CI, then tag the
    next unused RC, verify version and tag/release CI, and only then close #6.

## Non-goals

No automatic sync/rebase, all-stack rewrite, archived publication, arbitrary
remote/refspec CLI, PR creation/retargeting, remote rollback, multi-parent
graph, repository relocation, general journal repair or bulk historical
tpatch evidence rewrite.
