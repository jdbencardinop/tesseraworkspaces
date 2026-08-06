# Exploration

- new `internal/registry.go` and tests
- new `internal/cli/registry.go` and CLI tests
- `internal/cli/init.go` for `--register`
- `internal/workspace.go` identity/kind/marker helpers
- root command/completions and embedded skills

Tests use isolated HOME/XDG directories and real temporary Git repos/external/checkout workspaces. Cover permissions, atomic locking/concurrency, aliases/ambiguity, move/repair, missing/mismatch/prune, init registration, and no-registry external regressions.
