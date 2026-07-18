# Exploration: fix-sync-continue-descendants

## Relevant Files

- `internal/syncstate.go`: persisted state model
- `internal/cli/sync.go`: `handleSyncContinue`, abort/push flow
- `internal/cli/sync_helpers.go`: `syncWithStackFiltered`, conflict persistence, completion
- new `internal/cli/sync_continue_integration_test.go`

## Minimal Change

Make pending order authoritative, avoid terminally skipping deferred descendants, return an explicit sync result, preserve state on subsequent conflict/validation failure, verify all ancestry edges before state deletion and success output.
