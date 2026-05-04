# Configuration

tesseraworkspaces resolves the workspace root directory using the following priority chain:

| Priority | Source | Example |
|----------|--------|---------|
| 1 | `TWS_ROOT` environment variable | `export TWS_ROOT=/data/workspaces` |
| 2 | Global config (`~/.config/tws/config.yaml`) keyed by repo path | See below |
| 3 | Repo-relative sibling directory | `../myapp.tws/` |
| 4 | Global fallback | `~/tws` |

## Global config file

Location: `~/.config/tws/config.yaml`

```yaml
# Workspace paths (keyed by repo path)
workspaces:
  /Users/you/projects/myapp: /data/workspaces/myapp
  /Users/you/projects/api: /custom/path/api-workspaces

# Agent command launched by tws open (default: claude)
agent_command: claude

# Use tmux for tws open (default: false)
# Override per-invocation with --tmux or --no-tmux
use_tmux: false
```

### Agent command

The `agent_command` field controls what `tws open` runs in the worktree. Defaults to `claude`.

Examples:

```yaml
agent_command: claude          # default — includes -c auto-detection
agent_command: opencode        # OpenCode
agent_command: aider           # Aider
agent_command: codex            # Codex CLI
agent_command: claude-dev      # Claude Code dev build — -c auto-detection works
```

When the agent is `claude`, `claude-dev`, or `cc`, tws automatically detects existing Claude sessions and appends `-c` (continue) on subsequent opens.

Each key is the absolute path to a git repository. The value is the workspace root where features and worktrees are stored for that repo.

If no entry exists for the current repo, tesseraworkspaces defaults to a sibling directory named `<repo>.tws/` next to the repo.

## Environment variable

Set `TWS_ROOT` to override all config resolution for the current session:

```sh
TWS_ROOT=/tmp/scratch tws add my-feature
```

## Default behavior

When no config exists and no env var is set:

- **Inside a git repo** (`/Users/you/projects/myapp`): workspace root is `../myapp.tws/`
- **Outside a git repo**: workspace root falls back to `~/tws`

## Workspace layout

```
<workspace-root>/
  <feature-name>/
    FEATURE.md
    CLAUDE.local.md
    stack.yaml
    worktrees/
      <branch-name>/
```

## Branch stacks (stack.yaml)

Each feature can have a `stack.yaml` that tracks branch dependencies:

```yaml
branches:
  - name: auth-models
    base: main
  - name: auth-middleware
    base: auth-models
  - name: auth-routes
    base: auth-middleware
```

- **name**: the branch name (matches the worktree folder name)
- **base**: the parent branch to rebase against during `tws sync`

`stack.yaml` is created automatically when you use `tws new`. The `--base` flag sets the parent:

```sh
tws new auth auth-models                    # base defaults to main
tws new auth auth-middleware --base auth-models
tws new auth auth-routes --base auth-middleware
```

`tws sync <feature>` rebases branches in topological order (parents first). If a rebase fails, all dependent branches are skipped.

`tws stack <feature>` prints the dependency tree:

```
(main)
└── auth-models
    └── auth-middleware
        └── auth-routes
```

Features without a `stack.yaml` fall back to rebasing all worktrees against `origin/main`.

### Divergent stacks

Multiple branches can share the same parent, creating a tree structure:

```yaml
branches:
  - name: auth-models
    base: main
  - name: auth-middleware
    base: auth-models
  - name: auth-routes
    base: auth-middleware
  - name: auth-tests
    base: auth-models       # diverges from middleware
```

```sh
tws new auth auth-tests --base auth-models
```

```
(main)
└── auth-models
    ├── auth-middleware
    │   └── auth-routes
    └── auth-tests
```

Sync handles divergent stacks correctly — each lineage is rebased independently. A failure in `auth-middleware` skips `auth-routes` but does NOT skip `auth-tests` (different lineage).
