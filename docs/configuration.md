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
    worktrees/
      <branch-name>/
```
