# Engineering workflow

This document is the durable implementation contract for human and coding-agent work on tesseraworkspaces.

## Current product direction

`tws` supports two explicit workspace modes:

- **external** — existing sibling workspace with linked Git worktrees; default and backward compatible.
- **checkout** — repository-local `.tws/features/` metadata and one physical Git checkout.

Current shipped checkout slices:

1. workspace mode foundation;
2. lifecycle and logical branches;
3. transactional stack sync;
4. namespaced feature layout with legacy migration;
5. single-owner direct/tmux agent sessions;
6. checkout doctor and list observability;
7. workspace sibling links (`<spaces-root>/spaces.yaml`, `tws space add/list/show/remove`);
8. agent work status (`tws status [feature] [--json]`) plus external per-invocation direct session records;
9. mode-independent stack ancestry evaluation shared by checkout doctor, checkout list, and external `tws doctor`;
10. stack status (`tws stack status <feature> [--json]`) projecting ancestry, materialization, upstream, and parent counts;
11. sync modes (`tws sync <feature>` with `--fetch`/`--no-fetch`,
    `--full`/`--local-only`, and `--only`/`--from`) carrying the frozen mode,
    scope, push, validation, and recovery decision through `--continue` and
    `--abort` in both workspace modes, with the no-flag run unchanged;
12. rebase plan guard: a preview-and-approve workflow for stack sync that
    shows the old base, new base, and a replay-candidate count per entry
    before anything moves, refuses execution above an operator-set replay
    bound without an approval matching the previewed plan's fingerprint,
    records a guarded run's bound in recovery state so an older release
    refuses to resume it unguarded, and reports a guard refusal as one
    distinguishable line separate from every refusal tws already performed;
13. safe reparent/restack: `tws stack reparent <feature> <entry> --onto <dest>`
    moves one entry onto a new parent and replays its descendant closure,
    committing every moved branch in one compare-and-swap ref transaction and
    writing the post-image `stack.yaml` durably. A preview route describes the
    run, moves no branch and writes no tws state; may fetch according to policy. A fresh execution is
    always guarded, requiring the fingerprint that preview printed plus a
    replay bound; the resume and rollback verbs take the whole frozen decision
    from persisted state and never accept an approval token. The ref commit is
    race-atomic and crash-atomic only on the reftable backend, there is exactly
    one commit point, and recovery past it is forward-only. Git refs,
    `stack.yaml` metadata, and worktree/index state are three separate effects;
    runtime state is durable recovery evidence, not a fourth effect. No remote
    ref or pull request is ever changed. External direct/tmux/all launches publish
    intent before their final mutation check, and top-level external push holds the
    shared feature mutation lock, preventing session or multi-entry push races
    with reparent. Every mutating external sync route holds that lock through
    rebase, metadata, optional push and remote follow-up clearing, rechecks
    reparent state after claiming, and releases last. Checkout feature-directory
    opens retain the workspace launch intent through the agent/shell after a
    final mutation/reparent/session check; differently spelled repository aliases
    on one edge are refused. A stack-entry destination stores its logical
    entry name, a named literal ref stores its full `refs/...` name, and a raw
    object id stores the full lowercase OID. If the checkout-global lock is
    absent, current tws scans every feature's recoverable checkout sync/reparent
    state before fresh mutation; recovery reconstructs only its own reservation.
    v1.2.16 only sees same-feature
    compatibility after envelope birth; do not use an older tws while any
    reparent is active or recoverable.

The opt-in global workspace registry is also shipped for stable cross-repository
discovery, health checks, and moved-target repair.

Next roadmap work: **scoped standalone push (#6)** — Path B implementation candidate (unreleased).
The separate `scoped-push-selection` boundary adds logical entry/subtree
publication in external and checkout modes, with selected-only dry-run and
durable continuation, while preserving unscoped behavior. Existing scoped
sync-plus-push already publishes its selected scope; do not duplicate its
selector or fabricate a sync transaction for standalone publication.
Implementation and RC publication are authorized through the established
independent review, frozen-candidate gates, record/replay, and landing process.
External feature rename (#5) is fully released and verified in
`v1.2.18-rc.7` at `d38b442` under
[`fix-external-feature-rename-backpointers`](../.tpatch/features/fix-external-feature-rename-backpointers/request.md).
Issue #5 is closed after successful Ubuntu/macOS main and tag/release CI.
Template conflict resolution is fully released and verified in
`v1.2.18-rc.6` at `5b531a8`: explicit interactive resolution of managed-owned
regular text files, remembered keep/edit decisions, and two-way staged editing.
Role-aware templates (#2) are fully released and verified in
`v1.2.18-rc.5` at `9c634f2`: independent worktree/orchestrator selections,
global/per-repository defaults, scoped synchronization, native agent guidance,
and ownership-tracked file updates.
Scoped status projection (#3) is fully released and verified in
`v1.2.18-rc.4`; named status builds only the requested feature plus shared
workspace evidence, with bounded probes and the existing versioned schema.
Ordinary sync safety (#4) is fully released and verified in `v1.2.18-rc.2`.
Slash-containing worktree injection (#1) is fully released and verified in
`v1.2.18-rc.3`, including explicit checkout injection refusal. All six
milestones have successful main/tag CI, and issues #4, #1, #3, #2, and #5 are closed.
Preserve those status,
sync/reparent/session, and injection behaviors.

The shipped `named-feature-templates` boundary is complete; issue #2 required
safe conflict refusal, not the deferred interactive editor. Legacy
directory-based templates retain copy-if-missing behavior; named templates
or role flags opt into managed updates with saved per-feature selections.
Managed updates refuse unmanaged files or symlinks. Modified owned content
still refuses by default unless eligible for explicit text conflict resolution.
Removing obsolete, unchanged owned files requires an exact-path
warning and default-No confirmation, or explicit unattended consent;
declining leaves the update unapplied. This narrow deletion consent is not
interactive conflict resolution.

Keep `template-conflict-resolution` a separate feature, not unfinished #2 work.
The shipped resolver excludes legacy templates, unmanaged collisions,
symlinks, permission drift, and unresolved recovery. It remembers keep/edit only
for the acknowledged local content and incoming rendered proposal; changed
template content or an unacknowledged local edit requires another decision.
Customized-file deletion remains out of scope, including when its template
source disappears. Versioned decision metadata makes older managed-state
readers/writers refuse rather than overwrite customizations; workspace owner
discovery may also refuse. This is not a universal disable switch: rc.5
checkout `tws new` may still create logical branch/stack metadata without
reading the ledger, leaving template bytes and acknowledgements unchanged.
Affected workspaces require consistent use of the newer CLI; do not forge Git
locks or recovery state to block unrelated older commands. The
[analysis](../.tpatch/features/template-conflict-resolution/analysis.md),
[specification](../.tpatch/features/template-conflict-resolution/spec.md), and
[exploration](../.tpatch/features/template-conflict-resolution/exploration.md)
define the approved implementation boundary. The resolver carries decisions
through locked replanning and checks unwritten accepted files before ledger
publication and cleanup. Existing v1 is not eagerly converted; a first
noncanonical decision saves v2, with no automatic downgrade. Explicit review
includes unchanged overrides and requires a terminal; editor exit is not
consent. The release completed the required reviews, verification, landing,
and Ubuntu/macOS main and tag CI. Keep the versioned tpatch artifacts as the
historical implementation record rather than rewriting them after publication.
See [`roadmap.md`](roadmap.md).

The shipped #5 fix preserves ordinary dirty work and updates only verified pointer
files for future external renames. Repository-wide Git repair is excluded
because even an explicit path can repair unrelated siblings. Success requires
complete Git links/registrations at the final location; interruptions use
journal-bound continuation/abort, with automatic rollback only when exact
checks prove it safe. General repair of older broken layouts and repository
or populated-submodule relocation remain outside this boundary. Directory
movement, pointer publication, and spaces publication are separate effects,
with no all-file/cross-repository atomicity or snapshot-isolation claim.
Completion is durable only after Git and registry postconditions; afterward
continuation is verified cleanup only, never rollback. Unknown residue,
reoccupied names, and foreign edits retain evidence and refuse. Old/new name
admission is held through effects and launches; unrelated features remain
usable, while unresolved registry ownership can serialize registry writers.
Do not use an older binary while rename is active or recoverable. This journal
is independently typed, not a sync/reparent/template recovery compatibility
envelope. Pinned identities and exclusive publication do not promise portable
inode-CAS or preservation of later writes through already-open descriptors.

Published v1.2.18-rc.6 limitation: materialized external feature rename can leave
stale Git backpointers ([#5](https://github.com/jdbencardinop/tesseraworkspaces/issues/5)).
The external rename fix released in v1.2.18-rc.7 covers future renames without re-rendering templates
or changing v1/v2 ownership/link identities and acknowledgements. A later
explicit template sync may need new consent for guidance embedding the new
feature name. It does not repair older broken layouts. Export retains its established omission
of ledger data from archives while preserving effective bytes and the live ledger.

The approved #6 scope publishes selected active refs from verified repositories
without requiring materialized worktrees, creating worktrees, or switching the
checkout. Existing no-selector behavior remains unchanged, including the
standalone checkout refusal. Its new publication state must be independently
typed, freeze selected identities and lease authority, preserve confirmed
successes, and never retry with an unguarded or feature-wide push. Cancellation
abandons remaining publication, not already-published remote refs. Dry-run is
local-only with honest unknown remote state. Preserve rename admission across
selected-root fallback and all effects; do not probe unrelated worktrees or
clear their reparent follow-up rows.

The candidate uses its own strict version-one publication journal, shared
feature/checkout mutation reservations, exact token-owned object protection,
and explicit continuation/cancellation. It keeps terminal receipts so visible
outcomes can repeat their durability barriers. Pending state excludes current
mutators even after process death or lock removal; it is not a fabricated sync
transaction and does not promise universal old-binary blocking. Independent
review, replay, landing and CI publication still precede release of #6.

## Tpatch workflow

Use tpatch as structured patch management, not primarily as a generator.

1. Check `tpatch status <slug>` and `tpatch next <slug>`.
2. Register with an explicit clean slug.
3. Add hard/soft dependencies and run `tpatch feature deps --validate-all`.
4. Author concise Path B artifacts:
   - `analysis.md`
   - `spec.md`
   - `exploration.md`
5. Advance phases with `--manual` — the coding agent is the provider.
6. Mark implementation started.
7. Implement and test without committing.
8. Preview with `tpatch land <slug> --dry-run`.
9. Land with `tpatch land <slug>` for one commit with feature trailers.
10. Run `tpatch verify <slug>` and record known historical verifier limitations honestly.

Do not batch unrelated features into one record range. Override collisions only with a precise understood reason.

## Implementation and review loop

For each feature:

1. Dispatch one isolated Opus implementer with exact scope, files, tests, compatibility rules, and non-goals.
2. Parent agent audits the returned diff before importing it.
3. Run focused and full gates.
4. Dispatch three isolated Opus reviewers:
   - code quality/security;
   - architecture/correctness/tests;
   - CLI UX/discoverability.
5. Consolidate only blocking findings; non-blocking observations may become backlog items.
6. Dispatch a revision implementer with exact findings.
7. Repeat reviews until all blockers are approved.
8. Run final gates, land, push, and tag the next patch version.

Review agents never commit, push, tag, or modify unrelated files.

## Required gates

Before landing:

```bash
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all
```

Also run feature-specific CLI smoke tests. Git behavior requires real temporary repositories and local bare remotes; do not rely only on mocks.

After landing, rerun full gates and check a clean tree before push/tag.

## Coding conventions

- Prefer error-returning helpers and Cobra `RunE`; avoid `os.Exit` or fatal helpers inside reusable logic.
- Validate all system boundaries before mutation.
- Persist transaction/session state atomically before irreversible Git actions.
- Metadata changes happen only after Git success, with rollback where practical.
- Never use `git reset --hard`, force push, or destructive cleanup as a shortcut.
- A destructive recovery command must name exactly what it clears or discards;
  it must never restore or resume state on the caller's behalf, and it must
  never claim to have removed something it did not find.
- Use `--force-with-lease` for rewritten branches.
- Keep external and checkout mode dispatch explicit; do not infer checkout mode merely from `.tws/` existence.
- Preserve external-mode paths and behavior with regression tests.
- In checkout mode, reject dirty/detached/concurrent operations instead of auto-stashing.
- Use `StackEntry.Name` for tws identity and `StackEntry.GitBranch()` for Git operations.
- Use repo-scoped base resolution. Explicit refs are literal; omitted base uses selected repo `origin/HEAD`.
- Agent skills/documentation must be updated when user-facing behavior changes.

## Release cadence

- Each approved landed feature gets the next patch tag unless it changes incompatible storage/semantics; use an RC in that case.
- Tag only after push-ready clean-tree audit and full gates.
- Verify the built binary reports the exact tag.
- Watch GitHub Actions for main and tag runs before starting dependent work when CI behavior changed.

## Known tpatch metadata maintenance

Some old bundled features have canonical patches that fail dependency-closure verification while recipes replay and product tests pass. This is tracked by `repair-historical-tpatch-artifacts` and intentionally deferred until tpatch is upgraded.

Do not distort dependencies merely to silence verification. Repair topologically and preserve append-only history.
