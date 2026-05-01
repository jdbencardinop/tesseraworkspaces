# tesseraspaces

A CLI tool for creating feature-scoped workspaces with multiple git worktrees. Work on parallel branches or stacked diffs within a single feature, each in its own tmux session with your coding agent ready to go.

## How it works

1. **Add a feature** — creates a feature directory with shared context files
2. **Create worktrees** — spin up isolated branches under that feature using `git worktree`
3. **Open a worktree** — launches a tmux session with your editor and Claude Code attached
4. **Sync** — rebases all worktrees in a feature against `origin/main`

## Requirements

- [Go](https://go.dev/dl/) 1.26+
- [git](https://git-scm.com/)
- [tmux](https://github.com/tmux/tmux) (for `ts open`)

## Install

```sh
go install github.com/jdbencardinop/tesseraspaces/cmd/ts@latest
```

Or build from source:

```sh
git clone https://github.com/jdbencardinop/tesseraspaces.git
cd tesseraspaces
make install
```

## Usage

```sh
# Register a new feature workspace
ts add <feature>

# Create a worktree branch under a feature
ts new <feature> <branch>

# Open a worktree in a tmux session
ts open <feature> <branch>

# Rebase all worktrees in a feature against origin/main
ts sync <feature>
```

## Configuration

By default, tesseraspaces creates workspaces in a sibling directory next to your repo (e.g., `../myapp.ts/`). This can be customized via environment variable or config file.

See [docs/configuration.md](docs/configuration.md) for details.

## License

[MIT](LICENSE)
