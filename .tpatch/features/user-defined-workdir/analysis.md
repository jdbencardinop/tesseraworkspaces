# Analysis: user-defined-workdir

## Summary

The workspace root is hardcoded in `internal/paths.go:TsRoot()` to `~/ts`. All commands (add, new, open, sync) derive paths from this single function. The change is well-isolated — `TsRoot()` is the only function that needs to become configurable, and all downstream path helpers already depend on it.

## Affected Areas

- `internal/paths.go` — `TsRoot()` must read from config instead of returning a hardcoded path
- New: `internal/config.go` — config loading/saving (where to store workspace root per repo or globally)
- `cmd/ts/main.go` — may need a `config` subcommand to let users set their workdir

## Compatibility

**Status**: safe

This is a greenfield project with no external consumers. The only contract is the `TsRoot()` return value, which all other code already consumes through the helper functions.

## Current Behavior

```
TsRoot() → ~/ts                     (hardcoded)
FeaturePath(f) → ~/ts/<f>
WorktreePath(f, b) → ~/ts/<f>/worktrees/<b>
```

## Desired Behavior

```
TsRoot() → resolved from (in priority order):
  1. TS_ROOT env var (session override)
  2. Per-repo config file (.ts/config or similar)
  3. Global config (~/.config/ts/config.yaml or similar)
  4. Default fallback: ~/ts
```

## Design Decisions (Resolved)

1. **Config format**: YAML
2. **Config location**: Global only — `~/.config/ts/config.yaml`, keyed by repo path. Per-repo config file deferred to a future feature.
3. **No `ts config` subcommand** for now — env var + file editing is sufficient.
4. **XDG**: yes, `~/.config/ts/`

## Default Workspace Resolution

```
TsRoot(repo) → resolved from (in priority order):
  1. TS_ROOT env var (session override, absolute path)
  2. Global config entry for this repo's path (~/.config/ts/config.yaml)
  3. Repo-relative default: ../<repo-name>.ts/  (sibling of repo dir)
  4. Global fallback: ~/ts
```

Example config.yaml:
```yaml
workspaces:
  /Users/jbencardino/projects/myapp: /custom/path/myapp.ts
```

Example defaults (no config, in /Users/jbencardino/projects/myapp):
```
FeaturePath("auth")  → ../myapp.ts/auth/
WorktreePath("auth", "login-fix") → ../myapp.ts/auth/worktrees/login-fix/
```

## Acceptance Criteria

1. `TsRoot()` resolves workdir following the 4-step priority chain above
2. A user can override via `TS_ROOT` env var
3. A user can set per-repo workdir in `~/.config/ts/config.yaml`
4. When no config exists, defaults to `../<repo-name>.ts/` if inside a repo, else `~/ts`
5. All commands (add, new, open, sync) respect the resolved root
6. No regressions — existing `~/ts` behavior preserved as global fallback
