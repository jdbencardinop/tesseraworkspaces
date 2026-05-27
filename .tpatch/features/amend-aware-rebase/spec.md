# Specification: amend-aware-rebase
## Minimal Changeset
1. `internal/stack.go` — LastBaseSHA field, UpdateBaseSHA, GetBaseSHA helpers
2. `internal/cli/sync_helpers.go` — --onto logic, SHA update after rebase
