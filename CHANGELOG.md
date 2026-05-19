# Changelog

## v0.7.3

- **Decision read tracking** — `tws decisions show` defaults to unread only, `tws decisions ack` marks all as read, `tws open` shows unread count

## v0.7.2

- **Tab completions** — all commands now provide feature/branch suggestions via `tws completion zsh/bash/fish`

## v0.7.1

- **Template `--template` flag** — `tws add --template <dir>` copies external templates into inject/
- **`tws template sync`** — backfill templates into existing features
- **Quick start** — registered for next release

## v0.7.0

- **Targeted messaging** — `tws decide --to <branch>` for directed decisions, `tws decisions show --mine`
- **Cross-worktree decisions** — `tws decide` and `tws decisions` for broadcasting design decisions

## v0.6.0

- **Context injection** — `inject/` directory with symlinks into worktrees, `tws inject` command
- **Health checks** — `tws doctor` for branch consistency, dirty worktree, and inject validation
- **Workspace templates** — `~/.config/tws/templates/` auto-populate inject/ on `tws add`
- **Rename** — `tws rename feature/branch` with stack.yaml ref updates

## v0.5.0

- **Config command** — `tws config show/set/get` with `--repo` flag
- **Close command** — `tws close` kills tmux sessions, stale session warnings
- **Bug fixes** — cwd after agent exit, auto-select messaging

## v0.4.0

- **Pluggable agent** — `agent_command` in config, Claude/OpenCode/Aider/Codex support
- **tmux-free mode** — direct exec by default, `--tmux` opt-in
- **Archive** — `tws archive` with smart sync, archived vs missing detection

## v0.3.0

- **Cobra migration** — proper flag parsing, auto-generated help, shell completions
- **Skill distribution** — `tws init` installs Claude + Copilot agent skills
- **Interactive picker** — `tws open` with fzf/numbered list, 0/1/2 arg modes
- **Divergent stacks** — DAG support formalized with tests
- **Per-repo config** — `.tws/config.yaml` overrides global config

## v0.2.0

- **Delete** — `tws delete` removes feature and worktrees
- **Versioned builds** — `--version`, `--help`, ldflags from git tags
- **Fix claude session** — auto-detect existing sessions for `-c` flag
- **List** — `tws list/ls` shows features and branches
- **Checkout existing** — `tws new` with existing branches, `--force` for checked-out
- **Lint fixes** — golangci-lint errcheck compliance

## v0.1.0

- **Initial release** — `tws add`, `tws new`, `tws open`, `tws sync`, `tws stack`
- **Configurable workspace** — TWS_ROOT env, global config, repo-relative sibling default
- **Auto-detect** — workspace and worktree detection with `.tws-workspace` marker
- **Git worktree** — direct `git worktree add`, no worktrunk dependency
- **Stacked diffs** — `stack.yaml` with dependency tracking and topo-sorted sync
