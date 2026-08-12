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
| `tws registry add/list/show/check/...` | Manage opt-in global workspace discovery |
| `tws space add/list/show/remove` | Link and discover tool-owned sibling spaces |
| `tws doctor [feature]` | Run health checks |
| `tws status [feature] [--json]` | Agent work status per branch |
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

### Global Workspace Registry

The registry is opt-in discovery state stored under the XDG data directory. It never owns or deletes repositories/workspaces.

```sh
tws registry add /path/to/repo --alias rp
tws registry list
tws registry show rp
tws registry check rp
tws registry repair rp /new/path
tws registry alias rp aks-rp
tws registry remove rp
tws registry prune --missing --force   # --force required in non-TTY use
```

`tws init --register --register-alias <name>` enrolls the current repository after a successful initialization; it fails with a not-a-Git-repository error outside a repo. Selectors are exact ID, alias, or canonical path; do not guess fuzzy names. An alias may not shadow an entry ID or a registered path.

`tws registry list --json` and `tws registry check --json` emit an array (`[]` when empty) with snake_case keys.

**Workspace identity and markers.** On explicit enrollment, tws writes a small opaque marker file inside tool-owned metadata. Git-backed targets use `.git/tws/workspace-id`; checkout mode and linked worktrees share the main repository's Git common directory. External workspaces use `.tws-workspace/workspace-id`. The marker survives moves and workspace-mode switches.

- `tws registry check` is read-only; it never creates or repairs markers.
- Moved target → `tws registry repair <selector> <new-path>` succeeds with no extra flag because the marker and Git identity are unchanged.
- Replaced or deleted marker → status `mismatched`; rerun repair with `--allow-identity-change` only when the replacement is intentional.
- `tws registry remove` and `tws registry prune` drop registry metadata only; targets and marker files are never deleted.

### Sibling Spaces

A workspace is surrounded by tool-owned sibling spaces — learning notes, ticket stores, patch metadata, research, authored docs. `tws` records **where** they live; the linked tool owns the content.

**Never hard-code a sibling path. Always discover it:**

```sh
tws space list --json --all               # the complete registry; [] when empty
tws space list --json                     # cwd-scoped: workspace-wide + detected feature
tws space list --json --feature <feature> # workspace-wide links plus that feature's
tws space show <name> --json              # add --workspace or --feature <f> if ambiguous
```

A bare `tws space list` is scoped to your current location: it shows every
workspace-wide link plus the links of the feature you are inside when one is
detected. Use `--all` whenever you need the complete view regardless of cwd.

`--json` emits snake_case keys including `resolved_path`, `scope`, `scope_status`, and `status`. Use `resolved_path` as the directory to work in; treat `status: missing` and `scope_status: feature-missing` as "report it, do not repair it".

Registering a link is explicit and never creates the target:

```sh
tws space add learning ./learning --kind learning --description "notes"
tws space add patching ./<feature>/patching --kind patching --feature <feature>
tws space remove learning --workspace     # drops the link only; scope selector is explicit
```

- An empty result is normal on a fresh clone — do not invent paths, ask or register one.
- `--kind` is a free token (`learning`, `tickets`, `patching`, `research`, `docs` are conventions, not a closed set).
- A registered directory is never a feature: `tws add/new/delete/rename/...` and `tws migrate-layout` refuse that name, and it is excluded from `tws list`. Ownership is decided by filesystem identity, so a different letter case or an absolute spelling of the same directory is refused too.
- When one name exists both workspace-wide and inside a feature, `show`/`remove` report the ambiguity; disambiguate with `--workspace` or `--feature <feature>`.
- If `spaces.yaml` is malformed or from a newer schema, feature and space commands fail loudly. Fix or remove the file; never work around it.

### Health Checks

```sh
tws doctor                # check all features
tws doctor auth           # check one feature
```

**External mode** detects: wrong branch, uncommitted changes, missing inject symlinks, and per-edge stack ancestry when the source repository is resolvable. Ancestry findings appear as one issue per edge (`ancestry <status>: <reason>` plus a hint), one informational `ancestry note:` per edge whose base a sync path would resolve differently, and at most one feature-scoped `repo-unavailable` or `repo-source-mismatch` informational line. External doctor also runs from the workspace root or a feature directory, and still reports the non-Git checks when no source repository can be found.

**Checkout mode** produces a comprehensive read-only report covering:
- Workspace identity (mode, stable ID, repo, metadata root)
- Current branch (or detached HEAD), dirty state, active Git operation (merge/rebase/cherry-pick/revert)
- Sync transactions: discovers all `.tws/state/*-checkout-sync.*` entries, classifies live/stale/invalid, shows stage/failure/lock PID and exact `--continue`/`--abort` guidance
- Agent session: PID or tmux liveness, state/lock mismatch detection, `tws close` guidance
- Per-entry: logical name, Git branch, archive/ref/current status, base ancestry (`current`/`stale`/`divergent`/`missing`/`cross-repo-unsupported`, or `unevaluated`), local and parent HEAD SHAs, plus an indented `reason:` detail line carrying the reason, `last-base`, and `merge-base`. Archived entries report the same ancestry data but are informational and never counted
- Context links: inspects recorded symlink targets (healthy/missing/replaced/not-symlink)

`stale` means the parent moved forward — run `tws sync`. `divergent` means the
recorded base commit is no longer in the parent's history, so an `--onto` rebase
is required; `tws sync` replays the edge that way too, with the flags its own
workspace mode requires (external sync adds `--update-refs`, checkout sync adds
`--no-fork-point`), so the printed command is an equivalent manual repair, not a
copy of what sync runs. That guidance is a complete, runnable command naming the
full base ref, the full recorded base commit, and the target child branch
explicitly, so it moves that branch instead of detaching HEAD; prose in the same
line stays abbreviated, but nothing inside a backticked command is shortened. `missing` on a child branch never suggests
`tws new` — the stack entry already exists, so the guidance offers restoring the
branch from its remote or a known commit, or deliberately removing and
recreating the entry. `cross-repo-unsupported` means the entry targets another
repository, which tws never probes. `unevaluated` means no ancestry conclusion
was reached (no configured base, or no resolvable source repository). Both
`stale` and `divergent` exit 0. Doctor is strictly read-only and never contacts
a remote.

Doctor never mutates Git state, locks, or files. Warnings and informational issues do not return an error exit; only corrupt metadata or unreadable state does.

### Checkout List

In checkout mode, `tws list` shows:
- Logical branch name and Git branch (when different)
- Current branch marker
- Archived status
- Ancestry status (`stale`/`divergent`/`missing`/`cross-repo-unsupported`/`unevaluated`)
- Active session marker

### Agent Work Status

`tws status [feature] [--json]` is the read-only projection of what tws knows
about each logical branch. With no argument it always covers every feature in
the resolved workspace, from any working directory; pass a feature name to
filter. It works in both workspace modes.

Two axes are never collapsed:

- `runtime_presence` — `present | absent | stale | unknown`. Is a tws-owned
  runtime alive? Derived only from records tws itself wrote plus one tmux
  inventory.
- `agent_state` — `working | ready | blocked | done | unknown`. What is the
  agent doing? **`agent_state` is always `unknown` at this version; use
  `needs_attention`.**

`attention.status` is `needs_attention | active | idle` at three levels
(`entry`, `feature`, `workspace`) and is authoritative. **`attention.status`
inherits upward: a workspace or feature can be `needs_attention` with
`issue_count: 0` because a child is — read `report.issues[]` for the detail.**
Every issue has exactly one home in that flat list.

Caveats worth stating to a human:

- **A `present` from tws means a process with that PID exists, not that that
  exact process exists.**
- tws does not observe an externally launched agent; it is `absent` on the
  tws-owned axis, never inferred.

Exit status is 0 whenever a report was produced, including when branches need
attention or state is stale or corrupt — unlike `tws doctor`. A non-zero exit
means no report could be produced at all.

```sh
tws status
tws status auth --json | jq '.issues[] | select(.severity=="warning")'
tws status --json | jq -r '.workspace.attention.status'
```

In external mode, `tws open <feature> <branch>` (the default, non-tmux path)
records a hidden per-invocation session under `<feature>/.sessions/`. Records
are advisory: `tws status` never removes one, `tws close` removes only provably
dead records, and `tws rename`/`archive`/`delete` refuse while any record is not
provably dead. A record that is neither live nor provably dead (corrupt, or
owned by another user) is reported by `tws close` and left in place. tws never
kills a direct process — exit the session instead.

The human view prints the workspace verdict in its header and every issue in a
block keyed by its own home (`Branch: <feature>/<name>`, `Feature: <feature>`,
`Workspace:`), so a `[!] attn` row always shows its code, message, and guidance
without `--json`.

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

Sync refuses dirty/detached repositories and concurrent operations, persists recovery state under `.tws/state/`, supports amend-aware rebases and validation, and restores the original branch on success/abort.

Checkout mode supports one branch-owning agent session at a time:

```sh
tws open auth auth-models          # run agent + shell, then restore original branch
tws open auth auth-models --tmux   # recorded tmux session keeps branch active
tws close auth auth-models         # kill recorded tmux, clean context, restore
```

Sessions reject dirty/detached repos, active sync, archived/multi-repo entries, and concurrent owners. Injected context is locally excluded and only tws-owned links are cleaned. `--all` and automatic hooks remain unsupported in checkout mode.

## When to Use

- When the user wants to work on multiple branches in parallel within a feature
- When setting up stacked diffs/PRs
- When managing worktrees for agent workflows
- Run `tws list` to see current features and branches before suggesting actions
- Run `tws stack <feature>` to understand branch dependencies before syncing
- **Run `tws decisions show <feature>` at the start of each session** to check for updates
- After making a breaking change, **record it with `tws decide`** so sibling agents know
- Run `tws doctor` if something seems wrong (branch mismatch, missing files)
