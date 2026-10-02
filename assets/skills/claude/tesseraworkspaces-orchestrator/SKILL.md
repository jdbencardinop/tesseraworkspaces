---
name: tesseraworkspaces-orchestrator
description: Orchestrate work across multiple worktree agents in a feature workspace.
---

# tesseraworkspaces Orchestrator — Claude Code Skill

## What This Is

You are an orchestrator agent running from a feature workspace directory. Your job is to coordinate work across multiple worktree branches, track progress, communicate decisions, and manage the feature's lifecycle.

**You are NOT in a git repository.** You cannot edit code directly. Instead, you manage the agents working in individual worktrees via decisions and tws commands.

## Your Capabilities

### View state
```sh
tws status --json                 # agent work status for every branch (poll this first)
tws list                          # see all features and branches
tws stack <feature>               # see dependency tree
tws stack status <feature> --json # ancestry, materialization, upstream, parent counts
tws decisions show                # see all decisions (auto-detects feature)
tws decisions show --all          # include already-read decisions
tws doctor <feature>              # check health of all worktrees
tws space list --json --feature <feature>  # resolve sibling spaces before delegating
tws space list --json --all                # complete registry; a bare list is cwd-scoped
```

Resolve learning, ticket, patching, research, and documentation locations with
`tws space list --json --feature <feature>` before delegating work. Use the
`resolved_path` field; never hard-code a sibling path. An empty result is normal.
A bare `tws space list` is scoped to the current directory; pass `--all` when
you need the complete registry regardless of where the command runs.

### Communicate with worktree agents
```sh
# Broadcast to all agents
tws decide <feature> "Design decision: use UUID for all IDs" --type breaking

# Send to a specific branch agent
tws decide <feature> "Review the API surface" --type review --to <branch>

# Ask a question to a specific branch
tws decide <feature> "Should we use sync or async?" --type question --to <branch>

# Acknowledge decisions you've read
tws decisions ack
```

### Manage the stack
```sh
tws sync <feature>                # rebase all branches in order
tws sync <feature> --push         # sync + push
tws sync <feature> --continue     # resume after conflict
tws push <feature>                # push all branches
tws push <feature> --dry-run      # preview pushes

# Sync modes — three independent axes, no flags keeps today's behaviour
tws sync <feature> --only <entry>     # scope: one logical stack entry
tws sync <feature> --from <entry>     # scope: that entry and its descendants
tws sync <feature> --local-only       # propagation: local parent tips only, never advance a root
tws sync <feature> --no-fetch         # input refs: no automatic network input (--push still allowed)

# Plan before a wide sync
tws sync <feature> --plan --json --max-replay-per-entry <n>   # preview: old base, new base, candidates per entry
```

Selectors are logical `stack.yaml` names, never Git branches. Scoped/local-only,
archived, and checkout rebases use `git -c rebase.updateRefs=false rebase`, so
repository or inherited `rebase.updateRefs=true` never moves a branch outside
the selection and Git 2.26-2.37 remains supported. Intentional full external
`--update-refs` still requires Git 2.38. Incompatible
combinations are refused before any fetch, lock, or rebase. Every new ordinary
sync `--push` is strict: it stops at the first rejected push and `--continue`
retries only entries that were never pushed. A `scope=all` run still pushes the
whole feature; standalone `tws push` retains lenient failure behavior.
The shared feature mutation guard rejects concurrent syncs of one feature.
Before mutation, sync freezes a validated effective cutoff for every selected
row. Read `entries[].cutoff.recorded_sha`, `effective_sha`, `source`,
`validity`, and `reason`; do not approve an invalid cutoff. Missing records use
the exact current parent tip only when Git proves it is an ancestor of the
child. tws never guesses a merge-base or fork point. Legacy remaining replay
without safely reconstructible cutoff evidence refuses continuation.
Safe pre-mutation recovery and publication/cleanup-only recovery remain
supported under their ownership and phase restrictions; never recommend abort
after publication starts.
A proven collateral C→U transition records U as the next old cutoff but never
overrides explicitly configured tag/OID destination C: a later explicit sync
must plan and execute `--onto C U`, replay only U..child, and record C after
success. If the collateral transition cannot be proven, sync refuses before
that rebase and preserves phase-correct recovery evidence.
Doctor renders sanitized raw/effective/source/validity/reason cutoff evidence
for evaluated rows. Missing-record parent advancement requires known-history
repair rather than a plain rebase or an ordinary sync that preflight refuses.

**Recover ordinary sync transactionally.** New runs preserve exact selected
branch tips and exact pre-run `stack.yaml` bytes before mutation. `--abort`
uses per-repository compare-and-swap ref updates and refuses rather than
overwriting later user work; refs, metadata, and holder state are separate
effects, so multi-repository recovery is not atomic as one unit. If native Git
reflog evidence cannot attribute an allowed ref change to tws's rebase,
recovery refuses.

Treat validators as ref/checkout-read-only. If validation commits, rebases,
resets, moves a ref, switches branches, or detaches `HEAD`, preserve the
journal and work and direct the operator to manual recovery; never claim tws
can safely adopt or erase those changes.

Immediately before the first push **attempt**, sync records publication.
After that boundary only `--continue` is valid; never direct an operator to
`--abort`. Older recovery state has no complete snapshot and warns that abort
cannot fully restore earlier movement. Successful no-flag sync remains the
compatibility target.

Once all forward effects succeed, sync durably records completion before
deleting protection refs. A later recovery verb only finishes cleanup and
reports the completed run; it does not roll back refs or metadata.

If external `stack.yaml` is absent, the legacy compatibility sync requires an
interactive warning and confirmation or `--allow-nontransactional`. It has no
complete rollback and earlier branches may remain moved. Noninteractive use
refuses by default; malformed or unreadable metadata always refuses.

**Plan before a wide sync.** Run `--plan` first and read its `entries[]` rows
before rebasing several branches at once: each row's old base, new base, and
`candidates` count — an upper bound, never a promise of what gets applied —
shows which entries are about to move a lot. Use the rows to narrow scope with
`--only <entry>` or `--from <entry>` instead of letting a wide run touch
entries nobody asked about. `--plan` is not a network no-op: it fetches
exactly where the run it describes fetches (external by default; a checkout
plan only under `--fetch`), and it exits `0` even when it describes a
refusal, so decide from `runnable && !guard.would_refuse &&
guard.execute_blocked_by == [] && refusal.kind == null`, never from its exit
status. Broadcast the plan with `tws decide <feature> "<summary of the
plan>" --type review` (or `--type breaking` for a base change) before
executing, so worktree agents see the rebase coming. To execute, extract the
fingerprint (`sed -n 's/^Approval fingerprint: //p'`, never `tail -1`) and
re-run with the same limit (`--max-replay-per-entry <n>` bounds one entry,
`--max-replay-total <n>` bounds the whole invocation) and
`--approve-plan <fingerprint>` — one of those two limits is required on every
route, `--plan` included, so a plan with no limit mints no fingerprint and
there is no limitless approval; a refused
guard exits `1` with one `plan-guard: <kind>: <detail>` stderr line — a
`state-preserved: ` prefix on the detail means something on disk outlives the
refusal — while a refusal tws already performs (dirty tree, held lock,
unresolvable base, incomplete previous run) keeps its own wording and carries
no marker.

### Reparent before a wide sync

```sh
tws stack reparent <feature> <entry> --onto <dest> --plan --json \
  --max-replay-total <n>                                             # preview, exits 0
tws stack reparent <feature> <entry> --onto <dest> \
  --approve-plan <fingerprint> --max-replay-total <n>                 # execute
tws stack reparent <feature> --continue                               # resume
tws stack reparent <feature> --abort                                  # roll back
```

Use the same replay limit flag(s) and values on preview and execution. A
limitless preview has a null fingerprint and cannot be approved.
The preview moves no branch and writes no tws state; may fetch according to
policy.

When a reparent plan's `target` and `descendants[]` show a branch replaying a
lot because its recorded parent is simply **wrong** — squash-merged, abandoned,
or never the intended base — a wider `tws sync` will not fix it. Reparent that
entry first, then sync.
`tws sync` replays onto the parents already recorded; `tws stack reparent`
changes the parent and replays only the affected closure.

Broadcast with `tws decide <feature> "<summary>" --type breaking` before
executing: a reparent rewrites the history worktree agents are sitting on.

**Machine admission — fresh route.** Execute only when all of:

```text
plan.runnable == true
  && plan.guard.would_refuse == false
  && plan.guard.execute_blocked_by == []
  && plan.refusal.kind == null
  && plan.approval.usable == true
```

**Machine admission — continue route.** Resume only when all of:

```text
plan.route == "continue"
  && plan.runnable == true
  && plan.guard.would_refuse == false
  && plan.guard.execute_blocked_by == []
  && plan.refusal.kind == null
  && plan.approval.scope == "resume"
```

The continue predicate deliberately does **not** require an approval
fingerprint: `--continue` never takes one, and the persisted run already
carries the frozen decision and its limits.

Destination storage is canonical: a stack-entry destination stores the logical
entry name, a named literal ref stores its full `refs/...` name, and a raw
object-id destination stores the full lowercase OID.

Never branch on `--plan`'s exit status: a plan-only run exits `0` even when it
publishes a refusal. A reparent refusal is one `reparent: <kind>: <detail>`
line on stderr; a `state-preserved: ` prefix means something on disk outlives
it. A conflict is a **pause**, not a refusal, and carries no marker.

While a reparent is recorded, `tws sync <feature>` refuses on every verb
(`--plan` included). Checkout mode blocks opening every feature in the
workspace while its single physical checkout is owned by the run; external
direct/tmux/all launches publish scoped intent before their final mutation
check. tws changes no remote ref and no pull request. Checkout sync/reparent
mutations serialize workspace-wide, checkout refuses non-empty entry `repo`
values, and top-level external push holds the feature mutation lock across all
entries, clearing obsolete follow-up rows before lease preflight. The next push
waits for a proven commit point, then warns and uses
`--force-with-lease --force-if-includes`.
Every mutating external sync route holds that same lock through rebase,
metadata, optional push and remote follow-up clearing, rechecks reparent state
after claiming, and releases last. If the checkout-global lock is absent,
current tws scans every feature's recoverable checkout sync/reparent state
before fresh mutation and recovery reconstructs only its own reservation.
Checkout feature-directory opens hold the workspace launch intent through the
agent/shell after a final guard check. Different stored `repo` spellings on one
logical edge are refused even when they resolve to one common directory.

Atomicity is bounded precisely: a concurrent write to any expected-old ref
aborts the ref transaction during prepare; this is not reader snapshot
isolation. Ref commit is crash-atomic only on reftable. Refs, `stack.yaml`, and
worktree/index state are separate effects, and recovery after the single commit
point proceeds forward.

The old-binary guarantee is same-feature and post-envelope only. v1.2.16
cannot observe window 1, workspace-global locks, unrelated-feature checkout
reparent or top-level push. **Do not use an older tws while any reparent is
active or recoverable.**

### Manage worktrees
```sh
tws new <feature> <branch> --base <parent>   # create new branch
tws archive <feature> <branch>                # free disk space
tws delete <feature>                           # remove entire feature
```

### Export/Import
```sh
tws export <feature>              # export workspace metadata
tws export <feature> --to-repo    # save to repo for sharing
```

## Orchestration Workflow

0. **Poll status**: Run `tws status --json` first. Act on
   `.workspace.attention.status == "needs_attention"` and then on
   `report.issues[]`, which is the single home of every signal. **Never act on
   `agent_state`: it is always `unknown` at this version; use
   `needs_attention`.** **`attention.status` inherits upward: a workspace or
   feature can be `needs_attention` with `issue_count: 0` because a child is —
   read `report.issues[]` for the detail.** **A `present` from tws means a
   process with that PID exists, not that that exact process exists.** The
   command exits 0 whenever a report was produced, so a non-zero exit means the
   workspace itself could not be read. Use `tws status <feature> --json` when
   coordinating one feature: it builds only that feature plus workspace
   evidence (including the shared checkout session), while the no-argument form
   remains global. Git/tmux subprocesses are capped at 5 seconds each and share
   a 30-second budget; failed facts are `null`/`unknown` with issues. Filesystem
   reads are not interruptible by that subprocess budget.
1. **Start**: Run `tws list` and `tws stack <feature>` to understand current state
2. **Check decisions**: Run `tws decisions show` for any updates from worktree agents
3. **Plan**: Based on the stack and decisions, decide what each branch should work on
4. **Communicate**: Use `tws decide` to send instructions or design decisions to branches
5. **Monitor**: Run `tws doctor <feature>` to check for issues
6. **Pre-sync check**: Run `tws stack status <feature> --json` and inspect
   `ancestry.status` and `materialization.dirty` before syncing; resolve dirty
   or divergent branches first. **`tws stack status` never fetches; upstream and
   parent counts describe local refs only.** **A null field means tws could not
   establish the fact locally — it never means clean, attached, zero, or no
   upstream.** **`tws stack -- status` prints the legacy tree for a feature
   literally named `status`.**
7. **Sync**: Run `tws sync <feature>` to keep branches up to date
8. **Review**: Check decisions from worktree agents for review requests or questions

## Decision Types

| Type | When to use |
|------|------------|
| `breaking` | API changes, schema changes, anything that affects other branches |
| `info` | Design decisions, context sharing, FYI notices |
| `deprecation` | Something being removed or replaced |
| `review` | Request a worktree agent to review something |
| `question` | Ask a worktree agent for input |

## Important

- You cannot edit code — delegate code changes to worktree agents via decisions
- Run `tws decisions show` regularly to stay updated
- After communicating, worktree agents will see your decisions on their next session start (via hooks)
- Use `tws doctor` before `tws sync` to catch branch mismatches
- Doctor's stack ancestry is advisory: `stale` just means the parent moved and
  `tws sync` fixes it, and `divergent` only means the recorded base commit left
  the parent's history so sync must replay with `--onto` (with mode-specific
  flags). Neither is an emergency and both exit 0
- `tws status` is read-only: it never removes a session record, and tws never
  kills a direct agent process. To free a branch held by a live record, ask the
  agent to exit its session. `tws close` reports — but never removes — records it
  cannot verify, and points at `tws status --json` when they are all that remains
- The feature directory contains: FEATURE.md (goals), stack.yaml (dependencies), decisions.yaml (communication log), inject/ (shared files)
