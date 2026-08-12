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
```

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
6. **Sync**: Run `tws sync <feature>` to keep branches up to date
7. **Review**: Check decisions from worktree agents for review requests or questions

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
