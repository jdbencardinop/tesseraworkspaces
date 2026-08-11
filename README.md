# tesseraworkspaces

A CLI tool for creating feature-scoped workspaces with multiple git worktrees. Work on parallel branches or stacked diffs within a single feature, each with its own coding agent. Built for teams and solo developers who use AI coding agents in parallel.

## Quick Start

```sh
# Install
go install github.com/jdbencardinop/tesseraworkspaces/cmd/tws@latest

# Create a feature with a branch and start coding
cd ~/projects/myapp
tws add auth -n auth-models --open
```

## How it works

1. **Add a feature** — creates a workspace with shared context and inject files
2. **Create worktrees** — spin up isolated branches, stacked or parallel
3. **Open a worktree** — launch your coding agent in the worktree directory
4. **Decide** — broadcast design decisions to sibling worktrees
5. **Sync** — rebase all branches in dependency order, amend-aware
6. **Push** — push all branches with `--force-with-lease`

## Features

### Stacked & Divergent Branches

```sh
tws new auth auth-models                              # selected repo's origin/HEAD
tws new auth auth-middleware --base auth-models        # stacks on auth-models
tws new auth auth-tests --base auth-models             # parallel to middleware
tws new auth release-check --base origin/release        # explicit remote ref
tws new auth wiki-docs --repo ../wiki --base master     # local master in wiki repo

# Result:
# (<default>)
# └── auth-models
#     ├── auth-middleware
#     └── auth-tests
```

### Cross-Worktree Agent Communication

Agents in different worktrees can communicate via decisions:

```sh
tws decide auth "Changed User.ID to uuid" --type breaking
tws decide auth "Review API surface" --type review --to auth-middleware
tws decisions show                  # auto-detects feature, shows unread only
tws decisions ack                   # mark as read
```

With hooks installed, Claude Code agents see new decisions automatically on session start:

```sh
tws hooks install auth              # install on all worktrees
tws config set auto_hooks true      # auto-install on every tws new
```

### Smart Sync

```sh
tws sync auth                       # quiet fetch + rebase in dependency order
tws sync auth --push                # sync + push all branches
tws sync auth --continue            # resume after conflict resolution
tws sync auth --abort               # discard sync state
```

- **Amend-aware** — uses `--onto` to avoid ghost conflicts from amended commits
- **Archived branch support** — syncs archived branches via `--update-refs` or optimistic rebase
- **Post-rebase validation** — run `test_command` after each rebase (e.g., `go build ./...`)
- **Conflict recovery** — saves state, guides resolution, resumes with `--continue`

### Context Injection

Shared files in `inject/` are symlinked into every worktree:

```sh
# Edit once, all worktrees see changes
echo "# Auth context" > ../myapp.tws/auth/inject/CLAUDE.local.md

# Re-sync after adding new files
tws inject auth

# Target a gitignored subdirectory
tws inject auth --into .context
```

### Multi-Repo Workspaces

Work on code and docs repos in the same feature:

```sh
tws add auth -n code-branch
tws new auth wiki-docs --repo ~/projects/myapp-wiki
```

### Workspace Portability

```sh
tws export auth                     # YAML to stdout
tws export auth --to-repo           # save to .tws/workspaces/ (travels with git push)
tws export auth --full -o auth.tar.gz  # tarball with inject files
tws import --from-repo auth         # recreate on another machine
```

### 3-Tier Skill System

- **Worktree skills** — injected into each worktree, agents work on code
- **Orchestrator skill** — auto-installed in feature dir, coordinates agents
- **Global skills** — installed via `tws init`, knows how to create workspaces

```sh
tws init                            # install Claude + Copilot skills
tws init --agent claude             # Claude only
tws init --register --register-alias myapp   # also enroll in the global registry
```

## All Commands

| Command | Description |
|---------|-------------|
| `tws add <feature> [-n branch] [--open] [--tmux]` | Create feature workspace |
| `tws new <feature> <branch> [--base] [--repo] [--force]` | Create worktree branch |
| `tws open [feature] [branch] [--tmux] [--no-agent]` | Open worktree (interactive picker if no args) |
| `tws sync <feature> [--push] [--continue] [--abort] [--verbose]` | Rebase in dependency order |
| `tws push <feature> [--dry-run]` | Push all branches |
| `tws stack <feature>` | Show dependency tree |
| `tws list` / `tws ls` | List features and branches |
| `tws delete <feature>` | Remove feature and worktrees |
| `tws archive <feature> <branch>` | Remove worktree, keep branch |
| `tws decide <feature> "<msg>" [--type] [--to]` | Record a decision |
| `tws decisions show [feature] [--mine] [--all]` | View decisions |
| `tws decisions ack [feature]` | Mark decisions as read |
| `tws inject <feature> [branch] [--into path]` | Sync inject files |
| `tws doctor [feature]` | Health checks |
| `tws rename feature/branch` | Rename feature or branch |
| `tws config show/set/get` | Manage configuration |
| `tws hooks install/remove [--all]` | Manage agent hooks |
| `tws export <feature> [--full] [--to-repo]` | Export workspace |
| `tws import <file> [--from-repo]` | Import workspace |
| `tws template sync [--all] [--template dir]` | Backfill templates |
| `tws close <feature> <branch>` | Kill tmux session |
| `tws registry add <path> [--alias name]` | Register a repo/workspace for discovery |
| `tws registry list/show/check [--json]` | Inspect registered workspaces |
| `tws registry alias <selector> <alias> [--remove]` | Manage aliases |
| `tws registry repair <selector> <new-path> [--allow-identity-change]` | Re-point a moved entry |
| `tws registry remove <selector>` | Drop registry metadata (never deletes files) |
| `tws registry prune --missing [--force]` | Drop entries whose targets are gone |
| `tws space add <name> <path> --kind <kind> [--description text] [--feature f]` | Link a tool-owned sibling space |
| `tws space list [--feature f] [--all] [--kind k] [--json]` | Discover linked sibling spaces (bare list is cwd-scoped) |
| `tws space show <name> [--feature f \| --workspace] [--json]` | Show one linked space |
| `tws space remove <name> [--feature f \| --workspace]` | Drop the link (never deletes the target) |
| `tws init [--agent] [--force] [--register] [--register-alias name]` | Install agent skills |

### Global Workspace Registry

Opt-in discovery index at `${XDG_DATA_HOME:-~/.local/share}/tws/registry.yaml`
(directory `0700`, file `0600`). Nothing is created until you enroll explicitly.

```sh
tws registry add . --alias myapp          # enroll the current repo/workspace
tws init --register --register-alias myapp  # enroll after a successful init
tws registry list --json                  # deterministic output; empty is []
tws registry check                        # ok / missing / mismatched / invalid
tws registry repair myapp /new/path       # re-point after a move
tws registry prune --missing --force      # --force required in non-TTY use
```

Selectors are exact: entry ID, alias, or canonical path. Aliases may not shadow
an entry ID or a registered path.

**Identity and markers.** Git-backed targets carry a small opaque marker at
`.git/tws/workspace-id`; checkout mode and linked worktrees share the main
repository's Git common directory. External workspaces use
`.tws-workspace/workspace-id`. Markers are created only on explicit enrollment,
survive moves and workspace-mode switches, and detect replacement.

- Moved target: `tws registry repair <selector> <new-path>` — no extra flag needed.
- Replaced target (marker or Git identity changed): add `--allow-identity-change`.
- `tws registry remove`/`prune` only drop registry metadata; targets and marker
  files are never deleted.

### Workspace sibling links

A tws workspace is surrounded by tool-owned sibling spaces: learning notes,
ticket stores, patch metadata, research, and authored documentation. `tws space`
records **where** they live in `<workspace-root>/spaces.yaml` so agents and
humans discover them by command instead of by hard-coded path.

```sh
tws space add learning ./learning --kind learning --description "notes"
tws space add patching ./acme/patching --kind patching --feature acme
tws space list                        # cwd-scoped: workspace-wide + detected feature
tws space list --all                  # the complete registry, from anywhere
tws space list --json                 # deterministic output; empty is []
tws space list --feature acme         # workspace-wide entries plus acme's
tws space show learning --workspace   # scope selectors disambiguate a shared name
tws space remove learning --workspace # drops the link; never deletes the target
```

- **Location metadata only.** `tws` never reads, writes, validates, or deletes
  the content of a linked space, and it never learns the linked tool's schema or
  lifecycle. `tws space add` is the only command that creates anything for this
  feature, and it never creates the target directory.
- **Where the file lives.** External mode uses the resolved external root
  (`$TWS_ROOT` when set); checkout mode uses `<repo>/.tws` and ignores
  `TWS_ROOT`, as every other checkout command does. `tws space list` always
  prints `Workspace: <root> (mode: <mode>, scope: <scope>)` before its results,
  including the empty state, so the active file and scope are unambiguous.
- **Default scope is your location.** A bare `tws space list` shows every
  workspace-wide entry plus the entries of the feature you are inside when one
  is detected; outside a feature it is already complete. Use `--all` for the
  complete registry from anywhere, and `--kind` to filter. `--json` is a bare
  array with no header. A filter that hides everything says so and reports how
  many entries are registered, which is never confused with an empty registry.
- **Scope selectors.** When the same name exists workspace-wide and inside a
  feature, `tws space show` / `tws space remove` report the ambiguity and
  accept `--workspace` or `--feature <name>` (mutually exclusive) to select
  exactly one.
- **Two path forms.** Targets inside the workspace root are stored
  workspace-relative and stay portable; targets outside are stored absolute. A
  target must exist and be a directory, but it does **not** need to be a Git
  repository.
- **Local state.** `spaces.yaml` and `.spaces.lock` are mode `0600`, are not
  shared, and are not included in `tws export` / `tws import`. The advisory lock
  is POSIX-only (macOS and Linux).
- **Feature-name protection.** A registered target directory can never
  masquerade as a feature. Ownership is decided by filesystem identity, so a
  hand-edited absolute path inside the workspace root, a symlinked spelling, or
  a different letter case on a case-insensitive volume is recognised as the
  same directory. `tws add`, `new`, `delete`, `rename`, `archive`,
  `sync`, `export`, `import`, `open`, `stack`, `inject`, `push`, `decide`,
  `doctor`, `template sync`, `hooks install`, and `tws migrate-layout` all
  refuse a feature name owned by a registered space, and feature listings
  exclude it. `tws delete` and `tws migrate-layout` refuse when a registered
  target lives inside the feature — `migrate-layout` never rewrites a registered
  path, it names the blockers and the exact scope-qualified
  `tws space remove` command for each, and `--all` is all-or-nothing — and
  `tws rename feature` rewrites relative entries while refusing pinned absolute
  ones.
- **Strict on untrusted metadata.** If `spaces.yaml` exists but is unreadable,
  symlinked, malformed, carries an unknown field, or declares a future schema
  version, every command that consults workspace features or spaces exits
  nonzero having changed nothing. Only shell completion degrades, silently
  offering no candidates. When the file is absent — the normal state — nothing
  is created and every pre-existing command behaves exactly as before.
- **Inside a sibling space the enclosing `.tws-workspace` marker wins**, so
  `tws space list` keeps targeting the parent workspace even when the space is
  its own Git repository. No `spaces.yaml` or `.tws` directory is ever created
  for the sibling repo.

## Requirements

- [Go](https://go.dev/dl/) 1.26+
- [git](https://git-scm.com/)
- [tmux](https://github.com/tmux/tmux) (optional, for `tws open --tmux`)
- A coding agent: [Claude Code](https://claude.ai/claude-code) (default), [OpenCode](https://opencode.ai), [Aider](https://aider.chat), or any CLI agent

## Configuration

```sh
tws config set agent_command opencode        # change agent
tws config set use_tmux true --repo          # per-repo tmux default
tws config set test_command "go build ./..."  # post-rebase validation
tws config set auto_hooks true               # auto-install hooks on tws new
tws config set inject_into .context          # inject target subdirectory
```

Config files: `~/.config/tws/config.yaml` (global), `.tws/config.yaml` (per-repo). Env: `TWS_ROOT`.

Shell completions: `tws completion zsh/bash/fish/powershell`

## Documentation

- [Cheatsheet](docs/cheatsheet.md)
- [Configuration](docs/configuration.md)
- [Agent Hooks](docs/hooks.md)
- [v1.2 RC Validation](docs/v1.2-rc-validation.md)
- [Roadmap](docs/roadmap.md)

## Install from Source

```sh
git clone https://github.com/jdbencardinop/tesseraworkspaces.git
cd tesseraworkspaces
make install       # installs to $GOPATH/bin/tws
make build         # builds to bin/tws with version from git tag
```

## License

[MIT](LICENSE)
