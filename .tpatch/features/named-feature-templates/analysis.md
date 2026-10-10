# Analysis: named, role-aware feature templates

## Baseline and compatibility verdict

Issue [#2](https://github.com/jdbencardinop/tesseraworkspaces/issues/2) is the
next approved product feature after #4, #1, and #3. The baseline is
`v1.2.18-rc.4`, `947032d1e7e1014dbc6a4f0f5e7de4611db02b03`. Those issues are
closed and their releases verified. This is planning only: no template
implementation, migration, release, or provider integration is claimed.

The requested behavior is not present in the baseline. Source inspection
finds directory copying and template backfill, but no named registry,
independent role selections, persisted template provenance, ownership
ledger, or role-aware Copilot orchestrator rendering. Existing Go, Cobra,
YAML, embedded assets, and filesystem facilities are sufficient; no new
dependency, language, license, daemon, or remote service is required.

The main compatibility risk is treating today's untracked copied files as
managed merely because their bytes match a template. Another is placing a
named registry inside the recursively copied legacy templates directory.
The new mode must be explicit, own only the files and links it actually
creates, and preserve existing directory-based behavior outside that mode.

## Existing behavior and insertion points

- `internal/cli/add.go:addCmd`, `addExternal`, and `addCheckout` accept
  repeated directory-valued template flags. They apply templates to
  `inject/`, create a Claude context file, and install the embedded Claude
  orchestrator skill regardless of the configured agent.
- `applyTemplates` applies the default directory first, then explicit
  directories. `copyDir` skips existing files: earlier content wins; this is
  not an overwrite-based layering engine. Both copy and sync helpers print
  several errors without returning them. Preserve the legacy route; do not
  reuse those success-shaped failures in the new managed route.
- `internal/config.go:TemplatePath` chooses repository
  `.tws/templates/` before `~/.config/tws/templates/`. `LoadConfigForRepo`
  and `mergeConfig` provide the per-repository/global precedence pattern.
  Config loaders currently hide parse/read errors; managed resolution needs
  an error-returning path instead of treating invalid defaults as absent.
- `internal/cli/template.go` only implements `sync`. It backfills `inject/`
  and invokes `InjectFilesForFeature`; it has no role or ownership model.
- `internal/inject.go` now verifies external Git worktree roots and handles
  slash-containing logical names. Preserve that RC3 discovery. Existing
  `InjectFiles` skips any existing leaf; managed materialization must instead
  distinguish a recorded matching link from an unmanaged collision.
- `internal/session.go:PlanCheckoutSessionLinks` temporarily exposes
  feature `inject/` files during a checkout session, rejects collisions, and
  cleans up its own links. Template synchronization must not turn checkout
  mode into permanent repository injection or a second physical checkout.
- `internal/cli/init.go` embeds/installs Claude and Copilot assets, but its
  detection and boolean `installFile` helper are not a managed template
  renderer. Keep global skill installation and agent launch commands intact.
- `internal/cli/space_guard_test.go` deliberately freezes legacy template
  stdout/exit behavior. Gate the new strict error behavior rather than
  weakening these tests to accommodate a global helper rewrite.
- `internal/cli/sync_plan_docs_test.go` pins the current roadmap target.
  Refresh that documentation assertion with the #3 release receipt while
  preserving its sync/reparent safety checks.

These are source-level findings, not newly executed template reproductions.
The implementation must add permanent behavioral coverage, not cite this
analysis as evidence that a candidate passes.

## User-approved decisions

1. **Opt-in managed behavior.** Named templates or role flags select the
   new path. Legacy directory-based add/backfill retains copy-if-missing
   behavior. Managed sync defaults to both roles; legacy sync stays
   worktree-only. Store the registry separately from the old recursively
   copied directories.
2. **Persist selections.** Save each feature's role selection and resolved
   source. Defaults fill new/unconfigured roles; later defaults or a newly
   shadowing registry entry must not silently retarget an existing role.
   Ordinary sync refreshes content from the saved source. Switching a
   template or rendering agent requires an explicit selection.
3. **Conditional deletion with consent.** An obsolete file can be removed
   only if ownership and unchanged contents are proved. First warn with the
   exact paths and ask a default-No question. Declining retains the files
   and leaves the proposed update unapplied. Noninteractive deletion needs
   explicit, narrowly scoped consent and must still show the warning.
4. **No override through consent.** Edited owned files, unmanaged files,
   and unmanaged symlinks remain hard conflicts. Neither a yes answer nor
   an automation flag grants permission to delete or replace them.
   Keep/replace/diff/edit remains `template-conflict-resolution`, not this
   implementation boundary.
5. **Root-only managed worktree guidance.** Require repository-root
   injection for native managed worktree instructions in this slice.
   Preserve legacy custom targets and orchestrator-only updates. The user
   chose this boundary rather than expanding per-file routing and checkout
   session behavior now.
6. **Preserve before replacement/removal.** During implementation review,
   the user approved preserving old objects in a private recovery area
   before changing existing owned paths, with automatic cleanup of verified
   old copies after successful ownership publication. Detected conflicts or
   failures retain both versions and report their locations. The original
   exact-path deletion warning/default-No prompt remains mandatory.
   The user accepted the limit that automatic cleanup cannot preserve
   additional writes made later through an already-open file descriptor.
   This is not a promise of inode/hash-conditioned POSIX rename/unlink or
   cross-file atomicity.

## Proposed bounded design

Use one shared planner for registry references, roles, native rendering,
owned regular files, and external injection links. Apply only the selected
roles. Resolve repository identity from the workspace, not whichever
worktree or feature directory happens to be the current directory. Save
versioned feature-local selections and per-path ownership evidence outside
the injected asset tree. Do not adopt existing assets, including old
automatically installed Claude files, through matching content alone.

A registry package retains the requested `feature/` and `inject/` layout.
An optional versioned manifest adds literal role guidance and skill names
filtered by role/agent, without copying installed skill implementations or
evaluating commands. New managed rendering supports Claude and Copilot;
custom agent commands require an explicit supported rendering agent.

Preflight sources, destinations, ownership, obsolete paths, and worktree
targets before writes or a removal prompt. Consent refers to that exact
plan; changed evidence invalidates it. Coordinate writers and preserve
existing workspace/session/mutation exclusions. Do not promise a single
atomic filesystem transaction across multiple files or worktrees. Surface
partial I/O failures and preserve evidence rather than adopting changed
files on retry.

## Risks and non-goals

- Source/destination symlinks, parent-directory replacement, tracked worktree
  paths, and hash/type drift must never authorize writes outside the plan.
- File removals include managed external links: leaving a dangling injected
  instruction link is not successful synchronization.
- Native worktree instructions need native root locations. Do not silently
  claim agent discovery when a non-root injection target relocates them.
- Repeated add must not reach the legacy external `FEATURE.md` overwrite
  before managed conflict checks.
- Feature rename/layout migration must preserve relative ownership paths.
  Existing export/import carries stack/decisions and optionally inject
  bytes, not a trusted ownership transfer; imported files stay unmanaged.
- Do not change Git topology, sync/reparent recovery, checkout injection
  refusal, global skill installation, semantic agent status, or tss
  coordination. No remote template fetching, arbitrary template execution,
  ownership adoption, or general conflict editor is in scope.

## Dependencies and next evidence

Retain hard parents `workspace-templates` and `template-sync-existing`.
Promote the existing `tiered-skill-system` relation to hard because the
feature extends that shipped role split. Add
`fix-inject-slash-worktree-discovery` as hard because managed external
materialization relies on its verified target enumeration. Both are
already applied. Keep `skill-distribution`, `copilot-codex-hooks`, and
`template-conflict-resolution` as soft relationships, not prerequisites
requiring new work.

Define the CLI/state/consent contract, map existing helpers and tests, and
validate the dependency DAG before implementation. Keep the historical
tpatch verifier limitation separate. The current task ends after manual
analysis, definition, and exploration; implementation and publication need
their own subsequent work.
