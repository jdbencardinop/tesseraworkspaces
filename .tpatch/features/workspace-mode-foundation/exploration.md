# Exploration

## Critical Files

- `internal/config.go`: persisted config schema and global/per-repo merge
- `internal/paths.go`: current external resolution precedence and path helpers
- new `internal/workspace.go`: mode, resolved workspace, stable identity, capabilities
- `internal/paths_test.go`: existing path characterization
- new workspace-mode tests under `internal/`

## Reuse

- `MainRepoRoot` / `MainRepoRootIn`
- `DetectWorkspaceRoot`
- `ConfigPath` / `RepoConfigPath`
- current `resolveTwsRoot` precedence

## Test Focus

External mode output must remain byte-for-byte equivalent for all existing path scenarios. Checkout mode is parsed/resolved but has no lifecycle behavior in this slice.
