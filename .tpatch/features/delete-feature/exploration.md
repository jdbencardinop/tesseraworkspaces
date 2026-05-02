# Exploration: delete-feature

## Relevant Files
- **New: `internal/cli/delete.go`**
- **`cmd/tws/main.go`** — add `case "delete"`

## Minimal Changeset
Two files. Load stack.yaml, iterate worktrees, git worktree remove, rm feature dir.
