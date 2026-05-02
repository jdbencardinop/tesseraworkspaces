# Specification: fix-initial-claude-session

## Acceptance Criteria

1. First `tws open` creates session and runs `claude` (no `-c`)
2. Subsequent `tws open` reattaches to existing session without re-running claude
3. No welcome message clutter

## Minimal Changeset

1. `internal/cli/open.go` — rewrite with proper has-session check
