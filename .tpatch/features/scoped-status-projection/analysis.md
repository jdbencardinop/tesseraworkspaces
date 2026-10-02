# Analysis: scoped status projection

## Baseline and verdict

Issue [#3](https://github.com/jdbencardinop/tesseraworkspaces/issues/3) remains
reproducible on released `v1.2.18-rc.3`,
`86c7d5872b6a7d203840c06c403df14cda733c0e`. Issues #4 and #1 are closed after
their verified RC2/RC3 releases. The hard parent
`agent-work-status-dashboard` is applied. Lane B runtime-boundary coordination
is complete and adds no implementation prerequisite to this tws-local feature.

The fix is not simply moving `FilterFeature` earlier: the CLI resolver can
scan other features before the builder runs, the builder explicitly preserves
whole-workspace runtime presence under filtering, and checkout session
attribution assumes all features have been built. These paths must agree on
scope before implementation. No language, dependency, or licensing change is
needed; the existing Go/Cobra implementation is sufficient.

## Reproduced behavior

A separately built RC3 CLI was exercised in temporary real Git repositories,
with linked external worktrees and an explicitly configured checkout workspace.
Fixtures use isolated HOME/config and explicit Git author/committer identity.
A Git shim logs argv and delegates to real Git, except for a controlled
one-second delay on the unrelated worktree's dirty probe or an injected dirty
probe failure. tmux deterministically reports no server.

| Case | RC3 result |
| --- | --- |
| `status auth` from the external repository | Only auth appears in JSON, but billing receives two Git probes, including its delayed dirty probe; 1.764 seconds in this controlled run. |
| `status auth` from the external workspace root | Billing receives three Git probes, including repository inference and the delayed dirty probe; 1.269 seconds. |
| `status missing` from the repository | Eventually exits nonzero with no stdout, but first performs three dirty probes, including billing; 1.215 seconds. |
| Selected dirty probes exit 42 | Report exits zero, workspace and entry `dirty` are both `false`, and there are no issues. Probe failure is incorrectly represented as clean. |
| Requested checkout feature is valid; billing exists in both legacy and new layouts | `status auth` fails on billing's ambiguity and emits no document. |

Timing demonstrates the deliberately injected unrelated delay, not a general
performance benchmark. Logged argv, rather than elapsed time alone, proves
the unrelated accesses. Temporary repositories are removed after the run.

The session retains `reproduce-scoped-status.py` as the reproduction harness.
Implementation must turn these cases into permanent regression tests rather
than depending on that session artifact.

## Existing code and prior art

- `internal/cli/status.go`: resolves the workspace before selecting the
  feature; calls `BuildAgentStatus`, enriches every feature with the CLI's
  execution-consistent reparent projection, and only then filters.
- `internal/agent_status.go:ResolveStatusWorkspace`: calls `LoadConfig` and
  `MainRepoRoot`; outside Git it calls `inferExternalRepoRoot`.
- `internal/workspace.go:inferExternalRepoRootWith`: reads every feature stack
  and probes default-repository worktrees, even when a sibling/configured
  repository candidate already exists. Changing only the builder leaves this
  source of unrelated I/O intact.
- `internal/agent_status.go:BuildAgentStatus`: lists and resolves all features,
  inspects their reparent scratch paths, collects tmux and Git inventories,
  builds workspace evidence, then reads every stack/session/sync/worktree.
- `internal/resolve.go`: existing named-feature guards and new/legacy path
  resolution provide the primitives for selection without sibling
  materialization. Requested-path ambiguity and untrusted `spaces.yaml` must
  remain fatal; unrelated-path ambiguity must no longer be consulted.
- `internal/agent_status.go:attributeCheckoutSession`: reports a workspace
  `session-unattributed` warning if the recorded entry is not among built
  features. A scoped build must not label a session from an unselected feature
  orphaned merely because its stack was deliberately not read.
- `internal/agent_status.go:FilterFeature` and
  `internal/agent_status_test.go:TestAgentStatusFilterFeature`: explicitly
  preserve whole-workspace runtime presence when narrowing an existing
  report. A selected-only build cannot reproduce arbitrary external session
  facts from unselected feature records.
- `internal/agent_status.go:RealTmuxInventory`, `BuildWorktreeInventory`, and
  `internal/exec.go:MainRepoRootIn` invoke subprocesses without deadlines.
  `LoadConfig` also reaches Git through its per-repository config resolution.
  The budget must include resolution, not only final entry dirty probes.
- `internal/stack_status.go:probeDirty` already provides read-only
  `GIT_OPTIONAL_LOCKS=0` and an error channel. `gitDirty` and
  `healthCurrentBranch` in `internal/checkout_health.go` deliberately preserve
  legacy error-swallowing behavior for other callers. Add bounded,
  error-returning status paths without changing mutation callers.
- `internal/checkout_health.go:realProcessChecker.Probe` uses signal zero,
  not a child process. Preserve live/dead/unknown semantics and do not create
  unbounded goroutines to simulate cancellable probes.
- `internal/cli/reparent_observability.go` resolves the same feature location
  as execution when configured and environment roots diverge. Scoped status
  must preserve that route, the single pre-stdout anchored notice, and
  compatibility-hint suppression without examining unrelated features.
- `internal/cli/stack_status.go` already selects a named feature before its
  builder. Stack status, doctor, list, and mutation operations are not added
  to this issue's implementation scope through shared-helper edits.

## Proposed boundary

Change only `tws status [feature]` and its status-specific resolution/probes.
A named request validates/selects its feature before inventories, feature
records, and expensive Git work. Necessary workspace config, space ownership,
repository evidence, and the single checkout session/lock remain visible.
Resolve external default-repository evidence without scanning sibling stacks
or worktrees; ambiguity or missing evidence must be explicit, not silently
interpreted as a different workspace or a clean repository.

Keep unscoped successful reports, JSON keys/types/schema version, ordering,
empty arrays/nulls, local-only behavior, exit conventions, and the separate
runtime/semantic axes. Failed or timed-out observations become unknown/null
with explicit issues, never fabricated clean/absent facts. Timeouts may
intentionally change failed-probe output while healthy unscoped output stays
compatible. All report and resolution probes must share the selected budget;
no late uncapped probe may run after it expires.

Preserve external multi-worktree and explicit checkout behavior, new/legacy
layout guards, sync recovery/dirty-blocking evidence, and reparent
observability. Do not alter sync/reparent/session mutation admission, direct
process identity, provider integration, injection, templates, or release
metadata. `direct`/`tmux`/`all` are launch styles; `external`/`checkout` are
workspace modes.

## Approved decisions

1. **Scoped workspace runtime meaning.** Aggregate the requested
   feature plus genuinely workspace-scoped evidence, keeping the checkout
   session visible even when it belongs to another feature. Document this
   intentional scoped-report behavior change; unscoped reports retain their
   existing meaning. The user approved starting with this scoped model.
   Do not silently retain a false claim of global completeness.
2. **Probe latency policy.** Five seconds per
   subprocess, thirty seconds total for status subprocess work, including
   resolution, with no new CLI flags in this feature. Budget exhaustion must
   stop further subprocess dispatch and retain already established evidence.
   The final spec must define cancellation, process/pipe cleanup, and test
   tolerances. Ordinary filesystem reads are not made interruptible by an
   exec context and must not be covered by a false hard wall-clock promise.
3. **Global follow-up.** Keep the existing no-argument global command. Measure
   its costs after scoped behavior is in place; broader continuation past
   broken feature topology is a later decision, not part of this boundary.

## Required next evidence

Define and explore before marking implementation started. Cover both modes,
repository/worktree/workspace/feature-directory
invocations, healthy unscoped parity, no probes of unrelated worktrees,
unrelated malformed/ambiguous metadata, missing and invalid requested names,
off-scope checkout sessions, unknown Git facts, stalled Git/tmux subprocesses,
and exhausted budgets with no child-process leaks. Use real temporary Git
repositories and deterministic wrappers, not host tmux/session state.

Retain the existing hard parent and evaluate further dependencies during
exploration; validate the repository-local DAG before implementation. The
historical verifier limitation is not a reason to alter the dependency graph.
