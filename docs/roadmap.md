# tesseraworkspaces roadmap

This roadmap is organized by correctness and user value rather than release date. `tws` remains a Git worktree orchestrator: it may integrate with specialized tools, but it should not duplicate their source of truth.

## Now — stack safety and observability

Shipped foundations:

- explicit external/checkout workspace modes;
- checkout lifecycle and logical branch metadata;
- transactional checkout stack sync;
- `.tws/features/` layout with legacy migration;
- single-owner checkout direct/tmux agent sessions;
- checkout doctor and list observability;
- an opt-in global registry with stable workspace identity, health checks, and
  moved-target repair;
- workspace sibling links: `<spaces-root>/spaces.yaml` plus
  `tws space add/list/show/remove` for discovering tool-owned learning, ticket,
  patching, research, and documentation spaces, with feature-name protection and
  strict failure on untrusted metadata;
- agent work status: `tws status [feature] [--json]` projects materialization,
  tws-launched runtimes, and attention needs over a versioned two-axis schema,
  plus per-invocation external direct session records so a tws-launched agent is
  observable, crash-detectable, and concurrency-safe;
- stack ancestry doctor: one mode-independent, read-only `StackEdge` evaluator
  shared by checkout doctor, checkout list, and external `tws doctor`, reporting
  `current | stale | divergent | missing | cross-repo-unsupported` plus an
  explicit unevaluated state, with a reason and guidance per edge;
- stack status: `tws stack status <feature> [--json]` projects local head,
  configured base and parent head, the recorded `last_base_sha` verdict,
  ancestry state, materialization, dirty/in-progress operation, upstream state,
  and ahead/behind counts over a versioned document. It consumes the shipped
  `StackEdge` projection rather than computing ancestry of its own, is
  read-only and local-only with no implicit fetch, and reports a fact it cannot
  establish locally as `null` rather than as clean, attached, or zero;
- sync modes: `tws sync <feature>` carries three independent axes — input-ref
  policy (`--fetch`/`--no-fetch`), propagation policy (`--full`/`--local-only`),
  and selection scope (`--only <entry>`/`--from <entry>`, by logical
  `stack.yaml` name) — with mode-dependent defaults that leave the no-flag run
  unchanged in both workspace modes. A scoped run drops `--update-refs` so it
  cannot move an unselected branch; the frozen decision, scope, push, and
  validation command are persisted and carried through `--continue` and
  `--abort`; incompatible combinations are refused before any fetch, lock, or
  rebase; and external sync and push share one resolved layout;
- rebase plan guard: `tws sync` can preview the exact rebase a run would
  perform — the old base, the new base, and a replay-candidate count per
  entry — before anything moves, and refuses to execute a plan whose replay
  count exceeds an operator-set bound unless the operator approves the exact
  previewed plan by its fingerprint. A preview still fetches wherever the run
  it describes fetches, so it is a safety gate rather than an offline mode; a
  guarded run records its bound in recovery state so an older tws release
  refuses to resume it instead of silently dropping the guard; and a guard
  refusal is a single anchored line on stderr, distinct from every refusal
  tws already performed;
- safe reparent/restack: `tws stack reparent <feature> <entry> --onto <dest>`
  moves one stack entry onto a new parent and replays its descendant closure,
  coordinating three separate effects for a single-parent stack: Git refs,
  `stack.yaml` metadata, and worktree/index state. Runtime artifacts are durable
  recovery evidence, not a fourth transactional effect. A preview route describes the whole run — destination, per-row replay
  boundary, candidate counts, collateral refs, holders and remote follow-up —
  and moves no branch and writes no tws state; may fetch according to policy. A
  fresh execution is always guarded: it requires the fingerprint that preview
  printed plus a replay bound, re-enforced against freshly measured counts
  immediately before each row is computed. Every moved branch lands in one
  compare-and-swap ref transaction, so the ref commit is race-atomic and
  crash-atomic only on the reftable backend; the run has exactly one commit point,
  requiring both a durably written post-image `stack.yaml` and refs already at
  their planned values, and recovery past it is forward-only. The resume and
  rollback verbs take the whole frozen decision from persisted state and never
  accept an approval token. External direct/tmux/all launches publish intent
  before their final mutation check, and top-level external push holds the
  feature mutation lock across every entry. Every mutating external sync route
  holds the same lock through rebase, metadata, optional push and remote
  follow-up clearing, rechecks reparent state after claiming, and releases
  last. Checkout feature-directory opens retain the workspace launch intent
  through their agent/shell after a final mutation/reparent/session check.
  Repository aliases with different stored spellings are refused even when
  they share one common dir. A stack-entry destination stores its logical entry
  name, a named literal ref stores its full `refs/...` name, and a raw object
  id stores the full lowercase OID. tws changes no remote ref and no pull request; a
  rewritten branch is recorded locally so the next push warns and strengthens
  its lease. When the checkout-global lock is absent, current tws scans every
  feature's recoverable checkout sync/reparent state before fresh mutation and
  reconstructs only the matching recovery reservation. v1.2.16 only fails closed for same-feature sync after envelope
  birth; do not use an older tws while any reparent is active or recoverable.

Current target: **slash-containing worktree injection (issue #1)** — discover
the actual external linked-worktree roots for complete logical names such as
`review/pr-123`, never inject into intermediate directories, and explicitly
refuse both checkout injection forms. Ordinary sync safety (#4) is fully
released and verified in `v1.2.18-rc.2`; the release receipt records passing
main/tag CI, exact binaries, and a clean published tree.

The approved delivery order is **#4, #1, #3, #2**. Keep each existing tpatch
feature as its own implementation boundary; the issue-to-feature mapping below
is the queue, not permission to combine unrelated fixes. Lane B coordination
is handled in its separate thread.

Follow-ups explicitly owned by later features rather than by `tws status`:

- **`tss-agent-state-provider`** — populate `agent_state` from a versioned
  provider and cover runtimes tws did not launch. "blocked (needs
  approval/input)" is deferred to that provider, not dropped: nothing in tws
  observes agent stdin, tool-permission prompts, or turn boundaries, so the
  semantic axis ships honest at `unknown`.
- **Portable process birth identity** — record and verify a process start time
  or a birth-stable handle to close the PID-reuse window. Today a `present`
  means a process with that PID exists, not that that exact process exists.
- **Base ancestry per branch** — owned by the shipped stack ancestry doctor and
  the shipped stack status command above, which carry the
  `current | stale | divergent | missing | cross-repo-unsupported` semantics and
  the unevaluated state. If `tws status` ever surfaces ancestry it must consume
  the shipped `StackEdge` projection rather than compute its own; adding the key
  is additive and does not bump `schema_version`.

## Completed P0 correctness

### Configured base controls branch creation

`tws new <feature> <name> --base <ref>` and `tws add -n` must create new branches from the selected ref in the selected repository. Explicit refs are literal (`master`, `origin/master`, tags, commits); only an omitted base uses that repository's `origin/HEAD`.

### Sync continuation completes descendants

After resolving a rebase conflict, `tws sync --continue` must resume deferred descendants in topological order. It must preserve state on later failures and only report completion when every relevant parent-child edge is current.

### Sync validates the real Git branch

For decoupled names, `StackEntry.Name` identifies the tws worktree while `StackEntry.GitBranch()` identifies the Git branch. Sync must use the latter for Git validation and ref operations.

## Prioritized tws backlog

- **Sync modes (shipped)**: local-only propagation, no-fetch operation, surgical branch/descendant sync, and explicit root targets, with the frozen decision persisted and carried through `--continue`/`--abort`.
- **Safe reparent/restack (shipped)**: `tws stack reparent` coordinates three separate effects — Git refs, `stack.yaml` metadata, and worktree/index state — behind a preview-and-approve gate. Runtime artifacts remain durable recovery evidence rather than a separate effect. Its single compare-and-swap ref transaction is race-atomic, crash-atomic only on the reftable backend, and the run has one commit point with an explicit forward-only recovery contract.

| Order | Priority | GitHub issue | Existing tpatch features |
|---|---|---|---|
| 1 | P0: reproduce, then resolve remaining safety defects | [#4: ordinary sync rollback and replay cutoff](https://github.com/jdbencardinop/tesseraworkspaces/issues/4) | [`sync-transactional-abort`](../.tpatch/features/sync-transactional-abort/request.md), [`sync-cutoff-integrity`](../.tpatch/features/sync-cutoff-integrity/request.md) |
| 2 | P1: correctness | [#1: slash-containing worktree injection](https://github.com/jdbencardinop/tesseraworkspaces/issues/1) | [`fix-inject-slash-worktree-discovery`](../.tpatch/features/fix-inject-slash-worktree-discovery/request.md) |
| 3 | P1: scoped-status reliability | [#3: avoid building unrelated feature status](https://github.com/jdbencardinop/tesseraworkspaces/issues/3) | [`scoped-status-projection`](../.tpatch/features/scoped-status-projection/request.md) |
| 4 | P2: workflow enhancement | [#2: role-aware templates](https://github.com/jdbencardinop/tesseraworkspaces/issues/2) | [`named-feature-templates`](../.tpatch/features/named-feature-templates/request.md), [`template-conflict-resolution`](../.tpatch/features/template-conflict-resolution/request.md) |

Issue #4's transactional abort and cutoff fixes are shipped and verified in
`v1.2.18-rc.2`. The current implementation boundary is only
[`fix-inject-slash-worktree-discovery`](../.tpatch/features/fix-inject-slash-worktree-discovery/request.md);
do not combine it with scoped status or template work.

Nonblocking reparent follow-up:
[`fix-reparent-completion-context`](../.tpatch/features/fix-reparent-completion-context/request.md)
aligns shell-completion candidates with the execution-selected external feature
root and target-entry repository. Explicit execution remains protected; this
follow-up does not displace the issue queue above.

## Agent integration — P2

- **Named feature templates**: manage a global/per-repo template registry with `add`, `list`, `show`, and `remove`. A template contains `feature/` assets copied to the feature root and `inject/` assets copied to the worktree injection source. This standardizes orchestrator skills, feature docs, prompts, and shared worktree context across future features.
- **Agent-aware context files**: map local instructions to conventions understood by Claude, Copilot, Codex, and other configured agents.
- **Copilot and Codex hooks**: install supported decision-reading integrations or warn clearly when the configured agent has no hook adapter.
- **Explicit decision acknowledgement**: allow feature-root orchestration with `tws decisions ack --branch <name>`.
- **Inter-feature messaging**: allow one feature orchestrator to target another feature with a durable message while preserving separate stacks and lifecycle state. Do not merge feature workspaces for the first version.
- **Workspace sibling links (shipped)**: `<spaces-root>/spaces.yaml` is the dynamic registry for tool-owned learning, tickets, patching, research, and documentation spaces. Agents discover links through `tws space list/show`; skills teach discovery but do not embed mutable paths. Entries may be workspace-wide or feature-scoped, while each linked tool remains authoritative for its content and lifecycle.
- **Agent work status (shipped)**: `tws status [feature] [--json]` surfaces materialized branches, tws-launched sessions, and attention needs without pretending to replace the agent harness. It ships the stable two-axis schema (`runtime_presence` / `agent_state`) that makes a later `tss` provider purely additive.
- **Context summaries**: maintain feature-level and worktree-session recaps while preserving authored source documents.

## Tool collaboration contracts

### tesserapatch

Near-term collaboration should define a stable, read-only contract for patch identity and change queries. `tws` may use that contract to plan safer rebases and distinguish logical changes from raw SHA identity, but must not reimplement tpatch patch identity or reconciliation logic.

Long-term stretch work may explore patch-theory-backed composition for multi-parent change dependencies. Until that contract is proven, tws keeps one configured base per branch and linearizes or explicitly merges sibling dependencies.

### tesseratickets

Ticket storage, lifecycle, claims, dependency/frontier queries, and canonical ticket state belong to tesseratickets. `tws` may store a pointer to a ticket store and project relevant status into feature/worktree views, but decisions remain a communication log rather than a ticket tracker.

The tesseratickets team is currently evaluating Markdown- and Dolt-backed ticket CLIs. Integration should wait for a stable storage and query contract rather than choosing a backend inside tws.

## Optional hub / meta-workspace direction

A tws feature directory can be an optional hub for sibling tool-owned spaces:

```text
<feature>/
├── worktrees/        # tws / Git
├── inject/           # tws shared context
├── docs/             # authored feature material
├── learning/         # /teach or another learning tool
├── tickets/          # tesseratickets-owned store or pointer
└── patching/         # tpatch-owned metadata or pointer
```

The hub is not mandatory and does not make tws authoritative over these subspaces. Each tool owns its schema and lifecycle; tws provides location, discovery, and orchestration links.

A higher-level “super tws” spanning multiple project workspace roots remains research-only. The workspace-level `spaces.yaml` registry should be field-tested first: it may solve most learning/ticket/patch discovery needs without another hierarchy. A later investigation can test whether a small cross-project index that discovers existing `.tws` roots adds enough value. No nested workspace hierarchy should be introduced without a concrete workflow that cannot be solved by discovery and links.

## Research / P3

- **workmux and tesserasessions session-provider research**: preserve the
  [workmux comparison](research/workmux-market-research.md) and the
  [tws-tss collaboration plan](integrations/tesserasessions-collaboration.md).
  The delivered tss runtime-status contract remains authoritative; joint work
  should extend it with durable provider identity and versioned control rather
  than create a competing status schema. A local coordination mailbox is
  discoverable with `tws space show tws-tss-session-design --workspace`.
- **Historical tpatch artifact repair**: after upgrading tpatch, audit old bundled shared-file features topologically, repair canonical patch boundaries/dependencies carefully, regenerate recipes, and publish a verification report. This is backlog maintenance and does not block product work while code gates and recipe replay remain green.
- PR provider adapters for GitHub and Azure DevOps.
- Workspace portability refinements and structured retrospective export.
- Template conflict resolution (`keep`, `replace`, `diff`, `edit`).
- Gitignored scratch-context initialization and ignore verification.
- Repository-specific recipes distributed as templates/skills rather than core commands.

## Stretch goals

- Patch-identity-assisted sync through a tesserapatch contract.
- Patch-theory-backed multi-parent composition after semantics and failure recovery are defined.
- External tracker frontier/status projection through a tesseratickets contract.

## Non-goals

- Turning `tws decisions` into a general ticket tracker.
- Reimplementing tpatch patch identity, reconciliation, or patch theory inside tws.
- Owning tesseratickets storage or lifecycle.
- Hard-coding repository-specific proto, Bazel, local-module-replace, or release workflows in core tws.
- Implicit multi-parent rebases without explicit merge/composition semantics.
