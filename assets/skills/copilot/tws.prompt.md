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
- `tws sync <feature> [--push] [--verbose] [--continue] [--abort]` — Rebase worktrees in dependency order
- `tws push <feature> [--dry-run]` — Push all branches with --force-with-lease
- `tws export <feature> [--full] [--to-repo]` — Export workspace metadata
- `tws import <file> [--from-repo <feature>]` — Import workspace
- `tws stack <feature>` — Show branch dependency tree
- `tws list` — List features and branches
- `tws delete <feature>` — Remove feature and all worktrees
- `tws archive <feature> <branch>` — Remove worktree, keep branch ref
- `tws decide <feature> "<summary>" [--type T] [--to B]` — Record a decision
- `tws decisions show [feature] [--mine] [--all]` — View decisions (auto-detects feature)
- `tws decisions ack [feature]` — Mark all decisions as read
- `tws inject <feature> [branch] [--into <path>]` — Sync inject/ files into worktrees
- `tws hooks install/remove [feature]` — Manage agent hooks
- `tws doctor [feature]` — Run health checks
- `tws rename feature/branch` — Rename feature or branch
- `tws config show/set/get` — Manage configuration
- `tws close <feature> <branch>` — Kill tmux session
- `tws template sync <feature> [--template <dir>]` — Backfill templates

## Stacked & Divergent Branches

```sh
tws new auth auth-models                              # selected repo's origin/HEAD
tws new auth auth-middleware --base auth-models        # stacks on auth-models
tws new auth auth-tests --base auth-models             # diverges (parallel)
tws new auth wiki-docs --repo ../wiki --base master     # base resolved in wiki repo
```

Explicit base refs are literal (`master` is local, `origin/master` is remote); tags and commit SHAs are accepted. Sync rebases in topological order. After resolving a conflict, `tws sync <feature> --continue` resumes deferred descendants and only reports completion after parent-child ancestry is current.

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

## Checkout Workspace Mode

For a small repo using one physical checkout:

```sh
tws init --mode checkout
tws mode
tws add auth
tws new auth auth-models
git switch auth-models
```

Checkout mode stores local metadata under `.tws/features/`, adds `.tws/` to the repo's local Git exclude, creates logical Git branches without linked worktrees, and preserves branches on archive/delete by default. It is single-repository; `--repo` is rejected.

`tws sync <feature>` is transactional in checkout mode: it requires a clean attached checkout, switches/rebases logical branches sequentially, persists recovery state under `.tws/state/`, and restores the original branch. Use `--continue` after resolving conflicts and `--abort` to recover.

`tws open <feature> <branch>` runs the configured agent in the repository root and restores the original branch after the agent/follow-up shell exits. `--tmux` keeps the branch owned by a recorded tmux session until `tws close`. Only one checkout session may own the repository; `--all` and automatic hooks remain unsupported.

## Workflow

1. Run `tws list` to see current state
2. Run `tws decisions show <feature>` to check for unread decisions from siblings
3. Run `tws stack <feature>` to understand dependencies
4. Use `tws sync <feature>` to keep branches up to date
5. Use `tws sync <feature> --push` to sync and push in one command
6. After breaking changes, run `tws decide <feature> "summary" --type breaking`
7. Run `tws doctor` if something seems wrong
8. Use `tws archive` to free disk space, `tws new` to restore
9. Set `test_command` in config for automatic validation after rebase
