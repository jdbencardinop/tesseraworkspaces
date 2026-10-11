# Managed template conflict resolution

## Boundary

Add explicit interactive resolution to the managed template behavior shipped
in `v1.2.18-rc.5`. This is `template-conflict-resolution`, not an extension of
the already-landed `named-feature-templates` commit. The user approved
managed-owned text only, remembered keep/edit decisions, versioned metadata,
two-way staged editing, explicit opt-in, and refusal of customized-file
deletion. Planning is complete and the user has authorized implementation.
Pushing or tagging still requires separate approval.

## Entry points and eligibility

1. Add `--resolve-conflicts` to `tws add <feature>` and
   `tws template sync <feature>|--all`. It enables per-file decisions, not
   ownership adoption or a new selection/default policy. Existing
   `--template`, role, agent, and scope semantics remain unchanged.
2. Without the flag, unresolved managed conflicts still fail before mutation
   with actionable feature/role/path diagnostics. Only an eligible conflict
   should recommend the resolver flag. No prompts occur automatically.
   A valid remembered override is not an unresolved conflict.
3. The flag alone must not migrate a legacy feature or select built-in managed
   roles. Reject it with directory-valued templates or a legacy-only target.
   For a mixed `--all` batch, identify legacy members explicitly and retain
   their existing copy-if-missing behavior only after all managed decisions
   and preflight checks succeed. Existing explicit managed selectors may
   still opt a feature into managed planning, but do not adopt present assets.
4. A resolvable conflict needs an existing ledger-owned path and a present
   regular file, unchanged recorded permission mode, valid owning roots and
   role, and a present rendered incoming regular text file. Text means valid
   UTF-8 without NUL; empty non-native assets are valid. Reject binary,
   type, permission, missing-source, and ownership ambiguity as hard errors.
   Ordinary non-conflicting binary/template updates retain rc.5 behavior.
5. Source/package, destination-parent, control-path, source-origin, renderer,
   root identity, literal Git tracking, and external-link checks still run.
   Unmanaged same-content files, symlinks (including parent/dangling links),
   replaced worktrees, malformed/future metadata, and pending recovery never
   become menu items. Editing a worktree symlink's source through that link
   can change owned source bytes, but the resolver acts only on the verified
   regular source in `inject/`, never on the symlink.
6. Collect eligible conflicts without suppressing any hard error. Preflight
   every selected feature and role before prompting or mutating, including
   external propagation targets and late hard blockers in a mixed batch.
   A plan with unresolved conflicts is not executable through any caller.

## Interaction, diff, and editor

7. Present conflicts deterministically by feature, role, and relative path.
   Show the owning feature, role, exact escaped destination, source identity,
   current/incoming digests and permission modes. Offer `keep`, `replace`,
   `diff`, `edit`, and `cancel`, with unambiguous single-letter aliases
   `k/r/d/e/c`. Empty input defaults to cancel, not acknowledgement.
8. Keep preserves the exact current bytes/mode and records the acknowledged
   incoming proposal. Replace uses the exact rendered incoming bytes/mode.
   Edit starts from current bytes in a private temporary file, then proposes
   its accepted bytes with the displayed incoming mode. A choice authorizes
   only that observed path/proposal, not later conflicts or the whole role.
9. Diff shows current versus rendered incoming content as a two-way unified
   diff and returns to the same unresolved menu. It neither records consent
   nor modifies assets. Do not imply a three-way base exists. Use plain,
   unpaged output, safe path labels, and escape payload control characters
   other than ordinary newlines/tabs in terminal presentation. Never run
   repository diff drivers, external diff commands, text converters, or
   commands embedded in content.
10. Editor selection is nonempty `VISUAL`, then `EDITOR`, then `vi`. Parse
    executable/arguments with quoting and escaping but without shell
    expansion, command substitution, pipes, or evaluation. Append the
    absolute temporary path as a separate argument; reject malformed commands.
    Run only after an explicit edit choice. Document that GUI editors must
    wait for completion, for example `code --wait`.
11. Stage outside feature/worktree/registry roots in a private directory
    (0700), with private regular files (0600). Expose the exact incoming
    snapshot separately for reference; do not edit live assets, source
    packages, or the ledger. A normal editor atomic-save replacement is
    allowed only after validating the resulting file through the pinned
    private directory: regular, non-symlink, not hard-linked to other files,
    valid text, with safe parents. Never follow editor-created links.
12. Editor exit success does not accept its output. Show a candidate preview
    and require explicit default-No acceptance before recording the choice;
    decline returns to the unresolved menu. Validate native format rules,
    including valid Copilot worktree `applyTo` frontmatter, for kept/edited
    native content. Reject invalid output rather than silently rewriting it.
    Accepted bytes must be exactly the displayed/fingerprinted proposal.
13. During decision collection, EOF, interrupt, cancel, input/output failure,
    editor launch/exit error, or invalid editor output ends the invocation
    nonzero before feature effects. Do not silently select keep or retry
    with another editor. Preserve staged edits on editor or validation
    failure and identify their location; explicit cancellation may discard
    known scratch files after warning. Cleanup removes only exact verified
    scratch entries, never arbitrary editor-created residue.
14. Require terminal input when a new decision is necessary. Piped answers
    are not permission, even with the flag. No keep-all, replace-all, yes-all,
    unattended decision file, or force policy is introduced. A run with no
    decisions to collect may run noninteractively. Ordinary sync without the
    flag may reuse an unchanged remembered override without terminal input;
    explicit review of overrides with the flag requires a terminal.
    Existing removal consent remains the only unattended deletion permission.

## Persistent decision semantics

15. Keep effective owned hash/mode distinct from the acknowledged incoming
    rendered hash/mode. Bind the acknowledgement to feature/role/path and
    saved source identity (kind/scope/name/origin) and rendering selection.
    A hash equal to a known file, a marker comment, or an acknowledgement
    for another role/path never establishes ownership.
16. Save keep/edit acknowledgements only with successfully published
    ownership state. Updating just the effective owned hash is insufficient:
    it would let the next sync overwrite the customization. Store only the
    current decision, not durable base contents or an audit-history store.
17. Ordinary repeat sync preserves acknowledged current bytes when the
    incoming rendered proposal and selection still match. Unrelated package
    file changes or defaults do not invalidate that per-file decision.
    Changed incoming bytes/mode, explicit source/renderer changes, or changed
    local bytes require a new decision. Without the flag or terminal input,
    refuse; never auto-replace merely because the new local hash was saved.
18. Passing the resolver flag includes unchanged remembered overrides in the
    selected roles' review, so the user can choose replace without waiting
    for a template change. Replace clears that file's override. A keep/edit result
    identical to incoming bytes/mode is canonical, not a sticky override.
    Unacknowledged local edits still require a decision even when they happen
    to equal the incoming content.
19. Missing acknowledged customized assets fail closed; do not silently
    recreate the template version and drop the decision. Missing ordinary
    owned files retain rc.5 recreation behavior. A source path removed or
    renamed out of the desired tree while customized remains a hard error:
    preserve it and its acknowledgement, without a keep-deletion tombstone.
20. Read valid v1 state without eager conversion. Persist version 2 only when
    a noncanonical keep/edit decision is first saved. Strictly validate new
    fields, digests, actions, role/path bindings, and version combinations;
    v1 cannot carry hidden v2 decisions. Preserve v2 thereafter, even when its
    last override is cleared. Unknown versions/fields fail closed.
21. Demonstrate that released rc.5 rejects v2 when a managed template writer
    or context reader loads ownership state, rather than falling through to
    legacy behavior or erasing decisions. This is not a universal disable
    switch for old binaries: rc.5 checkout branch creation does not read the
    ledger and can still change branch/stack metadata without changing
    customized template files or acknowledgements. The user approved this
    precise boundary after it was reproduced with the released binary.
    Owner scans may also reject workspace discovery; warn before the first
    v2 decision is accepted and document consistent newer-CLI use. Do not
    forge Git recovery/lock state to block unrelated older commands. This
    slice does not implement downgrade or metadata-stripping tools.

## Consent, concurrency, and preservation

22. Keep decisions in memory until the entire requested batch is resolved.
    Before execution starts, decline/cancel/error leaves feature assets,
    ledgers, saved selections,
    external links, legacy members, and add's branch/session effects
    unapplied. Private scratch is the only pre-consent file output; no
    feature-local resolver state or writer lock is held while waiting on a
    human or editor.
23. Run the existing exact-path, default-No obsolete-removal confirmation
    separately after conflict decisions and before execution. A declined
    removal cancels the entire invocation, including remembered decisions.
    `--allow-managed-removal` and add's existing `--force` do not approve text
    replacement or adoption. Customized obsolete files are hard errors even
    with both consent flags; only ordinary unchanged owned paths are eligible.
24. Bind decisions to original ledger bytes, exact local observations
    (type/hash/mode/device/inode), resolved feature/metadata roots, source
    package evidence, selection, and external target identity. Include these
    and accepted candidate digests in the plan fingerprint. Keep/replacement
    choices requiring only a ledger write still need file observations.
25. Reuse lifecycle, managed writer, and existing external/checkout mutation
    reservations. After all decisions, replan all features under exclusion
    and require the same evidence before any feature applies. Replanning
    cannot discard decisions, broaden their scope, reinterpret keep as
    ordinary template replacement, or prompt while holding reservations.
    Source/destination/ledger/root/target drift aborts rather than silently
    accepting a new snapshot.
26. Recheck acknowledged-but-unwritten files before ledger publication and
    through relevant cleanup checkpoints, not just before acquiring locks.
    In particular, a keep-only metadata write must not adopt a concurrent
    edit or symlink swap. A no-op replace with already-equal bytes requires
    the same evidence checks.
27. Publish accepted replacements and ledger changes through the existing
    preserve-before-mutate protocol, held no-symlink parents, immutable
    intents, and exclusive publication. Never implement the editor path with
    a direct write to a live destination. Automatic cleanup is permitted only
    after verified publication, with the parent's exact-entry limitations.
28. On failure after execution begins, report completed/remaining effects and
    retained recovery locations, including the affected feature in a batch.
    Preserve user/incoming versions and evidence under the existing protocol;
    unresolved recovery blocks retries and context exposure. Do not claim
    cross-feature or all-file atomicity, unconditional rollback, or stronger
    guarantees against late writes through already-open descriptors.

## Integration and verification

29. A saved customization must remain usable by context validation, checkout
    temporary links, external injection, new/open, rename, archive/reactivate,
    and export without another acknowledgement. These paths must preserve
    new metadata and verify effective owned bytes strictly; they never
    launch a resolver. Later unacknowledged edits still fail closed where
    the parent validates managed context. Do not change their unrelated UX,
    public refusal IDs, repository resolution, or Git recovery schemas.
    Export preserves effective asset bytes and the live ledger; retain its
    existing archive omission of ownership metadata rather than promising
    portable acknowledgements. The user explicitly deferred the pre-existing
    materialized external feature-rename/backpointer defect to GitHub #5.
    Preserve decisions across branch rename, archive/reactivation, and
    feature metadata moves, without claiming to repair that existing Git
    backpointer behavior or expanding this feature into its repair.
30. Preserve independent role scopes and native rendering: an orchestrator
    decision cannot affect worktree content, and vice versa. External mode
    retains multi-repository/slash-name worktrees and root-only managed
    injection. Checkout remains explicit, single-repository, one physical
    checkout, with session-owned temporary context rather than permanent
    injection. Do not migrate registry/default configuration.
31. Exercise CLI help, explicit and absent flags, legacy and mixed batches,
    all choices/cancellation paths, repeat/review/changed proposals,
    local and permission drift, empty/non-UTF-8/NUL content, native format,
    missing/obsolete overrides, strict v1/v2 parsing and downgrade refusal.
    Use real temporary repos, local remotes, worktrees, terminal fixtures, and
    deterministic fake editors; do not invoke a person's real editor.
32. Add phase-boundary regressions for source/root/ledger/link changes during
    prompts, editor atomic saves and symlink/hard-link replacements, keep-only
    drift before state publication, partial failures, and bounded cleanup.
    Reuse existing filesystem hooks and fixtures instead of replacing their
    checks with mocks. Preserve all named-template regressions.
33. Update `README.md`, `docs/cheatsheet.md`, `docs/configuration.md`,
    `CHANGELOG.md`, and the three embedded agent guidance surfaces when the
    feature is implemented. Explain explicit opt-in, keep persistence,
    obsolete customization refusal, editor waiting, v2 downgrade limits, and
    noninteractive refusal. Planning handoff updates do not claim the flag
    already exists.
34. Required executable gates before implementation can land include:

    ```bash
    go test ./internal ./internal/cli -run 'Test(ManagedTemplate|NamedTemplate|SyncPlanDocs_|ReparentDocs_NormativeClaims)' -count=1 -timeout 20m
    go test ./... -count=1 -timeout 40m
    go vet ./...
    golangci-lint run ./...
    make build
    git diff --check
    tpatch feature deps --validate-all
    ```

    Format changed Go files, perform real-CLI/terminal smoke cases and the
    repository's scoped review loop, then record/replay/land one feature
    commit. Preserve honest reporting of inherited tpatch verification
    limitations. Implementation is approved; publication still requires
    separate approval. No release tag is chosen by this artifact.

## Smallest implementation sequence

1. Extend strict state/decision semantics and structured planning in
   `internal/template_managed.go`; establish effective-content and drift tests.
2. Add the opt-in shared CLI flow in `internal/cli/managed_template.go`, wired
   only through `internal/cli/add.go` and `internal/cli/template.go`. Isolate
   temporary editing/diff execution behind small test seams.
3. Carry decisions through reservations/replanning and add required
   unwritten-file checkpoints in `internal/template_preserve.go`. Keep the
   existing filesystem and lifecycle mechanisms.
4. Extend existing core/CLI test suites and user/agent docs, complete review
   and gates, then follow the ordinary Path B record/replay/land process.

## Explicit non-goals

No automatic or three-way merging, stored merge bases, AI/provider calls,
unattended conflict policies, legacy directory conflict UI, unmanaged-file
adoption, symlink or permission conflict overrides, customized-file deletion,
recovery bypass, registry redesign, new Git mutation behavior, or downgrade
command, or repair of the separately tracked GitHub #5 feature-rename defect.
