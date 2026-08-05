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
| `tws sync <feature> [--push] [--verbose] [--continue] [--abort]` | Rebase worktrees in dependency order |
| `tws push <feature> [--dry-run]` | Push all branches with --force-with-lease |
| `tws export <feature> [--full] [--to-repo] [-o file]` | Export workspace metadata |
| `tws import <file> [--from-repo <feature>]` | Import workspace from YAML or tarball |
| `tws stack <feature>` | Show branch dependency tree |
| `tws list` / `tws ls` | List features and branches |
| `tws delete <feature>` | Remove feature and all worktrees |
| `tws archive <feature> <branch>` | Remove worktree, keep branch ref |
| `tws decide <feature> "<summary>" [--type T] [--to B]` | Record a decision |
| `tws decisions show [feature] [--mine] [--all]` | View decisions (auto-detects feature) |
| `tws decisions ack [feature]` | Mark decisions as read |
| `tws inject <feature> [branch] [--into <path>]` | Sync inject/ files into worktrees |
| `tws hooks install [feature]` | Install Claude Code auto-read hooks |
| `tws hooks remove [feature]` | Remove auto-read hooks |
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

Use `--base` to declare dependencies. An omitted base uses the selected repository's `origin/HEAD`. Explicit refs are literal: `master` means local `master`, while `origin/master` means the remote-tracking ref. Tags and commit SHAs are also accepted.

```sh
tws new auth auth-models                              # selected repo's origin/HEAD
tws new auth auth-middleware --base auth-models        # stacks on auth-models
tws new auth auth-tests --base auth-models             # diverges (parallel to middleware)
tws new auth release-check --base origin/release        # explicit remote ref
tws new auth wiki-docs --repo ../wiki --base master     # base resolved in wiki repo
```

From an existing feature directory, `tws new` infers a single source repository from feature metadata/worktrees. Multi-repo features require `--repo` when the source is ambiguous.

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
tws sync <feature> --continue    # resume after conflict resolution
tws sync <feature> --abort       # discard sync state, start fresh
tws push <feature>               # push all branches with --force-with-lease
tws push <feature> --dry-run     # preview what would be pushed
```

If `test_command` is configured, it runs after each successful rebase. Validation failure skips dependent branches.

**Conflict recovery:** When sync hits a conflict, it saves state and prints instructions. After resolving, run `tws sync <feature> --continue`; deferred descendants return to pending and are rebased before completion. If another branch fails, the updated state is preserved. `Sync complete` is printed only after configured parent-child ancestry is current.

**Amend-aware:** If a parent branch was amended, sync uses `--onto` to avoid ghost conflicts from stale SHAs.

### Workspace Portability

```sh
tws export auth                          # YAML to stdout
tws export auth -o auth.yaml             # YAML to file
tws export auth --full -o auth.tar.gz    # tarball with inject files
tws export auth --to-repo                # save to .tws/workspaces/ (travels with git push)
tws import auth.yaml                     # recreate from YAML
tws import auth.tar.gz                   # recreate from tarball
tws import --from-repo auth              # recreate from .tws/workspaces/
```

### Auto-Read Hooks

Install hooks so Claude Code automatically checks for new decisions:

```sh
tws hooks install auth          # install on all worktrees in feature
tws hooks install                # auto-detect feature from cwd
tws hooks remove auth            # uninstall hooks
```

This writes `.claude/settings.local.json` in each worktree with SessionStart hooks that run `tws decisions show --mine` on startup and resume. Unread decisions appear automatically at the start of each session.

**Note:** `tws decisions show` and `tws decisions ack` work without a feature argument when run from inside a worktree — the feature is auto-detected from the path.

### Health Checks

```sh
tws doctor                # check all features
tws doctor auth           # check one feature
```

Detects: wrong branch, uncommitted changes, missing inject symlinks.

## Checkout Workspace Mode

For small repositories that use one physical checkout instead of linked worktrees:

```sh
tws init --mode checkout       # enable local .tws/ metadata (git-excluded)
tws enable --mode checkout     # same as above; unified helper for both commands
tws mode                       # show resolved mode and metadata root
tws add auth                   # metadata under .tws/features/auth
tws new auth auth-models       # create/register a Git branch, no linked worktree
git switch auth-models         # activate the logical branch manually
```

Checkout mode creates `.tws/features/` and `.tws/state/` and adds `.tws/` to
`.git/info/exclude` (local ignore, not committed). Must be run from the main
worktree (rejects linked worktrees with `.git` file).

To migrate legacy features from `.tws/<name>` to `.tws/features/<name>`:

```sh
tws migrate-layout --all       # preflight all, rollback on failure
tws migrate-layout auth        # single feature
```

Checkout mode is explicit; legacy repositories remain in external mode. It is intentionally single-repository and does not accept `tws new --repo`. `tws archive` hides a logical branch without deleting the Git branch; `tws delete` preserves branches unless an explicit branch-deletion flag is supplied.

Checkout mode supports transactional stack sync in the single physical checkout:

```sh
tws sync auth                 # sequential branch switch/rebase, then restore original branch
tws sync auth --continue      # resume an interrupted/conflicted transaction
tws sync auth --abort         # abort rebase and restore original branch
tws sync auth --push          # push only after complete ancestry + restoration
```

Sync refuses dirty/detached repositories and concurrent operations, persists recovery state under `.tws/state/`, supports amend-aware rebases and validation, and restores the original branch on success/abort. `tws open`, `tws close`, and automatic hooks are still deferred to the checkout-agent-sessions feature.

## When to Use

- When the user wants to work on multiple branches in parallel within a feature
- When setting up stacked diffs/PRs
- When managing worktrees for agent workflows
- Run `tws list` to see current features and branches before suggesting actions
- Run `tws stack <feature>` to understand branch dependencies before syncing
- **Run `tws decisions show <feature>` at the start of each session** to check for updates
- After making a breaking change, **record it with `tws decide`** so sibling agents know
- Run `tws doctor` if something seems wrong (branch mismatch, missing files)
