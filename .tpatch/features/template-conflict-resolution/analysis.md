# Template conflict resolution: analysis

## Summary and baseline

Plan a separate, opt-in interactive resolver on top of the managed template
contract released at `9c634f2a0e8c9e89e620060ec24a6a1df736f446`
(`v1.2.18-rc.5`). Main and tag CI passed on Ubuntu/macOS, the prerelease was
published, and issue #2 was closed. Its acceptance required safe refusal,
not this editor. The original #4, #1, #3, #2 queue is complete.

The resolver is not already implemented in this baseline. Manual analyze,
define, and explore are complete, and the user has authorized Path B
implementation. Follow the required review, validation, record/replay, and
single-feature landing process. Publication requires separate approval;
do not push or tag.

## Current behavior and relevant constraints

- `internal/cli/add.go` and `internal/cli/template.go` distinguish named
  managed selection from directory-based copy-if-missing. Existing defaults
  never silently migrate an old feature. Preserve that dispatch.
- `internal/template_managed.go:planManagedRoleFiles` rejects unmanaged
  destinations and any present owned file whose hash/type/mode differs.
  This rejection happens before deletion consent or filesystem writes.
  It currently returns errors, not selectable conflict records.
- `ManagedTemplateOwnedFile` stores a hash and mode, not old file contents.
  `ManagedTemplateRoleState` persists source origin and rendering agent.
  There is no base snapshot for a truthful automatic three-way merge.
- `matchOwnedFile` also protects context exposure, links-only injection,
  retirement, and rename recovery. Weakening it globally would let a launch
  or lifecycle command acknowledge edits without the user's decision.
- `.tws-templates.json` uses version 1 with strict decoding.
  A kept customization cannot be represented solely by updating its owned
  hash: ordinary sync would regard it as safely replaceable on the next run.
  A sidecar ignored by rc.5 would have the same downgrade hazard.
- `confirmManagedTemplateRemovals` lists exact obsolete paths and defaults
  to No. `executeManagedTemplatePlansWithReservations` acquires existing
  lifecycle/mode reservations, replans every feature, then starts effects.
  All selected features are preflighted before legacy batch work.
- `internal/template_preserve.go` preserves old objects before mutation,
  publishes without replacing concurrent paths, and cleans only verified
  owned copies after success. Pending recovery blocks retry even if the
  active ledger is missing. Keep-only acknowledgements must gain equivalent
  before-publication checks despite not writing the kept asset.

## User decisions

The first slice handles managed-owned regular text files only, in worktree
or orchestrator assets. Legacy directory templates remain unchanged.
Unmanaged files, symlinks, permission drift, invalid roots/ownership, tracked
injection collisions, and unresolved recovery are not override candidates.

Keep/edit remembers the acknowledged local bytes and the incoming rendered
proposal. An unchanged repeat sync preserves the customization; changed
incoming content or a later unacknowledged local edit needs a new decision.
The user approved a newer ownership format only when a remembered decision
is saved. Older rc.5 must fail closed, and may refuse workspace discovery
when scanning newer ledgers. Use the newer CLI consistently in that workspace.

The user selected explicit interactive opt-in on add/sync rather than
automatic prompting or unattended conflict policies. Diff is two-way;
editing happens on a temporary copy and requires explicit acceptance, not
automatic merging. Customized-file deletion is excluded, including when an
upstream template removes a previously kept/edited path.

## Compatibility and implementation approach

No language or toolchain upgrade is required. The intentional compatibility
change is the fail-closed v2 ownership format for remembered decisions, not
a change to workspace mode, Git state, or legacy template storage.

Separate conflict discovery, user decisions, and guarded execution. Preserve
the existing refusal route by default. Discover all hard blockers before
asking for decisions; do not convert arbitrary planner errors into prompts.
The core remains noninteractive, while Cobra owns terminal input/output and
editor invocation.

Accept v1 ledgers without eager migration. A v2 file record can retain the
effective owned hash/mode plus an optional keep/edit acknowledgement bound
to the incoming rendered hash/mode and its role's source/renderer selection.
The spec freezes semantics, not unnecessary redundant storage fields.
Once upgraded, do not silently downgrade. The old strict reader must
demonstrably reject v2 instead of falling back to legacy behavior.

Carry each decision's exact file observation, ledger/source evidence,
role/path, and resolved roots through locked replanning and publication.
Kept files are still strict owned content after acknowledgement; subsequent
unacknowledged edits remain errors for all non-resolver callers. A matching
file without ledger ownership never becomes eligible, even if the user
selects replace.

Native Copilot instructions must retain valid `applyTo` frontmatter.
Do not silently rerender or append guidance to a user-accepted edit, or
weaken native format requirements to make acceptance succeed.

## Dependencies and risks

Retain the hard parent `template-sync-existing`; add the now-shipped
`named-feature-templates` as a hard parent. First remove the obsolete reverse
soft hint from `named-feature-templates` to this feature, otherwise the new
edge creates a cycle. That old hint anticipated interaction but did not block
the safe-refusal release. This is a truthful dependency correction, not
historical patch repair. Leave published patches and validation receipts
unchanged.

The inherited historical canonical-patch verification limitation through
`fix-inject-slash-worktree-discovery` remains maintenance work under
`repair-historical-tpatch-artifacts`. Keep DAG validation distinct from
canonical-patch verification; do not remove real parents to get green output.

Primary risks are acknowledging a stale file, dropping a remembered override
on ordinary sync, losing staged edits on error, leaking human consent across
features, accepting an unsafe editor replacement, and changing lifecycle or
legacy semantics. Test them with real filesystem/Git fixtures and deterministic
failure seams. Do not claim multi-file or cross-feature atomicity, portable
inode-conditioned compare-and-swap, or stronger open-descriptor guarantees
than the parent feature.

## Confirmed inherited boundaries

Independent real-binary checks during implementation confirmed that rc.5
checkout `new` does not read template ownership state: it may create a
logical branch/stack entry while preserving all customized template bytes
and ledger data. The user approved documenting the guard precisely as a
managed-state reader/writer boundary, not a universal old-binary disable
switch. Continue requiring consistent newer-CLI use in affected workspaces;
do not invent persistent Git mutation locks to broaden the guarantee.

The same unchanged rc.5 binary reproduces stale Git backpointers after
renaming an external feature with materialized worktrees. The user approved
deferring that existing defect and requested a backlog issue:
[GitHub #5](https://github.com/jdbencardinop/tesseraworkspaces/issues/5).
This feature preserves acknowledgement data through existing lifecycle
operations but does not claim to repair that compound rename/archive flow.
Export likewise retains its established archive omission of ownership
metadata while preserving effective asset bytes and the live ledger.

## Non-goals

No legacy conflict editor, unowned-file adoption, force-overwrite escape,
symlink resolution, binary/permission conflict resolution, customized-file
deletion, source-missing keep tombstones, automatic three-way merge, content
history store, editor installation, AI/provider integration, registry redesign,
Git recovery changes, or reopening issue #2.
