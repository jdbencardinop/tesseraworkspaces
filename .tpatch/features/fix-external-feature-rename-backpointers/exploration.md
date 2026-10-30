# External feature rename: grounded exploration

Planning baseline: `e578451`; published implementation baseline `v1.2.18-rc.6`.
The working implementation base advanced to `6819a4e` for two separately
approved test-only status-fixture prerequisites (`d8e821b`, `6819a4e`),
both excluded from this feature.
This document records insertion points and a test plan, not an implementation.

## Current route and smallest coherent change

`internal/cli/rename.go:renameFeatureCmd` is the public entry. Its current
sequence is context resolution, lifecycle/mode reservation, staged spaces
rename, direct-record guard, `os.Rename`, reservation retargeting, then registry
commit. It only moves the directory back on registry failure.

Keep checkout behavior separate. Introduce an external coordinator that
plans complete relocation, updates exact Git pointers, validates the result,
and owns durable rollback/recovery. A dedicated feature-specific core module
may be justified during implementation; do not grow a generic transaction
framework or copy branch rename's remove/re-add logic.

Wire explicit continue/abort before ordinary old-feature existence resolution.
The old path can legitimately be absent after the first effect. New flags
belong only to feature rename, not `rename branch`.

## Reuse and gaps

- `internal/cli/managed_template.go:resolveFeatureContext`,
  `managedTemplateWorkspaceForFeature`, and config resolution already preserve
  the execution-selected root and controlling repository. Resolve that once;
  entry repositories are separate identities, not a reason to select another
  feature namespace.
- `acquireManagedFeatureRenameReservation`, its `Moved`/`Release` methods,
  and `recheckManagedTemplateSessions` are existing admission mechanisms.
  Keep actual moved-path tracking and existing shared mutation exclusions.
  Legacy features also need safe affected-worktree/session admission, not
  accidental reliance on a managed-ledger-only check.
- `internal/template_managed.go:AcquireManagedFeatureLifecycleLock` locks
  the feature directory inode, not the workspace namespace. It follows a
  directory move but does not reserve an absent old/destination path.
  Recovery/name admission and genuine no-replace movement must close that gap.
- `internal/inject.go:injectStackEntries`, `discoverInjectWorktrees`,
  `discoverLegacyInjectWorktrees`, `resolveInjectGitDir`,
  `verifyInjectGitBackpointer`, `verifyInjectCommonDir`, and
  `verifyInjectWorktreeRegistration` provide proven identity/path checks.
  Expose or reuse appropriately rather than duplicate permissive parsers.
  Do not mistake active injection discovery for full physical move coverage:
  it skips archived and missing entries, and authoritative stacks omit
  unlisted directories.
- `internal/workspace.go:FeaturePath` is a path constructor, while
  `internal/resolve.go:ResolveFeaturePath` validates names and layout.
  `internal/spaces.go:GuardFeatureName` provides the existing name/registered
  space guard. Validate the destination before using a constructor; do not
  rely on a registry that may be absent to validate it later.
- `internal/template_fs_unix.go` has object identity, pinned directories,
  no-replace rename, binding checks, and directory sync. Its platform rename
  implementations live in `internal/template_rename_linux.go`,
  `internal/template_rename_darwin.go`, and
  `internal/template_rename_unsupported.go`. Reuse the mechanics without
  changing their established guarantees.
- `internal/template_preserve.go` is prior art for preserved originals,
  immutable pre-effect intents, ambiguous-effect handling, and bounded
  cleanup. A feature-rename journal must be independently typed and outside
  the moved tree; do not masquerade as a template or Git rebase operation.
- `internal/spaces.go:SpacesRenameTx` and `BeginSpacesFeatureRename` own
  registry semantic rewrites. Add only the necessary exact pre/postimage,
  preservation, publication-outcome, and durability coordination for this
  operation. `saveSpaces` currently syncs the temp file and renames it; the
  rename coordinator cannot assume parent-directory durability or an owned
  rollback interface that is not there.

The per-worktree pointer plan should bind the regular marker, admin/common
directories, backpointer, current HEAD/registration, and physical old/new
locations. Preserve each pointer's path style and skip unchanged forward
bytes. Standard feature renames retain path depth; no branch identity or
managed relative symlink mapping should need rewriting.

## Git API evidence

The official documentation and pinned upstream source are cited in
`analysis.md`. `git worktree repair <selected-path>` additionally invokes a
repository-wide linked-worktree repair loop. The session's isolated
`feature-rename-git-repair-scope.json` experiment confirmed unselected sibling
mutation under both absolute and relative settings on Git 2.55.0.

Use read-only Git validation plus exact operation-owned file publication,
not that broad command as a hidden mutation. Do not normalize pointer styles
from mutable Git configuration or repair another feature as a side effect.

## Recovery and consumers to wire

The journal locator must survive moving the feature and bind both names to
one operation. CLI recovery must locate it before resolving an old path that
may no longer exist. Follow existing schema/identity/error conventions, and
distinguish preimage, owned postimage, completed publication, and unknown drift.

Inspect named feature admission and mutation paths while implementing:
`internal/cli/add.go`, `internal/cli/new.go`, `internal/cli/open.go`,
`internal/cli/direct_open.go`, `internal/cli/inject.go`,
`internal/cli/template.go`, `internal/cli/rename.go`,
`internal/cli/archive.go`, `internal/cli/delete.go`,
`internal/cli/sync.go`, `internal/cli/push.go`, and
`internal/cli/space.go`. Centralize pending-rename checks where possible;
do not create independent inconsistent blockers in each command. Checkout
must not start consuming external rename state. Preserve read-only
observability or explicit pending errors without adding a competing schema.

## Existing test anchors

| Existing source | Required extension |
|---|---|
| `internal/cli/space_lifecycle_test.go`: relative rewrites, checkout layout, pinned entries, no-op registry, `TestSpaceLifecycle_RenameRollbackOnSaveFailure` | Materialized worktrees in multiple repositories; Git pointer/registry failure ordering; partial publication and recovered consistency. |
| `internal/spaces_test.go`: `TestBeginSpacesFeatureRenameRewritesAndPins`, no-match and source/destination guard tests | Exact registry pre/postimage and foreign-edit refusal without changing semantic rewrites. |
| `internal/cli/named_template_test.go`: `TestManagedTemplateStateSurvivesFeatureRename`, writer/session refusals, execution-root rename case | Real external worktrees, v1 ownership, old/new roots, destination reoccupation, and no implicit source re-rendering. |
| `internal/cli/named_template_conflict_lifecycle_test.go` | V2 acknowledgements and effective content across feature rename, injection, archive/reactivation, and ordinary sync without manual repair. |
| `internal/cli/named_template_final_regression_test.go` | Reuse multi-repository/decoupled-name fixtures and existing mode/lifecycle/recovery constraints; do not change unrelated branch-rename behavior. |
| `internal/cli/close_records_test.go`: `TestRenameFeatureRefusesLiveRecords` | Preserve public refusal semantics and ensure no renamed/relocated live session records. |
| `internal/cli/space_guard_test.go` | Destination/source names and registered targets are guarded before path mutation. |
| `internal/inject_test.go`, `internal/template_managed_test.go` | Exact Git links, slash paths, stale/foreign markers, literal names, and stable old/new inode identity fixtures. |
| `internal/cli/sync_plan_docs_test.go` | Planning handoff now; executable release/CLI guidance after implementation. |

New cases should use the `FeatureRename` name so the focused command in the
spec selects them. Include ordinary dirty/index/untracked content, locked and
in-progress worktrees, absolute/relative pointers, broken unselected siblings,
missing/archived/unlisted physical entries, case aliases, unsafe parent swaps,
recovery at either directory location, and every preservation/publication
checkpoint. Assert that no target or restored registration names a missing
or old location after successful completion.

Use temporary `GIT_CONFIG_GLOBAL` files with explicit author/committer identity;
CLI TestMain resets injected `GIT_CONFIG_COUNT` pairs. Use `t.Chdir` for fixture
cwd changes. When a fixture needs a replaced inode, retain the original
object instead of assuming unlink/recreate produces a different identity.

`internal/template_managed.go:renderManagedNativeGuidance` embeds the feature
name in built-in content. Preserve native overrides during the move, and test
that a later explicit template sync retains its normal new-proposal conflict
behavior rather than silently reacknowledging changed rendered guidance.

## Handoff and stop point

Hard dependencies are the existing rename command, workspace sibling links,
and the current template conflict/ownership boundary. Their transitive
parents provide workspace resolution and verified injection discovery.
Do not rewrite shipped parent artifacts or bundle historical verification
repair.

The manual analysis, specification, and exploration phases completed at
`defined`, last command `explore`, with a valid DAG and a planning-only
handoff. The user has now authorized implementation in an isolated worktree,
followed by the required review, validation, record/replay, and single-feature
landing process. No push or tag is authorized.
