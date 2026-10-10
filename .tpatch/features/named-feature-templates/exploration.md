# Exploration: named, role-aware feature templates

## Baseline and smallest coherent change

Explored `v1.2.18-rc.4` at `947032d`. This is a source map and implementation
plan, not an implemented candidate. Product code is unchanged by planning.
The only Go change in the planning handoff updates the documentation test's
current-target assertion and adds the verified RC4 milestone.

Use a shared internal managed-template planner/state model, with thin CLI
registry, selection, and consent adapters. Keep the legacy directory route
separate at the boundary, not a second copy of every template operation.
Do not put ownership logic in Cobra handlers or copy the entire injection/
session implementation. No additional dependency is currently justified.

## CLI, defaults, and resolution

| Existing file/symbol | Planned responsibility |
| --- | --- |
| `internal/cli/template.go:templateCmd`, `templateSyncCmd`, `syncFeatureTemplate` | Register registry commands; choose legacy/managed route; scope roles and batches; propagate managed errors. Keep the old helper's legacy behavior until its callers are explicitly dispatched. |
| `internal/cli/add.go:addCmd`, `addExternal`, `addCheckout` | Add role/agent/removal-consent flags; preflight managed add before bootstrap writes or branch/session effects. Do not run both bootstrap paths. |
| `internal/cli/add.go:applyTemplates`, `copyDir` | Preserve flat-directory compatibility and first-existing-file-wins order. These void/error-printing and `os.Stat`-based helpers are not safe managed writers. |
| `internal/config.go:Config`, `mergeConfig`, `LoadConfigForRepo`, `TemplatePath` | Add role defaults and explicit error-returning managed config resolution using the owning repository. Keep registry storage outside legacy template paths. |
| `internal/cli/config.go:configSetCmd`, `configGetCmd`, `configShowCmd` | Wire both new keys, validation, help, completion, and visible values. Avoid an unrelated rewrite of legacy `os.Exit` paths. |
| `internal/workspace.go:RequireWorkspace` | Resolve the controlling repository/mode rather than guessing from `.tws/` or agent cwd. |
| `internal/resolve.go:ResolveFeaturePath`, `RequireFeaturePath`, `ResolveFeaturePathFor`, `ListFeaturesResolved` | Preserve actual selected external roots, checkout layout ambiguity, and guarded feature identity. Hidden repository registry storage must never enumerate as a feature. |
| `internal/paths.go:ListFeaturesE` | Preserve runtime error propagation and registered-space filtering for sync-all. Completion-only `ListFeatures` is not an execution primitive. |

Registry copies and ownership state are new data contracts, not extensions
to `stack.yaml`. The package layout, reference syntax, versioned manifest,
and proposed feature-root `.tws-templates.json` are defined in the spec.
Do not interpret an existing feature's file bytes as a migration record.

## Native guidance and skills

- `assets/skills/embed.go` exposes `ClaudeSkill`, `ClaudeOrchestratorSkill`,
  and `CopilotSkill`. The baseline lacks a dedicated native Copilot
  orchestrator asset. Reuse relevant role prose and add the minimum
  role-specific rendering/embedded content; do not copy the Copilot prompt's
  `mode/tools` frontmatter into a native instruction document.
- `assets/skills/claude/tesseraworkspaces/SKILL.md`,
  `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`, and
  `assets/skills/copilot/tws.prompt.md` are the existing agent-facing docs to
  update when implementation changes commands.
- `internal/cli/init.go:detectAgent` checks directories, while
  `installFile` returns a boolean after printing errors. Neither supplies
  the new saved-agent/ownership contract. Do not change `tws init` or global
  skill installation through accidental helper reuse.
- `internal/cli/open.go:isClaudeAgent` and
  `internal/session.go:CheckoutSessionAgentCommand` show existing Claude
  aliases. Agent command parsing is launch behavior, not permission to
  execute a template or rewrite the configured command.
- The available Copilot CLI help confirms native `AGENTS.md`,
  `.github/instructions/**/*.instructions.md`, and
  `.github/copilot-instructions.md` in the Git root/current directory.
  Use the spec's non-colliding worktree instruction filename and
  feature-root orchestrator file. Manifest references are data, not a skill
  installation or a guarantee that a referenced skill is available.

## Materialization and ownership

- `internal/inject.go:InjectFiles` is called beyond template sync; it skips
  existing leaves and does not record ownership. A managed-only dispatch
  must record new links and recognize only already recorded links.
  Preserve the existing path for user/legacy inject files.
- `InjectFilesForFeature`, `discoverInjectWorktrees`, and
  `ResolveInjectWorktree` provide RC3's validated target enumeration.
  Reuse this logic, including slash names, archive/missing handling,
  copied-marker refusal, and multi-repository worktrees. Do not independently
  recurse through intermediate directories.
- `internal/cli/new.go`, `internal/cli/open.go`, and
  `internal/cli/inject.go` reach injection after later materialization or
  opening. Their managed links must not become unowned just because they
  were not created by `template sync`. Template-driven link retirement must
  be scoped to recorded, verified, untracked links.
- `internal/inject.go:ResolveInjectInto` relocates the entire inject tree;
  it has no native-agent exception. Keep custom-target behavior on the
  legacy route; refuse incompatible non-root targets in managed worktree
  planning instead of silently producing undiscoverable native instructions.
  The user explicitly approved this first-slice boundary.
- `internal/session.go:PlanCheckoutSessionLinks`,
  `ApplyCheckoutSessionLinks`, and `CleanupCheckoutSessionLinks` own
  temporary checkout links and excludes. Keep that ownership separate from
  the template ledger and never add permanent injection in checkout mode.
- `internal/session.go:ensureSessionDestination` and root-relative atomic
  state helpers illustrate boundary and persistence checks. Reuse suitable
  primitives without assuming `trackedPath` distinguishes Git errors from
  a proved untracked path; managed writes require that distinction.
- `internal/cli/sync_modes.go` and `internal/checkout_sync.go` show existing
  external/workspace mutation reservation and recheck patterns;
  `internal/external_session_intent.go` and `internal/session.go` show
  launch-intent coordination. Implementation must establish the appropriate
  exclusion, not add a check-then-write race or a new sync recovery format.
  Preserve current session/sync/reparent semantics.

## Consent and failure behavior

`internal/cli/sync.go:approveMissingStackFallback` is prior art for a
stderr warning, Cobra-provided input, a default-No prompt, and explicit
noninteractive consent. It returns errors on cancellation/read failure.
Use the same UX shape with a template-specific exact-path list and
`--allow-managed-removal`; do not share or widen sync's authorization flag.

Preflight all managed batch plans before legacy or managed asset writes.
Prompt only after every obsolete path is proven eligible. Once consent is
obtained, claim/recheck the exact plan before mutation; changed evidence
invalidates consent. Declining leaves files, selections, ownership, branches,
and launch state unchanged. This is deletion authorization only, not the
deferred keep/replace/diff/edit implementation.

Regular file writes and state publication can be atomic individually, but
the group is not one filesystem transaction. Inject failure points between
effects and ownership publication. A retry must preserve and report drift,
never "repair" a partial update by silently adopting its output.

## Lifecycle surfaces

- `internal/cli/rename.go:renameFeatureCmd` and
  `internal/migrate.go:MigrateFeatureLayout` move feature directories.
  Feature-relative state should move with them without embedding old
  destination paths; saved source identity must not silently rebind.
- `internal/export.go:WorkspaceExport` contains stack/decisions, and
  `internal/cli/export.go:exportTarball` includes inject bytes, not arbitrary
  feature-root assets. Preserve this format and disclose the managed-state/
  orchestrator omission rather than claiming portable template ownership.
- `internal/cli/importcmd.go:recreateWorkspaceE`, `recreateCheckout`, and
  `recreateExternal` restore assets without provenance. Imported bytes
  remain unmanaged; importing cannot bypass a later template ownership
  conflict. Do not migrate export formats in this slice.

## Existing tests and additions

| Existing coverage | Preserve or extend |
| --- | --- |
| `internal/cli/inject_test.go:TestTemplateAndImportUseNestedWorktreeDiscovery`, `TestTemplateAndImportSurfaceDiscoveryErrors` | Keep legacy stdout/error behavior; add separate managed nonzero-error, nested-link ownership, and removal assertions. |
| `internal/inject_test.go` and `internal/cli/inject_test.go` | Retain discovery, relative links, malformed metadata, special names, and checkout standalone-injection refusal. |
| `internal/cli/checkout_lifecycle_test.go:TestCheckoutAdd_CreatesFeatureAssets`, `TestCheckoutAdd_Idempotent` | Keep the no-flag legacy layout and authored feature document; add managed role/configuration fixtures. |
| `internal/cli/space_guard_test.go:TestSpaceGuard_TemplateSyncAndHooksInstallCarveOutBoundary` | Preserve legacy exit/stdout carve-outs and unrelated hooks behavior; assert new errors only on managed dispatch. |
| `internal/session_test.go`, `internal/cli/direct_open_test.go`, `internal/cli/checkout_lifecycle_test.go` | Keep session ownership/exclusions and add relevant managed-context concurrency cases. |
| `internal/cli/sync_transaction_v2_test.go:TestSyncTransactionMissingStackRequiresExplicitConsent` | Reuse testing style, not sync state: deterministic input/TTY seams, noninteractive refusal, affirmative consent, and non-bypassable guards. |
| `internal/resolve_test.go`, `internal/workspace_test.go`, `internal/migrate_test.go`, `internal/cli/space_migrate_test.go` | Check hidden registry exclusion, cwd/mode routing, guarded names, and relative state across migration/rename. |
| `internal/cli/sync_import_export_test.go`, `internal/cli/checkout_lifecycle_test.go` | Preserve export/runtime exclusions and explicit unmanaged import semantics. |
| `internal/cli/sync_plan_docs_test.go` | Advance current-target prose without weakening shipped sync/reparent/status milestones. |

Add focused planner/CLI tests under the existing `internal` and
`internal/cli` test packages. The spec lists the required registry,
precedence, ownership, native rendering, consent, race, and partial-failure
matrix. Use real temporary Git repos/worktrees, isolated HOME/config, and
explicit fixture identity. Do not depend on installed agents or real tmux.

## Dependency and stopping point

Keep `workspace-templates` and `template-sync-existing` hard. Promote
`tiered-skill-system` from soft to hard and add
`fix-inject-slash-worktree-discovery` hard for the required shipped contracts.
Retain other soft links; in particular, the requested
`template-conflict-resolution` feature is not an implementation gate.
No provider feature or historical artifact repair becomes a prerequisite.

Advance manual analyze/define/explore and validate the DAG. Stop in
`defined` with exploration complete: no implementation recipe, apply session,
product changes, landing, push, or tag belongs to this planning task.
