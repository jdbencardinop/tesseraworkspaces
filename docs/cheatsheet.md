# tws cheat sheet

## Install

```sh
go install github.com/jdbencardinop/tesseraworkspaces/cmd/tws@latest

# Enable shell completions (zsh)
tws completion zsh > $(brew --prefix)/share/zsh/site-functions/_tws
```

## Quick start (one command)

```sh
cd ~/projects/myapp

# Create feature + branch + open agent — all in one
tws add auth -n auth-models --open

# With an existing remote branch
git fetch origin
tws add auth -n feature/auth-api --open

# With a custom template
tws add auth -n auth-models --template ~/templates/go-project --open

# Without opening (just setup)
tws add auth -n auth-models
```

## Create branches (stacked diffs)

```sh
tws new auth auth-models                              # selected repo's origin/HEAD
tws new auth auth-middleware --base auth-models        # stacks on auth-models
tws new auth auth-routes --base auth-middleware        # stacks on auth-middleware
tws new auth auth-tests --base auth-models             # diverges (parallel to middleware)
tws new auth release-check --base origin/release        # explicit remote ref
tws new auth wiki-docs --repo ../wiki --base master     # local master in wiki repo

# Result:
# (<default>)
# └── auth-models
#     ├── auth-middleware
#     │   └── auth-routes
#     └── auth-tests
```

## Migrate an existing branch

```sh
tws new auth my-existing-branch                   # auto-detects existing branch
tws new auth main --force                         # force if already checked out
```

## Work in a worktree

```sh
tws open auth auth-models              # cd + run agent (default)
tws open auth auth-models --tmux       # wrap in tmux session
tws open auth auth-models --no-agent   # just print the path
tws open                               # interactive picker (fzf if available)
tws open auth                          # pick branch within feature
tws open auth --feature-dir            # feature orchestrator directory
tws open auth --all                    # tmux: orchestrator + one window per worktree
```

## Decisions (cross-worktree communication)

```sh
# Record a decision (broadcast to all worktrees)
tws decide auth "Changed User.ID from string to uuid" --type breaking

# Record a targeted decision (only for a specific branch)
tws decide auth "Review the API surface" --type review --to auth-middleware

# Add details
tws decide auth "Added UserRepository" --type info \
  --details "Use internal.UserRepository instead of direct DB calls"

# View unread decisions (default — only shows new ones)
tws decisions show auth

# View all decisions (including already read)
tws decisions show auth --all

# Filter by source branch
tws decisions show auth --branch auth-models

# Show only decisions relevant to your branch
tws decisions show auth --mine

# Mark all as read
tws decisions ack auth
```

Decision types: `breaking` | `info` | `deprecation` | `review` | `question`

When you `tws open`, unread decisions are shown automatically:
```
  2 new decision(s) (1 for you) (run: tws decisions show auth)
```

## See what you have

```sh
tws list                     # all features and branches
tws stack auth               # dependency tree for a feature
tws doctor auth              # health checks (branch mismatch, dirty, etc.)
```

## Sync (rebase in dependency order)

```sh
tws sync auth                # fetches, then rebases parent→child
                             # if auth-models fails, middleware+routes are skipped
                             # archived branches synced via --update-refs or optimistic rebase
```

## Archive and restore

```sh
tws archive auth auth-middleware   # remove worktree, keep branch ref
tws new auth auth-middleware       # restore (idempotent, no stack.yaml duplicate)
```

## Clean up

```sh
tws delete auth              # removes all worktrees + feature dir
tws close auth auth-models   # kill tmux session for a worktree
```

## Context injection

```sh
# Files in inject/ are symlinked into every worktree
ls ../myapp.tws/auth/inject/       # CLAUDE.local.md, .claude/skills/, etc.

# Re-sync after adding new files to inject/
tws inject auth                    # all worktrees
tws inject auth auth-models        # single worktree

# Backfill templates into existing features
tws template sync auth --template ~/templates/base
tws template sync --all            # all features
```

## Configuration

```sh
tws config show                              # show resolved config
tws config set agent_command opencode        # change agent globally
tws config set use_tmux true --repo          # per-repo setting
tws config get agent_command                 # check current value

# Config files:
#   Global: ~/.config/tws/config.yaml
#   Per-repo: .tws/config.yaml
#   Env override: TWS_ROOT=/custom/path
```

## Rename

```sh
tws rename feature old-name new-name               # rename feature
tws rename branch auth old-branch new-branch        # rename branch + update refs
```

## Agent skills

```sh
tws init                        # install Claude + Copilot skills
tws init --agent claude         # Claude only
tws init --agent copilot        # Copilot only
tws init --force                # overwrite existing
```
