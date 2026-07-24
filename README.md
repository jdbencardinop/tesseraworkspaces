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
| `tws init [--agent] [--force]` | Install agent skills |

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
