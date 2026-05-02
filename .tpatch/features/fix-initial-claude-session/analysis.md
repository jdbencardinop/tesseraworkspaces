# Analysis: fix-initial-claude-session

## Summary

`tws open` has several bugs in the tmux/claude session handling. The `tmux has-session` check is ignored, so a new session is created every time. `claude -c` (continue) is always used but fails on first open since there's no session to continue. The send-keys call is also malformed.

## Affected Areas

- `internal/cli/open.go` — rewrite session creation logic

## Fix

1. Check if tmux session already exists via `tmux has-session`
2. If exists: just attach (session is already running claude)
3. If new: create session, run `claude` (not `-c`) in the worktree directory
4. Clean up the welcome messages — they clutter the session and are one-time noise
