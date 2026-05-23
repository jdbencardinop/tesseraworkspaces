# Analysis: multi-repo-workspaces
## Summary
Add repo field to StackEntry. Branches without repo use the default (current repo). tws new --repo adds cross-repo worktrees. Sync fetches and rebases per-repo using worktree dirs for git context.
## Affected Areas
- `internal/stack.go` — add Repo field to StackEntry, add UniqueRepos helper
- `internal/cli/new.go` — add --repo flag, pass repo to createWorktree
- `internal/cli/sync.go` — fetch per-repo
- `internal/cli/sync_helpers.go` — use worktree dir for archived rebases
