# Analysis: tmux-session-management
## Summary
Add tws close to kill tmux sessions. Show tmux status in tws list. Warn on stale sessions in tws open.
## Affected Areas
- New: `internal/cli/close.go`
- `internal/cli/list.go` — show [tmux] status
- `internal/cli/open.go` — warn on stale session
- `internal/cli/root.go` — register close command
