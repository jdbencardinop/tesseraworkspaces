# tws and tesserasessions collaboration plan

Status: proposed for joint review

Date: 2026-09-11

## Existing foundation

tesserasessions (`tss`) already ships the first integration contract:

[`tesserasessions/docs/runtime-status-contract.md`](https://github.com/jdbencardinop/tesserasessions/blob/8cb4ae7d1eec761fc626f67ad0d01ff1206a50d6/docs/runtime-status-contract.md)

That contract is authoritative for side-effect-free live status queries. It
already defines:

- the ownership boundary between tws and tss;
- a schema-v1 batch request and response;
- path, repository, and branch matching;
- provider freshness and per-query errors;
- separate `runtime_presence` and `agent_state`;
- merge precedence that preserves tws-owned live evidence;
- the planned `tws/tss-status-enrichment` consumer feature.

The joint work must extend this foundation. It must not create a second status
schema or read the tss SQLite database directly.

## Product boundary

### tws owns

- workspace, feature, stack entry, Git branch, repository, and worktree
  topology;
- worktree creation, rename, archive, removal, and Git synchronization;
- checkout branch switching and restoration;
- inject/templates and durable decisions;
- sessions tws launches and the local evidence needed to recover them;
- local locks, failed stages, stale state, and destructive-action gates;
- final `tws status` rollups.

### tss owns

- historical coding-agent inventory;
- cross-agent runtime discovery;
- Herdr/tmux and later live providers;
- semantic agent state and its provenance;
- provider-specific attach, open, send, read, run, wait, reap, and resurrection;
- multiplexer target identity;
- provider capability and freshness reporting.

### Neither project should own

- another agent's transcript store;
- a hosted coordination service;
- silent remote LLM summarization;
- Git topology inferred from terminal names;
- terminal ownership inferred only from a lossy worktree name.

## Integration slices

### Slice 1: tss status enrichment

Register a tws `tss-status-enrichment` feature after joint acceptance of the
consumer behavior.

The adapter should:

- invoke `tss status --json` with a bounded timeout;
- send one query per tws status entry;
- validate schema version and complete output before consuming it;
- keep the baseline tws report unchanged when tss is absent, times out, or
  returns incompatible data;
- never replace tws-owned `present` evidence with tss `absent` or `unknown`;
- allow tss present/blocked/stale evidence to raise activity or attention;
- publish provider availability/freshness additively if exposed.

The normative request, response, and precedence rules already live in the tss
runtime status contract.

### Slice 2: session control provider

The existing tss commands (`attach`, `open`, `send`, `read`, and `run`) are
useful operator commands, but they currently produce or execute shell command
strings based on inventory/runtime rows.

The joint PRD should define a versioned provider contract suitable for tws:

| Operation | Purpose |
| --- | --- |
| `ensure` | Create or adopt a provider target from a desired-session spec |
| `attach` | Return to an existing target without changing Git topology |
| `observe` | Return bounded runtime and semantic state |
| `close` | Close only the provider-owned target |
| `send` | Deliver input with explicit safety/quoting semantics |
| `read` | Read bounded recent output |
| `run` | Run a command in a new provider surface |
| `wait` | Wait for a declared semantic/runtime condition |
| `resurrect` | Recreate a dead target from provider-owned durable state |

Potentially interactive operations must retain a `--print` or plan mode.

### Slice 3: durable provider identity

tws should persist an opaque provider reference, not a tmux session name.

The provider observation should include:

- provider and backend;
- provider instance ID;
- stable session/target ID;
- window/pane/surface IDs when applicable;
- runtime presence;
- agent state;
- process/pane birth identity;
- observation and expiry timestamps;
- capability set;
- optional adoption/match evidence.

Path remains evidence, never sole identity. Git topology remains tws-owned.

### Slice 4: agent-state provenance

The contract should state how each semantic value was observed:

- native provider state;
- agent lifecycle hook;
- foreground command heuristic;
- transcript/store evidence;
- unknown.

Consumers need source, confidence, observation time, and expiry. `ready` must
continue to mean a live agent can accept input, not the absence of a runtime.

### Slice 5: optional workmux provider

Do not integrate workmux directly into tws.

After the provider contract is stable, tss may investigate a capability-gated
workmux adapter for:

- tokenized worktree/window identity;
- rich layouts;
- lifecycle hook status;
- attention routing;
- resurrection;
- multi-agent fan-out.

Native Herdr and tmux remain required baseline providers. An absent workmux
binary must never reduce existing behavior.

## Desired-session request

The ADR/PRD should converge on a request shaped approximately like this:

```json
{
  "schema_version": 1,
  "request_id": "open-auth-models",
  "operation": "ensure",
  "session": {
    "workspace_id": "ws-123",
    "feature": "auth",
    "entry": "models",
    "path": "/workspaces/auth/models",
    "repo_root": "/repos/app",
    "git_branch": "feature/auth-models",
    "agent_command": ["claude"],
    "profile": "worktree-agent",
    "attach_policy": "attach-or-create"
  }
}
```

The exact field set is not decided by this document.

## Coordination channel

The local collaboration mailbox is:

```text
../tesserasessions/.gitignored/tws-tesserasessions/
```

tws registers that directory as a sibling space named
`tws-tss-session-design`.

The mailbox is for coordination and draft handoffs only. It is intentionally
ignored by Git. Durable accepted work must be copied into the owning
repository:

- tss provider contracts and provider-facing PRDs live in tesserasessions;
- tws consumer behavior and adapter ADRs live in tesseraworkspaces;
- joint decisions are referenced from both.

### Mailbox protocol

```text
tws-tesserasessions/
|-- README.md
|-- status/
|   |-- tws.yaml
|   `-- tss.yaml
|-- messages/
|   `-- <timestamp>-<sender>-to-<recipient>-<topic>.md
|-- drafts/
|   `-- README.md
`-- prompts/
    `-- tesserasessions-kickoff.md
```

Rules:

1. Each project writes only its own `status/<project>.yaml`.
2. Messages are immutable one-file handoffs; replies create a new file.
3. Drafts name one current editor in their header.
4. Never put transcripts, prompts from real users, credentials, databases, or
   agent store paths in the mailbox.
5. Accepted ADRs/PRDs move into Git and the mailbox records their repository,
   commit, path, and SHA-256.
6. Cross-repository tpatch dependencies are recorded as external references in
   the status files; native tpatch DAGs remain repository-local.
7. A design is ready only when both status files record the same artifact hash
   with `verdict: approved`.

### Status vocabulary

Use:

```text
proposed
drafting
reviewing
changes-requested
approved
implemented
superseded
blocked
```

## Initial joint deliverables

| ID | Deliverable | Owner | Initial state |
| --- | --- | --- | --- |
| `ADR-RUNTIME-BOUNDARY` | Runtime and control ownership boundary | Joint | proposed |
| `PRD-TSS-STATUS-CONSUMER` | tws adapter for existing status schema | tws | proposed |
| `PRD-SESSION-CONTROL` | Versioned session-control provider contract | tss-led | proposed |
| `ADR-SESSION-IDENTITY` | Durable provider session identity and adoption | Joint | proposed |
| `THREAT-SESSION-CONTROL` | Quoting, target ownership, stale identity, and destructive-operation threat model | Joint | proposed |
| `RESEARCH-WORKMUX-PROVIDER` | Optional workmux provider feasibility | tss | deferred |

## Readiness gates

An ADR or PRD is ready to register for implementation when:

- ownership and non-goals are explicit;
- request/response schemas are versioned and total;
- timeout, absence, incompatibility, and stale-data behavior are specified;
- native Herdr/tmux baseline behavior is preserved;
- security and target-ownership rules are reviewed;
- both project status files approve the same content hash;
- the final artifact is committed in its owning repository;
- each implementation feature records the external artifact URL/commit/hash.

## Terminology: workspace mode versus session mode

Two independent axes are easy to conflate.

### Workspace mode

`external` and `checkout` describe how tws materializes Git work:

| Workspace mode | Git layout |
| --- | --- |
| `external` | Feature metadata plus one linked worktree per active entry |
| `checkout` | Repository-local metadata and one physical checkout that switches branches |

### Session launch mode

`direct` and `tmux` describe how the agent/shell process is launched:

| Session launch | Meaning |
| --- | --- |
| `direct` | tws starts the agent as a child process in the current terminal, then normally starts a shell after the agent exits |
| `tmux` | tws creates or attaches to a tmux target and runs the agent there |

The axes combine:

| Workspace | Launch | Current behavior |
| --- | --- | --- |
| external | direct | Default external `tws open`; one token-owned record per invocation |
| external | tmux | One deterministic branch session; feature `--all` creates a multi-window session |
| checkout | direct | Locks the checkout, switches to the entry branch, runs agent/shell, then restores |
| checkout | tmux | Locks and switches the checkout, creates durable session state, and restores on close |

Therefore "direct session" is not a third workspace mode. It is the default
non-tmux launch path. The familiar sibling `<repo>.tws` layout is external
workspace mode; opening one of its worktrees without `--tmux` is an
external-direct session.
