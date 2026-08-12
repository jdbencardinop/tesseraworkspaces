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
  explicit unevaluated state, with a reason and guidance per edge.

Current target: **stack status** — the P1 items below. It consumes the shipped
`StackEdge` projection rather than computing ancestry of its own.

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
  by "Stack status" below, which carry the
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

## P1 stack safety and observability backlog

- **Sync modes**: support local-only propagation, no-fetch operation, surgical branch/descendant sync, and explicit root targets.
- **Rebase plan guard**: show the old base, new base, and replay count; stop surprising broad rebases before they start.
- **Stack status**: show local head, configured parent and parent head, ancestry state, dirty/rebase state, upstream, and ahead/behind counts. Consumes the shipped `StackEdge` projection.
- **Safe reparent/restack**: update Git and metadata atomically for a single-parent stack.

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
