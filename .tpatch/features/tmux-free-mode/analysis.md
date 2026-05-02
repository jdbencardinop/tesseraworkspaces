# Analysis: tmux-free-mode

## Summary

Flip the default for `tws open` from tmux to plain mode (cd + exec agent). Add `--tmux` flag to opt-in to tmux wrapping. Add `--no-agent` to just cd without launching an agent. Add `use_tmux` config option for users who prefer tmux as default.

## Affected Areas

- `internal/cli/open.go` — rewrite to default to exec mode, tmux as opt-in
- `internal/config.go` — add `UseTmux` field
- `docs/configuration.md` — document `use_tmux` option
- `docs/cheatsheet.md` — update open examples

## Acceptance Criteria

1. `tws open feature branch` — cd + exec agent (default)
2. `tws open feature branch --tmux` — wrap in tmux session
3. `tws open feature branch --no-agent` — just cd, print path
4. `use_tmux: true` in config makes tmux the default
5. `--tmux`/`--no-tmux` override config
6. Claude -c detection still works in both modes
7. tmux not required unless --tmux or config enables it
