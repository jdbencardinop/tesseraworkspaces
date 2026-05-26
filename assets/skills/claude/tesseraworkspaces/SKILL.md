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
| `tws add <feature> [-n <branch>] [--open] [--tmux]` | Create feature (and optionally a branch) |
| `tws new <feature> <branch> [--base <parent>] [--force]` | Create a worktree branch |
| `tws open [feature] [branch] [--tmux] [--no-agent]` | Open worktree and run agent |
| `tws sync <feature> [--push] [--verbose]` | Rebase worktrees in dependency order |
| `tws push <feature> [--dry-run]` | Push all branches with --force-with-lease |
| `tws stack <feature>` | Show branch dependency tree |
| `tws list` / `tws ls` | List features and branches |
| `tws delete <feature>` | Remove feature and all worktrees |
| `tws archive <feature> <branch>` | Remove worktree, keep branch ref |
| `tws decide <feature> "<summary>" [--type T] [--to B]` | Record a decision |
| `tws decisions show <feature> [--mine] [--all]` | View decisions |
| `tws decisions ack <feature>` | Mark decisions as read |
| `tws inject <feature> [branch]` | Sync inject/ files into worktrees |
| `tws doctor [feature]` | Run health checks |
| `tws rename feature <old> <new>` | Rename a feature |
| `tws rename branch <feature> <old> <new>` | Rename a branch |
| `tws config show/set/get` | Manage configuration |
| `tws close <feature> <branch>` | Kill tmux session |
| `tws template sync <feature> [--template <dir>]` | Backfill templates |
| `tws init [--agent claude\|copilot]` | Install agent skills |
| `tws --version` | Print version |

### Quick Start

Create a feature, branch, and open the agent in one command:

```sh
tws add auth -n auth-models --open
tws add auth -n auth-models --open --tmux    # with tmux
tws add auth -n existing-branch --open       # with existing branch
```

### Stacked Branches

Use `--base` to declare dependencies. Branches without `--base` default to `main` (parallel, not stacked):

```sh
tws new auth auth-models                          # base: main
tws new auth auth-middleware --base auth-models    # stacks on auth-models
tws new auth auth-tests --base auth-models         # diverges (parallel to middleware)
```

`tws sync` rebases in topological order. Divergent stacks are supported (A→B, A→C). If a rebase fails, only that branch's descendants are skipped — sibling lineages continue.

### Workspace Layout

```
<workspace-root>/                    # e.g., ../myapp.tws/
  .tws-workspace                     # workspace marker
  <feature>/
    FEATURE.md
    stack.yaml                       # branch dependency graph
    decisions.yaml                   # cross-worktree decisions
    read-state.yaml                  # per-branch last-read tracking
    inject/                          # shared files symlinked into worktrees
      CLAUDE.local.md               # shared context (edit here, all worktrees see it)
      .claude/skills/               # per-feature agent skills
    worktrees/
      <branch>/                      # full git worktree checkout
        CLAUDE.local.md → ../../inject/CLAUDE.local.md  (symlink)
```

### Context Injection

Files in `inject/` are symlinked into every worktree. Edit once, all worktrees see changes.

**Important:** Injected files appear as untracked in git status. Either:
- Add them to `.gitignore` (e.g., `CLAUDE.local.md`)
- Place them in an already-ignored subfolder (e.g., `inject/.claude/`)

Re-sync after adding new files: `tws inject <feature>`

### Cross-Worktree Decisions

Agents can broadcast or target decisions to sibling worktrees:

```sh
tws decide <feature> "Changed User.ID to uuid" --type breaking
tws decide <feature> "Review API surface" --type review --to auth-middleware
tws decisions show <feature>           # unread only (default)
tws decisions show <feature> --all     # everything
tws decisions ack <feature>            # mark all as read
```

Decision types: `breaking` | `info` | `deprecation` | `review` | `question`

### Configuration

```sh
tws config show                              # show resolved config
tws config set agent_command opencode        # change agent
tws config set use_tmux true --repo          # per-repo setting
tws config set test_command "go build ./..."  # validation after rebase
```

Config files: global (`~/.config/tws/config.yaml`), per-repo (`.tws/config.yaml`). Env: `TWS_ROOT`.

Config keys: `agent_command`, `use_tmux`, `inject_into`, `test_command`.

### Sync and Push

```sh
tws sync <feature>                # fetch (quiet) + rebase in dependency order
tws sync <feature> --push        # sync + push all branches
tws sync <feature> --verbose     # show full fetch output
tws push <feature>               # push all branches with --force-with-lease
tws push <feature> --dry-run     # preview what would be pushed
```

If `test_command` is configured, it runs after each successful rebase. Validation failure skips dependent branches.

### Health Checks

```sh
tws doctor                # check all features
tws doctor auth           # check one feature
```

Detects: wrong branch, uncommitted changes, missing inject symlinks.

## When to Use

- When the user wants to work on multiple branches in parallel within a feature
- When setting up stacked diffs/PRs
- When managing worktrees for agent workflows
- Run `tws list` to see current features and branches before suggesting actions
- Run `tws stack <feature>` to understand branch dependencies before syncing
- **Run `tws decisions show <feature>` at the start of each session** to check for updates
- After making a breaking change, **record it with `tws decide`** so sibling agents know
- Run `tws doctor` if something seems wrong (branch mismatch, missing files)
