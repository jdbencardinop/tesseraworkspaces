# Specification: sync cutoff integrity

## Problem

Ordinary stack sync can replay the wrong commit range when `last_base_sha` is
missing, stale, or not refreshed by every successful execution path. In
particular, archived external entries currently use a broad plain rebase and
leave the old cutoff behind. A recorded stale ancestor may still pass ordinary
Git existence and ancestry checks while causing shared upstream and parent
history to be replayed.

Sync must establish one evidence-backed old-parent boundary for every selected
row before any row moves, freeze that decision across parent rewrites and
recovery, execute the exact measured range, and publish the new boundary only
after verified Git success.

## Acceptance criteria

1. A genuinely new branch created by `tws new` in external or checkout mode
   resolves its requested creation base to one full object ID before branch
   creation, creates the branch from that exact ID, and records the same ID as
   `last_base_sha`. Logical names decoupled from Git branch names and explicit
   refs are supported. Existing/adopted/restored branches and imported entries
   retain their evidence or remain unknown; no creation cutoff is invented.
2. Before a fresh transactional sync mutates any selected branch, metadata, or
   checkout, every selected row has a frozen source repository, parent
   preimage, child preimage, raw recorded cutoff, effective cutoff, and cutoff
   decision.
3. A missing recorded cutoff is accepted only when Git proves the frozen
   current parent tip is an ancestor of the frozen child tip in the same object
   store. That exact parent object ID becomes the effective cutoff even when an
   earlier row later rewrites the parent.
4. A recorded cutoff is refused when it cannot be resolved to a commit in the
   row repository, is not an ancestor of the frozen child, belongs only to an
   unrelated repository/object store, or exact graph evidence proves that it
   predates history already shared by the frozen parent and child. Sync never
   substitutes merge-base, fork-point, reflog, patch-equivalence, or an
   arbitrary recent ancestor as an ownership boundary.
5. Cutoff refusal happens before any selected row moves and reports the logical
   entry, Git branch, raw cutoff, frozen parent/child object IDs, failed proof,
   and actionable manual inspection/repair guidance. Approval tokens and replay
   limit waivers cannot override a cutoff refusal.
6. Every actual ordinary-sync rebase, including external materialized,
   external archived, checkout, scoped, guarded, full, local-only, default, and
   resumed execution, consumes the frozen validated effective cutoff. Git argv,
   replay candidate counts, fingerprints, and user-visible plans describe that
   same range.
7. Intentional no-work, already-contained, and fast-forward outcomes are
   detected from verified Git results and do not claim an unmeasured replay
   count or drop unique child changes.
8. A successful row records the exact destination parent cutoff only after Git
   verifies the row reached its intended result. Failed and untouched rows keep
   their original metadata. This applies to materialized entries, archived
   entries, genuinely moved `--update-refs` collateral entries, and
   continue/manual-conflict completion. A collateral action may truthfully
   record a C-to-U boundary transition even when the configured tag or object
   parent remains C; on a later explicit sync, U is the old cutoff and the
   configured parent C remains the destination. Sync must replay only U..child
   onto C and record C after that Git action succeeds. It must not substitute U
   as the destination, silently no-op, or replay C..child.
   When one observed `--update-refs` action completes multiple selected rows,
   metadata is finalized recoverably, then completion of the primary and every
   actually moved selected collateral row is saved in one progress update.
   Metadata and progress are separate writes. A crash after that progress save
   cannot rerun a collateral row with its obsolete cutoff.
9. Collateral attribution is based on frozen ref preimages and observed
   postimages, not graph position. Only refs that Git actually moved are marked
   complete or skipped, and each moved stack entry receives the correct
   destination cutoff. Reusing a primary destination requires proof that the
   collateral row's frozen cutoff equals the primary replay cutoff and that the
   resulting transition is valid. A cutoff inside the primary replay range
   requires its actual rewritten local-parent counterpart; otherwise the
   affected rebase is refused before mutation with the failed proof/ref and
   phase-correct recovery guidance.
   Every executor arm where collateral is disabled invokes Git with
   `-c rebase.updateRefs=false rebase`, including scoped/local-only external,
   archived external, checkout, and their resume paths, so repository or
   inherited `rebase.updateRefs=true` cannot move an unowned ref without using
   the Git-2.38-only positive or negative rebase options. Plans, fingerprints,
   JIT revalidation, collateral mechanism, and warnings publish the same argv.
   Intentional full external `--update-refs` retains its exact Git 2.38
   capability gate; Git 2.26-2.37 remains supported on disabled routes.
   Continuation capability admission uses selected entries minus completed
   entries, including a retrying failed entry stored separately from pending.
   Unsupported retries refuse before another Git action is recorded.
10. Recovery state freezes the effective cutoff decision and its proof inputs.
    Resume revalidates repository identity and relevant object/ref evidence
    before execution. Older recovery documents that cannot reconstruct a safe
    cutoff from existing durable preimages refuse with legacy recovery
    guidance rather than widening the replay.
11. Abort preserves the released transaction contract: original refs,
    checkout/index/worktree state, and stack metadata are restored byte for
    byte. Publication ownership, live-lock, orphan-state, compatibility
    sentinel, no-stack consent, and cleanup-only restrictions remain intact.
12. Plan, status, and doctor/readback surfaces distinguish the raw recorded
    cutoff from the chosen effective cutoff and state an honest source and
    validity/reason. Read-only routes perform no metadata writes and no
    additional fetch. Existing nonempty metadata is not falsely labelled as
    sync-created. Both doctor formatters show sanitized raw/effective/source/
    validity/reason evidence for evaluated cutoffs and omit a fabricated
    decision for unevaluated rows. Human prose abbreviates object IDs while
    structured fields and executable repair commands retain full values.
    Missing-record parent advancement guidance
    explains the deterministic refusal and known-history repair instead of
    promising a plain rebase.
13. Existing valid no-flag sync behavior remains compatible apart from the
    intentional safety refusals, new branch initialization, correct metadata
    refresh, and additive output fields. External multi-worktree and
    multi-repository stacks remain supported; checkout remains one repository
    and one physical checkout.
14. The explicit no-stack nontransactional exception remains separate and does
    not synthesize stack cutoffs.
15. Real Git regression tests cover exact refs, measured replay counts, and
    exact metadata for:
    - new external and checkout branch creation, explicit refs, decoupled
      names, and a moving base-ref race;
    - missing-cutoff parent-ancestor success with a parent rewritten earlier in
      the run, plus missing ambiguous/nonancestor preflight refusal;
    - correct, nonexistent, nonancestor, and deliberately stale
      pre-shared-history cutoffs under full/local-only, default/scoped, guarded
      and unguarded routes in both workspace modes;
    - archived external child sync followed by restore/materialized resync;
    - actual `--update-refs` collateral movement in materialized/archived,
      decoupled-name, and multi-repository stacks;
    - tag and raw-object collateral C-to-U attribution followed by full and
      scoped/materialized explicit sync onto configured C, with exact U..child
      replay, plan destination/count/argv/fingerprint, checkout parity, and
      post-success metadata C;
    - conflict continue, validation failure, abort, foreign ref movement,
      corrupted recovery evidence, and legacy recovery refusal;
    - post-progress interruption after primary plus collateral completion is
      durable, with no duplicate rebase or shared-history replay on continue;
    - repository-local and inherited `rebase.updateRefs=true` with an
      in-range unheld ref under scoped, local-only, archived, and checkout
      execution, proving exact ref preservation and invocation-local false
      override plans on modern and real Git 2.37;
    - external/checkout doctor valid-recorded, valid-parent-tip, invalid, and
      unevaluated cutoff rendering plus sanitized direct refusal context;
    - read-only plan/status/doctor behavior and guard refusal precedence.
16. The focused integration suite, sequential required full test commands,
    `go vet`, `golangci-lint`, `make build`, `gofmt`, `git diff --check`, and
    `tpatch feature deps --validate-all` pass.

## Implementation plan

1. Introduce a shared typed cutoff decision/resolver near the existing rebase
   plan and ancestry helpers. It will resolve only within the selected row
   repository and return raw/effective values, source, validity, proof inputs,
   and an actionable refusal.
2. Extend fresh-run preflight and transaction/recovery rows to freeze original
   parent and child tips plus the cutoff decision before mutation. Revalidate
   immediately before row execution and after recovery admission.
3. Make the planner, guard fingerprint/count path, external executor, archived
   executor, checkout executor, and continue path consume the same effective
   cutoff. Replace positional collateral assumptions with observed ref
   preimage/postimage accounting.
4. Centralize post-success metadata finalization so each genuinely completed
   row records its destination cutoff and untouched/failed rows retain their
   frozen original value.
5. Seed exact creation cutoffs only for branches that `tws new` genuinely
   creates, using one resolved object ID for both creation and metadata.
6. Add truthful additive plan/status/doctor fields and update CLI help,
   README/cheatsheet, embedded skills, and documentation tests.
7. Add focused process-level tests with real repositories, bare remotes, and
   worktrees; then run the required sequential validation gates.

## Out of scope

- Issue #1 injection correctness, issue #3 scoped status, issue #2 templates,
  Lane B/tss/mailbox work, and safe-reparent behavior changes.
- Automatic cutoff repair or inference from merge-base, fork-point, reflog,
  patch identity/equivalence, or arbitrary recent ancestors.
- Successor maps, Git hooks, jj integration, and content/patch matching. Future
  patch-identity research is tracked separately by
  `tpatch-patch-identity-research`.
- Fetch policy changes, remote mutation, push/tag/release work, tpatch landing,
  or publication approval.
