# Safe external feature rename

## Boundary and authorization

Implement GitHub #5 in `fix-external-feature-rename-backpointers`, separately
from the already released rename/template features. Preparation is complete,
and the user has authorized Path B implementation through the required
review, validation, record/replay, and single-feature landing process.
Publication still requires separate approval; do not push or tag.

The user approved narrowly scoped pointer updates with no dangling Git
relationships after successful completion, future-renames-only scope,
ordinary dirty-work preservation, verified rollback, and explicit
operation-scoped continue/abort. This is not a general repository repair tool.

## CLI, scope, and inventory

1. Preserve `tws rename feature <old-name> <new-name>` for a fresh rename.
   Fix external mode for both legacy and managed features. Valid checkout
   behavior, including legacy/new layout handling and registry semantics,
   remains unchanged and never invokes external worktree repair.
2. Add external-only `--continue` and `--abort` to that feature-rename route.
   They are mutually exclusive, take the same old/new names, and operate
   only on a matching durable journal from this implementation. No journal
   means refusal, not heuristic repair. A fresh invocation must not silently
   resume pending work. Branch rename gets no new recovery flags.
3. Validate both names before constructing mutation paths. Preserve name,
   registered-space, root-selection, owning-repository, and layout guards.
   A fresh rename requires the source to be the expected regular directory
   and the destination to be absent using no-follow inspection; recovery
   validates the location allowed by its recorded phase. Refuse identical,
   aliasing, ancestor/descendant, symlinked, or occupied source/destination
   relationships; never replace an existing destination, even an empty one.
4. Build a complete, immutable relocation plan before effects. Record the
   controlling workspace separately from each selected entry's repository.
   The old-to-new mapping preserves logical worktree names and their path
   depth; no Git branch, configured base, or `StackEntry.GitBranch()` changes.
5. Account for every Git worktree that the directory move would affect,
   not just the active injection projection. Cover flat/slash names,
   decoupled Git names, multiple repositories, and legacy features without
   `stack.yaml`. With authoritative stack metadata, unlisted physical
   worktrees, archived-but-materialized entries, duplicate mappings, or
   overlapping physical worktrees refuse rather than being silently skipped.
6. Ordinary archived/nonmaterialized entries need no pointer update and
   retain their metadata. A missing path with a still-registered stale Git
   worktree entry is an already-broken layout, not permission to adopt or
   repair it. Malformed/unreadable metadata, copied markers, inconsistent
   registrations, and pre-existing broken pointers refuse before movement.
   A feature with no materialized worktrees still supports metadata rename.
7. Prove the supported Git relationship for each materialized worktree:
   regular `.git` file, exact administrative-directory identity, regular
   `gitdir` and `commondir` records, expected common repository identity,
   matching top-level root, and exact registration in byte-safe Git inventory.
   Repository remote URLs alone never establish identity.
8. Inventory must identify unsupported relocation requirements, not move them
   and hope validation passes later. Refuse primary/common Git directories
   inside the moving tree, nested repositories or populated submodules that
   require additional relocation, and path-bearing Git configuration whose
   complete supported relocation cannot be proved. Do not rewrite arbitrary
   configuration, hooks, alternates, or other tools' references.

## Preservation and scoped Git updates

9. Preserve ordinary staged, unstaged, untracked, ignored, executable, and
   symlinked user content. Do not stash, clean, reset, checkout, remove/re-add
   worktrees, or reconstruct working directories. Preserve index bytes and
   HEAD/ref values; validation must not refresh/write the index as a side
   effect. Dirty content alone is not a refusal.
10. Refuse active Git operations/index locks, locked worktrees, active or
    unverifiable sessions, and existing sync/reparent/template recovery.
    Recheck relevant admission under the existing lifecycle and external
    mutation reservations. Never kill an agent or override a Git lock.
11. Relocate only the feature directory and exact selected pointer files,
    plus the already-supported spaces rewrite. Do not invoke ordinary
    `git worktree repair`, even with explicit paths, because its repository
    scan can modify unrelated worktrees. Git read-only discovery remains
    the authoritative postcondition check.
12. Preserve absolute/relative pointer style on a per-record basis; do not
    infer it solely from mutable `worktree.useRelativePaths`. Compute the
    new binding from the verified roots. Do not rewrite a forward `.git`
    file when its exact existing bytes already bind the correct unchanged
    admin directory. Never normalize unselected linking files.
13. Keep administrative/common directory identities stable. Preserve v1/v2
    managed ledgers, effective file bytes/modes, remembered overrides,
    source/renderer provenance, logical link mappings, and symlink identities.
    Moving a feature is not template synchronization or ownership adoption.
    Existing unacknowledged user edits remain unacknowledged, not overwritten.
14. Use pinned no-symlink parents and the existing same-filesystem exclusive
    publication/preservation mechanisms. Snapshot exact pointer preimages,
    modes, identities, roots, and intended postimages; preserve originals
    before replacing any pointer or registry record. Apply only at the
    recorded bindings. Reject unexpected replacement or content drift.
15. Publish the feature directory with genuine no-replace semantics, not
    a preflight-only existence check followed by overwriting rename.
    A returned rename/fsync error may occur after movement: determine the
    actual location from verified identities rather than assuming nothing
    happened. The same rule applies to pointer and registry publication.
16. Preserve all unrelated worktree records, pointers, refs, configuration,
    and feature contents, even when an unrelated worktree is already broken.
    Do not run a repository-wide repair or introduce a clean-all/prune step.
    Test this with a deliberately broken sibling in the same repository.

## Registry coordination and success conditions

17. Reuse `BeginSpacesFeatureRename` semantics: preserve pinned-target
    refusals, rewrite only the selected relative paths/feature scopes, and
    do not create `spaces.yaml` when absent. Hold its existing lock when
    applicable and account for registry appearance/replacement/content drift.
    Unrelated entries and their timestamps remain unchanged.
18. Stage registry changes before movement but publish them only after every
    affected Git relationship has been repaired and validated at its new
    location. Coordinate owned pre/postimages and directory durability with
    the rename operation; a generic returned error is not proof that the
    registry remains at its preimage. Do not silently overwrite external
    registry edits.
19. Report successful rename only after the new feature directory is the
    original directory identity, every affected worktree has valid forward
    and backward Git pointers and common-dir identity, and Git reports its
    new top-level/registered path. No affected registration may still name
    its old path or point to a missing `.git` file. HEAD/ref values and
    managed ownership evidence remain intact.
20. Registry state must match the chosen final feature name, and the durable
    journal must record completion before verified cleanup. A successful
    rollback must satisfy the equivalent complete Git/registry checks at
    the old location. A partial result must never print the normal success
    message or imply that dangling administrative links have been repaired.
21. Exercise subsequent archive/reactivation, injection, verified discovery,
    and ordinary template sync after a successful rename, without a manual
    `git worktree repair` between commands. Preserve V2 overrides and their
    effective bytes through rename/recovery and non-rendering lifecycle
    operations without another acknowledgement. If a later explicit template
    sync produces genuinely different content (for example native guidance
    embeds the new feature name), retain the existing conflict policy.
    Rename must not silently re-render or acknowledge that new proposal.

## Failure, journaling, and recovery

22. Use a versioned, durable feature-rename journal in the execution-selected
    workspace metadata, outside the moving feature. It must remain
    discoverable by both names even when either feature path is absent.
    Record both names/paths, root/repository/admin identities, exact
    pointer/registry preimages and proposed postimages, ordered effect
    intents, progress, and preserved-object locations. No secrets or arbitrary
    executable commands belong in the journal.
23. Persist an effect's intent and required recovery evidence before it can
    mutate data; sync records and containing directories in the correct
    order. Unknown versions, malformed data, unsafe paths, symlinks, or
    unverifiable evidence fail closed. Recovery data is not authority to
    mutate a different repository or a newly replaced root.
24. The source lifecycle lock follows the moved directory inode; carry the
    real current path through reservation release. Add explicit admission
    for pending old/new name bindings where needed. Cooperating fresh
    lifecycle/mutation and context-launch routes must not treat either name
    as healthy/new while recovery is pending. Preserve unrelated-feature
    behavior; registry serialization may cover the shared registry while
    the operation owns an unresolved registry rewrite.
25. On ordinary failure, attempt automatic rollback only for verified
    operation-owned effects. Never overwrite a new occupant at the old or
    new path, restore stale pointer bytes over a foreign edit, erase user
    work, or rewrite unrelated state. Preserve both versions/evidence and
    return a clear partial-outcome error if rollback cannot be completed.
26. Explicit continuation validates the journal, identities, currently
    observed pre/postimages, sessions, locks, and admission before retrying
    only recorded unfinished effects. It must not replan a different rename
    from current config, assume an unknown path belongs to this operation,
    or expand the worktree/pointer set.
27. Explicit abort restores verified originals and the old directory/registry
    relationship only while rollback remains supported by the journal's
    completion state and exact evidence. After durable successful publication,
    continuation may finish verified cleanup only; abort must not silently
    undo a completed rename. Repeated recovery is idempotent or explicitly
    reports already-completed/not-pending, never a success-shaped fallback.
28. Recovery dispatch locates the bound journal before assuming the old
    feature directory exists. Wrong names, another repository, another
    journal, altered roots, and incompatible flags refuse. The new verbs
    cannot repair arbitrary pre-existing corruption without such a journal.
29. Retain meaningful completed/pending steps and exact diagnostic locations
    on failure. Cleanup removes only verified operation-owned entries;
    unexpected residue survives and is reported. Never recursively sweep
    arbitrary remaining files or remove evidence merely to make retry work.
    Do not forge sync/reparent/template state to obtain an old compatibility
    lock or claim that older binaries universally recognize new recovery.
30. Document that directory movement, Git pointer publication, and
    `spaces.yaml` publication are separate effects, with no all-file or
    cross-repository atomicity/snapshot-isolation claim. Temporary mismatches
    during interruption require explicit recovery; success requires complete
    no-dangling-link postconditions. Users must not use older binaries while
    this new operation is active or recoverable.

## Tests, documentation, and implementation sequence

31. Use real temporary repositories, local bare remotes, worktrees, and
    isolated configuration. Cover legacy/managed v1/v2, flat/slash names,
    multiple repositories, decoupled branch names, absolute/relative pointers,
    empty/archived features, ordinary dirty/index/untracked content, custom
    execution roots, physical aliases, and affected/unaffected siblings.
32. Add deterministic checkpoints around directory movement, each pointer
    preservation/publication, registry publication, completion, and cleanup.
    Test failures before/after real effects, crashes/interrupted journals,
    safe rollback, unsafe rollback refusal, continuation/abort, source/dest
    reoccupation, pointer/admin/root replacement, and concurrent registry
    edits. Retain old inodes when a fixture requires distinct identities;
    do not rely on unlink/recreate assigning a new inode.
33. Preserve the existing checkout, spaces, direct-session, branch-rename,
    template, and recovery regression suites. Update README, cheatsheet,
    relevant agent guidance, changelog, roadmap/workflow, CLI help/completion,
    and executable documentation assertions when implementation is authorized.
    Explain future-only repair, dirty-content preservation, recovery scope,
    unsupported layouts, and the distinction between success and partial state.
34. Before a later implementation lands, run focused real-Git tests and the
    required repository gates:

    ```bash
    go test ./internal ./internal/cli -run 'Test(.*FeatureRename|SpaceLifecycle|BeginSpacesFeatureRename|ManagedTemplate|NamedTemplate|SyncPlanDocs_)' -count=1 -timeout 20m
    go test ./... -count=1 -timeout 40m
    go vet ./...
    golangci-lint run ./...
    make build
    git diff --check
    tpatch feature deps --validate-all
    ```

    Format changed Go files and complete the mandated review loop and
    manual record/replay/land process. Keep historical tpatch verifier
    limitations explicit. Do not amend earlier shipped commits or combine
    this fix with unrelated Git lifecycle repairs.

The smallest coherent sequence is: build verified external relocation and
recovery state, wire the feature CLI and pending-operation admission, coordinate
exact pointer/registry effects and recovery, then add failure-ordering and
lifecycle regressions plus documentation. Implement in an isolated worktree;
do not amend the published implementation or bypass the review/landing gates.

## Non-goals

General repair of old broken features, repository-wide Git repair, branch
rename/rebase/ref changes, worktree remove/re-add, stash/clean/reset, moving
main/common repositories, populated submodule relocation, arbitrary external
path rewrites, template re-rendering/adoption, new status schema, remote
publication, and historical tpatch artifact repair.
