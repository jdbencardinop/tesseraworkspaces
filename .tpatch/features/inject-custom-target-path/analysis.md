# Analysis: inject-custom-target-path
## Summary
Add --into flag to tws inject and a inject_into config option for the default target subdirectory. Default remains worktree root for backwards compatibility.
## Affected Areas
- `internal/config.go` — add InjectInto field
- `internal/inject.go` — accept target prefix
- `internal/cli/inject.go` — add --into flag, read config default
- `internal/cli/new.go` — pass inject target from config
- `internal/cli/open.go` — pass inject target from config
