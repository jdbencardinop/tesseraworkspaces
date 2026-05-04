---
mode: agent
description: Manage feature workspaces and stacked worktrees with tws
tools: terminal, editFiles, readFile
---

# tesseraworkspaces (tws)

You are working in a project that uses `tws` for feature-scoped workspaces with stacked git worktrees.

## CLI Reference

`tws` is a compiled Go binary on PATH. Invoke it directly:

- `tws add <feature>` — Create a feature workspace
- `tws new <feature> <branch> [--base <parent>] [--force]` — Create a worktree branch
- `tws open <feature> <branch> [--tmux]` — Open worktree and run agent
- `tws sync <feature>` — Rebase worktrees in dependency order
- `tws stack <feature>` — Show branch dependency tree
- `tws list` — List features and branches
- `tws delete <feature>` — Remove feature and all worktrees
- `tws archive <feature> <branch>` — Remove worktree, keep branch ref

## Stacked Branches

Use `--base` to declare dependencies between branches:

```sh
tws new auth auth-models                          # base: main
tws new auth auth-middleware --base auth-models    # stacks on auth-models
```

`tws sync` rebases in topological order. If a parent fails, children are skipped.

## Workspace Layout

```
<workspace-root>/
  <feature>/
    FEATURE.md
    stack.yaml          # branch dependency graph
    worktrees/<branch>/ # git worktree checkouts
```

## Configuration

Config: `~/.config/tws/config.yaml`

```yaml
workspaces:
  /path/to/repo: /custom/workspace/path
agent_command: claude
use_tmux: false
```

Override with `TWS_ROOT` env var.

## Workflow

1. Run `tws list` to see current state
2. Run `tws stack <feature>` to understand dependencies
3. Use `tws sync <feature>` to keep branches up to date
4. Use `tws archive` to free disk space, `tws new` to restore
