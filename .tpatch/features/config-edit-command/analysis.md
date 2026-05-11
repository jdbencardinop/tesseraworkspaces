# Analysis: config-edit-command
## Summary
Add tws config subcommand with show/set/get for editing config without hand-editing YAML.
## Affected Areas
- New: `internal/cli/config.go`
- `internal/config.go` — add SaveConfig helper
- `cmd/tws/main.go` — already handled by cobra root (just add subcommand)
