# Specification: worktree-context-injection
## Minimal Changeset
1. `internal/inject.go` — symlink logic
2. `internal/cli/add.go` — create inject/
3. `internal/cli/new.go` — inject on create
4. `internal/cli/open.go` — inject on open
5. New: `internal/cli/inject.go` — tws inject command
6. `internal/cli/root.go` — register
