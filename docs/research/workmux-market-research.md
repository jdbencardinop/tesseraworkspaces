# workmux market and architecture research

Status: completed

Research date: 2026-09-11

Pinned workmux revision:
[`0c6bedc55ceac499997438cca57a72139f116047`](https://github.com/raine/workmux/tree/0c6bedc55ceac499997438cca57a72139f116047)
(v0.1.260)

This document preserves the workmux research used to evaluate future
tesseraworkspaces (`tws`) and tesserasessions (`tss`) work. It is source and
documentation research; workmux was not executed locally.

## Executive conclusion

workmux and tws overlap in worktree creation, agent launching, and tmux usage,
but they optimize for different layers:

- workmux is a terminal-session and parallel-agent cockpit built around Git
  worktrees;
- tws is a Git topology and workspace-safety orchestrator that can launch
  sessions;
- tss is already the natural owner of cross-agent inventory, runtime
  observation, and live session control.

The ecosystem should not copy workmux wholesale into tws. The best use of the
research is:

1. adopt selected Git/worktree safety and provisioning ideas in tws;
2. use workmux's session UX as reference material for tss;
3. integrate tws and tss through versioned CLI JSON contracts;
4. consider workmux only as a later optional tss provider, never as a direct
   tws dependency.

This conclusion does not replace the current tws target,
`safe-reparent-restack`.

## Product snapshot

workmux describes itself as an opinionated connector between Git worktrees,
terminal multiplexers, and coding agents, rather than a replacement for those
tools:
[README lines 22-28](https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/README.md#L22-L28).

At the research revision it was a rapidly changing Rust 0.1.x project with
roughly 2,440 stars, 260 tags, and a high concentration of commits from one
maintainer. Its principal audience is a terminal-native developer running
multiple agent tasks in parallel.

### Capability comparison

| Area | workmux | tws | Assessment |
| --- | --- | --- | --- |
| Primary abstraction | One worktree mapped to one multiplexer target | Feature workspace containing branches/worktrees and a dependency graph | Different center of gravity |
| Terminal model | tmux window by default; optional session mode; configurable windows, panes, and layouts | External branch session, feature-wide `--all` session, or one locked checkout session | workmux is richer |
| Session identity | Random token in Git config and tmux window option | External: deterministic lossy name plus pane-path verification; checkout: hashed name plus state/lock; direct: token-owned records | workmux is stronger for external tmux identity |
| Agent state | Agent hooks report working, waiting, or done; per-pane state | `runtime_presence` is measured; `agent_state` remains deliberately `unknown` without a provider | Current tws gap, already assigned to tss |
| Runtime control | `send`, `capture`, `status`, `wait`, `run`, `resurrect`, dashboard, sidebar | `open`, `close`, `status`; durable decision log | workmux has a stronger live control plane |
| Git topology | One saved base per branch; merge/rebase/squash | Stack DAG, scoped restacking, amend-aware cutoffs, resumable sync | tws is substantially stronger |
| Rebase safety | Native rebase with ordinary conflict continuation | Plan, replay bounds, fingerprint approval, JIT guard, and recovery state | tws differentiator |
| Multi-repo | Lifecycle commands are repository-scoped | External workspaces support multi-repo entries; checkout is explicitly single-repository | tws is stronger |
| Coordination | Ephemeral live prompt transport | Durable broadcast and targeted decisions with acknowledgement | Complementary |
| Cleanup | Identity revalidation, rename-to-quarantine, retry/backoff | Strong command guards; no equivalent quarantine design | workmux has transferable safety ideas |
| Automation | Headless JSON provisioning and matrix fan-out | Scriptable commands and stable status schemas, but less provisioning fan-out | workmux is stronger |
| Platform | Unix-oriented; non-tmux backends experimental | Go foundation; tmux optional | tws has the broader base |

## workmux session architecture

### Multiplexer modes and layouts

workmux defaults to one tmux window per worktree in the current session. It can
instead create one tmux session per worktree, with multiple configured windows
and panes. Its multiplexer abstraction also has experimental WezTerm, kitty,
and Zellij backends.

The shared multiplexer trait and pane orchestration are in:

- [`src/multiplexer/mod.rs`](https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/multiplexer/mod.rs#L119-L122);
- [`setup_panes`](https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/multiplexer/mod.rs#L621-L862).

Panes start through a shell-readiness handshake before commands are sent.
Session mode is tmux-only and does not support duplicate targets. The sidebar
is also tmux-only.

### Durable target identity

Names are not authoritative. workmux persists
`workmux.worktree.<handle>.*` metadata in repository-local Git config and mints
a random 128-bit window token:

- [`ensure_worktree_window_token_in`](https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/git/worktree.rs#L400-L410);
- [`set_window_ownership`](https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/multiplexer/tmux.rs#L841-L887).

Old unmanaged windows can be adopted only when name and pane-path evidence is
unambiguous. Renames migrate the Git-config metadata and agent-state records.

This is materially stronger than relying on a sanitized target name. It is the
most directly transferable session idea from workmux.

### Agent state and attention routing

Agent-specific hooks write window- and pane-scoped status. Per-pane JSON state
stores process, command, boot, agent, and native session identity:

[`src/state/types.rs`](https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/state/types.rs#L73-L142).

That data powers:

- a tmux status-bar indicator;
- focus acknowledgement;
- a sidebar daemon;
- a dashboard with diff, patch, and agent-input modes;
- `last-done` and `last-agent` navigation;
- `send`, `capture`, `wait`, and `run`;
- crash recovery through `resurrect`;
- stale-agent reaping.

Agent support is not uniform. Some adapters cannot report `waiting`, and
agents without installed hooks are invisible to the semantic dashboard.

### Session survival

workmux creates ordinary tmux windows and sessions and does not supervise them
with a long-running parent. They therefore survive workmux exiting. `close`
kills the target, and `resurrect` reconstructs targets from retained agent
state.

This survival property is inferred from the implementation, not stated as a
formal compatibility guarantee.

## Git and worktree behavior

workmux provides:

- creation from local branches, remote refs, forks, and GitHub/GitLab PRs;
- a recorded base per branch;
- merge, rebase, and squash cleanup flows;
- `merge --into` for simple parent-branch workflows;
- adoption of existing worktrees;
- duplicate windows in window mode;
- `remove --gone`;
- file copy/symlink operations;
- lifecycle hooks;
- a dirty-worktree rescue flow;
- headless provisioning and JSON receipts;
- multi-agent matrix generation.

It does not provide:

- a stack dependency graph;
- topological or scoped restacking;
- amend-aware `last_base_sha` semantics;
- a rebase plan or approval fingerprint;
- resumable sync state;
- bulk sync or push;
- multi-repository lifecycle workspaces.

Multi-repository workspaces remain requested in
[raine/workmux#161](https://github.com/raine/workmux/issues/161).

## Safety patterns worth adopting

### Quarantine-before-delete

Before destructive cleanup, workmux records and revalidates device/inode and
repository identity, then renames the worktree into a quarantine path before
Git pruning and recursive deletion:

[`src/workflow/cleanup.rs`](https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/workflow/cleanup.rs#L323-L430).

Deletion retries with backoff and leaves quarantine evidence when it cannot
finish. This fits tws and should remain a tws-owned Git/worktree feature.

### Hardened Git execution

workmux centralizes Git config overrides and environment scrubbing:

[`src/git/security.rs`](https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/git/security.rs#L6-L65).

Notable controls include disabling repository-controlled hooks, external diff
programs, editors, interactive credentials, and unapproved protocols, plus
removing ambient `GIT_CONFIG_*`, `GIT_DIR`, `GIT_WORK_TREE`, and similar
variables.

tws should analyze compatibility before adopting this globally. Some commands
may intentionally honor user hooks.

### Repo-scoped locking

workmux serializes Git-config writes through `flock` on a file in the Git
common directory. This is useful precedent for machine-local metadata shared
across linked worktrees.

## What tws already does better

tws has stronger semantics for:

- stack dependencies and topological ordering;
- scoped sync and local-only propagation;
- amend-aware cutoffs;
- rebase preview, bounds, approval, and JIT revalidation;
- resumable external and checkout recovery;
- multi-repository external workspaces;
- explicit single-checkout mode;
- durable cross-worktree decisions;
- workspace registry and sibling-space discovery;
- direct-session ownership records;
- separation of runtime presence from semantic agent state.

workmux's `merge --into` is useful but is not a replacement for the tws stack
model.

## Similar capabilities implemented differently

| Problem | workmux | tws |
| --- | --- | --- |
| Open existing environment | Switch current client to a window/session | Attach to an external session or a durable checkout session |
| Group a feature | Current tmux session with worktree windows | `tws open --all` feature session with orchestrator and worktree windows |
| Session discovery | Git-config metadata plus tmux ownership token | Deterministic names plus one batched tmux inventory |
| Collision handling | Token authoritative | External pane-path disambiguation; checkout hash |
| Agent resume | Per-agent continue/fork flags | Claude session-directory detection and automatic `-c` |
| Agent communication | Live prompt input | Durable decisions log |
| Runtime status | Agent hooks push semantic state | tws records and tmux observations; semantic state stays unknown |
| Context setup | Copy/symlink operations and hooks | Inject/templates plus checkout session links |
| Recovery | Reopen terminal/agent targets | Restore Git transactions and checkout branch state |
| Close | Kill terminal target, retain worktree | Reconcile direct records or restore checkout branch/context state |

One important default differs: workmux usually creates a window in the current
tmux session. tws external `--tmux` creates a separate session per branch;
`--all` creates a feature session with worktree windows but does not launch an
agent in every window.

## tesserasessions inspection update

After the workmux research, the sibling tesserasessions repository was
inspected at commit `8cb4ae7`.

tss already implements much of the proposed session boundary:

- a local-first historical inventory across Claude, Copilot, Hermes, OpenCode,
  Codex, Herdr, and tmux;
- a delivered, side-effect-free
  [`runtime-status-contract`](https://github.com/jdbencardinop/tesserasessions/blob/8cb4ae7d1eec761fc626f67ad0d01ff1206a50d6/docs/runtime-status-contract.md);
- separate `runtime_presence` and `agent_state`;
- bounded Herdr/tmux probes with freshness and match evidence;
- `attach`, `open`, `send`, `read`, and `run`;
- Herdr-first and tmux-fallback backend selection;
- optional `--print` for review before execution.

The existing contract already states the correct ownership split:

- tws owns topology, sessions it launches, local liveness, locks, failures, and
  final rollups;
- tss owns cross-agent runtime observation, semantic agent state, and
  externally launched runtimes.

The next integration is already named `tws/tss-status-enrichment`. A new
competing status schema should not be created.

Remaining joint design gaps are:

1. an opaque, provider-owned durable session reference for targets tws
   launches;
2. a versioned control contract beyond shell command generation;
3. session creation/layout intent and capability negotiation;
4. process birth identity and stale-instance reconciliation;
5. agent-state provenance and confidence;
6. crash restore/resurrection semantics;
7. optional provider adapters such as workmux.

## Should the ecosystem integrate workmux?

Not as a direct tws dependency.

The ecosystem already covers the essential responsibility areas:

- tws owns workspace and Git topology;
- tss owns historical inventory, runtime observation, and live control;
- Herdr is the preferred tss live backend;
- tmux is the portable fallback.

workmux could later be useful as an optional tss provider for capabilities that
would otherwise be expensive to reproduce:

- rich multiplexer layouts;
- tokenized worktree/window identity;
- agent lifecycle hooks;
- attention routing and dashboard state;
- resurrection;
- multi-agent fan-out.

Any adapter should be optional and capability-gated. It should consume
workmux's public CLI/state contracts rather than reading undocumented internals
where possible. The reasons to defer are:

- workmux is still versioned 0.1.x and changes rapidly;
- alternative multiplexer support is experimental;
- its global tmux sidebar hooks can conflict with user configuration;
- several identity and reconciliation defects remain open, including
  raine/workmux#208, raine/workmux#209, raine/workmux#219,
  raine/workmux#238, and raine/workmux#252;
- a direct tws integration would create overlapping ownership.

Recommended decision: native tss Herdr/tmux support remains the baseline.
Revisit a `workmux` tss provider only after the joint provider-capability
contract exists.

## Candidate follow-up artifacts

| Priority | Artifact | Owner |
| --- | --- | --- |
| 1 | ADR: tws-tss runtime and control ownership boundary | Joint |
| 2 | PRD: versioned session-control provider contract | tss-led |
| 3 | `tss-status-enrichment` tpatch feature | tws |
| 4 | ADR: durable session identity and adoption | Joint |
| 5 | `safe-worktree-quarantine-removal` tpatch feature | tws |
| 6 | `worktree-create-plan` tpatch feature | tws |
| 7 | `headless-worktree-provisioning` tpatch feature | tws |
| 8 | `rescue-uncommitted-work` tpatch feature | tws |
| 9 | Git execution hardening analysis/ADR | tws |
| 10 | Optional `workmux` provider investigation | tss |

## Research limitations

- workmux was not run locally;
- the dashboard and sidebar implementations were surveyed, not audited
  line-by-line;
- the sandbox subsystem was read mainly from documentation;
- exact matrix-expansion and PR behavior was not runtime-tested;
- popularity and activity numbers are a point-in-time observation;
- inferred platform and tmux-version requirements are not documented
  compatibility guarantees.
