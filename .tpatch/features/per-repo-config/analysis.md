# Analysis: per-repo-config

## Summary

Add `.tws/config.yaml` in the repo root as an optional per-repo config. Per-repo values override global config. The file is committable so team members share the same settings.

## Resolution Order

1. Global config (`~/.config/tws/config.yaml`)
2. Per-repo config (`.tws/config.yaml`) — overrides global
3. Env var `TWS_ROOT` — overrides everything (workspace path only)
4. CLI flags — override everything

## Per-repo Config Fields

Only `agent_command` and `use_tmux` make sense per-repo. `workspaces` is global-only (it maps repo paths to workspace paths, which is inherently a global concern).

## Affected Areas

- `internal/config.go` — add `LoadRepoConfig()`, merge with global config in `LoadConfig()`
- `docs/configuration.md` — document per-repo config

## Acceptance Criteria

1. `.tws/config.yaml` in repo root is loaded and merged with global config
2. Per-repo values override global
3. Missing per-repo config is fine (global only)
4. Missing global config is fine (per-repo only)
5. Both missing is fine (defaults)
