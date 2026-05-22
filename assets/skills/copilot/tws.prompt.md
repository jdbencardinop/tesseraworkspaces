---
mode: agent
description: Manage feature workspaces and stacked worktrees with tws
tools: terminal, editFiles, readFile
---

# tesseraworkspaces (tws)

You are working in a project that uses `tws` for feature-scoped workspaces with stacked git worktrees.

## CLI Reference

`tws` is a compiled Go binary on PATH. Invoke it directly:

- `tws add <feature> [-n <branch>] [--open]` — Create feature (quick start with -n)
- `tws new <feature> <branch> [--base <parent>] [--force]` — Create worktree branch
- `tws open [feature] [branch] [--tmux] [--no-agent]` — Open worktree and run agent
- `tws sync <feature>` — Rebase worktrees in dependency order
- `tws stack <feature>` — Show branch dependency tree
- `tws list` — List features and branches
- `tws delete <feature>` — Remove feature and all worktrees
- `tws archive <feature> <branch>` — Remove worktree, keep branch ref
- `tws decide <feature> "<summary>" [--type T] [--to B]` — Record a decision
- `tws decisions show <feature> [--mine] [--all]` — View decisions (unread by default)
- `tws decisions ack <feature>` — Mark all decisions as read
- `tws inject <feature> [branch]` — Sync inject/ files into worktrees
- `tws doctor [feature]` — Run health checks
- `tws rename feature/branch` — Rename feature or branch
- `tws config show/set/get` — Manage configuration
- `tws close <feature> <branch>` — Kill tmux session
- `tws template sync <feature> [--template <dir>]` — Backfill templates

## Stacked & Divergent Branches

```sh
tws new auth auth-models                          # base: main
tws new auth auth-middleware --base auth-models    # stacks on auth-models
tws new auth auth-tests --base auth-models         # diverges (parallel)
```

Sync rebases in topological order. Divergent stacks (A→B, A→C) are supported.

## Context Injection

Files in `inject/` are symlinked into every worktree. Edit once, all worktrees see changes.
Injected files appear as untracked in git — add them to `.gitignore` or use an ignored subfolder.

## Decisions

```sh
tws decide <feature> "Changed X" --type breaking           # broadcast
tws decide <feature> "Review Y" --type review --to <branch> # targeted
tws decisions show <feature>                                 # unread only
tws decisions ack <feature>                                  # mark as read
```

## Workflow

1. Run `tws list` to see current state
2. Run `tws decisions show <feature>` to check for unread decisions from siblings
3. Run `tws stack <feature>` to understand dependencies
4. Use `tws sync <feature>` to keep branches up to date
5. After breaking changes, run `tws decide <feature> "summary" --type breaking`
6. Run `tws doctor` if something seems wrong
7. Use `tws archive` to free disk space, `tws new` to restore
