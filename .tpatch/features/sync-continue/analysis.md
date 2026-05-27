# Analysis: sync-continue
## Summary
Persist sync state on conflict. tws sync --continue resumes. tws sync --abort cleans up. State file in feature dir.
## Affected Areas
- New: `internal/syncstate.go` — SyncState struct, Load/Save/Delete
- `internal/cli/sync.go` — --continue and --abort flags
- `internal/cli/sync_helpers.go` — write state on conflict, check state on start
