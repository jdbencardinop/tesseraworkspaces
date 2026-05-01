# Exploration: user-defined-workdir

## Relevant Files

- **`internal/paths.go`** — Contains `TsRoot()` with hardcoded `~/ts`; needs refactoring to 4-step resolution
- **`internal/config.go`** — New file; `Config` struct, `LoadConfig()` reading `~/.config/ts/config.yaml`
- **`internal/exec.go`** — Likely has git command helpers; need `RepoRoot()` function added here (or new `git.go`)
- **`internal/cli/add.go`** — Calls `TsRoot()`/path helpers; verify no hardcoded paths
- **`internal/cli/new.go`** — Same
- **`internal/cli/open.go`** — Same
- **`internal/cli/sync.go`** — Same
- **`go.mod`** — Add `gopkg.in/yaml.v3` dependency

## Minimal Changeset

1. **Create `internal/config.go`** — `Config{Workspaces map[string]string}`, `LoadConfig()` reads `~/.config/ts/config.yaml`, returns zero-value if missing.

2. **Add `RepoRoot()` to `internal/exec.go`** — Shells out `git rev-parse --show-toplevel`, returns absolute path or error.

3. **Refactor `internal/paths.go`** — Replace hardcoded `TsRoot()` with 4-step resolution: (1) `$TS_ROOT` env, (2) config lookup by `RepoRoot()`, (3) `../<repo-name>.ts/` sibling, (4) `~/ts` fallback. Update `FeaturePath`/`WorktreePath` accordingly.

4. **Update `go.mod`** — Add `gopkg.in/yaml.v3`.

5. **Verify `internal/cli/*.go`** — Confirm all four commands use path helpers (no hardcoded `~/ts`). Likely no changes needed.
