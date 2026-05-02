# Analysis: pluggable-agent-command

## Summary

Replace hardcoded "claude" in `tws open` with a configurable agent command from `~/.config/tws/config.yaml`. The `-c` (continue) session detection is Claude-specific so it should only apply when the configured agent is claude.

## Affected Areas

- `internal/config.go` — add `AgentCommand` field, default to "claude"
- `internal/cli/open.go` — read agent command from config, conditionally apply `-c`
- `docs/configuration.md` — document `agent_command` config
- `README.md` — note Claude as default, mention configurability

## Acceptance Criteria

1. Default behavior unchanged (uses claude)
2. `agent_command` in config overrides the agent
3. Claude-specific `-c` only applies when agent is "claude"
4. Empty `agent_command` means no agent launched (just opens tmux in the dir)
