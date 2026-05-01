# tesseraspaces

A CLI tool for creating feature-scoped workspaces with multiple git worktrees. Work on parallel branches or stacked diffs within a single feature, each in its own tmux session with your coding agent ready to go.

## How it works

1. **Add a feature** — creates a feature directory with shared context files
2. **Create worktrees** — spin up isolated branches under that feature using [worktrunk](https://github.com/max-sixty/worktrunk)
3. **Open a worktree** — launches a tmux session with your editor and Claude Code attached
4. **Sync** — rebases all worktrees in a feature against `origin/main`

## Requirements

- [Go](https://go.dev/dl/) 1.26+
- [tmux](https://github.com/tmux/tmux)
- [worktrunk](https://github.com/max-sixty/worktrunk) (`wt` must be available on PATH)

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

## License

[MIT](LICENSE)
