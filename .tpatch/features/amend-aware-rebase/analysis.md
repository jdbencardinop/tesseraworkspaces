# Analysis: amend-aware-rebase
## Summary
Track last_base_sha per branch in stack.yaml. On sync, use git rebase --onto when the base branch has changed (amended/rebased) to only replay unique commits. Update SHA after successful sync.
## Affected Areas
- `internal/stack.go` — add LastBaseSHA field to StackEntry, UpdateBaseSHA helper
- `internal/cli/sync_helpers.go` — use --onto when last_base_sha differs from current base
