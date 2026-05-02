# tws cheat sheet

## Install

```sh
go install github.com/jdbencardinop/tesseraworkspaces/cmd/tws@latest
```

## Setup a feature workspace

```sh
cd ~/projects/myapp          # navigate to your repo
tws add auth                 # creates ../myapp.tws/auth/
```

## Create branches (stacked diffs)

```sh
tws new auth auth-models                          # first branch (base: main)
tws new auth auth-middleware --base auth-models    # stacks on auth-models
tws new auth auth-routes --base auth-middleware    # stacks on auth-middleware
```

## Migrate an existing branch

```sh
tws new auth my-existing-branch                   # auto-detects existing branch
tws new auth main --force                         # force if already checked out
```

## Work in a worktree

```sh
tws open auth auth-models              # cd + run agent (default)
                                       # first time: runs `claude`
                                       # subsequent: runs `claude -c` (continue)
tws open auth auth-models --tmux       # wrap in tmux session
tws open auth auth-models --no-agent   # just print the path
```

## See what you have

```sh
tws list                     # all features and branches
tws stack auth               # dependency tree for a feature

# Example output of tws stack:
# (main)
# └── auth-models
#     └── auth-middleware
#         └── auth-routes
```

## Sync (rebase in dependency order)

```sh
tws sync auth                # fetches, then rebases parent→child
                             # if auth-models fails, middleware+routes are skipped
```

## Clean up

```sh
tws delete auth              # removes all worktrees + feature dir
                             # branches are preserved in git
```

## Configuration

```sh
# Override workspace root for this session
TWS_ROOT=/custom/path tws add feature

# Global config: ~/.config/tws/config.yaml
# workspaces:
#   /path/to/repo: /custom/workspace/path

# Default: workspace is at ../<repo-name>.tws/
# Fallback: ~/tws
```
