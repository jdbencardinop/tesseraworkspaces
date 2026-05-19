# Analysis: decision-read-tracking

## Summary

Track which decisions each branch has seen. Store a `read-state.yaml` in the feature dir mapping branch names to last-read decision ID. Show only unread decisions by default, add `tws decisions ack` to mark as read.

## Data Model

```yaml
# ../myapp.tws/auth/read-state.yaml
branches:
  auth-models: 3        # last read decision ID
  auth-middleware: 1
```

## Changes

- `internal/decisions.go` — add ReadState struct, Load/Save, ack logic
- `internal/cli/decisions.go` — add `ack` subcommand, default to unread, `--all` for everything
- `internal/cli/open.go` — show unread count instead of total

## Acceptance Criteria

1. `tws decisions <feature>` shows only unread decisions by default
2. `tws decisions <feature> --all` shows everything
3. `tws decisions ack <feature>` marks all decisions as read for current branch
4. `tws open` shows unread count, not total
5. New worktrees start with last-read = 0 (all decisions are unread)
