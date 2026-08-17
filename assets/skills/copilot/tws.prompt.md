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
- `tws sync <feature> [--push] [--verbose] [--continue] [--abort] [--fetch|--no-fetch] [--full|--local-only] [--only <entry>|--from <entry>]` — Rebase worktrees in dependency order, optionally scoped
- `tws push <feature> [--dry-run]` — Push all branches with --force-with-lease
- `tws export <feature> [--full] [--to-repo]` — Export workspace metadata
- `tws import <file> [--from-repo <feature>]` — Import workspace
- `tws stack <feature>` — Show branch dependency tree
- `tws stack status <feature> [--json]` — Stack ancestry, materialization, and upstream status
- `tws list` — List features and branches
- `tws delete <feature>` — Remove feature and all worktrees
- `tws archive <feature> <branch>` — Remove worktree, keep branch ref
- `tws decide <feature> "<summary>" [--type T] [--to B]` — Record a decision
- `tws decisions show [feature] [--mine] [--all]` — View decisions (auto-detects feature)
- `tws decisions ack [feature]` — Mark all decisions as read
- `tws inject <feature> [branch] [--into <path>]` — Sync inject/ files into worktrees
- `tws hooks install/remove [feature]` — Manage agent hooks
- `tws registry add/list/show/check/...` — Manage opt-in global workspace discovery
- `tws space add/list/show/remove` — Link and discover tool-owned sibling spaces
- `tws doctor [feature]` — Run health checks, including stack ancestry per configured parent-child edge
- `tws status [feature] [--json]` — Agent work status per branch (always workspace-wide unless filtered)
- `tws rename feature/branch` — Rename feature or branch
- `tws config show/set/get` — Manage configuration
- `tws close <feature> <branch>` — Close a session: refuses while a direct record is live, then kills tmux
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

`tws sync <feature>` is transactional in checkout mode: it requires a clean attached checkout, switches/rebases logical branches sequentially, persists recovery state under `.tws/state/`, and restores the original branch. Use `--continue` after resolving conflicts and `--abort` to recover. It must be run from the repository checkout: a cwd inside a linked worktree of the same repository is refused.

Sync modes apply to both workspace modes and are three independent axes: `--fetch`/`--no-fetch` (input refs; external defaults to `fetch`, checkout to `no-fetch`), `--full`/`--local-only` (propagation), and `--only <entry>`/`--from <entry>` (scope, by logical `stack.yaml` name). Running `tws sync <feature>` with no mode flag is unchanged. `--no-fetch` forbids automatic network *input* only — an explicit `--push` is still allowed. A scoped run drops `git rebase --update-refs`, so unselected branches never move. With a scoped flag (`--only`/`--from`), `--push` is strict: the run stops at the first rejected push, keeps its recovery state, and `--continue` retries only the entries that were never pushed; a `scope=all` run pushes the whole feature leniently, as `tws push` does. Do not resume a scoped checkout sync with an older tws; abort it instead.

`tws open <feature> <branch>` runs the configured agent in the repository root and restores the original branch after the agent/follow-up shell exits. `--tmux` keeps the branch owned by a recorded tmux session until `tws close`. Only one checkout session may own the repository; `--all` and automatic hooks remain unsupported.

## Global Workspace Registry

Opt-in discovery index under the XDG data directory. It never owns, moves, or deletes repositories/workspaces.

```sh
tws registry add /path/to/repo --alias rp
tws init --register --register-alias rp    # enroll after a successful init
tws registry list --json                   # array output; empty is []
tws registry check                         # ok / missing / mismatched / invalid
tws registry repair rp /new/path           # moved target: no extra flag needed
tws registry remove rp                     # metadata only; files are untouched
tws registry prune --missing --force       # --force required in non-TTY use
```

Selectors are exact ID, alias, or canonical path — never guess fuzzy names. An alias may not shadow an entry ID or a registered path.

Enrollment writes a small opaque marker file in tool-owned metadata. Git-backed targets use `.git/tws/workspace-id`; checkout mode and linked worktrees share the main repository's Git common directory. External workspaces use `.tws-workspace/workspace-id`. Identity survives moves and workspace-mode switches and detects replacement. `tws registry check` is read-only. Use `--allow-identity-change` on repair only when the target was intentionally replaced.

## Workspace Sibling Links

Learning notes, ticket stores, patch metadata, research, and authored docs live in sibling directories that `tws` locates but never owns. Discover them by command; never hard-code a path.

```sh
tws space list --json --all                # the complete registry; [] when empty
tws space list --json                       # cwd-scoped: workspace-wide + detected feature
tws space list --json --feature <feature>  # workspace-wide links plus that feature's
tws space show <name> --json               # add --workspace or --feature <f> if ambiguous
tws space add learning ./learning --kind learning --description "notes"
tws space remove learning --workspace      # drops the link; never deletes the target
```

Use the `resolved_path` field of the JSON output. `status: missing` and `scope_status: feature-missing` are reports, not repairs. An empty result is normal on a fresh clone — ask or register a link instead of guessing. A bare `tws space list` is scoped to the current directory (workspace-wide links plus the detected feature); use `--all` for the complete view. A registered directory is never a feature: feature commands and `tws migrate-layout` refuse that name by filesystem identity — including a different letter case or absolute spelling — and it is excluded from `tws list`. When a name exists in two scopes, disambiguate `show`/`remove` with `--workspace` or `--feature <feature>`. A malformed or future-schema `spaces.yaml` makes feature and space commands fail loudly; fix the file rather than working around it.

## Agent Work Status

`tws status [feature] [--json]` projects what tws knows about each logical branch. With no argument it always covers every feature in the resolved workspace, from any working directory.

Two axes are never collapsed: `runtime_presence` (`present|absent|stale|unknown`) answers "is a tws-owned runtime alive?", and `agent_state` (`working|ready|blocked|done|unknown`) answers "what is the agent doing?". **`agent_state` is always `unknown` at this version; use `needs_attention`.** **`attention.status` inherits upward: a workspace or feature can be `needs_attention` with `issue_count: 0` because a child is — read `report.issues[]` for the detail.** **A `present` from tws means a process with that PID exists, not that that exact process exists.**

Exit status is 0 whenever a report was produced, including when branches need attention; a non-zero exit means no report could be produced at all. `tws status` is strictly read-only and tws never kills a direct agent process. The human view prints the workspace verdict in its header and every issue in a `Branch:`/`Feature:`/`Workspace:` block, so a `[!] attn` row always shows its guidance without `--json`.

```sh
tws status
tws status auth --json | jq '.issues[] | select(.severity=="warning")'
```

## Workflow

0. Run `tws status --json` and act on `.workspace.attention.status == "needs_attention"` plus `report.issues[]`; never act on `agent_state`
1. Run `tws list` to see current state
2. Run `tws decisions show <feature>` to check for unread decisions from siblings
3. Run `tws stack <feature>` to understand dependencies
4. Run `tws stack status <feature> --json` before syncing and check `ancestry.status` and `materialization.dirty`. `tws stack status` never fetches; upstream and parent counts describe local refs only. A null field means tws could not establish the fact locally — it never means clean, attached, zero, or no upstream. `tws stack -- status` prints the legacy tree for a feature literally named `status`
5. Use `tws sync <feature>` to keep branches up to date
6. Use `tws sync <feature> --push` to sync and push in one command
7. After breaking changes, run `tws decide <feature> "summary" --type breaking`
8. Run `tws doctor` if something seems wrong
9. Use `tws archive` to free disk space, `tws new` to restore
10. Set `test_command` in config for automatic validation after rebase
