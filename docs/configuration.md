# Configuration

tesseraspaces resolves the workspace root directory using the following priority chain:

| Priority | Source | Example |
|----------|--------|---------|
| 1 | `TS_ROOT` environment variable | `export TS_ROOT=/data/workspaces` |
| 2 | Global config (`~/.config/ts/config.yaml`) keyed by repo path | See below |
| 3 | Repo-relative sibling directory | `../myapp.ts/` |
| 4 | Global fallback | `~/ts` |

## Global config file

Location: `~/.config/ts/config.yaml`

```yaml
workspaces:
  /Users/you/projects/myapp: /data/workspaces/myapp
  /Users/you/projects/api: /custom/path/api-workspaces
```

Each key is the absolute path to a git repository. The value is the workspace root where features and worktrees are stored for that repo.

If no entry exists for the current repo, tesseraspaces defaults to a sibling directory named `<repo>.ts/` next to the repo.

## Environment variable

Set `TS_ROOT` to override all config resolution for the current session:

```sh
TS_ROOT=/tmp/scratch ts add my-feature
```

## Default behavior

When no config exists and no env var is set:

- **Inside a git repo** (`/Users/you/projects/myapp`): workspace root is `../myapp.ts/`
- **Outside a git repo**: workspace root falls back to `~/ts`

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
- **base**: the parent branch to rebase against during `ts sync`

`stack.yaml` is created automatically when you use `ts new`. The `--base` flag sets the parent:

```sh
ts new auth auth-models                    # base defaults to main
ts new auth auth-middleware --base auth-models
ts new auth auth-routes --base auth-middleware
```

`ts sync <feature>` rebases branches in topological order (parents first). If a rebase fails, all dependent branches are skipped.

`ts stack <feature>` prints the dependency tree:

```
(main)
└── auth-models
    └── auth-middleware
        └── auth-routes
```

Features without a `stack.yaml` fall back to rebasing all worktrees against `origin/main`.
