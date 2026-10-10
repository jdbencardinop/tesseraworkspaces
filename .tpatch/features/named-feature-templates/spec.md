# Named, role-aware feature templates

## Boundary

Existing directory backfill cannot independently configure worktree and
orchestrator guidance or prove which files a later refresh may change.
Implement issue #2's named registry and managed role-aware template contract
in `named-feature-templates`. Keep `template-conflict-resolution` separate.
The planning handoff is complete and the user has authorized implementation.
Publication still requires separate approval; no push or tag is authorized.

The user approved opt-in managed behavior, saved per-feature role selections,
removal of obsolete unchanged owned files only after a warning and
default-No confirmation, and root-only injection for managed worktree
guidance in this slice. Consent never overrides ownership or modified-file
checks. Legacy directory copying remains a compatibility path.

During implementation review the user also approved preserve-before-mutate
recovery copies, cleaned up automatically only after a successful update and
verified ownership publication. Conflicts/failures preserve versions and
report their locations. The accepted open-file-descriptor limitation and
required filesystem protocol are specified below.

## CLI and selection acceptance criteria

1. Add `tws template add <name> <path> [--repo]`, `list`, `show <name>`, and
   `remove <name> [--repo]`. Add/remove default to global storage; repository
   operations require an unambiguous owning repository. List/show expose
   origin and effective resolution without modifying files. Duplicate add
   refuses; remove deletes only that registry entry, never feature assets.
   Deliberate remove/add can replace an entry; missing saved entries fail
   rather than falling through to another scope.
2. Copy validated packages into `~/.config/tws/template-registry/<name>/`
   or `<repo>/.tws/.template-registry/<name>/`. The hidden repository
   directory is already excluded by the feature-name rules. Neither path
   is inside legacy `templates/`; adding registry entries cannot make the
   legacy copier inject the registry into every feature. Registering a
   template does not enable checkout mode.
3. References are an unqualified name, `repo:<name>`, or `global:<name>`.
   Unqualified lookup prefers the owning repository over global; explicit
   scope never falls back. Names are single non-hidden components, cannot
   contain separators, traversal, colons, or control characters, and must
   not alias another entry under the destination filesystem's semantics.
   Registry copies are independent of the original source directory.
4. Add independent `--worktree-template` and `--orchestrator-template`
   reference flags to feature creation and template synchronization.
   Add `worktree_template` and `orchestrator_template` config keys to
   global/per-repository merging, show/get/set, help, and completion.
   Config set validates reference syntax; use-time resolution validates
   the actual source and reports malformed/unreadable config explicitly.
5. Initial selection is explicit flag, then repository default, then global
   default, then built-in native role guidance. Role defaults opt newly
   created features into managed mode. They must not implicitly migrate
   an existing legacy feature on add or sync. Explicit legacy directory
   inputs still choose the legacy route on an unmanaged feature even when
   role defaults exist; ignored defaults are not mixed explicit selectors.
6. Persist each configured role's resolved source, scope/origin, and rendering
   agent. For an existing managed role, precedence is explicit selection,
   then saved selection; defaults only fill selected, unconfigured roles.
   Ordinary sync refreshes the registered copy at that same source. A new
   repository entry cannot shadow a previously selected global source.
   Moving/removing a recorded origin requires explicit reselection, not
   heuristic rebinding.
7. `tws add <feature> --template <name>` remains the requested convenient
   named-package selection for both roles. An existing directory argument
   still means legacy flat copying, including bare relative directories.
   Explicit path syntax stays directory-valued; otherwise a valid bare
   value can resolve as a registry name. Unknown registry references fail
   clearly. Use scope-qualified names to disambiguate a registry entry
   from an existing directory.
8. Preserve repeated directory-valued `--template` inputs and their current
   default-first, first-existing-file-wins ordering. Reject mixing legacy
   directories with named/role selections, multiple named package aliases,
   or the named alias with role flags before mutation. A managed feature
   cannot be silently downgraded through a legacy invocation.
9. Add `tws template sync <feature> --scope worktree|orchestrator|both`.
   A managed feature defaults to `both`; a legacy feature without an
   explicit managed selector keeps the existing worktree-only backfill.
   An explicit role/scope selector opts a legacy feature into the managed
   planner but does not adopt its existing files. A role flag outside the
   selected scope is an error. An unselected role's files, selection, and
   ownership evidence remain untouched. Explicit rendering-agent selection
   also opts into managed planning; it cannot be combined with legacy
   directory copying. A removal-consent flag alone never migrates a feature.
10. Preserve `--all` and feature-name/registered-space guards. Resolve all
    selected managed plans before executing any part of a mixed batch,
    including legacy features. A managed preflight conflict or declined
    removal prompt leaves the entire batch unapplied. Deterministic runtime
    I/O failures after execution starts are reported with per-feature
    progress; do not claim cross-feature filesystem atomicity.

## Package and agent contract

11. A managed package has `feature/` assets for the orchestrator role and
    `inject/` assets for the worktree role. Either subtree may be absent.
    Optional `README.md` and `template.yaml` are package metadata, not
    injected files. Reject unsupported package entries, special files, and
    symlinks rather than silently following or dropping them. Packages
    contain data only: no shell evaluation, executable hooks, network
    fetching, or template-language execution.
12. Optional `template.yaml` has version 1, literal Markdown guidance by
    role, and skill references. A minimal example is:

    ```yaml
    version: 1
    guidance:
      worktree: Implement and test only the selected stack entry.
      orchestrator: Coordinate the feature without editing unrelated stacks.
    skills:
      - name: code-review
        roles: [worktree]
        agents: [claude, copilot]
    ```

    Validate keys, types, duplicate entries, roles, agent values, and schema
    version. Missing manifest means no extra guidance/references. Skill
    agents may be omitted to select both supported agents; roles are
    explicit. Unknown versions or malformed manifests refuse before writes.
13. Filter skill references by the selected role and rendering agent;
    render names/invocation guidance deterministically. Never copy, install,
    search for, or embed canonical skill implementations from agent stores.
    A reference is not proof of installation or successful activation.
14. Support rendering agents `claude` and `copilot`. A role's initial agent
    comes from explicit `--agent` or a recognized executable in the resolved
    `agent_command`; preserve established Claude aliases. Unknown wrappers
    or custom agents require explicit supported `--agent`, not substring
    guessing or executing the command. Store the result. If the configured
    agent later differs, report the mismatch and require explicit
    reselection rather than silently changing saved guidance. The flag
    selects rendering only; it never changes launch configuration.
15. Claude worktree guidance uses `inject/CLAUDE.local.md`; Claude
    orchestrator guidance uses feature-root `CLAUDE.md`. Copilot worktree
    guidance uses `inject/.github/instructions/tws-worktree.instructions.md`
    with valid `applyTo` frontmatter; Copilot orchestrator guidance uses
    feature-root `AGENTS.md`. Use role-appropriate native prose, not a prompt
    file's unrelated `mode/tools` header.
16. The selected template's file at a native guidance destination, if any,
    provides that role's base guidance instead of the built-in body.
    Manifest guidance/references are rendered into that single planned
    output. Do not merge with a pre-existing destination file or permit two
    unrelated sources to claim the same path. Other assets are copied
    literally within their selected role. Missing template payload still
    permits built-in native guidance.
17. Native worktree guidance must appear at native repository-root paths.
    This slice does not introduce per-file injection routing: managed
    worktree operations refuse non-root `inject_into` with actionable
    guidance before writes. Orchestrator-only operations are unaffected.
    Preserve legacy custom-target injection unchanged; do not silently
    change repository config or claim discovery from a relocated directory.

## Ownership, writes, and deletion consent

18. Store versioned managed state in feature-root `.tws-templates.json`,
    outside `inject/`. It records role selections and owned paths, including
    regular-file content digests/modes and external symlink filesystem
    identity, exact Git-worktree identity, source mapping, and target.
    Paths are relative to their validated feature/worktree roots; never
    trust an arbitrary absolute destination loaded from metadata.
19. A file is owned only when the ledger records its creation/application
    and current type/content/mode match the recorded evidence. Matching
    content or a generated comment alone cannot adopt an existing file.
    Missing, malformed, future-version, symlinked, or ambiguous ownership
    metadata cannot authorize replacement/removal. Absent metadata makes
    existing assets unmanaged, including older TWS-generated assets.
20. Refuse unmanaged destination files or symlinks, modified owned files,
    changed link targets, conflicting path types, and duplicate ownership.
    An unmanaged same-content file is still a conflict. Never truncate or
    follow a dangling symlink to make the write succeed.
21. Bound all source/destination operations to verified roots. Validate
    parent components as well as leaves, pin directory identities through
    effects, and recheck against concurrent replacement. Enumerate and read
    packages through the same rooted filesystem. Reject `.git`,
    stack/decision/config files, workspace
    runtime state, ownership/lock metadata, `worktrees/`, and other
    tool-owned control paths as package asset destinations. The orchestrator
    payload cannot reach `inject/`; the worktree payload cannot reach the
    feature root. Do not rely on textual path-prefix checks alone.
22. Plan desired writes and obsolete owned paths for selected roles. Missing
    desired files may be recreated safely; files no longer desired may be
    removed only with unchanged recorded evidence and the consent below.
    Validate every proposed removal before asking. Conflicts are errors,
    not choices offered by the prompt.
23. Before any asset/selection/ownership write, print a deterministic warning
    to stderr listing every proposed deletion with feature, role, exact
    resolved path, and file/link kind. Include owned external injection
    links being retired, not just their sources. Then ask:

    ```text
    Remove these unchanged TWS-managed files and apply the template update? [y/N]
    ```

    Only an affirmative `y`/`yes` proceeds. No, empty input, EOF, or
    cancellation retains all files and leaves the entire requested update
    unapplied, including new files and saved selections. Return nonzero with
    a clear cancellation message; input errors remain explicit errors.
24. For automation, `--allow-managed-removal` on add/sync supplies consent
    only for eligible obsolete managed paths in this invocation. Always
    print the same exact-path warning even with this flag. Without a TTY
    and without the flag, refuse instead of blocking on stdin or silently
    deleting. Operations with no removals need no prompt.
25. Revalidate the exact plan after consent, under writer exclusion, and
    immediately before filesystem effects. Drift in sources, destinations,
    metadata, worktree identity, or removal sets invalidates consent and
    refuses; never expand the approved deletion set. A yes answer or
    automation flag cannot override a changed/edited/unmanaged target.
26. Use error-returning helpers and Cobra `RunE`; emit useful source,
    destination, role, and conflict diagnostics. Managed sync must not print
    an error and exit successfully. Stage regular-file writes safely and
    publish ownership state atomically; keep evidence until effects succeed.
    Do not promise atomicity across all assets, metadata, and worktree links.
27. On a mid-apply failure, return nonzero, identify completed/remaining
    effects, and preserve evidence. An interrupted write or failed ownership
    publication must make retry fail closed on drift/unmanaged outputs,
    never silently adopt the partially written files. Do not use broad
    cleanup, destructive rollback, or a new Git recovery envelope.
28. Serialize managed writers and preserve existing mutation/session
    exclusions. Do not refresh/remove checkout context during an active or
    uncertain checkout session, or bypass recoverable sync/reparent state.
    Establish/recheck the applicable reservation before effects; a
    preflight-only race check is insufficient. Keep Git recovery schemas
    and existing commands' normal behavior unchanged.

## Mode, lifecycle, and compatibility acceptance criteria

29. Both workspace modes store durable assets only in the execution-resolved
    feature directory. Preserve new/legacy checkout layout ambiguity errors,
    registered-space protection, environment/config root selection, and
    explicit mode dispatch. Resolve the controlling repository once;
    per-worktree cwd or a secondary repository must not silently select a
    different feature-wide default/registry.
30. External propagation uses the shipped verified worktree discovery:
    flat/slash logical names, decoupled Git names, multi-repository entries,
    archived/missing worktrees, and malformed metadata keep their established
    semantics. Never inject into an intermediate directory or an unverified
    copied `.git` marker.
31. Record only external links actually created by the managed path.
    A recorded link is reusable/removable only if its identity/target still
    match, its Git worktree is verified, and it is not tracked by Git.
    Existing unrecorded links remain unmanaged, even if their targets match.
    Require its source to be an owned role file and its path/target to match
    the generated mapping. A recreated same-target link is not the originally
    owned link. Query Git paths literally, never as pathspec patterns.
    Creation and removal include safe parent/leaf checks; missing or
    unavailable Git evidence cannot mean "untracked".
32. Wire managed link creation through new/open/inject and template sync,
    not only feature creation: later materialized worktrees must not produce
    unowned links that the next sync cannot safely recognize. Non-template
    user inject assets retain their existing behavior. Retiring an owned
    source must not leave its owned materialized links dangling.
33. Checkout add/sync stages assets under `.tws/features/<feature>` or its
    valid existing layout, never injects permanently into the single physical
    checkout, and never creates linked worktrees or switches branches.
    Existing session-owned temporary links remain owned by the session
    lifecycle, not the template ledger. Standalone checkout `tws inject`
    remains refused.
34. Managed add preflights before the legacy external `FEATURE.md` write.
    Create a default feature document only if missing and not supplied by
    the selected payload. Do not overwrite an authored feature document.
    Existing add/new/open flags retain their Git/session meaning, and
    declined template consent cannot create a branch or launch an agent.
35. Relative feature state survives feature rename and layout migration.
    Registry-source relocation requires explicit reselection when the saved
    origin is unavailable; do not infer a substitute. Preserve existing
    export formats: they do not transfer registry selection/ownership or
    orchestrator assets. Document that limitation, surface it when exporting
    a managed feature, and treat imported inject files as unmanaged.
36. Legacy directory routes keep valid path selection, copy-if-missing
    ordering, custom injection targets, no-flag asset layout, and established
    diagnostics. Do not weaken existing space-guard or injection regression
    tests to force the new managed behavior onto legacy invocations.

## Implementation and evidence

37. First implement the shared reference/package/state planner and ownership
    checks; then wire registry/config and role flags; then native rendering
    and mode-aware materialization; then deletion consent and failure paths.
    Keep these steps in one named-feature boundary, without implementing the
    interactive conflict editor.
38. Add real temporary Git repository/worktree fixtures with isolated HOME
    and explicit Git identity. Cover both modes and invocation locations,
    scoped/global registry shadowing, persisted choices, copied-source
    isolation, duplicate/missing names, config errors, role combinations,
    legacy repeated directories, and mixed-batch preflight.
39. Cover Claude/Copilot native destinations, configured/explicit/custom
    agents, role-filtered manifests, malformed/unknown metadata, no skill
    body copying, and non-root managed injection refusal.
40. Cover unmanaged same-content files, modified owned files, dangling and
    parent symlinks, tracked/deleted Git paths, reserved destinations,
    exact-link ownership, source removal, template/agent switches, later
    new/open/inject materialization, and checkpoint I/O failures.
41. Exercise deletion warning contents and ordering, default No, yes, no,
    empty input, EOF, read error, non-TTY refusal, explicit automation
    consent, no-removal/no-prompt, mixed-batch cancellation, and post-consent
    drift. Assert snapshots of assets/selections/refs are unchanged on
    refusal. Every unsafe-ownership case must refuse even with consent.
42. Preserve checkout session cleanup and concurrency, external nested
    worktree discovery, feature-space guards, rename/migration, and export
    exclusion coverage. Test the actual filesystem/Git effects, not only
    printed success messages.
43. Update CLI help/completion, README, cheatsheet, CHANGELOG under
    unreleased, and the existing embedded Claude/Copilot skill documents.
    Explain managed opt-in, saved selections, root-only native guidance,
    ownership conflicts, exact-path consent, cancellation, unattended use,
    legacy behavior, and export limitations.
44. Focused implementation command:
    `go test ./internal ./internal/cli -run 'TestNamedTemplate|TestManagedTemplate|TestTemplate|TestInject|TestCheckoutAdd|TestSpaceGuard|TestSession|TestSyncPlanDocs_PlanningProse' -count=1`.
    The first two prefixes denote tests to add, not existing passing evidence.
    Before landing, run the required full tests, vet, lint, build, formatting,
    dependency validation, diff checks, and exact recipe replay.

## Approved filesystem preservation contract

45. POSIX does not provide portable rename/unlink conditioned on an expected
    inode or content hash. Do not claim such a compare-and-swap guarantee.
    Pin feature, source, registry, and worktree roots/parents and verify their
    exact filesystem/Git identities rather than accepting another valid
    directory at the same pathname. Managed writers and registry operations
    need atomic ownership/exclusion, not stale-record check-then-unlink
    reclamation. Use existing OS-backed locking patterns where suitable;
    release errors must be surfaced.
46. Publish a previously absent file with true no-replace semantics, such
    as linking a fully staged regular file into the absent destination.
    Stage symlinks privately so their created identity can be captured before
    exclusive publication. A concurrently appearing destination must never
    be overwritten or adopted. Ledger creation/publication has the same
    ownership requirements as asset effects.
47. Before replacing or removing an existing owned object, persist bounded
    pending-operation evidence and move the object to a unique same-filesystem
    recovery name with exclusive/no-replace rename. Inspect the captured
    object's identity, type, digest/mode or target without following links.
    On mismatch, restore only if the original destination remains absent;
    otherwise preserve both paths and fail. Publish replacements only into
    absent destinations. Never overwrite a concurrently recreated path.
    Linux/macOS primitives must fail closed when unsupported; ordinary
    overwriting rename is not a fallback. This is template filesystem
    evidence, not a new Git sync/reparent recovery envelope.
48. The user chose automatic cleanup after success: remove only verified
    old recovery copies after the desired assets and ownership state were
    published successfully. Revalidate before cleanup; observed edits,
    mismatches, or failures preserve objects/evidence and report exact
    locations. No automatic retry may adopt partial outputs, discard a
    pending record, or overwrite later user work. Keep private staging,
    recovery copies, and pending metadata out of injection, package inputs,
    and exports. Existing default-No consent for obsolete active paths is
    unchanged. Document that this protocol does not protect writes arriving
    later through an already-open descriptor to an old object after its
    final successful verification, and does not provide multi-file atomicity
    or prevent non-cooperating edits after a completed operation.
49. Add deterministic tests at the final filesystem boundaries: another
    valid worktree or registry root replaces a recorded root; a new/edited
    leaf or ledger appears after observation; a same-target symlink has a
    new identity; two stale-lock contenders race; native case aliases are
    nonadjacent in lexical order; a native file conflicts with a descendant;
    and a tracked-but-deleted name contains Git pathspec magic. Assert no
    clobbering, exact conflict/progress reporting, preservation on failure,
    successful verified cleanup, and refusal of unsupported primitives.

## Non-goals

- Interactive keep/replace/diff/edit, automatic ownership adoption, or a
  force flag that overrides user files.
- Automatic migration of legacy features, arbitrary template execution,
  remote registries, a package manager, or skill-body distribution.
- New Git topology, session/runtime providers, semantic status, hooks
  installation, permanent checkout injection, or changes to published
  sync/reparent recovery contracts.
- Per-file injection routing, a new portable export schema, full filesystem
  transactions, or historical tpatch artifact repair.
