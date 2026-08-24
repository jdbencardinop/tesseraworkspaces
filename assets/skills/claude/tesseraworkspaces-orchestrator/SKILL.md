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

Selectors are logical `stack.yaml` names, never Git branches. A scoped run drops
`--update-refs`, so it never moves a branch outside the selection. Incompatible
combinations are refused before any fetch, lock, or rebase. With a scoped flag
(`--only`/`--from`), `--push` is strict: the run stops at the first rejected push
and `--continue` retries only the entries that were never pushed; a `scope=all`
run pushes the whole feature leniently, as `tws push` does. Two concurrent syncs
against one feature remain unsafe.

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
   workspace itself could not be read.
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
