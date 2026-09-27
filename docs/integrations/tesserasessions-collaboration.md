# tws and tesserasessions collaboration plan

Status: runtime-boundary ADR jointly approved and published; dependent PRDs pending

Updated: 2026-09-27

## Approved artifact references

The canonical
[`ADR-RUNTIME-BOUNDARY`](https://github.com/jdbencardinop/tesserasessions/blob/53e63f751611b084c492c1f1179a6002c20a3125/docs/adrs/ADR-001-tws-tss-runtime-control-boundary.md)
is owned by tss and jointly approved. Its published bytes are identical to the
approved mailbox draft; the mailbox draft is now superseded.

| Artifact | Repository | Commit | Path | SHA-256 |
| --- | --- | --- | --- | --- |
| Runtime/control boundary | `jdbencardinop/tesserasessions` | `53e63f751611b084c492c1f1179a6002c20a3125` | `docs/adrs/ADR-001-tws-tss-runtime-control-boundary.md` | `8dd2face55393a0a98d51db683c3d6192ece10136784b14224e70a9a82e00c9a` |
| Approved planning snapshot | `jdbencardinop/tesseraworkspaces` | `e251db119c32a87375687931ca5fdd7b9139a621` | `docs/integrations/tesserasessions-collaboration.md` | `e6ca0b83a13a4c6e8223d762265b7f70d7baccdb7d8f20898f32d0070b8608bf` |

The planning approval applies to the pinned historical bytes, not every later
revision of this file. This document now records tws follow-ups and links the
canonical decisions; it is not a second runtime-status or control contract.
Artifact provenance uses the commit containing the approved bytes, never a
later mailbox-only coordination commit.

The accepted ADR requires fresh exact `observe_ref` evidence to govern its
referenced target. Status-v1 path observations remain independent unless tss
provides generation-bound equivalence evidence; they may add activity or
attention but never recover control authority. Stale exact evidence cannot
erase fresher discovery, and tws-owned liveness, failures, and final rollups
retain authority. The control PRD must keep `screen_heuristic` and
`foreground_command` as distinct provenance sources.

`PRD-TWS-SESSION-CONTROL-CLIENT` is a tws-owned follow-up: the sole canonical
persisted provider-slot state machine, native-session conflict checks, opaque
reference storage, crash recovery, and unresolved-quarantine UX. It consumes
the approved tss control protocol rather than redefining it. Direct-process
birth identity and fail-closed native tmux identity checks precede provider
reference storage or provider-backed launch.

Status-consumer PRD drafting remains independent of control work and uses the
existing status-v1 contract. Control work proceeds through the joint identity
ADR and threat model, the tss control PRD, then the tws client PRD. Each
implementation still needs its own approved, committed feature boundary.

Lane B runtime-boundary coordination is converged. Product priorities remain
in the separate [tws roadmap](../roadmap.md); this coordination work does not
start or reorder product implementation. Workmux remains deferred.

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
- desired provider-session topology and launch intent after validating the
  repository, branch, worktree, checkout lock, and agent invocation;
- worktree creation, rename, archive, removal, and Git synchronization;
- checkout branch switching and restoration;
- inject/templates and durable decisions;
- direct child-process sessions tws launches and the local evidence needed to
  recover them;
- the decision that a provider-backed session should close;
- local locks, failed stages, stale state, and destructive-action gates;
- final `tws status` rollups.

### tss owns

- historical coding-agent inventory;
- cross-agent runtime discovery;
- Herdr/tmux and later live providers;
- semantic agent state and its provenance;
- provider execution and target-specific revalidation;
- provider-specific ensure, observe, literal-text send, bounded read, safe
  wait, and owned-target close where capabilities permit;
- minting, decoding, and revalidating opaque provider references;
- multiplexer target identity and generation;
- provider capability and freshness reporting.

For provider-backed sessions, tws authorizes a destructive close from its
topology; tss still performs the close and refuses when ownership, provider
instance, target generation, or scope no longer matches.

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

The existing human commands stay available. They are not automatically safe
machine APIs because several currently compose shell command strings and
`run` joins arguments before `sh -lc`.

Control schema v1 should be deliberately narrow and one-operation-per-process:

| Operation | Purpose |
| --- | --- |
| `capabilities` | Side-effect-free provider capability discovery |
| `ensure` | Create a new provider-owned target only when creation is idempotently recoverable by `request_id` |
| `observe_ref` | Revalidate and observe one exact provider reference |
| `send_text` | Send a JSON literal string with separate `submit: none|enter` semantics |
| `read_text` | Return bounded recent output with source, byte/line limits, format, and truncation evidence |
| `wait_state` | Wait only when occupant/generation pinning is proved and revalidated |
| `close_owned` | Close only a target created by v1 `ensure`, after ownership/generation checks |

Explicitly excluded from control v1:

- inherited-TTY `attach`;
- generic `run_argv` until a provider preserves argv boundaries end to end;
- foreign target adoption;
- arbitrary terminal key programs;
- force close and general reaping;
- resurrection.

These exclusions do not remove the current human `attach`, `open`, `send`,
`read`, and `run` commands. They separate an operator command from a stable
machine contract.

`ensure` uses `request_id` as its idempotency key. Reusing a request ID with a
different normalized request fails. A provider advertises `ensure.v1` only if
it can recover the original target after a lost response. Timeout after
dispatch yields `outcome_unknown`; retrying the same request ID is
reconciliation, not a fresh create.

### Slice 3: durable provider identity

tss mints and owns provider references. tws stores each reference byte-for-byte
and never extracts provider internals or ownership tokens.

A minimal consumer-visible envelope is:

```json
{
  "kind": "tss_provider_ref",
  "version": 1,
  "provider": "herdr",
  "opaque": "<provider-specific opaque value>"
}
```

Internally, the opaque value binds provider, provider instance, stable target,
target generation/birth identity, tss ownership nonce, and the idempotent
ensure request ID. Every target-specific operation, including reads,
revalidates it. Path, branch, target name, PID, and pane ID are evidence, not
authority.

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
- state provenance and confidence;
- diagnostic surface/match evidence.

Path remains evidence, never sole identity. Git topology remains tws-owned.

### Slice 4: agent-state provenance

Status schema v1 remains unchanged. Provenance and confidence first belong to
the new reference-scoped control observation; a later status-v2 decision may
reuse them.

The control observation should state how each semantic value was observed:

- native provider state;
- agent lifecycle hook;
- screen-content heuristic, distinct from foreground-command evidence;
- foreground command heuristic;
- persisted-store evidence;
- unknown.

Consumers need source, confidence, observation time, and expiry. `ready` must
continue to mean a live agent can accept input, not the absence of a runtime.
Use closed qualitative confidence values rather than floating-point scores.

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

The initial adapter, if ever registered, should be read-only. Control requires
a documented versioned public workmux schema, provider instance and target
generation, public ownership semantics, capability discovery, structured
errors, and supported version ranges. Current private state files,
`@workmux_*` options, and hook commands are insufficient contracts.

## Desired-session request

The ADR/PRD should converge on an envelope shaped approximately like this:

```json
{
  "schema_version": 1,
  "request_id": "open-auth-models-01",
  "operation": "ensure",
  "consumer": {
    "name": "tws",
    "correlation_id": "workspace-entry-opaque",
    "metadata": {
      "workspace_id": "ws-123",
      "feature": "auth",
      "entry": "models"
    }
  },
  "provider_selection": {
    "preferred": ["herdr", "tmux"],
    "required_capabilities": ["ensure.v1", "observe_ref.v1"]
  },
  "provider_ref": null,
  "input": {
    "cwd": "/workspaces/auth/models",
    "repo_root": "/repos/app",
    "git_branch": "feature/auth-models",
    "agent": {
      "kind": "claude",
      "argv": ["claude"],
      "profile": "worktree-agent"
    },
    "create_policy": "create_new"
  }
}
```

Rules to preserve:

- unknown request fields fail in schema v1;
- provider selection finishes before mutation;
- no fallback occurs after a mutating provider call begins;
- environment values remain excluded until the threat model defines
  redaction/storage behavior;
- one request performs one operation.

Expected response outcomes are:

```text
succeeded
partial
failed
outcome_unknown
```

Partial creation returns provider reference and completed-step evidence.
Stale/replaced/ownership-mismatched references fail without path/name/PID
fallback.

The exact final field set remains a PRD decision.

### Capability vocabulary

Candidate capabilities:

```text
ensure.v1
observe_ref.v1
send_text.v1
read_text.v1
wait_state.v1
close_owned.v1
attach_interactive.v1   # later
run_argv.v1             # reserved
adopt_foreign.v1        # later
resurrect.v1            # later
```

Clients never infer capability from a provider name. Constraints report text
and output bounds, submit modes, read formats, wait predicates, maximum
timeouts, close scope, idempotency, and generation guarantees.

### Timeout and failure rules

- Invalid/incompatible requests fail before provider invocation.
- Provider absence or timeout before dispatch is `failed` and may be retried.
- Timeout after mutation dispatch is `outcome_unknown`; no automatic provider
  failover or mutation retry occurs.
- Reusing the same idempotent `ensure` request ID is the only automatic
  reconciliation mechanism for an unknown create outcome.
- A partial result includes its provider reference and completed-step
  evidence.
- Stale, replaced, or ownership-mismatched references never fall back to path,
  name, branch, pane ID, or PID.
- Close validates ownership generation rather than volatile activity/content
  revision.
- Orphans may remain when the provider or reference is unavailable; force
  close and general reaping are not v1 behavior.

### Threat-model minimum

The joint threat model must cover:

- argv/shell injection and literal text versus terminal-key semantics;
- output bounds, truncation, and sensitive screen content;
- provider instance, boot, target generation, and same-user spoofing;
- path/repository/branch as candidate evidence but never authority;
- stale evidence and unknown mutation outcomes;
- absence of foreign target adoption;
- exact owned-scope destructive close with no force flag;
- PID reuse for tws direct sessions using process-start/host-boot identity
  where available;
- environment-secret omission and redaction.

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

## Joint deliverables

| ID | Deliverable | Owner | State |
| --- | --- | --- | --- |
| `ADR-RUNTIME-BOUNDARY` | Runtime and control ownership boundary | tss canonical, joint review | approved and published at the pinned reference above |
| `PRD-TSS-STATUS-CONSUMER` | tws adapter for existing status schema | tws | accepted for drafting against status v1 |
| `PRD-SESSION-CONTROL` | Narrow versioned session-control provider contract | tss-led | accepted for drafting after identity/threat-model agreement |
| `PRD-TWS-SESSION-CONTROL-CLIENT` | Provider-slot state machine and recovery UX consuming the tss protocol | tws | accepted follow-up; awaits approved control protocol |
| `ADR-SESSION-IDENTITY` | Durable provider session identity and reference storage | tss canonical, joint review | accepted for drafting |
| `THREAT-SESSION-CONTROL` | Quoting, target ownership, stale identity, and destructive-operation threat model | tss with tws topology/direct-process addendum | accepted for drafting |
| `RESEARCH-WORKMUX-PROVIDER` | Optional workmux provider feasibility | tss | deferred |

## Accepted review decisions

The first tss review accepted:

- status schema v1 unchanged;
- status enrichment ready for a tws-owned consumer PRD;
- tss ownership of provider references;
- byte-opaque reference storage in tws;
- no foreign adoption in control v1;
- no resurrection in control v1;
- workmux provider deferred;
- tws direct processes remain tws-owned.

The review requested the narrower control operation set above, explicit lost
response/idempotency handling, qualitative state provenance, and a joint
threat model before implementation.

## Proposed tpatch feature split after approval

No feature below should be registered until its owning ADR/PRD is approved.

### tesserasessions

```text
provider-session-identity
provider-observation-provenance
        \            /
         session-control-contract
             |-- herdr-control-provider-v1
             `-- tmux-control-provider-v1
```

`workmux-runtime-provider` remains deferred and should start read-only if a
versioned public workmux contract becomes available.

### tesseraworkspaces

```text
agent-work-status-dashboard
        `-- tss-status-enrichment

direct-session-birth-identity
        `-- provider-session-reference-storage
                `-- tss-session-control-client
```

Cross-repository dependencies are recorded as canonical artifact
repository/commit/path/hash references, not native tpatch edges.

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

`direct`, `tmux`, and feature-wide `all` describe launch styles, not workspace
modes:

| Session launch | Meaning |
| --- | --- |
| `direct` | tws starts the agent as a child process in the current terminal, then normally starts a shell after the agent exits |
| `tmux` | tws creates or attaches to a tmux target and runs the agent there |
| `all` | External feature-wide tmux launch with orchestrator and worktree windows; not an additional workspace mode |

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
