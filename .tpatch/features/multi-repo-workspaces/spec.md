# Specification: multi-repo-workspaces
## Minimal Changeset
1. `internal/stack.go` — Repo field, UniqueRepos
2. `internal/cli/new.go` — --repo flag
3. `internal/cli/sync.go` — per-repo fetch
4. `internal/cli/sync_helpers.go` — repo-aware archived rebase
