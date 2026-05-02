# tesseraworkspaces

A CLI tool for creating feature-scoped workspaces with multiple git worktrees. Work on parallel branches or stacked diffs within a single feature, each in its own tmux session with your coding agent ready to go.

## How it works

1. **Add a feature** — creates a feature directory with shared context files
2. **Create worktrees** — spin up isolated branches under that feature using `git worktree`
3. **Open a worktree** — launches a tmux session with your coding agent attached
4. **Sync** — rebases all worktrees in a feature in dependency order

## Requirements

- [Go](https://go.dev/dl/) 1.26+
- [git](https://git-scm.com/)
- [tmux](https://github.com/tmux/tmux) (for `tws open`)
- A coding agent: [Claude Code](https://claude.ai/claude-code) (default), [OpenCode](https://opencode.ai), [Aider](https://aider.chat), or any CLI agent (configurable via `~/.config/tws/config.yaml`)

## Install

```sh
go install github.com/jdbencardinop/tesseraworkspaces/cmd/tws@latest
```

Or build from source:

```sh
git clone https://github.com/jdbencardinop/tesseraworkspaces.git
cd tesseraworkspaces
make install
```

## Usage

```sh
# Register a new feature workspace
tws add <feature>

# Create a worktree branch under a feature
tws new <feature> <branch> [--base <parent>]

# Open a worktree in a tmux session
tws open <feature> <branch>

# Rebase all worktrees in a feature (respects dependency order)
tws sync <feature>

# Show the branch dependency tree for a feature
tws stack <feature>
```

## Configuration

By default, tesseraworkspaces creates workspaces in a sibling directory next to your repo (e.g., `../myapp.tws/`). This can be customized via environment variable or config file.

See [docs/configuration.md](docs/configuration.md) for details.

## License

[MIT](LICENSE)
