# Exploration: scoped status projection

## Entry and resolution

- `internal/cli/status.go:statusCmd` currently calls the global resolver/
  builder, overwrites reparent projections for all features, then filters.
  Select scope before these operations and create the invocation budget at
  the command boundary.
- `internal/config.go:LoadConfig`, `repoConfigPath`, and
  `LoadConfigForRepo` expose the hidden unbounded Git config-discovery path.
  Status should use already resolved roots and reuse config merging without
  calling the global unbounded resolver.
- `internal/agent_status.go:ResolveStatusWorkspace` and
  `internal/workspace.go:inferExternalRepoRootWith` are the two layers needing
  status-specific scope/context propagation. Keep existing global mutation
  resolution untouched.
- `internal/resolve.go:ResolveFeaturePath`, `ListFeaturesResolved`,
  `validateFeatureName`, and space guards provide existing path and namespace
  rules. Scoped builds resolve only their selected feature; global builds
  retain listing and ambiguity behavior.
- `internal/paths.go:DetectWorkspaceRoot` provides read-only marker/config
  discovery outside Git. Do not infer checkout mode from directory presence.

## Builder and facts

- `internal/agent_status.go:AgentStatusOpts`, `BuildAgentStatus`, and
  `statusBuilder` are the natural scope/budget seam. Existing Proc/Tmux/Now
  tests must remain useful.
- `buildWorkspaceHeader`, `buildFeature`, `buildEntry`,
  `buildCheckoutMaterialization`, `buildExternalMaterialization`, and
  `probeWorktree` cover the status facts. Error-swallowing `gitDirty`,
  `gitRefExists`, and `healthCurrentBranch` must not be used to turn deadline
  failures into clean/absent facts.
- `RealTmuxInventory.Snapshot` and `BuildWorktreeInventory` provide parsers/
  inventory semantics worth sharing rather than duplicating. Add bounded
  status execution at their command boundary, preserving other consumers.
- `internal/stack_status.go:probeDirty` already suppresses optional index
  refresh. Its read-only command/environment behavior is prior art; its
  existing stack-status caller need not change.
- `internal/checkout_health.go:realProcessChecker.Probe` uses signal zero.
  `proberAsChecker` supports existing recovery classifiers without a second
  process implementation.
- `projectCheckoutSession` and `attributeCheckoutSession` need an explicit
  off-scope session case: keep shared state, do not read another stack, and
  do not call that excluded entry orphaned. Global attribution remains intact.
- `FilterFeature` deliberately preserves global runtime facts on an existing
  report. Leave this helper compatible; the new CLI scoped builder owns the
  user-approved different aggregation.

## Mutation and observability boundaries

- `internal/cli/reparent_observability.go:reparentProjectionFor` and
  `reparentFeaturePathFor` reconcile an environment/configured external root
  difference using `internal/cli/sync_modes.go:resolveExternalSyncLayout`.
  That resolver performs file reads, not Git; preserve its exact selected
  feature behavior and pre-stdout anchored notice.
- `internal/reparent_state.go:BuildReparentProjection` and
  `ActiveReparentScratchPath` classify selected durable artifacts read-only.
  Do not walk excluded feature artifacts just to filter scratch inventory.
- Sync projection in `agent_status.go` uses
  `ClassifyExternalSyncState`/`buildOneSyncReport`; preserve RC2 recovery
  guidance and compatibility-artifact suppression. No recovery schema or
  admission logic is in scope.

## Existing tests and documentation

- `internal/agent_status_test.go`: real external/checkout fixtures,
  controllable Proc/Tmux/Now, exact JSON key snapshots, issues/rollups,
  failed/ambiguous topology, and direct/session cases.
- `internal/cli/status_test.go`: real command execution, guarded feature
  names, cwd equivalence, deterministic missing/idle tmux fixtures, help,
  JSON and human output. Add scoped resolution and pre-probe refusal cases.
- `internal/cli/sync_plan_docs_test.go`: housekeeping assertions now name #3
  and retain verified RC2/RC3 milestones and reparent safety prose.
- `internal/cli/stack_status_test.go` and `internal/stack_status_test.go`:
  shared inventory/dirty helpers must remain compatible.
- `README.md`, `docs/cheatsheet.md`, `CHANGELOG.md`,
  `assets/skills/claude/tesseraworkspaces/SKILL.md`,
  `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`, and
  `assets/skills/copilot/tws.prompt.md` carry the user/agent status guidance.

The smallest coherent implementation is one scoped status path with shared
bounded observation plumbing, not parallel global/scoped copies. Add focused
status-specific helpers/tests as needed, preserving non-status wrappers.
The existing hard parent remains `agent-work-status-dashboard`; this feature
consumes newer sync/reparent projections without changing their contracts.
No cross-repository dependency or tss implementation feature is needed.
