---
name: tesseraworkspaces
description: Manage feature-scoped workspaces with stacked git worktrees for parallel agent workflows.
---

# tesseraworkspaces (tws) — Claude Code Skill

## What This Is

tesseraworkspaces is a CLI tool for creating feature-scoped workspaces with multiple git worktrees. It lets you work on parallel branches or stacked diffs within a single feature, each with its own coding agent.

## CLI Reference

`tws` is a compiled Go binary on PATH. Invoke it directly:

```sh
tws <command> [args]
```

### Commands

| Command | Description |
|---------|-------------|
| `tws add <feature>` | Create a feature workspace |
| `tws new <feature> <branch> [--base <parent>] [--force]` | Create a worktree branch |
| `tws open <feature> <branch> [--tmux]` | Open worktree and run agent |
| `tws sync <feature>` | Rebase worktrees in dependency order |
| `tws stack <feature>` | Show branch dependency tree |
| `tws list` / `tws ls` | List features and branches |
| `tws delete <feature>` | Remove feature and all worktrees |
| `tws archive <feature> <branch>` | Remove worktree, keep branch ref |
| `tws init [--agent claude\|copilot]` | Install agent skills |
| `tws --version` | Print version |

### Stacked Branches

When creating branches, use `--base` to declare dependencies:

```sh
tws new auth auth-models                          # base: main (default)
tws new auth auth-middleware --base auth-models    # stacks on auth-models
tws new auth auth-routes --base auth-middleware    # stacks on auth-middleware
```

This creates a `stack.yaml` in the feature directory:

```yaml
branches:
  - name: auth-models
    base: main
  - name: auth-middleware
    base: auth-models
  - name: auth-routes
    base: auth-middleware
```

`tws sync` rebases in topological order (parents first). If a rebase fails, dependent branches are skipped.

### Workspace Layout

```
<workspace-root>/                    # e.g., ../myapp.tws/
  .tws-workspace                     # workspace marker
  <feature>/
    FEATURE.md
    CLAUDE.local.md                  # shared context across worktrees
    stack.yaml                       # branch dependency graph
    worktrees/
      <branch>/                      # full git worktree checkout
```

### Configuration

Global config at `~/.config/tws/config.yaml`:

```yaml
workspaces:
  /path/to/repo: /custom/workspace/path
agent_command: claude                # default agent for tws open
use_tmux: false                      # wrap tws open in tmux
```

Environment variable `TWS_ROOT` overrides all config.

## When to Use

- When the user wants to work on multiple branches in parallel within a feature
- When setting up stacked diffs/PRs
- When managing worktrees for agent workflows
- Run `tws list` to see current features and branches before suggesting actions
- Run `tws stack <feature>` to understand branch dependencies before syncing
