# Exploration

## Critical Files

- `internal/cli/sync.go`, `sync_helpers.go`
- `internal/syncstate.go`
- new `internal/checkout_transaction.go`
- `internal/workspace.go`
- checkout integration tests under `internal/cli/`

## Reuse

- Stack TopoSort, GitBranch, explicit base resolution, amend-aware last_base_sha
- runValidation and final ancestry checks
- existing sync --continue/--abort UX

## Tests

Use real temporary Git repos. Cover clean/dirty/detached/preexisting rebase, lock contention/stale lock, successful sequential sync/restoration, parent conflict + continue through descendants, second conflict, abort restoration, validation failure, injected interruption after each persisted step, and paired external sync regression.
